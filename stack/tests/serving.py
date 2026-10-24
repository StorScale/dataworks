"""Models as an API: docker compose run --rm test serving

A project of the suite's own, served, with bob and alice its editors. These
check (use case 15.1, R15.4):

  1. bob trains churn and registers it; alice approves the version. A
     serving in the project's spec answers with it, through the platform;
  2. alice calls it signed in (a Keycloak token): a prediction for each row,
     with the version that answered; carol (no member) is refused, and both
     the call and the refusal are in the project's audit log, rows not kept;
  3. a call over the serving's rows is refused;
  4. bob registers a better version and alice approves it: the serving
     answers with it within a couple of minutes, nobody restarting anything;
  5. a member whose role doesn't give call-models (carol, as a looker) is
     refused, naming the permission.

The project is removed after.
"""
import json
import os

import requests
from ml import token
from scoring import customers, train_and_register

from lakekit import check, finish
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "served"
H = {"X-Platform-Request": "1"}
SERVING = {"name": "churn", "model": "churn", "maxRows": 5}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Serving: the serving suite's.", "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}]}}
ROLE = "serving-looker"
ROWS = [{"tenure": 3.0, "spend": 40.0}, {"tenure": 48.0, "spend": 320.0}, {"tenure": 20.0, "spend": 120.0}]


def predict(user, rows):
    r = requests.post(f"{PLATFORM}/api/projects/{NAME}/serving/{SERVING['name']}/predict", timeout=120,
                      headers={"Authorization": f"Bearer {token(user)}"}, json={"dataframe_records": rows})
    return r, (r.json() if r.headers.get("content-type", "").startswith("application/json") else {})


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        check("bob makes a project", r.ok and ready is not None, r.text[:200])

        # 1. Registered, approved, served.
        rows = customers()
        first = train_and_register(rows, project=NAME)
        ok = alice.post(f"{url}/models/churn/versions/{first}/approve", headers=H, timeout=60)
        put = bob.put(url, data=json.dumps(dict(SPEC, spec=dict(SPEC["spec"], serving=[SERVING]))), timeout=30)
        listed = lambda: (bob.get(f"{url}/serving", timeout=60).json() or {}).get("serving", [])
        serving = wait(lambda: next((s for s in listed() if s["name"] == SERVING["name"] and s.get("version") == first), None), 900, every=10)
        check("a serving in the spec answers with churn's approved version, through the platform",
              ok.ok and put.ok and serving is not None and serving["url"].endswith(f"/api/projects/{NAME}/serving/churn/predict"), (ok.status_code, put.text[:200], listed()))

        # 2. Called, signed in; refused to someone else; both in the audit log.
        got, body = predict("alice", ROWS)
        preds = body.get("predictions") or []
        check("alice calls it with a Keycloak token: a prediction for each of the three rows (the chances of each class), from the approved version",
              got.ok and len(preds) == 3 and all(len(p) == 2 and abs(sum(p) - 1) < 1e-6 for p in preds) and body.get("version") == first
              and preds[0][1] > preds[1][1], (got.status_code, body))
        refused, _ = predict("carol", ROWS)
        check("carol, no member of the project, is refused", refused.status_code == 403, refused.status_code)
        log = lambda: (bob.get(f"{url}/audit", params={"source": "platform", "q": "model"}, timeout=60).json() or {}).get("events", [])
        seen = wait(lambda: (lambda ev: ev if any(e["action"] == "model.called" and e["user"] == "alice" for e in ev)
                             and any(e["action"] == "model.call" and e["user"] == "carol" and not e.get("allowed") for e in ev) else None)(log()), 120, every=5)
        called = next((e for e in seen or [] if e["action"] == "model.called"), {})
        check("the project's audit log has alice's call (the version, three rows, not the rows) and carol's refusal",
              seen is not None and f"version {first}, 3 rows" in called.get("detail", "") and "spend" not in called.get("detail", ""), called)

        # 3. The serving's limit.
        too_many, body = predict("alice", ROWS * 2)
        check("a call over the serving's five rows is refused, saying so", too_many.status_code == 413 and "5" in body.get("error", ""), (too_many.status_code, body))

        # 4. A new version approved: served without a restart.
        second = train_and_register(rows, project=NAME)
        ok = alice.post(f"{url}/models/churn/versions/{second}/approve", headers=H, timeout=60)
        moved = wait(lambda: (lambda b: b if b.get("version") == second else None)(predict("alice", ROWS[:1])[1]), 240, every=10)
        check("bob registers version 2 and alice approves it: the serving answers with it within minutes",
              ok.ok and second != first and moved is not None, (first, second, ok.status_code, moved))

        # 5. A role without call-models.
        role = bob.put(f"{PLATFORM}/api/roles/{ROLE}", headers=H, timeout=30, json={"description": "Reads, calls no models", "permissions": ["read"]})
        members = SPEC["spec"]["members"] + [{"user": "carol", "role": ROLE}]
        put = bob.put(url, data=json.dumps(dict(SPEC, spec=dict(SPEC["spec"], members=members, serving=[SERVING]))), timeout=30)
        joined = wait(lambda: next((m for m in (((projects(bob) or {}).get(NAME) or {}).get("status") or {}).get("members", [])
                                    if m["username"] == "carol" and m["role"] == ROLE), None), 300, every=10)
        refused, body = predict("carol", ROWS[:1])
        check("carol, a member as a looker (read, not call-models), is refused, naming the permission",
              role.ok and put.ok and joined is not None and refused.status_code == 403 and "call-models" in body.get("error", ""),
              (role.status_code, put.text[:150], joined, refused.status_code, body))
    finally:
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        bob.delete(f"{PLATFORM}/api/roles/{ROLE}", headers=H, timeout=30)   # once no project gives it
    finish()


if __name__ == "__main__":
    main()
