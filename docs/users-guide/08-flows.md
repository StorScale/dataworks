# 8. Flows

A flow is a pipeline built on a canvas: steps joined into a graph, from one or more tables to one output table. Each step is
a dbt model in the project's repo, so what an analyst builds on the canvas is code an engineer can read, review and change
by hand, and the canvas keeps working after the change. This chapter is for the analysts who build flows, the engineers who
review and change them, and the administrators who set up what they need (a repo, the semantic layer, the LLM gateway).

In this chapter:

- [What a flow is](#what-a-flow-is)
- [Building a flow on the canvas](#building-a-flow-on-the-canvas)
- [The step kinds](#the-step-kinds)
- [The prepare step and its processors](#the-prepare-step-and-its-processors)
- [The model step (Ask a model)](#the-model-step-ask-a-model)
- [Previews and what they cost](#previews-and-what-they-cost)
- [Saving a flow as a merge request](#saving-a-flow-as-a-merge-request)
- [Code steps: changing a flow by hand](#code-steps-changing-a-flow-by-hand)
- [The flows API](#the-flows-api)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

---

## What a flow is

**What it's for.** An analyst needs a table that doesn't exist yet: orders joined to campaign responses, filtered, grouped,
with a computed column. A flow builds it from steps picked from dropdowns, shows each step's columns and SQL before anything
runs, previews each step on a sample, and saves the result as dbt models in the project's repo, on a branch with a merge
request. Merged, it's one of the project's pipelines, built on its schedule with its checks.

**Where it lives.** A flow named `campaign_results` is a folder of the project's repo:

| File | What it is |
|---|---|
| `<path>/dbt/models/flows/campaign_results/<model>.sql` | One dbt model per step. A step's model is named `<flow>__<step>` (`campaign_results__filter_2`), ephemeral; the output step's is named after its table, materialized as a table |
| `<path>/dbt/models/flows/campaign_results/sources.yml` | The tables the flow's inputs read, as dbt sources (one per catalog and namespace, named `<flow>__<catalog>_<namespace>`) |
| `<path>/dbt/models/flows/campaign_results/schema.yml` | Each model's description and place on the canvas (`meta.storscale`: step, x, y), and the output's checks as dbt tests |
| `<path>/flow_campaign_results.py` | The Airflow DAG `flow_campaign_results`, which builds the flow on its schedule |
| `<path>/dbt/dbt_project.yml` | Written by the first save if the repo has none: `profile: storscale`, `model-paths: [models]` |

`<path>` is the project's `pipelines.path` (the DAGs' folder, `dags` in the examples). A model the canvas wrote starts with
its step's settings as YAML in a comment, and a hash of the SQL under it:

```sql
-- storscale-step:
--   kind: filter
--   inputs:
--     - input
--   where: converted
--   sql: 3f1c0e9a2b7d4c55
{{ config(materialized='ephemeral') }}
select *
from {{ ref('campaign_results__input') }}
where converted
```

The canvas reads the folder back the same way: a model whose header still matches its SQL is a step; one changed by hand,
or written without a header, is a [code step](#code-steps-changing-a-flow-by-hand).

**Who can do it.**

| What | Who |
|---|---|
| Open the Flows tab, open a flow, see each step's columns and SQL, preview a step | Any member of the project (an administrator counts as an editor). A preview runs as you, under your rules |
| Start a flow, add, change and connect steps on the canvas | The permission `build`: editors, and custom roles that have it ([Roles](16-roles.md)). Others see the canvas read-only; through the API, any member may compile and preview a flow they send |
| Save a flow (as a merge request) | `build` |
| Run a model step's question for every row (`POST .../steps/{step}/run`) | `build`, the project's pipelines, administrators |
| Merge the merge request | Whoever GitLab lets merge into the branch the pipelines follow |
| Start the flow's DAG by hand | `run-pipelines` ([Pipelines and the project's repo](09-pipelines-and-git.md)) |

**What it needs.** Building and previewing need only the platform: the catalog (Nessie, for the inputs' columns) and the
semantic layer (semanticd, which runs previews in Trino as you). Saving needs the project's repo: `pipelines.repo` with an
`https://` address, `pipelines.credentials.secretRef` naming a connection of kind `git`, and preferably `pipelines.ref`
([Pipelines and the project's repo](09-pipelines-and-git.md#connecting-the-projects-repo) says how). Without it, the Flows tab
says "Flows can be built and previewed here; saving one needs the project's pipelines repo (…)", with the reason.

See [Lab 9 in the lab guide](../guide.md#lab-9-which-campaigns-pay-for-themselves), and
[Lab 10](../guide.md#lab-10-the-project-as-code-with-a-git-repo) for saving.

---

## Building a flow on the canvas

### Set up

1. Open the project's **Flows** tab. It lists the flows in the repo (the folders under `models/flows/` on the branch the
   pipelines follow, and any saved on a branch of their own and not merged yet), each with its branch and a link to its
   merge request. The same tab holds the project's **Uploads** ([Getting data in](03-data-in.md)).
2. Under **Start a flow**, type a name (`campaign_results`) and press **New flow**. The name is lowercase letters, digits and
   underscores, starting with a letter, at most 41 characters: it names the dbt models and, by default, the output table.
3. **Add a step…** offers the kinds below. A new step is placed beside the selected one and takes it as its input when it
   takes one. Drag from a node's right edge to another's left edge to connect them; select a connection or a node and press
   Delete to remove it.
4. Select a node to see its settings on the right. Every table, column, step and metric is picked from a dropdown, so what
   you can choose is what exists. Inputs offer the project's tables, and the tables of other projects' data products it
   consumes, under **Products of other projects**.
5. Each change is compiled again a moment later (about 0.4 seconds): every node shows its column count, "can't compile" in
   red, or "columns unknown". The errors are listed above the canvas, one per failing step.
6. Optionally, type a schedule in the box beside **Save as merge request**: cron's five fields, UTC (`0 6 * * 1`, Mondays at
   06:00). Empty means the DAG runs only when started.
7. Press **Preview** on a selected step to see its rows ([Previews](#previews-and-what-they-cost)), or **SQL** to read its model.
8. Press **Save as merge request** ([Saving](#saving-a-flow-as-a-merge-request)).

### How it behaves

- **Compiling** works out each step's columns and their Trino types from its inputs', and its SQL, before anything runs. An
  input's columns come from the catalog. A metric step's come from the semantic layer, asked as you. A code step's come from
  Trino, which the platform asks as you (it runs the step's query with no rows).
- A step that can't be compiled has an error and no columns; the steps after it say "its input `<step>` has an error".
- **Reading the folder back**: opening a flow reads its folder from its own branch (`storscale/flow-<name>`) while that
  branch exists, else from the branch the pipelines follow.
- Nothing is written anywhere until you save; closing the canvas without saving loses the change.

---

## The step kinds

Each step kind, its label on the canvas, its inputs, and its settings as JSON (the field names of the flow's JSON, the
`step` objects in [the flows API](#the-flows-api)). Every step also has `id` (its name: lowercase letters, digits and
underscores, starting with a letter), `kind`, `inputs` (step IDs), an optional `description`, and `x`, `y` (its place).

| Kind | Canvas label | Inputs | Settings |
|---|---|---|---|
| `input` | Input | none | `table`; `namespace` (another project's, for a product it consumes; default the project's); `catalog` (default the project's, `iceberg`) |
| `select` | Select and rename | 1 | `columns`: `[{name, as}]`, the columns kept, in order, each renamed when `as` is set |
| `retype` | Retype | 1 | `columns`: `[{name, type}]`, each cast to a Trino type |
| `filter` | Filter | 1 | `where`: a Trino condition |
| `join` | Join | 2 (left, right) | `join`: `inner` (default), `left` or `full`; `on`: `[{left, right}]` |
| `union` | Union | 2 or more | `distinct`: true for `union`, false (default) for `union all` |
| `dedupe` | Deduplicate | 1 | `by`: the columns that make rows the same (none: whole rows); `order`: `[{column, desc}]`, which row to keep first |
| `group` | Group by | 1 | `by`; `aggregates`: `[{function, column, as}]` |
| `pivot` | Pivot | 1 | `by` (the rows); `column` (whose values become columns); `values` (those values); `value`; `function` |
| `unpivot` | Unpivot | 1 | `unpivot` (columns that become rows); `name` (the name column, default `name`); `value` (the value column, default `value`) |
| `window` | Window | 1 | `partition`; `order`; `windows`: `[{function, column, as, offset}]` |
| `prepare` | Prepare | 1 | `processors`: `[{column, do, args, as}]` ([below](#the-prepare-step-and-its-processors)) |
| `formula` | Formula | 1 | `columns`: `[{name, expression, type}]` |
| `llm` | Ask a model | 1 | `column`, `mode`, `values`, `prompt`, `name`, `bank`, `limit` ([below](#the-model-step-ask-a-model)) |
| `metric` | Metric | none | `metrics`; `by` (dimensions); `where` |
| `output` | Output | 1 | `table` (the table it makes; default the flow's name on the canvas); `checks`: `[{column, test, values}]` |
| `code` | Code | 0 or more (its refs) | `sql`: the model as written ([below](#code-steps-changing-a-flow-by-hand)) |

What each does, and what it refuses:

- **input** reads `select * from` the table, through the flow's dbt source. The table must be the project's, or a data
  product's table whose `consumers` name this project ([Data products](11-data-products.md)); otherwise "`<namespace>.<table>`
  isn't the project's, nor a product it consumes". Its catalog must be the lakehouse's: "an input is a table of the
  lakehouse (iceberg), not of …".
- **select** keeps at least one column ("a select step keeps at least one column"); two kept columns can't share a name.
  The canvas's **Keep all** fills in every input column.
- **retype** casts columns. The type is one of `varchar`, `char`, `varbinary`, `tinyint`, `smallint`, `integer`, `bigint`,
  `real`, `double`, `decimal`, `boolean`, `date`, `time`, `timestamp`, `json`, `uuid`, with a size or precision where Trino
  takes one (`decimal(10,2)`, `timestamp(6)`, `timestamp(3) with time zone`). Anything else: "`"x"` isn't a Trino type".
- **filter** keeps the rows where the condition holds (`not test and amount > 0`).
- **join** joins two inputs on pairs of columns. Columns whose types can't be compared are refused before anything runs:
  "join on filter.customer_id (bigint) and input_2.responded_on (date): their types differ; retype one first". Numbers
  compare with numbers, text with text, timestamps with timestamps of the same zone-ness. A right-hand column whose name
  the left has is renamed with `_right` (`customer_id_right`).
- **union** stacks two or more inputs. Every input must have the first's columns, by name, of compatible types, and no
  others: "union: input x has 6 columns, y has 5; select the same columns in each first".
- **dedupe** without `by` is `select distinct *`; with `by`, keeps one row per `by` value, the first by `order` (or by the
  `by` columns when no order is given).
- **group** groups by `by` (none: one row for the whole input) with at least one aggregate: `sum`, `count`, `min`, `max`,
  `avg`, `count_distinct`. `count` without a column is `count(*)`, named `count`; otherwise an aggregate without `as` is
  named `<function>_<column>` (`sum_amount`). `sum` and `avg` need a number column. Sums of integers are `bigint`, of
  `decimal(p,s)` `decimal(38,s)`; averages of integers are `double`.
- **pivot** makes one column per listed value of `column`, each `function` (default `sum`; `sum`, `count`, `min`, `max`,
  `avg`) of `value`, grouped by `by`. Trino needs the values named: "pivot: say which values of region become columns
  (Trino needs them named)". A value becomes a column name in lower case with other characters as underscores
  (`North America` → `north_america`; one starting with a digit gets `v_`).
- **unpivot** turns the listed columns into rows of two new columns, their names and their values. The listed columns must
  share a type: "unpivot: … retype them to one type first".
- **window** adds columns computed over a window (`partition` by, `order` by): `rank`, `dense_rank`, `row_number`,
  `running_sum`, `sum`, `avg`, `min`, `max`, `count`, `lag`, `lead`. Each needs a name (`as`). `rank`, `dense_rank`,
  `row_number`, `running_sum`, `lag` and `lead` need an order: "window: rank needs an order". `lag` and `lead` look
  `offset` rows away (default 1).
- **formula** computes columns from Trino expressions (`revenue / budget`). A `type` casts the result and tells the steps
  after what it is; without one, the column's type is unknown to the canvas. A formula named as an existing column replaces it.
- **metric** asks the semantic layer for a metric query's SQL (its metrics, grouped by `by`, filtered by `where`, in
  MetricFlow's filter syntax: `{{ Dimension('order__region') }} = 'EU'`), so a flow's revenue is the company's
  ([Metrics and business terms](06-metrics-and-terms.md)). It needs at least one metric, and the semantic layer: "the
  semantic layer isn't set up, which gives a metric step its SQL".
- **output** is the table the flow makes, with dbt tests on its columns: `not_null`, `unique`, `accepted_values` (with
  `values`). A flow has exactly one output; saving refuses a flow without one: "a flow is saved with an output step: the
  table it makes".

An expression in a filter or a formula is one expression, nothing more: it can't contain `;`, `--`, `/*`, `{{` or `{%`
("an expression can't contain "--"").

A flow's shape is checked first: names, each step's number of inputs ("step join (join): takes 2 inputs, not 1"; "takes
at least two inputs" for a union; "takes no input" for an input or a metric), inputs that exist, one output, and no circle
("the steps go round in a circle: a -> b -> a").

---

## The prepare step and its processors

**What it's for.** The cleaning that every file needs (trim, case, parse dates and numbers, fill nulls, standardize values)
without writing SQL. A prepare step applies named processors to columns, in order; each is compiled to Trino SQL, and each
argument is a value, never SQL.

**Set up.** **Add a step → Prepare**, then **Add a processor** for each change: pick the column, the processor (grouped as
Text, Nulls, Numbers, Dates, Flags and values), its arguments, and optionally a new column's name ("as"). A processor
changes its column in place; with "as", it makes a new column and leaves the original alone. Later processors see the
column as the earlier ones left it.

```json
{"id": "clean", "kind": "prepare", "inputs": ["orders"], "x": 440, "y": 0,
 "processors": [
   {"column": "region", "do": "trim"},
   {"column": "region", "do": "upper"},
   {"column": "region", "do": "standardize", "args": ["EUROPE=EU, USA=US"]},
   {"column": "amount", "do": "round", "args": ["2"]},
   {"column": "ordered_on", "do": "days_since", "as": "days_ago"}
 ]}
```

**The processors** (42), their arguments in order (optional ones marked), and the column's type after ("same" keeps it;
"text" makes it `varchar`). A processor that works on text reads a non-text column as text.

| Group | `do` | Label | Arguments | Type after |
|---|---|---|---|---|
| text | `trim` | Trim spaces | | text |
| text | `upper` | Upper case | | text |
| text | `lower` | Lower case | | text |
| text | `title` | Title Case | | text |
| text | `squeeze` | Squeeze spaces (one between words, none around) | | text |
| text | `left` | Keep the first characters | how many (number) | text |
| text | `right` | Keep the last characters | how many (number) | text |
| text | `substring` | Substring | from (1 is the first) (number); length (number, optional) | text |
| text | `replace` | Replace text | find (text); replace with (text, optional) | text |
| text | `regex_replace` | Replace by pattern | pattern; replace with (text, optional) | text |
| text | `extract` | Extract by pattern | pattern; group (0 is the whole match) (number, optional) | text |
| text | `split` | Split, and keep a part | separator (text); part (1 is the first) (number) | text |
| text | `prefix` | Add a prefix | prefix (text) | text |
| text | `suffix` | Add a suffix | suffix (text) | text |
| text | `pad_left` | Pad on the left | to length (number); with (text) | text |
| text | `pad_right` | Pad on the right | to length (number); with (text) | text |
| text | `length` | Length | | bigint |
| text | `hash` | Hash (SHA-256) | | text |
| nulls | `fill` | Fill nulls with a value | value | same |
| nulls | `null_if` | Null when it equals a value | value | same |
| nulls | `empty_to_null` | Null when empty or blank | | text |
| nulls | `null_unless` | Null unless it matches a pattern | pattern | text |
| numbers | `parse_number` | Parse as a number (digits, sign and point kept) | | double |
| numbers | `round` | Round | decimals (number) | same |
| numbers | `abs` | Absolute value | | same |
| numbers | `floor` | Round down | | same |
| numbers | `ceil` | Round up | | same |
| numbers | `clamp` | Clamp between two values | lowest (number); highest (number) | same |
| numbers | `bucket` | Bucket (1 to n, between two values) | lowest; highest; buckets (numbers) | bigint |
| numbers | `scale` | Multiply by | factor (number) | same |
| numbers | `to_text` | To text | | text |
| dates | `parse_date` | Parse as a date | format (`%Y-%m-%d`; empty for ISO) (optional) | date |
| dates | `parse_timestamp` | Parse as a timestamp | format (`%Y-%m-%d %H:%i:%s`; empty for ISO) (optional) | timestamp(3) |
| dates | `format_date` | Format a date as text | format (`%Y-%m-%d`) | text |
| dates | `date_part` | A part of a date | part (unit) | bigint |
| dates | `truncate_date` | Truncate a date | to (unit) | same |
| dates | `add_days` | Add days (negative to subtract) | days (number) | same |
| dates | `days_since` | Days since (to today) | | bigint |
| flags | `to_boolean` | To true or false (yes, y, 1, true; no, n, 0, false) | | boolean |
| flags | `not_null_flag` | True when set | | boolean |
| flags | `is_in` | True when one of | values, comma-separated (list) | boolean |
| flags | `standardize` | Standardize values (from=to, comma-separated) | from=to, comma-separated (pairs) | same |

How arguments are read:

- A **number** must parse as one: "Round: decimals is a number, not "two"".
- A **unit** is one of `year`, `quarter`, `month`, `week`, `day`, `hour`.
- **pairs** are `from=to`, comma-separated: "Standardize values (from=to, comma-separated): each pair is from=to; "EU" isn't".
- A **pattern** is a Trino (Java) regular expression; a **format** is Trino's `date_parse` format.
- `fill`, `null_if`, `is_in` and `standardize` read their values as the column's type: a number as a number, a date column's
  value as `date '…'`, a timestamp's as `timestamp '…'`, anything else as text.
- `parse_date`, `parse_timestamp` and `parse_number` give null for a value they can't parse, rather than failing the step.
- A required argument left empty: "Round needs decimals". Too many: "Round takes 1 argument(s), not 2".

**Limits.** A prepare step applies at least one processor ("a prepare step applies at least one processor"). A processor
on a column the input hasn't: "Trim spaces: no column region in the step's input". An unknown processor: "processor 2: no
processor "titlecase"". What a processor can't say is a formula's job.

---

## The model step (Ask a model)

**What it's for.** A free-text column (a review, a complaint) becomes a labelled one: each row's value is sent to a model
through the platform's LLM gateway, on the project's key and budget, under the guardrail, and the answer is a new column.
Or each value is searched in one of the project's knowledge banks, and the closest passage is the new column.

**Who can do it.** Those with `build` add one on the canvas; any member may preview it, and the preview spends the
project's budget, recorded for whoever previewed. Saving needs `build`; the full run is the flow's DAG, as the project's
pipelines.

**What it needs.** The project's gateway key: the project has an LLM budget (`llm.budget` in its spec), and the operator
keeps its key as the connection `<project>-llm` ([AI](14-ai.md)). Without it: "the project has no gateway key: give it an
LLM budget (llm.budget in its spec) and the operator makes one". The `nearest` mode also needs a built knowledge bank.

### Set up

**Add a step → Ask a model**, then:

| Field on the canvas | JSON | What |
|---|---|---|
| Column | `column` | The column asked about |
| What to do | `mode` | `classify` (Classify into labels), `extract` (Extract), `summarize` (Summarize), `nearest` (Nearest passage in a knowledge bank) |
| Labels, comma-separated | `values` | `classify`: at least two labels, each a word or a few |
| What to extract / Instructions (optional) | `prompt` | `extract`: required, what to extract; `classify` and `summarize`: optional guidance |
| Knowledge bank | `bank` | `nearest`: the bank to search |
| Answer column | `name` | The new column's name; `nearest` adds `<name>_source` too |
| Rows a run, at most | `limit` | 1 to 1000; empty or 0 means 1000 |

```json
{"id": "topic", "kind": "llm", "inputs": ["masked"], "x": 660, "y": 0,
 "column": "text", "mode": "classify", "values": ["delivery", "sizing", "quality", "price", "service"],
 "prompt": "Delivery is about shipping and packaging; sizing about fit; quality about the product failing.",
 "name": "topic", "limit": 1000}
```

### The modes

| Mode | What the model is asked | The answer column |
|---|---|---|
| `classify` | "Classify each item below as exactly one of: <labels>." then your instructions | One of the labels (matched without regard to case), or null when the answer isn't one (counted as unread) |
| `extract` | "For each item below, extract what is asked." then your prompt | The text extracted, or null when there's nothing |
| `summarize` | "Summarize each item below in one sentence." then your instructions | One sentence |
| `nearest` | Not a question: each value is embedded through the gateway (the model named `embedding`) and the bank's closest passage found in Trino by cosine similarity | The passage, and `<name>_source`: where it's from (`table.column:ref`, or `files:<folder>:<key>`) |

The platform asks in batches of 20 rows: one question per batch, the values numbered, each cut at 2,000 characters, and
the model told to answer with a JSON array of strings, one per item, in order. A batch whose answer isn't such an array
leaves that batch's answers empty and says so ("rows 21 to 40: the answer has 19 items for 20 values; their answers are
empty"). Both answer columns are `varchar`.

### How it behaves: when the model is asked

The model is asked in exactly two cases:

1. **You preview the model step itself** (select it and press **Preview**). The platform runs the steps before it, as you,
   on a sample, takes the first 50 rows (fewer if the preview asks for fewer), and asks the model about them: at most three
   questions of twenty rows. The result shows the rows with the answer column, and a note: "The model was asked 3
   question(s) about these rows, 0.0042 spent, 1 answer(s) unread." The spend goes on the project's key, recorded for you.
2. **The flow runs.** The DAG the canvas writes asks first (one Airflow task per model step, `llm_<step>`), then builds the
   models with dbt. The task calls `POST /api/projects/{name}/flows/{flow}/steps/{step}/run` as the project's pipelines:
   the platform reads the step's input in full (no sample) up to the step's `limit`, asks the model about every row, and
   writes the rows with the answers as a table of the project's, `<flow>__<step>` (`review_topics__topic`), replacing the
   last run's. The step's dbt model reads that table. The audit log records `flow.llm`: "120 rows asked in 6 questions,
   0.0310 spent, 0 answers unread". The catalog gets the table's lineage from the flow's inputs.

Nothing else asks the model. Compiling doesn't. **Previewing a later step doesn't**: in a preview of any step after a model
step, the model step's answer column is there but empty (null), and the preview says "the column topic of the step topic is
empty in this preview: preview that step to ask the model". Opening, saving and reading the flow don't ask either.

### Limits

- The guardrail: a question carrying what looks like personal data or a secret is refused, and the whole preview or run
  fails, naming what was found: "asking the model: rows 1 to 20: the gateway refused (HTTP 400): the question holds an email
  address, which may not leave the platform; it wasn't sent to the model (the gateway's guardrail) (mask such values with a
  prepare step first)". Put a prepare step before the model step (`regex_replace` the addresses to `[email]`). See
  [Lab 18](../guide.md#lab-18-what-customers-say-sorted-by-a-model).
- The budget: a question the gateway refuses because the project's budget is spent fails the run the same way.
- At most 1,000 rows a run, and the run **keeps only the rows it asked about**: the step's table, and every step after it,
  has at most `limit` rows. Filter before the model step: "an llm step reads at most 1000 rows a run (filter first)".
- A model step's input is computed as a preview computes it, so a second model step that reads the first one's answer gets
  it empty: don't chain one model step's answer into another's question.
- The answer column's name must be new: "the input already has a column topic".
- `nearest`: "the project has no knowledge bank manuals", or "the bank manuals isn't built yet: build it from the project's
  Knowledge card" ([AI](14-ai.md)).

---

## Previews and what they cost

**What it's for.** Seeing a step's rows before anything is saved, cheaply, under your own rules.

**How it behaves.**

- **Preview** runs one query: the steps up to the selected one as `with` clauses, then the first rows of that step. It goes
  to Trino through the semantic layer, signed in as you, so your readers' row filters and masks apply: a reader sees what a
  reader may ([Access and protection](04-access-and-protection.md)).
- **Inputs are sampled.** An input of more than 50,000 rows is read with `tablesample bernoulli`, at the percentage that gives
  about 50,000 rows (never below 0.01%); smaller inputs are read whole. The note under the rows names the inputs that were
  sampled: "A sample of uploads_orders." A join or a group on a sample is a sample's answer, not the table's.
- The canvas shows 100 rows; the API takes `limit` up to 1,000 (more, or none, is 100).
- **SQL** shows the step's model as it will be saved, or, after a preview, the query the preview ran.
- A code step is previewed with its `config` removed and its `ref` and `source` calls turned into the preview's own names;
  other Jinja can't run there: "step x uses Jinja the canvas can't run (only ref, source and config); a preview can't show it".
- A metric step's SQL is the semantic layer's, asked as you: "its SQL isn't known yet (sign in, so the semantic layer can be
  asked as you)" when there's no one to ask as.

**What it costs.** A plain preview is one Trino query on about 50,000 rows of each input, as you (it counts against your
project's limits, [Operating the platform](17-operating.md)). A preview of a model step also spends the project's LLM budget:
up to three questions about 50 rows. Previewing other steps never asks the model. Previews are logged by platformd (who,
which step, how many rows, and a model step's cost), not recorded in the audit log.

---

## Saving a flow as a merge request

### Set up

Press **Save as merge request** (enabled when the flow compiles with no errors and the project has a repo). The canvas then
shows "on storscale/flow-campaign-results" and a **merge request** link.

### How it behaves

1. The flow is compiled again; a step that can't be compiled stops the save: "a step can't be compiled: filter_2: …"
   (HTTP 422, with every error).
2. The platform commits, with the repo's token, on the branch `storscale/flow-<name>` (underscores become dashes), made from
   the branch the pipelines follow (`pipelines.ref`, or the repo's default branch in GitLab when no ref is set):
   - each step's model with its header (a code step as written), `sources.yml` and `schema.yml`;
   - the DAG `<path>/flow_<name>.py`;
   - `<path>/dbt/dbt_project.yml`, if there isn't one;
   - the deletion of models of steps no longer in the flow.
   The commit message is "Flow campaign_results: 10 steps into campaign_results", built on the canvas by whoever saved.
3. It opens a merge request "Flow campaign_results" into the branch the pipelines follow, or updates the open one. Saving
   again updates the same branch and merge request.
4. The audit log records `flow.saved` ("campaign_results: 10 steps into campaign_results, on storscale/flow-campaign-results").
   A save refused for want of `build` is recorded as a refusal: "the project's editors save flows in it" (HTTP 403).

The merge request is reviewed like any other change: with reviews on, its dbt models are built on a data branch and the
merge request is told how it went ([merge-request reviews](09-pipelines-and-git.md#merge-request-reviews-on-data-branches)).
Merged, the project's Airflow loads the DAG within about a minute.

**The DAG.** `flow_<name>`, tagged `flow`, one run at a time, on the flow's schedule (UTC; none: by hand). It builds with the
platform's dbt helper, `dbt.build(project="retail", path="dbt", select="flows.campaign_results")`: `dbt build` on the flow's
models only, in Trino, as the project's pipelines, its checks run as dbt tests, its artifacts sent to the catalog (so the
table appears with its column lineage). With model steps, their `llm.run` tasks run first:

```python
# dags/flow_review_topics.py, as the canvas writes it (abridged)
from airflow.sdk import dag
from storscale import dbt, llm

@dag(dag_id="flow_review_topics", schedule="0 7 * * 1", start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False,
     max_active_runs=1, tags=["flow"])
def flow_review_topics():
    asked = [llm.run(project="retail", flow="review_topics", step="topic")]
    asked >> dbt.build(project="retail", path="dbt", select="flows.review_topics")
```

A failed run is one of the project's alerts, and the Pipelines tab shows why ([Pipelines and the project's repo](09-pipelines-and-git.md#running-pipelines-and-reading-run-logs)).

---

## Code steps: changing a flow by hand

**What it's for.** What the canvas can't say. An engineer opens the merge request (or the repo), changes a model's SQL, and
pushes. The canvas shows that step as **Code** from then on, and the rest as steps.

**How it behaves.**

- A model is a step while the hash in its header matches the SQL under it. Change the SQL (or remove the header) and it's a
  code step: its SQL is kept exactly as written, and the canvas never rewrites it.
- A code step's inputs are its `{{ ref('…') }}` calls to models of the same folder. A ref to a model outside the folder
  stops the flow from opening: "model x refers to y, which isn't in the flow's folder: the canvas reads only its own models".
- A code step's columns are what Trino says its query returns, asked as you when the flow is compiled. Until then it shows
  "columns unknown", and so do the steps after it.
- A code step named without the flow's prefix and not marked `ephemeral` in its `config` is the flow's output, written by hand.
- On the canvas a code step's SQL can be edited in its settings; **Add a step** doesn't offer the kind. Through the API, a
  step `{"kind": "code", "sql": "…"}` is accepted.

See [Lab 10](../guide.md#lab-10-the-project-as-code-with-a-git-repo), step 1.

---

## The flows API

All routes take the web app's session or a Keycloak token (`Authorization: Bearer …`); with a session, a change also needs
the header `X-Platform-Request: 1`. A flow is sent as `{"flow": {name, schedule, steps}}`.

| Method and path | What | Who |
|---|---|---|
| `GET /api/projects/{name}/flows` | The flows: `{flows: [{name, branch, mergeRequest}], dbtPath, target}`, or `{flows: [], why}` without a repo | Members |
| `GET /api/projects/{name}/flows/{flow}` | One flow read from the repo and compiled: `{flow, order, steps, errors, branch, mergeRequest, dbtPath, processors}` | Members |
| `POST /api/projects/{name}/flows/compile` | Compile a flow as sent: each step's `model`, `columns`, `sql`, `error`, `unknown`; nothing runs | Members |
| `POST /api/projects/{name}/flows/preview` | `{flow, step, limit}`: a step's rows on a sample, as you: `{sql, columns, types, rows, seconds, sampled, notes}`; a model step's adds `cost`, `batches`, `unread` | Members |
| `PUT /api/projects/{name}/flows/{flow}` | Save as a merge request: `{branch, mergeRequest, new, files, dag}` | `build` |
| `POST /api/projects/{name}/flows/{flow}/steps/{step}/run` | Ask the model about a saved flow's model step, every row up to its limit, and write its table: `{table, rows, batches, cost, unread, warnings}` | `build`, the project's pipelines, administrators |

A complete flow, as Lab 9 builds it, saved with `PUT /api/projects/retail/flows/campaign_results`:

```json
{"flow": {"name": "campaign_results", "schedule": "0 6 * * 1", "steps": [
  {"id": "input", "kind": "input", "table": "uploads_campaign_responses", "x": 30, "y": 30},
  {"id": "filter", "kind": "filter", "inputs": ["input"], "where": "converted", "x": 220, "y": 30},
  {"id": "input_2", "kind": "input", "table": "uploads_orders", "x": 30, "y": 120},
  {"id": "join", "kind": "join", "inputs": ["filter", "input_2"], "join": "inner",
   "on": [{"left": "customer_id", "right": "customer_id"}], "x": 410, "y": 30},
  {"id": "filter_2", "kind": "filter", "inputs": ["join"],
   "where": "not test and not refunded and ordered_on >= responded_on and ordered_on <= date_add('day', 30, responded_on)", "x": 600, "y": 30},
  {"id": "input_3", "kind": "input", "table": "uploads_campaigns", "x": 410, "y": 120},
  {"id": "join_2", "kind": "join", "inputs": ["filter_2", "input_3"], "join": "inner",
   "on": [{"left": "campaign_id", "right": "campaign_id"}], "x": 790, "y": 30},
  {"id": "group", "kind": "group", "inputs": ["join_2"], "by": ["name", "budget"],
   "aggregates": [{"function": "sum", "column": "amount", "as": "revenue"},
                  {"function": "count_distinct", "column": "customer_id", "as": "buyers"},
                  {"function": "count", "as": "orders"}], "x": 980, "y": 30},
  {"id": "formula", "kind": "formula", "inputs": ["group"],
   "columns": [{"name": "roi", "expression": "revenue / budget", "type": "double"}], "x": 1170, "y": 30},
  {"id": "prepare", "kind": "prepare", "inputs": ["formula"],
   "processors": [{"column": "roi", "do": "round", "args": ["2"]}], "x": 1360, "y": 30},
  {"id": "out", "kind": "output", "inputs": ["prepare"], "table": "campaign_results",
   "checks": [{"column": "name", "test": "not_null"}, {"column": "name", "test": "unique"}], "x": 1550, "y": 30}
]}}
```

A preview of one step of it: `POST /api/projects/retail/flows/preview` with `{"flow": {…}, "step": "join", "limit": 100}`.

---

## Settings

| Setting | Chart value (default) | Compose | What it changes |
|---|---|---|---|
| The semantic layer | always installed (semanticd) | `SEMANTIC_URL` on platformd | Runs every preview and a metric step's SQL; without it: "the semantic layer isn't set up (no SEMANTIC_URL), which runs the previews" |
| The LLM gateway | `llm.gateway` (`stub`) | `stub` | `stub` answers without a model (a lab's install), `litellm` asks the company's models, `""` gives no keys, so model steps can't run ([AI](14-ai.md)) |
| The guardrail | `llm.guardrails` (`true`), `llm.forbiddenTerms` (`[]`) | the same | What a model step's questions may not carry |
| The branch a repo is followed on | `pipelines.defaultRef` (`main`) | `PIPELINES_DEFAULT_REF` on the operator (`main`) | Which branch Airflow builds flows from when the project sets no `pipelines.ref` (see the gotcha below) |

The preview's sample (about 50,000 rows an input), a model step's preview (50 rows), its batch (20 rows) and its run
(1,000 rows at most) are fixed in the code, not settings.

---

## Limits and gotchas

- **Saving needs an https repo with a token.** A repo over SSH runs pipelines, but the canvas can't commit to it: "the
  project's repo needs an https address and a token (its connection) for the platform to commit to it". The connection
  needs the token in its `password`: "the repo's connection has no token". The token needs GitLab's `api` scope and the
  Developer role; a weaker one gets HTTP 403 from GitLab's API.
- **Set `pipelines.ref`.** Without it, the canvas saves into the repo's default branch in GitLab, while Airflow follows the
  install's default (`main`) and reviews watch merge requests into `main`. A repo whose default branch is another (`master`)
  then has flows merged where nothing builds them. Name the branch in `pipelines.ref`.
- **Delete the flow's branch when its merge request merges** (GitLab's "Delete source branch"). While `storscale/flow-<name>`
  exists, the canvas opens the flow from it, not from the branch the pipelines follow, so a later change made on that branch
  in the repo isn't what the canvas shows.
- **Previews are samples.** A large input is read at about 50,000 rows, so a join's or a group's preview numbers aren't the
  table's. The note names the sampled inputs.
- **One output.** A flow makes one table; a second output step: "a flow has one output step; this one has 2".
- **Names.** A flow's, a step's and a new column's name are lowercase letters, digits and underscores, starting with a
  letter. The name in the address must be the flow's: "the flow's name is lowercase letters, digits and underscores, and the
  one in the address".
- **Schedule.** Cron's five fields, UTC: "a schedule as cron's five fields (UTC): 0 6 * * 1, or none".
- **Inputs from other projects** are only the tables of data products the project consumes.
- **No Python step yet**, and no Spark: every step is Trino SQL (or a model's answers written as a table).
- **Unsaved work is lost** when you leave the canvas.

See [Troubleshooting](18-troubleshooting.md) for more of the errors, and [Lab 18](../guide.md#lab-18-what-customers-say-sorted-by-a-model)
for a model step end to end.
