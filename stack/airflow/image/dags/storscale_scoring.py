"""The projects' scorings (use case 15.1, R15.3): a DAG each, from the
platform operator's list, tagged project:<name> so it's that project's
pipeline.

In a project's own Airflow (STORSCALE_PROJECT names it), its own scorings; in
the shared one, those of the projects without one. Each run is
storscale_score_run.py in its own virtualenv (/opt/score: MLflow and the
model libraries the notebooks have), as the project's pipelines: the model's
approved version scores the table, and the run's drift is recorded with the
platform.
"""
import json
import os
import subprocess
import urllib.request

import pendulum
from airflow.sdk import dag, get_current_context, task

from storscale import dbt

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
MINE = os.environ.get("STORSCALE_PROJECT", "")


def scorings():
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/scoring", timeout=5) as r:
            listed = json.load(r)
    except (OSError, ValueError):
        return []   # the operator starting: the next parse has them
    if MINE:
        return [x for x in listed if x["project"] == MINE]
    return [x for x in listed if not x["own"]]


def make(ps):
    s, project, dag_id = ps["scoring"], ps["project"], ps["dag"]

    @dag(dag_id=dag_id, schedule=s["schedule"], start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False, max_active_runs=1,
         tags=[f"project:{project}", "scoring"],
         description=f"the approved {s['model']} scoring {s['table']} into {s['output']} at {s['schedule']} (UTC)",
         doc_md=f"The scoring **{s['name']}** of the project {project}: the registered model {s['model']}'s approved version scores "
                f"{s['table']} into {s['output']}, as the project's pipelines; each run's drift is recorded with the platform.")
    def the_scoring():
        @task(task_id="score", retries=1)
        def score():
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env.update({"STORSCALE_SCORING": json.dumps(ps), "STORSCALE_TOKEN": dbt.token(),
                        "STORSCALE_RUN_ID": get_current_context()["run_id"]})
            subprocess.run(["/opt/score/bin/python", "/opt/score/storscale_score_run.py"], env=env, check=True)
        score()
    return dag_id, the_scoring()


for _ps in scorings():
    _id, _dag = make(_ps)
    globals()[_id] = _dag
