"""A plan of a project's release (use case 7.4): platformd starts a run of
this DAG when someone plans a ref of the project's repo, with the project,
the ref and the plan in the run's conf.

The run checks the repo out at the ref (the project's git connection,
git_<project>) and sends its storscale/ folder (project.yaml, semantic.yaml,
targets/*.yaml) and the commit to platformd, as the project's pipelines;
platformd composes the spec for its install's target and says what it would
change. When the checkout fails, it says why, so the plan says so.

It's in the Airflow image (/opt/storscale/dags, the bundle "storscale"),
like the review of a merge request.
"""
import os
import tempfile

import pendulum
import requests
from airflow.sdk import BaseHook, dag, get_current_context, task

from storscale import dbt, git

PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080")
LIMIT = 1 << 20   # a file of the folder, at most


def folder_files(root):
    """The folder's files platformd reads, by their path in it."""
    out = {}
    for name in ["project.yaml", "semantic.yaml"]:
        path = os.path.join(root, name)
        if os.path.isfile(path):
            out[name] = open(path, encoding="utf-8").read(LIMIT)
    targets = os.path.join(root, "targets")
    if os.path.isdir(targets):
        for name in sorted(os.listdir(targets)):
            if name.endswith(".yaml") and os.path.isfile(os.path.join(targets, name)):
                out["targets/" + name] = open(os.path.join(targets, name), encoding="utf-8").read(LIMIT)
    return out


@dag(dag_id="storscale_plan", schedule=None, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False,
     max_active_runs=4, tags=["storscale"], doc_md=__doc__)
def storscale_plan():
    @task(task_id="plan", retries=0)
    def plan():
        c = get_current_context()["dag_run"].conf
        project, ref, plan_id = c["project"], c["ref"], c["plan"]
        body = {}
        try:
            conn = BaseHook.get_connection(f"git_{project.replace('-', '_')}")
            work = os.path.join(tempfile.mkdtemp(prefix="plan-"), "repo")
            body["commit"] = git.checkout(conn, ref, work, depth=1)
            body["files"] = folder_files(os.path.join(work, c.get("folder") or "storscale"))
        except Exception as e:  # noqa: BLE001 (the plan says why)
            why = getattr(e, "stderr", None) or str(e)
            body = {"error": f"checking out {ref}: {why.strip()[-500:]}"}
        r = requests.post(f"{PLATFORM}/api/projects/{project}/plans/{plan_id}/files", json=body, timeout=120,
                          headers={"Authorization": f"Bearer {dbt.token()}"})
        r.raise_for_status()
        print(f"plan {plan_id} of {ref}: {r.json().get('state')} {r.json().get('error', '')}")
    plan()


storscale_plan()
