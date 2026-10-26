"""Sign-in with the company's Entra ID (use case 6.1): docker compose run --rm test entra

The platform's setup (setup/realm.py) makes Entra ID an identity provider of
Keycloak's realm when the install names a tenant (sso.entra), its app roles
putting people in the platform's groups. CI can't reach Entra, so a stand-in
plays it: a realm of its own in the same Keycloak, whose client gives each
person their app roles in the token's roles claim, as Entra does. Under an
alias of its own (entra-test), so an install's real Entra is never touched.

  1. setup, run against the stand-in, makes the identity provider and its
     mappers (the username, a group per role); run again, it changes nothing;
  2. erin (Analyst) signs in to the platform through it, as a browser does:
     she's erin.test, in analysts, and may use the platform; frank (Engineer)
     is in engineers, an administrator; gail (no role) is refused;
  3. erin's role is taken away at the stand-in: her next sign-in takes her
     out of analysts, and she's refused.

Then it takes the stand-in, the provider and the people it brought away.
"""
import html
import json
import os
import re
import subprocess
import sys
from urllib.parse import urljoin

import requests

from lakekit import KEYCLOAK, check, finish

sys.path.insert(0, "/tools/setup")
from realm import Admin  # noqa: E402

PLATFORM = os.environ["PLATFORM_URL"]
STANDIN, ALIAS, CLIENT, SECRET = "entra-standin", "entra-test", "dataworks", "standin-client-secret"
PASSWORD = "Standin-Passw0rd!"
PEOPLE = {"erin.test": ["Analyst"], "frank.test": ["Engineer"], "gail.test": []}
ROLES = {"Analyst": ["analysts"], "Engineer": ["engineers"], "Administrator": ["engineers"]}


def master(kc, method, path, **kw):
    """The admin API above one realm (to make and remove the stand-in's)."""
    return requests.request(method, f"{KEYCLOAK}/admin/realms{path}", headers=kc.h, timeout=30, **kw)


def make_standin(kc):
    """Entra, played: a realm, a client whose roles are the app's, and three people."""
    master(kc, "DELETE", f"/{STANDIN}")
    r = master(kc, "POST", "", json={"realm": STANDIN, "enabled": True, "sslRequired": "none"})
    r.raise_for_status()
    master(kc, "POST", f"/{STANDIN}/clients", json={
        "clientId": CLIENT, "enabled": True, "publicClient": False, "secret": SECRET, "standardFlowEnabled": True,
        "redirectUris": [f"{PLATFORM}/sso/realms/lakehouse/broker/{ALIAS}/endpoint"],
        "protocolMappers": [{"name": "app roles", "protocol": "openid-connect", "protocolMapper": "oidc-usermodel-client-role-mapper",
                             "config": {"usermodel.clientRoleMapping.clientId": CLIENT, "claim.name": "roles", "multivalued": "true",
                                        "jsonType.label": "String", "id.token.claim": "true", "access.token.claim": "true"}}]}).raise_for_status()
    cid = master(kc, "GET", f"/{STANDIN}/clients", params={"clientId": CLIENT}).json()[0]["id"]
    for role in ROLES:
        master(kc, "POST", f"/{STANDIN}/clients/{cid}/roles", json={"name": role}).raise_for_status()
    for name, roles in PEOPLE.items():
        first = name.split(".")[0].title()
        master(kc, "POST", f"/{STANDIN}/users", json={
            "username": f"{name}@harbor.example", "email": f"{name}@harbor.example", "firstName": first, "lastName": "Test",
            "enabled": True, "emailVerified": True,
            "credentials": [{"type": "password", "value": PASSWORD, "temporary": False}]}).raise_for_status()
        set_roles(kc, name, roles)
    return cid


def set_roles(kc, name, roles):
    cid = master(kc, "GET", f"/{STANDIN}/clients", params={"clientId": CLIENT}).json()[0]["id"]
    uid = master(kc, "GET", f"/{STANDIN}/users", params={"username": f"{name}@harbor.example", "exact": "true"}).json()[0]["id"]
    have = master(kc, "GET", f"/{STANDIN}/users/{uid}/role-mappings/clients/{cid}").json()
    if have:
        master(kc, "DELETE", f"/{STANDIN}/users/{uid}/role-mappings/clients/{cid}", json=have)
    if roles:
        reps = [master(kc, "GET", f"/{STANDIN}/clients/{cid}/roles/{r}").json() for r in roles]
        master(kc, "POST", f"/{STANDIN}/users/{uid}/role-mappings/clients/{cid}", json=reps).raise_for_status()


def setup():
    """The platform's setup of the realm, pointed at the stand-in: its log lines."""
    e = {**os.environ, "ENTRA_AUTHORITY": f"{PLATFORM}/sso/realms/{STANDIN}", "ENTRA_ALIAS": ALIAS,
         "ENTRA_CLIENT_ID": CLIENT, "ENTRA_CLIENT_SECRET": SECRET, "ENTRA_DISPLAY_NAME": "Harbor (stand-in)",
         "ENTRA_ROLES": json.dumps(ROLES)}
    r = subprocess.run([sys.executable, "-u", "/tools/setup/realm.py"], capture_output=True, text=True, env=e)
    return r.returncode, [line for line in (r.stdout + r.stderr).splitlines() if line.strip()]


def sign_in(person):
    """The platform, signed in through the stand-in as a browser would: Keycloak's
    page, its button for the provider, the stand-in's form. The session's answer."""
    s = requests.Session()
    r = s.get(f"{PLATFORM}/auth/login?next=/", timeout=30)
    link = re.search(r'href="([^"]*/broker/' + re.escape(ALIAS) + r'/login[^"]*)"', r.text)
    if not link:
        return None, "no button for the provider on Keycloak's page"
    r = s.get(urljoin(r.url, html.unescape(link.group(1))), timeout=30)
    form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
    if not form:
        return None, f"no stand-in sign-in form ({r.url})"
    r = s.post(html.unescape(form.group(1)), timeout=60,
               data={"username": f"{person}@harbor.example", "password": PASSWORD, "credentialId": ""})
    if "/broker/" in r.url or "login-actions" in r.url:
        title = re.search(r"<title>([^<]*)", r.text)
        return None, f"stopped at Keycloak: {r.url.split('?')[0]} ({title.group(1).strip() if title else ''})"
    a = s.get(f"{PLATFORM}/api/session", headers={"X-Platform-Request": "1"}, timeout=30)
    return a.status_code, a.json()


def lakehouse_user(kc, name):
    found = kc("GET", "/users", params={"username": name, "exact": "true"}).json()
    return found[0] if found else None


def groups_of(kc, name):
    u = lakehouse_user(kc, name)
    return sorted(g["name"] for g in kc("GET", f"/users/{u['id']}/groups").json()) if u else None


def main():
    kc = Admin()
    for name in PEOPLE:   # left by a run that stopped
        u = lakehouse_user(kc, name)
        if u:
            kc("DELETE", f"/users/{u['id']}")
    master(kc, "DELETE", f"/lakehouse/identity-provider/instances/{ALIAS}")   # 404 when there's none: fine
    try:
        make_standin(kc)

        # 1. The platform's setup makes the provider and its mappers; again, nothing.
        rc, lines = setup()
        idp = kc("GET", f"/identity-provider/instances/{ALIAS}")
        mappers = sorted(m["name"] for m in kc("GET", f"/identity-provider/instances/{ALIAS}/mappers").json()) if idp.ok else []
        check("setup makes the Entra ID provider from the tenant's discovery document, with its mappers (username, a group per role)",
              rc == 0 and idp.ok and idp.json()["config"]["issuer"] == f"{PLATFORM}/sso/realms/{STANDIN}"
              and mappers == ["dataworks: role Administrator -> engineers", "dataworks: role Analyst -> analysts",
                              "dataworks: role Engineer -> engineers", "dataworks: username"],
              (rc, [x for x in lines if "identity provider" in x or "Entra" in x][-6:], mappers))
        rc, lines = setup()
        check("... and run again, changes nothing", rc == 0 and not [x for x in lines if "identity provider" in x], lines[-3:])

        # 2. Signing in through it.
        code, body = sign_in("erin.test")
        check("erin (Analyst in Entra) signs in through it: she's erin.test, in analysts, and may use the platform",
              code == 200 and body["user"]["username"] == "erin.test" and "analysts" in body["user"]["groups"] and not body.get("admin"),
              (code, body if code != 200 else {k: body[k] for k in ("user", "admin")}))
        code, body = sign_in("frank.test")
        check("frank (Engineer) is in engineers: an administrator", code == 200 and "engineers" in body["user"]["groups"] and body.get("admin"),
              (code, body if code != 200 else body["user"]["groups"]))
        code, body = sign_in("gail.test")
        check("gail (no app role) signs in, and is refused the platform", code == 403 and groups_of(kc, "gail.test") == [],
              (code, body, groups_of(kc, "gail.test")))

        # 3. A role taken away in Entra takes its group away at the next sign-in.
        set_roles(kc, "erin.test", [])
        code, body = sign_in("erin.test")
        check("erin's Analyst role taken away in Entra: her next sign-in takes her out of analysts, and she's refused",
              code == 403 and "analysts" not in (groups_of(kc, "erin.test") or []), (code, groups_of(kc, "erin.test")))
    finally:
        for name in PEOPLE:
            u = lakehouse_user(kc, name)
            if u:
                kc("DELETE", f"/users/{u['id']}")
        master(kc, "DELETE", f"/lakehouse/identity-provider/instances/{ALIAS}")
        master(kc, "DELETE", f"/{STANDIN}")
    finish()


if __name__ == "__main__":
    main()
