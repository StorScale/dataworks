"""catalog-sync keeps the catalog (OpenMetadata) in step with the platform.

Every round, it:
  1. signs in to OpenMetadata as the platform operator's service account (an
     administrator there), and writes the ingestion bot's token to
     /catalog/token, for the services that report to the catalog (Airflow's
     lineage, platformd's Flow view); in Kubernetes, to the Secret
     catalog-token, which they mount there;
  2. ingests Trino's tables (the iceberg catalog, and each project's
     sources' catalogs) as the database service "trino", so every table,
     column and schema is in the catalog (its Trino account may see what's
     in a source, and read none of it);
  3. runs each project's checks (spec.tables.checks) as OpenMetadata tests on
     its tables, which shows their results on the tables;
  4. describes each project's dbt models from its last run's artifacts (use
     case 7.5: dbt/<project>/ in the platform's bucket, which platformd keeps):
     their descriptions, their lineage and their tests' results;
  5. links each source's table a project copies (sources[].copy, use case
     8.1) to its copy, <source>_<table>, column by column; and each import's
     pipeline (use case 8.2) to its tables, <import>_<resource>: their lineage;
  6. puts each project's registered models (R15.6) in the ML model service
     "storscale-mlflow", as <project>__<model>: their cards, approved
     versions, who registered and approved them, and the catalog's commit
     their training run read; each scoring's input upstream of its model and
     its output downstream, the input's snapshot then on the edge. platformd
     says what they are (only it reaches MLflow); models gone are removed.

Once, it also makes the pipeline service "airflow", and has OpenMetadata file
the pipelines it learns of from OpenLineage events under it.

Metadata is ingested every METADATA_EVERY seconds; checks run every round.
"""
import json
import logging
import os
import re
import sys
import time

import boto3
import classify
import requests
from metadata.workflow.data_quality import TestSuiteWorkflow
from metadata.workflow.metadata import MetadataWorkflow
from metadata.workflow.profiler import ProfilerWorkflow

logging.basicConfig(level=logging.INFO, format="catalog-sync: %(message)s")
log = logging.getLogger("catalog-sync")

sys.path.insert(0, "/tools")   # stack/tools: k8s.py, in Kubernetes
try:
    import k8s
except ImportError:
    k8s = None
for noisy in ("metadata", "urllib3", "botocore", "sqlfluff"):
    logging.getLogger(noisy).setLevel(logging.WARNING)

env = os.environ
OM = "http://openmetadata:8585/api"
SERVICE = "trino"
EVERY = int(env.get("SYNC_EVERY", "20"))
METADATA_EVERY = int(env.get("METADATA_EVERY", "60"))
CLASSIFY_EVERY = int(env.get("CLASSIFY_EVERY", "120"))   # where personal data is (use case 12.5)
PROFILE_EVERY = int(env.get("PROFILE_EVERY", "3600"))    # what each table holds (6.11): a table is profiled when it has no profile, or one this old
CHECKS = {  # a project's check -> OpenMetadata's test definition
    "unique": "columnValuesToBeUnique",
    "notNull": "columnValuesToBeNotNull",
    "rowCount": "tableRowCountToBeBetween",
    "acceptedValues": "columnValuesToBeInSet",
    "range": "columnValuesToBeBetween",
    "regex": "tableCustomSQLQuery",        # Trino's regexp_like: OpenMetadata's own regex test falls back to LIKE on Trino
    "references": "tableCustomSQLQuery",   # a query for the values the other table lacks
    "sql": "tableCustomSQLQuery",          # the project's own query, which must return no rows
}


def admin_headers():
    r = requests.post(f"{env['KEYCLOAK_DIRECT_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    r.raise_for_status()
    return {"Authorization": f"Bearer {r.json()['access_token']}"}


def bot_token():
    """The ingestion bot's JWT (OpenMetadata's own), as an administrator reads it."""
    h = admin_headers()
    bot = requests.get(f"{OM}/v1/bots/name/ingestion-bot", headers=h, timeout=10)
    bot.raise_for_status()
    auth = requests.get(f"{OM}/v1/users/auth-mechanism/{bot.json()['botUser']['id']}", headers=h, timeout=10)
    auth.raise_for_status()
    return auth.json()["config"]["JWTToken"]


def server_config(token):
    return {"openMetadataServerConfig": {"hostPort": OM, "authProvider": "openmetadata",
                                         "securityConfig": {"jwtToken": token}},
            "loggerLevel": "WARN"}


def trino_connection(catalog):
    return {"config": {
        "type": "Trino", "hostPort": "trino:8443", "username": "openmetadata",
        "authType": {"password": env["OPENMETADATA_TRINO_PASSWORD"]},
        # One catalog a run: without one, the connection's test reads an
        # information_schema of no catalog, which Ranger refuses.
        "catalog": catalog,
        "connectionArguments": {"http_scheme": "https", "verify": "/tls/cert.pem"},
    }}


def catalogs():
    """The Trino catalogs to describe: the lakehouse's, and the projects' sources'."""
    out = ["iceberg"]
    for p in projects():
        prefix = p["metadata"]["name"].replace("-", "_")
        out += [f"{prefix}_{s['name']}" for s in p.get("spec", {}).get("sources") or []]
    return out


def ingest_metadata(token):
    """Each catalog in turn, into the one service: a source's failing (its
    database down) doesn't keep the others from the catalog."""
    failed = []
    for catalog in catalogs():
        try:
            ingest_catalog(token, catalog)
        except Exception as e:  # noqa: BLE001
            if catalog == "iceberg":
                raise
            failed.append(f"{catalog}: {e}")
    if failed:
        log.warning("metadata: sources not described: %s", "; ".join(failed))


def ingest_catalog(token, catalog):
    workflow = MetadataWorkflow.create({
        "source": {"type": "trino", "serviceName": SERVICE, "serviceConnection": trino_connection(catalog),
                   "sourceConfig": {"config": {"type": "DatabaseMetadata", "markDeletedTables": True,
                                               "includeViews": True,
                                               "schemaFilterPattern": {"excludes": ["information_schema", "system"]}}}},
        "sink": {"type": "metadata-rest", "config": {}},
        "workflowConfig": server_config(token),
    })
    workflow.execute()
    workflow.raise_from_status()
    workflow.stop()


def ingest_dbt(token):
    """Each project's dbt artifacts (its last run's), with OpenMetadata's dbt
    source: the models' descriptions and columns, their lineage, their tests'
    results, on the tables the Trino ingestion described."""
    s3 = boto3.client("s3", endpoint_url="http://buckets:9000", region_name="us-east-1",
                      aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])
    for p in projects():
        name = p["metadata"]["name"]
        try:
            s3.head_object(Bucket="storscale-platform", Key=f"dbt/{name}/manifest.json")
        except Exception:  # noqa: BLE001 (no dbt run yet)
            continue
        workflow = MetadataWorkflow.create({
            "source": {"type": "dbt", "serviceName": SERVICE, "sourceConfig": {"config": {
                "type": "DBT", "dbtUpdateDescriptions": True, "includeTags": True,
                "dbtConfigSource": {"dbtConfigType": "s3",
                                    "dbtSecurityConfig": {"awsAccessKeyId": env["STORE_ACCESS_KEY"], "awsSecretAccessKey": env["STORE_SECRET_KEY"],
                                                          "awsRegion": "us-east-1", "endPointURL": "http://buckets:9000"},
                                    "dbtPrefixConfig": {"dbtBucketName": "storscale-platform", "dbtObjectPrefix": f"dbt/{name}"}}}}},
            "sink": {"type": "metadata-rest", "config": {}},
            "workflowConfig": server_config(token),
        })
        try:
            workflow.execute()
            workflow.raise_from_status()
        except Exception as e:  # noqa: BLE001 (one project's run doesn't keep the others out)
            log.warning("dbt of %s: %s", name, e)
        finally:
            workflow.stop()


def projects():
    s3 = boto3.client("s3", endpoint_url="http://buckets:9000", region_name="us-east-1",
                      aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])
    out = []
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket="storscale-platform", Prefix="projects/"):
        for o in page.get("Contents", []):
            out.append(json.loads(s3.get_object(Bucket="storscale-platform", Key=o["Key"])["Body"].read()))
    return out


GLOSSARY = "BusinessTerms"


def model_of(project):
    s3 = boto3.client("s3", endpoint_url="http://buckets:9000", region_name="us-east-1",
                      aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])
    try:
        return s3.get_object(Bucket="storscale-platform", Key=f"semantic/{project}.yaml")["Body"].read().decode()
    except Exception:  # noqa: BLE001 (none)
        return ""


def term_tables(term, docs):
    """The tables a term comes from (as semanticd says them): an entity's or a
    dimension's semantic models; a metric's measures', through the metrics it's made of."""
    models = [d["semantic_model"] for d in docs if "semantic_model" in d]
    metrics = {d["metric"]["name"]: d["metric"] for d in docs if "metric" in d}
    rel = lambda sm: tuple(str((sm.get("node_relation") or {}).get(k, "")) for k in ("database", "schema_name", "alias"))
    out, name = set(), term["name"]
    for sm in models:
        if any(x.get("name") == name for x in (sm.get("entities") or []) + (sm.get("dimensions") or [])):
            out.add(rel(sm))
    seen = set()

    def walk(m):
        if m in seen or m not in metrics:
            return
        seen.add(m)
        tp = metrics[m].get("type_params") or {}
        measure = tp.get("measure")
        measure = measure.get("name") if isinstance(measure, dict) else measure
        for sm in models:
            if measure and any(x.get("name") == measure for x in sm.get("measures") or []):
                out.add(rel(sm))
        for ref in [tp.get("numerator"), tp.get("denominator"), *(tp.get("metrics") or [])]:
            walk(ref.get("name") if isinstance(ref, dict) else ref)
    walk(name)
    return sorted(out)


def glossary_terms():
    """The company's terms: every shared project's model's `terms:` document (use case 10.1)."""
    import yaml
    out = {}
    for p in projects():
        if not ((p.get("spec") or {}).get("semantic") or {}).get("shared"):
            continue
        try:
            docs = [d for d in yaml.safe_load_all(model_of(p["metadata"]["name"])) if isinstance(d, dict)]
        except yaml.YAMLError:
            continue
        model = [d for d in docs if "terms" not in d]
        for t in (t for d in docs if "terms" in d for t in d["terms"] or []):
            if isinstance(t, dict) and t.get("name"):
                out[t["name"]] = {**t, "tables": term_tables(t, model)}
    return out


def sync_glossary():
    """OpenMetadata's glossary Business terms, as the shared models' terms say:
    each term with its definition, owner and synonyms, and the tables it comes
    from tagged with it; terms no longer there removed."""
    h = admin_headers()
    terms = glossary_terms()
    if not terms and requests.get(f"{OM}/v1/glossaries/name/{GLOSSARY}", headers=h, timeout=10).status_code == 404:
        return
    g = requests.put(f"{OM}/v1/glossaries", headers=h, timeout=10, json={
        "name": GLOSSARY, "displayName": "Business terms",
        "description": "The company's words for its data, from the platform's shared semantic model: kept by the platform."})
    g.raise_for_status()
    # Terms are listed by their glossary's id (its name finds none).
    have = requests.get(f"{OM}/v1/glossaryTerms", headers=h, timeout=10, params={"glossary": g.json()["id"], "limit": 1000}).json().get("data", [])
    for t in have:
        if t["name"] not in terms:
            requests.delete(f"{OM}/v1/glossaryTerms/{t['id']}", headers=h, timeout=10, params={"hardDelete": "true", "recursive": "true"})
    for name, t in terms.items():
        body = {"glossary": GLOSSARY, "name": name, "displayName": t.get("label") or name.replace("_", " ").capitalize(),
                "description": t.get("definition") or "No definition yet.", "synonyms": [str(x) for x in t.get("synonyms") or []]}
        if t.get("owner"):
            u = requests.get(f"{OM}/v1/users/name/{t['owner']}", headers=h, timeout=10)
            if u.ok:
                body["owners"] = [{"id": u.json()["id"], "type": "user"}]
        r = requests.put(f"{OM}/v1/glossaryTerms", headers=h, timeout=10, json=body)
        if not r.ok:
            log.warning("glossary: %s: %s", name, r.text[:200])
            continue
        fqn = f"{GLOSSARY}.{name}"
        for database, schema, table in t["tables"]:
            tr = requests.get(f"{OM}/v1/tables/name/{SERVICE}.{database}.{schema}.{table}", headers=h, timeout=10, params={"fields": "tags"})
            if not tr.ok:
                continue   # not described yet: the next round
            tags = tr.json().get("tags") or []
            if any(x.get("tagFQN") == fqn for x in tags):
                continue
            patch = [{"op": "add", "path": f"/tags/{len(tags)}",
                      "value": {"tagFQN": fqn, "source": "Glossary", "labelType": "Manual", "state": "Confirmed"}}]
            pr = requests.patch(f"{OM}/v1/tables/{tr.json()['id']}", headers={**h, "Content-Type": "application/json-patch+json"},
                                timeout=10, data=json.dumps(patch))
            if not pr.ok:
                log.warning("glossary: tagging %s.%s with %s: %s", schema, table, name, pr.text[:200])


DOMAIN = "StorScale"


def product_hash(pr):
    """A product's promise, as platformd hashes it to certify (its table, version and contract)."""
    import hashlib
    b = json.dumps({"Table": pr.get("table", ""), "Version": pr.get("version", ""), "Contract": pr.get("contract")},
                   separators=(",", ":")).encode()
    return hashlib.sha256(b).hexdigest()[:16]


def sync_products():
    """OpenMetadata's data products as the projects' (use case 10.3): in the
    domain StorScale (shown as the install's title, DataWorks by default), one per product, its table its asset and its owner the
    product's; a certified product's table marked Certification.Gold (and
    unmarked when it isn't any more); products gone, removed."""
    h = admin_headers()
    wanted = {}
    for p in projects():
        spec, name = p.get("spec") or {}, p["metadata"]["name"]
        ns = (spec.get("tables") or {}).get("namespace") or name.replace("-", "_")
        for pr in spec.get("products") or []:
            wanted[f"{name}__{pr['name']}"] = (name, ns, pr)
    if not wanted and requests.get(f"{OM}/v1/domains/name/{DOMAIN}", headers=h, timeout=10).status_code == 404:
        return
    requests.put(f"{OM}/v1/domains", headers=h, timeout=10, json={
        "name": DOMAIN, "displayName": os.environ.get("BRAND_TITLE") or "DataWorks", "domainType": "Aggregate",
        "description": "The data products the platform's projects publish: kept by the platform."}).raise_for_status()
    have = requests.get(f"{OM}/v1/dataProducts", headers=h, timeout=10, params={"domain": DOMAIN, "limit": 1000}).json().get("data", [])
    for d in have:
        if d["name"] not in wanted:
            requests.delete(f"{OM}/v1/dataProducts/{d['id']}", headers=h, timeout=10, params={"hardDelete": "true"})
    for key, (project, ns, pr) in wanted.items():
        t = requests.get(f"{OM}/v1/tables/name/{SERVICE}.iceberg.{ns}.{pr['table']}", headers=h, timeout=10, params={"fields": "certification"})
        body = {"name": key, "displayName": f"{pr['name']} ({project})", "domains": [DOMAIN],
                "description": (pr.get("description") or f"{pr['table']} of {project}") + f" Version {pr.get('version', '')}."}
        if t.ok:
            body["assets"] = [{"id": t.json()["id"], "type": "table"}]
        if pr.get("owner"):
            u = requests.get(f"{OM}/v1/users/name/{pr['owner']}", headers=h, timeout=10)
            if u.ok:
                body["owners"] = [{"id": u.json()["id"], "type": "user"}]
        r = requests.put(f"{OM}/v1/dataProducts", headers=h, timeout=10, json=body)
        if not r.ok:
            log.warning("data product %s: %s", key, r.text[:200])
        if not t.ok:
            continue   # not described yet: the next round
        cert = pr.get("certification") or {}
        certified = cert.get("hash") == product_hash(pr)
        now = (t.json().get("certification") or {}).get("tagLabel", {}).get("tagFQN")
        patch = None
        if certified and now != "Certification.Gold":
            ms = int(time.time() * 1000)
            patch = [{"op": "add", "path": "/certification", "value": {
                "tagLabel": {"tagFQN": "Certification.Gold", "source": "Classification", "labelType": "Manual", "state": "Confirmed"},
                "appliedDate": ms, "expiryDate": ms + 365 * 24 * 3600 * 1000}}]
        elif not certified and now == "Certification.Gold":
            patch = [{"op": "remove", "path": "/certification"}]
        if patch:
            pr_ = requests.patch(f"{OM}/v1/tables/{t.json()['id']}", headers={**h, "Content-Type": "application/json-patch+json"},
                                 timeout=10, data=json.dumps(patch))
            if not pr_.ok:
                log.warning("certification of %s: %s", key, pr_.text[:200])


def copy_lineage():
    """Each copied source table, upstream of its copy (both described by now,
    the copy once its first commit is in): an edge, its columns matched by
    name. Put again each round: OpenMetadata keeps one edge per pair."""
    h = admin_headers()

    def table(fqn):
        r = requests.get(f"{OM}/v1/tables/name/{fqn}", params={"fields": "columns"}, headers=h, timeout=30)
        return r.json() if r.ok else None
    for p in projects():
        tables = p["spec"].get("tables")
        if tables is None:
            continue
        prefix = p["metadata"]["name"].replace("-", "_")
        namespace = tables.get("namespace") or prefix
        for s in p["spec"].get("sources") or []:
            for t in (s.get("copy") or {}).get("tables") or []:
                schema, name = t.split(".", 1)
                src = table(f"{SERVICE}.{prefix}_{s['name']}.{schema}.{name}")
                dst = table(f"{SERVICE}.{tables.get('catalog') or 'iceberg'}.{namespace}.{s['name']}_{name}")
                if not src or not dst:
                    continue
                theirs = {c["name"] for c in src.get("columns", [])}
                columns = [{"fromColumns": [f"{src['fullyQualifiedName']}.{c['name']}"], "toColumn": f"{dst['fullyQualifiedName']}.{c['name']}"}
                           for c in dst.get("columns", []) if c["name"] in theirs]
                r = requests.put(f"{OM}/v1/lineage", headers=h, timeout=30, json={"edge": {
                    "fromEntity": {"id": src["id"], "type": "table"}, "toEntity": {"id": dst["id"], "type": "table"},
                    "lineageDetails": {"columnsLineage": columns, "description": f"Copied by change capture (source {s['name']})"}}})
                if not r.ok:
                    log.warning("lineage of %s to %s: HTTP %d %s", src["fullyQualifiedName"], dst["fullyQualifiedName"], r.status_code, r.text[:200])


def import_lineage():
    """Each import's pipeline (its DAG, in the pipeline service "airflow"),
    upstream of the tables it writes (once they're described)."""
    h = admin_headers()
    pipelines_url = env.get("PIPELINES_URL", "")
    for p in projects():
        tables = p["spec"].get("tables")
        if tables is None:
            continue
        prefix = p["metadata"]["name"].replace("-", "_")
        namespace = tables.get("namespace") or prefix
        for im in p["spec"].get("imports") or []:
            dag = f"import_{prefix}_{im['name']}"
            r = requests.put(f"{OM}/v1/pipelines", headers=h, timeout=30, json={
                "name": f"airflow-{dag}", "displayName": dag, "service": "airflow",
                "description": f"The import {im['name']} of the project {p['metadata']['name']} ({im['source']}): the platform keeps it.",
                **({"sourceUrl": f"{pipelines_url}/dags/{dag}"} if pipelines_url else {})})
            if not r.ok:
                log.warning("pipeline %s: HTTP %d %s", dag, r.status_code, r.text[:200])
                continue
            pipeline = r.json()
            names = [x["name"] for x in (im.get("api") or {}).get("resources", [])] + \
                    [x["table"].split(".", 1)[1] for x in (im.get("database") or {}).get("tables", [])]
            for name in names:
                t = requests.get(f"{OM}/v1/tables/name/{SERVICE}.{tables.get('catalog') or 'iceberg'}.{namespace}.{im['name']}_{name}",
                                 headers=h, timeout=30)
                if not t.ok:
                    continue
                r = requests.put(f"{OM}/v1/lineage", headers=h, timeout=30, json={"edge": {
                    "fromEntity": {"id": pipeline["id"], "type": "pipeline"}, "toEntity": {"id": t.json()["id"], "type": "table"},
                    "lineageDetails": {"description": f"Imported by {dag} ({im['source']})"}}})
                if not r.ok:
                    log.warning("lineage of %s to %s: HTTP %d %s", dag, name, r.status_code, r.text[:200])


def dedupe_column_lineage():
    """Each edge into a project's tables keeps one entry per column and its
    sources: the catalog had campaign_results' name from campaigns' name ten
    times over. What was repeated is logged, with the edge's source and
    pipeline, so whoever adds the copies can be found."""
    h = admin_headers()
    for p in projects():
        tables = p["spec"].get("tables")
        if tables is None:
            continue
        schema = f"{SERVICE}.{tables.get('catalog') or 'iceberg'}.{tables.get('namespace') or p['metadata']['name'].replace('-', '_')}"
        r = requests.get(f"{OM}/v1/tables", params={"databaseSchema": schema, "limit": 1000}, headers=h, timeout=30)
        for t in r.json().get("data", []) if r.ok else []:
            r = requests.get(f"{OM}/v1/lineage/table/name/{t['fullyQualifiedName']}", headers=h, timeout=30,
                             params={"upstreamDepth": 1, "downstreamDepth": 0})
            if not r.ok:
                continue
            fqn = {n["id"]: n["fullyQualifiedName"] for n in r.json().get("nodes", [])}
            for e in r.json().get("upstreamEdges", []):
                details = e.get("lineageDetails") or {}
                columns = details.get("columnsLineage") or []
                kept = merge_column_lineage(columns)
                if len(kept) == len(columns):
                    continue
                log.warning("lineage %s -> %s: %d column entries, %d once merged (source %s, pipeline %s); repeated: %s",
                            fqn.get(e["fromEntity"], e["fromEntity"]), t["fullyQualifiedName"], len(columns), len(kept),
                            details.get("source"), (details.get("pipeline") or {}).get("fullyQualifiedName"),
                            json.dumps(repeated_column_lineage(columns))[:2000])
                r = requests.patch(f"{OM}/v1/lineage/table/{e['fromEntity']}/table/{e['toEntity']}", timeout=30,
                                   headers={**h, "Content-Type": "application/json-patch+json"},
                                   json=[{"op": "replace", "path": "/columnsLineage", "value": kept}])
                if not r.ok:
                    log.warning("lineage %s -> %s: merging its columns: HTTP %d %s", e["fromEntity"], t["fullyQualifiedName"],
                                r.status_code, r.text[:200])


def merge_column_lineage(columns):
    """One entry per column, each source column once, in the order first
    seen; an entry's other fields (its function) are the last copy's."""
    out = {}
    for c in columns:
        to = c.get("toColumn")
        if to in out:
            sources = out[to]["fromColumns"]
            out[to] = {**c, "fromColumns": sources + [f for f in c.get("fromColumns") or [] if f not in sources]}
        else:
            out[to] = {**c, "fromColumns": list(dict.fromkeys(c.get("fromColumns") or []))}
    return list(out.values())


def repeated_column_lineage(columns):
    """The entries of a column held more than once, as the catalog has them."""
    count = {}
    for c in columns:
        count[c.get("toColumn")] = count.get(c.get("toColumn"), 0) + 1
    return [c for c in columns if count[c.get("toColumn")] > 1][:6]


PLATFORMD_URL = env.get("PLATFORMD_URL", "http://platformd:8080")
# The platform as people reach it, for the models' links: PIPELINES_URL is <platform>/pipelines.
PLATFORM_URL = env.get("PLATFORM_URL") or env.get("PIPELINES_URL", "").removesuffix("/pipelines")
ML_SERVICE = "storscale-mlflow"


def when(ms):
    return time.strftime("%Y-%m-%d %H:%M UTC", time.gmtime(ms / 1000)) if ms else "at a time not recorded"


def model_description(project, m, scorings):
    """A model's page: its card, then what the platform knows of it."""
    v = m.get("approved")
    out = [m.get("description") or "No card yet: it's written on the project's Models tab.", "", "---", "",
           f"The registered model **{m['name']}** of the project **{project}** ({m.get('versions', 0)} versions), "
           "kept in the catalog by the platform from its MLflow registry."]
    if not v:
        out.append("\nNo version is approved yet: nothing scores with it.")
    else:
        out.append(f"\n**Approved version {v['version']}**: registered by {v.get('registeredBy') or 'someone'}, "
                   f"approved by {v.get('approvedBy') or 'someone'}" + (f" ({v['approvedAt']})." if v.get("approvedAt") else "."))
        if v.get("runId"):
            run = f"Trained by the run `{v['runId']}`" + (f" of {v['runBy']}" if v.get("runBy") else "") + f", started {when(v.get('started'))}"
            if v.get("dataCommit"):
                out.append(f"\n{run}, on the catalog at its Nessie commit `{v['dataCommit']}`: every table as it was then.")
            else:
                out.append(f"\n{run}; it recorded no data commit.")
        if v.get("tables"):
            out.append("\nIts training read " + ", ".join(f"`{t['table']}`" + (f" (snapshot {t['snapshot']})" if t.get("snapshot") else "")
                                                       for t in v["tables"]) + ", as its notebook's queries recorded them.")
    for s in scorings:
        out.append(f"\nScores `{s['table']}` into `{s['output']}` (the scoring {s['name']}, `{s['schedule']}`).")
    return "\n".join(out)


def sync_models():
    """OpenMetadata's ML models as the projects' registered models (R15.6),
    as platformd says them; upstream of each model, the tables its approved
    version's training read (each at its snapshot then) and each scoring's
    input table (with its snapshot when the approved version was trained);
    downstream, each scoring's output. Only tables the catalog has. Models
    gone (their project too) removed, and edges neither draws; a project
    whose models platformd couldn't read keeps its."""
    h = admin_headers()
    r = requests.get(f"{PLATFORMD_URL}/internal/catalog/models", headers=h, timeout=120)
    r.raise_for_status()
    got = r.json()
    if not got.get("available"):
        return
    wanted, unknown = {}, set()
    for p in got.get("projects") or []:
        if p.get("error"):
            unknown.add(p["name"])
            log.warning("models of %s: %s", p["name"], p["error"])
        for m in p.get("models") or []:
            wanted[f"{p['name']}__{m['name']}"] = (p, m)
    if not wanted and requests.get(f"{OM}/v1/services/mlmodelServices/name/{ML_SERVICE}", headers=h, timeout=10).status_code == 404:
        return
    requests.put(f"{OM}/v1/services/mlmodelServices", headers=h, timeout=10, json={
        "name": ML_SERVICE, "displayName": "DataWorks MLflow", "serviceType": "Mlflow",
        "description": "The projects' registered models, from the platform's MLflow (a workspace per project): kept by the platform."}).raise_for_status()
    have = requests.get(f"{OM}/v1/mlmodels", headers=h, timeout=30, params={"service": ML_SERVICE, "limit": 1000})
    have.raise_for_status()
    for x in have.json().get("data", []):
        if x["name"] not in wanted and x["name"].split("__", 1)[0] not in unknown:
            requests.delete(f"{OM}/v1/mlmodels/{x['id']}", headers=h, timeout=30, params={"hardDelete": "true"})
            log.info("model %s: no longer registered, removed", x["name"])

    found = {}

    def by_name(fqn):   # catalog.schema.table, in the catalog's Trino service; None when it hasn't the table
        if fqn not in found:
            t = requests.get(f"{OM}/v1/tables/name/{SERVICE}.{fqn}", headers=h, timeout=30)
            found[fqn] = t.json() if t.ok else None
        return found[fqn]

    def table(p, name):
        return by_name(f"{p['catalog']}.{p['namespace']}.{name}")
    for key, (p, m) in wanted.items():
        project, v = p["name"], m.get("approved") or {}
        scorings = [s for s in p.get("scorings") or [] if s["model"] == m["name"]]
        body = {"name": key, "displayName": f"{m['name']} ({project})", "service": ML_SERVICE,
                "algorithm": v.get("algorithm") or "mlmodel",   # as OpenMetadata's own MLflow ingestion says it, when the run doesn't
                "description": model_description(project, m, scorings),
                "mlHyperParameters": [{"name": k, "value": str(val)} for k, val in sorted((v.get("params") or {}).items())]}
        if v.get("source"):
            body["mlStore"] = {"storage": v["source"]}
        if PLATFORM_URL:
            body["sourceUrl"] = f"{PLATFORM_URL}/p/{project}/models"
        r = requests.put(f"{OM}/v1/mlmodels", headers=h, timeout=30, json=body)
        if not r.ok:
            log.warning("model %s: HTTP %d %s", key, r.status_code, r.text[:200])
            continue
        model = r.json()
        edges = set()
        upstream = {}   # a table's id -> (its name, what its edge to the model says): one edge, however it's drawn
        for t in v.get("tables") or []:
            src = by_name(t["table"])
            if src:   # what the approved version's training read, and at which snapshot
                at = f"Nessie commit `{v['dataCommit']}`" if v.get("dataCommit") else "a Nessie commit not recorded"
                at += f", Iceberg snapshot {t['snapshot']}" if t.get("snapshot") else ""
                upstream.setdefault(src["id"], (t["table"], []))[1].append(
                    f"Trained on: the approved version, {v['version']}, read it (the run `{v.get('runId')}`, {when(v.get('started'))}) at {at}.")
        for s in scorings:
            src, dst = table(p, s["table"]), table(p, s["output"])
            if src:   # the input, at the snapshot the approved version was trained on
                at = "No version is approved yet."
                if v:
                    at = f"Its approved version, {v['version']}, was trained on the catalog at Nessie commit `{v.get('dataCommit') or 'unrecorded'}` ({when(v.get('started'))})"
                    at += f", when this table was Iceberg snapshot {s['inputSnapshot']}." if s.get("inputSnapshot") else (
                        ", when this table's snapshot isn't known." if v.get("dataCommit") else ".")
                columns = f" (its columns {', '.join(s['columns'])})" if s.get("columns") else ""
                upstream.setdefault(src["id"], (s["table"], []))[1].append(
                    f"Scored by {m['name']}{columns}: the scoring {s['name']} ({s['schedule']}). {at}")
            if dst:
                edges.add((model["id"], dst["id"]))
                r = requests.put(f"{OM}/v1/lineage", headers=h, timeout=30, json={"edge": {
                    "fromEntity": {"id": model["id"], "type": "mlmodel"}, "toEntity": {"id": dst["id"], "type": "table"},
                    "lineageDetails": {"description": f"Its scores of {s['table']}, by the approved version: the scoring {s['name']} ({s['schedule']}), written afresh each run."}}})
                if not r.ok:
                    log.warning("lineage of %s to %s: HTTP %d %s", key, s["output"], r.status_code, r.text[:200])
        for src_id, (name, said) in upstream.items():
            edges.add((src_id, model["id"]))
            r = requests.put(f"{OM}/v1/lineage", headers=h, timeout=30, json={"edge": {
                "fromEntity": {"id": src_id, "type": "table"}, "toEntity": {"id": model["id"], "type": "mlmodel"},
                "lineageDetails": {"description": " ".join(said)}}})
            if not r.ok:
                log.warning("lineage of %s to %s: HTTP %d %s", name, key, r.status_code, r.text[:200])
        if project not in unknown:
            prune_model_lineage(h, model["id"], edges)


def prune_model_lineage(h, model_id, keep):
    """A model's edges to and from tables that neither its scorings nor its training draw any more, deleted."""
    r = requests.get(f"{OM}/v1/lineage/mlmodel/{model_id}", headers=h, timeout=30, params={"upstreamDepth": 1, "downstreamDepth": 1})
    if not r.ok:
        return
    lineage = r.json()
    types = {n["id"]: n.get("type") for n in lineage.get("nodes") or []}
    for e in (lineage.get("upstreamEdges") or []) + (lineage.get("downstreamEdges") or []):
        a, b = (x.get("id") if isinstance(x, dict) else x for x in (e.get("fromEntity"), e.get("toEntity")))
        if model_id not in (a, b) or (a, b) in keep:
            continue
        other = b if a == model_id else a
        if types.get(other) != "table":
            continue
        ta, tb = ("mlmodel", "table") if a == model_id else ("table", "mlmodel")
        d = requests.delete(f"{OM}/v1/lineage/{ta}/{a}/{tb}/{b}", headers=h, timeout=30)
        log.info("lineage of model %s: an edge nothing draws now, removed (HTTP %d)", model_id, d.status_code)


def q(name):
    return '"' + name.replace('"', '""') + '"'


def test_case(project, check, catalog="iceberg", namespace=None):
    """One of the project's checks as an OpenMetadata test case: named by the
    project, the table, the column and the kind (or as the check names
    itself), with the kind's parameters."""
    kind = check["check"]
    # (a regex check is named _matches: once a test of OpenMetadata's own regex, whose definition a test case can't change)
    name = "_".join(filter(None, [project, check["table"], check.get("name") or check.get("column"), "matches" if kind == "regex" else kind]))
    tc = {"name": name, "testDefinitionName": CHECKS[kind], "parameterValues": []}
    if check.get("column"):
        tc["columnName"] = check["column"]
    params = tc["parameterValues"]
    if kind in ("rowCount", "range"):
        for k, param in (("min", "minValue"), ("max", "maxValue")):
            if check.get(k) is not None:
                params.append({"name": param, "value": str(check[k])})
    elif kind == "acceptedValues":
        params.append({"name": "allowedValues", "value": json.dumps(check.get("values") or [])})
    elif kind in ("regex", "references", "sql"):
        if kind == "regex":   # the column's values that don't match
            table = f"{q(catalog)}.{q(namespace)}.{q(check['table'])}"
            pattern = (check.get("pattern") or "").replace("'", "''")
            sql = (f"SELECT {q(check['column'])} FROM {table} WHERE {q(check['column'])} IS NOT NULL "
                   f"AND NOT regexp_like(CAST({q(check['column'])} AS varchar), '{pattern}')")
        elif kind == "references":   # the column's values that aren't in the other table's column
            table = f"{q(catalog)}.{q(namespace)}.{q(check['table'])}"
            ref = f"{q(catalog)}.{q(namespace)}.{q(check['refTable'])}"
            sql = (f"SELECT t.{q(check['column'])} FROM {table} t WHERE t.{q(check['column'])} IS NOT NULL "
                   f"AND NOT EXISTS (SELECT 1 FROM {ref} r WHERE r.{q(check['refColumn'])} = t.{q(check['column'])})")
        else:   # the project's tables named bare, as its people write them: qualified, since the catalog's session has no schema
            sql = re.sub(r"(?i)\b(from|join)\s+([a-z_][a-z0-9_]*)\b(?!\s*\.)",
                         lambda m: f"{m.group(1)} {q(catalog)}.{q(namespace)}.{q(m.group(2))}", check.get("sql") or "")
        params += [{"name": "sqlExpression", "value": sql}, {"name": "strategy", "value": "ROWS"},
                   {"name": "operator", "value": "=="}, {"name": "threshold", "value": "0"}]
    return tc


def prune_checks(token, project, fqn, keep):
    """The project's test cases on the table that its spec no longer declares, deleted (with their results)."""
    h = {"Authorization": f"Bearer {token}"}
    link = f"<#E::table::{fqn}>"
    r = requests.get(f"{OM}/v1/dataQuality/testCases", headers=h, timeout=30, params={"entityLink": link, "includeAllTests": "true", "limit": 200})
    if not r.ok:
        return
    for t in r.json().get("data", []):
        if t["name"].startswith(project + "_") and t["name"] not in keep:
            requests.delete(f"{OM}/v1/dataQuality/testCases/{t['id']}", headers=h, timeout=30, params={"hardDelete": "true", "recursive": "true"})
            log.info("check %s: no longer declared, removed", t["name"])


def run_checks(token):
    for p in projects():
        tables = p["spec"].get("tables") or {}
        checks = tables.get("checks") or []
        namespace = tables.get("namespace") or p["metadata"]["name"].replace("-", "_")
        catalog = tables.get("catalog") or "iceberg"
        by_table = {}
        for c in checks:
            by_table.setdefault(c["table"], []).append(test_case(p["metadata"]["name"], c, catalog, namespace))
        for table, cases in by_table.items():
            fqn = f"{SERVICE}.{catalog}.{namespace}.{table}"
            r = requests.get(f"{OM}/v1/tables/name/{fqn}", headers={"Authorization": f"Bearer {token}"}, timeout=10)
            if r.status_code == 404:
                continue  # not ingested yet
            prune_checks(token, p["metadata"]["name"], fqn, {c["name"] for c in cases})
            workflow = TestSuiteWorkflow.create({
                "source": {"type": "testsuite", "serviceName": SERVICE,
                           "sourceConfig": {"config": {"type": "TestSuite", "entityFullyQualifiedName": fqn}}},
                "processor": {"type": "orm-test-runner", "config": {"forceUpdate": True, "testCases": cases}},
                "sink": {"type": "metadata-rest", "config": {}},
                "workflowConfig": server_config(token),
            })
            workflow.execute()
            workflow.stop()


PROFILE_PER_ROUND = int(env.get("PROFILE_PER_ROUND", "3"))


def protected_columns(project):
    """A project's tagged columns (catalog-sync's own classification/<project>.json), by table."""
    s3 = boto3.client("s3", endpoint_url="http://buckets:9000", region_name="us-east-1",
                      aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])
    try:
        doc = json.loads(s3.get_object(Bucket="storscale-platform", Key=f"classification/{project}.json")["Body"].read())
    except Exception:  # noqa: BLE001 (none yet)
        return {}
    out = {}
    for c in doc.get("columns") or []:
        out.setdefault(c["table"], set()).add(c["column"])
    return out
PROFILED = {}   # table FQN -> when this process last profiled it (the catalog's list doesn't carry the profile)


def profile_tables(token):
    """Each project's tables that have no profile, or one older than
    PROFILE_EVERY, profiled by OpenMetadata's profiler as the catalog's Trino
    account (row counts; each column's nulls, distinct values, min, max,
    mean), for the Data page and the catalog's own pages (use case 6.11).
    Columns the catalog has tagged (personal, financial, health) aren't
    profiled: the account sees them as they are, for the checks, and their
    values' ranges would be in the catalog for anyone to read."""
    h = {"Authorization": f"Bearer {token}"}
    stale = time.time() - PROFILE_EVERY
    for p in projects():
        tables = p["spec"].get("tables") or {}
        namespace = tables.get("namespace") or p["metadata"]["name"].replace("-", "_")
        catalog = tables.get("catalog") or "iceberg"
        r = requests.get(f"{OM}/v1/tables", headers=h, timeout=30, params={"databaseSchema": f"{SERVICE}.{catalog}.{namespace}", "limit": 200})
        if not r.ok:
            continue
        due = [t["name"] for t in r.json().get("data", []) if not t.get("deleted") and PROFILED.get(t["fullyQualifiedName"], 0) < stale]
        due = due[:PROFILE_PER_ROUND]   # a few a round, so a round (and the checks in it) stays short after a restart
        if not due:
            continue
        tagged = protected_columns(p["metadata"]["name"])
        skip = [{"fullyQualifiedName": f"{SERVICE}.{catalog}.{namespace}.{t}", "columnConfig": {"excludeColumns": sorted(tagged[t])}}
                for t in due if tagged.get(t)]
        workflow = ProfilerWorkflow.create({
            "source": {"type": "trino", "serviceName": SERVICE, "serviceConnection": trino_connection(catalog),
                       "sourceConfig": {"config": {"type": "Profiler", "computeTableMetrics": True, "computeColumnMetrics": True,   # the whole table (the default sample)
                                                   "databaseFilterPattern": {"includes": [f"^{catalog}$"]},
                                                   "schemaFilterPattern": {"includes": [f"^{namespace}$"]},
                                                   "tableFilterPattern": {"includes": [f"^{t}$" for t in due]}}}},
            "processor": {"type": "orm-profiler", "config": {"tableConfig": skip} if skip else {}},
            "sink": {"type": "metadata-rest", "config": {}},
            "workflowConfig": server_config(token),
        })
        workflow.execute()
        workflow.stop()
        for t in due:
            PROFILED[f"{SERVICE}.{catalog}.{namespace}.{t}"] = time.time()
        log.info("profiled %s: %s", namespace, ", ".join(due))


def lineage_settings():
    """Pipelines that report lineage (OpenLineage) belong to the service "airflow"."""
    h = admin_headers()
    r = requests.put(f"{OM}/v1/services/pipelineServices", headers=h, timeout=10, json={
        "name": "airflow", "serviceType": "Airflow", "description": "The platform's pipelines.",
        "connection": {"config": {"type": "Airflow", "hostPort": env.get("PIPELINES_URL", "http://airflow:8090"),
                                  "connection": {"type": "Backend"}}}})
    r.raise_for_status()
    r = requests.put(f"{OM}/v1/system/settings", headers=h, timeout=10, json={
        "config_type": "openLineageSettings",
        "config_value": {"enabled": True, "autoCreateEntities": True, "defaultPipelineService": "airflow"}})
    r.raise_for_status()


# The platform's look, as OpenMetadata's: its logo and icon (platformd's,
# through the gateway on the catalog's name), and the web app's colours.
# The logo comes fitted to OpenMetadata's sidebar (platformd's catalog-logo: a
# wide logo would overflow it); the colours are the platform's top bar's
# near-black, as OpenMetadata's slots use them (primary for buttons and links,
# hover a light tint, selected darker than primary, info a light tone).
THEME = {
    "customLogoConfig": {"customLogoUrlPath": "/_storscale/brand/catalog-logo", "customMonogramUrlPath": "/_storscale/brand/icon",
                         "customFaviconUrlPath": "/_storscale/brand/icon"},
    "customTheme": {"primaryColor": "#10151c", "hoverColor": "#e8ecf1", "selectedColor": "#0b0f14", "errorColor": "#b4232a",
                    "successColor": "#1e7d4f", "warningColor": "#b86e00", "infoColor": "#8b97a8",
                    # The home page's Welcome card (unset: OpenMetadata's blue): a charcoal
                    # blue, under its white text and its light pattern.
                    "panelBackgroundColor": "#2c3e50"},
}


def theme_settings():
    r = requests.put(f"{OM}/v1/system/settings", headers=admin_headers(), timeout=10,
                     json={"config_type": "customUiThemePreference", "config_value": THEME})
    r.raise_for_status()


def trino_cursor_factory():
    """Connections to Trino as the catalog's account, for the classification's samples."""
    import trino.auth
    import trino.dbapi

    def connect(catalog, schema):
        return trino.dbapi.connect(host="trino", port=8443, http_scheme="https", verify="/tls/cert.pem", user="openmetadata",
                                   auth=trino.auth.BasicAuthentication("openmetadata", env["OPENMETADATA_TRINO_PASSWORD"]),
                                   catalog=catalog, schema=schema, source="catalog-sync/classification")
    return connect


def classify_projects():
    """Where personal data is: declared and found, tagged in the catalog, written for the operator."""
    s3 = boto3.client("s3", endpoint_url="http://buckets:9000", region_name="us-east-1",
                      aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])
    classify.run(OM, admin_headers(), SERVICE, projects(), trino_cursor_factory(), s3, "storscale-platform")


def main():
    os.makedirs("/catalog", exist_ok=True)
    last_metadata = 0.0
    last_classified = 0.0
    shared = None   # the token last written to the Secret
    while True:
        try:
            lineage_settings()
            theme_settings()
            break
        except Exception as e:  # noqa: BLE001 (OpenMetadata may still be starting)
            log.warning("lineage settings: %s", e)
            time.sleep(5)
    while True:
        try:
            token = bot_token()
            with open("/catalog/token.tmp", "w") as f:
                f.write(token)
            os.chmod("/catalog/token.tmp", 0o644)
            os.replace("/catalog/token.tmp", "/catalog/token")
            if k8s and k8s.in_cluster() and token != shared:
                k8s.put_secret("catalog-token", {"token": token})
                shared = token
            if time.time() - last_metadata > METADATA_EVERY:
                ingest_metadata(token)
                ingest_dbt(token)
                copy_lineage()
                import_lineage()
                try:
                    dedupe_column_lineage()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("column lineage: %s", e)
                try:
                    sync_glossary()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("glossary: %s", e)
                try:
                    sync_products()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("data products: %s", e)
                try:
                    sync_models()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("models: %s", e)
                last_metadata = time.time()
                log.info("metadata: ingested trino")
            if time.time() - last_classified > CLASSIFY_EVERY:
                try:
                    classify_projects()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("classification: %s", e)
                last_classified = time.time()
            run_checks(token)
            try:
                profile_tables(token)
            except Exception as e:  # noqa: BLE001 (the next round tries again)
                log.warning("profiling: %s", e)
            with open("/catalog/ready", "w") as f:
                f.write(str(time.time()))
        except Exception as e:  # noqa: BLE001 (keep going: the next round may work)
            log.warning("round failed: %s", e)
        time.sleep(EVERY)


if __name__ == "__main__":
    main()
