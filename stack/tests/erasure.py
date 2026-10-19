"""Erase a customer, and retention: docker compose run --rm test erasure

A project of the suite's own, with customers and orders declared as people's
data (customer_id), and orders kept for 30 days. These check (use case 6.9,
under the CCPA):

  1. bob (a data-protection approver) erases customer 2: their rows go from
     both tables, the record says what was deleted where, when the request
     was made and by when it's due (45 days), who verified it, and the
     deliveries that carried their data (none here); the audit log has the
     erasure without the ID; the record is listed, without the ID;
  2. carol can't ask (no access), and a project with no tables of people's
     data has nothing to erase from;
  3. retention: an order older than 30 days goes at the next maintenance
     round; a fresh one stays.

The project is removed after.
"""
import json
import os

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "erased"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Erasure and retention: the erasure suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}],
                 "tables": {"people": [{"table": "customers", "column": "customer_id"}, {"table": "orders", "column": "customer_id"}],
                            "retention": [{"table": "orders", "column": "ordered_on", "keep": "30d"}]}}}


def rows(table, where=""):
    return sql("bob", f"SELECT count(*) FROM {table} {where}", "iceberg", NAME)[0][0]


def main():
    bob, carol = platform("bob"), platform("carol")
    try:
        r = bob.put(f"{PLATFORM}/api/projects/{NAME}", data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
            (projects(bob) or {}).get(NAME)), 900, every=10)
        sql("bob", "DROP TABLE IF EXISTS customers", "iceberg", NAME)
        sql("bob", "DROP TABLE IF EXISTS orders", "iceberg", NAME)
        sql("bob", "CREATE TABLE customers AS SELECT * FROM (VALUES (1, 'Ana', 'ana@example.com'), (2, 'Ben', 'ben@example.org'), (3, 'Cy', 'cy@example.net')) "
                   "AS t (customer_id, name, email)", "iceberg", NAME)
        sql("bob", "CREATE TABLE orders AS SELECT * FROM (VALUES (1, 1, 10.0, TIMESTAMP '2026-10-01 10:00:00'), (2, 2, 7.5, TIMESTAMP '2026-10-02 10:00:00'), "
                   "(3, 2, 3.0, TIMESTAMP '2026-10-03 10:00:00'), (4, 3, 9.0, TIMESTAMP '2020-01-01 10:00:00')) AS t (order_id, customer_id, amount, ordered_on)", "iceberg", NAME)
        check("bob makes a project declaring customers and orders as people's data, with 30 days' retention on orders", r.ok and ready is not None and rows("orders") == 4, r.text[:200])

        # 1. Erased.
        e = bob.post(f"{PLATFORM}/api/projects/{NAME}/erasures", headers=H, timeout=120, json={"person": "2", "requestedOn": "2026-10-19", "verifiedBy": "the privacy tool"})
        ej = e.json() if e.ok else {}
        deleted = {t["table"]: t["rows"] for t in ej.get("tables", [])}
        check("bob (a data-protection approver) erases customer 2: their rows go from customers and orders",
              e.status_code == 200 and deleted == {"customers": 1, "orders": 2} and rows("customers", "WHERE customer_id = 2") == 0 and rows("orders", "WHERE customer_id = 2") == 0,
              (e.status_code, e.text[:300]))
        check("... the record says when they asked, by when it's due (45 days), who verified it, and that no delivery carried their data",
              ej.get("requestedOn") == "2026-10-19" and ej.get("dueBy") == "2026-12-03" and ej.get("verifiedBy") == "the privacy tool" and ej.get("deliveries") == []
              and "2" not in json.dumps({k: v for k, v in ej.items() if k not in ("tables", "at", "deliveriesChecked", "deliveriesNotCheckable", "id")}).replace("2026", ""),
              {k: v for k, v in ej.items() if k != "deliveries"})
        listed = (bob.get(f"{PLATFORM}/api/projects/{NAME}/erasures", timeout=30).json() or {}).get("erasures", [])
        audit = wait(lambda: next((x for x in (bob.get(f"{PLATFORM}/api/projects/{NAME}/audit", timeout=30).json() or {}).get("events", [])
                                   if x.get("action") == "erasure"), None), 60, every=5)
        check("... it's listed and in the audit log, without the person's ID",
              len(listed) == 1 and listed[0]["id"] == ej.get("id") and audit is not None and "customer 2" not in json.dumps(audit) and '"2"' not in json.dumps(audit),
              (len(listed), audit))
        check("... the other customers are untouched", rows("customers") == 2 and rows("orders") == 2, (rows("customers"), rows("orders")))

        # 2. Who may ask.
        hers = carol.post(f"{PLATFORM}/api/projects/{NAME}/erasures", headers=H, timeout=30, json={"person": "1"})
        check("carol can't ask for an erasure", hers.status_code in (401, 403), hers.status_code)
        nothing = bob.post(f"{PLATFORM}/api/projects/sales/erasures", headers=H, timeout=60, json={"person": "nobody"})
        check("a project with no tables of people's data has nothing to erase from, and says so", nothing.status_code in (200, 422), (nothing.status_code, nothing.text[:150]))

        # 3. Retention: the 2020 order goes at the next maintenance round.
        m = bob.post(f"{PLATFORM}/api/projects/{NAME}/maintenance", headers=H, timeout=30)
        gone = wait(lambda: True if rows("orders", "WHERE order_id = 4") == 0 else None, 600, every=10)
        check("an order past its 30 days' retention goes at the next maintenance round; the fresh ones stay",
              m.ok and gone is True and rows("orders") == 1, (m.status_code, m.text[:150], rows("orders")))
    finally:
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
