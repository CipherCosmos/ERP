package catalogue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Bulk catalogue import.
//
// This is what decides whether a shop with two thousand items can start at all.
// Entering them one at a time through the product form is roughly two working
// weeks, and nobody does it — they keep using the old system, or they enter the
// four hundred items they sell most and the rest never get a barcode.
//
// Three decisions shape everything below.
//
// It is all or nothing. Every row is checked before any row is written, and the
// write is one transaction. A partial import is worse than a refused one,
// because the sheet and the catalogue then disagree in a way nobody can see:
// you cannot tell by looking which four hundred of two thousand rows landed.
//
// It reports every problem, not the first. A sheet is corrected in a
// spreadsheet, in one pass. Returning one error per upload turns a ten-minute
// correction into an afternoon.
//
// It refuses to guess at a price. A row with no money columns is a catalogue
// entry with no price, which is legitimate. A row priced at zero is a product
// given away, which is also legitimate. The two are not the same thing and the
// reader does not conflate them.

// ImportRequest is one upload.
type ImportRequest struct {
	// StoreID is where the prices in the sheet apply. Required when the sheet
	// carries any price column: a price without a store has nowhere to live.
	StoreID uuid.UUID
	// UpdateExisting allows the import to change products already in the
	// catalogue. It is off by default, and deliberately so — a shop that
	// uploads last month's sheet by mistake should add nothing and change
	// nothing, not silently roll two thousand prices backwards.
	UpdateExisting bool
	// Apply writes. Without it the whole thing runs and reports, and the
	// transaction is rolled back — which is how the shopkeeper sees what an
	// import would do before it does it.
	Apply bool
}

// ImportOutcome is what happened to one row.
type ImportOutcome string

const (
	ImportCreated ImportOutcome = "created"
	ImportUpdated ImportOutcome = "updated"
	// ImportSkipped is a row matching a product already in the catalogue when
	// the caller did not ask for updates.
	ImportSkipped ImportOutcome = "skipped"
	ImportFailed  ImportOutcome = "failed"
)

// ImportRowResult says what became of one line of the sheet.
type ImportRowResult struct {
	Line    int           `json:"line"`
	Name    string        `json:"name"`
	Outcome ImportOutcome `json:"outcome"`
	// MatchedOn says how an existing product was recognised — "barcode" or
	// "name", so a surprising match can be understood rather than guessed at.
	MatchedOn string     `json:"matched_on,omitempty"`
	ProductID *uuid.UUID `json:"product_id,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// ImportReport is the whole answer: what would happen, or what did.
type ImportReport struct {
	// Applied is false for a dry run. Everything else in the report means the
	// same thing either way, which is the point: a dry run is the real thing
	// with the transaction rolled back, not a separate code path that might
	// disagree with it.
	Applied bool `json:"applied"`

	Rows       int `json:"rows"`
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
	PricesSet  int `json:"prices_set"`
	Categories int `json:"categories_created"`

	// NewCategories names the categories this import would create, because
	// "4 categories" tells a shopkeeper nothing about whether they are the four
	// they meant or four typos of ones they already have.
	NewCategories []string `json:"new_categories,omitempty"`

	// Problems are the parse-time faults: a cell that is not a number, a
	// duplicate barcode inside the file.
	Problems []domain.ImportProblem `json:"problems,omitempty"`
	// Results carries one entry per row that got as far as being considered.
	Results []ImportRowResult `json:"results"`
}

// OK reports whether the sheet is clean enough to apply.
func (r ImportReport) OK() bool { return len(r.Problems) == 0 && r.Failed == 0 }

// unitSynonyms maps what other systems print onto the codes this one seeds.
// Keys are lowercase, matching the lookup.
var unitSynonyms = map[string]string{
	"pcs": "NOS", "pc": "NOS", "piece": "NOS", "pieces": "NOS",
	"ea": "NOS", "each": "NOS", "unit": "NOS", "units": "NOS",
	"no": "NOS", "no.": "NOS", "nos.": "NOS", "number": "NOS",
	"kgs": "KG", "kilo": "KG", "kilos": "KG", "kilogram": "KG", "kilograms": "KG", "kg.": "KG",
	"gm": "G", "gms": "G", "gram": "G", "grams": "G",
	"ltr": "L", "ltrs": "L", "litre": "L", "litres": "L", "liter": "L", "liters": "L",
	"mtr": "M", "mtrs": "M", "metre": "M", "metres": "M", "meter": "M", "meters": "M",
	"dozens": "DOZ", "dzn": "DOZ", "dozen": "DOZ",
	"tonne": "TON", "tonnes": "TON", "mt": "TON",
	"quintal": "QTL", "qtls": "QTL",
	"sqft": "SQFT", "sq.ft": "SQFT", "sqfeet": "SQFT",
	"sqm": "SQM", "sq.m": "SQM",
	"feet": "FT", "foot": "FT", "inch": "IN", "inches": "IN",
}

// Every value above must be a code the units migration seeds. A synonym
// pointing at a unit that does not exist is not harmful — the second lookup
// misses and the row fails the same way — but it is a lie in the table, and
// the test below holds it to the seeded set.

// UnitSynonymSpellings lists the alternative spellings this import accepts, so
// a test can hold every one of them to a unit that actually exists.
func UnitSynonymSpellings() []string {
	spellings := make([]string, 0, len(unitSynonyms))
	for spelling := range unitSynonyms {
		spellings = append(spellings, spelling)
	}
	sort.Strings(spellings)
	return spellings
}

// errDryRun unwinds the transaction after a dry run. It never reaches a caller.
var errDryRun = errors.New("catalogue: dry run")

// ImportProducts reads a catalogue sheet and, if asked, applies it.
//
// The dry run is the same code with the transaction rolled back at the end.
// That is not a shortcut: a preview computed by a different path is a preview
// that can disagree with the import, and the one thing a preview has to be is
// true.
func (s *Service) ImportProducts(ctx context.Context, r io.Reader, req ImportRequest) (ImportReport, error) {
	sheet, err := domain.ParseImport(r)
	if err != nil {
		return ImportReport{}, err
	}
	if sheet.HasAnyPriceColumn() && req.StoreID == uuid.Nil {
		return ImportReport{}, fmt.Errorf("%w: the sheet sets prices, so it needs a store", ErrStoreRequired)
	}

	report := ImportReport{
		Rows:     len(sheet.Rows),
		Problems: sheet.Problems,
		Results:  make([]ImportRowResult, 0, len(sheet.Rows)),
	}
	// A sheet with a bad cell is not applied at all, and is not half-checked
	// either: the rows that parsed are still walked, so the shopkeeper sees the
	// whole picture in one report.
	dryRun := !req.Apply || len(sheet.Problems) > 0

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		run := &importRun{
			svc:        s,
			tx:         tx,
			sheet:      sheet,
			req:        req,
			categories: map[string]uuid.UUID{},
			units:      map[string]*domain.UnitOfMeasure{},
		}
		if err := run.execute(ctx, &report); err != nil {
			return err
		}
		if dryRun || !report.OK() {
			// Everything above ran for real. Undoing it is what makes the
			// report trustworthy: the categories were created, the products
			// were written, the constraints were checked by the database, and
			// then none of it happened.
			return errDryRun
		}
		report.Applied = true
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return ImportReport{}, err
	}
	return report, nil
}

// importRun is the state of one import: the transaction, and the name-to-id
// answers already looked up.
type importRun struct {
	svc   *Service
	tx    platform.Tx
	sheet domain.ImportSheet
	req   ImportRequest

	// categories is keyed by the full path, lowercased, so "Grocery > Flour"
	// and a later "grocery > flour" resolve to the same category rather than
	// creating a second one inside the same import.
	categories map[string]uuid.UUID
	units      map[string]*domain.UnitOfMeasure
}

func (run *importRun) execute(ctx context.Context, report *ImportReport) error {
	for _, row := range run.sheet.Rows {
		result, err := run.row(ctx, row, report)
		if err != nil {
			return err
		}
		switch result.Outcome {
		case ImportCreated:
			report.Created++
		case ImportUpdated:
			report.Updated++
		case ImportSkipped:
			report.Skipped++
		case ImportFailed:
			report.Failed++
		}
		report.Results = append(report.Results, result)
	}
	return nil
}

// row handles one line. A returned error is infrastructural — the database is
// gone — and aborts the import. A row that is merely wrong comes back as a
// failed result, so the rest of the sheet is still reported on.
func (run *importRun) row(ctx context.Context, row domain.ImportRow, report *ImportReport) (ImportRowResult, error) {
	result := ImportRowResult{Line: row.Line, Name: row.Name}
	fail := func(format string, args ...any) ImportRowResult {
		result.Outcome = ImportFailed
		result.Message = fmt.Sprintf(format, args...)
		return result
	}

	categoryID, err := run.category(ctx, row.CategoryPath, report)
	if err != nil {
		if isRowFault(err) {
			return fail("%s", plainMessage(err)), nil
		}
		return result, err
	}

	unit, err := run.unit(ctx, row.Unit)
	if err != nil {
		if isRowFault(err) {
			return fail("%s", plainMessage(err)), nil
		}
		return result, err
	}

	existing, matchedOn, err := run.match(ctx, row, categoryID)
	if err != nil {
		return result, err
	}
	result.MatchedOn = matchedOn

	if existing != nil {
		id := existing.ID
		result.ProductID = &id
		if !run.req.UpdateExisting {
			result.Outcome = ImportSkipped
			result.Message = fmt.Sprintf("already in the catalogue, matched on %s", matchedOn)
			return result, nil
		}
		updated := run.merge(*existing, row, categoryID, unit.ID)
		p, err := domain.NewProduct(updated)
		if err != nil {
			return fail("%s", plainMessage(err)), nil
		}
		p.ID = existing.ID
		p.IsActive = existing.IsActive
		found, err := run.svc.repo.UpdateProduct(ctx, run.tx, p)
		if err != nil {
			if isRowFault(err) {
				return fail("%s", plainMessage(err)), nil
			}
			return result, err
		}
		if !found {
			return fail("the product went away while the import was running"), nil
		}
		result.Outcome = ImportUpdated
		if err := run.price(ctx, existing.ID, row, report); err != nil {
			if isRowFault(err) {
				return fail("%s", plainMessage(err)), nil
			}
			return result, err
		}
		return result, nil
	}

	created, err := run.svc.CreateProductTx(ctx, run.tx, CreateProductInput{
		Product: run.fresh(row, categoryID, unit.ID),
		StoreID: run.req.StoreID,
		Price:   run.priceFor(row),
	})
	if err != nil {
		if isRowFault(err) {
			return fail("%s", plainMessage(err)), nil
		}
		return result, err
	}
	id := created.ID
	result.ProductID = &id
	result.Outcome = ImportCreated
	if row.HasPrice && run.req.StoreID != uuid.Nil {
		report.PricesSet++
	}
	return result, nil
}

// category resolves a path, creating what is missing.
//
// Each level is looked up under the one above, so "Grocery > Flour" finds the
// Flour that sits under Grocery and not a Flour somewhere else in the tree.
func (run *importRun) category(ctx context.Context, path []string, report *ImportReport) (uuid.UUID, error) {
	var parent *uuid.UUID
	var walked []string

	for _, name := range path {
		walked = append(walked, name)
		key := strings.ToLower(strings.Join(walked, ">"))
		if id, ok := run.categories[key]; ok {
			copied := id
			parent = &copied
			continue
		}

		found, ok, err := run.svc.repo.FindCategoryByName(ctx, run.tx, name, parent)
		if err != nil {
			return uuid.Nil, err
		}
		if !ok {
			// The HSN is left blank on a created category. It is a default for
			// the product form, and guessing it from the first product in the
			// sheet would put one product's classification on everything added
			// to that category afterwards.
			fresh, err := domain.NewCategory(name, "", "", "", parent)
			if err != nil {
				return uuid.Nil, err
			}
			if err := run.svc.repo.CreateCategory(ctx, run.tx, fresh); err != nil {
				return uuid.Nil, err
			}
			report.Categories++
			report.NewCategories = append(report.NewCategories, strings.Join(walked, " > "))
			found = fresh
		}
		run.categories[key] = found.ID
		copied := found.ID
		parent = &copied
	}

	if parent == nil {
		return uuid.Nil, domain.Invalid("a product needs a category")
	}
	return *parent, nil
}

func (run *importRun) unit(ctx context.Context, text string) (domain.UnitOfMeasure, error) {
	key := strings.ToLower(text)
	if cached, ok := run.units[key]; ok {
		if cached == nil {
			return domain.UnitOfMeasure{}, domain.Invalid(
				"%q is not a unit — use a code or name from the Units screen", text)
		}
		return *cached, nil
	}

	found, ok, err := run.svc.repo.FindUnitByCodeOrName(ctx, run.tx, text)
	if err != nil {
		return domain.UnitOfMeasure{}, err
	}
	if !ok {
		// A shop's own sheet says PCS, PC, EA or PIECE where this system's
		// seeded unit is called NOS. None of those is a different unit — they
		// are the same idea spelt the way another piece of software spelt it,
		// and refusing them refuses the import over vocabulary.
		//
		// The synonym is tried only after the direct lookup misses, so a shop
		// that has created a unit actually called PCS gets its own.
		if alias, has := unitSynonyms[key]; has {
			found, ok, err = run.svc.repo.FindUnitByCodeOrName(ctx, run.tx, alias)
			if err != nil {
				return domain.UnitOfMeasure{}, err
			}
		}
	}
	if !ok {
		// Cached as a miss too. A sheet of two thousand rows with one misspelt
		// unit would otherwise ask the database two thousand times to be told
		// the same thing.
		run.units[key] = nil
		return domain.UnitOfMeasure{}, domain.Invalid(
			"%q is not a unit — use a code or name from the Units screen", text)
	}
	run.units[key] = &found
	return found, nil
}

// match finds the product this row is about, if the catalogue already has it.
//
// Barcode first, because a barcode is the shop's own identifier and unique
// across the table. Name within category second, because most sheets have no
// barcode column at all. The two are reported separately so a match nobody
// expected can be explained.
func (run *importRun) match(ctx context.Context, row domain.ImportRow, categoryID uuid.UUID) (*domain.Product, string, error) {
	if row.Barcode != "" {
		found, ok, err := run.svc.repo.FindProductByBarcode(ctx, run.tx, row.Barcode)
		if err != nil {
			return nil, "", err
		}
		if ok {
			return &found, "barcode", nil
		}
	}
	found, ok, err := run.svc.repo.FindProductByName(ctx, run.tx, row.Name, categoryID)
	if err != nil {
		return nil, "", err
	}
	if ok {
		return &found, "name", nil
	}
	return nil, "", nil
}

// fresh builds the product a new row describes.
func (run *importRun) fresh(row domain.ImportRow, categoryID, uomID uuid.UUID) domain.NewProductInput {
	return domain.NewProductInput{
		Name:       row.Name,
		NameHi:     row.NameHi,
		NameKn:     row.NameKn,
		CategoryID: categoryID,
		HSNCode:    row.HSNCode,
		SACCode:    row.SACCode,
		Barcode:    row.Barcode,
		UOMID:      uomID,
		// One of the stocking unit. A pack of any other size is a product unit,
		// which this import does not create — see the note on ImportRow.
		BaseUnitQty:   money.QuantityFromInt(1),
		GSTRate:       row.GSTRate,
		AllowDecimal:  row.AllowDecimal,
		MinStock:      row.MinStock,
		Brand:         row.Brand,
		Description:   row.Description,
		TracksExpiry:  row.TracksExpiry,
		ShelfLifeDays: row.ShelfLifeDays,
	}
}

// merge applies a row to a product already in the catalogue, changing only what
// the sheet carried a column for.
//
// This is the rule that makes a price-list upload safe. A sheet with name,
// category, unit, GST and selling price has five columns; without this, saving
// it back would blank the barcode, the brand, the reorder level and the
// description of every product it touched.
func (run *importRun) merge(existing domain.Product, row domain.ImportRow, categoryID, uomID uuid.UUID) domain.NewProductInput {
	in := domain.NewProductInput{
		Name:              existing.Name,
		NameHi:            existing.NameHi,
		NameKn:            existing.NameKn,
		CategoryID:        existing.CategoryID,
		HSNCode:           existing.HSNCode,
		SACCode:           existing.SACCode,
		Barcode:           existing.Barcode,
		UOMID:             existing.UOMID,
		BaseUnitQty:       existing.BaseUnitQty,
		GSTRate:           existing.GSTRate,
		IsFree:            existing.IsFree,
		AllowDecimal:      existing.AllowDecimal,
		MinStock:          existing.MinStock,
		ImageURL:          existing.ImageURL,
		Brand:             existing.Brand,
		Description:       existing.Description,
		TracksExpiry:      existing.TracksExpiry,
		ShelfLifeDays:     existing.ShelfLifeDays,
		PreferredVendorID: existing.PreferredVendorID,
	}

	has := run.sheet.Has
	if has("name") {
		in.Name = row.Name
	}
	if has("name_hi") {
		in.NameHi = row.NameHi
	}
	if has("name_kn") {
		in.NameKn = row.NameKn
	}
	if has("category") {
		in.CategoryID = categoryID
	}
	if has("unit") {
		in.UOMID = uomID
	}
	if has("barcode") && row.Barcode != "" {
		// A blank barcode cell does not clear one. A sheet exported from a
		// system that never held barcodes has the column and no values in it,
		// and clearing on blank would strip every barcode the shop had scanned
		// in by hand.
		in.Barcode = row.Barcode
	}
	if has("hsn_code") || has("sac_code") {
		if row.HSNCode != "" || row.SACCode != "" {
			in.HSNCode, in.SACCode = row.HSNCode, row.SACCode
		}
	}
	if has("gst_rate") {
		in.GSTRate = row.GSTRate
	}
	if has("brand") {
		in.Brand = row.Brand
	}
	if has("description") {
		in.Description = row.Description
	}
	if has("min_stock") {
		in.MinStock = row.MinStock
	}
	if has("sold_loose") {
		in.AllowDecimal = row.AllowDecimal
	}
	if has("expires") {
		in.TracksExpiry = row.TracksExpiry
	}
	if has("shelf_life_days") {
		in.ShelfLifeDays = row.ShelfLifeDays
	}
	return in
}

// priceFor is the opening price a new product carries, or nil when the sheet
// priced nothing for this row.
func (run *importRun) priceFor(row domain.ImportRow) *domain.UnitPrice {
	if !row.HasPrice || run.req.StoreID == uuid.Nil {
		return nil
	}
	return &domain.UnitPrice{
		MRP:                row.MRP,
		CostPrice:          row.CostPrice,
		SellPrice:          row.SellPrice,
		WholesaleSellPrice: row.WholesaleSellPrice,
		IsActive:           true,
	}
}

// price writes the prices for a product the catalogue already had.
//
// Like merge, only the columns the sheet carried change. A price row is
// replaced wholesale by the repository, so the existing one is read and the
// named figures set on top of it — otherwise a sheet carrying only a selling
// price would zero the MRP and the cost, and a zero MRP means "not a packaged
// commodity", which switches off the rule that stops the shop selling above it.
func (run *importRun) price(ctx context.Context, productID uuid.UUID, row domain.ImportRow, report *ImportReport) error {
	if !row.HasPrice || run.req.StoreID == uuid.Nil {
		return nil
	}
	unitID, ok, err := run.svc.repo.DefaultProductUnitID(ctx, run.tx, productID)
	if err != nil {
		return err
	}
	if !ok {
		return domain.Invalid("this product has no default unit, so a price has nothing to attach to")
	}

	price, err := run.svc.repo.GetPrice(ctx, run.tx, productID, run.req.StoreID, unitID)
	if err != nil && !errors.Is(err, platform.ErrNotFound) {
		return err
	}
	price.ProductID = productID
	price.StoreID = run.req.StoreID
	price.ProductUnitID = unitID
	price.IsActive = true

	has := run.sheet.Has
	if has("mrp") {
		price.MRP = row.MRP
	}
	if has("cost_price") {
		price.CostPrice = row.CostPrice
	}
	if has("sell_price") {
		price.SellPrice = row.SellPrice
	}
	if has("wholesale_price") {
		price.WholesaleSellPrice = row.WholesaleSellPrice
	}
	if err := price.Validate(); err != nil {
		return err
	}
	if err := run.svc.repo.UpsertPrice(ctx, run.tx, price); err != nil {
		return err
	}
	report.PricesSet++
	return nil
}

// isRowFault reports whether an error is about this row rather than about the
// system. A validation failure or a unique collision is the sheet's problem and
// belongs in the report; a dead connection is not.
func isRowFault(err error) bool {
	return errors.Is(err, domain.ErrValidation) ||
		errors.Is(err, platform.ErrConflict) ||
		errors.Is(err, platform.ErrConstraint) ||
		errors.Is(err, platform.ErrReference)
}

// plainMessage strips the wrapping a caller does not need to read. The layers
// below prefix "create product: validation failed: ...", which in a per-row
// report is four words of noise on every line.
func plainMessage(err error) string {
	msg := err.Error()
	for _, prefix := range []string{
		"create product: ", "update product: ",
		domain.ErrValidation.Error() + ": ",
	} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return strings.TrimPrefix(msg, domain.ErrValidation.Error()+": ")
}
