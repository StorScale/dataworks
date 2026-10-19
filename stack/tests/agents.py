"""An agent of the project's own: docker compose run --rm test agents

A project of the suite's own (bob and alice both editors, so one can approve
what the other registered). These check (use case 12.2):

  1. bob registers an agent with two of the MCP server's tools; it can't sign
     in yet (no client until it's approved), and bob can't approve his own;
  2. alice approves it: its secret is shown once, the operator makes its
     client, and it signs in with client credentials and asks for a metric
     (Ranger's audit names it); a tool it wasn't given is refused, naming
     what it may use;
  3. a change to its tools takes the approval away;
  4. revoked, it signs in no more.

The project is removed after.
"""
import json
import os
import time

import requests
from semantic import Agent

from lakekit import check, env, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "agented"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "An agent of the project's own: the agents suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}], "tables": {}}}
MODEL = """semantic_model:
  name: orders
  node_relation: {database: iceberg, schema_name: agented, alias: orders}
  defaults: {agg_time_dimension: ordered_on}
  entities:
    - {name: order, type: primary, expr: id}
  dimensions:
    - {name: region, type: categorical}
    - {name: ordered_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: amount_sum, expr: amount, agg: sum}
---
metric:
  name: revenue
  type: simple
  type_params: {measure: amount_sum}
"""


def token_for(client_id, secret):
    """A Keycloak token for an agent's client (client credentials), or None."""
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10,
                      data={"grant_type": "client_credentials", "client_id": client_id, "client_secret": secret})
    return r.json().get("access_token") if r.ok else None


def main():
    bob, alice = platform("bob"), platform("alice")
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "CREATE TABLE orders AS SELECT * FROM (VALUES (1, 'EU', 10.0, DATE '2026-10-01'), (2, 'US', 7.5, DATE '2026-10-02')) "
                   "AS t (id, region, amount, ordered_on)", "iceberg", NAME)
        m = bob.put(f"{PLATFORM}/api/projects/{NAME}/semantic", json={"yaml": MODEL}, headers=H, timeout=120)
        check("bob makes a project with a table and a metric, alice an editor too", r.ok and ready is not None and m.ok, (r.text[:150], m.text[:200]))

        # 1. Registered, not yet approved.
        reg = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/nightly", headers=H, timeout=30,
                      json={"description": "sums the day", "tools": ["list_metrics", "query_metrics"]})
        own = bob.post(f"{PLATFORM}/api/projects/{NAME}/agents/nightly/approve", headers=H, timeout=30)
        listed = (bob.get(f"{PLATFORM}/api/projects/{NAME}/agents", timeout=30).json() or {}).get("agents", [])
        mine = next((a for a in listed if a["name"] == "nightly"), {})
        check("bob registers an agent with two tools; it awaits approval, and bob can't approve his own",
              reg.ok and own.status_code == 403 and mine.get("approved") is False and mine.get("registeredBy") == "bob", (reg.text[:200], own.status_code, mine))
        time.sleep(70)   # a pass of the operator
        check("... and it has no client yet", token_for(f"agent-{NAME}-nightly", "anything") is None)

        # 2. Approved by alice: its client made, it asks for a metric, and only with its tools.
        ap = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/nightly/approve", headers=H, timeout=30)
        aj = ap.json() if ap.ok else {}
        secret = aj.get("secret", "")
        tok = wait(lambda: token_for(aj.get("clientId", ""), secret), 300, every=10) if secret else None
        check("alice approves it: its secret shown once, and within a minute its client signs in", ap.ok and aj.get("approved") is True and tok is not None,
              (ap.status_code, {k: v for k, v in aj.items() if k != "secret"}))
        agent = Agent(tok) if tok else None
        answer, err = agent.call("query_metrics", project=NAME, metrics=["revenue"]) if agent else (None, "no token")
        check("... it asks for the project's revenue as itself", answer is not None and answer.get("rows") and float(answer["rows"][0][0]) == 17.5, (answer, err))
        _, refused = agent.call("list_products") if agent else (None, "no token")
        check("... and a tool it wasn't given is refused, naming what it may use",
              refused is not None and "list_metrics" in refused and "query_metrics" in refused, refused)
        audit = wait(lambda: next((e for e in (bob.get(f"{PLATFORM}/api/projects/{NAME}/audit", timeout=30).json() or {}).get("events", [])
                                   if e.get("action") == "agent.approved"), None), 60, every=5)
        check("the approval is in the audit log", audit is not None, audit)

        # 3. A change to its tools takes the approval away.
        ch = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/nightly", headers=H, timeout=30,
                     json={"description": "sums the day", "tools": ["list_metrics", "query_metrics", "list_products"]})
        listed = (bob.get(f"{PLATFORM}/api/projects/{NAME}/agents", timeout=30).json() or {}).get("agents", [])
        mine = next((a for a in listed if a["name"] == "nightly"), {})
        check("a change to its tools takes the approval away", ch.ok and mine.get("approved") is False, (ch.status_code, mine))
        gone = wait(lambda: True if token_for(aj.get("clientId", ""), secret) is None else None, 180, every=10)
        check("... and its client with it, until approved again", gone is True)

        # 4. Approved again, then revoked.
        ap2 = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/nightly/approve", headers=H, timeout=30)
        secret2 = (ap2.json() if ap2.ok else {}).get("secret", "")
        back = wait(lambda: token_for(aj.get("clientId", ""), secret2), 300, every=10) if secret2 else None
        rv = bob.delete(f"{PLATFORM}/api/projects/{NAME}/agents/nightly", headers=H, timeout=30)
        gone = wait(lambda: True if token_for(aj.get("clientId", ""), secret2) is None else None, 180, every=10) if back else None
        check("approved again it's back, with a new secret; revoked, it signs in no more", back is not None and rv.status_code == 204 and gone is True,
              (ap2.status_code, rv.status_code, gone))
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
