"""A project's sources, end to end: docker compose run --rm test sources

A source is one of the company's databases, queried where it is: the
project names it (spec.sources) and one of its connections, and the
platform operator makes a Trino catalog of it, which Ranger lets the
project read and no one write. These check, with sales and a source on
the platform's own Postgres (Trino's limits' database, as its own account):

  1. bob (an editor) reads the source's table through its catalog;
  2. he can't write to it;
  3. alice (a reader) can't read it, until the source says readers may;
  4. the connection's password is in no statement Ranger's audit holds
     (it's in a credentials file Trino reads);
  5. the source removed, its catalog goes.

The sales spec, the connection and the source's table are put back after.
"""
import json
import os

import psycopg2
import requests

from lakekit import check, denied, env, finish, s3_root, sql

import k8s
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, CONNECTION, CATALOG = "sales", "storscale-test-source", "sales_finance"
SOLR = "http://ranger-solr:8983/solr/ranger_audits"
DB = {"host": "trino-db", "port": "5432", "database": "trino", "username": "trino", "password": env["TRINO_DB_PASSWORD"]}


def source_db(statements):
    with psycopg2.connect(host=DB["host"], port=DB["port"], dbname=DB["database"], user=DB["username"],
                          password=DB["password"], connect_timeout=10) as conn, conn.cursor() as cur:
        for s in statements:
            cur.execute(s)


def put_connection(data):
    if k8s.in_cluster():
        k8s.put_secret(CONNECTION, data, labels={"platform.storscale.io/connection": PROJECT})
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"connections/{PROJECT}/{CONNECTION}.json", Body=json.dumps(data).encode())


def delete_connection():
    if k8s.in_cluster():
        k8s._api("DELETE", f"secrets/{CONNECTION}")
    else:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"connections/{PROJECT}/{CONNECTION}.json")


def reads(user):
    """The source's table's rows, as user: None when it can't be read."""
    try:
        return sql(user, "SELECT count(*) FROM sources_check.invoices", CATALOG)[0][0]
    except Exception:  # noqa: BLE001 (no catalog yet, or refused)
        return None


def catalogs(user):
    return {r[0] for r in sql(user, "SHOW CATALOGS", "iceberg")}


def main():
    bob = platform("bob")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    source_db(["DROP SCHEMA IF EXISTS sources_check CASCADE", "CREATE SCHEMA sources_check",
               "CREATE TABLE sources_check.invoices (id integer, amount numeric(10, 2))",
               "INSERT INTO sources_check.invoices VALUES (1, 10.50), (2, 99.00)"])
    put_connection(DB)
    put = lambda readers: bob.put(url, timeout=30, data=json.dumps({**before, "spec": {**before["spec"], "sources": [
        {"name": "finance", "kind": "postgresql", "connection": CONNECTION, "readers": readers}]}}))
    try:
        # 1. Read where it is.
        r = put(False)
        check("bob adds a source to sales", r.ok, r.text[:200])
        # The operator makes the catalog once Trino sees the connection (a minute or so on Kubernetes).
        n = wait(lambda: reads("bob"), 240, every=5)
        check("bob reads the source's table through its catalog (sales_finance)", n == 2, n)

        # 2. Read-only.
        msg = denied("bob", "INSERT INTO sources_check.invoices VALUES (3, 1.00)", CATALOG)
        check("... and can't write to it", msg is not None, msg)

        # 3. Readers, where the source says so.
        check("alice (a reader) can't read it", reads("alice") is None and CATALOG not in catalogs("alice"))
        r = put(True)
        n = wait(lambda: reads("alice"), 120, every=5)
        check("... until the source says readers may", r.ok and n == 2, n)

        # 4. The password in no statement: Ranger's audit of the operator's.
        def audited():
            r = requests.get(f"{SOLR}/select", params={"q": 'reqUser:"platform-operator"', "rows": 200, "wt": "json",
                                                      "sort": "evtTime desc"}, timeout=30)
            docs = r.json().get("response", {}).get("docs", []) if r.ok else []
            return [d for d in docs if CATALOG in d.get("resource", "") + d.get("reqData", "")]
        docs = wait(audited, 90, every=5) or []
        stmts = [d.get("reqData", "") for d in docs]
        check("Ranger audited the operator making the catalog, the password in none of it",
              docs and not any(DB["password"] in json.dumps(d) for d in docs) and all("connection-credential-file" in s for s in stmts if "CREATE CATALOG" in s),
              [(d.get("access"), d.get("reqData", "")[:160]) for d in docs[:3]])

        # 5. Gone with the source.
        r = bob.put(url, data=json.dumps(before), timeout=30)
        gone = wait(lambda: CATALOG not in catalogs("bob"), 120, every=5)
        check("the source removed, its catalog goes", r.ok and gone)
    finally:
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        source_db(["DROP SCHEMA IF EXISTS sources_check CASCADE"])
    finish()


if __name__ == "__main__":
    main()
