"""A backup, and a restore from it, end to end (Kubernetes): run in three
phases around storscale backup and storscale restore, as CI does.

    storscale test --k8s restore_mark      something in each place a backup keeps
    storscale backup --k8s
    storscale test --k8s restore_damage    all of it removed
    storscale restore --k8s --key KEY --yes
    storscale test --k8s restore_check     all of it back

The places: Nessie's catalog (a tag), Keycloak's database (a person),
Ranger's (a policy), the Projects (a project, applied again), and the data
(a file in sales' bucket, copied off and back: storscale restore --data).
"""
import json
import os
import sys

import requests

from lakekit import KEYCLOAK, REALM, allow, check, env, finish, ranger, res, s3_root, sql
from projects import platform, wait

PLATFORM = os.environ["PLATFORM_URL"]
NESSIE = "http://nessie:19120/api/v2"
TAG, PERSON, POLICY, PROJECT = "restore-check", "restore-check", "restore-check: a policy kept", "restore-check"
FILE = ("sales", "restore-check/kept.txt")


def file():
    try:
        return s3_root().get_object(Bucket=FILE[0], Key=FILE[1])["Body"].read().decode()
    except Exception:  # noqa: BLE001 (gone)
        return None


def keycloak():
    tok = requests.post(f"{KEYCLOAK}/realms/master/protocol/openid-connect/token", timeout=10, data={
        "grant_type": "password", "client_id": "admin-cli", "username": "admin", "password": env["KEYCLOAK_ADMIN_PASSWORD"]})
    tok.raise_for_status()
    return f"{KEYCLOAK}/admin/realms/{REALM}", {"Authorization": f"Bearer {tok.json()['access_token']}"}


def person():
    base, h = keycloak()
    found = requests.get(f"{base}/users", params={"username": PERSON, "exact": "true"}, headers=h, timeout=10).json()
    return found[0] if found else None


def tag():
    r = requests.get(f"{NESSIE}/trees/{TAG}", timeout=10)
    return r.json()["reference"] if r.status_code == 200 else None


def policy():
    found = ranger("GET", "/service/public/v2/api/service/lakehouse/policy", params={"policyName": POLICY})
    return found[0] if found else None


def project(s):
    r = s.get(f"{PLATFORM}/api/projects/{PROJECT}", timeout=30)
    return r.json() if r.ok else None


def mark():
    main = requests.get(f"{NESSIE}/trees/main", timeout=10).json()["reference"]
    r = requests.post(f"{NESSIE}/trees", params={"name": TAG, "type": "TAG"}, json=main, timeout=10)
    check("a Nessie tag (the catalog)", r.ok or tag() is not None, r.text[:200])
    base, h = keycloak()
    r = requests.post(f"{base}/users", headers=h, json={"username": PERSON, "enabled": True, "firstName": "Restore", "lastName": "Check",
                                                       "email": "restore-check@example.com"}, timeout=10)
    check("a person in Keycloak", r.status_code in (201, 409), f"HTTP {r.status_code}")
    if policy() is None:
        ranger("POST", "/service/public/v2/api/policy", json={
            "service": "lakehouse", "name": POLICY, "isEnabled": True, "policyType": 0,
            "resources": res(catalog="iceberg", schema="restore_check", table="kept", column="*"),
            "policyItems": [allow(["select"], users=["alice"])]})
    check("a Ranger policy", policy() is not None)
    bob = platform("bob")
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": PROJECT},
            "spec": {"description": "Kept by a backup.", "members": [{"group": "engineers", "role": "editor"}],
                     "tables": {"namespace": "restore_check"}}}
    r = bob.put(f"{PLATFORM}/api/projects/{PROJECT}", data=json.dumps(spec), timeout=30)
    ready = wait(lambda: (project(bob) or {}).get("status", {}).get("phase") == "Ready", 180, every=5)
    check("a project, applied", r.ok and ready, r.text[:200])
    s3_root().put_object(Bucket=FILE[0], Key=FILE[1], Body=b"kept by a backup")
    check("a file in sales' bucket (the data)", file() == "kept by a backup")


def damage():
    t = tag()
    if t:
        requests.delete(f"{NESSIE}/trees/{TAG}@{t['hash']}", timeout=10)
    check("the tag is gone", tag() is None)
    p = person()
    if p:
        base, h = keycloak()
        requests.delete(f"{base}/users/{p['id']}", headers=h, timeout=10)
    check("the person is gone", person() is None)
    pol = policy()
    if pol:
        ranger("DELETE", f"/service/public/v2/api/policy/{pol['id']}")
    check("the policy is gone", policy() is None)
    bob = platform("bob")
    bob.delete(f"{PLATFORM}/api/projects/{PROJECT}", timeout=30)
    gone = wait(lambda: project(bob) is None, 180, every=5)
    check("the project is gone (undone by the operator)", gone)
    s3_root().delete_object(Bucket=FILE[0], Key=FILE[1])
    check("the file is gone", file() is None)


def restored():
    check("the Nessie tag is back", tag() is not None)
    check("the person is back in Keycloak", person() is not None)
    check("the Ranger policy is back", policy() is not None)
    bob = platform("bob")
    ready = wait(lambda: (project(bob) or {}).get("status", {}).get("phase") == "Ready", 300, every=5)
    check("the project is back, and applied", ready, project(bob))
    check("the file is back in sales' bucket (the data, copied back)", file() == "kept by a backup", file())
    rows = sql("bob", "SELECT count(*) FROM orders", "iceberg", "sales")
    check("the tables still read (the catalog in step with the data)", rows and rows[0][0] > 0, rows)
    # Leave nothing behind.
    damage()


if __name__ == "__main__":
    {"restore_mark": mark, "restore_damage": damage, "restore_check": restored}[sys.argv[1]]()
    finish()
