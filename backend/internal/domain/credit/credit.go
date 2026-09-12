// Package credit holds the customer credit aggregate: the customers a shop
// extends credit to, their limits, and the ledger of credit issued against
// invoices and payments received back.
//
// Nothing here talks to a database or to HTTP. In particular the payment
// allocation — the arithmetic that got the headline defect wrong — is a pure
// function over a slice of entries, so it can be reasoned about and tested
// without a database, and so the caller is forced to read its rows before it
// writes any (see AllocatePayment).
//
// # The ledger model
//
// A credit_entries row is one of three things, and the distinction is what the
// old code lost:
//
//	issue    the shop gave the customer goods on credit. This is a debt.
//	         amount is what was owed; paid_amount is how much of it has been
//	         settled so far.
//	payment  the customer handed over money. It is NOT a debt. It is written
//	         self-settled (paid_amount == amount, status paid) so that it
//	         contributes exactly zero to SUM(amount - paid_amount), and it
//	         exists so the ledger still shows what happened and when.
//	advance  the unallocated remainder of an overpayment. Also self-settled,
//	         also contributes zero to the debt.
//
// The defect this replaces inserted a payment as amount = X, paid_amount = 0,
// status = 'completed'. Every outstanding query is SUM(amount - paid_amount),
// so a customer paying Rs.5,000 watched their outstanding balance go *up* by
// Rs.5,000. Recording the payment self-settled, and separately allocating it
// against the open issue rows, is what makes a payment reduce the balance.
//
// # The debt predicate
//
// Exactly one rule decides whether a row is still owed:
//
//	Status.CountsAsDebt() — pending or partial, nothing else.
//
// The schema's enum is pending | partial | paid | voided; 'completed' does not
// exist. Readers that tested status != 'paid' counted voided rows and the old
// bogus 'completed' payment rows as debt, which is the second half of the same
// bug. Every read in this aggregate goes through the one predicate.
package credit

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

// ErrLimitExceeded means a credit sale would take the customer past their
// credit limit. It is a distinct sentinel because the caller — billing —
// must reject the sale rather than treat it as an internal failure.
var ErrLimitExceeded = errors.New("credit limit exceeded")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// ---------------------------------------------------------------------------
// Vocabulary
// ---------------------------------------------------------------------------

// Type is what a ledger row records. It mirrors the credit_entry_type enum.
type Type string

const (
	// TypeIssue is credit extended to the customer: a debt.
	TypeIssue Type = "issue"
	// TypePayment is money received from the customer.
	TypePayment Type = "payment"
	// TypeAdvance is the unallocated remainder of an overpayment.
	TypeAdvance Type = "advance"
)

// Valid reports whether t is one of the three enum members.
func (t Type) Valid() bool {
	switch t {
	case TypeIssue, TypePayment, TypeAdvance:
		return true
	}
	return false
}

// Status is how far a ledger row has been settled. It mirrors the
// credit_entry_status enum, which is deliberately pending | partial | paid |
// voided — there is no 'completed'.
type Status string

const (
	// StatusPending is a debt against which nothing has been paid.
	StatusPending Status = "pending"
	// StatusPartial is a debt that has been partly settled.
	StatusPartial Status = "partial"
	// StatusPaid is fully settled. Payment and advance rows are born paid.
	StatusPaid Status = "paid"
	// StatusVoided is cancelled, because the invoice behind it was voided.
	// A voided row is not a debt and is not a payment; it never happened.
	StatusVoided Status = "voided"
)

// Valid reports whether s is one of the four enum members. In particular
// "completed" is not valid: the old code wrote it and the schema has no such
// value.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPartial, StatusPaid, StatusVoided:
		return true
	}
	return false
}

// CountsAsDebt is the single definition of "this row is still owed". Every
// outstanding-balance, credit-limit and overdue read in this aggregate agrees
// with it, and the SQL that implements it lives in exactly one string
// (creditOpenStatuses in the repository).
//
// Note what it excludes and why:
//
//	paid    settled, and — because payment and advance rows are written
//	        self-settled — this is also every payment ever received. A reader
//	        testing status != 'paid' counts those payments as debt.
//	voided  the invoice was cancelled. status != 'paid' counts it as debt too.
func (s Status) CountsAsDebt() bool {
	return s == StatusPending || s == StatusPartial
}

// StatusFor derives the settlement status of a debt from exact amounts.
//
// This is the fix for the defect that never let a debt close. The old code
// used `newPaid >= amount` on float64: paying Rs.1.30 off in ten instalments
// of Rs.0.13 leaves paid at 1.2999999999999998 against an amount of 1.30, so
// the entry stayed 'partial' forever, kept consuming the customer's credit
// limit and never left the overdue report. money.Money compares exactly, with
// no epsilon anywhere, so 0.13 x 10 is 1.30 and the entry closes.
func StatusFor(amount, paid money.Money) Status {
	switch {
	case paid.GreaterThanOrEqual(amount):
		return StatusPaid
	case paid.IsPositive():
		return StatusPartial
	default:
		return StatusPending
	}
}

// DefaultCreditDays is the payment term a credit sale gets when the caller
// does not name a due date.
const DefaultCreditDays = 30

// DefaultDueDate returns the due date for a credit sale issued at from.
func DefaultDueDate(from time.Time) time.Time {
	return from.AddDate(0, 0, DefaultCreditDays)
}

// ---------------------------------------------------------------------------
// Customer
// ---------------------------------------------------------------------------

// Customer is someone the shop bills, and possibly extends credit to. A
// customer belongs to exactly one store: the credit limit is that store's
// money at risk, so it is not a group-wide figure.
type Customer struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	NameHi      string      `json:"name_hi"`
	Phone       string      `json:"phone"`
	Address     string      `json:"address"`
	GSTIN       string      `json:"gstin"`
	CreditLimit money.Money `json:"credit_limit"`
	IsWholesale bool        `json:"is_wholesale"`
	StoreID     uuid.UUID   `json:"store_id"`
	IsActive    bool        `json:"is_active"`
}

// NewCustomerInput is the caller-supplied shape for creating a customer.
// StoreID is deliberately not part of it: the service takes the store as an
// explicit parameter from the authenticated session, so a request body cannot
// name a store it is not entitled to.
type NewCustomerInput struct {
	Name        string
	NameHi      string
	Phone       string
	Address     string
	GSTIN       string
	CreditLimit money.Money
	IsWholesale bool
}

// NewCustomer validates and returns a customer ready to persist in storeID.
func NewCustomer(storeID uuid.UUID, in NewCustomerInput) (Customer, error) {
	if storeID == uuid.Nil {
		return Customer{}, invalid("store is required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Customer{}, invalid("customer name is required")
	}
	if len(name) > 200 {
		return Customer{}, invalid("customer name is longer than 200 characters")
	}
	gstin := strings.ToUpper(strings.TrimSpace(in.GSTIN))
	if err := ValidateGSTIN(gstin); err != nil {
		return Customer{}, err
	}
	if err := ValidateCreditLimit(in.CreditLimit); err != nil {
		return Customer{}, err
	}
	return Customer{
		ID:          uuid.New(),
		Name:        name,
		NameHi:      strings.TrimSpace(in.NameHi),
		Phone:       strings.TrimSpace(in.Phone),
		Address:     strings.TrimSpace(in.Address),
		GSTIN:       gstin,
		CreditLimit: in.CreditLimit,
		IsWholesale: in.IsWholesale,
		StoreID:     storeID,
		IsActive:    true,
	}, nil
}

// CustomerUpdate carries a partial edit. A nil field is left alone, which is
// what lets a screen that only edits the phone number not silently reset the
// credit limit to zero.
type CustomerUpdate struct {
	Name        *string
	NameHi      *string
	Phone       *string
	Address     *string
	GSTIN       *string
	CreditLimit *money.Money
	IsWholesale *bool
	IsActive    *bool
}

// Apply merges the update into c and validates the result. It returns the
// updated customer rather than mutating in place, so a rejected update cannot
// leave a half-modified value behind.
func (c Customer) Apply(u CustomerUpdate) (Customer, error) {
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return Customer{}, invalid("customer name is required")
		}
		if len(name) > 200 {
			return Customer{}, invalid("customer name is longer than 200 characters")
		}
		c.Name = name
	}
	if u.NameHi != nil {
		c.NameHi = strings.TrimSpace(*u.NameHi)
	}
	if u.Phone != nil {
		c.Phone = strings.TrimSpace(*u.Phone)
	}
	if u.Address != nil {
		c.Address = strings.TrimSpace(*u.Address)
	}
	if u.GSTIN != nil {
		gstin := strings.ToUpper(strings.TrimSpace(*u.GSTIN))
		if err := ValidateGSTIN(gstin); err != nil {
			return Customer{}, err
		}
		c.GSTIN = gstin
	}
	if u.CreditLimit != nil {
		if err := ValidateCreditLimit(*u.CreditLimit); err != nil {
			return Customer{}, err
		}
		c.CreditLimit = *u.CreditLimit
	}
	if u.IsWholesale != nil {
		c.IsWholesale = *u.IsWholesale
	}
	if u.IsActive != nil {
		c.IsActive = *u.IsActive
	}
	return c, nil
}

// ValidateCreditLimit rejects a negative limit. The database carries the same
// CHECK; this exists to produce a usable message rather than a constraint
// violation.
func ValidateCreditLimit(limit money.Money) error {
	if limit.IsNegative() {
		return invalid("credit limit cannot be negative, got %s", limit.StringFixed())
	}
	return nil
}

// ValidateGSTIN checks the length of a supplied GSTIN. A GSTIN is statutorily
// 15 characters; an empty one means "unregistered customer", which is the
// normal case over a retail counter and is not an error.
func ValidateGSTIN(gstin string) error {
	if gstin == "" {
		return nil
	}
	if len(gstin) != 15 {
		return invalid("GSTIN must be 15 characters, got %d", len(gstin))
	}
	return nil
}

// CustomerFilter narrows a customer listing. StoreID is mandatory at the
// service boundary: an unscoped customer list leaks one store's ledger to
// another.
type CustomerFilter struct {
	StoreID uuid.UUID
	Search  string
	Limit   int
	// IncludeInactive lists deactivated customers too.
	IncludeInactive bool
}

// Account is the read model a credit screen wants: the customer together with
// what they owe.
type Account struct {
	Customer
	// Outstanding is the sum of the still-owed part of every issue row that
	// counts as debt. It is never negative.
	Outstanding money.Money `json:"outstanding"`
	// Advance is the total of overpayments held for this customer. It is
	// reported separately and is deliberately NOT netted off Outstanding:
	// an advance is not automatically applied to a later sale, which is the
	// behaviour the system has always had. Netting it silently would change
	// what a cashier is told to collect.
	Advance money.Money `json:"advance"`
}

// Available is the credit headroom left: limit minus what is already owed. It
// can be negative if the limit was lowered below an existing balance.
func (a Account) Available() money.Money {
	return a.CreditLimit.Sub(a.Outstanding)
}

// ---------------------------------------------------------------------------
// Ledger entries
// ---------------------------------------------------------------------------

// Entry is one row of the credit ledger. See the package documentation for
// what each Type means.
type Entry struct {
	ID         uuid.UUID   `json:"id"`
	CustomerID uuid.UUID   `json:"customer_id"`
	InvoiceID  *uuid.UUID  `json:"invoice_id"`
	Amount     money.Money `json:"amount"`
	PaidAmount money.Money `json:"paid_amount"`
	Type       Type        `json:"type"`
	DueDate    *time.Time  `json:"due_date"`
	Status     Status      `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
}

// Due is the unsettled part of the row, regardless of status.
func (e Entry) Due() money.Money { return e.Amount.Sub(e.PaidAmount) }

// Outstanding is what this row contributes to the customer's balance: its
// unsettled part if it still counts as debt, and exactly zero otherwise.
//
// This is the Go statement of the SQL the repository runs, and the two are
// pinned together by TestOutstandingMatchesTheDomainRule.
func (e Entry) Outstanding() money.Money {
	if !e.Status.CountsAsDebt() {
		return money.Zero()
	}
	return e.Due()
}

// IsOverdue reports whether the row is still owed and its due date has passed.
func (e Entry) IsOverdue(asOf time.Time) bool {
	if !e.Status.CountsAsDebt() || e.DueDate == nil {
		return false
	}
	return e.DueDate.Before(asOf)
}

// EntryView is an entry with the customer's name, for the ledger listing.
type EntryView struct {
	Entry
	CustomerName string `json:"customer_name"`
}

// EntryFilter narrows a ledger listing.
type EntryFilter struct {
	StoreID    uuid.UUID
	CustomerID *uuid.UUID
	// Type, when set, restricts to one kind of row.
	Type *Type
	// OpenOnly restricts to rows that still count as debt.
	OpenOnly bool
	Limit    int
}

// NewIssue returns a credit-sale ledger row: a debt of amount, due on dueDate.
func NewIssue(customerID uuid.UUID, invoiceID *uuid.UUID, amount money.Money, dueDate *time.Time) (Entry, error) {
	if customerID == uuid.Nil {
		return Entry{}, invalid("customer is required")
	}
	if amount.IsNegative() {
		return Entry{}, invalid("credit amount cannot be negative, got %s", amount.StringFixed())
	}
	return Entry{
		ID:         uuid.New(),
		CustomerID: customerID,
		InvoiceID:  invoiceID,
		Amount:     amount,
		PaidAmount: money.Zero(),
		Type:       TypeIssue,
		DueDate:    dueDate,
		Status:     StatusPending,
	}, nil
}

// NewPayment returns the ledger row for money received from a customer.
//
// It is written self-settled — paid_amount equals amount, status paid — so it
// contributes exactly zero to SUM(amount - paid_amount). The row exists for
// the audit trail: what was received, and when. The reduction in the balance
// comes from allocating it against the open issue rows, not from this row.
func NewPayment(customerID uuid.UUID, amount money.Money) (Entry, error) {
	if customerID == uuid.Nil {
		return Entry{}, invalid("customer is required")
	}
	if !amount.IsPositive() {
		return Entry{}, invalid("payment amount must be greater than zero, got %s", amount.StringFixed())
	}
	return Entry{
		ID:         uuid.New(),
		CustomerID: customerID,
		Amount:     amount,
		PaidAmount: amount,
		Type:       TypePayment,
		Status:     StatusPaid,
	}, nil
}

// NewAdvance returns the ledger row for the unallocated remainder of an
// overpayment. Like a payment it is self-settled and contributes nothing to
// the debt.
func NewAdvance(customerID uuid.UUID, amount money.Money) (Entry, error) {
	if customerID == uuid.Nil {
		return Entry{}, invalid("customer is required")
	}
	if !amount.IsPositive() {
		return Entry{}, invalid("advance amount must be greater than zero, got %s", amount.StringFixed())
	}
	return Entry{
		ID:         uuid.New(),
		CustomerID: customerID,
		Amount:     amount,
		PaidAmount: amount,
		Type:       TypeAdvance,
		Status:     StatusPaid,
	}, nil
}

// ---------------------------------------------------------------------------
// Payment allocation
// ---------------------------------------------------------------------------

// Allocation is one write the caller must make to apply a payment: set this
// entry's paid_amount and status.
type Allocation struct {
	EntryID uuid.UUID   `json:"entry_id"`
	Applied money.Money `json:"applied"`
	// PaidAmount is the entry's new total paid, not the increment.
	PaidAmount money.Money `json:"paid_amount"`
	Status     Status      `json:"status"`
}

// AllocatePayment spreads amount across open entries oldest-first and reports
// what to write, plus whatever could not be allocated.
//
// It takes a slice, not a cursor, and that is load-bearing rather than
// stylistic. The code this replaces held a database cursor open over the
// entries and issued an UPDATE inside `for rows.Next()`. On SQLite that
// happened to work; pgx refuses it outright with "conn busy", because a
// single connection cannot run a statement while a result set is streaming on
// it. Computing every write first, from rows already drained into memory, is
// what makes the allocation correct on Postgres and testable without one.
//
// The arithmetic is exact throughout. Each entry receives min(remaining, due),
// so a new paid_amount can never exceed the entry's amount — the schema
// carries the same CHECK — and an entry that receives its full due lands on
// exactly amount, which is what closes it.
//
// Entries must be supplied in the order they should be settled: oldest first.
// Rows that are not debts, or that have nothing outstanding, are skipped.
func AllocatePayment(open []Entry, amount money.Money) (allocations []Allocation, remainder money.Money) {
	remainder = amount
	allocations = make([]Allocation, 0, len(open))

	for _, e := range open {
		if !remainder.IsPositive() {
			break
		}
		if !e.Status.CountsAsDebt() {
			continue
		}
		due := e.Due()
		if !due.IsPositive() {
			continue
		}

		applied := due
		if remainder.LessThan(due) {
			applied = remainder
		}
		newPaid := e.PaidAmount.Add(applied)

		allocations = append(allocations, Allocation{
			EntryID:    e.ID,
			Applied:    applied,
			PaidAmount: newPaid,
			Status:     StatusFor(e.Amount, newPaid),
		})
		remainder = remainder.Sub(applied)
	}
	return allocations, remainder
}

// ---------------------------------------------------------------------------
// Credit limit
// ---------------------------------------------------------------------------

// LimitStatus is the answer to "may this customer take on this much more
// credit?", with the figures behind the answer so a caller can explain the
// refusal instead of just reporting one.
type LimitStatus struct {
	CustomerID  uuid.UUID   `json:"customer_id"`
	Limit       money.Money `json:"credit_limit"`
	Outstanding money.Money `json:"outstanding"`
	Requested   money.Money `json:"requested"`
}

// Projected is what the customer would owe if the sale went through.
func (l LimitStatus) Projected() money.Money { return l.Outstanding.Add(l.Requested) }

// Available is the headroom before the sale.
func (l LimitStatus) Available() money.Money { return l.Limit.Sub(l.Outstanding) }

// Allowed reports whether the sale is within the limit. A sale that lands
// exactly on the limit is allowed; a limit is a ceiling, not a barrier one
// paisa below itself.
func (l LimitStatus) Allowed() bool {
	return l.Projected().LessThanOrEqual(l.Limit)
}

// Err returns ErrLimitExceeded with the figures, or nil when the sale is
// within the limit.
func (l LimitStatus) Err() error {
	if l.Allowed() {
		return nil
	}
	return fmt.Errorf("%w: %s outstanding plus %s requested is over the %s limit",
		ErrLimitExceeded, l.Outstanding.Format(), l.Requested.Format(), l.Limit.Format())
}

// OverdueCustomer is one line of the overdue report.
type OverdueCustomer struct {
	CustomerID  uuid.UUID   `json:"customer_id"`
	Name        string      `json:"name"`
	Phone       string      `json:"phone"`
	CreditLimit money.Money `json:"credit_limit"`
	Outstanding money.Money `json:"outstanding"`
	DueCount    int         `json:"due_count"`
	EarliestDue *time.Time  `json:"earliest_due"`
}
