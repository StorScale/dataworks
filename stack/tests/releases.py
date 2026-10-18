"""A project as code, from its repo: docker compose run --rm test releases

The suite serves a project's repo (git over HTTP, as pipelines_git does)
with a storscale/ folder at two tags, and makes the project as v1 has it.
These check (use case 7.4):

  1. alice (an editor) plans v2: the plan says what it changes (carol added
     as a reader, a metric added, the description from this install's
     target's file); alice can't apply it (an administrator applies);
  2. bob plans it too, and can't apply his own plan; he applies alice's:
     the project's spec and metrics are v2's, and it's the release;
  3. the rollback: alice plans v1, bob applies it, the spec and metrics are
     v1's again;
  4. a ref the repo hasn't: the plan fails, saying so.

The project and its connection are removed after.
"""
import json
import os
import socket
import subprocess
import tempfile
import threading
from http.server import ThreadingHTTPServer

from lakekit import check, finish

import pipelines_git as pg
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME, PORT = "released", 8425
H = {"X-Platform-Request": "1"}

SEMANTIC = """semantic_model:
  name: regions
  node_relation: {database: iceberg, schema_name: released, alias: regions}
  defaults: {agg_time_dimension: sold_on}
  entities:
    - {name: region_row, type: primary, expr: id}
  dimensions:
    - {name: region, type: categorical}
    - {name: sold_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: regions_n, expr: "1", agg: sum}
---
metric:
  name: region_count
  description: How many regions.
  type: simple
  type_params: {measure: regions_n}
"""
MORE = """---
metric:
  name: region_rows
  description: How many rows.
  type: simple
  type_params: {measure: regions_n}
"""


def spec(repo, members):
    return {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "A project as code: the releases suite's.", "members": members, "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags",
                                   "credentials": {"secretRef": pg.SECRET}}}}


def plan(who, ref):
    """A plan of ref, by who, once it's checked out; or what went wrong."""
    r = who.post(f"{PLATFORM}/api/projects/{NAME}/plans", json={"ref": ref}, headers=H, timeout=60)
    if not r.ok:
        return {"state": f"HTTP {r.status_code}", "error": r.text[:200]}
    pid = r.json()["id"]

    def done():
        ps = (who.get(f"{PLATFORM}/api/projects/{NAME}/plans", timeout=60).json() or {}).get("plans", [])
        return next((p for p in ps if p["id"] == pid and p["state"] != "fetching"), None)
    return wait(done, 600, every=5) or {"id": pid, "state": "still fetching"}


def apply(who, p):
    return who.post(f"{PLATFORM}/api/projects/{NAME}/plans/{p.get('id')}/apply", headers=H, timeout=60)


def current(bob):
    p = (projects(bob) or {}).get(NAME) or {}
    s = (p.get("spec") or {}).get("spec") or {}
    model = bob.get(f"{PLATFORM}/api/projects/{NAME}/semantic", timeout=60).json()
    return ({m.get("user") or m.get("group"): m["role"] for m in s.get("members", [])}, s.get("description"),
            sorted(m["name"] for m in model.get("metrics", [])))


def main():
    root = tempfile.mkdtemp()
    pg.Git.root = root
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", f"{root}/{NAME}.git"], check=True)
    work = tempfile.mkdtemp()
    pg.git("init", "-q", "-b", "main", cwd=work)
    pg.git("remote", "add", "origin", f"{root}/{NAME}.git", cwd=work)
    here = socket.gethostbyname(socket.gethostname())
    repo = f"http://{here}:{PORT}/{NAME}.git"
    v1_members = [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}]
    v2_members = v1_members + [{"user": "carol", "role": "reader"}]
    targets = {f"storscale/targets/{t}.yaml": f"spec: {{description: 'Released, as {t} has it.'}}\n" for t in ("development", "production")}

    def release(members, semantic, tag):
        pg.push(work, {"dags/README.md": "The project's pipelines.\n",
                       "storscale/project.yaml": json.dumps(spec(repo, members), indent=2),
                       "storscale/semantic.yaml": semantic, **targets}, tag)
        pg.git("tag", tag, cwd=work)
        pg.git("push", "-q", "origin", tag, cwd=work)
    release(v1_members, SEMANTIC, "v1")
    release(v2_members, SEMANTIC + MORE, "v2")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), pg.Git)
    threading.Thread(target=server.serve_forever, daemon=True).start()

    bob, alice = platform("bob"), platform("alice")
    pg.put_secret(NAME)
    try:
        # The project as v1 has it (made by hand, as a project is before its first release).
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec(repo, v1_members)), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        r2 = bob.put(f"{PLATFORM}/api/projects/{NAME}/semantic", json={"yaml": SEMANTIC}, headers=H, timeout=60)
        check("bob makes the project as v1 has it", r.ok and ready is not None and r2.ok, (r.text[:200], r2.text[:200]))
        target = bob.get(f"{PLATFORM}/api/projects/{NAME}/plans", timeout=60).json().get("target")

        # 1. alice plans v2.
        p2 = plan(alice, "v2")
        changes = p2.get("changes") or []
        check("alice plans v2: carol added as a reader, a metric added, the description from the target's file",
              p2.get("state") == "ready" and "member added: carol as reader" in changes and "metric added: region_rows" in changes
              and "changed: description" in changes and len(p2.get("commit") or "") == 40, p2)
        r = apply(alice, p2)
        check("... and can't apply it: an administrator does", r.status_code == 403, f"HTTP {r.status_code}")

        # 2. bob's own plan he can't apply; alice's he does.
        mine = plan(bob, "v2")
        r = apply(bob, mine)
        check("bob can't apply a plan he made", mine.get("state") == "ready" and r.status_code == 403, (mine.get("state"), r.status_code))
        r = apply(bob, p2)
        got = current(bob)
        check("bob applies alice's: the project's spec and metrics are v2's",
              r.ok and got == ({"engineers": "editor", "alice": "editor", "carol": "reader"}, f"Released, as {target} has it.",
                               ["region_count", "region_rows"]), (r.text[:200], got))
        rel = bob.get(f"{PLATFORM}/api/projects/{NAME}/plans", timeout=60).json().get("release") or {}
        check("... and v2 is the release: planned by alice, applied by bob", (rel.get("ref"), rel.get("by"), rel.get("appliedBy")) == ("v2", "alice", "bob"), rel)
        r = apply(bob, mine)
        check("bob's own plan, made before v2 was applied, is refused", r.status_code in (403, 409), f"HTTP {r.status_code}")

        # 3. The rollback.
        p1 = plan(alice, "v1")
        r = apply(bob, p1)
        got = current(bob)
        check("the rollback: alice plans v1 (carol removed, the metric removed), bob applies it",
              "member removed: carol as reader" in (p1.get("changes") or []) and "metric removed: region_rows" in (p1.get("changes") or [])
              and r.ok and got[0] == {"engineers": "editor", "alice": "editor"} and got[2] == ["region_count"], (p1, r.text[:200], got))

        # 4. A ref that isn't there.
        bad = plan(alice, "v9")
        check("a ref the repo hasn't: the plan fails, saying so", bad.get("state") == "failed" and "v9" in (bad.get("error") or ""), bad)
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        pg.delete_secret(NAME)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
