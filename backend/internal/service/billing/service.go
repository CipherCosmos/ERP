// Package billing is the service layer for sales.
//
// Creating a sale is the one place in this system where stock, tax, customer
// credit and a statutory document number all have to move together or not at
// all. It therefore happens in a single transaction, and every figure on the
// invoice is computed here rather than accepted from the caller.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domain "gbos/internal/domain/billing"
	"gbos/internal/domain/document"
	"gbos/internal/domain/money"
	"gbos/internal/domain/reconcile"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"

	"github.com/google/uuid"
)

var (
	// ErrAlreadyVoided is returned when an invoice has already been voided.
	// It is a distinct error because the caller usually wants to treat a
	// double-click as harmless rather than as a failure.
	ErrAlreadyVoided = errors.New("invoice is already voided")

	// ErrPriceChanged is returned when the caller's cached price does not
	// match the price the server holds.
	ErrPriceChanged = errors.New("the price has changed since the item was added")

	// ErrTerminalRequired is returned when a sale is raised without a terminal.
	ErrTerminalRequired = errors.New("a terminal is required to number an invoice")
)

// Repository is the persistence this service needs.
type Repository interface {
	NextInvoiceNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, finYear string) (int64, error)
	// ClaimInvoiceNumber records a number the till allocated and moves the
	// terminal's counter past it, so a later server-side allocation cannot
	// reissue it.
	ClaimInvoiceNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, finYear string, seq int64) error
	InsertInvoice(ctx context.Context, q platform.Querier, inv domain.Invoice) error
	InsertItems(ctx context.Context, q platform.Querier, items []domain.Item) error
	GetInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (domain.Invoice, error)
	LockInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (domain.Invoice, error)
	ItemsForInvoice(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) ([]domain.Item, error)
	ListInvoices(ctx context.Context, q platform.Querier, f repo.InvoiceFilter) ([]domain.Invoice, error)
	VoidInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (bool, error)

	GSTR1Rows(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) ([]repo.GSTR1Row, error)
	MarkGSTR1Filed(ctx context.Context, q platform.Querier, storeID uuid.UUID, month string, from, to time.Time) (int64, error)

	StoreGSTINForBilling(ctx context.Context, q platform.Querier, storeID uuid.UUID) (string, error)
	ProductsForSale(ctx context.Context, q platform.Querier, storeID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]repo.SaleProduct, error)
	GetCustomerForSale(ctx context.Context, q platform.Querier, storeID, customerID uuid.UUID) (repo.CustomerForSale, error)

	FindProcessed(ctx context.Context, q platform.Querier, storeID uuid.UUID, key string) ([]byte, bool, error)
	RecordProcessed(ctx context.Context, q platform.Querier, storeID uuid.UUID, key, path string, status int, response any) error
}

// StockMover is the part of the stock aggregate a sale needs: take goods out
// when they are sold, put them back when the sale is voided.
type StockMover interface {
	DeductFEFO(ctx context.Context, tx platform.Tx, req stockdomain.DeductionRequest) (stockdomain.Deduction, error)
	ReturnToStock(ctx context.Context, q platform.Querier, in stockdomain.Receipt) error
}

// ExceptionRecorder records what the server could not verify about a sale it
// had to accept anyway. See the reconcile package for why a completed sale is
// flagged rather than refused.
type ExceptionRecorder interface {
	Insert(ctx context.Context, q platform.Querier, e reconcile.Exception) error
}

// CreditRecorder is the part of the credit aggregate a sale needs.
type CreditRecorder interface {
	EnsureWithinCreditLimit(ctx context.Context, tx platform.Tx, storeID, customerID uuid.UUID, additional money.Money) error
	// RecordCreditSale writes the debt. alreadyIncurred says the shop has
	// already extended it — an offline sale — so the limit check has nothing
	// left to prevent.
	RecordCreditSale(ctx context.Context, tx platform.Tx, storeID, customerID, invoiceID uuid.UUID, amount money.Money, alreadyIncurred bool) error
	VoidInvoiceCredit(ctx context.Context, tx platform.Tx, invoiceID uuid.UUID) error
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service raises and voids sales.
type Service struct {
	db     DB
	repo   Repository
	stock  StockMover
	credit CreditRecorder
	// exceptions may be nil, and a nil one records nothing. A sale must not
	// fail because reconciliation is not wired up.
	exceptions ExceptionRecorder
	now        func() time.Time
}

// New returns a billing service.
func New(db DB, r Repository, stock StockMover, credit CreditRecorder) *Service {
	return &Service{db: db, repo: r, stock: stock, credit: credit, now: time.Now}
}

// WithExceptions attaches the recorder for what the server cannot verify about a
// sale it accepts anyway. Without it an offline sale still lands; it just lands
// unflagged, which is why wiring is not optional in production.
func (s *Service) WithExceptions(rec ExceptionRecorder) *Service {
	clone := *s
	clone.exceptions = rec
	return &clone
}

// WithClock replaces the clock, for tests that need a fixed financial year.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// CreateInput describes a sale to raise.
type CreateInput struct {
	StoreID    uuid.UUID
	UserID     uuid.UUID
	TerminalID uuid.UUID
	CustomerID *uuid.UUID

	Lines        []domain.LineInput
	BillDiscount money.Money
	PaymentMode  domain.PaymentMode
	// RoundTotal rounds the grand total to the nearest rupee.
	RoundTotal bool
	// DueDate applies to a credit sale.
	DueDate *time.Time

	// IdempotencyKey makes a retried request safe. The same key returns the
	// original invoice rather than raising a second one — which is what stops
	// a double-clicked Checkout button producing two sales, and what lets an
	// offline till replay its queue without fear.
	IdempotencyKey string

	// InvoiceNo is the number the till allocated for this sale.
	//
	// A till that cannot reach the server still has to hand the customer a tax
	// invoice, and a tax invoice carries a number, so the till draws from its own
	// terminal's series. It does so online too: one path, exercised every day,
	// rather than a second one that runs only when the network is down.
	//
	// Blank means the server allocates, which is what an older client sends and
	// what the seed and test paths use.
	//
	// The number is checked against the terminal and financial year it claims,
	// and the unique index on (store_id, invoice_no) refuses a duplicate — so two
	// tills sharing one terminal code fail loudly on sync rather than quietly
	// issuing the same number twice.
	InvoiceNo string

	// BilledOffline records that the till raised this while it could not reach
	// the server, and BilledAt when it did so by the till's own clock. The server
	// cannot know either; guessing from arrival time would be wrong on every slow
	// connection.
	BilledOffline bool
	BilledAt      *time.Time
}

// Result is a raised sale.
type Result struct {
	Invoice domain.Invoice `json:"invoice"`
	Items   []domain.Item  `json:"items"`
	// Replayed reports that this response came from a previous request with
	// the same idempotency key, rather than from a sale raised just now.
	Replayed bool `json:"replayed"`
}

// Create raises a sale.
//
// Everything happens in one transaction: the number is allocated, the stock
// leaves, the receivable is recorded and the invoice is written, or none of it
// does. The figures are computed from the store's own prices and the products'
// own tax rates; the request supplies quantities and discounts, nothing more.
func (s *Service) Create(ctx context.Context, in CreateInput) (Result, error) {
	if in.StoreID == uuid.Nil || in.UserID == uuid.Nil {
		return Result{}, fmt.Errorf("%w: store and user are required", domain.ErrValidation)
	}
	if in.TerminalID == uuid.Nil {
		return Result{}, ErrTerminalRequired
	}
	if !in.PaymentMode.Valid() {
		return Result{}, fmt.Errorf("%w: %q is not a payment mode", domain.ErrValidation, in.PaymentMode)
	}
	if in.PaymentMode.OnCredit() && in.CustomerID == nil {
		return Result{}, fmt.Errorf("%w: a credit sale needs a customer to owe the money", domain.ErrValidation)
	}
	if len(in.Lines) == 0 {
		return Result{}, fmt.Errorf("%w: an invoice needs at least one line", domain.ErrValidation)
	}

	if in.IdempotencyKey != "" {
		if replayed, found, err := s.replay(ctx, in.StoreID, in.IdempotencyKey); err != nil {
			return Result{}, err
		} else if found {
			return replayed, nil
		}
	}

	var out Result
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		result, err := s.create(ctx, tx, in)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		// A retry that arrives while the original request is still in flight
		// finds nothing to replay above, because the first sale has not
		// committed yet. Both then do the work and one of them loses the key on
		// the way out.
		//
		// Losing it is the whole point: that transaction rolled back entire, so
		// no second invoice exists and no stock moved twice. But the caller must
		// not be handed a bare conflict — a till that gets an error after a
		// successful sale either prints nothing or bills the customer again,
		// which is exactly what the key exists to prevent. By the time the
		// unique index refused this insert the winner had committed, so its sale
		// is readable now: answer with it.
		if in.IdempotencyKey != "" && errors.Is(err, platform.ErrConflict) {
			replayed, found, replayErr := s.replay(ctx, in.StoreID, in.IdempotencyKey)
			if replayErr != nil {
				// The conflict says the key is taken; failing to read back what
				// it was taken for is a separate problem, and both belong in
				// the answer rather than one masking the other.
				return Result{}, errors.Join(err, replayErr)
			}
			if found {
				return replayed, nil
			}
		}
		return Result{}, err
	}
	return out, nil
}

// replay returns the sale a previous request with this key raised.
//
// The key is scoped to the store, in the primary key of processed_requests, so
// there is nothing to check here: another shop's "till-1-0001" is a different
// row and this lookup cannot reach it.
//
// That scoping replaced a check. The key space used to be global, and a shop
// whose key another shop had already used was refused — at the till, with a
// customer waiting, for a collision it could neither see nor avoid. A till
// numbering its requests has no other key to offer. Making the two rows distinct
// is the only answer that lets both sales happen.
func (s *Service) replay(ctx context.Context, storeID uuid.UUID, key string) (Result, bool, error) {
	body, found, err := s.repo.FindProcessed(ctx, s.db, storeID, key)
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
	out.Replayed = true
	return out, true, nil
}

func (s *Service) create(ctx context.Context, tx platform.Tx, in CreateInput) (Result, error) {
	storeGSTIN, err := s.repo.StoreGSTINForBilling(ctx, tx, in.StoreID)
	if err != nil {
		return Result{}, fmt.Errorf("load store: %w", err)
	}

	var customer repo.CustomerForSale
	if in.CustomerID != nil {
		customer, err = s.repo.GetCustomerForSale(ctx, tx, in.StoreID, *in.CustomerID)
		if err != nil {
			if errors.Is(err, platform.ErrNotFound) {
				return Result{}, fmt.Errorf("%w: no such customer in this store", domain.ErrValidation)
			}
			return Result{}, fmt.Errorf("load customer: %w", err)
		}
	}

	priced, err := s.priceLines(ctx, tx, in, customer)
	if err != nil {
		return Result{}, err
	}

	computed, err := domain.Compute(domain.ComputeInput{
		Lines:        priced,
		BillDiscount: in.BillDiscount,
		InterState:   domain.InterStateSupply(storeGSTIN, customer.GSTIN),
		RoundTotal:   in.RoundTotal,
	})
	if err != nil {
		return Result{}, err
	}

	// The credit limit is checked inside this transaction, against a locked
	// customer row, and before any stock moves. Checking it outside would let
	// two simultaneous sales each see headroom that only one of them can use.
	//
	// An offline sale is a different question. The till could not check the
	// limit, the goods have gone and the customer has left; the debt is real
	// whatever the limit says. Refusing here would mean a receivable the shop
	// cannot collect on because its own books deny the sale happened. So it is
	// recorded, and flagged for somebody to ring the customer.
	var overLimit error
	if in.PaymentMode.OnCredit() {
		err := s.credit.EnsureWithinCreditLimit(ctx, tx, in.StoreID, customer.ID, computed.Total)
		switch {
		case err == nil:
		case in.BilledOffline:
			overLimit = err
		default:
			return Result{}, err
		}
	}

	invoiceID := uuid.New()
	items, err := s.deductStock(ctx, tx, in, invoiceID, computed)
	if err != nil {
		return Result{}, err
	}

	now := s.now()
	if in.BilledAt != nil {
		// The till's clock decides the document date, because that is when the
		// customer was served. For an offline sale the server hears about it
		// later, sometimes much later, and dating it on arrival would put a
		// Tuesday's takings in Wednesday's column.
		now = *in.BilledAt
	}
	terminalCode, err := s.terminalCode(ctx, tx, in.StoreID, in.TerminalID)
	if err != nil {
		return Result{}, err
	}
	invoiceNo, err := s.invoiceNumber(ctx, tx, in, terminalCode, now)
	if err != nil {
		return Result{}, err
	}

	terminalID := in.TerminalID
	inv := domain.Invoice{
		ID:            invoiceID,
		InvoiceNo:     invoiceNo,
		StoreID:       in.StoreID,
		UserID:        in.UserID,
		TerminalID:    &terminalID,
		CustomerID:    in.CustomerID,
		InvoiceDate:   now,
		SubTotal:      computed.SubTotal,
		CGST:          computed.CGST,
		SGST:          computed.SGST,
		IGST:          computed.IGST,
		Discount:      computed.Discount,
		RoundOff:      computed.RoundOff,
		Total:         computed.Total,
		Status:        domain.StatusCompleted,
		PaymentMode:   in.PaymentMode,
		BilledOffline: in.BilledOffline,
		BilledAt:      now,
		CreatedAt:     now,
	}

	if err := s.repo.InsertInvoice(ctx, tx, inv); err != nil {
		return Result{}, fmt.Errorf("write invoice: %w", err)
	}
	if err := s.repo.InsertItems(ctx, tx, items); err != nil {
		return Result{}, fmt.Errorf("write invoice items: %w", err)
	}

	if in.PaymentMode.OnCredit() {
		// The error is returned, not discarded. The implementation this
		// replaces used `_, _ = tx.Exec(...)` here, so a credit sale could
		// commit with the goods gone and no receivable recorded.
		if err := s.credit.RecordCreditSale(ctx, tx, in.StoreID, customer.ID, inv.ID, inv.Total, in.BilledOffline); err != nil {
			return Result{}, fmt.Errorf("record receivable: %w", err)
		}
	}

	// Whatever the server could not verify about this sale, written in the same
	// transaction as the sale itself. An exception written afterwards can be
	// lost to a crash in between, and the sale would then stand with nothing
	// saying it needs checking — worse than either outcome alone, because
	// nobody would know to look.
	if err := s.recordExceptions(ctx, tx, in, inv, items, overLimit); err != nil {
		return Result{}, err
	}

	out := Result{Invoice: inv, Items: items}
	if in.IdempotencyKey != "" {
		if err := s.repo.RecordProcessed(ctx, tx, in.StoreID, in.IdempotencyKey, "/api/v1/invoices", 201, out); err != nil {
			if errors.Is(err, platform.ErrConflict) {
				// Another request with this key committed first. Its sale
				// stands and this one must not, so the whole transaction goes
				// back. Create catches this conflict on the way out and answers
				// with the sale that did land; the caller never sees it.
				return Result{}, fmt.Errorf("%w: this request has already been processed", platform.ErrConflict)
			}
			return Result{}, fmt.Errorf("record idempotency key: %w", err)
		}
	}
	return out, nil
}

// priceLines resolves each requested line against the store's own price list,
// the unit it is being sold in, and the product's own tax rate.
func (s *Service) priceLines(ctx context.Context, tx platform.Tx, in CreateInput, customer repo.CustomerForSale) ([]domain.PricedLine, error) {
	ids := make([]uuid.UUID, 0, len(in.Lines))
	for _, l := range in.Lines {
		ids = append(ids, l.ProductID)
	}
	products, err := s.repo.ProductsForSale(ctx, tx, in.StoreID, ids)
	if err != nil {
		return nil, fmt.Errorf("load products: %w", err)
	}

	priced := make([]domain.PricedLine, len(in.Lines))
	for i, l := range in.Lines {
		p, ok := products[l.ProductID]
		if !ok {
			return nil, fmt.Errorf("%w: line %d refers to a product this store does not sell", domain.ErrValidation, i+1)
		}
		if !p.IsActive {
			return nil, fmt.Errorf("%w: %s is no longer on sale", domain.ErrValidation, p.Name)
		}

		unit, rate, err := sellingUnit(p, l.ProductUnitID, customer.IsWholesale)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}

		// The fraction rule is applied to what leaves the shelf, not to what
		// was asked for. allow_decimal is a property of the product's stocking
		// unit — half a bar of soap is not a thing — so half a pair, which is
		// one whole bar, is fine and 1.5 bars is not.
		base := unit.BaseQuantity(l.UnitQty)
		if !p.AllowDecimal && !base.Decimal().IsInteger() {
			return nil, fmt.Errorf("%w: %s cannot be sold in fractions, and %s %s comes to %s",
				domain.ErrValidation, p.Name, l.UnitQty, unit.Label, base)
		}

		// A supplied rate is a staleness check, not an instruction. If the
		// caller's cached price differs from the shop's, the sale is refused
		// so the till can refresh rather than charge yesterday's price. It is
		// compared per selling unit, which is the only figure the shop holds.
		if l.Rate != nil && !l.Rate.Equal(rate) {
			return nil, fmt.Errorf("%w: %s by the %s is %s, not %s",
				ErrPriceChanged, p.Name, unit.Label, rate, *l.Rate)
		}

		priced[i] = domain.PricedLine{
			LineInput:   l,
			Unit:        unit,
			Rate:        rate,
			Description: p.Name,
			HSNCode:     p.HSNCode,
			GSTRate:     p.GSTRate,
		}
	}
	return priced, nil
}

// sellingUnit picks the unit a line is sold in and the price of one of them.
//
// A zero unit id means the product's default, which is what a bare quantity has
// always meant and what a client written before units existed sends. Anything
// else must be a unit of this product: a line naming another product's sack has
// to fail rather than quietly price a different thing, since the two ids are
// indistinguishable to a caller that mixed them up.
func sellingUnit(p repo.SaleProduct, unitID uuid.UUID, wholesale bool) (domain.SellingUnit, money.Money, error) {
	if len(p.Units) == 0 {
		return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
			"%w: %s has no unit set up, so there is nothing to sell it by", domain.ErrValidation, p.Name)
	}

	var chosen repo.SaleUnit
	if unitID == uuid.Nil {
		// The default, falling back to the first active unit rather than
		// failing: a product with no default is a data fault that must not take
		// the till down mid-sale.
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
			return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
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
			return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
				"%w: unit %s is not one of the ways %s is sold", domain.ErrValidation, unitID, p.Name)
		}
		if !chosen.IsActive {
			return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
				"%w: the %s of %s is no longer in use", domain.ErrValidation, chosen.Label, p.Name)
		}
	}

	// A unit may be bought but not sold — a supplier's pallet the shop breaks
	// down and never puts on the counter.
	if !chosen.IsSellable {
		return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
			"%w: %s is not sold by the %s", domain.ErrValidation, p.Name, chosen.Label)
	}
	// An unpriced unit must be refused, not sold at zero. The price list is per
	// unit now, so a sack the shop has priced and a sack it has forgotten to
	// price look identical unless this says otherwise.
	if !chosen.HasPrice {
		return domain.SellingUnit{}, money.Zero(), fmt.Errorf(
			"%w: this store has no price for %s by the %s", domain.ErrValidation, p.Name, chosen.Label)
	}

	rate := chosen.SellPrice
	if wholesale && !chosen.WholesalePrice.IsZero() {
		rate = chosen.WholesalePrice
	}
	return domain.SellingUnit{ID: chosen.ID, Label: chosen.Label, QtyInBase: chosen.QtyInBase}, rate, nil
}

// deductStock takes the goods out of stock and builds the invoice lines,
// snapshotting the cost of what actually left.
//
// Stock moves in the product's base unit throughout. Two 25 kg sacks take 50 kg
// off the shelf and a 250 g pouch takes 0.25, so the shop can never be out of
// loose rice while sacks sit unopened.
//
// The shelf is touched in stockdomain.InLockOrder rather than in the order the
// customer put things on the counter. Both orders bill identically — the lines
// are written back into their own positions — but only one of them lets two
// counters serving overlapping baskets deadlock each other. See InLockOrder for
// why the ordering has to be agreed rather than natural.
func (s *Service) deductStock(ctx context.Context, tx platform.Tx, in CreateInput, invoiceID uuid.UUID, computed domain.Computed) ([]domain.Item, error) {
	items := make([]domain.Item, len(computed.Lines))
	productIDs := make([]uuid.UUID, len(computed.Lines))
	for i, l := range computed.Lines {
		productIDs[i] = l.ProductID
	}

	for _, i := range stockdomain.InLockOrder(productIDs) {
		l := computed.Lines[i]
		base := l.Quantity()
		deduction, err := s.stock.DeductFEFO(ctx, tx, stockdomain.DeductionRequest{
			ProductID: l.ProductID,
			StoreID:   in.StoreID,
			BatchHint: l.BatchHint,
			Quantity:  base,
			// Only for a sale that has already happened. At the counter, stock
			// going short is something a cashier can act on; on sync the goods
			// are gone and refusing would put the books at odds with the drawer.
			AllowShortfall: in.BilledOffline,
		})
		if err != nil {
			return nil, err
		}

		batch := ""
		if len(deduction.Batches) > 0 {
			batch = deduction.Batches[0].BatchNo
		}
		items[i] = domain.Item{
			ID:            uuid.New(),
			InvoiceID:     invoiceID,
			ProductID:     l.ProductID,
			ProductName:   l.Description,
			HSNCode:       l.HSNCode,
			GSTRate:       l.GSTRate,
			BatchNo:       batch,
			ProductUnitID: l.Unit.ID,
			UnitQty:       l.UnitQty,
			UnitLabel:     l.Unit.Label,
			Quantity:      base,
			Rate:          l.Rate,
			// The discount stored on the line is everything the customer was
			// given on it, including its share of any bill-level discount, so
			// the line's own figures add up on the printed invoice.
			Discount:       l.Discount.Add(l.ApportionedDiscount),
			CGST:           l.Tax.CGST,
			SGST:           l.Tax.SGST,
			IGST:           l.Tax.IGST,
			Total:          l.Total,
			Shortfall:      deduction.Shortfall,
			CostOfGoods:    deduction.TotalCost,
			UnitCostAtSale: deduction.UnitCost,
		}
	}
	return items, nil
}

// invoiceNumber returns the number this sale is issued under: the one the till
// allocated, or the next in the terminal's series when the till states none.
//
// A till allocates its own because it has to be able to bill with the connection
// down, and a tax invoice carries a number. Accepting one is therefore not a
// concession to offline working; it is the ordinary path, and the server-side
// allocation is what remains for clients that do not keep a counter.
//
// What arrives is checked against the terminal and year it claims. Without that
// a till could state another terminal's series, a closed year's, or a number far
// ahead of the counter — each of which breaks the consecutive series Rule 46(b)
// requires, and none of which the unique index would catch.
func (s *Service) invoiceNumber(ctx context.Context, tx platform.Tx, in CreateInput, terminalCode string, now time.Time) (string, error) {
	finYear := document.FinancialYear(now)

	if in.InvoiceNo == "" {
		seq, err := s.repo.NextInvoiceNumber(ctx, tx, in.StoreID, in.TerminalID, finYear)
		if err != nil {
			return "", fmt.Errorf("allocate invoice number: %w", err)
		}
		return document.Number(document.PrefixInvoice, terminalCode, finYear, seq), nil
	}

	prefix, claimedTerminal, claimedYear, seq, err := document.ParseNumber(in.InvoiceNo)
	if err != nil {
		return "", fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if prefix != document.PrefixInvoice {
		return "", fmt.Errorf("%w: %q is not an invoice number", domain.ErrValidation, in.InvoiceNo)
	}
	if claimedTerminal != terminalCode {
		return "", fmt.Errorf("%w: %q belongs to counter %s, and this sale is from %s",
			domain.ErrValidation, in.InvoiceNo, claimedTerminal, terminalCode)
	}
	if claimedYear != finYear {
		// A till that was offline across midnight on 1 April would otherwise
		// extend the closed year's series, and neither year would then be the
		// unbroken run the rule asks for.
		return "", fmt.Errorf("%w: %q is in financial year %s and this sale falls in %s",
			domain.ErrValidation, in.InvoiceNo, claimedYear, finYear)
	}
	if err := s.repo.ClaimInvoiceNumber(ctx, tx, in.StoreID, in.TerminalID, finYear, seq); err != nil {
		return "", fmt.Errorf("record invoice number: %w", err)
	}
	return in.InvoiceNo, nil
}

// recordExceptions writes what the server could not verify about a sale it
// accepted anyway.
//
// Nothing here fails the sale. By the time it runs the goods have gone and the
// money is in the drawer; an exception that could roll that back would be a way
// of losing a sale to a bookkeeping note.
func (s *Service) recordExceptions(ctx context.Context, tx platform.Tx,
	in CreateInput, inv domain.Invoice, items []domain.Item, overLimit error) error {
	if s.exceptions == nil {
		return nil
	}

	add := func(kind reconcile.Kind, detail string, fill func(*reconcile.Exception)) error {
		e, err := reconcile.New(in.StoreID, kind, detail)
		if err != nil {
			return err
		}
		terminal := in.TerminalID
		invoiceID := inv.ID
		e.TerminalID = &terminal
		e.InvoiceID = &invoiceID
		if fill != nil {
			fill(&e)
		}
		return s.exceptions.Insert(ctx, tx, e)
	}

	if overLimit != nil {
		customer := *in.CustomerID
		if err := add(reconcile.CreditLimitExceeded,
			"This sale took the customer past their credit limit. The till was offline and could not check it. "+
				"Decide whether to raise the limit or ask for payment.",
			func(e *reconcile.Exception) {
				e.CustomerID = &customer
				e.Expected = "within the customer's credit limit"
				e.Actual = overLimit.Error()
			}); err != nil {
			return fmt.Errorf("record credit limit exception: %w", err)
		}
	}

	for _, it := range items {
		if it.Shortfall.IsZero() {
			continue
		}
		product := it.ProductID
		if err := add(reconcile.Oversold,
			"The shelf held less of this than the sale took. The goods have gone, so count the shelf "+
				"and adjust it to what is actually there.",
			func(e *reconcile.Exception) {
				e.ProductID = &product
				e.Expected = it.Quantity.StringFixed() + " on the shelf"
				e.Actual = it.Quantity.Sub(it.Shortfall).StringFixed() + " was there"
			}); err != nil {
			return fmt.Errorf("record oversold exception: %w", err)
		}
	}
	return nil
}

// terminalCode resolves the counter's code for the invoice number.
//
// The store is part of the lookup, not just the terminal id. Terminal codes
// are unique per store, so borrowing another shop's terminal would open a
// counter row under this store keyed to their id and stamp their code onto
// this store's invoice series.
func (s *Service) terminalCode(ctx context.Context, tx platform.Tx, storeID, terminalID uuid.UUID) (string, error) {
	var code string
	err := tx.QueryRowContext(ctx,
		`SELECT code FROM terminals WHERE id = $1 AND store_id = $2 AND is_active`,
		terminalID, storeID).Scan(&code)
	if err != nil {
		mapped := platform.MapError(err)
		if errors.Is(mapped, platform.ErrNotFound) {
			return "", fmt.Errorf("%w: no active terminal %s in this store", domain.ErrValidation, terminalID)
		}
		return "", fmt.Errorf("load terminal: %w", mapped)
	}
	return code, nil
}

// Void cancels a sale, returning the goods to stock and cancelling any
// receivable it raised.
//
// The status guard lives in the UPDATE, so two simultaneous voids cannot both
// restore the stock. The second one finds nothing to change and is told the
// invoice was already voided.
func (s *Service) Void(ctx context.Context, storeID, invoiceID uuid.UUID) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		voided, err := s.repo.VoidInvoice(ctx, tx, storeID, invoiceID)
		if err != nil {
			return fmt.Errorf("void invoice: %w", err)
		}
		if !voided {
			// Either it does not exist, or it was already voided. Tell those
			// apart so the caller can report the right thing.
			if _, err := s.repo.GetInvoice(ctx, tx, storeID, invoiceID); err != nil {
				return err
			}
			return ErrAlreadyVoided
		}

		items, err := s.repo.ItemsForInvoice(ctx, tx, invoiceID)
		if err != nil {
			return fmt.Errorf("load invoice items: %w", err)
		}
		// In the same agreed order a sale takes them, so a void at one counter
		// and a sale at the next cannot wait on each other. A void runs beside
		// live billing by definition — it is what a cashier does when the
		// customer changes their mind.
		productIDs := make([]uuid.UUID, len(items))
		for i, it := range items {
			productIDs[i] = it.ProductID
		}
		for _, i := range stockdomain.InLockOrder(productIDs) {
			it := items[i]
			// The value restored is the value that left, read off the line.
			// Putting the goods back without it would leave the batch holding
			// more stock than it is worth, and a void must not change what the
			// shop's inventory is valued at.
			if err := s.stock.ReturnToStock(ctx, tx, stockdomain.Receipt{
				ProductID: it.ProductID,
				StoreID:   storeID,
				BatchNo:   it.BatchNo,
				Quantity:  it.Quantity,
				TotalCost: it.CostOfGoods,
			}); err != nil {
				return fmt.Errorf("return goods to stock: %w", err)
			}
		}

		if err := s.credit.VoidInvoiceCredit(ctx, tx, invoiceID); err != nil {
			return fmt.Errorf("cancel receivable: %w", err)
		}
		return nil
	})
}

// Get returns one invoice with its lines.
func (s *Service) Get(ctx context.Context, storeID, id uuid.UUID) (Result, error) {
	inv, err := s.repo.GetInvoice(ctx, s.db, storeID, id)
	if err != nil {
		return Result{}, err
	}
	items, err := s.repo.ItemsForInvoice(ctx, s.db, id)
	if err != nil {
		return Result{}, err
	}
	return Result{Invoice: inv, Items: items}, nil
}

// List returns invoices matching a filter.
func (s *Service) List(ctx context.Context, f repo.InvoiceFilter) ([]domain.Invoice, error) {
	return s.repo.ListInvoices(ctx, s.db, f)
}

// GSTR1 returns the unfiled outward supplies for a month, excluding voided
// invoices.
func (s *Service) GSTR1(ctx context.Context, storeID uuid.UUID, month string) ([]repo.GSTR1Row, error) {
	from, to, err := monthRange(month)
	if err != nil {
		return nil, err
	}
	return s.repo.GSTR1Rows(ctx, s.db, storeID, from, to)
}

// MarkGSTR1Filed records that a month's supplies have been filed.
func (s *Service) MarkGSTR1Filed(ctx context.Context, storeID uuid.UUID, month string) (int64, error) {
	from, to, err := monthRange(month)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		marked, err := s.repo.MarkGSTR1Filed(ctx, tx, storeID, month, from, to)
		if err != nil {
			return err
		}
		n = marked
		return nil
	})
	return n, err
}

// monthRange turns "2026-04" into the half-open range covering it.
func monthRange(month string) (from, to time.Time, err error) {
	// Parsed IN the shop's zone, not UTC.
	//
	// time.Parse with no zone in the layout yields UTC, so "2026-04" became
	// midnight UTC — 05:30 on 1 April in India. A sale rung up at 00:30 IST on
	// the first of the month fell outside the month it was made in and filed in
	// the previous month's GSTR-1, while a sale at 00:30 IST on the first of the
	// NEXT month filed in this one. A return is a statutory document and the
	// period it covers is the shop's calendar month, not the server's.
	//
	// document.Location is the same fixed +05:30 the financial year turns on, so
	// a month boundary and a year boundary can never disagree.
	parsed, err := time.ParseInLocation("2006-01", month, document.Location)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: month must look like 2026-04, got %q", domain.ErrValidation, month)
	}
	return parsed, parsed.AddDate(0, 1, 0), nil
}
