package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// fileStore keeps the platform's files (the platform's bucket).
type fileStore interface {
	PutFile(ctx context.Context, key string, data []byte, contentType string) error
	GetFile(ctx context.Context, key string) ([]byte, error) // nil when there's none
}

// dbtArtifacts are the files of a dbt run that the catalog reads (use case
// 7.5): what the models are, their columns, and how their tests went.
var dbtArtifacts = []string{"manifest", "catalog", "run_results"}

// handleDbtArtifacts takes a project's dbt run's artifacts from its
// pipelines (the platform's helper, storscale.dbt, posts them after each
// run) and keeps them in the platform's bucket as dbt/<project>/<name>.json,
// where catalog-sync reads them. Only the project's own pipelines account
// may: a Keycloak token (its service account's), as Bearer.
func (s *server) handleDbtArtifacts(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	who, ok := s.projectPipelines(w, r, name)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the artifacts, as JSON: " + err.Error()})
		return
	}
	kept := []string{}
	for _, a := range dbtArtifacts {
		data, ok := body[a]
		if !ok || len(data) == 0 || string(data) == "null" {
			continue
		}
		if err := s.files.PutFile(r.Context(), "dbt/"+name+"/"+a+".json", data, "application/json"); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "none of " + strings.Join(dbtArtifacts, ", ")})
		return
	}
	s.record(r, who, "dbt.artifacts", "project:"+name, strings.Join(kept, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"kept": kept})
}

// projectPipelines is who a request's token names, when that's the project's
// pipelines account (and the platform keeps files); otherwise it answers.
func (s *server) projectPipelines(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	if s.files == nil || s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the platform keeps no files here (no STORE_URL)"})
		return "", false
	}
	who, err := s.bearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return "", false
	}
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project"})
		return "", false
	}
	for _, m := range st.Members {
		if m.Username == who && m.Role == "pipelines" {
			return who, true
		}
	}
	s.recordRefusal(r, who, "dbt.artifacts", "project:"+name, "not the project's pipelines")
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the project's pipelines may"})
	return "", false
}

// handleDbtArtifact is one of a project's last dbt run's artifacts, to its
// pipelines (a merge request's review compares its change with production's
// manifest).
func (s *server) handleDbtArtifact(w http.ResponseWriter, r *http.Request) {
	name, artifact := r.PathValue("name"), r.PathValue("artifact")
	if !slices.Contains(dbtArtifacts, artifact) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such artifact"})
		return
	}
	if _, ok := s.projectPipelines(w, r, name); !ok {
		return
	}
	data, err := s.files.GetFile(r.Context(), "dbt/"+name+"/"+artifact+".json")
	switch {
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	case data == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no dbt run of the project's yet"})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// bearer is who a request's Keycloak token (Bearer) names, once verified:
// its preferred_username (a service account's, service-account-<client>).
func (s *server) bearer(r *http.Request) (string, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return "", errors.New("a Keycloak token, as Bearer")
	}
	if s.tokenUser != nil { // the tests'
		return s.tokenUser(raw)
	}
	tok, err := s.tokens.Verify(r.Context(), raw)
	if err != nil {
		return "", errors.New("the token isn't the realm's, or has expired")
	}
	var claims struct {
		Username string `json:"preferred_username"`
	}
	if err := tok.Claims(&claims); err != nil || claims.Username == "" {
		return "", errors.New("the token names no one")
	}
	return claims.Username, nil
}

// tokenVerifier verifies the realm's access tokens, whichever client they
// were issued to (a project's pipelines client).
func tokenVerifier(issuer string, keys oidc.KeySet) *oidc.IDTokenVerifier {
	return oidc.NewVerifier(issuer, keys, &oidc.Config{SkipClientIDCheck: true})
}
