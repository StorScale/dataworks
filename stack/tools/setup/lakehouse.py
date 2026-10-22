"""Configure the platform's base: Buckets, then Ranger.

Buckets
  - buckets `warehouse` (Iceberg tables), `scratch` (people's own files) and
    `storscale-platform` (the platform's projects, and their status);
  - policy `lakehouse-engine` (the warehouse, read and write) for the engines'
    service accounts, `trino-svc` and `nessie-svc`; policy `platform-store` for
    platformd's account, `platform-svc`;
  - policies `analysts` and `engineers`, named after the Keycloak groups people
    sign in with: `scratch` only. Nobody but the engines can read the warehouse,
    so Ranger's masks and row filters can't be bypassed by reading the files.

Ranger
  - Keycloak's users and groups, copied over, and the pipelines' service
    account in group pipelines (Keycloak doesn't list service accounts among
    its users, so Ranger's usersync would need the same);
  - the Trino service `lakehouse`, and the policies below: who may run
    queries and see the catalog. A project's tables, and who may use them,
    are the project's: the platform operator adds and removes those policies.

Safe to run again: everything is created or updated in place.
"""
from lakekit import (
    BASE_POLICIES,
    RANGER,
    allow,
    bucket_rw,
    ensure_buckets_identity,
    env,
    keycloak_people,
    log,
    ranger,
    res,
    setup_buckets,
    setup_ranger,
    sync_people,
    wait_for,
)

ICEBERG = "iceberg"
CATALOG_USER = "openmetadata"  # the catalog's service user: it reads every table, to describe and check them
# platformd's table maintenance: Trino's optimize, expire_snapshots and
# remove_orphan_files are table procedures, which need alter; optimize scans
# the table it rewrites, which needs select too. So it may read every table,
# as the catalog's user may; its password is platformd's alone.
MAINTENANCE_USER = "maintenance"
# The platform operator makes and drops the projects' sources' catalogs
# (CREATE CATALOG): that, on any catalog, and nothing in them. Who may read a
# source is its project's policies.
OPERATOR_USER = "platform-operator"
# Anyone signed in may run queries and see that the iceberg catalog is there
# (Ranger's group public), each project's own pipelines account among them
# (a group of its own, <project>-pipelines, which these shared policies can't
# name): what one may read in a namespace or table is still its project's.
POLICIES = [{**p, "policyItems": p["policyItems"] + ([allow(["execute"], groups=["public"], users=[CATALOG_USER, MAINTENANCE_USER, OPERATOR_USER])]
                                                     if p["name"] == "run queries" else [])}
            for p in BASE_POLICIES] + [
    # Everyone may see that the catalog is there; engineers may add namespaces
    # to it. What's in a namespace is up to its project. (Ranger allows one
    # access policy per resource, so the catalog's policy has an item per group.)
    {"name": "iceberg catalog", "resources": res(catalog=ICEBERG),
     "policyItems": [allow(["all"], groups=["engineers"]), allow(["use", "show"], groups=["public"], users=[CATALOG_USER, MAINTENANCE_USER])]},
    {"name": "operator: catalogs", "resources": res(catalog="*"),
     "policyItems": [allow(["create", "drop"], users=[OPERATOR_USER])]},
    {"name": "catalog: every namespace", "resources": res(catalog=ICEBERG, schema="*"),
     "policyItems": [allow(["use", "show"], users=[CATALOG_USER, MAINTENANCE_USER])]},
    {"name": "catalog: every table", "resources": res(catalog=ICEBERG, schema="*", table="*", column="*"),
     "policyItems": [allow(["select", "show"], users=[CATALOG_USER]), allow(["select", "alter", "delete"], users=[MAINTENANCE_USER])]},
    # The catalog's information_schema, which tools read to list tables and
    # columns (Superset does, to check a dataset; dbt does, as a project's
    # pipelines). Trino filters it down to what each one may see, so everyone
    # may read it (Ranger's group public): each project's pipelines too.
    {"name": "iceberg information_schema", "resources": res(catalog=ICEBERG, schema="information_schema"),
     "policyItems": [allow(["use", "show"], groups=["public"], users=[CATALOG_USER])]},
    {"name": "iceberg information_schema tables",
     "resources": res(catalog=ICEBERG, schema="information_schema", table="*", column="*"),
     "policyItems": [allow(["select"], groups=["public"], users=[CATALOG_USER])]},
]

if __name__ == "__main__":
    setup_buckets(
        buckets=["warehouse", "scratch", "storscale-platform", "storscale-backups", "mlflow"],
        policies={"lakehouse-engine": bucket_rw("warehouse"), "platform-store": bucket_rw("storscale-platform"),
                  "backup-store": bucket_rw("storscale-backups"),  # the backups' own account: that bucket alone
                  "mlflow-store": bucket_rw("mlflow"),  # MLflow's artifacts, a workspace (project) a folder
                  "analysts": bucket_rw("scratch"), "engineers": bucket_rw("scratch")},
        accounts=[(env["TRINO_S3_ACCESS_KEY"], env["TRINO_S3_SECRET_KEY"], "lakehouse-engine"),
                  (env["NESSIE_S3_ACCESS_KEY"], env["NESSIE_S3_SECRET_KEY"], "lakehouse-engine"),
                  (env["PLATFORM_STORE_ACCESS_KEY"], env["PLATFORM_STORE_SECRET_KEY"], "platform-store"),
                  (env["BACKUP_S3_ACCESS_KEY"], env["BACKUP_S3_SECRET_KEY"], "backup-store"),
                  (env["MLFLOW_S3_ACCESS_KEY"], env["MLFLOW_S3_SECRET_KEY"], "mlflow-store"),
                  # The projects' sources' copies (change capture) write their tables.
                  (env["COPY_S3_ACCESS_KEY"], env["COPY_S3_SECRET_KEY"], "lakehouse-engine")])
    ensure_buckets_identity()
    wait_for("Ranger", f"{RANGER}/login.jsp")  # unauthenticated: five failed sign-ins lock Ranger's admin
    sync_people({**keycloak_people(), "service-account-airflow-pipelines": ["pipelines"]})
    known = {u["name"] for u in ranger("GET", "/service/xusers/users?pageSize=1000")["vXUsers"]}
    for name, first, suffix in [(CATALOG_USER, "OpenMetadata", "om"), (MAINTENANCE_USER, "Table maintenance", "mt"),
                                (OPERATOR_USER, "Platform operator", "op")]:
        if name not in known:  # Ranger's own sign-in for them is never used: they sign in to Trino
            ranger("POST", "/service/xusers/secure/users", json={
                "name": name, "firstName": first, "password": env["RANGER_PASSWORD"] + suffix,
                "userRoleList": ["ROLE_USER"], "status": 1, "userSource": 1})
    # Superset's policies are the trino stage's (superset.py): kept here, so
    # an upgrade, which runs both stages again, never drops them for a while.
    setup_ranger(POLICIES, keep=("superset:", "BI tools:"))
    log("done")
