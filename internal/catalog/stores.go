package catalog

import (
	"context"
	"fmt"
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
