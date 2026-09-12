package catalogue_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/catalogue"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// newService wires a real repository to a real database. These tests use
// Postgres rather than a mock because the behaviour worth testing here —
// constraint enforcement, ILIKE semantics, transaction rollback — lives in the
// database and a mock would assert only that the code calls itself.
// byHand is the author every test price carries: somebody typing it into the
// price screen. The user is nil because these tests are about the price rules
// and not about who set them; the history tests name a real one.
var byHand = domain.PriceAuthor{Source: domain.PriceManual}

func newService(t *testing.T) (*service.Service, *platform.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db := testsupport.OpenDB(t)

	var storeID, uomID uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO stores (name) VALUES ('Test Shop') RETURNING id`).Scan(&storeID); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	// The units migration seeds the starter set, and units_of_measure.code is
	// unique, so the kilogram is looked up rather than inserted. Inserting one
	// here would fail on the unique index — and a second 'KG' with a different
	// family would be worse than the failure.
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM units_of_measure WHERE code = 'KG'`).Scan(&uomID); err != nil {
		t.Fatalf("read the seeded kilogram: %v", err)
	}

	return service.New(db, repo.NewCatalogue()), db, storeID, uomID
}

// unitID returns the id of a named unit of measure from the migration's seed.
func unitID(t *testing.T, db *platform.DB, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM units_of_measure WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("read seeded unit %s: %v", code, err)
	}
	return id
}

func newCategory(t *testing.T, svc *service.Service) uuid.UUID {
	t.Helper()
	c, err := svc.CreateCategory(context.Background(), "Groceries", "किराना", "ದಿನಸಿ", "1001", nil)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	return c.ID
}

func productInput(categoryID, uomID uuid.UUID, name, barcode string) domain.NewProductInput {
	return domain.NewProductInput{
		Name:       name,
		CategoryID: categoryID,
		UOMID:      uomID,
		Barcode:    barcode,
		// Every product carries the classification a tax invoice has to print.
		// A fixture without one is not a product a shop could sell.
		HSNCode:      "10063010",
		BaseUnitQty:  money.QuantityFromInt(1),
		GSTRate:      money.RateFromInt(5),
		AllowDecimal: true,
		MinStock:     money.QuantityFromInt(2),
	}
}

func TestCreateProductWithPriceIsAtomic(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	price := &domain.UnitPrice{
		MRP:                money.MustFromString("60.00"),
		CostPrice:          money.MustFromString("41.25"),
		SellPrice:          money.MustFromString("55.00"),
		WholesaleSellPrice: money.MustFromString("50.60"),
	}
	p, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Wheat Flour 1kg", "8901001100001"),
		StoreID: storeID,
		Price:   price,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	got, err := svc.GetProduct(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if !got.SellPrice.Equal(money.MustFromString("55.00")) {
		t.Errorf("sell price = %s, want 55.00", got.SellPrice)
	}
	if !got.MRP.Equal(money.MustFromString("60.00")) {
		t.Errorf("mrp = %s, want 60.00", got.MRP)
	}

	// A product is created with the unit it is transacted in, in the same
	// transaction. Without one there is nothing for a price row to name and no
	// meaning for a bare quantity.
	if p.DefaultUnit.ID == uuid.Nil {
		t.Fatal("a new product has no default unit")
	}
	if !p.DefaultUnit.IsDefault || !p.DefaultUnit.QtyInBase.Equal(money.QuantityFromInt(1)) {
		t.Errorf("default unit = %+v, want one default unit worth 1 of the stocking unit", p.DefaultUnit)
	}
	if p.DefaultUnit.Label != "Kilogram" {
		t.Errorf("default unit label = %q, want the unit of measure's name", p.DefaultUnit.Label)
	}

	// GetPrice is the authoritative price record and reads back what was
	// written in the same transaction as the product.
	stored, err := svc.GetPrice(ctx, p.ID, storeID, p.DefaultUnit.ID)
	if err != nil {
		t.Fatalf("get price: %v", err)
	}
	if !stored.CostPrice.Equal(money.MustFromString("41.25")) {
		t.Errorf("cost price = %s, want 41.25", stored.CostPrice)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM product_prices WHERE product_id = $1`, p.ID).Scan(&n); err != nil {
		t.Fatalf("count prices: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 price row, got %d", n)
	}
}

// TestProductReadModelCarriesCategoryStockAndCost covers the three fields the
// products screen needs and the listing did not send.
//
// Each absence showed as a specific defect. category_name missing left the
// Category column reading "-" for every product, because the row carries only a
// category id. stock missing left the Stock column at 0.00 for every product
// while the shelves held real quantities, which is worse than blank: it is a
// confident wrong answer. cost_price missing made the Set Price dialog on the
// same screen pre-fill a cost of zero, so saving it wrote the real cost away.
func TestProductReadModelCarriesCategoryStockAndCost(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	stocked, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Basmati Rice", "8901001100201"),
		StoreID: storeID,
		Price: &domain.UnitPrice{
			MRP:       money.MustFromString("500.00"),
			CostPrice: money.MustFromString("337.50"),
			SellPrice: money.MustFromString("450.00"),
		},
	})
	if err != nil {
		t.Fatalf("create stocked product: %v", err)
	}

	// A product with no stock row at all, and no price row either. It must
	// still list, reading zero rather than dropping out of the catalogue.
	bare, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Zzz Unstocked Item", "8901001100202"),
	})
	if err != nil {
		t.Fatalf("create unstocked product: %v", err)
	}

	// Two batches of the stocked product, because stock is summed across lots
	// and a per-batch figure would understate what is on the shelf. And a batch
	// of the same product in another store, which is not this store's stock.
	var otherStore uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO stores (name) VALUES ('Second Shop') RETURNING id`).Scan(&otherStore); err != nil {
		t.Fatalf("seed second store: %v", err)
	}
	for _, b := range []struct {
		store uuid.UUID
		batch string
		qty   string
	}{
		{storeID, "B1", "90.500"},
		{storeID, "B2", "7.000"},
		{otherStore, "B3", "99.000"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
			 VALUES ($1, $2, $3, $4, round($4::numeric * 337.50, 2))`,
			stocked.ID, b.store, b.batch, b.qty); err != nil {
			t.Fatalf("seed batch %s: %v", b.batch, err)
		}
	}

	products, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("listed %d products, want 2 — a product with no stock row must still list", len(products))
	}

	byID := make(map[uuid.UUID]domain.PricedProduct, len(products))
	for _, p := range products {
		byID[p.ID] = p
	}

	got := byID[stocked.ID]
	if got.CategoryName != "Groceries" {
		t.Errorf("category_name = %q, want Groceries — the screen shows a Category column", got.CategoryName)
	}
	// 90.5 + 7 in this store; the other store's 99 is not ours.
	if !got.Stock.Equal(money.MustQuantityFromString("97.5")) {
		t.Errorf("stock = %s, want 97.5 (the two batches in this store, and not the third elsewhere)", got.Stock)
	}
	if !got.CostPrice.Equal(money.MustFromString("337.50")) {
		t.Errorf("cost_price = %s, want 337.50 — the Set Price dialog pre-fills from this", got.CostPrice)
	}
	if !got.SellPrice.Equal(money.MustFromString("450.00")) {
		t.Errorf("sell_price = %s, want 450.00", got.SellPrice)
	}

	unstocked := byID[bare.ID]
	if !unstocked.Stock.IsZero() {
		t.Errorf("a product with no stock row reads %s, want 0", unstocked.Stock)
	}
	if !unstocked.CostPrice.IsZero() {
		t.Errorf("a product with no price row reads a cost of %s, want 0", unstocked.CostPrice)
	}
	if unstocked.CategoryName != "Groceries" {
		t.Errorf("category_name = %q on the unstocked product", unstocked.CategoryName)
	}

	// Get-one and the barcode lookup share the read model, so they must agree
	// with the listing rather than drifting from it.
	one, err := svc.GetProduct(ctx, stocked.ID, storeID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if one.CategoryName != got.CategoryName || !one.Stock.Equal(got.Stock) || !one.CostPrice.Equal(got.CostPrice) {
		t.Errorf("get-one disagrees with the listing: %+v vs %+v", one, got)
	}
	scanned, err := svc.FindByBarcode(ctx, "8901001100201", storeID)
	if err != nil {
		t.Fatalf("barcode lookup: %v", err)
	}
	if !scanned.Stock.Equal(got.Stock) {
		t.Errorf("barcode lookup stock = %s, want %s", scanned.Stock, got.Stock)
	}

	// Stock is read fresh on every call and never cached: this is the one
	// figure on the screen that moves with every sale.
	if _, err := db.ExecContext(ctx,
		`UPDATE stock SET quantity = quantity - 10 WHERE product_id = $1 AND store_id = $2 AND batch_no = 'B1'`,
		stocked.ID, storeID); err != nil {
		t.Fatalf("sell from a batch: %v", err)
	}
	after, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID})
	if err != nil {
		t.Fatalf("list again: %v", err)
	}
	for _, p := range after {
		if p.ID != stocked.ID {
			continue
		}
		if !p.Stock.Equal(money.MustQuantityFromString("87.5")) {
			t.Errorf("stock after a sale = %s, want 87.5 — the figure must be read fresh", p.Stock)
		}
	}
}

// TestProductGSTRateIsAJSONNumber pins the wire shape of gst_rate.
//
// It was a bare decimal.Decimal, which marshals quoted, so the catalogue went
// out as "gst_rate":"5". The products screen renders the value straight into
// `{p.gst_rate}%` and the form parses it with parseFloat, both of which want a
// number. Inbound requests still accept either form.
func TestProductGSTRateIsAJSONNumber(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	in := productInput(categoryID, uomID, "Taxed Item", "")
	in.GSTRate = money.MustRateFromString("12.5")
	created, err := svc.CreateProduct(ctx, service.CreateProductInput{Product: in, StoreID: storeID})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	got, err := svc.GetProduct(ctx, created.ID, storeID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"gst_rate":12.5`) {
		t.Errorf("gst_rate is not an unquoted number: %s", raw)
	}
	if strings.Contains(string(raw), `"gst_rate":"`) {
		t.Errorf("gst_rate went out as a quoted string: %s", raw)
	}

	// The rate survives the database round trip exactly: 12.5 is not
	// representable in binary floating point and must not have gone near one.
	if !got.GSTRate.Equal(money.MustRateFromString("12.5")) {
		t.Errorf("gst_rate read back as %s, want 12.5", got.GSTRate)
	}
}

func TestCreateProductRollsBackPriceWhenProductFails(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	in := productInput(categoryID, uomID, "Duplicate Barcode", "8901001100002")
	price := &domain.UnitPrice{SellPrice: money.MustFromString("10.00")}

	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: in, StoreID: storeID, Price: price,
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Same barcode: the unique index rejects the product, and the price
	// written earlier in the same transaction must go with it.
	before := countPrices(t, db, storeID)
	_, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: in, StoreID: storeID, Price: price,
	})
	if err == nil {
		t.Fatal("expected a duplicate barcode to be rejected")
	}
	if !errors.Is(err, platform.ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
	if after := countPrices(t, db, storeID); after != before {
		t.Errorf("price rows changed from %d to %d — the transaction did not roll back", before, after)
	}
}

func TestProductSearchIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Basmati Rice", "8901001100010"),
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	// SQLite's LIKE is case-insensitive for ASCII and Postgres's is not, so a
	// literal port of this query would silently return nothing here.
	for _, term := range []string{"basmati", "BASMATI", "Basmati", "rice"} {
		found, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID, Search: term})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if len(found) != 1 {
			t.Errorf("search %q returned %d products, want 1", term, len(found))
		}
	}
}

func TestProductSearchEscapesWildcards(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	for _, name := range []string{"Sugar 1kg", "50% Discount Pack"} {
		if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
			Product: productInput(categoryID, uomID, name, ""),
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	// A user typing "%" means the character, not "match everything".
	found, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID, Search: "50%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("searching for \"50%%\" returned %d products, want 1", len(found))
	}
	if found[0].Name != "50% Discount Pack" {
		t.Errorf("matched %q", found[0].Name)
	}
}

func TestSellingAboveMRPIsRejected(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	p, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Packaged Snack", ""),
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	// Selling above the printed MRP breaches the Legal Metrology (Packaged
	// Commodities) Rules, so the system must not be able to express it.
	_, err = svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:     p.ID,
		ProductUnitID: p.DefaultUnit.ID,
		StoreID:       storeID,
		MRP:           money.MustFromString("20.00"),
		SellPrice:     money.MustFromString("25.00"),
	}, byHand)
	if err == nil {
		t.Fatal("expected a selling price above MRP to be rejected")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation, got %v", err)
	}

	// An MRP of zero means "not a packaged commodity" and is not a ceiling.
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:     p.ID,
		ProductUnitID: p.DefaultUnit.ID,
		StoreID:       storeID,
		MRP:           money.Zero(),
		SellPrice:     money.MustFromString("25.00"),
	}, byHand); err != nil {
		t.Errorf("an unpriced MRP should not cap the selling price: %v", err)
	}
}

func TestSetPriceReplacesRatherThanDuplicating(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	p, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Cooking Oil 1L", ""),
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	for _, amount := range []string{"180.00", "185.00", "190.00"} {
		if _, err := svc.SetPrice(ctx, domain.UnitPrice{
			ProductID:     p.ID,
			ProductUnitID: p.DefaultUnit.ID,
			StoreID:       storeID,
			SellPrice:     money.MustFromString(amount),
		}, byHand); err != nil {
			t.Fatalf("set price %s: %v", amount, err)
		}
	}

	// The ON CONFLICT names product_prices_product_store_unit_key. The old
	// product_prices_product_store_key no longer exists, so a stale conflict
	// target is not a lint issue but an error on every save — and a missing one
	// would leave three rows here instead of one.
	if n := countPrices(t, db, storeID); n != 1 {
		t.Errorf("three price changes produced %d rows, want 1", n)
	}
	got, err := svc.GetPrice(ctx, p.ID, storeID, p.DefaultUnit.ID)
	if err != nil {
		t.Fatalf("get price: %v", err)
	}
	if !got.SellPrice.Equal(money.MustFromString("190.00")) {
		t.Errorf("sell price = %s, want the latest 190.00", got.SellPrice)
	}
}

func TestDeactivatedProductLeavesTheCatalogue(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	p, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Discontinued Item", ""),
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	if err := svc.SetProductActive(ctx, p.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	active, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("deactivated product still listed: %d products", len(active))
	}

	// It must remain reachable for the screen that reactivates it, and for
	// the invoice history that references it.
	all, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID, IncludeInactive: true})
	if err != nil {
		t.Fatalf("list including inactive: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected the product to survive deactivation, got %d", len(all))
	}
}

func TestSetProductActiveReportsMissingProduct(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)

	err := svc.SetProductActive(context.Background(), uuid.New(), false)
	if !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown product, got %v", err)
	}
}

func TestInvalidGSTRateIsRejected(t *testing.T) {
	t.Parallel()
	svc, _, _, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	for _, rate := range []money.Rate{
		money.RateFromInt(-1),
		money.RateFromInt(101),
	} {
		in := productInput(categoryID, uomID, "Bad Rate", "")
		in.GSTRate = rate
		if _, err := svc.CreateProduct(ctx, service.CreateProductInput{Product: in}); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("GST rate %s: expected ErrValidation, got %v", rate, err)
		}
	}
}

func TestBarcodeLookupFindsTheProduct(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Scanned Item", "8901001100099"),
		StoreID: storeID,
		Price:   &domain.UnitPrice{SellPrice: money.MustFromString("45.00")},
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	got, err := svc.FindByBarcode(ctx, "8901001100099", storeID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Name != "Scanned Item" {
		t.Errorf("found %q", got.Name)
	}

	if _, err := svc.FindByBarcode(ctx, "0000000000000", storeID); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown barcode, got %v", err)
	}
}

func TestSeveralProductsMayHaveNoBarcode(t *testing.T) {
	t.Parallel()
	svc, _, _, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	// Loose goods have no barcode. Storing "" would collide on the unique
	// index the second time, so a blank barcode must persist as NULL.
	for _, name := range []string{"Loose Rice", "Loose Dal", "Loose Sugar"} {
		if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
			Product: productInput(categoryID, uomID, name, ""),
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
}

func TestWholesalePricingFallsBackToRetail(t *testing.T) {
	t.Parallel()
	p := domain.PricedProduct{
		SellPrice:          money.MustFromString("55.00"),
		WholesaleSellPrice: money.MustFromString("50.60"),
	}
	if got := p.PriceFor(true); !got.Equal(money.MustFromString("50.60")) {
		t.Errorf("wholesale customer charged %s, want 50.60", got)
	}
	if got := p.PriceFor(false); !got.Equal(money.MustFromString("55.00")) {
		t.Errorf("retail customer charged %s, want 55.00", got)
	}

	// No wholesale price set means the shelf price applies, rather than zero.
	noWholesale := domain.PricedProduct{SellPrice: money.MustFromString("55.00")}
	if got := noWholesale.PriceFor(true); !got.Equal(money.MustFromString("55.00")) {
		t.Errorf("with no wholesale price a wholesale customer was charged %s, want 55.00", got)
	}
}

func countPrices(t *testing.T, db *platform.DB, storeID uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM product_prices WHERE store_id = $1`, storeID).Scan(&n); err != nil {
		t.Fatalf("count prices: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Units: the change this migration exists for
// ---------------------------------------------------------------------------

// rice creates the product these tests sell loose and by the sack, and returns
// it with its default unit (the loose kilo).
func rice(t *testing.T, svc *service.Service, categoryID, uomID, storeID uuid.UUID) service.CreatedProduct {
	t.Helper()
	p, err := svc.CreateProduct(context.Background(), service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Sona Masoori Rice", "8901001100301"),
		StoreID: storeID,
		Price:   &domain.UnitPrice{SellPrice: money.MustFromString("45.00")},
	})
	if err != nil {
		t.Fatalf("create rice: %v", err)
	}
	return p
}

// TestBulkIsPricedIndependentlyOfLoose is the whole point of the change.
//
// The schema this replaces was UNIQUE (product_id, store_id): one price per
// product, so a 25 kg sack necessarily cost twenty-five times the loose kilo.
// A wholesaler's entire trade is that it does not. Here loose rice is Rs.45/kg
// and the sack is Rs.1,050 — Rs.42/kg — and the two prices are stored rows,
// neither derived from the other.
func TestBulkIsPricedIndependentlyOfLoose(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID:     p.ID,
		Label:         "25 kg sack",
		QtyInBase:     money.QuantityFromInt(25),
		Barcode:       "8901001100302",
		IsSellable:    true,
		IsPurchasable: true,
		SortOrder:     2,
	})
	if err != nil {
		t.Fatalf("add sack: %v", err)
	}
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:     p.ID,
		ProductUnitID: sack.ID,
		StoreID:       storeID,
		SellPrice:     money.MustFromString("1050.00"),
	}, byHand); err != nil {
		t.Fatalf("price the sack: %v", err)
	}

	prices, err := svc.ListPrices(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("list prices: %v", err)
	}
	if len(prices) != 2 {
		t.Fatalf("listed %d priced units, want 2", len(prices))
	}

	// Default first, then smallest to largest: a counter reaches for loose far
	// more often than for a sack.
	if !prices[0].Unit.IsDefault || prices[0].Unit.Label != "Kilogram" {
		t.Errorf("first unit is %q (default=%v), want the default Kilogram first",
			prices[0].Unit.Label, prices[0].Unit.IsDefault)
	}
	if prices[1].Unit.Label != "25 kg sack" {
		t.Errorf("second unit is %q, want the sack", prices[1].Unit.Label)
	}

	if !prices[0].Price.SellPrice.Equal(money.MustFromString("45.00")) {
		t.Errorf("loose = %s, want 45.00", prices[0].Price.SellPrice)
	}
	// 1,050 and not 1,125. Twenty-five times the loose price is what the old
	// model forced; this figure could not previously be expressed at all.
	if !prices[1].Price.SellPrice.Equal(money.MustFromString("1050.00")) {
		t.Errorf("sack = %s, want 1050.00 and NOT 25 x 45 = 1125.00", prices[1].Price.SellPrice)
	}

	// The comparison a buyer actually makes: Rs.42/kg in bulk against Rs.45
	// loose. It is for display only — billing multiplies, never divides.
	if got := prices[1].Price.EffectiveRatePerBase(false); !got.Equal(money.MustFromString("42.00")) {
		t.Errorf("the sack works out at %s per kilo, want exactly 42.00", got)
	}

	// Both rows exist independently: changing one leaves the other alone.
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:     p.ID,
		ProductUnitID: p.DefaultUnit.ID,
		StoreID:       storeID,
		SellPrice:     money.MustFromString("47.00"),
	}, byHand); err != nil {
		t.Fatalf("reprice loose: %v", err)
	}
	sackPrice, err := svc.GetPrice(ctx, p.ID, storeID, sack.ID)
	if err != nil {
		t.Fatalf("get sack price: %v", err)
	}
	if !sackPrice.SellPrice.Equal(money.MustFromString("1050.00")) {
		t.Errorf("repricing loose moved the sack to %s — the prices are not independent",
			sackPrice.SellPrice)
	}
}

// TestSellingTwoSacksLeavesOnePoolOfKilograms proves the stock model the units
// migration chose: one pool, in the product's base unit.
//
// Two sacks is 50 kg out of the same kilograms a 250 g sale takes 0.25 from.
// There is no separate sack balance, so the shop can never be out of loose rice
// while sacks sit on the shelf.
func TestSellingTwoSacksLeavesOnePoolOfKilograms(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add sack: %v", err)
	}
	pouch, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "250 g pouch", QtyInBase: money.MustQuantityFromString("0.25"),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add pouch: %v", err)
	}

	// Two sacks is exactly fifty kilograms — a multiplication, with no rate and
	// no rounding anywhere in it.
	if got := sack.BaseQuantity(money.QuantityFromInt(2)); !got.Equal(money.QuantityFromInt(50)) {
		t.Errorf("two 25 kg sacks = %s, want exactly 50", got)
	}
	// And a 250 g sale takes a quarter of a kilo from that same pool.
	if got := pouch.BaseQuantity(money.QuantityFromInt(1)); !got.Equal(money.MustQuantityFromString("0.25")) {
		t.Errorf("one 250 g pouch = %s, want exactly 0.25", got)
	}
	if got := pouch.BaseQuantity(money.QuantityFromInt(4)); !got.Equal(money.QuantityFromInt(1)) {
		t.Errorf("four pouches = %s, want exactly 1 kilo", got)
	}

	// Every unit reports against one figure, and the product read model carries
	// exactly one stock number no matter how many units it can be sold in.
	got, err := svc.GetProduct(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if len(got.Units) != 3 {
		t.Fatalf("product offers %d sellable units, want 3", len(got.Units))
	}
	if !got.Stock.IsZero() {
		t.Errorf("stock = %s on a product nothing was received into", got.Stock)
	}
}

// TestAUnitWorthNothingIsRefused: qty_in_base is what turns a sale into a stock
// movement. Zero would let a sale deduct nothing while the bill printed as
// though goods had left, and negative would run stock backwards.
func TestAUnitWorthNothingIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	for name, qty := range map[string]money.Quantity{
		"zero":     money.ZeroQuantity(),
		"negative": money.MustQuantityFromString("-1"),
	} {
		_, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
			ProductID: p.ID, Label: "Impossible", QtyInBase: qty,
			IsSellable: true, IsPurchasable: true,
		})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s qty_in_base: expected ErrValidation, got %v", name, err)
		}
	}

	// A rejected unit is not stored: only the default the product was created
	// with survives.
	units, err := svc.ListProductUnits(ctx, p.ID)
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	if len(units) != 1 {
		t.Errorf("a rejected unit was stored anyway: %d rows", len(units))
	}
}

// TestSettingANewDefaultClearsTheOldOneAtomically.
//
// idx_product_units_one_default is a partial unique index over
// (product_id) WHERE is_default. Setting the new default before clearing the
// old one is rejected outright, and clearing in a separate transaction would
// leave a window in which the product has no default at all — during which a
// bare quantity means nothing.
func TestSettingANewDefaultClearsTheOldOneAtomically(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add sack: %v", err)
	}

	promoted, err := svc.SetDefaultUnit(ctx, sack.ID, p.ID)
	if err != nil {
		t.Fatalf("set the sack as default: %v", err)
	}
	if !promoted.IsDefault {
		t.Error("the promoted unit did not come back as the default")
	}

	// Exactly one default, and it is the new one. Two would make a bare quantity
	// ambiguous; none would make it meaningless.
	var defaults int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM product_units WHERE product_id = $1 AND is_default`, p.ID).Scan(&defaults); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	if defaults != 1 {
		t.Fatalf("%d units are marked default, want exactly 1", defaults)
	}

	units, err := svc.ListProductUnits(ctx, p.ID)
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	for _, u := range units {
		if u.ID == sack.ID && !u.IsDefault {
			t.Error("the sack is not the default")
		}
		if u.ID == p.DefaultUnit.ID && u.IsDefault {
			t.Error("the loose kilo is still marked default")
		}
	}

	// The product read model follows: its headline price figures and its
	// default_product_unit_id now describe the sack.
	got, err := svc.GetProduct(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if got.DefaultProductUnitID != sack.ID {
		t.Errorf("default_product_unit_id = %s, want the sack %s", got.DefaultProductUnitID, sack.ID)
	}
	if len(got.Units) == 0 || !got.Units[0].Unit.IsDefault {
		t.Error("the units list no longer leads with the default")
	}

	// Promoting the same unit again is idempotent rather than a unique-index
	// violation: ClearDefault excepts the unit being promoted.
	if _, err := svc.SetDefaultUnit(ctx, sack.ID, p.ID); err != nil {
		t.Errorf("re-promoting the current default failed: %v", err)
	}
}

// TestANonSellableUnitCannotBeTheDefault. The default is what a bare quantity
// means and what a sale charges for, so a purchase-only pallet cannot hold the
// role.
func TestANonSellableUnitCannotBeTheDefault(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	pallet, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "Supplier pallet", QtyInBase: money.QuantityFromInt(1000),
		IsSellable: false, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add pallet: %v", err)
	}

	if _, err := svc.SetDefaultUnit(ctx, pallet.ID, p.ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation promoting a non-sellable unit, got %v", err)
	}

	// A unit that is not sellable is also not on the counter's list.
	prices, err := svc.ListPrices(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("list prices: %v", err)
	}
	for _, entry := range prices {
		if entry.Unit.ID == pallet.ID {
			t.Error("a purchase-only unit is being offered for sale")
		}
	}

	// And the domain refuses to construct one at all.
	if _, err := svc.SetDefaultUnit(ctx, uuid.New(), p.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown unit, got %v", err)
	}
}

// TestAnUnpricedUnitIsListedRatherThanHidden. A unit that vanishes from the
// counter looks to a cashier exactly like a missing product; one shown as
// unpriced says precisely what to go and fix.
func TestAnUnpricedUnitIsListedRatherThanHidden(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	bag, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "5 kg bag", QtyInBase: money.QuantityFromInt(5),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add bag: %v", err)
	}

	prices, err := svc.ListPrices(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("list prices: %v", err)
	}
	var found bool
	for _, entry := range prices {
		if entry.Unit.ID != bag.ID {
			continue
		}
		found = true
		if entry.HasPrice {
			t.Error("an unpriced unit reports has_price true")
		}
		// Zero is not the same statement as "no price": a free item and an
		// unpriced one are different things, and has_price is what separates them.
		if !entry.Price.SellPrice.IsZero() {
			t.Errorf("an unpriced unit carries a price of %s", entry.Price.SellPrice)
		}
	}
	if !found {
		t.Error("the unpriced 5 kg bag was hidden from the counter")
	}
}

// TestScanningTheSackPricesTheSack. A barcode identifies a unit, not just a
// product — which is why product_units.barcode is uniquely indexed.
func TestScanningTheSackPricesTheSack(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		Barcode: "8901001100302", IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add sack: %v", err)
	}
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID: p.ID, ProductUnitID: sack.ID, StoreID: storeID,
		SellPrice: money.MustFromString("1050.00"),
	}, byHand); err != nil {
		t.Fatalf("price the sack: %v", err)
	}

	// The sack's own barcode resolves to the sack, at the sack's price.
	scanned, err := svc.FindByBarcode(ctx, "8901001100302", storeID)
	if err != nil {
		t.Fatalf("scan the sack: %v", err)
	}
	if scanned.ID != p.ID {
		t.Errorf("scan found product %s, want %s", scanned.ID, p.ID)
	}
	if scanned.ScannedUnit.ID != sack.ID {
		t.Errorf("scan resolved to unit %q, want the 25 kg sack", scanned.ScannedUnit.Label)
	}
	if !scanned.MatchedUnitBarcode {
		t.Error("matched_unit_barcode is false for a scan of the unit's own barcode")
	}
	if !scanned.ScannedHasPrice || !scanned.ScannedPrice.SellPrice.Equal(money.MustFromString("1050.00")) {
		t.Errorf("the till would charge %s for a sack, want 1050.00", scanned.ScannedPrice.SellPrice)
	}
	if !scanned.ScannedUnit.QtyInBase.Equal(money.QuantityFromInt(25)) {
		t.Errorf("the scanned unit is worth %s of the stocking unit, want 25", scanned.ScannedUnit.QtyInBase)
	}
	// The headline figures still describe the DEFAULT unit, so a client that
	// ignores the scan fields goes on behaving as it did.
	if !scanned.SellPrice.Equal(money.MustFromString("45.00")) {
		t.Errorf("the product's headline sell_price = %s, want the loose 45.00", scanned.SellPrice)
	}

	// The product's own barcode names no unit, so it falls back to the default.
	loose, err := svc.FindByBarcode(ctx, "8901001100301", storeID)
	if err != nil {
		t.Fatalf("scan the product barcode: %v", err)
	}
	if loose.ScannedUnit.ID != p.DefaultUnit.ID {
		t.Errorf("a product barcode resolved to %q, want the default unit", loose.ScannedUnit.Label)
	}
	if loose.MatchedUnitBarcode {
		t.Error("matched_unit_barcode is true for a scan of the product's barcode")
	}
	if !loose.ScannedPrice.SellPrice.Equal(money.MustFromString("45.00")) {
		t.Errorf("loose scan charges %s, want 45.00", loose.ScannedPrice.SellPrice)
	}

	if _, err := svc.FindByBarcode(ctx, "0000000000000", storeID); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown barcode, got %v", err)
	}
}

// TestTwoUnitsCannotShareABarcode: a scan has to identify one thing, so
// uniqueness is global across product units rather than per product.
func TestTwoUnitsCannotShareABarcode(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	if _, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "5 kg bag", QtyInBase: money.QuantityFromInt(5),
		Barcode: "8901001100777", IsSellable: true, IsPurchasable: true,
	}); err != nil {
		t.Fatalf("add bag: %v", err)
	}
	if _, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		Barcode: "8901001100777", IsSellable: true, IsPurchasable: true,
	}); !errors.Is(err, platform.ErrConflict) {
		t.Errorf("expected ErrConflict for a duplicate unit barcode, got %v", err)
	}

	// Many units legitimately have no barcode, and blank must persist as NULL
	// or the second one collides on the same unique index.
	for _, label := range []string{"Loose scoop", "Half kilo", "Two kilo"} {
		if _, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
			ProductID: p.ID, Label: label, QtyInBase: money.QuantityFromInt(2),
			IsSellable: true, IsPurchasable: true,
		}); err != nil {
			t.Fatalf("add unbarcoded unit %q: %v", label, err)
		}
	}
}

// TestDeactivatedUnitLeavesTheCounterAndGivesUpTheDefault.
//
// A deactivated unit keeping the default flag would be the worst of both: a
// hidden unit still deciding what a bare quantity means, and — because the
// partial index does not care that the row is inactive — no other unit able to
// take the role.
func TestDeactivatedUnitLeavesTheCounterAndGivesUpTheDefault(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add sack: %v", err)
	}

	if err := svc.SetProductUnitActive(ctx, p.DefaultUnit.ID, p.ID, false); err != nil {
		t.Fatalf("deactivate the default unit: %v", err)
	}

	var defaults int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM product_units WHERE product_id = $1 AND is_default`, p.ID).Scan(&defaults); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	if defaults != 0 {
		t.Errorf("a deactivated unit is still marked default (%d defaults)", defaults)
	}

	// The role is now free for another unit to take.
	if _, err := svc.SetDefaultUnit(ctx, sack.ID, p.ID); err != nil {
		t.Fatalf("promote the sack after the old default was retired: %v", err)
	}

	// The retired unit is off the counter but still on the management screen,
	// because invoice history references it and somebody may want it back.
	prices, err := svc.ListPrices(ctx, p.ID, storeID)
	if err != nil {
		t.Fatalf("list prices: %v", err)
	}
	for _, entry := range prices {
		if entry.Unit.ID == p.DefaultUnit.ID {
			t.Error("a deactivated unit is still offered for sale")
		}
	}
	units, err := svc.ListProductUnits(ctx, p.ID)
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	if len(units) != 2 {
		t.Errorf("the management screen lists %d units, want both", len(units))
	}
}

// TestUnitsConvertWithinAFamilyAndNotAcross.
//
// The family is what refuses nonsense. Kilograms convert to grams because both
// measure weight; kilograms to litres is not a conversion that exists to be got
// wrong. The factors are the migration's own seed data, so this also checks
// that a kilo really was recorded as a thousand grams.
func TestUnitsConvertWithinAFamilyAndNotAcross(t *testing.T) {
	t.Parallel()
	svc, db, _, _ := newService(t)
	ctx := context.Background()

	units, err := svc.ListUnits(ctx, domain.UnitFilter{})
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	byCode := make(map[string]domain.UnitOfMeasure, len(units))
	for _, u := range units {
		byCode[u.Code] = u
	}
	for _, code := range []string{"KG", "G", "L", "FT", "M"} {
		if _, ok := byCode[code]; !ok {
			t.Fatalf("the seeded starter set has no %s", code)
		}
	}
	_ = unitID(t, db, "KG")

	// Two and a half kilos is two thousand five hundred grams, exactly.
	grams, err := domain.ConvertWithin(money.MustQuantityFromString("2.5"), byCode["KG"], byCode["G"])
	if err != nil {
		t.Fatalf("kg to g: %v", err)
	}
	if !grams.Equal(money.QuantityFromInt(2500)) {
		t.Errorf("2.5 kg = %s g, want exactly 2500", grams)
	}
	back, err := domain.ConvertWithin(grams, byCode["G"], byCode["KG"])
	if err != nil {
		t.Fatalf("g to kg: %v", err)
	}
	if !back.Equal(money.MustQuantityFromString("2.5")) {
		t.Errorf("the round trip returned %s, want 2.5 — a factor lost precision", back)
	}

	// A foot is 0.3048 m exactly, which is why the FACTOR carries nine decimal
	// places rather than being rounded to something convenient.
	if !byCode["FT"].FactorToBase.Equal(money.MustRateFromString("0.3048")) {
		t.Errorf("the foot's factor is %s, want exactly 0.3048", byCode["FT"].FactorToBase)
	}
	// A quantity, by contrast, is stored at three decimal places — the scale of
	// every quantity column in the schema — so the exactness of the factor shows
	// at a length where three places can hold it. A thousand feet is 304.8 m
	// against the 305 a factor rounded to 0.305 would have produced: the error
	// the nine decimal places exist to prevent, made visible.
	feet, err := domain.ConvertWithin(money.QuantityFromInt(1000), byCode["FT"], byCode["M"])
	if err != nil {
		t.Fatalf("ft to m: %v", err)
	}
	if !feet.Equal(money.MustQuantityFromString("304.8")) {
		t.Errorf("1000 ft = %s m, want exactly 304.8", feet)
	}

	// And the conversion that must not exist.
	if _, err := domain.ConvertWithin(money.QuantityFromInt(1), byCode["KG"], byCode["L"]); err == nil {
		t.Fatal("kilograms were converted into litres")
	} else if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation converting across families, got %v", err)
	}
}

// TestANewUnitNeedsAFamilyAndAPositiveFactor. A unit that converts to nothing
// is not a unit, and a zero factor is a division by zero waiting in the
// conversion path.
func TestANewUnitNeedsAFamilyAndAPositiveFactor(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	families, err := svc.ListFamilies(ctx, false)
	if err != nil {
		t.Fatalf("list families: %v", err)
	}
	var weight domain.Family
	for _, f := range families {
		if f.Code == "WEIGHT" {
			weight = f
		}
	}
	if weight.ID == uuid.Nil {
		t.Fatal("the migration seeded no WEIGHT family")
	}
	if weight.BaseUOMID == uuid.Nil {
		t.Error("the WEIGHT family has no canonical unit, so nothing in it converts")
	}

	if _, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "BAD", Name: "No family", FactorToBase: money.RateFromInt(1),
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for a unit with no family, got %v", err)
	}
	for name, factor := range map[string]money.Rate{
		"zero":     money.ZeroRate(),
		"negative": money.MustRateFromString("-2"),
	} {
		if _, err := svc.CreateUnit(ctx, service.CreateUnitInput{
			Code: "BAD", Name: "Bad factor", FamilyID: weight.ID, FactorToBase: factor,
		}); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s factor: expected ErrValidation, got %v", name, err)
		}
	}

	// A real one: the maund, still used in parts of the grain trade. No deploy,
	// just a row — which is the point of units being data.
	maund, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "MAUND", Name: "Maund", DecimalPlaces: 3,
		FamilyID: weight.ID, FactorToBase: money.MustRateFromString("37.3242"),
	})
	if err != nil {
		t.Fatalf("create maund: %v", err)
	}
	if !maund.FactorToBase.Equal(money.MustRateFromString("37.3242")) {
		t.Errorf("factor round-tripped as %s, want 37.3242", maund.FactorToBase)
	}

	// It converts against the units already in its family, immediately.
	all, err := svc.ListUnits(ctx, domain.UnitFilter{FamilyID: &weight.ID})
	if err != nil {
		t.Fatalf("list weight units: %v", err)
	}
	var kg, m domain.UnitOfMeasure
	for _, u := range all {
		switch u.Code {
		case "KG":
			kg = u
		case "MAUND":
			m = u
		}
	}
	// Ten of them, so that the fourth decimal place of the factor still shows at
	// the three decimal places a quantity is stored to.
	kilos, err := domain.ConvertWithin(money.QuantityFromInt(10), m, kg)
	if err != nil {
		t.Fatalf("maund to kg: %v", err)
	}
	if !kilos.Equal(money.MustQuantityFromString("373.242")) {
		t.Errorf("ten maunds = %s kg, want 373.242", kilos)
	}
}

// TestFactorToBaseIsAJSONNumber pins the wire shape. A bare decimal.Decimal
// marshals quoted, and a client doing arithmetic on "0.000001" gets a string.
func TestFactorToBaseIsAJSONNumber(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)

	units, err := svc.ListUnits(context.Background(), domain.UnitFilter{})
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	var milligram domain.UnitOfMeasure
	for _, u := range units {
		if u.Code == "MG" {
			milligram = u
		}
	}
	if milligram.ID == uuid.Nil {
		t.Fatal("no milligram in the seeded set")
	}

	raw, err := json.Marshal(milligram)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"factor_to_base":"`) {
		t.Errorf("factor_to_base went out as a quoted string: %s", raw)
	}
	if !strings.Contains(string(raw), `"factor_to_base":0.000001`) {
		t.Errorf("factor_to_base did not survive as an exact number: %s", raw)
	}
}

// TestANewFamilyAdoptsItsFirstUnitAsTheBase. A family whose canonical unit does
// not exist converts nothing, so "add a family, add its units" has to work in
// the obvious order without a separate step nobody would know to take.
func TestANewFamilyAdoptsItsFirstUnitAsTheBase(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	family, err := svc.CreateFamily(ctx, "time", "Time")
	if err != nil {
		t.Fatalf("create family: %v", err)
	}
	if family.Code != "TIME" {
		t.Errorf("family code = %q, want it upper-cased", family.Code)
	}
	if family.BaseUOMID != uuid.Nil {
		t.Error("a brand new family already has a canonical unit")
	}

	hour, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "HR", Name: "Hour", FamilyID: family.ID, FactorToBase: money.RateFromInt(1),
	})
	if err != nil {
		t.Fatalf("create hour: %v", err)
	}

	families, err := svc.ListFamilies(ctx, false)
	if err != nil {
		t.Fatalf("list families: %v", err)
	}
	for _, f := range families {
		if f.ID != family.ID {
			continue
		}
		if f.BaseUOMID != hour.ID {
			t.Errorf("the family's base unit is %s, want the hour %s", f.BaseUOMID, hour.ID)
		}
	}

	// A second unit does not displace the base.
	day, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "DAY", Name: "Day", FamilyID: family.ID, FactorToBase: money.RateFromInt(24),
	})
	if err != nil {
		t.Fatalf("create day: %v", err)
	}
	hours, err := domain.ConvertWithin(money.QuantityFromInt(2), day, hour)
	if err != nil {
		t.Fatalf("days to hours: %v", err)
	}
	if !hours.Equal(money.QuantityFromInt(48)) {
		t.Errorf("two days = %s hours, want 48", hours)
	}

	// A unit from another family cannot become this one's canonical unit: that
	// would silently corrupt every conversion in the family at once.
	weightUnits, err := svc.ListUnits(ctx, domain.UnitFilter{})
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	for _, u := range weightUnits {
		if u.Code != "KG" {
			continue
		}
		if err := svc.SetFamilyBaseUnit(ctx, family.ID, u.ID); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("a kilogram was accepted as the base of the time family: %v", err)
		}
	}
}

// TestPricingAnotherProductsUnitIsRefused. product_prices has no constraint
// tying its product_id to its product_unit_id's product, so this is the only
// thing standing between the two.
func TestPricingAnotherProductsUnitIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	rice := rice(t, svc, categoryID, uomID, storeID)
	dal, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Toor Dal", "8901001100401"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create dal: %v", err)
	}

	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:     rice.ID,
		ProductUnitID: dal.DefaultUnit.ID,
		StoreID:       storeID,
		SellPrice:     money.MustFromString("45.00"),
	}, byHand); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation pricing another product's unit, got %v", err)
	}

	// The same guard on the update path.
	if _, err := svc.UpdateProductUnit(ctx, dal.DefaultUnit.ID, domain.NewProductUnitInput{
		ProductID: rice.ID, Label: "Hijacked", QtyInBase: money.QuantityFromInt(1),
		IsSellable: true, IsPurchasable: true,
	}); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound updating another product's unit, got %v", err)
	}
}

// TestUpdatingAUnitKeepsItsDefaultFlag. is_default is not among the editable
// fields, so an ordinary edit can never trip the one-default index — and can
// never silently clear the flag either.
func TestUpdatingAUnitKeepsItsDefaultFlag(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	updated, err := svc.UpdateProductUnit(ctx, p.DefaultUnit.ID, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "Loose (per kg)", QtyInBase: money.QuantityFromInt(1),
		IsSellable: true, IsPurchasable: true, SortOrder: 5,
	})
	if err != nil {
		t.Fatalf("update unit: %v", err)
	}
	if !updated.IsDefault {
		t.Error("editing the default unit cleared its default flag")
	}
	if updated.Label != "Loose (per kg)" {
		t.Errorf("label = %q", updated.Label)
	}

	units, err := svc.ListProductUnits(ctx, p.ID)
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	if len(units) != 1 || !units[0].IsDefault {
		t.Errorf("after the edit the product has %d units and the default is unclear", len(units))
	}
}

// TestListingManyProductsDoesNotDuplicateRows.
//
// product_prices is now unique on (product_id, store_id, product_unit_id), so a
// join on (product_id, store_id) alone returns one row per priced unit. A
// product sold three ways would appear three times, at three prices, in no
// defined order. The join goes through the default unit for exactly this
// reason, and the correlated stock subquery stays a subquery for the same one.
func TestListingManyProductsDoesNotDuplicateRows(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)
	p := rice(t, svc, categoryID, uomID, storeID)

	for _, u := range []struct {
		label string
		qty   int64
		price string
	}{
		{"5 kg bag", 5, "225.00"},
		{"25 kg sack", 25, "1050.00"},
	} {
		unit, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
			ProductID: p.ID, Label: u.label, QtyInBase: money.QuantityFromInt(u.qty),
			IsSellable: true, IsPurchasable: true,
		})
		if err != nil {
			t.Fatalf("add %s: %v", u.label, err)
		}
		if _, err := svc.SetPrice(ctx, domain.UnitPrice{
			ProductID: p.ID, ProductUnitID: unit.ID, StoreID: storeID,
			SellPrice: money.MustFromString(u.price),
		}, byHand); err != nil {
			t.Fatalf("price %s: %v", u.label, err)
		}
	}

	// Two batches as well, because the stock figure is a correlated subquery
	// and turning it into a join would repeat the product once per lot.
	for _, batch := range []string{"B1", "B2"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
			 VALUES ($1, $2, $3, 40, 1500.00)`, p.ID, storeID, batch); err != nil {
			t.Fatalf("seed batch %s: %v", batch, err)
		}
	}

	products, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("one product priced three ways and held in two batches listed %d times", len(products))
	}

	got := products[0]
	// The headline price is the DEFAULT unit's, not whichever row came back
	// first, so the existing products screen goes on showing Rs.45 a kilo.
	if !got.SellPrice.Equal(money.MustFromString("45.00")) {
		t.Errorf("headline sell_price = %s, want the default unit's 45.00", got.SellPrice)
	}
	if !got.Stock.Equal(money.QuantityFromInt(80)) {
		t.Errorf("stock = %s, want 80 summed across both batches", got.Stock)
	}
	if len(got.Units) != 3 {
		t.Fatalf("the listing carries %d units, want all 3 so the POS can offer a choice", len(got.Units))
	}
	// Default first, then by size ascending.
	wantOrder := []string{"Kilogram", "5 kg bag", "25 kg sack"}
	for i, want := range wantOrder {
		if got.Units[i].Unit.Label != want {
			t.Errorf("unit %d is %q, want %q", i, got.Units[i].Unit.Label, want)
		}
	}
	if !got.Units[2].Price.SellPrice.Equal(money.MustFromString("1050.00")) {
		t.Errorf("the sack in the listing costs %s, want 1050.00", got.Units[2].Price.SellPrice)
	}
}

// Products that predate the HSN rule.
//
// A tax invoice has to print the classification of what it sells, so every bill
// including one of these is a document the shop cannot file. The constructor
// refuses to make one, which is why this test inserts a row directly: that is
// exactly the situation the filter exists for, a product already on the shelf
// from before the rule.
//
// Refusing to sell them would stop a shop trading over a missing field. The
// filter is the alternative — somebody works through the list.
func TestUnclassifiedProductsCanBeFound(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	classified, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Zz Classified", "8901088100001"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create the classified product: %v", err)
	}

	var oldID, retiredID uuid.UUID
	for _, row := range []struct {
		name   string
		active bool
		into   *uuid.UUID
	}{
		{"Zz Predates The Rule", true, &oldID},
		{"Zz Retired And Unclassified", false, &retiredID},
	} {
		if err := db.QueryRowContext(ctx,
			`INSERT INTO products (name, category_id, uom_id, base_unit_qty, gst_rate,
				min_stock, is_active, hsn_code, sac_code)
			 -- hsn_code is nullable and sac_code is NOT NULL with a '' default,
			 -- so "no classification" is spelt two different ways in the same
			 -- row. The filter COALESCEs both, and this covers the pair.
			 VALUES ($1, $2, $3, 1, 5, 0, $4, NULL, '') RETURNING id`,
			row.name, categoryID, uomID, row.active).Scan(row.into); err != nil {
			t.Fatalf("insert %s: %v", row.name, err)
		}
	}

	found, err := svc.ListProducts(ctx, domain.ProductFilter{
		StoreID:      storeID,
		Unclassified: true,
	})
	if err != nil {
		t.Fatalf("list unclassified: %v", err)
	}

	ids := map[uuid.UUID]bool{}
	for _, p := range found {
		ids[p.ID] = true
	}
	if !ids[oldID] {
		t.Error("a product with no HSN is missing from the unclassified list")
	}
	if ids[classified.ID] {
		t.Error("a product with an HSN is in the unclassified list")
	}
	// A retired product cannot be sold, so it cannot produce an invoice that
	// cannot be filed. Listing it would send somebody to fix something that
	// does not matter, and on a shop with years of withdrawn lines that is most
	// of the list.
	if ids[retiredID] {
		t.Error("a retired product is in the unclassified list")
	}
}

// The three list filters are applied by the database, not by the caller.
//
// The products screen used to narrow the arrived rows in the browser, and two
// of the three could not work that way: "show inactive" narrowed a list that
// had never contained an inactive product, and the search box only ever saw
// the first page the server sent.
func TestProductFiltersAreAppliedByTheServer(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()
	categoryID := newCategory(t, svc)

	live, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Zz Filter Live", "8901088100002"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	retired, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(categoryID, uomID, "Zz Filter Retired", "8901088100003"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.SetProductActive(ctx, retired.ID, false); err != nil {
		t.Fatalf("retire: %v", err)
	}

	has := func(f domain.ProductFilter, id uuid.UUID) bool {
		t.Helper()
		found, err := svc.ListProducts(ctx, f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, p := range found {
			if p.ID == id {
				return true
			}
		}
		return false
	}

	base := domain.ProductFilter{StoreID: storeID, CategoryID: &categoryID}
	if has(base, retired.ID) {
		t.Error("a retired product appears without being asked for")
	}
	withInactive := base
	withInactive.IncludeInactive = true
	if !has(withInactive, retired.ID) {
		t.Error("include_inactive did not produce the retired product")
	}
	if !has(withInactive, live.ID) {
		t.Error("include_inactive dropped the live product")
	}

	// Search runs in the database, so it reaches a product the caller never
	// received a row for. ILIKE, not LIKE: Postgres's LIKE is case-sensitive
	// where SQLite's was not, so a straight port would have made every product
	// search silently miss.
	search := domain.ProductFilter{StoreID: storeID, Search: "filter live"}
	if !has(search, live.ID) {
		t.Error("a lowercase search did not find a capitalised product name")
	}
	search.Search = "Filter Live"
	if !has(search, live.ID) {
		t.Error("search did not find the product by name")
	}
	search.Search = "8901088100002"
	if !has(search, live.ID) {
		t.Error("search did not find the product by barcode")
	}
}
