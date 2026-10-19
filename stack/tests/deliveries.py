"""File deliveries to a partner, end to end: docker compose run --rm test deliveries

The suite plays the partner: an SFTP server (its host key known), a bucket
(Buckets' own, as "another company's"), and a PGP key pair. It makes a
project, deliver, with bob and alice its editors and a table, regions, and
two deliveries of it (use case 9.1):

  1. a delivery doesn't run until approved, and bob, who made it, can't
     approve it; alice (another editor) does;
  2. the SFTP one runs: a file per region, gzipped and encrypted for the
     partner's key, and a manifest whose checksums match; the S3 one, a
     JSON lines file of the regions' counts;
  3. each run is in the delivery log;
  4. a change to what it sends (its format) needs approving again: until
     then it has no DAG;
  5. a failing check sends nothing (more rows than it allows);
  6. a delivery of a people's table (tables.people) needs a data-protection
     approver too; once it has run, searching the log by a person's ID
     finds it, from the table as it was then (their row deleted since),
     and the log itself holds no one's ID (use case 9.2).

The project, its connections, its table and what was sent are removed after.
"""
import gzip
import hashlib
import json
import os
import socket
import subprocess
import tempfile
import threading

import k8s
import paramiko

from lakekit import check, env, finish, s3_root, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME, SFTP_PORT = "deliver", 8425
BUCKET, SFTP_USER, SFTP_PASSWORD = "partner-drop", "acme", "acme-sftp-pass-1"
DROP = tempfile.mkdtemp(prefix="sftp-")


# --- The partner's SFTP server ------------------------------------------------------------

class Server(paramiko.ServerInterface):
    def check_auth_password(self, username, password):
        return paramiko.AUTH_SUCCESSFUL if (username, password) == (SFTP_USER, SFTP_PASSWORD) else paramiko.AUTH_FAILED

    def get_allowed_auths(self, username):
        return "password"

    def check_channel_request(self, kind, chanid):
        return paramiko.OPEN_SUCCEEDED if kind == "session" else paramiko.OPEN_FAILED_ADMINISTRATIVELY_PROHIBITED


class Handle(paramiko.SFTPHandle):
    def stat(self):
        return paramiko.SFTPAttributes.from_stat(os.fstat(self.writefile.fileno()))


class Files(paramiko.SFTPServerInterface):
    """Files under DROP alone."""

    def path(self, p):
        return os.path.join(DROP, os.path.normpath("/" + p).lstrip("/"))

    def open(self, path, flags, attr):
        full = self.path(path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        h = Handle(flags)
        h.writefile = h.readfile = open(full, "wb" if flags & os.O_WRONLY else "rb")
        h.filename = full
        return h

    def stat(self, path):
        try:
            return paramiko.SFTPAttributes.from_stat(os.stat(self.path(path)))
        except OSError as e:
            return paramiko.SFTPServer.convert_errno(e.errno)

    lstat = stat

    def list_folder(self, path):
        return [paramiko.SFTPAttributes.from_stat(os.stat(os.path.join(self.path(path), f)), f) for f in os.listdir(self.path(path))]


def serve_sftp(host_key):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(("0.0.0.0", SFTP_PORT))
    sock.listen(8)

    def one(conn):
        t = paramiko.Transport(conn)
        t.add_server_key(host_key)
        t.set_subsystem_handler("sftp", paramiko.SFTPServer, Files)
        t.start_server(server=Server())
        while t.is_active():
            t.join(1)

    def loop():
        while True:
            conn, _ = sock.accept()
            threading.Thread(target=one, args=(conn,), daemon=True).start()
    threading.Thread(target=loop, daemon=True).start()


# --- The partner's key ----------------------------------------------------------------------

def gpg(*args, input=None):
    return subprocess.run(["gpg", "--batch", "--homedir", GNUPG, *args], input=input, capture_output=True, check=True).stdout


GNUPG = tempfile.mkdtemp(prefix="gnupg-")


def partner_key():
    os.chmod(GNUPG, 0o700)
    gpg("--passphrase", "", "--quick-gen-key", "acme@example.com", "rsa2048", "encr", "never")
    return gpg("--armor", "--export", "acme@example.com").decode()


# --- The platform -----------------------------------------------------------------------------

def put_connection(name, data):
    if k8s.in_cluster():
        k8s.put_secret(name, data, labels={"platform.storscale.io/connection": NAME})
    else:
        s3_root().put_object(Bucket="storscale-platform", Key=f"connections/{NAME}/{name}.json", Body=json.dumps(data).encode())


def delete_connection(name):
    if k8s.in_cluster():
        k8s._api("DELETE", f"secrets/{name}")
    else:
        s3_root().delete_object(Bucket="storscale-platform", Key=f"connections/{NAME}/{name}.json")


def deliveries(s):
    r = s.get(f"{PLATFORM}/api/projects/{NAME}/deliveries", timeout=30)
    return {d["name"]: d for d in (r.json().get("deliveries", []) if r.ok else [])}


def approve(s, name):
    return s.post(f"{PLATFORM}/api/projects/{NAME}/deliveries/{name}/approve", headers={"X-Platform-Request": "1"}, timeout=30)


def pipeline(s, dag):
    r = s.get(f"{PLATFORM}/api/projects/{NAME}/pipelines", timeout=60)
    return next((p for p in (r.json().get("pipelines", []) if r.ok else []) if p["id"] == dag), None)


def run(s, dag):
    r = s.post(f"{PLATFORM}/api/projects/{NAME}/pipelines/{dag}/runs", headers={"X-Platform-Request": "1"}, timeout=60)
    run_id = r.json().get("run") if r.ok else None
    if not run_id:
        return f"not started: HTTP {r.status_code} {r.text[:200]}"
    return wait(lambda: next((x["state"] for x in (pipeline(s, dag) or {}).get("runs", [])
                              if x["id"] == run_id and x["state"] in ("success", "failed")), None), 600, every=10) or "still running"


def main():
    host_key = paramiko.RSAKey.generate(2048)
    serve_sftp(host_key)
    here = socket.gethostbyname(socket.gethostname())
    s3 = s3_root()
    try:
        s3.create_bucket(Bucket=BUCKET)
    except Exception:  # noqa: BLE001 (there)
        pass
    public = partner_key()
    put_connection("storscale-test-sftp", {"host": here, "port": str(SFTP_PORT), "username": SFTP_USER, "password": SFTP_PASSWORD,
                                 "hostKey": f"ssh-rsa {host_key.get_base64()}", "path": "upload"})
    put_connection("storscale-test-s3", {"endpoint": "http://buckets:9000", "bucket": BUCKET, "prefix": "acme",
                               "accessKey": env["BUCKETS_ROOT_USER"], "secretKey": env["BUCKETS_ROOT_PASSWORD"]})
    put_connection("storscale-test-pgp", {"pgpPublicKey": public})

    bob, alice = platform("bob"), platform("alice")
    sftp_delivery = {"name": "acme", "table": "regions", "format": "csv", "gzip": True, "files": "acme_{date}_{part}.csv.gz.pgp",
                     "splitBy": "region", "schedule": "0 6 * * 1", "target": "storscale-test-sftp", "pgp": "storscale-test-pgp",
                     "checks": {"minRows": 1, "parts": ["EU", "US"]}}
    s3_delivery = {"name": "counts", "query": "SELECT region, count(*) AS n FROM regions GROUP BY region", "format": "jsonl",
                   "files": "acme_counts_{date}.jsonl", "schedule": "0 6 * * 1", "target": "storscale-test-s3"}
    spec = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
            "spec": {"description": "File deliveries: the deliveries suite's.",
                     "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "editor"}], "tables": {},
                     "pipelines": {"serviceAccount": "airflow-pipelines"}, "deliveries": [sftp_delivery, s3_delivery]}}
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps({**spec, "spec": {**spec["spec"], "deliveries": []}}), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 600, every=10)
        sql("bob", "CREATE TABLE regions AS SELECT * FROM (VALUES (1, 'EU', 10.5), (2, 'EU', 20.0), (3, 'US', 7.25)) AS t (id, region, amount)",
            "iceberg", NAME)
        r = bob.put(url, data=json.dumps(spec), timeout=30)
        check("bob makes a project with two deliveries (SFTP, S3)", r.ok and ready is not None, r.text[:200])

        # 1. Approval.
        ds = deliveries(bob)
        check("a delivery isn't approved until another editor approves it",
              not ds.get("acme", {}).get("approved", True) and "other than bob" in " ".join(ds.get("acme", {}).get("needs", [])), ds.get("acme"))
        code = approve(bob, "acme").status_code
        check("... bob, who made it, can't", code == 403, f"HTTP {code}")
        a1, a2 = approve(alice, "acme"), approve(alice, "counts")
        check("... alice (another editor) does", a1.ok and a1.json().get("approved") and a2.ok and a2.json().get("approved"), (a1.text[:120], a2.text[:120]))

        # 2. It runs.
        found = wait(lambda: pipeline(bob, "delivery_deliver_acme") and pipeline(bob, "delivery_deliver_counts"), 420, every=10)
        check("... and its DAG is one of the project's pipelines", found is not None)
        state = run(bob, "delivery_deliver_acme")
        today = __import__("datetime").datetime.now(__import__("datetime").timezone.utc).strftime("%Y-%m-%d")
        sent = sorted(f for f in (os.listdir(os.path.join(DROP, "upload")) if os.path.isdir(os.path.join(DROP, "upload")) else []) if today in f)
        check("the SFTP delivery runs: a file per region, and a manifest", state == "success" and len(sent) == 3, f"{state}: {sent}")
        manifest = json.load(open(os.path.join(DROP, "upload", next(f for f in sent if f.startswith("manifest_"))))) if any(
            f.startswith("manifest_") for f in sent) else {}
        eu = next((f for f in sent if "_EU." in f), None)
        body = open(os.path.join(DROP, "upload", eu), "rb").read() if eu else b""
        clear = gzip.decompress(gpg("--decrypt", input=body)).decode() if body else ""
        check("... each encrypted for the partner's key and gzipped: EU's two rows",
              clear.splitlines()[:1] == ["id,region,amount"] and len(clear.splitlines()) == 3, clear[:200])
        entry = next((f for f in manifest.get("files", []) if f["name"] == eu), {})
        check("... and the manifest's checksum is the file's", entry.get("sha256") == hashlib.sha256(body).hexdigest() and entry.get("rows") == 2, entry)
        state = run(bob, "delivery_deliver_counts")
        keys = [o["Key"] for o in s3.list_objects_v2(Bucket=BUCKET, Prefix="acme/").get("Contents", [])]
        counts = next((k for k in keys if "counts" in k), None)
        lines = s3.get_object(Bucket=BUCKET, Key=counts)["Body"].read().decode().splitlines() if counts else []
        check("the S3 delivery runs: the regions' counts, as JSON lines, beside a manifest",
              state == "success" and sorted(json.loads(x)["n"] for x in lines) == [1, 2] and any("manifest_" in k for k in keys), f"{state}: {keys}")

        # 3. The log.
        runs = deliveries(bob).get("acme", {}).get("runs", [])
        check("each run is in the delivery log: its files, checksums and target",
              runs and len(runs[0].get("files", [])) == 2 and runs[0].get("target", "").startswith("sftp://"), runs[:1])

        # 4. A change to what it sends.
        changed = {**spec, "spec": {**spec["spec"], "deliveries": [{**sftp_delivery, "format": "jsonl"}, s3_delivery]}}
        r = bob.put(url, data=json.dumps(changed), timeout=30)
        ds = deliveries(bob)
        gone = wait(lambda: pipeline(bob, "delivery_deliver_acme") is None, 300, every=10)
        check("a change to what it sends needs approving again, and until then it has no DAG",
              r.ok and not ds.get("acme", {}).get("approved", True) and ds.get("counts", {}).get("approved") and gone, ds.get("acme", {}).get("needs"))

        # 5. A failing check.
        strict = {**changed, "spec": {**changed["spec"], "deliveries": [changed["spec"]["deliveries"][0], {**s3_delivery, "checks": {"maxRows": 1}}]}}
        bob.put(url, data=json.dumps(strict), timeout=30)
        # Its DAG says its checks: the run waits for Airflow to have the new ones.
        wait(lambda: '"maxRows": 1' in ((pipeline(bob, "delivery_deliver_counts") or {}).get("description") or ""), 300, every=10)
        for o in s3.list_objects_v2(Bucket=BUCKET, Prefix="acme/").get("Contents", []):
            s3.delete_object(Bucket=BUCKET, Key=o["Key"])
        state = run(bob, "delivery_deliver_counts")
        after = [o["Key"] for o in s3.list_objects_v2(Bucket=BUCKET, Prefix="acme/").get("Contents", [])]
        check("a failing check (more rows than allowed) sends nothing, and the run fails", state == "failed" and after == [], f"{state}: {after}")

        # 6. What was sent about a person (9.2): a table of people's data, declared.
        sql("bob", "CREATE TABLE customers AS SELECT * FROM (VALUES ('C-1042', 'Ana', 'EU'), ('C-2000', 'Ben', 'US')) AS t (customer_id, name, region)",
            "iceberg", NAME)
        people = {"name": "people", "table": "customers", "format": "csv", "files": "people_{date}.csv", "schedule": "0 6 * * 1",
                  "target": "storscale-test-s3"}
        personal = {**strict, "spec": {**strict["spec"], "tables": {"people": [{"table": "customers", "column": "customer_id"}]},
                                       "deliveries": strict["spec"]["deliveries"] + [people]}}
        r = bob.put(url, data=json.dumps(personal), timeout=30)
        needs = deliveries(bob).get("people", {}).get("needs", [])
        check("a delivery of a people's table is personal: it needs a data-protection approver too",
              r.ok and any("data-protection" in n for n in needs), (r.text[:200], needs))
        approve(alice, "people")
        if not deliveries(bob).get("people", {}).get("approved"):
            print("skip what was sent about a person: no data-protection approver here besides bob, who changed it (deliveries.approverGroups)")
        else:
            wait(lambda: pipeline(bob, "delivery_deliver_people"), 420, every=10)
            state = run(bob, "delivery_deliver_people")
            sql("bob", "DELETE FROM customers WHERE customer_id = 'C-1042'", "iceberg", NAME)   # gone since: found as it was

            def search(s, person):
                r = s.get(f"{PLATFORM}/api/projects/{NAME}/deliveries/search", params={"person": person}, timeout=120)
                return r.status_code, (r.json() if r.ok else {})
            code, found = search(alice, "C-1042")
            hits = found.get("runs", [])
            check("alice (a data-protection approver) finds the run that sent C-1042, from the table as it was then, though their row is gone now",
                  state == "success" and code == 200 and len(hits) == 1 and hits[0].get("delivery") == "people" and hits[0].get("rows") == 1
                  and hits[0].get("target", "").startswith("s3://"), (state, code, found))
            code, none = search(alice, "C-9999")
            check("... and none for someone it didn't send", code == 200 and none.get("runs") == [] and none.get("checked", 0) >= 1, none)
            log_runs = deliveries(bob).get("people", {}).get("runs", [])
            check("... the delivery log holds no one's ID", log_runs and "C-1042" not in json.dumps(log_runs), log_runs[:1])
    finally:
        try:
            sql("bob", "DROP TABLE IF EXISTS regions", "iceberg", NAME)
            sql("bob", "DROP TABLE IF EXISTS customers", "iceberg", NAME)
        except Exception as e:  # noqa: BLE001
            print(f"  (dropping regions and customers: {e})")
        bob.delete(url, headers={"X-Platform-Request": "1"}, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
        for c in ("storscale-test-sftp", "storscale-test-s3", "storscale-test-pgp"):
            delete_connection(c)
        for o in s3.list_objects_v2(Bucket=BUCKET).get("Contents", []):
            s3.delete_object(Bucket=BUCKET, Key=o["Key"])
    finish()


if __name__ == "__main__":
    main()
