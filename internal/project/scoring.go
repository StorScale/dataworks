package project

import (
	"fmt"
	"regexp"
	"strings"
)

// A Scoring is a model put to use on a schedule (use case 15.1, R15.3): the
// approved version of one of the project's registered models (its alias
// "approved", which only another editor sets) scores a table's rows, as the
// project's pipelines, into a table of the project's; each run's inputs are
// compared with a reference (the first run's of that version) and the
// project alerted when they drift past the threshold.
type Scoring struct {
	Name     string   `json:"name"`
	Model    string   `json:"model"`             // a registered model of the project's MLflow workspace
	Table    string   `json:"table"`             // its input: one of the project's tables
	ID       string   `json:"id"`                // the column that names each row, carried to the output
	Columns  []string `json:"columns,omitempty"` // the model's inputs; none: every column but the id
	Output   string   `json:"output"`            // the table the scores are written to (made afresh each run)
	Schedule string   `json:"schedule"`          // cron, UTC
	Drift    float64  `json:"drift,omitempty"`   // the PSI past which a column has drifted (default 0.2)
	MaxRows  int      `json:"maxRows,omitempty"` // read at most (default 100,000)
}

var scoringNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

// DAG is the Airflow DAG that runs the scoring, in a project.
func (s Scoring) DAG(project string) string {
	return "scoring_" + strings.ReplaceAll(project, "-", "_") + "_" + strings.ReplaceAll(s.Name, "-", "_")
}

// ScoringOf is the project's scoring of that name, or nil.
func (p *Project) ScoringOf(name string) *Scoring {
	for i := range p.Spec.Scoring {
		if p.Spec.Scoring[i].Name == name {
			return &p.Spec.Scoring[i]
		}
	}
	return nil
}

func (p *Project) checkScoring(bad func(format string, a ...any)) {
	seen, outputs := map[string]bool{}, map[string]bool{}
	for i, s := range p.Spec.Scoring {
		at := fmt.Sprintf("scoring %d", i+1)
		if scoringNameRE.MatchString(s.Name) {
			at = "scoring: " + s.Name
		}
		switch {
		case !scoringNameRE.MatchString(s.Name):
			bad("%s: a name is lowercase letters, digits and dashes, starting with a letter: %q isn't", at, s.Name)
		case seen[s.Name]:
			bad("scoring: two named %s", s.Name)
		}
		seen[s.Name] = true
		if s.Model == "" || len(s.Model) > 200 {
			bad("%s: the registered model it uses, by its name (its approved version is used)", at)
		}
		for _, f := range []struct{ what, v string }{{"table", s.Table}, {"id", s.ID}, {"output", s.Output}} {
			if !identifierRE.MatchString(f.v) {
				bad("%s: %s %q: a table or column of the project's, by its name", at, f.what, f.v)
			}
		}
		for _, c := range s.Columns {
			if !identifierRE.MatchString(c) || c == s.ID {
				bad("%s: column %q: one of the table's, by its name, not its id", at, c)
			}
		}
		if s.Output == s.Table {
			bad("%s: the output is a table of its own, not its input", at)
		}
		if outputs[s.Output] {
			bad("scoring: two write %s", s.Output)
		}
		outputs[s.Output] = true
		if !cronRE.MatchString(s.Schedule) {
			bad("%s: schedule %q: five cron fields, as 0 4 * * *", at, s.Schedule)
		}
		if s.Drift < 0 || s.Drift > 10 {
			bad("%s: drift %v: a PSI, as 0.2 (more is a bigger shift)", at, s.Drift)
		}
		if s.MaxRows < 0 || s.MaxRows > 5_000_000 {
			bad("%s: maxRows %d: up to five million", at, s.MaxRows)
		}
	}
	if len(p.Spec.Scoring) > 0 && (p.Spec.Tables == nil || p.Spec.Pipelines == nil) {
		bad("scoring: a model scores the project's tables as its pipelines: the project needs tables and pipelines")
	}
}
