package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// Adopter keeps a project's adopted tables (tables.adopt): tables of
// another platform's Iceberg catalog, an external store the administrator
// set up (the chart's externalStores), taken into the project where their
// files are. Each is registered in the project's namespace from its
// current metadata file (Nessie's Iceberg REST API: nothing is copied or
// written), and registered again from the newer one whenever the other
// platform changes the table. The other platform keeps writing it until the
// table is handed over, so here it's read-only (the project's Ranger
// policies) and the store's key is too.
//
// Where a table is now, the operator asks Trino: the store's own catalog
// there (ext_<store>, a Hive Metastore's or a REST catalog's) has each
// table's metadata files, newest last ($metadata_log_entries).
type Adopter struct {
	Trino   *trino.Client    // the operator's, which may read the stores' catalogs
	Iceberg *catalog.Iceberg // Nessie's Iceberg REST API, the platform's warehouse
}

// ExternalCatalog is the Trino catalog of an external store.
func ExternalCatalog(store string) string { return "ext_" + store }

// latest is a table's current metadata file in its store: from, as
// <store>.<schema>.<table>.
func (a *Adopter) latest(ctx context.Context, from string) (string, error) {
	parts := strings.Split(from, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("from %q: as <store>.<schema>.<table>", from)
	}
	rows, err := a.Trino.Exec(ctx, fmt.Sprintf(`SELECT file FROM %s.%s.%s ORDER BY timestamp DESC LIMIT 1`,
		trino.Quote(ExternalCatalog(parts[0])), trino.Quote(parts[1]), trino.Quote(parts[2]+"$metadata_log_entries")))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return "", fmt.Errorf("%s has no metadata file", from)
	}
	file, _ := rows[0][0].(string)
	if file == "" {
		return "", fmt.Errorf("%s: its metadata file isn't a name: %v", from, rows[0][0])
	}
	return file, nil
}

// Apply registers each adopted table from its store's current metadata
// file, when it isn't already, and unregisters those adopted before
// (prev's) that the spec no longer lists. A table of the project's own by
// an adopted one's name is never replaced: it's an error to resolve.
func (a *Adopter) Apply(ctx context.Context, p, prev *project.Project) error {
	var errs []error
	want := map[string]bool{}
	if t := p.Spec.Tables; t != nil {
		for _, ad := range t.Adopt {
			want[ad.Table] = true
			if err := a.follow(ctx, t.Namespace, ad); err != nil {
				errs = append(errs, fmt.Errorf("adopting %s from %s: %w", ad.Table, ad.From, err))
			}
		}
	}
	if prev != nil && prev.Spec.Tables != nil {
		for _, ad := range prev.Spec.Tables.Adopt {
			if !want[ad.Table] || (p.Spec.Tables != nil && p.Spec.Tables.Namespace != prev.Spec.Tables.Namespace) {
				if err := a.Iceberg.Unregister(ctx, prev.Spec.Tables.Namespace, ad.Table); err != nil {
					errs = append(errs, fmt.Errorf("letting %s go: %w", ad.Table, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (a *Adopter) follow(ctx context.Context, namespace string, ad project.Adoption) error {
	latest, err := a.latest(ctx, ad.From)
	if err != nil {
		return err
	}
	cur, err := a.Iceberg.MetadataLocation(ctx, namespace, ad.Table)
	if err != nil {
		return err
	}
	switch {
	case cur == latest:
		return nil
	case cur == "":
		return a.Iceberg.Register(ctx, namespace, ad.Table, latest)
	case path.Dir(cur) != path.Dir(latest):
		// Its metadata isn't the store table's: a table of the project's own.
		return fmt.Errorf("the project has a table %s of its own (its metadata in %s), which isn't replaced: rename one of them", ad.Table, path.Dir(cur))
	}
	// The other platform changed it: registered again from its newer file.
	// The table is missing between the two, a moment; nothing is lost.
	if err := a.Iceberg.Unregister(ctx, namespace, ad.Table); err != nil {
		return err
	}
	return a.Iceberg.Register(ctx, namespace, ad.Table, latest)
}

// Remove unregisters a project's adopted tables, leaving their files to
// the store they came from (a project deleted keeps no hold on them).
func (a *Adopter) Remove(ctx context.Context, p *project.Project) error {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	var errs []error
	for _, ad := range t.Adopt {
		if err := a.Iceberg.Unregister(ctx, t.Namespace, ad.Table); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
