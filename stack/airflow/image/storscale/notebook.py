"""A notebook on a schedule (use case 7.7), as JupyterLab's Schedule writes
it into the project's repo, beside its DAG file:

    from storscale import notebook
    notebook.schedule(project="sales", name="churn_features", schedule="0 5 * * *", owner="dana")

Each run executes notebooks/<name>.ipynb with papermill, in a virtualenv of
its own (/opt/notebook: the notebook image's packages), as the project's
pipelines: buckets_lake there has the calls it has in JupyterLab (s3(),
session(), username()), signed in as the pipelines' service account. The
executed notebook, and its HTML, go to the platform, linked from the run on
the project's Pipelines tab; a failed run says which cell failed, and why.
"""
import inspect
import json
import os
import subprocess
import tempfile
from urllib.parse import quote

import pendulum
import requests
from airflow.sdk import BaseHook, dag, get_current_context, task

from storscale import dbt

PAPERMILL = "/opt/notebook/bin/papermill"
JUPYTER = "/opt/notebook/bin/jupyter"


def _keycloak():
    """Where the kernel gets the pipelines' tokens (client credentials)."""
    c = BaseHook.get_connection("keycloak_pipelines")
    scheme = c.extra_dejson.get("scheme", "http")
    host = f"{c.host}:{c.port}" if c.port else c.host
    return {"STORSCALE_TOKEN_URL": f"{scheme}://{host}/{c.schema}/realms/{dbt.REALM}/protocol/openid-connect/token",
            "STORSCALE_CLIENT_ID": c.login, "STORSCALE_CLIENT_SECRET": c.password,
            "STORSCALE_RUN_AS": f"service-account-{c.login}"}


def failure(executed):
    """Which cell failed, and why: the first error output in the executed notebook."""
    try:
        with open(executed) as f:
            nb = json.load(f)
    except (OSError, ValueError):
        return "it didn't run (no notebook came out)"
    n = 0
    for cell in nb.get("cells", []):
        if cell.get("cell_type") != "code":
            continue
        n += 1
        for out in cell.get("outputs", []):
            if out.get("output_type") == "error":
                return f"cell {n} failed: {out.get('ename')}: {out.get('evalue')}"
    return ""


def keep(project, dag_id, run_id, executed, failed):
    """The executed notebook (and its HTML) to the platform: never fails the run."""
    try:
        with open(executed) as f:
            nb = json.load(f)
    except (OSError, ValueError):
        return
    html = ""
    out = subprocess.run([JUPYTER, "nbconvert", "--to", "html", "--stdout", executed], capture_output=True, text=True)
    if out.returncode == 0:
        html = out.stdout
    try:
        r = requests.post(f"{dbt.PLATFORM}/api/projects/{project}/notebooks/runs/{dag_id}/{quote(run_id, safe='')}",
                          json={"notebook": nb, "html": html, "failed": failed}, timeout=120,
                          headers={"Authorization": f"Bearer {dbt.token()}"})
        print(f"storscale.notebook: the executed notebook to the platform: HTTP {r.status_code} {r.text[:200]}")
    except requests.RequestException as e:
        print(f"storscale.notebook: the executed notebook not sent: {e}")


def schedule(project, name, schedule, owner=""):
    """The DAG notebook_<name>: notebooks/<name>.ipynb, beside the calling DAG file, on a schedule (cron, UTC)."""
    caller = inspect.stack()[1]
    source = os.path.join(os.path.dirname(os.path.abspath(caller.filename)), "notebooks", f"{name}.ipynb")
    dag_id = f"notebook_{name}"

    @dag(dag_id=dag_id, schedule=schedule, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False,
         max_active_runs=1, tags=["notebook"], owner_links={},
         description=f"{owner}'s notebook {name}.ipynb, at {schedule} (UTC)",
         doc_md=f"**{name}.ipynb**, scheduled by {owner} from JupyterLab: each run's executed notebook is linked from the run.")
    def the_notebook():
        @task(task_id="run_notebook", retries=0)
        def run_notebook():
            run_id = get_current_context()["run_id"]
            work = tempfile.mkdtemp(prefix="notebook-")
            executed = os.path.join(work, f"{name}.ipynb")
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env.update(_keycloak())
            env["STORSCALE_PROJECT"] = project
            ran = subprocess.run([PAPERMILL, source, executed, "--cwd", work, "--log-output", "--kernel", "python3"], env=env)
            failed = failure(executed) if ran.returncode != 0 else ""
            keep(project, dag_id, run_id, executed, failed)
            if ran.returncode != 0:
                raise RuntimeError(f"the notebook {name}.ipynb: {failed or 'papermill failed (exit %d)' % ran.returncode}")
        run_notebook()
    caller.frame.f_globals[dag_id] = the_notebook()
