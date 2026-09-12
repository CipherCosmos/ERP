// Package salesreturn is the service layer for goods moving without a sale:
// customer returns and inventory write-offs.
//
// It owns the transactions and declares narrow ports for what it needs from its
// neighbours, rather than importing their services. Stock movement in
// particular is not this package's business — it states that it needs goods put
// back and goods taken out, and the composition root satisfies that from the
// stock aggregate.
//
// # The rule this package exists to keep
//
// Approving a return moves stock, so approving a return must happen at most
// once, and only for the store that owns it. Both halves of that live in a
// single guarded UPDATE whose RowsAffected decides whether this call owns the
// approval. The implementation this replaces (returns.go:110-171) had neither
// half: it selected the return by id alone, never read the status, updated
// without a guard, restored the goods into the store named in the *query
// string*, and ignored the error from Commit. Approving twice restored the
// stock twice; approving somebody else's return moved their goods onto your
// shelf.
package salesreturn

import (
	"context"
	"errors"
	"fmt"

	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	"gbos/internal/domain/reconcile"
	domain "gbos/internal/domain/salesreturn"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"

	"github.com/google/uuid"
)

var (
	// ErrStoreRequired is returned when a store-scoped operation is called
	// without a store.
	ErrStoreRequired = errors.New("store is required")

	// ErrAlreadyApproved is returned when a return has already been approved.
	// It is a distinct error because the caller usually wants to treat a
	// double-click as harmless rather than as a failure — and because the one
	// thing it must never do is restore the goods a second time.
	ErrAlreadyApproved = errors.New("return has already been approved")

	// ErrInvoiceVoided is returned when the sale a return refers to has been
	// cancelled. See Approve for why that is refused rather than allowed.
	ErrInvoiceVoided = errors.New("the invoice this return refers to has been voided")

	// ErrNotOnInvoice is returned when a return names an invoice that never
	// sold the product being returned.
	ErrNotOnInvoice = errors.New("the invoice does not carry this product")

	// ErrOverReturn is returned when a return would take back more of a product
	// than the invoice sold.
	ErrOverReturn = errors.New("more is being returned than was sold")
)

// Repository is the persistence this service needs. Every method takes a
// Querier so the service decides whether it runs in a transaction.
type Repository interface {
	InsertReturn(ctx context.Context, q platform.Querier, r domain.Return) error
	GetReturn(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (domain.Return, error)
	ApproveReturn(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (bool, error)
	ListReturns(ctx context.Context, q platform.Querier, f domain.ReturnFilter) ([]domain.Return, error)
	SoldFrom(ctx context.Context, q platform.Querier, storeID, invoiceID, productID uuid.UUID) (repo.SoldFrom, error)

	InsertLoss(ctx context.Context, q platform.Querier, l domain.Loss) error
	ListLosses(ctx context.Context, q platform.Querier, f domain.LossFilter) ([]domain.Loss, error)
}

// StockMover is the part of the stock aggregate this one needs: put goods back
// when a customer returns them, take goods out when they are written off.
//
// Both methods take the caller's transaction so the movement lands with the
// document that caused it. A write-off that commits without its stock, or stock
// that leaves without a write-off, is the kind of discrepancy nobody finds
// until a physical count — and it is exactly what the old loss handler shipped.
type StockMover interface {
	// ReturnToStock adds goods back into a named batch, creating the batch if
	// the lot has since been emptied.
	ReturnToStock(ctx context.Context, q platform.Querier, in stockdomain.Receipt) error

	// WriteOff takes goods out of a store, consuming batches
	// first-expired-first-out, and reports which lots it took them from. It
	// must fail rather than fill short: a write-off of ten units either takes
	// ten or takes none.
	//
	// FEFO is the whole point of this signature. A write-off larger than any
	// single batch spans several, and the single-batch subquery the old handler
	// used (`WHERE rowid = (SELECT ... quantity >= ? ... LIMIT 1)`) matched
	// nothing at all in that case — changing zero rows, returning a nil error,
	// and leaving the API to answer 201 with the goods still on the shelf.
	WriteOff(ctx context.Context, tx platform.Tx, req stockdomain.DeductionRequest) (stockdomain.Deduction, error)

	// ProductCostPrice is the store's recorded cost for one base unit of a
	// product. It values goods coming back with no sale behind them, so a
	// goodwill return does not join a batch at no cost and write it down.
	ProductCostPrice(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) (money.Money, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service records customer returns and inventory losses.
type Service struct {
	db    DB
	repo  Repository
	stock StockMover
	// exceptions may be nil, and a nil one records nothing. A return must not
	// fail because reconciliation is not wired up.
	exceptions ExceptionRecorder
}

// ExceptionRecorder records what the server could not verify about a return it
// had to accept anyway. See the reconcile package for why a refund already paid
// is flagged rather than refused.
type ExceptionRecorder interface {
	Insert(ctx context.Context, q platform.Querier, e reconcile.Exception) error
}

// New returns a returns-and-losses service.
func New(db DB, r Repository, stock StockMover) *Service {
	return &Service{db: db, repo: r, stock: stock}
}

// WithExceptions attaches the recorder for what the server cannot verify about a
// return it accepts anyway.
func (s *Service) WithExceptions(rec ExceptionRecorder) *Service {
	clone := *s
	clone.exceptions = rec
	return &clone
}

// ---------------------------------------------------------------------------
// Customer returns
// ---------------------------------------------------------------------------

// CreateReturn records a customer return. It moves no stock: a return is born
// pending and the goods go back only when it is approved.
//
// When the return names an invoice, that invoice is checked here — it must
// belong to this store, it must not be voided, it must actually have sold the
// product, and it must not already have had this much of it taken back. None of
// those checks existed before, so a return could be raised against any invoice
// in any store, for a product that was never on it, for any quantity at all.
func (s *Service) CreateReturn(ctx context.Context, in domain.ReturnInput) (domain.Return, error) {
	if in.StoreID == uuid.Nil {
		return domain.Return{}, ErrStoreRequired
	}
	ret, err := domain.NewReturn(in)
	if err != nil {
		return domain.Return{}, err
	}

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		// What could not be verified, when the till could not verify it. A
		// return taken while the connection was down is a claim the cashier has
		// already paid out on: the goods are back on the counter and the money
		// has gone. Refusing it on sync leaves the shop having paid against a
		// record it does not hold, so it lands and the doubt is written down.
		var problem error

		if ret.InvoiceID != nil {
			sold, err := s.soldFrom(ctx, tx, ret)
			if err != nil {
				return err
			}
			if !sold.LineFound {
				problem = fmt.Errorf("%w: invoice %s sold no %s", ErrNotOnInvoice, *ret.InvoiceID, ret.ProductID)
			} else {
				// The already-returned figure counts every return against this
				// invoice line whatever its status, because a pending return is
				// a claim on the same goods as an approved one.
				outstanding := sold.SoldQuantity.Sub(sold.ReturnedQuantity)
				if ret.Quantity.GreaterThan(outstanding) {
					problem = fmt.Errorf("%w: %s sold, %s already returned, %s asked for",
						ErrOverReturn, sold.SoldQuantity.StringFixed(),
						sold.ReturnedQuantity.StringFixed(), ret.Quantity.StringFixed())
				}
			}
		}
		if problem != nil && !ret.RaisedOffline {
			// At the counter, with a connection, this is something the cashier
			// can act on before handing anything over.
			return problem
		}
		if err := s.repo.InsertReturn(ctx, tx, ret); err != nil {
			return err
		}
		if problem == nil {
			return nil
		}
		return s.recordOverReturn(ctx, tx, ret, problem)
	})
	if err != nil {
		return domain.Return{}, err
	}
	return ret, nil
}

// recordOverReturn writes the exception for a return the server could not
// reconcile against its invoice.
//
// It does not fail the return. By the time it runs the customer has their money
// and the goods are on the counter; an exception that could roll that back would
// be a way of losing a refund to a bookkeeping note.
func (s *Service) recordOverReturn(ctx context.Context, tx platform.Tx, ret domain.Return, problem error) error {
	if s.exceptions == nil {
		return nil
	}
	e, err := reconcile.New(ret.StoreID, reconcile.OverReturned,
		"More came back than this invoice sold, or it never sold this at all. The till was offline "+
			"and could not check. The money has gone, so decide whether to accept the return or "+
			"recover it from the customer.")
	if err != nil {
		return err
	}
	product := ret.ProductID
	e.ProductID = &product
	e.InvoiceID = ret.InvoiceID
	e.Expected = "a return within what the invoice sold"
	e.Actual = problem.Error()
	return s.exceptions.Insert(ctx, tx, e)
}

// Approve accepts a return and puts the goods back on the shelf, exactly once.
//
// # Exactly once
//
// The status change is a guarded UPDATE carrying both the pending predicate and
// the store predicate, and its RowsAffected decides whether this call owns the
// approval. A second approval — a double-clicked button, a replayed request, a
// second goroutine — changes no row, moves no stock, and is told
// ErrAlreadyApproved. A caller in the wrong store changes no row either, and is
// told the return does not exist, which is also the truth from where they are
// standing.
//
// # Where the goods go
//
// Into the batch they left on, read from the invoice line, when the return
// names an invoice that carried the product. Otherwise into the product's
// default (untracked) lot. Not, as before, into "the first row of the stock
// table for this product with a positive quantity", and emphatically not into a
// freshly invented "RETURN-STOCK" batch, which fragmented the product's on-hand
// quantity and booked the goods back in at a cost price of zero.
//
// The cost carried on the receipt is zero, which means "do not reprice": the
// goods are the same goods, and putting them back is not a revaluation.
//
// # Returns against a voided invoice
//
// Refused. Voiding an invoice already returns every line of it to stock
// (billing.Void does exactly that), so approving a customer return against a
// voided sale would put the same physical goods back a second time — the very
// stock-inventing defect this port exists to close — and would refund a
// customer whose receivable has already been cancelled. The check runs inside
// the transaction and after the guarded UPDATE, so a void that lands between
// the two rolls this approval back rather than racing it.
func (s *Service) Approve(ctx context.Context, storeID, id uuid.UUID) (domain.Return, error) {
	if storeID == uuid.Nil {
		return domain.Return{}, ErrStoreRequired
	}

	var approved domain.Return
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		ok, err := s.repo.ApproveReturn(ctx, tx, storeID, id)
		if err != nil {
			return fmt.Errorf("approve return: %w", err)
		}
		if !ok {
			// Nothing changed. Either the return is not this store's (or does
			// not exist), or it was already approved. Tell those apart so the
			// caller can report the right thing — and note that the read is
			// store-scoped, so the wrong-store case surfaces as ErrNotFound and
			// leaks nothing about another shop's paperwork.
			if _, err := s.repo.GetReturn(ctx, tx, storeID, id); err != nil {
				return err
			}
			return ErrAlreadyApproved
		}

		ret, err := s.repo.GetReturn(ctx, tx, storeID, id)
		if err != nil {
			return fmt.Errorf("read approved return: %w", err)
		}

		batch := stockdomain.DefaultBatch
		// restored is what the goods coming back are worth. Zero means "not
		// known from a sale", and the fallback below values them at the
		// catalogue cost rather than letting them dilute the batch they join.
		restored := money.Zero()
		if ret.InvoiceID != nil {
			sold, err := s.soldFrom(ctx, tx, ret)
			if err != nil {
				return err
			}
			if billingdomain.Status(sold.InvoiceStatus) == billingdomain.StatusVoided {
				return fmt.Errorf("%w: invoice %s was cancelled and its goods are already back in stock",
					ErrInvoiceVoided, *ret.InvoiceID)
			}
			if sold.LineFound {
				batch = sold.BatchNo
			}
			// The share of what the line cost that corresponds to the quantity
			// coming back. A full return restores exactly what the line cost,
			// with no division at all.
			if sold.SoldQuantity.IsPositive() {
				restored = sold.SoldCost.Share(ret.Quantity, sold.SoldQuantity)
			}
		}
		if restored.IsZero() {
			// A goodwill return with no invoice behind it, or a sale whose cost
			// was never recorded. The goods are real and have value; valuing
			// them at nothing would quietly write down the batch they rejoin.
			catalogue, err := s.stock.ProductCostPrice(ctx, tx, ret.ProductID, ret.StoreID)
			if err != nil {
				return fmt.Errorf("read catalogue cost price: %w", err)
			}
			restored = catalogue.Mul(ret.Quantity)
		}

		// The destination store is the return's own, read from the row this
		// transaction just claimed — never a store the caller named. That is
		// the fix for returns.go:149, which restored into the query string's
		// store_id and so let one shop's approval credit another shop's shelf.
		if err := s.stock.ReturnToStock(ctx, tx, stockdomain.Receipt{
			ProductID: ret.ProductID,
			StoreID:   ret.StoreID,
			BatchNo:   batch,
			Quantity:  ret.Quantity,
			TotalCost: restored,
		}); err != nil {
			return fmt.Errorf("return goods to stock: %w", err)
		}

		ret.RestoredBatch = batch
		approved = ret
		return nil
	})
	if err != nil {
		return domain.Return{}, err
	}
	return approved, nil
}

// soldFrom loads the invoice line a return refers to, turning a missing invoice
// into a validation error rather than a bare not-found.
func (s *Service) soldFrom(ctx context.Context, tx platform.Tx, ret domain.Return) (repo.SoldFrom, error) {
	sold, err := s.repo.SoldFrom(ctx, tx, ret.StoreID, *ret.InvoiceID, ret.ProductID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return repo.SoldFrom{}, fmt.Errorf("%w: no invoice %s in this store",
				domain.ErrValidation, *ret.InvoiceID)
		}
		return repo.SoldFrom{}, fmt.Errorf("read the sale this return refers to: %w", err)
	}
	return sold, nil
}

// GetReturn returns one customer return, scoped to a store.
func (s *Service) GetReturn(ctx context.Context, storeID, id uuid.UUID) (domain.Return, error) {
	if storeID == uuid.Nil {
		return domain.Return{}, ErrStoreRequired
	}
	return s.repo.GetReturn(ctx, s.db, storeID, id)
}

// ListReturns returns a store's customer returns.
func (s *Service) ListReturns(ctx context.Context, f domain.ReturnFilter) ([]domain.Return, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, fmt.Errorf("%w: %q is not a return status", domain.ErrValidation, f.Status)
	}
	return s.repo.ListReturns(ctx, s.db, f)
}

// ---------------------------------------------------------------------------
// Inventory losses
// ---------------------------------------------------------------------------

// RecordLoss writes stock off and takes it out of inventory, in one
// transaction.
//
// The goods leave first, through a first-expired-first-out walk that spans as
// many batches as it needs, and the loss row is written only once that has
// succeeded. Either both happen or neither does.
//
// That ordering, and the transaction around it, are the fix for the worst
// defect in the code this replaces. returns.go:226-238 inserted the loss row
// and then deducted with
//
//	UPDATE stock SET quantity = quantity - ?
//	 WHERE rowid = (SELECT rowid FROM stock
//	                 WHERE product_id = ? AND store_id = ? AND quantity >= ?
//	                 ORDER BY quantity DESC LIMIT 1)
//
// which has three faults at once. `rowid` does not exist in Postgres. The
// subquery demands one batch holding the entire quantity, so a write-off of 10
// units spread over batches of 6 and 5 matched nothing. And nothing checked
// RowsAffected, so zero rows changed, err was nil, the API answered 201 — and
// the loss was on the books with the goods still on the shelf, permanently, with
// nothing in the response to say so.
func (s *Service) RecordLoss(ctx context.Context, in domain.LossInput) (domain.Loss, error) {
	if in.StoreID == uuid.Nil {
		return domain.Loss{}, ErrStoreRequired
	}
	loss, err := domain.NewLoss(in)
	if err != nil {
		return domain.Loss{}, err
	}

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		deduction, err := s.stock.WriteOff(ctx, tx, stockdomain.DeductionRequest{
			ProductID: loss.ProductID,
			StoreID:   loss.StoreID,
			BatchHint: loss.BatchNo,
			Quantity:  loss.Quantity,
		})
		if err != nil {
			// Insufficient stock arrives here wrapping
			// stockdomain.ErrInsufficientStock. It is returned untouched so the
			// caller can classify it, and returning it rolls the transaction
			// back — so no loss row survives a write-off that did not happen.
			return err
		}

		// Record the lot the goods actually came out of, not the one that was
		// asked for. When the write-off spans batches, Batches carries all of
		// them and batch_no carries the first — which under FEFO is the
		// earliest-expiring one.
		loss.BatchNo = deduction.PrimaryBatch()
		loss.Batches = allocations(deduction)

		if err := s.repo.InsertLoss(ctx, tx, loss); err != nil {
			return fmt.Errorf("record inventory loss: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.Loss{}, err
	}
	return loss, nil
}

// allocations maps what the stock aggregate consumed onto this aggregate's own
// vocabulary, so the domain package does not depend on stock's.
func allocations(d stockdomain.Deduction) []domain.Allocation {
	if len(d.Batches) == 0 {
		return nil
	}
	out := make([]domain.Allocation, len(d.Batches))
	for i, b := range d.Batches {
		out[i] = domain.Allocation{BatchNo: b.BatchNo, Quantity: b.Quantity, Cost: b.Cost}
	}
	return out
}

// ListLosses returns a store's inventory write-offs.
func (s *Service) ListLosses(ctx context.Context, f domain.LossFilter) ([]domain.Loss, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListLosses(ctx, s.db, f)
}
