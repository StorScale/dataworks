"""The LLM gateway, and budgets: docker compose run --rm test gateway

A project of the suite's own, with an LLM budget of a dollar a month. These
check (use case 12.1), against the gateway's stand-in (llm_stub: answers that
repeat the question, a cent per hundred characters):

  1. the operator makes the project a key at the gateway, kept as the
     connection gated-llm; the Agents tab's page shows it with no spend; the
     key answers a completion;
  2. a long question takes the spend past 80%: the page shows it, and the
     project is alerted; one past the budget is refused as LiteLLM refuses it
     (HTTP 400, budget exceeded);
  3. the budget raised, the key is a new one, the old one gone;
  4. an agent with a budget, approved by alice, gets a key of its own with
     that budget; revoked, its key goes;
  5. the project removed, its keys go, by the master key's word.

The master key is LLM_MASTER_KEY (the install's). The project is removed after.
"""
import json
import os

import requests

from lakekit import check, finish
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
MASTER = os.environ.get("LLM_MASTER_KEY") or "sk-storscale-local"
NAME = "gated"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "The LLM gateway and budgets: the gateway suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}], "tables": {},
                 "llm": {"budget": 1}}}


def keys(gateway):
    """The project's keys at the gateway, by alias (the master key's view)."""
    r = requests.get(f"{gateway}/key/list", headers={"Authorization": f"Bearer {MASTER}"}, timeout=10)
    return {k["key_alias"]: k for k in (r.json() if r.ok else {}).get("keys", []) if (k.get("key_alias") or "").startswith(f"storscale/{NAME}")}


def ask(gateway, key, text):
    return requests.post(f"{gateway}/chat/completions", headers={"Authorization": f"Bearer {key}"}, timeout=30,
                         json={"model": "default", "messages": [{"role": "user", "content": text}]})


def main():
    bob, alice = platform("bob"), platform("alice")
    gateway = None
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        llm = lambda: bob.get(f"{PLATFORM}/api/projects/{NAME}/llm", timeout=30).json() or {}
        uses = lambda: {u["who"]: u for u in llm().get("uses", [])}
        mine = wait(lambda: (lambda u: u if u.get(NAME, {}).get("key") else None)(uses()), 300, every=10) or uses()
        gateway = llm().get("gateway") or ""
        conns = (bob.get(f"{PLATFORM}/api/projects/{NAME}/connections", timeout=30).json() or {}).get("connections", [])
        conn = next((c for c in conns if c["name"] == f"{NAME}-llm"), {})
        check("bob makes a project with an LLM budget of a dollar; the operator makes it a key, kept as the connection gated-llm",
              r.ok and ready is not None and mine.get(NAME, {}).get("key") is True and conn.get("kind") == "llm" and conn.get("addedBy") == "platform-operator",
              (r.text[:150], mine, conn))
        check("... the Agents tab's page shows the budget, with no spend yet", mine.get(NAME, {}).get("budget") == 1 and mine.get(NAME, {}).get("spend") == 0, mine)
        at = keys(gateway)
        key = at.get(f"storscale/{NAME}", {}).get("key", "")
        check("... and the gateway lists it under the project's name, with its budget", key.startswith("sk-") and at[f"storscale/{NAME}"].get("max_budget") == 1, at)

        # 1. It answers.
        a = ask(gateway, key, "How many orders came in yesterday?")
        content = (a.json() if a.ok else {}).get("choices", [{}])[0].get("message", {}).get("content", "")
        check("the key answers a completion", a.ok and "orders" in content, (a.status_code, a.text[:200]))

        # 2. Near the budget, then past it.
        near = ask(gateway, key, "x" * 9000)   # 90 cents, with the stand-in
        shown = wait(lambda: (lambda u: u if (u.get(NAME, {}).get("share") or 0) >= 0.8 else None)(uses()), 60, every=5) or uses()
        issues = lambda: (bob.get(f"{PLATFORM}/api/projects/{NAME}/alerts", timeout=30).json() or {}).get("issues", [])
        alerts = wait(lambda: (lambda xs: xs if any(i["key"] == f"llm/{NAME}/near" for i in xs) else None)(issues()), 150, every=10) or issues()
        check("a long question takes the spend past 80% of the budget: the page shows it, and the project is alerted",
              near.ok and shown.get(NAME, {}).get("spend", 0) >= 0.9 and any(i["key"] == f"llm/{NAME}/near" and i["kind"] == "llm" for i in alerts),
              (near.status_code, shown, [i["key"] for i in alerts]))
        over = ask(gateway, key, "y" * 12000)
        check("one past the budget is refused, as LiteLLM refuses it", over.status_code == 400 and "udget" in over.text, (over.status_code, over.text[:200]))

        # 3. The budget raised: a new key, the old one gone.
        more = dict(SPEC, spec=dict(SPEC["spec"], llm={"budget": 2}))
        up = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(more), timeout=30)
        renewed = wait(lambda: (lambda k: k if k and k != key else None)(keys(gateway).get(f"storscale/{NAME}", {}).get("key")), 300, every=10)
        fresh = ask(gateway, renewed or key, "Are you back?")
        check("the budget raised, the key is a new one that answers again, the old one gone",
              up.ok and renewed is not None and fresh.ok and uses().get(NAME, {}).get("budget") == 2 and ask(gateway, key, "old?").status_code == 401,
              (up.status_code, up.text[:150], renewed is not None, fresh.status_code, uses()))

        # 4. An agent with a budget of its own.
        reg = bob.put(f"{PLATFORM}/api/projects/{NAME}/agents/helper", headers=H, timeout=30, json={"description": "answers questions", "budget": 0.5})
        ap = alice.post(f"{PLATFORM}/api/projects/{NAME}/agents/helper/approve", headers=H, timeout=30)
        agent = wait(lambda: keys(gateway).get(f"storscale/{NAME}/agent/helper"), 300, every=10) or {}
        check("an agent with a budget, approved by alice, gets a key of its own with that budget, shown on the page",
              reg.ok and ap.ok and agent.get("max_budget") == 0.5 and uses().get("helper", {}).get("key") is True and uses()["helper"].get("budget") == 0.5,
              (reg.text[:150], ap.status_code, agent, uses()))
        rv = bob.delete(f"{PLATFORM}/api/projects/{NAME}/agents/helper", headers=H, timeout=30)
        gone = wait(lambda: True if f"storscale/{NAME}/agent/helper" not in keys(gateway) else None, 180, every=10)
        check("revoked, its key goes", rv.status_code == 204 and gone is True, (rv.status_code, keys(gateway)))
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        if gateway:
            left = wait(lambda: True if not keys(gateway) else None, 120, every=10)
            check("the project removed, its keys go", left is True, keys(gateway))
    finish()


if __name__ == "__main__":
    main()
