package project

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// An Agent of the project's own (use case 12.2): a program that reads the
// project's data through the MCP server with an identity of its own (a
// confidential Keycloak client, agent-<project>-<name>, whose service
// account is one of the project's readers), allowed only the tools named,
// approved by an editor other than the one who registered it before it may
// sign in, and revoked at once by taking it out of the spec.
type Agent struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner"`           // a person: who answers for it
	Tools       []string `json:"tools,omitempty"` // of the MCP server's, by name; none: all a reader may use
	Schedule    string   `json:"schedule,omitempty"`
	// The platform's, kept as they were on each save: who registered it, who
	// approved it (someone else), and when. Changed tools or owner need
	// approval again.
	RegisteredBy string    `json:"registeredBy,omitempty"`
	RegisteredAt time.Time `json:"registeredAt,omitzero"`
	ApprovedBy   string    `json:"approvedBy,omitempty"`
	ApprovedAt   time.Time `json:"approvedAt,omitzero"`
}

// AgentTools are the MCP server's tools an agent may be given.
var AgentTools = []string{"list_projects", "list_metrics", "query_metrics", "explain_query", "list_terms", "describe_term", "search_terms",
	"list_products", "describe_product", "query_product"}

var agentNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// AgentClientID is the Keycloak client of a project's agent.
func AgentClientID(project, agent string) string { return "agent-" + project + "-" + agent }

// ClientID is the agent's Keycloak client, in a project.
func (a Agent) ClientID(project string) string { return AgentClientID(project, a.Name) }

// ConnectionName is the project's connection holding the agent's secret.
func (a Agent) ConnectionName(project string) string { return project + "-agent-" + a.Name }

// Approved says whether the agent may sign in: approved, and not changed since.
func (a Agent) Approved() bool { return a.ApprovedBy != "" }

// Fingerprint is what approval covers: a change to it needs approval again.
func (a Agent) Fingerprint() string {
	return a.Owner + "|" + strings.Join(sorted(a.Tools), ",") + "|" + a.Schedule
}

func (p *Project) agentProblems(bad func(string, ...any)) {
	seen := map[string]bool{}
	for _, a := range p.Spec.Agents {
		switch {
		case !agentNameRE.MatchString(a.Name):
			bad("agents: name %q: lowercase letters, digits and dashes, starting with a letter", a.Name)
		case seen[a.Name]:
			bad("agents: %q twice", a.Name)
		}
		seen[a.Name] = true
		if !principalRE.MatchString(a.Owner) {
			bad("agents: %s: owner %q isn't a person's name", a.Name, a.Owner)
		}
		for _, t := range a.Tools {
			if !slices.Contains(AgentTools, t) {
				bad("agents: %s: no tool %q (one of %s)", a.Name, t, strings.Join(AgentTools, ", "))
			}
		}
		if a.Schedule != "" && !cronRE.MatchString(a.Schedule) {
			bad("agents: %s: schedule %q: five cron fields, as 0 6 * * 1", a.Name, a.Schedule)
		}
		if a.ApprovedBy != "" && a.ApprovedBy == a.RegisteredBy {
			bad("agents: %s: approved by the one who registered it", a.Name)
		}
	}
}

// KeepAgentState keeps what only the platform sets on a project's agents
// (who registered and approved each, when) across a save, and takes an
// approval away when what it covered changed.
func KeepAgentState(before, p *Project) {
	prev := map[string]Agent{}
	if before != nil {
		for _, a := range before.Spec.Agents {
			prev[a.Name] = a
		}
	}
	for i := range p.Spec.Agents {
		a := &p.Spec.Agents[i]
		old, had := prev[a.Name]
		a.RegisteredBy, a.RegisteredAt = old.RegisteredBy, old.RegisteredAt
		a.ApprovedBy, a.ApprovedAt = old.ApprovedBy, old.ApprovedAt
		if had && old.Fingerprint() != a.Fingerprint() {
			a.ApprovedBy, a.ApprovedAt = "", time.Time{}
		}
	}
}

// AgentOf is the project's agent of that name, or nil.
func (p *Project) AgentOf(name string) *Agent {
	for i := range p.Spec.Agents {
		if p.Spec.Agents[i].Name == name {
			return &p.Spec.Agents[i]
		}
	}
	return nil
}

// AgentToolsAllowed are the tools an agent's client may use: those named,
// or all of them when none are; an unknown client gets none.
func (p *Project) AgentToolsAllowed(clientID string) ([]string, bool) {
	for _, a := range p.Spec.Agents {
		if a.ClientID(p.Metadata.Name) == clientID {
			if !a.Approved() {
				return nil, true
			}
			if len(a.Tools) == 0 {
				return slices.Clone(AgentTools), true
			}
			return slices.Clone(a.Tools), true
		}
	}
	return nil, false
}

// String is the agent in words, for the audit log.
func (a Agent) String() string {
	tools := "every tool"
	if len(a.Tools) > 0 {
		tools = strings.Join(a.Tools, ", ")
	}
	return fmt.Sprintf("%s (owner %s; %s)", a.Name, a.Owner, tools)
}
