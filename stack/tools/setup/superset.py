"""Configure the lakehouse for Superset, after the lakehouse's own setup:

  - Ranger: a user for Superset's service account, "superset", and the
    lakehouse's policies plus one: superset may run queries as anyone
    (impersonate). Each query then runs as the person in Superset, so Ranger
    applies that person's policies, masks and row filters.

Safe to run again.
"""
import importlib.util

from lakekit import allow, log, ranger, res, setup_ranger

# The lakehouse's policies (../lakehouse/tools/setup.py), so there's one list.
spec = importlib.util.spec_from_file_location("lakehouse_setup", "/tools/setup/lakehouse.py")
lakehouse = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lakehouse)

SERVICE_USER = "superset"
POLICIES = lakehouse.POLICIES + [
    {"name": "superset: queries as people", "resources": res(trinouser="*"),
     "policyItems": [allow(["impersonate"], users=[SERVICE_USER])]},
    # Tools read table metadata from Trino's system catalog: system.jdbc and
    # system.metadata (Trino's SQLAlchemy and JDBC drivers do; so does dbt, as
    # each project's pipelines; so does the catalog's user, openmetadata).
    # Trino filters those tables to what each one may see, so everyone may
    # read them (Ranger's group public); not system.runtime (queries, nodes).
    {"name": "BI tools: system catalog", "resources": res(catalog="system"),
     "policyItems": [allow(["use", "show"], groups=["public"], users=["openmetadata"])]},
    {"name": "BI tools: system metadata schemas", "resources": res(catalog="system", schema=["jdbc", "metadata"]),
     "policyItems": [allow(["use", "show"], groups=["public"], users=["openmetadata"])]},
    {"name": "BI tools: system metadata tables",
     "resources": res(catalog="system", schema=["jdbc", "metadata"], table="*", column="*"),
     "policyItems": [allow(["select"], groups=["public"], users=["openmetadata"])]},
]

if __name__ == "__main__":
    users = {u["name"] for u in ranger("GET", "/service/xusers/users?pageSize=1000")["vXUsers"]}
    if SERVICE_USER not in users:
        ranger("POST", "/service/xusers/secure/users", json={
            "name": SERVICE_USER, "firstName": "Superset", "password": "unused-Sup3rset",
            "userRoleList": ["ROLE_USER"], "status": 1, "userSource": 1})
    setup_ranger(POLICIES)
    log("done")
