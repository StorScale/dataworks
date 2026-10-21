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

func TestTheGovernanceViewIsEveryProjectAtOnce(t *testing.T) {
	company, err := project.Parse([]byte(`{"metadata":{"name":"company"},"spec":{"members":[{"user":"bob","role":"editor"},{"group":"analysts","role":"reader"}],
		"tables":{"readers":{"tables":["bookings"],"masks":[{"table":"bookings","column":"email","type":"MASK_HASH"}]}},
		"products":[{"name":"bookings","table":"bookings","owner":"bob","version":"1","contract":{"columns":["booking_id"]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	campaigns, _ := project.Parse([]byte(`{"metadata":{"name":"campaigns"},"spec":{"members":[{"user":"dana","role":"editor"}],"tables":{}}}`))
	st := &projectsKept{statuses: statuses{list: []*project.Status{
		{Name: "company", Members: []project.Person{{Username: "bob", Role: "editor"}}},
		{Name: "campaigns", Members: []project.Person{{Username: "dana", Role: "editor"}}}}},
		specs: map[string]*project.Project{"company": company, "campaigns": campaigns}}
	hour := time.Now().Add(time.Hour)
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}, ProductCertifierGroups: []string{"governance"}},
		store: st, files: kept{}, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"dana":  {User: user{Username: "dana", Groups: []string{"analysts"}}, Expires: hour},
			"priya": {User: user{Username: "priya", Groups: []string{"analysts", "governance"}}, Expires: hour},
			"erin":  {User: user{Username: "erin", Groups: []string{"analysts", "engineers"}}, Expires: hour},
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
	if code, _ := call("bob", "GET", "/api/governance", ""); code != http.StatusForbidden {
		t.Errorf("an editor saw it: %d", code)
	}
	if code, body := call("dana", "POST", "/api/products/company/bookings/requests", `{"consumer":"campaigns","why":"targeting"}`); code != http.StatusOK {
		t.Fatalf("request: %d %s", code, body)
	}
	for _, who := range []string{"priya", "erin"} {
		code, body := call(who, "GET", "/api/governance", "")
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", who, code, body)
		}
		var g struct {
			Pending  []govPending `json:"pending"`
			Rules    []govRules   `json:"rules"`
			Products []govProduct `json:"products"`
		}
		json.Unmarshal([]byte(body), &g)
		if len(g.Pending) != 1 || g.Pending[0].Kind != "access" || g.Pending[0].Name != "bookings for campaigns" {
			t.Errorf("%s pending: %+v", who, g.Pending)
		}
		if len(g.Products) != 1 || g.Products[0].Certified {
			t.Errorf("%s products: %+v", who, g.Products)
		}
		var rules govRules
		for _, r := range g.Rules {
			if r.Project == "company" {
				rules = r
			}
		}
		if len(rules.Readers) != 1 || rules.Readers[0] != "analysts" || len(rules.Masks) != 1 || rules.Masks[0] != "bookings.email: MASK_HASH" {
			t.Errorf("%s rules: %+v", who, g.Rules)
		}
	}
}
