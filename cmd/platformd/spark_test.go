package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Plans as Trino's EXPLAIN (TYPE LOGICAL, FORMAT JSON) renders them.
func sparkPlan(kids ...string) string {
	return `{"id":"9","name":"Output","descriptor":{"columnNames":"[id, region]"},"outputs":[],"details":[],"estimates":[],"children":[` +
		strings.Join(kids, ",") + `]}`
}

func sparkNode(name, table string, kids ...string) string {
	d := `{}`
	if table != "" {
		d = `{"table":"` + table + `"}`
	}
	return `{"id":"1","name":"` + name + `","descriptor":` + d + `,"outputs":[],"details":[],"estimates":[],"children":[` + strings.Join(kids, ",") + `]}`
}

func TestSparkFull(t *testing.T) {
	scan := sparkNode("TableScan", "iceberg:sales.orders$data@4711")
	for _, c := range []struct {
		name, plan string
		full       bool
	}{
		{"the table alone", sparkPlan(scan), true},
		{"a distributed plan's fragments", `{"0":` + sparkPlan(sparkNode("RemoteSource", "")) + `,"1":` + sparkNode("TableScan", "iceberg:sales.orders$data@1") + `}`, false},
		{"a row filter", sparkPlan(sparkNode("ScanFilter", "iceberg:sales.orders$data@4711")), false},
		{"a row filter above the scan", sparkPlan(sparkNode("Filter", "", scan)), false},
		{"a row filter pushed into the scan", sparkPlan(sparkNode("TableScan", "iceberg:sales.orders$data@4711 constraint on [region]")), false},
		{"a column mask", sparkPlan(sparkNode("ScanProject", "iceberg:sales.orders$data@4711")), false},
		{"a mask above the scan", sparkPlan(sparkNode("Project", "", scan)), false},
		{"an empty answer", sparkPlan(sparkNode("Values", "")), false},
		{"not JSON", "Output[columnNames = [id]]", false},
	} {
		if got, why := sparkFull(c.plan); got != c.full {
			t.Errorf("%s: full %v (%s), want %v", c.name, got, why, c.full)
		}
	}
}

func TestSparkPolicy(t *testing.T) {
	b, err := sparkPolicy([]sparkTable{
		{Name: "sales.orders", folders: []string{"s3://warehouse/sales/orders-0a1b"}},
		{Name: "sales.notes", Write: true, folders: []string{"s3://warehouse/sales/notes-2c3d/", "s3a://warehouse/elsewhere/notes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Statement []struct {
			Action, Resource []string
		}
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"s3:GetObject"}, {"s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}}
	resources := []string{
		"arn:aws:s3:::warehouse/sales/orders-0a1b/* arn:aws:s3:::warehouse/sales/notes-2c3d/* arn:aws:s3:::warehouse/elsewhere/notes/*",
		"arn:aws:s3:::warehouse/sales/notes-2c3d/* arn:aws:s3:::warehouse/elsewhere/notes/*"}
	if len(p.Statement) != 2 {
		t.Fatalf("policy %s", b)
	}
	for i, st := range p.Statement {
		if strings.Join(st.Action, " ") != strings.Join(want[i], " ") || strings.Join(st.Resource, " ") != resources[i] {
			t.Errorf("statement %d: %v on %v", i, st.Action, st.Resource)
		}
	}
	if strings.Contains(strings.Join(p.Statement[1].Resource, " "), "orders") {
		t.Errorf("a table read only is written: %s", b)
	}
	if _, err := sparkPolicy([]sparkTable{{Name: "x.y", folders: []string{"file:///tmp/y"}}}); err == nil {
		t.Error("a folder outside a bucket was taken")
	}
}

func TestSparkEmpty(t *testing.T) {
	if !sparkEmpty(sparkPlan(sparkNode("Values", ""))) {
		t.Error("an empty table's plan isn't Values")
	}
	for _, p := range []string{sparkPlan(sparkNode("TableScan", "iceberg:x.y$data@1")),
		sparkPlan(sparkNode("Filter", "", sparkNode("Values", ""))), "not JSON"} {
		if sparkEmpty(p) {
			t.Errorf("taken for an empty table: %s", p)
		}
	}
}

func TestSparkCreate(t *testing.T) {
	var n sparkNew
	if err := json.Unmarshal([]byte(`{"name":"sales.order_counts","columns":[
		{"name":"customer_id","type":"bigint"},{"name":"Total \"x\"","type":"decimal(10,2)"},
		{"name":"at","type":"timestamp(6) with time zone"},{"name":"tags","type":"array(row(k varchar, v integer))"}]}`), &n); err != nil {
		t.Fatal(err)
	}
	got, err := sparkCreate(n)
	want := `CREATE TABLE IF NOT EXISTS "iceberg"."sales"."order_counts" ("customer_id" bigint, "Total ""x""" decimal(10,2), ` +
		`"at" timestamp(6) with time zone, "tags" array(row(k varchar, v integer)))`
	if err != nil || got != want {
		t.Fatalf("got %q, %v\nwant %q", got, err, want)
	}
	for _, bad := range []string{`{"name":"order_counts","columns":[{"name":"a","type":"bigint"}]}`,
		`{"name":"sales.Bad-Name","columns":[{"name":"a","type":"bigint"}]}`,
		`{"name":"sales.t","columns":[]}`,
		`{"name":"sales.t","columns":[{"name":"a","type":"bigint); DROP TABLE x; --"}]}`,
		`{"name":"sales.t","columns":[{"name":"a","type":"varchar'"}]}`,
		`{"name":"sales.t","columns":[{"name":"a","type":"decimal(10,2"}]}`} {
		var b sparkNew
		if err := json.Unmarshal([]byte(bad), &b); err != nil {
			t.Fatal(err)
		}
		if stmt, err := sparkCreate(b); err == nil {
			t.Errorf("%s made %s", bad, stmt)
		}
	}
}
