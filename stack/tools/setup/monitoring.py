"""Configure the monitoring example's Buckets, as docs/monitoring.md describes:

  - a metrics-only user for Prometheus (policy buckets-prometheus, allowed only
    admin:Prometheus), and its bearer token from `mc admin prometheus generate`,
    in the file Prometheus reads (/token/token; in Kubernetes, the Secret
    prometheus-token);
  - buckets with data for the dashboards: photos (versioned), logs (with a
    quota) and backups.

Safe to run again.
"""
import io
import os
import re

import k8s
from botocore.config import Config

from lakekit import env, log, mc, s3_root, setup_buckets

PROMETHEUS_POLICY = [{"Effect": "Allow", "Action": ["admin:Prometheus"], "Resource": ["arn:aws:s3:::*"]}]

setup_buckets(buckets=["photos", "logs", "backups"], policies={"buckets-prometheus": PROMETHEUS_POLICY},
              accounts=[(env["PROMETHEUS_USER"], env["PROMETHEUS_SECRET"], "buckets-prometheus")])
mc("version", "enable", "buckets/photos")
mc("quota", "set", "buckets/logs", "--size", "2GiB")

# The token, generated with an alias for the metrics user (not root).
mc("alias", "set", "metrics", "http://buckets:9000", env["PROMETHEUS_USER"], env["PROMETHEUS_SECRET"])
out = mc("admin", "prometheus", "generate", "metrics").stdout
token = re.search(r"bearer_token:\s*(\S+)", out).group(1)
if k8s.in_cluster():
    k8s.put_secret("prometheus-token", {"token": token})
    log("Prometheus: metrics user, and its token in the Secret prometheus-token")
else:
    os.makedirs("/token", exist_ok=True)
    with open("/token/token", "w") as f:
        f.write(token)
    os.chmod("/token/token", 0o644)
    log("Prometheus: metrics user, and its token in /token/token")

# Sample objects for the dashboards: not worth stopping an install or upgrade for.
# A write that hangs (a stuck lock in Buckets, 2026-10-19) is given up after a
# short wait and the rest skipped, with a warning.
s3 = s3_root(config=Config(read_timeout=20, connect_timeout=10, retries={"max_attempts": 2}))
try:
    for i in range(40):
        s3.put_object(Bucket="photos", Key=f"2026/10/img-{i:04d}.jpg", Body=os.urandom(64 * 1024 * (1 + i % 8)))
    for i in range(20):
        s3.put_object(Bucket="backups", Key=f"db/dump-{i:03d}.bin", Body=io.BytesIO(os.urandom(1024 * 1024)))
    log("data: photos, logs, backups")
except Exception as e:  # noqa: BLE001 (the dashboards' sample data is all that's missing)
    log(f"data: the sample objects for the dashboards weren't all written ({type(e).__name__}: {str(e)[:120]}); going on without them")
log("done")
