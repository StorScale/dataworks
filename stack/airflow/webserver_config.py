"""Sign-in to Airflow's UI and API (the FAB auth manager): Keycloak, through
OpenID Connect. Keycloak's groups are Airflow's roles, set again at every
sign-in; anyone in none of the groups is refused. The shared Airflow's groups
are engineers and analysts; a project's own Airflow's are the project's
editors and readers (AIRFLOW_ROLE_GROUPS, from the operator)."""
import json
import os

from airflow.providers.fab.auth_manager.security_manager.override import FabAirflowSecurityManagerOverride
from flask_appbuilder.security.manager import AUTH_OAUTH

KEYCLOAK = os.environ["KEYCLOAK_URL"] + "/realms/lakehouse"
# Its own cookie name: the platform's tools share one address (Superset's
# Flask app would otherwise use "session" too), and so do projects' Airflows.
SESSION_COOKIE_NAME = os.environ.get("AIRFLOW_SESSION_COOKIE", "airflow_session")

AUTH_TYPE = AUTH_OAUTH
OAUTH_PROVIDERS = [{
    "name": "keycloak", "icon": "fa-key", "token_key": "access_token",
    "remote_app": {
        "client_id": "airflow",
        # The API server's alone: the scheduler and DAG processor run
        # projects' DAG code and sign no one in, so they go without it.
        "client_secret": os.environ.get("AIRFLOW_OAUTH_SECRET", ""),
        "server_metadata_url": f"{KEYCLOAK}/.well-known/openid-configuration",
        "api_base_url": f"{KEYCLOAK}/protocol/openid-connect/",
        "client_kwargs": {"scope": "openid profile email"},
    },
}]

# Engineers (a project's editors) operate pipelines (trigger, clear, see
# connections); analysts (its readers) watch them. Nobody gets Admin from
# Keycloak here.
AUTH_ROLES_MAPPING = json.loads(os.environ.get("AIRFLOW_ROLE_GROUPS") or '{"engineers": ["Op"], "analysts": ["Viewer"]}')
ALLOWED_GROUPS = set(AUTH_ROLES_MAPPING)
AUTH_USER_REGISTRATION = True
AUTH_USER_REGISTRATION_ROLE = "Viewer"
AUTH_ROLES_SYNC_AT_LOGIN = True


class KeycloakSecurityManager(FabAirflowSecurityManagerOverride):
    def get_oauth_user_info(self, provider, resp):
        me = self.appbuilder.sm.oauth_remotes[provider].get("userinfo").json()
        return {"username": me["preferred_username"], "email": me.get("email", ""),
                "first_name": me.get("given_name", ""), "last_name": me.get("family_name", ""),
                "role_keys": me.get("groups", [])}

    def auth_user_oauth(self, userinfo):
        if not ALLOWED_GROUPS & set(userinfo.get("role_keys", [])):
            return None
        return super().auth_user_oauth(userinfo)


SECURITY_MANAGER_CLASS = KeycloakSecurityManager
