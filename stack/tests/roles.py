"""Roles of the company's own: docker compose run --rm test roles

bob, an administrator, defines a role, steward: read every table as it is,
and confirm the catalog's tags; nothing else. A project of the suite's own,
stewarded, gives it to alice; carol is its reader, under its row filter and
mask. These check (use case 16.1):

  1. alice, a steward, reads every row of orders with its card numbers
     whole in Trino, where carol (a reader) sees the EU's rows, masked;
     the Access tab names the role and what it gives;
  2. she may confirm the catalog's tags, and nothing a steward isn't given:
     no table made, no file uploaded, no tag made;
  3. bob takes away read-all and classify: within minutes she reads as a
     reader does, and may no longer confirm tags; the change is in the
     audit log;
  4. a role a project gives can't be removed; alice (no administrator)
     can't define one; once no project gives it, it's removed.

The project and the role are removed after.
"""
import json
import os
import time

from lakekit import check, denied, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "stewarded"
ROLE = "steward"
H = {"X-Platform-Request": "1"}
MEMBERS = [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": ROLE}, {"user": "carol", "role": "reader"}]
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Roles: the roles suite's.", "members": MEMBERS, "files": {},
                 "tables": {"readers": {"rowFilters": [{"table": "orders", "filter": "region = 'EU'"}],
                                        "masks": [{"table": "orders", "column": "card", "type": "MASK"}]}}}}
ORDERS = [(1, "EU", "4111111111111111"), (2, "US", "5500000000000004"), (3, "EU", "340000000000009")]


def reads(user):
    """What a person reads of orders: its rows' ids and their cards, or Trino's refusal."""
    try:
        return sorted(sql(user, "SELECT id, card FROM orders", "iceberg", NAME))
    except Exception as e:  # noqa: BLE001
        return str(e).splitlines()[0][:120]


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{NAME}"
    role_url = f"{PLATFORM}/api/roles/{ROLE}"
    try:
        defined = bob.put(role_url, headers=H, timeout=30, json={"description": "Reads every table as it is and confirms the catalog's tags; changes nothing",
                                                                 "permissions": ["read-all", "read-files", "classify"]})
        refused = alice.put(f"{PLATFORM}/api/roles/sneaky", headers=H, timeout=30, json={"permissions": ["read-all"]})
        check("bob, an administrator, defines steward; alice can't define a role", defined.ok and refused.status_code == 403,
              (defined.status_code, defined.text[:200], refused.status_code))

        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "CREATE TABLE IF NOT EXISTS orders (id integer, region varchar, card varchar)", "iceberg", NAME)
        sql("bob", "DELETE FROM orders", "iceberg", NAME)
        sql("bob", "INSERT INTO orders VALUES " + ", ".join(f"({i}, '{g}', '{c}')" for i, g, c in ORDERS), "iceberg", NAME)
        member = next((m for m in ((ready or {}).get("status") or {}).get("members", []) if m["username"] == "alice"), {})
        check("bob makes a project that gives alice steward and carol reader; the platform records what alice may do",
              r.ok and ready is not None and member.get("role") == ROLE and "read-all" in member.get("can", []) and "build" not in member.get("can", []),
              (r.status_code, r.text[:200], member))

        # 1. A steward reads as it is; a reader under the readers' rules.
        whole = [(i, c) for i, _, c in ORDERS]
        got_alice = wait(lambda: (lambda x: x if x == whole else None)(reads("alice")), 300, every=10) or reads("alice")
        got_carol = reads("carol")
        check("alice, a steward, reads every order with its card whole; carol, a reader, the EU's, masked",
              got_alice == whole and [i for i, _ in got_carol] == [1, 3] and all(c != "4111111111111111" for _, c in got_carol), (got_alice, got_carol))
        roles = (alice.get(f"{PLATFORM}/api/roles", timeout=30).json() or {}).get("roles", [])
        steward = next((x for x in roles if x["name"] == ROLE), {})
        check("the roles name steward, what it gives and the project giving it", steward.get("projects") == [NAME] and "classify" in steward.get("granted", []), steward)

        # 2. What a steward may do, and not.
        classify = lambda s: s.post(f"{url}/classification", headers=H, timeout=30, json={"table": "orders", "column": "card", "tag": "personal", "action": "inspect"})
        may = classify(alice)
        made = denied("alice", "CREATE TABLE notes (x integer)", "iceberg", NAME)
        upload = alice.post(f"{url}/uploads", headers=H, timeout=30, files={"file": ("a.csv", "x\n1\n", "text/csv")})
        tag = alice.post(f"{url}/tags", headers=H, timeout=30, json={"name": "nope", "at": "now", "keep": "1d"})
        check("alice may confirm the catalog's tags (her request is checked, not refused), and may not make a table, upload or make a tag",
              may.status_code not in (401, 403) and made is not None and upload.status_code == 403 and tag.status_code == 403,
              (may.status_code, made, upload.status_code, tag.status_code))

        # 3. The role changed: every project giving it follows.
        changed = bob.put(role_url, headers=H, timeout=30, json={"description": "Reads as readers do", "permissions": ["read", "read-files"]})
        masked = wait(lambda: (lambda x: x if isinstance(x, list) and [i for i, _ in x] == [1, 3] else None)(reads("alice")), 600, every=15)
        check("bob takes read-all and classify away: within minutes alice reads the EU's orders, masked, as carol does",
              changed.ok and masked is not None and all(c != "4111111111111111" for _, c in masked), (changed.status_code, reads("alice")))
        no_more = wait(lambda: True if classify(alice).status_code == 403 else None, 120, every=5)
        log = (bob.get(f"{PLATFORM}/api/audit", params={"source": "platform", "q": "role"}, timeout=60).json() or {}).get("events", [])
        saved = [e for e in log if e["action"] == "role.saved" and e["resource"] == f"role:{ROLE}"]
        check("... and may no longer confirm tags; the change is in the audit log, with what it was",
              no_more is True and any("was read-all" in e.get("detail", "") for e in saved), (no_more, saved[:2]))

        # 4. Removing it.
        in_use = bob.delete(role_url, headers=H, timeout=30)
        bob.put(url, data=json.dumps(dict(SPEC, spec=dict(SPEC["spec"], members=[m for m in MEMBERS if m["role"] != ROLE]))), timeout=30)
        time.sleep(2)
        gone = bob.delete(role_url, headers=H, timeout=30)
        check("a role a project gives can't be removed; once none does, it is", in_use.status_code == 409 and gone.status_code == 204,
              (in_use.status_code, in_use.text[:200], gone.status_code))
    finally:
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        bob.delete(role_url, headers=H, timeout=30)
    finish()


if __name__ == "__main__":
    main()
