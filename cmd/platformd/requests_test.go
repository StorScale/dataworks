package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every request gets an ID, answered in X-Request-ID and quoted in a refusal.
func TestRequestIDs(t *testing.T) {
	var logged strings.Builder
	s := &server{cfg: config{Groups: []string{"analysts"}}, now: time.Now, log: slog.New(slog.NewTextHandler(&logged, nil)),
		sessions: map[string]*session{}, tokenUser: func(string) (string, error) { return "", errors.New("the token isn't the realm's") }}
	h := s.routes()
	r := httptest.NewRequest("GET", "/api/projects/sales/flows", nil)
	r.Header.Set("Authorization", "Bearer nobody")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	id := w.Header().Get("X-Request-ID")
	if w.Code != http.StatusUnauthorized || len(id) != 16 || out["request"] != id || out["error"] == "" {
		t.Errorf("%d %s %v", w.Code, id, out)
	}
	if !strings.Contains(logged.String(), "request refused") || !strings.Contains(logged.String(), "request="+id) || !strings.Contains(logged.String(), "status=401") {
		t.Errorf("log: %s", logged.String())
	}
	// A caller's plain ID is kept; a strange one isn't.
	r = httptest.NewRequest("GET", "/api/projects/sales/flows", nil)
	r.Header.Set("X-Request-ID", "ticket-4711")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("X-Request-ID") != "ticket-4711" {
		t.Errorf("a given ID: %q", w.Header().Get("X-Request-ID"))
	}
	r = httptest.NewRequest("GET", "/healthz", nil)
	r.Header.Set("X-Request-ID", "bad id\n")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); got == "bad id\n" || len(got) != 16 {
		t.Errorf("a strange ID: %q", got)
	}
	_ = io.Discard
}
