# DataWorks

DataWorks is one open data platform on [Buckets](https://github.com/StorScale/buckets). People sign in once, to one place, and work in **projects**: a project brings together a team's data, notebooks, pipelines, dashboards, metrics, models and agents, and who may do what with them. Access is granted once, and the platform puts it into every tool underneath.

It brings together:

- **Tables:** Iceberg, Nessie and Trino, on Buckets
- **Notebooks:** JupyterHub, with VS Code in the browser
- **Dashboards:** Superset
- **Pipelines:** Airflow and dbt, from each project's git repo; dlt imports and Debezium change capture
- **Metrics:** a semantic layer (MetricFlow), with an MCP server for assistants and agents
- **Catalog, lineage and data quality:** OpenMetadata and OpenLineage
- **Models:** MLflow, and an LLM gateway (LiteLLM) in front of the company's models
- **Access:** Keycloak and Apache Ranger
- **Monitoring:** Prometheus and Grafana

## What it does (0.31.0)

- **One web app with one sign-in.** Notebooks, SQL, dashboards, pipelines and monitoring open inside it; each project has its own pages for its data, pipelines, metrics, apps, models, agents and access.
- **Projects** defined once, and applied in Keycloak, Ranger, Buckets, Nessie, Trino and Airflow. Readers see their rows and masked columns everywhere, in SQL, dashboards, notebooks and agents' answers alike. Roles of the company's own, per project and platform-wide.
- **Git-native pipelines:** each project's DAGs and dbt models from its repo, in an Airflow of its own; a merge request built and tested on a data branch of its own; the project itself released from its repo.
- **Data in and out:** databases queried in place or copied by change capture, applications' data and files imported, Salesforce, Gong, HighSpot and SAP S/4HANA from a form; file deliveries to clients over SFTP and S3, approved before they run.
- **The business layer:** the company's terms and metrics defined once, the same in SQL, Superset and assistants; certified data products read in place across projects.
- **A pipeline canvas** that writes dbt models as a merge request; a step that asks a model about a column.
- **Governed AI:** an LLM gateway with budgets and a guardrail; agents with their own identity, approved before they run; knowledge banks; new personal columns masked by their tag.
- **Models:** MLflow per project, approval before use, batch scoring with drift, a model as an API.
- **For the people who run it:** health and alerts, the audit log, usage and cost per project, limits, backups and restore, one-command upgrades, a Helm chart for a real cluster.

## Documentation

- **[The lab guide](docs/guide.md):** one company's quarter in 22 labs, with its data, saying what the business gets from each. Start here.
- **[The Users Guide](docs/users-guide/00-index.md):** the platform page by page, for the people who use it.
- **[Handoff](docs/handoff.md):** installing and upgrading on a real Kubernetes cluster, where things stand, and where the code is.
- **The API reference:** in the web app (the account menu's **API reference**), or as OpenAPI at `/api/openapi.json` ([cmd/platformd/openapi.json](cmd/platformd/openapi.json)).
- **[The Python package](stack/python/README.md):** `storscale`, the API from a script, signed in as a person or a service account.
- **[The plan](docs/plan.md)** and **[the use cases](docs/use-cases.md):** what's built, phase by phase, and what's next.
- **[CHANGELOG](CHANGELOG.md):** what each release added, and how to upgrade to it.
- **[stack/README.md](stack/README.md):** the services, and how they fit together.

## Try it on one machine (Docker Compose)

You need Docker with Compose 2.24 or later, about 10 GB of memory for Docker, and Go 1.26 or later to build the command.

```bash
go build -o bin/storscale ./cmd/storscale
bin/storscale up --dir stack      # this checkout's stack: builds the platform's images too
bin/storscale test --dir stack    # the end-to-end checks, the browser's included
```

Then open http://storscale.localhost:8800 and sign in as `alice` (an analyst), `bob` (an engineer, who also administers the platform) or `carol` (who has no access). Their password is `LAKEHOUSE_USER_PASSWORD` in [stack/.env](stack/.env), where every password is a local test value. `storscale urls` lists where each tool is.

One Keycloak sign-in covers the platform and every tool in it: JupyterHub, Superset, Airflow, Grafana and the catalog. Ranger is for administrators: sign in as `admin` with `RANGER_PASSWORD`.

| | |
|---|---|
| The platform | http://storscale.localhost:8800 |
| Notebooks (JupyterHub) | http://storscale.localhost:8800/notebooks/ |
| SQL and dashboards (Superset) | http://storscale.localhost:8800/dashboards/ |
| Pipelines (Airflow) | http://storscale.localhost:8800/pipelines/ |
| Monitoring (Grafana) | http://storscale.localhost:8800/monitoring/ |
| Catalog (OpenMetadata, in its own tab) | http://catalog.storscale.localhost:8800/_storscale/launch.html |
| Access policies (Ranger) | http://access.storscale.localhost:8800 |
| Agents (MCP) | http://storscale.localhost:8800/mcp |
| S3 (Buckets) | http://s3.storscale.localhost:8800 |
| Trino, for JDBC and CLI clients | https://localhost:8443 |

From a checkout, `--dir stack` builds the platform's images from it. Without it, `storscale` runs the stack built into the binary, with the released images, at its own version.

`storscale test` runs every suite, or those named (`storscale test --dir stack lakehouse projects`). The lab guide is a suite too, run only when named: `storscale test --dir stack guide` does its labs through the API and leaves what it makes, for you to look at.

`storscale down` stops the platform, and `storscale down --volumes` also deletes its data. `storscale status` and `storscale logs [service]` say what's running.

With rootless Docker (Lima, for example), set `DOCKER_SOCK=/run/user/<uid>/docker.sock` so JupyterHub can start notebook servers.

## On Kubernetes

The Helm chart, [deploy/helm/storscale-platform](deploy/helm/storscale-platform), runs the same platform on Kubernetes, with the stack's own configuration. It's published as `oci://ghcr.io/storscale/charts/storscale-platform`, with the images at `ghcr.io/storscale`.

**On a kind cluster of its own**, `storscale --k8s` runs it. It needs kind, kubectl and Helm, and about 10 GB of memory for Docker.

```bash
bin/storscale up --dir stack --k8s     # a kind cluster, this checkout's images, and the chart
bin/storscale test --dir stack --k8s   # the same checks, as helm test
kubectl --context kind-storscale -n storscale get projects
```

Without `--dir`, `storscale up --k8s` installs the published chart at its own version (or `--version`). The addresses are the same as with Compose. Every password is generated on install and kept in the Secret `storscale-env`: `storscale up --k8s` prints how to read the people's. `storscale down --k8s` uninstalls the platform; `--volumes` also deletes the cluster.

**On a real cluster**, install the chart with Helm:

```bash
helm install storscale oci://ghcr.io/storscale/charts/storscale-platform --version 0.31.0 \
  -n storscale --create-namespace -f platform.yaml
```

The platform's domain (`global.domain`, with `catalog.`, `access.` and `s3.` under it) must resolve to the gateway, from people's browsers and from inside the cluster too (on kind, [deploy/kind/dns.sh](deploy/kind/dns.sh) does that with one rule in the cluster's DNS). If a private CA signs the platform's certificate, give it to the chart (`--set-file global.ca.certs=root-ca.pem`) so the services trust it. [docs/handoff.md](docs/handoff.md) has the values file for an ingress that ends TLS, backups, templates' GitLab, the LLM gateway, Buckets' capacity, and what's still to do on a real cluster.

**Upgrades:** `storscale upgrade --context <cluster> --version <release>` takes the platform on any Kubernetes cluster to a newer release: the release's Project definition first, which Helm never upgrades, then the chart with the values it was installed with, then it waits for every service. It checks first and asks; `--dry-run` only checks. `storscale backup` and `storscale restore` take and restore backups (see the handoff).

## Licence

AGPL-3.0, like Buckets. See [LICENSE](LICENSE).
