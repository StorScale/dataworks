"""The semantic layer, and agents: docker compose run --rm test semantic

The sales project defines its metrics once (projects/sales.semantic.yaml).
People and agents ask for metrics by name; each question runs in Trino as
whoever asked. These check, with agents talking MCP to the platform at /mcp:

  1. the MCP server is an OAuth resource server: without a token it refuses,
     pointing to its metadata, which names Keycloak; a token that isn't for
     it is refused too;
  2. an agent signed in as alice finds the sales project and its metrics,
     and asks for revenue by region: it gets the EU only, as alice does in
     Trino; by card number, the cards are masked; it can see the SQL;
  3. Ranger's audit log has the agent's queries, under alice's name and
     naming the agent;
  4. carol's agent gets nothing: no projects, and no answers;
  5. bob (an editor) changes what revenue means, in the platform, and
     alice's agent's next answer follows it; alice can't change it.
"""
import html
import json
import os
import re
import time

import requests

from lakekit import check, env, finish

PLATFORM = os.environ["PLATFORM_URL"]
MCP = PLATFORM + "/mcp"
TOKENS = f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token"


def agent_token(user, client="storscale-agent"):
    r = requests.post(TOKENS, timeout=10, data={"grant_type": "password", "client_id": client, "username": user,
                                                 "password": env["LAKEHOUSE_USER_PASSWORD"]})
    r.raise_for_status()
    return r.json()["access_token"]


class Agent:
    """A minimal MCP client (streamable HTTP, JSON responses), as an agent would be."""

    def __init__(self, token):
        self.h = {"Accept": "application/json, text/event-stream", "Authorization": f"Bearer {token}"}
        self.n = 0
        self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                "clientInfo": {"name": "storscale-checks", "version": "1"}})

    def rpc(self, method, params=None):
        self.n += 1
        r = requests.post(MCP, headers=self.h, timeout=120, json={"jsonrpc": "2.0", "id": self.n, "method": method,
                                                                  **({"params": params} if params else {})})
        r.raise_for_status()
        return r.json()

    def call(self, tool, **args):
        """(result, error): a tool's structured result, or its error text."""
        res = self.rpc("tools/call", {"name": tool, "arguments": args}).get("result", {})
        text = " ".join(c.get("text", "") for c in res.get("content", []))
        if res.get("isError"):
            return None, text
        out = res.get("structuredContent")
        if out is None:  # the result as JSON text
            try:
                out = json.loads(text)
            except ValueError:
                out = text
        if isinstance(out, dict) and set(out) == {"result"}:
            out = out["result"]
        return out, None


def platform(user):
    s = requests.Session()
    r = s.get(f"{PLATFORM}/auth/login?next=/", timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    s.post(html.unescape(form.group(1)), timeout=60,
           data={"username": user, "password": env["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    s.headers["X-Platform-Request"] = "1"
    return s


def ranger_audits(user, text, timeout=90):
    """Ranger's audit records of user's requests whose statement contains text."""
    deadline = time.time() + timeout
    while True:
        r = requests.get("http://ranger-solr:8983/solr/ranger_audits/select", timeout=10,
                         params={"q": f"reqUser:{user}", "rows": 500, "fl": "reqData,reqUser,result", "sort": "evtTime desc"})
        docs = [d for d in r.json()["response"]["docs"] if text in (d.get("reqData") or "")] if r.ok else []
        if docs or time.time() > deadline:
            return docs
        time.sleep(5)


def main():
    # 1. An OAuth resource server.
    r = requests.post(MCP, json={"jsonrpc": "2.0", "id": 1, "method": "initialize"},
                      headers={"Accept": "application/json, text/event-stream"}, timeout=30)
    meta_url = re.search(r'resource_metadata="([^"]+)"', r.headers.get("www-authenticate", ""))
    meta = requests.get(meta_url.group(1), timeout=10).json() if meta_url else {}
    check("without a token, the MCP server refuses, and points to its metadata", r.status_code == 401 and meta_url,
          f"HTTP {r.status_code}, {r.headers.get('www-authenticate', '')[:60]}")
    check("... which names Keycloak as the authorization server",
          meta.get("resource") == MCP and meta.get("authorization_servers") == [f"{env['KEYCLOAK_URL']}/realms/lakehouse"],
          meta.get("authorization_servers"))
    other = requests.post(MCP, timeout=30, json={"jsonrpc": "2.0", "id": 1, "method": "initialize"},
                          headers={"Accept": "application/json, text/event-stream",
                                   "Authorization": f"Bearer {agent_token('alice', client='lakehouse')}"}).status_code
    check("a token that isn't for the MCP server is refused", other == 401, f"a lakehouse-client token: HTTP {other}")

    # 2. alice's agent.
    alice = Agent(agent_token("alice"))
    projects, err = alice.call("list_projects")
    check("alice's agent finds the sales project, with metrics", {"project": "sales", "role": "reader", "has_metrics": True} in (projects or []),
          projects or err)
    model, err = alice.call("list_metrics", project="sales")
    names = sorted(m["name"] for m in (model or {}).get("metrics", []))
    check("... and its metrics", names == ["average_order_value", "customer_count", "order_count", "revenue"], names or err)
    by_region, err = alice.call("query_metrics", project="sales", metrics=["revenue", "order_count"], group_by=["order__region"])
    regions = {row[0] for row in (by_region or {}).get("rows", [])}
    check("revenue by region, for alice's agent: the EU only, as for alice in Trino", regions == {"EU"},
          (by_region or {}).get("rows") or err)
    eu_revenue = float(by_region["rows"][0][1]) if by_region and by_region.get("rows") else None
    by_card, err = alice.call("query_metrics", project="sales", metrics=["order_count"], group_by=["order__card_number"])
    cards = [row[0] for row in (by_card or {}).get("rows", [])]
    check("... grouped by card number, the cards are masked", cards and all(c is None or c.startswith("X") for c in cards),
          cards[:3] or err)
    sql, err = alice.call("explain_query", project="sales", metrics=["revenue"], group_by=["order__region"])
    check("... and it can see the SQL", sql and "FROM iceberg.sales.orders" in sql, (sql or err or "")[-80:].replace("\n", " "))

    # 3. Ranger's audit log: alice's name, and the agent's.
    audits = ranger_audits("alice", "agent=storscale-agent")
    check("Ranger's audit log has the agent's queries, under alice's name, naming the agent", len(audits) > 0,
          f"{len(audits)} records by alice from agent=storscale-agent")

    # 4. carol's agent.
    carol = Agent(agent_token("carol"))
    projects, err = carol.call("list_projects")
    answer, aerr = carol.call("query_metrics", project="sales", metrics=["revenue"])
    check("carol's agent gets nothing: no projects, no answers", projects == [] and answer is None and "not a member" in (aerr or ""),
          f"projects {projects}, query: {aerr}")

    # 5. bob changes revenue; alice's agent follows.
    bob, alice_p = platform("bob"), platform("alice")
    original = bob.get(f"{PLATFORM}/api/projects/sales/semantic", timeout=60).json()["yaml"]
    net = original.replace("- {name: amount_sum, expr: amount, agg: sum, description: The orders' amounts}",
                           "- {name: amount_sum, expr: amount * 0.9, agg: sum, description: \"The orders' amounts, net of a 10% fee\"}")
    denied = alice_p.put(f"{PLATFORM}/api/projects/sales/semantic", json={"yaml": net}, timeout=60).status_code
    check("alice (a reader) can't change the metrics", denied == 403, f"HTTP {denied}")
    bad = bob.put(f"{PLATFORM}/api/projects/sales/semantic", json={"yaml": "metric: {name: oops}"}, timeout=60)
    check("a definition that doesn't compile is refused, saying why", bad.status_code == 422, bad.json().get("error", "")[:100])
    # The model's tests (12.3): run as bob; a change that breaks one refused.
    ran = bob.post(f"{PLATFORM}/api/projects/sales/semantic/tests", json={}, headers={"X-Platform-Request": "1"}, timeout=120)
    rj = ran.json() if ran.ok else {}
    check("the model's tests run from the platform, and pass", ran.ok and rj.get("failed") == 0 and rj.get("passed", 0) >= 2
          and all(t["passed"] for t in rj.get("tests", [])), (ran.status_code, ran.text[:300]))
    renamed = original.replace("name: order_count", "name: orders_count")
    refused = bob.put(f"{PLATFORM}/api/projects/sales/semantic", json={"yaml": renamed}, timeout=60)
    check("a change that breaks a test (its metric renamed) is refused, saying which", refused.status_code == 422
          and "one_order_count" in refused.json().get("error", "") and "order_count" in refused.json().get("error", ""), refused.text[:200])
    saved = bob.put(f"{PLATFORM}/api/projects/sales/semantic", json={"yaml": net}, timeout=60)
    check("bob (an editor) changes revenue, in the platform", saved.ok and net != original, f"HTTP {saved.status_code}")
    after, err = alice.call("query_metrics", project="sales", metrics=["revenue"], group_by=["order__region"])
    new = float(after["rows"][0][1]) if after and after.get("rows") else None
    check("alice's agent's next answer follows the new definition",
          eu_revenue and new and abs(new - eu_revenue * 0.9) < 0.01, f"EU revenue {eu_revenue} -> {new}")
    via_platform = alice_p.post(f"{PLATFORM}/api/projects/sales/semantic/query", timeout=60,
                                json={"metrics": ["revenue"], "group_by": ["order__region"]}).json()
    check("... as does alice's, in the platform", via_platform.get("rows") and float(via_platform["rows"][0][1]) == new,
          via_platform.get("rows") or via_platform.get("error"))
    bob.put(f"{PLATFORM}/api/projects/sales/semantic", json={"yaml": original}, timeout=60)
    finish()


if __name__ == "__main__":
    main()
