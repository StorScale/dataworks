package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// The assistants the company approved to use the platform over MCP (use case
// 10.4): registered by administrators, kept at assistants/<name>.json, and
// given a Keycloak client by the operator.

func assistantKey(name string) string { return "assistants/" + name + ".json" }

type assistantView struct {
	project.Assistant
	ClientID string `json:"clientId"`
	MCPURL   string `json:"mcpUrl"`
}

func (s *server) assistantView(a project.Assistant) assistantView {
	return assistantView{a, a.ClientID(), strings.TrimRight(s.cfg.PlatformURL, "/") + "/mcp"}
}

// administrator is the signed-in administrator, or it answers.
func (s *server) administrator(w http.ResponseWriter, r *http.Request, what string) *session {
	sess := s.signedIn(w, r)
	if sess == nil {
		return nil
	}
	if !s.isAdmin(sess.User) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrators " + what})
		return nil
	}
	return sess
}

func (s *server) assistants(r *http.Request) ([]project.Assistant, error) {
	keys, err := s.files.ListFiles(r.Context(), "assistants/")
	if err != nil {
		return nil, err
	}
	out := []project.Assistant{}
	for _, k := range keys {
		b, err := s.files.GetFile(r.Context(), k)
		if err != nil {
			return nil, err
		}
		var a project.Assistant
		if json.Unmarshal(b, &a) == nil && a.Name != "" {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *server) handleAssistants(w http.ResponseWriter, r *http.Request) {
	if s.administrator(w, r, "see the registered assistants") == nil {
		return
	}
	if s.files == nil {
		writeJSON(w, http.StatusOK, map[string]any{"assistants": []assistantView{}, "available": false})
		return
	}
	list, err := s.assistants(r)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the platform store isn't answering"})
		return
	}
	out := []assistantView{}
	for _, a := range list {
		out = append(out, s.assistantView(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"assistants": out, "available": true})
}

func (s *server) handlePutAssistant(w http.ResponseWriter, r *http.Request) {
	sess := s.administrator(w, r, "register assistants")
	if sess == nil {
		return
	}
	if s.files == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this install has no file store for assistants"})
		return
	}
	var a project.Assistant
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.Name = r.PathValue("name")
	if err := a.Check(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	a.AddedBy, a.AddedAt = sess.User.Username, time.Now().UTC()
	if b, err := s.files.GetFile(r.Context(), assistantKey(a.Name)); err == nil && b != nil {
		var was project.Assistant
		if json.Unmarshal(b, &was) == nil && was.AddedBy != "" {
			a.AddedBy, a.AddedAt = was.AddedBy, was.AddedAt
		}
	}
	b, _ := json.Marshal(a)
	if err := s.files.PutFile(r.Context(), assistantKey(a.Name), b, "application/json"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the platform store isn't answering"})
		return
	}
	s.record(r, sess.User.Username, "assistant.register", "assistant:"+a.Name, strings.Join(a.RedirectURIs, " "))
	writeJSON(w, http.StatusOK, s.assistantView(a))
}

func (s *server) handleDeleteAssistant(w http.ResponseWriter, r *http.Request) {
	sess := s.administrator(w, r, "remove assistants")
	if sess == nil {
		return
	}
	name := r.PathValue("name")
	if s.files == nil || !project.NameOK(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such assistant"})
		return
	}
	if err := s.files.DeleteFile(r.Context(), assistantKey(name)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the platform store isn't answering"})
		return
	}
	s.record(r, sess.User.Username, "assistant.remove", "assistant:"+name, "")
	w.WriteHeader(http.StatusNoContent)
}
