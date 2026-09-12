// Package credit is the service layer for customer credit: it owns
// transactions, enforces the rules that span more than one row, and is the
// only place that decides what a caller is allowed to do.
//
// The Repository interface is declared here, by the consumer, so that this
// package states what it needs rather than accepting whatever a database
// package happens to offer.
//
// # What changed relative to the handlers this replaces
//
//   - A payment no longer increases what the customer owes. See RecordPayment.
//   - Settlement uses exact decimal comparison, so a debt actually closes.
//   - Allocation reads its rows into memory before it writes any of them.
//   - The credit-limit check returns an error instead of failing open, and
//     runs inside the caller's transaction so it cannot be raced.
//   - Every customer write is scoped to a store the caller passes in, rather
//     than trusting a customer id from the request body.
package credit

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "gbos/internal/domain/credit"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when an operation that is inherently
// store-scoped is called without one. Credit is store money at risk, so there
// is no unscoped variant of any of these operations.
var ErrStoreRequired = errors.New("store is required")

// ErrCustomerRequired is returned when a customer id is missing.
var ErrCustomerRequired = errors.New("customer is required")

// Repository is the persistence this service needs.
//
// Most methods take a platform.Querier, so the service decides whether they
// run in a transaction. The two that take a platform.Tx take locks, which
// outside a transaction would be released immediately and mean nothing; the
// signature is what stops that being a runtime surprise.
type Repository interface {
	CreateCustomer(ctx context.Context, q platform.Querier, c domain.Customer) error
	UpdateCustomer(ctx context.Context, q platform.Querier, storeID uuid.UUID, c domain.Customer) (bool, error)
	GetCustomer(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.Customer, error)
	LockCustomer(ctx context.Context, tx platform.Tx, id, storeID uuid.UUID) (domain.Customer, error)
	GetAccount(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.Account, error)
	ListAccounts(ctx context.Context, q platform.Querier, f domain.CustomerFilter) ([]domain.Account, error)

	CreateEntry(ctx context.Context, q platform.Querier, e domain.Entry) (domain.Entry, error)
	GetEntry(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.EntryView, error)
	ListEntries(ctx context.Context, q platform.Querier, f domain.EntryFilter) ([]domain.EntryView, error)
	LockOpenIssues(ctx context.Context, tx platform.Tx, customerID uuid.UUID) ([]domain.Entry, error)
	ApplyAllocation(ctx context.Context, q platform.Querier, a domain.Allocation) (bool, error)

	Outstanding(ctx context.Context, q platform.Querier, customerID uuid.UUID) (money.Money, error)
	AdvanceBalance(ctx context.Context, q platform.Querier, customerID uuid.UUID) (money.Money, error)
	StoreOutstanding(ctx context.Context, q platform.Querier, storeID uuid.UUID) (money.Money, error)
	Overdue(ctx context.Context, q platform.Querier, storeID uuid.UUID, asOf time.Time) ([]domain.OverdueCustomer, error)
	VoidInvoiceEntries(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) (int64, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies customer credit rules.
type Service struct {
	db   DB
	repo Repository
	// now is the clock, injectable so that due dates and the overdue report
	// are testable without sleeping.
	now func() time.Time
}

// New returns a credit service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo, now: time.Now}
}

// WithClock returns a copy of the service that reads the time from now. It is
// for tests that need a fixed today.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

// CreateCustomer validates and stores a customer in storeID.
//
// The store is a parameter, not a field of the input. A request body naming
// its own store is a request body deciding its own authorisation.
func (s *Service) CreateCustomer(ctx context.Context, storeID uuid.UUID, in domain.NewCustomerInput) (domain.Customer, error) {
	if storeID == uuid.Nil {
		return domain.Customer{}, ErrStoreRequired
	}
	c, err := domain.NewCustomer(storeID, in)
	if err != nil {
		return domain.Customer{}, err
	}
	if err := s.repo.CreateCustomer(ctx, s.db, c); err != nil {
		return domain.Customer{}, fmt.Errorf("create customer: %w", err)
	}
	return c, nil
}

// UpdateCustomer applies a partial edit to a customer of storeID.
//
// Read and write happen in one transaction, and both are scoped to the store.
// The implementation this replaces checked the store with a separate query and
// then ran an unscoped `UPDATE customers ... WHERE id = ?`, so a cashier could
// set any customer's credit limit — including a customer of another store —
// to whatever they liked. Here a customer outside storeID is simply not found.
func (s *Service) UpdateCustomer(ctx context.Context, storeID, id uuid.UUID, u domain.CustomerUpdate) (domain.Customer, error) {
	if storeID == uuid.Nil {
		return domain.Customer{}, ErrStoreRequired
	}
	if id == uuid.Nil {
		return domain.Customer{}, ErrCustomerRequired
	}

	var updated domain.Customer
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		current, err := s.repo.LockCustomer(ctx, tx, id, storeID)
		if err != nil {
			return err
		}
		next, err := current.Apply(u)
		if err != nil {
			return err
		}
		found, err := s.repo.UpdateCustomer(ctx, tx, storeID, next)
		if err != nil {
			return err
		}
		if !found {
			return platform.ErrNotFound
		}
		updated = next
		return nil
	})
	if err != nil {
		return domain.Customer{}, err
	}
	return updated, nil
}

// GetAccount returns a customer of storeID with what they owe.
func (s *Service) GetAccount(ctx context.Context, storeID, id uuid.UUID) (domain.Account, error) {
	if storeID == uuid.Nil {
		return domain.Account{}, ErrStoreRequired
	}
	return s.repo.GetAccount(ctx, s.db, id, storeID)
}

// ListAccounts returns the customers of one store with their balances.
func (s *Service) ListAccounts(ctx context.Context, f domain.CustomerFilter) ([]domain.Account, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListAccounts(ctx, s.db, f)
}

// Outstanding returns what one customer of storeID still owes.
func (s *Service) Outstanding(ctx context.Context, storeID, customerID uuid.UUID) (money.Money, error) {
	account, err := s.GetAccount(ctx, storeID, customerID)
	if err != nil {
		return money.Zero(), err
	}
	return account.Outstanding, nil
}

// StoreOutstanding returns the total credit outstanding across a store.
func (s *Service) StoreOutstanding(ctx context.Context, storeID uuid.UUID) (money.Money, error) {
	if storeID == uuid.Nil {
		return money.Zero(), ErrStoreRequired
	}
	return s.repo.StoreOutstanding(ctx, s.db, storeID)
}

// ---------------------------------------------------------------------------
// Ledger reads
// ---------------------------------------------------------------------------

// ListEntries returns the credit ledger for one store.
func (s *Service) ListEntries(ctx context.Context, f domain.EntryFilter) ([]domain.EntryView, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListEntries(ctx, s.db, f)
}

// GetEntry returns one ledger row belonging to storeID.
func (s *Service) GetEntry(ctx context.Context, storeID, id uuid.UUID) (domain.EntryView, error) {
	if storeID == uuid.Nil {
		return domain.EntryView{}, ErrStoreRequired
	}
	return s.repo.GetEntry(ctx, s.db, id, storeID)
}

// Overdue returns the overdue report for a store as of asOf. A zero asOf means
// now.
func (s *Service) Overdue(ctx context.Context, storeID uuid.UUID, asOf time.Time) ([]domain.OverdueCustomer, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if asOf.IsZero() {
		asOf = s.now()
	}
	return s.repo.Overdue(ctx, s.db, storeID, asOf)
}

// ---------------------------------------------------------------------------
// Credit limit
// ---------------------------------------------------------------------------

// CheckCreditLimit reports whether customerID of storeID can take on
// `requested` more credit, and returns the figures behind the answer.
//
// It takes a platform.Tx rather than opening its own, because billing has to
// call it inside the transaction that creates the invoice. Checking the limit
// in a separate transaction is a race with a plain reading: two terminals can
// each check, each see room, and each commit a sale that on its own fits.
// LockCustomer takes the customer's row lock, so the second caller waits for
// the first to commit and then reads the balance the first sale produced.
//
// Every error is returned. The check this replaces was written
//
//	_ = tx.QueryRow(`SELECT COALESCE(SUM(...)) ...`).Scan(&outstanding)
//
// with the error discarded, so any failure — a typo in the SQL, a cancelled
// context, a dropped connection — left `outstanding` at its zero value and the
// limit check passed. A credit check that cannot read the balance must refuse
// the sale, not wave it through.
func (s *Service) CheckCreditLimit(ctx context.Context, tx platform.Tx, storeID, customerID uuid.UUID, requested money.Money) (domain.LimitStatus, error) {
	if storeID == uuid.Nil {
		return domain.LimitStatus{}, ErrStoreRequired
	}
	if customerID == uuid.Nil {
		return domain.LimitStatus{}, ErrCustomerRequired
	}

	customer, err := s.repo.LockCustomer(ctx, tx, customerID, storeID)
	if err != nil {
		return domain.LimitStatus{}, fmt.Errorf("load customer for credit check: %w", err)
	}
	outstanding, err := s.repo.Outstanding(ctx, tx, customerID)
	if err != nil {
		return domain.LimitStatus{}, fmt.Errorf("read outstanding for credit check: %w", err)
	}
	return domain.LimitStatus{
		CustomerID:  customerID,
		Limit:       customer.CreditLimit,
		Outstanding: outstanding,
		Requested:   requested,
	}, nil
}

// EnsureWithinCreditLimit is CheckCreditLimit reduced to a yes-or-error. It
// returns domain.ErrLimitExceeded when the sale would breach the limit, and
// the underlying error when the limit could not be established.
//
// This is the call billing should make: there is no return value it can
// accidentally ignore.
func (s *Service) EnsureWithinCreditLimit(ctx context.Context, tx platform.Tx, storeID, customerID uuid.UUID, requested money.Money) error {
	status, err := s.CheckCreditLimit(ctx, tx, storeID, customerID, requested)
	if err != nil {
		return err
	}
	return status.Err()
}

// ---------------------------------------------------------------------------
// Issuing credit
// ---------------------------------------------------------------------------

// IssueInput describes credit extended to a customer.
type IssueInput struct {
	StoreID    uuid.UUID
	CustomerID uuid.UUID
	// InvoiceID is the sale the credit was raised against. It is nil only for
	// an opening balance migrated from elsewhere.
	InvoiceID *uuid.UUID
	Amount    money.Money
	// DueDate is when payment falls due. Nil means the default term.
	DueDate *time.Time

	// AlreadyIncurred records a debt the shop has already extended, skipping the
	// limit check.
	//
	// It is for one case: a credit sale a till billed while it could not reach
	// the server. The goods have gone and the customer has left, so the debt
	// exists whatever the limit says. Refusing to record it would give the shop
	// a receivable its own books deny, which is worse than an over-limit
	// customer — the caller raises a reconciliation exception instead, and
	// somebody rings them.
	//
	// Never set it for a sale being made now.
	AlreadyIncurred bool
}

// RecordCreditSale checks the credit limit and writes the debt, both inside
// the caller's transaction.
//
// Billing calls this while holding the transaction that creates the invoice,
// so the limit that was checked is the limit that applied when the sale
// landed. The customer's row lock is taken by the limit check and held until
// the caller commits.
func (s *Service) RecordCreditSale(ctx context.Context, tx platform.Tx, in IssueInput) (domain.Entry, error) {
	if in.StoreID == uuid.Nil {
		return domain.Entry{}, ErrStoreRequired
	}
	if in.CustomerID == uuid.Nil {
		return domain.Entry{}, ErrCustomerRequired
	}
	if !in.Amount.IsPositive() {
		return domain.Entry{}, fmt.Errorf("%w: credit amount must be greater than zero, got %s",
			domain.ErrValidation, in.Amount.StringFixed())
	}

	if !in.AlreadyIncurred {
		if err := s.EnsureWithinCreditLimit(ctx, tx, in.StoreID, in.CustomerID, in.Amount); err != nil {
			return domain.Entry{}, err
		}
	}

	dueDate := in.DueDate
	if dueDate == nil {
		due := domain.DefaultDueDate(s.now())
		dueDate = &due
	}

	entry, err := domain.NewIssue(in.CustomerID, in.InvoiceID, in.Amount, dueDate)
	if err != nil {
		return domain.Entry{}, err
	}
	stored, err := s.repo.CreateEntry(ctx, tx, entry)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("record credit sale: %w", err)
	}
	return stored, nil
}

// IssueCredit is RecordCreditSale in a transaction of its own, for callers
// that are not already in one.
func (s *Service) IssueCredit(ctx context.Context, in IssueInput) (domain.Entry, error) {
	var entry domain.Entry
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		entry, err = s.RecordCreditSale(ctx, tx, in)
		return err
	})
	if err != nil {
		return domain.Entry{}, err
	}
	return entry, nil
}

// VoidInvoiceCredit marks the credit raised against an invoice as voided, in
// the caller's transaction, and returns how many ledger rows changed.
//
// Billing calls this while voiding the invoice. A voided row stops counting as
// debt everywhere, because everywhere agrees on
// domain.Status.CountsAsDebt — which the old `status != 'paid'` readers did
// not, so a voided invoice went on consuming the customer's credit limit on
// one screen while showing as cancelled on another.
func (s *Service) VoidInvoiceCredit(ctx context.Context, tx platform.Tx, invoiceID uuid.UUID) (int64, error) {
	if invoiceID == uuid.Nil {
		return 0, fmt.Errorf("%w: invoice is required", domain.ErrValidation)
	}
	n, err := s.repo.VoidInvoiceEntries(ctx, tx, invoiceID)
	if err != nil {
		return 0, fmt.Errorf("void invoice credit: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

// PaymentInput describes money received from a customer.
//
// There is deliberately no payment method or note here. credit_entries has no
// column for either; the tender used belongs to invoice_payments and the note
// belongs to the audit trail. Accepting a field the ledger then silently drops
// would be worse than not accepting it.
type PaymentInput struct {
	StoreID    uuid.UUID
	CustomerID uuid.UUID
	Amount     money.Money
}

// PaymentResult is what a recorded payment did.
type PaymentResult struct {
	// Payment is the ledger row for the money received.
	Payment domain.Entry `json:"payment"`
	// Allocations are the debts this payment moved, oldest first.
	Allocations []domain.Allocation `json:"allocations"`
	// Applied is how much of the payment landed on a debt.
	Applied money.Money `json:"applied"`
	// Advance is the overpayment held back, when the customer paid more than
	// they owed. Nil otherwise.
	Advance *domain.Entry `json:"advance,omitempty"`
	// Outstanding is what the customer owes after the payment.
	Outstanding money.Money `json:"outstanding"`
}

// RecordPayment records money received from a customer and settles their
// oldest debts with it.
//
// This is the fix for the worst defect in the system. The handler this
// replaces inserted the payment as a new ledger row with amount = the payment
// and paid_amount = 0, and every outstanding query is
// SUM(amount - paid_amount) — so recording a Rs.5,000 payment raised the
// customer's balance by Rs.5,000, and a customer who paid in full ended up
// owing twice the original bill.
//
// The model here is both halves of what a payment is:
//
//   - The money received is written as one self-settled ledger row
//     (paid_amount = amount, status paid). It contributes exactly zero to the
//     balance, and it is the record of what was received and when.
//   - The balance falls because the payment is allocated against the open
//     issue rows, oldest first, raising their paid_amount.
//
// Recording only the row would leave the debt untouched; allocating only would
// leave no evidence the money arrived. Signed entries were not an option and
// would not have been better: the schema constrains amount >= 0 and
// paid_amount <= amount precisely so that a ledger row cannot express a
// negative debt by accident.
//
// The sequence is: lock the customer, read every open debt into memory, decide
// the whole allocation, then write. Reading before writing is not a style
// preference — the old loop issued its UPDATE while its SELECT cursor was
// still open, which pgx rejects outright with "conn busy".
func (s *Service) RecordPayment(ctx context.Context, in PaymentInput) (PaymentResult, error) {
	if in.StoreID == uuid.Nil {
		return PaymentResult{}, ErrStoreRequired
	}
	if in.CustomerID == uuid.Nil {
		return PaymentResult{}, ErrCustomerRequired
	}
	if !in.Amount.IsPositive() {
		return PaymentResult{}, fmt.Errorf("%w: payment amount must be greater than zero, got %s",
			domain.ErrValidation, in.Amount.StringFixed())
	}

	var result PaymentResult
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		// Scoping: a customer of another store is not found, so a cashier
		// cannot post a payment against a ledger they cannot see. The lock is
		// taken first and always first, so this path and the credit-limit
		// check acquire in the same order and cannot deadlock.
		customer, err := s.repo.LockCustomer(ctx, tx, in.CustomerID, in.StoreID)
		if err != nil {
			return err
		}

		// Drain every open debt into memory before writing anything.
		open, err := s.repo.LockOpenIssues(ctx, tx, customer.ID)
		if err != nil {
			return fmt.Errorf("load open credit entries: %w", err)
		}

		// Decide the whole allocation up front, in exact decimal arithmetic.
		allocations, remainder := domain.AllocatePayment(open, in.Amount)

		payment, err := domain.NewPayment(customer.ID, in.Amount)
		if err != nil {
			return err
		}
		storedPayment, err := s.repo.CreateEntry(ctx, tx, payment)
		if err != nil {
			return fmt.Errorf("record payment: %w", err)
		}

		for _, a := range allocations {
			moved, err := s.repo.ApplyAllocation(ctx, tx, a)
			if err != nil {
				return fmt.Errorf("settle credit entry %s: %w", a.EntryID, err)
			}
			if !moved {
				// Unreachable while the FOR UPDATE lock is held, which is why
				// it is an error rather than a skip: if it ever fires, the
				// allocation was computed against a row that has since moved
				// and the whole payment must roll back rather than land
				// partly.
				return fmt.Errorf("settle credit entry %s: %w", a.EntryID, platform.ErrConflict)
			}
		}

		// Overpayment is held as an advance, as it always has been. The guard
		// is exact: the old code used `remaining > 0.01`, which both created
		// spurious advances out of float residue and silently swallowed
		// genuine remainders below a paisa.
		var advance *domain.Entry
		if remainder.IsPositive() {
			entry, err := domain.NewAdvance(customer.ID, remainder)
			if err != nil {
				return err
			}
			stored, err := s.repo.CreateEntry(ctx, tx, entry)
			if err != nil {
				return fmt.Errorf("record advance: %w", err)
			}
			advance = &stored
		}

		outstanding, err := s.repo.Outstanding(ctx, tx, customer.ID)
		if err != nil {
			return fmt.Errorf("read outstanding after payment: %w", err)
		}

		result = PaymentResult{
			Payment:     storedPayment,
			Allocations: allocations,
			Applied:     in.Amount.Sub(remainder),
			Advance:     advance,
			Outstanding: outstanding,
		}
		return nil
	})
	if err != nil {
		return PaymentResult{}, err
	}
	return result, nil
}

// AdvanceBalance returns the total of overpayments held for a customer of
// storeID.
func (s *Service) AdvanceBalance(ctx context.Context, storeID, customerID uuid.UUID) (money.Money, error) {
	account, err := s.GetAccount(ctx, storeID, customerID)
	if err != nil {
		return money.Zero(), err
	}
	return account.Advance, nil
}
