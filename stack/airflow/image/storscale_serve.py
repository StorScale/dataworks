"""A project's models answering as an API (use case 15.1, R15.4), in the
virtualenv the scorings run in (/opt/score/bin/python), so a model loads as
it was saved.

Each serving's model is its approved version (models:/<model>@approved),
read through the platform's MLflow (/mlflow/<project>) as a Keycloak client
(client credentials: SERVING_CLIENT_ID, SERVING_CLIENT_SECRET, at
KEYCLOAK_URL). Every minute it looks again: a newly approved version is
loaded and answers from then on; one taken away leaves nothing to answer.

On Kubernetes a server serves one serving (STORSCALE_SERVING, as the operator
lists them); in Compose one server serves every project's, as the operator
lists them at OPERATOR_URL/serving. Only platformd reaches it, which signs
the caller in, checks they're the project's, and records the call:

  POST /p/<project>/<serving>/invocations   {"dataframe_records": [...]}, {"dataframe_split": {...}},
                                            {"instances": [...]} or {"inputs": ...}: rows of the model's inputs
                                            -> {"predictions": [...], "model": ..., "version": ...}
  GET  /p/<project>/<serving>               the model, the version served, or why none
  GET  /healthz
"""
import json
import os
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import mlflow
import pandas as pd
import requests

PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080").rstrip("/")
OPERATOR = os.environ.get("OPERATOR_URL", "http://platform-operator:8081").rstrip("/")
TOKEN_URL = os.environ["KEYCLOAK_URL"].rstrip("/") + "/realms/lakehouse/protocol/openid-connect/token"
CLIENT, SECRET = os.environ["SERVING_CLIENT_ID"], os.environ["SERVING_CLIENT_SECRET"]
FIXED = os.environ.get("STORSCALE_SERVING")   # one serving (Kubernetes), else every project's (Compose)
EVERY = int(os.environ.get("SERVING_REFRESH_SECONDS", "60"))
MAX_BODY = 32 << 20


def log(msg):
    print(f"serving: {msg}", flush=True)


class Token:
    """The client's access token, fetched again a minute before it ends."""

    def __init__(self):
        self.value, self.until, self.lock = "", 0.0, threading.Lock()

    def get(self):
        with self.lock:
            if time.time() > self.until:
                r = requests.post(TOKEN_URL, data={"grant_type": "client_credentials", "client_id": CLIENT, "client_secret": SECRET}, timeout=30)
                r.raise_for_status()
                body = r.json()
                self.value, self.until = body["access_token"], time.time() + max(30, body.get("expires_in", 300) - 60)
            return self.value


token = Token()
loading = threading.Lock()   # MLflow's tracking address is the process's: one project's load at a time


class Served:
    """A serving: its model's approved version, loaded, or why there's none."""

    def __init__(self, project, serving):
        self.project, self.name = project, serving["name"]
        self.update(serving)
        self.version, self.model, self.error = None, None, "loading"

    def update(self, serving):
        self.model_name, self.max_rows = serving["model"], int(serving.get("maxRows") or 1000)

    def refresh(self):
        with loading:
            os.environ["MLFLOW_TRACKING_URI"] = f"{PLATFORM}/mlflow/{self.project}"
            os.environ["MLFLOW_TRACKING_TOKEN"] = token.get()
            mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
            mlflow.set_registry_uri(os.environ["MLFLOW_TRACKING_URI"])
            try:
                version = mlflow.MlflowClient().get_model_version_by_alias(self.model_name, "approved").version
            except mlflow.exceptions.MlflowException as e:
                self.version, self.model = None, None
                self.error = f"no version of {self.model_name} is approved" if "RESOURCE_DOES_NOT_EXIST" in str(e) or "alias" in str(e).lower() else str(e)[:300]
                return
            if version != self.version:
                model = mlflow.pyfunc.load_model(f"models:/{self.model_name}/{version}")
                self.version, self.model, self.error = version, model, None
                log(f"{self.project}/{self.name}: {self.model_name} version {version}")

    def status(self):
        return {"project": self.project, "serving": self.name, "model": self.model_name, "version": self.version,
                "maxRows": self.max_rows, "error": self.error}


served = {}   # (project, name) -> Served
served_lock = threading.Lock()


def listed():
    if FIXED:
        return [json.loads(FIXED)]
    with urllib.request.urlopen(OPERATOR + "/serving", timeout=10) as r:
        return json.load(r)


def refresh_all():
    try:
        wanted = {(x["project"], x["serving"]["name"]): x for x in listed()}
    except (OSError, ValueError) as e:
        log(f"the servings' list: {e}")
        return
    with served_lock:
        for key in list(served):
            if key not in wanted:
                del served[key]
        for key, x in wanted.items():
            if key in served:
                served[key].update(x["serving"])
            else:
                served[key] = Served(x["project"], x["serving"])
        todo = list(served.values())
    for s in todo:
        try:
            s.refresh()
        except Exception as e:  # noqa: BLE001 (a model that won't load is that serving's error, not the server's)
            s.version, s.model, s.error = None, None, f"{type(e).__name__}: {str(e)[:300]}"
            log(f"{s.project}/{s.name}: {s.error}")


def loop():
    while True:
        refresh_all()
        time.sleep(EVERY)


def frame(body):
    """The rows a call sent, as MLflow's scoring server takes them."""
    if "dataframe_split" in body:
        split = body["dataframe_split"]
        return pd.DataFrame(split["data"], columns=split.get("columns"), index=split.get("index"))
    if "dataframe_records" in body:
        return pd.DataFrame.from_records(body["dataframe_records"])
    rows = body.get("instances", body.get("inputs"))
    if isinstance(rows, list) and (not rows or isinstance(rows[0], dict)):
        return pd.DataFrame.from_records(rows)
    if isinstance(rows, dict):
        return pd.DataFrame(rows)
    raise ValueError("rows: dataframe_records, dataframe_split, or instances (a list of objects)")


def plain(out):
    """A model's answer as JSON's values: a frame's records, an array's lists, NumPy's numbers as Python's."""
    if hasattr(out, "to_dict") and hasattr(out, "columns"):
        return json.loads(out.to_json(orient="records"))
    if hasattr(out, "tolist"):
        return out.tolist()
    if isinstance(out, list):
        return [plain(x) for x in out]
    return out


class Handler(BaseHTTPRequestHandler):
    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def serving(self):
        parts = self.path.split("?", 1)[0].strip("/").split("/")
        if len(parts) < 3 or parts[0] != "p":
            return None, parts
        with served_lock:
            return served.get((parts[1], parts[2])), parts

    def do_GET(self):  # noqa: N802 (http.server's name)
        if self.path == "/healthz":
            return self.reply(200, {"ok": True})
        s, parts = self.serving()
        if s is None or len(parts) != 3:
            return self.reply(404, {"error": "no such serving here"})
        return self.reply(200, s.status())

    def do_POST(self):  # noqa: N802
        s, parts = self.serving()
        if s is None or parts[3:] != ["invocations"]:
            return self.reply(404, {"error": "no such serving here"})
        size = int(self.headers.get("Content-Length") or 0)
        if size > MAX_BODY:
            return self.reply(413, {"error": f"a call is up to {MAX_BODY >> 20} MB"})
        model, version = s.model, s.version
        if model is None:
            return self.reply(503, {"error": s.error or "loading", "model": s.model_name})
        try:
            rows = frame(json.loads(self.rfile.read(size) or b"{}"))
        except (ValueError, KeyError, TypeError) as e:
            return self.reply(400, {"error": str(e)[:500]})
        if len(rows) > s.max_rows:
            return self.reply(413, {"error": f"{len(rows)} rows: a call scores up to {s.max_rows}"})
        try:
            out = model.predict(rows)
        except Exception as e:  # noqa: BLE001 (the model's own refusal: its inputs' schema, most often)
            return self.reply(400, {"error": f"{type(e).__name__}: {str(e)[:500]}", "model": s.model_name, "version": version})
        return self.reply(200, {"predictions": plain(out), "model": s.model_name, "version": version})

    def log_message(self, fmt, *args):   # the platform records the calls, not the rows
        pass


if __name__ == "__main__":
    refresh_all()
    threading.Thread(target=loop, daemon=True).start()
    log("listening on 8080")
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
