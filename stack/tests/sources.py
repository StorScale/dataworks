"""A project's sources, end to end: docker compose run --rm test sources

A source is one of the company's databases, queried where it is: the
project names it (spec.sources) and one of its connections, and the
platform operator makes a Trino catalog of it, which Ranger lets the
project read and no one write. These check, with sales and a source on
the platform's own Postgres (Trino's limits' database, as its own account):

  1. bob (an editor) reads the source's table through its catalog, and
     nothing of the schema the source leaves out;
  2. he can't write to it;
  3. alice (a reader) can't read it, until the source says readers may,
     and then sees it as its masks and row filters say;
  4. the connection's password is in no statement Ranger's audit holds
     (it's in a credentials file Trino reads);
  5. the catalog (OpenMetadata) describes the source's table;
  6. the source removed, its catalog goes.

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
OM = "http://openmetadata:8585/api/v1"
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


def reads(user, table="sources_check.invoices"):
    """The source's table's rows, as user: None when it can't be read."""
    try:
        return sql(user, f"SELECT count(*) FROM {table}", CATALOG)[0][0]
    except Exception:  # noqa: BLE001 (no catalog yet, or refused)
        return None


def rows(user):
    try:
        return [[r[0], None if r[1] is None else float(r[1])] for r in sql(user, "SELECT id, amount FROM sources_check.invoices ORDER BY id", CATALOG)]
    except Exception:  # noqa: BLE001
        return None


def described():
    """The source's tables in the catalog (OpenMetadata), as its administrator reads them."""
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    h = {"Authorization": f"Bearer {r.json()['access_token']}"}
    out = set()
    for schema in ("sources_check", "sources_hidden"):
        r = requests.get(f"{OM}/tables", params={"databaseSchema": f"trino.{CATALOG}.{schema}", "limit": 50}, headers=h, timeout=30)
        out |= {f"{schema}.{t['name']}" for t in (r.json().get("data", []) if r.ok else [])}
    return out


def catalogs(user):
    return {r[0] for r in sql(user, "SHOW CATALOGS", "iceberg")}


def main():
    bob = platform("bob")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    source_db(["DROP SCHEMA IF EXISTS sources_check CASCADE", "DROP SCHEMA IF EXISTS sources_hidden CASCADE",
               "CREATE SCHEMA sources_check", "CREATE SCHEMA sources_hidden",
               "CREATE TABLE sources_check.invoices (id integer, amount numeric(10, 2))",
               "INSERT INTO sources_check.invoices VALUES (1, 10.50), (2, 99.00)",
               "CREATE TABLE sources_hidden.salaries (id integer)", "INSERT INTO sources_hidden.salaries VALUES (1)"])
    put_connection(DB)
    source = {"name": "finance", "kind": "postgresql", "connection": CONNECTION, "schemas": ["sources_check"]}
    put = lambda **more: bob.put(url, timeout=30, data=json.dumps({**before, "spec": {**before["spec"], "sources": [{**source, **more}]}}))
    try:
        # 1. Read where it is.
        r = put()
        check("bob adds a source to sales", r.ok, r.text[:200])
        # The operator makes the catalog once Trino sees the connection (a minute or so on Kubernetes).
        n = wait(lambda: reads("bob"), 240, every=5)
        check("bob reads the source's table through its catalog (sales_finance)", n == 2, n)
        check("... and nothing of the schema the source leaves out", reads("bob", "sources_hidden.salaries") is None)

        # 2. Read-only.
        msg = denied("bob", "INSERT INTO sources_check.invoices VALUES (3, 1.00)", CATALOG)
        check("... and can't write to it", msg is not None, msg)

        # 3. Readers, where the source says so.
        check("alice (a reader) can't read it", reads("alice") is None and CATALOG not in catalogs("alice"))
        r = put(readers=True, masks=[{"table": "sources_check.invoices", "column": "amount", "type": "MASK_NULL"}],
                rowFilters=[{"table": "sources_check.invoices", "filter": "id = 1"}])
        # Ranger's policies reach Trino within its plugin's poll (30s).
        seen = wait(lambda: (lambda x: x if x == [[1, None]] else None)(rows("alice")), 180, every=5)
        check("... until the source says readers may, and then as its row filter and mask say (one row, no amount)",
              r.ok and seen == [[1, None]], rows("alice"))
        check("bob (an editor) still sees it all", rows("bob") == [[1, 10.5], [2, 99.0]], rows("bob"))

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

        # 5. In the catalog: what the source shows, not what it leaves out.
        got = wait(lambda: (lambda d: d if "sources_check.invoices" in d else None)(described()), 300, every=10) or described()
        check("the catalog describes the source's table, and not the schema it leaves out",
              "sources_check.invoices" in got and not any(t.startswith("sources_hidden.") for t in got), sorted(got))

        # 6. Gone with the source.
        r = bob.put(url, data=json.dumps(before), timeout=30)
        gone = wait(lambda: CATALOG not in catalogs("bob"), 120, every=5)
        check("the source removed, its catalog goes", r.ok and gone)
    finally:
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        source_db(["DROP SCHEMA IF EXISTS sources_check CASCADE", "DROP SCHEMA IF EXISTS sources_hidden CASCADE"])
    finish()


if __name__ == "__main__":
    main()
