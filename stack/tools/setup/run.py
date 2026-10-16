"""Configure the platform, one stage at a time: python /tools/setup/run.py <stage>

  base    before Trino starts: the platform's parts of Keycloak's realm;
          Buckets' buckets, policies and accounts for the lakehouse, the
          notebooks and monitoring; Ranger's people and policies.
  trino   once Trino is up: what Superset needs, then the sales
          project (projects/sales.json), applied by the operator, and its data.

Every step is safe to run again.
"""
import subprocess
import sys

STAGES = {"base": ["realm", "lakehouse", "jupyterhub", "monitoring"], "trino": ["superset", "project"]}

for step in STAGES[sys.argv[1]]:
    print(f"setup: == {step}", flush=True)
    if subprocess.run([sys.executable, "-u", f"/tools/setup/{step}.py"]).returncode:
        sys.exit(f"setup: {step} failed")
