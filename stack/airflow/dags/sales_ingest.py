"""Land a day's orders in Buckets, load them into the lakehouse's
iceberg.sales.orders through Trino, then rebuild iceberg.sales.orders_by_region
from it and report that step's lineage, column by column, to the catalog.
Every task runs as the pipelines' own identity (lakehouse_identity), not as
whoever triggered the run.

One of the day's orders comes without a card number: the sales project's
check on orders.card_number catches it, in the catalog."""
import csv
import io

import pendulum
from airflow.sdk import dag, get_current_context, task
from botocore.exceptions import ClientError

NEW_ORDERS = [
    (101, "Gus", "4222222222222", "EU", 64.10, "2026-09-07"),
    (102, "Hana", "", "APAC", 212.00, "2026-09-07"),  # no card number
]

SUMMARY = ("INSERT INTO orders_by_region SELECT region, count(id), sum(amount) "
           "FROM orders GROUP BY region")
# What each column of orders_by_region comes from, in orders.
SUMMARY_COLUMNS = {"region": ["region"], "orders": ["id"], "amount": ["amount"]}


@dag(schedule=None, start_date=pendulum.datetime(2026, 9, 1, tz="UTC"), catchup=False,
     tags=["lakehouse", "project:sales"], doc_md=__doc__)  # project:<name>: the platform shows it in that project
def sales_ingest():

    @task
    def land_orders():
        from lakehouse_identity import buckets_s3
        s3 = buckets_s3()
        run = get_current_context()["run_id"].replace(":", "-").replace("+", "-")
        buf = io.StringIO()
        csv.writer(buf).writerows(NEW_ORDERS)
        key = f"landing/orders-{run}.csv"
        s3.put_object(Bucket="sales", Key=key, Body=buf.getvalue().encode())
        try:                                        # the project's pipelines policy covers sales/landing/ only
            s3.list_objects_v2(Bucket="warehouse")
            warehouse = "OK"
        except ClientError as e:
            warehouse = e.response["Error"]["Code"]
        return {"key": key, "warehouse": warehouse}

    @task
    def load_orders(landed):
        from lakehouse_identity import buckets_s3, trino
        body = buckets_s3().get_object(Bucket="sales", Key=landed["key"])["Body"].read().decode()
        rows = list(csv.reader(io.StringIO(body)))
        cur = trino().cursor()
        cur.execute("SELECT current_user")
        me = cur.fetchall()[0][0]
        cur.execute("DELETE FROM orders WHERE id IN (" + ", ".join(r[0] for r in rows) + ")")
        cur.fetchall()
        values = ", ".join(f"({i}, '{c}', {repr(n) if n else 'NULL'}, '{r}', {a}, DATE '{d}')" for i, c, n, r, a, d in rows)
        cur.execute(f"INSERT INTO orders VALUES {values}")
        cur.fetchall()
        cur.execute("SELECT count(*) FROM orders")
        return {"trino_user": me, "inserted": len(rows), "orders": cur.fetchall()[0][0], **landed}

    @task
    def summarize_orders(loaded):
        from lakehouse_identity import report_lineage, trino
        cur = trino().cursor()
        cur.execute("DELETE FROM orders_by_region")
        cur.fetchall()
        cur.execute(SUMMARY)
        cur.fetchall()
        cur.execute("SELECT count(*) FROM orders_by_region")
        regions = cur.fetchall()[0][0]
        reported = report_lineage("sales_ingest", get_current_context()["run_id"], task="summarize_orders",
                                  inputs=["iceberg.sales.orders"], output="iceberg.sales.orders_by_region",
                                  columns=SUMMARY_COLUMNS, sql=SUMMARY)
        return {**loaded, "regions": regions, "lineage": reported}

    summarize_orders(load_orders(land_orders()))


sales_ingest()
