// Package reconcile holds what the server could not verify about a sale it had
// to accept anyway.
//
// The rule this package exists for: a sale that has already happened is never
// rejected on sync, only flagged. A till that billed while the connection was
// down sold goods that left the shop and took money that is in the drawer. If
// the shelf now disagrees, the shelf is wrong; if a customer went past a credit
// limit the till could not check, the debt is real. Refusing either would leave
// a shop whose books disagree with its cash drawer, which is the one thing this
// system exists to prevent.
//
// So the sale lands and an exception is recorded. The exception is not an error
// and not a warning — it is a task: somebody counts the shelf, or rings the
// customer. Resolving one is a person saying they have done that, and it is
// deliberately not a delete: a count of these over a month is the signal that a
// shop's offline window is too long or its stock counts are wrong.
package reconcile

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrValidation marks a badly formed exception.
var ErrValidation = errors.New("validation failed")

// Kind is what could not be verified.
type Kind string

const (
	// Oversold: the shelf held less than the sale took. The goods are gone
	// either way; the question is what the shelf actually has.
	Oversold Kind = "oversold"
	// CreditLimitExceeded: the sale took the customer past the limit the till
	// last saw. The debt is real and somebody has to decide what to do about it.
	CreditLimitExceeded Kind = "credit_limit_exceeded"
	// PriceChanged: the till priced from a snapshot the shop has since changed.
	// The customer paid what the receipt says, so the receipt stands.
	PriceChanged Kind = "price_changed"
	// StaleCatalogue: the till's snapshot was older than the shop allows.
	StaleCatalogue Kind = "stale_catalogue"
	// OverReturned: more came back than the invoice sold, or the invoice never
	// sold it at all. The money has gone over the counter either way.
	OverReturned Kind = "over_returned"
)

// Valid reports whether k is a kind this system records.
func (k Kind) Valid() bool {
	switch k {
	case Oversold, CreditLimitExceeded, PriceChanged, StaleCatalogue, OverReturned:
		return true
	}
	return false
}

// Exception is one thing about one sale that somebody has to look at.
type Exception struct {
	ID         uuid.UUID  `json:"id"`
	StoreID    uuid.UUID  `json:"store_id"`
	TerminalID *uuid.UUID `json:"terminal_id"`
	InvoiceID  *uuid.UUID `json:"invoice_id"`
	ProductID  *uuid.UUID `json:"product_id"`
	CustomerID *uuid.UUID `json:"customer_id"`

	Kind Kind `json:"kind"`
	// Expected is what the till believed and Actual what the server found, both
	// as text because the pair is read by a person and not computed with.
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// Detail says what to do about it, in the words somebody at the shop would
	// use. An exception nobody can act on is noise.
	Detail string `json:"detail"`

	// ProductName and InvoiceNo ride along on the read paths so the screen can
	// name what it is talking about without a lookup per row.
	ProductName string `json:"product_name,omitempty"`
	InvoiceNo   string `json:"invoice_no,omitempty"`

	ResolvedAt   *time.Time `json:"resolved_at"`
	ResolvedBy   *uuid.UUID `json:"resolved_by"`
	ResolvedNote string     `json:"resolved_note"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Resolved reports whether somebody has dealt with this.
func (e Exception) Resolved() bool { return e.ResolvedAt != nil }

// New builds an exception, refusing one that names nothing actionable.
func New(storeID uuid.UUID, kind Kind, detail string) (Exception, error) {
	if storeID == uuid.Nil {
		return Exception{}, fmt.Errorf("%w: an exception belongs to a store", ErrValidation)
	}
	if !kind.Valid() {
		return Exception{}, fmt.Errorf("%w: %q is not a kind of exception this system records", ErrValidation, kind)
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return Exception{}, fmt.Errorf("%w: an exception says what somebody should do about it", ErrValidation)
	}
	return Exception{ID: uuid.New(), StoreID: storeID, Kind: kind, Detail: detail}, nil
}

// Filter narrows a listing.
type Filter struct {
	StoreID uuid.UUID
	// IncludeResolved lists the ones already dealt with. The screen defaults to
	// the open ones, which is the working list.
	IncludeResolved bool
	Kind            Kind
	Limit           int
}
