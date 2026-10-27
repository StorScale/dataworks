package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// Table maintenance: each night, and when a project's editor asks, every
// table of every project is kept in shape, as the platform's maintenance
// account in Trino: its small files compacted (optimize). One project at a
// time. Iceberg's own expire_snapshots and remove_orphan_files don't apply:
// Nessie keeps a table's history as its commits, shared by every project
// and kept by its tags and branches, and turns them off (gc.enabled=false);
// Nessie's GC collects what no reference needs, across the install.

// A tableRun is what maintenance last did to a table.
type tableRun struct {
	Table   string        `json:"table"`
	Started time.Time     `json:"started"`
	Ended   time.Time     `json:"ended"`
	Done    []string      `json:"done"`            // the steps that ran: optimize
	Error   string        `json:"error,omitempty"` // the step that failed, and why
	Before  catalog.Stats `json:"before"`
	After   catalog.Stats `json:"after"`
}

type maintainer struct {
	trino   *trino.Client
	iceberg *catalog.Iceberg
	at      int    // minutes after midnight, UTC
	atText  string // as configured (02:00)
	history string // how far back the tables' history goes (Nessie's GC: P7D)

	mu      sync.Mutex
	runs    map[string]map[string]tableRun // project -> table -> the last run
	queued  []string                       // projects waiting, in order
	running string                         // the project being kept, if any
	nightly string                         // the day (UTC) of the last nightly round
	wake    chan struct{}
}

func (s *server) startMaintenance(ctx context.Context) error {
	password := os.Getenv("MAINTENANCE_TRINO_PASSWORD")
	if password == "" || s.store == nil {
		return nil // no maintenance account: no maintenance
	}
	at, err := time.Parse("15:04", envOr("MAINTENANCE_AT", "02:00"))
	if err != nil {
		return fmt.Errorf("MAINTENANCE_AT: as 02:00 (UTC): %w", err)
	}
	tc, err := trino.New(envOr("TRINO_URL", "https://trino:8443"), "maintenance", password, os.Getenv("TRINO_CA"))
	if err != nil {
		return fmt.Errorf("trino: %w", err)
	}
	m := &maintainer{trino: tc, at: at.Hour()*60 + at.Minute(), atText: at.Format("15:04"), history: os.Getenv("MAINTENANCE_KEEP_HISTORY"),
		iceberg: &catalog.Iceberg{HTTP: &http.Client{Timeout: 30 * time.Second},
			Base: strings.TrimRight(envOr("NESSIE_URL", "http://nessie:19120"), "/") + "/iceberg", Warehouse: envOr("NESSIE_WAREHOUSE", "warehouse")},
		runs: map[string]map[string]tableRun{}, wake: make(chan struct{}, 1)}
	// Started after tonight's time: the next round is tomorrow's.
	if now := time.Now().UTC(); now.Hour()*60+now.Minute() >= m.at {
		m.nightly = now.Format("2006-01-02")
	}
	s.maintainer = m
	go m.schedule(ctx, s)
	go m.work(ctx, s)
	s.log.Info("table maintenance", "at", m.atText+" UTC")
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// schedule queues every project once a night.
func (m *maintainer) schedule(ctx context.Context, s *server) {
	for {
		now := time.Now().UTC()
		day := now.Format("2006-01-02")
		m.mu.Lock()
		due := now.Hour()*60+now.Minute() >= m.at && m.nightly != day
		if due {
			m.nightly = day
		}
		m.mu.Unlock()
		if due {
			s.expireTags(ctx)
			s.expireDeliveryTags(ctx)
			if projects, err := s.store.Projects(ctx); err == nil {
				for _, p := range projects {
					m.enqueue(p.Metadata.Name)
				}
			} else {
				s.log.Error("maintenance: the projects", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// enqueue asks for a project's maintenance; false if it's queued or running already.
func (m *maintainer) enqueue(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == name {
		return false
	}
	for _, q := range m.queued {
		if q == name {
			return false
		}
	}
	m.queued = append(m.queued, name)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return true
}

func (m *maintainer) state(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == name {
		return "running"
	}
	for _, q := range m.queued {
		if q == name {
			return "queued"
		}
	}
	return "idle"
}

// work keeps the queued projects, one at a time.
func (m *maintainer) work(ctx context.Context, s *server) {
	for {
		m.mu.Lock()
		if len(m.queued) == 0 {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			}
			continue
		}
		name := m.queued[0]
		m.queued, m.running = m.queued[1:], name
		m.mu.Unlock()

		p, err := s.store.Project(ctx, name)
		if err == nil && p != nil {
			m.keepProject(ctx, s, p)
		}
		m.mu.Lock()
		m.running = ""
		m.mu.Unlock()
	}
}

func (m *maintainer) keepProject(ctx context.Context, s *server, p *project.Project) {
	t := p.Spec.Tables
	if t == nil || (t.Maintenance != nil && t.Maintenance.Paused) {
		return
	}
	tables, err := m.iceberg.Tables(ctx, t.Namespace)
	if err != nil {
		s.log.Error("maintenance: the tables", "project", p.Metadata.Name, "error", err)
		return
	}
	runs := map[string]tableRun{}
	for _, name := range tables {
		if t.Adopted(name) { // the platform it came from writes it, and keeps it in shape
			continue
		}
		run := tableRun{Table: name, Started: time.Now().UTC(), Done: []string{}}
		run.Before, _ = m.iceberg.Stats(ctx, t.Namespace, name)
		table := strings.Join([]string{trino.Quote(t.Catalog), trino.Quote(t.Namespace), trino.Quote(name)}, ".")
		steps := []struct{ name, sql string }{}
		for _, rt := range t.Retention { // rows past their keep go first, then the files are compacted
			if rt.Table == name {
				steps = append(steps, struct{ name, sql string }{"retention",
					fmt.Sprintf("DELETE FROM %s WHERE %s < CAST(current_timestamp - INTERVAL '%d' DAY AS %s)", table, trino.Quote(rt.Column), project.KeepDays(rt.Keep), "timestamp(6)")})
			}
		}
		steps = append(steps, struct{ name, sql string }{"optimize", "ALTER TABLE " + table + " EXECUTE optimize"})
		for _, step := range steps {
			sctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			_, err := m.trino.Exec(sctx, step.sql)
			cancel()
			if err != nil {
				run.Error = step.name + ": " + err.Error()
				break
			}
			run.Done = append(run.Done, step.name)
		}
		run.After, _ = m.iceberg.Stats(ctx, t.Namespace, name)
		run.Ended = time.Now().UTC()
		runs[name] = run
		if run.Error != "" {
			s.log.Error("maintenance", "project", p.Metadata.Name, "table", name, "error", run.Error)
		} else {
			s.log.Info("maintenance", "project", p.Metadata.Name, "table", name, "took", run.Ended.Sub(run.Started).Round(time.Second).String(),
				"files", fmt.Sprintf("%d -> %d", run.Before.DataFiles, run.After.DataFiles))
		}
		m.mu.Lock()
		if m.runs[p.Metadata.Name] == nil {
			m.runs[p.Metadata.Name] = map[string]tableRun{}
		}
		m.runs[p.Metadata.Name][name] = run
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.runs[p.Metadata.Name] = runs // tables since dropped are forgotten
	m.mu.Unlock()
}

// failures are a project's tables whose last maintenance failed, for its alerts.
func (m *maintainer) failures(name string) []tableRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tableRun
	for _, r := range m.runs[name] {
		if r.Error != "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}

// metrics are the last runs, for Prometheus.
func (m *maintainer) metrics() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP platform_table_maintenance_last_run_seconds When a table's maintenance last ended (Unix time).\n")
	b.WriteString("# TYPE platform_table_maintenance_last_run_seconds gauge\n")
	var lines []string
	for p, runs := range m.runs {
		for t, r := range runs {
			failed := 0
			if r.Error != "" {
				failed = 1
			}
			labels := fmt.Sprintf(`{project=%q,table=%q}`, p, t)
			lines = append(lines, fmt.Sprintf("platform_table_maintenance_last_run_seconds%s %d\nplatform_table_maintenance_failed%s %d\nplatform_table_data_files%s %d\n",
				labels, r.Ended.Unix(), labels, failed, labels, r.After.DataFiles))
		}
	}
	sort.Strings(lines)
	b.WriteString("# HELP platform_table_maintenance_failed 1 if a table's last maintenance failed.\n# TYPE platform_table_maintenance_failed gauge\n")
	b.WriteString("# HELP platform_table_data_files A table's data files after its last maintenance.\n# TYPE platform_table_data_files gauge\n")
	for _, l := range lines {
		b.WriteString(l)
	}
	return b.String()
}

// handleMaintenance is a project's tables as maintenance keeps them, for its
// people; its editors (and administrators) ask for it now.
func (s *server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	m := s.maintainer
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "tables": []any{}})
		return
	}
	if r.Method == "POST" {
		if r.Header.Get("X-Platform-Request") != "1" {
			s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
			return
		}
		if !v.can(project.PermRunPipelines) {
			s.fail(w, r, http.StatusForbidden, "the project's editors ask for its maintenance")
			return
		}
		if t := v.Spec.Spec.Tables; t == nil || (t.Maintenance != nil && t.Maintenance.Paused) {
			s.fail(w, r, http.StatusConflict, "the project's tables have no maintenance (none, or paused)")
			return
		}
		s.expireTags(r.Context()) // tags past their keepUntil, as the nightly round does
		if m.enqueue(v.Name) {
			s.log.Info("maintenance asked for", "project", v.Name, "by", sess.User.Username)
			s.record(r, sess.User.Username, "maintenance.asked", "project:"+v.Name, "")
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"state": m.state(v.Name)})
		return
	}
	t := v.Spec.Spec.Tables
	out := map[string]any{"enabled": true, "at": m.atText + " UTC", "state": m.state(v.Name), "tables": []any{}, "keepHistory": m.history}
	if t == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["paused"] = t.Maintenance != nil && t.Maintenance.Paused
	names, err := m.iceberg.Tables(r.Context(), t.Namespace)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	m.mu.Lock()
	last := m.runs[v.Name]
	m.mu.Unlock()
	var tables []any
	for _, name := range names {
		row := map[string]any{"name": name}
		if st, err := m.iceberg.Stats(r.Context(), t.Namespace, name); err == nil {
			row["now"] = st
		}
		if run, ok := last[name]; ok {
			row["lastRun"] = run
		}
		tables = append(tables, row)
	}
	if tables != nil {
		out["tables"] = tables
	}
	writeJSON(w, http.StatusOK, out)
}
