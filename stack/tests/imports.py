"""A project's import, end to end: docker compose run --rm test imports

An import copies an application's data into the project's tables on a
schedule (spec.imports): dlt, run by the platform's Airflow as one of the
project's pipelines. These check, with sales and a small CRM's API served
here (a token, a cursor, its leads changing):

  1. bob adds the import, and it's one of sales' pipelines;
  2. a run copies the leads into sales.crm_leads, signed in with the token;
  3. a second run asks only for what's new past the cursor, and merges it
     (a lead changed, one added);
  4. alice (a reader of sales' listed tables) can't read it;
  5. a run that fails (the API down) is one of sales' alerts.

The sales spec, the connection and the tables are put back after.
"""
import json
import os
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from lakekit import check, denied, finish, s3_root, sql

import k8s
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, CONNECTION, PORT, TOKEN = "sales", "storscale-test-import", 8422, "crm-test-token"
DAG, TABLE = "import_sales_crm", "crm_leads"


class CRM(BaseHTTPRequestHandler):
    """GET /v1/leads?since=<time>: the leads changed after it, as {"data": [...]}."""
    leads = [{"id": 1, "name": "Ada", "updated_at": "2026-10-01T10:00:00Z"},
             {"id": 2, "name": "Grace", "updated_at": "2026-10-02T10:00:00Z"},
             {"id": 3, "name": "Edsger", "updated_at": "2026-10-03T10:00:00Z"}]
    asked = []   # (since, signed in)
    down = False

    def log_message(self, *args):
        pass

    def do_GET(self):
        url = urlparse(self.path)
        since = parse_qs(url.query).get("since", [""])[0]
        CRM.asked.append((since, self.headers.get("Authorization") == f"Bearer {TOKEN}"))
        if CRM.down or url.path != "/v1/leads":
            self.send_response(503 if CRM.down else 404)
            self.end_headers()
            return
        body = json.dumps({"data": [x for x in CRM.leads if x["updated_at"] > since]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def put_connection(data):
    if k8s.in_cluster():
        k8s.put_secret(CONNECTION, data, labels={"platform.storscale.io/connection": PROJECT})
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"connections/{PROJECT}/{CONNECTION}.json", Body=json.dumps(data).encode())


def delete_connection():
    if k8s.in_cluster():
        k8s._api("DELETE", f"secrets/{CONNECTION}")
    else:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"connections/{PROJECT}/{CONNECTION}.json")


def pipeline(s):
    r = s.get(f"{PLATFORM}/api/projects/{PROJECT}/pipelines", timeout=60)
    return next((p for p in (r.json().get("pipelines", []) if r.ok else []) if p["id"] == DAG), None)


def run(s):
    """A run, started and waited for: its state."""
    r = s.post(f"{PLATFORM}/api/projects/{PROJECT}/pipelines/{DAG}/runs", headers={"X-Platform-Request": "1"}, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return f"not started: HTTP {r.status_code} {r.text[:200]}"
    done = wait(lambda: next((x["state"] for x in (pipeline(s) or {}).get("runs", [])
                              if x["id"] == run_id and x["state"] in ("success", "failed")), None), 600, every=10)
    return done or "still running"


def rows():
    try:
        return [list(r) for r in sql("bob", f"SELECT id, name FROM {TABLE} ORDER BY id", "iceberg", "sales")]
    except Exception as e:  # noqa: BLE001
        return str(e)[:200]


def main():
    bob = platform("bob")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    server = ThreadingHTTPServer(("0.0.0.0", PORT), CRM)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    here = socket.gethostbyname(socket.gethostname())
    put_connection({"token": TOKEN})
    spec = {**before, "spec": {**before["spec"], "imports": [{
        "name": "crm", "source": "rest_api", "connection": CONNECTION, "schedule": "0 0 1 1 *",   # by hand here
        "api": {"baseUrl": f"http://{here}:{PORT}/v1", "resources": [{
            "name": "leads", "path": "leads", "dataSelector": "data", "primaryKey": "id",
            "cursor": "updated_at", "cursorParam": "since", "cursorStart": "2026-01-01T00:00:00Z"}]}}]}}
    try:
        # 1. The import, a pipeline of sales'.
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check("bob adds an import (a CRM's leads) to sales", r.ok, r.text[:200])
        found = wait(lambda: pipeline(bob), 420, every=10)
        check("... and it's one of sales' pipelines", found is not None, found)

        # 2. A first run.
        state = run(bob)
        check("a run copies the leads into sales.crm_leads", state == "success" and rows() == [[1, "Ada"], [2, "Grace"], [3, "Edsger"]],
              f"{state}: {rows()}")
        check("... signed in with the import's token, asking from the cursor's start",
              CRM.asked and all(signed for _, signed in CRM.asked) and CRM.asked[0][0] == "2026-01-01T00:00:00Z", CRM.asked[:3])

        # 3. Only what's new, merged.
        CRM.asked.clear()
        CRM.leads[1] = {"id": 2, "name": "Grace Hopper", "updated_at": "2026-10-04T10:00:00Z"}
        CRM.leads.append({"id": 4, "name": "Barbara", "updated_at": "2026-10-05T10:00:00Z"})
        state = run(bob)
        check("a second run asks only for what's new past the cursor", CRM.asked and CRM.asked[0][0] == "2026-10-03T10:00:00Z", CRM.asked[:3])
        check("... and merges it: a lead changed, one added, none twice",
              state == "success" and rows() == [[1, "Ada"], [2, "Grace Hopper"], [3, "Edsger"], [4, "Barbara"]], f"{state}: {rows()}")

        # 4. The project's rules.
        msg = denied("alice", f"SELECT count(*) FROM {TABLE}", "iceberg", "sales")
        check("it's under sales' rules: alice (who reads its listed tables) can't read it", msg is not None, msg)

        # 5. A failed run alerts.
        CRM.down = True
        state = run(bob)
        issue = wait(lambda: next((i for i in bob.get(f"{url}/alerts", timeout=30).json().get("issues", [])
                                   if i["key"].startswith(f"pipeline/{DAG}/")), None), 180, every=10)
        check("a run that fails (the API down) is one of sales' alerts", state == "failed" and issue is not None, f"{state}: {issue}")
    finally:
        CRM.down = False
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        for t in (TABLE, "_dlt_loads", "_dlt_pipeline_state", "_dlt_version"):
            try:
                sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", "sales")
            except Exception as e:  # noqa: BLE001
                print(f"  (dropping {t}: {e})")
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
