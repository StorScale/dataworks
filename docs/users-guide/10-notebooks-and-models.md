# 10. Notebooks and machine learning

This chapter is for analysts and data scientists who work in notebooks, and for the editors and administrators who put their models to use. It covers JupyterLab and VS Code in the platform, the `buckets_lake` module that signs a notebook in as you (Buckets, SQL, MLflow, the company's libraries), each project's own MLflow workspace, registering and approving model versions, model cards, scoring a table on a schedule with drift checks, serving a model as an API, how models appear in the catalog's lineage, scheduled notebooks, and the three notebook templates.

In this chapter:

- [JupyterLab and VS Code](#jupyterlab-and-vs-code)
- [buckets_lake: the notebook signed in as you](#buckets_lake-the-notebook-signed-in-as-you)
- [MLflow, a workspace per project](#mlflow-a-workspace-per-project)
- [Registering and approving model versions](#registering-and-approving-model-versions)
- [Model cards](#model-cards)
- [Scoring on a schedule, and drift](#scoring-on-a-schedule-and-drift)
- [Serving a model as an API](#serving-a-model-as-an-api)
- [Models in the catalog's lineage](#models-in-the-catalogs-lineage)
- [Scheduled notebooks](#scheduled-notebooks)
- [The notebook templates: churn, propensity, forecast](#the-notebook-templates-churn-propensity-forecast)
- [Settings](#settings)
- [Errors you may meet](#errors-you-may-meet)

Lab 11 in the lab guide walks through most of this end to end: [Lab 11: Customers worth a call](../guide.md#lab-11-customers-worth-a-call-a-notebook-on-a-schedule).

---

## JupyterLab and VS Code

**What it's for.** A notebook server of your own, inside the platform, signed in as you: no keys to paste, the same rules as everywhere else, and your files kept between sessions. VS Code runs in the browser on the same files for those who prefer an editor.

**Who can do it.** Anyone in the Keycloak groups `analysts` or `engineers` may sign in to JupyterHub; members of `engineers` are the hub's administrators. Of the example people, alice (analysts) and bob (engineers) can; carol (no groups) can't. These groups are set in `stack/jupyterhub/jupyterhub_config.py` (`allowed_groups`, `admin_groups`), not by a chart value.

### Set up

Nothing to set up per project. To open it:

1. **Tools → Notebooks**, or a project's **Notebooks** tab (shown when the install has notebooks). JupyterLab opens in the platform's frame, at the platform's `/notebooks/`, signed in through Keycloak.
2. Your files are in `work/` (`/home/jovyan/work`). They live on a volume of your own that outlives the server: on Kubernetes a persistent volume claim `notebooks-<you>` (size `notebooks.storage`, default `1Gi`); in Compose a Docker volume `storscale-notebooks-<you>`.
3. For VS Code, open JupyterLab's launcher and pick **VS Code**: code-server at your server's `/vscode/`, on the same `work/` folder, with no sign-in of its own (the hub has signed you in).

### How it behaves

- Each person gets a server of their own: a container (Compose) or a pod (Kubernetes). It starts within two minutes (the hub's start timeout is 120 seconds).
- The hub keeps your Keycloak tokens, encrypted, and refreshes them when they're more than two minutes old, and before each start. `buckets_lake` asks the hub for the current one, so a notebook is always signed in as you.
- A server serves every project you're in: its CPU and memory are yours, not a project's (`notebooks.cpuLimit`, `notebooks.memoryLimit`).
- The image has, beside JupyterLab 4.5: boto3, requests, pandas, pyarrow, scikit-learn, statsmodels, scipy, LightGBM, XGBoost, FLAML, MLflow (the skinny client), skops, geopandas and networkx.
- Only the platform's own pages may frame the hub and the notebook servers.

### Limits and gotchas

- Notebook servers are a person's, so a project's limits ([Projects](02-projects.md)) don't cap them; the install's `notebooks.*` settings do.
- `geopandas` and `networkx` are in the notebook image but not in the environment a scheduled notebook runs in (see [Scheduled notebooks](#scheduled-notebooks)).
- Someone outside `analysts` and `engineers` is refused at the hub's sign-in, whatever projects they're in.

---

## buckets_lake: the notebook signed in as you

**What it's for.** One module, already on the path, that turns your sign-in into everything a notebook needs: Buckets (S3) credentials, SQL through the platform under your rules, the project's MLflow, and the company's shared libraries. No key lives in the notebook.

**Who can do it.** Anyone signed in to JupyterLab. What each call may reach is what you may reach: your Buckets policies follow your Keycloak groups, and SQL runs as you under the project's rules ([Access and protection](04-access-and-protection.md)).

### The calls

| Call | What it gives |
|---|---|
| `buckets_lake.username()` | Your name, as Keycloak gives it (`preferred_username`). |
| `buckets_lake.token()` | Your current Keycloak access token, from the hub. |
| `buckets_lake.session()` | A boto3 session whose credentials renew themselves. |
| `buckets_lake.s3()` | A boto3 S3 client for Buckets, as you. |
| `buckets_lake.sql(project, query)` | A query's answer as a pandas DataFrame, run in Trino through the platform's SQL route, as you. |
| `buckets_lake.tables()` | The tables `sql()` has read in this kernel, as `catalog.schema.table`, sorted. |
| `buckets_lake.mlflow(project)` | The `mlflow` module, pointed at the project's MLflow workspace, signed in as you. |
| `buckets_lake.library(name, path="")` | One of the company's shared libraries put on `sys.path`; returns its folder. |

### s3() and session()

```python
import io, pandas as pd, buckets_lake
s3 = buckets_lake.s3()
orders = pd.read_csv(io.BytesIO(s3.get_object(Bucket="retail", Key="landing/uploads/orders.csv")["Body"].read()))
s3.put_object(Bucket="home", Key=f"{buckets_lake.username()}/notes.txt", Body=b"draft")
```

The token is exchanged with Buckets' STS (`AssumeRoleWithWebIdentity`, role `arn:minio:iam:::role/jupyterhub`) for credentials that last an hour and renew themselves. Your private prefix is `home/<username>/`. A project's bucket is named after the project unless its spec says otherwise (`files.bucket`).

### sql()

```python
orders = buckets_lake.sql("retail", "SELECT region, sum(amount) AS revenue FROM uploads_orders GROUP BY region")
```

- The query uses the project's own table names (`FROM uploads_orders`, not `iceberg.retail.uploads_orders`), and runs as you, under the project's rules: a reader's row filters and masks apply, as on the **SQL** tab.
- Types come back as pandas types: integers as `int64` (`float64` when there are nulls), reals and decimals as `float64`, booleans as `bool` (`boolean` with nulls), dates and timestamps as datetimes. A timestamp with a time zone pandas can't read stays text.
- At most 100,000 rows. A full answer warns: "the answer has 100000 rows, the most the platform's SQL route gives: there may be more". Aggregate or sample in the query.
- A refusal raises with the platform's reason: `PermissionError` when it's who you are (401 or 403), `RuntimeError` otherwise, worded "the platform refused the query on retail (403): ...".
- **What it read is kept for lineage.** After the answer, `sql()` asks Trino for the query's `EXPLAIN (TYPE IO, FORMAT JSON)` and records the tables it read. `buckets_lake.tables()` lists them. On MLflow runs they become the tag `lake.tables` (see [MLflow](#mlflow-a-workspace-per-project)). A query `EXPLAIN` can't describe is simply not recorded; the answer still stands.

### mlflow()

```python
mlflow = buckets_lake.mlflow("retail")
mlflow.set_experiment("churn")
```

Sets `MLFLOW_TRACKING_URI` to the platform's `/mlflow/retail` and `MLFLOW_TRACKING_TOKEN` to your token, renewed every two minutes. It also registers a run-context provider, so each run started from then on is tagged `lake.tables` with what `sql()` read before it; and `sql()` run inside a run updates the tag.

### library()

```python
where = buckets_lake.library("harbor-utils")          # the library at the ref the administrators gave it
from harbor_utils import cleaning
```

The platform reads the library from GitLab for you (`GET /api/libraries/{library}` names the commit; `GET /api/libraries/{library}/archive?commit=...` gives its files), so no GitLab token is needed in the notebook. Each commit is fetched once into a cache under `/tmp/storscale-libraries`. `path` names a folder inside the library to put on `sys.path` instead of its top. Libraries are kept on **Administration → Templates**; see [Pipelines and the project's repo](09-pipelines-and-git.md) for defining them. A failure raises `RuntimeError("library <name>: HTTP <status> ...")`.

### The pipelines' variant

A scheduled notebook, and an agent's notebook, run in Airflow with a `buckets_lake` of the same calls (`s3()`, `session()`, `username()`, `token()`, `sql()`, `tables()`, `mlflow()`, `library()`), so a notebook that ran by hand runs on a schedule unchanged. The differences:

| | In JupyterLab | On a schedule |
|---|---|---|
| Signed in as | you | the project's pipelines' service account, or the agent's client for an agent's run |
| `username()` | your name | `STORSCALE_RUN_AS`: `service-account-<client>` |
| Token | from the hub | client credentials from Keycloak, fetched for each call |
| STS role | `jupyterhub` | `airflow` |
| `agent()`, `ask()` | absent | present: for agents only (see [AI](14-ai.md#agents-with-a-notebook)) |

The project's pipelines read and write all of the project's tables, so a notebook that worked under your reader's rules may see more rows on a schedule; one that worked as an editor may see fewer as an agent, which reads as a reader does.

### Limits and gotchas

- `sql()` posts to the platform's SQL route; it doesn't connect to Trino itself, so the 100,000-row limit holds whatever the client.
- `lake.tables` holds at most 8,000 characters (MLflow's limit for a tag's value); tables past that are left out.
- The `storscale.*` tags are the platform's: name your own tags anything else.

---

## MLflow, a workspace per project

**What it's for.** One MLflow tracking server and model registry for the install, with a workspace per project: experiments, runs, logged models, registered models and artifacts are the project's, and no other project sees them. Each run is stamped with who ran it and the data it read, so a model can always be traced to its data.

**Who can do it.**

| Who | May |
|---|---|
| The project's members (readers included) | Read its experiments, runs and models. |
| Members with the permission `models` (editors) and the project's pipelines | Track runs, log and register models. |
| Administrators, signed in | As an editor. |
| Anyone else | Nothing: "not a member of the project". |

### Set up

Nothing per project: the workspace is made the first time anyone in the project reaches it. From a notebook, use `buckets_lake.mlflow("<project>")`. From anywhere else (a laptop, a CI job), point MLflow's own client at the platform:

```bash
export MLFLOW_TRACKING_URI=https://<platform>/mlflow/retail
export MLFLOW_TRACKING_TOKEN=<a Keycloak access token>
```

The project's **Models** tab shows this address under **From a notebook**.

```python
import buckets_lake
mlflow = buckets_lake.mlflow("retail")      # the project's workspace, as you
mlflow.set_experiment("churn")
with mlflow.start_run():                    # stamped with you and the data's commit
    ...                                     # train; mlflow.log_metric("auc", auc)
    mlflow.sklearn.log_model(model, name="model", registered_model_name="churn")
```

### How it behaves

- Only platformd reaches MLflow, at `/mlflow/<project>/...`. It signs the caller in (the web app's session, or a Keycloak token as `MLFLOW_TRACKING_TOKEN`), checks they're in the project, and names the project's workspace on every request, whatever the caller sent: a request naming another workspace still lands in the project's.
- Only MLflow's tracking, registry and artifact routes go through (`/api/2.0|3.0/mlflow/{experiments,runs,metrics,registered-models,model-versions,logged-models,artifacts,datasets,traces}` and `/mlflow-artifacts/artifacts`). MLflow's own web UI is not served; the **Models** tab is the platform's view.
- **Stamps on every run.** When a run is created, the platform adds `storscale.user` (who ran it) and `storscale.data_commit` (the catalog's Nessie `main` commit at that moment: every table as it was). With `buckets_lake.sql()`, the run also carries `lake.tables`. Together they say exactly which data the model learned from; read it again with **As of** on the SQL tab ([History and releases](15-history-and-releases.md)).
- **Stamps on every version.** When a model version is created, the platform adds `storscale.registered_by`.
- Artifacts are kept in Buckets (the bucket `mlflow`), proxied through MLflow.
- Deleting a project deletes its workspace: first its experiments' artifacts, then the workspace with its experiments, runs and models.

**Audit log:** `mlflow.run` ("a run started") for each run created, and `model.registered` for each registered model or version created, under the person or pipelines that did it; a refused write is `mlflow.write`.

### Limits and gotchas

- You can't set, change or delete a `storscale.*` tag, or the alias `approved`, through MLflow. The platform refuses with 403: "the storscale.* tags are the platform's: a version is approved on the project's Models tab".
- A reader's write (a new run, a registration) is refused: "the project's readers read its experiments and models; its editors and pipelines write them".
- An agent is a reader of its project: it reads the project's experiments and models but doesn't log or register.
- A route other than tracking, registry or artifacts answers 404: "not one of MLflow's routes the platform serves (tracking, the registry, artifacts)".
- An install without MLflow answers 503 "MLflow isn't set up here (no MLFLOW_URL)", and the Models tab says "MLflow isn't set up on this install."

---

## Registering and approving model versions

**What it's for.** A model reaches a pipeline or an API only once someone other than its author has looked at it. Registering makes a numbered version; approving it, by a second editor, points the alias `approved` at it, and that's the version scorings and servings load.

**Who can do it.** Registering: members with `models` (editors) and the project's pipelines. Approving: members with `approve` (editors), never the person who registered that version. Administrators approve as editors, under the same rule.

### Set up

1. **Register.** In MLflow, `registered_model_name` on `log_model` (or `mlflow.register_model(...)`) makes a new version of a registered model in the project's workspace. On the project's **Models** tab, the model's card lists the version with **Registered** (when) and **By** (you).
2. **Approve.** Another editor opens **Models** and presses **Approve** on the version's row. To the person who registered it, the row says "another editor approves it" instead of a button. The model's heading then shows "version N approved".

API:

```bash
curl -s -X POST https://<platform>/api/projects/retail/models/churn/versions/2/approve \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1"
# {"approved": true, "by": "bob", "at": "2026-10-24T09:12:03Z"}
```

`GET /api/projects/{name}/models` lists the registered models (each with `name`, `description`, `approved`, and its `versions` with `version`, `runId`, `source`, `status`, `created`, `registeredBy`, `approvedBy`, `approvedAt`), the experiments, `canApprove`, and `trackingUri`.

### How it behaves

- Approval sets two tags on the version, `storscale.approved_by` and `storscale.approved_at`, and moves the registered model's alias `approved` to it. Only the platform can set these.
- Approving a newer version moves the alias: the older version stays loadable by its number (`models:/churn/1`) but is no longer what pipelines load (`models:/churn@approved`).
- Scorings pick up the new version at their next run; servings within about a minute.
- **Audit log:** `model.approved` with "registered by <name>"; a refused self-approval is recorded as `model.approved` with "approving their own".

### Limits and gotchas

- Approving your own version: 403 "a model version is approved by an editor other than the one who registered it". Have a second editor in every project that registers models.
- A version or model that doesn't exist: 404 "no such model version: ...".
- Approving through MLflow's own API (the alias or the tags) is refused (see above).
- There is no "unapprove": approve another version to move the alias.

---

## Model cards

**What it's for.** A short, human description of what a model is for, what it learned from, how it was judged and where it shouldn't be used, kept with the model and shown in the catalog.

**Who can do it.** Members with `models` (editors). The **Write its card** button shows to editors.

### Set up

1. On **Models**, on the model's card, press **Write its card** (or **Edit its card**).
2. Write it. The placeholder suggests four lines: *Purpose*, *Data*, *Metrics*, *Limits*.
3. **Save the card**.

It's the registered model's description in MLflow, written through the project's MLflow route (`PATCH /mlflow/<project>/api/2.0/mlflow/registered-models/update` with `{"name": ..., "description": ...}`), so `mlflow.MlflowClient().update_registered_model(name, description=...)` from a notebook does the same.

### How it behaves

The card shows under the model's name on **Models**, and is the model's description in the catalog (see [Models in the catalog's lineage](#models-in-the-catalogs-lineage)). A model without one says "No card yet: its purpose, data, metrics and limits."

---

## Scoring on a schedule, and drift

**What it's for.** Putting an approved model to work: every night (or on any schedule) its approved version scores a table's rows into a table of the project's, as the project's pipelines. Each run's inputs are compared with what the model saw on its first run, so a model drifting away from its data says so before its scores go wrong.

**Who can do it.** Scorings are declared in the project's spec: administrators on the **Edit** page, or a release of the project's repo ([History and releases](15-history-and-releases.md)). They run as the project's pipelines. The project's members see them on **Models**.

**What it needs.** The project has `tables` and `pipelines`, and the model has an approved version.

### Set up

Add `scoring` to the spec (**Edit** → the JSON box):

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [
      {"group": "engineers", "role": "editor"},
      {"user": "alice", "role": "editor"}
    ],
    "tables": {},
    "pipelines": {"serviceAccount": "airflow-pipelines"},
    "scoring": [
      {
        "name": "churn-nightly",
        "model": "churn",
        "table": "customer_features",
        "id": "customer_id",
        "columns": ["orders", "revenue", "tenure_days"],
        "output": "customer_scores",
        "schedule": "0 3 * * *",
        "drift": 0.2,
        "maxRows": 100000
      }
    ]
  }
}
```

| Field | Required | What |
|---|---|---|
| `name` | yes | Lowercase letters, digits and dashes, starting with a letter, up to 41 characters. |
| `model` | yes | A registered model of the project's MLflow workspace, by name. Its approved version is used. |
| `table` | yes | The input: one of the project's tables. |
| `id` | yes | The column that names each row; it's carried to the output so scores join back. |
| `columns` | no | The model's inputs, by name. None: every column of `table` but `id`. |
| `output` | yes | The table the scores are written to, made afresh each run. Not the input; no two scorings write the same output. |
| `schedule` | yes | Cron's five fields, UTC. |
| `drift` | no | The population stability index past which a column has drifted. Default `0.2`; 0 to 10. |
| `maxRows` | no | The most rows read. Default 100,000; up to 5,000,000. |

There is no API to add a scoring by itself; it's part of the spec (`PUT /api/projects/{name}`, administrators). Reading them: `GET /api/projects/{name}/scoring`.

### How it behaves

- Within a minute of saving, the **Pipelines** tab lists the scoring's DAG, `scoring_<project>_<name>` with dashes as underscores (`scoring_retail_churn_nightly`), tagged `scoring`. It runs in the project's own Airflow if it has one, else the shared one.
- **Each run**, as the project's pipelines, in a Python environment of its own (MLflow, scikit-learn, LightGBM, XGBoost, FLAML, skops, pandas):
  1. loads `models:/<model>@approved` through the project's MLflow;
  2. reads `SELECT * FROM <table> LIMIT <maxRows>` through Trino;
  3. predicts on `columns`. A score per row: when the model answers several columns (a classifier's probabilities), the last one, the positive class's of two;
  4. writes `output` afresh (`CREATE OR REPLACE TABLE`) with four columns: the `id` column (as `varchar`), `score` (`double`, or `varchar` when the scores aren't numbers), `model_version` and `scored_at` (`timestamp(3)`, UTC);
  5. works out each input column's drift and records the run with the platform.
- **Drift.** A version's first run is its reference: each input column's profile (a number's shares over ten quantile bins of that run; a text column's ten commonest values and "other"), kept by the platform. Every later run of the same version is profiled over the same bins and compared by the population stability index (PSI). A newly approved version gets a new reference from its first run.
- **Alerts.** When the latest run's worst column is past the threshold, the project's alerts say "churn-nightly: its inputs have drifted (revenue, PSI 0.85, past 0.20)", with "version 2 of churn, the run of ..., against the version's first run; retrain, or check the data", linked to **Models**. Alerts go where the project's alerts go ([Data quality](05-quality.md)).
- **The Scoring card** on **Models** lists each scoring: its DAG, model and the version of its last run, input → output, schedule, last run (rows and the scores' mean), and the worst column's PSI, green or red against the threshold.
- **Metrics.** Prometheus scrapes `storscale_scoring_drift{project,scoring,version,column}` (each column's PSI on the latest run) and `storscale_scoring_rows{project,scoring}`, so Grafana can draw them.
- The platform keeps a scoring's 60 most recent runs (in its bucket, `scoring/<project>/<name>/`).

### Settings

None of its own. The scoring runs in the project's Airflow (`pipelines.memory`, default `3Gi`).

### Limits and gotchas

- **Only the first `maxRows` rows are scored**, in whatever order Trino reads them. A table bigger than `maxRows` is scored in part, with no warning: raise `maxRows` or split the table.
- The model must load in the scoring's environment: scikit-learn, LightGBM, XGBoost and FLAML models do; a model needing a library the environment doesn't have (statsmodels, for one) fails to load. The churn and propensity templates restrict FLAML to the kinds a scoring can load for this reason.
- The model is given exactly `columns` as a DataFrame: names and types must match what it was trained on. Log an `input_example` so MLflow records the signature.
- The scoring scores the table as it is: rebuild the features table (a pipeline of its own) before the scoring runs.
- No version approved: the run fails at loading `models:/<model>@approved`.
- Refusals at save time: "scoring: a model scores the project's tables as its pipelines: the project needs tables and pipelines"; "scoring: churn-nightly: the output is a table of its own, not its input"; "scoring: two write customer_scores"; "scoring: churn-nightly: schedule \"...\": five cron fields, as 0 4 * * *"; "drift 12: a PSI, as 0.2 (more is a bigger shift)"; "maxRows ...: up to five million".

---

## Serving a model as an API

**What it's for.** A score on demand: the web shop asks for one customer's churn chance as they sign in, rather than waiting for the night's run. A served model answers at the platform's address, signed in, each call audited, and moves to a newly approved version by itself.

**Who can do it.** Servings are declared in the project's spec (administrators on **Edit**, or a release). Calling one: any member of the project (readers, editors, custom roles) and its agents, signed in with a Keycloak token or the web app's session. Not members are refused.

### Set up

Add `serving` to the spec:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [
      {"group": "engineers", "role": "editor"},
      {"user": "alice", "role": "editor"}
    ],
    "tables": {},
    "serving": [
      {"name": "churn", "model": "churn", "memory": "1Gi", "maxRows": 1000}
    ]
  }
}
```

| Field | Required | What |
|---|---|---|
| `name` | yes | Up to 16 lowercase letters, digits and dashes, starting with a letter. `model-<project>-<name>` must fit in 63 characters. |
| `model` | yes | A registered model of the project's, by name (letters, digits, `_ . -`). Its approved version is served. |
| `memory` | no | Its server's memory on Kubernetes, from `256Mi` to `16Gi`. Default the install's `serving.memory` (`1Gi`). |
| `maxRows` | no | The most rows a call may score. Default 1,000; up to 100,000. |

**Call it:**

```bash
curl -s https://<platform>/api/projects/retail/serving/churn/predict \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"dataframe_records": [{"orders": 1, "revenue": 40.0, "tenure_days": 300}]}'
# {"predictions": [[0.31, 0.69]], "model": "churn", "version": "2"}
```

The body is MLflow's: `{"dataframe_records": [...]}`, `{"dataframe_split": {"columns": [...], "data": [...]}}`, `{"instances": [...]}` or `{"inputs": ...}`. The answer is `{"predictions": [...], "model": ..., "version": ...}`: the version that answered. A model logged with `pyfunc_predict_fn="predict_proba"` answers each class's chance.

`GET /api/projects/{name}/serving` lists the project's servings, each with `maxRows`, `url` (where to post), `version` (served now) or `error` (why none is).

### How it behaves

- **On Kubernetes**, each serving gets a server of its own: a Deployment, Service and Secret named `model-<project>-<name>`, signed in to Keycloak as a client of its own (`serving.<project>.<name>`) whose service account reads the project as its readers do, and nothing else's. With the chart's `networkPolicies.enabled`, a network policy keeps everyone but platformd out. **In Compose**, one shared server serves every project's servings, signed in as the pipelines' account, which the operator makes a reader of each project it serves.
- The server loads `models:/<model>@approved` through the project's MLflow, and **looks again every 60 seconds**: a newly approved version answers within about a minute, at the same address, nobody restarting anything.
- **Models → Serving** shows each serving, the version it's answering with (or why not: "starting", "no version of churn is approved", "its server isn't answering (starting?)"), its rows a call, and the address, with a `curl` example.
- A project's calls run at most eight at a time across its servings.
- **Audit log:** each call is `model.called` with "churn version 2, 1 rows" (who, the version, how many rows; never the rows). Each refusal is `model.call` with the reason: not a member, the project's calls at their limit, or the model server's error.

### Settings

| Setting | Default | What |
|---|---|---|
| `serving.memory` (chart) | `1Gi` | A serving's server's memory when its spec names none. |
| `SERVING_URL` (Compose, platformd) | `http://serving:8080` | Where the model server answers. Empty: models aren't served. |
| `SERVING_REFRESH_SECONDS` (the model server) | `60` | How often it looks for a newly approved version. |

### Limits and gotchas

| Answer | Why | What to do |
|---|---|---|
| 403 "not a member of the project" | The caller isn't one of the project's people or agents. | Add them to the project, or call as someone who is. |
| 429 "the project's models answer 8 calls at a time: try again in a moment" | Eight of the project's calls are already in flight. | Retry after the `Retry-After` second. |
| 413 "a call is up to 32 MB" | The body is too big. | Send fewer rows a call. |
| 413 "1500 rows: a call scores up to 1000" | Over the serving's `maxRows`. | Split the call, or raise `maxRows`. |
| 503 "no version of churn is approved" | Nothing to serve yet. | Approve a version on **Models**. |
| 400 "ValueError: ..." with the model and version | The model refused the rows, most often their columns or types. | Send the columns the model was trained on. |
| 502 "the model's server isn't answering (starting?)" | The server is starting or down. | Wait a minute; on Kubernetes check the pod `model-<project>-<name>`. |
| 503 "models aren't served here (no SERVING_URL)" | The install doesn't serve models. | An administrator sets it up. |

- A person calls only with a role that gives `call-models` (the built-in reader and editor do): "your role in retail (looker) doesn't let you call its models (call-models)" (HTTP 403), the refusal in the audit log. The project's own accounts (its pipelines, its agents) call as members.
- A served model's version moves the moment another is approved: tell its callers, or read `version` in each answer.

---

## Models in the catalog's lineage

**What it's for.** Finding a model where people already look for data: the catalog shows each registered model between the tables it learned from and the tables it scores into, with its card and approval, so "what does this model read, and who approved it?" has an answer.

**Who can do it.** Nothing to do: catalog-sync keeps it. Anyone who can open the catalog (**Tools → Catalog**) sees it.

### How it behaves

- Every round of its metadata ingest (every `METADATA_EVERY` seconds, default 60), catalog-sync asks platformd for every project's registered models and scorings (`GET /internal/catalog/models`, its own service account only) and puts each model in the ML model service `storscale-mlflow`, as `<project>__<model>` (`retail__churn`).
- **Its page:** the card as its description, then the version approved, who registered and who approved it, the training run (who ran it, its parameters, the algorithm as autologging names it) and the catalog's Nessie commit when the run started. A model without a card says "No card yet: it's written on the project's Models tab." The page links back to the project's **Models** tab.
- **Upstream:** the tables the approved version's run read (its `lake.tables`), each at its Iceberg snapshot at the run's data commit, and each scoring's input table.
- **Downstream:** each scoring's output table, the edge saying "Its scores of customer_features, by the approved version: the scoring churn-nightly (0 3 * * *), written afresh each run."
- Edges no longer drawn (a scoring removed, a version trained on other tables) are pruned; models no longer registered are removed.

### Limits and gotchas

- Only a version that's approved appears with its run and tables; a model with no approved version appears with its card alone.
- Training tables come from `lake.tables`, which only `buckets_lake.sql()` records. A model trained on data read another way (`s3()`, a CSV) has no training tables in the lineage; its run's `storscale.data_commit` still names the data.
- Up to 100 of a run's tables are given to the catalog.

---

## Scheduled notebooks

**What it's for.** An analyst's notebook becomes a project's pipeline with one button, as a reviewed change in the project's repo rather than a job on someone's laptop. Each run keeps its executed notebook, outputs and all.

**Who can do it.** Members with the permission `build` (editors) of a project that has a repo the platform can commit to: an `https://` repo and its token (the repo's connection). The pipelines and git side is in [Pipelines and the project's repo](09-pipelines-and-git.md).

### Set up

1. In JupyterLab, open the notebook and press **Schedule** on its toolbar (or right-click it in the file browser: **Schedule in a project…**, also in the command palette).
2. Pick the **Project** (the projects you edit; those without a usable repo are listed greyed out with why) and **When (cron, UTC)**, default `0 5 * * *`.
3. **Schedule**. The dialog "Scheduled, for review" links the merge request.
4. Review and merge it in GitLab. Within a minute of the merge, the **Pipelines** tab lists `notebook_<name>`.

API (what the button calls, as you): `GET /api/notebooks/projects` lists the projects you may schedule in; `POST /api/projects/{name}/notebooks/schedules` with `{"path": "call_list.ipynb", "schedule": "0 6 * * 1", "notebook": {...}}` commits it and answers with `dag`, `branch`, `commit`, `mergeRequest`, `iid` and `new`.

### How it behaves

- The notebook goes with its outputs cleared: what it computed may be data no repo should hold.
- The platform commits two files to the branch `storscale/schedule-<name>`: the notebook as `<dags path>/notebooks/<name>.ipynb`, and a five-line DAG file `<dags path>/notebook_<name>.py` that calls `notebook.schedule(project=..., name=..., schedule=..., owner=...)`. `<name>` is the file's name lowercased, other characters as `_`, up to 50 characters. Scheduling the same notebook again updates the same branch and merge request.
- **Each run** executes the notebook with papermill as the project's pipelines, with `STORSCALE_PROJECT` set to the project (the templates read it). The executed notebook and its HTML go to the platform, linked from the run on **Pipelines** (**notebook**): the HTML is shown with no script running, or download the `.ipynb` (`GET /api/projects/{name}/notebooks/runs/{dag}/{run}?format=html|ipynb`, to the project's members).
- A failed run says which cell failed and why ("cell 3 failed: KeyError: ..."), the project's alerts say so, and the kept notebook shows the cell and its error.
- **Audit log:** `notebook.scheduled` (the path, schedule, commit, branch and merge request), and `notebook.ran` for each run ("ran" or "failed: ...").

### Limits and gotchas

- "the project's editors schedule notebooks in it" (403): you aren't an editor there.
- "the project's repo needs an https address and a token (its connection) for the platform to commit to it": the repo is SSH, or has no token. "the repo's connection has no token": the connection is empty.
- "a schedule as cron's five fields (UTC): 0 5 * * *": the schedule isn't valid.
- The scheduled environment has the notebook image's analysis and model libraries but not geopandas or networkx.
- A scheduled notebook has no gateway key: `buckets_lake.ask()` is for agents ([AI](14-ai.md)).
- It runs as the pipelines, not as you: it may read more than you can, and writes only where the pipelines may (all tables; the bucket's `landing/`).

See [Lab 11](../guide.md#lab-11-customers-worth-a-call-a-notebook-on-a-schedule), steps 1 to 5.

---

## The notebook templates: churn, propensity, forecast

**What it's for.** A worked start for the three models most teams want, each ending in a registered version ready for approval: change the parameters, run top to bottom.

**Who can do it.** Anyone in JupyterLab. Registering needs `models` in the project; making the features table needs `write-tables`.

### Where they are

Each server start puts them in `work/templates/`: `churn.ipynb`, `propensity.ipynb`, `forecast.ipynb`. A template you've changed, or deleted, is yours: only one still as it was last put there is updated when the image is.

| Template | Model | Judged by | Examples use |
|---|---|---|---|
| `churn.ipynb` | A classifier: each customer's chance of stopping. FLAML picks among LightGBM, XGBoost, random forest, extra trees and L1 logistic regression. | ROC AUC on a held-out quarter | `customer_features`; label from `uploads_customers` and `uploads_orders` (no order since 1 September) |
| `propensity.ipynb` | A classifier: each customer's chance of responding to a campaign. Same estimators. | ROC AUC on a held-out quarter | `customer_campaign_features`; label from `uploads_campaign_responses` (campaign 6) |
| `forecast.ipynb` | A daily series for the next `HORIZON` days. FLAML tries ARIMA, SARIMAX, Holt-Winters and tree models on the calendar. | Mean absolute error on the last `HORIZON` days, beside a naive forecast (`mae_naive`) | Revenue a day from `uploads_orders` |

### Use one

1. Save a copy (**File → Save Notebook As…**).
2. Change the first code cell's parameters: `PROJECT` (it reads `STORSCALE_PROJECT` first, so a scheduled run uses its project), the features table and `ID`, `COLUMNS`, the `LABEL` and the `LABELS` query (or `SERIES`, `DATE`, `VALUE`, `HORIZON` for the forecast), `MODEL`, `EXPERIMENT`, and `TIME_BUDGET` (seconds FLAML may spend; default 60).
3. For churn and propensity, make the features table once: adapt `FEATURES_SQL` and run it (uncomment the last line of its cell) or on the **SQL** tab. One table, two uses: the model learns from it and the scoring scores it, a row per `ID`, its inputs as numbers, no label. The inputs describe each row *before* the period the label looks at.
4. Run top to bottom. The data comes through `buckets_lake.sql()` (as you, at most 100,000 rows: aggregate in SQL). The run logs its parameters, metrics and model in the project's MLflow and registers a new version of `MODEL`.
5. Then, as each template's **Next** section says: write the card, have another editor approve, and for churn and propensity add a scoring. The templates give it:

```json
{"scoring": [{"name": "churn-nightly", "model": "churn", "table": "customer_features", "id": "customer_id",
              "output": "customer_churn_scores", "schedule": "0 3 * * *", "drift": 0.2}]}
```

### Limits and gotchas

- The classifiers assert their inputs are numbers: "the model takes numbers: in customer_features, CAST these or make a 0/1 column per value: [...]".
- The forecast template asserts three times `HORIZON` days of history at least.
- **The forecast model is the whole FLAML AutoML object, saved with cloudpickle, and loads where FLAML and statsmodels are: in notebooks.** Use it from a notebook, as its **Next** section shows (`mlflow.pyfunc.load_model("models:/revenue_forecast@approved")`), and write the forecast to the landing area for import; don't point a scoring or serving at it.
- Propensity wants hundreds of rows with a few dozen responders at least, or the model is noise.

---

## Settings

| Setting (chart) | Default | Compose | What |
|---|---|---|---|
| `notebooks.memoryLimit` | `1G` | fixed `1G` in `jupyterhub_config.py` | Each person's notebook server's memory. |
| `notebooks.cpuLimit` | `2` | 2 (the hub reads `NOTEBOOK_CPUS`, which `compose.yaml` doesn't set) | Each person's notebook server's CPUs. |
| `notebooks.storage` | `1Gi` | a Docker volume, no size | Each person's `work/` volume. |
| `storage.jupyterhub` | `1Gi` | | The hub's own database volume. |
| `placement.jupyterhub` | `{}` | | A node selector for the hub. |
| `images.mlflow` | `platform-mlflow` | `mlflow` service | MLflow's image. |
| `serving.memory` | `1Gi` | one shared `serving` service | A serving's server's memory. |

platformd reaches MLflow at `MLFLOW_URL` (Compose: `http://mlflow:5000`) and the model server at `SERVING_URL`; MLflow keeps its database in `mlflow-db` and its artifacts in Buckets. [Operating the platform](17-operating.md) has the rest.

---

## Errors you may meet

| Where | The words | What to do |
|---|---|---|
| `sql()` | `PermissionError: the platform refused the query on retail (403): ...` | You may not read that: check your role and the readers' rules. |
| `sql()` | warning "the answer has 100000 rows ..." | Aggregate or sample in SQL. |
| `mlflow()` | 403 "not a member of the project" | Use a project you're in. |
| MLflow write | 403 "the project's readers read its experiments and models; its editors and pipelines write them" | Ask for `models`, or have an editor run it. |
| MLflow tag | 403 "the storscale.* tags are the platform's ..." | Use another tag name; approve on **Models**. |
| Approve | 403 "a model version is approved by an editor other than the one who registered it" | Have another editor approve. |
| Approve | 403 "the project's editors approve its models (it takes the permission approve)" | Ask for `approve`. |
| `library()` | `RuntimeError: library <name>: HTTP 404 ...` | Check the name on **Administration → Templates**. |
| Schedule | "You aren't an editor of a project with a repo, where a notebook can be scheduled." | Become an editor, or connect a repo ([Pipelines](09-pipelines-and-git.md)). |
| Scoring run | fails loading `models:/churn@approved` | Approve a version. |
| Predict | 503 "no version of churn is approved" | Approve a version; wait a minute. |

More in [Troubleshooting](18-troubleshooting.md).
