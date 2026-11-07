"""End-to-end checks for the JupyterHub example: docker compose run --rm test

People sign in through Keycloak's own login form, as in a browser. Then code
runs in each person's notebook kernel, in their own container, through the
Jupyter Server API:

  1. Keycloak sign-in: bob (engineers) and alice (analysts) get in, with their
     Keycloak groups mirrored in the hub; bob administers the hub, alice
     doesn't; carol (no group) is refused;
  2. each notebook gets its person's own token and Buckets credentials, and no
     keys of any kind;
  3. each person reads and writes home/<them>/ only;
  4. the sales project's files: analysts (readers) read them, and only
     engineers (editors) write them;
  5. what notebooks write is in Buckets, under each person's prefix;
  6. on Kubernetes, Distributed Spark: the first profile, Spark's executors as
     pods, and Spark's credentials for the tables a person reads whole only.
"""
import html
import json
import os
import re
import time
import uuid
from urllib.parse import urlparse

import requests
import websocket

from lakekit import check, finish, keys, s3_root, sql

HUB = os.environ["NOTEBOOKS_URL"]


def sign_in(user):
    """Sign in through Keycloak's login form; returns (session, final response)."""
    s = requests.Session()
    r = s.get(f"{HUB}/hub/oauth_login", timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    r = s.post(html.unescape(form.group(1)), timeout=120,
               data={"username": user, "password": os.environ["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    return s, r


def hub_xsrf(s):
    """The hub's XSRF cookie. A running notebook server sets one of its own, under /user/<name>/."""
    return next((c.value for c in s.cookies if c.name == "_xsrf" and c.path.startswith(urlparse(HUB).path + "/hub")), "")


def hub_api(s, method, path, **kw):
    r = s.request(method, f"{HUB}/hub/api{path}", headers={"X-XSRFToken": hub_xsrf(s)}, timeout=30, **kw)
    r.raise_for_status()
    return r.json() if r.text.strip() else None


def server_token(s, user):
    """An API token for the person's own notebook server, as they could make one in the hub."""
    t = hub_api(s, "POST", f"/users/{user}/tokens", json={
        "note": "end-to-end checks", "expires_in": 3600,
        "scopes": [f"access:servers!user={user}", f"servers!user={user}", f"read:users!user={user}"]})
    return t["token"]


def wait_for_server(s, user, timeout=180, profile=None):
    """profile: the Kubernetes profile to start (spark, notebook); None, the first (Distributed Spark)."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        server = hub_api(s, "GET", f"/users/{user}")["servers"].get("")
        if not server:
            hub_api(s, "POST", f"/users/{user}/server", json={"profile": profile} if profile else None)
        elif server["ready"]:
            return
        time.sleep(2)
    raise TimeoutError(f"{user}'s notebook server didn't start")


def run(user, token, code, timeout=120):
    """Run code in a new kernel on the person's server; returns its stdout. timeout: the longest
    it may go without a word (a Spark job's executors start first)."""
    auth = {"Authorization": f"token {token}"}
    base = f"{HUB}/user/{user}/api"
    kernel = requests.post(f"{base}/kernels", headers=auth, json={"name": "python3"}, timeout=60).json()["id"]
    ws = websocket.create_connection(HUB.replace("http", "ws", 1) + f"/user/{user}/api/kernels/{kernel}/channels",
                                     header=[f"Authorization: token {token}"], timeout=timeout)
    msg_id = uuid.uuid4().hex
    ws.send(json.dumps({
        "header": {"msg_id": msg_id, "username": user, "session": uuid.uuid4().hex,
                   "msg_type": "execute_request", "version": "5.3"},
        "parent_header": {}, "metadata": {}, "channel": "shell",
        "content": {"code": code, "silent": False, "store_history": False, "user_expressions": {},
                    "allow_stdin": False, "stop_on_error": True}}))
    out, err = [], None
    while True:
        m = json.loads(ws.recv())
        if m.get("parent_header", {}).get("msg_id") != msg_id:
            continue
        t = m["msg_type"]
        if t == "stream":
            out.append(m["content"]["text"])
        elif t == "error":
            err = "\n".join(m["content"]["traceback"])
        elif t == "status" and m["content"]["execution_state"] == "idle":
            break
    ws.close()
    requests.delete(f"{base}/kernels/{kernel}", headers=auth, timeout=30)
    if err:
        raise RuntimeError(f"{user}'s notebook: {re.sub(r'\x1b\[[0-9;]*m', '', err)}")
    return "".join(out)


# Runs in a person's notebook: what they can and can't do in Buckets.
NOTEBOOK = r'''
import base64, io, json, os
import pandas as pd
import buckets_lake
from botocore.exceptions import ClientError

def proxied():
    """What jupyter-server-proxy serves on this server (its servers-info), as the server itself says."""
    import urllib.request
    url = "http://127.0.0.1:8888" + os.environ.get("JUPYTERHUB_SERVICE_PREFIX", "/") + "server-proxy/servers-info"
    req = urllib.request.Request(url, headers={"Authorization": "token " + os.environ.get("JUPYTERHUB_API_TOKEN", "")})
    try:
        return [s.get("name") for s in json.load(urllib.request.urlopen(req, timeout=10)).get("server_processes", [])]
    except Exception as e:  # noqa: BLE001
        return str(e)[:200]

def code(fn):
    try:
        fn(); return "OK"
    except ClientError as e:
        return e.response["Error"]["Code"]

me = buckets_lake.username()
other = OTHER
claims = json.loads(base64.urlsafe_b64decode(buckets_lake.token().split(".")[1] + "=="))
s3 = buckets_lake.s3()
orders = pd.read_parquet(io.BytesIO(s3.get_object(Bucket="sales", Key="datasets/orders.parquet")["Body"].read()))
print(json.dumps({
    "user": me,
    "token_user": claims.get("preferred_username"), "token_groups": claims.get("groups"),
    "token_aud": claims.get("aud"),
    # (AWS_CA_BUNDLE, with a private CA, is the CAs' file, not a key)
    "keys_in_env": sorted(k for k in os.environ
                          if k.startswith(("AWS_", "MINIO_", "BUCKETS_ROOT")) and k != "AWS_CA_BUNDLE"),
    "put_own": code(lambda: s3.put_object(Bucket="home", Key=f"{me}/notes.txt", Body=f"{me}'s notes".encode())),
    "list_own": code(lambda: s3.list_objects_v2(Bucket="home", Prefix=f"{me}/")),
    "get_other": code(lambda: s3.get_object(Bucket="home", Key=f"{other}/notes.txt")),
    "list_other": code(lambda: s3.list_objects_v2(Bucket="home", Prefix=f"{other}/")),
    "put_other": code(lambda: s3.put_object(Bucket="home", Key=f"{other}/hello.txt", Body=b"hi")),
    "orders": len(orders),
    "write_datasets": code(lambda: s3.put_object(Bucket="sales", Key=f"datasets/from-{me}.txt", Body=b"x")),
    # The libraries the common analyses and models need (use case 7.8), and VS Code on the path.
    # Each imported, as a notebook would (LightGBM needs the OpenMP runtime; FLAML's AutoML imports it).
    "libraries": [m for m in ("sklearn", "statsmodels", "scipy", "lightgbm", "xgboost", "flaml", "flaml.automl", "mlflow", "skops.io", "geopandas", "networkx")
                  if __import__("subprocess").run([__import__("sys").executable, "-c", "import " + m], capture_output=True).returncode != 0],
    # The models' templates (15.1, R15.7), put in the person's work folder as the server started.
    "templates": sorted(f for f in os.listdir(os.path.expanduser("~/work/templates")) if f.endswith(".ipynb")) if os.path.isdir(os.path.expanduser("~/work/templates")) else [],
    "code_server": __import__("shutil").which("code-server") is not None,
    "proxied": proxied(),
}))
'''


# Runs in a person's notebook: Spark, where the notebook has it (the Distributed Spark
# profile, on Kubernetes), and the credentials the platform gives Spark, as the person.
SPARK = r'''
import json, os, socket
import pandas as pd, requests, boto3
import buckets_lake
from botocore.exceptions import ClientError

def credentials(body=None):
    r = requests.post(os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080") + "/api/notebooks/spark-credentials",
                      json=body or {}, headers={"Authorization": "Bearer " + buckets_lake.token(), "X-Platform-Request": "1"}, timeout=300)
    return r.status_code, r.json()

out = {"spark": bool(os.environ.get("SPARK_REMOTE")),
       "k8s_token": os.path.exists("/var/run/secrets/kubernetes.io/serviceaccount/token")}
# Nessie itself, from the notebook: not reachable, or not answering (with no token, or the person's own).
def nessie(headers=None):
    try:
        return requests.get("http://nessie:19120/api/v2/trees", headers=headers or {}, timeout=10).status_code
    except requests.RequestException:
        return "unreachable"
out["nessie"] = [nessie(), nessie({"Authorization": "Bearer " + buckets_lake.token()})]
status, given = credentials()
out["status"] = status
out["tables"] = {t["name"]: t["write"] for t in given.get("tables", [])}
out["skipped"] = {t["name"]: t["why"] for t in given.get("skipped", [])}
if status == 200:
    s3 = boto3.client("s3", endpoint_url=buckets_lake.ENDPOINT, region_name="us-east-1",
                      aws_access_key_id=given["accessKeyId"], aws_secret_access_key=given["secretAccessKey"],
                      aws_session_token=given["sessionToken"])
    def code(fn):
        try:
            fn(); return "OK"
        except ClientError as e:
            return e.response["Error"]["Code"]
    out["list_warehouse"] = code(lambda: s3.list_objects_v2(Bucket="warehouse"))
    out["read_home"] = code(lambda: s3.get_object(Bucket="home", Key=f"{buckets_lake.username()}/notes.txt"))
if out["spark"]:
    import threading
    try:   # a failure is the checks' to report, with what came before it
        spark = buckets_lake.spark()
        timer = threading.Timer(300, spark.interruptAll)   # executors that never start: a failure, not a hang
        timer.start()
        out["payroll"] = spark.table("sales.payroll").count()
        out["payroll_trino"] = len(buckets_lake.sql("sales", "SELECT * FROM payroll"))
        # Four partitions, each on whatever executor it lands on: their hosts are executors' pods.
        hosts = spark.range(0, 4, 1, 4).mapInPandas(
            lambda batches: (pd.DataFrame({"host": [socket.gethostname()]}) for _ in batches), "host string").distinct().collect()
        out["hosts"] = sorted(h.host for h in hosts)
        out["spark_tables"] = dict(buckets_lake.spark_tables)
        timer.cancel()
        spark.stop()
        # A table made for Spark (by Trino, as the person), then written from Spark.
        made = buckets_lake.spark(create={"sales.spark_check": {"id": "bigint", "who": "varchar"}})
        made.createDataFrame([(1, "a"), (2, "b"), (3, "c")], "id long, who string").writeTo("sales.spark_check").append()
        out["made"] = buckets_lake.spark_tables.get("sales.spark_check")
        out["made_rows"] = len(buckets_lake.sql("sales", "SELECT * FROM spark_check"))
        try:   # Spark's catalog (platformd's) drops nothing
            made.sql("DROP TABLE sales.spark_check").collect()
            out["spark_drop"] = "dropped"
        except Exception as e:
            out["spark_drop"] = str(e)[:300]
        made.stop()
        # A session given payroll alone: its catalog answers payroll, and nothing else.
        limited = buckets_lake.spark(tables=["sales.payroll"])
        try:
            limited.table("sales.orders").count()
            out["spark_other"] = "read"
        except Exception as e:
            out["spark_other"] = str(e)[:300]
        limited.stop()
    except Exception as e:
        out["error"] = f"{type(e).__name__}: {str(e)[:1200]}"
else:   # someone who may not make tables there asks Spark to
    out["create_status"], answer = credentials({"create": [{"name": "sales.spark_denied", "columns": [{"name": "a", "type": "bigint"}]}]})
    out["create_error"] = answer.get("error", "")[:300]
print(json.dumps(out))
'''


def show_spark(user):
    """What CI shows when Spark fails, before the notebook stops: its driver's log (warnings and
    errors), and its executors' pods and the events of the Spark namespace."""
    sa = "/var/run/secrets/kubernetes.io/serviceaccount"
    try:
        with open(f"{sa}/token") as f:
            token = f.read().strip()
        with open(f"{sa}/namespace") as f:
            ns = f.read().strip()
    except OSError:
        return
    base = f"https://{os.environ['KUBERNETES_SERVICE_HOST']}:{os.environ.get('KUBERNETES_SERVICE_PORT', '443')}/api/v1/namespaces"

    def get(path, **params):
        r = requests.get(f"{base}/{path}", params=params, headers={"Authorization": f"Bearer {token}"}, verify=f"{sa}/ca.crt", timeout=30)
        return r if r.ok else print(f"  {path}: HTTP {r.status_code} {r.text[:200]}")

    r = get(f"{ns}/pods/jupyter-{user}/log", container="spark")
    if r:
        seen, trace = set(), 0
        print(f"  --- jupyter-{user}'s spark container (warnings and errors, each once, and their traces' start):")
        for line in r.text.splitlines():
            if re.search(r" (WARN|ERROR) ", line):
                key = re.sub(r"^\S+ \S+ ", "", line)
                trace = 0 if key in seen else 8
                if trace:
                    seen.add(key)
                    print("    " + line[:400])
            elif trace and (line[:1].isspace() or re.match(r"(Caused by|[\w.$]+(Exception|Error))", line)):
                trace -= 1
                print("    " + line[:400])
            else:
                trace = 0
    sns = os.environ.get("STORSCALE_SPARK_NAMESPACE", f"{ns}-spark")
    r = get(f"{sns}/pods")
    for pod in (r.json().get("items", []) if r else []):
        name = pod["metadata"]["name"]
        st = pod.get("status", {})
        print(f"  {sns}/{name}: {st.get('phase')} {json.dumps([c.get('state') for c in st.get('containerStatuses', [])])[:300]}")
        for c in st.get("conditions", []):
            if c.get("status") != "True":
                print(f"    {c.get('type')}: {c.get('reason') or ''} {c.get('message') or ''}")
        r = get(f"{sns}/pods/{name}/log", tailLines="40")
        if r:
            print("\n".join("    " + line for line in r.text.splitlines()))
    r = get(f"{sns}/events")
    for e in (r.json().get("items", []) if r else [])[-20:]:
        print(f"  {sns} event {e.get('involvedObject', {}).get('name')}: {e.get('reason')}: {(e.get('message') or '')[:300]}")


def notebook_checks(user, other, profile=None):
    s, r = sign_in(user)
    me = hub_api(s, "GET", "/user")
    wait_for_server(s, user, profile=profile)
    try:
        out = run(user, server_token(s, user), NOTEBOOK.replace("OTHER", repr(other)))
        result = json.loads(out.strip().splitlines()[-1])
        if os.environ.get("KUBERNETES_SERVICE_HOST"):   # Spark is Kubernetes' alone
            try:
                result["spark"] = json.loads(run(user, server_token(s, user), SPARK, timeout=420).strip().splitlines()[-1])
            except RuntimeError as e:   # a failed check, not the end of the suite
                result["spark"] = {"error": str(e)[-1500:]}
            if result["spark"].get("error"):
                show_spark(user)
        # VS Code, as JupyterLab's launcher opens it: served by the person's own server through jupyter-server-proxy.
        vs = requests.get(f"{HUB}/user/{user}/vscode/", headers={"Authorization": f"token {server_token(s, user)}"}, timeout=120, allow_redirects=True)
        result["vscode"] = vs.status_code if vs.ok else f"{vs.status_code} at {vs.url}: {vs.text[:200]}"
    finally:
        hub_api(s, "DELETE", f"/users/{user}/server")  # done (or failed): stop it, as the hub's idle culler would
    return me, result


def main():
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            if requests.get(f"{HUB}/hub/health", timeout=5).ok:
                break
        except requests.RequestException:
            pass
        time.sleep(2)

    # bob first: alice's checks read bob's prefix, so it must exist.
    if os.environ.get("KUBERNETES_SERVICE_HOST"):   # Spark's table, as a failed run may have left it
        sql("bob", "DROP TABLE IF EXISTS spark_check", "iceberg", "sales")
    # bob's notebook has Distributed Spark; alice's, the notebook alone.
    bob, b = notebook_checks("bob", other="alice", profile="spark")
    alice, a = notebook_checks("alice", other="bob", profile="notebook")

    # 1. Keycloak sign-in, groups and hub roles.
    check("people sign in to JupyterHub with Keycloak, and their groups come along",
          # (and their projects' groups: sales-readers, sales-editors)
          {"analysts", "sales-readers"} <= set(alice["groups"]) and {"engineers", "sales-editors"} <= set(bob["groups"])
          and "engineers" not in alice["groups"],
          f"alice {alice['groups']}, bob {bob['groups']}")
    check("Keycloak's engineers group administers the hub, and analysts don't",
          bob["admin"] is True and alice["admin"] is False, f"bob admin={bob['admin']}, alice admin={alice['admin']}")
    _, r = sign_in("carol")
    check("people in neither group can't sign in", r.status_code == 403, f"HTTP {r.status_code}")
    check("a notebook has the analysis and ML libraries, and VS Code opens from the launcher (7.8)",
          a["libraries"] == [] and a["code_server"] is True and a["vscode"] == 200, (a["libraries"], a["code_server"], a["proxied"], a["vscode"]))
    check("... and the models' templates, churn, forecast and propensity, are in the person's work folder (15.1)",
          a["templates"] == ["churn.ipynb", "forecast.ipynb", "propensity.ipynb"], a["templates"])

    # 2. Each notebook works as its person, with no keys.
    check("each notebook gets its own person's Keycloak token",
          a["token_user"] == "alice" and b["token_user"] == "bob" and "lakehouse" in (a["token_aud"] or []),
          f"alice's notebook: {a['token_user']} {a['token_groups']}; bob's: {b['token_user']} {b['token_groups']}")
    check("notebooks hold no keys", not a["keys_in_env"] and not b["keys_in_env"],
          f"credential variables: {a['keys_in_env'] + b['keys_in_env'] or 'none'}")

    # 3. A private home prefix each.
    check("each person reads and writes their own home prefix",
          a["put_own"] == a["list_own"] == b["put_own"] == "OK", f"alice {a['put_own']}, bob {b['put_own']}")
    check("nobody reads or writes another person's prefix",
          {a["get_other"], a["list_other"], a["put_other"]} == {"AccessDenied"},
          f"alice on bob's: GetObject {a['get_other']}, ListObjects {a['list_other']}, PutObject {a['put_other']}")

    # 4. Shared datasets: groups decide.
    check("analysts and engineers read the shared datasets", a["orders"] == b["orders"] == 6,
          f"{a['orders']} orders read with pandas")
    check("only engineers write the shared datasets",
          a["write_datasets"] == "AccessDenied" and b["write_datasets"] == "OK",
          f"alice {a['write_datasets']}, bob {b['write_datasets']}")

    # 5. In Buckets.
    homes = keys(s3_root(), "home")
    check("the notebooks' files are in Buckets, each under its person's prefix",
          {"alice/notes.txt", "bob/notes.txt"} <= set(homes), ", ".join(sorted(homes)))

    # 6. Distributed Spark (Kubernetes): Spark's driver beside the notebook, executors as pods,
    # and credentials for the tables the person reads whole, from the platform.
    if "spark" in b:
        bs, as_ = b["spark"], a["spark"]
        check("bob's notebook (Distributed Spark) has Spark, and alice's (the notebook alone) hasn't",
              bs.get("spark") is True and as_.get("spark") is False, bs.get("error") or as_.get("error") or f"bob {bs['spark']}, alice {as_['spark']}")
        check("... and the notebook itself has no Kubernetes token (only Spark's container does)",
              bs.get("k8s_token") is False and as_.get("k8s_token") is False, f"bob {bs.get('k8s_token')}, alice {as_.get('k8s_token')}")
        check("Spark reads a table as Trino does, as the person, with executors as pods",
              bs.get("payroll") is not None and bs.get("payroll") == bs.get("payroll_trino") >= 1 and bs.get("hosts") and all("-exec-" in h for h in bs["hosts"]),
              f"{bs.get('payroll')} payroll rows (Trino: {bs.get('payroll_trino')}), on {bs.get('hosts')}")
        check("Spark gets the tables a person reads whole, and changes those they edit (bob, sales' editor: payroll)",
              bs.get("status") == 200 and bs.get("tables", {}).get("sales.payroll") is True,
              f"HTTP {bs.get('status')}: {bs.get('tables')}, skipped {bs.get('skipped')}")
        check("... and nothing else: no listing the warehouse, no one's home",
              bs.get("list_warehouse") == "AccessDenied" and bs.get("read_home") == "AccessDenied",
              f"ListObjects {bs.get('list_warehouse')}, home {bs.get('read_home')}")
        check("Spark writes a table made for it: Trino makes it, as the person, and the session may write it",
              bs.get("made") == "read and write" and bs.get("made_rows") == 3,
              f"sales.spark_check: {bs.get('made')}, {bs.get('made_rows')} rows through Trino")
        check("Spark's catalog drops nothing: platformd's, on the session's tables (Nessie answers the platform alone)",
              "drop or rename" in bs.get("spark_drop", ""), bs.get("spark_drop"))
        check("... and a session reads only the tables it was given (payroll alone: orders refused)",
              "isn't one of this session's tables" in bs.get("spark_other", ""), bs.get("spark_other"))
        check("... and the notebook can't reach Nessie itself, without a token or with the person's own",
              all(v in ("unreachable", 401, 403) for v in bs.get("nessie", [200]) + as_.get("nessie", [200])),
              f"bob {bs.get('nessie')}, alice {as_.get('nessie')}")
        check("... and only where the person may make tables (alice, an analyst, is refused)",
              as_.get("create_status") == 403 and "spark_denied" in as_.get("create_error", ""),
              f"HTTP {as_.get('create_status')}: {as_.get('create_error')}")
        sql("bob", "DROP TABLE IF EXISTS spark_check", "iceberg", "sales")
        check("a table with a row filter or mask for the person isn't Spark's: alice reads orders through Trino only",
              # (her tables elsewhere are the other suites': business shares company's customers with her)
              "sales.orders" in as_.get("skipped", {}) and not any(t.startswith("sales.") for t in as_.get("tables") or {}),
              f"HTTP {as_.get('status')}: {as_.get('tables')}, skipped {as_.get('skipped')}")
    finish()


if __name__ == "__main__":
    main()
