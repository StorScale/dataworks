package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/audit"
	"github.com/StorScale/storscale-platform/internal/project"
)

// The audit log: what people did in the platform, recorded beside Ranger's
// record of every access Trino allowed or refused (internal/audit).

func (s *server) startAudit() error {
	u := strings.TrimRight(os.Getenv("AUDIT_URL"), "/")
	if u == "" {
		return nil
	}
	keep, ok := project.Within(envOr("AUDIT_KEEP", "365d"))
	if !ok {
		return fmt.Errorf("AUDIT_KEEP %q: as 365d", os.Getenv("AUDIT_KEEP"))
	}
	s.audit = &audit.Log{URL: u, HTTP: &http.Client{Timeout: 15 * time.Second}, Keep: keep}
	return nil
}

// clientIP is where a request came from: the first address the gateway saw.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// record adds one of the platform's events to the audit log; recordRefusal,
// someone it turned away.
func (s *server) record(r *http.Request, who, action, resource, detail string) {
	s.writeAudit(audit.Event{User: who, Action: action, Resource: resource, Allowed: true, Detail: detail, IP: clientIP(r)})
}

func (s *server) recordRefusal(r *http.Request, who, action, resource, why string) {
	s.writeAudit(audit.Event{User: who, Action: action, Resource: resource, Detail: why, IP: clientIP(r)})
}

// writeAudit writes in the background: a slow log never holds up a request.
func (s *server) writeAudit(e audit.Event) {
	if s.audit == nil {
		return
	}
	e.Time = time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.audit.Add(ctx, e); err != nil {
			s.log.Error("audit", "action", e.Action, "resource", e.Resource, "error", err)
		}
	}()
}

// changes says what a project's change touched: the parts of its spec, as
// tables.readers.masks, or that it's new.
func changes(before, after *project.Project) string {
	if before == nil {
		return "created"
	}
	var a, b map[string]any
	ja, _ := json.Marshal(before.Spec)
	jb, _ := json.Marshal(after.Spec)
	json.Unmarshal(ja, &a)
	json.Unmarshal(jb, &b)
	var paths []string
	var walk func(prefix string, x, y any, depth int)
	walk = func(prefix string, x, y any, depth int) {
		mx, okx := x.(map[string]any)
		my, oky := y.(map[string]any)
		if !okx || !oky || depth == 3 {
			if !reflect.DeepEqual(x, y) {
				paths = append(paths, prefix)
			}
			return
		}
		keys := map[string]bool{}
		for k := range mx {
			keys[k] = true
		}
		for k := range my {
			keys[k] = true
		}
		for k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			walk(p, mx[k], my[k], depth+1)
		}
	}
	walk("", a, b, 0)
	if len(paths) == 0 {
		return "saved, unchanged"
	}
	sort.Strings(paths)
	return "changed: " + strings.Join(paths, ", ")
}

// query is an audit query from a request's parameters.
func auditQuery(r *http.Request) audit.Query {
	q := r.URL.Query()
	aq := audit.Query{User: q.Get("user"), Result: q.Get("result"), Source: q.Get("source"), Text: q.Get("q")}
	aq.Limit, _ = strconv.Atoi(q.Get("limit"))
	if t, err := time.Parse(time.RFC3339Nano, q.Get("before")); err == nil {
		aq.Before = t
	}
	if t, err := time.Parse(time.RFC3339Nano, q.Get("since")); err == nil {
		aq.Since = t
	}
	return aq
}

// projectResources are a project's records' resources: its own, and its tables'.
func projectResources(p *project.Project) []string {
	out := []string{"project:" + p.Metadata.Name}
	if t := p.Spec.Tables; t != nil {
		out = append(out, t.Catalog+"/"+t.Namespace)
	}
	return out
}

// handleProjectAudit is a project's audit log, for its editors and the
// platform's administrators: who changed it, and every access to its
// tables that Trino allowed or refused.
func (s *server) handleProjectAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if !v.can(project.PermSeeAudit) {
		s.fail(w, r, http.StatusForbidden, "a project's audit log is for its editors")
		return
	}
	q := auditQuery(r)
	q.Resources = projectResources(v.Spec)
	s.answerAudit(w, r, q)
}

// handleAudit is the whole platform's audit log, for its administrators;
// ?project= narrows it to a project's, ?format=csv is for auditors.
func (s *server) handleAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.member(w, r) // a read, and a download link (?format=csv): no X-Platform-Request
	if sess == nil {
		return
	}
	if !s.isAdmin(sess.User) {
		s.fail(w, r, http.StatusForbidden, "the platform's audit log is its administrators'")
		return
	}
	q := auditQuery(r)
	if name := r.URL.Query().Get("project"); name != "" && s.store != nil {
		p, err := s.store.Project(r.Context(), name)
		if err != nil || p == nil {
			s.fail(w, r, http.StatusNotFound, "no such project")
			return
		}
		q.Resources = projectResources(p)
	}
	s.answerAudit(w, r, q)
}

func (s *server) answerAudit(w http.ResponseWriter, r *http.Request, q audit.Query) {
	if s.audit == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "events": []any{}})
		return
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if r.URL.Query().Get("format") == "csv" && q.Limit == 100 {
		q.Limit = 1000
	}
	events, err := s.audit.Find(r.Context(), q)
	if err != nil {
		s.log.Error("audit", "error", err)
		s.fail(w, r, http.StatusBadGateway, "the audit log isn't answering")
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
		c := csv.NewWriter(w)
		c.Write([]string{"time", "user", "action", "resource", "result", "detail", "source", "policy", "reason", "ip"})
		for _, e := range events {
			result := "denied"
			if e.Allowed {
				result = "allowed"
			}
			policy := ""
			if e.Policy > 0 {
				policy = strconv.FormatInt(e.Policy, 10)
			}
			c.Write([]string{e.Time.UTC().Format(time.RFC3339), e.User, e.Action, e.Resource, result, e.Detail, e.Source, policy, e.Reason, e.IP})
		}
		c.Flush()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "events": events, "more": len(events) == q.Limit})
}
