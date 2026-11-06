"""Spark, and Iceberg's jars, into /opt/spark for the spark image (the Dockerfile's
spark target): each download checked against the digest pinned in the Dockerfile.
Spark's own Kubernetes entrypoint, which starts executors, becomes the image's."""
import hashlib
import io
import os
import shutil
import tarfile
import urllib.request

SPARK = os.environ["SPARK_VERSION"]
ICEBERG = os.environ["ICEBERG_VERSION"]
HOME = "/opt/spark"
JARS = {  # Iceberg's, for Spark 4.1 (Scala 2.13): sha256
    "iceberg-spark-runtime-4.1_2.13": "3123c3798af642dd537230923548e013ef41cc28b6626e5225808cf5670a0f82",
    "iceberg-aws-bundle": "dd7e49ffa4ac9b215bed846581991259e3aec9bb2fa08d6cec029c46cd66d957",
}


def get(urls, digest, algo):
    """The first of urls that answers, checked against its pinned digest."""
    for url in urls if isinstance(urls, list) else [urls]:
        try:
            with urllib.request.urlopen(url, timeout=600) as r:
                data = r.read()
        except OSError as e:
            print(f"{url}: {e}; trying the next")
            continue
        if hashlib.new(algo, data).hexdigest() != digest:
            raise SystemExit(f"{url}: its {algo} isn't the one pinned")
        return data
    raise SystemExit(f"none answered: {urls}")


# Apache's CDN, fast, has a line's latest release only; its archive has every one, slowly.
name = f"spark-{SPARK}/spark-{SPARK}-bin-hadoop3.tgz"
tgz = get([f"https://dlcdn.apache.org/spark/{name}", f"https://archive.apache.org/dist/spark/{name}"],
          os.environ["SPARK_SHA512"], "sha512")
with tarfile.open(fileobj=io.BytesIO(tgz), mode="r:gz") as t:
    for m in t.getmembers():
        m.name = m.name.split("/", 1)[1] if "/" in m.name else ""
        if m.name:
            t.extract(m, HOME, filter="tar")
for name, sha256 in JARS.items():
    jar = get(f"https://repo1.maven.org/maven2/org/apache/iceberg/{name}/{ICEBERG}/{name}-{ICEBERG}.jar", sha256, "sha256")
    with open(f"{HOME}/jars/{name}-{ICEBERG}.jar", "wb") as f:
        f.write(jar)

for script in ("entrypoint.sh", "decom.sh"):
    shutil.copy(f"{HOME}/kubernetes/dockerfiles/spark/{script}", f"/opt/{script}")
    os.chmod(f"/opt/{script}", 0o755)
for unused in ("examples", "data", "kubernetes/tests"):
    shutil.rmtree(f"{HOME}/{unused}", ignore_errors=True)
# Spark's entrypoint writes its executors' Java options to the working directory.
os.makedirs(f"{HOME}/work-dir", exist_ok=True)
shutil.chown(f"{HOME}/work-dir", int(os.environ.get("NB_UID", "1000")), int(os.environ.get("NB_GID", "100")))
