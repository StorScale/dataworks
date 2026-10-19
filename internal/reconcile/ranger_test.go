package reconcile

import (
	"testing"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A consumer project's group Ranger doesn't have yet (campaigns has no
// pipelines) is left out, rather than the whole sync refused.
func TestKnownGroups(t *testing.T) {
	have := map[string]int{"company-readers": 1, "company-editors": 2, "campaigns-readers": 3}
	got := knownGroups([]project.RangerPolicy{
		{Name: "namespace", PolicyItems: []project.RangerItem{
			{Groups: []string{"company-readers", "campaigns-readers", "campaigns-pipelines"}},
			{Groups: []string{"company-editors"}},
		}},
		{Name: "mask", PolicyType: 1, DataMaskPolicyItems: []project.RangerItem{
			{Groups: []string{"campaigns-pipelines"}},
		}},
		{Name: "by-user", PolicyItems: []project.RangerItem{{Users: []string{"svc"}}}},
	}, have)
	if len(got) != 2 || got[0].Name != "namespace" || got[1].Name != "by-user" {
		t.Fatalf("policies %+v", got)
	}
	if gs := got[0].PolicyItems[0].Groups; len(gs) != 2 || gs[1] != "campaigns-readers" {
		t.Fatalf("groups %v", gs)
	}
	if len(got[0].PolicyItems) != 2 {
		t.Fatalf("items %+v", got[0].PolicyItems)
	}
}
