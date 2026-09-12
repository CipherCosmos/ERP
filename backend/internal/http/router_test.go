package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gbos/internal/app"
	domain "gbos/internal/domain/user"
	api "gbos/internal/http"
	platform "gbos/internal/platform/postgres"
	"gbos/internal/testsupport"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// harness boots the real router over a real database, so these tests exercise
// the whole chain: authenticate, resolve the store, check the permission, run
// the handler, hit Postgres.
type harness struct {
	t       *testing.T
	router  *gin.Engine
	db      *platform.DB
	storeID uuid.UUID
	token   string
}

// TestMain sets gin's mode once for the whole binary.
//
// gin.SetMode writes a package-level variable with no synchronisation, and
// every test here builds its own harness in parallel — so calling it per
// harness is a data race that -race reports on every test in this package. It
// is set before any test runs instead, which is the only moment nothing else is
// looking at it.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db := testsupport.OpenDB(t)

	svcs, err := app.Build(db, app.Config{JWTSecret: "an-integration-test-signing-secret-value"})
	if err != nil {
		t.Fatalf("build services: %v", err)
	}
	router, shutdown, err := api.NewRouter(svcs, api.Config{CORSOrigin: "http://localhost:3000", DB: db})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	t.Cleanup(shutdown)

	h := &harness{t: t, router: router, db: db}
	h.seedOwner()
	return h
}

// seedOwner writes an owner directly, because Register deliberately refuses to
// grant that role.
func (h *harness) seedOwner() {
	h.t.Helper()
	ctx := context.Background()

	if err := h.db.QueryRowContext(ctx,
		`INSERT INTO stores (name, gstin) VALUES ('Integration Shop', '29ABCDE1234F1Z5') RETURNING id`).
		Scan(&h.storeID); err != nil {
		h.t.Fatalf("seed store: %v", err)
	}

	u, err := domain.New("owner1", "integration-pass-1", domain.RoleOwner, h.storeID)
	if err != nil {
		h.t.Fatalf("build owner: %v", err)
	}
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, store_id, is_active)
		 VALUES ($1, $2, $3, 'owner', $4, true)`,
		u.ID, u.Username, u.PasswordHash, h.storeID); err != nil {
		h.t.Fatalf("seed owner: %v", err)
	}

	body := h.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "owner1", "password": "integration-pass-1"})
	if body.Code != http.StatusOK {
		h.t.Fatalf("login: status %d body %s", body.Code, body.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode login: %v", err)
	}
	h.token = resp.Token
}

func (h *harness) request(method, target, token string, payload any) *httptest.ResponseRecorder {
	h.t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("encode payload: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func TestHealthReportsTheDatabase(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.request(http.MethodGet, "/api/v1/health", "", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", got.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The check this replaces returned 200 without touching the database, so
	// it stayed green while the database was unreachable.
	if body["database"] != "ok" {
		t.Errorf("database = %q, want ok", body["database"])
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for name, token := range map[string]string{
		"no token":      "",
		"garbage token": "not-a-real-token",
	} {
		got := h.request(http.MethodGet, "/api/v1/products", token, nil)
		if got.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, got.Code)
		}
	}
}

func TestFullCatalogueRoundTripThroughHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// A unit family, a unit of measure inside it, then a category, then a
	// product with an opening price — the sequence a shop actually follows when
	// setting up a line of trade the seed data did not anticipate.
	family := h.request(http.MethodPost, "/api/v1/unit-families", h.token,
		map[string]any{"code": "BUNDLE", "name": "Bundles"})
	if family.Code != http.StatusCreated {
		t.Fatalf("create unit family: status %d body %s", family.Code, family.Body.String())
	}
	var familyBody struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	mustDecode(t, family.Body.Bytes(), &familyBody)

	// A unit must belong to a family and carry a positive factor: one that
	// converts to nothing is not a unit. Omitting the family is refused.
	orphan := h.request(http.MethodPost, "/api/v1/uoms", h.token,
		map[string]any{"code": "ORPHAN", "name": "No family"})
	if orphan.Code != http.StatusBadRequest {
		t.Errorf("a unit with no family: status %d, want 400", orphan.Code)
	}

	unit := h.request(http.MethodPost, "/api/v1/uoms", h.token, map[string]any{
		"code": "BDL", "name": "Bundle", "decimal_places": 0,
		"family_id": familyBody.ID, "factor_to_base": 1,
	})
	if unit.Code != http.StatusCreated {
		t.Fatalf("create unit: status %d body %s", unit.Code, unit.Body.String())
	}
	var unitBody struct {
		ID           string          `json:"id"`
		FamilyID     string          `json:"family_id"`
		FactorToBase json.RawMessage `json:"factor_to_base"`
	}
	mustDecode(t, unit.Body.Bytes(), &unitBody)
	if unitBody.FamilyID != familyBody.ID {
		t.Errorf("family_id = %q, want %q", unitBody.FamilyID, familyBody.ID)
	}
	// A bare decimal marshals quoted; a factor has to reach the client as a
	// number it can compute with.
	if got := string(unitBody.FactorToBase); got != "1" {
		t.Errorf("factor_to_base = %s, want the unquoted number 1", got)
	}

	// The listing carries the new family and the units migration's own seed set.
	families := h.request(http.MethodGet, "/api/v1/unit-families", h.token, nil)
	if families.Code != http.StatusOK {
		t.Fatalf("list unit families: status %d body %s", families.Code, families.Body.String())
	}
	var familyList struct {
		Families []struct {
			Code      string `json:"code"`
			BaseUOMID string `json:"base_uom_id"`
		} `json:"families"`
	}
	mustDecode(t, families.Body.Bytes(), &familyList)
	seen := make(map[string]string, len(familyList.Families))
	for _, f := range familyList.Families {
		seen[f.Code] = f.BaseUOMID
	}
	for _, code := range []string{"WEIGHT", "VOLUME", "COUNT", "LENGTH", "AREA", "BUNDLE"} {
		if _, ok := seen[code]; !ok {
			t.Errorf("unit family %s is missing from the listing", code)
		}
	}
	// The new family adopted its first factor-one unit as its canonical one,
	// because a family with no base converts nothing.
	if seen["BUNDLE"] != unitBody.ID {
		t.Errorf("the BUNDLE family's base unit is %q, want the bundle %q", seen["BUNDLE"], unitBody.ID)
	}

	category := h.request(http.MethodPost, "/api/v1/categories", h.token,
		map[string]any{"name": "Groceries", "hsn_code": "1001"})
	if category.Code != http.StatusCreated {
		t.Fatalf("create category: status %d body %s", category.Code, category.Body.String())
	}
	var categoryBody struct {
		ID string `json:"id"`
	}
	mustDecode(t, category.Body.Bytes(), &categoryBody)

	created := h.request(http.MethodPost, "/api/v1/products", h.token, map[string]any{
		"name":          "Basmati Rice",
		"category_id":   categoryBody.ID,
		"uom_id":        unitBody.ID,
		"hsn_code":      "10063010",
		"barcode":       "8901001100001",
		"gst_rate":      5,
		"allow_decimal": true,
		"mrp":           "500.00",
		"cost_price":    "337.50",
		"sell_price":    "450.00",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create product: status %d body %s", created.Code, created.Body.String())
	}

	listed := h.request(http.MethodGet, "/api/v1/products?search=basmati", h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list products: status %d body %s", listed.Code, listed.Body.String())
	}
	var list struct {
		Products []struct {
			Name      string `json:"name"`
			SellPrice any    `json:"sell_price"`
		} `json:"products"`
	}
	mustDecode(t, listed.Body.Bytes(), &list)
	if len(list.Products) != 1 {
		t.Fatalf("found %d products, want 1", len(list.Products))
	}
	if list.Products[0].Name != "Basmati Rice" {
		t.Errorf("name = %q", list.Products[0].Name)
	}

	// A scanned barcode must resolve through the same stack.
	scanned := h.request(http.MethodGet, "/api/v1/barcodes/lookup/8901001100001", h.token, nil)
	if scanned.Code != http.StatusOK {
		t.Errorf("barcode lookup: status %d body %s", scanned.Code, scanned.Body.String())
	}
}

func TestAnotherStoreIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	// A second shop the caller has no business reading.
	var other uuid.UUID
	if err := h.db.QueryRowContext(ctx,
		`INSERT INTO stores (name) VALUES ('Somebody Elses Shop') RETURNING id`).Scan(&other); err != nil {
		t.Fatalf("seed other store: %v", err)
	}

	// The caller here is an owner, who legitimately may reach any active
	// store. A store that does not exist must still be refused.
	missing := h.request(http.MethodGet, "/api/v1/products?store_id="+uuid.New().String(), h.token, nil)
	if missing.Code != http.StatusForbidden {
		t.Errorf("unknown store: status %d, want 403", missing.Code)
	}

	reachable := h.request(http.MethodGet, "/api/v1/products?store_id="+other.String(), h.token, nil)
	if reachable.Code != http.StatusOK {
		t.Errorf("owner reaching a second store: status %d body %s", reachable.Code, reachable.Body.String())
	}
}

func TestCashierCannotReachAnotherStore(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	registered := h.request(http.MethodPost, "/api/v1/auth/register", h.token,
		map[string]string{"username": "till1", "password": "till-password-1", "role": "cashier"})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register cashier: status %d body %s", registered.Code, registered.Body.String())
	}

	login := h.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "till1", "password": "till-password-1"})
	if login.Code != http.StatusOK {
		t.Fatalf("cashier login: status %d body %s", login.Code, login.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	mustDecode(t, login.Body.Bytes(), &session)

	var other uuid.UUID
	if err := h.db.QueryRowContext(ctx,
		`INSERT INTO stores (name) VALUES ('Other Shop') RETURNING id`).Scan(&other); err != nil {
		t.Fatalf("seed other store: %v", err)
	}

	// This is the defect the scoping middleware exists to close.
	got := h.request(http.MethodGet, "/api/v1/products?store_id="+other.String(), session.Token, nil)
	if got.Code != http.StatusForbidden {
		t.Errorf("cashier reaching another store: status %d, want 403", got.Code)
	}

	// And a cashier has no permission to create accounts at all.
	escalate := h.request(http.MethodPost, "/api/v1/auth/register", session.Token,
		map[string]string{"username": "planted", "password": "planted-pass-1", "role": "manager"})
	if escalate.Code != http.StatusForbidden {
		t.Errorf("cashier registering a user: status %d, want 403", escalate.Code)
	}
}

func TestLogoutEndsTheSessionImmediately(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if got := h.request(http.MethodGet, "/api/v1/products", h.token, nil); got.Code != http.StatusOK {
		t.Fatalf("before logout: status %d", got.Code)
	}
	if got := h.request(http.MethodPost, "/api/v1/auth/logout", h.token, nil); got.Code != http.StatusOK {
		t.Fatalf("logout: status %d body %s", got.Code, got.Body.String())
	}
	if got := h.request(http.MethodGet, "/api/v1/products", h.token, nil); got.Code != http.StatusUnauthorized {
		t.Errorf("after logout: status %d, want 401 — a token cannot be un-issued, so signing out must invalidate it server-side", got.Code)
	}
}

func TestErrorsDoNotLeakDriverDetail(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// Two products with the same barcode: the second violates a unique index.
	unitID := h.seededUnit(t, "NOS")
	category := h.request(http.MethodPost, "/api/v1/categories", h.token, map[string]any{"name": "Cat"})
	var categoryBody struct{ ID string }
	mustDecode(t, category.Body.Bytes(), &categoryBody)

	product := map[string]any{
		"name": "Duplicate", "category_id": categoryBody.ID, "uom_id": unitID,
		"barcode": "8901001100777", "gst_rate": 5, "hsn_code": "10063010",
	}
	if first := h.request(http.MethodPost, "/api/v1/products", h.token, product); first.Code != http.StatusCreated {
		t.Fatalf("first create: status %d body %s", first.Code, first.Body.String())
	}
	second := h.request(http.MethodPost, "/api/v1/products", h.token, product)
	if second.Code != http.StatusConflict {
		t.Errorf("duplicate barcode: status %d, want 409", second.Code)
	}

	// The implementation this replaces answered with err.Error(), handing the
	// caller the table, column and constraint that failed.
	body := second.Body.String()
	for _, leak := range []string{"products_barcode", "SQLSTATE", "pq:", "constraint", "INSERT INTO"} {
		if bytes.Contains([]byte(body), []byte(leak)) {
			t.Errorf("response leaks %q: %s", leak, body)
		}
	}
}

// requestWithHeaders is request plus arbitrary headers, for the idempotency
// key that a till sends on every sale.
func (h *harness) requestWithHeaders(method, target, token string, payload any, headers map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatalf("encode payload: %v", err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// seededUnit returns the id of one of the units of measure the units migration
// seeds. units_of_measure.code is unique, so a test that wants a kilogram reads
// the one that is there rather than creating a second.
func (h *harness) seededUnit(t *testing.T, code string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT id FROM units_of_measure WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("read seeded unit %s: %v", code, err)
	}
	return id
}

// sellableProduct sets a shop up to sell: a unit, a category, a priced product
// and stock on the shelf. Returns the product and terminal ids.
func (h *harness) sellableProduct(t *testing.T) (productID, terminalID string) {
	t.Helper()
	ctx := context.Background()

	unitID := h.seededUnit(t, "NOS")

	category := h.request(http.MethodPost, "/api/v1/categories", h.token, map[string]any{"name": "Groceries"})
	var categoryBody struct{ ID string }
	mustDecode(t, category.Body.Bytes(), &categoryBody)

	created := h.request(http.MethodPost, "/api/v1/products", h.token, map[string]any{
		"name": "Soap Bar", "category_id": categoryBody.ID, "uom_id": unitID,
		"gst_rate": 18, "hsn_code": "34011190", "mrp": "70.00", "cost_price": "45.00", "sell_price": "60.00",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create product: status %d body %s", created.Code, created.Body.String())
	}
	var product struct{ ID string }
	mustDecode(t, created.Body.Bytes(), &product)

	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
		 VALUES ($1, $2, 'OPENING', 100, 4500.00)`, product.ID, h.storeID); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	if err := h.db.QueryRowContext(ctx,
		`INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
		h.storeID).Scan(&terminalID); err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	return product.ID, terminalID
}

func TestSellingThroughHTTPEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, terminalID := h.sellableProduct(t)

	sale := map[string]any{
		"terminal_id":  terminalID,
		"payment_mode": "cash",
		"items":        []map[string]any{{"product_id": productID, "quantity": 3}},
	}
	got := h.request(http.MethodPost, "/api/v1/invoices", h.token, sale)
	if got.Code != http.StatusCreated {
		t.Fatalf("sale: status %d body %s", got.Code, got.Body.String())
	}

	var created struct {
		Invoice struct {
			ID        string `json:"id"`
			InvoiceNo string `json:"invoice_no"`
			SubTotal  any    `json:"sub_total"`
			Total     any    `json:"total"`
			Status    string `json:"status"`
		} `json:"invoice"`
	}
	mustDecode(t, got.Body.Bytes(), &created)

	// Three bars at Rs.60 is Rs.180 taxable, 18% is Rs.32.40, total Rs.212.40.
	if fmt.Sprint(created.Invoice.SubTotal) != "180" {
		t.Errorf("sub_total = %v, want 180", created.Invoice.SubTotal)
	}
	if fmt.Sprint(created.Invoice.Total) != "212.4" {
		t.Errorf("total = %v, want 212.4", created.Invoice.Total)
	}
	if created.Invoice.Status != "completed" {
		t.Errorf("status = %q, want completed", created.Invoice.Status)
	}
	if !strings.HasPrefix(created.Invoice.InvoiceNo, "INV/T1/") {
		t.Errorf("invoice number %q does not carry the terminal series", created.Invoice.InvoiceNo)
	}

	// Voiding it, then voiding it again.
	if v := h.request(http.MethodPost, "/api/v1/invoices/"+created.Invoice.ID+"/void", h.token, nil); v.Code != http.StatusOK {
		t.Fatalf("void: status %d body %s", v.Code, v.Body.String())
	}
	if v := h.request(http.MethodPost, "/api/v1/invoices/"+created.Invoice.ID+"/void", h.token, nil); v.Code != http.StatusConflict {
		t.Errorf("second void: status %d, want 409", v.Code)
	}
}

func TestARetriedSaleIsNotChargedTwice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, terminalID := h.sellableProduct(t)

	sale := map[string]any{
		"terminal_id":  terminalID,
		"payment_mode": "cash",
		"items":        []map[string]any{{"product_id": productID, "quantity": 2}},
	}
	key := map[string]string{"Idempotency-Key": uuid.New().String()}

	first := h.requestWithHeaders(http.MethodPost, "/api/v1/invoices", h.token, sale, key)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: status %d body %s", first.Code, first.Body.String())
	}
	// A retry answers 200, not 201: this request created nothing.
	second := h.requestWithHeaders(http.MethodPost, "/api/v1/invoices", h.token, sale, key)
	if second.Code != http.StatusOK {
		t.Fatalf("retry: status %d body %s", second.Code, second.Body.String())
	}

	var a, b struct {
		Invoice struct {
			ID string `json:"id"`
		} `json:"invoice"`
		Replayed bool `json:"replayed"`
	}
	mustDecode(t, first.Body.Bytes(), &a)
	mustDecode(t, second.Body.Bytes(), &b)
	if a.Invoice.ID != b.Invoice.ID {
		t.Errorf("the retry raised a second sale: %s then %s", a.Invoice.ID, b.Invoice.ID)
	}
	if !b.Replayed {
		t.Error("the retry should report itself as a replay")
	}

	var onHand string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT quantity::text FROM stock WHERE product_id = $1`, productID).Scan(&onHand); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if !strings.HasPrefix(onHand, "98") {
		t.Errorf("on hand = %s, want 98 — the stock moved twice", onHand)
	}
}

func TestSellingIntoAnotherStoreIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, terminalID := h.sellableProduct(t)

	var other uuid.UUID
	if err := h.db.QueryRowContext(context.Background(),
		`INSERT INTO stores (name) VALUES ('Other Shop') RETURNING id`).Scan(&other); err != nil {
		t.Fatalf("seed other store: %v", err)
	}

	// The owner may reach the other store, but this store's terminal does not
	// belong to it, so the sale cannot be numbered there.
	got := h.request(http.MethodPost, "/api/v1/invoices?store_id="+other.String(), h.token, map[string]any{
		"terminal_id":  terminalID,
		"payment_mode": "cash",
		"items":        []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	if got.Code == http.StatusCreated {
		t.Error("a sale numbered on another store's terminal should be refused")
	}
}

// ---------------------------------------------------------------------------
// Wire shape
// ---------------------------------------------------------------------------

// TestProductListingWireShape asserts the bytes GET /products actually puts on
// the wire, not merely the values the service produced.
//
// Every field checked here was a defect on the products screen. gst_rate went
// out quoted, because a bare decimal.Decimal marshals as a string, and the
// screen renders it straight into `{p.gst_rate}%`. category_name, stock and
// cost_price were absent altogether, leaving a Category column stuck on "-", a
// Stock column reading 0.00 next to full shelves, and a Set Price dialog that
// pre-filled a cost of zero over the real one.
func TestProductListingWireShape(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, _ := h.sellableProduct(t)

	listed := h.request(http.MethodGet, "/api/v1/products", h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list products: status %d body %s", listed.Code, listed.Body.String())
	}
	body := listed.Body.String()

	// sellableProduct sets 18% GST, a cost of 45.00, and 100 units of stock.
	for _, want := range []string{
		`"gst_rate":18`,
		`"category_name":"Groceries"`,
		`"cost_price":45.00`,
		`"stock":100.000`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in the listing: %s", want, body)
		}
	}
	if strings.Contains(body, `"gst_rate":"`) {
		t.Errorf("gst_rate went out as a quoted string; the screen renders it as a number: %s", body)
	}

	// Decoding into the shapes the client declares is the other half of the
	// check: json.Number accepts a JSON number and refuses a JSON string, which
	// is exactly the distinction that broke the page.
	var list struct {
		Products []struct {
			GSTRate      json.Number `json:"gst_rate"`
			CategoryName string      `json:"category_name"`
			CostPrice    json.Number `json:"cost_price"`
			Stock        json.Number `json:"stock"`
		} `json:"products"`
	}
	mustDecode(t, listed.Body.Bytes(), &list)
	if len(list.Products) != 1 {
		t.Fatalf("listed %d products, want 1", len(list.Products))
	}
	got := list.Products[0]
	if got.GSTRate.String() != "18" {
		t.Errorf("gst_rate = %s, want 18", got.GSTRate)
	}
	if got.CategoryName != "Groceries" {
		t.Errorf("category_name = %q, want Groceries", got.CategoryName)
	}
	if got.CostPrice.String() != "45.00" {
		t.Errorf("cost_price = %s, want 45.00", got.CostPrice)
	}
	if got.Stock.String() != "100.000" {
		t.Errorf("stock = %s, want 100.000 — the shelf holds 100 units", got.Stock)
	}

	// Get-one goes through the same read model and must say the same things.
	one := h.request(http.MethodGet, "/api/v1/products/"+productID, h.token, nil)
	if one.Code != http.StatusOK {
		t.Fatalf("get product: status %d body %s", one.Code, one.Body.String())
	}
	for _, want := range []string{`"gst_rate":18`, `"category_name":"Groceries"`, `"cost_price":45.00`, `"stock":100.000`} {
		if !strings.Contains(one.Body.String(), want) {
			t.Errorf("expected %s from get-one: %s", want, one.Body.String())
		}
	}
}

// TestProfitAndLossWireShape asserts that gross_margin_percent reaches the
// client as a number.
//
// It was a bare decimal.Decimal and went out as "gross_margin_percent":"25".
// The profit and loss page calls .toFixed(1) on it, which a string does not
// have, so the page threw at render.
func TestProfitAndLossWireShape(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	productID, terminalID := h.sellableProduct(t)

	// One sale: 5 bars at Rs.60 costing Rs.45 each. Rs.300 taxable against
	// Rs.225 of cost is a gross margin of exactly 25%.
	sale := h.request(http.MethodPost, "/api/v1/invoices", h.token, map[string]any{
		"terminal_id":  terminalID,
		"payment_mode": "cash",
		"items":        []map[string]any{{"product_id": productID, "quantity": 5}},
	})
	if sale.Code != http.StatusCreated {
		t.Fatalf("sale: status %d body %s", sale.Code, sale.Body.String())
	}

	today := time.Now().Format("2006-01-02")
	report := h.request(http.MethodGet,
		"/api/v1/reports/profit-loss?start_date="+today+"&end_date="+today, h.token, nil)
	if report.Code != http.StatusOK {
		t.Fatalf("profit and loss: status %d body %s", report.Code, report.Body.String())
	}
	body := report.Body.String()

	if strings.Contains(body, `"gross_margin_percent":"`) {
		t.Errorf("gross_margin_percent went out as a quoted string; the client calls .toFixed(1) on it: %s", body)
	}
	var pl struct {
		GrossMargin json.Number `json:"gross_margin_percent"`
		GrossProfit json.Number `json:"gross_profit"`
	}
	mustDecode(t, report.Body.Bytes(), &pl)
	if pl.GrossMargin.String() != "25" {
		t.Errorf("gross_margin_percent = %s, want 25", pl.GrossMargin)
	}
	if pl.GrossProfit.String() != "75.00" {
		t.Errorf("gross_profit = %s, want 75.00 (300 taxable less 225 of cost)", pl.GrossProfit)
	}
}

func mustDecode(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
