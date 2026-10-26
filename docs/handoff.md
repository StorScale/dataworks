# Handoff: DataWorks on a real Kubernetes cluster

For whoever picks this up next, person or agent, on a machine that can reach
the target cluster. Read this, then [plan.md](plan.md) and the
[CHANGELOG](../CHANGELOG.md).

## Where things stand (2026-10-24)

- **Release 0.34.0** has phases 0 to 5 of the plan (the platform, its web app, projects, the
  catalog, the semantic layer with MCP for agents, and the Helm chart), ready for a real cluster:
  behind an ingress that ends TLS, with a private CA, on Buckets 1.20.4. It has these from
  phases 6 to 16 too ([plan.md](plan.md)'s phase table says what each phase has and lacks,
  and [use-cases.md](use-cases.md) each requirement, with the date it was built):
  - **Phase 6, production-ready:** Keycloak on its own database; `storscale upgrade`; the Health
    page, with alerts to administrators and to each project (Teams, Slack, email); table
    maintenance and Nessie's GC; the audit log; backups and restore, into a new install too;
    high availability, network policies and Pod Security; limits per project; the data as it
    was (6.10); erasure under the CCPA and retention per table (6.9); profiling and more kinds
    of check (6.11); Buckets' capacity (drives grown by the chart, more pools, a project told as
    its bucket fills).
  - **Phase 7, git-native projects:** pipelines from each project's repo, in an Airflow of its own
    on Kubernetes; dbt in the repo; a merge request's change built on its own data branch,
    GitLab told (7.3); the project as code, planned and applied by another (7.4); the
    Connections tab; a notebook scheduled from JupyterLab; VS Code and the ML libraries.
  - **Phase 8, getting data in:** databases queried where they are (8.3), copied by change
    capture (8.1), applications' data and uploaded files imported with dlt (8.2), Salesforce,
    Gong, HighSpot and SAP S/4HANA (OData) from a form (8.4).
  - **Phase 9, getting data out:** file deliveries over SFTP and S3, approved before they run
    (9.1), and the delivery log searched by a person's ID (9.2).
  - **Phase 10, the business layer:** the company's terms in a shared semantic model (10.1),
    Superset's datasets on it (10.2), certified data products read in place across projects,
    with a form to publish one (10.3), and assistants over MCP on them (10.4).
  - **Phase 11, the canvas:** a flow of steps saved as dbt models in a merge request, uploads as
    inputs, a prepare step and an LLM step.
  - **Phase 12, governed AI:** the LLM gateway (LiteLLM on Microsoft Foundry, proven on a real
    cluster, or the stand-in) with budgets and a guardrail; agents of a project's own, from a
    notebook or a prompt, as tools of each other, with the project's own tools; semantic tests;
    a new column of personal data protected by its tag; knowledge banks; each model call; the
    Governance page.
  - **Phases 13 to 16:** usage and cost per project (13.1); a project's apps, the API reference
    and the Python package (14.1, 14.3); MLflow per project, approval, batch scoring with
    drift, a model as an API, models in the catalog (15.1); roles of the company's own, per
    project and platform-wide (16.1).
  - **Not yet placed, now built:** a new team's project from a template, and the company's
    shared libraries (U.2), with the install's GitLab.
- **What's next** (the "Not yet" of each phase in [plan.md](plan.md), and the open questions in
  [use-cases.md](use-cases.md)):
  - **Before real users:** corporate sign-in (Entra ID, brokered by Keycloak) and the example
    users switched off (`examples.users` exists and nothing reads it yet), access requests,
    and who approves them (6.1).
  - **Hardening:** Buckets' encryption at rest, Pod Security's restricted standard, more than one
    platformd (its sessions are in memory), a version matrix per release.
  - **Features left in built phases:** shares for partners (9.3); Power BI (10.2, to decide);
    a Python step and Spark (11.3, 11.6; nothing runs on the Spark Operator yet); the assistant
    on the SQL page (12.4); SAP HANA or BW as a database (8.4); a readers' Home per project and the
    package on PyPI (14); the models on the Governance page; a DAG ID clash refused, not only
    shown (7.2).
- **CI passes on every way of running it:**
  - **`platform`** runs the Compose stack and every suite (`stack/tests/run.py`: the suites that
    share `sales` in a chain, those with projects of their own alongside; the `guide` suite and
    the restore's run only when named).
  - **`kubernetes`** installs the chart on kind with `storscale up --k8s`, then runs every
    suite with `helm test`, the browser suite included.
  - **`kubernetes-https`** does the same behind HTTPS with a private CA, as on a real cluster
    (`deploy/kind/https.sh`, `https.yaml`): every suite but the browser's.
- **What's published, and signed:**
  - The images: `ghcr.io/storscale/platformd:0.34.0` and `ghcr.io/storscale/platform-*:0.34.0`.
  - The chart: `oci://ghcr.io/storscale/charts/storscale-platform` at version 0.34.0.
- **Installed on a real cluster:** Kubernetes 1.27, shared with other teams, with
  ingress-nginx and a certificate from a corporate Vault PKI (through cert-manager).
  Every suite but the browser's passes there. That install found the three bugs the
  `kubernetes-https` job now catches; see the CHANGELOG. The lab guide's labs were run end to end
  on a real cluster for 0.31.0; running them as the `guide` suite found most of 0.31.0's fixes.
- **The lab guide as a suite:** `stack/tests/guide.py` runs [guide.md](guide.md)'s labs through
  the API, as alice, bob and carol, and leaves what it makes. It runs only when named
  (`storscale test --k8s guide`, or `helm test` with the ConfigMap below); its `labs` key picks
  labs (`"1 2 3"`). Labs 10, 17, 18 and 20 (and 11's schedule) need retail to have a GitLab
  repo: its pipelines' repo set on its Edit page, and the connection `retail-dags` holding its
  token; they run (under `helm test` too) when it has one.

## Install on a real cluster

Needs about 12–16 GB of memory, 4 or more CPUs, about 60 GB of disk, Helm 3.14 or later
(`storscale upgrade` uses `--reset-then-reuse-values`), and a default StorageClass (or set
`storage.storageClassName` and `buckets.storageClassName`); and for each project with a
pipelines repo, its own Airflow's memory (`pipelines.memory`, 3 GB unless the project gives
another). Label the namespace for Pod Security, as `storscale up --k8s` does on kind:
`kubectl label namespace storscale pod-security.kubernetes.io/enforce=baseline
pod-security.kubernetes.io/warn=restricted`.

With TLS at the Ingress, a certificate from cert-manager, and a private CA, a values file
such as this one (`platform.yaml`):

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
      cert-manager.io/common-name: platform.example.com   # if the issuer wants one
      nginx.ingress.kubernetes.io/proxy-body-size: "0"     # S3 uploads
    tls:
      - secretName: storscale-tls
        hosts: [platform.example.com, catalog.platform.example.com, access.platform.example.com, s3.platform.example.com]
trino:
  service: {type: ClusterIP}
```

```bash
helm install storscale oci://ghcr.io/storscale/charts/storscale-platform --version 0.34.0 \
  -n storscale --create-namespace -f platform.yaml --set-file global.ca.certs=root-ca.pem
kubectl -n storscale get pods,jobs,projects -w      # the first install takes 10–20 minutes
helm test storscale -n storscale --logs --timeout 90m
```

Install with the subcharts on, unless the cluster already runs one: a Spark Operator that
another release installed owns the Spark CRDs (`--set spark-operator.enabled=false`). Helm installs a subchart's CRDs (Buckets' and the
Spark Operator's) only on a fresh install, never on an upgrade.

The same holds for the platform's own Project definition: without the new one, Kubernetes
silently drops whatever fields the old definition lacks from every project (0.4.x's lacks
`pipelines.repo` and the rest), and the operator never sees them. **Upgrade with
`storscale upgrade`**, which applies the release's Project definition first, then upgrades the
chart keeping the values it was installed with (`--reset-then-reuse-values`, so the new
release's defaults apply to new settings), pins the images to the release, and waits for every
service. It first checks the release isn't mid-upgrade, isn't going backwards, and that the
release's images are published; it says what it will do, and asks:

```bash
storscale upgrade --context <cluster> --version 0.34.0 --dry-run   # check; show the CRD's changes
storscale upgrade --context <cluster> --version 0.34.0             # then do it
```

`-f`, `--set` and `--set-file` change settings over those it was installed with. If it fails,
it prints the `helm rollback` to the revision before. CI's `kubernetes` job upgrades the last
release to each commit this way, with a project made under the old one, and runs every suite.

**Backups** are off until the chart has a certificate to encrypt them for. Make the
pair, give the chart the certificate, and keep the key somewhere else than the cluster
(without it no backup can be restored; with it anyone can read them):

```bash
storscale backup keygen --out ~/storscale-backup-keys
helm upgrade ... --set-file backup.certificate=~/storscale-backup-keys/storscale-backup-cert.pem
storscale backup --context <cluster>                     # one now; then nightly at 04:00 UTC
storscale restore --context <cluster> --key ~/storscale-backup-keys/storscale-backup-key.pem   # the latest
```

They go to the platform's own Buckets unless `backup.s3` names another S3 endpoint, which
a real install should: a lost cluster takes its own Buckets with it.

**A cluster lost:** with `backup.s3` elsewhere and `backup.data.enabled` (the data copied there
nightly), install the platform afresh on a new cluster with the same `backup.*` settings and
certificate, then

```bash
storscale restore --context <new cluster> --key <dir>/storscale-backup-key.pem --into-new
```

copies the data back, restores every database, gives each database role its password of then,
puts the backup's Secrets back, restarts Buckets and runs the setup steps again.

**Project templates and shared libraries** (U.2) need the company's GitLab: a group the new
projects' repos go in, and a token (scope `api`) that may make projects there and read the
templates' and libraries' repos, in a Secret of the release's namespace (key `token`; read at
each use, so a new token needs no restart):

```bash
kubectl -n storscale create secret generic storscale-gitlab --from-literal=token=<token>
storscale upgrade --context <cluster> --set templates.gitlab.url=https://gitlab.example.com \
  --set templates.gitlab.group=data/teams --set templates.gitlab.tokenSecret=storscale-gitlab
```

Without it, templates are only a spec (no repo), and there are no libraries. A new
project's repo token lasts a year (GitLab's most); renew it on the project's Connections tab.

**Alerts and the LLM gateway** need nothing at install, and say nothing until set:

- The administrators' alerts go where the Secret `storscale-alerts` says (`teamsWebhook`,
  `slackWebhook`, `emailTo`, and `smtpServer`, `smtpFrom`, `smtpUser`, `smtpPassword` for
  email); each project's go where its `notify` says.
- The gateway is the platform's stand-in (`llm.gateway: stub`) until the company's models are
  given: make the Secret `storscale-llm` (key `api_key`), then `--set llm.gateway=litellm` with
  `llm.litellm.models` (the chart's values show Anthropic's models on Microsoft Foundry, and an
  embedding model for the knowledge banks).

People's password (the example users alice, bob, carol):
`kubectl -n storscale get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d`.
Every other secret is in the same Secret. They're generated on install and kept on upgrade.

## On a real cluster

Items 1 to 3 work on one; 4 to 6 are still to do.

1. **One address, inside and out.** Every service uses the platform's public names:
   `domain`, plus `catalog.`, `access.` and `s3.` under it. Keycloak's tokens carry
   that issuer, and Buckets, Trino and the tools fetch Keycloak's keys from it.
   - Point DNS for all four names at the Ingress.
   - Pods must resolve them to the gateway too. Ordinary DNS works if the load balancer
     allows hairpin traffic. If not, add a CoreDNS rewrite as
     [deploy/kind/dns.sh](../deploy/kind/dns.sh) does on kind.
2. **HTTPS.** TLS ends at the Ingress: `global.scheme=https` and `global.port=443` give URLs
   with no port, and the gateway listens on `gateway.port` (8800) over plain HTTP.
   - Give the Ingress its certificate with `gateway.ingress.annotations` (cert-manager) and
     `gateway.ingress.tls`, for the domain and `catalog.`, `access.`, `s3.` under it.
   - With the Ingress on, the gateway trusts `X-Forwarded-Proto` from private ranges
     (`gateway.trustedProxies`), so Superset and Airflow build `https` sign-in callbacks.
   - ingress-nginx limits request bodies to 1 MB unless told otherwise
     (`nginx.ingress.kubernetes.io/proxy-body-size: "0"`), which S3 uploads hit.
   - A private CA is ready: give it as `--set-file global.ca.certs=root-ca.pem` and every
     service that calls the platform's public names trusts it (Python, Go, curl, Java,
     notebooks, and Buckets through `SSL_CERT_FILE`, since bucketsd uses OpenSSL). The browser
     suite's Chromium doesn't read it yet: it keeps its own trust store (NSS).
3. **Keycloak** runs in production mode on a database of its own (`keycloak`, on the
   platform's Postgres), and imports the realm (`stack/keycloak/realm.json`) only when there's
   none. At each install and upgrade a setup step puts back the platform's own parts of it (its
   clients, service accounts and settings); people, groups and whatever an administrator adds
   stay (plan, "Phase 6: Keycloak for real use").
4. **The example users** (alice, bob, carol) and their groups come from the realm, and
   corporate sign-in (Entra ID) isn't connected yet. `examples.users` exists in values but
   nothing reads it: it doesn't switch them off yet.
5. **Sizes:**
   - Buckets is one server with four drives (`buckets.servers`, `volumesPerServer`,
     `storage`). To grow it, see **Buckets' capacity** below.
   - Resource requests are set for a laptop; raise them for real use.
   - Trino gets 2.5 GB (`trino.memory`, 2560Mi), and no workers unless `trino.workers` says.
6. **No Spark yet.** The Spark Operator is installed, but nothing runs on it: no suite, no
   canvas step, no usage counted.

### Buckets' capacity

Buckets' usable space is its drives' total less parity: four drives at EC:2 hold half
their size. The administrators are told as it fills (the `BucketsCapacityLow` rule at 15%
free, `BucketsCapacityCritical` at 5%, `BucketsCapacityFullIn7Days`), and each project's
people as its own bucket nears its `limits.storage`. Two ways to grow it:

**Bigger drives** (the storage class must allow it: `kubectl get storageclass <class> -o
jsonpath='{.allowVolumeExpansion}'` says `true`). Raise every drive of the first pool
together; an erasure set holds only as much as its smallest drive:

```bash
storscale upgrade --context <cluster> --version <installed> --set buckets.storage=40Gi --dry-run
storscale upgrade --context <cluster> --version <installed> --set buckets.storage=40Gi
kubectl -n storscale get pvc -l buckets.io/cluster=buckets   # within five minutes: 40Gi each
```

The chart keeps the drives' claim template as it was made (Kubernetes never lets a
StatefulSet's change, and Buckets' operator would stop at it), and the platform operator
asks for the new size on each drive's claim. Most drivers grow the file system while the
drive is in use; if a claim's `FileSystemResizePending` condition stays, delete one Buckets
pod at a time (the operator's StatefulSet makes it again) and wait for it to be ready
before the next. `mc admin info` then shows the new total. The dry run refuses a smaller
size, another storage class or another shape for a pool that exists, before anything changes.

**Another pool**, when the drives can't grow or more servers are wanted: a whole set of
servers and drives beside the first, usually as many drives as it, of any size:

```bash
storscale upgrade ... --set 'buckets.pools[0].servers=1' --set 'buckets.pools[0].volumesPerServer=4' \
  --set 'buckets.pools[0].storage=100Gi'   # optional: buckets.pools[0].storageClassName
```

Adding one restarts every Buckets server together, so S3 is down for a minute or two; new
objects then go to the pool with the most free space. A pool can't be removed through the
chart (its data would go with it), and nothing moves existing objects unless someone runs
`mc admin rebalance start`. Keep the pools in your values file: `storscale upgrade` reuses
the installed values, and a later `-f` without them is refused.

## Things we learned the hard way

- **A private CA reaches each client its own way.** CI's `kubernetes-https` job now tests it;
  the first real install found these:
  - bucketsd is C with OpenSSL: it ignores `SSL_CERT_DIR` files without hashed names, so it
    gets `SSL_CERT_FILE`.
  - Python's `requests` puts `REQUESTS_CA_BUNDLE` in place of a session's own `verify=`,
    which is how the Trino client trusts Trino: the bundle holds Trino's certificate too.
  - Airflow's API server (uvicorn) trusts `X-Forwarded-Proto` only from
    `FORWARDED_ALLOW_IPS`, `127.0.0.1` by default, so without it Airflow sends Keycloak an
    `http://` callback, which Keycloak refuses.

- **Buckets reads Keycloak's OpenID configuration only as it starts.** If it starts before
  Keycloak, nobody can get Buckets credentials from a Keycloak token
  (`role arn:minio:iam:::role/... not found`).
  - The first setup step checks this and replaces Buckets' pods if needed
    (`ensure_buckets_identity` in `stack/tools/lakekit.py`).
  - A fix in Buckets itself, retrying the discovery, would be better.
- **Pods have `enableServiceLinks: false`.** Kubernetes' `SUPERSET_PORT=tcp://...`
  variables broke Superset, which read one as its own port.
- **First installs are slow** while ~10 GB of images download, and services wait for the
  setup Jobs. The chart's Deployments get an hour (`progressDeadlineSeconds`). `storscale up
  --k8s` reports every half minute which pods aren't ready and why.
- **Ranger's admin server needs its 1.5 GiB.** After a restart, its start script waits
  10 minutes on the mark the interrupted run left.
- **OpenSearch's disk thresholds are off.** On a nearly full disk they made the catalog's
  indices read-only, and lineage calls then hung.
- **Setup steps are Jobs named after the release revision.** Each `helm upgrade` runs them
  again, and they're safe to run again. Services wait for them through the ConfigMap
  `storscale-setup-done`.
- **`helm test --logs` also prints the logs of test pods from an earlier run** when an
  early test fails. Read the pod names.
- **To pick suites:** create the ConfigMap `storscale-tests`, with key `suites` holding the
  suites' names, space-separated (`shell` is the browser suite; `guide` the lab guide, with
  key `labs` for its labs). Then run `helm test`. Delete the ConfigMap to run them all (the
  guide's suite and the restore's run only when named). `storscale test --k8s <suite> ...`
  does this on kind.

## Where the code is

- `cmd/platformd`: the platform's backend, its API (`openapi.json`, kept current by a test)
  and the web app's server; `web/`: the web app (React).
- `cmd/platform-operator` and `internal/reconcile`: projects applied to Keycloak, Ranger,
  Buckets, Nessie, Trino, Airflow and the LLM gateway, their scorings and servings, and
  Buckets' drives grown.
- `cmd/storscale`: the command (`up`, `test`, `upgrade`, `backup`, `restore`).
- `internal/`: the project spec and its checks (`project`), the project store (`store`:
  `Bucket` for Compose, `Kube` and `Mirror` for Kubernetes), the canvas's compiler (`flow`),
  GitLab (`gitlab`), Airflow (`pipelines`), Trino, the catalog, health, the audit log and the
  LLM gateway.
- `deploy/helm/storscale-platform`: the chart. It reads `stack/` through a `files/`
  link, so Compose and Kubernetes run the same configuration. `deploy/crds/project.yaml`:
  the Project definition.
- `deploy/kind`: the kind cluster, its DNS rule, the image loading, and values for CI's
  variants (`https.yaml`, `ha.yaml`, `templates.yaml`, ...); `storscale up --k8s` uses them.
- `stack/`: the Compose stack and each service's image and configuration: semanticd and the
  MCP server (`stack/semantic`), catalog-sync (`stack/catalog/sync`), Airflow's image with the
  platform's helpers for DAGs (`stack/airflow/image`), the notebook image with `buckets_lake`
  and the model templates (`stack/jupyterhub/image`), the setup steps (`stack/tools/setup`),
  and the end-to-end suites (`stack/tests`, `guide.py` among them).
- `stack/python`: the `storscale` Python package, the API from a script.
- `docs/`: the [lab guide](guide.md) with its data (`docs/lab/`), the
  [Users Guide](users-guide/00-index.md), [plan.md](plan.md) and [use-cases.md](use-cases.md).

## Working conventions

- **Commits:** author `Russell Myers <russell.myers@outlook.com>`, ending with a
  `Co-Authored-By:` line for the agent. Ask before pushing.
- **Changelog:** keep `CHANGELOG.md`'s `[Unreleased]` current (Keep a Changelog).
- **Checks before a push:** `gofmt`, `go vet`, `go test ./...`, `npm run build` in `web/`, and
  `ruff check stack` with a compile of the Python (`ruff.toml`), as CI's `cli` job runs them;
  then the suites in CI. Push bundles of related commits, not single ones.
- **License:** AGPL-3.0-or-later.
- **Naming:** Buckets' product name is plural (`BucketsCluster`, `buckets-*`); the singular
  is only for one S3 bucket.
- **Credentials:** the person running the session does any step that needs them. Never put
  a token or password in a command, a file or a commit.

## Related repositories

- **[StorScale/buckets](https://github.com/StorScale/buckets):** the storage. The platform
  pins 1.20.4: bucketsd in `upstream.bucketsd` and `stack/.env`, and the operator chart in
  `Chart.yaml` (then `helm dependency update`). Run the suites after moving it.
- **[StorScale/buckets-website](https://github.com/StorScale/buckets-website):**
  storscale.io. `deploy/deploy.sh` builds from the Buckets checkout beside it (or
  `BUCKETS_REPO`), so pull that checkout first, or the site publishes an old version.
