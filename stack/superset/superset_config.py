"""Superset for the Superset example: Keycloak sign-in, Keycloak groups as
Superset roles, and queries to Trino run as the person who signed in."""
import os

from flask_appbuilder.security.manager import AUTH_OAUTH
from superset.security import SupersetSecurityManager

SECRET_KEY = os.environ["SUPERSET_SECRET_KEY"]
MAPBOX_API_KEY = os.environ.get("MAPBOX_API_KEY", "")   # the deck.gl map charts' base map (empty: the layers alone)
# Its own cookie name: the platform's tools share one address (Airflow's
# Flask app would otherwise use "session" too).
SESSION_COOKIE_NAME = "superset_session"
SQLALCHEMY_DATABASE_URI = (f"postgresql+psycopg2://superset:{os.environ['SUPERSET_DB_PASSWORD']}"
                           "@superset-db:5432/superset")

# Its logo and the logo's link, as whole addresses. Under /dashboards Superset
# puts its root in front of APP_ICON and the theme's logo, and its frontend
# does it again (/dashboards/dashboards/...): a whole address is left as is.
# superset.config, being read, has its THEME_DEFAULT by now.
from superset import config as _defaults  # noqa: E402

DASHBOARDS_URL = os.environ["DASHBOARDS_URL"]
APP_ICON = f"{DASHBOARDS_URL}/static/assets/images/superset-logo-horiz.png"
THEME_DEFAULT = {**_defaults.THEME_DEFAULT,
                 "token": {**_defaults.THEME_DEFAULT["token"],
                           "brandLogoUrl": APP_ICON, "brandLogoHref": f"{DASHBOARDS_URL}/"}}

# Behind the gateway: its callback address (for Keycloak) uses the scheme and
# name people used, which the gateway forwards.
ENABLE_PROXY_FIX = True
PROXY_FIX_CONFIG = {"x_for": 1, "x_proto": 1, "x_host": 1, "x_port": 0, "x_prefix": 0}  # the host carries any port

# --- Sign-in: Keycloak, through OpenID Connect -----------------------------------------
KEYCLOAK = os.environ["KEYCLOAK_URL"] + "/realms/lakehouse"
AUTH_TYPE = AUTH_OAUTH
OAUTH_PROVIDERS = [{
    "name": "keycloak", "icon": "fa-key", "token_key": "access_token",
    "remote_app": {
        "client_id": "superset",
        "client_secret": os.environ["SUPERSET_OAUTH_SECRET"],
        "server_metadata_url": f"{KEYCLOAK}/.well-known/openid-configuration",
        "api_base_url": f"{KEYCLOAK}/protocol/openid-connect/",
        "client_kwargs": {"scope": "openid profile email"},
    },
}]

# Keycloak's groups are Superset's roles, set again at every sign-in. Everyone
# gets SQL Lab and the lakehouse database ("Lakehouse SQL", made by
# bootstrap.py); engineers may also build datasets, charts and dashboards.
ALLOWED_GROUPS = {"analysts", "engineers"}
AUTH_USER_REGISTRATION = True
AUTH_USER_REGISTRATION_ROLE = "Gamma"
AUTH_ROLES_MAPPING = {
    "analysts": ["Gamma", "sql_lab", "Lakehouse SQL"],
    "engineers": ["Alpha", "sql_lab", "Lakehouse SQL"],
}
AUTH_ROLES_SYNC_AT_LOGIN = True


class KeycloakSecurityManager(SupersetSecurityManager):
    def oauth_user_info(self, provider, response=None):
        me = self.appbuilder.sm.oauth_remotes[provider].get("userinfo").json()
        return {"username": me["preferred_username"], "email": me.get("email", ""),
                "first_name": me.get("given_name", ""), "last_name": me.get("family_name", ""),
                "role_keys": me.get("groups", [])}

    def auth_user_oauth(self, userinfo):
        # Only members of the mapped groups get in; anyone else is refused.
        if not ALLOWED_GROUPS & set(userinfo.get("role_keys", [])):
            return None
        return super().auth_user_oauth(userinfo)


CUSTOM_SECURITY_MANAGER = KeycloakSecurityManager

# The example runs on plain HTTP between containers.
TALISMAN_ENABLED = False
WTF_CSRF_ENABLED = True


# --- In the platform's frame ------------------------------------------------------------
# The platform opens Superset at /login/keycloak?next=<page>. Someone already
# signed in goes straight to <page>: Superset's own login view would send them
# to the home page instead.
def FLASK_APP_MUTATOR(app):
    from flask import redirect, request
    from flask_login import current_user

    @app.before_request
    def signed_in_go_next():
        if request.path.rstrip("/") == "/login/keycloak" and current_user.is_authenticated:
            nxt = request.args.get("next", "")
            return redirect(nxt if nxt.startswith("/") and not nxt.startswith(("//", "/\\")) else "/")
        return None
