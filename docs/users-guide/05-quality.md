# 5. Data quality

This chapter is for a table's owner: the engineer or analyst who wants a table to say when it's wrong or late before a dashboard reader notices. It covers the checks a project declares on its tables (all eight kinds), freshness targets, the profile the catalog keeps of every table, where the results show (the Quality view, the Overview, the catalog), the project's alerts and the channels they go to (Teams, Slack, email), and muting one for a while. Where the data comes from is [chapter 3](03-data-in.md); a data product's contract, which promises the same checks and freshness to its consumers, is [chapter 11](11-data-products.md).

In this chapter:

- [What the platform watches](#what-the-platform-watches)
- [Checks](#checks)
- [The eight kinds of check](#the-eight-kinds-of-check)
- [Freshness](#freshness)
- [Profiling: what a table holds](#profiling-what-a-table-holds)
- [The Quality view](#the-quality-view)
- [Project alerts](#project-alerts)
- [Where alerts go](#where-alerts-go)
- [Muting](#muting)
- [Settings](#settings)
- [What the audit log records](#what-the-audit-log-records)

---

## What the platform watches

| What | Declared in | Run by | How often | Shows on |
|---|---|---|---|---|
| Checks | `tables.checks` | catalog-sync, as OpenMetadata tests | Every round of catalog-sync (about every 20 seconds plus the round's work) | Data → Quality; Overview; the catalog's table page |
| Freshness | `tables.freshness` | platformd | Every minute | Overview's Alerts |
| Profile | Nothing: every table | catalog-sync, OpenMetadata's profiler | A table with no profile, or one over an hour old | Data → Columns; the catalog's table page |
| Project alerts | `notify` says where | platformd | Every minute | Overview's Alerts; Teams, Slack, email |

See [Lab 5 in the lab guide](../guide.md#lab-5-a-table-that-says-when-its-wrong).

---

## Checks

A check is a test on one of the project's tables: a column with no nulls, a row count in a range, values from a list, a query that must return nothing. The catalog runs each one, keeps its result on the table, and the project hears when one fails and when it passes again.

**Who can do it:** checks are in the spec, which administrators change (the Edit page's **JSON**: checks have no form of their own yet). Anyone who may see the project sees the results.

### Set up

On the project's Edit page (Overview → **Edit project**), press **JSON**, and under `spec.tables` add `checks`:

```json
{
  "tables": {
    "checks": [
      {"table": "uploads_orders", "check": "notNull", "column": "customer_id"},
      {"table": "uploads_orders", "check": "rowCount", "min": 300},
      {"table": "uploads_customers", "check": "unique", "column": "email"},
      {"table": "uploads_orders", "check": "acceptedValues", "column": "region", "values": ["EU", "US", "APAC"]},
      {"table": "uploads_orders", "check": "range", "column": "amount", "min": 0, "max": 10000},
      {"table": "uploads_customers", "check": "regex", "column": "region", "pattern": "^(EU|US|APAC)$"},
      {"table": "uploads_orders", "check": "references", "column": "customer_id", "refTable": "uploads_customers", "refColumn": "customer_id"},
      {"table": "uploads_orders", "check": "sql", "name": "no_refund_without_amount", "sql": "SELECT order_id FROM uploads_orders WHERE refunded AND amount <= 0"}
    ]
  }
}
```

Press **Save**. Through the API the whole project goes: `PUT /api/projects/{name}` with the project's JSON (as `GET /api/projects/{name}` gives its `spec`).

Each check has:

| Field | What |
|---|---|
| `table` | One of the project's tables |
| `check` | `unique`, `notNull`, `rowCount`, `acceptedValues`, `range`, `regex`, `references` or `sql` |
| `column` | The column, for all but `rowCount` and `sql` |
| `min`, `max` | `rowCount`: rows. `range`: the column's values. Whole numbers; one or both |
| `values` | `acceptedValues`: what the column may hold |
| `pattern` | `regex`: what every value matches |
| `refTable`, `refColumn` | `references`: the table and column every value is found in |
| `sql` | `sql`: a query that returns no rows when all is well |
| `name` | Optional; a `sql` check needs one. Lowercase letters, digits and underscores |

### How it behaves

- catalog-sync turns each check into an OpenMetadata test case on the table and runs the table's tests every round. The first result comes within a minute or two of the save, once the catalog has the table.
- Each test is named `<project>_<table>_<name or column>_<kind>` (a `regex` check ends in `_matches`): `retail_uploads_orders_customer_id_notNull`, `retail_uploads_orders_rowCount`, `retail_uploads_orders_no_refund_without_amount_sql`. The platform's pages drop the `<project>_<table>_` part.
- A check taken out of the spec is deleted from the catalog, with its results, at the next round.
- Checks run as the catalog's own Trino account: every row, but tagged columns masked ([chapter 4](04-access-and-protection.md#protection-tags-and-the-classifier)).
- A failing check is an issue on the project ([below](#project-alerts)) until it passes; its channels hear when it starts failing and when it passes again.

### Limits and gotchas

- Checks run on the table as it is now; they don't stop a pipeline's run or a write. A dbt model's own tests, which do, are [chapter 9](09-pipelines-and-git.md).
- Two checks of the same kind on the same column of a table get the same name; give one a `name`.
- **A check on a column a protection tag masks tests the masked value.** A `regex` of an email address on a tagged `email` sees its hash and fails; `acceptedValues` and `range` likewise. `notNull`, `unique` and `references` still answer truly on a hashed text or a hashed `bigint`/`integer` (a hash keeps nulls, duplicates and joins), but a tagged date or decimal is masked to null, so `notNull` on it fails.
- `min` and `max` are whole numbers: a `range` with `9.99` is refused when the spec is read ("not a project: json: cannot unmarshal number 9.99 …").
- The refused words, by kind:
  - "check 3: unique needs a column"
  - "check 2: rowCount needs a min, a max, or both"
  - "check 4: acceptedValues needs the values"
  - "check 6: regex needs a pattern"
  - "check 7: references needs refTable and refColumn (the table and column every value is found in)"
  - "check 8: sql needs the query (one that returns no rows when all is well)"
  - "check 8: a sql check needs a name (lowercase letters, digits and underscores)"
  - "check 1: "notnull" must be one of unique, notNull, rowCount, acceptedValues, range, regex, references, sql"

---

## The eight kinds of check

| Kind | Passes when | OpenMetadata's test |
|---|---|---|
| `unique` | No value of `column` appears twice | `columnValuesToBeUnique` |
| `notNull` | `column` has no nulls. A failure says how many: "nullCount=1" | `columnValuesToBeNotNull` |
| `rowCount` | The table's rows are at least `min` and at most `max` | `tableRowCountToBeBetween` |
| `acceptedValues` | Every value of `column` is one of `values` | `columnValuesToBeInSet` |
| `range` | Every value of `column` is at least `min` and at most `max` | `columnValuesToBeBetween` |
| `regex` | Every value of `column` that isn't null matches `pattern` (Trino's `regexp_like`, on the value as text) | A query of the values that don't match, which must return none |
| `references` | Every value of `column` that isn't null is found in `refTable`.`refColumn` | A query of the values the other table lacks, which must return none |
| `sql` | Your `sql` returns no rows | Your query, which must return none |

A `regex` pattern is matched anywhere in the value unless anchored: `^(EU|US|APAC)$` for the whole of it.

A `references` check's other table is one of the same project's tables.

A `sql` check's query may name the project's tables bare, as on the SQL tab: `FROM uploads_orders` and `JOIN uploads_customers` are read as the project's. Write it to return the rows that are wrong:

```sql
SELECT o.order_id
FROM uploads_orders o
JOIN uploads_customers c ON c.customer_id = o.customer_id
WHERE o.ordered_on < c.signed_up
```

Only names after `FROM` and `JOIN` are qualified; a table named elsewhere (after a comma, say) must be written in full, `iceberg.retail.uploads_customers`.

---

## Freshness

A freshness target says how recently a table must have changed. A table older than that is stale, and the project hears of it.

**Who can do it:** administrators (the Edit page).

### Set up

On the Edit page's form, under **Alerts**, **Tables that must change in time** → **Add a table**: the table, and the window (`24h`). In the spec, `tables.freshness`:

```json
{
  "tables": {
    "freshness": [
      {"table": "uploads_orders", "within": "24h"},
      {"table": "crm_leads", "within": "2h"}
    ]
  }
}
```

`within` is a number and a unit: `30m`, `24h`, `7d` (`m` minutes, `h` hours, `d` days).

### How it behaves

- Every minute platformd asks Nessie when each table's current snapshot was made. Past the window, the project has an issue: "uploads_orders is stale: not changed within 24h", with "last changed 2026-10-23 06:00 UTC" (or "never changed", for a table with no snapshot yet). It shows amber on the Overview, and the channels hear of it as a warning.
- When the table changes, the issue clears, and the channels are told it's resolved.
- A table that can't be looked at (missing, or Nessie not answering) shows on the Overview's Alerts as "Can't tell when uploads_orders last changed: …".

### Limits and gotchas

- Anything that commits a new snapshot counts as a change, whether or not the rows differ: the nightly compaction when it rewrites the table's files, and an import that replaces its table on every run (a files import without a primary key) can keep a table looking fresh. Set the window to what the business expects, and check the source too.
- "tables: freshness: within "1 day": as 30m, 24h or 7d".
- Freshness isn't shown on the Data tab or in the catalog yet: it's an alert.
- A data product's contract promises a freshness of its own to its consumers ([chapter 11](11-data-products.md)).

---

## Profiling: what a table holds

The catalog profiles every table of every project: its row count, and each column's share of nulls, how many distinct values, the smallest and the largest.

**Who can do it:** nothing to set up. Every member sees the profile.

### How it behaves

- catalog-sync profiles a table that has no profile, or one over an hour old, with OpenMetadata's profiler, on the whole table, as the catalog's Trino account. It takes a few of a project's tables each round (three), so after a restart the profiles fill in over a few minutes.
- **Data** → a table → **Columns**: above the columns, "305 rows, profiled 12 minutes ago."; the **Holds** column says what each holds: "no nulls · 3 distinct · 2 to 4 characters", "5% null · 280 distinct · 3.5 to 9999". For a text column the smallest and largest are its values' lengths.
- The catalog's own page for the table shows the same.

### Limits and gotchas

- The profile is the whole table's, unfiltered: a reader sees the row count and each column's range of rows the readers' rules hide from them, and of tables they can't read. A tagged column is profiled masked (its hash), so its values don't show.
- A table just made has no profile until the next round reaches it ("–" in **Holds**).

---

## The Quality view

**Who can do it:** any member.

- **Data** → a table → **Quality**: each check, its status (**passing**, **failing**, **aborted**, or **not run yet**), its last result ("nullCount=1") and when it ran. The tab shows a red count of the failing ones; the list of tables shows a red dot beside a table with one.
- **Overview**: a red banner while any check fails ("uploads_orders: customer_id_notNull is failing." with its result, and **See the check**); the **Checks** tile counts them ("6/7", "1 failing", "all passing", "none yet").
- **The catalog** (Tools → Catalog): the table's page shows the same tests and their results.
- `GET /api/projects/{name}/flow` gives every table's checks, with `status` (`Success`, `Failed`, `Aborted`, or empty: not run yet), `result` and `when`.

---

## Project alerts

platformd looks at every project each minute and keeps a list of what's wrong in it now: its issues. The Overview's **Alerts** card lists them; the project's channels are told as each starts and as it comes right.

**Who can do it:** every member sees the list; the channels are set by administrators (the Edit page); muting takes `mute-alerts`.

| Kind | The issue says | Told as | Resolved told |
|---|---|---|---|
| A pipeline's latest run failed | "import_retail_uploads's latest run failed", the run's ID | down | No: the next good run ends it quietly |
| A check failing | "uploads_orders: the check retail_uploads_orders_customer_id_notNull is failing", its result | down | Yes |
| A stale table | "uploads_orders is stale: not changed within 24h" | warning | Yes |
| A column the catalog tagged | "uploads_customers.email looks personal: the catalog tagged it, and it's masked" ([chapter 4](04-access-and-protection.md)) | down | Yes |
| A table's maintenance failed | "uploads_orders: its maintenance failed", the error ([chapter 15](15-history-and-releases.md)) | warning | Yes |
| A copy not running for 10 minutes | "the copy of dashboards isn't running (since 09:12 UTC)" ([chapter 3](03-data-in.md#change-capture-copies)) | down | Yes |
| The bucket filling | "the bucket retail is 80% full (…)", then "… is full (…): new files are refused" ([chapter 17](17-operating.md)) | warning, then down | Yes |
| A data product off its contract | "the product orders breaks its contract: …" ([chapter 11](11-data-products.md)) | down | Yes |
| The LLM budget, the monthly cost budget, a scoring's drift | As [chapter 14](14-ai.md), [chapter 17](17-operating.md) and [chapter 10](10-notebooks-and-models.md) say | | Yes |

Each issue links to where to look (the pipeline's runs, the table's Quality tab). `GET /api/projects/{name}/alerts` gives the list (`issues`, each with `key`, `kind`, `title`, `detail`, `link` and `mutedUntil`), where the project is told (`notify`), and what couldn't be looked at (`unknown`). An agent of the project reads the same list at `GET /api/agent/projects/{name}/alerts` ([chapter 14](14-ai.md)).

### How it behaves

- An issue is told once, when it starts. While it lasts it isn't told again.
- A check that passes again, a table that changes, and the rest of the kinds marked above are told as **resolved**.
- When platformd starts, what's wrong already isn't told: its first look only learns the list.
- When the project's `notify` changes (a channel named, an address added), everything wrong now is told at once to all its channels.
- A pipeline's issue is its latest run: a new run that succeeds ends it.

---

## Where alerts go

**Who can do it:** administrators (the Edit page). The Teams and Slack connections are given by anyone with `connections` ([chapter 3](03-data-in.md#connections)).

### Set up

1. On the project's **Connections**, add one of kind **A Teams channel** (`webhook`: the channel's Workflows webhook URL), or **A Slack channel** (`webhook`: an incoming webhook URL), named `<project>-…`.
2. On the Edit page's form, under **Alerts**: **Teams channel** (the connection's name), **Slack channel** (the connection's name), **Email** (addresses, comma-separated). In the spec, `notify`:

```json
{
  "notify": {
    "teams": "retail-alerts",
    "slack": "retail-alerts-slack",
    "email": ["retail-data@harbor.example"]
  }
}
```

3. Save. The Overview's Alerts card ends "Told to its Teams channel and retail-data@harbor.example."; with nothing set, "Not told anywhere yet: the platform's administrators set where, when they edit the project."

### How it behaves

- Teams gets an Adaptive Card; Slack a message with the title in bold, each fact on a line and the link; email a message through the install's mail server. Each says "<project>: <issue>" (or "<project>: resolved: <issue>"), the project and the detail, and links to the platform ("Open it in the platform").
- Email goes through the install's mail server, set in the platform's Secret `storscale-alerts` (`smtpServer`, `smtpFrom`, `smtpUser`, `smtpPassword`; [chapter 17](17-operating.md)). Without it, the addresses get nothing.
- An alert that can't be sent (a webhook refused, a connection missing) is in platformd's log ("project alert not sent"), not on the page.

### Limits and gotchas

- `notify.teams` must be a connection's name ("notify: teams "Retail Alerts" isn't a connection's name"); each address an address ("notify: "retail-data" isn't an email address"). The spec doesn't check the connection exists: a misspelt one fails when there's something to tell.
- The Overview's "Told to …" line names Teams and email, not Slack, though Slack is told.
- A Slack connection named by `notify.slack` can be removed while named (the Connections tab doesn't count it as used); the project's Slack alerts then fail.
- Not yet: the failing task and its error in a pipeline's alert (the run is named and linked), and a check's failing rows.

---

## Muting

Muting stops one issue's alerts for a while: while it's being fixed, or while a source is known to be down.

**Who can do it:** the permission `mute-alerts` (editors; administrators). Refused: "the project's editors mute its alerts".

### Set up

On the Overview's **Alerts** card, **Mute** beside an issue mutes it for a day ("No alerts about this for a day"); it then says "Muted until …" and offers **Unmute**. Through the API, for up to a week:

```bash
curl -X POST "$PLATFORM/api/projects/retail/alerts/mute" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"key": "stale/uploads_orders", "hours": 72}'
```

`key` is the issue's (from `GET /api/projects/{name}/alerts`: `check/<table>/<check>`, `stale/<table>`, `pipeline/<pipeline>/<run>`, `protect/<table>/<column>`, …); `hours` from 0 (unmute) to 168. The answer is the project's alerts, as the GET gives them.

### How it behaves

- A muted issue still shows on the Overview; it isn't told when it starts or when it's resolved.
- The audit log records `alert.muted`, with the issue and the hours.

### Limits and gotchas

- Mutes are kept in platformd's memory: a restart of platformd forgets them (the audit log keeps who muted what).
- A pipeline's issue is keyed by its run: muting it mutes that run's failure, not the next run's.
- "want {key, hours}: up to a week".

---

## Settings

| Setting | Where | Default | What |
|---|---|---|---|
| The install's mail server | Secret `storscale-alerts`: `smtpServer`, `smtpFrom`, `smtpUser`, `smtpPassword` | None | Projects' email alerts ([chapter 17](17-operating.md)) |
| catalog-sync's round | catalog-sync's environment, `SYNC_EVERY` | 20 seconds | How often checks run (not a chart value) |
| Profiles' age | catalog-sync's environment, `PROFILE_EVERY` | 3600 seconds | A table's profile older than this is made again (not a chart value) |
| Tables profiled per round | catalog-sync's environment, `PROFILE_PER_ROUND` | 3 | Per project (not a chart value) |

---

## What the audit log records

| Action | When |
|---|---|
| `project.saved` | The spec saved: checks, freshness and `notify` are in it, with which parts changed |
| `alert.muted` | An issue muted or unmuted, for how many hours |
| `connection.saved` | A Teams or Slack channel's connection added or changed |

See [Lab 21](../guide.md#lab-21-who-did-what) and [chapter 17](17-operating.md).
