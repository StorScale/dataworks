"""A merge request on a data branch, end to end: docker compose run --rm test reviews

The suite serves a project's repo (git over HTTP, as pipelines_git does) and
the little of GitLab's API the platform uses (merge requests, commit
statuses, notes), and makes a project, reviewed, whose repo has a dbt model.
These check (use case 7.3):

  1. production's model is built by its DAG (dbt, as the project's pipelines);
  2. a merge request changing the model gets a data branch: the platform
     builds the change there (the model with a third row), and tells GitLab
     (a status, success; a note with the model's rows on the branch and in
     production, and a sample of those that differ), while production's
     table is as it was; a new commit updates that note, not another;
  3. bob (an editor) reads the branch's table through its catalog;
  4. the merge request closed, its branch's catalog goes.

The project, its connection and its tables are removed after.
"""
import json
import os
import socket
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote

import requests

import pipelines_git as pg
from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME, PORT, API_PORT = "reviewed", 8423, 8424
WEBHOOK_TOKEN = "webhook-" + os.urandom(8).hex()
MODEL = "select id, region, date '2026-10-01' as sold_on from (values {rows}) as t (id, region)\n"
# A metric on the model, so a merge request's note says how it moved.
SEMANTIC = """semantic_model:
  name: regions
  node_relation: {database: iceberg, schema_name: reviewed, alias: regions}
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
TESTS = """
---
tests:
  - name: regions_counted
    metrics: [region_count]
    expect: {rows: 1}
"""

DBT_DAG = '''"""Production's dbt models."""
import pendulum

from airflow.sdk import dag
from storscale import dbt


@dag(schedule=None, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False)
def reviewed_dbt():
    dbt.build(project="reviewed", path="dbt")


reviewed_dbt()
'''


class GitLab(BaseHTTPRequestHandler):
    """GitLab's API for the repo reviewed/dags: its merge requests (as the suite
    opens and closes them), the statuses and notes the platform sends."""
    mrs = []        # open ones
    statuses = []   # (sha, state, description)
    notes = {}      # id -> (iid, body)
    signed = []     # signed in with the repo's token, each call

    def log_message(self, *args):
        pass

    def answer(self, code, body=None):
        data = json.dumps(body if body is not None else {}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        GitLab.signed.append(self.headers.get("PRIVATE-TOKEN") == pg.TOKEN)
        path = unquote(self.path).split("?")[0]
        if path.endswith("/notes"):
            iid = int(path.split("/")[-2])
            return self.answer(200, [{"id": k, "body": b} for k, (i, b) in sorted(GitLab.notes.items()) if i == iid])
        if path == f"/api/v4/projects/{NAME}/dags/merge_requests":
            return self.answer(200, GitLab.mrs)
        self.answer(404, {"message": "404 Not Found"})

    def do_POST(self):
        GitLab.signed.append(self.headers.get("PRIVATE-TOKEN") == pg.TOKEN)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        path = unquote(self.path)
        if "/statuses/" in path:
            GitLab.statuses.append((path.rsplit("/", 1)[1], body.get("state"), body.get("description", ""), body.get("name", "")))
            return self.answer(201, {})
        if path.endswith("/notes"):
            k = len(GitLab.notes) + 1
            GitLab.notes[k] = (int(path.split("/")[-2]), body.get("body", ""))
            return self.answer(201, {"id": k})
        self.answer(404, {"message": "404 Not Found"})

    def do_PUT(self):
        GitLab.signed.append(self.headers.get("PRIVATE-TOKEN") == pg.TOKEN)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        k = int(unquote(self.path).split("?")[0].rsplit("/", 1)[1])
        if k not in GitLab.notes:
            return self.answer(404, {"message": "404 Not found"})
        GitLab.notes[k] = (GitLab.notes[k][0], body.get("body", ""))
        self.answer(200, {"id": k})


def run_dag(bob, dag):
    r = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{dag}/runs", headers={"X-Platform-Request": "1"}, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return f"not started: HTTP {r.status_code}"

    def state():
        ps = (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])
        return next((x["state"] for p in ps if p["id"] == dag for x in p.get("runs", [])
                     if x["id"] == run_id and x["state"] in ("success", "failed")), None)
    return wait(state, 600, every=10) or "still running"


def rows(catalog):
    try:
        return [list(r) for r in sql("bob", "SELECT id, region FROM regions ORDER BY id", catalog, NAME)]
    except Exception as e:  # noqa: BLE001
        return str(e)[:160]


def main():
    root = tempfile.mkdtemp()
    pg.Git.root = root
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", f"{root}/{NAME}.git"], check=True)
    # The review fetches the merge request's commit by its hash.
    subprocess.run(["git", "-C", f"{root}/{NAME}.git", "config", "uploadpack.allowReachableSHA1InWant", "true"], check=True)
    work = tempfile.mkdtemp()
    pg.git("init", "-q", "-b", "main", cwd=work)
    pg.git("remote", "add", "origin", f"{root}/{NAME}.git", cwd=work)
    dbt_files = {"dbt/dbt_project.yml": "name: reviewed\nversion: '1.0'\nconfig-version: 2\nprofile: storscale\nmodel-paths: [models]\n"
                                        "models:\n  reviewed:\n    +materialized: table\n",
                 "dbt/models/schema.yml": "version: 2\nmodels:\n  - name: regions\n    columns:\n      - name: id\n        tests: [unique, not_null]\n"}
    pg.push(work, {"dags/reviewed_dbt.py": DBT_DAG, **{f"dags/{k}": v for k, v in dbt_files.items()},
                   "dags/dbt/models/regions.sql": MODEL.format(rows="(1, 'EU'), (2, 'US')"),
                   # The project as code (7.4): its semantic model with a test (12.3), reviewed with the change.
                   "storscale/semantic.yaml": SEMANTIC + TESTS}, "production's model")
    here = socket.gethostbyname(socket.gethostname())
    git_server = ThreadingHTTPServer(("0.0.0.0", PORT), pg.Git)
    api_server = ThreadingHTTPServer(("0.0.0.0", API_PORT), GitLab)
    for s in (git_server, api_server):
        threading.Thread(target=s.serve_forever, daemon=True).start()

    bob = platform("bob")
    # The repo's token, and the token GitLab's webhook is sent with.
    if pg.k8s.in_cluster():
        pg.k8s.put_secret(pg.SECRET, {"username": pg.USER, "password": pg.TOKEN, "webhookToken": WEBHOOK_TOKEN},
                          labels={"platform.storscale.io/connection": NAME})
    else:
        pg.s3_root().put_object(Bucket="storscale-platform", Key=f"connections/{NAME}/{pg.SECRET}.json",
                                Body=json.dumps({"username": pg.USER, "password": pg.TOKEN, "webhookToken": WEBHOOK_TOKEN}).encode())
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "Merge requests on data branches: the reviews suite's.",
                     "members": [{"group": "engineers", "role": "editor"}], "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": f"http://{here}:{PORT}/{NAME}.git", "path": "dags",
                                   "credentials": {"secretRef": pg.SECRET},
                                   "reviews": {"gitlab": f"http://{here}:{API_PORT}", "project": f"{NAME}/dags", "dbt": "dags/dbt"}}}}
    try:
        # 1. Production's model.
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        check("bob makes a project whose merge requests are reviewed", r.ok and ready is not None, r.text[:200])
        loaded = wait(lambda: any(p["id"] == "reviewed_dbt" for p in
                                  (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])), 600, every=10)
        state = run_dag(bob, "reviewed_dbt") if loaded else "not loaded"
        check("production's model is built by its DAG", state == "success" and rows("iceberg") == [[1, "EU"], [2, "US"]], f"{state}: {rows('iceberg')}")
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}/semantic", json={"yaml": SEMANTIC}, headers={"X-Platform-Request": "1"}, timeout=60)
        check("... and a metric on it (region_count)", r.ok, r.text[:200])

        # 2. A merge request, on its data branch.
        pg.git("checkout", "-q", "-b", "more-regions", cwd=work)
        with open(os.path.join(work, "dags/dbt/models/regions.sql"), "w") as f:
            f.write(MODEL.format(rows="(1, 'EU'), (2, 'US'), (3, 'APAC')"))
        pg.git("commit", "-q", "-am", "APAC too", cwd=work)
        pg.git("push", "-q", "origin", "more-regions", cwd=work)
        sha = pg.git("rev-parse", "HEAD", cwd=work)
        GitLab.mrs = [{"iid": 1, "title": "APAC too", "sha": sha, "source_branch": "more-regions", "target_branch": "main",
                       "web_url": f"http://{here}:{API_PORT}/{NAME}/dags/-/merge_requests/1"}]
        done = wait(lambda: next((s for s in GitLab.statuses if s[0] == sha and s[1] in ("success", "failed") and s[3] == "storscale/dbt"), None), 900, every=10)
        check("the merge request's change is built on its data branch, and GitLab told: success", done and done[1] == "success",
              done or GitLab.statuses[-3:])
        note = next((b for i, b in GitLab.notes.values() if i == 1), "")
        check("... with a note of the model's rows: 3 on the branch, 2 in production, and the row that differs",
              "| `regions` | 3 | 2 |" in note and "APAC" in note, note[:400])
        check("... and the metric that moved: region_count, 3 on the branch, 2 in production", "| `region_count` | 3 | 2 |" in note, note[-300:])
        semantic_status = next((s for s in GitLab.statuses if s[0] == sha and len(s) > 3 and s[3] == "storscale/semantic"), None)
        check("the model's test ran on the branch (12.3): its status success, the note saying the answer is as in production",
              semantic_status is not None and semantic_status[1] == "success" and "the semantic model's 1 tests pass on the branch" in note,
              (semantic_status, note[-400:]))
        check("... signed in to GitLab with the repo's token, each call", GitLab.signed and all(GitLab.signed), f"{sum(GitLab.signed)} of {len(GitLab.signed)}")
        check("production's table is as it was", rows("iceberg") == [[1, "EU"], [2, "US"]], rows("iceberg"))

        # A new commit: the same note, updated.
        with open(os.path.join(work, "dags/dbt/models/regions.sql"), "w") as f:
            f.write(MODEL.format(rows="(1, 'EU'), (2, 'US'), (3, 'APAC'), (4, 'LATAM')"))
        pg.git("commit", "-q", "-am", "LATAM too", cwd=work)
        pg.git("push", "-q", "origin", "more-regions", cwd=work)
        sha2 = pg.git("rev-parse", "HEAD", cwd=work)
        GitLab.mrs = [{**GitLab.mrs[0], "sha": sha2}]
        again = wait(lambda: next((s for s in GitLab.statuses if s[0] == sha2 and s[1] in ("success", "failed") and s[3] == "storscale/dbt"), None), 900, every=10)
        mine = [b for i, b in GitLab.notes.values() if i == 1]
        check("a new commit is built too, and its note updated, not another added",
              again and again[1] == "success" and len(mine) == 1 and "| `regions` | 4 | 2 |" in mine[0], (again, len(mine), (mine or [""])[0][:200]))

        # 3. The branch, read.
        check("bob reads the branch's table through its catalog (reviewed_mr_1)",
              rows("reviewed_mr_1") == [[1, "EU"], [2, "US"], [3, "APAC"], [4, "LATAM"]], rows("reviewed_mr_1"))

        # GitLab's webhook, at once (with the token the repo's connection holds).
        hook = f"{PLATFORM}/api/webhooks/gitlab/{NAME}"
        wrong = requests.post(hook, headers={"X-Gitlab-Token": "nope", "X-Gitlab-Event": "Merge Request Hook"}, json={}, timeout=30).status_code
        right = requests.post(hook, headers={"X-Gitlab-Token": WEBHOOK_TOKEN, "X-Gitlab-Event": "Merge Request Hook"}, json={}, timeout=30).status_code
        check("GitLab's webhook is taken with the project's token, and refused without it", (wrong, right) == (401, 202), (wrong, right))

        # 4. Closed.
        GitLab.mrs = []
        gone = wait(lambda: "reviewed_mr_1" not in {r[0] for r in sql("bob", "SHOW CATALOGS", "iceberg")}, 300, every=10)
        check("the merge request closed, its branch's catalog goes", gone)
    finally:
        GitLab.mrs = []
        try:
            sql("bob", "DROP TABLE IF EXISTS regions", "iceberg", NAME)
        except Exception as e:  # noqa: BLE001
            print(f"  (dropping regions: {e})")
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        pg.delete_secret(NAME)
        git_server.shutdown()
        api_server.shutdown()
    finish()


if __name__ == "__main__":
    main()
