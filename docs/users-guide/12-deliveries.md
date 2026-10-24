# 12. Deliveries to partners

A delivery sends files to a client or partner on a schedule: a table of the project's, or a query on its tables, written as CSV, Parquet or JSON lines, split into a file per value if asked, compressed and PGP-encrypted if asked, with a manifest beside them, to an SFTP server or an S3 bucket. It runs only once someone other than who changed it has approved it, and a delivery of people's data needs a data-protection approver too. This chapter is for the project's editors who set deliveries up, the people who approve them, and the administrators and privacy team who answer for them.

In this chapter:

- [What a delivery is for](#what-a-delivery-is-for)
- [Who can do what](#who-can-do-what)
- [Targets: where files go](#targets-where-files-go)
- [PGP: encrypting for the recipient](#pgp-encrypting-for-the-recipient)
- [Declaring a delivery](#declaring-a-delivery)
- [Formats and file names](#formats-and-file-names)
- [Checks before sending](#checks-before-sending)
- [Approvals](#approvals)
- [Runs and manifests](#runs-and-manifests)
- [The delivery log](#the-delivery-log)
- [Settings](#settings)
- [Limits and gotchas](#limits-and-gotchas)

See [Lab 14 in the lab guide](../guide.md#lab-14-a-file-to-a-partner-approved) for a delivery set up, approved and run end to end, and [Privacy](13-privacy.md) for searching what was sent about a person.

---

## What a delivery is for

Partners and clients want files: last week's campaign results every Monday, the orders they brought in, in their own bucket or on their SFTP server. Without a platform this is a script on someone's machine or a spreadsheet in an email, with nobody approving what leaves and no record of what was sent.

A delivery is declared once in the project's spec: what it sends, in what shape, where and when. It runs as one of the project's pipelines, sends nothing until approved, sends nothing when a check fails, writes a manifest with each file's checksum, and leaves a record of every run in the delivery log. When it carries people's data, the catalog as it read the tables is kept, so the log can be searched later for what was sent about one person.

## Who can do what

| Action | Who |
|---|---|
| Declare or change a delivery (the project's spec, on the **Edit** page or `PUT /api/projects/{name}`) | The platform's administrators. A release applied from the project's repo can change it too ([History and releases](15-history-and-releases.md)). |
| Add the target and PGP connections (the **Connections** tab) | The project's editors, or a role with the `connections` permission |
| See the **Deliveries** tab, its deliveries and their runs | The project's members, and administrators |
| Approve as an editor | The project's editors, a role with `approve`, or an administrator; never the person who last changed what the delivery sends |
| Approve as a data-protection approver (needed for people's data) | Anyone with the platform permission `protect-personal`: the built-in platform role `data-protection` (its groups are the install's `deliveries.approverGroups`), a platform role an administrator defines with `protect-personal`, or an administrator |
| Start a run now (**Pipelines** → **Run now**) | Whoever may run the project's pipelines: its editors, a role with `run-pipelines` |
| Search the delivery log by a person's ID | Data-protection approvers and administrators ([Privacy](13-privacy.md)) |

On the lab install, bob (engineers) is an administrator, so he is also a data-protection approver; on Compose the analysts (alice) are data-protection approvers too, so the example has one besides bob.

## Targets: where files go

A delivery's `target` is the name of one of the project's connections: either **An SFTP server** (kind `sftp`) or **An S3 bucket** (kind `s3`). The connection holds the address and the credentials; the spec only names it.

### Set up

1. Open the project → **Connections** → **Add a connection**.
2. **Name**: the project's name, a dash, then your own: `retail-acme-bucket`. A connection added on the page must start with the project's name and a dash, in lower-case letters, digits and dashes.
3. **Kind**: **An S3 bucket** or **An SFTP server**, then fill the fields below and press **Add**. Secret fields are written once and never shown again; **New values** on the card replaces them (a secret left empty stays as it was).

| Kind | Field (key) | Required | Notes |
|---|---|---|---|
| `s3` | Endpoint (`endpoint`) | no | Empty means AWS |
| | Region (`region`) | no | Default `us-east-1` |
| | Bucket (`bucket`) | yes | |
| | Prefix (`prefix`) | no | Files go under it: `from-harbor/harbor_campaigns_2026-10-26.csv` |
| | Access key (`accessKey`) | yes | |
| | Secret key (`secretKey`) | yes, secret | |
| `sftp` | Host (`host`) | yes | |
| | Port (`port`) | no | Default 22 |
| | User (`username`) | yes | |
| | Password (`password`) | one of these two, secret | |
| | Private key (`sshPrivateKey`) | | Takes precedence over the password when both are given |
| | The server's host key (`hostKey`) | yes | As a `known_hosts` line, or its last two fields: `ssh-ed25519 AAAA...` |
| | Folder (`path`) | no | Default the login folder |

The same through the API (`POST` adds, `PUT` gives new values):

```bash
curl -X POST "$PLATFORM/api/projects/retail/connections" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"name": "retail-acme-bucket", "kind": "s3",
       "values": {"endpoint": "https://s3.acme.example", "bucket": "acme", "prefix": "from-harbor",
                  "accessKey": "AKIA...", "secretKey": "..."}}'
```

`PUT /api/projects/{name}/connections/{connection}` takes `{"kind": ..., "values": {...}}` to change one; `DELETE` removes one that nothing names (the page's **Remove** is disabled while a delivery uses it).

### How it behaves

- The platform operator copies the target's values (and the PGP key, if any) into an Airflow connection of the platform's own, `delivery_<project>_<delivery>`, in the Airflow the project's pipelines run in (its own on Kubernetes when it has a repo, else the shared one). The run reads them from its environment; they never appear in its arguments or its log.
- An S3 target is written with path-style addressing, one object per file at `<prefix>/<name>`.
- An SFTP target's host key is checked before anything is sent. A different key stops the run: `the SFTP server's key isn't the one given (<type>): nothing sent`. That one isn't retried.
- Removing a delivery, or the project, removes its Airflow connection, and the target's credentials with it.

## PGP: encrypting for the recipient

### Set up

1. On **Connections**, **Add a connection** of kind **A recipient's PGP key** (`pgp`), named for example `retail-acme-pgp`, with the recipient's armored public key in **Public key (armored)** (`pgpPublicKey`).
2. Name it in the delivery's `pgp`: `"pgp": "retail-acme-pgp"`.

### How it behaves

Each file (after compression, when `gzip` is on) is encrypted for that key with GnuPG, as binary OpenPGP, trusting the key as given. The manifest is not encrypted: it names the files, their rows, sizes and checksums, and holds no data. The platform doesn't add `.pgp` to the names; put it in `files` yourself (`acme_{date}_{part}.csv.gz.pgp`).

A connection without `pgpPublicKey` puts the project's `deliveries` part in error: `delivery <name>: the connection "<pgp>" has no pgpPublicKey`. A key GnuPG can't import fails the run with `the recipient's key: ...`.

## Declaring a delivery

### Set up

Deliveries are part of the project's spec. As an administrator, open the project → **Edit** → **JSON**, add a `deliveries` list, and press **Save**. The project needs `tables` (a delivery sends them; without them the spec is refused), and should have `pipelines`, since a delivery runs as the project's pipelines.

| Field | Required | What it is |
|---|---|---|
| `name` | yes | 1 to 20 lower-case letters, digits and underscores, starting with a letter; unique in the project |
| `table` | one of `table`, `query` | One of the project's tables |
| `query` | | One `SELECT` (or `WITH ... SELECT`) on the project's tables, with no `;` |
| `format` | yes | `csv`, `parquet` or `jsonl` |
| `gzip` | no | `true` compresses each file |
| `files` | yes | The files' names: must hold `{date}`, and `{part}` when split; no folders |
| `splitBy` | no | A column: a file per value; goes with `{part}` in `files` |
| `schedule` | yes | Cron's five fields, in UTC: `0 6 * * 1` is Mondays at 06:00 |
| `target` | yes | A connection of kind `sftp` or `s3` |
| `pgp` | no | A connection of kind `pgp` |
| `checks` | no | What must hold before anything is sent: see [Checks](#checks-before-sending) |
| `changedBy`, `approval` | never by hand | The platform's own: who last changed what the delivery sends, and its approval. Whatever is written there is replaced by what the platform had. |

A complete spec for Harbor Outfitters' `retail` project with two deliveries, one of a table and one of a query on a people's table:

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "description": "Harbor Outfitters: orders, customers, campaigns",
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
    "pipelines": {"serviceAccount": "airflow-pipelines"},
    "deliveries": [
      {
        "name": "campaign_results",
        "table": "campaign_results",
        "format": "csv",
        "files": "harbor_campaigns_{date}.csv",
        "schedule": "0 6 * * 1",
        "target": "retail-acme-bucket",
        "checks": {"minRows": 1, "within": "7d"}
      },
      {
        "name": "partner_orders",
        "query": "SELECT order_id, customer_id, ordered_on, amount, region FROM uploads_orders WHERE channel = 'partner' AND NOT test",
        "format": "csv",
        "gzip": true,
        "files": "harbor_partner_orders_{date}_{part}.csv.gz.pgp",
        "splitBy": "region",
        "schedule": "0 6 * * 1",
        "target": "retail-acme-sftp",
        "pgp": "retail-acme-pgp",
        "checks": {"minRows": 1, "maxRows": 100000, "parts": ["EU", "NA", "APAC"]}
      }
    ]
  }
}
```

The same spec is saved with `PUT /api/projects/retail` (an administrator's token, header `X-Platform-Request: 1`). `GET /api/projects/{name}/deliveries` lists the project's deliveries as the Deliveries tab shows them.

### How it behaves

- Saving records the administrator as `changedBy` for each delivery that is new or whose sends changed (see [Approvals](#approvals)), and the audit log gets `project.saved` with the parts of the spec that changed.
- Within a minute the operator applies the project. Its **Overview** → **Applied to** card lists a `deliveries` part: green when every delivery's target (and key) was found and handed to Airflow; red with the reason when not.
- The **Deliveries** tab appears once the project has a delivery. Each card shows what it sends, its format, gzip and PGP, the split column, target, schedule and file names, the query if any, its personal columns (amber), who changed it, who approved it, and what it still needs.
- Only approved deliveries become pipelines. Within a few minutes of approval, `delivery_<project>_<name>` (dashes in the project's name become underscores: `delivery_retail_campaign_results`) appears on the **Pipelines** tab, tagged `project:<project>` and `delivery`.

## Formats and file names

| `format` | What's written |
|---|---|
| `csv` | A header row of the columns, then the rows; a null is written as an empty field |
| `jsonl` | One JSON object per line, column to value; dates and other values JSON doesn't have are written as text |
| `parquet` | One Parquet file, its types from the rows |

The files' names come from `files`:

- `{date}` is the run's date, `YYYY-MM-DD` (Airflow's date for the run).
- `{part}` is the `splitBy` column's value. Each distinct value gets its own file, holding that value's rows; files are made in the order of the values. A `splitBy` column that isn't in what's sent fails the run: `splitBy <column>: no such column`.
- `gzip: true` compresses each file (the name isn't changed; add `.gz` yourself).
- A manifest goes beside the files: `manifest_<delivery>_<date>.json`.

For `harbor_partner_orders_{date}_{part}.csv.gz.pgp`, split by region, the Monday 26 October run writes `harbor_partner_orders_2026-10-26_EU.csv.gz.pgp`, `..._NA.csv.gz.pgp`, `..._APAC.csv.gz.pgp`, and `manifest_partner_orders_2026-10-26.json`.

## Checks before sending

### Set up

Under the delivery's `checks`:

| Field | Meaning |
|---|---|
| `minRows` | At least this many rows |
| `maxRows` | At most this many (0 or absent: no upper limit); not below `minRows` |
| `within` | The table changed within `30m`, `24h` or `7d` (its latest snapshot's commit). Only for a delivery of a `table`; a query's is accepted and ignored. |
| `parts` | Every one of these values of `splitBy` is there; needs `splitBy` |

```json
{"checks": {"minRows": 1, "maxRows": 100000, "within": "24h", "parts": ["EU", "NA", "APAC"]}}
```

### How it behaves

The checks run first, as the project's pipelines in Trino. If any fails, nothing is sent: the run log says `checks failed, nothing sent:` and each problem (`12 rows, fewer than 100`, `campaign_results not changed within 24h (last ...)`, `parts missing: APAC`), the run fails, and the project's alerts say the pipeline's latest run failed, where its `notify` sends them ([Projects](02-projects.md)). The table's own quality checks ([Data quality](05-quality.md)) are separate: a delivery doesn't run them.

## Approvals

### What needs approving

An approval covers what a delivery sends and where: its `table` or `query`, `splitBy`, `format`, `target` and `pgp`. Changing any of these makes the delivery unapproved again, names whoever saved the change as `changedBy`, and takes its pipeline away until it's approved again. Changing `schedule`, `files`, `gzip` or `checks` keeps the approval.

Who must approve:

1. **An editor of the project other than `changedBy`**: the project's editors, a role with `approve`, or an administrator.
2. **A data-protection approver other than `changedBy`**, when the delivery sends personal columns. A delivery's personal columns are:
   - the columns the project masks for its readers (`tables.readers.masks`) of its table, or named in its query;
   - the person's column of a people's table (`tables.people`): the whole table when the delivery sends it, or, for a query, when the query names both the table and its person's column.

The card lists what's still needed in words, for example `Needs: an editor of the project other than bob; a data-protection approver (engineers, analysts).`

### Set up

On the **Deliveries** tab, a delivery that isn't approved has an **Approve** button for those with the `approve` permission. Press it. The answer says whether the delivery is now approved, and if not, who's still needed.

A person who is both (an editor and a data-protection approver, as an administrator is) gives both approvals with one press. A data-protection approver who is only a reader of the project doesn't see the button; they approve through the API:

```bash
curl -X POST "$PLATFORM/api/projects/retail/deliveries/partner_orders/approve" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1"
```

```json
{"approved": false, "needs": ["an editor of the project other than bob"]}
```

They must be able to see the project (a member in any role, or an administrator); otherwise the answer is `no such project`.

### How it behaves

- Each approval is recorded on the delivery's `approval`: the hash of what it sends, who approved as editors (`by`), who as data-protection approvers (`protection`), when, and whether it's complete (`done`).
- The audit log records `delivery.approved` with what it sends and its personal columns (`sends a query (csv) to retail-acme-sftp; personal: uploads_orders.customer_id`). A refused approval by the one who changed it is recorded as refused (`changed it`).
- Once complete, the operator lists it as approved, and the pipeline appears within a few minutes. It starts from its approval: a delivery approved on a Thursday sends next Monday's, not last Monday's (no catch-up), and one run at a time.
- The **Governance** page lists every unapproved delivery across projects under **Awaiting approval**, with who it waits for ([Operating](17-operating.md#governance)).

## Runs and manifests

### Set up

Nothing to set: an approved delivery runs on its schedule. To run it now, open **Pipelines**, choose `delivery_<project>_<name>` and press **Run now**, or `POST /api/projects/{name}/pipelines/{pipeline}/runs`.

### How it behaves

Each run, in order:

1. **Checks**, as above. A failing one sends nothing.
2. **Reads** the rows through Trino as the project's pipelines (their Keycloak token, client tag `project:<project>`, so the query counts against the project's limits and usage).
3. **Writes** the files: rendered in the format, split, gzipped and encrypted as asked.
4. **Writes a manifest**: the delivery, project, date, total rows, the table's latest snapshot ID (for a delivery of a table; a query's is empty), and each file's name, rows, bytes and SHA-256 (of the bytes as sent: compressed and encrypted).
5. **Sends** the files and the manifest to the target, trying three times (10 then 20 seconds apart); `sending failed (...); again in 10s` is logged between tries.
6. **Records** the run with the platform (the delivery log), and for a delivery of personal columns, platformd tags the catalog's commit the run read at (see [The delivery log](#the-delivery-log)).

The run's log, on **Pipelines** → the run → **log** (or **why it failed**), has lines such as:

```text
delivery retail/partner_orders: sent 4 files, 212 rows, to sftp://acme@sftp.acme.example/inbox
delivery retail/partner_orders: recorded: HTTP 200
```

A partner checks a file against the manifest:

```bash
sha256sum harbor_partner_orders_2026-10-26_EU.csv.gz.pgp
jq '.files[] | select(.name=="harbor_partner_orders_2026-10-26_EU.csv.gz.pgp") | .sha256' manifest_partner_orders_2026-10-26.json
```

## The delivery log

### How it behaves

- Each run is kept by platformd in its own bucket as `deliveries/<project>/<delivery>/<time>.json`: the manifest, where it went (`s3://acme/from-harbor`, `sftp://user@host/path`), when the tables were read, what it sent (table or query), the approval it ran under, and a summary.
- The audit log has `delivery.sent` with the summary (`4 files, 212 rows, to sftp://...`).
- The **Deliveries** tab shows each delivery's last ten runs: when, rows, the files with their sizes (hover a file for its SHA-256), and where.
- For a delivery with personal columns, the run also records `kept`: the Nessie commit on `main` at the moment it read the tables, and a tag on it, `Delivery.<project>.<delivery>.<time>`, kept until `deliveries.keepFor` (default seven years) has passed. Nightly maintenance removes those tags past it. This is what lets a data-protection approver search the log for one person's rows years later, from the tables as they were; the log itself holds no one's ID ([Privacy](13-privacy.md#what-was-sent-about-a-person)).

The log is read through the project's deliveries:

```bash
curl "$PLATFORM/api/projects/retail/deliveries" -H "Authorization: Bearer $TOKEN"
```

Each delivery in the answer has `approved`, `personal`, `needs` and `runs`.

## Settings

| Chart value | Compose (`stack/compose.yaml`, platformd) | Default | What it does |
|---|---|---|---|
| `deliveries.approverGroups` | `DELIVERY_APPROVER_GROUPS` | empty: the administrators' groups (`PLATFORM_ADMIN_GROUPS`, `engineers`). Compose sets `engineers,analysts`. | Keycloak groups of the built-in platform role `data-protection` (comma-separated) |
| `deliveries.keepFor` | `DELIVERY_KEEP_FOR` | `7y` | How long each personal delivery's read is kept (a Nessie tag per run): years (`7y`) or days (`400d`). platformd refuses to start with another form. |
| `maintenance.at` | `MAINTENANCE_AT` | `02:00` (UTC) | When the nightly round removes delivery tags past `keepFor` |

Administrators can also give `protect-personal` to more groups by defining a platform role on Administration → **Roles** ([Roles](16-roles.md)).

## Limits and gotchas

- **Who changed it can't approve it.** The administrator who saved a delivery (or planned the release that brought it) is its `changedBy`; another editor approves. `someone other than who changed what it sends approves it`.
- **People's data needs two approvals.** A delivery that sends a people's table, its person's column in a query, or a masked column waits for a data-protection approver as well as an editor.
- **A query is matched by words.** A query's personal columns are found by the column and table names it contains. `SELECT *` from a people's table through a view or a CTE named otherwise may not be recognised: name the table and its person's column in the query.
- **One broken target stops them all.** If any delivery's target or key connection is missing, the project's `deliveries` part is red (`delivery partner_orders: no connection "retail-acme-sftp" among the project's`) and none of the project's deliveries has a pipeline until it's fixed.
- **Connection names.** A connection added on the page must start with the project's name and a dash: `retail-acme-bucket`, not `acme-bucket`.
- **The Deliveries tab shows only when the project has a delivery.** For a project without one, open `/p/<project>/deliveries` directly to reach the privacy search and erasure.
- **Not yet:** fixed-width files, Azure Blob and Google Cloud Storage targets, and opening "the data as sent" from a run on the SQL page.
- **SFTP needs the host key.** Without `hostKey` the connection can't be saved (`The server's host key (as in known_hosts) is needed`), and a run against a connection made outside the platform without one stops: `the SFTP target has no hostKey`.
- **Checks don't include the table's quality checks.** Use `minRows`, `maxRows`, `within` and `parts`, or make the delivery read a table only a passing pipeline writes.

The errors you may meet, and what to do about each, are in [Troubleshooting](18-troubleshooting.md#deliveries).
