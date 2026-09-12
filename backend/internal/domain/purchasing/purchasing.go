// Package purchasing holds the buy-side entities: vendors, purchase orders and
// their lines, goods receipts, purchase returns and vendor payments.
//
// Nothing here talks to a database or to HTTP. The arithmetic that decides what
// a purchase order costs lives in this package and only here, because the
// implementation this replaces computed it inline in an HTTP handler in float64
// and compared the result to the client's figure with a 5-paise tolerance. A
// tolerance is not a rounding policy: it is an admission that two computations
// of the same number cannot be expected to agree. Here they agree exactly, so
// there is no tolerance and there must never be one.
package purchasing

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/document"
	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

// ErrCreditLimitExceeded means the order would push the vendor's exposure past
// the limit recorded against them.
var ErrCreditLimitExceeded = errors.New("vendor credit limit exceeded")

// ErrOverReceipt means more goods were presented against a purchase order line
// than that line ordered.
var ErrOverReceipt = errors.New("receipt exceeds the quantity ordered")

// ErrInvalidTransition means a document was not in the state the operation
// requires — a cancelled order being received, a return being approved twice.
// Every transition is guarded inside its UPDATE, so this is what a caller sees
// when the guard matched no row.
var ErrInvalidTransition = errors.New("document is not in a state that allows this")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// RateScale is the number of decimal places a per-unit purchase rate carries.
//
// Four, not two, because purchase_items.rate is NUMERIC(14,4): a rate is
// multiplied by a fractional quantity before anything is rounded, so it holds
// more precision than the paisa the line total settles at. Rates are normalised
// to this scale on construction, which is what makes a stored rate identical to
// the rate its totals were computed from — reload a purchase order a year later
// and recomputing it reproduces the same figures.
const RateScale = 4

// ---------------------------------------------------------------------------
// Statuses
// ---------------------------------------------------------------------------

// POStatus is the lifecycle of a purchase order. It mirrors the
// purchase_order_status enum in the schema.
type POStatus string

// The purchase order lifecycle. An order is raised pending; goods arriving
// against it make it partial and then received; an order nothing has been
// received against may be cancelled. cancelled and received are terminal.
const (
	StatusPending   POStatus = "pending"
	StatusPartial   POStatus = "partial"
	StatusReceived  POStatus = "received"
	StatusCancelled POStatus = "cancelled"
)

// Valid reports whether s is one of the four states the schema allows.
func (s POStatus) Valid() bool {
	switch s {
	case StatusPending, StatusPartial, StatusReceived, StatusCancelled:
		return true
	}
	return false
}

// Open reports whether goods may still be received against an order in this
// state. It is the guard the receiving path applies, expressed once here so the
// SQL and the Go agree on the same set.
func (s POStatus) Open() bool { return s == StatusPending || s == StatusPartial }

// ReturnStatus is the lifecycle of a purchase return, mirroring the
// purchase_return_status enum.
type ReturnStatus string

// A purchase return is raised pending and is approved once. Approval is what
// moves the stock, so it must happen exactly once.
const (
	ReturnPending  ReturnStatus = "pending"
	ReturnApproved ReturnStatus = "approved"
)

// ---------------------------------------------------------------------------
// Vendor
// ---------------------------------------------------------------------------

// Vendor is a supplier the shop buys from.
type Vendor struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Phone       string      `json:"phone"`
	Address     string      `json:"address"`
	GSTIN       string      `json:"gstin"`
	State       string      `json:"state"`
	CreditDays  int         `json:"credit_days"`
	CreditLimit money.Money `json:"credit_limit"`
	IsActive    bool        `json:"is_active"`
}

// VendorInput is the caller-supplied shape for creating or replacing a vendor.
type VendorInput struct {
	Name        string
	Phone       string
	Address     string
	GSTIN       string
	State       string
	CreditDays  int
	CreditLimit money.Money
}

// NewVendor validates and returns a vendor ready to persist.
func NewVendor(in VendorInput) (Vendor, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Vendor{}, invalid("vendor name is required")
	}
	if len(name) > 200 {
		return Vendor{}, invalid("vendor name is longer than 200 characters")
	}
	if in.CreditDays < 0 {
		return Vendor{}, invalid("credit days cannot be negative")
	}
	if in.CreditLimit.IsNegative() {
		return Vendor{}, invalid("credit limit cannot be negative")
	}
	gstin := strings.ToUpper(strings.TrimSpace(in.GSTIN))
	if gstin != "" && len(gstin) != 15 {
		return Vendor{}, invalid("GSTIN must be 15 characters, got %d", len(gstin))
	}
	return Vendor{
		ID:          uuid.New(),
		Name:        name,
		Phone:       strings.TrimSpace(in.Phone),
		Address:     strings.TrimSpace(in.Address),
		GSTIN:       gstin,
		State:       strings.TrimSpace(in.State),
		CreditDays:  in.CreditDays,
		CreditLimit: in.CreditLimit.Round(),
		IsActive:    true,
	}, nil
}

// VendorFilter narrows a vendor listing.
type VendorFilter struct {
	Search          string
	Limit           int
	IncludeInactive bool
}

// ---------------------------------------------------------------------------
// Tax placement
// ---------------------------------------------------------------------------

// IsInterState reports whether a supply between these two GSTINs crosses a
// state boundary, which decides whether the tax is IGST or CGST+SGST.
//
// The first two characters of a GSTIN are the state code. When either party's
// GSTIN is missing or malformed the supply is treated as intra-state, which is
// the same default the implementation this replaces applied: an unregistered
// local supplier is the ordinary case, and CGST+SGST is what a shop with no
// GSTIN on file is actually charged.
func IsInterState(storeGSTIN, vendorGSTIN string) bool {
	s := strings.TrimSpace(storeGSTIN)
	v := strings.TrimSpace(vendorGSTIN)
	if len(s) < 2 || len(v) < 2 {
		return false
	}
	return !strings.EqualFold(s[:2], v[:2])
}

// ---------------------------------------------------------------------------
// Purchase order
// ---------------------------------------------------------------------------

// PurchaseOrder is an order placed on a vendor. Every money field on it is
// derived from the lines by the server; none is ever taken from a client.
type PurchaseOrder struct {
	ID           uuid.UUID   `json:"id"`
	PONo         string      `json:"po_no"`
	StoreID      uuid.UUID   `json:"store_id"`
	VendorID     uuid.UUID   `json:"vendor_id"`
	VendorName   string      `json:"vendor_name"`
	OrderDate    time.Time   `json:"order_date"`
	ExpectedDate *time.Time  `json:"expected_date"`
	SubTotal     money.Money `json:"sub_total"`
	CGST         money.Money `json:"cgst"`
	SGST         money.Money `json:"sgst"`
	IGST         money.Money `json:"igst"`
	Discount     money.Money `json:"discount"`
	Total        money.Money `json:"total"`
	Status       POStatus    `json:"status"`
}

// BuyingUnit is the unit a purchase line is placed in, as the server resolved
// it: ten sacks rather than 250 kg.
//
// It is the buy-side counterpart of billing's SellingUnit and carries the same
// two facts — a name to print and a size to convert by. The catalogue owns the
// full definition.
type BuyingUnit struct {
	ID    uuid.UUID
	Label string
	// QtyInBase is how many of the product's own stocking unit one of these
	// amounts to: a loose kilo 1, a 25 kg sack 25.
	QtyInBase money.Quantity
}

// BaseQuantity converts an amount expressed in this unit into the product's
// stocking unit. Ten sacks of 25 kg is 250 kg, and that is what arrives on the
// shelf.
func (u BuyingUnit) BaseQuantity(unitQty money.Quantity) money.Quantity {
	return unitQty.Mul(u.QtyInBase.Decimal())
}

// PurchaseItem is one line of a purchase order.
type PurchaseItem struct {
	ID          uuid.UUID `json:"id"`
	POID        uuid.UUID `json:"po_id"`
	ProductID   uuid.UUID `json:"product_id"`
	ProductName string    `json:"product_name"`

	// ProductUnitID and UnitQty are what was ordered — ten sacks — and what the
	// order prints. UnitLabel rides along on the read paths for the same reason
	// ProductName does: without it the screen shows a uuid.
	ProductUnitID uuid.UUID      `json:"product_unit_id"`
	UnitQty       money.Quantity `json:"unit_qty"`
	UnitLabel     string         `json:"unit_label"`

	// Quantity is the same amount in the product's BASE stocking unit, which is
	// what stock, receipts and every existing report read. It is
	// UnitQty x the unit's qty_in_base.
	Quantity money.Quantity `json:"quantity"`
	// UOMID names a unit of measure and is not the same thing as
	// ProductUnitID: a kilogram is a kilogram everywhere, whereas a sack is
	// 25 kg of this product and 50 kg of another.
	UOMID *uuid.UUID `json:"uom_id"`
	// ReceivedQty is in base units, so it can be compared against Quantity
	// however the goods actually turned up.
	ReceivedQty money.Quantity `json:"received_qty"`
	// Rate is the price of one buying unit — one sack — so that Total is
	// Rate x UnitQty exactly.
	Rate     money.Money `json:"rate"`
	Discount money.Money `json:"discount"`
	CGST     money.Money `json:"cgst"`
	SGST     money.Money `json:"sgst"`
	IGST     money.Money `json:"igst"`
	Total    money.Money `json:"total"`
}

// Outstanding returns how much of this line is still to arrive. It is never
// negative: a line already over-received reads as nothing outstanding.
func (i PurchaseItem) Outstanding() money.Quantity {
	remaining := i.Quantity.Sub(i.ReceivedQty)
	if remaining.IsNegative() {
		return money.ZeroQuantity()
	}
	return remaining
}

// LineInput is one caller-supplied purchase order line.
//
// Note what is absent: there is no field for the tax, none for the line total
// that the server will believe, and none for the base quantity. The GST rate
// comes from the product, the amounts are computed here, ClaimedTotal is only
// ever compared against them, and what lands on the shelf follows from the unit.
type LineInput struct {
	ProductID uuid.UUID
	// ProductUnitID names what is being bought: the 25 kg sack rather than the
	// loose kilo. The zero value means the product's default unit, which is
	// what a client written before units existed implies.
	ProductUnitID uuid.UUID
	// UnitQty is how many of that unit — ten sacks.
	UnitQty  money.Quantity
	UOMID    *uuid.UUID
	Rate     money.Money
	Discount money.Money

	// ClaimedTotal is the line total the client believes it is ordering, used
	// as a cross-check so a pricing disagreement between the till and the
	// server surfaces at order time rather than at reconciliation. Zero means
	// "no claim". When non-zero it must equal the computed total exactly —
	// both figures are exact decimals derived from the same inputs, so any
	// difference at all is a real disagreement and not rounding noise.
	ClaimedTotal money.Money
}

// LineAmounts is the server's arithmetic for one line.
type LineAmounts struct {
	// Taxable is rate x quantity, less the line discount.
	Taxable money.Money
	// Tax is the CGST/SGST/IGST breakdown of Taxable.
	Tax money.TaxSplit
	// Total is Taxable plus the whole of Tax.
	Total money.Money
}

// ComputeLine works out what a purchase order line comes to.
//
// rate is per buying unit and unitQty counts them, so the line amount is a
// multiplication: ten sacks at Rs.900 is Rs.9,000. Deriving a rate per kilo and
// multiplying the 250 kg back up is the arithmetic that leaves a bill a paisa
// short, and for a pack size that does not divide there is no exact per-kilo
// rate at all.
//
// This is otherwise the arithmetic the old PurchaseHandler.Create got right in
// shape and wrong in type: it derived the tax from the product's own GST rate
// rather than trusting the client, which is correct, but did it in float64 and
// halved the tax with `gst / 2` — the exact defect money.SplitTax exists to
// prevent, since half of an odd number of paise has no representation and both
// halves were stored unrounded.
//
// There is exactly one rounding per line, at the paisa, inside Mul and inside
// SplitTax. rate x unitQty is rounded once because that product is the figure
// the invoice prints; the subtraction of the discount and the addition of the
// tax are exact.
func ComputeLine(rate money.Money, unitQty money.Quantity, discount money.Money, gstRate money.Rate, interState bool) (LineAmounts, error) {
	if unitQty.IsNegative() || unitQty.IsZero() {
		return LineAmounts{}, invalid("quantity must be greater than zero")
	}
	if rate.IsNegative() {
		return LineAmounts{}, invalid("rate cannot be negative")
	}
	if discount.IsNegative() {
		return LineAmounts{}, invalid("discount cannot be negative")
	}
	if err := ValidateGSTRate(gstRate); err != nil {
		return LineAmounts{}, err
	}

	taxable := rate.Mul(unitQty).Sub(discount)
	if taxable.IsNegative() {
		return LineAmounts{}, invalid("discount %s exceeds the line amount %s", discount, rate.Mul(unitQty))
	}

	split := money.SplitTax(taxable, gstRate, interState)
	return LineAmounts{Taxable: taxable, Tax: split, Total: taxable.Add(split.Total())}, nil
}

// ValidateGSTRate rejects rates outside the statutory range.
func ValidateGSTRate(rate money.Rate) error {
	if rate.IsNegative() || rate.GreaterThan(money.RateFromInt(100)) {
		return invalid("GST rate must be between 0 and 100, got %s", rate.String())
	}
	return nil
}

// NormaliseRate rounds a per-unit rate to RateScale, so that the value the
// arithmetic uses is exactly the value the column stores.
func NormaliseRate(rate money.Money) money.Money {
	return money.FromDecimal(rate.Decimal().Round(RateScale))
}

// POFilter narrows a purchase order listing. StoreID is mandatory: a purchase
// order belongs to one store and there is no cross-store listing.
type POFilter struct {
	StoreID  uuid.UUID
	VendorID *uuid.UUID
	Status   POStatus
	Limit    int
}

// ---------------------------------------------------------------------------
// Document numbering
// ---------------------------------------------------------------------------

// FinancialYear returns the Indian financial year containing t, in the
// '2026-27' form the document_sequences.fin_year CHECK requires. The year runs
// April to March, so January belongs to the year that started the previous
// April.
func FinancialYear(t time.Time) string {
	return document.FinancialYear(t)
}

// FormatPONumber renders an allocated sequence value as a purchase order
// number: PO/T1/2026-27/00001.
//
// The terminal code and financial year are in the number rather than only in
// the sequence table because the series has to be self-describing on a printed
// document, and because po_no is UNIQUE (store_id, po_no) — two terminals in
// one store draw from separate counters and would otherwise collide on 00001.
func FormatPONumber(terminalCode, finYear string, seq int64) string {
	return document.Number(document.PrefixPurchaseOrder, terminalCode, finYear, seq)
}

// ---------------------------------------------------------------------------
// Goods receipt
// ---------------------------------------------------------------------------

// GoodsReceipt records what physically arrived against a purchase order.
type GoodsReceipt struct {
	ID   uuid.UUID `json:"id"`
	POID uuid.UUID `json:"po_id"`
	// PONo is the human-readable number of the purchase order this receipt is
	// against, such as PO/T1/2026-27/00001. The receiving screen has a "PO
	// Reference" column, and with only the id to show it printed the first
	// eight characters of a UUID - a string that identifies the order to nobody
	// and cannot be matched against the paperwork in the receiver's hand.
	//
	// The read paths already join purchase_orders to scope the receipt to a
	// store, and the receiving path already locks that row, so this column
	// costs nothing to carry on any of them.
	PONo         string    `json:"po_no"`
	ReceivedDate time.Time `json:"received_date"`
	ReceivedBy   string    `json:"received_by"`
	Notes        string    `json:"notes"`
	// ItemCount is how many lines the receipt has. The listing shows it as an
	// "Items" column and does not fetch the lines of every receipt to count
	// them, so the count is aggregated in the query that produces the listing.
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
}

// GoodsReceiptItem is one line of a goods receipt: how much arrived, how much
// was accepted into stock, and how much was rejected back to the vendor.
//
// Every quantity on it is in the product's BASE stocking unit, which is what
// its columns hold and what the stock it moved is counted in. A delivery stated
// as ten sacks is recorded here as the 250 kg it amounts to: goods_receipt_items
// carries no unit of its own, and a field that vanished on the next read would
// be worse than none.
type GoodsReceiptItem struct {
	ID        uuid.UUID `json:"id"`
	GRID      uuid.UUID `json:"gr_id"`
	ProductID uuid.UUID `json:"product_id"`
	// ProductName rides along on the read paths, as it does on a purchase
	// order line. Without it the receipt screen prints a truncated uuid in the
	// Product column, which nobody on a loading bay can check goods against.
	ProductName string         `json:"product_name"`
	OrderedQty  money.Quantity `json:"ordered_qty"`
	ReceivedQty money.Quantity `json:"received_qty"`
	AcceptedQty money.Quantity `json:"accepted_qty"`
	RejectedQty money.Quantity `json:"rejected_qty"`
	Reason      string         `json:"reason"`
}

// ReceiptLineInput is one caller-supplied goods receipt line.
//
// Its quantities are counted in the line's unit — ten sacks, two of them
// rejected — and are converted to base units once, by NewGoodsReceiptItem.
type ReceiptLineInput struct {
	ProductID uuid.UUID
	// ProductUnitID names the unit the delivery is counted in. The zero value
	// means the unit the purchase order line was placed in, which is what a
	// receiving clerk confirming an order against its paperwork means, and what
	// a client written before units existed sends.
	ProductUnitID uuid.UUID
	ReceivedQty   money.Quantity
	RejectedQty   money.Quantity

	// AcceptedQty is a pointer because "not stated" and "zero" are different
	// facts and the type must be able to tell them apart. That distinction is
	// the whole of defect 2: the code this replaces wrote
	//
	//	if item.AcceptedQty > 0 { accepted = item.AcceptedQty } else { accepted = item.ReceivedQty }
	//
	// so a delivery of 100 units with all 100 rejected fell through the else
	// branch and booked 100 units of rejected goods into sellable stock. Here,
	// nil means "derive it" and a stated zero means zero.
	AcceptedQty *money.Quantity

	Reason string

	// BatchNo identifies the physical lot. Blank means the receipt allocates
	// one.
	BatchNo string

	// CostPrice is the landed cost of one buying unit of this lot — what one
	// sack cost. Zero means "use the store's recorded cost price for the unit".
	CostPrice money.Money
}

// NewGoodsReceiptItem validates a receipt line, resolves the accepted quantity
// and converts the three quantities into the product's base stocking unit.
//
// The invariant enforced here is accepted + rejected == received. When the
// caller does not state an accepted quantity it is derived as received minus
// rejected, so the ordinary "nothing was rejected" case needs no ceremony while
// a full rejection still yields zero accepted.
//
// The invariant is checked in the unit the clerk counted in and only then
// converted. Converting first and comparing after would let a pack size that
// does not land on a whole number of the third decimal place turn a delivery
// that balances into one that does not.
func NewGoodsReceiptItem(grID uuid.UUID, ordered money.Quantity, unit BuyingUnit, in ReceiptLineInput) (GoodsReceiptItem, error) {
	if in.ProductID == uuid.Nil {
		return GoodsReceiptItem{}, invalid("product is required on a receipt line")
	}
	if unit.ID == uuid.Nil {
		return GoodsReceiptItem{}, invalid("a receipt line needs a unit to be counted in")
	}
	if !unit.QtyInBase.IsPositive() {
		return GoodsReceiptItem{}, invalid(
			"one %s must be worth more than zero of the stocking unit, got %s", unit.Label, unit.QtyInBase)
	}
	if in.ReceivedQty.IsNegative() {
		return GoodsReceiptItem{}, invalid("received quantity cannot be negative")
	}
	if in.ReceivedQty.IsZero() {
		return GoodsReceiptItem{}, invalid("received quantity must be greater than zero")
	}
	if in.RejectedQty.IsNegative() {
		return GoodsReceiptItem{}, invalid("rejected quantity cannot be negative")
	}
	if in.RejectedQty.GreaterThan(in.ReceivedQty) {
		return GoodsReceiptItem{}, invalid("rejected %s is more than the %s received", in.RejectedQty, in.ReceivedQty)
	}

	accepted := in.ReceivedQty.Sub(in.RejectedQty)
	if in.AcceptedQty != nil {
		accepted = *in.AcceptedQty
		if accepted.IsNegative() {
			return GoodsReceiptItem{}, invalid("accepted quantity cannot be negative")
		}
		if !accepted.Add(in.RejectedQty).Equal(in.ReceivedQty) {
			return GoodsReceiptItem{}, invalid(
				"accepted %s plus rejected %s does not equal the %s received",
				accepted, in.RejectedQty, in.ReceivedQty)
		}
	}

	return GoodsReceiptItem{
		ID:          uuid.New(),
		GRID:        grID,
		ProductID:   in.ProductID,
		OrderedQty:  ordered,
		ReceivedQty: unit.BaseQuantity(in.ReceivedQty),
		AcceptedQty: unit.BaseQuantity(accepted),
		RejectedQty: unit.BaseQuantity(in.RejectedQty),
		Reason:      strings.TrimSpace(in.Reason),
	}, nil
}

// ReceiptAllocation assigns part of a received quantity to one purchase order
// line.
type ReceiptAllocation struct {
	ItemID uuid.UUID
	Qty    money.Quantity
}

// AllocateReceipt works out which purchase order lines a delivery of received
// units of productID books against, filling each line's outstanding quantity in
// turn.
//
// received is in the product's base stocking unit, as are the lines' own
// quantities. Allocating in base units is what lets an order placed in sacks be
// filled by a delivery counted in loose kilos, which is an ordinary thing for a
// vendor to do and impossible to express against a per-line pack count.
//
// It refuses to allocate more than the order asked for. Nothing in the previous
// implementation did: a purchase order for 100 accepted a receipt of 500, the
// extra 400 went into sellable stock, and the order was then marked fully
// received because total received exceeded total ordered. The schema
// deliberately carries no CHECK for this — a historically over-received row must
// still be storable — so the rule lives here and in the guard on the UPDATE.
//
// A product legitimately appears on more than one line of an order (two batches
// at two rates), which is why this returns a list rather than a single line.
func AllocateReceipt(items []PurchaseItem, productID uuid.UUID, received money.Quantity) ([]ReceiptAllocation, error) {
	if received.IsNegative() || received.IsZero() {
		return nil, invalid("received quantity must be greater than zero")
	}

	matching := make([]PurchaseItem, 0, 2)
	outstanding := money.ZeroQuantity()
	for _, it := range items {
		if it.ProductID == productID {
			matching = append(matching, it)
			outstanding = outstanding.Add(it.Outstanding())
		}
	}
	if len(matching) == 0 {
		return nil, invalid("product %s is not on this purchase order", productID)
	}
	if received.GreaterThan(outstanding) {
		return nil, fmt.Errorf("%w: product %s has %s still outstanding but %s was presented",
			ErrOverReceipt, productID, outstanding, received)
	}

	allocations := make([]ReceiptAllocation, 0, len(matching))
	remaining := received
	for _, it := range matching {
		if remaining.IsZero() {
			break
		}
		take := it.Outstanding()
		if take.IsZero() {
			continue
		}
		if take.GreaterThan(remaining) {
			take = remaining
		}
		allocations = append(allocations, ReceiptAllocation{ItemID: it.ID, Qty: take})
		remaining = remaining.Sub(take)
	}
	if !remaining.IsZero() {
		// Unreachable given the outstanding check above; stated so that a
		// future change to either side cannot silently drop stock.
		return nil, fmt.Errorf("%w: %s of product %s could not be allocated to a line",
			ErrOverReceipt, remaining, productID)
	}
	return allocations, nil
}

// NextStatus decides what a purchase order's status becomes once every line's
// received quantity is known.
//
// It is expressed on the totals rather than on a count of lines because a count
// answers the wrong question: the implementation this replaces asked
// "COUNT(*) WHERE received_qty < quantity", discarded the Scan error so the
// count stayed 0 on failure, and marked the order fully received.
func NextStatus(items []PurchaseItem) POStatus {
	if len(items) == 0 {
		return StatusPending
	}
	anyReceived := false
	allReceived := true
	for _, it := range items {
		if it.ReceivedQty.IsPositive() {
			anyReceived = true
		}
		if it.ReceivedQty.LessThan(it.Quantity) {
			allReceived = false
		}
	}
	switch {
	case allReceived:
		return StatusReceived
	case anyReceived:
		return StatusPartial
	default:
		return StatusPending
	}
}

// ---------------------------------------------------------------------------
// Purchase return
// ---------------------------------------------------------------------------

// PurchaseReturn is goods going back to a vendor.
type PurchaseReturn struct {
	ID         uuid.UUID    `json:"id"`
	POID       *uuid.UUID   `json:"po_id"`
	VendorID   uuid.UUID    `json:"vendor_id"`
	VendorName string       `json:"vendor_name"`
	StoreID    uuid.UUID    `json:"store_id"`
	ReturnDate time.Time    `json:"return_date"`
	Total      money.Money  `json:"total"`
	Status     ReturnStatus `json:"status"`
	Reason     string       `json:"reason"`
	UserID     uuid.UUID    `json:"user_id"`
	CreatedAt  time.Time    `json:"created_at"`
}

// PurchaseReturnItem is one line of a purchase return.
type PurchaseReturnItem struct {
	ID        uuid.UUID      `json:"id"`
	ReturnID  uuid.UUID      `json:"return_id"`
	ProductID uuid.UUID      `json:"product_id"`
	Quantity  money.Quantity `json:"quantity"`
	Rate      money.Money    `json:"rate"`
	Total     money.Money    `json:"total"`
}

// ReturnLineInput is one caller-supplied purchase return line.
type ReturnLineInput struct {
	ProductID uuid.UUID
	Quantity  money.Quantity
	Rate      money.Money
}

// NewPurchaseReturnItem validates a return line and computes its total
// server-side.
func NewPurchaseReturnItem(returnID uuid.UUID, in ReturnLineInput) (PurchaseReturnItem, error) {
	if in.ProductID == uuid.Nil {
		return PurchaseReturnItem{}, invalid("product is required on a return line")
	}
	if in.Quantity.IsNegative() || in.Quantity.IsZero() {
		return PurchaseReturnItem{}, invalid("return quantity must be greater than zero")
	}
	if in.Rate.IsNegative() {
		return PurchaseReturnItem{}, invalid("return rate cannot be negative")
	}
	rate := NormaliseRate(in.Rate)
	return PurchaseReturnItem{
		ID:        uuid.New(),
		ReturnID:  returnID,
		ProductID: in.ProductID,
		Quantity:  in.Quantity,
		Rate:      rate,
		Total:     rate.Mul(in.Quantity),
	}, nil
}

// ReturnFilter narrows a purchase return listing.
type ReturnFilter struct {
	StoreID  uuid.UUID
	VendorID *uuid.UUID
	Status   ReturnStatus
	Limit    int
}

// ---------------------------------------------------------------------------
// Vendor payment
// ---------------------------------------------------------------------------

// VendorPayment is money paid out to a vendor. It is what makes a vendor's
// exposure fall as bills are settled.
type VendorPayment struct {
	ID       uuid.UUID `json:"id"`
	VendorID uuid.UUID `json:"vendor_id"`
	// VendorName is the display name of VendorID, filled by the listing query
	// from the vendors table. The payments screen has a Vendor column and no
	// other source for it, so without this it rendered a raw UUID.
	//
	// It is blank on the object returned from recording a payment, for the same
	// reason PurchaseReturn.VendorName is: the create path is given a vendor id
	// and has no need to read the vendor back. The client posting a payment
	// already knows which vendor it chose.
	VendorName  string      `json:"vendor_name"`
	POID        *uuid.UUID  `json:"po_id"`
	Amount      money.Money `json:"amount"`
	PaymentMode string      `json:"payment_mode"`
	Reference   string      `json:"reference"`
	PaymentDate time.Time   `json:"payment_date"`
	Notes       string      `json:"notes"`
	UserID      uuid.UUID   `json:"user_id"`
	StoreID     uuid.UUID   `json:"store_id"`
	CreatedAt   time.Time   `json:"created_at"`
}

// PaymentInput is the caller-supplied shape for recording a vendor payment.
type PaymentInput struct {
	VendorID    uuid.UUID
	POID        *uuid.UUID
	Amount      money.Money
	PaymentMode string
	Reference   string
	PaymentDate time.Time
	Notes       string
	UserID      uuid.UUID
	StoreID     uuid.UUID
}

// NewVendorPayment validates and returns a payment ready to persist.
func NewVendorPayment(in PaymentInput) (VendorPayment, error) {
	if in.VendorID == uuid.Nil {
		return VendorPayment{}, invalid("vendor is required")
	}
	if in.StoreID == uuid.Nil {
		return VendorPayment{}, invalid("store is required")
	}
	if in.UserID == uuid.Nil {
		return VendorPayment{}, invalid("user is required")
	}
	if in.Amount.IsNegative() || in.Amount.IsZero() {
		return VendorPayment{}, invalid("payment amount must be greater than zero")
	}
	if in.PaymentDate.IsZero() {
		return VendorPayment{}, invalid("payment date is required")
	}
	mode := strings.TrimSpace(in.PaymentMode)
	if mode == "" {
		mode = "bank"
	}
	return VendorPayment{
		ID:          uuid.New(),
		VendorID:    in.VendorID,
		POID:        in.POID,
		Amount:      in.Amount.Round(),
		PaymentMode: mode,
		Reference:   strings.TrimSpace(in.Reference),
		PaymentDate: in.PaymentDate,
		Notes:       strings.TrimSpace(in.Notes),
		UserID:      in.UserID,
		StoreID:     in.StoreID,
	}, nil
}

// ---------------------------------------------------------------------------
// Vendor exposure
// ---------------------------------------------------------------------------

// Exposure is what a vendor is owed: everything ordered from them that has not
// been cancelled, less everything paid to them.
//
// The implementation this replaces summed only pending and partial purchase
// orders and never looked at vendor_payments at all, so a fully received but
// unpaid order dropped straight out of the vendor's exposure the moment the
// last carton was booked in — exactly when the shop owed the money.
type Exposure struct {
	// Ordered is the total of every non-cancelled purchase order.
	Ordered money.Money `json:"ordered"`
	// Paid is the total of every payment made to the vendor.
	Paid money.Money `json:"paid"`
}

// Outstanding returns Ordered less Paid. It can legitimately be negative when a
// vendor holds an advance, and is deliberately not clamped: a vendor sitting on
// the shop's money has negative exposure and should be able to order against it.
func (e Exposure) Outstanding() money.Money { return e.Ordered.Sub(e.Paid) }

// WithinLimit reports whether adding an order of newOrder keeps the vendor
// inside limit. A limit of zero means no limit is recorded, which is how the
// column's default reads and what the previous implementation also assumed.
//
// The comparison is exact. There is no tolerance and no float anywhere in it,
// so a limit is a limit rather than approximately a limit.
func (e Exposure) WithinLimit(newOrder, limit money.Money) bool {
	if limit.IsZero() {
		return true
	}
	return e.Outstanding().Add(newOrder).LessThanOrEqual(limit)
}
