# 13. Privacy

This chapter is for the privacy team (the platform's data-protection approvers) and the administrators: how the platform knows where a person's data is, how to find what was sent about a person to partners, how to erase a person from a project's tables and the files its imports read, what the erasure record holds, how retention keeps less to erase, and what the tables' history still keeps after an erasure. The platform follows the CCPA, as amended by the CPRA; the deadline is an install setting.

In this chapter:

- [The legal basis and the deadline](#the-legal-basis-and-the-deadline)
- [Who can do what](#who-can-do-what)
- [Where a person's data is](#where-a-persons-data-is)
- [What was sent about a person](#what-was-sent-about-a-person)
- [Erasure](#erasure)
- [The erasure record](#the-erasure-record)
- [What history keeps](#what-history-keeps)
- [Retention](#retention)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

See [Lab 15 in the lab guide](../guide.md#lab-15-where-a-persons-data-is-and-erase-a-customer) for customer 35 found, searched and erased, and [Lab 4](../guide.md#lab-4-personal-data-found-and-protected) for personal columns found by the catalog.

---

## The legal basis and the deadline

The company follows the CCPA as amended by the CPRA (decided 2026-10-19):

- a request to delete is verified first, and answered within 45 days (one 45-day extension with notice is the law's, not the platform's);
- the recipients of the person's data are told, so the platform names the deliveries that carried it;
- the record of the request is kept at least 24 months. The platform never deletes erasure records.

The platform computes each erasure's due date as the request's date plus `erasure.deadlineDays` (default 45). It doesn't stop you erasing after it; it shows the due date in the record, on the Deliveries tab's Erasures list and on the Governance page.

## Who can do what

Everything in this chapter takes the platform permission `protect-personal`. It comes with:

- the built-in platform role **data-protection**, given to the Keycloak groups in `deliveries.approverGroups` (default: the administrators' groups; Compose: `engineers,analysts`);
- any platform role an administrator defines on Administration → **Roles** with `protect-personal` ([Roles](16-roles.md));
- being an administrator.

| Action | Route | Needs |
|---|---|---|
| Search what was sent about a person | `GET /api/projects/{name}/deliveries/search?person=<ID>` | `protect-personal` |
| Erase a person | `POST /api/projects/{name}/erasures` | `protect-personal`; to rewrite the landing files too, permission to write the project's files (its editors, a role with `write-files`) |
| See the erasure records | `GET /api/projects/{name}/erasures` | `protect-personal` |
| See every project's erasures | Administration → **Governance** (`GET /api/governance`) | `see-governance` (the data-protection role has it); its Erasures card lists them only to those with `protect-personal` |

On the web app, the search and erasure live on the project's **Deliveries** tab, under **What was sent about a person**; you reach the project's tabs as a member of the project or as an administrator.

## Where a person's data is

### What it's for

Before anything can be found or erased, the platform has to know which tables hold people and which column says who. That declaration drives deliveries' approvals ([Deliveries](12-deliveries.md#approvals)), the search below, and erasure.

### Set up

As an administrator, on the project's **Edit** → **JSON**, add `tables.people`: each table holding people's data, and the column identifying a person in it.

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [
      {"group": "engineers", "role": "editor"},
      {"group": "analysts", "role": "reader"}
    ],
    "tables": {
      "people": [
        {"table": "uploads_customers", "column": "customer_id"},
        {"table": "uploads_orders", "column": "customer_id"}
      ]
    },
    "files": {},
    "pipelines": {"serviceAccount": "airflow-pipelines"}
  }
}
```

Each table may be listed once; table and column are identifiers. `PUT /api/projects/retail` saves it.

### How it behaves

- **The register.** Administration → **Governance** → **Readers' rules, by project** has a **People's data** column listing each project's `tables.people`, beside its masks and its tagged columns.
- **The columns.** The catalog's classifier tags personal columns (`personal`, `financial`, `health`) and masks them for everyone the install doesn't except; the **Data** page shows each column's protection. That's covered in [Access and protection](04-access-and-protection.md). `tables.people` is the declaration; the tags are what was found in the columns.
- **Who's in the data.** On the **SQL** tab, as someone who reads the tables unmasked (an editor): `SELECT * FROM uploads_customers WHERE customer_id = 35`.

## What was sent about a person

### What it's for

When a person asks what the company shared about them, or an erasure needs their data's recipients, the privacy team needs every file sent to a partner that carried that person's rows. The delivery log holds no one's ID; instead, each run of a delivery of personal data kept the catalog as it read the tables, and the search runs what that delivery sent again on the tables as they were, for that person's rows.

### Set up

1. Open the project → **Deliveries**. Below the deliveries, data-protection approvers see **What was sent about a person**.
2. Type the person's ID in **Their ID** (`35`, `C-1042`) and press **Search**.

Through the API:

```bash
curl "$PLATFORM/api/projects/retail/deliveries/search?person=35" -H "Authorization: Bearer $TOKEN"
```

```json
{
  "runs": [
    {"delivery": "partner_orders", "date": "2026-10-26", "sent": "2026-10-26T06:00:41Z",
     "target": "s3://acme/from-harbor", "approval": {"by": ["alice"], "protection": ["bob"], "at": "2026-10-22T09:14:03Z"},
     "rows": 3}
  ],
  "checked": 4,
  "notKept": 2
}
```

### How it behaves

- For each run in the project's delivery log that kept its read (a Nessie tag at the commit it read), the search runs the delivery's table or query again at that commit, as the platform's maintenance account in Trino, and counts the rows whose person's column, as text, equals the ID.
- The page says how many runs sent the person's data, how many were checked, and how many couldn't be (`not personal, or from before reads were kept`). The table lists each run with the person's rows: the delivery, its date, where it went, how many of their rows, and who approved it.
- A run shows **unclear** (hover for why) when its rows couldn't be counted: `what it sent has no column identifying a person (tables.people)` (a delivery of a masked column, but not of a people's table), or `it couldn't be run again: ...` (for example, the tag has expired).
- The audit log records `deliveries.searched` with how many runs were checked and how many had the person's rows, never the ID. A refused search is recorded too (`not a data-protection approver`).

## Erasure

### What it's for

A person asks to be forgotten. The platform deletes their rows from every table the project declares as holding people, takes them out of the landing files those tables are imported from (so the next import doesn't bring them back), names the deliveries that carried their data, and keeps a record for the auditor that holds a hash of the person's ID and never the ID.

### Set up

1. On the project's **Deliveries** tab, under **What was sent about a person**, type the ID in **Their ID** and press **Erase this person**.
2. The page asks **When did the person ask** (a date such as `2026-10-19`; empty means today), and **Who verified the request** (`the privacy tool`, or a person; empty means you).
3. Confirm: *Delete every row of 35 from this project's tables of people's data? This can't be undone, and is recorded.*

Through the API (the privacy tool's way in):

```bash
curl -X POST "$PLATFORM/api/projects/retail/erasures" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"person": "35", "requestedOn": "2026-10-19", "verifiedBy": "the privacy tool"}'
```

| Field | Required | Meaning |
|---|---|---|
| `person` | yes | The person's ID, as the people's tables hold it (up to 200 characters; spaces around it are dropped) |
| `requestedOn` | no | When the person asked, `YYYY-MM-DD`; default today. The due date counts from it. |
| `verifiedBy` | no | Who verified the request; default the caller |

From Python, with the [storscale package](17-operating.md#the-python-package-and-the-api):

```python
import storscale

p = storscale.Platform("https://storscale.example.com")
e = p.call("post_projects_name_erasures", {"person": "35", "requestedOn": "2026-10-19", "verifiedBy": "the privacy tool"}, name="retail")
print(e["id"], e["dueBy"], [(t["table"], t["rows"]) for t in e["tables"]])
```

### How it behaves

In order, within seconds:

1. **What was sent about them.** The same search as above runs first, on the tables as each delivery read them (the deletion doesn't change that), and its runs go into the record.
2. **The tables.** For each table in `tables.people`, the platform counts the rows whose identifying column, as text, equals the ID, and deletes them (an Iceberg delete through Trino), as the platform's **maintenance** account in Trino, which reads and deletes unmasked. From then on, nobody's query finds them in the current tables.
3. **The landing files.** If the project has a files import ([Getting data in](03-data-in.md)) that reads files under `landing/uploads/` into one of its people's tables (an import `uploads` with a file table `orders` makes `uploads_orders`), each file in the project's bucket matching that file's name or glob is read and written again without the person's rows:
   - **CSV**: the header row is kept; the column is found by name (any case); rows whose value, trimmed, equals the ID are dropped; the rest are written back.
   - **JSON lines**: lines whose value for the column equals the ID (a number written as `35`, not `35.0`) are dropped.
   - **Excel and Parquet** files aren't rewritten. Each is named in the record as not done: `a xlsx file isn't rewritten here: take the person's rows out of it by hand, or remove it` (or `a parquet file ...`).
   - This is done **as the person erasing**, with their own sign-in to Buckets: the bucket's policies decide. The project's editors may write its files; anyone else gets each file named in the record with why (`writing it again: Access Denied...`), and the tables are erased all the same.
   - A file without the person is listed with 0 rows and left as it was.
4. **Where the rows still are.** The record says how long the tables' history is kept (`historyKept`) and the date the erased rows age out of it (`agesOutBy`: now plus the history window), and names the project's tags (`closes`) that keep versions with the rows however old ([What history keeps](#what-history-keeps)).
5. **The record** is written to the platform's bucket, `erasures/<project>/<id>.json`, and the audit log gets an `erasure` entry.

The page then says, for example: *Erased. 1 rows from uploads_customers, 8 rows from uploads_orders; and from the landing files the imports read, 1 rows from customers.csv, 8 rows from orders.csv, so the next import doesn't bring them back; 1 deliveries had carried their data. Due by 2026-12-03; recorded as 20261019T101502-3f2a9c, without the ID. The tables' old versions hold the rows until about 2026-10-26.* Anything not done is listed under **Not done:**.

The answer is HTTP 200 when everything was done, and HTTP 207 (Multi-Status) when something wasn't: the record is kept either way, with `errors` naming each table or file and why.

## The erasure record

### What it holds

| Field | Meaning |
|---|---|
| `id` | When it ran, then six characters of the person's hash: `20261019T101502-3f2a9c` |
| `project` | The project |
| `personHash` | The first 16 hexadecimal digits of the SHA-256 of the ID. Never the ID: a second request for the same person is recognisable by it. |
| `requestedOn`, `dueBy` | When the person asked; that plus the deadline |
| `by`, `verifiedBy`, `at` | Who erased, who verified the request, when |
| `tables` | Each people's table, its column, and how many rows were deleted |
| `files` | Each landing file rewritten, its column, and how many rows were taken out |
| `deliveries` | The delivery runs that carried the person's rows (as the search returns them) |
| `deliveriesChecked`, `deliveriesNotCheckable` | How many runs were checked, and how many couldn't be |
| `historyKept`, `agesOutBy` | How long the tables' history is kept, and when the erased rows age out of it |
| `closes` | The project's tags, which keep their versions however old |
| `errors` | What couldn't be done, by table or file, and why (absent when everything was) |

### Where to read it

- **The Deliveries tab** → **Erasures** (data-protection approvers): each record's time, asked on, due by, what was deleted (table and file counts, "with errors" when there were some), how many deliveries, by whom, verified by whom. The card says *Each answered within 45 days of the request, as the law sets; the records are kept, without anyone's ID.*
- **`GET /api/projects/{name}/erasures`**: the records, newest first, with `deadlineDays`.
- **Administration → Governance** → **Erasures**: every project's, with its due date, who, and **erased** or how many tables or files were left.
- **The Audit tab**: an `erasure` entry, by the person who erased, whose detail names the record and counts, and never the ID: `20261019T101502-3f2a9c: 9 rows from 2 tables and 9 from 2 landing files, 1 deliveries had carried the person's data; verified by the privacy tool; due 2026-12-03`. A refused request is recorded as refused (`not a data-protection approver`).

## What history keeps

An erasure deletes the rows from the tables as they are now. Older versions of the data still hold them, for as long as the platform keeps those versions:

| Where | How long | What the record says |
|---|---|---|
| The tables' history (every version, for reading a table as it was: [History and releases](15-history-and-releases.md)) | The install's history window, `maintenance.history.keep` (default `P7D`, a week). Nessie's garbage collection then deletes versions older than that and the files no branch or tag needs. | `historyKept` and `agesOutBy` |
| The project's tags (a month-end close, `close-2026-09`) | Until each tag's `keepUntil`, however old | `closes` names the project's tags (all of them: it doesn't check which hold the person) |
| Each run of a delivery of personal data | Its read is tagged (`Delivery.<project>.<delivery>.<time>`) for `deliveries.keepFor`, default seven years: the search above depends on it | Not listed; the `deliveries` field names the runs |
| Backups (Kubernetes) | The newest `backup.keep` nightly backups (default 14), and the data copy if `backup.data` is on | Not listed |

The record makes these visible rather than silent; what a close or a delivery's kept read may keep is for the privacy team and Finance to decide. Taking a tag away early is an editor's **Remove** on the **Tags** tab ([History and releases](15-history-and-releases.md)).

## Retention

### What it's for

Rows that age out never need erasing. Retention deletes a table's rows older than a set age, every night.

### Set up

As an administrator, on **Edit** → **JSON**, add `retention` under `spec.tables` (here beside `people`):

```json
{
  "tables": {
    "people": [
      {"table": "uploads_customers", "column": "customer_id"},
      {"table": "uploads_orders", "column": "customer_id"}
    ],
    "retention": [
      {"table": "uploads_campaign_responses", "column": "responded_on", "keep": "730d"},
      {"table": "uploads_orders", "column": "ordered_on", "keep": "7y"}
    ]
  }
}
```

| Field | Meaning |
|---|---|
| `table` | One of the project's tables |
| `column` | The date or timestamp column that dates its rows |
| `keep` | Days or years: `730d`, `2y` (a year counts as 365 days; 1 to 99999) |

### How it behaves

- Each night at `maintenance.at` (02:00 UTC by default), table maintenance runs `DELETE FROM <table> WHERE <column> < <now minus keep>` as the maintenance account, before compacting the table's files. A project editor can start a round now: **Data** → a table → **Storage** → **Run now**.
- The deleted rows stay in the tables' history until the history window passes, like an erasure's.
- A project whose `tables.maintenance.paused` is `true` has no retention either.
- A failed retention step shows on the table's Storage tab and as the project's alert (`<table>: its maintenance failed`).

## Settings

| Chart value | Compose (platformd's environment) | Default | What it does |
|---|---|---|---|
| `erasure.deadlineDays` | `ERASURE_DEADLINE_DAYS` | `45` | Days from the request to the due date |
| `deliveries.approverGroups` | `DELIVERY_APPROVER_GROUPS` | the administrators' groups (Compose: `engineers,analysts`) | Who has the built-in `data-protection` role |
| `deliveries.keepFor` | `DELIVERY_KEEP_FOR` | `7y` | How long each personal delivery's read is kept, for the search |
| `maintenance.history.keep` | `NESSIE_GC_KEEP` in `stack/.env` (platformd reads it as `MAINTENANCE_KEEP_HISTORY`) | `P7D` (Compose's lab value: `PT2M`) | The tables' history window: when erased rows age out |
| `maintenance.at` | `MAINTENANCE_AT` | `02:00` | When retention runs |
| `audit.keep` | `AUDIT_KEEP` | `365d` | How long the platform's audit entries (the `erasure` entry among them) are kept |

## Limits and gotchas

- **The ID is matched as text, exactly.** `CAST(column AS varchar) = '35'`. `035`, `C-1042 ` with a different case, or a number stored with decimals won't match. Search first: the counts in the answer say what matched.
- **Only declared tables.** An erasure reaches `tables.people` and the landing files feeding them. Tables built from them (a dbt model, a flow's output, a model's training data), other projects' copies, files outside `landing/uploads/`, and data branches are not erased yet. Declare every table that holds people, or rebuild derived tables after an erasure.
- **No people's tables, no erasure.** `the project declares no tables of people's data (tables.people), so there's nothing to erase from`.
- **Landing files need write access.** A data-protection approver who isn't an editor of the project erases the tables, but each landing file is named in the record as not rewritten. Have an editor with `protect-personal` (or an administrator) erase, or rewrite those files afterwards; otherwise the next import brings the person back.
- **Excel and Parquet landing files** are named, not rewritten: take the rows out by hand, upload the file again, and run the import.
- **The search and erasure need table maintenance set up.** Without the maintenance account (`MAINTENANCE_TRINO_PASSWORD`) and the platform's files: `erasing needs the platform's files and its Trino account (maintenance)`.
- **Runs from before reads were kept**, and runs of deliveries without personal columns, can't be searched: they're counted as `notKept`.
- **The Deliveries tab only shows when the project has a delivery.** To search or erase in a project without one, open `/p/<project>/deliveries` directly, or use the API.
- **Not yet:** erasing through lineage (what was made from the people's tables), the declared files' prefixes beyond the imports' landing files, and the privacy tool's own sign-in (use a service account or a person's token today).
