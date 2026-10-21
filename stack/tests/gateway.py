"""The LLM gateway, and budgets: docker compose run --rm test gateway

A project of the suite's own, with an LLM budget of a dollar a month. These
check (use case 12.1), against the gateway's stand-in (llm_stub: answers that
repeat the question, a cent per hundred characters):

  1. the operator makes the project a key at the gateway, kept as the
     connection gated-llm; the Agents tab's page shows it with no spend; the
     key answers a completion; a question carrying a card number and an email
     address, or one of the platform's secrets, is refused by the guardrail
     (HTTP 400, naming what was found and never the value), and costs nothing;
  2. questions take the spend past 80%: the page shows it, and the project is
     alerted; one past the budget is refused as LiteLLM refuses it (HTTP 400,
     budget exceeded). At the stand-in one long question does it (a cent per
     hundred characters); at a real gateway (LiteLLM on the company's models,
     whose prices are its own) the budget is a few cents and questions of a
     few hundred tokens are asked until the spend gets there;
  3. the budget raised, the key is a new one, the old one gone;
  4. an agent with a budget, approved by alice, gets a key of its own with
     that budget; revoked, its key goes;
  5. the project removed, its keys go, by the master key's word.

The master key is LLM_MASTER_KEY (the install's). The project is removed after.
"""
import json
import os
import time

import k8s
import requests

from lakekit import check, finish, s3_root
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
MASTER = os.environ.get("LLM_MASTER_KEY") or "sk-storscale-local"
GATEWAY = os.environ.get("LLM_GATEWAY_URL", "http://llm:4000")
NAME = "gated"
H = {"X-Platform-Request": "1"}


def is_real():
    """LiteLLM answers /health/readiness (its version and models); the stand-in doesn't."""
    try:
        return requests.get(f"{GATEWAY}/health/readiness", timeout=10).ok
    except requests.RequestException:
        return False


REAL = is_real()
BUDGET = 0.03 if REAL else 1   # dollars a month: a few cents at a real gateway, a dollar at the stand-in's prices
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "The LLM gateway and budgets: the gateway suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}], "tables": {},
                 "llm": {"budget": BUDGET}}}


def keys(gateway):
    """The project's keys at the gateway, by alias (the master key's view): each with its id, the key
    itself at the stand-in, its hash at LiteLLM (which lists keys by their hash, never the key)."""
    r = requests.get(f"{gateway}/key/list", params={"return_full_object": "true", "size": 100}, headers={"Authorization": f"Bearer {MASTER}"}, timeout=10)
    out = {}
    for k in (r.json() if r.ok else {}).get("keys", []):
        if isinstance(k, dict) and (k.get("key_alias") or "").startswith(f"storscale/{NAME}"):
            out[k["key_alias"]] = {**k, "id": k.get("key") or k.get("token")}
    return out


def raw_key(conn):
    """The key the operator made, from the connection it keeps it in (the platform shows it to no one):
    the project's Secret on Kubernetes, connections/<project>/<name>.json in the platform's bucket on Compose."""
    if k8s.in_cluster():
        return k8s.get_secret(conn).get("llmKey", "")
    try:
        d = json.loads(s3_root().get_object(Bucket="storscale-platform", Key=f"connections/{NAME}/{conn}.json")["Body"].read())
    except Exception:   # not there yet
        return ""
    return d.get("llmKey") or (d.get("data") or {}).get("llmKey") or ""


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
        check("... the Agents tab's page shows the budget, with no spend yet", mine.get(NAME, {}).get("budget") == BUDGET and mine.get(NAME, {}).get("spend") == 0, mine)
        at = keys(gateway)
        key = raw_key(f"{NAME}-llm")
        check("... and the gateway lists it under the project's name, with its budget", key.startswith("sk-") and at.get(f"storscale/{NAME}", {}).get("max_budget") == BUDGET, (key[:3], at))

        # 1. It answers.
        a = ask(gateway, key, "How many orders came in yesterday?")
        content = (a.json() if a.ok else {}).get("choices", [{}])[0].get("message", {}).get("content", "")
        check("the key answers a completion", a.ok and (bool(content.strip()) if REAL else "orders" in content), (a.status_code, a.text[:200]))   # a real model's words are its own
        calls = lambda: bob.get(f"{PLATFORM}/api/projects/{NAME}/llm/calls", timeout=60).json() or {}
        seen = wait(lambda: (lambda c: c if (c.get("total") or {}).get("calls") else None)(calls()), 120, every=5) or calls()   # LiteLLM writes its logs a moment later
        first = (seen.get("calls") or [{}])[-1]
        check("the project's model calls (12.8): the call, whose key, the model, tokens, cost and time, never the question",
              (seen.get("total") or {}).get("calls", 0) >= 1 and first.get("who") == NAME and first.get("model") and first.get("tokens", 0) > 0
              and "orders came in" not in json.dumps(seen), (seen.get("total"), first, seen.get("errors")))
        spent = lambda: keys(gateway).get(f"storscale/{NAME}", {}).get("spend")
        before = spent()
        masked = ask(gateway, key, "Write to jane.doe@example.com that card 4111 1111 1111 1111 was declined")
        secret = ask(gateway, key, "My config has LLM_MASTER_KEY=sk-0123456789abcdef0123456789abcdef, is that right?")
        check("a question carrying a card number and an email address is refused by the guardrail, naming what was found and never the value",
              masked.status_code == 400 and "a card number" in masked.text and "an email address" in masked.text
              and "4111" not in masked.text and "jane" not in masked.text, (masked.status_code, masked.text[:300]))
        if not k8s.in_cluster():   # Compose keeps "Project Falcon" in (LLM_FORBIDDEN_TERMS); a chart install names its own terms
            term = ask(gateway, key, "Draft a teaser for project falcon's launch")
            check("... one naming a term the company keeps in (Compose's: Project Falcon), naming the kind and not the term",
                  term.status_code == 400 and "a term the company keeps in" in term.text and "falcon" not in term.text.lower(), (term.status_code, term.text[:300]))
        check("... one carrying a secret of the platform's too, and neither costs anything",
              secret.status_code == 400 and "a gateway key" in secret.text and "sk-0123" not in secret.text and spent() == before,
              (secret.status_code, secret.text[:300], before, spent()))

        # 2. Near the budget, then past it.
        answered = False
        if REAL:   # questions of a few hundred tokens, until the spend LiteLLM records (a moment after each) is past 80%
            words = " ".join(f"item {i} costs {i * 7 % 101} and ships in {i % 9} days;" for i in range(40))
            for _ in range(40):
                near = ask(gateway, key, "Summarise this list in one line: " + words)
                answered = answered or near.ok
                time.sleep(4)
                if not near.ok or (uses().get(NAME, {}).get("share") or 0) >= 0.8:
                    break
        else:
            near = ask(gateway, key, "x" * 9000)   # 90 cents, with the stand-in
            answered = near.ok
        shown = wait(lambda: (lambda u: u if (u.get(NAME, {}).get("share") or 0) >= 0.8 else None)(uses()), 60, every=5) or uses()
        issues = lambda: (bob.get(f"{PLATFORM}/api/projects/{NAME}/alerts", timeout=30).json() or {}).get("issues", [])
        alerted = lambda xs: [i for i in xs if i["kind"] == "llm" and i["key"] in (f"llm/{NAME}/near", f"llm/{NAME}")]   # at 80%, or at the budget
        alerts = wait(lambda: (lambda xs: xs if alerted(xs) else None)(issues()), 150, every=10) or issues()
        check("questions take the spend past 80% of the budget: the page shows it, and the project is alerted",
              answered and (shown.get(NAME, {}).get("share") or 0) >= 0.8 and alerted(alerts), (near.status_code, shown, [i["key"] for i in alerts]))
        for _ in range(10):   # the stand-in refuses the first; LiteLLM refuses once its recorded spend is past the budget
            over = ask(gateway, key, "y" * 12000 if not REAL else "Summarise this list in one line: " + words)
            if over.status_code in (400, 422) or not REAL:
                break
            time.sleep(4)
        check("one past the budget is refused, as LiteLLM refuses it (HTTP 422, budget exceeded)", over.status_code in (400, 422) and "udget" in over.text,
              (over.status_code, over.text[:200]))

        # 3. The budget raised: a new key, the old one gone.
        more = dict(SPEC, spec=dict(SPEC["spec"], llm={"budget": BUDGET * 2}))
        up = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(more), timeout=30)
        first = at.get(f"storscale/{NAME}", {}).get("id")
        renewed = wait(lambda: (lambda k: k if k and k != first else None)(keys(gateway).get(f"storscale/{NAME}", {}).get("id")), 300, every=10)
        new_key = wait(lambda: (lambda k: k if k and k != key else None)(raw_key(f"{NAME}-llm")), 120, every=5) or key
        fresh = ask(gateway, new_key, "Are you back?")
        check("the budget raised, the key is a new one that answers again, the old one gone",
              up.ok and renewed is not None and fresh.ok and uses().get(NAME, {}).get("budget") == BUDGET * 2 and ask(gateway, key, "old?").status_code == 401,
              (up.status_code, up.text[:150], renewed is not None, fresh.status_code, fresh.text[:200], uses()))

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
