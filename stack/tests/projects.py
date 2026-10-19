"""Projects, end to end: docker compose run --rm test projects

A project is a team's tables, files and pipelines, and who may use them. An
administrator (bob, an engineer) creates, changes and deletes projects in
the platform; the platform operator applies them to Keycloak, Ranger,
Buckets and Nessie. These check that what a project says is what people get:

  1. the sales project (projects/sales.json, made at setup) is ready, with
     its members: alice reads, bob edits, the pipelines' service account runs;
  2. only administrators change projects; people see the projects they're
     in; a spec with mistakes is refused, saying what's wrong;
  3. a new project, marketing, with bob alone: bob gets its tables and its
     bucket; alice gets neither, and doesn't see it;
  4. deleting sales takes its access away, in every system: alice and bob
     lose its tables in Trino (Ranger) and its files in Buckets, the
     pipelines lose its landing/, and the project's Keycloak groups go. Its
     data stays;
  5. creating sales again gives it all back.
"""
import base64
import html
import json
import os
import re
import time

import requests

from lakekit import check, code, denied, env, finish, s3_as, s3_root, service_token, sql, token

PLATFORM = os.environ["PLATFORM_URL"]
SALES = open("/projects/sales.json").read()
MARKETING = json.dumps({
    "apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": "marketing"},
    "spec": {"members": [{"user": "bob", "role": "editor"}], "tables": {}, "files": {}}})


def platform(user):
    """A platform session, signed in through Keycloak's form."""
    s = requests.Session()
    r = s.get(f"{PLATFORM}/auth/login?next=/", timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    s.post(html.unescape(form.group(1)), timeout=60,
           data={"username": user, "password": env["LAKEHOUSE_USER_PASSWORD"], "credentialId": ""})
    s.headers["X-Platform-Request"] = "1"
    return s


def projects(s):
    r = s.get(f"{PLATFORM}/api/projects", timeout=30)
    return {p["name"]: p for p in r.json()["projects"]} if r.ok else r.status_code


def wait(fn, timeout=150, every=3):
    deadline = time.time() + timeout
    while True:
        got = fn()
        if got or time.time() > deadline:
            return got
        time.sleep(every)


def ready(s, name):
    return wait(lambda: (lambda p: p if p and (p.get("status") or {}).get("phase") == "Ready" else None)(
        (projects(s) or {}).get(name) if isinstance(projects(s), dict) else None))


def gone(s, name):
    return wait(lambda: name not in projects(s))


def groups_in_token(user):
    claims = token(user).split(".")[1]
    return json.loads(base64.urlsafe_b64decode(claims + "=" * (-len(claims) % 4))).get("groups", [])


def runs(user, statement, schema):
    """Whether Trino runs the statement for the user."""
    try:
        sql(user, statement, catalog="iceberg", schema=schema)
        return True
    except Exception:  # noqa: BLE001 (denied, or not there)
        return False


def sales_regions(user):
    try:
        return {r[0] for r in sql(user, "SELECT region FROM orders", catalog="iceberg", schema="sales")}
    except Exception as e:  # noqa: BLE001
        return str(e).splitlines()[0][:100]


def pipelines_landing():
    tok = service_token("airflow-pipelines", env["AIRFLOW_PIPELINES_SECRET"])
    return code(lambda: s3_as("pipelines", tok).put_object(Bucket="sales", Key="landing/check.txt", Body=b"x"))


def main():
    bob, alice, carol = platform("bob"), platform("alice"), platform("carol")

    # 1. The sales project.
    p = ready(bob, "sales")
    members = {m["username"]: m["role"] for m in ((p or {}).get("status") or {}).get("members", [])}
    check("the sales project is ready in every system",
          p and all(part["ok"] for part in p["status"]["parts"]),
          ", ".join(f"{x['system']} {'ok' if x['ok'] else x['message']}" for x in (p or {}).get("status", {}).get("parts", [])))
    check("... with its members: alice reads, bob edits, the pipelines run",
          members == {"alice": "reader", "bob": "editor", "service-account-airflow-pipelines": "pipelines"}, members)

    # 2. Who may change projects, and who sees which.
    mine = projects(alice)
    check("people see the projects they're in, with their role",
          isinstance(mine, dict) and mine.get("sales", {}).get("role") == "reader", {k: v.get("role") for k, v in (mine or {}).items()} if isinstance(mine, dict) else mine)
    put = alice.put(f"{PLATFORM}/api/projects/marketing", data=MARKETING, timeout=30).status_code
    rm = alice.delete(f"{PLATFORM}/api/projects/sales", timeout=30).status_code
    check("only administrators create and delete projects", put == 403 and rm == 403, f"alice: create HTTP {put}, delete HTTP {rm}")
    check("people in neither group can't use projects", projects(carol) == 403, f"carol: HTTP {projects(carol)}")
    r = bob.put(f"{PLATFORM}/api/projects/oops", timeout=30,
                data=json.dumps({"metadata": {"name": "oops"}, "spec": {"members": [{"group": "engineers", "role": "owner"}]}}))
    check("a spec with mistakes is refused, saying what's wrong",
          r.status_code == 422 and "owner" in r.text, f"HTTP {r.status_code} {r.json().get('error', '')[:120]}")

    # 3. A new project: bob's alone.
    put = bob.put(f"{PLATFORM}/api/projects/marketing", data=MARKETING, timeout=30).status_code
    check("an administrator creates a project, and it's applied", put == 200 and ready(bob, "marketing"), f"HTTP {put}")
    made = wait(lambda: runs("bob", "CREATE TABLE IF NOT EXISTS campaigns (name varchar)", "marketing"), 90)
    wrote = code(lambda: s3_as("bob").put_object(Bucket="marketing", Key="plan.txt", Body=b"launch"))
    check("... its member gets its tables and its bucket", made and wrote == "OK", f"table {'made' if made else 'denied'}, file {wrote}")
    no_table = denied("alice", "SELECT * FROM campaigns", catalog="iceberg", schema="marketing")
    no_file = code(lambda: s3_as("alice").get_object(Bucket="marketing", Key="plan.txt"))
    check("... and nobody else does", no_table and no_file == "AccessDenied", f"alice: {no_table}; file {no_file}")
    check("... nor sees it", "marketing" not in projects(alice), sorted(projects(alice)))

    # 4. Deleting sales takes its access away.
    rm = bob.delete(f"{PLATFORM}/api/projects/sales", timeout=30).status_code
    check("an administrator deletes a project, and it's undone", rm == 204 and gone(bob, "sales"), f"HTTP {rm}")
    lost = wait(lambda: not isinstance(sales_regions("alice"), set), 120)
    check("... alice loses its tables", lost, f"alice: {sales_regions('alice')}")
    # The editors' policy goes with the readers', but Trino refreshes its policies every half minute: wait for it as for alice's.
    no_insert = wait(lambda: denied("bob", "INSERT INTO orders VALUES (99, 'Zed', '4000', 'EU', 1, DATE '2026-09-30')", catalog="iceberg", schema="sales"), 120)
    check("... bob can't change them", no_insert, no_insert)
    files = code(lambda: s3_as("alice").get_object(Bucket="sales", Key="datasets/orders.parquet"))
    check("... nor read its files", files == "AccessDenied", f"alice: {files}")
    landing = pipelines_landing()
    # Refused either way: by Buckets, or by its STS (whose token then names no group with a policy).
    check("... the pipelines lose its landing/", landing != "OK", f"the service account: {landing}")
    check("... and the project's groups are gone from Keycloak", "sales-readers" not in groups_in_token("alice"), groups_in_token("alice"))
    kept = code(lambda: s3_root().head_object(Bucket="sales", Key="datasets/orders.parquet"))
    check("... while its data stays", kept == "OK", f"sales/datasets/orders.parquet: {kept}")

    # 5. Creating it again gives it all back.
    put = bob.put(f"{PLATFORM}/api/projects/sales", data=SALES, timeout=30).status_code
    check("creating sales again applies it", put == 200 and ready(bob, "sales"), f"HTTP {put}")
    back = wait(lambda: sales_regions("alice") == {"EU"}, 120)
    check("... alice reads its tables again, EU only", back, f"alice: {sales_regions('alice')}")
    files = code(lambda: s3_as("alice").get_object(Bucket="sales", Key="datasets/orders.parquet"))
    check("... and its files", files == "OK", f"alice: {files}")
    check("... and the pipelines their landing/", pipelines_landing() == "OK", "")

    bob.delete(f"{PLATFORM}/api/projects/marketing", timeout=30)
    gone(bob, "marketing")
    finish()


if __name__ == "__main__":
    main()
