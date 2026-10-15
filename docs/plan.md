# StorScale Platform: plan

**StorScale Platform** is one product over the open-source tools the Buckets integration guides already run and test together. You sign in once, and you get one home, one notion of a *project*, one catalog and one place to grant access. Each tool's own UI stays available underneath.

- **Repo:** `github.com/StorScale/storscale-platform` (AGPL-3.0, like Buckets).
- **Web:** `storscale.io/platform`.
- **CLI:** `storscale up`.
- **Naming:** *StorScale Buckets* is the storage; *StorScale Platform* is the platform on top.

## What it's for

Dataiku's core value is projects, a Flow, a catalog, governed access and automation in one UI. The parts already exist as open source, and the guides prove they fit together with one identity (Keycloak groups → Buckets policies, Ranger policies, Airflow and Superset roles). What's missing is the product layer:

1. **One shell:** single sign-on, one navigation, and one domain.
2. **Projects:** a team's data, notebooks, pipelines, dashboards and access as one object.
3. **One catalog and lineage graph** across Trino, Spark, Airflow and dbt.
4. **Access in one place:** a grant made once becomes the matching Keycloak, Ranger and Buckets settings.

The first users are data engineers and analysts. ML and GenAI wait until there are data scientists.

## Tools

| Layer | MVP | Later | Not included |
|---|---|---|---|
| Storage | Buckets | | |
| Table format, catalog | Iceberg, Nessie | Hive Metastore (for existing Hive estates) | |
| SQL | Trino | | Dremio (see decision 4) |
| Compute | | Spark (Spark Operator) | |
| Identity, access | Keycloak, Ranger | | |
| Notebooks | JupyterHub | | |
| BI | Superset | | |
| Ingestion | | dlt or Airbyte, Debezium (phase 8) | |
| Pipelines | Airflow | dbt (phase 7) | |
| Catalog, lineage, data quality | OpenMetadata (with OpenLineage ingestion), Soda checks | | |
| Monitoring | Prometheus, Grafana, Loki | | |
| Semantic layer, agent access | MetricFlow (phase 4), the platform's MCP server | | |
| ML, GenAI | | MLflow, LiteLLM, pgvector | |

## Architecture

```
            browser ── https://platform.example.com
                │
        ┌───────▼────────┐  one domain, a name per tool: auth., notebooks., dashboards., pipelines., monitoring., catalog.
        │  gateway       │  (Envoy Gateway / ingress; TLS)
        └───┬────────┬───┘
            │        │ tool UIs, behind the same session
   ┌────────▼───┐  ┌─▼──────────────────────────────────────────┐
   │ platformd  │  │ JupyterHub · Superset · Airflow · Grafana ·  │
   │ (BFF+API)  │  │ OpenMetadata · Trino UI · Ranger (admins)    │
   └──┬─────────┘  └──────────────────────────────────────────────┘
      │ OIDC (Keycloak) · signed calls to Buckets · REST to each tool
      │ Kubernetes API (Project CRs)
   ┌──▼───────────────┐
   │ platform-operator │ reconciles Project → Keycloak group, Buckets bucket+policy,
   └───────────────────┘ Ranger policies, Nessie namespace/branch, Jupyter profile, Superset role, Airflow DAG folder
```

- **`platform/web`:** React 18 + TypeScript + Vite. It starts from the Buckets console's shell, theme tokens, `call()` wrapper and components. Native pages cover what crosses tools: Home, Projects, Catalog and lineage, Access, Runs, and Health. Each tool's own UI opens inside the shell, embedded or in its own tab:
  - **Superset:** its embedded SDK, with guest tokens.
  - **Grafana:** panels embedded in the shell.
  - **JupyterLab and Airflow:** under names on the same domain, with their own OIDC sign-in. Keycloak's SSO session makes that sign-in silent.
  - **Names, not paths:** Superset and Ranger don't run well under a path prefix, so each tool gets a name of its own under one domain (`notebooks.storscale.localhost`). Browsers resolve `*.localhost` to the machine itself, so a laptop needs no hosts file. Inside the stack the same names lead to the gateway, so Keycloak's tokens have one issuer.
- **`platformd`:** a backend-for-frontend, the same design as consoled:
  - OIDC sign-in, with tokens kept server-side in an encrypted cookie;
  - calls to each tool's API with the user's token, or by impersonation where the tool needs it (Trino);
  - live data streamed to the browser over HTTP.

  It is written in **Go**: every client it needs (Kubernetes, OIDC, REST) already exists in Go, and nothing in it is performance-critical.
- **`platform-operator`:** a `Project` CRD, plus `PlatformInstall` for the install itself. It's written in Go with controller-runtime. Each reconcile is idempotent and writes to the tools' admin APIs. This is the code the examples' `lakekit.py` and the `*_setup.py` scripts already run, rewritten in Go.
- **Install:**
  - Kubernetes: a Helm umbrella chart (`storscale/platform`) that depends on the upstream charts plus the Buckets operator.
  - A laptop: `storscale up`, which runs the same stack with Compose, reusing `examples/common` and the example stacks.

## The Project, the central object

```yaml
apiVersion: platform.storscale.io/v1alpha1
kind: Project
metadata: { name: sales }
spec:
  members:
    - { group: engineers, role: editor }
    - { group: analysts,  role: reader, rowFilter: "region = 'EU'", mask: [email] }
  storage:   { bucket: sales-landing, quotaGiB: 500 }
  tables:    { catalog: iceberg, namespace: sales }      # Nessie namespace; branch per change request (later)
  notebooks: { image: storscale/notebook:py312, cpu: 2, memory: 8Gi }
  pipelines:                                              # a git repo of DAGs per project (phase 7)
    serviceAccount: airflow-pipelines
    repo: https://git.example.com/sales/dags.git
    path: dags/
    ref: main                                             # by default the install's own (main in dev, a release branch in prod)
    credentials: { secretRef: sales-git }                 # a Secret's name: never a token in the spec
  dashboards: true
```

From this one object, the operator creates:
- the Keycloak groups and the service account;
- the Buckets bucket and policies;
- the Ranger policies, with masks and row filters;
- the Nessie namespace;
- a JupyterHub profile;
- the Superset role and its dataset permissions;
- the Airflow DAG folder, its role and its connection.

Its status reports each piece.

The examples already prove every mapping in this list by hand.

**Phase 2 delivered (2026-10-06)** the parts that cover access: `members`, `tables` (with the readers' view: tables, row filters and masks), `files` and `pipelines.serviceAccount`, applied to Keycloak, Ranger, Buckets and Nessie (`stack/projects/sales.json` is one). Still to come:
- notebook profiles and quotas;
- a pipelines repo per project (phase 7, below);
- Superset and Airflow roles per project. Until then, those tools' roles come from the platform-wide groups, while their data access already follows the project through Trino and Buckets.

## Matching Dataiku

| Dataiku | StorScale Platform | When |
|---|---|---|
| Projects, permissions, user isolation | `Project` CRD; Keycloak, Ranger and impersonation | MVP |
| Flow (visual DAG) | A read-only Flow view drawn from OpenLineage and dbt lineage, with links into each job | MVP (view); an editable Flow is a later gap to build |
| Code recipes (SQL, Python, PySpark) | Trino, Jupyter, Spark, and dbt models | MVP (Spark later) |
| Join, group, window and pivot recipes | dbt models, or Trino SQL from a template | Phase 7 (dbt) |
| Prepare recipe (point-and-click) | Gap to build: a recipe builder that generates SQL | Phase 11 |
| Datasets and connections, Iceberg REST | Trino catalogs, Nessie's Iceberg REST endpoint | MVP |
| Connectors that bring data in (databases, applications) | Connectors (dlt or Airbyte) and change capture from databases (Debezium), into the project's Iceberg tables | Phase 8 |
| Semantic Models, AI SQL, agent tools (MCP) | The semantic layer: MetricFlow models, an editor in the shell, and an MCP server that queries as the person or agent asking | Phase 4 |
| Data catalog, column lineage, search | OpenMetadata + OpenLineage, shown in the shell | MVP |
| Data quality rules | Soda checks as Airflow tasks, results in the catalog | MVP |
| Scenarios, triggers, reporters | Airflow (data-aware scheduling); alerts on failures, failed checks and stale tables | MVP; the alerts in phase 6 |
| Dashboards, charts | Superset | MVP |
| Audit trail | Ranger, Keycloak, Trino and Buckets audit logs sent to Loki, with one Audit page | Phase 6 |
| Code environments, compute on Kubernetes | JupyterHub profiles, images built per project, quotas | MVP |
| Project version control (git) | Each project's pipelines (and later its whole spec) in its own git repo, readable as code | Phase 7 (DAGs first) |
| Bundles and Deployer (dev to prod) | Git CI plus a Nessie branch per change, checked, then merged; promotion between installs, with rollback | Phase 7 |
| LLM Mesh (gateway, cost caps, guardrails) | LiteLLM in front of the models, with a budget per project, and every call audited | Phase 9 |
| Agents, prompt studio, Knowledge Banks | The MCP server (phase 4), agent identity and audit, Langfuse, pgvector | Phase 9 (agent access to data came in phase 4) |
| Govern (approvals, registry, standards) | Approvals and a registry for models and agents, in the project | Phase 9 (agents), phase 11 (models) |
| Usage and cost per project | Trino, Spark and storage use attributed to projects, with budgets, in Grafana | Phase 10 |
| AutoML, experiment tracking, model registry, drift | MLflow, FLAML, Evidently + Grafana | Phase 11 |
| Webapps and Dataiku Apps | Simple data apps per project | Phase 11 |
| Answers, Stories | Not planned yet | Not included |

## What buyers value (research, 2026-10-15)

A survey of Dataiku and the platforms it's compared with (Databricks, Snowflake, Microsoft Fabric, Palantir Foundry, Domino, and the open-source stacks), from vendors' release notes, analysts' public summaries, review sites and practitioner surveys. Some of the evidence is vendor-reported, and G2, TrustRadius and Gartner Peer Insights couldn't be read directly.

- **Why people buy Dataiku:** one project and Flow shared by coders and non-coders; visual recipes beside code; scenarios that automate manual work (its ROI studies credit them with 80–90% of it); reuse; lineage; and Govern, its approvals and standards (Gartner ranks it first for the product owner). Since 2024 its new work is GenAI: the LLM Mesh, agents, semantic models for agents and, in 15.0, its own MCP server. The complaints: price, a learning curve and many clicks, slowness on large data, and git: projects are git repos, but their JSON doesn't diff well and conflicts can't be resolved in the UI. What customers value is the promotion from dev to prod (bundles, the Deployer, rollback) more than git itself.
- **Across the market in 2026:**
  1. **A trustworthy, governed foundation** (quality, lineage, access everywhere) comes first: in dbt's 2026 survey 83% call trust a priority, and in Informatica's 57% of data leaders say unreliable data keeps AI from production.
  2. **Agents over governed data, with a semantic layer:** every vendor's headline (Genie One, CoWork, Fabric data agents, AIP). Gartner (May 2026) puts the semantic layer's gain in agents' accuracy at up to 80%. The semantic layers are converging on the Open Semantic Interchange, around open-sourced MetricFlow.
  3. **Git-based CI/CD:** a declarative project file with a target per environment, plan then deploy, an isolated environment per pull request, CI on what changed (Databricks' Bundles, Snowflake's DCM Projects and dbt Slim CI, Fabric's deployment pipelines). Palantir's branching goes furthest: one branch across pipelines, logic and apps.
  4. **Governance for AI:** agents' identity, audit and spend caps (Databricks' AI Gateway, Snowflake's Agent Identity), and approvals for models and agents.
  5. **Cost visible and controlled:** Fabric's capacity costs are its most common complaint, and spend outruns budgets (dbt 2026: 57% spend more on compute, 36% have more budget).
  6. **Self-service:** visual prep, and simple data apps.
  7. **Low operating burden:** the main objection to assembled open-source stacks is the upgrade treadmill and version skew, separate access and metadata models, and no one accountable. Managed offerings (Datacoves, Astronomer) sell exactly that.
- **Where StorScale stands:**
  - **Strong already:** the project as the unit, Iceberg and Nessie, Ranger's policies applying to agents as to people, column lineage, and the semantic layer with an MCP server that queries as the user, where Dataiku (15.x), Databricks and Dremio are going.
  - **Where it can lead:** Nessie's catalog-wide branches. A pull request becomes a git branch and a data branch, its pipelines and checks run there, and both merge together (write, audit, publish). Only Palantir does something like it, and no open stack packages it.
  - **To watch:** consolidation. SAP is buying Dremio, Nessie's owner, and Fivetran has merged with dbt Labs and bought SQLMesh's maker. Keep an Iceberg REST alternative to Nessie in view (Apache Polaris, Lakekeeper).

Sources: Dataiku's version 14 and 15 release notes (https://www.dataiku.com/product/product-updates/version-14) and its version control docs (https://doc.dataiku.com/dss/latest/collaboration/version-control.html); Forrester's Wave for data lakehouses, Q3 2026 (https://www.forrester.com/blogs/takeaways-from-the-forrester-wave-data-lakehouses-q3-2026/); Gartner on semantics and agents (https://gcom.pdo.aws.gartner.com/en/newsroom/press-releases/2026-05-11-gartner-says-lack-of-semantics-causes-inaccurate-artificial-intelligence-agents-and-wasted-spending); dbt Labs' 2026 report (https://www.getdbt.com/blog/new-dbt-labs-report-finds-ai-driven-acceleration-is-outpacing-trust-and-governance); Databricks' and Snowflake's CI/CD docs (https://docs.databricks.com/aws/en/dev-tools/ci-cd, https://docs.snowflake.com/developer-guide/builders/devops); Palantir's Foundry Branching (https://palantir.com/docs/foundry/foundry-branching/overview).

## Phases

Each phase ends with a gate: an end-to-end test like the examples' `test.py`, run in CI. The use cases and storyboards behind phases 6 to 11, and the requirements they set, are in [use-cases.md](use-cases.md).

| Phase | Deliverable | Gate |
|---|---|---|
| 0 | Repo, CI, `storscale up` running the lakehouse, Superset, Airflow, JupyterHub and monitoring stacks from one Compose project on one domain | All existing example tests pass against the combined stack |
| 1 | The shell: `platformd` OIDC sign-in, navigation, Home, tool pages embedded or linked with silent SSO | Playwright: alice signs in once and reaches SQL, notebooks, dashboards and pipelines without a second login; carol is refused |
| 2 | Projects: the CRD and operator (also running in Compose mode), plus the Projects and Access pages | Creating `sales` gives each user exactly the access the guides check today, in every tool; deleting it removes that access |
| 3 | Catalog and lineage: OpenMetadata, OpenLineage from Airflow, Spark and Trino, a Flow view, Soda checks | A pipeline run appears in the lineage graph, column-level; a failing check shows on the dataset |
| 4 | The semantic layer and agent access (below): MetricFlow models per project, the Semantic layer pages, the MCP server | An agent signed in as alice asks for revenue by region and gets EU only, masked as in Trino; carol's agent gets nothing; bob's metric change shows up in the next answer; Ranger's audit names both the agent and alice |
| 5 | Kubernetes: the Helm umbrella chart, the Buckets operator, Spark Operator, KubeSpawner | `helm install` on kind gives a healthy install; the phase 1–4 tests pass on it |
| 6 | Production-ready: Keycloak on a real database, in production mode, and who owns the realm decided; the example users switchable off; one-command upgrades and a tested version matrix; Audit and Health pages (Loki, Grafana); backup and upgrade notes; Iceberg table maintenance (compaction, snapshot expiry, orphan files); high availability and hardening (replicas, network policies, Pod Security, encryption at rest, image scanning and SBOMs); alerts on failed pipelines, failed checks and stale tables; docs on storscale.io/platform | A full upgrade between two releases on a real cluster, with no lost projects, people or settings |
| 7 | Git-native projects: Airflow DAGs from a git repo per project, first (below); dbt projects in the same repo, run by Airflow, with their tests and lineage in the catalog; then the project spec in git, with a target per environment; a Nessie branch per change, its pipelines and checks run there before it merges (write, audit, publish); promotion between installs, with rollback | A push to a project's repo runs on its own data branch, passes the checks, and merges to production; a bad change is refused and production is untouched |
| 8 | Getting data in: connectors (dlt or Airbyte) for applications and files, and change capture from databases (Debezium) into a project's Iceberg tables, set up per project, with their credentials as Secrets | A database table's inserts, updates and deletes reach its Iceberg table within minutes, with lineage, and only the project's people can read it |
| 9 | Governed AI: agents' identity and audit; an LLM gateway (LiteLLM) with a budget per project; approvals for agents; tests for the semantic layer; the Open Semantic Interchange; classification of columns (personal, financial) in the catalog, with Ranger's policies by tag; an AI assistant for SQL and pipeline code | An agent's answers stay within the caller's policies and the project's budget, and every call is audited; a column newly tagged personal is masked everywhere without a policy written for it |
| 10 | Usage and cost per project: Trino, Spark and storage use attributed to projects, with budgets and alerts in Grafana | A project's usage and cost show, and crossing its budget alerts |
| 11 | Self-service and ML: visual prep (a recipe builder that writes SQL); simple data apps; MLflow, with a model registry and approvals | An analyst makes a clean dataset and an app from it without code |

**Not yet placed:**
- **Retention and erasure:** retention per table, and erasing a person's data everywhere (Iceberg deletes, then snapshot expiry and the old files removed), for GDPR and CCPA.
- **Templates and reuse:** project templates, shared libraries and recipes.
- **Data products and sharing:** a project publishes a table with an owner, a contract (schema, freshness, quality) and versions; others subscribe, granted access, not copies; sharing outside the company later.

**To decide, with the first teams:** streaming (Kafka, with Flink or Spark, into Iceberg) if something needs data in minutes; Python environments per project and GPUs for data science; a CLI, Terraform provider and published API for managing projects as code; disaster recovery to a second site (Buckets' site replication, a standby catalog).

## Phase 5: Kubernetes, how

- **One chart:** `deploy/helm/storscale-platform`. It reads the stack's own files (`stack/`: the realm, Trino's and the gateway's configuration, the DAGs, the setup scripts) through a symlink, so Compose and Kubernetes run the same configuration.
- **Same names:** each Compose service becomes a Kubernetes Service of the same name (`keycloak`, `trino`, `buckets`, `airflow-db`, ...). The gateway's routes, the tools' settings and the end-to-end tests work unchanged.
- **One address:** the platform's names (`storscale.localhost`, `catalog.`, `access.`, `s3.`) resolve to the gateway's Service inside the cluster as well as outside it, so tokens keep one issuer. A real install uses its domain's DNS. On kind, one rewrite rule in the cluster's DNS does it (`deploy/kind/dns.sh`), as Compose's network aliases do, and port 8800 on the host reaches the gateway.
- **Dependencies, as subcharts:** the Buckets operator (`oci://ghcr.io/storscale/charts/buckets-operator`) runs a `BucketsCluster` named `buckets`, and the Spark Operator (Kubeflow's chart) runs Spark jobs.
- **Secrets:** one Secret, `storscale-env`, holds what `.env` holds in Compose. Each value is generated on install and kept on upgrade, unless one is given in values. Trino's TLS certificate and the realm's signing key are Secrets made by the chart. Files one service writes for others (the catalog's bot token, Prometheus' Buckets token) become Secrets their writers keep up to date.
- **Order:** one-off setup steps are Jobs, named after the release's revision, so an upgrade runs them again. Services that need one wait for it in an init container, as Compose's `depends_on` does.
- **Notebooks:** JupyterHub starts each person's server as a pod (KubeSpawner) instead of a container.
- **Projects:** `Project` resources. platformd and the operator use the Kubernetes API as the project store, in place of the Buckets bucket that Compose uses.
- **Tests:** `storscale test --k8s` runs the same suites in a pod in the release's namespace. CI installs the chart on kind and runs them.

**Phase 5 delivered (2026-10-07):**
- The chart and `storscale --k8s`, as above, with the Buckets operator, the Spark Operator, KubeSpawner and Project resources.
- One PostgreSQL for the tools' metadata (Airflow, Superset, OpenMetadata) instead of three, keeping their Service names.
- Every suite passes on kind, unchanged except two:
  - The monitoring suite fails a drive from a pod beside Buckets', which mounts the drive's volume (bucketsd's image has no shell).
  - The setup steps create the `sales` project as a Project resource.
- Still to come: a Spark suite (the operator is installed, but nothing runs on it yet), and the browser suite's run on a laptop's kind cluster, which runs out of memory in a 10 GB VM; CI runs it.

## Phase 7: pipelines from git, how

Each project's DAGs come from its own git repo, as an Airflow 3 **DAG bundle** (`GitDagBundle`, from the git provider already in Airflow's image).

- **The spec:** `pipelines: {serviceAccount, repo, path, ref, credentials: {secretRef}}` (above). `ref` defaults to the install's own (`main` in development, a release branch or tag in production). Credentials are only ever a Secret's name; a repo URL with a user or token in it is refused.
- **Bundles:** one per project, `project-<name>`, with `subdir` from `path`, `git_conn_id` `git_<name>`, and a refresh about every minute. The platform's own DAGs stay in a local bundle.
- **The operator:**
  - serves the list of bundles (`GET /airflow/bundles`): the platform's own, and each project's whose connection is in place. Airflow 3.3 reads its bundles only as it starts, and has no API to add one, so the scheduler and the DAG processor start through a small wrapper (`stack/tools/airflow_bundles.py`) that reads the list, and starts its process again when the list changes. The same on Kubernetes and in Compose, with no rights to restart anything;
  - keeps each project's git connection in Airflow through its REST API, as the platform's account (its `Op` role may), from the Secret the project names (on Kubernetes a Secret in the namespace; in Compose `secrets/<name>.json` in the platform's bucket): never on a command line or in a file. A token rotated in the Secret reaches Airflow within a minute;
  - reports it in the project's status (the `airflow` part).
- **SSH hosts:** the git hosts' keys are the install's (`pipelines.gitKnownHosts`), checked strictly; without them a host's key is taken the first time.
- **What runs:** each run records the commit it was created from (`bundle_version`), and its tasks run that commit; a rerun runs it again. A push reaches Airflow within the refresh interval; Airflow 3.3 has no refresh API for a webhook to call.
- **In the platform:** a project's Pipelines page lists the DAGs of its bundle (`bundle_name`), not its `project:<name>` tag, which any repo could copy. It shows each pipeline's commit and each run's, with links to the repo, and import errors.
- **Guardrails:**
  - DAG IDs are global in Airflow, and when two bundles define the same one the last parsed wins, without a warning: the platform refuses a clash between projects.
  - Every project's code runs in one Airflow, which can read every connection: each project's git credential is a read-only deploy key or a token for its repo only.
  - The git provider is pinned at 1.0.1 or later: before 1.0.0, a token could be stored on disk in the clone's git config.
- **Tests:** a suite with a git server of its own (Compose and kind): push a DAG, create a project pointing at it, see the DAG appear and run at that commit; push a change and see the next commit; check that no token is in Airflow's config, its logs or the clone; delete the project and see its DAGs go.
- **Effort:** about 9 to 13 days.

## The semantic layer, and agents

People define what the business means once: entities (customer, order), dimensions (region, month), measures and metrics (revenue, active customers), and how tables join. People, dashboards and AI agents then ask for *metrics*, not SQL. They get the same answer, under the same access rules.

- **Definitions:** MetricFlow's YAML (semantic models and metrics), the Open Semantic Interchange's reference engine, Apache 2.0. It lives in each project's Git repo, beside its dbt models, so it's reviewed and versioned like code. Cube Core is the fallback; its own MCP server is in Cube's paid plans.
- **Building it, in the shell:**
  - **Pages:** a Semantic layer page per project. Browse entities, dimensions and metrics; start a model from a table in the catalog (phase 3) with its columns and joins filled in; edit with validation; preview a metric's SQL and its result before saving.
  - **Saving:** saving opens a change on the project's repo, or commits it directly for engineers.
  - **Who may edit:** engineers edit; analysts read.
- **Compiling:** `semanticd`, a small Python service around MetricFlow, compiles metric requests to Trino SQL. It never holds data credentials. The query runs in Trino *as whoever asked*, so Ranger's policies, masks and row filters apply, as they do in Superset.
- **Agent access (MCP):** the platform serves an MCP server at `mcp.storscale.localhost`, with OAuth through Keycloak (the MCP authorization spec). Its tools:
  - `list_metrics`, `describe_metric`, `list_dimensions`;
  - `query_metrics` (metrics, dimensions, filters, time grain);
  - `explain` (the SQL that would run);
  - `search_catalog` (from phase 3).

  Each agent gets only the projects and metrics its identity may see.
- **Agent identity:**
  - **An assistant acting for a person** (Claude, an IDE) signs in as that person through Keycloak, so it sees what they see.
  - **An autonomous agent** gets a Keycloak service account in a group, like Airflow's pipelines: narrow, audited, and revoked in one place.
  - **Audit:** Ranger's audit log and the platform's audit page record each query under the agent's name and the person it acted for.
- **Also served by the semantic layer:** Superset datasets from the metrics, so dashboards and agents agree, and `query_metrics` from notebooks (a Python client).



**Phase 4 delivered (2026-10-07)** the definitions, the compiling, agent access and the audit:
- **Where the models live:** in the project store, not yet in each project's Git repo.
- **How queries run:** `semanticd` forwards the caller's Keycloak token to Trino, so their access rules apply. The agent client's tokens carry both audiences, the MCP server's and Trino's. Keycloak's token exchange would be cleaner, and comes later.
- **Still to come:** Superset datasets from the metrics, a Python client for notebooks, and `search_catalog`.

## Decisions (2026-10-06)

1. **Repo and license:** `StorScale/storscale-platform`, AGPL-3.0. Buckets is a dependency (its images, chart and operator), not vendored code.
2. **`platformd` and `platform-operator` are written in Go** (controller-runtime for the operator). The C stays in Buckets.
3. **Catalog:** OpenMetadata, with OpenLineage ingestion; DataHub is not used.
4. **Dremio is left out.** It overlaps Trino and Superset, and its OSS edition has no OIDC or RBAC.
5. **Compose first, then Helm:** `storscale up` reuses the tested Buckets examples (phase 0); the Helm umbrella chart follows in phase 5.
6. **A semantic layer that agents can use (added 2026-10-06):** MetricFlow for the definitions, and the platform's own MCP server for agents, as phase 4. It comes after the catalog, whose tables and columns it starts from.
7. **Data quality with OpenMetadata's own tests (phase 3), not Soda:** it's one tool fewer, the results show on the tables in the catalog, and projects declare their checks (`spec.tables.checks`). Soda or Great Expectations can post results to the same tests through OpenMetadata's API later.
8. **The catalog opens in its own tab (phase 3), not in the platform's frame:** OpenMetadata's web app keeps its token in a service worker. Each project's Overview and Data pages show its lineage and checks inside the platform.
9. **The platform's own pages for everyday work; the tools for depth (added 2026-10-07):** the web app is organised around the project, not the tools. Its own pages cover what people do daily: browsing tables, previewing them, SQL, metrics, pipeline runs and access. They read the same APIs and run queries as the person asking. JupyterLab and Superset dashboards are shown in the frame, as project tabs. Airflow, SQL Lab, the catalog and Ranger stay one click away for authoring and administration.
10. **The roadmap after phase 5 (added 2026-10-15):** from the research above, in this order: production-ready (6), git-native projects (7), getting data in (8), governed AI (9), usage and cost per project (10), self-service and ML (11). Real people come before new features; then what buyers value most where StorScale can lead (git and data branches together, agents under the same policies).
11. **Airflow takes its DAGs from git as DAG bundles, one per project (added 2026-10-15),** not git-sync: bundles version each run by its commit and take several repos, where git-sync takes one repo and keeps no versions.
12. **What an install needs before real use (added 2026-10-15):** a review of the repo against the research found no ingestion, no Iceberg table maintenance, single replicas and no network policies, no alerts on data problems, and dbt named in the plan but not run. Maintenance, high availability and hardening, and alerts join phase 6; dbt joins phase 7; getting data in becomes phase 8; classification with policies by tag joins governed AI.
