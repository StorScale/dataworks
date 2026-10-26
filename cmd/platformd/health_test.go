package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/health"
)

func TestTheHealthPageIsAdministratorsAndSaysWhatsAffectedToAll(t *testing.T) {
	k := newFakeKeycloak(t)
	ts, s := newTestPlatform(t, k, func(*config) {})
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	var sent []health.Alert
	teams := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var card map[string]any
		json.NewDecoder(r.Body).Decode(&card)
		sent = append(sent, health.Alert{Title: "received"})
	}))
	defer teams.Close()
	s.health = &watch{link: "http://storscale.localhost:8800/admin/health",
		checker: &health.Checker{Checks: []health.Check{
			{Name: "superset", Label: "Dashboards", URL: down.URL, Features: []string{"dashboards"}}}, Confirm: 1},
		notifier: &health.Notifier{Targets: func(context.Context) (health.Targets, error) {
			return health.Targets{TeamsWebhook: teams.URL, EmailTo: []string{"ops@example.com"}}, nil
		}}}
	s.health.checker.Once(context.Background())

	get := func(c *http.Cookie) (int, map[string]any) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/health", nil)
		req.AddCookie(c)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, body
	}
	alice, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	code, body := get(alice)
	if code != 200 || strings.Join(toStrings(body["affected"]), ",") != "dashboards" || body["services"] != nil {
		t.Errorf("alice: %d %v, want what's affected, and no more", code, body)
	}
	bob, _ := signIn(t, ts, k, map[string]any{"preferred_username": "bob", "groups": []string{"engineers"}}, "/")
	code, body = get(bob)
	svcs, _ := body["services"].([]any)
	if code != 200 || len(svcs) != 1 || svcs[0].(map[string]any)["state"] != "down" ||
		body["targets"].(map[string]any)["teams"] != true {
		t.Fatalf("bob (an administrator): %d %v", code, body)
	}

	post := func(c *http.Cookie, action, payload string, header bool) (int, map[string]any) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/health/"+action, strings.NewReader(payload))
		req.AddCookie(c)
		if header {
			req.Header.Set("X-Platform-Request", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, _ := post(alice, "mute", `{"service": "superset", "hours": 2}`, true); code != http.StatusForbidden {
		t.Errorf("alice mutes: %d, want refused", code)
	}
	if code, _ := post(bob, "mute", `{"service": "superset", "hours": 2}`, false); code != http.StatusForbidden {
		t.Errorf("a mute without the platform's header: %d, want refused", code)
	}
	if code, _ := post(bob, "mute", `{"service": "superset", "hours": 2}`, true); code != 200 || s.health.notifier.Muted("superset").IsZero() {
		t.Errorf("bob mutes: %d", code)
	}
	code, out := post(bob, "test", "", true)
	if code != 200 || out["targets"].(map[string]any)["teams"] != "sent" || len(sent) != 1 {
		t.Errorf("a test alert: %d %v, %d received", code, out, len(sent))
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
