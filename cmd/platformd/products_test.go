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

	"github.com/StorScale/storscale-platform/internal/project"
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
