package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

func TestAnAgentReadsItsProject(t *testing.T) {
	sales := &project.Project{Metadata: project.Metadata{Name: "sales"}}
	st := &statuses{list: []*project.Status{{Name: "sales", Phase: project.Ready, Spec: sales, Members: []project.Person{
		{Username: "service-account-agent-sales-nightly", Role: "agent"}, {Username: "bob", Role: "editor"}}}}}
	pa := &projectAlerts{issues: map[string]map[string]issue{"sales": {"check/orders/fresh": {Key: "check/orders/fresh", Kind: "check", Title: "orders is stale"}}}, muted: map[string]time.Time{}, checked: true}
	s := &server{store: st, now: time.Now, projectAlerts: pa, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokenUser: func(raw string) (string, error) {
			if raw == "bad" {
				return "", errors.New("not the realm's")
			}
			return raw, nil
		}}
	get := func(path, token string, h http.HandlerFunc) (int, map[string]any) {
		r := httptest.NewRequest("GET", path, nil)
		r.SetPathValue("name", "sales")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, c := range []struct {
		token string
		want  int
	}{{"", http.StatusUnauthorized}, {"bad", http.StatusUnauthorized}, {"service-account-agent-other-x", http.StatusForbidden}} {
		if got, _ := get("/api/agent/projects/sales/alerts", c.token, s.handleAgentAlerts); got != c.want {
			t.Errorf("alerts, %q: HTTP %d, want %d", c.token, got, c.want)
		}
	}
	code, out := get("/api/agent/projects/sales/alerts", "service-account-agent-sales-nightly", s.handleAgentAlerts)
	if code != http.StatusOK || out["checked"] != true || len(out["issues"].([]any)) != 1 || out["phase"] != "Ready" {
		t.Fatalf("the agent's alerts: HTTP %d %v", code, out)
	}
	if code, out := get("/api/agent/projects/sales/alerts", "bob", s.handleAgentAlerts); code != http.StatusOK || len(out["issues"].([]any)) != 1 {
		t.Fatalf("a member's token works the same: HTTP %d %v", code, out)
	}
	// Runs, with no Airflow here: said, not failed.
	if code, out := get("/api/agent/projects/sales/runs", "service-account-agent-sales-nightly", s.handleAgentRuns); code != http.StatusOK || out["note"] == nil {
		t.Fatalf("runs without Airflow: HTTP %d %v", code, out)
	}
}
