// Package payment holds how a sale was settled: the tenders taken against an
// invoice, and the day's settlement figures a shop counts its cash drawer
// against.
//
// # Why this package exists
//
// The implementation it replaces accepted a split payment whose parts summed
// to within five paise of the invoice total:
//
//	if math.Abs(totalPaid-invTotal) > 0.05 { ... }
//
// That tolerance was never a business rule. No Indian payment instrument moves
// a fraction of a paisa, so a split that does not sum exactly is either a
// client bug or a miscount at the counter — and either way the shop is short
// or over by real money at the end of the day. The slop existed only because
// float64 arithmetic could not be trusted to agree with itself. With exact
// decimal money there is nothing left for it to paper over, so Settle requires
// equality and says which way the tender is out when it is not.
//
// The settlement vocabulary is billing.PaymentMode, not a second list. The
// invoice_payments.payment_mode column is free text in the schema, which is
// how "phonepe", "GPay " and "" got into it; NormaliseMode folds those onto
// the closed vocabulary on the way out, and anything it does not recognise
// lands on ModeOther rather than being quietly added to the cash total.
package payment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

// ErrAmountMismatch means the tenders do not sum to the invoice total. It is
// distinct from ErrValidation because the counter's response differs: a
// mismatch is corrected by recounting the money, not by fixing the request.
var ErrAmountMismatch = errors.New("the payments do not add up to the invoice total")

// ErrAlreadySettled means this invoice already has payments recorded against
// it. Settling twice would double the day's takings.
var ErrAlreadySettled = errors.New("payments have already been recorded for this invoice")

// ErrNotSettleable means the invoice cannot take a payment, because it was
// voided. There is nothing to settle: the goods went back on the shelf.
var ErrNotSettleable = errors.New("a voided invoice cannot be settled")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// ---------------------------------------------------------------------------
// Tenders
// ---------------------------------------------------------------------------

// Payment is one tender recorded against an invoice: this much, this way.
type Payment struct {
	ID        uuid.UUID           `json:"id"`
	InvoiceID uuid.UUID           `json:"invoice_id"`
	Mode      billing.PaymentMode `json:"payment_mode"`
	Amount    money.Money         `json:"amount"`
	Reference string              `json:"reference"`
	CreatedAt time.Time           `json:"created_at"`
}

// TenderInput is one requested part of a settlement, before it is written.
type TenderInput struct {
	Mode billing.PaymentMode
	// Amount is what was taken by this method. It must be positive: a
	// zero-value tender records nothing and would only inflate the mode counts
	// on the settlement report.
	Amount money.Money
	// Reference is the card slip number, UPI transaction id or cheque number.
	// Optional, and free text by nature — it comes off a printed receipt.
	Reference string
}

// IsTender reports whether m is a way money actually crosses the counter.
//
// Two members of billing.ValidPaymentModes are deliberately not tenders:
//
//   - "split" describes an invoice that has tenders, so a tender that is
//     itself "split" is a category error.
//   - "credit" is the absence of settlement. Recording it here would put money
//     in the day's takings that never entered the drawer, and would not create
//     the receivable that a credit sale actually needs. That belongs to the
//     credit aggregate, which the invoice's own payment_mode routes it to.
func IsTender(m billing.PaymentMode) bool {
	switch m {
	case billing.PaymentCash, billing.PaymentCard, billing.PaymentUPI,
		billing.PaymentBank, billing.PaymentCheque:
		return true
	}
	return false
}

// Settle turns a set of requested tenders into the payments to write, refusing
// any set that does not sum to the invoice total exactly.
//
// The equality is the point of this function. See the package documentation
// for why the five-paise tolerance it replaces was never a rule.
func Settle(invoiceID uuid.UUID, total money.Money, tenders []TenderInput, at time.Time) ([]Payment, error) {
	if invoiceID == uuid.Nil {
		return nil, invalid("a payment needs an invoice")
	}
	if len(tenders) == 0 {
		return nil, invalid("a settlement needs at least one payment")
	}
	if total.IsNegative() {
		return nil, invalid("an invoice total cannot be negative, got %s", total)
	}

	payments := make([]Payment, len(tenders))
	paid := money.Zero()
	for i, t := range tenders {
		if !t.Mode.Valid() {
			return nil, invalid("payment %d: %q is not a payment mode", i+1, t.Mode)
		}
		if !IsTender(t.Mode) {
			return nil, invalid("payment %d: %q is not a way money is taken at the counter", i+1, t.Mode)
		}
		if !t.Amount.IsPositive() {
			return nil, invalid("payment %d: an amount of %s settles nothing", i+1, t.Amount)
		}
		payments[i] = Payment{
			ID:        uuid.New(),
			InvoiceID: invoiceID,
			Mode:      t.Mode,
			Amount:    t.Amount,
			Reference: strings.TrimSpace(t.Reference),
			CreatedAt: at,
		}
		paid = paid.Add(t.Amount)
	}

	// Exact, and the message says which way it is out, because "short" and
	// "over" call for different actions at the counter.
	if diff := paid.Sub(total); !diff.IsZero() {
		if diff.IsNegative() {
			return nil, fmt.Errorf("%w: the payments come to %s, which is %s short of the invoice total %s",
				ErrAmountMismatch, paid, diff.Neg(), total)
		}
		return nil, fmt.Errorf("%w: the payments come to %s, which is %s over the invoice total %s",
			ErrAmountMismatch, paid, diff, total)
	}
	return payments, nil
}

// ---------------------------------------------------------------------------
// Settlement report
// ---------------------------------------------------------------------------

// ModeOther is the line a recorded mode lands on when it is not one the system
// knows. It is deliberately outside billing's closed vocabulary, because that
// is exactly what it means.
//
// The implementation this replaces added unrecognised modes to the cash total
// "for settlement visibility", which is the opposite of visible: it raised the
// cash the drawer was expected to hold by an amount that was never cash.
const ModeOther billing.PaymentMode = "other"

// CanonicalModes is the order a settlement sheet reads in — the money that
// moves fastest first, and what was not settled at all last. Every one of
// these appears on the report even when its total is zero, so the shape of the
// sheet does not change from day to day.
var CanonicalModes = []billing.PaymentMode{
	billing.PaymentCash,
	billing.PaymentCard,
	billing.PaymentUPI,
	billing.PaymentBank,
	billing.PaymentCheque,
	billing.PaymentCredit,
}

// modeAliases folds the free-text values that reached invoice_payments.payment_mode
// and invoices.payment_mode onto the closed vocabulary. The keys are lowercase
// and trimmed; NormaliseMode does that normalisation.
var modeAliases = map[string]billing.PaymentMode{
	"cash": billing.PaymentCash,

	"card":        billing.PaymentCard,
	"debit":       billing.PaymentCard,
	"debit_card":  billing.PaymentCard,
	"credit_card": billing.PaymentCard,
	"creditcard":  billing.PaymentCard,

	"upi":       billing.PaymentUPI,
	"phonepe":   billing.PaymentUPI,
	"gpay":      billing.PaymentUPI,
	"googlepay": billing.PaymentUPI,
	"paytm":     billing.PaymentUPI,

	"bank":          billing.PaymentBank,
	"bank_transfer": billing.PaymentBank,
	"neft":          billing.PaymentBank,
	"rtgs":          billing.PaymentBank,
	"imps":          billing.PaymentBank,
	"netbanking":    billing.PaymentBank,

	"cheque": billing.PaymentCheque,
	"check":  billing.PaymentCheque,
	"dd":     billing.PaymentCheque,

	"credit": billing.PaymentCredit,
	"udhar":  billing.PaymentCredit,
}

// NormaliseMode maps a recorded mode onto the line of the report it belongs on.
//
// The second result is false when the row must not be counted at all. That
// happens for "split", which on an invoice header means "this sale has tender
// rows" — and if the header says split while no tender rows exist, the sale is
// genuinely unsettled. Counting the header figure would hide that; leaving it
// out surfaces it in Settlement.Unsettled, which is where it belongs.
func NormaliseMode(raw string) (billing.PaymentMode, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == string(billing.PaymentSplit) {
		return "", false
	}
	if m, ok := modeAliases[key]; ok {
		return m, true
	}
	return ModeOther, true
}

// ModeLine is one line of the settlement report.
type ModeLine struct {
	Mode   billing.PaymentMode `json:"payment_mode"`
	Amount money.Money         `json:"amount"`
	// Count is how many tenders made up this line, which is what a cashier
	// checks a card terminal's batch report against.
	Count int `json:"count"`
}

// TenderGroup is one grouped row of recorded settlement, as the database
// returns it: a raw mode string that has not yet been folded onto the
// vocabulary.
type TenderGroup struct {
	Mode   string
	Amount money.Money
	Count  int
}

// SettlementInput is everything the report is computed from.
type SettlementInput struct {
	StoreID uuid.UUID
	// Day is the trading day being reconciled.
	Day time.Time
	// TotalSales and Invoices cover the day's completed sales. Voided sales
	// are excluded upstream and must stay excluded: see Summarise.
	TotalSales money.Money
	Invoices   int
	// Tenders is the day's settlement, grouped by recorded mode.
	Tenders []TenderGroup
	// Refunds is what went back over the counter that day.
	Refunds money.Money
}

// Settlement is a day's takings, broken down by how they were settled.
//
// Every figure below is derived from the ones above it, and the derivations
// are exact:
//
//	Settled   = sum of Modes
//	Collected = Settled - the credit line
//	Net       = Collected - Refunds
//	Unsettled = TotalSales - Settled
type Settlement struct {
	// Date is the trading day, as 2006-01-02.
	Date    string    `json:"date"`
	StoreID uuid.UUID `json:"store_id"`

	// TotalSales is what the day's completed invoices came to.
	TotalSales money.Money `json:"total_sales"`
	// Invoices is how many completed invoices that was.
	Invoices int `json:"total_invoices"`

	// Modes is the breakdown, in CanonicalModes order.
	Modes []ModeLine `json:"modes"`

	// Settled is everything accounted for by a recorded mode, credit included.
	Settled money.Money `json:"settled"`
	// OnCredit is the part of the day that was not settled today at all — it
	// became a receivable instead.
	OnCredit money.Money `json:"on_credit"`
	// Collected is the money that actually moved: Settled less credit. This is
	// the figure the drawer, the card batch and the UPI statement add up to.
	Collected money.Money `json:"collected"`

	// Refunds is what was paid back over the counter.
	Refunds money.Money `json:"refunds_total"`
	// Net is what the business is left holding: Collected less Refunds.
	Net money.Money `json:"net_collections"`

	// Unsettled is the day's sales that no recorded mode accounts for. It is
	// normally zero. A non-zero figure is a real finding — an invoice marked
	// "split" whose tender rows were never written, or legacy rows that do not
	// sum to their invoice — and it is reported rather than absorbed.
	Unsettled money.Money `json:"unsettled"`
}

// Amount returns what was settled in one mode, zero if nothing was.
func (s Settlement) Amount(m billing.PaymentMode) money.Money {
	for _, line := range s.Modes {
		if line.Mode == m {
			return line.Amount
		}
	}
	return money.Zero()
}

// Count returns how many tenders were recorded in one mode.
func (s Settlement) Count(m billing.PaymentMode) int {
	for _, line := range s.Modes {
		if line.Mode == m {
			return line.Count
		}
	}
	return 0
}

// Summarise builds the day's settlement from its parts.
//
// It replaces eleven parallel accumulators over two queries with one fold. The
// arithmetic is exact decimal throughout, so the report's own parts sum to its
// own net figure with nothing left over — which is the property that makes it
// usable for reconciling a cash drawer at all.
//
// Voided sales are absent from every figure here, because they are filtered
// out of TotalSales and Tenders before this is called. That is deliberate and
// load-bearing: a voided sale's money was handed back or never taken, so
// counting its tenders would tell the shopkeeper to expect cash the drawer
// does not hold. The tender rows survive the void as history — they record
// what happened before the cancellation — but they are not takings.
func Summarise(in SettlementInput) Settlement {
	amounts := make(map[billing.PaymentMode]money.Money, len(CanonicalModes)+1)
	counts := make(map[billing.PaymentMode]int, len(CanonicalModes)+1)

	settled := money.Zero()
	for _, g := range in.Tenders {
		mode, counted := NormaliseMode(g.Mode)
		if !counted {
			continue
		}
		amounts[mode] = amounts[mode].Add(g.Amount)
		counts[mode] += g.Count
		settled = settled.Add(g.Amount)
	}

	modes := make([]ModeLine, 0, len(CanonicalModes)+1)
	for _, m := range CanonicalModes {
		modes = append(modes, ModeLine{Mode: m, Amount: amounts[m], Count: counts[m]})
	}
	// "other" appears only when something landed on it, because a line for a
	// vocabulary that was not used is noise — but hiding it when it was used
	// would be the very defect this replaces.
	if counts[ModeOther] > 0 || !amounts[ModeOther].IsZero() {
		modes = append(modes, ModeLine{Mode: ModeOther, Amount: amounts[ModeOther], Count: counts[ModeOther]})
	}

	onCredit := amounts[billing.PaymentCredit]
	collected := settled.Sub(onCredit)

	return Settlement{
		Date:       in.Day.Format(DateLayout),
		StoreID:    in.StoreID,
		TotalSales: in.TotalSales,
		Invoices:   in.Invoices,
		Modes:      modes,
		Settled:    settled,
		OnCredit:   onCredit,
		Collected:  collected,
		Refunds:    in.Refunds,
		Net:        collected.Sub(in.Refunds),
		Unsettled:  in.TotalSales.Sub(settled),
	}
}

// DateLayout is how a trading day is written, both on the wire and in the SQL
// this package's repository sends. Passing the day as text rather than as a
// timestamp is deliberate: a timestamp would be cast to a date using whatever
// time zone the database session happens to be in, so the day a report covers
// would depend on the connection rather than on the request.
const DateLayout = "2006-01-02"

// ParseDay reads a trading day written as 2006-01-02.
func ParseDay(s string) (time.Time, error) {
	day, err := time.Parse(DateLayout, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, invalid("a date must look like 2026-04-01, got %q", s)
	}
	return day, nil
}
