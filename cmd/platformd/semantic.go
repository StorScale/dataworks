package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

var semanticClient = &http.Client{Timeout: 120 * time.Second}

// handleSemantic passes a project's semantic-layer requests (its model, and
// queries of its metrics) to semanticd, as the signed-in person: with their
// own access token, refreshed when it has expired. semanticd checks that
// they're a member, and runs queries in Trino as them. A model saved is in
// the audit log (semantic.saved), with the metrics it changed.
func (s *server) handleSemantic(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	switch {
	case sess == nil:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
		return
	// Someone in the platform's groups, or a member of the project (its
	// pipelines and agents, signed in with a token: a scheduled notebook's
	// buckets_lake.sql), whom semanticd checks again.
	case !s.allowed(sess.User.Groups) && (r.PathValue("name") == "" || s.mlflowRole(r, r.PathValue("name"), sess.User.Username) == ""):
		s.fail(w, r, http.StatusForbidden, "no access")
		return
	case s.cfg.SemanticURL == "":
		s.fail(w, r, http.StatusServiceUnavailable, "the semantic layer isn't set up (no SEMANTIC_URL)")
		return
	case r.Method != http.MethodGet && r.Header.Get("X-Platform-Request") != "1":
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	case sess.Token == nil:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
		return
	}
	tok, err := s.oauth.TokenSource(r.Context(), sess.Token).Token()
	if err != nil {
		// The Keycloak session is over: sign in again.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
		return
	}
	if tok.AccessToken != sess.Token.AccessToken {
		s.mu.Lock()
		sess.Token = tok
		s.mu.Unlock()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	saving := r.Method == http.MethodPut && r.PathValue("name") != "" && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/projects/"+r.PathValue("name")+"/semantic"), "/")
	var before string
	if saving {
		before = s.semanticModel(r.Context(), r.PathValue("name"))
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, s.cfg.SemanticURL+r.URL.Path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := semanticClient.Do(req)
	if err != nil {
		s.log.Error("semantic layer", "error", err)
		s.fail(w, r, http.StatusBadGateway, "the semantic layer isn't answering")
		return
	}
	defer res.Body.Close()
	if saving && res.StatusCode/100 == 2 {
		var in struct {
			YAML string `json:"yaml"`
		}
		json.Unmarshal(body, &in)
		detail := "saved"
		if moved, err := project.MetricChanges(before, in.YAML); err == nil && len(moved) > 0 {
			detail = strings.Join(moved, "; ")
		}
		s.record(r, sess.User.Username, "semantic.saved", "project:"+r.PathValue("name"), detail)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	io.Copy(w, io.LimitReader(res.Body, 8<<20))
}

// semanticModel is a project's model as kept (semantic/<project>.yaml), or "" for none.
func (s *server) semanticModel(ctx context.Context, name string) string {
	if s.files == nil {
		return ""
	}
	b, err := s.files.GetFile(ctx, "semantic/"+name+".yaml")
	if err != nil {
		return ""
	}
	return string(b)
}
