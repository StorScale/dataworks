"""End-to-end checks for the monitoring example: docker compose run --rm test

  1. Prometheus scrapes all three of Buckets' metrics endpoints, as a
     metrics-only user, which can read no data;
  2. the Helm chart's alert rules load, with no errors;
  3. the four Grafana dashboards' queries return data from Prometheus;
  4. people sign in to Grafana with Keycloak: engineers (bob) as Editors,
     analysts (alice) as Viewers; anyone in neither group (carol) is refused;
  5. a failed drive shows up and recovers: made unreadable under load, as a
     failing disk is, Buckets reports it offline and the erasure set degraded,
     and the alerts for both start; once it's readable again, it's back online
     and the drive-offline alert stops.

(An emptied drive doesn't make a reliable check: Buckets formats it back into
its slot within seconds, often before Prometheus's next scrape sees it.)
"""
import html
import json
import os
import re
import time

import requests
from botocore.exceptions import ClientError

from lakekit import check, env, finish, s3

import k8s


def drive(mode):
    """Make one of Buckets' four drives unreadable (0o000), or readable again,
    as a failing disk would be: in Compose, the drive's volume, mounted here
    (/drive3); in Kubernetes, its volume claim, from a pod beside Buckets'
    (BUCKETS_DRIVE: <pod>/<claim>)."""
    if os.environ.get("BUCKETS_DRIVE"):
        pod, claim = os.environ["BUCKETS_DRIVE"].split("/")
        # As root: the volume's directory may be root's (kind's local volumes
        # are). Restored, it is Buckets' own, as in Compose.
        restore = f"chown 65532:65532 /volume && chmod {mode:o} /volume"
        k8s.run_with_volume(pod, claim, ["sh", "-c", restore if mode else "chmod 0 /volume"], user=0)
    else:
        os.chmod("/drive3", mode)

PROM = "http://prometheus:9090/api/v1"
GRAFANA = "http://grafana:3000/monitoring"
GRAFANA_URL = os.environ["MONITORING_URL"]  # through the gateway, as a browser
DASHBOARDS = "/dashboards/*.json"
VARS = {"$namespace": "local", "$cluster": "store", "${namespace}": "local", "${cluster}": "store",
        "$__rate_interval": "1m", "$__interval": "15s", "$__range": "10m", "${__rate_interval}": "1m"}


def query(expr):
    r = requests.get(f"{PROM}/query", params={"query": expr}, timeout=30).json()
    return r["data"]["result"] if r.get("status") == "success" else None


def value(expr):
    res = query(expr)
    return float(res[0]["value"][1]) if res else None


def wait(fn, timeout, every=5):
    deadline = time.time() + timeout
    while True:
        got = fn()
        if got or time.time() > deadline:
            return got
        time.sleep(every)


def alerts():
    return {a["labels"]["alertname"]: a["state"]
            for a in requests.get(f"{PROM}/alerts", timeout=30).json()["data"]["alerts"]}


def dashboard_queries():
    """{dashboard title: [(panel title, expr)]}, from the dashboards Grafana loaded."""
    auth = ("admin", env["GRAFANA_ADMIN_PASSWORD"])
    out = {}
    for d in requests.get(f"{GRAFANA}/api/search", params={"type": "dash-db", "query": "Buckets"},
                          auth=auth, timeout=30).json():
        dash = requests.get(f"{GRAFANA}/api/dashboards/uid/{d['uid']}", auth=auth, timeout=30).json()["dashboard"]
        panels = [p for p in dash["panels"] if p.get("type") != "row"]
        out[dash["title"]] = [(p.get("title", ""), t["expr"]) for p in panels for t in p.get("targets", []) if t.get("expr")]
    return out


def grafana_role(user):
    """Sign in to Grafana through Keycloak's login form; the role Grafana gave them, or None."""
    s = requests.Session()
    r = s.get(f"{GRAFANA_URL}/login/generic_oauth", timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    if not form:
        return None
    s.post(html.unescape(form.group(1)), timeout=60,
           data={"username": user, "password": env["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    me = s.get(f"{GRAFANA_URL}/api/user/orgs", timeout=30)
    return me.json()[0]["role"] if me.ok and me.json() else None


def main():
    # 1. Scraping, as a metrics-only user.
    up = wait(lambda: (lambda r: r if r and len(r) == 3 and all(x["value"][1] == "1" for x in r) else None)(query('up{job=~"buckets-.*"}')), 120)
    check("Prometheus scrapes Buckets' node, cluster and bucket metrics",
          up is not None, ", ".join(sorted(f"{x['metric']['job']} up" for x in (up or []))) or "not all up")
    services = wait(lambda: query("platform_service_up") or None, 120) or []
    check("... and the platform's services, as its Health page checks them",
          len(services) > 20 and all(x["value"][1] == "1" for x in services),
          f"{sum(x['value'][1] == '1' for x in services)} of {len(services)} up")
    try:
        s3(aws_access_key_id=env["PROMETHEUS_USER"], aws_secret_access_key=env["PROMETHEUS_SECRET"]).list_objects_v2(Bucket="photos")
        denied = "allowed"
    except ClientError as e:
        denied = e.response["Error"]["Code"]
    check("... as a metrics-only user, which can read no data", denied == "AccessDenied", f"ListObjects: {denied}")

    # 2. The alert rules.
    rules = [r for g in requests.get(f"{PROM}/rules", timeout=30).json()["data"]["groups"] for r in g["rules"]]
    bad = [r["name"] for r in rules if r.get("health") != "ok"]
    check("the Helm chart's alert rules load, with no errors", rules and not bad,
          f"{len(rules)} rules" + (f"; unhealthy: {bad}" if bad else ""))

    # 3. The dashboards' queries. Bucket usage comes from Buckets' scanner, a
    # little after startup, so this waits for the data to arrive.
    dashboards = wait(dashboard_queries, 60)

    def coverage():
        total, empty = 0, []
        for title, targets in sorted(dashboards.items()):
            for panel, expr in targets:
                for k, v in VARS.items():
                    expr = expr.replace(k, v)
                total += 1
                if not query(expr):
                    empty.append(f"{title.split(' / ')[-1]}: {panel}")
        return total, empty

    deadline = time.time() + 240
    while True:
        total, empty = coverage()
        # Panels for what this example doesn't run (replication, the KMS, the
        # console's sign-ins) have nothing to show.
        if total - len(empty) >= total * 0.75 or time.time() > deadline:
            break
        time.sleep(10)
    check("Grafana has the four dashboards", len(dashboards) == 4, ", ".join(sorted(dashboards)))
    check("the dashboards' queries return data", total - len(empty) >= total * 0.75,
          f"{total - len(empty)} of {total} queries; empty: {'; '.join(sorted(set(empty)))}")

    # 4. Keycloak sign-in.
    roles = {u: grafana_role(u) for u in ("bob", "alice", "carol")}
    check("people sign in to Grafana with Keycloak: engineers edit, analysts view",
          roles["bob"] == "Editor" and roles["alice"] == "Viewer", f"bob {roles['bob']}, alice {roles['alice']}")
    check("people in neither group can't sign in to Grafana", roles["carol"] is None, f"carol {roles['carol']}")

    # 5. A failed drive, and its recovery. Unreadable until it's restored.
    drive(0o000)
    try:
        offline = wait(lambda: (value("max(minio_cluster_drive_offline_total)") or 0) >= 1, 180)
        check("a failed drive shows up as offline", offline,
              f"drives online {value('max(minio_cluster_drive_online_total)'):.0f}, offline "
              f"{value('max(minio_cluster_drive_offline_total)'):.0f}")
        started = wait(lambda: (lambda a: a if {"BucketsDriveOffline", "BucketsErasureSetDegraded"} <= set(a) else None)(alerts()), 180)
        check("the drive-offline and degraded-set alerts start", started,
              ", ".join(f"{k} {v}" for k, v in sorted((started or alerts()).items())) or "no alerts")
    finally:
        drive(0o755)
    online = wait(lambda: value("max(minio_cluster_drive_offline_total)") == 0, 180)
    check("once it's readable again, the drive is back online", online,
          f"drives online {value('max(minio_cluster_drive_online_total)'):.0f}, offline "
          f"{value('max(minio_cluster_drive_offline_total)'):.0f}")
    stopped = wait(lambda: "BucketsDriveOffline" not in alerts(), 180)
    check("... and the drive-offline alert stops", stopped,
          ", ".join(f"{k} {v}" for k, v in sorted(alerts().items())) or "no alerts")

    finish()


if __name__ == "__main__":
    main()
