package project

import (
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
	  "members": [{"group": "a", "user": "b", "role": "owner"}],
	  "tables": {"namespace": "Bad Name", "readers": {"masks": [{"table": "t", "column": "c", "type": "SCRAMBLE"}]}}}}`))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`name "X"`, "not both", `role "owner"`, `namespace "Bad Name"`, `mask type "SCRAMBLE"`} {
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
	if f := byName["catalog=iceberg schema=sales table=orders 2"]; f.RowFilterPolicyItems[0].RowFilterInfo.FilterExpr != "region = 'EU'" {
		t.Errorf("row filter: %+v", f)
	}
	if m := byName["catalog=iceberg schema=sales table=orders column=card_number 1"]; m.DataMaskPolicyItems[0].DataMaskInfo.DataMaskType != "MASK_SHOW_LAST_4" {
		t.Errorf("mask: %+v", m)
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
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"maintenance":{"keepSnapshots":"14d","compact":false}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Spec.Tables.Maintenance; m.KeepSnapshots != "14d" || m.Compacts() {
		t.Errorf("maintenance: %+v (compacts %v)", m, m.Compacts())
	}
	if !(*Maintenance)(nil).Compacts() || !(&Maintenance{}).Compacts() {
		t.Error("left out, maintenance compacts")
	}
	_, err = Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"maintenance":{"keepSnapshots":"a week"}}}}`))
	if err == nil || !strings.Contains(err.Error(), "keepSnapshots") {
		t.Errorf("keepSnapshots \"a week\": %v, want refused", err)
	}
}
