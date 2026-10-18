"""The projects' imports (use case 8.2): a DAG each, from the platform
operator's list (spec.imports of every project it applied), tagged
project:<name> so it's that project's pipeline.

Each run is dlt (stack/airflow/image/storscale_import_run.py, in dlt's own
virtualenv at /opt/dlt), copying the import's resources into the project's
Iceberg tables as <import>_<resource>. The import's credentials are an Airflow connection the
operator keeps (import_<project>_<name>): read here, handed to the run in its
environment, never in its arguments or the logs.
"""
import json
import os
import subprocess
import urllib.request

import pendulum

from airflow.sdk import BaseHook, dag, task

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
RUNNER = "/opt/dlt/storscale_import_run.py"   # in the image, beside dlt's virtualenv
PYTHON = "/opt/dlt/bin/python"


def imports():
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/imports", timeout=5) as r:
            return json.load(r)
    except (OSError, ValueError):
        return []   # the operator starting: the next parse has them


def make(pi):
    im, project = pi["import"], pi["project"]
    dag_id = f"import_{project.replace('-', '_')}_{im['name']}"

    @dag(dag_id=dag_id, schedule=im.get("schedule") or "@hourly", start_date=pendulum.datetime(2026, 1, 1, tz="UTC"),
         catchup=False, max_active_runs=1, tags=[f"project:{project}", "import"],
         doc_md=f"The import **{im['name']}** of the project {project} ({im['source']}): its resources into "
                f"`iceberg.{pi['namespace']}.{im['name']}_<resource>`. Kept by the platform (the project's spec).")
    def the_import():
        @task(task_id="copy")
        def copy():
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env["STORSCALE_IMPORT"] = json.dumps(pi)
            if pi.get("connId"):
                c = BaseHook.get_connection(pi["connId"])
                env["STORSCALE_IMPORT_SECRET"] = json.dumps({"host": c.host, "port": c.port, "database": c.schema,
                                                             "username": c.login, "password": c.password})
            subprocess.run([PYTHON, RUNNER], env=env, check=True)
        copy()
    return dag_id, the_import()


for _pi in imports():
    _id, _dag = make(_pi)
    globals()[_id] = _dag
