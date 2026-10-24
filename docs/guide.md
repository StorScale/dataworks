# StorScale Platform: a lab guide

One company's quarter, told in 22 labs: Harbor Outfitters starts with four spreadsheets, three answers to every question and a privacy officer who can't say where the customers' data is, and ends with one revenue number every tool agrees on, campaign decisions made on it, a partner fed on a schedule, a customer erased in minutes, models asked under a budget, and every change released on purpose. Each lab is ten to twenty minutes, opens with the moment in the story that calls for it, says what the business gets, and ends with something you can check. The labs build on each other, in order.

**What you need.** The platform running: on your machine, `storscale up`, then `http://storscale.localhost:8800` (the [README](../README.md) says how); or your company's install, at its own address. Three people exist in the example sign-in: **alice**, an analyst; **bob**, an engineer, who also administers the platform; and **carol**, who has no access. Their password is `LAKEHOUSE_USER_PASSWORD` in `stack/.env` on a local install. Two browser windows, or one normal and one private, make switching between alice and bob easy. Labs 10, 11, 17, 18 and 20 need a GitLab repo the platform can reach (Lab 10 says how); the rest need nothing but the platform.

**The company.** Harbor Outfitters sells outdoor gear in three regions through its web shop, its stores and partners, and runs marketing campaigns. Its data, five CSV files and two pages in [`docs/lab/data/`](lab/data/) (made by [`make_data.py`](lab/make_data.py), so the numbers below hold):

| File | What | Rows |
|---|---|---|
| `customers.csv` | Customers: names, email addresses and phone numbers (personal data, on purpose), region, segment | 60 |
| `orders.csv` | Orders from July to September 2026: region, channel, amount, a few refunds, five test rows | 305 |
| `campaigns.csv` | Six marketing campaigns with budgets | 6 |
| `campaign_responses.csv` | Who responded to which campaign, and whether they converted | 180 |
| `reviews.csv` | What customers wrote about what they bought, with stars; three sign off with an email address | 90 |
| `care.md`, `returns.html` | Two of the company's pages, the care guide and the returns page, for Lab 18's knowledge bank of files | |

**The quarter.** In the first week of October the CEO asks three questions: what was revenue by region, which campaigns paid for themselves and should run again, and which customers are worth a call before the holidays. Finance, marketing and the web team each answer the first one with a different number. The privacy officer has a fourth question nobody can answer: where is the customers' personal data, who can see it, and what was sent to partners about a customer who has just asked to be forgotten. The labs are the quarter in which the platform answers all four, and what each answer was worth.

| Part | The story | Labs |
|---|---|---|
| I | **Trust the data:** the four files become governed tables, personal data finds its own protection, and a table says when it's wrong | 1–5 |
| II | **One number:** revenue defined once, the same to the cent in SQL, dashboards and an assistant's answer | 6–8 |
| III | **Decisions worth money:** which campaigns pay, built on a canvas and released as code; who to call, from a notebook on a schedule, and who's likely to stop, scored each night | 9–11 |
| IV | **Shared, and safe:** marketing reads retail's table in place, a partner gets a file every Monday, the privacy officer's day | 12–15 |
| V | **AI on the same rules:** models through a gateway on a budget, an agent of the project's own, what customers say sorted by a model | 16–18 |
| VI | **Keep it true:** last month's number reproduced, releases on purpose, who did what (and a role of the company's own), the platform operated | 19–22 |

---

# Part I: trust the data

Monday. The orders file is on a shared drive; three people have three copies. Nothing can be decided on it until everyone reads the same table, under the same rules, and knows when it's wrong.

## Lab 1: Sign in, and see what each person sees

**The moment:** the analysts need a place to work that isn't a shared drive, and the engineers won't hand out database passwords.

**Value:** one sign-in for every tool, and each person sees exactly what their role allows. No shared passwords, no tool-by-tool accounts.

1. Open the platform as **alice**. Home greets her and lists her tools (SQL, notebooks, dashboards, pipelines, the catalog, monitoring) and her projects: `sales`, where she's a reader.
2. Open the **Tools** menu and open SQL, then Dashboards: no second sign-in. The platform signed her in to each tool.
3. Open `sales`. The tabs are the project's pages: Overview, Data, Metrics, SQL, Notebooks, Pipelines, Flows, Agents, Dashboards, Tags, Imports, Connections, Access. On **Data**, open `orders` and read the note above its columns: as a reader she sees only EU rows, and card numbers show their last four digits. Open **Preview**: that's what she gets.
4. Sign out. Sign in as **carol**: "No access". She isn't in a group the platform admits.
5. Sign in as **bob**. He has the same tabs plus Audit, an **Administration** menu, and on `sales` he's an editor.

**Check:** alice's preview of `orders` has only `EU` in the region column; bob's has all three regions.

---

## Lab 2: A project for the retail team

**The moment:** the retail team gets a home for its data, with its people, its rules and where it's told about problems written down once.

**Value:** a project is the unit of ownership. Its members, its tables, who may read what, and where it's told about problems are declared once, and every tool follows: Trino, the buckets, the catalog, Airflow, dashboards, notebooks, agents.

As **bob**, Administration → Projects → **New project**:

1. Name `retail`, description "Harbor Outfitters: orders, customers, campaigns".
2. Members: the group `engineers` as editors; the group `analysts` as readers. alice is an analyst, so she reads `retail` as its readers do: Labs 3 to 8 show what that means. (She becomes an editor in Lab 9, when she starts building.)
3. Tick **Tables** (the project gets a namespace, `retail`, in the lakehouse), **Files** (a bucket of its own, where uploads land) and **Pipelines**, service account `airflow-pipelines` (the project's imports, deliveries and scoring run as its pipelines, which read its landing area and write its tables; adding an import gives a project pipelines anyway).
4. Under the readers' rules, add a row filter on `uploads_orders` (the table Lab 3 makes of the orders file): `region = 'EU'`, for the EU analysts' team. Leave masks empty: Lab 4 shows a better way.
5. Save. Within a minute the Overview says **Ready**, and the Access tab shows who got what: the readers' and editors' groups in Keycloak, Ranger's policies on the namespace, the bucket's policies.

Also open **Edit** on the project and, in the JSON box, add under `tables`:

```json
"people": [{"table": "uploads_customers", "column": "customer_id"}, {"table": "uploads_orders", "column": "customer_id"}]
```

This declares which tables hold people and which column identifies one. The platform uses it for the privacy labs (4, 14 and 15): those tables are personal data from now on, a delivery of either needs a data-protection approver, and an erasure reaches both.

**Check:** on the Access tab, `bob` is an editor of `retail` (through `engineers`) and `alice` a reader (through `analysts`); on the Audit tab, the project's creation is the first entry, with who did it.

---

## Lab 3: Get the data in

**The moment:** the four files, and the one database that stays where it is.

**Value:** a file becomes a governed table in minutes, with no engineer writing a loader; a database the company already runs is queried where it is, under the project's rules, with no copy; the same forms take applications (Salesforce) and change capture.

As **bob**, open `retail` → **Flows** → the **Uploads** panel at the bottom.

1. **Upload a file**: `customers.csv`. The upload lands in the project's landing area, as bob (the bucket's policy decides who may write there), and the panel shows a preview: the columns and the types the platform read from the first rows. `customer_id` is a bigint, `email` a varchar, `signed_up` a date.
2. Press **Import as a table**, accept the name `customers`. The project gains an import called `uploads`, which reads the landing area into the table `uploads_customers`. Do the same for `orders.csv`, `campaigns.csv` and `campaign_responses.csv`: each joins the same import. (`reviews.csv` waits for Lab 18.)
3. Open **Pipelines**: `import_retail_uploads` is there, scheduled hourly. Press **Run now** and watch the run; it takes a minute. (A run that fails says why on this page: "why it failed" next to the run.)
4. Open **Data**: four tables, `uploads_customers`, `uploads_orders`, `uploads_campaigns`, `uploads_campaign_responses`, with their columns, from the catalog. Open **Preview** on `uploads_orders`. Within a few minutes the catalog has profiled each table: above the columns, how many rows and when they were counted; beside each column's type, what it holds: its share of nulls, how many distinct values, the smallest and the largest. `region` holds 3 distinct values; `amount` runs from a few dollars to 9,999 (the test rows).
5. **A database where it is.** The company's dashboards tool keeps its own database, and on a lab install it stands in for the ERP. On **Connections**, add one of kind **A database user**, named `dashboards-db`: host `superset-db`, port `5432`, database `superset`, username `superset`, and the password (`SUPERSET_DB_PASSWORD` in `stack/.env` on a local install; on a company install, the read-only account the database's owner gives). The password is written once and never shown again. Then on **Edit**, in the JSON box, add:

   ```json
   "sources": [{"name": "dashboards", "kind": "postgresql", "connection": "dashboards-db"}]
   ```

   Save. Within a minute the project has a catalog of its own, `retail_dashboards`, and on the **SQL** tab `SELECT count(*) FROM retail_dashboards.public.dashboards` answers from the database itself: nothing was copied. Its editors and pipelines may read it, no one may write it, and it joins with the lakehouse's tables in the same query. On Kubernetes the same source can be copied into the project's tables by change capture instead (`"copy": {"tables": ["public.dashboards"]}` on the source: inserts, updates and deletes follow within a minute).
6. Open **Imports**: the uploads import's definition, and the same place an application is added from a form: Salesforce by its objects and a date to start from, or any REST API, each on a schedule, each run copying only what's new.

**Check:** on the SQL tab, as bob, `SELECT count(*) FROM uploads_orders` gives 305; as alice, the same query gives the EU rows only (about a third), because the readers' rule from Lab 2 holds for her: she's a reader.

---

## Lab 4: Personal data, found and protected

**The moment:** the privacy officer asks where the customers' email addresses went. Nobody wrote a policy for them.

**Value:** the customers' emails and phone numbers were uploaded in Lab 3 without anyone writing a policy for them. The catalog finds them, and the platform masks them for everyone but the groups the company excepts, everywhere Trino is: SQL, dashboards, notebooks, deliveries, assistants.

1. Wait a few minutes after the import (the catalog describes new tables every minute and classifies every two), then open **Data** → `uploads_customers` as bob. The **Protection** column shows `personal (suggested)` on `email` and `phone`, found by their names and by what their values look like; and `personal` confirmed on `customer_id`, because Lab 2 declared it.
2. The project's Overview lists an alert: "email looks personal: the catalog tagged it, and it's masked". The owner looks, and either confirms or removes the tag.
3. As **alice**, run on the SQL tab: `SELECT first_name, email, phone FROM uploads_customers`. The email and phone columns are hashed. Nobody wrote a policy; the tag did it.
4. As **bob**, confirm `email` (the tag stays whatever the catalog finds later) and, to see the other direction, remove the tag on `phone`. Within a minute alice reads phone numbers again, and the catalog doesn't suggest it back.
5. Open the catalog (Tools → Catalog) and find `uploads_customers`: the same tags, under the **Protection** classification, with their state. The catalog's own page is another place to confirm or correct them.

What a tag means is set once per install: `personal` is hashed, `financial` masked, `health` nulled, each with the groups that see the column as it is (on a local install, the engineers). The privacy officer sets that; project owners never write masks for personal data again.

**Check:** alice's query shows 64-character hashes for `email` and real numbers for `phone`; bob's shows both as they are.

---

## Lab 5: A table that says when it's wrong

**The moment:** last quarter a report went to the board with a week of orders missing, and nobody knew until a customer asked. Before anyone builds on `uploads_orders`, it has to tell its owner when it's wrong or late.

**Value:** a table that's wrong or late tells its owner before a dashboard reader notices, and the catalog shows every table's checks and their last result.

As **bob**, open `retail` → **Edit** and, in the JSON box under `tables`, add:

```json
"checks": [
  {"table": "uploads_orders", "check": "notNull", "column": "customer_id"},
  {"table": "uploads_orders", "check": "rowCount", "min": 300},
  {"table": "uploads_customers", "check": "unique", "column": "email"},
  {"table": "uploads_orders", "check": "acceptedValues", "column": "region", "values": ["EU", "US", "APAC"]},
  {"table": "uploads_orders", "check": "range", "column": "amount", "min": 0, "max": 10000},
  {"table": "uploads_orders", "check": "references", "column": "customer_id", "refTable": "uploads_customers", "refColumn": "customer_id"},
  {"table": "uploads_orders", "check": "sql", "name": "no_refund_without_amount", "sql": "SELECT order_id FROM uploads_orders WHERE refunded AND amount <= 0"}
],
"freshness": [{"table": "uploads_orders", "within": "24h"}]
```

Save. (The freshness part has a form of its own on the page; checks don't yet.) Eight kinds of check exist: `unique`, `notNull`, `rowCount`, `acceptedValues`, `range`, `regex` (every value matches a pattern), `references` (every value is found in another table's column), and `sql` (a query of your own that must return no rows; the project's tables may be named bare, as on the SQL tab).

1. The platform runs the checks in the catalog every minute. Open **Data** → `uploads_orders` → **Quality**: its checks, passing, with when they last ran (the test orders' amount of 9,999 is under the range's ceiling; the references check finds every order's customer).
2. Break one: on the SQL tab, `INSERT INTO uploads_orders (order_id, region, amount, refunded, test, _dlt_load_id, _dlt_id) VALUES (307, 'EU', 5.0, false, false, 'by-hand', 'by-hand-307')` (no customer; the two `_dlt` columns are the import's own record of each row, never empty). Within a minute **Quality** shows `notNull` on `customer_id` failing ("nullCount=1"), the project's **Overview** lists it as an issue, and the people the project notifies (a Teams or Slack channel, or email, set on the Edit page) are told. The catalog's own page for the table shows the same test, failed.
3. Fix it: `DELETE FROM uploads_orders WHERE order_id = 307`. The next round passes, the issue clears, and the channel is told it's resolved.
4. **Freshness:** `uploads_orders` must change within a day. Tomorrow, if nothing has loaded it, the Overview says it's stale and the channel hears; a load (the import's hourly run writes nothing when nothing changed, so set the window to what the business expects). The same rule is what a data product's contract promises (Lab 12).

**Check:** Quality shows every check passing again, and the Audit tab shows your insert and delete.

**What Part I gained:** four files are tables anyone entitled can read, the EU analysts see EU rows and nobody sees an email address they shouldn't, the ERP is one query away without a copy, and the orders table reports its own faults. The shared drive is retired.

---

# Part II: one number

Tuesday. Finance says Q3 revenue was 22,168. Marketing's tool says 23,250 (it counts refunded orders). The web shop's export says 73,245 (it counts the test orders too). The CEO wants one number, and wants to know what it means.

## Lab 6: Define revenue once

**The moment:** bob writes down what revenue is, once, where every tool reads it.

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
2. Ask: pick `revenue` and `order_count`, group by `order__region`, press **Ask**. As bob: US about 11,341, EU about 7,263, APAC about 3,564; 300 orders in all (refunded ones included: `order_count` counts every real order). That's Finance's 22,168, and now everyone's. As **alice**, the same question gives EU only: the question runs as the person asking, under her rule.
3. Press **Run the 2 tests**: both pass. (A test may also be a question in words, `question: What was turnover by region?` with the call it should become, `metrics: [revenue]` and `group_by: [order__region]`: once the project has an LLM budget (Lab 16), the model is asked through the gateway and its call compared, so a renamed metric or a lost synonym fails before anyone asks it for real.) (As alice they'd fail on `three_regions`: she sees one region. Tests are run by the owner, and in a merge request's review, Lab 10.)
4. Change the definition of `amount_sum` to `amount * 0.9` and save: the next answer follows. Put it back.
5. **Share them.** On `retail` → **Edit**, add `"semantic": {"shared": true}` to the spec and save: retail's metrics and terms are the company's now, for every project to build on (Lab 12) and every assistant to answer from (Lab 8). Open **Tools → Business terms**: **Revenue** and **Customer** are there, each with its owner, its definition and synonyms, the metrics and tables behind it, and the projects that use it. Search for "turnover" from the top bar: it finds Revenue. The catalog's glossary has the same terms, kept in step.
6. **A new term.** Marketing keeps saying "basket size" and meaning revenue per order. Make it a word of the company's: on **Metrics** → **Edit**, add to the `terms:` document

   ```yaml
     - name: average_order_value
       owner: bob
       definition: "Revenue per real order, in US dollars: what a customer spends on one order, on average."
       synonyms: [basket size, AOV]
   ```

   and save. A term names something the model defines (a metric, an entity or a dimension): `average_order_value` is the metric above, so it's accepted; a term for something the model doesn't define is refused on save, naming it. On **Business terms** the new term is there with its owner, its definition, its metric and the table behind it; the top bar's search finds it by "basket size"; the catalog's glossary has it within a few minutes; and an assistant asked *"what's our basket size by region?"* (Lab 8) answers from `average_order_value`.

**Check:** **Business terms** lists Revenue, Customer and Average order value, and searching "AOV" finds the last; the Metrics tab's answer for revenue by region matches `SELECT region, round(sum(amount), 2) FROM uploads_orders WHERE not test AND not refunded GROUP BY 1` on the SQL tab, to the cent.

---

## Lab 7: Dashboards on the same definitions

**The moment:** marketing's dashboard gets rebuilt on the definition instead of its own SQL, and the 23,400 disappears.

**Value:** the dashboard's revenue is the semantic layer's revenue, not a chart author's own SQL. Each person's chart shows what they may see.

1. Open `retail` → **Dashboards**. Superset opens in the platform, signed in as you. Under **Datasets**, find the dataset the platform made from the model `orders` of `retail`, within a minute of saving it: its dimensions as the model names them (`order__region`, `order__channel`, `orders__ordered_on`) and its metrics (`revenue`, `order_count`, `refund_count`, `customer_count`).
2. Make a chart on it: revenue by `order__region`, bar chart. Save it to a new dashboard, "Harbor Outfitters".
3. As **alice**, open the dashboard: EU only, because the chart's query ran as her.
4. **Or an app, without Superset.** For the regional leads who only need the one number: `retail` → **Apps** → **Make an app**: name `revenue-by-region`, metric `revenue`, by `order__region`, as bars, with a filter on `order__region` (values `EU, US, APAC`). Open it: bars by region, the filter above them, a table view a click away. As **alice** it's on her **Home** under **Apps**, and it shows her the EU alone: an app is fetched as whoever opens it. An app can also be a table's rows, or (after Lab 18's knowledge bank) questions answered from what customers wrote, each answer with the reviews it's from.

The dataset is the platform's: when the model changes, the dataset follows; charts people make on it are theirs and are never touched. A metric a dataset can't express (a ratio across models) stays the semantic layer's and the dataset's description says so.

**Check:** the chart's bars are the numbers Lab 6 gave.

---

## Lab 8: Ask an assistant

**The moment:** the CEO asks the question in a chat window instead of waiting for Thursday's deck.

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

**What Part II gained:** one revenue number, 22,168, in SQL, in the dashboard and in a chat, each under the asker's rules, and the first of the CEO's three questions answered the same way by everyone.

---

# Part III: decisions worth money

Wednesday. Six campaigns ran last quarter for 4,900 in budget. Which paid for themselves? And the sales team wants a list of customers to call before the holidays. Both answers have to be built, not looked up, and built so they keep being right next quarter.

## Lab 9: Which campaigns pay for themselves

**The moment:** alice builds the answer from steps, sees every step's result before anything runs, and the answer is code engineers can review.

**Value:** an analyst builds a transformation from steps, sees each step's result on a sample before anything runs, and what she builds is code (dbt models) that engineers review and release like any other change. No parallel world of visual pipelines nobody can version.

First, as **bob**, on `retail` → **Edit**, add a member: the user `alice`, as an editor, and save. She builds from now on (Lab 17 also needs two editors). A person given several roles has the one that gives the most, so the readers' rules no longer apply to her on `retail`; on `sales` she's still a reader.

As **alice**, now an editor of `retail`, open **Flows**; under **Start a flow**, name it `campaign_results` and press **New flow**. The canvas fills the window; every column, table, step and metric a step needs is picked from a dropdown, so what you can choose is what exists. The question: for each campaign, the revenue of the orders its converted responders placed in the thirty days after responding, against its budget.

1. **Add a step → Input**, pick the table `uploads_campaign_responses` from the dropdown (the project's tables, and the products it reads). The node shows 7 columns, the file's five and the import's own `_dlt_load_id` and `_dlt_id`: the canvas asked the catalog.
2. With it selected, **Add a step → Filter**, condition `converted`. Press **Preview** to see the rows, read on a sample as alice.
3. **Add a step → Input**, table `uploads_orders`; then select the filter node and **Add a step → Join**: inputs `filter` (left) and `input_2` (right) from the dropdowns, kind `inner`, on `customer_id` = `customer_id`. The node shows the joined columns; the clashing `customer_id` from the right gets `_right`.
4. **Add a step → Filter** on the join: `not test and not refunded and ordered_on >= responded_on and ordered_on <= date_add('day', 30, responded_on)`: the orders in the month after the response.
5. **Add a step → Input**, table `uploads_campaigns`; select the second filter and **Add a step → Join**: `filter_2` (left) and `input_3` (right), inner, on `campaign_id` = `campaign_id`.
6. **Add a step → Group by**: add the columns `name` and `budget` (a dropdown each); aggregates `sum amount as revenue`, `count_distinct customer_id as buyers`, `count as orders`.
7. **Add a step → Formula**: column `roi`, expression `revenue / budget`, type `double`.
8. **Add a step → Prepare**: one processor on `roi`: **Round**, decimals `2`. (A processor changes its column, or makes a new one when "as" is set; each argument is a value, never SQL.)
9. **Add a step → Output**, table `campaign_results`, with two checks: `name` not null, `name` unique.
10. Press **Preview** on the output: the whole company, since alice is an editor now (a reader's preview reads the EU rows only): **Store weekend** brought 2,353.46 on a budget of 300, an ROI of 7.84; **Partner push** 2.97; **September clearance** 1.52; **Summer sale** 1.40; **Back to school** 0.83 and **Enterprise outreach** 0.88 didn't pay back. Press **SQL** to read the model the platform wrote for each step.

Try breaking it: change the first join to `customer_id` = `responded_on`. The canvas refuses before anything runs: "their types differ; retype one first". Put it back.

**Saving** writes the steps as dbt models into the project's pipelines repo, on a branch with a merge request, and a DAG that builds the table on the schedule you give. It needs the project to have a repo (Lab 10). Without one the button says so; the flow still works as a preview.

**Check:** bob's preview of the output has six rows, one per campaign, with Store weekend's ROI at 7.84; the SQL tab shows one `select` per step with the step's definition as a comment above it.

---

## Lab 10: The project as code (with a git repo)

**The moment:** the campaign answer becomes a table that rebuilds itself every Monday, and a change to it is reviewed on its own copy of the data before it reaches anyone.

**Value:** every change to pipelines, models and the project itself is a reviewed change in git, tested on the data before it merges, and released to production by someone other than its author.

This lab needs a GitLab repo the platform can reach. Make the repo a project access token (Settings → Access tokens) with the **`api`** scope and the **Developer** role: the platform reads the repo with it, commits flows and scheduled notebooks on branches of their own, opens merge requests and reports reviews on them, and the project's Airflow clones with it. A token with only `read_repository`, or a Guest's or Reporter's, gets HTTP 403 from GitLab's API (the Flows tab says so). Give the project the token on **Connections** (kind **A git token**, named `retail-git`; the user is the token's name), then on the project's **Edit** page fill in the Pipelines part: the repo's address, the DAGs' folder (`dags`), the branch, the connection, and the GitLab review settings: the GitLab address is the server's (`https://gitlab.com`, not the repo's address), the project path is left empty (the repo's own), the dbt folder is `dags/dbt`. Within a minute the project has an Airflow of its own on Kubernetes, reading that repo.

1. **Flows → Save as merge request** (Lab 9's flow, with a schedule of `0 6 * * 1`): the models with their steps as comments, `sources.yml`, `schema.yml` with the checks, and the DAG, on a branch with a merge request. Bob opens the merge request in GitLab: readable dbt. He changes one model's SQL by hand and pushes; the canvas shows that step as code from then on, and the rest as steps.
2. **A change, tested first:** the merge request's dbt models are built on a data branch of the project's tables (a Nessie branch), its tests run there, and the merge request gets a status and a note: rows on the branch against production, which metrics moved, and the semantic model's tests with the answers that changed. Production is untouched until the merge.
3. **Merge.** Within a minute the **Pipelines** tab lists `flow_campaign_results`; press **Run now**. The run builds the models with dbt as the project's pipelines, and `campaign_results` is a table of the project's.
4. **Released by plan:** with `storscale/project.yaml` and `storscale/semantic.yaml` in the repo, the **Releases** tab plans a tag (what would change: members, rules, metrics) and an administrator other than the planner applies it. Lab 20 does it.

**Check:** after the run, `campaign_results` is on the Data tab and in the catalog with its lineage back to `uploads_campaign_responses`, `uploads_orders` and `uploads_campaigns` column by column, and a failing check on it is one of the project's alerts.

---

## Lab 11: Customers worth a call (a notebook on a schedule)

**The moment:** the sales team wants a list every Monday: the customers who order often but didn't answer the September campaign. Alice has it in a notebook; nobody wants it running from her laptop.

**Value:** an analyst's notebook becomes a pipeline with one button, and stays a reviewed, versioned change: no notebook running from someone's laptop, no copy pasted into a DAG by an engineer.

With the repo from Lab 10, as **alice**, open **Tools → Notebooks** (JupyterLab opens in the platform, signed in as her; its launcher also opens **VS Code** in the browser, on the same files, for those who prefer it, and the image has scikit-learn, statsmodels, geopandas and the other libraries an analysis needs) and make a notebook, `call_list.ipynb`, with one cell:

```python
import io, pandas as pd, buckets_lake
s3 = buckets_lake.s3()                      # Buckets, as the person running the notebook: no keys in it
read = lambda name: pd.read_csv(io.BytesIO(s3.get_object(Bucket="retail", Key=f"landing/uploads/{name}.csv")["Body"].read()))
orders, responses, customers = read("orders"), read("campaign_responses"), read("customers")
real = orders[~orders.test & ~orders.refunded]
spent = real.groupby("customer_id").agg(orders=("order_id", "count"), revenue=("amount", "sum")).round(2)
heard = responses[responses.campaign_id == 6].customer_id          # who answered September's campaign
calls = (spent[(spent.orders >= 3) & ~spent.index.isin(heard)]
         .join(customers.set_index("customer_id")[["first_name", "last_name", "segment", "region"]])
         .sort_values("revenue", ascending=False).reset_index())
s3.put_object(Bucket="retail", Key="landing/reports/call-list.csv", Body=calls.to_csv(index=False).encode())
calls.head(10)
```

1. Run it: thirty customers, Viktor Schmidt first (seven orders, 905.92), and a file in the project's landing area. It ran as alice, under her rules.
2. Press **Schedule** on the notebook's toolbar (it's also on the notebook's menu in the file browser). Pick `retail` (the projects she edits that have a repo) and `0 6 * * 1`: Mondays at six, UTC. The platform takes the notebook with its outputs cleared (what it computed may be data no repo should hold) and commits it with a small DAG to the project's repo, on a branch of its own, `storscale/schedule-call-list`, with a merge request.
3. Open the merge request in GitLab: `dags/notebooks/call_list.ipynb` and `dags/notebook_call_list.py`, five lines that say which notebook, which project and when. Scheduling again updates the same branch and merge request. Merge it.
4. Within a minute the **Pipelines** tab lists `notebook_call_list`. Press **Run now**. The run goes as the project's pipelines, not as alice: it reads the landing area and writes the report there because the pipelines may (and only there). When it ends, the run has a **notebook** link: the executed notebook, its outputs in it, kept by the platform.
5. Break it on purpose: change the cell to read a file that isn't there, schedule again, merge, run. The run fails, the project's alerts say so, and the kept notebook shows the cell and its error.

6. **A model instead of a rule.** "Seven orders" is a rule; the team wants a churn score: who's likely to stop ordering. First the table it will score, so the model learns from exactly what it'll be given: on the **SQL** tab, as alice,

   ```sql
   CREATE TABLE customer_features AS
   SELECT c.customer_id,
          count(o.order_id) AS orders,
          CAST(coalesce(sum(o.amount), 0) AS double) AS revenue,
          date_diff('day', c.signed_up, DATE '2026-10-01') AS tenure_days
   FROM uploads_customers c
   LEFT JOIN uploads_orders o ON o.customer_id = c.customer_id AND NOT o.test AND NOT o.refunded
   GROUP BY c.customer_id, c.signed_up
   ```

   (`work/templates/churn.ipynb` in her notebooks does the same with FLAML choosing the model; this is the plain version, to see each part.) Then, in a new cell of `call_list.ipynb` (it uses the first cell's `customers` and `real`), the same three columns, and who stopped (no order since 1 September), trained and logged in `retail`'s own MLflow workspace:

   ```python
   from sklearn.linear_model import LogisticRegression
   from sklearn.metrics import roc_auc_score
   per = real.groupby("customer_id").agg(orders=("order_id", "count"), revenue=("amount", "sum"), last=("ordered_on", "max"))
   f = customers[["customer_id", "signed_up"]].join(per, on="customer_id")
   X = pd.DataFrame({"orders": f.orders.fillna(0).astype(int), "revenue": f.revenue.fillna(0.0).astype(float),
                     "tenure_days": (pd.Timestamp("2026-10-01") - pd.to_datetime(f.signed_up)).dt.days})
   churned = (f["last"].isna() | (pd.to_datetime(f["last"]) < "2026-09-01")).astype(int)   # 14 of the 60
   mlflow = buckets_lake.mlflow("retail")       # the project's workspace, as alice
   mlflow.set_experiment("churn")
   with mlflow.start_run():                     # stamped with alice and the catalog's commit
       model = LogisticRegression(max_iter=1000).fit(X, churned)
       mlflow.log_metric("auc", roc_auc_score(churned, model.predict_proba(X)[:, 1]))
       mlflow.sklearn.log_model(model, name="model", registered_model_name="churn",
                                pyfunc_predict_fn="predict_proba", input_example=X.head())
   ```

   No other project sees the run, and the commit it's stamped with means the data it learned from can be read again exactly (Lab 19). On `retail` → **Models**, `churn` has version 1, registered by alice: **Write its card** (purpose, data, metrics, limits), and have **bob** approve the version, since alice can't approve her own. The alias `approved` now names it, and that's what a scoring pipeline loads (`models:/churn@approved`).
7. **Score every customer, every night.** In `retail`'s spec (**Edit** → JSON), add `"scoring": [{"name": "churn-nightly", "model": "churn", "table": "customer_features", "id": "customer_id", "output": "customer_scores", "schedule": "0 3 * * *"}]` and save. Within a minute the **Pipelines** tab lists `scoring_retail_churn_nightly`; press **Run now**. The run goes as the project's pipelines: it loads the approved version, reads `customer_features` through Trino and writes `customer_scores` afresh, each customer's `customer_id` (so it joins back), the chance they've stopped ordering, the model's version and when. On **Models**, the **Scoring** card shows the run: 60 rows, and no drift, since a version's first run is the reference every later run is compared with.
8. **When the customers change.** Next month's customers won't look like September's. Stand in for that: on the SQL tab, `UPDATE customer_features SET revenue = revenue * 3`, and run the scoring again. The run compares each input column with the reference (the population stability index): `revenue` has moved far past 0.2 and `orders` hasn't, so the **Scoring** card shows `revenue` in red, and the project's alerts (Overview) say "churn-nightly: its inputs have drifted (revenue, …)". That's the cue to retrain on the new data, register a version, and have it approved: its first run becomes the new reference. Grafana draws the same numbers (`storscale_scoring_drift`).
9. **Version 2, on a version of the data.** Retrain on the customers as they are now, and name the data it learns from, so anyone can read it again later however the table changes.
   1. **Name the data.** As alice, on the **Tags** tab, make a tag: `churn-v2-data`, at `now`, keep for `1y`, note "What churn version 2 learned from". The tag names the catalog's commit: every table of the project as it is at this moment, kept for a year.
   2. **Train on it, through SQL.** In a new cell of `call_list.ipynb` (it uses the earlier cells' `churned` and `customers`), read the features with `buckets_lake.sql()`, which runs the query as alice and records which tables it read, and log the run as before:

      ```python
      feats = buckets_lake.sql("retail", "SELECT customer_id, orders, revenue, tenure_days FROM customer_features ORDER BY customer_id")
      y = pd.Series(churned.values, index=customers.customer_id).loc[feats.customer_id].values
      X2 = feats[["orders", "revenue", "tenure_days"]]
      with mlflow.start_run():                 # tagged with the tables read (lake.tables) and the data's commit
          model = LogisticRegression(max_iter=1000).fit(X2, y)
          mlflow.log_metric("auc", roc_auc_score(y, model.predict_proba(X2)[:, 1]))
          mlflow.sklearn.log_model(model, name="model", registered_model_name="churn",
                                   pyfunc_predict_fn="predict_proba", input_example=X2.head())
      ```

      On **Models**, `churn` has version 2, registered by alice, with version 1 still beside it: nothing is replaced, each version stays loadable by its number (`models:/churn/1`). Update the card's **Data** line to say "customer_features at the tag churn-v2-data".
   3. **Approve it.** As **bob**, approve version 2: the alias `approved` moves to it. Run the scoring again: it loads version 2, its first run is the new reference, and the drift alert clears. `customer_scores` says version `2` on every row.
   4. **Read what it learned from.** Change the features again (`UPDATE customer_features SET orders = orders + 1`). On the SQL tab, **As of** `churn-v2-data`, `SELECT round(sum(revenue), 2), sum(orders) FROM customer_features` gives the table as version 2 saw it, not today's. In the catalog, `retail__churn` has `customer_features` as what it was trained on, at the table's snapshot when version 2's run started; the run itself carries the catalog's commit (`storscale.data_commit`, the tag's). The model's version and the data's version name each other.
   5. To put the table back for the next labs, run the `CREATE TABLE` of step 6 again as `CREATE OR REPLACE TABLE`. The next scoring run then shows drift against version 2's reference (it learned from the tripled revenue): the check doing its job, and the cue for a version 3.
10. **The web shop asks too.** The site wants the score as a customer signs in, not overnight. In the spec, add `"serving": [{"name": "churn", "model": "churn"}]` and save: the platform starts a server of `retail`'s own that loads the approved version, and **Models** → **Serving** shows it answering with version 1, and the address to call. As the web team's service would, with a Keycloak token (bob's here), post a customer's three columns:

   ```bash
   curl -s https://<platform>/api/projects/retail/serving/churn/predict -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" -d '{"dataframe_records": [{"orders": 1, "revenue": 40.0, "tenure_days": 300}]}'
   ```

   The answer is each class's chance, the second the chance they've stopped, and the version that answered: 2. As **carol**, the same call is refused; on **Audit**, bob's call is there (`model.called`, the version, one row: never the row itself) and carol's refusal beside it. The next version approved answers at the same address within a minute, nobody restarting anything.
11. **Where it sits in the catalog.** In the catalog (**Tools → Catalog**), search `churn`: the model `retail__churn` is a node between `customer_features` and `customer_scores` in their lineage, its card as its description, with the approved version, who registered and approved it, and the catalog's commit its training run started from: the data it learned from, findable again (Lab 19).

**Check:** the run's notebook shows the table with Viktor Schmidt first, and `landing/reports/call-list.csv` is in the `retail` bucket (read it from a notebook with `buckets_lake.s3().get_object`); `SELECT * FROM customer_scores ORDER BY score DESC` lists the customers likeliest to have stopped, each with version `2` of `churn`; **Models** lists versions 1 and 2, version 2 approved; and **As of** `churn-v2-data` reads `customer_features` as version 2 learned from it.

**What Part III gained:** the second and third questions answered: Store weekend pays back nearly eight times and runs again in December; Enterprise outreach is cut; and sales gets thirty names every Monday morning. Both answers are code in the repo, rebuilt on schedule, and a wrong change can't reach them without a review.

---

# Part IV: shared, and safe

Thursday. Marketing wants the orders for its own models. Acme Outdoors, a partner, wants the campaign results and its orders every week. And the privacy officer's question is still open: customer 35 has written to ask to be forgotten.

## Lab 12: A certified data product, read in place

**The moment:** marketing asks for the orders, and gets them where they are, under retail's rules, with retail's definitions.

**Value:** one team publishes a table with a promise (its columns, how fresh it stays, the checks it passes); another team reads it where it is, under the publisher's rules, with no copy to go stale, and builds its own metrics on the publisher's definitions. The governance group certifies what it has checked, and a broken promise is the publisher's alert, naming who depends on it.

1. **Publish.** As **bob**, open **Data products** (the Tools menu) and press **Publish a data product** (anyone who may write a project's tables may). Fill it in:
   - **From the project** `retail`, **Its table** `uploads_orders`, **Name** `orders`, **Version** `1`;
   - **What it is**: "Harbor Outfitters' orders, net of test rows"; **Who answers for it**: `bob`;
   - **The business terms it holds**: tick **Revenue** (Lab 6);
   - **What it promises**: tick the columns `order_id`, `customer_id`, `ordered_on`, `region` and `amount`, and **Changed at least every** `24h`.

   Press **Publish it**. The page lists `orders`: its owner, version, contract, the term **Revenue** it holds, no consumers yet, not certified; its card has **Change** and **Remove** for retail's editors. (The same product is `retail`'s spec's `products`, which the form writes: `"products": [{"name": "orders", "table": "uploads_orders", ...}]`; a project kept as code (Lab 10) has it in its repo.) (Lab 6 shared retail's metrics, so other projects may build on them.)
2. **A second team asks.** Make a project `marketing` (Administration → Projects → New: `analysts` as editors, tick Tables, and in the JSON box `"semantic": {"uses": ["retail"]}`). As **alice** (an editor of `marketing`), open **Data products** and press **Ask for it** on `orders`, for `marketing`, with a reason.
3. **The publisher approves.** As **bob**, the product's card shows the request; approve it. Within a minute `marketing`'s people may read `iceberg.retail.uploads_orders` in place: as alice, on `marketing`'s SQL tab, `SELECT region, count(*) FROM iceberg.retail.uploads_orders GROUP BY 1` works. She sees every region because she's an editor of `retail` too (Lab 9); a consumer who isn't sees what retail's readers see, the EU rows: retail's rules hold for consumers. Nothing was copied; there's one table.
4. **On the publisher's definitions.** As alice, on `marketing` → **Metrics** → **Edit**, paste a model of marketing's own that builds on retail's:

   ```yaml
   metric:
     name: revenue_per_customer
     description: Revenue per customer, on retail's definitions of both.
     type: ratio
     type_params: {numerator: {name: revenue}, denominator: {name: customer_count}}
   ```

   Save, and ask for `revenue_per_customer` by `order__region`: the answer uses retail's `revenue` and `customer_count`, not defined again here, so marketing's revenue per customer is the company's revenue divided by the company's customers. A change to retail's model that would break marketing's is refused on save, naming `marketing`.
5. **Certify.** As **bob** (the install's certifiers are its administrators unless set otherwise), press **Certify** on the card: the product is marked certified, first in the list, and the catalog shows it as a data product with its table marked Gold. The certification records what it certified: the table, the version and the contract.
6. **A promise changed.** On the product's card, **Change**: untick `amount`, save. The certification lapses, because what was checked isn't what's promised now; `marketing` still reads it. Tick it back, save, and certify again. (**Remove** stays off while another project reads the product.)
7. **A promise broken.** Rename a contracted column: `ALTER TABLE uploads_orders RENAME COLUMN region TO country`. Within a minute `retail`'s Overview lists the broken contract, naming `marketing` as the consumer that depends on it; a merge request that would drop the column fails its status (Lab 10) saying the same. Rename it back.
8. **Taking it away** is the publisher's too: remove `marketing` from the product's consumers, and its reads are refused at once. Put it back.

**Check:** `marketing`'s alice reads `iceberg.retail.uploads_orders` in place (every region, as retail's editor too), asks for `revenue_per_customer` and gets an answer, and the Data products page shows `orders` certified with `marketing` as its consumer.

---

## Lab 13: Rules for readers, and every tool following

**The moment:** the campaign responses are the marketing team's to see, not every analyst's; and nobody outside the team needs the day a customer signed up.

**Value:** a rule is written once, in words anyone can read on the Access tab, and holds in SQL, dashboards, notebooks, the canvas, deliveries and assistants alike. There's no tool where the rule is forgotten.

As **bob**, open `retail` → **Edit**:

1. **Which tables readers see:** in "Tables readers may read", enter `uploads_orders, uploads_customers, uploads_campaigns`. Readers (the analysts) lose `uploads_campaign_responses`; editors keep everything.
2. **What they see of them:** in the JSON box, under `tables.readers`, add a mask beside the row filter from Lab 2:

   ```json
   "masks": [{"table": "uploads_customers", "column": "signed_up", "type": "MASK_DATE_SHOW_YEAR"}]
   ```

   Readers see the year a customer signed up, not the day. (The email and phone columns are already masked by their tags, Lab 4: a project's own masks and the install's protection rules add up.)
3. Save, and open **Access**: the rules in words. "Readers: the EU rows of uploads_orders; signed_up shows the year", and the three tables they may read.
4. To see them as a reader does, take alice's editor membership off for a moment (on **Edit**; she stays a reader through `analysts`), and as **alice**, see them hold: the Data tab lists three tables; **Preview** of `uploads_customers` shows `2026-01-01` for every date; the SQL tab refuses `uploads_campaign_responses`; a chart on the dashboard (Lab 7) still shows EU only; the assistant (Lab 8) answers EU only; the canvas's previews (Lab 9) read under the same rules. Then, as bob, put her back as an editor.
5. **Roles, not people:** rules name groups (`analysts`), so a new analyst gets them the day they join the group, and loses them the day they leave. A member named as a user (alice as an editor, Lab 2) is the exception for when a person needs more than their group.

**Check:** alice's preview of `uploads_customers`, while she's a reader, has every `signed_up` on January the first; bob's has the real dates.

---

## Lab 14: A file to a partner, approved

**The moment:** Acme Outdoors wants the campaign results and the orders it brought in, every Monday, in its own bucket. Last year someone emailed a spreadsheet.

**Value:** a delivery is declared once (what, in what shape, where, when), approved by someone other than who made it, approved again by a data-protection approver when it carries people's data, run on the schedule with a manifest and a log of every file sent, and reproducible from the data as it was. Nobody emails spreadsheets.

1. **The partner's bucket.** On a lab install Acme's bucket is one of the platform's: as **bob**, Administration → Projects → New: `acme`, "Acme Outdoors: what we send them", `engineers` as editors, tick **Files**. Its bucket is `acme`.
2. **Where to send.** On `retail` → **Connections**, add one of kind **An S3 bucket**, named `acme-bucket`: endpoint `http://buckets:9000`, bucket `acme`, prefix `from-harbor`, and an access key and secret that may write there (on a local install, Buckets' root keys, `BUCKETS_ROOT_USER` and `BUCKETS_ROOT_PASSWORD` in `stack/.env`; on a company install, the keys the partner gives). An SFTP server is the other kind of target, with its host key checked, and a PGP key encrypts what's sent.
3. **The delivery.** On **Edit**, in the JSON box, add:

   ```json
   "deliveries": [{"name": "campaign_results", "table": "campaign_results", "format": "csv", "files": "harbor_campaigns_{date}.csv",
                   "schedule": "0 6 * * 1", "target": "acme-bucket"}]
   ```

   Save, and open the **Deliveries** tab: `campaign_results` is there, awaiting approval, and bob, who made it, can't approve it. As **alice**, approve it. On **Pipelines**, `delivery_retail_campaign_results` appears; press **Run now**. The run writes `from-harbor/harbor_campaigns_<today>.csv` and a manifest with its checksum, and the Deliveries tab lists the run: the file, its size, its checksum, who approved.
4. **People's data needs a second approval.** As **alice**, add a second delivery, the orders Acme's partners brought in:

   ```json
   {"name": "partner_orders", "query": "SELECT order_id, customer_id, ordered_on, amount FROM uploads_orders WHERE channel = 'partner' AND NOT test",
    "format": "jsonl", "files": "harbor_partner_orders_{date}.jsonl", "schedule": "0 6 * * 1", "target": "acme-bucket"}
   ```

   `uploads_orders` is a people's table (Lab 2), so the Deliveries tab names the personal column, `customer_id`, and asks for two approvals: another editor's, and a data-protection approver's (on a local install, an administrator; on a company install, the privacy team's group). As **bob**, give both. Run it: the file lands beside the first, and the delivery records whose data it carried, so Lab 15's search can find it.
5. **A failed check sends nothing.** A delivery runs the table's checks (Lab 5) first; one that fails stops the delivery, and the project is alerted instead of the partner getting a wrong file.

**Check:** as bob, in a notebook, `buckets_lake.s3().list_objects_v2(Bucket="acme", Prefix="from-harbor")["Contents"]` lists both files and their manifests; the Deliveries tab shows both runs, each with who approved it.

---

## Lab 15: Where a person's data is, and erase a customer

**The moment:** customer 35 asks to be forgotten. The privacy officer has to say where their data is, what was sent about them, and delete it, inside the law's deadline, with a record.

**Value:** the privacy officer answers "what do we hold about this person, and where did we send it" from the platform, not from a hunt through mailboxes; and a request to be forgotten is answered inside the law's deadline by someone with the right to, in every table that holds the person, with a record the auditor can read, and without the platform itself keeping the person's ID. The company follows the CCPA: 45 days to answer, the request verified first, the recipients of the person's data told.

As **bob** (an administrator; on a company install, the data-protection approvers' group), on `retail`:

1. **Where their data is.** Lab 2 declared `uploads_customers` and `uploads_orders` by `customer_id`; Lab 4 found the columns. The catalog shows both (the `Protection` tags), and the Data page shows each column's protection. That's the register of personal data, kept as data changes.
2. **Who's in the data.** On the SQL tab: `SELECT * FROM uploads_customers WHERE customer_id = 35` shows them, and `SELECT count(*) FROM uploads_orders WHERE customer_id = 35` their eight orders, three of them through partners.
3. **What was sent about them.** On the Deliveries tab, type `35` and **Search**: each delivery of people's data is run again on the tables as they were when it read them, and the runs that carried this person's rows are listed with when, to whom and who approved: Lab 14's `partner_orders` run, to Acme. The search isn't recorded with the ID. The privacy officer now knows whom to tell.
4. **Erase.** Same box, **Erase this person**: when they asked (today), who verified the request (you, or "the privacy tool"), confirm. In seconds the answer says: 1 row from `uploads_customers`, 8 from `uploads_orders`, and the same rows taken out of the files the import reads them from, `customers.csv` and `orders.csv` in the landing area (cleansed too, so the next hourly import doesn't bring the person back), the delivery that had carried their data, the due date 45 days after the request, the record's ID, and until when the tables' old versions still hold the rows. `SELECT * FROM uploads_customers WHERE customer_id = 35` is empty now, for everyone.
5. **The record.** The Erasures list on the same tab: when, asked on, due by, what was deleted where, how many deliveries, by whom, verified by whom. Open the Audit tab: an `erasure` entry with the record's ID and the counts, and nowhere the number 35. The record is kept at least two years; it carries a hash of the ID, so a second request for the same person is recognisable, and never the ID.
6. **What the past keeps.** Lab 19 shows every version of a table is kept for the history window. The erased rows are in those old versions until the window passes (the record's "ages out by"), and in any close tagged while they were there (the record names the closes). That's the one place erasure and "the data as it was" meet; the record makes it visible rather than silent, and Finance and the privacy team decide between them what a close may keep.
7. **Retention, so there's less to erase.** Under `tables`, add `"retention": [{"table": "uploads_campaign_responses", "column": "responded_on", "keep": "730d"}]`. Responses older than two years are deleted by the nightly maintenance; press **Run now** on the Data page's Storage tab to see a round happen. Rows that age out never need erasing.

**Check:** customer 35 has no rows in either table, nor in `landing/uploads/customers.csv` and `orders.csv` (run `import_retail_uploads` again: they don't come back); the Erasures list has one entry with the due date 45 days after today and one delivery named; the Audit tab's `erasure` entry names the record and not the person.

**What Part IV gained:** marketing builds on retail's table and definitions without a copy; Acme gets its two files every Monday with nobody emailing anything; and the privacy officer's question, which used to take weeks of asking around, took a search and a button, with a record for the auditor.

---

# Part V: AI on the same rules

The following week. The team wants a model to write the Monday summary, sort what customers say about the products, and answer questions in the team's chat. The CFO wants to know what that costs, and the privacy officer wants to know what leaves the building.

## Lab 16: Models through the gateway, on a budget

**The moment:** before anyone pastes a provider's key into a notebook, the company puts one gateway in front of the models, with a budget per project.

**Value:** every call to a model goes through one gateway the company controls, with a key per project and per agent, each with a monthly budget: the spend is visible beside the budget, the project is warned at 80%, and at the budget the gateway stops answering rather than the bill growing. Nobody pastes a provider's key into a notebook. (On a company install the gateway is LiteLLM on the company's models, Anthropic's through Microsoft Foundry; a lab install runs the platform's stand-in, which answers by repeating the question and charges a cent per hundred characters, so a budget is reached in minutes.)

As **bob**, on `retail`:

1. **Give the project a budget.** On the project's Edit page, add `"llm": {"budget": 0.05}` to the spec (five cents a month, for the lab; a real project gets dollars). Save. Within a minute the **Agents** tab's gateway card says `retail: 0.00 of 0.05 spent`, and the **Connections** tab lists `retail-llm`, kind `llm`, added by `platform-operator`, with its key, the gateway's address and the budget set. The key is shown nowhere: a pipeline in the project's Airflow, a notebook's job, an agent's deployment or the canvas reads it as the project's connection (on Kubernetes, the Secret `retail-llm`).
2. **Ask through it.** For the lab, read the key as the administrator (`kubectl -n storscale get secret retail-llm -o jsonpath='{.data.llmKey}' | base64 -d`; on Compose it's `connections/retail/retail-llm.json` in the platform's bucket) and, in a notebook cell:

   ```python
   import requests
   r = requests.post("http://llm:4000/chat/completions", headers={"Authorization": "Bearer " + KEY},
                     json={"model": "default", "messages": [{"role": "user", "content": "How were sales in the west last week?"}]})
   print(r.status_code, r.json()["choices"][0]["message"]["content"])
   ```

   The stand-in answers by repeating the question; LiteLLM would answer from the model. Run the cell four times: the Agents tab shows `0.04 of 0.05 spent`, and the project's Overview carries an alert: the project has used 80% of its LLM budget.
3. **What may not leave.** Ask it to `Write to jane.doe@example.com that card 4111 1111 1111 1111 was declined`: the answer is `400`, "the question holds a card number and an email address, which may not leave the platform; it wasn't sent to the model". The gateway's guardrail looks at every question for what the platform masks in tables (card numbers, national IDs, bank accounts, email addresses, phone numbers) and for its own secrets (keys by their shape, a secret's name with a value), and refuses before the model sees it, naming what it found and never the value; the refusal costs nothing. The install may keep its own terms in as well (`llm.forbiddenTerms`: a codename, a client's name; a local install keeps "Project Falcon"): ask about *project falcon's launch* and the refusal says the question holds a term the company keeps in, without repeating it. The same guardrail runs in LiteLLM and in the stand-in. An agent's `buckets_lake.ask()` raises it in those words.
4. **At the budget.** Run it twice more: the second answer is `422`, "Budget has been exceeded", the gateway's own refusal, the same whether the stand-in or LiteLLM. The alert now says the project is at its budget and the gateway answers it no more this month.
5. **Raise it.** Change the spec to `"llm": {"budget": 1}`: the operator makes a new key (the old one answers no more, so a program holding it reads the connection again) and the tab shows `0.00 of 1.00 spent`. Labs 17 and 18 spend against it.
6. **An agent's own budget.** On the Agents tab, register `nightly-summary` as Lab 17 will, with a **budget** of `0.50`, and have alice approve it. Its row shows `0.00 of 0.50 spent` once the operator has made its key, kept beside its secret in the agent's connection; the agent spends against its own budget, not the project's, and an agent that loops is stopped at its budget with bob alerted. **Revoke** it: its key goes with its client.

7. **What was asked, by whom.** The **Model calls** card on the same tab (for the project's editors) lists every call of the project's keys and its agents': when, whose key, who asked, the model, tokens, cost and seconds, with totals by person and by model for the day, week or month. It's read from the gateway's own records; the questions and answers themselves are never kept.

**Check:** the Agents tab shows the project's spend and the agent's against their budgets; the Audit tab has the budget changes as project edits; a call past the budget is refused by the gateway, not by the platform.

---

## Lab 17: An agent of the project's own

**The moment:** the Monday summary writes itself, by a program with a name, an owner, an approval and a switch.

**Value:** a program that reads the project's data gets an identity of its own, only the tools it needs, an owner, approval before it runs, and a switch that stops it at once. The audit names it.

1. As **bob**, open `retail` → **Agents** → **Register an agent**: name `nightly-summary`, "sums the day's orders for the team's chat", tools `list_metrics` and `query_metrics`. Register. It awaits approval; bob can't approve his own.
2. As **alice** (an editor too), open the same tab and **Approve**. The page shows the agent's client ID and its secret, once. Give them to the program.
3. The program signs in to Keycloak with those (client credentials) and asks the MCP server for `revenue` by region: it gets the EU's numbers, since it's a reader of the project and the readers' rule (Lab 2) holds for it as for anyone, and the audit names `agent-retail-nightly-summary`. Ask it for `list_products`: refused, naming the two tools it may use.
4. Change its tools (add `list_products`): the approval is taken away, and its client with it, until alice approves again.
5. **Revoke** it: within a minute it signs in no more.
6. **Let the platform run it** (the project's repo, Lab 10, and its budget, Lab 16). Put a notebook in the repo under the pipelines' path, say `notebooks/nightly_summary.ipynb`:

   ```python
   import buckets_lake
   a = buckets_lake.agent()                                   # the MCP server, as the agent
   revenue = a.call("query_metrics", project="retail", metrics=["revenue"], group_by=["order__region"])
   night = a.call("list_runs", project="retail", limit=10)          # what ran overnight, and how it ended
   alerts = a.call("list_alerts", project="retail")                 # failed checks, stale tables, budgets
   print(buckets_lake.ask(f"Two sentences for the team's chat: revenue by region {revenue}; runs {night['runs']}; alerts {alerts['issues']}"))
   ```

   Register the agent again with the tools `query_metrics`, `list_runs` and `list_alerts` (the last two are read-only views of the Pipelines tab and the project's alerts, for an agent that reports), a **budget** of `0.50`, a **schedule** of `0 6 * * *` and the **notebook** `notebooks/nightly_summary.ipynb`, and have alice approve it. Within a minute or two the Agents tab names its pipeline, `agent_retail_nightly_summary`, on the Pipelines tab: run it now. The run checks the repo out and executes the notebook as the agent: `buckets_lake.username()` says `service-account-agent-retail-nightly-summary`, the audit names it, a tool it wasn't given raises in the notebook naming what it may use, and `ask()` spends against the agent's own budget. The executed notebook, outputs and all, is linked from the run, and the Audit tab has `agent.ran`.

7. **Or no notebook at all.** Register `triage` with the tools `list_alerts` and `list_runs`, a **budget** of `0.50`, a **schedule** of `0 7 * * *`, and, in place of a notebook, what it's to do: *Each morning, read the open alerts and the failed runs, and write a two-sentence summary for the team.* No repo needed. Once alice approves it, it's a pipeline like the other; run it now. The platform's runner gives the model the agent's tools, calls each one it asks for as the agent, and stops at its report; the run's record shows the task, each tool call with its answer, and the report.

8. **An agent that asks another, and calls the company's API.** On **Connections**, add `retail-tickets`, an **API token** for the ticketing system. On **Edit**, declare the API's operation as a tool of the project's: `"tools": [{"name": "open_tickets", "description": "The open support tickets about an order.", "url": "https://tickets.example.com/api", "method": "GET", "path": "/orders/{order_id}/tickets", "connection": "retail-tickets", "parameters": [{"name": "order_id", "in": "path", "required": true}]}]`. Register `weekly` with the tools `tool:open_tickets` and `agent:retail/triage`, a budget and a prompt (*Each Monday, ask triage for the week's alerts, look up the tickets on the orders they name, and write it up*), and have alice approve it. Its run calls `list_tools`, `ask_agent` (triage does its part as itself and reports) and `call_tool` (the platform calls the API with the token; the agent never sees it).

**Check:** on the Audit tab, `agent.registered`, `agent.approved`, `agent.ran` and `agent.revoked`, each with who did it, and `agent.asked` and `tool.called` for weekly's run; the agent's run notebook shows the agent's own name as who ran it.

---

## Lab 18: What customers say, sorted by a model

**The moment:** ninety reviews came in last quarter. The product team wants to know what they're about, without reading them one by one, and the privacy officer wants to know that the three customers who wrote their email address into a review didn't just have it sent to a model.

**Value:** a free-text column becomes a labelled one on the canvas, through the gateway on the project's key and budget, under the guardrail, and the labels are a table the dashboards and the next steps read. What a model may not see is masked first, in the same flow.

As **bob**, on `retail`:

1. **The reviews.** On **Flows** → **Uploads**, upload `reviews.csv` and **Import as a table** named `reviews`; run `import_retail_uploads` from the Pipelines tab. The Data tab has `uploads_reviews`: `review_id`, `order_id`, `customer_id`, `reviewed_on`, `stars` and `text`.
2. **The flow.** On **Flows**, new flow `review_topics`. **Add a step → Input**, table `uploads_reviews`. **Add a step → Ask a model**: column `text`, **Classify into labels** `delivery, sizing, quality, price, service`, instructions `Delivery is about shipping and packaging; sizing about fit; quality about the product failing; price about cost; service about staff and support.`, answer column `topic`. Press **Preview**: the platform asks the model about the sample's reviews, in one question for twenty rows, and the note under the rows says how many questions and what they cost. On a lab install the stand-in answers `delivery` for every row; on a company install LiteLLM's model reads the reviews. (If the sample holds one of the three reviews that end with an address, the preview is refused instead: "the question holds an email address, which may not leave the platform"; the next step is why.)
3. **Mask first.** Select the input node and **Add a step → Prepare**: one processor on `text`, **Replace by pattern**, pattern `[^@\s]+@[^@\s]+\.[a-z]+`, replace with `[email]`. Connect the model step to the prepare step instead of the input (pick `prepare` as its input). Preview the prepare step: the three reviews now sign off `Write back at [email].`; preview the model step: it answers for every row, whatever the sample. The guardrail saw nothing to refuse, because nothing personal was sent.
4. **Count them.** **Add a step → Group by** on the model step: by `topic`; aggregates `count as reviews`, `avg stars as stars`. **Add a step → Output**, table `review_topics`, check `topic` not null. Preview the output: a row per topic.
5. **Run it for real.** **Save as merge request** with a schedule of `0 7 * * 1`, merge, and run `flow_review_topics` from the Pipelines tab: the run asks the model about every review first (the task `llm_llm`, a thousand rows at most a run), writes the answers as a table the models read, then builds the rest with dbt. `review_topics` is a table of the project's, and a chart on it (Lab 7) shows the product team where the complaints are. The spend is on the Agents tab's gateway card, against the project's budget.

6. **Search what they say.** On the **Agents** tab, the **Knowledge banks** card: **Add a bank** named `reviews`, from the table `uploads_reviews`, its text column `text` and its id column `review_id`, and press **Add**, then **Build**. The platform embeds each review through the gateway (the install's embedding model, on the project's key) and writes the table `knowledge_reviews`, a table of the project's like any other. The three reviews with an address stop the build at the guardrail, as they stopped the model step: point the bank at the masked text instead (a view of the prepare step's output, or the flow's table) and build again. Then search *the zip broke* in the card's box: the reviews about zips and seams come first, by meaning, with their row to cite. An assistant asks the same with the tool `search_documents` (Lab 8), as the person.
7. **Pages, too, and kept fresh.** Upload the care guide (`care.md`) and the returns page (`returns.html`) on **Flows** → **Uploads**, and on the card **Add a bank** named `manuals`, from the folder `landing/uploads/`, built on the schedule `0 3 * * *` (`retail` has pipelines since Lab 10). With a schedule it's one of the project's pipelines, `knowledge_retail_manuals`, which builds it afresh each night as the project's pipelines; run it now. A search for *how do I wash it* finds the care guide, its file named to cite. On the canvas, an **Ask a model** step's mode **Nearest passage in a knowledge bank**, on `manuals`, gives each review the closest passage of the guides and where it's from: a reply drafted from what the company already says.

**Check:** `SELECT topic, reviews FROM review_topics` on the SQL tab gives a row per topic (one row, `delivery`, 90, with the stand-in); the Audit tab has a `flow.llm` entry saying how many rows were asked in how many questions and what it cost.

**What Part V gained:** the Monday summary, the review topics and any question in the chat go through one gateway under a budget the CFO can read, nothing personal reached a model, and every agent has a name on the audit log and a switch.

---

# Part VI: keep it true

End of the quarter. The board pack is due; Finance wants last month's number reproduced exactly; the auditor wants to know who read what; the engineers want to release the quarter's changes on purpose, and roll back if they must.

## Lab 19: The data as it was

**The moment:** Finance asks for September's revenue as it stood at the close, after a month of corrections.

**Value:** a number in last month's board pack can be reproduced today, to the row, however the tables have changed since. Month-end closes are kept for as long as Finance says, whatever retention does to the rest.

As **bob**, on `retail`:

1. Open **Data** → `uploads_orders` → **History**: every change to the table so far (the import's load, Lab 5's insert and delete, Lab 15's erasure), who made it, when, and how many rows it added or removed. Each entry has **Read it as it was**.
2. Change the data. On the **SQL** tab, run `INSERT INTO uploads_orders VALUES (306, 7, DATE '2026-10-01', 'EU', 'web', 88.0, false, false, 'by-hand', 'by-hand-306')` (the last two are the import's own columns, Lab 5), then `UPDATE uploads_orders SET amount = 1.0 WHERE order_id = 1`. Back on **History**: two more entries, yours.
3. On the import's first load (the entry just after the table's creation, which holds no rows), **Read it as it was**: the SQL tab opens with the table named at that commit (`"uploads_orders#<hash>"`). `SELECT count(*), round(sum(amount), 2) FROM ...` gives the table as the import left it: 305 rows and the old amount. The same query without the suffix gives today's table: one row more than before your insert, customer 35's rows gone, and the changed amount.
4. Any moment, not only a commit: on the SQL tab, type a date and time in the **As of** box under the query and press **Read the tables then** (or Enter). The query's tables are rewritten to the commit current at that moment. `now` puts them back.
5. **A close that's kept.** On the **Tags** tab, make a tag: name `close-2026-09`, at `now`, keep for `7y`, note "September's close". The tag names a commit; the platform keeps that commit's data however old it gets, where the rest of the history is kept for the install's window (the Data page says how long). On the SQL tab, **As of** `close-2026-09` reads every table of the project as it was at the close.
6. Reports and files that must be reproducible use the same thing: a delivery records the commit it read (Lab 14), and the catalog's lineage names the commit each run read.

**Check:** `SELECT count(*) FROM uploads_orders` as of `close-2026-09` counts your inserted row (the close came after it); as of the import's first load it gives 305.

---

## Lab 20: Released on purpose (with a git repo)

**The moment:** the quarter's changes to the project go to production as a tag, planned by one person and applied by another.

**Value:** what runs in production is a tag in git, planned by one person and applied by another, with the difference spelled out first; going back is applying an earlier tag, and the data written in between stays.

With the repo from Lab 10, put the project itself in it:

```
storscale/
  project.yaml              # the spec: members, tables and their rules, checks, products, deliveries, agents
  semantic.yaml             # the metrics, terms and tests of Lab 6
  targets/development.yaml  # what differs per install: laid over project.yaml (maps merged, lists replaced)
  targets/production.yaml
```

1. Tag the repo `v1.0`. As **alice**, open `retail` → **Releases** and plan `v1.0`: the platform checks the repo out at the tag in the project's Airflow, composes the spec for this install's target, checks it as any spec, and shows what would change: members, readers' rules, checks, products, and the metrics added, removed or changed.
2. As **bob** (an administrator other than the planner), **Apply**. The spec and the model are saved as if edited by hand, approvals kept, and the release is recorded: the tag, its commit, who planned, who applied, when. The Audit tab has it.
3. Change the model in the repo (revenue excluding partner orders, say), open a merge request: the review (Lab 10) builds it on a data branch and the note says which of the model's tests changed their answers and by how much, before anyone merges. Merge, tag `v1.1`, plan, apply: the next question on the Metrics tab follows the new definition.
4. **Rollback:** plan `v1.0` and apply it. The spec and the metrics are `v1.0`'s again; the tables' data written since stays, with its history (Lab 19), and the Releases tab says so.
5. A plan older than the project's current spec (someone edited it by hand in between) is refused: plan again from the repo, so the repo stays the truth.

**Check:** the Releases tab lists `v1.0`, `v1.1`, `v1.0` with who planned and applied each; `SELECT count(*) FROM uploads_orders` is unchanged by the rollback.

---

## Lab 21: Who did what

**The moment:** the auditor asks who read the customers' table this quarter, and whether anyone who shouldn't have tried.

**Value:** "who read the customers' table last month" is a search, not an investigation. Every read in Trino, every refusal, every change to a project and every approval is in one log, with the person, and for an assistant's question, the assistant and the person.

1. As **bob**, open `retail` → **Audit**. In **Search**, `uploads_customers`, and press **Show**: every query that touched it, by whom, allowed or refused, and the query itself. Then **Person** `alice` and **Result** **Refused only**: the reads her rules refused. **From** and **To** narrow it to the quarter; **Export CSV** (top right, for administrators) hands the auditor the same rows.
2. Make a refusal: as **alice**, on the SQL tab, `SELECT * FROM uploads_campaign_responses` (Lab 13 took it from the readers' list); or, as **carol** (no project), any query. Back on bob's Audit tab, the refusal is there within a minute, with the reason Ranger gave.
3. Filter by **Source**: Trino's reads (Ranger's log), the platform's own actions (projects saved, tags made, agents approved, classifications confirmed, uploads, deliveries approved, the erasure, the model asked), the assistants' questions (Lab 8: the agent and the person both named).
4. Administration → **Audit** is the same log for every project, for the platform's administrators.
5. Nothing in the log can be edited from the platform; it's written as things happen and read by whoever may.
6. **Every project at once.** The log says what happened; Administration → **Governance** says where things stand now, across every project. It's for the administrators, and for the products' certifiers and the data-protection approvers, who reach it from the account menu. **Awaiting approval** lists the agents, deliveries and requests for a data product that wait on someone, and whom. **Agents** lists every project's agents with their owner, tools, schedule, last run and spend against their budget (`nightly-summary`, `triage` and `weekly` from Lab 17). **What readers see** shows what each tag means across the platform (the mask, and who sees through it), then each project's readers with their tables, row filters, masks and tagged columns (`retail`'s readers' rules from Lab 13, the email addresses from Lab 4). **Data products** shows each product and whether it's certified (Lab 12). **Erasures** shows each one with its deadline (Lab 15). The page changes nothing; each row links to where the decision is made.

7. **A role of the company's own.** The auditor needs to read every table as it is and see the log, and to change nothing: neither an editor (who writes) nor a reader (who's masked). As **bob**, open Administration → **Roles**. Editor and reader are listed as the platform's permissions, built in, and so are the install's platform roles (finance, governance, data-protection). Press **Define a project role**: name `auditor`, "Reads every table as it is and the audit log; changes nothing", and tick **read-all**, **read-files** and **see-audit**. Press **Define it**: the card lists what it gives, read among them ("comes with another": read-all gives it).
8. **Give it.** On `retail` → **Edit**, add a member, the person `alice`, as `auditor` (the role is offered beside editor and reader; her group still makes her a reader too, and the platform takes the role that gives the most). Save. Within a minute, on **Access**, alice is an auditor, and the card says in words what that gives. As alice, on the SQL tab, `uploads_customers` shows the email addresses whole, where Lab 4 masked them for readers, and **Audit** is on her tabs; a `CREATE TABLE` is refused, and so is **Make a tag**.
9. **Change it, everywhere at once.** As bob, on the `auditor` card, **Change**: untick **read-all**, save. Within a minute alice reads as readers do again, in every project giving the role, and Administration → **Audit** has bob's change with what the role was before. **Remove** is refused while `retail` gives it: take alice's `auditor` membership off first.
10. **A platform role.** The privacy office's people work across every project: on the same page, **Define a platform role** `privacy-office`, given to their Keycloak group (`analysts` here, to try it as alice), with **protect-personal** and **see-governance**. Within half a minute alice sees the **Governance** page and could approve Lab 14's delivery and answer Lab 15's erasure without being an editor anywhere. Remove the role afterwards.

**Check:** the quarter is there in order: the uploads, the import's runs, the tags confirmed, the campaign flow saved and merged, the product certified, the deliveries approved, the erasure, the agent approved and revoked, the model asked, with the right names on each; and the `auditor` role's definition and change (`role.saved`) are among them, with bob's name.

---

## Lab 22: Operate it

**The moment:** the people who run the platform make sure it keeps running: told when something breaks, able to upgrade, able to restore.

**Value:** the people who run the platform see its health, who did what, and that one project can't slow another; the people who own a project are told when something breaks; an install is upgraded with one command and restored from a backup into a new cluster.

1. **Alerts:** on `retail`'s Overview, the project's issues: a failed pipeline run, a failing check, a table not changed in time, a column protected on the catalog's word, a broken product contract, a budget at 80%. Set a Teams or Slack channel (a connection holding the channel's webhook) or email on the project's Edit page to be told, and mute an issue for a day while it's being fixed.
2. **Audit:** the project's Audit tab, and Administration → Audit for the whole platform (Lab 21).
3. **Health:** Administration → Health: every service, as the platform checks it, with the features each one serves; Monitoring opens Grafana.
4. **History and closes:** a table's History and the **Tags** tab (Lab 19); the Data page's **Storage** tab shows each table's files and snapshots and the nightly maintenance (compaction, old snapshots expired, the window the history is kept for).
5. **Limits:** on a project's Edit page, how many queries at once, how much memory each, how much storage; one project can't starve the others, and a query that hits a limit is told so where it ran.
6. **What it costs.** Finance wants each project's month in money. `retail`'s **Usage** tab, for its editors, shows this month against last: its queries' CPU time, what it keeps (each table's data files and its bucket, sampled each day), and its model calls, each priced by the install's rate card (`usage.rates`: a CPU-hour, a GB-month; the model calls as the gateway priced them). Below that come who ran the queries, the costliest queries with their SQL, and the largest tables. Every query counts for the project whose tables it read, as Trino reports it finishing, so alice's questions from Lab 8 and the flows' runs are here too. On the Edit page, give the project `"cost": {"budget": 50}` (a month, in the rate card's currency): at 80% and again at 100% the project is alerted like any other issue, and nothing is stopped (the limits above do that). Administration → **Usage** lists every project's month, its budget and last month; the install's finance group sees the same page from the account menu (`usage.viewerGroups`). Spark isn't counted yet.
7. **From a script.** The account menu's **API reference** lists every route the web app uses, each with what it does (`/api/openapi.json`, for any client generator). From Python, `pip install "git+https://github.com/StorScale/storscale-platform#subdirectory=stack/python"`, then `p = storscale.Platform("http://storscale.localhost:8800")` asks for your name and password; `p.projects()`, `p.sql("retail", "SELECT count(*) FROM uploads_orders")`, `run = p.start_run("retail", "flow_campaign_results")` and `p.wait_for_run(...)` do as bob does in the web app, under bob's rules. A service account signs in with its client and secret instead.
8. **Upgrades:** `storscale upgrade --context <cluster> --version <x> --dry-run` shows what a release would change (the Project definition included), then without `--dry-run` applies it, waits for every service, and prints the rollback. The CHANGELOG says what each release needs.
9. **Removing a project.** On a project's Overview, the administrators' **Delete** takes away its people's access, its Airflow, its agents' clients and its keys at the gateway, and deletes the credentials it kept (its repo's token, its sources' and targets' passwords, its API tokens, its agents' secrets). Its tables and files stay, for whoever must decide what becomes of them.
10. **Backups (Kubernetes):** `storscale backup keygen --out <dir>` makes a key pair; the certificate goes to the chart (`backup.certificate`), the key somewhere other than the cluster. Each night a CronJob dumps every database, exports the projects and their secrets, and keeps them encrypted in the platform's own Buckets or another endpoint (`backup.s3`), the newest few; `backup.data` copies the buckets too. `storscale backup` takes one now; `storscale restore --key <key>` puts one back into the install it came from, and `--into-new` into another install when a cluster is lost. Restoring onto a fresh install is what CI checks a release with.

**Check:** Administration → Health shows every service up; a `--dry-run` upgrade to the current version changes nothing.

**What Part VI gained:** September's number is reproducible to the row for seven years, the quarter's changes went to production as a tag with a rollback in hand, the auditor's question is a search, and the platform is upgraded and backed up by command.

---

## What the quarter gained

| Lab | The company got |
|---|---|
| 1–2 | One sign-in; a project where the retail team's people, tables and rules are written once |
| 3 | Four files as tables, the ERP queried in place, in an afternoon |
| 4 | Email addresses masked for everyone but the privacy team, without a policy written |
| 5 | A table that reports its own faults before a reader notices |
| 6–8 | One revenue number, 22,168, in SQL, dashboards, apps and chat, each under the asker's rules; Revenue and Customer as the company's words |
| 9–10 | Store weekend pays back 7.8 times and runs again; Enterprise outreach is cut; the answer is code, reviewed, rebuilt weekly |
| 11 | Thirty customers to call, every Monday, from a notebook nobody runs by hand; a churn score for every customer each night, and for the web shop as it asks, from an approved model, that says when the customers stop looking like the ones it learned from |
| 12 | Marketing reads retail's orders in place and builds on its definitions; a broken promise alerts the publisher |
| 13 | Readers' rules in words, holding in every tool |
| 14 | Acme gets two files a week, approved, logged, reproducible; nobody emails spreadsheets |
| 15 | A customer erased in minutes with a record, 45 days ahead of the law |
| 16 | Models on a budget the CFO reads; nothing personal leaves the platform |
| 17 | The Monday summary written by an agent with a name, an owner and a switch |
| 18 | Ninety reviews sorted into five topics by a model, addresses masked first |
| 19 | September's close reproducible to the row for seven years |
| 20 | The quarter's changes released as a tag, with a rollback |
| 21 | The auditor's question answered by a search, and a role of the company's own that reads everything and changes nothing; every project's approvals, agents, rules and erasures on one page |
| 22 | Upgraded and restored by command; what each project costs a month, against its budget |

---

## Where to go next

- [use-cases.md](use-cases.md): the stories the platform is built around, phase by phase, with their requirements and what's built.
- [plan.md](plan.md): how each piece works, and what's next.
- The CHANGELOG: what each release added, and how to upgrade.
