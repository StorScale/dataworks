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
