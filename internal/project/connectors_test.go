package project

import (
	"strings"
	"testing"
)

// Gong, HighSpot and SAP are connectors as Salesforce is: an import of each names
// its objects (from the connector's) and a connection.
func TestTheConnectorsImports(t *testing.T) {
	parse := func(im string) error {
		_, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},
			"pipelines":{"serviceAccount":"airflow-pipelines"},"imports":[` + im + `]}}`))
		return err
	}
	for _, c := range []struct {
		im, want string // want: "" when it's accepted, else a part of the refusal
	}{
		{`{"name":"calls","source":"gong","connection":"sales-gong","gong":{"objects":["users","calls","call_transcripts"],"since":"2026-01-01"}}`, ""},
		{`{"name":"content","source":"highspot","connection":"sales-hs","highspot":{"objects":["spots","items","users"]}}`, ""},
		{`{"name":"calls","source":"gong","connection":"sales-gong","gong":{"objects":["deals"]}}`, `"deals" isn't one of Gong's objects`},
		{`{"name":"calls","source":"gong","gong":{"objects":["calls"]}}`, "a gong import names its objects (gong.objects) and a connection (kind gong)"},
		{`{"name":"content","source":"highspot","connection":"sales-hs","highspot":{"objects":["spots"]},"gong":{"objects":["calls"]}}`, "a highspot import has no gong part"},
		{`{"name":"calls","source":"gong","connection":"sales-gong","gong":{"objects":["calls"],"since":"last year"}}`, "since \"last year\": a date"},
		{`{"name":"crm","source":"salesforce","connection":"sales-sf","salesforce":{"objects":["account"]}}`, ""},
		{`{"name":"erp","source":"sap","connection":"sales-sap","sap":{"objects":["sales_orders","sales_order_items","business_partners"],"since":"2025-01-01"}}`, ""},
		{`{"name":"erp","source":"sap","connection":"sales-sap","sap":{"objects":["A_SalesOrder"]}}`, `"A_SalesOrder" isn't one of SAP S/4HANA's objects`},
		{`{"name":"erp","source":"sap","sap":{"objects":["products"]}}`, "a sap import names its objects (sap.objects) and a connection (kind sap)"},
		{`{"name":"erp","source":"gong","connection":"sales-gong","gong":{"objects":["calls"]},"sap":{"objects":["products"]}}`, "a gong import has no sap part"},
	} {
		err := parse(c.im)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.im, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.im, err, c.want)
		}
	}
	if c := ConnectorOf("gong"); c == nil || c.Connection != "gong" || KindOf("gong") == nil || KindOf("highspot") == nil {
		t.Error("the connectors and their connection kinds")
	}
	for _, c := range Connectors {
		if KindOf(c.Connection) == nil {
			t.Errorf("%s signs in with %s, which isn't a connection kind", c.Source, c.Connection)
		}
		for _, o := range c.Incremental {
			if !strings.Contains(" "+strings.Join(c.Objects, " ")+" ", " "+o+" ") {
				t.Errorf("%s: %s is copied by its changes but isn't one of its objects", c.Source, o)
			}
		}
	}
}
