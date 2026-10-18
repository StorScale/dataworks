package main

import (
	"strings"
	"testing"

	"github.com/StorScale/storscale-platform/internal/project"
)

func TestADeliveryNeedsAnotherToApproveWhatItSends(t *testing.T) {
	parse := func(d string) *project.Project {
		p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
			"spec":{"members":[{"group":"engineers","role":"editor"}],
			"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK"}]}},"deliveries":[` + d + `]}}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	s := &server{cfg: config{DeliveryApproverGroups: []string{"privacy"}}}
	regions := `{"name":"acme","query":"select region, count(*) n from orders group by region","format":"csv","files":"a_{date}.csv","schedule":"0 6 * * 1","target":"acme"}`

	// bob makes it: he's who changed it, and it needs another editor.
	p := parse(regions)
	keepDeliveryApprovals(nil, p, "bob")
	d := p.Spec.Deliveries[0]
	if d.ChangedBy != "bob" || len(s.needs(p, d)) != 1 || !strings.Contains(s.needs(p, d)[0], "other than bob") {
		t.Fatalf("new: changed by %q, needs %v", d.ChangedBy, s.needs(p, d))
	}
	// alice approves: done.
	p.Spec.Deliveries[0].Approval = &project.Approval{Hash: d.SendsHash(), By: []string{"alice"}, Done: true}
	if n := s.needs(p, p.Spec.Deliveries[0]); len(n) != 0 || !p.Spec.Deliveries[0].Approved() {
		t.Fatalf("approved: needs %v", n)
	}
	// Saved again as it was, by carlos: still alice's approval, still bob's change.
	again := parse(regions)
	keepDeliveryApprovals(p, again, "carlos")
	if a := again.Spec.Deliveries[0]; a.ChangedBy != "bob" || !a.Approved() {
		t.Errorf("saved unchanged: %+v", a)
	}
	// A client can't approve it by sending an approval: what's kept is the platform's.
	forged := parse(strings.Replace(regions, `"target":"acme"`, `"target":"acme","approval":{"hash":"x","by":["eve"],"done":true}`, 1))
	keepDeliveryApprovals(nil, forged, "eve")
	if forged.Spec.Deliveries[0].Approval != nil {
		t.Errorf("a forged approval kept: %+v", forged.Spec.Deliveries[0].Approval)
	}
	// Its query changed to send card numbers: approving again, by an editor and a data-protection approver.
	personal := parse(strings.Replace(regions, "select region, count(*) n", "select region, card_number", 1))
	keepDeliveryApprovals(p, personal, "carlos")
	pd := personal.Spec.Deliveries[0]
	if pd.ChangedBy != "carlos" || pd.Approved() || len(s.needs(personal, pd)) != 2 {
		t.Fatalf("changed: %+v, needs %v", pd, s.needs(personal, pd))
	}
	pd.Approval = &project.Approval{Hash: pd.SendsHash(), By: []string{"bob"}, Protection: []string{"carlos"}}
	if n := s.needs(personal, pd); len(n) != 1 || !strings.Contains(n[0], "privacy") {
		t.Errorf("carlos changed it, so his data-protection approval doesn't count: %v", n)
	}
	pd.Approval.Protection = append(pd.Approval.Protection, "priya")
	if n := s.needs(personal, pd); len(n) != 0 {
		t.Errorf("bob and priya: %v", n)
	}
}
