package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

func TestTrinosQueriesAreCountedByProjectAndPriced(t *testing.T) {
	t.Setenv("TRINO_SHARED_SECRET", "s3cret")
	sales, _ := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"cost":{"budget":1}}}`))
	ops, _ := project.Parse([]byte(`{"metadata":{"name":"ops"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{"namespace":"operations"}}}`))
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Spec: sales,
		Members: []project.Person{{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}, {Name: "ops", Spec: ops}}},
		specs: map[string]*project.Project{"sales": sales, "ops": ops}}
	hour := time.Now().Add(time.Hour)
	files := kept{}
	s := &server{cfg: config{Groups: []string{"analysts", "finance"}, AdminGroups: []string{"engineers"}, UsageGroups: []string{"finance"},
		Rates: rates{Currency: "USD", CPUHour: 3600, StorageGBMonth: 1}}, // a dollar a CPU-second, to read the sums easily
		store: st, files: files, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)), sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour},
			"frank": {User: user{Username: "frank", Groups: []string{"finance"}}, Expires: hour}}}
	s.usage = &usageCounter{host: "a", by: map[string]*trinoUsage{}, dirty: map[string]bool{}}
	h := s.routes()
	post := func(secret, body string) int {
		r := httptest.NewRequest("POST", "/internal/usage/trino", strings.NewReader(body))
		r.Header.Set("X-Storscale-Usage", secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	now := float64(time.Now().Unix())
	event := func(id, user string, cpu any, inputs ...string) map[string]any {
		var ins []map[string]string
		for _, in := range inputs {
			cat, schema, _ := strings.Cut(in, ".")
			ins = append(ins, map[string]string{"catalogName": cat, "schema": schema, "table": "t"})
		}
		return map[string]any{"metadata": map[string]any{"queryId": id, "query": "SELECT 1"}, "context": map[string]any{"user": user},
			"statistics": map[string]any{"cpuTime": cpu, "physicalInputBytes": 1000}, "ioMetadata": map[string]any{"inputs": ins}, "endTime": now}
	}
	one, _ := json.Marshal(event("q1", "bob", 0.6, "iceberg.sales"))
	if code := post("wrong", string(one)); code != http.StatusUnauthorized {
		t.Errorf("without Trino's secret: %d", code)
	}
	if code := post("s3cret", string(one)); code != http.StatusNoContent {
		t.Fatalf("an event: %d", code)
	}
	list, _ := json.Marshal([]any{event("q2", "alice", "250ms", "iceberg.sales", "iceberg.operations"), // half each
		event("q3", "bob", "PT0.2S", "iceberg.elsewhere")}) // no project's
	post("s3cret", string(list))
	s.usage.keep(context.Background(), s)
	month := time.Now().UTC().Format("2006-01")
	u := s.usageOf(context.Background(), sales, month)
	if u.Trino.Queries != 2 || math.Abs(u.Trino.CPUSeconds-0.725) > 1e-9 || u.Trino.ByUser["alice"].CPUSeconds != 0.125 || len(u.Trino.Top) != 2 {
		t.Errorf("sales: %+v", u.Trino)
	}
	if o := s.usageOf(context.Background(), ops, month); o.Trino.Queries != 1 || o.Trino.CPUSeconds != 0.125 {
		t.Errorf("ops: %+v", o.Trino)
	}
	if u.Cost["trino"] != 0.725 || u.Cost["total"] != 0.725 || u.Share == nil {
		t.Errorf("cost: %v", u.Cost)
	}
	// Another host's count is added to it.
	other, _ := json.Marshal(&trinoUsage{Queries: 1, CPUSeconds: 0.2, ByUser: map[string]*usageShare{"bob": {Queries: 1, CPUSeconds: 0.2}}, ByDay: map[string]*usageShare{}})
	files.PutFile(context.Background(), usageKey("sales", month, "b"), other, "")
	if u := s.usageOf(context.Background(), sales, month); u.Trino.Queries != 3 || u.Cost["total"] != 0.925 {
		t.Errorf("two hosts: %+v %v", u.Trino.Queries, u.Cost)
	}
	if is := s.usageIssues(context.Background(), sales, "/p/sales"); len(is) != 1 || is[0].Kind != "cost" || !strings.Contains(is[0].Title, "92%") {
		t.Errorf("at 92.5%% of the budget: %+v", is)
	}
	// Who sees it: the project's editors, finance; not its readers.
	get := func(who, path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		who, path string
		want      int
	}{{"bob", "/api/projects/sales/usage", 200}, {"alice", "/api/projects/sales/usage", 403}, {"frank", "/api/projects/sales/usage", 200},
		{"frank", "/api/usage", 200}, {"bob", "/api/usage", 403}, {"bob", "/api/projects/sales/usage?month=soon", 400}} {
		if code := get(c.who, c.path); code != c.want {
			t.Errorf("%s %s: %d, want %d", c.who, c.path, code, c.want)
		}
	}
}

func TestMoneyKeepsASmallAmount(t *testing.T) {
	for in, want := range map[float64]string{12.345: "12.35", 0: "0.00", 0.0000417: "0.000042", 0.005: "0.005", 1e-9: "0.000000001", -0.0023: "-0.0023"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %s, want %s", in, got, want)
		}
	}
}
