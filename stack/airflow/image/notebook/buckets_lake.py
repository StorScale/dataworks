"""Buckets from a scheduled notebook (use case 7.7), as the project's pipelines.

The same calls as JupyterLab's buckets_lake (s3(), session(), username(),
token()), so a notebook runs on a schedule as it ran by hand; but signed in
as the project's pipelines' service account, not the person: its Keycloak
token (client credentials, from the environment the run gives the kernel),
exchanged with Buckets' STS for temporary credentials, which renew
themselves.
"""
import datetime
import os

import boto3
import requests
from botocore.config import Config
from botocore.credentials import RefreshableCredentials
from botocore.session import get_session

ENDPOINT = os.environ.get("BUCKETS_ENDPOINT", "http://buckets:9000")
REGION = os.environ.get("BUCKETS_REGION", "us-east-1")
DURATION = 3600


def username():
    """Who the notebook runs as: the pipelines' service account."""
    return os.environ.get("STORSCALE_RUN_AS", "pipelines")


def token():
    """The pipelines' current Keycloak access token."""
    r = requests.post(os.environ["STORSCALE_TOKEN_URL"], timeout=30, data={
        "grant_type": "client_credentials", "client_id": os.environ["STORSCALE_CLIENT_ID"],
        "client_secret": os.environ["STORSCALE_CLIENT_SECRET"]})
    r.raise_for_status()
    return r.json()["access_token"]


def _fetch():
    sts = boto3.client("sts", endpoint_url=ENDPOINT, region_name=REGION,
                       aws_access_key_id="unused", aws_secret_access_key="unused")
    c = sts.assume_role_with_web_identity(RoleArn="arn:minio:iam:::role/airflow", RoleSessionName="notebook",
                                          WebIdentityToken=token(), DurationSeconds=DURATION)["Credentials"]
    return {"access_key": c["AccessKeyId"], "secret_key": c["SecretAccessKey"], "token": c["SessionToken"],
            "expiry_time": c["Expiration"].astimezone(datetime.timezone.utc).isoformat()}


def session():
    """A boto3 session whose credentials renew themselves through Keycloak and STS."""
    core = get_session()
    core._credentials = RefreshableCredentials.create_from_metadata(
        metadata=_fetch(), refresh_using=_fetch, method="buckets-sts")
    core.set_config_variable("region", REGION)
    return boto3.Session(botocore_session=core)


def s3():
    """A boto3 S3 client for Buckets, as the project's pipelines."""
    return session().client("s3", endpoint_url=ENDPOINT, config=Config(s3={"addressing_style": "path"}))
