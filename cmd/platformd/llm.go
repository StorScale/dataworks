package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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

// llmCall is one call of a project's keys, as the Model calls card shows it.
type llmCall struct {
	At      time.Time `json:"at"`
	Who     string    `json:"who"`  // the project, or an agent's name: whose key
	User    string    `json:"user"` // who asked, when the caller said (a person, an agent's account)
	Kind    string    `json:"kind"` // a chat, or embeddings
	Model   string    `json:"model"`
	Tokens  int       `json:"tokens"`
	Cost    float64   `json:"cost"`
	Seconds float64   `json:"seconds"`
	Status  string    `json:"status"` // success, failure
}

// handleLLMCalls are a project's calls of the gateway, by its keys, over the
// last days (default 7, up to 31): each call's time, whose key, who asked,
// the model, tokens, cost, time taken and outcome, and their totals by day,
// person and model. To its editors and administrators: never the question or answer.
func (s *server) handleLLMCalls(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	if role != "editor" {
		s.fail(w, r, http.StatusForbidden, "the project's editors see its model calls")
		return
	}
	if s.gateway == nil || s.store == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform has no LLM gateway")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 31 {
		days = 7
	}
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -(days - 1))
	conns, err := s.store.Connections(r.Context(), p.Metadata.Name)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	keys := map[string]string{} // whose -> key
	for _, c := range conns {
		if k := c.Data["llmKey"]; k != "" {
			whose := p.Metadata.Name
			for _, a := range p.Spec.Agents {
				if c.Name == a.ConnectionName(p.Metadata.Name) {
					whose = a.Name
				}
			}
			keys[whose] = k
		}
	}
	calls := []llmCall{}
	var errs []string
	for whose, key := range keys {
		cs, err := s.gateway.Calls(r.Context(), key, from, to)
		if err != nil {
			errs = append(errs, whose+": "+err.Error())
			continue
		}
		for _, c := range cs {
			kind := "chat"
			if strings.Contains(c.CallType, "embedding") {
				kind = "embeddings"
			}
			model := c.ModelGroup
			if model == "" {
				model = c.Model
			}
			status := c.Status
			if status == "" {
				status = "success"
			}
			calls = append(calls, llmCall{At: c.StartTime, Who: whose, User: c.User, Kind: kind, Model: model, Tokens: c.TotalTokens,
				Cost: c.Spend, Seconds: c.EndTime.Sub(c.StartTime).Seconds(), Status: status})
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].At.After(calls[j].At) })
	type total struct {
		Calls  int     `json:"calls"`
		Tokens int     `json:"tokens"`
		Cost   float64 `json:"cost"`
	}
	sum := func(key func(llmCall) string) map[string]*total {
		out := map[string]*total{}
		for _, c := range calls {
			k := key(c)
			if out[k] == nil {
				out[k] = &total{}
			}
			out[k].Calls++
			out[k].Tokens += c.Tokens
			out[k].Cost += c.Cost
		}
		return out
	}
	all := sum(func(llmCall) string { return "all" })["all"]
	if all == nil {
		all = &total{}
	}
	recent := calls
	if len(recent) > 200 {
		recent = recent[:200]
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "total": all, "calls": recent, "errors": errs,
		"byDay":   sum(func(c llmCall) string { return c.At.Format("2006-01-02") }),
		"byUser":  sum(func(c llmCall) string { return c.User }),
		"byModel": sum(func(c llmCall) string { return c.Model }),
		"byWho":   sum(func(c llmCall) string { return c.Who })})
}
