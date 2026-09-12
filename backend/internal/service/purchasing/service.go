// Package purchasing is the service layer for the buy side: vendors, purchase
// orders, goods receipts, purchase returns and vendor payments.
//
// It owns transactions, enforces the rules that span more than one row, and is
// the only place that decides what a caller is allowed to do. The Repository
// and StockWriter interfaces are declared here, by the consumer, so this
// package states what it needs rather than accepting whatever a database or a
// neighbouring aggregate happens to offer.
package purchasing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/purchasing"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when an operation that is inherently
// store-scoped is called without one.
var ErrStoreRequired = errors.New("store is required")

// docTypePurchaseOrder is the document_sequences.doc_type this service draws
// numbers under.
const docTypePurchaseOrder = "purchase_order"

// Repository is the persistence this service needs. Every method takes a
// Querier so the service decides whether it runs in a transaction.
type Repository interface {
	CreateVendor(ctx context.Context, q platform.Querier, v domain.Vendor) error
	UpdateVendor(ctx context.Context, q platform.Querier, v domain.Vendor) (bool, error)
	SetVendorActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)
	GetVendor(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Vendor, error)
	ListVendors(ctx context.Context, q platform.Querier, f domain.VendorFilter) ([]domain.Vendor, error)
	VendorExposure(ctx context.Context, q platform.Querier, vendorID uuid.UUID) (domain.Exposure, error)

	StoreGSTIN(ctx context.Context, q platform.Querier, storeID uuid.UUID) (string, error)
	TerminalCode(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID) (string, error)
	ProductsForPurchase(ctx context.Context, q platform.Querier, storeID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]repo.PurchaseProduct, error)
	UserName(ctx context.Context, q platform.Querier, userID uuid.UUID) (string, error)
	NextDocumentNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, docType, finYear string) (int64, error)

	CreatePurchaseOrder(ctx context.Context, q platform.Querier, po domain.PurchaseOrder) error
	CreatePurchaseItem(ctx context.Context, q platform.Querier, it domain.PurchaseItem) error
	GetPurchaseOrder(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.PurchaseOrder, error)
	ListPurchaseOrders(ctx context.Context, q platform.Querier, f domain.POFilter) ([]domain.PurchaseOrder, error)
	ListPurchaseItems(ctx context.Context, q platform.Querier, poID uuid.UUID, forUpdate bool) ([]domain.PurchaseItem, error)
	AddReceivedQty(ctx context.Context, q platform.Querier, itemID uuid.UUID, qty money.Quantity) (bool, error)
	SetPurchaseOrderStatus(ctx context.Context, q platform.Querier, id, storeID uuid.UUID, next domain.POStatus, from []domain.POStatus) (bool, error)
	LockPurchaseOrder(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.POStatus, string, error)

	CreateGoodsReceipt(ctx context.Context, q platform.Querier, g domain.GoodsReceipt) error
	CreateGoodsReceiptItem(ctx context.Context, q platform.Querier, it domain.GoodsReceiptItem) error
	GetGoodsReceipt(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.GoodsReceipt, error)
	ListGoodsReceipts(ctx context.Context, q platform.Querier, storeID uuid.UUID, poID *uuid.UUID, limit int) ([]domain.GoodsReceipt, error)
	ListGoodsReceiptItems(ctx context.Context, q platform.Querier, grID uuid.UUID) ([]domain.GoodsReceiptItem, error)

	CreatePurchaseReturn(ctx context.Context, q platform.Querier, pr domain.PurchaseReturn) error
	CreatePurchaseReturnItem(ctx context.Context, q platform.Querier, it domain.PurchaseReturnItem) error
	GetPurchaseReturn(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.PurchaseReturn, error)
	ListPurchaseReturns(ctx context.Context, q platform.Querier, f domain.ReturnFilter) ([]domain.PurchaseReturn, error)
	ListPurchaseReturnItems(ctx context.Context, q platform.Querier, returnID uuid.UUID) ([]domain.PurchaseReturnItem, error)
	ApprovePurchaseReturn(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (bool, error)

	CreateVendorPayment(ctx context.Context, q platform.Querier, p domain.VendorPayment) error
	ListVendorPayments(ctx context.Context, q platform.Querier, storeID uuid.UUID, vendorID *uuid.UUID, limit int) ([]domain.VendorPayment, error)
}

// StockMovement is one product's worth of stock going in or out.
type StockMovement struct {
	StoreID   uuid.UUID
	ProductID uuid.UUID
	// BatchNo identifies the physical lot. It is never blank by the time it
	// reaches the stock aggregate: the schema makes batch_no NOT NULL DEFAULT ''
	// precisely so the (product, store, batch) unique index bites, and a
	// distinct batch per receipt is what keeps landed costs separable.
	BatchNo  string
	Quantity money.Quantity
	// TotalCost is what the goods coming in cost, in full — eight sacks at
	// Rs.940 is Rs.7,520.00. A total and not a rate: the batch holds value, and
	// a per-unit cost for a pack size that does not divide evenly cannot be
	// stored without losing paise.
	//
	// It is meaningless on the way out — what a batch cost is already recorded
	// against that batch — and is left zero there.
	TotalCost money.Money
}

// StockWriter is the slice of the stock aggregate that purchasing needs, and
// nothing more.
//
// Purchasing does not write to the stock table itself. Goods arriving and goods
// going back to a vendor are stock movements, and stock movements — batch
// selection, cost layering, the non-negative invariant — belong to the stock
// aggregate. Both methods take a Querier so the movement lands inside the same
// transaction as the document that caused it: a receipt that commits without
// its stock, or stock that commits without its receipt, is the kind of
// discrepancy nobody finds until a physical count.
type StockWriter interface {
	// ReceiveIntoStock adds accepted goods to a store's on-hand quantity.
	ReceiveIntoStock(ctx context.Context, tx platform.Tx, m StockMovement) error
	// RemoveFromStock takes goods out, and must fail rather than let on-hand
	// stock go negative.
	//
	// A blank BatchNo means "wherever it is": goods going back to a vendor were
	// bought across however many consignments the shop happened to receive, and
	// a purchase return line names no batch. The implementation walks batches
	// first-expiring-first, as a sale does. Both methods take a Tx rather than a
	// Querier because the movement must land in the same transaction as the
	// document that caused it, and a FEFO walk needs row locks to be correct.
	RemoveFromStock(ctx context.Context, tx platform.Tx, m StockMovement) error
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
	InTxRetry(ctx context.Context, opts *sql.TxOptions, attempts int, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies purchasing rules.
type Service struct {
	db    DB
	repo  Repository
	stock StockWriter
}

// New returns a purchasing service.
func New(db DB, repo Repository, stock StockWriter) *Service {
	return &Service{db: db, repo: repo, stock: stock}
}

// ---------------------------------------------------------------------------
// Vendors
// ---------------------------------------------------------------------------

// CreateVendor validates and stores a new vendor.
func (s *Service) CreateVendor(ctx context.Context, in domain.VendorInput) (domain.Vendor, error) {
	v, err := domain.NewVendor(in)
	if err != nil {
		return domain.Vendor{}, err
	}
	if err := s.repo.CreateVendor(ctx, s.db, v); err != nil {
		return domain.Vendor{}, fmt.Errorf("create vendor: %w", err)
	}
	return v, nil
}

// UpdateVendor replaces a vendor's editable fields.
func (s *Service) UpdateVendor(ctx context.Context, id uuid.UUID, in domain.VendorInput) (domain.Vendor, error) {
	v, err := domain.NewVendor(in)
	if err != nil {
		return domain.Vendor{}, err
	}
	v.ID = id

	found, err := s.repo.UpdateVendor(ctx, s.db, v)
	if err != nil {
		return domain.Vendor{}, fmt.Errorf("update vendor: %w", err)
	}
	if !found {
		return domain.Vendor{}, platform.ErrNotFound
	}
	return v, nil
}

// SetVendorActive deactivates or reactivates a vendor. There is no delete: a
// vendor with purchase history is referenced by orders and payments.
func (s *Service) SetVendorActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetVendorActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set vendor active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// GetVendor returns one vendor together with what they are currently owed.
func (s *Service) GetVendor(ctx context.Context, id uuid.UUID) (domain.Vendor, domain.Exposure, error) {
	v, err := s.repo.GetVendor(ctx, s.db, id)
	if err != nil {
		return domain.Vendor{}, domain.Exposure{}, err
	}
	e, err := s.repo.VendorExposure(ctx, s.db, id)
	if err != nil {
		return domain.Vendor{}, domain.Exposure{}, fmt.Errorf("vendor exposure: %w", err)
	}
	return v, e, nil
}

// ListVendors returns vendors matching the filter.
func (s *Service) ListVendors(ctx context.Context, f domain.VendorFilter) ([]domain.Vendor, error) {
	return s.repo.ListVendors(ctx, s.db, f)
}

// VendorExposure returns what a vendor is owed.
func (s *Service) VendorExposure(ctx context.Context, vendorID uuid.UUID) (domain.Exposure, error) {
	return s.repo.VendorExposure(ctx, s.db, vendorID)
}

// ---------------------------------------------------------------------------
// Purchase orders
// ---------------------------------------------------------------------------

// CreatePurchaseOrderInput is a purchase order as a caller states it.
//
// There is no field here for a total, a tax amount or a GST rate. Everything
// financial is derived: the rate and quantity come from the caller, the GST rate
// comes from the product, and the server computes the rest.
type CreatePurchaseOrderInput struct {
	StoreID  uuid.UUID
	VendorID uuid.UUID
	// TerminalID identifies the counter whose numbering series this order draws
	// from. document_sequences is keyed by terminal, so an order cannot be
	// numbered without one.
	TerminalID   uuid.UUID
	OrderDate    time.Time
	ExpectedDate *time.Time
	Lines        []domain.LineInput
}

// CreatePurchaseOrder computes a purchase order server-side, checks it against
// the vendor's credit limit, allocates its number and writes it — all in one
// transaction.
//
// It runs at SERIALIZABLE with a retry, because it reads the vendor's exposure
// and writes a decision based on it. At any weaker level two orders placed at
// the same instant each see the exposure before the other and both pass a limit
// that the pair of them breaches.
func (s *Service) CreatePurchaseOrder(ctx context.Context, in CreatePurchaseOrderInput) (domain.PurchaseOrder, []domain.PurchaseItem, error) {
	if in.StoreID == uuid.Nil {
		return domain.PurchaseOrder{}, nil, ErrStoreRequired
	}
	if in.VendorID == uuid.Nil {
		return domain.PurchaseOrder{}, nil, fmt.Errorf("%w: vendor is required", domain.ErrValidation)
	}
	if in.TerminalID == uuid.Nil {
		return domain.PurchaseOrder{}, nil, fmt.Errorf("%w: terminal is required to number a purchase order", domain.ErrValidation)
	}
	if in.OrderDate.IsZero() {
		return domain.PurchaseOrder{}, nil, fmt.Errorf("%w: order date is required", domain.ErrValidation)
	}
	if len(in.Lines) == 0 {
		return domain.PurchaseOrder{}, nil, fmt.Errorf("%w: a purchase order needs at least one line", domain.ErrValidation)
	}

	var po domain.PurchaseOrder
	var items []domain.PurchaseItem

	err := s.db.InTxRetry(ctx, platform.Serializable, platform.DefaultTxAttempts,
		func(ctx context.Context, tx platform.Tx) error {
			vendor, err := s.repo.GetVendor(ctx, tx, in.VendorID)
			if err != nil {
				return fmt.Errorf("vendor %s: %w", in.VendorID, err)
			}
			if !vendor.IsActive {
				return fmt.Errorf("%w: vendor %q is not active", domain.ErrValidation, vendor.Name)
			}
			storeGSTIN, err := s.repo.StoreGSTIN(ctx, tx, in.StoreID)
			if err != nil {
				return fmt.Errorf("store %s: %w", in.StoreID, err)
			}
			interState := domain.IsInterState(storeGSTIN, vendor.GSTIN)

			// One lookup for every product on the order, rather than one query
			// per line inside the loop. The rates and the units are the
			// product's own; a client-supplied tax figure is never consulted.
			products, err := s.repo.ProductsForPurchase(ctx, tx, in.StoreID, lineProductIDs(in.Lines))
			if err != nil {
				return fmt.Errorf("products for purchase: %w", err)
			}

			poID := uuid.New()
			computed, totals, err := computeOrder(poID, in.Lines, products, interState)
			if err != nil {
				return err
			}

			exposure, err := s.repo.VendorExposure(ctx, tx, in.VendorID)
			if err != nil {
				return fmt.Errorf("vendor exposure: %w", err)
			}
			if !exposure.WithinLimit(totals.Total, vendor.CreditLimit) {
				return fmt.Errorf("%w: vendor %q owes %s, this order is %s, and the limit is %s",
					domain.ErrCreditLimitExceeded, vendor.Name,
					exposure.Outstanding(), totals.Total, vendor.CreditLimit)
			}

			terminalCode, err := s.repo.TerminalCode(ctx, tx, in.StoreID, in.TerminalID)
			if err != nil {
				return fmt.Errorf("terminal %s: %w", in.TerminalID, err)
			}
			finYear := domain.FinancialYear(in.OrderDate)
			seq, err := s.repo.NextDocumentNumber(ctx, tx, in.StoreID, in.TerminalID, docTypePurchaseOrder, finYear)
			if err != nil {
				return fmt.Errorf("allocate purchase order number: %w", err)
			}

			po = domain.PurchaseOrder{
				ID:           poID,
				PONo:         domain.FormatPONumber(terminalCode, finYear, seq),
				StoreID:      in.StoreID,
				VendorID:     in.VendorID,
				VendorName:   vendor.Name,
				OrderDate:    in.OrderDate,
				ExpectedDate: in.ExpectedDate,
				SubTotal:     totals.SubTotal,
				CGST:         totals.CGST,
				SGST:         totals.SGST,
				IGST:         totals.IGST,
				Discount:     totals.Discount,
				Total:        totals.Total,
				Status:       domain.StatusPending,
			}
			if err := s.repo.CreatePurchaseOrder(ctx, tx, po); err != nil {
				return fmt.Errorf("insert purchase order: %w", err)
			}
			for _, it := range computed {
				if err := s.repo.CreatePurchaseItem(ctx, tx, it); err != nil {
					return fmt.Errorf("insert purchase item: %w", err)
				}
			}
			items = computed
			return nil
		})
	if err != nil {
		return domain.PurchaseOrder{}, nil, err
	}
	return po, items, nil
}

// orderTotals is the header arithmetic accumulated from the lines.
type orderTotals struct {
	SubTotal money.Money
	CGST     money.Money
	SGST     money.Money
	IGST     money.Money
	Discount money.Money
	Total    money.Money
}

// computeOrder turns caller-supplied lines into purchase order items and the
// header totals.
//
// The client's line total, when it sends one, is compared for exact equality.
// The implementation this replaces allowed a 5-paise drift because both sides
// were float64 and could not be expected to agree; here both sides are exact
// decimals computed from the same rate, quantity, discount and statutory rate,
// so any difference at all is a genuine disagreement about what is being
// ordered and is worth stopping for.
func computeOrder(poID uuid.UUID, lines []domain.LineInput, products map[uuid.UUID]repo.PurchaseProduct, interState bool) ([]domain.PurchaseItem, orderTotals, error) {
	items := make([]domain.PurchaseItem, 0, len(lines))
	var totals orderTotals

	for i, line := range lines {
		if line.ProductID == uuid.Nil {
			return nil, orderTotals{}, fmt.Errorf("%w: line %d has no product", domain.ErrValidation, i+1)
		}
		p, ok := products[line.ProductID]
		if !ok {
			return nil, orderTotals{}, fmt.Errorf("%w: line %d names product %s, which is unknown or inactive",
				domain.ErrValidation, i+1, line.ProductID)
		}
		unit, _, err := buyingUnit(p, line.ProductUnitID)
		if err != nil {
			return nil, orderTotals{}, fmt.Errorf("line %d: %w", i+1, err)
		}

		rate := domain.NormaliseRate(line.Rate)
		amounts, err := domain.ComputeLine(rate, line.UnitQty, line.Discount, p.GSTRate, interState)
		if err != nil {
			return nil, orderTotals{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		if !line.ClaimedTotal.IsZero() && !line.ClaimedTotal.Equal(amounts.Total) {
			return nil, orderTotals{}, fmt.Errorf(
				"%w: line %d claims a total of %s but %s x %s %s less %s at %s%% GST comes to %s",
				domain.ErrValidation, i+1, line.ClaimedTotal, rate, line.UnitQty, unit.Label,
				line.Discount, p.GSTRate, amounts.Total)
		}

		items = append(items, domain.PurchaseItem{
			ID:            uuid.New(),
			POID:          poID,
			ProductID:     line.ProductID,
			ProductName:   p.Name,
			ProductUnitID: unit.ID,
			UnitQty:       line.UnitQty,
			UnitLabel:     unit.Label,
			// What the order is worth is counted in sacks; what the shelf will
			// receive is counted in kilos.
			Quantity:    unit.BaseQuantity(line.UnitQty),
			UOMID:       line.UOMID,
			ReceivedQty: money.ZeroQuantity(),
			Rate:        rate,
			Discount:    line.Discount,
			CGST:        amounts.Tax.CGST,
			SGST:        amounts.Tax.SGST,
			IGST:        amounts.Tax.IGST,
			Total:       amounts.Total,
		})

		totals.SubTotal = totals.SubTotal.Add(amounts.Taxable)
		totals.CGST = totals.CGST.Add(amounts.Tax.CGST)
		totals.SGST = totals.SGST.Add(amounts.Tax.SGST)
		totals.IGST = totals.IGST.Add(amounts.Tax.IGST)
		totals.Discount = totals.Discount.Add(line.Discount)
		totals.Total = totals.Total.Add(amounts.Total)
	}
	return items, totals, nil
}

// buyingUnit picks the unit a purchase line is placed in, and returns the
// store's recorded cost for one of them.
//
// A zero unit id means the product's default, which is what a bare quantity has
// always meant. Anything else must be a unit of this product: a line naming
// another product's sack has to fail rather than quietly order a different
// size, since the two ids are indistinguishable to a caller that mixed them up.
func buyingUnit(p repo.PurchaseProduct, unitID uuid.UUID) (domain.BuyingUnit, money.Money, error) {
	if len(p.Units) == 0 {
		return domain.BuyingUnit{}, money.Zero(), fmt.Errorf(
			"%w: %s has no unit set up, so there is nothing to order it by", domain.ErrValidation, p.Name)
	}

	var chosen repo.PurchaseUnit
	if unitID == uuid.Nil {
		// The default, falling back to the first unit still in use rather than
		// failing: a product with no default is a data fault, and refusing to
		// buy stock is a poor way to report it.
		for _, u := range p.Units {
			if !u.IsActive {
				continue
			}
			if u.IsDefault {
				chosen = u
				break
			}
			if chosen.ID == uuid.Nil {
				chosen = u
			}
		}
		if chosen.ID == uuid.Nil {
			return domain.BuyingUnit{}, money.Zero(), fmt.Errorf(
				"%w: %s has no unit still in use", domain.ErrValidation, p.Name)
		}
	} else {
		found := false
		for _, u := range p.Units {
			if u.ID == unitID {
				chosen, found = u, true
				break
			}
		}
		if !found {
			return domain.BuyingUnit{}, money.Zero(), fmt.Errorf(
				"%w: unit %s is not one of the ways %s is transacted", domain.ErrValidation, unitID, p.Name)
		}
		if !chosen.IsActive {
			return domain.BuyingUnit{}, money.Zero(), fmt.Errorf(
				"%w: the %s of %s is no longer in use", domain.ErrValidation, chosen.Label, p.Name)
		}
	}

	// A unit may be sold but not bought — a 250 g pouch the shop weighs out
	// itself and no supplier delivers.
	if !chosen.IsPurchasable {
		return domain.BuyingUnit{}, money.Zero(), fmt.Errorf(
			"%w: %s is not bought by the %s", domain.ErrValidation, p.Name, chosen.Label)
	}
	// There is deliberately no HasPrice guard here, unlike the sell side. A
	// purchase states its own rate — the vendor's invoice is the authority —
	// and the catalogue's cost is only a fallback for a receipt that omits one.
	return domain.BuyingUnit{ID: chosen.ID, Label: chosen.Label, QtyInBase: chosen.QtyInBase},
		chosen.CostPrice, nil
}

func lineProductIDs(lines []domain.LineInput) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(lines))
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if _, ok := seen[l.ProductID]; ok {
			continue
		}
		seen[l.ProductID] = struct{}{}
		ids = append(ids, l.ProductID)
	}
	return ids
}

// ListPurchaseOrders returns a store's purchase orders.
func (s *Service) ListPurchaseOrders(ctx context.Context, f domain.POFilter) ([]domain.PurchaseOrder, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, fmt.Errorf("%w: %q is not a purchase order status", domain.ErrValidation, f.Status)
	}
	return s.repo.ListPurchaseOrders(ctx, s.db, f)
}

// GetPurchaseOrder returns one order with its lines.
func (s *Service) GetPurchaseOrder(ctx context.Context, id, storeID uuid.UUID) (domain.PurchaseOrder, []domain.PurchaseItem, error) {
	if storeID == uuid.Nil {
		return domain.PurchaseOrder{}, nil, ErrStoreRequired
	}
	po, err := s.repo.GetPurchaseOrder(ctx, s.db, id, storeID)
	if err != nil {
		return domain.PurchaseOrder{}, nil, err
	}
	items, err := s.repo.ListPurchaseItems(ctx, s.db, id, false)
	if err != nil {
		return domain.PurchaseOrder{}, nil, fmt.Errorf("purchase order lines: %w", err)
	}
	return po, items, nil
}

// CancelPurchaseOrder cancels an order that nothing has been received against.
//
// The precondition lives in the UPDATE, so a cancel racing a goods receipt is
// decided by the database rather than by whichever request read the status
// first.
func (s *Service) CancelPurchaseOrder(ctx context.Context, id, storeID uuid.UUID) error {
	if storeID == uuid.Nil {
		return ErrStoreRequired
	}
	cancelled, err := s.repo.SetPurchaseOrderStatus(ctx, s.db, id, storeID,
		domain.StatusCancelled, []domain.POStatus{domain.StatusPending})
	if err != nil {
		return fmt.Errorf("cancel purchase order: %w", err)
	}
	if cancelled {
		return nil
	}
	// Nothing changed. Read the order back so the caller learns which of the
	// two reasons applies rather than a bare "no".
	po, err := s.repo.GetPurchaseOrder(ctx, s.db, id, storeID)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: purchase order %s is %s, and only a pending order can be cancelled",
		domain.ErrInvalidTransition, po.PONo, po.Status)
}

// ---------------------------------------------------------------------------
// Goods receipts
// ---------------------------------------------------------------------------

// ReceiveGoodsInput is a delivery arriving against a purchase order.
type ReceiveGoodsInput struct {
	POID         uuid.UUID
	StoreID      uuid.UUID
	UserID       uuid.UUID
	ReceivedDate time.Time
	Notes        string
	Lines        []domain.ReceiptLineInput
}

// ReceiveGoods records a goods receipt, books the accepted quantities into
// stock, advances each purchase order line's received quantity and moves the
// order's status — atomically.
//
// This is one operation, not two. The code it replaces had two: PurchaseHandler
// .Receive and GRNHandler.Create, which did overlapping halves of this job with
// different defects. Receive booked stock and never wrote a receipt document;
// GRN wrote the document and booked rejected goods into stock as though they
// had been accepted. Both read the order's status outside their own transaction
// and wrote the new status with no guard.
func (s *Service) ReceiveGoods(ctx context.Context, in ReceiveGoodsInput) (domain.GoodsReceipt, []domain.GoodsReceiptItem, error) {
	if in.StoreID == uuid.Nil {
		return domain.GoodsReceipt{}, nil, ErrStoreRequired
	}
	if in.POID == uuid.Nil {
		return domain.GoodsReceipt{}, nil, fmt.Errorf("%w: purchase order is required", domain.ErrValidation)
	}
	if in.UserID == uuid.Nil {
		return domain.GoodsReceipt{}, nil, fmt.Errorf("%w: user is required", domain.ErrValidation)
	}
	if in.ReceivedDate.IsZero() {
		return domain.GoodsReceipt{}, nil, fmt.Errorf("%w: received date is required", domain.ErrValidation)
	}
	if len(in.Lines) == 0 {
		return domain.GoodsReceipt{}, nil, fmt.Errorf("%w: a goods receipt needs at least one line", domain.ErrValidation)
	}

	var receipt domain.GoodsReceipt
	var receiptItems []domain.GoodsReceiptItem

	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		// Lock the order and read its status inside this transaction. The old
		// code read the status through the pool before opening its transaction,
		// so an order could be cancelled in the gap and still be received.
		status, poNo, err := s.repo.LockPurchaseOrder(ctx, tx, in.POID, in.StoreID)
		if err != nil {
			return fmt.Errorf("purchase order %s: %w", in.POID, err)
		}
		if !status.Open() {
			return fmt.Errorf("%w: purchase order is %s, so nothing can be received against it",
				domain.ErrInvalidTransition, status)
		}

		// Drained, and locked FOR UPDATE. Everything below reads from this
		// slice; no cursor is open while the writes run.
		items, err := s.repo.ListPurchaseItems(ctx, tx, in.POID, true)
		if err != nil {
			return fmt.Errorf("purchase order lines: %w", err)
		}
		if len(items) == 0 {
			return fmt.Errorf("%w: purchase order has no lines to receive against", domain.ErrValidation)
		}

		receivedBy, err := s.repo.UserName(ctx, tx, in.UserID)
		if err != nil {
			return fmt.Errorf("user %s: %w", in.UserID, err)
		}
		products, err := s.repo.ProductsForPurchase(ctx, tx, in.StoreID, receiptProductIDs(in.Lines))
		if err != nil {
			return fmt.Errorf("products for purchase: %w", err)
		}

		grID := uuid.New()
		// working tracks the received quantity as this receipt allocates it, so
		// two lines naming the same product cannot each be allowed the same
		// outstanding quantity.
		working := append([]domain.PurchaseItem(nil), items...)

		type plan struct {
			item        domain.GoodsReceiptItem
			allocations []domain.ReceiptAllocation
			batchNo     string
			// landedCost is what the accepted goods on this line cost in full,
			// not a rate. See the comment where it is computed.
			landedCost money.Money
		}
		plans := make([]plan, 0, len(in.Lines))

		for i, line := range in.Lines {
			p, ok := products[line.ProductID]
			if !ok {
				return fmt.Errorf("%w: receipt line %d names product %s, which is unknown or inactive",
					domain.ErrValidation, i+1, line.ProductID)
			}
			// A receipt line that names no unit is counted in the unit its
			// purchase order line was placed in: a clerk confirming ten sacks
			// against the paperwork in their hand means ten sacks.
			unitID := line.ProductUnitID
			if unitID == uuid.Nil {
				unitID = orderedUnit(items, line.ProductID)
			}
			unit, recordedCost, err := buyingUnit(p, unitID)
			if err != nil {
				return fmt.Errorf("receipt line %d: %w", i+1, err)
			}

			item, err := domain.NewGoodsReceiptItem(grID, orderedFor(items, line.ProductID), unit, line)
			if err != nil {
				return fmt.Errorf("receipt line %d: %w", i+1, err)
			}

			// Allocation is in base units, and so is everything the receipt
			// stores, so an order placed in sacks can be filled by a delivery
			// counted loose.
			//
			// It allocates what was RECEIVED, deliberately, while stock takes
			// only what was accepted. A delivery that arrived and was refused is
			// a delivery: the order is closed against it and the remedy is a
			// debit note or a purchase return, not a line left open waiting for
			// a replacement nobody promised. What was turned away is not lost —
			// the receipt records rejected_qty against the line.
			//
			// Changing this to AcceptedQty would leave every partially rejected
			// order permanently short, and TestRejectedGoodsNeverReachStock
			// pins the intended behaviour.
			allocations, err := domain.AllocateReceipt(working, line.ProductID, item.ReceivedQty)
			if err != nil {
				return fmt.Errorf("receipt line %d: %w", i+1, err)
			}
			applyAllocations(working, allocations)

			// The landed cost of one buying unit: what the receipt says a sack
			// cost, else the store's recorded cost for that same unit, else what
			// the order agreed to pay for it.
			//
			// The order's rate is the last resort rather than no cost at all. A
			// product whose unit carries no recorded cost — an ordinary state
			// for a pack somebody added without pricing it — was landing stock
			// at zero, which is not a cheap batch but a missing one: COGS
			// vanishes, gross profit reads as the whole sale price, and nothing
			// anywhere says so. The rate on the purchase order is what the shop
			// is actually paying, so it is the best figure available and it is
			// always present.
			cost := line.CostPrice
			if !cost.IsPositive() {
				cost = recordedCost
			}
			if !cost.IsPositive() {
				cost = orderedRate(items, line.ProductID)
			}
			// What the accepted goods cost, in full: the price of one pack
			// times the packs accepted. A multiplication, so it is exact.
			//
			// This used to divide by the pack size to get a cost per base unit,
			// on the reasoning that a recurring decimal there costs a fraction
			// of a paisa. Measured, it cost more than that and always in the
			// same direction: a 30 kg sack at Rs.940 is Rs.31.3333... a kilo,
			// stored as Rs.31.33, so ten paise went missing between buying the
			// sack and selling it. The batch carries value now, and nothing on
			// the way in divides.
			landed := cost.Mul(domain.AcceptedUnits(line))

			batch := line.BatchNo
			if batch == "" {
				batch = "GRN-" + grID.String()[:8]
			}
			plans = append(plans, plan{
				item:        item,
				allocations: allocations,
				batchNo:     batch,
				landedCost:  landed,
			})
		}

		receipt = domain.GoodsReceipt{
			ID:           grID,
			POID:         in.POID,
			PONo:         poNo,
			ReceivedDate: in.ReceivedDate,
			ReceivedBy:   receivedBy,
			Notes:        in.Notes,
			// The lines about to be written. Filling it here means the receipt
			// returned from this call carries the same count the listing will
			// report for it, rather than a zero that reads as "no lines".
			ItemCount: len(plans),
		}
		if err := s.repo.CreateGoodsReceipt(ctx, tx, receipt); err != nil {
			return fmt.Errorf("insert goods receipt: %w", err)
		}

		// The delivery is booked in stockdomain.InLockOrder, not in the order
		// the clerk keyed the lines. A storekeeper checking in a van-load while
		// the counter bills the same goods is the ordinary case here, and the
		// two transactions have to agree on which product's rows to take first
		// or they deadlock. The receipt's own lines are still returned in the
		// order they were stated.
		productIDs := make([]uuid.UUID, len(plans))
		for i, p := range plans {
			productIDs[i] = p.item.ProductID
		}
		written := make([]domain.GoodsReceiptItem, len(plans))
		for _, idx := range stockdomain.InLockOrder(productIDs) {
			p := plans[idx]
			if err := s.repo.CreateGoodsReceiptItem(ctx, tx, p.item); err != nil {
				return fmt.Errorf("insert goods receipt item: %w", err)
			}
			for _, a := range p.allocations {
				booked, err := s.repo.AddReceivedQty(ctx, tx, a.ItemID, a.Qty)
				if err != nil {
					return fmt.Errorf("advance received quantity: %w", err)
				}
				if !booked {
					// The guard in the UPDATE refused it. Another receipt
					// committed between our read and this write.
					return fmt.Errorf("%w: purchase order line %s cannot take a further %s",
						domain.ErrOverReceipt, a.ItemID, a.Qty)
				}
			}
			// Only what was accepted goes into stock. A delivery of 100 with
			// 100 rejected adds nothing — the whole of defect 2.
			if p.item.AcceptedQty.IsPositive() {
				if err := s.stock.ReceiveIntoStock(ctx, tx, StockMovement{
					StoreID:   in.StoreID,
					ProductID: p.item.ProductID,
					BatchNo:   p.batchNo,
					Quantity:  p.item.AcceptedQty,
					TotalCost: p.landedCost,
				}); err != nil {
					return fmt.Errorf("receive product %s into stock: %w", p.item.ProductID, err)
				}
			}
			written[idx] = p.item
		}

		// Re-read the lines so the new status is decided on what is actually
		// stored, not on what we believe we wrote.
		fresh, err := s.repo.ListPurchaseItems(ctx, tx, in.POID, false)
		if err != nil {
			return fmt.Errorf("re-read purchase order lines: %w", err)
		}
		next := domain.NextStatus(fresh)
		moved, err := s.repo.SetPurchaseOrderStatus(ctx, tx, in.POID, in.StoreID, next,
			[]domain.POStatus{domain.StatusPending, domain.StatusPartial})
		if err != nil {
			return fmt.Errorf("advance purchase order status: %w", err)
		}
		if !moved {
			return fmt.Errorf("%w: purchase order left the receivable states while it was being received",
				domain.ErrInvalidTransition)
		}

		receiptItems = written
		return nil
	})
	if err != nil {
		return domain.GoodsReceipt{}, nil, err
	}
	return receipt, receiptItems, nil
}

// applyAllocations advances the in-memory received quantities so a later line
// of the same receipt sees what an earlier one already took.
func applyAllocations(items []domain.PurchaseItem, allocations []domain.ReceiptAllocation) {
	for _, a := range allocations {
		for i := range items {
			if items[i].ID == a.ItemID {
				items[i].ReceivedQty = items[i].ReceivedQty.Add(a.Qty)
				break
			}
		}
	}
}

// orderedFor sums what the order asked for of one product, for the receipt's
// ordered_qty column.
func orderedFor(items []domain.PurchaseItem, productID uuid.UUID) money.Quantity {
	total := money.ZeroQuantity()
	for _, it := range items {
		if it.ProductID == productID {
			total = total.Add(it.Quantity)
		}
	}
	return total
}

// orderedRate returns what the order agreed to pay for one buying unit of a
// product, used as the last-resort cost basis when a receipt states none and
// the catalogue has none recorded.
//
// It is the rate on the purchase order, so it is per buying unit exactly like
// the other two candidates, and the caller divides it by the unit size the same
// way. Zero comes back only when the product is not on the order at all, which
// the caller has already refused.
func orderedRate(items []domain.PurchaseItem, productID uuid.UUID) money.Money {
	for _, it := range items {
		if it.ProductID == productID {
			return it.Rate
		}
	}
	return money.Zero()
}

// orderedUnit returns the unit the order was placed in for one product, which
// is what a receipt line counts in unless it says otherwise.
//
// It takes the first line naming the product. A product legitimately appears on
// two lines of one order, and if those were placed in different units the
// receipt has to name which it means; taking the first is the same rule
// AllocateReceipt already fills lines by. The nil uuid comes back for a line
// predating the units migration, and buyingUnit reads that as the default.
func orderedUnit(items []domain.PurchaseItem, productID uuid.UUID) uuid.UUID {
	for _, it := range items {
		if it.ProductID == productID {
			return it.ProductUnitID
		}
	}
	return uuid.Nil
}

func receiptProductIDs(lines []domain.ReceiptLineInput) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(lines))
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if _, ok := seen[l.ProductID]; ok {
			continue
		}
		seen[l.ProductID] = struct{}{}
		ids = append(ids, l.ProductID)
	}
	return ids
}

// GetGoodsReceipt returns one receipt with its lines.
func (s *Service) GetGoodsReceipt(ctx context.Context, id, storeID uuid.UUID) (domain.GoodsReceipt, []domain.GoodsReceiptItem, error) {
	if storeID == uuid.Nil {
		return domain.GoodsReceipt{}, nil, ErrStoreRequired
	}
	g, err := s.repo.GetGoodsReceipt(ctx, s.db, id, storeID)
	if err != nil {
		return domain.GoodsReceipt{}, nil, err
	}
	items, err := s.repo.ListGoodsReceiptItems(ctx, s.db, id)
	if err != nil {
		return domain.GoodsReceipt{}, nil, fmt.Errorf("goods receipt lines: %w", err)
	}
	return g, items, nil
}

// ListGoodsReceipts returns a store's goods receipts.
func (s *Service) ListGoodsReceipts(ctx context.Context, storeID uuid.UUID, poID *uuid.UUID, limit int) ([]domain.GoodsReceipt, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListGoodsReceipts(ctx, s.db, storeID, poID, limit)
}

// ---------------------------------------------------------------------------
// Purchase returns
// ---------------------------------------------------------------------------

// CreatePurchaseReturnInput is goods going back to a vendor, as a caller states
// it. As with a purchase order, the total is computed rather than accepted.
type CreatePurchaseReturnInput struct {
	POID       *uuid.UUID
	VendorID   uuid.UUID
	StoreID    uuid.UUID
	UserID     uuid.UUID
	ReturnDate time.Time
	Reason     string
	Lines      []domain.ReturnLineInput
}

// CreatePurchaseReturn raises a pending purchase return. No stock moves here:
// stock moves on approval, once.
func (s *Service) CreatePurchaseReturn(ctx context.Context, in CreatePurchaseReturnInput) (domain.PurchaseReturn, []domain.PurchaseReturnItem, error) {
	if in.StoreID == uuid.Nil {
		return domain.PurchaseReturn{}, nil, ErrStoreRequired
	}
	if in.VendorID == uuid.Nil {
		return domain.PurchaseReturn{}, nil, fmt.Errorf("%w: vendor is required", domain.ErrValidation)
	}
	if in.UserID == uuid.Nil {
		return domain.PurchaseReturn{}, nil, fmt.Errorf("%w: user is required", domain.ErrValidation)
	}
	if in.ReturnDate.IsZero() {
		return domain.PurchaseReturn{}, nil, fmt.Errorf("%w: return date is required", domain.ErrValidation)
	}
	if len(in.Lines) == 0 {
		return domain.PurchaseReturn{}, nil, fmt.Errorf("%w: a purchase return needs at least one line", domain.ErrValidation)
	}

	returnID := uuid.New()
	items := make([]domain.PurchaseReturnItem, 0, len(in.Lines))
	total := money.Zero()
	for i, line := range in.Lines {
		it, err := domain.NewPurchaseReturnItem(returnID, line)
		if err != nil {
			return domain.PurchaseReturn{}, nil, fmt.Errorf("return line %d: %w", i+1, err)
		}
		items = append(items, it)
		total = total.Add(it.Total)
	}

	pr := domain.PurchaseReturn{
		ID:         returnID,
		POID:       in.POID,
		VendorID:   in.VendorID,
		StoreID:    in.StoreID,
		ReturnDate: in.ReturnDate,
		Total:      total,
		Status:     domain.ReturnPending,
		Reason:     in.Reason,
		UserID:     in.UserID,
	}

	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		if err := s.repo.CreatePurchaseReturn(ctx, tx, pr); err != nil {
			return fmt.Errorf("insert purchase return: %w", err)
		}
		for _, it := range items {
			if err := s.repo.CreatePurchaseReturnItem(ctx, tx, it); err != nil {
				return fmt.Errorf("insert purchase return item: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.PurchaseReturn{}, nil, err
	}
	return pr, items, nil
}

// ApprovePurchaseReturn approves a pending return and takes the goods out of
// stock, exactly once.
//
// Two things make "exactly once" true rather than likely. The status change is
// a guarded UPDATE whose RowsAffected decides whether this call owns the
// approval, so a second concurrent approval changes no row and moves no stock.
// And the return's lines are drained into a slice before any stock is touched:
// the code this replaces iterated an open cursor and issued the stock UPDATE
// inside the loop, which pgx rejects outright with "conn busy" and which, even
// where it ran, held a cursor across writes to the rows it was reading.
func (s *Service) ApprovePurchaseReturn(ctx context.Context, id, storeID uuid.UUID) (domain.PurchaseReturn, error) {
	if storeID == uuid.Nil {
		return domain.PurchaseReturn{}, ErrStoreRequired
	}

	var approved domain.PurchaseReturn
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		ok, err := s.repo.ApprovePurchaseReturn(ctx, tx, id, storeID)
		if err != nil {
			return fmt.Errorf("approve purchase return: %w", err)
		}
		if !ok {
			pr, err := s.repo.GetPurchaseReturn(ctx, tx, id, storeID)
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: purchase return is %s, and only a pending return can be approved",
				domain.ErrInvalidTransition, pr.Status)
		}

		// Drained before a single write.
		items, err := s.repo.ListPurchaseReturnItems(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("purchase return lines: %w", err)
		}
		if len(items) == 0 {
			return fmt.Errorf("%w: purchase return has no lines", domain.ErrValidation)
		}

		// In the same agreed order every other multi-product movement uses; see
		// stockdomain.InLockOrder.
		productIDs := make([]uuid.UUID, len(items))
		for i, it := range items {
			productIDs[i] = it.ProductID
		}
		for _, i := range stockdomain.InLockOrder(productIDs) {
			it := items[i]
			if err := s.stock.RemoveFromStock(ctx, tx, StockMovement{
				StoreID:   storeID,
				ProductID: it.ProductID,
				Quantity:  it.Quantity,
			}); err != nil {
				return fmt.Errorf("remove product %s from stock: %w", it.ProductID, err)
			}
		}

		approved, err = s.repo.GetPurchaseReturn(ctx, tx, id, storeID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.PurchaseReturn{}, err
	}
	return approved, nil
}

// GetPurchaseReturn returns one return with its lines.
func (s *Service) GetPurchaseReturn(ctx context.Context, id, storeID uuid.UUID) (domain.PurchaseReturn, []domain.PurchaseReturnItem, error) {
	if storeID == uuid.Nil {
		return domain.PurchaseReturn{}, nil, ErrStoreRequired
	}
	pr, err := s.repo.GetPurchaseReturn(ctx, s.db, id, storeID)
	if err != nil {
		return domain.PurchaseReturn{}, nil, err
	}
	items, err := s.repo.ListPurchaseReturnItems(ctx, s.db, id)
	if err != nil {
		return domain.PurchaseReturn{}, nil, fmt.Errorf("purchase return lines: %w", err)
	}
	return pr, items, nil
}

// ListPurchaseReturns returns a store's purchase returns.
func (s *Service) ListPurchaseReturns(ctx context.Context, f domain.ReturnFilter) ([]domain.PurchaseReturn, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListPurchaseReturns(ctx, s.db, f)
}

// ---------------------------------------------------------------------------
// Vendor payments
// ---------------------------------------------------------------------------

// RecordVendorPayment records money paid to a vendor, which is what reduces the
// vendor's exposure against their credit limit.
func (s *Service) RecordVendorPayment(ctx context.Context, in domain.PaymentInput) (domain.VendorPayment, error) {
	p, err := domain.NewVendorPayment(in)
	if err != nil {
		return domain.VendorPayment{}, err
	}
	if err := s.repo.CreateVendorPayment(ctx, s.db, p); err != nil {
		return domain.VendorPayment{}, fmt.Errorf("record vendor payment: %w", err)
	}
	return p, nil
}

// ListVendorPayments returns a store's vendor payments.
func (s *Service) ListVendorPayments(ctx context.Context, storeID uuid.UUID, vendorID *uuid.UUID, limit int) ([]domain.VendorPayment, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListVendorPayments(ctx, s.db, storeID, vendorID, limit)
}
