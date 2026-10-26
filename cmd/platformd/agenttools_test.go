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
	"github.com/StorScale/dataworks/internal/store"
)

func TestAProjectsToolsAreCalledByTheAgentsGivenThemAndItsEditors(t *testing.T) {
	var saw *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = r
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"shipped"}`))
	}))
	defer api.Close()
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"llm":{"budget":5},
		"tools":[{"name":"order_status","description":"An order's status.","url":"` + api.URL + `/v2","method":"GET","path":"/orders/{id}",
			"connection":"sales-shop","parameters":[{"name":"id","in":"path","required":true},{"name":"detail","in":"query","type":"boolean"}]}],
		"agents":[{"name":"asker","owner":"bob","tools":["tool:order_status","agent:sales/triage"],"registeredBy":"bob","approvedBy":"alice"},
			{"name":"triage","owner":"bob","prompt":"sum up the alerts","budget":1,"registeredBy":"bob","approvedBy":"alice"},
			{"name":"other","owner":"bob","tools":["list_alerts"],"registeredBy":"bob","approvedBy":"alice"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	members := []project.Person{{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"},
		{Username: "service-account-agent-sales-asker", Role: "reader"}, {Username: "service-account-agent-sales-other", Role: "reader"}}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: members}}},
		specs: map[string]*project.Project{"sales": sales}},
		conns: map[string]store.StoredConnection{"sales/sales-shop": {ConnectionMeta: store.ConnectionMeta{Name: "sales-shop"}, Data: map[string]string{"token": "tok-1\n"}}}}
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}}, store: st, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: map[string]*session{}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const asker, other = "service-account-agent-sales-asker", "service-account-agent-sales-other"
	const tool = "/api/agent/projects/sales/tools/order_status/call"
	for _, c := range []struct {
		who, body string
		want      int
	}{
		{"alice", `{"arguments":{"id":"1"}}`, http.StatusForbidden}, // a reader
		{other, `{"arguments":{"id":"1"}}`, http.StatusForbidden},   // an agent not given it
		{asker, `{"arguments":{}}`, http.StatusBadRequest},          // its id needed
		{asker, `{"arguments":{"id":"1","sql":"drop"}}`, http.StatusBadRequest},
		{asker, `{"arguments":{"id":"1","detail":"maybe"}}`, http.StatusBadRequest},
		{"bob", `{"arguments":{"id":"7"}}`, http.StatusOK},
	} {
		if code, body := call(c.who, "POST", tool, c.body); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.who, c.body, code, body, c.want)
		}
	}
	code, body := call(asker, "POST", tool, `{"arguments":{"id":"a/b c","detail":true}}`)
	if code != http.StatusOK || !strings.Contains(body, `"status":200`) || !strings.Contains(body, `"answer":{"status":"shipped"}`) {
		t.Fatalf("asker's call: %d %s", code, body)
	}
	if saw.URL.EscapedPath() != "/v2/orders/a%2Fb%20c" || saw.URL.Query().Get("detail") != "true" || saw.Header.Get("Authorization") != "Bearer tok-1" {
		t.Errorf("the API saw %s %s, %q", saw.URL.EscapedPath(), saw.URL.RawQuery, saw.Header.Get("Authorization"))
	}
	var listed struct {
		Tools  []toolView    `json:"tools"`
		Agents []askableView `json:"agents"`
	}
	_, body = call(asker, "GET", "/api/agent/projects/sales/tools", "")
	json.Unmarshal([]byte(body), &listed)
	if len(listed.Tools) != 1 || len(listed.Agents) != 1 || listed.Agents[0].Name != "triage" {
		t.Errorf("the asker's list: %s", body)
	}
	_, body = call(other, "GET", "/api/agent/projects/sales/tools", "")
	listed.Tools, listed.Agents = nil, nil
	json.Unmarshal([]byte(body), &listed)
	if len(listed.Tools) != 0 || len(listed.Agents) != 0 {
		t.Errorf("the other's list: %s", body)
	}
	// Asking: only the agents given it, and only an agent with a prompt.
	if code, _ := call(other, "POST", "/api/agent/projects/sales/agents/triage/ask", `{"task":"sum up"}`); code != http.StatusForbidden {
		t.Errorf("an agent not given triage asked it: %d", code)
	}
	if code, _ := call("bob", "POST", "/api/agent/projects/sales/agents/asker/ask", `{"task":"sum up"}`); code != http.StatusNotFound {
		t.Errorf("an agent with no prompt was asked: %d", code)
	}
	if code, _ := call("carol", "POST", "/api/agent/projects/sales/agents/triage/ask", `{"task":"sum up"}`); code != http.StatusForbidden {
		t.Errorf("no member asked: %d", code)
	}
}
