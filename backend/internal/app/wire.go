// Package app is the composition root: it builds the services, wires the
// aggregates to one another, and hands the result to the HTTP layer.
//
// The adapters here exist so that no aggregate has to know another's exact
// method signatures. Billing declares the narrow capability it needs — take
// goods out, put goods back, record a receivable — and this package satisfies
// it from the stock and credit services. That keeps the dependency pointing at
// an interface billing owns rather than at another service's API.
package app

import (
	"context"

	"gbos/internal/domain/money"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	analyticssvc "gbos/internal/service/analytics"
	auditsvc "gbos/internal/service/audit"
	authsvc "gbos/internal/service/auth"
	billingsvc "gbos/internal/service/billing"
	cataloguesvc "gbos/internal/service/catalogue"
	creditsvc "gbos/internal/service/credit"
	expensesvc "gbos/internal/service/expense"
	paymentsvc "gbos/internal/service/payment"
	purchasingsvc "gbos/internal/service/purchasing"
	reconcilesvc "gbos/internal/service/reconcile"
	salesreturnsvc "gbos/internal/service/salesreturn"
	stocksvc "gbos/internal/service/stock"
	storesvc "gbos/internal/service/store"

	"github.com/google/uuid"
)

// Services are the wired application services.
type Services struct {
	Auth       *authsvc.Service
	Store      *storesvc.Service
	Catalogue  *cataloguesvc.Service
	Stock      *stocksvc.Service
	Credit     *creditsvc.Service
	Purchasing *purchasingsvc.Service
	Billing    *billingsvc.Service
	Payment    *paymentsvc.Service
	// SalesReturn covers customer returns and inventory write-offs.
	SalesReturn *salesreturnsvc.Service
	// Analytics is read-only reporting. Caching is off until a composition
	// root calls WithCache; see that package's comment for what may be cached.
	Analytics *analyticssvc.Service
	// Expense records what the shop spends running itself. Analytics reads the
	// expenses table directly for the profit and loss deduction, so this
	// service owns the writes and does not duplicate that read.
	Expense *expensesvc.Service
	// Audit is the change trail. It is both a read endpoint and the sink the
	// mutation middleware writes to.
	Audit *auditsvc.Service
	// Reconcile is the working list of what a shop has to look at after a till
	// has been offline: stock the books could not account for, a credit sale
	// past a limit the till could not check.
	Reconcile *reconcilesvc.Service
}

// Config describes what the services need beyond a database.
type Config struct {
	// JWTSecret signs session tokens. Required.
	JWTSecret string
}

// Build wires every service against one database pool.
func Build(db *platform.DB, cfg Config) (Services, error) {
	auth, err := authsvc.New(db, repo.NewUser(), authsvc.Config{Secret: cfg.JWTSecret})
	if err != nil {
		return Services{}, err
	}

	stockRepo := repo.NewStock()
	stock := stocksvc.New(db, stockRepo)
	credit := creditsvc.New(db, repo.NewCredit())
	paymentRepo := repo.NewPayment()

	return Services{
		Auth:       auth,
		Store:      storesvc.New(db, repo.NewStore()),
		Catalogue:  cataloguesvc.New(db, repo.NewCatalogue()),
		Stock:      stock,
		Credit:     credit,
		Purchasing: purchasingsvc.New(db, repo.NewPurchasing(), purchasingStock{svc: stock, repo: stockRepo}),
		Billing: billingsvc.New(db, repo.NewBilling(),
			billingStock{svc: stock, repo: stockRepo},
			billingCredit{svc: credit}).
			// A sale the server cannot fully verify is accepted and flagged, not
			// refused. Without this the sale still lands; it lands unflagged,
			// which is why the recorder is wired here and not left optional.
			WithExceptions(repo.NewReconcile()),
		// The payment repository also satisfies payment's RefundReader port.
		// When the returns aggregate grows a service of its own, that argument
		// is where it plugs in and nothing inside payment changes.
		Payment: paymentsvc.New(db, paymentRepo, paymentRepo),
		SalesReturn: salesreturnsvc.New(db, repo.NewSalesReturn(),
			salesReturnStock{svc: stock, repo: stockRepo}).
			WithExceptions(repo.NewReconcile()),
		// The credit service satisfies analytics' CreditReader directly, so the
		// dashboard's outstanding balance uses the credit aggregate's own
		// definition of a debt rather than a second copy of it.
		Analytics: analyticssvc.New(db, repo.NewAnalytics(), credit),
		Expense:   expensesvc.New(db, repo.NewExpense()),
		// The working list of what a shop has to look at after a till has been
		// offline. It reads with the pool rather than a transaction: nothing it
		// does needs one, and a screen refreshing every few seconds should not
		// hold one open.
		Reconcile: reconcilesvc.New(db, repo.NewReconcile()),
		Audit:     auditsvc.New(db, repo.NewAudit()),
	}, nil
}

// salesReturnStock satisfies salesreturn's StockMover.
type salesReturnStock struct {
	svc  *stocksvc.Service
	repo *repo.Stock
}

// ReturnToStock puts a customer's goods back into the batch they left on,
// leaving that batch's cost basis alone: putting goods back is not a
// revaluation.
func (a salesReturnStock) ReturnToStock(ctx context.Context, q platform.Querier, in stockdomain.Receipt) error {
	return a.repo.AddQuantity(ctx, q, in)
}

// WriteOff takes written-off goods out through the same first-expired-first-out
// walk a sale uses, so a loss larger than any single batch spans several rather
// than silently moving nothing at all.
func (a salesReturnStock) WriteOff(ctx context.Context, tx platform.Tx, req stockdomain.DeductionRequest) (stockdomain.Deduction, error) {
	return a.svc.DeductFEFO(ctx, tx, req)
}

// ProductCostPrice values goods coming back that have no sale behind them.
func (a salesReturnStock) ProductCostPrice(ctx context.Context, q platform.Querier,
	productID, storeID uuid.UUID) (money.Money, error) {
	return a.repo.ProductCostPrice(ctx, q, productID, storeID)
}

// purchasingStock satisfies purchasing's StockWriter.
type purchasingStock struct {
	svc  *stocksvc.Service
	repo *repo.Stock
}

// ReceiveIntoStock adds arriving goods to their batch, carrying the landed
// cost so that what this consignment cost stays attached to what arrived in
// it.
func (a purchasingStock) ReceiveIntoStock(ctx context.Context, tx platform.Tx, m purchasingsvc.StockMovement) error {
	return a.repo.AddQuantity(ctx, tx, stockdomain.Receipt{
		ProductID: m.ProductID,
		StoreID:   m.StoreID,
		BatchNo:   m.BatchNo,
		Quantity:  m.Quantity,
		TotalCost: m.TotalCost,
	})
}

// RemoveFromStock takes goods back out, through the same first-expiring-first
// walk a sale uses.
//
// It used to deduct only from the named batch. A purchase return line carries
// no batch — the goods were bought across however many consignments arrived —
// so every approval asked for a batch literally called "" and was refused with
// `batch "" does not hold 25 of this product` while the shelf held hundreds.
// Approving a purchase return could not succeed at all.
//
// DeductFEFO keeps the named-batch fast path, so a caller that does know which
// consignment is going back still gets an exact deduction, and it fails rather
// than letting on-hand stock go negative: sending a vendor more than the shop
// holds is a mistake worth surfacing, not absorbing.
func (a purchasingStock) RemoveFromStock(ctx context.Context, tx platform.Tx, m purchasingsvc.StockMovement) error {
	_, err := a.svc.DeductFEFO(ctx, tx, stockdomain.DeductionRequest{
		ProductID: m.ProductID,
		StoreID:   m.StoreID,
		BatchHint: m.BatchNo,
		Quantity:  m.Quantity,
	})
	return err
}

// billingStock satisfies billing's StockMover.
type billingStock struct {
	svc  *stocksvc.Service
	repo *repo.Stock
}

func (a billingStock) DeductFEFO(ctx context.Context, tx platform.Tx, req stockdomain.DeductionRequest) (stockdomain.Deduction, error) {
	return a.svc.DeductFEFO(ctx, tx, req)
}

// ReturnToStock puts voided goods back into the batch they came from, so a
// void restores the shop's position exactly rather than creating a new batch
// at an invented cost.
func (a billingStock) ReturnToStock(ctx context.Context, q platform.Querier, in stockdomain.Receipt) error {
	return a.repo.AddQuantity(ctx, q, in)
}

// billingCredit satisfies billing's CreditRecorder.
type billingCredit struct {
	svc *creditsvc.Service
}

func (a billingCredit) EnsureWithinCreditLimit(ctx context.Context, tx platform.Tx, storeID, customerID uuid.UUID, additional money.Money) error {
	return a.svc.EnsureWithinCreditLimit(ctx, tx, storeID, customerID, additional)
}

// RecordCreditSale writes the debt. alreadyIncurred is set for a sale a till
// billed while it could not reach the server: the goods have gone and the
// customer has left, so the limit check has nothing left to prevent and refusing
// would leave the shop a receivable its own books deny.
func (a billingCredit) RecordCreditSale(ctx context.Context, tx platform.Tx,
	storeID, customerID, invoiceID uuid.UUID, amount money.Money, alreadyIncurred bool) error {
	id := invoiceID
	_, err := a.svc.RecordCreditSale(ctx, tx, creditsvc.IssueInput{
		StoreID:         storeID,
		CustomerID:      customerID,
		InvoiceID:       &id,
		Amount:          amount,
		AlreadyIncurred: alreadyIncurred,
	})
	return err
}

// VoidInvoiceCredit discards the row count: billing does not care how many
// ledger rows a void touched, only that it succeeded. An invoice paid partly
// in cash and partly on credit has one row; one migrated from an older system
// might have none.
func (a billingCredit) VoidInvoiceCredit(ctx context.Context, tx platform.Tx, invoiceID uuid.UUID) error {
	_, err := a.svc.VoidInvoiceCredit(ctx, tx, invoiceID)
	return err
}
