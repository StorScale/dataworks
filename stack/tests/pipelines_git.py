"""A project's pipelines from its git repo, end to end:
docker compose run --rm test pipelines_git

The suite serves a git repo of its own over HTTP (git http-backend, behind a
token), as a team's git host would, and makes a project, pipes, that names it:

  1. the operator gives Airflow the repo: its connection holds the token,
     which Airflow's API never shows; Airflow loads the repo's DAG, at the
     commit pushed, and the platform shows it as the project's;
  2. bob runs it from the platform; the run says which commit it ran, and
     nothing it can see (the checkout's git settings, its environment, its
     log) holds the token. Neither as it's parsed nor as it runs can its
     code read the platform's secrets (Airflow has its own keys only);
  3. a new commit reaches Airflow, with a file that doesn't load: the
     platform shows the new commit and names the broken file;
  4. deleting the project takes its repo out of Airflow: its DAGs, its
     connection and its bundle go.

The repo has a dbt project too (use case 7.5): its DAG calls the platform's
helper (storscale.dbt.build), whose run makes the model's table in the
project's namespace, as its pipelines' service account, its tests passing.

On Kubernetes the project gets an Airflow of its own, and these check too
that it's the project's alone: its repo isn't in the shared Airflow; its
pipelines run as a service account of its own, in the memory it gives
them; its UI is at /airflow/pipes/, for its members only; and its DAG code
can't reach another tool's database. Deleting the project takes its
Airflow away.

The secret holding the token is the suite's, as one of the project's
connections: in Compose an object in the platform's bucket
(connections/<project>/<name>.json), in Kubernetes a Secret labelled
with the project. One labelled for another project is refused first.
"""
import base64
import html
import json
import os
import re
import secrets
import socket
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import k8s
import requests

from lakekit import check, env, finish, s3_root, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
OPERATOR = "http://platform-operator:8081"
NAME, DAG, SECRET, PORT = "pipes", "pipes_hello", "storscale-test-git", 8418
OWN = k8s.in_cluster()   # the project has an Airflow of its own
AIRFLOW = f"http://project-{NAME}-airflow:8090/airflow/{NAME}" if OWN else "http://airflow:8090/pipelines"
USER, TOKEN = "pipes-bot", "gittest_" + secrets.token_hex(16)

DBT_DAG = '''"""The project's dbt models, built by the platform's helper."""
import pendulum

from airflow.sdk import dag
from storscale import dbt


@dag(schedule=None, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False)
def pipes_dbt():
    dbt.build(project="pipes", path="dbt")


pipes_dbt()
'''
DBT_PROJECT = {
    "dags/dbt/dbt_project.yml": "name: pipes\nversion: '1.0'\nconfig-version: 2\nprofile: storscale\nmodel-paths: [models]\n"
                                "models:\n  pipes:\n    +materialized: table\n",
    "dags/dbt/models/regions.sql": "select * from (values (1, 'EU'), (2, 'US')) as t (id, region)\n",
    "dags/dbt/models/schema.yml": "version: 2\nmodels:\n  - name: regions\n    description: The regions, for the dbt check.\n    columns:\n      - name: id\n"
                                  "        tests: [unique, not_null]\n      - name: region\n        tests: [not_null]\n",
}

DAG_V1 = '''"""Says hello, and what of git and the platform's secrets it can see: as
it's parsed (in its description) and as it runs (in its log)."""
import os
import subprocess
from datetime import datetime

import requests

from airflow.sdk import dag, task

PLATFORM_SECRETS = ["BUCKETS_ROOT_PASSWORD", "KEYCLOAK_ADMIN_PASSWORD", "RANGER_PASSWORD", "OPERATOR_CLIENT_SECRET",
                    "PLATFORM_STORE_SECRET_KEY", "PLATFORM_OAUTH_SECRET", "AIRFLOW_PLATFORM_PASSWORD", "AIRFLOW_OAUTH_SECRET"]
SA = "/var/run/secrets/kubernetes.io/serviceaccount"


def exposed():
    """What of the platform's this code can read: none, if Airflow is given only its own keys."""
    found = [k for k in PLATFORM_SECRETS if os.environ.get(k)]
    if os.path.exists("/tls/key.pem") or os.path.exists("/tls/trino.pem"):
        found.append("trino-key")
    # A project's own Airflow (not the shared one, whose database is airflow):
    # with its own database's user, which its processes' environment holds,
    # it can't reach another tool's database.
    try:
        environ = open("/proc/1/environ", "rb").read().split(b"\\0")
    except OSError:  # unreadable: nothing to take from it
        environ = []
    conn = next((e.split(b"=", 1)[1].decode() for e in environ if e.startswith(b"AIRFLOW__DATABASE__SQL_ALCHEMY_CONN=")), "")
    if conn and not conn.endswith("/airflow"):
        import psycopg2
        server = conn.replace("postgresql+psycopg2://", "postgresql://").rsplit("/", 1)[0]
        for db in ("airflow", "superset"):
            try:
                psycopg2.connect(server + "/" + db, connect_timeout=5).close()
                found.append("database-" + db)
            except psycopg2.OperationalError as e:
                if "permission denied" not in str(e):
                    found.append("database-" + db + "(" + str(e).strip()[:60] + ")")
    if os.path.exists(SA + "/token"):
        ns = open(SA + "/namespace").read().strip()
        r = requests.get(f"https://kubernetes.default.svc/api/v1/namespaces/{{ns}}/secrets/storscale-env", timeout=10,
                         headers={{"Authorization": "Bearer " + open(SA + "/token").read().strip()}}, verify=SA + "/ca.crt")
        if r.status_code != 403:
            found.append(f"kubernetes-secrets(HTTP {{r.status_code}})")
    return ",".join(found) or "none"


@dag(dag_id="pipes_hello", description="{description}; exposed: " + exposed(), schedule=None,
     start_date=datetime(2026, 1, 1), catchup=False)
def pipes_hello():
    @task
    def show():
        print("pipes_check: exposed " + exposed())
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


def put_secret(project=NAME):
    """The repo's token, as one of the project's connections: a Secret labelled
    with the project, or connections/<project>/ in the platform's bucket."""
    data = {"username": USER, "password": TOKEN}
    if k8s.in_cluster():
        k8s.put_secret(SECRET, data, labels={"platform.storscale.io/connection": project})
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"connections/{project}/{SECRET}.json", Body=json.dumps(data).encode())


def delete_secret(project=NAME):
    if k8s.in_cluster():
        k8s._api("DELETE", f"secrets/{SECRET}")
    else:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"connections/{project}/{SECRET}.json")


def airflow():
    """The project's Airflow's API (its own, or the shared one), as the platform's account."""
    pw = k8s.get_secret(f"project-{NAME}-airflow").get("PLATFORM_PASSWORD") if OWN else env["AIRFLOW_PLATFORM_PASSWORD"]
    r = requests.post(f"{AIRFLOW}/auth/token", timeout=30, json={"username": "storscale-platform", "password": pw})
    r.raise_for_status()
    s = requests.Session()
    s.headers["Authorization"] = "Bearer " + r.json()["access_token"]
    return s


def sign_in(base, user):
    """Sign in to an Airflow's UI through Keycloak's form; API headers, or None."""
    s = requests.Session()
    r = s.get(f"{base}/auth/login/keycloak?next=", timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    if not form:
        return None
    s.post(html.unescape(form.group(1)), timeout=120,
           data={"username": user, "password": env["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    token = s.cookies.get("_token")
    return {"Authorization": f"Bearer {token}"} if token else None


def memory_mib(deployment):
    """A Deployment's containers' memory limits, added up, in MiB."""
    total = 0
    for c in deployment["spec"]["template"]["spec"]["containers"]:
        q = c.get("resources", {}).get("limits", {}).get("memory", "0Mi")
        total += int(q[:-2]) * (1024 if q.endswith("Gi") else 1)
    return total


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
    v1 = push(work, {f"dags/{DAG}.py": DAG_V1.format(description="Hello, v1"), "dags/pipes_dbt.py": DBT_DAG, **DBT_PROJECT}, "v1")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Git)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    repo = f"http://{socket.gethostbyname(socket.gethostname())}:{PORT}/{NAME}.git"

    bob = platform("bob")
    put_secret("someone-else")   # first, a credential that isn't the project's
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "Pipelines from git: the pipelines_git suite's.",
                     "members": [{"group": "engineers", "role": "editor"}], "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines", "repo": repo, "path": "dags",
                                   "credentials": {"secretRef": SECRET}, "memory": "2Gi"}}}
    try:
        # 1. The repo, in Airflow.
        put = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)

        def airflow_part():
            st = ((projects(bob) or {}).get(NAME) or {}).get("status") or {}
            return next((x for x in st.get("parts") or [] if x["system"] == "airflow"), None)
        refused = wait(lambda: (lambda x: x if x and not x["ok"] and SECRET in x.get("message", "") else None)(airflow_part()),
                       600 if OWN else 180)
        check("a credential that isn't one of the project's connections is refused",
              refused is not None and TOKEN not in json.dumps(refused), (refused or {}).get("message"))
        delete_secret("someone-else")
        put_secret()
        p = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 600 if OWN else 180)
        parts = {x["system"]: x for x in ((p or {}).get("status") or {}).get("parts") or []}
        check("a project names its pipelines repo, and the operator gives it to Airflow",
              put.ok and parts.get("airflow", {}).get("ok"), f"HTTP {put.status_code}, airflow: {parts.get('airflow')}")
        bundles = requests.get(f"{OPERATOR}/airflow/bundles", timeout=10).json()
        if OWN:
            check("... an Airflow of its own: the shared one doesn't take the repo",
                  all(b["name"] != f"project-{NAME}" for b in bundles) and TOKEN not in json.dumps(bundles),
                  [b["name"] for b in bundles])
            members = {m["username"]: m["role"] for m in p["status"].get("members") or []}
            check("... whose pipelines run as a service account of the project's own",
                  members.get(f"service-account-project-{NAME}-pipelines") == "pipelines"
                  and "service-account-airflow-pipelines" not in members, members)
            d = k8s._api("GET", f"deployments/project-{NAME}-airflow", api="apis/apps/v1")
            check("... in the memory the project gives it (2Gi)", d.ok and memory_mib(d.json()) == 2048,
                  f"HTTP {d.status_code}, {memory_mib(d.json()) if d.ok else '?'} MiB")
            ui = f"{PLATFORM}/airflow/{NAME}"
            health = requests.get(f"{ui}/api/v2/monitor/health", timeout=30)
            check("... its UI is at the platform's /airflow/pipes/", health.ok, f"HTTP {health.status_code}")
            bob_ui, alice_ui = sign_in(ui, "bob"), sign_in(ui, "alice")
            conns = requests.get(f"{ui}/api/v2/connections", headers=bob_ui, timeout=30).status_code if bob_ui else None
            check("... where the project's editors sign in (Op), and people outside it can't",
                  conns == 200 and alice_ui is None, f"bob: {conns}, alice: {'refused' if alice_ui is None else 'signed in'}")
        else:
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
        check("... and the DAG's code, as it's parsed, can't read the platform's secrets",
              loaded is not None and loaded["description"].endswith("exposed: none"), (loaded or {}).get("description"))
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
        check("... nor the platform's secrets: not in its environment, its files, through Kubernetes, or another database",
              any(x.endswith("pipes_check: exposed none") for x in seen),
              next((x for x in seen if "pipes_check: exposed" in x), "no line"))

        # 2b. dbt, as the project's pipelines.
        r = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/pipes_dbt/runs", headers={"X-Platform-Request": "1"}, timeout=60)
        dbt_run = r.json().get("run") if r.ok else None
        state = wait(lambda: next((x["state"] for x in next((p for p in pipelines(bob).get("pipelines", []) if p["id"] == "pipes_dbt"), {}).get("runs", [])
                                   if x["id"] == dbt_run and x["state"] in ("success", "failed")), None), 600, every=10) if dbt_run else None
        try:
            regions = sql("bob", "SELECT id, region FROM regions ORDER BY id", "iceberg", "pipes")
        except Exception as e:  # noqa: BLE001
            regions = str(e)[:200]
        check("dbt builds the repo's model (dbt build, its tests passing) into the project's namespace, as its pipelines",
              state == "success" and regions == [[1, "EU"], [2, "US"]], f"run {state}: {regions}")
        def described():
            r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
                "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
            t = requests.get("http://openmetadata:8585/api/v1/tables/name/trino.iceberg.pipes.regions",
                             headers={"Authorization": f"Bearer {r.json()['access_token']}"}, timeout=30)
            return (t.json().get("description") or None) if t.ok else None
        desc = wait(described, 300, every=10) if state == "success" else None
        check("... and the catalog has the model as dbt describes it", desc == "The regions, for the dbt check.", desc)
        if state != "success" and dbt_run:   # dbt's own words, from the task's log
            log = af.get(f"{AIRFLOW}/api/v2/dags/pipes_dbt/dagRuns/{dbt_run}/taskInstances/dbt_build/logs/1",
                         params={"full_content": "true"}, headers={"Accept": "application/json"}, timeout=60)
            entries = log.json().get("content", []) if log.ok else []
            lines = [e.get("event", "") if isinstance(e, dict) else str(e) for e in entries]
            print("\n".join("    " + x for x in "\n".join(lines).splitlines()[-60:]) or f"    (no log: HTTP {log.status_code})")

        # 3. A new commit, with a broken file.
        v2 = push(work, {f"dags/{DAG}.py": DAG_V1.format(description="Hello, v2"),
                         "dags/broken.py": "from airflow.sdk import dag\n\nthis is not python\n"}, "v2, and a mistake")
        now = wait(lambda: hello(bob, v2), 420, every=10)
        check("a new commit reaches Airflow, and the platform shows it", now is not None and now["description"].startswith("Hello, v2;"),
              f"{DAG} at {v2[:7]}" if now else f"still at {(hello(bob) or {}).get('commit', '?')[:7]}")
        errs = wait(lambda: pipelines(bob).get("importErrors"), 180, every=10) or []
        check("... and names the file in it that didn't load", any(e["file"].endswith("broken.py") for e in errs),
              [e["file"] for e in errs])
        page = pipelines(bob)
        check("... with the repo it came from", page.get("repo", {}).get("url") == repo and TOKEN not in json.dumps(page),
              page.get("repo"))
    finally:
        try:
            sql("bob", "DROP TABLE IF EXISTS regions", "iceberg", "pipes")
        except Exception as e:  # noqa: BLE001
            print(f"  (dropping regions: {e})")
        # 4. Deleting the project takes its repo out of Airflow.
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        went = wait(lambda: NAME not in (projects(bob) or {}), 180)
        delete_secret()
        delete_secret("someone-else")
    if OWN:
        gone = wait(lambda: not k8s.get_secret(f"project-{NAME}-airflow") and k8s._api(
            "GET", f"deployments/project-{NAME}-airflow", api="apis/apps/v1").status_code == 404, 180)
        check("deleting the project takes its Airflow away: its pod, its keys", went and gone,
              "gone" if gone else "still there")
        server.shutdown()
        finish()
        return
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
