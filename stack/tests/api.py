"""The API and the Python package: docker compose run --rm test api

These check (use case 14.3):

  1. the API reference (/api/openapi.json) lists platformd's routes, each with
     what it does, to anyone;
  2. with the storscale package, bob signs in by name and password and, as
     himself: makes a project, reads it among his projects, queries it, calls
     a route by its operation ID (its usage), and removes it;
  3. carol, in none of the platform's groups, is refused, saying so;
  4. alice signs in with the package by a device code (as from a notebook
     or a pod, and as anyone signed in through Entra ID must): the package
     shows a link and a code, she approves it in her browser, and the
     package is her;
  5. a service account, an application's identity in the project (a CRM's):
     alice, an editor, registers it as an editor; she can't approve it, bob
     (an administrator) does and gets its secret once; it signs in with its
     secret (client credentials) and, as the project's editor, queries it
     and keeps a connection there; revoked, it signs in no more.

The project is removed after.
"""
import html
import json
import os
import re
import sys
from urllib.parse import urljoin

import requests

sys.path.insert(0, "/python")
import storscale  # noqa: E402
from lakekit import check, env, finish  # noqa: E402
from projects import platform, projects, wait  # noqa: E402

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "scripted"
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Made from Python: the api suite's.", "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "editor"}], "tables": {}}}


def _signs_in(given):
    """The service account signed in with its secret, or None while Keycloak refuses it."""
    try:
        return storscale.Platform(PLATFORM, client_id=given["clientId"], client_secret=given["secret"])
    except storscale.Error:
        return None


def main():
    # 1. The reference.
    r = requests.get(f"{PLATFORM}/api/openapi.json", timeout=30)
    spec = r.json() if r.ok else {}
    ops = [(m, p, o) for p, v in (spec.get("paths") or {}).items() for m, o in v.items()]
    check("the API reference lists platformd's routes, each with what it does, to anyone",
          r.ok and spec.get("openapi", "").startswith("3.") and len(ops) > 90 and all(o.get("summary") for _, _, o in ops)
          and "/api/projects/{name}/usage" in spec["paths"], (r.status_code, len(ops)))

    password = env["LAKEHOUSE_USER_PASSWORD"]
    try:
        # 2. The package, as bob.
        p = storscale.Platform(PLATFORM, username="bob", password=password)
        me = p.me()
        check("bob signs in with the package, by name and password", (me.get("user") or {}).get("username") == "bob", me.get("user"))
        p.request("PUT", f"/api/projects/{NAME}", json=SPEC)
        ready = wait(lambda: next((x for x in p.projects() if x["name"] == NAME and (x.get("status") or {}).get("phase") == "Ready"), None), 900, every=10)
        check("... makes a project, and reads it among his projects, as himself", ready is not None and ready.get("role") == "editor", ready)
        answer = p.sql(NAME, "SELECT 1 + 1 AS two")
        check("... queries it, under its rules", answer.get("rows") == [[2]], answer)
        usage = p.call("get_projects_name_usage", name=NAME)
        check("... and calls a route by its operation ID (the project's usage)", (usage.get("usage") or {}).get("project") == NAME, usage)

        # 3. carol, refused.
        try:
            storscale.Platform(PLATFORM, username="carol", password=password).projects()
            refused = None
        except storscale.Refused as e:
            refused = e
        check("carol, in none of the platform's groups, is refused, saying so", refused is not None and refused.status == 403, str(refused))

        # 4. alice, by a device code: what the package shows, approved in "her browser".
        shown = {}

        def approve(message):
            shown["message"] = message
            link = re.search(r"open (\S+) and approve the code (\S+)", message)
            if not link:
                return
            b = requests.Session()
            r = b.get(link.group(1), timeout=30)   # the code filled in: Keycloak's sign-in, then the device's
            form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', r.text)
            if form:
                r = b.post(urljoin(r.url, html.unescape(form.group(1))), timeout=60,
                           data={"username": "alice", "password": password, "credentialId": ""})
            consent = re.search(r'<form[^>]*action="([^"]+)"[^>]*>(?:(?!</form>).)*name="accept"', r.text, re.S)
            if consent:   # Keycloak asks a device sign-in's person to grant the client access (its action is relative)
                r = b.post(urljoin(r.url, html.unescape(consent.group(1))), timeout=60, data={"accept": "Yes"})
            shown["approved"] = r.status_code

        try:
            me = storscale.Platform(PLATFORM, show=approve).me()
        except storscale.Error as e:
            me = {"error": str(e)}
        check("alice signs in with the package by a device code: a link and a code, approved in her browser",
              (me.get("user") or {}).get("username") == "alice" and "approve the code" in shown.get("message", ""),
              {"shown": shown.get("message"), "me": me.get("user") or me})

        # 5. A service account: registered by alice, approved by bob, used with its secret, revoked.
        alice = storscale.Platform(PLATFORM, username="alice", password=password)
        alice.request("PUT", f"/api/projects/{NAME}/serviceaccounts/provisioner", json={"role": "editor", "description": "the api suite's"})
        try:
            alice.request("POST", f"/api/projects/{NAME}/serviceaccounts/provisioner/approve")
            refused = None
        except storscale.Refused as e:
            refused = e
        check("alice (an editor) registers a service account, and can't approve it: an administrator does",
              refused is not None and refused.status == 403 and "administrator" in refused.message, str(refused))
        given = p.request("POST", f"/api/projects/{NAME}/serviceaccounts/provisioner/approve")
        check("bob (an administrator) approves it, and gets its client and secret once",
              given.get("clientId") == f"svc-{NAME}-provisioner" and len(given.get("secret") or "") == 48, {k: v for k, v in given.items() if k != "secret"})
        svc = wait(lambda: _signs_in(given), 180, every=10)
        me = svc.me().get("user") if svc else {}
        check("... it signs in with its secret (client credentials), as itself",
              (me or {}).get("username") == f"service-account-svc-{NAME}-provisioner", me)
        mine = next((x for x in (svc.projects() if svc else []) if x["name"] == NAME), {})
        answer = svc.sql(NAME, "SELECT 3 AS three") if svc else {}
        svc.request("PUT", f"/api/projects/{NAME}/connections/{NAME}-from-svc", json={"kind": "api", "values": {"token": "t"}}) if svc else None
        kept = [c["name"] for c in alice.request("GET", f"/api/projects/{NAME}/connections").get("connections", [])]
        check("... a member in its role: it reads the project as an editor, queries it, and keeps a connection there",
              mine.get("role") == "editor" and answer.get("rows") == [[3]] and f"{NAME}-from-svc" in kept, {"role": mine.get("role"), "rows": answer.get("rows"), "kept": kept})
        alice.request("DELETE", f"/api/projects/{NAME}/serviceaccounts/provisioner")
        gone = wait(lambda: _signs_in(given) is None, 180, every=10)
        check("revoked, it signs in no more", gone, "")
    finally:
        bob = platform("bob")
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    print(json.dumps({"operations": len(ops)}))
    finish()


if __name__ == "__main__":
    main()
