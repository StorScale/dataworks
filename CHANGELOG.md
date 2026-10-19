# Changelog

All notable changes to StorScale Platform are recorded here, in the [Keep a Changelog](https://keepachangelog.com/) format. The project uses [semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **The company's terms** ([docs/use-cases.md](docs/use-cases.md), 10.1): a project marked `semantic.shared` holds a semantic model others build on (`semantic.uses`), compiled with theirs and not defined again in them; a change to it that would break a model built on it is refused, naming the project. A model's `terms:` (owner, definition, synonyms) are the platform's: a Business terms page (in the top bar), a page per term (its tables, metrics and the projects using it), terms first in search, and OpenMetadata's glossary, Business terms, kept in step, the tables tagged. The `business` suite checks it.
- **Dashboards on the same definitions** (10.2, Superset): `superset-sync` keeps a Superset dataset for each semantic model, with its metrics defined and the dimensions of the models it joins to by entity, so a chart's numbers are the semantic layer's, under the person's rules. Power BI is to decide (how people reach it and sign in).
- **Certified data products** (10.3): a project publishes tables for others (`spec.products`: owner, version, contract, the terms they hold). Another project's editors ask for one, the producer's editors approve, and the consuming project reads it in place, under the producer's rules (its readers' masks and row filters on that table hold for the consumers too), never a copy. The governance group (`products.certifierGroups`, default the administrators) certifies a product as it is: a change of its table, version or contract takes that away. A product's table breaking its contract (a column gone, too stale, its checks failing) is its project's alert, naming the consumers, and a merge request that would drop a contracted column fails its review. A Data products page (certified first), products in search, and OpenMetadata's data products (the certified ones' tables Certification.Gold). The `business` suite checks it.
- **Salesforce from a connector's form** (8.4): an import's source may be `salesforce` (dlt's verified source, copied in at a pinned commit), its objects and since when, signed in by a connection of kind salesforce (and a CA to trust, for a proxy that inspects TLS). The project's editors add, change and remove imports on the new Imports tab. The `imports` suite plays Salesforce.

### Changed
- **The catalog's Welcome card is charcoal blue** (`#2c3e50`, OpenMetadata's `panelBackgroundColor`), not OpenMetadata's blue.

### Fixed
- **The notebooks suite on a real cluster** cleans up its connection through the platform (the test pod may delete only Secrets of its own names), so a second run there doesn't meet the first's.

## [0.16.1] - 2026-10-18

The catalog's logo fitted to its sidebar and its colours the platform's; a deleted project's deliveries' connections removed.

**Upgrading from 0.16.0:** `storscale upgrade --context <cluster> --version 0.16.1`, after a `--dry-run`. Nothing to set.

### Fixed
- **The catalog's logo fits its sidebar:** OpenMetadata draws it 40 pixels high and as wide as its proportions make it, so a wide logo with no size of its own (an SVG with only a viewBox) overflowed the sidebar. It now gets platformd's `/brand/catalog-logo`, the logo fitted into 150 by 40.
- **The catalog's colours are the platform's:** its buttons and links take the top bar's near-black (`#10151c`), not the platform's link blue; hover and info greys from the bar too.
- **A deleted project's deliveries' connections go:** the operator removed a project's imports', sources' and git connections from the shared Airflow, but not its deliveries' (each holding its target's credentials). A project with its own Airflow lost them with it.

## [0.16.0] - 2026-10-18

Phase 7 completed: a project's spec and metrics released from its repo (planned, then applied by another), its credentials given on a Connections tab, and a notebook scheduled from JupyterLab as a merge request; what was sent about a person, found again from the tables as they were; and the catalog in the platform's look.

**Upgrading from 0.15.0:**
- `storscale upgrade --context <cluster> --version 0.16.0`, after a `--dry-run`. The Project CRD gains `tables.people` (the upgrade applies it first). The notebook image gains JupyterLab's Schedule (and pins JupyterLab 4.5.10); people's notebook servers take it when they next start. The Airflow image gains the scheduled notebooks' virtualenv.
- `install.target` (default `production`) is which of a project's repo targets (`storscale/targets/<target>.yaml`) this install is.
- `deliveries.keepFor` (default `7y`) is how long a personal delivery's read is kept (Nessie tags, so its data isn't collected); storage grows by what those tags hold. `deliveries.approverGroups` now also says who searches the delivery log by a person's ID.
- A project's connections can now be given on its Connections tab by its editors; those made with kubectl stay, and show there.
- Trino's pods ask for 2560Mi each now (was 2Gi): CI saw Trino killed for memory (OOMKilled) late in the suites, its memory outside the heap having grown with the catalogs made and dropped while it ran.

### Added
- **What was sent about a person** ([docs/use-cases.md](docs/use-cases.md), 9.2): a project declares its tables of people's data and the column identifying a person (`tables.people`); a delivery of them is personal, and needs a data-protection approver. Each run of a personal delivery has the tables as it read them kept (a Nessie tag, for `deliveries.keepFor`), and the data-protection approvers search the delivery log by a person's ID on the Deliveries tab: each run is run again on the tables as they were, and the runs that carried that person's rows are listed with when, to whom, and who approved. The log holds no one's ID, and the search's audit record doesn't either. In the examples (Compose, a checkout's kind install) the analysts are data-protection approvers too, so alice can approve what bob changes.
- **A notebook on a schedule** ([docs/use-cases.md](docs/use-cases.md), 7.7): JupyterLab gets Schedule (a toolbar button, a file-browser item, a command): pick a project you edit and a time (cron, UTC), and the platform commits the notebook (its outputs cleared) and a small DAG that runs it to a branch of the project's repo, with a GitLab merge request. Merged, the project's Airflow runs it with papermill as the project's pipelines, in a virtualenv with the notebook image's packages (`buckets_lake` works there as in JupyterLab, as the pipelines). Each run's executed notebook is kept and linked from the run on the Pipelines tab; a failing cell fails the run, saying which and why. The `notebooks` suite plays GitLab and checks it end to end.
- **The catalog wears the platform's look:** OpenMetadata shows the platform's logo (on its sign-in page), its icon (in its navigation bar and its browser tab) and the web app's colours. catalog-sync sets them (OpenMetadata's `customUiThemePreference`) as it starts; the images are platformd's `/brand/logo` and `/brand/icon`, served on the catalog's own name through the gateway (`/_storscale/brand/`), so a deployment's brand (`brand.logo`, `brand.icon`) shows there too. Without a logo of its own, `/brand/logo` is the icon, and without an icon, StorScale's (it was a 404).
- **A project's Connections tab** ([docs/use-cases.md](docs/use-cases.md), 7.6): its editors give the platform credentials once, of a kind (a git token, an SSH key, a database user, an API token, an SFTP server, an S3 bucket, a PGP key, a Teams channel), each kind's keys checked; members see each connection's name, kind, its values that aren't secret, who added and last changed it, and what in the spec uses it, never a password, token or key. A new value is pasted over the old (a rotation; the operator applies the project at once); one the spec still names can't be removed. Names start with the project's (`sales-dags`), and a name taken by the platform's own Secret or another project's is refused. They're kept as before (labelled Secrets on Kubernetes, the platform's bucket in Compose), so connections made with kubectl show too. The sources suite gives its database user this way.
- **A project as code, released from its repo** ([docs/use-cases.md](docs/use-cases.md), 7.4): beside its pipelines, a project's repo holds `storscale/project.yaml` (its spec), `semantic.yaml` (its metrics) and `targets/<target>.yaml` (what differs on one install, laid over `project.yaml`: maps merged, lists replaced); an install says which target it is (`install.target`, default `production`; Compose is `development`). On the project's new Releases tab an editor plans a tag, a branch or a commit: the platform's DAG `storscale_plan` (the bundle `storscale`, in the project's Airflow) checks the repo out with its connection and sends the folder to platformd, which composes the spec for this install, checks it and the semantic model (semanticd's new `/semantic/check`), and lists what would change: members added and removed, the parts of the spec changed, the deliveries that will need approving again, the metrics added, changed and removed. An administrator other than who planned it applies it: the spec and the model saved, the release recorded (its ref and commit, who planned and who applied), in the audit log. A plan made before the project changed is refused (planned again). A rollback is a plan of an earlier tag. The `releases` suite checks it end to end.

## [0.15.0] - 2026-10-18

File deliveries to clients and partners (SFTP and S3), approved before they run; an import's reset, GitLab's webhooks, how far a copy is behind, and the metrics a merge request moves.

**Upgrading from 0.14.0:**
- `storscale upgrade --context <cluster> --version 0.15.0`, after a `--dry-run`. Trino's pods ask for 2Gi each now (was 1536Mi). The Airflow image is larger (the deliveries' virtualenv, gpg).
- Who approves deliveries that send personal data: `deliveries.approverGroups` (Keycloak groups), default the platform's administrators.
- GitLab's webhooks, if wanted: in the repo's connection, `webhookToken`; in GitLab, a webhook to `https://<platform>/api/webhooks/gitlab/<project>` with that token, for merge request events. Without them, the platform polls each minute as before.
- A delivery's target is reached from the Airflow its DAG runs in (egress is open): a partner's SFTP server or bucket behind a firewall needs the cluster's nodes let through.

### Added
- **File deliveries to clients and partners** ([docs/use-cases.md](docs/use-cases.md), 9.1; SFTP and S3 first): a project names its deliveries (`deliveries`: a table or a query of its own, CSV, Parquet or JSON lines, gzipped if asked, the files' names with `{date}` and `{part}`, a file per value of a column if asked, a schedule, a target connection, an SFTP server with its host key checked or an S3 bucket, and the recipient's PGP key). A delivery runs only once approved by someone other than who last changed what it sends (another editor of the project, or an administrator); one sending personal columns (those the project masks for its readers, until columns are tagged) needs a data-protection approver too (`deliveries.approverGroups`, default the administrators). A change to what it sends (its table or query, split, format, target or key) needs approving again, and until then it has no DAG. Its run, as the project's pipelines in its Airflow (the platform's DAG file `storscale_deliveries.py`, the run in a virtualenv of its own with pyarrow, paramiko, boto3 and gpg): its checks first (rows within a range, its table changed in time, every part there; a failing one sends nothing), the files written, compressed and encrypted, a manifest beside them (each file's rows, bytes and SHA-256, the tables' snapshots read), all sent, tried three times. Each run is in the delivery log (platformd keeps it, with an audit record), on the project's new Deliveries tab with its approval. The `deliveries` suite plays the partner (an SFTP server, a bucket, a key pair) and checks it end to end.
- **An import reset**: the project's editors have an import copy everything again (dlt drops its tables and where its cursors got to), from the editor; the imports suite checks it.
- **GitLab's webhooks**: a merge request's event (with the token the repo's connection holds, `webhookToken`) has the operator look at the project's merge requests at once; polling goes on beside it.
- **How far a copy is behind**: the project's Overview shows each copied table with when its last change was committed (`/api/projects/<name>/copies`).
- **The metrics that moved**: a merge request's note lists the project's metrics (its semantic model) whose values differ on the branch from production's.
- **A merge request's note, kept and fuller:** the platform's note on a merge request is updated at each new commit (one note per merge request, marked as the platform's), and holds a sample of the rows that differ between the branch and production, for each model built.

## [0.14.0] - 2026-10-18

A merge request's dbt change built and tested on a data branch of its own (GitLab), and the merge request told how it went.

**Upgrading from 0.13.0:**
- `storscale upgrade --context <cluster> --version 0.14.0`, after a `--dry-run`. Every Airflow (the shared one, and each project's) gains the platform's bundle `storscale` (the DAG `storscale_review`).
- A project asks for reviews with `pipelines.reviews` (its GitLab's address, the repo's path there, the dbt project's folder); the repo's token needs GitLab's API (read merge requests, write statuses and notes: the `api` scope). The operator reaches GitLab from the cluster (egress is open) each minute.

### Added
- **A merge request on a data branch** ([docs/use-cases.md](docs/use-cases.md), 7.3; GitLab): a project's `pipelines.reviews` names its GitLab and its repo's dbt project. The operator polls the repo's open merge requests (its token, GitLab's API) and, for each, makes a Nessie branch of the project's tables (`<project>-mr-<iid>`, from main: nothing copied) and a Trino catalog on it (`<project>_mr_<iid>`, its editors reading, its pipelines building), then starts the platform's DAG `storscale_review` (in the Airflow image, in the project's own Airflow) at each new commit: it checks the repo out at that commit, builds the dbt models the change touches and those downstream (`state:modified+`, against production's manifest, which platformd keeps) in the branch's catalog, as the project's pipelines, and tells the merge request: a commit status (`storscale/dbt`: pending, running, success or failed, naming the failed tests) and a note with each model's rows on the branch and in production. Production is untouched; a merge request closed or merged, its branch and catalog go (production's next run builds what was merged). The `reviews` suite plays GitLab's API and checks it end to end.

## [0.13.0] - 2026-10-18

dbt in a project's repo: its models built by Airflow in Trino as the project's pipelines, and described in the catalog; Trino's memory set right.

**Upgrading from 0.12.0:**
- `storscale upgrade --context <cluster> --version 0.13.0`, after a `--dry-run`. Trino restarts with its own JVM settings (its heap 60% of its pod's), and the setup steps run again: a project's pipelines may then make, drop and alter tables in its namespace, and anyone signed in may run queries and read Trino's metadata (filtered to what each may see), as the notes under Changed say.
- A repo's DAG uses dbt with `from storscale import dbt` and `dbt.build(project=..., path="dbt")`; its `dbt_project.yml` says `profile: storscale`.

### Added
- **dbt in a project's repo** ([docs/use-cases.md](docs/use-cases.md), 7.5): a DAG of the repo's calls the platform's helper, `storscale.dbt.build(project=..., path="dbt")`, and its run is `dbt build` on the dbt project beside it (dbt-core 1.12 with dbt-trino, in a virtualenv of their own in the Airflow image), in Trino, on the project's Iceberg tables, signed in as the project's pipelines service account (a Keycloak token): Ranger's rules are the pipelines', and its audit names them. The helper writes dbt's profile, so the repo holds no settings and no secrets. After each run, passed or failed, the helper sends dbt's artifacts (manifest, catalog, run results) to platformd, which takes them from the project's own pipelines account alone (its Keycloak token) and keeps them in the platform's bucket (`dbt/<project>/`); catalog-sync describes the models from them (OpenMetadata's dbt source): their descriptions, columns, lineage and tests' results. The pipelines_git suite's repo has a dbt project, whose model's table the suite reads and whose description the catalog shows. Not yet: dbt packages, and runs of only what changed.

### Changed
- **Trino has 2Gi** (`trino.memory`, and Compose's), up from 1536Mi: its memory outside the heap grows with each catalog made while it runs (sources, merge requests' branches), and CI saw its container killed for memory (OOMKilled) with 1536Mi.

### Fixed
- **Trino's memory:** its heap is 60% of its pod's (its own jvm.config; the image's 80%, with 256 MB of code cache besides, left too little for the rest, and Trino's container was killed under load in CI).
- **The data's backup copies a bucket again when an object fails** (read short mid-copy), up to three times: the copy is incremental, so again fetches only what's missing.

### Changed
- **A project's pipelines make their tables** (create, drop, alter, in the project's namespace alone: dbt's models), beside reading and writing them.
- **Anyone signed in may run queries and see the iceberg catalog is there** (Ranger's group public), each project's own pipelines account among them (on Kubernetes a group of its own, which the shared policies can't name): what one may read in a namespace or table is still its project's.
- **Everyone may read the catalog's information_schema and Trino's system metadata** (`system.jdbc`, `system.metadata`; Ranger's group public): Trino shows in them only what each one may see, and each project's own pipelines (dbt) read them. Not `system.runtime`.

## [0.12.0] - 2026-10-18

An application's data imported into a project's tables on a schedule (dlt, in the platform's Airflow): any REST API, or a database by a cursor column.

**Upgrading from 0.11.0:**
- `storscale upgrade --context <cluster> --version 0.12.0`, after a `--dry-run`. The Airflow image is larger (dlt and pyiceberg, in a virtualenv of their own), and its scheduler now holds the lakehouse's writers' Buckets account (the copies'), for the imports' runs.
- An import's API or database is reached from the Airflow scheduler's pod (egress is open).

### Added
- **An application's data, imported** ([docs/use-cases.md](docs/use-cases.md), 8.2): a project names imports (`imports`): any REST API (dlt's `rest_api`: its resources' paths, where the objects are in the answer, a primary key, a cursor and its query parameter) or a database by a cursor column (`sql_database`, PostgreSQL or MySQL), one of its connections (the API's token, or the database's account), and a schedule. Each is a DAG of the platform's Airflow, tagged as the project's pipeline (its Pipelines page, its alerts when a run fails): dlt (1.30, in a virtualenv of its own in the Airflow image) copies each resource into `<import>_<resource>` in the project's namespace, an Iceberg table written through Nessie's REST catalog (pyiceberg), merged on its primary key or appended to, each run asking only for what's past its cursor (dlt keeps it beside the data). The import's credentials are an Airflow connection the operator keeps, handed to the run in its environment. An import runs once as it's added (Airflow's latest slot of its schedule), then on its schedule. The catalog has each import's pipeline upstream of its tables. The project editor has an Imports section. The `imports` suite serves a small API with a token and a cursor, and checks a first run, a second run asking only for what's new and merging it, the project's rules on the table, its lineage, and a failed run's alert.

## [0.11.0] - 2026-10-18

A database's changes copied into the lakehouse (change capture, on Kubernetes), and the rest of querying a database where it is: a source's schemas, its readers' masks and row filters, its tables in the catalog and on the SQL page.

**Upgrading from 0.10.0:**
- `storscale upgrade --context <cluster> --version 0.11.0`, after a `--dry-run`. A new Buckets account (`copy`, for the copies) is made by the setup steps; nothing copies until a project asks.
- A copy reads the source database's change log, which its administrator grants the source's account: PostgreSQL `REPLICATION` (and `wal_level=logical` on the server), SQL Server CDC on the tables, MySQL's row binlog. A PostgreSQL `numeric` column needs its precision declared to arrive as a decimal. A copy's pod reaches the database from the cluster (egress is open).
- Removing a copy leaves its PostgreSQL replication slot (`storscale_<project>_<source>`) for the database's administrator to drop: until then, the database keeps its log for it.

### Fixed
- **A restore waits for the stopped services' pods to be gone**, not only for their Deployments to want none: a pod shutting down could write into a database being restored (OpenMetadata's scheduler did, and the restore failed).

### Added
- **A database's changes into the lakehouse** ([docs/use-cases.md](docs/use-cases.md), 8.1; Kubernetes): a source copies tables into the project's own (`sources[].copy.tables`), by change capture. The operator starts Debezium Server with its Iceberg sink for it (a pod per source, no Kafka; `upstream.debezium`): a first copy of each table, then every insert, update and delete, committed within `copy.commitEvery` seconds (30), into `<source>_<table>` in the project's namespace, keyed by the table's primary key, new columns added as they come. The tables are the project's like any other: its masks, row filters and readers' tables apply. Its place in the change log and the schema's history are Iceberg tables of the platform's (`storscale_cdc`), so it keeps no volume. PostgreSQL, MySQL and SQL Server; the source's account needs the database's change log (PostgreSQL: `REPLICATION` and `wal_level=logical`; SQL Server: CDC on the tables; MySQL: the row binlog). The copy is a part of the project's status; the editor takes the tables to copy. The `copies` suite (CI's HTTPS job, its Postgres with `postgres.logicalReplication`) copies a table, then an insert, an update and a delete, then a column added. Numbers are exact (decimals as decimals: `decimal.handling.mode=precise`); a PostgreSQL `numeric` of no declared precision comes as Debezium's variable-scale struct (its scale and its value's bytes), which the sink doesn't turn into a decimal: declare its precision in the source. A copy not running for 10 minutes is told to its project (its alerts: Teams, email), and its pod crashing or stuck is on the Health page and alerts the administrators. The catalog has each copy's lineage, its source's table upstream of it, column by column. Not yet: how far a running copy is behind its source.
- **A project's sources, the rest of 8.3:** a source may name the only schemas the project reads of it (`schemas`), and narrow what its readers see with masks and row filters (`masks`, `rowFilters`, tables as `schema.table`), as a project's tables do. Its tables are in the catalog (OpenMetadata), beside the lakehouse's: catalog-sync describes each project's sources' catalogs too, its Trino account seeing their schemas, tables and columns and reading none of their data. The SQL page's schema browser lists the sources' tables as Trino shows them to each person. The project editor takes a source's schemas, and its readers' row filters and masks (and "what each role will get" lists them). The `sources` suite checks each.

## [0.10.0] - 2026-10-17

The data as it was (tags, each table's history, As of), querying a company database where it is (a project's sources), high availability, disaster recovery (the data copied off the cluster, a restore into a new install), and the rest of Pod Security.

**Upgrading from 0.9.0:**
- `storscale upgrade --context <cluster> --version 0.10.0`, after a `--dry-run`. Trino restarts, now making catalogs while it runs; JupyterHub's hub takes its files of before once (it now runs unprivileged); a new Trino account, `platform-operator`, gets its password and its Ranger policy from the setup steps. In Compose: `storscale down`, then `up`.
- Nothing runs more than one pod unless asked: `replicas.*` and `trino.workers` (deploy/kind/ha.yaml shows them).
- `backup.data` stays off until set; with backups off it does nothing.
- A project's sources reach their databases from Trino's pods (egress is open): a database behind a firewall needs Trino's nodes let through.

### Added
- **The data as it was** ([docs/use-cases.md](docs/use-cases.md), 6.10): a table is read as it was at a commit of the catalog, `"orders#<commit>"`, under its readers' rules. A project's **tags** keep moments (a month-end close, a delivery) as long as asked: its editors make and remove them on the new Tags tab (a name, a moment: now, a date or time, a commit; how long to keep it, as `7y`; a note), they're part of the project (`tables.tags`, so backups keep them), the operator keeps Nessie's tags (`<project>.<name>`) as they say, nightly maintenance removes those past their `keepUntil`, and the audit log records each. Each table's new **History** tab lists its changes, newest first: when, what it did (rows added and removed), who wrote it (from the audit log), the tags on it, each readable as it was then. The SQL page's **As of** reads the project's tables at a tag, a time or a commit. The `history` suite checks it.
- **High availability:** `replicas.*` runs more than one pod of the gateway, Keycloak (its clustered cache, its pods finding each other through its database), Nessie, semanticd, Superset and Airflow's API, spread across nodes, each with a disruption budget; `trino.workers` adds Trino workers beside the coordinator. CI's HTTPS job runs two of the gateway, Keycloak and Nessie and a worker, removes one pod of each, and checks again. platformd, the operator and the databases stay one each.
- **Disaster recovery:** `backup.data` copies every bucket (the lake, the projects', the platform's) to `backup.s3`, nightly, what changed, never deleting there; `storscale restore --data` copies them back, and `--into-new` restores into another install than the one backed up (a cluster lost): the data copied back, the databases, each database role given its password of then, the backup's Secrets (`storscale-env` among them) put back, Buckets restarted and the setup steps run again so every account matches. CI copies the data and checks a file comes back with `--data`; `--into-new` is checked in parts (its Job's steps, the roles' SQL), not yet end to end.

- **Query a database where it is** ([docs/use-cases.md](docs/use-cases.md), 8.3): a project names the company's databases it reads (`sources`: PostgreSQL, MySQL, SQL Server or Oracle, and one of its connections, with the database's read-only account), and each becomes a Trino catalog of the project's own, `<project>_<name>` (`sales_finance`), joined with the lakehouse's tables on the SQL page, in Superset and in notebooks. Its editors and pipelines may read it, its readers where the source says so, and no one may write it (Ranger). The platform operator makes the catalogs while Trino runs (Trino's dynamic catalogs), as a Trino account of its own; a source's user and password go into a credentials file Trino reads (`trino-sources`; the JDBC connectors' `credential-provider.type=FILE`), never into the statement, which Trino logs and Ranger audits (it holds the database's URL alone). The project editor has a Sources section; the `sources` suite checks it. Not yet: the sources' tables in the catalog, masks on them.

### Changed
- **Trino's catalogs are made while it runs** (`catalog.management=dynamic`), on the coordinator and the workers: the install's catalog files are copied into Trino's own directory as it starts. A new Trino account, `platform-operator` (its password `OPERATOR_TRINO_PASSWORD`, made on upgrade), may make and drop catalogs, and read nothing in them. Trino's workers may reach the platform's Postgres (a source may be one of its databases).
- **Pod security, the rest:** every container drops every capability. The Postgres servers run as `postgres` (999) from the start; the gateway (Caddy) and JupyterHub's hub as an unprivileged user (Caddy keeps the bind-port capability its binary carries); the browser checks as Playwright's own user; and every image whose user is known runs with it declared (`runAsNonRoot`). Short of Kubernetes' restricted standard still: OpenMetadata and Ranger's admin, whose images' user ids aren't known here (docs/plan.md). On an install from before, a step gives JupyterHub's hub its files of then (root's), once.

## [0.9.0] - 2026-10-17

Backups and restore, Nessie's catalog in Postgres, network policies and Pod Security, images scanned; and a security fix: a table read as it was keeps its readers' masks and row filters.

**Upgrading from 0.8.0:**
- `storscale upgrade --context <cluster> --version 0.9.0`, after a `--dry-run`. Nessie's catalog moves from RocksDB into Postgres once, before Nessie starts; the RocksDB files stay on `nessie-data` as `catalog-rocksdb-moved` (to roll back to 0.8.0, rename them back to `catalog`). In Compose: `storscale down`, then `up`.
- Network policies are on: they need the cluster's network plugin to enforce them (without one they do nothing). `networkPolicies.enabled: false` turns them off.
- Label the namespace for Pod Security: `kubectl label namespace storscale --overwrite pod-security.kubernetes.io/enforce=baseline pod-security.kubernetes.io/warn=restricted`.
- Backups stay off until there's a certificate (`storscale backup keygen`; `backup.certificate`), and the key that opens them is kept off the cluster.
- The security fix applies when the operator next applies each project (within its resync, or on any change).

### Added
- **Backups, and restoring from one** (Kubernetes): each night (`backup.schedule`) a CronJob dumps every database (the platform's Postgres, with Nessie's catalog, Keycloak, Airflow, Superset and OpenMetadata, and Ranger's), exports the Projects and Secrets, and keeps them in `backup.bucket` (the newest `backup.keep`), encrypted for `backup.certificate` (a tar.gz in a CMS envelope, AES-256-GCM: `openssl cms -decrypt` opens it). Off until there's a certificate: `storscale backup keygen --out DIR` makes the pair; the key, which a restore needs, belongs somewhere else than the cluster. Backups go to the platform's own Buckets, as an account of their own, unless `backup.s3` names another S3 endpoint (a lost cluster takes its own Buckets with it). `storscale backup` takes one now; `storscale restore --key KEY [--backup NAME]` puts one back into the install it came from: every Deployment stopped (their replicas saved, so a restore cut off can be finished), the databases restored, the Projects and Secrets put back, everything started again. The Health page says when the last backup succeeded, and the administrators are alerted when none has for a day; a failed run alerts too. CI backs up, removes a Nessie tag, a Keycloak person, a Ranger policy and a project, restores, and checks them back. Not yet: the data in Buckets copied elsewhere, without which a lost cluster can't be restored from a backup.

- **Network policies** (`networkPolicies.enabled`, on): nothing reaches a pod of the platform's namespace unless it should. The gateway takes connections from anywhere (it's the way in), and Trino's 8443 (JDBC clients); the platform's Postgres, Ranger's database and audit index, the catalog's search index and Keycloak take them only from the workloads that use them (a notebook can't reach a database); every other service only from the namespace's own pods. Egress is open. They need a CNI that enforces them; kind's does, so CI runs with them.
- **Every image scanned for known vulnerabilities** (Trivy) as it's published: all of them in the repository's Security tab, the high and critical ones with a fix in the workflow run's summary. A report, not a gate. (The images already carried an SBOM and build provenance, and a release's are signed.)
- **Pod security, the first part:** every container of the chart runs without privilege escalation and with the runtime's default seccomp profile; those whose images run as a user of their own (Keycloak, Trino, Nessie and its tools, Airflow, Superset, Grafana, Prometheus, OpenSearch, Ranger's Solr, platformd, the operator, semanticd) also with every Linux capability dropped. The platform's tools image (its setup steps, waits, backups and checks) and catalog-sync run as an unprivileged user (65532), enforced; Compose still runs them as root, for its volumes. `storscale up --k8s` labels the namespace for Pod Security admission: the baseline standard enforced (no privileged pods, no host network or files), the restricted one warned of; on a cluster of your own: `kubectl label namespace storscale pod-security.kubernetes.io/enforce=baseline pod-security.kubernetes.io/warn=restricted`. The Postgres client steps (each database's making, the backup's dump, the restore) run as the image's postgres user. Not yet restricted: the upstream images that start as root (the Postgres servers, Ranger's admin, Caddy, OpenMetadata, JupyterHub's hub); docs/plan.md lists what each would take.

### Changed
- **Nessie keeps the catalog in Postgres** (its `JDBC2` store): on Kubernetes a `nessie` database on the platform's Postgres (`nessie-db`, made if missing), in Compose a `nessie-db` server of its own. The catalog (every table, its history, branches and tags) is then in the database dumps the backups take, and Nessie no longer holds a volume's lock. An install from before keeps its catalog: before Nessie starts, a step moves it from RocksDB into Postgres once, with Nessie's admin tool, and renames the RocksDB files to `catalog-rocksdb-moved` on the `nessie-data` volume (to roll back, rename them back to `catalog`). In Compose, upgrade with `storscale down` then `up`, so Nessie isn't running while its catalog moves.

### Fixed
- **A table read at a Nessie reference keeps its readers' rules.** A table can be read as it was through its name: `"orders#<commit>"` (Nessie resolves the commit, and Trino passes the name through; a branch or tag after `@` isn't found). Ranger's policies named tables exactly, so such a name was another table to Ranger: refused where a project lists its readers' tables, but where its readers see every table, read **without the table's masks and row filters**. The operator's policies for a reader's table, a mask and a row filter now name the table at every reference too; the lakehouse suite checks alice sees `orders#<commit>` as she sees `orders`, and can't read `payroll#<commit>`. (No example project lets its readers see every table; any other install's do, until upgraded.)


## [0.8.0] - 2026-10-17

Tables kept in shape (compaction, and Nessie's GC for their history), and the audit log.

**Upgrading from 0.7.0:**
- `storscale upgrade --context <cluster> --version 0.8.0`, after a `--dry-run` (it applies the Project definition, which adds `tables.maintenance`).
- Nessie's GC runs nightly from now on and deletes the tables' history older than `maintenance.history.keep` (`P7D`), and the files only that history used: the data as it was beyond a week is gone after its first run. Set a longer `maintenance.history.keep` before upgrading to keep more; tag (in Nessie) anything to keep for good.
- Ranger's Solr becomes a StatefulSet with a volume (`audit.storage`): the audit records from before the upgrade, which lived only in its pod, are lost once more; from now on they're kept.
- Trino has a new account, `maintenance` (its password generated into `storscale-env`), which Ranger lets read and alter every table; its password is platformd's alone.

### Added
- **The audit log** ([docs/use-cases.md](docs/use-cases.md), 6.3): platformd records what people do in the platform (signing in, or being turned away; a project created, changed, saying which parts, or deleted; a pipeline run started; an alert muted or tested; maintenance asked for) beside Ranger's record of every access Trino allowed or refused, in Ranger's Solr, which now keeps them on a volume of its own (`audit.storage`, 5Gi). The platform's records are kept `audit.keep` (365d). A project's new Audit tab, for its editors, shows its records and every access to its tables; Administration › Audit shows the whole platform's, filtered by person, text, outcome, source and dates, with a CSV export for auditors. The `audit` suite checks it.
- **Table maintenance** ([docs/use-cases.md](docs/use-cases.md), 6.5): each night (`maintenance.at`, UTC) platformd compacts every project's small files into larger ones (Trino's `optimize`), one project at a time, as Trino's new `maintenance` account, which Ranger lets alter and read every table (`optimize` scans the table it rewrites), as the catalog's account may read them; its password is platformd's alone. A project may pause it (`tables.maintenance.paused`). Each table's new Storage tab shows its files, size and rows, and what its maintenance last did; the project's editors ask for a run now. A failed run alerts the project, and runs are Prometheus metrics (`platform_table_maintenance_*`). The `maintenance` suite checks it. Then Nessie's GC (`nessie-gc`, a CronJob at `maintenance.history.schedule`; a daily loop in Compose) deletes the tables' history older than `maintenance.history.keep` (`P7D`) and files no branch or tag needs, across the install, as Nessie's own Buckets account; a tag, as a month-end close, keeps what it points to, and a failed run alerts the administrators. Iceberg's own snapshot expiry and orphan-file removal don't apply to Nessie's tables, whose history is Nessie's commits: how far back the data as it was goes is the install's setting, not a project's. The Storage tab says how far back it goes. A checkout's own install keeps two minutes' history and collects every three, so the `maintenance` suite sees the files before a compaction, and a file nothing used, deleted.

## [0.7.0] - 2026-10-17

The platform's health, with alerts to its administrators and to each project's own people; and one-command upgrades.

**Upgrading from 0.6.0:**
- Use `storscale upgrade`, built from the v0.7.0 tag (`go build -o bin/storscale ./cmd/storscale`): `storscale upgrade --context <cluster> --version 0.7.0`, after a `--dry-run`. It applies the release's Project definition first (it adds `notify` and `tables.freshness`), keeps the values the platform was installed with, and waits for every service. By hand, as before: `kubectl apply -f deploy/crds/project.yaml` from the v0.7.0 tag, then `helm upgrade`.
- Alerts go nowhere until they're told where: the platform's Secret `storscale-alerts` for the administrators' (`teamsWebhook`, `emailTo`, and `smtpServer`, `smtpFrom`, `smtpUser`, `smtpPassword` for email); each project's `notify` for its own. The Health page works without them.
- The operator gives every project its finalizer as it applies it, including those made with kubectl; deleting one now waits for the operator to take its access out.

### Added
- **The platform's health, and alerts to its administrators** ([docs/use-cases.md](docs/use-cases.md), 6.4): platformd checks every service every 15 seconds, the same in Compose and on Kubernetes, where a service that's down gets its reason from its pods ("FailedMount: ... can't read superblock"). Administration › Health shows each service, why it's down and what people lose; Home tells everyone what isn't available just now. Administrators are alerted in a Teams channel (its Workflows webhook, as an Adaptive Card) and by email when a service goes down or comes back, and in the last 14 days of the platform's certificate; they can mute a service's alerts and send a test alert. Where alerts go is the platform's Secret `storscale-alerts` (`teamsWebhook`, `emailTo`, `smtpServer`, `smtpFrom`, `smtpUser`, `smtpPassword`). The checks are Prometheus metrics too (`platform_service_up`). platformd also delivers the monitoring rules firing in Prometheus (storage near full, a drive offline: nothing delivered them before), when they start and when they resolve, and, on Kubernetes, a setup step that gave up; the Health page lists them as other problems. The `health` suite checks every service is up, who sees what, muting, and a test alert reaching a Teams webhook.
- **Alerts to each project's own people** ([docs/use-cases.md](docs/use-cases.md), 6.7): platformd tells a project when its pipeline's latest run fails, a check fails, or a table isn't changed in time, and when a check or a table comes right. A project's `notify` says where: `teams`, one of its connections holding a Teams channel's webhook, and `email`. Its `tables.freshness` lists tables that must change within a while (`24h`, `30m`, `7d`), measured from each table's current snapshot in Nessie. The project's Overview has an Alerts card (what's wrong now, where it's told; editors mute an issue for a day), and the project editor sets both. The `project_alerts` suite checks it end to end.
- **One-command upgrades:** `storscale upgrade` takes the platform on any Kubernetes cluster (`--context`, `--namespace`, `--release`) to a release (`--version`, default storscale's own). It checks that the release isn't mid-upgrade, that it isn't going backwards (`--force`), and that the release's images are published; says what it will do and asks (`--yes`; `--dry-run` shows the Project definition's changes and checks the chart renders against the cluster, changing nothing). Then it applies the release's Project definition, which Helm never upgrades; upgrades the chart keeping the values it was installed with (and the release's defaults for new settings), with its images pinned to the release; waits for every service to roll out and be ready; and prints the `helm rollback` that undoes it. `--test` then runs the end-to-end checks. `storscale up --k8s` applies the Project definition too, and installs a published release with `--version`. CI's `kubernetes` job now installs the last release, keeps a project in it, upgrades to the commit and checks the project kept every setting before running every suite; `kubernetes-https` keeps checking a fresh install.

### Fixed
- **A project made with kubectl kept its members' access after it was deleted.** Only projects saved through the platform got the operator's finalizer, so deleting one made with `kubectl apply` removed it at once: its access stayed in Keycloak, Ranger and Buckets, and it stayed in people's project lists (semanticd's). The operator now gives every project it applies its finalizer, and undoes any project already gone this way (its status is kept in the platform's bucket). Found by CI's new upgrade job.
- The project editor no longer drops a project's pipeline repo, path, ref, credentials and memory when its service account is changed.

## [0.6.0] - 2026-10-16

Keycloak keeps its realm, its people and their sign-ins through restarts, with the realm's ownership split between the platform and the organisation; and limits per project, so no one project slows the others.

**Upgrading from 0.5.0:**
- Apply the release's Project definition first (`kubectl apply -f deploy/crds/project.yaml` from the v0.6.0 tag): it adds `limits`. Helm never upgrades CRDs.
- Keycloak and Trino each get a database of their own on the platform's Postgres (`keycloak`, `trino`), made by their pods as they start; their passwords are generated into `storscale-env`. Keycloak's first start on its database builds the realm from `realm.json` once more, as every restart did before: anyone added to Keycloak by hand since it last restarted needs adding again, and everyone signs in again once.
- Trino refuses queries until the operator has written its limits, a few seconds after it starts.

### Added
- **Limits per project** ([docs/use-cases.md](docs/use-cases.md), 6.8): a project's `limits` (Trino queries at once, queued, memory; its bucket's size) cap its share of the platform, with the install's defaults (`limits` in the chart's values). Trino's resource groups come from a database the operator keeps (`trino-db`), read every second: a group per project, for its members' queries that name it (client tag `project:<name>`, which the SQL page and metrics set) and its own pipelines'; a group per person for the rest; one cap for the install. A refused query names the group whose limit it met. Notebooks have a person's limits: CPU (`notebooks.cpuLimit`) beside memory. The `limits` suite checks where queries run, a refusal, and a bucket's size.

### Changed
- **The data as it was, added to the plan and use cases (phase 6, storyboard 6.10):** a table's history on its page; its data as of a date, a month-end close or a delivery; tags that keep a close beyond retention; and deliveries that record what they read, so any file or report can be made again. How this sits with erasure is an open question.
- **Keycloak keeps its realm, and the realm's ownership is split** ([docs/plan.md](docs/plan.md), "Phase 6: Keycloak for real use"): Keycloak runs in production mode on a database of its own (Compose: `keycloak-db`; Kubernetes: a `keycloak` database on the platform's Postgres, made if missing). It imports `realm.json` only when there's no realm yet. At every install and upgrade a setup step (`setup/realm.py`) keeps the platform's own parts as `realm.json` says: the realm's token settings, each tool's client (its addresses, flows, secret and mappers), the platform's service accounts' roles. People, groups, and whatever an administrator adds stay as they are; a restart no longer signs everyone out or loses them.


## [0.5.0] - 2026-10-16

Projects' pipelines from their own git repos, each project in an Airflow of its own on Kubernetes, and Airflow given only the keys it needs.

**Upgrading from 0.4.x:**
- Apply the new Project definition first: `kubectl apply -f deploy/crds/project.yaml` (from the v0.5.0 tag). Helm installs a chart's CRDs but never upgrades them, and without it Kubernetes drops the new `pipelines` fields (`repo`, `path`, `ref`, `credentials`, `memory`) from every project, silently.
- Keycloak's realm gains the operator's `manage-clients` role and the `/airflow/*` sign-in addresses; Keycloak restarts with the upgrade and imports them.
- A Secret a project names as its repo's credentials must carry the label `platform.storscale.io/connection=<project>` (in Compose: `connections/<project>/<name>.json` in the platform's bucket). Nothing in 0.4.x used them.
- Each project given a pipelines repo starts an Airflow of its own: 3 GB of memory unless it sets `pipelines.memory`.

### Added
- **Projects' pipelines from git (phase 7, first steps):**
  - A project names a git repo for its pipelines (`pipelines.repo`, `path`, `ref`, `credentials.secretRef`); a repo address with credentials in it is refused, and credentials are only ever a Secret's name.
  - The operator keeps each project's git connection in Airflow (`git_<name>`), from that Secret, and serves the DAG bundles Airflow should run: the platform's own, and one per project (`project-<name>`). Airflow's scheduler and DAG processor start through a wrapper that takes the list and starts them again when it changes.
  - The git provider is pinned at 1.0.0 (before it, a token could be kept on disk in a clone). SSH repos are checked against the install's known hosts (`pipelines.gitKnownHosts`).
  - A project's Pipelines page lists the DAGs in its repo, and the platform's own tagged for it; a DAG in another project's repo can't claim it by its tag. It names the repo, shows the commit each pipeline is at and each run ran, lists the files in the repo that didn't load, and warns when another repo has a DAG with the same ID.
  - **An Airflow per project, on Kubernetes:** a project with a repo gets an Airflow of its own (`project-<name>-airflow`), made by the operator from the chart's template: its own database and Postgres user, its own keys, and a Keycloak service account that's a member of this project alone. Its DAG code can reach nothing of another project's, nor the platform's. Its UI is at `/airflow/<name>/`, for the project's members. Its memory is the project's (`pipelines.memory`, 2Gi to 64Gi) or the install's (3Gi). Deleting the project, or its repo, removes it, with its run history. In Compose, projects' repos stay in the one shared Airflow, for one team.
  - A new suite, `pipelines_git`, serves a git repo of its own and checks the whole path: the DAG loads and runs at the commit pushed, the next commit follows, the token shows nowhere, and deleting the project takes its DAGs away. The tools image has git for it; the test pod may write its own Secret.

### Fixed
- **A project could name any Secret as its repo's credentials,** the platform's own `storscale-env` among them. Now it reaches only its own connections: on Kubernetes, Secrets labelled `platform.storscale.io/connection=<project>`; in Compose, `connections/<project>/<name>.json` in the platform's bucket. Anything else is refused, naming the Secret but never its contents.
- **Task logs on Kubernetes:** Airflow's API server fetched a task's log from the scheduler by the pod's name, which the cluster's DNS doesn't know, so logs came back empty (in Airflow's UI too). The scheduler now gives its IP.

### Changed
- **Airflow gets only the keys it needs:** every project's DAG code runs in Airflow, which had the platform's whole secret set (`.env`, or the Secret `storscale-env`), Trino's private key and, on Kubernetes, a service account that could read every Secret. Now it has its own keys by name, Trino's certificate alone, and a service account (`storscale-airflow`) that may only read whether the setup steps are done. Airflow's own keys are still readable by DAG code; the rest of the work is in the plan.
- **The plan's roadmap after phase 5** ([docs/plan.md](docs/plan.md)), from research into what buyers of Dataiku and comparable platforms value: production-ready (6), git-native projects with Airflow's DAGs from a git repo per project first (7), getting data in (8), governed AI (9), usage and cost per project (10), self-service and ML (11).
- **Use cases and storyboards for phases 6 to 11** ([docs/use-cases.md](docs/use-cases.md)): who does what, frame by frame, the requirements each sets, and the questions still open.
- **What commercial platforms have that our users need, added to the plan and use cases:** getting data out becomes phase 9 (file deliveries to clients and partners, encrypted, checked, approved and logged; shares for partners who read in place), and the phases after it move to 10–12. Limits per project, and retention and erasure of consumer data, join phase 6; a Connections tab (credentials given to the platform, not the cluster) and scheduling a notebook join phase 7; querying databases where they are starts phase 8.
- **What an install needs before real use, added to the plan:** Iceberg table maintenance, high availability and hardening, and alerts on data problems (phase 6); dbt (phase 7); getting data in, by connectors and change capture, as a new phase 8; classification of columns with Ranger's policies by tag (phase 9). Retention and erasure, templates, and data products are listed, not yet placed.

## [0.4.2] - 2026-10-13

The brand in the browser's tab too: its title and icon.

### Added
- **The brand in the browser's tab too:** its title (`brand.title`, by default "<name> Platform") and an icon (`brand.icon`, a square image, since a tab shows it at 16 to 32 pixels). platformd puts them in the app's page as it serves it, so the tab shows them from the first moment, the sign-in included, and serves the icon at `/brand/icon`. ICO images are taken too.

## [0.4.1] - 2026-10-13

A brand of your own in the top bar, and Superset's corner put right: its logo loads, and it no longer reports to Scarf.

### Added
- **A brand of your own in the top bar's corner** (`brand.name`, `brand.logo`): a logo (SVG, PNG, JPEG, GIF or WebP, given base64) takes the place of StorScale's icon and name, which stay the default. platformd serves it at `/brand/logo`, so that an SVG opened on its own runs nothing, and refuses at start-up a file that isn't an image.

### Fixed
- **Superset's logo, and its link, in its top-left corner.** Under `/dashboards`, Superset 6.1 puts its root in front of the logo's address and its frontend does it again (`/dashboards/dashboards/...`), so the logo was a broken image, and its link led nowhere. Superset gets them as whole addresses, which neither changes.
- **No usage pixel from Superset.** Superset 6.1's frontend always draws a tracking pixel from Scarf (`apachesuperset.gateway.scarf.sh`), which reports its version from inside the network. Superset runs without a content security policy of its own, so the gateway gives its pages one that allows images only from where Superset's default policy does: browsers never fetch the pixel.

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
- **Setup waits for Ranger's API, not its login page.** Ranger's login page answers before its REST API does, and in CI the API went on answering 404 for minutes after, so the first setup step failed on every try. Setup now waits until the API asks for a sign-in (401, unauthenticated, so it never counts toward Ranger's lockout), and Ranger's readiness probe and Compose's healthcheck check the same. On Kubernetes, a Ranger whose API isn't up 20 minutes after it starts is started again (a startup probe).
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
