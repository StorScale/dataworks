"""The notebook server's half of JupyterLab's Schedule (use case 7.7).

GET  /storscale/projects   the projects the person may schedule in (platformd's answer)
POST /storscale/schedule   {path, project, schedule}: the notebook, its outputs
                           cleared, to platformd, which commits it to the
                           project's repo with a pipeline, as a merge request

Both call platformd as the person, with their Keycloak token from the hub
(buckets_lake.token()): platformd checks they're an editor of the project.
"""
import json
import os

import buckets_lake
import requests
import tornado.web
from jupyter_server.base.handlers import APIHandler
from jupyter_server.utils import ensure_async, url_path_join

PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080")


def _platform(method, path, body=None):
    try:
        r = requests.request(method, f"{PLATFORM}{path}", json=body, timeout=60,
                             headers={"Authorization": f"Bearer {buckets_lake.token()}"})
    except (requests.RequestException, RuntimeError) as e:
        return 502, {"error": f"the platform isn't answering: {e}"}
    try:
        return r.status_code, r.json()
    except ValueError:
        return r.status_code, {"error": r.text[:300]}


def cleared(nb):
    """The notebook without its outputs: what it computed may be data no repo should hold."""
    for cell in nb.get("cells", []):
        if cell.get("cell_type") == "code":
            cell["outputs"], cell["execution_count"] = [], None
    return nb


class Projects(APIHandler):
    @tornado.web.authenticated
    def get(self):
        code, out = _platform("GET", "/api/notebooks/projects")
        self.set_status(code)
        self.finish(json.dumps(out))


class Schedule(APIHandler):
    @tornado.web.authenticated
    async def post(self):
        body = self.get_json_body() or {}
        path, project = body.get("path", ""), body.get("project", "")
        if not path.endswith(".ipynb") or not project:
            self.set_status(400)
            return self.finish(json.dumps({"error": "a notebook and a project"}))
        model = await ensure_async(self.contents_manager.get(path, content=True, type="notebook"))
        nb = cleared(json.loads(json.dumps(model["content"])))
        code, out = _platform("POST", f"/api/projects/{requests.utils.quote(project, safe='')}/notebooks/schedules",
                              {"path": path, "schedule": body.get("schedule", ""), "notebook": nb})
        self.log.info("storscale: %s scheduled in %s: HTTP %s", path, project, code)
        self.set_status(code)
        self.finish(json.dumps(out))


def _jupyter_server_extension_points():
    return [{"module": "storscale_schedule"}]


def _load_jupyter_server_extension(server):
    base = server.web_app.settings["base_url"]
    server.web_app.add_handlers(".*$", [
        (url_path_join(base, "storscale/projects"), Projects),
        (url_path_join(base, "storscale/schedule"), Schedule),
    ])
