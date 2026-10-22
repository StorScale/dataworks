"""Buckets from a JupyterHub notebook, as the person who signed in.

    import buckets_lake
    s3 = buckets_lake.s3()                       # a boto3 S3 client
    s3.put_object(Bucket="home", Key=f"{buckets_lake.username()}/notes.txt", Body=b"...")

    import io, pandas as pd                      # pandas, through the same client
    df = pd.read_parquet(io.BytesIO(s3.get_object(Bucket="sales", Key="datasets/orders.parquet")["Body"].read()))

    orders = buckets_lake.sql("sales", "SELECT * FROM orders LIMIT 100")   # a query, as you, in Trino

No keys live in the notebook. The hub keeps the person's Keycloak tokens (and
refreshes them); this asks the hub for the current one with the server's own
API token, then exchanges it with Buckets' STS (AssumeRoleWithWebIdentity)
for temporary credentials. Their policies follow the person's Keycloak
groups, and their private prefix is home/<username>/. The credentials renew
themselves before they expire.
"""
import datetime
import os
import warnings
from urllib.parse import quote

import boto3
import requests
from botocore.config import Config
from botocore.credentials import RefreshableCredentials
from botocore.session import get_session

ENDPOINT = os.environ.get("BUCKETS_ENDPOINT", "http://buckets:9000")
REGION = os.environ.get("BUCKETS_REGION", "us-east-1")
DURATION = 3600


def username():
    """The signed-in person's name, as Keycloak gives it (preferred_username)."""
    return os.environ["JUPYTERHUB_USER"]


def _hub_user():
    # /users/<name>, not /user: the hub fills in auth_state only on the former.
    r = requests.get(f"{os.environ['JUPYTERHUB_API_URL']}/users/{username()}", timeout=10,
                     headers={"Authorization": f"token {os.environ['JUPYTERHUB_API_TOKEN']}"})
    r.raise_for_status()
    return r.json()


def token():
    """The person's current Keycloak access token, from the hub (which refreshes it)."""
    state = _hub_user().get("auth_state") or {}
    if "access_token" not in state:
        raise RuntimeError("the hub gave no access token: is admin:auth_state!user in the server role?")
    return state["access_token"]


def _fetch():
    sts = boto3.client("sts", endpoint_url=ENDPOINT, region_name=REGION,
                       aws_access_key_id="unused", aws_secret_access_key="unused")
    c = sts.assume_role_with_web_identity(RoleArn="arn:minio:iam:::role/jupyterhub", RoleSessionName=username(),
                                          WebIdentityToken=token(), DurationSeconds=DURATION)["Credentials"]
    return {"access_key": c["AccessKeyId"], "secret_key": c["SecretAccessKey"], "token": c["SessionToken"],
            "expiry_time": c["Expiration"].astimezone(datetime.timezone.utc).isoformat()}


def session():
    """A boto3 session whose credentials renew themselves through the hub and STS."""
    core = get_session()
    core._credentials = RefreshableCredentials.create_from_metadata(
        metadata=_fetch(), refresh_using=_fetch, method="buckets-sts")
    core.set_config_variable("region", REGION)
    return boto3.Session(botocore_session=core)


def s3():
    """A boto3 S3 client for Buckets, as the signed-in person."""
    return session().client("s3", endpoint_url=ENDPOINT, config=Config(s3={"addressing_style": "path"}))


# --- SQL, as the SQL tab runs it ---------------------------------------------------------

ROWS = 100_000   # the most the platform's SQL route answers a script with
_INTEGER = ("tinyint", "smallint", "integer", "bigint")
_REAL = ("real", "double", "decimal")


def sql(project, query):
    """A query's answer as a pandas DataFrame, its columns named: run in Trino
    through the platform's SQL route, as you, under the project's rules and
    with its own table names (FROM orders, not iceberg.<project>.orders).
    Integers come back as int64 (float64 with nulls), reals and decimals as
    float64, dates and timestamps as datetimes. At most 100,000 rows: aggregate
    or sample in the query. A refusal raises, with the platform's reason:
    PermissionError when it's who you are, RuntimeError otherwise."""
    import pandas as pd
    url = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080").rstrip("/")
    r = requests.post(f"{url}/api/projects/{quote(project, safe='')}/sql", json={"sql": query, "limit": ROWS}, timeout=600,
                      headers={"Authorization": f"Bearer {token()}", "X-Platform-Request": "1"})
    try:
        body = r.json()
    except ValueError:
        body = {"error": r.text[:300]}
    if not r.ok:
        why = body.get("error") if isinstance(body, dict) else None
        if r.status_code == 401:
            raise PermissionError(f"the platform refused the query on {project}: {why or 'the token was not accepted'}")
        cls = PermissionError if r.status_code == 403 else RuntimeError
        raise cls(f"the platform refused the query on {project} ({r.status_code}): {why or r.reason}")
    df = pd.DataFrame(body["rows"], columns=body["columns"])
    for i, kind in enumerate(t.split("(")[0].strip() for t in body.get("types") or []):
        col = df.iloc[:, i]
        if kind in _INTEGER:
            df.isetitem(i, col.astype("int64" if col.notna().all() else "float64"))
        elif kind in _REAL:
            df.isetitem(i, col.astype("float64"))
        elif kind == "boolean":
            df.isetitem(i, col.astype("bool" if col.notna().all() else "boolean"))
        elif kind in ("date", "timestamp"):
            try:
                df.isetitem(i, pd.to_datetime(col))
            except (ValueError, TypeError):
                pass   # a time zone pandas doesn't read: left as text
    if len(df) == ROWS:
        warnings.warn(f"the answer has {ROWS} rows, the most the platform's SQL route gives: there may be more", stacklevel=2)
    return df


def mlflow(project):
    """MLflow, in the project's workspace, as you (use case 15.1): the
    platform's MLflow, reached through platformd, which signs you in with
    the token below (renewed every two minutes) and keeps everything in the
    project. Returns the mlflow module, ready: mlflow.set_experiment("churn"),
    mlflow.sklearn.autolog(), mlflow.register_model(...)."""
    import threading

    import mlflow as _mlflow
    os.environ["MLFLOW_TRACKING_URI"] = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080").rstrip("/") + "/mlflow/" + project

    def renew():
        os.environ["MLFLOW_TRACKING_TOKEN"] = token()
        t = threading.Timer(120, renew)
        t.daemon = True
        t.start()
    renew()
    _mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
    return _mlflow
