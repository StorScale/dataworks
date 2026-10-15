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

---

## Phase 8: governed AI

### 8.1 sam asks a question

1. sam asks an assistant (Claude, signed in to the platform as sam): "What was revenue by region last quarter, and why did EU fall?"
2. The assistant asks the platform's MCP server for the `revenue` metric by region and month. It runs as sam: sam sees the EU only, as on the dashboards, and the numbers match them to the cent.
3. The answer says which metric definitions it used, and links to them.
4. sam asks for customers' card numbers. They come back masked, as for sam anywhere else.
5. The question's cost (LLM tokens, Trino time) counts against the `sales` project's budget.

**Requirements**
- R8.1 Every answer from the semantic layer, run as the person, with its definitions cited.
- R8.2 An LLM gateway (LiteLLM) in front of the models: per project and per person budgets, cost recorded, and guardrails (no secrets or masked values sent out).
- R8.3 Agents' calls audited with the person behind them (R6.11).

### 8.2 An agent of the project's own

1. bob sets up a nightly agent for `sales`: it reads the failed checks and posts a summary to the team's chat.
2. It gets an identity of its own, a service account in the project, and may only read the project's checks and tables.
3. The project's owner approves it, and it appears in the project's list of agents, with what it may use.
4. One night it loops. At its budget, the gateway stops it, and bob gets an alert.

**Requirements**
- R8.4 Agents registered in a project: identity, owner, tools and data allowed, budget.
- R8.5 Approval before an agent runs; revoking it takes effect at once.
- R8.6 Spend caps that stop an agent, with an alert.

### 8.3 A metric change, tested

1. bob changes `revenue` to exclude refunds, in a pull request.
2. The semantic layer's tests run (as in 7.3): known answers for known questions (EU revenue for September), and a set of plain-language questions whose expected metric calls are checked.
3. The pull request shows which answers changed. After merge, sam's next question gets the new definition.

**Requirements**
- R8.7 Tests for the semantic layer: expected values, and expected metric calls for plain-language questions.

### 8.4 alice writes SQL with help

1. On the SQL page, alice asks the assistant for "orders by region per week with a 4-week moving average".
2. It writes the SQL from the project's tables and their columns (the catalog), and she runs it as herself.
3. It never sees data she can't, and its suggestions use only tables she can read.

**Requirements**
- R8.8 An assistant in the SQL page (and for pipeline code), given the project's schema and run under the person's access.

---

## Phase 9: usage and cost per project

### 9.1 frank's monthly view

1. frank opens the Usage page for `sales`: this month's Trino CPU time, Spark time, storage (by bucket and table), LLM spend, and the trend against last month.
2. It shows what drives it: the top pipelines, people and queries.
3. At 80% of the month's budget, frank and the project's owner get an alert.

**Requirements**
- R9.1 Usage attributed to projects: Trino (query to project, through the person or service account), Spark, Buckets storage, the LLM gateway.
- R9.2 A rate card (what a CPU-hour, a GB-month and a token cost), set by the install.
- R9.3 Budgets per project, with alerts.

### 9.2 A runaway query

1. dana's notebook runs a query in a loop. Trino's load climbs.
2. The platform alerts priya, naming the project, the person, and the query.
3. Trino's resource group for `sales` already caps its share, so other projects are unaffected; priya can stop the query from the Health page.

**Requirements**
- R9.4 Trino resource groups per project.
- R9.5 Alerts naming the project, person and query; a way to stop it.

---

## Phase 10: self-service and ML

### 10.1 alice cleans a spreadsheet without code

1. alice uploads a supplier's CSV to the project's landing area. The platform previews it.
2. She builds a recipe: rename columns, parse dates, drop test rows, remove duplicates, join to `orders` by order ID. Each step previews its result. The SQL it generates is shown beside it.
3. She saves it as a table in `sales`. Its lineage appears in the catalog, and it can run on a schedule.
4. Saved, it's code in the project's repo (as a dbt model), so bob can review and change it like any other.

**Requirements**
- R10.1 Uploads to the project's landing area, with a preview.
- R10.2 A recipe builder: steps, each previewed, generating SQL (or a dbt model) kept in the project's repo.
- R10.3 The result in the catalog with its lineage, and schedulable as a pipeline.

### 10.2 An app for sam

1. alice makes a small app from the `revenue` metric: a region filter, a table and a chart.
2. She shares it with sam, who opens it from Home; it shows what sam may see.

**Requirements**
- R10.4 Simple data apps per project, built from metrics and tables, run as the person viewing them.

### 10.3 dana puts a model into use

1. dana trains a churn model in a notebook. Each run is tracked (MLflow): parameters, metrics, the data's Nessie commit.
2. She registers the best one and asks for approval. The project's owner sees its evaluation and approves.
3. It's used by a pipeline that scores customers nightly, and its drift is monitored.

**Requirements**
- R10.5 MLflow per project, its runs tied to the data's commit.
- R10.6 A model registry with approval before use.
- R10.7 Batch scoring as a pipeline; drift monitored in Grafana.

---

## From storyboards to phases

| Phase | Storyboards | Its gate tests |
|---|---|---|
| 6 | 6.1–6.4 | 6.2 on the real cluster: an upgrade with no lost projects, people or settings |
| 7 | 7.1–7.4 | 7.3: a pull request runs on its own data branch, a good one merges, a bad one is refused |
| 8 | 8.1–8.4 | 8.1 and 8.2: within the person's or agent's rules and budget, every call audited |
| 9 | 9.1–9.2 | 9.1: usage and cost show per project, and a budget alerts |
| 10 | 10.1–10.3 | 10.1: a clean table from a spreadsheet without code, in the catalog with its lineage |

Each storyboard becomes an end-to-end suite, as the phases before did: the people and agents above, against a real install, in CI.
