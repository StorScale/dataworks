"""A pipeline built on the canvas, end to end: docker compose run --rm test flows

The suite serves a project's repo (git over HTTP, as pipelines_git does) and,
on the same address, the little of GitLab's API the canvas uses (branches,
trees, files, commits, merge requests: applied to the real repo), and makes a
project whose repo it is. These check (use cases 11.1 and 11.2):

  1. alice (a reader) builds a flow on the canvas: orders filtered, grouped
     by region, into a table; each step compiled to its columns and SQL
     before anything runs, and a join on columns of different types refused
     saying which; she previews a step on a sample, as herself;
  2. she can't save it; bob (an editor) saves it: the models with their
     headers, sources and schema, the DAG, on a branch of its own, with a
     merge request;
  3. merged, the project's Airflow builds it with dbt, as the project's
     pipelines: the table is there with the right numbers, and the catalog
     has it with its lineage from orders;
  4. bob changes one model by hand: the canvas shows that step as code, its
     columns Trino's, and the rest as steps;
  5. a metric step (in sales, whose metrics are defined) gets the semantic
     layer's SQL, so a flow's revenue is the company's;
  6. an upload (R11.3), in a project of its own without a repo (so its
     imports run where its Pipelines tab looks): bob puts a CSV in the landing
     area as himself (alice, a reader, can't), previewed with its types;
     imported, it's a table of the project's, which a flow takes as an input.

Both projects (and the first's connection) are removed after; nothing of
sales is changed (its metrics are read for the metric step).

"""
import json
import os
import socket
import subprocess
import tempfile
import threading
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

import requests

import pipelines_git as pg
from lakekit import check, env, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
OM = "http://openmetadata:8585/api/v1"
NAME, PORT, CONNECTION = "flowed", 8427, "flowed-git"
UPLOADED = "uploaded"  # the uploads' project: no repo, so its imports show where its Pipelines tab looks
H = {"X-Platform-Request": "1"}
FLOWS = f"{PLATFORM}/api/projects/{NAME}/flows"

FLOW = {"name": "weekly", "schedule": "0 6 * * 1", "steps": [
    {"id": "orders", "kind": "input", "table": "orders", "x": 0, "y": 0},
    {"id": "real", "kind": "filter", "inputs": ["orders"], "where": "not test", "description": "No test rows", "x": 220, "y": 0},
    {"id": "clean", "kind": "prepare", "inputs": ["real"], "description": "Regions as written", "x": 440, "y": 0,
     "processors": [{"column": "region", "do": "trim"}, {"column": "region", "do": "upper"}, {"column": "region", "do": "standardize", "args": ["EUROPE=EU, USA=US"]},
                    {"column": "region", "do": "length", "as": "region_len"}]},
    {"id": "mood", "kind": "llm", "inputs": ["clean"], "column": "region", "mode": "classify", "values": ["east", "west"], "name": "side",
     "prompt": "Which side of the Atlantic is the region on?", "description": "The platform asks the model", "x": 660, "y": 0},
    {"id": "by_region", "kind": "group", "inputs": ["mood"], "by": ["region"],
     "aggregates": [{"function": "sum", "column": "amount", "as": "revenue"}, {"function": "count", "as": "orders"}], "x": 880, "y": 0},
    {"id": "out", "kind": "output", "inputs": ["by_region"], "table": "revenue_by_region",
     "checks": [{"column": "region", "test": "not_null"}, {"column": "region", "test": "unique"}], "x": 1100, "y": 0},
]}


class Host(pg.Git):
    """The repo (git over HTTP, as pg.Git), and GitLab's API for it at /api/v4."""
    bare = ""
    mrs = []

    def gitlab(self):
        return self.path.startswith("/api/v4/")

    def do_GET(self):
        self.api() if self.gitlab() else self.serve()

    def do_HEAD(self):
        self.api()

    def do_POST(self):
        self.api() if self.gitlab() else self.serve()

    def answer(self, code, body=None, raw=None):
        data = b"" if self.command == "HEAD" else (raw if raw is not None else json.dumps(body if body is not None else {}).encode())
        self.send_response(code)
        self.send_header("Content-Type", "text/plain" if raw is not None else "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def git(self, *args):
        return subprocess.run(["git", "--git-dir", Host.bare, *args], capture_output=True, text=True)

    def has(self, ref, path=""):
        return self.git("cat-file", "-e", f"refs/heads/{ref}" + (f":{path}" if path else "")).returncode == 0

    def api(self):
        if self.headers.get("PRIVATE-TOKEN") != pg.TOKEN:
            return self.answer(401, {"message": "401 Unauthorized"})
        u = urlparse(self.path)
        path, q = unquote(u.path).removeprefix(f"/api/v4/projects/{NAME}"), parse_qs(u.query)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}") if self.command == "POST" else {}
        ref = q.get("ref", [""])[0]
        if path == "":
            return self.answer(200, {"default_branch": "main"})
        if path == "/repository/branches":
            out = self.git("for-each-ref", "--format=%(refname:short)", "refs/heads/").stdout.split()
            return self.answer(200, [{"name": b} for b in out if q.get("search", [""])[0] in b])
        if path.startswith("/repository/branches/"):
            return self.answer(200 if self.has(path.removeprefix("/repository/branches/")) else 404, {})
        if path == "/repository/tree":
            if not self.has(ref):
                return self.answer(404, {"message": "404 Tree Not Found"})
            out = self.git("ls-tree", "-r", "--name-only", f"refs/heads/{ref}", "--", q.get("path", [""])[0] + "/").stdout.split("\n")
            entries, dirs = [], set()
            for f in filter(None, out):
                entries.append({"path": f, "type": "blob"})
                d = os.path.dirname(f)
                while d and d not in dirs and d != q.get("path", [""])[0]:
                    dirs.add(d)
                    entries.append({"path": d, "type": "tree"})
                    d = os.path.dirname(d)
            return self.answer(200, entries)
        if path.startswith("/repository/files/") and path.endswith("/raw"):
            f = path.removeprefix("/repository/files/").removesuffix("/raw")
            if not self.has(ref, f):
                return self.answer(404, {"message": "404 File Not Found"})
            return self.answer(200, raw=self.git("show", f"refs/heads/{ref}:{f}").stdout.encode())
        if path.startswith("/repository/files/"):
            return self.answer(200 if self.has(ref, path.removeprefix("/repository/files/")) else 404, {})
        if path == "/repository/commits":
            work = tempfile.mkdtemp()
            pg.git("clone", "-q", Host.bare, work, cwd="/")
            pg.git("checkout", "-q", "-B", body["branch"], "origin/" + (body.get("start_branch") or body["branch"]), cwd=work)
            for a in body["actions"]:
                target = os.path.join(work, a["file_path"])
                if a["action"] == "delete":
                    os.remove(target)
                    continue
                os.makedirs(os.path.dirname(target), exist_ok=True)
                with open(target, "w") as f:
                    f.write(a["content"])
            pg.git("add", "-A", cwd=work)
            pg.git("commit", "-q", "-m", body["commit_message"], cwd=work)
            pg.git("push", "-q", "origin", body["branch"], cwd=work)
            return self.answer(201, {"id": pg.git("rev-parse", "HEAD", cwd=work)})
        if path == "/merge_requests" and self.command == "GET":
            src = q.get("source_branch", [""])[0]
            return self.answer(200, [m for m in Host.mrs if not src or m["source_branch"] == src])
        if path == "/merge_requests":
            mr = {**body, "iid": len(Host.mrs) + 1, "web_url": f"http://gitlab.example/{NAME}/-/merge_requests/{len(Host.mrs) + 1}"}
            Host.mrs.append(mr)
            return self.answer(201, mr)
        self.answer(404, {"message": "404 Not Found"})


def merge(work, branch):
    """The merge request, merged (as someone would in GitLab)."""
    pg.git("fetch", "-q", "origin", cwd=work)
    pg.git("checkout", "-q", "main", cwd=work)
    pg.git("merge", "-q", "--no-edit", f"origin/{branch}", cwd=work)
    pg.git("push", "-q", "origin", "main", cwd=work)
    pg.git("push", "-q", "origin", "--delete", branch, cwd=work)  # as GitLab does when the merge request says so


def run(bob, dag, project=NAME):
    """A run of the DAG, once Airflow has it, to its end: (run, state)."""
    pipelines = lambda: (bob.get(f"{PLATFORM}/api/projects/{project}/pipelines", timeout=60).json() or {}).get("pipelines", [])
    if not wait(lambda: any(p["id"] == dag for p in pipelines()), 600, every=10):
        errors = (bob.get(f"{PLATFORM}/api/projects/{project}/pipelines", timeout=60).json() or {}).get("importErrors") or []
        return None, f"not loaded: {json.dumps(errors)[:400]}"
    # A DAG with a schedule gets a run for the latest past interval as soon as Airflow has it:
    # let it finish, so the manual run is the only one building the table.
    busy = lambda: any(x["state"] in ("running", "queued") for p in pipelines() if p["id"] == dag for x in p.get("runs", []))
    wait(lambda: None if busy() else True, 600, every=10)
    r = bob.post(f"{PLATFORM}/api/projects/{project}/pipelines/{dag}/runs", headers=H, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return None, f"not started: HTTP {r.status_code}"
    state = lambda: next((x["state"] for p in pipelines() if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
    done = wait(state, 900, every=10) or "still running"
    if done != "success":  # why, in the words of what ran
        r = bob.get(f"{PLATFORM}/api/projects/{project}/pipelines/{dag}/runs/{requests.utils.quote(run_id, safe='')}/log", timeout=60)
        done += ": " + "\n".join((r.json().get("log", "") if r.ok else r.text).splitlines()[-60:])
    return run_id, done


def catalog():
    """A session on OpenMetadata's API, as the platform's own account (as the catalog suite reads it)."""
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    r.raise_for_status()
    s = requests.Session()
    s.headers["Authorization"] = f"Bearer {r.json()['access_token']}"
    return s


def lineage(om):
    r = om.get(f"{OM}/lineage/table/name/trino.iceberg.{NAME}.revenue_by_region?upstreamDepth=1&downstreamDepth=0", timeout=30)
    if not r.ok:
        return None
    names = {n["id"]: n["fullyQualifiedName"] for n in r.json().get("nodes", [])}
    return [names.get(e["fromEntity"], "?") for e in r.json().get("upstreamEdges", [])] or None


def main():
    root = tempfile.mkdtemp()
    pg.Git.root, Host.bare = root, f"{root}/{NAME}.git"
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", Host.bare], check=True)
    work = tempfile.mkdtemp()
    pg.git("init", "-q", "-b", "main", cwd=work)
    pg.git("remote", "add", "origin", Host.bare, cwd=work)
    pg.push(work, {"dags/README.md": "The project's pipelines.\n"}, "the repo")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Host)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    repo = f"http://{socket.gethostbyname(socket.gethostname())}:{PORT}/{NAME}.git"

    bob, alice = platform("bob"), platform("alice")
    uploaded = False
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "A pipeline on the canvas: the flows suite's.",
                     "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {},
                     "llm": {"budget": 1},   # the llm step asks on the project's key
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags", "credentials": {"secretRef": CONNECTION}}}}
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        values = {"username": pg.USER, "password": pg.TOKEN}
        c = bob.post(f"{PLATFORM}/api/projects/{NAME}/connections", timeout=30, json={"name": CONNECTION, "kind": "git", "values": values})
        if c.status_code == 409:
            c = bob.put(f"{PLATFORM}/api/projects/{NAME}/connections/{CONNECTION}", timeout=30, json={"values": values})
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "DROP TABLE IF EXISTS orders", "iceberg", NAME)  # a run before, on a cluster that keeps a deleted project's tables
        sql("bob", "DROP TABLE IF EXISTS suppliers", "iceberg", NAME)  # a run before, on a cluster that keeps a deleted project's tables
        sql("bob", "CREATE TABLE orders AS SELECT * FROM (VALUES (1, 'EU', DOUBLE '10.0', false), (2, 'EU', DOUBLE '12.5', false), (3, 'US', DOUBLE '7.0', false), "
                   "(4, 'US', DOUBLE '100.0', true)) AS t (order_id, region, amount, test)", "iceberg", NAME)
        sql("bob", "CREATE TABLE suppliers AS SELECT * FROM (VALUES ('1', 'Acme'), ('2', 'Globex')) AS t (id, name)", "iceberg", NAME)
        check("bob makes a project with a repo and its orders table", r.ok and c.ok and ready is not None, (r.text[:200], c.text[:200]))

        # 1. alice builds the flow: compiled before anything runs; a preview as herself.
        compiled = alice.post(f"{FLOWS}/compile", headers=H, json={"flow": FLOW}, timeout=60)
        cj = compiled.json() if compiled.ok else {}
        cols = [(c["name"], c["type"]) for c in (cj.get("steps", {}).get("by_region", {}).get("columns") or [])]
        check("alice's flow compiles: each step's columns and SQL, before anything runs",
              compiled.ok and cj.get("errors") == [] and cols == [("region", "varchar"), ("revenue", "double"), ("orders", "bigint")]
              and "group by \"region\"" in cj.get("steps", {}).get("by_region", {}).get("sql", ""), (compiled.status_code, compiled.text[:400]))
        clean = cj.get("steps", {}).get("clean", {})
        check("... the prepare step's processors, in order, typed: trim and upper case in place, a length as a new column",
              [(c["name"], c["type"]) for c in (clean.get("columns") or [])][-2:] == [("test", "boolean"), ("region_len", "bigint")]
              and 'upper(trim("region"))' in clean.get("sql", "") and "when upper(trim(\"region\")) = 'EUROPE' then 'EU'" in clean.get("sql", "")
              and any(d["name"] == "standardize" for d in cj.get("processors") or []), (clean.get("columns"), clean.get("sql", "")[:300], len(cj.get("processors") or [])))
        bad = {**FLOW, "steps": FLOW["steps"][:2] + [
            {"id": "suppliers", "kind": "input", "table": "suppliers", "x": 0, "y": 100},
            {"id": "joined", "kind": "join", "inputs": ["real", "suppliers"], "join": "left", "on": [{"left": "order_id", "right": "id"}], "x": 440, "y": 50},
            {"id": "out", "kind": "output", "inputs": ["joined"], "table": "t", "x": 660, "y": 0}]}
        refused = alice.post(f"{FLOWS}/compile", headers=H, json={"flow": bad}, timeout=60)
        why = (refused.json().get("steps", {}).get("joined", {}).get("error", "") if refused.ok else refused.text)
        check("a join on columns of different types is refused, saying which, before it runs",
              "join on real.order_id (integer) and suppliers.id (varchar): their types differ" in why, why[:200])
        asked = wait(lambda: (lambda r: r if r.ok else None)(alice.post(f"{FLOWS}/preview", headers=H, json={"flow": FLOW, "step": "mood"}, timeout=120)), 180, every=10)
        aj = asked.json() if asked is not None and asked.ok else {}
        sides = [r[-1] for r in aj.get("rows", [])]
        check("she previews the llm step: the model is asked about a sample on the project's key, its answers a column, the cost said",
              asked is not None and aj.get("columns", [])[-1:] == ["side"] and sides and all(x == "east" for x in sides) and (aj.get("cost") or 0) > 0 and aj.get("batches") == 1,
              (asked.status_code if asked is not None else None, (asked.text if asked is not None else "")[:300]))
        preview = alice.post(f"{FLOWS}/preview", headers=H, json={"flow": FLOW, "step": "by_region"}, timeout=120)
        pj = preview.json() if preview.ok else {}
        rows = sorted(tuple(r) for r in pj.get("rows", []))
        check("she previews the group step on a sample, as herself: the rows and the SQL that made them",
              preview.ok and rows == [("EU", 22.5, 2), ("US", 7.0, 1)] and 'with "orders" as (' in pj.get("sql", ""), (preview.status_code, preview.text[:300]))

        # A metric step on the semantic layer's definitions (in sales, which has them): its SQL is the company's revenue.
        metric = {"name": "rev", "steps": [{"id": "revenue", "kind": "metric", "metrics": ["revenue"], "by": ["order__region"], "x": 0, "y": 0},
                                           {"id": "out", "kind": "output", "inputs": ["revenue"], "table": "revenue_by_region", "x": 220, "y": 0}]}
        m = alice.post(f"{PLATFORM}/api/projects/sales/flows/compile", headers=H, json={"flow": metric}, timeout=120)
        mj = m.json() if m.ok else {}
        rev = mj.get("steps", {}).get("revenue", {})
        names = [c["name"] for c in (rev.get("columns") or [])]
        check("a metric step's SQL is the semantic layer's revenue, its columns Trino's",
              m.ok and mj.get("errors") == [] and "iceberg.sales.orders" in rev.get("sql", "") and "revenue" in names and "order__region" in names
              and " LIMIT " not in rev.get("sql", "").upper(), (m.status_code, m.text[:400]))

        # 6. An upload, in a project of its own (one without a repo, whose imports run in
        # the platform's Airflow, where its Pipelines tab looks): into the landing area as
        # the person, previewed, imported, an input.
        up_spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": UPLOADED},
                   "spec": {"description": "Uploads: the flows suite's.", "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}],
                            "tables": {}, "files": {}, "pipelines": {"serviceAccount": "airflow-pipelines"}}}
        made = bob.put(f"{PLATFORM}/api/projects/{UPLOADED}", data=json.dumps(up_spec), timeout=30)
        uploaded = made.ok
        wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(UPLOADED)), 900, every=10)
        SU = f"{PLATFORM}/api/projects/{UPLOADED}/uploads"
        csv = "Supplier ID,Name,Rating\n1,Acme,4.5\n2,Globex,3\n"
        hers = alice.post(SU, headers=H, files={"file": ("suppliers.csv", csv, "text/csv")}, timeout=60)
        his = bob.post(SU, headers=H, files={"file": ("suppliers.csv", csv, "text/csv")}, timeout=60)
        uj = his.json() if his.ok else {}
        types = [(c["name"], c["type"]) for c in uj.get("preview", {}).get("columns", [])]
        listed = (alice.get(SU, timeout=30).json() or {}).get("uploads", [])
        check("bob uploads a CSV to a project's landing area as himself (alice, a reader, can't): previewed with its types, listed",
              made.ok and hers.status_code == 403 and his.ok and types == [("supplier_id", "bigint"), ("name", "varchar"), ("rating", "double")]
              and uj.get("table") == "suppliers" and any(u["name"] == "suppliers.csv" for u in listed), (hers.status_code, hers.text[:200], his.status_code, his.text[:300]))
        imp = bob.put(f"{PLATFORM}/api/projects/{UPLOADED}/imports/uploads", headers=H, timeout=30,
                      json={"source": "files", "schedule": "0 0 1 1 *", "files": {"tables": [{"name": "suppliers", "file": "suppliers.csv"}]}})
        wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(UPLOADED)), 600, every=10)
        run_id, state = run(bob, f"import_{UPLOADED}_uploads", project=UPLOADED)
        got = sorted(tuple(r) for r in sql("bob", "SELECT supplier_id, name, rating FROM uploads_suppliers", "iceberg", UPLOADED)) if state == "success" else state
        check("imported (the files import, run as the project's pipelines), it's a table of the project's", imp.ok and got == [(1, "Acme", 4.5), (2, "Globex", 3.0)], (imp.status_code, imp.text[:200], run_id, got))
        as_input = alice.post(f"{PLATFORM}/api/projects/{UPLOADED}/flows/compile", headers=H, timeout=60, json={"flow": {"name": "sup", "steps": [
            {"id": "sup", "kind": "input", "table": "uploads_suppliers", "x": 0, "y": 0}, {"id": "out", "kind": "output", "inputs": ["sup"], "table": "sup", "x": 200, "y": 0}]}})
        in_cols = [c["name"] for c in ((as_input.json() if as_input.ok else {}).get("steps", {}).get("sup", {}).get("columns") or [])]
        check("... which a flow takes as an input, its columns known", as_input.ok and "supplier_id" in in_cols and "rating" in in_cols, (as_input.status_code, as_input.text[:300]))

        # 2. Saved by bob: a branch, the files, a merge request.
        hers = alice.put(f"{FLOWS}/weekly", headers=H, json={"flow": FLOW}, timeout=60)
        his = bob.put(f"{FLOWS}/weekly", headers=H, json={"flow": FLOW}, timeout=60)
        out = his.json() if his.ok else {}
        files = ""
        if his.ok:
            pg.git("fetch", "-q", "origin", cwd=work)
            files = pg.git("ls-tree", "-r", "--name-only", "origin/storscale/flow-weekly", cwd=work)
            real = pg.git("show", "origin/storscale/flow-weekly:dags/dbt/models/flows/weekly/weekly__real.sql", cwd=work)
        else:
            real = ""
        listed = alice.get(FLOWS, timeout=30).json().get("flows", []) if his.ok else []
        check("alice (a reader) can't save it; bob saves it: the models with their headers, the DAG, a merge request",
              hers.status_code == 403 and his.ok and out.get("branch") == "storscale/flow-weekly" and len(Host.mrs) == 1
              and all(f in files for f in ("dags/dbt/dbt_project.yml", "dags/dbt/models/flows/weekly/weekly__real.sql", "dags/dbt/models/flows/weekly/revenue_by_region.sql",
                                           "dags/dbt/models/flows/weekly/sources.yml", "dags/dbt/models/flows/weekly/schema.yml", "dags/flow_weekly.py"))
              and real.startswith("-- storscale-step:\n--   kind: filter") and "{{ ref('weekly__orders') }}" in real
              and any(f["name"] == "weekly" and f.get("branch") == "storscale/flow-weekly" for f in listed),
              (hers.status_code, his.text[:300], files, real[:200]))

        # 3. Merged, built: the table, its numbers, its lineage in the catalog.
        merge(work, "storscale/flow-weekly")
        run_id, state = run(bob, "flow_weekly")
        table = sorted(tuple(r) for r in sql("bob", "SELECT region, revenue, orders FROM revenue_by_region", "iceberg", NAME)) if state == "success" else state
        check("merged, the project's Airflow builds it with dbt: the table is there, with its numbers", table == [("EU", 22.5, 2), ("US", 7.0, 1)], (run_id, table))
        om = catalog()
        up = wait(lambda: lineage(om), 420, every=15) if state == "success" else None
        check("... and the catalog has it, its lineage from orders", bool(up) and any(u.endswith(f".{NAME}.orders") for u in up), up)

        # 4. bob changes a model by hand: a code step from then on.
        pg.git("fetch", "-q", "origin", cwd=work)
        pg.git("checkout", "-q", "main", cwd=work)
        pg.git("reset", "-q", "--hard", "origin/main", cwd=work)
        model = os.path.join(work, "dags/dbt/models/flows/weekly/weekly__real.sql")
        with open(model) as f:
            text = f.read()
        with open(model, "w") as f:
            f.write(text.replace("where not test", "where not test and amount > 8"))
        pg.git("commit", "-q", "-am", "Only real orders over 8", cwd=work)
        pg.git("push", "-q", "origin", "main", cwd=work)
        v = bob.get(f"{FLOWS}/weekly", timeout=120)
        vj = v.json() if v.ok else {}
        steps = {s["id"]: s for s in vj.get("flow", {}).get("steps", [])}
        real_cols = [c["name"] for c in (vj.get("steps", {}).get("real", {}).get("columns") or [])]
        check("bob changes one model by hand: the canvas shows that step as code (its columns Trino's), the rest as steps",
              v.ok and steps.get("real", {}).get("kind") == "code" and "amount > 8" in steps.get("real", {}).get("sql", "")
              and steps.get("real", {}).get("inputs") == ["orders"] and real_cols == ["order_id", "region", "amount", "test"]
              and steps.get("by_region", {}).get("kind") == "group" and vj.get("errors") == [], (v.status_code, v.text[:400]))
    finally:
        if uploaded:
            bob.delete(f"{PLATFORM}/api/projects/{UPLOADED}/uploads/suppliers.csv", headers=H, timeout=30)
            bob.delete(f"{PLATFORM}/api/projects/{UPLOADED}", headers=H, timeout=30)
        unnamed = {**spec, "spec": {**spec["spec"], "pipelines": {k: v for k, v in spec["spec"]["pipelines"].items() if k != "credentials"}}}
        bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(unnamed), timeout=30)
        bob.delete(f"{PLATFORM}/api/projects/{NAME}/connections/{CONNECTION}", timeout=30)
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
