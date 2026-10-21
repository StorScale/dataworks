package project

import (
	"slices"
	"strings"
	"testing"
)

func TestAProjectsToolsAndAgentsGrants(t *testing.T) {
	base := `{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},"files":{},"llm":{"budget":5},"pipelines":{"serviceAccount":"airflow-pipelines"}}}`
	p, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.Tools = []ConnectorTool{{Name: "order_status", Description: "An order's status, by its id.", URL: "https://api.example.com/v2",
		Method: "GET", Path: "/orders/{id}", Connection: p.Metadata.Name + "-shop", Parameters: []ToolParameter{{Name: "id", In: "path", Required: true}}}}
	p.Spec.Agents = []Agent{{Name: "triage", Owner: "bob", Prompt: "sum up", Budget: 1},
		{Name: "asker", Owner: "bob", Tools: []string{"list_alerts", "agent:" + p.Metadata.Name + "/triage", "tool:order_status", "agent:other/helper"}}}
	p.Spec.Knowledge = []KnowledgeBank{{Name: "docs", Files: []KnowledgeFiles{{Prefix: "landing/docs/"}}, Schedule: "0 3 * * *"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	agents, tools := p.Spec.Agents[1].AgentGrants()
	if !slices.Equal(agents, []string{p.Metadata.Name + "/triage", "other/helper"}) || !slices.Equal(tools, []string{"order_status"}) {
		t.Errorf("grants: %v %v", agents, tools)
	}
	if got := p.Spec.Agents[1].MCPTools(); !slices.Equal(got, []string{"list_alerts", "list_tools", "ask_agent", "call_tool"}) {
		t.Errorf("MCP tools: %v", got)
	}
	if got := p.Spec.Agents[0].MCPTools(); !slices.Equal(got, AgentTools) {
		t.Errorf("none named: %v", got)
	}
	if uses := p.ConnectionUses(p.Metadata.Name + "-shop"); !slices.Equal(uses, []string{"tool order_status"}) {
		t.Errorf("uses: %v", uses)
	}
	for _, c := range []struct {
		change func(q *Project)
		want   string
	}{
		{func(q *Project) { q.Spec.Tools[0].Path = "/orders/{order}" }, "the path's {order} needs a parameter"},
		{func(q *Project) { q.Spec.Tools[0].URL = "ftp://x" }, "the API's address"},
		{func(q *Project) { q.Spec.Tools[0].Method = "TRACE" }, "GET, POST, PUT, PATCH or DELETE"},
		{func(q *Project) { q.Spec.Tools[0].Connection = "shop" }, "starts with the project's and a dash"},
		{func(q *Project) {
			q.Spec.Tools[0].Parameters = append(q.Spec.Tools[0].Parameters, ToolParameter{Name: "note", In: "body"})
		}, "a GET has no body"},
		{func(q *Project) { q.Spec.Agents[1].Tools = []string{"tool:nope"} }, "the project has no tool nope"},
		{func(q *Project) { q.Spec.Agents[1].Tools = []string{"agent:" + q.Metadata.Name + "/nobody"} }, "the project has no agent nobody"},
		{func(q *Project) { q.Spec.Agents[1].Tools = []string{"agent:" + q.Metadata.Name + "/asker"} }, "doesn't ask itself"},
		{func(q *Project) { q.Spec.Agents[1].Tools = []string{"drop_tables"} }, "no tool \"drop_tables\""},
		{func(q *Project) { q.Spec.Knowledge[0].Files[0].Prefix = "landing/docs" }, "ending in /"},
		{func(q *Project) { q.Spec.Files = nil }, "which it doesn't have (files)"},
		{func(q *Project) { q.Spec.Knowledge[0].Schedule = "nightly" }, "five cron fields"},
		{func(q *Project) { q.Spec.Knowledge[0].Files = nil }, "a folder of files"},
		{func(q *Project) { q.Spec.Pipelines = nil }, "give the project pipelines"},
	} {
		q, _ := Parse([]byte(base))
		q.Spec = p.Spec
		q.Spec.Tools = []ConnectorTool{p.Spec.Tools[0]}
		q.Spec.Tools[0].Parameters = slices.Clone(p.Spec.Tools[0].Parameters)
		q.Spec.Agents = slices.Clone(p.Spec.Agents)
		q.Spec.Knowledge = []KnowledgeBank{{Name: "docs", Files: []KnowledgeFiles{{Prefix: "landing/docs/"}}, Schedule: "0 3 * * *"}}
		c.change(q)
		if err := q.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
}
