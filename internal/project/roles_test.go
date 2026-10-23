package project

import (
	"slices"
	"strings"
	"testing"
)

func TestRoles(t *testing.T) {
	for _, tc := range []struct {
		role Role
		want string
	}{
		{Role{Name: "steward", Permissions: []string{PermReadAll, PermClassify}}, ""},
		{Role{Name: "editor", Permissions: []string{PermRead}}, "the platform's own"},
		{Role{Name: "S", Permissions: []string{PermRead}}, "lowercase"},
		{Role{Name: "steward"}, "at least one permission"},
		{Role{Name: "steward", Permissions: []string{"drop-everything"}}, `no permission "drop-everything"`},
	} {
		err := tc.role.Check()
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%+v: %v, want %q", tc.role, err, tc.want)
		}
	}
	w := Role{Name: "loader", Permissions: []string{PermWriteTables, PermWriteFiles}}
	if !w.Has(PermReadAll) || !w.Has(PermRead) || !w.Has(PermReadFiles) || w.Has(PermApprove) {
		t.Errorf("implied: %v", w.Granted())
	}
	if !Permits(Editor, nil, PermApprove) || Permits(Reader, nil, PermApprove) || !Permits(Reader, nil, PermCallModels) ||
		!Permits("steward", []string{PermClassify}, PermClassify) || Permits("steward", []string{PermClassify}, PermBuild) {
		t.Error("Permits")
	}
}

func TestCustomRolesInPolicies(t *testing.T) {
	p, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"group":"engineers","role":"editor"},
		{"group":"analysts","role":"reader"},{"user":"carol","role":"steward"},{"user":"dan","role":"analyst"}],
		"tables":{"readers":{"rowFilters":[{"table":"orders","filter":"region = 'EU'"}],"masks":[{"table":"orders","column":"card_number","type":"MASK"}]}},
		"files":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	p.Roles = map[string]Role{
		"steward": {Name: "steward", Permissions: []string{PermReadAll, PermReadFiles, PermClassify}},
		"analyst": {Name: "analyst", Permissions: []string{PermRead, PermWriteFiles}},
	}
	if got := p.CustomRoles(); !slices.Equal(got, []string{"analyst", "steward"}) {
		t.Fatalf("custom roles: %v", got)
	}
	groupsOf := func(name string, items func(RangerPolicy) []RangerItem) []string {
		var out []string
		for _, pol := range p.RangerPolicies() {
			if pol.Name == p.RangerPrefix()+name {
				for _, it := range items(pol) {
					out = append(out, it.Groups...)
				}
			}
		}
		return out
	}
	access := func(pol RangerPolicy) []RangerItem { return pol.PolicyItems }
	tables := groupsOf("tables", access)
	for _, g := range []string{"sales-editors", "sales-role-steward", "sales-readers", "sales-role-analyst"} {
		if !slices.Contains(tables, g) {
			t.Errorf("tables policy lacks %s: %v", g, tables)
		}
	}
	masked := groupsOf("mask:orders.card_number", func(pol RangerPolicy) []RangerItem { return pol.DataMaskPolicyItems })
	filtered := groupsOf("filter:orders", func(pol RangerPolicy) []RangerItem { return pol.RowFilterPolicyItems })
	for _, got := range [][]string{masked, filtered} {
		if !slices.Equal(got, []string{"sales-readers", "sales-role-analyst"}) {
			t.Errorf("the readers' rules hold readers and analysts, not the steward nor editors: %v", got)
		}
	}
	b := p.BucketsPolicies()
	if len(b["sales-role-steward"]) != len(b["sales-readers"]) || len(b["sales-role-analyst"]) != len(b["sales-editors"]) {
		t.Errorf("buckets: steward reads, analyst writes: %v", b)
	}
	if !slices.Contains(p.Groups(), "sales-role-steward") || p.Group("steward") != "sales-role-steward" || p.Group("pipelines") != "sales-pipelines" {
		t.Errorf("groups: %v", p.Groups())
	}
	before := p.Hash()
	p.Roles["steward"] = Role{Name: "steward", Permissions: []string{PermRead}}
	if p.Hash() == before {
		t.Error("a role changed is a project to apply again")
	}
}
