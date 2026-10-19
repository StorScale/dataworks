"""The platform's health and its alerts, end to end: docker compose run --rm test health

platformd checks every service of the platform every few seconds (the
Health page) and alerts the administrators, in Teams and by email, where
the platform's Secret storscale-alerts says. These check:

  1. every service is up, as the Health page has it;
  2. alice (not an administrator) is told only what's affected, and can't
     mute alerts;
  3. bob mutes a service's alerts, and unmutes them;
  4. alerts reach a Teams channel's webhook (one this suite serves) as an
     Adaptive Card: a test alert, sent from the page.

What storscale-alerts held before is put back after.
"""
import json
import os
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import k8s

from lakekit import check, finish, s3_root
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
SECRET, PORT = "storscale-alerts", 8420


class Teams(BaseHTTPRequestHandler):
    """A Teams channel's Workflows webhook: what's posted to it."""
    received = []

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        Teams.received.append((self.path, self.headers.get("Content-Type"), body))
        self.send_response(202)
        self.send_header("Content-Length", "0")
        self.end_headers()


def read_secret():
    if k8s.in_cluster():
        return k8s.get_secret(SECRET) or None
    try:
        return json.loads(s3_root().get_object(Bucket="storscale-platform", Key=f"secrets/{SECRET}.json")["Body"].read())
    except Exception:  # noqa: BLE001 (none)
        return None


def write_secret(data):
    if k8s.in_cluster():
        if data is None:
            k8s._api("DELETE", f"secrets/{SECRET}")
        else:
            k8s.put_secret(SECRET, data)
    elif data is None:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"secrets/{SECRET}.json")
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"secrets/{SECRET}.json", Body=json.dumps(data).encode())


def main():
    bob, alice = platform("bob"), platform("alice")
    health = lambda s: s.get(f"{PLATFORM}/api/health", timeout=30)

    # 1. Every service up.
    def all_up():
        r = health(bob)
        services = (r.json().get("services") or []) if r.ok else []
        return services if services and all(s["state"] == "up" for s in services) else None
    up = wait(all_up, 240, every=10)
    r = health(bob).json()
    down = [f"{s['label']}: {s.get('reason') or s['state']}" for s in r.get("services") or [] if s["state"] != "up"]
    check("the Health page has every service up", up is not None, f"{len(up)} services" if up else down or r)

    # 2. What others see.
    r = health(alice)
    check("alice is told only what's affected", r.ok and r.json().get("affected") == [] and "services" not in r.json(), r.text[:160])
    code = alice.post(f"{PLATFORM}/api/health/mute", json={"service": "superset", "hours": 1}, timeout=30).status_code
    check("... and can't mute alerts", code == 403, f"HTTP {code}")

    # 3. Mute, and unmute.
    m = bob.post(f"{PLATFORM}/api/health/mute", json={"service": "superset", "hours": 1}, timeout=30)
    muted = next((s.get("mutedUntil") for s in health(bob).json().get("services", []) if s["name"] == "superset"), None)
    bob.post(f"{PLATFORM}/api/health/mute", json={"service": "superset", "hours": 0}, timeout=30)
    unmuted = next((s.get("mutedUntil") for s in health(bob).json().get("services", []) if s["name"] == "superset"), "x")
    check("bob mutes a service's alerts for an hour, and unmutes them", m.ok and muted and not unmuted, f"{muted} then {unmuted}")

    # 4. A test alert, to a Teams webhook of this suite's own.
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Teams)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    webhook = f"http://{socket.gethostbyname(socket.gethostname())}:{PORT}/workflows/test-signature"
    before = read_secret()
    write_secret({"teamsWebhook": webhook})
    try:
        # platformd reads the targets at most every 30 seconds.
        result = wait(lambda: (lambda r: r if r.get("teams") == "sent" else None)(
            bob.post(f"{PLATFORM}/api/health/test", timeout=60).json().get("targets", {})), 90, every=10)
        check("a test alert is sent to the administrators' Teams channel", result is not None, result)
        card = json.loads(Teams.received[-1][2]) if Teams.received else {}
        att = (card.get("attachments") or [{}])[0]
        text = json.dumps(att.get("content", {}))
        check("... as an Adaptive Card, saying what it is and linking to the Health page",
              att.get("contentType") == "application/vnd.microsoft.card.adaptive" and "A test alert from the platform" in text
              and "/admin/health" in text, text[:200] or "nothing received")
        r = health(bob).json()
        check("... and it's among the recent alerts", any(a["title"] == "A test alert from the platform" for a in r.get("alerts", [])),
              [a["title"] for a in r.get("alerts", [])][:3])
    finally:
        write_secret(before)
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
