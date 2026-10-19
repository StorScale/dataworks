"""The projects' agents on a schedule (use case 12.2): a DAG each, from the
platform operator's list of the approved ones with a notebook and a
schedule, tagged project:<name> so it's that project's pipeline.

In a project's own Airflow (STORSCALE_PROJECT names it), its own agents; in
the shared one, those of the projects without one. Each run checks the
project's repo out (its git connection) and runs the agent's notebook with
papermill as 7.7 runs a scheduled notebook, but signed in as the agent: its
Keycloak client and its gateway key from the connection the operator keeps
(agent_<project>_<name>), in the kernel's environment, never in the logs.
The executed notebook goes to the platform, the agent's record of the run.
"""
import json
import os
import subprocess
import tempfile
import urllib.request

import pendulum
from airflow.sdk import BaseHook, dag, get_current_context, task

from storscale import git, notebook

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
MINE = os.environ.get("STORSCALE_PROJECT", "")


def agents():
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/agents", timeout=5) as r:
            listed = json.load(r)
    except (OSError, ValueError):
        return []   # the operator starting: the next parse has them
    if MINE:
        return [x for x in listed if x["project"] == MINE]
    return [x for x in listed if not x["own"]]


def make(pa):
    a, project, dag_id = pa["agent"], pa["project"], pa["dag"]
    approved = a.get("approvedAt")
    start = pendulum.parse(approved) if approved else pendulum.now("UTC")

    @dag(dag_id=dag_id, schedule=a["schedule"], start_date=start, catchup=False, max_active_runs=1,
         tags=[f"project:{project}", "agent"],
         description=f"the agent {a['name']} ({a.get('description') or 'no description'}): {a['notebook']} at {a['schedule']} (UTC)",
         doc_md=f"The agent **{a['name']}** of the project {project}, owner {a.get('owner')}, approved by {a.get('approvedBy')}: "
                f"runs `{a['notebook']}` from the project's repo as itself, with the tools {', '.join(a.get('tools') or []) or 'a reader may use'}. "
                f"Each run's executed notebook is linked from the run.")
    def the_agent():
        @task(task_id="run_agent", retries=0)
        def run_agent():
            run_id = get_current_context()["run_id"]
            c = BaseHook.get_connection(pa["connId"])
            extra = c.extra_dejson or {}
            work = tempfile.mkdtemp(prefix="agent-")
            repo = os.path.join(work, "repo")
            git.checkout(BaseHook.get_connection(pa["gitConnId"]), pa.get("ref") or "HEAD", repo)
            source = os.path.join(repo, pa.get("path") or "", a["notebook"])
            if not os.path.isfile(source):
                raise RuntimeError(f"the agent's notebook {a['notebook']} isn't in the repo at {pa.get('ref') or 'HEAD'}")
            executed = os.path.join(work, os.path.basename(a["notebook"]))
            env = {k: v for k, v in os.environ.items() if not k.startswith("AIRFLOW__")}
            env.update(notebook._keycloak())   # the token endpoint; the client is the agent's
            env.update({"STORSCALE_CLIENT_ID": c.login, "STORSCALE_CLIENT_SECRET": c.password, "STORSCALE_RUN_AS": f"service-account-{c.login}",
                        "STORSCALE_PROJECT": project, "STORSCALE_AGENT": a["name"], "STORSCALE_MCP_URL": extra.get("mcp_url", ""),
                        "STORSCALE_LLM_URL": extra.get("llm_url") or "", "STORSCALE_LLM_KEY": extra.get("llm_key") or ""})
            ran = subprocess.run([notebook.PAPERMILL, source, executed, "--cwd", work, "--log-output", "--kernel", "python3"], env=env)
            failed = notebook.failure(executed) if ran.returncode != 0 else ""
            notebook.keep(project, dag_id, run_id, executed, failed)
            if ran.returncode != 0:
                raise RuntimeError(f"the agent {a['name']}: {failed or 'papermill failed (exit %d)' % ran.returncode}")
        run_agent()
    return dag_id, the_agent()


for _pa in agents():
    _id, _dag = make(_pa)
    globals()[_id] = _dag
