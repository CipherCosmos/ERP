// Package salesreturn holds the two ways goods move without a sale: a customer
// bringing something back off an invoice, and stock written off as damaged,
// wasted, stolen or expired.
//
// They live together because they are the same shape of event — inventory
// crossing the shop's boundary outside the till — and because the code this
// replaces kept them in one handler file.
//
// Nothing here talks to a database or to HTTP. Quantities are money.Quantity
// and amounts are money.Money, so every comparison in this aggregate is exact.
// The implementation this replaces compared float64 amounts and quantities, and
// wrote `fmt.Sprintf("qty=%.3f amount=%.2f", ...)` into the audit trail from
// values that had already lost precision.
package salesreturn

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// ---------------------------------------------------------------------------
// Customer returns
// ---------------------------------------------------------------------------

// Status is the lifecycle of a customer return. It matches the return_status
// enum exactly, which has two values and no more.
//
// # The gap this type is honest about
//
// There is no rejected, cancelled or void status, because the enum does not
// have one. A return raised in error can therefore only be left pending
// forever; nothing in the schema can record that a manager looked at it and
// said no. That is a real gap in the data model, and it is named here rather
// than papered over: inventing a third status in Go would either fail against
// the enum at INSERT time or require a migration that is not this change's to
// make. Closing it means adding a value to return_status (and, to be useful, a
// rejected_by/rejected_reason pair) in a migration of its own.
type Status string

// The statuses the return_status enum declares.
const (
	// StatusPending is a return that has been recorded but whose goods have not
	// moved. Nothing about the shop's stock position has changed yet.
	StatusPending Status = "pending"

	// StatusCompleted is a return that has been approved. Approval is the event
	// that puts the goods back on the shelf, which is precisely why it must
	// happen at most once.
	StatusCompleted Status = "completed"
)

// Valid reports whether s is a status the enum holds.
func (s Status) Valid() bool {
	return s == StatusPending || s == StatusCompleted
}

// Approved reports whether the goods have already gone back into stock.
func (s Status) Approved() bool { return s == StatusCompleted }

// Return is a customer bringing goods back.
//
// InvoiceID is optional: a shop takes back goods on a bill it can find and,
// occasionally, goods it cannot. When it is set, the return is anchored to a
// real sale and the goods can go back into the batch they actually left on.
type Return struct {
	ID        uuid.UUID  `json:"id"`
	InvoiceID *uuid.UUID `json:"invoice_id"`
	ProductID uuid.UUID  `json:"product_id"`
	StoreID   uuid.UUID  `json:"store_id"`

	Quantity money.Quantity `json:"quantity"`
	Amount   money.Money    `json:"amount"`

	Reason string `json:"reason"`
	// RefundMode is how the customer was made whole — cash, card reversal, a
	// credit note. It is free text in the schema and is not constrained here;
	// blank means the shop recorded none.
	RefundMode string `json:"refund_mode"`

	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	// RestoredBatch names the batch the goods went back into. It is filled in
	// by an approval and is not a stored column: the returns table has neither
	// a batch_no nor an invoice_item_id, so the link to a physical lot is
	// derived at approval time from the invoice line. Blank means either that
	// this return has not been approved, or that the product is not
	// batch-tracked and went back into its default lot.
	RestoredBatch string `json:"restored_batch,omitempty"`
}

// ReturnInput is a return as a caller asks for it.
type ReturnInput struct {
	InvoiceID  *uuid.UUID
	ProductID  uuid.UUID
	StoreID    uuid.UUID
	Quantity   money.Quantity
	Amount     money.Money
	Reason     string
	RefundMode string
}

// NewReturn validates and returns a pending return ready to persist.
//
// A return is always born pending. There is no path that creates one already
// completed, because completion is what moves the stock and stock only moves
// through Approve.
func NewReturn(in ReturnInput) (Return, error) {
	if in.ProductID == uuid.Nil {
		return Return{}, invalid("product is required")
	}
	if in.StoreID == uuid.Nil {
		return Return{}, invalid("store is required")
	}
	if in.InvoiceID != nil && *in.InvoiceID == uuid.Nil {
		return Return{}, invalid("invoice is set but empty")
	}
	if err := ValidateQuantity(in.Quantity); err != nil {
		return Return{}, err
	}
	// The schema carries CHECK (quantity >= 0 AND amount >= 0). A zero refund is
	// legitimate — an exchange, or goods taken back as a goodwill gesture — so
	// only a negative amount is refused, and it is refused here with a message
	// rather than at the database with a SQLSTATE.
	if in.Amount.IsNegative() {
		return Return{}, invalid("refund amount cannot be negative, got %s", in.Amount)
	}
	if !in.Amount.Equal(in.Amount.Round()) {
		return Return{}, invalid("refund amount %s is finer than a paisa", in.Amount)
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return Return{}, invalid("a reason is required for every return")
	}

	return Return{
		ID:         uuid.New(),
		InvoiceID:  in.InvoiceID,
		ProductID:  in.ProductID,
		StoreID:    in.StoreID,
		Quantity:   in.Quantity,
		Amount:     in.Amount,
		Reason:     reason,
		RefundMode: strings.TrimSpace(in.RefundMode),
		Status:     StatusPending,
	}, nil
}

// ReturnFilter narrows a listing of customer returns.
type ReturnFilter struct {
	StoreID   uuid.UUID
	ProductID *uuid.UUID
	InvoiceID *uuid.UUID
	// Status empty means every status.
	Status Status
	Limit  int
}

// ---------------------------------------------------------------------------
// Inventory losses
// ---------------------------------------------------------------------------

// Loss is stock written off outside a sale: damage, wastage, expiry, theft.
//
// LossType is free text in the schema and is not constrained to an enum here.
// The values the shop uses today ("damage", "wastage", "expiry", "theft") are a
// reporting convention, not an invariant, and turning a convention into a Go
// enum would reject rows the database is perfectly willing to hold.
type Loss struct {
	ID        uuid.UUID `json:"id"`
	ProductID uuid.UUID `json:"product_id"`
	StoreID   uuid.UUID `json:"store_id"`

	// BatchNo is the lot the write-off was actually taken from. On the way in
	// it is a hint; on the way out it is the first batch the deduction
	// consumed, which is not necessarily the one that was asked for.
	BatchNo string `json:"batch_no"`

	Quantity money.Quantity `json:"quantity"`
	LossType string         `json:"loss_type"`
	Reason   string         `json:"reason"`
	// UserID attributes the write-off. The schema makes it NOT NULL because an
	// unattributed write-off is indistinguishable from theft.
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`

	// Batches names every lot the write-off actually came out of. It is not a
	// stored column — inventory_losses holds a single batch_no — and exists so
	// the caller can see what a multi-batch write-off consumed. A loss larger
	// than any single batch spans several, and the single-batch subquery the
	// old handler used silently deducted nothing in exactly that case.
	Batches []Allocation `json:"batches,omitempty"`
}

// Value is what the written-off goods cost the business, summed over the lots
// they actually came from. It is zero when the allocation is not known, which
// is the case for a loss read back from the database.
func (l Loss) Value() money.Money {
	total := money.Zero()
	for _, a := range l.Batches {
		total = total.Add(a.Cost)
	}
	return total
}

// Allocation is one lot a write-off was taken from.
type Allocation struct {
	BatchNo  string         `json:"batch_no"`
	Quantity money.Quantity `json:"quantity"`
	// Cost is the value that actually left this lot, as the stock aggregate
	// debited it. It is carried rather than recomputed from a rate: the value a
	// batch holds is the stored figure, and multiplying a per-unit cost back up
	// by a quantity is what used to lose paise on every pack size that does not
	// divide evenly.
	Cost money.Money `json:"cost"`
}

// UnitCost is Cost over Quantity, for display.
func (a Allocation) UnitCost() money.Rate {
	if a.Quantity.IsZero() {
		return money.ZeroRate()
	}
	return money.RateFromDecimal(a.Cost.Decimal().DivRound(a.Quantity.Decimal(), 8))
}

// LossInput is a write-off as a caller asks for it.
type LossInput struct {
	ProductID uuid.UUID
	StoreID   uuid.UUID
	UserID    uuid.UUID
	// BatchNo names a lot to take the goods from first. It is a hint, never a
	// constraint: when the named lot cannot cover the whole quantity the
	// deduction falls through to a first-expired-first-out walk. Blank means no
	// hint at all.
	//
	// Blank is deliberately *not* turned into a generated batch number. The old
	// handler wrote "LOSS-20060102150405" when none was given, which invented a
	// new lot per second and fragmented a product's on-hand quantity across
	// rows that no report could reassemble.
	BatchNo  string
	Quantity money.Quantity
	LossType string
	Reason   string
}

// NewLoss validates and returns a write-off ready to persist.
func NewLoss(in LossInput) (Loss, error) {
	if in.ProductID == uuid.Nil {
		return Loss{}, invalid("product is required")
	}
	if in.StoreID == uuid.Nil {
		return Loss{}, invalid("store is required")
	}
	if in.UserID == uuid.Nil {
		return Loss{}, invalid("a user must be attributed for every inventory loss")
	}
	if err := ValidateQuantity(in.Quantity); err != nil {
		return Loss{}, err
	}
	lossType := strings.TrimSpace(in.LossType)
	if lossType == "" {
		return Loss{}, invalid("a loss type is required — what kind of loss this was")
	}

	return Loss{
		ID:        uuid.New(),
		ProductID: in.ProductID,
		StoreID:   in.StoreID,
		BatchNo:   in.BatchNo,
		Quantity:  in.Quantity,
		LossType:  lossType,
		Reason:    strings.TrimSpace(in.Reason),
		UserID:    in.UserID,
	}, nil
}

// LossFilter narrows a listing of inventory losses.
type LossFilter struct {
	StoreID   uuid.UUID
	ProductID *uuid.UUID
	LossType  string
	Limit     int
}

// ---------------------------------------------------------------------------
// Shared validation
// ---------------------------------------------------------------------------

// ValidateQuantity rejects a quantity that cannot be moved.
//
// The sign check matters because both movements in this aggregate are relative
// updates against on-hand stock. A negative return quantity would subtract from
// the shelf while telling the customer their goods were taken back; a negative
// loss would mint stock out of a write-off.
//
// The rounding check matters because the column is numeric(14,3): a request for
// 0.0004 units stores as 0.000 and becomes a no-op that nonetheless reports
// success.
func ValidateQuantity(q money.Quantity) error {
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
