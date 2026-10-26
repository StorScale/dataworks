package main

import (
	"testing"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
)

// A reader under the readers' rules doesn't see what the profiler found on
// rows or columns the rules hide.
func TestReadersSeeNoHiddenProfiles(t *testing.T) {
	n, d := 7.0, 3.0
	prof := func() *catalog.ColumnProfile { return &catalog.ColumnProfile{Distinct: &d} }
	flow := &catalog.Flow{Tables: []catalog.Table{
		{Name: "orders", Rows: &n, ProfiledAt: 1, Columns: []catalog.Column{{Name: "id", Profile: prof()}}},
		{Name: "customers", Rows: &n, ProfiledAt: 1, Columns: []catalog.Column{{Name: "id", Profile: prof()}, {Name: "email", Profile: prof()}, {Name: "card", Profile: prof()}}},
	}}
	t.Run("filtered and masked", func(t *testing.T) {
		tables := &project.Tables{Readers: &project.ReaderView{RowFilters: []project.RowFilter{{Table: "orders", Filter: "region = 'EU'"}},
			Masks: []project.Mask{{Table: "customers", Column: "card", Type: "MASK"}}}}
		hideProfiles(flow, tables, []project.ProtectedColumn{{Table: "customers", Column: "email", Tag: "personal"}})
		orders, customers := flow.Tables[0], flow.Tables[1]
		if orders.Rows != nil || orders.ProfiledAt != 0 || orders.Columns[0].Profile != nil {
			t.Errorf("a filtered table's profile shown: %+v", orders)
		}
		if customers.Rows == nil || customers.Columns[0].Profile == nil || customers.Columns[1].Profile != nil || customers.Columns[2].Profile != nil {
			t.Errorf("customers: %+v", customers)
		}
	})
}
