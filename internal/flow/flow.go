// Package flow is a pipeline built on a canvas (use cases 11.1 and 11.2): a
// set of steps, each a dbt model in the project's repo under
// models/flows/<flow>/, compiled to the SQL Trino runs. The canvas is a view
// of that folder: a model the canvas wrote starts with its step as YAML in a
// comment (-- storscale-step:) and a hash of the SQL under it; one changed
// by hand, or written without a header, is a code step from then on.
package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The kinds of step (R11.1).
const (
	Input   = "input"
	Select  = "select"
	Retype  = "retype"
	Filter  = "filter"
	Join    = "join"
	Union   = "union"
	Dedupe  = "dedupe"
	Group   = "group"
	Pivot   = "pivot"
	Unpivot = "unpivot"
	Window  = "window"
	Formula = "formula"
	Prepare = "prepare" // named processors on columns (R11.7; prepare.go)
	Output  = "output"
	Metric  = "metric" // a metric query on the semantic layer's definitions (11.2's step 3)
	Code    = "code"   // a model written or changed by hand (11.2)
)

var Kinds = []string{Input, Select, Retype, Filter, Join, Union, Dedupe, Group, Pivot, Unpivot, Window, Formula, Prepare, Output, Metric, Code}

// A Column is one of a step's result, with its Trino type ("" when the
// platform can't tell: a formula without a type, a code step not yet run).
type Column struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// A ColumnSetting is a column a step names: kept and renamed (select),
// cast (retype), or computed (formula).
type ColumnSetting struct {
	Name       string `json:"name" yaml:"name"`
	As         string `json:"as,omitempty" yaml:"as,omitempty"`                 // select: its new name
	Type       string `json:"type,omitempty" yaml:"type,omitempty"`             // retype: cast to; formula: what it is
	Expression string `json:"expression,omitempty" yaml:"expression,omitempty"` // formula: a Trino expression
}

// A Pair is a join's condition: a column of the left input equal to one of the right.
type Pair struct {
	Left  string `json:"left" yaml:"left"`
	Right string `json:"right" yaml:"right"`
}

// An Ordering is a column to order by.
type Ordering struct {
	Column string `json:"column" yaml:"column"`
	Desc   bool   `json:"desc,omitempty" yaml:"desc,omitempty"`
}

// An Aggregate is a group step's measure.
type Aggregate struct {
	Function string `json:"function" yaml:"function"` // sum, count, min, max, avg, count_distinct
	Column   string `json:"column,omitempty" yaml:"column,omitempty"`
	As       string `json:"as,omitempty" yaml:"as,omitempty"`
}

// A WindowFunction is a window step's new column.
type WindowFunction struct {
	Function string `json:"function" yaml:"function"` // rank, dense_rank, row_number, running_sum, sum, avg, min, max, count, lag, lead
	Column   string `json:"column,omitempty" yaml:"column,omitempty"`
	As       string `json:"as" yaml:"as"`
	Offset   int    `json:"offset,omitempty" yaml:"offset,omitempty"` // lag and lead: how far (default 1)
}

// A Check is a dbt test on the output's column.
type Check struct {
	Column string   `json:"column" yaml:"column"`
	Test   string   `json:"test" yaml:"test"` // not_null, unique, accepted_values
	Values []string `json:"values,omitempty" yaml:"values,omitempty"`
}

// A Step is one of the flow's. Which settings matter depends on its kind;
// the others are empty. Its YAML form is the model's header.
type Step struct {
	ID          string   `json:"id" yaml:"-"`
	Kind        string   `json:"kind" yaml:"kind"`
	Description string   `json:"description,omitempty" yaml:"-"`
	Inputs      []string `json:"inputs,omitempty" yaml:"inputs,omitempty"` // steps, by ID
	// input: a table (of the project's by default; a product it consumes
	// names its producer's namespace).
	Table     string `json:"table,omitempty" yaml:"table,omitempty"`
	Catalog   string `json:"catalog,omitempty" yaml:"catalog,omitempty"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	// select, retype, formula
	Columns []ColumnSetting `json:"columns,omitempty" yaml:"columns,omitempty"`
	// filter
	Where string `json:"where,omitempty" yaml:"where,omitempty"`
	// join
	Join string `json:"join,omitempty" yaml:"join,omitempty"` // inner, left, full
	On   []Pair `json:"on,omitempty" yaml:"on,omitempty"`
	// union
	Distinct bool `json:"distinct,omitempty" yaml:"distinct,omitempty"`
	// dedupe (by: the columns that make a row the same; none: whole rows),
	// group (by), pivot (by: the rows), window (partition and order)
	By        []string   `json:"by,omitempty" yaml:"by,omitempty"`
	Order     []Ordering `json:"order,omitempty" yaml:"order,omitempty"`
	Partition []string   `json:"partition,omitempty" yaml:"partition,omitempty"`
	// group
	Aggregates []Aggregate `json:"aggregates,omitempty" yaml:"aggregates,omitempty"`
	// pivot: one column per value of Column, each Function of Value
	Column   string   `json:"column,omitempty" yaml:"column,omitempty"`
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
	Value    string   `json:"value,omitempty" yaml:"value,omitempty"`
	Function string   `json:"function,omitempty" yaml:"function,omitempty"`
	// unpivot: Columns' names become rows of Name and Value
	Unpivot []string `json:"unpivot,omitempty" yaml:"unpivot,omitempty"`
	Name    string   `json:"name,omitempty" yaml:"name,omitempty"`
	// window
	Windows []WindowFunction `json:"windows,omitempty" yaml:"windows,omitempty"`
	// prepare: processors on columns, in order
	Processors []Processor `json:"processors,omitempty" yaml:"processors,omitempty"`
	// output: the table it makes (Table), and its checks
	Checks []Check `json:"checks,omitempty" yaml:"checks,omitempty"`
	// metric: a metric query (its metrics, grouped by By, filtered by Where),
	// whose SQL the semantic layer gives, so a flow's revenue is the company's
	Metrics []string `json:"metrics,omitempty" yaml:"metrics,omitempty"`
	// code: the model as written (its whole body, config and all)
	SQL string `json:"sql,omitempty" yaml:"-"`
	// The canvas's layout.
	X float64 `json:"x" yaml:"-"`
	Y float64 `json:"y" yaml:"-"`
}

// A Flow is a canvas: its steps, one of them the output.
type Flow struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule,omitempty"` // cron (UTC); "" is by hand
	Steps    []Step `json:"steps"`
}

var (
	nameRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	typeRE     = regexp.MustCompile(`^(varchar|char|varbinary|tinyint|smallint|integer|bigint|real|double|decimal|boolean|date|time|timestamp|json|uuid)(\([0-9]+(, ?[0-9]+)?\))?( with time zone)?$`)
	Aggregates = []string{"sum", "count", "min", "max", "avg", "count_distinct"}
	Windows    = []string{"rank", "dense_rank", "row_number", "running_sum", "sum", "avg", "min", "max", "count", "lag", "lead"}
	Tests      = []string{"not_null", "unique", "accepted_values"}
	Joins      = []string{"inner", "left", "full"}
)

// NameOK says whether s can name a flow, a step or a column the canvas makes.
func NameOK(s string) bool { return nameRE.MatchString(s) }

// Step is the step with that ID, or nil.
func (f *Flow) Step(id string) *Step {
	for i := range f.Steps {
		if f.Steps[i].ID == id {
			return &f.Steps[i]
		}
	}
	return nil
}

// OutputStep is the flow's output step, or nil.
func (f *Flow) OutputStep() *Step {
	for i := range f.Steps {
		if f.Steps[i].Kind == Output {
			return &f.Steps[i]
		}
	}
	return nil
}

// Check says what's wrong with the flow's shape: its names, each step's
// inputs (how many, and that they exist), and that the steps form a DAG
// ending in one output. What a step does with its columns is Compile's.
func (f *Flow) Check() error {
	if !NameOK(f.Name) {
		return fmt.Errorf("a flow's name is lowercase letters, digits and underscores, starting with a letter: %q isn't", f.Name)
	}
	seen := map[string]bool{}
	outputs := 0
	for i := range f.Steps {
		s := &f.Steps[i]
		switch {
		case !NameOK(s.ID):
			return fmt.Errorf("a step's name is lowercase letters, digits and underscores, starting with a letter: %q isn't", s.ID)
		case seen[s.ID]:
			return fmt.Errorf("two steps named %s", s.ID)
		case !slices.Contains(Kinds, s.Kind):
			return fmt.Errorf("step %s: no step kind %q (one of %s)", s.ID, s.Kind, strings.Join(Kinds, ", "))
		}
		seen[s.ID] = true
		if s.Kind == Output || f.CodeOutput(s) {
			outputs++
		}
	}
	for i := range f.Steps {
		s := &f.Steps[i]
		want := [2]int{1, 1}
		switch s.Kind {
		case Input, Metric:
			want = [2]int{0, 0}
		case Join:
			want = [2]int{2, 2}
		case Union:
			want = [2]int{2, 1 << 20}
		case Code:
			want = [2]int{0, 1 << 20}
		}
		if n := len(s.Inputs); n < want[0] || n > want[1] {
			return fmt.Errorf("step %s (%s): %s", s.ID, s.Kind, inputsWanted(s.Kind, want, n))
		}
		for _, in := range s.Inputs {
			if !seen[in] {
				return fmt.Errorf("step %s: its input %s isn't a step of the flow", s.ID, in)
			}
			if in == s.ID {
				return fmt.Errorf("step %s: its own input", s.ID)
			}
		}
	}
	if outputs > 1 {
		return fmt.Errorf("a flow has one output step; this one has %d", outputs)
	}
	if _, err := f.order(); err != nil {
		return err
	}
	return nil
}

func inputsWanted(kind string, want [2]int, n int) string {
	switch {
	case want[0] == want[1] && want[0] == 0:
		return "takes no input"
	case want[0] == want[1]:
		return fmt.Sprintf("takes %d input%s, not %d", want[0], map[bool]string{true: "s", false: ""}[want[0] > 1], n)
	case kind == Union:
		return fmt.Sprintf("takes at least two inputs, not %d", n)
	}
	return fmt.Sprintf("takes %d inputs", n)
}

// order is the steps in an order each comes after its inputs.
func (f *Flow) order() ([]string, error) {
	done := map[string]bool{}
	var out []string
	var visit func(s *Step, path []string) error
	visit = func(s *Step, path []string) error {
		if done[s.ID] {
			return nil
		}
		if slices.Contains(path, s.ID) {
			return fmt.Errorf("the steps go round in a circle: %s", strings.Join(append(path, s.ID), " -> "))
		}
		path = append(path, s.ID)
		for _, in := range s.Inputs {
			if err := visit(f.Step(in), path); err != nil {
				return err
			}
		}
		done[s.ID] = true
		out = append(out, s.ID)
		return nil
	}
	for i := range f.Steps {
		if err := visit(&f.Steps[i], nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- The model files --------------------------------------------------------

const headerMark = "-- storscale-step:"

// Header is a step's definition as the comment at the top of its model:
// its settings as YAML, and the hash of the SQL under it.
func Header(s *Step, body string) string {
	var b strings.Builder
	b.WriteString(headerMark + "\n")
	var y strings.Builder
	enc := yaml.NewEncoder(&y)
	enc.SetIndent(2)
	enc.Encode(s)
	enc.Close()
	for _, line := range strings.Split(strings.TrimRight(y.String(), "\n"), "\n") {
		b.WriteString("--   " + line + "\n")
	}
	b.WriteString("--   sql: " + Hash(body) + "\n")
	return b.String()
}

// Hash is what the header keeps of the SQL under it, so a change by hand shows.
func Hash(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return hex.EncodeToString(sum[:])[:16]
}

// ParseModel reads a model: its step from the header, when it has one and
// the SQL under it is as the header says; otherwise a code step holding the
// model as it is. The step's ID and inputs are the caller's to set (its
// file's name, and its refs).
func ParseModel(text string) (step Step, body string) {
	if !strings.HasPrefix(text, headerMark) {
		return Step{Kind: Code, SQL: text}, text
	}
	lines := strings.Split(text, "\n")
	var y []string
	i := 1
	for ; i < len(lines) && strings.HasPrefix(lines[i], "--"); i++ {
		y = append(y, strings.TrimPrefix(strings.TrimPrefix(lines[i], "--"), "  "))
	}
	body = strings.Join(lines[i:], "\n")
	var head struct {
		Step `yaml:",inline"`
		SQL  string `yaml:"sql"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(y, "\n")), &head); err != nil || head.SQL != Hash(body) || !slices.Contains(Kinds, head.Kind) || head.Kind == Code {
		return Step{Kind: Code, SQL: text}, text
	}
	return head.Step, body
}

var (
	refRE    = regexp.MustCompile(`\{\{\s*ref\(\s*['"]([^'"]+)['"]\s*\)\s*\}\}`)
	sourceRE = regexp.MustCompile(`\{\{\s*source\(\s*['"]([^'"]+)['"]\s*,\s*['"]([^'"]+)['"]\s*\)\s*\}\}`)
	configRE = regexp.MustCompile(`\{\{\s*config\(([^)]*)\)\s*\}\}`)
)

// Refs are the models a code step's SQL refers to.
func Refs(sql string) []string {
	var out []string
	for _, m := range refRE.FindAllStringSubmatch(sql, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// ModelName is a step's dbt model: <flow>__<step>, or the output's table.
func ModelName(flow string, s *Step) string {
	if s.Kind == Output || s.Kind == Code && s.Table != "" {
		return s.Table
	}
	return flow + "__" + s.ID
}

// A Source is a dbt source the flow's inputs read: one per catalog and namespace.
type Source struct {
	Name      string
	Catalog   string
	Namespace string
	Tables    []string
}

// SourceName is the dbt source an input is read through.
func SourceName(flow, catalog, namespace string) string {
	return flow + "__" + catalog + "_" + namespace
}

// Sources are the flow's inputs' tables by catalog and namespace.
func (f *Flow) Sources() []Source {
	var out []Source
	for i := range f.Steps {
		s := &f.Steps[i]
		if s.Kind != Input {
			continue
		}
		name := SourceName(f.Name, s.Catalog, s.Namespace)
		j := slices.IndexFunc(out, func(x Source) bool { return x.Name == name })
		if j < 0 {
			out = append(out, Source{Name: name, Catalog: s.Catalog, Namespace: s.Namespace})
			j = len(out) - 1
		}
		if !slices.Contains(out[j].Tables, s.Table) {
			out[j].Tables = append(out[j].Tables, s.Table)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for i := range out {
		sort.Strings(out[i].Tables)
	}
	return out
}

// Files are the flow's folder, models/flows/<name>/: a model per step (with
// its header, or as written for a code step), the sources its inputs read,
// and schema.yml (dbt's: each model's description and layout, the output's
// checks as tests). Paths are relative to the dbt project.
func Files(c *Compiled) map[string]string {
	f := c.Flow
	dir := "models/flows/" + f.Name + "/"
	out := map[string]string{}
	for i := range f.Steps {
		s := &f.Steps[i]
		body := c.Steps[s.ID].SQL
		if s.Kind == Code {
			out[dir+ModelName(f.Name, s)+".sql"] = s.SQL
			continue
		}
		if body == "" { // after a code step Trino hasn't described: left as it is in the repo
			continue
		}
		mat := "ephemeral"
		if s.Kind == Output {
			mat = "table"
		}
		body = fmt.Sprintf("{{ config(materialized='%s') }}\n%s\n", mat, body)
		out[dir+ModelName(f.Name, s)+".sql"] = Header(s, body) + body
	}
	out[dir+"sources.yml"] = sourcesYAML(f)
	out[dir+"schema.yml"] = schemaYAML(f)
	return out
}

func sourcesYAML(f *Flow) string {
	type table struct {
		Name string `yaml:"name"`
	}
	type source struct {
		Name     string  `yaml:"name"`
		Database string  `yaml:"database"`
		Schema   string  `yaml:"schema"`
		Tables   []table `yaml:"tables"`
	}
	doc := struct {
		Version int      `yaml:"version"`
		Sources []source `yaml:"sources"`
	}{Version: 2}
	for _, s := range f.Sources() {
		src := source{Name: s.Name, Database: s.Catalog, Schema: s.Namespace}
		for _, t := range s.Tables {
			src.Tables = append(src.Tables, table{Name: t})
		}
		doc.Sources = append(doc.Sources, src)
	}
	y, _ := yaml.Marshal(doc)
	return "# The tables this flow reads, as dbt sources: written by the platform's canvas.\n" + string(y)
}

// schemaModel is a model in schema.yml: dbt's properties, and the step's
// place on the canvas under meta.storscale.
type schemaModel struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Meta        map[string]any `yaml:"meta,omitempty"`
	Columns     []schemaColumn `yaml:"columns,omitempty"`
}

type schemaColumn struct {
	Name  string `yaml:"name"`
	Tests []any  `yaml:"tests,omitempty"`
}

func schemaYAML(f *Flow) string {
	doc := struct {
		Version int           `yaml:"version"`
		Models  []schemaModel `yaml:"models"`
	}{Version: 2}
	for i := range f.Steps {
		s := &f.Steps[i]
		m := schemaModel{Name: ModelName(f.Name, s), Description: s.Description,
			Meta: map[string]any{"storscale": map[string]any{"step": s.ID, "x": s.X, "y": s.Y}}}
		if s.Kind == Output {
			byCol := map[string]*schemaColumn{}
			for _, ch := range s.Checks {
				col := byCol[ch.Column]
				if col == nil {
					m.Columns = append(m.Columns, schemaColumn{Name: ch.Column})
					col = &m.Columns[len(m.Columns)-1]
					byCol[ch.Column] = col
				}
				if ch.Test == "accepted_values" {
					col.Tests = append(col.Tests, map[string]any{"accepted_values": map[string]any{"values": ch.Values}})
				} else {
					col.Tests = append(col.Tests, ch.Test)
				}
			}
		}
		doc.Models = append(doc.Models, m)
	}
	y, _ := yaml.Marshal(doc)
	return "# The flow's models (their place on the canvas under meta) and the output's checks: written by the platform's canvas.\n" + string(y)
}

// Read is the flow in a folder's files (paths relative to the folder:
// <model>.sql, sources.yml, schema.yml), as Files wrote them or as someone
// changed them since. A model whose header no longer matches its SQL, or
// has none, is a code step: its inputs are its refs within the flow, and
// its SQL is kept as it is. The order of the steps is schema.yml's, then
// the files' names.
func Read(name string, files map[string]string) (*Flow, error) {
	f := &Flow{Name: name}
	sources := map[string]Source{}
	if y, ok := files["sources.yml"]; ok {
		var doc struct {
			Sources []struct {
				Name     string `yaml:"name"`
				Database string `yaml:"database"`
				Schema   string `yaml:"schema"`
				Tables   []struct {
					Name string `yaml:"name"`
				} `yaml:"tables"`
			} `yaml:"sources"`
		}
		if err := yaml.Unmarshal([]byte(y), &doc); err != nil {
			return nil, fmt.Errorf("sources.yml: %v", err)
		}
		for _, s := range doc.Sources {
			src := Source{Name: s.Name, Catalog: s.Database, Namespace: s.Schema}
			for _, t := range s.Tables {
				src.Tables = append(src.Tables, t.Name)
			}
			sources[s.Name] = src
		}
	}
	type meta struct {
		step        string
		description string
		x, y        float64
		checks      []Check
	}
	metas := map[string]meta{}
	var order []string
	if y, ok := files["schema.yml"]; ok {
		var doc struct {
			Models []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Meta        struct {
					Storscale struct {
						Step string  `yaml:"step"`
						X    float64 `yaml:"x"`
						Y    float64 `yaml:"y"`
					} `yaml:"storscale"`
				} `yaml:"meta"`
				Columns []struct {
					Name  string `yaml:"name"`
					Tests []any  `yaml:"tests"`
				} `yaml:"columns"`
			} `yaml:"models"`
		}
		if err := yaml.Unmarshal([]byte(y), &doc); err != nil {
			return nil, fmt.Errorf("schema.yml: %v", err)
		}
		for _, m := range doc.Models {
			mt := meta{step: m.Meta.Storscale.Step, description: m.Description, x: m.Meta.Storscale.X, y: m.Meta.Storscale.Y}
			for _, c := range m.Columns {
				for _, t := range c.Tests {
					switch t := t.(type) {
					case string:
						mt.checks = append(mt.checks, Check{Column: c.Name, Test: t})
					case map[string]any:
						if av, ok := t["accepted_values"].(map[string]any); ok {
							ch := Check{Column: c.Name, Test: "accepted_values"}
							if vs, ok := av["values"].([]any); ok {
								for _, v := range vs {
									ch.Values = append(ch.Values, fmt.Sprint(v))
								}
							}
							mt.checks = append(mt.checks, ch)
						}
					}
				}
			}
			metas[m.Name] = mt
			order = append(order, m.Name)
		}
	}
	var models []string
	for path := range files {
		if strings.HasSuffix(path, ".sql") && !strings.Contains(path, "/") {
			models = append(models, strings.TrimSuffix(path, ".sql"))
		}
	}
	sort.Strings(models)
	sort.SliceStable(models, func(i, j int) bool {
		a, b := slices.Index(order, models[i]), slices.Index(order, models[j])
		if a < 0 {
			a = len(order)
		}
		if b < 0 {
			b = len(order)
		}
		return a < b
	})
	byModel := map[string]string{} // model -> step ID
	for _, m := range models {
		id := strings.TrimPrefix(m, name+"__")
		if mt, ok := metas[m]; ok && mt.step != "" {
			id = mt.step
		}
		byModel[m] = id
	}
	for _, m := range models {
		text := files[m+".sql"]
		step, _ := ParseModel(text)
		step.ID = byModel[m]
		mt := metas[m]
		step.Description, step.X, step.Y = mt.description, mt.x, mt.y
		if step.Kind == Code {
			step.Inputs = nil
			for _, ref := range Refs(text) {
				in, ok := byModel[ref]
				if !ok {
					return nil, fmt.Errorf("model %s refers to %s, which isn't in the flow's folder: the canvas reads only its own models", m, ref)
				}
				step.Inputs = append(step.Inputs, in)
			}
			if !strings.HasPrefix(m, name+"__") && !isEphemeral(text) {
				step.Table = m // the output, written by hand
			}
		} else if step.Kind == Output {
			step.Table = m
			step.Checks = mt.checks
		}
		f.Steps = append(f.Steps, step)
	}
	return f, nil
}

func isEphemeral(text string) bool {
	for _, m := range configRE.FindAllStringSubmatch(text, -1) {
		if strings.Contains(m[1], "ephemeral") {
			return true
		}
	}
	return false
}

// CodeOutput says whether a code step is the flow's output (a model not
// marked ephemeral, named without the flow's prefix).
func (f *Flow) CodeOutput(s *Step) bool { return s.Kind == Code && s.Table != "" }

// DAG is the Airflow DAG that builds the flow on its schedule.
func DAG(project, name, schedule, owner, dbtPath string) string {
	sched := "None"
	if schedule != "" {
		sched = fmt.Sprintf("%q", schedule)
	}
	return fmt.Sprintf(`"""The flow %s, built on the platform's canvas by %s: its dbt models are
%s/models/flows/%s, built by the platform's helper (storscale.dbt) in Trino, as
the project's pipelines, with their checks.
"""
# An Airflow DAG (Airflow reads only files that say so: "airflow" and "dag").
import pendulum

from airflow.sdk import dag
from storscale import dbt


@dag(dag_id="flow_%s", schedule=%s, start_date=pendulum.datetime(2026, 1, 1, tz="UTC"), catchup=False,
     max_active_runs=1, tags=["flow"])  # one build of the table at a time
def flow_%s():
    dbt.build(project=%q, path=%q, select="flows.%s")


flow_%s()
`, name, owner, dbtPath, name, name, sched, name, project, dbtPath, name, name)
}
