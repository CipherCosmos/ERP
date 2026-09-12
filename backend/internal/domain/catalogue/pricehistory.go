package catalogue

import (
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// Price history.
//
// The question this answers is "why is this ₹45 when it was ₹42 last week",
// which nothing could answer before: product_prices holds one row per product,
// store and unit, and every write replaced it, so the price the shop is
// charging today was the only price the system had ever heard of.
//
// A recorded change is the state after it, not the difference. The reader
// derives what moved by comparing adjacent rows, which means there is one
// source of truth per row and no stored delta that can disagree with the
// figures either side of it.

// PriceSource says how a price came to be what it is.
type PriceSource string

const (
	// PriceOpening is the price a product was created with — where it started,
	// which is a fact rather than an absence.
	PriceOpening PriceSource = "opening"
	// PriceManual is somebody typing it: the product form or the price screen.
	PriceManual PriceSource = "manual"
	// PriceImport is a bulk catalogue sheet.
	PriceImport PriceSource = "import"
)

// Valid reports whether this is a source the database will accept. The column
// is an enum, so an unknown value is refused there too — this catches it a
// layer earlier, with a sentence instead of a constraint name.
func (s PriceSource) Valid() bool {
	switch s {
	case PriceOpening, PriceManual, PriceImport:
		return true
	}
	return false
}

// PriceAuthor is the context a price write carries beyond the figures: who
// changed it, how, and why if they said.
type PriceAuthor struct {
	// UserID is uuid.Nil when nobody is behind the write — the first-run
	// bootstrap. The column is nullable for exactly that case, because
	// recording a fictional author is worse than recording none.
	UserID uuid.UUID
	Source PriceSource
	Note   string
}

// Validate rejects an author a history row could not be written from.
func (a PriceAuthor) Validate() error {
	if !a.Source.Valid() {
		return invalid("%q is not a way a price can be set", a.Source)
	}
	if len(a.Note) > MaxPriceNoteLength {
		return invalid("a price note may be at most %d characters", MaxPriceNoteLength)
	}
	return nil
}

// MaxPriceNoteLength bounds the reason somebody gives for a price change. It is
// a line, not a document.
const MaxPriceNoteLength = 200

// PriceChange is one recorded state of one price.
type PriceChange struct {
	ID            uuid.UUID `json:"id"`
	ProductID     uuid.UUID `json:"product_id"`
	StoreID       uuid.UUID `json:"store_id"`
	ProductUnitID uuid.UUID `json:"product_unit_id"`
	// ProductName and UnitLabel ride along so a store-wide list of price changes
	// reads without a lookup per row, and so a history covering several units of
	// the same product says which unit each entry is about.
	ProductName string `json:"product_name"`
	UnitLabel   string `json:"unit_label"`

	MRP                money.Money `json:"mrp"`
	CostPrice          money.Money `json:"cost_price"`
	SellPrice          money.Money `json:"sell_price"`
	WholesaleSellPrice money.Money `json:"wholesale_sell_price"`

	ChangedBy     *uuid.UUID  `json:"changed_by,omitempty"`
	ChangedByName string      `json:"changed_by_name,omitempty"`
	Source        PriceSource `json:"source"`
	Note          string      `json:"note,omitempty"`
	At            time.Time   `json:"at"`
}

// Margin is what the shop makes on one of this unit at the selling price.
func (c PriceChange) Margin() money.Money { return c.SellPrice.Sub(c.CostPrice) }

// PriceMovement is one entry in a history with the previous figures alongside,
// so a row reads as a change rather than as a state.
//
// The previous values are derived here rather than stored, which is what keeps
// a history from being able to contradict itself.
type PriceMovement struct {
	PriceChange
	// Previous is nil on the earliest entry — the price did not change into
	// this, it started here.
	Previous *PriceChange `json:"previous,omitempty"`
}

// SellChange is how much the selling price moved. Zero when nothing moved or
// when this is where the price started.
func (m PriceMovement) SellChange() money.Money {
	if m.Previous == nil {
		return money.Zero()
	}
	return m.SellPrice.Sub(m.Previous.SellPrice)
}

// CostChange is how much the cost moved.
func (m PriceMovement) CostChange() money.Money {
	if m.Previous == nil {
		return money.Zero()
	}
	return m.CostPrice.Sub(m.Previous.CostPrice)
}

// AsMovements pairs each entry with the one before it in its own series.
//
// Two things about this are easy to get wrong and were both got wrong first.
//
// The input is newest first, which is the order the history is read in and the
// order a screen wants. The previous entry is therefore the NEXT element, not
// the one before it — and reading it the other way turns every rise into a
// fall, which looks right until somebody checks a number.
//
// And a series is one unit of one product. A history covering a product sold
// loose and by the sack interleaves both, so the row under the sack's price is
// usually the loose price — pairing them reports the sack as having risen by
// the difference between a sack and a kilo, which is not a price change that
// ever happened. The previous entry is the next one with the same unit.
func AsMovements(newestFirst []PriceChange) []PriceMovement {
	movements := make([]PriceMovement, len(newestFirst))
	// The most recent entry already seen for each series, walking backwards
	// through time — which, in a newest-first list, means walking from the end.
	for i := len(newestFirst) - 1; i >= 0; i-- {
		movements[i] = PriceMovement{PriceChange: newestFirst[i]}
	}
	seen := make(map[uuid.UUID]int, len(newestFirst))
	for i := len(newestFirst) - 1; i >= 0; i-- {
		key := newestFirst[i].ProductUnitID
		if earlier, ok := seen[key]; ok {
			previous := newestFirst[earlier]
			movements[i].Previous = &previous
		}
		seen[key] = i
	}
	return movements
}
