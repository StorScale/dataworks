# Changelog

All notable changes to StorScale Platform are recorded here, in the [Keep a Changelog](https://keepachangelog.com/) format. The project uses [semantic versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] - 2026-10-12

For a real cluster: the platform behind an ingress that ends TLS, with a private CA, on Buckets 1.19.0. Installed on one (Kubernetes 1.27, ingress-nginx, a corporate Vault PKI through cert-manager), where every suite but the browser's passes, as it does in CI's new `kubernetes-https` job.

### Added
- **The chart trusts a private CA,** for a platform whose certificate a company's own CA signs: give it as PEM (`--set-file global.ca.certs=root-ca.pem`), or name a ConfigMap that holds it (`global.ca.configMap`). The services call the platform at its public address too, so each one that does trusts the CA as browsers do:
  - An init container adds it to the public CAs, as a PEM bundle and a Java truststore. Python, Go, curl and boto3 read the bundle through their usual variables (`SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, ...), and Java (Trino, OpenMetadata) through `JAVA_TOOL_OPTIONS`.
  - The bundle also holds Trino's own certificate (Secret `trino-tls`). `REQUESTS_CA_BUNDLE` takes the place of a `requests` session's own `verify=`, which is how the Trino client trusts Trino.
  - Notebook pods get the same, through JupyterHub.
  - Buckets, whose pods its operator makes, reads the CA from `SSL_CERT_FILE` (bucketsd uses OpenSSL), in place of its image's list of public CAs.
  - Without `global.ca`, the chart renders as before.
- **The platform can sit behind an ingress that ends TLS,** at an address with no port (`global.scheme: https`, `global.port: 443`):
  - The port is left out of the platform's URLs when it's the scheme's own. The gateway listens on a port of its own (`gateway.port`, 8800), over plain HTTP.
  - The gateway passes on the scheme people used (`X-Forwarded-Proto`) from the proxies it trusts: with the ingress on, those in private ranges (`gateway.trustedProxies`). Superset (`ENABLE_PROXY_FIX`) and Airflow's API server (`--proxy-headers`, trusting the gateway with `FORWARDED_ALLOW_IPS`) build their sign-in callbacks from it.
  - Keycloak's realm takes the platform's address whole (`PLATFORM_URL`, `CATALOG_PUBLIC_URL`) for its redirect URIs and the MCP audience, so they follow the scheme too.
  - The pipelines' Keycloak connection keeps its scheme (`extra.scheme`).
  - The Ingress takes annotations (`gateway.ingress.annotations`), for cert-manager.

### Changed
- **Buckets 1.19.0**, up from 1.12.0, in Compose and the chart (bucketsd and its operator). It takes about a third of the memory under load. The operator's `BucketsCluster` only gains fields (`fips`, `scim`).
  - Since 1.15 each server keeps a copy of its audit log on its first drive, by default up to 10 GiB, as much as the chart's drives hold. The platform keeps 1 GiB (`buckets.auditLocalMax`).
- **A setup step that fails on Kubernetes says why.** Each try of a setup Job runs in a pod of its own (`restartPolicy: Never`), kept with its logs after the Job gives up. Before, the Job's one pod went with it, and its logs too.
  - `storscale up --k8s` prints a failed try's logs, and a crashing container's last run, as they happen. It stops as soon as a setup step is out of tries.
  - It also stops on a container that keeps crashing. Between crashes the container runs for a moment, which had reset its count, so it waited the whole hour.
  - CI's "What went wrong" also prints the Jobs' events and each crashed container's run before its restart.
- **CI tests the platform behind HTTPS with a private CA** (the job `kubernetes-https`), as on a real cluster: on kind, ingress-nginx ends TLS with a certificate from a throwaway CA that only the platform's settings know of. It runs every suite but the browser's. The first real install found three bugs this would have caught (Buckets', Trino's and Airflow's).
  - `deploy/kind/https.sh` makes the CA, the certificate and ingress-nginx, and points the platform's names at it. `deploy/kind/https.yaml` holds the values.
  - `storscale up --k8s` takes more values files (`STORSCALE_K8S_VALUES`), and `deploy/kind/dns.sh` a Service to point the names at.
  - An Ingress TLS entry without `hosts` covers the domain and `catalog.`, `access.`, `s3.` under it.
  - CI's "What went wrong" is a script, `deploy/kind/diagnose.sh`.
- **Setup steps wait for their database** on Kubernetes: `airflow-init`, `superset-init` and the catalog's migration start once PostgreSQL accepts connections (`wait.py tcp`). On a first install Airflow's often started first, failed, and was tried again.

### Fixed
- **Superset's dashboard and chart cards show their picture.** Superset's frontend asks for those fallback images at `/static/assets/images/...`, without its `/dashboards` root, and the platform answered with its own page. The gateway sends those requests to Superset.

## [0.3.0] - 2026-10-08

The first release: phases 0 to 5 of [the plan](docs/plan.md).

### Added
- **Phase 5: Kubernetes.**
  - **A Helm chart, `deploy/helm/storscale-platform`,** runs the whole platform with the stack's own configuration (through a `files/` link to `stack/`). Each Compose service keeps its name as a Kubernetes Service, so the tools' settings and the checks work unchanged.
  - **Dependencies:** Buckets runs under its operator (a `BucketsCluster` named `buckets`, from `oci://ghcr.io/storscale/charts/buckets-operator`), and the Spark Operator is installed (Kubeflow's chart).
  - **Notebooks are pods:** JupyterHub uses KubeSpawner on Kubernetes, with a volume per person.
  - **Projects are `Project` resources on Kubernetes.** platformd and the operator keep them through the Kubernetes API (`PROJECT_STORE=kubernetes`). The operator's finalizer keeps a deleted project until its access has been undone. A copy of each project and its status stays in the bucket, for the services that read projects there.
  - **Secrets** are generated on install and kept on upgrade (the Secret `storscale-env`), unless given in values. So are Trino's certificate and the realm's signing key. Files that one service writes for others become Secrets: the catalog's bot token, and Prometheus' Buckets token.
  - **Setup steps are Jobs,** named after the release's revision so each upgrade runs them again. Services wait for the steps they need, as Compose's `depends_on` does.
  - **One address:** on kind, one rule in the cluster's DNS makes the platform's names resolve to the gateway inside the cluster, as Compose's network aliases do (`deploy/kind/dns.sh`).
  - **`storscale up|status|logs|test|down --k8s`** runs the platform on a kind cluster of its own. From a checkout, it builds the images (`deploy/kind/images.sh`) and uses the checkout's chart; otherwise it installs the published chart at its version.
  - **`helm test`** runs the end-to-end suites. Every suite passes on kind.
  - **CI** installs the chart on kind and runs every suite. Releases publish the chart's images (`ghcr.io/storscale/platform-*`) and the chart (`oci://ghcr.io/storscale/charts/storscale-platform`).
- **A redesigned web app: the project is the frame.** The platform's look was a sidebar of tools. It's now one product, built around the project you're working in.
  - **A top bar on every page:** a project switcher, search over the project's tables, columns, metrics and pipelines (press `/`), the tools, and your account. There's one design system: IBM Plex Sans and Mono, one accent colour, and colour only for meaning (green passing, red failing, amber "limited for you").
  - **Home:** your projects with their tables, metrics, checks and last run; what needs attention (failing checks, failed runs); and recent pipeline runs.
  - **Each project's pages, at `/p/<name>`:**
    - **Overview:** tiles, how data flows, failing checks, what each role can do (yours marked), its people, and where it's applied.
    - **Data:** each table's columns and types, with what a reader doesn't see. It also has a preview of the rows *as you*, its lineage, and its checks.
    - **Metrics:** the semantic layer, restyled. Pick metrics and dimensions as chips; answers come as bars and a table.
    - **SQL:** a query editor with the project's schema beside it. It runs in Trino as you, in the project's namespace.
    - **Pipelines:** the project's Airflow DAGs (tagged `project:<name>`), their runs and the newest run's tasks. Editors can start a run.
    - **Notebooks** and **Dashboards** show JupyterLab and Superset in the frame.
    - **Access:** members, the roles, and the readers' limits.
  - **Administration (`/admin`)** gathers projects, the access matrix, monitoring (Grafana) and the administrators' tools.
  - Old addresses (`/projects/<name>`, `/projects/<name>/flow`, `/access`) redirect to their new pages.
- **API for the new pages:**
  - `POST /api/projects/<name>/sql` runs one statement as the caller. platformd hands it to `semanticd`, which runs it in Trino with the caller's token, so Ranger applies.
  - `GET /api/projects/<name>/pipelines` lists the project's pipelines and runs, and `POST /api/projects/<name>/pipelines/<id>/runs` starts a run, for editors only. Both go through Airflow's API as a platform account, `storscale-platform`, created by `airflow-init`.
  - The flow now carries each column's type and each table's description.
- **The stack is lighter, so it fits a 10 GB Docker VM with the browser suite running:**
  - Airflow's LocalExecutor keeps 4 workers instead of 32, which halves the scheduler's memory.
  - Ranger's admin server runs with a 512 MB heap instead of the 1 GB its start script pins.
  - Trino gets 1.5 GB instead of 2 GB, a heap of about 1.2 GB, and at most 512 MB for a query. The kernel had been stopping it when the VM ran short.
  - The browser suite waits for the pipeline run it starts to finish, so it never overlaps another suite's run.
- **Kubernetes, from CI's first full run:**
  - **People could get no Buckets credentials from a Keycloak token** when Buckets started before Keycloak: it reads Keycloak's OpenID configuration only as it starts. The first setup step now checks, and if so starts Buckets afresh: on Kubernetes it replaces Buckets' pods, and in Compose it restarts its server.
  - **Ranger's admin server gets 1.5 GiB.** At 1 GiB it was restarted, and each restart cost ten minutes, while its start script waited out the mark the interrupted one left.
  - **Notebooks may be shown in the platform's frame.** KubeSpawner dropped the setting that allows it, because no command was given.
  - **`storscale up --k8s` says what it's waiting for** every half minute: a setup step, an image, a crash. It stops early when a pod is stuck. Before, it waited in silence.
- **A first install on Kubernetes no longer times out.** Services wait for the setup steps, which wait for Keycloak, Buckets, Ranger and Trino, and with every image still to pull that takes longer than Kubernetes' default 10 minutes for a Deployment to become ready. The chart's Deployments now get an hour (`progressDeadlineSeconds`), and `storscale up --k8s` waits up to an hour.
- **Signing out always ends the Keycloak session.** A request still in flight when the platform's session ended (Home loads each project's tables, metrics and pipelines) came back 401, and the app's "sign in again" could beat the browser to Keycloak's logout, which signed the person straight back in.
- **The lakehouse setup step keeps Superset's Ranger policies.** It used to remove them, until the next step put them back, so every upgrade broke Superset's queries for about a minute.
- **Buckets 1.12.0**, up from 1.7.0, which frees what it allocates.
- **The catalog keeps working when the disk is nearly full.** OpenSearch made the catalog's indices read-only once the disk was 95% full, and then the pipeline's lineage reports hung until they timed out. That's the default for a cluster; on one node on a laptop, the disk thresholds are now off.
- The plan: a semantic layer that agents can use (phase 4). MetricFlow holds the definitions, there's an editor in the shell, and an MCP server lets agents query as the person or service account asking, under Ranger's rules.
- The plan: [docs/plan.md](docs/plan.md). It covers the architecture, the `Project` object, a map of Dataiku's features onto open-source tools, the phases and the decisions.
- **Phase 4:** a semantic layer that agents can use.
  - **`semanticd`:** holds each project's metrics, written in MetricFlow's YAML (0.213, as a library, without dbt). It compiles requests to Trino SQL and runs them as whoever asked, so Ranger's row filters and masks apply to agents as to people. Ranger's audit log names both the agent and the person.
  - **An MCP server at `/mcp`** for agents. It's an OAuth resource server with Keycloak as the authorization server, signed in through the client `storscale-agent`. Its tools are `list_projects`, `list_metrics`, `query_metrics` and `explain_query`.
  - **The Semantic layer page:** a project's metrics, a way to ask for them, how to connect an agent, and the YAML, which editors change and which is checked before it's saved.
  - **The `sales` project's model:** revenue, order count, customer count and average order value, by region, customer, card number or date.
  - **The `semantic` suite:**
    - The MCP server refuses requests without the right token.
    - alice's agent gets EU revenue only, and masked card numbers.
    - carol's agent gets nothing.
    - bob's change to revenue shows up in alice's agent's next answer, and alice can't make changes.
    - Ranger's audit log names both alice and the agent.
- **Phase 3:** the catalog, lineage and data quality.
  - **OpenMetadata 2.0.4** as the catalog. It signs people in with Keycloak, and someone signed in to the platform needs no second password. It's trimmed to fit: no Airflow of its own, OpenSearch with no plugins and a small heap, and its own small Postgres.
  - **`catalog-sync`** ingests Trino's tables into the catalog and runs each project's data-quality checks as OpenMetadata tests.
  - **Checks in the project spec:** `spec.tables.checks`, one of `unique`, `notNull` or `rowCount`. The `sales` project checks that order IDs are unique, card numbers are never missing, and orders isn't empty.
  - **Column-level lineage from pipelines:** the sales pipeline now also rebuilds `orders_by_region`, and reports that step to the catalog as an OpenLineage event, column by column and under its own name. One of its orders has no card number, which the project's check catches.
  - **The Flow view:** each project's tables, the pipelines between them (column by column), and their checks, with failing checks called out. Its data comes from the catalog through `GET /api/projects/<name>/flow`.
  - **The Catalog** is on the platform's sidebar and opens in its own tab.
  - **The `catalog` suite:** bob runs the pipeline, and the suite finds its lineage and pipeline in the catalog, the failing check with its reason, both in alice's Flow view, and nothing for carol. It also checks that the catalog's sign-in needs no second password.
- **The tools open inside the platform in Safari too.** They now sit on the platform's own address, at paths: `/notebooks/`, `/dashboards/`, `/pipelines/`, `/monitoring/`, and Keycloak at `/sso/`. They used to have names of their own (`notebooks.storscale.localhost`), and Safari blocks the cookies of frames from any other origin, so each tool's sign-in failed inside the platform. The browser suite now runs in WebKit as well as Chromium. platformd's session cookie is no longer marked Secure over plain HTTP, because Safari dropped it.
- **The stack fits a 10 GB Docker VM:** the Java services' heaps are set (OpenMetadata, OpenSearch, Keycloak, Solr, Nessie, and a 2 GB limit for Trino), and long-running services restart if they stop. Running out of memory had killed Trino and the test browser.
- **The notebook checks stop the servers they start.**
- **Phase 2:** projects.
  - **What a project is:** a team's tables (an Iceberg namespace), files (a bucket) and pipelines (a Keycloak service account), and who may use them. Members are Keycloak groups or people, as readers or editors. Readers can be narrowed to some tables, with row filters and column masks.
  - **`platform-operator`:** applies each project to Keycloak (groups and their members), Ranger (the same groups, and the tables' policies), Buckets (the bucket and its policies) and Nessie (the namespace). It writes back each project's status, applies it again when it changes and every minute, and undoes it when it's deleted. The data stays.
  - **The Projects pages:** a list, each project's members and what each role gets, the operator's status, and an editor (a form, or JSON). The Access page shows who has which role in which project. Administrators (engineers) make and change projects; everyone else sees the projects they're in.
  - **The API:** `GET`, `PUT` and `DELETE` on `/api/projects/<name>`, and `GET /api/access`.
  - **The `Project` custom resource** (`deploy/crds/project.yaml`) is ready for Kubernetes (phase 5). Until then, projects live in a Buckets bucket.
  - **The `sales` project now grants the guides' access to the sales tables and files.** It replaces the setup scripts' grants. Its files moved to the `sales` bucket: `datasets/` for the shared data, and `landing/` for the pipelines.
  - **The `projects` suite:** an administrator creates a project and its member gets its tables and bucket while nobody else does. Deleting `sales` takes its access away in Trino, Buckets and Keycloak, but keeps its data. Creating it again gives the access back.
- **Phase 1:** the platform's web app at `http://storscale.localhost:8800`.
  - **What's in it:** one sign-in with Keycloak, a sidebar with every tool, a Home page with each person's tools and access, and sign-out (which also ends the Keycloak session). People in neither group are refused.
  - **The tools open inside it:** notebooks (JupyterLab), SQL (Superset's SQL Lab), dashboards, pipelines (Airflow) and monitoring (Grafana), each already signed in. A tool stays where it was when you switch to another.
  - **`platformd`:** the backend, in Go. It signs people in with OIDC (authorization code with PKCE), checks the ID token against the gateway's issuer, keeps sessions server-side, lists each person's tools, and serves the web app (React and TypeScript).
  - **The image:** `ghcr.io/storscale/platformd`, multi-arch, signed on release tags. `storscale up --dir stack` in a checkout builds it from source.
  - **The `shell` suite:** a Chromium test of the whole flow. alice signs in once and reaches every tool; carol is refused; signing out ends the Keycloak session. `storscale test` runs it with the other suites.
- **Keycloak's signing key is kept** across restarts, so a restarted Keycloak no longer invalidates every token, or the keys Trino and Buckets have cached.
- **Phase 0:** `storscale up` runs the whole platform on one machine with Docker Compose. That's Buckets, Keycloak, Nessie, Trino, Ranger, JupyterHub, Superset, Airflow, Prometheus and Grafana.
  - **One domain:** a gateway gives every tool its own name on one port (`http://notebooks.storscale.localhost:8800`, and so on), with a home page linking them all. Keycloak issues every token for one issuer.
  - **The CLI:** the `storscale` command carries the stack inside it and has `up`, `status`, `urls`, `test`, `logs`, `down` and `version`.
  - **`storscale test`:** runs the Buckets integration guides' end-to-end checks against the one platform. That's the lakehouse, Superset, Airflow, JupyterHub and monitoring suites.
  - **Grafana:** people sign in with Keycloak. Engineers get Editor, analysts get Viewer, and anyone in neither group is refused. The monitoring suite checks all three.
  - **The drive-failure checks:** they make a drive unreadable, then check that it's reported offline, that the alerts start, and that it recovers. An emptied drive made a flaky check, because Buckets often formats it back before Prometheus sees it fail.
  - **CI:** go vet and the CLI's tests on each change. The whole platform is also started, and every suite run.
