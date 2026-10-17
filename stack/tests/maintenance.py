"""Table maintenance, end to end: docker compose run --rm test maintenance

platformd keeps every project's tables each night, as Trino's maintenance
account: their small files compacted. These check, on a table of sales' own
made for the purpose:

  1. alice (a reader) can't ask for it; bob (an editor) can, and it runs;
  2. the table's small files are compacted into one, its rows intact.

(Its history, and files nothing uses, are Nessie's to collect, across the
install.) The table is dropped after.
"""
import calendar
import os
import time

from lakekit import check, finish, sql

from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
PROJECT, TABLE = "sales", "maint_check"


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
    finally:
        q(f"DROP TABLE IF EXISTS {TABLE}")
    finish()


if __name__ == "__main__":
    main()
