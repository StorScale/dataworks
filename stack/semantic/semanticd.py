"""semanticd: StorScale Platform's semantic layer, and its MCP server for agents.

Each project can have a semantic model: MetricFlow's YAML (semantic models
and metrics) over its tables, kept in the project store as
semantic/<project>.yaml. People, dashboards and agents then ask for metrics
("revenue by region"), not SQL. semanticd compiles each request to Trino SQL
with MetricFlow, and runs it in Trino *as whoever asked*, with their own
Keycloak token, so Ranger's policies, row filters and masks apply to agents
exactly as they do to people. Each query carries a comment naming the agent
(the token's client) and the person, which Ranger's audit log keeps.

Two ways in, both signed in with Keycloak:
  /api/...   the platform (platformd), with the signed-in person's token
  /mcp       agents, over MCP (streamable HTTP); an OAuth resource server,
             whose tokens must be for it (their audience names its URL)
"""
import hashlib
import json
import logging
import os
import re
import tempfile
import time
from contextlib import suppress
from dataclasses import dataclass

import boto3
import jwt
import trino
import yaml
from botocore.exceptions import ClientError
from starlette.requests import Request
from starlette.responses import JSONResponse

from mcp.server.auth.middleware.auth_context import get_access_token
from mcp.server.auth.provider import AccessToken, TokenVerifier
from mcp.server.auth.settings import AuthSettings
from mcp.server.mcpserver import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from mcp.server.transport_security import TransportSecuritySettings
from metricflow.engine.metricflow_engine import MetricFlowEngine, MetricFlowQueryRequest
from metricflow.protocols.sql_client import SqlEngine
from metricflow.sql.render.trino import TrinoSqlPlanRenderer
from metricflow_semantic_interfaces.parsing.dir_to_model import parse_directory_of_yaml_files_to_semantic_manifest
from metricflow_semantic_interfaces.validations.semantic_manifest_validator import SemanticManifestValidator
from metricflow_semantics.model.semantic_manifest_lookup import SemanticManifestLookup

logging.basicConfig(level=logging.INFO, format="semanticd: %(message)s")
log = logging.getLogger("semanticd")

env = os.environ
ISSUER = env["KEYCLOAK_URL"].rstrip("/") + "/realms/" + env.get("KEYCLOAK_REALM", "lakehouse")
JWKS = env.get("KEYCLOAK_DIRECT_URL", env["KEYCLOAK_URL"]).rstrip("/") + "/realms/" + env.get("KEYCLOAK_REALM", "lakehouse") + "/protocol/openid-connect/certs"
MCP_URL = env["MCP_URL"]  # this server's MCP endpoint, as agents see it: its tokens' audience
PLATFORM_URL = env.get("PLATFORM_URL") or MCP_URL.removesuffix("/mcp")  # the platform's pages, for what an answer cites
TRINO_AUDIENCE = env.get("TRINO_AUDIENCE", "lakehouse")
STORE_BUCKET = env.get("STORE_BUCKET", "storscale-platform")
MAX_ROWS = 1000

jwks = jwt.PyJWKClient(JWKS, cache_keys=True, lifespan=300)
store = boto3.client("s3", endpoint_url=env.get("STORE_URL", "http://buckets:9000"), region_name="us-east-1",
                     aws_access_key_id=env["STORE_ACCESS_KEY"], aws_secret_access_key=env["STORE_SECRET_KEY"])


# --- Who's asking -----------------------------------------------------------------

@dataclass
class Caller:
    token: str     # their Keycloak access token, which Trino accepts
    username: str  # preferred_username: who Trino runs the query as
    agent: str     # the token's client (azp): the app or agent asking
    groups: list


def verify(token: str, audience: str) -> Caller:
    """A Keycloak access token for this audience, checked; or ValueError."""
    try:
        key = jwks.get_signing_key_from_jwt(token).key
        claims = jwt.decode(token, key, algorithms=["RS256"], issuer=ISSUER, audience=audience,
                            options={"require": ["exp", "iss", "aud"]})
    except jwt.PyJWTError as e:
        raise ValueError(str(e)) from e
    if TRINO_AUDIENCE not in (claims["aud"] if isinstance(claims["aud"], list) else [claims["aud"]]):
        raise ValueError(f"the token isn't for Trino too (its audience lacks {TRINO_AUDIENCE})")
    return Caller(token, claims.get("preferred_username", ""), claims.get("azp", ""), claims.get("groups", []))


class KeycloakVerifier(TokenVerifier):
    """MCP's resource-server check: the token must be for this MCP server."""

    async def verify_token(self, token: str) -> AccessToken | None:
        try:
            c = verify(token, MCP_URL)
        except ValueError as e:
            log.info("MCP: refused a token: %s", e)
            return None
        claims = jwt.decode(token, options={"verify_signature": False})
        return AccessToken(token=token, client_id=c.agent, scopes=claims.get("scope", "").split(),
                           expires_at=claims.get("exp"), resource=MCP_URL, subject=c.username, claims=claims)


def mcp_caller() -> Caller:
    at = get_access_token()
    if at is None:
        raise PermissionError("not signed in")
    return verify(at.token, MCP_URL)


# --- Projects, and their semantic models -----------------------------------------------

def get_json(key):
    try:
        return json.loads(store.get_object(Bucket=STORE_BUCKET, Key=key)["Body"].read())
    except ClientError as e:
        if e.response["Error"]["Code"] in ("NoSuchKey", "404"):
            return None
        raise


def projects_of(c: Caller):
    """The projects the caller is a member of (by the operator's status), with their role."""
    out = {}
    for page in store.get_paginator("list_objects_v2").paginate(Bucket=STORE_BUCKET, Prefix="status/"):
        for o in page.get("Contents", []):
            st = get_json(o["Key"]) or {}
            for m in st.get("members", []):
                if m["username"] == c.username:
                    out[st["name"]] = m["role"]
    return out


def model_yaml(project):
    try:
        return store.get_object(Bucket=STORE_BUCKET, Key=f"semantic/{project}.yaml")["Body"].read().decode()
    except ClientError as e:
        if e.response["Error"]["Code"] in ("NoSuchKey", "404"):
            return None
        raise


class CompileOnlyTrino:
    """MetricFlow's SQL client, for compiling only: semanticd runs the SQL itself."""
    sql_engine_type = property(lambda s: SqlEngine.TRINO)
    sql_plan_renderer = property(lambda s: TrinoSqlPlanRenderer())

    def query(self, *a, **k): raise NotImplementedError
    def execute(self, *a, **k): raise NotImplementedError
    def dry_run(self, *a, **k): raise NotImplementedError
    def close(self): pass
    def render_bind_parameter_key(self, k): raise NotImplementedError


def with_time_spine(docs, project):
    """MetricFlow needs a time spine declared, even when no query uses one."""
    if not any("project_configuration" in d for d in docs):
        ns = (get_json(f"projects/{project}.json") or {}).get("spec", {}).get("tables", {}).get("namespace") or project.replace("-", "_")
        docs = docs + [{"project_configuration": {"time_spines": [{
            "node_relation": {"database": "iceberg", "schema_name": ns, "alias": "time_spine_day"},
            "primary_column": {"name": "date_day", "time_granularity": "day"}}]}}]
    return docs


# --- The company's model (use case 10.1) ---------------------------------------------------
#
# A project marked semantic.shared holds a model the others build on: each
# project's model is compiled with the shared ones before its own. A model's
# `terms:` document (the business's words for what it defines: an owner, a
# definition, synonyms) is the platform's, taken out before MetricFlow reads it.

def shared_projects():
    out = []
    for page in store.get_paginator("list_objects_v2").paginate(Bucket=STORE_BUCKET, Prefix="projects/"):
        for o in page.get("Contents", []):
            p = get_json(o["Key"]) or {}
            if ((p.get("spec") or {}).get("semantic") or {}).get("shared"):
                out.append(p["metadata"]["name"])
    return sorted(out)


def split(text):
    """A model's documents for MetricFlow, and its terms."""
    try:
        docs = [d for d in yaml.safe_load_all(text or "") if d]
    except yaml.YAMLError as e:
        raise ValueError(f"not YAML: {e}") from e
    out, terms = [], []
    for d in docs:
        if not isinstance(d, dict):
            raise ValueError("each document of a model is a mapping (semantic_model, metric, terms)")
        if "terms" in d:
            terms += d["terms"] or []
        else:
            out.append(d)
    return out, terms


def defined(docs):
    """What a model's documents define, by kind: semantic models, metrics, entities, dimensions."""
    out = {"semantic_model": set(), "metric": set(), "entity": set(), "dimension": set()}
    for d in docs:
        if "semantic_model" in d:
            sm = d["semantic_model"]
            out["semantic_model"].add(sm.get("name"))
            out["entity"] |= {e.get("name") for e in sm.get("entities") or []}
            out["dimension"] |= {x.get("name") for x in sm.get("dimensions") or []}
        if "metric" in d:
            out["metric"].add(d["metric"].get("name"))
    return out


def uses(project):
    """The shared models a project's builds on (its spec's semantic.uses), those that are shared."""
    spec = (get_json(f"projects/{project}.json") or {}).get("spec") or {}
    shared = set(shared_projects())
    return [u for u in (spec.get("semantic") or {}).get("uses") or [] if u in shared and u != project]


def shared_docs(project, override=None):
    """The documents of the shared models a project builds on, override a
    (project, text) in place of one's saved model: and which project each name is from."""
    docs, owner = [], {}
    for sp in uses(project):
        text = override[1] if override and override[0] == sp else model_yaml(sp)
        if not text:
            continue
        theirs, _ = split(text)
        for d in theirs:
            if "project_configuration" in d:
                continue   # each project's own time spine
            docs.append(d)
        for kind, names in defined(theirs).items():
            if kind in ("semantic_model", "metric"):
                for n in names:
                    owner[(kind, n)] = sp
    return docs, owner


def check_terms(terms, docs):
    have = defined(docs)
    problems = []
    for t in terms:
        if not isinstance(t, dict) or not t.get("name"):
            problems.append("terms: each has a name: an entity, metric or dimension of the model")
            continue
        kind = t.get("kind") or next((k for k in ("entity", "metric", "dimension") if t["name"] in have[k]), None)
        if kind not in ("entity", "metric", "dimension") or t["name"] not in have[kind]:
            problems.append(f"terms: {t['name']} isn't an entity, metric or dimension of this model")
    if problems:
        raise ValueError("; ".join(problems))


_engines = {}  # project -> (hash of its model, engine, summary)


def compile_model(project, text, override=None):
    """MetricFlow's engine for a project's model (built on the shared ones), and
    what it offers; or ValueError, saying what's wrong."""
    own, terms = split(text)
    base, owner = shared_docs(project, override)
    clash = sorted(f"{n} (the {kind.replace('_', ' ')} of {owner[(kind, n)]})" for kind, names in defined(own).items()
                   if kind in ("semantic_model", "metric") for n in names if (kind, n) in owner)
    if clash:
        raise ValueError("the company's model has these already, so build on them rather than defining them again: " + ", ".join(clash))
    check_terms(terms, own)
    docs = with_time_spine(base + own, project)
    with tempfile.TemporaryDirectory() as d:
        for i, doc in enumerate(docs):
            with open(os.path.join(d, f"{i:03}.yaml"), "w") as f:
                yaml.safe_dump(doc, f)
        try:
            manifest = parse_directory_of_yaml_files_to_semantic_manifest(d).semantic_manifest
        except Exception as e:  # noqa: BLE001 (MetricFlow's parse errors vary)
            raise ValueError(f"MetricFlow can't read it: {e}") from e
    result = SemanticManifestValidator().validate_semantic_manifest(manifest)
    if result.errors:
        raise ValueError("; ".join(str(e.message) if hasattr(e, "message") else str(e) for e in result.errors))
    engine = MetricFlowEngine(SemanticManifestLookup(manifest), CompileOnlyTrino())
    ours = defined(own)["metric"]
    metrics = [{"name": m.name, "description": m.description or "", "type": str(m.type.value),
                "dimensions": sorted({d.granularity_free_dunder_name for d in m.dimensions} | {"metric_time"}),
                **({} if m.name in ours else {"from": owner.get(("metric", m.name), "")})}
               for m in engine.list_metrics()]
    tables = sorted(f"{sm.node_relation.database}.{sm.node_relation.schema_name}.{sm.node_relation.alias}"
                    for sm in manifest.semantic_models)
    return engine, {"metrics": metrics, "tables": tables, "terms": terms, "builtOn": sorted(set(owner.values()))}


def users_of(shared):
    """The projects whose models build on a shared one."""
    out = []
    for page in store.get_paginator("list_objects_v2").paginate(Bucket=STORE_BUCKET, Prefix="projects/"):
        for o in page.get("Contents", []):
            p = get_json(o["Key"]) or {}
            if shared in (((p.get("spec") or {}).get("semantic") or {}).get("uses") or []):
                out.append(p["metadata"]["name"])
    return sorted(out)


def broken_by(project, text):
    """The projects building on a shared model whose models its new text would break, and why."""
    out = {}
    for other in users_of(project):
        mine = model_yaml(other)
        if not mine:
            continue
        try:
            compile_model(other, mine, override=(project, text))
        except ValueError as e:
            out[other] = str(e)[:300]
    return out


def models_digest(project, text):
    h = hashlib.sha256(text.encode())
    for sp in uses(project):
        h.update(b"\0" + sp.encode() + b"\0" + (model_yaml(sp) or "").encode())
    return h.hexdigest()


def engine_for(project):
    text = model_yaml(project)
    if text is None:
        raise LookupError(f"project {project} has no semantic model")
    digest = models_digest(project, text)   # the shared models' changes count too
    cached = _engines.get(project)
    if cached and cached[0] == digest:
        return cached[1], cached[2]
    engine, summary = compile_model(project, text)
    _engines[project] = (digest, engine, summary)
    return engine, summary


# --- Queries -------------------------------------------------------------------------------

def compile_query(project, metrics, group_by=(), where=(), order_by=(), limit=None, cap=True):
    """The SQL of a metric query. cap: at most MAX_ROWS (every query that runs
    here); without it, the whole answer (a flow's metric step, which dbt
    materialises)."""
    engine, _ = engine_for(project)
    try:
        req = MetricFlowQueryRequest.create(metric_names=list(metrics), group_by_names=list(group_by) or None,
                                            where_constraints=list(where) or None, order_by_names=list(order_by) or None,
                                            limit=min(int(limit or MAX_ROWS), MAX_ROWS) if cap else None)
        return engine.explain(req).sql_statement.sql
    except Exception as e:  # noqa: BLE001 (MetricFlow's query errors vary)
        raise ValueError(f"MetricFlow can't answer that: {e}") from e


def namespace_of(project):
    tables = (get_json(f"projects/{project}.json") or {}).get("spec", {}).get("tables", {})
    return tables.get("catalog") or "iceberg", tables.get("namespace") or project.replace("-", "_")


def run(c: Caller, project, sql, metrics, limit=MAX_ROWS):
    """Run the SQL in Trino as the caller, in the project's namespace (so a
    table's own name is enough). The leading comment names the agent and the
    person, so Ranger's audit log has both."""
    catalog, schema = namespace_of(project)
    note = f"-- storscale: agent={c.agent} user={c.username} project={project} metrics={','.join(metrics)}\n"
    conn = trino.dbapi.connect(host=env.get("TRINO_HOST", "trino"), port=8443, http_scheme="https",
                               verify=env.get("TRINO_CA", "/tls/cert.pem"), user=c.username,
                               auth=trino.auth.JWTAuthentication(c.token), source=f"storscale-semantic/{c.agent}",
                               catalog=catalog, schema=schema,
                               # The project's limits apply (Trino's resource group for it).
                               client_tags=[f"project:{project}"])
    cur = conn.cursor()
    try:
        cur.execute(note + sql)
        rows = cur.fetchmany(limit)
        cols = [d[0] for d in cur.description]
        types = [d[1] for d in cur.description]
    finally:
        with suppress(Exception):
            conn.close()
    return {"columns": cols, "types": types,
            "rows": [[v if isinstance(v, (int, float, str, type(None))) else str(v) for v in r] for r in rows]}


def answer(c: Caller, project, metrics, group_by=(), where=(), order_by=(), limit=None, run_it=True, cap=True):
    if not metrics:
        raise ValueError("ask for at least one metric")
    sql = compile_query(project, metrics, group_by, where, order_by, limit, cap=cap or run_it)
    out = {"project": project, "sql": sql}
    if run_it:
        started = time.time()
        try:
            out.update(run(c, project, sql, metrics))
        except trino.exceptions.TrinoUserError as e:
            raise PermissionError(e.message) if e.error_name == "PERMISSION_DENIED" else ValueError(e.message) from e
        log.info("query: %s for %s, project %s, metrics %s by %s: %d rows in %.1fs", c.agent, c.username, project,
                 ",".join(metrics), ",".join(group_by) or "-", len(out["rows"]), time.time() - started)
    return out


# --- The MCP server -----------------------------------------------------------------------

def tool(fn):
    """An MCP tool whose refusals and mistakes reach the agent, saying what's wrong."""
    import functools

    @functools.wraps(fn)
    def wrapped(*a, **k):
        try:
            return fn(*a, **k)
        except (PermissionError, LookupError, ValueError) as e:
            raise ToolError(str(e)) from e
    return wrapped


mcp = MCPServer(
    "StorScale Platform: metrics",
    instructions="Answers questions with the platform's governed definitions: the company's business terms, the "
                 "projects' metrics, and the data products. Start from what the question is about (search_terms), "
                 "then the projects you can use, their metrics and the dimensions to group them by, then query; "
                 "a data product's rows come from query_product. Every query runs as you, under your access rules: "
                 "rows you may not see are left out, and masked columns stay masked. Every answer says what it used "
                 "(its definitions, terms and products, with links): name them to the person. When no term, metric "
                 "or product covers a question, say so rather than guessing.",
    token_verifier=KeycloakVerifier(),
    auth=AuthSettings(issuer_url=ISSUER, resource_server_url=MCP_URL, validate_token_resource=False),
)


@mcp.tool()
@tool
def list_projects() -> list[dict]:
    """The projects you're a member of, with your role in each, and whether each has metrics."""
    c = mcp_caller()
    return [{"project": p, "role": role, "has_metrics": model_yaml(p) is not None} for p, role in sorted(projects_of(c).items())]


@mcp.tool()
@tool
def list_metrics(project: str) -> dict:
    """A project's metrics: each one's name, description, and the dimensions it can be grouped or filtered by."""
    c = mcp_caller()
    if project not in projects_of(c):
        raise PermissionError(f"you're not a member of {project}")
    return engine_for(project)[1]


@mcp.tool()
@tool
def query_metrics(project: str, metrics: list[str], group_by: list[str] = [], where: list[str] = [],  # noqa: B006
                  order_by: list[str] = [], limit: int = 100) -> dict:  # noqa: B006
    """Compute metrics, optionally grouped by dimensions (e.g. order__region, metric_time__month) and
    filtered, e.g. where=["{{ Dimension('order__region') }} = 'EU'"]. order_by takes metric or dimension
    names, with "-" for descending. Runs as you: your row filters and masks apply."""
    c = mcp_caller()
    if project not in projects_of(c):
        raise PermissionError(f"you're not a member of {project}")
    out = answer(c, project, metrics, group_by, where, order_by, limit)
    out["used"] = cite(project, metrics, group_by)
    return out


@mcp.tool()
@tool
def list_terms() -> dict:
    """The company's business terms (entities, metrics and dimensions defined once for everyone): each
    with its definition, owner, synonyms, the tables it comes from, and the projects using it."""
    mcp_caller()
    return {"terms": all_terms()}


@mcp.tool()
@tool
def describe_term(term: str) -> dict:
    """One business term, by name or synonym: its definition, owner, kind, the metrics on it, and where it's used."""
    mcp_caller()
    for t in all_terms():
        if term.lower() in {t["name"].lower(), t["label"].lower()} | {x.lower() for x in t["synonyms"]}:
            return t
    raise LookupError(f"no business term is called {term}; list_terms has them all")


@mcp.tool()
@tool
def search_terms(question: str) -> dict:
    """The business terms a question is about, by their names and synonyms. When none match, say so to the
    person rather than answering from guesses: the platform defines what the business means, and only that
    is governed."""
    mcp_caller()
    found = matching_terms(question)
    out = {"terms": found}
    if not found:
        out["note"] = "No governed definition covers this question. Tell the person so, and which terms exist (list_terms), rather than guessing."
    return out


@mcp.tool()
@tool
def list_products() -> dict:
    """The data products: tables a project publishes under a contract, certified first. Each with its producer,
    owner, version, terms, contract (columns kept, freshness, checks), consumers, and whether it's certified."""
    mcp_caller()
    return {"products": all_products()}


@mcp.tool()
@tool
def describe_product(project: str, product: str) -> dict:
    """One data product, by its producing project and name."""
    mcp_caller()
    for pr in all_products():
        if pr["project"] == project and pr["name"] == product:
            return pr
    raise LookupError(f"{project} has no product called {product}; list_products has them all")


@mcp.tool()
@tool
def query_product(project: str, product: str, columns: list[str] = [], where: str = "", limit: int = 100) -> dict:  # noqa: B006
    """Rows of a data product's table, as you: a reader of the producing project, or of a project it was shared
    with, under the producer's rules (its row filters and masks). Anyone else is refused: the person's project
    can ask for the product on the platform's Products page. columns: a subset (default all); where: a SQL
    condition on them."""
    c = mcp_caller()
    pr = describe_product(project, product)
    cols = ", ".join('"' + x.replace('"', '""') + '"' for x in columns) or "*"
    table = ".".join('"' + x + '"' for x in pr["table"].split("."))
    sql = f"SELECT {cols} FROM {table}" + (f" WHERE {where}" if where.strip() else "") + f" LIMIT {min(int(limit or 100), MAX_ROWS)}"
    try:
        out = run(c, project, sql, [f"product:{product}"], limit=min(int(limit or 100), MAX_ROWS))
    except trino.exceptions.TrinoUserError as e:
        if e.error_name == "PERMISSION_DENIED":
            raise PermissionError(f"{e.message}. {c.username} isn't a reader of {project} or of a project {product} was shared with; "
                                  f"their project can ask for it at {pr['link']}") from e
        raise ValueError(e.message) from e
    out["sql"] = sql
    out["used"] = {"products": [{k: pr[k] for k in ("project", "name", "certified", "owner", "version", "link")}], "terms": [
        {k: t[k] for k in ("name", "label", "kind", "definition", "owner", "link")} for t in all_terms() if t["name"] in pr["terms"]]}
    return out


@mcp.tool()
@tool
def explain_query(project: str, metrics: list[str], group_by: list[str] = [], where: list[str] = []) -> str:  # noqa: B006
    """The SQL a query would run in Trino, without running it."""
    c = mcp_caller()
    if project not in projects_of(c):
        raise PermissionError(f"you're not a member of {project}")
    return answer(c, project, metrics, group_by, where, run_it=False)["sql"]


# --- The platform's API (platformd calls it with the signed-in person's token) ----------------

def api(fn):
    async def handler(request: Request):
        auth = request.headers.get("authorization", "")
        if not auth.lower().startswith("bearer "):
            return JSONResponse({"error": "not signed in"}, status_code=401)
        try:
            c = verify(auth[7:], TRINO_AUDIENCE)
            body = await request.json() if request.method in ("POST", "PUT") else {}
            return JSONResponse(fn(c, request.path_params.get("project", ""), body, request))
        except PermissionError as e:
            return JSONResponse({"error": str(e)}, status_code=403)
        except LookupError as e:
            return JSONResponse({"error": str(e)}, status_code=404)
        except ValueError as e:
            return JSONResponse({"error": str(e)}, status_code=422)
    return handler


def member(c, project):
    role = projects_of(c).get(project)
    if not role:
        raise PermissionError(f"you're not a member of {project}")
    return role


def get_model(c, project, body, request):
    member(c, project)
    text = model_yaml(project)
    if text is None:
        return {"project": project, "yaml": "", "metrics": [], "tables": []}
    try:
        summary = engine_for(project)[1]
    except ValueError as e:
        return {"project": project, "yaml": text, "metrics": [], "tables": [], "error": str(e)}
    return {"project": project, "yaml": text, **summary}


def put_model(c, project, body, request):
    if member(c, project) != "editor":
        raise PermissionError(f"only {project}'s editors change its semantic model")
    text = body.get("yaml", "")
    _, summary = compile_model(project, text)  # ValueError: what's wrong
    refuse_breaking(project, text)
    store.put_object(Bucket=STORE_BUCKET, Key=f"semantic/{project}.yaml", Body=text.encode(), ContentType="application/yaml")
    _engines.pop(project, None)
    log.info("model: %s saved %s's semantic model (%d metrics)", c.username, project, len(summary["metrics"]))
    return {"project": project, "yaml": text, **summary}


def check_model(c, project, body, request):
    """Whether a model is one, not saved (a plan of the project's repo, use case 7.4): its members (and pipelines)."""
    member(c, project)
    _, summary = compile_model(project, body.get("yaml", ""))  # ValueError: what's wrong
    refuse_breaking(project, body.get("yaml", ""))
    return {"project": project, **summary}


def refuse_breaking(project, text):
    """A shared model's change may not break the projects that build on it."""
    if project not in shared_projects():
        return
    broken = broken_by(project, text)
    if broken:
        raise ValueError("it would break the models of " + "; ".join(f"{p} ({why})" for p, why in sorted(broken.items())))


def tables_of(term, docs):
    """The tables a term comes from: an entity's or a dimension's semantic
    models, a metric's measures' (and the metrics it's made of)."""
    models = {d["semantic_model"]["name"]: d["semantic_model"] for d in docs if "semantic_model" in d}
    metrics = {d["metric"]["name"]: d["metric"] for d in docs if "metric" in d}
    rel = lambda sm: ".".join(str(sm.get("node_relation", {}).get(k, "")) for k in ("database", "schema_name", "alias"))
    out, kind, name = set(), term.get("kind"), term["name"]
    if kind in (None, "entity", "dimension"):
        for sm in models.values():
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
        for sm in models.values():
            if measure and any(x.get("name") == measure for x in sm.get("measures") or []):
                out.add(rel(sm))
        for k in ("numerator", "denominator"):
            ref = tp.get(k)
            walk(ref.get("name") if isinstance(ref, dict) else ref)
        for ref in tp.get("metrics") or []:
            walk(ref.get("name") if isinstance(ref, dict) else ref)
    if kind in (None, "metric"):
        walk(name)
    return sorted(out)


def strings(x):
    if isinstance(x, dict):
        return set().union(*(strings(v) for v in x.values())) if x else set()
    if isinstance(x, list):
        return set().union(*(strings(v) for v in x)) if x else set()
    return {x} if isinstance(x, str) else set()


def get_terms(c, project, body, request):
    """The company's terms (every shared project's), to anyone signed in: each
    with its definition, owner and synonyms, the tables it comes from, the
    metrics on it (an entity's), and the projects whose models use it."""
    models = {}
    for page in store.get_paginator("list_objects_v2").paginate(Bucket=STORE_BUCKET, Prefix="semantic/"):
        for o in page.get("Contents", []):
            name = o["Key"][len("semantic/"):].removesuffix(".yaml")
            with suppress(ValueError):
                models[name] = split(model_yaml(name) or "")
    out = []
    builders = {sp: users_of(sp) for sp in shared_projects()}
    for sp in builders:
        docs, terms = models.get(sp, ([], []))
        have = defined(docs)
        for t in terms:
            if not isinstance(t, dict) or not t.get("name"):
                continue
            kind = t.get("kind") or next((k for k in ("entity", "metric", "dimension") if t["name"] in have[k]), "")
            on = sorted(d["metric"]["name"] for d in docs if "metric" in d and kind == "entity"
                        and set(tables_of({"name": d["metric"]["name"], "kind": "metric"}, docs)) & set(tables_of({**t, "kind": kind}, docs)))
            used = sorted(p for p in builders[sp] if t["name"] in strings(models.get(p, ([], []))[0]))
            out.append({"name": t["name"], "label": t.get("label") or t["name"].replace("_", " ").capitalize(), "kind": kind,
                        "definition": t.get("definition", ""), "owner": t.get("owner", ""), "synonyms": t.get("synonyms") or [],
                        "project": sp, "tables": tables_of({**t, "kind": kind}, docs), "metrics": on, "usedBy": used})
    return {"terms": sorted(out, key=lambda x: x["label"].lower())}


# --- The business layer, for assistants (use case 10.4) --------------------------------------

def all_specs():
    out = []
    for page in store.get_paginator("list_objects_v2").paginate(Bucket=STORE_BUCKET, Prefix="projects/"):
        for o in page.get("Contents", []):
            p = get_json(o["Key"]) or {}
            if p.get("metadata", {}).get("name"):
                out.append(p)
    return out


def product_hash(pr):
    """A product's promise, as platformd hashes it to certify (its table, version and contract)."""
    b = json.dumps({"Table": pr.get("table", ""), "Version": pr.get("version", ""), "Contract": pr.get("contract")},
                   separators=(",", ":")).encode()
    return hashlib.sha256(b).hexdigest()[:16]


def all_products():
    """Every project's data products, certified first: what they are is no
    secret (the Products page shows them to anyone signed in); reading them is."""
    out = []
    for p in all_specs():
        name = p["metadata"]["name"]
        catalog, ns = (p.get("spec") or {}).get("tables", {}).get("catalog") or "iceberg", (p.get("spec") or {}).get("tables", {}).get("namespace") or name.replace("-", "_")
        for pr in (p.get("spec") or {}).get("products") or []:
            cert = pr.get("certification") or {}
            certified = bool(cert) and cert.get("hash") == product_hash(pr)
            out.append({"project": name, "name": pr["name"], "table": f"{catalog}.{ns}.{pr.get('table', '')}",
                        "description": pr.get("description", ""), "owner": pr.get("owner", ""), "version": pr.get("version", ""),
                        "terms": pr.get("terms") or [], "contract": pr.get("contract") or {}, "consumers": pr.get("consumers") or [],
                        "certified": certified, "certifiedBy": cert.get("by", "") if certified else "",
                        "certifiedAt": cert.get("at", "") if certified else "",
                        "link": f"{PLATFORM_URL}/products?product={name}/{pr['name']}"})
    return sorted(out, key=lambda x: (not x["certified"], x["project"], x["name"]))


def term_view(t):
    return {**t, "link": f"{PLATFORM_URL}/terms/{t['name']}"}


def all_terms():
    return [term_view(t) for t in get_terms(None, "", {}, None)["terms"]]


def words_of(text):
    """A text's words, singular (clients and client are one word)."""
    out = set()
    for w in re.split(r"[^a-z0-9]+", (text or "").lower()):
        if len(w) > 2:
            out.add(w[:-2] if w.endswith("ies") and len(w) > 4 else w[:-1] if w.endswith("s") and len(w) > 3 else w)
    return out


def matching_terms(question):
    """The terms a question's words name (their names, labels and synonyms), best first."""
    asked = words_of(question)
    scored = []
    for t in all_terms():
        names = words_of(t["name"]) | words_of(t["label"]) | set().union(*(words_of(x) for x in t["synonyms"]), set())
        hits = len(asked & names)
        if hits:
            scored.append((-hits, t["label"].lower(), t))
    return [t for _, _, t in sorted(scored, key=lambda x: x[:2])]


def parts_of(metric, docs):
    """A metric and the metrics it's made of (a ratio's, a derived one's)."""
    metrics = {d["metric"]["name"]: d["metric"] for d in docs if "metric" in d}
    out, todo = set(), [metric]
    while todo:
        m = todo.pop()
        if m in out or m not in metrics:
            continue
        out.add(m)
        tp = metrics[m].get("type_params") or {}
        for ref in [tp.get("numerator"), tp.get("denominator")] + list(tp.get("metrics") or []):
            if ref:
                todo.append(ref.get("name") if isinstance(ref, dict) else ref)
    return out


def cite(project, metrics, group_by=()):
    """What an answer used, for the assistant to name: the metrics' definitions
    (and whose they are), the terms on them, and the products behind the tables read."""
    _, summary = engine_for(project)
    own, _ = split(model_yaml(project) or "")
    base, _ = shared_docs(project)
    docs = base + own
    used_metrics = [{"name": m["name"], "description": m["description"], "definedIn": m.get("from") or project,
                     "link": f"{PLATFORM_URL}/projects/{m.get('from') or project}/metrics"}
                    for m in summary["metrics"] if m["name"] in metrics]
    tables, made_of = set(), set()
    for m in metrics:
        tables.update(tables_of({"name": m, "kind": "metric"}, docs))
        made_of.update(parts_of(m, docs))
    entities = {g.split("__", 1)[0] for g in group_by if "__" in g}
    terms = [t for t in all_terms() if t["name"] in made_of or t["name"] in entities
             or (t["kind"] == "entity" and set(t["tables"]) & tables)]
    products = [{k: pr[k] for k in ("project", "name", "certified", "owner", "version", "link")}
                for pr in all_products() if pr["table"] in tables]
    return {"metrics": used_metrics, "terms": [{k: t[k] for k in ("name", "label", "kind", "definition", "owner", "link")} for t in terms],
            "products": products}


def post_query(c, project, body, request):
    """A metric query, run as the caller; explain: its SQL only (with limit 0,
    uncapped: the canvas's metric step, which dbt materialises in full)."""
    member(c, project)
    explain = bool(body.get("explain"))
    return answer(c, project, body.get("metrics", []), body.get("group_by", []), body.get("where", []),
                  body.get("order_by", []), body.get("limit"), run_it=not explain, cap=not (explain and body.get("limit") == 0))


def post_sql(c, project, body, request):
    """A SQL statement, run in Trino as the caller (the platform's SQL editor, and table previews)."""
    member(c, project)
    sql = (body.get("sql") or "").strip().rstrip(";")
    if not sql:
        raise ValueError("write a statement")
    if ";" in sql:
        raise ValueError("one statement at a time")
    started = time.time()
    try:
        out = run(c, project, sql, ["sql"], limit=min(int(body.get("limit") or MAX_ROWS), MAX_ROWS))
    except trino.exceptions.TrinoUserError as e:
        raise PermissionError(e.message) if e.error_name == "PERMISSION_DENIED" else ValueError(e.message) from e
    out["seconds"] = round(time.time() - started, 2)
    return out


mcp.custom_route("/api/projects/{project}/sql", methods=["POST"])(api(post_sql))
mcp.custom_route("/api/terms", methods=["GET"])(api(get_terms))
mcp.custom_route("/api/projects/{project}/semantic", methods=["GET"])(api(get_model))
mcp.custom_route("/api/projects/{project}/semantic", methods=["PUT"])(api(put_model))
mcp.custom_route("/api/projects/{project}/semantic/check", methods=["POST"])(api(check_model))
mcp.custom_route("/api/projects/{project}/semantic/query", methods=["POST"])(api(post_query))


@mcp.custom_route("/healthz", methods=["GET"])
async def healthz(request: Request):
    return JSONResponse({"ok": True})


host = re.sub(r"^https?://", "", MCP_URL).split("/")[0]
app = mcp.streamable_http_app(
    streamable_http_path="/mcp", stateless_http=True, json_response=True,
    transport_security=TransportSecuritySettings(allowed_hosts=[host, "semanticd:8000", "localhost:8000"],
                                                 allowed_origins=[MCP_URL.rsplit("/mcp", 1)[0]]))
