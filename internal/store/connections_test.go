package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeSecrets is the Kubernetes API's Secrets in one namespace.
type fakeSecrets map[string]kubeSecret

func (f fakeSecrets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/ns/secrets"), "/")
	reply := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	var body kubeSecret
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		json.Unmarshal(data, &body)
	}
	old, ok := f[name]
	switch {
	case r.Method == "GET" && name == "":
		sel := strings.TrimPrefix(r.URL.Query().Get("labelSelector"), ConnectionLabel+"=")
		items := []kubeSecret{}
		for _, s := range f {
			if s.Metadata.Labels[ConnectionLabel] == sel {
				items = append(items, s)
			}
		}
		reply(200, map[string]any{"items": items})
	case !ok && r.Method != "POST":
		reply(404, map[string]string{"message": "not found"})
	case r.Method == "GET":
		reply(200, old)
	case r.Method == "POST", r.Method == "PUT":
		if r.Method == "PUT" && body.Metadata.ResourceVersion != old.Metadata.ResourceVersion {
			reply(409, map[string]string{"message": "conflict"})
			return
		}
		body.Metadata.ResourceVersion += "1"
		f[body.Metadata.Name] = body
		reply(200, body)
	case r.Method == "DELETE":
		delete(f, name)
		reply(200, map[string]string{})
	}
}

func TestAProjectsConnectionsAreItsLabelledSecrets(t *testing.T) {
	secrets := fakeSecrets{}
	platform := kubeSecret{Data: map[string][]byte{"KEY": []byte("x")}}
	platform.Metadata.Name = "storscale-env"
	secrets["storscale-env"] = platform
	ts := httptest.NewServer(secrets)
	defer ts.Close()
	k := &Kube{URL: ts.URL + "/apis/platform.storscale.io/v1alpha1/namespaces/ns/projects",
		Token: func() (string, error) { return "t", nil }, HTTP: ts.Client()}
	ctx := context.Background()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	c := StoredConnection{ConnectionMeta: ConnectionMeta{Name: "sales-dags", Kind: "git", AddedBy: "bob", AddedAt: at},
		Data: map[string]string{"username": "bot", "password": "tok1"}}
	if err := k.PutConnection(ctx, "sales", c); err != nil {
		t.Fatal(err)
	}
	c.Data["password"], c.ChangedBy, c.ChangedAt = "tok2", "alice", at.Add(time.Hour)
	if err := k.PutConnection(ctx, "sales", c); err != nil {
		t.Fatal("rotated:", err)
	}
	got, err := k.Connections(ctx, "sales")
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	if g := got[0]; g.Kind != "git" || g.AddedBy != "bob" || !g.AddedAt.Equal(at) || g.ChangedBy != "alice" || g.Data["password"] != "tok2" {
		t.Errorf("kept: %+v", g)
	}
	if others, _ := k.Connections(ctx, "hr"); len(others) != 0 {
		t.Errorf("hr's: %+v", others)
	}
	// Another project, or the platform's own Secret, isn't the project's to replace or remove.
	if err := k.PutConnection(ctx, "hr", c); !errors.Is(err, ErrNotTheProjects) {
		t.Errorf("hr over sales': %v", err)
	}
	if err := k.PutConnection(ctx, "sales", StoredConnection{ConnectionMeta: ConnectionMeta{Name: "storscale-env"}}); !errors.Is(err, ErrNotTheProjects) {
		t.Errorf("over the platform's: %v", err)
	}
	if err := k.DeleteConnection(ctx, "sales", "storscale-env"); !errors.Is(err, ErrNotTheProjects) || secrets["storscale-env"].Data == nil {
		t.Errorf("removing the platform's: %v", err)
	}
	if err := k.DeleteConnection(ctx, "sales", "sales-dags"); err != nil || len(secrets) != 1 {
		t.Errorf("removed: %v, left %d", err, len(secrets))
	}
	if err := k.DeleteConnection(ctx, "sales", "sales-dags"); err != nil {
		t.Errorf("removed twice: %v", err)
	}
}
