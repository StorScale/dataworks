"""A project's import, end to end: docker compose run --rm test imports

An import copies an application's data into the project's tables on a
schedule (spec.imports): dlt, run by the platform's Airflow as one of the
project's pipelines. These check, with sales and a small CRM's API served
here (a token, a cursor, its leads changing):

  1. bob adds the import, and it's one of sales' pipelines;
  2. a run copies the leads into sales.crm_leads, signed in with the token,
     page by page (the API's cursor in its answer: rest_api paging);
  3. a second run asks only for what's new past the cursor, and merges it
     (a lead changed, one added);
  4. alice (a reader of sales' listed tables) can't read it;
  5. the catalog has its lineage: the import's pipeline upstream of its table;
  6. a run that fails (the API down) is one of sales' alerts;
  7. Salesforce, from its connector (8.4): bob gives sales a Salesforce
     connection on its Connections tab and adds the import from the form's
     endpoint (alice, a reader, can't); a run copies the accounts and leads
     (the stand-in for Salesforce served here over HTTPS, its certificate
     the connection's to trust), and the next merges a changed account;
  8. Gong and HighSpot, from theirs: with an API key each (Basic sign-in),
     a run copies Gong's users, calls (not the private one) and their
     transcripts, and HighSpot's spots, every item of each (paged) and users
     (stand-ins served here: connector_standins.py); the next Gong run asks
     only for the calls since the last;
  9. SAP S/4HANA, from its: with a communication user in client 100, a run
     copies business partners (paged by the server's __next), customers, the
     sales orders changed since a date and their items, amounts as decimals
     (a stand-in of its OData V2 APIs); the next asks only for what changed
     since, and copies the new order's items; and an ODP extraction (a CDS
     view's, through the same gateway): subscribed and copied whole, then
     by SAP's delta, a changed row merged, a deleted one removed.

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

import k8s
import requests
from connector_standins import GONG_KEY, GONG_SECRET, HS_KEY, HS_SECRET, SAP, SAP_CLIENT, SAP_PASSWORD, SAP_USER, Gong, Highspot

from lakekit import check, denied, env, finish, s3_root, sql
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, CONNECTION, PORT, TOKEN = "sales", "storscale-test-import", 8422, "crm-test-token"
DAG, TABLE = "import_sales_crm", "crm_leads"


class CRM(BaseHTTPRequestHandler):
    """GET /v1/leads?since=<time>: the leads changed after it, two to a page, as
    {"data": [...], "nextCursor": <the next page's, or null>}; ?cursor= asks for that page."""
    leads = [{"id": 1, "name": "Ada", "updated_at": "2026-10-01T10:00:00Z"},
             {"id": 2, "name": "Grace", "updated_at": "2026-10-02T10:00:00Z"},
             {"id": 3, "name": "Edsger", "updated_at": "2026-10-03T10:00:00Z"}]
    asked = []   # (since, signed in)
    pages = []   # the cursor each request asked with ("" for the first page)
    down = False

    def log_message(self, *args):
        pass

    def do_GET(self):
        url = urlparse(self.path)
        since = parse_qs(url.query).get("since", [""])[0]
        cursor = parse_qs(url.query).get("cursor", [""])[0]
        CRM.asked.append((since, self.headers.get("Authorization") == f"Bearer {TOKEN}"))
        CRM.pages.append(cursor)
        if CRM.down or url.path != "/v1/leads":
            self.send_response(503 if CRM.down else 404)
            self.end_headers()
            return
        changed = sorted((x for x in CRM.leads if x["updated_at"] > since), key=lambda x: x["updated_at"])
        start = int(cursor or 0)
        body = json.dumps({"data": changed[start:start + 2], "nextCursor": str(start + 2) if start + 2 < len(changed) else None}).encode()
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


SF_PORT, GONG_PORT, HS_PORT, SAP_PORT = 8428, 8429, 8430, 8431


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
    for prefix in ("sales/_dlt_", f"sales/{TABLE}", "sales/sf_", "sales/gong_", "sales/hs_", "sales/erp_", "sales/odp_"):
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
            "cursor": "updated_at", "cursorParam": "since", "cursorStart": "2026-01-01T00:00:00Z",
            "paging": {"type": "cursor", "next": "nextCursor", "param": "cursor"}}]}}]}}
    try:
        # 1. The import, a pipeline of sales'.
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check("bob adds an import (a CRM's leads) to sales", r.ok, r.text[:200])
        found = wait(lambda: pipeline(bob), 420, every=10)
        check("... and it's one of sales' pipelines", found is not None, found)

        # 2. A first run.
        CRM.pages.clear()
        state = run(bob)
        check("a run copies the leads into sales.crm_leads", state == "success" and rows() == [[1, "Ada"], [2, "Grace"], [3, "Edsger"]],
              f"{state}: {rows()}")
        check("... signed in with the import's token, asking from the cursor's start",
              CRM.asked and all(signed for _, signed in CRM.asked) and CRM.asked[0][0] == "2026-01-01T00:00:00Z", CRM.asked[:3])
        check("... page by page, following the answer's nextCursor (two leads to a page)", CRM.pages[:2] == ["", "2"], CRM.pages[:4])

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

        # 8. Gong and HighSpot, from theirs.
        servers = [ThreadingHTTPServer(("0.0.0.0", GONG_PORT), Gong), ThreadingHTTPServer(("0.0.0.0", HS_PORT), Highspot)]
        for sv in servers:
            threading.Thread(target=sv.serve_forever, daemon=True).start()
        made = [bob.post(f"{url}/connections", timeout=30, json={"name": "sales-gong", "kind": "gong", "values": {
                    "accessKey": GONG_KEY, "accessKeySecret": GONG_SECRET, "baseUrl": f"http://{here}:{GONG_PORT}"}}),
                bob.post(f"{url}/connections", timeout=30, json={"name": "sales-hs", "kind": "highspot", "values": {
                    "apiKey": HS_KEY, "apiSecret": HS_SECRET, "baseUrl": f"http://{here}:{HS_PORT}"}}),
                bob.put(f"{url}/imports/gong", headers={"X-Platform-Request": "1"}, timeout=30, json={
                    "source": "gong", "connection": "sales-gong", "schedule": "0 0 1 1 *",
                    "gong": {"objects": ["users", "calls", "call_transcripts"], "since": "2026-09-01"}}),
                bob.put(f"{url}/imports/hs", headers={"X-Platform-Request": "1"}, timeout=30, json={
                    "source": "highspot", "connection": "sales-hs", "schedule": "0 0 1 1 *", "highspot": {"objects": ["spots", "items", "users"]}})]
        check("bob adds Gong and HighSpot imports, each with its API key", all(m.ok for m in made), [m.text[:150] for m in made if not m.ok])
        found = wait(lambda: pipeline(bob, "import_sales_gong") and pipeline(bob, "import_sales_hs"), 420, every=10)
        app_rows = lambda q: (lambda: [list(x) for x in sql("bob", q, "iceberg", "sales")])
        state = run(bob, "import_sales_gong") if found else "not loaded"
        try:
            got = (app_rows("SELECT id FROM gong_users ORDER BY id")(), app_rows("SELECT id, title FROM gong_calls ORDER BY id")(),
                   app_rows("SELECT call_id FROM gong_call_transcripts ORDER BY call_id")())
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("a run copies Gong's users, calls (not the private one) and their transcripts, signed in with the key",
              state == "success" and got == ([["u1"], ["u2"]], [["c1", "Acme renewal"], ["c3", "Globex discovery"]], [["c1"], ["c3"]]),
              (state, got))
        Gong.asked.clear()
        Gong.calls.append({"id": "c4", "title": "Initech pricing", "started": "2026-10-01T09:00:00Z", "isPrivate": False})
        state = run(bob, "import_sales_gong")
        since = [a[3] for a in Gong.asked if a[1].endswith("/calls")]
        try:
            got = app_rows("SELECT id FROM gong_calls ORDER BY id")()
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("... and the next asks only for the calls since the last, and adds the new one",
              state == "success" and since == ["2026-09-20T15:00:00Z"] and got == [["c1"], ["c3"], ["c4"]], (state, since, got))
        state = run(bob, "import_sales_hs") if found else "not loaded"
        try:
            got = (app_rows("SELECT id FROM hs_spots ORDER BY id")(), app_rows("SELECT spot_id, count(*) FROM hs_items GROUP BY 1 ORDER BY 1")(),
                   app_rows("SELECT id FROM hs_users")())
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("a run copies HighSpot's spots, every item of each (paged) and its users",
              state == "success" and got == ([["sp1"], ["sp2"]], [["sp1", 102], ["sp2", 1]], [["hu1"]]), (state, got))
        for sv in servers:
            sv.shutdown()

        # 9. SAP S/4HANA, from its.
        sap_server = ThreadingHTTPServer(("0.0.0.0", SAP_PORT), SAP)
        threading.Thread(target=sap_server.serve_forever, daemon=True).start()
        made = [bob.post(f"{url}/connections", timeout=30, json={"name": "sales-sap", "kind": "sap", "values": {
                    "baseUrl": f"http://{here}:{SAP_PORT}", "username": SAP_USER, "password": SAP_PASSWORD, "client": SAP_CLIENT}}),
                bob.put(f"{url}/imports/erp", headers={"X-Platform-Request": "1"}, timeout=30, json={
                    "source": "sap", "connection": "sales-sap", "schedule": "0 0 1 1 *",
                    "sap": {"objects": ["business_partners", "customers", "sales_orders", "sales_order_items"], "since": "2026-09-10"}})]
        check("bob adds an SAP S/4HANA import (a communication user, client 100)", all(m.ok for m in made), [m.text[:150] for m in made if not m.ok])
        found = wait(lambda: pipeline(bob, "import_sales_erp"), 420, every=10)
        state = run(bob, "import_sales_erp") if found else "not loaded"
        try:
            got = (app_rows("SELECT business_partner FROM erp_business_partners ORDER BY 1")(), app_rows("SELECT count(*) FROM erp_customers")(),
                   app_rows("SELECT sales_order, total_net_amount, typeof(total_net_amount) FROM erp_sales_orders ORDER BY 1")(),
                   app_rows("SELECT sales_order, sales_order_item FROM erp_sales_order_items ORDER BY 1, 2")())
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("a run copies the partners changed since the date (the server paging), the customers, the order changed since and its item, amounts as decimals",
              state == "success" and isinstance(got, tuple) and got[0] == [["10001"], ["10002"]] and got[1] == [[2]]
              and len(got[2]) == 1 and got[2][0][0] == "2" and str(got[2][0][2]).startswith("decimal") and got[3] == [["2", "10"]],
              (state, got))
        SAP.asked.clear()
        SAP.rows["A_SalesOrder"].append({"SalesOrder": "3", "SoldToParty": "10000", "TotalNetAmount": "10.00", "LastChangeDateTime": "2026-10-05T08:00:00Z"})
        SAP.rows["A_SalesOrderItem"].append({"SalesOrder": "3", "SalesOrderItem": "10", "Material": "LAMP", "RequestedQuantity": "1.000", "NetAmount": "10.00"})
        state = run(bob, "import_sales_erp")
        orders_asked = [a[1] for a in SAP.asked if a[0] == "A_SalesOrder"]
        try:
            got = app_rows("SELECT sales_order, sales_order_item FROM erp_sales_order_items ORDER BY 1, 2")()
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("... and the next asks only for the orders changed since the last, and copies the new order's item",
              state == "success" and orders_asked and "2026-09-20T15:30:00Z" in orders_asked[0] and got == [["2", "10"], ["3", "10"]],
              (state, orders_asked, got))

        # An ODP extraction: whole, then by SAP's delta.
        r = bob.put(f"{url}/imports/odp", headers={"X-Platform-Request": "1"}, timeout=30, json={
            "source": "sap", "connection": "sales-sap", "schedule": "0 0 1 1 *", "sap": {"objects": [], "odp": [
                {"name": "margins", "service": "ZMARGIN_ODP_SRV", "entitySet": "EntityOfZCDS_MARGIN", "primaryKey": ["SalesOrder", "SalesOrderItem"]}]}})
        found = r.ok and wait(lambda: pipeline(bob, "import_sales_odp"), 420, every=10)
        state = run(bob, "import_sales_odp") if found else f"not loaded: {r.text[:150]}"
        margins = app_rows("SELECT sales_order, sales_order_item, margin FROM odp_margins ORDER BY 1, 2")
        try:
            got = margins()
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("an ODP extraction is subscribed (Prefer: odata.track-changes) and copied whole, its decimals decimals",
              state == "success" and any("odata.track-changes" in p for p in SAP.subscribed) and isinstance(got, list)
              and [x[:2] for x in got] == [["1", "10"], ["1", "20"], ["2", "10"]] and str(got[0][2]) == "300.000000000", (state, SAP.subscribed[:1], got))
        SAP.odp_changes += [("U", "-1", {"SalesOrder": "1", "SalesOrderItem": "10", "Margin": "300.00"}),
                            ("U", "1", {"SalesOrder": "1", "SalesOrderItem": "10", "Margin": "310.00"}),
                            ("D", "-1", {"SalesOrder": "1", "SalesOrderItem": "20", "Margin": "50.50"}),
                            ("C", "1", {"SalesOrder": "3", "SalesOrderItem": "10", "Margin": "5.00"})]
        state = run(bob, "import_sales_odp")
        try:
            got = [[a, b, str(c)] for a, b, c in margins()]
        except Exception as e:  # noqa: BLE001
            got = str(e)[:200]
        check("... and the next run takes SAP's delta: the changed row merged, the deleted one removed, the new one added",
              state == "success" and got == [["1", "10", "310.000000000"], ["2", "10", "20.000000000"], ["3", "10", "5.000000000"]], (state, got))
        sap_server.shutdown()
    finally:
        CRM.down = False
        for im in ("sf", "gong", "hs", "erp", "odp"):
            bob.delete(f"{url}/imports/{im}", headers={"X-Platform-Request": "1"}, timeout=30)
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        for c in ("sales-sf", "sales-gong", "sales-hs", "sales-sap"):
            bob.delete(f"{url}/connections/{c}", timeout=30)
        try:
            app_tables = [r[0] for r in sql("bob", "SHOW TABLES", "iceberg", "sales") if r[0].startswith(("gong_", "hs_", "erp_", "odp_"))]
        except Exception:  # noqa: BLE001
            app_tables = []
        for t in (TABLE, "sf_account", "sf_lead", *app_tables, "_dlt_loads", "_dlt_pipeline_state", "_dlt_version"):
            try:
                sql("bob", f"DROP TABLE IF EXISTS {t}", "iceberg", "sales")
            except Exception as e:  # noqa: BLE001
                print(f"  (dropping {t}: {e})")
        clear_dlt()
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
