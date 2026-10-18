"""A merge request's review (use case 7.3): the platform operator starts a
run of this DAG for each new commit of an open merge request, with what it
needs in the run's conf: the project, the merge request (iid, commit), the
data branch's Trino catalog, and GitLab's address and the repo's path.

The run checks the repo out at that commit (the project's git connection,
git_<project>), builds the dbt models the change touches and those
downstream (state:modified+, against production's manifest, which the
platform keeps), in the branch's catalog, as the project's pipelines; then
tells the merge request: a commit status (storscale/dbt), and a note with
each model's rows on the branch against production's.

It's in the Airflow image (/opt/storscale/dags, the bundle "storscale"):
the platform's code, in the project's own Airflow on Kubernetes (its
identity, its git connection), in the shared one in Compose.
"""
import base64
import json
import os
import subprocess
import tempfile

import pendulum
import requests

from airflow.sdk import BaseHook, dag, get_current_context, task
from storscale import dbt

PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080")
STATUS = "storscale/dbt"


class GitLab:
    def __init__(self, base, project, token):
        self.url = f"{base.rstrip('/')}/api/v4/projects/{requests.utils.quote(project, safe='')}"
        self.h = {"PRIVATE-TOKEN": token}

    def status(self, sha, state, description, target=""):
        body = {"state": state, "name": STATUS, "description": description[:255]}
        if target:
            body["target_url"] = target
        requests.post(f"{self.url}/statuses/{sha}", json=body, headers=self.h, timeout=30).raise_for_status()

    def note(self, iid, body):
        requests.post(f"{self.url}/merge_requests/{iid}/notes", json={"body": body}, headers=self.h, timeout=30).raise_for_status()


def checkout(conn, sha, work):
    """The repo at sha, in work: the token as a header, never in a URL or on disk."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0"}
    auth = []
    if conn.password:
        basic = base64.b64encode(f"{conn.login or 'oauth2'}:{conn.password}".encode()).decode()
        auth = ["-c", f"http.extraHeader=Authorization: Basic {basic}"]
    subprocess.run(["git", "init", "-q", work], check=True, env=env)
    subprocess.run(["git", *auth, "-C", work, "fetch", "-q", "--depth", "50", conn.host, sha], check=True, env=env)
    subprocess.run(["git", "-C", work, "checkout", "-q", "FETCH_HEAD"], check=True, env=env)


def rows(token, catalog, schema, table):
    import trino
    conn = trino.dbapi.connect(host=os.environ.get("TRINO_HOST", "trino"), port=8443, http_scheme="https",
                               verify=os.environ.get("TRINO_CA", "/tls/cert.pem"), auth=trino.auth.JWTAuthentication(token),
                               catalog=catalog, schema=schema)
    try:
        cur = conn.cursor()
        cur.execute(f'SELECT count(*) FROM "{table}"')
        return cur.fetchone()[0]
    except Exception:  # noqa: BLE001 (not there: a new model, or none in production)
        return None
    finally:
        conn.close()


@dag(dag_id="storscale_review", schedule=None, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False,
     max_active_runs=4, tags=["storscale"], doc_md=__doc__)
def storscale_review():
    @task(task_id="review")
    def review():
        c = get_current_context()["dag_run"].conf
        project, iid, sha = c["project"], int(c["iid"]), c["sha"]
        git = BaseHook.get_connection(f"git_{project.replace('-', '_')}")
        gitlab = GitLab(c["gitlab"], c["gitlabProject"], git.password)
        gitlab.status(sha, "running", "dbt build on the merge request's data branch", c.get("url", ""))
        try:
            work = tempfile.mkdtemp(prefix="review-")
            checkout(git, sha, os.path.join(work, "repo"))
            project_dir = os.path.join(work, "repo", c["dbt"])
            tok = dbt.token()
            # Production's manifest, to build only what changed (and what's downstream).
            state = os.path.join(work, "state")
            os.makedirs(state)
            r = requests.get(f"{PLATFORM}/api/projects/{project}/dbt/artifacts/manifest", timeout=60,
                             headers={"Authorization": f"Bearer {tok}"})
            select = []
            if r.ok:
                with open(os.path.join(state, "manifest.json"), "wb") as f:
                    f.write(r.content)
                select = ["--select", "state:modified+", "--state", state]
            dbt.write_profile(work, c["catalog"], c["namespace"])
            target = os.path.join(work, "target")
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env.update({"STORSCALE_TRINO_TOKEN": tok, "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"})
            built = subprocess.run([dbt.DBT, "build", "--project-dir", project_dir, "--profiles-dir", work, "--profile", "storscale",
                                    "--target-path", target, "--log-path", os.path.join(work, "logs"), *select], env=env)
            try:
                with open(os.path.join(target, "run_results.json")) as f:
                    results = json.load(f)["results"]
            except (OSError, ValueError, KeyError):
                results = []
            failed = [x for x in results if x.get("status") in ("error", "fail")]
            models = [x["unique_id"].split(".")[-1] for x in results if x["unique_id"].startswith("model.") and x.get("status") == "success"]
            lines = [f"**StorScale: dbt on the data branch** (`{c['branch']}`, commit `{sha[:8]}`)", ""]
            if models:
                lines += ["| model | rows on the branch | rows in production |", "|---|---|---|"]
                for m in models:
                    lines.append(f"| `{m}` | {rows(tok, c['catalog'], c['namespace'], m)} | {rows(tok, c['prodCatalog'], c['namespace'], m)} |")
            else:
                lines.append("No model changed.")
            for x in failed:
                lines.append(f"- ✗ `{x['unique_id']}`: {(x.get('message') or x.get('status'))[:300]}")
            ok = built.returncode == 0 and not failed
            gitlab.note(iid, "\n".join(lines))
            gitlab.status(sha, "success" if ok else "failed",
                          f"{len(models)} models built, their tests passing" if ok else f"{len(failed)} failed: " +
                          ", ".join(x["unique_id"].split(".")[-1] for x in failed)[:200], c.get("url", ""))
            if not ok:
                raise RuntimeError(f"the merge request's dbt build failed (exit {built.returncode})")
        except Exception as e:
            if not isinstance(e, RuntimeError):
                gitlab.status(sha, "failed", f"the review couldn't run: {str(e)[:200]}", c.get("url", ""))
            raise
    review()


storscale_review()
