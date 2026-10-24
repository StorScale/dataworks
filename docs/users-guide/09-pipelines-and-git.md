# 9. Pipelines and the project's repo

A project's pipelines are Airflow DAGs in a git repo of its own, run as the project's pipelines service account. The repo
also holds its dbt models, the flows built on the canvas, its scheduled notebooks, and the project itself as code. This
chapter is for the engineers who write pipelines and dbt models, the analysts who schedule notebooks, and the administrators
who connect a project's repo and keep the company's shared libraries.

In this chapter:

- [Connecting the project's repo](#connecting-the-projects-repo)
- [The project's Airflow](#the-projects-airflow)
- [DAGs in the repo](#dags-in-the-repo)
- [Running pipelines and reading run logs](#running-pipelines-and-reading-run-logs)
- [dbt in a pipeline](#dbt-in-a-pipeline)
- [Merge-request reviews on data branches](#merge-request-reviews-on-data-branches)
- [Scheduled notebooks](#scheduled-notebooks)
- [The company's shared libraries](#the-companys-shared-libraries)
- [The pipelines API](#the-pipelines-api)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

---

## Connecting the project's repo

**What it's for.** Pipelines written as code, reviewed and versioned in git, and run by the platform: push a DAG to the
repo and it's one of the project's pipelines within a minute, running as the project's service account, never as a person.
The same repo is where the canvas saves flows, where notebooks are scheduled, where merge requests are reviewed on their
own copy of the data, and where the project is released from.

**Who can do it.**

| What | Who |
|---|---|
| Give the project the repo's token (a connection) | The permission `connections` (editors) |
| Name the repo in the project's spec (the **Edit** page) | Administrators |
| Start runs | `run-pipelines` (editors) |
| See the Pipelines tab, its runs and their logs | The project's members |

### Set up

**1. A token in GitLab.** In the repo, **Settings → Access tokens**: a project access token with the **`api`** scope and the
**Developer** role. The platform reads the repo with it, commits flows and scheduled notebooks on branches of their own,
opens merge requests, and reports reviews on them; the project's Airflow clones with it. A token with only
`read_repository`, or a Guest's or Reporter's role, is refused by GitLab's API (HTTP 403). (A project made from a template
gets such a token made for it, [Projects](02-projects.md).)

**2. The connection.** On the project's **Connections** tab, add a connection of kind **A git token**:

| Field | Key | What |
|---|---|---|
| User (the token's name) | `username` | The token's name in GitLab |
| Token (GitLab: the api scope, the Developer role) | `password` | The token. Required |
| GitLab webhook's token | `webhookToken` | Optional: the secret a GitLab webhook sends ([below](#merge-request-reviews-on-data-branches)) |

Its name starts with the project's and a dash: `retail-git`. For a repo over SSH, the kind is **An SSH key**
(`sshPrivateKey`, and `sshPrivateKeyPassphrase` if it has one); see the limits below. Through the API:

```json
{"name": "retail-git", "kind": "git", "values": {"username": "storscale-retail", "password": "glpat-xxxxxxxxxxxxxxxxxxxx"}}
```

sent as `POST /api/projects/retail/connections` ([Getting data in](03-data-in.md) has the Connections tab in full).

**3. The spec.** As an administrator, open the project's **Edit** page and tick **Pipelines**:

| Field on the page | Spec field | What |
|---|---|---|
| Keycloak client whose service account runs them | `pipelines.serviceAccount` | Required. The shared Airflow's is `airflow-pipelines`. A project with an Airflow of its own (on Kubernetes, with a repo) runs as a service account the operator makes for it, `project-<name>-pipelines`, whatever this says |
| Repo (https or ssh address, no credentials in it) | `pipelines.repo` | `https://gitlab.example.com/harbor/retail-pipelines.git`, or an ssh address (`ssh://…` or `git@host:group/repo.git`) |
| Folder of the DAGs | `pipelines.path` | The folder in the repo Airflow reads (`dags`); default the whole repo |
| Branch (or tag) | `pipelines.ref` | **The branch the pipelines follow. Set it.** Default: the install's own (`main`), see below |
| Connection with its token or key | `pipelines.credentials.secretRef` | The connection of step 2 |
| GitLab address (the server's) | `pipelines.reviews.gitlab` | `https://gitlab.example.com`: the server's address, not the repo's |
| Project path (empty: the repo's own) | `pipelines.reviews.project` | The repo's path in GitLab, as `group/repo`; leave it empty |
| dbt folder | `pipelines.reviews.dbt` | The dbt project's folder in the repo: `dags/dbt` when the DAGs are in `dags` |

`pipelines.memory` (the project's Airflow's memory, `2Gi` to `64Gi`, as `3Gi` or `2560Mi`) is set in the JSON box. The
same in the spec:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "description": "Harbor Outfitters' retail team.",
    "members": [
      {"group": "engineers", "role": "editor"},
      {"user": "alice", "role": "editor"}
    ],
    "tables": {},
    "pipelines": {
      "serviceAccount": "airflow-pipelines",
      "repo": "https://gitlab.example.com/harbor/retail-pipelines.git",
      "path": "dags",
      "ref": "main",
      "credentials": {"secretRef": "retail-git"},
      "memory": "3Gi",
      "reviews": {"gitlab": "https://gitlab.example.com", "dbt": "dags/dbt"}
    }
  }
}
```

### How it behaves

- **Within a minute** the operator gives Airflow the repo: an Airflow connection `git_<project>` (dashes as underscores)
  with the token, and a DAG bundle `project-<project>` that follows `pipelines.ref` in `pipelines.path`. On Kubernetes the
  project gets an Airflow of its own ([below](#the-projects-airflow)).
- Airflow fetches the branch every 60 seconds. Each pipeline and each run shows the commit it came from.
- The platform's own uses of the repo (the canvas, scheduling a notebook) talk to GitLab's API at `pipelines.reviews.gitlab`
  when reviews are set, else at the repo address's scheme and host, with the connection's `password`.
- Removing `pipelines.repo` removes the Airflow connection and the project's bundle, and with them its DAGs.

**Which branch.** Three things follow a branch of the repo, and they agree only when `pipelines.ref` is set:

| What | Follows |
|---|---|
| Airflow (the DAGs that run) | `pipelines.ref`, else the install's `pipelines.defaultRef` (`main`) |
| The canvas and scheduled notebooks (where merge requests go) | `pipelines.ref`, else the repo's default branch in GitLab |
| Merge-request reviews (which merge requests are reviewed) | `pipelines.ref`, else `main` |

A repo whose default branch isn't `main` (an old `master`) and no `ref` set: flows and notebooks are merged into `master`,
while Airflow looks for a `main` the repo doesn't have. Name the branch.

See [Lab 10 in the lab guide](../guide.md#lab-10-the-project-as-code-with-a-git-repo).

---

## The project's Airflow

**What it's for.** A project's DAG code is code: it shouldn't be able to read another project's connections or the
platform's secrets. On Kubernetes, each project with a repo gets an Airflow of its own.

**How it behaves.**

| | On Kubernetes (the chart) | In Compose |
|---|---|---|
| Where a project's DAGs run | Its own Airflow, `project-<name>-airflow`: one pod with the API server, the scheduler (LocalExecutor, four tasks at a time) and the DAG processor, its own Postgres database (`airflow_<name>`), its own keys | The shared Airflow |
| Its DAGs | Its repo's bundle, and the platform's own DAGs (merge-request reviews, plans, deliveries) | Its repo's bundle, in the shared Airflow beside the platform's and other projects' |
| Runs as | `project-<name>-pipelines`, a Keycloak service account that's a member of this project alone | `pipelines.serviceAccount` (`airflow-pipelines`) |
| Its UI | **Open in Airflow** on the Pipelines tab opens `/airflow/<name>/` in a tab of its own | **Open in Airflow** opens the shared Airflow (**Tools → Pipelines**) |
| Memory | `pipelines.memory`, else the chart's `pipelines.memory` (`3Gi`) | the shared Airflow's |

A project's imports ([Getting data in](03-data-in.md)) run in the platform's Airflow even when the project has its own, and
are listed on its Pipelines tab beside its own pipelines.

---

## DAGs in the repo

**What it's for.** Any Airflow DAG the project needs, written in Python, reviewed in git.

### Set up

Push a DAG file into `pipelines.path`. Airflow reads only files that mention both "airflow" and "dag" (its safe mode), so
a file of helpers without them is skipped. A minimal one:

```python
# dags/orders_daily.py: an Airflow DAG
import pendulum
from airflow.sdk import dag, task


@dag(dag_id="orders_daily", schedule="0 5 * * *", start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False)
def orders_daily():
    @task
    def hello():
        print("retail's pipelines, as the project's service account")
    hello()


orders_daily()
```

### How it behaves

- The project's pipelines are the DAGs of its bundle (`project-<name>`), and those of the platform's own DAGs tagged
  `project:<name>`. A tag `project:<name>` on a DAG from another project's repo counts for nothing.
- Its tasks run as the project's pipelines: in Trino, Buckets and the catalog they may do what the project's pipelines
  may ([Roles](16-roles.md)).
- **A file that doesn't load** is shown on the Pipelines tab: "A file in the repo didn't load", with the file and the end of
  its error. The pipeline stays at its last good version.
- **Two repos, one DAG ID.** Airflow keeps one DAG per ID. The Pipelines tab warns: "Another repo has a DAG with this ID
  too (marketing). Airflow keeps only one at a time, so this one may show the other's code. Give one of them a new
  `dag_id`."

---

## Running pipelines and reading run logs

### Set up

Open the project's **Pipelines** tab. On the left, the project's pipelines, each with its last run's state. For the one
selected:

- where it's from ("From the repo, at `3f1c0e9`", or "The platform's own, in Airflow's DAGs folder"), paused or not;
- **Recent runs**, up to twelve, as coloured marks;
- **Latest run**: its tasks on one time axis, its state, how long it took, who started it;
- a table of runs: state, commit, started, took, and **By** (who started it, or "schedule");
- for a run that ended, **log** (or **why it failed**): what each task logged, its last try, in the words of what ran
  (dbt's, dlt's, the notebook's);
- for a scheduled notebook's run, **notebook**: the executed notebook ([below](#scheduled-notebooks)).

**Run now** (with `run-pipelines`) starts a run. "Started run `manual__…`. It runs as the project's pipelines service
account." whoever pressed it; the run records who asked (`requested_by`), and the audit log records `pipeline.started`.

The tab polls Airflow every five seconds.

### Limits

- Without `run-pipelines`: "The project's editors can start runs."; through the API, "only the project's editors run its
  pipelines" (HTTP 403).
- A pipeline that isn't the project's can't be started from it: "no pipeline "x" in project retail".
- A pipeline whose latest run failed is one of the project's alerts ("orders_daily's latest run failed"), sent where the
  project's `notify` says ([Data quality](05-quality.md)).
- Pausing, clearing and backfilling are Airflow's: **Open in Airflow**.

---

## dbt in a pipeline

**What it's for.** SQL transformations kept as dbt models in the project's repo, with their tests, built in Trino on the
project's Iceberg tables as the project's pipelines, their models, columns, tests and lineage shown in the catalog.

### Set up

Put a dbt project in the repo beside the DAGs, `dags/dbt/` (the canvas's flows go in the same one, under
`models/flows/`). Its `dbt_project.yml` names the profile `storscale` and holds no settings or secrets:

```yaml
# dags/dbt/dbt_project.yml
name: retail
version: '1.0'
config-version: 2
profile: storscale
model-paths: [models]
```

Then a DAG that builds it with the platform's helper:

```python
# dags/retail_models.py: an Airflow DAG
import pendulum
from airflow.sdk import dag

from storscale import dbt


@dag(dag_id="retail_models", schedule="@daily", start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False)
def retail_models():
    dbt.build(project="retail", path="dbt")   # the dbt project beside this DAG file


retail_models()
```

`storscale.dbt.build(project, path="dbt", schema=None, catalog="iceberg", task_id="dbt_build", select=None)` is one task:

| Argument | What |
|---|---|
| `project` | The project's name |
| `path` | The dbt project's folder, relative to the DAG file (default `dbt`) |
| `schema` | The project's namespace (default its name, dashes as underscores) |
| `catalog` | The Trino catalog (default `iceberg`) |
| `task_id` | The task's ID (default `dbt_build`) |
| `select` | dbt's selection (`flows.campaign_results`, `tag:daily`); default everything |

### How it behaves

1. dbt runs in a virtualenv of its own (`/opt/dbt`: dbt-core and dbt-trino), signed in to Trino with a Keycloak token of
   the project's service account (the Airflow connection `keycloak_pipelines`, which the platform keeps): Ranger's rules
   are the pipelines', and its audit names the account.
2. The profile is written for the run: Trino's address and certificate, the catalog, the project's namespace, four threads.
3. With a `packages.yml`, the project is copied to a folder of the run's and `dbt deps` installs its packages there
   ([below](#dbt-packages-and-the-company-libraries)).
4. `dbt build` runs the models and their tests; then `dbt docs generate`, whatever came of the build.
5. The run's artifacts (`manifest.json`, `catalog.json`, `run_results.json`) go to the platform, as the same account
   (`POST /api/projects/{name}/dbt/artifacts`, the project's pipelines only; recorded as `dbt.artifacts`). The catalog reads
   them: the models, their columns, their lineage column by column, and their tests' results. The last manifest is also
   what merge-request reviews compare a change with.
6. A failed build fails the task: "dbt build failed (exit 1): its log above says which model or test".

### dbt packages and the company libraries

A `packages.yml` beside `dbt_project.yml` is installed with `dbt deps` at each run (the repo's checkout is read-only, so
the project is copied first). A package that names one of the company's shared libraries by its repo's address is replaced
by the library's files at the ref the administrators gave it, fetched through the platform, so the project holds no token
for it:

```yaml
# dags/dbt/packages.yml
packages:
  - git: "https://gitlab.example.com/data/libraries/company-dbt.git"   # a shared library: fetched through the platform
  - package: dbt-labs/dbt_utils                                        # any other package: as dbt deps gets it
    version: 1.3.0
```

- The address is matched without credentials, `.git` or a trailing slash, in any case. Administration → Templates shows
  each library's address in the column **In packages.yml**.
- A library's `revision` is ignored, saying so in the log ("the library's ref is the administrators' (revision v2 ignored)");
  a `subdirectory` is kept.
- The run's log names the libraries used: "storscale.dbt: the company's libraries: company-dbt".
- A failed `dbt deps`: "dbt deps failed (exit 2): its log above says which package".

---

## Merge-request reviews on data branches

**What it's for.** A change to the project's dbt models is built and tested on its own copy of the data before it merges,
and the merge request says what it would do: which models built, their rows against production's, which rows differ,
which metrics move, which tests fail, and whether a data product's contract would break. Production is untouched.

**Who can do it.** An administrator turns it on (`pipelines.reviews`). Anyone who can open a merge request in the repo gets
it reviewed.

### Set up

`pipelines.reviews` with `gitlab` (the GitLab server's address) and `dbt` (the dbt project's folder in the repo), on an
`https://` repo with a token (`credentials`). Optionally, a GitLab webhook so a review starts at once: in the repo,
**Settings → Webhooks**, the address `https://<platform>/api/webhooks/gitlab/<project>`, the secret token the same as the
connection's `webhookToken`, and the events **Merge request events** (and **Push events**). Without a webhook the operator
finds new merge requests and commits at its next pass, within a minute or two.

### How it behaves

1. Each pass (every minute), the operator lists the repo's open merge requests into the branch the pipelines follow
   (`pipelines.ref`, else `main`).
2. For each, a **data branch**: a Nessie branch `<project>-mr-<iid>` (`retail-mr-7`), made from the catalog's main as it is
   when the merge request is first seen, and a Trino catalog on it, `<project>_mr_<iid>` (`retail_mr_7`).
3. At each new commit of the merge request, it starts a run of the platform's DAG `storscale_review` in the project's
   Airflow, and sets the commit's status `storscale/dbt` to pending.
4. The run checks the repo out at that commit, fetches production's last manifest, and runs `dbt build --select
   state:modified+` (the models the change touches and those downstream; everything when production has no manifest yet),
   with the project's packages, in the branch's catalog, as the project's pipelines.
5. It tells the merge request, in one note it updates at each commit ("StorScale: dbt on the data branch"):
   - each model built, with its rows on the branch and in production, and a sample of the rows that differ;
   - the project's metrics whose values differ on the branch from production's (up to twenty metrics);
   - each failed model or test, with its message;
   - with `storscale/semantic.yaml` in the commit, the semantic model's tests run on the branch, those that fail and those
     whose answers changed; their own status `storscale/semantic`;
   - each data product whose contracted columns the branch's table would lose, and who reads it.
6. The status `storscale/dbt` ends `success` ("3 models built, their tests passing") or `failed` (naming what failed). The
   review's own run is in the project's Airflow (**Open in Airflow**), not on the Pipelines tab.
7. When the merge request is merged or closed, its catalog and its Nessie branch are removed. Production's own runs build
   what was merged.

The webhook route answers only for a project with reviews, and only with the right token: "the webhook's token isn't the
project's (webhookToken)" (HTTP 401). Other GitLab events are ignored.

### Limits

- GitLab only: the reviews use GitLab's merge requests, statuses and notes.
- The reviews' settings are refused when they can't work: "reviews: gitlab "gitlab.example.com": GitLab's address,
  https://...", "reviews: GitLab's API needs the repo's credentials (a token, for an https repo)", "reviews: dbt "": the dbt
  project's folder in the repo", "reviews: project "x/y" isn't the repo's path, "harbor/retail-pipelines": leave it empty for
  the repo's own".
- A data branch holds the data as it was when the merge request was first seen; a long-open merge request reviews against
  an older copy.
- Only dbt models are built on the branch, not the project's other DAGs.

See [Lab 10](../guide.md#lab-10-the-project-as-code-with-a-git-repo), step 2.

---

## Scheduled notebooks

**What it's for.** An analyst's notebook runs on a schedule as a pipeline, reviewed like any other change: no notebook
running from a laptop, no copy pasted into a DAG.

**Who can do it.** Someone with `build` in a project that has an https repo with a token.

### Set up

1. In JupyterLab (**Tools → Notebooks**), press **Schedule** on the notebook's toolbar, or **Schedule in a project…** on a
   notebook's menu in the file browser.
2. In **Schedule this notebook**, pick the **Project** (those you may schedule in) and **When (cron, UTC)**: `0 5 * * *` is
   every day at 05:00; `0 6 * * 1` is Mondays at 06:00. Press **Schedule**.
3. "Scheduled, for review": the merge request's link. Merge it.

### How it behaves

- The notebook is sent with its outputs cleared (what it computed may be data no repo should hold).
- The platform commits two files on the branch `storscale/schedule-<name>`, from the branch the pipelines follow: the
  notebook, `<path>/notebooks/<name>.ipynb`, and its DAG, `<path>/notebook_<name>.py`, five lines:

  ```python
  from storscale import notebook

  notebook.schedule(project="retail", name="call_list", schedule="0 6 * * 1", owner="alice")
  ```

  `<name>` is the file's name in lower case, other characters as underscores (`Call List.ipynb` → `call_list`).
- It opens a merge request "Schedule call_list.ipynb", or updates the open one: scheduling again updates the same branch.
  The audit log records `notebook.scheduled`.
- Merged, the DAG `notebook_<name>` runs the notebook with papermill, in a virtualenv with the notebook image's packages,
  as the project's pipelines: `buckets_lake` signs in as the pipelines' service account.
- Each run's executed notebook (and its HTML) is kept by the platform, linked from the run on the Pipelines tab
  (**notebook**), and recorded as `notebook.ran`. A failed run says which cell failed and why, and is one of the project's
  alerts.

### Limits

- No project to pick: "You aren't an editor of a project with a repo, where a notebook can be scheduled."
- The schedule is cron's five fields, UTC: "a schedule as cron's five fields (UTC): 0 5 * * *".
- The notebook runs with the image's packages; a package of the project's own isn't installed.

See [Lab 11](../guide.md#lab-11-customers-worth-a-call-a-notebook-on-a-schedule), and [Notebooks and models](10-notebooks-and-models.md).

---

## The company's shared libraries

**What it's for.** Code kept once and used by every project without copying it: dbt macros and generic tests, Python
modules. The administrators name a library's repo in the install's GitLab and a ref; projects use it by name, and the
platform reads it for whoever asks, so no project holds a token for it. A new ref reaches every project at its next run.

**Who can do it.** Administrators add, change and remove libraries. Anyone the realm signed in (a person, a project's
pipelines, an agent) may read them.

**What it needs.** The install's GitLab (`templates.gitlab` in the chart; [Settings](#settings)). Without it,
Administration → Templates says "Libraries need the install's GitLab (templates.gitlab)."

### Set up

1. **Administration → Templates → Shared libraries → Add a library**:

   | Field | JSON | What |
   |---|---|---|
   | Name | `name` (from the address) | Lower-case letters, digits and dashes: `company-dbt` |
   | What it's for | `description` | Required |
   | Its repo in GitLab | `repo` | Its path, as `group/repo`: `data/libraries/company-dbt` |
   | Ref | `ref` | A tag (best: it doesn't move), a branch or a commit: `v1.0.0` |

   The platform checks the repo has that ref, with the install's token. **Change** moves a library to another ref.
2. Use it:
   - **In a dbt project**: name it in `packages.yml` by its address, `- git: "https://gitlab.example.com/data/libraries/company-dbt.git"`
     ([above](#dbt-packages-and-the-company-libraries)).
   - **In a pipeline**: `storscale.library.use(name, path="")` puts the library's files (its folder `path` in the repo,
     default its top) on `sys.path`:

     ```python
     from airflow.sdk import task
     from storscale import library


     @task
     def score():
         library.use("company-utils")          # its repo's files on sys.path, at its ref
         from company_utils import clean
         ...
     ```

     `library.libraries()` lists them; `library.fetch(name)` returns the folder.
   - **In a notebook**: `buckets_lake.library("company-utils")`, then import its modules.

Through the API (administrators): `PUT /api/libraries/company-dbt` with

```json
{"description": "The company's dbt macros and checks", "repo": "data/libraries/company-dbt", "ref": "v1.0.0"}
```

### How it behaves

- Each use asks the platform which commit the library's ref names now, and fetches the files at that commit as a tar
  archive (the install's token reads GitLab). Each commit is fetched once per Airflow worker or notebook server (a cache
  under `/tmp`).
- The audit log records `library.saved` (with the repo, the ref and its commit, and what it was) and `library.removed`.
- A removed library can't be fetched any more: "no library x (Administration → Templates lists them)" (HTTP 404).

### Limits

- Libraries live in the install's GitLab, read with the install's token: the token must be able to read the library's repo.
- `repo: the library's path in GitLab, as group/repo`; `ref: a tag, a branch or a commit (a tag is best: it doesn't move)`;
  "the library's repo data/x at v9: …" when GitLab hasn't it.
- A library at a branch changes under every project when the branch moves: use tags.

---

## The pipelines API

| Method and path | What | Who |
|---|---|---|
| `GET /api/projects/{name}/pipelines` | The pipelines, their recent runs and the newest run's tasks; `canRun`; the repo (`url`, `path`, `ref`) and the files Airflow couldn't load; `airflowPath` for a project's own Airflow | Members |
| `POST /api/projects/{name}/pipelines/{pipeline}/runs` | Start a run: `{run}` | `run-pipelines` |
| `GET /api/projects/{name}/pipelines/{pipeline}/runs/{run}/log` | What its tasks logged: `{log}` | Members |
| `POST /api/projects/{name}/dbt/artifacts` | A dbt run's artifacts, for the catalog | The project's pipelines |
| `GET /api/projects/{name}/dbt/artifacts/{artifact}` | The last run's `manifest` (for reviews) | The project's pipelines |
| `POST /api/projects/{name}/notebooks/schedules` | `{path, schedule, notebook}`: schedule a notebook (JupyterLab calls it) | `build` |
| `GET /api/projects/{name}/notebooks/runs/{dag}/{run}` | A run's executed notebook: `?format=html` (default) or `ipynb` | Members |
| `POST /api/webhooks/gitlab/{name}` | A GitLab webhook's merge request or push event | GitLab, with `webhookToken` |
| `GET /api/libraries`, `GET /api/libraries/{library}`, `GET /api/libraries/{library}/archive` | The libraries; one with the commit its ref names; its files (`?commit=`) | Anyone signed in to the realm |
| `PUT /api/libraries/{library}`, `DELETE /api/libraries/{library}` | Add or change, remove a library | Administrators |

With a session, a change needs the header `X-Platform-Request: 1`; a Keycloak token (`Authorization: Bearer …`) works too.

---

## Settings

| Setting | Chart value (default) | Compose | What it changes |
|---|---|---|---|
| The branch a repo is followed on when its project names none | `pipelines.defaultRef` (`main`) | `PIPELINES_DEFAULT_REF` on the operator (default `main`) | Airflow's branch for a project without `pipelines.ref` |
| A project's Airflow's memory | `pipelines.memory` (`3Gi`) | n/a (the shared Airflow) | When the project sets no `pipelines.memory` |
| The git hosts' SSH keys | `pipelines.gitKnownHosts` (`""`; `--set-file pipelines.gitKnownHosts=known_hosts`) | `GIT_KNOWN_HOSTS` | SSH repos checked against them; without, a host's key is taken the first time it's seen |
| A project's own Airflow | always on Kubernetes (`PROJECT_AIRFLOWS=true`) | off: one shared Airflow | Where a project's DAGs run |
| The install's GitLab, for templates and libraries | `templates.gitlab.url`, `.group`, `.tokenSecret` (a Secret with key `token`) | `GITLAB_URL`, `GITLAB_GROUP`, `GITLAB_TOKEN` in `stack/.env` | Shared libraries and project templates; a new token is read at each use |

---

## Limits and gotchas

- **Set `pipelines.ref`** to the branch: see [which branch](#connecting-the-projects-repo).
- **An SSH repo** runs pipelines (with `sshPrivateKey`; a key with a passphrase can't be checked out for reviews or plans),
  but the canvas, scheduled notebooks and reviews need an `https://` repo with a token.
- **No credentials in the address**: "repo: no user or token in the address; name a Secret in credentials". An ssh repo
  without credentials: "an ssh repo needs credentials: a Secret with sshPrivateKey".
- **path, ref, credentials, memory and reviews need a repo**: setting them without `pipelines.repo` is refused.
- **The connection must be the project's**: one named in `secretRef` that isn't among its connections stops the operator:
  "no secret "retail-git" (the repo's credentials) among the project's connections"; one without a token: "secret
  "retail-git" has no password (a token) for an https repo".
- **The token's scope**: `api` and Developer. Merge requests and statuses fail with HTTP 403 otherwise.
- **The Pipelines tab says "every few minutes"**; Airflow in fact fetches the branch every 60 seconds.
- **DAG IDs are global** to an Airflow: in Compose, every project shares one, so give DAGs IDs that name the project.

See [Troubleshooting](18-troubleshooting.md) for the errors by feature, and [History and releases](15-history-and-releases.md)
for releasing the project itself from the same repo.
