# 18. Troubleshooting

This chapter collects the errors people meet, in the platform's own words, grouped by feature: what each means and what to do about it. It starts with the two things that make any error easier to chase, the request ID and the run log, then goes feature by feature. It's for everyone: the person who saw the error, and the administrator they hand it to.

In this chapter:

- [Request IDs](#request-ids)
- [The run log](#the-run-log)
- [The project's status: Applied to](#the-projects-status-applied-to)
- [Signing in and access](#signing-in-and-access)
- [Projects and their spec](#projects-and-their-spec)
- [Connections](#connections)
- [Uploads and imports](#uploads-and-imports)
- [Pipelines](#pipelines)
- [SQL, metrics and limits](#sql-metrics-and-limits)
- [Access and protection](#access-and-protection)
- [History, tags and maintenance](#history-tags-and-maintenance)
- [Releases](#releases)
- [Flows](#flows)
- [Data products](#data-products)
- [Deliveries](#deliveries)
- [Privacy: search and erasure](#privacy-search-and-erasure)
- [AI: the gateway, agents, knowledge banks, apps](#ai-the-gateway-agents-knowledge-banks-apps)
- [Models](#models)
- [Roles and templates](#roles-and-templates)
- [Audit, usage, governance and health](#audit-usage-governance-and-health)
- [Installs and upgrades](#installs-and-upgrades)

---

## Request IDs

Every request to the platform gets an ID. It comes back in the answer's `X-Request-ID` header, and every refusal or failure is answered the same way, as JSON with the ID in it:

```json
{"error": "someone other than who changed what it sends approves it", "request": "9f3c1a0b7d2e4c55"}
```

platformd logs the same ID with the method, the path, the status and the error: a refusal (4xx) as `request refused`, a failure of the platform's (5xx) as `request failed`. So:

1. **On the web app**, the error shows in a red banner. To get its ID, open the browser's developer tools → Network, find the request that failed, and read `X-Request-ID` in its response headers (or `request` in its body).
2. **From a script**, the Python package puts it in the exception: `storscale.Error: the audit log isn't answering (HTTP 502, request 9f3c1a0b7d2e4c55)`, and on `err.request_id`. With curl, `-i` shows the header.
3. **Give your own:** send `X-Request-ID: <id>` (up to 64 printable characters, no spaces) and the platform uses it, so a privacy tool or a job can tie its own log to the platform's.
4. **An administrator finds it** in platformd's log: `storscale logs platformd | grep 9f3c1a0b7d2e4c55` on Compose, `kubectl -n storscale logs deploy/platformd | grep 9f3c1a0b7d2e4c55` on Kubernetes. The line has the ID, the method, the path, the status and the error; a refusal the platform records (a delivery approved by who changed it, a search by someone who may not) is in the audit log too, with the person.

A `401` answer is `{"login": "/auth/login"}`, not an error: the session ended, and the web app signs in again.

## The run log

Pipelines (imports, deliveries, flows, dbt, scheduled notebooks, agents, knowledge banks, scorings) run in Airflow. When one fails:

1. Open the project → **Pipelines**, find the pipeline, and press **why it failed** beside the failed run (**log** beside any other).
2. The log is each task's last try, `=== <task> (try N)` then what it logged: dbt's, dlt's, the notebook's or the delivery's own words.
3. Through the API: `GET /api/projects/{name}/pipelines/{pipeline}/runs/{run}/log`, or `p.run_log("retail", "import_retail_uploads", run)` in Python.

A failed latest run is also one of the project's alerts (`<pipeline>'s latest run failed`), sent where its `notify` says.

A banner above the pipelines, *A file in the repo didn't load* (or *N files ...*), means Airflow couldn't load a DAG file from the project's repo; it names the file and the error.

## The project's status: Applied to

Within a minute of a save, the platform operator applies the project to every system. Its **Overview** shows the phase (**Ready**, **Error**, **Deleting**, or *Waiting for the operator*), and the **Applied to** card lists each system with a green or red dot and, when red, the operator's message. A red part doesn't undo the others; the operator tries again every few seconds and resyncs every minute.

| Part | Message | Means | Do |
|---|---|---|---|
| `keycloak` | `no Keycloak group "<g>"`, `no Keycloak user "<u>"` | A member names a group or person the realm doesn't have | Fix the member, or make the group in Keycloak |
| `keycloak` | `Keycloak client "<id>" has no service account` | `pipelines.serviceAccount` names a client without one | Name the pipelines' client (`airflow-pipelines` on the example install) |
| `roles` | `no role <name>: an administrator defines it (Administration → Roles); its members have no access meanwhile` | A member's role was removed or never defined | Define it on Administration → Roles, or change the member's role |
| `ranger` | `waiting for Keycloak` | The keycloak part failed first | Fix that one |
| `buckets` | `bucket <b>'s size limit: ...` | Buckets refused the quota | Check `limits.storage`, and Buckets' health |
| `trino` | `source <s>: no connection "<c>"`, `source <s>: its connection needs a username and a password` | A source's connection is missing or incomplete | Add or fix it on **Connections** |
| `trino` | `source <s>: its url isn't a JDBC URL (jdbc:...)`, `... its url holds a password (or a line break): the password goes in password, which Trino never logs` | The connection's `url` | Put the password in its own field |
| `trino` | `source catalog <c>: ... (Trino may not see its connection yet: the next pass tries again)` | Trino hasn't taken the new catalog yet | Wait a minute |
| `copy` | `copying a source needs Kubernetes (Compose can't start its pods)` | Change-capture copies run only on Kubernetes | Query the source in place instead |
| `copy` | `copying <s>: copying needs its connection's host, username and password (a url alone won't do)` | | Give the connection host, username and password |
| `imports` | `imports run in the platform's Airflow, and there's none here` | No Airflow on this install | |
| `imports` | `import <i>: no connection "<c>" among the project's`, `... its connection has no token`, `... its connection needs a host, username and password`, `... its port "<p>" isn't a number` | The import's connection | Fix it on **Connections** |
| `airflow` | `no secret "<s>" (the repo's credentials) among the project's connections`, `secret "<s>" has no password (a token) for an https repo`, `secret "<s>" has no sshPrivateKey for an ssh repo` | The repo's credentials | Add the connection `pipelines.credentials.secretRef` names |
| `airflow` | `its Airflow is starting`, `its Airflow is starting again, with its connections` | A project's own Airflow (Kubernetes) is coming up | Wait |
| `deliveries` | `delivery <d>: no connection "<c>" among the project's`, `delivery <d>: the connection "<c>" has no pgpPublicKey` | A delivery's target or key; while it's red, none of the project's deliveries runs | See [Deliveries](#deliveries) |
| `agents` | `agent <a>: its secret isn't kept yet` | A scheduled agent's secret (a connection the platform makes when the agent is approved) isn't there | Have the agent approved, by an editor other than who registered it |
| `reviews` | `merge request <n>: ...`, `the repo's credentials (<s>): ...` | Merge-request reviews couldn't reach GitLab or build the data branch | Check the repo's token (it needs GitLab's API) |
| `serving` | `serving <s>'s secret: ...` | A model server's client | Check the project's serving and its model version |

## Signing in and access

| Message | Means | Do |
|---|---|---|
| `no access` | You're signed in but in none of the platform's groups (`PLATFORM_GROUPS`, `analysts,engineers` by default): carol, on the example install | An administrator adds you to a group in Keycloak |
| `no such project` | You aren't a member of it (and aren't an administrator), or it doesn't exist | Ask its administrator to add you; check the name |
| `not a member of the project` | The same, for routes a member's token calls (notebooks, MLflow, models) | |
| `<who> isn't a member of <project>` | An agent's or a script's token isn't a member | Add the person or service account |
| `missing X-Platform-Request` | A change sent without the header a cross-site form can't send | Scripts send `X-Platform-Request: 1` on every `POST`, `PUT` and `DELETE`; the Python package does |
| `projects aren't set up (no STORE_URL)` | platformd has no project store configured | An install problem: check platformd's environment |

## Projects and their spec

Saving a spec (the **Edit** page's **Save**, or `PUT /api/projects/{name}`) checks it all at once and lists every problem, separated by `; `. The most common:

| Message | Do |
|---|---|
| `only administrators change projects` | Project specs are the administrators'; ask one, or change it through a release ([History and releases](15-history-and-releases.md)) |
| `not a project: json: unknown field "<f>"` | A misspelt or misplaced field: the spec accepts only its own |
| `the project's name must be <name>` | The JSON's `metadata.name` differs from the project being saved |
| `name "<n>": 3 to 40 lowercase letters, digits and dashes, starting with a letter` | |
| `a project needs at least one member` | |
| `member 2: give a group or a user, not both` | |
| `member 1: role "<r>": reader, editor, or a role the platform's administrators define` / `role <r>: reader, editor, or a role defined on Administration → Roles (none is called that)` | Define the role first, or use reader or editor |
| `tables: people: <t> twice`, `tables: people: "<t>"."<c>": a table and its column identifying a person` | |
| `tables: retention: <t>: keep "<k>": as 730d or 2y` | |
| `tables: freshness: within "<w>": as 30m, 24h or 7d` | |
| `limits: queries <n>: from 1 to 100 at once`, `limits: queued <n>: up to 1000`, `limits: queryMemory "<m>": as 4GB, 512MB or 50%`, `limits: storage "<s>": as 500Gi or 2Ti`, `limits: storage needs files (a bucket)` | |
| `cost: budget <b>: a month, more than nothing and up to ten million` | |
| `llm: budget <b>: USD a month, up to a million` | |
| `notify: "<e>" isn't an email address` | |
| `the project store isn't answering` | platformd couldn't reach where projects are kept (Buckets, or Kubernetes): an administrator checks Health |
| A Kubernetes schema message | On Kubernetes, the cluster checked the spec against the Project definition; an old definition means the install wasn't upgraded with `storscale upgrade` |

## Connections

| Message | Means | Do |
|---|---|---|
| `a connection's name starts with the project's and a dash (retail-dags), in lower-case letters, digits and dashes` | A new connection's name | Name it `retail-...` |
| `the project has a connection <c> already: give it new values` | | Use **New values** (or `PUT`) |
| `kind: one of git, ssh, database, api, sftp, s3, pgp, teams` | An unknown kind | Pick one the page offers |
| `<Field> is needed`, `one of password, sshPrivateKey is needed`, `<key>: no such key for <Kind>`, `<key>: too long` | The values for the kind | Fill the required fields |
| `the project's editors change its connections (it takes the permission connections)` | | Ask an editor, or a role with `connections` |
| `no such connection` | | |
| **Remove** is greyed, *Used by ...* | The spec names it | Take it out of the spec first |

## Uploads and imports

| Message | Means | Do |
|---|---|---|
| `the project's editors upload files to it`, `the project's editors remove its uploads` | | Ask an editor, or a role with `write-files` |
| `a file, as multipart form data (file), up to 256 MB: ...` | The file is too big or the request malformed | Split the file, or put it in the bucket another way |
| `a file's name is letters, digits, dots, dashes and underscores: <name>` | | Rename it |
| `Buckets: ...` | Buckets refused or failed | Check the project's bucket isn't full, and Health |
| `no connection <c> among the project's (its Connections tab)` | An import names a connection the project lacks | Add it first |
| `imports: <i>: a file of landing/uploads/ (no folders), ending in one of csv, xlsx, jsonl, parquet: "<f>"` | | |
| `the project's editors reset its imports` | | |
| `no Airflow here` | | An install problem |

A files import that fails with `Forbidden` in its run log reading the landing area: the project's pipelines can't read it. Since 0.31.0 an import gives the project pipelines; on an older project, add `pipelines` to its spec. A table whose dates were imported as text before 0.31.0 needs its import's **Reset** (the Imports tab) to take dates.

## Pipelines

| Message | Means | Do |
|---|---|---|
| `only the project's editors run its pipelines` | | An editor, or a role with `run-pipelines` |
| `pipelines aren't set up (no AIRFLOW_URL)` | | An install problem |
| A run that fails | | Read [the run log](#the-run-log) |

## SQL, metrics and limits

The SQL tab, metrics and assistants go through the metrics and SQL service (semanticd), which runs queries in Trino as you.

| Message | Means | Do |
|---|---|---|
| `you're not a member of <project>` | | |
| `write a statement`, `one statement at a time` | | One statement, no trailing second one |
| `Access Denied: ...` (Trino's) | Ranger refused: you may not read that table or column (a reader's tables list, a table outside your projects) | Check the project's **Access** tab; the refusal is in the Audit tab with Ranger's reason |
| A query refused with `QUERY_QUEUE_FULL`, naming a group such as `project-retail` | The project's (or your own) limit of waiting queries is reached | Wait, cancel what's running, or raise `limits.queued` ([Operating](17-operating.md#limits)) |
| `the semantic layer isn't answering` | semanticd is down or slow | Health page: *Metrics and SQL service* |
| `project <p> has no semantic model` | | Write one on the Metrics tab |
| `only those who may change <p>'s metrics (its editors) change its semantic model` | | A role with `change-metrics` |
| `ask for at least one metric`, `MetricFlow can't answer that: ...` | | |
| `the company's model has these already, so build on them rather than defining them again: ...` | A shared model defines those names | Use the shared model's ([Metrics and terms](06-metrics-and-terms.md)) |
| `it would break the models of <project> (<why>)` | Another project builds on this model | Keep the names it uses |

## Access and protection

| Message | Means | Do |
|---|---|---|
| `the project's editors confirm or remove a column's tags` | | A role with `classify` |
| `the catalog has no column <t>.<c>` | The catalog hasn't seen the column yet | Wait for catalog-sync, or check the name |
| `the catalog isn't set up, or the project has no tables` | | |
| `the action is confirm or remove` | | |

A column you expect to read whole shows hashed or masked: it's tagged `personal`, `financial` or `health` and you're not excepted ([Access and protection](04-access-and-protection.md)). The project's alerts say *<t>.<c> looks personal: the catalog tagged it, and it's masked* until someone confirms or removes the tag.

## History, tags and maintenance

| Message | Means | Do |
|---|---|---|
| `the project's editors make and remove its tags` | | A role with `release` |
| `the project has a tag <name> already` | | Choose another name |
| `keep: as 90d or 7y` | | |
| `want ?table=<one of the project's tables>` | | |
| `the project's editors ask for its maintenance` | | A role with `run-pipelines` |
| `the project's tables have no maintenance (none, or paused)` | `tables.maintenance.paused` is on, or there are no tables | |
| Storage tab: *Table maintenance isn't set up on this install* | platformd has no maintenance account | An install problem: `MAINTENANCE_TRINO_PASSWORD` |
| Alert `<table>: its maintenance failed` | Retention or compaction failed | The table's **Storage** tab says which step and why |

## Releases

| Message | Means | Do |
|---|---|---|
| `the project has no repo (pipelines.repo)` | | Connect a repo first |
| `a ref of the repo: a tag (v1.3), a branch, or a commit` | | |
| `someone other than who planned it applies it` | Four eyes | Another editor applies |
| `the project has changed since it was planned: plan it again` | | Plan again |
| `the plan is <state>` | It was applied or failed already | |
| `the spec is saved, the semantic model isn't: ...` | The release's semantic model couldn't be saved | The message says why; fix `semantic.yaml` in the repo and plan again |

## Flows

| Message | Means | Do |
|---|---|---|
| `the project's editors save flows in it` | | A role with `build` |
| `the flow's name is lowercase letters, digits and underscores, and the one in the address` | | |
| `a flow is saved with an output step: the table it makes` | | Add an output step |
| `a schedule as cron's five fields (UTC): 0 6 * * 1, or none` | | |
| `step <s>: ...` | A step doesn't compile | The step's message says why |
| `GitLab refused the commit: ...`, `the merge request: ...` | Saving as a merge request failed | Check the repo's token |
| `the project's editors and its pipelines run a step's model` | | |

## Data products

| Message | Means | Do |
|---|---|---|
| `the editors of <consumer> ask for it` | Only the consuming project's editors ask | |
| `<consumer> reads it already` | | |
| `the editors of <project> answer requests for its products` | | |
| `the governance group (a platform role with certify-products) certifies products` | | |
| `<consumers> read it: take them off its consumers first` | A product with consumers can't be taken back | |
| `no request from <consumer>`, `approve or decline` | | |

## Deliveries

| Where | Message | Means | Do |
|---|---|---|---|
| Approve | `someone other than who changed what it sends approves it` | You saved the change (or planned the release that brought it) | Another editor approves |
| Approve | `the project's editors (and data-protection approvers) approve deliveries` | You have neither `approve` nor `protect-personal` | |
| Approve | `no such delivery` | | |
| Card | `Needs: an editor of the project other than <who>` | Waiting for an editor | |
| Card | `Needs: a data-protection approver (<groups>)` | It sends personal columns | Someone in those groups (or an administrator) approves; a data-protection approver who is only a reader approves through `POST .../approve` |
| Card | *not approved* again after a save | Its table, query, split, format, target or PGP key changed | Approve again |
| Spec | `deliveries: <d>: it sends the project's tables, and it has none (tables)` | | |
| Spec | `deliveries: <d>: a table or a query, one of them` | | |
| Spec | `deliveries: <d>: its query is one SELECT` | Not a `SELECT`/`WITH`, or has a `;` | |
| Spec | `deliveries: <d>: format "<f>": one of csv, parquet, jsonl` | | |
| Spec | `deliveries: <d>: files "<f>": a name with {date} (and {part}, split), no folders` | | |
| Spec | `deliveries: <d>: splitBy (a column) and {part} in the files' name go together` | | |
| Spec | `deliveries: <d>: schedule "<s>": five cron fields, as 0 6 * * 1` | | |
| Spec | `deliveries: <d>: its target and pgp are connections' names` | | |
| Spec | `deliveries: <d>: checks: rows from minRows to maxRows`, `checks: within "<w>": as 30m, 24h, 7d`, `checks: parts need splitBy` | | |
| Applied to | `delivery <d>: no connection "<c>" among the project's` | The target is missing; **no** delivery of the project runs until fixed | Add it on Connections |
| Applied to | `delivery <d>: the connection "<c>" has no pgpPublicKey` | | Give the key connection the recipient's public key |
| Run log | `checks failed, nothing sent: <problems>` | A check failed (`N rows, fewer than M`, `... not changed within 24h (last ...)`, `parts missing: ...`) | Fix the data, or the check |
| Run log | `the SFTP target has no hostKey: ...` | | Give the connection the server's host key |
| Run log | `the SFTP server's key isn't the one given (<type>): nothing sent` | The server's key changed, or the wrong one was given | Confirm the new key with the partner, then update `hostKey` |
| Run log | `the recipient's key: ...` | GnuPG couldn't import the PGP key | Paste the whole armored public key |
| Run log | `splitBy <c>: no such column` | | The query must return the split column |
| Run log | `sending failed (...); again in 10s` | A transfer failed; it's tried three times | If all three fail, the run fails: check the target's address and credentials |
| Pipelines | No `delivery_...` pipeline | It isn't approved, or the project's `deliveries` part is red | Approve it; fix the part; allow a few minutes for Airflow to load it |

## Privacy: search and erasure

| Message | Means | Do |
|---|---|---|
| `the data-protection approvers (a platform role with protect-personal) search the delivery log` / `... ask for erasures` / `... see erasures` | | Be given a platform role with `protect-personal`, or be an administrator |
| `a person's ID (person=)`, `a person's ID (person)` | Empty, or over 200 characters | |
| `requestedOn as a date, 2026-10-19` | | |
| `the project declares no tables of people's data (tables.people), so there's nothing to erase from` | | Declare them ([Privacy](13-privacy.md#where-a-persons-data-is)) |
| `no such project with tables` | | |
| `erasing needs the platform's files and its Trino account (maintenance)`, `searching needs ...` | | An install problem: the maintenance account |
| `the erasure ran but its record couldn't be kept: ...` | The tables were erased; the record wasn't written | Note the answer's details, check Buckets, and erase again (it finds nothing more, and keeps a record) |
| HTTP 207 and **Not done:** | Something wasn't erased; the record names it | See below |
| Not done: `<table>: ...` | Trino couldn't count or delete in that table | Check the table exists and its column; erase again |
| Not done: `landing/uploads/<f>: a xlsx file isn't rewritten here: take the person's rows out of it by hand, or remove it` (or `a parquet file ...`) | | Edit the file, upload it again, run the import |
| Not done: `landing/uploads/<f>: writing it again: Access Denied...` | You may not write the project's files | An editor with `protect-personal` (or an administrator) erases again, or rewrites the file |
| Not done: `landing/uploads/<f>: no column <c> in it` | The CSV hasn't the person's column | |
| Not done: `landing files: no token: sign in again` | | Sign in again and erase again |
| Search: **unclear**, `what it sent has no column identifying a person (tables.people)` | The delivery sent masked columns but no people's table | |
| Search: **unclear**, `it couldn't be run again: ...` | The kept read is gone or the query no longer runs | |
| Search: *N not checkable* | Runs of deliveries without personal columns, or from before reads were kept | |

## AI: the gateway, agents, knowledge banks, apps

| Message | Means | Do |
|---|---|---|
| `the project has no gateway key: give it an LLM budget (llm.budget in its spec) and the operator makes one` | | Add `"llm": {"budget": 20}` ([AI](14-ai.md)) |
| `the platform has no LLM gateway` | `llm.gateway` is empty | |
| `the project's editors see its model calls` | | |
| `an agent is approved by an editor other than the one who registered it` | | |
| `the project has no agent <a> to ask: an approved agent with a prompt and a budget` | | |
| `the agent <a>'s client or gateway key isn't kept yet` | Just approved | Wait a minute |
| `the project has no tool <t>`, `the project's editors, and the agents given it (tool:<t>), call its tools` | | Give the agent the tool |
| `the tool's connection <c> isn't kept yet: add it on the Connections tab (an API token)` | | |
| `the API didn't answer: ...` | The tool's API | |
| `the project has no knowledge bank <b>` | | |
| `the project's editors and its pipelines build its knowledge banks` | | |
| `embedding chunks N to M: ...`, `embedding the question: ...` | The gateway refused or failed to embed | The message says why |
| `a question app answers questions: ask it`, `only a question app is asked` | | |
| `asking the model: ...`, `the model couldn't be asked: ...` | The gateway refused (the guardrail, a budget) or failed | The message says which |

## Models

| Message | Means | Do |
|---|---|---|
| `MLflow isn't set up here (no MLFLOW_URL)` | | |
| `the project's readers read its experiments and models; its editors and pipelines write them` | | A role with `models` |
| `a model version is approved by an editor other than the one who registered it` | | |
| `no such model version: ...` | | |
| `the project serves no model as <s>` | | |
| `the project's models answer N calls at a time: try again in a moment` | | Retry |
| `a call is up to 32 MB` | | |
| `the model's server isn't answering (starting?)` | | Wait, then check the serving |
| `models aren't served here (no SERVING_URL)` | | |

## Roles and templates

| Message | Means | Do |
|---|---|---|
| `<r> is the install's (its settings' groups): define another` | finance, governance and data-protection are built in | |
| `<r> is a project's role: remove it first to make it another kind` | | |
| `the projects <p> give <r>: change their members first` | A role in use can't be removed | |
| `the install has no GitLab for templates' repos (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN): leave repo out` | | Set `templates.gitlab` ([Operating](17-operating.md#the-installs-settings)) |
| `there's a project <p> already` | | |
| `no library <l> (Administration → Templates lists them)` | | |

## Audit, usage, governance and health

| Message | Means | Do |
|---|---|---|
| `a project's audit log is for its editors` | | A role with `see-audit` |
| `the platform's audit log is its administrators'` | | |
| `the audit log isn't answering` | Ranger's Solr is down | Health page |
| `the project's editors, and the install's usage viewers, see what it uses and costs` | | A role with `see-usage`, or the finance platform role |
| `the install's usage viewers and its administrators see every project's usage` | | |
| `month: as 2026-10` | | |
| `the administrators, the products' certifiers and the data-protection approvers see the governance view` | | |
| `administrators only`, `health checks are off` | | |
| `want {service, hours}: up to a week` | | |
| **Send a test alert** says *No alert targets are set (the Secret storscale-alerts).* | | Set the Secret ([Operating](17-operating.md#health-and-the-administrators-alerts)) |
| An alert target answers with an error | The webhook or SMTP server refused | platformd logs `alert not sent` with the target and why |

## Installs and upgrades

| Message | Means | Do |
|---|---|---|
| `the platform didn't start: see storscale status and storscale logs` | A Compose service didn't become healthy | `storscale status`, then `storscale logs <service>` |
| `Docker is rootless here, so notebooks need DOCKER_SOCK=/run/user/<uid>/docker.sock` | | Set it |
| `no release "storscale" in the namespace "storscale" (context "..."): install it first` | | |
| `the release storscale is pending-upgrade: another install, upgrade or rollback is under way (or was cut off; see helm history)` | | Wait, or `helm rollback` |
| `<v> is older than the installed <w>: a downgrade isn't tested, ...` | | `--force`, or `helm rollback` |
| `these images aren't published at <v>: ...` | The release's images aren't out yet | Wait for its images workflow |
| `upgrade needs Helm 3.14 or later` | | |
| `the chart doesn't render: ...` | A value is wrong (the dry run caught it) | Fix the value |
| `upgraded, but ... (see kubectl get pods,jobs -n storscale; to undo: helm rollback ...)` | A service didn't become ready | Read the pods' state; roll back if needed |
| `pool-0's drives are ... they can grow, never shrink`, `... a pool's shape can't change`, `... a pool's class can't change`, `Buckets has N pools and the values give M` | | See [Buckets capacity](17-operating.md#buckets-capacity) |
| `backups aren't set up here: give the chart backup.certificate (storscale backup keygen makes one)` | | |
| `this key isn't the one for the cluster's backup certificate: its backups can't be opened with it` | | Find the matching key |
| `<file> exists: keygen never overwrites a key (backups made for it need it)` | | Use another `--out` |
| `--data: the data isn't copied off this install (backup.data), so there's none to copy back` | | |
| `the restore failed: ... (the platform was started again; its databases may be part restored: restore again)` | | Restore again |
