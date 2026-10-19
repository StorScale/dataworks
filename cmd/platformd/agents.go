package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// A project's agents (use case 12.2): registered by its editors (name, owner,
// the tools of the MCP server's it may use, a schedule), approved by an
// editor other than the one who registered it (an agent that changed what
// approval covered waits for it again), and revoked at once. Approval makes
// the agent's secret (shown once, kept as the project's connection
// <project>-agent-<name>); the operator makes its client from that.

type agentView struct {
	project.Agent
	ClientID   string `json:"clientId"`
	Connection string `json:"connection"`
	Approved   bool   `json:"approved"`
	McpURL     string `json:"mcpUrl"`
	DAG        string `json:"dag,omitempty"` // the pipeline that runs it on its schedule, once approved
}

func (s *server) agentViews(p *project.Project) []agentView {
	out := []agentView{}
	for _, a := range p.Spec.Agents {
		v := agentView{Agent: a, ClientID: a.ClientID(p.Metadata.Name), Connection: a.ConnectionName(p.Metadata.Name),
			Approved: a.Approved(), McpURL: s.cfg.PlatformURL + "/mcp"}
		if a.Runs() {
			v.DAG = a.DAG(p.Metadata.Name)
		}
		out = append(out, v)
	}
	return out
}

// handleAgents lists a project's agents, to its members.
func (s *server) handleAgents(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.agentViews(v.Spec), "tools": project.AgentTools,
		"canEdit": v.Role == project.Editor || s.isAdmin(sess.User)})
}

// handlePutAgent registers an agent, or changes one (which takes its approval away if the change is to what approval covers).
func (s *server) handlePutAgent(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "register its agents")
	if sess == nil {
		return
	}
	var a project.Agent
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		s.fail(w, r, http.StatusBadRequest, "an agent, as JSON: "+err.Error())
		return
	}
	a.Name = r.PathValue("agent")
	if a.Owner == "" {
		a.Owner = sess.User.Username
	}
	p := *v.Spec
	p.Spec.Agents = slices.Clone(p.Spec.Agents)
	i := slices.IndexFunc(p.Spec.Agents, func(x project.Agent) bool { return x.Name == a.Name })
	added := i < 0
	if added {
		p.Spec.Agents = append(p.Spec.Agents, a)
	} else {
		p.Spec.Agents[i] = a
	}
	project.KeepAgentState(v.Spec, &p)
	if ag := p.AgentOf(a.Name); added {
		ag.RegisteredBy, ag.RegisteredAt = sess.User.Username, s.now().UTC()
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "agent.registered", "project:"+v.Name+"/agent:"+a.Name, map[bool]string{true: "registered ", false: "changed "}[added]+a.String())
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, s.agentViews(&p))
}

// handleApproveAgent approves an agent: an editor other than the one who
// registered it. The agent's secret is made here and shown once; the
// operator makes its client from the connection that keeps it.
func (s *server) handleApproveAgent(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "approve its agents")
	if sess == nil {
		return
	}
	name := r.PathValue("agent")
	p := *v.Spec
	p.Spec.Agents = slices.Clone(p.Spec.Agents)
	a := p.AgentOf(name)
	switch {
	case a == nil:
		s.fail(w, r, http.StatusNotFound, "no such agent")
		return
	case a.RegisteredBy == sess.User.Username:
		s.recordRefusal(r, sess.User.Username, "agent.approved", "project:"+v.Name+"/agent:"+name, "approving their own")
		s.fail(w, r, http.StatusForbidden, "an agent is approved by an editor other than the one who registered it")
		return
	case a.Approved():
		writeJSON(w, http.StatusOK, map[string]any{"approved": true, "by": a.ApprovedBy, "at": a.ApprovedAt})
		return
	}
	b := make([]byte, 24)
	rand.Read(b)
	secret := hex.EncodeToString(b)
	conn := store.StoredConnection{ConnectionMeta: store.ConnectionMeta{Name: a.ConnectionName(v.Name), Kind: "agent", AddedBy: sess.User.Username, AddedAt: s.now().UTC()},
		Data: map[string]string{"clientId": a.ClientID(v.Name), "secret": secret}}
	if err := s.store.PutConnection(r.Context(), v.Name, conn); err != nil {
		s.fail(w, r, http.StatusBadGateway, "the agent's secret couldn't be kept: "+err.Error())
		return
	}
	a.ApprovedBy, a.ApprovedAt = sess.User.Username, s.now().UTC()
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "agent.approved", "project:"+v.Name+"/agent:"+name, a.String())
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"approved": true, "by": a.ApprovedBy, "at": a.ApprovedAt,
		"clientId": a.ClientID(v.Name), "secret": secret, "mcpUrl": s.cfg.PlatformURL + "/mcp",
		"note": "The secret is shown once: give it to the agent now. The operator makes its client within a minute."})
}

// handleDeleteAgent revokes an agent: out of the spec, its secret gone; the
// operator removes its client at its next pass, and its token is refused.
func (s *server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "revoke its agents")
	if sess == nil {
		return
	}
	name := r.PathValue("agent")
	p := *v.Spec
	before := len(p.Spec.Agents)
	p.Spec.Agents = slices.DeleteFunc(slices.Clone(p.Spec.Agents), func(x project.Agent) bool { return x.Name == name })
	if len(p.Spec.Agents) == before {
		s.fail(w, r, http.StatusNotFound, "no such agent")
		return
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.store.DeleteConnection(r.Context(), v.Name, v.Name+"-agent-"+name)
	s.record(r, sess.User.Username, "agent.revoked", "project:"+v.Name+"/agent:"+name, "revoked at "+s.now().UTC().Format(time.RFC3339))
	s.kick(r.Context(), v.Name)
	w.WriteHeader(http.StatusNoContent)
}
