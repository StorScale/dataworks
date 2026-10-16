package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
)

// Airflow takes a project's pipelines from its git repo as a DAG bundle of
// the project's own (project-<name>), with the repo's credentials in an
// Airflow connection (git_<name>). The operator keeps the connection; the
// bundles, Airflow reads as it starts, from the operator (Bundles), through
// the wrapper its processes start in (stack/tools/airflow_bundles.py).
type Airflow struct {
	API *pipelines.Client
	// DefaultRef is the branch a project's repo is followed on when it names
	// none: the install's own (main in development, a release branch in production).
	DefaultRef string
	// KnownHosts is the file, in Airflow's containers, of the git hosts' SSH
	// keys (the install's): with it, SSH repos are checked against it; without,
	// a host's key is taken the first time it's seen.
	KnownHosts string
}

// Bundle is one of Airflow's DAG bundles, as [dag_processor] dag_bundle_config_list has them.
type Bundle struct {
	Name      string         `json:"name"`
	Classpath string         `json:"classpath"`
	Kwargs    map[string]any `json:"kwargs"`
}

// The platform's own DAGs, from Airflow's dags folder.
var localBundle = Bundle{Name: "dags-folder", Classpath: "airflow.dag_processing.bundles.local.LocalDagBundle", Kwargs: map[string]any{}}

const refreshSeconds = 60 // how often a project's repo is fetched

func (a *Airflow) bundle(p *project.Project) Bundle {
	pl := p.Spec.Pipelines
	ref := pl.Ref
	if ref == "" {
		ref = a.DefaultRef
	}
	kw := map[string]any{"tracking_ref": ref, "git_conn_id": p.GitConnID(), "repo_url": pl.Repo, "refresh_interval": refreshSeconds}
	if pl.Path != "" {
		kw["subdir"] = pl.Path
	}
	return Bundle{Name: p.BundleName(), Classpath: "airflow.providers.git.bundles.git.GitDagBundle", Kwargs: kw}
}

// connection is the project's git connection, from its repo and its secret.
func (a *Airflow) connection(p *project.Project, secret map[string]string) (pipelines.Connection, error) {
	pl := p.Spec.Pipelines
	conn := pipelines.Connection{ID: p.GitConnID(), Type: "git", Host: pl.Repo}
	if strings.HasPrefix(pl.Repo, "https://") || strings.HasPrefix(pl.Repo, "http://") {
		if secret != nil {
			if secret["password"] == "" {
				return conn, fmt.Errorf("secret %q has no password (a token) for an https repo", pl.Credentials.SecretRef)
			}
			conn.Login, conn.Password = secret["username"], secret["password"]
		}
		return conn, nil
	}
	key := secret["sshPrivateKey"]
	if key == "" {
		return conn, fmt.Errorf("secret %q has no sshPrivateKey for an ssh repo", pl.Credentials.SecretRef)
	}
	extra := map[string]string{"private_key": key, "strict_host_key_checking": "accept-new"}
	if a.KnownHosts != "" {
		extra["strict_host_key_checking"], extra["known_hosts_file"] = "yes", a.KnownHosts
	}
	if pass := secret["sshPrivateKeyPassphrase"]; pass != "" {
		extra["private_key_passphrase"] = pass
	}
	b, _ := json.Marshal(extra)
	conn.Extra = string(b)
	return conn, nil
}

// applyAirflow makes the shared Airflow hold the project's git connection,
// or none when the project has no repo.
func (r *Reconciler) applyAirflow(ctx context.Context, p *project.Project) error {
	pl := p.Spec.Pipelines
	if pl == nil || pl.Repo == "" {
		return r.Airflow.API.DeleteConnection(ctx, p.GitConnID())
	}
	return r.putGitConnection(ctx, r.Airflow.API, p)
}

// putGitConnection makes an Airflow (the shared one, or the project's own)
// hold the project's git connection, from the Secret it names.
func (r *Reconciler) putGitConnection(ctx context.Context, api *pipelines.Client, p *project.Project) error {
	pl := p.Spec.Pipelines
	var secret map[string]string
	if pl.Credentials != nil {
		var err error
		if secret, err = r.Store.Connection(ctx, p.Metadata.Name, pl.Credentials.SecretRef); err != nil {
			return fmt.Errorf("reading secret %q: %w", pl.Credentials.SecretRef, err)
		}
		if secret == nil {
			return fmt.Errorf("no secret %q (the repo's credentials) among the project's connections", pl.Credentials.SecretRef)
		}
	}
	conn, err := r.Airflow.connection(p, secret)
	if err != nil {
		return err
	}
	if err := api.PutConnection(ctx, conn); err != nil {
		// Airflow's answer may echo what was sent: say only what failed, and how.
		var e *pipelines.APIError
		if errors.As(err, &e) {
			return fmt.Errorf("Airflow refused the connection %s for %s (HTTP %d)", conn.ID, redact(pl.Repo), e.Code)
		}
		return fmt.Errorf("Airflow: the connection %s: %v", conn.ID, err)
	}
	return nil
}

// redact is a repo's address without anything that could be a credential.
func redact(repo string) string {
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host + u.Path
	}
	return repo
}

// Bundles are the DAG bundles Airflow should run: the platform's own, and
// one for each project whose repo and connection have been applied.
func (r *Reconciler) Bundles(ctx context.Context) ([]Bundle, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []Bundle{localBundle}
	if r.ProjectAirflows != nil { // each project's repo is in its own Airflow
		return out, nil
	}
	for _, st := range statuses {
		if st.Spec == nil || st.Phase == project.Deleting || st.Spec.Spec.Pipelines == nil || st.Spec.Spec.Pipelines.Repo == "" {
			continue
		}
		for _, pt := range st.Parts {
			if pt.System == "airflow" && pt.OK {
				out = append(out, r.Airflow.bundle(st.Spec))
			}
		}
	}
	sort.Slice(out[1:], func(i, j int) bool { return out[1+i].Name < out[1+j].Name })
	return out, nil
}
