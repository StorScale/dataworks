# Handoff: StorScale Platform on a real Kubernetes cluster

For whoever picks this up next, person or agent, on a machine that can reach
the target cluster. Read this, then [plan.md](plan.md) and the
[CHANGELOG](../CHANGELOG.md).

## Where things stand (2026-10-08)

- **Release 0.3.0** is phases 0–5 of the plan: the platform, its web app, projects,
  the catalog, the semantic layer with MCP for agents, and the Helm chart.
- **CI passes on both ways of running it:**
  - **`platform`** runs the Compose stack and every suite.
  - **`kubernetes`** installs the chart on kind with `storscale up --k8s`, then runs every
    suite with `helm test`, the browser suite included.
- **What's published, and signed:**
  - The images: `ghcr.io/storscale/platformd:0.3.0` and `ghcr.io/storscale/platform-*:0.3.0`.
  - The chart: `oci://ghcr.io/storscale/charts/storscale-platform` at version 0.3.0.
- **Only ever installed on kind.** It ran at `http://storscale.localhost:8800`, with a
  single node and CoreDNS rewritten so the platform's names resolve to the gateway.
  Nobody has installed it on a real cluster yet; that is the next step.

## Install on a real cluster

Needs about 12–16 GB of memory, 4 or more CPUs, about 60 GB of disk, Helm, and a
default StorageClass (or set `storage.storageClassName` and `buckets.storageClassName`).

```bash
helm install storscale oci://ghcr.io/storscale/charts/storscale-platform --version 0.3.0 \
  -n storscale --create-namespace \
  --set global.domain=platform.example.com \
  --set gateway.service.type=ClusterIP --set gateway.ingress.enabled=true \
  --set gateway.ingress.className=nginx \
  --set trino.service.type=ClusterIP
kubectl -n storscale get pods,jobs,projects -w      # the first install takes 10–20 minutes
helm test storscale -n storscale --logs --timeout 60m
```

Install with the subcharts on. Helm installs a subchart's CRDs (Buckets' and the
Spark Operator's) only on a fresh install, never on an upgrade.

People's password (the example users alice, bob, carol):
`kubectl -n storscale get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d`.
Every other secret is in the same Secret. They're generated on install and kept on upgrade.

## Untested on a real cluster: expect to fix these

1. **One address, inside and out.** Every service uses the platform's public names:
   `domain`, plus `catalog.`, `access.` and `s3.` under it. Keycloak's tokens carry
   that issuer, and Buckets, Trino and the tools fetch Keycloak's keys from it.
   - Point DNS for all four names at the Ingress.
   - Pods must resolve them to the gateway too. Ordinary DNS works if the load balancer
     allows hairpin traffic. If not, add a CoreDNS rewrite as
     [deploy/kind/dns.sh](../deploy/kind/dns.sh) does on kind.
2. **HTTPS, and which port.** `global.port` is two things at once: the port in every URL
   (`scheme://domain:port`), and the port the gateway (Caddy) listens on, over plain HTTP.
   - A real install wants TLS at the Ingress on 443, with URLs carrying no port.
   - Expect to split this into a public port (in URLs) and the gateway's own port.
     Keycloak (`KC_HOSTNAME`, `KC_PROXY_HEADERS=xforwarded`), the gateway's Caddyfile and
     the tools' callback URLs all follow `global.*`.
   - `global.scheme=https` has never been tried.
   - A private CA is ready: give it as `--set-file global.ca.certs=root-ca.pem` and every
     service that calls the platform's public names trusts it (Python, Go, curl, Java,
     notebooks, and Buckets through `SSL_CERT_DIR`). Tested on a real cluster against a
     corporate Vault PKI. The browser suite's Chromium doesn't read it yet: it keeps its own
     trust store (NSS).
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

- **[StorScale/buckets](https://github.com/StorScale/buckets):** the storage, at 1.16.0.
  The chart pins bucketsd 1.12.0 (`upstream.bucketsd`), the version tested here. Moving to
  1.16.0 is a values change; run the suites after.
- **[StorScale/buckets-website](https://github.com/StorScale/buckets-website):**
  storscale.io. `deploy/deploy.sh` builds from the Buckets checkout beside it (or
  `BUCKETS_REPO`), so pull that checkout first, or the site publishes an old version.
