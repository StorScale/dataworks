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
  5. the catalog has its lineage: the import's pipeline upstream of its table;
  6. a run that fails (the API down) is one of sales' alerts;
  7. Salesforce, from its connector (8.4): bob gives sales a Salesforce
     connection on its Connections tab and adds the import from the form's
     endpoint (alice, a reader, can't); a run copies the accounts and leads
     (the stand-in for Salesforce served here over HTTPS, its certificate
     the connection's to trust), and the next merges a changed account.

The sales spec, the connection and the tables are put back after.
"""
import json
import os
import re
import socket
import ssl
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import requests

from lakekit import check, denied, env, finish, s3_root, sql

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


SF_PORT = 8428


class Salesforce(BaseHTTPRequestHandler):
    """The little of Salesforce's REST API dlt's source uses: describe, and
    SOQL queries (its bulk API refused, so the source queries instead)."""
    fields = {"Account": ["Id", "Name", "Industry", "LastModifiedDate", "SystemModstamp"], "Lead": ["Id", "Name", "Company", "SystemModstamp"]}
    records = {"Account": [{"Id": "001A", "Name": "Acme", "Industry": "Retail", "LastModifiedDate": "2026-09-01T10:00:00.000+0000"},
                           {"Id": "001B", "Name": "Globex", "Industry": "Energy", "LastModifiedDate": "2026-09-02T10:00:00.000+0000"}],
               "Lead": [{"Id": "00QA", "Name": "Ann", "Company": "Initech"}]}
    signed = []

    def log_message(self, *args):
        pass

    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        self.reply(400, [{"errorCode": "FeatureNotEnabled", "message": "Async API not enabled"}])

    def do_GET(self):
        Salesforce.signed.append(self.headers.get("Authorization") == "Bearer sf-test-session")
        u = urlparse(self.path)
        m = re.search(r"/sobjects/(\w+)/describe", u.path)
        if m:
            kinds = {"LastModifiedDate": "datetime", "SystemModstamp": "datetime"}
            return self.reply(200, {"fields": [{"name": f, "type": kinds.get(f, "string"), "compoundFieldName": None} for f in Salesforce.fields[m.group(1)]]})
        if u.path.rstrip("/").endswith("/query"):
            q = parse_qs(u.query)["q"][0]
            obj = re.search(r"FROM (\w+)", q).group(1)
            out = [dict(r, SystemModstamp=r.get("LastModifiedDate") or "2026-09-01T00:00:00.000+0000") for r in Salesforce.records[obj]]
            w = re.search(r"WHERE (\w+) > (\S+)", q)
            if w:
                out = [r for r in out if r[w.group(1)][:19] > w.group(2)[:19]]
            return self.reply(200, {"totalSize": len(out), "done": True, "records": [dict(r, attributes={"type": obj}) for r in out]})
        self.reply(404, [{"errorCode": "NOT_FOUND", "message": self.path}])


def salesforce_server(here):
    """The stand-in, over HTTPS (simple_salesforce speaks nothing else): its
    certificate made here, for the connection to trust."""
    d = tempfile.mkdtemp()
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", f"{d}/key.pem", "-out", f"{d}/cert.pem",
                    "-days", "1", "-subj", f"/CN={here}", "-addext", f"subjectAltName=IP:{here}"], check=True, capture_output=True)
    server = ThreadingHTTPServer(("0.0.0.0", SF_PORT), Salesforce)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(f"{d}/cert.pem", f"{d}/key.pem")
    server.socket = ctx.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, open(f"{d}/cert.pem").read()


def clear_dlt():
    """dlt's own state (where its cursors got to) is files beside the dataset
    (warehouse/sales/_dlt_*), not tables Trino drops: removed, so a run of
    this suite starts afresh, whatever one before left."""
    s3 = s3_root()
    for prefix in ("sales/_dlt_", f"sales/{TABLE}", "sales/sf_"):
        for page in s3.get_paginator("list_objects_v2").paginate(Bucket="warehouse", Prefix=prefix):
            keys = [{"Key": o["Key"]} for o in page.get("Contents", [])]
            if keys:
                s3.delete_objects(Bucket="warehouse", Delete={"Objects": keys})


def pipeline(s, dag=DAG):
    r = s.get(f"{PLATFORM}/api/projects/{PROJECT}/pipelines", timeout=60)
    return next((p for p in (r.json().get("pipelines", []) if r.ok else []) if p["id"] == dag), None)


def run(s, dag=DAG):
    """A run, started and waited for: its state."""
    r = s.post(f"{PLATFORM}/api/projects/{PROJECT}/pipelines/{dag}/runs", headers={"X-Platform-Request": "1"}, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return f"not started: HTTP {r.status_code} {r.text[:200]}"
    done = wait(lambda: next((x["state"] for x in (pipeline(s, dag) or {}).get("runs", [])
                              if x["id"] == run_id and x["state"] in ("success", "failed")), None), 600, every=10)
    return done or "still running"


def rows():
    try:
        return [list(r) for r in sql("bob", f"SELECT id, name FROM {TABLE} ORDER BY id", "iceberg", "sales")]
    except Exception as e:  # noqa: BLE001
        return str(e)[:200]


def upstream():
    """The table's upstream nodes in the catalog (OpenMetadata), as its administrator reads them."""
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "client_credentials", "client_id": "platform-operator", "client_secret": env["OPERATOR_CLIENT_SECRET"]})
    h = {"Authorization": f"Bearer {r.json()['access_token']}"}
    r = requests.get(f"http://openmetadata:8585/api/v1/lineage/table/name/trino.iceberg.sales.{TABLE}",
                     params={"upstreamDepth": 1, "downstreamDepth": 0}, headers=h, timeout=30)
    if not r.ok:
        return []
    nodes = {n["id"]: n.get("fullyQualifiedName") for n in r.json().get("nodes", [])}
    return [nodes.get(e["fromEntity"]) for e in r.json().get("upstreamEdges", [])]


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    server = ThreadingHTTPServer(("0.0.0.0", PORT), CRM)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    here = socket.gethostbyname(socket.gethostname())
    for t in (TABLE, "_dlt_loads", "_dlt_pipeline_state", "_dlt_version"):
        try:
            sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", "sales")
        except Exception:  # noqa: BLE001
            pass
    clear_dlt()
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

        # A reset: all copied again, from the cursor's start.
        CRM.asked.clear()
        r = bob.post(f"{url}/imports/crm/reset", headers={"X-Platform-Request": "1"}, timeout=60)
        reset_id = r.json().get("run") if r.ok else None
        state = wait(lambda: next((x["state"] for x in (pipeline(bob) or {}).get("runs", [])
                                   if x["id"] == reset_id and x["state"] in ("success", "failed")), None), 600, every=10) if reset_id else r.text[:200]
        check("a reset copies it all again, from the cursor's start, none twice",
              state == "success" and CRM.asked and CRM.asked[0][0] == "2026-01-01T00:00:00Z"
              and rows() == [[1, "Ada"], [2, "Grace Hopper"], [3, "Edsger"], [4, "Barbara"]], f"{state}: {CRM.asked[:2]} {rows()}")
        code = alice.post(f"{url}/imports/crm/reset", headers={"X-Platform-Request": "1"}, timeout=60).status_code
        check("... and only an editor resets it: alice (a reader) can't", code == 403, f"HTTP {code}")

        # 4. The project's rules.
        msg = denied("alice", f"SELECT count(*) FROM {TABLE}", "iceberg", "sales")
        check("it's under sales' rules: alice (who reads its listed tables) can't read it", msg is not None, msg)

        # 5. Lineage.
        up = wait(upstream, 300, every=10) or upstream()
        check("the catalog has its lineage: the import's pipeline upstream of its table", "airflow.airflow-import_sales_crm" in up, up)

        # 6. A failed run alerts.
        CRM.down = True
        state = run(bob)
        issue = wait(lambda: next((i for i in bob.get(f"{url}/alerts", timeout=30).json().get("issues", [])
                                   if i["key"].startswith(f"pipeline/{DAG}/")), None), 180, every=10)
        check("a run that fails (the API down) is one of sales' alerts", state == "failed" and issue is not None, f"{state}: {issue}")
        CRM.down = False

        # 7. Salesforce, from its connector.
        sf_server, sf_cert = salesforce_server(here)
        c = bob.post(f"{url}/connections", timeout=30, json={"name": "sales-sf", "kind": "salesforce", "values": {
            "instanceUrl": f"https://{here}:{SF_PORT}", "sessionId": "sf-test-session", "caCertificate": sf_cert}})
        crm = {"source": "salesforce", "connection": "sales-sf", "schedule": "0 0 1 1 *", "salesforce": {"objects": ["account", "lead"]}}
        code = alice.put(f"{url}/imports/sf", json=crm, headers={"X-Platform-Request": "1"}, timeout=30).status_code
        r = bob.put(f"{url}/imports/sf", json=crm, headers={"X-Platform-Request": "1"}, timeout=30)
        check("bob adds Salesforce's accounts and leads from the connector's form (alice, a reader, can't)",
              c.ok and r.ok and code == 403, (c.text[:150], r.text[:200], code))
        found = wait(lambda: pipeline(bob, "import_sales_sf"), 420, every=10)
        state = run(bob, "import_sales_sf") if found else "not loaded"
        sf_rows = lambda t, cols: (lambda: [list(x) for x in sql("bob", f"SELECT {cols} FROM {t} ORDER BY id", "iceberg", "sales")])
        try:
            accounts, leads = sf_rows("sf_account", "id, name, industry")(), sf_rows("sf_lead", "id, company")()
        except Exception as e:  # noqa: BLE001
            accounts, leads = str(e)[:200], []
        check("a run copies Salesforce's accounts and leads into sales.sf_account and sf_lead, with the connection's session",
              state == "success" and accounts == [["001A", "Acme", "Retail"], ["001B", "Globex", "Energy"]] and leads == [["00QA", "Initech"]]
              and Salesforce.signed and all(Salesforce.signed), (state, accounts, leads))
        Salesforce.records["Account"][0].update(Industry="Media", LastModifiedDate="2026-10-01T10:00:00.000+0000")
        state = run(bob, "import_sales_sf")
        try:
            accounts = sf_rows("sf_account", "id, industry")()
        except Exception as e:  # noqa: BLE001
            accounts = str(e)[:200]
        check("... and the next merges the account that changed", state == "success" and accounts == [["001A", "Media"], ["001B", "Energy"]],
              (state, accounts))
        sf_server.shutdown()
    finally:
        CRM.down = False
        bob.delete(f"{url}/imports/sf", headers={"X-Platform-Request": "1"}, timeout=30)
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        bob.delete(f"{url}/connections/sales-sf", timeout=30)
        for t in (TABLE, "sf_account", "sf_lead", "_dlt_loads", "_dlt_pipeline_state", "_dlt_version"):
            try:
                sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", "sales")
            except Exception as e:  # noqa: BLE001
                print(f"  (dropping {t}: {e})")
        clear_dlt()
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
