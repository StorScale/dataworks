package catalog

import "testing"

func TestTableLocationKeepsTheSchemesSlashes(t *testing.T) {
	loc, file, err := tableLocation("s3a://data-acquisition/warehouse/cpa_distance_analysis/metadata/00001-1e1d.metadata.json")
	if err != nil || loc != "s3a://data-acquisition/warehouse/cpa_distance_analysis" || file != "00001-1e1d.metadata.json" {
		t.Fatalf("got %q %q %v", loc, file, err)
	}
	for _, bad := range []string{"", "x.metadata.json", "s3://b/t/data/x.metadata.json", "s3://b/t/metadata/", "s3://b/t/metadata/x.avro"} {
		if _, _, err := tableLocation(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
