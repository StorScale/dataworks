package reconcile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/dataworks/internal/pipelines"
	"github.com/StorScale/dataworks/internal/project"
)

// template is the chart's project Airflow, cut down to its placeholders.
const template = `{
  "deployment": {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "@AIRFLOW@",
    "labels": {"platform.storscale.io/project": "@PROJECT@"}},
    "spec": {"template": {"spec": {"containers": [
      {"name": "api-server", "env": [
        {"name": "AIRFLOW__DATABASE__SQL_ALCHEMY_CONN", "value": "postgresql+psycopg2://@DATABASE@:$(DB_PASSWORD)@airflow-db:5432/@DATABASE@"},
        {"name": "AIRFLOW__DAG_PROCESSOR__DAG_BUNDLE_CONFIG_LIST", "value": "@BUNDLES@"},
        {"name": "AIRFLOW_ROLE_GROUPS", "value": "@ROLE_GROUPS@"}],
       "resources": {"limits": {"memory": "@MEMORY_API@"}, "requests": {"memory": "@REQUEST_API@"}}},
      {"name": "scheduler", "resources": {"limits": {"memory": "@MEMORY_SCHEDULER@"}}},
      {"name": "dag-processor", "resources": {"limits": {"memory": "@MEMORY_PROCESSOR@"}}}]}}}},
  "service": {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "@AIRFLOW@"}}
}`

func aProject(t *testing.T, name, pipelines string) *project.Project {
	t.Helper()
	p, err := project.Parse([]byte(`{"metadata": {"name": "` + name + `"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
		"pipelines": ` + pipelines + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAProjectsAirflowIsTheChartsWithItsOwnValues(t *testing.T) {
	var tpl Template
	if err := json.Unmarshal([]byte(template), &tpl); err != nil {
		t.Fatal(err)
	}
	pa := &ProjectAirflows{Template: tpl, DefaultMemory: "2Gi"}
	p := aProject(t, "sales-eu", `{"serviceAccount": "x", "repo": "https://git.example.com/sales/dags.git", "memory": "4Gi"}`)
	d, svc := pa.objects(&Airflow{DefaultRef: "main"}, p, "")
	got, _ := json.Marshal(d)
	for _, want := range []string{`"name":"project-sales-eu-airflow"`, `"platform.storscale.io/project":"sales-eu"`,
		`postgresql+psycopg2://airflow_sales_eu:$(DB_PASSWORD)@airflow-db:5432/airflow_sales_eu`,
		`"memory":"1536Mi"`, `"memory":"768Mi"`, `"memory":"1792Mi"`, // 4Gi: 3/8 for the API server, half of it asked for
		`\"git_conn_id\":\"git_sales_eu\"`, `\"name\":\"project-sales-eu\"`, `\"tracking_ref\":\"main\"`,
		`\"sales-eu-editors\":[\"Op\"]`, `\"sales-eu-readers\":[\"Viewer\"]`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("deployment: no %s in %s", want, got)
		}
	}
	if regexp.MustCompile(`@[A-Z_]+@`).Match(got) || !strings.Contains(string(got), `"AIRFLOW__DATABASE__SQL_ALCHEMY_CONN"`) {
		t.Errorf("a placeholder left, or Airflow's own setting changed: %s", got)
	}
	if svc["metadata"].(map[string]any)["name"] != "project-sales-eu-airflow" {
		t.Errorf("service: %v", svc)
	}
	small := aProject(t, "ads", `{"serviceAccount": "x", "repo": "https://git.example.com/ads.git"}`)
	m := pa.memory(small)
	if m["@MEMORY_API@"] != "768Mi" || m["@MEMORY_SCHEDULER@"] != "896Mi" || m["@MEMORY_PROCESSOR@"] != "384Mi" {
		t.Errorf("the install's 2Gi: %v", m)
	}
}

func TestTheConnectionToKeycloakIsTheProjectsOwnClient(t *testing.T) {
	pa := &ProjectAirflows{PlatformURL: "https://storscale.example.com"}
	p := aProject(t, "sales", `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales.git"}`)
	c, err := pa.keycloakConnection(p, "s3cret")
	if err != nil || c.ID != "keycloak_pipelines" || c.Host != "storscale.example.com" || c.Port != 0 || c.Schema != "sso" ||
		c.Login != "project-sales-pipelines" || c.Password != "s3cret" || c.Extra != `{"scheme": "https"}` {
		t.Errorf("%+v %v", c, err)
	}
	pa.PlatformURL = "http://storscale.localhost:8800"
	if c, _ := pa.keycloakConnection(p, "s"); c.Port != 8800 || c.Extra != `{"scheme": "http"}` {
		t.Errorf("with a port: %+v", c)
	}
}

// fakeKube is Kubernetes' API for Secrets, Services and Deployments.
type fakeKube struct {
	mu      sync.Mutex
	objects map[string]map[string]any // path -> object
	calls   []string
}

func (f *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	body, _ := io.ReadAll(r.Body)
	switch r.Method {
	case "GET":
		o, ok := f.objects[r.URL.Path]
		if !ok {
			http.Error(w, `{"message": "not found"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(o)
	case "POST":
		var o map[string]any
		json.Unmarshal(body, &o)
		f.objects[r.URL.Path+"/"+o["metadata"].(map[string]any)["name"].(string)] = o
	case "PATCH":
		if r.Header.Get("Content-Type") != "application/apply-patch+yaml" || r.URL.Query().Get("fieldManager") != "platform-operator" {
			http.Error(w, `{"message": "not a server-side apply"}`, http.StatusBadRequest)
			return
		}
		var o map[string]any
		json.Unmarshal(body, &o)
		f.objects[r.URL.Path] = o
	case "DELETE":
		delete(f.objects, r.URL.Path)
	}
}

func TestAProjectsKeysAreMadeOnceAndKept(t *testing.T) {
	f := &fakeKube{objects: map[string]map[string]any{}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	k := &Kube{URL: ts.URL, Namespace: "storscale", HTTP: ts.Client(), Token: func() (string, error) { return "t", nil }}
	pa := &ProjectAirflows{Kube: k}
	p := aProject(t, "sales", `{"serviceAccount": "x", "repo": "https://git.example.com/sales.git"}`)
	first, err := pa.keys(context.Background(), p)
	if err != nil || len(first[keyDBPassword]) != 48 || len(first[keyPlatformPassword]) != 48 {
		t.Fatalf("keys: %v", err)
	}
	if f, _ := base64.URLEncoding.DecodeString(first[keyFernet]); len(f) != 32 {
		t.Errorf("a Fernet key is 32 bytes, URL-safe base64: %d", len(f))
	}
	// Kubernetes gives a Secret's data in base64, as it was sent.
	stored := f.objects["/api/v1/namespaces/storscale/secrets/project-sales-airflow"]
	again, _ := pa.keys(context.Background(), p)
	if again[keyDBPassword] != first[keyDBPassword] || stored["metadata"].(map[string]any)["labels"].(map[string]any)["platform.storscale.io/project"] != "sales" {
		t.Errorf("the keys again: %v, want the same; labels %v", again[keyDBPassword] == first[keyDBPassword], stored["metadata"])
	}
}

func TestAProjectWithoutAnAirflowHasNothingToTakeDown(t *testing.T) {
	f := &fakeKube{objects: map[string]map[string]any{}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	r := &Reconciler{ProjectAirflows: &ProjectAirflows{Kube: &Kube{URL: ts.URL, Namespace: "storscale", HTTP: ts.Client(),
		Token: func() (string, error) { return "t", nil }}}}
	p := aProject(t, "sales", `{"serviceAccount": "airflow-pipelines"}`)
	if err := r.teardownProjectAirflow(context.Background(), p); err != nil || len(f.calls) != 1 {
		t.Errorf("%v, calls %v: want one look for its Secret, and nothing else (no database, no Keycloak)", err, f.calls)
	}
	if r.ownAirflow(p) {
		t.Error("a project without a repo has no Airflow of its own")
	}
	if !r.ownAirflow(aProject(t, "ads", `{"serviceAccount": "x", "repo": "https://git.example.com/ads.git"}`)) {
		t.Error("a project with a repo, on Kubernetes, has its own")
	}
}

func TestTheSharedAirflowTakesNoProjectsRepoWhenEachHasItsOwn(t *testing.T) {
	sales := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git"}`)
	r := &Reconciler{Airflow: &Airflow{DefaultRef: "main"}, ProjectAirflows: &ProjectAirflows{},
		Store: &memStore{statuses: []*project.Status{{Name: "sales", Phase: project.Ready, Spec: sales,
			Parts: []project.Part{{System: "airflow", OK: true}}}}}}
	got, _ := r.Bundles(context.Background())
	if len(got) != 2 || got[0].Name != "dags-folder" || got[1].Name != "storscale" {
		t.Errorf("bundles %v, want the platform's own only", got)
	}
}

func TestAProjectsAirflowStartsAgainWhenItsConnectionsChange(t *testing.T) {
	var tpl Template
	json.Unmarshal([]byte(template), &tpl)
	pa := &ProjectAirflows{Template: tpl, DefaultMemory: "2Gi"}
	p := aProject(t, "sales", `{"serviceAccount": "x", "repo": "https://git.example.com/sales.git"}`)
	d, _ := pa.objects(&Airflow{DefaultRef: "main"}, p, "abc123")
	ann := d["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann[ConnectionsAnnotation] != "abc123" {
		t.Errorf("the pod's connections: %v", ann)
	}
	if d, _ := pa.objects(&Airflow{DefaultRef: "main"}, p, ""); strings.Contains(fmt.Sprint(d), ConnectionsAnnotation) {
		t.Error("a pod that hasn't started with its connections yet: no annotation")
	}
	git := pipelines.Connection{ID: "git_sales", Type: "git", Host: "https://git.example.com/sales.git", Login: "bob", Password: "ghp_one"}
	first := connectionsHash("key", git)
	if again := connectionsHash("key", git); again != first || len(first) != 32 {
		t.Errorf("the same connections, the same hash: %s %s", first, again)
	}
	git.Password = "ghp_two"
	if connectionsHash("key", git) == first {
		t.Error("a rotated token: want a new hash, and so a restart")
	}
	if connectionsHash("other key", git) == connectionsHash("key", git) {
		t.Error("the hash is keyed: without the Airflow's key it says nothing of the token")
	}
}

// A project with an Airflow of its own has its files imported, in the shared
// Airflow, as its own pipelines: the shared account isn't one of its members.
func TestImportsSignInAsTheProjectsOwnPipelines(t *testing.T) {
	spec := func(name, pipelines string) *project.Project {
		p, err := project.Parse([]byte(`{"metadata": {"name": "` + name + `"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
			"tables": {}, "files": {}, "pipelines": ` + pipelines + `,
			"imports": [{"name": "uploads", "source": "files", "files": {"tables": [{"name": "orders", "file": "orders.csv"}]}}]}}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	own, shared := spec("retail-eu", `{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/retail.git"}`), spec("sales", `{"serviceAccount": "airflow-pipelines"}`)
	applied := []project.Part{{System: "imports", OK: true}}
	r := &Reconciler{ProjectAirflows: &ProjectAirflows{}, Store: &memStore{statuses: []*project.Status{
		{Name: "retail-eu", Spec: own, Parts: applied}, {Name: "sales", Spec: shared, Parts: applied}}}}
	got, err := r.Imports(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("imports: %+v %v", got, err)
	}
	if got[0].Project != "retail-eu" || got[0].Identity != "pipelines_retail_eu" || got[1].Identity != "" {
		t.Errorf("identities: %q %q", got[0].Identity, got[1].Identity)
	}
}
