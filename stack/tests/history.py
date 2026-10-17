"""The data as it was, end to end: docker compose run --rm test history

Nessie keeps the catalog's history as its commits; a table is read as it was
at one through its name ("orders#<commit>"), and a project's tags keep
moments for as long as it says. These check, with a table of sales' own:

  1. bob tags a moment, and the operator makes the Nessie tag;
  2. the table's history shows its changes, the tag on its own, and who
     wrote them;
  3. the tag stands for its commit, and the table read there is as it was;
  4. alice (a reader) sees the tags and can't make one;
  5. a tag past its keepUntil goes when maintenance runs, and a tag removed
     goes from Nessie.

The table, and the tags, are removed after.
"""
import json
import os
import time

import requests

from lakekit import check, finish, sql

from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
NESSIE = "http://nessie:19120/api/v2"
PROJECT, TABLE = "sales", "hist_check"


def nessie_tag(name):
    r = requests.get(f"{NESSIE}/trees/{PROJECT}.{name}", timeout=10)
    return r.json()["reference"]["hash"] if r.status_code == 200 else None


def main():
    bob, alice = platform("bob"), platform("alice")
    url = f"{PLATFORM}/api/projects/{PROJECT}"
    q = lambda s: sql("bob", s, "iceberg", PROJECT)
    tags = lambda s: {t["name"]: t for t in s.get(f"{url}/tags", timeout=30).json().get("tags", [])}
    q(f"DROP TABLE IF EXISTS {TABLE}")
    q(f"CREATE TABLE {TABLE} (id integer)")
    q(f"INSERT INTO {TABLE} VALUES (1)")
    try:
        # 1. A tag.
        r = bob.post(f"{url}/tags", json={"name": "hist-first", "at": "now", "keep": "1d", "note": "one row"}, timeout=30)
        check("bob tags the tables as they are now", r.status_code == 201, r.text[:200])
        tagged = r.json().get("commit") if r.ok else None
        made = wait(lambda: nessie_tag("hist-first") == tagged and tags(bob).get("hist-first", {}).get("inPlace"), 180, every=5)
        check("... and the operator makes it in Nessie (sales.hist-first)", made, nessie_tag("hist-first"))
        q(f"INSERT INTO {TABLE} VALUES (2)")

        # 2. The table's history.
        # Who wrote each change comes from the audit log, which Trino's Ranger
        # plugin writes in batches: a few seconds behind.
        hist = lambda: bob.get(f"{url}/history", params={"table": TABLE}, timeout=60).json().get("changes", [])
        changes = wait(lambda: (lambda cs: cs if any(c.get("who") for c in cs[:2]) else None)(hist()), 90, every=5) or hist()
        check("the table's history has its changes, newest first", len(changes) >= 2 and changes[0]["time"] >= changes[1]["time"],
              [(c["hash"][:8], c.get("stats", {}).get("addedRows")) for c in changes[:4]])
        check("... the newest added a row", (changes[0].get("stats") or {}).get("addedRows") == 1, changes[:1])
        check("... and the tag is on the change it keeps", any("hist-first" in (c.get("tags") or []) for c in changes),
              [(c["hash"][:8], c.get("tags")) for c in changes[:4]])
        check("... and who wrote it, from the audit log", any(c.get("who") == "bob" for c in changes[:2]),
              [(c["hash"][:8], c.get("who")) for c in changes[:4]])

        # 3. As it was.
        m = bob.get(f"{url}/asof", params={"at": "hist-first"}, timeout=30).json()
        check("the tag stands for its commit", m.get("commit") == tagged, m)
        then, now = q(f'SELECT count(*) FROM "{TABLE}#{tagged}"'), q(f"SELECT count(*) FROM {TABLE}")
        check("... and the table read there is as it was (1 row; 2 now)", then == [[1]] and now == [[2]], f"{then} then, {now} now")

        # 4. A reader.
        r = alice.get(f"{url}/tags", timeout=30)
        check("alice (a reader) sees the project's tags", r.ok and "hist-first" in {t["name"] for t in r.json().get("tags", [])}, r.text[:200])
        code = alice.post(f"{url}/tags", json={"name": "alices", "at": "now", "keep": "1d", "note": ""}, timeout=30).status_code
        check("... and can't make one", code == 403, f"HTTP {code}")

        # 5. Past its keep, and removed.
        spec = bob.get(url, timeout=30).json()["spec"]
        yesterday = time.strftime("%Y-%m-%d", time.gmtime(time.time() - 86400))
        spec["spec"]["tables"].setdefault("tags", []).append({"name": "hist-past", "commit": tagged, "keepUntil": yesterday, "by": "bob"})
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        made = wait(lambda: nessie_tag("hist-past") is not None, 180, every=5)
        check("a tag kept until yesterday is made", r.ok and made, r.text[:200])
        bob.post(f"{url}/maintenance", timeout=30)  # maintenance removes the tags past their keep, as each night
        gone = wait(lambda: "hist-past" not in tags(bob) and nessie_tag("hist-past") is None, 180, every=5)
        check("... and goes when maintenance runs, from the project and Nessie", gone, list(tags(bob)))
        r = bob.delete(f"{url}/tags/hist-first", timeout=30)
        gone = wait(lambda: nessie_tag("hist-first") is None, 180, every=5)
        check("bob removes a tag, and it goes from Nessie", r.status_code == 204 and gone, f"HTTP {r.status_code}")
    finally:
        for name in ("hist-first", "hist-past"):
            bob.delete(f"{url}/tags/{name}", timeout=30)
        q(f"DROP TABLE IF EXISTS {TABLE}")
    finish()


if __name__ == "__main__":
    main()
