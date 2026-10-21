"""An llm step of a flow (use case 11.3): the platform asks the model about
the step's input and writes the answers as the step's table, which the
flow's dbt models read. The DAG the canvas writes calls this before
dbt.build; the platform does the asking (platformd, on the project's
gateway key and budget, under the guardrail), as the project's pipelines.
"""
import requests
from airflow.sdk import task

from storscale import dbt


def run(project, flow, step, task_id=None):
    """The task: the platform asks the model about every row the step's input
    gives (up to the step's limit) and writes the table; what it cost is in the log."""

    @task(task_id=task_id or f"llm_{step}")
    def run_llm():
        r = requests.post(f"{dbt.PLATFORM}/api/projects/{project}/flows/{flow}/steps/{step}/run", timeout=3600,
                          headers={"Authorization": f"Bearer {dbt.token()}"})
        if not r.ok:
            raise RuntimeError(f"the platform couldn't run the step {step}: HTTP {r.status_code} {r.text[:400]}")
        out = r.json()
        print(f"storscale.llm: {step}: {out.get('rows')} rows asked in {out.get('batches')} questions, {out.get('cost', 0):.4f} spent, "
              f"{out.get('unread', 0)} answers unread, written to {out.get('table')}")
        for w in out.get("warnings") or []:
            print(f"storscale.llm: {step}: {w}")
        return out
    return run_llm()
