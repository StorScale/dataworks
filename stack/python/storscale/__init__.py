"""StorScale Platform's API, as Python (use case 14.3, R14.8).

    import storscale
    p = storscale.Platform("https://storscale.example.com")
    p.projects()
    p.call("get_projects_name_usage", name="sales")

Every call runs as whoever signed in (a person, or a service account), under
their projects' rules. The routes come from the platform's own reference
(/api/openapi.json), so call() reaches any of them by its operation ID; the
methods below are the common ones, by name.
"""
import getpass
import os
import re
import sys
import time
from urllib.parse import quote

import requests

__all__ = ["Platform", "Error", "Refused"]
__version__ = "0.33.0"

REALM = "lakehouse"
PUBLIC_CLIENT = "storscale-agent"   # the realm's public client, for a person's name and password


class Error(RuntimeError):
    """The platform said no, or couldn't: its reason, the HTTP status and the request's ID."""

    def __init__(self, status, message, request_id=None):
        super().__init__(f"{message} (HTTP {status}" + (f", request {request_id})" if request_id else ")"))
        self.status, self.message, self.request_id = status, message, request_id


class Refused(Error):
    """Not allowed: the caller may not do this (HTTP 401 or 403)."""


class Platform:
    """The platform at url (or STORSCALE_URL), signed in as the arguments, the environment or a prompt say."""

    def __init__(self, url=None, *, token=None, client_id=None, client_secret=None, username=None, password=None, timeout=300):
        self.url = (url or os.environ.get("STORSCALE_URL") or "").rstrip("/")
        if not self.url:
            raise ValueError("the platform's address: Platform('https://...') or STORSCALE_URL")
        self.timeout = timeout
        self.http = requests.Session()
        self.http.headers["User-Agent"] = f"storscale-python/{__version__}"
        self._token, self._expires, self._refresh = token or os.environ.get("STORSCALE_TOKEN"), float("inf"), None
        self._client = (client_id or os.environ.get("STORSCALE_CLIENT_ID"), client_secret or os.environ.get("STORSCALE_CLIENT_SECRET"))
        self._person = (username or os.environ.get("STORSCALE_USERNAME"), password or os.environ.get("STORSCALE_PASSWORD"))
        self._ops = None
        if not self._token:
            if not all(self._client) and not all(self._person):
                if not sys.stdin.isatty():
                    raise ValueError("sign in: a token, a client and its secret, or a name and password (see the README)")
                self._person = (input("StorScale user: "), getpass.getpass("Password: "))
            self._sign_in()

    # --- signing in -----------------------------------------------------------------------

    def _token_url(self):
        return f"{self.url}/sso/realms/{REALM}/protocol/openid-connect/token"

    def _sign_in(self):
        if self._refresh:
            data = {"grant_type": "refresh_token", "client_id": PUBLIC_CLIENT, "refresh_token": self._refresh}
        elif all(self._client):
            data = {"grant_type": "client_credentials", "client_id": self._client[0], "client_secret": self._client[1]}
        else:
            data = {"grant_type": "password", "client_id": PUBLIC_CLIENT, "username": self._person[0], "password": self._person[1]}
        r = self.http.post(self._token_url(), data=data, timeout=30)
        if r.status_code in (400, 401) and self._refresh:   # the refresh token has expired: sign in again
            self._refresh = None
            return self._sign_in()
        if not r.ok:
            raise Refused(r.status_code, "Keycloak refused the sign-in: " + (r.json().get("error_description") if r.headers.get("content-type", "").startswith("application/json") else r.text[:200]))
        t = r.json()
        self._token, self._expires, self._refresh = t["access_token"], time.time() + t.get("expires_in", 300) - 30, t.get("refresh_token")

    def _bearer(self):
        if time.time() >= self._expires and (self._refresh or all(self._client) or all(self._person)):
            self._sign_in()
        return self._token

    # --- calls ----------------------------------------------------------------------------

    def request(self, method, path, json=None, params=None, raw=False):
        """A call of the platform's API, as the signed-in caller: its JSON answer (or the response, raw)."""
        r = self.http.request(method, self.url + path, json=json, params=params, timeout=self.timeout,
                              headers={"Authorization": f"Bearer {self._bearer()}", "X-Platform-Request": "1"})
        if raw:
            return r
        if not r.ok:
            body = r.json() if r.headers.get("content-type", "").startswith("application/json") else {"error": r.text[:300]}
            cls = Refused if r.status_code in (401, 403) else Error
            raise cls(r.status_code, body.get("error") or r.reason, body.get("request"))
        if r.status_code == 204 or not r.content:
            return None
        return r.json() if r.headers.get("content-type", "").startswith("application/json") else r.text

    def operations(self):
        """The platform's routes by operation ID: (method, path, summary), from its reference."""
        if self._ops is None:
            spec = self.http.get(self.url + "/api/openapi.json", timeout=60).json()
            self._ops = {op["operationId"]: (m.upper(), path, op.get("summary", ""))
                         for path, ops in spec["paths"].items() for m, op in ops.items()}
        return self._ops

    def call(self, operation_id, body=None, **params):
        """Any route by its operation ID (see operations()): its path's parameters by name, the rest as the query; body as JSON."""
        try:
            method, path, _ = self.operations()[operation_id]
        except KeyError:
            raise ValueError(f"no operation {operation_id}: see Platform.operations()") from None
        for name in re.findall(r"\{([^}]+)\}", path):
            if name not in params:
                raise TypeError(f"{operation_id} needs {name}")
            path = path.replace("{" + name + "}", quote(str(params.pop(name)), safe=""))
        return self.request(method, path, json=body, params=params or None)

    # --- the common ones ------------------------------------------------------------------

    def me(self):
        """Who's signed in, and what the platform has."""
        return self.request("GET", "/api/session")

    def projects(self):
        """The projects the caller may see, each with its spec, status and the caller's role."""
        return (self.request("GET", "/api/projects") or {}).get("projects", [])

    def project(self, name):
        return self.request("GET", f"/api/projects/{quote(name, safe='')}")

    def sql(self, project, statement, limit=None):
        """A query, as the caller, under the project's rules: {"columns": [...], "rows": [...]}."""
        return self.request("POST", f"/api/projects/{quote(project, safe='')}/sql", json={"sql": statement, "limit": limit})

    def pipelines(self, project):
        """The project's pipelines and their latest runs."""
        return (self.request("GET", f"/api/projects/{quote(project, safe='')}/pipelines") or {}).get("pipelines", [])

    def start_run(self, project, pipeline):
        """Starts a run of one of the project's pipelines (its editors): the run's ID."""
        return self.request("POST", f"/api/projects/{quote(project, safe='')}/pipelines/{quote(pipeline, safe='')}/runs", json={})["run"]

    def run_log(self, project, pipeline, run):
        """What a run's tasks logged."""
        return self.request("GET", f"/api/projects/{quote(project, safe='')}/pipelines/{quote(pipeline, safe='')}/runs/{quote(run, safe='')}/log")["log"]

    def wait_for_run(self, project, pipeline, run, every=10, timeout=3600):
        """Waits for a run to end: its state (success or failed)."""
        end = time.time() + timeout
        while time.time() < end:
            for p in self.pipelines(project):
                if p["id"] == pipeline:
                    state = next((r["state"] for r in p.get("runs", []) if r["id"] == run), None)
                    if state in ("success", "failed"):
                        return state
            time.sleep(every)
        raise TimeoutError(f"the run {run} of {pipeline} didn't end in {timeout}s")

    def flows(self, project):
        return self.request("GET", f"/api/projects/{quote(project, safe='')}/flows")

    def flow(self, project, name):
        return self.request("GET", f"/api/projects/{quote(project, safe='')}/flows/{quote(name, safe='')}")

    def knowledge(self, project):
        """The project's knowledge banks, and whether each is built."""
        return (self.request("GET", f"/api/projects/{quote(project, safe='')}/knowledge") or {}).get("banks", [])

    def search(self, project, bank, query, k=5):
        """A knowledge bank searched by meaning: the closest passages, each with where it's from."""
        return self.request("POST", f"/api/projects/{quote(project, safe='')}/knowledge/{quote(bank, safe='')}/search",
                            json={"query": query, "k": k})["hits"]

    def usage(self, project, month=None):
        """A project's month (2026-10; this one by default): what it used and cost, against its budget."""
        return self.request("GET", f"/api/projects/{quote(project, safe='')}/usage", params={"month": month} if month else None)
