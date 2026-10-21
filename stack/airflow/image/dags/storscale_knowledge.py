"""The projects' knowledge banks on a schedule (use case 12.6): a DAG each,
from the platform operator's list of the banks with a schedule, tagged
project:<name> so it's that project's pipeline.

In a project's own Airflow (STORSCALE_PROJECT names it), its own banks; in
the shared one, those of the projects without one. Each run asks the
platform to build the bank afresh as the project's pipelines (their
Keycloak token): its tables' rows and its folders' files read, embedded on
the project's gateway key, and written as the bank's table.
"""
import json
import os
import urllib.request

import pendulum
import requests
from airflow.sdk import dag, task

from storscale import dbt

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
MINE = os.environ.get("STORSCALE_PROJECT", "")


def banks():
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/knowledge", timeout=5) as r:
            listed = json.load(r)
    except (OSError, ValueError):
        return []   # the operator starting: the next parse has them
    if MINE:
        return [x for x in listed if x["project"] == MINE]
    return [x for x in listed if not x["own"]]


def make(pb):
    b, project, dag_id = pb["bank"], pb["project"], pb["dag"]
    sources = [f"{t['table']}.{t['column']}" for t in b.get("tables") or []] + [f"files {f['prefix']}" for f in b.get("files") or []]

    @dag(dag_id=dag_id, schedule=b["schedule"], start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False, max_active_runs=1,
         tags=[f"project:{project}", "knowledge"],
         description=f"the knowledge bank {b['name']} built afresh from {', '.join(sources)} at {b['schedule']} (UTC)",
         doc_md=f"The knowledge bank **{b['name']}** of the project {project}: {b.get('description') or ''} "
                f"Built afresh from {', '.join(sources)}, embedded on the project's gateway key, as the project's pipelines.")
    def the_bank():
        @task(task_id="build", retries=1)
        def build():
            r = requests.post(f"{dbt.PLATFORM}/api/projects/{project}/knowledge/{b['name']}/build", timeout=3600,
                              headers={"Authorization": f"Bearer {dbt.token()}"})
            if not r.ok:
                raise RuntimeError(f"the platform couldn't build {b['name']}: HTTP {r.status_code} {r.text[:400]}")
            out = r.json()
            print(f"storscale.knowledge: {b['name']}: {out.get('chunks')} passages ({out.get('files', 0)} files, {out.get('skipped', 0)} skipped), "
                  f"{out.get('cost', 0):.4f} spent, written to {out.get('table')}")
            return out
        build()
    return dag_id, the_bank()


for _pb in banks():
    _id, _dag = make(_pb)
    globals()[_id] = _dag
