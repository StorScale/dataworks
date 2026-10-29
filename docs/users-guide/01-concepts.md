# 1. Concepts and first sign-in

This chapter is for everyone who uses the platform: analysts, engineers and the administrators who run it. It says what the
platform is made of, how people and Keycloak groups decide who gets in and what they may do, how signing in works, what a
project is, which tools sit behind the platform's pages, and what each of the example people (alice, bob and carol) sees
on their first sign-in. The other chapters build on the words defined here.

In this chapter:

- [The platform in one page](#the-platform-in-one-page)
- [People and groups](#people-and-groups)
- [Signing in](#signing-in)
- [Projects](#projects)
- [The tools](#the-tools)
- [The home page and the top bar](#the-home-page-and-the-top-bar)
- [A project's tabs](#a-projects-tabs)
- [What each person sees](#what-each-person-sees)
- [The example people](#the-example-people)
- [Settings that change this chapter](#settings-that-change-this-chapter)
- [Limits and gotchas](#limits-and-gotchas)

## The platform in one page

The platform is a lakehouse with a web app in front of it. Data lives as Iceberg tables in Buckets (S3-compatible
storage), catalogued by Nessie and queried through Trino. Apache Ranger decides, query by query, who may read what in Trino.
Keycloak signs everyone in, once, for every tool. Around that core are the tools people work in: JupyterHub (notebooks),
Superset (SQL Lab and dashboards), Airflow (pipelines), OpenMetadata (the catalog), Grafana (monitoring), MLflow (models) and
an LLM gateway (models through a budget).

Two programs of the platform's own hold it together:

| Program | What it does |
|---|---|
| **platformd** | The web app and its API (`/api/...`). It signs people in with Keycloak, shows each person their projects and tools, and checks every change they ask for. |
| **The platform operator** | Applies each project to the systems it lives in: Keycloak groups, Ranger policies, Buckets buckets and policies, Nessie namespaces, Airflow. It looks every few seconds for a changed project, and goes over every project again each minute, so a change made outside the platform (someone added to a Keycloak group) is picked up within a minute. |

A **project** is what the operator applies (see [Projects](#projects) below). A person never configures Ranger, Buckets or
Keycloak groups by hand for a project: the project says what it wants, and the operator keeps the systems that way.

## People and groups

Everyone is a user in Keycloak, in the realm `lakehouse`. What a person may do comes from the Keycloak groups they're in:

| Groups | Setting (platformd) | Default | What membership gives |
|---|---|---|---|
| The platform's groups | `PLATFORM_GROUPS` | `analysts,engineers` | May use the platform at all. Someone in none of these is shown "No access". |
| The administrators' groups | `PLATFORM_ADMIN_GROUPS` | `engineers` | Administration, every project, every permission in every project, and the platform roles' permissions. |
| A project's member groups | the project's `members` | none | Membership in that project, with a role (see [Projects](#projects)). |
| A platform role's groups | Administration → Roles | see below | A platform-wide permission: every project's usage, the Governance page, certifying data products, protecting people's data. See [Roles](16-roles.md). |

The install's own platform roles take their groups from three settings, each defaulting to the administrators' groups:

| Platform role | Gives | Setting | Chart value |
|---|---|---|---|
| `finance` | `see-all-usage` | `USAGE_VIEWER_GROUPS` | `usage.viewerGroups` (a list; `[]` by default) |
| `governance` | `certify-products`, `see-governance` | `PRODUCT_CERTIFIER_GROUPS` | `products.certifierGroups` (comma-separated; `""` by default) |
| `data-protection` | `protect-personal`, `see-governance` | `DELIVERY_APPROVER_GROUPS` | `deliveries.approverGroups` (comma-separated; `""` by default) |

Groups beat names: a project that names the group `analysts` as its readers has every analyst as a reader, the day they
join the group, and loses them the day they leave. Name a person (`{"user": "alice", ...}`) only when they need something
their group doesn't give.

## Signing in

### What it's for

One sign-in for the platform and every tool behind it, so no one shares passwords and no tool keeps accounts of its own.

### Who can do it

Anyone with a Keycloak account. Only people in one of the platform's groups (`PLATFORM_GROUPS`) get past the first page.

### How it works

1. Open the platform's address (on a local install, `http://storscale.localhost:8800`). The web app asks platformd who's
   signed in (`GET /api/session`); with no session, the browser goes to `/auth/login` and on to Keycloak's sign-in page
   (Keycloak is at `/sso` on the platform's address).
2. Sign in with Keycloak's form. Keycloak sends the browser back to `/auth/callback`; platformd checks the token (OpenID
   Connect, authorization code with PKCE) and starts a session: a cookie, `storscale_session`, good for 10 hours.
3. Open any tool from the **Tools** menu: each one signs in with Keycloak on the way, without a form, because the person
   already has a Keycloak session.
4. **Sign out** (the account menu, top right) ends the platform's session and Keycloak's too, so the tools are signed out
   with it.

### With the company's account (Entra ID)

An install can let people sign in with their Microsoft Entra ID account (`sso.entra`, [Operating the
platform](17-operating.md)). Keycloak's page then has a button for it (*Microsoft Entra ID*, or the install's name for it)
beside the form, and the rest is as above:

- **Their username** is their Entra login name's local part: `russell.myers@vericast.com` signs in as `russell.myers`. It's
  the name projects, the audit log and the tools know them by.
- **Their name and email** are Entra's `given_name`, `family_name` and `email` claims, updated at every sign-in. The app
  registration must add them to its ID token (*Token configuration*, optional claims); without them Keycloak splits the
  display name ("Russell Myers (Vericast)" becomes first name "Russell Myers", last name "(Vericast)") and asks the person
  to correct their profile.
- **Their groups** come from the app roles Entra gives them on the platform's app registration, mapped to the platform's
  groups: by default `Analyst` puts them in `analysts`, and `Engineer` and `Administrator` in `engineers` (the
  administrators). They're set again at every sign-in, so a role an Entra administrator takes away takes its groups away at
  the next one. Someone with no role signs in and gets "No access".
- **Projects** still say who's a member, by username or by group, as for anyone else.

Sign-ins and sign-outs are in the audit log (`signed.in`, `signed.out`). A sign-in by someone in none of the platform's
groups is recorded as refused, with "in none of the platform's groups". See [Operating the platform](17-operating.md) for the
audit log.

### From a script

The API takes a Keycloak access token of the realm's in place of the cookie: `Authorization: Bearer <token>`. platformd
reads the person and their groups from the token, so a script can do what its person may do on the pages and nothing more.
A change (anything but a `GET`) also needs the header `X-Platform-Request: 1`, which a cross-site form can't send. The
account menu's **API reference** (and `GET /api/openapi.json`) lists every route. [Operating the platform](17-operating.md)
covers the Python package, which handles tokens for you.

A token for a script, as yourself, without a password (an Entra ID sign-in has none to give it): the realm's public client
`storscale-agent` takes a device code (OAuth's device authorization grant). The script asks for one, you open its link in your
own browser and approve, and the script gets your token: the Python package does all of it, `storscale.Platform(url)`.

```bash
curl -s https://<platform>/sso/realms/lakehouse/protocol/openid-connect/auth/device \
  -d client_id=storscale-agent -d scope=openid -d code_challenge=$CHALLENGE -d code_challenge_method=S256
# open its verification_uri_complete; then, every interval seconds until it's approved:
curl -s https://<platform>/sso/realms/lakehouse/protocol/openid-connect/token -d client_id=storscale-agent \
  -d grant_type=urn:ietf:params:oauth:grant-type:device_code -d device_code=$DEVICE_CODE -d code_verifier=$VERIFIER
```

### Limits and gotchas

- **"No access".** Signed in, but in none of the platform's groups: the page says "Your account isn't in a group that may
  use DataWorks (analysts, engineers)." with the install's groups, and offers **Sign out**. An administrator adds
  the person to a group in Keycloak; then they sign out and in again.
- **Group changes need a new sign-in for the platform's own checks.** Whether someone is an administrator, may use the
  platform, or has a platform role is read from the groups in their token as they signed in. Project membership is
  different: the operator reads Keycloak's groups each minute, so a person added to a project's group is a member within a
  minute, without signing in again.
- **Sessions live in platformd's memory.** When platformd restarts, everyone signs in again; Keycloak's own session makes
  that silent (no form).
- **An old sign-in link** (more than 10 minutes, or used once already) starts the sign-in again rather than failing.
- **"Keycloak refused the sign-in: ..."** is Keycloak's own refusal, passed on with its error code.

See [Lab 1](../guide.md#lab-1-sign-in-and-see-what-each-person-sees) in the lab guide.

## Projects

A project is a team's tables, files and pipelines, and who may do what with them. It's the unit of ownership: everything
else in this guide (imports, checks, metrics, flows, agents, deliveries, models, apps) belongs to a project and follows its
rules.

A project is written as a spec, JSON, which an administrator makes on Administration → Projects (see [Projects](02-projects.md)):

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
    "tables": {},
    "files": {},
    "pipelines": {"serviceAccount": "airflow-pipelines"}
  }
}
```

What each part becomes:

| Part | In the systems | Named |
|---|---|---|
| The project's members, by role | Keycloak groups, one per role, and the same groups in Ranger and Buckets | `retail-editors`, `retail-readers`, `retail-role-<role>` for a role of the company's own |
| `tables` | A namespace in Nessie's `iceberg` catalog, queried through Trino, with Ranger's policies on it | `iceberg.retail` (dashes in the name become underscores) |
| `files` | A bucket in Buckets, with a policy per role | `retail` |
| `pipelines` | A Keycloak service account that runs the project's pipelines: it reads and writes the tables, and the files under `landing/` | `retail-pipelines` (its group) |

Each project has **members**, each a Keycloak group or a person, with a **role**:

| Role | In short |
|---|---|
| `editor` | Makes and changes the project's tables, files, pipelines, metrics and models; everything the project has. |
| `reader` | Reads what the project shows readers (the tables it lists, through its row filters and masks), its files, and calls its models. |
| A role of the company's own | What an administrator defined on Administration → Roles, from the platform's permissions. See [Roles](16-roles.md). |

Two more kinds of member are the platform's, never given by hand: `pipelines` (the project's service account) and its
agents' and served models' accounts, which read as its readers do.

The operator records, for each project, what it last applied: a **phase** (`Ready`, `Applying`, `Error`, `Deleting`; the
web app shows "Waiting for the operator" before the first pass), a line per system it applied (`keycloak`, `ranger`,
`buckets`, `nessie`, and others as the project uses them), and who's in it through which role. A project's Overview shows
these under **Applied to** and **People**.

## The tools

The **Tools** menu (top bar) lists the tools behind the platform; Home lists the same under **Also on the platform**. Each
tool checks for itself what a person may do: the menu is what to show, not what's allowed.

| Tool | What it is | Where it opens |
|---|---|---|
| **Notebooks** | JupyterLab, in a notebook server of your own, with your own Buckets credentials (JupyterHub) | In the platform's frame, `/notebooks/` |
| **SQL** | Superset's SQL Lab: query the lakehouse in Trino, as yourself | In the platform's frame, `/dashboards/sqllab/` |
| **Dashboards** | Superset's dashboards and charts | In the platform's frame, `/dashboards/` |
| **Pipelines** | Airflow: pipelines, their runs and their logs | In the platform's frame, `/pipelines/` |
| **Catalog** | OpenMetadata: every table and column, their lineage and their checks | Its own tab, at `catalog.` under the platform's address |
| **Monitoring** | Grafana: the storage's dashboards and alerts | In the platform's frame, `/monitoring/` |
| **Access policies** | Apache Ranger: who may query what. Administrators only, signed in with Ranger's `admin` account | Its own tab, at `access.` under the platform's address |

Trino itself takes JDBC and CLI clients at its own port (on a local install, `https://localhost:8443`), signed in with a
Keycloak token. Every query, from any tool, runs in Trino as the person who asked, so Ranger's rules hold the same in SQL
Lab, a dashboard, a notebook and an assistant.

Most of the time, a project's own tabs do the work without opening a tool: the SQL tab, the Data tab, the Pipelines tab.
The tools are for what the platform's pages don't cover.

## The home page and the top bar

### Home

Home greets the person by first name ("Good morning, Alice") and shows:

- **Your projects**: a card for each project they're a member of (every project, for an administrator), with their role,
  the project's description, its phase when it isn't Ready, how many tables and metrics it has, failing checks or "checks
  passing", and when its pipeline last ran. "You're not in any project yet. Ask an administrator to add you." when there's none.
- **Continue in &lt;project&gt;**: back to the project they were last in.
- **Apps**: the apps of their projects, when there are any (see [Dashboards, apps and assistants](07-dashboards-apps-assistants.md)).
- **Recent pipeline runs**: the six latest runs across their projects, who started them (or "schedule").
- **Needs attention**: every failing check and every pipeline whose last run failed, each linking to it; "Nothing: every
  check is passing and every last run succeeded." otherwise.
- **Also on the platform**: the tools.
- A notice from the Health page when part of the platform is down.

Home refreshes itself every 30 seconds.

### The top bar

| Part | What it does |
|---|---|
| The brand (top left) | Home. |
| **Project ▾** | Switch project: "Your projects" with your role in each, then Home, and for an administrator **Manage projects**. |
| Search (press `/`) | In a project: its business terms, data products, tables, columns, metrics and pipelines. Outside one: business terms, data products and projects. Terms come first, then certified products. |
| **Business terms** | The company's terms (when the semantic layer is installed). See [Metrics and business terms](06-metrics-and-terms.md). |
| **Data products** | Every project's data products. See [Data products](11-data-products.md). |
| **Tools ▾** | The tools above. |
| Your name | The account menu: who you're signed in as and your groups; **Administration** (administrators); **Usage and cost** and **Governance** (for those with the platform roles that give them, who aren't administrators); **API reference**; **Sign out**. |

## A project's tabs

Open a project (`/p/<project>`) and its header shows its name, your role in it, its phase when it isn't Ready, and its
description; below, the tabs. A tab shows only when the project has what it needs, and some only for some people:

| Tab | Shown when | What it's for | Chapter |
|---|---|---|---|
| **Overview** | always | Tiles, how data flows, what each role can do, its people, its alerts, where it's applied; Edit and Delete for administrators | [Projects](02-projects.md) |
| **Data** | it has `tables` (and the catalog is installed) | Each table: its columns, preview, quality, history | [Data quality](05-quality.md) |
| **Metrics** | `tables` (and the semantic layer) | The semantic model and its metrics | [Metrics](06-metrics-and-terms.md) |
| **SQL** | `tables` (and the semantic layer) | Queries in Trino, as yourself | [Metrics](06-metrics-and-terms.md) |
| **Notebooks** | the Notebooks tool is there | JupyterHub, in the frame | [Notebooks and models](10-notebooks-and-models.md) |
| **Pipelines** | it has `pipelines` (and Airflow) | Its pipelines, runs and logs | [Pipelines and git](09-pipelines-and-git.md) |
| **Flows** | `tables` (and the semantic layer) | The canvas | [Flows](08-flows.md) |
| **Agents** | `tables` (and the semantic layer) | Its agents | [AI](14-ai.md) |
| **Apps** | `tables` (and the semantic layer) | Its apps | [Dashboards, apps and assistants](07-dashboards-apps-assistants.md) |
| **Models** | always | MLflow's models, approvals, scoring, serving | [Notebooks and models](10-notebooks-and-models.md) |
| **Dashboards** | the Dashboards tool is there | Superset, in the frame | [Dashboards, apps and assistants](07-dashboards-apps-assistants.md) |
| **Tags** | `tables` | Moments of the tables kept | [History and releases](15-history-and-releases.md) |
| **Deliveries** | it has at least one delivery | Files to partners | [Deliveries](12-deliveries.md) |
| **Releases** | its pipelines have a repo | Plans and releases of the project as code | [History and releases](15-history-and-releases.md) |
| **Imports** | `tables` | Copying data in on a schedule | [Getting data in](03-data-in.md) |
| **Connections** | always | Its credentials | [Getting data in](03-data-in.md) |
| **Access** | always | Who's in it, through which group, what each role gives, what readers don't see | [Access and protection](04-access-and-protection.md) |
| **Audit** | you have `see-audit` in it (editors, administrators) | Its audit log | [Operating the platform](17-operating.md) |
| **Usage** | you have `see-usage` in it, or `see-all-usage` across the platform | Its usage and cost | [Operating the platform](17-operating.md) |

The project's Edit page (`/p/<project>/edit`) is for administrators, opened from **Edit project** on the Overview or
**Change members** on the Access tab; it isn't a tab.

## What each person sees

What a person sees follows from two things: their groups (which give the platform and its Administration) and their role
in each project.

| | Someone in none of the platform's groups | A project's reader | A project's editor | An administrator |
|---|---|---|---|---|
| Gets in | No: "No access" | Yes | Yes | Yes |
| Projects on Home and in the switcher | none | those they're a member of | those they're a member of | every project |
| A project's Overview "What you can do here" | | "You're a reader." | "You're an editor." | "You administer the platform; you're not a member of this project." (unless also a member) |
| Tables | | those the project shows readers, through its row filters and masks | every table as it is | what the groups they're in are given in Trino (see below) |
| Audit and Usage tabs | | no | yes | yes |
| Run pipelines, change metrics, build flows, apps and agents, give connections | | no | yes | yes |
| Edit or delete the project, make projects | | no | no | yes |
| Administration (account menu) | | no | no | yes |
| Access policies (Ranger) in Tools | | no | no | yes |

A role of the company's own sits in between: the Overview says "Your role here is &lt;role&gt;.", and what the role's
permissions give decides each tab and button. See [Roles](16-roles.md).

An administrator's power in a project is the platform's: on the project's pages and API they may do everything. Ranger
gives administrators nothing of their own: what they read in Trino is what the groups they're in are given, so an
administrator who isn't a member of a project reads none of its tables (the install's `engineers` are editors of the
example projects, which is why bob reads them).

## The example people

A local install comes with three people in Keycloak, and their password is `LAKEHOUSE_USER_PASSWORD` in `stack/.env`. On
Kubernetes it's generated on install and kept in the Secret `storscale-env`:

```bash
kubectl -n storscale get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d
```

| Person | Keycloak group | Gets in | Administrator | In the sample project `sales` |
|---|---|---|---|---|
| **alice** (Alice Analyst), an analyst | `analysts` | yes | no | reader (through `analysts`): the EU's orders, card numbers showing their last four digits |
| **bob** (Bob Engineer), an engineer | `engineers` | yes | yes | editor (through `engineers`): every order, whole |
| **carol** (Carol Contractor) | none | no: "No access" | no | none |

The install also comes with a sample project, `sales` (orders and payroll, for the integration guides), whose editors are
`engineers` and readers `analysts`. The lab guide makes `retail` and `marketing` for Harbor Outfitters.

With the defaults, bob is in every one of the install's platform roles (their groups default to the administrators'). On
Compose, the data-protection approvers are `engineers,analysts` (`DELIVERY_APPROVER_GROUPS` in `stack/compose.yaml`), so the
example has an approver besides bob: alice has `protect-personal` and `see-governance`, and sees **Governance** in her
account menu. On Kubernetes, `deliveries.approverGroups` is empty, so only the administrators are.

See [Lab 1](../guide.md#lab-1-sign-in-and-see-what-each-person-sees) in the lab guide, which signs in as each of them.

## Settings that change this chapter

| What | platformd's environment (Compose: `stack/compose.yaml`) | Chart (`values.yaml`) | Default |
|---|---|---|---|
| Who may use the platform | `PLATFORM_GROUPS` | not a value: platformd's default | `analysts,engineers` |
| Who administers it | `PLATFORM_ADMIN_GROUPS` | not a value: platformd's default | `engineers` |
| Every project's usage (finance) | `USAGE_VIEWER_GROUPS` | `usage.viewerGroups` | the administrators' groups |
| Certifying products, Governance | `PRODUCT_CERTIFIER_GROUPS` | `products.certifierGroups` | the administrators' groups |
| Data-protection approvers | `DELIVERY_APPROVER_GROUPS` (Compose sets `engineers,analysts`) | `deliveries.approverGroups` | the administrators' groups |
| The platform's address | `STORSCALE_DOMAIN`, `STORSCALE_PORT` in `stack/.env` | `global.domain`, `global.port`, `global.scheme` | `storscale.localhost`, `8800`, `http` |
| The brand in the top bar and the tab | `BRAND_NAME`, `BRAND_TITLE`, `BRAND_LOGO`, `BRAND_ICON` | `brand.name`, `brand.title`, `brand.logo`, `brand.icon` | none: the tab reads *DataWorks* |
| The example people | the realm (`stack/keycloak/realm.json`) | `examples.users` | `true` |

## Limits and gotchas

- **`examples.users` doesn't switch the example people off yet.** The value is in the chart, but alice, bob and carol come
  from the realm whatever it says. Remove them in Keycloak on a real install.
- **Signed in, but no projects.** Home says "You're not in any project yet. Ask an administrator to add you." An
  administrator adds the person (or a group they're in) to a project's members.
- **A tab is missing.** Tabs show only when the project has what they need (Pipelines needs `pipelines`; Deliveries needs a
  delivery; Releases needs a repo) and when you have the permission (Audit, Usage). The table in
  [A project's tabs](#a-projects-tabs) says which.
- **The Trino address on Home is fixed text** (`https://localhost:8443`), right for a local install; on another install,
  Trino's port is wherever the chart's `trino.service` puts it.
