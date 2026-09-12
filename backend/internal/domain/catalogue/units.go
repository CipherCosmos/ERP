package catalogue

import (
	"strings"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// A shop sells the same goods two ways. Rice leaves the counter as 250 grams
// weighed into a bag, and it leaves as a 25 kg sack carried to a van. The
// customer, the price and the paperwork differ; the rice does not.
//
// Three types carry that, and the split between them is the whole design:
//
//   - Family and UnitOfMeasure describe measurement itself. A kilogram is a
//     thousand grams in every shop in the world, so the factor lives once, on
//     the unit, and is universal.
//
//   - ProductUnit describes how ONE product is handled: loose by the kilo, a
//     5 kg bag, a 25 kg sack. A "sack" is not a unit of measure — it is 25 kg
//     of this rice and 50 kg of that cement — so its size lives on the product,
//     not on a global table.
//
// Collapsing the two is the tempting mistake. It forces either a fake global
// "sack" unit that is a different size everywhere it is used, or a copy of the
// entire unit table for every product.

// Family groups units that can be converted between: weight, volume, count,
// length, area — and whatever else a trade needs, since these are rows.
//
// The family is also what refuses nonsense. Units convert only within a
// family, so rice measured in kilograms can never be sold in litres by
// arithmetic accident; the conversion simply does not exist.
type Family struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
	// BaseUOMID is the unit every member converts through. It is not
	// privileged in any other way — the kilogram is the canonical weight here,
	// but a shop that thinks in grams loses nothing.
	BaseUOMID uuid.UUID `json:"base_uom_id"`
	IsActive  bool      `json:"is_active"`

	// BaseUOMCode and UnitCount ride along on the read model. The units screen
	// lists families and would otherwise fetch the canonical unit's code and
	// count the members once per row.
	BaseUOMCode string `json:"base_uom_code,omitempty"`
	UnitCount   int    `json:"unit_count"`
}

// UnitUsage is what depends on a unit of measure. It is what decides whether a
// unit can be retired, and the counts are what the refusal says.
type UnitUsage struct {
	// Products stocked in this unit. Retiring it leaves every one of them with
	// a quantity and nothing to say what the quantity is of.
	Products int `json:"products"`
	// ProductUnits selling or buying in this measure.
	ProductUnits int `json:"product_units"`
	// IsFamilyBase says this is the unit its whole family converts through.
	// Every other member's factor is expressed against it, so retiring it is
	// not removing one row, it is removing the anchor for all of them.
	IsFamilyBase bool `json:"is_family_base"`
	// SiblingCount is how many other active units share the family, which is
	// what makes being the base matter.
	SiblingCount int `json:"sibling_count"`
}

// InUse reports whether anything depends on the unit.
func (u UnitUsage) InUse() bool {
	return u.Products > 0 || u.ProductUnits > 0 || (u.IsFamilyBase && u.SiblingCount > 0)
}

// NewFamily validates and returns a unit family ready to persist.
func NewFamily(code, name string) (Family, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	if code == "" {
		return Family{}, invalid("unit family code is required")
	}
	if name == "" {
		return Family{}, invalid("unit family name is required")
	}
	if len(code) > 40 {
		return Family{}, invalid("unit family code is longer than 40 characters")
	}
	return Family{ID: uuid.New(), Code: code, Name: name, IsActive: true}, nil
}

// ConvertWithin converts a quantity between two units of the same family.
//
// It converts through the family's canonical unit rather than storing a factor
// for every pair: n units need n factors this way and n squared the other, and
// the pairwise table is the one that goes inconsistent the first time somebody
// edits half of it.
func ConvertWithin(qty money.Quantity, from, to UnitOfMeasure) (money.Quantity, error) {
	if from.FamilyID != to.FamilyID {
		return money.ZeroQuantity(), invalid(
			"cannot convert %s to %s: they measure different things", from.Code, to.Code)
	}
	if !to.FactorToBase.IsPositive() {
		return money.ZeroQuantity(), invalid("unit %s has no usable conversion factor", to.Code)
	}
	if from.ID == to.ID {
		return qty, nil
	}
	// qty x from.factor gives the canonical amount; dividing by to.factor
	// expresses it in the target. Division is unavoidable here and is why
	// factors carry nine decimal places: a foot is 0.3048 m exactly, so the
	// round trip does not drift.
	return qty.Mul(from.FactorToBase.Decimal()).Div(to.FactorToBase.Decimal()), nil
}

// ProductUnit is one way a particular product may be bought or sold.
//
// It is a flat list, not a tree. The implementation this replaces nested a
// case inside a carton inside a piece and multiplied up the chain to find a
// size — which meant a repeated division on every read, and a size that
// silently changed for every descendant the moment one ancestor was edited.
// With a single stock pool there is nothing for the hierarchy to describe: a
// case is 144 pieces whether or not anyone bothers to model the carton.
type ProductUnit struct {
	ID        uuid.UUID `json:"id"`
	ProductID uuid.UUID `json:"product_id"`
	// UOMID is set when this unit is a genuine measure — "loose, by the kilo".
	// It is nil for a pack, because a sack is not a measurement.
	UOMID *uuid.UUID `json:"uom_id"`
	// Label is what the counter shows: "Loose (kg)", "5 kg bag", "25 kg sack".
	Label string `json:"label"`
	// QtyInBase is how many of the product's own stocking unit one of these
	// amounts to: loose kilo 1, 5 kg bag 5, 250 g pouch 0.25. It is the number
	// that turns a sale into a stock movement, and the reason a shop cannot be
	// out of loose rice while sacks sit on the shelf.
	QtyInBase money.Quantity `json:"qty_in_base"`
	// Barcode is per unit, not per product: the sack and the packet scan
	// differently, which is what lets a scan choose the price by itself.
	Barcode string `json:"barcode"`
	// IsDefault marks the unit a bare quantity means, and the one the counter
	// offers first. Exactly one per product, enforced by a partial unique index.
	IsDefault     bool `json:"is_default"`
	IsSellable    bool `json:"is_sellable"`
	IsPurchasable bool `json:"is_purchasable"`
	IsActive      bool `json:"is_active"`
	SortOrder     int  `json:"sort_order"`
}

// NewProductUnitInput is the caller-supplied shape for defining a unit.
type NewProductUnitInput struct {
	ProductID     uuid.UUID
	UOMID         *uuid.UUID
	Label         string
	QtyInBase     money.Quantity
	Barcode       string
	IsDefault     bool
	IsSellable    bool
	IsPurchasable bool
	SortOrder     int
}

// NewProductUnit validates and returns a product unit ready to persist.
func NewProductUnit(in NewProductUnitInput) (ProductUnit, error) {
	label := strings.TrimSpace(in.Label)
	if in.ProductID == uuid.Nil {
		return ProductUnit{}, invalid("product is required")
	}
	if label == "" {
		return ProductUnit{}, invalid("unit label is required")
	}
	if len(label) > 200 {
		return ProductUnit{}, invalid("unit label is longer than 200 characters")
	}
	// Zero would make a sale deduct nothing from stock and a purchase add
	// nothing; negative would run stock backwards. The column carries the same
	// CHECK, so this exists to say why rather than to be the only guard.
	if !in.QtyInBase.IsPositive() {
		return ProductUnit{}, invalid(
			"one %s must be worth more than zero of the stocking unit, got %s", label, in.QtyInBase)
	}
	// A unit nobody may transact in is not a unit, it is a row.
	if !in.IsSellable && !in.IsPurchasable {
		return ProductUnit{}, invalid("%s must be sellable, purchasable, or both", label)
	}
	// The default is what a bare quantity means, so it has to be one a sale can
	// actually use.
	if in.IsDefault && !in.IsSellable {
		return ProductUnit{}, invalid("the default unit must be sellable")
	}
	return ProductUnit{
		ID:            uuid.New(),
		ProductID:     in.ProductID,
		UOMID:         in.UOMID,
		Label:         label,
		QtyInBase:     in.QtyInBase,
		Barcode:       strings.TrimSpace(in.Barcode),
		IsDefault:     in.IsDefault,
		IsSellable:    in.IsSellable,
		IsPurchasable: in.IsPurchasable,
		IsActive:      true,
		SortOrder:     in.SortOrder,
	}, nil
}

// BaseQuantity converts an amount expressed in this unit into the product's
// stocking unit. Two sacks of 25 kg is 50 kg.
//
// This is the single multiplication that connects what the customer asked for
// to what leaves the shelf, and every stock movement goes through it.
func (u ProductUnit) BaseQuantity(unitQty money.Quantity) money.Quantity {
	return unitQty.Mul(u.QtyInBase.Decimal())
}

// UnitQuantity is the inverse: how many of this unit a base amount is. It is
// for display — "about 9 sacks and 12 kg loose" — and never for pricing, which
// multiplies rather than divides so that a bill cannot drift by a paisa.
func (u ProductUnit) UnitQuantity(baseQty money.Quantity) money.Quantity {
	if !u.QtyInBase.IsPositive() {
		return money.ZeroQuantity()
	}
	return baseQty.Div(u.QtyInBase.Decimal())
}

// DefaultUnit returns the unit a bare quantity means.
//
// It falls back to the first unit rather than failing, because a product with
// no default is a data fault that must not take the till down mid-sale; the
// caller gets a usable answer and the fault shows up in the management screen.
func DefaultUnit(units []ProductUnit) (ProductUnit, bool) {
	for _, u := range units {
		if u.IsDefault && u.IsActive {
			return u, true
		}
	}
	for _, u := range units {
		if u.IsActive {
			return u, true
		}
	}
	return ProductUnit{}, false
}

// FindUnit returns the unit with this id from a product's list.
func FindUnit(units []ProductUnit, id uuid.UUID) (ProductUnit, bool) {
	for _, u := range units {
		if u.ID == id {
			return u, true
		}
	}
	return ProductUnit{}, false
}

// UnitPrice is what one product unit sells for in one store.
//
// There is a row per unit, which is the change that makes bulk expressible at
// all. One price per product forces a 25 kg sack to cost exactly 25 times the
// loose kilo; a wholesaler's entire trade is that it does not.
type UnitPrice struct {
	ProductID     uuid.UUID `json:"product_id"`
	ProductUnitID uuid.UUID `json:"product_unit_id"`
	StoreID       uuid.UUID `json:"store_id"`
	// Label and QtyInBase ride along on the read model so a price list can be
	// rendered without a second lookup.
	Label     string         `json:"label"`
	QtyInBase money.Quantity `json:"qty_in_base"`

	MRP                money.Money `json:"mrp"`
	CostPrice          money.Money `json:"cost_price"`
	SellPrice          money.Money `json:"sell_price"`
	WholesaleSellPrice money.Money `json:"wholesale_sell_price"`
	IsActive           bool        `json:"is_active"`
}

// Validate rejects a price a sale could not legally or sensibly use.
func (p UnitPrice) Validate() error {
	for label, amount := range map[string]money.Money{
		"MRP":             p.MRP,
		"cost price":      p.CostPrice,
		"selling price":   p.SellPrice,
		"wholesale price": p.WholesaleSellPrice,
	} {
		if amount.IsNegative() {
			return invalid("%s cannot be negative", label)
		}
	}
	// Selling above MRP is an offence under the Legal Metrology (Packaged
	// Commodities) Rules, so the system must not be able to express it. An MRP
	// of zero means "not a packaged commodity" and is not a ceiling — which is
	// exactly the loose case, where there is no printed maximum.
	// The label is blank when a price is checked before it is attached to a
	// unit — which is what bulk import does, on every row — so it is appended
	// only when there is one. "above the MRP 50 for " is not a sentence.
	if !p.MRP.IsZero() && p.SellPrice.GreaterThan(p.MRP) {
		return invalid("selling price %s is above the MRP %s%s", p.SellPrice, p.MRP, forLabel(p.Label))
	}
	if !p.MRP.IsZero() && p.WholesaleSellPrice.GreaterThan(p.MRP) {
		return invalid("wholesale price %s is above the MRP %s%s", p.WholesaleSellPrice, p.MRP, forLabel(p.Label))
	}
	return nil
}

func forLabel(label string) string {
	if strings.TrimSpace(label) == "" {
		return ""
	}
	return " for " + label
}

// PriceFor returns what this customer pays for one of this unit.
func (p UnitPrice) PriceFor(wholesale bool) money.Money {
	if wholesale && !p.WholesaleSellPrice.IsZero() {
		return p.WholesaleSellPrice
	}
	return p.SellPrice
}

// EffectiveRatePerBase is the price per stocking unit — what a 25 kg sack at
// ₹1,050 works out to per kilo.
//
// It is for comparison only: showing a buyer that the sack is ₹42/kg against
// ₹45 loose, and flagging a pack priced above the loose rate as probably a
// typo. Billing never uses it, because dividing to find a rate and multiplying
// back to find a total is how a bill ends up a paisa short.
func (p UnitPrice) EffectiveRatePerBase(wholesale bool) money.Money {
	if !p.QtyInBase.IsPositive() {
		return money.Zero()
	}
	return p.PriceFor(wholesale).DivQuantity(p.QtyInBase)
}

// SellableUnitsWithPrice pairs each sellable unit with its price, in the order
// the counter should offer them: the default first, then smallest to largest,
// because a counter reaches for loose far more often than for a sack.
type SellableUnitsWithPrice struct {
	Unit  ProductUnit `json:"unit"`
	Price UnitPrice   `json:"price"`
	// HasPrice is false when no price row exists for this unit in this store.
	// The unit is still returned rather than hidden: a unit that silently
	// vanishes from the counter looks like a missing product, while one shown
	// as unpriced says exactly what to go and fix.
	HasPrice bool `json:"has_price"`
}
