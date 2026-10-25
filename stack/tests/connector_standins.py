"""Stand-ins for Gong's and HighSpot's APIs, as the imports suite plays them
(the parts the platform's connectors read: stack/airflow/image/sources/storscale_connectors.py).
Each checks the Basic sign-in it's given, pages as the real API does, and
keeps what it was asked."""
import base64
import json
from http.server import BaseHTTPRequestHandler
from urllib.parse import parse_qs, urlparse

GONG_KEY, GONG_SECRET = "gong-test-key", "gong-test-secret"
HS_KEY, HS_SECRET = "hs-test-key", "hs-test-secret"


class _API(BaseHTTPRequestHandler):
    key = secret = ""
    asked = []

    def log_message(self, *args):
        pass

    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def signed(self):
        want = "Basic " + base64.b64encode(f"{self.key}:{self.secret}".encode()).decode()
        if self.headers.get("Authorization") != want:
            self.reply(401, {"errors": ["the key and secret aren't this API's"]})
            return False
        return True

    def do_GET(self):
        self.route("GET")

    def do_POST(self):
        self.route("POST")


class Gong(_API):
    """Gong's v2: users (two pages, by cursor), calls (one private, filtered by
    fromDateTime), transcripts by call IDs, scorecards and answered scorecards."""
    key, secret = GONG_KEY, GONG_SECRET
    asked = []
    users = [{"id": "u1", "emailAddress": "dana@example.com"}, {"id": "u2", "emailAddress": "eli@example.com"}]
    calls = [{"id": "c1", "title": "Acme renewal", "started": "2026-09-02T10:00:00Z", "isPrivate": False},
             {"id": "c2", "title": "Private 1:1", "started": "2026-09-03T10:00:00Z", "isPrivate": True},
             {"id": "c3", "title": "Globex discovery", "started": "2026-09-20T15:00:00Z", "isPrivate": False}]

    def route(self, method):
        if not self.signed():
            return
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}") if method == "POST" else {}
        Gong.asked.append((method, u.path, q.get("cursor") or body.get("cursor"), q.get("fromDateTime")))
        path = u.path.removeprefix("/v2")
        if path == "/users":   # one user a page, to page by cursor
            i = int(q.get("cursor") or 0)
            page = {"users": Gong.users[i:i + 1], "records": {"totalRecords": len(Gong.users), "currentPageSize": 1}}
            if i + 1 < len(Gong.users):
                page["records"]["cursor"] = str(i + 1)
            return self.reply(200, page)
        if path == "/calls":
            calls = [c for c in Gong.calls if c["started"] >= q.get("fromDateTime", "")]
            if not calls:   # as Gong does
                return self.reply(404, {"errors": ["No calls found corresponding to the provided filters"]})
            return self.reply(200, {"calls": calls, "records": {"totalRecords": len(calls)}})
        if path == "/calls/transcript":
            ids = (body.get("filter") or {}).get("callIds") or []
            return self.reply(200, {"callTranscripts": [{"callId": i, "transcript": [{"speakerId": "u1", "sentences": [{"text": f"Hello on {i}"}]}]}
                                                        for i in ids]})
        if path == "/settings/scorecards":
            return self.reply(200, {"scorecards": [{"scorecardId": "s1", "scorecardName": "Discovery"}]})
        if path == "/stats/activity/scorecards":
            return self.reply(200, {"answeredScorecards": [{"answeredScorecardId": "a1", "scorecardId": "s1", "callId": "c1",
                                                            "reviewTime": "2026-09-05T09:00:00Z"}]})
        self.reply(404, {"errors": [f"no {path}"]})


class Highspot(_API):
    """HighSpot's v1.0: spots and their items and users, each paged by start and
    limit, the records in collection (the stand-in's page is short at 100)."""
    key, secret = HS_KEY, HS_SECRET
    asked = []
    spots = [{"id": "sp1", "title": "Sales plays"}, {"id": "sp2", "title": "Product sheets"}]
    items = {"sp1": [{"id": f"it{i}", "title": f"Play {i}"} for i in range(1, 103)], "sp2": [{"id": "it200", "title": "Tent sheet"}]}
    users = [{"id": "hu1", "email": "dana@example.com"}]

    def route(self, method):
        if not self.signed():
            return
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        Highspot.asked.append((u.path, q))
        start, limit = int(q.get("start", 0)), int(q.get("limit", 100))
        path = u.path.removeprefix("/v1.0")
        rows = {"/spots": Highspot.spots, "/users": Highspot.users, "/items": Highspot.items.get(q.get("spot"), [])}.get(path)
        if rows is None:
            return self.reply(404, {"error": f"no {path}"})
        self.reply(200, {"collection": rows[start:start + limit], "counts": {"total": len(rows)}})


SAP_USER, SAP_PASSWORD, SAP_CLIENT = "COMM_STORSCALE", "sap-test-password", "100"


def _ms(iso):
    import datetime
    return int(datetime.datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp() * 1000)


class SAP(_API):
    """S/4HANA's OData V2 APIs, the little the connector reads: each service's
    $metadata, its entity sets as V2 JSON (d.results; dates as /Date(ms)/,
    decimals as text, __metadata and deferred navigation beside the fields),
    $filter on a change stamp (ge) or on keys (eq ... or ...), $top and $skip,
    and the server's own paging (__next) on business partners, two a page.
    Signed in as a communication user, in client 100."""
    key, secret = SAP_USER, SAP_PASSWORD
    asked = []
    TYPES = {   # entity set: {field: Edm type}
        "A_BusinessPartner": {"BusinessPartner": "Edm.String", "BusinessPartnerFullName": "Edm.String", "LastChangeDate": "Edm.DateTime"},
        "A_Customer": {"Customer": "Edm.String", "CustomerName": "Edm.String"},
        "A_SalesOrder": {"SalesOrder": "Edm.String", "SoldToParty": "Edm.String", "TotalNetAmount": "Edm.Decimal",
                         "LastChangeDateTime": "Edm.DateTimeOffset"},
        "A_SalesOrderItem": {"SalesOrder": "Edm.String", "SalesOrderItem": "Edm.String", "Material": "Edm.String",
                             "RequestedQuantity": "Edm.Decimal", "NetAmount": "Edm.Decimal"},
    }
    SERVICES = {"API_BUSINESS_PARTNER": ["A_BusinessPartner", "A_Customer"], "API_SALES_ORDER_SRV": ["A_SalesOrder", "A_SalesOrderItem"]}
    rows = {
        "A_BusinessPartner": [{"BusinessPartner": f"1000{i}", "BusinessPartnerFullName": n, "LastChangeDate": d}
                              for i, (n, d) in enumerate([("Acme Corp", "2026-09-01"), ("Globex", "2026-09-15"), ("Initech", "2026-10-01")])],
        "A_Customer": [{"Customer": "10000", "CustomerName": "Acme Corp"}, {"Customer": "10001", "CustomerName": "Globex"}],
        "A_SalesOrder": [{"SalesOrder": "1", "SoldToParty": "10000", "TotalNetAmount": "1250.50", "LastChangeDateTime": "2026-09-02T10:00:00Z"},
                         {"SalesOrder": "2", "SoldToParty": "10001", "TotalNetAmount": "99.99", "LastChangeDateTime": "2026-09-20T15:30:00Z"}],
        "A_SalesOrderItem": [{"SalesOrder": "1", "SalesOrderItem": "10", "Material": "TENT-2P", "RequestedQuantity": "5.000", "NetAmount": "1000.00"},
                             {"SalesOrder": "1", "SalesOrderItem": "20", "Material": "STOVE", "RequestedQuantity": "5.000", "NetAmount": "250.50"},
                             {"SalesOrder": "2", "SalesOrderItem": "10", "Material": "MUG", "RequestedQuantity": "3.000", "NetAmount": "99.99"}],
    }

    def metadata(self, service):
        props = lambda es: "".join(f'<Property Name="{f}" Type="{t}"/>' for f, t in SAP.TYPES[es].items())
        types = "".join(f'<EntityType Name="{es}Type"><Key><PropertyRef Name="{list(SAP.TYPES[es])[0]}"/></Key>{props(es)}</EntityType>'
                        for es in SAP.SERVICES[service])
        sets = "".join(f'<EntitySet Name="{es}" EntityType="{service}.{es}Type"/>' for es in SAP.SERVICES[service])
        body = (f'<?xml version="1.0" encoding="utf-8"?><edmx:Edmx Version="1.0" xmlns:edmx="http://schemas.microsoft.com/ado/2007/06/edmx">'
                f'<edmx:DataServices><Schema Namespace="{service}" xmlns="http://schemas.microsoft.com/ado/2008/09/edm">{types}'
                f'<EntityContainer Name="{service}_Entities">{sets}</EntityContainer></Schema></edmx:DataServices></edmx:Edmx>').encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/xml")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    @staticmethod
    def matches(row, flt):
        import re
        if not flt:
            return True
        m = re.fullmatch(r"(\w+) ge (datetimeoffset|datetime)'([^']+)'", flt)
        if m:
            at = lambda x: _ms(x if "T" in x else x + "T00:00:00Z")
            return at(row[m.group(1)]) >= at(m.group(3) if m.group(3).endswith("Z") else m.group(3) + "Z")
        ors = re.findall(r"(\w+) eq '([^']*)'", flt)
        if ors and len(ors) == flt.count(" eq "):
            return any(row[f] == v for f, v in ors)
        raise ValueError(f"the stand-in doesn't read {flt}")

    def route(self, method):
        if not self.signed():
            return
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        parts = u.path.removeprefix("/sap/opu/odata/sap/").split("/")
        SAP.asked.append((parts[-1], q.get("$filter"), q.get("$skip") or q.get("$skiptoken"), q.get("sap-client")))
        if q.get("sap-client") != SAP_CLIENT:
            return self.reply(400, {"error": {"message": {"value": "client 000 has no such user"}}})
        if len(parts) != 2 or parts[0] not in SAP.SERVICES:
            return self.reply(404, {"error": {"message": {"value": f"no service {u.path}"}}})
        service, es = parts
        if es == "$metadata":
            return self.metadata(service)
        if es not in SAP.SERVICES[service]:
            return self.reply(404, {"error": {"message": {"value": f"no entity set {es}"}}})
        try:
            got = [r for r in SAP.rows[es] if self.matches(r, q.get("$filter"))]
        except ValueError as e:
            return self.reply(400, {"error": {"message": {"value": str(e)}}})
        kinds = SAP.TYPES[es]
        out = []
        for r in got:
            v = {"__metadata": {"uri": f"{es}('x')", "type": f"{service}.{es}Type"}}
            for f, x in r.items():
                v[f] = f"/Date({_ms(x if 'T' in x else x + 'T00:00:00Z')})/" if kinds[f] in ("Edm.DateTime", "Edm.DateTimeOffset") else x
            v["to_Item"] = {"__deferred": {"uri": f"{es}('x')/to_Item"}}
            out.append(v)
        if es == "A_BusinessPartner":   # the server's paging: two a page, then __next
            start = int(q.get("$skiptoken") or 0)
            page = {"results": out[start:start + 2]}
            if start + 2 < len(out):
                page["__next"] = f"{es}?$skiptoken={start + 2}"
            return self.reply(200, {"d": page})
        skip, top = int(q.get("$skip") or 0), int(q.get("$top") or 1000)
        self.reply(200, {"d": {"results": out[skip:skip + top]}})
