# 15. History and releases

Two ways of keeping things true over time. **History**: every change to a project's tables is a commit of the catalog, so a
table can be read as it was at any moment the install still keeps, and a tag keeps a moment (a month-end close) for as long
as the project says. **Releases**: the project itself (its members, rules, checks, products and metrics) kept as code in its
repo, planned from a tag by one person and applied by another, and rolled back by applying an earlier one. This chapter is
for analysts and finance users who need last month's numbers again, the editors who make tags and plan releases, and the
administrators who apply them and set how much history the install keeps.

In this chapter:

- [A table's history](#a-tables-history)
- [Reading the tables as they were (As of)](#reading-the-tables-as-they-were-as-of)
- [Tags: moments kept, such as a close](#tags-moments-kept-such-as-a-close)
- [Nightly maintenance and what history is kept](#nightly-maintenance-and-what-history-is-kept)
- [The project as code](#the-project-as-code)
- [Planning and applying a release](#planning-and-applying-a-release)
- [Rolling back](#rolling-back)
- [The API](#the-api)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

---

## A table's history

**What it's for.** Seeing what happened to a table: each change, when, what it did (rows added and removed), who wrote it,
and which tags hold it; and reading it as it was before any of them.

**Who can do it.** Any member of the project. Reading a change as it was runs as you, under your rules: a reader who sees
the EU's rows and masked card numbers today sees the same in last month's data.

### Set up

Open **Data**, pick a table, and its **History** tab. It lists the table's changes, newest first:

| Column | What |
|---|---|
| When | The commit's time, and its hash (the first twelve characters) |
| What | What the change did: its operation (`append`, `overwrite`, `delete`), `+N rows`, `−N rows`, and the rows the table had then; the project's tags on that commit, as blue pills |
| Who | Who wrote to the table then, from the audit log; `–` when it can't tell |
| | **Read it as it was**, or "past the history kept" |

**Read it as it was** opens the SQL tab with `SELECT * FROM "uploads_orders#<commit>" LIMIT 100`: the table named at that
commit. Change the query as you like; it runs as you.

### How it behaves

- The catalog (Nessie) keeps every table's changes as commits on its `main` branch; a table is read at one by its name and
  the commit, `"uploads_orders#3f1c0e9a2b7d4c55…"` (quoted, because of the `#`). Trino passes the name to Nessie, and
  Ranger's policies name it too, so row filters and masks hold.
- **A table's first entry is its creation**, which holds no rows (it reads as a change with 0 rows then). For an imported
  table, **the import's first load is the entry after it**: read that one to see the table as the import left it. See
  [Lab 19](../guide.md#lab-19-the-data-as-it-was).
- The tab shows up to the 50 newest changes, looking back over at most 5,000 of the catalog's commits (the catalog is shared
  by every project, so on a busy install an old table's first changes may be further back than that). Rows and who wrote
  are filled in for the 25 newest changes still kept.
- **Who** is the user Trino's audit (Ranger) records writing to the table (an insert, update, delete, merge, create or
  truncate) from three minutes before the commit to one minute after: a person, or a project's pipelines service account.
- A change older than the history the install keeps, and not held by a tag, says "past the history kept"; its data files
  may be gone ([what is kept](#nightly-maintenance-and-what-history-is-kept)). The newest change is always readable.
- The tab's note says how far back: "The data as it was is kept 168h back, and as long as a tag holds it."

---

## Reading the tables as they were (As of)

**What it's for.** Reproducing a number from last month exactly: every table of a query read as of a tag, a time or a commit.

**Who can do it.** Any member, on the SQL tab, under their own rules.

### Set up

1. On the **SQL** tab, write the query with the project's own table names:
   `SELECT round(sum(amount), 2) FROM uploads_orders WHERE NOT test`.
2. In the box under it (**As of: now, a tag, 2026-09-30, a commit**), type the moment and press **Read the tables then**
   (or Enter). The query's project tables are rewritten to that commit (`FROM "uploads_orders#<commit>"`), and the moment is
   shown: "as of the tag close-2026-09: commit 3f1c0e9a2b7d".
3. Run it. **As of** `now` (or an empty box) puts the names back.

A moment is one of:

| You type | It means |
|---|---|
| `now` (or nothing) | The catalog's main as it is |
| A tag of the project's: `close-2026-09` | The commit the tag keeps |
| A commit: 16 to 64 hex characters | That commit |
| A time: `2026-09-30T18:00:00Z`, `2026-09-30T18:00` or `2026-09-30 18:00` (UTC) | The commit of main current at that moment |
| A date: `2026-09-30` | As it was at the day's end, 23:59:59 UTC |

Through the API, `GET /api/projects/retail/asof?at=close-2026-09` answers `{commit, time, label}`; a notebook or a script
names the table at that commit itself: `SELECT … FROM "uploads_orders#<commit>"` through `buckets_lake.sql()`
([Notebooks and models](10-notebooks-and-models.md)).

### Limits

- A moment it can't read: ""last week": now, one of the project's tags, a time (2026-09-30 or 2026-09-30T18:00:00Z) or a commit".
- A time before the history kept: "the catalog's history kept starts after 2026-09-01 00:00 UTC".
- Only `FROM` and `JOIN` of the project's own tables are rewritten; another project's product named in the query is read as
  it is now.
- There is no restore button: to put a table back, read it as it was and write it with SQL (`CREATE TABLE … AS SELECT …
  FROM "uploads_orders#<commit>"`), which needs `write-tables`.

---

## Tags: moments kept, such as a close

**What it's for.** A month-end close, the data a model learned from, the state at an audit: every table of the project as it
was at a moment, kept for as long as you say (seven years for a close), whatever the install's history window does to the rest.

**Who can do it.** Seeing the tags: any member. Making and removing them: the permission `release` (editors).

### Set up

On the **Tags** tab, **Make a tag**:

| Field | What |
|---|---|
| Name | Lower-case letters, digits and dashes: `close-2026-09` |
| At | A moment, as **As of** takes it: `now`, `2026-09-30`, a commit (default `now`) |
| Keep for | Days or years: `90d`, `7y` (default `7y`) |
| Note | What it is, at most 500 characters: "September's close, signed off by Finance" |

Press **Make the tag**. The list shows each tag's name, its commit, **Kept until**, the note and who made it, and
"being applied" until the catalog has it. **Remove** takes it away.

The same in the spec, `tables.tags` (the Tags tab writes it; `by` is who made it):

```json
{"tables": {"tags": [
  {"name": "close-2026-09", "commit": "3f1c0e9a2b7d4c55e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d",
   "keepUntil": "2033-10-03", "note": "September's close, signed off by Finance", "by": "bob"}
]}}
```

Through the API: `POST /api/projects/retail/tags` with

```json
{"name": "close-2026-09", "at": "now", "keep": "7y", "note": "September's close"}
```

and `DELETE /api/projects/retail/tags/close-2026-09`.

### How it behaves

- A tag names the catalog's commit on main at that moment: every table of the project, as it was. The platform saves it in
  the project's spec; within a minute the operator makes the Nessie tag `<project>.<name>` (`retail.close-2026-09`) at that
  commit, and removes those no longer listed.
- Read a table at a tag with **As of** on the SQL tab, or from its History, where the tag shows on the change it keeps.
- Nessie's GC keeps whatever a tag points to, however old, so a tagged moment stays readable past the history window.
- Each night, before the tables are kept, tags past their **Kept until** are removed from every project
  ([below](#nightly-maintenance-and-what-history-is-kept)); asking for maintenance by hand does the same.
- The audit log records `tag.made` (its commit, the moment, until when, the note), `tag.removed`, and `tag.expired`
  (by `storscale-platform`).
- Deliveries of personal data keep the catalog as each run read it the same way, with tags of their own
  ([Deliveries](12-deliveries.md)); what an erasure means for tagged closes is in [Privacy](13-privacy.md).

### Limits

- Without `release`: "the project's editors make and remove its tags" (HTTP 403). A project without tables: "the project has
  no tables" (409).
- A name taken: "the project has a tag close-2026-09 already" (409). A bad name: "tables: tags: "Close Sept" isn't a tag's
  name (lower case letters, digits and -)".
- **Keep for** as `90d` or `7y`: "keep: as 90d or 7y".
- A tag holds every table's data files at that commit: a long-kept tag on fast-changing tables keeps their old files in Buckets.

See [Lab 19](../guide.md#lab-19-the-data-as-it-was).

---

## Nightly maintenance and what history is kept

**What it's for.** Keeping the tables fast and their storage in check: small files compacted each night, rows past their
retention deleted, and history older than the install's window removed, except what tags hold.

**Who can do it.** It runs by itself. Asking for a project's maintenance now: `run-pipelines` (editors). Pausing it: an
administrator, in the spec. Setting the window: the install's operators.

### How it behaves

Each night at `maintenance.at` (02:00 UTC by default), platformd:

1. removes the projects' tags past their keep, and the deliveries' tags past `deliveries.keepFor`;
2. queues every project, and keeps them one at a time, as Trino's `maintenance` account. For each table of the project:
   - **retention** first, when `tables.retention` names the table: `DELETE` the rows whose date column is older than its
     keep ([Privacy](13-privacy.md));
   - **optimize**: `ALTER TABLE … EXECUTE optimize`, small files compacted into larger ones.
   Each step may take up to 30 minutes; a failed step ends that table's maintenance and says why.

Later (`maintenance.history.schedule`, 03:30 UTC by default), **Nessie's GC** deletes, across the install, the tables'
history older than `maintenance.history.keep` (`P7D`, a week, by default) and the files no branch or tag needs. Iceberg's
own snapshot expiry is off: Nessie keeps a table's history as its commits, shared by every project.

So, of a table's past: the last week (by default) is readable at any moment; older moments are readable only where a tag
(a close, a delivery's read, a merge request's data branch) holds them; the rest is gone.

### Set up

- **Run now**: **Data** → a table → **Storage**, **Run now** (all of the project's tables, one at a time). The button says
  "Waiting its turn…" while queued and "Keeping the tables…" while it runs. The Storage tab shows the table's data files,
  rows, and what the last maintenance did (the files and bytes before and after, and how long it took) or why it failed. The audit log records
  `maintenance.asked`.
- **Pausing** a project's maintenance (an administrator, in the spec):

  ```json
  {"tables": {"maintenance": {"paused": true}}}
  ```

- A table whose last maintenance failed is one of the project's alerts. Prometheus has
  `platform_table_maintenance_last_run_seconds`, `platform_table_maintenance_failed` and `platform_table_data_files`, per
  project and table.

### Limits

- Paused, or no tables: "the project's tables have no maintenance (none, or paused)" (409).
- Without the maintenance account (`MAINTENANCE_TRINO_PASSWORD`), there's no maintenance: "Table maintenance isn't set up
  on this install."
- Maintenance remembers its last runs only since platformd last started: "Not kept since the platform last started."

---

## The project as code

**What it's for.** What a project is (its members, readers' rules, checks, imports, products, deliveries, agents, metrics)
kept in its repo, reviewed like code, and the same on every install apart from what differs per install. Production then
runs a tag of the repo, not whatever someone last typed on the Edit page.

### Set up

In the project's repo (the one in `pipelines.repo`), a folder `storscale/`:

```text
storscale/
  project.yaml              # the whole project: apiVersion, kind, metadata (its name) and spec
  semantic.yaml             # the semantic model: metrics, terms and tests (optional)
  targets/development.yaml  # what differs on the install whose target is development (optional)
  targets/production.yaml   # ... and on production
```

**`project.yaml` is the whole project**, as the Edit page's JSON box has it, in YAML or JSON: `apiVersion`
(`platform.storscale.io/v1alpha1`), `kind` (`Project`), `metadata` with its `name`, and `spec`. Applying it replaces the
project's spec, so whatever it leaves out, the project loses: its `pipelines` (and with them the repo itself), its imports,
its tags. Start from the project's spec as it is (the Edit page's JSON box) and keep it whole.

```yaml
# storscale/project.yaml
apiVersion: platform.storscale.io/v1alpha1
kind: Project
metadata:
  name: retail
spec:
  description: Harbor Outfitters' retail team.
  members:
    - {group: engineers, role: editor}
    - {user: alice, role: editor}
  tables:
    freshness:
      - {table: uploads_orders, within: 24h}
  pipelines:
    serviceAccount: airflow-pipelines
    repo: https://gitlab.example.com/harbor/retail-pipelines.git
    path: dags
    ref: main
    credentials: {secretRef: retail-git}
    reviews: {gitlab: "https://gitlab.example.com", dbt: dags/dbt}
```

**A target's file** is laid over `project.yaml` for the install whose target it is: maps are merged, anything else (a list,
a value) is replaced whole. So a target that changes one member replaces the whole `members` list.

```yaml
# storscale/targets/production.yaml
spec:
  description: Harbor Outfitters' retail team (production).
  pipelines:
    ref: production
```

The install's target is `install.target` in the chart (`production`), `INSTALL_TARGET` in Compose (`development`). The
Releases tab names it: "and `targets/production.yaml` for this install (production)".

**`semantic.yaml`** is the project's semantic model as the Metrics tab keeps it ([Metrics and business terms](06-metrics-and-terms.md)).
Without one, a release leaves the model as it is.

---

## Planning and applying a release

**What it's for.** A change to the project reaches it on purpose: someone plans a tag of the repo and sees exactly what it
would change; someone else applies it.

**Who can do it.**

| What | Who |
|---|---|
| See the plans and the release | The project's members |
| Plan a ref | `release` (editors) |
| Apply a plan | An administrator who isn't the one who planned it |

**What it needs.** The project's repo (`pipelines.repo`), the platform's files (its bucket) and Airflow.

### Set up

1. Tag the repo (`git tag v1.0 && git push origin v1.0`).
2. On the project's **Releases** tab, type the ref in **A release to plan** (`v1.0`, a branch such as `main`, or a commit)
   and press **Plan**. The plan shows "checking out…", then **ready** with what would change, or **failed** with why.
3. An administrator other than the planner presses **Apply** on the ready plan. The card at the top then says "Released:
   v1.0 (3f1c0e9a2b7d), planned by alice, applied by bob" and when.

### How it behaves

1. **Plan.** platformd records the plan (`fetching`) and starts a run of the platform's DAG `storscale_plan` in the
   project's Airflow. The run checks the repo out at the ref with the project's git connection, and sends back the commit
   and the folder's files (`project.yaml`, `semantic.yaml`, `targets/*.yaml`, each up to 1 MiB), as the project's pipelines.
   The audit log records `release.planned`.
2. platformd composes the spec (`project.yaml`, with `targets/<this install's target>.yaml` laid over it), checks it as any
   spec is checked, checks `semantic.yaml` with the semantic layer, and lists what would change against the project as it is:

   | A change | Reads |
   |---|---|
   | A member added or removed | `member added: carol as reader`, `member removed: group analysts as reader` |
   | A part of the spec changed (two levels deep) | `changed: description`, `changed: tables.readers`, `changed: pipelines.ref` |
   | A delivery whose sending changes | `delivery acme-weekly: what it sends changes, so it needs approving again` |
   | A metric | `metric added: region_rows`, `metric changed: revenue`, `metric removed: region_count` |
   | The model changed, its metrics not | `semantic model changed (its metrics as they were)` |

   Or "No changes."
3. **Apply.** The plan applies only if the project is still as it was when planned (its spec and semantic model). The
   composed spec is saved as if edited by hand (checked again), keeping what only the platform sets: deliveries' approvals
   (a delivery whose sending changed needs approving again, [Deliveries](12-deliveries.md)), and each product's consumers
   and certification ([Data products](11-data-products.md)). `semantic.yaml`, when there is one, becomes the project's model.
   The operator applies the spec within a minute, as any change.
4. The release is recorded: the plan becomes **applied**, with who applied it and when, and the audit log records
   `release.applied` with the ref, its commit, the target, who planned it and the changes.

The Releases tab lists the 30 newest plans; each keeps its ref, commit, target, who planned it and when, its state, and its
changes.

### Limits

- Planning without `release`: "the project's editors plan its releases" (403). Without a repo: "the project has no repo
  (pipelines.repo)". A ref that can't be one: "a ref of the repo: a tag (v1.3), a branch, or a commit".
- A ref the repo hasn't: the plan **fails**, saying so ("checking out v9: …").
- `project.yaml` missing: "no storscale/project.yaml at v1.0". Naming another project: "project.yaml names project "sales",
  not "retail"". A field the spec doesn't have: "not a project: json: unknown field "readers"". Any of the spec's own
  checks fails the plan with its words.
- A plan whose run never answers fails after 15 minutes: "no answer from its run (storscale_plan in the project's Airflow)".
- Applying your own plan: "someone other than who planned it applies it" (403, recorded as a refusal). Not an administrator:
  refused. A plan not ready: "the plan is failed" (409).
- The project changed since the plan (someone saved the Edit page, or another release was applied): "the project has
  changed since it was planned: plan it again" (409). Plan again from the repo, so the repo stays the truth.

See [Lab 20](../guide.md#lab-20-released-on-purpose-with-a-git-repo).

---

## Rolling back

**What it's for.** Going back to the project as an earlier release had it, when a release turns out wrong.

**How.** Plan the earlier ref (`v1.0`) and have an administrator apply it, as any release. Its spec and metrics come back;
the plan lists what that undoes ("member removed: carol as reader", "metric removed: region_rows").

**What it doesn't do.** The tables' data written since stays, with its history: a rollback is of the project's definition,
not of its data. To read the data as it was before the bad release, use [As of](#reading-the-tables-as-they-were-as-of) at
the earlier release's time, or a tag made before it. The DAGs and dbt models are what the branch `pipelines.ref` names holds, not part of
the release: revert them in git.

---

## The API

| Method and path | What | Who |
|---|---|---|
| `GET /api/projects/{name}/history?table=<table>` | A table's changes, newest first: `{table, changes: [{hash, time, message, author, keys, stats, tags, who, kept, query}], keepHistory}` | Members |
| `GET /api/projects/{name}/asof?at=<moment>` | The commit a moment stands for: `{commit, time, label}` | Members |
| `GET /api/projects/{name}/tags` | The tags: `{tags: [{name, commit, keepUntil, note, by, nessie, inPlace}], keepHistory}` | Members |
| `POST /api/projects/{name}/tags` | Make one: `{name, at, keep, note}` | `release` |
| `DELETE /api/projects/{name}/tags/{tag}` | Remove one | `release` |
| `GET /api/projects/{name}/maintenance` | The tables as maintenance keeps them: `{enabled, at, state, paused, keepHistory, tables}` | Members |
| `POST /api/projects/{name}/maintenance` | Ask for the project's maintenance now: `{state}` (202) | `run-pipelines` |
| `GET /api/projects/{name}/plans` | The plans (30 newest), the release, the install's `target`, `repo`, `canPlan`, `canApply` | Members |
| `POST /api/projects/{name}/plans` | Plan a ref: `{"ref": "v1.0"}` | `release` |
| `POST /api/projects/{name}/plans/{plan}/apply` | Apply a ready plan | An administrator other than the planner |
| `POST /api/projects/{name}/plans/{plan}/files` | The repo's `storscale/` folder at the plan's ref (the DAG `storscale_plan` sends it) | The project's pipelines |

With a session, a change needs the header `X-Platform-Request: 1`; a Keycloak token (`Authorization: Bearer …`) works too.

---

## Settings

| Setting | Chart value (default) | Compose | What it changes |
|---|---|---|---|
| When the tables are kept each night | `maintenance.at` (`"02:00"`, UTC) | `MAINTENANCE_AT` on platformd (default `02:00`) | Compaction, retention, and tags' expiry |
| How much history is kept | `maintenance.history.keep` (`P7D`) | `NESSIE_GC_KEEP` in `stack/.env` (`PT2M` as shipped, for the tests: set `P7D` or more for real use) | The window in which any moment can be read; an ISO 8601 duration |
| When Nessie's GC runs | `maintenance.history.schedule` (`"30 3 * * *"`) | daily at 03:30 UTC, or every `NESSIE_GC_EVERY` seconds (`180` as shipped; leave it empty for real use) | When old history is deleted |
| The maintenance account | `MAINTENANCE_TRINO_PASSWORD` in the Secret `storscale-env` | `MAINTENANCE_TRINO_PASSWORD` in `stack/.env` | Without it, no maintenance |
| This install's target | `install.target` (`production`) | `INSTALL_TARGET: development` on platformd | Which `storscale/targets/<target>.yaml` a plan lays over `project.yaml` |
| How long deliveries' reads are kept | `deliveries.keepFor` (`7y`) | `DELIVERY_KEEP_FOR` on platformd (default `7y`) | Their tags' keep ([Deliveries](12-deliveries.md)) |

---

## Limits and gotchas

- **A history window shorter than you think.** The shipped Compose `.env` keeps two minutes of history, collected every
  three minutes, so the test suites can watch history go. On an install for real use, set `NESSIE_GC_KEEP=P7D` (or more)
  and leave `NESSIE_GC_EVERY` empty. Anything that must outlive the window needs a tag.
- **The window is install-wide.** It isn't per project or per table; a project keeps longer only by tagging.
- **A release replaces the whole spec.** `project.yaml` must hold everything the project has, `pipelines` included. What the
  platform sets is kept: the Tags tab's tags (a tag `project.yaml` names too stays as the platform made it), agents'
  registrations and approvals (a new agent registered by who planned it), data products' consumers and certification, and
  deliveries' approvals.
- **Lists in a target replace, maps merge.** A target's `members` is the whole list.
- **A hand edit invalidates a plan.** Edit the repo, not the Edit page, once a project is released from code.
- **Rollback is of the definition, not of the data.**
- **Reading as it was is under today's rules.** Masked stays masked and filtered stays filtered, whatever the rules were then.

See [Troubleshooting](18-troubleshooting.md), [Pipelines and the project's repo](09-pipelines-and-git.md) for the repo
itself, and [Operating the platform](17-operating.md) for backups, which are a different thing from history.
