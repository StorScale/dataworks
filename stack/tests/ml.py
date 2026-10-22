"""Models: docker compose run --rm test ml

A project of the suite's own, modeled, with bob and alice its editors. These
check (use case 15.1), through the platform's MLflow (/mlflow/<project>/, a
Keycloak token as MLflow's client sends it):

  1. bob tracks a run in an experiment of the project's: the platform stamps
     it with who ran it and the data's Nessie commit; his metric is kept, and
     an artifact goes to Buckets and comes back;
  2. the project's workspace is its own: a request naming another workspace
     still lands in the project's, and carol (no member) is refused;
  3. bob registers a version of a model; he can't approve it himself, nor set
     the platform's approval tags or alias through MLflow; alice (another
     editor) approves it on the Models tab, which shows it approved.

The project is removed after.
"""
import json
import os
import time

import requests

from lakekit import check, env, finish
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "modeled"
H = {"X-Platform-Request": "1"}
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Models: the ml suite's.", "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}]}}


def token(user):
    r = requests.post(f"{env['KEYCLOAK_URL']}/realms/lakehouse/protocol/openid-connect/token", timeout=10,
                      data={"grant_type": "password", "client_id": "storscale-agent", "username": user, "password": env["LAKEHOUSE_USER_PASSWORD"]})
    r.raise_for_status()
    return r.json()["access_token"]


class MLflow:
    """MLflow's REST API, as its client calls it: the tracking URI and a bearer token."""

    def __init__(self, user, project=NAME, workspace=None):
        self.base = f"{PLATFORM}/mlflow/{project}"
        self.h = {"Authorization": f"Bearer {token(user)}"}
        if workspace:
            self.h["X-MLFLOW-WORKSPACE"] = workspace   # a guess the platform replaces

    def call(self, method, path, **kw):
        r = requests.request(method, self.base + path, headers=self.h, timeout=60, **kw)
        return r, (r.json() if r.headers.get("content-type", "").startswith("application/json") and r.content else {})

    def api(self, method, path, body=None, params=None):
        return self.call(method, "/api/2.0/mlflow/" + path, json=body, params=params)


def main():
    bob = platform("bob")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        check("bob makes a project with alice a second editor", r.ok and ready is not None, r.text[:200])

        # 1. A run, stamped by the platform.
        ml = MLflow("bob")
        _, got = ml.api("GET", "experiments/get-by-name", params={"experiment_name": "churn"})
        exp = (got.get("experiment") or {}).get("experiment_id")
        if not exp:
            made, got = ml.api("POST", "experiments/create", {"name": "churn"})
            exp = got.get("experiment_id")
        _, got = ml.api("POST", "runs/create", {"experiment_id": exp, "start_time": int(time.time() * 1000),
                                                "tags": [{"key": "storscale.user", "value": "mallory"}, {"key": "team", "value": "retention"}]})
        run = (got.get("run") or {}).get("info", {}).get("run_id")
        ml.api("POST", "runs/log-metric", {"run_id": run, "key": "auc", "value": 0.87, "timestamp": int(time.time() * 1000), "step": 0})
        _, got = ml.api("GET", "runs/get", params={"run_id": run})
        tags = {t["key"]: t["value"] for t in (got.get("run") or {}).get("data", {}).get("tags", [])}
        metrics = {m["key"]: m["value"] for m in (got.get("run") or {}).get("data", {}).get("metrics", [])}
        check("bob tracks a run: the platform stamps it with who ran it and the data's commit (not what he claimed), his metric kept",
              run and tags.get("storscale.user") == "bob" and len(tags.get("storscale.data_commit", "")) > 8 and tags.get("team") == "retention"
              and metrics.get("auc") == 0.87, (run, tags, metrics))
        path = f"/api/2.0/mlflow-artifacts/artifacts/{exp}/{run}/artifacts/model/MLmodel"
        put, _ = ml.call("PUT", path, data=b"flavors: {}\n")
        back, _ = ml.call("GET", path)
        check("... and an artifact goes to Buckets through the platform, and comes back", put.ok and back.ok and back.content == b"flavors: {}\n",
              (put.status_code, put.text[:200], back.status_code))

        # 2. The project's workspace, whatever's asked.
        _, seen = MLflow("bob", workspace="default").api("POST", "experiments/search", {"max_results": 100})
        names = {e["name"] for e in seen.get("experiments", [])}
        refused, _ = MLflow("carol").api("POST", "experiments/search", {"max_results": 10})
        check("the project's workspace is its own: asking for another still lands in the project's; carol is refused",
              "churn" in names and refused.status_code == 403, (sorted(names), refused.status_code))

        # 3. Registered, and approved by someone else.
        ml.api("POST", "registered-models/create", {"name": "churn"})
        _, got = ml.api("POST", "model-versions/create", {"name": "churn", "source": f"runs:/{run}/model", "run_id": run})
        version = (got.get("model_version") or {}).get("version")
        own = bob.post(f"{url}/models/churn/versions/{version}/approve", headers=H, timeout=60)
        forged_tag, _ = ml.api("POST", "model-versions/set-tag", {"name": "churn", "version": version, "key": "storscale.approved_by", "value": "bob"})
        forged_alias, _ = ml.api("POST", "registered-models/alias", {"name": "churn", "alias": "approved", "version": version})
        check("bob registers a version; he can't approve it himself, nor set the approval's tags or alias through MLflow",
              version and own.status_code == 403 and forged_tag.status_code == 403 and forged_alias.status_code == 403,
              (version, own.status_code, forged_tag.status_code, forged_alias.status_code))
        alice = platform("alice")
        ok = alice.post(f"{url}/models/churn/versions/{version}/approve", headers=H, timeout=60)
        models = (bob.get(f"{url}/models", timeout=60).json() or {}).get("models", [])
        churn = next((m for m in models if m["name"] == "churn"), {})
        v = next((x for x in churn.get("versions", []) if x["version"] == version), {})
        check("alice, another editor, approves it; the Models tab shows it approved, by her, registered by bob",
              ok.ok and churn.get("approved") == version and v.get("approvedBy") == "alice" and v.get("registeredBy") == "bob", (ok.status_code, ok.text[:200], churn))
    finally:
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
