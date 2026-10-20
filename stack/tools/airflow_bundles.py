"""Run one of Airflow's processes with the DAG bundles the platform operator
says to: the platform's own DAGs, and each project's pipelines repo
(internal/reconcile/airflow.go).

    python airflow_bundles.py airflow scheduler        (and dag-processor)

The list goes to the process in its settings, and to a file the process
reads instead, through the image's airflow_local_settings.py, whenever it
reads the list. The DAG processor reads it only as it starts, so when the
list changes this starts it again. The scheduler never reads it itself:
each task's process does, as the task starts, so the scheduler runs on
(and the tasks it runs with it); the file keeps it current, so a run of a
project's pipeline started as soon as the DAG appears finds its bundle.

OPERATOR_URL: the operator (http://platform-operator:8081). The list holds
repos' addresses and connections' names, never credentials: those are in
Airflow's connections. BUNDLES_FILE: where the list is kept
(/tmp/storscale-bundles.json).
"""
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081")
LOCAL = [{"name": "dags-folder", "classpath": "airflow.dag_processing.bundles.local.LocalDagBundle", "kwargs": {}},
         {"name": "storscale", "classpath": "airflow.dag_processing.bundles.local.LocalDagBundle", "kwargs": {"path": "/opt/storscale/dags"}}]
POLL = int(os.environ.get("BUNDLES_POLL_SECONDS", "5"))
FILE = os.environ.get("BUNDLES_FILE", "/tmp/storscale-bundles.json")


def log(msg):
    print(f"airflow_bundles: {msg}", file=sys.stderr, flush=True)


def fetch():
    """The operator's list, or None if it can't be had; [the platform's own]
    when the operator runs no projects' repos."""
    try:
        with urllib.request.urlopen(OPERATOR + "/airflow/bundles", timeout=5) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        return LOCAL if e.code == 404 else None
    except (OSError, ValueError):
        return None


def canonical(bundles):
    return json.dumps(bundles, sort_keys=True)


def keep(bundles):
    """The list in the file, whole at every moment (written beside, then moved over)."""
    tmp = FILE + ".tmp"
    with open(tmp, "w") as f:
        f.write(canonical(bundles))
    os.replace(tmp, FILE)
    log("bundles: " + ", ".join(b["name"] for b in bundles))


def start(argv, bundles):
    keep(bundles)
    env = dict(os.environ, AIRFLOW__DAG_PROCESSOR__DAG_BUNDLE_CONFIG_LIST=canonical(bundles), STORSCALE_BUNDLES=FILE)
    return subprocess.Popen(argv, env=env)


def stop(child, grace=120):
    child.send_signal(signal.SIGTERM)
    try:
        child.wait(grace)
    except subprocess.TimeoutExpired:
        child.kill()
        child.wait()


def main():
    argv = sys.argv[1:]
    if not argv:
        sys.exit(__doc__)
    bundles, deadline = fetch(), time.time() + 60
    while bundles is None and time.time() < deadline:  # the operator may be starting too
        time.sleep(3)
        bundles = fetch()
    if bundles is None:
        log(f"the operator ({OPERATOR}) isn't answering: the platform's own DAGs only, for now")
        bundles = LOCAL
    child = start(argv, bundles)

    def forward(signum, _):
        stop(child)
        sys.exit(child.returncode or 0)
    signal.signal(signal.SIGTERM, forward)
    signal.signal(signal.SIGINT, forward)

    restarts = argv[-1] != "scheduler"   # the DAG processor reads the list as it starts; the scheduler's tasks read the file
    while True:
        time.sleep(POLL)
        if child.poll() is not None:
            sys.exit(child.returncode)
        now = fetch()
        if now is None or canonical(now) == canonical(bundles):
            continue
        before, after = {b["name"] for b in bundles}, {b["name"] for b in now}
        change = f"bundles changed (added {sorted(after - before) or 'none'}, removed {sorted(before - after) or 'none'}, or settings)"
        bundles = now
        if not restarts:
            log(change)
            keep(bundles)
            continue
        log(f"{change}: starting {argv[-1]} again")
        stop(child)
        child = start(argv, bundles)


if __name__ == "__main__":
    main()
