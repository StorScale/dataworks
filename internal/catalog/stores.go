package catalog

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/StorScale/dataworks/internal/trino"
)

// ExternalCatalog is the Trino catalog of an external store (the chart's
// externalStores): its own catalog, read-only, which says where its tables are.
func ExternalCatalog(store string) string { return "ext_" + store }

// A StoreMetadataFile is one of an external store's table's metadata files.
type StoreMetadataFile struct {
	File string
	At   time.Time
}

// StoreMetadata are a store's table's metadata files, newest first, as its
// own catalog has them in Trino ($metadata_log_entries): from as
// <store>.<schema>.<table>, read as tr's account (one Ranger lets read the
// stores' catalogs).
func StoreMetadata(ctx context.Context, tr *trino.Client, from string) ([]StoreMetadataFile, error) {
	parts := strings.Split(from, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("from %q: as <store>.<schema>.<table>", from)
	}
	rows, err := tr.Exec(ctx, fmt.Sprintf(`SELECT file, CAST(to_unixtime("timestamp") * 1000 AS bigint) FROM %s.%s.%s ORDER BY "timestamp" DESC`,
		trino.Quote(ExternalCatalog(parts[0])), trino.Quote(parts[1]), trino.Quote(parts[2]+"$metadata_log_entries")))
	if err != nil {
		return nil, err
	}
	out := []StoreMetadataFile{}
	for _, row := range rows {
		if len(row) < 1 {
			continue
		}
		file, _ := row[0].(string)
		if file == "" {
			return nil, fmt.Errorf("%s: its metadata file isn't a name: %v", from, row[0])
		}
		f := StoreMetadataFile{File: file}
		if len(row) > 1 {
			switch ms := row[1].(type) {
			case float64:
				f.At = time.UnixMilli(int64(ms)).UTC()
			case int64:
				f.At = time.UnixMilli(ms).UTC()
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no metadata file", from)
	}
	return out, nil
}

// WritableCatalog is the Trino catalog that may register and unregister
// tables in an external store's own catalog (externalStores[].catalog.write).
func WritableCatalog(store string) string { return "extw_" + store }

// IsNotFound says whether an error of the stores' catalogs is a table (or
// schema) that isn't there.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "does not exist") || strings.Contains(m, "not found")
}

// UnregisterInStore takes a table out of its store's own catalog, its files
// left where they are (Iceberg's unregister_table): the store's writers no
// longer find it. from as <store>.<schema>.<table>.
func UnregisterInStore(ctx context.Context, tr *trino.Client, from string) error {
	parts := strings.Split(from, ".")
	if len(parts) != 3 {
		return fmt.Errorf("from %q: as <store>.<schema>.<table>", from)
	}
	_, err := tr.Exec(ctx, fmt.Sprintf(`CALL %s.system.unregister_table(schema_name => '%s', table_name => '%s')`,
		trino.Quote(WritableCatalog(parts[0])), parts[1], parts[2]))
	return err
}

// RegisterInStore puts a table in its store's own catalog from a metadata
// file (Iceberg's register_table: nothing copied), as it was given back.
func RegisterInStore(ctx context.Context, tr *trino.Client, from, metadataFile string) error {
	parts := strings.Split(from, ".")
	if len(parts) != 3 {
		return fmt.Errorf("from %q: as <store>.<schema>.<table>", from)
	}
	dir, file := path.Split(metadataFile)                                       // .../<table>/metadata/, <n>.metadata.json
	location := strings.TrimSuffix(path.Dir(strings.TrimSuffix(dir, "/")), "/") // .../<table>
	if path.Base(strings.TrimSuffix(dir, "/")) != "metadata" || file == "" {
		return fmt.Errorf("%s isn't a table's metadata file (<table>/metadata/<file>)", metadataFile)
	}
	_, err := tr.Exec(ctx, fmt.Sprintf(`CALL %s.system.register_table(schema_name => '%s', table_name => '%s', table_location => '%s', metadata_file_name => '%s')`,
		trino.Quote(WritableCatalog(parts[0])), parts[1], parts[2], strings.ReplaceAll(location, "'", "''"), strings.ReplaceAll(file, "'", "''")))
	return err
}
