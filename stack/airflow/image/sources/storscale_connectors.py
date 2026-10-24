"""The platform's connectors (use case 8.4): an import's dlt resources for
a named application, from its objects and its connection's sign-in. The
import runner (storscale_import_run.py) lands them as it does any source's.
"""
import os

import dlt

# Salesforce's objects copied by their changes (the others are replaced each
# run): the field their changes are stamped in, as dlt's source reads them.
SALESFORCE_CURSORS = {
    "account": "LastModifiedDate", "campaign_member": "SystemModstamp", "event": "SystemModstamp", "opportunity": "SystemModstamp",
    "opportunity_contact_role": "SystemModstamp", "opportunity_line_item": "SystemModstamp", "task": "SystemModstamp",
}


def salesforce_credentials(secret):
    """dlt's Salesforce credentials from the connection's keys (7.6's salesforce kind)."""
    from salesforce.helpers.client import ConsumerKeySecretAuth, InstanceAuth, OrganizationIdAuth, SecurityTokenAuth
    if secret.get("sessionId"):
        return InstanceAuth(session_id=secret["sessionId"], instance_url=secret.get("instanceUrl"))
    if secret.get("consumerKey"):
        return ConsumerKeySecretAuth(user_name=secret["username"], password=secret["password"],
                                     consumer_key=secret["consumerKey"], consumer_secret=secret.get("consumerSecret"))
    if secret.get("organizationId"):
        return OrganizationIdAuth(user_name=secret["username"], password=secret["password"], organization_id=secret["organizationId"])
    return SecurityTokenAuth(user_name=secret["username"], password=secret["password"], security_token=secret.get("securityToken", ""))


def trust(ca):
    """Trust a CA's certificate as well as the usual ones (a proxy that inspects TLS)."""
    if not ca:
        return
    import tempfile

    import certifi
    with open(certifi.where()) as f:
        bundle = f.read()
    fd, path = tempfile.mkstemp(prefix="ca-", suffix=".pem")
    with os.fdopen(fd, "w") as f:
        f.write(bundle + "\n" + ca.strip() + "\n")
    os.environ["REQUESTS_CA_BUNDLE"] = path


def salesforce(im, secret, name):
    """The import's Salesforce objects as dlt resources, each into <import>_<object>:
    those copied by their changes merged on their Id, from `since` if given."""
    from salesforce import salesforce_source
    sf = im["salesforce"]
    trust(secret.get("caCertificate"))
    if secret.get("domain"):
        os.environ["SOURCES__SALESFORCE__DOMAIN"] = secret["domain"]   # login, or test (a sandbox)
    source = salesforce_source(credentials=salesforce_credentials(secret)).with_resources(*sf["objects"])
    for obj in sf["objects"]:
        res = source.resources[obj]
        hints = {"table_name": f"{name}_{obj}"}
        if obj in SALESFORCE_CURSORS:
            hints["primary_key"] = "Id"   # as Salesforce names it (dlt makes the column id)
            if sf.get("since"):
                hints["incremental"] = dlt.sources.incremental(SALESFORCE_CURSORS[obj], initial_value=f"{sf['since']}T00:00:00.000Z")
        res.apply_hints(**hints)
    return [source]


# --- Gong and HighSpot, on their REST APIs ---------------------------------------------
# Each signs in with an API key and secret (HTTP Basic). An object is a dlt
# resource into <import>_<object>, merged on its ID where it's copied by its
# changes, replaced each run otherwise.

def _api(base, key, secret):
    """A small client: Basic sign-in, waiting out the API's rate limit (429, Retry-After)."""
    import time

    import requests
    s = requests.Session()
    s.auth = (key, secret)
    s.headers.update({"Accept": "application/json", "User-Agent": "storscale-import"})

    def call(method, path, params=None, body=None, empty=()):
        for _ in range(8):
            r = s.request(method, base.rstrip("/") + path, params=params, json=body, timeout=120)
            if r.status_code == 429:
                time.sleep(min(60, float(r.headers.get("Retry-After") or 5)))
                continue
            if r.status_code in empty:   # Gong answers 404 when a filter finds nothing
                return {}
            if r.status_code >= 400:
                raise RuntimeError(f"{method} {path}: HTTP {r.status_code}: {r.text[:300]}")
            return r.json()
        raise RuntimeError(f"{method} {path}: still rate-limited after eight tries")
    return call


def gong(im, secret, name):
    """The import's Gong objects (its v2 API: https://api.gong.io/v2, or the
    tenant's address the connection gives). Calls are the public ones only."""
    g = im["gong"]
    trust(secret.get("caCertificate"))
    base = (secret.get("baseUrl") or "https://api.gong.io").rstrip("/")
    call = _api(base if base.endswith("/v2") else base + "/v2", secret["accessKey"], secret["accessKeySecret"])
    since = f"{g['since']}T00:00:00Z" if g.get("since") else "2000-01-01T00:00:00Z"

    def pages(method, path, key, params=None, body=None, empty=(404,)):
        """Every page of a list: Gong's cursor is in records.cursor, sent back as cursor."""
        params, body = dict(params or {}), (dict(body) if body is not None else None)
        while True:
            data = call(method, path, params=params if method == "GET" else None, body=body, empty=empty)
            yield from data.get(key) or []
            cursor = (data.get("records") or {}).get("cursor")
            if not cursor:
                return
            if method == "GET":
                params["cursor"] = cursor
            else:
                body["cursor"] = cursor

    @dlt.source(name=f"gong_{name}")
    def source():
        @dlt.resource(name="users", table_name=f"{name}_users", primary_key="id", write_disposition="replace")
        def users():
            yield from pages("GET", "/users", "users")

        @dlt.resource(name="calls", table_name=f"{name}_calls", primary_key="id", write_disposition="merge")
        def calls(started=dlt.sources.incremental("started", initial_value=since)):
            for c in pages("GET", "/calls", "calls", params={"fromDateTime": started.last_value}):
                if not c.get("isPrivate"):
                    yield c

        # Each new call's transcript (the calls it reads are those just copied).
        @dlt.transformer(data_from=calls, name="call_transcripts", table_name=f"{name}_call_transcripts", primary_key="callId",
                         write_disposition="merge")
        def call_transcripts(batch):
            ids = [c["id"] for c in (batch if isinstance(batch, list) else [batch])]
            for i in range(0, len(ids), 100):
                yield from pages("POST", "/calls/transcript", "callTranscripts", body={"filter": {"callIds": ids[i:i + 100]}})

        @dlt.resource(name="scorecards", table_name=f"{name}_scorecards", primary_key="scorecardId", write_disposition="replace")
        def scorecards():
            yield from pages("GET", "/settings/scorecards", "scorecards")

        @dlt.resource(name="answered_scorecards", table_name=f"{name}_answered_scorecards", primary_key="answeredScorecardId",
                      write_disposition="merge")
        def answered_scorecards(reviewed=dlt.sources.incremental("reviewTime", initial_value=since)):
            yield from pages("POST", "/stats/activity/scorecards", "answeredScorecards",
                             body={"filter": {"reviewFromDate": reviewed.last_value[:10]}})

        return users, calls, call_transcripts, scorecards, answered_scorecards

    return [source().with_resources(*g["objects"])]


def highspot(im, secret, name):
    """The import's HighSpot objects (its v1.0 API, at the region's address
    the connection gives: https://api-su2.highspot.com, say). Spots are those
    the API key may view; items, theirs."""
    trust(secret.get("caCertificate"))
    base = secret["baseUrl"].rstrip("/")
    call = _api(base if base.endswith("/v1.0") else base + "/v1.0", secret["apiKey"], secret["apiSecret"])
    page = 100

    def pages(path, params=None):
        """Every page of a list: start and limit; the last page is short."""
        start = 0
        while True:
            got = call("GET", path, params={**(params or {}), "start": start, "limit": page}).get("collection") or []
            yield from got
            if len(got) < page:
                return
            start += page

    def spot_list():
        return list(pages("/spots", {"right": "view"}))

    @dlt.resource(name="spots", table_name=f"{name}_spots", primary_key="id", write_disposition="replace")
    def spots():
        yield from spot_list()

    @dlt.resource(name="items", table_name=f"{name}_items", primary_key="id", write_disposition="replace")
    def items():
        for s in spot_list():
            for it in pages("/items", {"spot": s["id"]}):
                yield {**it, "spot_id": s["id"]}

    @dlt.resource(name="users", table_name=f"{name}_users", primary_key="id", write_disposition="replace")
    def users():
        yield from pages("/users")

    return [r for n, r in (("spots", spots), ("items", items), ("users", users)) if n in im["highspot"]["objects"]]
