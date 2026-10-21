"""An agent of the project's own: docker compose run --rm test agents

A project of the suite's own (bob and alice both editors, so one can approve
what the other registered). These check (use case 12.2):

  1. bob registers an agent with two of the MCP server's tools; it can't sign
     in yet (no client until it's approved), and bob can't approve his own;
  2. alice approves it: its secret is shown once, the operator makes its
     client, and it signs in with client credentials and asks for a metric
     (Ranger's audit names it) and for the project's alerts (platformd, by
     the agent's own token); a tool it wasn't given is refused, naming
     what it may use;
  3. a change to its tools takes the approval away;
  4. revoked, it signs in no more;
  5. an agent with a prompt and no notebook (12.7), on a schedule, with a
     budget: approved, it's a pipeline of the project's, and a run has the
     platform's runner do the prompt with its tools, as the agent, through
     the gateway on its key; the run's record is the transcript;
  6. a tool of the project's own (Keycloak's userinfo, signed in by an API
     connection holding alice's token) and the agent of 5 given to another
     agent (tool:whoami, agent:agented/triage): it lists them, calls the
     tool (the platform sends the token, the agent never sees it), and asks
     triage, which answers with its report; both in the audit log; an agent
     not given them is refused.

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
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}], "tables": {},
                 "llm": {"budget": 1}}}
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
        sql("bob", "DROP TABLE IF EXISTS orders", "iceberg", NAME)  # a run before, on a cluster that keeps a deleted project's tables
        sql("bob", "CREATE TABLE orders AS SELECT * FROM (VALUES (1, 'EU', 10.0, DATE '2026-10-01'), (2, 'US', 7.5, DATE '2026-10-02')) "
                   "AS t (id, region, amount, ordered_on)", "iceberg", NAME)
        m = bob.put(f"{PLATFORM}/api/projects/{NAME}/semantic", json={"yaml": MODEL}, headers=H, timeout=120)
        check("bob makes a project with a table and a metric, alice an editor too", r.ok and ready is not None and m.ok, (r.text[:150], m.text[:200]))

        # 1. Registered, not yet approved.
        reg = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/nightly", headers=H, timeout=30,
                      json={"description": "sums the day", "tools": ["list_metrics", "query_metrics", "list_alerts"]})
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
        alerts, aerr = agent.call("list_alerts", project=NAME) if agent else (None, "no token")
        check("... and reads the project's alerts as itself (none yet)", alerts is not None and isinstance(alerts.get("issues"), list), (alerts, aerr))
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

        # 5. An agent with a prompt and no notebook, run by the platform (12.7).
        reg = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/triage", headers=H, timeout=30,
                      json={"description": "sums up the open alerts", "tools": ["list_alerts"], "schedule": "0 6 * * *", "budget": 0.5,
                            "prompt": "Read the project's open alerts (list_alerts) and sum them up in two sentences for the team."})
        ap3 = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/triage/approve", headers=H, timeout=30)
        dag = "agent_" + NAME + "_triage"
        listed = lambda: (bob.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60).json() or {}).get("pipelines", [])
        loaded = wait(lambda: True if any(p["id"] == dag for p in listed()) else None, 600, every=10)
        started = bob.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{dag}/runs", headers=H, timeout=60) if loaded else None
        run_id = (started.json() if started is not None and started.ok else {}).get("run")
        state = lambda: next((x["state"] for p in listed() if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
        final = wait(state, 600, every=10) if run_id else None
        record = bob.get(f"{PLATFORM}/api/projects/{NAME}/notebooks/runs/{dag}/{requests.utils.quote(run_id or '', safe='')}", timeout=60) if final else None
        page = record.text if record is not None and record.ok else ""
        check("an agent with a prompt and no notebook: approved, its pipeline runs the prompt with its tools as the agent, the transcript its record",
              reg.ok and ap3.ok and final == "success" and "list_alerts" in page and "The report" in page,
              (reg.status_code, reg.text[:200], ap3.status_code, bool(loaded), final, page[:300]))

        # 6. A tool of the project's own, and an agent as a tool (12.7).
        kc_token = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10,
                                 data={"grant_type": "password", "client_id": "storscale-agent", "username": "alice", "scope": "openid",
                                       "password": env["LAKEHOUSE_USER_PASSWORD"]}).json().get("access_token", "")
        conn = bob.post(f"{PLATFORM}/api/projects/{NAME}/connections", headers=H, timeout=30,
                        json={"name": f"{NAME}-kc", "kind": "api", "values": {"token": kc_token}})
        spec = (bob.get(f"{PLATFORM}/api/projects/{NAME}", timeout=30).json() or {}).get("spec") or {}
        spec.setdefault("spec", {})["tools"] = [{"name": "whoami", "description": "Who the connection's token belongs to.", "method": "GET",
                                                 "url": "http://keycloak:8080/sso/realms/lakehouse/protocol/openid-connect", "path": "/userinfo",   # as platformd reaches it
                                                 "connection": f"{NAME}-kc"}]
        put = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(spec), timeout=30)
        reg = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/asker", headers=H, timeout=30,
                      json={"description": "asks the others", "tools": ["tool:whoami", f"agent:{NAME}/triage"]})
        ap4 = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/asker/approve", headers=H, timeout=30)
        a4 = ap4.json() if ap4.ok else {}
        tok4 = wait(lambda: token_for(a4.get("clientId", ""), a4.get("secret", "")), 300, every=10) if a4.get("secret") else None
        asker = Agent(tok4) if tok4 else None
        listed, lerr = asker.call("list_tools", project=NAME) if asker else (None, "no token")
        check("an agent given a tool of the project's and another agent lists them",
              conn.ok and put.ok and reg.ok and listed is not None and [t["name"] for t in listed.get("tools", [])] == ["whoami"]
              and [a["name"] for a in listed.get("agents", [])] == ["triage"], (conn.status_code, put.status_code, put.text[:200], reg.status_code, listed, lerr))
        who, werr = asker.call("call_tool", project=NAME, tool="whoami") if asker else (None, "no token")
        check("... calls the tool: the platform signs in with the connection's token (alice's), which the agent never sees",
              who is not None and who.get("status") == 200 and (who.get("answer") or {}).get("preferred_username") == "alice" and kc_token not in json.dumps(who),
              (werr, str(who)[:300]))
        asked, aerr = asker.call("ask_agent", project=NAME, agent="triage", task="Sum up the open alerts (list_alerts) for the team.") if asker else (None, "no token")
        check("... and asks triage, which does it with its own tools, as itself, and reports",
              asked is not None and asked.get("report") and [s["tool"] for s in asked.get("steps", [])] == ["list_alerts"], (aerr, str(asked)[:300]))
        bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/plain", headers=H, timeout=30, json={"description": "reads alerts", "tools": ["list_alerts"]})
        ap5 = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/plain/approve", headers=H, timeout=30)
        a5 = ap5.json() if ap5.ok else {}
        tok5 = wait(lambda: token_for(a5.get("clientId", ""), a5.get("secret", "")), 300, every=10) if a5.get("secret") else None
        plain = Agent(tok5) if tok5 else None
        _, refused = plain.call("ask_agent", project=NAME, agent="triage", task="sum up") if plain else (None, "no token")
        audit = wait(lambda: {e.get("action") for e in (bob.get(f"{PLATFORM}/api/projects/{NAME}/audit", timeout=30).json() or {}).get("events", [])}
                     >= {"tool.called", "agent.asked"} or None, 60, every=5)
        check("an agent not given them is refused, naming what it may use; the calls are in the audit log",
              refused is not None and "list_alerts" in refused and audit is True, (refused, audit))
        for a in ("asker", "plain", "triage"):
            bob.delete(f"{PLATFORM}/api/projects/{NAME}/agents/{a}", headers=H, timeout=30)
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
