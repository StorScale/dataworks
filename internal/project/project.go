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
}

// CatalogUser is the catalog's (OpenMetadata's) Trino account: it may see
// what's in a source (its schemas, tables and columns), to describe them,
// and read none of its data.
const CatalogUser = "openmetadata"

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
}

// A Tag is a moment of every table, kept: the catalog at a commit.
type Tag struct {
	Name      string `json:"name"`      // close-2026-09: Nessie's is <project>.<name>
	Commit    string `json:"commit"`    // the catalog's commit (main) it keeps
	KeepUntil string `json:"keepUntil"` // a date, 2033-10-03
	Note      string `json:"note,omitempty"`
	By        string `json:"by,omitempty"` // who made it
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
	Email []string `json:"email,omitempty"`
}

// A Check is a data-quality test on one of the project's tables. The catalog
// runs it, and shows its result on the table.
type Check struct {
	Table  string `json:"table"`
	Column string `json:"column,omitempty"` // for unique and notNull
	Check  string `json:"check"`            // one of CheckTypes
	Min    *int64 `json:"min,omitempty"`    // for rowCount
	Max    *int64 `json:"max,omitempty"`
}

// CheckTypes are the checks a project can ask for.
var CheckTypes = []string{"unique", "notNull", "rowCount"}

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

// Validate says what's wrong with a project, all at once.
func (p *Project) Validate() error {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if p.APIVersion != APIVersion || p.Kind != Kind {
		bad("apiVersion and kind must be %s and %s", APIVersion, Kind)
	}
	if !nameRE.MatchString(p.Metadata.Name) {
		bad("name %q: 3 to 40 lowercase letters, digits and dashes, starting with a letter", p.Metadata.Name)
	}
	if len(p.Spec.Members) == 0 {
		bad("a project needs at least one member")
	}
	for i, m := range p.Spec.Members {
		switch {
		case (m.Group == "") == (m.User == ""):
			bad("member %d: give a group or a user, not both", i+1)
		case m.Group != "" && !principalRE.MatchString(m.Group), m.User != "" && !principalRE.MatchString(m.User):
			bad("member %d: %q isn't a group or user name", i+1, m.Group+m.User)
		}
		if m.Role != Reader && m.Role != Editor {
			bad("member %d: role %q must be %s or %s", i+1, m.Role, Reader, Editor)
		}
	}
	if t := p.Spec.Tables; t != nil {
		if !identifierRE.MatchString(t.Catalog) || !identifierRE.MatchString(t.Namespace) {
			bad("tables: catalog %q and namespace %q must be lowercase identifiers", t.Catalog, t.Namespace)
		}
		if r := t.Readers; r != nil {
			for _, tb := range r.Tables {
				if !identifierRE.MatchString(tb) {
					bad("readers: table %q isn't an identifier", tb)
				}
			}
			for _, f := range r.RowFilters {
				if !identifierRE.MatchString(f.Table) || strings.TrimSpace(f.Filter) == "" {
					bad("readers: row filter on %q needs a table and a condition", f.Table)
				}
			}
			for _, m := range r.Masks {
				if !identifierRE.MatchString(m.Table) || !identifierRE.MatchString(m.Column) {
					bad("readers: mask on %q.%q needs a table and a column", m.Table, m.Column)
				}
				if !slices.Contains(MaskTypes, m.Type) {
					bad("readers: mask type %q must be one of %s", m.Type, strings.Join(MaskTypes, ", "))
				}
			}
		}
	}
	if t := p.Spec.Tables; t != nil {
		for i, c := range t.Checks {
			switch {
			case !identifierRE.MatchString(c.Table):
				bad("check %d: table %q isn't an identifier", i+1, c.Table)
			case !slices.Contains(CheckTypes, c.Check):
				bad("check %d: %q must be one of %s", i+1, c.Check, strings.Join(CheckTypes, ", "))
			case c.Check != "rowCount" && !identifierRE.MatchString(c.Column):
				bad("check %d: %s needs a column", i+1, c.Check)
			case c.Check == "rowCount" && c.Min == nil && c.Max == nil:
				bad("check %d: rowCount needs a min, a max, or both", i+1)
			}
		}
	}
	if f := p.Spec.Files; f != nil && !nameRE.MatchString(f.Bucket) {
		bad("files: bucket %q: 3 to 40 lowercase letters, digits and dashes", f.Bucket)
	}
	if pl := p.Spec.Pipelines; pl != nil {
		if !principalRE.MatchString(pl.ServiceAccount) {
			bad("pipelines: serviceAccount %q isn't a Keycloak client ID", pl.ServiceAccount)
		}
		for _, e := range pl.problems() {
			bad("pipelines: %s", e)
		}
	}
	if t := p.Spec.Tables; t != nil {
		for _, f := range t.Freshness {
			if !identifierRE.MatchString(f.Table) {
				bad("tables: freshness: table %q isn't a table's name", f.Table)
			}
			if _, ok := Within(f.Within); !ok {
				bad("tables: freshness: within %q: as 30m, 24h or 7d", f.Within)
			}
		}
		seen := map[string]bool{}
		for _, tg := range t.Tags {
			switch {
			case !tagNameRE.MatchString(tg.Name):
				bad("tables: tags: %q isn't a tag's name (lower case letters, digits and -)", tg.Name)
			case seen[tg.Name]:
				bad("tables: tags: %q twice", tg.Name)
			case !commitRE.MatchString(tg.Commit):
				bad("tables: tags: %s: commit %q isn't a Nessie commit", tg.Name, tg.Commit)
			case len(tg.Note) > 500:
				bad("tables: tags: %s: a note of at most 500 characters", tg.Name)
			}
			if _, err := time.Parse("2006-01-02", tg.KeepUntil); err != nil {
				bad("tables: tags: %s: keepUntil %q: a date, as 2033-10-03", tg.Name, tg.KeepUntil)
			}
			seen[tg.Name] = true
		}
	}
	if n := p.Spec.Notify; n != nil {
		if n.Teams != "" && !secretRE.MatchString(n.Teams) {
			bad("notify: teams %q isn't a connection's name", n.Teams)
		}
		for _, e := range n.Email {
			if !emailRE.MatchString(e) {
				bad("notify: %q isn't an email address", e)
			}
		}
	}
	seenSource := map[string]bool{}
	for _, src := range p.Spec.Sources {
		switch {
		case !sourceNameRE.MatchString(src.Name):
			bad("sources: name %q: 1 to 20 lowercase letters, digits and underscores, starting with a letter", src.Name)
		case seenSource[src.Name]:
			bad("sources: %q twice", src.Name)
		}
		seenSource[src.Name] = true
		if !slices.Contains(SourceKinds, src.Kind) {
			bad("sources: %s: kind %q: one of %s", src.Name, src.Kind, strings.Join(SourceKinds, ", "))
		}
		if !secretRE.MatchString(src.Connection) {
			bad("sources: %s: connection %q isn't a connection's name", src.Name, src.Connection)
		}
		for _, sc := range src.Schemas {
			if !identifierRE.MatchString(sc) || sc == "information_schema" {
				bad("sources: %s: schema %q isn't an identifier", src.Name, sc)
			}
		}
		if (len(src.Masks) > 0 || len(src.RowFilters) > 0) && !src.Readers {
			bad("sources: %s: masks and row filters are what readers see, and its readers can't read it (readers: true)", src.Name)
		}
		for _, f := range src.RowFilters {
			if !schemaTableRE.MatchString(f.Table) || strings.TrimSpace(f.Filter) == "" {
				bad("sources: %s: a row filter needs a table, as schema.table, and a condition: %q", src.Name, f.Table)
			}
		}
		for _, m := range src.Masks {
			if !schemaTableRE.MatchString(m.Table) || !identifierRE.MatchString(m.Column) {
				bad("sources: %s: a mask needs a table, as schema.table, and a column: %q.%q", src.Name, m.Table, m.Column)
			}
			if !slices.Contains(MaskTypes, m.Type) {
				bad("sources: %s: mask type %q must be one of %s", src.Name, m.Type, strings.Join(MaskTypes, ", "))
			}
		}
	}
	if l := p.Spec.Limits; l != nil {
		if l.Queries < 0 || l.Queries > 100 {
			bad("limits: queries %d: from 1 to 100 at once", l.Queries)
		}
		if l.Queued < 0 || l.Queued > 1000 {
			bad("limits: queued %d: up to 1000", l.Queued)
		}
		if l.QueryMemory != "" && !queryMemRE.MatchString(l.QueryMemory) {
			bad("limits: queryMemory %q: as 4GB, 512MB or 50%%", l.QueryMemory)
		}
		if l.Storage != "" {
			if !storageRE.MatchString(l.Storage) {
				bad("limits: storage %q: as 500Gi or 2Ti", l.Storage)
			} else if p.Spec.Files == nil {
				bad("limits: storage needs files (a bucket)")
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
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
		if pl.Path != "" || pl.Ref != "" || pl.Credentials != nil || pl.Memory != "" {
			out = append(out, "path, ref, credentials and memory need a repo")
		}
		return out
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
	sum := sha256.Sum256(append([]byte(p.Metadata.Name+"\x00"), b...))
	return hex.EncodeToString(sum[:8])
}

// Group is the Keycloak group that holds a project's members in one role (or
// its pipelines' service account): <project>-readers, -editors, -pipelines.
// The same name is their Buckets policy and their group in Ranger.
func (p *Project) Group(role string) string {
	switch role {
	case Reader:
		return p.Metadata.Name + "-readers"
	case Editor:
		return p.Metadata.Name + "-editors"
	}
	return p.Metadata.Name + "-pipelines"
}

// Groups are all of a project's groups, whether it uses them or not: what to
// remove when the project goes.
func (p *Project) Groups() []string {
	return []string{p.Group(Reader), p.Group(Editor), p.Group("pipelines")}
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
	out := map[string][]Statement{p.Group(Reader): read, p.Group(Editor): write}
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

// RangerPolicies are the Ranger policies for the project's tables: one per
// resource (Ranger allows no more), named with RangerPrefix.
func (p *Project) RangerPolicies() []RangerPolicy {
	return append(p.tablePolicies(), p.sourcePolicies()...)
}

// sourcePolicies let the project read its sources (editors and pipelines,
// and readers where a source says so), and no one write them: Ranger allows
// only what a policy allows.
func (p *Project) sourcePolicies() []RangerPolicy {
	var out []RangerPolicy
	desc := "From the project " + p.Metadata.Name + ": the platform operator keeps it as the project says."
	for _, src := range p.Spec.Sources {
		cat, named := p.SourceCatalog(src), p.RangerPrefix()+"source:"+src.Name
		who := []string{p.Group(Editor)}
		if p.Spec.Pipelines != nil {
			who = append(who, p.Group("pipelines"))
		}
		if src.Readers {
			who = append(who, p.Group(Reader))
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
				Resources: inSchemas("table", "*", "column", "*"), PolicyItems: see(items("select", "show"), "show")})
		for _, m := range src.Masks {
			sc, tb, _ := strings.Cut(m.Table, ".")
			it := allow(p.Group(Reader), "select")
			it.DataMaskInfo = &RangerMask{DataMaskType: m.Type}
			out = append(out, RangerPolicy{Name: named + ":mask:" + m.Table + "." + m.Column, PolicyType: 1, Description: desc,
				Resources:           resources("catalog", cat, "schema", sc, "table", tb, "column", m.Column),
				DataMaskPolicyItems: []RangerItem{it}})
		}
		for _, f := range src.RowFilters {
			sc, tb, _ := strings.Cut(f.Table, ".")
			it := allow(p.Group(Reader), "select")
			it.RowFilterInfo = &RangerRowFilter{FilterExpr: f.Filter}
			out = append(out, RangerPolicy{Name: named + ":filter:" + f.Table, PolicyType: 2, Description: desc,
				Resources:            resources("catalog", cat, "schema", sc, "table", tb),
				RowFilterPolicyItems: []RangerItem{it}})
		}
	}
	return out
}

func (p *Project) tablePolicies() []RangerPolicy {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	readers, editors, pipelines := p.Group(Reader), p.Group(Editor), p.Group("pipelines")
	hasPipelines := p.Spec.Pipelines != nil
	desc := "From the project " + p.Metadata.Name + ": the platform operator keeps it as the project says."
	named := func(n string) string { return p.RangerPrefix() + n }

	schema := RangerPolicy{Name: named("namespace"), Description: desc,
		Resources:   resources("catalog", t.Catalog, "schema", t.Namespace),
		PolicyItems: []RangerItem{allow(editors, "all"), allow(readers, "use", "show")}}
	tables := RangerPolicy{Name: named("tables"), Description: desc,
		Resources:   resources("catalog", t.Catalog, "schema", t.Namespace, "table", "*", "column", "*"),
		PolicyItems: []RangerItem{allow(editors, "all")}}
	if hasPipelines {
		schema.PolicyItems = append(schema.PolicyItems, allow(pipelines, "use", "show"))
		tables.PolicyItems = append(tables.PolicyItems, allow(pipelines, "select", "insert", "delete", "show"))
	}
	var view ReaderView
	if t.Readers != nil {
		view = *t.Readers
	}
	out := []RangerPolicy{schema}
	if len(view.Tables) == 0 {
		tables.PolicyItems = append(tables.PolicyItems, allow(readers, "select", "show"))
		out = append(out, tables)
	} else {
		out = append(out, tables)
		for _, tb := range sorted(view.Tables) {
			out = append(out, RangerPolicy{Name: named("read:" + tb), Description: desc,
				Resources:   atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", tb, "column", "*")),
				PolicyItems: []RangerItem{allow(readers, "select", "show")}})
		}
	}
	for _, m := range view.Masks {
		it := allow(readers, "select")
		it.DataMaskInfo = &RangerMask{DataMaskType: m.Type}
		out = append(out, RangerPolicy{Name: named("mask:" + m.Table + "." + m.Column), PolicyType: 1, Description: desc,
			Resources:           atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", m.Table, "column", m.Column)),
			DataMaskPolicyItems: []RangerItem{it}})
	}
	for _, f := range view.RowFilters {
		it := allow(readers, "select")
		it.RowFilterInfo = &RangerRowFilter{FilterExpr: f.Filter}
		out = append(out, RangerPolicy{Name: named("filter:" + f.Table), PolicyType: 2, Description: desc,
			Resources:            atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", f.Table)),
			RowFilterPolicyItems: []RangerItem{it}})
	}
	return out
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return slices.Compact(out)
}
