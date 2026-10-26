package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/StorScale/dataworks/internal/pipelines"
	"github.com/StorScale/dataworks/internal/project"
)

// ProjectAirflows gives each project with a pipelines repo an Airflow of
// its own, on Kubernetes: its own database and Postgres user, its own keys,
// and a Keycloak service account that's a member of this project alone. Its
// DAG code can then reach nothing of another project's, nor the platform's
// (Airflow can't keep DAG authors from its own database's keys: its
// security model says so). The pod is the chart's (Template), with the
// project's name, bundle and memory filled in.
type ProjectAirflows struct {
	Kube     *Kube
	Postgres *Postgres
	Template Template
	// DefaultMemory is a project's Airflow's when it names none (pipelines.memory).
	DefaultMemory string
	// PlatformURL is the platform's address: the pipelines sign in to Keycloak there.
	PlatformURL string
}

// Template is the chart's project Airflow (templates/airflow-project.yaml),
// with placeholders in its strings: @PROJECT@, @AIRFLOW@, @DATABASE@,
// @BUNDLES@, @ROLE_GROUPS@, and @MEMORY_<PART>@ / @REQUEST_<PART>@
// for the parts API, SCHEDULER and PROCESSOR.
type Template struct {
	Deployment map[string]any `json:"deployment"`
	Service    map[string]any `json:"service"`
}

// LoadTemplate reads the chart's template (JSON) from a file.
func LoadTemplate(path string) (Template, error) {
	var t Template
	data, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	if t.Deployment == nil || t.Service == nil {
		return t, fmt.Errorf("%s: no deployment or service", path)
	}
	return t, nil
}

// The keys a project's Airflow is made with, in its Secret (AirflowName).
const (
	keyDBPassword       = "DB_PASSWORD"
	keyFernet           = "FERNET_KEY"
	keySecret           = "SECRET_KEY"
	keyJWT              = "JWT_SECRET"
	keyPlatformPassword = project.AirflowPlatformPasswordKey
	keyPipelinesSecret  = "PIPELINES_SECRET"
)

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newAirflowKeys() map[string]string {
	fernet := make([]byte, 32)
	rand.Read(fernet)
	return map[string]string{
		keyDBPassword: randomHex(24), keyFernet: base64.URLEncoding.EncodeToString(fernet),
		keySecret: randomHex(32), keyJWT: randomHex(32), keyPlatformPassword: randomHex(24),
		keyPipelinesSecret: randomHex(24),
	}
}

func labels(p *project.Project) map[string]string {
	return map[string]string{"app.kubernetes.io/part-of": "storscale-platform", "app.kubernetes.io/managed-by": "platform-operator",
		"app.kubernetes.io/name": "project-airflow", "platform.storscale.io/project": p.Metadata.Name}
}

// keys are the project's Airflow's keys, made the first time.
func (pa *ProjectAirflows) keys(ctx context.Context, p *project.Project) (map[string]string, error) {
	return pa.Kube.EnsureSecret(ctx, p.AirflowName(), labels(p), newAirflowKeys)
}

// identity makes the project's pipelines client (its service account), with
// the secret its Airflow holds.
func (pa *ProjectAirflows) identity(ctx context.Context, kc *Keycloak, p *project.Project) error {
	keys, err := pa.keys(ctx, p)
	if err != nil {
		return err
	}
	return kc.ensurePipelinesClient(ctx, p.PipelinesClientID(), "Pipelines of project "+p.Metadata.Name, keys[keyPipelinesSecret])
}

// memory splits the project's Airflow's memory between its parts: limits,
// and requests of half as much.
func (pa *ProjectAirflows) memory(p *project.Project) map[string]string {
	q := p.Spec.Pipelines.Memory
	if q == "" {
		q = pa.DefaultMemory
	}
	total, ok := project.MiB(q)
	if !ok || total < project.MinMemoryMiB {
		total = project.MinMemoryMiB
	}
	api, scheduler := total*3/8, total*7/16
	parts := map[string]int{"API": api, "SCHEDULER": scheduler, "PROCESSOR": total - api - scheduler}
	out := map[string]string{}
	for part, mi := range parts {
		out["@MEMORY_"+part+"@"] = strconv.Itoa(mi) + "Mi"
		out["@REQUEST_"+part+"@"] = strconv.Itoa(mi/2) + "Mi"
	}
	return out
}

// fill is the template with the project's values in its strings.
func fill(v any, repl *strings.Replacer) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[repl.Replace(k)] = fill(e, repl)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fill(e, repl)
		}
		return out
	case string:
		return repl.Replace(x)
	}
	return v
}

// ConnectionsAnnotation, on a project Airflow's pod, says which connections
// it started with (a keyed hash of them). Airflow's git bundle reads its
// connection once, as it starts: one that came later, or a rotated token,
// is only used once the pod starts again.
const ConnectionsAnnotation = "platform.storscale.io/connections"

// connectionsHash is a keyed hash (HMAC, with the Airflow's own secret key)
// of its connections: it changes when they do, and says nothing of them.
func connectionsHash(key string, conns ...pipelines.Connection) string {
	m := hmac.New(sha256.New, []byte(key))
	json.NewEncoder(m).Encode(conns)
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// objects are the project's Airflow's Deployment, its pod marked with the
// connections it starts with, and its Service.
func (pa *ProjectAirflows) objects(a *Airflow, p *project.Project, connections string) (deployment, service map[string]any) {
	bundles, _ := json.Marshal([]Bundle{a.bundle(p), StorscaleBundle})
	groups := map[string][]string{p.Group(project.Editor): {"Op"}, p.Group(project.Reader): {"Viewer"}}
	for _, name := range p.CustomRoles() { // a custom role (16.1): Op if it runs the pipelines, else it watches them
		if r, ok := p.RoleOf(name); ok {
			groups[p.Group(name)] = []string{"Viewer"}
			if r.Has(project.PermRunPipelines) {
				groups[p.Group(name)] = []string{"Op"}
			}
		}
	}
	roles, _ := json.Marshal(groups)
	pairs := []string{"@PROJECT@", p.Metadata.Name, "@AIRFLOW@", p.AirflowName(), "@DATABASE@", p.AirflowDatabase(),
		"@BUNDLES@", string(bundles), "@ROLE_GROUPS@", string(roles)}
	for k, v := range pa.memory(p) {
		pairs = append(pairs, k, v)
	}
	repl := strings.NewReplacer(pairs...)
	deployment, service = fill(pa.Template.Deployment, repl).(map[string]any), fill(pa.Template.Service, repl).(map[string]any)
	if connections != "" {
		if tpl, ok := deployment["spec"].(map[string]any)["template"].(map[string]any); ok {
			md, _ := tpl["metadata"].(map[string]any)
			if md == nil {
				md = map[string]any{}
				tpl["metadata"] = md
			}
			ann, _ := md["annotations"].(map[string]any)
			if ann == nil {
				ann = map[string]any{}
				md["annotations"] = ann
			}
			ann[ConnectionsAnnotation] = connections
		}
	}
	return deployment, service
}

// API is the project's Airflow's REST API, as the platform's account.
func (pa *ProjectAirflows) API(ctx context.Context, p *project.Project) (*pipelines.Client, error) {
	keys, err := pa.keys(ctx, p)
	if err != nil {
		return nil, err
	}
	return pipelines.New(p.AirflowURL(), "storscale-platform", keys[keyPlatformPassword]), nil
}

// keycloakConnection is the pipelines' Keycloak client, as the platform's
// own DAGs find theirs (keycloak_pipelines): the token endpoint at the
// platform's address.
func (pa *ProjectAirflows) keycloakConnection(p *project.Project, secret string) (pipelines.Connection, error) {
	u, err := url.Parse(pa.PlatformURL)
	if err != nil || u.Host == "" {
		return pipelines.Connection{}, fmt.Errorf("the platform's address %q", pa.PlatformURL)
	}
	c := pipelines.Connection{ID: "keycloak_pipelines", Type: "http", Host: u.Hostname(), Schema: "sso",
		Login: p.PipelinesClientID(), Password: secret, Extra: `{"scheme": "` + u.Scheme + `"}`,
		Description: "The project's pipelines' Keycloak service account (client credentials)"}
	if port := u.Port(); port != "" {
		c.Port, _ = strconv.Atoi(port)
	}
	return c, nil
}

// applyProjectAirflow makes the project's Airflow: its database, its pod
// and Service, and, once it's up, its connections.
func (r *Reconciler) applyProjectAirflow(ctx context.Context, p *project.Project) error {
	pa := r.ProjectAirflows
	keys, err := pa.keys(ctx, p)
	if err != nil {
		return fmt.Errorf("its keys: %w", err)
	}
	if err := pa.Postgres.Ensure(ctx, p.AirflowDatabase(), keys[keyDBPassword]); err != nil {
		return err
	}
	started, err := pa.Kube.PodAnnotation(ctx, p.AirflowName(), ConnectionsAnnotation)
	if err != nil {
		return err
	}
	deployment, service := pa.objects(r.Airflow, p, started)
	if err := pa.Kube.Apply(ctx, "services", service); err != nil {
		return err
	}
	if err := pa.Kube.Apply(ctx, "deployments", deployment); err != nil {
		return err
	}
	ready, err := pa.Kube.Ready(ctx, p.AirflowName())
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("its Airflow is starting")
	}
	api, err := pa.API(ctx, p)
	if err != nil {
		return err
	}
	kc, err := pa.keycloakConnection(p, keys[keyPipelinesSecret])
	if err != nil {
		return err
	}
	git, err := r.gitConnection(ctx, p)
	if err != nil {
		return err
	}
	if err := api.PutConnection(ctx, kc); err != nil {
		var e *pipelines.APIError
		if errors.As(err, &e) {
			return fmt.Errorf("its Airflow refused the connection %s (HTTP %d)", kc.ID, e.Code)
		}
		return fmt.Errorf("its Airflow: the connection %s: %v", kc.ID, err)
	}
	if err := putConnection(ctx, api, p, git); err != nil {
		return err
	}
	// The shared Airflow runs the project's imports (storscale_imports.py), as
	// its pipelines: this account, not the shared one, which is no longer
	// among the project's members.
	if r.Airflow != nil && len(p.Spec.Imports) > 0 {
		shared := kc
		shared.ID, shared.Description = p.ImportIdentityConnID(), "The project "+p.Metadata.Name+"'s pipelines, for its imports"
		if err := r.Airflow.API.PutConnection(ctx, shared); err != nil {
			return fmt.Errorf("the platform's Airflow: the connection %s: %v", shared.ID, err)
		}
	}
	// Started without these connections (its first start, or a token since
	// rotated): start it again, with them.
	if want := connectionsHash(keys[keySecret], kc, git); want != started {
		deployment, _ := pa.objects(r.Airflow, p, want)
		if err := pa.Kube.Apply(ctx, "deployments", deployment); err != nil {
			return err
		}
		return errors.New("its Airflow is starting again, with its connections")
	}
	return nil
}

// teardownProjectAirflow removes a project's Airflow, if it has one: its
// pod, Service, database (and its runs' history), keys and service account.
func (r *Reconciler) teardownProjectAirflow(ctx context.Context, p *project.Project) error {
	pa := r.ProjectAirflows
	found, err := pa.Kube.do(ctx, "GET", pa.Kube.path("secrets", p.AirflowName()), "", nil, nil)
	if err != nil || !found {
		return err // nothing to remove: it never had one, or it's gone
	}
	var errs []error
	for _, kind := range []string{"deployments", "services"} {
		if err := pa.Kube.Delete(ctx, kind, p.AirflowName()); err != nil {
			errs = append(errs, err)
		}
	}
	if err := pa.Postgres.Drop(ctx, p.AirflowDatabase()); err != nil {
		errs = append(errs, err)
	}
	if err := r.Keycloak.deleteClient(ctx, p.PipelinesClientID()); err != nil {
		errs = append(errs, err)
	}
	if r.Airflow != nil {
		if err := r.Airflow.API.DeleteConnection(ctx, p.ImportIdentityConnID()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...) // the Secret stays, so this is tried again
	}
	return pa.Kube.Delete(ctx, "secrets", p.AirflowName())
}
