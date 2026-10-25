"""One run of a project's import (use case 8.2), in dlt's virtualenv
(/opt/dlt/bin/python), as the DAG file storscale_imports.py starts it.

STORSCALE_IMPORT is the import, as the platform operator lists it (its
project, namespace and spec); STORSCALE_IMPORT_SECRET its credentials, from
its Airflow connection (a connector's, such as Salesforce's, in its Extra). Each resource lands in iceberg.<namespace> as
<import>_<resource>: merged on its primary key, or appended to; past its
cursor alone, where it has one (dlt keeps the cursor in the namespace's
_dlt_pipeline_state, so a run in a fresh pod goes on from the last).

The tables are Iceberg's, through Nessie's REST catalog (pyiceberg), their
files in the warehouse bucket, as the lakehouse's writers' Buckets account
(COPY_S3_ACCESS_KEY, COPY_S3_SECRET_KEY).
"""
import json
import os
import urllib.parse

import dlt
from dlt.destinations import filesystem

pi = json.loads(os.environ["STORSCALE_IMPORT"])
secret = json.loads(os.environ.get("STORSCALE_IMPORT_SECRET") or "{}")
im, project, namespace = pi["import"], pi["project"], pi["namespace"]
name = im["name"]

# pyiceberg's catalog, as dlt asks pyiceberg for it (the catalog "default").
access, key = os.environ["COPY_S3_ACCESS_KEY"], os.environ["COPY_S3_SECRET_KEY"]
for k, v in {"TYPE": "rest", "URI": os.environ.get("NESSIE_ICEBERG_URL", "http://nessie:19120/iceberg/"),
             "WAREHOUSE": "warehouse", "S3__ENDPOINT": "http://buckets:9000", "S3__ACCESS_KEY_ID": access,
             "S3__SECRET_ACCESS_KEY": key, "S3__REGION": "us-east-1", "S3__PATH_STYLE_ACCESS": "true"}.items():
    os.environ[f"PYICEBERG_CATALOG__DEFAULT__{k}"] = v


def disposition(primary_key):
    return {"disposition": "merge", "strategy": "upsert"} if primary_key else "append"


def rest_api():
    from dlt.sources.rest_api import rest_api_source
    api = im["api"]
    client = {"base_url": api["baseUrl"]}
    if secret.get("password"):
        client["auth"] = {"type": "bearer", "token": secret["password"]}
    resources = []
    for r in api["resources"]:
        endpoint = {"path": r["path"]}
        if r.get("dataSelector"):
            endpoint["data_selector"] = r["dataSelector"]
        if r.get("cursor"):
            endpoint["params"] = {r["cursorParam"]: {"type": "incremental", "cursor_path": r["cursor"],
                                                     "initial_value": r["cursorStart"]}}
        res = {"name": r["name"], "endpoint": endpoint, "table_name": f"{name}_{r['name']}",
               "write_disposition": disposition(r.get("primaryKey"))}
        if r.get("primaryKey"):
            res["primary_key"] = r["primaryKey"]
        resources.append(res)
    return [rest_api_source({"client": client, "resources": resources}, name=name)]


def sql_database():
    from dlt.sources.sql_database import sql_table
    db = im["database"]
    scheme = {"postgresql": "postgresql+psycopg2", "mysql": "mysql+pymysql"}[db["kind"]]
    port = secret.get("port") or {"postgresql": 5432, "mysql": 3306}[db["kind"]]
    url = (f"{scheme}://{urllib.parse.quote(secret['username'], safe='')}:{urllib.parse.quote(secret['password'], safe='')}"
           f"@{secret['host']}:{port}/{urllib.parse.quote(secret.get('database') or '', safe='')}")
    out = []
    for t in db["tables"]:
        schema, table = t["table"].split(".", 1)
        res = sql_table(credentials=url, table=table, schema=schema,
                        incremental=dlt.sources.incremental(t["cursor"]) if t.get("cursor") else None)
        res.apply_hints(table_name=f"{name}_{table}", write_disposition=disposition(t.get("primaryKey")),
                        primary_key=t.get("primaryKey") or None)
        out.append(res)
    return out


def salesforce():
    import sys
    sys.path.insert(0, "/opt/dlt/sources")   # dlt's verified sources, and the platform's connectors (8.4)
    import storscale_connectors
    return storscale_connectors.salesforce(im, secret, name)


def connector():
    """Gong's, HighSpot's or SAP's objects, from the platform's connectors (8.4)."""
    import sys
    sys.path.insert(0, "/opt/dlt/sources")
    import storscale_connectors
    return getattr(storscale_connectors, im["source"])(im, secret, name)


def files():
    """Files of the project's landing area (landing/uploads/ in its bucket),
    each a table: read as the project's pipelines (their Keycloak client's
    token, exchanged with Buckets' STS, so the bucket's own policy decides).
    CSV, Excel (every sheet, stacked), JSON lines and Parquet, by extension;
    a file read again replaces its table, or merges on its primary key."""
    import xml.etree.ElementTree as ET

    import requests
    from dlt.sources.filesystem import filesystem, read_csv, read_jsonl, read_parquet

    identity = json.loads(os.environ["STORSCALE_IMPORT_IDENTITY"])
    r = requests.post(identity["token_url"], timeout=30, data={"grant_type": "client_credentials", "client_id": identity["client_id"],
                                                               "client_secret": identity["client_secret"]})
    r.raise_for_status()
    # Buckets' STS (AssumeRoleWithWebIdentity), called directly: the dlt virtualenv has no boto3.
    endpoint = os.environ.get("BUCKETS_ENDPOINT", "http://buckets:9000")
    sts = requests.post(endpoint, timeout=30, data={"Action": "AssumeRoleWithWebIdentity", "Version": "2011-06-15", "DurationSeconds": "3600",
                                                    "RoleSessionName": f"import-{project}", "WebIdentityToken": r.json()["access_token"]})
    if not sts.ok:
        raise RuntimeError(f"Buckets' STS refused the pipelines' token: HTTP {sts.status_code} {sts.text[:300]}")
    ns = {"s": "https://sts.amazonaws.com/doc/2011-06-15/"}
    c = ET.fromstring(sts.text).find(".//s:Credentials", ns)
    creds = {"aws_access_key_id": c.find("s:AccessKeyId", ns).text, "aws_secret_access_key": c.find("s:SecretAccessKey", ns).text,
             "aws_session_token": c.find("s:SessionToken", ns).text, "endpoint_url": endpoint, "region_name": "us-east-1"}

    @dlt.transformer(standalone=True)
    def read_excel(items):
        import pandas as pd
        for item in items:
            with item.open() as f:
                for _sheet, frame in pd.read_excel(f, sheet_name=None).items():
                    for row in frame.to_dict(orient="records"):
                        yield row

    # Text files hold dates as text: an ISO date or timestamp becomes one, as
    # the upload's preview says it will (2026-05-03 a date, not a varchar).
    import re
    from datetime import date, datetime
    day = re.compile(r"^\d{4}-\d{2}-\d{2}$")
    moment = re.compile(r"^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(\.\d{1,6})?)?$")

    def dates(row):
        for k, v in row.items():
            if isinstance(v, str):
                try:
                    if day.match(v):
                        row[k] = date.fromisoformat(v)
                    elif moment.match(v):
                        row[k] = datetime.fromisoformat(v.replace(" ", "T"))
                except ValueError:   # 2026-02-30: text it stays
                    pass
        return row

    out = []
    for t in im["files"]["tables"]:
        fmt = t["file"].rsplit(".", 1)[-1].lower()
        reader = {"csv": read_csv, "jsonl": read_jsonl, "parquet": read_parquet, "xlsx": read_excel, "xls": read_excel}[fmt]
        listing = filesystem(bucket_url=f"s3://{project}/landing/uploads/", file_glob=t["file"], credentials=creds)
        res = listing | reader()
        if fmt != "parquet":
            res = res.add_map(dates)
        res.apply_hints(table_name=f"{name}_{t['name']}", primary_key=t.get("primaryKey") or None,
                        write_disposition=disposition(t.get("primaryKey")) if t.get("primaryKey") else "replace")
        out.append(res)
    return out


sources = {"rest_api": rest_api, "sql_database": sql_database, "salesforce": salesforce, "gong": connector, "highspot": connector, "sap": connector, "files": files}[im["source"]]()
destination = filesystem(bucket_url="s3://warehouse", credentials={
    "aws_access_key_id": access, "aws_secret_access_key": key, "endpoint_url": "http://buckets:9000", "region_name": "us-east-1"})
pipeline = dlt.pipeline(pipeline_name=f"import_{project.replace('-', '_')}_{name}", destination=destination,
                        dataset_name=namespace, pipelines_dir="/tmp/storscale-imports")
# A reset: the resources' tables and dlt's state of them dropped first, so all is copied again.
refresh = "drop_sources" if os.environ.get("STORSCALE_IMPORT_RESET") == "1" else None
mine = []
if refresh:
    # dlt's own drop empties an Iceberg table but keeps it, and its columns'
    # types with it (Iceberg won't make a text column a date): the import's
    # tables (<import>_<resource>) are dropped from the catalog, so they're made afresh.
    from pyiceberg.catalog import load_catalog
    from pyiceberg.exceptions import NoSuchNamespaceError
    catalog = load_catalog("default")
    try:
        mine = [t for t in catalog.list_tables(namespace) if t[-1].startswith(f"{name}_")]
    except NoSuchNamespaceError:
        mine = []
    for t in mine:
        catalog.drop_table(t)
        print(f"reset: dropped {'.'.join(t)}")
info = pipeline.run(sources, table_format="iceberg", refresh=refresh)
print(info)
if im["source"] == "sap":   # an ODP extraction's deletions (dlt's Iceberg merge makes none)
    import storscale_connectors
    storscale_connectors.apply_sap_deletions(namespace, pipeline.default_schema.naming)
if refresh and mine:
    # The run after a drop can leave a dropped table unmade (dlt's own record
    # said it was there); an ordinary run makes it, as the next would.
    missing = [t for t in mine if not catalog.table_exists(t)]
    if missing:
        print(f"reset: {', '.join('.'.join(t) for t in missing)} not made again yet: once more")
        sources = {"rest_api": rest_api, "sql_database": sql_database, "salesforce": salesforce, "gong": connector, "highspot": connector, "sap": connector, "files": files}[im["source"]]()
        print(pipeline.run(sources, table_format="iceberg"))
