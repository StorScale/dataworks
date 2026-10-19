# StorScale Platform: a lab guide

A walk through the platform with a small company's data, for anyone who will use it: analysts, data engineers, the people who own a project, and whoever decides whether to run it. Each lab takes ten to twenty minutes, says what the business gets out of it, and ends with something you can check. The labs build on each other: Part I shows the platform doing the work (thirteen labs, an afternoon); Part II shows it keeping its promises on versions, reviews, rules and records (seven more).

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

4. **Erase a customer:** Lab 20 does it end to end, with the record and the 45-day clock.

**Check:** as bob, open **Data** → `uploads_customers` → **History**: every change to the table, who made it, and "read it as it was" for any of them. That's the same machinery the search and the erasure (Lab 20) use.

---

## Lab 10: An agent of the project's own

**Value:** a program that reads the project's data gets an identity of its own, only the tools it needs, an owner, approval before it runs, and a switch that stops it at once. The audit names it.

1. As **bob**, open `retail` → **Agents** → **Register an agent**: name `nightly-summary`, "sums the day's orders for the team's chat", tools `list_metrics` and `query_metrics`. Register. It awaits approval; bob can't approve his own.
2. As **alice** (an editor too), open the same tab and **Approve**. The page shows the agent's client ID and its secret, once. Give them to the program.
3. The program signs in to Keycloak with those (client credentials) and asks the MCP server for `revenue` by region: it gets the whole company's numbers, as a reader of the project, and the audit names `agent-retail-nightly-summary`. Ask it for `list_products`: refused, naming the two tools it may use.
4. Change its tools (add `list_products`): the approval is taken away, and its client with it, until alice approves again.
5. **Revoke** it: within a minute it signs in no more.
6. **Let the platform run it** (needs the project's repo, Lab 11). Put a notebook in the repo under the pipelines' path, say `notebooks/nightly_summary.ipynb`:

   ```python
   import buckets_lake
   a = buckets_lake.agent()                                   # the MCP server, as the agent
   revenue = a.call("query_metrics", project="retail", metrics=["revenue"], group_by=["order__region"])
   print(buckets_lake.ask(f"Two sentences for the team's chat on yesterday's revenue by region: {revenue}"))
   ```

   Register the agent again with the same tools, a **schedule** of `0 6 * * *` and the **notebook** `notebooks/nightly_summary.ipynb`, and have alice approve it. Within a minute or two the Agents tab names its pipeline, `agent_retail_nightly_summary`, on the Pipelines tab: run it now. The run checks the repo out and executes the notebook as the agent: `buckets_lake.username()` says `service-account-agent-retail-nightly-summary`, the audit names it, a tool it wasn't given raises in the notebook naming what it may use, and `ask()` spends against the agent's own budget (Lab 21). The executed notebook, outputs and all, is linked from the run, and the Audit tab has `agent.ran`.

**Check:** on the Audit tab, `agent.registered`, `agent.approved`, `agent.ran` and `agent.revoked`, each with who did it; the agent's run notebook shows the agent's own name as who ran it.

---

## Lab 11: The project as code (with a git repo)

**Value:** every change to pipelines, models and the project itself is a reviewed change in git, tested on the data before it merges, and released to production by someone other than its author.

This lab needs a GitLab repo the platform can reach. Give the project its token on **Connections** (kind "git token", named `retail-git`), then on the project's **Edit** page fill in the Pipelines part: the repo's address, the DAGs' folder (`dags`), the branch, the connection, and the GitLab review settings. Within a minute the project has an Airflow of its own on Kubernetes, reading that repo.

1. **Flows → Save as merge request** (Lab 5's flow): the models with their steps as comments, `sources.yml`, `schema.yml` with the checks, and the DAG, on a branch with a merge request. Bob opens the merge request in GitLab: readable dbt. He changes one model's SQL by hand and pushes; the canvas shows that step as code from then on, and the rest as steps.
2. **A change, tested first:** the merge request's dbt models are built on a data branch of the project's tables (a Nessie branch), its tests run there, and the merge request gets a status and a note: rows on the branch against production, which metrics moved, and the semantic model's tests with the answers that changed. Production is untouched until the merge.
3. **Released by plan:** with `storscale/project.yaml` and `storscale/semantic.yaml` in the repo, the **Releases** tab plans a tag (what would change: members, rules, metrics) and an administrator other than the planner applies it. A rollback is a plan of an earlier tag.

**Check:** after the merge, `weekly_revenue` is a table of the project's, in the catalog with its lineage back to `uploads_orders` column by column, and a failing check on it is one of the project's alerts.

---

## Lab 12: A notebook on a schedule (with a git repo)

**Value:** an analyst's notebook becomes a pipeline with one button, and stays a reviewed, versioned change: no notebook running from someone's laptop, no copy pasted into a DAG by an engineer.

With the repo from Lab 11, as **alice**, open **Tools → Notebooks** (JupyterLab opens in the platform, signed in as her) and make a notebook, `weekly_summary.ipynb`, with one cell:

```python
import io, pandas as pd, buckets_lake
s3 = buckets_lake.s3()                      # Buckets, as the person running the notebook: no keys in it
orders = pd.read_csv(io.BytesIO(s3.get_object(Bucket="retail", Key="landing/uploads/orders.csv")["Body"].read()))
real = orders[~orders.test & ~orders.refunded]
weekly = (real.assign(week=pd.to_datetime(real.ordered_on).dt.to_period("W").astype(str))
              .groupby(["region", "week"]).amount.sum().round(2).reset_index())
s3.put_object(Bucket="retail", Key="landing/reports/weekly-summary.csv", Body=weekly.to_csv(index=False).encode())
weekly.tail()
```

1. Run it: the last weeks' revenue by region, and a file in the project's landing area. It ran as alice, under her rules.
2. Press **Schedule** on the notebook's toolbar (it's also on the notebook's menu in the file browser). Pick `retail` (the projects she edits that have a repo) and `0 6 * * 1`: Mondays at six, UTC. The platform takes the notebook with its outputs cleared (what it computed may be data no repo should hold) and commits it with a small DAG to the project's repo, on a branch of its own, `storscale/schedule-weekly-summary`, with a merge request.
3. Open the merge request in GitLab: `dags/notebooks/weekly_summary.ipynb` and `dags/notebook_weekly_summary.py`, five lines that say which notebook, which project and when. Scheduling again updates the same branch and merge request. Merge it.
4. Within a minute the **Pipelines** tab lists `notebook_weekly_summary`. Press **Run now**. The run goes as the project's pipelines, not as alice: it reads the landing area and writes the report there because the pipelines may (and only there). When it ends, the run has a **notebook** link: the executed notebook, its outputs in it, kept by the platform.
5. Break it on purpose: change the cell to read a file that isn't there, schedule again, merge, run. The run fails, the project's alerts say so, and the kept notebook shows the cell and its error.

**Check:** the run's notebook shows the table of weekly revenue, and `landing/reports/weekly-summary.csv` is in the `retail` bucket (read it from a notebook with `buckets_lake.s3().get_object`).

---

## Lab 13: Operate it

**Value:** the people who run the platform see its health, who did what, and that one project can't slow another; the people who own a project are told when something breaks.

1. **Alerts:** on `retail`'s Overview, the project's issues: a failed pipeline run, a failing check, a table not changed in time (set `freshness` on the project), a column protected on the catalog's word. Set a Teams channel or email on the project's Edit page to be told.
2. **Audit:** the project's Audit tab, and Administration → Audit for the whole platform: who read what in Trino (Ranger's log), who changed a project, who approved what.
3. **Health:** Administration → Health: every service, as the platform checks it; Monitoring opens Grafana.
4. **History and closes:** a table's History (Lab 9), and the **Tags** tab: tag the month-end as `2026-09-close` and it's kept however old the rest of the history gets; any report can be reproduced from it.
5. **Limits:** on a project's Edit page, how many queries at once, how much memory each, how much storage; one project can't starve the others.

**Check:** Administration → Health shows every service up; `retail`'s Audit tab shows this afternoon's work.

---

# Part II: versioning and governance

The labs above show the platform doing work. These show it keeping its promises: every version of the data is there to go back to, every change is reviewed and released on purpose, and who may see what is declared once, enforced everywhere and written down.

---

## Lab 14: The data as it was

**Value:** a number in last month's board pack can be reproduced today, to the row, however the tables have changed since. Month-end closes are kept for as long as Finance says, whatever retention does to the rest.

As **bob**, on `retail`:

1. Open **Data** → `uploads_orders` → **History**: every change to the table so far (the import's load), who made it, when, and how many rows it added. Each entry has **Read it as it was**.
2. Change the data. On the **SQL** tab, run `INSERT INTO uploads_orders VALUES (306, 7, DATE '2026-10-01', 'EU', 'web', 88.0, false, false)`, then `UPDATE uploads_orders SET amount = 1.0 WHERE order_id = 1`. Back on **History**: two more entries, yours.
3. On the first entry's **Read it as it was**: the SQL tab opens with the table named at that commit (`"uploads_orders#<hash>"`). `SELECT count(*), round(sum(amount), 2) FROM ...` gives the table as the import left it: 305 rows and the old amount. The same query without the suffix gives 306 rows and the changed amount.
4. Any moment, not only a commit: on the SQL tab, type `2026-10-19` (or any date and time) in **As of** and press Enter. The query's tables are rewritten to the commit current at that moment. `now` puts them back.
5. **A close that's kept.** On the **Tags** tab, make a tag: name `close-2026-09`, at `now`, keep for `7y`, note "September's close". The tag names a commit; the platform keeps that commit's data however old it gets, where the rest of the history is kept for the install's window (the Data page says how long). On the SQL tab, **As of** `close-2026-09` reads every table of the project as it was at the close.
6. Reports and files that must be reproducible use the same thing: a delivery records the commit it read (Lab 9), and the catalog's lineage names the commit each run read.

**Check:** `SELECT count(*) FROM uploads_orders` as of `close-2026-09` gives 306 (the close came after your insert); as of the first history entry it gives 305.

---

## Lab 15: Who did what

**Value:** "who read the payroll table last month" is a search, not an investigation. Every read in Trino, every refusal, every change to a project and every approval is in one log, with the person, and for an assistant's question, the assistant and the person.

1. As **bob**, open `retail` → **Audit**. Search `uploads_customers`: every query that touched it, by whom, allowed or refused, and the query itself. Filter by person `alice`, then **Refused only**: the reads her rules refused.
2. Make a refusal: as **alice**, on the SQL tab, `SELECT * FROM uploads_campaign_responses` after Lab 16 has taken it from the readers' list; or, as **carol** (no project), any query. Back on bob's Audit tab, the refusal is there within a minute, with the reason Ranger gave.
3. Filter by **Source**: Trino's reads (Ranger's log), the platform's own actions (projects saved, tags made, agents approved, classifications confirmed, uploads), the assistants' questions (Lab 8: the agent and the person both named).
4. Administration → **Audit** is the same log for every project, for the platform's administrators.
5. Nothing in the log can be edited from the platform; it's written as things happen and read by whoever may.

**Check:** the entries from Labs 3 to 10 are all there, in order: the uploads, the import's runs, the tags confirmed, the agent approved and revoked, with the right names on each.

---

## Lab 16: Rules for readers, and every tool following

**Value:** a rule is written once, in words anyone can read on the Access tab, and holds in SQL, dashboards, notebooks, the canvas, deliveries and assistants alike. There's no tool where the rule is forgotten.

As **bob**, open `retail` → **Edit**:

1. **Which tables readers see:** in "Tables readers may read", enter `uploads_orders, uploads_customers, uploads_campaigns`. Readers (the analysts) lose `uploads_campaign_responses`; editors keep everything.
2. **What they see of them:** in the JSON box, under `tables.readers`, add a mask beside the row filter from Lab 2:

   ```json
   "masks": [{"table": "uploads_customers", "column": "signed_up", "type": "MASK_DATE_SHOW_YEAR"}]
   ```

   Readers see the year a customer signed up, not the day. (The email and phone columns are already masked by their tags, Lab 4: a project's own masks and the install's protection rules add up.)
3. Save, and open **Access**: the rules in words. "Readers: the EU rows of uploads_orders; signed_up shows the year", and the three tables they may read.
4. As **alice**, see them hold: the Data tab lists three tables; **Preview** of `uploads_customers` shows `2026-01-01` for every date; the SQL tab refuses `uploads_campaign_responses`; a chart on the dashboard (Lab 7) still shows EU only; the assistant (Lab 8) answers EU only; the canvas's previews (Lab 5) read under the same rules.
5. **Roles, not people:** rules name groups (`analysts`), so a new analyst gets them the day they join the group, and loses them the day they leave. A member named as a user (alice as an editor, Lab 2) is the exception for when a person needs more than their group.

**Check:** alice's preview of `uploads_customers` has every `signed_up` on January the first; bob's has the real dates.

---

## Lab 17: Checks and freshness

**Value:** a table that's wrong or late tells its owner before a dashboard reader notices, and the catalog shows every table's checks and their last result.

As **bob**, open `retail` → **Edit** and, in the JSON box under `tables`, add:

```json
"checks": [
  {"table": "uploads_orders", "check": "notNull", "column": "customer_id"},
  {"table": "uploads_orders", "check": "rowCount", "min": 300},
  {"table": "uploads_customers", "check": "unique", "column": "email"}
],
"freshness": [{"table": "uploads_orders", "within": "24h"}]
```

Save. (The freshness part has a form of its own on the page; checks don't yet.)

1. The platform runs the checks in the catalog every minute. Open **Data** → `uploads_orders` → **Quality**: the three checks, passing, with when they last ran.
2. Break one: on the SQL tab, `INSERT INTO uploads_orders (order_id, region, amount, refunded, test) VALUES (307, 'EU', 5.0, false, false)` (no customer). Within a minute **Quality** shows `notNull` on `customer_id` failing ("nullCount=1"), the project's **Overview** lists it as an issue, and the people the project notifies (a Teams channel or email, set on the Edit page) are told. The catalog's own page for the table shows the same test, failed.
3. Fix it: `DELETE FROM uploads_orders WHERE order_id = 307`. The next round passes, the issue clears, and the channel is told it's resolved.
4. **Freshness:** `uploads_orders` must change within a day. Tomorrow, if nothing has loaded it, the Overview says it's stale and the channel hears; a load (the import's hourly run writes nothing when nothing changed, so set the window to what the business expects). The same rule is what a data product's contract promises (Lab 18).

**Check:** Quality shows three checks passing again, and the Audit tab shows your insert and delete.

---

## Lab 18: A certified data product, read in place

**Value:** one team publishes a table with a promise (its columns, how fresh it stays, the checks it passes); another team reads it where it is, under the publisher's rules, with no copy to go stale. The governance group certifies what it has checked, and a broken promise is the publisher's alert, naming who depends on it.

1. **Publish.** As **bob**, on `retail` → **Edit**, add in the JSON box:

   ```json
   "products": [{"name": "orders", "table": "uploads_orders", "description": "Harbor Outfitters' orders, net of test rows", "owner": "bob", "version": "1",
                 "terms": ["revenue"], "contract": {"columns": ["order_id", "customer_id", "ordered_on", "region", "amount"], "within": "24h"}}]
   ```

   Save. The **Data products** page (the Tools menu) lists `orders`: its owner, version, contract, the term **Revenue** it holds, no consumers yet, not certified.
2. **A second team asks.** Make a project `marketing` (Administration → Projects → New: `analysts` as editors, tick Tables). As **alice** (an editor of `marketing`), open **Data products** and press **Ask for it** on `orders`, for `marketing`, with a reason.
3. **The publisher approves.** As **bob**, the product's card shows the request; approve it. Within a minute `marketing`'s people may read `iceberg.retail.uploads_orders` in place: as alice, on `marketing`'s SQL tab, `SELECT region, count(*) FROM iceberg.retail.uploads_orders GROUP BY 1` works, and gives EU only: retail's row filter holds for consumers too. Nothing was copied; there's one table.
4. **Certify.** As **bob** (the install's certifiers are its administrators unless set otherwise), press **Certify** on the card: the product is marked certified, first in the list, and the catalog shows it as a data product with its table marked Gold. The certification records what it certified: the table, the version and the contract.
5. **A promise changed.** Edit the product's contract (drop `amount` from its columns) and save: the certification lapses, because what was checked isn't what's promised now. Put it back and certify again.
6. **A promise broken.** Rename a contracted column: `ALTER TABLE uploads_orders RENAME COLUMN region TO country`. Within a minute `retail`'s Overview lists the broken contract, naming `marketing` as the consumer that depends on it; a merge request that would drop the column fails its status (Lab 11) saying the same. Rename it back.
7. **Taking it away** is the publisher's too: remove `marketing` from the product's consumers, and its reads are refused at once.

**Check:** `marketing`'s alice reads `iceberg.retail.uploads_orders` with EU rows only, and the Data products page shows `orders` certified with `marketing` as its consumer.

---

## Lab 19: Released on purpose (with a git repo)

**Value:** what runs in production is a tag in git, planned by one person and applied by another, with the difference spelled out first; going back is applying an earlier tag, and the data written in between stays.

With the repo from Lab 11, put the project itself in it:

```
storscale/
  project.yaml              # the spec: members, tables and their rules, checks, products, agents
  semantic.yaml             # the metrics, terms and tests of Lab 6
  targets/development.yaml  # what differs per install: laid over project.yaml (maps merged, lists replaced)
  targets/production.yaml
```

1. Tag the repo `v1.0`. As **alice**, open `retail` → **Releases** and plan `v1.0`: the platform checks the repo out at the tag in the project's Airflow, composes the spec for this install's target, checks it as any spec, and shows what would change: members, readers' rules, checks, products, and the metrics added, removed or changed.
2. As **bob** (an administrator other than the planner), **Apply**. The spec and the model are saved as if edited by hand, approvals kept, and the release is recorded: the tag, its commit, who planned, who applied, when. The Audit tab has it.
3. Change the model in the repo (revenue excluding partner orders, say), open a merge request: the review (Lab 11) builds it on a data branch and the note says which of the model's tests changed their answers and by how much, before anyone merges. Merge, tag `v1.1`, plan, apply: the next question on the Metrics tab follows the new definition.
4. **Rollback:** plan `v1.0` and apply it. The spec and the metrics are `v1.0`'s again; the tables' data written since stays, with its history (Lab 14), and the Releases tab says so.
5. A plan older than the project's current spec (someone edited it by hand in between) is refused: plan again from the repo, so the repo stays the truth.

**Check:** the Releases tab lists `v1.0`, `v1.1`, `v1.0` with who planned and applied each; `SELECT count(*) FROM uploads_orders` is unchanged by the rollback.

---

## Lab 20: Erase a customer

**Value:** a request to be forgotten is answered inside the law's deadline by someone with the right to, in every table that holds the person, with a record the auditor can read and the privacy officer can act on, and without the platform itself keeping the person's ID. The company follows the CCPA: 45 days to answer, the request verified first, the recipients of the person's data told.

As **bob** (an administrator; on a company install, the data-protection approvers' group), on `retail`:

1. **Declare where people are.** Lab 2 declared `uploads_customers` by `customer_id`. Add the orders too: on the project's Edit page, under `tables`, make it `"people": [{"table": "uploads_customers", "column": "customer_id"}, {"table": "uploads_orders", "column": "customer_id"}]`. The catalog tags both columns `personal`, confirmed (Lab 4), and both tables count as people's data from now on: a delivery of either needs a data-protection approver, and an erasure reaches both.
2. **Who's in the data.** On the SQL tab: `SELECT customer_id, count(*) FROM uploads_orders GROUP BY 1 ORDER BY 2 DESC LIMIT 3`. Pick a customer with several orders, say `17`. `SELECT * FROM uploads_customers WHERE customer_id = 17` shows them.
3. **What was sent about them.** On the Deliveries tab, type `17` and **Search**: each delivery of people's data is run again on the tables as they were when it read them, and the runs that carried this person's rows are listed with when, to whom and who approved. (With no deliveries set up, the answer is none; Lab 9 says how to set one up.) The search isn't recorded with the ID.
4. **Erase.** Same box, **Erase this person**: when they asked (today), who verified the request (you, or "the privacy tool"), confirm. In seconds the answer says: 1 row from `uploads_customers`, their orders from `uploads_orders`, the deliveries that had carried their data, the due date 45 days after the request, the record's ID, and until when the tables' old versions still hold the rows. `SELECT * FROM uploads_customers WHERE customer_id = 17` is empty now, for everyone.
5. **The record.** The Erasures list on the same tab: when, asked on, due by, what was deleted where, how many deliveries, by whom, verified by whom. Open the Audit tab: an `erasure` entry with the record's ID and the counts, and nowhere the number 17. The record is kept at least two years; it carries a hash of the ID, so a second request for the same person is recognisable, and never the ID.
6. **What the past keeps.** Lab 14 showed every version of a table is kept for the history window. The erased rows are in those old versions until the window passes (the record's "ages out by"), and in any close tagged while they were there (the record names the closes). That's the one place erasure and "the data as it was" meet; the record makes it visible rather than silent, and Finance and the privacy team decide between them what a close may keep.
7. **Retention, so there's less to erase.** Under `tables`, add `"retention": [{"table": "uploads_campaign_responses", "column": "responded_on", "keep": "730d"}]`. Responses older than two years are deleted by the nightly maintenance; press **Run now** on the Data page's Storage tab to see a round happen. Rows that age out never need erasing.

**Check:** customer 17 has no rows in either table; the Erasures list has one entry with the due date 45 days after today; the Audit tab's `erasure` entry names the record and not the person.

---

## Lab 21: Models through the gateway, on a budget

**Value:** every call to a model goes through one gateway the company controls, with a key per project and per agent, each with a monthly budget: the spend is visible beside the budget, the project is warned at 80%, and at the budget the gateway stops answering rather than the bill growing. Nobody pastes a provider's key into a notebook. (On a company install the gateway is LiteLLM on the company's models, Anthropic's through Microsoft Foundry; a lab install runs the platform's stand-in, which answers by repeating the question and charges a cent per hundred characters, so a budget is reached in minutes.)

As **bob**, on `retail`:

1. **Give the project a budget.** On the project's Edit page, add `"llm": {"budget": 0.05}` to the spec (five cents a month, for the lab; a real project gets dollars). Save. Within a minute the **Agents** tab's gateway card says `retail: 0.00 of 0.05 spent`, and the **Connections** tab lists `retail-llm`, kind `llm`, added by `platform-operator`, with its key, the gateway's address and the budget set. The key is shown nowhere: a pipeline in the project's Airflow, a notebook's job or an agent's deployment reads it as the project's connection (on Kubernetes, the Secret `retail-llm`).
2. **Ask through it.** For the lab, read the key as the administrator (`kubectl -n storscale get secret retail-llm -o jsonpath='{.data.llmKey}' | base64 -d`; on Compose it's `connections/retail/retail-llm.json` in the platform's bucket) and, in a notebook cell:

   ```python
   import requests
   r = requests.post("http://llm:4000/chat/completions", headers={"Authorization": "Bearer " + KEY},
                     json={"model": "default", "messages": [{"role": "user", "content": "How were sales in the west last week?"}]})
   print(r.status_code, r.json()["choices"][0]["message"]["content"])
   ```

   The stand-in answers by repeating the question; LiteLLM would answer from the model. Run the cell four times: the Agents tab shows `0.04 of 0.05 spent`, and the project's Overview carries an alert: the project has used 80% of its LLM budget.
3. **At the budget.** Run it twice more: the second answer is `400`, "Budget has been exceeded", the gateway's own refusal, the same whether the stand-in or LiteLLM. The alert now says the project is at its budget and the gateway answers it no more this month.
4. **Raise it.** Change the spec to `"llm": {"budget": 1}`: the operator makes a new key (the old one answers no more, so a program holding it reads the connection again) and the tab shows `0.00 of 1.00 spent`.
5. **An agent's own budget.** On the Agents tab, register `nightly-summary` as in Lab 10 with a **budget** of `0.50`, and have alice approve it. Its row shows `0.00 of 0.50 spent` once the operator has made its key, kept beside its secret in the agent's connection; the agent spends against its own budget, not the project's, and an agent that loops is stopped at its budget with bob alerted. **Revoke** it: its key goes with its client.

**Check:** the Agents tab shows the project's spend and the agent's against their budgets; the Audit tab has the budget changes as project edits; a call past the budget is refused by the gateway, not by the platform.

---

## Where to go next

- [use-cases.md](use-cases.md): the stories the platform is built around, phase by phase, with their requirements and what's built.
- [plan.md](plan.md): how each piece works, and what's next.
- The CHANGELOG: what each release added, and how to upgrade.
