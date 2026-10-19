"""The company's terms, end to end: docker compose run --rm test business

The suite makes two projects: company, whose semantic model is shared (the
company's: customers and bookings, with terms that say what they mean and who
owns them), and campaigns, whose model builds on it (semantic.uses) with a
metric of its own.
These check (use case 10.1):

  1. the terms are the platform's: Customer and Bookings with their
     definitions, owners and synonyms, where they come from, and campaigns
     among the projects using them;
  2. alice (a reader of both) asks campaigns' own metric, bookings per
     customer, by the company's customer segment, and gets it, under her rules;
  3. campaigns can't define the company's metric again;
  4. a change to the company's model that would break campaigns' is refused,
     naming campaigns;
  5. the catalog's glossary has the terms, and the company's tables are
     tagged with them.

The projects and their tables are removed after.
"""
import json
import os

from lakekit import check, finish, sql

import catalog as om
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
    - {name: customer, type: primary, expr: customer_id}
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
    - {name: customer, type: foreign, expr: customer_id}
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
  - name: customer
    label: Customer
    owner: bob
    definition: A company or person who has signed a contract with us, active or not.
    synonyms: [client, account]
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


def project(name, extra=None):
    return {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": name},
            "spec": {"description": f"The business suite's {name}.", "members": [{"group": "engineers", "role": "editor"},
                     {"user": "alice", "role": "reader"}], "tables": {}, **(extra or {})}}


def put_model(s, name, text):
    return s.put(f"{PLATFORM}/api/projects/{name}/semantic", json={"yaml": text}, headers=H, timeout=120)


def main():
    bob, alice = platform("bob"), platform("alice")
    ready = lambda name: wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)(
        (projects(bob) or {}).get(name)), 600, every=10)
    try:
        r1 = bob.put(f"{PLATFORM}/api/projects/{COMPANY}", data=json.dumps(project(COMPANY, {"semantic": {"shared": True}})), timeout=30)
        r2 = bob.put(f"{PLATFORM}/api/projects/{CAMPAIGNS}", data=json.dumps(project(CAMPAIGNS, {"semantic": {"uses": [COMPANY]}})), timeout=30)
        ok = r1.ok and r2.ok and ready(COMPANY) and ready(CAMPAIGNS)
        sql("bob", "CREATE TABLE customers AS SELECT * FROM (VALUES (1, 'enterprise', DATE '2026-01-05'), (2, 'enterprise', DATE '2026-02-01'), "
                   "(3, 'small business', DATE '2026-03-09')) AS t (customer_id, segment, signed_up)", "iceberg", COMPANY)
        sql("bob", "CREATE TABLE bookings AS SELECT * FROM (VALUES (10, 1, 1000.0, DATE '2026-09-01'), (11, 2, 3000.0, DATE '2026-09-02'), "
                   "(12, 3, 500.0, DATE '2026-09-03')) AS t (booking_id, customer_id, amount, booked_on)", "iceberg", COMPANY)
        m1, m2 = put_model(bob, COMPANY, COMPANY_MODEL), put_model(bob, CAMPAIGNS, CAMPAIGNS_MODEL)
        check("bob makes the company's shared model, and campaigns' built on it",
              ok and m1.ok and m2.ok and COMPANY in m2.json().get("builtOn", []), (r1.text[:150], m1.text[:200], m2.text[:200]))

        # 1. The terms.
        r = alice.get(f"{PLATFORM}/api/terms", timeout=60)
        terms = {t["name"]: t for t in (r.json().get("terms", []) if r.ok else [])}
        c, b = terms.get("customer", {}), terms.get("bookings_total", {})
        check("the platform's terms: Customer and Bookings, defined, owned, and where they come from",
              c.get("label") == "Customer" and c.get("owner") == "bob" and "client" in c.get("synonyms", [])
              and "iceberg.company.customers" in c.get("tables", []) and b.get("label") == "Bookings"
              and b.get("tables") == ["iceberg.company.bookings"] and c.get("kind") == "entity", (r.status_code, c, b))
        check("... with the metrics on Customer, and campaigns among the projects using them",
              "customer_count" in c.get("metrics", []) and CAMPAIGNS in b.get("usedBy", []), (c.get("metrics"), b.get("usedBy")))

        # 2. alice asks campaigns' own metric by the company's segment.
        r = alice.post(f"{PLATFORM}/api/projects/{CAMPAIGNS}/semantic/query", headers=H, timeout=120,
                       json={"metrics": ["bookings_per_customer"], "group_by": ["customer__segment"], "order_by": ["customer__segment"]})
        rows = r.json().get("rows") if r.ok else r.text[:300]
        check("alice asks campaigns' bookings per customer by the company's segment, and gets it",
              r.ok and [[x[0], round(float(x[1]))] for x in rows] == [["enterprise", 2000], ["small business", 500]], rows)

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
            return t.ok and any(x.get("tagFQN") == "BusinessTerms.customer" for x in t.json().get("tags", []))
        found = wait(tagged, 900, every=15)
        gt = s.get(f"{om.OM}/glossaryTerms", params={"glossary": "BusinessTerms", "limit": 100}, timeout=30)
        names = sorted(t["name"] for t in (gt.json().get("data", []) if gt.ok else []))
        check("the catalog's glossary has the terms, and the company's customers table is tagged Customer",
              {"customer", "bookings_total"} <= set(names) and found, (names, found))
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
