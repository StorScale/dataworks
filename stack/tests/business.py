"""The company's terms, end to end: docker compose run --rm test business

The suite makes two projects: company, whose semantic model is shared (the
company's: customers and bookings, with terms that say what they mean and who
owns them) and whose tables are its data products, and campaigns, whose model
builds on it (semantic.uses) with a metric of its own. alice is a reader of
campaigns only: company's tables are hers through its products. Its terms
are the platform's, so their names and synonyms are its own (account_holder,
not the lab guide's customer): it runs beside the guide's retail project.
These check (use case 10.1):

  1. the terms are the platform's: Account holder and Bookings with their
     definitions, owners and synonyms, where they come from, and campaigns
     among the projects using them;
  2. alice (a reader of both) asks campaigns' own metric, bookings per
     customer, by the company's customer segment, and gets it, under her rules;
  3. campaigns can't define the company's metric again;
  4. a change to the company's model that would break campaigns' is refused,
     naming campaigns;
  5. the catalog's glossary has the terms, and the company's tables are
     tagged with them;
  6. Superset has the company's models as datasets, their metrics defined
     (10.2): alice's chart of bookings by customer segment there agrees with
     the semantic layer's answer, both under her rules;
  7. data products (10.3): company publishes customers and bookings; alice,
     a reader of campaigns and not of company, reads neither until campaigns
     asks and company approves, then both in place, under company's rules
     (its readers' row filter on bookings holds for her: the numbers above
     are hers); certified, bookings is certified in the catalog too, and a
     change of its contract takes the certification away; a contracted
     column gone is company's alert, naming campaigns;
  8. an assistant (10.4): over MCP, alice's finds the terms a question is
     about (by a synonym), is told when none covers one, finds bookings
     certified, and its answers cite the metric, terms and product used;
     it reads the product in place under company's rules; carol's sees the
     product and is refused its rows; bob registers an assistant (a Keycloak
     client, made by the operator), a person signs in through it, and
     removed, it signs no one in.

The projects and their tables are removed after.
"""
import json
import os

import catalog as om
import requests
import superset as ss
from semantic import TOKENS, Agent, agent_token

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
H = {"X-Platform-Request": "1"}
COMPANY, CAMPAIGNS = "company", "campaigns"

COMPANY_MODEL = """semantic_model:
  name: customers
  description: Every customer, one row each.
  node_relation: {database: iceberg, schema_name: company, alias: customers}
  defaults: {agg_time_dimension: signed_up}
  entities:
    - {name: account_holder, type: primary, expr: customer_id}
  dimensions:
    - {name: segment, type: categorical}
    - {name: signed_up, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: customer_ids, expr: customer_id, agg: count_distinct}
---
semantic_model:
  name: bookings
  description: Every booking, one row each.
  node_relation: {database: iceberg, schema_name: company, alias: bookings}
  defaults: {agg_time_dimension: booked_on}
  entities:
    - {name: booking, type: primary, expr: booking_id}
    - {name: account_holder, type: foreign, expr: customer_id}
  dimensions:
    - {name: booked_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: booking_amount, expr: amount, agg: sum}
---
metric:
  name: customer_count
  description: How many customers.
  type: simple
  type_params: {measure: customer_ids}
---
metric:
  name: bookings_total
  description: What customers booked.
  type: simple
  type_params: {measure: booking_amount}
---
terms:
  - name: account_holder
    label: Account holder
    owner: bob
    definition: A company or person who has signed a contract with us, active or not.
    synonyms: [subscriber, contract holder]
  - name: bookings_total
    label: Bookings
    owner: bob
    definition: The total value of signed orders, before cancellations, in US dollars.
"""

CAMPAIGNS_MODEL = """metric:
  name: bookings_per_customer
  description: What each customer booked, on average.
  type: ratio
  type_params: {numerator: {name: bookings_total}, denominator: {name: customer_count}}
"""


def project(name, extra=None, tables=None, alice=True):
    members = [{"group": "engineers", "role": "editor"}] + ([{"user": "alice", "role": "reader"}] if alice else [])
    return {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": name},
            "spec": {"description": f"The business suite's {name}.", "members": members, "tables": tables or {}, **(extra or {})}}


PRODUCTS = [{"name": "customers", "table": "customers", "owner": "bob", "version": "1", "terms": ["account_holder"],
             "description": "Every customer, one row each.", "contract": {"columns": ["customer_id", "segment"], "within": "30d"}},
            {"name": "bookings", "table": "bookings", "owner": "bob", "version": "1", "terms": ["bookings_total"],
             "description": "Every booking, one row each.", "contract": {"columns": ["booking_id", "customer_id", "amount"]}}]
COMPANY_SPEC = project(COMPANY, {"semantic": {"shared": True}, "products": PRODUCTS},
                       tables={"readers": {"rowFilters": [{"table": "bookings", "filter": "amount < 2000"}]}}, alice=False)


def can_read(user, table):
    try:
        return [list(r) for r in sql(user, f"SELECT * FROM {table} ORDER BY 1", "iceberg", COMPANY)]
    except Exception as e:  # noqa: BLE001
        return str(e)[:160]


def token_or_none(user, client):
    """A token from a client, or None when Keycloak won't give one (the client's gone)."""
    r = requests.post(TOKENS, timeout=10, data={"grant_type": "password", "client_id": client, "username": user,
                                                 "password": os.environ["LAKEHOUSE_USER_PASSWORD"]})
    return r.json().get("access_token") if r.ok else None


def products(s):
    r = s.get(f"{PLATFORM}/api/products", timeout=30)
    return {f"{p['project']}/{p['name']}": p for p in (r.json().get("products", []) if r.ok else [])}


def put_model(s, name, text):
    return s.put(f"{PLATFORM}/api/projects/{name}/semantic", json={"yaml": text}, headers=H, timeout=120)


def main():
    bob, alice = platform("bob"), platform("alice")
    ready = lambda name: wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
        (projects(bob) or {}).get(name)), 600, every=10)
    try:
        r1 = bob.put(f"{PLATFORM}/api/projects/{COMPANY}", data=json.dumps(COMPANY_SPEC), timeout=30)
        r2 = bob.put(f"{PLATFORM}/api/projects/{CAMPAIGNS}", data=json.dumps(project(CAMPAIGNS, {"semantic": {"uses": [COMPANY]}})), timeout=30)
        ok = r1.ok and r2.ok and ready(COMPANY) and ready(CAMPAIGNS)
        sql("bob", "CREATE TABLE customers AS SELECT * FROM (VALUES (1, 'enterprise', DATE '2026-01-05'), (2, 'enterprise', DATE '2026-02-01'), "
                   "(3, 'small business', DATE '2026-03-09')) AS t (customer_id, segment, signed_up)", "iceberg", COMPANY)
        sql("bob", "CREATE TABLE bookings AS SELECT * FROM (VALUES (10, 1, 1000.0, DATE '2026-09-01'), (11, 2, 3000.0, DATE '2026-09-02'), "
                   "(12, 3, 500.0, DATE '2026-09-03')) AS t (booking_id, customer_id, amount, booked_on)", "iceberg", COMPANY)
        m1, m2 = put_model(bob, COMPANY, COMPANY_MODEL), put_model(bob, CAMPAIGNS, CAMPAIGNS_MODEL)
        check("bob makes the company's shared model, and campaigns' built on it",
              ok and m1.ok and m2.ok and COMPANY in m2.json().get("builtOn", []), (r1.text[:150], m1.text[:200], m2.text[:200]))

        # The products, read in place (7, first: the rest reads company's tables through them).
        before = can_read("alice", "bookings")
        asks = [bob.post(f"{PLATFORM}/api/products/{COMPANY}/{p}/requests", headers=H, timeout=30,
                         json={"consumer": CAMPAIGNS, "why": "campaign targeting"}) for p in ("customers", "bookings")]
        waiting = products(bob).get(f"{COMPANY}/bookings", {}).get("requests", [])
        answers = [bob.post(f"{PLATFORM}/api/products/{COMPANY}/{p}/requests/{CAMPAIGNS}/approve", headers=H, timeout=30)
                   for p in ("customers", "bookings")]
        after = wait(lambda: (lambda x: x if isinstance(x, list) else None)(can_read("alice", "bookings")), 240, every=10) or can_read("alice", "bookings")
        check("alice (campaigns' reader) can't read company's bookings until campaigns asks for the product and company approves",
              isinstance(before, str) and all(a.ok for a in asks + answers) and [w["consumer"] for w in waiting] == [CAMPAIGNS],
              (before, [a.status_code for a in asks + answers], waiting))
        check("... then reads it in place, under company's rules: its readers' row filter holds for her (no 3000)",
              isinstance(after, list) and [r[0] for r in after] == [10, 12], after)

        # 1. The terms.
        r = alice.get(f"{PLATFORM}/api/terms", timeout=60)
        terms = {t["name"]: t for t in (r.json().get("terms", []) if r.ok else [])}
        c, b = terms.get("account_holder", {}), terms.get("bookings_total", {})
        check("the platform's terms: Account holder and Bookings, defined, owned, and where they come from",
              c.get("label") == "Account holder" and c.get("owner") == "bob" and "subscriber" in c.get("synonyms", [])
              and "iceberg.company.customers" in c.get("tables", []) and b.get("label") == "Bookings"
              and b.get("tables") == ["iceberg.company.bookings"] and c.get("kind") == "entity", (r.status_code, c, b))
        check("... with the metrics on Account holder, and campaigns among the projects using them",
              "customer_count" in c.get("metrics", []) and CAMPAIGNS in b.get("usedBy", []), (c.get("metrics"), b.get("usedBy")))

        # 2. alice asks campaigns' own metric by the company's segment.
        r = alice.post(f"{PLATFORM}/api/projects/{CAMPAIGNS}/semantic/query", headers=H, timeout=120,
                       json={"metrics": ["bookings_per_customer"], "group_by": ["account_holder__segment"], "order_by": ["account_holder__segment"]})
        rows = r.json().get("rows") if r.ok else r.text[:300]
        check("alice asks campaigns' bookings per customer by the company's segment, and gets it",
              r.ok and [[x[0], round(float(x[1]))] for x in rows] == [["enterprise", 500], ["small business", 500]], rows)

        # 3. Not defined again.
        again = CAMPAIGNS_MODEL + "---\nmetric:\n  name: bookings_total\n  type: simple\n  type_params: {measure: booking_amount}\n"
        r = put_model(bob, CAMPAIGNS, again)
        check("campaigns can't define the company's Bookings again", r.status_code == 422 and "company" in r.text, r.text[:300])

        # 4. A change that would break campaigns.
        without = COMPANY_MODEL.replace("---\nmetric:\n  name: customer_count\n  description: How many customers.\n  type: simple\n  type_params: {measure: customer_ids}\n", "")
        r = put_model(bob, COMPANY, without)
        check("a change to the company's model that would break campaigns' is refused, naming it",
              r.status_code == 422 and CAMPAIGNS in r.text, r.text[:300])

        # 5. The catalog's glossary.
        s = om.catalog()

        def tagged():
            t = s.get(f"{om.OM}/tables/name/trino.iceberg.company.customers", params={"fields": "tags"}, timeout=30)
            return t.ok and any(x.get("tagFQN") == "BusinessTerms.account_holder" for x in t.json().get("tags", []))
        def terms():
            glossary = s.get(f"{om.OM}/glossaries/name/BusinessTerms", timeout=30)
            gt = s.get(f"{om.OM}/glossaryTerms", params={"glossary": glossary.json().get("id") if glossary.ok else "", "limit": 100}, timeout=30)
            return sorted(t["name"] for t in (gt.json().get("data", []) if gt.ok else []))
        # Both come with catalog-sync's round (minutes apart, with every project's columns sampled).
        found = wait(lambda: tagged() and {"account_holder", "bookings_total"} <= set(terms()), 900, every=15)
        names = terms()
        check("the catalog's glossary has the terms, and the company's customers table is tagged Account holder",
              {"account_holder", "bookings_total"} <= set(names) and found, (names, found))

        # 6. Superset's datasets.
        sup = ss.sign_in("alice")

        def dataset():
            r = sup.get(f"{ss.SS}/api/v1/dataset/", params={"q": "(page_size:100)"}, timeout=30)
            return next((d for d in (r.json().get("result", []) if r.ok else []) if d["table_name"] == "bookings (semantic)"), None)
        d = wait(dataset, 300, every=10)
        r = sup.post(f"{ss.SS}/api/v1/chart/data", headers=ss.headers(sup), timeout=120, json={
            "datasource": {"id": d["id"], "type": "table"} if d else {}, "result_format": "json", "result_type": "full",
            "queries": [{"metrics": ["bookings_total"], "columns": ["account_holder__segment"], "orderby": [["account_holder__segment", True]], "row_limit": 100}]})
        chart = [[x["account_holder__segment"], round(float(x["bookings_total"]))] for x in r.json()["result"][0]["data"]] if r.ok and d else r.text[:300]
        sem = alice.post(f"{PLATFORM}/api/projects/{CAMPAIGNS}/semantic/query", headers=H, timeout=120,
                         json={"metrics": ["bookings_total"], "group_by": ["account_holder__segment"], "order_by": ["account_holder__segment"]})
        answer = [[x[0], round(float(x[1]))] for x in sem.json().get("rows", [])] if sem.ok else sem.text[:300]
        check("Superset has the company's bookings as a dataset, and alice's chart there agrees with the semantic layer",
              d is not None and chart == answer == [["enterprise", 1000], ["small business", 500]], (chart, answer))

        # 7. Certified; the catalog; a change takes it away; a broken contract.
        r = bob.post(f"{PLATFORM}/api/products/{COMPANY}/bookings/certification", headers=H, timeout=30)
        listed = list(products(alice))
        check("bob (an administrator) certifies bookings, which lists first", r.ok and products(alice).get(f"{COMPANY}/bookings", {}).get("certified")
              and listed[0] == f"{COMPANY}/bookings", (r.text[:200], listed))

        # 10.4: an assistant on the business layer, through the MCP server.
        ag = Agent(agent_token("alice"))
        found, _ = ag.call("search_terms", question="Which subscribers' bookings fell most this quarter?")
        names = {t["name"] for t in (found or {}).get("terms", [])}
        check("alice's assistant finds Account holder (by a synonym) and Bookings among the company's terms", names == {"account_holder", "bookings_total"}, found)
        none, _ = ag.call("search_terms", question="What was the weather in Paris?")
        check("... and is told plainly when no governed definition covers a question",
              none is not None and none.get("terms") == [] and "No governed definition" in none.get("note", ""), none)
        pr, _ = ag.call("describe_product", project=COMPANY, product="bookings")
        res, e = ag.call("query_metrics", project=CAMPAIGNS, metrics=["bookings_per_customer"], group_by=["account_holder__segment"], order_by=["account_holder__segment"])
        used = (res or {}).get("used", {})
        check("it finds bookings certified, and its answer cites the metric, the terms and the product it used",
              (pr or {}).get("certified") is True and e is None and [[x[0], round(float(x[1]))] for x in res["rows"]] == [["enterprise", 500], ["small business", 500]]
              and [m["name"] for m in used.get("metrics", [])] == ["bookings_per_customer"] and {t["name"] for t in used.get("terms", [])} >= {"account_holder", "bookings_total"}
              and any(p["name"] == "bookings" and p["certified"] for p in used.get("products", [])), (pr, e, used))
        rows, e = ag.call("query_product", project=COMPANY, product="bookings", columns=["booking_id", "amount"])
        check("alice's assistant reads the product in place, under company's rules (no 3000)", e is None and [r[0] for r in rows["rows"]] == [10, 12], (rows, e))
        carol = Agent(agent_token("carol"))
        listed, _ = carol.call("list_products")
        _, refused = carol.call("query_product", project=COMPANY, product="bookings")
        check("carol's assistant sees the product exists, and is refused its rows, told where her project would ask",
              bool(listed) and any(p["name"] == "bookings" for p in listed["products"]) and bool(refused) and "ask for it" in refused, (listed, refused))
        r = bob.put(f"{PLATFORM}/api/assistants/devassist", headers=H, timeout=30,
                    json={"description": "the company's assistant", "redirectUris": ["http://localhost:*"]})
        tok = wait(lambda: token_or_none("alice", "assistant-devassist"), 240, every=10)
        got, e = Agent(tok).call("list_terms") if tok else (None, "no token")
        check("bob registers an assistant: it gets a Keycloak client, and a person signed in through it reaches the MCP server",
              r.ok and r.json().get("clientId") == "assistant-devassist" and bool(tok) and e is None and bool(got), (r.text[:200], e))
        r = bob.delete(f"{PLATFORM}/api/assistants/devassist", headers=H, timeout=30)
        gone = wait(lambda: token_or_none("alice", "assistant-devassist") is None or None, 240, every=10)
        check("... and removed, it signs no one in any more", r.status_code == 204 and bool(gone), r.status_code)

        def in_catalog():
            dp = s.get(f"{om.OM}/dataProducts/name/{COMPANY}__bookings", timeout=30)
            t = s.get(f"{om.OM}/tables/name/trino.iceberg.company.bookings", params={"fields": "certification"}, timeout=30)
            gold = t.ok and ((t.json().get("certification") or {}).get("tagLabel") or {}).get("tagFQN") == "Certification.Gold"
            return dp.ok and gold
        check("the catalog has the data product, its table certified Gold", wait(in_catalog, 900, every=15))
        v2 = json.loads(json.dumps(COMPANY_SPEC))
        v2["spec"]["products"][1]["contract"]["columns"] = ["booking_id", "amount"]
        r = bob.put(f"{PLATFORM}/api/projects/{COMPANY}", data=json.dumps(v2), timeout=30)
        p2 = products(alice).get(f"{COMPANY}/bookings", {})
        check("a change of its contract takes the certification away, and keeps its consumers",
              r.ok and not p2.get("certified") and p2.get("consumers") == [CAMPAIGNS], p2)
        sql("bob", "ALTER TABLE customers RENAME COLUMN segment TO tier", "iceberg", COMPANY)
        issue = wait(lambda: next((i for i in bob.get(f"{PLATFORM}/api/projects/{COMPANY}/alerts", timeout=30).json().get("issues", [])
                                   if i["key"] == "product/customers/columns"), None), 240, every=10)
        check("a contracted column gone (segment) is company's alert, naming campaigns",
              issue is not None and "segment" in issue.get("title", "") and CAMPAIGNS in issue.get("detail", ""), issue)
    finally:
        for name, tables in ((COMPANY, ("customers", "bookings")), (CAMPAIGNS, ())):
            for t in tables:
                try:
                    sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", name)
                except Exception as e:  # noqa: BLE001
                    print(f"  (dropping {t}: {e})")
            bob.delete(f"{PLATFORM}/api/projects/{name}", headers=H, timeout=30)
        wait(lambda: not {COMPANY, CAMPAIGNS} & set(projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
