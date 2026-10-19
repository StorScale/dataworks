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
from airflow.sdk import BaseHook, dag, get_current_context, task

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
            # A reset (the platform's, at an editor's ask): its tables and where it got to dropped, all copied again.
            if (get_current_context()["dag_run"].conf or {}).get("reset"):
                env["STORSCALE_IMPORT_RESET"] = "1"
            if pi.get("connId"):
                c = BaseHook.get_connection(pi["connId"])
                secret = {"host": c.host, "port": c.port, "database": c.schema, "username": c.login, "password": c.password}
                secret.update(c.extra_dejson or {})   # a connector's sign-in (8.4: Salesforce's keys)
                env["STORSCALE_IMPORT_SECRET"] = json.dumps(secret)
            if im["source"] == "files":
                # The project's landing area is read as the pipelines (their Keycloak
                # client, exchanged with Buckets' STS for credentials by the runner).
                k = BaseHook.get_connection("keycloak_pipelines")
                scheme = (k.extra_dejson or {}).get("scheme", "http")
                host = f"{k.host}:{k.port}" if k.port else k.host
                env["STORSCALE_IMPORT_IDENTITY"] = json.dumps({
                    "token_url": f"{scheme}://{host}/{k.schema}/realms/lakehouse/protocol/openid-connect/token",
                    "client_id": k.login, "client_secret": k.password})
            subprocess.run([PYTHON, RUNNER], env=env, check=True)
        copy()
    return dag_id, the_import()


for _pi in imports():
    _id, _dag = make(_pi)
    globals()[_id] = _dag
