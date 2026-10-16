package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCatalog answers the three questions Flow asks: a schema's tables, a
// table's lineage, and a table's checks.
func fakeCatalog(t *testing.T) *httptest.Server {
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tables", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer the-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("databaseSchema") != "trino.iceberg.sales" {
			http.NotFound(w, r)
			return
		}
		reply(w, map[string]any{"data": []any{
			map[string]any{"id": "t1", "name": "orders", "fullyQualifiedName": "trino.iceberg.sales.orders",
				"columns": []any{map[string]any{"name": "id", "dataType": "BIGINT"}, map[string]any{"name": "region", "dataType": "VARCHAR", "dataTypeDisplay": "varchar"}}},
			map[string]any{"id": "t2", "name": "orders_by_region", "fullyQualifiedName": "trino.iceberg.sales.orders_by_region",
				"columns": []any{map[string]any{"name": "region"}, map[string]any{"name": "orders"}}},
			map[string]any{"id": "t3", "name": "gone", "fullyQualifiedName": "trino.iceberg.sales.gone", "deleted": true},
		}})
	})
	edge := map[string]any{"fromEntity": "t1", "toEntity": "t2", "lineageDetails": map[string]any{
		"pipeline": map[string]any{"name": "airflow-sales_ingest", "displayName": "sales_ingest"},
		"columnsLineage": []any{
			map[string]any{"fromColumns": []string{"trino.iceberg.sales.orders.region"}, "toColumn": "trino.iceberg.sales.orders_by_region.region"},
			map[string]any{"fromColumns": []string{"trino.iceberg.sales.orders.id"}, "toColumn": "trino.iceberg.sales.orders_by_region.orders"},
		}}}
	mux.HandleFunc("GET /api/v1/lineage/table/name/{fqn}", func(w http.ResponseWriter, r *http.Request) {
		nodes := []any{
			map[string]any{"id": "t1", "fullyQualifiedName": "trino.iceberg.sales.orders"},
			map[string]any{"id": "t2", "fullyQualifiedName": "trino.iceberg.sales.orders_by_region"},
			map[string]any{"id": "x9", "fullyQualifiedName": "trino.iceberg.marketing.campaigns"},
		}
		reply(w, map[string]any{"nodes": nodes, "downstreamEdges": []any{edge}, "upstreamEdges": []any{edge,
			map[string]any{"fromEntity": "x9", "toEntity": "t1", "lineageDetails": map[string]any{}}}})
	})
	mux.HandleFunc("GET /api/v1/dataQuality/testCases", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("entityLink") != "<#E::table::trino.iceberg.sales.orders>" || r.URL.Query().Get("includeAllTests") != "true" {
			reply(w, map[string]any{"data": []any{}})
			return
		}
		reply(w, map[string]any{"data": []any{
			map[string]any{"name": "sales_orders_card_number_notNull", "testCaseResult": map[string]any{
				"testCaseStatus": "Failed", "result": "Found nullCount=1. It should be 0", "timestamp": 1}},
			map[string]any{"name": "sales_orders_id_unique"},
		}})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestFlow(t *testing.T) {
	ts := fakeCatalog(t)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("the-token\n"), 0o600)
	flow, err := New(ts.URL, token, "trino").Flow(context.Background(), "iceberg", "sales")
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Tables) != 3 {
		t.Fatalf("tables: %+v (want orders, orders_by_region, and the other project's campaigns; not the deleted one)", flow.Tables)
	}
	var orders, external *Table
	for i := range flow.Tables {
		switch flow.Tables[i].Name {
		case "orders":
			orders = &flow.Tables[i]
		case "trino.iceberg.marketing.campaigns":
			external = &flow.Tables[i]
		}
	}
	if orders == nil || len(orders.Checks) != 2 || orders.Checks[0].Status != "Failed" || orders.Checks[1].Status != "" {
		t.Errorf("orders' checks: %+v", orders)
	}
	if orders != nil && (len(orders.Columns) != 2 || orders.Columns[0] != (Column{"id", "bigint"}) || orders.Columns[1].Type != "varchar") {
		t.Errorf("orders' columns: %+v, want names and lowercase types", orders.Columns)
	}
	if external == nil || !external.External {
		t.Errorf("a table upstream in another project should be in the flow, marked external: %+v", external)
	}
	if len(flow.Edges) != 2 {
		t.Fatalf("edges: %+v (the same edge, upstream and downstream, counts once)", flow.Edges)
	}
	var made *Edge
	for i := range flow.Edges {
		if flow.Edges[i].To == "trino.iceberg.sales.orders_by_region" {
			made = &flow.Edges[i]
		}
	}
	if made == nil || made.Pipeline != "sales_ingest" || len(made.Columns) != 2 || made.Columns[1].To != "orders" || made.Columns[1].From[0] != "id" {
		t.Errorf("orders -> orders_by_region: %+v", made)
	}
}

func TestFlowBeforeTheCatalogKnowsTheProject(t *testing.T) {
	ts := fakeCatalog(t)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("the-token"), 0o600)
	flow, err := New(ts.URL, token, "trino").Flow(context.Background(), "iceberg", "brand_new")
	if err != nil || len(flow.Tables) != 0 || flow.Edges == nil {
		t.Fatalf("a namespace the catalog hasn't ingested: %+v, %v (want an empty flow)", flow, err)
	}
	if _, err := New(ts.URL, filepath.Join(t.TempDir(), "missing"), "trino").Flow(context.Background(), "iceberg", "sales"); err == nil {
		t.Error("no token: want an error saying the catalog isn't set up")
	}
}

func TestATablesLastChangeIsItsCurrentSnapshots(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/iceberg/v1/config":
			if r.URL.Query().Get("warehouse") != "warehouse" {
				t.Errorf("warehouse %q", r.URL.Query().Get("warehouse"))
			}
			w.Write([]byte(`{"defaults": {"prefix": "main|warehouse"}, "overrides": {}}`))
		case "/iceberg/v1/main%7Cwarehouse/namespaces/sales/tables/orders":
			w.Write([]byte(`{"metadata": {"current-snapshot-id": 2, "snapshots": [
				{"snapshot-id": 1, "timestamp-ms": 1760000000000}, {"snapshot-id": 2, "timestamp-ms": 1760640000000}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	at, err := LastChange(context.Background(), ts.Client(), ts.URL+"/iceberg", "warehouse", "sales", "orders")
	if err != nil || !at.Equal(time.UnixMilli(1760640000000)) {
		t.Errorf("%v %v, want the current snapshot's time", at, err)
	}
	if _, err := LastChange(context.Background(), ts.Client(), ts.URL+"/iceberg", "warehouse", "sales", "nothing"); err == nil {
		t.Error("no such table: want an error")
	}
}
