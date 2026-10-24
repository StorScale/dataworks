# 4. Access and protection

This chapter is for whoever decides who sees what of a project's tables: the project's owner, the administrator who edits its spec, and the privacy officer who sets what a tag of personal data means across the install. It covers the readers' rules (which tables readers may read, which rows, and what of each column), the declaration of which tables hold people, the protection tags the catalog suggests and people confirm or remove, the install's rules for those tags, and what each kind of person ends up seeing: editors, readers, roles of the company's own, the project's pipelines and the projects that consume its data products. Every rule here is applied by Apache Ranger in Trino, so it holds wherever a person's query goes through Trino: the SQL tab, the Data tab's preview, dashboards, notebooks, the canvas's previews, apps and assistants. Roles themselves are [chapter 16](16-roles.md); erasure and the rest of privacy, [chapter 13](13-privacy.md).

In this chapter:

- [Who sees what](#who-sees-what)
- [Tables readers may read](#tables-readers-may-read)
- [Row filters for readers](#row-filters-for-readers)
- [Column masks for readers](#column-masks-for-readers)
- [Mask types](#mask-types)
- [People's data (tables.people)](#peoples-data-tablespeople)
- [Protection tags and the classifier](#protection-tags-and-the-classifier)
- [Confirming and removing a tag](#confirming-and-removing-a-tag)
- [The install's protection rules](#the-installs-protection-rules)
- [Seeing the rules: the Access tab and others](#seeing-the-rules-the-access-tab-and-others)
- [What the audit log records](#what-the-audit-log-records)

---

## Who sees what

A project's members are Keycloak groups (or named people), each with a role ([chapter 2](02-projects.md)). The operator puts each role's people in a Keycloak group of the project's (`<project>-editors`, `<project>-readers`, `<project>-role-<name>`), and Ranger's policies name those groups. How a group reads the tables depends on its role's permissions: `write-tables` writes them and reads them all as they are; `read-all` reads them all as they are; `read` reads them under the readers' rules.

| Who | Tables | Readers' row filters and masks | Protection tags' masks |
|---|---|---|---|
| **Editors**, and roles with `write-tables` | All, and may make, change and drop them | None | None: they see the column as it is |
| Roles with `read-all` (and not `write-tables`) | All, read only | None | None |
| **Readers**, and roles with `read` (and not `read-all`) | Those readers may read (default all) | Applied | Masked, unless the install excepts one of their groups |
| The project's **pipelines** (`<project>-pipelines`): its imports, deliveries, scorings and DAGs | All, read and write | None | None |
| A project **consuming** one of its data products: that project's readers, editors, pipelines and roles that read | The product's table only | Applied, as for readers | Masked, unless excepted |
| The catalog's own Trino account (`openmetadata`): its checks, profiles and samples | All, read only | None | As they are (tagged columns aren't profiled) |
| Anyone else | Nothing of the project's | | |

An **editor is never filtered or masked by the readers' rules, whatever other groups they're in.** alice can be an editor of `retail` and also an analyst, in a group that reads `retail` as a reader or that consumes its data product: in the readers' filter and mask policies the project's editors and read-all roles come first, unfiltered and unmasked, and Ranger takes the first item a person matches. Readers and consuming projects get the rules.

Someone given several roles in one project (a group as reader, and themselves as editor) has the one that gives the most.

Administrators may do every platform action in every project (the Edit page, confirming tags, muting alerts). What they read in Trino is what their memberships give, like anyone else.

The rules reach a table read as it was, too: `uploads_orders@close-2026-09` and `uploads_orders#<commit>` are under the same policies as `uploads_orders` ([chapter 15](15-history-and-releases.md)).

---

## Tables readers may read

By default a project's readers may read every table in its namespace. Name some, and they may read only those; the rest are the editors' alone.

**Who can do it:** administrators (the spec, on the Edit page).

### Set up

On the project's Edit page (Overview → **Edit project**), under **Tables**, **Tables readers may read**: a comma-separated list, empty for "all of them". In the spec it's `tables.readers.tables`:

```json
{
  "tables": {
    "readers": {
      "tables": ["uploads_orders", "uploads_customers", "uploads_campaigns"]
    }
  }
}
```

### How it behaves

- Ranger's policy on the namespace lets readers see it; a policy per listed table (`project:<name>:read:<table>`) lets them read it. A table not listed is refused in Trino: on the SQL tab the query fails with Trino's "Access Denied".
- A new table made later is the editors' alone until it's listed.
- On the Data tab every member sees every table's name; a table readers can't read has a small "editors" beside it, and opened, the banner says "Only editors can read it." (to a reader: "You see this table as a reader.").
- A data product's table is readable by its consumers whether or not it's listed ([chapter 11](11-data-products.md)).

### Limits and gotchas

- Each name must be a table's: "readers: table "Orders" isn't an identifier".
- The list is of names; it doesn't check that the tables exist. A name misspelt shows nothing to readers.

See [Lab 13 in the lab guide](../guide.md#lab-13-rules-for-readers-and-every-tool-following).

---

## Row filters for readers

A row filter is a SQL condition on a table: readers see only the rows it holds for.

**Who can do it:** administrators (the spec).

### Set up

On the Edit page, **Row filters for readers** → **Add a row filter**: the table, and the condition (`region = 'EU'`). In the spec, `tables.readers.rowFilters`:

```json
{
  "tables": {
    "readers": {
      "rowFilters": [
        {"table": "uploads_orders", "filter": "region = 'EU'"},
        {"table": "uploads_campaign_responses", "filter": "responded_at >= DATE '2026-01-01'"}
      ]
    }
  }
}
```

### How it behaves

- Ranger adds the condition to every reader's query of the table, in Trino: `SELECT count(*) FROM uploads_orders` gives alice the EU's rows, about a third of bob's 305.
- The project's editors and read-all roles have an item of their own in the same policy with no filter at all (an empty one): Trino refuses to update a table under any row filter, even `true`, so an editor can still insert, update and delete.
- Consuming projects of a data product on the table get the same filter.
- The project's pipelines aren't named in it: they read every row.

### Limits and gotchas

- One filter per table: Ranger keeps one row-filter policy per table. Combine conditions with `AND` and `OR`.
- The condition is Trino SQL on the table's own columns. A condition that doesn't compile fails every reader's query of the table, not the save.
- "readers: row filter on "uploads_orders" needs a table and a condition".

See [Lab 2](../guide.md#lab-2-a-project-for-the-retail-team) and [Lab 13](../guide.md#lab-13-rules-for-readers-and-every-tool-following).

---

## Column masks for readers

A mask shows readers less of a column: its last four characters, a hash, the year of a date, nothing.

**Who can do it:** administrators (the spec).

### Set up

On the Edit page, **Column masks for readers** → **Add a mask**: the table, the column, and the type. In the spec, `tables.readers.masks`:

```json
{
  "tables": {
    "readers": {
      "masks": [
        {"table": "uploads_customers", "column": "signed_up", "type": "MASK_DATE_SHOW_YEAR"},
        {"table": "uploads_orders", "column": "card_number", "type": "MASK_SHOW_LAST_4"}
      ]
    }
  }
}
```

The three parts together, as they sit in a project's spec:

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
      "readers": {
        "tables": ["uploads_orders", "uploads_customers", "uploads_campaigns"],
        "rowFilters": [{"table": "uploads_orders", "filter": "region = 'EU'"}],
        "masks": [{"table": "uploads_customers", "column": "signed_up", "type": "MASK_DATE_SHOW_YEAR"}]
      },
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

### How it behaves

- Ranger masks the column for readers (and consuming projects) in every query. The Data tab's **Readers see** column says what they get ("year only", "last 4 only"), and a reader's **Preview** shows it: `2026-01-01` for every `signed_up`.
- Editors and read-all roles come first in the mask's policy with no mask (`MASK_NONE`).
- A project's own mask and the install's protection tags add up: on a column the project masks and the catalog tags, the project's readers (and consuming projects) get the project's mask (the last four digits of a card, say), everyone else the tag's, and the tag's excepted groups see it as it is. Other tagged columns keep the tags' masks.

### Limits and gotchas

- One mask per column.
- **A project's own masks are written for a text column.** `MASK`, `MASK_SHOW_LAST_4`, `MASK_SHOW_FIRST_4` and `MASK_HASH` give text back, and Trino refuses a mask that changes a number's or a date's type ("Invalid column mask"), for every reader's query of the column. On a number or a date use `MASK_NULL` (or `MASK_DATE_SHOW_YEAR` on a date), or let a protection tag do it: a tag's mask knows the column's type (below).
- **A project's own mask can't lift a tag's.** A readers' mask of `MASK_NONE` on a column the catalog tagged personal gives readers the tag's hash, not the values. Any other mask of the project's stands for its readers, even one that shows more than the tag's (`MASK_SHOW_LAST_4` on a card number the catalog tagged financial): it's the project's choice for its own readers, and the Governance page lists each project's masks. The Data tab's **Readers see** describes the project's own mask, and the **Protection** column the tag.
- "readers: mask type "HASH" must be one of MASK, MASK_SHOW_LAST_4, MASK_SHOW_FIRST_4, MASK_HASH, MASK_NULL, MASK_NONE, MASK_DATE_SHOW_YEAR".

---

## Mask types

| Type | The Data tab says | What a reader gets, as a project's mask | As a protection tag's mask (it knows the column's type) |
|---|---|---|---|
| `MASK` | masked | Ranger's redaction of the text | Text: redacted. Any other type: null |
| `MASK_SHOW_LAST_4` | last 4 only | The last four characters; the rest redacted | Text: so. Any other type: null |
| `MASK_SHOW_FIRST_4` | first 4 only | The first four characters; the rest redacted | Text: so. Any other type: null |
| `MASK_HASH` | hashed | SHA-256 of the text, as 64 hex characters | Text: the same. `bigint` and `integer`: a number of the same type, hashed. Any other type (a date, a decimal): null |
| `MASK_NULL` | hidden | Null | Null |
| `MASK_NONE` | shown | The value as it is | The value as it is |
| `MASK_DATE_SHOW_YEAR` | year only | The date's year (January the first of it) | The same |

A number hashed to a number keeps its nulls, its duplicates and its joins: `customer_id` hashed in `uploads_customers` and in `uploads_orders` still joins, and the catalog's checks on it (not null, unique, references) still answer truly. Its values don't show. Before 0.31.0 such a column was nulled.

---

## People's data (tables.people)

`tables.people` says which tables hold people's data and which column identifies a person in each. That column is personal from the moment it's declared: the catalog tags it `personal`, confirmed. The same declaration is what a delivery of those tables needs a data-protection approver for ([chapter 12](12-deliveries.md)), what a search of what was sent about a person looks in, and what an erasure reaches ([chapter 13](13-privacy.md)).

**Who can do it:** administrators (the spec, in the Edit page's JSON: the form has no field for it).

### Set up

```json
{
  "tables": {
    "people": [
      {"table": "uploads_customers", "column": "customer_id"},
      {"table": "uploads_orders", "column": "customer_id"}
    ]
  }
}
```

### How it behaves

- At its next round (every two minutes) the classifier tags each declared column `personal`, confirmed, in the catalog. The Data tab's **Protection** column shows it as `personal` with no **remove** beside it ("declared by the project").
- From then on the column is masked as the install's rule for `personal` says: on a local install hashed, for everyone but the engineers. A `bigint` ID is hashed to a `bigint`.

### Limits and gotchas

- One entry per table: "tables: people: uploads_orders twice".
- "tables: people: "uploads_customers"."": a table and its column identifying a person".
- To take a declaration back, remove it from the spec. Removing the tag on the Data page doesn't: the next round tags it again.

See [Lab 2](../guide.md#lab-2-a-project-for-the-retail-team) and [Lab 4](../guide.md#lab-4-personal-data-found-and-protected).

---

## Protection tags and the classifier

Nobody writes a policy for a column of personal data that arrives in a new import. The catalog finds it, tags it, and the platform masks it at once; the project's owner then confirms the tag or removes it.

The catalog (OpenMetadata) has a classification called **Protection** with three tags:

| Tag | What it's for |
|---|---|
| `personal` | Identifies a person, or is about one: names, contact details, identifiers, dates of birth |
| `financial` | A person's money: salaries, balances, card and account numbers' amounts |
| `health` | A person's health: diagnoses, prescriptions, conditions |

A tag on a column is **suggested** (found by the catalog) or **confirmed** (by a person, or declared by the project).

**Who can do it:** the classifier runs by itself. Confirming and removing take the permission `classify` (editors; administrators).

### How the classifier decides

Every two minutes catalog-sync looks at every project's tables, as the catalog knows them:

1. **Declared:** each column in `tables.people` is tagged `personal`, confirmed.
2. **By name:** a column (of any type) whose name matches is tagged, suggested.
   - `personal`: `email`, `e_mail`, `phone`, `mobile`, `telephone`, `ssn`, `social_security`, `passport`, `national_id`, `tax_id`, `iban`, `card_number`, `cc_number`, `credit_card`, `date_of_birth`, `dob`, `birth_date`, `birthdate`, `first_name`, `last_name`, `surname`, `full_name`, `home_address`, `street`, `postal_code`, `zip_code`
   - `financial`: `salary`, `wage`, `income`, `net_worth`, `credit_score`, `account_balance`, `balance`
   - `health`: `diagnosis` (and words starting `diagnos`), `icd10`, `icd_10`, `medical`, `prescription`, `health_condition`, `condition`

   The word may be the whole name or a part between underscores: `customer_email`, `phone_2`.
3. **By value:** each text column not yet tagged is sampled: its first 200 rows, read from Trino as the catalog's own account. When at least 3 values are there and 60% of them look like one of these, it's tagged `personal`, suggested:

   | Looks like | Pattern |
   |---|---|
   | An email address | `name@domain.tld` |
   | A card number | 13 to 19 digits (spaces or dashes allowed) passing the Luhn check |
   | A national ID | `123-45-6789` |
   | A bank account | An IBAN |
   | A phone number | 9 to 20 characters of digits, spaces, `( ) . -`, an optional leading `+`, at least 8 digits; **not** a date or a time written as text (`2026-05-03`, `2026-05-03 10:00`) and not a plain decimal number (`1792780188.63`) |

4. **Never:** the import's own columns, `_dlt_load_id`, `_dlt_id` and the rest starting `_dlt_`, are never tagged. A column that already has a Protection tag isn't looked at again. A suggestion someone removed isn't made again.
5. What's tagged now (declared, suggested, confirmed, with each column's type) is written to the platform's bucket, `classification/<project>.json`. The operator reads it at its next pass over the project (each project at least once a minute) and makes a Ranger mask policy for each tagged column: `project:<name>:protect:<table>.<column>`.

### How it behaves

- **A suggested tag protects at once**, before anyone confirms it (the install's `applySuggested`, true by default).
- The project's Overview lists each suggested tag as an alert, "uploads_customers.email looks personal: the catalog tagged it, and it's masked", with "Confirm the tag, or remove it if the column isn't personal, on the Data page." It goes to the project's channels like any alert ([chapter 5](05-quality.md#project-alerts)) and clears once the tag is confirmed or removed.
- The Data tab's **Protection** column shows each column's tags: `personal (suggested)` in blue, `personal` (confirmed) in amber; hovering says "declared by the project", "found by the catalog" or "tagged in the catalog".
- A column with two tags is masked by the strongest: `health`, then `financial`, then `personal`.
- The mask follows the column's type as the catalog has it (see [Mask types](#mask-types)).
- Who sees a tagged column as it is: the project's editors, its read-all roles and its pipelines (they own and write the data, and Trino refuses a write to a table the writer is masked on), the platform's maintenance account (erasure, retention), and the groups the install's rule excepts. Everyone else who can read the table sees it masked: readers, roles with `read`, consuming projects, and the catalog's own account.
- The tag's mask reaches every tool that reads through Trino as the person: the SQL tab, previews, dashboards, notebooks, apps, assistants. The project's pipelines read the column as it is, so a delivery of it sends it as it is: that's why a delivery of personal columns needs a data-protection approver ([chapter 12](12-deliveries.md)).

### Settings

The classifier's timings are catalog-sync's own environment, not chart values: it ingests the catalog's metadata every 60 seconds (`METADATA_EVERY`) and classifies every 120 (`CLASSIFY_EVERY`). A new table is in the catalog within a minute or so and classified within two more; its mask follows within a minute of that. What the tags mean is the install's: [below](#the-installs-protection-rules).

### Limits and gotchas

- Only `personal` is found by value. `financial` and `health` are found by a column's name only.
- Free text that mentions a person (a review signed with an email address) isn't found unless 60% of the sampled values look like one: the classifier tags columns, not sentences.
- The sample is the table's first 200 rows, not a random 200.
- The catalog's own account sees a tagged column as it is, so the checks test its values ([chapter 5](05-quality.md#checks)); the profiler leaves tagged columns out, so their values' ranges aren't in the catalog.
- catalog-sync reads up to 200 tables of a project's namespace each round.

See [Lab 4 in the lab guide](../guide.md#lab-4-personal-data-found-and-protected).

---

## Confirming and removing a tag

**Who can do it:** the permission `classify` (editors; administrators). Refused: "the project's editors confirm or remove a column's tags".

### Set up

On **Data**, open the table. In the **Protection** column:

- **confirm** beside a suggested tag: the tag stays, whatever the catalog finds later.
- **remove** beside a tag that isn't declared: the tag goes, and the catalog doesn't suggest it again. Within a minute the column is read as it is.

The catalog's own page for the table (Tools → Catalog) shows the same tags under **Protection**, and is another place to confirm or correct them.

Through the API:

```bash
curl -X POST "$PLATFORM/api/projects/retail/classification" \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"table": "uploads_customers", "column": "email", "tag": "personal", "action": "confirm"}'
```

The answer is `{"state": "confirmed"}` or `{"state": "removed"}` (`"absent"` when the tag to remove wasn't there). `confirm` on a tag the column doesn't have adds it, confirmed: that's how a person tags a column the classifier missed. `GET /api/projects/{name}/classification` lists the project's tagged columns (`table`, `column`, `tag`, `state`, `type`, `declared`, `automated`), with when catalog-sync last wrote them (`at`) and the tags there are.

### How it behaves

- The platform changes the tag in the catalog (as its own account), and the project's view at once; catalog-sync's next round writes the file again, and the operator's next pass changes Ranger's policy.
- The audit log records `classification.confirmed` or `classification.removed`, with the table, column and tag; a refused attempt is recorded too.

### Limits and gotchas

- "the catalog has no column uploads_customers.mail": the table or column isn't in the catalog yet, or is misspelt.
- "a table, a column and a tag, each an identifier"; "the action is confirm or remove".
- A declared column has no **remove** on the Data page; removing its tag through the API lasts only until the next round. Take it out of `tables.people` instead.
- A tag must be one of the Protection classification's: `personal`, `financial`, `health`.

---

## The install's protection rules

What each tag means is set once for the install: the mask, and the Keycloak groups that see the column as it is. Project owners don't write masks for personal data.

**Who can do it:** whoever installs the platform (the chart's values, or Compose's environment).

### Set up

On Kubernetes, the chart's `protection` values:

```yaml
protection:
  applySuggested: true
  tags:
    personal: {mask: MASK_HASH, except: [data-protection]}
    financial: {mask: MASK, except: []}
    health: {mask: MASK_NULL, except: []}
```

In Compose, `PROTECTION_TAGS` on both `platformd` and `platform-operator` in `stack/compose.yaml`, as JSON; a local install excepts the engineers:

```yaml
PROTECTION_TAGS: '{"personal": {"mask": "MASK_HASH", "except": ["engineers"]}, "financial": {"mask": "MASK", "except": ["engineers"]}, "health": {"mask": "MASK_NULL", "except": ["engineers"]}}'
```

`PROTECTION_APPLY_SUGGESTED` (default `true`; `false`: confirmed tags only) is the chart's `applySuggested`.

| Setting | Default | What |
|---|---|---|
| `protection.tags.personal` | `{mask: MASK_HASH, except: []}` | Hashed, for everyone |
| `protection.tags.financial` | `{mask: MASK, except: []}` | Redacted, for everyone |
| `protection.tags.health` | `{mask: MASK_NULL, except: []}` | Null, for everyone |
| `protection.applySuggested` | `true` | A suggested tag protects before anyone confirms it |

A tag given replaces the default's rule for it; a tag left out keeps the default; a rule without a mask is `MASK`.

### How it behaves

- The operator reads the rules and makes each tagged column's mask policy from them; platformd shows them on the Governance page ("What readers see": each tag, its mask and who's excepted, and whether suggested tags count).
- Changing them takes a restart of both (`storscale upgrade` with the new values), then each project's next pass.

### Limits and gotchas

- A bad value stops platformd and the operator starting: "PROTECTION_TAGS: "Personal" isn't a tag's name", "PROTECTION_TAGS: personal: mask "HASH" isn't one of MASK, MASK_SHOW_LAST_4, …".
- An excepted group sees the column as it is only where it may read the table at all.
- The catalog has three tags. A rule for a tag of another name has nothing in the catalog to apply to.

---

## Seeing the rules: the Access tab and others

**Who can do it:** any member sees a project's Access tab; administrators see Administration → Access.

- **Access** (a project's tab): **Members**, each person, their role and through which Keycloak group (or "named in the project"), with **Change members** for administrators; "Someone who joins one of those groups in Keycloak joins the project within a minute." **What readers don't see**: the tables they may read, each row filter ("Only rows where …") and each mask. Beside them, a card per role saying in words what it gets in each system ("Read uploads_orders, uploads_customers, uploads_campaigns in iceberg.retail", "uploads_orders: only rows where region = 'EU'"), your own role marked "you", and a card for each role of the company's own the project gives.
- **Data**: above a table's columns, a banner when readers see less: "Readers see less of this table." to an editor, "You see this table as a reader." to a reader, with each filter and mask. The **Readers see** column says it per column; **Protection** shows the tags.
- **The Edit page** shows "What each role will get" before you save.
- **Administration → Access** (administrators; `GET /api/access`): every person, and their role in each project.
- **Governance** (administrators, product certifiers and data-protection approvers): every project's readers' rules, declared people's tables and tagged columns, and the install's tag rules ([chapter 17](17-operating.md)).
- **Ranger**: each project's policies are named `project:<name>:…` and say "From the project <name>: the platform operator keeps it as the project says." The operator puts back anything changed by hand.

---

## What the audit log records

| Action | When |
|---|---|
| `project.saved` | The spec saved (readers' rules and people's tables are in it), with which parts changed |
| `classification.confirmed`, `classification.removed` | A tag confirmed or removed, with the table, column and tag |
| A refusal of `classification.changed` | Someone without `classify` tried |

Every query Trino allowed or refused is in the audit log too, with the Ranger policy that decided it: the project's **Audit** tab shows the accesses to its tables ([chapter 17](17-operating.md)). See [Lab 21](../guide.md#lab-21-who-did-what).
