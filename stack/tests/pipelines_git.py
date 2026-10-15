"""A project's pipelines from its git repo, end to end:
docker compose run --rm test pipelines_git

The suite serves a git repo of its own over HTTP (git http-backend, behind a
token), as a team's git host would, and makes a project, pipes, that names it:

  1. the operator gives Airflow the repo: its connection holds the token,
     which Airflow's API never shows; Airflow loads the repo's DAG, at the
     commit pushed, and the platform shows it as the project's;
  2. bob runs it from the platform; the run says which commit it ran, and
     nothing it can see (the checkout's git settings, its environment, its
     log) holds the token;
  3. a new commit reaches Airflow, with a file that doesn't load: the
     platform shows the new commit and names the broken file;
  4. deleting the project takes its repo out of Airflow: its DAGs, its
     connection and its bundle go.

The secret holding the token is the suite's: in Compose an object in the
platform's bucket (secrets/<name>.json), in Kubernetes a Secret.
"""
import base64
import json
import os
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import requests

from lakekit import check, env, finish, s3_root

import k8s
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
AIRFLOW = "http://airflow:8090/pipelines"
OPERATOR = "http://platform-operator:8081"
NAME, DAG, SECRET, PORT = "pipes", "pipes_hello", "storscale-test-git", 8418
USER, TOKEN = "pipes-bot", "gittest_" + secrets.token_hex(16)

DAG_V1 = '''"""Says hello, and what of git it can see."""
import os
import subprocess
from datetime import datetime

from airflow.sdk import dag, task


@dag(dag_id="pipes_hello", description="{description}", schedule=None, start_date=datetime(2026, 1, 1), catchup=False)
def pipes_hello():
    @task
    def show():
        here = os.path.dirname(os.path.abspath(__file__))
        git = subprocess.run(["git", "-C", here, "config", "--list", "--show-origin"], capture_output=True, text=True)
        for line in (git.stdout + git.stderr).splitlines():
            print("pipes_check: git " + line)
        for key, value in sorted(os.environ.items()):
            if "GIT" in key:
                print(f"pipes_check: env {{key}}={{value}}")
        print("pipes_check: done")
    show()


pipes_hello()
'''

# --- The git host -------------------------------------------------------------------


class Git(BaseHTTPRequestHandler):
    """git's smart HTTP protocol (git http-backend), for USER with TOKEN only."""
    root = ""
    seen = []   # (path, signed in with the token)

    def log_message(self, *args):
        pass

    def do_GET(self):
        self.serve()

    def do_POST(self):
        self.serve()

    def serve(self):
        want = "Basic " + base64.b64encode(f"{USER}:{TOKEN}".encode()).decode()
        authed = self.headers.get("Authorization") == want
        Git.seen.append((self.path, authed))
        if not authed:
            self.send_response(401)
            self.send_header("WWW-Authenticate", 'Basic realm="git"')
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        path, _, query = self.path.partition("?")
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        cgi = dict(os.environ, GIT_PROJECT_ROOT=Git.root, GIT_HTTP_EXPORT_ALL="1", PATH_INFO=path,
                   QUERY_STRING=query, REQUEST_METHOD=self.command, REMOTE_USER=USER,
                   CONTENT_TYPE=self.headers.get("Content-Type", ""), CONTENT_LENGTH=str(len(body)),
                   HTTP_CONTENT_ENCODING=self.headers.get("Content-Encoding", ""),
                   GIT_PROTOCOL=self.headers.get("Git-Protocol", ""))
        out = subprocess.run(["git", "http-backend"], input=body, env=cgi, capture_output=True).stdout
        head, _, payload = out.partition(b"\r\n\r\n")
        status = 200
        headers = []
        for line in head.decode().split("\r\n"):
            k, _, v = line.partition(":")
            if k.lower() == "status":
                status = int(v.split()[0])
            elif k:
                headers.append((k, v.strip()))
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def git(*args, cwd):
    return subprocess.run(["git", "-c", "user.name=Pipes", "-c", "user.email=pipes@example.com", *args],
                          cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def push(work, files, message):
    """Commit the files (path -> text) and push them; the commit's SHA."""
    for path, text in files.items():
        os.makedirs(os.path.dirname(os.path.join(work, path)), exist_ok=True)
        with open(os.path.join(work, path), "w") as f:
            f.write(text)
    git("add", "-A", cwd=work)
    git("commit", "-q", "-m", message, cwd=work)
    git("push", "-q", "origin", "main", cwd=work)
    return git("rev-parse", "HEAD", cwd=work)


# --- The platform and Airflow -------------------------------------------------------


def put_secret():
    """The repo's token, where the operator reads it."""
    data = {"username": USER, "password": TOKEN}
    if k8s.in_cluster():
        k8s.put_secret(SECRET, data)
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"secrets/{SECRET}.json", Body=json.dumps(data).encode())


def delete_secret():
    if k8s.in_cluster():
        k8s._api("DELETE", f"secrets/{SECRET}")
    else:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"secrets/{SECRET}.json")


def airflow():
    """Airflow's API, as the platform's account."""
    r = requests.post(f"{AIRFLOW}/auth/token", timeout=30,
                      json={"username": "storscale-platform", "password": env["AIRFLOW_PLATFORM_PASSWORD"]})
    r.raise_for_status()
    s = requests.Session()
    s.headers["Authorization"] = "Bearer " + r.json()["access_token"]
    return s


def pipelines(bob):
    r = bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60)
    return r.json() if r.ok else {}


def hello(bob, commit=None):
    """The project's pipes_hello, from its repo (at the commit, if given)."""
    for p in pipelines(bob).get("pipelines", []):
        if p["id"] == DAG and p["bundle"] == f"project-{NAME}" and (commit is None or p.get("commit") == commit):
            return p
    return None


def main():
    # The repo, served from here: Airflow reaches this container (or pod) at its address.
    root = tempfile.mkdtemp()
    Git.root = root
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", f"{root}/{NAME}.git"], check=True)
    work = tempfile.mkdtemp()
    git("init", "-q", "-b", "main", cwd=work)
    git("remote", "add", "origin", f"{root}/{NAME}.git", cwd=work)
    v1 = push(work, {f"dags/{DAG}.py": DAG_V1.format(description="Hello, v1")}, "v1")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Git)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    repo = f"http://{socket.gethostbyname(socket.gethostname())}:{PORT}/{NAME}.git"

    bob = platform("bob")
    put_secret()
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "Pipelines from git: the pipelines_git suite's.",
                     "members": [{"group": "engineers", "role": "editor"}],
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags",
                                   "credentials": {"secretRef": SECRET}}}}
    try:
        # 1. The repo, in Airflow.
        put = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        p = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 180)
        parts = {x["system"]: x for x in ((p or {}).get("status") or {}).get("parts", [])}
        check("a project names its pipelines repo, and the operator gives it to Airflow",
              put.ok and parts.get("airflow", {}).get("ok"), f"HTTP {put.status_code}, airflow: {parts.get('airflow')}")
        bundles = requests.get(f"{OPERATOR}/airflow/bundles", timeout=10).json()
        check("... as a bundle that names the repo and its connection, never the token",
              any(b["name"] == f"project-{NAME}" for b in bundles) and TOKEN not in json.dumps(bundles),
              [b["name"] for b in bundles])
        af = airflow()
        conn = af.get(f"{AIRFLOW}/api/v2/connections/git_{NAME}", timeout=30)
        check("... its connection holds the token, which Airflow's API never shows",
              conn.ok and conn.json().get("login") == USER and TOKEN not in conn.text,
              f"HTTP {conn.status_code}, password {'hidden' if TOKEN not in conn.text else 'SHOWN'}")
        loaded = wait(lambda: hello(bob, v1), 600, every=10)
        check("Airflow loads the repo's DAG, at the commit pushed", loaded is not None,
              f"{DAG} at {v1[:7]}" if loaded else f"not yet: {[(x['id'], x.get('commit')) for x in pipelines(bob).get('pipelines', [])]}")
        check("... fetching it with the token", any(a for _, a in Git.seen) and all(
            a for path, a in Git.seen if not path.endswith("/info/refs?service=git-upload-pack")),
              f"{sum(a for _, a in Git.seen)} of {len(Git.seen)} requests signed in")

        # 2. A run, and what it sees.
        r = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{DAG}/runs", headers={"X-Platform-Request": "1"}, timeout=60)
        run_id = r.json().get("run") if r.ok else None
        check("bob runs it from the platform", run_id is not None, f"HTTP {r.status_code} {r.text[:200]}")
        run = wait(lambda: next((x for x in (hello(bob) or {}).get("runs", [])
                                 if x["id"] == run_id and x["state"] in ("success", "failed")), None), 300, every=5)
        check("... it succeeds, and says which commit it ran",
              run and run["state"] == "success" and run.get("commit") == v1, run)
        log = af.get(f"{AIRFLOW}/api/v2/dags/{DAG}/dagRuns/{run_id}/taskInstances/show/logs/1",
                     params={"full_content": "true"}, headers={"Accept": "application/json"}, timeout=60)
        text = log.text if log.ok else ""
        entries = log.json().get("content", []) if log.ok else []
        lines = [e.get("event", "") if isinstance(e, dict) else str(e) for e in entries]
        seen = [line for line in "\n".join(lines).splitlines() if "pipes_check:" in line]
        check("... and no token in what the task sees: the checkout's git settings, its environment, its log",
              log.ok and any("pipes_check: done" in x for x in seen) and TOKEN not in text and not any("***" in x for x in seen),
              f"HTTP {log.status_code}, {len(seen)} lines checked")

        # 3. A new commit, with a broken file.
        v2 = push(work, {f"dags/{DAG}.py": DAG_V1.format(description="Hello, v2"),
                         "dags/broken.py": "from airflow.sdk import dag\n\nthis is not python\n"}, "v2, and a mistake")
        now = wait(lambda: hello(bob, v2), 420, every=10)
        check("a new commit reaches Airflow, and the platform shows it", now is not None and now["description"] == "Hello, v2",
              f"{DAG} at {v2[:7]}" if now else f"still at {(hello(bob) or {}).get('commit', '?')[:7]}")
        errs = wait(lambda: pipelines(bob).get("importErrors"), 180, every=10) or []
        check("... and names the file in it that didn't load", any(e["file"].endswith("broken.py") for e in errs),
              [e["file"] for e in errs])
        page = pipelines(bob)
        check("... with the repo it came from", page.get("repo", {}).get("url") == repo and TOKEN not in json.dumps(page),
              page.get("repo"))
    finally:
        # 4. Deleting the project takes its repo out of Airflow.
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        went = wait(lambda: NAME not in (projects(bob) or {}), 180)
        delete_secret()
    check("deleting the project takes its repo out of Airflow", went and wait(
        lambda: all(b["name"] != f"project-{NAME}" for b in requests.get(f"{OPERATOR}/airflow/bundles", timeout=10).json()), 60),
        "the project, and its bundle, gone")
    af = airflow()
    conn = af.get(f"{AIRFLOW}/api/v2/connections/git_{NAME}", timeout=30).status_code
    check("... and its connection", conn == 404, f"HTTP {conn}")
    left = wait(lambda: af.get(f"{AIRFLOW}/api/v2/dags", params={"bundle_name": f"project-{NAME}"}, timeout=30)
                .json().get("total_entries") == 0, 300, every=10)
    check("... and its DAGs", left, "none left" if left else "still listed")
    server.shutdown()
    finish()


if __name__ == "__main__":
    main()
