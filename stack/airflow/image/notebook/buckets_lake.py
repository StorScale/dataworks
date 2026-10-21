"""Buckets from a scheduled notebook (use case 7.7), as the project's pipelines.

The same calls as JupyterLab's buckets_lake (s3(), session(), username(),
token()), so a notebook runs on a schedule as it ran by hand; but signed in
as the project's pipelines' service account, not the person: its Keycloak
token (client credentials, from the environment the run gives the kernel),
exchanged with Buckets' STS for temporary credentials, which renew
themselves.
"""
import datetime
import json
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


# --- As an agent (use case 12.2): the MCP server, and the LLM gateway -------------------

class _MCP:
    """A minimal MCP client (streamable HTTP, JSON answers), signed in as this run's identity."""

    def __init__(self, url):
        self.url, self.n = url, 0
        self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                "clientInfo": {"name": "storscale-agent", "version": "1"}})

    def rpc(self, method, params=None):
        self.n += 1
        r = requests.post(self.url, timeout=120, json={"jsonrpc": "2.0", "id": self.n, "method": method, **({"params": params} if params else {})},
                          headers={"Accept": "application/json, text/event-stream", "Authorization": f"Bearer {token()}"})
        r.raise_for_status()
        return r.json()

    def tools(self):
        """The tools this agent may use, by name."""
        return [t["name"] for t in self.rpc("tools/list").get("result", {}).get("tools", [])]

    def call(self, tool, **args):
        """A tool's structured result; a refusal raises, naming what the agent may use."""
        res = self.rpc("tools/call", {"name": tool, "arguments": args}).get("result", {})
        text = " ".join(c.get("text", "") for c in res.get("content", []))
        if res.get("isError"):
            raise PermissionError(text)
        out = res.get("structuredContent")
        if out is None:
            try:
                out = json.loads(text)
            except ValueError:
                out = text
        if isinstance(out, dict) and set(out) == {"result"}:
            out = out["result"]
        return out


def agent():
    """The project's MCP server, as the agent this notebook runs as (STORSCALE_MCP_URL)."""
    url = os.environ.get("STORSCALE_MCP_URL")
    if not url:
        raise RuntimeError("not an agent's run: no STORSCALE_MCP_URL (an agent's notebook runs from its schedule)")
    return _MCP(url)


def ask(prompt, model="default", system=None):
    """A model's answer through the platform's gateway, on this agent's key and budget (STORSCALE_LLM_KEY)."""
    url, key = os.environ.get("STORSCALE_LLM_URL"), os.environ.get("STORSCALE_LLM_KEY")
    if not url or not key:
        raise RuntimeError("no gateway key: give the agent a budget (its registration) and the operator makes one")
    messages = ([{"role": "system", "content": system}] if system else []) + [{"role": "user", "content": prompt}]
    r = requests.post(f"{url.rstrip('/')}/chat/completions", timeout=300, headers={"Authorization": f"Bearer {key}"},
                      json={"model": model, "messages": messages, "user": username()})
    if r.status_code in (400, 422) and "udget" in r.text:   # LiteLLM answers 422 past the budget (400 before 1.8x)
        raise RuntimeError("the agent is at its LLM budget: the gateway answers it no more this month")
    if r.status_code == 400 and "guardrail" in r.text:   # what may not leave the platform: the gateway names it, never the value
        try:
            why = r.json()["error"]
            why = why.get("message") if isinstance(why, dict) else why
        except (ValueError, KeyError, AttributeError):
            why = r.text[:300]
        raise RuntimeError(f"the gateway refused the question: {why}")
    r.raise_for_status()
    return r.json()["choices"][0]["message"]["content"]
