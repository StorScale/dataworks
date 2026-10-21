package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
)

// A project's own tools, and its agents as tools (use case 12.7, R12.16,
// R12.17). The MCP server's list_tools, call_tool and ask_agent come here
// with the caller's own token (an agent's, or a person's assistant's):
//
//   - a tool of the project's is an API's operation, signed in by one of its
//     connections; the platform calls it, so the token never reaches the
//     agent. An agent given it (tool:<name>) or an editor may call it.
//   - an agent asked is an approved agent with a prompt: the platform runs
//     its loop (as the runner does on its schedule) as that agent, its
//     client and its gateway key, with the task given; an agent given it
//     (agent:<project>/<name>) or a member of its project may ask. An
//     agent so asked isn't given ask_agent itself, so asking can't loop.
//
// Each call is in the audit log, under the caller, naming the agent asked.

const (
	toolTimeout     = 30 * time.Second
	toolAnswerMax   = 1 << 20 // bytes of an API's answer read
	toolAnswerChars = 20000   // characters given back
	askMaxSteps     = 8
)

// callerAgent is the project's agent a token's user is, if it's one
// (service-account-agent-<project>-<name>), or nil.
func callerAgent(p *project.Project, who string) *project.Agent {
	client, ok := strings.CutPrefix(who, "service-account-")
	if !ok {
		return nil
	}
	for i := range p.Spec.Agents {
		if p.Spec.Agents[i].ClientID(p.Metadata.Name) == client {
			return &p.Spec.Agents[i]
		}
	}
	return nil
}

// agentByUser is the agent (and its project) a token's user is, across projects; nil for a person.
func (s *server) agentByUser(ctx context.Context, who string) (*project.Project, *project.Agent) {
	if !strings.HasPrefix(who, "service-account-agent-") {
		return nil, nil
	}
	specs, err := s.store.Projects(ctx)
	if err != nil {
		return nil, nil
	}
	for _, p := range specs {
		if a := callerAgent(p, who); a != nil {
			return p, a
		}
	}
	return nil, nil
}

type toolView struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Method      string                  `json:"method"`
	Parameters  []project.ToolParameter `json:"parameters"`
}

type askableView struct {
	Project     string `json:"project"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// mayCallTool says whether the caller may call the project's tool: an agent
// given it, or an editor.
func mayCallTool(caller *project.Agent, role, tool string) bool {
	if caller != nil {
		_, tools := caller.AgentGrants()
		return slices.Contains(tools, tool)
	}
	return role == project.Editor
}

// mayAsk says whether the caller may ask the project's agent: an agent given
// it, or a person who's a member (projectRole has checked that).
func mayAsk(caller *project.Agent, projectName, agent string) bool {
	if caller != nil {
		agents, _ := caller.AgentGrants()
		return slices.Contains(agents, projectName+"/"+agent)
	}
	return true
}

func askable(a project.Agent) bool { return a.Approved() && a.Prompt != "" && a.Budget > 0 }

// handleListTools is what the caller may call and ask in a project.
func (s *server) handleListTools(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	_, caller := s.agentByUser(r.Context(), who)
	tools, agents := []toolView{}, []askableView{}
	for _, t := range p.Spec.Tools {
		if mayCallTool(caller, role, t.Name) {
			params := t.Parameters
			if params == nil {
				params = []project.ToolParameter{}
			}
			tools = append(tools, toolView{Name: t.Name, Description: t.Description, Method: t.Method, Parameters: params})
		}
	}
	for _, a := range p.Spec.Agents {
		if askable(a) && mayAsk(caller, p.Metadata.Name, a.Name) && (caller == nil || caller.Name != a.Name) {
			agents = append(agents, askableView{Project: p.Metadata.Name, Name: a.Name, Description: a.Description})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p.Metadata.Name, "tools": tools, "agents": agents})
}

// handleCallTool calls a tool of the project's with the arguments given, as the platform, signed in by its connection.
func (s *server) handleCallTool(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("tool")
	t := p.ToolOf(name)
	resource := "project:" + p.Metadata.Name + "/tool:" + name
	if t == nil {
		s.fail(w, r, http.StatusNotFound, "the project has no tool "+name)
		return
	}
	_, caller := s.agentByUser(r.Context(), who)
	if !mayCallTool(caller, role, name) {
		why := "the project's editors, and the agents given it (tool:" + name + "), call its tools"
		s.recordRefusal(r, who, "tool.call", resource, why)
		s.fail(w, r, http.StatusForbidden, why)
		return
	}
	var in struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil {
		s.fail(w, r, http.StatusBadRequest, "the arguments, as JSON: {\"arguments\": {...}}")
		return
	}
	var secret map[string]string
	if t.Connection != "" {
		c, err := s.store.Connection(r.Context(), p.Metadata.Name, t.Connection)
		if err != nil || c == nil || c["token"] == "" {
			s.fail(w, r, http.StatusUnprocessableEntity, "the tool's connection "+t.Connection+" isn't kept yet: add it on the Connections tab (an API token)")
			return
		}
		secret = c
	}
	req, err := toolRequest(r.Context(), *t, in.Arguments)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if secret != nil {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(secret["token"]))
	}
	started := time.Now()
	resp, err := (&http.Client{Timeout: toolTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		s.record(r, who, "tool.called", resource, "failed: "+err.Error())
		s.fail(w, r, http.StatusBadGateway, "the API didn't answer: "+err.Error())
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, toolAnswerMax))
	var answer any = string(data)
	switch {
	case len(data) > toolAnswerChars: // cut, it's text
		answer = string(data[:toolAnswerChars]) + "…"
	case json.Valid(data):
		answer = json.RawMessage(data)
	}
	s.record(r, who, "tool.called", resource, fmt.Sprintf("%s %s: HTTP %d in %.1fs", t.Method, req.URL.Path, resp.StatusCode, time.Since(started).Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"tool": name, "status": resp.StatusCode, "answer": answer})
}

// toolRequest is the HTTP request a tool's call makes: the path's parameters
// put in it (escaped), the query's added, the body's sent as a JSON object.
func toolRequest(ctx context.Context, t project.ConnectorTool, args map[string]any) (*http.Request, error) {
	path := t.Path
	query := url.Values{}
	body := map[string]any{}
	for _, prm := range t.Parameters {
		v, has := args[prm.Name]
		if !has || v == nil {
			if prm.Required {
				return nil, fmt.Errorf("the tool needs %s", prm.Name)
			}
			continue
		}
		str := fmt.Sprint(v)
		switch prm.Type {
		case "integer":
			if _, err := strconv.ParseInt(str, 10, 64); err != nil {
				return nil, fmt.Errorf("%s is a whole number", prm.Name)
			}
		case "number":
			if _, err := strconv.ParseFloat(str, 64); err != nil {
				return nil, fmt.Errorf("%s is a number", prm.Name)
			}
		case "boolean":
			if _, err := strconv.ParseBool(str); err != nil {
				return nil, fmt.Errorf("%s is true or false", prm.Name)
			}
		}
		switch prm.In {
		case "path":
			if str == "" {
				return nil, fmt.Errorf("the tool needs %s", prm.Name)
			}
			path = strings.ReplaceAll(path, "{"+prm.Name+"}", url.PathEscape(str))
		case "query":
			query.Set(prm.Name, str)
		case "body":
			body[prm.Name] = v
		}
	}
	for k := range args {
		if !slices.ContainsFunc(t.Parameters, func(p project.ToolParameter) bool { return p.Name == k }) {
			return nil, fmt.Errorf("the tool has no parameter %s", k)
		}
	}
	u := strings.TrimRight(t.URL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if slices.ContainsFunc(t.Parameters, func(p project.ToolParameter) bool { return p.In == "body" }) {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, t.Method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// handleAskAgent has an agent of the project's do a task, as itself, and answers with its report.
func (s *server) handleAskAgent(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("agent")
	resource := "project:" + p.Metadata.Name + "/agent:" + name
	a := p.AgentOf(name)
	if a == nil || !askable(*a) {
		s.fail(w, r, http.StatusNotFound, "the project has no agent "+name+" to ask: an approved agent with a prompt and a budget")
		return
	}
	_, caller := s.agentByUser(r.Context(), who)
	if !mayAsk(caller, p.Metadata.Name, name) || (caller != nil && caller.Name == name) {
		why := "an agent asks only the agents it's given (agent:" + p.Metadata.Name + "/" + name + ")"
		s.recordRefusal(r, who, "agent.ask", resource, why)
		s.fail(w, r, http.StatusForbidden, why)
		return
	}
	var in struct {
		Task string `json:"task"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Task) == "" {
		s.fail(w, r, http.StatusBadRequest, "the task, as JSON: {\"task\": ...}")
		return
	}
	sec, err := s.store.Connection(r.Context(), p.Metadata.Name, a.ConnectionName(p.Metadata.Name))
	if err != nil || sec == nil || sec["secret"] == "" || sec["llmKey"] == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "the agent "+name+"'s client or gateway key isn't kept yet")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	out, err := s.runAgent(ctx, p, *a, sec, who, in.Task)
	detail := fmt.Sprintf("asked as %s by %s: %d tool calls, %.4f spent", "service-account-"+a.ClientID(p.Metadata.Name), who, len(out.Steps), out.Cost)
	if err != nil {
		s.record(r, who, "agent.asked", resource, detail+"; stopped: "+err.Error())
		s.fail(w, r, http.StatusUnprocessableEntity, "the agent "+name+": "+err.Error())
		return
	}
	s.record(r, who, "agent.asked", resource, detail)
	writeJSON(w, http.StatusOK, out)
}

type askStep struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Refused   bool           `json:"refused,omitempty"`
}

type askAnswer struct {
	Agent  string    `json:"agent"`
	Report string    `json:"report"`
	Steps  []askStep `json:"steps"`
	Cost   float64   `json:"cost"`
}

// runAgent is the runner's loop (stack/airflow/image/storscale/agent.py), as
// the agent: its tools from the MCP server by its own token, less ask_agent;
// the model through the gateway on its key; each tool it asks for called as
// it; until it reports.
func (s *server) runAgent(ctx context.Context, p *project.Project, a project.Agent, sec map[string]string, asker, task string) (*askAnswer, error) {
	out := &askAnswer{Agent: p.Metadata.Name + "/" + a.Name, Steps: []askStep{}}
	if s.cfg.SemanticURL == "" {
		return out, errors.New("the MCP server isn't set up here")
	}
	tok, err := s.clientToken(ctx, sec["clientId"], sec["secret"])
	if err != nil {
		return out, err
	}
	mcp := &mcpClient{url: s.cfg.SemanticURL + "/mcp", token: tok}
	if _, err := mcp.rpc(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "storscale-platformd", "version": "1"}}); err != nil {
		return out, err
	}
	listed, err := mcp.rpc(ctx, "tools/list", nil)
	if err != nil {
		return out, err
	}
	var tools []map[string]any
	var names []string
	if ts, ok := listed["tools"].([]any); ok {
		allowed := a.MCPTools()
		for _, x := range ts {
			t, _ := x.(map[string]any)
			n, _ := t["name"].(string)
			if n == "" || n == "ask_agent" || !slices.Contains(allowed, n) {
				continue
			}
			params := t["inputSchema"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": n, "description": t["description"], "parameters": params}})
			names = append(names, n)
		}
	}
	system := "You are the agent " + a.Name + " of the project " + p.Metadata.Name
	if a.Description != "" {
		system += ": " + a.Description
	}
	system += ". " + asker + " asks you to do a task, within what you're for:\n\n" + a.Prompt +
		"\n\nUse the tools to do it; each answers as you, under the project's rules. When you're done, answer with your report alone."
	messages := []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": task}}
	gw := &flow.GatewayAsker{URL: sec["llmUrl"], Key: sec["llmKey"], User: "service-account-" + a.ClientID(p.Metadata.Name)}
	for range askMaxSteps {
		msg, cost, err := gw.Chat(ctx, messages, tools)
		out.Cost += cost
		if err != nil {
			return out, err
		}
		calls, _ := msg["tool_calls"].([]any)
		if len(calls) == 0 {
			out.Report, _ = msg["content"].(string)
			out.Report = strings.TrimSpace(out.Report)
			return out, nil
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": msg["content"], "tool_calls": calls})
		for _, x := range calls {
			c, _ := x.(map[string]any)
			fn, _ := c["function"].(map[string]any)
			tool, _ := fn["name"].(string)
			args := map[string]any{}
			if raw, _ := fn["arguments"].(string); raw != "" {
				_ = json.Unmarshal([]byte(raw), &args)
			}
			text, refused := "", false
			if !slices.Contains(names, tool) {
				text, refused = "no tool "+tool+": the agent may use "+strings.Join(names, ", "), true
			} else {
				res, err := mcp.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
				if err != nil {
					text, refused = err.Error(), true
				} else {
					text, refused = mcpText(res)
				}
			}
			out.Steps = append(out.Steps, askStep{Tool: tool, Arguments: args, Refused: refused})
			if len(text) > toolAnswerChars {
				text = text[:toolAnswerChars]
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": c["id"], "content": text})
		}
	}
	return out, fmt.Errorf("no report after %d rounds of tools", askMaxSteps)
}

// clientToken is a Keycloak token for a client, by its secret (client credentials).
func (s *server) clientToken(ctx context.Context, client, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {secret}}
	req, _ := http.NewRequestWithContext(ctx, "POST", s.cfg.direct()+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Keycloak: %w", err)
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&t) != nil || t.AccessToken == "" {
		return "", fmt.Errorf("the agent couldn't sign in (Keycloak: HTTP %d)", resp.StatusCode)
	}
	return t.AccessToken, nil
}

// mcpClient speaks to the MCP server (streamable HTTP, JSON answers) with a token.
type mcpClient struct {
	url, token string
	n          int
}

func (m *mcpClient) rpc(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	m.n++
	in := map[string]any{"jsonrpc": "2.0", "id": m.n, "method": method}
	if params != nil {
		in["params"] = params
	}
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, "POST", m.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+m.token)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the MCP server: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("the MCP server: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	if i := bytes.Index(data, []byte("data: ")); i >= 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data = bytes.TrimSpace(data[i+6:])
	}
	var out struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("the MCP server's answer: %w", err)
	}
	if out.Error != nil {
		return nil, errors.New(out.Error.Message)
	}
	return out.Result, nil
}

// mcpText is a tool call's answer as text (its structured content as JSON when it has some), and whether it's a refusal.
func mcpText(res map[string]any) (string, bool) {
	refused, _ := res["isError"].(bool)
	if sc, ok := res["structuredContent"]; ok && sc != nil {
		b, _ := json.Marshal(sc)
		return string(b), refused
	}
	var parts []string
	if cs, ok := res["content"].([]any); ok {
		for _, c := range cs {
			if m, ok := c.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	return strings.Join(parts, " "), refused
}
