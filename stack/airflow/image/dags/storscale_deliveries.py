"""The projects' deliveries (use case 9.1): a DAG each, from the platform
operator's list of the approved ones (an unapproved delivery has no DAG, so
it doesn't run), tagged project:<name> so it's that project's pipeline.

In a project's own Airflow (STORSCALE_PROJECT names it), its own deliveries;
in the shared one, those of the projects without one. Each run is
storscale_delivery_run.py in its own virtualenv (/opt/deliver), as the
project's pipelines (their Keycloak token), its target and key from the
connection the operator keeps (delivery_<project>_<name>), handed to it in
its environment, never in its arguments or the logs.
"""
import json
import os
import subprocess
import urllib.request

import pendulum

from airflow.sdk import BaseHook, dag, get_current_context, task
from storscale import dbt

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
MINE = os.environ.get("STORSCALE_PROJECT", "")


def deliveries():
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/deliveries", timeout=5) as r:
            listed = json.load(r)
    except (OSError, ValueError):
        return []   # the operator starting: the next parse has them
    if MINE:   # a project's own Airflow: its deliveries
        return [x for x in listed if x["project"] == MINE]
    return [x for x in listed if not x["own"]]   # the shared one: those of the projects without their own


def make(pd):
    d, project = pd["delivery"], pd["project"]
    dag_id = f"delivery_{project.replace('-', '_')}_{d['name']}"

    # From its approval on: a delivery approved on a Thursday sends next
    # Monday's, not last Monday's at once (Airflow runs a new DAG's latest slot).
    approved = (d.get("approval") or {}).get("at")
    start = pendulum.parse(approved) if approved else pendulum.now("UTC")
    checks = json.dumps(d.get("checks") or {}, sort_keys=True)

    @dag(dag_id=dag_id, schedule=d["schedule"], start_date=start, catchup=False,
         max_active_runs=1, tags=[f"project:{project}", "delivery"],
         description=f"{d.get('table') or 'a query'} as {d['format']} to {d['target']}; checks {checks}",
         doc_md=f"The delivery **{d['name']}** of the project {project}: {d.get('table') or 'a query'} as {d['format']} "
                f"to {d['target']}, approved by {', '.join((d.get('approval') or {}).get('by') or [])}.")
    def the_delivery():
        @task(task_id="deliver")
        def deliver():
            c = BaseHook.get_connection(pd["connId"])
            ctx = get_current_context()
            day = (ctx.get("logical_date") or pendulum.now("UTC")).strftime("%Y-%m-%d")
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env.update({"STORSCALE_DELIVERY": json.dumps(pd), "STORSCALE_DELIVERY_SECRET": c.extra or "{}",
                        "STORSCALE_TOKEN": dbt.token(), "STORSCALE_DATE": day})
            subprocess.run(["/opt/deliver/bin/python", "/opt/deliver/storscale_delivery_run.py"], env=env, check=True)
        deliver()
    return dag_id, the_delivery()


for _pd in deliveries():
    _id, _dag = make(_pd)
    globals()[_id] = _dag
