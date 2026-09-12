package catalogue_test

import (
	"context"
	"strings"
	"testing"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	service "gbos/internal/service/catalogue"

	"github.com/google/uuid"
)

func importCSV(t *testing.T, svc *service.Service, csv string, req service.ImportRequest) service.ImportReport {
	t.Helper()
	report, err := svc.ImportProducts(context.Background(), strings.NewReader(csv), req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return report
}

// The whole point of the feature: a sheet a shop already has, applied in one
// go, with the categories it names brought into existence along the way.
func TestImportCreatesProductsAndTheCategoriesTheyNeed(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)
	ctx := context.Background()

	csv := `Item Name,Stock Group,UOM,HSN Code,GST Rate (%),M.R.P.,Purchase Rate,Selling Price,Barcode
Parle-G 200g,Zz Import > Biscuits,PCS,19053100,18,30,24.50,30,8901063099001
Marie Gold 250g,Zz Import > Biscuits,PCS,19053100,18,40,32,40,8901063099002
Sona Masoori Rice,Zz Import > Rice,KG,10063020,0,0,52,64,
`
	report := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID, Apply: true})

	if !report.Applied {
		t.Fatalf("not applied; problems %v results %+v", report.Problems, report.Results)
	}
	if report.Created != 3 || report.Updated != 0 || report.Failed != 0 {
		t.Fatalf("created %d updated %d failed %d, want 3/0/0", report.Created, report.Updated, report.Failed)
	}
	// Three levels named, three categories made: the parent once, both children.
	if report.Categories != 3 {
		t.Errorf("categories created = %d, want 3", report.Categories)
	}
	if report.PricesSet != 3 {
		t.Errorf("prices set = %d, want 3", report.PricesSet)
	}

	// The tree, not a flat list: the sheet said "Zz Import > Biscuits" and that
	// is what came out.
	categories, err := svc.ListCategories(ctx, false)
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	var biscuits domain.Category
	for _, c := range categories {
		if c.Name == "Biscuits" && c.ParentName == "Zz Import" {
			biscuits = c
		}
	}
	if biscuits.ID == uuid.Nil {
		t.Fatal("Biscuits was not created under Zz Import")
	}
	if biscuits.ProductCount != 2 {
		t.Errorf("Biscuits holds %d products, want 2", biscuits.ProductCount)
	}

	// A product is sellable the moment it exists: it has a default unit, and the
	// price in the sheet is attached to it.
	products, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID, CategoryID: &biscuits.ID})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("products = %d, want 2", len(products))
	}
	for _, p := range products {
		if p.SellPrice.IsZero() {
			t.Errorf("%s has no selling price", p.Name)
		}
	}
}

// A dry run is the real thing with the transaction rolled back, so what it
// reports is what would happen — including anything the database itself would
// have refused.
func TestImportDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	svc, db, storeID, _ := newService(t)
	ctx := context.Background()

	csv := `name,category,unit,hsn_code,gst_rate,sell_price
Zz Dry Run Item,Zz Dry Run,KG,10063020,5,45
`
	report := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID})

	if report.Applied {
		t.Fatal("a dry run reported itself as applied")
	}
	if report.Created != 1 || report.Categories != 1 {
		t.Errorf("dry run said created %d categories %d, want 1 and 1", report.Created, report.Categories)
	}
	if len(report.NewCategories) != 1 || report.NewCategories[0] != "Zz Dry Run" {
		t.Errorf("new categories = %v, want it to name the one it would make", report.NewCategories)
	}

	var products, categories int
	if err := db.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM products WHERE name = 'Zz Dry Run Item'),
		        (SELECT count(*) FROM categories WHERE name = 'Zz Dry Run')`).Scan(&products, &categories); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if products != 0 || categories != 0 {
		t.Errorf("after a dry run the database holds %d products and %d categories, want none", products, categories)
	}
}

// One bad row stops the whole sheet. A partial import is worse than a refused
// one: the sheet and the catalogue then disagree in a way nobody can see.
func TestImportIsAllOrNothing(t *testing.T) {
	t.Parallel()
	svc, db, storeID, _ := newService(t)
	ctx := context.Background()

	csv := `name,category,unit,hsn_code,gst_rate,sell_price
Zz Good One,Zz AllOrNothing,KG,10063020,5,45
Zz Bad One,Zz AllOrNothing,KG,10063020,not a rate,45
`
	report := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID, Apply: true})

	if report.Applied {
		t.Fatal("a sheet with a bad row was applied")
	}
	if len(report.Problems) != 1 {
		t.Fatalf("problems = %v", report.Problems)
	}
	// The good row is still reported on, so the shopkeeper sees the whole
	// picture rather than only the failure.
	if report.Created != 1 {
		t.Errorf("the good row was not reported: created = %d", report.Created)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM products WHERE name = 'Zz Good One'`).Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 0 {
		t.Error("the good row landed even though the sheet was refused")
	}
}

// Re-running the same sheet must not double the catalogue. By default a product
// already there is left alone.
func TestImportSkipsWhatIsAlreadyThere(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)

	csv := `name,category,unit,hsn_code,gst_rate,sell_price,barcode
Zz Repeat Item,Zz Repeat,KG,10063020,5,45,8901063099101
`
	first := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID, Apply: true})
	if first.Created != 1 {
		t.Fatalf("first run created %d, want 1", first.Created)
	}

	second := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID, Apply: true})
	if second.Created != 0 || second.Skipped != 1 {
		t.Fatalf("second run created %d skipped %d, want 0 and 1", second.Created, second.Skipped)
	}
	if second.Results[0].MatchedOn != "barcode" {
		t.Errorf("matched on %q, want barcode", second.Results[0].MatchedOn)
	}
	if second.Categories != 0 {
		t.Errorf("second run created %d categories, want none", second.Categories)
	}
}

// The rule that makes a price-list upload safe: a sheet without a column must
// not blank the field that column would have carried.
func TestImportUpdateLeavesColumnsTheSheetDoesNotCarry(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)
	ctx := context.Background()

	full := `name,category,unit,hsn_code,gst_rate,barcode,brand,min_stock,mrp,cost_price,sell_price
Zz Merge Item,Zz Merge,KG,10063020,5,8901063099201,Daawat,12,60,40,50
`
	if r := importCSV(t, svc, full, service.ImportRequest{StoreID: storeID, Apply: true}); r.Created != 1 {
		t.Fatalf("setup: created %d, problems %v", r.Created, r.Problems)
	}

	// Now a price list: five columns, no barcode, no brand, no reorder level,
	// no MRP, no cost.
	priceList := `name,category,unit,hsn_code,gst_rate,sell_price
Zz Merge Item,Zz Merge,KG,10063020,5,55
`
	report := importCSV(t, svc, priceList,
		service.ImportRequest{StoreID: storeID, Apply: true, UpdateExisting: true})
	if report.Updated != 1 {
		t.Fatalf("updated %d, want 1; problems %v results %+v", report.Updated, report.Problems, report.Results)
	}

	products, err := svc.ListProducts(ctx, domain.ProductFilter{StoreID: storeID, Search: "Zz Merge Item"})
	if err != nil || len(products) != 1 {
		t.Fatalf("read back: %v, %d products", err, len(products))
	}
	p := products[0]
	if p.Barcode != "8901063099201" {
		t.Errorf("barcode = %q — a sheet without the column blanked it", p.Barcode)
	}
	if p.Brand != "Daawat" {
		t.Errorf("brand = %q — a sheet without the column blanked it", p.Brand)
	}
	if !p.MinStock.Equal(money.QuantityFromInt(12)) {
		t.Errorf("min stock = %s — a sheet without the column blanked it", p.MinStock)
	}
	if !p.SellPrice.Equal(money.MustFromString("55")) {
		t.Errorf("sell price = %s, want the new 55", p.SellPrice)
	}
	// And the MRP is still there, which matters: a zero MRP means "not a
	// packaged commodity" and switches off the rule that stops the shop selling
	// above it.
	if !p.MRP.Equal(money.MustFromString("60")) {
		t.Errorf("mrp = %s, want the 60 the sheet did not mention", p.MRP)
	}
	if !p.CostPrice.Equal(money.MustFromString("40")) {
		t.Errorf("cost = %s, want the 40 the sheet did not mention", p.CostPrice)
	}
}

// A row naming a unit that does not exist is the row's problem, not the
// sheet's. It is reported against its line and the rest of the sheet is still
// checked.
func TestImportReportsAnUnknownUnitAgainstItsRow(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)

	csv := `name,category,unit,hsn_code,gst_rate
Zz Fine,Zz Units,KG,10063020,5
Zz Broken,Zz Units,furlongs,10063020,5
`
	report := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID, Apply: true})
	if report.Applied {
		t.Fatal("a sheet with a bad unit was applied")
	}
	if report.Created != 1 || report.Failed != 1 {
		t.Fatalf("created %d failed %d, want 1 and 1: %+v", report.Created, report.Failed, report.Results)
	}
	bad := report.Results[1]
	if bad.Line != 3 || !strings.Contains(bad.Message, "furlongs") {
		t.Errorf("the failure said %q on line %d, want it to quote the unit", bad.Message, bad.Line)
	}
	if !strings.Contains(bad.Message, "Units screen") {
		t.Errorf("the failure said %q, want it to say where the unit names come from", bad.Message)
	}
}

// A unit is resolved by code, by name, or by what another system prints for the
// same idea. A shop whose sheet says PCS is not using a different unit from the
// one this system calls NOS, and refusing the import over vocabulary refuses
// the shop.
func TestImportResolvesAUnitByCodeNameOrSynonym(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)

	csv := `name,category,unit,hsn_code,gst_rate
Zz By Code,Zz Unit Names,KG,10063020,5
Zz By Name,Zz Unit Names,Kilogram,10063020,5
Zz Lower,Zz Unit Names,kg,10063020,5
Zz Pieces,Zz Unit Names,PCS,10063020,5
Zz Each,Zz Unit Names,Each,10063020,5
Zz Kgs,Zz Unit Names,Kgs,10063020,5
Zz Litres,Zz Unit Names,Ltr,10063020,5
`
	report := importCSV(t, svc, csv, service.ImportRequest{StoreID: storeID})
	if report.Created != 7 || report.Failed != 0 {
		t.Fatalf("created %d failed %d: %+v", report.Created, report.Failed, report.Results)
	}
}

// Every synonym has to point at a unit the migration actually seeds. One that
// does not is not harmful — the row fails the same way it would have — but it
// is a promise in a lookup table that nothing keeps, and the table is exactly
// the kind of thing that rots quietly.
func TestEverySynonymNamesARealUnit(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)

	var csv strings.Builder
	csv.WriteString("name,category,unit,hsn_code,gst_rate\n")
	for i, spelling := range service.UnitSynonymSpellings() {
		csv.WriteString("Zz Synonym ")
		csv.WriteString(spelling)
		csv.WriteString(" ")
		csv.WriteString(itoa(i))
		csv.WriteString(",Zz Synonyms,")
		csv.WriteString(spelling)
		csv.WriteString(",10063020,5\n")
	}

	report := importCSV(t, svc, csv.String(), service.ImportRequest{StoreID: storeID})
	if report.Failed != 0 {
		for _, r := range report.Results {
			if r.Outcome == service.ImportFailed {
				t.Errorf("%s", r.Message)
			}
		}
		t.Fatalf("%d synonyms name a unit that does not exist", report.Failed)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// A sheet that prices things needs a store to price them in. Saying so is
// better than importing two thousand products with no prices and leaving the
// shopkeeper to find out at the till.
func TestImportRefusesPricesWithNoStore(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)

	_, err := svc.ImportProducts(context.Background(),
		strings.NewReader("name,category,unit,hsn_code,gst_rate,sell_price\nA,Zz,KG,10063020,5,45\n"),
		service.ImportRequest{})
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("err = %v, want a refusal naming the store", err)
	}
}

// A sheet may legitimately carry no prices at all: a shop bringing in its item
// list first and pricing it afterwards.
func TestImportAcceptsASheetWithNoPrices(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)

	report := importCSV(t, svc,
		"name,category,unit,hsn_code,gst_rate\nZz No Price,Zz NoPrice,KG,10063020,5\n",
		service.ImportRequest{StoreID: storeID, Apply: true})
	if report.Created != 1 || report.PricesSet != 0 {
		t.Fatalf("created %d prices %d, want 1 and 0", report.Created, report.PricesSet)
	}
}
