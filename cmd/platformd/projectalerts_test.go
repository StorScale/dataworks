package main

import (
	"net/http"
	"testing"
	"time"
)

func newProjectAlerts() *projectAlerts {
	return &projectAlerts{issues: map[string]map[string]issue{}, told: map[string]string{}, muted: map[string]time.Time{}, http: http.DefaultClient}
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
