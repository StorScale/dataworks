package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/audit"
	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/project"
)

// The data as it was (use case 6.10): Nessie keeps the catalog's history as
// its commits on main, and a table is read at one through its name,
// "orders#<commit>" (Trino passes it through to Nessie; Ranger's policies
// name it too, so masks and filters hold). A project's tags keep moments for
// as long as it says; a table's history is its commits.

type history struct {
	nessie  *catalog.Nessie
	iceberg *catalog.Iceberg
	keep    time.Duration // how far back the data goes (Nessie's GC); 0: unknown
}

func (s *server) startHistory() {
	base := strings.TrimRight(envOr("NESSIE_URL", "http://nessie:19120"), "/")
	h := &history{
		nessie:  &catalog.Nessie{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: base + "/api/v2"},
		iceberg: &catalog.Iceberg{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: base + "/iceberg", Warehouse: envOr("NESSIE_WAREHOUSE", "warehouse")},
	}
	h.keep, _ = isoDuration(envOr("MAINTENANCE_KEEP_HISTORY", ""))
	s.history = h
}

var isoRE = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// isoDuration is an ISO 8601 duration as Nessie's GC takes it (P7D, PT2M).
func isoDuration(s string) (time.Duration, bool) {
	m := isoRE.FindStringSubmatch(s)
	if m == nil || s == "P" || s == "PT" {
		return 0, false
	}
	n := func(i int) time.Duration { v, _ := strconv.Atoi(m[i]); return time.Duration(v) }
	return n(1)*24*time.Hour + n(2)*time.Hour + n(3)*time.Minute + n(4)*time.Second, true
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// A moment is a commit of the catalog, and how it was named.
type moment struct {
	Commit string    `json:"commit"`
	Time   time.Time `json:"time,omitempty"`
	Label  string    `json:"label"` // now, the tag close-2026-09, 2026-09-30 18:00 UTC, a commit
}

// resolve turns now, one of the project's tags, a time or a commit into the
// catalog's commit on main.
func (h *history) resolve(ctx context.Context, p *project.Project, at string) (*moment, error) {
	at = strings.TrimSpace(at)
	if at == "" || at == "now" {
		ref, err := h.nessie.Ref(ctx, "main")
		if err != nil || ref == nil {
			return nil, fmt.Errorf("the catalog's main: %v", err)
		}
		return &moment{Commit: ref.Hash, Time: time.Now().UTC(), Label: "now"}, nil
	}
	if t := p.Spec.Tables; t != nil {
		for _, tg := range t.Tags {
			if tg.Name == at {
				return &moment{Commit: tg.Commit, Label: "the tag " + tg.Name}, nil
			}
		}
	}
	if hashRE.MatchString(at) {
		return &moment{Commit: at, Label: "the commit " + at[:12]}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		t, err := time.Parse(layout, at)
		if err != nil {
			continue
		}
		if layout == "2006-01-02" {
			t = t.Add(24*time.Hour - time.Second) // the day: as it was at its end
		}
		c, err := h.nessie.At(ctx, t, 20000)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("the catalog's history kept starts after %s", t.UTC().Format("2006-01-02 15:04 MST"))
		}
		return &moment{Commit: c.Hash, Time: c.Time, Label: t.UTC().Format("2006-01-02 15:04 MST")}, nil
	}
	return nil, fmt.Errorf("%q: now, one of the project's tags, a time (2026-09-30 or 2026-09-30T18:00:00Z) or a commit", at)
}

// keepRE is how long a tag is kept: days or years.
var keepRE = regexp.MustCompile(`^([1-9][0-9]{0,4})(d|y)$`)

func keepUntil(keep string, from time.Time) (string, bool) {
	m := keepRE.FindStringSubmatch(keep)
	if m == nil {
		return "", false
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "y" {
		return from.AddDate(n, 0, 0).Format("2006-01-02"), true
	}
	return from.AddDate(0, 0, n).Format("2006-01-02"), true
}

// handleAsOf is the commit a moment stands for, to read the tables at.
func (s *server) handleAsOf(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	m, err := s.history.resolve(r.Context(), v.Spec, r.URL.Query().Get("at"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type tagView struct {
	project.Tag
	Nessie  string `json:"nessie"`  // its name in Nessie
	InPlace bool   `json:"inPlace"` // Nessie has it at its commit (the operator made it)
}

// handleTags lists a project's tags (its members); its editors make and
// remove them.
func (s *server) handleTags(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	p := v.Spec
	if r.Method != "GET" {
		if r.Header.Get("X-Platform-Request") != "1" {
			s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
			return
		}
		if v.Role != project.Editor && !s.isAdmin(sess.User) {
			s.fail(w, r, http.StatusForbidden, "the project's editors make and remove its tags")
			return
		}
		if p.Spec.Tables == nil {
			s.fail(w, r, http.StatusConflict, "the project has no tables")
			return
		}
	}
	switch r.Method {
	case "POST":
		var req struct {
			Name, At, Keep, Note string
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			s.fail(w, r, http.StatusBadRequest, "want {name, at, keep, note}")
			return
		}
		until, ok := keepUntil(req.Keep, time.Now().UTC())
		if !ok {
			s.fail(w, r, http.StatusBadRequest, "keep: as 90d or 7y")
			return
		}
		m, err := s.history.resolve(r.Context(), p, req.At)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		tag := project.Tag{Name: req.Name, Commit: m.Commit, KeepUntil: until, Note: req.Note, By: sess.User.Username}
		next := *p
		tables := *p.Spec.Tables
		if slices.ContainsFunc(tables.Tags, func(t project.Tag) bool { return t.Name == req.Name }) {
			s.fail(w, r, http.StatusConflict, "the project has a tag "+req.Name+" already")
			return
		}
		tables.Tags = append(append([]project.Tag{}, tables.Tags...), tag)
		next.Spec.Tables = &tables
		if err := s.saveSpec(r.Context(), &next); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.record(r, sess.User.Username, "tag.made", "project:"+v.Name+"/tag:"+req.Name,
			fmt.Sprintf("at %s (%s), kept until %s; %s", m.Commit, m.Label, until, req.Note))
		writeJSON(w, http.StatusCreated, tag)
		return
	case "DELETE":
		name := r.PathValue("tag")
		next := *p
		tables := *p.Spec.Tables
		kept := slices.DeleteFunc(append([]project.Tag{}, tables.Tags...), func(t project.Tag) bool { return t.Name == name })
		if len(kept) == len(tables.Tags) {
			s.fail(w, r, http.StatusNotFound, "no tag "+name)
			return
		}
		tables.Tags = kept
		next.Spec.Tables = &tables
		if err := s.saveSpec(r.Context(), &next); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.record(r, sess.User.Username, "tag.removed", "project:"+v.Name+"/tag:"+name, "")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out := []tagView{}
	if t := p.Spec.Tables; t != nil {
		have := map[string]string{}
		if refs, err := s.history.nessie.Tags(r.Context(), v.Name+"."); err == nil {
			for _, ref := range refs {
				have[ref.Name] = ref.Hash
			}
		}
		for _, tg := range t.Tags {
			out = append(out, tagView{Tag: tg, Nessie: p.NessieTag(tg), InPlace: have[p.NessieTag(tg)] == tg.Commit})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out, "keepHistory": s.history.keep.String()})
}

// saveSpec saves a project as it is now, but for what's changed: re-checked.
func (s *server) saveSpec(ctx context.Context, p *project.Project) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	checked, err := project.Parse(data)
	if err != nil {
		return err
	}
	return s.store.PutProject(ctx, checked)
}

// A change is one of a table's commits, with what it did.
type change struct {
	catalog.Commit
	Stats *catalog.Stats `json:"stats,omitempty"` // the table at that commit (nil past the data kept)
	Tags  []string       `json:"tags,omitempty"`
	Who   string         `json:"who,omitempty"` // from the audit log: who wrote to the table then
	Kept  bool           `json:"kept"`          // its data is still there (within the history kept, or tagged)
	Query string         `json:"query"`         // the name to read it as it was then
}

// handleHistory is a table's changes, newest first.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	t := v.Spec.Spec.Tables
	table := r.URL.Query().Get("table")
	if t == nil || !identRE.MatchString(table) {
		s.fail(w, r, http.StatusBadRequest, "want ?table=<one of the project's tables>")
		return
	}
	commits, err := s.history.nessie.History(r.Context(), t.Namespace+"."+table, 50, 5000)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	tagsAt := map[string][]string{}
	for _, tg := range t.Tags {
		tagsAt[tg.Commit] = append(tagsAt[tg.Commit], tg.Name)
	}
	out := []change{}
	for i, c := range commits {
		ch := change{Commit: c, Tags: tagsAt[c.Hash], Query: fmt.Sprintf(`"%s#%s"`, table, c.Hash)}
		ch.Kept = i == 0 || len(ch.Tags) > 0 || s.history.keep == 0 || time.Since(c.Time) <= s.history.keep
		if i < 25 && ch.Kept {
			if st, err := s.history.iceberg.Stats(r.Context(), t.Namespace, table+"#"+c.Hash); err == nil {
				ch.Stats = &st
			}
			ch.Who = s.writer(r.Context(), t, table, c.Time)
		}
		out = append(out, ch)
	}
	writeJSON(w, http.StatusOK, map[string]any{"table": table, "changes": out, "keepHistory": s.history.keep.String()})
}

var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// writer is who wrote to the table about then, as Ranger recorded it.
func (s *server) writer(ctx context.Context, t *project.Tables, table string, at time.Time) string {
	if s.audit == nil {
		return ""
	}
	events, err := s.audit.Find(ctx, audit.Query{Resources: []string{t.Catalog + "/" + t.Namespace + "/" + table}, Source: "trino",
		Result: "allowed", Since: at.Add(-3 * time.Minute), Before: at.Add(time.Minute), Limit: 50})
	if err != nil {
		return ""
	}
	for _, e := range events {
		a := strings.ToLower(e.Action)
		if strings.HasPrefix(a, "insert") || strings.HasPrefix(a, "delete") || strings.HasPrefix(a, "update") ||
			strings.HasPrefix(a, "create") || strings.HasPrefix(a, "merge") || strings.HasPrefix(a, "truncate") {
			return e.User
		}
	}
	return ""
}

// expireTags removes the tags past their keepUntil from every project: the
// operator then removes Nessie's.
func (s *server) expireTags(ctx context.Context) {
	projects, err := s.store.Projects(ctx)
	if err != nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, p := range projects {
		t := p.Spec.Tables
		if t == nil || len(t.Tags) == 0 {
			continue
		}
		kept := slices.DeleteFunc(append([]project.Tag{}, t.Tags...), func(tg project.Tag) bool { return tg.KeepUntil < today })
		if len(kept) == len(t.Tags) {
			continue
		}
		next := *p
		tables := *t
		tables.Tags = kept
		next.Spec.Tables = &tables
		if err := s.saveSpec(ctx, &next); err != nil {
			s.log.Error("tags: expiring", "project", p.Metadata.Name, "error", err)
			continue
		}
		s.log.Info("tags: expired", "project", p.Metadata.Name, "removed", len(t.Tags)-len(kept))
		s.writeAudit(audit.Event{User: "storscale-platform", Action: "tag.expired", Resource: "project:" + p.Metadata.Name, Allowed: true,
			Detail: fmt.Sprintf("%d past their keepUntil", len(t.Tags)-len(kept))})
	}
}
