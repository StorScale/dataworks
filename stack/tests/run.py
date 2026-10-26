"""The platform's end-to-end checks: docker compose run --rm test [suite ...]

Runs the Buckets integration guides' checks against the one platform, in two
lanes. The chain: the suites that share the lakehouse's sales project, one
after another (the sales tables are loaded afresh before each suite that
reads them; projects deletes the sales project and makes it again, after the
suites that use it; monitoring runs last, failing one of Buckets' four drives
for a minute, as a failing disk would). Alongside, once health has had its
turn (it breaks things on purpose): the suites with projects of their own,
one at a time, their output shown as each finishes. The chain waits for them
before monitoring.
"""
import subprocess
import sys
import threading
import time

from lakekit import load_sales_tables

# The chain, in order; then the suites with projects of their own.
CHAIN = ["realm", "health", "lakehouse", "superset", "airflow", "jupyterhub", "catalog", "semantic", "projects", "project_alerts",
         "audit", "maintenance", "history", "sources", "copies", "imports", "deliveries", "monitoring"]
OWN = ["business", "entra", "limits", "pipelines_git", "reviews", "releases", "notebooks", "flows", "classification", "agents", "erasure", "gateway", "knowledge", "usage", "api", "apps", "ml", "scoring", "serving", "roles", "templates"]
SUITES = CHAIN + OWN
FRESH_TABLES = {"superset", "airflow"}
# Run only when named, in this order, around storscale backup and restore (restore.py).
ON_REQUEST = ["restore_mark", "restore_damage", "restore_check"]
# Run only when named, and leave what they make: the lab guide, against an install (guide.py).
BY_NAME = ["guide"]

wanted = sys.argv[1:] or SUITES
unknown = set(wanted) - set(SUITES) - set(ON_REQUEST) - set(BY_NAME)
if unknown:
    sys.exit(f"unknown suites: {', '.join(sorted(unknown))}; the suites are {', '.join(SUITES + ON_REQUEST + BY_NAME)}")

results = {}
printing = threading.Lock()


def run_suite(suite, capture):
    """One suite: its exit code and time. capture: its output kept and shown
    whole when it's done (the own lane), else streamed (the chain)."""
    if suite in FRESH_TABLES:
        load_sales_tables()
    started = time.time()
    script = ["/tests/restore.py", suite] if suite in ON_REQUEST else [f"/tests/{suite}.py"]
    if not capture:
        print(f"\n=== {suite}", flush=True)
        code = subprocess.run([sys.executable, "-u", *script]).returncode
    else:
        done = subprocess.run([sys.executable, "-u", *script], capture_output=True, text=True)
        code = done.returncode
        with printing:
            print(f"\n=== {suite} (alongside the chain, {time.time() - started:.0f}s)\n{done.stdout}{done.stderr}", flush=True)
    results[suite] = (code == 0, time.time() - started)


chain = [s for s in CHAIN + ON_REQUEST + BY_NAME if s in wanted]
own = [s for s in OWN if s in wanted]
alongside = None


def start_own():
    global alongside
    if own and alongside is None:
        alongside = threading.Thread(target=lambda: [run_suite(s, capture=True) for s in own], daemon=True)
        alongside.start()


if "health" not in chain:
    start_own()
for suite in chain:
    if suite == "monitoring" and alongside:
        alongside.join()
    run_suite(suite, capture=False)
    if suite == "health":
        start_own()
start_own()
if alongside:
    alongside.join()

print("\n=== summary")
for suite in [s for s in SUITES + ON_REQUEST + BY_NAME if s in results]:
    ok, took = results[suite]
    print(f"{'ok  ' if ok else 'FAIL'} {suite}  ({took:.0f}s)")
failed = [s for s in results if not results[s][0]]
sys.exit(f"\n{len(failed)} of {len(results)} suites failed: {', '.join(failed)}" if failed else 0)
