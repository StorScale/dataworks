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
     pipeline (use case 8.2) to its tables, <import>_<resource>: their lineage.

Once, it also makes the pipeline service "airflow", and has OpenMetadata file
the pipelines it learns of from OpenLineage events under it.

Metadata is ingested every METADATA_EVERY seconds; checks run every round.
"""
import json
import logging
import os
import sys
import time

import boto3
import classify
import requests
from metadata.workflow.data_quality import TestSuiteWorkflow
from metadata.workflow.metadata import MetadataWorkflow

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
CHECKS = {  # a project's check -> OpenMetadata's test definition
    "unique": "columnValuesToBeUnique",
    "notNull": "columnValuesToBeNotNull",
    "rowCount": "tableRowCountToBeBetween",
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
    domain StorScale, one per product, its table its asset and its owner the
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
        "name": DOMAIN, "displayName": "StorScale Platform", "domainType": "Aggregate",
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


def test_case(project, check):
    name = "_".join(filter(None, [project, check["table"], check.get("column"), check["check"]]))
    tc = {"name": name, "testDefinitionName": CHECKS[check["check"]], "parameterValues": []}
    if check.get("column"):
        tc["columnName"] = check["column"]
    if check["check"] == "rowCount":
        for k, param in (("min", "minValue"), ("max", "maxValue")):
            if check.get(k) is not None:
                tc["parameterValues"].append({"name": param, "value": str(check[k])})
    return tc


def run_checks(token):
    for p in projects():
        tables = p["spec"].get("tables") or {}
        checks = tables.get("checks") or []
        namespace = tables.get("namespace") or p["metadata"]["name"].replace("-", "_")
        catalog = tables.get("catalog") or "iceberg"
        by_table = {}
        for c in checks:
            by_table.setdefault(c["table"], []).append(test_case(p["metadata"]["name"], c))
        for table, cases in by_table.items():
            fqn = f"{SERVICE}.{catalog}.{namespace}.{table}"
            r = requests.get(f"{OM}/v1/tables/name/{fqn}", headers={"Authorization": f"Bearer {token}"}, timeout=10)
            if r.status_code == 404:
                continue  # not ingested yet
            workflow = TestSuiteWorkflow.create({
                "source": {"type": "testsuite", "serviceName": SERVICE,
                           "sourceConfig": {"config": {"type": "TestSuite", "entityFullyQualifiedName": fqn}}},
                "processor": {"type": "orm-test-runner", "config": {"forceUpdate": True, "testCases": cases}},
                "sink": {"type": "metadata-rest", "config": {}},
                "workflowConfig": server_config(token),
            })
            workflow.execute()
            workflow.stop()


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
                    sync_glossary()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("glossary: %s", e)
                try:
                    sync_products()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("data products: %s", e)
                last_metadata = time.time()
                log.info("metadata: ingested trino")
            if time.time() - last_classified > CLASSIFY_EVERY:
                try:
                    classify_projects()
                except Exception as e:  # noqa: BLE001 (the next round tries again)
                    log.warning("classification: %s", e)
                last_classified = time.time()
            run_checks(token)
            with open("/catalog/ready", "w") as f:
                f.write(str(time.time()))
        except Exception as e:  # noqa: BLE001 (keep going: the next round may work)
            log.warning("round failed: %s", e)
        time.sleep(EVERY)


if __name__ == "__main__":
    main()
