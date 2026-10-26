package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/StorScale/dataworks/internal/project"
)

// A serving (use case 15.1, R15.4) is one of a project's models answering
// as an API: a server (storscale_serve.py, in Airflow's image, in the
// virtualenv the scorings run in) loads the model's approved version and
// answers platformd, which alone reaches it and signs callers in. On
// Kubernetes each serving has a server of its own (ProjectServings: a
// Deployment, a Service and a Secret named model-<project>-<name>, from the
// chart's template), signed in to Keycloak as a client of its own whose
// service account reads the project as its readers do, and nothing else's.
// In Compose one shared server loads every project's, signed in as the
// shared account (ServingAccount), made a reader of each project it serves.

// ProjectServings are the servings' servers on Kubernetes.
type ProjectServings struct {
	Kube     *Kube
	Template Template
	// DefaultMemory is a server's when its serving names none.
	DefaultMemory string
}

// A ProjectServing is one serving, as the shared server lists them.
type ProjectServing struct {
	Project string          `json:"project"`
	Serving project.Serving `json:"serving"`
}

const servingSecretKey = "CLIENT_SECRET"

func servingLabels(p *project.Project, s project.Serving) map[string]string {
	return map[string]string{"app.kubernetes.io/part-of": "storscale-platform", "app.kubernetes.io/managed-by": "platform-operator",
		"app.kubernetes.io/name": "model-serving", "platform.storscale.io/project": p.Metadata.Name, "platform.storscale.io/serving": s.Name}
}

func servingSelector(p *project.Project) string {
	return "app.kubernetes.io/name=model-serving,platform.storscale.io/project=" + p.Metadata.Name
}

// Servings are the applied projects' servings, for the shared server (Compose).
func (r *Reconciler) Servings(ctx context.Context) ([]ProjectServing, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectServing{}
	for _, st := range statuses {
		p := st.Spec
		if p == nil || st.Phase == project.Deleting {
			continue
		}
		for _, s := range p.Spec.Serving {
			out = append(out, ProjectServing{Project: p.Metadata.Name, Serving: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Project+"/"+out[i].Serving.Name < out[j].Project+"/"+out[j].Serving.Name
	})
	return out, nil
}

// servingIdentities are the readers the project's servings sign in as: on
// Kubernetes, each serving's own client (made here, its secret kept in its
// server's Secret); in Compose, the shared server's account. The clients of
// servings that went are removed.
func (r *Reconciler) servingIdentities(ctx context.Context, p *project.Project) ([]kcUser, error) {
	var out []kcUser
	kept := map[string]bool{}
	if ps := r.ProjectServings; ps != nil {
		for _, s := range p.Spec.Serving {
			keys, err := ps.Kube.EnsureSecret(ctx, p.ServingName(s), servingLabels(p, s), func() map[string]string {
				return map[string]string{servingSecretKey: randomHex(24)}
			})
			if err != nil {
				return nil, fmt.Errorf("serving %s's secret: %w", s.Name, err)
			}
			id := p.ServingClientID(s)
			if err := r.Keycloak.ensurePipelinesClient(ctx, id, s.Name+", a model served by "+p.Metadata.Name, keys[servingSecretKey]); err != nil {
				return nil, err
			}
			sa, err := r.Keycloak.serviceAccount(ctx, id)
			if err != nil {
				return nil, err
			}
			if sa != nil {
				out = append(out, *sa)
			}
			kept[id] = true
		}
	} else if r.ServingAccount != "" && len(p.Spec.Serving) > 0 {
		sa, err := r.Keycloak.serviceAccount(ctx, r.ServingAccount)
		if err != nil {
			return nil, err
		}
		if sa == nil {
			return nil, fmt.Errorf("Keycloak client %q (the servings') has no service account", r.ServingAccount)
		}
		out = append(out, *sa)
	}
	if err := r.Keycloak.deleteClientsExcept(ctx, project.ServingClientPrefix(p.Metadata.Name), kept); err != nil {
		return nil, err
	}
	return out, nil
}

func (ps *ProjectServings) memory(s project.Serving) (limit, request string) {
	q := s.Memory
	if q == "" {
		q = ps.DefaultMemory
	}
	mi, ok := project.MiB(q)
	if !ok || mi < project.MinServingMiB {
		mi = 1024
	}
	return strconv.Itoa(mi) + "Mi", strconv.Itoa(mi/2) + "Mi"
}

func (ps *ProjectServings) objects(p *project.Project, s project.Serving) (deployment, service map[string]any) {
	limit, request := ps.memory(s)
	repl := strings.NewReplacer("@PROJECT@", p.Metadata.Name, "@SERVER@", p.ServingName(s), "@SERVING@", s.Name,
		"@MODEL@", s.Model, "@CLIENT@", p.ServingClientID(s), "@MAX_ROWS@", strconv.Itoa(s.Rows()),
		"@MEMORY@", limit, "@REQUEST@", request)
	return fill(ps.Template.Deployment, repl).(map[string]any), fill(ps.Template.Service, repl).(map[string]any)
}

// applyServing starts (or changes) the project's servings' servers, and
// removes those of servings that went. A server answers once it's up, with
// the approved version or the reason there's none; the project needn't wait.
func (r *Reconciler) applyServing(ctx context.Context, p *project.Project) error {
	ps := r.ProjectServings
	if ps == nil {
		return nil
	}
	want := map[string]bool{}
	var errs []error
	for _, s := range p.Spec.Serving {
		want[p.ServingName(s)] = true
		deployment, service := ps.objects(p, s)
		if err := ps.Kube.Apply(ctx, "services", service); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := ps.Kube.Apply(ctx, "deployments", deployment); err != nil {
			errs = append(errs, err)
		}
	}
	if err := ps.remove(ctx, p, want); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// remove deletes the project's servers not wanted (all of them, with want nil).
func (ps *ProjectServings) remove(ctx context.Context, p *project.Project, want map[string]bool) error {
	names, err := ps.Kube.Names(ctx, "deployments", servingSelector(p))
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		if want[n] {
			continue
		}
		for _, kind := range []string{"deployments", "services", "secrets"} {
			if err := ps.Kube.Delete(ctx, kind, n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// teardownServing removes a deleted project's servers and their clients.
func (r *Reconciler) teardownServing(ctx context.Context, p *project.Project) error {
	var errs []error
	if ps := r.ProjectServings; ps != nil {
		errs = append(errs, ps.remove(ctx, p, nil))
	}
	errs = append(errs, r.Keycloak.deleteClientsExcept(ctx, project.ServingClientPrefix(p.Metadata.Name), nil))
	return errors.Join(errs...)
}
