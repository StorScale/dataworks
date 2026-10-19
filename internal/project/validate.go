package project

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The spec's checks, one per area of it (members, tables, pipelines, sources,
// imports, deliveries, ...), each saying what's wrong in plain words. Validate
// runs them all and gathers every problem at once, so a spec is fixed in one
// go. A new area adds its check here, with a table test in validate_test.go.

// validators are the checks, in the order their problems are reported.
var validators = []func(p *Project, bad func(format string, a ...any)){
	(*Project).checkMembers, (*Project).checkTables, (*Project).checkFiles, (*Project).checkPipelines,
	(*Project).productProblems, (*Project).agentProblems, (*Project).checkSemantic, (*Project).checkHistory, (*Project).checkNotify,
	(*Project).checkSources, (*Project).checkImports, (*Project).checkDeliveries, (*Project).checkLimits,
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
	for _, check := range validators {
		check(p, bad)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// checkMembers checks who the project's members are: groups or users, readers or editors.
func (p *Project) checkMembers(bad func(format string, a ...any)) {
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
}

// checkTables checks the tables' names, the readers' rules (their tables, row filters and masks) and the checks on them.
func (p *Project) checkTables(bad func(format string, a ...any)) {
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
}

// checkFiles checks the project's bucket.
func (p *Project) checkFiles(bad func(format string, a ...any)) {
	if f := p.Spec.Files; f != nil && !nameRE.MatchString(f.Bucket) {
		bad("files: bucket %q: 3 to 40 lowercase letters, digits and dashes", f.Bucket)
	}
}

// checkPipelines checks the pipelines' service account and repo.
func (p *Project) checkPipelines(bad func(format string, a ...any)) {
	if pl := p.Spec.Pipelines; pl != nil {
		if !principalRE.MatchString(pl.ServiceAccount) {
			bad("pipelines: serviceAccount %q isn't a Keycloak client ID", pl.ServiceAccount)
		}
		for _, e := range pl.problems() {
			bad("pipelines: %s", e)
		}
	}
}

// checkSemantic checks the semantic layer's settings: the shared models it builds on.
func (p *Project) checkSemantic(bad func(format string, a ...any)) {
	if sm := p.Spec.Semantic; sm != nil {
		for _, u := range sm.Uses {
			switch {
			case !nameRE.MatchString(u):
				bad("semantic: uses: %q isn't a project's name", u)
			case u == p.Metadata.Name:
				bad("semantic: uses: a project's model doesn't build on itself")
			}
		}
	}
}

// checkHistory checks what the catalog keeps of the tables: freshness, the people's tables, the tags.
func (p *Project) checkHistory(bad func(format string, a ...any)) {
	if t := p.Spec.Tables; t != nil {
		for _, f := range t.Freshness {
			if !identifierRE.MatchString(f.Table) {
				bad("tables: freshness: table %q isn't a table's name", f.Table)
			}
			if _, ok := Within(f.Within); !ok {
				bad("tables: freshness: within %q: as 30m, 24h or 7d", f.Within)
			}
		}
		for _, rt := range t.Retention {
			if !identifierRE.MatchString(rt.Table) || !identifierRE.MatchString(rt.Column) {
				bad("tables: retention: %q.%q: a table and the column that dates its rows", rt.Table, rt.Column)
			}
			if !KeepOK(rt.Keep) {
				bad("tables: retention: %s: keep %q: as 730d or 2y", rt.Table, rt.Keep)
			}
		}
		people := map[string]bool{}
		for _, pk := range t.People {
			switch {
			case !identifierRE.MatchString(pk.Table) || !identifierRE.MatchString(pk.Column):
				bad("tables: people: %q.%q: a table and its column identifying a person", pk.Table, pk.Column)
			case people[pk.Table]:
				bad("tables: people: %s twice", pk.Table)
			}
			people[pk.Table] = true
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
}

// checkNotify checks where alerts go.
func (p *Project) checkNotify(bad func(format string, a ...any)) {
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
}

// checkSources checks the sources: databases queried where they are, and copied.
func (p *Project) checkSources(bad func(format string, a ...any)) {
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
		if c := src.Copy; c != nil {
			switch {
			case !slices.Contains(CopyKinds, src.Kind):
				bad("sources: %s: a %s database can't be copied yet (%s can)", src.Name, src.Kind, strings.Join(CopyKinds, ", "))
			case p.Spec.Tables == nil:
				bad("sources: %s: its tables are copied into the project's, and it has none (tables)", src.Name)
			case len(c.Tables) == 0:
				bad("sources: %s: copy names no tables", src.Name)
			}
			into := map[string]string{}
			for _, t := range c.Tables {
				if !schemaTableRE.MatchString(t) {
					bad("sources: %s: copy: %q: a table as schema.table", src.Name, t)
					continue
				}
				if other, ok := into[CopyTable(src, t)]; ok {
					bad("sources: %s: copy: %s and %s would both be %s", src.Name, other, t, CopyTable(src, t))
				}
				into[CopyTable(src, t)] = t
			}
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
}

// checkImports checks the imports: what each copies, by its source's kind.
func (p *Project) checkImports(bad func(format string, a ...any)) {
	seenImport := map[string]bool{}
	for _, im := range p.Spec.Imports {
		switch {
		case !sourceNameRE.MatchString(im.Name):
			bad("imports: name %q: 1 to 20 lowercase letters, digits and underscores, starting with a letter", im.Name)
		case seenImport[im.Name]:
			bad("imports: %q twice", im.Name)
		case p.Spec.Tables == nil:
			bad("imports: %s: its tables are the project's, and it has none (tables)", im.Name)
		}
		seenImport[im.Name] = true
		if im.Connection != "" && !secretRE.MatchString(im.Connection) {
			bad("imports: %s: connection %q isn't a connection's name", im.Name, im.Connection)
		}
		if im.Schedule != "" && !cronRE.MatchString(im.Schedule) {
			bad("imports: %s: schedule %q: five cron fields, as 0 * * * *", im.Name, im.Schedule)
		}
		into := map[string]bool{}
		landing := func(name string) {
			if into[ImportTable(im, name)] {
				bad("imports: %s: two resources would both be %s", im.Name, ImportTable(im, name))
			}
			into[ImportTable(im, name)] = true
		}
		switch im.Source {
		case "rest_api":
			a := im.API
			if a == nil || !strings.HasPrefix(a.BaseURL, "https://") && !strings.HasPrefix(a.BaseURL, "http://") || len(a.Resources) == 0 {
				bad("imports: %s: a rest_api import has an api: its baseUrl (http or https) and its resources", im.Name)
				break
			}
			for _, r := range a.Resources {
				if !identifierRE.MatchString(r.Name) || r.Path == "" || strings.Contains(r.Path, "://") {
					bad("imports: %s: a resource has a name (an identifier) and a path under the baseUrl: %q", im.Name, r.Name)
				}
				if (r.Cursor != "") != (r.CursorParam != "") || r.Cursor != "" && r.CursorStart == "" {
					bad("imports: %s: %s: a cursor names its field, its query parameter and where it starts", im.Name, r.Name)
				}
				landing(r.Name)
			}
		case "salesforce":
			sf := im.Salesforce
			if sf == nil || len(sf.Objects) == 0 || im.Connection == "" {
				bad("imports: %s: a salesforce import names its objects (salesforce.objects) and a connection (kind salesforce)", im.Name)
				break
			}
			for _, o := range sf.Objects {
				if !slices.Contains(SalesforceObjects, o) {
					bad("imports: %s: %q isn't one of Salesforce's objects: %s", im.Name, o, strings.Join(SalesforceObjects, ", "))
				}
				landing(o)
			}
			if _, err := time.Parse("2006-01-02", sf.Since); sf.Since != "" && err != nil {
				bad("imports: %s: since %q: a date, as 2024-01-01", im.Name, sf.Since)
			}
		case "files":
			f := im.Files
			if f == nil || len(f.Tables) == 0 {
				bad("imports: %s: a files import names its tables (files.tables), each a file of the landing area", im.Name)
				break
			}
			if im.Connection != "" {
				bad("imports: %s: a files import needs no connection: it reads the project's landing area as its pipelines", im.Name)
			}
			for _, t := range f.Tables {
				if !identifierRE.MatchString(t.Name) || t.PrimaryKey != "" && !identifierRE.MatchString(t.PrimaryKey) {
					bad("imports: %s: a table has a name (an identifier), and a primary key that is one: %q", im.Name, t.Name)
				}
				if t.File == "" || strings.Contains(t.File, "/") || strings.Contains(t.File, "..") || FileFormat(t.File) == "" {
					bad("imports: %s: %s: a file of landing/uploads/ (no folders), ending in one of %s: %q", im.Name, t.Name, strings.Join(FileFormats, ", "), t.File)
				}
				landing(t.Name)
			}
		case "sql_database":
			d := im.Database
			if d == nil || !slices.Contains([]string{"postgresql", "mysql"}, d.Kind) || len(d.Tables) == 0 || im.Connection == "" {
				bad("imports: %s: a sql_database import has a database (its kind, postgresql or mysql, and its tables) and a connection", im.Name)
				break
			}
			for _, t := range d.Tables {
				if !schemaTableRE.MatchString(t.Table) || t.Cursor != "" && !identifierRE.MatchString(t.Cursor) || t.PrimaryKey != "" && !identifierRE.MatchString(t.PrimaryKey) {
					bad("imports: %s: a table as schema.table, its cursor and primary key columns: %q", im.Name, t.Table)
				}
				landing(t.Table)
			}
		default:
			bad("imports: %s: source %q: one of %s", im.Name, im.Source, strings.Join(ImportSources, ", "))
		}
	}
}

// checkDeliveries checks the deliveries: what each sends, where, when and how.
func (p *Project) checkDeliveries(bad func(format string, a ...any)) {
	seenDelivery := map[string]bool{}
	for _, d := range p.Spec.Deliveries {
		switch {
		case !sourceNameRE.MatchString(d.Name):
			bad("deliveries: name %q: 1 to 20 lowercase letters, digits and underscores, starting with a letter", d.Name)
		case seenDelivery[d.Name]:
			bad("deliveries: %q twice", d.Name)
		case p.Spec.Tables == nil:
			bad("deliveries: %s: it sends the project's tables, and it has none (tables)", d.Name)
		}
		seenDelivery[d.Name] = true
		if (d.Table == "") == (d.Query == "") {
			bad("deliveries: %s: a table or a query, one of them", d.Name)
		}
		if d.Table != "" && !identifierRE.MatchString(d.Table) {
			bad("deliveries: %s: table %q isn't one of the project's names", d.Name, d.Table)
		}
		if q := strings.ToLower(strings.TrimSpace(d.Query)); d.Query != "" && (!strings.HasPrefix(q, "select") && !strings.HasPrefix(q, "with") || strings.Contains(q, ";")) {
			bad("deliveries: %s: its query is one SELECT", d.Name)
		}
		if !slices.Contains(DeliveryFormats, d.Format) {
			bad("deliveries: %s: format %q: one of %s", d.Name, d.Format, strings.Join(DeliveryFormats, ", "))
		}
		if d.Files == "" || strings.ContainsAny(d.Files, "/\\") || !strings.Contains(d.Files, "{date}") {
			bad("deliveries: %s: files %q: a name with {date} (and {part}, split), no folders", d.Name, d.Files)
		}
		if (d.SplitBy != "") != strings.Contains(d.Files, "{part}") || d.SplitBy != "" && !identifierRE.MatchString(d.SplitBy) {
			bad("deliveries: %s: splitBy (a column) and {part} in the files' name go together", d.Name)
		}
		if !cronRE.MatchString(d.Schedule) {
			bad("deliveries: %s: schedule %q: five cron fields, as 0 6 * * 1", d.Name, d.Schedule)
		}
		if !secretRE.MatchString(d.Target) || d.PGP != "" && !secretRE.MatchString(d.PGP) {
			bad("deliveries: %s: its target and pgp are connections' names", d.Name)
		}
		if c := d.Checks; c != nil {
			if c.MinRows < 0 || c.MaxRows < 0 || c.MaxRows > 0 && c.MaxRows < c.MinRows {
				bad("deliveries: %s: checks: rows from minRows to maxRows", d.Name)
			}
			if _, ok := Within(c.Within); c.Within != "" && !ok {
				bad("deliveries: %s: checks: within %q: as 30m, 24h, 7d", d.Name, c.Within)
			}
			if len(c.Parts) > 0 && d.SplitBy == "" {
				bad("deliveries: %s: checks: parts need splitBy", d.Name)
			}
		}
	}
}

// checkLimits checks the project's limits on queries and storage.
func (p *Project) checkLimits(bad func(format string, a ...any)) {
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
}
