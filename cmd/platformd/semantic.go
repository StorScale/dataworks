package main

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

var semanticClient = &http.Client{Timeout: 120 * time.Second}

// handleSemantic passes a project's semantic-layer requests (its model, and
// queries of its metrics) to semanticd, as the signed-in person: with their
// own access token, refreshed when it has expired. semanticd checks that
// they're a member, and runs queries in Trino as them.
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	io.Copy(w, io.LimitReader(res.Body, 8<<20))
}
