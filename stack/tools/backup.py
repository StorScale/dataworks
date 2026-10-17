"""The platform's backups, on Kubernetes: the job's steps after its databases
are dumped (the backup CronJob, and the restore Job storscale restore makes).

    python backup.py run            the dumps in /backup, with the Projects and
                                    Secrets, packed, encrypted for the backup
                                    certificate, uploaded; the oldest pruned
    python backup.py fetch NAME     a backup (or "latest") fetched, decrypted
                                    with the restore key, unpacked into /backup
    python backup.py apply          its Projects and Secrets put back

A backup is backups/<time>.tar.gz.cms in the backup bucket: a tar.gz in a CMS
envelope (AES-256-GCM, its key for the certificate's RSA key), which openssl
alone opens: openssl cms -decrypt -binary -inform DER -inkey KEY -recip CERT.
Beside it, backups/<time>.json says what's in it (not encrypted: no secrets).
"""
import json
import os
import subprocess
import sys
import tarfile
import time

import k8s

WORK = "/backup"                  # the dumps, and what's unpacked
CERT = "/backup-cert/cert.pem"    # whom backups are encrypted for (the chart's backup.certificate)
KEY = "/restore-key/key.pem"      # the private key, only while a restore runs (storscale restore)
PREFIX = "backups/"


def log(msg):
    print(f"backup: {msg}", flush=True)


def store():
    import boto3
    from botocore.config import Config
    return boto3.client("s3", endpoint_url=os.environ.get("BACKUP_ENDPOINT") or "http://buckets:9000",
                        region_name=os.environ.get("BACKUP_REGION") or "us-east-1",
                        aws_access_key_id=os.environ["BACKUP_ACCESS_KEY"], aws_secret_access_key=os.environ["BACKUP_SECRET_KEY"],
                        config=Config(s3={"addressing_style": "path"}))


BUCKET = os.environ.get("BACKUP_BUCKET", "storscale-backups")


def clean(obj):
    """A resource as it can be created again: no server-set fields, no status."""
    md = obj["metadata"]
    keep = {k: md[k] for k in ("name", "labels", "annotations", "finalizers") if md.get(k)}
    out = {k: v for k, v in obj.items() if k not in ("metadata", "status")}
    out["metadata"] = keep
    return out


def export():
    """The Projects, and the Secrets (storscale-env's passwords and keys, which
    the databases' encrypted settings need; the projects' connections; their
    Airflows' keys), as JSON in WORK."""
    r = k8s._api("GET", "projects", api=k8s.PROJECTS)
    r.raise_for_status()
    projects = [clean(p) for p in r.json().get("items", [])]
    r = k8s._api("GET", "secrets")
    r.raise_for_status()
    secrets = [clean(s) for s in r.json().get("items", [])
               if s.get("type") == "Opaque" and not s["metadata"]["name"].startswith("sh.helm.")]
    for name, items in (("projects", projects), ("secrets", secrets)):
        with open(f"{WORK}/{name}.json", "w") as f:
            json.dump(items, f)
    return projects, secrets


def run():
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    projects, secrets = export()
    dumps = sorted(os.path.relpath(os.path.join(d, f), WORK) for d, _, fs in os.walk(WORK) for f in fs if f.endswith(".dump"))
    if not dumps:
        sys.exit("backup: no database dumps in /backup")
    packed, sealed = f"/tmp/{stamp}.tar.gz", f"/tmp/{stamp}.tar.gz.cms"
    with tarfile.open(packed, "w:gz") as t:
        t.add(WORK, arcname="backup")
    subprocess.run(["openssl", "cms", "-encrypt", "-binary", "-stream", "-aes-256-gcm", "-outform", "DER",
                    "-in", packed, "-out", sealed, CERT], check=True)
    os.remove(packed)
    s3 = store()
    s3.upload_file(sealed, BUCKET, f"{PREFIX}{stamp}.tar.gz.cms")
    manifest = {"time": stamp, "version": os.environ.get("STORSCALE_VERSION", ""), "bytes": os.path.getsize(sealed),
                "databases": dumps, "projects": [p["metadata"]["name"] for p in projects],
                "secrets": [s["metadata"]["name"] for s in secrets]}
    s3.put_object(Bucket=BUCKET, Key=f"{PREFIX}{stamp}.json", Body=json.dumps(manifest, indent=1).encode())
    log(f"{stamp}: {len(dumps)} databases, {len(projects)} projects, {len(secrets)} secrets, {manifest['bytes']} bytes")
    prune(s3, int(os.environ.get("BACKUP_KEEP") or 14))


def backups(s3):
    """The backups' names, oldest first."""
    names = set()
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket=BUCKET, Prefix=PREFIX):
        for o in page.get("Contents", []):
            if o["Key"].endswith(".tar.gz.cms"):
                names.add(o["Key"][len(PREFIX):-len(".tar.gz.cms")])
    return sorted(names)


def prune(s3, keep):
    old = backups(s3)[:-keep] if keep > 0 else []
    for name in old:
        for suffix in (".tar.gz.cms", ".json"):
            s3.delete_object(Bucket=BUCKET, Key=f"{PREFIX}{name}{suffix}")
    if old:
        log(f"pruned {len(old)}, the oldest beyond {keep}")


def fetch(name):
    s3 = store()
    have = backups(s3)
    if not have:
        sys.exit("backup: no backups in the bucket")
    if name == "latest":
        name = have[-1]
    if name not in have:
        sys.exit(f"backup: no backup {name}; there are: {', '.join(have[-10:])}")
    sealed, packed = f"/tmp/{name}.tar.gz.cms", f"/tmp/{name}.tar.gz"
    s3.download_file(BUCKET, f"{PREFIX}{name}.tar.gz.cms", sealed)
    r = subprocess.run(["openssl", "cms", "-decrypt", "-binary", "-inform", "DER", "-in", sealed, "-out", packed,
                        "-inkey", KEY, "-recip", CERT], capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(f"backup: {name} can't be decrypted with this key (or it's damaged): {r.stderr.strip()[:300]}")
    with tarfile.open(packed) as t:
        t.extractall("/", filter="data")  # into /backup (the archive's root)
    with open(f"{WORK}/name", "w") as f:
        f.write(name)
    log(f"fetched {name}")


def apply():
    """The backup's Projects put back as they were (a project made since is
    left as it is); its Secrets put back where they're missing or differ,
    but for storscale-env, which a restore into the same install keeps."""
    with open(f"{WORK}/projects.json") as f:
        projects = json.load(f)
    with open(f"{WORK}/secrets.json") as f:
        secrets = json.load(f)
    for s in secrets:
        name = s["metadata"]["name"]
        if name == "storscale-env":
            continue
        r = k8s._api("GET", f"secrets/{name}")
        if r.status_code == 200 and r.json().get("data") == s.get("data"):
            continue
        body = {"apiVersion": "v1", "kind": "Secret", "type": s.get("type", "Opaque"), "metadata": s["metadata"], "data": s.get("data", {})}
        r = k8s._api("PUT", f"secrets/{name}", json=body) if r.status_code == 200 else k8s._api("POST", "secrets", json=body)
        r.raise_for_status()
        log(f"secret {name} put back")
    for p in projects:
        k8s.put_project({"apiVersion": p["apiVersion"], "metadata": {"name": p["metadata"]["name"]}, "spec": p["spec"]})
        log(f"project {p['metadata']['name']} put back")
    r = k8s._api("GET", "projects", api=k8s.PROJECTS)
    r.raise_for_status()
    newer = sorted({x["metadata"]["name"] for x in r.json().get("items", [])} - {p["metadata"]["name"] for p in projects})
    if newer:
        log(f"projects made since the backup, left as they are: {', '.join(newer)}")


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    if cmd == "run":
        run()
    elif cmd == "fetch" and len(sys.argv) == 3:
        fetch(sys.argv[2])
    elif cmd == "apply":
        apply()
    else:
        sys.exit(__doc__)
