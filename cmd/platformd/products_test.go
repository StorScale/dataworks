package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

func TestAProductIsAskedForApprovedAndCertified(t *testing.T) {
	company, err := project.Parse([]byte(`{"metadata":{"name":"company"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},
		"products":[{"name":"bookings","table":"bookings","owner":"bob","version":"1","contract":{"columns":["booking_id"]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	campaigns, _ := project.Parse([]byte(`{"metadata":{"name":"campaigns"},"spec":{"members":[{"user":"dana","role":"editor"}],"tables":{}}}`))
	st := &projectsKept{statuses: statuses{list: []*project.Status{
		{Name: "company", Members: []project.Person{{Username: "bob", Role: "editor"}}},
		{Name: "campaigns", Members: []project.Person{{Username: "dana", Role: "editor"}, {Username: "alice", Role: "reader"}}}}},
		specs: map[string]*project.Project{"company": company, "campaigns": campaigns}}
	hour := time.Now().Add(time.Hour)
	s := &server{cfg: config{Groups: []string{"analysts", "privacy"}, AdminGroups: []string{"engineers"}, ProductCertifierGroups: []string{"governance"}},
		store: st, files: kept{}, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"dana":  {User: user{Username: "dana", Groups: []string{"analysts"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour},
			"priya": {User: user{Username: "priya", Groups: []string{"analysts", "governance"}}, Expires: hour},
		}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const base = "/api/products/company/bookings"
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "POST", base + "/requests", `{"consumer":"campaigns"}`, http.StatusForbidden},        // a reader of campaigns
		{"dana", "POST", base + "/requests", `{"consumer":"company"}`, http.StatusUnprocessableEntity}, // its own
		{"dana", "POST", base + "/requests", `{"consumer":"campaigns","why":"campaign targeting"}`, http.StatusOK},
		{"dana", "POST", base + "/requests/campaigns/approve", "", http.StatusForbidden}, // not company's editor
		{"bob", "POST", base + "/requests/campaigns/maybe", "", http.StatusNotFound},
		{"bob", "POST", base + "/requests/campaigns/approve", "", http.StatusOK},
		{"bob", "POST", base + "/requests/campaigns/approve", "", http.StatusNotFound}, // answered
		{"bob", "POST", base + "/certification", "", http.StatusForbidden},             // not the governance group
		{"priya", "POST", base + "/certification", "", http.StatusOK},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	pr := st.specs["company"].ProductOf("bookings")
	if len(pr.Consumers) != 1 || pr.Consumers[0] != "campaigns" || !pr.Certified() || pr.Certification.By != "priya" {
		t.Fatalf("after: %+v %+v", pr, pr.Certification)
	}
	// Listed to anyone signed in, certified first; requests only to the producer's editors.
	_, body := call("alice", "GET", "/api/products", "")
	var out struct {
		Products []productView `json:"products"`
	}
	json.Unmarshal([]byte(body), &out)
	if len(out.Products) != 1 || !out.Products[0].Certified || out.Products[0].CanApprove {
		t.Errorf("alice's list: %s", body)
	}
	// An administrator's save can't forge consumers or certification.
	forged := *st.specs["company"]
	forged.Spec.Products = []project.Product{{Name: "bookings", Table: "bookings", Owner: "bob", Version: "2", Consumers: []string{"eve"}}}
	project.KeepProductState(st.specs["company"], &forged)
	if fp := forged.Spec.Products[0]; len(fp.Consumers) != 1 || fp.Consumers[0] != "campaigns" || fp.Certified() {
		t.Errorf("kept, and version 2 not certified: %+v", fp)
	}
	if code, _ := call("bob", "DELETE", base+"/consumers/campaigns", ""); code != 200 || len(st.specs["company"].ProductOf("bookings").Consumers) != 0 {
		t.Errorf("removed: %d", code)
	}
}

func TestAProjectsEditorPublishesAProduct(t *testing.T) {
	retail, err := project.Parse([]byte(`{"metadata":{"name":"retail"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],"tables":{},
		"products":[{"name":"orders","table":"uploads_orders","owner":"bob","version":"1","consumers":["marketing"],
		"contract":{"columns":["order_id"]},"certification":{"by":"priya","at":"2026-10-01T00:00:00Z","hash":"x"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	retail.Spec.Products[0].Certification.Hash = retail.Spec.Products[0].PromiseHash()
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "retail", Members: []project.Person{
		{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}}}, specs: map[string]*project.Project{"retail": retail}}
	hour := time.Now().Add(time.Hour)
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}},
		store: st, files: kept{}, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour},
		}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const customers = `{"table":"uploads_customers","description":"Harbor's customers","owner":"bob","version":"1","terms":["customer"],
		"contract":{"columns":["customer_id","region"],"within":"24h","checks":true}}`
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "PUT", "/api/projects/retail/products/customers", customers, http.StatusForbidden}, // a reader
		{"bob", "PUT", "/api/projects/retail/products/customers", `{"table":"uploads_customers","owner":"bob"}`, http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/projects/retail/products/customers", `{"table":"x","owner":"bob","version":"1","consumers":["eve"]}`, http.StatusOK}, // consumers forged: dropped
		{"bob", "PUT", "/api/projects/retail/products/customers", `{"table":"x","owner":"bob","version":"1","colour":"red"}`, http.StatusBadRequest},
		{"bob", "PUT", "/api/projects/retail/products/customers", customers, http.StatusOK},
		{"bob", "DELETE", "/api/projects/retail/products/orders", "", http.StatusConflict}, // marketing reads it
		{"bob", "DELETE", "/api/projects/retail/products/none", "", http.StatusNotFound},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	got := st.specs["retail"]
	if pr := got.ProductOf("customers"); pr == nil || pr.Contract.Within != "24h" || pr.Terms[0] != "customer" || len(pr.Consumers) != 0 {
		t.Fatalf("published: %+v", pr)
	}
	// A changed promise keeps who reads it, and lapses the certification.
	if code, body := call("bob", "PUT", "/api/projects/retail/products/orders", `{"table":"uploads_orders","owner":"bob","version":"2","contract":{"columns":["order_id"]}}`); code != 200 {
		t.Fatalf("changed: %d %s", code, body)
	}
	if pr := st.specs["retail"].ProductOf("orders"); pr.Version != "2" || len(pr.Consumers) != 1 || pr.Certification == nil || pr.Certified() {
		t.Errorf("after the change: %+v %+v", pr, pr.Certification)
	}
	if code, _ := call("bob", "DELETE", "/api/projects/retail/products/customers", ""); code != http.StatusNoContent || st.specs["retail"].ProductOf("customers") != nil {
		t.Errorf("taken back: %d", code)
	}
}
