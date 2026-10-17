"""Table maintenance, end to end: docker compose run --rm test maintenance

platformd keeps every project's tables each night, as Trino's maintenance
account: their small files compacted. Then Nessie's GC deletes the history
older than the install keeps (maintenance.history.keep), and files no branch
or tag needs. These check, on a table of sales' own made for the purpose:

  1. alice (a reader) can't ask for it; bob (an editor) can, and it runs;
  2. the table's small files are compacted into one, its rows intact;
  3. once the history before the compaction is older than the install keeps,
     the files only it used are deleted, and so is a file nothing ever used,
     the rows still intact. Only where the install keeps a few minutes'
     history (a checkout's own does): elsewhere, a week or more.

The table is dropped after.
"""
import calendar
import os
import re
import time
from urllib.parse import urlparse

import requests

from lakekit import check, finish, s3_root, sql

from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, TABLE = "sales", "maint_check"
NESSIE = "http://nessie:19120/iceberg"


def seconds(iso):
    """An ISO 8601 duration (P7D, PT2M) in seconds; None if it isn't one."""
    m = re.fullmatch(r"P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?", iso or "")
    if not m:
        return None
    d, h, mi, s = (int(x or 0) for x in m.groups())
    return ((d * 24 + h) * 60 + mi) * 60 + s


def files():
    """The table's files in the warehouse: (bucket, data prefix, keys)."""
    cfg = requests.get(f"{NESSIE}/v1/config?warehouse=warehouse", timeout=10).json()
    prefix = (cfg.get("overrides") or {}).get("prefix") or (cfg.get("defaults") or {}).get("prefix")
    loc = urlparse(requests.get(f"{NESSIE}/v1/{prefix}/namespaces/{PROJECT}/tables/{TABLE}", timeout=10).json()["metadata"]["location"])
    bucket, data = loc.netloc, loc.path.strip("/") + "/data/"
    keys = set()
    for page in s3_root().get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix=data):
        keys |= {o["Key"] for o in page.get("Contents", [])}
    return bucket, data, keys


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    m = bob.get(f"{url}/maintenance", timeout=30).json()
    check("table maintenance is set up", m.get("enabled") is True, m)
    q = lambda s: sql("bob", s, "iceberg", PROJECT)

    q(f"DROP TABLE IF EXISTS {TABLE}")
    q(f"CREATE TABLE {TABLE} (id integer, region varchar)")
    for i in range(1, 8):  # a file each
        q(f"INSERT INTO {TABLE} VALUES ({i}, '{'EU' if i < 7 else 'US'}')")
    bucket, data, small = files()
    orphan = data + "left-by-a-failed-write.parquet"
    s3_root().put_object(Bucket=bucket, Key=orphan, Body=b"no branch or tag ever used this")
    try:
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
        check("... and the table is kept", run is not None and not run.get("error") and run.get("done") == ["optimize"],
              run and {k: run[k] for k in ("done", "error") if k in run})
        if not run:
            return

        # 2. Compacted, with its rows.
        b, a = run["before"], run["after"]
        check("its small files are compacted into one", b["dataFiles"] >= 7 and a["dataFiles"] == 1, f"{b['dataFiles']} -> {a['dataFiles']}")
        rows = q(f"SELECT count(*), sum(id) FROM {TABLE}")
        check("... and every row is still there", rows == [[7, 28]], rows)

        # 3. The history before it, collected.
        keep = seconds(m.get("keepHistory"))
        if keep is None or keep > 300:
            check("the install keeps the tables' history for a while", keep is not None, f"{m.get('keepHistory')} (collection itself is checked where it's minutes)")
        else:
            def collected():
                _, _, now = files()
                return now if not (small & now) and orphan not in now else None
            left = wait(collected, keep + 9 * 60, every=15)
            _, _, now = files()
            check(f"Nessie's GC deletes the files only history older than {m.get('keepHistory')} used, and a file nothing used",
                  left is not None, f"{len(small & now)} of {len(small)} small files left; the orphan {'left' if orphan in now else 'gone'}")
            rows = q(f"SELECT count(*), sum(id) FROM {TABLE}")
            check("... and every row is still there", rows == [[7, 28]], rows)
    finally:
        q(f"DROP TABLE IF EXISTS {TABLE}")
        try:
            s3_root().delete_object(Bucket=bucket, Key=orphan)
        except Exception:  # noqa: BLE001 (gone already)
            pass
    finish()


if __name__ == "__main__":
    main()
