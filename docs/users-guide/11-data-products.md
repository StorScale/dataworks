# 11. Data products

A data product is one of a project's tables published for other projects, with an owner, a version and a promise (its contract):
the columns it keeps, how fresh it stays, its checks passing. Another project's editors ask for it; the publisher's editors approve;
the consuming project then reads the table where it is, under the publisher's rules, with no copy to go stale. The governance group
certifies what it has checked, and a broken promise is the publisher's alert, naming who depends on it. This chapter is for the
engineers who publish tables, the analysts whose projects ask for them, and the governance group that certifies them.

In this chapter:

- [Publishing a data product](#publishing-a-data-product)
- [The contract, and broken promises](#the-contract-and-broken-promises)
- [Asking for a product](#asking-for-a-product)
- [Approving, declining and removing consumers](#approving-declining-and-removing-consumers)
- [Reading a product in place](#reading-a-product-in-place)
- [Certifying](#certifying)
- [Changing and removing a product](#changing-and-removing-a-product)
- [Where products show](#where-products-show)
- [Settings](#settings)
- [Errors you may meet](#errors-you-may-meet)

Who does what, in one table:

| Action | Who | Where |
|---|---|---|
| Publish, change, remove a product | Those who may write the project's tables (`write-tables`: editors, and roles that give it) | Data products → **Publish a data product**, a card's **Change** and **Remove** |
| Ask for a product for a project | That project's editors (the permission `build`) | A card's **Ask for it** |
| Approve or decline a request; remove a consumer | The publishing project's editors (`build`) | The card's **Approve**, **Decline**, **Remove `<project>`** |
| Certify, or take certification away | The governance group (the platform permission `certify-products`; administrators always) | The card's **Certify** / **Take certification away** |
| See the products | Anyone signed in | The top bar's **Data products** |
| Read a product's rows | The publisher's members, and the members of the projects it was approved for | SQL, notebooks, pipelines, metrics, assistants |

---

## Publishing a data product

**What it's for.** Marketing wants retail's orders. Copying the table gives marketing a second table that drifts from the first and
escapes retail's rules. Publishing it says: this table is for others, this person answers for it, and this is what we promise.

**Who can do it.** Anyone who may write the project's tables: its editors, and members whose role gives `write-tables`.

### Set up

**On the page.** Open the top bar's **Data products** and press **Publish a data product** (it shows when you may write some
project's tables). Fill in the form:

| Field | What it is |
|---|---|
| **From the project** | The project publishing it: those whose tables you may write |
| **Its table** | One of the project's tables, picked from the catalog's list (or typed, when the catalog has none yet) |
| **Name** | The product's name: lowercase letters, digits and underscores (`orders`). Picking a table suggests one |
| **Version** | Its version, as `1` or `2.3` (default `1`) |
| **What it is** | A description |
| **Who answers for it** | The owner: a user name (default: you) |
| **The business terms it holds** | Tick the company's terms the table holds (shown when there are any: [Business terms](06-metrics-and-terms.md#business-terms)) |
| **What it promises its consumers** | Tick the columns it keeps (picked from the table's columns); **Changed at least every** (default `24h`); **The project's checks on its table pass** |

Press **Publish it**. The page lists the product as a card: its name and project, **not certified**, its version, its table and
owner, what it promises, the terms it holds, and "Read by: no other project yet".

**In the project's spec**, the same product is an entry of `products` (the form writes it there):

```json
{
  "apiVersion": "platform.storscale.io/v1alpha1",
  "kind": "Project",
  "metadata": {"name": "retail"},
  "spec": {
    "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "reader"}],
    "tables": {
      "readers": {"rowFilters": [{"table": "uploads_orders", "filter": "region = 'EU'"}]}
    },
    "products": [
      {
        "name": "orders",
        "table": "uploads_orders",
        "description": "Harbor Outfitters' orders, net of test rows",
        "owner": "bob",
        "version": "1",
        "terms": ["revenue"],
        "contract": {
          "columns": ["order_id", "customer_id", "ordered_on", "region", "amount"],
          "within": "24h",
          "checks": true
        }
      }
    ]
  }
}
```

| Field | What it is |
|---|---|
| `name` | Required: lowercase letters, digits and underscores, starting with a letter or underscore, up to 63 characters; unique in the project |
| `table` | Required: one of the project's tables, by its bare name. The project must have `tables` |
| `owner` | Required: a person's user name |
| `version` | Required: up to 20 characters (`1`, `2.3`) |
| `description` | What it is |
| `terms` | The company's business terms it holds, by name |
| `contract.columns` | The columns it keeps, with these names |
| `contract.within` | It changes at least this often: `30m`, `24h`, `7d` |
| `contract.checks` | `true`: the project's checks on its table pass ([Data quality](05-quality.md)) |
| `consumers`, `certification` | The platform's: who reads it, and who certified what. Whatever a save says, they're kept as they were |

A project kept as code has the same in `storscale/project.yaml`, applied by a release ([History and releases](15-history-and-releases.md)).

**Through the API**, for those who may write the project's tables:

| Method and path | What it does |
|---|---|
| `PUT /api/projects/{name}/products/{product}` | Publishes a table as a product, or changes its promise. Body: `table`, `description`, `owner`, `version`, `terms`, `contract` |
| `DELETE /api/projects/{name}/products/{product}` | Takes it back, once no other project reads it |

```bash
curl -s -X PUT http://storscale.localhost:8800/api/projects/retail/products/orders \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"table": "uploads_orders", "description": "Harbor Outfitters orders, net of test rows", "owner": "bob",
       "version": "1", "terms": ["revenue"],
       "contract": {"columns": ["order_id", "customer_id", "ordered_on", "region", "amount"], "within": "24h"}}'
```

The answer is the product as saved. An unknown field is refused ("a data product, as JSON: json: unknown field …"); `consumers` and
`certification` in the body are ignored.

### How it behaves

- The product is saved in the project's spec, checked as any spec is; the operator is asked to apply the project at once.
- The platform's audit log records `product.saved`: "published: table uploads_orders, version 1", or "changed: …".
- Within a few minutes the catalog (OpenMetadata) has it as a data product in the domain **StorScale Platform**, named
  `orders (retail)`, with its table as its asset and its owner when the catalog knows that user.

See [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place), step 1.

### Limits and gotchas

- **The suggested name can be refused.** Picking a table suggests its name without `uploads_` and with underscores made dashes
  (`uploads_campaign_responses` becomes `campaign-responses`), but a product's name takes no dashes: the save says
  `products: name "campaign-responses": an identifier`. Write it with underscores (`campaign_responses`). (`uploads_orders`
  becomes `orders`, which is fine.)
- **Names aren't checked against the table.** A table that doesn't exist yet, or a contracted column it doesn't have, is accepted;
  the contract's check then reports the columns missing ([below](#the-contract-and-broken-promises)).
- **A product's name and project can't be changed** on the form: remove it and publish it again (once no project reads it).
- A product is one table. Several tables are several products.

---

## The contract, and broken promises

**What it's for.** A consumer builds on the columns it reads. The contract says which columns will stay, how fresh the table stays,
and whether its checks pass; the platform watches each, and tells the publisher, naming who's let down, when one breaks.

**Who can do it.** The contract is part of the product: those who publish it set it.

### How it behaves

Once a minute, the platform looks at each product with a contract and raises an alert on the publishing project for each promise
broken:

| Promise | Broken when | The alert says |
|---|---|---|
| `columns` | A contracted column isn't in the table (renamed or dropped) | "the product orders breaks its contract: region gone from uploads_orders" |
| `within` | The table hasn't changed within the window (by its last commit) | "the product orders breaks its contract: uploads_orders not changed within 24h" |
| `checks` | One of the project's checks on the table is failing | "the product orders breaks its contract: a check on uploads_orders is failing" |

Each alert names the consumers it lets down ("its consumers: marketing", or "no consumers yet") and links to the table. The alerts
are the project's own: on its **Overview**'s **Alerts** card, sent where the project's `notify` says, muted as other alerts are
([Data quality](05-quality.md)), and listed to assistants by `list_alerts`.

**In merge requests.** With a GitLab repo, a merge request whose dbt build leaves a product's table without a contracted column fails
its review: the review's note says "the product `orders` (uploads_orders): its contract's columns region would be gone; read by
marketing", and its status is failed ([Pipelines and the project's repo](09-pipelines-and-git.md)).

See [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place), step 7.

### Limits and gotchas

- A broken contract doesn't stop anyone reading the product, nor take away its certification: it's an alert for the publisher to
  act on.
- The column check compares names only: a column whose type changed keeps its promise as far as the platform can tell.
- A compatible change (a new column) needs nothing; a breaking one is best published as a new version, or a new product, while
  consumers move.

---

## Asking for a product

**What it's for.** A project that needs another's table asks for it, saying why, and the publisher decides. Nothing is read until
they approve.

**Who can do it.** The editors of the project that will read it (the permission `build` there, or an administrator). The **Ask for
it** button shows for projects where you're an editor.

### Set up

On **Data products**, press **Ask for it** on the product's card. Pick **For the project** (your projects that don't read it yet),
write **What it's for** ("The regional forecast"), and press **Ask**.

**Through the API:** `POST /api/products/{project}/{product}/requests`, with the producing project and product in the path:

```bash
curl -s -X POST http://storscale.localhost:8800/api/products/retail/orders/requests \
  -H "Authorization: Bearer $TOKEN" -H "X-Platform-Request: 1" -H "Content-Type: application/json" \
  -d '{"consumer": "marketing", "why": "Campaign results by region"}'
```

### How it behaves

- The request waits in the platform's store until the publisher's editors answer; asking again replaces it.
- The publisher's editors see it on the card, under **Asked for**: the project, who asked, and why. The **Governance** page lists it
  among what waits for an approval.
- The platform's audit log records `product.requested`, "for marketing".

See [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place), step 2.

### Limits and gotchas

- "the project to read it in (consumer), not its own": a project doesn't ask for its own product.
- "the editors of marketing ask for it": you must be able to build in the consuming project.
- "marketing reads it already": it's a consumer already.

---

## Approving, declining and removing consumers

**What it's for.** The publisher decides who reads its table, and can take that back.

**Who can do it.** The publishing project's editors (the permission `build` there), and administrators.

### Set up

On the product's card, under **Asked for**, press **Approve** or **Decline**. Under the card's line "Read by: …", **Remove
`<project>`** (confirmed with "Stop marketing reading orders?") takes a consumer away.

**Through the API:**

| Method and path | What it does |
|---|---|
| `POST /api/products/{project}/{product}/requests/{consumer}/approve` | Approves the request: the consumer reads it |
| `POST /api/products/{project}/{product}/requests/{consumer}/decline` | Declines it |
| `DELETE /api/products/{project}/{product}/consumers/{consumer}` | Takes the consumer's reading away |

### How it behaves

- Approving adds the project to the product's `consumers` in the publisher's spec and has the operator apply it: within a minute the
  consumer's people read the table ([below](#reading-a-product-in-place)). Removing a consumer takes it out, and their reads are
  refused once it's applied, within a minute.
- The request is gone once answered, either way.
- The platform's audit log records `product.approved` and `product.declined` ("for marketing"), and `product.consumer-removed`.

### Limits and gotchas

- "the editors of retail answer requests for its products" / "… decide who reads its products": you need `build` on the publisher.
- "no request from marketing": it was answered already, or never made.
- "marketing doesn't read it": it isn't a consumer.

---

## Reading a product in place

**What it's for.** The consumer reads the publisher's own table: one table, always current, under the publisher's rules.

**Who can do it.** The consuming project's readers, editors and pipelines, and its members whose custom role reads (a role giving
`read`, `read-all` or `write-tables`), plus the publisher's own members as always.

### How it behaves

The operator gives the consuming project's groups, in Ranger, `select` on the product's table and nothing else of the publisher's
(and `use` and `show` on its namespace, to find it). The publisher's readers' rules on that table, its row filters and masks, apply
to the consumers too, in the same Ranger policies:

- A consumer who isn't also a member of the publisher sees what the publisher's readers see: with retail's row filter
  `region = 'EU'`, marketing's people read the EU rows only.
- A consumer who is also an editor of the publisher (alice, in the labs) reads it as an editor: every row.

The table keeps its own name. From the consuming project's **SQL** tab, a notebook, a pipeline or a flow:

```sql
SELECT region, count(*) AS orders, round(sum(amount), 2) AS amount
FROM iceberg.retail.uploads_orders
WHERE NOT test
GROUP BY region
```

Other ways in:

- **Metrics on the publisher's definitions.** If the publisher's model is shared and the consumer's spec has
  `"semantic": {"uses": ["retail"]}`, the consumer's model builds on retail's metrics; the questions read the product's table as the
  person asking ([Sharing a model](06-metrics-and-terms.md#sharing-a-model-and-building-on-another-projects)).
- **Assistants.** `query_product` reads a product's rows as the person; someone who isn't a reader of the publisher or of a consumer
  is refused, and told the product's page where their project can ask for it ([Assistants](07-dashboards-apps-assistants.md#assistants-over-mcp)).
- **Flows.** The canvas's **Input** step lists the project's tables and the products it reads ([Flows](08-flows.md)).

See [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place), steps 3, 4 and 8.

### Limits and gotchas

- **Read only.** Consumers can't write the table.
- **Only the product's table.** Being a consumer of `orders` reads nothing else of retail's.
- The publisher's rules are those for its readers on that table (`tables.readers`) and the install's protection tags
  ([Access and protection](04-access-and-protection.md)): a column retail masks for its readers is masked for its consumers.

---

## Certifying

**What it's for.** Anyone can publish a table; the governance group says which ones it has checked. A certified product is marked
everywhere it shows and listed first. The certification is of what was checked: the table, the version and the contract. A change
to any of them lapses it.

**Who can do it.** The governance group: members of the Keycloak groups given a platform role with `certify-products`. The built-in
platform role **governance** gives it (and the Governance page) to the groups in `products.certifierGroups`, by default the
platform's administrators; an administrator can define other platform roles that give it ([Roles](16-roles.md)). Administrators may
always certify.

### Set up

On the product's card, **Certify**. On a certified one, **Take certification away**.

**Through the API:** `POST /api/products/{project}/{product}/certification` certifies it as it is now;
`DELETE /api/products/{project}/{product}/certification` takes it away.

### How it behaves

- The certification records who certified it, when, and a hash of the product's table, version and contract.
- The card shows **certified**, "Certified by bob" and the date; certified products list first on the page, in search, in the
  Governance page's products and in assistants' `list_products`.
- The catalog marks the product's table **Certification.Gold** (for a year by the catalog's clock), and removes the mark when the
  product is no longer certified.
- **A changed promise lapses it.** When the table, version or contract changes (on the form, the API, the spec or a release), the
  hash no longer matches: the product shows **not certified**, the catalog's mark goes, and the governance group certifies again.
  Its consumers keep reading it. The description, owner and terms can change without lapsing it.
- The platform's audit log records `product.certified` and `product.uncertified` ("version 1, table uploads_orders"); a save that
  lapsed one says so in its `product.saved` record ("its certification lapsed (the promise changed)").

See [Lab 12](../guide.md#lab-12-a-certified-data-product-read-in-place), steps 5 and 6.

### Limits and gotchas

- "the governance group (a platform role with certify-products) certifies products": you're not in it.
- A broken contract (a column gone) doesn't lapse the certification by itself; the alert does the telling.

---

## Changing and removing a product

**What it's for.** Promises change: a column added to the contract, a new version. And a product nobody reads any more can go.

**Who can do it.** Those who may write the project's tables.

### Set up

**Change** on the card opens the form with the product's values (its project and name fixed). Change what you need and press
**Save**. On a certified product the form warns: "A changed promise lapses its certification: the governance group certifies it
again."

**Remove** (confirmed with "Take orders back? No other project reads it.") takes the product back. It's off while any project
reads it, saying which ("marketing read it: remove them first").

**Through the API:** `PUT /api/projects/{name}/products/{product}` with the whole product, as when publishing; `DELETE
/api/projects/{name}/products/{product}`.

### How it behaves

- A change keeps who reads it: its consumers stay, and keep reading.
- A changed table, version or contract lapses a certification ([Certifying](#certifying)).
- Removing takes the product out of the spec, out of the catalog's data products, and the platform's audit log records
  `product.removed`.

### Limits and gotchas

- **A product another project reads can't be removed:** "marketing read it: take them off its consumers first" (409). Remove the
  consumers first, then the product.
- That rule is the Data products page's and its API's. An administrator who takes the product out of the spec's JSON on the
  project's **Edit** page removes it whatever reads it, and its consumers lose their reading: use the page.
- Changing `table` points the product (and its consumers) at another table; that's a changed promise, so a certification lapses.

---

## Where products show

| Where | What |
|---|---|
| **Data products** (top bar) | Every project's products, to anyone signed in, certified first, refreshed every 30 seconds; requests only to the publisher's editors |
| The top bar's search | Products after business terms, certified first ("Product ✓"); picking one opens its card |
| The catalog (OpenMetadata) | The domain **StorScale Platform**: one data product per product, its table as its asset; a certified one's table marked Certification.Gold |
| **Governance** page | Every product with its owner, certification and consumers; requests waiting for an editor ([Operating the platform](17-operating.md)) |
| Assistants | `list_products`, `describe_product`, `query_product`; answers from metrics cite the products behind the tables they read |
| A business term's page | The terms ticked on a product link to their pages |

**Through the API:** `GET /api/products`, to anyone signed in: every product with its project, `certified`, `consumers`, `requests`
(for its editors) and `canApprove`; and `canCertify` for the caller.

---

## Settings

| Setting | Chart value (`deploy/helm/storscale-platform/values.yaml`) | Compose | Default |
|---|---|---|---|
| Who certifies data products | `products.certifierGroups` (Keycloak groups, comma-separated) | `PRODUCT_CERTIFIER_GROUPS` on platformd | empty: the platform's administrators' groups |
| How often catalog-sync updates the catalog's data products | not a chart value | `METADATA_EVERY` on catalog-sync | 60 seconds |

---

## Errors you may meet

| Error | What it means | What to do |
|---|---|---|
| "the project's editors publish its data products (it takes the permission write-tables)" | You may not write that project's tables | Ask an editor, or for a role with `write-tables` |
| `products: name "x-y": an identifier` | The name has dashes or capitals | Use lowercase letters, digits and underscores |
| "products: orders: its owner is a person (their user name)" | The owner is empty or not a user name | Give a user name |
| "products: orders: a version, as 1 or 2.3" | No version, or longer than 20 characters | Give one |
| "products: orders: a product is one of the project's tables, and it has none (tables)" | The project has no `tables` | Give the project tables first ([Projects](02-projects.md)) |
| `products: orders: contract: within "1 day": as 30m, 24h or 7d` | The freshness window isn't a duration the platform reads | Write `24h` or `1d` |
| "the editors of marketing ask for it" | You can't build in the project you asked for | Ask as one of its editors |
| "marketing read it: take them off its consumers first" | Removing a product a project reads | Remove the consumer first |
| "the governance group (a platform role with certify-products) certifies products" | You may not certify | Ask the governance group |
| "Access Denied: …" on `iceberg.retail.uploads_orders` | Your project isn't a consumer yet, or the operator hasn't applied the approval | Ask for the product; wait a minute after approval |
