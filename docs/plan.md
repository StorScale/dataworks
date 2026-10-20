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
| Prepare recipe (point-and-click), visual Flow | A canvas of steps (as Domo's Magic ETL) that writes dbt models into the project's repo, reviewed like code | Built (0.18.0, 0.19.x); a prepare step and an LLM step proposed |
| Datasets and connections, Iceberg REST | Trino catalogs, Nessie's Iceberg REST endpoint | MVP |
| Connectors that bring data in (databases, applications) | dlt imports and change capture from databases (Debezium), into the project's Iceberg tables; a gallery of one-click connectors (Salesforce, Gong, HighSpot, SAP) | Phase 8 (built: 8.1–8.3; the gallery, 8.4) |
| Semantic Models, AI SQL, agent tools (MCP) | The semantic layer: MetricFlow models, an editor in the shell, and an MCP server that queries as the person or agent asking; a company-wide model (Customer, Revenue, Campaign, Booking) that projects build on | Phase 4; company-wide, phase 10 |
| Data catalog, column lineage, search | OpenMetadata + OpenLineage, shown in the shell; a glossary from the semantic model, certified data products, a Collibra sync if Collibra is the catalog of record | MVP; glossary and products, phase 10 |
| Data quality rules | The project's checks (unique, notNull, rowCount), freshness targets, dbt tests, the semantic tests, the contracts' checks; Soda was never needed | Built; more check kinds proposed (the capability map, 2026-10-20) |
| Scenarios, triggers, reporters | Airflow (data-aware scheduling); alerts on failures, failed checks and stale tables | MVP; the alerts in phase 6 |
| Dashboards, charts | Superset; on the semantic layer's definitions, with Power BI beside it | MVP; on the semantic layer, phase 10 |
| Audit trail | Ranger, Keycloak, Trino and Buckets audit logs sent to Loki, with one Audit page | Phase 6 |
| Code environments, compute on Kubernetes | JupyterHub profiles, images built per project, quotas | MVP |
| Project version control (git) | Each project's pipelines (and later its whole spec) in its own git repo, readable as code | Phase 7 (DAGs first) |
| Bundles and Deployer (dev to prod) | Git CI plus a Nessie branch per change, checked, then merged; promotion between installs, with rollback | Phase 7 |
| LLM Mesh (gateway, cost caps, guardrails) | LiteLLM in front of the models, with a budget per project and per agent, the guardrail, and every call audited | Built (0.19.x); Langfuse and knowledge banks proposed |
| Agents, prompt studio, Knowledge Banks | The MCP server (phase 4), reaching the governed data products and company model (DevAssist among its clients), agent identity and audit, Langfuse, pgvector | Phase 10 (data products for agents), phase 12 (the rest) |
| Govern (approvals, registry, standards) | Approvals and a registry for models and agents, in the project | Phase 12 (agents), phase 14 (models) |
| Usage and cost per project | Trino, Spark and storage use attributed to projects, with budgets, in Grafana | Phase 13 |
| AutoML, experiment tracking, model registry, drift | MLflow, FLAML as a library, Evidently + Grafana; a model served as an API; no visual AutoML | Phase 15 (proposed split of 14) |
| Webapps and Dataiku Apps | Simple data apps per project | Phase 14 |
| Answers, Stories | Not planned | Not included; nor a plugin model, R, image and video processing, visual statistics, or third-party model monitoring (the capability map, 2026-10-20) |

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

## Dataiku's capability map, checked (2026-10-20)

Dataiku's product, as its own material lays it out in ten areas, checked item by item against what's built and what the phases plan. Each item gets one of three answers: **built** (in a release, or on main), **planned** (the phase that has it), or **gap**, with how to fill it, and whether to. The check is a map, not a commitment: the additions it proposes are gathered at the end, with where they'd go, for a decision.

### 1. Data access and preparation

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Connect to existing data platforms | Built: databases queried where they are (8.3), copied by change capture (8.1), applications imported with dlt (8.2), Salesforce from a form (8.4), uploads (11.1) | Gong, HighSpot, SAP (8.4) | None |
| Visual data preparation | Built: the canvas (11.1, 11.2), previewed on a sample, saved as dbt models | Spark for what Trino can't do in one query (R11.6) | None |
| 100+ built-in transformations | Partial: 13 step kinds, and a formula step for the rest (any Trino expression) | — | **Gap.** Dataiku's hundred are mostly one-column processors (trim, case, split, parse a date, fill nulls, extract by regex, standardize, round, bucket, geocode). Fill: a *prepare* step on the canvas, a list of named processors on columns, each compiled to Trino SQL (the formula step's machinery), with the common forty first. A day or two. |
| Cleansing, joins, filtering, enrichment, feature preparation | Built: join, union, filter, dedupe, group, pivot, window, formula; enrichment by join on uploads and products | — | The prepare step above covers cleansing; feature preparation is the window and formula steps, and notebooks |
| Visual recipes plus Python, R, SQL, Spark | Built: canvas (visual), SQL (Trino, dbt), Python (notebooks, scheduled; code steps as dbt models) | Spark (R11.6; the Spark Operator is in the chart, unused) | **R: gap, and not to fill** unless a team asks: an R kernel in the notebook image is an afternoon (IRkernel), but nothing else of the platform speaks R. A Python step on the canvas (a model written in Python, run by Airflow) is the Spark step's cousin: place it with R11.6. |
| Schema and data-type handling | Built: typed steps (the retype step, types from the catalog), schema changes followed by copies (R8.2) and imports | — | None |
| Dataset profiling and exploration | Partial: previews and column types on the Data page, the sample catalog-sync reads for classification | — | **Gap.** Fill: OpenMetadata's own profiler (row counts, nulls, distinct, min, max, histograms) run by catalog-sync's round or the nightly maintenance, shown on the Data page per column, and feeding the checks (a column's null share, say). A day. |
| Data quality rules and validation checks | Built: the project's checks (unique, notNull, rowCount), freshness targets, dbt tests, the semantic tests (12.3), the contracts' checks (10.3), checks before a delivery (9.1); failed checks are alerts | — | **Partial gap:** more kinds of check (accepted values, a range, a regex, a referential check, a custom SQL assertion). Fill: extend `tables.checks` with those five; a day. Soda, named in the MVP table, was never added: dbt tests and the project's checks took its place. Say so in the table. |
| Dataset certification | Built: certified data products (10.3) | — | None |
| Data lineage and transformation lineage | Built: OpenLineage from Airflow and dbt, column-level, in the catalog; the canvas's flows with their lineage | — | None |

### 2. Analytics

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Interactive statistics | Nothing visual; notebooks with pandas | — | **Gap; not to fill as a visual tool.** Fill the code path: scikit-learn, statsmodels and scipy in the notebook image (an hour), and a *Describe* card on the Data page from the profiler above. |
| Charts and visualizations, dashboards | Built: Superset, on the semantic layer's definitions (10.2) | Power BI (R10.5) | None |
| Reusable analytical workflows | Built: pipelines in git, flows on the canvas, dbt models, scheduled notebooks | Templates (U.2) | None |
| Visual and code-based analysis | Built: Superset and the canvas; SQL and notebooks | — | None |
| Time-series processing | Partial: MetricFlow's time dimensions and grains; Trino's date functions; the window step | — | Fill with the prepare step (resample, lag, rolling: window-step presets) and statsmodels in the image. Small. |
| Geographic data | Nothing | — | **Gap.** Fill: Trino's geospatial functions are already there (ST_* on the canvas's formula step); geopandas in the notebook image; Superset's deck.gl map charts (a plugin to enable). A day. |
| Graph analysis | Nothing | — | **Gap; code path only:** networkx in the image. An hour. A graph database isn't the platform's. |
| Text and NLP | Nothing | 12.4's assistant, 12's agents | **Gap.** Fill: an *LLM step* on the canvas (classify, extract, summarize a column through the gateway, on the project's key and budget, under the guardrail) covers what most teams mean by NLP today; spaCy in the image for the rest. The step, two days. |
| Image, audio, video processing | Nothing | — | **Gap; not to fill.** The lakehouse holds the files (buckets); processing them is notebook code, or the LLM step on a model that reads images. Say so. |
| Semantic models | Built: MetricFlow per project and company-wide (4, 10.1), the MCP server, terms | The Open Semantic Interchange (12) | None |

### 3. Machine learning and AutoML

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Visual AutoML | Nothing | — | **Gap; a decision.** Dataiku's visual ML workbench is years of work. Fill the code path instead: FLAML or AutoGluon in the notebook image, with a notebook template that trains, evaluates and logs to MLflow (14.2). If a team later wants it visual, a *train* step on the canvas over FLAML is the shape. |
| Code-first ML | Partial: notebooks with pandas and pyarrow; no ML libraries in the image | MLflow (14.2) | Fill: scikit-learn, lightgbm, xgboost, FLAML, mlflow in the image. An hour. |
| Prediction, classification, regression, clustering, time-series forecasting, causal ML, computer vision | Nothing | 14.2 | All code-path on the libraries above (causal: dowhy or econml; vision: torch, optional, large); none as visual recipes |
| Model comparison and evaluation, feature engineering, hyperparameter optimisation, explainability | Nothing | 14.2 (MLflow) | MLflow's experiments compare runs; FLAML tunes; shap explains. Libraries and a notebook template; no UI of the platform's beyond MLflow's. |
| Templates for common modelling use cases | Nothing | U.2 (project templates) | Fill: notebook templates in the template repo (churn, forecast, propensity), with U.2. |

### 4. Generative AI

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| LLM Mesh: provider abstraction, routing | Built: LiteLLM in front of the models (12.1), a model list per install; LiteLLM routes, falls back and load-balances by its own settings | — | None in the software; the install's model list decides. Expose LiteLLM's fallbacks and routing in the chart's values when a second provider arrives. |
| Quotas and usage controls | Built: budgets per project and per agent, keys, spend shown, alerts at 80% and at the budget | Per-person spend (12.1) | None |
| LLM monitoring | Partial: spend by key; refusals in the gateway's log | Langfuse (12) | **Gap.** Fill: Langfuse in the chart (traces, latency, tokens, cost per project and agent, prompts, scores), fed by LiteLLM's callback; its sign-in through Keycloak. Two days. |
| Cost and risk controls | Built: budgets, the guardrail, approvals of agents | — | None |
| Prompt engineering (prompt studio) | Nothing | — | **Gap.** Fill: Langfuse's prompt management (versions, labels, playground) is the studio; the platform's `buckets_lake.ask()` fetches a prompt by name. With Langfuse above. |
| RAG, semantic search, knowledge-base integration | Nothing | pgvector (named in the stack, no phase) | **Gap.** Fill as 12.6, *knowledge banks*: a project declares document sources (a bucket prefix, a product's text column); the platform embeds them through the gateway into pgvector in its Postgres, under the project's access; a `search_documents` MCP tool and a `knowledge` step on the canvas; the assistant and agents cite the passages. Three to four days. |
| Text analysis and enrichment | Nothing | — | The LLM step (area 2) |
| Custom Q&A and chatbot applications | Nothing | 12.4's SQL assistant; 14.1's data apps | Fill: a Q&A app as a kind of data app (14.1) over the knowledge bank and the semantic layer, as the person. With 14.1. |
| PII detection | Built: classification in the catalog (12.5), the guardrail (12.1) | — | None |
| Toxicity detection | Nothing | — | **Gap.** Fill: a second guardrail, a moderation model through the gateway itself (LiteLLM's `during_call` hook, the provider's moderation endpoint or a small classifier), on by a chart value. A day, once a provider is there. |
| Forbidden-term controls | Nothing | — | **Gap, small.** Fill: `llm.guardrails.terms`, the install's list of words and patterns the guardrail refuses (a codename, a client's name). An hour. |
| AI-assisted data preparation and SQL generation | Nothing | 12.4 (an assistant in the SQL page and for pipeline code) | Planned; add the canvas: "describe the step" fills a formula or filter. With 12.4. |

### 5. AI agents

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Visual agents | Nothing: an agent is a registration and a notebook | — | **Gap.** Fill as 12.7, *agents without a notebook*: a registered agent carries a prompt, its tools and a schedule, and the platform's runner (semanticd's loop over the gateway and the MCP server, as the agent) runs it; the Agents tab's form is the whole of it. Two days. |
| Code agents | Built: a notebook in the repo, run on a schedule as the agent (12.2) | — | None |
| Multi-step agent workflows | Partial: a notebook may loop over tools | — | The runner above is a multi-step loop; a pipeline may chain agents as tasks (Airflow). |
| Agent tools and API integration | Built: the MCP server's tools (metrics, terms, products, runs, alerts), granted per agent | — | **Partial gap:** a project's own tools (a REST API of the company's, with a connection's credentials). Fill: `spec.agents[].tools` may name a *connector tool* defined in the project (an OpenAPI operation and the connection to sign in with), served by the MCP server as the agent. Two days. |
| Agent Hub | Partial: the Agents tab per project | — | Fill: a platform-wide Agents page (every project's, their owners, approval, spend, last run), an afternoon, with the governance page in area 8. |
| Multi-agent orchestration | Nothing | — | Fill with 12.7: an approved agent is itself a tool another agent may be given (`agent:<project>/<name>`), audited as both. A day on top of the runner. |
| Centralized lifecycle management | Built: register, approve by another editor, change (approval again), revoke at once; the client and keys made and removed by the operator | — | None |
| Agent monitoring | Built: runs and their notebooks, alerts, spend, the audit log naming the agent | Langfuse traces | None beyond Langfuse |
| Cost, performance and risk tracking | Built: cost (budgets); risk: the guardrail, read-only tools, approval | Performance (Langfuse: latency, tokens, scores) | With Langfuse |
| Coordination of agents, models, data, business logic and human decisions | Built in part: agents under the project's rules and semantic layer, approvals by people | — | None to add as a feature; this is what the project is for |
| Cobuild (a natural-language interface to build things) | Nothing | 12.4 | **Gap; later.** 12.4's assistant, given the platform's own API as tools (make a flow, a check, a delivery), is Cobuild's shape. After 12.4. |

### 6. MLOps

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Model registry, versioning | Nothing | 14.2 (MLflow per project, a registry with approval) | Planned |
| Model deployment: batch scoring | Nothing | 14.2 (a pipeline) | Planned |
| Real-time / API deployment | Nothing | — | **Gap.** Fill: a registered, approved model served as an API in the project's namespace (MLflow's own serving image, behind the gateway, signed in through Keycloak, under the project's limits), with the audit naming the caller. Three days. |
| Deployment automation, dev/test/prod lifecycle | Built for projects: plan and apply with a target per install (7.4), data branches (7.3) | Models follow: a model's version in the project's spec, promoted by apply | With 14.2 |
| Approval workflows | Built for agents, deliveries, products, project applies | 14.2 for models | Planned |
| Model lineage | Nothing | R14.2: runs tied to the data's commit | Planned; add the model's inputs (tables, at their snapshots) to the catalog's lineage, so a model is a node like a table. With 14.2. |
| Model performance monitoring, drift | Nothing | R14.4 (Evidently, Grafana) | Planned |
| Regulatory / model documentation | Nothing | — | **Gap.** Fill: a model card per registered model (purpose, data, metrics, owner, approvals, limits), kept with the registry and shown with the product-like page; a day with 14.2. |
| Monitoring of models on third-party ML platforms | Nothing | — | Not to fill. |

### 7. Pipeline and workflow automation

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Automated scenarios, scheduled execution, dataset refresh | Built: Airflow per project, schedules, data-aware runs; imports and copies on schedules | — | None |
| Data-quality checks, metrics and checks | Built (area 1) | — | The extra check kinds (area 1) |
| Alerting | Built: the project's alerts page and the administrators' (6.4, 6.7) | — | None |
| Microsoft Teams and Slack notifications | Nothing: alerts are shown, not sent | R6.19 (6.7) | **Gap, overdue.** Fill: `notifications` per project (a Teams or Slack incoming webhook, an email address) and per install, sent by platformd when an alert opens and closes, muted for a while on request (R6.21). A day. |
| Production bundles | Built: projects as code, plan then apply, a target per install (7.4) | — | None |
| API deployment | Nothing | — | The model API above (area 6); data apps (14.1) |
| Workflow orchestration | Built: Airflow | — | None |

### 8. Governance

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| AI governance | Built: agents' identity, approval, tools, budgets, audit; the guardrail; assistants registered | — | None |
| Model governance, model registry | Nothing | 14.2 | Planned |
| Data lineage | Built | — | None |
| Model lineage | Nothing | 14.2 | Planned (area 6) |
| Approval workflows | Built: agents, deliveries, products' access, project applies; access requests (R6.4) | — | None |
| Documentation | Built: terms with owners and definitions, products' contracts, a project's description | Model cards (area 6) | None beyond model cards |
| Dataset certification | Built (10.3) | — | None |
| Risk oversight | Partial: alerts per project; the audit page | — | **Gap.** Fill: a *Governance* page for the administrators and the governance group: everything awaiting approval across projects (agents, deliveries, products, applies), the policies in force (masks by tag, who's excepted), the agents and their spend, the models and their state, the erasures due, in one place. Two days; it reads what exists. |
| Monitoring | Built | — | None |
| Governance across agents, models, analytics and pipelines | Built for agents, data and pipelines; models with 14.2 | — | The Governance page is where it shows |

### 9. Collaboration and application development

| Capability | Today | Roadmap | Gap, and how to fill it |
|---|---|---|---|
| Collaborative projects | Built | — | None |
| Dashboards | Built (Superset) | — | None |
| Workspaces (a business user's portal of dashboards and apps) | Partial: Home shows the person's projects | — | Fill with 14.1: a project's *Home for readers* lists its dashboards, products, apps and metrics, as the person. Small. |
| Stories | Nothing | Not included | Not to fill. |
| Dataiku Applications, web apps, business applications | Nothing | 14.1 (data apps) | Planned |
| Code notebooks | Built | — | None |
| Code Studios (an IDE in the browser) | Nothing | — | **Gap, small.** Fill: code-server (VS Code) as a JupyterHub profile beside JupyterLab, the project's repo cloned into it, signed in as the person. Half a day. |
| APIs: REST | Built: platformd's API, used by the web app and the suites; documented in the guide's labs as used | — | **Gap: no reference.** Fill: an OpenAPI document generated from platformd's routes, served at `/api/openapi.json` and as a page. A day. |
| Python API | Partial: `buckets_lake` in notebooks (S3, the MCP client, the gateway) | — | Fill: a `storscale` Python package (the platform's API as methods, signed in as the person or a service account; `buckets_lake` folded in), published with the release. Two days. |
| R API | Nothing | — | Not to fill (area 1). |
| Plugins and extensions | Nothing | 8.4's connectors are the platform's own | **Decision:** no plugin model. The extension points are the ones the platform already has: a project's repo (pipelines, dbt, notebooks), connectors kept by the platform, the MCP tools, the data apps. Say so. |

### 10. Enterprise deployment

Covered by phases 5 and 6, and by running on the company's cluster: the Helm chart, an upgrade command with its CRD and rollback, Keycloak brokering the company's identity, Ranger's policies everywhere, the audit log, backups and restore, high availability, network policies and Pod Security, limits per project, images scanned with SBOMs, a private CA. Left from 6: Buckets' encryption at rest (R6.18), disaster recovery to a second site (to decide).

### What the check changes (proposed, 2026-10-20)

Where the map found gaps worth filling, placed; the rest are decisions not to build (visual AutoML and statistics, image and video processing, R, Stories, a plugin model, third-party model monitoring), which the Matching Dataiku table now says.

- **Into phase 6 (overdue):** notifications to Teams, Slack and email per project and per install (R6.19, R6.21): a day.
- **Into phase 1 and 3 (the foundation, small):** profiling in the catalog with OpenMetadata's profiler, shown on the Data page (a day); five more check kinds (accepted values, range, regex, referential, SQL assertion; a day); the notebook image with the analysis and ML libraries (scikit-learn, statsmodels, scipy, lightgbm, xgboost, FLAML, mlflow, geopandas, networkx; an hour) and a code-server profile (half a day); Superset's map charts (an hour).
- **Into phase 11:** a prepare step (named processors on columns, the common forty; two days) and an LLM step (classify, extract, summarize through the gateway; two days); a Python step with R11.6's Spark step.
- **Into phase 12, as new use cases:** 12.6 knowledge banks (pgvector, embeddings through the gateway, `search_documents`, a canvas step; four days); 12.7 agents without a notebook, and agents as tools of agents (three days); 12.8 Langfuse (traces, prompts, scores, its sign-in through Keycloak; two days); the guardrail's forbidden terms (an hour) and a moderation guardrail (a day, with a provider); a project's own connector tools for agents (two days); a platform-wide Agents page (an afternoon).
- **Into phase 8 (governance):** a Governance page across projects (two days).
- **Phase 14, split in two:** 14 stays data apps, with a readers' Home per project and a Q&A app over the knowledge bank; **a new phase 15, ML and MLOps**, takes 14.2 and grows: MLflow per project, the registry with approval, batch scoring, drift, a model served as an API, model cards, models in the catalog's lineage. Three weeks.
- **Cross-cutting, with the first teams:** the OpenAPI reference (a day) and the `storscale` Python package (two days); Cobuild's shape after 12.4.

In all, some six weeks of additions, none of them a new system except Langfuse and pgvector (both in the stack's table since the start). What they'd give that Dataiku's map asks for and the roadmap didn't: text in the lakehouse (knowledge banks, the LLM step), agents a business user can set up, a model in production as an API, and the operators' view across projects.

## Phases

Each phase ends with a gate: an end-to-end test like the examples' `test.py`, run in CI. The use cases and storyboards behind phases 6 to 12, and the requirements they set, are in [use-cases.md](use-cases.md).

| Phase | Deliverable | Gate |
|---|---|---|
| 0 | Repo, CI, `storscale up` running the lakehouse, Superset, Airflow, JupyterHub and monitoring stacks from one Compose project on one domain | All existing example tests pass against the combined stack |
| 1 | The shell: `platformd` OIDC sign-in, navigation, Home, tool pages embedded or linked with silent SSO | Playwright: alice signs in once and reaches SQL, notebooks, dashboards and pipelines without a second login; carol is refused |
| 2 | Projects: the CRD and operator (also running in Compose mode), plus the Projects and Access pages | Creating `sales` gives each user exactly the access the guides check today, in every tool; deleting it removes that access |
| 3 | Catalog and lineage: OpenMetadata, OpenLineage from Airflow, Spark and Trino, a Flow view, Soda checks | A pipeline run appears in the lineage graph, column-level; a failing check shows on the dataset |
| 4 | The semantic layer and agent access (below): MetricFlow models per project, the Semantic layer pages, the MCP server | An agent signed in as alice asks for revenue by region and gets EU only, masked as in Trino; carol's agent gets nothing; bob's metric change shows up in the next answer; Ranger's audit names both the agent and alice |
| 5 | Kubernetes: the Helm umbrella chart, the Buckets operator, Spark Operator, KubeSpawner | `helm install` on kind gives a healthy install; the phase 1–4 tests pass on it |
| 6 | Production-ready: Keycloak on a real database, in production mode, and who owns the realm decided; the example users switchable off; one-command upgrades and a tested version matrix; Audit and Health pages (Loki, Grafana); backup and upgrade notes; Iceberg table maintenance (compaction, snapshot expiry, orphan files); high availability and hardening (replicas, network policies, Pod Security, encryption at rest, image scanning and SBOMs); alerts on failed pipelines, failed checks and stale tables; limits per project (Trino's resource groups, quotas for notebooks, Spark and buckets, Airflow's memory), so one project can't slow the others; retention per table and erasure of a person's data everywhere (consumer data); a table's history, and its data as of a date, a month-end close or a delivery (Iceberg's time travel, made visible, with tags kept for closes); docs on storscale.io/platform | A full upgrade between two releases on a real cluster, with no lost projects, people or settings |
| 7 | Git-native projects: Airflow DAGs from a git repo per project, first (below), then each project's DAG code kept apart from the others' and from the platform's secrets (below); dbt projects in the same repo, run by Airflow, with their tests and lineage in the catalog; then the project spec in git, with a target per environment; a Nessie branch per change, its pipelines and checks run there before it merges (write, audit, publish); promotion between installs, with rollback; a Connections tab, where editors give the platform credentials once, kept as the project's Secrets; scheduling a notebook as a pipeline | A push to a project's repo runs on its own data branch, passes the checks, and merges to production; a bad change is refused and production is untouched |
| 8 | Getting data in: databases queried where they are (Trino catalogs per project, under its rules), first; connectors (dlt or Airbyte) for applications and files, and change capture from databases (Debezium) into a project's Iceberg tables, set up per project, with their credentials from its connections; then one-click connectors from a gallery (Salesforce, then Gong and HighSpot, then SAP S/4HANA by OData and SAP HANA or BW as databases) | A database table's inserts, updates and deletes reach its Iceberg table within minutes, with lineage, and only the project's people can read it |
| 9 | Getting data out: scheduled file deliveries to clients and partners (SFTP, other companies' buckets), with formats, PGP encryption and manifests; checks before sending; approval of what's sent, with personal columns named; a delivery log that finds what was sent about a person; read-only, expiring shares for partners who read in place | A delivery reaches a client's SFTP server, encrypted, with its manifest; a failing check sends nothing; the log answers what was sent about a person |
| 10 | The business layer: the company's entities and metrics (Customer, Revenue, Campaign, Booking) defined once in a shared semantic model that projects build on; the platform and the catalog's glossary showing them before tables; Superset's datasets and Power BI on those definitions; certified data products with contracts, found in the catalog and read in place across projects (a Collibra sync if it's the catalog of record); the MCP server reaching them, with DevAssist as a client | Revenue the same to the cent in Superset, Power BI, SQL and an assistant, each under the person's rules; a certified product read in place by another project, and a breaking change refused |
| 11 | Visual transformation: a canvas of steps (join, filter, group, pivot, formula, deduplicate), each previewed, as in Domo's Magic ETL, that writes dbt models into the project's repo as a merge request, reviewed on a data branch and released like code; uploads (CSV, Excel) as inputs; Spark later | A pipeline built on the canvas runs on a schedule with its lineage in the catalog, and is a reviewed change in the repo |
| 12 | Governed AI: agents' identity and audit; an LLM gateway (LiteLLM) with a budget per project; approvals for agents; tests for the semantic layer; the Open Semantic Interchange; classification of columns (personal, financial) in the catalog, with Ranger's policies by tag; an AI assistant for SQL and pipeline code | An agent's answers stay within the caller's policies and the project's budget, and every call is audited; a column newly tagged personal is masked everywhere without a policy written for it |
| 13 | Usage and cost per project: Trino, Spark and storage use attributed to projects, with budgets and alerts in Grafana | A project's usage and cost show, and crossing its budget alerts |
| 14 | Data apps and ML: simple data apps; MLflow, with a model registry and approvals | A model approved before a pipeline uses it, its drift watched; an app shared with sam that shows what he may see |

**Not yet placed:**
- **Templates and reuse:** project templates, shared libraries and recipes.
- **Sharing outside the company:** data products read by partners (9.3 begins it).

**Decided (2026-10-19):** the LLM provider is Microsoft Foundry, serving Anthropic's models (phase 12's gateway).

**Decided (2026-10-19):** erasure (6.9) follows the CCPA (as amended by the CPRA), as the company's counsel reads it. What that sets for the design: a request to delete is answered within 45 days of receipt (one extension of 45 more, with notice to the person), so the erasure itself and its record must land well inside that; the request is verified before anything is deleted (the privacy tool's, or an administrator's, word: the platform records who asked and who verified, never the person's data); the erasure's record names the deliveries that carried the person's data (R6.28), so the recipients can be told, and the platform keeps the records of requests for at least 24 months; and data in backups is deleted when the backup is next restored or expires, which the record says. Other laws' deadlines are a setting, not a rewrite.

**To decide, with the first teams:** saved and shared queries, and query history, on the SQL page; streaming (Kafka, with Flink or Spark, into Iceberg) if something needs data in minutes; Python environments per project and GPUs for data science; a CLI, Terraform provider and published API for managing projects as code; disaster recovery to a second site (Buckets' site replication, a standby catalog).

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

## Phase 6: Keycloak for real use, how

Today Keycloak runs in development mode and imports the realm (`stack/keycloak/realm.json`) afresh at every start: what anyone changes in it is gone at the next restart, and people are part of the platform's configuration. For real use the realm's ownership is split (decided 2026-10-16):

- **The platform owns its own parts**, kept in git and applied at every install and upgrade:
  - the realm itself, and the settings the tools rely on (token lifespans, TLS required or not);
  - its signing key (the chart's: Trino and Buckets keep the keys they've fetched);
  - the tools' clients (platform, lakehouse, jupyterhub, superset, grafana, airflow, openmetadata, the agents' client): their redirect addresses, flows, secrets (from `storscale-env`) and the mappers the platform needs (groups and audiences in tokens);
  - its service accounts (the operator's, the shared pipelines') and the roles they're given.
- **The operator owns what projects make:** each project's groups (`<name>-readers`, `-editors`, `-pipelines`) and, on Kubernetes, its pipelines client. As now.
- **The organisation owns the people:** users and their credentials, the role groups (`engineers`, `analysts`), identity providers (corporate sign-in), password policy, sessions, MFA and brute-force settings, themes. An upgrade never writes them. The example people (alice, bob, carol) and the role groups are made on a first install only, and only with `examples.users`.

How:
- **Keycloak** in production mode (`kc.sh start`), on the platform's Postgres (a `keycloak` database and user), behind the gateway (its hostname the platform's `/sso`, proxy headers trusted), health on its management port. One replica; more with the high-availability work.
- **The platform's part is applied by a setup step** (`setup/keycloak.py`, as the master realm's admin), not by importing the realm, which only ever makes a realm that isn't there:
  - the realm, if there's none;
  - each platform client, by its client ID: made if missing, else only the fields the platform sets are changed (redirect addresses, web origins, flows, secret, its own mappers by name); fields and mappers the organisation added stay;
  - the service accounts' roles: added, never others taken away;
  - the chart's signing key: present, active, first.
- **The realm file splits in two:** `platform.json` (settings, clients, service accounts, key) and `examples.json` (the example people and groups).
- **The same in Compose,** so CI checks an upgrade: the setup step runs twice, and a user, a group and a client mapper added by hand are still there, while a client's changed redirect address is put back.
- **Moving an install over:** today's realm lives in Keycloak's development database inside its container, and is rebuilt at each restart. The first start on Postgres builds it once more, then keeps it. People added by hand since Keycloak last restarted would need adding again: check before upgrading `storscale-da1`.
- **Status (2026-10-16):** built: Keycloak in production mode on Postgres (its own server in Compose, a database on the platform's in Kubernetes, made on upgrade by an init container); the realm imported only when there's none (Keycloak's own import skips an existing realm, so the file isn't split yet); `setup/realm.py` keeping the platform's parts at every install and upgrade; the `realm` suite checks an upgrade keeps an administrator's person, group, mapper and attribute, and puts the platform's parts back. The example people are still made with the realm on a first install, whatever `examples.users` says (R6.2, next).
- **Decided (2026-10-16):** the identity provider will be Entra ID, brokered by Keycloak, its groups mapped to the platform's roles (the group names become settings). Until real users come, the platform stays in development with its example people; corporate sign-in and `examples.users` wait.
- **Still open:** who approves access requests.
- **Effort:** about 4 to 6 days.

### Phase 6: where it stands (2026-10-17)

| Part | State |
|---|---|
| Keycloak on its own database, production mode; the realm's ownership split | Built (0.6.0) |
| Limits per project | Built (0.6.0) |
| Health page; alerts to administrators (services, Prometheus rules, failed setup steps, the certificate) and to each project (failed runs and checks, stale tables) | Built (0.7.0); use cases 6.4, 6.7 |
| One-command upgrades (`storscale upgrade`), CI upgrading the last release to each commit | Built (0.7.0); a wider version matrix to come |
| Iceberg table maintenance: compaction | In 0.8.0; use case 6.5 |
| The tables' history and files nothing uses, collected by Nessie's GC, kept as long as the install says | In 0.8.0: Iceberg's own expiry doesn't apply to Nessie's tables |
| The audit log: the platform's records beside Ranger's, per project and for administrators, with export | In 0.8.0; use case 6.3; Keycloak's and Buckets' events, and agents apart from people, to come |
| The example users switchable off | Next, before Entra ID |
| Backups: every database (Nessie's catalog now among them, in Postgres) and the Projects and Secrets, nightly, encrypted for a key kept off the cluster; `storscale backup` and `restore`, tested in CI | In 0.9.0; the data copied off the cluster (`backup.data`) and a restore into another install (`--into-new`) in 0.10.0: `--into-new` checked in parts, not end to end |
| High availability and hardening: replicas, network policies, Pod Security, encryption at rest, image scanning and SBOMs | Network policies, Pod Security (baseline enforced), image scanning in 0.9.0; replicas of the stateless services and Trino workers in 0.10.0; platformd's and the databases' replicas, and encryption at rest, to do |
| Retention per table and erasure of a person's data | Waiting on decisions: which laws, what deadlines, how kept closes are treated |
| A table's history and its data as of a date or a close (6.10) | In 0.10.0: tags, the History tab, As of on the SQL page; the access fix in 0.9.0 |
| Docs on storscale.io/platform | To do |

### Pod security: what's left (2026-10-17)

Every container runs without privilege escalation, with the default seccomp profile and every capability dropped; the namespace enforces Pod Security's baseline standard and warns of the restricted one. The Postgres servers run as `postgres` (999), the gateway and JupyterHub's hub as an unprivileged user, and every image whose user id is known has it declared (`runAsNonRoot`). Short of the restricted standard still, each with what it would take:

| Container | Why | What it would take |
|---|---|---|
| `openmetadata`, `openmetadata-migrate` | The image's user id isn't known here | Read it from the image (or a derived image with one), then declare it |
| `ranger-admin` | Its image runs as `ranger`, by name: Kubernetes can't verify a name | Its uid from the image, declared |
| The Buckets operator | Its subchart's own settings | Its values, if it has them |

**Then:** enforce restricted.

### The data as it was (use case 6.10), how (design, 2026-10-17)

**Where history lives.** Nessie keeps each table's history as its commits on `main`; the Iceberg metadata it serves holds only the current snapshot, so Trino's `FOR TIMESTAMP AS OF` sees nothing older. Nessie's GC keeps a week of history (`maintenance.history.keep`); a tag keeps what it points to for as long as it exists.

**Reading as of a moment.** Nessie resolves a reference in a table's name, through its Iceberg REST API. **Checked (2026-10-17):** Trino passes `"orders#<commit>"` through, and reads the table as it was at that commit; `"orders@main"` (a branch or tag) isn't found. So the platform turns a tag or a time into the commit it stands for (Nessie's references and log), and every read as of a moment is `SELECT ... FROM iceberg.sales."orders#<commit>"`. Still to check: that writes to such a name are refused.

**Access never widens (R6.30), and a gap this shows today.** The operator's Ranger policies name tables exactly, and masks and row filters are keyed on the name. `orders@main` is another name to Ranger: where a project lists its readers' tables (as sales does) it matches nothing and is refused, which is safe; but where a project's readers see all its tables (the `*` policy), it would be allowed **without the table's masks and row filters**. The fix: every table-specific resource the operator writes (a reader's table, a mask, a row filter) also names `<table>@*` and `<table>#*`. **Done (2026-10-17):** the hole was real for `orders#<commit>` (read through Trino); with the fix, the lakehouse suite checks alice reads it as EU rows with card numbers masked, as she does `orders`, and can't read `payroll#<commit>`.

**Tags (R6.31).** A tag is Nessie's: one name for every table at a moment (a close is the whole catalog at once, which is what Finance means), cheap, and kept by GC. Names are the project's: `sales.close-2026-09` (a project's tags begin with its name; Nessie's tags are the whole catalog's). A project's editors make and remove its tags on a Tags tab, and pipelines through the platform's API (`POST /api/projects/{name}/tags`, as the project's service account). platformd records who made each, why, and until when it's kept (`keep`, as `7y`); the nightly maintenance removes those past it, and the audit log records each.

**A table's history (R6.29).** The table's page gets a History tab: Nessie's commit log for the table (`GET /api/v2/trees/main/history`, filtered to its key), each commit's time and its snapshot summary (rows and files added and removed: Nessie keeps the summary of each commit's snapshot), back as far as GC has kept. Who made a change: Trino commits to Nessie without the person's name, so the page takes it from the audit log (Ranger's record of the write, and its query, at that time) and the pipeline run from Airflow's runs at that time. A tag on the history marks a close. Older than the history kept: the page says so.

**Where it shows.** The SQL page and notebooks get an "as of" choice (a tag, a time, a commit) that writes the name; dashboards query a tag through a dataset's SQL. Deliveries (9.1) record the commit they read, so "open the data as sent" is `orders#<commit>`.

**Open, unchanged:** erasure against kept history (6.9: a close kept with a person's rows removed, or with personal columns erased, and its totals noted) needs the privacy team's and Finance's answer; how long closes are kept (often seven years) sets the storage they hold.

**Order and effort:** (1) the access check and the operator's fix, a day; (2) reading as of a tag, time or commit on the SQL page, with the checks above, a day; (3) tags: API, Tags tab, keep and removal, audit, two days; (4) the History tab, two days. **Built (2026-10-17):** all four. Tags are the project's (`tables.tags`: a name, a commit, keepUntil, a note, who), the operator keeps Nessie's as they say; the platform turns a tag or a time into its commit. Not yet: notebooks and dashboards read as of a moment by the name alone (no picker), and deliveries (9.1) recording their commit.

### DevAssist asks the same data (use case 10.4), how (design and build, 2026-10-19)

**Nothing new to reach, only tools to reach it.** The MCP server (semanticd, phase 4) already answers metric questions as the person: a project's metrics, with the company's they build on (10.1), run in Trino under their rules, and Ranger's audit names the agent and the person. What's missing is the business layer's words: an assistant asked about customers' bookings can't find out what Customer means, or that there's a certified `bookings` product, without a member telling it. So the server gains tools over what 10.1 and 10.3 made, nothing an assistant could reach raw tables with:

- `list_terms` and `describe_term`: the company's terms (`/api/terms`, as the Business terms page shows them: definition, owner, kind, synonyms, the metrics on an entity, the tables they come from, the projects using them), to anyone signed in, as that page is; a term is a definition, not data.
- `search_terms(words)`: terms matching a question's words (names and synonyms), or an empty list that says plainly that no governed definition covers it, so the assistant has something to say instead of guessing.
- `list_products` and `describe_product`: the data products, certified first, each with its producer, owner, version, description, terms, contract (columns, freshness, checks), consumers, and whether it's certified (and since when); to anyone signed in, as the Products page is.
- `query_product(project, product, columns, where, limit)`: rows of a product's table, in Trino as the person: a reader of the producer, or of a consuming project, gets them under the producer's rules (10.3), anyone else is refused by Ranger as anywhere. The tool says how to ask for the product (the Products page) when refused.
- `query_metrics` as it is, and every answer from it now carries `used`: the metrics' definitions, the terms they're on, and the product behind a table it read, each with the platform's link (`/terms/<name>`, `/products/<project>/<name>`), so an assistant cites them (R10.12). The server's instructions say to: answer from terms, metrics and products; name what was used; and say when nothing governed covers the question.

**Registered assistants (R10.13).** Today the realm has one agent client (`storscale-agent`, public, for Claude and IDEs). An assistant the company runs (DevAssist) is registered by an administrator on the Admin page, Assistants: a name and its redirect URIs. platformd keeps it (`assistants/<name>.json` in the platform's bucket) and the operator makes the Keycloak client, `assistant-<name>`, as `storscale-agent` is made (public, PKCE, the audience mappers for the MCP server and Trino, the person's groups), so the client's ID is the name in Ranger's audit and the platform's. Taking it away removes the client: it signs no one in from then on, and the tokens it had end within Keycloak's token lifetime (minutes). MCP's dynamic client registration (any client registering itself) isn't offered: the use case says assistants the company approves. Sign-in stays OAuth through Keycloak, as the person (R10.13); a confidential client (a service with a secret of its own) and an autonomous agent's identity are 12.2. **Built 2026-10-19**, the tools, the citations and the registration; the audit page's filter by agent is left to the search box (the agent's name is in each record).

**Audit.** The queries are already in the audit log under the person and the agent (Ranger's, through Trino: `query_product` runs the same way). Looking terms and products up isn't data access and isn't recorded. The audit page's filter gains the agent, so "what did DevAssist ask, for whom" is one search. Spend and guardrails are 12.1's gateway.

**Checks:** the `business` suite gains: alice's agent finds Customer among the terms (by a synonym), finds `bookings` certified, and gets campaigns' bookings by the company's segment with the definitions and the product cited; `search_terms` for something the model doesn't define says so; carol's agent sees the terms and the product, and `query_product` refuses her (she's no one's reader); an administrator registers an assistant, and a token from it reaches the MCP server and is refused once the assistant is taken away.

**Order and effort:** the tools and the citations, a day; registered assistants, a day; the checks, half a day.

### Erase a customer (use case 6.9), how (built 2026-10-19, under the CCPA)

**Asked by the approvers.** The data-protection approvers (`deliveries.approverGroups`, who also search the delivery log) and administrators ask, with the person's ID, when the person asked (the 45-day clock starts there: `erasure.deadlineDays`), and who verified the request (the privacy tool, or a person). The platform deletes the person's rows from each table the project declares as people's data (`tables.people`, by its person column), as its maintenance account (which reads and deletes unmasked; the setup gives it `delete`), and runs 9.2's search first, so the record names the deliveries that carried the person's data (R6.28).

**Recorded, never the data.** `erasures/<project>/<id>.json`: a hash of the ID (so the same request is recognised; the ID itself is nowhere), the dates (asked, due, done), who asked and who verified, what was deleted where, the deliveries, and where the rows still are: the tables' old versions for as long as the history is kept (6.10's window, the record's `agesOutBy`), and any close tagged while they were there (6.10's tension with erasure, now visible per record rather than hidden). The audit log has the erasure without the ID. The records are kept at least two years.

**Retention (R6.27).** `tables.retention`: a table, the column that dates its rows, and how long to keep them (`730d`, `2y`); the nightly maintenance (6.5) deletes rows past it before compacting.

**Left:** the tables lineage says were made from the declared ones (the catalog knows), the files' prefixes in the landing area, the data branches (short-lived: a merge request's), expiring the old versions early where the law wants it sooner than the history window, and the privacy tool's own sign-in (an agent of 12.2's kind). Open with the privacy team: who may ask besides the approvers, and the backup window.

### A new column of personal data, protected by its tag (use case 12.5), how (design, 2026-10-19; built 2026-10-19)

**Built as designed, with one change:** the detection is catalog-sync's own (column names, and patterns over a sample of each text column's values read as the catalog's Trino account: email addresses, phone numbers, card numbers by Luhn, national IDs, bank accounts), not OpenMetadata's PII processor, which needs presidio and spaCy with a language model (some 600 MB more in the image) for what names and free text need; that stays an option to add when a company wants it. The rest is as below: the `Protection` classification in the catalog, the file in the bucket, the operator's mask policies (Ranger's `public` group masked, the excepted groups first with no mask), the Data page's confirm and remove, the alert. The `classification` suite runs in the suites' own lane.

**The catalog classifies, the platform protects.** OpenMetadata's auto-classification (its profiler's sample data, the `PII` classification's detectors: email addresses, phone numbers, names, card numbers) tags columns as it finds them; catalog-sync runs it for each project's tables each round, as it runs the metadata ingestion. The platform's own classification in the catalog, `Protection`, has the tags that mean something to Ranger: `personal`, `financial`, `health`. catalog-sync maps the catalog's findings to them (`PII.Sensitive` → `personal`, suggested) and tags the columns a project declares (`tables.people`, R6.25) as `personal`, confirmed: so the catalog shows where personal data is, declared and found alike.

**One policy engine, not two.** Ranger's tag-based policies would need a tag service, the Trino plugin's tag download and a sync of entities and tags into Ranger: a second path beside the resource policies the operator writes today. Instead, catalog-sync writes each project's protected columns to the platform's bucket (`classification/<project>.json`: table, column, tag, suggested or confirmed, by whom), and the operator turns them into the mask policies it already knows how to write (`<project>:protect:<table>.<column>`, managed and prefixed like the rest): masked for everyone but the install's excepted groups. What a tag means is the install's (`protection.tags`, Helm values and platformd's config: `personal: {mask: hash, except: [data-protection]}`, `financial: {mask: redact, except: [finance]}`, `health: {mask: nullify, except: []}` by default); a project's own `readers.masks` stay beside them. A suggested tag protects at once (`protection.applySuggested`, default true: the use case's "applies at once"); the project's owner confirms or removes it in the catalog, or on the project's Data page, and the next pass follows. Ranger's rules hold everywhere Trino does: SQL, dashboards, notebooks, deliveries, the semantic layer, the assistants (R12.10).

**Seen.** The Data page marks protected columns with their tag and whether it's suggested or confirmed; an editor confirms or removes a tag there (platformd, through OpenMetadata's API, as the platform's account, recorded in the audit log); the catalog's own page does the same. A project's alert says when a new column was protected, so the owner looks.

**Checks:** a `classification` suite: bob adds a column of email addresses to a sales table; the catalog tags it, the Data page shows it suggested, and alice (a reader) sees it masked in SQL without anyone writing a policy; bob confirms it; bob removes the tag from a column that isn't personal, and alice reads it again; a column declared in `tables.people` is tagged `personal` in the catalog, confirmed.

**Order and effort:** the auto-classification workflow and the `Protection` classification in catalog-sync, a day; the operator's policies from `classification/`, a day; the Data page and its actions, half a day; the suite, half a day. **Open:** which groups are excepted at this install (the data-protection approvers of 9.1, by default); whether financial and health detectors find enough without rules of the company's own (the catalog takes custom detectors).

### A metric change, tested (use case 12.3), how (design, 2026-10-19; built 2026-10-19)

**Built as designed** (the known-answer tests; the plain-language ones wait for 12.1): `tests:` in the model, checked with it, run from the Metrics page and in the review on the data branch, the note saying which answers changed. The 8.2 gap went in with it: a project with its own Airflow lists, starts and reads the logs of its imports from the platform's Airflow.

**Tests beside the model.** A project's semantic model gains a `tests:` document (as `terms:` was added, taken out before MetricFlow reads it): known answers for known questions, each a metric query (metrics, group by, where) and the rows it must give, or a property of them (`rows: 3`, `revenue: {min: 0}`, `matches: {metrics: [order_count], group_by: [order__region]}`: the same as another query's answer). semanticd checks the document with the model (every test names metrics and dimensions the model has) and runs the tests on request: `POST /api/projects/{p}/semantic/tests`, as the caller, answering which passed and which didn't, with the rows that differed.

**In a merge request.** The repo's `storscale/semantic.yaml` (7.4) carries the tests with the model. 7.3's review (`storscale_review`) gains a second status, `storscale/semantic`: it asks semanticd to compile the commit's model (the existing check, with the branch's text as the override) and run its tests on the merge request's data branch (the review catalog in place of the project's), as the pipelines; then runs the same tests against production's model and data, and notes on the merge request which answers changed and how (EU revenue for September: 41,200 → 39,850, refunds now excluded). A test that fails on the branch fails the status; a changed answer is told, not refused: that's what a review is for. Merged, 7.4's plan applies the model, and the next question gets the new definition.

**Later, with 12.1:** a set of plain-language questions with the metric calls expected of them (`"revenue by region last quarter" → revenue by order__region, metric_time__quarter`), run through the LLM gateway against the model's terms, so a renamed term or a new synonym is tested too.

**Checks:** the `semantic` suite: a model with tests, all passing; a change that breaks one is refused on save (the check) and fails the merge request's status in the `reviews` suite, whose note names the changed answer; the tests run from the Metrics page.

**Order and effort:** the tests document and semanticd's run, a day; the review's status and note, a day; the pages and checks, half a day.

### An LLM gateway, and agents of the project's own (use cases 12.1 and 12.2), how (design, 2026-10-19)

**Decided (2026-10-19):** the models are Anthropic's, served by Microsoft Foundry; the company's Foundry resource is the platform's one provider.

**Built (2026-10-19, 12.1's first part):** the gateway in the chart (the stand-in by default, LiteLLM with the Foundry models when `llm.gateway: litellm` and the Secret `storscale-llm` holds the key: its `model_list` is the chart's `llm.litellm.models`), the key per project and per agent with its budget, made by the operator and kept as connections, the spend shown on the Agents tab, the alerts at 80% and at the budget; the `gateway` suite on the stand-in. LiteLLM itself runs on the dev cluster once the Foundry key is there; its database is the platform's Postgres (`litellm`, made by LiteLLM's own migration with the master key as its password), to be checked there. **Built (2026-10-20):** the guardrail, one module for LiteLLM (a pre-call custom guardrail, loaded from beside its config) and the stand-in: a question carrying the shape of a value the platform masks (card, national ID, bank account, email, phone: catalog-sync's own patterns) or a secret of the platform's (keys by shape, a secret's name with a value) is refused with HTTP 400 naming the kind, never the value, at no cost; `llm.guardrails` turns it off. **Left:** the plain-language tests (12.3), per-person spend, DevAssist through the gateway.

**Built (2026-10-19, 12.2's first part):** registration, approval by another editor (the secret made then, shown once, kept as a connection), the confidential client made by the operator with the readers' rights, the MCP server allowing only the agent's tools, revocation; the `agents` suite. **Built (2026-10-19, 12.2's second part):** scheduled agents as pipelines: an agent with a `notebook` in the project's repo and a `schedule` is, once approved, the DAG `agent_<project>_<name>` (storscale_agents.py, from the operator's `/airflow/agents` as deliveries are), which checks the repo out and runs the notebook with papermill as the agent: its client and gateway key from the Airflow connection `agent_<project>_<name>` the operator keeps, into the kernel's environment, where `buckets_lake.agent()` is the MCP server as the agent and `buckets_lake.ask()` the gateway on its budget. The executed notebook is the run's record (`agent.ran` in the audit). The read-only tools for a project's runs and alerts (`list_runs`, `list_alerts`, platformd's `/api/agent/...` by the agent's token) came the next day. **Left:** the gateway's guardrails.

**The gateway.** LiteLLM, a Deployment of the platform's (`llm-gateway`, its own Postgres database for keys and spend), configured with the Foundry resource as its provider (LiteLLM's Azure AI Foundry provider, the Anthropic models by their Foundry deployment names; the endpoint and key, or an Entra ID client, in the platform's Secret `storscale-llm`, never in a project's). Nothing on the platform calls a model directly: the SQL assistant (12.4), a project's agents and, if wanted, DevAssist's requests through the platform go through it. The gateway's key per project is the operator's to make (LiteLLM's virtual keys: a key per project with the project's budget from its spec, `spec.llm.budget`, per month; and a key per registered agent with its own), kept as the project's connection `<project>-llm`, which its pipelines and notebooks read as they read other connections. Spend is attributed to the project (and the agent) by the key, and to the person by the user LiteLLM records (`user` on each request: the platform sends the person's name). Guardrails: LiteLLM's pre-call hooks, the platform's own first: no value a mask would hide leaves (the platform knows the masked columns), and no secret of the platform's (its key names). At a budget's end the key stops answering, and the project is alerted (6.7: `llm/<project>` and `agent/<project>/<agent>` issues).

**Agents of the project's own.** A project's editor registers an agent (`spec.agents`, through a form on the project's Agents tab, as imports are added): its name, what it's for, its owner, the tools it may use (of the MCP server's, by name: `list_metrics`, `query_metrics`, `describe_product`; and the project's checks and runs, by new read-only tools), its budget, and its schedule if it runs on one. The operator makes it a confidential Keycloak client (`agent-<project>-<name>`, client credentials: what 10.4 deferred), a member of the project with a role of its own, `agent`, whose Ranger and bucket rights are the readers' on what it's allowed; and its gateway key. The project's owner (an editor other than who registered it) approves it before it runs; revoking it removes the client, and the gateway's key, at once (R12.5). An agent that runs on a schedule is a pipeline of the project's (a DAG the platform writes, as 7.7 writes a notebook's), signed in as its client; one driven from outside (a company tool) signs in as the client itself, with the MCP server told which tools its token allows (the token's client names the agent). Every call it makes is in Ranger's audit as the agent with the person, or without one, behind it (R12.3, as 10.4 does for assistants).

**Checks:** an `agents` suite: a project registers an agent with a small budget and two tools; refused before approval, it answers after; it queries a metric (the audit names it), is refused a tool it wasn't given, is stopped at its budget with the project alerted, and revoked, signs in no more. The gateway is played by a stand-in in CI (an OpenAI-shaped server with a counted budget); the real Foundry resource is the real cluster's to add, as a Secret.

**Order and effort:** the gateway's chart and the key per project, a day; the agent's spec, client, role and approval, two days; scheduled agents as pipelines, a day; the suite, a day. **Open:** Entra ID or a key for the Foundry resource (a key first, Entra ID when the platform's Keycloak federates to it for Power BI too); which of DevAssist's traffic goes through the gateway, if any.

**Also next (from 8.2):** a project with an Airflow of its own sees its imports' runs on its Pipelines tab, listed from the platform's Airflow beside its own, half a day; it goes in the first bundle after 0.18.0 with 12.5 and 12.3.

### Consolidation (2026-10-19)

A first-principles look at the code after phase 11 found the architecture sound (access delegated to the stores' own policies, one declarative spec, end-to-end suites as the specification, few dependencies) and five places where the structure had begun to fight it. All five were done in one bundle: one error helper and request IDs in platformd (every refusal logged with its cause, every answer quoting an ID); the spec's checks split into a validator per area with a table test each; ruff and a compile of the stack's Python in CI; the suites in two lanes (the `sales`-sharing chain, and the suites with projects of their own alongside), with the `flows` suite's uploads in a project of their own. Still open from that look: sessions in memory keep platformd at one replica (Postgres is there when that must change); the HTTP API has no contract beyond the web's `api.ts`; semanticd is one module that would read better as four (api, tools, model, terms).

### A pipeline on a canvas (use cases 11.1 and 11.2), how (design, 2026-10-19; first part built 2026-10-19)

**Built (first part):** the compiler (`internal/flow`: every step kind, typed from the catalog's columns), the Flows tab (React Flow, loaded only when the canvas is), previews through semanticd's SQL path as the person, saving as a merge request (models, `sources.yml`, `schema.yml`, the DAG, a `dbt_project.yml` if the repo has none; a step dropped loses its model), reading a flow back from its branch or the target with code steps described by Trino, and the `flows` suite. What the design called `flow.yml` is dbt's own `schema.yml` (each model's description, its place on the canvas under `meta.storscale`, the output's checks as tests), since dbt reads every YAML file under `models/` as its own. Inputs are the project's tables and the products it consumes; a flow's name and its steps' are identifiers; a step's model is `<flow>__<step>`, the output's its table. The metric step is built too: semanticd explains the query uncapped (`explain` with `limit: 0`), Trino describes its columns, and the model holds the SQL (full table names, so its lineage isn't dbt's: the catalog shows it as a model without sources). Uploads are built as designed, with one choice: platformd writes the file as the person (their token exchanged with Buckets' STS, as notebooks do), so no account of the platform's needs the bucket; and the files import reads the landing area as the shared Airflow's pipelines client (`keycloak_pipelines`, a member of the project's pipelines group by `pipelines.serviceAccount`), exchanged the same way. Excel is read by the import (pandas, openpyxl), not previewed before. **Left:** incremental outputs, Spark (R11.6); and, from 8.2, a project with an Airflow of its own doesn't see its imports' runs (they run in the platform's Airflow) on its Pipelines tab.

**Code first, canvas on it.** A canvas is a folder of dbt models in the project's repo, `models/flows/<flow>/`, one model per step, and the canvas is a view of that folder, so the two can't drift (R11.5). Each model the canvas wrote starts with a header, the step's definition as YAML in a comment (`-- storscale-step:` ... kind, its settings, its inputs as `ref()`s, and a hash of the SQL under it), and the SQL the platform made from it below. `flow.yml` beside them holds the output's name, the layout (positions), the schedule, and the steps' checks as dbt tests. Opening the canvas reads the folder: a model whose SQL still hashes as the header says is its step, with its settings; one changed by hand (11.2) is a code step from then on, its SQL shown, its columns found by asking Trino what it returns (`DESCRIBE OUTPUT`), so the steps after it still know their types. A model without a header is a code step too, so a flow can be started in code and finished on the canvas.

**One query in Trino.** Every step but the output is `materialized: ephemeral`, so dbt compiles the flow to one `SELECT` with a CTE per step and Trino runs it as one query; the output step is a table (or incremental by merge, later) in the project's namespace, as 7.5's models are. The same compiler in platformd (`internal/flow`, Go: deterministic, unit-tested on each step kind) writes the models and makes the previews, so what alice sees on the canvas is what runs.

**The steps (R11.1)**, Magic ETL's tiles in dbt's terms: input (a table of the project's, a product it consumes, a source's copy, or an upload), select and rename, retype, filter, join (inner, left, full; on columns, each pair checked for type), union, deduplicate, group by (sum, count, min, max, average, count distinct), pivot and unpivot, window (rank, running sum, lag and lead, over a partition and order), formula (a Trino expression, checked by compiling it), and output. A step on the company's terms (11.2's step 3): a metric step takes a metric query (metrics, dimensions, time grain) and semanticd's `explain` gives it its SQL, so a flow's Revenue is the company's.

**Previewed on a sample.** Each step's result is the query up to that step, run as the person (platformd's SQL path, as the SQL page runs), with each input sampled (`TABLESAMPLE BERNOULLI(1)` under a cap, so a preview costs seconds); the SQL is beside it. Columns and types are worked out from the catalog's (`internal/catalog` `Columns`) through each step before anything runs, so a join on a varchar and an integer, a formula on a missing column, or a group by a column dropped earlier says why on the canvas, not from Trino.

**Uploads (R11.3).** A CSV or Excel file goes to the project's landing area (`landing/uploads/<name>` in its files bucket, where the pipelines read already) with a preview of its first rows and the types the platform read. Becoming a table is the import path (8.2): dlt's `filesystem` source, in the image already, as an import of the project's, so the table is the pipelines' and in the catalog like any import; the canvas's input is that table.

**Saved as a merge request (R11.2).** Saving is 7.7's path: platformd commits through GitLab's API, on a branch of the flow's own (`storscale/flow-<name>`), the models with their headers, `flow.yml`, and a DAG `flow_<name>.py` that calls `storscale.dbt.build(path="dbt", select="flows.<name>")` on the schedule chosen, with a merge request into the branch the pipelines follow; saving again updates the branch. Reviewed on a data branch (7.3), released by plan (7.4). Merged, dbt runs it with its tests, the output is in the catalog with its lineage column by column from dbt's artifacts (7.5), and a failing check is the project's alert (R11.4).

**In the shell.** A Flows tab on the project: the canvas (React Flow, MIT, as `@xyflow/react`; the Overview's Flow view stays hand-drawn), a step's settings on the right, its preview and SQL below, Save as merge request. Engineers and analysts who edit the project build flows; readers see them.

**Later (R11.6):** the same models under dbt-spark for what Trino can't do in one query; incremental outputs; a flow's own checks from the contract it publishes (10.3).

**Checks:** a `flows` suite (Kubernetes, with 7.3's GitLab): alice adds `orders` and an uploaded CSV, joins, filters, groups by region and week, previews each step on a sample with its SQL; a join on columns of different types is refused saying which; she saves, the merge request has the models with headers and the DAG; merged, the DAG runs, the table is in the catalog with its lineage from `orders`; bob changes one model's SQL and the canvas shows that step as code and the rest as steps; a metric step's SQL is the company's revenue.

**Order and effort:** the compiler and its unit tests, two days; the canvas, three days; previews and type checks, a day; save, the merge request and the DAG, a day; uploads, a day; the checks, a day. The largest item so far, so in two parts: the canvas, compiler and save first (11.1 without uploads, 11.2), uploads and the metric step after.

### A certified data product, found (use case 10.3), how (design, 2026-10-19)

**A product is the producing project's.** `spec.products`: a table of the project's published, with a description, an owner (a person), a version, the business terms it holds (10.1), and its contract: the columns it keeps, how fresh it stays (`within`, as freshness says it), and the project's checks on it that must pass. Who consumes it and its certification are the platform's, never set by hand (as a delivery's approval is): a save keeps them as they were.

**Read in place.** An editor of another project asks for a product (`POST /api/products/<project>/<product>/requests`, naming their project); an editor of the producing project (or an administrator) approves, and the consuming project joins the product's consumers. The operator then lets the consumer's groups (readers, editors, pipelines) read that table, and only it, as the producer's readers do: the producer's masks and row filters on it hold for them too, so it's read under the producer's rules, in place, never copied. Taking a consumer away is the producer's editors' too.

**Certified.** The governance group (`products.certifierGroups`, default the administrators) certifies a product: the certification records what it certified (the table, version and contract, hashed) and lapses when they change, so a certified product is one whose promise someone checked.

**The contract held.** Each minute platformd checks each product's table against its contract (its columns there, changed within its window, its checks passing); a broken one is the producing project's alert, naming its consumers. A merge request (7.3) whose branch would drop a contracted column fails its status, saying who depends on it.

**Found.** A Products page (certified first; each with its owner, version, contract, terms, consumers, and Ask for it); the search box finds products after terms; in the catalog, OpenMetadata's data products (in a domain of the platform's), each with its table, and the certified ones' tables marked Certification.Gold.

**Checks:** the `business` suite: company publishes `bookings` (contract: its columns, fresh within a day); campaigns' editor asks for it and company's approves; campaigns' reader then reads it in place, as company's readers do (a row filter on it holds for them); an administrator certifies it, and a contract change takes the certification away; the table losing a contracted column is company's alert, naming campaigns; the catalog has the data product.

### Dashboards on the same definitions (use case 10.2), how (design, 2026-10-18; Superset built, Power BI to decide)

**Superset.** `superset-sync`, from Superset's own image beside it (one replica: two never write at once), reads the semantic models from the platform's bucket each minute and keeps a dataset in Superset for each semantic model of a project's (the company's shared ones among them): a SQL view over the model's table, its dimensions named as MetricFlow names them (`booking__booked_on`), its measures, and through each foreign entity the dimensions of the model that entity is primary in (`customer__segment` on bookings); the model's metrics are the dataset's (a simple metric its measure's aggregate; a ratio of two such on the model). A metric no dataset can say (a ratio across models, a derived one) stays the semantic layer's, and the dataset's description says so. The datasets are marked as the platform's: one whose model is gone is removed, and people's own datasets are never touched. Charts on them run in Trino as the person (Superset impersonates), so Ranger's rules hold, and their numbers are the semantic layer's: the business suite checks a chart against the semantic layer's answer.

**Power BI (to decide).** It has no Trino connector of its own: Desktop, the Service through Microsoft's on-premises data gateway, or both; people signing in with Entra ID (Keycloak federated to it) or a personal token; and Starburst's certified connector or the open-source ODBC driver with a custom connector. It will read the same definitions: the datasets' SQL, made views in a schema of their own.

### One-click connectors, Salesforce first (use case 8.4), how (design, 2026-10-18)

**A connector is an import of a kind the platform keeps.** 8.2's imports (dlt, in the project's Airflow, its tables `<import>_<object>`) gain a third source beside `rest_api` and `sql_database`: `salesforce`, with the objects to copy (`account`, `opportunity`, `campaign`, `lead`, `contact`, …: the source's resources) and, if wanted, `since` (how much history: the incremental objects start there). Gong and HighSpot follow as kinds of their own on dlt's REST support; SAP comes by 8.1 and 8.3 (HANA or BW as a database) or by OData.

**The code is dlt's.** dlt's verified Salesforce source (Apache-2.0, on `simple_salesforce`), copied into the Airflow image at a pinned commit, with `simple-salesforce` in the imports' virtualenv. Each object is a dlt resource: those Salesforce changes in place (accounts, opportunities, campaign members, tasks) copied incrementally by their modification stamps and merged; the others replaced each run.

**Sign-in.** A connection of a new kind, `salesforce` (7.6): a user's name, password and security token; or a connected app's consumer key and secret beside the user's; or an instance's address and a session (a token from elsewhere). The sandbox (`test.salesforce.com`) by `domain: test`.

**From a form, by the project's editors.** The project's Sources tab gets Add a connector: Salesforce, then the connection (or a new one, on the Connections tab), the objects (ticked), the schedule, and since when. The platform writes the import into the project's spec: the editors' own endpoint for imports (`PUT /api/projects/<p>/imports/<name>`, and `DELETE`), the import checked as any spec change and recorded in the audit log; administrators keep the whole spec.

**Checks:** the `imports` suite plays Salesforce (its REST API's describe and query, the bulk API refused so the source falls back to queries): bob adds a Salesforce import from the form's endpoint with two objects; it runs; the tables are there with the records; a second run after a record changes merges it (the incremental object), and alice (a reader) can't add one.

**Left:** Salesforce's deleted records (its `queryAll` and `IsDeleted`), OAuth sign-in from the form (a connected app's web flow, the token kept), Gong, HighSpot and SAP.

### Customer, defined once (use case 10.1), how (design, 2026-10-18)

**The company's model is a project's.** A project marked `semantic.shared: true` (the install's `company`, say) holds the company-wide semantic model: MetricFlow's semantic models, entities and metrics over its tables (`Customer`, `Revenue`, `Campaign`, `Booking`). Its editors are the governance group, so it changes as any project's model does: by its editors, or released from its repo (7.4: plan, then applied by another). Who may read the data behind it is Ranger's, as for any project: the shared project's readers.

**Projects build on it.** A project names the shared models its own builds on (`semantic.uses: [company]`), and semanticd compiles its model with them before its own: its metrics use `customer` and `revenue` without defining them, its semantic models join to the shared ones by entity. Building on it is a project's choice, not imposed: MetricFlow wants a model's measure names unique and a name one kind of thing throughout, so a project with its own `customer` dimension (the `sales` example) would break if the company's model were laid on it unasked. A project that defines a name the company's model has is refused, saying it's the company's. A change to the shared model that would break a model built on it (a name it uses gone, a clash) is refused, naming the projects, so the governance group sees what depends on what. A query runs as the person, in the project, as before: the shared tables are named in full, and Ranger decides.

**Terms.** What MetricFlow doesn't hold, a model's `terms:` document holds: each business term (an entity, a metric, a dimension) with its owner, its definition in plain words, and its synonyms. semanticd takes it out before MetricFlow reads the model, and checks each term names something the model has.

**Terms first.** semanticd answers `/api/terms`: every shared term with its definition, owner, kind, the metrics on it (an entity's), the tables it comes from, and the projects whose models use it. The platform's Business terms page lists them, a page per term, and the search box finds terms before tables (synonyms too).

**In the catalog.** catalog-sync keeps an OpenMetadata glossary, Business terms, in step with the shared terms (definitions, owners as reviewers, synonyms), and tags the tables each term comes from with it, so the catalog's search and a table's page show the business's words.

**Checks:** the `business` suite: a shared project (`company`, customers and bookings, terms with owners) and a project building on it (`campaigns`, `semantic.uses: [company]`, its own metric on the company's); the terms listed with definitions, owners and where they're used; alice (a reader of both) asks for the campaigns metric by customer segment, under her rules; a project redefining `revenue` refused; a shared change that would break `campaigns` refused, naming it; the catalog's glossary has the terms, and the tables are tagged.

**Order and effort:** semanticd's merge and checks, a day; terms and the Business terms page, a day; the glossary, a day; the checks, a day.

### What was sent about a person (use case 9.2), how (built 2026-10-18)

**Decided (2026-10-18):** found again from the tables, never kept in the log; searched by the data-protection approvers (`deliveries.approverGroups`); the tables as read kept for `deliveries.keepFor` (default 7 years).

**Whose data.** A project declares its tables of people's data and the column identifying a person in each (`tables.people`: `customers` by `customer_id`; R6.25's first part, which 6.9's erasure needs too). A delivery of such a table, or a query naming one and its person's column, is personal: it needs a data-protection approver as masked columns do.

**Kept as read.** A personal delivery's run records when it read the tables (`readAt`) and what it sent (its table or query, never a row); platformd, taking the run into the log, tags the catalog's commit at that moment in Nessie (`Delivery.<project>.<delivery>.<time>`: capitalised, so no project's own tags, which the operator keeps, are these), so Nessie's GC keeps those tables' data; the nightly round removes tags past `keepFor`.

**Searched.** A data-protection approver (or an administrator) gives a person's ID on the Deliveries tab: for each kept run, platformd runs what it sent again at its commit (the table as `"<table>#<commit>"`; a query with each of the project's tables in it read so), counting that person's rows, as the platform's maintenance account in Trino (which reads unmasked). The answer: the runs that carried them, when, to whom, approved by whom; runs it couldn't tell about say why. The audit log records the search, not the ID.

**Left:** a query naming a table the project has since dropped can't be run again (said so); runs from before this have nothing kept; the privacy tool's API (6.9) and erasure.

### A notebook on a schedule (use case 7.7), how (built 2026-10-18)

**In JupyterLab.** The notebook image has an extension, Schedule: a button on a notebook's toolbar, an item on a notebook's menu in the file browser, and a command. It asks which project (those the person edits, with a repo) and when (cron, UTC). Its server half (`storscale_schedule`, in the notebook server) reads the notebook, clears its outputs (what it computed may be data no repo should hold), and sends it to platformd with the person's Keycloak token, from the hub.

**Into the repo, reviewed.** platformd checks the person is an editor of the project, then commits through GitLab's API (no git in platformd; the repo's token from its connection; GitLab's address from `pipelines.reviews`, or the repo's own): the notebook as `<dags>/notebooks/<name>.ipynb` and a DAG file `<dags>/notebook_<name>.py` (`storscale.notebook.schedule(...)`), on a branch of their own (`storscale/schedule-<name>`), with a merge request into the branch the project's pipelines follow. Scheduling it again updates the branch and its merge request. Always a merge request: R7.21 asks for review, and a protected branch needs one anyway.

**The run.** `storscale.notebook` (in the Airflow image) runs it with papermill in a virtualenv of its own (`/opt/notebook`: the notebook image's packages, so a notebook runs as it ran by hand), as the project's pipelines: its `buckets_lake` has JupyterLab's calls, signed in as the pipelines' service account. Each run's executed notebook, and its HTML, go to platformd (`notebook-runs/<project>/<dag>/<run>`), linked from the run on the Pipelines tab (the HTML served sandboxed: its outputs, nothing run). A failed run fails its task saying which cell failed and why; the project's alerts (6.7) say the pipeline failed.

**Open:** a project's own Python packages (the image's for now; see "To decide"); the alert naming the failing cell itself, not only the run.

### A project's credentials, given on its Connections tab (use case 7.6), how (built 2026-10-18)

**Kept where they were.** A connection is what it was before the tab: on Kubernetes a Secret labelled `platform.storscale.io/connection=<project>` (its kind, who added it and when, who last changed it, as annotations), in Compose `connections/<project>/<name>.json` in the platform's bucket (and `connection-meta/` beside). Everything that reads connections (the operator, platformd's alerts) is unchanged; one made with kubectl is listed too ("made outside the platform").

**Kinds.** git (a token, and GitLab's webhook token), ssh (a key), database (sources, copies, sql_database imports), api (a rest_api import's token), sftp and s3 (deliveries' targets), pgp (a recipient's key), teams (alerts). Each says its keys, which are secret (never shown again: only that they're set) and which aren't (a host, a user's name, a bucket: shown), and which are needed.

**Who.** The project's members see the list: each one's name, kind, the values that aren't secret, who added and changed it, and what in the spec uses it. Its editors (and administrators) add one, whose name starts with the project's and a dash (`sales-dags`), so it can't be the platform's own Secret or another project's (and one whose name is taken by something not the project's is refused); give one new values (a secret left empty stays as it was: rotating is pasting the new one), after which the operator applies the project at once; and remove one the spec doesn't name (one it names says by what). Each change is in the audit log, never with a value.

**Left:** connections stay when their project is removed (a project made again with the same name finds them); the platform's own Secrets aren't on any tab.

### The project as code, from development to production (use case 7.4), how (design, 2026-10-18)

**In the repo.** Beside the pipelines, a folder `storscale/`: `project.yaml` (the project's spec: members, tables and their readers' rules, checks, sources, imports, deliveries), `semantic.yaml` (its metrics), and `targets/<target>.yaml`, what differs per install (members, buckets, schedules), laid over `project.yaml` (maps merged, lists replaced). An install says which target it is (`install.target`: development, production). A release is a git tag (`v1.3`).

**Plan.** An editor asks for a plan of a ref (a tag, a branch, a commit) on the project's Releases tab. platformd starts the platform's DAG `storscale_plan` in the project's Airflow (it has the repo's credentials, and git), which checks the repo out at the ref and sends `storscale/` back to platformd as the project's pipelines. platformd composes the spec for this install's target, checks it (as any spec), and keeps the plan: the ref and its commit, who asked, and what changes: the spec's (members, readers' rules, pipelines, deliveries; the audit log's diff) and the metrics' (added, removed, changed).

**Apply (R7.13).** An administrator other than who asked for the plan applies it (a plan can change the project's members and readers' rules, which only administrators change today): the spec saved as any change is (deliveries' approvals kept as they were, a change to what one sends needing approval again), the semantic model saved, the release recorded (its ref, commit, who planned and who applied, when), in the audit log. A plan older than the project's current spec (changed since by another plan or by hand) is refused: planned again.

A repo's `project.yaml` may pin `pipelines.ref` to the release's tag: the pipelines' code then moves with the spec.

**Rollback (R7.14).** A plan of an earlier release, applied the same way. Data written in between stays (the tables are the lakehouse's, with their history: 6.10's as-of reads them as they were), and the Releases tab says so.

**Checks:** the `releases` suite serves a repo (pipelines_git's git host) with `storscale/` at two tags, the project made as v1 has it: v2 planned by alice (its changes listed: a member added, a metric added, the target's description), refused when applied by her or by bob on his own plan, applied by bob; v1 planned and applied (the rollback), the project's spec and metrics as v1's; a ref the repo hasn't, a failed plan.

**Order and effort:** the DAG and the files' composing, a day; plans and applying, a day; the Releases tab, a day; the checks, a day.

### A file delivery (use case 9.1), how (design, 2026-10-18; in 0.15.0)

**Built:** the spec (`deliveries`), the approvals (platformd: who changed what it sends, kept by it; approving, by another editor or an administrator, and a data-protection approver for personal columns; a change needing approval again), the operator's connection per delivery in the Airflow its DAG runs in, the approved ones listed for the DAG file (`storscale_deliveries.py`), the run (`storscale_delivery_run.py`: checks, files, gzip, PGP, manifest, SFTP or S3, three tries), the delivery log and the Deliveries tab, the `deliveries` suite. Who approves is a default until the user decides (`deliveries.approverGroups`). **Left:** fixed-width files, Azure Blob and Google Cloud Storage, whose data a delivery carried (9.2), shares in place (9.3).

**Targets first: SFTP and S3** (decided with the user, 2026-10-18): an SFTP server (its host key checked, a user and a key or password) and an S3-compatible bucket of another company's (an endpoint, a bucket and prefix, its keys), each one of the project's connections. Azure Blob and Google Cloud Storage later.

**What a project says.** `spec.deliveries`: each a name, what it sends (a table of the project's, or a query on its tables), the format (CSV, Parquet, JSON lines; fixed width later), compression (gzip), the files' names (`acme_results_{date}.csv.gz`, with `{date}` the run's), splitting by a column (one file per region: `{part}` in the name), a schedule, the target (a connection), PGP encryption with the recipient's public key (a connection's `pgpPublicKey`), and its checks: rows within a range, the table changed within a time (as the project's freshness), the parts expected all there.

**Approval (R9.4).** A delivery doesn't run until approved, and again whenever what it sends changes (its table or query, its columns, its target): the platform keeps the approval as the hash of those, who gave it and when, and the operator pauses the delivery's DAG while the hash differs. The approval page shows the columns that go, and which are personal: until columns are tagged (10.5), those the project masks for its readers. **Who approves** (a default, until the user decides): an editor of the project other than the one who last changed the delivery; and, when it sends personal columns, also a member of the install's data-protection group (`deliveries.approverGroup`, default the platform's administrators).

**How it runs.** A DAG per delivery, from the platform's DAG file in the Airflow image (`storscale_deliveries.py`, the bundle `storscale`), in the project's own Airflow on Kubernetes, as the project's pipelines account: the checks first (a failing one sends nothing, and alerts as any of its pipelines does); the rows read through Trino under the project's rules (as the pipelines read them); the files written, compressed, encrypted (PGP, the recipient's key), and sent (SFTP with the host's key checked, or S3), with a manifest beside them (each file's name, rows and SHA-256); a failed transfer tried again three times, then failing the run (an alert). The tables' snapshots read are recorded with each run (R6.32's "as it was").

**The delivery log (R9.5).** Each run's files (names, sizes, checksums, rows), target, approval and the tables' snapshots, kept by platformd beside the audit log, on the project's Deliveries tab and in the audit export.

**Checks:** an SFTP server and an S3 bucket the suite runs (a test SFTP server in its pod; a bucket of Buckets' own, as "another company's"), a delivery of a table split by region, encrypted for a key the suite holds: refused until approved by another editor, then run, the files decrypted and their manifest checked, a change to its columns needing approval again, a failing check sending nothing.

**Order and effort:** the spec, approval and the pausing, two days; the DAG (read, write, compress, encrypt, send, manifest), two days; the log and the tab, a day; the checks, a day.

### A pull request on a data branch (use case 7.3), how (design, 2026-10-18; the first part in 0.14.0)

**Built:** `pipelines.reviews` (GitLab's address, the repo's path there, the dbt project's folder); the operator's poll of GitLab (`internal/gitlab`), the branches (`<project>-mr-<iid>`) and catalogs (`<project>_mr_<iid>`, Nessie's REST catalog at the branch: `http://nessie:19120/iceberg/<branch>/`), their Ranger policies, the run started a pass after the branch is made (Trino's policies first), once per commit (its run id); the DAG `storscale_review` (`/opt/storscale/dags`, the bundle `storscale` in every Airflow); platformd serving the last manifest to the project's pipelines; the `reviews` suite. **Then:** the note updated at each commit (marked, found among the merge request's notes), with a sample of the rows that differ (each model, `EXCEPT` both ways, five rows), and the metrics that moved (each metric's SQL from the semantic layer, run on production and on the branch); GitLab's webhooks (`/api/webhooks/gitlab/<project>`, the token in the repo's connection as `webhookToken`) kicking the operator, polling beside them.

**What runs on the branch.** A pull request's change to the project's dbt models (7.5), built on a Nessie branch of their own, and checked there: only the models the change touches and those downstream of them (`dbt build --select state:modified+`, against production's manifest, which the platform keeps since 7.5's artifacts), their tests, and the project's checks on those tables. The project's Airflow DAGs aren't run per branch: Airflow knows one DAG of each id, and a branch's DAGs would be the same ids again (a branch's DAG code is tested by the repo's own CI). Large tables: the branch starts as production's data (a Nessie branch copies nothing), so a model reads what production has, and only what it writes is new.

**How a run reaches the branch.** The platform makes the Nessie branch `<project>/pr-<n>` from main, and a Trino catalog for it (`<project>_pr_<n>`, dynamic, as 8.3's sources; the Iceberg REST catalog with Nessie's prefix for that branch, `<branch>|warehouse`), which only the project's people and pipelines may read. A Job of the platform's runs `dbt build` there as the project's pipelines account (the helper's profile, the branch's catalog), with the repo at the pull request's commit.

**What the pull request is told (R7.8, R7.9).** A status check on the commit: passed or failed, and which tests failed and why; a comment with each changed model's row count against production's, and a sample of rows that differ. Later: the semantic layer's metrics that moved (EU revenue 513.84 → 498.10).

**On merge (R7.10).** The branch's data is dropped (the branch removed): production's next run builds the merged models on main, as it always does. Publishing the branch's data itself (write, audit, publish) waits: a Nessie merge conflicts when main changed the same tables meanwhile.

**The git host: GitLab** (decided with the user, 2026-10-18): its merge requests, and commit statuses through its API (`/api/v4/projects/:id/merge_requests`, `/statuses/:sha`), polled each minute first (no need for GitLab to reach the platform), webhooks later. In CI, the suite plays GitLab's API (as it plays a git host and a CRM's). Others, for the record: GitHub (and GitHub Enterprise), GitLab, Gitea and Forgejo have pull requests, webhooks and commit statuses alike; Azure DevOps and Bitbucket differ. The project's connection (its repo's token) needs the right to read pull requests and write statuses. Polling the host every minute, or its webhooks (to platformd, at the platform's address): webhooks need the host to reach the platform.

**Checks:** a Gitea in CI (the suite's own git host, as pipelines_git's), a pull request changing a model: its branch made, its build run there, the status on the commit, production untouched, and the branch gone when the pull request closes.

**Order and effort:** the branch, its catalog and the Job, two days; the git host's pull requests and statuses (one host), two days; the comparison and the comment, a day; the checks, a day.

### dbt in a project's repo (use case 7.5), how (design, 2026-10-18; in 0.13.0)

**Built:** the helper (`storscale.dbt`, in the Airflow image's `/opt/storscale`) and dbt's virtualenv (`/opt/dbt`: dbt-core 1.12.5, dbt-trino 1.10.5), the project's pipelines allowed to make their tables, information_schema readable by all, and the pipelines_git suite's dbt project. **Then:** the artifacts into the catalog (the helper posts them to platformd's `/api/projects/<name>/dbt/artifacts`, as the project's pipelines account's token; platformd keeps them in `dbt/<project>/`; catalog-sync runs OpenMetadata's dbt source on them). **Left:** dbt packages (`dbt deps` would write into the read-only checkout), runs of only what changed (with 7.3), and a long run outliving its Keycloak token (minutes).

**Where dbt runs.** In the project's own Airflow (on Kubernetes; the shared one in Compose), as a task of a DAG in the project's repo: the platform's image gives the DAG a helper, `storscale.dbt.build(path="dbt")`, which the repo's DAG calls (its path is the repo's, so the helper finds the dbt project beside the DAG). The task runs `dbt build` in a virtualenv of its own (`/opt/dbt`: dbt-core with dbt-trino, beside Airflow's packages, as dlt is), in Trino, on the project's Iceberg tables, signed in as the project's pipelines service account (a Keycloak token, Trino's JWT sign-in, as lakehouse_identity does): Ranger's rules are the project's pipelines', and its audit names the account. The helper writes dbt's profile itself (Trino's address, its certificate, the catalog, the project's namespace as the schema), so the repo holds no connection settings and no secrets.

**What a project says.** Nothing new: a repo with a DAG that calls the helper. Its models and tests are dbt's own files (`dbt_project.yml`, `models/`), reviewed and versioned with the DAGs.

**In the catalog (R7.16).** The helper puts dbt's artifacts of each run (`manifest.json`, `catalog.json`, `run_results.json`) in the platform's bucket (`dbt/<project>/`), and catalog-sync ingests them (OpenMetadata's dbt source, from S3): the models' descriptions and columns, their lineage column by column (from dbt's own graph), and their tests' results beside the project's checks.

**Pull requests (R7.17)** come with 7.3's branches: a branch's run builds only what changed and what depends on it (`dbt build --select state:modified+`, against production's manifest).

**Checks:** the pipelines_git suite's repo gains a dbt project (a model on `orders`, two tests, one failing on purpose): its DAG runs `dbt build`, the model's table is made as the pipelines' account (Ranger's audit), and the catalog has the model, its lineage from `orders`, and the failing test.

**To check first:** dbt-trino's JWT sign-in against Trino 483, and its Iceberg models (table, incremental by merge) through Nessie's REST catalog.

**Order and effort:** the helper and the image, a day; the artifacts into the catalog, a day; the checks, a day.

### An application's data (use case 8.2), how (design, 2026-10-18; in 0.12.0)

**Built:** the spec (`imports`: `rest_api` and `sql_database`), the operator's Airflow connections per import and its list (`/airflow/imports`), the DAG file `storscale_imports.py` (a DAG per import, tagged as the project's pipeline), the run (`storscale_import_run.py`, in dlt's virtualenv in the Airflow image: dlt 1.30.0, pyiceberg 0.12.0), the scheduler with the lakehouse's writers' Buckets account (the copies'), and the `imports` suite. **Then:** the editor's Imports section, and catalog-sync's lineage (each import's pipeline upstream of its tables). **Left:** files from the project's bucket (`filesystem`: its account would need the project's bucket, which the lakehouse's writers' doesn't have), applications' own sources (HubSpot, Salesforce) by demand, and a reset of an import is built (an editor's ask: a run with dlt's `refresh="drop_sources"`; dlt keeps where its cursors got to in files beside the dataset, `warehouse/<namespace>/_dlt_*`, which dropping its tables leaves). An import runs once as it's added (Airflow's latest slot of its schedule, `catchup` off). In Compose the platform's Airflow also runs projects' repos, whose code could read the writers' account there (on Kubernetes they run in Airflows of their own).

**dlt, not Airbyte** (decided with the user, 2026-10-18): dlt is a Python library (Apache-2.0), run as an Airflow task, so an application's import is a pipeline like any other of the project's, with nothing new to run; Airbyte is a service of its own (its server, workers, a database), for connectors we'd mostly not use. dlt's filesystem destination writes Iceberg tables (`table_format="iceberg"`) through pyiceberg, which takes Nessie's REST catalog and the warehouse on Buckets; merges by key (`upsert`) and appends; its incremental state (each source's cursor) is in tables of its own beside the data (`_dlt_pipeline_state`), so a run copies only what's new (R8.6). (dltHub's own Iceberg destination, under a commercial licence, isn't needed.)

**What a project says.** `spec.imports`: each a name, a source, one of the project's connections (the application's token or key), what to copy (`resources`: the application's objects, as the source names them), a schedule (cron; default hourly), and how (`append`, or `merge` on the objects' keys). Sources to start: dlt's own `rest_api` (any REST API, declared: endpoints, paging, cursor), `sql_database` (a database without change capture, by a cursor column), and `filesystem` (files in the project's bucket, as CSV, JSON or Parquet); then verified sources by demand (HubSpot, Salesforce), baked into the image as dlt's code for them.

**What the platform makes.** A DAG per import, in the project's Airflow (on Kubernetes) or the shared one (Compose), from a DAG file of the platform's (`storscale_imports.py`, in the Airflow image) that reads the project's imports from the platform: its run is dlt's pipeline, into the project's namespace as `<import>_<resource>` (dlt's own tables there as `_dlt_*`). It writes as the imports' own Buckets account (as copies do); Nessie's REST catalog has no accounts of its own, so Ranger's rules (through Trino) are what keep the tables the project's. The runs are the project's pipelines: on its Pipelines page, alerting when one fails (6.7, as any of its pipelines), under its limits (its Airflow's memory).

**Lineage (R8.3):** catalog-sync puts each import's pipeline upstream of its tables.

**Checks:** a `rest_api` import of a small API the checks run (a few pages, a cursor): its tables made, a second run copying only what's new, a merge updating a row, the tables under the project's rules, a failed run alerting.

**To check first:** pyiceberg against Nessie's REST catalog and Buckets (S3 path style), and Trino reading dlt's tables; dlt and pyiceberg's versions beside Airflow's in its image.

**Order and effort:** the check above, a day; the spec, the DAG file and the Airflow image, two days; the editor and the checks, a day.

### A database's changes into the lakehouse (use case 8.1), how (design, 2026-10-17; in 0.11.0)

**Built:** the spec (`copy.tables`), the operator's Deployments (from the chart's `templates/copy.yaml`, settings in a Secret per copy), the copies' own Buckets account (`COPY_S3_*`), the Nessie namespace `storscale_cdc` for their state, the project's status part `copy`, the editor's tables to copy, and the `copies` suite. Decided with the user (2026-10-17): no Kafka; PostgreSQL, SQL Server and MySQL first. **Then:** decimals exact (`precise`: the sink makes Iceberg decimals of Debezium's), a copy not running for 10 minutes an issue of its project's (its alerts), a copy's pod crashing or stuck a Health finding, and catalog-sync's lineage from each copied table to its copy, column by column. **Known limit:** a PostgreSQL `numeric` of no declared precision comes as Debezium's variable-scale struct (`scale`, `value` bytes), which the sink keeps as a struct (seen in CI, 2026-10-18): declare its precision, or a later step turns it into a decimal. **Then:** how far a copy is behind, as each copied table's last commit (on the project's Overview). **Left:** Debezium's own lag (a JMX metric, which Debezium Server doesn't serve over HTTP: a JMX exporter in its pod).

**Kafka or not.** Not, to start: Debezium Server with an Iceberg sink ([debezium-server-iceberg](https://github.com/memiiso/debezium-server-iceberg), Apache-2.0, 1.2.0.Final on Debezium 3.6 and Iceberg 1.11; an image at `ghcr.io/memiiso/debezium-server-iceberg`) reads a database's change log and writes Iceberg tables itself: one pod per source, no Kafka, no Spark. Its offsets and schema history are Iceberg tables too, so it keeps no volume. Kafka (with Debezium's Kafka Connect, and Iceberg's own Kafka Connect sink) is the way when many readers want the same changes, or for streaming (phase 9's events); it's a service of its own to run, and nothing needs it yet. A source can move to it later without its tables changing.

**What a project says.** A source of 8.3 copies too: `sources[].copy: {tables: [schema.table, ...]}`. The same connection: for change capture, its account needs what the database's log asks for (PostgreSQL: `REPLICATION` and a publication of the tables, with `wal_level=logical`; SQL Server: CDC enabled on the database and the tables; MySQL: the binlog in row format; Oracle: LogMiner), which is the database administrator's to grant, and the project editor says so.

**What the platform makes.** On Kubernetes, the operator makes a Deployment per copying source (as it makes a project's Airflow), its settings in a Secret (the connection's password among them): Debezium's source connector for the kind, the first copy (`snapshot.mode=initial`) and then every change; the Iceberg sink writing to Nessie's REST catalog as the lakehouse's own Buckets account (as Trino does), in upsert mode keyed by each table's primary key (a table without one is appended to), deletes applied (`upsert-keep-deletes=false`), format version 2 (equality deletes, which Trino reads; nightly maintenance compacts them), new columns added as they come (`allow-field-addition`). The tables land in the project's namespace as `<source>_<table>` (`finance_invoices`), so the project's rules (Ranger's, through Trino) apply to them as to any of its tables: masks, row filters, readers' tables. Its offsets and schema history are tables in a namespace of the platform's (`storscale_cdc`), which no project's policies name. In Compose, no copying (as no project Airflows): the operator can't start containers there.

**Seen and alerted (R8.4).** The project's status has a `copy` part per source (its pod running, its last commit); the Deployment's state and Debezium's metrics (its lag behind the source, events, errors; Quarkus' `/q/metrics`, scraped by Prometheus) show on the project's page and the Health page, and a source stopped or behind by more than `copy.lagAlert` (default 15m) alerts the project (6.7).

**Lineage (R8.3).** catalog-sync adds an edge from each copied source table (in the catalog since 8.3) to its Iceberg table.

**Checks:** a source on the platform's Postgres in CI (the `kubernetes` jobs, with `wal_level=logical` set on it by a chart value CI alone sets): its table copied, a row inserted, updated and deleted there and seen in the Iceberg table within a minute or two, a column added and followed, the tables under the project's masks, the lag on the project's page.

**To check first:** the sink against Nessie's REST catalog and Buckets (its catalog settings are Iceberg's own, passed through), its table naming (`table-namespace`, `table-prefix`, `destination-regexp`), and that Trino 483 reads its tables, deletes and all.

**Order and effort:** the check above with a Postgres source in kind, a day; the spec, the operator's Deployments and the status, two days; metrics, the Health page and alerts, a day; lineage and the editor, a day. Which databases first: PostgreSQL and SQL Server (the use case's), MySQL with them (the same work); Oracle after, LogMiner being a project of its own.

### Query sources (use case 8.3), how (design, 2026-10-17; in 0.10.0 and 0.11.0)

**Built:** the spec (`sources`), the operator's catalogs (as its Trino account `platform-operator`, which Ranger lets make and drop catalogs and nothing in them) and the credentials files (`trino-sources`: on Kubernetes a Secret mounted into the coordinator and the workers; in Compose a volume), Ranger's rules (editors and pipelines read, readers where `readers` says, no one writes), the project editor's Sources section, and the `sources` suite (a source on the platform's own Postgres: read, not written, readers only when allowed, the password in none of Ranger's audit). Trino runs with `catalog.management=dynamic`, its catalogs in `/data/trino/catalog` (the install's copied in as it starts; the sources' made again by the operator after a restart, within its resync). **Then (2026-10-17, after 0.10.0):** a source's `schemas` (the only ones it may read), its readers' `masks` and `rowFilters` (tables as `schema.table`), its tables in the catalog (catalog-sync ingests every source's catalog beside `iceberg`, as OpenMetadata's Trino account, which may see a source's metadata and read none of its data), and the SQL page's schema browser for them. The project editor takes all of it. **Left:** the sources' lineage into the project's tables (R8.3, with 8.1).

**What a project says.** `spec.sources`: each a name, a kind (`postgresql`, `mysql`, `sqlserver`, `oracle`), one of the project's connections (a Secret labelled for the project: `host`, `port`, `database`, `username`, `password`; a read-only user of the source's own), and whether its readers may read it (`readers`, default false: editors only).

**What the platform makes.** A Trino catalog per source, `<project>_<name>` (`sales_finance`), read-only by Ranger's rules: the project's editors (and readers, if allowed) may use and select from it, and no one may write. Its tables appear in the catalog (OpenMetadata) as the lakehouse's do; masks on them later, as the lakehouse's.

**How Trino gets the catalog.** Trino's catalogs are files read as it starts, or (`catalog.management=dynamic`, which Trino calls experimental) made and dropped by `CREATE CATALOG` and `DROP CATALOG` while it runs. The operator makes them dynamically, as Trino's `platform-operator` account, with the user and password not in the statement (which Trino logs, and Ranger's audit records) but in a credentials file the statement names: the JDBC connectors' `credential-provider.type=FILE` and `connection-credential-file`, read as the catalog is made. The files are in a Secret the operator keeps (`trino-sources`), mounted into the coordinator and the workers (each makes its own connector), and a source's file must be there (the kubelet's sync, up to a minute) before its catalog is made. The statement holds the URL (where the database is; a URL with a password in it is refused). **Tried first, and not there:** `${FILE:...}` in the statement's properties. Trino 483 resolves only `${ENV:...}` unless a secrets plugin is installed (airlift's `Bootstrap`: the `file` provider is a plugin's), so CI's first run had Trino refuse it (`No secret provider for key 'file'`).

**Limits (R8.9).** A source's queries run in the project's resource group (6.8); the connector's own (fetch size, pushdown) left as Trino's.

**Order and effort:** the check above, with a source on the platform's own Postgres in CI, a day; the spec, the operator's catalogs and Ranger's rules, two days; the catalog's ingestion of a source and the SQL page's schema browser, a day.

### One-command upgrades (built 2026-10-16)

Helm installs a chart's CRDs once and never upgrades them, so each release's notes said to apply the Project definition by hand first; the 0.5.0 upgrade of `storscale-da1` without it silently dropped `pipelines.repo` and the rest from every project. And a checkout's chart once pointed `storscale-da1` at images not yet published.

- **`storscale upgrade`,** on any cluster: checks the release (not mid-upgrade, not going backwards, its images published on ghcr.io), says what it will do and asks; applies the release's Project definition; `helm upgrade --reset-then-reuse-values` with the images pinned to the release; waits for every Deployment and StatefulSet to roll out and every pod to be ready, with the reasons of what isn't; prints the `helm rollback` that undoes it. `--dry-run` shows the CRD's changes and renders the chart against the cluster.
- **Tested in CI:** the `kubernetes` job installs the last release, makes a project under it, upgrades to the commit, checks the project kept every setting and is Ready, then runs every suite. That is one pair, the last release to the next; a wider matrix (each of the last few releases, and Kubernetes versions) is still to come, as are database backups before an upgrade.

## Phase 7: pipelines from git, how

Each project's DAGs come from its own git repo, as an Airflow 3 **DAG bundle** (`GitDagBundle`, from the git provider already in Airflow's image).

- **The spec:** `pipelines: {serviceAccount, repo, path, ref, credentials: {secretRef}}` (above). `ref` defaults to the install's own (`main` in development, a release branch or tag in production). Credentials are only ever a Secret's name; a repo URL with a user or token in it is refused.
- **Bundles:** one per project, `project-<name>`, with `subdir` from `path`, `git_conn_id` `git_<name>`, and a refresh about every minute. The platform's own DAGs stay in a local bundle.
- **The operator:**
  - serves the list of bundles (`GET /airflow/bundles`): the platform's own, and each project's whose connection is in place. Airflow 3.3 reads its bundles from its settings and has no API to add one, so the scheduler and the DAG processor start through a small wrapper (`stack/tools/airflow_bundles.py`) that reads the list and keeps it in a file the image's `airflow_local_settings.py` answers Airflow with whenever it asks: a task's process (forked from the scheduler as the task starts) finds a bundle the scheduler never knew, so only the DAG processor, which reads the list as it starts, is started again when the list changes. The same on Kubernetes and in Compose, with no rights to restart anything;
  - keeps each project's git connection in Airflow through its REST API, as the platform's account (its `Op` role may), from one of the project's connections (on Kubernetes a Secret in the namespace labelled `platform.storscale.io/connection=<project>`; in Compose `connections/<project>/<name>.json` in the platform's bucket). A Secret without the label, the platform's or another project's, is refused (added 2026-10-16): never on a command line or in a file. A token rotated in the Secret reaches Airflow within a minute;
  - reports it in the project's status (the `airflow` part).
- **SSH hosts:** the git hosts' keys are the install's (`pipelines.gitKnownHosts`), checked strictly; without them a host's key is taken the first time.
- **What runs:** each run records the commit it was created from (`bundle_version`), and its tasks run that commit; a rerun runs it again. A push reaches Airflow within the refresh interval; Airflow 3.3 has no refresh API for a webhook to call.
- **In the platform:** a project's Pipelines page lists the DAGs of its bundle (`bundle_name`), and the platform's own tagged `project:<name>`; a tag in another project's repo doesn't count. It names the repo, shows each pipeline's commit and each run's, and lists the repo's files that didn't load.
- **Guardrails:**
  - DAG IDs are global in Airflow, and when two bundles define the same one the last parsed wins, without a warning: the Pipelines page warns of a clash (from the DAG's versions, which name their bundles). Refusing one is later work.
  - Every project's code runs in one Airflow, which can read every connection: each project's git credential is a read-only deploy key or a token for its repo only. That limits the damage; it doesn't stop it (next section).
  - The git provider is pinned at 1.0.0: it hands git the token through a credential helper, for each fetch. Before it, a token could be stored on disk in the clone's git config.
- **Tests:** `pipelines_git`, with a git server of its own (git http-backend behind a token, in the test's container or pod), in Compose and on kind: push a DAG, create a project pointing at it, see the DAG appear and run at that commit; push a change and see the next commit, and a broken file named; check that the token isn't in Airflow's API, the bundle list, or what a task sees (the checkout's git settings, its environment, its log); delete the project and see its DAGs, connection and bundle go.
- **Status (2026-10-15):** built, as above.

### Keeping projects' DAG code apart (next, before a second team uses it)

Today every project's DAG code runs in one Airflow, as the same user, in the same containers:
- **What it could read:** every connection, so every project's git token; and the containers' environment, which was the platform's whole secret set (`.env` in Compose, the Secret `storscale-env` on Kubernetes). Step 1 takes the platform's secrets away; every connection stays readable until step 2. A DAG file runs as it's parsed, so a push to any project's repo is enough; no run is needed.
- **So:** until this is done, a team's repo is as trusted as the platform. One team, or several that trust each other, only.

The work, in order:
1. **Give Airflow only what it needs.** Its containers take named keys, not all of `storscale-env` (its database, Fernet, JWT and API keys, the platform's account, the pipelines' Keycloak secret), and none of the platform's admin secrets. Done on its own, it's the biggest gain.
   - **Built (2026-10-15):** the scheduler and DAG processor get Airflow's database, Fernet, API and JWT keys and nothing more of `storscale-env`; the API server also its Keycloak client secret; `airflow-init` also the platform's account and the pipelines' secret. Trino's certificate without its key. On Kubernetes a service account of Airflow's own, which may only read whether the setup steps are done (the platform's own could read every Secret). `pipelines_git` checks it, at parse time and in a task.
   - **What's left for steps 2 and 3:** Airflow's own keys are still there for any DAG to read. With its database password and Fernet key, a DAG reads every connection; with the JWT secret, it can sign itself an API token. The catalog's bot token (for lineage) is there too.
2. **An Airflow per project (decided 2026-10-15).** A DAG processor or a team per project can't do it in Airflow 3.3: the DAG processor must reach the database directly, parsing runs in a fork of it, and Airflow's security model says a DAG author "can potentially retrieve credentials from the parent process"; multi-team mode is experimental and shares signing keys and connections. So each project with a repo gets an Airflow of its own, and its code can only reach its own:
   - **What it is:** one pod per project (`project-<name>-airflow`): the API server, the scheduler (LocalExecutor) and the DAG processor, with one bundle, the project's repo. No Kubernetes token.
   - **Its memory:** the project's (`pipelines.memory`, 2Gi to 64Gi), or the install's (`pipelines.memory` in the chart's values, 3Gi). Split 3/8 to the API server, 7/16 to the scheduler, 3/16 to the DAG processor; each asks for half its limit.
   - **Its keys:** a database and a Postgres user of its own (`airflow_<name>`) on the shared Airflow database server, which no one else may connect to (the operator takes CONNECT from PUBLIC on every database there); its own Fernet, API and JWT keys and platform account, made once by the operator into a Secret `project-<name>-airflow`. Its connections: the repo's (`git_<name>`) and its pipelines' Keycloak client (`keycloak_pipelines`). Nothing else of the platform's.
   - **Who its tasks run as:** a Keycloak service account per project (`project-<name>-pipelines`), made by the operator with the secret it keeps (the realm may be imported afresh), a member of that project alone. The spec's `serviceAccount` is then the shared Airflow's only.
   - **Its UI:** at the platform's `/airflow/<name>/` (the gateway finds the Service from the path). People sign in with Keycloak, through the shared Airflow's client: the project's editors as Op, its readers as Viewer, anyone else refused. Each Airflow has its own cookies.
   - **The operator:** creates and upgrades it from the chart's template (`templates/airflow-project.yaml`, a ConfigMap the operator fills in), and deletes it: the Deployment and Service, the database and user, the Keycloak client, then the Secret. Deleting a project, or its repo, drops its Airflow database and its run history. It has rights on Deployments and Services in the namespace, and Postgres' admin password.
   - **The platform:** platformd talks to each project's Airflow, with the password in its Secret, for the Pipelines page; "Open in Airflow" opens the project's own.
   - **The shared Airflow:** keeps the platform's own DAGs (`sales_ingest`, `lakehouse_identity`), and no project's repo, on Kubernetes.
   - **Compose:** one shared Airflow with the projects' bundles, as before: for development on one machine, by one team. The operator in Compose can't start containers, by design.
   - **Lineage:** a project's Airflow reports to the catalog with a bot token of its own, later; until then its runs have no lineage.
   - **Status (2026-10-16):** built, with unit tests; not yet run on kind.
3. **A test that it holds:** `pipelines_git`, on kind: the shared Airflow doesn't take the repo; the pipelines run as the project's own service account, in the memory it gives; the UI is at `/airflow/pipes/`, for the project's editors and no one else; a DAG, as it's parsed and as it runs, finds no platform secret, no Kubernetes token, and can't connect to another tool's database with its Airflow's own user; deleting the project takes its Airflow away.
- **Decided (2026-10-16):** each project's Airflow UI gets a path; each project sets its own memory, with the install's default.
- **Effort:** step 1 about 2 days (done); step 2 about 10 to 14; step 3 about 2.

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
13. **What commercial platforms have that our users need (added 2026-10-16):** the company delivers files to clients and partners, and holds consumer data; a second team may share the install. So: getting data out becomes phase 9 (governed AI, usage and cost, and self-service move to 10, 11 and 12); limits per project, and retention and erasure, join phase 6; a Connections tab (credentials given to the platform, not to the cluster) and scheduling a notebook join phase 7; querying databases in place (Trino's catalogs) starts phase 8. Saved queries, a CLI and API, and disaster recovery stay to decide.
14. **The data as it was, made visible (added 2026-10-16):** Iceberg keeps every version of a table, but only SQL can reach it. Phase 6 adds a table's history on its page, the data as of a date, a month-end close or a delivery, tags that keep a close beyond retention, and deliveries that record what they read, so any file or report can be reproduced. Its tension with erasure (a kept close against erasing a person from old versions) is open, for the privacy team and Finance together.
15. **What the data engineers asked for (added 2026-10-18):** one-click connectivity (Salesforce, HighSpot, Gong, SAP), visual transformation as in Magic ETL, a governed semantic layer in the business's words (Customer, Revenue, Campaign, Booking), self-service BI over it, catalog and discovery, and AI as another consumer of the same governed data. Each was partly there (dlt imports, per-project MetricFlow models, Superset over tables, OpenMetadata, the MCP server over metrics), so they became: one-click connectors in phase 8 (8.4: Salesforce first, then Gong and HighSpot, then SAP both as S/4HANA by OData and as HANA or BW databases); a new phase 10, the business layer (a company-wide semantic model, Superset and Power BI on it, certified data products, agents and DevAssist on them); and visual transformation as phase 11, moved from the last phase and made a canvas that writes dbt models, so visual pipelines are reviewed and released like code. Governed AI, usage and cost, and data apps and ML follow as phases 12 to 14. Open: OpenMetadata or Collibra as the catalog of record (OpenMetadata now; a Collibra sync designed as optional).
