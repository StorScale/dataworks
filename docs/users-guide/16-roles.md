# 16. Roles

Roles say what people may do. This chapter is for the platform's administrators, who define roles, and for anyone who
needs to know exactly what a role gives in Trino, Buckets, Airflow and the platform's own pages. It covers the two built-in
project roles (editor and reader) as the platform's permissions, defining roles of the company's own from those
permissions, giving them in a project, the platform roles given to Keycloak groups across every project, and what a change
to a role reaches, and when.

In this chapter:

- [Two kinds of role](#two-kinds-of-role)
- [The built-in project roles](#the-built-in-project-roles)
- [The project permissions](#the-project-permissions)
- [Defining a project role](#defining-a-project-role)
- [Giving a project role](#giving-a-project-role)
- [How a project role is applied](#how-a-project-role-is-applied)
- [Reading tables: readers' rules, read-all and editors](#reading-tables-readers-rules-read-all-and-editors)
- [Platform roles](#platform-roles)
- [Changing and removing a role](#changing-and-removing-a-role)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

## Two kinds of role

| | Project role | Platform role |
|---|---|---|
| Given | by a project, to its members (people and Keycloak groups), in its spec | on Administration → Roles, to Keycloak groups |
| Reaches | that project only | every project, and the pages across them |
| Built in | `editor`, `reader` | `finance`, `governance`, `data-protection` (from the install's settings) |
| Made of | the 17 project permissions | the 4 platform permissions |
| Defined by the company | yes: **Define a project role** | yes: **Define a platform role** |

An administrator (in `PLATFORM_ADMIN_GROUPS`, `engineers` by default) has every permission of both kinds, in every
project, on the platform's pages and API. What an administrator reads in Trino is still what their groups are given there.

## The built-in project roles

### What they're for

Most teams need two roles: those who build (editors) and those who read (readers). The platform defines both as sets of
its permissions, so a role of the company's own is made of the same parts.

| Role | Permissions | In short |
|---|---|---|
| `editor` | all 17 | Makes and changes the project's tables, files, pipelines, metrics and models |
| `reader` | `read`, `read-files`, `call-models` | Reads what the project shows readers, and calls its models |

The project's pipelines (`pipelines`), its approved agents (`agent`) and its served models' accounts (`serving`) are
members of the platform's own, never given by hand: the pipelines read and write the tables and the bucket's `landing/`;
agents and servings read as readers do.

Administration → Roles lists editor and reader as cards marked **built in**, with what each gives in words. They can't be
changed or removed.

## The project permissions

Seventeen, in the order the Roles page lists them. Some imply others: a role with `write-tables` reads every table as it
is (`read-all`), and so reads (`read`); a role with `write-files` reads the files. The Roles page marks an implied
permission "(comes with another)".

| Permission | The Roles page says | Where it holds |
|---|---|---|
| `read` | Read the tables the project shows readers, as readers see them (its row filters and masks) | Trino (Ranger): the tables in `tables.readers.tables` (or all), under the readers' row filters and masks and the tags' masks; a source with `readers: true` |
| `read-all` | Read every table of the project as it is: no readers' row filters or masks, and tagged columns unmasked | Trino (Ranger): every table, unfiltered and unmasked; every source; the data branches of its merge requests |
| `write-tables` | Make, change and drop the project's tables | Trino (Ranger): everything in the namespace; publishing its data products |
| `read-files` | Read the project's files (its bucket) | Buckets: list and read the bucket |
| `write-files` | Write the project's files, and upload to its landing area | Buckets: read, write, delete; uploads to the landing area (the Flows canvas's uploads) |
| `run-pipelines` | Start and clear the project's pipelines' runs, and its tables' maintenance | The Pipelines tab's runs; asking for table maintenance; the project's own Airflow as `Op` (else `Viewer`) |
| `change-metrics` | Change the project's metrics (its semantic model) and run their tests | semanticd: saving the semantic model |
| `build` | Build the project's flows, apps, knowledge banks and agents, and schedule notebooks | Flows, apps, knowledge banks, agents (registering, revoking), scheduled notebooks, calling the project's tools, asking for and answering requests for data products |
| `models` | Track runs and register models in the project's MLflow | MLflow: writing (reading is every member's); scorings' references and runs |
| `call-models` | Call the project's served models | (see [Limits and gotchas](#limits-and-gotchas)) |
| `approve` | Approve agents, model versions and deliveries (never one's own) | Approving agents, model versions, deliveries |
| `classify` | Confirm or remove the catalog's tags on the project's columns | The Data page's classification |
| `connections` | Give and change the project's credentials (its connections) | The Connections tab; the Imports tab |
| `release` | Plan and apply releases, make tags, and restore from history | The Releases and Tags tabs |
| `see-usage` | See the project's usage and cost | The Usage tab |
| `see-audit` | See the project's audit log | The Audit tab |
| `mute-alerts` | Mute the project's alerts for a while | **Mute** on the Overview's Alerts |

A page or a route that needs a permission the person lacks refuses in words that name it, as "the project's editors
change its connections (it takes the permission connections)", and the tab or button that needs it isn't shown.

## Defining a project role

### What it's for

Teams need roles between editor and reader: a data steward who reads every table unmasked and confirms the catalog's tags
but changes nothing; an auditor who reads everything and the audit log; an analyst who runs pipelines but doesn't build.
An administrator defines each once, for every project to give.

### Who can do it

Administrators. Anyone signed in sees the roles (`GET /api/roles`); anyone else who tries to define one is refused: "only
administrators change projects".

### Set up: on the page

1. Open **Administration** → **Roles** → **Define a project role**.
2. **Name**: 2 to 24 lowercase letters, digits and dashes, starting with a letter: `auditor`. Fixed once defined.
3. **What it's for**: up to 300 characters: "Reads every table as it is and the audit log; changes nothing".
4. **What it may do**: tick the permissions. `read` is ticked to start with. For the auditor: `read-all`, `read-files`,
   `see-audit`.
5. Press **Define it**. The role's card lists what it gives, `read` among them "(comes with another)", and "No project gives
   it." until one does.

### Set up: the API

`PUT /api/roles/{role}`, with `X-Platform-Request: 1`:

```json
{
  "description": "Reads every table as it is and the audit log; changes nothing",
  "permissions": ["read-all", "read-files", "see-audit"]
}
```

| Field | What it is |
|---|---|
| `description` | Up to 300 characters. Optional. |
| `permissions` | At least one of the 17 project permissions |
| `scope` | Leave it out for a project role (`"platform"` for a platform role) |

The name comes from the path. It answers with the role, what it grants (`granted`, the implied permissions with it) and the
projects giving it. The audit log records `role.saved` with "permissions read-all, read-files, see-audit".

The definitions are kept in the platform's bucket (`roles/roles.json`), not in any project.

### Limits and gotchas

What's refused (400 unless said):

| Message | Why |
|---|---|
| "a role's name is 2 to 24 lowercase letters, digits and dashes, starting with a letter: "Auditor" isn't" | The name |
| "editor is the platform's own" | `editor`, `reader`, `pipelines`, `agent`, `serving`, `admin`, `editors`, `readers` |
| "finance is the install's (its settings' groups): define another" | A built-in platform role's name |
| "a role gives at least one permission" | None ticked |
| "a description of up to 300 characters" | Too long |
| "no permission "write" for a project's role: they're [read read-all ...]" | Not one of the 17 |
| "a project's role is given in a project's spec, not to groups here" | `groups` on a project role |
| "auditor is a project's role: remove it first to make it another kind" (409) | Changing a role's scope |
| "a role, as JSON: its description and permissions" | The body isn't JSON |
| "the platform keeps no files here (no STORE_URL)" (503) | No platform bucket |

## Giving a project role

### Set up

A project gives a defined role exactly as it gives editor and reader: on the project's Edit page, **Members**, the role
offered beside editor and reader, for a Keycloak group or a person. In the spec:

```json
"members": [
  {"group": "engineers", "role": "editor"},
  {"group": "analysts", "role": "reader"},
  {"user": "alice", "role": "auditor"}
]
```

See [Projects](02-projects.md#members-and-roles).

### How it behaves

- **A role must be defined before a project gives it.** Saving a project that gives an undefined role is refused (422):
  "role steward: reader, editor, or a role defined on Administration → Roles (none is called that)". A platform role can't be
  given in a project: the same refusal.
- **One role each, the one that gives the most.** Someone given several roles in a project (through groups and by name)
  has one: an editor's above every other, a reader's below every other, and between them the role that grants more
  permissions. alice, a reader through `analysts` and an auditor by name, is an auditor.
- **Within a minute**, the project's **Access** tab lists her as `auditor`, with a card (titled "Auditor", its group
  `retail-role-auditor`) saying in words what the role gives; the Overview's "What you can do here" says "Your role here is
  auditor."; her header shows the role; the tabs and buttons follow its permissions (the Audit tab appears, for one).
- Administration → **Access** shows every person's role in every project.

## How a project role is applied

The operator applies a project role in every system the built-in roles reach, from one mapping:

| System | What the operator does |
|---|---|
| **Keycloak** | A group per role per project: `<project>-role-<role>`, holding exactly the people given the role (and who aren't given a higher one) |
| **Ranger** (Trino) | The same group, named in the project's policies by what the role's permissions give: the namespace, its tables, the readers' row filters and masks, the tags' masks, its sources, its merge requests' data branches, and the data products it consumes. See the next section |
| **Buckets** | A policy named as the group: read and write with `write-files`, read with `read-files`, none otherwise |
| **The project's own Airflow** (Kubernetes, a project with a repo) | The group as `Op` with `run-pipelines`, else `Viewer` |
| **platformd and semanticd** | Each member's permissions are recorded in the project's status; every page and route checks the one it needs |

**What doesn't follow a project role:** Superset's own roles (who may build datasets, charts and dashboards) and the shared
Airflow's pages follow the platform's groups (`analysts`, `engineers`). What a role *reads* holds in Superset all the same,
since every query runs in Trino as the person. A project's pipelines are run from its Pipelines tab, which asks for
`run-pipelines`.

A data product's consumers: when a project consumes another's product, its custom roles that read (`read`, `read-all` or
`write-tables`) read the product too, as its readers and editors do: under the producing project's readers' rules.

## Reading tables: readers' rules, read-all and editors

How a role reads a project's tables decides which of its Ranger policies name its group. There are three ways:

| A role with | Reads | The readers' rules (`tables.readers`) | The tags' masks (protection) |
|---|---|---|---|
| `write-tables` (editors) | every table, and writes them | don't apply | lifted |
| `read-all`, not `write-tables` | every table, as it is | don't apply | lifted |
| `read`, not `read-all` (readers) | the tables readers may read | apply: their row filters and masks | apply |
| none of them | no table (not even the namespace's list) | | |

The project's pipelines read and write every table, and the readers' rules don't name them either.

**Editors and read-all roles come first, unfiltered.** Ranger keeps one row-filter policy and one mask policy for each
table and column, and applies the first item of it that a person matches. A person can be in several groups at once: alice
may edit `retail` and be an analyst, and the analysts may be readers somewhere, or `retail`'s data may be read by another
project whose groups she's in. So in each of the project's readers' row-filter and mask policies, the groups that read the
tables as they are (its editors' and its read-all roles') come first, with no mask (`MASK_NONE`) and no row filter, then
the readers' groups (and the consuming projects' groups) with the filter or mask. Whatever else alice is, an editor of
`retail` reads `retail`'s tables whole.

"No row filter" for them is an empty one, which Ranger applies as none: Trino refuses to update a table that has any row
filter on it (even `true`), so editors keep `UPDATE`, `DELETE` and `MERGE` on a filtered table.

Within one project, the operator puts each person in one of the project's groups only (see
[Giving a project role](#giving-a-project-role)), so an editor of a project is never also in its readers' group.

For example, with `retail`'s readers' rule `uploads_orders: region = 'EU'` and a mask on `uploads_customers.signed_up`:

| | `uploads_orders` | `uploads_customers.signed_up` |
|---|---|---|
| bob, an editor | every region | the date |
| alice, an auditor (`read-all`) | every region | the date |
| an analyst, a reader | `EU` only | the year only |
| a reader of `marketing`, which consumes `uploads_orders` as a data product | `EU` only | (not a product: not read) |

The readers' rules themselves (which tables, which filters, which masks) are in
[Access and protection](04-access-and-protection.md). The Governance page's **What readers see** lists, for each project,
its readers and the custom roles that read as readers do (with `read` but not `read-all`), with the role named.

See [Lab 13](../guide.md#lab-13-rules-for-readers-and-every-tool-following) for the readers' rules, and
[Lab 21](../guide.md#lab-21-who-did-what) for a role that lifts them.

## Platform roles

### What they're for

Some people work across every project without being a member of any: finance reads every project's cost; the governance
group certifies data products; the privacy office approves deliveries of personal data and answers erasures. A platform
role gives such a group its permissions everywhere.

### The platform permissions

| Permission | The Roles page says | Where it holds |
|---|---|---|
| `see-all-usage` | See every project's usage and cost | Administration → Usage, or **Usage and cost** in the account menu; every project's Usage tab |
| `see-governance` | See the Governance page: every project's approvals, agents, rules, products and erasures | **Governance** in the account menu |
| `certify-products` | Certify data products, and take certification away | The Data products page |
| `protect-personal` | Approve deliveries that send personal columns, search what was sent about a person, and record erasures | Deliveries, the delivery log's search, erasures |

### The install's platform roles

Three are built in, made from the install's settings, and shown on the Roles page marked **the install's**:

| Role | Permissions | Its groups (setting) | Chart value | Default |
|---|---|---|---|---|
| `finance` | `see-all-usage` | `USAGE_VIEWER_GROUPS` | `usage.viewerGroups` | the administrators' groups |
| `governance` | `certify-products`, `see-governance` | `PRODUCT_CERTIFIER_GROUPS` | `products.certifierGroups` | the administrators' groups |
| `data-protection` | `protect-personal`, `see-governance` | `DELIVERY_APPROVER_GROUPS` | `deliveries.approverGroups` | the administrators' groups |

Change their groups with the settings (`storscale upgrade --set products.certifierGroups=governance`), not on the page.
Compose sets `DELIVERY_APPROVER_GROUPS: engineers,analysts`, so the example's alice is a data-protection approver there.

### Who can do it

Administrators define, change and remove platform roles.

### Set up: on the page

1. **Administration** → **Roles** → **Define a platform role**.
2. **Name**: `privacy-office`.
3. **What it's for**: "The privacy office: approves deliveries of personal data, answers erasure requests".
4. **Keycloak groups given it**: comma-separated, at least one: `privacy-office`. Everyone in them has it, across every
   project.
5. **What it may do**: tick `protect-personal` and `see-governance`.
6. **Define it**. The card says "Given to the Keycloak groups privacy-office."

### Set up: the API

`PUT /api/roles/privacy-office`, with `X-Platform-Request: 1`:

```json
{
  "description": "The privacy office: approves deliveries of personal data, answers erasure requests",
  "scope": "platform",
  "groups": ["privacy-office"],
  "permissions": ["protect-personal", "see-governance"]
}
```

### How it behaves

- Someone may do what any platform role of any of their groups gives; an administrator, everything.
- platformd reads the defined platform roles again every half minute, so a new or changed one reaches its groups within
  half a minute (at once on the platformd that saved it). The account menu's links come with the page: reload it.
- A person's groups are those in their sign-in. Someone just added to the role's group in Keycloak signs out and in
  again to have it.
- The audit log records `role.saved` with the permissions and "given to groups privacy-office" (and, for a change, what
  they were).

### Limits and gotchas

- **"a platform role is given to at least one Keycloak group"**; **"no permission "read" for a platform role: they're
  [see-all-usage see-governance certify-products protect-personal]"**; **""data protection" isn't a Keycloak group's name"**.
- A platform role is never a project's: a project can't give it to its members.
- A platform role gives nothing inside a project's tables: a privacy officer who must read personal data needs a project
  role too (or to be in a protection rule's `except`; see [Access and protection](04-access-and-protection.md)).

## Changing and removing a role

### Set up

On a role's card, **Change** opens its form (name and kind fixed): change the description, the permissions, or (a
platform role) its groups, and **Save**. **Remove** asks "Remove the role auditor?". The API: `PUT /api/roles/{role}` with
the whole role again; `DELETE /api/roles/{role}` (with `X-Platform-Request: 1`), which answers 204.

### How it behaves

- **A project role changed reaches every project giving it within a minute.** A project's applied state covers the
  permissions of the roles it gives, so the operator applies each such project again, without its spec changing: the
  groups' Ranger policies, Buckets policies, its Airflow's roles and the permissions recorded for each member. Take
  `read-all` from `auditor`, and alice reads `retail` as readers do again, in every project giving the role.
- **A platform role changed reaches its groups within half a minute.**
- The audit log records each change, with what it was: `role.saved`, "was read-all, read-files, see-audit; now read,
  read-files, see-audit"; and `role.removed`.
- **A project role that a project gives can't be removed** (409): "the projects retail give auditor: change their members
  first". The refusal is in the audit log too. On the page, **Remove** is greyed out, with "Given in retail" as its tip.
  Change those projects' members, then remove it.
- A platform role can be removed at any time; its groups lose what it gave within half a minute.
- If a role a project gives disappears all the same (its file edited by hand), its members get nothing, and the project
  says so under **Applied to**: "no role auditor: an administrator defines it (Administration → Roles); its members have no
  access meanwhile". The Access tab says "No role auditor is defined: its members have no access until an administrator
  defines it."

See [Lab 21](../guide.md#lab-21-who-did-what) in the lab guide, steps 7 to 10: an auditor defined, given, changed, and a
platform role.

## Settings

| What | Chart value | Compose (platformd's environment) | Default |
|---|---|---|---|
| The administrators | not a value | `PLATFORM_ADMIN_GROUPS` | `engineers` |
| `finance`'s groups | `usage.viewerGroups` (a list) | `USAGE_VIEWER_GROUPS` | the administrators' groups |
| `governance`'s groups | `products.certifierGroups` | `PRODUCT_CERTIFIER_GROUPS` | the administrators' groups |
| `data-protection`'s groups | `deliveries.approverGroups` | `DELIVERY_APPROVER_GROUPS` (`engineers,analysts` in `stack/compose.yaml`) | the administrators' groups |

The defined roles are kept in the platform's bucket, `roles/roles.json` (`STORE_BUCKET`, `storscale-platform` by default):
the operator and platformd both read it, and backups keep it with the bucket.

## Limits and gotchas

- **`call-models` isn't checked yet.** It's listed, and the reader has it, but any member of a project may call its served
  models whatever their role; a role without `call-models` doesn't stop it.
- **Approving is never one's own.** `approve` lets someone approve agents, model versions and deliveries, but not one they
  registered or changed themselves, whatever their role: "someone other than who changed what it sends approves it".
- **A role with `write-tables` reads everything as it is.** There's no "writes but reads as a reader": writing a table
  implies reading all of it.
- **A role without `read` (or more) sees no tables at all**: not even the namespace's list. A role for running pipelines
  alone still needs `read` to see what they made.
- **Reading files and tables are separate.** `read-all` doesn't give the bucket: add `read-files`.
- **Asking for a data product takes `build`** in the asking project, and answering requests for one takes `build` in the
  producing project.
- **Superset's and the shared Airflow's own roles** follow the platform's groups, not project roles (see
  [How a project role is applied](#how-a-project-role-is-applied)).
- **The Roles page needs the platform's bucket.** Without `STORE_URL`, roles can't be defined: "the platform keeps no
  files here (no STORE_URL)".
