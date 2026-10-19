package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/StorScale/storscale-platform/internal/project"
)

// The LLM gateway, as the platform shows it (use case 12.1): a project's
// budget and spend, and each agent's, read from the gateway by the keys
// the operator kept as the project's connections. At the budget the gateway
// stops answering the key, and the project is alerted.

type llmUse struct {
	Who    string   `json:"who"` // the project, or an agent's name
	Budget float64  `json:"budget"`
	Spend  float64  `json:"spend"`
	Key    bool     `json:"key"`             // the gateway has a key for it
	Error  string   `json:"error,omitempty"` // why the spend couldn't be read
	Share  *float64 `json:"share,omitempty"` // spend over budget
}

// llmUses reads a project's gateway keys' spend.
func (s *server) llmUses(ctx context.Context, p *project.Project) []llmUse {
	out := []llmUse{}
	if s.gateway == nil || s.store == nil {
		return out
	}
	conns, err := s.store.Connections(ctx, p.Metadata.Name)
	if err != nil {
		return out
	}
	byName := map[string]map[string]string{}
	for _, c := range conns {
		byName[c.Name] = c.Data
	}
	read := func(who, conn string, budget float64) {
		u := llmUse{Who: who, Budget: budget}
		data := byName[conn]
		if data == nil || data["llmKey"] == "" {
			out = append(out, u)
			return
		}
		u.Key = true
		info, err := s.gateway.Info(ctx, data["llmKey"])
		if err != nil {
			u.Error = err.Error()
		} else {
			u.Spend = info.Spend
			if info.MaxBudget != nil && *info.MaxBudget > 0 {
				u.Budget = *info.MaxBudget
			}
			if u.Budget > 0 {
				share := u.Spend / u.Budget
				u.Share = &share
			}
		}
		out = append(out, u)
	}
	if l := p.Spec.LLM; l != nil && l.Budget > 0 {
		read(p.Metadata.Name, p.Metadata.Name+"-llm", l.Budget)
	}
	for _, a := range p.Spec.Agents {
		if a.Budget > 0 {
			read(a.Name, a.ConnectionName(p.Metadata.Name), a.Budget)
		}
	}
	return out
}

// handleLLM is a project's gateway use, to its members.
func (s *server) handleLLM(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	url := ""
	if s.gateway != nil {
		url = s.gateway.URL
	}
	writeJSON(w, http.StatusOK, map[string]any{"gateway": url, "uses": s.llmUses(r.Context(), v.Spec)})
}

// llmIssues are a project's keys at or near their budget.
func (s *server) llmIssues(ctx context.Context, p *project.Project, link string) []issue {
	var out []issue
	for _, u := range s.llmUses(ctx, p) {
		if u.Share == nil {
			continue
		}
		key := "llm/" + p.Metadata.Name
		who := "the project"
		if u.Who != p.Metadata.Name {
			key, who = "agent/"+p.Metadata.Name+"/"+u.Who, "the agent "+u.Who
		}
		switch {
		case *u.Share >= 1:
			out = append(out, issue{Key: key, Kind: "llm", Title: fmt.Sprintf("%s is at its LLM budget (%.2f of %.2f): the gateway answers it no more this month", who, u.Spend, u.Budget),
				Detail: "Raise the budget on the project's settings, or wait for the month.", Link: link + "/agents"})
		case *u.Share >= 0.8:
			out = append(out, issue{Key: key + "/near", Kind: "llm", Title: fmt.Sprintf("%s has used %.0f%% of its LLM budget (%.2f of %.2f)", who, *u.Share*100, u.Spend, u.Budget),
				Link: link + "/agents"})
		}
	}
	return out
}
