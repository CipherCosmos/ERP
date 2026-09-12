// Package stock is the service layer for on-hand inventory: it owns
// transactions, enforces the rules that span more than one row, and is the only
// place that decides whether a movement is allowed.
//
// The Repository interface is declared here, by the consumer, so this package
// states what it needs rather than accepting whatever a database package
// happens to offer.
//
// # The rule this package exists to keep
//
// Stock never leaves except through a guarded relative update whose
// RowsAffected is checked. The read-modify-write that the HTTP handlers used
// (read the quantity into Go, compute the new one, write an absolute value)
// loses one of two concurrent adjustments every time they interleave, and it
// cannot be made safe by checking harder before the write — by the time the
// write runs, the check is stale. Every write in this file therefore hands the
// arithmetic and the sufficiency test to the database in one statement.
package stock

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "gbos/internal/domain/stock"

	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when a store-scoped operation is called without
// a store.
var ErrStoreRequired = errors.New("store is required")

// ErrSameStore is returned when a transfer names one store twice.
var ErrSameStore = errors.New("a transfer needs two different stores")

// Repository is the persistence this service needs. Every method takes a
// Querier so the service decides whether it runs in a transaction.
type Repository interface {
	ListStock(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.StockLine, error)
	ProductBatches(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) ([]domain.Batch, error)
	GetBatch(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, batchNo string) (domain.Batch, error)
	BatchesForDeduction(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) ([]domain.Batch, error)
	LowStock(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.LowStockItem, error)
	ExpiringBatches(ctx context.Context, q platform.Querier, storeID uuid.UUID, days int) ([]domain.ExpiringBatch, error)
	ExpiredBatches(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.ExpiringBatch, error)
	ProductUnitFactor(ctx context.Context, q platform.Querier, unitID, productID uuid.UUID) (domain.UnitFactor, error)
	ProductCostPrice(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) (money.Money, error)

	AddQuantity(ctx context.Context, q platform.Querier, in domain.Receipt) error
	DeductBatch(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, error)
	DeductBatchByID(ctx context.Context, q platform.Querier, id uuid.UUID, qty money.Quantity) (bool, error)
	Reserve(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, error)
	Release(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, error)

	InsertAdjustment(ctx context.Context, q platform.Querier, a *domain.Adjustment) error
	ListAdjustments(ctx context.Context, q platform.Querier, f domain.AdjustmentFilter) ([]domain.Adjustment, error)
	InsertLoss(ctx context.Context, q platform.Querier, l domain.Loss) error
	ListLosses(ctx context.Context, q platform.Querier, f domain.LossFilter) ([]domain.Loss, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies stock rules.
type Service struct {
	db   DB
	repo Repository
}

// New returns a stock service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ListStock returns every batch holding stock in a store.
func (s *Service) ListStock(ctx context.Context, storeID uuid.UUID) ([]domain.StockLine, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListStock(ctx, s.db, storeID)
}

// ProductStock returns the batches of one product held in one store, earliest
// expiry first — the order they will be sold in.
func (s *Service) ProductStock(ctx context.Context, productID, storeID uuid.UUID) ([]domain.Batch, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if productID == uuid.Nil {
		return nil, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}
	return s.repo.ProductBatches(ctx, s.db, productID, storeID)
}

// LowStock returns the products in a store at or below their reorder level.
func (s *Service) LowStock(ctx context.Context, storeID uuid.UUID) ([]domain.LowStockItem, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.LowStock(ctx, s.db, storeID)
}

// DefaultExpiryWindow is the horizon of the expiring-stock report when the
// caller names none.
const DefaultExpiryWindow = 30

// MaxExpiryWindow bounds the horizon, so a report cannot be asked to scan the
// whole table.
const MaxExpiryWindow = 3650

// Expiring returns batches that expire within the next `days` days and are not
// expired yet. It is the discount-and-move worklist; Expired is the write-off
// one, and no batch is on both.
func (s *Service) Expiring(ctx context.Context, storeID uuid.UUID, days int) ([]domain.ExpiringBatch, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if days <= 0 {
		days = DefaultExpiryWindow
	}
	if days > MaxExpiryWindow {
		days = MaxExpiryWindow
	}
	return s.repo.ExpiringBatches(ctx, s.db, storeID, days)
}

// Expired returns batches that are already past their expiry date and still
// hold stock. They are the write-off worklist.
func (s *Service) Expired(ctx context.Context, storeID uuid.UUID) ([]domain.ExpiringBatch, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ExpiredBatches(ctx, s.db, storeID)
}

// ListAdjustments returns the stock audit trail for a store.
func (s *Service) ListAdjustments(ctx context.Context, f domain.AdjustmentFilter) ([]domain.Adjustment, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListAdjustments(ctx, s.db, f)
}

// ListLosses returns recorded inventory losses for a store.
func (s *Service) ListLosses(ctx context.Context, f domain.LossFilter) ([]domain.Loss, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListLosses(ctx, s.db, f)
}

// ---------------------------------------------------------------------------
// Adjust
// ---------------------------------------------------------------------------

// AdjustInput is a stock-take correction or a manual receipt.
type AdjustInput struct {
	ProductID uuid.UUID
	StoreID   uuid.UUID
	UserID    uuid.UUID

	// BatchNo may be blank, meaning the product's untracked default batch.
	BatchNo string

	// Quantity is always a positive magnitude, in the product's BASE unit.
	// Type carries the direction, so a caller cannot express "add minus fifty"
	// and get a removal.
	//
	// When ProductUnitID is set this is computed rather than supplied; see
	// UnitQty.
	Quantity money.Quantity

	// ProductUnitID and UnitQty let a shopkeeper book goods in the way they
	// actually arrive — "ten sacks" rather than "250 kilograms" — without doing
	// the arithmetic at the keyboard, where it gets done wrong.
	//
	// They are optional and travel together. When set, Quantity is derived as
	// UnitQty x the unit's qty_in_base and any Quantity the caller sent is
	// ignored, because two independent statements of the same amount can
	// disagree and only one of them can move stock.
	//
	// This is a multiplication into the single pool, not a second balance:
	// ten sacks adds 250 kg to the same kilograms a 250 g sale takes 0.25 from.
	ProductUnitID uuid.UUID
	UnitQty       money.Quantity

	// Type must be domain.AdjustAdd or domain.AdjustRemove.
	Type domain.AdjustmentType

	Reason string

	// CostPrice, ExpiryDate and MfgDate apply to an addition only. A zero cost
	// means "do not reprice"; the catalogue cost price is used when the batch
	// is being created from nothing. Nil dates leave any stored dates alone.
	CostPrice  money.Money
	ExpiryDate *time.Time
	MfgDate    *time.Time
}

// AdjustResult reports the state of the batch after the adjustment.
type AdjustResult struct {
	Batch domain.Batch      `json:"batch"`
	Entry domain.Adjustment `json:"adjustment"`
	// Unit is the unit the caller stated the movement in, present only when
	// they named one. It carries the multiplier that was applied, so a client
	// can show "10 sacks = 250.000 kg" and a reader of the response can check
	// the arithmetic rather than trust it.
	Unit *domain.UnitFactor `json:"unit,omitempty"`
	// UnitQty is what the caller asked to move, in that unit.
	UnitQty money.Quantity `json:"unit_qty,omitempty"`
}

// Adjust adds stock to a batch or removes it, and records the movement.
//
// A removal is a single guarded relative update. It never reads the quantity
// into Go first, so two cashiers adjusting the same batch at the same moment
// cannot lose one another's work — which inventory.go:159-232 does, because it
// computes the new quantity in Go and writes it back absolutely.
func (s *Service) Adjust(ctx context.Context, in AdjustInput) (AdjustResult, error) {
	if in.StoreID == uuid.Nil {
		return AdjustResult{}, ErrStoreRequired
	}
	if in.Type != domain.AdjustAdd && in.Type != domain.AdjustRemove {
		return AdjustResult{}, fmt.Errorf("%w: adjustment type must be %q or %q, got %q",
			domain.ErrValidation, domain.AdjustAdd, domain.AdjustRemove, in.Type)
	}

	// Resolve the stated unit into base quantity BEFORE anything is validated
	// or written, so that everything downstream — the guard, the audit row, the
	// batch — sees one number in one unit.
	unit, err := s.resolveUnit(ctx, &in)
	if err != nil {
		return AdjustResult{}, err
	}
	if err := domain.ValidateMovement(in.Quantity); err != nil {
		return AdjustResult{}, err
	}

	// The signed delta the audit trail records.
	delta := in.Quantity
	if in.Type == domain.AdjustRemove {
		delta = delta.Neg()
	}
	entry, err := domain.NewAdjustment(in.ProductID, in.StoreID, in.UserID, in.BatchNo,
		delta, in.Type, s.reasonWithUnit(in, unit))
	if err != nil {
		return AdjustResult{}, err
	}

	var after domain.Batch
	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		switch in.Type {
		case domain.AdjustAdd:
			cost := in.CostPrice
			if cost.IsZero() {
				// Fall back to the catalogue cost so a batch is not booked in
				// at no cost. The error is returned rather than discarded.
				// This one is already per base unit, so it is not divided below.
				catalogueCost, err := s.repo.ProductCostPrice(ctx, tx, in.ProductID, in.StoreID)
				if err != nil {
					return fmt.Errorf("read catalogue cost price: %w", err)
				}
				cost = catalogueCost
			} else if unit != nil && !unit.QtyInBase.IsZero() {
				// A caller who counted in sacks priced in sacks. Stock is costed
				// per base unit, so a Rs.940 sack of 25 kg is Rs.37.60 a kilo.
				//
				// Storing the sack price against a kilo is silent and expensive:
				// the row commits, and every valuation, COGS figure and profit
				// line downstream is out by the size of the pack — twenty-five
				// times here. The receiving path already divides (see
				// purchasing's costPerBase); this path did not, which made the
				// same delivery cost different money depending on which screen
				// booked it in.
				cost = cost.DivQuantity(unit.QtyInBase)
			}
			if err := s.repo.AddQuantity(ctx, tx, domain.Receipt{
				ProductID:  in.ProductID,
				StoreID:    in.StoreID,
				BatchNo:    in.BatchNo,
				Quantity:   in.Quantity,
				CostPrice:  cost,
				ExpiryDate: in.ExpiryDate,
				MfgDate:    in.MfgDate,
			}); err != nil {
				return fmt.Errorf("add stock: %w", err)
			}

		case domain.AdjustRemove:
			ok, err := s.repo.DeductBatch(ctx, tx, in.ProductID, in.StoreID, in.BatchNo, in.Quantity)
			if err != nil {
				return fmt.Errorf("remove stock: %w", err)
			}
			if !ok {
				return s.explainFailedDeduction(ctx, tx, in.ProductID, in.StoreID, in.BatchNo, in.Quantity)
			}
		}

		if err := s.repo.InsertAdjustment(ctx, tx, &entry); err != nil {
			return fmt.Errorf("record adjustment: %w", err)
		}

		adjusted, err := s.repo.GetBatch(ctx, tx, in.ProductID, in.StoreID, in.BatchNo)
		if err != nil {
			return fmt.Errorf("read adjusted batch: %w", err)
		}
		after = adjusted
		return nil
	})
	if err != nil {
		return AdjustResult{}, err
	}
	result := AdjustResult{Batch: after, Entry: entry}
	if unit != nil {
		result.Unit = unit
		result.UnitQty = in.UnitQty
	}
	return result, nil
}

// resolveUnit turns "ten sacks" into the base quantity that will actually move,
// and reports the unit it used. It returns nil when the caller stated a plain
// base quantity, which is the ordinary case and needs no lookup at all.
//
// The factor is read from the database rather than accepted from the caller.
// A client that could name its own qty_in_base could book in one sack and have
// the shop's books credited with a tonne.
func (s *Service) resolveUnit(ctx context.Context, in *AdjustInput) (*domain.UnitFactor, error) {
	if in.ProductUnitID == uuid.Nil {
		return nil, nil
	}
	if !in.UnitQty.IsPositive() {
		return nil, fmt.Errorf("%w: how many of that unit? unit_qty must be greater than zero",
			domain.ErrValidation)
	}

	factor, err := s.repo.ProductUnitFactor(ctx, s.db, in.ProductUnitID, in.ProductID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return nil, fmt.Errorf("%w: no unit %s for this product",
				domain.ErrValidation, in.ProductUnitID)
		}
		return nil, fmt.Errorf("read product unit: %w", err)
	}
	if err := factor.Validate(); err != nil {
		return nil, err
	}

	// One multiplication, at the one point where what the shopkeeper said meets
	// what leaves the shelf.
	in.Quantity = factor.BaseQuantity(in.UnitQty)
	return &factor, nil
}

// reasonWithUnit keeps what the shopkeeper actually said in the audit trail.
//
// The stored quantity is in base units, so an entry reading "250.000" gives no
// hint that somebody counted ten sacks. Recording both is what lets a later
// stock-take dispute be settled.
func (s *Service) reasonWithUnit(in AdjustInput, unit *domain.UnitFactor) string {
	if unit == nil {
		return in.Reason
	}
	stated := fmt.Sprintf("%s x %s", in.UnitQty.String(), unit.Label)
	if in.Reason == "" {
		return stated
	}
	return in.Reason + " (" + stated + ")"
}

// explainFailedDeduction turns a guarded update that changed no rows into the
// error that says why: either the batch is not there, or it does not hold
// enough unreserved stock. The guard is authoritative; this read is only for
// the message, and it runs after the write has already been refused.
func (s *Service) explainFailedDeduction(ctx context.Context, tx platform.Tx,
	productID, storeID uuid.UUID, batchNo string, requested money.Quantity) error {
	b, err := s.repo.GetBatch(ctx, tx, productID, storeID, batchNo)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return fmt.Errorf("%w: no batch %q of that product in this store",
				domain.ErrInsufficientStock, batchNo)
		}
		return err
	}
	return domain.Insufficient(requested, b.Available())
}

// ---------------------------------------------------------------------------
// Reserve / release
// ---------------------------------------------------------------------------

// ReservationInput names a quantity of a batch to hold or to let go.
type ReservationInput struct {
	ProductID uuid.UUID
	StoreID   uuid.UUID
	UserID    uuid.UUID
	BatchNo   string
	Quantity  money.Quantity
	Reason    string
}

// Reserve holds stock against a pending order.
//
// Reserving more than is on hand is refused with a clear error rather than
// reaching the database and violating CHECK (reserved_qty <= quantity). The old
// handler compared two separately-read figures in Go, so a concurrent removal
// between the two reads produced a constraint violation instead.
func (s *Service) Reserve(ctx context.Context, in ReservationInput) error {
	return s.reservation(ctx, in, domain.AdjustReserve)
}

// Release returns reserved stock to general availability.
func (s *Service) Release(ctx context.Context, in ReservationInput) error {
	return s.reservation(ctx, in, domain.AdjustRelease)
}

func (s *Service) reservation(ctx context.Context, in ReservationInput, kind domain.AdjustmentType) error {
	if in.StoreID == uuid.Nil {
		return ErrStoreRequired
	}
	if err := domain.ValidateMovement(in.Quantity); err != nil {
		return err
	}

	delta := in.Quantity
	if kind == domain.AdjustRelease {
		delta = delta.Neg()
	}
	entry, err := domain.NewAdjustment(in.ProductID, in.StoreID, in.UserID, in.BatchNo,
		delta, kind, in.Reason)
	if err != nil {
		return err
	}

	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var ok bool
		var err error
		if kind == domain.AdjustReserve {
			ok, err = s.repo.Reserve(ctx, tx, in.ProductID, in.StoreID, in.BatchNo, in.Quantity)
		} else {
			ok, err = s.repo.Release(ctx, tx, in.ProductID, in.StoreID, in.BatchNo, in.Quantity)
		}
		if err != nil {
			return fmt.Errorf("%s stock: %w", kind, err)
		}
		if !ok {
			b, getErr := s.repo.GetBatch(ctx, tx, in.ProductID, in.StoreID, in.BatchNo)
			if getErr != nil {
				if errors.Is(getErr, platform.ErrNotFound) {
					return fmt.Errorf("%w: no batch %q of that product in this store",
						domain.ErrInsufficientStock, in.BatchNo)
				}
				return getErr
			}
			if kind == domain.AdjustReserve {
				return domain.Insufficient(in.Quantity, b.Available())
			}
			return fmt.Errorf("%w: %s requested but only %s is reserved",
				domain.ErrInsufficientReserved, in.Quantity.StringFixed(), b.ReservedQty.StringFixed())
		}
		return s.repo.InsertAdjustment(ctx, tx, &entry)
	})
}

// ---------------------------------------------------------------------------
// Transfer
// ---------------------------------------------------------------------------

// TransferInput moves one batch of a product between stores.
type TransferInput struct {
	ProductID   uuid.UUID
	FromStoreID uuid.UUID
	ToStoreID   uuid.UUID
	UserID      uuid.UUID
	BatchNo     string
	Quantity    money.Quantity
	Reason      string
}

// Transfer moves stock from one store to another.
//
// The quantity is validated positive before anything is written. That check is
// the fix for a stock-minting defect: inventory.go:305 issued
// `SET quantity = quantity - ?` with the request body's number, so a negative
// quantity added to the source and added again at the destination, creating
// goods that never existed. The deduction is then guarded and its RowsAffected
// checked, so the destination is only credited if the source was actually
// debited — and both happen in one transaction.
func (s *Service) Transfer(ctx context.Context, in TransferInput) error {
	if in.FromStoreID == uuid.Nil || in.ToStoreID == uuid.Nil {
		return ErrStoreRequired
	}
	if in.FromStoreID == in.ToStoreID {
		return ErrSameStore
	}
	if err := domain.ValidateMovement(in.Quantity); err != nil {
		return err
	}

	out, err := domain.NewAdjustment(in.ProductID, in.FromStoreID, in.UserID, in.BatchNo,
		in.Quantity.Neg(), domain.AdjustTransferOut, in.Reason)
	if err != nil {
		return err
	}
	into, err := domain.NewAdjustment(in.ProductID, in.ToStoreID, in.UserID, in.BatchNo,
		in.Quantity, domain.AdjustTransferIn, in.Reason)
	if err != nil {
		return err
	}

	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		// Read the source batch for the cost basis and dates to carry across.
		// This read decides nothing about sufficiency — the guarded update
		// below does that — so it cannot go stale in a way that matters.
		src, err := s.repo.GetBatch(ctx, tx, in.ProductID, in.FromStoreID, in.BatchNo)
		if err != nil {
			if errors.Is(err, platform.ErrNotFound) {
				return fmt.Errorf("%w: no batch %q of that product in the source store",
					domain.ErrInsufficientStock, in.BatchNo)
			}
			return fmt.Errorf("read source batch: %w", err)
		}

		ok, err := s.repo.DeductBatch(ctx, tx, in.ProductID, in.FromStoreID, in.BatchNo, in.Quantity)
		if err != nil {
			return fmt.Errorf("debit source store: %w", err)
		}
		if !ok {
			return domain.Insufficient(in.Quantity, src.Available())
		}

		if err := s.repo.AddQuantity(ctx, tx, domain.Receipt{
			ProductID:  in.ProductID,
			StoreID:    in.ToStoreID,
			BatchNo:    in.BatchNo,
			Quantity:   in.Quantity,
			CostPrice:  src.CostPrice,
			ExpiryDate: src.ExpiryDate,
			MfgDate:    src.MfgDate,
		}); err != nil {
			return fmt.Errorf("credit destination store: %w", err)
		}

		if err := s.repo.InsertAdjustment(ctx, tx, &out); err != nil {
			return fmt.Errorf("record transfer out: %w", err)
		}
		return s.repo.InsertAdjustment(ctx, tx, &into)
	})
}

// ---------------------------------------------------------------------------
// Loss
// ---------------------------------------------------------------------------

// LossInput writes stock off.
type LossInput struct {
	ProductID uuid.UUID
	StoreID   uuid.UUID
	UserID    uuid.UUID
	BatchNo   string
	Quantity  money.Quantity
	LossType  string
	Reason    string
}

// RecordLoss writes stock off and deducts it, in one transaction.
//
// The deduction runs through the same FEFO walk a sale does, so a loss larger
// than any single batch spans several. That is the fix for the worst defect in
// the old code: returns.go:226-238 deducted with a subquery that picked one
// batch holding the whole quantity and never checked RowsAffected, so when no
// single batch held enough the loss row was written, no stock moved, and the
// API answered 201. The books and the shelf then disagreed permanently, with
// nothing in the response to say so.
func (s *Service) RecordLoss(ctx context.Context, in LossInput) (domain.Loss, error) {
	if in.StoreID == uuid.Nil {
		return domain.Loss{}, ErrStoreRequired
	}
	loss, err := domain.NewLoss(in.ProductID, in.StoreID, in.UserID, in.BatchNo,
		in.Quantity, in.LossType, in.Reason)
	if err != nil {
		return domain.Loss{}, err
	}

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		deduction, err := s.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: in.ProductID,
			StoreID:   in.StoreID,
			BatchHint: in.BatchNo,
			Quantity:  in.Quantity,
		})
		if err != nil {
			return err
		}

		// Record the batch the loss actually came out of, not the one that was
		// asked for; when the write-off spans batches, Consumed carries all of
		// them.
		loss.BatchNo = deduction.PrimaryBatch()
		loss.Consumed = deduction.Batches

		if err := s.repo.InsertLoss(ctx, tx, loss); err != nil {
			return fmt.Errorf("record loss: %w", err)
		}

		entry, err := domain.NewAdjustment(in.ProductID, in.StoreID, in.UserID, loss.BatchNo,
			in.Quantity.Neg(), domain.AdjustLoss, in.LossType)
		if err != nil {
			return err
		}
		return s.repo.InsertAdjustment(ctx, tx, &entry)
	})
	if err != nil {
		return domain.Loss{}, err
	}
	return loss, nil
}

// ---------------------------------------------------------------------------
// FEFO / FIFO deduction
// ---------------------------------------------------------------------------

// DeductFEFO removes a quantity of a product from a store, consuming batches in
// first-expired-first-out order — earliest expiry first, undated batches last,
// then oldest received first — and returns the batches consumed together with
// the weighted-average unit cost for the COGS snapshot.
//
// It takes the caller's transaction rather than opening its own, because a sale
// deducts stock for every line of an invoice and either the whole invoice
// commits or none of it does.
//
// # The contract on failure
//
// A deduction that cannot be satisfied in full returns an error wrapping
// domain.ErrInsufficientStock, and by then it may already have debited some
// batches. That is safe and deliberate: the caller's transaction must roll
// back, and platform.InTx does exactly that when fn returns an error. There is
// no partial fill — a sale for five units either takes five or takes none.
//
// # Why the rows are drained before anything is written
//
// The batch walk reads a cursor and then writes to the same connection. The old
// implementation (billing.go:54-98) interleaved them, calling tx.Exec inside
// `for rows.Next()`. SQLite tolerated that because it buffered the result set;
// pgx does not — a second operation while a cursor is open is "conn busy",
// which arrives as `driver: bad connection` — so the first multi-batch sale
// after the cutover would have failed outright. The repository therefore
// returns a fully drained slice and every write here happens after the cursor
// is closed. TestFEFOConsumesEarliestExpiringBatchFirst spans three batches and
// so cannot pass any other way.
//
// # Exactness
//
// Quantities are money.Quantity and compared exactly. The predicate the old
// code used to decide whether the deduction was satisfied was
// `if remaining > 0.001` — a float epsilon that let a sale complete against a
// thousandth of a unit that was not there, and that would drift with the
// magnitude of the numbers involved. There is no epsilon here.
func (s *Service) DeductFEFO(ctx context.Context, tx platform.Tx, req domain.DeductionRequest) (domain.Deduction, error) {
	if err := req.Validate(); err != nil {
		return domain.Deduction{}, err
	}

	consumed := make([]domain.ConsumedBatch, 0, 2)
	remaining := req.Quantity

	// Drain the cursor completely. Nothing below writes until this returns.
	//
	// This is also the ONLY statement in this function that takes a lock, and
	// that is deliberate. A batch hint used to be served by its own
	// read-then-guarded-update against one named row, taken before this walk —
	// so a deduction with a hint and one without acquired the same product's
	// rows in two different orders and could deadlock each other over a single
	// product. One locking statement, in one order, cannot.
	batches, err := s.repo.BatchesForDeduction(ctx, tx, req.ProductID, req.StoreID)
	if err != nil {
		return domain.Deduction{}, fmt.Errorf("read batches for deduction: %w", err)
	}

	// The hint wins only when the named batch can serve the whole request on
	// its own — a return going back to the lot it came from. When it cannot it
	// is ignored entirely and the walk below decides, because a hint is an
	// optimisation and never a constraint.
	if req.BatchHint != domain.DefaultBatch {
		for _, b := range batches {
			if b.BatchNo != req.BatchHint || b.Available().LessThan(remaining) {
				continue
			}
			// The guard is re-evaluated in SQL rather than trusted from the
			// row just read. The FOR UPDATE above means it cannot have moved,
			// and the sufficiency test still belongs in the write.
			ok, err := s.repo.DeductBatchByID(ctx, tx, b.ID, remaining)
			if err != nil {
				return domain.Deduction{}, fmt.Errorf("deduct hinted batch %q: %w", b.BatchNo, err)
			}
			if ok {
				consumed = append(consumed, domain.ConsumedBatch{
					StockID:    b.ID,
					BatchNo:    b.BatchNo,
					Quantity:   remaining,
					UnitCost:   b.CostPrice,
					ExpiryDate: b.ExpiryDate,
				})
				remaining = money.ZeroQuantity()
			}
			break
		}
	}

	if remaining.IsPositive() {
		for _, b := range batches {
			if !remaining.IsPositive() {
				break
			}
			available := b.Available()
			if !available.IsPositive() {
				continue
			}
			take := available
			if take.GreaterThan(remaining) {
				take = remaining
			}

			ok, err := s.repo.DeductBatchByID(ctx, tx, b.ID, take)
			if err != nil {
				return domain.Deduction{}, fmt.Errorf("deduct batch %q: %w", b.BatchNo, err)
			}
			if !ok {
				// Another transaction emptied this batch after it was read.
				// The guard refused the write, so nothing was oversold; move
				// on to the next batch.
				continue
			}

			consumed = append(consumed, domain.ConsumedBatch{
				StockID:    b.ID,
				BatchNo:    b.BatchNo,
				Quantity:   take,
				UnitCost:   b.CostPrice,
				ExpiryDate: b.ExpiryDate,
			})
			remaining = remaining.Sub(take)
		}
	}

	// Exact, not "close enough". remaining is a decimal; if a thousandth of a
	// unit is missing, a thousandth of a unit is missing.
	if remaining.IsPositive() {
		// What was actually available is what the walk managed to take. It is
		// read from the debits just made rather than from a fresh SUM, which
		// would report the post-debit figure and understate the shortfall.
		return domain.Deduction{}, domain.Insufficient(req.Quantity, req.Quantity.Sub(remaining))
	}

	return domain.NewDeduction(consumed, req.Quantity), nil
}
