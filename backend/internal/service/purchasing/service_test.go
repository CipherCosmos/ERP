package purchasing_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/purchasing"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/purchasing"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Test doubles and fixtures
// ---------------------------------------------------------------------------

// fakeStock stands in for the stock aggregate, which is being ported
// separately. Purchasing owns the decision of how much stock moves and when;
// the stock aggregate owns how. These tests assert the decision, so the
// recorder is exactly the right depth of double — a real stock table here would
// let a purchasing defect hide behind a stock defect.
type fakeStock struct {
	mu       sync.Mutex
	received []service.StockMovement
	removed  []service.StockMovement
	// failRemoveFor makes RemoveFromStock refuse, standing in for the stock
	// aggregate's non-negative invariant.
	failRemoveFor map[uuid.UUID]error
}

func newFakeStock() *fakeStock {
	return &fakeStock{failRemoveFor: map[uuid.UUID]error{}}
}

func (f *fakeStock) ReceiveIntoStock(_ context.Context, _ platform.Tx, m service.StockMovement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, m)
	return nil
}

func (f *fakeStock) RemoveFromStock(_ context.Context, _ platform.Tx, m service.StockMovement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failRemoveFor[m.ProductID]; ok {
		return err
	}
	f.removed = append(f.removed, m)
	return nil
}

func (f *fakeStock) receivedQty(productID uuid.UUID) money.Quantity {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := money.ZeroQuantity()
	for _, m := range f.received {
		if m.ProductID == productID {
			total = total.Add(m.Quantity)
		}
	}
	return total
}

func (f *fakeStock) removedQty(productID uuid.UUID) money.Quantity {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := money.ZeroQuantity()
	for _, m := range f.removed {
		if m.ProductID == productID {
			total = total.Add(m.Quantity)
		}
	}
	return total
}

// fixture is one isolated database with the minimum a purchase order needs.
type fixture struct {
	svc   *service.Service
	db    *platform.DB
	stock *fakeStock

	storeID    uuid.UUID // GSTIN 29..., Karnataka
	terminalID uuid.UUID // code T1
	userID     uuid.UUID
	uomID      uuid.UUID
	categoryID uuid.UUID
}

const (
	karnatakaGSTIN   = "29AAAAA0000A1Z5"
	karnatakaVendor  = "29BBBBB0000B1Z5"
	maharashtraVndr  = "27CCCCC0000C1Z5"
	testOrderDateStr = "2026-06-15" // financial year 2026-27
)

func orderDate(t *testing.T) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", testOrderDateStr)
	if err != nil {
		t.Fatalf("parse order date: %v", err)
	}
	return d
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	// A small pool per test. These tests run in parallel and each gets its own
	// database, so the production default of 20 connections apiece would exhaust
	// a stock max_connections long before the suite finished. Nothing here needs
	// concurrency within a single test.
	db, err := platform.Open(ctx, platform.Config{
		DSN:          testsupport.DSN(t),
		MaxOpenConns: 3,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &fixture{db: db, stock: newFakeStock()}
	f.storeID = seedStore(t, db, "Main Shop", karnatakaGSTIN)
	f.terminalID = seedTerminal(t, db, f.storeID, "T1")
	f.userID = seedUser(t, db, "storekeeper-"+uuid.NewString()[:8])

	// The units migration seeds the ordinary Indian retail vocabulary, so the
	// kilogram is already on file with its family and conversion factor. A test
	// that inserted its own would collide on the unique code, and one that
	// invented a KG with no family would be a unit nothing can convert.
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM units_of_measure WHERE code = 'KG'`).Scan(&f.uomID); err != nil {
		t.Fatalf("find the kilogram: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`).Scan(&f.categoryID); err != nil {
		t.Fatalf("seed category: %v", err)
	}

	f.svc = service.New(db, repo.NewPurchasing(), f.stock)
	return f
}

func seedStore(t *testing.T, db *platform.DB, name, gstin string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO stores (name, gstin) VALUES ($1, $2) RETURNING id`, name, gstin).Scan(&id); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return id
}

func seedTerminal(t *testing.T, db *platform.DB, storeID uuid.UUID, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO terminals (store_id, code, name) VALUES ($1, $2, $2) RETURNING id`,
		storeID, code).Scan(&id); err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	return id
}

func seedUser(t *testing.T, db *platform.DB, username string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, role) VALUES ($1, 'x', 'manager') RETURNING id`,
		username).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// seedProduct creates a product at a given GST rate, bought and sold loose by
// its stocking unit.
//
// One default unit worth one of the stocking unit is what every product had
// before bulk buying existed, so this is the fixture for every case that is not
// about units.
func (f *fixture) seedProduct(t *testing.T, name string, gstRate int64) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.QueryRowContext(context.Background(),
		`INSERT INTO products (name, category_id, uom_id, gst_rate, allow_decimal)
		 VALUES ($1, $2, $3, $4, true) RETURNING id`,
		name, f.categoryID, f.uomID, gstRate).Scan(&id); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	f.seedUnit(t, id, "Loose", "1", true)
	return id
}

// seedUnit adds a way the product may be transacted in. qtyInBase is how many
// of the product's stocking unit one of these amounts to: "1" for the loose
// kilo, "25" for a sack.
func (f *fixture) seedUnit(t *testing.T, productID uuid.UUID, label, qtyInBase string, isDefault bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.QueryRowContext(context.Background(),
		`INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		productID, f.uomID, label, money.MustQuantityFromString(qtyInBase), isDefault).Scan(&id); err != nil {
		t.Fatalf("seed unit %s: %v", label, err)
	}
	return id
}

// seedUnitCost records what the store paid for one of a unit, which is the
// fallback a receipt uses when it states no landed cost of its own.
func (f *fixture) seedUnitCost(t *testing.T, productID, unitID uuid.UUID, mrp, cost, sell string) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(),
		`INSERT INTO product_prices (product_id, store_id, product_unit_id, mrp, cost_price, sell_price)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		productID, f.storeID, unitID,
		money.MustFromString(mrp), money.MustFromString(cost), money.MustFromString(sell)); err != nil {
		t.Fatalf("seed price: %v", err)
	}
}

// defaultUnit is the unit seedProduct gave the product, for a test that needs
// to name it.
func (f *fixture) defaultUnit(t *testing.T, productID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT id FROM product_units WHERE product_id = $1 AND is_default`, productID).Scan(&id); err != nil {
		t.Fatalf("find the default unit: %v", err)
	}
	return id
}

func (f *fixture) seedVendor(t *testing.T, name, gstin string, creditLimit string) uuid.UUID {
	t.Helper()
	v, err := f.svc.CreateVendor(context.Background(), domain.VendorInput{
		Name:        name,
		GSTIN:       gstin,
		State:       "Karnataka",
		CreditLimit: money.MustFromString(creditLimit),
	})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	return v.ID
}

// order55At5 is one line of a product costing Rs.55 at 5% GST — the exact case
// the money package documents, where the tax of 2.75 cannot be halved into
// paise and a naive split drifts a paisa per line.
func order55At5(productID uuid.UUID, claimed string) domain.LineInput {
	line := domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(1),
		Rate:      money.MustFromString("55.00"),
	}
	if claimed != "" {
		line.ClaimedTotal = money.MustFromString(claimed)
	}
	return line
}

func (f *fixture) createOrder(t *testing.T, vendorID uuid.UUID, lines ...domain.LineInput) (domain.PurchaseOrder, []domain.PurchaseItem) {
	t.Helper()
	po, items, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID:    f.storeID,
		VendorID:   vendorID,
		TerminalID: f.terminalID,
		OrderDate:  orderDate(t),
		Lines:      lines,
	})
	if err != nil {
		t.Fatalf("create purchase order: %v", err)
	}
	return po, items
}

func (f *fixture) poStatus(t *testing.T, poID uuid.UUID) string {
	t.Helper()
	var status string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status FROM purchase_orders WHERE id = $1`, poID).Scan(&status); err != nil {
		t.Fatalf("read purchase order status: %v", err)
	}
	return status
}

// ---------------------------------------------------------------------------
// Purchase order totals
// ---------------------------------------------------------------------------

func TestPurchaseOrderTotalsAreComputedServerSide(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Local Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Basmati Rice 1kg", 5)

	po, items := f.createOrder(t, vendorID, order55At5(productID, ""))

	// 55.00 taxable, 5% GST = 2.75, split 1.38 + 1.37 so the two halves sum to
	// the whole rather than to 2.76.
	if !po.SubTotal.Equal(money.MustFromString("55.00")) {
		t.Errorf("sub total = %s, want 55.00", po.SubTotal)
	}
	if !po.CGST.Equal(money.MustFromString("1.38")) {
		t.Errorf("cgst = %s, want 1.38", po.CGST)
	}
	if !po.SGST.Equal(money.MustFromString("1.37")) {
		t.Errorf("sgst = %s, want 1.37", po.SGST)
	}
	if !po.CGST.Add(po.SGST).Equal(money.MustFromString("2.75")) {
		t.Errorf("cgst + sgst = %s, want exactly the 2.75 of tax due", po.CGST.Add(po.SGST))
	}
	if !po.Total.Equal(money.MustFromString("57.75")) {
		t.Errorf("total = %s, want 57.75", po.Total)
	}
	if len(items) != 1 || !items[0].Total.Equal(money.MustFromString("57.75")) {
		t.Fatalf("line totals = %+v", items)
	}

	// And what is stored is what was computed, not something rounded on the way
	// in and back out.
	stored, storedItems, err := f.svc.GetPurchaseOrder(ctx, po.ID, f.storeID)
	if err != nil {
		t.Fatalf("get purchase order: %v", err)
	}
	if !stored.Total.Equal(po.Total) || !stored.CGST.Equal(po.CGST) || !stored.SGST.Equal(po.SGST) {
		t.Errorf("reloaded order %+v does not match what was written", stored)
	}
	if !storedItems[0].Rate.Equal(money.MustFromString("55.00")) {
		t.Errorf("reloaded rate = %s, want 55.00", storedItems[0].Rate)
	}
}

func TestLyingClientLineTotalIsRejected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Local Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Basmati Rice 1kg", 5)

	// The honest figure is accepted.
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "57.75")},
	}); err != nil {
		t.Fatalf("a correct client total should be accepted: %v", err)
	}

	// A claim that is wrong by any amount is refused. One paisa is not
	// tolerance, it is a disagreement: both sides compute exact decimals from
	// the same inputs, so there is nothing for a tolerance to absorb.
	for _, claim := range []string{"57.74", "57.76", "50.00", "1000.00"} {
		_, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
			StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
			OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, claim)},
		})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("client total %s: expected ErrValidation, got %v", claim, err)
		}
	}

	var n int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM purchase_orders WHERE store_id = $1`, f.storeID).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if n != 1 {
		t.Errorf("%d orders survived, want only the one with an honest total", n)
	}
}

func TestTaxSplitsByPlaceOfSupply(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	productID := f.seedProduct(t, "Basmati Rice 1kg", 5)

	// The store is in Karnataka (29). A Karnataka vendor is an intra-state
	// supply: CGST + SGST, IGST zero.
	local := f.seedVendor(t, "Karnataka Supplier", karnatakaVendor, "0")
	intra, _ := f.createOrder(t, local, order55At5(productID, ""))
	if !intra.IGST.IsZero() {
		t.Errorf("intra-state order carries IGST %s, want zero", intra.IGST)
	}
	if !intra.CGST.Add(intra.SGST).Equal(money.MustFromString("2.75")) {
		t.Errorf("intra-state CGST+SGST = %s, want 2.75", intra.CGST.Add(intra.SGST))
	}

	// A Maharashtra vendor (27) is inter-state: the whole tax is IGST.
	distant := f.seedVendor(t, "Mumbai Supplier", maharashtraVndr, "0")
	inter, _ := f.createOrder(t, distant, order55At5(productID, ""))
	if !inter.IGST.Equal(money.MustFromString("2.75")) {
		t.Errorf("inter-state IGST = %s, want 2.75", inter.IGST)
	}
	if !inter.CGST.IsZero() || !inter.SGST.IsZero() {
		t.Errorf("inter-state order carries CGST %s and SGST %s, want both zero", inter.CGST, inter.SGST)
	}

	// Either way the buyer pays the same total; only the destination of the tax
	// differs.
	if !intra.Total.Equal(inter.Total) {
		t.Errorf("intra-state total %s and inter-state total %s differ", intra.Total, inter.Total)
	}

	// The domain rule this rests on, stated directly.
	if got := money.SplitTax(money.MustFromString("55.00"), money.RateFromInt(5), true); !got.IGST.Equal(money.MustFromString("2.75")) {
		t.Errorf("SplitTax inter-state = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Purchase order numbering
// ---------------------------------------------------------------------------

func TestPurchaseOrderNumbersAreConsecutivePerStoreAndTerminal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	for i, want := range []string{"PO/T1/2026-27/00001", "PO/T1/2026-27/00002", "PO/T1/2026-27/00003"} {
		po, _ := f.createOrder(t, vendorID, order55At5(productID, ""))
		if po.PONo != want {
			t.Errorf("order %d numbered %q, want %q", i+1, po.PONo, want)
		}
	}

	// A second terminal in the same store runs its own series, which is why
	// po_no is unique per store rather than globally: both counters legitimately
	// reach 00001.
	secondTerminal := seedTerminal(t, f.db, f.storeID, "T2")
	po, _, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: secondTerminal,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	})
	if err != nil {
		t.Fatalf("create on second terminal: %v", err)
	}
	if po.PONo != "PO/T2/2026-27/00001" {
		t.Errorf("second terminal numbered %q, want PO/T2/2026-27/00001", po.PONo)
	}

	// And a different store's T1 is a different series again.
	otherStore := seedStore(t, f.db, "Branch", karnatakaGSTIN)
	otherTerminal := seedTerminal(t, f.db, otherStore, "T1")
	po, _, err = f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: otherStore, VendorID: vendorID, TerminalID: otherTerminal,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	})
	if err != nil {
		t.Fatalf("create in second store: %v", err)
	}
	if po.PONo != "PO/T1/2026-27/00001" {
		t.Errorf("second store numbered %q, want its own PO/T1/2026-27/00001", po.PONo)
	}

	// A terminal that belongs to another store cannot number an order in this
	// one.
	if _, _, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: otherTerminal,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	}); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected a foreign terminal to be rejected, got %v", err)
	}
}

func TestPurchaseOrderNumberIsNotBurnedByAFailedOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	first, _ := f.createOrder(t, vendorID, order55At5(productID, ""))
	if first.PONo != "PO/T1/2026-27/00001" {
		t.Fatalf("first order numbered %q", first.PONo)
	}

	// A rejected order rolls back, and its number rolls back with it. A
	// Postgres SEQUENCE would have consumed 00002 here and left a gap, which is
	// what CGST Rule 46(b) forbids of a document series.
	if _, _, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "999.00")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected the lying total to be rejected, got %v", err)
	}

	next, _ := f.createOrder(t, vendorID, order55At5(productID, ""))
	if next.PONo != "PO/T1/2026-27/00002" {
		t.Errorf("after a rolled-back order the next number is %q, want PO/T1/2026-27/00002", next.PONo)
	}
}

// ---------------------------------------------------------------------------
// Vendor credit limit and exposure
// ---------------------------------------------------------------------------

func TestVendorCreditLimitBlocksAnOrderThatExceedsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	// A limit of 100 admits one order of 57.75 and not two.
	vendorID := f.seedVendor(t, "Capped Supplier", karnatakaVendor, "100.00")
	productID := f.seedProduct(t, "Rice", 5)

	f.createOrder(t, vendorID, order55At5(productID, ""))

	_, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	})
	if !errors.Is(err, domain.ErrCreditLimitExceeded) {
		t.Fatalf("expected ErrCreditLimitExceeded, got %v", err)
	}

	// The failed order left nothing behind.
	var n int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM purchase_orders WHERE vendor_id = $1`, vendorID).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if n != 1 {
		t.Errorf("%d orders exist, want 1", n)
	}

	// Paying the vendor frees the limit again.
	if _, err := f.svc.RecordVendorPayment(ctx, domain.PaymentInput{
		VendorID: vendorID, StoreID: f.storeID, UserID: f.userID,
		Amount: money.MustFromString("57.75"), PaymentDate: orderDate(t), PaymentMode: "bank",
	}); err != nil {
		t.Fatalf("record payment: %v", err)
	}
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	}); err != nil {
		t.Errorf("after settling the first order the second should fit: %v", err)
	}

	// A vendor with no limit recorded is not capped.
	uncapped := f.seedVendor(t, "Open Supplier", karnatakaVendor, "0")
	for range 3 {
		if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
			StoreID: f.storeID, VendorID: uncapped, TerminalID: f.terminalID,
			OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
		}); err != nil {
			t.Fatalf("uncapped vendor: %v", err)
		}
	}
}

func TestFullyReceivedButUnpaidOrderStaysInVendorExposure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Capped Supplier", karnatakaVendor, "100.00")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, order55At5(productID, ""))
	receiveAll(t, f, po.ID, productID, money.QuantityFromInt(1))
	if got := f.poStatus(t, po.ID); got != string(domain.StatusReceived) {
		t.Fatalf("order status = %s, want received", got)
	}

	// The defect: the old exposure query summed only 'pending' and 'partial'
	// orders, so this fully received and entirely unpaid order vanished from
	// the vendor's exposure at exactly the moment the money became due.
	exposure, err := f.svc.VendorExposure(ctx, vendorID)
	if err != nil {
		t.Fatalf("vendor exposure: %v", err)
	}
	if !exposure.Outstanding().Equal(money.MustFromString("57.75")) {
		t.Errorf("outstanding = %s, want the 57.75 still owed on a received but unpaid order", exposure.Outstanding())
	}

	// And the limit still bites.
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	}); !errors.Is(err, domain.ErrCreditLimitExceeded) {
		t.Errorf("expected the received-but-unpaid order to still consume the limit, got %v", err)
	}

	// A cancelled order, by contrast, is not exposure.
	cancelled, _ := f.createOrderForVendor(t, vendorID, productID)
	if err := f.svc.CancelPurchaseOrder(ctx, cancelled.ID, f.storeID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	after, err := f.svc.VendorExposure(ctx, vendorID)
	if err != nil {
		t.Fatalf("vendor exposure: %v", err)
	}
	if !after.Outstanding().Equal(money.MustFromString("57.75")) {
		t.Errorf("outstanding = %s after a cancellation, want it unchanged at 57.75", after.Outstanding())
	}
}

// createOrderForVendor raises an order without the credit check tripping, by
// using a product priced at zero.
func (f *fixture) createOrderForVendor(t *testing.T, vendorID, productID uuid.UUID) (domain.PurchaseOrder, []domain.PurchaseItem) {
	t.Helper()
	po, items, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t),
		Lines: []domain.LineInput{{
			ProductID: productID,
			UnitQty:   money.QuantityFromInt(1),
			Rate:      money.Zero(),
		}},
	})
	if err != nil {
		t.Fatalf("create zero-value order: %v", err)
	}
	return po, items
}

// ---------------------------------------------------------------------------
// Receiving goods
// ---------------------------------------------------------------------------

func receiveAll(t *testing.T, f *fixture, poID, productID uuid.UUID, qty money.Quantity) domain.GoodsReceipt {
	t.Helper()
	g, _, err := f.svc.ReceiveGoods(context.Background(), service.ReceiveGoodsInput{
		POID: poID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: qty}},
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}
	return g
}

func TestRejectedGoodsNeverReachStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(100),
		Rate:      money.MustFromString("55.00"),
	})

	// 100 arrive and all 100 are rejected. The code this replaces read
	// "AcceptedQty is zero" as "not stated" and fell back to ReceivedQty,
	// putting 100 units of rejected goods into sellable stock.
	_, items, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   productID,
			ReceivedQty: money.QuantityFromInt(100),
			RejectedQty: money.QuantityFromInt(100),
			Reason:      "water damaged",
		}},
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}

	if !items[0].AcceptedQty.IsZero() {
		t.Errorf("accepted = %s, want zero", items[0].AcceptedQty)
	}
	if got := f.stock.receivedQty(productID); !got.IsZero() {
		t.Errorf("%s units reached stock, want nothing — every unit was rejected", got)
	}
	if len(f.stock.received) != 0 {
		t.Errorf("stock was touched %d times for a wholly rejected delivery", len(f.stock.received))
	}

	// The receipt still records what physically arrived, and the order is
	// closed: the goods came, they were refused, nothing more is coming.
	var received, rejected, accepted money.Quantity
	if err := f.db.QueryRowContext(ctx,
		`SELECT received_qty, rejected_qty, accepted_qty FROM goods_receipt_items WHERE product_id = $1`,
		productID).Scan(&received, &rejected, &accepted); err != nil {
		t.Fatalf("read receipt line: %v", err)
	}
	if !received.Equal(money.QuantityFromInt(100)) || !rejected.Equal(money.QuantityFromInt(100)) || !accepted.IsZero() {
		t.Errorf("stored receipt line: received %s, rejected %s, accepted %s", received, rejected, accepted)
	}
	if got := f.poStatus(t, po.ID); got != string(domain.StatusReceived) {
		t.Errorf("order status = %s, want received", got)
	}
}

// TestReceivingWithNoStatedCostFallsBackToTheOrderedRate.
//
// Stock landing at zero cost is not a cheap batch, it is a missing one: COGS
// vanishes for everything sold from it, gross profit reads as the whole sale
// price, and nothing anywhere says so.
//
// It happened whenever a receipt stated no cost and the unit carried none
// recorded — an ordinary state for a pack somebody added without pricing it.
// The purchase order always knows what the shop agreed to pay, so that is the
// last resort.
func TestReceivingWithNoStatedCostFallsBackToTheOrderedRate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	// The product has no recorded cost price, and the receipt states none.
	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(10),
		Rate:      money.MustFromString("900.00"),
	})

	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   productID,
			ReceivedQty: money.QuantityFromInt(10),
		}},
	}); err != nil {
		t.Fatalf("receive goods: %v", err)
	}

	if len(f.stock.received) != 1 {
		t.Fatalf("stock was touched %d times, want once", len(f.stock.received))
	}
	got := f.stock.received[0].TotalCost
	if got.IsZero() {
		t.Fatal("stock landed at zero cost — every sale from this batch would report the full price as profit")
	}
	// The order's rate times what was accepted: ten at Rs.900.
	if want := money.MustFromString("9000.00"); !got.Equal(want) {
		t.Errorf("landed cost = %s, want ten at the ordered rate, %s", got, want)
	}
}

func TestPartialRejectionBooksOnlyWhatWasAccepted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(100), Rate: money.MustFromString("55.00"),
	})

	if _, _, err := f.svc.ReceiveGoods(context.Background(), service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   productID,
			ReceivedQty: money.QuantityFromInt(100),
			RejectedQty: money.QuantityFromInt(30),
		}},
	}); err != nil {
		t.Fatalf("receive goods: %v", err)
	}

	if got := f.stock.receivedQty(productID); !got.Equal(money.QuantityFromInt(70)) {
		t.Errorf("%s units reached stock, want the 70 that were accepted", got)
	}
}

func TestStatedAcceptedQuantityMustReconcile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)
	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(100), Rate: money.MustFromString("55.00"),
	})

	// Stating an accepted quantity that does not add up with the rejected one
	// is a counting error at the receiving bay, and is refused rather than
	// quietly reconciled.
	accepted := money.QuantityFromInt(90)
	_, _, err := f.svc.ReceiveGoods(context.Background(), service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   productID,
			ReceivedQty: money.QuantityFromInt(100),
			RejectedQty: money.QuantityFromInt(30),
			AcceptedQty: &accepted,
		}},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for 90 + 30 != 100, got %v", err)
	}

	// A stated zero is honoured as zero rather than read as "unstated".
	zero := money.ZeroQuantity()
	if _, items, err := f.svc.ReceiveGoods(context.Background(), service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   productID,
			ReceivedQty: money.QuantityFromInt(100),
			RejectedQty: money.QuantityFromInt(100),
			AcceptedQty: &zero,
		}},
	}); err != nil {
		t.Fatalf("receive goods: %v", err)
	} else if !items[0].AcceptedQty.IsZero() {
		t.Errorf("accepted = %s, want the stated zero", items[0].AcceptedQty)
	}
	if got := f.stock.receivedQty(productID); !got.IsZero() {
		t.Errorf("%s units reached stock, want nothing", got)
	}
}

func TestOverReceiptIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(100), Rate: money.MustFromString("55.00"),
	})

	// 500 against an order for 100. Nothing in the previous implementation
	// stopped this; the surplus went straight into stock.
	_, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(500)}},
	})
	if !errors.Is(err, domain.ErrOverReceipt) {
		t.Fatalf("expected ErrOverReceipt, got %v", err)
	}
	if got := f.stock.receivedQty(productID); !got.IsZero() {
		t.Errorf("%s units reached stock from a refused receipt", got)
	}
	if got := f.poStatus(t, po.ID); got != string(domain.StatusPending) {
		t.Errorf("order status = %s after a refused receipt, want pending", got)
	}

	// Receiving in instalments is fine, right up to the ordered quantity.
	receiveAll(t, f, po.ID, productID, money.QuantityFromInt(60))
	if got := f.poStatus(t, po.ID); got != string(domain.StatusPartial) {
		t.Errorf("order status = %s after 60 of 100, want partial", got)
	}

	// The order is still open and still receivable, so this is the cap and not
	// the status guard talking: 40 remain and 41 is one too many.
	_, _, err = f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(41)}},
	})
	if !errors.Is(err, domain.ErrOverReceipt) {
		t.Errorf("expected ErrOverReceipt for 41 against 40 outstanding, got %v", err)
	}

	receiveAll(t, f, po.ID, productID, money.QuantityFromInt(40))
	if got := f.poStatus(t, po.ID); got != string(domain.StatusReceived) {
		t.Errorf("order status = %s after 100 of 100, want received", got)
	}

	// And once it is closed, nothing more arrives at all.
	_, _, err = f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(1)}},
	})
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected a fully received order to be closed, got %v", err)
	}
	if got := f.stock.receivedQty(productID); !got.Equal(money.QuantityFromInt(100)) {
		t.Errorf("stock received %s in total, want exactly the 100 ordered", got)
	}
}

func TestReceiptFallsBackToTheStoresRecordedCostPrice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	f.seedUnitCost(t, productID, f.defaultUnit(t, productID), "70.00", "41.2500", "60.00")

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00"),
	})

	// No cost stated on the receipt line, so the store's recorded cost applies.
	receiveAll(t, f, po.ID, productID, money.QuantityFromInt(4))
	if got := f.stock.received[0].TotalCost; !got.Equal(money.MustFromString("165.00")) {
		t.Errorf("landed cost sent to stock = %s, want four at the store's 41.25, 165.00", got)
	}
	if f.stock.received[0].BatchNo == "" {
		t.Error("a blank batch reached the stock aggregate; the receipt must allocate one")
	}

	// A stated landed cost wins over the recorded one.
	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID: productID, ReceivedQty: money.QuantityFromInt(6),
			BatchNo: "LOT-2026-06", CostPrice: money.MustFromString("52.10"),
		}},
	}); err != nil {
		t.Fatalf("receive goods: %v", err)
	}
	last := f.stock.received[len(f.stock.received)-1]
	if !last.TotalCost.Equal(money.MustFromString("312.60")) {
		t.Errorf("landed cost = %s, want six at the stated 52.10, 312.60", last.TotalCost)
	}
	if last.BatchNo != "LOT-2026-06" {
		t.Errorf("batch = %q, want the stated LOT-2026-06", last.BatchNo)
	}
}

func TestCancelledOrderCannotBeResurrectedByAReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00"),
	})
	if err := f.svc.CancelPurchaseOrder(ctx, po.ID, f.storeID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// The old goods receipt path wrote `SET status = 'received'` with no guard,
	// so a receipt against a cancelled order silently brought it back to life
	// and booked the goods in.
	_, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(10)}},
	})
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition receiving against a cancelled order, got %v", err)
	}
	if got := f.poStatus(t, po.ID); got != string(domain.StatusCancelled) {
		t.Errorf("order status = %s, want it to have stayed cancelled", got)
	}
	if got := f.stock.receivedQty(productID); !got.IsZero() {
		t.Errorf("%s units reached stock against a cancelled order", got)
	}

	var receipts int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM goods_receipts WHERE po_id = $1`, po.ID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 0 {
		t.Errorf("%d goods receipts were written against a cancelled order", receipts)
	}

	// Cancelling twice is not a second cancellation.
	if err := f.svc.CancelPurchaseOrder(ctx, po.ID, f.storeID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected a second cancel to be refused, got %v", err)
	}
}

func TestPartiallyReceivedOrderCannotBeCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00"),
	})
	receiveAll(t, f, po.ID, productID, money.QuantityFromInt(4))

	if err := f.svc.CancelPurchaseOrder(context.Background(), po.ID, f.storeID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected a partially received order to refuse cancellation, got %v", err)
	}
	if got := f.poStatus(t, po.ID); got != string(domain.StatusPartial) {
		t.Errorf("order status = %s, want partial", got)
	}
}

func TestReceiptRollsBackEntirelyWhenStockRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)
	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00"),
	})

	// The stock aggregate refusing a movement must take the receipt document
	// and the advanced quantities with it, not leave a receipt recording goods
	// that were never booked in.
	stubbornStock := &refusingStock{err: errors.New("stock ledger refused the movement")}
	svc := service.New(f.db, repo.NewPurchasing(), stubbornStock)
	if _, _, err := svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(10)}},
	}); err == nil {
		t.Fatal("expected the receipt to fail when stock refuses")
	}

	var receipts int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM goods_receipts WHERE po_id = $1`, po.ID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 0 {
		t.Errorf("%d goods receipts survived a rolled-back transaction", receipts)
	}
	var receivedQty money.Quantity
	if err := f.db.QueryRowContext(ctx,
		`SELECT received_qty FROM purchase_items WHERE po_id = $1`, po.ID).Scan(&receivedQty); err != nil {
		t.Fatalf("read received qty: %v", err)
	}
	if !receivedQty.IsZero() {
		t.Errorf("received quantity = %s after a rolled-back receipt, want zero", receivedQty)
	}
	if got := f.poStatus(t, po.ID); got != string(domain.StatusPending) {
		t.Errorf("order status = %s, want pending", got)
	}
}

// TestGoodsReceiptListingCarriesThePONumberAndItsLineCount covers the two
// columns the receiving screen could not fill.
//
// The listing sent po_id and nothing else about the order, so the screen's "PO
// Reference" column printed the first eight characters of a UUID - a string
// that matches nothing on the delivery note in the receiver's hand. And with no
// line count it showed "-" under Items, because fetching the lines of every
// receipt to count them is not something a listing does.
func TestGoodsReceiptListingCarriesThePONumberAndItsLineCount(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	rice := f.seedProduct(t, "Rice", 5)
	dal := f.seedProduct(t, "Dal", 5)

	po, _ := f.createOrder(t, vendorID,
		domain.LineInput{ProductID: rice, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00")},
		domain.LineInput{ProductID: dal, UnitQty: money.QuantityFromInt(4), Rate: money.MustFromString("90.00")},
	)
	if po.PONo == "" {
		t.Fatal("the order under test has no number")
	}

	created, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{
			{ProductID: rice, ReceivedQty: money.QuantityFromInt(10)},
			{ProductID: dal, ReceivedQty: money.QuantityFromInt(4)},
		},
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}

	// The receipt handed back from receiving already knows both figures, so a
	// client that just posted a delivery sees the same numbers the listing will
	// show it a moment later.
	if created.PONo != po.PONo {
		t.Errorf("po_no on the created receipt = %q, want %q", created.PONo, po.PONo)
	}
	if created.ItemCount != 2 {
		t.Errorf("item_count on the created receipt = %d, want 2", created.ItemCount)
	}

	receipts, err := f.svc.ListGoodsReceipts(ctx, f.storeID, nil, 0)
	if err != nil {
		t.Fatalf("list goods receipts: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("listed %d receipts, want 1", len(receipts))
	}
	if receipts[0].PONo != po.PONo {
		t.Errorf("po_no = %q, want %q — the screen shows this, not the id", receipts[0].PONo, po.PONo)
	}
	if !strings.HasPrefix(receipts[0].PONo, "PO/T1/") {
		t.Errorf("po_no %q does not carry the terminal series", receipts[0].PONo)
	}
	if receipts[0].ItemCount != 2 {
		t.Errorf("item_count = %d, want 2 — the receipt has a rice line and a dal line",
			receipts[0].ItemCount)
	}

	// Get-one shares the read model and must agree with the listing.
	one, items, err := f.svc.GetGoodsReceipt(ctx, created.ID, f.storeID)
	if err != nil {
		t.Fatalf("get goods receipt: %v", err)
	}
	if one.PONo != po.PONo || one.ItemCount != len(items) {
		t.Errorf("get-one disagrees with the listing: po_no %q, item_count %d, %d lines",
			one.PONo, one.ItemCount, len(items))
	}

	// Receipt lines name their product, as order lines do. Without this the
	// screen prints a truncated uuid in the Product column, which is unusable
	// for checking a delivery against what was ordered.
	byName := map[string]bool{}
	for _, it := range items {
		if it.ProductName == "" {
			t.Errorf("receipt line for product %s came back with no product_name", it.ProductID)
		}
		byName[it.ProductName] = true
	}
	if !byName["Rice"] || !byName["Dal"] {
		t.Errorf("receipt lines named %v, want both Rice and Dal", byName)
	}
}

// refusingStock refuses every movement.
type refusingStock struct{ err error }

func (r *refusingStock) ReceiveIntoStock(context.Context, platform.Tx, service.StockMovement) error {
	return r.err
}
func (r *refusingStock) RemoveFromStock(context.Context, platform.Tx, service.StockMovement) error {
	return r.err
}

// ---------------------------------------------------------------------------
// Buying in bulk
// ---------------------------------------------------------------------------

// sackOfRice is the buy side of the case this change exists for: stock arrives
// in sacks and is stocked, counted and sold by the kilo.
func (f *fixture) sackOfRice(t *testing.T) (productID, loose, sack uuid.UUID) {
	t.Helper()
	productID = f.seedProduct(t, "Rice", 5)
	return productID, f.defaultUnit(t, productID), f.seedUnit(t, productID, "25 kg sack", "25", false)
}

func TestAnOrderPlacedInSacksIsPricedInSacksAndStockedInKilos(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, _, sack := f.sackOfRice(t)

	po, items := f.createOrder(t, vendorID, domain.LineInput{
		ProductID:     rice,
		ProductUnitID: sack,
		UnitQty:       money.QuantityFromInt(10),
		Rate:          money.MustFromString("900.00"),
	})

	// Ten sacks at Rs.900 is Rs.9,000 exactly. A rate per kilo derived from the
	// sack — 36 here, and 333.333... for a Rs.1,000 three-kilo pack — is how a
	// line ends up a paisa out.
	if !items[0].Total.Equal(money.MustFromString("9450.00")) {
		t.Errorf("line total = %s, want 9450.00 (9000 plus 5%% GST)", items[0].Total)
	}
	if !po.SubTotal.Equal(money.MustFromString("9000.00")) {
		t.Errorf("sub_total = %s, want exactly 9000.00", po.SubTotal)
	}
	if !items[0].UnitQty.Equal(money.QuantityFromInt(10)) {
		t.Errorf("unit_qty = %s, want 10", items[0].UnitQty)
	}
	// purchase_items.quantity stays in base units, which is what a receipt is
	// booked against and what stock counts.
	if !items[0].Quantity.Equal(money.QuantityFromInt(250)) {
		t.Errorf("quantity = %s, want 250 — the order is 250 kg of rice", items[0].Quantity)
	}
	if items[0].UnitLabel != "25 kg sack" {
		t.Errorf("unit label = %q, want %q", items[0].UnitLabel, "25 kg sack")
	}

	// And it reads back that way, through the join the order screen renders.
	_, reread, err := f.svc.GetPurchaseOrder(context.Background(), po.ID, f.storeID)
	if err != nil {
		t.Fatalf("re-read order: %v", err)
	}
	if reread[0].ProductUnitID != sack {
		t.Errorf("product_unit_id = %s, want the sack %s", reread[0].ProductUnitID, sack)
	}
	if !reread[0].Quantity.Equal(money.QuantityFromInt(250)) {
		t.Errorf("stored quantity = %s, want 250", reread[0].Quantity)
	}
}

func TestADeliveryCountedInSacksBooksKilosIntoStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, _, sack := f.sackOfRice(t)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID:     rice,
		ProductUnitID: sack,
		UnitQty:       money.QuantityFromInt(10),
		Rate:          money.MustFromString("900.00"),
	})

	// The clerk counts ten sacks. Nothing in the request says 250.
	receipt, items, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   rice,
			ReceivedQty: money.QuantityFromInt(10),
			CostPrice:   money.MustFromString("900.00"),
		}},
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}
	if receipt.ID == uuid.Nil {
		t.Fatal("no receipt was written")
	}
	if !items[0].ReceivedQty.Equal(money.QuantityFromInt(250)) {
		t.Errorf("received_qty = %s, want 250 — the receipt is stored in base units", items[0].ReceivedQty)
	}
	if got := f.stock.receivedQty(rice); !got.Equal(money.QuantityFromInt(250)) {
		t.Errorf("stock received %s, want 250 kg from ten 25 kg sacks", got)
	}
	// The batch holds what the delivery cost, stated: ten sacks at Rs.900. The
	// per-kilo figure the reports want is derived from it — Rs.36 here — rather
	// than stored, because a pack size that does not divide evenly has no exact
	// per-kilo price to store.
	if got := f.stock.received[0].TotalCost; !got.Equal(money.MustFromString("9000.00")) {
		t.Errorf("landed cost = %s, want ten sacks at Rs.900, 9000.00", got)
	}

	// Ten sacks is the whole order, so it is fully received.
	reread, err := f.svc.ListPurchaseOrders(ctx, domain.POFilter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if reread[0].Status != domain.StatusReceived {
		t.Errorf("status = %s after the whole order arrived, want received", reread[0].Status)
	}
}

func TestAnOrderInSacksCanBeFilledByADeliveryCountedLoose(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, loose, sack := f.sackOfRice(t)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID:     rice,
		ProductUnitID: sack,
		UnitQty:       money.QuantityFromInt(10),
		Rate:          money.MustFromString("900.00"),
	})

	// The vendor sends 250 kg loose instead of ten sacks. Both are 250 kg of
	// rice, and the order is filled — which is only expressible because
	// allocation happens in base units.
	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:     rice,
			ProductUnitID: loose,
			ReceivedQty:   money.QuantityFromInt(250),
		}},
	}); err != nil {
		t.Fatalf("receive goods loose: %v", err)
	}
	if got := f.stock.receivedQty(rice); !got.Equal(money.QuantityFromInt(250)) {
		t.Errorf("stock received %s, want 250", got)
	}
	orders, err := f.svc.ListPurchaseOrders(ctx, domain.POFilter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if orders[0].Status != domain.StatusReceived {
		t.Errorf("status = %s, want received", orders[0].Status)
	}
}

func TestPartialRejectionOfSacksBooksOnlyTheAcceptedKilos(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, _, sack := f.sackOfRice(t)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: rice, ProductUnitID: sack,
		UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("900.00"),
	})

	// Ten sacks arrive, two of them split open. The invariant
	// accepted + rejected == received is checked in sacks and converted after.
	_, items, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID:   rice,
			ReceivedQty: money.QuantityFromInt(10),
			RejectedQty: money.QuantityFromInt(2),
			Reason:      "split in transit",
		}},
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}
	if !items[0].AcceptedQty.Equal(money.QuantityFromInt(200)) {
		t.Errorf("accepted = %s, want 200 kg from eight good sacks", items[0].AcceptedQty)
	}
	if !items[0].RejectedQty.Equal(money.QuantityFromInt(50)) {
		t.Errorf("rejected = %s, want 50 kg from two split sacks", items[0].RejectedQty)
	}
	if got := f.stock.receivedQty(rice); !got.Equal(money.QuantityFromInt(200)) {
		t.Errorf("stock received %s, want only the 200 kg accepted", got)
	}
}

func TestAnOrderNamingAnotherProductsUnitIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, _, _ := f.sackOfRice(t)
	cement := f.seedProduct(t, "Cement", 28)
	// A cement sack is 50 kg where a rice sack is 25. Pricing one as the other
	// is exactly the mistake a shared unit id would let through.
	cementSack := f.seedUnit(t, cement, "50 kg sack", "50", false)

	_, _, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID, OrderDate: orderDate(t),
		Lines: []domain.LineInput{{
			ProductID: rice, ProductUnitID: cementSack,
			UnitQty: money.QuantityFromInt(1), Rate: money.MustFromString("900.00"),
		}},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for another product's unit, got %v", err)
	}

	var orders int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM purchase_orders WHERE store_id = $1`, f.storeID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Errorf("a refused order left %d purchase orders behind", orders)
	}
}

func TestAUnitThatIsSoldButNotBoughtIsRefusedOnAnOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, _, _ := f.sackOfRice(t)

	// A 250 g pouch the shop weighs out itself and no supplier delivers.
	pouch := f.seedUnit(t, rice, "250 g pouch", "0.25", false)
	if _, err := f.db.ExecContext(context.Background(),
		`UPDATE product_units SET is_purchasable = false WHERE id = $1`, pouch); err != nil {
		t.Fatalf("mark the pouch unpurchasable: %v", err)
	}

	_, _, err := f.svc.CreatePurchaseOrder(context.Background(), service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID, OrderDate: orderDate(t),
		Lines: []domain.LineInput{{
			ProductID: rice, ProductUnitID: pouch,
			UnitQty: money.QuantityFromInt(4), Rate: money.MustFromString("12.00"),
		}},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for a unit that is not bought, got %v", err)
	}
	if !strings.Contains(err.Error(), "250 g pouch") {
		t.Errorf("error %q does not name the unit that cannot be bought", err)
	}
}

func TestABareQuantityOnAnOrderStillMeansTheDefaultUnit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, loose, _ := f.sackOfRice(t)

	// A buyer's client written before units existed sends a product and a
	// number, and that has to keep meaning the loose kilo.
	_, items := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: rice, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("40.00"),
	})
	if items[0].ProductUnitID != loose {
		t.Errorf("product_unit_id = %s, want the default unit %s", items[0].ProductUnitID, loose)
	}
	if !items[0].Quantity.Equal(money.QuantityFromInt(10)) {
		t.Errorf("quantity = %s, want 10 — a bare quantity is already in base units", items[0].Quantity)
	}
}

func TestTheRecordedCostOfTheOrderedUnitIsTheReceiptsFallback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Wholesaler", karnatakaVendor, "0")
	rice, loose, sack := f.sackOfRice(t)
	// Two costs for one product, which is the whole point of a price per unit:
	// a sack is not 25 times the loose kilo.
	f.seedUnitCost(t, rice, loose, "50.00", "40.0000", "45.00")
	f.seedUnitCost(t, rice, sack, "1100.00", "875.0000", "1050.00")

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: rice, ProductUnitID: sack,
		UnitQty: money.QuantityFromInt(4), Rate: money.MustFromString("900.00"),
	})

	// No cost stated on the receipt line, so the sack's recorded cost applies —
	// not the loose kilo's, and not whichever price row the plan found first.
	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: rice, ReceivedQty: money.QuantityFromInt(4)}},
	}); err != nil {
		t.Fatalf("receive goods: %v", err)
	}
	if got := f.stock.received[0].TotalCost; !got.Equal(money.MustFromString("3500.00")) {
		t.Errorf("landed cost = %s, want four sacks at the recorded Rs.875, 3500.00", got)
	}
}

// ---------------------------------------------------------------------------
// Purchase returns
// ---------------------------------------------------------------------------

func (f *fixture) createReturn(t *testing.T, vendorID, productID uuid.UUID, qty int64, rate string) domain.PurchaseReturn {
	t.Helper()
	pr, _, err := f.svc.CreatePurchaseReturn(context.Background(), service.CreatePurchaseReturnInput{
		VendorID: vendorID, StoreID: f.storeID, UserID: f.userID, ReturnDate: orderDate(t),
		Reason: "damaged in transit",
		Lines: []domain.ReturnLineInput{{
			ProductID: productID,
			Quantity:  money.QuantityFromInt(qty),
			Rate:      money.MustFromString(rate),
		}},
	})
	if err != nil {
		t.Fatalf("create purchase return: %v", err)
	}
	return pr
}

func TestApprovingAPurchaseReturnTwiceMovesStockOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)

	pr := f.createReturn(t, vendorID, productID, 5, "55.00")
	if !pr.Total.Equal(money.MustFromString("275.00")) {
		t.Errorf("return total = %s, want the server's 275.00", pr.Total)
	}

	approved, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, f.storeID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != domain.ReturnApproved {
		t.Errorf("status = %s, want approved", approved.Status)
	}
	if got := f.stock.removedQty(productID); !got.Equal(money.QuantityFromInt(5)) {
		t.Fatalf("%s units left stock on the first approval, want 5", got)
	}

	// The old code checked the status in Go and then wrote it unconditionally,
	// so a replayed approval succeeded and took the goods out of stock a second
	// time. The guard is now in the UPDATE.
	for i := range 3 {
		if _, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, f.storeID); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("replay %d: expected ErrInvalidTransition, got %v", i+1, err)
		}
	}
	if got := f.stock.removedQty(productID); !got.Equal(money.QuantityFromInt(5)) {
		t.Errorf("%s units left stock in total, want exactly the 5 returned", got)
	}
	if n := len(f.stock.removed); n != 1 {
		t.Errorf("stock was moved %d times, want once", n)
	}
}

func TestPurchaseReturnApprovalDrainsItsLinesBeforeMovingStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")

	// Several lines, so the approval path really does hold a multi-row result
	// while it writes. The implementation this replaces issued the stock UPDATE
	// inside for rows.Next() on an open cursor, which pgx rejects with "conn
	// busy" — this test fails outright rather than subtly if that returns.
	lines := make([]domain.ReturnLineInput, 0, 5)
	productIDs := make([]uuid.UUID, 0, 5)
	for i := range 5 {
		id := f.seedProduct(t, "Returned Item "+string(rune('A'+i)), 5)
		productIDs = append(productIDs, id)
		lines = append(lines, domain.ReturnLineInput{
			ProductID: id,
			Quantity:  money.QuantityFromInt(int64(i) + 1),
			Rate:      money.MustFromString("10.00"),
		})
	}

	pr, items, err := f.svc.CreatePurchaseReturn(ctx, service.CreatePurchaseReturnInput{
		VendorID: vendorID, StoreID: f.storeID, UserID: f.userID,
		ReturnDate: orderDate(t), Lines: lines,
	})
	if err != nil {
		t.Fatalf("create purchase return: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("%d lines written, want 5", len(items))
	}
	// 10 x (1+2+3+4+5) = 150.
	if !pr.Total.Equal(money.MustFromString("150.00")) {
		t.Errorf("return total = %s, want 150.00", pr.Total)
	}

	if _, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, f.storeID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	for i, id := range productIDs {
		if got := f.stock.removedQty(id); !got.Equal(money.QuantityFromInt(int64(i) + 1)) {
			t.Errorf("product %d: %s left stock, want %d", i+1, got, i+1)
		}
	}
}

func TestPurchaseReturnApprovalRollsBackWhenStockRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)
	pr := f.createReturn(t, vendorID, productID, 5, "55.00")

	// Not enough on hand: the stock aggregate refuses, and the approval must go
	// with it. An approved return whose goods never left is a return that can
	// never be approved again and never actually happened.
	f.stock.failRemoveFor[productID] = errors.New("insufficient stock")
	if _, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, f.storeID); err == nil {
		t.Fatal("expected approval to fail when stock refuses")
	}

	var status string
	if err := f.db.QueryRowContext(ctx,
		`SELECT status FROM purchase_returns WHERE id = $1`, pr.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(domain.ReturnPending) {
		t.Errorf("status = %s after a failed approval, want pending", status)
	}

	// Once stock can take it, the same return approves normally.
	delete(f.stock.failRemoveFor, productID)
	if _, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, f.storeID); err != nil {
		t.Errorf("retry after the stock problem cleared: %v", err)
	}
}

func TestPurchaseReturnIsScopedToItsStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Rice", 5)
	pr := f.createReturn(t, vendorID, productID, 1, "55.00")

	other := seedStore(t, f.db, "Branch", karnatakaGSTIN)
	if _, err := f.svc.ApprovePurchaseReturn(ctx, pr.ID, other); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected another store's return to be invisible, got %v", err)
	}
	if got := f.stock.removedQty(productID); !got.IsZero() {
		t.Errorf("%s units left stock via a cross-store approval", got)
	}
}

// ---------------------------------------------------------------------------
// Vendor payments
// ---------------------------------------------------------------------------

// TestVendorPaymentListingCarriesTheVendorName brings the payments listing into
// line with the purchase-return listing beside it.
//
// Purchase returns join vendors for the name; vendor payments did not, so the
// payments screen had only a vendor id to put in its Vendor column and rendered
// a raw UUID. The two screens sit next to each other and list documents about
// the same vendors, so one of them showing a name and the other a UUID is not a
// difference anybody chose.
func TestVendorPaymentListingCarriesTheVendorName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	balaji := f.seedVendor(t, "Sri Balaji Traders", karnatakaVendor, "0")
	other := f.seedVendor(t, "Krishna Wholesale", maharashtraVndr, "0")
	productID := f.seedProduct(t, "Rice", 5)

	for _, v := range []uuid.UUID{balaji, other} {
		if _, err := f.svc.RecordVendorPayment(ctx, domain.PaymentInput{
			VendorID: v, StoreID: f.storeID, UserID: f.userID,
			Amount: money.MustFromString("1000.00"), PaymentDate: orderDate(t), PaymentMode: "bank",
		}); err != nil {
			t.Fatalf("record payment: %v", err)
		}
	}

	payments, err := f.svc.ListVendorPayments(ctx, f.storeID, nil, 0)
	if err != nil {
		t.Fatalf("list vendor payments: %v", err)
	}
	if len(payments) != 2 {
		t.Fatalf("listed %d payments, want 2", len(payments))
	}
	names := make(map[uuid.UUID]string, len(payments))
	for _, p := range payments {
		if p.VendorName == "" {
			t.Errorf("payment %s carries no vendor_name, so the screen can only show the id %s", p.ID, p.VendorID)
		}
		names[p.VendorID] = p.VendorName
	}
	if names[balaji] != "Sri Balaji Traders" {
		t.Errorf("vendor_name = %q, want Sri Balaji Traders", names[balaji])
	}
	if names[other] != "Krishna Wholesale" {
		t.Errorf("vendor_name = %q, want Krishna Wholesale", names[other])
	}

	// Filtering by vendor still resolves the name, and still filters.
	filtered, err := f.svc.ListVendorPayments(ctx, f.storeID, &balaji, 0)
	if err != nil {
		t.Fatalf("list for one vendor: %v", err)
	}
	if len(filtered) != 1 || filtered[0].VendorName != "Sri Balaji Traders" {
		t.Errorf("filtered listing = %+v, want one payment to Sri Balaji Traders", filtered)
	}

	// The same field on the purchase-return listing, which is the precedent
	// this follows. If either stops filling it the two have drifted apart again.
	ret := f.createReturn(t, balaji, productID, 1, "55.00")
	returns, err := f.svc.ListPurchaseReturns(ctx, domain.ReturnFilter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list purchase returns: %v", err)
	}
	if len(returns) != 1 || returns[0].ID != ret.ID {
		t.Fatalf("listed %d returns, want the one just raised", len(returns))
	}
	if returns[0].VendorName != names[balaji] {
		t.Errorf("purchase returns say %q and vendor payments say %q for the same vendor",
			returns[0].VendorName, names[balaji])
	}
}

// ---------------------------------------------------------------------------
// Vendors
// ---------------------------------------------------------------------------

func TestVendorLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	v, err := f.svc.CreateVendor(ctx, domain.VendorInput{
		Name: "Sri Balaji Traders", Phone: "9880011223", GSTIN: strings.ToLower(karnatakaVendor),
		State: "Karnataka", CreditDays: 30, CreditLimit: money.MustFromString("50000.00"),
	})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	if v.GSTIN != karnatakaVendor {
		t.Errorf("GSTIN stored as %q, want it upper-cased to %q", v.GSTIN, karnatakaVendor)
	}

	updated, err := f.svc.UpdateVendor(ctx, v.ID, domain.VendorInput{
		Name: "Sri Balaji Traders & Co", Phone: "9880011224", GSTIN: karnatakaVendor,
		State: "Karnataka", CreditDays: 45, CreditLimit: money.MustFromString("75000.00"),
	})
	if err != nil {
		t.Fatalf("update vendor: %v", err)
	}
	got, exposure, err := f.svc.GetVendor(ctx, updated.ID)
	if err != nil {
		t.Fatalf("get vendor: %v", err)
	}
	if got.CreditDays != 45 || !got.CreditLimit.Equal(money.MustFromString("75000.00")) {
		t.Errorf("vendor after update = %+v", got)
	}
	if !exposure.Outstanding().IsZero() {
		t.Errorf("a new vendor is owed %s, want nothing", exposure.Outstanding())
	}

	// Search is case-insensitive; Postgres LIKE is not, and a literal port of
	// the SQLite query would silently return nothing here.
	for _, term := range []string{"balaji", "BALAJI", "Balaji"} {
		found, err := f.svc.ListVendors(ctx, domain.VendorFilter{Search: term})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if len(found) != 1 {
			t.Errorf("search %q returned %d vendors, want 1", term, len(found))
		}
	}

	if err := f.svc.SetVendorActive(ctx, v.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	active, err := f.svc.ListVendors(ctx, domain.VendorFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("a deactivated vendor is still listed: %d", len(active))
	}

	// An order on an inactive vendor is refused rather than raised against a
	// supplier the shop has stopped using.
	productID := f.seedProduct(t, "Rice", 5)
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: v.ID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected an inactive vendor to be refused, got %v", err)
	}

	if _, err := f.svc.UpdateVendor(ctx, uuid.New(), domain.VendorInput{Name: "Nobody"}); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound updating an unknown vendor, got %v", err)
	}
}

func TestUnknownOrInactiveProductIsRefusedOnAnOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")

	// The old code discarded the product lookup error and carried on with a GST
	// rate of 0, quietly under-taxing an order for a product that did not exist.
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(uuid.New(), "")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected an unknown product to be refused, got %v", err)
	}

	productID := f.seedProduct(t, "Discontinued", 5)
	if _, err := f.db.ExecContext(ctx, `UPDATE products SET is_active = false WHERE id = $1`, productID); err != nil {
		t.Fatalf("deactivate product: %v", err)
	}
	if _, _, err := f.svc.CreatePurchaseOrder(ctx, service.CreatePurchaseOrderInput{
		StoreID: f.storeID, VendorID: vendorID, TerminalID: f.terminalID,
		OrderDate: orderDate(t), Lines: []domain.LineInput{order55At5(productID, "")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected an inactive product to be refused, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pure domain arithmetic
// ---------------------------------------------------------------------------

func TestFinancialYearRunsAprilToMarch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ date, want string }{
		{"2026-04-01", "2026-27"},
		{"2026-06-15", "2026-27"},
		{"2027-03-31", "2026-27"},
		{"2027-04-01", "2027-28"},
		{"2026-01-15", "2025-26"},
		{"2099-12-31", "2099-00"},
	} {
		d, err := time.Parse("2006-01-02", tc.date)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.date, err)
		}
		if got := domain.FinancialYear(d); got != tc.want {
			t.Errorf("FinancialYear(%s) = %q, want %q", tc.date, got, tc.want)
		}
	}
}

func TestComputeLineRejectsADiscountLargerThanTheLine(t *testing.T) {
	t.Parallel()
	_, err := domain.ComputeLine(money.MustFromString("10.00"), money.QuantityFromInt(2),
		money.MustFromString("25.00"), money.RateFromInt(5), false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for a negative line amount, got %v", err)
	}
}

func TestExposureWithinLimitIsExact(t *testing.T) {
	t.Parallel()
	e := domain.Exposure{
		Ordered: money.MustFromString("900.00"),
		Paid:    money.MustFromString("0.00"),
	}
	limit := money.MustFromString("1000.00")

	// Landing exactly on the limit is inside it.
	if !e.WithinLimit(money.MustFromString("100.00"), limit) {
		t.Error("an order landing exactly on the limit should be allowed")
	}
	// One paisa past it is not. There is no epsilon here to absorb it.
	if e.WithinLimit(money.MustFromString("100.01"), limit) {
		t.Error("an order one paisa past the limit should be refused")
	}
	// A zero limit means no limit is recorded.
	if !e.WithinLimit(money.MustFromString("999999.00"), money.Zero()) {
		t.Error("a vendor with no recorded limit should not be capped")
	}
}

func TestAllocateReceiptSpreadsAcrossDuplicateLines(t *testing.T) {
	t.Parallel()
	productID := uuid.New()
	first, second := uuid.New(), uuid.New()
	items := []domain.PurchaseItem{
		{ID: first, ProductID: productID, Quantity: money.QuantityFromInt(10)},
		{ID: second, ProductID: productID, Quantity: money.QuantityFromInt(5)},
	}

	got, err := domain.AllocateReceipt(items, productID, money.QuantityFromInt(12))
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("allocated across %d lines, want 2", len(got))
	}
	if got[0].ItemID != first || !got[0].Qty.Equal(money.QuantityFromInt(10)) {
		t.Errorf("first allocation = %+v", got[0])
	}
	if got[1].ItemID != second || !got[1].Qty.Equal(money.QuantityFromInt(2)) {
		t.Errorf("second allocation = %+v", got[1])
	}

	if _, err := domain.AllocateReceipt(items, productID, money.QuantityFromInt(16)); !errors.Is(err, domain.ErrOverReceipt) {
		t.Errorf("expected ErrOverReceipt for 16 against 15 ordered, got %v", err)
	}
	if _, err := domain.AllocateReceipt(items, uuid.New(), money.QuantityFromInt(1)); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for a product not on the order, got %v", err)
	}
}

// TestADeliveryIsBookedInAnAgreedOrderSoItCannotDeadlockTheCounter checks the
// order a receipt touches the shelf in, rather than only what it puts there.
//
// A storekeeper booking in a van-load and a cashier billing the same goods are
// the two things this shop does at once, and both take row locks on the same
// stock rows. Whichever order the clerk keys the delivery lines in, the
// movements have to be applied in the one order every other multi-product write
// uses — ascending product id, stock.InLockOrder — or the receipt and the sale
// end up waiting on each other and Postgres kills one of them.
//
// The receipt's own lines still come back in the order they were stated: the
// paperwork reads as it was entered, only the shelf is touched in an agreed
// sequence.
func TestADeliveryIsBookedInAnAgreedOrderSoItCannotDeadlockTheCounter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")

	// Three products, ordered on the receipt in the reverse of their lock
	// order, so an implementation that follows the request cannot pass.
	products := []uuid.UUID{
		f.seedProduct(t, "Rice", 5),
		f.seedProduct(t, "Soap", 18),
		f.seedProduct(t, "Oil", 5),
	}
	lockOrder := stockdomain.InLockOrder(products)
	stated := make([]uuid.UUID, len(products))
	for i, idx := range lockOrder {
		stated[len(products)-1-i] = products[idx]
	}

	orderLines := make([]domain.LineInput, len(stated))
	receiptLines := make([]domain.ReceiptLineInput, len(stated))
	for i, p := range stated {
		orderLines[i] = domain.LineInput{
			ProductID: p, UnitQty: money.QuantityFromInt(10), Rate: money.MustFromString("55.00"),
		}
		receiptLines[i] = domain.ReceiptLineInput{ProductID: p, ReceivedQty: money.QuantityFromInt(10)}
	}

	po, _ := f.createOrder(t, vendorID, orderLines...)
	_, items, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: receiptLines,
	})
	if err != nil {
		t.Fatalf("receive goods: %v", err)
	}

	if len(f.stock.received) != len(products) {
		t.Fatalf("stock was touched %d times for %d lines", len(f.stock.received), len(products))
	}
	for i, idx := range lockOrder {
		if want := products[idx]; f.stock.received[i].ProductID != want {
			t.Errorf("stock movement %d was for %s, want %s — the delivery did not follow the lock order",
				i, f.stock.received[i].ProductID, want)
		}
	}

	// The clerk's own lines are unmoved.
	for i, want := range stated {
		if items[i].ProductID != want {
			t.Errorf("receipt line %d is for %s, want the %s the clerk keyed there",
				i, items[i].ProductID, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Expiry at the receiving bay
// ---------------------------------------------------------------------------

// perishable makes a product that expires, optionally with a shelf life.
func (f *fixture) perishable(t *testing.T, name string, shelfLifeDays *int) uuid.UUID {
	t.Helper()
	id := f.seedProduct(t, name, 5)
	if _, err := f.db.ExecContext(context.Background(),
		`UPDATE products SET tracks_expiry = true, shelf_life_days = $2 WHERE id = $1`,
		id, shelfLifeDays); err != nil {
		t.Fatalf("mark %s perishable: %v", name, err)
	}
	return id
}

// batchExpiry is the date on the last movement handed to the stock writer for
// this product. These tests stop at that boundary: purchasing's job is to
// decide the date and hand it over, and what the stock aggregate does with it
// is the stock aggregate's own tests.
func (f *fixture) batchExpiry(t *testing.T, productID uuid.UUID) *time.Time {
	t.Helper()
	f.stock.mu.Lock()
	defer f.stock.mu.Unlock()
	for i := len(f.stock.received) - 1; i >= 0; i-- {
		if f.stock.received[i].ProductID == productID {
			return f.stock.received[i].ExpiryDate
		}
	}
	t.Fatalf("no stock movement was recorded for %s", productID)
	return nil
}

// A product that expires needs a date on every lot, and receiving is where it
// has to be asked.
//
// It was not asked. Every batch that arrived through a purchase receipt carried
// no expiry date at all — the movement type had nowhere to put one — so the
// expiry report showed only the batches somebody had entered by hand and missed
// everything actually on the shelf. The product field that says a product
// expires existed the whole time and nothing read it.
func TestReceivingAPerishableNeedsADate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Dairy", karnatakaVendor, "0")
	productID := f.perishable(t, "Toned Milk 500 ml", nil)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(100),
		Rate:      money.MustFromString("30.00"),
	})

	_, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(100)}},
	})
	if err == nil {
		t.Fatal("a perishable was received with no expiry date")
	}
	if !strings.Contains(err.Error(), "Toned Milk 500 ml") {
		t.Errorf("message = %q, want it to name the product — a receipt has many lines", err)
	}
	if !strings.Contains(err.Error(), "expiry date") {
		t.Errorf("message = %q, want it to say what is missing", err)
	}

	// Nothing reached stock: the refusal has to take the whole receipt with it,
	// or the order advances against goods that never got to the shelf.
	if !f.stock.receivedQty(productID).IsZero() {
		t.Errorf("%s reached stock despite the refusal", f.stock.receivedQty(productID))
	}
	// And no receipt document was written either.
	var receipts int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM goods_receipts WHERE po_id = $1`, po.ID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 0 {
		t.Errorf("%d goods receipts were written despite the refusal", receipts)
	}

	// With a date it goes through, and the date reaches the batch.
	expiry := orderDate(t).AddDate(0, 0, 7)
	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID: productID, ReceivedQty: money.QuantityFromInt(100), ExpiryDate: &expiry,
		}},
	}); err != nil {
		t.Fatalf("receive with a date: %v", err)
	}
	got := f.batchExpiry(t, productID)
	if got == nil {
		t.Fatal("the batch has no expiry date")
	}
	if !got.Equal(expiry) {
		t.Errorf("batch expiry = %s, want %s", got.Format("2006-01-02"), expiry.Format("2006-01-02"))
	}
}

// The shelf life is what turns a packing date into an expiry, so a clerk keys
// what the packet says rather than doing the arithmetic.
func TestAShelfLifeCountsForwardFromTheManufactureDate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Biscuit Co", karnatakaVendor, "0")
	shelfLife := 270
	productID := f.perishable(t, "Parle-G 200g", &shelfLife)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(48),
		Rate:      money.MustFromString("24.50"),
	})

	made := orderDate(t).AddDate(0, 0, -30)
	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{
			ProductID: productID, ReceivedQty: money.QuantityFromInt(48), MfgDate: &made,
		}},
	}); err != nil {
		t.Fatalf("receive with a manufacture date: %v", err)
	}

	got := f.batchExpiry(t, productID)
	want := made.AddDate(0, 0, shelfLife)
	if got == nil || !got.Equal(want) {
		t.Errorf("batch expiry = %v, want %s counted from the packing date",
			got, want.Format("2006-01-02"))
	}
}

// A product that does not expire needs nothing, and receiving one must not
// start asking.
func TestReceivingSomethingThatDoesNotExpireNeedsNoDate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	vendorID := f.seedVendor(t, "Supplier", karnatakaVendor, "0")
	productID := f.seedProduct(t, "Steel Bucket", 18)

	po, _ := f.createOrder(t, vendorID, domain.LineInput{
		ProductID: productID,
		UnitQty:   money.QuantityFromInt(10),
		Rate:      money.MustFromString("180.00"),
	})

	if _, _, err := f.svc.ReceiveGoods(ctx, service.ReceiveGoodsInput{
		POID: po.ID, StoreID: f.storeID, UserID: f.userID, ReceivedDate: orderDate(t),
		Lines: []domain.ReceiptLineInput{{ProductID: productID, ReceivedQty: money.QuantityFromInt(10)}},
	}); err != nil {
		t.Fatalf("receive a product that does not expire: %v", err)
	}
	if got := f.batchExpiry(t, productID); got != nil {
		t.Errorf("batch expiry = %s, want none", got.Format("2006-01-02"))
	}
}
