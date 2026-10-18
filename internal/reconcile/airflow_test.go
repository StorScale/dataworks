package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// memStore is the store, in memory: statuses and secrets.
type memStore struct {
	store.Store
	statuses []*project.Status
	secrets  map[string]map[string]string
}

func (m *memStore) Statuses(context.Context) ([]*project.Status, error) { return m.statuses, nil }
func (m *memStore) Secret(_ context.Context, name string) (map[string]string, error) {
	return m.secrets[name], nil
}
func (m *memStore) Connection(_ context.Context, project, name string) (map[string]string, error) {
	return m.secrets[project+"/"+name], nil
}

// fakeAirflow is Airflow's API, as far as signing in and connections go.
type fakeAirflow struct {
	mu    sync.Mutex
	conns map[string]pipelines.Connection
	calls []string
}

func (f *fakeAirflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/pipelines/auth/token" {
		w.Write([]byte(`{"access_token": "jwt"}`))
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	id := strings.TrimPrefix(r.URL.Path, "/pipelines/api/v2/connections/")
	var c pipelines.Connection
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &c)
	switch {
	case r.Method == "POST" && r.URL.Path == "/pipelines/api/v2/connections":
		f.conns[c.ID] = c
	case r.Method == "PATCH":
		if _, ok := f.conns[id]; !ok {
			http.Error(w, `{"detail": "not found"}`, http.StatusNotFound)
			return
		}
		f.conns[id] = c
	case r.Method == "DELETE":
		if _, ok := f.conns[id]; !ok {
			http.Error(w, `{"detail": "not found"}`, http.StatusNotFound)
			return
		}
		delete(f.conns, id)
	}
}

func sales(t *testing.T, pipelinesSpec string) *project.Project {
	t.Helper()
	p, err := project.Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
		"pipelines": ` + pipelinesSpec + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheGitConnectionOfAProject(t *testing.T) {
	a := &Airflow{DefaultRef: "main"}
	https := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}}`)
	c, err := a.connection(https, map[string]string{"username": "bob", "password": "ghp_token"})
	if err != nil || c.ID != "git_sales" || c.Type != "git" || c.Host != "https://git.example.com/sales/dags.git" || c.Login != "bob" || c.Password != "ghp_token" {
		t.Errorf("https with a token: %+v %v", c, err)
	}
	if _, err := a.connection(https, map[string]string{"username": "bob"}); err == nil {
		t.Error("https with no token: want an error")
	}
	public := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git"}`)
	if c, err := a.connection(public, nil); err != nil || c.Login != "" || c.Password != "" {
		t.Errorf("a public repo: %+v %v, want no credentials", c, err)
	}
	ssh := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "git@git.example.com:sales/dags.git", "credentials": {"secretRef": "sales-git"}}`)
	c, err = a.connection(ssh, map[string]string{"sshPrivateKey": "-----BEGIN KEY-----"})
	if err != nil || !strings.Contains(c.Extra, `"private_key":"-----BEGIN KEY-----"`) || !strings.Contains(c.Extra, `"strict_host_key_checking":"accept-new"`) {
		t.Errorf("ssh, no known hosts: %+v %v", c, err)
	}
	a.KnownHosts = "/etc/storscale/git/known_hosts"
	c, _ = a.connection(ssh, map[string]string{"sshPrivateKey": "k"})
	if !strings.Contains(c.Extra, `"strict_host_key_checking":"yes"`) || !strings.Contains(c.Extra, `"known_hosts_file":"/etc/storscale/git/known_hosts"`) {
		t.Errorf("ssh with the install's known hosts: %s, want them checked", c.Extra)
	}
}

func TestBundlesAreTheAppliedProjects(t *testing.T) {
	applied := func(p *project.Project, ok bool) *project.Status {
		return &project.Status{Name: p.Metadata.Name, Phase: project.Ready, Spec: p, Parts: []project.Part{{System: "airflow", OK: ok}}}
	}
	sales := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "path": "dags"}`)
	other := func(name, extra string) *project.Project {
		p, _ := project.Parse([]byte(`{"metadata": {"name": "` + name + `"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
			"pipelines": {"serviceAccount": "x", "repo": "https://git.example.com/` + name + `.git"` + extra + `}}}`))
		return p
	}
	ads, broken := other("ads", `, "ref": "prod"`), other("broken", "")
	noRepo, _ := project.Parse([]byte(`{"metadata": {"name": "files"}, "spec": {"members": [{"group": "e", "role": "editor"}]}}`))
	r := &Reconciler{Airflow: &Airflow{DefaultRef: "main"}, Store: &memStore{statuses: []*project.Status{
		applied(sales, true), applied(broken, false), applied(ads, true), {Name: "files", Spec: noRepo},
	}}}
	got, err := r.Bundles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != "dags-folder,storscale,project-ads,project-sales" {
		t.Fatalf("bundles %v, want the platform's own two, then each applied project's, sorted", names)
	}
	if kw := got[3].Kwargs; kw["tracking_ref"] != "main" || kw["subdir"] != "dags" || kw["git_conn_id"] != "git_sales" || kw["refresh_interval"] != refreshSeconds {
		t.Errorf("sales' bundle: %v", kw)
	}
	if got[2].Kwargs["tracking_ref"] != "prod" {
		t.Errorf("ads' bundle follows its own ref: %v", got[2].Kwargs)
	}
}

func TestTheOperatorKeepsTheConnection(t *testing.T) {
	f := &fakeAirflow{conns: map[string]pipelines.Connection{}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	mem := &memStore{secrets: map[string]map[string]string{"sales/sales-git": {"username": "bob", "password": "ghp_token"}}}
	r := &Reconciler{Store: mem, Airflow: &Airflow{API: pipelines.New(ts.URL+"/pipelines", "storscale-platform", "pw"), DefaultRef: "main"}}
	ctx := context.Background()
	p := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}}`)

	if err := r.applyAirflow(ctx, p); err != nil || f.conns["git_sales"].Password != "ghp_token" {
		t.Fatalf("first apply: %v %+v", err, f.conns)
	}
	mem.secrets["sales/sales-git"]["password"] = "ghp_rotated"
	if err := r.applyAirflow(ctx, p); err != nil || f.conns["git_sales"].Password != "ghp_rotated" {
		t.Errorf("a rotated token: %v %+v, want it in Airflow", err, f.conns["git_sales"])
	}
	if got := strings.Join(f.calls, ", "); got != "PATCH /pipelines/api/v2/connections/git_sales, POST /pipelines/api/v2/connections, PATCH /pipelines/api/v2/connections/git_sales" {
		t.Errorf("calls: %s", got)
	}

	missing := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "credentials": {"secretRef": "nothing"}}`)
	if err := r.applyAirflow(ctx, missing); err == nil || !strings.Contains(err.Error(), `no secret "nothing"`) {
		t.Errorf("a missing secret: %v", err)
	}

	noRepo := sales(t, `{"serviceAccount": "airflow-pipelines"}`)
	if err := r.applyAirflow(ctx, noRepo); err != nil || len(f.conns) != 0 {
		t.Errorf("the repo removed: %v %+v, want the connection gone", err, f.conns)
	}
	if err := r.applyAirflow(ctx, noRepo); err != nil {
		t.Errorf("no repo, and no connection already: %v", err)
	}
}

func TestAnErrorNeverCarriesTheToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pipelines/auth/token" {
			w.Write([]byte(`{"access_token": "jwt"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		http.Error(w, "refused: "+string(body), http.StatusForbidden) // an answer that echoes the request
	}))
	defer ts.Close()
	r := &Reconciler{Store: &memStore{secrets: map[string]map[string]string{"sales/sales-git": {"password": "ghp_secret"}}},
		Airflow: &Airflow{API: pipelines.New(ts.URL+"/pipelines", "storscale-platform", "pw")}}
	p := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}}`)
	err := r.applyAirflow(context.Background(), p)
	if err == nil || strings.Contains(err.Error(), "ghp_secret") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error %v: want the status, never the token", err)
	}
}

func TestADeletedProjectsDeliveriesLeaveTheSharedAirflow(t *testing.T) {
	f := &fakeAirflow{conns: map[string]pipelines.Connection{}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	p, err := project.Parse([]byte(`{"metadata": {"name": "deliver"}, "spec": {"members": [{"group": "engineers", "role": "editor"}], "tables": {},
		"pipelines": {"serviceAccount": "airflow-pipelines"}, "deliveries": [{"name": "acme", "table": "regions", "format": "csv",
		"files": "a_{date}.csv", "schedule": "0 6 * * 1", "target": "deliver-s3"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := DeliveryConnID(p, p.Spec.Deliveries[0])
	f.conns[id] = pipelines.Connection{ID: id, Type: "generic", Extra: `{"target": {"secretKey": "s"}}`}
	r := &Reconciler{Airflow: &Airflow{API: pipelines.New(ts.URL+"/pipelines", "storscale-platform", "pw")}}
	if err := r.removeDeliveries(context.Background(), p); err != nil || len(f.conns) != 0 {
		t.Errorf("deleted: %v, left %+v", err, f.conns)
	}
	if err := r.removeDeliveries(context.Background(), p); err != nil {
		t.Errorf("again, with nothing left: %v", err)
	}
}
