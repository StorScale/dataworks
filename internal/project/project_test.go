package project

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const sales = `{
  "apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project",
  "metadata": {"name": "sales"},
  "spec": {
    "members": [{"group": "engineers", "role": "editor"}, {"group": "analysts", "role": "reader"}],
    "tables": {"readers": {"tables": ["orders"],
                           "rowFilters": [{"table": "orders", "filter": "region = 'EU'"}],
                           "masks": [{"table": "orders", "column": "card_number", "type": "MASK_SHOW_LAST_4"}]}},
    "files": {},
    "pipelines": {"serviceAccount": "airflow-pipelines"}
  }
}`

func TestParseFillsInDefaults(t *testing.T) {
	p, err := Parse([]byte(sales))
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.Tables.Catalog != "iceberg" || p.Spec.Tables.Namespace != "sales" || p.Spec.Files.Bucket != "sales" {
		t.Errorf("defaults: %+v %+v", p.Spec.Tables, p.Spec.Files)
	}
	if g := p.Groups(); strings.Join(g, ",") != "sales-readers,sales-editors,sales-pipelines" {
		t.Errorf("groups: %v", g)
	}
}

func TestADashedNameMakesAnIdentifierNamespace(t *testing.T) {
	p, err := Parse([]byte(strings.Replace(sales, `"name": "sales"`, `"name": "sales-eu"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.Tables.Namespace != "sales_eu" || p.Spec.Files.Bucket != "sales-eu" {
		t.Errorf("namespace %q, bucket %q", p.Spec.Tables.Namespace, p.Spec.Files.Bucket)
	}
}

func TestValidateSaysEverythingThatsWrong(t *testing.T) {
	_, err := Parse([]byte(`{"metadata": {"name": "X"}, "spec": {
	  "members": [{"group": "a", "user": "b", "role": "Owner"}],
	  "tables": {"namespace": "Bad Name", "readers": {"masks": [{"table": "t", "column": "c", "type": "SCRAMBLE"}]}}}}`))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`name "X"`, "not both", `role "Owner"`, `namespace "Bad Name"`, `mask type "SCRAMBLE"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't mention %s", err, want)
		}
	}
	if _, err := Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [], "surprise": 1}}`)); err == nil ||
		!strings.Contains(err.Error(), "surprise") {
		t.Errorf("an unknown field: %v", err)
	}
}

func TestChecks(t *testing.T) {
	ok := strings.Replace(sales, `"files": {},`, `"files": {},`, 1)
	ok = strings.Replace(ok, `"tables": {"readers"`, `"tables": {"checks": [{"table": "orders", "column": "id", "check": "unique"},
	  {"table": "orders", "check": "rowCount", "min": 1}], "readers"`, 1)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(sales, `"tables": {"readers"`, `"tables": {"checks": [{"table": "orders", "check": "unique"},
	  {"table": "orders", "check": "rowCount"}, {"table": "orders", "column": "id", "check": "fresh"}], "readers"`, 1)
	_, err := Parse([]byte(bad))
	for _, want := range []string{"unique needs a column", "rowCount needs a min", `"fresh" must be one of`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v doesn't mention %s", err, want)
		}
	}
}

func TestBucketsPolicies(t *testing.T) {
	p, _ := Parse([]byte(sales))
	pol := p.BucketsPolicies()
	if len(pol) != 3 {
		t.Fatalf("policies: %v", pol)
	}
	if got := pol["sales-readers"][1].Action; len(got) != 1 || got[0] != "s3:GetObject" {
		t.Errorf("readers only read: %v", got)
	}
	if got := pol["sales-pipelines"][2].Resource[0]; got != "arn:aws:s3:::sales/landing/*" {
		t.Errorf("pipelines write landing/ only: %v", got)
	}
}

func TestRangerPolicies(t *testing.T) {
	p, _ := Parse([]byte(sales))
	byName := map[string]RangerPolicy{}
	for _, rp := range p.RangerPolicies() {
		if !strings.HasPrefix(rp.Name, "project:sales:") {
			t.Errorf("policy %q doesn't carry the project's prefix", rp.Name)
		}
		key := ""
		for _, k := range []string{"catalog", "schema", "table", "column"} {
			if r, ok := rp.Resources[k]; ok {
				key += k + "=" + r.Values[0] + " "
			}
		}
		key += string(rune('0' + rp.PolicyType))
		for _, other := range byName {
			if other.Name == rp.Name {
				t.Errorf("two policies called %q", rp.Name)
			}
		}
		byName[key] = rp
	}
	if len(byName) != 5 {
		t.Fatalf("want namespace, tables, read:orders, a mask and a row filter, one per resource: %v", byName)
	}
	tables := byName["catalog=iceberg schema=sales table=* column=* 0"]
	for _, it := range tables.PolicyItems {
		if it.Groups[0] == "sales-readers" {
			t.Error("readers limited to orders must not get every table")
		}
	}
	// Editors first, as they are; then the readers, ruled.
	if f := byName["catalog=iceberg schema=sales table=orders 2"]; len(f.RowFilterPolicyItems) != 2 || f.RowFilterPolicyItems[0].Groups[0] != "sales-editors" ||
		f.RowFilterPolicyItems[0].RowFilterInfo.FilterExpr != "" || f.RowFilterPolicyItems[1].RowFilterInfo.FilterExpr != "region = 'EU'" {
		t.Errorf("row filter: %+v", f)
	}
	if m := byName["catalog=iceberg schema=sales table=orders column=card_number 1"]; len(m.DataMaskPolicyItems) != 2 ||
		m.DataMaskPolicyItems[0].DataMaskInfo.DataMaskType != "MASK_NONE" || m.DataMaskPolicyItems[1].DataMaskInfo.DataMaskType != "MASK_SHOW_LAST_4" {
		t.Errorf("mask: %+v", m)
	}
	// The table as it was (orders@a-tag, orders#a-commit) is the table: its
	// readers' access, mask and row filter reach it too.
	for _, key := range []string{"catalog=iceberg schema=sales table=orders column=* 0", "catalog=iceberg schema=sales table=orders 2",
		"catalog=iceberg schema=sales table=orders column=card_number 1"} {
		if got := strings.Join(byName[key].Resources["table"].Values, " "); got != "orders orders@* orders#*" {
			t.Errorf("%s: tables %q, want orders at every reference too", key, got)
		}
	}
}

func TestHashFollowsTheSpec(t *testing.T) {
	a, _ := Parse([]byte(sales))
	b, _ := Parse([]byte(strings.Replace(sales, "EU", "US", 1)))
	if a.Hash() == b.Hash() {
		t.Error("a changed spec kept its hash")
	}
	c, _ := Parse([]byte(sales))
	if a.Hash() != c.Hash() {
		t.Error("the same spec changed its hash")
	}
}

func withPipelines(t *testing.T, pipelines string) error {
	t.Helper()
	_, err := Parse([]byte(strings.Replace(sales, `"pipelines": {"serviceAccount": "airflow-pipelines"}`, `"pipelines": `+pipelines, 1)))
	return err
}

func TestAPipelinesRepo(t *testing.T) {
	for _, ok := range []string{
		`{"serviceAccount": "airflow-pipelines"}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git"}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "path": "etl/dags", "ref": "release/1.2",
		  "credentials": {"secretRef": "sales-git"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "git@git.example.com:sales/dags.git", "credentials": {"secretRef": "sales-git"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "ssh://git@git.example.com/sales/dags.git", "ref": "3f2a9c1",
		  "credentials": {"secretRef": "sales-git"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "memory": "2048Mi"}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "memory": "4Gi"}`,
	} {
		if err := withPipelines(t, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, tc := range []struct{ pipelines, want string }{
		{`{"serviceAccount": "airflow-pipelines", "path": "dags"}`, "need a repo"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://bob:ghp_token@git.example.com/sales/dags.git"}`, "no user or token"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://ghp_token@git.example.com/sales/dags.git"}`, "no user or token"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "ssh://git:secret@git.example.com/sales/dags.git", "credentials": {"secretRef": "s"}}`, "no password"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git?token=x"}`, "no query"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "file:///etc"}`, "https:// or ssh"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "git@git.example.com:sales/dags.git"}`, "an ssh repo needs credentials"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "path": "../other"}`, "a folder in the repo"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "path": "/abs"}`, "a folder in the repo"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "ref": "main..x"}`, "a branch, tag or commit"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "ref": "-delete"}`, "a branch, tag or commit"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "credentials": {"secretRef": "Sales_Git"}}`, "isn't a Secret's name"},
		{`{"serviceAccount": "airflow-pipelines", "memory": "2Gi"}`, "need a repo"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "memory": "1536Mi"}`, "from 2Gi to 64Gi"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "memory": "65Gi"}`, "from 2Gi to 64Gi"},
		{`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales/dags.git", "memory": "2G"}`, "from 2Gi to 64Gi"},
	} {
		if err := withPipelines(t, tc.pipelines); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.pipelines, err, tc.want)
		}
	}
}

func TestTheBundleAndConnectionOfAProject(t *testing.T) {
	p, _ := Parse([]byte(strings.Replace(sales, `"name": "sales"`, `"name": "sales-eu"`, 1)))
	if p.BundleName() != "project-sales-eu" || p.GitConnID() != "git_sales_eu" {
		t.Errorf("bundle %q, connection %q", p.BundleName(), p.GitConnID())
	}
	if p.AirflowName() != "project-sales-eu-airflow" || p.AirflowDatabase() != "airflow_sales_eu" || p.PipelinesClientID() != "project-sales-eu-pipelines" {
		t.Errorf("its Airflow %q, database %q, client %q", p.AirflowName(), p.AirflowDatabase(), p.PipelinesClientID())
	}
	if mi, ok := MiB("2Gi"); !ok || mi != 2048 {
		t.Errorf("2Gi: %d MiB", mi)
	}
}

func TestAProjectsLimits(t *testing.T) {
	with := func(limits string) error {
		_, err := Parse([]byte(strings.Replace(sales, `"pipelines": {"serviceAccount": "airflow-pipelines"}`,
			`"pipelines": {"serviceAccount": "airflow-pipelines"}, "limits": `+limits, 1)))
		return err
	}
	for _, ok := range []string{`{}`, `{"queries": 4, "queued": 40, "queryMemory": "50%", "storage": "500Gi"}`,
		`{"queryMemory": "4GB"}`, `{"queryMemory": "512MB", "storage": "2Ti"}`} {
		if err := with(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, tc := range []struct{ limits, want string }{
		{`{"queries": 101}`, "queries 101"},
		{`{"queued": -1}`, "queued -1"},
		{`{"queryMemory": "4G"}`, "queryMemory"},
		{`{"queryMemory": "150%"}`, "queryMemory"},
		{`{"storage": "500GB"}`, "storage"},
	} {
		if err := with(tc.limits); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.limits, err, tc.want)
		}
	}
	if StorageBytes("2Ti") != 2<<40 || StorageBytes("500Gi") != 500<<30 || StorageBytes("") != 0 {
		t.Errorf("storage in bytes: %d %d", StorageBytes("2Ti"), StorageBytes("500Gi"))
	}
}

func TestFreshnessAndWhereAlertsGo(t *testing.T) {
	with := func(extra string) error {
		_, err := Parse([]byte(strings.Replace(sales, `"pipelines": {"serviceAccount": "airflow-pipelines"}`,
			`"pipelines": {"serviceAccount": "airflow-pipelines"}`+extra, 1)))
		return err
	}
	if err := with(`, "notify": {"teams": "sales-alerts", "email": ["data@example.com"]}`); err != nil {
		t.Errorf("notify: %v", err)
	}
	for _, tc := range []struct{ extra, want string }{
		{`, "notify": {"teams": "Sales Alerts"}`, "isn't a connection's name"},
		{`, "notify": {"slack": "Sales Alerts"}`, "slack \"Sales Alerts\" isn't a connection's name"},
		{`, "notify": {"email": ["not-an-address"]}`, "isn't an email address"},
	} {
		if err := with(tc.extra); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.extra, err, tc.want)
		}
	}
	fresh := strings.Replace(sales, `"tables": {"readers"`, `"tables": {"freshness": [{"table": "orders", "within": "24h"}], "readers"`, 1)
	if _, err := Parse([]byte(fresh)); err != nil {
		t.Errorf("freshness: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(fresh, `"24h"`, `"1 day"`, 1))); err == nil || !strings.Contains(err.Error(), "as 30m, 24h or 7d") {
		t.Errorf("a freshness target that isn't one: %v", err)
	}
	if d, ok := Within("7d"); !ok || d != 7*24*time.Hour {
		t.Errorf("7d: %v", d)
	}
}

func TestMaintenanceSettings(t *testing.T) {
	p, err := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"maintenance":{"paused":true}}}}`))
	if err != nil || !p.Spec.Tables.Maintenance.Paused {
		t.Fatalf("maintenance: %+v, %v", p.Spec.Tables, err)
	}
}

func TestTags(t *testing.T) {
	spec := func(tags string) error {
		_, err := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"tags":` + tags + `}}}`))
		return err
	}
	if err := spec(`[{"name":"close-2026-09","commit":"0123456789abcdef0123","keepUntil":"2033-10-03","note":"September","by":"bob"}]`); err != nil {
		t.Errorf("a tag: %v", err)
	}
	for _, bad := range []string{
		`[{"name":"Close","commit":"0123456789abcdef","keepUntil":"2033-10-03"}]`,
		`[{"name":"close","commit":"main","keepUntil":"2033-10-03"}]`,
		`[{"name":"close","commit":"0123456789abcdef","keepUntil":"7y"}]`,
		`[{"name":"close","commit":"0123456789abcdef","keepUntil":"2033-10-03"},{"name":"close","commit":"0123456789abcdef","keepUntil":"2033-10-03"}]`,
	} {
		if spec(bad) == nil {
			t.Errorf("%s: want refused", bad)
		}
	}
	p, _ := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},"spec":{"members":[{"group":"engineers","role":"editor"}]}}`))
	if got := p.NessieTag(Tag{Name: "close-2026-09"}); got != "sales.close-2026-09" {
		t.Errorf("Nessie's name: %s", got)
	}
}

func TestSources(t *testing.T) {
	spec := func(name, sources string) (*Project, error) {
		return Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"` + name + `"},
			"spec":{"members":[{"group":"engineers","role":"editor"}],"sources":` + sources + `}}`))
	}
	p, err := spec("sales-eu", `[{"name":"finance","kind":"postgresql","connection":"finance-db"},{"name":"crm","kind":"mysql","connection":"crm","readers":true}]`)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if got := p.SourceCatalog(p.Spec.Sources[0]); got != "sales_eu_finance" {
		t.Errorf("catalog: %s", got)
	}
	for _, bad := range []string{
		`[{"name":"Finance","kind":"postgresql","connection":"finance-db"}]`,
		`[{"name":"finance","kind":"db2","connection":"finance-db"}]`,
		`[{"name":"finance","kind":"postgresql","connection":"Finance DB"}]`,
		`[{"name":"finance","kind":"postgresql","connection":"a"},{"name":"finance","kind":"mysql","connection":"b"}]`,
	} {
		if _, err := spec("sales", bad); err == nil {
			t.Errorf("%s: want refused", bad)
		}
	}
	// Read-only, the project's: editors and readers where it says so; no
	// tables of its own, and still the sources' policies.
	byName := map[string]RangerPolicy{}
	for _, rp := range p.RangerPolicies() {
		byName[rp.Name] = rp
		for _, it := range rp.PolicyItems {
			for _, a := range it.Accesses {
				if !slices.Contains([]string{"use", "show", "select"}, a.Type) {
					t.Errorf("%s: %s may %s", rp.Name, it.Groups, a.Type)
				}
			}
		}
	}
	if len(byName) != 8 {
		t.Fatalf("want a catalog, schemas, tables and metadata policy per source: %v", byName)
	}
	groups := func(name string) (g []string) {
		for _, it := range byName[name].PolicyItems {
			g = append(g, it.Groups...)
		}
		return g
	}
	if g := groups("project:sales-eu:source:finance:tables"); slices.Contains(g, "sales-eu-readers") || !slices.Contains(g, "sales-eu-editors") {
		t.Errorf("finance's readers: %v", g)
	}
	if g := groups("project:sales-eu:source:crm:tables"); !slices.Contains(g, "sales-eu-readers") {
		t.Errorf("crm's readers: %v", g)
	}
	if r := byName["project:sales-eu:source:crm"].Resources; r["catalog"].Values[0] != "sales_eu_crm" || len(r) != 1 {
		t.Errorf("crm's catalog policy: %v", r)
	}
}

func TestASourcesSchemasMasksAndFilters(t *testing.T) {
	parse := func(src string) (*Project, error) {
		return Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}],"sources":[` + src + `]}}`))
	}
	p, err := parse(`{"name":"finance","kind":"postgresql","connection":"fin","readers":true,"schemas":["ledger","budget"],
		"masks":[{"table":"ledger.accounts","column":"iban","type":"MASK_SHOW_LAST_4"}],"rowFilters":[{"table":"budget.lines","filter":"region = 'EU'"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RangerPolicy{}
	for _, rp := range p.RangerPolicies() {
		byName[rp.Name] = rp
	}
	if got := byName["project:sales:source:finance:tables"].Resources["schema"].Values; !slices.Equal(got, []string{"budget", "ledger", "information_schema"}) {
		t.Errorf("its schemas: %v", got)
	}
	m := byName["project:sales:source:finance:mask:ledger.accounts.iban"]
	if m.PolicyType != 1 || m.Resources["schema"].Values[0] != "ledger" || m.Resources["table"].Values[0] != "accounts" ||
		m.DataMaskPolicyItems[0].Groups[0] != "sales-readers" {
		t.Errorf("mask: %+v", m)
	}
	if f := byName["project:sales:source:finance:filter:budget.lines"]; f.PolicyType != 2 || f.RowFilterPolicyItems[0].RowFilterInfo.FilterExpr != "region = 'EU'" {
		t.Errorf("filter: %+v", f)
	}
	// The catalog sees what's there, and reads nothing.
	for _, it := range byName["project:sales:source:finance:tables"].PolicyItems {
		if slices.Contains(it.Users, CatalogUser) && (len(it.Accesses) != 1 || it.Accesses[0].Type != "show") {
			t.Errorf("the catalog's access: %+v", it.Accesses)
		}
	}
	md := byName["project:sales:source:finance:metadata"]
	if md.Resources["schema"].Values[0] != "information_schema" || len(md.PolicyItems) != 1 || md.PolicyItems[0].Users[0] != CatalogUser {
		t.Errorf("the catalog's metadata: %+v", md)
	}
	for _, bad := range []string{
		`{"name":"finance","kind":"postgresql","connection":"fin","masks":[{"table":"ledger.accounts","column":"iban","type":"MASK"}]}`,
		`{"name":"finance","kind":"postgresql","connection":"fin","readers":true,"masks":[{"table":"accounts","column":"iban","type":"MASK"}]}`,
		`{"name":"finance","kind":"postgresql","connection":"fin","schemas":["Ledger"]}`,
	} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%s: want refused", bad)
		}
	}
}

func TestASourcesCopy(t *testing.T) {
	parse := func(tables, src string) error {
		_, err := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}]` + tables + `,"sources":[` + src + `]}}`))
		return err
	}
	if err := parse(`,"tables":{}`, `{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":["public.invoices"]}}`); err != nil {
		t.Errorf("a copy: %v", err)
	}
	for _, c := range [][2]string{
		{``, `{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":["public.invoices"]}}`},
		{`,"tables":{}`, `{"name":"finance","kind":"oracle","connection":"fin","copy":{"tables":["public.invoices"]}}`},
		{`,"tables":{}`, `{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":[]}}`},
		{`,"tables":{}`, `{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":["invoices"]}}`},
		{`,"tables":{}`, `{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":["a.invoices","b.invoices"]}}`},
	} {
		if parse(c[0], c[1]) == nil {
			t.Errorf("%s %s: want refused", c[0], c[1])
		}
	}
	if got := CopyTable(Source{Name: "finance"}, "public.invoices"); got != "finance_invoices" {
		t.Errorf("CopyTable: %s", got)
	}
}

func TestImports(t *testing.T) {
	parse := func(tables, imp string) error {
		_, err := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}]` + tables + `,"imports":[` + imp + `]}}`))
		return err
	}
	ok := []string{
		`{"name":"crm","source":"rest_api","connection":"crm-token","schedule":"*/15 * * * *","api":{"baseUrl":"https://api.example.com/v1",
		  "resources":[{"name":"leads","path":"leads","primaryKey":"id","cursor":"updated_at","cursorParam":"since","cursorStart":"2026-01-01T00:00:00Z"}]}}`,
		`{"name":"erp","source":"sql_database","connection":"erp-db","database":{"kind":"postgresql","tables":[{"table":"public.orders","cursor":"updated_at","primaryKey":"id"}]}}`,
	}
	for _, imp := range ok {
		if err := parse(`,"tables":{}`, imp); err != nil {
			t.Errorf("%s: %v", imp, err)
		}
	}
	for _, c := range [][2]string{
		{``, ok[0]}, // no tables of its own
		{`,"tables":{}`, `{"name":"crm","source":"airbyte"}`},
		{`,"tables":{}`, `{"name":"crm","source":"rest_api","api":{"baseUrl":"ftp://x","resources":[{"name":"leads","path":"leads"}]}}`},
		{`,"tables":{}`, `{"name":"crm","source":"rest_api","api":{"baseUrl":"https://x","resources":[{"name":"leads","path":"leads","cursor":"updated_at"}]}}`},
		{`,"tables":{}`, `{"name":"crm","source":"rest_api","schedule":"hourly","api":{"baseUrl":"https://x","resources":[{"name":"leads","path":"leads"}]}}`},
		{`,"tables":{}`, `{"name":"erp","source":"sql_database","database":{"kind":"postgresql","tables":[{"table":"public.orders"}]}}`},
		{`,"tables":{}`, `{"name":"erp","source":"sql_database","connection":"erp","database":{"kind":"postgresql","tables":[{"table":"a.orders"},{"table":"b.orders"}]}}`},
	} {
		if parse(c[0], c[1]) == nil {
			t.Errorf("%s %s: want refused", c[0], c[1])
		}
	}
	p, _ := Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales-eu"},"spec":{"members":[{"group":"engineers","role":"editor"}]}}`))
	if got := p.ImportConnID(Import{Name: "crm"}); got != "import_sales_eu_crm" {
		t.Errorf("ImportConnID: %s", got)
	}
	if got := ImportTable(Import{Name: "erp"}, "public.orders"); got != "erp_orders" {
		t.Errorf("ImportTable: %s", got)
	}
}

func TestReviews(t *testing.T) {
	ok := `{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"},
		"reviews": {"gitlab": "https://gitlab.example.com", "dbt": "dbt"}}`
	if err := withPipelines(t, ok); err != nil {
		t.Errorf("reviews: %v", err)
	}
	// The repo's path as the project, with or without .git, and the repo's address as GitLab's: fine.
	if err := withPipelines(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"},
		"reviews": {"gitlab": "https://gitlab.example.com/sales/dags.git", "dbt": "dbt", "project": "sales/dags.git"}}`); err != nil {
		t.Errorf("reviews, the repo's own words: %v", err)
	}
	for _, bad := range []string{
		`{"serviceAccount": "airflow-pipelines", "reviews": {"gitlab": "https://gitlab.example.com", "dbt": "dbt"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "reviews": {"gitlab": "https://gitlab.example.com", "dbt": "dbt"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}, "reviews": {"gitlab": "gitlab.example.com", "dbt": "dbt"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}, "reviews": {"gitlab": "https://gitlab.example.com", "dbt": "dbt", "project": "data/sales-pipelines"}}`,
		`{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "credentials": {"secretRef": "sales-git"}, "reviews": {"gitlab": "https://gitlab.example.com", "dbt": "../dbt"}}`,
	} {
		if withPipelines(t, bad) == nil {
			t.Errorf("%s: want refused", bad)
		}
	}
	p := &Project{Metadata: Metadata{Name: "sales-eu"}}
	if p.ReviewBranch(42) != "sales-eu-mr-42" || p.ReviewCatalog(42) != "sales_eu_mr_42" {
		t.Errorf("branch %s, catalog %s", p.ReviewBranch(42), p.ReviewCatalog(42))
	}
}

func TestDeliveries(t *testing.T) {
	parse := func(d string) (*Project, error) {
		return Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}],
			"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK"}]}},"deliveries":[` + d + `]}}`))
	}
	p, err := parse(`{"name":"acme","table":"orders","format":"csv","gzip":true,"files":"acme_{date}_{part}.csv.gz","splitBy":"region",
		"schedule":"0 6 * * 1","target":"acme-sftp","pgp":"acme-pgp","checks":{"minRows":1,"maxRows":1000,"within":"24h","parts":["EU","US"]}}`)
	if err != nil {
		t.Fatalf("a delivery: %v", err)
	}
	d := p.Spec.Deliveries[0]
	if d.Approved() {
		t.Error("approved with no approval")
	}
	d.Approval = &Approval{Hash: d.SendsHash(), By: []string{"alice"}, Done: true}
	if !d.Approved() {
		t.Error("an approval of what it sends")
	}
	changed := d
	changed.Target = "other-sftp"
	if changed.Approved() {
		t.Error("a new target needs approving again")
	}
	renamed := d
	renamed.Files, renamed.Schedule = "acme2_{date}_{part}.csv.gz", "0 7 * * 1"
	if !renamed.Approved() {
		t.Error("its names and schedule aren't what's approved")
	}
	if got := p.PersonalColumns(d); len(got) != 1 || got[0] != "orders.card_number" {
		t.Errorf("personal columns: %v", got)
	}
	if got := p.PersonalColumns(Delivery{Query: "select region, count(*) from orders group by region"}); len(got) != 0 {
		t.Errorf("a query without them: %v", got)
	}
	for _, bad := range []string{
		`{"name":"acme","format":"csv","files":"a_{date}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","query":"select 1","format":"csv","files":"a_{date}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","format":"xlsx","files":"a_{date}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","format":"csv","files":"a.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","format":"csv","files":"x/a_{date}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","format":"csv","files":"a_{date}_{part}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","query":"delete from orders","format":"csv","files":"a_{date}.csv","schedule":"0 6 * * 1","target":"t"}`,
		`{"name":"acme","table":"orders","format":"csv","files":"a_{date}.csv","schedule":"weekly","target":"t"}`,
	} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%s: want refused", bad)
		}
	}
}

func TestFilesImport(t *testing.T) {
	for _, tc := range []struct{ im, want string }{
		{`{"name":"uploads","source":"files","files":{"tables":[{"name":"suppliers","file":"suppliers.csv"},{"name":"prices","file":"prices-*.xlsx","primaryKey":"sku"}]}}`, ""},
		{`{"name":"uploads","source":"files"}`, "names its tables"},
		{`{"name":"uploads","source":"files","connection":"x","files":{"tables":[{"name":"a","file":"a.csv"}]}}`, "needs no connection"},
		{`{"name":"uploads","source":"files","files":{"tables":[{"name":"a","file":"../a.csv"}]}}`, "a file of landing/uploads/"},
		{`{"name":"uploads","source":"files","files":{"tables":[{"name":"a","file":"a.txt"}]}}`, "ending in one of csv, xlsx, jsonl, parquet"},
		{`{"name":"uploads","source":"files","files":{"tables":[{"name":"a","file":"a.csv"},{"name":"a","file":"b.csv"}]}}`, "two resources would both be uploads_a"},
	} {
		_, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},"imports":[` + tc.im + `]}}`))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.im, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.im, err, tc.want)
		}
	}
	if FileFormat("Prices.XLS") != "xlsx" || FileFormat("a.parquet") != "parquet" || FileFormat("a") != "" {
		t.Error("formats")
	}
}

func TestKeepTags(t *testing.T) {
	before := &Project{Spec: Spec{Tables: &Tables{Tags: []Tag{{Name: "close-2026-09", Commit: "abc", KeepUntil: "2033-10-03", By: "bob"}}}}}
	release := &Project{}
	KeepTags(before, release)
	if release.Spec.Tables == nil || len(release.Spec.Tables.Tags) != 1 || release.Spec.Tables.Tags[0].By != "bob" {
		t.Errorf("a release without the tag: %+v", release.Spec.Tables)
	}
	both := &Project{Spec: Spec{Tables: &Tables{Namespace: "s", Tags: []Tag{{Name: "close-2026-09", Commit: "zzz"}, {Name: "q3", Commit: "def"}}}}}
	KeepTags(before, both)
	if tags := both.Spec.Tables.Tags; len(tags) != 2 || tags[0].Commit != "abc" || tags[1].Name != "q3" || both.Spec.Tables.Namespace != "s" {
		t.Errorf("a release naming one too: %+v", both.Spec.Tables)
	}
}

func TestAdoptedTablesAreReadOnly(t *testing.T) {
	p, _ := Parse([]byte(sales))
	p.Spec.Tables.Adopt = []Adoption{{Table: "orders", From: "lakehouse.sales.orders"}, {Table: "legacy", From: "lakehouse.sales.legacy"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	byName := map[string]RangerPolicy{}
	resources := map[string]bool{}
	for _, rp := range p.RangerPolicies() {
		byName[strings.TrimPrefix(rp.Name, "project:sales:")] = rp
		if rp.PolicyType == 0 && rp.Resources["table"].Values != nil {
			key := strings.Join(rp.Resources["table"].Values, ",") + fmt.Sprint(rp.Resources["table"].IsExcludes) + strings.Join(rp.Resources["column"].Values, ",")
			if resources[key] {
				t.Errorf("two access policies on %s: Ranger keeps one to a resource", key)
			}
			resources[key] = true
		}
	}
	all := byName["tables"].Resources["table"]
	if !all.IsExcludes || !slices.Contains(all.Values, "orders") || !slices.Contains(all.Values, "legacy@*") {
		t.Errorf("the writers' policy should leave the adopted tables out: %+v", all)
	}
	if _, ok := byName["read:orders"]; ok {
		t.Error("orders' readers belong in its adopted policy (one policy to a resource)")
	}
	for _, name := range []string{"adopted:orders", "adopted:legacy"} {
		rp, ok := byName[name]
		if !ok {
			t.Fatalf("no %s policy: %v", name, byName)
		}
		for _, it := range rp.PolicyItems {
			for _, a := range it.Accesses {
				if a.Type != "select" && a.Type != "show" {
					t.Errorf("%s: %s may %s: an adopted table is read-only here", name, it.Groups, a.Type)
				}
			}
		}
	}
	readersOf := func(name string) bool {
		for _, it := range byName[name].PolicyItems {
			if it.Groups[0] == "sales-readers" {
				return true
			}
		}
		return false
	}
	if !readersOf("adopted:orders") || readersOf("adopted:legacy") {
		t.Error("the readers read orders (their table) and not legacy")
	}
}
