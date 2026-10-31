# 17. Operating the platform

This chapter is for the people who run the platform: installing it on one machine with Docker Compose or on Kubernetes with its Helm chart, checking it and being told when something breaks, the audit log, the Governance and Usage pages, limits, table maintenance, backups and restore, upgrades, growing Buckets, the install's settings, and the platform's API and Python package. Most of it is for administrators (members of the `engineers` group on the example install); the Usage and Governance pages are also for finance, the governance group and the data-protection approvers.

In this chapter:

- [Installing](#installing)
- [Checking an install: storscale test](#checking-an-install-storscale-test)
- [Health and the administrators' alerts](#health-and-the-administrators-alerts)
- [The audit log](#the-audit-log)
- [Governance](#governance)
- [Usage and cost](#usage-and-cost)
- [Limits](#limits)
- [Table maintenance](#table-maintenance)
- [Backups and restore](#backups-and-restore)
- [Upgrades](#upgrades)
- [Buckets capacity](#buckets-capacity)
- [The install's settings](#the-installs-settings)
- [The Python package and the API](#the-python-package-and-the-api)

See [Lab 21](../guide.md#lab-21-who-did-what) and [Lab 22](../guide.md#lab-22-operate-it) in the lab guide.

---

## Installing

### What it's for

The platform runs the same way on one machine (Docker Compose, for trying it and for development) and on a Kubernetes cluster (the Helm chart, for real use). The `storscale` command starts, checks, upgrades, backs up and restores it.

### Who can do it

Whoever runs Docker on the machine, or has `kubectl` and `helm` rights on the cluster's namespace. Nothing here goes through the platform's own sign-in.

### On one machine (Docker Compose)

You need Docker with Compose 2.24 or later, about 10 GB of memory for Docker, and Go 1.26 or later to build the command.

```bash
go build -o bin/storscale ./cmd/storscale
bin/storscale up --dir stack      # this checkout's stack: builds the platform's images too
bin/storscale urls                # where each tool is
```

Then open `http://storscale.localhost:8800` and sign in as `alice`, `bob` or `carol`; their password is `LAKEHOUSE_USER_PASSWORD` in `stack/.env`, where every password is a local test value.

| Command | What it does |
|---|---|
| `storscale up [--port N]` | Starts the platform (builds its images the first time) and waits for it; prints the tools' addresses |
| `storscale status` | The services (`docker compose ps --all`) |
| `storscale urls` | Where each tool is |
| `storscale logs [service ...]` | Follows the logs (the last 100 lines first) |
| `storscale test [suite ...]` | The end-to-end checks |
| `storscale down [--volumes]` | Stops it; `--volumes` also deletes its data |
| `storscale version` | Its version |

`--dir DIR` uses the stack in a checkout instead of the one built into `storscale` (which runs the released images at its own version). The environment variables `STORSCALE_DOMAIN` and `STORSCALE_PORT` change the address (`storscale.localhost`, `8800`); with rootless Docker set `DOCKER_SOCK=/run/user/<uid>/docker.sock` so JupyterHub can start notebooks (`storscale up` warns when it's needed).

### On a kind cluster of your own

`storscale up --k8s` makes a kind cluster called `storscale` and installs the chart as the release `storscale` in the namespace `storscale` (the published chart at `storscale`'s version, or `--version`; with `--dir stack`, the checkout's chart and images built from it). It needs `kind`, `kubectl` and `helm`. `status`, `urls`, `logs <service ...>` (required on kind), `test`, `backup`, `restore` and `down` take `--k8s` too; `down --k8s --volumes` deletes the cluster. `STORSCALE_K8S_VALUES` names more values files for the chart.

### On a real cluster

It needs about 12 to 16 GB of memory, 4 or more CPUs, about 60 GB of disk, Helm, a default StorageClass (or `storage.storageClassName` and `buckets.storageClassName`), and for each project with a pipelines repo, its own Airflow's memory (`pipelines.memory`, 3Gi by default). With TLS at an ingress and a private CA, a values file such as:

```yaml
global:
  domain: platform.example.com
  scheme: https
  port: 443
gateway:
  service: {type: ClusterIP}
  ingress:
    enabled: true
    className: nginx
    annotations:
      cert-manager.io/cluster-issuer: my-issuer
      nginx.ingress.kubernetes.io/proxy-body-size: "0"     # S3 uploads
    tls:
      - secretName: storscale-tls
        hosts: [platform.example.com, catalog.platform.example.com, access.platform.example.com, s3.platform.example.com]
trino:
  service: {type: ClusterIP}
```

```bash
helm install storscale oci://ghcr.io/storscale/charts/storscale-platform --version 0.31.0 \
  -n storscale --create-namespace -f platform.yaml --set-file global.ca.certs=root-ca.pem
kubectl -n storscale get pods,jobs,projects -w      # the first install takes 10 to 20 minutes
helm test storscale -n storscale --logs --timeout 90m
```

How it behaves and what to watch:

- The platform's four names, `domain` and `catalog.`, `access.`, `s3.` under it, must resolve to the ingress from people's browsers and to the gateway from inside the cluster (hairpin traffic through the load balancer, or a CoreDNS rewrite as `deploy/kind/dns.sh` does).
- Every secret is generated on install and kept on upgrade, in the Secret `storscale-env`; give one in `secrets:` to choose it. The example people's password: `kubectl -n storscale get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d`.
- Keycloak runs in development mode with its realm imported at each start: fine for a trial, not for real users yet. The example users always exist (`examples.users` doesn't switch them off yet).
- A Spark Operator another release installed owns the Spark CRDs: install with `--set spark-operator.enabled=false`.
- Upgrade with `storscale upgrade`, never a bare `helm upgrade` ([Upgrades](#upgrades)).

### From a registry of your own (GitLab and Harbor)

A company that keeps its images in its own registry builds them there: `.gitlab-ci.yml` runs on the company's GitLab
runner and pushes the same ten images as the public build (linux/amd64) to a Harbor project, and on each release tag
the chart too, its `images.registry` set to that project. Pushes to the default branch push `<short sha>` and the
branch's name; a tag `v<version>` pushes `<version>` and `latest`; another branch builds everything without pushing.

Set up, once, in the GitLab project's or its group's **Settings → CI/CD → Variables**:

| Variable | What it is |
|---|---|
| `HARBOR_REGISTRY` | The registry's host, such as `harbor.example.internal` |
| `HARBOR_PROJECT` | The Harbor project for the images and the chart, with a folder in it if wanted (`vericast/dataworks` puts them at `vericast/dataworks/platformd` and so on) |
| `HARBOR_USER`, `HARBOR_PASSWORD` | A robot account of that project with push (and pull) permission; the password masked |
| `HARBOR_CA` | A **File** variable: the CA that signed the registry's certificate, when it isn't a public one |

The runner is the one tagged in the file (`default: tags`). Jobs build with rootless BuildKit, so the runner needs no
privileged pods, but it must reach the base images' registries (Docker Hub, quay.io, gcr.io) and, for the chart,
`ghcr.io` and `kubeflow.github.io` (its dependencies). Install from it as from the public chart:

```bash
helm install storscale oci://harbor.example.internal/<project>/charts/storscale-platform --version 0.34.0 \
  -n storscale --create-namespace -f platform.yaml --set-file global.ca.certs=root-ca.pem
```

An install from `ghcr.io` moves to Harbor at its next upgrade, its chart and images both:

```bash
storscale upgrade --context <cluster> --chart oci://harbor.example.internal/<project>/charts/storscale-platform \
  --version 0.34.0 --ca-file root-ca.pem --set images.registry=harbor.example.internal/<project> --dry-run
```

The `--set images.registry` is kept with the install's values, so later upgrades take Harbor's images too. The nodes
pull them anonymously when the Harbor project is public; a private one needs an image pull Secret in the namespace.

## Checking an install: storscale test

`storscale test` runs every end-to-end suite, or those named: `shell` (the web app, in a browser), `lakehouse`, `superset`, `airflow`, `jupyterhub`, `catalog`, `semantic`, `projects`, `monitoring`, and the rest in `stack/tests/` (`deliveries`, `erasure`, `health`, `limits`, `usage`, `audit`, ...). On Compose they run in the `test` service; with `--k8s` (or `storscale upgrade --test`) as `helm test`, the ConfigMap `storscale-tests` saying which. The lab guide is a suite too, run only when named (`storscale test --dir stack guide`), and it leaves what it makes.

A suite prints each check as it goes and fails the command if any failed: `failed: shell, the other suites`.

## Health and the administrators' alerts

### What it's for

The administrators need to know, before anyone tells them, that a service is down and what people can't do meanwhile; and to be told in their channel. Everyone else needs to know what isn't available just now.

### Who can do it

Administration → **Health** is the administrators'. Every signed-in person gets the short form: the home page's notice *Some of the platform isn't available just now: ... Its administrators have been told.* Muting and the test alert are administrators only (`administrators only`).

### Set up

Where alerts go is a Secret of the platform's own, `storscale-alerts`, read every half minute, so it changes without a restart:

| Key | What |
|---|---|
| `teamsWebhook` | A Teams channel's Workflows webhook (the alert is an Adaptive Card) |
| `slackWebhook` | A Slack incoming webhook |
| `emailTo` | Addresses, comma-separated |
| `smtpServer` | `smtp.example.com:587` (STARTTLS when it offers it) |
| `smtpFrom`, `smtpUser`, `smtpPassword` | The sender and its sign-in |

On Kubernetes:

```bash
kubectl -n storscale create secret generic storscale-alerts \
  --from-literal=teamsWebhook='https://prod-00.westeurope.logic.azure.com/workflows/...' \
  --from-literal=emailTo=platform-team@harbor.example \
  --from-literal=smtpServer=smtp.harbor.example:587 --from-literal=smtpFrom=storscale@harbor.example
```

On Compose the same keys go in a JSON object at `secrets/storscale-alerts.json` in the `storscale-platform` bucket.

Then on Administration → **Health**, **Where alerts go** says what's set, and **Send a test alert** sends one to each and says, per target, `sent` or why not.

### How it behaves

- platformd checks every service every 15 seconds: the gateway, Keycloak and its database, Buckets, Nessie, Trino and its limits' database, Ranger and its database, the metrics and SQL service (semanticd), the project operator, Superset and its database, Airflow (its database, scheduler and DAG processor), JupyterHub, the catalog (OpenMetadata), its search and database, Prometheus and Grafana.
- The page lists each service: its state (**up**, **down**, **unknown**) and since when, **Why** (on Kubernetes, from its pods: `FailedMount: ...`, `CrashLoopBackOff (7 restarts)`), what it's **Used for**, and **Mute 4 h** / **Unmute**. The banner says *All N services are up*, or how many are down and what people can't use.
- **Other problems** lists the monitoring rules firing in Prometheus (Buckets' drives offline, capacity low, errors), and on Kubernetes setup steps that gave up, copies (change capture) that stopped, and overdue backups.
- **Alerts** go to every target when a service goes down (`Trino is down`, with why and what it affects) and comes back (`... is back`); when a monitoring rule starts and resolves; when a setup step fails; when backups are overdue (`Backups are overdue`, none succeeded for 26 hours); and once a day in the platform certificate's last 14 days (`The platform's certificate ends in N days`). Services going down and monitoring rules firing aren't sent in the first five minutes after platformd starts, while everything starts too.
- **Recent alerts** shows what was sent since platformd started. A muted service's changes aren't sent; muting is recorded in the audit log (`alert.muted`), as is a test alert (`alert.tested`).
- The checks are Prometheus metrics too (`platform_service_up`, on platformd's port 9102); Administration → **Monitoring** opens Grafana.
- The API: `GET /api/health` (everyone gets `affected`; administrators the whole), `POST /api/health/mute` with `{"service": "trino", "hours": 4}` (up to a week; 0 unmutes), `POST /api/health/test`.

Each project's own alerts (a failed pipeline run, a failing check, a stale table, a budget, a bucket near its size) go to that project's people where its `notify` says; see [Projects](02-projects.md) and [Data quality](05-quality.md).

## The audit log

### What it's for

"Who read the customers' table last month, and who tried and was refused" is a search, not an investigation. The log holds two things: every access Trino allowed or refused (Ranger's audit), and everything people did in the platform (projects saved, approvals, uploads, erasures, model calls, sign-ins).

### Who can do it

| Where | Who |
|---|---|
| A project's **Audit** tab (`GET /api/projects/{name}/audit`) | Its editors, a role with `see-audit`, administrators. Holds the project's own records and every access to its tables. |
| Administration → **Audit** (`GET /api/audit`, `?project=` narrows it) | Administrators. Every project, and the platform's own records. |
| **Export CSV** (`?format=csv`, up to 1000 rows) | Administrators |

### Set up

Nothing to set: it's on in every install. On either page:

- **Person**: a user name; **Search**: a table, an action or words of a query; **Result**: *Allowed and refused*, *Refused only*, *Allowed only*; **Source**: *The platform and Trino*, *The platform's*, *Trino's*; **From** and **To**; press **Show**. **Older** pages back.
- API parameters: `user`, `q`, `result` (`allowed`, `denied`), `source` (`platform`, `trino`), `since`, `before` (RFC 3339), `limit` (default 100).

```bash
curl "$PLATFORM/api/projects/retail/audit?q=uploads_customers&result=denied&since=2026-10-01T00:00:00Z" \
  -H "Authorization: Bearer $TOKEN"
```

### How it behaves

- Both kinds are kept in Ranger's Solr: the platform's records for `audit.keep` (default `365d`), Ranger's own for 90 days, on a volume of `audit.storage` (5Gi).
- Trino's entries appear within about a minute, with the policy and the reason Ranger gave.
- The platform's records name the person, the action and the resource (`project:retail`, `project:retail/partner_orders`), whether it was allowed, a detail, and the client's address. Actions include `project.saved` (with the parts of the spec changed), `project.deleted`, `connection.saved`, `connection.removed`, `upload`, `upload.removed`, `import.saved`, `import.reset`, `pipeline.started`, `flow.saved`, `tag.made`, `tag.removed`, `release.planned`, `release.applied`, `classification.*`, `product.*`, `delivery.approved`, `delivery.sent`, `deliveries.searched`, `erasure`, `agent.registered`, `agent.approved`, `agent.revoked`, `agent.asked`, `model.registered`, `model.approved`, `model.called`, `llm.asked`, `tool.called`, `knowledge.*`, `app.*`, `role.saved`, `role.removed`, `template.saved`, `library.saved`, `maintenance.asked`, `alert.muted`, `alert.tested`, `signed.in`, `signed.out`.
- Nothing in the log can be changed from the platform. A write to the log never holds a request up; if the log can't be written, platformd logs `audit` with the error.

## Governance

### What it's for

The audit log says what happened; the Governance page says where things stand now, across every project: what waits for someone's approval, the agents and their spend, the rules readers are under, the data products, and the erasures with their deadlines. It changes nothing; each row links to where the decision is made.

### Who can do it

Anyone with the platform permission `see-governance`: administrators (Administration → **Governance**), and the built-in platform roles **governance** (`products.certifierGroups`) and **data-protection** (`deliveries.approverGroups`), who reach it from the account menu's **Governance**. `GET /api/governance`.

### How it behaves

The page's cards:

| Card | What |
|---|---|
| **Awaiting approval** | Agents not approved (waiting for *an editor of retail other than alice*), deliveries not approved (what they still need), requests to read a data product; with since when |
| **Agents** | Every project's agents: owner, tools, what runs on what schedule, the last run, spend against budget |
| **What readers see** | Each protection tag across the install: its mask and who's excepted |
| **Readers' rules, by project** | Readers, the only tables they see, row filters, masks, tagged columns, and the project's people's tables |
| **Data products** | Each product, owner, whether certified, consumers |
| **Erasures** | Each erasure with its due date, who, and **erased** or how many tables or files were left (only to those with `protect-personal`) |

A summary line counts them: `3 awaiting approval; 4 agents (3 approved); 1 of 2 products certified; 1 erasures recorded`.

## Usage and cost

### What it's for

Finance wants each project's month in money; a project's editors want to know what drives it. The platform counts each project's Trino CPU time, what it keeps, and its model calls, and prices them by the install's rate card.

### Who can do it

| Where | Who |
|---|---|
| A project's **Usage** tab (`GET /api/projects/{name}/usage?month=2026-10`) | Its editors (`see-usage`), administrators, the install's usage viewers |
| Administration → **Usage**, or the account menu's **Usage and cost** (`GET /api/usage?month=2026-10`) | Administrators, and the platform role **finance** (`usage.viewerGroups`, permission `see-all-usage`) |

### Set up

- The install's rate card, `usage.rates` (chart) or `USAGE_RATES` (platformd's environment, JSON): `{"currency": "USD", "cpuHour": 0.05, "storageGBMonth": 0.023}` by default. Model calls are priced already, by the gateway.
- Who sees every project: `usage.viewerGroups` (`USAGE_VIEWER_GROUPS`, comma-separated); empty means the administrators.
- A project's monthly budget, in the rate card's currency, under its `spec`:

```json
{"cost": {"budget": 50}}
```

### How it behaves

- **Queries:** Trino reports each finished query to platformd; a query counts for the projects whose tables it read (split evenly when it read several), with its CPU time, the bytes it read and who ran it. Each platformd keeps its counts a minute apart.
- **Storage:** once a day, each table's data files (Iceberg's `$files`) and the project's bucket (Buckets' count) are sampled; a GB-month is priced from the samples.
- **Model calls:** what the gateway says the project's keys spent this month.
- The Usage tab shows this month against the last (**What**, **Used**, **Cost**, **Rate**), then **Who ran the queries**, **The costliest queries** (with their SQL) and **The largest tables**. The administrators' page lists every project's month: **Total**, **Queries**, **Storage**, **Model calls**, **Budget**, **Last month**.
- At 80% of a budget the project is alerted (`the project has used 80% of its monthly budget (...)`), and again past 100% (`... past its budget of 50.00`, *Nothing is stopped: see what drives it on the Usage tab.*). Limits stop things; budgets don't.
- Not yet: Spark isn't counted.

## Limits

### What it's for

One project, or one person, can't slow the others. Trino's resource groups cap how many queries run and wait at once and how much memory a project's queries take; a project's bucket can have a size limit.

### Who can do it

The install's defaults are the chart's (`limits.*`); a project's own are on its spec, which administrators edit.

### Set up

On the project's **Edit** → **JSON**, under `spec`:

```json
{"limits": {"queries": 8, "queued": 100, "queryMemory": "4GB", "storage": "500Gi"}}
```

| Field | Meaning | Allowed | Install default |
|---|---|---|---|
| `queries` | Trino queries running at once | 1 to 100 | `limits.project.queries`: 4 |
| `queued` | Waiting; more are refused | up to 1000 | `limits.project.queued`: 40 |
| `queryMemory` | Trino memory for its queries | `4GB`, `512MB`, `50%` | `limits.project.queryMemory`: `50%` |
| `storage` | The project's bucket's size (needs `files`) | `500Gi`, `2Ti` | `limits.project.storage`: none |

A person's own queries (`limits.person.queries` 4, `queued` 100) and the whole install's (`limits.total.queries` 20, `queued` 1000) are the chart's only. On Compose the operator uses the same defaults, or `LIMITS_PROJECT_*`, `LIMITS_PERSON_*` and `LIMITS_TOTAL_*` in its environment when they're set.

### How it behaves

- The operator keeps Trino's resource groups (a group per project, per person, and one for the install) in a database Trino reads every second.
- A query counts against a project when one of its members names it (client tag `project:<name>`, which the SQL page, dashboards and metrics set) and for all the project's own pipelines; everything else counts against the person.
- A query past the queue is refused by Trino with `QUERY_QUEUE_FULL`, its message naming the group (`project-retail`), where it ran: on the SQL tab, in a dashboard, in a notebook.
- The bucket's size is a hard quota in Buckets. At 80% the project's people are alerted (`the bucket retail is 83% full (...)`), and when it's full (`... is full (...): new files are refused`); each resolves as the bucket empties.
- Notebooks are a person's: `notebooks.memoryLimit` (1G), `notebooks.cpuLimit` (2), `notebooks.storage` (1Gi).

## Table maintenance

### What it's for

Iceberg tables written often collect small files, and a table's history grows. Each night the platform compacts every project's tables, applies their retention, and lets the catalog's garbage collection delete history older than the window.

### Who can do it

It runs by itself. A project's editors (`run-pipelines`) start a round now: **Data** → a table → **Storage** → **Run now** (`POST /api/projects/{name}/maintenance`).

### Set up

| Setting | Default | What |
|---|---|---|
| `maintenance.at` (`MAINTENANCE_AT`) | `02:00` UTC | When platformd starts the nightly round |
| `maintenance.history.keep` | `P7D` | How far back the tables' history goes (Nessie's GC). Compose: `NESSIE_GC_KEEP` in `stack/.env` (`PT2M` for the lab) |
| `maintenance.history.schedule` | `30 3 * * *` | When Nessie's GC runs. Compose: daily, or every `NESSIE_GC_EVERY` seconds |
| A project's `tables.maintenance.paused` | `false` | `true` skips the project (compaction and retention) |

### How it behaves

- One project at a time, as Trino's `maintenance` account: each table's retention first (`tables.retention`, [Privacy](13-privacy.md#retention)), then `ALTER TABLE ... EXECUTE optimize`. Project tags past their `keepUntil` and delivery tags past `deliveries.keepFor` are removed at the start of the round.
- Nessie's GC then deletes history older than `history.keep`, and files no branch or tag needs; a tag (a close) keeps what it points to.
- Each table's **Storage** tab shows its files and snapshots, when it was last kept, and the step that failed if one did; a failed table is the project's alert (`<table>: its maintenance failed`). The page says *Table maintenance isn't set up on this install* when platformd has no maintenance account.

## External stores

### What it's for

Another platform's Iceberg tables (a Hive Metastore and its MinIO, say), which projects adopt where their files are while the move
to this platform goes on ([Adopted tables](03-data-in.md#adopted-tables-another-platforms-iceberg-tables-where-they-are)).

### Who can do it

Whoever installs the chart.

### Set up

The chart's `externalStores`, one entry per store, and a Secret in the release's namespace with the store's key (`accessKey`,
`secretKey`): a key that may **only read** the store's buckets. Nothing here writes to the store, and the key makes sure of it.

```yaml
externalStores:
  - name: lakehouse                        # projects adopt from lakehouse.<schema>.<table>
    catalog:
      type: hive_metastore                 # or rest (with uri and warehouse)
      uri: thrift://hive-metastore.lakehouse.svc.cluster.local:9083
    s3:
      endpoint: https://minio.lakehouse.svc.cluster.local
      region: us-east-1
      buckets: [warehouse]                 # the buckets its tables' files are in
    secret: lakehouse-store
```

```bash
kubectl -n <namespace> create secret generic lakehouse-store \
  --from-literal=accessKey=<read-only key> --from-literal=secretKey=<its secret>
```

The store's TLS certificate must be one the install trusts (`global.ca`).

### How it behaves

- Trino gets a catalog for each store, `ext_<name>`, read-only (`iceberg.security=read_only`), which only the platform operator
  may read (Ranger): it finds each adopted table's current metadata file there.
- The platform's `iceberg` catalog reads the store's buckets at the store's endpoint with its key (Trino's S3 security mapping,
  written at Trino's start from the Secret, never into a ConfigMap); every other bucket is read as before, in Buckets.
- Nessie reads the adopted tables' metadata there too (its per-bucket S3 settings, with the same key).
- Nessie's GC and the nightly maintenance never touch the store: GC reads and deletes in Buckets alone, and maintenance skips
  adopted tables.

## Backups and restore

Backups are for Kubernetes installs.

### What it's for

Losing a database (the projects' own records, Nessie's catalog, Keycloak, Ranger, Airflow, the catalog) or a whole cluster shouldn't lose the platform. Each night every database is dumped, the Projects and Secrets exported, and all of it packed and encrypted for a certificate whose key is kept off the cluster.

### Set up

```bash
storscale backup keygen --out ~/storscale-backup-keys
storscale upgrade --context <cluster> --set-file backup.certificate=$HOME/storscale-backup-keys/storscale-backup-cert.pem
storscale backup --context <cluster>                     # one now; then nightly
```

`keygen` writes `storscale-backup-cert.pem` (for the chart) and `storscale-backup-key.pem` (for a restore; keep it somewhere other than the cluster: without it no backup can be restored, with it anyone can read them). It never overwrites a key.

| Chart value | Default | What |
|---|---|---|
| `backup.certificate` | empty: backups off | The certificate backups are encrypted for |
| `backup.schedule` | `0 4 * * *` (UTC) | When the CronJob `storscale-backup` runs |
| `backup.keep` | `14` | How many backups are kept |
| `backup.bucket` | `storscale-backups` | Where |
| `backup.s3.endpoint`, `region`, `secretName` | empty: the platform's own Buckets | Another S3 endpoint, its credentials in a Secret with `accessKey` and `secretKey`. A real install should use one: a lost cluster takes its own Buckets with it. |
| `backup.data.enabled`, `schedule`, `allowInternal` | `false`, `30 4 * * *`, `false` | Every bucket copied to `backup.s3` nightly (what changed since); needs `backup.s3.endpoint` |

### How it behaves

- Administration → **Health** → **Backups** says when the last one succeeded and the schedule, or *Not set up: nothing is backed up*. If none succeeds for 26 hours, `Backups are overdue` is an alert and one of the page's other problems.
- `storscale restore --context <cluster> --key storscale-backup-key.pem` (the latest; `--backup NAME` another) checks the key is the certificate's, says what it will do and asks (`--yes` doesn't), then stops every Deployment, restores every database and the Projects and Secrets, and starts everything again. `--data` copies the buckets back from `backup.data`'s copy too. A restore that was cut off starts again from the replicas it saved.
- **A cluster lost:** with `backup.s3` elsewhere and `backup.data.enabled`, install the platform afresh on a new cluster with the same `backup.*` settings and certificate, then `storscale restore --context <new> --key <key> --into-new`: the data is copied back, every database restored, each database role given its password of then, the backup's Secrets put back, Buckets restarted, and the setup steps run again. CI checks each release this way.

## Upgrades

### Set up

```bash
storscale upgrade --context <cluster> --version 0.31.0 --dry-run   # check; show the Project definition's changes
storscale upgrade --context <cluster> --version 0.31.0             # then do it
```

| Flag | What |
|---|---|
| `--version V` | The release to go to (default `storscale`'s own) |
| `--context C`, `--namespace N`, `--release R` | Which cluster, namespace (`storscale`) and Helm release (`storscale`) |
| `-f FILE`, `--set K=V`, `--set-file K=PATH` | Settings to change, over those it was installed with |
| `--dry-run` | Show the Project definition's changes (`kubectl diff`) and check the chart renders against the cluster; change nothing |
| `--yes` | Don't ask; `--test` then runs the end-to-end checks |
| `--force` | Even to an older release |
| `--dir DIR` (with `--image-tag V` on a cluster) | A checkout's chart instead |
| `--chart oci://...` | The chart from a registry of the company's own (Harbor's, as below) instead of `ghcr.io`'s |
| `--ca-file PEM` | The CA of that registry, when its certificate isn't a public CA's (the chart's pull and the image check) |
| `--timeout` | How long to wait for the services (60 minutes) |

### How it behaves

1. It checks the release isn't mid-upgrade (`another install, upgrade or rollback is under way`), isn't going backwards (`... is older than the installed ...`, unless `--force`), that Helm is 3.14 or later, and that the release's images are published where the install takes them from (`images.registry`: a `--set`, else the installed value, else the chart's).
2. It applies the release's **Project definition** (the CRD), which Helm never upgrades. Without it Kubernetes silently drops whatever fields the old definition lacks from every project.
3. It upgrades the chart keeping the installed values (`--reset-then-reuse-values`, so the new release's defaults apply to new settings), with the images pinned to the release.
4. It waits for every service to roll out and be ready, then prints `Upgraded storscale from 0.30.0 to 0.31.0 (revision N).` If anything fails it prints the `helm rollback` that undoes it.

The CHANGELOG's *Upgrading from ...* paragraph says what each release needs. A `--dry-run` to the version already installed changes nothing.

## Buckets capacity

### What it's for

Buckets holds every table's files and every project's bucket. Its usable space is its drives' total less parity: four drives at EC:2 hold half their size. When it fills, it's grown by bigger drives or another pool.

### How you're told

- The administrators: the monitoring rules `BucketsCapacityLow` (15% free), `BucketsCapacityCritical` (5%) and `BucketsCapacityFullIn7Days` (at the last six hours' rate), delivered by platformd as alerts and listed under the Health page's other problems.
- A project's people: as its own bucket nears its `limits.storage` ([Limits](#limits)).

### Bigger drives

The storage class must allow volume expansion (`kubectl get storageclass <class> -o jsonpath='{.allowVolumeExpansion}'` says `true`). Every drive of a pool grows together: an erasure set holds only as much as its smallest drive.

```bash
storscale upgrade --context <cluster> --version <installed> --set buckets.storage=40Gi --dry-run
storscale upgrade --context <cluster> --version <installed> --set buckets.storage=40Gi
kubectl -n storscale get pvc -l buckets.io/cluster=buckets   # within five minutes: 40Gi each
```

The chart keeps the drives' claim template as it was made (Kubernetes never lets a StatefulSet's change); the platform operator asks for the new size on each drive's claim within five minutes and logs each drive's state (`grown from 10Gi to 40Gi`, `growing to 40Gi (has 10Gi)`, `its file system grows to 40Gi when its server next starts`). If a claim's `FileSystemResizePending` condition stays, delete one Buckets pod at a time and wait for it to be ready before the next.

### Another pool

When drives can't grow, or more servers are wanted: a whole set of servers and drives beside the first.

```bash
storscale upgrade ... --set 'buckets.pools[0].servers=1' --set 'buckets.pools[0].volumesPerServer=4' \
  --set 'buckets.pools[0].storage=100Gi'
```

Adding one restarts every Buckets server together (S3 is down for a minute or two); new objects go to the pool with the most free space; nothing moves existing objects unless someone runs `mc admin rebalance start`. Keep the pools in your values file: a later `-f` without them is refused.

### What's refused

The chart refuses, before anything changes:

- `pool-0's drives are 40Gi each: they can grow, never shrink (its storage: 20Gi)`
- `pool-0 is 1 server(s) of 4 drives: a pool's shape can't change (add a pool to buckets.pools instead)`
- `pool-0's drives are on storage class "standard": a pool's class can't change (add a pool on the new one instead)`
- `Buckets has 2 pools and the values give 1: a pool can't be removed here, its data would go with it`

## The install's settings

The chart's values that matter most, from `deploy/helm/storscale-platform/values.yaml`, with what Compose uses instead: environment variables of the service that reads them (platformd's unless said), set in `stack/compose.yaml` or `stack/.env`; one not set there takes the default. Change the chart's with `storscale upgrade --set ...` (or `-f`).

| Chart value | Default | Compose | What it does |
|---|---|---|---|
| `global.domain`, `port`, `scheme` | `storscale.localhost`, `8800`, `http` | `STORSCALE_DOMAIN`, `STORSCALE_PORT` | The platform's one address |
| `global.ca.certs` / `configMap` | empty | | Private CAs every service trusts |
| `sso.entra.enabled`, `tenantId`, `clientId`, `secretName` | off | `ENTRA_AUTHORITY`, `ENTRA_CLIENT_ID`, `ENTRA_CLIENT_SECRET` (setup's environment) | Sign-in with Entra ID, brokered by Keycloak (setup makes the identity provider `entra` from the tenant's discovery document). The app registration's redirect URI (Web) is `<platform URL>/sso/realms/lakehouse/broker/entra/endpoint`; its ID token carries `email`, `given_name`, `family_name` and `preferred_username`; its client secret is in the Secret `secretName` (key `client_secret`) |
| `sso.entra.roles` | `Analyst: [analysts]`, `Engineer: [engineers]`, `Administrator: [engineers]` | `ENTRA_ROLES` | The app roles (the token's `roles` claim) and the platform's groups each puts people in, at every sign-in |
| `sso.entra.displayName` | *Microsoft Entra ID* | `ENTRA_DISPLAY_NAME` | The button on Keycloak's sign-in page |
| `sso.entra.onBehalfOf.enabled`, `maxAssertionSeconds` | off, 5400 | `ENTRA_ON_BEHALF_OF`, `ENTRA_ON_BEHALF_OF_MAX_SECONDS` | Applications people sign in to through Entra (a CRM) acting for them: the `entra` provider's JWT authorization grant, accepting the app registration's client ID as the audience, assertions without `jti`, valid up to `maxAssertionSeconds` ahead (Entra's last 60–90 minutes). In Entra the app registration exposes a scope (`access_as_user`) with v2 access tokens (`requestedAccessTokenVersion: 2`), and the application is granted delegated permission to it. A project's service account with `onBehalfOf` then exchanges the person's token ([Projects](02-projects.md#acting-for-the-people-signed-in-to-it-onbehalfof)) |
| `brand.name`, `title`, `logo`, `icon` | none: *DataWorks* | `BRAND_NAME`, `BRAND_TITLE`, `BRAND_LOGO`, `BRAND_ICON` (platformd's environment) | The top bar and the browser tab. The title is `<name> DataWorks` unless given (`brand.name: Vericast` shows *Vericast DataWorks*); the catalog's domain of data products takes it too |
| `gateway.ingress.*`, `gateway.trustedProxies` | off | | The ingress and its TLS |
| `trino.memory`, `workers`, `workerMemory` | `2560Mi`, `0` | | Trino's size |
| `buckets.servers`, `volumesPerServer`, `storage`, `storageClassName`, `pools` | `1`, `4`, `10Gi`, default, `[]` | | Buckets' drives ([Buckets capacity](#buckets-capacity)) |
| `storage.*` | `postgres: 8Gi`, `nessie: 2Gi`, ... | | The databases' volumes |
| `pipelines.defaultRef`, `memory`, `gitKnownHosts` | `main`, `3Gi`, empty | | Projects' repos and their Airflows |
| `deliveries.approverGroups`, `keepFor` | the administrators, `7y` | `DELIVERY_APPROVER_GROUPS` (`engineers,analysts`), `DELIVERY_KEEP_FOR` | [Deliveries](12-deliveries.md#settings) |
| `products.certifierGroups` | the administrators | `PRODUCT_CERTIFIER_GROUPS` | Who certifies data products |
| `erasure.deadlineDays` | `45` | `ERASURE_DEADLINE_DAYS` | [Privacy](13-privacy.md#settings) |
| `protection.applySuggested`, `protection.tags` | `true`; `personal` `MASK_HASH`, `financial` `MASK`, `health` `MASK_NULL`, none excepted | `PROTECTION_APPLY_SUGGESTED`, `PROTECTION_TAGS` (excepts `engineers`) | What the catalog's tags mean ([Access and protection](04-access-and-protection.md)) |
| `llm.gateway` | `stub` | the `llm` service | `stub` (the stand-in), `litellm`, or empty (none) ([AI](14-ai.md)) |
| `llm.guardrails`, `llm.forbiddenTerms` | `true`, `[]` | `LLM_GUARDRAILS`, `LLM_FORBIDDEN_TERMS` (`Project Falcon`) | The guardrail |
| `llm.litellm.models` | `default`: `anthropic/claude-opus-5` | | LiteLLM's model list; the provider's key in the Secret `storscale-llm` (key `api_key`) |
| `usage.rates`, `usage.viewerGroups` | `{currency: USD, cpuHour: 0.05, storageGBMonth: 0.023}`, `[]` | `USAGE_RATES`, `USAGE_VIEWER_GROUPS` | [Usage and cost](#usage-and-cost) |
| `templates.gitlab.url`, `group`, `tokenSecret` | empty | `GITLAB_URL`, `GITLAB_GROUP`, `GITLAB_TOKEN` | The company's GitLab for project templates and shared libraries ([Projects](02-projects.md)) |
| `install.target` | `production` | `INSTALL_TARGET` (`development`) | Which of a project's `storscale/targets/<target>.yaml` this install is |
| `monitoring.sampleTraffic` | `false` | `STORSCALE_SAMPLE_TRAFFIC` (`true`) | Sample S3 traffic for the storage dashboards, on a fresh install with nothing else to show |
| `serving.memory`, `copy.memory`, `copy.commitEvery` | `1Gi`, `1Gi`, `30` | | Model servers; change-capture copies |
| `replicas.*` | `1` each | | Pods of each stateless service |
| `networkPolicies.enabled` | `true` | | Nothing reaches a pod unless it should |
| `audit.storage`, `audit.keep` | `5Gi`, `365d` | `AUDIT_KEEP` | [The audit log](#the-audit-log) |
| `backup.*` | off | | [Backups and restore](#backups-and-restore) |
| `maintenance.at`, `history.keep`, `history.schedule` | `02:00`, `P7D`, `30 3 * * *` | `MAINTENANCE_AT`, `NESSIE_GC_KEEP`, `NESSIE_GC_EVERY` | [Table maintenance](#table-maintenance) |
| `limits.project.*`, `limits.person.*`, `limits.total.*` | 4/40/50%/none; 4/100; 20/1000 | `LIMITS_*` (the operator's; same defaults) | [Limits](#limits) |
| `notebooks.memoryLimit`, `cpuLimit`, `storage` | `1G`, `2`, `1Gi` | | Each person's notebook |
| `superset.mapboxApiKey` | empty | `MAPBOX_API_KEY` | A base map for map charts |
| `examples.users` | `true` | | Doesn't switch the example people off yet |

Who administers is `PLATFORM_ADMIN_GROUPS` (default `engineers`), and who may sign in at all `PLATFORM_GROUPS` (default `analysts,engineers`): platformd's environment; the chart has no value for them.

## The Python package and the API

### What it's for

Everything the web app does is an API call, so a script, a scheduled job or the company's privacy tool can do it too, as a person or a service account, under the same rules.

### The API

- The account menu's **API reference** lists every route with what it does; `GET /api/openapi.json` is the same as OpenAPI, for any client generator.
- Sign in with a Keycloak access token of the realm `lakehouse`: `Authorization: Bearer <token>`.
- Every change (`POST`, `PUT`, `DELETE`) needs the header `X-Platform-Request: 1`; without it the answer is `403 missing X-Platform-Request`.
- Every answer carries `X-Request-ID`; every refusal or failure is JSON, `{"error": "...", "request": "<id>"}` ([Troubleshooting](18-troubleshooting.md#request-ids)).

### The Python package

```bash
pip install "git+https://github.com/StorScale/dataworks#subdirectory=stack/python"
```

```python
import storscale

p = storscale.Platform("https://storscale.example.com")   # or STORSCALE_URL; with no credentials, a device code to approve
p.projects()
p.sql("retail", "SELECT count(*) FROM uploads_orders")
run = p.start_run("retail", "delivery_retail_campaign_results")
p.wait_for_run("retail", "delivery_retail_campaign_results", run)
p.usage("retail")
p.call("get_projects_name_deliveries", name="retail")      # any route by its operation ID
```

It signs in, in this order, with `token=` (or `STORSCALE_TOKEN`), `client_id=` and `client_secret=` (or `STORSCALE_CLIENT_ID`, `STORSCALE_CLIENT_SECRET`: a service account), `username=` and `password=` (or `STORSCALE_USERNAME`, `STORSCALE_PASSWORD`, through the public client `storscale-agent`), or a device code: it shows a link and a code to approve in your own browser, where you sign in as you always do (Entra ID too, which has no password to give a script), for a notebook, a pod or a terminal without a browser. A refusal raises `storscale.Refused` (401, 403) or `storscale.Error`, with the platform's reason and the request's ID. A private CA: set `REQUESTS_CA_BUNDLE`.
