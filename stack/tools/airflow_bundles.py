"""Run one of Airflow's processes with the DAG bundles the platform operator
says to: the platform's own DAGs, and each project's pipelines repo
(internal/reconcile/airflow.go). Airflow reads its bundles only as it
starts, so when they change this starts the process again.

    python airflow_bundles.py airflow scheduler        (and dag-processor)

OPERATOR_URL: the operator (http://platform-operator:8081). The list holds
repos' addresses and connections' names, never credentials: those are in
Airflow's connections.
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
POLL = int(os.environ.get("BUNDLES_POLL_SECONDS", "15"))


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


def start(argv, bundles):
    env = dict(os.environ, AIRFLOW__DAG_PROCESSOR__DAG_BUNDLE_CONFIG_LIST=canonical(bundles))
    log("bundles: " + ", ".join(b["name"] for b in bundles))
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

    while True:
        time.sleep(POLL)
        if child.poll() is not None:
            sys.exit(child.returncode)
        now = fetch()
        if now is None or canonical(now) == canonical(bundles):
            continue
        before, after = {b["name"] for b in bundles}, {b["name"] for b in now}
        log(f"bundles changed (added {sorted(after - before) or 'none'}, removed {sorted(before - after) or 'none'}, "
            f"or settings): starting {argv[-1]} again")
        stop(child)
        bundles = now
        child = start(argv, bundles)


if __name__ == "__main__":
    main()
