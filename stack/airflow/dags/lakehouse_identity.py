"""How this example's pipelines reach the lakehouse: as their own identity, a
Keycloak service account (client airflow-pipelines, in group "pipelines").

Airflow keeps one secret, the client's, in the connection keycloak_pipelines.
Each task gets a short-lived access token for the service account (client
credentials), and then
  - buckets_s3(): Buckets credentials, exchanged for the token through STS,
    with the policies of the token's groups ("pipelines");
  - trino(): a Trino connection signed in with the token, so Ranger applies
    the pipelines group's policies and records the service account by name.
No S3 keys and no Trino password are stored anywhere in Airflow.

report_lineage() tells the catalog (OpenMetadata) what a step read and wrote,
column by column, as an OpenLineage event, with the token the catalog shares
for that (/catalog/token).
"""
import datetime
import os
import uuid

import boto3
import requests
import trino as trino_client
from botocore.config import Config

from airflow.sdk import BaseHook

BUCKETS = "http://buckets:9000"
REALM = "lakehouse"


def token():
    c = BaseHook.get_connection("keycloak_pipelines")
    scheme = c.extra_dejson.get("scheme", "http")
    host = f"{c.host}:{c.port}" if c.port else c.host   # no port: the scheme's own
    r = requests.post(f"{scheme}://{host}/{c.schema}/realms/{REALM}/protocol/openid-connect/token", timeout=30,
                      data={"grant_type": "client_credentials", "client_id": c.login, "client_secret": c.password})
    r.raise_for_status()
    return r.json()["access_token"]


def buckets_s3(access_token=None):
    sts = boto3.client("sts", endpoint_url=BUCKETS, region_name="us-east-1",
                       aws_access_key_id="unused", aws_secret_access_key="unused")
    c = sts.assume_role_with_web_identity(RoleArn="arn:minio:iam:::role/airflow", RoleSessionName="airflow",
                                          WebIdentityToken=access_token or token(), DurationSeconds=900)["Credentials"]
    return boto3.client("s3", endpoint_url=BUCKETS, region_name="us-east-1",
                        aws_access_key_id=c["AccessKeyId"], aws_secret_access_key=c["SecretAccessKey"],
                        aws_session_token=c["SessionToken"], config=Config(s3={"addressing_style": "path"}))


def trino(schema="sales"):
    return trino_client.dbapi.connect(
        host="trino", port=8443, http_scheme="https", verify="/tls/cert.pem",
        user="service-account-airflow-pipelines", auth=trino_client.auth.JWTAuthentication(token()),
        catalog="iceberg", schema=schema)


TRINO_NAMESPACE = "trino://trino:8443"


def report_lineage(pipeline, run_id, inputs, output, columns, sql=None, task=None):
    """Report a pipeline step's lineage to the catalog. columns: output column
    -> the columns it comes from in the first input. Returns the catalog's
    answer, or why there was none."""
    try:
        with open("/catalog/token") as f:
            catalog_token = f.read().strip()
    except OSError:
        return "no catalog token"
    catalog = os.environ.get("CATALOG_URL", "http://openmetadata:8585")
    headers = {"Authorization": f"Bearer {catalog_token}"}
    # The catalog files the lineage under the pipeline "airflow-<pipeline>" in
    # its pipeline service "airflow". It's made here, first: OpenMetadata
    # 2.0.4 fails to make it by itself.
    pipelines_url = os.environ.get("PIPELINES_URL", "")
    r = requests.put(f"{catalog}/api/v1/pipelines", headers=headers, timeout=30, json={
        "name": f"airflow-{pipeline}", "displayName": pipeline, "service": "airflow",
        "description": "An Airflow pipeline of the platform's.",
        **({"sourceUrl": f"{pipelines_url}/dags/{pipeline}"} if pipelines_url else {}),
        **({"tasks": [{"name": task, "displayName": task}]} if task else {})})
    if not r.ok:
        return f"pipeline: HTTP {r.status_code}: {r.text[:200]}"
    source = inputs[0]
    event = {
        "eventType": "COMPLETE",
        "eventTime": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z"),
        "producer": "https://github.com/StorScale/storscale-platform",
        "schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json",
        "run": {"runId": str(uuid.uuid5(uuid.NAMESPACE_URL, f"{pipeline}/{run_id}/{task}"))},
        "job": {"namespace": "airflow", "name": pipeline,
                **({"facets": {"sql": {"query": sql}}} if sql else {})},
        "inputs": [{"namespace": TRINO_NAMESPACE, "name": i} for i in inputs],
        "outputs": [{"namespace": TRINO_NAMESPACE, "name": output, "facets": {"columnLineage": {"fields": {
            col: {"inputFields": [{"namespace": TRINO_NAMESPACE, "name": source, "field": f} for f in fields]}
            for col, fields in columns.items()}}}}],
    }
    r = requests.post(f"{catalog}/api/v1/openlineage/lineage", json=event, headers=headers, timeout=30)
    return r.json().get("message", r.text[:200]) if r.ok else f"HTTP {r.status_code}: {r.text[:200]}"
