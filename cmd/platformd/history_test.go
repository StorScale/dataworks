package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
)

func TestDurationsAndKeeps(t *testing.T) {
	for in, want := range map[string]time.Duration{"P7D": 7 * 24 * time.Hour, "PT2M": 2 * time.Minute, "P1DT12H": 36 * time.Hour} {
		if got, ok := isoDuration(in); !ok || got != want {
			t.Errorf("isoDuration(%s) = %v, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "P", "7d", "PT"} {
		if _, ok := isoDuration(bad); ok {
			t.Errorf("isoDuration(%q): want refused", bad)
		}
	}
	from := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if got, _ := keepUntil("7y", from); got != "2033-10-03" {
		t.Errorf("7y: %s", got)
	}
	if got, _ := keepUntil("90d", from); got != "2027-01-01" {
		t.Errorf("90d: %s", got)
	}
	if _, ok := keepUntil("forever", from); ok {
		t.Error("forever: want refused")
	}
}

func TestAMomentIsACommit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/trees/main":
			w.Write([]byte(`{"reference": {"type": "BRANCH", "name": "main", "hash": "ffff0000ffff0000"}}`))
		case "/api/v2/trees/main/history":
			w.Write([]byte(`{"logEntries": [
				{"commitMeta": {"hash": "c2c2c2c2c2c2c2c2", "commitTime": "2026-10-02T12:00:00Z"}},
				{"commitMeta": {"hash": "c1c1c1c1c1c1c1c1", "commitTime": "2026-09-30T12:00:00Z"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	h := &history{nessie: &catalog.Nessie{HTTP: ts.Client(), Base: ts.URL + "/api/v2"}}
	p, _ := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"tags":[{"name":"close-2026-09","commit":"c1c1c1c1c1c1c1c1","keepUntil":"2033-10-03"}]}}}`))
	ctx := context.Background()
	for at, want := range map[string]string{
		"now": "ffff0000ffff0000", "": "ffff0000ffff0000",
		"close-2026-09":        "c1c1c1c1c1c1c1c1",
		"2026-10-01":           "c1c1c1c1c1c1c1c1", // the day's end: Oct 2's commit is after it
		"2026-10-02T13:00:00Z": "c2c2c2c2c2c2c2c2",
		"abcdef0123456789":     "abcdef0123456789",
	} {
		m, err := h.resolve(ctx, p, at)
		if err != nil || m.Commit != want {
			t.Errorf("%q: %+v, %v; want %s", at, m, err, want)
		}
	}
	if _, err := h.resolve(ctx, p, "2026-09-01"); err == nil || !strings.Contains(err.Error(), "starts after") {
		t.Errorf("before the history kept: %v", err)
	}
	if _, err := h.resolve(ctx, p, "last tuesday"); err == nil {
		t.Error("a moment it can't read: want refused")
	}
}
