# The stack

This directory is DataWorks on one machine: a Docker Compose project. `storscale up` runs it. The `storscale` binary carries a copy of this directory and writes it to your cache directory the first time it runs.

| Service | What it is | At |
|---|---|---|
| `gateway` | Caddy: the platform's one address, with a path per tool | |
| `platformd` | The platform's web app and its backend: Keycloak sign-in, the tools each person may open, projects | `http://storscale.localhost:8800` |
| `platform-operator` | Applies projects to Keycloak, Ranger, Buckets and Nessie, and keeps them that way | |
| `keycloak` | Sign-in for everything; realm `lakehouse`, with a signing key kept in a volume | `/sso/` |
| `buckets` | Buckets, one server with four drives (EC 2+2) | `s3.` |
| `nessie`, `trino` | The Iceberg catalog and the SQL engine | Trino: `https://localhost:8443` |
| `ranger-admin` (with `ranger-db`, `ranger-solr`) | Who may query what, and the audit log | `access.` |
| `jupyterhub` | A notebook server per person | `/notebooks/` |
| `superset` (with `superset-db`) | SQL Lab and dashboards | `/dashboards/` |
| `airflow` (with its scheduler, DAG processor and `airflow-db`) | Pipelines | `/pipelines/` |
| `prometheus`, `grafana` | Monitoring; Grafana signs people in with Keycloak | `/monitoring/` |
| `openmetadata` (with `openmetadata-db`, `openmetadata-search`) | The catalog: tables, lineage, data quality; signs people in with Keycloak | `catalog.` |
| `catalog-sync` | Keeps the catalog in step: Trino's tables, the projects' checks | |
| `semanticd` | The semantic layer (MetricFlow) and the MCP server for agents | `/mcp` |
| `setup`, `setup-trino` | One-off configuration, safe to run again | |

## One address

The platform is at `http://STORSCALE_DOMAIN:STORSCALE_PORT` (`storscale.localhost:8800`).

**The tools shown in its frames are on paths of that address:**
- `/notebooks/` is JupyterHub
- `/dashboards/` is Superset
- `/pipelines/` is Airflow
- `/monitoring/` is Grafana
- `/sso/` is Keycloak

Each one is configured to run under its path (`c.JupyterHub.base_url`, `SUPERSET_APP_ROOT`, Airflow's `base_url`, `GF_SERVER_SERVE_FROM_SUB_PATH`, `KC_HTTP_RELATIVE_PATH`). That keeps them on the platform's origin, which matters for Safari: it blocks the cookies of frames from any other origin, subdomains included, so each tool's sign-in would fail inside the platform. The browser suite runs in WebKit as well as Chromium to keep this working.

**The tools that open in a tab of their own keep names of their own:**
- `catalog.` is OpenMetadata
- `access.` is Ranger
- `s3.` is Buckets' S3 API

**Name resolution:**
- **In the browser:** browsers resolve `*.localhost` to your own machine, so no hosts file is needed.
- **Inside the stack:** the same names are network aliases of the gateway. Services reach each other at the addresses a browser uses, and Keycloak issues every token for one issuer, `http://storscale.localhost:8800/sso/realms/lakehouse`.
- **The libcurl exception:** libcurl resolves `*.localhost` names to loopback itself and never asks Docker's DNS. So anything that uses it (JupyterHub's HTTP client) calls Keycloak at `http://keycloak:8080/sso` directly.

To use another domain, set `STORSCALE_DOMAIN`, and make its names resolve to this machine.

## Projects

A project ([projects/sales.json](projects/sales.json) is one) is a team's tables, files and pipelines, and who may use them. Administrators (engineers) make, change and delete projects in the platform's Administration pages, or through `PUT` and `DELETE` on `/api/projects/<name>`. platformd keeps them in the `storscale-platform` bucket.

`platform-operator` applies each project and writes back its status. It runs again when the project changes, and every minute anyway, because a Keycloak group's members can change without the project changing. For project `sales`:

| System | What the project makes |
|---|---|
| Keycloak | Groups `sales-readers`, `sales-editors` and `sales-pipelines`, holding exactly the project's members. An editor is in the editors' group only, so the readers' masks and row filters don't apply to them. |
| Ranger | The same groups, with each member in them, and the policies named `project:sales:…`. Those cover the namespace, its tables (or only some, for readers), and the readers' masks and row filters. |
| Buckets | The bucket `sales`, and a policy per group: readers read it, editors write it, and the pipelines write `landing/`. |
| Nessie | The namespace `sales`. |

Deleting a project removes its groups and policies. Its data, the bucket and the tables, stays until someone removes it on purpose.

**Outside projects:** the setup scripts still grant what isn't tied to any project:
- people's own files (`scratch`, `home`);
- running queries, and seeing the catalog;
- Superset's right to run queries as people.

The `projects` suite checks that a project's access appears in every system when the project is created, and goes when it's deleted.

## The catalog

OpenMetadata 2.0.4 is trimmed to fit a laptop:
- it has no Airflow of its own;
- the server's heap is 640 MB;
- OpenSearch has every plugin removed (`catalog/opensearch`), a 384 MB heap, and starts in seconds instead of minutes.

Every 20 seconds, `catalog-sync` does three things:
- **Tables:** it ingests Trino's tables, as the service `trino`, through the service user `openmetadata`. Ranger lets that user read every table, to describe and check it.
- **Checks:** it runs each project's checks (`spec.tables.checks`: `unique`, `notNull`, `rowCount`) as OpenMetadata tests.
- **The bot's token:** it shares the catalog's ingestion-bot token in the `catalog` volume.

Two services use that token:
- **Airflow's pipelines** report their lineage, column by column, as OpenLineage events to `/api/v1/openlineage/lineage`. The sales pipeline does this for `orders_by_region` (`airflow/dags/lakehouse_identity.py`'s `report_lineage`).
- **platformd** reads a project's tables, lineage and checks for its Overview and Data pages (`/api/projects/<name>/flow`).

Each pipeline registers itself in the catalog before it reports, because OpenMetadata 2.0.4 fails to create the pipeline on its own.

**Signing in to OpenMetadata.** Its sign-in uses the person's Keycloak session, the same way the other tools' do. That needs `OIDC_MAX_AGE`, because by default it demands a fresh password; OpenMetadata copies the setting into its database on first start. Its web app keeps its token in a service worker, so the platform opens the catalog in its own tab through `catalog.storscale.localhost:8800/_storscale/launch.html`. That page starts the worker before signing in.


## The semantic layer, and agents

Each project can have a semantic model, kept in the project store as `semantic/<project>.yaml`; [projects/sales.semantic.yaml](projects/sales.semantic.yaml) is one. It's written in MetricFlow's YAML: semantic models over the project's tables, and the metrics made from them.

- **Editing:** a project's editors change the model in its Metrics page. `semanticd` compiles it with MetricFlow, without dbt, and refuses what doesn't compile, saying why.
- **Querying:** people (in the platform) and agents (over MCP) ask for metrics by name. `semanticd` compiles each request to Trino SQL and runs it **as whoever asked**, with their own Keycloak token. Ranger's policies, row filters and masks therefore apply to an agent exactly as to the person it works for.
- **Auditing:** each query starts with a comment naming the agent (the token's client) and the person, and Ranger's audit log keeps it.
- **Agents' access:** agents connect to `http://storscale.localhost:8800/mcp`, an OAuth resource server whose metadata names Keycloak. They sign in through Keycloak's client `storscale-agent`, as the person using them. That client's tokens are for both the MCP server and Trino.
  - The tools are `list_projects`, `list_metrics`, `query_metrics` and `explain_query`.
  - In Claude Code: `claude mcp add --transport http storscale http://storscale.localhost:8800/mcp`.

## Memory

The stack uses about 8 GB. The Java services' heaps are set explicitly (Keycloak, Nessie, Solr, OpenMetadata, OpenSearch, and Trino's 2 GB limit); without that, each JVM would size its heap from the machine's memory. Long-running services restart if they stop.

Keycloak's signing key is made once, in the `keycloak-keys` volume, and kept. Tokens stay valid when Keycloak restarts, and Trino and Buckets keep the keys they've fetched. The realm itself is imported afresh on each start.

## Where it comes from

The services and their configuration come from the tested integration guides in [Buckets](https://github.com/StorScale/buckets/tree/main/examples) (`examples/{common,lakehouse,superset,airflow,jupyterhub,monitoring}`, at Buckets commit `3f3267e`). Buckets' monitoring rules and dashboards come from `operator/helm/buckets-operator/monitoring`. The changes for running them as one platform:
- **Grafana signs people in with Keycloak:** engineers get Editor, analysts get Viewer, and anyone in neither group is refused. Grafana's `admin` account stays, for when Keycloak is down. Ranger still has only its own `admin` account.
- **One domain:** the tools' URLs, the Keycloak realm's redirect URIs, and Trino's and Buckets' token issuer.
- **Policies that add up:** the analysts' and engineers' Buckets policies combine what the lakehouse and the notebooks each need. Ranger gets one list of policies, covering the lakehouse, Superset and Airflow.
- **One Buckets:** four drives instead of one, so the monitoring checks can fail a drive.
- **The checks:** `tests/run.py` runs every guide's checks in turn. It loads the sales tables afresh before the suites that read them, and runs the drive-failure checks last. Those make a drive unreadable rather than emptying it, as the guide does. Buckets formats an emptied drive back within seconds, so Prometheus doesn't always see it fail.
