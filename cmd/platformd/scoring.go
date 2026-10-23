package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A project's scorings (use case 15.1, R15.3): each run (the DAG file
// storscale_scoring.py, as the project's pipelines) asks here for its model
// version's drift reference (the first run's profile of its inputs, which it
// makes when there's none), and records its rows, its scores' mean and each
// input column's drift (the population stability index against the
// reference). Kept in the platform's bucket, scoring/<project>/<name>/; the
// latest run's drift is a project alert past its threshold, and a metric
// Prometheus scrapes, so Grafana draws it.

const (
	scoringRunsKept = 60
	defaultDrift    = 0.2
)

var modelVersionRE = regexp.MustCompile(`^[0-9]{1,10}$`)

type scoringRun struct {
	Version   string             `json:"version"`
	Rows      int                `json:"rows"`
	Drift     map[string]float64 `json:"drift"`
	ScoreMean *float64           `json:"scoreMean,omitempty"`
	At        string             `json:"at"`
	Run       string             `json:"run,omitempty"` // the DAG run's id
}

// latestScoring is each scoring's latest run as recorded here, for the metrics (project/name -> scoringRun).
var latestScoring sync.Map

func scoringKey(projectName, name string) string { return "scoring/" + projectName + "/" + name + "/" }

// scoringOf is the request's project and scoring, and the caller's role in it (the project's pipelines' too).
func (s *server) scoringOf(w http.ResponseWriter, r *http.Request) (*project.Project, *project.Scoring, string, bool) {
	who, _, ok := s.person(w, r)
	if !ok {
		return nil, nil, "", false
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here (no STORE_URL)")
		return nil, nil, "", false
	}
	name := r.PathValue("name")
	role := s.mlflowRole(r, name, who)
	if role == "" {
		s.fail(w, r, http.StatusForbidden, "not a member of the project")
		return nil, nil, "", false
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, nil, "", false
	}
	sc := p.ScoringOf(r.PathValue("scoring"))
	if sc == nil {
		s.fail(w, r, http.StatusNotFound, "the project has no scoring "+r.PathValue("scoring"))
		return nil, nil, "", false
	}
	if role == "pipelines" || s.memberCan(r, name, who, project.PermModels) {
		role = "writer" // its pipelines, or someone who tracks the project's models
	}
	return p, sc, role, true
}

func scoringWriter(role string) bool { return role == "writer" }

// handleScoringReference is a model version's drift reference (GET; null
// when there's none yet), or the first run's, kept once a version (PUT: the
// project's pipelines or editors).
func (s *server) handleScoringReference(w http.ResponseWriter, r *http.Request) {
	p, sc, role, ok := s.scoringOf(w, r)
	if !ok {
		return
	}
	version, body := r.URL.Query().Get("version"), []byte(nil)
	if r.Method == http.MethodPut {
		if !scoringWriter(role) {
			s.fail(w, r, http.StatusForbidden, "the project's pipelines (and its editors) make a version's reference")
			return
		}
		body, _ = io.ReadAll(io.LimitReader(r.Body, 8<<20))
		var ref struct {
			Version json.RawMessage `json:"version"`
		}
		if json.Unmarshal(body, &ref) != nil {
			s.fail(w, r, http.StatusBadRequest, "a reference, as JSON, with its version")
			return
		}
		version = strings.Trim(string(ref.Version), `"`)
	}
	if !modelVersionRE.MatchString(version) {
		s.fail(w, r, http.StatusBadRequest, "version: the model version's number")
		return
	}
	key := scoringKey(p.Metadata.Name, sc.Name) + "reference-" + version + ".json"
	have, err := s.files.GetFile(r.Context(), key)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Content-Type", "application/json")
		if have == nil {
			have = []byte("null")
		}
		w.Write(have)
		return
	}
	if have != nil {
		s.fail(w, r, http.StatusConflict, "this version has its reference: its first run's")
		return
	}
	if err := s.files.PutFile(r.Context(), key, body, "application/json"); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kept": true, "version": version})
}

// handleScoringRun records a run (the project's pipelines, or an editor).
func (s *server) handleScoringRun(w http.ResponseWriter, r *http.Request) {
	p, sc, role, ok := s.scoringOf(w, r)
	if !ok {
		return
	}
	if !scoringWriter(role) {
		s.fail(w, r, http.StatusForbidden, "the project's pipelines record a scoring's runs")
		return
	}
	var run scoringRun
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&run); err != nil || !modelVersionRE.MatchString(run.Version) || run.At == "" {
		s.fail(w, r, http.StatusBadRequest, "a run, as JSON: its version, rows, drift by column and when")
		return
	}
	prefix := scoringKey(p.Metadata.Name, sc.Name) + "runs/"
	data, _ := json.Marshal(run)
	stamp := strings.NewReplacer(" ", "T", ":", "").Replace(run.At)
	if err := s.files.PutFile(r.Context(), prefix+stamp+".json", data, "application/json"); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if keys, err := s.files.ListFiles(r.Context(), prefix); err == nil && len(keys) > scoringRunsKept {
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-scoringRunsKept] {
			s.files.DeleteFile(r.Context(), k)
		}
	}
	latestScoring.Store(p.Metadata.Name+"/"+sc.Name, run)
	worst, col := worstDrift(run.Drift)
	s.log.Info("scoring run", "project", p.Metadata.Name, "scoring", sc.Name, "version", run.Version, "rows", run.Rows, "drift", worst, "column", col)
	writeJSON(w, http.StatusOK, map[string]any{"kept": true, "drift": worst, "column": col, "past": worst > threshold(sc)})
}

func threshold(sc *project.Scoring) float64 {
	if sc.Drift > 0 {
		return sc.Drift
	}
	return defaultDrift
}

// worstDrift is the column that drifted most, and by how much.
func worstDrift(d map[string]float64) (float64, string) {
	worst, col := 0.0, ""
	for c, v := range d {
		if v > worst || (v == worst && c < col) {
			worst, col = v, c
		}
	}
	return worst, col
}

// scoringRuns are a scoring's runs, newest first.
func (s *server) scoringRuns(ctx context.Context, projectName, name string, limit int) []scoringRun {
	out := []scoringRun{}
	if s.files == nil {
		return out
	}
	keys, err := s.files.ListFiles(ctx, scoringKey(projectName, name)+"runs/")
	if err != nil {
		return out
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, k := range keys[:min(len(keys), limit)] {
		var run scoringRun
		if b, err := s.files.GetFile(ctx, k); err == nil && json.Unmarshal(b, &run) == nil {
			out = append(out, run)
		}
	}
	return out
}

// handleScorings are the project's scorings with their recent runs: its members.
func (s *server) handleScorings(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	type view struct {
		project.Scoring
		Threshold float64      `json:"threshold"`
		DAG       string       `json:"dag"`
		Runs      []scoringRun `json:"runs"`
	}
	out := []view{}
	for _, sc := range p.Spec.Scoring {
		out = append(out, view{Scoring: sc, Threshold: threshold(&sc), DAG: sc.DAG(p.Metadata.Name), Runs: s.scoringRuns(r.Context(), p.Metadata.Name, sc.Name, 10)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scoring": out})
}

// scoringIssues alert a project whose scoring's latest run drifted past its threshold.
func (s *server) scoringIssues(ctx context.Context, p *project.Project, link string) []issue {
	var out []issue
	for _, sc := range p.Spec.Scoring {
		runs := s.scoringRuns(ctx, p.Metadata.Name, sc.Name, 1)
		if len(runs) == 0 {
			continue
		}
		latestScoring.Store(p.Metadata.Name+"/"+sc.Name, runs[0])
		if worst, col := worstDrift(runs[0].Drift); worst > threshold(&sc) {
			out = append(out, issue{Key: "drift/" + sc.Name + "/" + runs[0].Version, Kind: "drift", Link: link + "/models",
				Title:  fmt.Sprintf("%s: its inputs have drifted (%s, PSI %.2f, past %.2f)", sc.Name, col, worst, threshold(&sc)),
				Detail: fmt.Sprintf("version %s of %s, the run of %s, against the version's first run; retrain, or check the data", runs[0].Version, sc.Model, runs[0].At)})
		}
	}
	return out
}

// scoringMetrics are the scorings' latest runs, for Prometheus: each column's drift, the rows, the scores' mean.
func scoringMetrics() string {
	var b strings.Builder
	type row struct {
		key string
		run scoringRun
	}
	var rows []row
	latestScoring.Range(func(k, v any) bool {
		rows = append(rows, row{k.(string), v.(scoringRun)})
		return true
	})
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	b.WriteString("# HELP storscale_scoring_drift A scoring's latest run's drift by input column (PSI against the version's first run).\n# TYPE storscale_scoring_drift gauge\n")
	for _, x := range rows {
		p, n, _ := strings.Cut(x.key, "/")
		cols := make([]string, 0, len(x.run.Drift))
		for c := range x.run.Drift {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		for _, c := range cols {
			fmt.Fprintf(&b, "storscale_scoring_drift{project=%q,scoring=%q,version=%q,column=%q} %g\n", p, n, x.run.Version, c, x.run.Drift[c])
		}
	}
	b.WriteString("# HELP storscale_scoring_rows A scoring's latest run's rows scored.\n# TYPE storscale_scoring_rows gauge\n")
	for _, x := range rows {
		p, n, _ := strings.Cut(x.key, "/")
		fmt.Fprintf(&b, "storscale_scoring_rows{project=%q,scoring=%q} %d\n", p, n, x.run.Rows)
	}
	return b.String()
}
