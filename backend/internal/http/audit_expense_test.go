package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// auditRows reads the trail straight out of the database, so these tests
// assert what was stored rather than what an endpoint chose to show.
func auditRows(t *testing.T, h *harness) []struct {
	Action   string
	Table    string
	NewValue string
} {
	t.Helper()
	rows, err := h.db.QueryContext(context.Background(),
		`SELECT action, COALESCE(table_name, ''), COALESCE(new_value, '')
		   FROM audit_logs ORDER BY created_at`)
	if err != nil {
		t.Fatalf("read audit_logs: %v", err)
	}
	defer rows.Close()

	var out []struct {
		Action   string
		Table    string
		NewValue string
	}
	for rows.Next() {
		var r struct {
			Action   string
			Table    string
			NewValue string
		}
		if err := rows.Scan(&r.Action, &r.Table, &r.NewValue); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read audit_logs: %v", err)
	}
	return out
}

// TestAMutatingRequestIsRecorded checks the middleware is actually mounted. It
// was written for the previous implementation but never wired to the new
// router, so every change went unrecorded while the audit screen showed an
// empty table and looked like a quiet day.
func TestAMutatingRequestIsRecorded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	before := len(auditRows(t, h))

	created := h.request(http.MethodPost, "/api/v1/categories", h.token,
		map[string]any{"name": "Groceries", "hsn_code": "1001"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create category: status %d body %s", created.Code, created.Body.String())
	}

	rows := auditRows(t, h)
	if len(rows) != before+1 {
		t.Fatalf("the trail grew by %d rows, want 1", len(rows)-before)
	}
	last := rows[len(rows)-1]
	// Filed under the resource it changed, not under a constant. The screen's
	// filter and the (table_name, record_id) index both depend on this, and
	// both matched nothing while every row said "http_request".
	if last.Table != "categories" {
		t.Errorf("table_name = %q, want categories", last.Table)
	}
	if last.Action != "create" {
		t.Errorf("action = %q, want create", last.Action)
	}
	if !strings.Contains(last.NewValue, "Groceries") {
		t.Errorf("the recorded body does not carry what was asked for: %q", last.NewValue)
	}
}

// TestReadsAndFailuresAreNotRecorded keeps the trail readable: a listing is
// not a change, and a request that was refused changed nothing.
func TestReadsAndFailuresAreNotRecorded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if got := h.request(http.MethodGet, "/api/v1/products", h.token, nil); got.Code != http.StatusOK {
		t.Fatalf("list products: status %d", got.Code)
	}
	// A category with no name is refused, so nothing happened to record.
	if got := h.request(http.MethodPost, "/api/v1/categories", h.token, map[string]any{}); got.Code < 400 {
		t.Fatalf("an empty category should have been refused, got %d", got.Code)
	}

	if rows := auditRows(t, h); len(rows) != 0 {
		t.Errorf("the trail has %d rows after a read and a refusal, want 0: %+v", len(rows), rows)
	}
}

// TestAPasswordNeverReachesTheAuditLog is the defect the redaction exists for.
// Every mutating request is recorded with its body, several roles can read the
// trail, and the register and change-password endpoints both carry a plaintext
// credential — so a trail that stored bodies verbatim would be a table of
// passwords.
func TestAPasswordNeverReachesTheAuditLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	const plantedPassword = "a-very-secret-password-1"
	const rotatedPassword = "a-second-secret-password-2"

	registered := h.request(http.MethodPost, "/api/v1/auth/register", h.token,
		map[string]string{"username": "till1", "password": plantedPassword, "role": "cashier"})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register: status %d body %s", registered.Code, registered.Body.String())
	}

	login := h.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "till1", "password": plantedPassword})
	if login.Code != http.StatusOK {
		t.Fatalf("cashier login: status %d body %s", login.Code, login.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	mustDecode(t, login.Body.Bytes(), &session)

	changed := h.request(http.MethodPost, "/api/v1/auth/change-password", session.Token,
		map[string]string{"old_password": plantedPassword, "new_password": rotatedPassword})
	if changed.Code != http.StatusOK {
		t.Fatalf("change password: status %d body %s", changed.Code, changed.Body.String())
	}

	rows := auditRows(t, h)
	if len(rows) == 0 {
		t.Fatal("nothing was recorded, so this test would pass vacuously")
	}
	var sawRegister, sawChange bool
	for _, r := range rows {
		for _, secret := range []string{plantedPassword, rotatedPassword} {
			if strings.Contains(r.NewValue, secret) {
				t.Fatalf("a password was written to the audit log by %s: %s", r.Action, r.NewValue)
			}
		}
		if r.Table == "auth" && r.Action == "register" {
			sawRegister = true
			if !strings.Contains(r.NewValue, "[redacted]") {
				t.Errorf("the register body was stored without a redaction marker: %s", r.NewValue)
			}
			if !strings.Contains(r.NewValue, "till1") {
				t.Errorf("redaction removed the whole body rather than the credential: %s", r.NewValue)
			}
		}
		if r.Table == "auth" && r.Action == "change_password" {
			sawChange = true
			if !strings.Contains(r.NewValue, "[redacted]") {
				t.Errorf("the change-password body was stored without a redaction marker: %s", r.NewValue)
			}
		}
	}
	if !sawRegister {
		t.Error("registering a user was not recorded at all")
	}
	if !sawChange {
		t.Error("changing a password was not recorded at all")
	}
}

// TestReadingTheAuditLogNeedsThePermission: a cashier fills the trail without
// being able to read it. The entries name who did what and carry request
// bodies, so the list of readers is deliberately shorter than the list of
// people who generate them.
func TestReadingTheAuditLogNeedsThePermission(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	registered := h.request(http.MethodPost, "/api/v1/auth/register", h.token,
		map[string]string{"username": "till2", "password": "till-password-2", "role": "cashier"})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register cashier: status %d body %s", registered.Code, registered.Body.String())
	}
	login := h.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "till2", "password": "till-password-2"})
	var session struct {
		Token string `json:"token"`
	}
	mustDecode(t, login.Body.Bytes(), &session)

	if got := h.request(http.MethodGet, "/api/v1/audit", session.Token, nil); got.Code != http.StatusForbidden {
		t.Errorf("cashier reading the trail: status %d, want 403", got.Code)
	}

	// The owner may read it, and the collection comes back under "entries".
	owner := h.request(http.MethodGet, "/api/v1/audit", h.token, nil)
	if owner.Code != http.StatusOK {
		t.Fatalf("owner reading the trail: status %d body %s", owner.Code, owner.Body.String())
	}
	var body struct {
		Entries []struct {
			Action   string `json:"action"`
			Username string `json:"username"`
		} `json:"entries"`
	}
	mustDecode(t, owner.Body.Bytes(), &body)
	if len(body.Entries) == 0 {
		t.Fatal("the trail is empty even though a user was just registered")
	}
	if body.Entries[0].Username == "" {
		t.Error("the trail does not name who made the change")
	}
}

// TestExpensesRoundTripThroughHTTP walks the sequence the expenses screen
// follows: define a heading, spend under it, list it, total it.
func TestExpensesRoundTripThroughHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// Nothing seeds expense_categories, so the list starts empty and an
	// expense cannot be recorded until a shop defines a heading of its own.
	empty := h.request(http.MethodGet, "/api/v1/expenses/categories", h.token, nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("list categories: status %d body %s", empty.Code, empty.Body.String())
	}
	var categories struct {
		Categories []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"categories"`
	}
	mustDecode(t, empty.Body.Bytes(), &categories)
	if len(categories.Categories) != 0 {
		t.Fatalf("a fresh installation lists %d expense categories, want 0", len(categories.Categories))
	}

	created := h.request(http.MethodPost, "/api/v1/expenses/categories", h.token,
		map[string]any{"name": "Rent"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create category: status %d body %s", created.Code, created.Body.String())
	}
	var category struct {
		ID string `json:"id"`
	}
	mustDecode(t, created.Body.Bytes(), &category)

	recorded := h.request(http.MethodPost, "/api/v1/expenses", h.token, map[string]any{
		"store_id":     h.storeID.String(),
		"category_id":  category.ID,
		"amount":       12000.5,
		"description":  "April rent",
		"expense_date": "2026-04-05",
	})
	if recorded.Code != http.StatusCreated {
		t.Fatalf("record expense: status %d body %s", recorded.Code, recorded.Body.String())
	}

	listed := h.request(http.MethodGet, "/api/v1/expenses?start_date=2026-04-01&end_date=2026-04-30", h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list expenses: status %d body %s", listed.Code, listed.Body.String())
	}
	var list struct {
		Expenses []struct {
			CategoryName string          `json:"category_name"`
			Amount       json.RawMessage `json:"amount"`
		} `json:"expenses"`
	}
	mustDecode(t, listed.Body.Bytes(), &list)
	if len(list.Expenses) != 1 {
		t.Fatalf("listed %d expenses, want 1", len(list.Expenses))
	}
	if list.Expenses[0].CategoryName != "Rent" {
		t.Errorf("category_name = %q", list.Expenses[0].CategoryName)
	}
	if got := string(list.Expenses[0].Amount); got != "12000.50" {
		t.Errorf("amount = %s, want 12000.50", got)
	}

	summary := h.request(http.MethodGet, "/api/v1/expenses/summary?start_date=2026-04-01&end_date=2026-04-30", h.token, nil)
	if summary.Code != http.StatusOK {
		t.Fatalf("summary: status %d body %s", summary.Code, summary.Body.String())
	}
	var totals struct {
		Summary []struct {
			Category string          `json:"category"`
			Total    json.RawMessage `json:"total"`
		} `json:"summary"`
		GrandTotal json.RawMessage `json:"grand_total"`
	}
	mustDecode(t, summary.Body.Bytes(), &totals)
	if len(totals.Summary) != 1 || totals.Summary[0].Category != "Rent" {
		t.Fatalf("summary = %+v", totals.Summary)
	}
	if got := string(totals.GrandTotal); got != "12000.50" {
		t.Errorf("grand_total = %s, want 12000.50", got)
	}

	// A malformed date is refused rather than silently ignored: a filter that
	// quietly did not apply shows the wrong period with nothing to say so.
	bad := h.request(http.MethodGet, "/api/v1/expenses?start_date=not-a-date", h.token, nil)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("malformed start_date: status %d, want 400", bad.Code)
	}
}

// TestProductUnitsRoundTripThroughHTTP checks the routes the products screen
// calls, and the envelope key it unwraps them by.
//
// The path was /products/:id/packing-units and the envelope key packing_units.
// The path is now /products/:id/units — a product unit is no longer "how it is
// boxed" but "what it may be transacted as" — while the key stays `units`.
func TestProductUnitsRoundTripThroughHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, _ := h.sellableProduct(t)

	created := h.request(http.MethodPost, "/api/v1/products/"+productID+"/units", h.token,
		map[string]any{"label": "25 kg sack", "qty_in_base": 25, "barcode": "8901001100500", "sort_order": 1})
	if created.Code != http.StatusCreated {
		t.Fatalf("create product unit: status %d body %s", created.Code, created.Body.String())
	}
	var sack struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"is_default"`
	}
	mustDecode(t, created.Body.Bytes(), &sack)

	listed := h.request(http.MethodGet, "/api/v1/products/"+productID+"/units", h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list product units: status %d body %s", listed.Code, listed.Body.String())
	}
	var body struct {
		Units []struct {
			ID        string          `json:"id"`
			Label     string          `json:"label"`
			QtyInBase json.RawMessage `json:"qty_in_base"`
			Barcode   string          `json:"barcode"`
			IsDefault bool            `json:"is_default"`
		} `json:"units"`
	}
	mustDecode(t, listed.Body.Bytes(), &body)
	// Two: the default the product was created with, and the sack. The default
	// leads, because it is what the counter offers first.
	if len(body.Units) != 2 {
		t.Fatalf("listed %d units, want 2", len(body.Units))
	}
	if !body.Units[0].IsDefault {
		t.Errorf("the listing does not lead with the default unit: %+v", body.Units[0])
	}
	var found bool
	for _, u := range body.Units {
		if u.Label != "25 kg sack" {
			continue
		}
		found = true
		if got := string(u.QtyInBase); got != "25.000" {
			t.Errorf("qty_in_base = %s, want 25.000", got)
		}
		if u.Barcode != "8901001100500" {
			t.Errorf("barcode = %q", u.Barcode)
		}
	}
	if !found {
		t.Error("the sack is missing from the listing")
	}

	// Promoting the sack clears the previous default in the same transaction;
	// two defaults would make a bare quantity ambiguous.
	promoted := h.request(http.MethodPost,
		"/api/v1/products/"+productID+"/units/"+sack.ID+"/default", h.token, nil)
	if promoted.Code != http.StatusOK {
		t.Fatalf("set default: status %d body %s", promoted.Code, promoted.Body.String())
	}
	after := h.request(http.MethodGet, "/api/v1/products/"+productID+"/units", h.token, nil)
	mustDecode(t, after.Body.Bytes(), &body)
	var defaults int
	for _, u := range body.Units {
		if u.IsDefault {
			defaults++
			if u.ID != sack.ID {
				t.Errorf("the default is %q, want the sack", u.Label)
			}
		}
	}
	if defaults != 1 {
		t.Errorf("%d units are marked default, want exactly 1", defaults)
	}

	// qty_in_base is what turns a sale into a stock movement, so a unit worth
	// nothing or less than nothing is refused rather than defaulted to one.
	rejected := h.request(http.MethodPost, "/api/v1/products/"+productID+"/units", h.token,
		map[string]any{"label": "Impossible", "qty_in_base": -1})
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Errorf("a negative qty_in_base: status %d, want 422", rejected.Code)
	}
	zero := h.request(http.MethodPost, "/api/v1/products/"+productID+"/units", h.token,
		map[string]any{"label": "Also impossible"})
	if zero.Code != http.StatusUnprocessableEntity {
		t.Errorf("an omitted qty_in_base: status %d, want 422", zero.Code)
	}
}
