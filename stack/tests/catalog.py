"""The catalog, end to end: docker compose run --rm test catalog

The catalog (OpenMetadata) knows every table, how data flows between them,
and whether their checks pass. These check, after bob runs the sales
pipeline in Airflow:

  1. the catalog has the sales project's tables, from Trino;
  2. the run's lineage is there, column by column: orders_by_region is made
     from orders (region from region, orders from id, amount from amount),
     by the pipeline sales_ingest;
  3. the project's checks ran on orders: the run loaded an order without a
     card number, so "card numbers are never missing" fails, saying why,
     while the others pass;
  4. the platform's Flow view shows alice (a reader) the same lineage and
     the failing check; carol (in neither group) gets nothing;
  5. someone signed in to the platform signs in to the catalog without
     another password: its Keycloak sign-in returns at once, with a token
     the catalog accepts as theirs;
  6. it wears the platform's colours, and its logo and icon (platformd's,
     through the gateway on the catalog's name).
"""
import html
import os
import re
import time

import requests

from lakekit import check, env, finish

OM = "http://openmetadata:8585/api/v1"
AIRFLOW = os.environ["PIPELINES_URL"]
PLATFORM = os.environ["PLATFORM_URL"]


def catalog():
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    r.raise_for_status()
    s = requests.Session()
    s.headers["Authorization"] = f"Bearer {r.json()['access_token']}"
    return s


def keycloak_form(s, url):
    r = s.get(url, timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    return form


def sign_in(url, user):
    s = requests.Session()
    form = keycloak_form(s, url)
    s.post(html.unescape(form.group(1)), timeout=60,
           data={"username": user, "password": env["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    return s


def wait(fn, timeout=240, every=5):
    deadline = time.time() + timeout
    while True:
        got = fn()
        if got or time.time() > deadline:
            return got
        time.sleep(every)


def run_pipeline():
    s = sign_in(f"{AIRFLOW}/auth/login/keycloak?next=", "bob")
    h = {"Authorization": f"Bearer {s.cookies.get('_token')}"}
    r = requests.post(f"{AIRFLOW}/api/v2/dags/sales_ingest/dagRuns", headers=h, json={"logical_date": None}, timeout=60)
    run = r.json().get("dag_run_id") if r.ok else None
    state = wait(lambda: (lambda st: st if st in ("success", "failed") else None)(
        requests.get(f"{AIRFLOW}/api/v2/dags/sales_ingest/dagRuns/{run}", headers=h, timeout=30).json().get("state")), 300, 3) if run else None
    return run, state


def lineage(om):
    r = om.get(f"{OM}/lineage/table/name/trino.iceberg.sales.orders?upstreamDepth=0&downstreamDepth=1", timeout=30).json()
    names = {n["id"]: n["fullyQualifiedName"] for n in r.get("nodes", [])}
    for e in r.get("downstreamEdges", []):
        if names.get(e["toEntity"]) == "trino.iceberg.sales.orders_by_region":
            return e.get("lineageDetails", {})
    return None


def checks(om):
    link = "<#E::table::trino.iceberg.sales.orders>"
    r = om.get(f"{OM}/dataQuality/testCases", params={"entityLink": link, "includeAllTests": "true", "fields": "testCaseResult", "limit": 50}, timeout=30).json()
    return {t["name"]: t.get("testCaseResult") or {} for t in r.get("data", [])}


def main():
    om = catalog()

    # 1. The project's tables, from Trino.
    tables = wait(lambda: (lambda names: names if {"orders", "payroll", "orders_by_region"} <= names else None)(
        {t["name"] for t in om.get(f"{OM}/tables", params={"databaseSchema": "trino.iceberg.sales", "limit": 50}, timeout=30).json().get("data", [])}), 180)
    check("the catalog has the sales project's tables, from Trino", tables, ", ".join(sorted(tables or [])) or "none")

    # 2. bob runs the pipeline; its lineage, column by column.
    started = time.time() * 1000
    run, state = run_pipeline()
    check("bob runs the sales pipeline", state == "success", f"run {run}: {state}")
    # The lineage, with its columns: the catalog may file the run before it knows the new table's columns.
    details = wait(lambda: (lambda d: d if d and d.get("updatedAt", 0) >= started - 5000 and d.get("columnsLineage") else None)(lineage(om)), 180, 5) or lineage(om)
    cols = {c["toColumn"].rsplit(".", 1)[1]: [f.rsplit(".", 1)[1] for f in c["fromColumns"]] for c in (details or {}).get("columnsLineage", [])}
    check("the run's lineage is in the catalog: orders_by_region is made from orders, column by column",
          cols == {"region": ["region"], "orders": ["id"], "amount": ["amount"]},
          ", ".join(f"{k} ← orders.{','.join(v)}" for k, v in sorted(cols.items())) or "no lineage")
    pipeline = ((details or {}).get("pipeline") or {})
    check("... by the pipeline sales_ingest", pipeline.get("displayName") == "sales_ingest",
          pipeline.get("fullyQualifiedName", "no pipeline"))

    # 3. The project's checks, run after the pipeline loaded its orders. The
    # catalog's sync runs them every round, and a round that began before
    # the run's rows were visible still finds no missing card number: wait
    # for one that has seen them.
    seen = lambda r: r.get("timestamp", 0) > started and r.get("testCaseStatus") == "Failed"
    results = wait(lambda: (lambda c: c if seen(c.get("sales_orders_card_number_notNull", {})) else None)(checks(om)), 180)
    results = results or checks(om)
    nulls = results.get("sales_orders_card_number_notNull", {})
    check("the missing card number fails the project's check on orders, saying why",
          nulls.get("testCaseStatus") == "Failed" and "nullCount=1" in nulls.get("result", ""), nulls.get("result", "not run"))
    others = {k: v.get("testCaseStatus") for k, v in results.items() if k != "sales_orders_card_number_notNull"}
    check("... and the other checks pass", others and all(v == "Success" for v in others.values()), others)

    # 4. The platform's Flow view.
    def flow(user):
        s = sign_in(f"{PLATFORM}/auth/login?next=/", user)
        return s.get(f"{PLATFORM}/api/projects/sales/flow", timeout=60)

    r = flow("alice")
    f = r.json() if r.ok else {}
    edge = next((e for e in f.get("edges", []) if e["to"].endswith(".orders_by_region")), None)
    check("alice's Flow view shows the lineage, and the pipeline",
          edge and edge.get("pipeline") == "sales_ingest" and {c["to"] for c in edge["columns"]} == {"region", "orders", "amount"},
          f"HTTP {r.status_code}: {edge and edge.get('pipeline')}, columns {edge and sorted(c['to'] for c in edge['columns'])}")
    orders = next((t for t in f.get("tables", []) if t["name"] == "orders"), {})
    failing = [c["name"] for c in orders.get("checks", []) if c["status"] == "Failed"]
    check("... and the failing check on orders", failing == ["sales_orders_card_number_notNull"], failing)

    # 5. What the table holds: the profiler's count and each column's nulls, distinct values and range (6.11).
    def profiled():
        t = next((t for t in (flow("bob").json() or {}).get("tables", []) if t["name"] == "orders"), {})
        return t if t.get("rows") and any(c.get("profile") for c in t.get("columns", [])) else None
    prof = wait(profiled, 240, every=10) or {}
    amount = next((c.get("profile") or {} for c in prof.get("columns", []) if c["name"] == "amount"), {})
    check("the catalog profiled orders: its rows, and amount's nulls, distinct values, min and max, on the Data page",
          (prof.get("rows") or 0) >= 1 and "nulls" in amount and amount.get("distinct") and amount.get("min") is not None and amount.get("max") is not None,
          (prof.get("rows"), amount))
    as_alice = next((t for t in (flow("alice").json() or {}).get("tables", []) if t["name"] == "orders"), {})
    check("... which alice, who reads orders under a row filter, doesn't see (its figures are every row's)",
          prof and as_alice and as_alice.get("rows") is None and not any(c.get("profile") for c in as_alice.get("columns", [])),
          (as_alice.get("rows"), [c["name"] for c in as_alice.get("columns", []) if c.get("profile")]))
    carol = flow("carol").status_code
    check("people in neither group don't see it", carol == 403, f"carol: HTTP {carol}")

    # 5. The catalog's own sign-in, with the platform's Keycloak session.
    s = sign_in(f"{PLATFORM}/auth/login?next=/", "alice")
    catalog_url = PLATFORM.replace("://", "://catalog.", 1)
    r = s.get(f"{catalog_url}/api/v1/auth/login", params={"redirectUri": f"{catalog_url}/auth/callback"}, timeout=60)
    form = 'id="kc-form-login"' in r.text
    token = re.search(r"[#&]id_token=([^&]+)", r.url)
    me = requests.get(f"{catalog_url}/api/v1/users/loggedInUser", headers={"Authorization": f"Bearer {token.group(1)}"},
                      timeout=30).json().get("name") if token else None
    check("signed in to the platform, alice signs in to the catalog without another password",
          not form and me == "alice", "a Keycloak form" if form else f"the catalog says: {me}")

    # 6. The platform's look.
    theme = om.get(f"{OM}/system/settings/customUiThemePreference", timeout=30).json().get("config_value", {})
    logos = theme.get("customLogoConfig", {})
    icon = requests.get(f"{catalog_url}{logos.get('customMonogramUrlPath', '/none')}", timeout=30)
    check("the catalog wears the platform's colours and icon, served on its own name",
          theme.get("customTheme", {}).get("primaryColor") == "#10151c" and theme.get("customTheme", {}).get("panelBackgroundColor") == "#2c3e50" and icon.ok and icon.headers.get("Content-Type", "").startswith("image/"),
          (theme, icon.status_code, icon.headers.get("Content-Type")))
    finish()


if __name__ == "__main__":
    main()
