"""Usage and cost per project: docker compose run --rm test usage

A project of the suite's own, metered, with a table and a monthly budget too
small to stay under. These check (use case 13.1):

  1. bob's queries on its table are counted for the project (Trino tells
     platformd as each finishes): how many, their CPU time, who ran them,
     the costliest with their SQL; and priced by the install's rate card;
  2. alice, a reader, doesn't see what it costs; bob (an editor) does;
  3. the platform's page lists it, for the administrators;
  4. past its budget, the project is alerted, saying what it cost.

The project is removed after.
"""
import json
import os
import time

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "metered"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Usage and cost: the usage suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {},
                 "cost": {"budget": 0.000001}}}
MARK = "usage_suite_marker"


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "CREATE TABLE IF NOT EXISTS readings AS SELECT * FROM (VALUES (1, 2.5), (2, 3.5)) AS t (id, value)", "iceberg", NAME)
        for _ in range(3):
            sql("bob", f"SELECT count(*), sum(value) AS {MARK} FROM readings", "iceberg", NAME)
        check("bob makes a project with a monthly budget, and queries its table", r.ok and ready is not None, r.text[:200])

        # 1. Counted for the project, as each query finishes.
        def counted():
            got = bob.get(f"{url}/usage", timeout=60)
            u = (got.json() if got.ok else {}).get("usage") or {}
            return u if (u.get("trino") or {}).get("queries", 0) >= 3 else None
        u = wait(counted, 180, every=10) or {}
        t = u.get("trino") or {}
        check("bob's queries are counted for the project: how many, their CPU time, who ran them, the costliest with their SQL",
              t.get("queries", 0) >= 3 and "bob" in (t.get("byUser") or {}) and t.get("cpuSeconds", 0) > 0
              and any(MARK in q.get("sql", "") for q in t.get("top") or []), {k: t.get(k) for k in ("queries", "cpuSeconds", "byUser")})
        check("... priced by the install's rate card, against the project's budget", (u.get("share") or 0) > 0 and u.get("budget") == 0.000001 and "total" in (u.get("cost") or {}),
              {k: u.get(k) for k in ("cost", "budget", "share")})

        # 2. Who sees it.
        refused = alice.get(f"{url}/usage", timeout=60)
        check("alice, a reader, doesn't see what it costs", refused.status_code == 403, refused.status_code)

        # 3. The platform's page.
        listed = bob.get(f"{PLATFORM}/api/usage", timeout=120)
        rows = {p["project"]: p for p in (listed.json() if listed.ok else {}).get("projects", [])}
        check("the platform's page lists it, for the administrators", NAME in rows and rows[NAME]["trino"]["queries"] >= 3, (listed.status_code, list(rows)[:10]))

        # 4. Past its budget, alerted.
        def alerted():
            got = bob.get(f"{url}/alerts", timeout=60)
            return next((i for i in (got.json() if got.ok else {}).get("issues", []) if i.get("kind") == "cost"), None)
        issue = wait(alerted, 600, every=15)
        check("past its budget, the project is alerted, saying what it cost", issue is not None and "past its budget" in issue.get("title", ""), issue)
    finally:
        sql("bob", "DROP TABLE IF EXISTS readings", "iceberg", NAME)
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        time.sleep(1)
    finish()


if __name__ == "__main__":
    main()
