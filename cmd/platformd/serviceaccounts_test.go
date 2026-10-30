package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// A project's service accounts: its editors register them, an administrator
// other than the registrant approves (a service account may be an editor,
// and only administrators make members), the secret is shown once and kept,
// and the account, in none of the platform's groups, is let in as the
// member it is.
func TestServiceAccountsAreApprovedByAnotherAdministrator(t *testing.T) {
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"alice","role":"editor"},{"user":"erin","role":"reader"}],"tables":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	const crm = "service-account-svc-sales-crm"
	members := []project.Person{{Username: "alice", Role: "editor"}, {Username: "erin", Role: "reader"},
		{Username: crm, Role: "editor", Via: "service account", Can: editorRole().Granted()}}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: members}}},
		specs: map[string]*project.Project{"sales": sales}}, conns: map[string]store.StoredConnection{}}
	s := &server{cfg: config{Groups: []string{"analysts", "engineers"}, AdminGroups: []string{"engineers"}, PlatformURL: "https://dw.example.com"},
		store: st, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: map[string]*session{}}
	for name, groups := range map[string][]string{"alice": {"analysts"}, "erin": {"analysts"}, "bob": {"engineers"}, "dave": {"engineers"},
		crm: {"sales-editors"}, "service-account-platform-operator": {}} {
		s.sessions[name] = &session{User: user{Username: name, Groups: groups}, Expires: time.Now().Add(time.Hour)}
	}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		r.Header.Set("X-Platform-Request", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	spec := func() *project.Project { return st.specs["sales"] }

	if code, body := call("erin", "PUT", "/api/projects/sales/serviceaccounts/crm", `{"role":"editor"}`); code != http.StatusForbidden {
		t.Errorf("a reader registers one: %d %s", code, body)
	}
	if code, body := call("alice", "PUT", "/api/projects/sales/serviceaccounts/crm", `{"role":"editor","description":"the CRM"}`); code != http.StatusOK {
		t.Fatalf("an editor registers one: %d %s", code, body)
	}
	if sa := spec().ServiceAccountOf("crm"); sa == nil || sa.RegisteredBy != "alice" || sa.Owner != "alice" || sa.Approved() {
		t.Fatalf("registered: %+v", sa)
	}
	if code, body := call("alice", "POST", "/api/projects/sales/serviceaccounts/crm/approve", ""); code != http.StatusForbidden || !strings.Contains(body, "administrator") {
		t.Errorf("an editor approves it: %d %s", code, body)
	}
	code, body := call("dave", "POST", "/api/projects/sales/serviceaccounts/crm/approve", "")
	var got struct {
		ClientID, Secret, TokenURL, Username string
	}
	json.Unmarshal([]byte(body), &got)
	if code != http.StatusOK || got.ClientID != "svc-sales-crm" || len(got.Secret) != 48 || got.Username != crm ||
		got.TokenURL != "https://dw.example.com/sso/realms/lakehouse/protocol/openid-connect/token" {
		t.Fatalf("an administrator approves it: %d %s", code, body)
	}
	if c := st.conns["sales/sales-svc-crm"]; c.Data["secret"] != got.Secret || c.Data["clientId"] != "svc-sales-crm" {
		t.Errorf("its secret kept: %+v", c)
	}
	if sa := spec().ServiceAccountOf("crm"); sa.ApprovedBy != "dave" {
		t.Errorf("approved by: %+v", sa)
	}

	// An administrator registers one: another approves it, not them.
	call("bob", "PUT", "/api/projects/sales/serviceaccounts/feed", `{"role":"reader"}`)
	if code, body := call("bob", "POST", "/api/projects/sales/serviceaccounts/feed/approve", ""); code != http.StatusForbidden || !strings.Contains(body, "other than") {
		t.Errorf("its registrant approves it: %d %s", code, body)
	}

	// The service account, in none of the platform's groups, is let in as a member; another service account isn't.
	if code, body := call(crm, "GET", "/api/projects/sales/serviceaccounts", ""); code != http.StatusOK {
		t.Errorf("the service account reads its project: %d %s", code, body)
	}
	if code, _ := call("service-account-platform-operator", "GET", "/api/projects/sales/serviceaccounts", ""); code != http.StatusForbidden {
		t.Errorf("a client that isn't a project's service account: %d, want 403", code)
	}
	if code, body := call(crm, "PUT", "/api/projects/sales/serviceaccounts/more", `{"role":"editor"}`); code != http.StatusForbidden {
		t.Errorf("a service account registers another: %d %s", code, body)
	}

	// A rotation: a new secret, still approved; a changed role needs approval again.
	code, body = call("bob", "POST", "/api/projects/sales/serviceaccounts/crm/rotate", "")
	var rotated struct{ Secret string }
	json.Unmarshal([]byte(body), &rotated)
	if code != http.StatusOK || rotated.Secret == "" || rotated.Secret == got.Secret || st.conns["sales/sales-svc-crm"].Data["secret"] != rotated.Secret {
		t.Errorf("rotated: %d %s", code, body)
	}
	call("alice", "PUT", "/api/projects/sales/serviceaccounts/crm", `{"role":"reader","owner":"alice"}`)
	if sa := spec().ServiceAccountOf("crm"); sa.Approved() || sa.RegisteredBy != "alice" {
		t.Errorf("its role changed, it waits for approval again: %+v", sa)
	}

	// Revoked: out of the spec, its secret gone.
	if code, body := call("alice", "DELETE", "/api/projects/sales/serviceaccounts/crm", ""); code != http.StatusNoContent {
		t.Fatalf("revoked: %d %s", code, body)
	}
	if _, kept := st.conns["sales/sales-svc-crm"]; kept || spec().ServiceAccountOf("crm") != nil {
		t.Error("a revoked service account's secret and entry should be gone")
	}
}

func editorRole() project.Role {
	r, _ := (&project.Project{}).RoleOf(project.Editor)
	return r
}
