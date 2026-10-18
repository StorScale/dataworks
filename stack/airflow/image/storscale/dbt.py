"""dbt in a project's DAGs (use case 7.5): a task that runs `dbt build` on a
dbt project of the repo's, in Trino, on the project's Iceberg tables, as the
project's pipelines service account.

    from storscale import dbt

    @dag(schedule="@hourly", ...)
    def transform():
        dbt.build(project="sales", path="dbt")   # the dbt project beside this DAG file

dbt runs in a virtualenv of its own (/opt/dbt: dbt-core and dbt-trino), signed
in to Trino with a Keycloak token of the project's service account (the
connection keycloak_pipelines, which the platform keeps): Ranger's rules are
the pipelines', and its audit names the account. Its profile is written here
(Trino's address and certificate, the catalog, the project's namespace), so
the repo holds no settings and no secrets: its dbt_project.yml needs none.

After each run, passed or failed, its artifacts (manifest.json, catalog.json
from dbt docs generate, run_results.json) go to the platform (as the same
account), which keeps them for the catalog: the models, their columns, their
lineage and their tests' results appear there.
Not yet: dbt packages (packages.yml; `dbt deps` would write into the repo's
checkout, which is read-only).
"""
import inspect
import json
import os
import subprocess
import tempfile

import requests

from airflow.sdk import BaseHook, task

DBT = "/opt/dbt/bin/dbt"
PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080")
REALM = "lakehouse"


def _token():
    c = BaseHook.get_connection("keycloak_pipelines")
    scheme = c.extra_dejson.get("scheme", "http")
    host = f"{c.host}:{c.port}" if c.port else c.host
    r = requests.post(f"{scheme}://{host}/{c.schema}/realms/{REALM}/protocol/openid-connect/token", timeout=30,
                      data={"grant_type": "client_credentials", "client_id": c.login, "client_secret": c.password})
    r.raise_for_status()
    return r.json()["access_token"]


PROFILE = """storscale:
  target: platform
  outputs:
    platform:
      type: trino
      method: jwt
      jwt_token: "{{{{ env_var('STORSCALE_TRINO_TOKEN') }}}}"
      host: {host}
      port: 8443
      http_scheme: https
      cert: {cert}
      database: {catalog}
      schema: {schema}
      threads: 4
"""


def _report(project, target, token):
    """The run's artifacts to the platform, for the catalog: never fails the run."""
    body = {}
    for name in ("manifest", "catalog", "run_results"):
        try:
            with open(os.path.join(target, f"{name}.json")) as f:
                body[name] = json.load(f)
        except (OSError, ValueError):
            pass
    if not body:
        return
    try:
        r = requests.post(f"{PLATFORM}/api/projects/{project}/dbt/artifacts", json=body, timeout=120,
                          headers={"Authorization": f"Bearer {token}"})
        print(f"storscale.dbt: artifacts to the platform: HTTP {r.status_code} {r.text[:200]}")
    except requests.RequestException as e:
        print(f"storscale.dbt: artifacts not sent: {e}")


def build(project, path="dbt", schema=None, catalog="iceberg", task_id="dbt_build", select=None):
    """The task: `dbt build` on the dbt project at `path`, beside the DAG file
    that calls this. schema: the project's namespace (default its name, with
    underscores); select: dbt's selection (default everything)."""
    caller = os.path.dirname(os.path.abspath(inspect.stack()[1].filename))
    project_dir = os.path.join(caller, path)
    schema = schema or project.replace("-", "_")

    @task(task_id=task_id)
    def dbt_build():
        work = tempfile.mkdtemp(prefix="dbt-")
        with open(os.path.join(work, "profiles.yml"), "w") as f:
            f.write(PROFILE.format(host=os.environ.get("TRINO_HOST", "trino"), cert=os.environ.get("TRINO_CA", "/tls/cert.pem"),
                                   catalog=catalog, schema=schema))
        env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
        env.update({"STORSCALE_TRINO_TOKEN": _token(), "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"})
        cmd = [DBT, "build", "--project-dir", project_dir, "--profiles-dir", work, "--profile", "storscale",
               "--target-path", os.path.join(work, "target"), "--log-path", os.path.join(work, "logs")]
        if select:
            cmd += ["--select", select]
        built = subprocess.run(cmd, env=env)
        # The catalog's view of the run, whatever came of it.
        subprocess.run([DBT, "docs", "generate", "--project-dir", project_dir, "--profiles-dir", work, "--profile", "storscale",
                        "--target-path", os.path.join(work, "target"), "--log-path", os.path.join(work, "logs")], env=env)
        _report(project, os.path.join(work, "target"), env["STORSCALE_TRINO_TOKEN"])
        if built.returncode != 0:
            raise RuntimeError(f"dbt build failed (exit {built.returncode}): its log above says which model or test")
        return os.path.join(work, "target")
    return dbt_build()
