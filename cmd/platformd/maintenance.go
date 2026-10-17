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

	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// Table maintenance: each night, and when a project's editor asks, every
// table of every project is kept in shape, as the platform's maintenance
// account in Trino: its small files compacted (optimize), its snapshots
// older than the project's keepSnapshots expired with the files only they
// used (expire_snapshots; tagged and branched ones are kept), and files no
// snapshot uses removed (remove_orphan_files). One project at a time.

// A tableRun is what maintenance last did to a table.
type tableRun struct {
	Table   string        `json:"table"`
	Started time.Time     `json:"started"`
	Ended   time.Time     `json:"ended"`
	Done    []string      `json:"done"`            // the steps that ran: optimize, expire_snapshots, remove_orphan_files
	Error   string        `json:"error,omitempty"` // the step that failed, and why
	Before  catalog.Stats `json:"before"`
	After   catalog.Stats `json:"after"`
	Keep    string        `json:"keep"` // the retention it ran with
}

type maintainer struct {
	trino   *trino.Client
	iceberg *catalog.Iceberg
	at      int    // minutes after midnight, UTC
	atText  string // as configured (02:00)
	keep    string // the install's default keepSnapshots
	min     string // Trino's least retention (its catalog's expire-snapshots.min-retention)

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
	keep, min := envOr("MAINTENANCE_KEEP", "7d"), envOr("MAINTENANCE_MIN_RETENTION", "7d")
	for k, v := range map[string]string{"MAINTENANCE_KEEP": keep, "MAINTENANCE_MIN_RETENTION": min} {
		if _, ok := project.Within(v); !ok {
			return fmt.Errorf("%s %q: as 7d or 12h", k, v)
		}
	}
	tc, err := trino.New(envOr("TRINO_URL", "https://trino:8443"), "maintenance", password, os.Getenv("TRINO_CA"))
	if err != nil {
		return fmt.Errorf("trino: %w", err)
	}
	m := &maintainer{trino: tc, at: at.Hour()*60 + at.Minute(), atText: at.Format("15:04"), keep: keep, min: min,
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
	s.log.Info("table maintenance", "at", m.atText+" UTC", "keepSnapshots", keep, "minRetention", min)
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

// retention is how long a project's snapshots are kept: its own, or the
// install's; never less than Trino's least (it'd refuse).
func (m *maintainer) retention(mt *project.Maintenance) (string, time.Duration) {
	keep := m.keep
	if mt != nil && mt.KeepSnapshots != "" {
		keep = mt.KeepSnapshots
	}
	d, _ := project.Within(keep)
	if least, _ := project.Within(m.min); d < least {
		return m.min, least
	}
	return keep, d
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
	keep, d := m.retention(t.Maintenance)
	threshold := fmt.Sprintf("%ds", int(d.Seconds()))
	runs := map[string]tableRun{}
	for _, name := range tables {
		run := tableRun{Table: name, Started: time.Now().UTC(), Keep: keep, Done: []string{}}
		run.Before, _ = m.iceberg.Stats(ctx, t.Namespace, name)
		table := strings.Join([]string{trino.Quote(t.Catalog), trino.Quote(t.Namespace), trino.Quote(name)}, ".")
		steps := []struct{ name, sql string }{
			{"optimize", "ALTER TABLE " + table + " EXECUTE optimize"},
			{"expire_snapshots", "ALTER TABLE " + table + " EXECUTE expire_snapshots(retention_threshold => '" + threshold + "')"},
			{"remove_orphan_files", "ALTER TABLE " + table + " EXECUTE remove_orphan_files(retention_threshold => '" + threshold + "')"},
		}
		if !t.Maintenance.Compacts() {
			steps = steps[1:]
		}
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
				"files", fmt.Sprintf("%d -> %d", run.Before.DataFiles, run.After.DataFiles),
				"snapshots", fmt.Sprintf("%d -> %d", run.Before.Snapshots, run.After.Snapshots))
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
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Platform-Request"})
			return
		}
		if v.Role != project.Editor && !s.isAdmin(sess.User) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "the project's editors ask for its maintenance"})
			return
		}
		if t := v.Spec.Spec.Tables; t == nil || (t.Maintenance != nil && t.Maintenance.Paused) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the project's tables have no maintenance (none, or paused)"})
			return
		}
		if m.enqueue(v.Name) {
			s.log.Info("maintenance asked for", "project", v.Name, "by", sess.User.Username)
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"state": m.state(v.Name)})
		return
	}
	t := v.Spec.Spec.Tables
	out := map[string]any{"enabled": true, "at": m.atText + " UTC", "minRetention": m.min, "state": m.state(v.Name), "tables": []any{}}
	if t == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	keep, _ := m.retention(t.Maintenance)
	out["keepSnapshots"], out["compact"] = keep, t.Maintenance.Compacts()
	out["paused"] = t.Maintenance != nil && t.Maintenance.Paused
	names, err := m.iceberg.Tables(r.Context(), t.Namespace)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
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
