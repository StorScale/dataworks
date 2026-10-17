package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPlatformEventIsWrittenInRangersFields(t *testing.T) {
	var got []map[string]any
	solr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/solr/ranger_audits/update" || r.URL.Query().Get("commitWithin") == "" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{"responseHeader":{"status":0}}`))
	}))
	defer solr.Close()
	l := &Log{URL: solr.URL + "/solr/ranger_audits", HTTP: solr.Client(), Keep: 365 * 24 * time.Hour}
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	err := l.Add(context.Background(), Event{Time: at, User: "bob", Action: "project.saved", Resource: "project:sales",
		Allowed: true, Detail: "changed: tables.readers.masks", IP: "10.0.0.7"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	d := got[0]
	for k, want := range map[string]any{"reqUser": "bob", "access": "project.saved", "resource": "project:sales", "resType": "project",
		"result": float64(1), "reqData": "changed: tables.readers.masks", "repo": "storscale-platform", "evtTime": "2026-10-17T09:30:00.000Z",
		"cliIP": "10.0.0.7", "_ttl_": "+365DAYS", "logType": "RangerAudit"} {
		if d[k] != want {
			t.Errorf("%s: %v, want %v", k, d[k], want)
		}
	}
	if id, _ := d["id"].(string); !strings.HasPrefix(id, "storscale-") || len(id) < 30 {
		t.Errorf("id %q", d["id"])
	}
}

func TestAQueryPicksAProjectsRecords(t *testing.T) {
	q := Query{Resources: []string{"project:sales", "iceberg/sales"}, User: "Alice", Result: "denied",
		Before: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC), Limit: 50}
	p := q.params()
	fq := strings.Join(p["fq"], " && ")
	for _, want := range []string{
		`resource:project\:sales`, `resource:project\:sales\/*`, `resource:iceberg\/sales\/*`, // the project, and what's under it
		`reqUser:alice`, `result:0`, `evtTime:[* TO 2026-10-17T09:00:00.000Z}`,
	} {
		if !strings.Contains(fq, want) {
			t.Errorf("fq lacks %s:\n%s", want, fq)
		}
	}
	if p := (Query{Source: "trino"}).params(); strings.Join(p["fq"], "") != `-repo:storscale\-platform` {
		t.Errorf("Trino's: %v", p["fq"])
	}
	if strings.Contains(fq, `resource:iceberg\/salesforce`) || p.Get("rows") != "50" || p.Get("sort") != "evtTime desc" {
		t.Errorf("params: %v", p)
	}
}

func TestFoundRecordsAreEvents(t *testing.T) {
	solr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"numFound":2,"docs":[
			{"evtTime":"2026-10-17T09:31:00.000Z","reqUser":"alice","access":"select","resource":"iceberg/sales/payroll/salary","result":0,"repo":"lakehouse","policy":-1,"reason":"no policy"},
			{"evtTime":"2026-10-17T09:30:00.000Z","reqUser":"bob","access":"project.saved","resource":"project:sales","result":1,"repo":"storscale-platform","reqData":"changed: members"}]}}`))
	}))
	defer solr.Close()
	got, err := (&Log{URL: solr.URL, HTTP: solr.Client()}).Find(context.Background(), Query{})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v, %v", got, err)
	}
	if e := got[0]; e.User != "alice" || e.Allowed || e.Source != "trino" || e.Policy != 0 || e.Action != "select" || e.Reason != "no policy" {
		t.Errorf("Ranger's: %+v", e)
	}
	if e := got[1]; e.Source != "platform" || !e.Allowed || e.Detail != "changed: members" {
		t.Errorf("the platform's: %+v", e)
	}
}
