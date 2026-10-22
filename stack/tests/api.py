"""The API and the Python package: docker compose run --rm test api

These check (use case 14.3):

  1. the API reference (/api/openapi.json) lists platformd's routes, each with
     what it does, to anyone;
  2. with the storscale package, bob signs in by name and password and, as
     himself: makes a project, reads it among his projects, queries it, calls
     a route by its operation ID (its usage), and removes it;
  3. carol, in none of the platform's groups, is refused, saying so.

The project is removed after.
"""
import json
import os
import sys

import requests

sys.path.insert(0, "/python")
import storscale  # noqa: E402
from lakekit import check, env, finish  # noqa: E402
from projects import platform, projects, wait  # noqa: E402

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "scripted"
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Made from Python: the api suite's.", "members": [{"group": "engineers", "role": "editor"}], "tables": {}}}


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
    finally:
        bob = platform("bob")
        bob.delete(f"{PLATFORM}/api/projects/{NAME}", headers={"X-Platform-Request": "1"}, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    print(json.dumps({"operations": len(ops)}))
    finish()


if __name__ == "__main__":
    main()
