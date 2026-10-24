# 7. Dashboards, apps and assistants

The semantic model ([Metrics and business terms](06-metrics-and-terms.md)) defines revenue once; this chapter is about the
places people read it. Superset's dashboards, on datasets the platform makes from the models; apps, small pages a project's
builders declare for its readers; and assistants (Claude Code, a company's own) that ask the platform over MCP. Each of them asks
as the person using it, so each shows what that person's rules allow. The last section is the platform's API reference, for those
who'd rather call it themselves. It's for analysts who build charts and apps, engineers who connect assistants, and the
administrators who register them.

In this chapter:

- [Dashboards (Superset)](#dashboards-superset)
- [Superset's datasets made from the models](#supersets-datasets-made-from-the-models)
- [Apps](#apps)
- [Assistants over MCP](#assistants-over-mcp)
- [Registering an assistant](#registering-an-assistant)
- [The API reference](#the-api-reference)
- [Settings](#settings)

---

## Dashboards (Superset)

**What it's for.** Charts and dashboards for the people who read the numbers, built in a tool made for it (Apache Superset), on
the same tables and the same definitions as everything else, with each person's rules applied to what each chart shows them.

**Who can do it.** Superset lets in the members of two Keycloak groups and turns each into Superset roles at every sign-in:

| Keycloak group | Superset roles | What that gives |
|---|---|---|
| `analysts` | Gamma, `sql_lab`, Lakehouse SQL | SQL Lab and the lakehouse database, and what Superset's Gamma role allows; not making datasets |
| `engineers` | Alpha, `sql_lab`, Lakehouse SQL | The same, with Superset's Alpha role: making datasets, charts and dashboards |

Anyone in neither group is refused at sign-in. Superset's own roles decide the rest (who may edit whose dashboard); the platform
doesn't change them.

### Set up

- **A project's Dashboards tab**, or **Tools → Dashboards**: Superset's dashboard list, in the platform's frame, signed in with
  your Keycloak session (no second sign-in). **Open in a new tab ↗** on the frame's bar opens it on its own; **Reload** reloads it.
- **Tools → SQL**: Superset's SQL Lab, for SQL against the lakehouse.
- Superset is served at the platform's `/dashboards/`.

Build a chart as Superset's documentation says: **Datasets**, pick one (those the platform made are called `<model> (semantic)`,
below), make a chart, save it to a dashboard.

### How it behaves

- Superset reaches Trino through one database, **Lakehouse (Trino)**, as the service user `superset` with impersonation on: every
  query runs in Trino **as the person viewing the chart**, so Ranger's policies, row filters and masks apply, and Ranger's audit log
  names them. As alice, a chart of revenue by region shows the EU only. The database doesn't allow DML.
- Charts are Superset's: the platform keeps no copy of them and never changes them.

See [Lab 7](../guide.md#lab-7-dashboards-on-the-same-definitions).

### Limits and gotchas

- **Two groups only.** The groups Superset lets in (`analysts`, `engineers`) and their roles are fixed in
  `stack/superset/superset_config.py`; another group's people can't sign in to Superset, whatever their projects.
- **Dashboards aren't per project.** The Dashboards tab opens Superset's own list; what a chart shows is decided by Trino's rules for
  the viewer, not by which project's tab it was opened from.
- **Power BI** on the same definitions: not yet.

---

## Superset's datasets made from the models

**What it's for.** A chart built on raw SQL brings back the three revenues. The platform makes a Superset dataset from each
semantic model, with its metrics already defined, so a chart's `revenue` is the semantic layer's revenue, and follows when the
definition changes.

**Who can do it.** Nobody sets it up: a service beside Superset, `superset-sync`, does it for every project's model. Anyone who may
use Superset may chart on them.

### How it behaves

Every minute, `superset-sync` reads every project's model and keeps one Superset dataset per semantic model:

| Part | What it is | For `retail`'s model `orders` ([the example](06-metrics-and-terms.md#a-complete-example)) |
|---|---|---|
| Name | `<semantic model> (semantic)`, in the table's schema | `orders (semantic)`, schema `retail` |
| Columns | Each dimension, named `<primary entity>__<dimension>` as MetricFlow names it (time dimensions marked as times); each measure, under its own name; through each foreign entity, the dimensions of the model where that entity is primary, as `<entity>__<dimension>` | `order__region`, `order__channel`, `order__ordered_on`; `amount_sum`, `orders`, `refunds`, `customers` |
| Metrics | Each simple metric of a measure of this model, as its aggregate; each ratio of two such metrics, as one over the other | `revenue` = `SUM("amount_sum")`, `order_count`, `refund_count`, `customer_count`, `average_order_value` = `CAST(SUM("amount_sum") AS DOUBLE) / NULLIF(SUM("orders"), 0)` |
| Description | The model's description, then "From the semantic model of retail, kept by the platform: don't change it here." and, when some of its metrics can't be said in a dataset, "Ask the semantic layer for …" naming them | |

The dataset's SQL is a view over the model's table (and the tables it joins to), so its numbers are read in Trino as the viewer,
like any other chart.

- **Kept in step.** A changed model changes its dataset within a minute (`SYNC_EVERY`, 60 seconds). A model removed, or renamed,
  removes the dataset made from it. Datasets people make themselves are never touched.
- **Shared models.** A project that builds on a shared model ([sharing](06-metrics-and-terms.md#sharing-a-model-and-building-on-another-projects))
  gets datasets for its own semantic models; its foreign entities join to the shared models' too, and ratios of the shared
  metrics are defined where both measures are the dataset's own model's.

### Limits and gotchas

- **Don't edit a platform dataset.** The next change to its model replaces its SQL, columns and metrics. Make a dataset of your own
  (as an engineer) for anything else.
- **Only simple metrics and ratios of them.** A metric whose measures aren't all the model's own (a ratio across models), or any
  other kind (derived, cumulative, conversion), stays the semantic layer's: ask it on the Metrics tab, in an app, or through an
  assistant. The dataset's description names those metrics.
- **A renamed semantic model is a new dataset.** Charts on the old one lose it: keep a model's name once charts use it.
- **`metric_time` isn't a column.** Group a dataset by the model's own time dimension (`order__ordered_on`) and Superset's time grain.

---

## Apps

**What it's for.** The regional leads need one number by region, not Superset. An app is a small page of the project's, declared
rather than built: a metric (or a few) by a dimension, as bars, a line, one number or a table; a table's rows; or questions answered
from a knowledge bank. Its data is fetched as whoever opens it, so one app serves every audience: alice sees the EU, bob every region.

**Who can do it.** Making, changing and removing apps takes the permission `build` (editors have it). Every member of the project
opens them; they're on the project's **Apps** tab and on each member's **Home**, under **Apps**.

### Set up

On the project → **Apps** → **Make an app**:

| Field | What it is |
|---|---|
| **Name** | Lowercase letters, digits and dashes, starting with a letter, up to 41 characters (`revenue-by-region`) |
| **Title** | What the page and the card say (default: the name) |
| **What it's for** | A line under the title |
| **What it shows** | **Metrics, by a dimension**; **A table's rows**; or **Questions, answered from a knowledge bank** (offered when the project has a knowledge bank and an LLM budget) |
| **Metrics** | (metrics) The metrics, as chips: each is its own chart, on its own scale |
| **By** | (metrics) A dimension, or **nothing (the total)** |
| **As** | (metrics) **Bars**, **A line**, **One number** or **A table** |
| **Table**, **Columns (none: all)** | (table) The table, and the columns shown |
| **Knowledge bank** | (questions) The bank its answers come from |
| **Filters** | (metrics, table) **Add a filter**: a dimension (or a column), a label, and the values to pick from (none: the viewer types one) |

Press **Make it**. **Remove** on an app's card takes it out.

**In the project's spec**, the same apps are `apps`:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "reader"}],
    "tables": {},
    "llm": {"budget": 20},
    "knowledge": [{"name": "reviews", "tables": [{"table": "uploads_reviews", "column": "text", "id": "review_id"}]}],
    "apps": [
      {"name": "revenue-by-region", "title": "Revenue by region", "kind": "metric", "metrics": ["revenue"], "by": "order__region",
       "show": "bar", "filters": [{"field": "order__region", "label": "Region", "values": ["EU", "US", "APAC"]}]},
      {"name": "revenue-total", "title": "Revenue", "kind": "metric", "metrics": ["revenue"], "show": "number"},
      {"name": "orders", "title": "The orders", "kind": "table", "table": "uploads_orders",
       "columns": ["order_id", "region", "amount"], "filters": [{"field": "region", "label": "Region"}], "limit": 200},
      {"name": "ask-reviews", "title": "What customers say", "kind": "qa", "bank": "reviews"}
    ]
  }
}
```

| Field | Kinds | What it is |
|---|---|---|
| `name` | all | Required: `^[a-z][a-z0-9-]{0,40}$` |
| `title`, `description` | all | What the page says |
| `kind` | all | `metric`, `table` or `qa` |
| `metrics` | metric | At least one: the project's metrics, or the shared ones it builds on |
| `by` | metric | A dimension (`order__region`, `metric_time__month`); required for `bar` and `line` |
| `show` | metric | `table`, `bar`, `line` or `number` (one metric, by nothing); a table app shows `table` |
| `table`, `columns` | table | One of the project's tables, by its bare name, and the columns shown (none: all) |
| `filters` | metric, table | Each `{field, label, values}`: a dimension (metric) or a column (table) equal to the value the viewer picks; `values` up to 200 characters each |
| `bank` | qa | One of the project's knowledge banks (`knowledge`) |
| `limit` | metric, table | Rows shown, at most; default 500, up to 5000 |

**Through the API:**

| Method and path | What it does | Who |
|---|---|---|
| `GET /api/projects/{name}/apps` | The project's apps, and whether you may make them | Members |
| `PUT /api/projects/{name}/apps/{app}` | Makes an app, or changes one: the app as JSON (the fields above, without `name`) | `build` |
| `DELETE /api/projects/{name}/apps/{app}` | Takes it out | `build` |
| `POST /api/projects/{name}/apps/{app}/data` | Its rows, as you: `{"filters": {"order__region": "EU"}}` | Members |
| `POST /api/projects/{name}/apps/{app}/ask` | A question app's answer, as you: `{"question": "..."}` | Members |

```bash
curl -s -X PUT http://storscale.localhost:8800/api/projects/retail/apps/revenue-by-region \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"title": "Revenue by region", "kind": "metric", "metrics": ["revenue"], "by": "order__region", "show": "bar"}'
```

### How it behaves

- **Fetched as the viewer.** platformd fetches the data with the viewer's own token. A metric app asks the semantic layer (the
  filters become conditions on the dimensions, the rows ordered by `by`); a table app runs
  `SELECT <columns> FROM <table> WHERE CAST(<column> AS varchar) = '<value>'` in Trino. Ranger's rules apply either way.
- **The page.** Filters above the chart, applied with **Show**; **As a table** / **As a chart** switches a metric app's view; a
  number shows large, with its metric's name. "Nothing to show" when the rules or filters leave no rows.
- **A question app** embeds the question through the gateway (the install's embedding model, on the project's key), finds the five
  closest passages in the bank's table (`knowledge_<bank>`), read as the person asking, and has the model answer from those passages
  alone, citing them by number, or say they don't answer it. The answer lists the passages under **From:**, each with its source
  and row. It spends the project's LLM budget, under the guardrail ([AI](14-ai.md)).
- **Audited.** The platform's audit log records `app.saved` (made or changed, with its kind), `app.removed`, and each question
  asked (`app.asked`, with how many passages and what it spent).

See [Lab 7](../guide.md#lab-7-dashboards-on-the-same-definitions), step 4, and [Lab 18](../guide.md#lab-18-what-customers-say-sorted-by-a-model)
for the knowledge bank.

### Limits and gotchas

- **A table app made on the page is refused today.** The form's **Table** list offers the tables the semantic model reads, in full
  (`iceberg.retail.uploads_orders`), and a table app's `table` is the bare name, so saving says
  `table "iceberg.retail.uploads_orders": one of the project's tables, by its name`. Make a table app through the API or the spec
  with the bare name (`uploads_orders`). The list also lacks tables no semantic model reads.
- A filter with `values` refuses any other value: "Region is one of EU, US, APAC".
- A metric app needs the project's tables ("a metric app needs the project's tables, where its metrics are"); a bar or line needs
  `by` ("a bar chart is by a dimension (by)"); a number is one metric by nothing ("a number is one metric, by nothing").
- A question app needs a bank of the project's (`a question app answers from one of the project's knowledge banks (bank): "x" isn't
  one`) and an LLM budget ("give the project an LLM budget (llm.budget)"); asked before the bank is built, it says "the bank reviews
  isn't built yet: build it on the Agents tab's Knowledge card".
- Apps aren't shared outside the project: a person sees an app only as a member of its project.

---

## Assistants over MCP

**What it's for.** The CEO asks in a chat window instead of waiting for Thursday's deck. The platform's MCP server lets an assistant
(Claude Code, Claude, a company's own) answer from the same definitions, as the person using it: it finds the business terms, the
metrics and the data products, asks for the numbers under that person's rules, and says what it used. It can't see what the person
can't, and every query is audited with both their names.

**Who can do it.** Anyone signed in, through an assistant the platform knows: the realm's built-in client `storscale-agent` (for
Claude, IDEs and scripts), or one an administrator registered ([below](#registering-an-assistant)). The answers are the person's:
carol, a member of no project, gets no projects and no answers.

### Set up

The MCP server is at `<the platform's address>/mcp` (streamable HTTP). With Claude Code:

```bash
claude mcp add --transport http storscale http://storscale.localhost:8800/mcp
```

Claude Code finds the sign-in from the server (an OAuth resource server whose metadata names Keycloak); a browser opens on
Keycloak's sign-in; sign in as yourself. The project's **Metrics** tab shows the command with your install's address, on its
**Agents** card.

Other clients sign in the same way: OAuth's authorization code with PKCE, through the public client `storscale-agent`, whose
redirect addresses are `http://localhost:*`, `http://127.0.0.1:*` and `https://claude.ai/api/mcp/auth_callback`. A client that
needs another redirect address is registered as an assistant of its own.

### The tools

| Tool | Arguments | What it does |
|---|---|---|
| `search_terms` | `question` | The business terms a question is about, by their names, labels and synonyms; with a note to say so when none matches |
| `list_terms` | | Every business term: definition, owner, synonyms, tables, the projects using it |
| `describe_term` | `term` | One term, by name or synonym |
| `list_projects` | | The projects you're a member of, your role in each, and whether each has metrics |
| `list_metrics` | `project` | A project's metrics, each with its description and dimensions |
| `query_metrics` | `project`, `metrics`, `group_by`, `where`, `order_by`, `limit` (100) | Metrics computed as you; with `used`: what it used |
| `explain_query` | `project`, `metrics`, `group_by`, `where` | The SQL a query would run, without running it |
| `list_products` | | The data products, certified first: producer, owner, version, terms, contract, consumers, certification |
| `describe_product` | `project`, `product` | One data product |
| `query_product` | `project`, `product`, `columns`, `where`, `limit` (100) | Rows of a product's table, as you, under the producer's rules |
| `list_runs` | `project`, `limit` (20, up to 200) | A project's pipelines and their latest runs, and the repo's files Airflow couldn't load |
| `list_alerts` | `project` | A project's open alerts: failed runs and checks, stale tables, broken contracts, budgets |
| `search_documents` | `project`, `bank`, `query`, `k` (5) | A knowledge bank searched by meaning, each passage with its source and score |
| `list_tools` | `project` | A project's own tools you may call, and its agents you may ask |
| `call_tool` | `project`, `tool`, `arguments` | Calls one of a project's own tools |
| `ask_agent` | `project`, `agent`, `task` | Asks one of a project's agents to do a task; it may take minutes |

The server's instructions tell an assistant to start from `search_terms`, then the projects, metrics and dimensions, then query;
to name the definitions, terms and products each answer used; and to say so when no term, metric or product covers a question
rather than guess. The project tools and agents (`list_tools`, `call_tool`, `ask_agent`) are [AI](14-ai.md)'s.

Each `query_metrics` answer carries `used`: the metrics' definitions (and the project each is defined in, with a link to its
Metrics tab), the terms on what it asked for, and the data products behind the tables it read. Each `query_product` answer
carries the product and its terms.

### How it behaves

- **Signed in as the person.** The server accepts only Keycloak tokens of the realm `lakehouse` whose audience names the MCP
  server's address and Trino (`lakehouse`); any other is refused with 401. The person's projects are those whose members list them.
- **Asked as the person.** A query runs in Trino with the person's own token, so their row filters and masks apply. Its leading
  comment names the client and the person (`agent=storscale-agent user=alice`), and Ranger's audit log keeps it: the project's
  **Audit** tab lists the assistant's queries with both names. `query_product` runs the same way; a person who is neither a reader
  of the producer nor of a consuming project is refused, and told where their project can ask for the product.
- **A project's own agents** (clients `agent-<project>-<name>`, [AI](14-ai.md)) get only the tools their registration lists, and
  none until approved: "the agent may use: nothing yet (not approved)". Assistants and `storscale-agent` get every tool.
- **What's open to everyone signed in:** the terms and the list of data products (what exists is no secret; reading it is).

See [Lab 8](../guide.md#lab-8-ask-an-assistant).

### Limits and gotchas

- An answer has at most 1000 rows.
- The Metrics tab's **Agents** card lists four tools (`list_projects`, `list_metrics`, `query_metrics`, `explain_query`); the server
  has all sixteen above.
- MCP's dynamic client registration (a client registering itself) isn't offered: the company approves its assistants.
- A tool's refusal reaches the assistant in words ("you're not a member of retail", "no business term is called …; list_terms has
  them all"), which it can tell the person.

---

## Registering an assistant

**What it's for.** An assistant the company runs (DevAssist, say) should sign people in as itself, so the audit log says
"DevAssist, for alice" and the company can switch it off. Registering it gives it a Keycloak client of its own.

**Who can do it.** Administrators.

### Set up

**Administration → Assistants** → **Register an assistant**:

| Field | What it is |
|---|---|
| **Name** | 3 to 40 lowercase letters, digits and dashes, starting with a letter (`devassist`) |
| **What it is (optional)** | A description |
| **Redirect URIs, space-separated** | Where Keycloak may send the person back after they sign in: `https://` addresses, or `http://localhost:*` / `http://127.0.0.1` for one on the person's own machine; a trailing `*` is allowed |

Press **Register**. The table lists each assistant with its **Client ID** (`assistant-<name>`), the **MCP server** address to give
it, its redirect URIs, and who registered it when; **Remove** takes it away.

**Through the API**, administrators:

| Method and path | What it does |
|---|---|
| `GET /api/assistants` | The registered assistants, each with `clientId` and `mcpUrl` |
| `PUT /api/assistants/{name}` | Registers one, or changes it: `{"description": "...", "redirectUris": ["..."]}` |
| `DELETE /api/assistants/{name}` | Removes it |

```bash
curl -s -X PUT http://storscale.localhost:8800/api/assistants/devassist \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"description": "The company coding assistant", "redirectUris": ["https://devassist.example.com/callback"]}'
```

### How it behaves

- platformd keeps the registration in its store (`assistants/<name>.json`); the operator, on its next pass, makes the Keycloak
  client `assistant-<name>` as `storscale-agent` is made: public, PKCE (S256), the person's groups in its tokens, and tokens for the
  MCP server and Trino. The assistant then signs people in with OAuth's authorization code flow and calls `<platform>/mcp`.
- The client's ID is the agent's name everywhere: in each query's comment and Ranger's audit log.
- Removing it deletes the client: it signs no one in from then on, and the tokens it had end within Keycloak's token lifetime.
- The platform's audit log records `assistant.register` (with the redirect URIs) and `assistant.remove`.

### Limits and gotchas

- "an http redirect URI is for localhost only; elsewhere it's https"; "an assistant needs at least one redirect URI".
- Changing an assistant's redirect URIs updates its client; its other settings stay as the operator made them.
- An install without a file store can't register assistants: the page says so, and the API answers "this install has no file store
  for assistants".

---

## The API reference

**What it's for.** Everything the web app does is platformd's API, and the same calls serve scripts, the Python package and the
MCP server. The reference lists every route, so a developer can call them without reading the code.

**Who can do it.** Anyone may read the reference (it says what exists, not what's in it). Each call runs as whoever signed in,
under their projects' rules.

### Using it

- **On the platform:** the account menu (your name, top right) → **API reference**. Routes are grouped by what they're about
  (apps, products, semantic, …), with a search box; each opens to its description and its **Operation ID**.
- **As a document:** `GET /api/openapi.json`, OpenAPI 3.1, for any client generator. It's made from platformd's routes and their
  handlers' comments, and a test keeps it current, so a route can't be added without it.
- **Signing in:** `Authorization: Bearer <token>`, a Keycloak access token of the realm `lakehouse` (a person's, through
  `storscale-agent`, or a service account's); or the web app's session. Every change (any method but `GET`) also sends
  `X-Platform-Request: 1`.
- **Errors** are JSON: `{"error": "<why>", "request": "<id>"}`, with the same ID in the `X-Request-ID` header; quote it to an
  administrator ([Troubleshooting](18-troubleshooting.md)).
- **From Python**, the `storscale` package: `p.call("<operation ID>", body, **path_parameters)` calls any route; `p.operations()`
  lists them ([Operating the platform](17-operating.md)).

```python
import storscale

p = storscale.Platform("http://storscale.localhost:8800")   # asks for your name and password, or reads STORSCALE_TOKEN
p.call("post_projects_name_semantic_query", {"metrics": ["revenue"], "group_by": ["order__region"]}, name="retail")
```

---

## Settings

| Setting | Chart value (`deploy/helm/storscale-platform/values.yaml`) | Compose | Default |
|---|---|---|---|
| A base map for Superset's map charts | `superset.mapboxApiKey` | `MAPBOX_API_KEY` | empty: map layers on no base map |
| Superset's pods | `replicas.superset` | one container | 1 |
| Superset's Keycloak client secret, session key, database and Trino passwords | made by the chart | `SUPERSET_OAUTH_SECRET`, `SUPERSET_SECRET_KEY`, `SUPERSET_DB_PASSWORD`, `SUPERSET_TRINO_PASSWORD` in `stack/.env` | change the examples' values |
| How often datasets follow the models | not a chart value | `SYNC_EVERY` on `superset-sync` | 60 seconds |
| `semanticd`'s pods (the MCP server) | `replicas.semanticd` | one container | 1 |
| The MCP server's address | the platform's address + `/mcp` | `MCP_URL` on semanticd | `http://storscale.localhost:8800/mcp` locally |
