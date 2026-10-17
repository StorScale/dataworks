"""The platform's end-to-end checks: docker compose run --rm test [suite ...]

Runs the Buckets integration guides' checks against the one platform, one
suite at a time. The suites share the lakehouse's sales tables, so they're
loaded afresh before each suite that reads them. projects deletes the sales
project and makes it again, after the suites that use it. monitoring runs last: it
fails one of Buckets' four drives for a minute, as a failing disk would.
"""
import subprocess
import sys
import time

from lakekit import load_sales_tables

SUITES = ["realm", "health", "lakehouse", "superset", "airflow", "jupyterhub", "catalog", "semantic", "projects", "project_alerts", "audit", "maintenance", "pipelines_git", "limits", "monitoring"]
FRESH_TABLES = {"superset", "airflow"}

wanted = sys.argv[1:] or SUITES
unknown = set(wanted) - set(SUITES)
if unknown:
    sys.exit(f"unknown suites: {', '.join(sorted(unknown))}; the suites are {', '.join(SUITES)}")

results = {}
for suite in [s for s in SUITES if s in wanted]:
    print(f"\n=== {suite}", flush=True)
    if suite in FRESH_TABLES:
        load_sales_tables()
    started = time.time()
    code = subprocess.run([sys.executable, "-u", f"/tests/{suite}.py"]).returncode
    results[suite] = (code == 0, time.time() - started)

print("\n=== summary")
for suite, (ok, took) in results.items():
    print(f"{'ok  ' if ok else 'FAIL'} {suite}  ({took:.0f}s)")
failed = [s for s, (ok, _) in results.items() if not ok]
sys.exit(f"\n{len(failed)} of {len(results)} suites failed: {', '.join(failed)}" if failed else 0)
