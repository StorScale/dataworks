"""A source's tables copied into a project's, by change capture, end to end
(Kubernetes: storscale test --k8s copies; in Compose it's skipped, as there
are no copies there).

A project names a source and the tables to copy (spec.sources[].copy); the
platform operator starts Debezium for it, which copies each table into the
project's Iceberg tables (<source>_<table>), then every change. These check,
with sales and a source on the platform's own Postgres (a database of its
own, an account with REPLICATION; CI runs Postgres with wal_level=logical):

  1. the table is copied (sales.finance_invoices), and the copy's status is
     the project's;
  2. an insert, an update and a delete in the source reach it;
  3. a column added in the source is added to it;
  4. it's under the project's rules: alice (a reader of sales' listed
     tables only) can't read it.

The sales spec, the connection, the copied table and the source database
are removed after.
"""
import json
import os
import time

import psycopg2

from lakekit import check, denied, env, finish, sql

import k8s
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, CONNECTION, SOURCE, TABLE = "sales", "storscale-test-copy", "finance", "finance_invoices"
DB, ROLE = "copy_check", "copy_check"
SLOT = "storscale_sales_finance"
PASSWORD = "copy-check-" + str(int(time.time()))


def admin(statements, db="postgres"):
    conn = psycopg2.connect(host="postgres", dbname=db, user="postgres", password=env["POSTGRES_ADMIN_PASSWORD"], connect_timeout=10)
    conn.autocommit = True  # DROP DATABASE runs outside a transaction ("with conn" would open one)
    try:
        with conn.cursor() as cur:
            for s in statements:
                cur.execute(s)
    finally:
        conn.close()


def source(statements):
    conn = psycopg2.connect(host="postgres", dbname=DB, user=ROLE, password=PASSWORD, connect_timeout=10)
    with conn, conn.cursor() as cur:
        for s in statements:
            cur.execute(s)
    conn.close()


def rows(columns="id, amount"):
    try:
        return [list(r) for r in sql("bob", f"SELECT {columns} FROM {TABLE} ORDER BY id", "iceberg", "sales")]
    except Exception:  # noqa: BLE001 (not there yet)
        return None


def drop_source():
    # The copy's replication slot first (the database keeps its log for it), then its database.
    try:
        admin([f"SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = '{SLOT}' AND NOT active"])
    except Exception as e:  # noqa: BLE001
        print(f"  (the slot: {e})")
    admin([f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)", f"DROP ROLE IF EXISTS {ROLE}"])


def main():
    if not k8s.in_cluster():
        print("skipped: copies run on Kubernetes alone")
        return
    conn = psycopg2.connect(host="postgres", dbname="postgres", user="postgres", password=env["POSTGRES_ADMIN_PASSWORD"], connect_timeout=10)
    with conn, conn.cursor() as cur:
        cur.execute("SHOW wal_level")
        level = cur.fetchone()[0]
    conn.close()
    if level != "logical":
        print(f"skipped: the platform's Postgres has wal_level={level} (postgres.logicalReplication: true lets a source on it be copied)")
        return
    bob = platform("bob")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    drop_source()
    admin([f"CREATE ROLE {ROLE} LOGIN REPLICATION PASSWORD '{PASSWORD}'", f"CREATE DATABASE {DB} OWNER {ROLE}"])
    source(["CREATE TABLE public.invoices (id integer PRIMARY KEY, amount numeric(10, 2), note text)",
            "INSERT INTO public.invoices VALUES (1, 10.50, 'first'), (2, 99.00, 'second')"])
    k8s.put_secret(CONNECTION, {"host": "postgres", "port": "5432", "database": DB, "username": ROLE, "password": PASSWORD},
                   labels={"platform.storscale.io/connection": PROJECT})
    spec = {**before, "spec": {**before["spec"], "sources": [
        {"name": SOURCE, "kind": "postgresql", "connection": CONNECTION, "copy": {"tables": ["public.invoices"]}}]}}
    try:
        # 1. The first copy (the image is large: its first pull takes a while).
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check("bob has sales copy a source's table", r.ok, r.text[:200])
        got = wait(lambda: (lambda x: x if x and len(x) == 2 else None)(rows()), 600, every=10)
        check("the table is copied into sales (finance_invoices)", got and [g[0] for g in got] == [1, 2], got)
        parts = {p["system"]: p for p in (bob.get(url, timeout=30).json().get("status") or {}).get("parts", [])}
        check("... and its copy is part of the project's status", parts.get("copy", {}).get("ok"), parts.get("copy"))

        # 2. Changes, within a minute or two.
        source(["INSERT INTO public.invoices VALUES (3, 5.25, 'third')", "UPDATE public.invoices SET amount = 11.00 WHERE id = 1",
                "DELETE FROM public.invoices WHERE id = 2"])
        want = [[1, 11.0], [3, 5.25]]
        got = wait(lambda: (lambda x: x if x == want else None)([[a, float(b)] for a, b in rows() or []]), 300, every=10)
        check("an insert, an update and a delete in the source reach it", got == want, rows())

        # 3. A column added.
        source(["ALTER TABLE public.invoices ADD COLUMN region text", "INSERT INTO public.invoices VALUES (4, 1.00, 'fourth', 'EU')"])
        got = wait(lambda: (lambda x: x if x and x[-1] == [4, "EU"] else None)(rows("id, region")), 300, every=10)
        check("a column added in the source is added to it", got and got[-1] == [4, "EU"], rows("id, region"))

        # 4. The project's rules.
        msg = denied("alice", f"SELECT count(*) FROM {TABLE}", "iceberg", "sales")
        check("it's under sales' rules: alice (who reads its listed tables) can't read it", msg is not None, msg)

        # Its copy stops with the source.
        r = bob.put(url, data=json.dumps(before), timeout=30)
        gone = wait(lambda: k8s._api("GET", "secrets/copy-sales-finance").status_code == 404, 180, every=5)
        check("the source removed, its copy stops (its table stays, the project's)", r.ok and gone)
    finally:
        bob.put(url, data=json.dumps(before), timeout=30)
        k8s._api("DELETE", f"secrets/{CONNECTION}")
        wait(lambda: k8s._api("GET", "secrets/copy-sales-finance").status_code == 404, 180, every=5)
        try:
            sql("bob", f"DROP TABLE IF EXISTS {TABLE}", "iceberg", "sales")
        except Exception as e:  # noqa: BLE001
            print(f"  (dropping {TABLE}: {e})")
        drop_source()
    finish()


if __name__ == "__main__":
    main()
