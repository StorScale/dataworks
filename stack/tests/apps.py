"""A project's apps: docker compose run --rm test apps

A project of the suite's own, appy, whose readers see only the EU's orders
(a row filter), with a metric, a table of notes and a knowledge bank over
them. These check (use case 14.1):

  1. bob (an editor) makes a metric app (revenue by region, as bars, with a
     region filter) and a table app (the orders); alice, a reader, can't;
  2. one app, two people: bob sees both regions' revenue, alice only the
     EU's, each fetched as themselves; the table app the same;
  3. the region filter narrows bob's to the US;
  4. a question app over the knowledge bank: alice asks, and the answer comes
     with the passages it's from.

The project is removed after.
"""
import json
import os

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "appy"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Apps: the apps suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}],
                 "tables": {"readers": {"rowFilters": [{"table": "orders", "filter": "region = 'EU'"}]}},
                 "llm": {"budget": 1},
                 "knowledge": [{"name": "notes", "tables": [{"table": "notes", "column": "text", "id": "id"}]}]}}
MODEL = """semantic_model:
  name: orders
  node_relation: {database: iceberg, schema_name: appy, alias: orders}
  defaults: {agg_time_dimension: ordered_on}
  entities:
    - {name: order, type: primary, expr: id}
  dimensions:
    - {name: region, type: categorical}
    - {name: ordered_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: amount_sum, expr: amount, agg: sum}
---
metric:
  name: revenue
  type: simple
  type_params: {measure: amount_sum}
"""
BY_REGION = {"title": "Revenue by region", "kind": "metric", "metrics": ["revenue"], "by": "order__region", "show": "bar",
             "filters": [{"field": "order__region", "label": "Region", "values": ["EU", "US"]}]}
ORDERS = {"title": "The orders", "kind": "table", "table": "orders", "columns": ["id", "region", "amount"]}
ASK = {"title": "Ask the notes", "kind": "qa", "bank": "notes"}


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        conns = lambda: {c["name"] for c in (bob.get(f"{url}/connections", timeout=30).json() or {}).get("connections", [])}
        keyed = wait(lambda: True if f"{NAME}-llm" in conns() else None, 300, every=5)
        for t in ("orders", "notes", "knowledge_notes"):
            sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", NAME)   # a run before may have left them
        sql("bob", "CREATE TABLE orders AS SELECT * FROM (VALUES (1, 'EU', 10.0, DATE '2026-10-01'), (2, 'US', 7.5, DATE '2026-10-02')) "
                   "AS t (id, region, amount, ordered_on)", "iceberg", NAME)
        sql("bob", "CREATE TABLE notes AS SELECT * FROM (VALUES (1, 'The jacket seam split after a week of use.'), "
                   "(2, 'Returns are taken within 30 days, with the receipt.')) AS t (id, text)", "iceberg", NAME)
        m = bob.put(f"{url}/semantic", json={"yaml": MODEL}, headers=H, timeout=120)
        check("bob makes a project whose readers see the EU's orders, with a metric and notes", r.ok and ready is not None and keyed and m.ok,
              (r.text[:150], m.text[:200]))

        # 1. Made by an editor, not a reader.
        made = [bob.put(f"{url}/apps/{n}", json=a, headers=H, timeout=30) for n, a in (("by-region", BY_REGION), ("orders", ORDERS))]
        refused = alice.put(f"{url}/apps/hers", json=ORDERS, headers=H, timeout=30)
        listed = (alice.get(f"{url}/apps", timeout=30).json() or {}).get("apps", [])
        check("bob makes a metric app and a table app; alice, a reader, sees them but can't make one",
              all(x.ok for x in made) and refused.status_code == 403 and {a["name"] for a in listed} == {"by-region", "orders"},
              ([x.status_code for x in made], refused.status_code, [a["name"] for a in listed]))

        # 2. One app, two people.
        def rows(s, app, filters=None):
            got = s.post(f"{url}/apps/{app}/data", json={"filters": filters or {}}, headers=H, timeout=180)
            return (got.json() if got.ok else {}).get("rows"), got
        bob_rows, b = rows(bob, "by-region")
        alice_rows, a = rows(alice, "by-region")
        check("one app, two people: bob sees both regions' revenue, alice only the EU's, each as themselves",
              bob_rows and sorted(r[0] for r in bob_rows) == ["EU", "US"] and alice_rows and [r[0] for r in alice_rows] == ["EU"],
              (bob_rows, alice_rows, b.text[:200], a.text[:200]))
        bob_orders, _ = rows(bob, "orders")
        alice_orders, _ = rows(alice, "orders")
        check("... and the table app: bob two orders, alice the EU's one", len(bob_orders or []) == 2 and len(alice_orders or []) == 1,
              (bob_orders, alice_orders))

        # 3. The filter.
        us, _ = rows(bob, "by-region", {"order__region": "US"})
        check("the region filter narrows bob's to the US", us and [r[0] for r in us] == ["US"], us)

        # 4. A question app.
        built = bob.post(f"{url}/knowledge/notes/build", headers=H, timeout=600)
        qa = bob.put(f"{url}/apps/ask-notes", json=ASK, headers=H, timeout=30)
        asked = alice.post(f"{url}/apps/ask-notes/ask", json={"question": "What happened to the seam?"}, headers=H, timeout=300)
        aj = asked.json() if asked.ok else {}
        check("a question app over the knowledge bank: alice asks, and the answer comes with the passages it's from",
              built.ok and qa.ok and asked.ok and aj.get("answer") and any(p.get("source") == "notes.text" for p in aj.get("passages") or []),
              (built.status_code, qa.status_code, qa.text[:200], asked.status_code, asked.text[:300]))
    finally:
        for t in ("knowledge_notes", "notes", "orders"):
            sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", NAME)
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
