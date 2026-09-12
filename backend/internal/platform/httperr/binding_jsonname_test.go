package httperr

import "testing"

// TestSnakeCaseHandlesAcronyms pins the fallback used when the validator has
// not been configured. A client never sees "hSNCode" or "h_s_n_code"; both
// would be unmatchable against the key it sent.
func TestSnakeCaseHandlesAcronyms(t *testing.T) {
	cases := map[string]string{
		"FamilyID":   "family_id",
		"HSNCode":    "hsn_code",
		"GSTRate":    "gst_rate",
		"Name":       "name",
		"QtyInBase":  "qty_in_base",
		"ID":         "id",
		"StoreID":    "store_id",
		"UnitQty":    "unit_qty",
		"CGSTAmount": "cgst_amount",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
