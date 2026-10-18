package project

import (
	"slices"
	"strings"
	"testing"
)

const repoProject = `
apiVersion: platform.storscale.io/v1alpha1
kind: Project
metadata: {name: sales}
spec:
  description: Orders
  members:
    - {group: analysts, role: reader}
    - {user: alice, role: editor}
  tables: {readers: {masks: []}}
  limits: {queries: 4}
`

func TestComposeLaysTheTargetOver(t *testing.T) {
	p, err := Compose("sales", []byte(repoProject), []byte(`
spec:
  members:
    - {user: bob, role: editor}
  limits: {queued: 10}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Spec.Members) != 1 || p.Spec.Members[0].User != "bob" {
		t.Errorf("members (a list, replaced): %+v", p.Spec.Members)
	}
	if p.Spec.Limits == nil || p.Spec.Limits.Queries != 4 || p.Spec.Limits.Queued != 10 {
		t.Errorf("limits (a map, merged): %+v", p.Spec.Limits)
	}
	if p.Spec.Description != "Orders" {
		t.Errorf("description: %q", p.Spec.Description)
	}
}

func TestComposeRefuses(t *testing.T) {
	for _, c := range []struct{ name, project, target, want string }{
		{"sales", "", "", "empty"},
		{"other", repoProject, "", `names project "sales"`},
		{"sales", repoProject, "spec: {nonsense: 1}", "unknown field"},
		{"sales", "spec: [", "", "project.yaml"},
	} {
		_, err := Compose(c.name, []byte(c.project), []byte(c.target))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q over %q: %v, want %q", c.target, c.project, err, c.want)
		}
	}
}

func TestSpecChanges(t *testing.T) {
	before, _ := Compose("sales", []byte(repoProject), nil)
	after, _ := Compose("sales", []byte(repoProject), []byte("spec: {members: [{group: analysts, role: reader}, {user: carol, role: editor}], description: Sales}"))
	got := SpecChanges(before, after)
	want := []string{"member added: carol as editor", "member removed: alice as editor", "changed: description"}
	if !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
	if got := SpecChanges(before, before); len(got) != 0 {
		t.Errorf("unchanged: %q", got)
	}
}

func TestMetricChanges(t *testing.T) {
	was := "semantic_model: {name: orders}\n---\nmetric: {name: revenue, type: simple}\n---\nmetric: {name: orders, type: simple}\n"
	now := "semantic_model: {name: orders}\n---\nmetric: {name: revenue, type: ratio}\n---\nmetric: {name: margin, type: simple}\n"
	got, err := MetricChanges(was, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"metric added: margin", "metric changed: revenue", "metric removed: orders"}
	if !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
	if _, err := MetricChanges("", "metric: ["); err == nil {
		t.Error("bad YAML: no error")
	}
}

func TestConnections(t *testing.T) {
	for name, want := range map[string]bool{"sales-dags": true, "sales-": false, "dags": false, "hr-dags": false, "sales-Dags": false, "storscale-env": false} {
		if got := ConnectionNameProblem("sales", name) == ""; got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	db := KindOf("database")
	if got := db.ValuesProblems(map[string]string{"username": "u", "password": "p"}); len(got) != 1 || !strings.Contains(got[0], "host, url") {
		t.Errorf("no host: %q", got)
	}
	if got := db.ValuesProblems(map[string]string{"username": "u", "password": "p", "host": "h", "nonsense": "x"}); len(got) != 1 {
		t.Errorf("an unknown key: %q", got)
	}
	p, _ := Compose("sales", []byte(repoProject), []byte("spec: {notify: {teams: sales-teams}}"))
	if got := p.ConnectionUses("sales-teams"); !slices.Equal(got, []string{"alerts (notify.teams)"}) {
		t.Errorf("uses: %q", got)
	}
}

func TestPeoplesTablesArePersonal(t *testing.T) {
	p, err := Compose("sales", []byte(repoProject), []byte("spec: {tables: {people: [{table: customers, column: customer_id}]}}"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		d     Delivery
		table string
	}{
		{Delivery{Table: "customers"}, "customers"},
		{Delivery{Query: "select customer_id, sum(amount) from customers group by 1"}, "customers"},
		{Delivery{Query: "select region, count(*) from customers group by 1"}, ""}, // no one's ID in it
		{Delivery{Table: "orders"}, ""},
	} {
		if tb, _ := p.PersonKeyOf(c.d); tb != c.table {
			t.Errorf("%+v: %q, want %q", c.d, tb, c.table)
		}
		if got := p.PersonalColumns(c.d); (len(got) > 0) != (c.table != "") {
			t.Errorf("%+v: personal %v", c.d, got)
		}
	}
	if _, err := Compose("sales", []byte(repoProject), []byte("spec: {tables: {people: [{table: Customers, column: id}]}}")); err == nil {
		t.Error("a bad table name: no error")
	}
}
