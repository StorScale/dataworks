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
NOTEBOOK_ENV = {"BUCKETS_ENDPOINT": "http://buckets:9000", "BUCKETS_REGION": "us-east-1"}
NOTEBOOK_ARGS = ["--ServerApp.tornado_settings=" + json.dumps({"headers": FRAMED_BY})]
c.Spawner.start_timeout = 120
c.Spawner.http_timeout = 120  # a notebook server's first start can be slow on a busy machine
c.Spawner.notebook_dir = "/home/jovyan/work"

if os.environ.get("STORSCALE_K8S") == "true":
    c.JupyterHub.spawner_class = "kubespawner.KubeSpawner"
    c.KubeSpawner.namespace = os.environ["STORSCALE_NAMESPACE"]
    c.KubeSpawner.image = os.environ["NOTEBOOK_IMAGE"]
    c.KubeSpawner.image_pull_policy = os.environ.get("NOTEBOOK_PULL_POLICY", "IfNotPresent")
    # Each person's notebook has a person's limits: notebooks serve every
    # project a person is in, so they aren't a project's.
    c.KubeSpawner.mem_limit = os.environ.get("NOTEBOOK_MEMORY", "1G")
    c.KubeSpawner.cpu_limit = float(os.environ.get("NOTEBOOK_CPUS", "2"))
    c.KubeSpawner.mem_guarantee = "256M"
    c.KubeSpawner.environment = NOTEBOOK_ENV
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
