"""A project's alerts, end to end: docker compose run --rm test project_alerts

platformd looks at every project each minute: its pipelines' latest runs,
its tables' checks, and how long since each table it names in
tables.freshness last changed. It tells the project's own Teams channel
(a connection of the project's, holding the webhook) and its email, where
the project's notify says. These check, with sales:

  1. a table that hasn't changed in time is told to the project's channel,
     once it's named (as an Adaptive Card, linking to the table);
  2. the project's page has it: alice (a reader) sees it and can't mute it;
     bob (an editor) mutes it, and unmutes it;
  3. given more time, the table isn't stale: the channel is told it's resolved.

The sales spec, and the connection, are put back after.
"""
import json
import os
import socket
import threading
from http.server import ThreadingHTTPServer

import k8s
from health import Teams

from lakekit import check, finish, s3_root
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, CONNECTION, PORT = "sales", "storscale-test-alerts", 8421
TABLE = "orders"


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


def cards():
    """What the Teams channel's been told: each card's text."""
    out = []
    for _, _, body in list(Teams.received):
        att = (json.loads(body).get("attachments") or [{}])[0]
        if att:
            out.append(json.dumps(att.get("content", {})))
    return out


def slack_messages():
    """What the Slack channel's been told (the same webhook plays both): each message's text."""
    return [json.loads(body).get("text", "") for _, _, body in list(Teams.received) if "attachments" not in json.loads(body)]


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    before = bob.get(url, timeout=30).json()["spec"]
    alerts = lambda s: s.get(f"{url}/alerts", timeout=30)
    r = alerts(bob)
    check("the project's alerts are looked at (HEALTH is on)", r.ok and r.json().get("checked"), r.text[:200])

    server = ThreadingHTTPServer(("0.0.0.0", PORT), Teams)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    put_connection({"webhook": f"http://{socket.gethostbyname(socket.gethostname())}:{PORT}/workflows/sales-signature"})
    spec = json.loads(json.dumps(before))
    spec["spec"]["notify"] = {"teams": CONNECTION, "slack": CONNECTION}   # one webhook plays the Teams channel and the Slack one
    spec["spec"]["tables"]["freshness"] = [{"table": TABLE, "within": "1m"}]
    try:
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check("bob names the project's Teams channel, and a table that must change each minute", r.ok, r.text[:200])

        # 1. Told, once platformd next looks (each minute).
        stale = wait(lambda: next((c for c in cards() if f"{TABLE} is stale" in c), None), 180, every=5)
        check(f"the channel is told {TABLE} is stale", stale is not None,
              {"cards": cards()[-2:], "platformd can't tell": alerts(bob).json().get("unknown")})
        check("... as an Adaptive Card naming the project, and linking to the table",
              stale is not None and "sales: " in stale and f"/p/{PROJECT}/data?table={TABLE}" in stale, (stale or "")[:300])
        said = wait(lambda: next((m for m in slack_messages() if f"{TABLE} is stale" in m), None), 60, every=5)
        check("... and the Slack channel, as a message with the title in bold and the link",
              said is not None and said.startswith("*") and f"/p/{PROJECT}/data?table={TABLE}" in said, (said or slack_messages()[-2:]))

        # 2. The project's page.
        r = alerts(alice)
        issues = r.json().get("issues", []) if r.ok else []
        check("alice (a reader) sees it on the project's page", any(i["key"] == f"stale/{TABLE}" for i in issues), r.text[:300])
        check("... where it's told", r.ok and r.json().get("notify", {}).get("teams") is True and r.json().get("notify", {}).get("slack") is True, r.text[:200])
        code = alice.post(f"{url}/alerts/mute", json={"key": f"stale/{TABLE}", "hours": 1}, timeout=30).status_code
        check("... and can't mute it", code == 403, f"HTTP {code}")
        m = bob.post(f"{url}/alerts/mute", json={"key": f"stale/{TABLE}", "hours": 1}, timeout=30)
        muted = next((i.get("mutedUntil") for i in m.json().get("issues", []) if i["key"] == f"stale/{TABLE}"), None) if m.ok else None
        u = bob.post(f"{url}/alerts/mute", json={"key": f"stale/{TABLE}", "hours": 0}, timeout=30)
        unmuted = next((i.get("mutedUntil") for i in u.json().get("issues", []) if i["key"] == f"stale/{TABLE}"), "x") if u.ok else "x"
        check("bob (an editor) mutes it for an hour, and unmutes it", muted and not unmuted, f"{muted} then {unmuted}")

        # 3. More time: it isn't stale, and the channel is told.
        spec["spec"]["tables"]["freshness"] = [{"table": TABLE, "within": "3650d"}]
        bob.put(url, data=json.dumps(spec), timeout=30)
        resolved = wait(lambda: next((c for c in cards() if f"resolved: {TABLE} is stale" in c), None), 180, every=5)
        check("given ten years, it isn't stale: the channel is told it's resolved", resolved is not None, cards()[-3:])
    finally:
        bob.put(url, data=json.dumps(before), timeout=30)
        delete_connection()
        server.shutdown()
    finish()


if __name__ == "__main__":
    main()
