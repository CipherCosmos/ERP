// Package stock holds the on-hand inventory entities: batches of a product in a
// store, the adjustments that move them, and the losses that write them off.
//
// Nothing here talks to a database or to HTTP. Quantities are money.Quantity and
// costs are money.Money, so every comparison in this aggregate is exact. The
// implementation this replaces compared float64 quantities with a 0.001 epsilon
// (billing.go:103), which is precisely the kind of "close enough" that lets a
// sale complete against stock that is not there.
package stock

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

// ErrInsufficientStock means the requested quantity is not available to be
// moved. It is a business outcome, not a failure: the caller reports it to the
// user and the transaction rolls back.
var ErrInsufficientStock = errors.New("insufficient stock")

// ErrInsufficientReserved means more was released than was ever reserved.
var ErrInsufficientReserved = errors.New("insufficient reserved quantity")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// Insufficient builds an ErrInsufficientStock carrying what was asked for and
// what was actually there, so the message a cashier sees names the shortfall
// rather than saying only "insufficient stock".
func Insufficient(requested, available money.Quantity) error {
	return fmt.Errorf("%w: %s requested but only %s is unreserved and on hand",
		ErrInsufficientStock, requested.StringFixed(), available.StringFixed())
}

// DefaultBatch is the batch number a product carries when the goods are not
// batch-tracked.
//
// It is the empty string, not a generated sentinel, because the schema declares
// batch_no as NOT NULL with an empty-string default, precisely so that the
// unique key (product_id, store_id, batch_no) bites for untracked goods where a
// NULL would have made every row distinct. The old handlers
// invented a per-second batch number ("ADJ-20060102150405") when none was
// given, which fragmented one product's on-hand quantity across a new row for
// every adjustment.
const DefaultBatch = ""

// ---------------------------------------------------------------------------
// Batches
// ---------------------------------------------------------------------------

// Batch is one lot of a product held in one store. The unique key is
// (ProductID, StoreID, BatchNo).
type Batch struct {
	ID          uuid.UUID      `json:"id"`
	ProductID   uuid.UUID      `json:"product_id"`
	StoreID     uuid.UUID      `json:"store_id"`
	BatchNo     string         `json:"batch_no"`
	Quantity    money.Quantity `json:"quantity"`
	ReservedQty money.Quantity `json:"reserved_qty"`
	ExpiryDate  *time.Time     `json:"expiry_date"`
	MfgDate     *time.Time     `json:"mfg_date"`
	// TotalCost is what this batch cost, in full. It is the stored figure and
	// it is conserved: receiving adds what was paid, consumption moves a
	// proportional share out and leaves the rest.
	TotalCost money.Money `json:"total_cost"`
	// CostPrice is TotalCost over Quantity, computed by the database as a
	// generated column. It is for display and valuation, and it carries more
	// decimal places than money does because it is a rate and not an amount:
	// a 30 kg sack at Rs.940 is Rs.31.33333333 a kilo, and rounding that to
	// paise is what used to lose ten paise a sack.
	CostPrice money.Rate `json:"cost_price"`
}

// Available is the quantity that may actually be moved: what is on hand less
// what is already spoken for.
//
// Every deduction in this aggregate is guarded against this figure and not
// against Quantity. That is not only politeness towards a reservation — the
// schema carries CHECK (reserved_qty <= quantity), so deducting into the
// reserved portion is a constraint violation rather than a business error.
func (b Batch) Available() money.Quantity { return b.Quantity.Sub(b.ReservedQty) }

// Receipt describes stock arriving into a batch: a manual addition, the
// destination side of a transfer, or the output of a unit conversion.
//
// A zero CostPrice means "do not reprice"; nil dates mean "leave any stored
// dates alone". Both matter because a receipt into an existing batch adds to
// it, and adding stock is not the same event as revaluing it.
type Receipt struct {
	ProductID uuid.UUID
	StoreID   uuid.UUID
	BatchNo   string
	Quantity  money.Quantity
	// TotalCost is what the goods on this receipt cost, in full — four sacks at
	// Rs.940 is Rs.3,760.00, stated and not derived. Nothing divides it by the
	// quantity on the way in, which is what makes the value conserved: a
	// per-unit figure for a 30 kg sack cannot be stored without losing paise.
	//
	// Zero means "do not reprice": a caller adding stock without stating a cost
	// should not write off the batch's existing cost basis.
	TotalCost  money.Money
	ExpiryDate *time.Time
	MfgDate    *time.Time
}

// StockLine is the store-wide listing read model: a batch with the product name
// the screen needs, so the caller does not fetch products row by row.
type StockLine struct {
	Batch
	ProductName string `json:"product_name"`
}

// LowStockItem is one product whose total on-hand quantity in a store has
// fallen to or below its reorder level.
type LowStockItem struct {
	ProductID    uuid.UUID      `json:"product_id"`
	Name         string         `json:"name"`
	MinStock     money.Quantity `json:"min_stock"`
	CurrentStock money.Quantity `json:"current_stock"`

	// Everything below turns a list of names into an order.
	//
	// A list saying "Sona Masoori Rice, 12 kg left, minimum 50" tells a buyer
	// nothing they can act on without looking up who supplies it, what a sack
	// holds and what one costs — three lookups per line, on a list that is
	// forty lines long the morning after a good weekend.

	// PreferredVendorID is who this is normally bought from. Nil means nobody
	// has said, and those lines are grouped together so somebody can.
	PreferredVendorID   *uuid.UUID `json:"preferred_vendor_id,omitempty"`
	PreferredVendorName string     `json:"preferred_vendor_name,omitempty"`

	// PurchaseUnitID is the unit the shop buys this in — the 25 kg sack, not
	// the kilo. Zero when the product has no purchasable unit at all, which is
	// a product nothing can be ordered for and is worth seeing.
	PurchaseUnitID    uuid.UUID      `json:"purchase_unit_id,omitempty"`
	PurchaseUnitLabel string         `json:"purchase_unit_label,omitempty"`
	QtyInBase         money.Quantity `json:"qty_in_base"`
	// LastCost is what one of that unit last cost in this store. Zero means
	// unpriced, and the line has to say so rather than offer an order at nil.
	LastCost money.Money `json:"last_cost"`

	// SuggestedUnits is how many buying units to order, rounded up, to bring
	// stock back to twice the reorder level. See SuggestOrder for why twice.
	SuggestedUnits money.Quantity `json:"suggested_units"`
}

// Shortfall is how much stock is missing against the reorder level.
func (i LowStockItem) Shortfall() money.Quantity {
	if i.CurrentStock.GreaterThan(i.MinStock) {
		return money.ZeroQuantity()
	}
	return i.MinStock.Sub(i.CurrentStock)
}

// OrderValue is what the suggested quantity would cost.
func (i LowStockItem) OrderValue() money.Money { return i.LastCost.Mul(i.SuggestedUnits) }

// reorderTarget is the level an order brings stock back to, as a multiple of
// the reorder level.
//
// Ordering back to exactly the reorder level means tripping it again on the
// next sale, so the list would be the same length tomorrow. Twice is the
// smallest multiple that does not do that. It is a starting figure and every
// line is editable — the shop knows its own turnover, and this only has to be
// better than a blank box.
const reorderTarget = 2

// SuggestOrder returns how many buying units bring stock back to the target,
// rounded UP to a whole unit.
//
// Up, not to nearest: a shop cannot order two thirds of a sack, and rounding
// down leaves the product below its reorder level the moment the delivery is
// put away — which is the one outcome the whole list exists to prevent.
func SuggestOrder(minStock, currentStock, qtyInBase money.Quantity) money.Quantity {
	if !qtyInBase.IsPositive() {
		return money.ZeroQuantity()
	}
	target := minStock
	for i := 1; i < reorderTarget; i++ {
		target = target.Add(minStock)
	}
	if !target.GreaterThan(currentStock) {
		return money.ZeroQuantity()
	}
	needed := target.Sub(currentStock)
	return money.QuantityFromDecimal(needed.Decimal().Div(qtyInBase.Decimal()).Ceil())
}

// ExpiringBatch is a batch on one of the two expiry reports.
//
// The reports are disjoint and DaysUntilExpiry says which one a batch came
// from: zero or positive on the expiring report, where the batch is still
// sellable and the count is how long that lasts, and negative on the expired
// report, where it is how long ago the batch went off. No batch appears on
// both.
type ExpiringBatch struct {
	Batch
	ProductName     string `json:"product_name"`
	DaysUntilExpiry int    `json:"days_until_expiry"`
}

// ---------------------------------------------------------------------------
// Adjustments
// ---------------------------------------------------------------------------

// AdjustmentType records why stock moved.
type AdjustmentType string

// The adjustment types this aggregate writes.
const (
	AdjustAdd         AdjustmentType = "add"
	AdjustRemove      AdjustmentType = "remove"
	AdjustReserve     AdjustmentType = "reserve"
	AdjustRelease     AdjustmentType = "release"
	AdjustConversion  AdjustmentType = "conversion"
	AdjustTransferOut AdjustmentType = "transfer_out"
	AdjustTransferIn  AdjustmentType = "transfer_in"
	AdjustLoss        AdjustmentType = "loss"
)

// Valid reports whether t is a type this aggregate knows how to write.
func (t AdjustmentType) Valid() bool {
	switch t {
	case AdjustAdd, AdjustRemove, AdjustReserve, AdjustRelease,
		AdjustConversion, AdjustTransferOut, AdjustTransferIn, AdjustLoss:
		return true
	}
	return false
}

// DefaultReason is the reason recorded when the caller supplies none. It
// reproduces the defaults the old handler applied inline.
func (t AdjustmentType) DefaultReason() string {
	switch t {
	case AdjustAdd:
		return "received"
	case AdjustRemove:
		return "count_variance"
	default:
		return string(t)
	}
}

// Adjustment is one row of the stock audit trail.
//
// Quantity is a SIGNED delta, which is a deliberate divergence from the old
// handlers: they stored the magnitude and left adjustment_type to carry the
// direction, so summing the column produced a meaningless figure. The schema
// comment on stock_adjustments.quantity says the column is left unconstrained
// exactly so a negative correction is expressible.
//
// For AdjustReserve and AdjustRelease the delta applies to reserved_qty rather
// than to on-hand quantity; Type says which.
type Adjustment struct {
	ID        uuid.UUID      `json:"id"`
	ProductID uuid.UUID      `json:"product_id"`
	StoreID   uuid.UUID      `json:"store_id"`
	BatchNo   string         `json:"batch_no"`
	Quantity  money.Quantity `json:"quantity"`
	Type      AdjustmentType `json:"adjustment_type"`
	Reason    string         `json:"reason"`
	UserID    uuid.UUID      `json:"user_id"`
	CreatedAt time.Time      `json:"created_at"`

	// ProductName is populated by the listing read model only.
	ProductName string `json:"product_name,omitempty"`
}

// NewAdjustment validates and returns an audit row ready to persist. delta is
// signed: negative for stock leaving, positive for stock arriving.
func NewAdjustment(productID, storeID, userID uuid.UUID, batchNo string,
	delta money.Quantity, t AdjustmentType, reason string) (Adjustment, error) {
	if productID == uuid.Nil {
		return Adjustment{}, invalid("product is required")
	}
	if storeID == uuid.Nil {
		return Adjustment{}, invalid("store is required")
	}
	if userID == uuid.Nil {
		return Adjustment{}, invalid("a user must be attributed for every stock adjustment")
	}
	if !t.Valid() {
		return Adjustment{}, invalid("unknown adjustment type %q", string(t))
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = t.DefaultReason()
	}
	return Adjustment{
		ID:        uuid.New(),
		ProductID: productID,
		StoreID:   storeID,
		BatchNo:   batchNo,
		Quantity:  delta,
		Type:      t,
		Reason:    reason,
		UserID:    userID,
	}, nil
}

// AdjustmentFilter narrows the audit listing.
type AdjustmentFilter struct {
	StoreID   uuid.UUID
	ProductID *uuid.UUID
	Limit     int
}

// ---------------------------------------------------------------------------
// Losses
// ---------------------------------------------------------------------------

// Loss is stock written off: damage, expiry, theft, spillage.
type Loss struct {
	ID        uuid.UUID      `json:"id"`
	ProductID uuid.UUID      `json:"product_id"`
	StoreID   uuid.UUID      `json:"store_id"`
	BatchNo   string         `json:"batch_no"`
	Quantity  money.Quantity `json:"quantity"`
	LossType  string         `json:"loss_type"`
	Reason    string         `json:"reason"`
	UserID    uuid.UUID      `json:"user_id"`
	CreatedAt time.Time      `json:"created_at"`

	// Consumed names the batches the loss was actually taken from. A loss
	// larger than any single batch spans several, and the old handler's
	// single-batch subquery silently deducted nothing in that case.
	Consumed []ConsumedBatch `json:"consumed,omitempty"`
}

// NewLoss validates and returns a loss ready to persist.
func NewLoss(productID, storeID, userID uuid.UUID, batchNo string,
	quantity money.Quantity, lossType, reason string) (Loss, error) {
	if productID == uuid.Nil {
		return Loss{}, invalid("product is required")
	}
	if storeID == uuid.Nil {
		return Loss{}, invalid("store is required")
	}
	if userID == uuid.Nil {
		return Loss{}, invalid("a user must be attributed for every inventory loss")
	}
	if err := ValidateMovement(quantity); err != nil {
		return Loss{}, err
	}
	lossType = strings.TrimSpace(lossType)
	if lossType == "" {
		return Loss{}, invalid("loss type is required")
	}
	return Loss{
		ID:        uuid.New(),
		ProductID: productID,
		StoreID:   storeID,
		BatchNo:   batchNo,
		Quantity:  quantity,
		LossType:  lossType,
		Reason:    strings.TrimSpace(reason),
		UserID:    userID,
	}, nil
}

// LossFilter narrows the loss listing.
type LossFilter struct {
	StoreID  uuid.UUID
	LossType string
	Limit    int
}

// ---------------------------------------------------------------------------
// Movement validation
// ---------------------------------------------------------------------------

// ValidateMovement rejects a quantity that cannot be moved.
//
// The sign check is the fix for a live defect: the old transfer handler
// (inventory.go:305) issued `SET quantity = quantity - ?` with the caller's
// number and no sign check, so a request body carrying -50 subtracted -50 from
// the source and added 50 to the destination. That mints stock out of nothing.
//
// The rounding check matters because the column is numeric(14,3): a request for
// 0.0004 units would be stored as 0.000 and quietly become a no-op that
// nonetheless reported success.
func ValidateMovement(q money.Quantity) error {
	if q.IsNegative() {
		return invalid("quantity cannot be negative, got %s", q.String())
	}
	if q.IsZero() {
		return invalid("quantity must be greater than zero")
	}
	if q.Round().IsZero() {
		return invalid("quantity %s is smaller than the smallest storable quantity (%d decimal places)",
			q.String(), money.QuantityScale)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Transacting in a unit other than the base one
// ---------------------------------------------------------------------------
//
// The Conversion type that used to sit here repacked a batch from one packing
// unit into another, dividing the source quantity by the target's size to
// create a second batch. It is gone, and so is stock/convert.
//
// Under one stock pool held in the product's base unit there is nothing to
// convert BETWEEN. Receiving ten sacks adds 250 kg to the pool and selling
// 250 g takes 0.25 away from the same pool; there is no separate "sack" balance
// to move stock into. What remains is the multiplication below, applied at the
// moment goods move rather than stored as a second pile.

// UnitFactor is how much of a product's base unit one of a given product unit
// amounts to: a 25 kg sack is 25, a 250 g pouch is 0.25.
//
// It is the stock aggregate's narrow view of catalogue.ProductUnit — this
// package needs the multiplier and the label for the audit line, and nothing
// else — which is what keeps stock from depending on the catalogue's types.
type UnitFactor struct {
	UnitID    uuid.UUID      `json:"product_unit_id"`
	ProductID uuid.UUID      `json:"product_id"`
	Label     string         `json:"label"`
	QtyInBase money.Quantity `json:"qty_in_base"`
}

// BaseQuantity converts an amount expressed in this unit into the product's
// stocking unit. Ten sacks of 25 kg is 250 kg.
//
// It multiplies and never divides, so ten sacks is exactly 250 rather than a
// figure that has been through a rate. A zero or negative factor is refused by
// the caller before this runs; the column's CHECK refuses it too.
func (f UnitFactor) BaseQuantity(unitQty money.Quantity) money.Quantity {
	return unitQty.Mul(f.QtyInBase.Decimal())
}

// Validate rejects a factor that cannot be transacted with. A unit worth zero
// of the stocking unit would make a receipt add nothing and a sale deduct
// nothing, while still printing on the paperwork as though goods had moved.
func (f UnitFactor) Validate() error {
	if !f.QtyInBase.IsPositive() {
		return invalid("one %s must be worth more than zero of the stocking unit, got %s",
			f.Label, f.QtyInBase.String())
	}
	return nil
}

// ---------------------------------------------------------------------------
// FEFO deduction
// ---------------------------------------------------------------------------

// DeductionRequest asks for a quantity of a product to leave a store.
type DeductionRequest struct {
	// AllowShortfall takes whatever is on the shelf when the shelf holds less
	// than was asked for, instead of refusing.
	//
	// It exists for exactly one case: a sale that has already happened. A till
	// that billed while the connection was down sold goods that left the shop,
	// and the shelf disagreeing on sync means the shelf is wrong, not the sale.
	// Refusing would leave a shop whose books disagree with its cash drawer,
	// which is the one thing this system exists to prevent. The caller records
	// the shortfall as an exception for somebody to count.
	//
	// Never set it for a sale being made now. Stock going short at the counter
	// is something a cashier can act on, and silently selling air is not.
	AllowShortfall bool

	ProductID uuid.UUID
	StoreID   uuid.UUID

	// BatchHint names a specific batch to try first — a scanned batch code, or
	// the batch an earlier sale recorded so a return goes back where it came
	// from. When the named batch cannot satisfy the whole quantity the FEFO
	// walk runs instead; the hint is an optimisation, never a constraint.
	BatchHint string

	Quantity money.Quantity
}

// Validate rejects a request that cannot be served.
func (r DeductionRequest) Validate() error {
	if r.ProductID == uuid.Nil {
		return invalid("product is required")
	}
	if r.StoreID == uuid.Nil {
		return invalid("store is required")
	}
	return ValidateMovement(r.Quantity)
}

// ConsumedBatch is one batch a deduction took from.
type ConsumedBatch struct {
	StockID  uuid.UUID      `json:"stock_id"`
	BatchNo  string         `json:"batch_no"`
	Quantity money.Quantity `json:"quantity"`
	// Cost is the value that actually left this batch, as the database computed
	// and debited it in the same statement as the quantity. It is not a unit
	// cost multiplied back up: doing that reintroduces the rounding that storing
	// a rate caused in the first place.
	Cost       money.Money `json:"cost"`
	ExpiryDate *time.Time  `json:"expiry_date"`
}

// UnitCost is Cost over Quantity, for display. Zero quantity reads as zero
// rather than panicking: a consumed batch with no quantity is not a line anyone
// needs a rate for.
func (c ConsumedBatch) UnitCost() money.Rate {
	if c.Quantity.IsZero() {
		return money.ZeroRate()
	}
	return money.RateFromDecimal(c.Cost.Decimal().DivRound(c.Quantity.Decimal(), 8))
}

// Deduction is the outcome of a FEFO/FIFO stock deduction.
type Deduction struct {
	// Batches are the batches consumed, in the order they were consumed:
	// earliest expiry first.
	Batches []ConsumedBatch `json:"batches"`

	// Quantity is the total deducted, which always equals the quantity
	// requested — a deduction that could not be satisfied in full is an error,
	// never a short fill.
	Quantity money.Quantity `json:"quantity"`

	// TotalCost is the cost of goods sold for this deduction.
	TotalCost money.Money `json:"total_cost"`

	// UnitCost is TotalCost over Quantity: the weighted-average cost to
	// snapshot on the invoice line.
	UnitCost money.Money `json:"unit_cost"`

	// Shortfall is what the shelf could not supply, and is non-zero only when
	// the request allowed it. A sale that has already happened is recorded with
	// the goods it moved and this figure says how much of it the books could not
	// account for — which is a stock count waiting to happen, not a sale to undo.
	Shortfall money.Quantity `json:"shortfall,omitempty"`
}

// PrimaryBatch is the batch to record against the movement when only one batch
// number can be stored. It is the first batch consumed, which under FEFO is the
// earliest-expiring one.
func (d Deduction) PrimaryBatch() string {
	if len(d.Batches) == 0 {
		return DefaultBatch
	}
	return d.Batches[0].BatchNo
}

// NewDeduction totals a set of consumed batches into a deduction.
//
// TotalCost is the sum of what each batch was actually debited, so it is exactly
// the money that left inventory — no rate is multiplied by a quantity anywhere
// in this path. That is what makes the cost of goods sold for a delivery add up
// to what the delivery cost.
//
// UnitCost is the weighted average, derived from that total for display and for
// the per-unit figure on the invoice line. It rounds; the total does not, and
// the total is what reports sum.
//
// It panics if quantity is zero, which callers prevent by validating the
// request; a per-unit cost over zero units is not a number a ledger can hold.
func NewDeduction(consumed []ConsumedBatch, quantity money.Quantity) Deduction {
	total := money.Zero()
	moved := money.ZeroQuantity()
	for _, c := range consumed {
		total = total.Add(c.Cost)
		moved = moved.Add(c.Quantity)
	}
	d := Deduction{
		Batches:   consumed,
		Quantity:  quantity,
		TotalCost: total,
		Shortfall: quantity.Sub(moved),
	}
	// The average is over what actually moved. Dividing by what was asked for
	// would understate the cost of every unit that did leave, in exactly the
	// case where the figures are already in doubt.
	if moved.IsPositive() {
		d.UnitCost = total.DivQuantity(moved)
	}
	return d
}

// ---------------------------------------------------------------------------
// Lock ordering
// ---------------------------------------------------------------------------

// InLockOrder returns the positions of productIDs sorted by product id, which
// is the order a transaction touching several products must move them in.
//
// # Why this exists
//
// Every stock movement takes a row lock: the FEFO walk holds the product's
// batches FOR UPDATE, and the receiving upsert locks the row it conflicts on.
// A transaction that moves two products therefore holds one product's locks
// while it asks for the other's — and two transactions that disagree about
// which to take first deadlock.
//
// That is not a theoretical hazard, it is the ordinary shape of a Saturday
// morning. One customer buys rice and then soap while the next buys soap and
// then rice; the storekeeper books in a delivery of both while the counter is
// billing them. Postgres resolves the cycle by killing one transaction with
// SQLSTATE 40P01 a full deadlock_timeout later, which reaches the cashier as a
// failed sale with the customer standing there and nothing in the message they
// can act on. Measured against the real database, roughly half of the
// overlapping multi-line sales failed this way.
//
// Taking the locks in one agreed order removes the cycle rather than detecting
// it. Product id is an arbitrary but total order, it is stable across processes
// and restarts, and it needs no coordination: two transactions that both follow
// it can queue behind one another but can never wait on each other.
//
// The result is a permutation of indices rather than a reordered slice, so a
// caller can write its results back into their original positions and the
// order the customer's bill prints in is unaffected by the order the shelf was
// touched in.
//
// Equal ids keep their original relative order, so two lines of the same
// product are still applied in the sequence the request stated them.
func InLockOrder(productIDs []uuid.UUID) []int {
	order := make([]int, len(productIDs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return bytes.Compare(productIDs[order[a]][:], productIDs[order[b]][:]) < 0
	})
	return order
}
