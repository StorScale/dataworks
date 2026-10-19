"""The platform's connectors (use case 8.4): an import's dlt resources for
a named application, from its objects and its connection's sign-in. The
import runner (storscale_import_run.py) lands them as it does any source's.
"""
import os

import dlt

# Salesforce's objects copied by their changes (the others are replaced each
# run): the field their changes are stamped in, as dlt's source reads them.
SALESFORCE_CURSORS = {
    "account": "LastModifiedDate", "campaign_member": "SystemModstamp", "event": "SystemModstamp", "opportunity": "SystemModstamp",
    "opportunity_contact_role": "SystemModstamp", "opportunity_line_item": "SystemModstamp", "task": "SystemModstamp",
}


def salesforce_credentials(secret):
    """dlt's Salesforce credentials from the connection's keys (7.6's salesforce kind)."""
    from salesforce.helpers.client import ConsumerKeySecretAuth, InstanceAuth, OrganizationIdAuth, SecurityTokenAuth
    if secret.get("sessionId"):
        return InstanceAuth(session_id=secret["sessionId"], instance_url=secret.get("instanceUrl"))
    if secret.get("consumerKey"):
        return ConsumerKeySecretAuth(user_name=secret["username"], password=secret["password"],
                                     consumer_key=secret["consumerKey"], consumer_secret=secret.get("consumerSecret"))
    if secret.get("organizationId"):
        return OrganizationIdAuth(user_name=secret["username"], password=secret["password"], organization_id=secret["organizationId"])
    return SecurityTokenAuth(user_name=secret["username"], password=secret["password"], security_token=secret.get("securityToken", ""))


def trust(ca):
    """Trust a CA's certificate as well as the usual ones (a proxy that inspects TLS)."""
    if not ca:
        return
    import tempfile

    import certifi
    with open(certifi.where()) as f:
        bundle = f.read()
    fd, path = tempfile.mkstemp(prefix="ca-", suffix=".pem")
    with os.fdopen(fd, "w") as f:
        f.write(bundle + "\n" + ca.strip() + "\n")
    os.environ["REQUESTS_CA_BUNDLE"] = path


def salesforce(im, secret, name):
    """The import's Salesforce objects as dlt resources, each into <import>_<object>:
    those copied by their changes merged on their Id, from `since` if given."""
    from salesforce import salesforce_source
    sf = im["salesforce"]
    trust(secret.get("caCertificate"))
    if secret.get("domain"):
        os.environ["SOURCES__SALESFORCE__DOMAIN"] = secret["domain"]   # login, or test (a sandbox)
    source = salesforce_source(credentials=salesforce_credentials(secret)).with_resources(*sf["objects"])
    for obj in sf["objects"]:
        res = source.resources[obj]
        hints = {"table_name": f"{name}_{obj}"}
        if obj in SALESFORCE_CURSORS:
            hints["primary_key"] = "Id"   # as Salesforce names it (dlt makes the column id)
            if sf.get("since"):
                hints["incremental"] = dlt.sources.incremental(SALESFORCE_CURSORS[obj], initial_value=f"{sf['since']}T00:00:00.000Z")
        res.apply_hints(**hints)
    return [source]
