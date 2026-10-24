package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// A project's connections (use case 7.6): the credentials its editors give
// the platform once, kept as the project's (a labelled Secret, or in the
// platform's bucket), which its spec names. Members see each one's name,
// kind, who added it and when, its values that aren't secret, and where
// it's used; never a secret value.

type connectionView struct {
	store.ConnectionMeta
	Values map[string]string `json:"values"` // those that aren't secret
	Set    []string          `json:"set"`    // the secret keys that have a value
	UsedBy []string          `json:"usedBy"`
}

func view(p *project.Project, c store.StoredConnection) connectionView {
	v := connectionView{ConnectionMeta: c.ConnectionMeta, Values: map[string]string{}, Set: []string{}, UsedBy: p.ConnectionUses(c.Name)}
	if v.UsedBy == nil {
		v.UsedBy = []string{}
	}
	k := project.KindOf(c.Kind)
	for key, val := range c.Data {
		public := k != nil && slices.ContainsFunc(k.Fields, func(f project.ConnectionField) bool { return f.Key == key && !f.Secret })
		if public {
			v.Values[key] = val
		} else if val != "" {
			v.Set = append(v.Set, key) // made outside the platform, or a secret: only that it's there
		}
	}
	sort.Strings(v.Set)
	return v
}

// handleConnections is a project's connections, to its members.
func (s *server) handleConnections(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	cs, err := s.store.Connections(r.Context(), v.Name)
	if err != nil {
		s.log.Error("connections", "project", v.Name, "error", err)
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	out := []connectionView{}
	for _, c := range cs {
		out = append(out, view(v.Spec, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out, "kinds": project.ConnectionKinds,
		"canEdit": v.can(project.PermConnections)})
}

// editorOf is the signed-in editor (or administrator) of the request's
// project, for a change; otherwise it answers.
func (s *server) editorOf(w http.ResponseWriter, r *http.Request, perm, what string) (*session, *projectView) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return nil, nil
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return nil, nil
	}
	v := s.project(w, r, sess)
	if v == nil {
		return nil, nil
	}
	if !v.can(perm) {
		s.fail(w, r, http.StatusForbidden, "the project's editors "+what+" (it takes the permission "+perm+")")
		return nil, nil
	}
	return sess, v
}

// handlePutConnection adds a connection (POST, a new name: the project's
// and a dash first) or gives an existing one new values (PUT: a value left
// empty stays as it was; rotating a token is pasting the new one).
func (s *server) handlePutConnection(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermConnections, "change its connections")
	if sess == nil {
		return
	}
	var body struct {
		Name   string            `json:"name"`
		Kind   string            `json:"kind"`
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	adding := r.Method == http.MethodPost
	if !adding {
		body.Name = r.PathValue("connection")
	}
	cs, err := s.store.Connections(r.Context(), v.Name)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	i := slices.IndexFunc(cs, func(c store.StoredConnection) bool { return c.Name == body.Name })
	who, now := sess.User.Username, s.now().UTC()
	var c store.StoredConnection
	switch {
	case adding && i >= 0:
		s.fail(w, r, http.StatusConflict, "the project has a connection "+body.Name+" already: give it new values")
		return
	case adding:
		if why := project.ConnectionNameProblem(v.Name, body.Name); why != "" {
			s.fail(w, r, http.StatusUnprocessableEntity, why)
			return
		}
		c = store.StoredConnection{ConnectionMeta: store.ConnectionMeta{Name: body.Name, Kind: body.Kind, AddedBy: who, AddedAt: now}, Data: map[string]string{}}
	case i < 0:
		s.fail(w, r, http.StatusNotFound, "no such connection")
		return
	default:
		c = cs[i]
		if c.Kind == "" { // made outside the platform: its kind given now
			c.Kind = body.Kind
		}
		c.ChangedBy, c.ChangedAt = who, now
	}
	k := project.KindOf(c.Kind)
	if k == nil {
		var kinds []string
		for _, k := range project.ConnectionKinds {
			kinds = append(kinds, k.Kind)
		}
		s.fail(w, r, http.StatusUnprocessableEntity, "kind: one of "+strings.Join(kinds, ", "))
		return
	}
	data := map[string]string{}
	for key, val := range c.Data {
		data[key] = val
	}
	var changed []string
	for key, val := range body.Values {
		f := slices.IndexFunc(k.Fields, func(f project.ConnectionField) bool { return f.Key == key })
		if f >= 0 && k.Fields[f].Secret && val == "" {
			continue // a secret left empty: as it was
		}
		if val == "" {
			delete(data, key)
		} else {
			data[key] = strings.TrimRight(val, "\r\n") + map[bool]string{true: "\n", false: ""}[f >= 0 && k.Fields[f].Multi]
		}
		changed = append(changed, key)
	}
	if problems := k.ValuesProblems(data); len(problems) > 0 {
		s.fail(w, r, http.StatusUnprocessableEntity, strings.Join(problems, "; "))
		return
	}
	c.Data = data
	if err := s.store.PutConnection(r.Context(), v.Name, c); err != nil {
		if errors.Is(err, store.ErrNotTheProjects) {
			s.recordRefusal(r, who, "connection.saved", "project:"+v.Name+"/connection:"+c.Name, "not the project's")
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		s.log.Error("saving a connection", "project", v.Name, "connection", c.Name, "error", err)
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	sort.Strings(changed)
	detail := k.Label + "; " + map[bool]string{true: "added", false: "changed"}[adding] + ": " + strings.Join(changed, ", ")
	s.record(r, who, "connection.saved", "project:"+v.Name+"/connection:"+c.Name, detail)
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, view(v.Spec, c))
}

// handleDeleteConnection removes a connection the project's spec doesn't name.
func (s *server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermConnections, "remove its connections")
	if sess == nil {
		return
	}
	name := r.PathValue("connection")
	if uses := v.Spec.ConnectionUses(name); len(uses) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "still used by " + strings.Join(uses, ", "), "usedBy": uses})
		return
	}
	cs, err := s.store.Connections(r.Context(), v.Name)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	if !slices.ContainsFunc(cs, func(c store.StoredConnection) bool { return c.Name == name }) {
		s.fail(w, r, http.StatusNotFound, "no such connection")
		return
	}
	if err := s.store.DeleteConnection(r.Context(), v.Name, name); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "connection.removed", "project:"+v.Name+"/connection:"+name, "")
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

// kick asks the operator to apply a project now (its connections changed:
// Airflow's copies of them, Trino's catalogs, the copies' pods), not at its
// next pass.
func (s *server) kick(ctx context.Context, name string) {
	if s.cfg.OperatorURL == "" {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", s.cfg.OperatorURL+"/projects/"+name+"/kick", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.log.Warn("the operator, kicked", "project", name, "error", err)
		return
	}
	res.Body.Close()
}
