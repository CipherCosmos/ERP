// Package billing holds sales invoices: what is on them, and how their money
// is computed.
//
// The computation is the point of this package. In the implementation it
// replaces, the client sent sub_total, cgst, sgst, igst, discount, round_off
// and total, and the server validated some of them against each other within a
// five-paise tolerance — but never validated `total` against anything at all.
// A patched client could post a Rs.5,000 sale with total 1.00 and the books,
// the customer's ledger and the GST return would all take it.
//
// Here the server computes every figure from the line inputs and the product's
// own tax rate. Nothing monetary is believed.
package billing

import (
	"errors"
	"fmt"
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// Status is the lifecycle of an invoice. It matches the invoice_status enum:
// a sale is raised completed and can only ever be voided.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusVoided    Status = "voided"
)

// PaymentMode is how a sale was settled.
type PaymentMode string

const (
	PaymentCash   PaymentMode = "cash"
	PaymentCard   PaymentMode = "card"
	PaymentUPI    PaymentMode = "upi"
	PaymentBank   PaymentMode = "bank"
	PaymentCheque PaymentMode = "cheque"
	PaymentCredit PaymentMode = "credit"
	PaymentSplit  PaymentMode = "split"
)

// ValidPaymentModes are the settlement methods the system accepts.
var ValidPaymentModes = []PaymentMode{
	PaymentCash, PaymentCard, PaymentUPI, PaymentBank, PaymentCheque, PaymentCredit, PaymentSplit,
}

// Valid reports whether m is a mode the system knows.
func (m PaymentMode) Valid() bool {
	for _, v := range ValidPaymentModes {
		if m == v {
			return true
		}
	}
	return false
}

// OnCredit reports whether this sale creates a receivable.
func (m PaymentMode) OnCredit() bool { return m == PaymentCredit }

// Invoice is a sale.
type Invoice struct {
	ID          uuid.UUID  `json:"id"`
	InvoiceNo   string     `json:"invoice_no"`
	StoreID     uuid.UUID  `json:"store_id"`
	UserID      uuid.UUID  `json:"user_id"`
	TerminalID  *uuid.UUID `json:"terminal_id"`
	CustomerID  *uuid.UUID `json:"customer_id"`
	InvoiceDate time.Time  `json:"invoice_date"`

	SubTotal money.Money `json:"sub_total"`
	CGST     money.Money `json:"cgst"`
	SGST     money.Money `json:"sgst"`
	IGST     money.Money `json:"igst"`
	Discount money.Money `json:"discount"`
	RoundOff money.Money `json:"round_off"`
	Total    money.Money `json:"total"`

	Status       Status      `json:"status"`
	PaymentMode  PaymentMode `json:"payment_mode"`
	IsGSTR1Filed bool        `json:"is_gstr1_filed"`
	GSTR1Month   *string     `json:"gstr1_month"`
	CreatedAt    time.Time   `json:"created_at"`
}

// Item is one line of a sale.
type Item struct {
	ID        uuid.UUID `json:"id"`
	InvoiceID uuid.UUID `json:"invoice_id"`
	ProductID uuid.UUID `json:"product_id"`
	// ProductName rides along on the read paths, as UnitLabel does. Without it
	// a bill prints a uuid where the goods should be named, which is no use to
	// the customer holding it or to anyone answering a query about it later.
	ProductName string `json:"product_name"`
	BatchNo     string `json:"batch_no"`

	// ProductUnitID and UnitQty are what the customer actually asked for, and
	// what the bill prints: two 25 kg sacks rather than fifty of something.
	ProductUnitID uuid.UUID      `json:"product_unit_id"`
	UnitQty       money.Quantity `json:"unit_qty"`
	// UnitLabel rides along on the read paths so a bill can print "2 x 25 kg
	// sack" without a second lookup. It is not stored on the line: the label
	// belongs to the unit, and a shop that renames "sack" to "gunny" has
	// renamed it on every bill, not forked it.
	UnitLabel string `json:"unit_label"`

	// Quantity keeps the meaning it has everywhere else in this system: the
	// amount in the product's BASE stocking unit, which is what left the shelf.
	// It is UnitQty x the unit's qty_in_base, and stock, COGS and every
	// existing report go on reading it unchanged.
	Quantity money.Quantity `json:"quantity"`
	// Rate is the price of one selling unit — one sack, not one kilo — so that
	// Total is Rate x UnitQty exactly.
	Rate     money.Money `json:"rate"`
	Discount money.Money `json:"discount"`
	CGST     money.Money `json:"cgst"`
	SGST     money.Money `json:"sgst"`
	IGST     money.Money `json:"igst"`
	Total    money.Money `json:"total"`

	// UnitCostAtSale is the weighted-average cost of the batches consumed,
	// snapshotted here because the batch's cost can change afterwards and the
	// profit on this sale must not move with it. It is per base unit, because
	// that is what the batch it came from is costed in.
	UnitCostAtSale money.Money `json:"unit_cost_at_sale"`
}

// LineInput is one requested line of a sale, before the server prices it.
//
// Note what is absent: no tax figures and no line total. Those are computed,
// never accepted. Nor is there a base quantity: what leaves the shelf follows
// from the unit and is not something a caller states.
type LineInput struct {
	ProductID uuid.UUID
	// ProductUnitID names what the customer asked for: the 25 kg sack rather
	// than the loose kilo. The zero value means the product's default unit,
	// which is what a client written before units existed implies and what the
	// backfilled rows say.
	ProductUnitID uuid.UUID
	// UnitQty is how many of that unit — two sacks, or 0.25 of a loose kilo.
	UnitQty money.Quantity
	// BatchHint names a batch to consume first. It is an optimisation for a
	// scanned batch code, never a constraint.
	BatchHint string
	// Discount is an absolute amount off this line, not a percentage, so that
	// what the customer was given is recorded exactly as it was given.
	Discount money.Money
	// Rate is optional. When supplied it must equal the price the server holds
	// for this customer and this unit; it exists so a client can detect that
	// its cached price list is stale, not so it can choose what to charge.
	Rate *money.Money
}

// SellingUnit is the unit a line is transacted in, as the server resolved it.
//
// It carries only what pricing and stock need. The catalogue owns the full
// definition; billing needs the size and a name to print.
type SellingUnit struct {
	ID    uuid.UUID
	Label string
	// QtyInBase is how many of the product's own stocking unit one of these
	// amounts to: loose kilo 1, 25 kg sack 25, 250 g pouch 0.25.
	QtyInBase money.Quantity
}

// BaseQuantity converts an amount expressed in this unit into the product's
// stocking unit. Two sacks of 25 kg is 50 kg.
//
// This is the single multiplication that connects what the customer asked for
// to what leaves the shelf, and it is the invariant the schema states:
// quantity = unit_qty * product_units.qty_in_base.
func (u SellingUnit) BaseQuantity(unitQty money.Quantity) money.Quantity {
	return unitQty.Mul(u.QtyInBase.Decimal())
}

// PricedLine is a line joined to the authoritative price, unit and tax rate the
// server looked up.
type PricedLine struct {
	LineInput
	// Unit is the selling unit this line is transacted in: the one the request
	// named, or the product's default when it named none.
	Unit SellingUnit
	// Rate is the price the server holds for one of Unit, which is what will be
	// charged. It is per selling unit, never per base unit — a rate per kilo
	// derived from a sack's price is 41.999... and a bill built on it is short.
	Rate money.Money
	// GSTRate comes from the product record, never from the request.
	GSTRate money.Rate
}

// Quantity is what leaves the shelf, in the product's base stocking unit.
//
// It is derived rather than stored on the input, so there is exactly one place
// where a selling quantity becomes a stock movement.
func (l PricedLine) Quantity() money.Quantity { return l.Unit.BaseQuantity(l.UnitQty) }

// ComputedLine is a fully priced line.
type ComputedLine struct {
	PricedLine
	// Gross is rate x unit_qty before any discount — the price of a sack times
	// the number of sacks, never a per-kilo rate multiplied back up.
	Gross money.Money
	// Taxable is what tax is charged on: gross, less this line's own discount,
	// less its share of any bill-level discount.
	Taxable money.Money
	// ApportionedDiscount is this line's share of the bill-level discount.
	ApportionedDiscount money.Money
	Tax                 money.TaxSplit
	// Total is taxable plus tax.
	Total money.Money
}

// Computed is a fully priced invoice.
type Computed struct {
	Lines    []ComputedLine
	SubTotal money.Money
	CGST     money.Money
	SGST     money.Money
	IGST     money.Money
	// Discount is the total of every discount given, line-level and
	// bill-level, so the figure on the invoice is what the customer saved.
	Discount money.Money
	RoundOff money.Money
	Total    money.Money
}

// ComputeInput is everything needed to price a sale.
type ComputeInput struct {
	Lines []PricedLine
	// BillDiscount is an amount off the whole bill, applied after line
	// discounts.
	BillDiscount money.Money
	// InterState selects IGST over CGST+SGST. It is decided by comparing the
	// state codes of the store's and the customer's GSTIN.
	InterState bool
	// RoundTotal rounds the grand total to the nearest rupee and records the
	// adjustment, which is what the round_off column on an invoice is for.
	RoundTotal bool
}

// Compute prices a sale from its lines. Every monetary figure on the resulting
// invoice comes from here.
//
// A bill-level discount is apportioned across the lines in proportion to their
// value *before* tax is computed, because a discount given at the time of
// supply reduces the taxable value under section 15(3)(a) of the CGST Act.
// Taxing the full amount and then subtracting the discount would overstate the
// tax due and overcharge the customer.
func Compute(in ComputeInput) (Computed, error) {
	if len(in.Lines) == 0 {
		return Computed{}, invalid("an invoice needs at least one line")
	}
	if in.BillDiscount.IsNegative() {
		return Computed{}, invalid("bill discount cannot be negative")
	}

	// Line gross and line-level discount first, so the bill discount can be
	// shared out in proportion to what each line is actually worth.
	lines := make([]ComputedLine, len(in.Lines))
	netTotal := money.Zero()
	for i, l := range in.Lines {
		if err := validateLine(l); err != nil {
			return Computed{}, err
		}
		// Rate x UnitQty, a multiplication. Two sacks at 1,050 is exactly 2,100.
		// The alternative — divide the sack's price by 25 to get a rate per kilo
		// and multiply the 50 kg back up — is 41.999999 per kilo, and for a
		// 1,000 rupee 3 kg pack it is 333.333... with no exact form at all.
		gross := l.Rate.Mul(l.UnitQty)
		net := gross.Sub(l.Discount)
		if net.IsNegative() {
			return Computed{}, invalid("line %d: a discount of %s is more than the line is worth (%s)",
				i+1, l.Discount, gross)
		}
		lines[i] = ComputedLine{PricedLine: l, Gross: gross, Taxable: net}
		netTotal = netTotal.Add(net)
	}

	if in.BillDiscount.GreaterThan(netTotal) {
		return Computed{}, invalid("bill discount of %s is more than the bill is worth (%s)",
			in.BillDiscount, netTotal)
	}

	apportionBillDiscount(lines, netTotal, in.BillDiscount)

	out := Computed{
		Lines:    lines,
		SubTotal: money.Zero(),
		CGST:     money.Zero(),
		SGST:     money.Zero(),
		IGST:     money.Zero(),
		Discount: in.BillDiscount,
		RoundOff: money.Zero(),
	}
	for i := range lines {
		l := &lines[i]
		l.Taxable = l.Taxable.Sub(l.ApportionedDiscount)
		l.Tax = money.SplitTax(l.Taxable, l.GSTRate, in.InterState)
		l.Total = l.Taxable.Add(l.Tax.CGST).Add(l.Tax.SGST).Add(l.Tax.IGST)

		out.SubTotal = out.SubTotal.Add(l.Taxable)
		out.CGST = out.CGST.Add(l.Tax.CGST)
		out.SGST = out.SGST.Add(l.Tax.SGST)
		out.IGST = out.IGST.Add(l.Tax.IGST)
		out.Discount = out.Discount.Add(l.Discount)
	}

	gross := out.SubTotal.Add(out.CGST).Add(out.SGST).Add(out.IGST)
	if in.RoundTotal {
		rounded, adjustment := money.RoundOff(gross)
		out.RoundOff = adjustment
		out.Total = rounded
	} else {
		out.Total = gross
	}
	return out, nil
}

// apportionBillDiscount shares a bill-level discount across lines in
// proportion to their value.
//
// Proportional shares rarely divide into whole paise, so the shares are
// rounded down and the remainder is given to the largest line. That keeps the
// apportioned amounts summing to exactly the discount granted: paise invented
// or lost here would show up as an invoice whose parts do not add up to its
// total.
func apportionBillDiscount(lines []ComputedLine, netTotal, discount money.Money) {
	if discount.IsZero() || netTotal.IsZero() {
		for i := range lines {
			lines[i].ApportionedDiscount = money.Zero()
		}
		return
	}

	allocated := money.Zero()
	largest := 0
	for i := range lines {
		// Truncated, not rounded: rounding each share to nearest could
		// allocate more than the discount granted, which would have to be
		// clawed back from some arbitrary line.
		share := money.FromDecimal(discount.Decimal().
			Mul(lines[i].Taxable.Decimal()).
			Div(netTotal.Decimal()).
			Truncate(money.Scale))
		lines[i].ApportionedDiscount = share
		allocated = allocated.Add(share)
		if lines[i].Taxable.GreaterThan(lines[largest].Taxable) {
			largest = i
		}
	}
	if remainder := discount.Sub(allocated); !remainder.IsZero() {
		lines[largest].ApportionedDiscount = lines[largest].ApportionedDiscount.Add(remainder)
	}
}

func validateLine(l PricedLine) error {
	if l.ProductID == uuid.Nil {
		return invalid("a line needs a product")
	}
	if l.Unit.ID == uuid.Nil {
		return invalid("a line needs a unit to be sold in")
	}
	// A unit worth nothing of the stocking unit would take a sale's quantity to
	// zero and move no stock at all. The column carries the same CHECK; this
	// exists so a line built in memory cannot slip past it either.
	if !l.Unit.QtyInBase.IsPositive() {
		return invalid("one %s must be worth more than zero of the stocking unit, got %s",
			l.Unit.Label, l.Unit.QtyInBase)
	}
	if !l.UnitQty.IsPositive() {
		return invalid("quantity must be greater than zero, got %s", l.UnitQty)
	}
	// A quantity finer than the shop can write down is not a smaller sale, it
	// is a figure that will not survive being recorded. unit_qty is
	// numeric(14,3), so a request for 0.0006 kg is priced here on 0.0006 and
	// stored as 0.001: the line's own total stops equalling its rate times its
	// printed quantity, and no later reader can tell which of the two is the
	// sale that happened. Refuse it by name rather than silently round somebody
	// else's request for them.
	if !l.UnitQty.Equal(l.UnitQty.Round()) {
		return invalid("%s is finer than the %d decimal places a quantity is recorded to",
			l.UnitQty, money.QuantityScale)
	}
	// The same rule on what actually leaves the shelf. One 0.0004 kg unit, or
	// four hundred of them, is a unit defined finer than stock is counted in:
	// the multiplication rounds and every sale of it moves a quantity nobody
	// asked for. That is a fault in the unit, so the message names the unit.
	exactBase := money.QuantityFromDecimal(l.UnitQty.Decimal().Mul(l.Unit.QtyInBase.Decimal()))
	if !exactBase.Equal(exactBase.Round()) {
		return invalid("%s x the %s comes to %s of the stocking unit, which is finer than the %d decimal places stock is counted in",
			l.UnitQty, l.Unit.Label, exactBase, money.QuantityScale)
	}
	if l.Rate.IsNegative() {
		return invalid("rate cannot be negative, got %s", l.Rate)
	}
	if l.Discount.IsNegative() {
		return invalid("line discount cannot be negative, got %s", l.Discount)
	}
	if l.GSTRate.IsNegative() || l.GSTRate.GreaterThan(money.RateFromInt(100)) {
		return invalid("GST rate must be between 0 and 100, got %s", l.GSTRate)
	}
	return nil
}

// InterStateSupply reports whether a supply between two GSTINs crosses a state
// boundary, which decides IGST against CGST+SGST.
//
// An unregistered customer has no GSTIN and is treated as intra-state: the
// place of supply is the counter they are standing at.
func InterStateSupply(storeGSTIN, customerGSTIN string) bool {
	if len(storeGSTIN) < 2 || len(customerGSTIN) < 2 {
		return false
	}
	return storeGSTIN[:2] != customerGSTIN[:2]
}
