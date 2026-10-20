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
| **a client** | A client or partner who receives the company's files | Files on time, complete, and only what the contract allows; nothing else. |
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

**Decided (2026-10-16)**
- The identity provider will be Entra ID. Until real users come, the platform stays in development: the example people stay, and corporate sign-in (R6.1) and turning the examples off (R6.2) wait.
- The realm's ownership is split: the platform keeps its own parts (the tools' clients, its service accounts, its signing key, the settings it relies on) and applies them at each upgrade; the company owns the people, their sign-in and its policies, which an upgrade never writes (plan, "Phase 6: Keycloak for real use, how").

**Open questions**
- Do groups come from Entra ID (and the project maps them), or are they the platform's own?
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

**Built (2026-10-17):** the audit log is Ranger's Solr (`ranger_audits`), not Loki: Ranger already records there every access Trino allows or refuses, with the policy that decided it, and platformd now writes its own records beside them in Ranger's fields (who signed in or was turned away; a project created, changed, saying which parts, or deleted; a pipeline run started; an alert muted or tested; maintenance asked for), so Ranger's own Audit page shows both. Solr keeps them on a volume of its own (`audit.storage`); the platform's records are kept `audit.keep` (a year), Ranger's its default 90 days. A project's Audit tab, for its editors, shows its own records and every access to its tables; Administration › Audit shows the platform's, filtered by person, text, outcome, source and dates, and exports CSV. The `audit` suite checks a change, a refusal, who may read it, the export, and a sign-in. Not yet: Keycloak's and Buckets' events (R6.9), agents named apart from the person they act for (R6.11: today an agent's query is the person's), which tool a query came from, and how long auditors need records kept (open: a year is a guess).

### 6.4 Something is wrong (this happened, 2026-10-14)

1. After an upgrade, the catalog's search store can't mount its volume on its new node. Nothing in the platform says so: the catalog's pages just fail.
2. *What should have happened:* the Health page shows the catalog as down, with the reason (the volume won't mount, from Kubernetes' events) and what depends on it (the Data tab's lineage, search, checks). priya gets an alert.
3. When it recovers, the page says so, and the alert clears.

**Requirements**
- R6.12 A Health page: each service, its state and the reason from its events and logs, and the platform features each one serves.
- R6.13 Alerts to administrators (email, chat) for a service down, a setup step failing, a certificate near expiry, storage near full.

**Built (2026-10-16):** platformd checks every service every 15 seconds (its own health endpoint; on Kubernetes the reason from its pods' state and latest warning event), on an Administration › Health page, with a notice for everyone of what's affected; alerts to the administrators in Teams (a channel's Workflows webhook, as an Adaptive Card) and by email when a service goes down (two failed checks) or comes back, and in a certificate's last 14 days; muting per service; a test alert; the checks as Prometheus metrics. Where alerts go is the platform's Secret `storscale-alerts`. platformd also delivers Prometheus' monitoring rules as they fire and resolve (storage near full, a drive offline) and, on Kubernetes, a setup step that gave up. The project alerts of 6.7 followed.

### 6.5 Tables that stay fast

1. `sales.orders` gets a few thousand rows every ten minutes, from a pipeline and from change capture (8.1). After a month it's tens of thousands of small files and thousands of snapshots; alice's queries slow down, and storage keeps growing.
2. *What should happen:* each night the platform compacts the project's tables, expires snapshots older than the project's setting (say 7 days, more for tables that need time travel), and removes files nothing refers to, including those left by the branches of merged and closed pull requests (7.3).
3. The Health page shows each table's files, snapshots and size, and when it was last maintained; a table that grows out of line is flagged.

**Requirements**
- R6.14 Iceberg maintenance per project, scheduled: compaction, snapshot expiry, orphan file removal, and the branches of closed changes dropped.
- R6.15 Retention of snapshots per project and table (time travel kept as long as asked, no longer).
- R6.16 Each table's health (files, snapshots, size, last maintenance) on the Health page.

**Built (2026-10-17), the first part:** each night at `maintenance.at` (UTC) platformd compacts every project's small files (Trino's `optimize`), one project at a time, as Trino's `maintenance` account (Ranger lets it alter and read every table: `optimize` scans the table it rewrites). A project may pause it. Each table's Storage tab shows its files, size and rows and what maintenance last did; editors ask for a run now. A run that fails alerts the project (6.7), and the runs are Prometheus metrics. The `maintenance` suite checks it.

**Found while building it:** Iceberg's own snapshot expiry and orphan-file removal don't apply here. Nessie keeps each table's history as its own commits, on branches and tags that every project shares, and turns those procedures off (a file may be another branch's). What no branch or tag needs any more is collected by Nessie's GC (`nessie-gc`), across the whole install. So how far back the data as it was goes is the install's setting, not a project's (R6.15 becomes per install), and a month-end close kept beyond it is a Nessie tag (6.10), which GC keeps. **Built after (2026-10-17):** Nessie's GC (`nessie-gc`) nightly, after the compaction, as Nessie's own Buckets account: history older than `maintenance.history.keep` (a week) and files no branch or tag needs, deleted, across the install; a CronJob on Kubernetes, whose failed runs the Health page reports, and a daily loop in Compose. The `maintenance` suite checks it deletes the files only old history used and a file nothing used, rows intact. Its live set is kept in memory (Nessie suggests a database for large catalogs). Not yet either: the tables' health on the administrators' Health page, and the last runs kept across a restart of platformd (the tables' files, read from the catalog, are).

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

**Built (2026-10-16):** platformd looks at every project each minute: its pipelines' latest runs, its tables' checks, and its freshness targets (`tables.freshness`: a table, and how long it may go unchanged, as `30m`, `24h` or `7d`, measured from its current snapshot in Nessie). A project's `notify` says where it's told: `teams`, one of the project's connections holding a channel's Workflows webhook (`webhook`), and `email`, through the install's mail server (`storscale-alerts`). A channel is told when an issue starts, and when a failing check or a stale table comes right; a channel just named is told everything wrong at once. The project's Overview lists what's wrong now and where it's told; editors (and administrators) mute one issue for a day. Administrators set `notify` and freshness in the project's editor. The `project_alerts` suite checks a stale table reaching a project's Teams channel, the page, muting, and the resolve. Not yet: the failing task and its error in a pipeline's alert (the run is named and linked), the failing rows of a check, stale tables shown in the catalog and on the Data tab, Slack, and mutes kept across a restart of platformd (they're recorded in its log).

### 6.8 One project can't slow the others

1. dana's notebook runs a query in a loop, and Trino's load climbs. Meanwhile marketing's nightly pipelines start.
2. `sales`' share of Trino is capped (its resource group: how many queries at once, how much memory), so marketing's queries run at their usual speed. dana's extra queries wait in sales' own queue.
3. The platform alerts priya, naming the project, the person and the query. priya, or one of sales' editors, stops it from the Health page.
4. sales' notebooks and Spark jobs have the project's limits too (CPU, memory, how many at once); its Airflow has the memory the project gives it; its buckets a size quota. Asking for more than a limit is refused with a message naming the limit, not a crash.
5. When the team grows, priya raises sales' limits in its spec. A project that names none gets the install's defaults.

**Requirements**
- R6.22 Limits per project, in its spec, with the install's defaults: Trino (queries at once, queued, memory) through a resource group per project; notebooks and Spark (CPU, memory, how many) through Kubernetes quotas; its Airflow's memory (built); its buckets' size (Buckets' quotas).
- R6.23 Alerts naming the project, the person and the query, and a way to stop it.
- R6.24 A limit, when met, is said plainly where it's met (the SQL page, the notebook, the pipeline's run), naming the limit.

**Decided (2026-10-16)**
- A query counts against a project when one of its members names it (Trino's client tag `project:<name>`, which the platform's SQL page and metrics set), and all of the project's own pipelines' queries do; everything else counts against its person. A person in two projects chooses by naming one; naming a project they aren't in counts against themselves.
- Notebooks and their Spark sessions are a person's, not a project's (one notebook serves all of a person's projects): they have a person's limits, memory and CPU.

**Built (2026-10-16):** Trino's resource groups, kept by the operator in a database Trino reads every second (a group per project, per person, and one for the install); each project's bucket's size; a person's notebook's CPU beside its memory; the `limits` suite.

**Open questions**
- Hard caps, or fair shares that lend idle capacity? (Built: hard caps, with Trino's fair scheduling inside each.)

### 6.9 Erase a customer

1. A consumer asks to be forgotten. The company's privacy tool asks the platform (by its API), or priya does, to erase customer `C-1042` everywhere.
2. The platform finds where they are. Each project says which of its tables and file prefixes hold consumer data, and which column identifies the person (`customer_id`); lineage adds what was made from them: `orders`, `customers`, a model's training data, two buckets of files.
3. It deletes the rows (Iceberg deletes), expires the snapshots that still hold them and removes the old files, and does the same on the data branches. Backups older than the retention window age out.
4. It records what it erased and when, for the auditor, without recording the data itself, and lists the deliveries (9.2) that carried the person's data, so the company can tell their recipients.
5. Separately, each night, rows older than a table's retention (say 25 months for campaign responses) are deleted the same way.

**Requirements**
- R6.25 Each project declares where its consumer data is: the tables, the column that identifies a person, and the files' prefixes; the catalog shows it. Classification (12.5) later finds what's undeclared. (Built 2026-10-18: the tables and their person's column, `tables.people`; not yet the files' prefixes, or the catalog showing it.)
- R6.26 Erasure by a person's ID across the declared tables and what lineage says came from them, carried through snapshots, data branches and files, within the law's deadline, and recorded without the data; an API for the privacy tool. (Built 2026-10-19 for the declared tables, with the record and the deadline; the old versions age out with the history window; lineage's tables, files, data branches and the tool's own sign-in are left.)
- R6.27 Retention per table (rows older than a set age deleted), applied by the maintenance of 6.5. (Built 2026-10-19: `tables.retention`.)
- R6.28 The erasure's record lists the deliveries that carried the person's data (R9.6). (Built 2026-10-19.)

**Decided (2026-10-19):** the CCPA, as amended by the CPRA: a deletion request answered within 45 days (one 45-day extension with notice), verified first, its record kept at least 24 months, the deliveries that carried the person's data named so recipients can be told (see plan.md, "Decisions").

**Open questions**
- How long until erased data ages out of backups, and is that acceptable to the privacy team (the CPRA allows deleting from backups when they're next used)?
- Who may ask for an erasure: the privacy team's tool only, or administrators too?

### 6.10 The data as it was

1. A client disputes last Monday's file: its totals don't match their system. The account manager opens the delivery on the Deliveries tab (9.1) and chooses **Open the data as sent**. The SQL page opens the tables as they were when the file was made, from the snapshot the delivery recorded, and the file can be made again from them exactly.
2. Finance signed off the September close on October 3. In January an auditor asks why September's revenue now differs from the report. An analyst opens `orders` as of the close (a tag the close made: `close-2026-09`), compares it with today, and sees which rows changed after it, and which pipeline runs changed them.
3. A table's page shows its history: each change, when, by which pipeline run or person, and how many rows it added, changed and removed.
4. History older than the table's retention (6.5) is gone, and the page says so. A tag (a close, a delivery) is kept as long as the project says, so year-end figures stay reproducible.
5. Looking back never widens access: a reader who sees EU rows and masked card numbers today sees the same in last year's data.

**Requirements**
- R6.29 A table's history on its page: each change (snapshot), when, by which run or person, and the rows it added, changed and removed.
- R6.30 Data as of a date, a tag or a snapshot, on the SQL page, in notebooks and dashboards, under today's access rules (masked stays masked, filtered stays filtered).
- R6.31 Tags for moments that matter (a month-end close, a delivery), made by people or by pipelines, kept for a set time beyond the table's retention.
- R6.32 Each delivery records the snapshot of every table it read, so its files can be made again exactly (with R9.5).

**Built (2026-10-17):** a table is read as it was at a commit of the catalog, `"orders#<commit>"`, under its readers' rules (the operator's Ranger policies name it too). A project's tags (`tables.tags`) keep moments as long as asked: its editors make and remove them on its Tags tab, the operator keeps Nessie's tags as they say, nightly maintenance removes those past their keep, and the audit log records each. Each table's History tab lists its changes (when, rows added and removed, who wrote it, from the audit log, and the tags on it), each readable as it was; the SQL page reads the project's tables as of a tag, a time or a commit. Not yet: a picker in notebooks and dashboards (they name the table at its commit), deliveries recording their commit (R6.32, with 9.1), and the open questions above.

**Open questions**
- Erasure against kept history: erasing a person (6.9) must reach old snapshots, but a tagged close is kept so it can be reproduced. Is a close kept with personal columns erased, or with the person's rows removed and the totals noted as changed? This needs the privacy team's and Finance's answer together.
- How long are closes kept (finance records often seven years), and at what storage cost?
- Tags across the whole catalog (Nessie: one tag for every table at the close), or per table (Iceberg)?

### 6.11 What a table holds (added 2026-10-20)

1. alice opens `orders` on the Data page: each column's share of nulls, distinct values, min and max, a histogram; the table's row count and its growth. The catalog profiled it last night.
2. She adds a check: `status` takes only `open`, `shipped` or `cancelled`; `amount` is between 0 and 100000. The next run of the pipeline fails on a row outside them, and the project is alerted.

**Requirements**
- R6.33 Profiling per table (OpenMetadata's profiler), run by the catalog's round or the nightly maintenance, shown on the Data page and in the catalog.
- R6.34 More kinds of check in `tables.checks`: accepted values, a range, a regex, a referential check (a column's values exist in another table's), and a SQL assertion (a query that must return no rows).

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
- R7.6a A project's DAG code, as it is parsed and as it runs, reads only its own connections: not another project's, nor the platform's secrets (plan, "Keeping projects' DAG code apart").

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
- Which git hosts first? **GitLab** (the company's; decided 2026-10-18).
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

### 7.6 bob gives the platform a credential

1. sales' repo is private. On sales' Connections tab, bob adds a connection: its name (`sales-dags`), its kind (a git token; an SSH key; later a database user, an SFTP server, a PGP key), and the token, pasted once.
2. The platform keeps it as a Secret of sales' own; the tab shows its name, kind, who added it and when, never its value. bob picks it for the project's pipelines repo from a list.
3. Readers see the list, but can't add or change it; carol sees nothing.
4. bob rotates the token by pasting a new one; Airflow has it within a minute. He removes an old connection; anything still naming it says so.
5. The same tab holds the database users of 8.1 and 8.3, and the delivery targets and keys of 9.1.

**Requirements**
- R7.18 A Connections tab per project: editors add, replace and remove credentials (a token; a username and password; an SSH key with its host's key; a PGP key); values written once and never shown again; who and when in the audit log.
- R7.19 The operator keeps them as Secrets the project owns (labelled with it, removed with it), so nobody needs access to the cluster.
- R7.20 A project names only its own connections: another project's Secret, or the platform's, is refused. (Built, 2026-10-16: a Secret must carry the label `platform.storscale.io/connection=<project>`.)

### 7.7 dana schedules her notebook

1. dana's notebook `churn_features.ipynb` works. In JupyterLab she chooses **Schedule**: daily at 05:00, in project `sales`.
2. The platform adds a small pipeline to sales' repo, as a pull request if the branch is protected, that runs the notebook (papermill) in the project's own Airflow, as its service account.
3. Each run keeps its executed notebook (outputs, errors) in the project's bucket, linked from the run on the Pipelines tab.
4. A failed run tells dana (6.7), with the cell that failed.

**Requirements**
- R7.21 Scheduling a notebook from JupyterLab: a pipeline in the project's repo that runs it, reviewed like any other change.
- R7.22 Each run's executed notebook kept, and linked from the run.

**Open questions**
- The notebook's Python packages: the image's, or an environment per project (see "To decide")? The image's for now (built 2026-10-18): the scheduled run has the notebook image's packages.

### 7.8 dana works in VS Code (added 2026-10-20)

1. dana picks **VS Code** instead of JupyterLab when her notebook server starts. It opens on the project's repo, cloned, signed in as her; a commit there is a merge request like any other.
2. Her notebook imports scikit-learn, statsmodels and geopandas; they're in the image, with the libraries the common analyses and models need.

**Requirements**
- R7.23 code-server (VS Code in the browser) as a JupyterHub profile beside JupyterLab, the project's repo cloned into it with its connection, as the person.
- R7.24 The notebook image carries the analysis and ML libraries (scikit-learn, statsmodels, scipy, lightgbm, xgboost, FLAML, mlflow, geopandas, networkx), and Superset its map charts.

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

**Decided (2026-10-17):** Debezium Server writing to Iceberg, without Kafka (Kafka when streaming needs it); PostgreSQL, SQL Server and MySQL first, Oracle after.

### 8.2 An application's data

1. Marketing's leads are in a SaaS application (a CRM). Their project adds a source: the application, the objects (leads, campaigns), and a token given once and kept as a Secret.
2. The connector (dlt or Airbyte) copies them into the project's Iceberg tables on a schedule, incrementally.
3. They're in the catalog with their lineage, the schedule is a pipeline like any other, and a failure alerts.

**Requirements**
- R8.5 Connectors for applications and files (dlt or Airbyte), configured per project, scheduled by Airflow, writing to the project's Iceberg tables.
- R8.6 Connectors' state kept, so each run copies only what's new.

**Decided (2026-10-18):** dlt, run in the project's Airflow (Airbyte is a service of its own). First: any REST API (dlt's `rest_api`), a database by a cursor column, files in the project's bucket; applications' own sources (HubSpot, Salesforce) by demand.

### 8.3 Query a database where it is

1. Finance's budget figures live in the company's SQL Server. alice needs them beside `orders` today, without a copy.
2. bob adds the database to sales as a query source: its address, a read-only user from the Connections tab (7.6), and which schemas.
3. It appears in Trino as a catalog of sales' own (`sales_finance`); alice joins it with `orders` on the SQL page and in Superset.
4. Ranger's rules apply to it as to the lakehouse: sales' readers see it, others don't; columns may be masked.
5. When querying it in place is too slow, or too heavy for the source, bob changes it into a source copied by change capture (8.1).

**Requirements**
- R8.7 Query sources per project: databases (PostgreSQL, SQL Server, Oracle, MySQL) as Trino catalogs, read-only, with credentials from the project's connections.
- R8.8 The project's access rules on them, in Ranger, and their tables in the catalog.
- R8.9 Limits on what a query may pull from a source (6.8), so the source's own users aren't slowed.

**Decided:** Trino's dynamic catalogs (made while it runs, by the platform operator), the connection in files Trino reads, never in the statement (docs/plan.md).

### 8.4 One-click connectors

1. Marketing wants Salesforce in their project. On the project's Sources tab, bob picks **Salesforce** from the platform's connectors (Salesforce, Gong, HighSpot, SAP S/4HANA, SAP HANA and BW, any REST API, any database), signs in to Salesforce once (OAuth, or a token on the Connections tab), and ticks the objects he wants: accounts, opportunities, campaigns.
2. He picks when (hourly, nightly) and how much history. The platform writes the import (8.2: dlt, in the project's Airflow) and its first run starts.
3. The tables appear in the project with their columns described, lineage to Salesforce, and checks that each run copied something; Salesforce's deleted records are marked deleted.
4. Gong and HighSpot work the same way, from connectors the platform keeps (on dlt's REST support). SAP comes two ways: S/4HANA's OData services, through the company's SAP gateway; or the HANA or BW database itself, by change capture or by query (8.1, 8.3).
5. A connector's own update (a new Salesforce API version) is the platform's to make, not the project's.

**Requirements**
- R8.10 A gallery of named connectors, each with a form: sign-in (OAuth or the Connections tab), the objects to copy, the schedule, the history to start from. No code, and no spec written by hand.
- R8.11 Connectors kept by the platform (versioned, tested against each application's sandbox), on dlt: Salesforce (dlt's own source), Gong and HighSpot (on its REST support), SAP S/4HANA (OData), SAP HANA and BW (databases: 8.1 and 8.3).
- R8.12 Each connector's tables described in the catalog, with lineage to the application, and checks on each run (rows copied, deletes carried).

**Decided (2026-10-18):** Salesforce first (dlt has a source); Gong and HighSpot next; SAP both ways, S/4HANA by OData and HANA or BW as a database.

**Open questions**
- The company's SAP: which systems, and is its OData gateway reachable from the platform's network?
- Gong's and HighSpot's API access: the company's plan with each must include it.

---

## Phase 9: getting data out

The company delivers files to clients and partners, and some carry consumer data. Getting them out on time, complete, and with only what's allowed, matters as much as getting data in.

### 9.1 A file delivery to a client

1. Each Monday, client Acme gets last week's campaign results: a CSV file per region, compressed and PGP-encrypted with Acme's key, on Acme's SFTP server, with a manifest (files, rows, checksums).
2. bob sets it up in sales: a delivery, its query or table, the format (CSV, Parquet, fixed width), the files' names (`acme_results_{date}_{region}.csv.gz.pgp`), the schedule, and its target, from the Connections tab (Acme's SFTP host, user and key, and Acme's public key).
3. Before its first run the delivery is approved. The approval shows which columns go, and which are personal (6.9's declaration, later 10.5's tags): a delivery with personal data needs the project owner's approval and that of the company's named data-protection approver. A change to what it sends needs approving again, and doesn't run until it is.
4. On Monday at 06:00 it runs as a pipeline in the project's own Airflow, as the project's service account, under the project's access rules. It checks first: rows within the expected range, every region present, the tables fresh. A failing check sends nothing, and alerts (6.7).
5. The project's Deliveries tab shows each run: what was sent, to whom, sizes and checksums, and the transfer's result. A failed transfer is retried, then alerts.

**Requirements**
- R9.1 Deliveries per project: a query or table, a format (CSV, Parquet, JSON, fixed width), compression, file names, a schedule; run as pipelines in the project's Airflow.
- R9.2 Targets: SFTP (with the host's key checked), S3-compatible buckets of other companies; Azure Blob and Google Cloud Storage later. Credentials and keys from the project's connections (7.6); PGP encryption; a manifest with checksums.
- R9.3 Checks before sending (row counts, completeness, freshness, the project's checks): a failed check sends nothing.
- R9.4 Approval of what a delivery sends, and again when that changes; personal columns shown, and needing a named approver.
- R9.5 A delivery log: every file sent, to whom, when, its size and checksum, and the snapshot of each table it read (R6.32), kept with the audit log; retries and alerts.

**Open questions**
- Which formats and targets do today's deliveries use, and how are they run now (scripts, an MFT product)? **SFTP and S3 first** (decided 2026-10-18).
- Who is the data-protection approver?

### 9.2 What did we send about this person?

1. A consumer asks what the company shared about them, and with whom; or an erasure (6.9) needs the recipients of their data.
2. priya searches the delivery log by the person's ID: the deliveries that carried their rows, when, to whom, under which approval.
3. The answer goes to the privacy team; with an erasure, the recipients are asked to delete.

**Requirements**
- R9.6 Deliveries of personal data record whose data they carried (the people's IDs, or enough to find them again from the tables' history), for the legal period, searchable by person.

**Open questions**
- A list of IDs per file is itself personal data: is it kept, or found again by running the delivery's query on the data as it was (Iceberg's time travel), which needs the snapshots kept as long? **Found again from the tables, not kept** (decided 2026-10-18): the delivery log holds no personal data; it keeps each run's snapshots, and a search runs the delivery's query on the tables as they were then. So a delivery of personal data keeps the snapshots it read (tagged) for the legal period. Who searches: the data-protection approvers (`deliveries.approverGroups`); how long: a setting, `deliveries.keepFor`, default 7 years (both decided 2026-10-18).

### 9.3 A partner reads in place

1. A partner wants a table every day, but would rather read it than receive files.
2. sales shares `orders_by_region` with the partner: read-only, filtered to the partner's rows, through a credential that expires.
3. The partner reads it with their own tools. Every read is in the audit log; the share ends on its date, or at once when revoked.

**Requirements**
- R9.7 Shares with outside parties: read-only access to a table (or files), filtered, time-limited, audited, revocable at once.

**Open questions**
- Which tools do partners use (Snowflake, Databricks, Spark), and so which protocol: Iceberg's REST catalog, Delta Sharing, or signed links to files?

---

## Phase 10: the business layer

What people work with is the business's own words (Customer, Revenue, Campaign, Booking), defined once, governed, and the same in every tool: SQL, dashboards (Superset, Power BI), the catalog, and agents.

### 10.1 Customer, defined once

1. The data governance group (priya with the domain owners) defines the company's entities and metrics once, in a shared semantic model kept in its own repo: `Customer` (its key, its attributes, which tables it comes from), `Revenue`, `Campaign`, `Booking`, each with an owner, a definition in plain words, and the tests that pin its numbers (12.3).
2. A project uses them rather than defining its own: `sales` builds on `Customer` and `Revenue` and adds `Pipeline coverage`, its own metric, beside them.
3. alice searches the platform for "customer" and finds the entity, what it means, who owns it, the metrics on it and the projects using it, not a list of tables. She asks for Revenue by Customer segment and gets it, under her own rules (EU only).
4. A change to `Revenue`'s definition is a pull request to the shared model, reviewed by its owner, tested, and released (7.4) to every project at once.

**Requirements**
- R10.1 A company-wide semantic model (entities, dimensions, metrics) with owners and plain-language definitions, kept in git and released like a project (7.4); projects build on it and add their own.
- R10.2 The platform shows entities and metrics first, tables second: search, a page per entity and metric, and where each is used.
- R10.3 The catalog's glossary terms are the semantic model's entities and metrics, kept in step, linked to the tables and columns they come from.

### 10.2 Dashboards on the same definitions

1. alice opens Superset in the platform. Its datasets are the semantic model's (Customer, Revenue by month): the metrics come defined, so her chart's Revenue is the governed Revenue, to the cent.
2. sam opens Power BI. He connects to the platform with his company sign-in, picks Revenue and Customer from the same model, and builds his report; he sees the EU only, as everywhere.
3. When `Revenue`'s definition changes (10.1), both tools show the new numbers without anyone rebuilding a dataset.

**Requirements**
- R10.4 Superset's datasets made from the semantic model and kept in step with it, per project and the shared model.
- R10.5 Power BI over the platform: through Trino (DirectQuery) with the company's sign-in (Keycloak, Entra ID), on views or a semantic endpoint generated from the model, under Ranger's rules for the person.
- R10.6 One definition: a dashboard's metric and the semantic layer's answer agree, checked by a test.

### 10.3 A certified data product, found

1. sales publishes `orders_by_region` as a data product: an owner, a description, a contract (columns, freshness within a day, its checks), a version, and the entities it holds (Customer, Revenue).
2. The governance group reviews it and certifies it: the catalog shows it as certified, and search ranks certified products first.
3. marketing finds it, asks for it, and sales' owner approves: marketing reads it in place from their own project, under sales' rules, without a copy.
4. sales changes a column in a way the contract doesn't allow: their pull request (7.3) fails, saying marketing depends on it. A compatible change becomes a new version, and marketing is told.
5. If the company keeps Collibra as its catalog of record, certified products, the glossary and lineage are sent there too.

**Requirements**
- R10.7 Data products: a published table with an owner, a contract, versions and the entities it holds, found in the catalog.
- R10.8 Certification by the governance group, shown wherever the product appears, and lost when its contract breaks.
- R10.9 Access granted between projects, on request, read in place.
- R10.10 Contracts checked in pull requests, and those who depend on a product told of its changes.
- R10.11 A sync to Collibra (products, glossary, lineage), if Collibra is the company's catalog of record.

**Open questions**
- OpenMetadata or Collibra as the catalog of record? (Not decided, 2026-10-18: OpenMetadata now, the Collibra sync designed as optional.)

### 10.4 DevAssist asks the same data

1. A developer asks DevAssist (the company's assistant), signed in as them: "Which customers' bookings fell most this quarter?"
2. DevAssist is one of the platform's MCP clients. It finds the Customer and Booking entities and the certified `bookings` product, asks the semantic layer for Bookings by Customer, and gets what the developer may see, under the same rules as on a dashboard.
3. Its answer names the definitions and the product it used, linked. Asked something no governed definition covers, it says so, rather than guessing from raw tables.

**Requirements**
- R10.12 The MCP server's tools reach the company's semantic model and certified data products: find, describe, query (as the person), and cite.
- R10.13 DevAssist, and any assistant the company approves, registered as an MCP client of the platform, signing in as the person (OAuth), its calls audited (12.1).

---

## Phase 11: visual transformation

Pipelines built on a canvas, as in Domo's Magic ETL or Dataiku's visual recipes, that are code underneath: dbt models in the project's repo, reviewed and released like any other change.

### 11.1 alice builds a pipeline on a canvas

1. alice opens the canvas in `sales`. She drags in `orders` and a supplier's CSV she uploaded (with a preview), and adds steps: rename and retype columns, filter test rows, remove duplicates, join to `orders`, group by region and week, pivot, compute a column.
2. Each step shows its result on a sample, and the SQL it makes beside it. A step that would fail (a join on columns of different types) says why before she runs it.
3. She saves it as a table in `sales`: it's a set of dbt models in the project's repo, on a branch, as a merge request (as a scheduled notebook is, 7.7); its lineage, column by column, appears in the catalog.
4. Merged, it runs on a schedule as a pipeline, and its checks run with it.

**Requirements**
- R11.1 A canvas of steps (input, filter, select and rename, type, join, union, group, pivot and unpivot, window, formula, deduplicate, output), each previewed on a sample, in Trino.
- R11.2 Each canvas saved as dbt models in the project's repo, as a merge request, reviewed on a data branch (7.3), released by plan (7.4).
- R11.3 Uploads to the project's landing area (CSV, Excel), with a preview, as inputs.
- R11.4 The result in the catalog with its lineage, schedulable as a pipeline, its checks run with it.

### 11.2 bob changes it in code

1. bob opens alice's pipeline as code: the dbt models the canvas wrote, readable, with a comment per step.
2. He changes one model by hand. The canvas shows that step as code from then on, and the rest as steps.
3. A canvas step on the semantic model's terms (Customer, Revenue) uses their governed definitions (10.1).

**Requirements**
- R11.5 The canvas and the code are the same thing: a canvas opens from the repo's models, and a step changed by hand stays code.
- R11.6 Large data later: the same canvas on Spark, for what Trino can't do in one query.

### 11.3 alice cleans a column, and asks a model about another (added 2026-10-20)

1. alice adds a **prepare** step: trim `customer_name`, title-case it, parse `ordered_on` as a date, fill `region` with `unknown`. Each is a named processor on a column, compiled to SQL like the formula step; the preview shows the result.
2. She adds an **LLM** step on `complaint_text`: classify each row as `billing`, `delivery` or `product`. It runs through the gateway on the project's key, within its budget and under the guardrail; the preview shows a sample's labels and what it cost.
3. bob writes a step in Python for what SQL can't say; it runs in the project's Airflow.

**Requirements**
- R11.7 A prepare step: named processors on columns (trim, case, split, parse a date or number, fill nulls, extract by regex, replace, round, bucket, standardize), the common forty first, each compiled to Trino SQL. (Built 2026-10-20: 42 processors in five groups, in place or as a new column, typed; the `flows` suite runs one.)
- R11.8 An LLM step: classify, extract or summarize a column through the gateway (12.1), on the project's key and budget, under the guardrail, with the prompt kept in the model's file; the preview says what a sample cost.
- R11.9 A Python step beside R11.6's Spark: a model written in Python, run by the project's Airflow, its columns declared.

---

## Phase 12: governed AI

### 12.1 sam asks a question

1. sam asks an assistant (Claude, signed in to the platform as sam): "What was revenue by region last quarter, and why did EU fall?"
2. The assistant asks the platform's MCP server for the `revenue` metric by region and month. It runs as sam: sam sees the EU only, as on the dashboards, and the numbers match them to the cent.
3. The answer says which metric definitions it used, and links to them.
4. sam asks for customers' card numbers. They come back masked, as for sam anywhere else.
5. The question's cost (LLM tokens, Trino time) counts against the `sales` project's budget.

**Requirements**
- R12.1 Every answer from the semantic layer, run as the person, with its definitions cited.
- R12.2 An LLM gateway (LiteLLM) in front of the models: per project and per person budgets, cost recorded, and guardrails (no secrets or masked values sent out). (Built 2026-10-19: the gateway, keys and budgets per project and per agent, spend shown; 2026-10-20: the guardrail, refusing masked values' shapes and the platform's secrets. Per-person budgets left.)
- R12.3 Agents' calls audited with the person behind them (R6.11).
- R12.11 Forbidden terms (the install's words and patterns the guardrail refuses: a codename, a client's name), and a moderation guardrail through the gateway itself (toxicity, by a model), each on by a setting. (Added 2026-10-20.)

### 12.2 An agent of the project's own

1. bob sets up a nightly agent for `sales`: it reads the failed checks and posts a summary to the team's chat.
2. It gets an identity of its own, a service account in the project, and may only read the project's checks and tables.
3. The project's owner approves it, and it appears in the project's list of agents, with what it may use.
4. One night it loops. At its budget, the gateway stops it, and bob gets an alert.

**Requirements**
- R12.4 Agents registered in a project: identity, owner, tools and data allowed, budget. (Built 2026-10-19; the budget with the gateway, and an agent run on its schedule from a notebook in the project's repo, the same day.)
- R12.5 Approval before an agent runs; revoking it takes effect at once. (Built 2026-10-19: approved by an editor other than its registrant; revoked, its client is gone within a minute.)
- R12.6 Spend caps that stop an agent, with an alert. (Built 2026-10-19.)

### 12.3 A metric change, tested

1. bob changes `revenue` to exclude refunds, in a pull request.
2. The semantic layer's tests run (as in 7.3): known answers for known questions (EU revenue for September), and a set of plain-language questions whose expected metric calls are checked.
3. The pull request shows which answers changed. After merge, sam's next question gets the new definition.

**Requirements**
- R12.7 Tests for the semantic layer: expected values, and expected metric calls for plain-language questions. (Built 2026-10-19: the expected values, run from the Metrics page and in a merge request's review; the plain-language questions wait for the gateway, 12.1.)

### 12.4 alice writes SQL with help

1. On the SQL page, alice asks the assistant for "orders by region per week with a 4-week moving average".
2. It writes the SQL from the project's tables and their columns (the catalog), and she runs it as herself.
3. It never sees data she can't, and its suggestions use only tables she can read.

**Requirements**
- R12.8 An assistant in the SQL page (and for pipeline code), given the project's schema and run under the person's access.

### 12.5 A new column of personal data

1. A source (8.1) gains a column, `customer_email`. Nobody wrote a policy for it.
2. The catalog's classification finds it holds email addresses and tags it `personal`. The tag goes to Ranger.
3. Ranger's policy for `personal` (masked, except for those allowed) applies at once: alice sees it masked in SQL, dashboards, notebooks and the assistant's answers.
4. The project's owner sees the new tag, and can confirm it or correct it.

**Requirements**
- R12.9 Classification of columns in the catalog (personal, financial, health), automatic and confirmed by people. (Built 2026-10-19: catalog-sync's detectors and the project's declarations, confirmed or removed on the Data page or in the catalog.)
- R12.10 Ranger's policies by tag, with the catalog's tags kept in step, so a newly tagged column is protected without a policy of its own. (Built 2026-10-19: the operator makes a mask policy of each tagged column, by the install's rules per tag.)

### 12.6 A knowledge bank (added 2026-10-20)

1. bob declares a knowledge bank for `sales`: the contracts in `landing/contracts/` (PDF and text) and the `notes` column of `tickets`.
2. The platform embeds them through the gateway, into pgvector in its own Postgres, under the project's access, and keeps them in step as files and rows change.
3. sam's assistant asks `search_documents` for "early termination terms"; the answer cites the passages, from documents sam may read. A canvas step enriches each ticket with the nearest contract clause.

**Requirements**
- R12.12 Knowledge banks per project (`spec.knowledge`): bucket prefixes and table columns, embedded through the gateway on the project's key, kept in pgvector in the platform's Postgres, refreshed as the sources change.
- R12.13 A `search_documents` MCP tool and a canvas step (nearest passages for a column), as the person or agent, under the project's access; answers cite the passages.
- R12.14 A Q&A app over a knowledge bank and the semantic layer (14.1's apps), as the person.

### 12.7 An agent without a notebook (added 2026-10-20)

1. On the Agents tab, bob registers `triage`: a prompt ("each morning, read the open alerts and the failed runs, and write a summary for the team"), the tools `list_alerts` and `list_runs`, a schedule, a budget. No notebook.
2. alice approves it. The platform's runner loops the model over the tools as the agent, within its budget; each run is kept as its record, as a notebook's would be.
3. bob gives `triage` as a tool to a second agent, `weekly`, which asks it for the week's summaries; both are audited as themselves. He also gives `weekly` the company's ticketing API, declared in the project with the connection to sign in with.

**Requirements**
- R12.15 An agent run by the platform's runner from its prompt, tools and schedule (semanticd's loop over the gateway and the MCP server, as the agent), its run kept as its record; the Agents tab's form is all of it.
- R12.16 An approved agent as a tool of another (`agent:<project>/<name>`), audited as both.
- R12.17 A project's own connector tools: an OpenAPI operation and the connection it signs in with, declared in the project, served by the MCP server to the agents given it.

### 12.8 What the models are doing (added 2026-10-20)

1. frank opens the gateway's monitoring (Langfuse, signed in through Keycloak): every call by project and agent, its latency, tokens and cost, the prompt and answer where the project allows it, and scores from the semantic tests.
2. bob edits `triage`'s prompt there, labels a version `production`, and the runner uses it on the next run.

**Requirements**
- R12.18 Langfuse in the chart, fed by the gateway: traces, latency, tokens and cost per project and agent, scores; its sign-in through Keycloak, each project's people seeing their own.
- R12.19 Prompts managed there (versions, labels), fetched by name by the runner and `buckets_lake.ask()`.

### 12.9 The governance group's view (added 2026-10-20)

1. The governance group opens **Governance**: everything awaiting approval across projects (agents, deliveries, products' access, project applies), the policies in force (masks by tag and who's excepted), the agents and their spend, the models and their state (15), the erasures due.
2. An administrator opens **Agents** (platform-wide): every project's agents, their owners, approval, tools, spend and last run.

**Requirements**
- R12.20 A Governance page for administrators and the governance group, reading what exists: approvals pending, policies, agents, models, erasures.
- R12.21 A platform-wide Agents page.

---

## Phase 13: usage and cost per project

### 13.1 frank's monthly view

1. frank opens the Usage page for `sales`: this month's Trino CPU time, Spark time, storage (by bucket and table), LLM spend, and the trend against last month.
2. It shows what drives it: the top pipelines, people and queries.
3. At 80% of the month's budget, frank and the project's owner get an alert.

**Requirements**
- R13.1 Usage attributed to projects: Trino (query to project, through the person or service account), Spark, Buckets storage, the LLM gateway.
- R13.2 A rate card (what a CPU-hour, a GB-month and a token cost), set by the install.
- R13.3 Budgets per project, with alerts. (The limits that cap a project's use are 6.8's.)

---

## Phase 14: data apps and the API

### 14.1 An app for sam

1. alice makes a small app from the `revenue` metric: a region filter, a table and a chart.
2. She shares it with sam, who opens it from Home; it shows what sam may see.
3. sam's Home for `sales` lists its dashboards, products, apps and metrics, and the Q&A app over its knowledge bank (12.6).

**Requirements**
- R14.1 Simple data apps per project, built from metrics and tables, run as the person viewing them.
- R14.5 A readers' Home per project: its dashboards, products, apps and metrics, as the person. (Added 2026-10-20.)
- R14.6 A Q&A app over a knowledge bank and the semantic layer, one of the apps. (Added 2026-10-20.)

### 14.3 A developer uses the API (added 2026-10-20)

1. A developer reads the platform's API reference (OpenAPI, from platformd's routes) and, with the `storscale` Python package, makes a project's flow, starts a run and reads its log, signed in as herself or as a service account.

**Requirements**
- R14.7 An OpenAPI document generated from platformd's routes, served at `/api/openapi.json` and as a page.
- R14.8 A `storscale` Python package (the API as methods; `buckets_lake` folded in), published with each release.

---

## Phase 15: ML and MLOps (added 2026-10-20; was 14.2)

### 15.1 dana puts a model into use

1. dana trains a churn model in a notebook, from a template for the case. Each run is tracked (MLflow): parameters, metrics, the data's Nessie commit.
2. She registers the best one, writes its card (purpose, data, metrics, limits), and asks for approval. The project's owner sees its evaluation and approves.
3. It's used by a pipeline that scores customers nightly, and its drift is monitored. The catalog shows the model with the tables it was trained on, at their snapshots.
4. The web team calls it as an API, signed in; carol, outside the project, is refused. Each call is audited.

**Requirements**
- R15.1 MLflow per project, its runs tied to the data's commit.
- R15.2 A model registry with approval before use.
- R15.3 Batch scoring as a pipeline; drift monitored in Grafana.
- R15.4 A registered, approved model served as an API in the project's namespace (MLflow's serving image behind the gateway), signed in through Keycloak, under the project's limits, audited.
- R15.5 A model card per registered model, kept with the registry.
- R15.6 Models in the catalog's lineage, with their inputs at their snapshots.
- R15.7 The code path for the common cases: FLAML and the libraries of R7.24, and notebook templates (churn, forecast, propensity) with U.2's project template.

---

## Not yet placed

### U.2 A new team, from a template

1. A new team asks for a project. An administrator makes it from the company's template: its repo (with a sample pipeline, dbt model, checks and metric), its buckets, its groups, its notification channel.
2. The team signs in to a project that works on day one, and changes what they need.

**Requirements**
- R-U.3 Project templates: a spec and a repo to copy, kept by the platform's administrators.
- R-U.4 Shared libraries (code, recipes, checks) that projects use without copying.

## To decide, with the first teams

- **The SQL page:** saved and shared queries, and each person's query history.
- **Streaming:** does anything need data in minutes, not hours? Then Kafka (already needed for 8.1 perhaps), with Flink or Spark streaming into Iceberg.
- **Data science:** Python environments per project, and GPUs?
- **Managing as code:** a CLI for projects, a Terraform provider? (The API's reference and Python package are 14.3.)
- **Disaster recovery:** a second site, with Buckets' site replication and a standby catalog?

---

## From storyboards to phases

| Phase | Storyboards | Its gate tests |
|---|---|---|
| 6 | 6.1–6.11 | 6.2 on the real cluster: an upgrade with no lost projects, people or settings; 6.8: a project at its limits leaves another's queries at their usual speed; 6.9: an erasure carried through snapshots and files, and recorded; 6.10: a delivery's files made again exactly from the data as it was sent |
| 7 | 7.1–7.8 | 7.3: a pull request runs on its own data branch, a good one merges, a bad one is refused; 7.6: a private repo connected without access to the cluster |
| 8 | 8.1–8.4 | 8.1: a database table's changes reach its Iceberg table in minutes, under the project's rules; 8.3: a database queried in place, under the project's rules; 8.4: Salesforce's objects in a project's tables from a form, with no code |
| 9 | 9.1–9.3 | 9.1: a delivery reaches an SFTP server, encrypted, with its manifest, and a failing check sends nothing; 9.2: the log finds what was sent about a person |
| 10 | 10.1–10.4 | 10.1: Customer and Revenue defined once, used by two projects; 10.2: a Superset chart, a Power BI report and the semantic layer agree on Revenue, each under the person's rules; 10.3: a certified product read in place by another project, and a breaking change refused; 10.4: an assistant over MCP answers from the governed definitions, as the person |
| 11 | 11.1–11.3 | 11.1: a pipeline built on the canvas becomes dbt models in a merge request, runs, and shows its lineage; 11.2: one step changed by hand stays code; 11.3: a prepare step and an LLM step run and are previewed, the LLM step's cost counted against the budget |
| 12 | 12.1–12.9 | 12.1 and 12.2: within the person's or agent's rules and budget, every call audited; 12.5: a new personal column masked everywhere; 12.6: a search answers from documents the person may read, citing them; 12.7: an agent with no notebook runs on its schedule and is a tool of another, both audited; 12.9: the Governance page shows every approval pending |
| 13 | 13.1 | 13.1: usage and cost show per project, and a budget alerts |
| 14 | 14.1, 14.3 | 14.1: an app shared with sam shows what he may see; 14.3: a developer makes a project's flow from the package alone |
| 15 | 15.1 | 15.1: a model approved before a pipeline uses it, its drift watched; the same model answers as an API, audited, and refuses someone outside the project |

Each storyboard becomes an end-to-end suite, as the phases before did: the people and agents above, against a real install, in CI.
