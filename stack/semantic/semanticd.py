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


def with_time_spine(text, project):
    """MetricFlow needs a time spine declared, even when no query uses one."""
    docs = [d for d in yaml.safe_load_all(text) if d]
    if not any("project_configuration" in d for d in docs):
        ns = (get_json(f"projects/{project}.json") or {}).get("spec", {}).get("tables", {}).get("namespace") or project.replace("-", "_")
        docs.append({"project_configuration": {"time_spines": [{
            "node_relation": {"database": "iceberg", "schema_name": ns, "alias": "time_spine_day"},
            "primary_column": {"name": "date_day", "time_granularity": "day"}}]}})
    return docs


_engines = {}  # project -> (hash of its model, engine, summary)


def compile_model(project, text):
    """MetricFlow's engine for a model, and what it offers; or ValueError, saying what's wrong."""
    try:
        docs = with_time_spine(text, project)
    except yaml.YAMLError as e:
        raise ValueError(f"not YAML: {e}") from e
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
    metrics = [{"name": m.name, "description": m.description or "", "type": str(m.type.value),
                "dimensions": sorted({d.granularity_free_dunder_name for d in m.dimensions} | {"metric_time"})}
               for m in engine.list_metrics()]
    tables = sorted(f"{sm.node_relation.database}.{sm.node_relation.schema_name}.{sm.node_relation.alias}"
                    for sm in manifest.semantic_models)
    return engine, {"metrics": metrics, "tables": tables}


def engine_for(project):
    text = model_yaml(project)
    if text is None:
        raise LookupError(f"project {project} has no semantic model")
    digest = hashlib.sha256(text.encode()).hexdigest()
    cached = _engines.get(project)
    if cached and cached[0] == digest:
        return cached[1], cached[2]
    engine, summary = compile_model(project, text)
    _engines[project] = (digest, engine, summary)
    return engine, summary


# --- Queries -------------------------------------------------------------------------------

def compile_query(project, metrics, group_by=(), where=(), order_by=(), limit=None):
    engine, _ = engine_for(project)
    try:
        req = MetricFlowQueryRequest.create(metric_names=list(metrics), group_by_names=list(group_by) or None,
                                            where_constraints=list(where) or None, order_by_names=list(order_by) or None,
                                            limit=min(int(limit or MAX_ROWS), MAX_ROWS))
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
    finally:
        with suppress(Exception):
            conn.close()
    return {"columns": cols, "rows": [[v if isinstance(v, (int, float, str, type(None))) else str(v) for v in r] for r in rows]}


def answer(c: Caller, project, metrics, group_by=(), where=(), order_by=(), limit=None, run_it=True):
    if not metrics:
        raise ValueError("ask for at least one metric")
    sql = compile_query(project, metrics, group_by, where, order_by, limit)
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
    instructions="Answers questions with the platform's governed metrics. List the projects you can use, "
                 "then their metrics and the dimensions to group them by, then query. Every query runs as "
                 "you, under your access rules: rows you may not see are left out, and masked columns stay masked.",
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
    return answer(c, project, metrics, group_by, where, order_by, limit)


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
    store.put_object(Bucket=STORE_BUCKET, Key=f"semantic/{project}.yaml", Body=text.encode(), ContentType="application/yaml")
    _engines.pop(project, None)
    log.info("model: %s saved %s's semantic model (%d metrics)", c.username, project, len(summary["metrics"]))
    return {"project": project, "yaml": text, **summary}


def check_model(c, project, body, request):
    """Whether a model is one, not saved (a plan of the project's repo, use case 7.4): its members (and pipelines)."""
    member(c, project)
    _, summary = compile_model(project, body.get("yaml", ""))  # ValueError: what's wrong
    return {"project": project, **summary}


def post_query(c, project, body, request):
    member(c, project)
    return answer(c, project, body.get("metrics", []), body.get("group_by", []), body.get("where", []),
                  body.get("order_by", []), body.get("limit"), run_it=not body.get("explain"))


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
