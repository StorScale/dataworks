"""Keycloak's realm, split between the platform and the organisation:
docker compose run --rm test realm

The platform keeps its own parts of the realm (keycloak/realm.json) at each
install and upgrade (setup/realm.py); people, groups and what an
administrator adds are the organisation's, and an upgrade never writes them.
These play an administrator, then an upgrade:

  1. the administrator adds a person, a group, a mapper on the platform's
     client and an attribute of their own; and changes some of the
     platform's parts: the platform client's sign-in addresses, the token
     lifespan, and one of the operator's roles;
  2. the platform's update runs: what the administrator added is all still
     there; the platform's parts are as realm.json says again;
  3. run again, it changes nothing.

Then it takes its additions away.
"""
import json
import os
import subprocess
import sys

from lakekit import check, finish

sys.path.insert(0, "/tools/setup")
from realm import Admin, client_id, load  # noqa: E402

PERSON, GROUP, MAPPER, ATTRIBUTE = "dora-realm-test", "auditors-realm-test", "org-team", "storscale.test.note"


def update():
    """The platform's update of the realm, as setup runs it: its log lines."""
    r = subprocess.run([sys.executable, "-u", "/tools/setup/realm.py"], capture_output=True, text=True, env=os.environ)
    return r.returncode, [line for line in (r.stdout + r.stderr).splitlines() if line.strip()]


def operator_roles(kc):
    sa = kc("GET", f"/clients/{client_id(kc, 'platform-operator')}/service-account-user").json()
    rm = client_id(kc, "realm-management")
    return sa["id"], rm, {r["name"] for r in kc("GET", f"/users/{sa['id']}/role-mappings/clients/{rm}").json()}


def main():
    kc, want = Admin(), load()
    platform_want = next(c for c in want["clients"] if c["clientId"] == "platform")
    pid = client_id(kc, "platform")

    # 1. An administrator's additions, and changes to the platform's parts.
    kc("POST", "/users", json={"username": PERSON, "enabled": True, "firstName": "Dora", "lastName": "Test", "email": f"{PERSON}@example.com"})
    kc("POST", "/groups", json={"name": GROUP})
    kc("POST", f"/clients/{pid}/protocol-mappers/models", json={
        "name": MAPPER, "protocol": "openid-connect", "protocolMapper": "oidc-hardcoded-claim-mapper",
        "config": {"claim.name": "team", "claim.value": "finance", "jsonType.label": "String", "access.token.claim": "true"}})
    rep = kc("GET", f"/clients/{pid}").json()
    rep["attributes"] = {**rep.get("attributes", {}), ATTRIBUTE: "kept"}
    rep["redirectUris"] = ["https://wrong.example.com/*"]
    kc("PUT", f"/clients/{pid}", json=rep)
    kc("PUT", "", json={"accessTokenLifespan": 60})
    sa, rm, _ = operator_roles(kc)
    role = kc("GET", f"/clients/{rm}/roles/manage-clients").json()
    kc("DELETE", f"/users/{sa}/role-mappings/clients/{rm}", json=[role])

    try:
        # 2. The platform's update.
        code, lines = update()
        check("the platform's update of the realm runs", code == 0, lines[-1] if lines else "no output")
        people = [u["username"] for u in kc("GET", "/users", params={"username": PERSON, "exact": "true"}).json()]
        groups = [g["name"] for g in kc("GET", "/groups", params={"search": GROUP, "exact": "true"}).json()]
        mappers = {m["name"] for m in kc("GET", f"/clients/{pid}/protocol-mappers/models").json()}
        rep = kc("GET", f"/clients/{pid}").json()
        check("... keeps what an administrator added: a person, a group, a mapper, an attribute",
              people == [PERSON] and groups == [GROUP] and MAPPER in mappers and rep.get("attributes", {}).get(ATTRIBUTE) == "kept",
              f"person {people}, group {groups}, mapper {MAPPER in mappers}, attribute {rep.get('attributes', {}).get(ATTRIBUTE)}")
        lifespan = kc("GET", "").json().get("accessTokenLifespan")
        _, _, roles = operator_roles(kc)
        check("... and puts the platform's parts back: the client's addresses, the token lifespan, the operator's roles",
              rep["redirectUris"] == platform_want["redirectUris"] and lifespan == want["accessTokenLifespan"] and "manage-clients" in roles,
              f"redirects {rep['redirectUris']}, lifespan {lifespan}, manage-clients {'back' if 'manage-clients' in roles else 'missing'}")
        check("... and its own mappers stay as realm.json has them",
              {m["name"] for m in platform_want.get("protocolMappers", [])} <= mappers, sorted(mappers))

        # 3. Again: nothing to do.
        code, lines = update()
        changed = [line for line in lines if line.startswith("setup: realm:") and "as realm.json says" not in line]
        check("run again, it changes nothing", code == 0 and not changed, changed or "no changes")
    finally:
        for u in kc("GET", "/users", params={"username": PERSON, "exact": "true"}).json():
            kc("DELETE", f"/users/{u['id']}")
        for g in kc("GET", "/groups", params={"search": GROUP, "exact": "true"}).json():
            kc("DELETE", f"/groups/{g['id']}")
        for m in kc("GET", f"/clients/{pid}/protocol-mappers/models").json():
            if m["name"] == MAPPER:
                kc("DELETE", f"/clients/{pid}/protocol-mappers/models/{m['id']}")
    finish()


if __name__ == "__main__":
    main()
