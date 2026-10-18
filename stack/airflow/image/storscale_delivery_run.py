"""One run of a project's delivery (use case 9.1), in its own virtualenv
(/opt/deliver/bin/python), as the DAG file storscale_deliveries.py starts it.

STORSCALE_DELIVERY is the delivery as the platform operator lists it (its
project, catalog, namespace and spec); STORSCALE_DELIVERY_SECRET its target
and the recipient's key (its Airflow connection's extra); STORSCALE_TOKEN the
project's pipelines' Keycloak token; STORSCALE_DATE the run's date.

In order: the checks (a failing one sends nothing); the rows, read through
Trino as the project's pipelines; the files (a file per value of splitBy),
compressed and encrypted as asked; a manifest beside them (each file's name,
rows, bytes and SHA-256, the tables' snapshots read); all sent to the SFTP
server (its host key checked) or the S3 bucket, tried three times; the run
recorded with the platform (the delivery log).
"""
import csv
import datetime
import gzip
import hashlib
import io
import json
import os
import subprocess
import sys
import tempfile
import time

import requests
import trino

pd = json.loads(os.environ["STORSCALE_DELIVERY"])
secret = json.loads(os.environ.get("STORSCALE_DELIVERY_SECRET") or "{}")
token, day = os.environ["STORSCALE_TOKEN"], os.environ["STORSCALE_DATE"]
d, project, cat, ns = pd["delivery"], pd["project"], pd["catalog"], pd["namespace"]
PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080")


def log(msg):
    print(f"delivery {project}/{d['name']}: {msg}", flush=True)


def connect():
    return trino.dbapi.connect(host=os.environ.get("TRINO_HOST", "trino"), port=8443, http_scheme="https",
                               verify=os.environ.get("TRINO_CA", "/tls/cert.pem"), auth=trino.auth.JWTAuthentication(token),
                               catalog=cat, schema=ns, client_tags=[f"project:{project}"])


def query(sql):
    conn = connect()
    try:
        cur = conn.cursor()
        cur.execute(sql)
        rows = cur.fetchall()
        return [c[0] for c in cur.description], rows
    finally:
        conn.close()


def source():
    return f'"{cat}"."{ns}"."{d["table"]}"' if d.get("table") else f"({d['query']})"


# --- The checks: a failing one sends nothing ------------------------------------------

def check():
    c = d.get("checks") or {}
    problems = []
    _, (count,) = query(f"SELECT count(*) FROM {source()} AS t")
    n = count[0]
    if c.get("minRows") and n < c["minRows"]:
        problems.append(f"{n} rows, fewer than {c['minRows']}")
    if c.get("maxRows") and n > c["maxRows"]:
        problems.append(f"{n} rows, more than {c['maxRows']}")
    if c.get("within") and d.get("table"):
        unit = {"m": 60, "h": 3600, "d": 86400}[c["within"][-1]]
        _, rows = query(f'SELECT max(committed_at) FROM "{cat}"."{ns}"."{d["table"]}$snapshots"')
        last = rows[0][0]
        if last is None or (datetime.datetime.now(datetime.timezone.utc) - last).total_seconds() > int(c["within"][:-1]) * unit:
            problems.append(f"{d['table']} not changed within {c['within']} (last {last})")
    if c.get("parts"):
        _, rows = query(f'SELECT DISTINCT "{d["splitBy"]}" FROM {source()} AS t')
        missing = sorted(set(c["parts"]) - {str(r[0]) for r in rows})
        if missing:
            problems.append(f"parts missing: {', '.join(missing)}")
    return n, problems


def snapshots():
    if not d.get("table"):
        return {}
    _, rows = query(f'SELECT snapshot_id FROM "{cat}"."{ns}"."{d["table"]}$snapshots" ORDER BY committed_at DESC LIMIT 1')
    return {f"{cat}.{ns}.{d['table']}": str(rows[0][0]) if rows else None}


# --- The files -------------------------------------------------------------------------

def render(columns, rows):
    fmt = d["format"]
    if fmt == "csv":
        buf = io.StringIO()
        w = csv.writer(buf)
        w.writerow(columns)
        w.writerows([[("" if v is None else v) for v in r] for r in rows])
        return buf.getvalue().encode()
    if fmt == "jsonl":
        return "".join(json.dumps(dict(zip(columns, r)), default=str) + "\n" for r in rows).encode()
    import pyarrow as pa
    import pyarrow.parquet as pq
    table = pa.Table.from_pylist([dict(zip(columns, r)) for r in rows])
    buf = io.BytesIO()
    pq.write_table(table, buf)
    return buf.getvalue()


def encrypt(data):
    key = secret.get("pgpPublicKey")
    if not key:
        return data
    with tempfile.TemporaryDirectory() as home:
        env = {**os.environ, "GNUPGHOME": home}
        with open(os.path.join(home, "key.asc"), "w") as f:
            f.write(key)
        imported = subprocess.run(["gpg", "--batch", "--import", os.path.join(home, "key.asc")], env=env, capture_output=True, text=True)
        if imported.returncode != 0:
            raise RuntimeError(f"the recipient's key: {imported.stderr.strip()[:300]}")
        listed = subprocess.run(["gpg", "--batch", "--with-colons", "--list-keys"], env=env, capture_output=True, text=True, check=True)
        fpr = next(line.split(":")[9] for line in listed.stdout.splitlines() if line.startswith("fpr:"))
        out = subprocess.run(["gpg", "--batch", "--yes", "--trust-model", "always", "--recipient", fpr, "--encrypt"],
                             input=data, env=env, capture_output=True, check=True)
        return out.stdout


def files(columns, rows):
    """name -> (bytes, rows)"""
    groups = {"": rows}
    if d.get("splitBy"):
        i = columns.index(d["splitBy"]) if d["splitBy"] in columns else None
        if i is None:
            raise RuntimeError(f"splitBy {d['splitBy']}: no such column")
        groups = {}
        for r in rows:
            groups.setdefault(str(r[i]), []).append(r)
    out = {}
    for part, rs in sorted(groups.items()):
        name = d["files"].replace("{date}", day).replace("{part}", part)
        data = render(columns, rs)
        if d.get("gzip"):
            data = gzip.compress(data, mtime=0)
        out[name] = (encrypt(data), len(rs))
    return out


# --- The target ------------------------------------------------------------------------

def send(named):
    t = secret.get("target") or {}
    if t.get("bucket"):
        import boto3
        from botocore.config import Config
        s3 = boto3.client("s3", endpoint_url=t.get("endpoint") or None, region_name=t.get("region") or "us-east-1",
                          aws_access_key_id=t.get("accessKey"), aws_secret_access_key=t.get("secretKey"),
                          config=Config(s3={"addressing_style": "path"}))
        prefix = (t.get("prefix") or "").strip("/")
        for name, data in named.items():
            s3.put_object(Bucket=t["bucket"], Key=f"{prefix}/{name}" if prefix else name, Body=data)
        return f"s3://{t['bucket']}/{prefix}"
    import paramiko
    if not t.get("hostKey"):
        raise RuntimeError("the SFTP target has no hostKey: the server's key is checked (as a known_hosts line's key)")
    kind, b64 = t["hostKey"].split()[-2:]
    expected = paramiko.PKey.from_type_string(kind, __import__("base64").b64decode(b64))
    transport = paramiko.Transport((t["host"], int(t.get("port") or 22)))
    try:
        transport.start_client(timeout=30)
        got = transport.get_remote_server_key()
        if got.get_name() != expected.get_name() or got.asbytes() != expected.asbytes():
            raise RuntimeError(f"the SFTP server's key isn't the one given ({got.get_name()}): nothing sent")
        if t.get("sshPrivateKey"):
            key = paramiko.PKey.from_private_key(io.StringIO(t["sshPrivateKey"]))
            transport.auth_publickey(t["username"], key)
        else:
            transport.auth_password(t["username"], t["password"])
        sftp = paramiko.SFTPClient.from_transport(transport)
        path = (t.get("path") or ".").rstrip("/")
        for name, data in named.items():
            sftp.putfo(io.BytesIO(data), f"{path}/{name}")
        return f"sftp://{t['username']}@{t['host']}/{path}"
    finally:
        transport.close()


def main():
    n, problems = check()
    if problems:
        log("checks failed, nothing sent: " + "; ".join(problems))
        sys.exit(1)
    columns, rows = query(f"SELECT * FROM {source()} AS t")
    snaps = snapshots()
    out = files(columns, rows)
    manifest = {"delivery": d["name"], "project": project, "date": day, "rows": len(rows), "snapshots": snaps,
                "files": [{"name": k, "rows": r, "bytes": len(b), "sha256": hashlib.sha256(b).hexdigest()} for k, (b, r) in out.items()]}
    named = {k: b for k, (b, _) in out.items()}
    named[f"manifest_{d['name']}_{day}.json"] = json.dumps(manifest, indent=1).encode()
    for attempt in range(1, 4):
        try:
            where = send(named)
            break
        except Exception as e:  # noqa: BLE001 (a transfer: tried again)
            if attempt == 3 or "isn't the one given" in str(e):
                raise
            log(f"sending failed ({e}); again in {10 * attempt}s")
            time.sleep(10 * attempt)
    log(f"sent {len(out)} files, {len(rows)} rows, to {where}")
    approval = d.get("approval") or {}
    run = {**manifest, "target": where, "approval": {"by": approval.get("by"), "protection": approval.get("protection"), "at": approval.get("at")},
           "summary": f"{len(out)} files, {len(rows)} rows, to {where}"}
    r = requests.post(f"{PLATFORM}/api/projects/{project}/deliveries/{d['name']}/runs", json=run, timeout=60,
                      headers={"Authorization": f"Bearer {token}"})
    log(f"recorded: HTTP {r.status_code}")
    r.raise_for_status()


if __name__ == "__main__":
    main()
