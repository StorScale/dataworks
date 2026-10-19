package project

import (
	"strings"
	"testing"
	"time"
)

func TestAgents(t *testing.T) {
	spec := `{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},"agents":[%s]}}`
	for _, tc := range []struct{ agent, want string }{
		{`{"name":"nightly","owner":"bob","tools":["list_metrics","query_metrics"]}`, ""},
		{`{"name":"Nightly","owner":"bob"}`, "lowercase letters"},
		{`{"name":"a","owner":"bob","tools":["drop_tables"]}`, `no tool "drop_tables"`},
		{`{"name":"a","owner":"bob","schedule":"daily"}`, "five cron fields"},
		{`{"name":"a","owner":"bob"},{"name":"a","owner":"bob"}`, `"a" twice`},
		{`{"name":"a","owner":"bob","registeredBy":"bob","approvedBy":"bob"}`, "approved by the one who registered it"},
	} {
		_, err := Parse([]byte(strings.Replace(spec, "%s", tc.agent, 1)))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.agent, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.agent, err, tc.want)
		}
	}
	// What the platform sets is kept across a save; a change to what approval covered takes it away.
	before, _ := Parse([]byte(strings.Replace(spec, "%s", `{"name":"nightly","owner":"bob","tools":["list_metrics"],"registeredBy":"bob","approvedBy":"alice"}`, 1)))
	same, _ := Parse([]byte(strings.Replace(spec, "%s", `{"name":"nightly","owner":"bob","tools":["list_metrics"],"description":"reads the checks","approvedBy":"mallory"}`, 1)))
	KeepAgentState(before, same)
	if a := same.AgentOf("nightly"); a.ApprovedBy != "alice" || a.RegisteredBy != "bob" || !a.Approved() {
		t.Errorf("kept: %+v", a)
	}
	changed, _ := Parse([]byte(strings.Replace(spec, "%s", `{"name":"nightly","owner":"bob","tools":["list_metrics","query_metrics"]}`, 1)))
	KeepAgentState(before, changed)
	if a := changed.AgentOf("nightly"); a.Approved() || a.RegisteredBy != "bob" {
		t.Errorf("changed tools keep the approval: %+v", a)
	}
	// What a client may use.
	p, _ := Parse([]byte(strings.Replace(spec, "%s", `{"name":"nightly","owner":"bob","tools":["list_metrics"],"registeredBy":"bob","approvedBy":"alice","approvedAt":"2026-10-19T10:00:00Z"},{"name":"all","owner":"bob","registeredBy":"bob","approvedBy":"alice"},{"name":"new","owner":"bob"}`, 1)))
	if tools, ok := p.AgentToolsAllowed("agent-sales-nightly"); !ok || strings.Join(tools, ",") != "list_metrics" {
		t.Errorf("nightly: %v %v", tools, ok)
	}
	if tools, ok := p.AgentToolsAllowed("agent-sales-all"); !ok || len(tools) != len(AgentTools) {
		t.Errorf("all: %v %v", tools, ok)
	}
	if tools, ok := p.AgentToolsAllowed("agent-sales-new"); !ok || len(tools) != 0 {
		t.Errorf("unapproved: %v %v", tools, ok)
	}
	if _, ok := p.AgentToolsAllowed("agent-sales-nobody"); ok {
		t.Error("an unknown client is known")
	}
	if a := p.AgentOf("nightly"); a.ClientID("sales") != "agent-sales-nightly" || a.ConnectionName("sales") != "sales-agent-nightly" || a.ApprovedAt.IsZero() {
		t.Errorf("names: %+v", a)
	}
	_ = time.Now
}
