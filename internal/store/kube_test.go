package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

// fakeAPI is the Kubernetes API, as far as Projects go: create, read, replace,
// merge-patch (the resource and its status), and delete, which with a
// finalizer only marks the resource deleted.
type fakeAPI struct {
	mu    sync.Mutex
	items map[string]map[string]any
	rv    int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sa-token" {
		http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/projects"), "/"), "/")
	name, sub := parts[0], ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	var body map[string]any
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		json.Unmarshal(data, &body)
	}
	reply := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	bump := func(item map[string]any) {
		f.rv++
		item["metadata"].(map[string]any)["resourceVersion"] = string(rune('0' + f.rv))
	}
	item := f.items[name]
	switch {
	case r.Method == "GET" && name == "":
		list := []any{}
		for _, it := range f.items {
			list = append(list, it)
		}
		reply(200, map[string]any{"items": list})
	case r.Method == "GET" && item == nil, r.Method != "POST" && item == nil:
		reply(404, map[string]string{"message": "not found"})
	case r.Method == "GET":
		reply(200, item)
	case r.Method == "POST":
		n := body["metadata"].(map[string]any)["name"].(string)
		if spec, _ := body["spec"].(map[string]any); spec == nil || spec["members"] == nil {
			reply(422, map[string]string{"message": "spec.members: Required value"})
			return
		}
		f.items[n] = body
		bump(body)
		reply(201, body)
	case r.Method == "PUT":
		if body["metadata"].(map[string]any)["resourceVersion"] != item["metadata"].(map[string]any)["resourceVersion"] {
			reply(409, map[string]string{"message": "conflict"})
			return
		}
		body["status"] = item["status"] // a PUT of the resource leaves its status alone
		f.items[name] = body
		bump(body)
		reply(200, body)
	case r.Method == "PATCH" && sub == "status":
		item["status"] = body["status"]
		reply(200, item)
	case r.Method == "PATCH":
		md := item["metadata"].(map[string]any)
		md["finalizers"] = body["metadata"].(map[string]any)["finalizers"]
		if fin, _ := md["finalizers"].([]any); len(fin) == 0 && md["deletionTimestamp"] != nil {
			delete(f.items, name) // nothing holds it any more
		}
		reply(200, item)
	case r.Method == "DELETE":
		md := item["metadata"].(map[string]any)
		if fin, _ := md["finalizers"].([]any); len(fin) > 0 {
			md["deletionTimestamp"] = "2026-10-07T12:00:00Z"
		} else {
			delete(f.items, name)
		}
		reply(200, item)
	}
}

func newKube(t *testing.T) (*Kube, *fakeAPI) {
	f := &fakeAPI{items: map[string]map[string]any{}}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return &Kube{URL: ts.URL + "/projects", Token: func() (string, error) { return "sa-token", nil }, HTTP: ts.Client()}, f
}

func sales(t *testing.T, description string) *project.Project {
	p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"description":"` + description + `","members":[{"group":"engineers","role":"editor"}],"tables":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKubeProjects(t *testing.T) {
	k, f := newKube(t)
	ctx := context.Background()
	if err := k.PutProject(ctx, sales(t, "first")); err != nil {
		t.Fatal(err)
	}
	if md := f.items["sales"]["metadata"].(map[string]any); !slices.Contains(md["finalizers"].([]any), any(Finalizer)) {
		t.Errorf("a new project's finalizers: %v, want %s (the operator undoes it before it goes)", md["finalizers"], Finalizer)
	}
	if err := k.PutProject(ctx, sales(t, "second")); err != nil {
		t.Fatal(err)
	}
	p, err := k.Project(ctx, "sales")
	if err != nil || p == nil || p.Spec.Description != "second" {
		t.Fatalf("the project after a change: %+v, %v", p, err)
	}
	ps, err := k.Projects(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("projects: %d, %v", len(ps), err)
	}
	if none, err := k.Project(ctx, "marketing"); none != nil || err != nil {
		t.Errorf("a project that isn't there: %+v, %v (want nil, nil)", none, err)
	}
}

func TestKubeStatusAndDeletion(t *testing.T) {
	k, f := newKube(t)
	ctx := context.Background()
	p := sales(t, "")
	k.PutProject(ctx, p)
	if err := k.PutStatus(ctx, &project.Status{Name: "sales", Phase: project.Ready, Spec: p,
		Members: []project.Person{{Username: "bob", Role: "editor", Via: "engineers"}}}); err != nil {
		t.Fatal(err)
	}
	if n := f.items["sales"]["status"].(map[string]any)["memberCount"]; n != float64(1) {
		t.Errorf("status.memberCount: %v, want 1 (kubectl's Members column)", n)
	}
	sts, err := k.Statuses(ctx)
	if err != nil || len(sts) != 1 || sts[0].Phase != project.Ready || sts[0].Name != "sales" {
		t.Fatalf("statuses: %+v, %v", sts, err)
	}

	// Deleted: no longer a project, but its status stays until the operator is done.
	if err := k.DeleteProject(ctx, "sales"); err != nil {
		t.Fatal(err)
	}
	if ps, _ := k.Projects(ctx); len(ps) != 0 {
		t.Errorf("projects after a delete: %d, want 0", len(ps))
	}
	if sts, _ := k.Statuses(ctx); len(sts) != 1 || sts[0].Spec == nil {
		t.Errorf("statuses after a delete: %+v, want sales's, with the spec to undo", sts)
	}
	if err := k.PutProject(ctx, p); err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Errorf("saving a project being deleted: %v, want refused", err)
	}
	if err := k.DeleteStatus(ctx, "sales"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.items["sales"]; ok {
		t.Error("once the operator is done, the project is still there (its finalizer wasn't removed)")
	}
}

func TestKubeRefusals(t *testing.T) {
	k, _ := newKube(t)
	bad := sales(t, "")
	bad.Spec.Members = nil
	err := k.PutProject(context.Background(), bad)
	var refused *APIError
	if !errors.As(err, &refused) || refused.Code != 422 || !strings.Contains(refused.Message, "members") {
		t.Errorf("an invalid project: %v, want the API's 422 and its reason", err)
	}
	k.Token = func() (string, error) { return "wrong", nil }
	if _, err := k.Projects(context.Background()); err == nil {
		t.Error("a wrong token: no error")
	}
}

func TestASecretIsReadFromTheProjectsNamespace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") != "Bearer sa-token":
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		case r.URL.Path == "/api/v1/namespaces/storscale/secrets/sales-git":
			// Kubernetes gives a Secret's values in base64.
			w.Write([]byte(`{"data": {"username": "Ym9i", "password": "Z2hwX3Rva2Vu"}}`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	defer ts.Close()
	k := &Kube{URL: ts.URL + "/apis/platform.storscale.io/v1alpha1/namespaces/storscale/projects", HTTP: ts.Client(),
		Token: func() (string, error) { return "sa-token", nil }}
	got, err := k.Secret(context.Background(), "sales-git")
	if err != nil || got["username"] != "bob" || got["password"] != "ghp_token" {
		t.Errorf("Secret: %v %v, want bob and his token, decoded", got, err)
	}
	if got, err := k.Secret(context.Background(), "nothing"); got != nil || err != nil {
		t.Errorf("a missing Secret: %v %v, want nil and no error", got, err)
	}
}

func TestAProjectReadsOnlyItsOwnConnections(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/namespaces/storscale/secrets/sales-git":
			w.Write([]byte(`{"metadata": {"labels": {"platform.storscale.io/connection": "sales"}}, "data": {"password": "Z2hwX3Rva2Vu"}}`))
		case "/api/v1/namespaces/storscale/secrets/storscale-env": // the platform's own: no label
			w.Write([]byte(`{"metadata": {"labels": {"app.kubernetes.io/part-of": "storscale-platform"}}, "data": {"BUCKETS_ROOT_PASSWORD": "cm9vdA=="}}`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	defer ts.Close()
	k := &Kube{URL: ts.URL + "/apis/platform.storscale.io/v1alpha1/namespaces/storscale/projects", HTTP: ts.Client(),
		Token: func() (string, error) { return "t", nil }}
	ctx := context.Background()
	if got, err := k.Connection(ctx, "sales", "sales-git"); err != nil || got["password"] != "ghp_token" {
		t.Errorf("sales' own: %v %v", got, err)
	}
	if got, err := k.Connection(ctx, "marketing", "sales-git"); got != nil || err == nil || !strings.Contains(err.Error(), "isn't one of project marketing's connections") {
		t.Errorf("another project's: %v %v, want it refused", got, err)
	}
	if got, err := k.Connection(ctx, "sales", "storscale-env"); got != nil || err == nil {
		t.Errorf("the platform's own Secret: %v %v, want it refused", got, err)
	}
	if got, err := k.Connection(ctx, "sales", "nothing"); got != nil || err != nil {
		t.Errorf("none of that name: %v %v, want nil and no error", got, err)
	}
}

// A project made with kubectl has no finalizer: the operator gives it one as
// it applies it, so deleting it waits for the operator to undo it.
func TestAProjectMadeWithKubectlIsKeptUntilUndone(t *testing.T) {
	k, f := newKube(t)
	ctx := context.Background()
	f.items["sales"] = map[string]any{"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "sales", "resourceVersion": "1"},
		"spec":     map[string]any{"members": []any{map[string]any{"group": "engineers", "role": "editor"}}}}
	if err := k.PutStatus(ctx, &project.Status{Name: "sales", Phase: project.Ready}); err != nil {
		t.Fatal(err)
	}
	if fin, _ := f.items["sales"]["metadata"].(map[string]any)["finalizers"].([]any); !slices.Contains(fin, any(Finalizer)) {
		t.Fatalf("after the operator applied it, finalizers: %v, want %s", fin, Finalizer)
	}
	k.DeleteProject(ctx, "sales")
	if _, ok := f.items["sales"]; !ok {
		t.Error("deleted at once: the operator never undoes it")
	}
}

// copyStore is a Mirror's copy: statuses only.
type copyStore struct {
	Store
	statuses map[string]*project.Status
}

func (c *copyStore) Statuses(context.Context) ([]*project.Status, error) {
	var out []*project.Status
	for _, st := range c.statuses {
		out = append(out, st)
	}
	return out, nil
}

func (c *copyStore) DeleteStatus(_ context.Context, name string) error {
	delete(c.statuses, name)
	return nil
}

// One deleted before it had a finalizer is gone from Kubernetes, but its
// status (and spec) is still in the copy: the operator must undo it.
func TestAMirrorStillHasTheStatusOfAProjectGoneWithoutUndo(t *testing.T) {
	k, _ := newKube(t)
	ctx := context.Background()
	p := sales(t, "")
	k.PutProject(ctx, p)
	k.PutStatus(ctx, &project.Status{Name: "sales", Phase: project.Ready, Spec: p})
	gone := &project.Status{Name: "upgrade-check", Phase: project.Ready, Spec: p}
	m := &Mirror{Store: k, Copy: &copyStore{statuses: map[string]*project.Status{
		"sales": {Name: "sales", Phase: project.Ready, Spec: p}, "upgrade-check": gone}}}
	sts, err := m.Statuses(ctx)
	if err != nil || len(sts) != 2 || sts[1] != gone {
		t.Fatalf("statuses: %+v, %v; want sales's (from Kubernetes) and upgrade-check's (from the copy)", sts, err)
	}
	if err := m.DeleteStatus(ctx, "upgrade-check"); err != nil {
		t.Fatal(err)
	}
	if sts, _ := m.Statuses(ctx); len(sts) != 1 {
		t.Errorf("once undone: %+v, want sales's only", sts)
	}
}
