"""A new column of personal data, protected by its tag: docker compose run --rm test classification

A project of the suite's own, with a table of customers. These check (use
case 12.5):

  1. the column the project declares as identifying a person (tables.people)
     is tagged Protection.personal in the catalog, confirmed;
  2. the catalog finds the columns that look personal (email addresses,
     phone numbers) and tags them, suggested; the Data page's classification
     shows them, and the project's alerts say a column was protected;
  3. alice (a reader) sees the email column masked in SQL without anyone
     writing a policy (the operator made one from the tag);
  4. bob (an editor) confirms the email's tag, and removes the phone's: alice
     reads phone numbers again, and the tag isn't suggested back;
  5. alice can't change a tag.

The project is removed after.
"""
import json
import os
import time

import requests

from lakekit import check, env, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
OM = "http://openmetadata:8585/api/v1"
NAME = "classified"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Personal data by tag: the classification suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}],
                 "tables": {"people": [{"table": "customers", "column": "customer_id"}]}}}


def classification(s):
    r = s.get(f"{PLATFORM}/api/projects/{NAME}/classification", timeout=30)
    return {(c["table"], c["column"], c["tag"]): c for c in (r.json().get("columns", []) if r.ok else [])}


def tagged(s, column):
    return {k: v for k, v in classification(s).items() if k[1] == column}


def catalog():
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    r.raise_for_status()
    s = requests.Session()
    s.headers["Authorization"] = f"Bearer {r.json()['access_token']}"
    return s


def column_tags(om, column):
    r = om.get(f"{OM}/tables/name/trino.iceberg.{NAME}.customers", params={"fields": "columns,tags"}, timeout=30)
    if not r.ok:
        return None
    for c in r.json().get("columns", []):
        if c["name"] == column:
            return {t["tagFQN"]: t.get("state") for t in c.get("tags") or []}
    return None


def reads(user, column):
    """What alice sees of a column: the first value."""
    try:
        # Ana's row, by her name: customer_id, hashed for alice, would order by its hash.
        rows = sql(user, f"SELECT {column} FROM customers WHERE name = 'Ana'", "iceberg", NAME)
        return rows[0][0] if rows else None
    except Exception as e:  # noqa: BLE001 (refused, or not yet)
        return f"error: {str(e)[:300]}"


def main():
    bob, alice = platform("bob"), platform("alice")
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "DROP TABLE IF EXISTS customers", "iceberg", NAME)  # a run before, on a cluster that keeps a deleted project's tables
        sql("bob", "CREATE TABLE customers AS SELECT * FROM (VALUES "
                   "(1, 'Ana', 'ana@example.com', '+1 415 555 0101', 'likes blue'), "
                   "(2, 'Ben', 'ben@example.org', '+44 20 7946 0958', 'asked for a call'), "
                   "(3, 'Cy', 'cy@example.net', '+49 30 901820', 'none')) AS t (customer_id, name, email, phone, note)", "iceberg", NAME)
        check("bob makes a project declaring customers.customer_id as identifying a person, with a customers table", r.ok and ready is not None, r.text[:200])

        # 1 and 2. The catalog's tags: declared and found.
        found = wait(lambda: (lambda c: c if (("customers", "email", "personal") in c and ("customers", "customer_id", "personal") in c) else None)(classification(bob)),
                     420, every=15) or classification(bob)
        email, cid, phone = found.get(("customers", "email", "personal"), {}), found.get(("customers", "customer_id", "personal"), {}), found.get(("customers", "phone", "personal"), {})
        check("the declared column is tagged personal in the catalog, confirmed", cid.get("state") == "confirmed" and cid.get("declared") is True, cid)
        check("the catalog finds the email and phone columns and tags them personal, suggested",
              email.get("state") == "suggested" and email.get("automated") is True and phone.get("state") == "suggested", (email, phone))
        om = catalog()
        in_catalog = column_tags(om, "customer_id")
        check("... as the catalog's own page shows", in_catalog is not None and in_catalog.get("Protection.personal") == "Confirmed", in_catalog)
        issues = lambda: (bob.get(f"{PLATFORM}/api/projects/{NAME}/alerts", timeout=30).json() or {}).get("issues", [])
        alerts = wait(lambda: (lambda xs: xs if any(i["key"] == "protect/customers/email" for i in xs) else None)(issues()), 150, every=10) or issues()
        check("the project's alerts say a column was protected on the catalog's word",
              any(i["key"] == "protect/customers/email" and i["kind"] == "protect" for i in alerts), [i["key"] for i in alerts])
        check("... and the note column, plain text, isn't tagged", ("customers", "note", "personal") not in found, sorted(k for k in found if k[1] == "note"))

        # 3. Masked for alice, by the tag alone.
        masked = wait(lambda: (lambda v: v if isinstance(v, str) and "@" not in v and not v.startswith("error") else None)(reads("alice", "email")), 240, every=10)
        check("alice (a reader) sees the email column masked in SQL, with no policy written for it", masked is not None, reads("alice", "email"))
        check("... and the note column as it is", reads("alice", "note") == "likes blue", reads("alice", "note"))

        # 4. bob's word: confirm one, remove the other.
        hers = alice.post(f"{PLATFORM}/api/projects/{NAME}/classification", headers=H, timeout=30,
                          json={"table": "customers", "column": "phone", "tag": "personal", "action": "remove"})
        c1 = bob.post(f"{PLATFORM}/api/projects/{NAME}/classification", headers=H, timeout=30,
                      json={"table": "customers", "column": "email", "tag": "personal", "action": "confirm"})
        c2 = bob.post(f"{PLATFORM}/api/projects/{NAME}/classification", headers=H, timeout=30,
                      json={"table": "customers", "column": "phone", "tag": "personal", "action": "remove"})
        after = classification(bob)
        check("alice can't change a tag; bob confirms the email's and removes the phone's",
              hers.status_code == 403 and c1.ok and c2.ok and after.get(("customers", "email", "personal"), {}).get("state") == "confirmed"
              and ("customers", "phone", "personal") not in after, (hers.status_code, c1.text[:120], c2.text[:120]))
        plain = wait(lambda: (lambda v: v if v == "+1 415 555 0101" else None)(reads("alice", "phone")), 240, every=10)
        check("... alice reads phone numbers again", plain is not None, reads("alice", "phone"))
        time.sleep(150)  # two classification rounds
        check("... and the removed tag isn't suggested back", ("customers", "phone", "personal") not in classification(bob)
              and (column_tags(om, "phone") or {}).get("Protection.personal") is None, (tagged(bob, "phone"), column_tags(om, "phone")))
        check("the email stays masked for alice once confirmed", "@" not in str(reads("alice", "email")), reads("alice", "email"))
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
