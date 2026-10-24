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
