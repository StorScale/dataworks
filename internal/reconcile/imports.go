package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
)

// A ProjectImport is one of a project's imports (use case 8.2), as the
// platform's Airflow runs it (stack/airflow/dags/storscale_imports.py): a
// DAG each, its credentials in the Airflow connection ConnID (never here).
type ProjectImport struct {
	Project   string         `json:"project"`
	Namespace string         `json:"namespace"` // where its tables land, in the iceberg catalog
	ConnID    string         `json:"connId,omitempty"`
	Import    project.Import `json:"import"`
}

// Imports are every project's imports that its last apply made ready.
func (r *Reconciler) Imports(ctx context.Context) ([]ProjectImport, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectImport{}
	for _, st := range statuses {
		if st.Spec == nil || st.Phase == project.Deleting || st.Spec.Spec.Tables == nil {
			continue
		}
		for _, pt := range st.Parts {
			if pt.System != "imports" || !pt.OK {
				continue
			}
			for _, im := range st.Spec.Spec.Imports {
				pi := ProjectImport{Project: st.Name, Namespace: st.Spec.Spec.Tables.Namespace, Import: im}
				if im.Connection != "" {
					pi.ConnID = st.Spec.ImportConnID(im)
				}
				out = append(out, pi)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Project+"/"+out[i].Import.Name < out[j].Project+"/"+out[j].Import.Name
	})
	return out, nil
}

// importConnection is an import's Airflow connection, from the project's.
func importConnection(p *project.Project, im project.Import, secret map[string]string) (pipelines.Connection, error) {
	c := pipelines.Connection{ID: p.ImportConnID(im), Description: "The import " + im.Name + " of the project " + p.Metadata.Name + " (the platform keeps it)"}
	switch im.Source {
	case "rest_api":
		if secret["token"] == "" {
			return c, errors.New("its connection has no token")
		}
		c.Type, c.Password = "http", secret["token"]
	case "sql_database":
		if secret["host"] == "" || secret["username"] == "" || secret["password"] == "" {
			return c, errors.New("its connection needs a host, username and password")
		}
		c.Type, c.Host, c.Login, c.Password, c.Schema = im.Database.Kind, secret["host"], secret["username"], secret["password"], secret["database"]
		if secret["port"] != "" {
			port, err := strconv.Atoi(secret["port"])
			if err != nil {
				return c, fmt.Errorf("its port %q isn't a number", secret["port"])
			}
			c.Port = port
		}
	}
	return c, nil
}

// applyImports gives the platform's Airflow each import's credentials, and
// takes away those of imports the project no longer has.
func (r *Reconciler) applyImports(ctx context.Context, p *project.Project) error {
	if r.Airflow == nil {
		return errors.New("imports run in the platform's Airflow, and there's none here")
	}
	var errs []error
	want := map[string]bool{}
	for _, im := range p.Spec.Imports {
		if im.Connection == "" {
			continue
		}
		secret, err := r.Store.Connection(ctx, p.Metadata.Name, im.Connection)
		if err == nil && secret == nil {
			err = fmt.Errorf("no connection %q among the project's", im.Connection)
		}
		var conn pipelines.Connection
		if err == nil {
			conn, err = importConnection(p, im, secret)
		}
		if err == nil {
			err = r.Airflow.API.PutConnection(ctx, conn)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("import %s: %w", im.Name, err))
			continue
		}
		want[conn.ID] = true
	}
	// Those of before (the spec the operator last applied) that went.
	if st, err := r.Store.Status(ctx, p.Metadata.Name); err == nil && st != nil && st.Spec != nil {
		for _, im := range st.Spec.Spec.Imports {
			if id := p.ImportConnID(im); !want[id] {
				errs = append(errs, r.Airflow.API.DeleteConnection(ctx, id))
			}
		}
	}
	return errors.Join(errs...)
}

// removeImports takes away every import's credentials of a project that went.
func (r *Reconciler) removeImports(ctx context.Context, p *project.Project) error {
	if r.Airflow == nil {
		return nil
	}
	var errs []error
	for _, im := range p.Spec.Imports {
		errs = append(errs, r.Airflow.API.DeleteConnection(ctx, p.ImportConnID(im)))
	}
	return errors.Join(errs...)
}
