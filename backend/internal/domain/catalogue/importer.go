package catalogue

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"gbos/internal/domain/money"
)

// Bulk catalogue import.
//
// This file turns a spreadsheet into rows the service can act on. It resolves
// nothing that needs the database — no category ids, no unit ids, no decision
// about create versus update — because those are questions about what is
// already stored and belong in one transaction with the writing.
//
// The shape of the problem is not "parse a CSV". A shop arriving with two
// thousand items has them in a sheet somebody else's software wrote: the
// columns are in whatever order that software chose, named whatever it called
// them, with rupee signs and thousands separators in the money and a byte-order
// mark on the first header because it came out of Excel. Refusing that file is
// refusing the shop. So the reader matches columns by meaning rather than
// position, accepts the spellings those sheets actually use, and reports every
// bad row at once — because fixing a two-thousand-line sheet one error per
// upload is not a thing anyone will finish.

// MaxImportRows bounds one import. It is not a performance limit: the whole
// import is one transaction, and a transaction that holds locks for minutes is
// a shop that cannot bill while it runs.
const MaxImportRows = 5000

// ImportProblem is one thing wrong with one line, named well enough that
// somebody can find it in the sheet and fix it.
type ImportProblem struct {
	// Line is the line number in the file as a spreadsheet counts them: the
	// header is line 1, so this is the number shown in the row gutter.
	Line int `json:"line"`
	// Column is the heading as it appeared in the file, not the canonical name,
	// so the message points at something the reader can actually see.
	Column  string `json:"column,omitempty"`
	Message string `json:"message"`
}

func (p ImportProblem) String() string {
	if p.Column == "" {
		return fmt.Sprintf("line %d: %s", p.Line, p.Message)
	}
	return fmt.Sprintf("line %d, %s: %s", p.Line, p.Column, p.Message)
}

// ImportRow is one parsed line with everything settled that can be settled
// without reading the database.
type ImportRow struct {
	Line int `json:"line"`

	Name   string `json:"name"`
	NameHi string `json:"name_hi,omitempty"`
	NameKn string `json:"name_kn,omitempty"`
	// CategoryPath is the category column split on ">", outermost first, so
	// "Grocery > Flour" arrives as the two levels it names.
	CategoryPath []string `json:"category_path"`
	// Unit is the code or name as typed. Resolving it needs the units table.
	Unit    string `json:"unit"`
	Barcode string `json:"barcode,omitempty"`

	HSNCode string     `json:"hsn_code,omitempty"`
	SACCode string     `json:"sac_code,omitempty"`
	GSTRate money.Rate `json:"gst_rate"`

	Brand        string         `json:"brand,omitempty"`
	Description  string         `json:"description,omitempty"`
	MinStock     money.Quantity `json:"min_stock"`
	AllowDecimal bool           `json:"allow_decimal"`

	TracksExpiry  bool `json:"tracks_expiry"`
	ShelfLifeDays *int `json:"shelf_life_days,omitempty"`

	// HasPrice says the sheet carried at least one money column for this row.
	// A row without one is a catalogue entry with no price, which is legitimate
	// — a shop may price later — and is not the same as a row priced at zero.
	HasPrice           bool        `json:"has_price"`
	MRP                money.Money `json:"mrp"`
	CostPrice          money.Money `json:"cost_price"`
	SellPrice          money.Money `json:"sell_price"`
	WholesaleSellPrice money.Money `json:"wholesale_sell_price"`
}

// CategoryName is the leaf — what the product is actually filed under.
func (r ImportRow) CategoryName() string {
	if len(r.CategoryPath) == 0 {
		return ""
	}
	return r.CategoryPath[len(r.CategoryPath)-1]
}

// importField is a column this reader understands.
type importField int

const (
	fName importField = iota
	fNameHi
	fNameKn
	fCategory
	fUnit
	fBarcode
	fHSN
	fSAC
	fGST
	fBrand
	fDescription
	fMinStock
	fAllowDecimal
	fTracksExpiry
	fShelfLife
	fMRP
	fCost
	fSell
	fWholesale
)

// canonical is the name this reader uses for a column when it has to name one
// the file did not provide.
var canonical = map[importField]string{
	fName: "name", fNameHi: "name_hi", fNameKn: "name_kn",
	fCategory: "category", fUnit: "unit", fBarcode: "barcode",
	fHSN: "hsn_code", fSAC: "sac_code", fGST: "gst_rate",
	// "notes" rather than "description", and the difference matters: a sheet
	// exported from another system calls the product NAME "Description", and
	// this reader accepts that spelling below. Writing a column headed
	// "description" would therefore produce a file whose own reader takes it
	// for a second name column and ignores it.
	fBrand: "brand", fDescription: "notes", fMinStock: "min_stock",
	fAllowDecimal: "sold_loose", fTracksExpiry: "expires", fShelfLife: "shelf_life_days",
	fMRP: "mrp", fCost: "cost_price", fSell: "sell_price", fWholesale: "wholesale_price",
}

// headerAliases maps a normalised heading to the field it means.
//
// The spellings here are not invented. They are what Tally, Marg, Busy and a
// hand-kept Excel sheet call these columns, and every one of them is a file
// somebody will try to upload. Normalisation strips case and punctuation, so
// "GST Rate (%)" and "gst_rate" arrive at the same key and only one spelling of
// each idea needs listing.
var headerAliases = map[string]importField{
	"name": fName, "productname": fName, "product": fName, "item": fName,
	"itemname": fName, "particulars": fName, "description": fName,

	"namehi": fNameHi, "hindiname": fNameHi, "nameinhindi": fNameHi,
	"namekn": fNameKn, "kannadaname": fNameKn, "nameinkannada": fNameKn,

	"category": fCategory, "group": fCategory, "department": fCategory,
	"itemgroup": fCategory, "stockgroup": fCategory,

	"unit": fUnit, "uom": fUnit, "unitofmeasure": fUnit, "units": fUnit,
	"baseunit": fUnit,

	"barcode": fBarcode, "ean": fBarcode, "upc": fBarcode, "barcodeno": fBarcode,

	"hsn": fHSN, "hsncode": fHSN, "hsnsac": fHSN,
	"sac": fSAC, "saccode": fSAC,

	"gst": fGST, "gstrate": fGST, "gstpercent": fGST, "tax": fGST,
	"taxrate": fGST, "gstpercentage": fGST,

	"brand": fBrand, "company": fBrand, "make": fBrand, "manufacturer": fBrand,
	"notes": fDescription, "remarks": fDescription, "longdescription": fDescription,

	"minstock": fMinStock, "minimumstock": fMinStock, "reorderlevel": fMinStock,
	"reorder": fMinStock, "reorderpoint": fMinStock,

	"solddecimal": fAllowDecimal, "allowdecimal": fAllowDecimal, "loose": fAllowDecimal,
	"soldloose": fAllowDecimal, "decimal": fAllowDecimal, "fractional": fAllowDecimal,

	"expires": fTracksExpiry, "expiry": fTracksExpiry, "tracksexpiry": fTracksExpiry,
	"perishable": fTracksExpiry, "hasexpiry": fTracksExpiry,

	"shelflife": fShelfLife, "shelflifedays": fShelfLife, "shelflifeindays": fShelfLife,

	"mrp": fMRP, "maxretailprice": fMRP, "maximumretailprice": fMRP, "mrpprice": fMRP,

	"cost": fCost, "costprice": fCost, "purchaseprice": fCost, "purchaserate": fCost,
	"buyprice": fCost, "landedcost": fCost,

	"sell": fSell, "sellprice": fSell, "sellingprice": fSell, "saleprice": fSell,
	"salerate": fSell, "retailprice": fSell, "rate": fSell, "price": fSell,

	"wholesale": fWholesale, "wholesaleprice": fWholesale, "wholesalerate": fWholesale,
	"bulkprice": fWholesale, "dealerprice": fWholesale,
}

// required names the columns without which a row cannot become a product.
var required = []importField{fName, fCategory, fUnit, fGST}

// normaliseHeading reduces a heading to letters and digits, so spacing,
// punctuation, case and a stray "(%)" all stop mattering.
func normaliseHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ImportSheet is a parsed file: the rows, everything wrong with them, and
// which columns the file actually carried.
//
// The column set is not bookkeeping. When an import updates a product that is
// already in the catalogue, only the columns the sheet carried may change —
// otherwise uploading a price list, which has four columns, blanks every
// barcode, brand and reorder level in the shop.
type ImportSheet struct {
	Rows     []ImportRow     `json:"-"`
	Problems []ImportProblem `json:"problems,omitempty"`
	// Columns holds the canonical names of the columns present, so a caller
	// can say "this sheet does not set prices" rather than guess from the rows.
	Columns map[string]bool `json:"columns"`
}

// Has reports whether the sheet carried a column. The names are the canonical
// ones: name, name_hi, name_kn, category, unit, barcode, hsn_code, sac_code,
// gst_rate, brand, notes, min_stock, sold_loose, expires, shelf_life_days,
// mrp, cost_price, sell_price, wholesale_price.
func (s ImportSheet) Has(column string) bool { return s.Columns[column] }

// HasAnyPriceColumn reports whether the sheet sets prices at all.
func (s ImportSheet) HasAnyPriceColumn() bool {
	return s.Has("mrp") || s.Has("cost_price") || s.Has("sell_price") || s.Has("wholesale_price")
}

// ParseImport reads a catalogue sheet.
//
// It returns every row it could make sense of and every problem it found, not
// the first problem: an import is corrected in a spreadsheet, in one pass, and
// a reader that stops at the first bad cell turns a ten-minute correction into
// a hundred uploads. A returned error means the file itself is unusable — no
// header, no rows, a missing column — as opposed to a row being wrong.
func ParseImport(r io.Reader) (ImportSheet, error) {
	reader := csv.NewReader(stripBOM(r))
	// Rows are allowed to be short or long. A short one is reported against its
	// own line rather than aborting the read, which is what the default does —
	// and one ragged row at the end of an export is the commonest thing in
	// these files.
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err == io.EOF {
		return ImportSheet{}, invalid("the file is empty")
	}
	if err != nil {
		return ImportSheet{}, invalid("the file could not be read as a spreadsheet: %v", err)
	}

	// column position by field, and the heading as the file spelled it, so a
	// message can quote what the reader will see in their own sheet.
	at := map[importField]int{}
	spelt := map[importField]string{}
	var unknown []string
	for i, h := range header {
		key := normaliseHeading(h)
		if key == "" {
			continue
		}
		field, ok := headerAliases[key]
		if !ok {
			unknown = append(unknown, strings.TrimSpace(h))
			continue
		}
		// First spelling wins. A sheet with both "Price" and "Selling Price"
		// would otherwise silently take the later one, and which of the two
		// held the real price is not something to guess.
		if _, taken := at[field]; !taken {
			at[field] = i
			spelt[field] = strings.TrimSpace(h)
		}
	}

	var missing []string
	for _, field := range required {
		if _, ok := at[field]; !ok {
			missing = append(missing, canonical[field])
		}
	}
	// Either classification will do, but one of them has to be there. A tax
	// invoice must print the classification of what it sells, so a product
	// without one cannot be created through any path — and finding that out
	// two thousand times, once per row, is not a report anybody can read.
	_, hasHSN := at[fHSN]
	_, hasSAC := at[fSAC]
	if !hasHSN && !hasSAC {
		missing = append(missing, "hsn_code (or sac_code)")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		msg := fmt.Sprintf("the sheet is missing %s", strings.Join(missing, ", "))
		if len(unknown) > 0 {
			msg += fmt.Sprintf(" — the columns found were: %s", strings.Join(unknown, ", "))
		}
		return ImportSheet{}, invalid("%s", msg)
	}

	columns := make(map[string]bool, len(at))
	for field := range at {
		columns[canonical[field]] = true
	}

	var rows []ImportRow
	var problems []ImportProblem

	// Duplicates are caught here rather than left to the unique index, because
	// the index reports one collision and stops, and because a barcode repeated
	// inside the file is a mistake in the sheet rather than a clash with what is
	// already stored.
	seenBarcode := map[string]int{}
	seenName := map[string]int{}

	line := 1 // the header
	considered := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				problems = append(problems, ImportProblem{
					Line:    parseErr.Line,
					Message: "this line is not valid CSV — check for an unclosed quotation mark",
				})
				continue
			}
			return ImportSheet{}, invalid("the file could not be read past line %d: %v", line, err)
		}
		if blank(record) {
			// Excel appends these to almost every export.
			continue
		}
		considered++
		if considered > MaxImportRows {
			// Counted on lines read rather than rows accepted, so a sheet of ten
			// thousand bad rows is refused rather than answered with ten
			// thousand problems.
			return ImportSheet{}, invalid("an import is limited to %d rows; split the sheet", MaxImportRows)
		}

		row, rowProblems := parseImportRow(line, record, at, spelt)
		problems = append(problems, rowProblems...)
		if len(rowProblems) > 0 {
			continue
		}

		if row.Barcode != "" {
			if first, dup := seenBarcode[row.Barcode]; dup {
				problems = append(problems, ImportProblem{
					Line: line, Column: spelt[fBarcode],
					Message: fmt.Sprintf("barcode %s is already used on line %d", row.Barcode, first),
				})
				continue
			}
			seenBarcode[row.Barcode] = line
		}
		// Two rows naming the same product in the same category are a duplicate
		// however they are spelt, because that pair is how a row without a
		// barcode is matched to what is stored.
		key := strings.ToLower(row.Name) + "\x00" + strings.ToLower(row.CategoryName())
		if first, dup := seenName[key]; dup {
			problems = append(problems, ImportProblem{
				Line: line, Column: spelt[fName],
				Message: fmt.Sprintf("%s in %s is already on line %d", row.Name, row.CategoryName(), first),
			})
			continue
		}
		seenName[key] = line

		rows = append(rows, row)
	}

	if len(rows) == 0 && len(problems) == 0 {
		return ImportSheet{}, invalid("the sheet has a header but no rows")
	}
	return ImportSheet{Rows: rows, Problems: problems, Columns: columns}, nil
}

func parseImportRow(line int, record []string, at map[importField]int, spelt map[importField]string) (ImportRow, []ImportProblem) {
	var problems []ImportProblem
	complain := func(f importField, format string, args ...any) {
		problems = append(problems, ImportProblem{
			Line: line, Column: spelt[f], Message: fmt.Sprintf(format, args...),
		})
	}
	cell := func(f importField) string {
		i, ok := at[f]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	row := ImportRow{
		Line:        line,
		Name:        cell(fName),
		NameHi:      cell(fNameHi),
		NameKn:      cell(fNameKn),
		Unit:        cell(fUnit),
		Barcode:     cell(fBarcode),
		HSNCode:     cell(fHSN),
		SACCode:     cell(fSAC),
		Brand:       cell(fBrand),
		Description: cell(fDescription),
	}

	if row.Name == "" {
		complain(fName, "a product needs a name")
	}

	for _, part := range strings.Split(cell(fCategory), ">") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			row.CategoryPath = append(row.CategoryPath, trimmed)
		}
	}
	if len(row.CategoryPath) == 0 {
		complain(fCategory, "a product needs a category")
	}
	if len(row.CategoryPath) > MaxCategoryPath {
		complain(fCategory, "a category may be nested %d deep at most", MaxCategoryPath)
	}

	if row.Unit == "" {
		complain(fUnit, "a product needs a unit — the code or name from the Units screen")
	}

	if rate, ok := parseRateCell(cell(fGST)); !ok {
		complain(fGST, "%q is not a GST rate", cell(fGST))
	} else if err := ValidateGSTRate(rate); err != nil {
		complain(fGST, "%s", bareMessage(err))
	} else {
		row.GSTRate = rate
	}

	// The classification is checked with the same rule the product constructor
	// uses, so an import cannot put a product in the catalogue that the product
	// form would refuse. The one case worded differently here is the blank cell:
	// the constructor explains at length why a product needs a code, which is
	// the right thing to say once on a form and the wrong thing to repeat on
	// every line of a report.
	switch {
	case row.HSNCode == "" && row.SACCode == "":
		complain(fHSN, "an HSN code is required (or a SAC code, for a service)")
	default:
		hsn, sac, err := validateClassification(row.HSNCode, row.SACCode)
		if err != nil {
			field := fHSN
			if row.HSNCode == "" {
				field = fSAC
			}
			complain(field, "%s", bareMessage(err))
		} else {
			row.HSNCode, row.SACCode = hsn, sac
		}
	}

	if raw := cell(fMinStock); raw != "" {
		qty, err := money.QuantityFromString(stripNumeric(raw))
		switch {
		case err != nil:
			complain(fMinStock, "%q is not a quantity", raw)
		case qty.IsNegative():
			complain(fMinStock, "minimum stock cannot be negative")
		default:
			row.MinStock = qty
		}
	}

	if raw := cell(fAllowDecimal); raw != "" {
		if b, ok := parseBoolCell(raw); ok {
			row.AllowDecimal = b
		} else {
			complain(fAllowDecimal, "%q is not a yes or no", raw)
		}
	}
	if raw := cell(fTracksExpiry); raw != "" {
		if b, ok := parseBoolCell(raw); ok {
			row.TracksExpiry = b
		} else {
			complain(fTracksExpiry, "%q is not a yes or no", raw)
		}
	}
	if raw := cell(fShelfLife); raw != "" {
		days, err := strconv.Atoi(stripNumeric(raw))
		if err != nil {
			complain(fShelfLife, "%q is not a number of days", raw)
		} else if _, err := validateShelfLife(row.TracksExpiry, &days); err != nil {
			complain(fShelfLife, "%s", bareMessage(err))
		} else {
			row.ShelfLifeDays = &days
		}
	}

	for _, spec := range []struct {
		field importField
		into  *money.Money
	}{
		{fMRP, &row.MRP},
		{fCost, &row.CostPrice},
		{fSell, &row.SellPrice},
		{fWholesale, &row.WholesaleSellPrice},
	} {
		raw := cell(spec.field)
		if raw == "" {
			continue
		}
		amount, err := money.FromString(stripMoney(raw))
		if err != nil {
			complain(spec.field, "%q is not an amount", raw)
			continue
		}
		if amount.IsNegative() {
			complain(spec.field, "cannot be negative")
			continue
		}
		*spec.into = amount
		row.HasPrice = true
	}
	if row.HasPrice {
		price := UnitPrice{
			MRP: row.MRP, CostPrice: row.CostPrice,
			SellPrice: row.SellPrice, WholesaleSellPrice: row.WholesaleSellPrice,
		}
		if err := price.Validate(); err != nil {
			// Selling above MRP is the one this catches in practice, and it is
			// an offence rather than a preference, so it stops the row.
			complain(fSell, "%s", bareMessage(err))
		}
	}

	return row, problems
}

// MaxCategoryPath bounds "A > B > C" in the category column. Deeper than this
// is a mis-split rather than a catalogue.
const MaxCategoryPath = 4

// bareMessage drops the sentinel's own words from the front of a validation
// error. "validation failed: selling price 35 is above the MRP 33" is two
// useful words behind two that say nothing in a column headed "What is wrong".
func bareMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrValidation.Error()+": ")
}

func blank(record []string) bool {
	for _, f := range record {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// stripBOM removes the byte-order mark Excel writes at the start of a UTF-8
// CSV. Left in place it becomes part of the first heading, so a sheet whose
// first column is "name" arrives with an invisible character in front of it,
// and the import fails saying
// the name column is missing while the reader is looking straight at it.
func stripBOM(r io.Reader) io.Reader {
	buf := make([]byte, 3)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return io.MultiReader(strings.NewReader(string(buf[:n])), r)
	}
	if n == 3 && buf[0] == 0xEF && buf[1] == 0xBB && buf[2] == 0xBF {
		return r
	}
	return io.MultiReader(strings.NewReader(string(buf[:n])), r)
}

// stripMoney removes what a spreadsheet puts around a number: a rupee sign, the
// "Rs." some exports write instead, thousands separators, and the spaces
// between. "₹1,050.00" and "Rs. 1,050" are both 1050.
func stripMoney(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "₹")
	for _, prefix := range []string{"rs.", "rs", "inr"} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			s = s[len(prefix):]
			break
		}
	}
	return stripNumeric(s)
}

// stripNumeric removes separators and whitespace from a number, leaving the
// digits, sign and decimal point for the parser to judge.
func stripNumeric(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ',' || r == ' ' || r == ' ' || r == '\'' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// parseRateCell accepts "5", "5%", "5.00" and " 5 % ".
func parseRateCell(s string) (money.Rate, bool) {
	s = strings.TrimSuffix(stripNumeric(s), "%")
	if s == "" {
		// An empty GST cell is nil-rated, which is a real rate and a common one
		// — unbranded flour, fresh vegetables, milk. It is not a missing value.
		return money.Rate{}, true
	}
	rate, err := money.RateFromString(s)
	if err != nil {
		return money.Rate{}, false
	}
	return rate, true
}

// parseBoolCell accepts what a person types in a spreadsheet column they were
// told to answer yes or no in.
func parseBoolCell(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "t", "1", "haan", "हाँ", "ಹೌದು":
		return true, true
	case "n", "no", "false", "f", "0", "nahi", "नहीं", "ಇಲ್ಲ":
		return false, true
	}
	return false, false
}

// ImportTemplate is an empty catalogue sheet with the headings the reader is
// certain to accept and two worked rows.
//
// The examples are not decoration. They are the two shapes a grocery catalogue
// actually takes — a packaged item with an MRP and a barcode, and something
// weighed out of a sack with neither — and having both present answers the
// questions a blank header row leaves open: whether MRP is required (it is
// not), what "sold_loose" means, and how a sub-category is written.
const ImportTemplate = `name,category,unit,barcode,hsn_code,gst_rate,brand,mrp,cost_price,sell_price,wholesale_price,min_stock,sold_loose,expires,shelf_life_days
Parle-G Biscuits 200g,Grocery > Biscuits,PCS,8901063010123,19053100,18,Parle,30.00,24.50,30.00,28.00,24,no,yes,270
Sona Masoori Rice,Grocery > Rice,KG,,10063020,0,,0,52.00,64.00,58.00,50,yes,no,
`
