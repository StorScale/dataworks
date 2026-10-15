# StorScale Platform: use cases and storyboards

What people need to do, phase by phase, told as storyboards: who does what, frame by frame, and what they see. Each ends with the requirements it sets (R-numbers, which the phase's work and tests trace back to) and the questions still open. The phases are in [plan.md](plan.md); this is a first draft, to be worked through with the people who'll use the platform.

## People

| Who | Role | Wants |
|---|---|---|
| **alice** | Analyst, a reader of `sales` | Answers from the data: SQL, metrics, dashboards. Not to think about tools. |
| **bob** | Data engineer, an editor of `sales` | Pipelines that are reviewed, tested and safe to change. Git, not clicking. |
| **carol** | In neither group | Nothing. Every storyboard checks she gets nothing. |
| **priya** | Platform administrator | An install that upgrades without drama, says what's wrong, and holds up to an audit. |
| **sam** | Sales manager, a business user | To ask questions in plain words and get the same numbers as the dashboards. |
| **dana** | Data scientist | Notebooks, experiments, and a way to put a model into use. |
| **frank** | Budget owner (finance) | To know what each project costs, and be told before it overruns. |
| **an auditor** | Internal audit or compliance | Who did what with which data, and proof that rules held. |
| **agents** | An assistant acting for a person (Claude, an IDE), or one with its own identity | The same data, under the same rules, as the person or role behind them. |

---

## Phase 6: production-ready

### 6.1 The first real people sign in

1. priya connects the platform's Keycloak to the company's sign-in (Entra ID or LDAP), and turns off the example people (`examples.users: false`). alice, bob and carol, and their shared password, are gone.
2. Someone from sales signs in for the first time with their company account. The platform knows them, but they're in no project: Home says so, and shows how to ask for access.
3. They ask to join `sales` as a reader. The project's owner sees the request on the project's Access tab and approves it.
4. Their next page load shows `sales`, with only the EU rows and masked card numbers, as for any reader. Nobody edited a Ranger policy or a Keycloak group by hand.

**Requirements**
- R6.1 Sign-in through a corporate identity provider, brokered by Keycloak; the company's groups map to platform roles.
- R6.2 `examples.users: false` removes the example people and their groups; the tests that sign in as them use test accounts of their own.
- R6.3 A first-run Home for someone in no project, saying how to get access.
- R6.4 Access requests: ask, approve or refuse in the project, with the result applied by the operator and recorded in the audit log.

**Open questions**
- Who owns the realm after install: the chart (re-applied on upgrade) or the company's administrators (imported once)?
- Do groups come from the identity provider (and the project maps them), or are they the platform's own?
- Who approves: a project's editors, a named owner, or administrators only?

### 6.2 An upgrade that loses nothing

1. A new release is out. priya runs `storscale upgrade --check` against the cluster. It reads the installed version and the target's version matrix, and reports what will change: images, the database migrations each tool will run, and anything that needs a decision first.
2. It takes a backup: Keycloak's and the tools' PostgreSQL databases, Nessie's catalog, the projects, Buckets' configuration. It says where it went and how big it is.
3. priya upgrades. The Health page shows each service coming back, setup step by step, with any failure's own logs.
4. Afterwards every project, person, policy, metric and dashboard is there, and the end-to-end checks pass on the upgraded install.
5. Had it failed, `storscale upgrade --rollback` restores the backup and the previous release.

**Requirements**
- R6.5 A version matrix per release (every component's version, tested together), and an upgrade check that reads it.
- R6.6 Backup and restore of every stateful part, tested in CI: a restore onto a fresh install passes the suites.
- R6.7 Upgrades as one command, with a rollback.
- R6.8 Keycloak on a real database, in production mode, keeping its sessions and settings through restarts and upgrades.

**Open questions**
- Where do backups go: a Buckets bucket of the install's own, or somewhere outside it?
- How long may an upgrade make the platform unavailable?

### 6.3 Who read payroll?

1. An auditor asks: who read `sales.payroll` in September, including through agents?
2. priya opens the Audit page, filters by table and month, and sees each read: the person, the agent if there was one, the tool (Trino, SQL page, notebook, dashboard), the time, and whether a policy refused it.
3. carol's attempt is there, refused. An agent's read is there, under the agent's name and the person it acted for.
4. priya exports the result for the auditor.

**Requirements**
- R6.9 Audit events from Keycloak, Ranger, Trino, Buckets and the platform itself, in one place (Loki), kept for a set time.
- R6.10 An Audit page: filter by person, agent, project, table, tool, time and outcome; export.
- R6.11 Each event names the agent and the person it acted for.

### 6.4 Something is wrong (this happened, 2026-10-14)

1. After an upgrade, the catalog's search store can't mount its volume on its new node. Nothing in the platform says so: the catalog's pages just fail.
2. *What should have happened:* the Health page shows the catalog as down, with the reason (the volume won't mount, from Kubernetes' events) and what depends on it (the Data tab's lineage, search, checks). priya gets an alert.
3. When it recovers, the page says so, and the alert clears.

**Requirements**
- R6.12 A Health page: each service, its state and the reason from its events and logs, and the platform features each one serves.
- R6.13 Alerts to administrators (Grafana alerting: email, chat) for a service down, a setup step failing, a certificate near expiry, storage near full.

### 6.5 Tables that stay fast

1. `sales.orders` gets a few thousand rows every ten minutes, from a pipeline and from change capture (8.1). After a month it's tens of thousands of small files and thousands of snapshots; alice's queries slow down, and storage keeps growing.
2. *What should happen:* each night the platform compacts the project's tables, expires snapshots older than the project's setting (say 7 days, more for tables that need time travel), and removes files nothing refers to, including those left by the branches of merged and closed pull requests (7.3).
3. The Health page shows each table's files, snapshots and size, and when it was last maintained; a table that grows out of line is flagged.

**Requirements**
- R6.14 Iceberg maintenance per project, scheduled: compaction, snapshot expiry, orphan file removal, and the branches of closed changes dropped.
- R6.15 Retention of snapshots per project and table (time travel kept as long as asked, no longer).
- R6.16 Each table's health (files, snapshots, size, last maintenance) on the Health page.

### 6.6 A node fails

1. One of the cluster's nodes is drained for maintenance, or fails.
2. *What should happen:* people signed in stay signed in; the platform's pages, sign-in, SQL and dashboards keep working, because their services run more than one copy on different nodes; Trino keeps its other workers. Pipelines in flight are retried.
3. Services that can't run twice (a single database) say so on the Health page, and come back on another node within minutes, their volumes moving with them (or, as on 2026-10-14, telling priya when one won't).

**Requirements**
- R6.17 Replicas, spread across nodes, for the services that allow them (the gateway, platformd, Keycloak, Trino's workers, Superset), with disruption budgets so a drain never takes them all.
- R6.18 Hardening: network policies (each service reachable only by those that use it), Pod Security standards, Buckets' encryption at rest (its KMS), and images scanned, with SBOMs, in each release.

**Open questions**
- Which parts need high availability at first? (Sign-in and the platform's pages, surely; the catalog maybe not.)
- An external, managed PostgreSQL for the tools' databases, where the company has one?

### 6.7 The right people hear about it

1. At 6:05 the nightly `orders_daily` fails. bob gets a message in the team's chat, with the failing task, its error and a link to the run.
2. At 7:00 `sales.orders` hasn't been updated since yesterday morning; its freshness target is 24 hours. The project's chat channel is told, and the table shows as stale in the catalog and on the project's Data tab.
3. A check fails (a card number is missing): the table's owner hears of it, with the rows that failed.
4. bob mutes the stale-table alert for a day while he fixes it.

**Requirements**
- R6.19 Notifications per project to chat (Slack, Teams) and email: a pipeline failed, a check failed, a table is stale.
- R6.20 Freshness targets per table, in the project; stale tables shown in the catalog and the project's pages.
- R6.21 Who's told, set per project; alerts muted for a while, recorded.

---

## Phase 7: git-native projects

### 7.1 bob adds a pipeline through git

1. bob makes a repo for `sales`' pipelines. On the project's Pipelines tab he chooses **Connect a repo**, gives its address and branch, and gets a deploy key (read-only) to add to the repo. The platform keeps the key as a Secret; it never shows it again.
2. He pushes `orders_daily.py`. Within a minute it appears on the Pipelines tab with the commit it came from, linked to the repo.
3. He starts it. The run shows the same commit, and its tasks run as the project's pipelines service account.
4. He pushes a change with a syntax error. The pipeline stays at the last good commit; the tab shows the import error with the file and line.
5. He fixes it and pushes; the error goes, and the next run uses the new commit. Yesterday's run, rerun, still runs yesterday's commit.

**Requirements**
- R7.1 `pipelines.repo`, `path`, `ref` and `credentials.secretRef` in the project, applied by the operator as an Airflow DAG bundle (plan, "Phase 7: pipelines from git, how").
- R7.2 A deploy key made by the platform per project (or a token given once), kept only as a Secret.
- R7.3 The Pipelines tab: each pipeline's commit and each run's, links to the repo, import errors by file and line.
- R7.4 A project's pipelines are its bundle's DAGs, not those tagged `project:<name>`.

### 7.2 Two projects, one pipeline name

1. The marketing project's repo defines a pipeline `orders_daily`, the same ID as sales'.
2. marketing's Pipelines tab says the pipeline is refused, and why. sales' keeps running.
3. marketing also tags a pipeline `project:sales`. It doesn't appear in sales.

**Requirements**
- R7.5 DAG IDs unique across projects: a clash is refused and reported, never silently replaced.
- R7.6 Each project's git credential reads its repo only.

### 7.3 A change, tested on its own data first

1. bob opens a pull request that changes how `orders_by_region` is computed.
2. The platform makes a Nessie branch for it (`pr-42`), runs the project's pipelines there, then the project's checks and the semantic layer's tests. The data in production is untouched.
3. The pull request shows the result as a status check: checks passed, row counts and a sample of what changed against production, and the metrics that moved (EU revenue 513.84 → 498.10).
4. alice, a reviewer, opens the branch's data in the SQL page (read-only), sees the change, and approves.
5. bob merges. The platform merges the Nessie branch into main and Airflow picks up the new commit, together.
6. Another pull request fails a check. Its status says which and why; it can't merge; production never sees it.

**Requirements**
- R7.7 A data branch per pull request, made and removed by the platform.
- R7.8 The project's pipelines and checks run on the branch; results reported to the pull request (GitHub, GitLab, Gitea status APIs).
- R7.9 A comparison of the branch against production: row counts, a sample, the metrics that changed.
- R7.10 Merge publishes data and code together; a failed check blocks it.

**Open questions**
- Which git hosts first? (The company's.)
- How much does the branch run: the whole project's pipelines, or only what the change touches (as dbt's Slim CI)?
- Large tables: run the branch on a sample?

### 7.4 From development to production

1. The company runs two installs: development and production. `sales` in development is at a release, `v1.3`, of its repo (spec, pipelines, metrics, checks).
2. priya (or a pipeline in CI) asks production for a plan: two pipelines changed, one metric added, analysts' row filter unchanged, one new member.
3. Someone with the right to approve accepts it, and production applies it.
4. A problem shows next morning. priya rolls `sales` back to `v1.2`: its spec, pipelines and metrics. Data written in between stays, and the rollback says so.

**Requirements**
- R7.11 The whole project as code in its repo: members, tables, checks, pipelines, metrics.
- R7.12 Plan, then apply, per install, with a target per environment (values that differ: buckets, schedules, members).
- R7.13 Approval before a production apply, recorded.
- R7.14 Rollback to an earlier release of a project.

### 7.5 bob's transformations in dbt

1. bob keeps the project's SQL transformations as dbt models, in the same repo as its pipelines: `orders_by_region`, `customers_active`. Their tests (unique, not null, accepted values) sit beside them.
2. A pipeline runs them: `dbt build` in Airflow, as the project's service account, in Trino, on the project's Iceberg tables.
3. The models, their columns, their tests' results and their lineage (column by column) appear in the catalog, as the pipelines' do today.
4. In a pull request (7.3), only the models the change touches, and those downstream of them, run on the branch.
5. The semantic layer's metrics are defined on the dbt models, in the same repo.

**Requirements**
- R7.15 dbt projects in the project's repo, run by Airflow in Trino (and Spark later), under the project's service account.
- R7.16 dbt's tests and lineage in the catalog, beside the checks and lineage the platform already reports.
- R7.17 Runs of only what changed, and what depends on it, for a pull request.

---

## Phase 8: getting data in

### 8.1 A database's changes reach the lakehouse

1. The orders live in the company's PostgreSQL (or SQL Server, Oracle). bob adds a source to `sales`: the database, the tables to bring in, and a read-only user, whose password the platform keeps as a Secret.
2. The platform starts change capture (Debezium): a first copy of each table, then every insert, update and delete, into the project's Iceberg tables, within minutes.
3. The tables appear in the catalog with their source as lineage, and under the project's access rules: alice sees the EU rows only, with masked card numbers, as for any table of the project.
4. A column is added in the source database. The Iceberg table gains it; the pipeline doesn't break.
5. The source's lag and errors show on the project's pages and the Health page, and alert (6.7) if it stops.

**Requirements**
- R8.1 Sources per project: databases by change capture (Debezium, through Kafka or straight to Iceberg), with credentials only as Secrets.
- R8.2 Inserts, updates and deletes applied to Iceberg tables in minutes, with schema changes followed.
- R8.3 The source as the tables' lineage in the catalog; the project's access rules on them.
- R8.4 Lag and errors per source, shown and alerted.

**Open questions**
- Which databases first?
- Kafka in the platform (for change capture and, later, streaming), or Debezium Server writing to Iceberg without it?

### 8.2 An application's data

1. Marketing's leads are in a SaaS application (a CRM). Their project adds a source: the application, the objects (leads, campaigns), and a token given once and kept as a Secret.
2. The connector (dlt or Airbyte) copies them into the project's Iceberg tables on a schedule, incrementally.
3. They're in the catalog with their lineage, the schedule is a pipeline like any other, and a failure alerts.

**Requirements**
- R8.5 Connectors for applications and files (dlt or Airbyte), configured per project, scheduled by Airflow, writing to the project's Iceberg tables.
- R8.6 Connectors' state kept, so each run copies only what's new.

**Open questions**
- Which applications first?
- dlt (a library, run in Airflow, small) or Airbyte (many connectors, a service of its own to run)?

---

## Phase 9: governed AI

### 9.1 sam asks a question

1. sam asks an assistant (Claude, signed in to the platform as sam): "What was revenue by region last quarter, and why did EU fall?"
2. The assistant asks the platform's MCP server for the `revenue` metric by region and month. It runs as sam: sam sees the EU only, as on the dashboards, and the numbers match them to the cent.
3. The answer says which metric definitions it used, and links to them.
4. sam asks for customers' card numbers. They come back masked, as for sam anywhere else.
5. The question's cost (LLM tokens, Trino time) counts against the `sales` project's budget.

**Requirements**
- R9.1 Every answer from the semantic layer, run as the person, with its definitions cited.
- R9.2 An LLM gateway (LiteLLM) in front of the models: per project and per person budgets, cost recorded, and guardrails (no secrets or masked values sent out).
- R9.3 Agents' calls audited with the person behind them (R6.11).

### 9.2 An agent of the project's own

1. bob sets up a nightly agent for `sales`: it reads the failed checks and posts a summary to the team's chat.
2. It gets an identity of its own, a service account in the project, and may only read the project's checks and tables.
3. The project's owner approves it, and it appears in the project's list of agents, with what it may use.
4. One night it loops. At its budget, the gateway stops it, and bob gets an alert.

**Requirements**
- R9.4 Agents registered in a project: identity, owner, tools and data allowed, budget.
- R9.5 Approval before an agent runs; revoking it takes effect at once.
- R9.6 Spend caps that stop an agent, with an alert.

### 9.3 A metric change, tested

1. bob changes `revenue` to exclude refunds, in a pull request.
2. The semantic layer's tests run (as in 7.3): known answers for known questions (EU revenue for September), and a set of plain-language questions whose expected metric calls are checked.
3. The pull request shows which answers changed. After merge, sam's next question gets the new definition.

**Requirements**
- R9.7 Tests for the semantic layer: expected values, and expected metric calls for plain-language questions.

### 9.4 alice writes SQL with help

1. On the SQL page, alice asks the assistant for "orders by region per week with a 4-week moving average".
2. It writes the SQL from the project's tables and their columns (the catalog), and she runs it as herself.
3. It never sees data she can't, and its suggestions use only tables she can read.

**Requirements**
- R9.8 An assistant in the SQL page (and for pipeline code), given the project's schema and run under the person's access.

### 9.5 A new column of personal data

1. A source (8.1) gains a column, `customer_email`. Nobody wrote a policy for it.
2. The catalog's classification finds it holds email addresses and tags it `personal`. The tag goes to Ranger.
3. Ranger's policy for `personal` (masked, except for those allowed) applies at once: alice sees it masked in SQL, dashboards, notebooks and the assistant's answers.
4. The project's owner sees the new tag, and can confirm it or correct it.

**Requirements**
- R9.9 Classification of columns in the catalog (personal, financial, health), automatic and confirmed by people.
- R9.10 Ranger's policies by tag, with the catalog's tags kept in step, so a newly tagged column is protected without a policy of its own.

---

## Phase 10: usage and cost per project

### 10.1 frank's monthly view

1. frank opens the Usage page for `sales`: this month's Trino CPU time, Spark time, storage (by bucket and table), LLM spend, and the trend against last month.
2. It shows what drives it: the top pipelines, people and queries.
3. At 80% of the month's budget, frank and the project's owner get an alert.

**Requirements**
- R10.1 Usage attributed to projects: Trino (query to project, through the person or service account), Spark, Buckets storage, the LLM gateway.
- R10.2 A rate card (what a CPU-hour, a GB-month and a token cost), set by the install.
- R10.3 Budgets per project, with alerts.

### 10.2 A runaway query

1. dana's notebook runs a query in a loop. Trino's load climbs.
2. The platform alerts priya, naming the project, the person, and the query.
3. Trino's resource group for `sales` already caps its share, so other projects are unaffected; priya can stop the query from the Health page.

**Requirements**
- R10.4 Trino resource groups per project.
- R10.5 Alerts naming the project, person and query; a way to stop it.

---

## Phase 11: self-service and ML

### 11.1 alice cleans a spreadsheet without code

1. alice uploads a supplier's CSV to the project's landing area. The platform previews it.
2. She builds a recipe: rename columns, parse dates, drop test rows, remove duplicates, join to `orders` by order ID. Each step previews its result. The SQL it generates is shown beside it.
3. She saves it as a table in `sales`. Its lineage appears in the catalog, and it can run on a schedule.
4. Saved, it's code in the project's repo (as a dbt model), so bob can review and change it like any other.

**Requirements**
- R11.1 Uploads to the project's landing area, with a preview.
- R11.2 A recipe builder: steps, each previewed, generating SQL (or a dbt model) kept in the project's repo.
- R11.3 The result in the catalog with its lineage, and schedulable as a pipeline.

### 11.2 An app for sam

1. alice makes a small app from the `revenue` metric: a region filter, a table and a chart.
2. She shares it with sam, who opens it from Home; it shows what sam may see.

**Requirements**
- R11.4 Simple data apps per project, built from metrics and tables, run as the person viewing them.

### 11.3 dana puts a model into use

1. dana trains a churn model in a notebook. Each run is tracked (MLflow): parameters, metrics, the data's Nessie commit.
2. She registers the best one and asks for approval. The project's owner sees its evaluation and approves.
3. It's used by a pipeline that scores customers nightly, and its drift is monitored.

**Requirements**
- R11.5 MLflow per project, its runs tied to the data's commit.
- R11.6 A model registry with approval before use.
- R11.7 Batch scoring as a pipeline; drift monitored in Grafana.

---

## Not yet placed

### U.1 Erase a customer

1. A customer asks to be forgotten. priya (or a request from the company's privacy tool) asks the platform to erase customer `C-1042` everywhere.
2. The platform finds where they are, from the catalog's tags and lineage: `orders`, `customers`, a model's training data, two buckets of files.
3. It deletes the rows (Iceberg deletes), expires the snapshots that still hold them and removes the old files, and does the same on the data branches. Backups older than the retention window age out.
4. It records what it erased and when, for the auditor, without recording the data itself.

**Requirements**
- R-U.1 Erasure across a project's tables and files, found through tags and lineage, carried through snapshots, branches and files, and recorded.
- R-U.2 Retention per table (delete rows older than a set age), applied by the maintenance of 6.5.

### U.2 A new team, from a template

1. A new team asks for a project. An administrator makes it from the company's template: its repo (with a sample pipeline, dbt model, checks and metric), its buckets, its groups, its notification channel.
2. The team signs in to a project that works on day one, and changes what they need.

**Requirements**
- R-U.3 Project templates: a spec and a repo to copy, kept by the platform's administrators.
- R-U.4 Shared libraries (code, recipes, checks) that projects use without copying.

### U.3 sales publishes orders; marketing uses them

1. sales publishes `orders_by_region` as a data product: an owner, a description, a contract (columns, freshness within a day, its checks), and a version.
2. marketing finds it in the catalog and asks for it. sales' owner approves; marketing's people read it from their own project, under sales' rules, without a copy.
3. sales changes a column in a way the contract doesn't allow: their pull request (7.3) fails, saying marketing depends on it. A compatible change becomes a new version, and marketing is told.

**Requirements**
- R-U.5 Data products: a published table with an owner, a contract and versions, found in the catalog.
- R-U.6 Access granted between projects, on request, read in place.
- R-U.7 Contracts checked in pull requests, and those who depend on a product told of its changes.

## To decide, with the first teams

- **Streaming:** does anything need data in minutes, not hours? Then Kafka (already needed for 8.1 perhaps), with Flink or Spark streaming into Iceberg.
- **Data science:** Python environments per project, and GPUs?
- **Managing as code:** a CLI for projects, a Terraform provider, a published API?
- **Disaster recovery:** a second site, with Buckets' site replication and a standby catalog?

---

## From storyboards to phases

| Phase | Storyboards | Its gate tests |
|---|---|---|
| 6 | 6.1–6.7 | 6.2 on the real cluster: an upgrade with no lost projects, people or settings |
| 7 | 7.1–7.5 | 7.3: a pull request runs on its own data branch, a good one merges, a bad one is refused |
| 8 | 8.1–8.2 | 8.1: a database table's changes reach its Iceberg table in minutes, under the project's rules |
| 9 | 9.1–9.5 | 9.1 and 9.2: within the person's or agent's rules and budget, every call audited; 9.5: a new personal column masked everywhere |
| 10 | 10.1–10.2 | 10.1: usage and cost show per project, and a budget alerts |
| 11 | 11.1–11.3 | 11.1: a clean table from a spreadsheet without code, in the catalog with its lineage |

Each storyboard becomes an end-to-end suite, as the phases before did: the people and agents above, against a real install, in CI.
