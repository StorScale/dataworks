# Handoff: StorScale Platform on a real Kubernetes cluster

For whoever picks this up next, person or agent, on a machine that can reach
the target cluster. Read this, then [plan.md](plan.md) and the
[CHANGELOG](../CHANGELOG.md).

## Where things stand (2026-10-16)

- **Release 0.6.0** is phases 0–5 of the plan (the platform, its web app, projects, the
  catalog, the semantic layer with MCP for agents, and the Helm chart), ready for a real
  cluster: behind an ingress that ends TLS, with a private CA, on Buckets 1.19.0. It adds
  phase 7's first part: each project's pipelines from its own git repo, in an Airflow of
  its own on Kubernetes (plan, "Phase 7: pipelines from git, how"), and from phase 6 Keycloak on its own database and limits per project.
- **CI passes on every way of running it:**
  - **`platform`** runs the Compose stack and every suite.
  - **`kubernetes`** installs the chart on kind with `storscale up --k8s`, then runs every
    suite with `helm test`, the browser suite included.
  - **`kubernetes-https`** does the same behind HTTPS with a private CA, as on a real cluster
    (`deploy/kind/https.sh`, `https.yaml`): every suite but the browser's.
- **What's published, and signed:**
  - The images: `ghcr.io/storscale/platformd:0.6.0` and `ghcr.io/storscale/platform-*:0.6.0`.
  - The chart: `oci://ghcr.io/storscale/charts/storscale-platform` at version 0.6.0.
- **Installed on a real cluster:** Kubernetes 1.27, shared with other teams, with
  ingress-nginx and a certificate from a corporate Vault PKI (through cert-manager).
  Every suite but the browser's passes there. That install found the three bugs the
  `kubernetes-https` job now catches; see the CHANGELOG.

## Install on a real cluster

Needs about 12–16 GB of memory, 4 or more CPUs, about 60 GB of disk, Helm, and a
default StorageClass; and for each project with a pipelines repo, its own Airflow's memory
(`pipelines.memory`, 3 GB unless the project gives another) (or set `storage.storageClassName` and `buckets.storageClassName`).

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
helm install storscale oci://ghcr.io/storscale/charts/storscale-platform --version 0.6.0 \
  -n storscale --create-namespace -f platform.yaml --set-file global.ca.certs=root-ca.pem
kubectl -n storscale get pods,jobs,projects -w      # the first install takes 10–20 minutes
helm test storscale -n storscale --logs --timeout 60m
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
storscale upgrade --context <cluster> --version 0.7.0 --dry-run   # check; show the CRD's changes
storscale upgrade --context <cluster> --version 0.7.0             # then do it
```

`-f`, `--set` and `--set-file` change settings over those it was installed with. If it fails,
it prints the `helm rollback` to the revision before. CI's `kubernetes` job upgrades the last
release to each commit this way, with a project made under the old one, and runs every suite.

People's password (the example users alice, bob, carol):
`kubectl -n storscale get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d`.
Every other secret is in the same Secret. They're generated on install and kept on upgrade.

## On a real cluster

Items 1 and 2 work on one; 3 to 6 are still to do.

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
3. **Keycloak runs `start-dev`,** with its database in H2 inside the pod, and imports the
   realm afresh on each start (`stack/keycloak/realm.json`). Fine for a trial, not for
   real users: give it a database, and decide who owns the realm.
4. **The example users** (alice, bob, carol) and their groups come from the realm.
   `examples.users` exists in values but doesn't switch them off yet.
5. **Sizes:**
   - Buckets is one server with four drives (`buckets.servers`, `volumesPerServer`,
     `storage`).
   - Resource requests are set for a laptop; raise them for real use.
   - Trino gets 1.5 GB (`trino.memory`).
6. **No Spark suite yet.** The Spark Operator is installed, but nothing runs on it.

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
  suites' names, space-separated (`shell` is the browser suite). Then run `helm test`.
  Delete the ConfigMap to run them all.

## Where the code is

- `deploy/helm/storscale-platform`: the chart. It reads `stack/` through a `files/`
  link, so Compose and Kubernetes run the same configuration.
- `deploy/kind`: the kind cluster, its DNS rule, and the image loading
  (`storscale up --k8s` uses them).
- `stack/`: the Compose stack, the setup steps (`stack/tools/setup`), and the tests
  (`stack/tests`).
- `cmd/platformd`, `cmd/platform-operator`, `internal/store` (`Bucket` for Compose, `Kube`
  and `Mirror` for Kubernetes), and `web/`.

## Working conventions

- **Commits:** author `Russell Myers <russell.myers@outlook.com>`, ending with a
  `Co-Authored-By:` line for the agent. Ask before pushing.
- **Changelog:** keep `CHANGELOG.md`'s `[Unreleased]` current (Keep a Changelog).
- **License:** AGPL-3.0-or-later.
- **Naming:** Buckets' product name is plural (`BucketsCluster`, `buckets-*`); the singular
  is only for one S3 bucket.
- **Credentials:** the person running the session does any step that needs them. Never put
  a token or password in a command, a file or a commit.

## Related repositories

- **[StorScale/buckets](https://github.com/StorScale/buckets):** the storage. The platform
  pins 1.19.0: bucketsd in `upstream.bucketsd` and `stack/.env`, and the operator chart in
  `Chart.yaml` (then `helm dependency update`). Run the suites after moving it.
- **[StorScale/buckets-website](https://github.com/StorScale/buckets-website):**
  storscale.io. `deploy/deploy.sh` builds from the Buckets checkout beside it (or
  `BUCKETS_REPO`), so pull that checkout first, or the site publishes an old version.
