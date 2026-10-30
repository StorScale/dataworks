# 2. Projects

A project is a team's tables, files and pipelines, and who may do what with them. This chapter is for the platform's
administrators, who make and change projects, and for the engineers who need to know exactly what a project's spec says
and what the platform does with each part of it. It covers making a project on Administration → Projects (the form and its
JSON box) and through the API, its members and their roles, its tables, files and pipelines, where it's told of trouble,
its limits, changing and deleting it, and making a project from one of the company's templates.

In this chapter:

- [Making a project](#making-a-project)
- [The spec at a glance](#the-spec-at-a-glance)
- [Members and roles](#members-and-roles)
- [Service accounts: applications as members](#service-accounts-applications-as-members)
- [Tables](#tables)
- [Files](#files)
- [Pipelines](#pipelines)
- [Where the project is told of trouble (notify)](#where-the-project-is-told-of-trouble-notify)
- [Limits](#limits)
- [Changing a project, and its status](#changing-a-project-and-its-status)
- [Deleting a project](#deleting-a-project)
- [Project templates](#project-templates)
- [Shared libraries, on the same page](#shared-libraries-on-the-same-page)

## Making a project

### What it's for

A project gives a team a home for its data: a namespace of tables, a bucket of files, a service account for its pipelines,
and its members in their roles, all written down once. The platform applies it to Keycloak, Ranger, Buckets, Nessie and
Airflow and keeps them that way, so no one sets up access tool by tool.

### Who can do it

The platform's administrators (the `PLATFORM_ADMIN_GROUPS`, `engineers` by default). Anyone else who tries is refused with
"only administrators change projects". A project's editors change parts of it from its own tabs (its imports, connections,
agents, apps and more, each in its chapter), never the whole spec.

### Set up: on the page

1. Open **Administration** (the account menu, top right) → **Projects** → **New project**. (**From a template** beside it
   opens [Project templates](#project-templates).)
2. **Name**: lowercase letters, digits and dashes, 3 to 40 characters, starting with a letter: `retail`. The project's
   groups, bucket and namespace are named after it, and it can't be changed later.
3. **Description**: a line saying what it's for: "Harbor Outfitters: orders, customers, campaigns".
4. **Members**: a new project starts with the group `engineers` as editors. For each member pick **Keycloak group** or
   **Person**, type the group's or the person's username, and pick a role (editor, reader, or a role defined on
   Administration → Roles). **Add a member** adds a row; **Remove** takes one out. See [Members and roles](#members-and-roles).
5. **Tables** (ticked by default): the project's namespace, and what its readers may read. See [Tables](#tables).
6. **Files** (ticked by default): a bucket of its own. See [Files](#files).
7. **Pipelines**: tick it and give the Keycloak client whose service account runs them, `airflow-pipelines` on the example
   install. See [Pipelines](#pipelines).
8. **Alerts**: a Teams or Slack channel (the name of a connection of the project's), email addresses, and the tables that
   must change in time. See [Where the project is told of trouble](#where-the-project-is-told-of-trouble-notify).
9. The form also has **Imports**, **Sources** and **Table maintenance**: see [Getting data in](03-data-in.md) and
   [History and releases](15-history-and-releases.md).
10. Read **What each role will get**, below the form: a card per role (Editors, Readers, Pipelines) saying in words what
    each group gets, as the platform will apply it.
11. Press **Save**. The browser goes to the new project's Overview, which says **Waiting for the operator**, then
    **Applying**, then **Ready**, within a minute.

**Form** and **JSON** (top right) switch between the form and the whole project as JSON. The JSON box takes every field of
the spec, including those the form doesn't show (limits, checks, deliveries, products and the rest); what you type there is
what's saved. **Form** is greyed out while the JSON doesn't parse.

### Set up: the spec

The same project as JSON, as the JSON box shows it and the API takes it:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "description": "Harbor Outfitters: orders, customers, campaigns",
    "members": [
      {"group": "engineers", "role": "editor"},
      {"group": "analysts", "role": "reader"}
    ],
    "tables": {
      "readers": {
        "rowFilters": [{"table": "uploads_orders", "filter": "region = 'EU'"}]
      },
      "people": [
        {"table": "uploads_customers", "column": "customer_id"},
        {"table": "uploads_orders", "column": "customer_id"}
      ],
      "freshness": [{"table": "uploads_orders", "within": "24h"}]
    },
    "files": {},
    "pipelines": {"serviceAccount": "airflow-pipelines"},
    "notify": {"teams": "retail-alerts", "email": ["retail-data@example.com"]},
    "limits": {"queries": 4, "queued": 40, "queryMemory": "4GB", "storage": "500Gi"}
  }
}
```

`apiVersion` and `kind` may be left out (they're filled in); `metadata.name` and `spec.members` may not. An empty object
(`"tables": {}`, `"files": {}`) asks for the part with its defaults; leaving the key out means the project doesn't have it.

### Set up: the API

| Method and path | What it does | Who |
|---|---|---|
| `GET /api/projects` | The projects the caller may see (an administrator's, every one), each with its spec, status, the caller's role and permissions | anyone signed in |
| `GET /api/projects/{name}` | One of them | anyone signed in, for a project they may see |
| `PUT /api/projects/{name}` | Make or change a project: the body is the whole project, as above | administrators |
| `DELETE /api/projects/{name}` | Delete it (see [Deleting a project](#deleting-a-project)) | administrators |
| `GET /api/access` | Who has which role in which project | administrators |

A change needs the header `X-Platform-Request: 1`:

```bash
curl -X PUT "$PLATFORM/api/projects/retail" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" \
  -H "Content-Type: application/json" --data @retail.json
```

### How it behaves

- platformd reads the spec strictly (an unknown field is refused), fills in its defaults, checks every part at once, and
  answers with every problem it found, joined by "; ", so a spec is fixed in one go. On Kubernetes, projects are `Project`
  resources and Kubernetes checks them against its schema too; its refusal is passed on.
- A member given a role nobody defined is refused: "role steward: reader, editor, or a role defined on Administration →
  Roles (none is called that)".
- Saved, the project is in the project store, and the operator applies it at once (platformd asks it to; it also looks
  every few seconds). Each system it applies is a line under **Applied to** on the Overview: `keycloak` (the groups and who's
  in them), `ranger` (the groups, and the policies on the namespace, named `project:<name>:...`), `buckets` (the bucket, its
  size limit and its policies), `nessie` (the namespace), and others as the project uses them (`airflow`, `imports`,
  `trino`, `copy`, `llm`, `serving`, `deliveries`, `agents`, `reviews`, `roles`).
- The audit log records `project.saved` with who did it and what changed: "created", "changed: tables.readers,
  limits", or "saved, unchanged".

### Limits and gotchas

- **The name is fixed.** A project can't be renamed: make a new one. A `PUT` whose body names another project is refused:
  "the project's name must be retail".
- **The JSON is checked strictly.** A misspelt field is refused as `not a project: json: unknown field "membres"`.
- **"apiVersion and kind must be platform.storscale.io/v1alpha1 and Project"**: a body with other values there.
- **"a project needs at least one member"**.
- The form doesn't show limits, checks, deliveries, products, agents, apps or the rest: use the JSON box, or the tab each
  belongs to.

See [Lab 2](../guide.md#lab-2-a-project-for-the-retail-team) in the lab guide.

## The spec at a glance

Every field of `spec`, and where it's covered:

| Field | What it is | Chapter |
|---|---|---|
| `description` | A line saying what the project is for | here |
| `members` | Who's in it, with which role | [here](#members-and-roles), [Roles](16-roles.md) |
| `tables` | Its namespace of Iceberg tables | [here](#tables) |
| `tables.readers` | What readers may read: tables, row filters, masks | [Access and protection](04-access-and-protection.md) |
| `tables.people` | Tables of people's data, and the column identifying a person | [Access and protection](04-access-and-protection.md), [Privacy](13-privacy.md) |
| `tables.checks`, `tables.freshness` | Data-quality checks; how recently tables must change | [Data quality](05-quality.md) |
| `tables.maintenance` | The nightly upkeep (`paused`) | [History and releases](15-history-and-releases.md) |
| `tables.tags` | Moments of the tables kept | [History and releases](15-history-and-releases.md) |
| `tables.retention` | How long rows are kept | [Privacy](13-privacy.md) |
| `files` | Its bucket | [here](#files) |
| `pipelines` | Its service account, and its repo | [here](#pipelines), [Pipelines and git](09-pipelines-and-git.md) |
| `notify` | Where it's told of trouble | [here](#where-the-project-is-told-of-trouble-notify), [Data quality](05-quality.md) |
| `limits` | Its share of Trino and its bucket's size | [here](#limits), [Operating](17-operating.md) |
| `sources`, `imports` | Databases queried in place; data copied in | [Getting data in](03-data-in.md) |
| `deliveries` | Files to partners | [Deliveries](12-deliveries.md) |
| `semantic` | Its model shared, or built on others' | [Metrics and business terms](06-metrics-and-terms.md) |
| `products` | Its data products | [Data products](11-data-products.md) |
| `llm`, `agents`, `knowledge`, `tools` | The gateway budget, agents, knowledge banks, its tools | [AI](14-ai.md) |
| `apps` | Its apps | [Dashboards, apps and assistants](07-dashboards-apps-assistants.md) |
| `scoring`, `serving` | Models scoring tables, and answering as an API | [Notebooks and models](10-notebooks-and-models.md) |
| `cost` | Its monthly budget | [Operating the platform](17-operating.md) |

## Members and roles

### What it's for

Members say who may do what in the project. Name groups wherever you can: someone who joins the group in Keycloak joins
the project, and someone who leaves it leaves.

### Who can do it

Administrators, on the Edit page or through `PUT /api/projects/{name}`.

### Set up

Each member is a Keycloak group or one person, never both, with a role:

```json
"members": [
  {"group": "engineers", "role": "editor"},
  {"group": "analysts", "role": "reader"},
  {"user": "alice", "role": "editor"}
]
```

| Field | Value |
|---|---|
| `group` | A Keycloak group's name (letters, digits, `.`, `_`, `@`, `-`; up to 100 characters) |
| `user` | A Keycloak username |
| `role` | `editor`, `reader`, or a project role defined on Administration → Roles (see [Roles](16-roles.md)) |

On the Edit page: **Members**, a row each (**Keycloak group** or **Person**, the name, the role), **Add a member**,
**Remove**. The Access tab's **Change members** (administrators) opens the same page.

### How it behaves

- The operator finds each member in Keycloak (every person in a group) and puts each person in one Keycloak group of the
  project's, by role: `<project>-editors`, `<project>-readers`, or `<project>-role-<role>`. The same groups are in Ranger
  (its policies name them) and in Buckets (a policy per group).
- **One role each: the one that gives the most.** Someone who's a member twice (alice as a reader through `analysts`, and as
  an editor by name) has the editor's role. An editor's role is above every other; a reader's below every other; between
  them, the role that gives more permissions wins. So the readers' row filters and masks never hold someone another role
  lifts them from.
- The project's status lists each person with their role, the group they came through ("via analysts") or "named", and
  what their role lets them do. The Overview's **People** card and the **Access** tab show it; Administration → **Access**
  shows every person across every project.
- Keycloak's groups are read again each minute: someone added to `analysts` is a reader of every project that names
  `analysts`, within a minute, with nothing saved.

### Limits and gotchas

- **"member 2: give a group or a user, not both"**: a member with both, or neither.
- **"no Keycloak group "retail-team""** under **Applied to** (`keycloak`), phase Error: the group doesn't exist in Keycloak.
  Make it there, or fix the name; the operator tries again within seconds.
- **"no Keycloak user "dana""**: the same, for a person.
- **"no role steward: an administrator defines it (Administration → Roles); its members have no access meanwhile"**: a
  role that was defined when the project was saved and has since gone. Define it again, or change the members.
- `pipelines`, `agent`, `serving`, `admin`, `editors` and `readers` aren't roles a member can be given: they're the
  platform's own.

## Service accounts: applications as members

**What it's for.** An application that works with the project as a member would, signed in as itself rather than as a person: a
CRM that keeps the project's sales metrics (its semantic model) and its import, a company system that reads its tables or queries
its metrics on a schedule. A service account is that application's identity in the project: a member in a role you choose, here
and nowhere else, with a secret of its own that can be rotated and revoked, and every call audited under its name.

**Who can do it.** The project's editors register and revoke them. An **administrator other than the one who registered it**
approves each and rotates its secret: a service account may be an editor, and only administrators make people members.

### Set up

**On the page.** The project's **Access** tab → **Service accounts** → **Register one**: a name (lowercase letters, digits and
dashes), its role (reader, editor, or one of the company's roles), and what it's for. It waits for approval. An administrator
presses **Approve**: the page shows its client and secret **once**; give them to the application. **New secret** makes another
(the old one stops working within a minute); **Revoke** takes it away at once.

**Through the API:**

| Method and path | Who | What it does |
|---|---|---|
| `GET /api/projects/{name}/serviceaccounts` | members | The service accounts, each with its client, the name its tokens carry, and whether it's approved |
| `PUT /api/projects/{name}/serviceaccounts/{account}` | editors | Registers one, or changes it: `{"role": "editor", "description": "...", "owner": "alice"}`. A changed role or owner needs approval again |
| `POST /api/projects/{name}/serviceaccounts/{account}/approve` | an administrator, not its registrant | Approves it: the answer has `clientId` and `secret` (once) and `tokenUrl` |
| `POST /api/projects/{name}/serviceaccounts/{account}/rotate` | an administrator | A new secret, shown once |
| `DELETE /api/projects/{name}/serviceaccounts/{account}` | editors | Revokes it |

**The application signs in** with OAuth's client credentials, and calls the API with the token, as any member does:

```bash
TOKEN=$(curl -s https://<platform>/sso/realms/lakehouse/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=svc-crm-provisioner -d client_secret=$SECRET | jq -r .access_token)
curl -s -X PUT https://<platform>/api/projects/crm/semantic -H "Authorization: Bearer $TOKEN" \
  -H "X-Platform-Request: 1" -H "Content-Type: application/json" -d @model.json
```

In Python: `storscale.Platform(url, client_id="svc-crm-provisioner", client_secret=secret)`.

### How it behaves

- Its client is `svc-<project>-<name>` (confidential, client credentials only), made by the operator within a minute of approval;
  its tokens name it `service-account-svc-<project>-<name>`, which is how the project's **Members** list, the audit log and
  Ranger's audit show it.
- It's a member of the project in its role: what that role may do on the pages' API, read in Trino (Ranger) and change, it may;
  nothing in any other project, and never an administrator's tools.
- Its secret is kept as the project's connection `<project>-svc-<name>`, which only the platform reads.
- The platform's audit log records `serviceaccount.registered`, `.approved`, `.rotated` and `.revoked`, and refusals (approving
  one's own, approving without being an administrator).

### Limits and gotchas

- "a service account is approved by an administrator other than the one who registered it"; "… only administrators make: an
  administrator approves it" (an editor approving).
- A service account doesn't register service accounts.
- Tokens it already has last until they expire (minutes) after a revocation or a rotation; new ones are refused.
- A role nobody defined gives it nothing, as for people: the project's status says so.

## Tables

### What it's for

`tables` gives the project a namespace of Iceberg tables in the lakehouse, which its people query through Trino and its
pipelines write, under Ranger's policies.

### Who can do it

Administrators turn it on. Editors (and roles with `write-tables`) then make, change and drop tables in it.

### Set up

On the Edit page, tick **Tables**. **Namespace** may be left empty for the default. The readers' rules (**Tables readers
may read**, **Row filters for readers**, **Column masks for readers**) are in [Access and protection](04-access-and-protection.md).

```json
"tables": {
  "namespace": "retail",
  "readers": {"tables": ["uploads_orders", "uploads_customers"]},
  "maintenance": {"paused": false}
}
```

| Field | Default | What it is |
|---|---|---|
| `catalog` | `iceberg` | Trino's catalog, on Nessie |
| `namespace` | the project's name, dashes as underscores (`sales-eu` → `sales_eu`) | The schema in that catalog |
| `readers` | every table, as it is | What readers see: see [Access and protection](04-access-and-protection.md) |
| `checks`, `freshness` | none | See [Data quality](05-quality.md) |
| `maintenance` | on | `{"paused": true}` stops the nightly compaction. See [History and releases](15-history-and-releases.md) |
| `tags`, `people`, `retention` | none | See [History and releases](15-history-and-releases.md), [Access and protection](04-access-and-protection.md), [Privacy](13-privacy.md) |

### How it behaves

- The operator makes the namespace in Nessie and keeps Ranger's policies on it: editors (and roles with `write-tables`)
  may do anything in it; readers read the tables the project shows readers, under its row filters and masks; the
  pipelines' service account reads, inserts, deletes, creates, drops and alters tables in it (dbt's models, imports,
  flows), and nothing outside it. A table's past (`orders@close-2026-09`, `orders#<commit>`) is under the same rules.
- Tables are made by people (`CREATE TABLE iceberg.retail.notes ...` on the SQL tab), by imports and uploads
  ([Getting data in](03-data-in.md)), by flows and dbt.
- The catalog (OpenMetadata) finds the project's tables within a minute or so; the Overview says "The catalog hasn't seen
  this project's tables yet. It looks every minute." until it has.

### Limits and gotchas

- **"tables: catalog "x" and namespace "Sales" must be lowercase identifiers"**: letters, digits and underscores,
  starting with a letter or underscore.
- Imports, sources' copies, deliveries and knowledge banks need `tables`: without it they're refused ("its tables are the
  project's, and it has none (tables)").

## Files

### What it's for

`files` gives the project a bucket of its own in Buckets: uploads land there, pipelines read and write its `landing/`
folder, and notebooks read it with the person's own credentials.

### Who can do it

Administrators turn it on. Who may read and write it follows the roles: editors read and write; readers read.

### Set up

Tick **Files** on the Edit page; **Bucket** may be left empty for the default.

```json
"files": {"bucket": "retail"}
```

| Field | Default | Rule |
|---|---|---|
| `bucket` | the project's name | 3 to 40 lowercase letters, digits and dashes |

### How it behaves

- The operator makes the bucket, sets its size limit (the project's `limits.storage`, else the install's
  `limits.project.storage`, else none) and keeps a Buckets policy for each of the project's groups: those whose role has
  `write-files` (editors) may read, write and delete; those with `read-files` (readers) may list and read.
- The pipelines' service account may read and write under `landing/` only. Uploads go to `landing/uploads/`, which a
  `files` import reads ([Getting data in](03-data-in.md)).
- At 80% of its size limit the project is alerted ("the bucket retail is 80% full (...)"), and again when it's full
  ("the bucket retail is full (...): new files are refused"): Buckets refuses new files once it's full.

### Limits and gotchas

- **"files: bucket "Retail": 3 to 40 lowercase letters, digits and dashes"**.
- Give each project a bucket of its own: a bucket's policies are its project's groups', and the operator doesn't check
  that another project names the same one.

## Pipelines

### What it's for

`pipelines` gives the project a service account that runs its pipelines: its imports, deliveries, scorings, scheduled
notebooks and agents, its dbt models and its own DAGs. Whoever starts a run, it runs as this account, not as the person.

### Who can do it

Administrators turn it on. Starting and clearing runs takes `run-pipelines` (editors).

### Set up

Tick **Pipelines** on the Edit page and fill in **Keycloak client whose service account runs them**: `airflow-pipelines`,
the example realm's.

```json
"pipelines": {"serviceAccount": "airflow-pipelines"}
```

| Field | What it is |
|---|---|
| `serviceAccount` | The Keycloak client (with a service account) that runs them. Required. |
| `repo`, `path`, `ref`, `credentials`, `memory`, `reviews` | The project's own git repo, its Airflow and merge-request reviews: see [Pipelines and the project's repo](09-pipelines-and-git.md) |

**An import gives a project pipelines if it has none.** An import added on the **Imports** tab (or with
`PUT /api/projects/{name}/imports/{import}`) runs as the project's pipelines, so when the project has no `pipelines`, the
platform adds `{"serviceAccount": "airflow-pipelines"}` as it saves the import, and says so in the audit log ("the project
runs pipelines now (airflow-pipelines), as imports need"). See [Getting data in](03-data-in.md).

### How it behaves

- The operator puts the client's service account in the project's group `<project>-pipelines`, which Ranger and Buckets
  name: it reads and writes the namespace's tables and `landing/` in the bucket.
- Without a repo, the project's pipelines are the platform's (imports, deliveries, and so on) in the shared Airflow. With
  a repo, its own DAGs come from it; on Kubernetes the project then gets an Airflow of its own. See
  [Pipelines and the project's repo](09-pipelines-and-git.md).
- The **Pipelines** tab appears.

### Limits and gotchas

- **"Keycloak client "x" has no service account"** under **Applied to**: the client doesn't exist, or has no service
  account. Make it in Keycloak, or name `airflow-pipelines`.
- **"pipelines: path, ref, credentials, memory and reviews need a repo"**: those fields without `repo`.
- Taking `pipelines` away removes the pipelines' group: imports and deliveries then can't run.

## Where the project is told of trouble (notify)

### What it's for

The project's people hear of a failed pipeline run, a failing check or a table not changed in time where they work: a
Teams or Slack channel, or email. What the alerts are and how to mute them is in [Data quality](05-quality.md).

### Who can do it

Administrators set where (the Edit page). Editors give the channel's webhook as a connection (Connections tab, which takes
`connections`).

### Set up

1. On the project's **Connections** tab, add a connection of kind **A Teams channel** (key: the Workflows webhook URL) or
   **A Slack channel** (key: the incoming webhook URL). Its name starts with the project's and a dash: `retail-alerts`.
2. On the Edit page, under **Alerts**: **Teams channel** (the connection's name), **Slack channel** (the connection's
   name), **Email** (comma-separated addresses).

```json
"notify": {
  "teams": "retail-alerts",
  "slack": "retail-alerts-slack",
  "email": ["retail-data@example.com", "bob@example.com"]
}
```

### How it behaves

The platform looks at each project's issues every minute and tells the channels when an issue starts, and when a check or
a table comes right. Email goes through the install's mail server, as set for the platform's own alerts (the Secret
`storscale-alerts`; see [Operating the platform](17-operating.md)). The Overview's **Alerts** card says where the project
is told, or "Not told anywhere yet: the platform's administrators set where, when they edit the project."

### Limits and gotchas

- **"notify: teams "Alerts" isn't a connection's name"**; **"notify: "bob" isn't an email address"**.
- A `teams` or `slack` that names a connection the project doesn't have isn't refused when saved; the project just isn't
  told there. Add the connection.
- No mail server set for the install: email isn't sent.
- The Overview's **Alerts** card names only a Teams channel and email: a project told only in Slack reads "Not told
  anywhere yet", though its Slack channel is told.

## Limits

### What it's for

Limits cap the project's share of Trino and the size of its bucket, so one project can't slow or fill the platform for the
others.

### Who can do it

Administrators, in the JSON box (the form has no fields for them) or through the API.

### Set up

```json
"limits": {"queries": 8, "queued": 80, "queryMemory": "30%", "storage": "2Ti"}
```

| Field | Meaning | Rule | Install default (`limits.project.*`) |
|---|---|---|---|
| `queries` | Trino queries running at once | 1 to 100 | `4` |
| `queued` | Queries waiting; more are refused | up to 1000 | `40` |
| `queryMemory` | Trino memory for its queries | `4GB`, `512MB`, or a share of Trino's: `50%` | `50%` |
| `storage` | Its bucket's size | `500Gi`, `2Ti` (needs `files`) | none |

A field left out takes the install's default.

### How it behaves

The operator keeps a Trino resource group for each project with these limits. A query counts against the project when a
member names the project (the SQL page and metrics tag their queries `project:<name>`) and when the project's pipelines
run it; the rest count against the person. Notebooks are a person's, with a person's limits. More in
[Operating the platform](17-operating.md).

### Settings

`limits.project.queries`, `queued`, `queryMemory` and `storage` in the chart (Compose: the operator's
`LIMITS_PROJECT_QUERIES`, `LIMITS_PROJECT_QUEUED`, `LIMITS_PROJECT_QUERY_MEMORY`, `LIMITS_PROJECT_STORAGE`, with the same
defaults when unset).

### Limits and gotchas

- **"limits: queries 200: from 1 to 100 at once"**, **"limits: queryMemory "4G": as 4GB, 512MB or 50%"**, **"limits: storage
  "500GB": as 500Gi or 2Ti"**, **"limits: storage needs files (a bucket)"**.
- The storage alert's own words say to raise the limit with "Edit, Limits": that's the JSON box.

## Changing a project, and its status

### Set up

On the project's **Overview**, an administrator's **Administration** card has **Edit project**; the Access tab has **Change
members**. Both open `/p/<project>/edit`: the same form and JSON box, the name fixed. **Save** sends the whole project
(`PUT /api/projects/{name}`).

### How it behaves

- A save replaces the spec. What only the platform sets is kept as it was, whatever the body says: a delivery's approval,
  a data product's consumers and certification, an agent's approval.
- The operator applies the change within seconds. It also goes over every project again each minute, and tries a failed one
  again after 10 seconds.
- The project's phase: **Waiting for the operator** (never applied yet), **Applying**, **Ready**, **Error** (a part failed:
  its line under **Applied to** says why), **Deleting**. **Refresh status** (on the Administration card) reloads it.

### Limits and gotchas

- A project in **Error** still has what applied: a failed part is tried again, and the rest stay as they were.
- A change to a role a project gives (on Administration → Roles) applies the project again too, without its spec changing.
  See [Roles](16-roles.md).

## Deleting a project

### What it's for

A team or a trial that's finished: its access and credentials go, its data stays for someone to remove on purpose.

### Who can do it

Administrators.

### Set up

On the project's Overview, the **Administration** card: **Delete**, then, at "Delete retail? Its access and the
credentials it keeps go; its data stays.", **Delete** again. Or:

```bash
curl -X DELETE "$PLATFORM/api/projects/retail" -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1"
```

It answers 204.

### How it behaves

The project's phase is **Deleting** while the operator undoes it:

| Goes | Stays |
|---|---|
| Its Keycloak groups, Ranger groups and policies, Buckets policies | Its tables (the namespace in Nessie) |
| Its sources' Trino catalogs and their copies' pods | Its bucket and its files |
| Its imports' and deliveries' Airflow connections | The copies' tables |
| Its gateway keys, its agents' and served models' clients | |
| Its merge requests' data branches, its own Airflow (with its run history) | |
| Its Nessie tags (the history they kept goes after the catalog's next collection) | |
| Its connections: every credential it kept | |
| Its MLflow workspace: experiments, runs, models and their artifacts | |

The audit log records `project.deleted`.

### Limits and gotchas

- **"no such project"** (404): the name is wrong, or it's gone.
- The tables and bucket keep their names: a project made again with the same name has the same namespace and bucket, with
  what was left in them.

## Project templates

### What it's for

A new team asks for a project. An administrator makes it from one of the company's templates, so it works on day one:
the company's checks, its limits, where it's told of trouble, its pipelines' settings, and a copy of the template's repo
(a sample pipeline, a dbt model) as the team's own, in the company's GitLab. The team changes what it needs from there.

### Who can do it

- Anyone signed in sees the templates (what a project would start with) on Administration → Templates and
  `GET /api/templates`.
- Administrators define, change and remove templates, and make projects from them. Anyone else is refused: "only
  administrators change projects".

### Set up: the install's GitLab

A template's repo, and the project's copy of it, live in the install's GitLab. Without one, templates are a spec only (no
repo), and there are no libraries.

On Kubernetes, put a token in a Secret of the release's namespace (key `token`) and give the chart its GitLab:

```bash
kubectl -n storscale create secret generic storscale-gitlab --from-literal=token=<token>
storscale upgrade --context <cluster> --set templates.gitlab.url=https://gitlab.example.com \
  --set templates.gitlab.group=data/teams --set templates.gitlab.tokenSecret=storscale-gitlab
```

| Chart value | Compose (`stack/.env`) | What it is |
|---|---|---|
| `templates.gitlab.url` | `GITLAB_URL` | GitLab's address, `https://gitlab.example.com`. Empty: templates without repos, no libraries |
| `templates.gitlab.group` | `GITLAB_GROUP` | The group new projects' repos are made in, by its full path: `data/teams` |
| `templates.gitlab.tokenSecret` | `GITLAB_TOKEN` (the token itself) | The Secret holding the token (key `token`) |

The token (a group's or a service account's, scope `api`) must be allowed to make projects in the group and to read the
templates' and libraries' repos. On Kubernetes platformd reads it from the Secret at each use, so a new token needs no
restart. Compose's `stack/compose.yaml` defaults these to the `templates` test suite's stand-in GitLab
(`http://test:9480`, group `teams`), which answers only while that suite runs: set all three in `stack/.env` for a real one.

The Templates page says where copies go ("... as its own, in https://gitlab.example.com/data/teams"), or "the install's
GitLab (none is set: templates.gitlab)".

### Set up: defining a template

1. Make the template's repo in GitLab: a folder of DAGs (`dags/`), a dbt project, a README. Write `{project}` wherever the
   new project's name belongs (a DAG's `dbt.build(project="{project}", ...)`, a dbt profile, the README's title).
2. Open **Administration** → **Templates** → **Define a template**.
3. **Name**: lowercase letters, digits and dashes (`starter`); fixed once defined.
4. **What it's for**: a line; required.
5. **Its repo in GitLab**: the repo's path, `data/templates/starter`. Empty: no repo.
6. **The project's spec**: the `spec` part of a project, as the Edit page's JSON box shows it, without `members` and without
   `pipelines.repo` (those come when a project is made). It starts with a sample:

   ```json
   {
     "description": "{project}: a new team's",
     "tables": {},
     "files": {},
     "pipelines": {"serviceAccount": "airflow-pipelines", "path": "dags"},
     "limits": {"storage": "100Gi"}
   }
   ```

7. Press **Define it**. The card lists what the template gives: "A copy of the repo data/templates/starter, its pipelines
   in dags/", "Tables, 1 freshness targets, 1 checks", "A bucket of its own", "Limits: storage 50Gi", "Told of trouble at data-team@example.com".

**Change** opens the same form (the name fixed); **Remove** asks "Remove the template starter? Projects made from it stay as
they are."

A complete template, through the API (`PUT /api/templates/starter`, with `X-Platform-Request: 1`):

```json
{
  "description": "A new team's project: a sample pipeline and dbt model, the company's check, where it's told of trouble",
  "repo": "data/templates/starter",
  "spec": {
    "description": "{project}: made from the starter template",
    "tables": {
      "checks": [{"table": "orders", "column": "order_id", "check": "unique"}],
      "freshness": [{"table": "orders", "within": "24h"}]
    },
    "files": {},
    "pipelines": {"serviceAccount": "airflow-pipelines", "path": "dags", "memory": "2Gi", "reviews": {"dbt": "dags/dbt"}},
    "notify": {"email": ["data-team@example.com"]},
    "limits": {"storage": "50Gi"}
  }
}
```

| Field | What it is |
|---|---|
| `description` | What the template is for. Required. |
| `repo` | Its repo in the install's GitLab, `group/repo`. Optional. |
| `spec` | A project's spec, without `members` or `pipelines.repo`. `{project}` anywhere in it becomes the project's name. |

The name comes from the path; `changedBy` and `changedAt` are the platform's.

### Set up: making a project from a template

1. On **Administration** → **Templates** (or Administration → Projects → **From a template**), on the template's card,
   press **Make a project**.
2. **Name**: the project's name, `growth`.
3. **Editors**: Keycloak groups, or people as `@name`, comma-separated: `growth-team, @dana`. At least one.
4. **Readers**: the same, optional: `analysts`.
5. **Told of trouble at**: email addresses, comma-separated. Empty: the template's.
6. Press **Make it**. With a repo it says "Copying its repo…" (it can take up to two minutes); then the browser opens the
   new project.

Through the API, `POST /api/templates/{template}/projects` (administrators, with `X-Platform-Request: 1`):

```json
{
  "name": "growth",
  "description": "The growth team's campaigns and experiments",
  "members": [
    {"group": "growth-team", "role": "editor"},
    {"user": "dana", "role": "editor"},
    {"group": "analysts", "role": "reader"}
  ],
  "notify": {"email": ["growth-team@example.com"]}
}
```

| Field | What it is |
|---|---|
| `name` | The project's name. Required. |
| `members` | As a project's (any role, including the company's own). Required. |
| `description` | Replaces the template's, when given (the page doesn't send one: the template's stands). |
| `notify` | Replaces the template's `notify` whole, when given. |

It answers 201 with `{"name": ..., "spec": <the project>, "repo": <the new repo's web address>}`.

### How it behaves

Making a project from a template, in order:

1. **Checked first.** The project is made from the template on paper (with a stand-in repo) and checked as any project is,
   and the name is checked to be free, so nothing is made for a project that can't be.
2. **The repo copied** (a template with a repo). The template's repo is copied into the install's group as a repo of the
   project's own, named as the project (`data/teams/growth`), described "The growth project's pipelines, from the starter
   template". The platform waits for GitLab to finish copying (at most two minutes), then unlinks the copy from the
   template's: it's the team's, not a fork.
3. **`{project}` filled in.** In every text file of the copy's default branch, `{project}` becomes the project's name, in
   one commit: "The growth project's, from the starter template". A file without `{project}` is left as it is.
4. **A token of the repo's own.** GitLab makes a project access token for the new repo: named `storscale-growth`, the
   Developer role, scopes `api` and `read_repository`, valid for a year (GitLab's most). The platform keeps it as the
   project's connection **`growth-repo`** (kind `git`: user `storscale-growth`, the token as its password), which is never
   shown again. The template's token, and the install's, are never given to the project.
5. **The spec.** The template's spec, with `{project}` replaced by the name, the members (and description and notify, when
   given), and, with a repo, its pipelines naming the copy: `pipelines.repo` the copy's https address,
   `pipelines.credentials.secretRef` `growth-repo`. A template with a repo but no `pipelines` gets
   `{"serviceAccount": "airflow-pipelines"}`. A template whose pipelines have `reviews` gets them pointed at the install's
   GitLab and the copy's path, so merge requests on the new repo are reviewed on data branches from day one.
6. **Saved and applied.** The project is saved and the operator applies it at once. On Kubernetes, a project with a repo
   gets an Airflow of its own, which clones the copy with its own token; in Compose, the repo is a DAG bundle in the shared
   Airflow. See [Pipelines and the project's repo](09-pipelines-and-git.md).

If any step fails, what was made is taken back: the connection removed, the copied repo deleted.

The project is the team's from then on. Changing or removing the template changes no project made from it; each keeps
what it was made with.

The audit log records `template.saved` ("added" or "changed", and the repo), `template.removed`, and, for the project,
`project.saved` with "from the template starter; its repo data/teams/growth (a copy of data/templates/starter)".

### Settings

| Chart value | Compose | Default |
|---|---|---|
| `templates.gitlab.url` | `GITLAB_URL` | `""` (chart); the test suite's stand-in (Compose) |
| `templates.gitlab.group` | `GITLAB_GROUP` | `""` (chart); `teams` (Compose) |
| `templates.gitlab.tokenSecret` | `GITLAB_TOKEN` | `""` (chart); the stand-in's token (Compose) |

Templates and libraries are kept in the platform's bucket (`templates/templates.json`, `templates/libraries.json`); without
the platform's bucket (`STORE_URL`), they can't be changed: "the platform keeps no files here (no STORE_URL)".

### Limits and gotchas

What's refused when defining a template (422 unless said):

| Message | Why |
|---|---|
| "a template's name: lower-case letters, digits and dashes" | The name |
| "say what the template is for (its description)" | No description |
| "repo: the template repo's path in GitLab, as group/repo" | `repo` isn't a path |
| "a template has no members: they're given when a project is made from it" | `spec.members` given |
| "pipelines.repo: a project made from the template gets its repo (a copy of the template's); leave it out" | `spec.pipelines.repo` given |
| "a project made from it: ..." | The spec as a project would have it is wrong; the rest of the message says what, as for any project |
| "the install has no GitLab for templates' repos (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN): leave repo out" | A repo, and no GitLab |
| "the template's repo data/templates/starter: ..." | GitLab can't find the repo with the install's token |
| "a template, as JSON: its description, repo and spec: ..." (400) | The body isn't JSON, or has a field a template doesn't |

- **A template without a repo can't set the repo's settings.** `pipelines.path`, `ref`, `memory` and `reviews` need a repo,
  so the form's sample spec (which has `"path": "dags"`) is refused without one: "a project made from it: pipelines: path,
  ref, credentials, memory and reviews need a repo". Take `path` out, or give the template a repo.
- **The page's spec box isn't checked as JSON until you save:** "The spec isn't JSON: ...".

What's refused when making a project:

| Message | Status | Why |
|---|---|---|
| "no template starter" | 404 | The name |
| "there's a project growth already" | 409 | The name is taken |
| (a project's own problems, as for any project) | 422 | The name, the members, or the template's spec with them |
| "the install has no GitLab for templates' repos (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN)" | 503 | The template has a repo, the install no GitLab |
| "copying the template's repo: ..." | 502 | GitLab refused the copy: a repo of that name in the group already, or the token may not make projects there |
| "the project's repo: gitlab: the template's repo wasn't copied within 2m0s (still ...)" | 502 | GitLab was slow; nothing is left behind: try again |
| "the project's repo: ..." | 502 | Filling in `{project}`, or the token, failed (GitLab's own words follow) |
| "a connection growth-repo is another's: remove it first" | 502 | A Secret of that name exists that isn't the project's |

- **The repo token lasts a year.** Renew it before then: make a new project access token on the repo in GitLab and paste it
  as the password of the connection `<project>-repo` on the project's Connections tab.
- **`{project}` is filled in once,** when the project is made, in files of the default branch only (the first thousand
  entries of the repo's tree). Later changes to the template's repo don't reach the copy.
- **On Compose with the defaults,** `GITLAB_URL` is the test suite's stand-in: a template with a repo fails unless that
  suite is running. Set a real GitLab in `stack/.env`.

## Shared libraries, on the same page

Below the templates, **Shared libraries** lists the company's code that projects use without copying: a repo in the
install's GitLab at a ref (a tag is best: it doesn't move). The platform reads it with the install's token for whoever the
realm signed in, so no project holds a token for it. In a dbt project's `packages.yml`, name it by its address (the table's
**In packages.yml** column, `- git: "https://gitlab.example.com/data/libraries/company-dbt.git"`); in a pipeline,
`storscale.library.use("company-py")`; in a notebook, `buckets_lake.library("company-py")`.

Administrators **Add a library** (name, what it's for, its repo in GitLab, ref), **Change** it (a new ref reaches every
project at its next run) or **Remove** it ("Projects naming it can't fetch it any more."). The ref is checked to be in the
repo when it's saved.

| Method and path | What it does | Who |
|---|---|---|
| `GET /api/templates` | The templates and libraries, whether the caller may change them, and the install's GitLab and group | anyone signed in |
| `PUT /api/templates/{template}` | Define or change a template | administrators |
| `DELETE /api/templates/{template}` | Remove it | administrators |
| `POST /api/templates/{template}/projects` | Make a project from it | administrators |
| `GET /api/libraries` | The libraries, each with its repo's address | anyone the realm signed in (a person, a project's pipelines, an agent) |
| `GET /api/libraries/{library}` | A library, with the commit its ref names now | the same |
| `GET /api/libraries/{library}/archive` | Its files at a commit (default its ref's), as `.tar.gz` | the same |
| `PUT /api/libraries/{library}` | Define one, or move it to another ref: `{"description", "repo", "ref"}` | administrators |
| `DELETE /api/libraries/{library}` | Remove it | administrators |

How pipelines, dbt and notebooks use libraries is in [Pipelines and the project's repo](09-pipelines-and-git.md).
