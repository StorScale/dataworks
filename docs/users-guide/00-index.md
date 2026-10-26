# DataWorks Users Guide

How to set up and use each of the platform's features, one at a time. Each section says what a feature is for, who may use it,
how to set it up (on the platform's pages, in a project's spec, and through the API), how it behaves, the install settings that
change it, and its limits.

This guide is the reference. The [lab guide](../guide.md) is the other way in: one company's quarter, told in 22 labs that
build on each other, each with something to check. Where a lab shows a feature at work, the chapter here links to it.

The examples use the lab's company, Harbor Outfitters, its projects `retail` and `marketing`, and the example people:
**alice**, an analyst; **bob**, an engineer who also administers the platform; and **carol**, who has no access.

## Chapters

| | Chapter | What it covers |
|---|---|---|
| 1 | [Concepts and first sign-in](01-concepts.md) | The platform, people and groups, signing in, projects, the tools, what each person sees |
| 2 | [Projects](02-projects.md) | Making a project, its members, tables, files, pipelines, alerts and limits; project templates |
| 3 | [Getting data in](03-data-in.md) | Uploads, imports (files, APIs, databases, Salesforce, Gong, HighSpot, SAP S/4HANA), sources queried in place, change capture, connections |
| 4 | [Access and protection](04-access-and-protection.md) | Readers' rules (tables, row filters, masks), people's data, protection tags, what each role sees |
| 5 | [Data quality](05-quality.md) | Checks, freshness, profiling, the project's alerts |
| 6 | [Metrics and business terms](06-metrics-and-terms.md) | The semantic model, asking metrics, tests, sharing a model, business terms |
| 7 | [Dashboards, apps and assistants](07-dashboards-apps-assistants.md) | Superset on the models, apps, assistants over MCP |
| 8 | [Flows](08-flows.md) | The canvas: its steps, previews, the model step, saving as a merge request |
| 9 | [Pipelines and the project's repo](09-pipelines-and-git.md) | A GitLab repo, the project's Airflow, dbt, merge-request reviews, scheduled notebooks, shared libraries |
| 10 | [Notebooks and models](10-notebooks-and-models.md) | JupyterHub, buckets_lake, MLflow, approving models, scoring and drift, serving |
| 11 | [Data products](11-data-products.md) | Publishing, contracts, consumers, certification, reading in place |
| 12 | [Deliveries](12-deliveries.md) | Files to partners on a schedule, approved, with a manifest and a log |
| 13 | [Privacy](13-privacy.md) | Where a person's data is, what was sent about them, erasure, retention |
| 14 | [AI](14-ai.md) | The LLM gateway and budgets, the guardrail, agents, knowledge banks |
| 15 | [History and releases](15-history-and-releases.md) | A table's history, reading as it was, tags, the project as code, releases |
| 16 | [Roles](16-roles.md) | Built-in, project and platform roles, and what each permission gives |
| 17 | [Operating the platform](17-operating.md) | Installing, upgrading, health, audit, usage and cost, backups, Buckets capacity, the install's settings |
| 18 | [Troubleshooting](18-troubleshooting.md) | The errors people meet, what they mean, and what to do |

## Where else to look

- **This guide as one PDF:** [DataWorks-Users-Guide.pdf](DataWorks-Users-Guide.pdf), rebuilt with each release; each
  release's is also in the GitLab project's package registry (`dataworks-users-guide`), and the latest is the
  `users-guide` job's artifact on the default branch. Build it yourself with
  `python docs/users-guide/pdf/build.py . guide.pdf` (after `pip install -r docs/users-guide/pdf/requirements.txt`).
- [The lab guide](../guide.md): the platform end to end, in order, with checks.
- [The handoff](../handoff.md): how the platform is built and installed, and where the code is.
- The API reference: the account menu's **API reference** on the platform, or `/api/openapi.json`.
- [The Python package](../../stack/python/README.md): the API from Python, signed in as a person or a service account.
- [The changelog](../../CHANGELOG.md): what each release changed, and what an upgrade needs.
