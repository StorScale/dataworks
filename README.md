# StorScale Platform

StorScale Platform is one open data platform on [StorScale Buckets](https://github.com/StorScale/buckets). It brings together:

- **Tables:** Iceberg, Nessie and Trino
- **Notebooks:** JupyterHub
- **Dashboards:** Superset
- **Pipelines:** Airflow and dbt
- **Catalog, lineage and data quality:** OpenMetadata, OpenLineage and Soda
- **Access:** Keycloak and Apache Ranger
- **Monitoring:** Prometheus and Grafana

People sign in once to one place. They work in **projects**: a project brings together a team's data, notebooks, pipelines and dashboards, and the access to them. Access is granted once, and the platform puts it into every tool.

> **Status:** phase 5 of [the plan](docs/plan.md).
> - **One web app** with one sign-in. Notebooks, SQL, dashboards, pipelines and monitoring open inside it.
> - **Projects:** an administrator defines a project, and the platform applies its access in Keycloak, Ranger, Buckets and Nessie.
> - **A catalog:** OpenMetadata, with each project's lineage (column by column) and data-quality checks, shown in each project's Data and Overview pages.
> - **A semantic layer that agents can use:** each project's metrics are defined once (MetricFlow). Agents use them over MCP at `/mcp`, signed in through Keycloak as the person they work for, under that person's access rules.
> - **Kubernetes:** a Helm chart runs the same platform, with the same checks passing. Projects are `Project` resources, Buckets runs under its operator, and notebooks are pods.

## Try it

You need Docker with Compose 2.24 or later, about 10 GB of memory for Docker (the stack uses about 8 GB), and Go 1.26 or later to build the command.

```bash
go build -o bin/storscale ./cmd/storscale
bin/storscale up --dir stack      # this checkout's stack: builds the platform's image too
bin/storscale test --dir stack    # the end-to-end checks, the browser's included
```

Then open http://storscale.localhost:8800, the platform, and sign in as `alice` (an analyst) or `bob` (an engineer). Their password is `LAKEHOUSE_USER_PASSWORD` in [stack/.env](stack/.env). Every password there is a local test value.

One Keycloak sign-in covers the platform and every tool in it: JupyterHub, Superset, Airflow and Grafana. Ranger is for administrators: sign in as `admin` with `RANGER_PASSWORD`.

| | |
|---|---|
| Notebooks (JupyterHub) | http://storscale.localhost:8800/notebooks/ |
| SQL and dashboards (Superset) | http://storscale.localhost:8800/dashboards/ |
| Pipelines (Airflow) | http://storscale.localhost:8800/pipelines/ |
| Monitoring (Grafana) | http://storscale.localhost:8800/monitoring/ |
| Catalog (OpenMetadata) | http://catalog.storscale.localhost:8800 (opens in its own tab) |
| Access policies (Ranger) | http://access.storscale.localhost:8800 |
| Trino, for JDBC and CLI clients | https://localhost:8443 |

From a checkout, `--dir stack` builds the platform's images from it. Without it, `storscale` runs the stack built into the binary, with the released images, at its own version. On Kubernetes, without a checkout, `storscale up --k8s` installs the published chart, `oci://ghcr.io/storscale/charts/storscale-platform`, at its own version.

`storscale down` stops it, and `storscale down --volumes` also deletes its data. [stack/README.md](stack/README.md) describes the services.

With rootless Docker (Lima, for example), set `DOCKER_SOCK=/run/user/<uid>/docker.sock` so JupyterHub can start notebook servers.

## On Kubernetes

The Helm chart, [deploy/helm/storscale-platform](deploy/helm/storscale-platform), runs the same platform on Kubernetes, with the stack's own configuration. `storscale --k8s` runs it on a [kind](https://kind.sigs.k8s.io) cluster of its own. It needs kind, kubectl and Helm, and about 10 GB of memory for Docker.

```bash
bin/storscale up --dir stack --k8s     # a kind cluster, this checkout's images, and the chart
bin/storscale test --dir stack --k8s   # the same checks, as helm test
kubectl --context kind-storscale -n storscale get projects
```

The addresses are the same as with Compose. On Kubernetes, every password is generated on install and kept in the Secret `storscale-env`: `storscale up --k8s` prints how to read the people's. `storscale down --k8s` uninstalls the platform; `--volumes` also deletes the cluster.

On another cluster, install the chart, and make the platform's domain (`global.domain`, with `catalog.`, `access.` and `s3.` under it) resolve to the gateway Service. Inside the cluster it has to resolve to the gateway too: on kind, [deploy/kind/dns.sh](deploy/kind/dns.sh) does that with one rule in the cluster's DNS. If a private CA signs the platform's certificate, give it to the chart (`--set-file global.ca.certs=root-ca.pem`) so the services trust it. [docs/handoff.md](docs/handoff.md) covers installing on a real cluster, and what hasn't been tested there yet.

## Licence

AGPL-3.0, like Buckets. See [LICENSE](LICENSE).
