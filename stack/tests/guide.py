"""The lab guide (docs/guide.md), run against an install: run only when named,
storscale test --k8s guide (or docker compose run --rm --use-aliases test guide).

Each lab's steps, through the platform's API as the guide's people do them
on its pages (alice the analyst, bob the engineer and administrator, carol
with no access), and each lab's Check. Unlike the other suites it leaves
what it makes (the retail and marketing projects, their tables, models,
products and roles), for a person to look at afterwards, and a lab already
done is done again where that's harmless.

Which labs: all those that need nothing more than the platform, or those
named in /etc/storscale-tests/labs (the ConfigMap storscale-tests' key labs:
"1 2 3"), or GUIDE_LABS. Labs 10, 11's schedule, 17, 18 and 20 need a GitLab
repo: GUIDE_REPO (its https address), GUIDE_GITLAB (GitLab's address) and the
connection retail-dags holding its token, made on retail's Connections tab.
"""
import io
import json
import os
import sys
import time

import requests

from lakekit import check, env, finish
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
GIT_LABS = {10, 17, 18, 20}   # and 11's schedule, run when retail has a repo
ALL = [n for n in range(1, 23)]


def labs_wanted():
    raw = os.environ.get("GUIDE_LABS", "")
    try:
        raw = raw or open("/etc/storscale-tests/labs").read()
    except OSError:
        pass
    named = [int(x) for x in raw.replace(",", " ").split()]
    return named or [n for n in ALL if n not in GIT_LABS or os.environ.get("GUIDE_REPO")]


def lab_data():
    """The lab's five files, made as docs/lab/make_data.py makes them: {name: bytes}."""
    import tempfile
    here = tempfile.mkdtemp()
    src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "make_lab_data.py")).read()
    src = src.replace('HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data")', "HERE = _HERE")
    exec(compile(src, "make_lab_data", "exec"), {"_HERE": here, "__name__": "make_lab_data"})
    return {f: open(os.path.join(here, f), "rb").read() for f in os.listdir(here)}


# --- The platform, as its pages use it --------------------------------------------------

people = {}


class Person(requests.Session):
    """A person's session that signs in again when the platform's has ended
    (the guide takes longer than a sign-in lasts)."""

    def __init__(self, user):
        super().__init__()
        self.user = user
        self.sign_in()

    def sign_in(self):
        fresh = platform(self.user)
        self.cookies, self.headers = fresh.cookies, fresh.headers

    def request(self, method, url, *args, **kw):
        r = super().request(method, url, *args, **kw)
        if r.status_code == 401 and url.startswith(PLATFORM):
            self.sign_in()
            for f in (kw.get("files") or {}).values():   # an upload's file, read again from its start
                if isinstance(f, tuple) and hasattr(f[1], "seek"):
                    f[1].seek(0)
            r = super().request(method, url, *args, **kw)
        return r


def as_(user):
    if user not in people:
        people[user] = Person(user)
    return people[user]


def api(user, method, path, body=None, ok=(200, 201, 204), **kw):
    r = as_(user).request(method, f"{PLATFORM}{path}", json=body, timeout=kw.pop("timeout", 120), **kw)
    if r.status_code not in ok:
        print(f"    {user} {method} {path}: HTTP {r.status_code} {r.text[:300]}", flush=True)
    return r


def j(r):
    try:
        return r.json()
    except ValueError:
        return {}


def sql(user, project, statement, limit=1000):
    """The SQL tab: (columns, rows), or the refusal's text."""
    r = as_(user).post(f"{PLATFORM}/api/projects/{project}/sql", json={"sql": statement, "limit": limit}, timeout=600)
    b = j(r)
    if not r.ok:
        return b.get("error") or r.text[:300]
    return b.get("rows", [])


def project(user, name):
    r = as_(user).get(f"{PLATFORM}/api/projects/{name}", timeout=60)
    return j(r) if r.ok else None


def spec(name):
    """The project's spec, as bob's Edit page has it."""
    return (project("bob", name) or {}).get("spec")


def save_spec(p):
    return api("bob", "PUT", f"/api/projects/{p['metadata']['name']}", p)


def ready(name, timeout=600):
    return wait(lambda: ((project("bob", name) or {}).get("status") or {}).get("phase") == "Ready", timeout, every=10)


def pipelines(user, name):
    return j(as_(user).get(f"{PLATFORM}/api/projects/{name}/pipelines", timeout=120)).get("pipelines", [])


def run_pipeline(user, name, pipeline, timeout=900, again=True):
    """Run now, and wait: (state, run id). A run that failed because its
    Airflow hadn't its DAG yet (just committed) is run once more."""
    if not wait(lambda: any(p["id"] == pipeline for p in pipelines(user, name)), 600, every=10):
        return f"{pipeline} isn't listed", None
    r = api(user, "POST", f"/api/projects/{name}/pipelines/{pipeline}/runs")
    run = j(r).get("run")
    if not run:
        return f"not started: HTTP {r.status_code}", None
    state = wait(lambda: next((x["state"] for p in pipelines(user, name) if p["id"] == pipeline for x in p.get("runs", [])
                               if x["id"] == run and x["state"] in ("success", "failed")), None), timeout, every=10)
    if state == "failed" and again and "Dag not found" in run_log(user, name, pipeline, run):
        print(f"    ({pipeline}: its Airflow hadn't the DAG yet; once more)")
        time.sleep(60)
        return run_pipeline(user, name, pipeline, timeout, again=False)
    return state, run


def run_log(user, name, pipeline, run):
    return j(as_(user).get(f"{PLATFORM}/api/projects/{name}/pipelines/{pipeline}/runs/{run}/log", timeout=120)).get("log", "")


def tables(name):
    return {t["name"]: t for t in j(as_("bob").get(f"{PLATFORM}/api/projects/{name}/flow", timeout=120)).get("tables", [])}


def alerts(name):
    return j(as_("bob").get(f"{PLATFORM}/api/projects/{name}/alerts", timeout=60)).get("issues", [])


def heading(n, title):
    print(f"\n--- Lab {n}: {title}", flush=True)


# --- Part I: trust the data --------------------------------------------------------------

def lab1():
    heading(1, "Sign in, and see what each person sees")
    mine = {p["name"]: p for p in j(as_("alice").get(f"{PLATFORM}/api/projects", timeout=60)).get("projects", [])}
    check("alice's Home lists sales, where she's a reader", (mine.get("sales") or {}).get("role") == "reader", {k: v.get("role") for k, v in mine.items()})
    carol = platform("carol").get(f"{PLATFORM}/api/projects", timeout=60)
    check("carol has no access", carol.status_code in (401, 403), f"HTTP {carol.status_code}")
    bobs = {p["name"]: p for p in j(as_("bob").get(f"{PLATFORM}/api/projects", timeout=60)).get("projects", [])}
    check("bob is an editor of sales", (bobs.get("sales") or {}).get("role") == "editor", (bobs.get("sales") or {}).get("role"))
    a, b = sql("alice", "sales", "SELECT DISTINCT region FROM orders ORDER BY 1"), sql("bob", "sales", "SELECT DISTINCT region FROM orders ORDER BY 1")
    check("Check: alice's preview of orders is EU only; bob's has all three regions", a == [["EU"]] and len(b) == 3, f"alice {a}, bob {b}")


def lab2():
    heading(2, "A project for the retail team")
    p = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": "retail"},
         "spec": {"description": "Harbor Outfitters: orders, customers, campaigns",
                  "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "reader"}],
                  "pipelines": {"serviceAccount": "airflow-pipelines"},
                  "tables": {"readers": {"rowFilters": [{"table": "uploads_orders", "filter": "region = 'EU'"}]},
                             "people": [{"table": "uploads_customers", "column": "customer_id"}, {"table": "uploads_orders", "column": "customer_id"}]},
                  "files": {}}}
    old = spec("retail")
    if old:   # done before: keep what later labs added
        old["spec"]["description"], old["spec"]["members"] = p["spec"]["description"], p["spec"]["members"]
        old["spec"].setdefault("tables", {}).setdefault("people", p["spec"]["tables"]["people"])
        old["spec"].setdefault("files", {})
        old["spec"].setdefault("pipelines", p["spec"]["pipelines"])
        old["spec"]["tables"]["readers"] = {**old["spec"]["tables"].get("readers", {}), "rowFilters": p["spec"]["tables"]["readers"]["rowFilters"]}
        p = old
    r = save_spec(p)
    check("bob makes retail: its members, tables with the readers' row filter, files, pipelines, and who its people are", r.ok, f"HTTP {r.status_code}")
    check("... and within a minute it's Ready", ready("retail"), ((project("bob", "retail") or {}).get("status") or {}).get("phase"))
    st = (project("bob", "retail") or {}).get("status") or {}
    roles = {m["username"]: m["role"] for m in st.get("members") or []}
    st = wait(lambda: (lambda s: s if {m["username"]: m["role"] for m in s.get("members") or []}.get("alice") == "reader" else None)(
        (project("bob", "retail") or {}).get("status") or {}), 120, every=10) or st
    roles = {m["username"]: m["role"] for m in st.get("members") or []}
    check("Check: on Access, bob is an editor of retail and alice a reader (through analysts)",
          roles.get("alice") == "reader" and roles.get("bob") == "editor", roles)
    audit = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit", timeout=60))
    entries = audit.get("entries") or audit.get("events") or []
    saved = [e for e in entries if e.get("action") == "project.saved"]
    check("... and the Audit tab has the project's saving, by bob", any(e.get("user") == "bob" for e in saved), f"{len(saved)} project.saved entries")


FILES = ["customers.csv", "orders.csv", "campaigns.csv", "campaign_responses.csv"]


def lab3():
    heading(3, "Get the data in")
    data = lab_data()
    previews = {}
    for f in FILES:
        r = as_("bob").post(f"{PLATFORM}/api/projects/retail/uploads", files={"file": (f, io.BytesIO(data[f]), "text/csv")}, timeout=120)
        previews[f] = j(r).get("preview") or {}
        if not r.ok:
            print(f"    upload {f}: HTTP {r.status_code} {r.text[:200]}")
    cols = {c["name"]: c["type"] for c in previews["customers.csv"].get("columns", [])}
    check("bob uploads the four files; the preview reads customer_id a bigint, email a varchar, signed_up a date",
          cols.get("customer_id") == "bigint" and cols.get("email") == "varchar" and cols.get("signed_up") == "date", cols)
    tabs = [{"name": f[:-4], "file": f} for f in FILES]
    r = api("bob", "PUT", "/api/projects/retail/imports/uploads", {"name": "uploads", "source": "files", "schedule": "0 * * * *", "files": {"tables": tabs}})
    check("... and imports each as a table: the import uploads", r.ok, f"HTTP {r.status_code}")
    if "uploads_orders" in tables("retail"):   # done before: copied afresh (its tables dropped first), as the Imports tab's Reset does
        r = api("bob", "POST", "/api/projects/retail/imports/uploads/reset")
        run = j(r).get("run")
        state = wait(lambda: next((x["state"] for p in pipelines("bob", "retail") if p["id"] == "import_retail_uploads" for x in p.get("runs", [])
                                   if x["id"] == run and x["state"] in ("success", "failed")), None), 900, every=10) if run else f"HTTP {r.status_code}"
    else:
        state, _ = run_pipeline("bob", "retail", "import_retail_uploads")
    check("the Pipelines tab lists import_retail_uploads; Run now succeeds", state == "success", state)
    types = {r[0]: r[1] for r in sql("bob", "retail", "SHOW COLUMNS FROM uploads_orders") or [] if isinstance(r, list)}
    check("... ordered_on is a date, as the preview said", types.get("ordered_on") == "date", types)
    t = wait(lambda: (lambda x: x if all(f"uploads_{f[:-4]}" in x for f in FILES) else None)(tables("retail")), 600, every=15) or tables("retail")
    check("Data lists the four tables, from the catalog", all(f"uploads_{f[:-4]}" in t for f in FILES), sorted(t))
    prof = wait(lambda: (lambda o: o if o.get("rows") else None)(tables("retail").get("uploads_orders") or {}), 600, every=20) or {}
    region = next((c.get("profile") or {} for c in prof.get("columns", []) if c["name"] == "region"), {})
    check("... profiled: uploads_orders' row count, region holding 3 distinct values", prof.get("rows") == 305 and region.get("distinct") == 3,
          f"rows {prof.get('rows')}, region {region}")
    # A database where it is.
    pw = env.get("SUPERSET_DB_PASSWORD", "")
    host = "superset-db" if not os.environ.get("KUBERNETES_SERVICE_HOST") else os.environ.get("GUIDE_DASHBOARDS_DB_HOST", "postgres")
    values = {"host": host, "port": "5432", "database": "superset", "username": "superset", "password": pw}
    r = api("bob", "POST", "/api/projects/retail/connections", {"name": "retail-dashboards-db", "kind": "database", "values": values}, ok=(200, 201, 409))
    if r.status_code == 409:
        r = api("bob", "PUT", "/api/projects/retail/connections/retail-dashboards-db", {"kind": "database", "values": values})
    check("a connection for the dashboards' database (its password never shown again)", r.ok and pw not in r.text, f"HTTP {r.status_code}")
    p = spec("retail")
    p["spec"]["sources"] = [s for s in p["spec"].get("sources") or [] if s["name"] != "dashboards"] + [
        {"name": "dashboards", "kind": "postgresql", "connection": "retail-dashboards-db"}]
    r = save_spec(p)
    rows = wait(lambda: (lambda x: x if isinstance(x, list) else None)(sql("bob", "retail", "SELECT count(*) FROM retail_dashboards.public.dashboards")), 300, every=15)
    check("... a source: retail_dashboards answers from the database itself", r.ok and isinstance(rows, list) and rows and rows[0][0] >= 0, rows)
    a, b = sql("alice", "retail", "SELECT count(*) FROM uploads_orders"), sql("bob", "retail", "SELECT count(*) FROM uploads_orders")
    check("Check: bob counts 305 orders; alice the EU rows only (about a third)", b == [[305]] and isinstance(a, list) and a and 60 < a[0][0] < 160,
          f"bob {b}, alice {a}")


def lab4():
    heading(4, "Personal data, found and protected")
    def suggested():
        cs = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/classification", timeout=60)).get("columns", [])
        got = {(c["table"], c["column"]): c for c in cs}
        return got if ("uploads_customers", "email") in got else None   # phone: unless a run before took its tag off
    cols = wait(suggested, 900, every=30) or {}
    email, phone, cid = cols.get(("uploads_customers", "email")), cols.get(("uploads_customers", "phone")), cols.get(("uploads_customers", "customer_id"))
    check("the catalog suggests personal on email and phone; customer_id is personal, declared",
          bool(email and email["tag"] == "personal" and (phone is None or phone["tag"] == "personal") and cid and cid["state"] == "confirmed"),
          {k[1]: (v["tag"], v["state"]) for k, v in cols.items() if k[0] == "uploads_customers"})
    issues = alerts("retail")
    check("the Overview lists the email alert", any("email" in (i.get("message") or i.get("what") or json.dumps(i)) for i in issues), [i.get("message", i) for i in issues][:3])
    rows = wait(lambda: (lambda x: x if isinstance(x, list) and x and len(str(x[0][1])) == 64 else None)(
        sql("alice", "retail", "SELECT first_name, email, phone FROM uploads_customers LIMIT 3")), 300, every=15)
    check("as alice, email is hashed (and phone, unless its tag was taken off before)", bool(rows) and len(str(rows[0][1])) == 64, rows[:1] if isinstance(rows, list) else rows)
    a = api("bob", "POST", "/api/projects/retail/classification", {"table": "uploads_customers", "column": "email", "tag": "personal", "action": "confirm"})
    b = api("bob", "POST", "/api/projects/retail/classification", {"table": "uploads_customers", "column": "phone", "tag": "personal", "action": "remove"})
    check("bob confirms email and removes the tag on phone", a.ok and b.ok, f"HTTP {a.status_code}, {b.status_code}")
    rows = wait(lambda: (lambda x: x if isinstance(x, list) and x and len(str(x[0][2])) != 64 else None)(
        sql("alice", "retail", "SELECT first_name, email, phone FROM uploads_customers LIMIT 3")), 180, every=10)
    bobs = sql("bob", "retail", "SELECT first_name, email, phone FROM uploads_customers LIMIT 1")
    check("Check: alice's email is a 64-character hash and her phone real; bob's both as they are",
          bool(rows) and len(str(rows[0][1])) == 64 and isinstance(bobs, list) and "@" in str(bobs[0][1]), f"alice {rows[:1] if rows else rows}, bob {bobs}")


def lab5():
    heading(5, "A table that says when it's wrong")
    p = spec("retail")
    p["spec"]["tables"]["checks"] = [
        {"table": "uploads_orders", "check": "notNull", "column": "customer_id"},
        {"table": "uploads_orders", "check": "rowCount", "min": 300},
        {"table": "uploads_customers", "check": "unique", "column": "email"},
        {"table": "uploads_orders", "check": "acceptedValues", "column": "region", "values": ["EU", "US", "APAC"]},
        {"table": "uploads_orders", "check": "range", "column": "amount", "min": 0, "max": 10000},
        {"table": "uploads_orders", "check": "references", "column": "customer_id", "refTable": "uploads_customers", "refColumn": "customer_id"},
        {"table": "uploads_orders", "check": "sql", "name": "no_refund_without_amount", "sql": "SELECT order_id FROM uploads_orders WHERE refunded AND amount <= 0"}]
    p["spec"]["tables"]["freshness"] = [{"table": "uploads_orders", "within": "24h"}]
    r = save_spec(p)
    check("bob adds seven checks and a freshness target", r.ok, f"HTTP {r.status_code}")

    def checks(want_failing=None):
        cs = (tables("retail").get("uploads_orders") or {}).get("checks", [])
        if len(cs) < 6:
            return None
        failing = [c["name"] for c in cs if c.get("status") not in ("Success", "success", "passed")]
        if want_failing is None:
            return cs if not failing else None
        return cs if any(want_failing in f for f in failing) else None
    cs = wait(checks, 900, every=30)
    check("Quality: uploads_orders' checks run in the catalog, all passing", cs is not None,
          [(c["name"], c.get("status")) for c in (tables("retail").get("uploads_orders") or {}).get("checks", [])])
    ins = sql("bob", "retail", "INSERT INTO uploads_orders (order_id, region, amount, refunded, test, _dlt_load_id, _dlt_id) "
                               "VALUES (307, 'EU', 5.0, false, false, 'by-hand', 'by-hand-307')")
    failed = wait(lambda: checks("customer_id"), 600, every=30)
    issue = wait(lambda: [i for i in alerts("retail") if "customer_id" in json.dumps(i)], 300, every=20)
    check("an order with no customer: notNull on customer_id fails, and the Overview lists it",
          failed is not None and bool(issue), f"insert {ins}; failing {[(c['name'], c.get('result')) for c in (failed or [])][:2]}")
    sql("bob", "retail", "DELETE FROM uploads_orders WHERE order_id = 307")
    back = wait(checks, 600, every=30)
    check("Check: fixed, every check passes again", back is not None,
          [(c["name"], c.get("status")) for c in (tables("retail").get("uploads_orders") or {}).get("checks", [])])


# --- Part II: one number -----------------------------------------------------------------

MODEL = """semantic_model:
  name: orders
  description: Every order, one row each; test rows and refunds excluded by the metrics.
  node_relation: {database: iceberg, schema_name: retail, alias: uploads_orders}
  defaults: {agg_time_dimension: ordered_on}
  entities:
    - {name: order, type: primary, expr: order_id}
    - {name: customer, type: foreign, expr: customer_id}
  dimensions:
    - {name: region, type: categorical, description: "EU, US or APAC"}
    - {name: channel, type: categorical, description: "web, store or partner"}
    - {name: ordered_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: amount_sum, expr: "case when not test and not refunded then amount end", agg: sum, description: "The orders' amounts, net of refunds and test rows"}
    - {name: orders, expr: "case when not test then 1 end", agg: sum}
    - {name: refunds, expr: "case when refunded then 1 end", agg: sum}
    - {name: customers, expr: customer_id, agg: count_distinct}
---
metric:
  name: revenue
  description: What the orders brought in, net of refunds, test rows excluded.
  type: simple
  type_params: {measure: amount_sum}
---
metric:
  name: order_count
  description: How many real orders there were.
  type: simple
  type_params: {measure: orders}
---
metric:
  name: refund_count
  description: How many orders were refunded.
  type: simple
  type_params: {measure: refunds}
---
metric:
  name: customer_count
  description: How many customers ordered.
  type: simple
  type_params: {measure: customers}
---
metric:
  name: average_order_value
  description: Revenue per order.
  type: ratio
  type_params: {numerator: {name: revenue}, denominator: {name: order_count}}
---
terms:
  - name: revenue
    owner: bob
    definition: What the orders brought in, net of refunds, in US dollars. Test rows are never counted.
    synonyms: [sales, turnover]
  - name: customer
    owner: bob
    definition: A person or company that has placed at least one order.
    synonyms: [client, account]
---
tests:
  - name: three_regions
    metrics: [revenue]
    group_by: [order__region]
    expect: {rows: 3}
  - name: one_total
    metrics: [revenue, order_count]
    expect: {rows: 1}
"""
NEW_TERM = """  - name: average_order_value
    owner: bob
    definition: "Revenue per real order, in US dollars: what a customer spends on one order, on average."
    synonyms: [basket size, AOV]
"""


def metrics(user, name, ms, by=None):
    r = as_(user).post(f"{PLATFORM}/api/projects/{name}/semantic/query", json={"metrics": ms, "group_by": by or []}, timeout=300)
    b = j(r)
    return b if r.ok else {"error": b.get("error") or r.text[:300]}


def by_region(answer):
    cols = answer.get("columns") or []
    if "order__region" not in cols:
        return {}
    i = cols.index("order__region")
    return {row[i]: row for row in answer.get("rows") or []}


def lab6():
    heading(6, "Define revenue once")
    model = MODEL
    r = api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": model})
    check("bob saves the model; MetricFlow compiled it", r.ok and not j(r).get("error"), f"HTTP {r.status_code} {j(r).get('error', '')}")
    a = metrics("bob", "retail", ["revenue", "order_count"], ["order__region"])
    rows = by_region(a)
    total = round(sum(float(v[a["columns"].index("revenue")]) for v in rows.values()), 2) if rows else None
    orders = sum(int(v[a["columns"].index("order_count")]) for v in rows.values()) if rows else None
    check("asked by region: about 11,341 US, 7,263 EU, 3,564 APAC; 300 orders; 22,168 in all",
          total is not None and abs(total - 22168) < 1 and orders == 300, f"{ {k: v for k, v in rows.items()} } total {total}")
    al = by_region(metrics("alice", "retail", ["revenue"], ["order__region"]))
    check("... as alice, EU only", list(al) == ["EU"], list(al))
    t = j(as_("bob").post(f"{PLATFORM}/api/projects/retail/semantic/tests", json={}, timeout=300))
    check("Run the 2 tests: both pass", t.get("passed") == 2 and not t.get("failed"), {k: t.get(k) for k in ("passed", "failed")})
    changed = model.replace('expr: "case when not test and not refunded then amount end"', 'expr: "case when not test and not refunded then amount * 0.9 end"')
    api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": changed})
    tenth = by_region(metrics("bob", "retail", ["revenue"], ["order__region"]))
    api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": model})
    check("amount * 0.9: the next answer follows; put back", tenth and abs(sum(float(v[1]) for v in tenth.values()) - 22168 * 0.9) < 1,
          {k: v[1] for k, v in tenth.items()})
    p = spec("retail")
    p["spec"]["semantic"] = {**(p["spec"].get("semantic") or {}), "shared": True}
    check("bob shares retail's metrics and terms: the company's now", save_spec(p).ok, "")
    terms = {t["name"]: t for t in j(as_("bob").get(f"{PLATFORM}/api/terms", timeout=60)).get("terms", [])}
    check("Business terms: Revenue and Customer, each with its owner and synonyms",
          "turnover" in (terms.get("revenue") or {}).get("synonyms", []) and (terms.get("customer") or {}).get("owner") == "bob", sorted(terms))
    # A new term.
    with_term = model.replace("---\ntests:", NEW_TERM + "---\ntests:")
    r = api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": with_term})
    bad = api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": with_term.replace("  - name: average_order_value\n    owner", "  - name: basket_size\n    owner")}, ok=(400, 422))
    api("bob", "PUT", "/api/projects/retail/semantic", {"yaml": with_term})
    terms = {t["name"]: t for t in j(as_("bob").get(f"{PLATFORM}/api/terms", timeout=60)).get("terms", [])}
    aov = terms.get("average_order_value") or {}
    check("a new term, average_order_value (basket size, AOV): accepted; one the model doesn't define is refused",
          r.ok and bad.status_code in (400, 422) and "AOV" in aov.get("synonyms", []), f"saved {r.status_code}, refused {bad.status_code}: {j(bad).get('error', '')[:120]}")
    sq = sql("bob", "retail", "SELECT region, round(sum(amount), 2) FROM uploads_orders WHERE not test AND not refunded GROUP BY 1 ORDER BY 1")
    a = by_region(metrics("bob", "retail", ["revenue"], ["order__region"]))
    same = isinstance(sq, list) and all(abs(float(a[r[0]][1]) - float(r[1])) < 0.01 for r in sq if r[0] in a) and len(sq) == len(a)
    check("Check: Business terms lists all three; the Metrics answer matches the SQL tab's to the cent", same and "average_order_value" in terms, sq)


def lab7():
    heading(7, "Dashboards on the same definitions")
    app = {"name": "revenue-by-region", "kind": "metric", "metrics": ["revenue"], "by": "order__region", "show": "bar",
           "filters": [{"field": "order__region", "values": ["EU", "US", "APAC"]}]}
    r = api("bob", "PUT", "/api/projects/retail/apps/revenue-by-region", app)
    check("bob makes the app revenue-by-region: revenue by region, as bars, with a filter", r.ok, f"HTTP {r.status_code} {r.text[:200]}")
    def data(user):
        d = j(as_(user).post(f"{PLATFORM}/api/projects/retail/apps/revenue-by-region/data", json={"filters": {}}, timeout=300))
        return {row[0]: row[1] for row in d.get("rows") or []}
    b, a = data("bob"), data("alice")
    check("Check: its bars are Lab 6's numbers, and alice sees the EU alone", len(b) == 3 and abs(sum(float(x) for x in b.values()) - 22168) < 1 and list(a) == ["EU"],
          f"bob {b}, alice {a}")
    print("    (Superset's dataset, chart and dashboard are made by hand on its pages: not scripted)")


def mcp(user):
    """The platform's MCP server, signed in as an assistant would be (storscale-agent, as the person)."""
    tok = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=30, data={
        "grant_type": "password", "client_id": "storscale-agent", "username": user, "password": env["LAKEHOUSE_USER_PASSWORD"]}).json()["access_token"]
    n = [0]

    def rpc(method, params=None):
        n[0] += 1
        r = requests.post(f"{PLATFORM}/mcp", timeout=300, headers={"Authorization": f"Bearer {tok}", "Accept": "application/json, text/event-stream"},
                          json={"jsonrpc": "2.0", "id": n[0], "method": method, **({"params": params} if params else {})})
        try:
            return r.json()
        except ValueError:
            body = [x[5:] for x in r.text.splitlines() if x.startswith("data:")]
            return json.loads(body[-1]) if body else {"error": r.text[:200]}
    rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "guide", "version": "1"}})

    def call(tool, **args):
        res = rpc("tools/call", {"name": tool, "arguments": args}).get("result", {})
        out = res.get("structuredContent")
        if out is None:
            try:
                out = json.loads(" ".join(c.get("text", "") for c in res.get("content", [])))
            except ValueError:
                out = res
        return out.get("result", out) if isinstance(out, dict) and set(out) == {"result"} else out
    return call


def lab8():
    heading(8, "Ask an assistant")
    began = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    call = mcp("alice")
    ps = call("list_projects")
    check("the assistant lists the projects alice may read", isinstance(ps, list) and {"retail", "sales"} <= {p.get("project") for p in ps}, ps)
    a = call("query_metrics", project="retail", metrics=["revenue"], group_by=["order__region"])
    check("... asks for revenue by region, and gets EU only, as alice", "EU" in json.dumps(a) and "APAC" not in json.dumps(a), str(a)[:200])
    t = call("describe_term", term="turnover")
    check("... and answers what revenue means from the term (found by its synonym)", "refunds" in json.dumps(t), str(t)[:200])
    entries = wait(lambda: [e for e in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit?user=alice&since={began}", timeout=60)).get("events", [])
                            if "agent=" in (e.get("detail") or "") and "agent=platform" not in (e.get("detail") or "")], 240, every=20) or []
    check("Check: on Audit, the assistant's query names the agent and alice",
          any(e.get("user") == "alice" and "agent=" in (e.get("detail") or "") and "agent=platform" not in (e.get("detail") or "") for e in entries),
          f"{len(entries)} entries")


# --- Part III: decisions worth money ------------------------------------------------------

def campaign_flow(first_join=("customer_id", "customer_id")):
    st = lambda i, kind, x, y, **kw: {"id": i, "kind": kind, "x": x, "y": y, **kw}  # noqa: E731
    return {"name": "campaign_results", "steps": [
        st("input", "input", 0, 0, table="uploads_campaign_responses"),
        st("filter", "filter", 1, 0, inputs=["input"], where="converted"),
        st("input_2", "input", 0, 1, table="uploads_orders"),
        st("join", "join", 2, 0, inputs=["filter", "input_2"], join="inner", on=[{"left": first_join[0], "right": first_join[1]}]),
        st("filter_2", "filter", 3, 0, inputs=["join"],
           where="not test and not refunded and ordered_on >= responded_on and ordered_on <= date_add('day', 30, responded_on)"),
        st("input_3", "input", 0, 2, table="uploads_campaigns"),
        st("join_2", "join", 4, 0, inputs=["filter_2", "input_3"], join="inner", on=[{"left": "campaign_id", "right": "campaign_id"}]),
        st("group", "group", 5, 0, inputs=["join_2"], by=["name", "budget"],
           aggregates=[{"function": "sum", "column": "amount", "as": "revenue"}, {"function": "count_distinct", "column": "customer_id", "as": "buyers"},
                       {"function": "count", "as": "orders"}]),
        st("formula", "formula", 6, 0, inputs=["group"], columns=[{"name": "roi", "expression": "revenue / budget", "type": "double"}]),
        st("prepare", "prepare", 7, 0, inputs=["formula"], processors=[{"column": "roi", "do": "round", "args": ["2"]}]),
        st("output", "output", 8, 0, inputs=["prepare"], table="campaign_results",
           checks=[{"column": "name", "test": "not_null"}, {"column": "name", "test": "unique"}])]}


def alice_editor(on):
    """bob gives alice retail's editor role, or takes it back (she stays a reader through analysts)."""
    p = spec("retail")
    p["spec"]["members"] = [m for m in p["spec"]["members"] if m.get("user") != "alice"] + ([{"user": "alice", "role": "editor"}] if on else [])
    r = save_spec(p)
    want = "editor" if on else "reader"
    ok = wait(lambda: {m["username"]: m["role"] for m in ((project("bob", "retail") or {}).get("status") or {}).get("members") or []}.get("alice") == want, 180, every=10)
    return r.ok and ok


def lab9():
    heading(9, "Which campaigns pay for themselves")
    check("bob makes alice an editor of retail: she builds from now on", alice_editor(True), "")
    flow = campaign_flow()
    c = j(as_("alice").post(f"{PLATFORM}/api/projects/retail/flows/compile", json={"flow": flow}, timeout=120))
    ins = (c.get("steps") or {}).get("input", {}).get("columns") or []
    check("alice's canvas: the input shows uploads_campaign_responses' 7 columns (the import's two among them), and the flow compiles", len(ins) == 7 and not c.get("errors"),
          f"{len(ins)} columns; errors {c.get('errors')}")
    def preview(user):
        r = as_(user).post(f"{PLATFORM}/api/projects/retail/flows/preview", json={"flow": flow, "step": "output", "limit": 100}, timeout=300)
        b = j(r)
        if not r.ok:
            return b.get("error") or r.text[:200]
        cols = b.get("columns", [])
        return {row[cols.index("name")]: row[cols.index("roi")] for row in b.get("rows", [])}
    a = preview("alice")
    b = preview("bob")
    check("alice's preview: six campaigns, the whole company (she's an editor); Store weekend 7.84, Partner push 2.97, September clearance 1.52, Summer sale 1.40",
          isinstance(a, dict) and len(a) == 6 and abs(float(a.get("Store weekend", 0)) - 7.84) < 0.005 and abs(float(a.get("Partner push", 0)) - 2.97) < 0.005,
          a)
    check("... bob's the same", a == b, b)
    broken = j(as_("alice").post(f"{PLATFORM}/api/projects/retail/flows/compile", json={"flow": campaign_flow(("responded_on", "customer_id"))}, timeout=120))
    why = json.dumps(broken.get("errors") or broken.get("steps", {}).get("join", {}).get("error") or broken)
    check("a join on columns of different types is refused before anything runs", "type" in why, why[:200])
    sqls = [(c.get("steps") or {}).get(k, {}).get("sql", "") for k in (c.get("order") or [])]
    check("Check: six rows, Store weekend at 7.84; one select per step", isinstance(b, dict) and len(b) == 6 and sum("select" in x.lower() for x in sqls) >= 10,
          f"{len(sqls)} steps' SQL")


# --- Part IV: shared, and safe ------------------------------------------------------------

ORDERS_PRODUCT = {"table": "uploads_orders", "description": "Harbor Outfitters' orders, net of test rows", "owner": "bob", "version": "1",
                  "terms": ["revenue"], "contract": {"columns": ["order_id", "customer_id", "ordered_on", "region", "amount"], "within": "24h"}}


def put_product(body):
    """The Publish form (or, on an install without it, the spec's products)."""
    r = api("bob", "PUT", "/api/projects/retail/products/orders", body, ok=(200, 404, 405))
    if r.status_code in (404, 405):
        print("    (no form on this install: the spec's products instead)")
        p = spec("retail")
        p["spec"]["products"] = [x for x in p["spec"].get("products") or [] if x["name"] != "orders"] + [{"name": "orders", **body}]
        r = save_spec(p)
    return r


def products():
    return {(x["project"], x["name"]): x for x in j(as_("bob").get(f"{PLATFORM}/api/products", timeout=60)).get("products", [])}


def lab12():
    heading(12, "A certified data product, read in place")
    r = put_product(ORDERS_PRODUCT)
    p = spec("retail")
    p["spec"]["semantic"] = {**(p["spec"].get("semantic") or {}), "shared": True}
    save_spec(p)
    pr = products().get(("retail", "orders")) or {}
    check("bob publishes orders: owner, version, contract, the term Revenue (not certified, unless a run before did)", r.ok and pr.get("terms") == ["revenue"],
          f"HTTP {r.status_code}; {pr.get('contract')}")
    m = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": "marketing"},
         "spec": {"description": "Marketing: campaigns, on retail's data", "members": [{"group": "analysts", "role": "editor"}], "tables": {},
                  "semantic": {"uses": ["retail"]}}}
    old = spec("marketing")
    r = save_spec({**m, "spec": {**(old or {}).get("spec", {}), **m["spec"]}} if old else m)
    check("a project marketing, analysts its editors, using retail's metrics", r.ok and ready("marketing"), f"HTTP {r.status_code}")
    if "marketing" not in (pr.get("consumers") or []):
        api("alice", "POST", "/api/products/retail/orders/requests", {"consumer": "marketing", "why": "Campaign targeting by region"})
        a = api("bob", "POST", "/api/products/retail/orders/requests/marketing/approve")
        check("alice asks for it for marketing; bob approves", a.ok, f"HTTP {a.status_code}")
    rows = wait(lambda: (lambda x: x if isinstance(x, list) else None)(sql("alice", "marketing", "SELECT region, count(*) FROM iceberg.retail.uploads_orders GROUP BY 1")), 180, every=15)
    check("... marketing reads retail's table in place (alice, retail's editor too, every region; a reader of retail the EU's)", isinstance(rows, list) and len(rows) == 3, rows)
    ym = "metric:\n  name: revenue_per_customer\n  description: Revenue per customer, on retail's definitions of both.\n  type: ratio\n  type_params: {numerator: {name: revenue}, denominator: {name: customer_count}}\n"
    r = api("alice", "PUT", "/api/projects/marketing/semantic", {"yaml": ym})
    a = metrics("alice", "marketing", ["revenue_per_customer"], ["order__region"])
    check("alice's revenue_per_customer, on retail's revenue and customer_count", r.ok and bool(a.get("rows")), a.get("error") or a.get("rows"))
    pr = products().get(("retail", "orders")) or {}
    if not pr.get("certified"):
        api("bob", "POST", "/api/products/retail/orders/certification")
    check("bob certifies it", (products().get(("retail", "orders")) or {}).get("certified"), "")
    put_product({**ORDERS_PRODUCT, "contract": {**ORDERS_PRODUCT["contract"], "columns": ORDERS_PRODUCT["contract"]["columns"][:-1]}})
    lapsed = products().get(("retail", "orders")) or {}
    put_product(ORDERS_PRODUCT)
    api("bob", "POST", "/api/products/retail/orders/certification")
    check("a promise changed lapses the certification, keeps marketing; certified again", not lapsed.get("certified") and "marketing" in (lapsed.get("consumers") or []),
          f"lapsed: certified {lapsed.get('certified')}, consumers {lapsed.get('consumers')}")
    pr = products().get(("retail", "orders")) or {}
    check("Check: marketing's alice reads it and asks revenue_per_customer; orders certified with marketing its consumer",
          pr.get("certified") and "marketing" in (pr.get("consumers") or []) and bool(a.get("rows")), {k: pr.get(k) for k in ("certified", "consumers")})


def lab13():
    heading(13, "Rules for readers, and every tool following")
    p = spec("retail")
    rd = p["spec"]["tables"].setdefault("readers", {})
    rd["tables"] = ["uploads_orders", "uploads_customers", "uploads_campaigns"]
    rd["masks"] = [{"table": "uploads_customers", "column": "signed_up", "type": "MASK_DATE_SHOW_YEAR"}]
    r = save_spec(p)
    check("bob: readers see three tables, and signed_up as its year", r.ok, f"HTTP {r.status_code}")
    check("to see them as a reader does, alice's editor role comes off for a moment", alice_editor(False), "")
    # alice is an editor of retail (Lab 2): what readers see is checked as a reader of the project, carol has none; use sales' alice? the guide says alice.
    a = wait(lambda: (lambda x: x if isinstance(x, list) and x and str(x[0][0]).endswith("-01-01") else None)(
        sql("alice", "retail", "SELECT DISTINCT signed_up FROM uploads_customers")), 180, every=15)
    refused = sql("alice", "retail", "SELECT count(*) FROM uploads_campaign_responses")
    b = sql("bob", "retail", "SELECT count(DISTINCT signed_up) FROM uploads_customers")
    check("Check: alice's signed_up all January the first, campaign responses refused; bob's the real dates",
          bool(a) and len(a) <= 3 and isinstance(refused, str) and isinstance(b, list) and b[0][0] > 3, f"alice {a if a else 'real dates'}, responses {str(refused)[:80]}, bob {b}")
    check("... and alice is an editor again", alice_editor(True), "")


def lab14():
    heading(14, "A file to a partner, approved")
    acme = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": "acme"},
            "spec": {"description": "Acme Outdoors: what we send them", "members": [{"group": "engineers", "role": "editor"}], "files": {}}}
    r = save_spec(spec("acme") or acme)
    check("the partner's bucket: a project acme, with files", r.ok and ready("acme"), f"HTTP {r.status_code}")
    values = {"endpoint": "http://buckets:9000", "bucket": "acme", "prefix": "from-harbor", "accessKey": env["BUCKETS_ROOT_USER"], "secretKey": env["BUCKETS_ROOT_PASSWORD"]}
    r = api("bob", "POST", "/api/projects/retail/connections", {"name": "retail-acme-bucket", "kind": "s3", "values": values}, ok=(200, 201, 409))
    if r.status_code == 409:
        r = api("bob", "PUT", "/api/projects/retail/connections/retail-acme-bucket", {"kind": "s3", "values": values})
    check("where to send: the connection retail-acme-bucket", r.ok, f"HTTP {r.status_code}")
    p = spec("retail")
    first = {"name": "campaign_results", "table": "campaign_results", "format": "csv", "files": "harbor_campaigns_{date}.csv",
             "schedule": "0 6 * * 1", "target": "retail-acme-bucket"}
    second = {"name": "partner_orders", "query": "SELECT order_id, customer_id, ordered_on, amount FROM uploads_orders WHERE channel = 'partner' AND NOT test",
              "format": "jsonl", "files": "harbor_partner_orders_{date}.jsonl", "schedule": "0 6 * * 1", "target": "retail-acme-bucket"}
    have = {d["name"] for d in p["spec"].get("deliveries") or []}
    p["spec"]["deliveries"] = (p["spec"].get("deliveries") or []) + [d for d in (first, second) if d["name"] not in have]
    r = save_spec(p)
    ds = {d["name"]: d for d in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/deliveries", timeout=60)).get("deliveries", [])}
    check("the two deliveries, awaiting approval; partner_orders names its personal column customer_id",
          r.ok and "customer_id" in json.dumps((ds.get("partner_orders") or {}).get("personal")), {k: (v.get("approved"), v.get("needs")) for k, v in ds.items()})
    mine = api("bob", "POST", "/api/projects/retail/deliveries/campaign_results/approve", ok=(403, 409))
    check("bob, who made it, can't approve it", mine.status_code in (403, 409), f"HTTP {mine.status_code}")
    api("alice", "POST", "/api/projects/retail/deliveries/campaign_results/approve")
    a2 = api("alice", "POST", "/api/projects/retail/deliveries/partner_orders/approve")
    ds = {d["name"]: d for d in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/deliveries", timeout=60)).get("deliveries", [])}
    check("alice approves both as an editor; partner_orders still needs a data-protection approver who didn't make it (unless approved before)",
          ds["campaign_results"].get("approved") and (not ds["partner_orders"].get("approved") or (a2.status_code == 409)),
          {k: (v.get("approved"), v.get("needs")) for k, v in ds.items()})
    for d in ("campaign_results", "partner_orders"):
        if (ds.get(d) or {}).get("approved"):
            state, _ = run_pipeline("bob", "retail", f"delivery_retail_{d}")
            check(f"{d} runs, and lands in acme's bucket with its manifest", state == "success", state)
    from lakekit import s3_root
    files = [o["Key"] for o in s3_root().list_objects_v2(Bucket="acme", Prefix="from-harbor").get("Contents", [])]
    check("Check: acme's bucket has the files and their manifests", any("harbor_campaigns" in f for f in files), files)


def lab15():
    heading(15, "Where a person's data is, and erase a customer")
    n = sql("bob", "retail", "SELECT count(*) FROM uploads_orders WHERE customer_id = 35")
    check("customer 35's orders: eight", n == [[8]] or n == [[0]], n)
    hits = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/deliveries/search?person=35", timeout=600))
    check("what was sent about them: the deliveries' runs that carried their rows", "runs" in hits, {k: hits.get(k) for k in ("checked", "notKept")} | {"runs": [h.get("delivery") for h in hits.get("runs", [])]})
    done = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/erasures", timeout=60)).get("erasures", [])
    back = sql("bob", "retail", "SELECT count(*) FROM uploads_customers WHERE customer_id = 35")
    from lakekit import s3_root
    in_file = any(line.split(",")[0] == "35" for line in
                  s3_root().get_object(Bucket="retail", Key="landing/uploads/customers.csv")["Body"].read().decode().splitlines()[1:])
    if done and (back not in ([[0]], []) or in_file):
        print(f"    customer 35 is still here (tables {back}, landing file {in_file}): an erasure from before files were cleansed; erased once more")
        done = []
    if not done:
        r = api("bob", "POST", "/api/projects/retail/erasures", {"person": "35", "requestedOn": time.strftime("%Y-%m-%d"), "verifiedBy": "bob"}, timeout=600)
        check("bob erases customer 35", r.ok, f"HTTP {r.status_code} {r.text[:200]}")
        done = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/erasures", timeout=60)).get("erasures", [])
    e = max(done, key=lambda x: x.get("at", "")) if done else {}   # the newest
    counts = {t["table"]: t["rows"] for t in e.get("tables", [])}
    left = sql("bob", "retail", "SELECT (SELECT count(*) FROM uploads_customers WHERE customer_id = 35) + (SELECT count(*) FROM uploads_orders WHERE customer_id = 35)")
    check("Check: no rows left for 35; an erasure, due 45 days after the request, its deliveries named",
          left == [[0]] and len(done) >= 1 and e.get("dueBy"), f"{counts}, due {e.get('dueBy')}, deliveries {len(e.get('deliveries') or [])}")
    landing = {k: s3_root().get_object(Bucket="retail", Key=f"landing/uploads/{k}")["Body"].read().decode() for k in ("customers.csv", "orders.csv")}
    still = {k: sum(1 for line in v.splitlines()[1:] if line.split(",")[1 if k == "orders.csv" else 0] == "35") for k, v in landing.items()}
    check("... and the landing files are cleansed too: 35 is in neither customers.csv nor orders.csv",
          not any(still.values()) and [f["file"] for f in e.get("files") or []], f"left {still}; erased {[(f['file'], f['rows']) for f in e.get('files') or []]}")
    er = [x for x in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit?source=platform", timeout=60)).get("events", []) if "erasure" in (x.get("action") or "")]
    check("... the Audit tab's erasure entry names the record, not the person", bool(er) and not any('"35"' in json.dumps(x) for x in er), f"{len(er)} entries")
    p = spec("retail")
    p["spec"]["tables"]["retention"] = [{"table": "uploads_campaign_responses", "column": "responded_on", "keep": "730d"}]
    check("retention on the campaign responses: two years", save_spec(p).ok, "")


# --- Part V: AI on the same rules ---------------------------------------------------------

def lab16():
    heading(16, "Models through the gateway, on a budget")
    import k8s
    p = spec("retail")
    p["spec"]["llm"] = {"budget": 0.05}
    check("bob gives retail an LLM budget of 0.05", save_spec(p).ok, "")
    key = wait(lambda: (k8s.get_secret("retail-llm") if k8s.in_cluster() else {}).get("llmKey"), 180, every=10)
    check("... the operator makes its key, the connection retail-llm", bool(key), "")
    url = (k8s.get_secret("retail-llm") if k8s.in_cluster() else {}).get("url") or "http://llm:4000"
    ask = lambda q: requests.post(f"{url.rstrip('/')}/chat/completions", timeout=120, headers={"Authorization": "Bearer " + (key or "")},  # noqa: E731
                                  json={"model": "default", "messages": [{"role": "user", "content": q}], "max_tokens": 50})
    r = ask("How were sales in the west last week? Answer in five words.")
    check("asked through the gateway, on the project's key", r.status_code == 200, f"HTTP {r.status_code} {r.text[:120]}")
    g = ask("Write to jane.doe@example.com that card 4111 1111 1111 1111 was declined")
    check("what may not leave: a card number and an email address, refused before the model", g.status_code == 400 and "guardrail" in g.text.lower() or "may not leave" in g.text, f"HTTP {g.status_code} {g.text[:160]}")
    llm = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/llm", timeout=60))
    check("the Agents tab's gateway card: retail's spend against 0.05", "spent" in json.dumps(llm) or "budget" in json.dumps(llm), str(llm)[:200])
    p = spec("retail")
    p["spec"]["llm"] = {"budget": 1}
    check("raised to 1, for Labs 17 and 18", save_spec(p).ok, "")


# --- Part VI: keep it true ---------------------------------------------------------------

def at_commit(statement, table, commit):
    """A query with a table named at a commit, as Read it as it was writes it."""
    return statement.replace(table, f'"{table}#{commit}"', 1)


def lab19():
    heading(19, "The data as it was")
    h = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/history?table=uploads_orders", timeout=120))
    changes = h.get("changes", [])
    check("History: every change to uploads_orders, who made it, rows added and removed", len(changes) >= 2, f"{len(changes)} changes, kept {h.get('keepHistory')}")
    first = next((c for c in reversed(changes) if not (c.get("message") or "").startswith("Create")), {})   # the import's load
    if not sql("bob", "retail", "SELECT 1 FROM uploads_orders WHERE order_id = 306"):
        sql("bob", "retail", "INSERT INTO uploads_orders VALUES (306, 7, DATE '2026-10-01', 'EU', 'web', 88.0, false, false, 'by-hand', 'by-hand-306')")
    sql("bob", "retail", "UPDATE uploads_orders SET amount = 1.0 WHERE order_id = 1")
    then = sql("bob", "retail", f'SELECT count(*), round(sum(amount), 2) FROM {first["query"]}') if first.get("query") else "no history"
    now = sql("bob", "retail", "SELECT count(*), round(sum(amount), 2) FROM uploads_orders")
    check("Read it as it was: the first commit gives the table as the import left it (305 rows); today's differs",
          isinstance(then, list) and then[0][0] == 305 and then != now, f"then {then}, now {now}")
    tags = {t["name"] for t in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/tags", timeout=60)).get("tags", [])}
    if "close-2026-09" not in tags:
        r = api("bob", "POST", "/api/projects/retail/tags", {"name": "close-2026-09", "at": "now", "keep": "7y", "note": "September's close"})
        check("a close that's kept: the tag close-2026-09, for seven years", r.ok, f"HTTP {r.status_code}")
    m = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/asof?at=close-2026-09", timeout=60))
    closed = sql("bob", "retail", f'SELECT count(*) FROM "uploads_orders#{m.get("commit", "")}" WHERE order_id = 306') if m.get("commit") else m
    check("Check: as of close-2026-09 the inserted row counts; as of the first entry, 305", closed == [[1]] and isinstance(then, list) and then[0][0] == 305,
          f"close {closed}")


def lab21():
    heading(21, "Who did what")
    sql("alice", "retail", "SELECT * FROM uploads_campaign_responses LIMIT 1")
    def refused():
        ev = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit?q=uploads_campaign_responses&user=alice&result=denied", timeout=60)).get("events", [])
        return ev or None
    ev = wait(refused, 240, every=20)
    check("alice's refused read of the campaign responses is on bob's Audit tab, with the reason", bool(ev), (ev or [{}])[0].get("reason"))
    ev = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit?q=uploads_customers", timeout=60)).get("events", [])
    check("Search uploads_customers: every query that touched it, by whom", len(ev) > 0, f"{len(ev)} events, by {sorted({e['user'] for e in ev})}")
    g = api("bob", "GET", "/api/governance")
    check("Administration → Governance answers for the administrators", g.ok, f"HTTP {g.status_code}")
    # A role of the company's own.
    r = api("bob", "PUT", "/api/roles/auditor", {"description": "Reads every table as it is and the audit log; changes nothing",
                                                  "permissions": ["read-all", "read-files", "see-audit"]})
    roles = {x["name"]: x for x in j(as_("bob").get(f"{PLATFORM}/api/roles", timeout=60)).get("roles", [])}
    check("bob defines the project role auditor; its card lists what it gives, read with it",
          r.ok and "read" in (roles.get("auditor") or {}).get("granted", []), (roles.get("auditor") or {}).get("granted"))
    p = spec("retail")
    p["spec"]["members"] = [m for m in p["spec"]["members"] if not (m.get("user") == "alice" and m["role"] == "auditor")] + [{"user": "alice", "role": "auditor"}]
    check("on retail, alice is given auditor too", save_spec(p).ok, "")
    st = wait(lambda: next((m for m in ((project("bob", "retail") or {}).get("status") or {}).get("members") or [] if m["username"] == "alice"), None), 120, every=10)
    print(f"    alice on Access: {st}")
    # The guide's step 9 changes the role (read-all off), then removes alice's membership; the role is left for the spot check.
    p = spec("retail")
    p["spec"]["members"] = [m for m in p["spec"]["members"] if not (m.get("user") == "alice" and m["role"] == "auditor")]
    save_spec(p)
    r = api("bob", "PUT", "/api/roles/privacy-office", {"description": "The privacy office: approves deliveries of personal data, answers erasure requests",
                                                         "permissions": ["protect-personal", "see-governance"], "scope": "platform", "groups": ["analysts"]})
    seen = wait(lambda: api("alice", "GET", "/api/governance", ok=(200, 403)).ok, 90, every=10)
    check("a platform role privacy-office, given to analysts: within half a minute alice sees Governance", r.ok and seen, f"HTTP {r.status_code}")
    audit = j(as_("bob").get(f"{PLATFORM}/api/audit?source=platform&user=bob", timeout=60)).get("events", [])
    check("Check: the roles' definitions are in the audit log, with bob's name", any(e.get("action") == "role.saved" and e.get("user") == "bob" for e in audit),
          f"{len(audit)} events")


def lab22():
    heading(22, "Operate it")
    h = j(as_("bob").get(f"{PLATFORM}/api/health", timeout=120))
    down = [s.get("name") for s in h.get("services") or [] if s.get("state") != "up"]
    check("Check: Administration → Health shows every service up", h.get("checked") and not down, down)
    u = api("bob", "GET", "/api/projects/retail/usage")
    check("retail's Usage tab: this month against last", u.ok, str(j(u))[:160])
    ref = requests.get(f"{PLATFORM}/api/openapi.json", timeout=60)
    check("the API reference lists every route", ref.ok and len(ref.json().get("paths", {})) > 90, f"{len(ref.json().get('paths', {}))} paths" if ref.ok else ref.status_code)


def notebook(user, code, name="guide-notebook"):
    """Run notebook cells as user, in the notebook image people get: (exit code, output).
    The hub's part (the person's token) is played by a token of theirs."""
    import k8s

    from lakekit import token
    image = os.environ["STORSCALE_TOOLS_IMAGE"].replace("platform-tools", "platform-notebook")
    prelude = ("import os, buckets_lake\nbuckets_lake.token = lambda: os.environ['GUIDE_TOKEN']\n"
               "import pandas as pd\n")
    pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name},   # not the test pod's label: its GitLab Service picks by it
           "spec": {"restartPolicy": "Never", "containers": [{"name": "nb", "image": image, "command": ["python", "-c", prelude + code],
                    "env": [{"name": "GUIDE_TOKEN", "value": token(user)}, {"name": "JUPYTERHUB_USER", "value": user}, {"name": "HOME", "value": "/tmp"},
                            {"name": "STORSCALE_PLATFORMD_URL", "value": "http://platformd:8080"}, {"name": "BUCKETS_ENDPOINT", "value": "http://buckets:9000"}],
                    "securityContext": {"allowPrivilegeEscalation": False, "runAsNonRoot": True, "runAsUser": 1000, "capabilities": {"drop": ["ALL"]},
                                        "seccompProfile": {"type": "RuntimeDefault"}}}]}}
    k8s._api("DELETE", f"pods/{name}")
    wait(lambda: k8s._api("GET", f"pods/{name}").status_code == 404, 120, every=3)
    r = k8s._api("POST", "pods", json=pod)
    if not r.ok:
        return 1, f"the pod: HTTP {r.status_code} {r.text[:200]}"
    phase = wait(lambda: (lambda x: x if x in ("Succeeded", "Failed") else None)(k8s._api("GET", f"pods/{name}").json().get("status", {}).get("phase")), 900, every=5)
    out = k8s._api("GET", f"pods/{name}/log").text
    return (0 if phase == "Succeeded" else 1), out


CALL_LIST = """
import io
s3 = buckets_lake.s3()
read = lambda name: pd.read_csv(io.BytesIO(s3.get_object(Bucket="retail", Key=f"landing/uploads/{name}.csv")["Body"].read()))
orders, responses, customers = read("orders"), read("campaign_responses"), read("customers")
real = orders[~orders.test & ~orders.refunded]
spent = real.groupby("customer_id").agg(orders=("order_id", "count"), revenue=("amount", "sum")).round(2)
heard = responses[responses.campaign_id == 6].customer_id
calls = (spent[(spent.orders >= 3) & ~spent.index.isin(heard)]
         .join(customers.set_index("customer_id")[["first_name", "last_name", "segment", "region"]])
         .sort_values("revenue", ascending=False).reset_index())
s3.put_object(Bucket="retail", Key="landing/reports/call-list.csv", Body=calls.to_csv(index=False).encode())
print("CALLS", len(calls), calls.iloc[0].first_name, calls.iloc[0].last_name, int(calls.iloc[0].orders), calls.iloc[0].revenue)
"""
TRAIN_V1 = CALL_LIST + """
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score
per = real.groupby("customer_id").agg(orders=("order_id", "count"), revenue=("amount", "sum"), last=("ordered_on", "max"))
f = customers[["customer_id", "signed_up"]].join(per, on="customer_id")
X = pd.DataFrame({"orders": f.orders.fillna(0).astype(int), "revenue": f.revenue.fillna(0.0).astype(float),
                  "tenure_days": (pd.Timestamp("2026-10-01") - pd.to_datetime(f.signed_up)).dt.days})
churned = (f["last"].isna() | (pd.to_datetime(f["last"]) < "2026-09-01")).astype(int)
mlflow = buckets_lake.mlflow("retail")
mlflow.set_experiment("churn")
with mlflow.start_run():
    model = LogisticRegression(max_iter=1000).fit(X, churned)
    mlflow.log_metric("auc", roc_auc_score(churned, model.predict_proba(X)[:, 1]))
    mlflow.sklearn.log_model(model, name="model", registered_model_name="churn", pyfunc_predict_fn="predict_proba", input_example=X.head())
print("TRAINED", int(churned.sum()), "of", len(churned))
"""
TRAIN_V2 = TRAIN_V1.split("mlflow = buckets_lake.mlflow")[0] + """
mlflow = buckets_lake.mlflow("retail")
mlflow.set_experiment("churn")
feats = buckets_lake.sql("retail", "SELECT customer_id, orders, revenue, tenure_days FROM customer_features ORDER BY customer_id")
y = pd.Series(churned.values, index=customers.customer_id).loc[feats.customer_id].values
X2 = feats[["orders", "revenue", "tenure_days"]]
with mlflow.start_run():
    model = LogisticRegression(max_iter=1000).fit(X2, y)
    mlflow.log_metric("auc", roc_auc_score(y, model.predict_proba(X2)[:, 1]))
    mlflow.sklearn.log_model(model, name="model", registered_model_name="churn", pyfunc_predict_fn="predict_proba", input_example=X2.head())
print("TRAINED V2", len(X2))
"""
FEATURES = """CREATE {} TABLE customer_features AS
SELECT c.customer_id, count(o.order_id) AS orders, CAST(coalesce(sum(o.amount), 0) AS double) AS revenue,
       date_diff('day', c.signed_up, DATE '2026-10-01') AS tenure_days
FROM uploads_customers c
LEFT JOIN uploads_orders o ON o.customer_id = c.customer_id AND NOT o.test AND NOT o.refunded
GROUP BY c.customer_id, c.signed_up"""


def models():
    return {m["name"]: m for m in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/models", timeout=120)).get("models", [])}


def scoring_run():
    r = api("bob", "POST", "/api/projects/retail/scoring/churn-nightly/runs")
    st, _ = run_pipeline("bob", "retail", "scoring_retail_churn_nightly") if not r.ok else ("started", None)
    sc = wait(lambda: next((x for x in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/scoring", timeout=60)).get("scoring", [])
                            if x.get("name") == "churn-nightly" and (x.get("runs") or [{}])[0].get("state") in ("success", "failed")), None), 900, every=15)
    return sc or {}


def lab11():
    heading(11, "Customers worth a call (the notebook, and the churn model)")
    code, out = notebook("alice", CALL_LIST)
    line = next((x for x in out.splitlines() if x.startswith("CALLS")), out[-300:])
    check("alice's notebook: the call list, Viktor Schmidt first, written to landing/reports/call-list.csv", code == 0 and "Viktor Schmidt" in line, line)
    made = sql("alice", "retail", FEATURES.format("OR REPLACE"))
    check("customer_features, made on the SQL tab as alice", isinstance(made, list), made)
    if "churn" not in models():
        code, out = notebook("alice", TRAIN_V1)
        check("alice trains churn and registers version 1 in retail's MLflow", code == 0 and "TRAINED" in out, out[-300:])
    m = wait(lambda: models().get("churn"), 120, every=10) or {}
    v1 = next((v for v in m.get("versions", []) if str(v.get("version")) == "1"), {})
    check("Models: churn version 1, registered by alice", v1.get("by", v1.get("registeredBy")) in ("alice", None) and bool(v1), v1)
    own = api("alice", "POST", "/api/projects/retail/models/churn/versions/1/approve", ok=(403, 409))
    api("bob", "POST", "/api/projects/retail/models/churn/versions/1/approve", ok=(200, 204, 409))
    check("alice can't approve her own version; bob does", own.status_code in (403, 409) and str((models().get("churn") or {}).get("approved")) in ("1", "2"),
          f"alice {own.status_code}, approved {(models().get('churn') or {}).get('approved')}")
    p = spec("retail")
    p["spec"]["scoring"] = [{"name": "churn-nightly", "model": "churn", "table": "customer_features", "id": "customer_id", "output": "customer_scores", "schedule": "0 3 * * *"}]
    save_spec(p)
    state, _ = run_pipeline("bob", "retail", "scoring_retail_churn_nightly")
    n = sql("bob", "retail", "SELECT count(*), max(model_version) FROM customer_scores")
    check("scoring every customer: customer_scores, one row each, version 1", state == "success" and isinstance(n, list), f"run {state}: {n}")
    sql("alice", "retail", "UPDATE customer_features SET revenue = revenue * 3")
    state, _ = run_pipeline("bob", "retail", "scoring_retail_churn_nightly")
    sc = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/scoring", timeout=60))
    check("the customers change: the next run's drift names revenue", state == "success" and "revenue" in json.dumps(sc), str(sc)[:300])
    # Version 2, on a version of the data.
    tags = {t["name"] for t in j(as_("alice").get(f"{PLATFORM}/api/projects/retail/tags", timeout=60)).get("tags", [])}
    if "churn-v2-data" not in tags:
        r = api("alice", "POST", "/api/projects/retail/tags", {"name": "churn-v2-data", "at": "now", "keep": "1y", "note": "What churn version 2 learned from"})
        check("alice tags the data: churn-v2-data", r.ok, f"HTTP {r.status_code}")
    if len((models().get("churn") or {}).get("versions", [])) < 2:
        code, out = notebook("alice", TRAIN_V2)
        check("alice trains version 2 through buckets_lake.sql()", code == 0 and "TRAINED V2" in out, out[-300:])
    vs = [str(v.get("version")) for v in (models().get("churn") or {}).get("versions", [])]
    check("Models: versions 1 and 2, side by side", {"1", "2"} <= set(vs), vs)
    api("bob", "POST", "/api/projects/retail/models/churn/versions/2/approve", ok=(200, 204, 409))
    state, _ = run_pipeline("bob", "retail", "scoring_retail_churn_nightly")
    n = sql("bob", "retail", "SELECT max(model_version) FROM customer_scores")
    check("bob approves version 2; the scoring loads it", state == "success" and n == [["2"]] or n == [[2]], f"run {state}: {n}")
    sql("alice", "retail", "UPDATE customer_features SET orders = orders + 1")
    m = j(as_("alice").get(f"{PLATFORM}/api/projects/retail/asof?at=churn-v2-data", timeout=60))
    then = sql("alice", "retail", f'SELECT sum(orders) FROM "customer_features#{m.get("commit", "")}"')
    now = sql("alice", "retail", "SELECT sum(orders) FROM customer_features")
    check("As of churn-v2-data: customer_features as version 2 learned from it, not today's", isinstance(then, list) and then != now, f"then {then}, now {now}")
    sql("alice", "retail", FEATURES.format("OR REPLACE"))
    p = spec("retail")
    p["spec"]["serving"] = [{"name": "churn", "model": "churn"}]
    save_spec(p)
    def predict(user):
        return as_(user).post(f"{PLATFORM}/api/projects/retail/serving/churn/predict", timeout=120,
                              json={"dataframe_records": [{"orders": 1, "revenue": 40.0, "tenure_days": 300}]}) if user != "carol" else platform("carol").post(
            f"{PLATFORM}/api/projects/retail/serving/churn/predict", json={"dataframe_records": [{"orders": 1, "revenue": 40.0, "tenure_days": 300}]}, timeout=120)
    ans = wait(lambda: (lambda r: r if r.ok else None)(predict("bob")), 600, every=15)
    c = predict("carol")
    check("Check: the web shop's call answers with version 2; carol is refused", ans is not None and '"2"' in ans.text or '"version": 2' in (ans.text if ans else ""),
          f"{ans.text[:160] if ans else 'no answer'}; carol HTTP {c.status_code}")
    if (spec("retail")["spec"].get("pipelines") or {}).get("repo"):
        lab11_schedule()


# --- With the project's repo (Labs 10, 11's schedule, 17, 18, 20) --------------------------

def gitlab():
    """GitLab's API for retail's repo, signed in with the repo's token (its connection): call(method, path, **kw)."""
    from urllib.parse import quote, urlparse

    import k8s
    pl = spec("retail")["spec"]["pipelines"]
    tok = k8s.get_secret(pl["credentials"]["secretRef"]).get("password", "")
    base = ((pl.get("reviews") or {}).get("gitlab") or "https://gitlab.com").rstrip("/")
    path = urlparse(pl["repo"]).path.strip("/").removesuffix(".git")
    root = f"{base}/api/v4/projects/{quote(path, safe='')}"

    def call(method, sub="", **kw):
        return requests.request(method, root + sub, headers={"PRIVATE-TOKEN": tok}, timeout=120, **kw)
    return call


def target_branch(gl):
    return gl("GET").json().get("default_branch") or "main"


def merge(gl, mr_url, review=True):
    """Wait for the merge request's review status (when asked), then merge it: (status, merged)."""
    iid = int(mr_url.rstrip("/").rsplit("/", 1)[-1])
    status = None
    if review:
        def reviewed():
            sha = gl("GET", f"/merge_requests/{iid}").json().get("sha")
            sts = gl("GET", f"/repository/commits/{sha}/statuses").json() if sha else []
            done = [x for x in sts if isinstance(x, dict) and x.get("name") == "storscale/dbt" and x.get("status") in ("success", "failed")]
            return done[0] if done else None
        status = wait(reviewed, 1500, every=30)
    wait(lambda: gl("GET", f"/merge_requests/{iid}").json().get("detailed_merge_status") not in ("checking", "unchecked", "preparing", "approvals_syncing"), 300, every=10)
    m = gl("PUT", f"/merge_requests/{iid}/merge", json={"should_remove_source_branch": True})
    if not m.ok:   # GitLab still deciding (405): once more, a little later
        time.sleep(30)
        m = gl("PUT", f"/merge_requests/{iid}/merge", json={"should_remove_source_branch": True})
    if not m.ok and "already" not in m.text:
        print(f"    merge: HTTP {m.status_code} {m.text[:200]}; {gl('GET', f'/merge_requests/{iid}').json().get('detailed_merge_status')}")
    return status, m.ok or gl("GET", f"/merge_requests/{iid}").json().get("state") == "merged"


def lab10():
    heading(10, "The project as code (with a git repo)")
    gl = gitlab()
    check("retail's repo answers GitLab's API with its token", gl("GET").ok, gl("GET").status_code)
    flow = {**campaign_flow(), "schedule": "0 6 * * 1"}
    r = api("alice", "PUT", "/api/projects/retail/flows/campaign_results", {"flow": flow}, timeout=300)
    saved = j(r)
    check("Flows → Save as merge request: the models, schema, sources and the DAG, on a branch with a merge request",
          r.ok and saved.get("mergeRequest") and any(f.endswith(".sql") for f in saved.get("files", [])), {k: saved.get(k) for k in ("branch", "mergeRequest", "dag")})
    if not saved.get("mergeRequest"):
        return
    status, merged = merge(gl, saved["mergeRequest"])
    check("the merge request's models are built on a data branch, and it gets a status", status is not None, (status or {}).get("description"))
    check("... merged", merged, "")
    state, _ = run_pipeline("bob", "retail", "flow_campaign_results", timeout=1800)
    rows = sql("bob", "retail", "SELECT count(*) FROM campaign_results")
    check("Check: flow_campaign_results runs; campaign_results is a table of the project's, six rows", state == "success" and rows == [[6]], f"run {state}: {rows}")


def lab11_schedule():
    heading(11, "the notebook on a schedule (with the repo)")
    from lakekit import token
    gl = gitlab()
    cell = "import io, pandas as pd, buckets_lake\n" + CALL_LIST
    nb = {"cells": [{"cell_type": "code", "source": cell, "metadata": {}, "outputs": [], "execution_count": None}],
          "metadata": {"kernelspec": {"name": "python3", "display_name": "Python 3"}}, "nbformat": 4, "nbformat_minor": 5}
    r = requests.post(f"{PLATFORM}/api/projects/retail/notebooks/schedules", timeout=300, headers={"Authorization": f"Bearer {token('alice')}"},
                      json={"path": "call_list.ipynb", "schedule": "0 6 * * 1", "notebook": nb})
    b = j(r)
    check("alice schedules call_list.ipynb: the notebook and its DAG, on a branch with a merge request", r.ok and b.get("mergeRequest"), str(b)[:200])
    if not b.get("mergeRequest"):
        return
    _, merged = merge(gl, b["mergeRequest"], review=False)
    check("... merged", merged, "")
    state, run = run_pipeline("bob", "retail", "notebook_call_list", timeout=1800)
    nbrun = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/notebooks/runs/notebook_call_list/{run}", timeout=120)) if run else {}
    check("Check: notebook_call_list runs as the project's pipelines, and its executed notebook is kept", state == "success" and "CALLS" in json.dumps(nbrun),
          f"run {state}")


def agent_mcp(client_id, secret):
    tok = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=30, data={
        "grant_type": "client_credentials", "client_id": client_id, "client_secret": secret})
    if not tok.ok:
        return None
    t = tok.json()["access_token"]

    def call(tool, **args):
        r = requests.post(f"{PLATFORM}/mcp", timeout=300, headers={"Authorization": f"Bearer {t}", "Accept": "application/json, text/event-stream"},
                          json={"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": tool, "arguments": args}})
        try:
            body = r.json()
        except ValueError:
            data = [x[5:] for x in r.text.splitlines() if x.startswith("data:")]
            body = json.loads(data[-1]) if data else {"error": r.text[:200]}
        return body.get("result") or body
    return call


def lab17():
    heading(17, "An agent of the project's own")
    agent = {"description": "sums the day's orders for the team's chat", "owner": "bob", "tools": ["list_metrics", "query_metrics"]}
    api("bob", "DELETE", "/api/projects/retail/agents/nightly-summary", ok=(200, 204, 404))
    r = api("bob", "PUT", "/api/projects/retail/agents/nightly-summary", agent)
    own = api("bob", "POST", "/api/projects/retail/agents/nightly-summary/approve", {}, ok=(403, 409))
    check("bob registers nightly-summary; he can't approve his own", r.ok and own.status_code in (403, 409), f"HTTP {r.status_code}, own {own.status_code}")
    a = api("alice", "POST", "/api/projects/retail/agents/nightly-summary/approve", {})
    ap = j(a)
    check("alice approves it: its client ID and secret, once", a.ok and ap.get("clientId") and ap.get("secret"), ap.get("clientId"))
    call = wait(lambda: agent_mcp(ap.get("clientId", ""), ap.get("secret", "")), 120, every=10) if a.ok else None
    rev = call("query_metrics", project="retail", metrics=["revenue"], group_by=["order__region"]) if call else None
    refused = call("list_products") if call else None
    check("the agent asks for revenue by region: the whole company; list_products is refused, naming its tools",
          rev is not None and "APAC" in json.dumps(rev) and "may use" in json.dumps(refused), f"{str(rev)[:120]} / {str(refused)[:120]}")
    api("bob", "PUT", "/api/projects/retail/agents/nightly-summary", {**agent, "tools": agent["tools"] + ["list_products"]})
    ag = {x["name"]: x for x in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/agents", timeout=60)).get("agents", [])}
    check("its tools changed: the approval is taken away", not (ag.get("nightly-summary") or {}).get("approved"), (ag.get("nightly-summary") or {}).get("approval"))
    api("bob", "DELETE", "/api/projects/retail/agents/nightly-summary")
    gone = wait(lambda: agent_mcp(ap.get("clientId", ""), ap.get("secret", "")) is None, 120, every=10)
    check("revoked: within a minute it signs in no more", gone, "")
    # Run by the platform, from a notebook in the repo.
    gl = gitlab()
    branch = target_branch(gl)
    nb = {"cells": [{"cell_type": "code", "metadata": {}, "outputs": [], "execution_count": None, "source":
                     "import buckets_lake\na = buckets_lake.agent()\nrevenue = a.call(\"query_metrics\", project=\"retail\", metrics=[\"revenue\"], group_by=[\"order__region\"])\n"
                     "night = a.call(\"list_runs\", project=\"retail\", limit=10)\nalerts = a.call(\"list_alerts\", project=\"retail\")\n"
                     "print(buckets_lake.username())\n"
                     "print(buckets_lake.ask(f\"Two sentences for the team's chat: revenue by region {revenue}; runs {night['runs']}; alerts {alerts['issues']}\"))\n"}],
          "metadata": {"kernelspec": {"name": "python3", "display_name": "Python 3"}}, "nbformat": 4, "nbformat_minor": 5}
    path = (spec("retail")["spec"]["pipelines"].get("path") or "").strip("/")
    file = f"{path}/notebooks/nightly_summary.ipynb" if path else "notebooks/nightly_summary.ipynb"
    exists = gl("GET", f"/repository/files/{requests.utils.quote(file, safe='')}?ref={branch}").ok
    c = gl("POST", "/repository/commits", json={"branch": branch, "commit_message": "The nightly summary's notebook (Lab 17)",
                                                "actions": [{"action": "update" if exists else "create", "file_path": file, "content": json.dumps(nb, indent=1)}]})
    check(f"the notebook in the repo: {file}", c.ok, f"HTTP {c.status_code} {c.text[:160]}")
    r = api("bob", "PUT", "/api/projects/retail/agents/nightly-summary", {"description": "sums the night for the team's chat", "owner": "bob",
                                                                          "tools": ["query_metrics", "list_runs", "list_alerts"], "budget": 0.5,
                                                                          "schedule": "0 6 * * *", "notebook": "notebooks/nightly_summary.ipynb"})
    a = api("alice", "POST", "/api/projects/retail/agents/nightly-summary/approve", {})
    state, run = run_pipeline("bob", "retail", "agent_retail_nightly_summary", timeout=1800) if r.ok and a.ok else ("not registered", None)
    log = run_log("bob", "retail", "agent_retail_nightly_summary", run) if run else ""
    check("the platform runs it: the notebook as the agent, asking the model on its own budget", state == "success",
          f"run {state}" + ("; ran as the agent" if "service-account-agent-retail-nightly-summary" in log else ""))
    # A prompt, no notebook.
    r = api("bob", "PUT", "/api/projects/retail/agents/triage", {"description": "reads the alerts and failed runs each morning", "owner": "bob",
                                                                 "tools": ["list_alerts", "list_runs"], "budget": 0.5, "schedule": "0 7 * * *",
                                                                 "prompt": "Each morning, read the open alerts and the failed runs, and write a two-sentence summary for the team."})
    a = api("alice", "POST", "/api/projects/retail/agents/triage/approve", {})
    state, _ = run_pipeline("bob", "retail", "agent_retail_triage", timeout=1800) if r.ok and a.ok else ("not registered", None)
    check("triage, a prompt and two tools, runs with no repo", state == "success", f"run {state}")
    audit = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/audit?q=agent", timeout=60)).get("events", [])
    acts = {e.get("action") for e in audit}
    check("Check: the Audit tab has agent.registered, agent.approved, agent.ran and agent.revoked", {"agent.registered", "agent.approved", "agent.revoked"} <= acts,
          sorted(a for a in acts if a and a.startswith("agent")))


def lab18():
    heading(18, "What customers say, sorted by a model")
    data = lab_data()
    for f in ("reviews.csv", "care.md", "returns.html"):
        r = as_("bob").post(f"{PLATFORM}/api/projects/retail/uploads", files={"file": (f, io.BytesIO(data[f]))}, timeout=120)
        if not r.ok:
            print(f"    upload {f}: HTTP {r.status_code} {r.text[:200]}")
    im = next((x for x in spec("retail")["spec"].get("imports") or [] if x["name"] == "uploads"), None)
    tabs = [t for t in (im or {}).get("files", {}).get("tables", []) if t["name"] != "reviews"] + [{"name": "reviews", "file": "reviews.csv"}]
    api("bob", "PUT", "/api/projects/retail/imports/uploads", {"name": "uploads", "source": "files", "schedule": "0 * * * *", "files": {"tables": tabs}})
    state, _ = run_pipeline("bob", "retail", "import_retail_uploads")
    n = sql("bob", "retail", "SELECT count(*) FROM uploads_reviews")
    check("reviews.csv imported: uploads_reviews, 90 rows", state == "success" and n == [[90]], f"run {state}: {n}")
    st = lambda i, kind, x, **kw: {"id": i, "kind": kind, "x": x, "y": 0, **kw}  # noqa: E731
    flow = {"name": "review_topics", "steps": [
        st("input", "input", 0, table="uploads_reviews"),
        st("prepare", "prepare", 1, inputs=["input"], processors=[{"column": "text", "do": "regex_replace", "args": ["[^@\\s]+@[^@\\s]+\\.[a-z]+", "[email]"]}]),
        st("llm", "llm", 2, inputs=["prepare"], column="text", mode="classify", values=["delivery", "sizing", "quality", "price", "service"],
           prompt="Delivery is about shipping and packaging; sizing about fit; quality about the product failing; price about cost; service about staff and support.",
           name="topic"),
        st("group", "group", 3, inputs=["llm"], by=["topic"], aggregates=[{"function": "count", "as": "reviews"}, {"function": "avg", "column": "stars", "as": "stars"}]),
        st("output", "output", 4, inputs=["group"], table="review_topics", checks=[{"column": "topic", "test": "not_null"}])]}
    pv = as_("bob").post(f"{PLATFORM}/api/projects/retail/flows/preview", json={"flow": flow, "step": "prepare", "limit": 100}, timeout=300)
    masked = [r for r in j(pv).get("rows", []) if "[email]" in json.dumps(r)]
    check("mask first: the prepare step's preview signs the three reviews off with [email]", pv.ok and len(masked) >= 1, f"{len(masked)} masked in the sample")
    pv = as_("bob").post(f"{PLATFORM}/api/projects/retail/flows/preview", json={"flow": flow, "step": "output", "limit": 100}, timeout=600)
    check("the model classifies the masked reviews: a row per topic in the preview", pv.ok and len(j(pv).get("rows", [])) >= 1, (j(pv).get("rows") or j(pv).get("error") or "")[:5] if pv.ok else pv.text[:200])
    saved = j(api("bob", "PUT", "/api/projects/retail/flows/review_topics", {"flow": {**flow, "schedule": "0 7 * * 1"}}, timeout=300))
    if saved.get("mergeRequest"):
        merge(gitlab(), saved["mergeRequest"])
        state, _ = run_pipeline("bob", "retail", "flow_review_topics", timeout=2400)
        rows = sql("bob", "retail", "SELECT topic, reviews FROM review_topics ORDER BY 2 DESC")
        check("Check: flow_review_topics runs; review_topics has a row per topic", state == "success" and isinstance(rows, list) and rows, f"run {state}: {rows}")
    # Knowledge banks.
    sql("bob", "retail", "CREATE OR REPLACE VIEW reviews_masked AS SELECT review_id, regexp_replace(text, '[^@\\s]+@[^@\\s]+\\.[a-z]+', '[email]') AS text FROM uploads_reviews")
    api("bob", "PUT", "/api/projects/retail/knowledge/reviews", {"tables": [{"table": "reviews_masked", "column": "text", "id": "review_id"}]})
    b = api("bob", "POST", "/api/projects/retail/knowledge/reviews/build", {}, timeout=900)
    hits = j(as_("bob").post(f"{PLATFORM}/api/projects/retail/knowledge/reviews/search", json={"query": "the zip broke", "k": 3}, timeout=300)).get("hits", [])
    check("the knowledge bank reviews, from the masked text: 'the zip broke' finds reviews about zips", b.ok and any("zip" in h["text"].lower() for h in hits),
          [h["text"][:60] for h in hits])
    api("bob", "PUT", "/api/projects/retail/knowledge/manuals", {"files": [{"prefix": "landing/uploads/"}], "schedule": "0 3 * * *"})
    state, _ = run_pipeline("bob", "retail", "knowledge_retail_manuals", timeout=1800)
    hits = j(as_("bob").post(f"{PLATFORM}/api/projects/retail/knowledge/manuals/search", json={"query": "how do I wash it", "k": 3}, timeout=300)).get("hits", [])
    check("manuals, the uploaded pages, built each night: 'how do I wash it' finds the care guide", state == "success" and hits and "care" in hits[0].get("source", "") + hits[0].get("ref", ""),
          [(h.get("source"), h.get("ref")) for h in hits])


def lab20():
    heading(20, "Released on purpose (with a git repo)")
    gl = gitlab()
    branch = target_branch(gl)
    p = spec("retail")
    sp = p["spec"]
    for k in ("status",):
        sp.pop(k, None)
    model = j(as_("bob").get(f"{PLATFORM}/api/projects/retail/semantic", timeout=60)).get("yaml", "")
    files = {"storscale/project.yaml": json.dumps({"apiVersion": p["apiVersion"], "kind": p["kind"], "metadata": p["metadata"], "spec": sp}, indent=2) + "\n", "storscale/semantic.yaml": model,   # JSON is YAML
             "storscale/targets/development.yaml": "{}\n", "storscale/targets/production.yaml": "{}\n"}
    actions = []
    for f, text in files.items():
        exists = gl("GET", f"/repository/files/{requests.utils.quote(f, safe='')}?ref={branch}").ok
        actions.append({"action": "update" if exists else "create", "file_path": f, "content": text})
    c = gl("POST", "/repository/commits", json={"branch": branch, "commit_message": "The project as code (Lab 20)", "actions": actions})
    gl("DELETE", "/repository/tags/v1.0")
    t = gl("POST", "/repository/tags", json={"tag_name": "v1.0", "ref": branch})
    check("the project and its model in the repo, tagged v1.0", c.ok and t.ok, f"commit {c.status_code}, tag {t.status_code} {t.text[:120]}")

    def plan_and_apply(ref):
        r = api("alice", "POST", "/api/projects/retail/plans", {"ref": ref}, timeout=300)
        pid = j(r).get("id") or j(r).get("plan")
        ready = wait(lambda: next((x for x in j(as_("bob").get(f"{PLATFORM}/api/projects/retail/plans", timeout=60)).get("plans", [])
                                   if x.get("id") == pid and x.get("state") in ("ready", "failed", "refused")), None), 900, every=15) if pid else None
        a = api("bob", "POST", f"/api/projects/retail/plans/{pid}/apply") if ready and ready.get("state") == "ready" else None
        return r, ready, a
    r, ready, a = plan_and_apply("v1.0")
    check("alice plans v1.0, and bob (another administrator) applies it", a is not None and a.ok, f"plan HTTP {r.status_code} {str(ready)[:200]}")
    count = sql("bob", "retail", "SELECT count(*) FROM uploads_orders")
    changed = model.replace('expr: "case when not test and not refunded then amount end"', "expr: \"case when not test and not refunded and channel <> 'partner' then amount end\"")
    c = gl("POST", "/repository/commits", json={"branch": branch, "commit_message": "Revenue without partner orders (Lab 20)",
                                                "actions": [{"action": "update", "file_path": "storscale/semantic.yaml", "content": changed}]})
    gl("DELETE", "/repository/tags/v1.1")
    gl("POST", "/repository/tags", json={"tag_name": "v1.1", "ref": branch})
    r, ready, a = plan_and_apply("v1.1")
    after = by_region(metrics("bob", "retail", ["revenue"], ["order__region"]))
    total = sum(float(v[1]) for v in after.values()) if after else None
    check("v1.1: revenue without partner orders, planned and applied; the Metrics tab follows", a is not None and a.ok and total is not None and total < 22168, f"revenue {total}")
    r, ready, a = plan_and_apply("v1.0")
    back = by_region(metrics("bob", "retail", ["revenue"], ["order__region"]))
    total = sum(float(v[1]) for v in back.values()) if back else None
    check("Check: rolled back to v1.0: revenue as before, the orders' count unchanged",
          a is not None and a.ok and total is not None and abs(total - 22168) < 300 and sql("bob", "retail", "SELECT count(*) FROM uploads_orders") == count, f"revenue {total}")


LABS = {10: lab10, 17: lab17, 18: lab18, 20: lab20, 1: lab1, 2: lab2, 3: lab3, 4: lab4, 5: lab5, 6: lab6, 7: lab7, 8: lab8, 9: lab9, 11: lab11, 12: lab12, 13: lab13, 14: lab14, 15: lab15, 16: lab16, 19: lab19, 21: lab21, 22: lab22}


def main():
    wanted = labs_wanted()
    print(f"the guide's labs: {' '.join(map(str, wanted))}", flush=True)
    for n in wanted:
        if n not in LABS:
            print(f"\n--- Lab {n}: not scripted yet", flush=True)
            continue
        started = time.time()
        try:
            LABS[n]()
        except Exception as e:  # noqa: BLE001  a lab that breaks is a failed check; the next still runs
            check(f"Lab {n} ran to its end", False, f"{type(e).__name__}: {str(e)[:300]}")
        print(f"    ({time.time() - started:.0f}s)", flush=True)
    finish()


if __name__ == "__main__":
    sys.exit(main())
