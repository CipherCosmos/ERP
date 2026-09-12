package catalogue_test

import (
	"context"
	"strings"
	"testing"
	"time"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/catalogue"

	"github.com/google/uuid"
)

// seedUser puts a real person behind a price change, so the history can be
// checked for the name it shows rather than only for the figures.
func seedUser(t *testing.T, db *platform.DB, storeID uuid.UUID, username string) (uuid.UUID, string) {
	t.Helper()
	// Unique per test: these run in parallel against one database and
	// users.username is unique.
	username += "-" + uuid.NewString()[:8]
	var id uuid.UUID
	// The hash is never verified here — nothing in these tests signs in — but
	// the column is NOT NULL, so it has to be something.
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ($1, 'not-a-usable-hash', 'manager', $2) RETURNING id`,
		username, storeID).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id, username
}

// pricedProduct creates a product with an opening price and returns it.
func pricedProduct(t *testing.T, svc *service.Service, storeID, uomID uuid.UUID,
	name, barcode, sell string, by uuid.UUID) service.CreatedProduct {
	t.Helper()
	p, err := svc.CreateProduct(context.Background(), service.CreateProductInput{
		Product: productInput(newCategory(t, svc), uomID, name, barcode),
		StoreID: storeID,
		UserID:  by,
		Price: &domain.UnitPrice{
			MRP:                money.MustFromString("60.00"),
			CostPrice:          money.MustFromString("30.00"),
			SellPrice:          money.MustFromString(sell),
			WholesaleSellPrice: money.MustFromString("40.00"),
		},
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return p
}

// The question the table exists to answer: why is this ₹45 when it was ₹42 last
// week. Nothing could answer it before, because every price write replaced the
// only row the system had.
func TestPriceHistoryRecordsEveryChangeAndWhoMadeIt(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, username := seedUser(t, db, storeID, "zz-price-1")
	p := pricedProduct(t, svc, storeID, uomID, "Zz History Atta", "8901077100001", "42.00", userID)

	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID:          p.ID,
		ProductUnitID:      p.DefaultUnit.ID,
		StoreID:            storeID,
		MRP:                money.MustFromString("60.00"),
		CostPrice:          money.MustFromString("33.00"),
		SellPrice:          money.MustFromString("45.00"),
		WholesaleSellPrice: money.MustFromString("41.00"),
	}, domain.PriceAuthor{
		UserID: userID,
		Source: domain.PriceManual,
		Note:   "vendor raised the rate",
	}); err != nil {
		t.Fatalf("raise the price: %v", err)
	}

	history, err := svc.PriceHistory(ctx, p.ID, storeID, p.DefaultUnit.ID, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want 2", len(history))
	}

	// Newest first.
	latest := history[0]
	if !latest.SellPrice.Equal(money.MustFromString("45.00")) {
		t.Errorf("latest sell price = %s, want 45.00", latest.SellPrice)
	}
	if latest.Source != domain.PriceManual {
		t.Errorf("latest source = %q, want manual", latest.Source)
	}
	if latest.Note != "vendor raised the rate" {
		t.Errorf("note = %q — the reason is the point of recording one", latest.Note)
	}
	if latest.ChangedByName != username {
		t.Errorf("changed by %q, want the username", latest.ChangedByName)
	}

	// And it reads as a change, not a state: the previous figures are paired on.
	if latest.Previous == nil {
		t.Fatal("the latest entry has nothing before it")
	}
	if !latest.SellChange().Equal(money.MustFromString("3.00")) {
		t.Errorf("sell change = %s, want 3.00", latest.SellChange())
	}
	if !latest.CostChange().Equal(money.MustFromString("3.00")) {
		t.Errorf("cost change = %s, want 3.00", latest.CostChange())
	}

	// The earliest entry is where the price started, which is a fact rather
	// than a missing predecessor.
	opening := history[1]
	if opening.Previous != nil {
		t.Error("the opening price is paired with something before it")
	}
	if opening.Source != domain.PriceOpening {
		t.Errorf("opening source = %q, want opening", opening.Source)
	}
	if !opening.SellChange().IsZero() {
		t.Errorf("the opening entry reports a change of %s", opening.SellChange())
	}
}

// Re-saving a price that did not move is not a price change. A history full of
// entries that changed nothing is a history nobody reads.
func TestPriceHistoryIgnoresAWriteThatChangesNothing(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, _ := seedUser(t, db, storeID, "zz-price-2")
	p := pricedProduct(t, svc, storeID, uomID, "Zz Unchanged", "8901077100002", "42.00", userID)

	same := domain.UnitPrice{
		ProductID:          p.ID,
		ProductUnitID:      p.DefaultUnit.ID,
		StoreID:            storeID,
		MRP:                money.MustFromString("60.00"),
		CostPrice:          money.MustFromString("30.00"),
		SellPrice:          money.MustFromString("42.00"),
		WholesaleSellPrice: money.MustFromString("40.00"),
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.SetPrice(ctx, same, byHand); err != nil {
			t.Fatalf("re-save %d: %v", i, err)
		}
	}

	history, err := svc.PriceHistory(ctx, p.ID, storeID, p.DefaultUnit.ID, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d entries after three identical saves, want the opening one", len(history))
	}

	// But a change of one paisa in any of the four is a change.
	nudged := same
	nudged.WholesaleSellPrice = money.MustFromString("40.01")
	if _, err := svc.SetPrice(ctx, nudged, byHand); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	history, err = svc.PriceHistory(ctx, p.ID, storeID, p.DefaultUnit.ID, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries after a one-paisa move, want 2", len(history))
	}
}

// Every path that sets a price records one, because the recording happens in
// the same statement as the write. A rule that each caller must remember is a
// rule that lasts until the next caller is written.
func TestEveryPricePathIsRecorded(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, username := seedUser(t, db, storeID, "zz-price-3")

	// 1. Created with an opening price.
	p := pricedProduct(t, svc, storeID, uomID, "Zz Paths", "8901077100003", "42.00", userID)

	// 2. Typed into the price screen.
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID: p.ID, ProductUnitID: p.DefaultUnit.ID, StoreID: storeID,
		MRP:       money.MustFromString("60.00"),
		CostPrice: money.MustFromString("30.00"),
		SellPrice: money.MustFromString("44.00"),
	}, domain.PriceAuthor{UserID: userID, Source: domain.PriceManual}); err != nil {
		t.Fatalf("set price: %v", err)
	}

	// 3. A bulk sheet.
	report, err := svc.ImportProducts(ctx, strings.NewReader(
		"name,category,unit,hsn_code,gst_rate,sell_price,mrp,cost_price\n"+
			"Zz Paths,Groceries,KG,10063010,5,46.00,60.00,30.00\n"),
		service.ImportRequest{StoreID: storeID, Apply: true, UpdateExisting: true, UserID: userID})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.Updated != 1 {
		t.Fatalf("import updated %d, want 1: %v %+v", report.Updated, report.Problems, report.Results)
	}

	history, err := svc.PriceHistory(ctx, p.ID, storeID, p.DefaultUnit.ID, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history has %d entries, want one per path", len(history))
	}
	want := []domain.PriceSource{domain.PriceImport, domain.PriceManual, domain.PriceOpening}
	for i, source := range want {
		if history[i].Source != source {
			t.Errorf("entry %d came from %q, want %q", i, history[i].Source, source)
		}
		if history[i].ChangedByName != username {
			t.Errorf("entry %d was recorded against %q", i, history[i].ChangedByName)
		}
	}
}

// A product sold loose and by the sack has two price series. Reading one alone
// is how somebody concludes the wrong thing about the other, so asking for the
// product without naming a unit returns both.
func TestPriceHistoryCoversEveryUnitOfTheProduct(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, _ := seedUser(t, db, storeID, "zz-price-4")
	p := pricedProduct(t, svc, storeID, uomID, "Zz Two Units", "8901077100004", "45.00", userID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID:     p.ID,
		Label:         "25 kg sack",
		QtyInBase:     money.QuantityFromInt(25),
		IsSellable:    true,
		IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add the sack: %v", err)
	}
	if _, err := svc.SetPrice(ctx, domain.UnitPrice{
		ProductID: p.ID, ProductUnitID: sack.ID, StoreID: storeID,
		CostPrice: money.MustFromString("940.00"),
		SellPrice: money.MustFromString("1050.00"),
	}, byHand); err != nil {
		t.Fatalf("price the sack: %v", err)
	}

	both, err := svc.PriceHistory(ctx, p.ID, storeID, uuid.Nil, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("history has %d entries across both units, want 2", len(both))
	}
	labels := map[string]bool{}
	for _, entry := range both {
		labels[entry.UnitLabel] = true
	}
	if !labels["25 kg sack"] || !labels["Kilogram"] {
		t.Errorf("units in the history = %v, want both", labels)
	}

	// And asking for one unit returns only that one.
	justSack, err := svc.PriceHistory(ctx, p.ID, storeID, sack.ID, 0)
	if err != nil {
		t.Fatalf("read the sack's history: %v", err)
	}
	if len(justSack) != 1 || justSack[0].UnitLabel != "25 kg sack" {
		t.Fatalf("the sack's history = %+v", justSack)
	}
}

// The store-wide view: what moved lately. Entries here are from different
// products, so they are deliberately not paired — the row below one of them is
// not what it used to be, and pairing would invent a comparison.
func TestRecentPriceChangesAreNotPairedAcrossProducts(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, _ := seedUser(t, db, storeID, "zz-price-5")
	pricedProduct(t, svc, storeID, uomID, "Zz Recent A", "8901077100005", "42.00", userID)
	pricedProduct(t, svc, storeID, uomID, "Zz Recent B", "8901077100006", "55.00", userID)

	recent, err := svc.RecentPriceChanges(ctx, storeID, time.Now().Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("read recent: %v", err)
	}
	if len(recent) < 2 {
		t.Fatalf("recent changes = %d, want at least the two just made", len(recent))
	}
	for _, entry := range recent {
		if entry.Previous != nil {
			t.Fatalf("%s was paired with another product's price", entry.UnitLabel)
		}
	}
}

// AsMovements is given a list newest first, so the entry BEFORE a given one is
// the next element. Getting that backwards turns every rise into a fall, which
// looks right until somebody checks a number.
func TestMovementsPairEachEntryWithTheOneBeforeItInTime(t *testing.T) {
	t.Parallel()
	loose := uuid.New()
	newestFirst := []domain.PriceChange{
		{ProductUnitID: loose, SellPrice: money.MustFromString("45.00")},
		{ProductUnitID: loose, SellPrice: money.MustFromString("42.00")},
		{ProductUnitID: loose, SellPrice: money.MustFromString("40.00")},
	}
	movements := domain.AsMovements(newestFirst)

	if !movements[0].SellChange().Equal(money.MustFromString("3.00")) {
		t.Errorf("newest change = %s, want a rise of 3.00", movements[0].SellChange())
	}
	if !movements[1].SellChange().Equal(money.MustFromString("2.00")) {
		t.Errorf("middle change = %s, want a rise of 2.00", movements[1].SellChange())
	}
	if movements[2].Previous != nil {
		t.Error("the oldest entry was given a predecessor")
	}
}

// A history covering a product sold loose and by the sack interleaves both
// series, so the row under the sack's price is usually the loose price. Pairing
// them reports the sack as having risen by the difference between a sack and a
// kilo — a price change that never happened, shown in red beside a real figure.
func TestMovementsDoNotCompareOneUnitWithAnother(t *testing.T) {
	t.Parallel()
	loose, sack := uuid.New(), uuid.New()
	newestFirst := []domain.PriceChange{
		{ProductUnitID: sack, UnitLabel: "25 kg sack", SellPrice: money.MustFromString("1050.00")},
		{ProductUnitID: loose, UnitLabel: "Kilogram", SellPrice: money.MustFromString("45.00")},
		{ProductUnitID: sack, UnitLabel: "25 kg sack", SellPrice: money.MustFromString("1020.00")},
		{ProductUnitID: loose, UnitLabel: "Kilogram", SellPrice: money.MustFromString("42.00")},
	}
	movements := domain.AsMovements(newestFirst)

	if !movements[0].SellChange().Equal(money.MustFromString("30.00")) {
		t.Errorf("the sack moved by %s, want 30.00 against its own previous price",
			movements[0].SellChange())
	}
	if !movements[1].SellChange().Equal(money.MustFromString("3.00")) {
		t.Errorf("the loose price moved by %s, want 3.00", movements[1].SellChange())
	}
	// The earliest entry in each series started there and has nothing before it.
	if movements[2].Previous != nil {
		t.Error("the sack's first price was given a predecessor")
	}
	if movements[3].Previous != nil {
		t.Error("the loose first price was given a predecessor")
	}
}

func TestPriceAuthorRefusesWhatTheColumnWouldRefuse(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	p := pricedProduct(t, svc, storeID, uomID, "Zz Author", "8901077100007", "42.00", uuid.Nil)
	price := domain.UnitPrice{
		ProductID: p.ID, ProductUnitID: p.DefaultUnit.ID, StoreID: storeID,
		SellPrice: money.MustFromString("43.00"),
	}

	if _, err := svc.SetPrice(ctx, price, domain.PriceAuthor{Source: "telepathy"}); err == nil {
		t.Error("a price was set with a source the column does not allow")
	}
	if _, err := svc.SetPrice(ctx, price, domain.PriceAuthor{
		Source: domain.PriceManual,
		Note:   strings.Repeat("x", domain.MaxPriceNoteLength+1),
	}); err == nil {
		t.Error("a price note longer than the limit was accepted")
	}
}

// The same rule through the whole stack, because this is where the mistake was
// actually visible: the product-wide history showed a 25 kg sack as having
// risen by ₹625 when the row under it was the price of a kilo.
func TestProductWideHistoryComparesEachUnitWithItself(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	userID, _ := seedUser(t, db, storeID, "zz-price-6")
	p := pricedProduct(t, svc, storeID, uomID, "Zz Series", "8901077100008", "45.00", userID)

	sack, err := svc.AddProductUnit(ctx, domain.NewProductUnitInput{
		ProductID: p.ID, Label: "25 kg sack", QtyInBase: money.QuantityFromInt(25),
		IsSellable: true, IsPurchasable: true,
	})
	if err != nil {
		t.Fatalf("add the sack: %v", err)
	}
	for _, price := range []struct {
		unit uuid.UUID
		sell string
	}{
		{sack.ID, "1020.00"},
		{sack.ID, "1050.00"},
		{p.DefaultUnit.ID, "47.00"},
	} {
		if _, err := svc.SetPrice(ctx, domain.UnitPrice{
			ProductID: p.ID, ProductUnitID: price.unit, StoreID: storeID,
			CostPrice: money.MustFromString("30.00"),
			SellPrice: money.MustFromString(price.sell),
		}, byHand); err != nil {
			t.Fatalf("price %s: %v", price.sell, err)
		}
	}

	history, err := svc.PriceHistory(ctx, p.ID, storeID, uuid.Nil, 0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	for _, entry := range history {
		if entry.Previous == nil {
			continue
		}
		if entry.Previous.ProductUnitID != entry.ProductUnitID {
			t.Fatalf("%s was compared against %s", entry.UnitLabel, entry.Previous.UnitLabel)
		}
		// Nothing here moved by more than a few tens of rupees within its own
		// series. A change in the hundreds means a sack was compared to a kilo.
		if entry.SellChange().Abs().GreaterThan(money.MustFromString("100.00")) {
			t.Errorf("%s reports a move of %s, which is a comparison across units",
				entry.UnitLabel, entry.SellChange())
		}
	}
}
