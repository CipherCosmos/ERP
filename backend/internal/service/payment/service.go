// Package payment is the service layer for settling sales: recording how an
// invoice was paid, reading that back, and reporting a day's takings.
//
// # What changed relative to the handler this replaces
//
//   - The transaction is committed, and the commit error is returned. The
//     handler called tx.Commit() and discarded its result, then answered 201
//     with a total_paid figure for payments that may never have landed.
//   - The at-most-once guard fails closed. It was a `SELECT COUNT(*)` whose
//     scan error was discarded, so a failed count read as zero and a second
//     settlement went through; it also raced, because two requests under READ
//     COMMITTED both saw zero. The invoice row is now locked FOR UPDATE first.
//   - A split must sum to the invoice total exactly. The five-paise tolerance
//     is gone; see the domain package for why it was never a rule.
//   - Every read and write is scoped to a store passed in by the caller.
//   - The settlement report is one fold over two queries instead of eleven
//     accumulators over two, and it has no SQLite date() calls left in it.
package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	domain "gbos/internal/domain/payment"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when an operation is called without a store.
// Payments belong to an invoice that belongs to a store, and there is no
// unscoped variant of any of these operations: an invoice id on its own is a
// bearer token for another shop's money.
var ErrStoreRequired = errors.New("store is required")

// ErrInvoiceRequired is returned when an invoice id is missing.
var ErrInvoiceRequired = errors.New("invoice is required")

// Repository is the persistence this service needs.
//
// LockInvoiceToSettle takes a platform.Tx rather than a Querier because it
// takes a row lock, and a lock taken outside a transaction is released
// immediately and means nothing. The signature is what stops that being a
// runtime surprise.
type Repository interface {
	GetInvoiceToSettle(ctx context.Context, q platform.Querier, storeID, invoiceID uuid.UUID) (repo.SettleableInvoice, error)
	LockInvoiceToSettle(ctx context.Context, tx platform.Tx, storeID, invoiceID uuid.UUID) (repo.SettleableInvoice, error)
	HasPayments(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) (bool, error)
	InsertPayments(ctx context.Context, q platform.Querier, payments []domain.Payment) error
	PaymentsForInvoice(ctx context.Context, q platform.Querier, storeID, invoiceID uuid.UUID) ([]domain.Payment, error)

	SalesForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) (repo.DaySales, error)
	TendersForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) ([]domain.TenderGroup, error)

	FindProcessedPayment(ctx context.Context, q platform.Querier, key string) ([]byte, bool, error)
	RecordProcessedPayment(ctx context.Context, q platform.Querier, key, path string, status int, response any) error
}

// RefundReader is the part of the returns aggregate the settlement report
// needs: what a shop handed back over the counter on one day.
//
// It is a port rather than a direct call because a refund is not this
// aggregate's to own. The day is text in domain.DateLayout, for the reason
// given on that constant.
type RefundReader interface {
	RefundsForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) (money.Money, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
	InTxOpts(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx platform.Tx) error) error
}

// reportTx is the isolation a settlement report is read at.
//
// REPEATABLE READ, not the default: the report is three queries whose figures
// are compared against one another, and at READ COMMITTED each takes its own
// snapshot. A sale committing between the first and the second would leave the
// report claiming sales that no tender accounts for — an "unsettled" figure
// invented by the reporting itself. Read-only lets Postgres skip the
// bookkeeping for writes that cannot happen.
var reportTx = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}

// Service records and reports how sales were settled.
type Service struct {
	db      DB
	repo    Repository
	refunds RefundReader
	now     func() time.Time
}

// New returns a payment service.
func New(db DB, r Repository, refunds RefundReader) *Service {
	return &Service{db: db, repo: r, refunds: refunds, now: time.Now}
}

// WithClock replaces the clock, for tests that need a fixed trading day.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// SettleInput describes how an invoice was paid.
type SettleInput struct {
	StoreID   uuid.UUID
	InvoiceID uuid.UUID

	// Tenders are the parts of the settlement. One entry is an ordinary sale;
	// several is a split. They must sum to the invoice total exactly.
	Tenders []domain.TenderInput

	// IdempotencyKey makes a retried request safe. It is optional, and it is
	// not what enforces at-most-once settlement — the invoice row lock is. See
	// Settle for the difference and why both exist.
	IdempotencyKey string
}

// Result is a settled invoice.
type Result struct {
	InvoiceID uuid.UUID        `json:"invoice_id"`
	Total     money.Money      `json:"total_paid"`
	Payments  []domain.Payment `json:"payments"`
	Replayed  bool             `json:"replayed"`
}

// Settle records how an invoice was paid.
//
// # The two guards, and why they are different things
//
// An invoice is settled at most once. That is a fact about the world — the
// money crossed the counter once — and it must hold for as long as the invoice
// exists. It is enforced by locking the invoice row inside this transaction
// and then asking, with the error checked, whether payments already exist. The
// lock serialises concurrent attempts; the checked read fails closed.
//
// IdempotencyKey is a separate, weaker thing: it makes a retried HTTP request
// answer with the original result instead of a conflict, which is what a till
// on a flaky connection needs. It uses processed_requests, transactionally
// with the write, exactly as billing does.
//
// A derived key — processed_requests keyed on the invoice id — was considered
// as the invariant itself, since a primary key is a stronger guard than a
// lock plus a read. It was rejected: that table is documented as prunable
// ("these rows are only useful for as long as a client might retry"), so the
// invariant would quietly acquire an expiry date and an old invoice would
// become settleable again. An invariant that a retention job can delete is not
// an invariant.
func (s *Service) Settle(ctx context.Context, in SettleInput) (Result, error) {
	if in.StoreID == uuid.Nil {
		return Result{}, ErrStoreRequired
	}
	if in.InvoiceID == uuid.Nil {
		return Result{}, ErrInvoiceRequired
	}
	if len(in.Tenders) == 0 {
		return Result{}, fmt.Errorf("%w: a settlement needs at least one payment", domain.ErrValidation)
	}

	if in.IdempotencyKey != "" {
		if replayed, found, err := s.replay(ctx, in.InvoiceID, in.IdempotencyKey); err != nil {
			return Result{}, err
		} else if found {
			return replayed, nil
		}
	}

	var out Result
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		result, err := s.settle(ctx, tx, in)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return out, nil
}

func (s *Service) settle(ctx context.Context, tx platform.Tx, in SettleInput) (Result, error) {
	inv, err := s.repo.LockInvoiceToSettle(ctx, tx, in.StoreID, in.InvoiceID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			// Deliberately the same answer for "no such invoice" and "that
			// invoice belongs to another store". Distinguishing them would
			// confirm the existence of another shop's sale to anyone holding
			// a guessed id.
			return Result{}, fmt.Errorf("%w: no such invoice in this store", platform.ErrNotFound)
		}
		return Result{}, fmt.Errorf("load invoice: %w", err)
	}
	if inv.Status == billingdomain.StatusVoided {
		return Result{}, domain.ErrNotSettleable
	}

	// The key is looked up again here, inside the transaction and behind the
	// invoice's row lock, because the lookup before the transaction cannot see
	// a request that is still in flight.
	//
	// A till whose connection drops sends the tender twice within milliseconds.
	// Neither attempt finds the other's key, both do the work, and the lock
	// decides which one lands — after which the loser reads the winner's key
	// here and answers with the settlement that happened. Without this it would
	// fall through to ErrAlreadySettled, which is the right answer to a second
	// attempt at the same bill and the wrong one to the same attempt: a till
	// told its settlement failed either asks the customer to pay again or
	// leaves the sale looking unpaid.
	//
	// The lock is what makes this correct rather than merely likely. It is held
	// from LockInvoiceToSettle above until commit, so the winner's
	// processed_requests row is committed and visible by the time the loser
	// reaches this line.
	if in.IdempotencyKey != "" {
		replayed, found, err := s.replayIn(ctx, tx, in.InvoiceID, in.IdempotencyKey)
		if err != nil {
			return Result{}, err
		}
		if found {
			return replayed, nil
		}
	}

	settled, err := s.repo.HasPayments(ctx, tx, inv.ID)
	if err != nil {
		return Result{}, fmt.Errorf("check for existing payments: %w", err)
	}
	if settled {
		return Result{}, domain.ErrAlreadySettled
	}

	payments, err := domain.Settle(inv.ID, inv.Total, in.Tenders, s.now())
	if err != nil {
		return Result{}, err
	}
	if err := s.repo.InsertPayments(ctx, tx, payments); err != nil {
		return Result{}, fmt.Errorf("write payments: %w", err)
	}

	out := Result{InvoiceID: inv.ID, Total: inv.Total, Payments: payments}
	if in.IdempotencyKey != "" {
		if err := s.repo.RecordProcessedPayment(ctx, tx, in.IdempotencyKey, "/api/v1/payments/split", 201, out); err != nil {
			if errors.Is(err, platform.ErrConflict) {
				// Another request with this key committed first. Its
				// settlement stands and this one must not.
				return Result{}, fmt.Errorf("%w: this request has already been processed", platform.ErrConflict)
			}
			return Result{}, fmt.Errorf("record idempotency key: %w", err)
		}
	}
	return out, nil
}

// replay looks a key up through the pool, before any transaction is open. It
// answers the retry that arrives after the original committed.
func (s *Service) replay(ctx context.Context, invoiceID uuid.UUID, key string) (Result, bool, error) {
	return s.replayIn(ctx, s.db, invoiceID, key)
}

// replayIn is replay against a caller-chosen querier, so the same lookup can be
// made inside the settling transaction where the invoice lock has already
// serialised the competing attempt. See settle for why that second lookup
// exists.
func (s *Service) replayIn(ctx context.Context, q platform.Querier, invoiceID uuid.UUID, key string) (Result, bool, error) {
	body, found, err := s.repo.FindProcessedPayment(ctx, q, key)
	if err != nil {
		return Result{}, false, fmt.Errorf("check idempotency key: %w", err)
	}
	if !found {
		return Result{}, false, nil
	}
	var out Result
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, false, fmt.Errorf("decode replayed response: %w", err)
	}
	if out.InvoiceID != invoiceID {
		// processed_requests is keyed on the client's string alone, so a till
		// that reuses a key — or two shops whose keys are not globally unique —
		// lands on a row belonging to a different bill. Answering with it would
		// report somebody else's settlement as this one's and silently leave
		// this invoice unpaid. Refuse, so the caller retries under a key of its
		// own.
		return Result{}, false, fmt.Errorf(
			"%w: idempotency key %q was already used to settle another invoice", platform.ErrConflict, key)
	}
	out.Replayed = true
	return out, true, nil
}

// ForInvoice returns how one invoice was settled, scoped to a store.
//
// The invoice is looked up first so that an invoice belonging to another store
// is reported as missing rather than as an invoice with no payments — those
// are very different answers, and only one of them is true.
func (s *Service) ForInvoice(ctx context.Context, storeID, invoiceID uuid.UUID) ([]domain.Payment, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if invoiceID == uuid.Nil {
		return nil, ErrInvoiceRequired
	}
	if _, err := s.repo.GetInvoiceToSettle(ctx, s.db, storeID, invoiceID); err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return nil, fmt.Errorf("%w: no such invoice in this store", platform.ErrNotFound)
		}
		return nil, fmt.Errorf("load invoice: %w", err)
	}
	return s.repo.PaymentsForInvoice(ctx, s.db, storeID, invoiceID)
}

// Settlement reports a store's takings for one trading day: what was sold,
// how it was settled, what went back out in refunds, and what is left.
//
// Voided sales appear nowhere in it. Their tender rows survive as history —
// they record what happened before the cancellation — but a voided sale's
// money was handed back or never taken, so counting it would tell the
// shopkeeper to expect cash the drawer does not hold. The status filter the
// implementation this replaces got right is preserved on both sides of the
// breakdown.
func (s *Service) Settlement(ctx context.Context, storeID uuid.UUID, day time.Time) (domain.Settlement, error) {
	if storeID == uuid.Nil {
		return domain.Settlement{}, ErrStoreRequired
	}
	if day.IsZero() {
		day = s.now()
	}
	date := day.Format(domain.DateLayout)

	var out domain.Settlement
	err := s.db.InTxOpts(ctx, reportTx, func(ctx context.Context, tx platform.Tx) error {
		sales, err := s.repo.SalesForDay(ctx, tx, storeID, date)
		if err != nil {
			return fmt.Errorf("total the day's sales: %w", err)
		}
		tenders, err := s.repo.TendersForDay(ctx, tx, storeID, date)
		if err != nil {
			return fmt.Errorf("break the day down by payment mode: %w", err)
		}
		refunds, err := s.refunds.RefundsForDay(ctx, tx, storeID, date)
		if err != nil {
			return fmt.Errorf("total the day's refunds: %w", err)
		}

		out = domain.Summarise(domain.SettlementInput{
			StoreID:    storeID,
			Day:        day,
			TotalSales: sales.Total,
			Invoices:   sales.Count,
			Tenders:    tenders,
			Refunds:    refunds,
		})
		return nil
	})
	if err != nil {
		return domain.Settlement{}, err
	}
	return out, nil
}

// SettlementOn is Settlement for a day written as 2006-01-02.
func (s *Service) SettlementOn(ctx context.Context, storeID uuid.UUID, date string) (domain.Settlement, error) {
	day, err := domain.ParseDay(date)
	if err != nil {
		return domain.Settlement{}, err
	}
	return s.Settlement(ctx, storeID, day)
}
