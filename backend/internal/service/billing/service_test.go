package billing_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gbos/internal/app"
	domain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/billing"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// till is a shop set up to sell: a store, a counter, a cashier, and products
// with stock on the shelf.
type till struct {
	t          *testing.T
	db         *platform.DB
	svc        *service.Service
	svcs       app.Services
	storeID    uuid.UUID
	terminalID uuid.UUID
	userID     uuid.UUID
	uomID      uuid.UUID
	categoryID uuid.UUID
}

func newTill(t *testing.T) *till {
	t.Helper()
	db := testsupport.OpenDB(t)

	svcs, err := app.Build(db, app.Config{JWTSecret: "billing-tests-signing-secret-value-x"})
	if err != nil {
		t.Fatalf("build services: %v", err)
	}

	x := &till{t: t, db: db, svc: svcs.Billing, svcs: svcs}
	mustScan(t, db, `INSERT INTO stores (name, gstin) VALUES ('Till Shop', '29ABCDE1234F1Z5') RETURNING id`, &x.storeID)
	mustScan(t, db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`, &x.terminalID, x.storeID)
	mustScan(t, db, `INSERT INTO users (username, password_hash, role, store_id)
		VALUES ('till-cashier', 'x', 'cashier', $1) RETURNING id`, &x.userID, x.storeID)
	// The units migration seeds the ordinary Indian retail vocabulary, so the
	// kilogram is already on file with its family and conversion factor. A test
	// that inserted its own would collide on the unique code, and one that
	// invented a KG with no family would be a unit nothing can convert.
	mustScan(t, db, `SELECT id FROM units_of_measure WHERE code = 'KG'`, &x.uomID)
	mustScan(t, db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &x.categoryID)
	return x
}

func mustScan(t *testing.T, db *platform.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

// stockProduct creates a product sold loose by its stocking unit, priced at
// sell, with onHand units in stock at cost.
//
// One default unit worth one of the stocking unit is what every product had
// before bulk selling existed, so this is the fixture for every case that is
// not about units.
func (x *till) stockProduct(name, sell, cost, onHand string, gstPercent int64, allowDecimal bool) uuid.UUID {
	x.t.Helper()
	productID := x.product(name, gstPercent, allowDecimal)
	x.sellingUnit(productID, "Loose", "1", sell, true)
	x.stockOnHand(productID, onHand, cost)
	return productID
}

// product creates a product with no units and no price. Units and prices are
// separate fixtures because a product now has several of each.
func (x *till) product(name string, gstPercent int64, allowDecimal bool) uuid.UUID {
	x.t.Helper()
	var productID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO products (name, category_id, uom_id, gst_rate, allow_decimal)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, &productID,
		name, x.categoryID, x.uomID, gstPercent, allowDecimal)
	return productID
}

// sellingUnit adds a way the product may be sold, and this store's price for
// one of them.
//
// qtyInBase is how many of the product's stocking unit one of these amounts to:
// "1" for the loose kilo, "25" for a sack, "0.25" for a 250 g pouch. A blank
// sell price leaves the unit unpriced, which is the case a sale must refuse by
// name rather than bill at nothing.
func (x *till) sellingUnit(productID uuid.UUID, label, qtyInBase, sell string, isDefault bool) uuid.UUID {
	x.t.Helper()
	var unitID uuid.UUID
	mustScan(x.t, x.db,
		`INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`, &unitID,
		productID, x.uomID, label, money.MustQuantityFromString(qtyInBase), isDefault)
	if sell == "" {
		return unitID
	}
	// MRP is set to the selling price: the domain refuses a price above the
	// MRP, and these fixtures are not about that rule.
	if _, err := x.db.ExecContext(context.Background(),
		`INSERT INTO product_prices (product_id, store_id, product_unit_id, mrp, cost_price, sell_price)
		 VALUES ($1, $2, $3, $4, 0, $4)`, productID, x.storeID, unitID,
		money.MustFromString(sell)); err != nil {
		x.t.Fatalf("seed price for %s: %v", label, err)
	}
	return unitID
}

// stockOnHand puts an opening quantity on the shelf, in the product's base
// stocking unit, at a known cost.
func (x *till) stockOnHand(productID uuid.UUID, onHand, cost string) {
	x.t.Helper()
	if _, err := x.db.ExecContext(context.Background(),
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
		 VALUES ($1, $2, 'OPENING', $3, round($3::numeric * $4::numeric, 2))`,
		productID, x.storeID,
		money.MustQuantityFromString(onHand), money.MustFromString(cost)); err != nil {
		x.t.Fatalf("seed stock: %v", err)
	}
	// The cost price the store records for the product, which the catalogue
	// carries per unit. It is set against the default unit so the figures a
	// report reads for a loose kilo are the ones this test seeded.
	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE product_prices SET cost_price = $3
		  WHERE product_id = $1 AND store_id = $2
		    AND product_unit_id = (SELECT id FROM product_units WHERE product_id = $1 AND is_default)`,
		productID, x.storeID, money.MustFromString(cost)); err != nil {
		x.t.Fatalf("seed cost price: %v", err)
	}
}

func (x *till) customer(name string, creditLimit string, wholesale bool) uuid.UUID {
	x.t.Helper()
	var id uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO customers (name, store_id, credit_limit, is_wholesale)
		VALUES ($1, $2, $3, $4) RETURNING id`, &id, name, x.storeID, money.MustFromString(creditLimit), wholesale)
	return id
}

func (x *till) sale(in service.CreateInput) (service.Result, error) {
	if in.StoreID == uuid.Nil {
		in.StoreID = x.storeID
	}
	if in.UserID == uuid.Nil {
		in.UserID = x.userID
	}
	if in.TerminalID == uuid.Nil {
		in.TerminalID = x.terminalID
	}
	if in.PaymentMode == "" {
		in.PaymentMode = domain.PaymentCash
	}
	return x.svc.Create(context.Background(), in)
}

func (x *till) onHand(productID uuid.UUID) money.Quantity {
	x.t.Helper()
	var q money.Quantity
	mustScan(x.t, x.db, `SELECT COALESCE(SUM(quantity), 0) FROM stock WHERE product_id = $1 AND store_id = $2`,
		&q, productID, x.storeID)
	return q
}

// line names no unit, which means the product's default one — what a bare
// quantity has always meant and what a till written before units sends.
func line(productID uuid.UUID, qty string) domain.LineInput {
	return domain.LineInput{ProductID: productID, UnitQty: money.MustQuantityFromString(qty)}
}

// unitLine asks for qty of a particular unit: two sacks, or one 250 g pouch.
func unitLine(productID, unitID uuid.UUID, qty string) domain.LineInput {
	return domain.LineInput{
		ProductID:     productID,
		ProductUnitID: unitID,
		UnitQty:       money.MustQuantityFromString(qty),
	}
}

// --- the headline fix --------------------------------------------------------

func TestTheServerPricesTheSaleNotTheClient(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)

	// A client that could name its own price could post a Rs.1,125 sale as
	// Rs.1. Here the caller asserts a stale price and the sale is refused
	// rather than honoured.
	stale := money.MustFromString("1.00")
	l := line(rice, "2.5")
	l.Rate = &stale
	if _, err := x.sale(service.CreateInput{Lines: []domain.LineInput{l}}); !errors.Is(err, service.ErrPriceChanged) {
		t.Fatalf("expected ErrPriceChanged for a stale price, got %v", err)
	}

	// With no rate asserted, the server charges its own price.
	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "2.5")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if !got.Invoice.SubTotal.Equal(money.MustFromString("1125.00")) {
		t.Errorf("sub_total = %s, want 1125.00", got.Invoice.SubTotal)
	}
	if !got.Invoice.Total.Equal(money.MustFromString("1181.25")) {
		t.Errorf("total = %s, want 1181.25", got.Invoice.Total)
	}
	if !got.Items[0].UnitCostAtSale.Equal(money.MustFromString("337.50")) {
		t.Errorf("unit cost snapshot = %s, want 337.50", got.Items[0].UnitCostAtSale)
	}
}

func TestSaleDeductsStockAndSnapshotsCost(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "450.00", "337.50", "10", 5, true)

	if _, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "2.5")}}); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("7.5")) {
		t.Errorf("on hand = %s, want 7.5", got)
	}
}

func TestSellingMoreThanIsOnTheShelfRollsEverythingBack(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "450.00", "337.50", "3", 5, true)

	_, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "5")}})
	if !errors.Is(err, stockdomain.ErrInsufficientStock) {
		t.Fatalf("expected ErrInsufficientStock, got %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("3")) {
		t.Errorf("on hand = %s after a refused sale, want the original 3", got)
	}
	var invoices int
	mustScan(t, x.db, `SELECT count(*) FROM invoices WHERE store_id = $1`, &invoices, x.storeID)
	if invoices != 0 {
		t.Errorf("a refused sale left %d invoices behind", invoices)
	}
}

// --- units -------------------------------------------------------------------

// riceCounter is the shop this change exists for: one sack of rice, sold three
// ways. Bulk is cheaper per kilo, which one price per product could not express
// at all — a 25 kg sack was forced to cost exactly 25 times the loose kilo.
type riceCounter struct {
	*till
	rice  uuid.UUID
	loose uuid.UUID
	sack  uuid.UUID
	pouch uuid.UUID
}

func newRiceCounter(t *testing.T) *riceCounter {
	t.Helper()
	x := newTill(t)
	rice := x.product("Rice", 5, true)
	return &riceCounter{
		till:  x,
		rice:  rice,
		loose: x.sellingUnit(rice, "Loose (kg)", "1", "45.00", true),
		// Rs.1,050 for 25 kg is Rs.42 a kilo against Rs.45 loose. That gap is
		// the whole trade.
		sack:  x.sellingUnit(rice, "25 kg sack", "25", "1050.00", false),
		pouch: x.sellingUnit(rice, "250 g pouch", "0.25", "12.00", false),
	}
}

func TestABulkSaleIsPricedPerSackAndLeavesTheShelfInKilos(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, x.sack, "2")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}

	// Exactly 2,100. The arithmetic that fails here is the one that derives a
	// rate per kilo and multiplies the 50 kg back up.
	if !got.Invoice.SubTotal.Equal(money.MustFromString("2100.00")) {
		t.Errorf("sub_total = %s, want exactly 2100.00", got.Invoice.SubTotal)
	}
	if !got.Invoice.Total.Equal(money.MustFromString("2205.00")) {
		t.Errorf("total = %s, want 2205.00 (2100 plus 5%% GST)", got.Invoice.Total)
	}
	if !got.Items[0].Rate.Equal(money.MustFromString("1050.00")) {
		t.Errorf("rate = %s, want the sack's own 1050.00", got.Items[0].Rate)
	}
	if !got.Items[0].UnitQty.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("unit_qty = %s, want 2", got.Items[0].UnitQty)
	}
	if !got.Items[0].Quantity.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("quantity = %s, want 50 — stock is kept in the base unit", got.Items[0].Quantity)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after selling two sacks, want 50", got)
	}
}

func TestAPouchOfTheSameRiceIsPricedFromTheLooseRate(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, x.pouch, "1")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if !got.Invoice.SubTotal.Equal(money.MustFromString("12.00")) {
		t.Errorf("sub_total = %s, want 12.00", got.Invoice.SubTotal)
	}
	if !got.Items[0].Quantity.Equal(money.MustQuantityFromString("0.25")) {
		t.Errorf("quantity = %s, want 0.25", got.Items[0].Quantity)
	}
	// One stock pool: the pouch comes out of the same rice the sacks do, so the
	// shop can never be out of loose rice while sacks sit on the shelf.
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("99.75")) {
		t.Errorf("on hand = %s after a 250 g sale, want 99.75", got)
	}
}

func TestASackAndALooseKiloOnOneBillEachPriceFromTheirOwnUnit(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{
		unitLine(x.rice, x.sack, "2"),
		unitLine(x.rice, x.loose, "3"),
	}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}

	// 2,100 for the sacks and 135 for the loose. If the lines shared a price —
	// which is what one price per product means — one of them is wrong.
	if !got.Items[0].Rate.Equal(money.MustFromString("1050.00")) {
		t.Errorf("sack line rate = %s, want 1050.00", got.Items[0].Rate)
	}
	if !got.Items[1].Rate.Equal(money.MustFromString("45.00")) {
		t.Errorf("loose line rate = %s, want 45.00", got.Items[1].Rate)
	}
	if !got.Invoice.SubTotal.Equal(money.MustFromString("2235.00")) {
		t.Errorf("sub_total = %s, want 2235.00", got.Invoice.SubTotal)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("47")) {
		t.Errorf("on hand = %s, want 47 — 50 kg of sacks and 3 kg loose", got)
	}
}

func TestTheBillPrintsWhatWasAskedForRatherThanTheStockFigure(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	ctx := context.Background()
	x.stockOnHand(x.rice, "100", "33.75")

	sold, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, x.sack, "2")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}

	// Read back rather than trust what was returned: the columns and the join
	// are what a printed bill and a reprint a year later go through.
	got, err := x.svc.Get(ctx, x.storeID, sold.Invoice.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	it := got.Items[0]
	if it.ProductUnitID != x.sack {
		t.Errorf("product_unit_id = %s, want the sack %s", it.ProductUnitID, x.sack)
	}
	if it.UnitLabel != "25 kg sack" {
		t.Errorf("unit label = %q, want %q", it.UnitLabel, "25 kg sack")
	}
	// The line names the goods. Without this a bill prints a uuid where the
	// product should be, which is no use to the customer holding it or to
	// anyone answering a query about it a year later — and the screen was
	// already written expecting the field.
	if it.ProductName == "" {
		t.Error("the invoice line came back with no product_name")
	}
	// "2 x 25 kg sack", not "50".
	if !it.UnitQty.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("unit_qty = %s, want 2", it.UnitQty)
	}
	if !it.Quantity.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("quantity = %s, want 50", it.Quantity)
	}
}

func TestALineNamingAnotherProductsUnitIsRefused(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	soap := x.product("Soap", 18, false)
	soapBox := x.sellingUnit(soap, "Box of 12", "12", "600.00", true)
	x.stockOnHand(soap, "100", "45.00")

	// Two unit ids are indistinguishable to a caller that mixed them up, so
	// this has to fail rather than quietly price a box of soap as rice.
	_, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, soapBox, "1")}})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for another product's unit, got %v", err)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("rice on hand = %s after a refused sale, want 100", got)
	}
	if got := x.onHand(soap); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("soap on hand = %s after a refused sale, want 100", got)
	}
}

func TestAnUnpricedUnitIsRefusedRatherThanSoldForNothing(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	// A unit somebody defined and nobody priced. Left to a COALESCE it bills at
	// zero, and a shop gives away a 50 kg bale without a single error.
	bale := x.sellingUnit(x.rice, "50 kg bale", "50", "", false)

	_, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, bale, "1")}})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for an unpriced unit, got %v", err)
	}
	// The message has to name the unit: "no price for Rice" would send a
	// shopkeeper to the product screen, which is priced perfectly well.
	if !strings.Contains(err.Error(), "50 kg bale") {
		t.Errorf("error %q does not name the unit that is missing a price", err)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("on hand = %s after a refused sale, want 100", got)
	}
}

func TestAUnitThatIsBoughtButNotSoldIsRefused(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	// A supplier's pallet: the shop buys by it and never puts one on the
	// counter.
	pallet := x.sellingUnit(x.rice, "Pallet", "500", "19000.00", false)
	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE product_units SET is_sellable = false WHERE id = $1`, pallet); err != nil {
		t.Fatalf("mark the pallet unsellable: %v", err)
	}

	if _, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{unitLine(x.rice, pallet, "1")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for a unit that is not sold, got %v", err)
	}
}

func TestABareQuantityStillMeansTheDefaultUnit(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	x.stockOnHand(x.rice, "100", "33.75")

	// A till written before units existed sends a product and a number. That
	// has to keep meaning the loose kilo, which is what the migration
	// backfilled every existing line to.
	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(x.rice, "2")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if got.Items[0].ProductUnitID != x.loose {
		t.Errorf("product_unit_id = %s, want the default unit %s", got.Items[0].ProductUnitID, x.loose)
	}
	if !got.Invoice.SubTotal.Equal(money.MustFromString("90.00")) {
		t.Errorf("sub_total = %s, want 90.00 at the loose rate", got.Invoice.SubTotal)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("98")) {
		t.Errorf("on hand = %s, want 98", got)
	}
}

func TestVoidingABulkSaleRestoresTheBaseQuantityExactlyOnce(t *testing.T) {
	t.Parallel()
	x := newRiceCounter(t)
	ctx := context.Background()
	x.stockOnHand(x.rice, "100", "33.75")

	sold, err := x.sale(service.CreateInput{Lines: []domain.LineInput{unitLine(x.rice, x.sack, "2")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Fatalf("on hand = %s after selling two sacks, want 50", got)
	}

	// A void restores what left the shelf, which is 50 kg and not 2. This is
	// the path where a units bug destroys stock silently: put back the unit
	// quantity and 48 kg of rice vanishes from the books.
	if err := x.svc.Void(ctx, x.storeID, sold.Invoice.ID); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("on hand = %s after voiding two sacks, want the original 100", got)
	}
	if err := x.svc.Void(ctx, x.storeID, sold.Invoice.ID); !errors.Is(err, service.ErrAlreadyVoided) {
		t.Fatalf("second void: expected ErrAlreadyVoided, got %v", err)
	}
	if got := x.onHand(x.rice); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("on hand = %s after voiding twice, want 100", got)
	}
}

// --- numbering ---------------------------------------------------------------

func TestInvoiceNumbersAreConsecutivePerTerminal(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "100", 5, false)

	finYear := financialYearOf(time.Now())
	for i := 1; i <= 3; i++ {
		got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "1")}})
		if err != nil {
			t.Fatalf("sale %d: %v", i, err)
		}
		want := fmt.Sprintf("INV/T1/%s/%05d", finYear, i)
		if got.Invoice.InvoiceNo != want {
			t.Errorf("invoice %d numbered %q, want %q", i, got.Invoice.InvoiceNo, want)
		}
	}
}

func TestTwoTerminalsRunSeparateSeries(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "100", 5, false)

	var second uuid.UUID
	mustScan(t, x.db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T2', 'Counter 2') RETURNING id`,
		&second, x.storeID)

	first, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "1")}})
	if err != nil {
		t.Fatalf("counter 1 sale: %v", err)
	}
	other, err := x.sale(service.CreateInput{TerminalID: second, Lines: []domain.LineInput{line(rice, "1")}})
	if err != nil {
		t.Fatalf("counter 2 sale: %v", err)
	}

	finYear := financialYearOf(time.Now())
	if want := fmt.Sprintf("INV/T1/%s/00001", finYear); first.Invoice.InvoiceNo != want {
		t.Errorf("counter 1 issued %q, want %q", first.Invoice.InvoiceNo, want)
	}
	// Each counter owns its own series, which is what CGST Rule 46(b) permits
	// and what lets a till keep billing while offline.
	if want := fmt.Sprintf("INV/T2/%s/00001", finYear); other.Invoice.InvoiceNo != want {
		t.Errorf("counter 2 issued %q, want %q", other.Invoice.InvoiceNo, want)
	}
}

func TestConcurrentSalesDoNotCollideOrSkipNumbers(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "500", 5, false)

	const sales = 12
	var wg sync.WaitGroup
	errs := make([]error, sales)
	numbers := make([]string, sales)
	for i := 0; i < sales; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "1")}})
			errs[i] = err
			if err == nil {
				numbers[i] = got.Invoice.InvoiceNo
			}
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent sale %d failed: %v", i, err)
		}
		if seen[numbers[i]] {
			t.Errorf("invoice number %q was issued twice", numbers[i])
		}
		seen[numbers[i]] = true
	}
	if len(seen) != sales {
		t.Errorf("%d sales produced %d distinct numbers", sales, len(seen))
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("488")) {
		t.Errorf("on hand = %s after %d sales of 1, want 488", got, sales)
	}
}

// --- idempotency -------------------------------------------------------------

func TestTheSameRequestTwiceRaisesOneSale(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	key := uuid.New().String()
	in := service.CreateInput{Lines: []domain.LineInput{line(rice, "2")}, IdempotencyKey: key}

	first, err := x.sale(in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := x.sale(in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	// A double-clicked Checkout used to produce two invoices and deduct the
	// stock twice.
	if first.Invoice.ID != second.Invoice.ID {
		t.Errorf("replay raised a second sale: %s then %s", first.Invoice.ID, second.Invoice.ID)
	}
	if !second.Replayed {
		t.Error("the replayed response should say so")
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("48")) {
		t.Errorf("on hand = %s, want 48 — the stock moved twice", got)
	}
	var invoices int
	mustScan(t, x.db, `SELECT count(*) FROM invoices WHERE store_id = $1`, &invoices, x.storeID)
	if invoices != 1 {
		t.Errorf("%d invoices exist, want 1", invoices)
	}
}

// --- credit ------------------------------------------------------------------

func TestCreditSaleRecordsAReceivable(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	customer := x.customer("Corner Cafe", "10000.00", false)

	got, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "10")},
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	})
	if err != nil {
		t.Fatalf("credit sale: %v", err)
	}

	outstanding, err := x.svcs.Credit.Outstanding(ctx, x.storeID, customer)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	// The old code discarded the error from this insert, so a credit sale
	// could commit with the goods gone and nothing owed.
	if !outstanding.Equal(got.Invoice.Total) {
		t.Errorf("outstanding = %s, want the invoice total %s", outstanding, got.Invoice.Total)
	}
}

func TestCreditLimitBlocksTheSaleBeforeStockMoves(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	customer := x.customer("Overreacher", "500.00", false)

	_, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "10")}, // 1000 + tax, over the 500 limit
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	})
	if err == nil {
		t.Fatal("expected the credit limit to block this sale")
	}
	// The check ran before any goods moved, and the transaction rolled back
	// regardless. The old check read the outstanding balance with a discarded
	// error, so a failed read left it at zero and the limit always passed.
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after a blocked sale, want the original 50", got)
	}
}

// --- void --------------------------------------------------------------------

func TestVoidRestoresStockAndCancelsTheReceivable(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	customer := x.customer("Regular", "10000.00", false)

	sale, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "10")},
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}

	if err := x.svc.Void(ctx, x.storeID, sale.Invoice.ID); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after voiding, want the original 50", got)
	}
	outstanding, err := x.svcs.Credit.Outstanding(ctx, x.storeID, customer)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.IsZero() {
		t.Errorf("outstanding = %s after voiding the sale, want 0", outstanding)
	}
}

func TestVoidingTwiceRestoresStockOnce(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	sale, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "10")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if err := x.svc.Void(ctx, x.storeID, sale.Invoice.ID); err != nil {
		t.Fatalf("first void: %v", err)
	}

	// The old code read the status outside the transaction and updated without
	// a guard, so two clicks on Void both passed and the stock came back
	// twice — inventing ten units of rice.
	if err := x.svc.Void(ctx, x.storeID, sale.Invoice.ID); !errors.Is(err, service.ErrAlreadyVoided) {
		t.Fatalf("second void: expected ErrAlreadyVoided, got %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after voiding twice, want 50", got)
	}
}

func TestConcurrentVoidsRestoreStockOnce(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	sale, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "10")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = x.svc.Void(ctx, x.storeID, sale.Invoice.ID)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, service.ErrAlreadyVoided) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d concurrent voids succeeded, want exactly 1", succeeded)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after concurrent voids, want 50", got)
	}
}

// --- GST return --------------------------------------------------------------

func TestVoidedInvoicesAreNotFiledToGSTR1(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "100", 5, false)

	kept, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "5")}})
	if err != nil {
		t.Fatalf("first sale: %v", err)
	}
	cancelled, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "5")}})
	if err != nil {
		t.Fatalf("second sale: %v", err)
	}
	if err := x.svc.Void(ctx, x.storeID, cancelled.Invoice.ID); err != nil {
		t.Fatalf("void: %v", err)
	}

	month := kept.Invoice.InvoiceDate.Format("2006-01")
	rows, err := x.svc.GSTR1(ctx, x.storeID, month)
	if err != nil {
		t.Fatalf("gstr1: %v", err)
	}
	// Filing a cancelled sale overstates the liability the business declares
	// to the government. The old export had no status filter at all.
	if len(rows) != 1 {
		t.Fatalf("GSTR-1 has %d rows, want 1 — the voided sale was included", len(rows))
	}
	if rows[0].InvoiceNo != kept.Invoice.InvoiceNo {
		t.Errorf("filed %q, want %q", rows[0].InvoiceNo, kept.Invoice.InvoiceNo)
	}

	marked, err := x.svc.MarkGSTR1Filed(ctx, x.storeID, month)
	if err != nil {
		t.Fatalf("mark filed: %v", err)
	}
	if marked != 1 {
		t.Errorf("marked %d invoices filed, want 1", marked)
	}
	again, err := x.svc.GSTR1(ctx, x.storeID, month)
	if err != nil {
		t.Fatalf("gstr1 after filing: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("%d rows remain unfiled after filing", len(again))
	}
}

// --- reconciliation ----------------------------------------------------------

func TestADayOfTradingReconciles(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)
	soap := x.stockProduct("Soap", "60.00", "45.00", "100", 18, false)
	customer := x.customer("Credit Customer", "50000.00", false)

	type sale struct {
		lines    []domain.LineInput
		mode     domain.PaymentMode
		customer *uuid.UUID
		discount string
	}
	day := []sale{
		{lines: []domain.LineInput{line(rice, "2.5")}, mode: domain.PaymentCash},
		{lines: []domain.LineInput{line(soap, "3")}, mode: domain.PaymentUPI},
		{lines: []domain.LineInput{line(rice, "1"), line(soap, "2")}, mode: domain.PaymentCredit, customer: &customer},
		{lines: []domain.LineInput{line(rice, "4")}, mode: domain.PaymentCash, discount: "100.00"},
	}

	expectedRevenue := money.Zero()
	expectedTax := money.Zero()
	for i, s := range day {
		in := service.CreateInput{Lines: s.lines, PaymentMode: s.mode, CustomerID: s.customer}
		if s.discount != "" {
			in.BillDiscount = money.MustFromString(s.discount)
		}
		got, err := x.sale(in)
		if err != nil {
			t.Fatalf("sale %d: %v", i+1, err)
		}
		expectedRevenue = expectedRevenue.Add(got.Invoice.Total)
		expectedTax = expectedTax.Add(got.Invoice.CGST).Add(got.Invoice.SGST).Add(got.Invoice.IGST)

		// Every invoice must add up on its own.
		want := got.Invoice.SubTotal.
			Add(got.Invoice.CGST).Add(got.Invoice.SGST).Add(got.Invoice.IGST).
			Add(got.Invoice.RoundOff)
		if !got.Invoice.Total.Equal(want) {
			t.Errorf("sale %d: total %s but parts sum to %s", i+1, got.Invoice.Total, want)
		}
	}

	// And the day must add up in the database, to the paisa.
	var revenue, tax money.Money
	mustScan(t, x.db, `SELECT COALESCE(SUM(total), 0) FROM invoices WHERE store_id = $1 AND status = 'completed'`,
		&revenue, x.storeID)
	mustScan(t, x.db, `SELECT COALESCE(SUM(cgst + sgst + igst), 0) FROM invoices WHERE store_id = $1 AND status = 'completed'`,
		&tax, x.storeID)

	if !revenue.Equal(expectedRevenue) {
		t.Errorf("stored revenue %s, computed %s", revenue, expectedRevenue)
	}
	if !tax.Equal(expectedTax) {
		t.Errorf("stored tax %s, computed %s", tax, expectedTax)
	}

	// Stock moved by exactly what was sold.
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("92.5")) {
		t.Errorf("rice on hand = %s, want 92.5", got)
	}
	if got := x.onHand(soap); !got.Equal(money.MustQuantityFromString("95")) {
		t.Errorf("soap on hand = %s, want 95", got)
	}

	outstanding, err := x.svcs.Credit.Outstanding(ctx, x.storeID, customer)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if outstanding.IsZero() {
		t.Error("the credit sale left nothing outstanding")
	}
}

func TestFractionsAreRefusedForCountedGoods(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	soap := x.stockProduct("Soap", "60.00", "45.00", "100", 18, false)

	// Half a bar of soap is not a thing a shop can sell, and the unit of
	// measure says so.
	if _, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(soap, "1.5")}}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for a fractional count, got %v", err)
	}
}

func TestSaleRequiresAKnownTerminal(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "10", 5, false)

	if _, err := x.sale(service.CreateInput{
		TerminalID: uuid.New(),
		Lines:      []domain.LineInput{line(rice, "1")},
	}); err == nil {
		t.Error("a sale from an unregistered terminal should be refused")
	}
}

func TestAnotherStoresTerminalCannotBeBorrowed(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "10", 5, false)

	// A second shop with its own counter, coded T9.
	var otherStore, otherTerminal uuid.UUID
	mustScan(t, x.db, `INSERT INTO stores (name) VALUES ('Other Shop') RETURNING id`, &otherStore)
	mustScan(t, x.db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T9', 'Their Counter') RETURNING id`,
		&otherTerminal, otherStore)

	// Borrowing it would open a counter row under this store keyed to their
	// terminal and stamp T9 onto this shop's invoice series.
	if _, err := x.sale(service.CreateInput{
		TerminalID: otherTerminal,
		Lines:      []domain.LineInput{line(rice, "1")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation borrowing another store's terminal, got %v", err)
	}

	var sequences int
	mustScan(t, x.db, `SELECT count(*) FROM document_sequences WHERE store_id = $1`, &sequences, x.storeID)
	if sequences != 0 {
		t.Errorf("a refused sale created %d sequence rows", sequences)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("10")) {
		t.Errorf("on hand = %s after a refused sale, want 10", got)
	}
}

func financialYearOf(t time.Time) string {
	year := t.Year()
	if t.Month() < time.April {
		year--
	}
	return fmt.Sprintf("%04d-%02d", year, (year+1)%100)
}
