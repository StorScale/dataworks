# StorScale Platform: a lab guide

A walk through the platform with a small company's data, for anyone who will use it: analysts, data engineers, the people who own a project, and whoever decides whether to run it. Each lab takes ten to twenty minutes, says what the business gets out of it, and ends with something you can check. The labs build on each other; the whole guide is an afternoon.

**What you need.** The platform running: on your machine, `storscale up`, then `http://storscale.localhost:8800` (the [README](../README.md) says how); or your company's install, at its own address. Three people exist in the example sign-in: **alice**, an analyst; **bob**, an engineer, who also administers the platform; and **carol**, who has no access. Their password is `LAKEHOUSE_USER_PASSWORD` in `stack/.env` on a local install. Two browser windows, or one normal and one private, make switching between alice and bob easy.

**The company.** Harbor Outfitters sells outdoor gear in three regions through its web shop, its stores and partners, and runs marketing campaigns. Its data, four CSV files in [`docs/lab/data/`](lab/data/) (made by [`make_data.py`](lab/make_data.py), so the numbers below hold):

| File | What | Rows |
|---|---|---|
| `customers.csv` | Customers: names, email addresses and phone numbers (personal data, on purpose), region, segment | 60 |
| `orders.csv` | Orders from July to September 2026: region, channel, amount, a few refunds, five test rows | 305 |
| `campaigns.csv` | Six marketing campaigns with budgets | 6 |
| `campaign_responses.csv` | Who responded to which campaign, and whether they bought | 180 |

The questions the company asks of it: revenue by region and week; which campaigns pay for themselves; which customers are worth a call; and, from its privacy officer, where the personal data is and who may see it.

---

## Lab 1: Sign in, and see what each person sees

**Value:** one sign-in for every tool, and each person sees exactly what their role allows. No shared passwords, no tool-by-tool accounts.

1. Open the platform as **alice**. Home greets her and lists her tools (SQL, notebooks, dashboards, pipelines, the catalog, monitoring) and her projects: `sales`, where she's a reader.
2. Open the **Tools** menu and open SQL, then Dashboards: no second sign-in. The platform signed her in to each tool.
3. Open `sales`. The tabs are the project's pages: Overview, Data, Metrics, SQL, Notebooks, Pipelines, Flows, Agents, Dashboards, Tags, Imports, Connections, Access. On **Data**, open `orders` and read the note above its columns: as a reader she sees only EU rows, and card numbers show their last four digits. Open **Preview**: that's what she gets.
4. Sign out. Sign in as **carol**: "No access". She isn't in a group the platform admits.
5. Sign in as **bob**. He has the same tabs plus Audit, an **Administration** menu, and on `sales` he's an editor.

**Check:** alice's preview of `orders` has only `EU` in the region column; bob's has all three regions.

---

## Lab 2: A project for the retail team

**Value:** a project is the unit of ownership. Its members, its tables, who may read what, and where it's told about problems are declared once, and every tool follows: Trino, the buckets, the catalog, Airflow, dashboards, notebooks, agents.

As **bob**, Administration → Projects → **New project**:

1. Name `retail`, description "Harbor Outfitters: orders, customers, campaigns".
2. Members: the group `engineers` as editors; the group `analysts` as readers; and the user `alice` as an editor as well (Lab 10 needs two editors; she keeps reading `sales` as before).
3. Tick **Tables** (the project gets a namespace, `retail`, in the lakehouse) and **Files** (a bucket of its own, where uploads land).
4. Under the readers' rules, add a row filter on `orders`: `region = 'EU'`, for the EU analysts' team. Leave masks empty: Lab 4 shows a better way.
5. Save. Within a minute the Overview says **Ready**, and the Access tab shows who got what: the readers' and editors' groups in Keycloak, Ranger's policies on the namespace, the bucket's policies.

Also open **Edit** on the project and, in the JSON box, add under `tables`:

```json
"people": [{"table": "uploads_customers", "column": "customer_id"}]
```

This declares which table holds people and which column identifies one. The platform uses it for the privacy labs (4 and 9).

**Check:** on the Access tab, `alice` is an editor of `retail` and `analysts` are readers; on the Audit tab, the project's creation is the first entry, with who did it.

---

## Lab 3: Get the data in

**Value:** a file becomes a governed table in minutes, with no engineer writing a loader; the same path takes applications (Salesforce) and databases.

As **bob**, open `retail` → **Flows** → the **Uploads** panel at the bottom.

1. **Upload a file**: `customers.csv`. The upload lands in the project's landing area, as bob (the bucket's policy decides who may write there), and the panel shows a preview: the columns and the types the platform read from the first rows. `customer_id` is a bigint, `email` a varchar, `signed_up` a date.
2. Press **Import as a table**, accept the name `customers`. The project gains an import called `uploads`, which reads the landing area into the table `uploads_customers`. Do the same for `orders.csv`, `campaigns.csv` and `campaign_responses.csv`: each joins the same import.
3. Open **Pipelines**: `import_retail_uploads` is there, scheduled hourly. Press **Run now** and watch the run; it takes a minute. (A run that fails says why on this page: "why it failed" next to the run.)
4. Open **Data**: four tables, `uploads_customers`, `uploads_orders`, `uploads_campaigns`, `uploads_campaign_responses`, with their columns, from the catalog. Open **Preview** on `uploads_orders`.
5. Open **Imports**: the import's definition, and the same place a Salesforce connector would be added from a form.

**Check:** on the SQL tab, as bob, `SELECT count(*) FROM uploads_orders` gives 305; as alice, the same query gives the EU rows only (about a third), because the readers' rule from Lab 2 applies to every table the project has, this one included.

---

## Lab 4: Personal data, found and protected

**Value:** the customers' emails and phone numbers were uploaded in Lab 3 without anyone writing a policy for them. The catalog finds them, and the platform masks them for everyone but the groups the company excepts, everywhere Trino is: SQL, dashboards, notebooks, deliveries, assistants.

1. Wait a few minutes after the import (the catalog describes new tables every minute and classifies every two), then open **Data** → `uploads_customers` as bob. The **Protection** column shows `personal (suggested)` on `email` and `phone`, found by their names and by what their values look like; and `personal` confirmed on `customer_id`, because Lab 2 declared it.
2. The project's Overview lists an alert: "email looks personal: the catalog tagged it, and it's masked". The owner looks, and either confirms or removes the tag.
3. As **alice**, run on the SQL tab: `SELECT first_name, email, phone FROM uploads_customers`. The email and phone columns are hashed. Nobody wrote a policy; the tag did it.
4. As **bob**, confirm `email` (the tag stays whatever the catalog finds later) and, to see the other direction, remove the tag on `phone`. Within a minute alice reads phone numbers again, and the catalog doesn't suggest it back.
5. Open the catalog (Tools → Catalog) and find `uploads_customers`: the same tags, under the **Protection** classification, with their state. The catalog's own page is another place to confirm or correct them.

What a tag means is set once per install: `personal` is hashed, `financial` masked, `health` nulled, each with the groups that see the column as it is (on a local install, the engineers). The privacy officer sets that; project owners never write masks for personal data again.

**Check:** alice's query shows 64-character hashes for `email` and real numbers for `phone`; bob's shows both as they are.

---

## Lab 5: Build a pipeline on the canvas

**Value:** an analyst builds a transformation from steps, sees each step's result on a sample before anything runs, and what she builds is code (dbt models) that engineers review and release like any other change. No parallel world of visual pipelines nobody can version.

As **alice** (an editor of `retail`), open **Flows**, type `weekly_revenue` and press **New flow**.

1. **Add a step → Input**, table `uploads_orders`. The node shows 8 columns: the canvas asked the catalog.
2. With it selected, **Add a step → Filter**, condition `not test and not refunded`. Its node shows the same columns; press **Preview** to see the rows, read on a sample as alice (so EU rows only, under her rule).
3. **Add a step → Input**, table `uploads_customers`; then select the filter node and **Add a step → Join**: inputs `filter, input_2`, kind `left`, on `customer_id` = `customer_id`. The node shows the joined columns; a clashing column from the right gets `_right`.
4. **Add a step → Formula**: column `week`, expression `date_trunc('week', ordered_on)`, type `date`.
5. **Add a step → Group by**: by `region, week, segment`; aggregates `sum amount as revenue`, `count as orders`, `count_distinct customer_id as customers`.
6. **Add a step → Output**, table `weekly_revenue`, with two checks: `region` not null, `week` not null.
7. Press **Preview** on the output: the weekly revenue by region and segment, from a sample. Press **SQL** to read the model the platform wrote for it.

Try breaking it: change the join to `customer_id` = `email`. The canvas refuses before anything runs: "their types differ; retype one first". Put it back.

**Saving** writes the steps as dbt models into the project's pipelines repo, on a branch with a merge request, and a DAG that builds the table on the schedule you give. It needs the project to have a repo (Lab 11). Without one the button says so; the flow still works as a preview.

**Check:** the output step's preview has a row per region, week and segment, and the SQL tab shows one `select` per step with the step's definition as a comment above it.

---

## Lab 6: Define revenue once

**Value:** "revenue" means the same thing to the cent in SQL, in dashboards, in a file sent to a partner and in an assistant's answer, because it's defined once and every tool asks the same definition. A change to it is reviewed and tested.

As **bob**, open `retail` → **Metrics** → **Edit**, and paste:

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
  name: refund_count
  description: How many orders were refunded.
  type: simple
  type_params: {measure: refunds}
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
  - name: customer
    owner: bob
    definition: A person or company that has placed at least one order.
    synonyms: [client, account]
---
tests:
  - name: three_regions
    metrics: [revenue]
    group_by: [order__region]
    expect: {rows: 3}
  - name: one_total
    metrics: [revenue, order_count]
    expect: {rows: 1}
```

1. Save. The platform compiled it with MetricFlow before saving; a mistake is refused with the reason. The page lists the metrics and their dimensions.
2. Ask: pick `revenue` and `order_count`, group by `order__region`, press **Ask**. As bob: US about 11,341, EU about 7,263, APAC about 3,564; 287 orders in all. As **alice**, the same question gives EU only: the question runs as the person asking, under her rule.
3. Press **Run the 2 tests**: both pass. (As alice they'd fail on `three_regions`: she sees one region. Tests are run by the owner, and in a merge request's review, Lab 11.)
4. Change the definition of `amount_sum` to `amount * 0.9` and save: the next answer follows. Put it back.

**Check:** the Metrics tab's answer for revenue by region matches `SELECT region, round(sum(amount), 2) FROM uploads_orders WHERE not test AND not refunded GROUP BY 1` on the SQL tab, to the cent.

---

## Lab 7: Dashboards on the same definitions

**Value:** the dashboard's revenue is the semantic layer's revenue, not a chart author's own SQL. Each person's chart shows what they may see.

1. Open `retail` → **Dashboards**. Superset opens in the platform, signed in as you. Under **Datasets**, find the dataset the platform made from the model `orders` of `retail`, within a minute of saving it: its dimensions as the model names them (`order__region`, `order__channel`, `orders__ordered_on`) and its metrics (`revenue`, `order_count`, `refund_count`).
2. Make a chart on it: revenue by `order__region`, bar chart. Save it to a new dashboard, "Harbor Outfitters".
3. As **alice**, open the dashboard: EU only, because the chart's query ran as her.

The dataset is the platform's: when the model changes, the dataset follows; charts people make on it are theirs and are never touched. A metric a dataset can't express (a ratio across models) stays the semantic layer's and the dataset's description says so.

**Check:** the chart's bars are the numbers Lab 6 gave.

---

## Lab 8: Ask an assistant

**Value:** an assistant (Claude, a company tool, DevAssist) answers from the same definitions, as the person asking, and says what it used. It can't see what the person can't, and every question is in the audit log with both names.

The platform's MCP server is at `<the platform's address>/mcp`. With Claude Code on your machine:

```
claude mcp add --transport http storscale http://storscale.localhost:8800/mcp
```

Sign in when the browser opens, as **alice**. Then ask, in Claude Code: *"What was Harbor Outfitters' revenue by region last quarter, and what does revenue mean here?"*

1. The assistant lists the projects alice may read (`retail`, `sales`), finds `revenue`, asks for it by region, and answers with EU only, as alice would get it anywhere.
2. Its answer cites what it used: the metric's definition, the term **Revenue** (with its owner and synonyms), and, when the table is a certified data product, the product. Ask *"what's a customer?"* and it answers from the term, not from guesswork; ask about something the model doesn't cover and it says there's no governed definition rather than inventing one.
3. As **bob**, open `retail` → **Audit**: each of the assistant's queries is there, naming the agent (`storscale-agent`) and alice.
4. Administrators register the assistants the company approves (Administration → Assistants: a name and its redirect address); each gets a client of its own, so DevAssist and a Claude desktop app sign in as themselves.

**Check:** the assistant's revenue numbers are Lab 6's, and the Audit tab shows the questions.

---

## Lab 9: What was sent about a person, and where their data is

**Value:** the privacy officer answers "what do we hold about this person, and where did we send it" from the platform, not from a hunt through mailboxes.

1. **Where their data is:** Lab 2 declared `uploads_customers.customer_id`; Lab 4 found the columns. The catalog shows both (the `Protection` tags), and the Data page shows each column's protection. That's the register of personal data, kept as data changes.
2. **Deliveries:** a project sends files to clients and partners on a schedule (the Deliveries tab: a table or query, a format, a name with `{date}`, where to, when). A delivery of a table of people's data needs a data-protection approver's approval before it runs, with the personal columns named; the Deliveries tab shows what each run sent and who approved. (Setting one up needs a target: an SFTP server or an S3 bucket of your own, given on the Connections tab.)
3. **The search:** on the Deliveries tab, an approver enters a customer's ID. The platform runs each kept delivery again at the moment it read the data and lists the runs that carried that person's rows, when, to whom, and who approved. The audit log records that a search was made, never the ID.

**Check:** as bob, open **Data** → `uploads_customers` → **History**: every change to the table, who made it, and "read it as it was" for any of them. That's the same machinery the search uses.

---

## Lab 10: An agent of the project's own

**Value:** a program that reads the project's data gets an identity of its own, only the tools it needs, an owner, approval before it runs, and a switch that stops it at once. The audit names it.

1. As **bob**, open `retail` → **Agents** → **Register an agent**: name `nightly-summary`, "sums the day's orders for the team's chat", tools `list_metrics` and `query_metrics`. Register. It awaits approval; bob can't approve his own.
2. As **alice** (an editor too), open the same tab and **Approve**. The page shows the agent's client ID and its secret, once. Give them to the program.
3. The program signs in to Keycloak with those (client credentials) and asks the MCP server for `revenue` by region: it gets the whole company's numbers, as a reader of the project, and the audit names `agent-retail-nightly-summary`. Ask it for `list_products`: refused, naming the two tools it may use.
4. Change its tools (add `list_products`): the approval is taken away, and its client with it, until alice approves again.
5. **Revoke** it: within a minute it signs in no more.

**Check:** on the Audit tab, `agent.registered`, `agent.approved` and `agent.revoked`, each with who did it.

---

## Lab 11: The project as code (with a git repo)

**Value:** every change to pipelines, models and the project itself is a reviewed change in git, tested on the data before it merges, and released to production by someone other than its author.

This lab needs a GitLab repo the platform can reach. Give the project its token on **Connections** (kind "git token", named `retail-git`), then on the project's **Edit** page fill in the Pipelines part: the repo's address, the DAGs' folder (`dags`), the branch, the connection, and the GitLab review settings. Within a minute the project has an Airflow of its own on Kubernetes, reading that repo.

1. **Flows → Save as merge request** (Lab 5's flow): the models with their steps as comments, `sources.yml`, `schema.yml` with the checks, and the DAG, on a branch with a merge request. Bob opens the merge request in GitLab: readable dbt. He changes one model's SQL by hand and pushes; the canvas shows that step as code from then on, and the rest as steps.
2. **A change, tested first:** the merge request's dbt models are built on a data branch of the project's tables (a Nessie branch), its tests run there, and the merge request gets a status and a note: rows on the branch against production, which metrics moved, and the semantic model's tests with the answers that changed. Production is untouched until the merge.
3. **Released by plan:** with `storscale/project.yaml` and `storscale/semantic.yaml` in the repo, the **Releases** tab plans a tag (what would change: members, rules, metrics) and an administrator other than the planner applies it. A rollback is a plan of an earlier tag.

**Check:** after the merge, `weekly_revenue` is a table of the project's, in the catalog with its lineage back to `uploads_orders` column by column, and a failing check on it is one of the project's alerts.

---

## Lab 12: Operate it

**Value:** the people who run the platform see its health, who did what, and that one project can't slow another; the people who own a project are told when something breaks.

1. **Alerts:** on `retail`'s Overview, the project's issues: a failed pipeline run, a failing check, a table not changed in time (set `freshness` on the project), a column protected on the catalog's word. Set a Teams channel or email on the project's Edit page to be told.
2. **Audit:** the project's Audit tab, and Administration → Audit for the whole platform: who read what in Trino (Ranger's log), who changed a project, who approved what.
3. **Health:** Administration → Health: every service, as the platform checks it; Monitoring opens Grafana.
4. **History and closes:** a table's History (Lab 9), and the **Tags** tab: tag the month-end as `2026-09-close` and it's kept however old the rest of the history gets; any report can be reproduced from it.
5. **Limits:** on a project's Edit page, how many queries at once, how much memory each, how much storage; one project can't starve the others.

**Check:** Administration → Health shows every service up; `retail`'s Audit tab shows this afternoon's work.

---

## Where to go next

- [use-cases.md](use-cases.md): the stories the platform is built around, phase by phase, with their requirements and what's built.
- [plan.md](plan.md): how each piece works, and what's next.
- The CHANGELOG: what each release added, and how to upgrade.
