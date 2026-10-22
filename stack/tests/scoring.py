"""Batch scoring and drift: docker compose run --rm test scoring

A project of the suite's own, scored, with bob and alice its editors and a
table of customers. These check (use case 15.1, R15.3):

  1. bob trains a model on the customers in the suite (scikit-learn and
     MLflow's client, installed for it), logs and registers it through the
     platform's MLflow, and alice approves the version;
  2. a scoring in the project's spec (the model, the table, its id column,
     an output table and a schedule) is one of the project's pipelines; run,
     as the project's pipelines, it writes a score for every customer with
     the model's version, and records the run with no drift (it's the
     version's reference);
  3. the customers change (their spend, four times over); the next run
     records the drift of that column past the threshold, the project is
     alerted, and the Models tab lists the scoring with its runs.

The project is removed after, and its MLflow workspace with it: the next run's
model is version 1 again.
"""
import json
import os
import random
import subprocess
import sys
import tempfile

from ml import token

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "scored"
H = {"X-Platform-Request": "1"}
SCORING = {"name": "churn-nightly", "model": "churn", "table": "customers", "id": "customer_id", "columns": ["tenure", "spend"],
           "output": "customer_scores", "schedule": "0 3 * * *", "drift": 0.2}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Scoring: the scoring suite's.", "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}],
                 "tables": {}, "pipelines": {"serviceAccount": "airflow-pipelines"}}}
# The versions the platform's scoring runs with (stack/airflow/image/Dockerfile, /opt/score).
PACKAGES = ["mlflow-skinny==3.17.2", "scikit-learn==1.9.1", "skops==0.16.0", "pandas==3.0.6"]


def customers():
    rnd = random.Random(15)
    return [(f"C-{i:04d}", round(rnd.uniform(1, 60), 1), round(rnd.uniform(10, 400), 2)) for i in range(300)]


def train_and_register(rows):
    """A churn model on the customers (short tenure, low spend: likelier to leave), logged and registered as bob."""
    libs = tempfile.mkdtemp(prefix="scoring-libs-")
    subprocess.run([sys.executable, "-m", "pip", "install", "-q", "--disable-pip-version-check", "--target", libs, *PACKAGES], check=True)
    sys.path.insert(0, libs)
    import mlflow
    import pandas as pd
    from sklearn.linear_model import LogisticRegression
    os.environ["MLFLOW_TRACKING_URI"] = f"{PLATFORM}/mlflow/{NAME}"
    os.environ["MLFLOW_TRACKING_TOKEN"] = token("bob")
    df = pd.DataFrame(rows, columns=["customer_id", "tenure", "spend"])
    left = ((df.tenure < 12) & (df.spend < 150)).astype(int)
    left.iloc[:2] = [0, 1]   # both classes, whatever the draw
    mlflow.set_experiment("churn")
    with mlflow.start_run():
        model = LogisticRegression().fit(df[["tenure", "spend"]], left)
        mlflow.log_metric("accuracy", float(model.score(df[["tenure", "spend"]], left)))
        info = mlflow.sklearn.log_model(model, name="model", registered_model_name="churn", pyfunc_predict_fn="predict_proba")
    return str(info.registered_model_version)


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        rows = customers()
        sql("bob", "DROP TABLE IF EXISTS customer_scores", "iceberg", NAME)   # a run before may have left it (the namespace outlives the project)
        sql("bob", "CREATE TABLE IF NOT EXISTS customers (customer_id varchar, tenure double, spend double)", "iceberg", NAME)
        sql("bob", "DELETE FROM customers", "iceberg", NAME)
        sql("bob", "INSERT INTO customers VALUES " + ", ".join(f"('{i}', {t}, {s})" for i, t, s in rows), "iceberg", NAME)
        check("bob makes a project with a table of 300 customers and pipelines", r.ok and ready is not None, r.text[:200])

        # 1. Trained, registered, approved by someone else.
        version = train_and_register(rows)
        ok = alice.post(f"{url}/models/churn/versions/{version}/approve", headers=H, timeout=60)
        models = (bob.get(f"{url}/models", timeout=60).json() or {}).get("models", [])
        check("bob trains churn and registers it through the platform's MLflow; alice approves the version",
              ok.ok and next((m.get("approved") for m in models if m["name"] == "churn"), None) == version, (version, ok.status_code, ok.text[:200]))
        check("... version 1: a project made again starts with an empty workspace (the last run's went with its project)", version == "1", version)

        # 2. A scoring, run as the project's pipelines.
        spec = dict(SPEC, spec=dict(SPEC["spec"], scoring=[SCORING]))
        put = bob.put(url, data=json.dumps(spec), timeout=30)
        dag = f"scoring_{NAME}_churn_nightly"
        pipes = lambda: (bob.get(f"{url}/pipelines", timeout=60).json() or {}).get("pipelines", [])
        loaded = wait(lambda: True if any(p["id"] == dag for p in pipes()) else None, 600, every=10)
        check("a scoring in the spec is one of the project's pipelines", put.ok and loaded, (put.status_code, put.text[:200], [p["id"] for p in pipes()]))

        def run_once():
            started = bob.post(f"{url}/pipelines/{dag}/runs", headers=H, timeout=60) if loaded else None
            run_id = (started.json() if started is not None and started.ok else {}).get("run")
            state = lambda: next((x["state"] for p in pipes() if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
            return wait(state, 900, every=10) if run_id else None

        def latest():
            listed = (bob.get(f"{url}/scoring", timeout=60).json() or {}).get("scoring", [])
            return next((s for s in listed if s["name"] == SCORING["name"]), {})

        first = run_once()
        scores = sql("bob", "SELECT count(*), count(DISTINCT customer_id), min(score), max(score), min(model_version) FROM customer_scores", "iceberg", NAME) if first == "success" else [()]
        n, ids, lo, hi, v = (tuple(scores[0]) + (None,) * 5)[:5]
        sc = latest()
        run = (sc.get("runs") or [{}])[0]
        check("run, it writes a score (a probability) for each of the 300 customers, with the model's version",
              first == "success" and n == 300 and ids == 300 and 0 <= lo <= hi <= 1 and v == version, (first, scores))
        check("... and records the run, the version's reference: no drift",
              run.get("rows") == 300 and run.get("version") == version and max((run.get("drift") or {"": 9}).values()) < 0.05, run)

        # 3. The customers change; the next run sees it.
        sql("bob", "UPDATE customers SET spend = spend * 4 + 400", "iceberg", NAME)
        second = run_once()
        sc = latest()
        run = (sc.get("runs") or [{}])[0]
        drift = run.get("drift") or {}
        check("the customers' spend changes; the next run records that column's drift past the threshold, not tenure's",
              second == "success" and len(sc.get("runs", [])) >= 2 and drift.get("spend", 0) > SCORING["drift"] and drift.get("tenure", 9) < 0.05, (second, run))
        alerted = wait(lambda: next((i for i in (bob.get(f"{url}/alerts", timeout=60).json() or {}).get("issues", [])
                                     if i.get("kind") == "drift" and i.get("key") == f"drift/{SCORING['name']}/{version}"), None), 300, every=10)
        check("... and the project is alerted, naming the column", alerted is not None and "spend" in alerted.get("title", ""), alerted)
        seen = alice.get(f"{url}/scoring", timeout=60)
        check("the Models tab lists the scoring, its DAG, threshold and runs, to the project's people",
              seen.ok and any(s["name"] == SCORING["name"] and s["dag"] == dag and s["threshold"] == SCORING["drift"] and len(s["runs"]) >= 2
                              for s in seen.json().get("scoring", [])), seen.text[:300])
    finally:
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
