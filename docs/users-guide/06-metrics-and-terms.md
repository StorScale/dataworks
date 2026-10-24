# 6. Metrics and business terms

A project's metrics are defined once, in its semantic model, and every tool asks that one definition: the Metrics tab, Superset's
datasets, apps, assistants over MCP, flows and notebooks. This chapter is for the people who write those definitions (a project's
editors, and anyone given `change-metrics`), for the analysts who ask them, and for the administrators who make a project's model
the company's, so that other projects build on it and its business terms (Revenue, Customer) are everyone's words.

In this chapter:

- [The semantic model](#the-semantic-model)
- [Asking for metrics](#asking-for-metrics)
- [Tests](#tests)
- [Sharing a model, and building on another project's](#sharing-a-model-and-building-on-another-projects)
- [Business terms](#business-terms)
- [Settings](#settings)
- [Errors you may meet](#errors-you-may-meet)

---

## The semantic model

**What it's for.** Finance, marketing and the web team each wrote their own SQL for revenue and got three numbers. A semantic
model says once what revenue is (which column, which rows count, how it's added up) and by what it can be broken down (region,
channel, month). People and tools then ask for `revenue` by `order__region`, not for SQL, and get the same answer to the cent,
each under their own access rules.

**Who can do it.** Every member of the project reads the model and asks its metrics. Changing it takes the permission
`change-metrics`: a project's editors have it, and so does any custom role given it ([Roles](16-roles.md)). Readers see the
definitions but can't change them.

The model is [MetricFlow](https://docs.getdbt.com/docs/build/about-metricflow)'s YAML, read by the platform's semantic layer
(`semanticd`) without dbt. Each project has at most one model, kept in the platform's store as `semantic/<project>.yaml`. The
**Metrics** tab appears on a project that has tables (`tables` in its spec) on an install with the semantic layer.

### Set up

**On the page.** Open the project → **Metrics**. The **Definitions** card at the bottom has **Edit** (for those who may change
the model; **Show** for everyone else). Paste or write the YAML in the box and press **Save**. While it saves the button says
**Checking…**; **Discard** throws away an unsaved draft.

**As a file in the project's repo.** A project kept as code has the model in `storscale/semantic.yaml`, and a release applies it
([History and releases](15-history-and-releases.md)). There is no field for the model in the project's spec: the spec only says
whether the model is shared, or builds on shared ones (see [Sharing](#sharing-a-model-and-building-on-another-projects)).

**Through the API.** The model is read and written whole:

| Method and path | What it does | Who |
|---|---|---|
| `GET /api/projects/{name}/semantic` | The model's YAML, and what it offers: `metrics` (name, description, type, dimensions), `tables`, `terms`, `tests`, `builtOn` | The project's members |
| `PUT /api/projects/{name}/semantic` | Saves a new model, body `{"yaml": "..."}`, after compiling it | `change-metrics` |

```bash
# A token as bob (the public client storscale-agent; the Python package does this for you)
TOKEN=$(curl -s -d grant_type=password -d client_id=storscale-agent -d username=bob -d "password=$PASSWORD" \
  http://storscale.localhost:8800/sso/realms/lakehouse/protocol/openid-connect/token | jq -r .access_token)

# Save the model in orders.yaml as retail's
jq -Rs '{yaml: .}' orders.yaml | curl -s -X PUT http://storscale.localhost:8800/api/projects/retail/semantic \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" -d @-
```

Every change through the API (anything but `GET`) sends `X-Platform-Request: 1`, or it's refused with "missing X-Platform-Request".

### The documents of a model

A model is a YAML file of several documents, separated by `---`. Each document is a mapping with one of these keys:

| Document | What it is | Read by |
|---|---|---|
| `semantic_model` | A table, its entities (keys), dimensions (what to group by) and measures (what to add up) | MetricFlow |
| `metric` | A metric made from measures, or from other metrics | MetricFlow |
| `project_configuration` | MetricFlow's own settings, such as the time spine (optional: see [Limits](#limits-and-gotchas)) | MetricFlow |
| `terms` | The business's words for what the model defines: see [Business terms](#business-terms) | The platform |
| `tests` | Known answers to known questions: see [Tests](#tests) | The platform |

`terms` and `tests` are the platform's; it takes them out before MetricFlow reads the rest. A document that isn't a mapping is
refused: "each document of a model is a mapping (semantic_model, metric, terms, tests)".

Inside `semantic_model` and `metric` the fields are MetricFlow's own (`node_relation`, `defaults.agg_time_dimension`, `entities`,
`dimensions`, `measures`; a metric's `type` and `type_params`); MetricFlow's documentation describes them. The platform adds
nothing to them.

### A complete example

`retail`'s model from [Lab 6](../guide.md#lab-6-define-revenue-once), over the table `uploads_orders`:

```yaml
semantic_model:
  name: orders
  description: Every order, one row each; test rows and refunds excluded by the metrics.
  node_relation: {database: iceberg, schema_name: retail, alias: uploads_orders}
  defaults: {agg_time_dimension: ordered_on}
  entities:
    - {name: order, type: primary, expr: order_id}
    - {name: customer, type: foreign, expr: customer_id}
  dimensions:
    - {name: region, type: categorical, description: "EU, US or APAC"}
    - {name: channel, type: categorical, description: "web, store or partner"}
    - {name: ordered_on, type: time, type_params: {time_granularity: day}}
  measures:
    - {name: amount_sum, expr: "case when not test and not refunded then amount end", agg: sum, description: "The orders' amounts, net of refunds and test rows"}
    - {name: orders, expr: "case when not test then 1 end", agg: sum}
    - {name: refunds, expr: "case when refunded then 1 end", agg: sum}
    - {name: customers, expr: customer_id, agg: count_distinct}
---
metric:
  name: revenue
  description: What the orders brought in, net of refunds, test rows excluded.
  type: simple
  type_params: {measure: amount_sum}
---
metric:
  name: order_count
  description: How many real orders there were.
  type: simple
  type_params: {measure: orders}
---
metric:
  name: customer_count
  description: How many customers ordered.
  type: simple
  type_params: {measure: customers}
---
metric:
  name: average_order_value
  description: Revenue per order.
  type: ratio
  type_params: {numerator: {name: revenue}, denominator: {name: order_count}}
---
terms:
  - name: revenue
    owner: bob
    definition: What the orders brought in, net of refunds, in US dollars. Test rows are never counted.
    synonyms: [sales, turnover]
---
tests:
  - name: three_regions
    metrics: [revenue]
    group_by: [order__region]
    expect: {rows: 3}
```

`node_relation` names the table in full: `database` is the Trino catalog (`iceberg`), `schema_name` the project's namespace (the
project's name with dashes made underscores, unless `tables.namespace` says otherwise) and `alias` the table.

Dimensions are asked for by their entity and name, joined by two underscores: `order__region`, `order__ordered_on`. Every metric
can also be grouped by `metric_time`, at a grain: `metric_time__month`. Through a foreign entity, a model reaches the dimensions
of the model whose primary entity it is: with a `customers` model whose primary entity is `customer`, `orders` can be grouped by
`customer__segment`.

### How it behaves

- **Saving compiles first.** `semanticd` compiles the whole model with MetricFlow (with the shared models it builds on, if any)
  and checks its terms and tests. Anything wrong is refused with the reason, and nothing is saved: "not YAML: …", "MetricFlow
  can't read it: …", MetricFlow's validation messages, a term or a test naming something the model lacks.
- **A saved model applies to the next question anyone asks.** There's nothing to deploy: the Metrics tab, apps, Superset's
  datasets and assistants all read the saved model. Superset's dataset follows within a minute ([Dashboards](07-dashboards-apps-assistants.md#dashboards-superset)).
- **The page shows what it offers.** **The metrics** card lists each metric with its type and what it can be grouped by, and the
  tables the model reads ("over iceberg.retail.uploads_orders"). If the saved model no longer compiles (a shared model it builds on
  changed, say), a red banner says "The definitions don't compile:" and why.
- **Where it's recorded.** `semanticd`'s log records each save ("bob saved retail's semantic model (4 metrics)"). The questions
  asked of it are in Ranger's audit log, which the project's **Audit** tab shows ([Asking for metrics](#asking-for-metrics)). A model applied by
  a release is in the platform's audit log as the release ([History and releases](15-history-and-releases.md)).

### Limits and gotchas

- **One model per project**, as one file; it may hold any number of semantic models and metrics.
- **The time spine.** MetricFlow wants a time spine declared even when no question uses one. When the model declares none (no
  `project_configuration` document), the platform declares one: a table `time_spine_day` in the project's namespace, with a column
  `date_day`. It declares it; it doesn't make it. Plain questions don't read it; a metric that needs one (cumulative metrics, for
  instance) fails until that table exists. Make it in the project (a dbt model, or SQL), or declare your own `project_configuration`.
- **The rest is MetricFlow's.** Its own checks (names, references between measures and metrics, entity types) are passed on as it
  words them.
- **An administrator who isn't a member** of the project is refused the model ("you're not a member of retail"): the semantic layer
  answers members only.
- **A model that names a table you can't read compiles**, and the questions fail at Trino: compiling doesn't read the data.

---

## Asking for metrics

**What it's for.** Getting a number without writing SQL: pick the metrics, pick what to group them by, and get the governed
answer, with the SQL it ran shown.

**Who can do it.** The project's members. Each question runs in Trino as the person asking, so a reader under a row filter gets
only the rows their rules allow (alice, a reader of `retail`, gets the EU only), and masked columns stay masked.

### Set up

On the project → **Metrics**, the **Ask for metrics** card:

1. Under **Metrics**, press the chips of the metrics you want (hover one for its description).
2. Under **Group by**, press the dimensions to break them down by. Only those all the picked metrics share are offered.
3. Press **Ask**. The **Answer** card shows the rows (and bars, when there's one label and one number and at most 20 rows). **The
   SQL it ran, as you** opens the compiled SQL.

The top bar's search finds a project's metrics too; picking one opens the Metrics tab with it chosen.

**Through the API:** `POST /api/projects/{name}/semantic/query`, as a member:

```json
{
  "metrics": ["revenue", "order_count"],
  "group_by": ["order__region"],
  "where": ["{{ Dimension('order__channel') }} = 'web'"],
  "order_by": ["-revenue"],
  "limit": 100
}
```

| Field | What it is |
|---|---|
| `metrics` | The metrics, by name: at least one ("ask for at least one metric") |
| `group_by` | Dimensions, as `entity__dimension`, or `metric_time__day` (`__week`, `__month`, …) |
| `where` | Conditions in MetricFlow's template form: `{{ Dimension('order__region') }} = 'EU'`, `{{ TimeDimension('metric_time', 'day') }} >= '2026-09-01'` |
| `order_by` | Metric or dimension names; a leading `-` for descending |
| `limit` | Rows, at most 1000 |
| `explain` | `true`: the SQL only, not run |

The answer has `columns`, `types`, `rows` and the `sql` it ran. From Python, the same route by its operation ID:
`p.call("post_projects_name_semantic_query", {"metrics": ["revenue"], "group_by": ["order__region"]}, name="retail")`.

### How it behaves

- MetricFlow compiles the question to Trino SQL; `semanticd` runs it in Trino **as the person**, with their own Keycloak token,
  in the project's namespace, under the project's limits (the Trino resource group the client tag `project:<name>` selects).
- The SQL starts with a comment naming who asked and through what:
  `-- storscale: agent=<client> user=<person> project=<project> metrics=<metrics>`. Ranger's audit log keeps it, and the
  project's **Audit** tab shows it, so a question asked through an assistant names both the assistant and the person.
- An answer has at most 1000 rows; the Metrics tab asks for 100.

### Limits and gotchas

- A question MetricFlow can't compile (a dimension the metric doesn't have, a misspelt name) is refused as "MetricFlow can't answer
  that: …".
- A table the person may not read is Trino's refusal, passed on as a 403 with Ranger's words ("Access Denied: …").
- The **Group by** chips are the dimensions every picked metric has; to group by `metric_time` at a grain, use the API, or an app
  ([Apps](07-dashboards-apps-assistants.md#apps)).

---

## Tests

**What it's for.** A changed definition shouldn't quietly change last month's revenue. A test pins an answer to a question
(revenue by region has three rows; the quarter had 300 real orders), so a change that moves it fails before anyone relies on it.
A test may also be a question in words with the metric call it should become, so a renamed metric or a lost synonym fails before an
assistant meets it.

**Who can do it.** Writing tests is changing the model (`change-metrics`). Any member may run them; they run as whoever runs them.

### Set up

Add a `tests` document to the model:

```yaml
tests:
  - name: three_regions
    metrics: [revenue]
    group_by: [order__region]
    expect: {rows: 3}
  - name: web_orders
    metrics: [order_count]
    where: ["{{ Dimension('order__channel') }} = 'web'"]
    expect: {rows: 1}
  - name: all_orders
    metrics: [order_count]
    expect: {values: [[300]]}
  - name: turnover_in_words
    question: What was turnover by region?
    metrics: [revenue]
    group_by: [order__region]
```

| Field | What it is |
|---|---|
| `name` | Required, unique in the model ("each test has a name", "two tests named …") |
| `metrics` | Required: at least one metric the model has ("test …: the model has no metric …") |
| `group_by`, `where` | Lists, as in a question ("test …: group_by is a list") |
| `expect.rows` | How many rows the answer has: a number |
| `expect.values` | The rows themselves: a list of rows, each a list. Compared in any order; numbers as numbers, to six decimal places |
| `question` | A question in words. The model is asked which metric call answers it; the test passes when the call is `metrics` by `group_by` (in any order), and, if `expect` is given too, when its answer matches |

A test without `question` needs `expect` with `rows` or `values`.

To run them: on **Metrics**, the **Definitions** card has **Run the N tests** when the model has tests. Each shows ✓ or ✗ with
why: "2 rows, expected 3", "the rows differ from the expected: …", "the model asked for [...] by [...], expected [...] by [...]".

**Through the API:** `POST /api/projects/{name}/semantic/tests`, as a member, body `{}` for the saved model, or
`{"yaml": "..."}` to test a model before saving it. The answer lists each test with `passed`, `why`, its `rows`, and counts
`passed`, `failed` and `skipped`.

### How it behaves

- **Checked on save.** A model whose tests name a metric it doesn't have is refused, so renaming a metric a test uses fails the save,
  saying which test.
- **Run as the person.** A test's answer is the person's: as alice, under the EU row filter, `three_regions` fails with one row.
  Run tests as someone who reads the table as it is (an editor).
- **Questions in words** go to a model through the LLM gateway, on the project's key, with the model's metrics (their descriptions,
  and the terms' synonyms) and dimensions. A project without a gateway key (no `llm.budget`) skips them: "not asked: the project has
  no gateway key (llm.budget)". Each question is in the platform's audit log as `llm.asked`, with what it spent
  ([AI](14-ai.md)).
- **In merge requests.** With a GitLab repo, a merge request's review runs the tests on its data branch and compares each answer
  with production's ([Pipelines and the project's repo](09-pipelines-and-git.md)).

### Limits and gotchas

- `expect.values`: a value that reads as a number is compared as a number, to six decimal places, so `22168.4` and `"22168.40"`
  (Trino returns decimals as text) match; anything else is compared as text, and `null` as null.
- A question test spends the project's LLM budget each time it runs.

---

## Sharing a model, and building on another project's

**What it's for.** The company's words shouldn't be defined again in each project. One project's model (`retail`'s, with revenue
and customers) is made the company's: its terms appear on **Business terms** and in the catalog, and other projects build their
own metrics on its definitions instead of copying them.

**Who can do it.** Both settings are in the project's spec, which administrators change ([Projects](02-projects.md)).

### Set up

On the project → **Edit** (administrators), in the JSON box:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "reader"}],
    "tables": {},
    "semantic": {"shared": true}
  }
}
```

and for a project that builds on it:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "marketing"},
  "spec": {
    "members": [{"group": "analysts", "role": "editor"}],
    "tables": {},
    "semantic": {"uses": ["retail"]}
  }
}
```

| Field | What it is |
|---|---|
| `semantic.shared` | `true`: this project's model is the company's, for others to build on; its terms are the company's |
| `semantic.uses` | The shared models this project's model builds on, by project name ("semantic: uses: … isn't a project's name"; "a project's model doesn't build on itself") |

The same through the API is the whole spec: `PUT /api/projects/{name}` ([Projects](02-projects.md)).

Then `marketing`'s model uses `retail`'s metrics by name, without defining them:

```yaml
metric:
  name: revenue_per_customer
  description: Revenue per customer, on retail's definitions of both.
  type: ratio
  type_params: {numerator: {name: revenue}, denominator: {name: customer_count}}
```

### How it behaves

- **Compiled together.** A project's model is compiled with the shared models it uses first, then its own. `marketing`'s Metrics tab
  lists retail's metrics too, and can ask them by `marketing`'s dimensions and retail's.
- **Not defined twice.** A project that builds on a shared model may not define its semantic models' or metrics' names again:
  "the company's model has these already, so build on them rather than defining them again: revenue (the metric of retail)".
- **A shared model can't break its users.** Saving a shared model compiles every project that builds on it against the new text;
  if one would no longer compile, the save is refused naming it: "it would break the models of marketing (…)".
- **Answers cite their source.** An assistant's answer names each metric's definition and the project it's defined in
  ([Assistants](07-dashboards-apps-assistants.md#assistants-over-mcp)).
- **Reading still takes access.** Building on retail's metrics gives `marketing` the definitions, not the rows: the SQL reads
  `iceberg.retail.uploads_orders`, and runs as the person asking. They need to read that table: as a member of `retail`, or as a
  consumer of a data product of it ([Data products](11-data-products.md)).

See [Lab 6](../guide.md#lab-6-define-revenue-once) (sharing) and [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place)
(building on it).

### Limits and gotchas

- `uses` naming a project whose model isn't shared is accepted in the spec and ignored: nothing is built on it. Mark that project
  `shared` first.
- Each project keeps its own time spine; a shared model's `project_configuration` isn't carried over.
- There's no review of a shared model's changes beyond the compile and its users' compile; keep the shared project as code with
  merge requests for that ([Pipelines and the project's repo](09-pipelines-and-git.md)).

---

## Business terms

**What it's for.** "Customer", "revenue", "basket size": the words people use, each with one definition, one owner and the
synonyms people say instead, tied to what the model defines. People find data by those words before they find tables: the
**Business terms** page, the top bar's search, the catalog's glossary, and assistants all start from them.

**Who can do it.** Terms are part of a model: writing them takes `change-metrics` on that project. They are the company's only when
the project's model is shared (`"semantic": {"shared": true}`). Everyone signed in sees them.

### Set up

Add a `terms` document to the shared project's model, and save it on **Metrics** → **Edit**:

```yaml
terms:
  - name: revenue
    owner: bob
    definition: What the orders brought in, net of refunds, in US dollars. Test rows are never counted.
    synonyms: [sales, turnover]
  - name: customer
    owner: bob
    definition: A person or company that has placed at least one order.
    synonyms: [client, account]
  - name: average_order_value
    owner: bob
    definition: "Revenue per real order, in US dollars: what a customer spends on one order, on average."
    synonyms: [basket size, AOV]
```

| Field | What it is |
|---|---|
| `name` | Required: an entity, a metric or a dimension the model defines (`customer` is the entity above, `revenue` the metric) |
| `definition` | The meaning, in plain words |
| `owner` | Who answers for it: a user name |
| `synonyms` | Other words for it, which search and assistants match |
| `label` | Optional: the name shown (default: the name with underscores as spaces, capitalised: "Average order value") |
| `kind` | Optional: `entity`, `metric` or `dimension`, when one name is more than one of them |

**Quote a definition that has a colon in it.** YAML reads `definition: Revenue per real order, in US dollars: what…` as a mapping
inside a mapping and the save is refused ("not YAML: mapping values are not allowed here"). Put the whole definition in double
quotes, as above. The same goes for any value with `: `, or one starting with `[`, `{`, `*`, `&` or `#`.

### How it behaves

- **Checked on save.** Each term must name an entity, a metric or a dimension of this model; otherwise the save is refused, naming
  it: "terms: basket_size isn't an entity, metric or dimension of this model". With `kind`, the name must be one of that kind.
- **On the Business terms page.** The top bar's **Business terms** lists every term of every shared model: its label and kind, its
  definition, owner, and the projects using it. A term's own page (`/terms/<name>`) shows its definition, owner and synonyms ("Also
  called"), the shared model and project it's defined in, **Metrics on it** (for an entity: the metrics over the same tables),
  **Where it comes from** (the tables), and **Projects using it** (projects building on the shared model whose own model names it).
- **In search.** The top bar's search lists terms first, matching their label, definition and synonyms: "turnover" finds Revenue.
- **In the catalog.** catalog-sync keeps OpenMetadata's glossary **Business terms** in step with the shared models' terms (each with
  its definition, synonyms, and its owner when the catalog knows that user), tags the tables each term comes from with it, and
  removes terms no longer in any shared model. It runs every `METADATA_EVERY` seconds (60), so a new term is in the catalog within
  a few minutes.
- **For assistants.** The MCP tools `search_terms`, `describe_term` and `list_terms` answer from them, and every answer cites the
  terms behind what it used ([Assistants](07-dashboards-apps-assistants.md#assistants-over-mcp)). A question test's prompt lists the
  terms' synonyms beside the metrics.
- **For data products.** The publish form lists the company's terms to tick as those the product holds ([Data products](11-data-products.md)).

**Through the API:** `GET /api/terms`, to anyone signed in: each term with `name`, `label`, `kind`, `definition`, `owner`,
`synonyms`, `project`, `tables`, `metrics` and `usedBy`.

See [Lab 6](../guide.md#lab-6-define-revenue-once), steps 5 and 6.

### Limits and gotchas

- **Only shared models' terms are the company's.** A project whose model isn't shared may have a `terms` document; it's checked on
  save, and appears nowhere: not on Business terms, not in the catalog, not to assistants. Mark the project
  `"semantic": {"shared": true}` to publish its terms.
- **A term names what its own model defines.** In a project that builds on a shared model, a term naming the shared model's metric
  is refused: the term belongs in the shared model, with the metric.
- **A term isn't a free word.** "Basket size" can't be a term of its own; make it a synonym of the metric it means
  (`average_order_value`), or define that metric first.
- **The Business terms button** shows only on installs with the semantic layer.
- Two shared models with a term of the same name both list on the page, but the catalog's glossary keeps one of them. Keep names
  unique across shared projects.

---

## Settings

| Setting | Chart value (`deploy/helm/storscale-platform/values.yaml`) | Compose (`stack/compose.yaml`, `stack/.env`) | Default |
|---|---|---|---|
| How many `semanticd` pods | `replicas.semanticd` | one container | 1 |
| The semantic layer platformd calls | set by the chart | `SEMANTIC_URL` on platformd | `http://semanticd:8000`; empty turns off Metrics, SQL, Flows, Agents and Apps tabs and Business terms |
| How often the catalog's glossary is synced | not a chart value | `METADATA_EVERY` on catalog-sync | 60 seconds |
| How often Superset's datasets follow the models | not a chart value | `SYNC_EVERY` on superset-sync | 60 seconds |

---

## Errors you may meet

| Error | What it means | What to do |
|---|---|---|
| "only those who may change retail's metrics (its editors) change its semantic model" | You lack `change-metrics` on the project | Ask an editor, or for a role that gives it |
| "not YAML: mapping values are not allowed here" | A value with `: ` in it isn't quoted (often a term's definition) | Put the value in double quotes |
| "MetricFlow can't read it: …" | A document isn't the shape MetricFlow expects | Check the field names against MetricFlow's documentation |
| "terms: X isn't an entity, metric or dimension of this model" | A term names nothing the model defines | Name the metric, entity or dimension it means; make the word a synonym |
| "test X: the model has no metric Y" | A test names a metric that isn't there (often after a rename) | Rename it in the test too |
| "the company's model has these already, so build on them rather than defining them again: …" | Your model defines a name the shared model it uses has | Remove yours, and use theirs |
| "it would break the models of marketing (…)" | A change to a shared model breaks a project built on it | Change both: the user's model first, or keep the old name |
| "MetricFlow can't answer that: …" | The question names a metric or dimension that doesn't fit | Pick from the Metrics tab's list; dimensions are `entity__dimension` |
| "you're not a member of retail" | The semantic layer answers the project's members only | Be made a member |
| "The definitions don't compile: …" (banner) | The saved model no longer compiles, often after a shared model changed | Fix the model; the reason names what's wrong |
