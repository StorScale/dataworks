"""JupyterHub for the JupyterHub example: Keycloak sign-in, one container per
person, and each person's own Buckets credentials in their notebooks."""
import json
import os

c = get_config()  # noqa: F821 (JupyterHub provides it)

# Browsers reach Keycloak through the gateway (KEYCLOAK_URL). The hub calls it
# directly: its HTTP client (libcurl) resolves *.localhost names to the hub's
# own loopback, not to the gateway. Tokens name the gateway's address either way.
KEYCLOAK = os.environ["KEYCLOAK_URL"] + "/realms/lakehouse/protocol/openid-connect"
KEYCLOAK_DIRECT = "http://keycloak:8080/sso/realms/lakehouse/protocol/openid-connect"

# --- Sign-in: Keycloak, through OpenID Connect ---------------------------------------
c.JupyterHub.authenticator_class = "generic-oauth"
c.GenericOAuthenticator.client_id = "jupyterhub"
c.GenericOAuthenticator.client_secret = os.environ["JUPYTERHUB_OAUTH_SECRET"]
c.GenericOAuthenticator.oauth_callback_url = os.environ["NOTEBOOKS_URL"] + "/hub/oauth_callback"
c.GenericOAuthenticator.authorize_url = f"{KEYCLOAK}/auth"
c.GenericOAuthenticator.token_url = f"{KEYCLOAK_DIRECT}/token"
c.GenericOAuthenticator.userdata_url = f"{KEYCLOAK_DIRECT}/userinfo"
c.GenericOAuthenticator.logout_redirect_url = f"{KEYCLOAK}/logout"
c.GenericOAuthenticator.scope = ["openid", "profile", "email"]
c.GenericOAuthenticator.username_claim = "preferred_username"

# Keycloak's groups decide who may sign in and who runs the hub. The hub
# mirrors them as JupyterHub groups (manage_groups) on every sign-in.
c.GenericOAuthenticator.manage_groups = True
c.GenericOAuthenticator.auth_state_groups_key = "oauth_user.groups"
c.GenericOAuthenticator.allowed_groups = {"analysts", "engineers"}
c.GenericOAuthenticator.admin_groups = {"engineers"}

# Keep each person's tokens (encrypted with JUPYTERHUB_CRYPT_KEY) and refresh
# them with Keycloak's refresh token, so notebooks can always get a current one.
c.GenericOAuthenticator.enable_auth_state = True
c.GenericOAuthenticator.refresh_pre_spawn = True
c.GenericOAuthenticator.auth_refresh_age = 120

# A notebook server's own API token may read its person's tokens, and nothing
# of anyone else's: buckets_lake uses this to get Buckets credentials. A
# server's token can only hold scopes its person holds, so people get the
# scope for their own tokens too (admins already have it).
c.JupyterHub.load_roles = [
    {"name": "user", "scopes": ["self", "admin:auth_state!user"]},
    {"name": "server",
     "scopes": ["users:activity!user", "access:servers!server", "read:users:name!user", "admin:auth_state!user"]},
]

# --- In the platform's frame ---------------------------------------------------------
# The platform (PLATFORM_URL) shows the hub and each notebook server in a frame;
# no other page may.
FRAMED_BY = {"Content-Security-Policy": "frame-ancestors 'self'"}  # the platform is the same origin
c.JupyterHub.tornado_settings = {"headers": FRAMED_BY}

# --- Notebooks: a container per person (Compose), or a pod per person (Kubernetes) ----
NOTEBOOK_ENV = {"BUCKETS_ENDPOINT": "http://buckets:9000", "BUCKETS_REGION": "us-east-1",
                "STORSCALE_PLATFORMD_URL": "http://platformd:8080"}   # MLflow (15.1), through the platform: buckets_lake.mlflow()
NOTEBOOK_ARGS = ["--ServerApp.tornado_settings=" + json.dumps({"headers": FRAMED_BY})]
# Starting includes pulling the notebook image where a node has none yet (over a gigabyte;
# the chart pulls it onto every node ahead of time, notebooks.prePull).
c.Spawner.start_timeout = int(os.environ.get("NOTEBOOK_START_TIMEOUT", "600"))
c.Spawner.http_timeout = 120  # a notebook server's first start can be slow on a busy machine
c.Spawner.notebook_dir = "/home/jovyan/work"


# --- Distributed Spark, on Kubernetes ------------------------------------------------
# Spark Connect's server, in a container beside the person's notebook, is Spark's
# driver: buckets_lake.spark() is a session on it (sc://localhost:15002, which only
# the pod reaches), as the person. It starts executors in the chart's Spark namespace
# as jobs need them, and lets them go when idle. Only its container has the pod's
# Kubernetes token (notebook-spark's: pods in that namespace); the notebook has none.

SPARK_TOKEN = "/var/run/secrets/kubernetes.io/serviceaccount"   # where Spark's Kubernetes client looks


def _bytes(size):
    """A Kubernetes or Spark size (2G, 1536Mi, 4g) in bytes."""
    units = {"k": 10**3, "m": 10**6, "g": 10**9, "t": 10**12, "ki": 2**10, "mi": 2**20, "gi": 2**30, "ti": 2**40}
    s = str(size).strip().lower().rstrip("b")
    for u in sorted(units, key=len, reverse=True):
        if s.endswith(u):
            return int(float(s[:-len(u)]) * units[u])
    return int(s)


def spark_container(spark):
    driver, ex = spark["driver"], spark["executors"]
    memory = _bytes(driver["memory"])
    heap = memory * 3 // 5 // 2**20  # MiB: the JVM's heap; the rest is the JVM's own and Python's
    # Spark won't start a driver whose heap is under 450 MiB x 1.5 (as the JVM reports it, a
    # little under -Xmx): Spark Connect's server would exit, and the notebook find nothing on 15002.
    if heap < 512:
        raise ValueError(f"notebooks.spark.driver.memory {driver['memory']}: Spark's driver needs at least 1G")
    conf = {
        "spark.kubernetes.namespace": spark["namespace"],
        "spark.kubernetes.container.image": spark["image"],
        "spark.kubernetes.container.image.pullPolicy": spark["pullPolicy"],
        "spark.kubernetes.executor.podTemplateFile": "/etc/storscale-spark/executor.yaml",
        "spark.kubernetes.executor.label.storscale-notebook": "{user_server}",   # the hub's, as the notebook stops
        "spark.driver.host": "$(POD_IP)", "spark.driver.bindAddress": "0.0.0.0",
        "spark.driver.port": "7078", "spark.blockManager.port": "7079",
        "spark.connect.grpc.binding.address": "127.0.0.1",   # the notebook's alone
        "spark.authenticate": "true",                        # the driver and its executors only
        "spark.dynamicAllocation.enabled": "true", "spark.dynamicAllocation.shuffleTracking.enabled": "true",
        "spark.dynamicAllocation.minExecutors": "0", "spark.dynamicAllocation.maxExecutors": str(ex["max"]),
        "spark.dynamicAllocation.executorIdleTimeout": ex["idleTimeout"],
        "spark.executor.cores": str(ex["cores"]), "spark.kubernetes.executor.request.cores": str(ex.get("requestCores") or ex["cores"]),
        "spark.kubernetes.executor.limit.cores": str(ex["coreLimit"]), "spark.executor.memory": ex["memory"],
        # The platform's tables, as Trino names them (iceberg.<namespace>.<table>), and the default
        # catalog: spark.table("sales.orders"). Its credentials are the session's (buckets_lake.spark()),
        # and so is its catalog: platformd's, its tables alone (Nessie answers the platform's account only).
        "spark.sql.extensions": "org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions",
        "spark.sql.defaultCatalog": "iceberg",
        "spark.sql.catalog.iceberg": "org.apache.iceberg.spark.SparkCatalog",
        "spark.sql.catalog.iceberg.type": "rest",
        "spark.sql.catalog.iceberg.uri": "http://platformd:8080/internal/spark-catalog/",
        "spark.sql.catalog.iceberg.warehouse": "warehouse",
        "spark.sql.catalog.iceberg.io-impl": "org.apache.iceberg.aws.s3.S3FileIO",
        "spark.sql.catalog.iceberg.s3.endpoint": "http://buckets:9000",
        "spark.sql.catalog.iceberg.s3.path-style-access": "true",
        "spark.sql.catalog.iceberg.client.region": "us-east-1",
    }
    args = ["--master", "k8s://https://kubernetes.default.svc", "--name", "notebook",
            "--driver-memory", f"{heap}m"]
    for k, v in conf.items():
        args += ["--conf", f"{k}={v}"]
    return {
        "name": "spark",
        "image": spark["image"],
        "imagePullPolicy": spark["pullPolicy"],
        "command": ["/opt/spark/sbin/start-connect-server.sh"],
        "args": args,
        "env": [{"name": "POD_IP", "valueFrom": {"fieldRef": {"fieldPath": "status.podIP"}}},
                {"name": "SPARK_NO_DAEMONIZE", "value": "true"},   # in the foreground: its log is the container's
                {"name": "SPARK_LOG_DIR", "value": "/tmp/spark-logs"}, {"name": "SPARK_PID_DIR", "value": "/tmp"}],
        "resources": {"requests": {"cpu": "100m", "memory": str(driver.get("requestMemory") or memory)},
                      "limits": {"cpu": str(driver["cpu"]), "memory": str(memory)}},
        "securityContext": {"allowPrivilegeEscalation": False, "runAsUser": 1000, "runAsNonRoot": True,
                            "capabilities": {"drop": ["ALL"]}},
        "volumeMounts": [{"name": "spark-token", "mountPath": SPARK_TOKEN, "readOnly": True},
                         {"name": "spark-executor", "mountPath": "/etc/storscale-spark", "readOnly": True}],
        # Its executors let go before the pod is: Spark deletes them as it stops.
        "lifecycle": {"preStop": {"exec": {"command": ["/opt/spark/sbin/stop-connect-server.sh"]}}},
    }


def spark_profiles(spark, volumes):
    ex = spark["executors"]
    volumes = volumes + [
        {"name": "spark-token", "projected": {"sources": [
            {"serviceAccountToken": {"path": "token", "expirationSeconds": 3607}},
            {"configMap": {"name": "kube-root-ca.crt", "items": [{"key": "ca.crt", "path": "ca.crt"}]}},
            {"downwardAPI": {"items": [{"path": "namespace", "fieldRef": {"fieldPath": "metadata.namespace"}}]}}]}},
        {"name": "spark-executor", "configMap": {"name": "notebook-spark"}},
    ]
    profiles = [
        {"display_name": "Distributed Spark", "slug": "spark",
         "description": "Your notebook, with Spark beside it: buckets_lake.spark() is a Spark session on the "
                        f"platform's tables, as you. Executors start as your jobs need them (up to {ex['max']}, "
                        f"{ex['cores']} CPUs and {ex['memory']} each) and stop when idle.",
         "kubespawner_override": {
             "service_account": "notebook-spark",
             "automount_service_account_token": False,   # the Spark container's alone (spark-token)
             "extra_containers": [spark_container(spark)],
             "volumes": volumes,
             "environment": {"SPARK_REMOTE": "sc://localhost:15002",
                             "STORSCALE_SPARK_CREDENTIAL_HOURS": str(spark["credentialHours"])},
         }},
        {"display_name": "Notebook only", "slug": "notebook",
         "description": "Your notebook alone: pandas, SQL in Trino (buckets_lake.sql) and the models' libraries."},
    ]
    # The default first (notebooks.spark.default): Spark, unless the install says otherwise.
    if spark.get("default") is False:
        profiles.reverse()
    profiles[0]["default"] = True
    return profiles


def spark_executors_gone(namespace):
    """As a notebook stops: its executors that outlived its driver (the pod stopped before Spark let them go)."""
    async def hook(spawner):
        try:
            await spawner.api.delete_collection_namespaced_pod(
                namespace, label_selector=f"storscale-notebook={spawner._expand_user_properties('{user_server}')}")
        except Exception:
            spawner.log.exception("couldn't delete %s's Spark executors", spawner.user.name)
    return hook

if os.environ.get("STORSCALE_K8S") == "true":
    c.JupyterHub.spawner_class = "kubespawner.KubeSpawner"
    c.KubeSpawner.namespace = os.environ["STORSCALE_NAMESPACE"]
    c.KubeSpawner.image = os.environ["NOTEBOOK_IMAGE"]
    c.KubeSpawner.image_pull_policy = os.environ.get("NOTEBOOK_PULL_POLICY", "IfNotPresent")
    # Each person's notebook has a person's limits: notebooks serve every
    # project a person is in, so they aren't a project's.
    c.KubeSpawner.mem_limit = os.environ.get("NOTEBOOK_MEMORY", "1G")
    c.KubeSpawner.cpu_limit = float(os.environ.get("NOTEBOOK_CPUS", "2"))
    c.KubeSpawner.cpu_guarantee = 0.1   # without it, Kubernetes reserves the whole limit for each notebook
    c.KubeSpawner.mem_guarantee = "256M"
    c.KubeSpawner.environment = NOTEBOOK_ENV
    # Not Kubernetes' variables for every Service in the namespace (<NAME>_SERVICE_HOST, ...): a notebook reaches
    # what it uses by name, and its environment holds only what the platform gives it.
    c.KubeSpawner.extra_pod_config = {"enableServiceLinks": False}
    # KubeSpawner passes args only with a command of its own (otherwise it
    # runs the image's and drops them): the CSP header lets the platform frame it.
    c.KubeSpawner.cmd = ["start-singleuser.py"]   # the image's own start, with its hooks
    c.KubeSpawner.args = NOTEBOOK_ARGS
    c.KubeSpawner.uid = 1000
    c.KubeSpawner.fs_gid = 100
    # Each person's files, on a volume of their own that outlives their server.
    c.KubeSpawner.storage_pvc_ensure = True
    c.KubeSpawner.pvc_name_template = "notebooks-{user_server}"
    c.KubeSpawner.storage_capacity = os.environ.get("NOTEBOOK_STORAGE", "1Gi")
    c.KubeSpawner.volumes = [{"name": "work", "persistentVolumeClaim": {"claimName": "notebooks-{user_server}"}}]
    c.KubeSpawner.volume_mounts = [{"name": "work", "mountPath": "/home/jovyan/work"}]
    # A private CA the platform's certificate comes from (the chart's global.ca):
    # the chart gives the init container, volumes and environment that trust it.
    ca = json.loads(os.environ.get("NOTEBOOK_CA") or "{}")
    if ca:
        c.KubeSpawner.init_containers = ca["init_containers"]
        c.KubeSpawner.volumes += ca["volumes"]
        c.KubeSpawner.volume_mounts += ca["volume_mounts"]
        c.KubeSpawner.environment.update({e["name"]: e["value"] for e in ca["env"]})
    # Distributed Spark (the chart's notebooks.spark): a profile, the default unless notebooks.spark.default is false.
    spark = json.loads(os.environ.get("NOTEBOOK_SPARK") or "{}")
    if spark:
        c.KubeSpawner.profile_list = spark_profiles(spark, c.KubeSpawner.volumes)
        c.KubeSpawner.post_stop_hook = spark_executors_gone(spark["namespace"])
else:
    c.JupyterHub.spawner_class = "docker"
    c.DockerSpawner.image = "storscale-notebook"
    c.DockerSpawner.network_name = "storscale"
    c.DockerSpawner.remove = True
    c.DockerSpawner.mem_limit = "1G"
    c.DockerSpawner.cpu_limit = float(os.environ.get("NOTEBOOK_CPUS", "2"))
    c.DockerSpawner.volumes = {"storscale-notebooks-{username}": "/home/jovyan/work"}
    c.DockerSpawner.environment = NOTEBOOK_ENV
    c.DockerSpawner.args = NOTEBOOK_ARGS

# At the platform's /notebooks/: the same address as the platform, so its cookies are first-party.
c.JupyterHub.base_url = "/notebooks/"
c.JupyterHub.hub_ip = "0.0.0.0"
c.JupyterHub.hub_connect_ip = "jupyterhub"
c.JupyterHub.cookie_secret_file = "/srv/jupyterhub/data/cookie_secret"
c.JupyterHub.db_url = "sqlite:////srv/jupyterhub/data/jupyterhub.sqlite"
c.ConfigurableHTTPProxy.pid_file = "/tmp/jupyterhub-proxy.pid"   # the hub runs as an unprivileged user
