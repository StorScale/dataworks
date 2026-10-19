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
     columns Trino's, and the rest as steps.

The project and its connection are removed after.
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

from lakekit import check, env, finish, sql

import pipelines_git as pg
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
OM = "http://openmetadata:8585/api/v1"
NAME, PORT, CONNECTION = "flowed", 8427, "flowed-git"
H = {"X-Platform-Request": "1"}
FLOWS = f"{PLATFORM}/api/projects/{NAME}/flows"

FLOW = {"name": "weekly", "schedule": "0 6 * * 1", "steps": [
    {"id": "orders", "kind": "input", "table": "orders", "x": 0, "y": 0},
    {"id": "real", "kind": "filter", "inputs": ["orders"], "where": "not test", "description": "No test rows", "x": 220, "y": 0},
    {"id": "by_region", "kind": "group", "inputs": ["real"], "by": ["region"],
     "aggregates": [{"function": "sum", "column": "amount", "as": "revenue"}, {"function": "count", "as": "orders"}], "x": 440, "y": 0},
    {"id": "out", "kind": "output", "inputs": ["by_region"], "table": "revenue_by_region",
     "checks": [{"column": "region", "test": "not_null"}, {"column": "region", "test": "unique"}], "x": 660, "y": 0},
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


def run(bob, dag):
    """A run of the DAG, once Airflow has it, to its end: (run, state)."""
    pipelines = lambda: (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])
    if not wait(lambda: any(p["id"] == dag for p in pipelines()), 600, every=10):
        errors = (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("importErrors") or []
        return None, f"not loaded: {json.dumps(errors)[:400]}"
    r = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{dag}/runs", headers=H, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return None, f"not started: HTTP {r.status_code}"
    state = lambda: next((x["state"] for p in pipelines() if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
    return run_id, wait(state, 900, every=10) or "still running"


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
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "A pipeline on the canvas: the flows suite's.",
                     "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags", "credentials": {"secretRef": CONNECTION}}}}
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        values = {"username": pg.USER, "password": pg.TOKEN}
        c = bob.post(f"{PLATFORM}/api/projects/{NAME}/connections", timeout=30, json={"name": CONNECTION, "kind": "git", "values": values})
        if c.status_code == 409:
            c = bob.put(f"{PLATFORM}/api/projects/{NAME}/connections/{CONNECTION}", timeout=30, json={"values": values})
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
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
        bad = {**FLOW, "steps": FLOW["steps"][:2] + [
            {"id": "suppliers", "kind": "input", "table": "suppliers", "x": 0, "y": 100},
            {"id": "joined", "kind": "join", "inputs": ["real", "suppliers"], "join": "left", "on": [{"left": "order_id", "right": "id"}], "x": 440, "y": 50},
            {"id": "out", "kind": "output", "inputs": ["joined"], "table": "t", "x": 660, "y": 0}]}
        refused = alice.post(f"{FLOWS}/compile", headers=H, json={"flow": bad}, timeout=60)
        why = (refused.json().get("steps", {}).get("joined", {}).get("error", "") if refused.ok else refused.text)
        check("a join on columns of different types is refused, saying which, before it runs",
              "join on real.order_id (integer) and suppliers.id (varchar): their types differ" in why, why[:200])
        preview = alice.post(f"{FLOWS}/preview", headers=H, json={"flow": FLOW, "step": "by_region"}, timeout=120)
        pj = preview.json() if preview.ok else {}
        rows = sorted(tuple(r) for r in pj.get("rows", []))
        check("she previews the group step on a sample, as herself: the rows and the SQL that made them",
              preview.ok and rows == [("EU", 22.5, 2), ("US", 7.0, 1)] and 'with "orders" as (' in pj.get("sql", ""), (preview.status_code, preview.text[:300]))

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
        unnamed = {**spec, "spec": {**spec["spec"], "pipelines": {k: v for k, v in spec["spec"]["pipelines"].items() if k != "credentials"}}}
        bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(unnamed), timeout=30)
        bob.delete(f"{PLATFORM}/api/projects/{NAME}/connections/{CONNECTION}", timeout=30)
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
