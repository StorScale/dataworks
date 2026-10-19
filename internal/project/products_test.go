package project

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const companyJSON = `{"metadata":{"name":"company"},"spec":{"members":[{"user":"bob","role":"editor"}],
	"tables":{"readers":{"masks":[{"table":"bookings","column":"amount","type":"MASK_NULL"},{"table":"customers","column":"segment","type":"MASK"}],
	"rowFilters":[{"table":"bookings","filter":"amount < 2000"}]}},
	"products":[{"name":"bookings","table":"bookings","owner":"bob","version":"1","contract":{"columns":["booking_id","amount"],"within":"24h"}}]}}`

func TestProducts(t *testing.T) {
	p, err := Parse([]byte(companyJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ old, new, want string }{
		{`"owner":"bob"`, `"owner":"bob bob"`, "its owner is a person"},
		{`"version":"1"`, `"version":""`, "a version"},
		{`"within":"24h"`, `"within":"soon"`, "within"},
		{`"columns":["booking_id","amount"]`, `"columns":["Booking Id"]`, "isn't a column's name"},
		{`"version":"1"`, `"version":"1","consumers":["company"]`, "isn't another project's name"},
	} {
		_, err := Parse([]byte(strings.Replace(companyJSON, c.old, c.new, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.new, err, c.want)
		}
	}

	// Consumers and certification are the platform's: a save keeps them as they were.
	before, _ := Parse([]byte(companyJSON))
	before.Spec.Products[0].Consumers = []string{"campaigns"}
	before.Spec.Products[0].Certification = &Certification{By: "priya", At: time.Now(), Hash: before.Spec.Products[0].PromiseHash()}
	forged, _ := Parse([]byte(strings.Replace(companyJSON, `"version":"1"`, `"version":"1","consumers":["eve"]`, 1)))
	KeepProductState(before, forged)
	if pr := forged.Spec.Products[0]; len(pr.Consumers) != 1 || pr.Consumers[0] != "campaigns" || !pr.Certified() {
		t.Errorf("kept: %+v", pr)
	}
	// A contract change: no longer certified.
	changed, _ := Parse([]byte(strings.Replace(companyJSON, `"booking_id","amount"`, `"booking_id"`, 1)))
	KeepProductState(before, changed)
	if changed.Spec.Products[0].Certified() {
		t.Error("certified after its contract changed")
	}

	// Its consumers read its table, under the producer's mask and row filter
	// on it, and nothing else: in the producer's policies where one covers
	// the same resource (Ranger keeps one policy to a resource).
	p.Spec.Products[0].Consumers = []string{"campaigns"}
	pols := map[string]RangerPolicy{}
	resourceOf := map[string]string{}
	for _, pol := range p.RangerPolicies() {
		pols[pol.Name] = pol
		key := fmt.Sprintf("%d %v", pol.PolicyType, pol.Resources)
		if other, ok := resourceOf[key]; ok {
			t.Errorf("%s and %s cover the same resource: Ranger refuses the second", other, pol.Name)
		}
		resourceOf[key] = pol.Name
	}
	groups := func(items []RangerItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Groups...)
		}
		return out
	}
	consumers := []string{"campaigns-readers", "campaigns-editors", "campaigns-pipelines"}
	has := func(name string, items []RangerItem) {
		got := groups(items)
		for _, g := range consumers {
			if !slices.Contains(got, g) {
				t.Errorf("%s: no %s in %v", name, g, got)
			}
		}
	}
	has("namespace", pols["project:company:namespace"].PolicyItems)
	has("the table", pols["project:company:product-table:bookings"].PolicyItems)
	has("the mask on bookings", pols["project:company:mask:bookings.amount"].DataMaskPolicyItems)
	has("the filter on bookings", pols["project:company:filter:bookings"].RowFilterPolicyItems)
	if got := groups(pols["project:company:mask:customers.segment"].DataMaskPolicyItems); slices.Contains(got, "campaigns-readers") {
		t.Errorf("customers isn't a product: its mask names campaigns: %v", got)
	}
	if f := pols["project:company:filter:bookings"].RowFilterPolicyItems; f[len(f)-1].RowFilterInfo.FilterExpr != "amount < 2000" {
		t.Errorf("the consumers' filter: %+v", f[len(f)-1])
	}
	p.Spec.Products[0].Consumers = nil
	if got := p.ProductPolicies(); len(got) != 0 {
		t.Errorf("no consumers, no policies: %v", got)
	}
}
