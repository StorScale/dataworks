"""The little of Kubernetes' API the platform's setup steps use, when they run
in a cluster (STORSCALE_K8S): a Secret one step writes and others read, and
the marks that say a setup step of this release has finished.

    python k8s.py done <step>     mark <step> finished, for this revision

Uses the pod's service account; nothing outside the release's namespace.
"""
import base64
import os
import sys
import time

import requests

SA = "/var/run/secrets/kubernetes.io/serviceaccount"
DONE = "storscale-setup-done"   # a ConfigMap: setup step -> the revision it last finished for


def in_cluster():
    return os.environ.get("STORSCALE_K8S") == "true" and os.path.exists(f"{SA}/token")


def _api(method, path, api="api/v1", **kw):
    host = os.environ["KUBERNETES_SERVICE_HOST"]
    port = os.environ.get("KUBERNETES_SERVICE_PORT", "443")
    with open(f"{SA}/token") as f:
        token = f.read().strip()
    with open(f"{SA}/namespace") as f:
        ns = f.read().strip()
    return requests.request(method, f"https://{host}:{port}/{api}/namespaces/{ns}/{path}",
                            headers={"Authorization": f"Bearer {token}"}, verify=f"{SA}/ca.crt", timeout=30, **kw)


PROJECTS = "apis/platform.storscale.io/v1alpha1"
FINALIZER = "platform.storscale.io/undo"   # the operator undoes a deleted project, then lets it go


def put_project(p):
    """Create or change a Project resource, as platformd does (with the
    operator's finalizer): p is a project as projects/*.json has it."""
    name = p["metadata"]["name"]
    for _ in range(3):
        r = _api("GET", f"projects/{name}", api=PROJECTS)
        body = {"apiVersion": p["apiVersion"], "kind": "Project", "spec": p["spec"],
                "metadata": {"name": name, "finalizers": [FINALIZER]}}
        if r.status_code == 404:
            r = _api("POST", "projects", api=PROJECTS, json=body)
        else:
            r.raise_for_status()
            md = r.json()["metadata"]
            body["metadata"].update(resourceVersion=md["resourceVersion"],
                                    finalizers=sorted(set(md.get("finalizers", [])) | {FINALIZER}))
            r = _api("PUT", f"projects/{name}", api=PROJECTS, json=body)
        if r.status_code != 409:   # 409: changed since it was read; read it again
            r.raise_for_status()
            return
    r.raise_for_status()


def _put(kind, name, body, labels=None):
    """Create or replace a Secret or ConfigMap."""
    body = {"apiVersion": "v1", "kind": {"secrets": "Secret", "configmaps": "ConfigMap"}[kind],
            "metadata": {"name": name, "labels": {"app.kubernetes.io/part-of": "storscale-platform", **(labels or {})}}, **body}
    r = _api("PUT", f"{kind}/{name}", json=body)
    if r.status_code == 404:
        r = _api("POST", kind, json=body)
    r.raise_for_status()


def put_secret(name, data, labels=None):
    """A Secret of the given files (key -> text)."""
    _put("secrets", name, {"type": "Opaque",
                           "data": {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}}, labels)


def get_secret(name):
    r = _api("GET", f"secrets/{name}")
    if r.status_code == 404:
        return {}
    r.raise_for_status()
    return {k: base64.b64decode(v).decode() for k, v in (r.json().get("data") or {}).items()}


def done_revision(step):
    r = _api("GET", f"configmaps/{DONE}")
    if r.status_code == 404:
        return 0
    r.raise_for_status()
    return int((r.json().get("data") or {}).get(step, "0"))


def mark_done(step, revision):
    r = _api("GET", f"configmaps/{DONE}")
    data = (r.json().get("data") or {}) if r.ok else {}
    data[step] = str(revision)
    _put("configmaps", DONE, {"data": data})


def delete_pods(selector):
    """Delete the pods with these labels (their controller makes new ones)."""
    r = _api("DELETE", "pods", params={"labelSelector": selector})
    r.raise_for_status()
    return [i["metadata"]["name"] for i in r.json().get("items", [])]


def run_with_volume(beside, claim, command, user=65532):
    """Run a command in a pod of its own with the volume claim mounted at
    /volume, on the node of the pod `beside` (a ReadWriteOnce volume is
    mounted there already), and wait for it to finish."""
    pod = _api("GET", f"pods/{beside}").json()
    name = f"storscale-volume-{int(time.time())}"
    image = os.environ.get("STORSCALE_TOOLS_IMAGE") or _api("GET", f"pods/{os.environ['HOSTNAME']}").json()["spec"]["containers"][0]["image"]
    r = _api("POST", "pods", json={"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name}, "spec": {
        "nodeName": pod["spec"]["nodeName"], "restartPolicy": "Never", "enableServiceLinks": False,
        "securityContext": {"runAsUser": user, "runAsGroup": user},
        "containers": [{"name": "run", "image": image, "imagePullPolicy": "IfNotPresent", "command": command,
                        "volumeMounts": [{"name": "v", "mountPath": "/volume"}]}],
        "volumes": [{"name": "v", "persistentVolumeClaim": {"claimName": claim}}]}})
    r.raise_for_status()
    try:
        for _ in range(120):
            phase = _api("GET", f"pods/{name}").json().get("status", {}).get("phase")
            if phase in ("Succeeded", "Failed"):
                if phase == "Failed":
                    raise RuntimeError(f"{' '.join(command)} on {claim}: failed")
                return
            time.sleep(1)
        raise TimeoutError(f"{' '.join(command)} on {claim}: still running after 2 minutes")
    finally:
        _api("DELETE", f"pods/{name}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["done"] and len(sys.argv) == 3:
        mark_done(sys.argv[2], int(os.environ["STORSCALE_REVISION"]))
        print(f"{sys.argv[2]}: done (revision {os.environ['STORSCALE_REVISION']})")
    else:
        sys.exit(__doc__)
