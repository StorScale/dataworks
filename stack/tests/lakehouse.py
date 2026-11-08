"""End-to-end checks for the lakehouse example: docker compose run --rm test

  1. bob (engineers) creates Iceberg tables through Trino, loads them and
     changes one, and Nessie records each change as a commit;
  2. the tables' data and metadata are files in Buckets' warehouse bucket;
  3. alice (analysts) reads sales.orders through Trino with Ranger's row filter
     (EU only) and column mask (card numbers' last four digits), and is denied
     everything else;
  4. carol (no group) is denied by Ranger;
  5. alice signs in to Buckets with the same Keycloak token: her own bucket
     works, the warehouse's files are refused, so she can't read around Ranger;
  6. Ranger's audit log has the denials.
"""
import functools
import uuid

import requests

from lakekit import audited_denials, catalog_headers, check, code, finish, keys, s3_as, s3_root, token, wait_for_trino
from lakekit import denied as _denied
from lakekit import sql as _sql

# Every statement here runs in the iceberg catalog's sales schema.
sql = functools.partial(_sql, catalog="iceberg", schema="sales")
denied = functools.partial(_denied, catalog="iceberg", schema="sales")


ORDERS = [
    (1, "Ana", "4111111111111111", "EU", 120.50, "2026-09-01"),
    (2, "Ben", "5500000000000004", "US", 75.00, "2026-09-02"),
    (3, "Chloe", "340000000000009", "EU", 310.25, "2026-09-03"),
    (4, "Dev", "6011000000000004", "APAC", 42.00, "2026-09-04"),
    (5, "Eva", "3530111333300000", "EU", 18.99, "2026-09-05"),
    (6, "Finn", "4012888888881881", "US", 99.95, "2026-09-06"),
]


def main():
    wait_for_trino()

    # 1. Engineers build the tables.
    sql("bob", "CREATE SCHEMA IF NOT EXISTS iceberg.sales")
    for t in ("orders", "payroll"):
        sql("bob", f"DROP TABLE IF EXISTS {t}")
    sql("bob", "CREATE TABLE orders (id bigint, customer varchar, card_number varchar, region varchar, "
               "amount decimal(10,2), order_date date)")
    values = ", ".join(f"({i}, '{c}', '{n}', '{r}', {a}, DATE '{d}')" for i, c, n, r, a, d in ORDERS)
    sql("bob", f"INSERT INTO orders VALUES {values}")
    sql("bob", "INSERT INTO orders VALUES (7, 'Gus', '4222222222222', 'EU', 64.10, DATE '2026-09-07')")
    sql("bob", "UPDATE orders SET amount = 130.50 WHERE id = 1")
    now = sql("bob", "SELECT count(*), max(amount) FILTER (WHERE id = 1) FROM orders")[0]
    check("engineers create, load and change an Iceberg table", now[0] == 7 and float(now[1]) == 130.5,
          f"{now[0]} rows, order 1 now {now[1]}")
    # Nessie keeps a table's history as catalog commits (like git), not as Iceberg snapshots.
    log = requests.get("http://nessie:19120/api/v2/trees/main/history", params={"max-records": 50}, headers=catalog_headers(), timeout=10).json()
    changes = [e for e in log["logEntries"] if "sales.orders" in e["commitMeta"].get("message", "")]
    check("Nessie records every change as a commit", len(changes) >= 4,
          f"{len(changes)} commits for sales.orders on branch main")
    # Nessie answers the platform's catalog account alone: people reach the tables through
    # Trino (Ranger's rules), or Spark through platformd (the tables they read whole).
    anyone = requests.get("http://nessie:19120/api/v2/trees", timeout=10).status_code
    bob = {"Authorization": "Bearer " + token("bob")}
    # Nessie shows any signed-in caller its default branch's name and commit; nothing in it:
    # bob may neither list its tables (Nessie's API and Iceberg's) nor commit to it.
    main = requests.get("http://nessie:19120/api/v2/trees/main", headers=catalog_headers(), timeout=10).json()["reference"]["hash"]
    entries = requests.get("http://nessie:19120/api/v2/trees/main/entries", headers=bob, timeout=10)
    commit = requests.post(f"http://nessie:19120/api/v2/trees/main@{main}/history/commit", headers=bob, timeout=10, json={
        "commitMeta": {"message": "bob, past the platform"},
        "operations": [{"type": "DELETE", "key": {"elements": ["sales", "nessie_check_never_there"]}}]})
    cfg = requests.get("http://nessie:19120/iceberg/v1/config", params={"warehouse": "warehouse"}, headers=catalog_headers(), timeout=10).json()
    prefix = cfg.get("overrides", {}).get("prefix") or cfg.get("defaults", {}).get("prefix")
    as_bob_iceberg = requests.get(f"http://nessie:19120/iceberg/v1/{prefix}/namespaces/sales/tables",
                                  headers=bob, timeout=10)
    check("Nessie answers the catalog's account alone: not someone without a token, nor a person's own (bob, an engineer)",
          anyone == 401 and entries.status_code == 403 and commit.status_code == 403
          and "is not allowed" in as_bob_iceberg.text,
          f"no token: HTTP {anyone}; bob's: main's tables HTTP {entries.status_code}, a commit HTTP {commit.status_code} "
          f"{commit.text[:150]}, Iceberg's tables HTTP {as_bob_iceberg.status_code} {as_bob_iceberg.text[:150]}")
    sql("bob", "CREATE TABLE payroll (employee varchar, salary decimal(10,2))")
    sql("bob", "INSERT INTO payroll VALUES ('Ana', 5000.00)")

    # 2. The table is files in Buckets.
    files = keys(s3_root(), "warehouse", "sales/")
    data = [k for k in files if k.endswith(".parquet") and "/orders" in k]
    meta = [k for k in files if k.endswith(".metadata.json") and "/orders" in k]
    check("the table's data and metadata are in Buckets", data and meta,
          f"{len(data)} Parquet files, {len(meta)} metadata files under s3://warehouse/sales/")

    # 3. Analysts: one table, EU rows only, card numbers masked.
    rows = sql("alice", "SELECT id, region, card_number FROM orders ORDER BY id")
    regions = {r[1] for r in rows}
    check("Ranger's row filter: analysts see only EU orders", regions == {"EU"} and len(rows) == 4,
          f"{len(rows)} rows, regions {sorted(regions)}")
    cards = {i: n for i, _, n, *_ in ORDERS} | {7: "4222222222222"}
    masked = all(r[2] != cards[r[0]] and r[2].endswith(cards[r[0]][-4:]) for r in rows)
    check("Ranger's column mask: analysts see only card numbers' last four digits", masked, rows[0][2])
    msg = denied("alice", "SELECT * FROM payroll")
    check("analysts can't read other tables", msg, msg)

    # 3b. The table as it was, read at a Nessie commit (after #, which Trino
    # passes through to Nessie; a branch or tag after @ isn't found), and
    # never wider than the table itself.
    def at(user, name, cols="id, region, card_number"):
        try:
            return sql(user, f'SELECT {cols} FROM "{name}" ORDER BY 1')
        except Exception as e:  # noqa: BLE001 (refused, or not found)
            return str(e).splitlines()[0][:200]
    head = requests.get("http://nessie:19120/api/v2/trees/main", headers=catalog_headers(), timeout=10).json()["reference"]["hash"]
    everyone = at("bob", f"orders#{head}", "count(*)")
    check("a table reads as it was at a Nessie commit (orders#<commit>)", everyone == [[7]], everyone)
    then = at("alice", f"orders#{head}")
    check("... and alice sees it as she sees the table: EU rows, card numbers masked", then == rows, then)
    msg = denied("alice", f'SELECT * FROM "payroll#{head}"')
    check("... and still can't read a table she may not, at any commit", msg, msg)
    msg = denied("alice", "CREATE TABLE notes (t varchar)")
    check("analysts can't create tables", msg, msg)
    msg = denied("alice", "INSERT INTO orders VALUES (8, 'Hal', '4000', 'EU', 1, DATE '2026-09-08')")
    check("analysts can't write to sales.orders", msg, msg)

    # 4. No group, no access.
    msg = denied("carol", "SELECT count(*) FROM orders")
    check("people in neither group are denied", msg, msg)

    # 5. Buckets: people sign in with the same token, and can't read the warehouse.
    alice = s3_as("alice")
    key = f"alice/{uuid.uuid4().hex}.txt"
    wrote = code(lambda: alice.put_object(Bucket="scratch", Key=key, Body=b"my notes"))
    check("analysts sign in to Buckets with their Keycloak token and use their own bucket", wrote == "OK", wrote)
    got = code(lambda: alice.get_object(Bucket="warehouse", Key=data[0]))
    listed = code(lambda: alice.list_objects_v2(Bucket="warehouse"))
    check("analysts can't read the warehouse's files directly", got == "AccessDenied" and listed == "AccessDenied",
          f"GetObject {got}, ListObjects {listed}")

    # 6. Ranger's audit log (Solr) has the denials.
    hits = audited_denials("carol")
    check("Ranger's audit log records the denials", hits > 0, f"{hits} denied requests by carol")
    finish()


if __name__ == "__main__":
    main()
