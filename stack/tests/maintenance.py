"""Table maintenance, end to end: docker compose run --rm test maintenance

platformd keeps every project's tables each night, as Trino's maintenance
account: small files compacted, snapshots older than the project's
keepSnapshots expired, files no snapshot uses removed. These check, on a
table of sales' own made for the purpose:

  1. alice (a reader) can't ask for it; bob (an editor) can, and it runs;
  2. the table's small files are compacted into one, its rows intact;
  3. snapshots older than keepSnapshots are expired, and a file no snapshot
     uses is removed. Only where the install lets snapshots go after a few
     minutes (maintenance.minRetention; a checkout's own install does): the
     least is a day elsewhere, and then the snapshots must all be kept.

The table is dropped and sales' spec put back after.
"""
import calendar
import json
import os
import time
from urllib.parse import urlparse

import requests

from lakekit import check, finish, s3_root, sql

from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
NESSIE = "http://nessie:19120/iceberg"
PROJECT, TABLE, KEEP = "sales", "maint_check", "2m"


def minutes(within):
    n, unit = int(within[:-1]), within[-1]
    return n * {"m": 1, "h": 60, "d": 1440}[unit]


def location():
    """Where the table's files are, as the catalog has it: (bucket, prefix)."""
    prefix = requests.get(f"{NESSIE}/v1/config?warehouse=warehouse", timeout=10).json()["overrides"]["prefix"]
    meta = requests.get(f"{NESSIE}/v1/{prefix}/namespaces/{PROJECT}/tables/{TABLE}", timeout=10).json()["metadata"]
    u = urlparse(meta["location"])
    return u.netloc, u.path.strip("/")


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    m = bob.get(f"{url}/maintenance", timeout=30).json()
    check("table maintenance is set up", m.get("enabled") is True, m)
    short = minutes(m.get("minRetention") or "7d") <= 3
    before_spec = bob.get(url, timeout=30).json()["spec"]
    q = lambda s: sql("bob", s, "iceberg", PROJECT)

    q(f"DROP TABLE IF EXISTS {TABLE}")
    q(f"CREATE TABLE {TABLE} (id integer, region varchar)")
    for i in range(1, 7):  # a file, and a snapshot, each
        q(f"INSERT INTO {TABLE} VALUES ({i}, 'EU')")
    bucket, prefix = location()
    orphan = f"{prefix}/data/left-by-a-failed-write.parquet"
    s3_root().put_object(Bucket=bucket, Key=orphan, Body=b"not referenced by any snapshot")
    try:
        spec = json.loads(json.dumps(before_spec))
        spec["spec"]["tables"]["maintenance"] = {"keepSnapshots": KEEP}
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check(f"bob keeps sales' snapshots {KEEP}", r.ok, r.text[:200])
        if short:
            time.sleep(minutes(KEEP) * 60 + 20)  # the snapshots so far, and the orphan, grow older than that
        q(f"INSERT INTO {TABLE} VALUES (7, 'US')")  # a recent snapshot, which must be kept

        # 1. Asked for.
        code = alice.post(f"{url}/maintenance", timeout=30).status_code
        check("alice (a reader) can't ask for the project's maintenance", code == 403, f"HTTP {code}")
        asked = time.time()
        r = bob.post(f"{url}/maintenance", timeout=30)
        check("bob (an editor) asks for it", r.status_code == 202, f"HTTP {r.status_code} {r.text[:120]}")

        def done():
            d = bob.get(f"{url}/maintenance", timeout=30).json()
            t = next((t for t in d.get("tables", []) if t["name"] == TABLE), {})
            run = t.get("lastRun")
            if d.get("state") == "idle" and run and calendar.timegm(time.strptime(run["ended"][:19], "%Y-%m-%dT%H:%M:%S")) >= asked - 5:
                return run
        run = wait(done, 600, every=5)
        check("... and the table is kept", run is not None and not run.get("error") and len(run.get("done", [])) == 3,
              run and {k: run[k] for k in ("done", "error") if k in run})
        if not run:
            return

        # 2. Compacted, with its rows.
        b, a = run["before"], run["after"]
        check("its small files are compacted into one", b["dataFiles"] >= 7 and a["dataFiles"] == 1, f"{b['dataFiles']} -> {a['dataFiles']}")
        rows = q(f"SELECT count(*), sum(id) FROM {TABLE}")
        check("... and every row is still there", rows == [[7, 28]], rows)

        # 3. Expired, and the orphan gone; or, where snapshots are kept a day, all kept.
        if short:
            check(f"snapshots older than {KEEP} are expired; the recent ones are kept",
                  b["snapshots"] >= 7 and 1 <= a["snapshots"] <= 3, f"{b['snapshots']} -> {a['snapshots']}")
            try:
                s3_root().head_object(Bucket=bucket, Key=orphan)
                gone = False
            except Exception:  # noqa: BLE001 (404)
                gone = True
            check("a file no snapshot uses is removed", gone, orphan)
        else:
            check(f"snapshots younger than the install's least ({m.get('minRetention')}) are all kept",
                  a["snapshots"] >= b["snapshots"], f"{b['snapshots']} -> {a['snapshots']} (expiry itself is checked where it may be minutes)")
    finally:
        bob.put(url, data=json.dumps(before_spec), timeout=30)
        q(f"DROP TABLE IF EXISTS {TABLE}")
        try:
            s3_root().delete_object(Bucket=bucket, Key=orphan)
        except Exception:  # noqa: BLE001
            pass
    finish()


if __name__ == "__main__":
    main()
