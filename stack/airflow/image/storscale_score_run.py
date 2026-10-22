"""One run of a project's scoring (use case 15.1, R15.3), in its own
virtualenv (/opt/score/bin/python), as the DAG file storscale_scoring.py
starts it, as the project's pipelines.

STORSCALE_SCORING is the scoring as the platform operator lists it (its
project, catalog, namespace and spec); STORSCALE_TOKEN the pipelines'
Keycloak token.

In order: the model's approved version (models:/<model>@approved), through
the platform's MLflow (/mlflow/<project>, the token as MLflow's own); the
input's rows, read through Trino; their scores, written afresh as the output
table (each row under the input's id column, so it joins back), with the
model's version and the time; each input column's drift from the reference
(the first run's of that version, kept by the platform: the population
stability index over ten bins, or its categories), and the run recorded with
the platform, which alerts the project past the threshold.
"""
import datetime
import json
import math
import numbers
import os

import mlflow
import pandas as pd
import requests
import trino

ps = json.loads(os.environ["STORSCALE_SCORING"])
token = os.environ["STORSCALE_TOKEN"]
s, project, cat, ns = ps["scoring"], ps["project"], ps["catalog"], ps["namespace"]
PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080").rstrip("/")
API = f"{PLATFORM}/api/projects/{project}/scoring/{s['name']}"
H = {"Authorization": f"Bearer {token}", "X-Platform-Request": "1"}


def log(msg):
    print(f"scoring {project}/{s['name']}: {msg}", flush=True)


def connect():
    return trino.dbapi.connect(host=os.environ.get("TRINO_HOST", "trino"), port=8443, http_scheme="https",
                               verify=os.environ.get("TRINO_CA", "/tls/cert.pem"), auth=trino.auth.JWTAuthentication(token),
                               catalog=cat, schema=ns)


def q(name):
    return '"' + name.replace('"', '""') + '"'


def literal(v):
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "NULL"
    if isinstance(v, (int, float)) and not isinstance(v, bool):
        return repr(float(v)) if isinstance(v, float) else str(v)
    return "'" + str(v).replace("'", "''") + "'"


def psi(ref, now):
    """The population stability index of a column's distribution now, against its reference's
    (the bins' or categories' shares, both sides a little smoothed so an empty one doesn't divide by zero)."""
    total = 0.0
    for k in set(ref) | set(now):
        a, b = ref.get(k, 0.0) + 1e-4, now.get(k, 0.0) + 1e-4
        total += (b - a) * math.log(b / a)
    return round(total, 4)


def profile(df, columns, edges=None):
    """Each column's shares: over the reference's ten quantile bins for a number (made here when edges is
    None), else over its ten commonest values and "other". With the edges, to profile a later run alike."""
    out, made = {}, {}
    for c in columns:
        col = df[c]
        if pd.api.types.is_numeric_dtype(col) and not pd.api.types.is_bool_dtype(col):
            e = (edges or {}).get(c)
            if e is None:
                e = sorted(set(col.dropna().quantile([i / 10 for i in range(1, 10)]).tolist()))
                made[c] = e
            bins = pd.cut(col, [-math.inf] + list(e) + [math.inf], labels=False, duplicates="drop")
            shares = bins.value_counts(normalize=True, dropna=False)
            out[c] = {str(int(k)) if k == k else "null": float(v) for k, v in shares.items()}
        else:
            vals = col.astype("string").fillna("null")
            top = (edges or {}).get(c)
            if top is None:
                top = vals.value_counts().head(10).index.tolist()
                made[c] = top
            shares = vals.where(vals.isin(top), "other").value_counts(normalize=True)
            out[c] = {str(k): float(v) for k, v in shares.items()}
    return out, made


def main():
    os.environ["MLFLOW_TRACKING_URI"] = f"{PLATFORM}/mlflow/{project}"
    os.environ["MLFLOW_TRACKING_TOKEN"] = token
    client = mlflow.MlflowClient()
    version = client.get_model_version_by_alias(s["model"], "approved").version
    model = mlflow.pyfunc.load_model(f"models:/{s['model']}@approved")
    log(f"{s['model']} version {version}, the approved one")

    db = connect()
    cur = db.cursor()
    cur.execute(f"SELECT * FROM {q(s['table'])} LIMIT {int(s.get('maxRows') or 100000)}")
    df = pd.DataFrame(cur.fetchall(), columns=[d[0] for d in cur.description])
    columns = s.get("columns") or [c for c in df.columns if c != s["id"]]
    log(f"{len(df)} rows of {s['table']}, columns {', '.join(columns)}")
    scores = model.predict(df[columns])
    # A score per row: of several columns (a classifier's probabilities), the last, the positive class's of two.
    if hasattr(scores, "iloc"):   # a frame or series
        scores = scores.iloc[:, -1] if scores.ndim == 2 else scores
    elif getattr(scores, "ndim", 1) == 2:   # an array
        scores = scores[:, -1]
    scores = [v.item() if hasattr(v, "item") else v for v in scores]   # NumPy's numbers as Python's

    at = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S")
    numeric = all(isinstance(v, numbers.Number) and not isinstance(v, bool) for v in scores)
    out = q(s["output"])
    cur.execute(f"CREATE OR REPLACE TABLE {out} ({q(s['id'])} varchar, score {'double' if numeric else 'varchar'}, model_version varchar, scored_at timestamp(3))")
    cur.fetchall()
    for start in range(0, len(df), 500):
        rows = [f"({literal(str(i))}, {literal(float(v) if numeric else str(v))}, {literal(str(version))}, TIMESTAMP '{at}')"
                for i, v in zip(df[s["id"]].iloc[start:start + 500], scores[start:start + 500])]
        cur.execute(f"INSERT INTO {out} VALUES " + ", ".join(rows))
        cur.fetchall()
    log(f"{len(df)} scores written to {s['output']}")

    # Drift: against the reference of this version, made by its first run.
    r = requests.get(f"{API}/reference", params={"version": version}, headers=H, timeout=60)
    ref = r.json() if r.ok and r.content and r.json() else None
    if not ref:
        prof, edges = profile(df, columns)
        ref = {"version": version, "rows": len(df), "profile": prof, "edges": edges, "at": at}
        requests.put(f"{API}/reference", json=ref, headers=H, timeout=60).raise_for_status()
        log("this run is the reference of the version's drift")
    now, _ = profile(df, columns, ref["edges"])
    drift = {c: psi(ref["profile"].get(c, {}), now[c]) for c in columns}
    mean = (sum(scores) / len(scores)) if numeric and scores else None
    run = {"version": version, "rows": len(df), "drift": drift, "scoreMean": mean, "at": at, "run": os.environ.get("STORSCALE_RUN_ID", "")}
    requests.post(f"{API}/runs", json=run, headers=H, timeout=60).raise_for_status()
    log("drift: " + ", ".join(f"{c} {v}" for c, v in sorted(drift.items(), key=lambda x: -x[1])))


if __name__ == "__main__":
    main()
