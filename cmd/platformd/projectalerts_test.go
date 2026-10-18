package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

func newProjectAlerts() *projectAlerts {
	return &projectAlerts{issues: map[string]map[string]issue{}, told: map[string]string{}, muted: map[string]time.Time{}, unknown: map[string][]string{}, http: http.DefaultClient}
}

func TestProjectAlertsTellWhatStartsAndWhatComesRight(t *testing.T) {
	pa := newProjectAlerts()
	failed := issue{Key: "pipeline/load/r1", Kind: "pipeline", Title: "load's latest run failed"}
	check := issue{Key: "check/orders/not_null", Kind: "check", Title: "orders: the check not_null is failing"}
	stale := issue{Key: "stale/orders", Kind: "stale", Title: "orders is stale"}

	// platformd's first look: what's wrong already isn't news.
	if got := pa.update("sales", "h1", []issue{failed}); len(got) != 0 {
		t.Fatalf("first look sent %v", got)
	}
	pa.checked = true
	if got := pa.update("sales", "h1", []issue{failed}); len(got) != 0 {
		t.Fatalf("an issue already told is sent again: %v", got)
	}
	got := pa.update("sales", "h1", []issue{failed, check, stale})
	if len(got) != 2 || got[0].Title != "sales: "+check.Title || got[1].Title != "sales: "+stale.Title || got[1].Severity != "warning" {
		t.Fatalf("new issues: %+v", got)
	}

	// Muted: neither told nor resolved.
	pa.muted["sales/"+stale.Key] = time.Now().Add(time.Hour)
	got = pa.update("sales", "h1", nil)
	if len(got) != 1 || got[0].Title != "sales: resolved: "+check.Title || got[0].Severity != "up" {
		t.Fatalf("resolved (a pipeline's run isn't; a muted one isn't): %+v", got)
	}

	// No notify: nothing sent, though the issues are kept for the page.
	if got := pa.update("sales", "", []issue{check}); len(got) != 0 || len(pa.issues["sales"]) != 1 {
		t.Fatalf("without notify: %v, %v", got, pa.issues["sales"])
	}
	// A channel just named is told all that's wrong.
	if got := pa.update("sales", "h2", []issue{check}); len(got) != 1 {
		t.Fatalf("a new channel: %v", got)
	}
}

// statuses is a store with these statuses alone.
type statuses struct {
	store.Store
	list []*project.Status
}

func (s *statuses) Statuses(context.Context) ([]*project.Status, error) { return s.list, nil }

func TestACopyNotRunningIsAnIssueAfterAWhile(t *testing.T) {
	p, err := project.Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
		"tables": {}, "sources": [{"name": "finance", "kind": "postgresql", "connection": "fin", "copy": {"tables": ["public.invoices"]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &statuses{list: []*project.Status{{Name: "sales", Parts: []project.Part{
		{System: "copy", OK: false, Message: "copying finance: password authentication failed"}}}}}
	s := &server{store: st, projectAlerts: &projectAlerts{down: map[string]time.Time{}}}
	ctx := context.Background()
	if got := s.copyIssues(ctx, p, "/p/sales"); len(got) != 0 {
		t.Fatalf("at once: %v", got)
	}
	s.projectAlerts.down["sales/copy/finance"] = time.Now().Add(-CopyDownAfter - time.Minute)
	got := s.copyIssues(ctx, p, "/p/sales")
	if len(got) != 1 || got[0].Key != "copy/finance" || !strings.Contains(got[0].Detail, "password authentication failed") {
		t.Fatalf("after a while: %+v", got)
	}
	st.list[0].Parts[0] = project.Part{System: "copy", OK: true, Message: "applied"}
	if got := s.copyIssues(ctx, p, "/p/sales"); len(got) != 0 || len(s.projectAlerts.down) != 0 {
		t.Fatalf("running again: %v %v", got, s.projectAlerts.down)
	}
}
