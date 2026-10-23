"""A new team, from a template (use case U.2), end to end:
docker compose run --rm --use-aliases test templates

The suite plays the install's GitLab (its API, and its repos over git's
HTTP protocol), as the reviews suite does: the platform is given it as
templates.gitlab (in Compose, http://test:9480; on Kubernetes, the test
pod's Service storscale-test-gitlab, and its token in the Secret
storscale-test-gitlab, which the suite writes). It holds a template's repo
(a DAG, a dbt project) and two of the company's libraries: a dbt package
(a macro and a generic test, the company's shared check) and Python code.

  1. bob, an administrator, names the libraries at their tags, and the
     template: its repo and its spec (a check, where it's told of trouble,
     its limits). alice can't change either.
  2. bob makes a project, fresh, from the template, for alice: the
     template's repo is copied into the install's group as fresh's own,
     unlinked, given a token of its own (kept as the project's connection,
     never shown), and named in its pipelines.
  3. On day one alice runs its pipeline: Airflow clones the new repo with
     the repo's own token; the Python task imports the company's library,
     and dbt builds the model with the library's macro and tests it with
     its check, through the platform (the project holds no token for them).
  4. bob moves the dbt library to its next tag: the next run uses it, with
     nothing copied into the project's repo.
"""
import base64
import json
import os
import secrets
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

import k8s

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME, GROUP, PORT = "fresh", "teams", 9480
OWN = k8s.in_cluster()
BASE = "http://storscale-test-gitlab:9480" if OWN else "http://test:9480"
SECRET = "storscale-test-gitlab"
INSTALL_TOKEN = "glpat-install-" + secrets.token_hex(12) if OWN else os.environ.get("GITLAB_TOKEN", "storscale-test-gitlab")

DAG = '''"""fresh's first pipeline, from the starter template: the company's Python
library, then the dbt models (with the company's dbt library)."""
import pendulum

from airflow.sdk import dag, task
from storscale import dbt


@dag(schedule=None, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False)
def starter():
    @task
    def greet():
        from storscale import library
        library.use("company-py")
        import company_py
        print("starter_check: " + company_py.GREETING)

    greet() >> dbt.build(project="{project}", path="dbt")


starter()
'''
TEMPLATE_REPO = {
    "dags/starter.py": DAG,
    "dags/dbt/dbt_project.yml": "name: starter\nversion: '1.0'\nconfig-version: 2\nprofile: storscale\nmodel-paths: [models]\n"
                                "models:\n  starter:\n    +materialized: table\n",
    "dags/dbt/packages.yml": f"packages:\n  - git: \"{BASE}/libraries/company-dbt.git\"\n    revision: main\n",
    "dags/dbt/models/orders.sql": "select order_id, {{ company_dbt.cents_to_dollars('cents') }} as amount, {{ company_dbt.version() }} as library\n"
                                  "from (values (1, 1250), (2, 990)) as t (order_id, cents)\n",
    "dags/dbt/models/schema.yml": "version: 2\nmodels:\n  - name: orders\n    columns:\n      - name: order_id\n        tests: [unique, not_null]\n"
                                  "      - name: amount\n        tests: [company_dbt.positive]\n",
    "README.md": "# A new team's project\n\nMade from the starter template.\n",
}


def dbt_library(version):
    return {
        "dbt_project.yml": "name: company_dbt\nversion: '1.0'\nconfig-version: 2\nmacro-paths: [macros]\n",
        "macros/money.sql": "{% macro cents_to_dollars(column) %}cast({{ column }} as double) / 100{% endmacro %}\n"
                            f"{{% macro version() %}}'{version}'{{% endmacro %}}\n",
        "macros/positive.sql": "{% test positive(model, column_name) %}\nselect * from {{ model }} where {{ column_name }} <= 0\n{% endtest %}\n",
    }


PY_LIBRARY = {"company_py/__init__.py": 'GREETING = "hello from the company\'s library"\n'}


# --- GitLab, played here -------------------------------------------------------------


class GitLab(BaseHTTPRequestHandler):
    root = ""
    calls = []        # (method, path, with the install's token)
    tokens = {}       # repo path: its own token
    clones = []       # (repo path, the token it was fetched with: "repo", "install" or "none")
    copying = set()   # forks GitLab is still copying (answered once as started)
    commits = []      # (repo path, message, files): the platform's commits

    def log_message(self, *args):
        pass

    def answer(self, status, body=None, ctype="application/json"):
        data = body if isinstance(body, bytes) else json.dumps(body if body is not None else {}).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.route()

    def do_POST(self):
        self.route()

    def do_DELETE(self):
        self.route()

    def route(self):
        u = urlparse(self.path)
        if u.path.startswith("/api/v4/projects/"):
            return self.api(u)
        if ".git/" in u.path:
            return self.git(u)
        self.answer(404, {"message": "404 Not Found"})

    def repo(self, path):
        return os.path.join(GitLab.root, path + ".git")

    def describe(self, path, status="finished"):
        return {"id": abs(hash(path)) % 100000, "path_with_namespace": path, "http_url_to_repo": f"{BASE}/{path}.git",
                "web_url": f"{BASE}/{path}", "default_branch": "main", "import_status": status}

    def api(self, u):
        authed = self.headers.get("PRIVATE-TOKEN") == INSTALL_TOKEN
        rest = u.path[len("/api/v4/projects/"):]
        enc, _, sub = rest.partition("/")
        path = unquote(enc)
        GitLab.calls.append((self.command, path + ("/" + sub if sub else ""), authed))
        if not authed:
            return self.answer(401, {"message": "401 Unauthorized"})
        if not os.path.isdir(self.repo(path)):
            return self.answer(404, {"message": "404 Project Not Found"})
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        q = parse_qs(u.query)
        route = f"{self.command} {sub}"
        if route == "GET ":
            status = "started" if path in GitLab.copying else "finished"
            GitLab.copying.discard(path)
            return self.answer(200, self.describe(path, status))
        if route == "POST fork":
            made = f"{body['namespace_path']}/{body['path']}"
            if os.path.isdir(self.repo(made)):
                return self.answer(409, {"message": {"name": ["has already been taken"]}})
            subprocess.run(["git", "clone", "-q", "--bare", self.repo(path), self.repo(made)], check=True)
            GitLab.copying.add(made)
            return self.answer(201, self.describe(made, "scheduled"))
        if route in ("DELETE fork", "DELETE "):
            return self.answer(204 if sub == "fork" else 202, {})
        if route == "POST access_tokens":
            GitLab.tokens[path] = "glpat-repo-" + secrets.token_hex(12)
            return self.answer(201, {"token": GitLab.tokens[path], "scopes": body.get("scopes"), "access_level": body.get("access_level")})
        if self.command == "GET" and sub.startswith("repository/commits/"):
            ref = unquote(sub[len("repository/commits/"):])
            out = subprocess.run(["git", "-C", self.repo(path), "rev-parse", "--verify", "-q", ref + "^{commit}"], capture_output=True, text=True)
            return self.answer(200, {"id": out.stdout.strip()}) if out.returncode == 0 else self.answer(404, {"message": "404 Commit Not Found"})
        if route == "GET repository/tree":
            if q.get("page", ["1"])[0] != "1":
                return self.answer(200, [])
            out = subprocess.run(["git", "-C", self.repo(path), "ls-tree", "-r", "-t", q.get("ref", ["main"])[0]], capture_output=True, text=True)
            entries = [{"path": line.split("\t", 1)[1], "type": line.split()[1]} for line in out.stdout.splitlines()]
            return self.answer(200, entries)
        if self.command == "GET" and sub.startswith("repository/files/") and sub.endswith("/raw"):
            name = unquote(sub[len("repository/files/"):-len("/raw")])
            out = subprocess.run(["git", "-C", self.repo(path), "show", f"{q.get('ref', ['main'])[0]}:{name}"], capture_output=True)
            return self.answer(200, out.stdout, "text/plain") if out.returncode == 0 else self.answer(404, {"message": "404 File Not Found"})
        if route == "POST repository/commits":
            work = tempfile.mkdtemp()
            subprocess.run(["git", "clone", "-q", "-b", body["branch"], self.repo(path), work], check=True)
            for a in body["actions"]:
                with open(os.path.join(work, a["file_path"]), "w") as f:
                    f.write(a["content"])
            git("add", "-A", cwd=work)
            git("commit", "-q", "-m", body["commit_message"], cwd=work)
            git("push", "-q", "origin", body["branch"], cwd=work)
            GitLab.commits.append((path, body["commit_message"], [a["file_path"] for a in body["actions"]]))
            return self.answer(201, {"id": git("rev-parse", "HEAD", cwd=work)})
        if route == "GET repository/archive.tar.gz":
            sha = q.get("sha", ["main"])[0]
            name = path.rsplit("/", 1)[-1]
            out = subprocess.run(["git", "-C", self.repo(path), "archive", "--format=tar.gz", f"--prefix={name}-{sha}/", sha], capture_output=True)
            return self.answer(200, out.stdout, "application/gzip") if out.returncode == 0 else self.answer(404, {"message": "404 Not Found"})
        self.answer(404, {"message": "404 Not Found"})

    def git(self, u):
        path = u.path.split(".git/")[0].lstrip("/")
        user_pass = base64.b64decode(self.headers.get("Authorization", "Basic ")[6:] or b"").decode(errors="replace")
        password = user_pass.partition(":")[2]
        who = "repo" if password and password == GitLab.tokens.get(path) else "install" if password == INSTALL_TOKEN else "none"
        GitLab.clones.append((path, who))
        if who == "none":
            self.send_response(401)
            self.send_header("WWW-Authenticate", 'Basic realm="GitLab"')
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        cgi = dict(os.environ, GIT_PROJECT_ROOT=GitLab.root, GIT_HTTP_EXPORT_ALL="1", PATH_INFO=u.path, QUERY_STRING=u.query,
                   REQUEST_METHOD=self.command, REMOTE_USER="gitlab", CONTENT_TYPE=self.headers.get("Content-Type", ""),
                   CONTENT_LENGTH=str(len(body)), HTTP_CONTENT_ENCODING=self.headers.get("Content-Encoding", ""),
                   GIT_PROTOCOL=self.headers.get("Git-Protocol", ""))
        out = subprocess.run(["git", "http-backend"], input=body, env=cgi, capture_output=True).stdout
        head, _, payload = out.partition(b"\r\n\r\n")
        status, headers = 200, []
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
    return subprocess.run(["git", "-c", "user.name=Templates", "-c", "user.email=templates@example.com", *args],
                          cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def make_repo(path, files, tag=None):
    """A bare repo at path (group/name) with the files on main, tagged."""
    bare = os.path.join(GitLab.root, path + ".git")
    os.makedirs(os.path.dirname(bare), exist_ok=True)
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", bare], check=True)
    work = tempfile.mkdtemp()
    git("init", "-q", "-b", "main", cwd=work)
    git("remote", "add", "origin", bare, cwd=work)
    commit(work, files, "the first", tag)
    return work


def commit(work, files, message, tag=None):
    for p, text in files.items():
        os.makedirs(os.path.dirname(os.path.join(work, p)) or work, exist_ok=True)
        with open(os.path.join(work, p), "w") as f:
            f.write(text)
    git("add", "-A", cwd=work)
    git("commit", "-q", "-m", message, cwd=work)
    git("push", "-q", "origin", "main", cwd=work)
    if tag:
        git("tag", tag, cwd=work)
        git("push", "-q", "origin", tag, cwd=work)


# --- The platform ---------------------------------------------------------------------


def pipelines(s):
    r = s.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60)
    return r.json() if r.ok else {}


def run(s, pipeline="starter", timeout=900):
    """Run the pipeline as s: its run's state and log."""
    r = s.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{pipeline}/runs", timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return f"not started: HTTP {r.status_code} {r.text[:200]}", ""
    state = wait(lambda: next((x["state"] for x in next((p for p in pipelines(s).get("pipelines", []) if p["id"] == pipeline), {}).get("runs", [])
                               if x["id"] == run_id and x["state"] in ("success", "failed")), None), timeout, every=10)
    log = s.get(f"{PLATFORM}/api/projects/{NAME}/pipelines/{pipeline}/runs/{run_id}/log", timeout=60)
    return state, (log.json().get("log", "") if log.ok else f"(no log: HTTP {log.status_code})")


def library_column(user):
    try:
        return sql(user, "SELECT order_id, amount, library FROM orders ORDER BY order_id", "iceberg", NAME)
    except Exception as e:  # noqa: BLE001
        return str(e)[:200]


def main():
    GitLab.root = tempfile.mkdtemp()
    make_repo("templates/starter", TEMPLATE_REPO)
    dbt_lib = make_repo("libraries/company-dbt", dbt_library("v1"), tag="v1")
    make_repo("libraries/company-py", PY_LIBRARY, tag="v1")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), GitLab)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    if OWN:   # the install's token, where platformd reads it (a mounted Secret: a minute or so to arrive)
        k8s.put_secret(SECRET, {"token": INSTALL_TOKEN})

    bob, alice = platform("bob"), platform("alice")
    ready = wait(lambda: (lambda r: r.json() if r.ok and r.json().get("repos") and r.json().get("gitlab") == BASE else None)(
        bob.get(f"{PLATFORM}/api/templates", timeout=30)), 180, every=5)
    if not ready:
        r = bob.get(f"{PLATFORM}/api/templates", timeout=30)
        check("the platform is given this suite's GitLab (templates.gitlab: url " + BASE + ", group teams, tokenSecret " + SECRET + ")",
              False, r.text[:300])
        server.shutdown()
        finish()
        return
    template = {"description": "A new team's project: a sample pipeline and dbt model, the company's check, where it's told of trouble",
                "repo": "templates/starter",
                "spec": {"description": "{project}: made from the starter template", "files": {},
                         "tables": {"checks": [{"table": "orders", "column": "order_id", "check": "unique"}]},
                         "pipelines": {"serviceAccount": "airflow-pipelines", "path": "dags", "memory": "2Gi"},
                         "notify": {"email": ["data-team@example.com"]}, "limits": {"storage": "50Gi"}}}
    try:
        # 1. The libraries and the template.
        r = alice.put(f"{PLATFORM}/api/libraries/company-dbt", json={"description": "x", "repo": "libraries/company-dbt", "ref": "v1"}, timeout=30)
        put_dbt = bob.put(f"{PLATFORM}/api/libraries/company-dbt", timeout=30,
                          json={"description": "The company's dbt macros and checks", "repo": "libraries/company-dbt", "ref": "v1"})
        put_py = bob.put(f"{PLATFORM}/api/libraries/company-py", timeout=30,
                         json={"description": "The company's Python helpers", "repo": "libraries/company-py", "ref": "v1"})
        nowhere = bob.put(f"{PLATFORM}/api/libraries/company-dbt", timeout=30,
                          json={"description": "x", "repo": "libraries/company-dbt", "ref": "v9"})
        check("an administrator names the company's libraries at their tags; a tag that isn't there is refused; an analyst can't",
              put_dbt.ok and put_py.ok and nowhere.status_code == 422 and r.status_code == 403,
              f"bob {put_dbt.status_code}/{put_py.status_code}, v9 {nowhere.status_code}, alice {r.status_code}")
        r = alice.put(f"{PLATFORM}/api/templates/starter", json=template, timeout=30)
        put = bob.put(f"{PLATFORM}/api/templates/starter", json=template, timeout=30)
        listed = alice.get(f"{PLATFORM}/api/templates", timeout=30).json()
        check("... and the starter template: its repo and spec; anyone signed in sees it, only administrators change it",
              put.ok and r.status_code == 403 and [t["name"] for t in listed["templates"]] == ["starter"] and not listed["canEdit"],
              f"bob {put.status_code} {put.text[:200]}, alice {r.status_code}")

        # 2. A project from it.
        made = bob.post(f"{PLATFORM}/api/templates/starter/projects", timeout=300,
                        json={"name": NAME, "members": [{"user": "alice", "role": "editor"}], "notify": {"email": ["fresh-team@example.com"]}})
        body = made.json() if made.ok else {}
        spec = (body.get("spec") or {}).get("spec") or {}
        pl = spec.get("pipelines") or {}
        check("bob makes fresh from the template, for alice: its spec is the template's, with its members and channel",
              made.status_code == 201 and spec.get("description") == f"{NAME}: made from the starter template"
              and spec.get("members") == [{"user": "alice", "role": "editor"}] and spec.get("notify", {}).get("email") == ["fresh-team@example.com"]
              and (spec.get("tables") or {}).get("checks") and spec.get("limits", {}).get("storage") == "50Gi",
              f"HTTP {made.status_code} {made.text[:300]}")
        forks = [(m, p) for m, p, _ in GitLab.calls if p.startswith("templates/starter/fork") or p.startswith(f"{GROUP}/{NAME}")]
        check("... with a copy of the template's repo in the install's group, as its own (unlinked from the template's)",
              ("POST", "templates/starter/fork") in forks and ("DELETE", f"{GROUP}/{NAME}/fork") in forks
              and os.path.isdir(os.path.join(GitLab.root, f"{GROUP}/{NAME}.git")) and body.get("repo") == f"{BASE}/{GROUP}/{NAME}",
              forks)
        dag = subprocess.run(["git", "-C", os.path.join(GitLab.root, f"{GROUP}/{NAME}.git"), "show", "main:dags/starter.py"],
                             capture_output=True, text=True).stdout
        check("... its files made the project's: {project} in them is fresh, in one commit",
              f'project="{NAME}"' in dag and [c[2] for c in GitLab.commits] == [["dags/starter.py"]], GitLab.commits)
        token = GitLab.tokens.get(f"{GROUP}/{NAME}")
        conns = alice.get(f"{PLATFORM}/api/projects/{NAME}/connections", timeout=30)
        repo_conn = next((c for c in (conns.json().get("connections") or []) if c["name"] == f"{NAME}-repo"), None) if conns.ok else None
        check("... a token of the repo's own, kept as the project's connection (never shown), named in its pipelines",
              token and pl.get("repo") == f"{BASE}/{GROUP}/{NAME}.git" and pl.get("credentials", {}).get("secretRef") == f"{NAME}-repo"
              and repo_conn is not None and token not in made.text and token not in conns.text
              and all(a for _, _, a in GitLab.calls),
              f"connection {repo_conn and repo_conn.get('kind')}, pipelines {pl}")

        # 3. Day one: its pipeline, with the company's libraries.
        p = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(alice) or {}).get(NAME)),
                 900 if OWN else 300, every=10)
        check("the project is ready, for alice", p is not None, ((projects(alice) or {}).get(NAME) or {}).get("status"))
        loaded = wait(lambda: next((x for x in pipelines(alice).get("pipelines", []) if x["id"] == "starter"), None), 900, every=10)
        clones = sorted({w for path, w in GitLab.clones if path == f"{GROUP}/{NAME}"})
        check("Airflow loads the new repo's pipeline, cloning it with the repo's own token", loaded is not None and clones == ["repo"],
              f"{'loaded' if loaded else 'not loaded'}; clones signed in with {clones}")
        state, log = run(alice) if loaded else ("not loaded", "")
        rows = library_column("alice")
        check("alice runs it: its Python imports the company's library, through the platform",
              state == "success" and "starter_check: hello from the company's library" in log, f"run {state}")
        check("... and dbt builds the model with the library's macro, tested with its check",
              rows == [[1, 12.5, "v1"], [2, 9.9, "v1"]] and "positive" in log, rows)
        if state != "success":
            print("\n".join("    " + x for x in log.splitlines()[-60:]))

        # 4. The library's next tag, used without copying it.
        commit(dbt_lib, dbt_library("v2"), "v2", tag="v2")
        moved = bob.put(f"{PLATFORM}/api/libraries/company-dbt", timeout=30,
                        json={"description": "The company's dbt macros and checks", "repo": "libraries/company-dbt", "ref": "v2"})
        state, log = run(alice)
        rows = library_column("alice")
        untouched = git("ls-tree", "-r", "--name-only", "main", cwd=os.path.join(GitLab.root, f"{GROUP}/{NAME}.git"))
        check("bob moves the dbt library to v2: fresh's next run uses it, nothing of it copied into its repo",
              moved.ok and state == "success" and rows == [[1, 12.5, "v2"], [2, 9.9, "v2"]] and "dbt_packages" not in untouched,
              f"HTTP {moved.status_code}, run {state}: {rows}")
    finally:
        try:
            sql("bob", "DROP TABLE IF EXISTS orders", "iceberg", NAME)
        except Exception as e:  # noqa: BLE001
            print(f"  (dropping orders: {e})")
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        for kind, name in (("templates", "starter"), ("libraries", "company-dbt"), ("libraries", "company-py")):
            bob.delete(f"{PLATFORM}/api/{kind}/{name}", timeout=30)
        if OWN:
            k8s._api("DELETE", f"secrets/{SECRET}")
            time.sleep(1)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
