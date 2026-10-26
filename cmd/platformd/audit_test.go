package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/audit"
	"github.com/StorScale/dataworks/internal/project"
)

func TestChangesSayWhatAProjectsChangeTouched(t *testing.T) {
	parse := func(spec string) *project.Project {
		p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},"spec":` + spec + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := parse(`{"members":[{"group":"engineers","role":"editor"}],"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK"}]}}}`)
	b := parse(`{"members":[{"group":"engineers","role":"editor"},{"group":"analysts","role":"reader"}],"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK_SHOW_LAST_4"}]}}}`)
	if got := changes(a, b); got != "changed: members, tables.readers.masks" {
		t.Errorf("changes: %q", got)
	}
	if got := changes(nil, b); got != "created" {
		t.Errorf("a new project: %q", got)
	}
	if got := changes(b, b); got != "saved, unchanged" {
		t.Errorf("unchanged: %q", got)
	}
}

func TestTheAuditLogIsAdministratorsAndExports(t *testing.T) {
	k := newFakeKeycloak(t)
	ts, s := newTestPlatform(t, k, func(*config) {})
	solr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/update" { // signing in is recorded
			w.Write([]byte(`{}`))
			return
		}
		if r.URL.Query().Get("fq") != "result:0" {
			t.Errorf("fq: %v", r.URL.Query()["fq"])
		}
		w.Write([]byte(`{"response":{"docs":[{"evtTime":"2026-10-17T09:31:00.000Z","reqUser":"alice","access":"select",
			"resource":"iceberg/sales/payroll/salary","result":0,"repo":"trino","policy":-1}]}}`))
	}))
	defer solr.Close()
	s.audit = &audit.Log{URL: solr.URL, HTTP: solr.Client()}
	get := func(c *http.Cookie, path string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.AddCookie(c)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	alice, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	if res := get(alice, "/api/audit?result=denied"); res.StatusCode != 403 {
		t.Errorf("alice: HTTP %d, want 403", res.StatusCode)
	}
	bob, _ := signIn(t, ts, k, map[string]any{"preferred_username": "bob", "groups": []string{"engineers"}}, "/")
	res := get(bob, "/api/audit?result=denied")
	var body struct {
		Events []audit.Event `json:"events"`
	}
	json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if res.StatusCode != 200 || len(body.Events) != 1 || body.Events[0].User != "alice" || body.Events[0].Allowed {
		t.Fatalf("bob: HTTP %d %+v", res.StatusCode, body)
	}
	res = get(bob, "/api/audit?result=denied&format=csv")
	rows, err := csv.NewReader(res.Body).ReadAll()
	res.Body.Close()
	if err != nil || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/csv") || len(rows) != 2 ||
		strings.Join(rows[1][:5], ",") != "2026-10-17T09:31:00Z,alice,select,iceberg/sales/payroll/salary,denied" {
		t.Errorf("csv: %v %v", rows, err)
	}
}
