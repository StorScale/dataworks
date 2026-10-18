package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

type kept map[string]string

func (k kept) PutFile(_ context.Context, key string, data []byte, _ string) error {
	k[key] = string(data)
	return nil
}

func TestOnlyAProjectsPipelinesKeepItsDbtArtifacts(t *testing.T) {
	files := kept{}
	st := &statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "service-account-project-sales-pipelines", Role: "pipelines"}, {Username: "bob", Role: "editor"}}}}}
	s := &server{store: st, files: files, now: time.Now, tokenUser: func(raw string) (string, error) {
		if raw == "bad" {
			return "", errors.New("not the realm's")
		}
		return raw, nil
	}}
	post := func(token, body string) int {
		r := httptest.NewRequest("POST", "/api/projects/sales/dbt/artifacts", strings.NewReader(body))
		r.SetPathValue("name", "sales")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.handleDbtArtifacts(w, r)
		return w.Code
	}
	body := `{"manifest": {"nodes": {}}, "run_results": {"results": []}}`
	for _, c := range []struct {
		token string
		want  int
	}{{"", http.StatusUnauthorized}, {"bad", http.StatusUnauthorized}, {"bob", http.StatusForbidden}} {
		if got := post(c.token, body); got != c.want {
			t.Errorf("%q: HTTP %d, want %d", c.token, got, c.want)
		}
	}
	if len(files) != 0 {
		t.Fatalf("kept for someone else: %v", files)
	}
	if got := post("service-account-project-sales-pipelines", body); got != http.StatusOK {
		t.Fatalf("the project's pipelines: HTTP %d", got)
	}
	if files["dbt/sales/manifest.json"] != `{"nodes": {}}` || files["dbt/sales/run_results.json"] == "" || len(files) != 2 {
		t.Errorf("kept: %v", files)
	}
}
