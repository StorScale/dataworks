// Package project is the platform's central object: a team's tables, files
// and pipelines, and who may do what with them. A Project is applied to
// Keycloak, Buckets, Apache Ranger and Nessie by the platform operator; this
// package says what each of those should hold, and nothing about how.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	APIVersion = "platform.storscale.io/v1alpha1"
	Kind       = "Project"
)

// Roles a member can have.
const (
	Reader = "reader" // reads the tables (as the reader view allows) and the files
	Editor = "editor" // creates and changes the tables, writes the files
)

type Project struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	// Roles are the custom roles the platform's administrators define (16.1),
	// as the operator and platformd read them: not the spec's, but what its
	// members' roles mean.
	Roles map[string]Role `json:"-"`
	// ConsumerRoleGroups are, for each project consuming its data products,
	// the groups of that project's custom roles that read (16.1), which read
	// the products as its readers and editors do; the operator fills them in.
	ConsumerRoleGroups map[string][]string `json:"-"`
}

type Metadata struct {
	Name string `json:"name"`
}

type Spec struct {
	Description string     `json:"description,omitempty"`
	Members     []Member   `json:"members"`
	Tables      *Tables    `json:"tables,omitempty"`
	Files       *Files     `json:"files,omitempty"`
	Pipelines   *Pipelines `json:"pipelines,omitempty"`
	Limits      *Limits    `json:"limits,omitempty"`
	Notify      *Notify    `json:"notify,omitempty"`
	Sources     []Source   `json:"sources,omitempty"`
	Imports     []Import   `json:"imports,omitempty"`
	Deliveries  []Delivery `json:"deliveries,omitempty"`
	Semantic    *Semantic  `json:"semantic,omitempty"`
	Products    []Product  `json:"products,omitempty"`
	Agents      []Agent    `json:"agents,omitempty"` // programs of the project's own (12.2)
	// ServiceAccounts: applications' identities in the project, each a member in a role.
	ServiceAccounts []ServiceAccount `json:"serviceAccounts,omitempty"`
	LLM             *LLM             `json:"llm,omitempty"`       // the project's use of the LLM gateway (12.1)
	Knowledge       []KnowledgeBank  `json:"knowledge,omitempty"` // text the project's people and agents search by meaning (12.6)
	Tools           []ConnectorTool  `json:"tools,omitempty"`     // its own tools for its agents: an API's operations, signed in by a connection (12.7)
	Cost            *Cost            `json:"cost,omitempty"`      // what it may cost a month, by the install's rate card (13.1)
	Apps            []App            `json:"apps,omitempty"`      // small pages of its data, shown as whoever opens them (14.1)
	Scoring         []Scoring        `json:"scoring,omitempty"`   // its approved models scoring its tables on a schedule (15.1)
	Serving         []Serving        `json:"serving,omitempty"`   // its approved models answering as an API (15.1)
}

// Semantic is how the project's semantic model stands among the others'
// (use case 10.1). Shared, it's the company's (Customer, Revenue), for
// projects to build on. Uses names the shared models this project's model
// builds on: compiled with them first, it may not define their names again.
type Semantic struct {
	Shared bool     `json:"shared,omitempty"`
	Uses   []string `json:"uses,omitempty"`
}

// A Delivery sends files to a client or partner on a schedule (use case
// 9.1): a table of the project's (or a query on its tables), in a format,
// split by a column if asked, compressed, encrypted for the recipient, with
// a manifest, to an SFTP server or an S3 bucket (Target, one of the
// project's connections). It runs as the project's pipelines, only once
// approved, and again only once what it sends, as changed, is approved.
type Delivery struct {
	Name     string          `json:"name"`
	Table    string          `json:"table,omitempty"` // one of the project's, or Query
	Query    string          `json:"query,omitempty"` // a SELECT on the project's tables
	Format   string          `json:"format"`          // csv, parquet, jsonl
	Gzip     bool            `json:"gzip,omitempty"`
	Files    string          `json:"files"`             // names: {date}, {part} (SplitBy's value)
	SplitBy  string          `json:"splitBy,omitempty"` // a column: a file per value
	Schedule string          `json:"schedule"`          // cron (UTC)
	Target   string          `json:"target"`            // a connection: SFTP (host, port, username, password or sshPrivateKey, hostKey) or S3 (endpoint, bucket, prefix, accessKey, secretKey)
	PGP      string          `json:"pgp,omitempty"`     // a connection holding the recipient's pgpPublicKey
	Checks   *DeliveryChecks `json:"checks,omitempty"`
	// ChangedBy and Approval are the platform's, never set by hand: who last
	// changed what it sends, and who approved it (an approval by another).
	ChangedBy string    `json:"changedBy,omitempty"`
	Approval  *Approval `json:"approval,omitempty"`
}

// DeliveryChecks run before anything is sent: a failing one sends nothing.
type DeliveryChecks struct {
	MinRows int64    `json:"minRows,omitempty"`
	MaxRows int64    `json:"maxRows,omitempty"`
	Within  string   `json:"within,omitempty"` // its table changed within: 30m, 24h, 7d
	Parts   []string `json:"parts,omitempty"`  // SplitBy's values all there
}

// An Approval is who approved what a delivery sends (the Hash of it), and
// when; Done once everyone it needs has (another editor, and for personal
// data the install's data-protection approver too).
type Approval struct {
	Hash       string    `json:"hash"`
	By         []string  `json:"by"`                   // as editors (or administrators)
	Protection []string  `json:"protection,omitempty"` // as data-protection approvers
	At         time.Time `json:"at"`
	Done       bool      `json:"done,omitempty"`
}

// DeliveryFormats are the formats a delivery may write.
var DeliveryFormats = []string{"csv", "parquet", "jsonl"}

// SendsHash is what an approval covers: what a delivery sends and where
// (its table or query, split, format and target), not when or how it's
// named. A change to it needs approving again.
func (d Delivery) SendsHash() string {
	h := sha256.Sum256([]byte(strings.Join([]string{d.Table, d.Query, d.SplitBy, d.Format, d.Target, d.PGP}, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

// Approved says whether a delivery's approval covers what it sends now.
func (d Delivery) Approved() bool {
	return d.Approval != nil && d.Approval.Done && d.Approval.Hash == d.SendsHash()
}

// PersonalColumns are the columns a delivery sends that the project masks
// for its readers (personal, until columns are tagged): of its table, or
// those its query names.
func (p *Project) PersonalColumns(d Delivery) []string {
	var out []string
	if p.Spec.Tables == nil {
		return out
	}
	q := strings.ToLower(d.Query)
	if rv := p.Spec.Tables.Readers; rv != nil {
		for _, m := range rv.Masks {
			if d.Table != "" && m.Table == d.Table || d.Query != "" && strings.Contains(q, strings.ToLower(m.Column)) {
				out = append(out, m.Table+"."+m.Column)
			}
		}
	}
	if t, c := p.PersonKeyOf(d); c != "" && !slices.Contains(out, t+"."+c) {
		out = append(out, t+"."+c)
	}
	return sorted(out)
}

// PersonKeyOf is the table of people's data a delivery sends, and the column
// in what it sends that identifies a person: its table's (Tables.People), or,
// for a query, a person's column it names. Empty: none.
func (p *Project) PersonKeyOf(d Delivery) (table, column string) {
	if p.Spec.Tables == nil {
		return "", ""
	}
	q := strings.ToLower(d.Query)
	for _, pk := range p.Spec.Tables.People {
		if d.Table != "" && pk.Table == d.Table ||
			d.Query != "" && regexp.MustCompile(`\b`+pk.Table+`\b`).MatchString(q) && regexp.MustCompile(`\b`+pk.Column+`\b`).MatchString(q) {
			return pk.Table, pk.Column
		}
	}
	return "", ""
}

// An Import copies an application's data, or a database's by a cursor
// column, into the project's tables on a schedule (use case 8.2): dlt, run
// in the platform's Airflow, each of its resources into <import>_<name> in
// the project's namespace (ImportTable). A resource with a primary key is
// merged on it; one without, appended to. Each run copies only what's new
// when a resource names a cursor (dlt keeps where it got to).
type Import struct {
	Name       string            `json:"name"`
	Source     string            `json:"source"`               // one of ImportSources
	Connection string            `json:"connection,omitempty"` // rest_api: its token (key token); sql_database: host, port, database, username, password
	Schedule   string            `json:"schedule,omitempty"`   // cron (UTC); default hourly
	API        *ImportAPI        `json:"api,omitempty"`
	Database   *ImportDatabase   `json:"database,omitempty"`
	Salesforce *ImportSalesforce `json:"salesforce,omitempty"`
	Gong       *ImportApp        `json:"gong,omitempty"`
	Highspot   *ImportApp        `json:"highspot,omitempty"`
	SAP        *ImportSAP        `json:"sap,omitempty"`
	Files      *ImportFiles      `json:"files,omitempty"`
}

// App is a connector's import's objects (Salesforce's, Gong's, HighSpot's
// or SAP's), or nil for an import of another source.
func (im Import) App() *ImportApp {
	return im.Apps()[im.Source]
}

// Apps are the import's connector parts, by source (nil where it has none).
func (im Import) Apps() map[string]*ImportApp {
	var sap *ImportApp
	if im.SAP != nil {
		sap = &im.SAP.ImportApp
	}
	return map[string]*ImportApp{"salesforce": im.Salesforce, "gong": im.Gong, "highspot": im.Highspot, "sap": sap}
}

// ImportSources are dlt's sources an import may be: two general ones, the
// platform's connectors (use case 8.4), and files uploaded to the project's
// landing area (11.1's uploads).
var ImportSources = []string{"rest_api", "sql_database", "salesforce", "gong", "highspot", "sap", "files"}

// ImportFiles are files of the project's landing area (landing/uploads/ in
// its bucket, where the canvas's uploads go), each read into a table: CSV,
// Excel, JSON lines or Parquet, by its extension. The import needs no
// connection: it reads the bucket as the project's pipelines. A file read
// again replaces the table (there's no cursor in a file), unless it has a
// primary key, when its rows are merged.
type ImportFiles struct {
	Tables []ImportFile `json:"tables"`
}

type ImportFile struct {
	Name       string `json:"name"`                 // the table: <import>_<name>
	File       string `json:"file"`                 // under landing/uploads/: suppliers.csv, or a glob (prices-*.csv)
	PrimaryKey string `json:"primaryKey,omitempty"` // merged on it, when given
}

// FileFormats are the kinds of file an import reads, by extension.
var FileFormats = []string{"csv", "xlsx", "jsonl", "parquet"}

// FileFormat is a file's format by its extension, or "".
func FileFormat(file string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	if ext == "xls" {
		ext = "xlsx"
	}
	if slices.Contains(FileFormats, ext) {
		return ext
	}
	return ""
}

// ImportApp is an application's objects to copy, by one of the platform's
// connectors (8.4: its connection a kind of its own), and how far back the
// objects copied by their changes start (Since, a date; default all of it).
type ImportApp struct {
	Objects []string `json:"objects"`
	Since   string   `json:"since,omitempty"`
}

// ImportSAP is SAP's: its objects (the released APIs'), and the company's
// own ODP extractions (a CDS view's or a classic extractor's, served as
// OData by its gateway), each copied whole once, then by SAP's own delta,
// deletions included.
type ImportSAP struct {
	ImportApp
	ODP []SAPExtraction `json:"odp,omitempty"`
}

// An SAPExtraction is an ODP extraction an SAP import copies into
// <import>_<name>: the OData service the gateway serves it in (made in
// SEGW: Redefine, ODP Extraction), its entity set (EntityOf<the view or
// extractor>, FactsOf…, AttrOf…), and the fields its rows are merged on.
type SAPExtraction struct {
	Name       string   `json:"name"`
	Service    string   `json:"service"`   // ZSALES_ODP_SRV
	EntitySet  string   `json:"entitySet"` // EntityOfZCDS_SALES
	PrimaryKey []string `json:"primaryKey"`
}

var (
	sapServiceRE = regexp.MustCompile(`^[A-Za-z0-9_/]{1,80}$`)
	sapNameRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,79}$`)
)

// ImportSalesforce is Salesforce's (dlt's verified Salesforce source).
type ImportSalesforce = ImportApp

// A Connector is one of the platform's: the source an import names, the
// application, the connection kind it signs in with, its objects, and those
// copied by their changes and merged (the others are replaced each run).
type Connector struct {
	Source      string   `json:"source"`
	Label       string   `json:"label"`
	Connection  string   `json:"connection"`
	Objects     []string `json:"objects"`
	Incremental []string `json:"incremental"`
}

// Connectors are the platform's (8.4): Salesforce on dlt's verified source;
// Gong and HighSpot on their REST APIs, SAP S/4HANA on its OData APIs
// (stack/airflow/image/sources/storscale_connectors.py).
var Connectors = []Connector{
	{Source: "salesforce", Label: "Salesforce", Connection: "salesforce", Objects: SalesforceObjects, Incremental: SalesforceIncremental},
	{Source: "gong", Label: "Gong", Connection: "gong", Objects: GongObjects, Incremental: GongIncremental},
	{Source: "highspot", Label: "HighSpot", Connection: "highspot", Objects: HighspotObjects, Incremental: []string{}},
	{Source: "sap", Label: "SAP S/4HANA", Connection: "sap", Objects: SAPObjects, Incremental: SAPIncremental},
}

// ConnectorOf is the connector a source is, or nil.
func ConnectorOf(source string) *Connector {
	for i := range Connectors {
		if Connectors[i].Source == source {
			return &Connectors[i]
		}
	}
	return nil
}

// GongObjects are what the Gong connector copies: its users, its calls (the
// public ones), their transcripts, its scorecards, and the scorecards
// answered on calls. Calls, transcripts and answered scorecards are copied by
// their changes (calls by when they started, from Since).
var GongObjects = []string{"users", "calls", "call_transcripts", "scorecards", "answered_scorecards"}

var GongIncremental = []string{"calls", "call_transcripts", "answered_scorecards"}

// HighspotObjects are what the HighSpot connector copies: its spots (those
// its API key may view), their items, and its users; each replaced each run.
var HighspotObjects = []string{"spots", "items", "users"}

// SAPObjects are what the SAP connector copies, each an entity set of one of
// S/4HANA's released OData APIs: business partners, customers and suppliers
// (API_BUSINESS_PARTNER), products (API_PRODUCT_SRV), sales orders, billing
// documents and purchase orders with their items. Those SAP stamps with
// their last change are copied by it, from Since; an order's or document's
// items are those of the orders or documents a run copies. Customers and
// suppliers carry no such stamp: replaced each run.
var SAPObjects = []string{"business_partners", "customers", "suppliers", "products", "sales_orders", "sales_order_items",
	"billing_documents", "billing_document_items", "purchase_orders", "purchase_order_items"}

var SAPIncremental = []string{"business_partners", "products", "sales_orders", "sales_order_items", "billing_documents",
	"billing_document_items", "purchase_orders", "purchase_order_items"}

// SalesforceObjects are the objects dlt's Salesforce source copies, by its
// resources' names; Incremental, those copied by their changes and merged
// (the others are replaced each run).
var SalesforceObjects = []string{"account", "campaign", "campaign_member", "contact", "event", "lead", "opportunity",
	"opportunity_contact_role", "opportunity_line_item", "pricebook_2", "pricebook_entry", "product_2", "sf_user", "task", "user_role"}

var SalesforceIncremental = []string{"account", "campaign_member", "event", "opportunity", "opportunity_contact_role",
	"opportunity_line_item", "task"}

// ImportAPI is a REST API, as dlt's rest_api source takes it: its resources'
// paths under BaseURL, each a list of objects (at DataSelector, a JSONPath,
// when they're within the answer), in pages as Paging says (none: as dlt
// finds them, which it does for a Link header or a next URL, not a cursor).
type ImportAPI struct {
	BaseURL   string        `json:"baseUrl"`
	Resources []APIResource `json:"resources"`
}

type APIResource struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	DataSelector string `json:"dataSelector,omitempty"`
	PrimaryKey   string `json:"primaryKey,omitempty"`
	// Cursor is the objects' field that grows (updated_at, an id); the API
	// is asked for those past it, by the query parameter CursorParam,
	// starting at CursorStart.
	Cursor      string `json:"cursor,omitempty"`
	CursorParam string `json:"cursorParam,omitempty"`
	CursorStart string `json:"cursorStart,omitempty"`
	// Paging: how the API's answer comes in pages; none: as dlt finds them.
	Paging *APIPaging `json:"paging,omitempty"`
}

// APIPaging is how a resource's answer comes in pages, as dlt's paginators
// take it.
type APIPaging struct {
	// Type: cursor (the next page's cursor is in the answer, sent back as
	// a query parameter), link (the next page's URL is in the answer) or
	// single (one page: nothing to follow).
	Type  string `json:"type"`
	Next  string `json:"next,omitempty"`  // cursor: where the cursor is in the answer (a JSONPath: nextCursor); link: where the URL is (next)
	Param string `json:"param,omitempty"` // cursor: the query parameter it's sent back in (cursor)
}

// PagingTypes are the ways a REST API's answer may come in pages.
var PagingTypes = []string{"cursor", "link", "single"}

// ImportDatabase is a database read by a cursor column (no change capture:
// its deletes aren't seen; 8.1 for that).
type ImportDatabase struct {
	Kind   string    `json:"kind"` // postgresql, mysql
	Tables []DBTable `json:"tables"`
}

type DBTable struct {
	Table      string `json:"table"` // schema.table
	Cursor     string `json:"cursor,omitempty"`
	PrimaryKey string `json:"primaryKey,omitempty"`
}

// ImportTable is the project's table an import's resource lands in.
func ImportTable(i Import, resource string) string {
	if _, tb, ok := strings.Cut(resource, "."); ok {
		resource = tb
	}
	return i.Name + "_" + resource
}

// ImportConnID is the platform Airflow's connection holding an import's credentials.
func (p *Project) ImportConnID(i Import) string {
	return "import_" + strings.ReplaceAll(p.Metadata.Name, "-", "_") + "_" + i.Name
}

// A Source is one of the company's databases, queried where it is: a Trino
// catalog of the project's own (SourceCatalog), which its editors, and its
// readers if Readers says so, may read and no one may write. Connection
// names one of the project's connections (host, port, database, username,
// password: a read-only account of the database's own). Schemas, if given,
// are the only ones it may read (default all). Masks and RowFilters narrow
// what its readers see, as a project's tables' do; their tables are
// schema.table.
type Source struct {
	Name       string      `json:"name"`
	Kind       string      `json:"kind"`
	Connection string      `json:"connection"`
	Readers    bool        `json:"readers,omitempty"`
	Schemas    []string    `json:"schemas,omitempty"`
	Masks      []Mask      `json:"masks,omitempty"`
	RowFilters []RowFilter `json:"rowFilters,omitempty"`
	Copy       *Copy       `json:"copy,omitempty"`
}

// Copy is a source's tables copied into the project's own (use case 8.1),
// by change capture: a first copy, then every insert, update and delete,
// within minutes. Each lands in the project's namespace as <source>_<table>
// (CopyTable), under the project's rules like any of its tables. The
// source's account needs what the database's change log asks for (its
// administrator's to grant). Kubernetes only.
type Copy struct {
	Tables []string `json:"tables"` // schema.table (MySQL: database.table)
}

// CopyKinds are the sources that can be copied (Debezium's connectors).
var CopyKinds = []string{"postgresql", "mysql", "sqlserver"}

// CopyName names a source's copying: its Deployment and Secret.
func (p *Project) CopyName(s Source) string {
	return "copy-" + p.Metadata.Name + "-" + strings.ReplaceAll(s.Name, "_", "-")
}

// CopyTable is the project's table a source's table is copied into.
func CopyTable(s Source, table string) string {
	_, tb, _ := strings.Cut(table, ".")
	return s.Name + "_" + tb
}

// CatalogUser is the catalog's (OpenMetadata's) Trino account: it may see
// what's in a source (its schemas, tables and columns), to describe them,
// and read none of its data.
const CatalogUser = "openmetadata"

// LLM is a project's use of the LLM gateway (use case 12.1): a key of its
// own with a monthly budget (USD), kept as its connection <project>-llm.
type LLM struct {
	Budget float64 `json:"budget"`
}

// Cost is a project's monthly budget (use case 13.1, R13.3): what its
// Trino time, its storage and its model calls may cost a month, priced by
// the install's rate card; at 80% and at 100% the project is alerted. It
// doesn't stop anything (6.8's limits cap use; the gateway's budget caps
// the model calls).
type Cost struct {
	Budget float64 `json:"budget"` // a month, in the rate card's currency
}

// A KnowledgeBank is text of the project's, searched by meaning (use case
// 12.6): each source's rows embedded through the gateway on the project's
// key, kept as the table knowledge_<name> in the project's namespace (so
// the project's rules apply to it as to any table), and searched in Trino.
type KnowledgeBank struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Tables      []KnowledgeSource `json:"tables,omitempty"`
	Files       []KnowledgeFiles  `json:"files,omitempty"`    // text files in the project's bucket
	Schedule    string            `json:"schedule,omitempty"` // when the project's pipelines build it afresh (cron, UTC); "": by hand
}

// KnowledgeFiles are the text files (.txt, .md, .html) under a prefix of the
// project's bucket, as landing/docs/: each file's text, its key the ref.
type KnowledgeFiles struct {
	Prefix string `json:"prefix"`
}

// KnowledgeFileFormats are the files a bank reads, by their extension.
var KnowledgeFileFormats = []string{".txt", ".md", ".markdown", ".html", ".htm"}

// DAG is the Airflow DAG that builds the bank on its schedule, in a project.
func (k KnowledgeBank) DAG(project string) string {
	return "knowledge_" + strings.ReplaceAll(project, "-", "_") + "_" + k.Name
}

// A KnowledgeSource is a table's text column, and the column that names each row (its id).
type KnowledgeSource struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	ID     string `json:"id"`
}

// KnowledgeTable is the bank's table, in the project's namespace.
func (k KnowledgeBank) KnowledgeTable() string { return "knowledge_" + k.Name }

// MaintenanceUser is the platform's Trino account for table maintenance,
// erasure and retention: it reads and writes every table, unmasked.
const MaintenanceUser = "maintenance"

// SourceKinds are the databases a source may be (Trino's connectors).
var SourceKinds = []string{"postgresql", "mysql", "sqlserver", "oracle"}

// SourceCatalog is a source's Trino catalog: the project's name, then its own.
func (p *Project) SourceCatalog(s Source) string {
	return strings.ReplaceAll(p.Metadata.Name, "-", "_") + "_" + s.Name
}

// Limits cap a project's share of the platform, so one project can't slow
// the others; what it leaves out is the install's default. Queries count
// against them when a member names the project (Trino's client tag
// project:<name>, as the platform's SQL page and metrics do), and all its
// pipelines' queries. Notebooks are a person's, and have a person's limits.
type Limits struct {
	Queries     int    `json:"queries,omitempty"`     // Trino queries running at once
	Queued      int    `json:"queued,omitempty"`      // queries waiting; more are refused
	QueryMemory string `json:"queryMemory,omitempty"` // Trino memory for its queries: 4GB, or 50% of Trino's
	Storage     string `json:"storage,omitempty"`     // its bucket's size: 500Gi, 2Ti
}

// A Member is a Keycloak group, or one person, with a role. A group's members
// are the project's members for as long as they're in the group.
type Member struct {
	Group string `json:"group,omitempty"`
	User  string `json:"user,omitempty"`
	Role  string `json:"role"`
}

// Tables: an Iceberg namespace in Nessie, queried through Trino.
type Tables struct {
	Catalog   string      `json:"catalog,omitempty"`   // default iceberg
	Namespace string      `json:"namespace,omitempty"` // default: the project's name
	Readers   *ReaderView `json:"readers,omitempty"`
	Checks    []Check     `json:"checks,omitempty"`
	// Freshness: how recently each table must have changed; a table older
	// than that is stale, and the project is told (Notify).
	Freshness []Freshness `json:"freshness,omitempty"`
	// Maintenance: how the platform keeps the tables in shape each night.
	Maintenance *Maintenance `json:"maintenance,omitempty"`
	// Tags: moments of the tables kept (a month-end close, a delivery), each
	// a Nessie tag (<project>.<name>) at a commit, kept until KeepUntil,
	// whatever the history kept otherwise. The operator keeps Nessie's as
	// these say; nightly maintenance removes those past KeepUntil.
	Tags []Tag `json:"tags,omitempty"`
	// People: the tables that hold people's data (consumers'), and the
	// column that identifies a person in each (R6.25): what a delivery of
	// them sends is personal, and a search of the delivery log by a
	// person's ID (use case 9.2) and an erasure (6.9) look there.
	People []PersonKey `json:"people,omitempty"`
	// Retention: rows older than a table's keep (by a date or time column)
	// are deleted by the nightly maintenance (R6.27): 25 months of campaign
	// responses, say.
	Retention []Retention `json:"retention,omitempty"`
	// Adopt: tables of another platform's Iceberg catalog (an external
	// store the administrator set up: the chart's externalStores) taken
	// into the project where their files are. The operator registers each
	// table's current metadata file in the project's namespace, and again
	// whenever the other platform changes it; nothing is copied. They're
	// read-only here while the other platform writes them: the project's
	// writers and pipelines may only read them, and the nightly
	// maintenance leaves them alone.
	Adopt []Adoption `json:"adopt,omitempty"`
}

// An Adoption is a table of an external store taken into the project.
type Adoption struct {
	Table string `json:"table"` // its name in the project
	From  string `json:"from"`  // <store>.<schema>.<table>: where it is now
}

// Adopted says whether a table of the project's is an adopted one.
func (t *Tables) Adopted(table string) bool {
	if t == nil {
		return false
	}
	for _, a := range t.Adopt {
		if a.Table == table {
			return true
		}
	}
	return false
}

// A Retention is how long a table's rows are kept, by the column that dates them.
type Retention struct {
	Table  string `json:"table"`
	Column string `json:"column"` // a date or timestamp column
	Keep   string `json:"keep"`   // 730d, 2y
}

var keepRE = regexp.MustCompile(`^([1-9][0-9]{0,4})(d|y)$`)

// KeepOK says whether a keep is one (days or years: 90d, 7y).
func KeepOK(keep string) bool { return keepRE.MatchString(keep) }

// KeepDays is a keep in days (a year as 365).
func KeepDays(keep string) int {
	m := keepRE.FindStringSubmatch(keep)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "y" {
		return n * 365
	}
	return n
}

// A PersonKey is a table of people's data, and its column identifying a person.
type PersonKey struct {
	Table  string `json:"table"`
	Column string `json:"column"` // customer_id
}

// A Tag is a moment of every table, kept: the catalog at a commit.
type Tag struct {
	Name      string `json:"name"`      // close-2026-09: Nessie's is <project>.<name>
	Commit    string `json:"commit"`    // the catalog's commit (main) it keeps
	KeepUntil string `json:"keepUntil"` // a date, 2033-10-03
	Note      string `json:"note,omitempty"`
	By        string `json:"by,omitempty"` // who made it
}

// KeepTags keeps the project's tags across a release: they're moments made
// on the platform (the History tab), not code, so a project.yaml that
// doesn't name one doesn't take it away. One it names as well is the
// platform's, as it was made.
func KeepTags(before, p *Project) {
	if before == nil || before.Spec.Tables == nil || len(before.Spec.Tables.Tags) == 0 {
		return
	}
	tables := Tables{}
	if p.Spec.Tables != nil {
		tables = *p.Spec.Tables
	}
	tags := slices.Clone(before.Spec.Tables.Tags)
	for _, t := range tables.Tags {
		if !slices.ContainsFunc(tags, func(k Tag) bool { return k.Name == t.Name }) {
			tags = append(tags, t)
		}
	}
	tables.Tags = tags
	p.Spec.Tables = &tables
}

// NessieTag is a tag's name in Nessie: the project's name, then its own.
func (p *Project) NessieTag(t Tag) string { return p.Metadata.Name + "." + t.Name }

// Maintenance is the nightly upkeep of a project's tables: small files
// compacted into larger ones. (Their history, and files nothing uses, are
// the catalog's to collect, across the install: Nessie keeps every table's
// history as its commits, which all projects share.)
type Maintenance struct {
	Paused bool `json:"paused,omitempty"` // none for now
}

// Freshness is a table's freshness target: changed within the last Within
// (as 30m, 24h or 7d).
type Freshness struct {
	Table  string `json:"table"`
	Within string `json:"within"`
}

// Notify is where the project's alerts go: a failed pipeline, a failing
// check, a stale table. Teams names one of the project's connections, a
// Secret holding a Teams channel's Workflows webhook (key webhook); email
// goes through the install's mail server.
type Notify struct {
	Teams string   `json:"teams,omitempty"`
	Slack string   `json:"slack,omitempty"` // a connection holding a Slack incoming webhook (key webhook)
	Email []string `json:"email,omitempty"`
}

// A Check is a data-quality test on one of the project's tables. The catalog
// runs it, and shows its result on the table.
type Check struct {
	Name      string   `json:"name,omitempty"` // what to call it (a sql check needs one; the others are named by table, column and kind)
	Table     string   `json:"table"`
	Column    string   `json:"column,omitempty"` // for unique, notNull, acceptedValues, range, regex, references
	Check     string   `json:"check"`            // one of CheckTypes
	Min       *int64   `json:"min,omitempty"`    // rowCount: rows; range: the column's values
	Max       *int64   `json:"max,omitempty"`
	Values    []string `json:"values,omitempty"`   // acceptedValues: what the column may hold
	Pattern   string   `json:"pattern,omitempty"`  // regex: what every value matches
	RefTable  string   `json:"refTable,omitempty"` // references: the table and column every value is found in
	RefColumn string   `json:"refColumn,omitempty"`
	SQL       string   `json:"sql,omitempty"` // sql: a query that must return no rows
}

// CheckTypes are the checks a project can ask for.
var CheckTypes = []string{"unique", "notNull", "rowCount", "acceptedValues", "range", "regex", "references", "sql"}

// ReaderView narrows what readers see. Editors and pipelines see everything.
type ReaderView struct {
	Tables     []string    `json:"tables,omitempty"` // only these; default all
	RowFilters []RowFilter `json:"rowFilters,omitempty"`
	Masks      []Mask      `json:"masks,omitempty"`
}

type RowFilter struct {
	Table  string `json:"table"`
	Filter string `json:"filter"` // a SQL condition, e.g. region = 'EU'
}

type Mask struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Type   string `json:"type"` // one of MaskTypes
}

// Files: a bucket in Buckets.
type Files struct {
	Bucket string `json:"bucket,omitempty"` // default: the project's name
}

// Pipelines: a Keycloak client whose service account runs the project's
// pipelines. It reads and writes the tables, and the files under landing/.
// With a repo, the pipelines' code (Airflow's DAGs) comes from it: Airflow
// takes it as a DAG bundle of the project's own (BundleName). On Kubernetes
// such a project runs its pipelines in an Airflow of its own (AirflowName),
// as a service account of its own (PipelinesClientID), in Memory.
type Pipelines struct {
	ServiceAccount string          `json:"serviceAccount"`        // the Keycloak client's ID
	Repo           string          `json:"repo,omitempty"`        // a git repo: https://... or ssh, never with credentials in it
	Path           string          `json:"path,omitempty"`        // the DAGs' folder in it; default the whole repo
	Ref            string          `json:"ref,omitempty"`         // a branch, tag or commit; default the install's own
	Credentials    *GitCredentials `json:"credentials,omitempty"` // for a repo that isn't public
	Memory         string          `json:"memory,omitempty"`      // its Airflow's, as 3Gi or 2560Mi; default the install's
	Reviews        *Reviews        `json:"reviews,omitempty"`     // merge requests checked on a data branch
}

// Reviews checks each open merge request's change to the repo's dbt models
// on a data branch of its own (use case 7.3): a Nessie branch of the
// project's tables, where the models it touches (and those downstream) are
// built and tested, as the project's pipelines; the merge request is told
// how it went (a commit status, a comment). The repo's credentials (a
// token) need GitLab's API: reading merge requests, writing statuses and
// notes.
type Reviews struct {
	GitLab  string `json:"gitlab"`            // GitLab's address: https://gitlab.example.com
	Project string `json:"project,omitempty"` // the repo's path there (group/repo); default the repo URL's
	Dbt     string `json:"dbt"`               // the dbt project's folder in the repo
}

// ReviewBranch is a merge request's Nessie branch, and ReviewCatalog the
// Trino catalog on it.
func (p *Project) ReviewBranch(iid int) string { return fmt.Sprintf("%s-mr-%d", p.Metadata.Name, iid) }
func (p *Project) ReviewCatalog(iid int) string {
	return fmt.Sprintf("%s_mr_%d", strings.ReplaceAll(p.Metadata.Name, "-", "_"), iid)
}

// GitCredentials name where the repo's credentials are kept, never the
// credentials themselves: one of the project's connections (on Kubernetes,
// a Secret in the project's namespace labelled
// platform.storscale.io/connection=<project>; a Secret without it, the
// platform's or another project's, is refused) with username and password
// (a token), or sshPrivateKey. The git hosts' SSH keys are the install's
// (Airflow's known hosts), not a project's.
type GitCredentials struct {
	SecretRef string `json:"secretRef"`
}

// MaskTypes are Ranger's column masks for Trino.
var MaskTypes = []string{"MASK", "MASK_SHOW_LAST_4", "MASK_SHOW_FIRST_4", "MASK_HASH", "MASK_NULL", "MASK_NONE", "MASK_DATE_SHOW_YEAR"}

var (
	nameRE        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	secretRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	gitPathRE     = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
	gitRefRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`)
	scpRepoRE     = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9.-]+:[A-Za-z0-9_.~/-]+$`) // git@host:org/repo.git
	identifierRE  = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	principalRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,99}$`)
	memoryRE      = regexp.MustCompile(`^([1-9][0-9]{0,5})(Mi|Gi)$`)
	queryMemRE    = regexp.MustCompile(`^([1-9][0-9]{0,5}(MB|GB)|([1-9][0-9]?|100)%)$`)
	storageRE     = regexp.MustCompile(`^([1-9][0-9]{0,5})(Gi|Ti)$`)
	tagNameRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	schemaTableRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}\.[a-z_][a-z0-9_]{0,62}$`)
	cronRE        = regexp.MustCompile(`^[0-9*/,-]+( [0-9*/,-]+){4}$`)
	sourceNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,19}$`)
	commitRE      = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
	withinRE      = regexp.MustCompile(`^([1-9][0-9]{0,4})(m|h|d)$`)
	emailRE       = regexp.MustCompile(`^[^@\s,]+@[^@\s,]+\.[^@\s,]+$`)
)

// Parse reads a Project from JSON, fills in its defaults and validates it.
func Parse(data []byte) (*Project, error) {
	var p Project
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("not a project: %w", err)
	}
	p.Default()
	return &p, p.Validate()
}

// Default fills in what a spec may leave out.
func (p *Project) Default() {
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}
	if p.Kind == "" {
		p.Kind = Kind
	}
	if t := p.Spec.Tables; t != nil {
		if t.Catalog == "" {
			t.Catalog = "iceberg"
		}
		if t.Namespace == "" {
			t.Namespace = strings.ReplaceAll(p.Metadata.Name, "-", "_")
		}
	}
	if f := p.Spec.Files; f != nil && f.Bucket == "" {
		f.Bucket = p.Metadata.Name
	}
}

// Within reads a freshness target: 30m, 24h, 7d.
func Within(q string) (time.Duration, bool) {
	m := withinRE.FindStringSubmatch(q)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	return time.Duration(n) * unit, true
}

// StorageBytes is a storage limit in bytes: 0 when there's none (or it's not one).
func StorageBytes(q string) int64 {
	m := storageRE.FindStringSubmatch(q)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	if m[2] == "Ti" {
		return n << 40
	}
	return n << 30
}

// problems are what's wrong with the pipelines' repo, path, ref and credentials.
func (pl *Pipelines) problems() []string {
	var out []string
	if pl.Repo == "" {
		if pl.Path != "" || pl.Ref != "" || pl.Credentials != nil || pl.Memory != "" || pl.Reviews != nil {
			out = append(out, "path, ref, credentials, memory and reviews need a repo")
		}
		return out
	}
	if r := pl.Reviews; r != nil {
		switch {
		case !strings.HasPrefix(r.GitLab, "https://") && !strings.HasPrefix(r.GitLab, "http://"):
			out = append(out, fmt.Sprintf("reviews: gitlab %q: GitLab's address, https://...", r.GitLab))
		case pl.Credentials == nil || !strings.HasPrefix(pl.Repo, "https://") && !strings.HasPrefix(pl.Repo, "http://"):
			out = append(out, "reviews: GitLab's API needs the repo's credentials (a token, for an https repo)")
		case r.Dbt == "" || !gitPathRE.MatchString(r.Dbt) || slices.Contains(strings.Split(r.Dbt, "/"), ".."):
			out = append(out, fmt.Sprintf("reviews: dbt %q: the dbt project's folder in the repo", r.Dbt))
		case r.Project != "" && !gitPathRE.MatchString(r.Project):
			out = append(out, fmt.Sprintf("reviews: project %q: the repo's path in GitLab, as group/repo", r.Project))
		case r.Project != "" && sameHost(r.GitLab, pl.Repo) && !strings.EqualFold(strings.TrimSuffix(r.Project, ".git"), repoPath(pl.Repo)):
			out = append(out, fmt.Sprintf("reviews: project %q isn't the repo's path, %q: leave it empty for the repo's own", r.Project, repoPath(pl.Repo)))
		}
	}
	if pl.Memory != "" {
		if mi, ok := MiB(pl.Memory); !ok || mi < MinMemoryMiB || mi > MaxMemoryMiB {
			out = append(out, fmt.Sprintf("memory %q: from 2Gi to 64Gi, as 3Gi or 2560Mi", pl.Memory))
		}
	}
	ssh := false
	if u, err := url.Parse(pl.Repo); err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh") {
		switch {
		case u.Host == "" || u.Path == "" || u.Path == "/":
			out = append(out, fmt.Sprintf("repo %q needs a host and a path", pl.Repo))
		case u.RawQuery != "" || u.Fragment != "":
			out = append(out, fmt.Sprintf("repo %q: no query or fragment", pl.Repo))
		case u.Scheme == "ssh":
			ssh = true
			if _, set := u.User.Password(); set {
				out = append(out, "repo: no password in the address; name a Secret in credentials")
			}
		case u.User != nil:
			out = append(out, "repo: no user or token in the address; name a Secret in credentials")
		}
	} else if scpRepoRE.MatchString(pl.Repo) {
		ssh = true
	} else {
		out = append(out, fmt.Sprintf("repo %q: an https:// or ssh address of a git repo", pl.Repo))
	}
	if pl.Path != "" && (!gitPathRE.MatchString(pl.Path) || slices.Contains(strings.Split(pl.Path, "/"), "..") || len(pl.Path) > 200) {
		out = append(out, fmt.Sprintf("path %q: a folder in the repo, as dags or etl/dags", pl.Path))
	}
	if pl.Ref != "" && (!gitRefRE.MatchString(pl.Ref) || strings.Contains(pl.Ref, "..") || strings.HasSuffix(pl.Ref, "/") ||
		strings.HasSuffix(pl.Ref, ".lock")) {
		out = append(out, fmt.Sprintf("ref %q: a branch, tag or commit", pl.Ref))
	}
	switch c := pl.Credentials; {
	case c != nil && !secretRE.MatchString(c.SecretRef):
		out = append(out, fmt.Sprintf("credentials: secretRef %q isn't a Secret's name", c.SecretRef))
	case c == nil && ssh:
		out = append(out, "an ssh repo needs credentials: a Secret with sshPrivateKey")
	}
	return out
}

// BundleName is the Airflow DAG bundle of the project's pipelines repo.
func (p *Project) BundleName() string { return "project-" + p.Metadata.Name }

// GitConnID is the Airflow connection holding the repo's credentials.
func (p *Project) GitConnID() string { return "git_" + strings.ReplaceAll(p.Metadata.Name, "-", "_") }

// AirflowName names the project's own Airflow on Kubernetes: its Deployment,
// Service and Secret. Its UI is at the platform's /airflow/<project>/.
func (p *Project) AirflowName() string { return "project-" + p.Metadata.Name + "-airflow" }

// AirflowURL is where the platform reaches the project's own Airflow (its API).
func (p *Project) AirflowURL() string {
	return "http://" + p.AirflowName() + ":8090/airflow/" + p.Metadata.Name
}

// AirflowPlatformPasswordKey is the platform account's password in the
// project's Airflow's Secret (AirflowName), which the operator makes.
const AirflowPlatformPasswordKey = "PLATFORM_PASSWORD"

// AirflowDatabase is its Airflow's database, and the Postgres user that owns it.
func (p *Project) AirflowDatabase() string {
	return "airflow_" + strings.ReplaceAll(p.Metadata.Name, "-", "_")
}

// PipelinesClientID is the Keycloak client whose service account runs the
// pipelines of a project with an Airflow of its own: a member of this
// project alone.
func (p *Project) PipelinesClientID() string { return "project-" + p.Metadata.Name + "-pipelines" }

// ImportIdentityConnID is the shared Airflow's connection holding the
// project's own pipelines' sign-in (PipelinesClientID), which its imports
// read its landing area with when it has an Airflow of its own.
func (p *Project) ImportIdentityConnID() string {
	return "pipelines_" + strings.ReplaceAll(p.Metadata.Name, "-", "_")
}

// The memory a project's Airflow may have, in MiB.
const (
	MinMemoryMiB = 2048
	MaxMemoryMiB = 64 * 1024
)

// MiB reads a memory size given as <n>Mi or <n>Gi.
func MiB(q string) (int, bool) {
	m := memoryRE.FindStringSubmatch(q)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "Gi" {
		n *= 1024
	}
	return n, true
}

// Hash identifies a spec: the operator applies a project again when it changes.
func (p *Project) Hash() string {
	b, _ := json.Marshal(p.Spec)
	sum := sha256.New()
	sum.Write(append([]byte(p.Metadata.Name+"\x00"), b...))
	// The custom roles its members are given, as defined now: a role changed is a project to apply again.
	for _, name := range p.CustomRoles() {
		r, ok := p.Roles[name]
		rb, _ := json.Marshal(struct {
			Defined     bool
			Permissions []string
		}{ok, r.Granted()})
		sum.Write(append([]byte("\x00role:"+name+"\x00"), rb...))
	}
	// Its products' consumers' custom roles that read: a role changed there is a policy changed here.
	consumers := make([]string, 0, len(p.ConsumerRoleGroups))
	for c := range p.ConsumerRoleGroups {
		consumers = append(consumers, c)
	}
	sort.Strings(consumers)
	for _, c := range consumers {
		sum.Write([]byte("\x00consumer:" + c + "\x00" + strings.Join(p.ConsumerRoleGroups[c], ",")))
	}
	return hex.EncodeToString(sum.Sum(nil)[:8])
}

// Group is the Keycloak group that holds a project's members in one role (or
// its pipelines' service account): <project>-readers, -editors, -pipelines,
// and -role-<name> for a custom role (16.1). The same name is their Buckets
// policy and their group in Ranger.
func (p *Project) Group(role string) string {
	switch role {
	case Reader:
		return p.Metadata.Name + "-readers"
	case Editor:
		return p.Metadata.Name + "-editors"
	case "pipelines":
		return p.Metadata.Name + "-pipelines"
	}
	return p.Metadata.Name + "-role-" + role
}

// Groups are all of a project's groups, whether it uses them or not: what to
// remove when the project goes.
func (p *Project) Groups() []string {
	out := []string{p.Group(Reader), p.Group(Editor), p.Group("pipelines")}
	for _, r := range p.CustomRoles() {
		out = append(out, p.Group(r))
	}
	return out
}

// RangerPrefix starts the name of every Ranger policy a project owns.
func (p *Project) RangerPrefix() string { return "project:" + p.Metadata.Name + ":" }

// Statement is a Buckets (S3) policy statement.
type Statement struct {
	Effect    string         `json:"Effect"`
	Action    []string       `json:"Action"`
	Resource  []string       `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

// BucketsPolicies are the project's Buckets policies, by name (each is a group's).
func (p *Project) BucketsPolicies() map[string][]Statement {
	f := p.Spec.Files
	if f == nil {
		return nil
	}
	arn := "arn:aws:s3:::" + f.Bucket
	read := []Statement{
		{Effect: "Allow", Action: []string{"s3:GetBucketLocation", "s3:ListBucket"}, Resource: []string{arn}},
		{Effect: "Allow", Action: []string{"s3:GetObject"}, Resource: []string{arn + "/*"}},
	}
	write := []Statement{
		{Effect: "Allow", Action: []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"}, Resource: []string{arn}},
		{Effect: "Allow", Action: []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"},
			Resource: []string{arn + "/*"}},
	}
	out := map[string][]Statement{}
	for _, name := range append([]string{Reader, Editor}, p.CustomRoles()...) {
		switch r, ok := p.RoleOf(name); {
		case !ok:
		case r.Has(PermWriteFiles):
			out[p.Group(name)] = write
		case r.Has(PermReadFiles):
			out[p.Group(name)] = read
		}
	}
	if p.Spec.Pipelines != nil {
		out[p.Group("pipelines")] = []Statement{
			{Effect: "Allow", Action: []string{"s3:GetBucketLocation"}, Resource: []string{arn}},
			{Effect: "Allow", Action: []string{"s3:ListBucket"}, Resource: []string{arn},
				Condition: map[string]any{"StringLike": map[string][]string{"s3:prefix": {"landing/", "landing/*"}}}},
			{Effect: "Allow", Action: []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"},
				Resource: []string{arn + "/landing/*"}},
		}
	}
	return out
}

// RangerPolicy is an Apache Ranger policy for the Trino service, in the shape
// of Ranger's public API (without the service and IDs).
type RangerPolicy struct {
	Name                 string                    `json:"name"`
	PolicyType           int                       `json:"policyType"` // 0 access, 1 masking, 2 row filter
	Resources            map[string]RangerResource `json:"resources"`
	PolicyItems          []RangerItem              `json:"policyItems,omitempty"`
	DataMaskPolicyItems  []RangerItem              `json:"dataMaskPolicyItems,omitempty"`
	RowFilterPolicyItems []RangerItem              `json:"rowFilterPolicyItems,omitempty"`
	Description          string                    `json:"description,omitempty"`
}

type RangerResource struct {
	Values      []string `json:"values"`
	IsExcludes  bool     `json:"isExcludes"`
	IsRecursive bool     `json:"isRecursive"`
}

type RangerItem struct {
	Groups        []string         `json:"groups"`
	Users         []string         `json:"users"`
	DelegateAdmin bool             `json:"delegateAdmin"`
	Accesses      []RangerAccess   `json:"accesses"`
	DataMaskInfo  *RangerMask      `json:"dataMaskInfo,omitempty"`
	RowFilterInfo *RangerRowFilter `json:"rowFilterInfo,omitempty"`
}

type RangerAccess struct {
	Type      string `json:"type"`
	IsAllowed bool   `json:"isAllowed"`
}

type RangerMask struct {
	DataMaskType string `json:"dataMaskType"`
	ValueExpr    string `json:"valueExpr,omitempty"` // CUSTOM: the expression, {col} the column
}

// maskInfo is Ranger's mask for one of MaskTypes, on a column of a type (as
// the catalog names it; "" when unknown, taken as text). MASK_HASH is written
// as a custom expression on Trino's own sha256: the plugin's built-in
// transformer for it names a function Trino doesn't have. A mask must give
// the column's type back, and the redacting transformers (MASK, the
// show-first/last ones, a hash) are text, so on a column that isn't text (a
// number, a date) they become a null: Trino refuses the query otherwise
// ("Invalid column mask"), for every reader.
func maskInfo(maskType, colType string) *RangerMask {
	switch maskType {
	case "MASK_HASH":
		// A number hashed stays a number of its type, so the masked column
		// keeps its nulls, its duplicates and its joins (a customer_id hashed
		// the same in two tables still joins), and the catalog's checks on it
		// (not null, unique, references) still answer truly; its values don't show.
		switch strings.ToLower(colType) {
		case "bigint":
			return &RangerMask{DataMaskType: "CUSTOM", ValueExpr: "from_big_endian_64(substr(sha256(to_utf8(cast({col} as varchar))), 1, 8))"}
		case "integer", "int":
			return &RangerMask{DataMaskType: "CUSTOM", ValueExpr: "from_big_endian_32(substr(sha256(to_utf8(cast({col} as varchar))), 1, 4))"}
		}
		if !textType(colType) {
			return &RangerMask{DataMaskType: "MASK_NULL"}
		}
		return &RangerMask{DataMaskType: "CUSTOM", ValueExpr: "cast(to_hex(sha256(to_utf8({col}))) as varchar)"}
	case "MASK", "MASK_SHOW_LAST_4", "MASK_SHOW_FIRST_4":
		if !textType(colType) {
			return &RangerMask{DataMaskType: "MASK_NULL"}
		}
	}
	return &RangerMask{DataMaskType: maskType}
}

func textType(t string) bool {
	t = strings.ToLower(t)
	return t == "" || strings.HasPrefix(t, "varchar") || strings.HasPrefix(t, "char") || t == "string" || t == "text"
}

type RangerRowFilter struct {
	FilterExpr string `json:"filterExpr"`
}

func resources(kv ...string) map[string]RangerResource {
	out := map[string]RangerResource{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = RangerResource{Values: []string{kv[i+1]}}
	}
	return out
}

// atAnyRef is a table's resource at every Nessie reference too: a table is
// read as it was through its name (orders@close-2026-09, orders#<commit>),
// and a reader's access, masks and row filters must reach those names, or
// the past would show what the present hides.
func atAnyRef(r map[string]RangerResource) map[string]RangerResource {
	tb := r["table"].Values[0]
	r["table"] = RangerResource{Values: []string{tb, tb + "@*", tb + "#*"}}
	return r
}

func allow(group string, accesses ...string) RangerItem {
	it := RangerItem{Groups: []string{group}, Users: []string{}}
	for _, a := range accesses {
		it.Accesses = append(it.Accesses, RangerAccess{Type: a, IsAllowed: true})
	}
	return it
}

// repoPath is a repo's path in its GitLab, from its address: group/repo.
func repoPath(repo string) string {
	u, err := url.Parse(repo)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
}

// sameHost says whether two addresses name the same server.
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

// RangerPolicies are the Ranger policies for the project's tables: one per
// resource (Ranger allows no more), named with RangerPrefix.
func (p *Project) RangerPolicies() []RangerPolicy {
	return append(append(p.tablePolicies(), p.sourcePolicies()...), p.ProductPolicies()...)
}

// sourcePolicies let the project read its sources (editors and pipelines,
// and readers where a source says so), and no one write them: Ranger allows
// only what a policy allows.
func (p *Project) sourcePolicies() []RangerPolicy {
	var out []RangerPolicy
	desc := "From the project " + p.Metadata.Name + ": the platform operator keeps it as the project says."
	for _, src := range p.Spec.Sources {
		cat, named := p.SourceCatalog(src), p.RangerPrefix()+"source:"+src.Name
		who := append(p.tableWriters(), p.wholeReaders()...)
		if p.Spec.Pipelines != nil {
			who = append(who, p.Group("pipelines"))
		}
		if src.Readers {
			who = append(who, p.maskedReaders()...)
		}
		items := func(accesses ...string) []RangerItem {
			var its []RangerItem
			for _, g := range who {
				its = append(its, allow(g, accesses...))
			}
			return its
		}
		// Its schemas, or all; information_schema with them (Trino shows
		// in it only what each person may see).
		schemas := []string{"*"}
		if len(src.Schemas) > 0 {
			schemas = append(sorted(src.Schemas), "information_schema")
		}
		catalogUser := RangerItem{Groups: []string{}, Users: []string{CatalogUser}}
		see := func(its []RangerItem, accesses ...string) []RangerItem {
			it := catalogUser
			for _, a := range accesses {
				it.Accesses = append(it.Accesses, RangerAccess{Type: a, IsAllowed: true})
			}
			return append(its, it)
		}
		inSchemas := func(kv ...string) map[string]RangerResource {
			r := resources(append([]string{"catalog", cat, "schema", "-"}, kv...)...)
			r["schema"] = RangerResource{Values: schemas}
			return r
		}
		out = append(out,
			RangerPolicy{Name: named, Description: desc, Resources: resources("catalog", cat), PolicyItems: see(items("use", "show"), "use", "show")},
			RangerPolicy{Name: named + ":schemas", Description: desc, Resources: inSchemas(), PolicyItems: see(items("use", "show"), "use", "show")},
			RangerPolicy{Name: named + ":tables", Description: desc,
				Resources: inSchemas("table", "*", "column", "*"), PolicyItems: see(items("select", "show"), "show")},
			// The catalog reads what's there from information_schema (its
			// schemata, tables, columns): metadata, never a table's rows.
			RangerPolicy{Name: named + ":metadata", Description: desc,
				Resources: resources("catalog", cat, "schema", "information_schema", "table", "*", "column", "*"), PolicyItems: see(nil, "select")})
		for _, m := range src.Masks {
			sc, tb, _ := strings.Cut(m.Table, ".")
			var its []RangerItem
			for _, g := range p.maskedReaders() {
				it := allow(g, "select")
				it.DataMaskInfo = maskInfo(m.Type, "")
				its = append(its, it)
			}
			out = append(out, RangerPolicy{Name: named + ":mask:" + m.Table + "." + m.Column, PolicyType: 1, Description: desc,
				Resources:           resources("catalog", cat, "schema", sc, "table", tb, "column", m.Column),
				DataMaskPolicyItems: its})
		}
		for _, f := range src.RowFilters {
			sc, tb, _ := strings.Cut(f.Table, ".")
			var its []RangerItem
			for _, g := range p.maskedReaders() {
				it := allow(g, "select")
				it.RowFilterInfo = &RangerRowFilter{FilterExpr: f.Filter}
				its = append(its, it)
			}
			out = append(out, RangerPolicy{Name: named + ":filter:" + f.Table, PolicyType: 2, Description: desc,
				Resources:            resources("catalog", cat, "schema", sc, "table", tb),
				RowFilterPolicyItems: its})
		}
	}
	return out
}

// ReviewPolicies let the project's editors read, and its pipelines build
// in, the Trino catalogs of its open merge requests' data branches (use case
// 7.3): its namespace there, and nothing else.
func (p *Project) ReviewPolicies(iids []int) []RangerPolicy {
	t := p.Spec.Tables
	if t == nil || p.Spec.Pipelines == nil {
		return nil
	}
	desc := "From the project " + p.Metadata.Name + ": a merge request's data branch, which the platform operator keeps."
	readAll, pipelines := append(p.tableWriters(), p.wholeReaders()...), p.Group("pipelines")
	var out []RangerPolicy
	readers := func(accesses ...string) []RangerItem {
		var its []RangerItem
		for _, g := range readAll {
			its = append(its, allow(g, accesses...))
		}
		return its
	}
	for _, iid := range iids {
		cat, named := p.ReviewCatalog(iid), p.RangerPrefix()+fmt.Sprintf("review:%d", iid)
		schemas := resources("catalog", cat, "schema", "-")
		schemas["schema"] = RangerResource{Values: []string{t.Namespace, "information_schema"}}
		tables := resources("catalog", cat, "schema", "-", "table", "*", "column", "*")
		tables["schema"] = schemas["schema"]
		out = append(out,
			RangerPolicy{Name: named, Description: desc, Resources: resources("catalog", cat),
				PolicyItems: append(readers("use", "show"), allow(pipelines, "use", "show"))},
			RangerPolicy{Name: named + ":schemas", Description: desc, Resources: schemas,
				PolicyItems: append(readers("use", "show"), allow(pipelines, "use", "show"))},
			RangerPolicy{Name: named + ":tables", Description: desc, Resources: tables,
				PolicyItems: append(readers("select", "show"),
					allow(pipelines, "select", "insert", "delete", "create", "drop", "alter", "show"))})
	}
	return out
}

func (p *Project) tablePolicies() []RangerPolicy {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	// By how each role reads (16.1): its writers (editors), those who read
	// every table as it is, and those under the readers' rules (readers).
	writers, whole, masked, pipelines := p.tableWriters(), p.wholeReaders(), p.maskedReaders(), p.Group("pipelines")
	hasPipelines := p.Spec.Pipelines != nil
	desc := "From the project " + p.Metadata.Name + ": the platform operator keeps it as the project says."
	named := func(n string) string { return p.RangerPrefix() + n }
	each := func(groups []string, accesses ...string) []RangerItem {
		var its []RangerItem
		for _, g := range groups {
			its = append(its, allow(g, accesses...))
		}
		return its
	}

	schema := RangerPolicy{Name: named("namespace"), Description: desc,
		Resources:   resources("catalog", t.Catalog, "schema", t.Namespace),
		PolicyItems: append(append(each(writers, "all"), each(whole, "use", "show")...), each(masked, "use", "show")...)}
	tables := RangerPolicy{Name: named("tables"), Description: desc,
		Resources:   resources("catalog", t.Catalog, "schema", t.Namespace, "table", "*", "column", "*"),
		PolicyItems: append(each(writers, "all"), each(whole, "select", "show")...)}
	if hasPipelines {
		schema.PolicyItems = append(schema.PolicyItems, allow(pipelines, "use", "show"))
		// The pipelines make their tables too (dbt's models: made, swapped
		// in by a rename, dropped), in the project's namespace alone.
		tables.PolicyItems = append(tables.PolicyItems, allow(pipelines, "select", "insert", "delete", "create", "drop", "alter", "show"))
	}
	var view ReaderView
	if t.Readers != nil {
		view = *t.Readers
	}
	// The projects consuming its data products (use case 10.3) see its
	// namespace, read the products' tables, and have the readers' masks and
	// row filters on them: in these policies, as Ranger keeps one to a resource.
	consumers, allConsumers := p.consumerGroups()
	for _, g := range allConsumers {
		schema.PolicyItems = append(schema.PolicyItems, allow(g, "use", "show"))
	}
	// Each of the project's groups' items, then the consumers' (a copy of the first, for each consumer's group).
	alsoForAll := func(its []RangerItem, table string) []RangerItem {
		if len(its) == 0 {
			return its
		}
		for _, g := range consumers[table] {
			c := its[0]
			c.Groups = []string{g}
			its = append(its, c)
		}
		return its
	}
	alsoForEach := func(groups []string, table string, accesses ...string) []RangerItem {
		return alsoForAll(each(groups, accesses...), table)
	}
	// Adopted tables are read-only here, for everyone (the platform they
	// came from still writes them): out of the wildcard, which lets the
	// writers do anything, and in a policy of their own allowing reads,
	// which also holds the readers' and consumers' reads of them (Ranger
	// keeps one policy to a resource).
	var adopted []string
	for _, a := range t.Adopt {
		adopted = append(adopted, a.Table, a.Table+"@*", a.Table+"#*")
	}
	if len(adopted) > 0 {
		tables.Resources["table"] = RangerResource{Values: adopted, IsExcludes: true}
	}
	out := []RangerPolicy{schema}
	for _, a := range t.Adopt {
		its := append(each(writers, "select", "show"), each(whole, "select", "show")...)
		if hasPipelines {
			its = append(its, allow(pipelines, "select", "show"))
		}
		if len(view.Tables) == 0 || slices.Contains(view.Tables, a.Table) {
			its = append(its, each(masked, "select", "show")...)
		}
		out = append(out, RangerPolicy{Name: named("adopted:" + a.Table), Description: desc,
			Resources:   atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", a.Table, "column", "*")),
			PolicyItems: alsoForAll(its, a.Table)})
	}
	if len(view.Tables) == 0 {
		tables.PolicyItems = append(tables.PolicyItems, each(masked, "select", "show")...)
		out = append(out, tables)
	} else {
		out = append(out, tables)
		for _, tb := range sorted(view.Tables) {
			if t.Adopted(tb) {
				continue
			}
			out = append(out, RangerPolicy{Name: named("read:" + tb), Description: desc,
				Resources:   atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", tb, "column", "*")),
				PolicyItems: alsoForEach(masked, tb, "select", "show")})
		}
	}
	// Those who read the tables as they are (its editors, a read-all role)
	// come first, unmasked and unfiltered: Ranger takes the first item a
	// person matches, and an editor of the project may also be in a group
	// that's a consumer's or a reader's (alice, an analyst, edits it).
	asIs := append(slices.Clone(writers), whole...)
	for _, m := range view.Masks {
		var its []RangerItem
		for _, g := range masked {
			it := allow(g, "select")
			it.DataMaskInfo = maskInfo(m.Type, "")
			its = append(its, it)
		}
		var items []RangerItem
		for _, g := range asIs {
			it := allow(g, "select")
			it.DataMaskInfo = &RangerMask{DataMaskType: "MASK_NONE"}
			items = append(items, it)
		}
		out = append(out, RangerPolicy{Name: named("mask:" + m.Table + "." + m.Column), PolicyType: 1, Description: desc,
			Resources:           atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", m.Table, "column", m.Column)),
			DataMaskPolicyItems: append(items, alsoForAll(its, m.Table)...)})
	}
	for _, f := range view.RowFilters {
		var its []RangerItem
		for _, g := range masked {
			it := allow(g, "select")
			it.RowFilterInfo = &RangerRowFilter{FilterExpr: f.Filter}
			its = append(its, it)
		}
		var items []RangerItem
		for _, g := range asIs {
			it := allow(g, "select")
			// No filter at all (an empty one: Ranger applies none): Trino
			// refuses to update a table with any row filter, "true" too.
			it.RowFilterInfo = &RangerRowFilter{FilterExpr: ""}
			items = append(items, it)
		}
		out = append(out, RangerPolicy{Name: named("filter:" + f.Table), PolicyType: 2, Description: desc,
			Resources:            atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", f.Table)),
			RowFilterPolicyItems: append(items, alsoForAll(its, f.Table)...)})
	}
	return out
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return slices.Compact(out)
}
