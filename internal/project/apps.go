package project

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// An App is a small page of the project's (use case 14.1, R14.1, R14.6),
// declared, not coded: a metric (or a few) by a dimension, a table's
// columns, or questions answered from a knowledge bank; with filters the
// viewer picks. Its data is fetched as whoever opens it, so it shows each
// person what their rules let them see: one app, no copies per audience.
type App struct {
	Name        string      `json:"name"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Kind        string      `json:"kind"`              // metric, table or qa
	Metrics     []string    `json:"metrics,omitempty"` // metric: the project's metrics (or the company's it builds on)
	By          string      `json:"by,omitempty"`      // metric: the dimension its rows (or bars, or line) are by
	Table       string      `json:"table,omitempty"`   // table: one of the project's tables
	Columns     []string    `json:"columns,omitempty"` // table: its columns shown (none: all)
	Show        string      `json:"show,omitempty"`    // metric: table, bar, line or number; table: table
	Filters     []AppFilter `json:"filters,omitempty"`
	Bank        string      `json:"bank,omitempty"`  // qa: the knowledge bank answers come from
	Limit       int         `json:"limit,omitempty"` // rows shown, at most (default 500)
}

// An AppFilter is a choice the viewer makes: a dimension (metric apps) or a
// column (table apps) equal to a value, from the values listed or typed.
type AppFilter struct {
	Field  string   `json:"field"`
	Label  string   `json:"label,omitempty"`
	Values []string `json:"values,omitempty"`
}

// AppKinds and AppShows are what an app may be, and how a metric app shows.
var (
	AppKinds   = []string{"metric", "table", "qa"}
	AppShows   = []string{"table", "bar", "line", "number"}
	appNameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
	appFieldRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,120}$`)
)

// AppOf is the project's app of that name, or nil.
func (p *Project) AppOf(name string) *App {
	for i := range p.Spec.Apps {
		if p.Spec.Apps[i].Name == name {
			return &p.Spec.Apps[i]
		}
	}
	return nil
}

func (p *Project) checkApps(bad func(format string, a ...any)) {
	seen := map[string]bool{}
	for i, a := range p.Spec.Apps {
		at := fmt.Sprintf("apps %d", i+1)
		if appNameRE.MatchString(a.Name) {
			at = "apps: " + a.Name
		}
		switch {
		case !appNameRE.MatchString(a.Name):
			bad("%s: an app's name is lowercase letters, digits and dashes, starting with a letter: %q isn't", at, a.Name)
		case seen[a.Name]:
			bad("apps: two named %s", a.Name)
		}
		seen[a.Name] = true
		if a.Limit < 0 || a.Limit > 5000 {
			bad("%s: limit %d: up to 5000 rows", at, a.Limit)
		}
		for _, f := range a.Filters {
			if !appFieldRE.MatchString(f.Field) {
				bad("%s: filter %q: a dimension or a column, by its name", at, f.Field)
			}
			for _, v := range f.Values {
				if len(v) > 200 {
					bad("%s: filter %s: a value is up to 200 characters", at, f.Field)
				}
			}
		}
		switch a.Kind {
		case "metric":
			if p.Spec.Tables == nil {
				bad("%s: a metric app needs the project's tables, where its metrics are", at)
			}
			if len(a.Metrics) == 0 {
				bad("%s: a metric app shows at least one metric", at)
			}
			for _, m := range a.Metrics {
				if !appFieldRE.MatchString(m) {
					bad("%s: metric %q: by its name", at, m)
				}
			}
			if a.By != "" && !appFieldRE.MatchString(a.By) {
				bad("%s: by %q: a dimension, by its name (order__region, metric_time__month)", at, a.By)
			}
			switch {
			case !slices.Contains(AppShows, a.Show):
				bad("%s: show %q: %s", at, a.Show, strings.Join(AppShows, ", "))
			case a.Show == "number" && (len(a.Metrics) != 1 || a.By != ""):
				bad("%s: a number is one metric, by nothing", at)
			case (a.Show == "bar" || a.Show == "line") && a.By == "":
				bad("%s: a %s chart is by a dimension (by)", at, a.Show)
			}
		case "table":
			if p.Spec.Tables == nil {
				bad("%s: a table app needs the project's tables", at)
			}
			if !identifierRE.MatchString(a.Table) {
				bad("%s: table %q: one of the project's tables, by its name", at, a.Table)
			}
			for _, c := range a.Columns {
				if !appFieldRE.MatchString(c) {
					bad("%s: column %q: by its name", at, c)
				}
			}
			if a.Show != "" && a.Show != "table" {
				bad("%s: a table app shows a table", at)
			}
		case "qa":
			if !slices.ContainsFunc(p.Spec.Knowledge, func(k KnowledgeBank) bool { return k.Name == a.Bank }) {
				bad("%s: a question app answers from one of the project's knowledge banks (bank): %q isn't one", at, a.Bank)
			}
			if p.Spec.LLM == nil || p.Spec.LLM.Budget <= 0 {
				bad("%s: a question app asks a model on the project's gateway key: give the project an LLM budget (llm.budget)", at)
			}
		default:
			bad("%s: kind %q: %s", at, a.Kind, strings.Join(AppKinds, ", "))
		}
	}
}
