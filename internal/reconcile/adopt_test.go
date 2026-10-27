package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// fakeAdoption plays the store's catalog in Trino (each table's newest
// metadata file) and Nessie's Iceberg REST API (what's registered).
type fakeAdoption struct {
	mu         sync.Mutex
	store      map[string]string // "schema.table" -> its newest metadata file
	registered map[string]string // "namespace.table" -> its metadata file
	calls      []string
}

func (f *fakeAdoption) servers(t *testing.T) (*trino.Client, *catalog.Iceberg) {
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		// SELECT file FROM "ext_lakehouse"."sales"."orders$metadata_log_entries" ...
		q := string(sql)
		if !strings.Contains(q, `"ext_lakehouse".`) {
			t.Errorf("asked %q, not the store's catalog", q)
		}
		for key, file := range f.store {
			s, tb, _ := strings.Cut(key, ".")
			if strings.Contains(q, `"`+s+`"."`+tb+`$metadata_log_entries"`) {
				json.NewEncoder(w).Encode(map[string]any{"id": "q", "stats": map[string]any{"state": "FINISHED"}, "data": [][]any{{file}}})
				return
			}
		}
		w.Write([]byte(`{"id":"q","error":{"errorName":"TABLE_NOT_FOUND","message":"Table not found"}}`))
	}))
	t.Cleanup(tr.Close)
	ns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/iceberg/v1/main%7Cwarehouse/namespaces/")
		switch {
		case r.URL.Path == "/iceberg/v1/config":
			w.Write([]byte(`{"overrides": {"prefix": "main%7Cwarehouse"}}`))
			return
		case r.Method == "POST" && strings.HasSuffix(p, "/register"):
			var body struct {
				Name     string `json:"name"`
				Location string `json:"metadata-location"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			key := strings.TrimSuffix(p, "/register") + "." + body.Name
			if _, ok := f.registered[key]; ok {
				w.WriteHeader(409)
				return
			}
			f.registered[key] = body.Location
			f.calls = append(f.calls, "register "+key+" "+body.Location)
			w.Write([]byte(`{}`))
			return
		}
		nsName, tb, _ := strings.Cut(p, "/tables/")
		key := nsName + "." + tb
		switch r.Method {
		case "GET":
			loc, ok := f.registered[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"metadata-location": loc})
		case "DELETE":
			if r.URL.Query().Get("purgeRequested") != "false" {
				t.Errorf("%s dropped with purgeRequested=%q: its files are the store's", key, r.URL.Query().Get("purgeRequested"))
			}
			if _, ok := f.registered[key]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(f.registered, key)
			f.calls = append(f.calls, "unregister "+key)
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(ns.Close)
	return &trino.Client{URL: tr.URL, User: "platform-operator", Password: "p", HTTP: tr.Client()},
		&catalog.Iceberg{HTTP: ns.Client(), Base: ns.URL + "/iceberg", Warehouse: "warehouse"}
}

func (f *fakeAdoption) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func TestAdoptedTablesFollowTheirStore(t *testing.T) {
	f := &fakeAdoption{
		store: map[string]string{
			"sales.orders": "s3://lake/sales.db/orders/metadata/00003-a.metadata.json",
			"sales.custs":  "s3://lake/sales.db/custs/metadata/00001-b.metadata.json",
		},
		registered: map[string]string{
			// a table of the project's own, by an adopted one's name
			"retail.custs": "s3://warehouse/retail/custs_x/metadata/00009-c.metadata.json",
		},
	}
	tr, ic := f.servers(t)
	a := &Adopter{Trino: tr, Iceberg: ic}
	p := &project.Project{}
	p.Metadata.Name = "retail"
	p.Spec.Tables = &project.Tables{Catalog: "iceberg", Namespace: "retail", Adopt: []project.Adoption{
		{Table: "orders", From: "lakehouse.sales.orders"}, {Table: "custs", From: "lakehouse.sales.custs"}}}
	ctx := context.Background()

	err := a.Apply(ctx, p, nil)
	if err == nil || !strings.Contains(err.Error(), "a table custs of its own") {
		t.Errorf("a table of the project's own by an adopted name should be an error, and kept: %v", err)
	}
	if got := f.took(); len(got) != 1 || got[0] != "register retail.orders s3://lake/sales.db/orders/metadata/00003-a.metadata.json" {
		t.Errorf("first pass: %v (want orders registered from its current file, custs left alone)", got)
	}
	if f.registered["retail.custs"] != "s3://warehouse/retail/custs_x/metadata/00009-c.metadata.json" {
		t.Errorf("the project's own custs was changed: %v", f.registered["retail.custs"])
	}

	// Nothing changed in the store: nothing done.
	p.Spec.Tables.Adopt = p.Spec.Tables.Adopt[:1]
	if err := a.Apply(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.took(); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}

	// The other platform wrote orders: registered again from its newer file.
	f.store["sales.orders"] = "s3://lake/sales.db/orders/metadata/00004-d.metadata.json"
	if err := a.Apply(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.took(); strings.Join(got, "; ") != "unregister retail.orders; register retail.orders s3://lake/sales.db/orders/metadata/00004-d.metadata.json" {
		t.Errorf("after the store's change: %v", got)
	}

	// Taken out of the spec: unregistered (never purged), the store's files left alone.
	prev := *p
	pt := *p.Spec.Tables
	prev.Spec.Tables = &pt
	p.Spec.Tables = &project.Tables{Catalog: "iceberg", Namespace: "retail"}
	if err := a.Apply(ctx, p, &prev); err != nil {
		t.Fatal(err)
	}
	if got := f.took(); len(got) != 1 || got[0] != "unregister retail.orders" {
		t.Errorf("let go: %v", got)
	}

	// A store table that isn't there: said, nothing registered.
	p.Spec.Tables.Adopt = []project.Adoption{{Table: "gone", From: "lakehouse.sales.gone"}}
	if err := a.Apply(ctx, p, nil); err == nil || !strings.Contains(err.Error(), "adopting gone from lakehouse.sales.gone") {
		t.Errorf("a missing store table: %v", err)
	}
	if got := f.took(); len(got) != 0 {
		t.Errorf("a missing store table: %v", got)
	}
}
