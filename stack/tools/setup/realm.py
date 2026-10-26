"""Keep the platform's own parts of Keycloak's realm as keycloak/realm.json
says, at every install and upgrade.

The realm's ownership is split (docs/plan.md, "Phase 6: Keycloak for real
use"). Keycloak imports realm.json only when there's no realm yet; after
that, people and their sign-in belong to the organisation. This step keeps
what's the platform's:

  - the realm's settings the tools rely on (token lifespan, TLS);
  - each tool's client: made if it's missing, else only the fields
    realm.json sets are changed (redirect addresses, flows, its secret), and
    its mappers by name; fields and mappers someone added stay;
  - the platform's service accounts: the roles and groups realm.json gives
    them are added, none taken away.

Users, groups' members, policies and themes are never written. Nor the
signing key: the import made it, and the realm keeps it. Identity providers
neither, but one the install asks for: sign-in with the company's Entra ID
(use case 6.1), when ENTRA_AUTHORITY names its tenant:

  - an identity provider, entra (OpenID Connect), its endpoints from the
    authority's discovery document, its client ENTRA_CLIENT_ID and
    ENTRA_CLIENT_SECRET (the app registration's), shown on the sign-in page
    as ENTRA_DISPLAY_NAME;
  - a person's username their Entra login name's local part
    (russell.myers@vericast.com: russell.myers);
  - their groups from the app registration's roles (the token's roles
    claim), as ENTRA_ROLES maps them ({"Analyst": ["analysts"], ...}), set at
    each sign-in: a role taken away in Entra takes its groups away.

Mappers someone added to it stay; those the platform made and the mapping no
longer names go.

REALM_FILE: realm.json (/keycloak/realm.json). Placeholders in it, as
${NAME} or ${NAME:default}, are filled from the environment, as Keycloak's
own import fills them.
"""
import json
import os
import re
import sys

import requests

from lakekit import KEYCLOAK, REALM, env, log

REALM_FILE = os.environ.get("REALM_FILE", "/keycloak/realm.json")
SETTINGS = ("enabled", "sslRequired", "accessTokenLifespan")


def load():
    text = open(REALM_FILE).read()
    return json.loads(re.sub(r"\$\{([A-Za-z0-9_]+)(?::([^}]*))?\}",
                             lambda m: env.get(m.group(1), m.group(2) or ""), text))


class Admin:
    """Keycloak's admin API, as the master realm's admin."""

    def __init__(self):
        r = requests.post(f"{KEYCLOAK}/realms/master/protocol/openid-connect/token", timeout=30, data={
            "grant_type": "password", "client_id": "admin-cli",
            "username": "admin", "password": env["KEYCLOAK_ADMIN_PASSWORD"]})
        r.raise_for_status()
        self.h = {"Authorization": f"Bearer {r.json()['access_token']}"}
        self.base = f"{KEYCLOAK}/admin/realms/{REALM}"

    def __call__(self, method, path, **kw):
        r = requests.request(method, self.base + path, headers=self.h, timeout=30, **kw)
        if r.status_code >= 400 and not (method == "GET" and r.status_code == 404):
            # Keycloak's errors name the field, not secrets: never the body we sent.
            sys.exit(f"realm: {method} {path}: HTTP {r.status_code} {r.text[:200]}")
        return r


def same(a, b):
    """Equal, but a list of strings in any order: Keycloak keeps a client's
    addresses as a set, and hands them back in an order of its own."""
    if isinstance(a, list) and isinstance(b, list) and all(isinstance(x, str) for x in a + b):
        return sorted(a) == sorted(b)
    if isinstance(a, dict) and isinstance(b, dict):
        return a.keys() == b.keys() and all(same(a[k], b[k]) for k in a)
    return a == b


def overlay(have, want):
    """have with want's fields set; a map (attributes, a mapper's config) is
    set key by key, so keys Keycloak or an administrator added stay."""
    out = dict(have)
    for k, v in want.items():
        out[k] = {**(have.get(k) or {}), **v} if isinstance(v, dict) else v
    return out


def client_id(kc, client):
    found = kc("GET", "/clients", params={"clientId": client}).json()
    return found[0]["id"] if found else None


def apply_client(kc, want):
    want = dict(want)
    mappers = want.pop("protocolMappers", [])
    cid = client_id(kc, want["clientId"])
    if cid is None:
        kc("POST", "/clients", json={**want, "protocolMappers": mappers})
        log(f"realm: client {want['clientId']} made")
        return
    have = kc("GET", f"/clients/{cid}").json()
    merged = overlay(have, want)
    if not same(merged, have):
        kc("PUT", f"/clients/{cid}", json=merged)
        log(f"realm: client {want['clientId']}: " + ", ".join(sorted(k for k in want if not same(have.get(k), merged[k]))))
    existing = {m["name"]: m for m in kc("GET", f"/clients/{cid}/protocol-mappers/models").json()}
    for m in mappers:
        cur = existing.get(m["name"])
        if cur is None:
            kc("POST", f"/clients/{cid}/protocol-mappers/models", json=m)
            log(f"realm: client {want['clientId']}: mapper {m['name']} added")
        elif not same(overlay(cur, m), cur):
            kc("PUT", f"/clients/{cid}/protocol-mappers/models/{cur['id']}", json=overlay(cur, m))
            log(f"realm: client {want['clientId']}: mapper {m['name']} put back")


def group_id(kc, name):
    for g in kc("GET", "/groups", params={"search": name, "exact": "true", "briefRepresentation": "true"}).json():
        if g["name"] == name:
            return g["id"]
    kc("POST", "/groups", json={"name": name})
    log(f"realm: group {name} made")
    return group_id(kc, name)


def apply_service_account(kc, user):
    cid = client_id(kc, user["serviceAccountClientId"])
    if cid is None:
        sys.exit(f"realm: no client {user['serviceAccountClientId']} for its service account")
    sa = kc("GET", f"/clients/{cid}/service-account-user").json()
    for client, roles in (user.get("clientRoles") or {}).items():
        rid = client_id(kc, client)
        have = {r["name"] for r in kc("GET", f"/users/{sa['id']}/role-mappings/clients/{rid}").json()}
        missing = [kc("GET", f"/clients/{rid}/roles/{name}").json() for name in roles if name not in have]
        if missing:
            kc("POST", f"/users/{sa['id']}/role-mappings/clients/{rid}", json=missing)
            log(f"realm: {sa['username']}: {client} roles {', '.join(r['name'] for r in missing)} added")
    have = {g["name"] for g in kc("GET", f"/users/{sa['id']}/groups").json()}
    for name in user.get("groups") or []:
        name = name.lstrip("/")
        if name not in have:
            kc("PUT", f"/users/{sa['id']}/groups/{group_id(kc, name)}")
            log(f"realm: {sa['username']}: in group {name}")


ENTRA = env.get("ENTRA_ALIAS") or "entra"   # another only for the entra suite's stand-in, so a real one is never touched
MINE = "dataworks: "   # the prefix of the identity provider's mappers the platform makes


def entra_mappers():
    """The identity provider's mappers the platform keeps: the username, and a group per role."""
    out = [{"name": MINE + "username", "identityProviderAlias": ENTRA, "identityProviderMapper": "oidc-username-idp-mapper",
            "config": {"syncMode": "INHERIT", "template": "${CLAIM.preferred_username | localpart}", "target": "LOCAL"}}]
    roles = json.loads(env.get("ENTRA_ROLES") or "{}")
    for role, groups in sorted(roles.items()):
        for g in groups:
            out.append({"name": f"{MINE}role {role} -> {g}", "identityProviderAlias": ENTRA,
                        "identityProviderMapper": "oidc-advanced-group-idp-mapper",
                        "config": {"syncMode": "FORCE", "claims": json.dumps([{"key": "roles", "value": role}]),
                                   "are.claim.values.regex": "false", "group": "/" + g}})
    return out


def apply_entra(kc):
    """Sign-in with Entra ID, when the install names its tenant (ENTRA_AUTHORITY)."""
    authority = (env.get("ENTRA_AUTHORITY") or "").rstrip("/")
    if not authority:
        return
    for k in ("ENTRA_CLIENT_ID", "ENTRA_CLIENT_SECRET"):
        if not env.get(k):
            sys.exit(f"realm: sign-in with Entra ID needs {k} (the app registration's)")
    r = requests.get(authority + "/.well-known/openid-configuration", timeout=30)
    if not r.ok:
        sys.exit(f"realm: Entra ID's discovery document at {authority}: HTTP {r.status_code}")
    d = r.json()
    want = {"alias": ENTRA, "displayName": env.get("ENTRA_DISPLAY_NAME") or "Microsoft Entra ID", "providerId": "oidc",
            "enabled": True, "trustEmail": True, "storeToken": False, "firstBrokerLoginFlowAlias": "first broker login",
            "config": {"clientId": env["ENTRA_CLIENT_ID"], "clientSecret": env["ENTRA_CLIENT_SECRET"],
                       "clientAuthMethod": "client_secret_post", "issuer": d["issuer"],
                       "authorizationUrl": d["authorization_endpoint"], "tokenUrl": d["token_endpoint"],
                       "jwksUrl": d["jwks_uri"], "logoutUrl": d.get("end_session_endpoint", ""),
                       "useJwksUrl": "true", "validateSignature": "true", "pkceEnabled": "true", "pkceMethod": "S256",
                       "defaultScope": "openid profile email", "disableUserInfo": "true", "syncMode": "FORCE"}}
    have = kc("GET", f"/identity-provider/instances/{ENTRA}")
    if have.status_code == 404:
        kc("POST", "/identity-provider/instances", json=want)
        log(f"realm: identity provider {ENTRA} made ({d['issuer']})")
    else:
        cur = have.json()
        merged = overlay(cur, want)
        # Keycloak hands the secret back masked: it's sent again whenever anything else changed.
        changed = sorted(k for k in want if k != "config" and not same(cur.get(k), want[k]))
        changed += sorted("config." + k for k in want["config"] if k != "clientSecret" and cur.get("config", {}).get(k) != want["config"][k])
        if changed:
            kc("PUT", f"/identity-provider/instances/{ENTRA}", json=merged)
            log(f"realm: identity provider {ENTRA}: " + ", ".join(changed))
    existing = {m["name"]: m for m in kc("GET", f"/identity-provider/instances/{ENTRA}/mappers").json()}
    wanted = entra_mappers()
    for g in {grp for groups in json.loads(env.get("ENTRA_ROLES") or "{}").values() for grp in groups}:
        group_id(kc, g)   # a role's group, made if the realm has none yet
    for m in wanted:
        cur = existing.get(m["name"])
        if cur is None:
            kc("POST", f"/identity-provider/instances/{ENTRA}/mappers", json=m)
            log(f"realm: identity provider {ENTRA}: mapper {m['name'][len(MINE):]} added")
        elif not same(overlay(cur, m), cur):
            kc("PUT", f"/identity-provider/instances/{ENTRA}/mappers/{cur['id']}", json=overlay(cur, m))
            log(f"realm: identity provider {ENTRA}: mapper {m['name'][len(MINE):]} put back")
    for name, cur in existing.items():
        if name.startswith(MINE) and name not in {m["name"] for m in wanted}:
            kc("DELETE", f"/identity-provider/instances/{ENTRA}/mappers/{cur['id']}")
            log(f"realm: identity provider {ENTRA}: mapper {name[len(MINE):]} removed")


def main():
    want = load()
    kc = Admin()
    have = kc("GET", "")
    if have.status_code == 404:
        sys.exit(f"realm: Keycloak has no realm {REALM}: it imports {os.path.basename(REALM_FILE)} as it starts")
    settings = {k: want[k] for k in SETTINGS if k in want}
    if {k: have.json().get(k) for k in settings} != settings:
        kc("PUT", "", json=settings)
        log(f"realm: settings {', '.join(sorted(settings))}")
    for c in want.get("clients", []):
        apply_client(kc, c)
    for u in want.get("users", []):
        if u.get("serviceAccountClientId"):
            apply_service_account(kc, u)
    apply_entra(kc)
    log("realm: the platform's parts are as realm.json says")


if __name__ == "__main__":
    main()
