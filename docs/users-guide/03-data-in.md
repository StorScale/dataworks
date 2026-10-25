# 3. Getting data in

This chapter is for the people who bring a project's data onto the platform: an engineer adding a feed from an application or a database, an analyst uploading a spreadsheet, an administrator wiring up a project's sources. It covers every way in: files uploaded to the project's landing area and imported as tables; imports from a REST API, a database, Salesforce, Gong, HighSpot or SAP S/4HANA, each on a schedule; a database queried where it is, with no copy; a database copied by change capture; and the connections that hold the credentials all of these sign in with. Who may read what arrives is [chapter 4](04-access-and-protection.md); checking that it's right is [chapter 5](05-quality.md).

In this chapter:

- [Which way in](#which-way-in)
- [Uploads and the landing area](#uploads-and-the-landing-area)
- [Imports: what they have in common](#imports-what-they-have-in-common)
- [A files import](#a-files-import)
- [A REST API import](#a-rest-api-import)
- [A database import (sql_database)](#a-database-import-sql_database)
- [A Salesforce import](#a-salesforce-import)
- [A Gong import](#a-gong-import)
- [A HighSpot import](#a-highspot-import)
- [An SAP S/4HANA import](#an-sap-s4hana-import)
- [Schedules and runs](#schedules-and-runs)
- [Reset: copy everything again](#reset-copy-everything-again)
- [Removing an import](#removing-an-import)
- [Sources: a database queried in place](#sources-a-database-queried-in-place)
- [Change-capture copies](#change-capture-copies)
- [Connections](#connections)
- [What the audit log records](#what-the-audit-log-records)

---

## Which way in

| You have | Use | Lands as | Fresh |
|---|---|---|---|
| A CSV, Excel, JSON-lines or Parquet file | An upload, then a **files import** | `uploads_<name>` (or `<import>_<name>`) | Each scheduled run reads the file again |
| An application with a REST API | A **rest_api import** | `<import>_<resource>` | On its schedule; only what's past the cursor |
| A PostgreSQL or MySQL table with a column that grows | A **sql_database import** | `<import>_<table>` | On its schedule; only what's past the cursor (deletes aren't seen) |
| Salesforce | A **salesforce import**, from the Imports tab's form | `<import>_<object>` | On its schedule; some objects by their changes |
| Gong (calls, transcripts, scorecards) | A **gong import**, from the Imports tab's form | `<import>_<object>` | On its schedule; calls and answered scorecards by what's new |
| HighSpot (spots, items, users) | A **highspot import**, from the Imports tab's form | `<import>_<object>` | On its schedule; all of it each run |
| SAP S/4HANA (partners, products, orders, documents) | An **sap import**, from the Imports tab's form | `<import>_<object>` | On its schedule; most objects by their last change |
| A database the company runs, needed in queries today | A **source** | A Trino catalog `<project>_<source>`, nothing copied | Always: queried live |
| A database whose every change should reach the lakehouse | A source with **copy** (Kubernetes only) | `<source>_<table>` | Within `copy.commitEvery` seconds (30) |

Everything that lands is one of the project's Iceberg tables, in its namespace, under its rules ([chapter 4](04-access-and-protection.md)), in the catalog with its lineage. See [Lab 3 in the lab guide](../guide.md#lab-3-get-the-data-in), which does the first and the fifth, and shows where the others start.

---

## Uploads and the landing area

A project's landing area is `landing/uploads/` in its bucket. An upload puts a file there, as the person who uploads it, and shows its first rows with the types the platform reads in them. A file in the landing area is nothing until a [files import](#a-files-import) makes it a table; the canvas ([chapter 8](08-flows.md)) then takes that table as an input.

**Who can do it:** uploading and removing an upload takes the permission `write-files` (editors have it). The **Uploads** panel's buttons show to those with `build` (editors). Listing and previewing take membership of the project; the bucket's own policy decides what each person may read (`read-files`: readers have it). **Import as a table** takes `connections` (it adds an import).

### Set up

1. The project needs **Files** (a bucket) and **Tables**. Both are ticked on the project's Edit page ([chapter 2](02-projects.md)). Leave the bucket's name empty: uploads go to the bucket named after the project.
2. Open the project → **Flows**. The **Uploads** panel is at the bottom.
3. Press **Upload a file** and pick a `.csv`, `.xlsx`, `.xls`, `.jsonl` or `.parquet` file. The panel shows the file's name, its size and, for a CSV, a preview: each column's name and type, and the first 20 rows.
4. Press **Import as a table** beside the file and accept or change the table's name (the prompt reads "The table's name (uploads_<name>):"). That's the [files import](#a-files-import).

Through the API, a multipart form with the field `file` (and optionally `name`, the file's name in the landing area):

```bash
curl -X POST "$PLATFORM/api/projects/retail/uploads" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" \
  -F file=@customers.csv
```

The answer holds the upload (`name`, `size`, `at`, `format`), its `preview` and the `table` name the platform suggests (`customers`).

| Route | What |
|---|---|
| `GET /api/projects/{name}/uploads` | The landing area's files, newest first |
| `POST /api/projects/{name}/uploads` | Upload one (multipart, field `file`) |
| `GET /api/projects/{name}/uploads/{file}` | A file's preview, read again |
| `DELETE /api/projects/{name}/uploads/{file}` | Remove it |

### How it behaves

- The platform writes the file as you: your Keycloak token is exchanged with Buckets' STS, so Buckets' policy for the project decides. A reader's upload is refused by the platform first ("the project's editors upload files to it"); a write the bucket refuses says "Buckets refused it: the bucket's policy doesn't let you write it".
- The preview reads the first 64 KB of a CSV. A header row names the columns (lower case, other characters as underscores; an empty one becomes `column_<n>`). A first line with more semicolons than commas is read as semicolon-separated. Each column's type is the narrowest every value fits: `bigint`, `double`, `boolean`, `date` (`2026-05-03`), `timestamp` (`2026-05-03T10:00:00Z` or `2026-05-03 10:00:00`), else `varchar`. An Excel, JSON-lines or Parquet file says "previewed once imported"; any other file says "not a file an import reads (csv, xlsx, jsonl, parquet)".
- The suggested table name is the file's name without its extension, lower case, other characters as underscores, at most 60 characters, and `file_` in front when it would start with a digit.
- The audit log records `upload` (with the file's name and size) and `upload.removed`.

### Limits and gotchas

- A file is at most 256 MB. Its name is letters, digits, dots, dashes and underscores, up to 121 characters, starting with a letter or digit ("a file's name is letters, digits, dots, dashes and underscores").
- Uploading a file of a name that's there already replaces it. The next run of its import reads the new one.
- Removing an upload doesn't remove its import or its table.
- Uploads and files imports use the bucket named after the project, whatever `files.bucket` says. Leave `files.bucket` empty, or uploads and imports look for a bucket that isn't there.
- The landing area is a folder of the bucket: anyone who may read the project's files (its readers) may read what's uploaded, whatever the readers' rules say of the table made from it.

---

## Imports: what they have in common

An import copies data into the project's tables on a schedule. It's [dlt](https://dlthub.com), run in the platform's Airflow as one of the project's pipelines: one Airflow DAG per import, `import_<project>_<import>` (dashes in the project's name become underscores), tagged `project:<name>` and `import`. Each of its resources lands in the project's namespace as `<import>_<resource>`: merged on its primary key when it has one, otherwise appended to (a files import replaces its table instead). Where a resource names a cursor, each run copies only what's past it; dlt keeps where it got to in the project's namespace.

**Who can do it:** adding, changing, removing and resetting an import takes the permission `connections` (editors have it); refused, it says "the project's editors change its imports (it takes the permission connections)". Running one now takes `run-pipelines`. An administrator may also write imports into the spec on the Edit page.

### The spec

`spec.imports` is a list; each import has:

| Field | What |
|---|---|
| `name` | 1 to 20 lowercase letters, digits and underscores, starting with a letter. Its tables are `<name>_<resource>` |
| `source` | `files`, `rest_api`, `sql_database`, `salesforce`, `gong`, `highspot`, `sap` or `files` |
| `connection` | One of the project's [connections](#connections): `rest_api` an API token, `sql_database` a database user, `salesforce` a Salesforce org, `gong` a Gong API key, `highspot` a HighSpot API key, `sap` an SAP communication user. A files import has none |
| `schedule` | Cron's five fields, in UTC (`0 * * * *`). Empty: hourly |
| `files` / `api` / `database` / `salesforce` / `gong` / `highspot` / `sap` | What it copies, by its source (below); only the one its `source` names |

### Three ways to add one

- **From a form.** Files: the Uploads panel's **Import as a table** (above). Salesforce, Gong, HighSpot and SAP: Imports → **Add Salesforce**, **Add Gong**, **Add HighSpot**, **Add SAP S/4HANA** (below). REST APIs and databases: the project's Edit page, section **Imports** → **Add an import** (administrators).
- **In the spec**, on the Edit page's **JSON** (administrators).
- **Through the API**, one import at a time, the rest of the spec as it is:

  ```bash
  curl -X PUT "$PLATFORM/api/projects/retail/imports/crm" \
    -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
    -d '{"source": "rest_api", "connection": "retail-crm-token", "schedule": "0 * * * *",
         "api": {"baseUrl": "https://crm.example.com/v1", "resources": [{"name": "leads", "path": "leads", "dataSelector": "data", "primaryKey": "id"}]}}'
  ```

  The import's name is the path's; a body that names one is ignored for it. `DELETE /api/projects/{name}/imports/{import}` removes it.

### How it behaves

- An import runs as the project's pipelines: they read its landing area and write its tables. An import added through the API or a form to a project without pipelines gives it pipelines (the shared Airflow's service account `airflow-pipelines`), and the audit log says "the project runs pipelines now (airflow-pipelines), as imports need".
- A project with an Airflow of its own (on Kubernetes, one with a pipelines repo: [chapter 9](09-pipelines-and-git.md)) still runs its imports in the platform's Airflow, signed in as that project's own pipelines (the platform's Airflow holds their sign-in as the connection `pipelines_<project>`). Its Pipelines tab lists them beside its own.
- The import's credentials go into the platform's Airflow as a connection of its own (`import_<project>_<import>`), handed to the run in its environment, never in its arguments or the log.
- Within a minute of the save the operator applies the project; Airflow then reads the import's DAG at its next parse (allow a few minutes) and it appears on the **Pipelines** tab.
- The import adds two columns of its own to every table it writes: `_dlt_load_id` (the run that wrote the row) and `_dlt_id` (the row's own ID). They're never empty: a row you insert by hand needs them too ([Lab 5](../guide.md#lab-5-a-table-that-says-when-its-wrong) inserts `'by-hand'` and `'by-hand-307'`). The catalog never classifies them. dlt also keeps tables of its own in the namespace (`_dlt_loads`, `_dlt_pipeline_state`, `_dlt_version`).
- The catalog draws each import's pipeline upstream of its tables (lineage).
- A run that fails is one of the project's alerts ([chapter 5](05-quality.md#project-alerts)).

### Limits and gotchas

- An import needs the project to have tables: "imports: crm: its tables are the project's, and it has none (tables)".
- A resource with neither a primary key nor a cursor is appended to whole on every run: its rows pile up. Give it a primary key (rows merged), a cursor (only what's new), or both.
- Two resources that would land in the same table are refused: "imports: crm: two resources would both be crm_leads".
- An import written into the spec on the Edit page doesn't get pipelines by itself. Tick **Pipelines** too, or the run is refused its landing area (`Forbidden`).
- A connection named in the spec that the project doesn't have is told on the Overview, in the `imports` part of the status ("import crm: no connection "retail-crm" among the project's"). Through the API it's refused at once: "no connection retail-crm among the project's (its Connections tab)".

---

## A files import

A files import reads files of the landing area into tables: CSV, Excel (every sheet, stacked), JSON lines and Parquet, by the file's extension. It needs no connection.

### Set up

From the Uploads panel, **Import as a table** adds the file to the import called `uploads` (made the first time, hourly, `0 * * * *`), as the table `uploads_<name>`. Each file imported the same way joins it.

In the spec (`spec.imports`):

```json
{
  "name": "uploads",
  "source": "files",
  "schedule": "0 * * * *",
  "files": {
    "tables": [
      {"name": "customers", "file": "customers.csv", "primaryKey": "customer_id"},
      {"name": "orders", "file": "orders.csv"},
      {"name": "prices", "file": "prices-*.csv"}
    ]
  }
}
```

| Field | What |
|---|---|
| `files.tables[].name` | The table: `<import>_<name>` |
| `files.tables[].file` | A file of `landing/uploads/` (no folders), or a glob (`prices-*.csv`), ending in `.csv`, `.xlsx`, `.xls`, `.jsonl` or `.parquet` |
| `files.tables[].primaryKey` | Optional: rows merged on it |

### How it behaves

- Each run reads the files again as the project's pipelines (their Keycloak client's token, exchanged with Buckets' STS: the bucket's policy for `<project>-pipelines` lets them read and write `landing/` and nothing else of the bucket).
- Without a primary key, a run replaces the table with what the files hold now. With one, rows are merged on it: a row whose key is in the table is updated, a new key added, and a row gone from the file stays.
- An ISO date (`2026-05-03`) or timestamp (`2026-05-03T10:00`, `2026-05-03 10:00:00`, `2026-05-03T10:00:00.123456`) in a CSV, Excel or JSON-lines file becomes a `date` or a `timestamp`, as the preview said. A value that looks like one but isn't (`2026-02-30`) stays text. Parquet keeps its own types.

### Limits and gotchas

- A timestamp with a zone (`2026-05-03T10:00:00Z`, `+02:00`) stays text in the table, though the preview calls it a timestamp.
- A table imported before 0.31.0 has its dates as text. It stays so until its import is [reset](#reset-copy-everything-again).
- "imports: uploads: orders: a file of landing/uploads/ (no folders), ending in one of csv, xlsx, jsonl, parquet": a folder in the name, or an extension it doesn't read.
- "imports: uploads: a files import needs no connection: it reads the project's landing area as its pipelines".

---

## A REST API import

dlt's `rest_api` source: each resource is a path under the API's base address answering a list of objects; dlt follows the pages itself.

### Set up

1. On **Connections**, add one of kind **An API token** (`token`), named `<project>-<something>`. It's sent as a Bearer token. An API that needs none needs no connection.
2. On the Edit page (administrators), **Imports** → **Add an import**: the name, **a REST API**, the connection, the schedule (empty: hourly), the base URL, and for each resource its name, path, where in the answer the list is ("data selector"), its primary key, and a cursor (its field, its query parameter, where it starts). **Add a resource** for more. Or `PUT /api/projects/{name}/imports/{import}`, as above.

```json
{
  "name": "crm",
  "source": "rest_api",
  "connection": "retail-crm-token",
  "schedule": "0 * * * *",
  "api": {
    "baseUrl": "https://crm.example.com/v1",
    "resources": [
      {"name": "leads", "path": "leads", "dataSelector": "data", "primaryKey": "id",
       "cursor": "updated_at", "cursorParam": "since", "cursorStart": "2026-01-01T00:00:00Z"}
    ]
  }
}
```

| Field | What |
|---|---|
| `api.baseUrl` | `http://` or `https://` |
| `resources[].name` | An identifier: the table `<import>_<name>` |
| `resources[].path` | Under `baseUrl` (`leads`), never a full address |
| `resources[].dataSelector` | A JSONPath to the list in the answer (`data`), when it isn't the answer itself |
| `resources[].primaryKey` | Merged on it |
| `resources[].cursor`, `cursorParam`, `cursorStart` | The objects' field that grows (`updated_at`, an ID); the query parameter the API takes it in (`since`); the first value asked for. All three or none |

### How it behaves

The first run asks for `?since=<cursorStart>`; each run after asks from the largest `updated_at` it saw. [The imports suite](../../stack/tests/imports.py) shows it: a lead changed and one added reach `crm_leads` merged, none twice.

### Limits and gotchas

- "imports: crm: a rest_api import has an api: its baseUrl (http or https) and its resources".
- "imports: crm: leads: a cursor names its field, its query parameter and where it starts".
- The token's connection must be of kind `api` with a `token`; the operator otherwise says "import crm: its connection has no token".

---

## A database import (sql_database)

A PostgreSQL or MySQL table, read by a cursor column. It's for a table that only grows, or whose rows carry when they changed. Deletes in the database aren't seen; for those, use a [change-capture copy](#change-capture-copies).

### Set up

1. On **Connections**, add one of kind **A database user**: host, port (empty: 5432 or 3306), database, user and password. An import needs the host: a JDBC URL alone won't do.
2. On the Edit page, **Imports** → **Add an import** → **a database, by a cursor**, its kind, and each table as `schema.table` with its cursor and primary key columns. Or in the spec:

```json
{
  "name": "erp",
  "source": "sql_database",
  "connection": "retail-erp-db",
  "schedule": "30 * * * *",
  "database": {
    "kind": "postgresql",
    "tables": [
      {"table": "public.invoices", "cursor": "updated_at", "primaryKey": "invoice_id"},
      {"table": "public.regions", "primaryKey": "region_id"}
    ]
  }
}
```

The tables land as `erp_invoices` and `erp_regions` (the schema is dropped from the name).

### Limits and gotchas

- `database.kind` is `postgresql` or `mysql`: "imports: erp: a sql_database import has a database (its kind, postgresql or mysql, and its tables) and a connection".
- Tables of the same name in two schemas would land in the same table, and are refused.
- The connection needs a host, a user and a password: "import erp: its connection needs a host, username and password".
- The account needs only to read the tables. Give it a read-only account of the database's own.

---

## A Salesforce import

dlt's Salesforce source, from a form. Pick the objects; those that carry their changes are copied by them and merged on their `Id`, the others replaced on each run.

### Set up

1. On **Connections**, add one of kind **A Salesforce org** (fields [below](#connections)): a user, password and security token; or a user and password with a connected app's consumer key and secret; or an instance address and a session.
2. Open **Imports** → **Add Salesforce**. Name (default `crm`), **Sign in with** (the project's Salesforce connections; none yet: the form says to add one), the objects (those marked ↻ copy only what changed), **When (cron, UTC)** (default `0 * * * *`) and **History from** (a date; empty: all of it). Press **Add**.

```json
{
  "name": "crm",
  "source": "salesforce",
  "connection": "retail-salesforce",
  "schedule": "0 2 * * *",
  "salesforce": {"objects": ["account", "opportunity", "contact"], "since": "2024-01-01"}
}
```

| Objects copied by their changes (↻), merged on `Id` | Objects replaced each run |
|---|---|
| `account`, `campaign_member`, `event`, `opportunity`, `opportunity_contact_role`, `opportunity_line_item`, `task` | `campaign`, `contact`, `lead`, `pricebook_2`, `pricebook_entry`, `product_2`, `sf_user`, `user_role` |

`GET /api/connectors` lists the connectors the forms offer (Salesforce, Gong, HighSpot, SAP S/4HANA), each with its objects and those copied by their changes.

### How it behaves

- `since` applies to the objects copied by their changes: the first run starts at that date.
- `domain` on the connection picks `login` (production) or `test` (a sandbox). `caCertificate` is trusted beside the usual authorities, for a proxy that inspects TLS.
- Saved, the audit log's `import.saved` names the objects.

### Limits and gotchas

- "imports: crm: a salesforce import names its objects (salesforce.objects) and a connection (kind salesforce)".
- "imports: crm: "orders" isn't one of Salesforce's objects: account, campaign, …".
- `since` is a date: "imports: crm: since "1/1/24": a date, as 2024-01-01".
- The connection needs a user's name and password, or an instance's address and a session; otherwise the operator says so on the Overview.
- An SAP connector: not yet.

## A Gong import

Gong's calls, their transcripts and its scorecards, read from Gong's v2 API with an API key, from a form.

### Set up

1. In Gong, a technical administrator makes the API key: **Company settings** → **Ecosystem** → **API** → **Create**. It's an access key and its secret. Gong may give the company its own API address (shown on that page); otherwise it's `https://api.gong.io`.
2. On **Connections**, add one of kind **A Gong account's API key**: **Access key**, **Access key secret**, and **API address** only if Gong gave you one.
3. Open **Imports** → **Add Gong**. Name (default `gong`), **Sign in with**, the objects (all of them ticked to start), **When (cron, UTC)** and **History from** (the calls and answered scorecards from that date; empty: all of them). Press **Add**.

```json
{
  "name": "gong",
  "source": "gong",
  "connection": "sales-gong",
  "schedule": "0 3 * * *",
  "gong": {"objects": ["users", "calls", "call_transcripts"], "since": "2026-01-01"}
}
```

| Object | Table | How |
|---|---|---|
| `calls` ↻ | `<import>_calls` | Calls started since the last run's newest (from `since` the first time), merged on `id`. Private calls are left out |
| `call_transcripts` ↻ | `<import>_call_transcripts` | The transcript of each call that run copied (100 calls a request), merged on `call_id`; who said what is in `<import>_call_transcripts__transcript` and its `__sentences` |
| `answered_scorecards` ↻ | `<import>_answered_scorecards` | Scorecards answered on calls, from the last run's newest review, merged on `answered_scorecard_id` |
| `users` | `<import>_users` | Gong's users, replaced each run |
| `scorecards` | `<import>_scorecards` | The scorecards' definitions, replaced each run |

### How it behaves

- It signs in with the key and secret (HTTP Basic), pages through each list by Gong's cursor, and when Gong says it's sending too many requests (429), waits as long as Gong asks and tries again (up to 8 times).
- `call_transcripts` without `calls` still reads the calls (to know which transcripts to ask for), but only the transcripts are kept.
- No call since the last run is not an error: the run copies nothing new.
- Transcripts are what people said on calls: give the tables [protection tags](04-access-and-protection.md) or leave them out of readers' rules as you would any personal data, and see [chapter 13](13-privacy.md) for erasure.

### Limits and gotchas

- Gong allows about 3 requests a second and 10,000 a day for a company; a large first copy of transcripts can take a while, and may use a good part of the day's allowance.
- A wrong key or secret fails the run with Gong's 401 in its log, and the run is one of the project's alerts.
- "imports: gong: "deals" isn't one of Gong's objects: users, calls, call_transcripts, scorecards, answered_scorecards".

---

## A HighSpot import

HighSpot's spots, the items in them and its users, read from HighSpot's v1.0 API with an API key, from a form.

### Set up

1. HighSpot's API needs its **Platform Plus** add-on. A HighSpot administrator makes an API key and its secret in HighSpot's settings, and notes the API's address for the company's region, as `https://api-su2.highspot.com` (HighSpot says which).
2. On **Connections**, add one of kind **A HighSpot API key**: **API key**, **API secret** and **API address**.
3. Open **Imports** → **Add HighSpot**. Name (default `highspot`), **Sign in with**, the objects, **When (cron, UTC)**. Press **Add**. (There's no **History from**: each run copies all of it.)

```json
{
  "name": "highspot",
  "source": "highspot",
  "connection": "sales-hs",
  "schedule": "0 4 * * *",
  "highspot": {"objects": ["spots", "items", "users"]}
}
```

| Object | Table | How |
|---|---|---|
| `spots` | `<import>_spots` | The spots the key may view |
| `items` | `<import>_items` | Every item in each of those spots, with its spot in `spot_id` |
| `users` | `<import>_users` | HighSpot's users |

Each is replaced on every run.

### How it behaves

- It signs in with the key and secret (HTTP Basic), reads each list 100 at a time, and waits out HighSpot's 429s as Gong's are.
- What the key may see is what's copied: a spot the key can't view, and its items, aren't.

### Limits and gotchas

- No copy by changes: a large HighSpot is read whole on each run, so schedule it daily, not hourly.
- Items are the content's details (title, type, links), not the files themselves.

---

## An SAP S/4HANA import

S/4HANA's business partners, customers, suppliers, products, sales orders, billing documents and purchase orders, with
their items, read from its released OData APIs (the ones listed on SAP's Business Accelerator Hub, api.sap.com) as a
communication user, from a form. It's the platform's own connector: no SAP software runs on the platform.

### Set up

1. In S/4HANA, an SAP administrator makes a **communication arrangement** for the APIs below (in S/4HANA Cloud, from the
   communication scenarios that include them: `SAP_COM_0008` for business partners, customers and suppliers,
   `SAP_COM_0009` for products, `SAP_COM_0109` for sales orders, `SAP_COM_0120` for billing documents, `SAP_COM_0053`
   for purchase orders; on premises, with the services activated in the gateway, `/IWFND/MAINT_SERVICE`), with a
   **communication user** signed in by user and password, read access being enough. The platform must reach the
   gateway's address over HTTPS (ask the network team; the open question in [use case 8.4](../use-cases.md)).
2. On **Connections**, add one of kind **An SAP S/4HANA communication user**: the API's address, the user, its password,
   the **SAP client** if the system has several (`100`), and, if the gateway's certificate is signed by the company's own
   CA, that CA's certificate.
3. Open **Imports** → **Add SAP S/4HANA**. Name (default `erp`), **Sign in with**, the objects (business partners, sales
   orders and their items ticked to start), **When (cron, UTC)** and **History from** (the objects copied by their changes
   from that date; empty: all of it). Press **Add**.

```json
{
  "name": "erp",
  "source": "sap",
  "connection": "retail-sap",
  "schedule": "0 2 * * *",
  "sap": {"objects": ["business_partners", "sales_orders", "sales_order_items", "products"], "since": "2026-01-01"}
}
```

| Object | API and entity set | Merged on | How |
|---|---|---|---|
| `business_partners` ↻ | `API_BUSINESS_PARTNER` `A_BusinessPartner` | `BusinessPartner` | Changed since the last run, by `LastChangeDate` (a day: that day is read again, and merged) |
| `customers` | `API_BUSINESS_PARTNER` `A_Customer` | `Customer` | Replaced each run (SAP stamps no change on it) |
| `suppliers` | `API_BUSINESS_PARTNER` `A_Supplier` | `Supplier` | Replaced each run |
| `products` ↻ | `API_PRODUCT_SRV` `A_Product` | `Product` | By `LastChangeDateTime` |
| `sales_orders` ↻ | `API_SALES_ORDER_SRV` `A_SalesOrder` | `SalesOrder` | By `LastChangeDateTime` |
| `sales_order_items` ↻ | `API_SALES_ORDER_SRV` `A_SalesOrderItem` | `SalesOrder`, `SalesOrderItem` | The items of the orders a run copies |
| `billing_documents` ↻, `billing_document_items` ↻ | `API_BILLING_DOCUMENT_SRV` `A_BillingDocument`, `A_BillingDocumentItem` | `BillingDocument` (and `BillingDocumentItem`) | As sales orders |
| `purchase_orders` ↻, `purchase_order_items` ↻ | `API_PURCHASEORDER_PROCESS_SRV` `A_PurchaseOrder`, `A_PurchaseOrderItem` | `PurchaseOrder` (and `PurchaseOrderItem`) | As sales orders |

Each lands as `<import>_<object>`, its columns SAP's field names in lower case with underscores (`SalesOrder` becomes
`sales_order`, `TotalNetAmount` `total_net_amount`).

### How it behaves

- It reads each API's `$metadata` once a run, so the columns have their types: amounts and quantities are decimals, dates
  and times timestamps, though OData V2 sends them as text and `/Date(…)/`.
- It reads a thousand rows a request, follows the gateway's own paging (`__next`) when the gateway pages, and waits out
  a 429 or 503 (the gateway busy) as long as it's told, up to eight tries.
- Items are asked for forty orders or documents at a time (`SalesOrder eq '…' or …`).
- Choosing items without their orders still reads the orders (to know which items to ask for), but keeps only the items.
- Navigation properties (`to_Item`, `to_Partner`) aren't followed: an object is its entity set's own fields.

### ODP extractions: CDS views and extractors, by SAP's own delta

The released APIs stamp changes on a few objects and never record deletions. SAP's Operational Data Provisioning (ODP)
does both: the gateway serves a CDS view (SAP's `I_*` and `C_*` views, or the company's own) or a classic extractor
(`2LIS_11_VAITM`) as OData, and keeps a **delta queue** for each subscriber, so each read after the first returns what
was inserted, changed or deleted since. It's the way SAP allows other tools to extract data (SAP Note 3255746 rules
out ODP over RFC for them, not over OData).

**Set up in SAP** (the SAP team, once per extraction):

1. The CDS view must be delta-enabled: annotated `@Analytics.dataExtraction.enabled: true` with change data capture
   (`@Analytics.dataExtraction.delta.changeDataCapture.automatic: true`, or its mapping). Classic extractors are
   delta-enabled already.
2. In transaction `SEGW`, make a project, then on its Data Model **Redefine → ODP Extraction**: the context **ABAP Core
   Data Services** (a CDS view, by its SQL view name) or **DataSources/Extractors**. Generate it, and activate the
   service in `/IWFND/MAINT_SERVICE`. Its entity set is named `EntityOf<view or extractor>` (or `FactsOf…`, `AttrOf…`).
3. Give the platform's communication user access to the service.

**On the platform:** on the SAP import's form, **Add an ODP extraction**: a name of your own (its table is
`<import>_<name>`), the **service** (`ZSALES_ODP_SRV`), the **entity set** (`EntityOfZCDS_SALES_ITEM`) and the **key
fields** its rows are merged on (`SalesOrder, SalesOrderItem`, as SAP names them). An SAP import may have ODP
extractions alone, with no objects ticked. In the spec:

```json
"sap": {"objects": [], "odp": [{"name": "sales_items", "service": "ZSALES_ODP_SRV",
                                 "entitySet": "EntityOfZCDS_SALES_ITEM", "primaryKey": ["SalesOrder", "SalesOrderItem"]}]}
```

How it behaves:

- The first run subscribes (`Prefer: odata.track-changes`) and copies every row. SAP answers with a delta link, which
  the platform keeps with the import's own record of where it got to (dlt's state, saved only when a run has landed).
- Each run after reads the delta since: rows SAP marks deleted (`ODQ_CHANGEMODE` `D`) are deleted from the table by their
  key once the run has landed; an extractor's before images (`ODQ_ENTITYCNTR` `-1`) are left out; the rest are merged.
  `ODQ_CHANGEMODE` and `ODQ_ENTITYCNTR` aren't kept as columns.
- **Copy all again** drops the table and the kept delta link, so the next run subscribes again and copies everything.
  The old subscription stays in SAP's delta queue until the SAP team ends it (transaction `ODQMON`).

Limits and gotchas:

- **One subscription per user and service.** SAP's delta queue knows a subscriber by its user and service, so two
  imports reading the same extraction with the same communication user take each other's deltas. Give each import
  its own user, or each extraction its own service.
- **A delta not landed is read again.** A run that fails before it lands keeps the old delta link, and SAP repeats that
  delta. A run that lands and then fails to delete (a message in its log) leaves those rows until the next reset.
- An entity set that gives no delta link isn't an ODP extraction's: the run copies it whole each time, and says so in
  its log.
- "imports: erp: odp sales_items: the fields its rows are merged on (primaryKey), as SAP names them".

### Limits and gotchas

- **Deleted in SAP stays here.** A deleted order, or an item taken off an order, isn't seen (the APIs' change stamps
  don't record deletions): **Copy all again** (a reset) brings the tables back to what SAP has. An ODP extraction
  (below) sees deletions.
- Customers and suppliers are read whole each run; on a large system, schedule them daily.
- The communication user sees what its arrangement allows: an API not in the arrangement fails the run with SAP's
  403 in its log; a wrong password, 401; a wrong client, SAP's own message.
- Other entity sets of the released APIs aren't offered; a CDS view of SAP's or the company's comes in as an ODP
  extraction (below).
- "imports: erp: \"A_SalesOrder\" isn't one of SAP S/4HANA's objects: business_partners, customers, …": use the
  object's name from the table above, not the entity set's.

---

## Schedules and runs

**Who can do it:** the schedule is part of the import (`connections`); **Run now** takes `run-pipelines`.

- `schedule` is cron's five fields in UTC: `0 * * * *` on the hour, `30 6 * * 1-5` at 06:30 on weekdays. Empty means hourly. Anything else is refused: "imports: crm: schedule "hourly": five cron fields, as 0 * * * *".
- One run of an import at a time; a missed schedule isn't caught up.
- To run one now: **Pipelines** → pick `import_<project>_<import>` → **Run now**. Or `POST /api/projects/{name}/pipelines/{pipeline}/runs`. The audit log records `pipeline.started`.
- Each run's log is beside it on the Pipelines tab ("log", or "why it failed" for a failed run): `GET /api/projects/{name}/pipelines/{pipeline}/runs/{run}/log`. See [chapter 9](09-pipelines-and-git.md).
- A failed latest run is an alert to the project ([chapter 5](05-quality.md#project-alerts)).

---

## Reset: copy everything again

A reset drops the import's tables and where its cursors got to, then copies everything into new tables. Use it when a table's columns must change type (a table imported before 0.31.0, whose dates are text), when a cursor has gone wrong, or when the source was rewritten.

**Who can do it:** `connections` (editors). Refused: "the project's editors reset its imports".

### Set up

- **Imports** tab → the import's **Copy all again**; or on the Edit page, the import's **Reset** (it then reads "Copying all again…").
- `POST /api/projects/{name}/imports/{import}/reset`. The answer is the run's ID: `{"run": "reset-20261024T091500"}`.

### How it behaves

- The run drops every table of the namespace whose name starts with `<import>_`, and dlt's record of them, then copies from the start: from each cursor's `cursorStart`, from Salesforce's, Gong's or SAP's `since`, every file again.
- The tables are made afresh, with their columns' types as the data says now. **Their history before the reset goes with them**: the History tab and reading as it was ([chapter 15](15-history-and-releases.md)) start again at the reset.
- The audit log records `import.reset`: "its tables and where it got to dropped, all copied again".

### Limits and gotchas

- Any table of the namespace named `<import>_…` goes, whoever made it. Don't name your own tables after an import (`uploads_summary` beside the import `uploads` would be dropped).
- Until the run ends the tables are missing: queries, dashboards and checks on them fail meanwhile.

---

## Removing an import

**Imports** → **Remove** ("Remove crm? Its tables stay."), the Edit page's **Remove** beside it, or `DELETE /api/projects/{name}/imports/{import}`. The import's DAG and its Airflow connection go. Its tables stay, as data does: drop them on the SQL tab when they're no longer wanted. The audit log records `import.removed` ("its tables stay"). A missing one answers "no such import".

---

## Sources: a database queried in place

A source is one of the company's databases, queried where it is: a Trino catalog of the project's own, `<project>_<source>` (dashes in the project's name become underscores). Nothing is copied. Its tables join with the lakehouse's in the same query.

**Who can do it:** sources are in the spec, which administrators change (the Edit page). The connection is given by anyone with `connections`.

### Set up

1. Ask the database's owner for a read-only account. On **Connections**, add one of kind **A database user**: host, port, database (for Oracle, its service name), user, password; or a JDBC URL in place of host, port and database.
2. On the Edit page, **Sources** → **Add a source**: its name, kind, the connection, the schemas it may read (empty: all), and **Readers** if the project's readers may read it too. With Readers ticked, **Add a row filter** and **Add a mask** say what they see, each table as `schema.table`. Or in the spec:

```json
{
  "sources": [
    {
      "name": "finance",
      "kind": "sqlserver",
      "connection": "retail-finance-db",
      "schemas": ["budget"],
      "readers": true,
      "rowFilters": [{"table": "budget.lines", "filter": "region = 'EU'"}],
      "masks": [{"table": "budget.lines", "column": "approver", "type": "MASK"}]
    }
  ]
}
```

3. Save. Within a minute (on Kubernetes, up to a minute more while Trino sees the connection's file) the catalog `retail_finance` answers on the SQL tab:

```sql
SELECT l.region, sum(l.amount) AS budget, sum(o.amount) AS revenue
FROM retail_finance.budget.lines l
JOIN uploads_orders o ON o.region = l.region
GROUP BY l.region
```

| Field | What |
|---|---|
| `name` | 1 to 20 lowercase letters, digits and underscores, starting with a letter |
| `kind` | `postgresql`, `mysql`, `sqlserver` or `oracle` |
| `connection` | A database user of the project's |
| `readers` | `true`: its readers may read it too (default: editors and pipelines only) |
| `schemas` | Only these (default all) |
| `rowFilters`, `masks` | What its readers see; tables as `schema.table`. Mask types as for the project's tables ([chapter 4](04-access-and-protection.md#mask-types)) |
| `copy` | Copy its tables into the project's: [below](#change-capture-copies) |

### How it behaves

- The operator makes the catalog in Trino while it runs. The user and password go into a credentials file Trino reads, never into the statement Trino logs and Ranger audits; only the address is in the statement.
- The address is made from the connection: PostgreSQL `host:5432/database`, MySQL the whole server (each database a schema), SQL Server `host:1433` with the database and `encrypt=true`, Oracle `host:1521/service`. A `url` given is used as it is.
- Ranger lets the project's editors, read-all roles and pipelines read it, and its readers when `readers` is true. Nobody may write it.
- The catalog (OpenMetadata) describes its schemas, tables and columns as its own Trino account, which may read none of its rows.
- Removing a source drops its catalog.

### Settings

None of the chart's.

### Limits and gotchas

- "sources: finance: masks and row filters are what readers see, and its readers can't read it (readers: true)".
- A connection without a user and password: "source finance: its connection needs a username and a password".
- A `url` that isn't `jdbc:…`, or holds the word password: "its url holds a password (or a line break): the password goes in password, which Trino never logs".
- PostgreSQL, SQL Server and Oracle need the database: "a PostgreSQL source names its database".
- The connection's `sslmode` and `trustServerCertificate` are read by copies only. For a source queried in place, put such options in a JDBC URL.
- Heavy queries land on the company's database. When that's too much, copy it instead.

---

## Change-capture copies

A copy brings a source's tables into the project's own by change capture: a first copy of each, then every insert, update and delete. Each lands as `<source>_<table>` in the project's namespace, under its rules like any of its tables. Kubernetes only.

**Who can do it:** administrators (the spec); the database's administrator grants the account what change capture needs.

### Set up

1. The account needs the database's change log: PostgreSQL `REPLICATION` and `wal_level=logical`; SQL Server CDC on the tables; MySQL the row binlog.
2. On the Edit page, in the source's row, **copy: schema.table, …** (MySQL: `database.table`). Or `copy` on the source:

```json
{
  "sources": [
    {"name": "dashboards", "kind": "postgresql", "connection": "retail-dashboards-db",
     "copy": {"tables": ["public.dashboards", "public.slices"]}}
  ]
}
```

The tables land as `dashboards_dashboards` and `dashboards_slices`.

### How it behaves

- The operator runs a Deployment per copying source, `copy-<project>-<source>`, with Debezium Server and its Iceberg sink. It reads the change log and writes the tables itself through Nessie, keyed on each table's primary key: inserts and updates upserted, deletes applied, new columns added as they come, decimals kept exact.
- A change is committed to the table within `copy.commitEvery` seconds.
- On PostgreSQL it makes its own replication slot and publication, both named `storscale_<catalog>` (`storscale_retail_dashboards`).
- Where it got to is kept in Iceberg tables of the platform's own namespace, `storscale_cdc`.
- The project's **Overview** shows a **Copies** card: each copied table and when its last change was committed. `GET /api/projects/{name}/copies` says the same.
- A copy that isn't running for 10 minutes is an alert to the project: "the copy of dashboards isn't running (since 09:12 UTC)".
- The catalog draws each source table to its copy, column by column.

### Settings

| Chart value | Default | What |
|---|---|---|
| `copy.commitEvery` | `30` | Most seconds between a change and its commit (at least 5) |
| `copy.memory` | `1Gi` | Each copy's pod |
| `images.debezium` | `ghcr.io/memiiso/debezium-server-iceberg:1.2.0.Final` | The copier |
| `postgres.logicalReplication` | `false` | The platform's own Postgres with `wal_level=logical`, so a source on it can be copied (tests) |

### Limits and gotchas

- Compose can't run it: the project's status says "copying a source needs Kubernetes (Compose can't start its pods)".
- Oracle can't be copied yet: "sources: erp: a oracle database can't be copied yet (postgresql, mysql, sqlserver can)".
- The project needs tables: "sources: dashboards: its tables are copied into the project's, and it has none (tables)".
- Two source tables of the same name in different schemas would land in one table, and are refused.
- The connection needs a host, user and password: "copying needs its connection's host, username and password (a url alone won't do)".
- Removing `copy` stops the copy; its tables stay. A PostgreSQL replication slot stays too, and the database keeps its log for it until its administrator drops the slot.

---

## Connections

A connection is a credential the project's editors give the platform once: a token, a database account, a key. The spec names it; members see its name, kind, who added it and when, its values that aren't secret, and where it's used; nobody sees a secret value again.

**Who can do it:** adding, changing and removing take the permission `connections` (editors). Any member sees the list. Refused: "the project's editors change its connections (it takes the permission connections)".

### Set up

1. Open the project → **Connections** → **Add a connection**.
2. **Name**: it must start with the project's name and a dash (`retail-crm-token`), in lower-case letters, digits and dashes, up to 63 characters.
3. **Kind**: pick one; its fields appear (required ones starred). Press **Add**.
4. To rotate a secret, press **New values** on the connection and paste the new one; a secret left empty stays as it was. **Save new values**.
5. **Remove** is greyed out while the spec names it ("Used by …").

Through the API:

```bash
curl -X POST "$PLATFORM/api/projects/retail/connections" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"name": "retail-erp-db", "kind": "database",
       "values": {"host": "erp.example.com", "port": "5432", "database": "erp", "username": "lake_reader", "password": "…"}}'
```

| Route | What |
|---|---|
| `GET /api/projects/{name}/connections` | Its connections (`values` not secret, `set` the secret keys that have a value, `usedBy`), and every kind's fields |
| `POST /api/projects/{name}/connections` | Add one: `{name, kind, values}` |
| `PUT /api/projects/{name}/connections/{connection}` | New values: `{values}` (and `kind`, for one made outside the platform) |
| `DELETE /api/projects/{name}/connections/{connection}` | Remove one the spec doesn't name |

### Every kind and its fields

Secret fields (marked †) are never shown again. "One of" means at least one of those keys has a value.

| Kind (`kind`) | Label | For | Fields |
|---|---|---|---|
| `git` | A git token | The pipelines' repo (https) | `username` (the token's name); `password`† (token; GitLab: the `api` scope, the Developer role), required; `webhookToken`† (GitLab webhook's token) |
| `ssh` | An SSH key | The pipelines' repo (ssh) | `sshPrivateKey`†, required, multi-line; `sshPrivateKeyPassphrase`† |
| `database` | A database user | Sources, copies and sql_database imports | `host`, `port`, `database`, `username` (required), `password`† (required), `url` (a JDBC URL, in place of host, port and database), `sslmode` (PostgreSQL), `trustServerCertificate` (SQL Server: `true`). One of `host`, `url` |
| `salesforce` | A Salesforce org | Salesforce imports | `username`, `password`†, `securityToken`†, `consumerKey`†, `consumerSecret`†, `organizationId` (sign-in from trusted IP ranges), `domain` (`login` or `test`), `instanceUrl` (with a session, in place of a user), `sessionId`†, `caCertificate` (multi-line). One of `password`, `sessionId` |
| `gong` | A Gong account's API key | Gong imports | `accessKey` (required), `accessKeySecret`† (required), `baseUrl` (default `https://api.gong.io`; the company's own, if Gong gave one), `caCertificate` (multi-line) |
| `sap` | An SAP S/4HANA communication user | SAP imports | `baseUrl` (required: the gateway's address, as `https://sap-gw.example.com:44300`, or S/4HANA Cloud's API address, as `https://my123456-api.s4hana.cloud.sap`), `username` (required), `password`† (required), `client` (the SAP client; empty: the system's default), `caCertificate` (multi-line) |
| `highspot` | A HighSpot API key | HighSpot imports | `apiKey` (required), `apiSecret`† (required), `baseUrl` (the region's API address, as `https://api-su2.highspot.com`; required), `caCertificate` (multi-line) |
| `api` | An API token | rest_api imports, and the project's tools (sent as a Bearer) | `token`†, required |
| `sftp` | An SFTP server | Deliveries' targets | `host` (required), `port`, `username` (required), `password`†, `sshPrivateKey`† (multi-line), `hostKey` (as in known_hosts, required), `path` (folder). One of `password`, `sshPrivateKey` |
| `s3` | An S3 bucket | Deliveries' targets | `endpoint` (empty: AWS), `region`, `bucket` (required), `prefix`, `accessKey` (required), `secretKey`† (required) |
| `pgp` | A recipient's PGP key | Deliveries' encryption | `pgpPublicKey` (armored, required, multi-line) |
| `agent` | An agent's secret | An agent of the project's (made when it's approved) | `clientId`, `secret`† (required) |
| `teams` | A Teams channel | The project's alerts (`notify.teams`) | `webhook`† (Workflows webhook URL), required |
| `slack` | A Slack channel | The project's alerts (`notify.slack`) | `webhook`† (incoming webhook URL), required |

Deliveries' kinds are [chapter 12](12-deliveries.md); the git and ssh kinds [chapter 9](09-pipelines-and-git.md); the agent's [chapter 14](14-ai.md).

### How it behaves

- On Kubernetes a connection is a Secret in the platform's namespace, labelled `platform.storscale.io/connection=<project>`; in Compose, a file in the platform's bucket (`connections/<project>/<name>.json`). A project names only its own: a Secret of the platform's or another project's is refused ("that name is taken by something that isn't one of the project's connections").
- A change asks the operator to apply the project at once: Airflow's copies of the credentials, Trino's sources' catalogs and the copies' pods follow within a minute.
- The audit log records `connection.saved` (its kind, "added" or "changed", and which keys: never their values) and `connection.removed`.
- Deleting the project removes its connections.

### Limits and gotchas

- The name rule: "a connection's name starts with the project's and a dash (retail-dags), in lower-case letters, digits and dashes". (Lab 3 of the lab guide names its database connection `dashboards-db`; on the project `retail` it must be `retail-dashboards-db`.)
- A name that's taken: "the project has a connection retail-erp-db already: give it new values".
- A key the kind hasn't: "token: no such key for A database user". A missing required field: "Password is needed". Neither of a one-of: "one of host, url is needed".
- A value is at most 64 KB.
- Removing one the spec names is refused, naming its uses: "still used by source finance, import erp". The spec's uses counted are the pipelines' repo, `notify.teams`, sources, imports, deliveries (target and PGP key) and tools. A Slack connection named by `notify.slack` isn't counted: removing it is allowed, and the project's Slack alerts then fail.
- A connection made outside the platform shows as "made outside the platform"; give it its kind with **New values**.

---

## What the audit log records

| Action | When | Detail |
|---|---|---|
| `upload` | A file uploaded | Its name and size |
| `upload.removed` | An upload removed | Its name |
| `import.saved` | An import added or changed (through the API or a form) | "added: files", the objects for Salesforce, Gong, HighSpot and SAP, and whether pipelines were given |
| `import.removed` | An import removed | "its tables stay" |
| `import.reset` | A reset asked for | "its tables and where it got to dropped, all copied again" |
| `pipeline.started` | **Run now** | The run's ID |
| `connection.saved` | A connection added or given new values | Its kind and the keys changed |
| `connection.removed` | A connection removed | |
| `project.saved` | The spec saved on the Edit page | Which parts changed |

The project's **Audit** tab shows them (permission `see-audit`); see [chapter 17](17-operating.md).
