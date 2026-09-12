package http_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	domain "gbos/internal/domain/user"
)

// TestRolesAreEnforcedOverHTTP pins what each role may actually do, end to end
// through the router.
//
// Every one of these was reachable by every role. The rewrite that moved routes
// into handler packages did not carry the permission guards across, so rbac was
// a table nothing consulted: a cashier could create a store, write off stock,
// void a bill and read the profit and loss. The cases below are the ones a shop
// would care about most, and they are stated as the shop would state them.
func TestRolesAreEnforcedOverHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	cashier := h.tokenFor(domain.RoleCashier)
	warehouse := h.tokenFor(domain.RoleWarehouse)
	helper := h.tokenFor(domain.RoleHelper)

	cases := []struct {
		what    string
		token   string
		method  string
		path    string
		body    map[string]any
		allowed bool
	}{
		// A cashier bills and takes payments. That is the whole job.
		{"a cashier may bill", cashier, http.MethodPost, "/invoices", map[string]any{}, true},
		{"a cashier may read the catalogue", cashier, http.MethodGet, "/products", nil, true},
		{"a cashier may add a customer", cashier, http.MethodPost, "/customers", map[string]any{}, true},

		// And nothing in the back office.
		{"a cashier may not open a store", cashier, http.MethodPost, "/stores",
			map[string]any{"name": "Back Door", "location_type": "warehouse"}, false},
		{"a cashier may not write off stock", cashier, http.MethodPost, "/losses", map[string]any{}, false},
		{"a cashier may not move stock between shops", cashier, http.MethodPost, "/stock/transfer", map[string]any{}, false},
		{"a cashier may not void a bill", cashier, http.MethodPost,
			"/invoices/00000000-0000-0000-0000-000000000000/void", map[string]any{}, false},
		{"a cashier may not see the profit and loss", cashier, http.MethodGet, "/reports/profit-loss", nil, false},
		{"a cashier may not see the expense ledger", cashier, http.MethodGet, "/expenses", nil, false},
		{"a cashier may not pay a vendor", cashier, http.MethodPost, "/vendor-payments", map[string]any{}, false},
		{"a cashier may not change a price", cashier, http.MethodPost, "/products/price", map[string]any{}, false},

		// A warehouse hand moves goods and receives deliveries.
		{"a warehouse hand may move stock", warehouse, http.MethodPost, "/stock/adjust", map[string]any{}, true},
		{"a warehouse hand may receive a delivery", warehouse, http.MethodPost, "/grn", map[string]any{}, true},
		{"a warehouse hand may write off damage", warehouse, http.MethodPost, "/losses", map[string]any{}, true},
		// But does not bill, and does not see the money.
		{"a warehouse hand may not bill", warehouse, http.MethodPost, "/invoices", map[string]any{}, false},
		{"a warehouse hand may not see the profit and loss", warehouse, http.MethodGet, "/reports/profit-loss", nil, false},
		{"a warehouse hand may not take a credit payment", warehouse, http.MethodPost, "/credit/payment", map[string]any{}, false},

		// A helper reads, including the reports, and writes nothing.
		{"a helper may read the reports", helper, http.MethodGet, "/reports/profit-loss", nil, true},
		{"a helper may read the audit trail", helper, http.MethodGet, "/audit", nil, true},
		{"a helper may not bill", helper, http.MethodPost, "/invoices", map[string]any{}, false},
		{"a helper may not move stock", helper, http.MethodPost, "/stock/adjust", map[string]any{}, false},
		{"a helper may not add a product", helper, http.MethodPost, "/products", map[string]any{}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.what, func(t *testing.T) {
			t.Parallel()
			target := "/api/v1" + tc.path
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			got := h.request(tc.method, target+sep+"store_id="+h.storeID.String(), tc.token, tc.body)

			if tc.allowed {
				if got.Code == http.StatusForbidden {
					t.Errorf("refused with 403: %s", got.Body.String())
				}
				if got.Code == http.StatusUnauthorized {
					t.Errorf("got 401, so this case is not testing what it claims: %s", got.Body.String())
				}
				return
			}
			// Refused means refused by the permission check, not by a validation
			// error that happens to arrive first. Middleware runs before the
			// handler, so a guarded route answers 403 whatever the body says.
			if got.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — this is reachable: %s", got.Code, got.Body.String())
			}
		})
	}
}

// tokenFor creates a user with the given role in the harness store and signs
// them in, through the API, so the password policy and the rule that owner
// cannot be granted both apply.
func (h *harness) tokenFor(role domain.Role) string {
	h.t.Helper()
	username := "role-" + strings.ToLower(string(role))
	password := "role-probe-password-1"

	got := h.request(http.MethodPost, "/api/v1/auth/register?store_id="+h.storeID.String(), h.token,
		map[string]any{"username": username, "password": password, "role": string(role)})
	if got.Code != http.StatusCreated && got.Code != http.StatusOK {
		h.t.Fatalf("register %s: status %d body %s", role, got.Code, got.Body.String())
	}

	got = h.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": username, "password": password})
	if got.Code != http.StatusOK {
		h.t.Fatalf("login %s: status %d body %s", role, got.Code, got.Body.String())
	}
	var resp struct{ Token string }
	if err := json.Unmarshal(got.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode login for %s: %v", role, err)
	}
	if resp.Token == "" {
		h.t.Fatalf("no token issued for %s", role)
	}
	return resp.Token
}
