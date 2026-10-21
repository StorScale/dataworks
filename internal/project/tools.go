package project

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// A ConnectorTool is a tool of the project's own (use case 12.7, R12.17):
// one operation of an API (as an OpenAPI operation: a method, a path, its
// parameters), signed in by one of the project's connections (an API
// token, sent as a Bearer), which the platform calls for the agents given
// it (tool:<name>) and the project's editors, auditing each call. The
// token stays the platform's: the agent sees the tool, never the token.
type ConnectorTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	URL         string          `json:"url"`    // the API's address, as https://api.example.com/v2
	Method      string          `json:"method"` // GET, POST, PUT, PATCH or DELETE
	Path        string          `json:"path"`   // under the URL, its path parameters in braces: /orders/{id}
	Connection  string          `json:"connection,omitempty"`
	Parameters  []ToolParameter `json:"parameters,omitempty"`
}

// A ToolParameter is one of a tool's inputs: in the path, the query or the JSON body.
type ToolParameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`             // path, query or body
	Type        string `json:"type,omitempty"` // string (default), integer, number or boolean
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

var (
	toolNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	toolParamRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	pathParamRE = regexp.MustCompile(`\{([^{}]*)\}`)
	// An agent's grant of another agent (agent:<project>/<name>) or of a tool of its project's (tool:<name>).
	agentGrantRE = regexp.MustCompile(`^agent:([a-z][a-z0-9-]{2,39})/([a-z][a-z0-9-]{0,30})$`)
	toolGrantRE  = regexp.MustCompile(`^tool:([a-z][a-z0-9_]{0,40})$`)
)

// ToolOf is the project's tool of that name, or nil.
func (p *Project) ToolOf(name string) *ConnectorTool {
	for i := range p.Spec.Tools {
		if p.Spec.Tools[i].Name == name {
			return &p.Spec.Tools[i]
		}
	}
	return nil
}

// PathParams are the names in braces in the tool's path.
func (t ConnectorTool) PathParams() []string {
	var out []string
	for _, m := range pathParamRE.FindAllStringSubmatch(t.Path, -1) {
		out = append(out, m[1])
	}
	return out
}

func (p *Project) checkTools(bad func(format string, a ...any)) {
	seen := map[string]bool{}
	for i, t := range p.Spec.Tools {
		at := fmt.Sprintf("tools %d", i+1)
		if toolNameRE.MatchString(t.Name) {
			at = "tools: " + t.Name
		}
		switch {
		case !toolNameRE.MatchString(t.Name):
			bad("%s: a tool's name is lowercase letters, digits and underscores, starting with a letter: %q isn't", at, t.Name)
		case seen[t.Name]:
			bad("tools: two named %s", t.Name)
		}
		seen[t.Name] = true
		if strings.TrimSpace(t.Description) == "" {
			bad("%s: a description, so an agent knows when to use it", at)
		}
		if u, err := url.Parse(t.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.User != nil {
			bad("%s: url %q: the API's address, as https://api.example.com/v2", at, t.URL)
		}
		if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, t.Method) {
			bad("%s: method %q: GET, POST, PUT, PATCH or DELETE", at, t.Method)
		}
		if !strings.HasPrefix(t.Path, "/") || strings.Contains(t.Path, "..") || strings.ContainsAny(t.Path, "?# ") {
			bad("%s: path %q: under the url, starting with /, as /orders/{id}", at, t.Path)
		}
		if t.Connection != "" {
			if problem := ConnectionNameProblem(p.Metadata.Name, t.Connection); problem != "" {
				bad("%s: connection: %s", at, problem)
			}
		}
		names := map[string]string{}
		for _, prm := range t.Parameters {
			switch {
			case !toolParamRE.MatchString(prm.Name):
				bad("%s: parameter %q: letters, digits and underscores", at, prm.Name)
			case names[prm.Name] != "":
				bad("%s: parameter %s twice", at, prm.Name)
			case !slices.Contains([]string{"path", "query", "body"}, prm.In):
				bad("%s: parameter %s: in path, query or body, not %q", at, prm.Name, prm.In)
			case !slices.Contains([]string{"", "string", "integer", "number", "boolean"}, prm.Type):
				bad("%s: parameter %s: a string, integer, number or boolean, not %q", at, prm.Name, prm.Type)
			case prm.In == "body" && (t.Method == "GET" || t.Method == "DELETE"):
				bad("%s: parameter %s: a %s has no body", at, prm.Name, t.Method)
			}
			names[prm.Name] = prm.In
		}
		for _, n := range t.PathParams() {
			if names[n] != "path" {
				bad("%s: the path's {%s} needs a parameter of that name, in path", at, n)
			}
		}
		for n, in := range names {
			if in == "path" && !slices.Contains(t.PathParams(), n) {
				bad("%s: parameter %s is in path, but the path has no {%s}", at, n, n)
			}
		}
	}
}

// AgentGrants are an agent's grants beyond the MCP server's tools: the agents
// it may ask (project/name), and its project's tools it may call.
func (a Agent) AgentGrants() (agents, tools []string) {
	for _, t := range a.Tools {
		if m := agentGrantRE.FindStringSubmatch(t); m != nil {
			agents = append(agents, m[1]+"/"+m[2])
		} else if m := toolGrantRE.FindStringSubmatch(t); m != nil {
			tools = append(tools, m[1])
		}
	}
	return agents, tools
}

// agentToolProblem is what's wrong with a tool given an agent, or "": it may be: the MCP
// server's, another agent (agent:<project>/<name>), or a tool of the
// project's own (tool:<name>), the project's being checked here.
func (p *Project) agentToolProblem(a Agent, t string) string {
	if slices.Contains(AgentTools, t) {
		return ""
	}
	if m := agentGrantRE.FindStringSubmatch(t); m != nil {
		if m[1] == p.Metadata.Name && m[2] == a.Name {
			return "an agent doesn't ask itself"
		}
		if m[1] == p.Metadata.Name && p.AgentOf(m[2]) == nil {
			return "the project has no agent " + m[2]
		}
		return ""
	}
	if m := toolGrantRE.FindStringSubmatch(t); m != nil {
		if p.ToolOf(m[1]) == nil {
			return "the project has no tool " + m[1] + " (tools)"
		}
		return ""
	}
	return fmt.Sprintf("no tool %q (one of %s; or agent:<project>/<name>, or tool:<name> of the project's tools)", t, strings.Join(AgentTools, ", "))
}
