package audit_test

import (
	"testing"

	"gbos/internal/domain/audit"
)

// TestDescribeRoute walks the mutating routes the router actually registers.
// The cases are taken from the live route table rather than invented, because
// the value of this function is entirely in how it handles the shapes that
// exist: action suffixes, nested resources, and an id that is not last.
func TestDescribeRoute(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		template string
		path     string
		table    string
		recordID string
		action   string
	}{{
		name:     "plain create",
		method:   "POST",
		template: "/api/v1/customers",
		path:     "/api/v1/customers",
		table:    "customers",
		action:   "create",
	}, {
		name:     "plain update names the record",
		method:   "PUT",
		template: "/api/v1/customers/:id",
		path:     "/api/v1/customers/6f1c2b70-2a1e-4d55-9d0b-1f2e3a4b5c6d",
		table:    "customers",
		recordID: "6f1c2b70-2a1e-4d55-9d0b-1f2e3a4b5c6d",
		action:   "update",
	}, {
		// The case the whole function exists for: voiding an invoice is the
		// single most consequential thing in the trail, and it has to be
		// findable by invoice.
		name:     "action suffix beats the method",
		method:   "POST",
		template: "/api/v1/invoices/:id/void",
		path:     "/api/v1/invoices/8ac91f22-0000-4000-8000-000000000001/void",
		table:    "invoices",
		recordID: "8ac91f22-0000-4000-8000-000000000001",
		action:   "void",
	}, {
		name:     "multi-segment action is joined",
		method:   "POST",
		template: "/api/v1/invoices/gstr1/mark-filed",
		path:     "/api/v1/invoices/gstr1/mark-filed",
		table:    "invoices",
		action:   "gstr1_mark_filed",
	}, {
		// The id sits in the middle here, not at the end.
		name:     "identifier in a trailing position",
		method:   "POST",
		template: "/api/v1/purchases/cancel/:id",
		path:     "/api/v1/purchases/cancel/po-77",
		table:    "purchases",
		recordID: "po-77",
		action:   "cancel",
	}, {
		// Two identifiers: the terminal is what changed, so the store id must
		// not win merely by coming first.
		name:     "last identifier is the record",
		method:   "POST",
		template: "/api/v1/stores/:id/terminals/:terminalID/deactivate",
		path:     "/api/v1/stores/store-1/terminals/terminal-9/deactivate",
		table:    "stores",
		recordID: "terminal-9",
		action:   "terminals_deactivate",
	}, {
		name:     "nested collection",
		method:   "POST",
		template: "/api/v1/products/:id/packing-units",
		path:     "/api/v1/products/prod-3/packing-units",
		table:    "products",
		recordID: "prod-3",
		action:   "packing_units",
	}, {
		name:     "sub-resource under a module",
		method:   "POST",
		template: "/api/v1/expenses/categories",
		path:     "/api/v1/expenses/categories",
		table:    "expenses",
		action:   "categories",
	}, {
		name:     "hyphenated resource is normalised",
		method:   "POST",
		template: "/api/v1/purchase-returns/:id/approve",
		path:     "/api/v1/purchase-returns/pr-2/approve",
		table:    "purchase_returns",
		recordID: "pr-2",
		action:   "approve",
	}, {
		name:     "delete",
		method:   "DELETE",
		template: "/api/v1/vendors/:id",
		path:     "/api/v1/vendors/v-1",
		table:    "vendors",
		recordID: "v-1",
		action:   "delete",
	}, {
		// gin supplies no template for an unmatched route. Falling back to the
		// path still names the resource, and never invents an identifier it
		// cannot distinguish from a literal.
		name:     "no template falls back to the path",
		method:   "POST",
		template: "",
		path:     "/api/v1/stock/adjust",
		table:    "stock",
		action:   "adjust",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table, recordID, action := audit.DescribeRoute(tc.method, tc.template, tc.path)
			if table != tc.table {
				t.Errorf("table = %q, want %q", table, tc.table)
			}
			if recordID != tc.recordID {
				t.Errorf("recordID = %q, want %q", recordID, tc.recordID)
			}
			if action != tc.action {
				t.Errorf("action = %q, want %q", action, tc.action)
			}
		})
	}
}

// TestDescribeRouteAlwaysNamesAnAction guards the one invariant the caller
// depends on: NewEntry rejects a blank action, and dropping an audit row
// because a route had an unusual shape would be a silent gap in the trail.
func TestDescribeRouteAlwaysNamesAnAction(t *testing.T) {
	for _, tc := range []struct{ method, template, path string }{
		{"POST", "", ""},
		{"PUT", "/api/v1", "/api/v1"},
		{"HEAD", "/api/v1/products", "/api/v1/products"},
		{"POST", "/api/v1/:id", "/api/v1/only-an-id"},
	} {
		if _, _, action := audit.DescribeRoute(tc.method, tc.template, tc.path); action == "" {
			t.Errorf("DescribeRoute(%q, %q, %q) returned a blank action",
				tc.method, tc.template, tc.path)
		}
	}
}
