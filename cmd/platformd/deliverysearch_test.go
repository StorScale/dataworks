package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

func TestAsOfReadsTheTablesAtACommit(t *testing.T) {
	got := asOf(`select c.customer_id, o.amount from customers c join sales.orders o on o.customer = c.customer_id join iceberg."sales".orders_by_region r on true`,
		"iceberg", "sales", "abc", []string{"orders_by_region", "customers", "orders"})
	want := `select c.customer_id, o.amount from "iceberg"."sales"."customers#abc" c join "iceberg"."sales"."orders#abc" o on o.customer = c.customer_id join "iceberg"."sales"."orders_by_region#abc" r on true`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if quoteText("O'Brien") != "'O''Brien'" {
		t.Error(quoteText("O'Brien"))
	}
}

func TestTheDeliveryLogIsSearchedByAPersonsIDAgainTheTablesAsTheyWere(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql, _ := io.ReadAll(r.Body)
		mu.Lock()
		ran = append(ran, string(sql))
		mu.Unlock()
		switch {
		case strings.HasPrefix(string(sql), "SHOW TABLES"):
			w.Write([]byte(`{"id":"q","data":[["customers"]]}`))
		case strings.Contains(string(sql), "#c1"):
			w.Write([]byte(`{"id":"q","data":[[2]]}`))
		default:
			w.Write([]byte(`{"id":"q","data":[[0]]}`))
		}
	}))
	defer tr.Close()
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],
		"tables":{"people":[{"table":"customers","column":"customer_id"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	files := kept{}
	run := func(key, commit string) {
		data, _ := json.Marshal(map[string]any{"delivery": "acme", "date": key[:8], "recorded": key, "target": "sftp://acme",
			"sends": map[string]any{"table": "customers"}, "kept": map[string]any{"commit": commit, "tag": "Delivery.sales.acme." + key}})
		files["deliveries/sales/acme/"+key+".json"] = string(data)
	}
	run("20261001T050000Z", "c1")
	run("20261002T050000Z", "c2")
	files["deliveries/sales/acme/20260901T050000Z.json"] = `{"delivery":"acme","date":"2026-09-01"}` // before reads were kept
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales"}}}, specs: map[string]*project.Project{"sales": sales}}
	s := &server{cfg: config{Groups: []string{"analysts", "privacy"}, AdminGroups: []string{"engineers"}, DeliveryApproverGroups: []string{"privacy"}},
		store: st, files: files, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		maintainer: &maintainer{trino: &trino.Client{URL: tr.URL, User: "maintenance", Password: "p", HTTP: tr.Client()}},
		sessions: map[string]*session{
			"priya": {User: user{Username: "priya", Groups: []string{"privacy"}}, Expires: time.Now().Add(time.Hour)},
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)},
		}}
	h := s.routes()
	search := func(who, person string) (int, map[string]any) {
		r := httptest.NewRequest("GET", "/api/projects/sales/deliveries/search?person="+person, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := search("bob", "C-1042"); code != http.StatusForbidden {
		t.Errorf("bob, an editor but no data-protection approver: %d", code)
	}
	code, out := search("priya", "C-1042")
	runs, _ := out["runs"].([]any)
	if code != 200 || len(runs) != 1 || out["checked"] != 2.0 || out["notKept"] != 1.0 {
		t.Fatalf("priya: %d %v", code, out)
	}
	if hit := runs[0].(map[string]any); hit["rows"] != 2.0 || hit["sent"] != "20261001T050000Z" || hit["target"] != "sftp://acme" {
		t.Errorf("the run that carried C-1042: %v", hit)
	}
	want := `SELECT count(*) FROM "iceberg"."sales"."customers#c1" WHERE CAST("customer_id" AS varchar) = 'C-1042'`
	if !strings.Contains(strings.Join(ran, "\n"), want) {
		t.Errorf("ran:\n%s\nwant %s", strings.Join(ran, "\n"), want)
	}
}
