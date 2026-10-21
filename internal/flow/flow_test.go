package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var tables = map[string][]Column{
	"orders":    {{"order_id", "bigint"}, {"customer_id", "bigint"}, {"region", "varchar"}, {"amount", "decimal(10,2)"}, {"ordered_on", "date"}, {"test", "boolean"}},
	"suppliers": {{"id", "varchar"}, {"name", "varchar"}, {"region", "varchar"}},
}

func env() Env {
	return Env{Namespace: "sales", Columns: func(cat, ns, t string) ([]Column, error) {
		if cat != "iceberg" || ns != "sales" {
			return nil, fmt.Errorf("no namespace %s.%s", cat, ns)
		}
		cols, ok := tables[t]
		if !ok {
			return nil, fmt.Errorf("no table %s", t)
		}
		return cols, nil
	}}
}

// A flow of every kind of step: orders filtered, retyped, deduplicated,
// joined to suppliers, grouped by region and week, with a computed column,
// a window, a pivot and an unpivot, and a union, ending in a table.
func wide() *Flow {
	return &Flow{Name: "weekly", Schedule: "0 6 * * 1", Steps: []Step{
		{ID: "orders", Kind: Input, Table: "orders"},
		{ID: "suppliers", Kind: Input, Table: "suppliers"},
		{ID: "real", Kind: Filter, Inputs: []string{"orders"}, Where: "not test"},
		{ID: "typed", Kind: Retype, Inputs: []string{"real"}, Columns: []ColumnSetting{{Name: "customer_id", Type: "varchar"}}},
		{ID: "once", Kind: Dedupe, Inputs: []string{"typed"}, By: []string{"order_id"}, Order: []Ordering{{Column: "ordered_on", Desc: true}}},
		{ID: "named", Kind: Select, Inputs: []string{"suppliers"}, Columns: []ColumnSetting{{Name: "id"}, {Name: "name", As: "supplier"}}},
		{ID: "joined", Kind: Join, Inputs: []string{"once", "named"}, Join: "left", On: []Pair{{Left: "customer_id", Right: "id"}}},
		{ID: "week", Kind: Formula, Inputs: []string{"joined"}, Columns: []ColumnSetting{{Name: "week", Expression: "date_trunc('week', ordered_on)", Type: "date"}}},
		{ID: "ranked", Kind: Window, Inputs: []string{"week"}, Partition: []string{"region"}, Order: []Ordering{{Column: "amount", Desc: true}},
			Windows: []WindowFunction{{Function: "rank", As: "rank_in_region"}, {Function: "running_sum", Column: "amount", As: "running"}, {Function: "lag", Column: "amount", As: "previous", Offset: 2}}},
		{ID: "by_week", Kind: Group, Inputs: []string{"ranked"}, By: []string{"region", "week"},
			Aggregates: []Aggregate{{Function: "sum", Column: "amount"}, {Function: "count"}, {Function: "count_distinct", Column: "customer_id", As: "customers"}, {Function: "avg", Column: "order_id", As: "avg_id"}}},
		{ID: "wide", Kind: Pivot, Inputs: []string{"by_week"}, By: []string{"week"}, Column: "region", Values: []string{"EU", "North America"}, Value: "sum_amount"},
		{ID: "tall", Kind: Unpivot, Inputs: []string{"wide"}, Unpivot: []string{"eu", "north_america"}, Name: "region", Value: "revenue"},
		{ID: "again", Kind: Union, Inputs: []string{"tall", "tall"}},
		{ID: "out", Kind: Output, Inputs: []string{"again"}, Table: "weekly_revenue", Checks: []Check{{Column: "week", Test: "not_null"}, {Column: "region", Test: "accepted_values", Values: []string{"eu", "north_america"}}}},
	}}
}

func TestCompileEveryKind(t *testing.T) {
	c, err := Compile(wide(), env())
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Errors(); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := map[string]string{
		"real":    "order_id bigint, customer_id bigint, region varchar, amount decimal(10,2), ordered_on date, test boolean",
		"typed":   "order_id bigint, customer_id varchar, region varchar, amount decimal(10,2), ordered_on date, test boolean",
		"named":   "id varchar, supplier varchar",
		"joined":  "order_id bigint, customer_id varchar, region varchar, amount decimal(10,2), ordered_on date, test boolean, id varchar, supplier varchar",
		"week":    "order_id bigint, customer_id varchar, region varchar, amount decimal(10,2), ordered_on date, test boolean, id varchar, supplier varchar, week date",
		"ranked":  "order_id bigint, customer_id varchar, region varchar, amount decimal(10,2), ordered_on date, test boolean, id varchar, supplier varchar, week date, rank_in_region bigint, running decimal(38,2), previous decimal(10,2)",
		"by_week": "region varchar, week date, sum_amount decimal(38,2), count bigint, customers bigint, avg_id double",
		"wide":    "week date, eu decimal(38,2), north_america decimal(38,2)",
		"tall":    "week date, region varchar, revenue decimal(38,2)",
		"again":   "week date, region varchar, revenue decimal(38,2)",
		"out":     "week date, region varchar, revenue decimal(38,2)",
	}
	for id, w := range want {
		if got := describe(c.Steps[id].Columns); got != w {
			t.Errorf("%s: columns\n got %s\nwant %s", id, got, w)
		}
	}
	for id, part := range map[string]string{
		"orders":  "{{ source('weekly__iceberg_sales', 'orders') }}",
		"typed":   `cast("customer_id" as varchar) as "customer_id"`,
		"once":    `row_number() over (partition by "order_id" order by "ordered_on" desc)`,
		"named":   `"name" as "supplier"`,
		"joined":  `left join {{ ref('weekly__named') }} as r on l."customer_id" = r."id"`,
		"week":    `cast((date_trunc('week', ordered_on)) as date) as "week"`,
		"ranked":  `lag("amount", 2) over (partition by "region" order by "amount" desc) as "previous"`,
		"by_week": `count(distinct "customer_id") as "customers"`,
		"wide":    `sum(case when "region" = 'North America' then "sum_amount" end) as "north_america"`,
		"tall":    `cross join unnest(array['eu', 'north_america'], array["eu", "north_america"]) as t ("region", "revenue")`,
		"again":   "union all",
		"out":     "select *\nfrom {{ ref('weekly__again') }}",
	} {
		if !strings.Contains(c.Steps[id].SQL, part) {
			t.Errorf("%s: SQL lacks %q:\n%s", id, part, c.Steps[id].SQL)
		}
	}
	if c.Steps["out"].Model != "weekly_revenue" || c.Steps["real"].Model != "weekly__real" {
		t.Errorf("models: %s, %s", c.Steps["out"].Model, c.Steps["real"].Model)
	}
}

func describe(cols []Column) string {
	var parts []string
	for _, c := range cols {
		parts = append(parts, c.Name+" "+c.Type)
	}
	return strings.Join(parts, ", ")
}

// A step that can't work says why, before anything runs, and the steps
// after it say which input failed.
func TestCompileRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		step Step
		want string
	}{
		{"join types", Step{ID: "j", Kind: Join, Inputs: []string{"orders", "suppliers"}, On: []Pair{{Left: "customer_id", Right: "id"}}},
			"join on orders.customer_id (bigint) and suppliers.id (varchar): their types differ"},
		{"missing column", Step{ID: "f", Kind: Formula, Inputs: []string{"orders"}, Columns: []ColumnSetting{{Name: "x", Expression: "amount * 2"}}, Where: ""},
			""}, // fine: an expression isn't checked for its columns here, Trino does
		{"group by dropped column", Step{ID: "g", Kind: Group, Inputs: []string{"named"}, By: []string{"region"}, Aggregates: []Aggregate{{Function: "count"}}},
			"group by: no column region in its input (it has id, supplier)"},
		{"sum of text", Step{ID: "g", Kind: Group, Inputs: []string{"orders"}, Aggregates: []Aggregate{{Function: "sum", Column: "region"}}},
			"sum of region, which is varchar, not a number"},
		{"union shape", Step{ID: "u", Kind: Union, Inputs: []string{"orders", "suppliers"}},
			"union: input suppliers has no column order_id"},
		{"rank unordered", Step{ID: "w", Kind: Window, Inputs: []string{"orders"}, Windows: []WindowFunction{{Function: "rank", As: "r"}}},
			"rank needs an order"},
		{"bad type", Step{ID: "r", Kind: Retype, Inputs: []string{"orders"}, Columns: []ColumnSetting{{Name: "amount", Type: "money"}}},
			`"money" isn't a Trino type`},
		{"injection", Step{ID: "f", Kind: Filter, Inputs: []string{"orders"}, Where: "1=1; drop table orders"},
			`an expression can't contain ";"`},
		{"pivot values", Step{ID: "p", Kind: Pivot, Inputs: []string{"orders"}, Column: "region", Value: "amount"},
			"say which values of region become columns"},
		{"no table", Step{ID: "i", Kind: Input, Table: "nothing"},
			"no table nothing"},
	} {
		f := &Flow{Name: "f", Steps: []Step{
			{ID: "orders", Kind: Input, Table: "orders"},
			{ID: "suppliers", Kind: Input, Table: "suppliers"},
			{ID: "named", Kind: Select, Inputs: []string{"suppliers"}, Columns: []ColumnSetting{{Name: "id"}, {Name: "name", As: "supplier"}}},
			tc.step,
			{ID: "out", Kind: Output, Inputs: []string{tc.step.ID}, Table: "t"},
		}}
		c, err := Compile(f, env())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := c.Steps[tc.step.ID].Error
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: unexpected error %q", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: error %q, want %q", tc.name, got, tc.want)
		}
		if out := c.Steps["out"].Error; out != "its input "+tc.step.ID+" has an error" {
			t.Errorf("%s: the output's error: %q", tc.name, out)
		}
	}
}

func TestCheckShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []Step
		want  string
	}{
		{"two outputs", []Step{{ID: "a", Kind: Input, Table: "orders"}, {ID: "o", Kind: Output, Inputs: []string{"a"}, Table: "t"}, {ID: "p", Kind: Output, Inputs: []string{"a"}, Table: "u"}}, "a flow has one output step; this one has 2"},
		{"circle", []Step{{ID: "a", Kind: Filter, Inputs: []string{"b"}, Where: "x"}, {ID: "b", Kind: Filter, Inputs: []string{"a"}, Where: "x"}, {ID: "o", Kind: Output, Inputs: []string{"a"}, Table: "t"}},
			"go round in a circle"},
		{"join arity", []Step{{ID: "a", Kind: Input, Table: "orders"}, {ID: "j", Kind: Join, Inputs: []string{"a"}}, {ID: "o", Kind: Output, Inputs: []string{"j"}, Table: "t"}},
			"step j (join): takes 2 inputs, not 1"},
		{"unknown input", []Step{{ID: "o", Kind: Output, Inputs: []string{"x"}, Table: "t"}}, "its input x isn't a step"},
		{"bad name", []Step{{ID: "Bad Name", Kind: Input}}, `"Bad Name" isn't`},
		{"bad kind", []Step{{ID: "a", Kind: "magic"}}, `no step kind "magic"`},
	} {
		err := (&Flow{Name: "f", Steps: tc.steps}).Check()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The files round-trip: written with headers, read back as the same steps;
// a model changed by hand reads back as a code step with its refs.
func TestFilesAndRead(t *testing.T) {
	f := wide()
	f.Steps[2].Description = "Drop the test rows"
	f.Steps[2].X, f.Steps[2].Y = 120, 40
	c, err := Compile(f, env())
	if err != nil {
		t.Fatal(err)
	}
	files := Files(c)
	for _, p := range []string{"models/flows/weekly/weekly__real.sql", "models/flows/weekly/weekly_revenue.sql", "models/flows/weekly/sources.yml", "models/flows/weekly/schema.yml"} {
		if files[p] == "" {
			t.Errorf("no file %s (have %d)", p, len(files))
		}
	}
	real := files["models/flows/weekly/weekly__real.sql"]
	if !strings.HasPrefix(real, "-- storscale-step:\n--   kind: filter\n--   inputs:\n--     - orders\n--   where: not test\n--   sql: ") {
		t.Errorf("header:\n%s", real)
	}
	if !strings.Contains(real, "{{ config(materialized='ephemeral') }}\nselect *\nfrom {{ ref('weekly__orders') }}\nwhere not test") {
		t.Errorf("model:\n%s", real)
	}
	if out := files["models/flows/weekly/weekly_revenue.sql"]; !strings.Contains(out, "materialized='table'") {
		t.Errorf("the output isn't a table:\n%s", out)
	}
	if src := files["models/flows/weekly/sources.yml"]; !strings.Contains(src, "name: weekly__iceberg_sales\n") || !strings.Contains(src, "schema: sales") || !strings.Contains(src, "- name: orders") || !strings.Contains(src, "- name: suppliers") {
		t.Errorf("sources:\n%s", src)
	}
	if sch := files["models/flows/weekly/schema.yml"]; !strings.Contains(sch, "accepted_values") || !strings.Contains(sch, "description: Drop the test rows") || !strings.Contains(sch, "x: 120") {
		t.Errorf("schema:\n%s", sch)
	}

	folder := map[string]string{}
	for p, text := range files {
		folder[strings.TrimPrefix(p, "models/flows/weekly/")] = text
	}
	back, err := Read("weekly", folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Steps) != len(f.Steps) {
		t.Fatalf("%d steps back, want %d", len(back.Steps), len(f.Steps))
	}
	for i := range f.Steps {
		a, b := f.Steps[i], back.Steps[i]
		if a.ID != b.ID || a.Kind != b.Kind || b.Kind == Code {
			t.Errorf("step %d: %s %s back as %s %s", i, a.ID, a.Kind, b.ID, b.Kind)
		}
	}
	if s := back.Step("real"); s.Description != "Drop the test rows" || s.X != 120 || s.Where != "not test" || s.Inputs[0] != "orders" {
		t.Errorf("real read back as %+v", s)
	}
	if s := back.Step("out"); s.Table != "weekly_revenue" || len(s.Checks) != 2 || s.Checks[1].Values[1] != "north_america" {
		t.Errorf("out read back as %+v", s)
	}
	c2, err := Compile(back, env())
	if err != nil || len(c2.Errors()) > 0 {
		t.Fatalf("compiling what was read: %v %v", err, c2.Errors())
	}
	if Files(c2)["models/flows/weekly/weekly__ranked.sql"] != files["models/flows/weekly/weekly__ranked.sql"] {
		t.Error("the files aren't the same the second time")
	}

	// bob edits one model by hand (11.2): it's a code step from then on,
	// with its refs as inputs; the rest are still steps.
	folder["weekly__real.sql"] = strings.Replace(folder["weekly__real.sql"], "where not test", "where not test and amount > 0", 1)
	folder["extra.sql"] = "{{ config(materialized='ephemeral') }}\nselect * from {{ ref('weekly__once') }} where region = 'EU'\n"
	back, err = Read("weekly", folder)
	if err != nil {
		t.Fatal(err)
	}
	if s := back.Step("real"); s.Kind != Code || s.Inputs[0] != "orders" || !strings.Contains(s.SQL, "amount > 0") || !strings.HasPrefix(s.SQL, "-- storscale-step:") {
		t.Errorf("the changed model: %+v", s)
	}
	if s := back.Step("extra"); s == nil || s.Kind != Code || len(s.Inputs) != 1 || s.Inputs[0] != "once" || s.Table != "" {
		t.Errorf("the new model: %+v", s)
	}
	if s := back.Step("typed"); s.Kind != Retype {
		t.Errorf("typed is now %s", s.Kind)
	}
	c3, err := Compile(back, env())
	if err != nil {
		t.Fatal(err)
	}
	if r := c3.Steps["real"]; !r.Unknown || r.Error != "" {
		t.Errorf("the code step: %+v", r)
	}
	if r := c3.Steps["typed"]; r.Error != "" || !r.Unknown || len(r.Columns) != 0 {
		// Its input's columns aren't known, so its own can't be, until Trino describes the code step.
		t.Errorf("after the code step: %+v", r)
	}
	files3 := Files(c3)
	if files3["models/flows/weekly/weekly__real.sql"] != folder["weekly__real.sql"] {
		t.Error("a code step isn't written as it is")
	}
	if _, ok := files3["models/flows/weekly/weekly__typed.sql"]; ok {
		t.Error("a step after an undescribed code step is written (it should be left as it is)")
	}
	// With Trino's word on the code step's columns, the rest type again.
	e := env()
	e.CodeColumns = map[string][]Column{"real": tables["orders"], "extra": tables["orders"]}
	c4, _ := Compile(back, e)
	if got := describe(c4.Steps["typed"].Columns); !strings.HasPrefix(got, "order_id bigint, customer_id varchar") {
		t.Errorf("typed after the described code step: %s", got)
	}
}

func TestPreview(t *testing.T) {
	c, err := Compile(wide(), env())
	if err != nil {
		t.Fatal(err)
	}
	sampled := func(_, _, table string) float64 {
		if table == "orders" {
			return 2.5
		}
		return 100
	}
	sql, err := c.Preview("joined", sampled, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{
		`with "orders" as (`, `from "iceberg"."sales"."orders" tablesample bernoulli (2.5)`, `from "iceberg"."sales"."suppliers"` + "\n",
		`"joined" as (`, `left join "named" as r`, "select *\nfrom \"joined\"\nlimit 50",
	} {
		if !strings.Contains(sql, part) {
			t.Errorf("preview lacks %q:\n%s", part, sql)
		}
	}
	if strings.Contains(sql, `"wide" as`) || strings.Contains(sql, "{{") {
		t.Errorf("preview has steps it doesn't need, or Jinja:\n%s", sql)
	}
	// A code step previews with its refs and sources made CTEs and tables.
	f := wide()
	f.Steps[2] = Step{ID: "real", Kind: Code, Inputs: []string{"orders"},
		SQL: "{{ config(materialized='ephemeral') }}\nselect * from {{ ref('weekly__orders') }} where not test\nunion all select * from {{ source('weekly__iceberg_sales', 'orders') }} where test\n"}
	c, _ = Compile(f, env())
	sql, err = c.Preview("real", sampled, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `select * from "orders" where not test`) || !strings.Contains(sql, `select * from "iceberg"."sales"."orders" tablesample bernoulli (2.5) where test`) || strings.Contains(sql, "config") {
		t.Errorf("code preview:\n%s", sql)
	}
	f.Steps[2].SQL = "select {{ var('x') }} from {{ ref('weekly__orders') }}"
	c, _ = Compile(f, env())
	if _, err = c.Preview("real", sampled, 10); err == nil || !strings.Contains(err.Error(), "Jinja") {
		t.Errorf("Jinja: %v", err)
	}
	// A step with an error has no preview, nor do those after it.
	f = wide()
	f.Steps[6].On[0].Right = "nothing"
	c, _ = Compile(f, env())
	if _, err = c.Preview("week", sampled, 10); err == nil || !strings.Contains(err.Error(), "step joined: join, on the right (named): no column nothing") {
		t.Errorf("after an error: %v", err)
	}
	if _, err = c.Preview("named", sampled, 10); err != nil {
		t.Errorf("beside an error: %v", err)
	}
}

func TestDAG(t *testing.T) {
	d := DAG("sales", "weekly", "0 6 * * 1", "alice", "dbt", nil)
	for _, part := range []string{`dag_id="flow_weekly"`, `schedule="0 6 * * 1"`, `dbt.build(project="sales", path="dbt", select="flows.weekly")`, "from airflow.sdk import dag", "max_active_runs=1"} {
		if !strings.Contains(d, part) {
			t.Errorf("DAG lacks %q:\n%s", part, d)
		}
	}
	if !strings.Contains(DAG("sales", "weekly", "", "alice", "dbt", nil), "schedule=None") {
		t.Error("no schedule isn't None")
	}
}

// A metric step's SQL is the semantic layer's; unknown until someone can ask it.
func TestMetricStep(t *testing.T) {
	f := &Flow{Name: "rev", Steps: []Step{
		{ID: "revenue", Kind: Metric, Metrics: []string{"revenue"}, By: []string{"order__region"}, Where: "x"},
		{ID: "eu", Kind: Filter, Inputs: []string{"revenue"}, Where: "order__region = 'EU'"},
		{ID: "out", Kind: Output, Inputs: []string{"eu"}, Table: "revenue_eu"},
	}}
	e := env()
	var asked [][]string
	e.MetricSQL = func(metrics, by []string, where string) (string, []Column, error) {
		asked = append(asked, append(append(metrics, by...), where))
		return "SELECT region AS order__region, SUM(amount) AS revenue\nFROM iceberg.sales.orders\nGROUP BY 1\n", []Column{{"order__region", "varchar"}, {"revenue", "double"}}, nil
	}
	c, err := Compile(f, e)
	if err != nil || len(c.Errors()) > 0 {
		t.Fatalf("%v %v", err, c.Errors())
	}
	if got := describe(c.Steps["out"].Columns); got != "order__region varchar, revenue double" || len(asked) != 1 || asked[0][2] != "x" {
		t.Errorf("columns %s, asked %v", got, asked)
	}
	sql, err := c.Preview("eu", func(_, _, _ string) float64 { return 100 }, 10)
	if err != nil || !strings.Contains(sql, "\"revenue\" as (\n  SELECT region AS order__region") || !strings.Contains(sql, `from "revenue"`) {
		t.Errorf("preview: %v\n%s", err, sql)
	}
	m := Files(c)["models/flows/rev/rev__revenue.sql"]
	if !strings.HasPrefix(m, "-- storscale-step:\n--   kind: metric\n--   where: x\n--   by:\n--     - order__region\n--   metrics:\n--     - revenue\n--   sql: ") || !strings.Contains(m, "materialized='ephemeral'") {
		t.Errorf("model:\n%s", m)
	}
	e.MetricSQL = func(_, _ []string, _ string) (string, []Column, error) { return "", nil, nil }
	c, _ = Compile(f, e)
	if r := c.Steps["revenue"]; !r.Unknown || r.Error != "" || !c.Steps["eu"].Unknown {
		t.Errorf("unknown: %+v %+v", r, c.Steps["eu"])
	}
	if _, err := c.Preview("revenue", nil, 10); err == nil || !strings.Contains(err.Error(), "isn't known yet") {
		t.Errorf("preview unknown: %v", err)
	}
	f.Steps[0].Metrics = nil
	c, _ = Compile(f, e)
	if r := c.Steps["revenue"]; r.Error != "a metric step names at least one metric" {
		t.Errorf("no metrics: %+v", r)
	}
}

// A prepare step: processors in order on a column, in place or as a new
// one, typed as each leaves it; and what it refuses.
func TestPrepareStep(t *testing.T) {
	f := &Flow{Name: "clean", Steps: []Step{
		{ID: "orders", Kind: Input, Table: "orders"},
		{ID: "clean", Kind: Prepare, Inputs: []string{"orders"}, Processors: []Processor{
			{Column: "region", Do: "trim"},
			{Column: "region", Do: "upper"},
			{Column: "region", Do: "standardize", Args: []string{"EUROPE=EU, USA=US"}},
			{Column: "region", Do: "length", As: "region_len"},
			{Column: "amount", Do: "fill", Args: []string{"0"}},
			{Column: "amount", Do: "bucket", Args: []string{"0", "1000", "10"}, As: "size"},
			{Column: "ordered_on", Do: "truncate_date", Args: []string{"month"}, As: "month"},
			{Column: "ordered_on", Do: "date_part", Args: []string{"year"}, As: "year"},
			{Column: "ordered_on", Do: "fill", Args: []string{"2026-01-01"}, As: "ordered_or"},
			{Column: "ordered_on", Do: "format_date", Args: []string{"%Y-%m"}},
			{Column: "customer_id", Do: "to_text"},
			{Column: "customer_id", Do: "pad_left", Args: []string{"8", "0"}},
			{Column: "test", Do: "not_null_flag", As: "has_test"},
		}},
		{ID: "out", Kind: Output, Inputs: []string{"clean"}, Table: "orders_clean"},
	}}
	c, err := Compile(f, env())
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Errors(); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	r := c.Steps["clean"]
	got := describe(r.Columns)
	want := "order_id bigint, customer_id varchar, region varchar, amount decimal(10,2), ordered_on varchar, test boolean, region_len bigint, size bigint, month date, year bigint, ordered_or date, has_test boolean"
	if got != want {
		t.Errorf("columns:\n got %s\nwant %s", got, want)
	}
	for _, frag := range []string{
		`case when upper(trim("region")) = 'EUROPE' then 'EU' when upper(trim("region")) = 'USA' then 'US' else upper(trim("region")) end as "region"`,
		`length(case when upper(trim("region"))`, `as "region_len"`,
		`coalesce("amount", 0) as "amount"`,
		`width_bucket(coalesce("amount", 0), 0, 1000, 10) as "size"`,
		`date_trunc('month', "ordered_on") as "month"`, `year("ordered_on") as "year"`,
		`date_format("ordered_on", '%Y-%m') as "ordered_on"`,
		`lpad(cast("customer_id" as varchar), 8, '0') as "customer_id"`,
		`("test" is not null) as "has_test"`,
		`coalesce("ordered_on", date '2026-01-01') as "ordered_or"`,
	} {
		if !strings.Contains(r.SQL, frag) {
			t.Errorf("SQL lacks %s:\n%s", frag, r.SQL)
		}
	}
	for _, bad := range []struct {
		p   Processor
		why string
	}{
		{Processor{Column: "region", Do: "shout"}, `no processor "shout"`},
		{Processor{Column: "nope", Do: "trim"}, "no column nope"},
		{Processor{Column: "region", Do: "left"}, "needs how many"},
		{Processor{Column: "region", Do: "left", Args: []string{"many"}}, "is a number"},
		{Processor{Column: "ordered_on", Do: "date_part", Args: []string{"fortnight"}}, "is one of year, quarter"},
		{Processor{Column: "region", Do: "standardize", Args: []string{"EU"}}, "from=to"},
		{Processor{Column: "region", Do: "trim", As: "Bad Name"}, "lowercase letters"},
		{Processor{Column: "region", Do: "trim", Args: []string{"x"}}, "takes 0 argument(s)"},
	} {
		f.Steps[1].Processors = []Processor{bad.p}
		c, _ := Compile(f, env())
		if errs := c.Errors(); len(errs) == 0 || !strings.Contains(errs[0], bad.why) {
			t.Errorf("%v: wanted a refusal with %q, got %v", bad.p, bad.why, errs)
		}
	}
	// An argument that could be more than a value is a literal, never SQL.
	f.Steps[1].Processors = []Processor{{Column: "region", Do: "replace", Args: []string{"'; drop table x; --", "y"}}}
	c, _ = Compile(f, env())
	if errs := c.Errors(); len(errs) != 0 || !strings.Contains(c.Steps["clean"].SQL, `replace("region", '''; drop table x; --', 'y')`) {
		t.Errorf("a literal argument: %v\n%s", errs, c.Steps["clean"].SQL)
	}
}

type fakeAsker struct {
	answers func(prompt string) string
	prompts []string
}

func (a *fakeAsker) Ask(_ context.Context, prompt string) (string, float64, error) {
	a.prompts = append(a.prompts, prompt)
	return a.answers(prompt), 0.001, nil
}

// An llm step: typed as its input plus the answer's column, its table a
// source the model reads, its DAG asking before dbt builds; and the asking
// itself, in batches, with the labels checked.
func TestLLMStep(t *testing.T) {
	f := &Flow{Name: "moods", Steps: []Step{
		{ID: "orders", Kind: Input, Table: "orders"},
		{ID: "mood", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "classify", Values: []string{"east", "west"},
			Prompt: "Which side of the Atlantic is the region on?", Name: "side"},
		{ID: "out", Kind: Output, Inputs: []string{"mood"}, Table: "orders_sided"},
	}}
	c, err := Compile(f, env())
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Errors(); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := describe(c.Steps["mood"].Columns); !strings.HasSuffix(got, "test boolean, side varchar") {
		t.Errorf("columns: %s", got)
	}
	if sql := c.Steps["mood"].SQL; !strings.Contains(sql, "{{ source('moods__iceberg_sales', 'moods__mood') }}") {
		t.Errorf("the model reads the platform's table: %s", sql)
	}
	files := Files(c)
	if src := files["models/flows/moods/sources.yml"]; !strings.Contains(src, "moods__mood") {
		t.Errorf("sources.yml lacks the step's table:\n%s", src)
	}
	dag := DAG("sales", "moods", "", "alice", "dbt", f.LLMSteps())
	for _, frag := range []string{"from storscale import dbt, llm", `llm.run(project="sales", flow="moods", step="mood")`, "asked >> dbt.build("} {
		if !strings.Contains(dag, frag) {
			t.Errorf("DAG lacks %s:\n%s", frag, dag)
		}
	}
	// A later step's preview leaves the column empty.
	pv, err := c.Preview("out", func(_, _, _ string) float64 { return 100 }, 10)
	if err != nil || !strings.Contains(pv, `cast(null as varchar) as "side"`) {
		t.Errorf("preview: %v\n%s", err, pv)
	}
	// Asking: two batches of twenty and one of five, the labels checked.
	rows := make([][]any, 45)
	for i := range rows {
		rows[i] = []any{float64(i), "EU"}
	}
	ask := &fakeAsker{answers: func(prompt string) string {
		n := strings.Count(prompt, "\n") - 3 // the instruction, the prompt, the shape; then the items
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, []string{"EAST", "west", "north"}[i%3])
		}
		b, _ := json.Marshal(out)
		return "Sure:\n" + string(b)
	}}
	res, err := LLMApply(context.Background(), f.Step("mood"), []string{"order_id", "region"}, rows, ask)
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 3 || len(res.Rows) != 45 || res.Unread != 13 || res.Cost < 0.0029 { // "north" at every third place of each batch: 6, 6 and 1
		t.Errorf("batches %d rows %d unread %d cost %v", res.Batches, len(res.Rows), res.Unread, res.Cost)
	}
	if res.Rows[0][2] != "east" || res.Rows[1][2] != "west" || res.Rows[2][2] != nil {
		t.Errorf("answers: %v %v %v", res.Rows[0][2], res.Rows[1][2], res.Rows[2][2])
	}
	if p := ask.prompts[0]; !strings.HasPrefix(p, "Classify each item below as exactly one of: east, west.\nWhich side") || !strings.Contains(p, "JSON array of 20 strings") || !strings.Contains(p, "\n20. EU\n") {
		t.Errorf("prompt:\n%s", p)
	}
	// Refused by the gateway: the whole fails, saying why.
	_, err = LLMApply(context.Background(), f.Step("mood"), []string{"order_id", "region"}, rows[:3], refusingAsker{})
	if err == nil || !strings.Contains(err.Error(), "rows 1 to 3: the gateway refused") {
		t.Errorf("refusal: %v", err)
	}
	// What the step refuses.
	for _, bad := range []struct {
		s   Step
		why string
	}{
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "guess", Name: "x"}, "classify, extract, summarize or nearest"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "nope", Mode: "summarize", Name: "x"}, "no column nope"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "classify", Values: []string{"one"}, Name: "x"}, "at least two labels"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "extract", Name: "x"}, "needs a prompt"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "summarize", Name: "region"}, "already has a column region"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "summarize", Name: "x", Limit: 5000}, "at most 1000 rows"},
		{Step{ID: "m", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "nearest", Name: "x"}, "the knowledge bank to search"},
	} {
		g := &Flow{Name: "g", Steps: []Step{{ID: "orders", Kind: Input, Table: "orders"}, bad.s}}
		c, _ := Compile(g, env())
		if errs := c.Errors(); len(errs) == 0 || !strings.Contains(errs[0], bad.why) {
			t.Errorf("%v: wanted %q, got %v", bad.s.Mode, bad.why, errs)
		}
	}
}

// The nearest mode adds the passage and its source.
func TestNearestStep(t *testing.T) {
	f := &Flow{Name: "g", Steps: []Step{{ID: "orders", Kind: Input, Table: "orders"},
		{ID: "near", Kind: LLM, Inputs: []string{"orders"}, Column: "region", Mode: "nearest", Bank: "reviews", Name: "passage"}}}
	c, _ := Compile(f, env())
	if errs := c.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	cols := c.Steps["near"].Columns
	if n := len(cols); n < 2 || cols[n-2].Name != "passage" || cols[n-1].Name != "passage_source" {
		t.Errorf("columns: %+v", cols)
	}
}

type refusingAsker struct{}

func (refusingAsker) Ask(context.Context, string) (string, float64, error) {
	return "", 0, fmt.Errorf("the gateway refused (HTTP 400): the question holds an email address, which may not leave the platform")
}

// Chunks cut at a sentence, else a space, near the size; short text is one chunk.
func TestChunks(t *testing.T) {
	if got := Chunks("  one   short\ttext ", 100); len(got) != 1 || got[0] != "one short text" {
		t.Errorf("short: %q", got)
	}
	long := strings.Repeat("The seam split after a week. ", 100)
	got := Chunks(long, 200)
	for i, c := range got {
		if len(c) > 200 {
			t.Errorf("chunk %d is %d long", i, len(c))
		}
		if i < len(got)-1 && !strings.HasSuffix(c, ".") {
			t.Errorf("chunk %d isn't cut at a sentence: %q", i, c[len(c)-20:])
		}
	}
	if strings.Join(got, " ") != strings.TrimSpace(strings.Join(strings.Fields(long), " ")) {
		t.Errorf("the chunks don't make the text")
	}
	if got := Chunks(strings.Repeat("x", 500), 200); len(got) != 3 {
		t.Errorf("no space to cut at: %d chunks", len(got))
	}
}
