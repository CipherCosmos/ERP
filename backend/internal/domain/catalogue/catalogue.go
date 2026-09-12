// Package catalogue holds the product catalogue entities: categories, units of
// measure, products and their per-store prices.
//
// Nothing here talks to a database or to HTTP. The types carry their own
// validation so that an invalid product cannot be constructed in the first
// place, rather than being caught by whichever handler happened to remember.
package catalogue

import (
	"errors"
	"fmt"
	"strings"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// Invalid builds a validation error from outside this package.
//
// The service layer needs it for the rules it owns that are still the caller's
// mistake rather than the system's — a unit name that matches nothing, a
// category path that resolves to nothing — so that those come back through the
// same sentinel every other rejected value does and are rendered the same way.
func Invalid(format string, args ...any) error { return invalid(format, args...) }

// Category groups products for reporting and for the POS keypad. Names carry
// Hindi and Kannada translations because the shop floor is multilingual.
type Category struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	NameHi   string     `json:"name_hi"`
	NameKn   string     `json:"name_kn"`
	ParentID *uuid.UUID `json:"parent_id"`
	HSNCode  string     `json:"hsn_code"`
	IsActive bool       `json:"is_active"`

	// ParentName, ProductCount and ChildCount ride along on the read path. The
	// screen shows a tree with a count on each row, and fetching either per row
	// is a query per category on a page that exists to be scanned.
	//
	// ProductCount counts active products only. An inactive one is not
	// something a shop is still selling, and including it would make a category
	// look in use when the only thing in it was retired last year.
	ParentName   string `json:"parent_name,omitempty"`
	ProductCount int    `json:"product_count"`
	// Discontinued products filed here. They do not keep a category in use, but
	// they do move when it is merged.
	RetiredProductCount int `json:"retired_product_count"`
	ChildCount          int `json:"child_count"`
}

// CategoryUpdate is what may be changed about an existing category.
//
// The id is not in it: a category's identity is not editable, and taking it as
// a field invites a handler that lets the body decide which row it writes.
type CategoryUpdate struct {
	Name     string
	NameHi   string
	NameKn   string
	ParentID *uuid.UUID
	HSNCode  string
}

// NewCategory validates and returns a category ready to persist.
func NewCategory(name, nameHi, nameKn, hsnCode string, parentID *uuid.UUID) (Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Category{}, invalid("category name is required")
	}
	if len(name) > 200 {
		return Category{}, invalid("category name is longer than 200 characters")
	}
	return Category{
		ID:       uuid.New(),
		Name:     name,
		NameHi:   strings.TrimSpace(nameHi),
		NameKn:   strings.TrimSpace(nameKn),
		ParentID: parentID,
		HSNCode:  strings.TrimSpace(hsnCode),
		IsActive: true,
	}, nil
}

// ApplyUpdate returns the category with the editable fields replaced, validated
// the same way creating one is.
//
// It does NOT check the parent: whether a proposed parent would make the
// category its own ancestor is a question about the rest of the tree, and this
// package cannot see it. The service asks the database, inside the transaction
// that does the write, because two people reparenting at once can each see a
// tree that is fine and together make a loop.
func (c Category) ApplyUpdate(in CategoryUpdate) (Category, error) {
	fresh, err := NewCategory(in.Name, in.NameHi, in.NameKn, in.HSNCode, in.ParentID)
	if err != nil {
		return Category{}, err
	}
	if in.ParentID != nil && *in.ParentID == c.ID {
		return Category{}, invalid("a category cannot be inside itself")
	}
	fresh.ID = c.ID
	fresh.IsActive = c.IsActive
	return fresh, nil
}

// UnitOfMeasure is how a quantity is measured. DecimalPlaces is what makes
// "2.5 kg" expressible and "2.5 boxes" not.
type UnitOfMeasure struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
	// FamilyID is what a unit can be converted with. Units convert only within
	// a family, so rice held in kilograms can never be sold in litres by
	// arithmetic accident — the conversion does not exist to be got wrong.
	FamilyID uuid.UUID `json:"family_id"`
	// FactorToBase is how many of the family's canonical unit one of these is:
	// a gram is 0.001 kg, a quintal 100.
	//
	// money.Rate rather than a bare decimal.Decimal because it serialises as an
	// exact JSON number with no fixed scale, which is what a factor needs: a
	// milligram is 0.000001 and a square foot 0.09290304, and both must survive
	// a round trip whole. A bare decimal would arrive at the client quoted.
	FactorToBase  money.Rate `json:"factor_to_base"`
	DecimalPlaces int        `json:"decimal_places"`
	IsActive      bool       `json:"is_active"`
}

// NewUnitOfMeasure validates and returns a unit ready to persist.
func NewUnitOfMeasure(code, name string, decimalPlaces int, familyID uuid.UUID, factorToBase money.Rate) (UnitOfMeasure, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	if code == "" {
		return UnitOfMeasure{}, invalid("unit code is required")
	}
	if name == "" {
		return UnitOfMeasure{}, invalid("unit name is required")
	}
	if familyID == uuid.Nil {
		return UnitOfMeasure{}, invalid("unit %s must belong to a family, or nothing can convert it", code)
	}
	// A zero or negative factor is a division by zero waiting in the conversion
	// path, and a unit that converts to nothing is not a unit.
	if !factorToBase.IsPositive() {
		return UnitOfMeasure{}, invalid("unit %s needs a conversion factor greater than zero", code)
	}
	if decimalPlaces < 0 || decimalPlaces > money.QuantityScale {
		return UnitOfMeasure{}, invalid("decimal places must be between 0 and %d", money.QuantityScale)
	}
	return UnitOfMeasure{
		ID:            uuid.New(),
		Code:          code,
		Name:          name,
		FamilyID:      familyID,
		FactorToBase:  factorToBase,
		DecimalPlaces: decimalPlaces,
		IsActive:      true,
	}, nil
}

// Product is an item the shop sells. Prices are deliberately not here: the same
// product carries a different price in each store, so price lives on Price.
type Product struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	NameHi      string         `json:"name_hi"`
	NameKn      string         `json:"name_kn"`
	CategoryID  uuid.UUID      `json:"category_id"`
	HSNCode     string         `json:"hsn_code"`
	SACCode     string         `json:"sac_code"`
	Barcode     string         `json:"barcode"`
	UOMID       uuid.UUID      `json:"uom_id"`
	BaseUnitQty money.Quantity `json:"base_unit_qty"`
	// GSTRate is a percentage, so it is a money.Rate and reaches the client as
	// a JSON number. A bare decimal.Decimal serialises quoted, and the products
	// screen renders the value straight into `{p.gst_rate}%`.
	GSTRate      money.Rate     `json:"gst_rate"`
	IsFree       bool           `json:"is_free"`
	IsActive     bool           `json:"is_active"`
	AllowDecimal bool           `json:"allow_decimal"`
	MinStock     money.Quantity `json:"min_stock"`
	ImageURL     string         `json:"image_url"`
	Brand        string         `json:"brand"`
	Description  string         `json:"description"`

	// TracksExpiry says this product has a shelf life, so receiving asks for a
	// date and the expiry report covers it. It is the product's property and
	// never inferred per batch: leaving it to whoever was typing at the
	// receiving bay made the expiry report only as complete as their memory,
	// and nothing said a date was missing.
	TracksExpiry bool `json:"tracks_expiry"`
	// ShelfLifeDays is how long it lasts from manufacture, so a receipt can work
	// out the expiry instead of a clerk reading it off the packet. Nil means the
	// date is stated on arrival.
	ShelfLifeDays *int `json:"shelf_life_days"`
	// PreferredVendorID is who normally supplies this, which is what lets a
	// low-stock list become a purchase order rather than a list of names to look
	// up one at a time.
	PreferredVendorID *uuid.UUID `json:"preferred_vendor_id"`
}

// NewProductInput is the caller-supplied shape for creating a product.
type NewProductInput struct {
	Name         string
	NameHi       string
	NameKn       string
	CategoryID   uuid.UUID
	HSNCode      string
	SACCode      string
	Barcode      string
	UOMID        uuid.UUID
	BaseUnitQty  money.Quantity
	GSTRate      money.Rate
	IsFree       bool
	AllowDecimal bool
	MinStock     money.Quantity
	ImageURL     string
	Brand        string
	Description  string

	TracksExpiry      bool
	ShelfLifeDays     *int
	PreferredVendorID *uuid.UUID
}

// NewProduct validates and returns a product ready to persist.
func NewProduct(in NewProductInput) (Product, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Product{}, invalid("product name is required")
	}
	if in.CategoryID == uuid.Nil {
		return Product{}, invalid("product category is required")
	}
	if in.UOMID == uuid.Nil {
		return Product{}, invalid("unit of measure is required")
	}
	if err := ValidateGSTRate(in.GSTRate); err != nil {
		return Product{}, err
	}
	hsn, sac, err := validateClassification(in.HSNCode, in.SACCode)
	if err != nil {
		return Product{}, err
	}
	shelfLife, err := validateShelfLife(in.TracksExpiry, in.ShelfLifeDays)
	if err != nil {
		return Product{}, err
	}
	if in.BaseUnitQty.IsNegative() || in.BaseUnitQty.IsZero() {
		return Product{}, invalid("base unit quantity must be greater than zero")
	}
	if in.MinStock.IsNegative() {
		return Product{}, invalid("minimum stock cannot be negative")
	}
	return Product{
		ID:           uuid.New(),
		Name:         name,
		NameHi:       strings.TrimSpace(in.NameHi),
		NameKn:       strings.TrimSpace(in.NameKn),
		CategoryID:   in.CategoryID,
		HSNCode:      hsn,
		SACCode:      sac,
		Barcode:      strings.TrimSpace(in.Barcode),
		UOMID:        in.UOMID,
		BaseUnitQty:  in.BaseUnitQty,
		GSTRate:      in.GSTRate,
		IsFree:       in.IsFree,
		IsActive:     true,
		AllowDecimal: in.AllowDecimal,
		MinStock:     in.MinStock,
		ImageURL:     strings.TrimSpace(in.ImageURL),
		Brand:        strings.TrimSpace(in.Brand),
		Description:  strings.TrimSpace(in.Description),

		TracksExpiry:      in.TracksExpiry,
		ShelfLifeDays:     shelfLife,
		PreferredVendorID: in.PreferredVendorID,
	}, nil
}

// Packing units used to live here, as a tree: a case held cartons and a carton
// held pieces, each row carrying only its size in its parent. ProductUnit in
// units.go replaces it with a flat list whose every entry states its size in
// the product's own stocking unit. See the comment on that type for why flat is
// a correction rather than a simplification.

// validateShelfLife checks the pair that has to be read together.
//
// A shelf life on something that does not expire is a contradiction the database
// also refuses, and it is worth catching here with a sentence rather than there
// with a constraint name. A zero or negative one is a typo — a product that
// lasts no days is not a product — and silently storing it would make every
// receipt of it expire the moment it arrived.
func validateShelfLife(tracks bool, days *int) (*int, error) {
	if days == nil || *days == 0 {
		if tracks {
			// Legitimate: a shop that reads the date off the packet rather than
			// counting from manufacture. Most perishables work this way.
			return nil, nil
		}
		return nil, nil
	}
	if !tracks {
		return nil, invalid(
			"a shelf life of %d days was given for something not marked as expiring. "+
				"Tick that it expires, or clear the shelf life", *days)
	}
	if *days < 0 {
		return nil, invalid("shelf life cannot be negative, got %d days", *days)
	}
	return days, nil
}

// validateClassification requires a product to carry the code a tax invoice has
// to print for it: an HSN code for goods, a SAC code for a service.
//
// CGST Rule 46(g) names it among the particulars of an invoice, and since April
// 2021 it is mandatory — four digits for business-to-consumer supplies below the
// turnover threshold, six above it and for anything business-to-business. A
// product saved without one produces invoices that cannot be filed, and nothing
// said so: the field was optional, trimmed, and stored blank.
//
// The length is checked as well as the presence. A three-digit code is not a
// classification, it is a typo that the GST portal will reject months later when
// the return is filed, by which time the invoices are with customers.
//
// Exempt and zero-rated goods are classified too. The code says what the goods
// are, not what they are taxed at, so there is no exemption from having one.
func validateClassification(hsnIn, sacIn string) (hsn, sac string, err error) {
	hsn = strings.TrimSpace(hsnIn)
	sac = strings.TrimSpace(sacIn)

	switch {
	case hsn == "" && sac == "":
		return "", "", invalid(
			"an HSN code is required: a tax invoice has to print the classification of what it sells, " +
				"and a product without one produces invoices that cannot be filed. " +
				"Use the SAC code field instead if this is a service")
	case hsn != "" && sac != "":
		return "", "", invalid(
			"a product carries an HSN code or a SAC code, not both: HSN classifies goods and SAC classifies services")
	case hsn != "":
		if err := validateCode("HSN", hsn, 4, 6, 8); err != nil {
			return "", "", err
		}
	default:
		if err := validateCode("SAC", sac, 6); err != nil {
			return "", "", err
		}
	}
	return hsn, sac, nil
}

// validateCode checks that a classification is digits and one of the lengths the
// scheme defines.
func validateCode(kind, code string, lengths ...int) error {
	for _, r := range code {
		if r < '0' || r > '9' {
			return invalid("%s code %q must be digits only", kind, code)
		}
	}
	for _, n := range lengths {
		if len(code) == n {
			return nil
		}
	}
	return invalid("%s code %q is %d digits; it must be %s",
		kind, code, len(code), describeLengths(lengths))
}

func describeLengths(lengths []int) string {
	parts := make([]string, len(lengths))
	for i, n := range lengths {
		parts[i] = fmt.Sprintf("%d", n)
	}
	switch len(parts) {
	case 1:
		return parts[0] + " digits"
	case 2:
		return parts[0] + " or " + parts[1] + " digits"
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1] + " digits"
	}
}

// ValidateGSTRate rejects rates outside the statutory range. The database
// carries the same CHECK, so this exists to produce a usable message rather
// than a constraint violation.
func ValidateGSTRate(rate money.Rate) error {
	if rate.IsNegative() || rate.GreaterThan(money.RateFromInt(100)) {
		return invalid("GST rate must be between 0 and 100, got %s", rate.String())
	}
	return nil
}

// The store-wide Price type that used to sit here is gone. A price is now
// always the price of one product UNIT — see UnitPrice in units.go — because
// one price per product forces a 25 kg sack to cost exactly 25 times the loose
// kilo, and a wholesaler's whole trade is that it does not.

// UnitFilter narrows a listing of units of measure. A zero filter lists every
// active unit in every family, which is what the product form's unit picker
// wants; the management screen sets IncludeInactive so a retired unit can be
// brought back.
type UnitFilter struct {
	FamilyID        *uuid.UUID
	IncludeInactive bool
}

// ProductFilter narrows a product listing. A zero filter lists the active
// catalogue.
type ProductFilter struct {
	StoreID    uuid.UUID
	CategoryID *uuid.UUID
	Barcode    string
	Search     string
	Limit      int
	// IncludeInactive lists deactivated products too, for the management
	// screens that need to reactivate one.
	IncludeInactive bool
	// Unclassified lists only the products carrying neither an HSN nor a SAC
	// code.
	//
	// New products cannot be saved without one, but products created before that
	// rule existed are still on the shelf, and every invoice that sells one goes
	// out without the classification Rule 46(g) requires. Refusing to sell them
	// would stop a shop trading over a missing field, so the API exposes the list
	// instead and somebody works through it.
	Unclassified bool
}

// PricedProduct is the read model the POS and the products screen use: a
// product together with everything those screens show about it in the store
// being served - its price, its category's name, and how much of it is on the
// shelf.
//
// Category name and stock are here for the same reason the price is: the
// screen renders one row per product and would otherwise fetch them per row.
// Both are properties of the product *in a store* rather than of the product
// itself, which is why they belong on this type and not on Product.
type PricedProduct struct {
	Product
	// CategoryName is the display name of CategoryID, blank when the product
	// has no category. The products screen has a Category column and no other
	// way to fill it.
	CategoryName string `json:"category_name"`
	// PreferredVendorName rides along for the same reason the category's does:
	// the screen has a column for it and would otherwise fetch one per row.
	PreferredVendorName string      `json:"preferred_vendor_name"`
	MRP                 money.Money `json:"mrp"`
	// CostPrice is what the shop pays for the product in this store.
	//
	// It was deliberately absent from this read model, on the argument that a
	// cashier screen should not carry the shop's margin. That argument does not
	// survive contact with the Set Price dialog, which is on this same screen,
	// is reached from this same listing, and pre-filled a cost of zero on every
	// product because the only figure it had was the one this model withheld -
	// so saving the dialog silently wrote the cost away. The endpoint is behind
	// authentication and a store scope, and GET /products/:id already returned
	// the sell price and MRP; the cost is no more sensitive than those.
	CostPrice          money.Money `json:"cost_price"`
	SellPrice          money.Money `json:"sell_price"`
	WholesaleSellPrice money.Money `json:"wholesale_sell_price"`
	// Stock is the total quantity of this product on hand in the store, summed
	// across batches. A product with no stock row reads zero rather than
	// dropping out of the listing.
	//
	// It is read fresh on every listing and never cached. A stock figure is the
	// one number on this screen that changes with every sale, and a stale one
	// is worse than none: it reads as authoritative while telling a shopkeeper
	// they can sell something they cannot.
	//
	// Stock is in the product's BASE unit and there is only ever one pool of it.
	// Ten sacks received is 250 kg here, and selling 250 g takes 0.25 away, so
	// the shop cannot be out of loose rice while sacks sit on the shelf.
	Stock money.Quantity `json:"stock"`

	// DefaultProductUnitID identifies the unit the four price fields above
	// belong to, and the one a bare quantity means. It is uuid.Nil only for a
	// product with no units at all, which the migration and CreateProduct
	// between them make unreachable.
	DefaultProductUnitID uuid.UUID `json:"default_product_unit_id"`

	// Units are the ways this product may be sold, each with its own price in
	// this store, default first and then smallest to largest. This is what lets
	// the till offer "loose / 5 kg bag / 25 kg sack" and charge the sack's own
	// price rather than twenty-five times the kilo.
	//
	// A unit with no price row is still listed, marked has_price false. Hiding
	// it would look to a cashier like a missing product; showing it unpriced
	// says exactly what to go and fix.
	Units []SellableUnitsWithPrice `json:"units"`
}

// PriceFor returns the price to charge this customer for one of the DEFAULT
// unit. A wholesale customer pays the wholesale price when one is set;
// everyone else pays the shelf price.
//
// A till selling anything other than the default unit must price it from the
// matching entry in Units, not from here.
func (p PricedProduct) PriceFor(wholesale bool) money.Money {
	if wholesale && !p.WholesaleSellPrice.IsZero() {
		return p.WholesaleSellPrice
	}
	return p.SellPrice
}

// ScannedProduct is what a barcode resolves to.
//
// A scan identifies a UNIT, not just a product: the 25 kg sack and the 250 g
// pouch of one rice carry different barcodes precisely so the till can tell
// them apart and charge the sack's own price. The product's four headline price
// fields still describe its default unit, so a client that ignores everything
// below goes on behaving exactly as it did.
type ScannedProduct struct {
	PricedProduct

	// ScannedUnit is the unit the barcode identified. When the scan matched the
	// product's own barcode rather than a unit's, it is the default unit.
	ScannedUnit ProductUnit `json:"scanned_unit"`
	// ScannedPrice is that unit's price in this store, zero-valued when none is
	// on file. ScannedHasPrice, not a zero total, is what says so: a free item
	// and an unpriced one are different things.
	ScannedPrice    UnitPrice `json:"scanned_price"`
	ScannedHasPrice bool      `json:"scanned_has_price"`
	// MatchedUnitBarcode distinguishes a scan of the sack's own barcode from a
	// scan of the product's, which fell back to the default unit. A counter that
	// meant to scan the sack and got the loose kilo can be told why.
	MatchedUnitBarcode bool `json:"matched_unit_barcode"`
}
