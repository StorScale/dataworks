"""The audit log, end to end: docker compose run --rm test audit

What people do in the platform is recorded beside Ranger's record of every
access Trino allows or refuses, in Ranger's Solr. These check, with sales:

  1. bob's change to the project is in its audit log, saying what changed;
  2. alice's query of a table she may not read is refused, and the refusal
     is in the project's audit log;
  3. alice (a reader) can't read the project's audit log, nor the
     platform's; bob (an administrator) exports the platform's as CSV;
  4. signing in is recorded.
"""
import csv
import io
import json
import os

from lakekit import check, denied, finish
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT = "sales"


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    log = lambda s, path, **q: s.get(f"{PLATFORM}{path}", params=q, timeout=30)

    # 1. A change to the project.
    spec = bob.get(url, timeout=30).json()["spec"]
    before = spec["spec"].get("description", "")
    spec["spec"]["description"] = before + " (audited)"
    r = bob.put(url, data=json.dumps(spec), timeout=30)
    spec["spec"]["description"] = before
    bob.put(url, data=json.dumps(spec), timeout=30)
    check("bob changes the project's description, and back", r.ok, r.text[:160])
    saved = wait(lambda: [e for e in log(bob, f"/api/projects/{PROJECT}/audit", source="platform", user="bob").json().get("events", [])
                          if e["action"] == "project.saved" and "description" in (e.get("detail") or "")], 60, every=3)
    check("... and the project's audit log says so: who, and what changed", bool(saved),
          saved[0] if saved else log(bob, f"/api/projects/{PROJECT}/audit", source="platform").json())

    # 2. A refusal, as Ranger records it.
    why = denied("alice", "SELECT * FROM payroll", "iceberg", PROJECT)
    check("alice may not read payroll", why is not None, why)
    refused = wait(lambda: [e for e in log(bob, f"/api/projects/{PROJECT}/audit", result="denied", user="alice").json().get("events", [])
                            if "payroll" in e["resource"] and not e["allowed"]], 120, every=5)
    check("... and the refusal is in the project's audit log, from Trino", bool(refused) and refused[0]["source"] == "trino",
          refused[0] if refused else log(bob, f"/api/projects/{PROJECT}/audit", user="alice").json())

    # 3. Who reads it.
    code = log(alice, f"/api/projects/{PROJECT}/audit").status_code
    check("alice (a reader) can't read the project's audit log", code == 403, f"HTTP {code}")
    code = log(alice, "/api/audit").status_code
    check("... nor the platform's", code == 403, f"HTTP {code}")
    r = log(bob, "/api/audit", user="alice", result="denied", format="csv")
    rows = list(csv.reader(io.StringIO(r.text))) if r.ok else []
    check("bob exports the platform's audit log as CSV, the refusal in it",
          r.ok and r.headers.get("Content-Type", "").startswith("text/csv") and rows and rows[0][:5] == ["time", "user", "action", "resource", "result"]
          and any("payroll" in row[3] and row[4] == "denied" for row in rows[1:]), r.text[:300])

    # 4. Signing in.
    signed = wait(lambda: [e for e in log(bob, "/api/audit", source="platform", user="alice").json().get("events", [])
                           if e["action"] == "signed.in"], 30, every=3)
    check("signing in is recorded", bool(signed), signed[:1])
    finish()


if __name__ == "__main__":
    main()
