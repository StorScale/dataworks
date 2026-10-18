"""A notebook on a schedule, end to end: docker compose run --rm test notebooks

The suite serves a project's repo (git over HTTP, as pipelines_git does) and,
on the same address, the little of GitLab's API the platform uses to commit
to it (branches, files, commits, merge requests: applied to the real repo),
and makes a project whose repo it is, its token given on its Connections
tab. These check (use case 7.7), calling the platform as JupyterLab's
Schedule does (the person's Keycloak token):

  1. alice (a reader) has nowhere to schedule a notebook, and is refused;
  2. bob (an editor) schedules one: the notebook and the DAG that runs it are
     committed on a branch of their own, with a merge request;
  3. merged, the project's Airflow runs it as the project's pipelines, and
     the run's executed notebook (its outputs) is kept, which alice reads
     from the Pipelines tab's link;
  4. a notebook whose cell fails: its run fails, and the kept notebook
     shows the error.

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

from lakekit import check, finish, s3_root, token

import k8s
import pipelines_git as pg
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME, PORT, CONNECTION = "notebooked", 8426, "notebooked-git"
H = {"X-Platform-Request": "1"}


def notebook(*sources):
    return {"cells": [{"cell_type": "code", "metadata": {}, "source": s, "outputs": [], "execution_count": None} for s in sources],
            "metadata": {"kernelspec": {"name": "python3", "display_name": "Python 3", "language": "python"}},
            "nbformat": 4, "nbformat_minor": 5}


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

    def answer(self, code, body=None):
        data = b"" if self.command == "HEAD" else json.dumps(body if body is not None else {}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def has(self, ref, path=""):
        return subprocess.run(["git", "--git-dir", Host.bare, "cat-file", "-e", f"refs/heads/{ref}" + (f":{path}" if path else "")],
                              capture_output=True).returncode == 0

    def api(self):
        if self.headers.get("PRIVATE-TOKEN") != pg.TOKEN:
            return self.answer(401, {"message": "401 Unauthorized"})
        u = urlparse(self.path)
        path, q = unquote(u.path).removeprefix(f"/api/v4/projects/{NAME}"), parse_qs(u.query)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}") if self.command == "POST" else {}
        if path == "":
            return self.answer(200, {"default_branch": "main"})
        if path.startswith("/repository/branches/"):
            return self.answer(200 if self.has(path.removeprefix("/repository/branches/")) else 404, {})
        if path.startswith("/repository/files/"):
            return self.answer(200 if self.has(q["ref"][0], path.removeprefix("/repository/files/")) else 404, {})
        if path == "/repository/commits":
            work = tempfile.mkdtemp()
            pg.git("clone", "-q", Host.bare, work, cwd="/")
            pg.git("checkout", "-q", "-B", body["branch"], "origin/" + (body.get("start_branch") or body["branch"]), cwd=work)
            for a in body["actions"]:
                os.makedirs(os.path.dirname(os.path.join(work, a["file_path"])), exist_ok=True)
                with open(os.path.join(work, a["file_path"]), "w") as f:
                    f.write(a["content"])
            pg.git("add", "-A", cwd=work)
            pg.git("commit", "-q", "-m", body["commit_message"], cwd=work)
            pg.git("push", "-q", "origin", body["branch"], cwd=work)
            return self.answer(201, {"id": pg.git("rev-parse", "HEAD", cwd=work)})
        if path == "/merge_requests" and self.command == "GET":
            return self.answer(200, [m for m in Host.mrs if m["source_branch"] == q.get("source_branch", [""])[0]])
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


def schedule(user, path, nb, cron="0 5 * * *"):
    return requests.post(f"{PLATFORM}/api/projects/{NAME}/notebooks/schedules", timeout=60,
                         headers={"Authorization": f"Bearer {token(user)}"}, json={"path": path, "schedule": cron, "notebook": nb})


def run(bob, dag):
    """A run of the DAG, once Airflow has it, to its end: (run, state)."""
    loaded = wait(lambda: any(p["id"] == dag for p in
                              (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])), 600, every=10)
    if not loaded:
        return None, "not loaded"
    r = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{dag}/runs", headers=H, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return None, f"not started: HTTP {r.status_code}"

    def state():
        ps = (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])
        return next((x["state"] for p in ps if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
    return run_id, wait(state, 600, every=10) or "still running"


def kept(user, dag, run_id):
    r = user.get(f"{PLATFORM}/api/projects/{NAME}/notebooks/runs/{dag}/{requests.utils.quote(run_id, safe='')}", timeout=60)
    return r.text if r.ok else f"HTTP {r.status_code}"


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
            "spec": {"description": "Notebooks on a schedule: the notebooks suite's.",
                     "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags", "credentials": {"secretRef": CONNECTION}}}}
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        c = bob.post(f"{PLATFORM}/api/projects/{NAME}/connections", timeout=30,
                     json={"name": CONNECTION, "kind": "git", "values": {"username": pg.USER, "password": pg.TOKEN}})
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        check("bob makes a project with a repo, its token given on its Connections tab", r.ok and c.ok and ready is not None, (r.text[:200], c.text[:200]))

        # 1. alice reads, and can't schedule.
        mine = requests.get(f"{PLATFORM}/api/notebooks/projects", headers={"Authorization": f"Bearer {token('alice')}"}, timeout=30).json()
        r = schedule("alice", "work/sneaky.ipynb", notebook("1"))
        check("alice (a reader) has nowhere to schedule a notebook, and is refused",
              not any(p["name"] == NAME for p in mine.get("projects", [])) and r.status_code == 403, (mine, r.status_code))

        # 2. bob schedules one: a branch, a merge request.
        theirs = requests.get(f"{PLATFORM}/api/notebooks/projects", headers={"Authorization": f"Bearer {token('bob')}"}, timeout=30).json()
        r = schedule("bob", "work/Daily Check.ipynb", notebook("import buckets_lake\nprint('runs as', buckets_lake.username())", "print(6 * 7)"))
        out = r.json() if r.ok else {}
        files = ""
        if r.ok:
            pg.git("fetch", "-q", "origin", cwd=work)
            files = pg.git("ls-tree", "-r", "--name-only", "origin/storscale/schedule-daily-check", cwd=work)
        check("bob schedules his notebook: it and its DAG on a branch of their own, with a merge request",
              {"name": NAME} in theirs.get("projects", []) and out.get("dag") == "notebook_daily_check" and len(Host.mrs) == 1
              and "dags/notebooks/daily_check.ipynb" in files and "dags/notebook_daily_check.py" in files, (theirs, r.text[:300], files))

        # 3. Merged, it runs; its notebook kept.
        merge(work, "storscale/schedule-daily-check")
        run_id, state = run(bob, "notebook_daily_check")
        page = kept(alice, "notebook_daily_check", run_id) if run_id else ""
        check("merged, the project's Airflow runs it as the project's pipelines", state == "success", state)
        check("... and alice reads the run's notebook, its outputs in it", "42" in page and "runs as service-account-" in page, page[:300])

        # 4. A cell that fails.
        schedule("bob", "work/broken.ipynb", notebook("rows = 0", "raise ValueError('no rows today')"))
        merge(work, "storscale/schedule-broken")
        run_id, state = run(bob, "notebook_broken")
        page = kept(bob, "notebook_broken", run_id) if run_id else ""
        check("a notebook whose cell fails: its run fails, and its kept notebook shows why",
              state == "failed" and "no rows today" in page, (state, page[:300]))
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        # The project gone, its connection (the tab's, kept as any connection is).
        if k8s.in_cluster():
            k8s._api("DELETE", f"secrets/{CONNECTION}")
        else:
            for key in (f"connections/{NAME}/{CONNECTION}.json", f"connection-meta/{NAME}/{CONNECTION}.json"):
                s3_root().delete_object(Bucket="storscale-platform", Key=key)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
