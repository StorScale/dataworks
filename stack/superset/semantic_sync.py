"""Superset's datasets from the semantic models (use case 10.2): each
semantic model of a project's (the company's shared ones among them) a
dataset in Superset, with the model's metrics defined on it, so a chart's
Revenue is the governed Revenue. Run beside Superset, from its image
(superset-sync), every minute: datasets it made and whose model is gone are
removed, others (people's own) are never touched.

A dataset is a SQL view over the model's table: its dimensions named as
MetricFlow names them (<entity>__<dimension>), its measures, and, through
each foreign entity, the dimensions of the model that entity is primary in
(customer__segment on bookings). A simple metric is its measure's
aggregate; a ratio, of two such whose measures are both the model's. Other
metrics stay the semantic layer's alone, and the dataset's description says
which.
"""
import hashlib
import json
import logging
import os
import time

import yaml

logging.basicConfig(level=logging.INFO, format="superset-sync: %(message)s")
log = logging.getLogger("superset-sync")

DATABASE = "Lakehouse (Trino)"
MARK = "storscale_semantic"   # in a dataset's extra: the project and model it was made from
AGG = {"sum": "SUM({})", "count": "COUNT({})", "count_distinct": "COUNT(DISTINCT {})", "average": "AVG({})",
       "min": "MIN({})", "max": "MAX({})", "sum_boolean": "SUM(CAST({} AS INTEGER))", "median": "approx_percentile({}, 0.5)"}


def q(name):
    return '"' + str(name).replace('"', '""') + '"'


def relation(sm):
    r = sm.get("node_relation") or {}
    return ".".join(q(r[k]) for k in ("database", "schema_name", "alias") if r.get(k))


def primary(sm):
    return next((e for e in sm.get("entities") or [] if e.get("type") in ("primary", "natural", "unique")), None)


def name_of(ref):
    return ref.get("name") if isinstance(ref, dict) else ref


def dataset(project, sm, models, metrics):
    """The dataset of one semantic model: its SQL, columns and metrics, and
    the metrics left to the semantic layer."""
    prim = primary(sm)
    prefix = prim["name"] if prim else sm["name"]
    cols, select = [], []
    for d in sm.get("dimensions") or []:
        col = f"{prefix}__{d['name']}"
        select.append(f"{d.get('expr') or q(d['name'])} AS {q(col)}")
        cols.append({"column_name": col, "is_dttm": d.get("type") == "time", "description": d.get("description", "")})
    measures = {}
    for m in sm.get("measures") or []:
        select.append(f"{m.get('expr') or q(m['name'])} AS {q(m['name'])}")
        cols.append({"column_name": m["name"], "is_dttm": False, "description": m.get("description", "")})
        measures[m["name"]] = m
    joins, outer = [], [f"b.{q(c['column_name'])}" for c in cols]
    for e in sm.get("entities") or []:
        if e.get("type") != "foreign":
            continue
        target = next((o for o in models.values() if o is not sm and (primary(o) or {}).get("name") == e["name"]), None)
        if not target:
            continue
        fk = f"__fk_{e['name']}"
        select.append(f"{e.get('expr') or q(e['name'])} AS {q(fk)}")
        tp = primary(target)
        inner = [f"{tp.get('expr') or q(tp['name'])} AS {q('__pk')}"]
        for d in target.get("dimensions") or []:
            col = f"{e['name']}__{d['name']}"
            inner.append(f"{d.get('expr') or q(d['name'])} AS {q(col)}")
            outer.append(f"j_{e['name']}.{q(col)}")
            cols.append({"column_name": col, "is_dttm": d.get("type") == "time", "description": d.get("description", "")})
        joins.append(f"LEFT JOIN (SELECT {', '.join(inner)} FROM {relation(target)}) j_{e['name']} ON b.{q(fk)} = j_{e['name']}.{q('__pk')}")
    sql = f"SELECT {', '.join(outer)}\nFROM (SELECT {', '.join(select)} FROM {relation(sm)}) b" + "".join("\n" + j for j in joins)

    def simple(metric):
        if metric.get("type") != "simple":
            return None
        m = measures.get(name_of((metric.get("type_params") or {}).get("measure")))
        return AGG.get(m.get("agg"), "{}").format(q(m["name"])) if m else None
    out, left = [], []
    for metric in metrics.values():
        expr = simple(metric)
        if expr is None and metric.get("type") == "ratio":
            tp = metric.get("type_params") or {}
            num, den = metrics.get(name_of(tp.get("numerator"))), metrics.get(name_of(tp.get("denominator")))
            n, d = (simple(num) if num else None), (simple(den) if den else None)
            if n and d:
                expr = f"CAST({n} AS DOUBLE) / NULLIF({d}, 0)"
        if expr:
            out.append({"metric_name": metric["name"], "expression": expr, "description": metric.get("description", "")})
        elif any(x in str(metric) for x in measures):   # on this model, but not one a dataset can say
            left.append(metric["name"])
    desc = f"{sm.get('description', '')} From the semantic model of {project}, kept by the platform: don't change it here.".strip()
    if left:
        desc += f" Ask the semantic layer for {', '.join(sorted(left))}."
    return {"project": project, "model": sm["name"], "schema": (sm.get("node_relation") or {}).get("schema_name", ""),
            "table_name": f"{sm['name']} (semantic)", "sql": sql, "description": desc, "columns": cols, "metrics": out}


def datasets(projects):
    """Every dataset the models make. projects: {name: {"docs": [...], "uses": [...]}}
    (each model's documents without its terms)."""
    out = []
    for p, info in sorted(projects.items()):
        own = [d for d in info["docs"] if isinstance(d, dict)]
        shared = [d for u in info.get("uses") or [] for d in projects.get(u, {}).get("docs", []) if isinstance(d, dict)]
        models = {d["semantic_model"]["name"]: d["semantic_model"] for d in own + shared if "semantic_model" in d}
        metrics = {d["metric"]["name"]: d["metric"] for d in own + shared if "metric" in d}
        for d in own:
            if "semantic_model" in d:
                out.append(dataset(p, d["semantic_model"], models, metrics))
    return out


# --- Superset's side ----------------------------------------------------------------------

def read_models():
    import boto3
    s3 = boto3.client("s3", endpoint_url=os.environ.get("STORE_URL", "http://buckets:9000"), region_name="us-east-1",
                      aws_access_key_id=os.environ["PLATFORM_STORE_ACCESS_KEY"], aws_secret_access_key=os.environ["PLATFORM_STORE_SECRET_KEY"])
    bucket = os.environ.get("STORE_BUCKET", "storscale-platform")
    out = {}
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix="semantic/"):
        for o in page.get("Contents", []):
            name = o["Key"][len("semantic/"):].removesuffix(".yaml")
            try:
                text = s3.get_object(Bucket=bucket, Key=o["Key"])["Body"].read().decode()
                docs = [d for d in yaml.safe_load_all(text) if isinstance(d, dict) and "terms" not in d]
                spec = json.loads(s3.get_object(Bucket=bucket, Key=f"projects/{name}.json")["Body"].read()).get("spec") or {}
            except Exception as e:  # noqa: BLE001 (a model being written, a project gone)
                log.warning("%s: %s", name, e)
                continue
            out[name] = {"docs": docs, "uses": (spec.get("semantic") or {}).get("uses") or []}
    return out


def apply(wanted):
    """Superset's datasets as wanted: made or changed, and those it made before and isn't wanted, removed."""
    from superset import db
    from superset.connectors.sqla.models import SqlaTable, SqlMetric, TableColumn
    from superset.models.core import Database
    database = db.session.query(Database).filter_by(database_name=DATABASE).one()
    have = {}
    for t in db.session.query(SqlaTable).filter(SqlaTable.database_id == database.id).all():
        mark = json.loads(t.extra or "{}").get(MARK)
        if mark:
            have[(mark["project"], mark["model"])] = t
    for w in wanted:
        t = have.pop((w["project"], w["model"]), None)
        extra = json.dumps({MARK: {"project": w["project"], "model": w["model"], "hash": hashlib.sha256(json.dumps(w, sort_keys=True).encode()).hexdigest()}})
        if t is not None and t.extra == extra:
            continue
        if t is None:
            t = SqlaTable(table_name=w["table_name"], database=database, schema=w["schema"])
            db.session.add(t)
        t.sql, t.description, t.extra, t.table_name, t.schema = w["sql"], w["description"], extra, w["table_name"], w["schema"]
        t.columns = [TableColumn(column_name=c["column_name"], is_dttm=c["is_dttm"], description=c["description"],
                                 groupby=True, filterable=True) for c in w["columns"]]
        t.metrics = [SqlMetric(metric_name=m["metric_name"], expression=m["expression"], description=m["description"]) for m in w["metrics"]]
        log.info("dataset %s of %s: %d columns, metrics %s", w["table_name"], w["project"], len(w["columns"]),
                 ", ".join(m["metric_name"] for m in w["metrics"]) or "none")
    for (project, model), t in have.items():
        db.session.delete(t)
        log.info("dataset %s of %s: its model is gone, removed", t.table_name, project)
    db.session.commit()


def main():
    from superset.app import create_app
    app = create_app()
    last = None
    while True:
        try:
            wanted = datasets(read_models())
            digest = hashlib.sha256(json.dumps(wanted, sort_keys=True).encode()).hexdigest()
            if digest != last:
                with app.app_context():
                    apply(wanted)
                last = digest
        except Exception as e:  # noqa: BLE001 (Superset or the bucket starting: the next round)
            log.warning("round: %s", e)
            last = None
        time.sleep(int(os.environ.get("SYNC_EVERY", "60")))


if __name__ == "__main__":
    main()
