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
  5. what notebooks write is in Buckets, under each person's prefix.
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

from lakekit import check, finish, keys, s3_root

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


def wait_for_server(s, user, timeout=180):
    deadline = time.time() + timeout
    while time.time() < deadline:
        server = hub_api(s, "GET", f"/users/{user}")["servers"].get("")
        if not server:
            hub_api(s, "POST", f"/users/{user}/server")
        elif server["ready"]:
            return
        time.sleep(2)
    raise TimeoutError(f"{user}'s notebook server didn't start")


def run(user, token, code):
    """Run code in a new kernel on the person's server; returns its stdout."""
    auth = {"Authorization": f"token {token}"}
    base = f"{HUB}/user/{user}/api"
    kernel = requests.post(f"{base}/kernels", headers=auth, json={"name": "python3"}, timeout=60).json()["id"]
    ws = websocket.create_connection(HUB.replace("http", "ws", 1) + f"/user/{user}/api/kernels/{kernel}/channels",
                                     header=[f"Authorization: token {token}"], timeout=120)
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


def notebook_checks(user, other):
    s, r = sign_in(user)
    me = hub_api(s, "GET", "/user")
    wait_for_server(s, user)
    out = run(user, server_token(s, user), NOTEBOOK.replace("OTHER", repr(other)))
    result = json.loads(out.strip().splitlines()[-1])
    # VS Code, as JupyterLab's launcher opens it: served by the person's own server through jupyter-server-proxy.
    vs = requests.get(f"{HUB}/user/{user}/vscode/", headers={"Authorization": f"token {server_token(s, user)}"}, timeout=120, allow_redirects=True)
    result["vscode"] = vs.status_code if vs.ok else f"{vs.status_code} at {vs.url}: {vs.text[:200]}"
    hub_api(s, "DELETE", f"/users/{user}/server")  # done: stop it, as the hub's idle culler would
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
    bob, b = notebook_checks("bob", other="alice")
    alice, a = notebook_checks("alice", other="bob")

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
    finish()


if __name__ == "__main__":
    main()
