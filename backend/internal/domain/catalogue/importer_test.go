package catalogue_test

import (
	"errors"
	"strings"
	"testing"

	catalogue "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
)

func parse(t *testing.T, csv string) catalogue.ImportSheet {
	t.Helper()
	sheet, err := catalogue.ParseImport(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return sheet
}

// The reader has to take the file a shop already has, not a file it was asked
// to produce. This is one row written the way a real export writes it: headings
// in another order under other names, a rupee sign, thousands separators, a
// percent sign on the tax, and Windows line endings.
func TestImportReadsTheSheetAShopAlreadyHas(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "Item Name,Stock Group,UOM,HSN Code,GST Rate (%),Selling Price,Purchase Rate,M.R.P.\r\n"+
		"Sona Masoori Rice,Grocery > Rice,KG,10063020,5 %,\"₹1,050.00\",\"Rs. 940\",\"1,100\"\r\n")

	if len(sheet.Problems) != 0 {
		t.Fatalf("problems: %v", sheet.Problems)
	}
	if len(sheet.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(sheet.Rows))
	}
	row := sheet.Rows[0]
	if row.Name != "Sona Masoori Rice" {
		t.Errorf("name = %q", row.Name)
	}
	if len(row.CategoryPath) != 2 || row.CategoryPath[0] != "Grocery" || row.CategoryPath[1] != "Rice" {
		t.Errorf("category path = %v, want [Grocery Rice]", row.CategoryPath)
	}
	if row.CategoryName() != "Rice" {
		t.Errorf("category = %q, want the leaf", row.CategoryName())
	}
	if !row.GSTRate.Equal(money.RateFromInt(5)) {
		t.Errorf("gst = %s, want 5", row.GSTRate)
	}
	if !row.SellPrice.Equal(money.MustFromString("1050.00")) {
		t.Errorf("sell = %s, want 1050.00 — the rupee sign and the comma are not part of the number", row.SellPrice)
	}
	if !row.CostPrice.Equal(money.MustFromString("940")) {
		t.Errorf("cost = %s, want 940", row.CostPrice)
	}
	if !row.MRP.Equal(money.MustFromString("1100")) {
		t.Errorf("mrp = %s, want 1100", row.MRP)
	}
	if !row.HasPrice {
		t.Error("the row carried three prices and does not say so")
	}
}

// Excel writes a byte-order mark in front of the first heading. Left in, it
// becomes part of that heading, and the import fails saying the name column is
// missing while the reader is looking straight at it.
func TestImportSurvivesTheExcelByteOrderMark(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "\ufeffname,category,unit,gst_rate,hsn_code\nAtta,Grocery,KG,0,1101\n")
	if len(sheet.Rows) != 1 || sheet.Rows[0].Name != "Atta" {
		t.Fatalf("rows = %+v", sheet.Rows)
	}
}

func TestImportReportsEveryBadRowAtOnce(t *testing.T) {
	t.Parallel()
	sheet := parse(t, `name,category,unit,hsn_code,gst_rate,sell_price,mrp
Good,Grocery,KG,1101,5,40,50
,Grocery,KG,1101,5,40,50
Bad rate,Grocery,KG,1101,seven,40,50
Bad price,Grocery,KG,1101,5,"not money",50
Above MRP,Grocery,KG,1101,5,60,50
No category,,KG,1101,5,40,50
`)
	// Five bad rows, one good one, and the reader got to the end.
	if len(sheet.Rows) != 1 {
		t.Errorf("rows = %d, want the one good row", len(sheet.Rows))
	}
	if len(sheet.Problems) != 5 {
		t.Fatalf("problems = %d, want 5:\n%v", len(sheet.Problems), sheet.Problems)
	}
	// Each problem names the line as a spreadsheet numbers it, so it can be
	// found. Line 1 is the header.
	wantLines := []int{3, 4, 5, 6, 7}
	for i, p := range sheet.Problems {
		if p.Line != wantLines[i] {
			t.Errorf("problem %d is on line %d, want %d (%s)", i, p.Line, wantLines[i], p.Message)
		}
	}
	// And it quotes the heading the file used, not the canonical one.
	if sheet.Problems[1].Column != "gst_rate" {
		t.Errorf("column = %q", sheet.Problems[1].Column)
	}
	// Selling above MRP is an offence under the Legal Metrology rules, so it is
	// a refusal rather than a warning.
	if !strings.Contains(strings.ToLower(sheet.Problems[3].Message), "mrp") {
		t.Errorf("the above-MRP row said %q", sheet.Problems[3].Message)
	}
	// And every message reads as a sentence about the sheet. The sentinel's own
	// words in front of it — "validation failed: selling price 35 is above the
	// MRP 33" — are two words that say nothing in a column headed "What is
	// wrong", repeated on every line of the report.
	for _, p := range sheet.Problems {
		if strings.HasPrefix(p.Message, catalogue.ErrValidation.Error()) {
			t.Errorf("message %q still carries the sentinel in front of it", p.Message)
		}
	}
}

func TestImportQuotesTheHeadingTheFileUsed(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "Item Name,Stock Group,UOM,HSN,GST Rate (%)\nThing,Grocery,KG,1101,nonsense\n")
	if len(sheet.Problems) != 1 {
		t.Fatalf("problems = %v", sheet.Problems)
	}
	if sheet.Problems[0].Column != "GST Rate (%)" {
		t.Errorf("column = %q, want the heading as the sheet spells it", sheet.Problems[0].Column)
	}
	if got := sheet.Problems[0].String(); !strings.Contains(got, "line 2") {
		t.Errorf("rendered = %q, want the line number in it", got)
	}
}

// A duplicate inside the file has to be caught here. The unique index would
// report one collision and stop, halfway through a sheet the shopkeeper then
// cannot reason about.
func TestImportCatchesDuplicatesInsideTheFile(t *testing.T) {
	t.Parallel()
	sheet := parse(t, `name,category,unit,hsn_code,gst_rate,barcode
Atta 1kg,Grocery,KG,1101,0,8901001100001
Atta One Kilo,Grocery,KG,1101,0,8901001100001
Atta 1kg,Grocery,KG,1101,0,
`)
	if len(sheet.Rows) != 1 {
		t.Errorf("rows = %d, want only the first", len(sheet.Rows))
	}
	if len(sheet.Problems) != 2 {
		t.Fatalf("problems = %v", sheet.Problems)
	}
	if !strings.Contains(sheet.Problems[0].Message, "line 2") {
		t.Errorf("the repeated barcode said %q, want it to point at the first one", sheet.Problems[0].Message)
	}
	if !strings.Contains(sheet.Problems[1].Message, "line 2") {
		t.Errorf("the repeated name said %q, want it to point at the first one", sheet.Problems[1].Message)
	}
}

func TestImportRefusesASheetItCannotUse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, csv, want string
	}{
		{"empty file", "", "empty"},
		{"header only", "name,category,unit,gst_rate,hsn_code\n", "no rows"},
		{"no unit column", "name,category,gst_rate,hsn_code\nAtta,Grocery,0,1101\n", "unit"},
		{"nothing recognised", "col1,col2\na,b\n", "category, gst_rate, hsn_code (or sac_code), name, unit"},
		{"no classification", "name,category,unit,gst_rate\nAtta,Grocery,KG,0\n", "hsn_code (or sac_code)"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := catalogue.ParseImport(strings.NewReader(tc.csv))
			if !errors.Is(err, catalogue.ErrValidation) {
				t.Fatalf("err = %v, want a validation error", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A sheet that does not carry a column must not be read as carrying it empty.
// This is what stops a five-column price list blanking every barcode and brand
// in the shop.
func TestImportSheetSaysWhichColumnsItHad(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "name,category,unit,hsn_code,gst_rate,Selling Price\nAtta,Grocery,KG,1101,0,45\n")
	for _, present := range []string{"name", "category", "unit", "gst_rate", "hsn_code", "sell_price"} {
		if !sheet.Has(present) {
			t.Errorf("sheet says it has no %s column", present)
		}
	}
	for _, absent := range []string{"barcode", "brand", "min_stock", "mrp", "cost_price"} {
		if sheet.Has(absent) {
			t.Errorf("sheet claims a %s column it does not have", absent)
		}
	}
	if !sheet.HasAnyPriceColumn() {
		t.Error("a sheet with a selling price says it sets no prices")
	}
}

func TestImportSkipsTheBlankRowsExcelAppends(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "name,category,unit,hsn_code,gst_rate\nAtta,Grocery,KG,1101,0\n,,,,\n,,,,\n")
	if len(sheet.Rows) != 1 || len(sheet.Problems) != 0 {
		t.Fatalf("rows = %d, problems = %v", len(sheet.Rows), sheet.Problems)
	}
}

// An empty GST cell is nil-rated, not missing. Unbranded flour, fresh
// vegetables and milk are all zero, and they are most of a grocery's lines.
func TestImportTreatsABlankTaxCellAsNilRated(t *testing.T) {
	t.Parallel()
	sheet := parse(t, "name,category,unit,hsn_code,gst_rate\nTomatoes,Vegetables,KG,0702,\n")
	if len(sheet.Problems) != 0 {
		t.Fatalf("problems = %v", sheet.Problems)
	}
	if !sheet.Rows[0].GSTRate.IsZero() {
		t.Errorf("gst = %s, want nil-rated", sheet.Rows[0].GSTRate)
	}
}

func TestImportReadsYesAndNoTheWayPeopleWriteThem(t *testing.T) {
	t.Parallel()
	sheet := parse(t, `name,category,unit,hsn_code,gst_rate,sold_loose,expires
A,Grocery,KG,1101,0,yes,Y
B,Grocery,KG,1101,0,NO,false
C,Grocery,KG,1101,0,1,0
D,Grocery,KG,1101,0,maybe,no
`)
	if len(sheet.Rows) != 3 {
		t.Fatalf("rows = %d, want 3; problems %v", len(sheet.Rows), sheet.Problems)
	}
	want := []struct{ loose, expires bool }{{true, true}, {false, false}, {true, false}}
	for i, w := range want {
		if sheet.Rows[i].AllowDecimal != w.loose || sheet.Rows[i].TracksExpiry != w.expires {
			t.Errorf("row %d = loose %v expires %v, want %v %v",
				i, sheet.Rows[i].AllowDecimal, sheet.Rows[i].TracksExpiry, w.loose, w.expires)
		}
	}
	if len(sheet.Problems) != 1 || !strings.Contains(sheet.Problems[0].Message, "maybe") {
		t.Errorf("problems = %v, want the unreadable answer named", sheet.Problems)
	}
}

// The classification rule is the product constructor's, applied here, so an
// import cannot put a product in the catalogue that the product form refuses.
func TestImportAppliesTheSameClassificationRuleAsTheProductForm(t *testing.T) {
	t.Parallel()
	sheet := parse(t, `name,category,unit,gst_rate,hsn_code,sac_code
Both,Grocery,KG,0,10063020,998313
Short,Grocery,KG,0,100,
Letters,Grocery,KG,0,ABCD,
Fine,Grocery,KG,0,10063020,
`)
	if len(sheet.Rows) != 1 || sheet.Rows[0].Name != "Fine" {
		t.Fatalf("rows = %+v", sheet.Rows)
	}
	if len(sheet.Problems) != 3 {
		t.Fatalf("problems = %v", sheet.Problems)
	}
}

// The template has to be a file the reader accepts. It is the answer to "what
// are the columns called", and a template that does not import is worse than
// none.
func TestImportTemplateParsesCleanly(t *testing.T) {
	t.Parallel()
	sheet := parse(t, catalogue.ImportTemplate)
	if len(sheet.Problems) != 0 {
		t.Fatalf("the template does not import cleanly: %v", sheet.Problems)
	}
	if len(sheet.Rows) != 2 {
		t.Fatalf("rows = %d, want the two worked examples", len(sheet.Rows))
	}
	// One packaged with an MRP and a barcode, one weighed out with neither.
	if sheet.Rows[0].Barcode == "" || sheet.Rows[0].MRP.IsZero() {
		t.Error("the first example is meant to be a packaged item")
	}
	if sheet.Rows[1].Barcode != "" || !sheet.Rows[1].AllowDecimal {
		t.Error("the second example is meant to be something weighed out of a sack")
	}
}

func TestImportRefusesMoreRowsThanOneTransactionShouldHold(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("name,category,unit,gst_rate,hsn_code\n")
	for i := 0; i <= catalogue.MaxImportRows; i++ {
		b.WriteString("Item ")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString(itoa(i))
		b.WriteString(",Grocery,KG,1101,0\n")
	}
	_, err := catalogue.ParseImport(strings.NewReader(b.String()))
	if !errors.Is(err, catalogue.ErrValidation) || !strings.Contains(err.Error(), "split the sheet") {
		t.Fatalf("err = %v, want a refusal naming the remedy", err)
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

// The round trip is the whole point of the export: a shop's real price revision
// is export, edit in a spreadsheet, import. If the file that comes out is not a
// file that goes back in, none of that works — and the two halves share a row
// type and a column table precisely so it cannot quietly stop working.
func TestExportedSheetImportsBackUnchanged(t *testing.T) {
	t.Parallel()
	shelfLife := 270
	original := []catalogue.ImportRow{
		{
			Name: "Parle-G Biscuits 200g", NameHi: "पारले-जी", NameKn: "ಪಾರ್ಲೆ-ಜಿ",
			CategoryPath: []string{"Grocery", "Biscuits"},
			Unit:         "NOS", Barcode: "8901063010123",
			HSNCode: "19053100", GSTRate: money.RateFromInt(18),
			Brand: "Parle", Description: "glucose biscuit",
			MinStock: money.QuantityFromInt(24), AllowDecimal: false,
			TracksExpiry: true, ShelfLifeDays: &shelfLife,
			HasPrice: true,
			MRP:      money.MustFromString("30.00"), CostPrice: money.MustFromString("24.50"),
			SellPrice: money.MustFromString("30.00"), WholesaleSellPrice: money.MustFromString("28.00"),
		},
		{
			// The other shape: weighed out of a sack, no barcode, no MRP, and a
			// name with a comma and a quotation mark in it — which is what the
			// CSV writer has to quote and the reader has to give back intact.
			Name:         `Rice, "Sona Masoori"`,
			CategoryPath: []string{"Grocery", "Rice"},
			Unit:         "KG",
			SACCode:      "",
			HSNCode:      "10063020", GSTRate: money.RateFromInt(0),
			MinStock: money.MustQuantityFromString("50.500"), AllowDecimal: true,
			HasPrice:  true,
			CostPrice: money.MustFromString("52.00"), SellPrice: money.MustFromString("64.00"),
		},
	}

	var buf strings.Builder
	if err := catalogue.WriteExport(&buf, original); err != nil {
		t.Fatalf("write export: %v", err)
	}

	sheet, err := catalogue.ParseImport(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("read the export back: %v\n%s", err, buf.String())
	}
	if len(sheet.Problems) != 0 {
		t.Fatalf("the export does not import cleanly: %v\n%s", sheet.Problems, buf.String())
	}
	if len(sheet.Rows) != len(original) {
		t.Fatalf("rows = %d, want %d", len(sheet.Rows), len(original))
	}

	for i, want := range original {
		got := sheet.Rows[i]
		if got.Name != want.Name || got.NameHi != want.NameHi || got.NameKn != want.NameKn {
			t.Errorf("row %d names = %q/%q/%q, want %q/%q/%q",
				i, got.Name, got.NameHi, got.NameKn, want.Name, want.NameHi, want.NameKn)
		}
		if strings.Join(got.CategoryPath, ">") != strings.Join(want.CategoryPath, ">") {
			t.Errorf("row %d category = %v, want %v", i, got.CategoryPath, want.CategoryPath)
		}
		if got.Unit != want.Unit || got.Barcode != want.Barcode {
			t.Errorf("row %d unit/barcode = %q/%q, want %q/%q", i, got.Unit, got.Barcode, want.Unit, want.Barcode)
		}
		if got.HSNCode != want.HSNCode || got.SACCode != want.SACCode {
			t.Errorf("row %d classification = %q/%q, want %q/%q",
				i, got.HSNCode, got.SACCode, want.HSNCode, want.SACCode)
		}
		if !got.GSTRate.Equal(want.GSTRate) {
			t.Errorf("row %d gst = %s, want %s", i, got.GSTRate, want.GSTRate)
		}
		if got.Brand != want.Brand || got.Description != want.Description {
			t.Errorf("row %d brand/description = %q/%q", i, got.Brand, got.Description)
		}
		if !got.MinStock.Equal(want.MinStock) {
			t.Errorf("row %d min stock = %s, want %s", i, got.MinStock, want.MinStock)
		}
		if got.AllowDecimal != want.AllowDecimal || got.TracksExpiry != want.TracksExpiry {
			t.Errorf("row %d flags = %v/%v, want %v/%v",
				i, got.AllowDecimal, got.TracksExpiry, want.AllowDecimal, want.TracksExpiry)
		}
		switch {
		case want.ShelfLifeDays == nil && got.ShelfLifeDays != nil:
			t.Errorf("row %d gained a shelf life of %d", i, *got.ShelfLifeDays)
		case want.ShelfLifeDays != nil && got.ShelfLifeDays == nil:
			t.Errorf("row %d lost its shelf life", i)
		case want.ShelfLifeDays != nil && *got.ShelfLifeDays != *want.ShelfLifeDays:
			t.Errorf("row %d shelf life = %d, want %d", i, *got.ShelfLifeDays, *want.ShelfLifeDays)
		}
		for _, money := range []struct {
			name       string
			got, want_ interface{ String() string }
		}{
			{"mrp", got.MRP, want.MRP},
			{"cost", got.CostPrice, want.CostPrice},
			{"sell", got.SellPrice, want.SellPrice},
			{"wholesale", got.WholesaleSellPrice, want.WholesaleSellPrice},
		} {
			if money.got.String() != money.want_.String() {
				t.Errorf("row %d %s = %s, want %s", i, money.name, money.got, money.want_)
			}
		}
	}
}

// The export writes the answers its own reader accepts. A "true"/"false" or a
// "1"/"0" would also round-trip, but the column is one a person types in next,
// and they copy the row above.
func TestExportWritesYesAndNo(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	err := catalogue.WriteExport(&buf, []catalogue.ImportRow{{
		Name: "Loose thing", CategoryPath: []string{"Grocery"}, Unit: "KG",
		HSNCode: "1101", AllowDecimal: true, TracksExpiry: false,
	}})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(buf.String(), ",yes,") || !strings.Contains(buf.String(), ",no,") {
		t.Errorf("export wrote:\n%s", buf.String())
	}
}

// Every column the export writes must be one the import recognises, mapped to
// the field whose name it carries.
//
// This is the check that keeps the two halves together, and the failure it
// catches already happened once: the export wrote a column headed
// "description", which this reader takes for the product NAME — because a sheet
// from another system calls the name column that — so every exported
// description was read as a second name column and silently dropped.
func TestEveryExportedColumnIsOneTheImportUnderstands(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := catalogue.WriteExport(&buf, []catalogue.ImportRow{{
		Name: "Anything", CategoryPath: []string{"Grocery"}, Unit: "KG", HSNCode: "1101",
	}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	header := strings.Split(strings.SplitN(buf.String(), "\n", 2)[0], ",")

	sheet, err := catalogue.ParseImport(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("the export does not parse: %v", err)
	}

	seen := map[string]bool{}
	for _, column := range header {
		if seen[column] {
			t.Errorf("the export writes %q twice", column)
		}
		seen[column] = true
		// Has() is keyed on the canonical name of the field the heading
		// resolved to. A heading that landed on a different field — or on no
		// field at all — does not come back under its own name.
		if !sheet.Has(column) {
			t.Errorf("the export writes a column headed %q that the import does not read as %q", column, column)
		}
	}
	for column := range sheet.Columns {
		if !seen[column] {
			t.Errorf("the import found a %q column the export did not write", column)
		}
	}
}
