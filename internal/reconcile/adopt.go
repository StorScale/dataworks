package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

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
func ExternalCatalog(store string) string { return catalog.ExternalCatalog(store) }

// latest is a table's current metadata file in its store: from, as
// <store>.<schema>.<table>.
func (a *Adopter) latest(ctx context.Context, from string) (string, error) {
	files, err := catalog.StoreMetadata(ctx, a.Trino, from)
	if err != nil {
		return "", err
	}
	return files[0].File, nil
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
			if !ad.TakenOver.IsZero() {
				if err := a.owned(ctx, t.Namespace, ad); err != nil {
					errs = append(errs, fmt.Errorf("%s, taken over from %s: %w", ad.Table, ad.From, err))
				}
				continue
			}
			if err := a.follow(ctx, t.Namespace, ad); err != nil {
				errs = append(errs, fmt.Errorf("adopting %s from %s: %w", ad.Table, ad.From, err))
			}
		}
	}
	if prev != nil && prev.Spec.Tables != nil {
		for _, ad := range prev.Spec.Tables.Adopt {
			if !ad.TakenOver.IsZero() { // the project's own now: it stays, as its other tables do
				continue
			}
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

// owned checks a table taken over is still the project's alone: the other
// platform hasn't written it since. Its store's newest metadata file is then
// among the table's own (the one it was taken over from, or one before
// it); one that isn't is a change made there after the takeover, which isn't
// here, and two writers lose each other's changes.
func (a *Adopter) owned(ctx context.Context, namespace string, ad project.Adoption) error {
	latest, err := a.latest(ctx, ad.From)
	if err != nil {
		return err
	}
	ours, err := a.Iceberg.MetadataFiles(ctx, namespace, ad.Table)
	if err != nil {
		return err
	}
	if slices.Contains(ours, latest) {
		return nil
	}
	return fmt.Errorf("the other platform has written it since it was taken over (%s): its change (%s) isn't here; stop its writer there, and copy across what it wrote",
		ad.TakenOver.UTC().Format("2006-01-02 15:04 UTC"), path.Base(latest))
}

// Remove unregisters a project's adopted tables, leaving their files to
// the store they came from (a project deleted keeps no hold on them). Those
// taken over stay, as the project's other tables do.
func (a *Adopter) Remove(ctx context.Context, p *project.Project) error {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	var errs []error
	for _, ad := range t.Adopt {
		if !ad.TakenOver.IsZero() {
			continue
		}
		if err := a.Iceberg.Unregister(ctx, t.Namespace, ad.Table); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
