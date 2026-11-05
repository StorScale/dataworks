"""Shared by the examples' setup and test scripts: Buckets, Keycloak, Ranger
and Trino helpers.

Each example's tools/setup.py describes its buckets, accounts and Ranger
policies and calls these; its tools/test.py uses the check helpers at the end.
Everything is created or updated in place, so setup is safe to run again.
"""
import json
import os
import subprocess
import sys
import tempfile
import time
from http.cookiejar import DefaultCookiePolicy

import requests

env = os.environ
STORE_BUCKET = env.get("STORE_BUCKET", "storscale-platform")   # the platform's own bucket (the chart names it)
KEYCLOAK = env.get("KEYCLOAK_URL", "http://keycloak:8080")
REALM = "lakehouse"
RANGER = "http://ranger-admin:6080"
RANGER_SERVICE = "lakehouse"
PLUGIN_USER = "trino-plugin"  # Trino's Ranger plugin signs in as this user
BUCKETS = "http://buckets:9000"
# The platform's files on a company's store (storage.external), reached at
# BUCKETS all the same: only what the platform marked as its own is changed
# there (a bucket's tag dataworks=owned, a policy's ID dataworks).
EXTERNAL = os.environ.get("STORE_EXTERNAL") == "true"
OWNER_TAG, OWNER_ID = "dataworks=owned", "dataworks"


def log(msg):
    print(f"setup: {msg}", flush=True)


def wait_for(name, url, ok=lambda r: r.status_code == 200, timeout=600, **kw):
    deadline = time.time() + timeout
    while True:
        try:
            if ok(requests.get(url, timeout=5, **kw)):
                return
        except (requests.RequestException, ValueError, KeyError):
            pass
        if time.time() > deadline:
            sys.exit(f"{name} did not come up ({url})")
        time.sleep(2)


# Browsers count http://localhost and http://*.localhost as secure, so they send
# Secure cookies there, and Keycloak marks its cookies Secure on those names.
# Python's cookie jar doesn't; this makes the scripts' sessions behave like a
# browser. (Requests copies cookies into new jars, so it's set for all of them.)
_return_ok_secure = DefaultCookiePolicy.return_ok_secure


def _localhost_is_secure(self, cookie, request):
    host = request.host.split(":")[0]
    return host == "localhost" or host.endswith(".localhost") or _return_ok_secure(self, cookie, request)


DefaultCookiePolicy.return_ok_secure = _localhost_is_secure


# --- Buckets -------------------------------------------------------------------

def mc(*args, check=True):
    r = subprocess.run(["mc", "--no-color", *args], capture_output=True, text=True)
    if check and r.returncode != 0:
        # Never the credentials: `mc alias set <alias> <url> <key> <secret>`, `mc admin user add <alias> <user> <secret>`.
        shown = list(args)
        if shown[:2] == ["alias", "set"]:
            shown = shown[:4] + ["<key>", "<secret>"] * (len(shown) > 4)
        elif shown[:3] == ["admin", "user", "add"]:
            shown = shown[:5] + ["<secret>"] * (len(shown) > 5)
        shown = ["<secret>" if i and shown[i - 1] == "--secret-key" else a for i, a in enumerate(shown)]
        shown = ["<administrator>" if a == env.get("BUCKETS_ROOT_USER") else a for a in shown]   # nor its key's ID
        sys.exit(f"setup: mc {' '.join(shown)} failed:\n{r.stdout}{r.stderr}")
    return r


def bucket_rw(bucket):
    """Read and write one bucket: a policy's statements."""
    return [
        {"Effect": "Allow", "Action": ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"],
         "Resource": [f"arn:aws:s3:::{bucket}"]},
        {"Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject",
                                       "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
         "Resource": [f"arn:aws:s3:::{bucket}/*"]},
    ]


def _marked(value):
    """Whether mc's JSON (a bucket's tags, a policy's info) carries the platform's mark."""
    if isinstance(value, dict):
        if value.get("ID") == OWNER_ID or value.get("Id") == OWNER_ID or value.get("dataworks") == "owned":
            return True
        return any(_marked(v) for v in value.values())
    if isinstance(value, list):
        return any(_marked(v) for v in value)
    return False


def _mc_json(*args):
    r = mc(*args, "--json", check=False)
    try:
        return r.returncode, json.loads(r.stdout.strip().splitlines()[-1]) if r.stdout.strip() else {}
    except ValueError:
        return r.returncode, {}


def ensure_bucket(b):
    """A bucket of the platform's; on an external store, marked as its own,
    and one already there must be marked so."""
    if not EXTERNAL:   # the platform's own Buckets: everything there is its own, unmarked
        mc("mb", "--ignore-existing", f"buckets/{b}")
    elif mc("stat", f"buckets/{b}", check=False).returncode != 0:
        mc("mb", f"buckets/{b}")
        mc("tag", "set", f"buckets/{b}", OWNER_TAG)
    elif not _marked(_mc_json("tag", "list", f"buckets/{b}")[1]):
        sys.exit(f"setup: the store already has a bucket {b} that isn't the platform's (no tag {OWNER_TAG})")


def ensure_policy(name, statements):
    """A policy of the platform's; on an external store, marked with its ID,
    and one already there must be the platform's."""
    if EXTERNAL:
        code, info = _mc_json("admin", "policy", "info", "buckets", name)
        if code == 0 and info.get("status") == "success" and not _marked(info):
            sys.exit(f"setup: the store already has a policy {name} that isn't the platform's (its ID isn't {OWNER_ID})")
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"Version": "2012-10-17", "Statement": statements, **({"Id": OWNER_ID} if EXTERNAL else {})}, f)
    mc("admin", "policy", "create", "buckets", name, f.name)


def ensure_access_key(key, secret, statements):
    """On an external store, one of the platform's accounts as an access key of the administrator's
    (MinIO signing people in through LDAP makes no users of its own), its policy its own: made, or
    its secret and policy put back."""
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"Version": "2012-10-17", "Statement": statements}, f)
    if mc("admin", "user", "svcacct", "info", "buckets", key, check=False).returncode == 0:
        mc("admin", "user", "svcacct", "edit", "buckets", key, "--secret-key", secret, "--policy", f.name)
    else:
        mc("admin", "user", "svcacct", "add", "buckets", env["BUCKETS_ROOT_USER"], "--access-key", key,
           "--secret-key", secret, "--policy", f.name, "--name", key, "--description", "the platform's (DataWorks)")


def setup_buckets(buckets, policies, accounts):
    """buckets: names; policies: {name: statements}; accounts: [(access key, secret, policy)]."""
    wait_for("Buckets", f"{BUCKETS}/minio/health/live")
    mc("alias", "set", "buckets", BUCKETS, env["BUCKETS_ROOT_USER"], env["BUCKETS_ROOT_PASSWORD"])
    for b in buckets:
        ensure_bucket(b)
    for name, statements in policies.items():
        ensure_policy(name, statements)
    for user, secret, pol in accounts:
        if EXTERNAL:
            ensure_access_key(user, secret, policies[pol])
            continue
        mc("admin", "user", "add", "buckets", user, secret)
        mc("admin", "policy", "attach", "buckets", pol, "--user", user, check=False)  # fails if attached
    log(f"Buckets: buckets {', '.join(buckets)}; policies {', '.join(policies)}; "
        f"accounts {', '.join(a[0] for a in accounts)}")


# --- Keycloak -> Ranger users and groups -----------------------------------------

def keycloak_people():
    """{user: [groups]} from the realm, as an admin would sync them."""
    tok = requests.post(f"{KEYCLOAK}/realms/master/protocol/openid-connect/token", data={
        "grant_type": "password", "client_id": "admin-cli",
        "username": "admin", "password": env["KEYCLOAK_ADMIN_PASSWORD"]}, timeout=10)
    tok.raise_for_status()
    h = {"Authorization": f"Bearer {tok.json()['access_token']}"}
    base = f"{KEYCLOAK}/admin/realms/{REALM}"
    people = {}
    for u in requests.get(f"{base}/users?max=1000", headers=h, timeout=10).json():
        groups = requests.get(f"{base}/users/{u['id']}/groups", headers=h, timeout=10).json()
        people[u["username"]] = sorted(g["name"] for g in groups)
    return people


def ranger(method, path, **kw):
    r = requests.request(method, RANGER + path, auth=("admin", env["RANGER_PASSWORD"]), timeout=30,
                         headers={"Accept": "application/json"}, **kw)
    if r.status_code >= 400:
        raise RuntimeError(f"Ranger {method} {path}: {r.status_code} {r.text[:500]}")
    return r.json() if r.text.strip() else None


def sync_people(people):
    """Ranger's users and groups as Keycloak has them (Ranger's usersync does
    this against LDAP or Entra ID in a real deployment)."""
    groups = {g["name"]: g["id"] for g in ranger("GET", "/service/xusers/groups?pageSize=1000")["vXGroups"]}
    for name in sorted({g for gs in people.values() for g in gs} - set(groups)):
        groups[name] = ranger("POST", "/service/xusers/groups", json={"name": name, "description": "from Keycloak"})["id"]
    users = {u["name"]: u for u in ranger("GET", "/service/xusers/users?pageSize=1000")["vXUsers"]}
    for name, gs in sorted(people.items()):
        ids = [groups[g] for g in gs]
        if name in users:
            u = ranger("GET", f"/service/xusers/secure/users/{users[name]['id']}")
            u["groupIdList"] = ids
            ranger("PUT", f"/service/xusers/secure/users/{u['id']}", json=u)
        else:
            ranger("POST", "/service/xusers/secure/users", json={
                "name": name, "firstName": name, "password": env["RANGER_PASSWORD"] + name,
                "userRoleList": ["ROLE_USER"], "groupIdList": ids, "status": 1, "userSource": 1})
    log("Ranger: users " + ", ".join(f"{u} ({', '.join(g) or 'no group'})" for u, g in sorted(people.items())))


# --- Ranger: the Trino service and its policies -------------------------------------

def res(**kw):
    """A policy's resources: res(catalog="iceberg", schema="sales")."""
    return {k: {"values": v if isinstance(v, list) else [v], "isExcludes": False, "isRecursive": False}
            for k, v in kw.items()}


def allow(accesses, groups=(), users=()):
    return {"groups": list(groups), "users": list(users), "delegateAdmin": False,
            "accesses": [{"type": a, "isAllowed": True} for a in accesses]}


# Everyone who may use Trino at all: run queries, and act as themselves.
BASE_POLICIES = [
    {"name": "run queries", "resources": res(queryid="*"),
     "policyItems": [allow(["execute"], groups=["analysts", "engineers", "pipelines"])]},
    {"name": "act as yourself", "resources": res(trinouser="{USER}"),
     "policyItems": [allow(["impersonate"], users=["{USER}"])]},
]


def setup_ranger(policies, keep=()):
    """The plugin's user, the Trino service, and exactly these policies, beside
    the projects' (named project:..., which the platform operator keeps) and
    any whose names start with one of keep (a later step's)."""
    users = {u["name"] for u in ranger("GET", "/service/xusers/users?pageSize=1000")["vXUsers"]}
    if PLUGIN_USER not in users:
        ranger("POST", "/service/xusers/secure/users", json={
            "name": PLUGIN_USER, "firstName": "Trino plugin", "password": env["RANGER_PLUGIN_PASSWORD"],
            "userRoleList": ["ROLE_USER"], "status": 1, "userSource": 0})
    service = {
        "name": RANGER_SERVICE, "type": "trino", "description": "Trino on Buckets",
        "configs": {"username": "trino", "password": "unused",
                    "jdbc.driverClassName": "io.trino.jdbc.TrinoDriver",
                    "jdbc.url": "jdbc:trino://trino:8443",
                    # only the plugin's user may download policies, tags and users
                    "policy.download.auth.users": PLUGIN_USER,
                    "tag.download.auth.users": PLUGIN_USER,
                    "userstore.download.auth.users": PLUGIN_USER}}
    found = ranger("GET", f"/service/public/v2/api/service?serviceName={RANGER_SERVICE}")
    if found:
        ranger("PUT", f"/service/public/v2/api/service/{found[0]['id']}", json={**found[0], **service})
    else:
        ranger("POST", "/service/public/v2/api/service", json=service)
    existing = {p["name"]: p for p in ranger("GET", f"/service/public/v2/api/service/{RANGER_SERVICE}/policy")}
    wanted = {p["name"] for p in policies}
    for name, p in existing.items():  # Ranger's default policies, and any of ours since removed
        if name not in wanted and not name.startswith(("project:", *keep)):
            ranger("DELETE", f"/service/public/v2/api/policy/{p['id']}")
    for p in policies:
        body = {"service": RANGER_SERVICE, "isEnabled": True, "policyType": 0, **p}
        if p["name"] in existing:
            ranger("PUT", f"/service/public/v2/api/policy/{existing[p['name']]['id']}", json=body)
        else:
            ranger("POST", "/service/public/v2/api/policy", json=body)
    log(f"Ranger: service {RANGER_SERVICE} with {len(policies)} policies")


def setup_people_and_ranger(policies):
    # Unauthenticated: five failed sign-ins in five minutes lock Ranger's admin.
    wait_for("Ranger", f"{RANGER}/login.jsp")
    sync_people(keycloak_people())
    setup_ranger(policies)


# --- Sample data --------------------------------------------------------------------------

ORDERS = [  # (id, customer, card number, region, amount, order date)
    (1, "Ana", "4111111111111111", "EU", 120.50, "2026-09-01"),
    (2, "Ben", "5500000000000004", "US", 75.00, "2026-09-02"),
    (3, "Chloe", "340000000000009", "EU", 310.25, "2026-09-03"),
    (4, "Dev", "6011000000000004", "APAC", 42.00, "2026-09-04"),
    (5, "Eva", "3530111333300000", "EU", 18.99, "2026-09-05"),
    (6, "Finn", "4012888888881881", "US", 99.95, "2026-09-06"),
]


def load_sales_tables(user="bob", catalog="iceberg"):
    """iceberg.sales.orders (ORDERS) and iceberg.sales.payroll, written through Trino by `user`."""
    q = lambda statement: sql(user, statement, catalog=catalog, schema="sales")
    sql(user, f"CREATE SCHEMA IF NOT EXISTS {catalog}.sales", catalog=catalog)
    for t in ("orders", "payroll"):
        q(f"DROP TABLE IF EXISTS {t}")
    q("CREATE TABLE orders (id bigint, customer varchar, card_number varchar, region varchar, "
      "amount decimal(10,2), order_date date)")
    q("INSERT INTO orders VALUES " + ", ".join(f"({i}, '{c}', '{n}', '{r}', {a}, DATE '{d}')"
                                               for i, c, n, r, a, d in ORDERS))
    q("CREATE TABLE payroll (employee varchar, salary decimal(10,2))")
    q("INSERT INTO payroll VALUES ('Ana', 5000.00)")
    log(f"data: {catalog}.sales.orders ({len(ORDERS)} orders) and {catalog}.sales.payroll, written by {user}")


# --- Checks ---------------------------------------------------------------------------

failures = []


def check(name, ok, detail=""):
    print(f"{'ok  ' if ok else 'FAIL'} {name}{f'  ({detail})' if detail else ''}", flush=True)
    if not ok:
        failures.append(name)


def finish():
    print()
    if failures:
        sys.exit(f"{len(failures)} of the checks failed")
    print("all checks passed")


def token(user):
    """A Keycloak access token for one of the example's users."""
    r = requests.post(f"{KEYCLOAK}/realms/{REALM}/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "password", "client_id": "lakehouse",
        "username": user, "password": env["LAKEHOUSE_USER_PASSWORD"]})
    r.raise_for_status()
    return r.json()["access_token"]


def trino_cursor(user, catalog, schema=None, tags=None):
    """A Trino cursor for `user`, signed in with their token; tags: client
    tags, as project:<name> (the project's limits)."""
    import trino
    conn = trino.dbapi.connect(host="trino", port=8443, http_scheme="https", user=user,
                               auth=trino.auth.JWTAuthentication(token(user)), verify="/tls/cert.pem",
                               catalog=catalog, schema=schema, client_tags=tags)
    return conn.cursor()


def sql(user, statement, catalog, schema=None, tags=None):
    """Run a statement in Trino as `user`, signed in with their token."""
    cur = trino_cursor(user, catalog, schema, tags)
    cur.execute(statement)
    return cur.fetchall()


def denied(user, statement, catalog, schema=None):
    """The Trino error if Ranger refuses the statement, else None."""
    import trino
    try:
        sql(user, statement, catalog, schema)
    except trino.exceptions.TrinoUserError as e:
        if e.error_name == "PERMISSION_DENIED":
            return e.message
        raise
    return None


MAINTENANCE_TRINO_USER = "maintenance"   # platformd's table maintenance account (setup/lakehouse.py)


def wait_for_trino(timeout=180):
    """Until Trino has started and runs a query: it refuses every query until
    the platform operator has written its limits (its resource groups)."""
    wait_for("Trino", "https://trino:8443/v1/info", ok=lambda r: r.json()["starting"] is False,
             timeout=300, verify="/tls/cert.pem")
    deadline, last = time.time() + timeout, None
    while time.time() < deadline:
        try:
            if env.get("MAINTENANCE_TRINO_PASSWORD"):   # the platform's own account: no example person needed
                import trino
                trino.dbapi.connect(host="trino", port=8443, http_scheme="https", user=MAINTENANCE_TRINO_USER,
                                    auth=trino.auth.BasicAuthentication(MAINTENANCE_TRINO_USER, env["MAINTENANCE_TRINO_PASSWORD"]),
                                    verify="/tls/cert.pem", catalog="iceberg").cursor().execute("SELECT 1").fetchall()
            else:
                sql("bob", "SELECT 1", catalog="iceberg")
            return
        except Exception as e:  # noqa: BLE001 (no resource group yet, or Keycloak's token not yet)
            last = e
            time.sleep(3)
    sys.exit(f"setup: Trino runs no queries: {str(last).splitlines()[0][:200] if last else ''}")


def s3(config=None, **creds):
    """A boto3 S3 client on Buckets; config: botocore settings laid over the
    defaults (timeouts, retries)."""
    import boto3
    from botocore.config import Config
    base = Config(s3={"addressing_style": "path"})
    return boto3.client("s3", endpoint_url=BUCKETS, region_name="us-east-1",
                        config=base.merge(config) if config else base, **creds)


def s3_root(config=None):
    return s3(config=config, aws_access_key_id=env["BUCKETS_ROOT_USER"], aws_secret_access_key=env["BUCKETS_ROOT_PASSWORD"])


def service_token(client_id, secret):
    """A Keycloak access token for a client's service account (client credentials)."""
    r = requests.post(f"{KEYCLOAK}/realms/{REALM}/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": client_id, "client_secret": secret})
    r.raise_for_status()
    return r.json()["access_token"]


def s3_as(user, web_token=None):
    """Buckets credentials for a person (or, given its token, a service
    account): their Keycloak token through STS."""
    import boto3
    from botocore.config import Config
    # No RoleArn: MinIO, signing people in by their groups claim, refuses a role it doesn't know (Buckets takes either); boto3 wants one unless told not to check.
    sts = boto3.client("sts", endpoint_url=BUCKETS, region_name="us-east-1", config=Config(parameter_validation=False),
                       aws_access_key_id="unused", aws_secret_access_key="unused")
    c = sts.assume_role_with_web_identity(RoleSessionName=user,
                                          WebIdentityToken=web_token or token(user), DurationSeconds=900)["Credentials"]
    return s3(aws_access_key_id=c["AccessKeyId"], aws_secret_access_key=c["SecretAccessKey"],
              aws_session_token=c["SessionToken"])


def ensure_buckets_identity(timeout=600):
    """Make sure Buckets has its Keycloak sign-in settings loaded: people's
    Keycloak tokens must get them Buckets credentials (STS). Buckets reads
    Keycloak's OpenID configuration only as it starts, and in Kubernetes
    nothing orders the two (its pods are its operator's): if it started
    first, it never loads it. Then this starts Buckets afresh, now that
    Keycloak is up (in Kubernetes, its pods are replaced; in Compose, its
    server restarted), and waits for STS to work."""
    from botocore.exceptions import BotoCoreError, ClientError

    if EXTERNAL and env.get("STORE_STS") == "false":   # storage.external.sts: not yet
        log("the store doesn't take the platform's Keycloak tokens yet (storage.external.sts: false): people get "
            "no credentials of their own there (notebooks, pipelines) until it's given its OpenID settings")
        return
    examples = env.get("STORSCALE_EXAMPLES", "true") != "false"

    def sts():
        """True: it works; False: Buckets has no OpenID settings; None: it isn't answering."""
        try:
            if examples:
                s3_as("bob")   # credentials from his Keycloak token: what STS is for
            else:   # no example people: the pipelines' service account's token
                s3_as("service-account-airflow-pipelines", web_token=service_token("airflow-pipelines", env["AIRFLOW_PIPELINES_SECRET"]))
            return True
        except ClientError as e:
            # Its token taken, but no policy for its groups (none of the projects
            # gives one yet): the store reads Keycloak's tokens, which is the question.
            if "are defined" in e.response["Error"].get("Message", ""):
                return True
            if e.response["Error"].get("Code") in ("InvalidParameterValue", "InvalidIdentityToken"):
                return False
            raise
        except BotoCoreError:   # connection refused or reset: starting, or restarting
            return None

    deadline = time.time() + timeout
    state = sts()
    while state is None and time.time() < deadline:   # wait for it to answer at all
        time.sleep(5)
        state = sts()
    if state:
        return
    if state is None:
        sys.exit("setup: Buckets isn't answering")
    if EXTERNAL:   # a company's store: its settings are its own, never restarted from here
        sys.exit("setup: the store doesn't take the platform's Keycloak tokens (STS): give it the platform's "
                 "Keycloak as its OpenID provider (claim groups; the users' guide, Operating: an external store)")
    log("Buckets: its Keycloak sign-in settings didn't load (it started before Keycloak); starting it again")
    import k8s
    if k8s.in_cluster():
        log("Buckets: replaced pods " + ", ".join(k8s.delete_pods("buckets.io/cluster=buckets")))
    else:
        mc("alias", "set", "buckets", BUCKETS, env["BUCKETS_ROOT_USER"], env["BUCKETS_ROOT_PASSWORD"])
        mc("admin", "service", "restart", "buckets", check=False)
    while time.time() < deadline:
        time.sleep(5)
        if sts():
            log("Buckets: people sign in with Keycloak")
            return
    sys.exit("setup: Buckets doesn't take Keycloak tokens (STS), even started after Keycloak")


def keys(client, bucket, prefix=""):
    return [o["Key"] for p in client.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix=prefix)
            for o in p.get("Contents", [])]


def code(fn):
    """"OK", or the S3 error code fn raised."""
    from botocore.exceptions import ClientError
    try:
        fn()
        return "OK"
    except ClientError as e:
        return e.response["Error"]["Code"]


def audited(user, allowed, timeout=90):
    """How many of `user`'s requests Ranger's audit log (Solr) has, allowed or
    denied. The plugin sends audits in batches, so this waits for them."""
    deadline = time.time() + timeout
    while True:
        r = requests.get("http://ranger-solr:8983/solr/ranger_audits/select",
                         params={"q": f"reqUser:{user} AND result:{1 if allowed else 0}", "rows": 0}, timeout=10)
        hits = r.json()["response"]["numFound"] if r.ok else 0
        if hits or time.time() > deadline:
            return hits
        time.sleep(5)


def audited_denials(user, timeout=90):
    """How many of `user`'s requests Ranger's audit log has as denied."""
    return audited(user, allowed=False, timeout=timeout)
