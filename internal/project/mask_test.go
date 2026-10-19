package project

import "testing"

func TestMaskInfo(t *testing.T) {
	for _, tc := range []struct{ mask, colType, want string }{
		{"MASK_HASH", "varchar", "CUSTOM"},
		{"MASK_HASH", "", "CUSTOM"}, // unknown: text
		{"MASK_HASH", "decimal(10,2)", "MASK_NULL"},
		{"MASK", "varchar(20)", "MASK"},
		{"MASK", "decimal(10,2)", "MASK_NULL"}, // Ranger's redaction is text: Trino refuses it on a number
		{"MASK_SHOW_LAST_4", "bigint", "MASK_NULL"},
		{"MASK_NULL", "date", "MASK_NULL"},
		{"MASK_NONE", "int", "MASK_NONE"},
		{"MASK_DATE_SHOW_YEAR", "date", "MASK_DATE_SHOW_YEAR"},
	} {
		if got := maskInfo(tc.mask, tc.colType); got.DataMaskType != tc.want {
			t.Errorf("%s on %q: %s, want %s", tc.mask, tc.colType, got.DataMaskType, tc.want)
		}
	}
}
