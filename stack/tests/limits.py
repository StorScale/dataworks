"""Limits per project, end to end: docker compose run --rm test limits

A project's limits cap its share of the platform, so one project can't slow
the others (docs/use-cases.md, 6.8). These make a project, limits-test, with
bob as its editor and small limits (one query at a time, one waiting, a
1Gi bucket), and check:

  1. bob's query naming the project (client tag project:limits-test) runs
     in the project's resource group; his query naming none runs in his
     own; alice's naming it, who isn't a member, runs in hers;
  2. with one of the project's queries running and one waiting, a third is
     refused, and the refusal names the project's group;
  3. the project's bucket has its size limit.

Then it deletes the project.
"""
import json
import os
import re
import time

import requests

from lakekit import BUCKETS, check, env, finish, mc, token
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
TRINO = "https://trino:8443"
NAME = "limits-test"
# Long enough to hold its place while the others are tried: it's cancelled after.
LONG = ("SELECT count(*) FROM UNNEST(sequence(1, 10000)) a(x) CROSS JOIN UNNEST(sequence(1, 10000)) b(y) "
        "CROSS JOIN UNNEST(sequence(1, 1000)) c(z)")


def submit(user, statement, tags=None):
    """Start a statement through Trino's API; its first answer."""
    h = {"Authorization": f"Bearer {token(user)}", "X-Trino-User": user, "X-Trino-Catalog": "iceberg"}
    if tags:
        h["X-Trino-Client-Tags"] = ",".join(tags)
    r = requests.post(f"{TRINO}/v1/statement", data=statement, headers=h, verify="/tls/cert.pem", timeout=30)
    r.raise_for_status()
    return {"user": user, "h": h, **r.json()}


def advance(q, until, timeout=60):
    """Follow a query's answers until its state is one of `until`, or it fails.
    Trino only queues a statement once its client first asks after it: the
    first answer, from the POST, always says QUEUED."""
    deadline, asked = time.time() + timeout, False
    while time.time() < deadline:
        state = q.get("stats", {}).get("state")
        if asked and (state in until or q.get("error") or not q.get("nextUri")):
            return q
        if not q.get("nextUri"):
            return q
        r = requests.get(q["nextUri"], headers=q["h"], verify="/tls/cert.pem", timeout=30)
        q, asked = {"user": q["user"], "h": q["h"], **r.json()}, True
        time.sleep(0.5)
    return q


def finished(q):
    while q.get("nextUri"):
        q = {"user": q["user"], "h": q["h"],
             **requests.get(q["nextUri"], headers=q["h"], verify="/tls/cert.pem", timeout=60).json()}
    return q


def group_of(user, statement, tags=None):
    """The resource group a statement ran in, as Trino says (its path)."""
    q = finished(submit(user, statement, tags))
    for attempt in range(3):   # a connection cut mid-answer (it happens, with Trino's workers about) is asked again
        try:
            info = requests.get(f"{TRINO}/v1/query/{q['id']}", headers={"Authorization": f"Bearer {token(user)}"},
                                verify="/tls/cert.pem", timeout=30)
            break
        except (requests.exceptions.ChunkedEncodingError, requests.exceptions.ConnectionError):
            if attempt == 2:
                raise
    return ".".join(info.json().get("resourceGroupId") or []) if info.ok else f"HTTP {info.status_code}"


def cancel(q):
    requests.delete(f"{TRINO}/v1/query/{q['id']}", headers={"Authorization": f"Bearer {token(q['user'])}"},
                    verify="/tls/cert.pem", timeout=30)


def bucket_quota(bucket):
    mc("alias", "set", "buckets", BUCKETS, env["BUCKETS_ROOT_USER"], env["BUCKETS_ROOT_PASSWORD"])
    out = mc("quota", "info", f"buckets/{bucket}", "--json", check=False)
    try:
        return json.loads(out.stdout).get("quota")
    except ValueError:
        return out.stdout.strip()[:120] or out.stderr.strip()[:120]


def main():
    bob = platform("bob")
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "The limits suite's.", "members": [{"user": "bob", "role": "editor"}],
                     "files": {}, "limits": {"queries": 1, "queued": 1, "storage": "1Gi"}}}
    try:
        put = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        ready = wait(lambda: ((projects(bob) or {}).get(NAME) or {}).get("status", {}).get("phase") == "Ready", 180)
        check("a project with limits is applied", put.ok and ready, f"HTTP {put.status_code}")

        # 1. Which group a query runs in. (The operator writes Trino's groups
        # within seconds of the project's members being known.)
        tag = [f"project:{NAME}"]
        mine = wait(lambda: (lambda g: g if g == f"global.project-{NAME}" else None)(group_of("bob", "SELECT 1", tag)), 60)
        check("bob's query naming the project runs in the project's group", mine, mine or group_of("bob", "SELECT 1", tag))
        his = group_of("bob", "SELECT 1")
        check("... his query naming none, in his own", his == "global.people.bob", his)
        hers = group_of("alice", "SELECT 1", tag)
        check("... and alice's naming it, who isn't a member, in hers", hers == "global.people.alice", hers)

        # 2. One running, one waiting: a third is refused.
        first = advance(submit("bob", LONG, tag), {"RUNNING"})
        second = advance(submit("bob", LONG, tag), {"QUEUED", "RUNNING"}, timeout=10)
        third = advance(submit("bob", "SELECT 1", tag), {"FINISHED"})
        err = (third.get("error") or {})
        check("with one of the project's queries running and one waiting, a third is refused",
              first.get("stats", {}).get("state") == "RUNNING" and second.get("stats", {}).get("state") == "QUEUED"
              and err.get("errorName") == "QUERY_QUEUE_FULL",
              f"{first.get('stats', {}).get('state')}, {second.get('stats', {}).get('state')}, {err.get('errorName') or third.get('stats', {}).get('state')}")
        check("... saying which limit", re.search(rf"project-{NAME}", err.get("message", "")) is not None, err.get("message", "")[:160])
        for q in (first, second):
            cancel(q)

        # 3. The bucket's size.
        quota = bucket_quota(NAME)
        check("the project's bucket has its size limit (1Gi)", quota == 1 << 30, quota)
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 120)
    finish()


if __name__ == "__main__":
    main()
