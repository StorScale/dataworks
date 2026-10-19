"""The sales project, and its data: what the integration guides' checks use.

  - projects/sales.json goes into the project store, as platformd would put
    it there (in Kubernetes, a Project resource); the platform operator
    applies it (Keycloak groups, Ranger policies, the sales bucket and its
    policies, the Nessie namespace), and this waits until it says the project
    is ready (in the store's bucket, where the operator keeps a copy of each
    project's status in Kubernetes too);
  - then the project's data: iceberg.sales.orders and iceberg.sales.payroll,
    written by bob (an editor), and sales/datasets/orders.parquet.

Safe to run again.
"""
import io
import json
import sys
import time

import k8s
import pandas as pd

from lakekit import load_sales_tables, log, s3_root

STORE = "storscale-platform"


def wait_ready(name, timeout=300):
    s3, deadline = s3_root(), time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            st = json.loads(s3.get_object(Bucket=STORE, Key=f"status/{name}.json")["Body"].read())
            last = st
            if st["phase"] == "Ready":
                return st
        except s3.exceptions.NoSuchKey:
            pass
        time.sleep(2)
    sys.exit(f"setup: project {name} isn't ready: {json.dumps(last and last.get('parts'))}")


if __name__ == "__main__":
    with open("/projects/sales.json") as f:
        spec = f.read()
    if k8s.in_cluster():
        k8s.put_project(json.loads(spec))
    else:
        s3_root().put_object(Bucket=STORE, Key="projects/sales.json", Body=spec.encode(), ContentType="application/json")
    st = wait_ready("sales")
    log("project sales: " + ", ".join(f"{m['username']} ({m['role']})" for m in st["members"]))
    # Ranger's plugin in Trino picks up new policies and groups within seconds.
    for attempt in range(30):
        try:
            load_sales_tables()
            break
        except Exception as e:  # noqa: BLE001 (a denial until the plugin catches up)
            if attempt == 29:
                raise
            log(f"waiting for Trino to see the project ({str(e).splitlines()[0][:120]})")
            time.sleep(5)
    orders = pd.DataFrame({
        "id": [1, 2, 3, 4, 5, 6],
        "region": ["EU", "US", "EU", "APAC", "EU", "US"],
        "amount": [120.50, 75.00, 310.25, 42.00, 18.99, 99.95],
        "order_date": pd.to_datetime(["2026-09-01", "2026-09-02", "2026-09-03",
                                      "2026-09-04", "2026-09-05", "2026-09-06"]),
    })
    buf = io.BytesIO()
    orders.to_parquet(buf, index=False)
    s3_root().put_object(Bucket="sales", Key="datasets/orders.parquet", Body=buf.getvalue())
    log("Buckets: sales/datasets/orders.parquet (6 orders)")
    # The project's semantic model: its metrics, for people and agents.
    with open("/projects/sales.semantic.yaml") as f:
        s3_root().put_object(Bucket=STORE, Key="semantic/sales.yaml", Body=f.read().encode(), ContentType="application/yaml")
    log("semantic layer: the sales project's metrics (projects/sales.semantic.yaml)")
    # The table the sales pipeline rebuilds, by region (it may change rows,
    # not make tables).
    from lakekit import sql
    sql("bob", "CREATE TABLE IF NOT EXISTS orders_by_region (region varchar, orders bigint, amount decimal(12,2))",
        catalog="iceberg", schema="sales")
    log("data: iceberg.sales.orders_by_region (empty, for the pipeline)")
    log("done")
