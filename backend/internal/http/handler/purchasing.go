package handler

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/purchasing"
	stockdomain "gbos/internal/domain/stock"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	service "gbos/internal/service/purchasing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Purchasing serves the buy side: vendors, purchase orders, goods receipts,
// purchase returns and vendor payments.
type Purchasing struct {
	svc *service.Service
}

// NewPurchasing returns a purchasing handler.
func NewPurchasing(svc *service.Service) *Purchasing {
	return &Purchasing{svc: svc}
}

// Register mounts the purchasing routes.
//
// Every one is store-scoped, so they belong behind the store middleware. The
// paths mirror the ones the existing frontend already calls, including the two
// legacy spellings of receiving goods — POST /purchases/receive and POST /grn —
// which are now the same operation behind two doors.
func (h *Purchasing) Register(r gin.IRoutes) {
	r.GET("/vendors", h.ListVendors)
	r.GET("/vendors/:id", h.GetVendor)
	r.POST("/vendors", h.CreateVendor)
	r.PUT("/vendors/:id", h.UpdateVendor)
	r.POST("/vendors/:id/deactivate", h.DeactivateVendor)

	r.GET("/purchases", h.ListPurchaseOrders)
	r.GET("/purchases/:id", h.GetPurchaseOrder)
	r.POST("/purchases", h.CreatePurchaseOrder)
	r.POST("/purchases/receive", h.ReceiveGoods)
	r.POST("/purchases/cancel/:id", h.CancelPurchaseOrder)

	r.GET("/grn", h.ListGoodsReceipts)
	r.GET("/grn/:id", h.GetGoodsReceipt)
	r.POST("/grn", h.ReceiveGoods)

	r.GET("/purchase-returns", h.ListPurchaseReturns)
	r.GET("/purchase-returns/:id", h.GetPurchaseReturn)
	r.POST("/purchase-returns", h.CreatePurchaseReturn)
	r.POST("/purchase-returns/:id/approve", h.ApprovePurchaseReturn)

	r.GET("/vendor-payments", h.ListVendorPayments)
	r.POST("/vendor-payments", h.RecordVendorPayment)
	r.GET("/vendor-payments/summary", h.VendorPaymentSummary)
}

// ---------------------------------------------------------------------------
// Vendors
// ---------------------------------------------------------------------------

// purchasingVendorRequest is the wire shape for creating or replacing a vendor.
// CreditLimit binds as money.Money, which accepts a JSON number or a string
// without going through a float.
type purchasingVendorRequest struct {
	Name        string      `json:"name" binding:"required"`
	Phone       string      `json:"phone"`
	Address     string      `json:"address"`
	GSTIN       string      `json:"gstin"`
	State       string      `json:"state"`
	CreditDays  int         `json:"credit_days"`
	CreditLimit money.Money `json:"credit_limit"`
}

func (r purchasingVendorRequest) toInput() domain.VendorInput {
	return domain.VendorInput{
		Name:        r.Name,
		Phone:       r.Phone,
		Address:     r.Address,
		GSTIN:       r.GSTIN,
		State:       r.State,
		CreditDays:  r.CreditDays,
		CreditLimit: r.CreditLimit,
	}
}

func (h *Purchasing) ListVendors(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	vendors, err := h.svc.ListVendors(c.Request.Context(), domain.VendorFilter{
		Search:          c.Query("search"),
		Limit:           intQuery(c, "limit", 0),
		IncludeInactive: c.Query("include_inactive") == "true",
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vendors": vendors})
}

// GetVendor returns a vendor together with what they are currently owed, which
// is the figure a buyer needs before placing another order.
func (h *Purchasing) GetVendor(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	vendor, exposure, err := h.svc.GetVendor(c.Request.Context(), id)
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"vendor":      vendor,
		"exposure":    exposure,
		"outstanding": exposure.Outstanding(),
	})
}

func (h *Purchasing) CreateVendor(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	var req purchasingVendorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	created, err := h.svc.CreateVendor(c.Request.Context(), req.toInput())
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Purchasing) UpdateVendor(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req purchasingVendorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	updated, err := h.svc.UpdateVendor(c.Request.Context(), id, req.toInput())
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// DeactivateVendor retires a vendor. There is no delete: a vendor with purchase
// history is referenced by orders and payments.
func (h *Purchasing) DeactivateVendor(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetVendorActive(c.Request.Context(), id, false); err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": false})
}

// ---------------------------------------------------------------------------
// Purchase orders
// ---------------------------------------------------------------------------

// purchasingOrderLineRequest is one line of a purchase order.
//
// Note what is absent: there is no tax field and no total the server will
// believe. Total is a claim, compared for exact equality against the figure
// computed from the rate, quantity, discount and the product's own GST rate,
// so a pricing disagreement surfaces at order time rather than at
// reconciliation.
type purchasingOrderLineRequest struct {
	ProductID string `json:"product_id" binding:"required"`
	// ProductUnitID names what is being bought — the 25 kg sack rather than the
	// loose kilo — and is not the same thing as uom_id below: a kilogram is a
	// kilogram everywhere, whereas a sack is 25 kg of this product and 50 kg of
	// another. Absent means the product's default unit.
	ProductUnitID *string `json:"product_unit_id"`
	// UnitQty is how many of that unit. `quantity` is the older spelling of the
	// same field and is still accepted, because it always meant "how many of
	// the thing you are ordering" and the default unit is what it meant it in.
	UnitQty  *money.Quantity `json:"unit_qty"`
	Quantity *money.Quantity `json:"quantity"`
	UOMID    *string         `json:"uom_id"`
	// Rate is the price of one buying unit — one sack — so the line total is a
	// multiplication.
	Rate     money.Money `json:"rate"`
	Discount money.Money `json:"discount"`
	Total    money.Money `json:"total"`
}

// qty resolves the two spellings. A line stating neither is left at zero, which
// the domain refuses with "quantity must be greater than zero".
func (r purchasingOrderLineRequest) qty() money.Quantity {
	switch {
	case r.UnitQty != nil:
		return *r.UnitQty
	case r.Quantity != nil:
		return *r.Quantity
	default:
		return money.ZeroQuantity()
	}
}

type purchasingOrderRequest struct {
	VendorID string `json:"vendor_id" binding:"required"`
	// TerminalID names the counter whose numbering series this order draws
	// from. document_sequences is keyed by terminal, so an order cannot be
	// numbered without one.
	TerminalID   string                       `json:"terminal_id" binding:"required"`
	OrderDate    *string                      `json:"order_date"`
	ExpectedDate *string                      `json:"expected_date"`
	Items        []purchasingOrderLineRequest `json:"items" binding:"required"`
}

func (h *Purchasing) ListPurchaseOrders(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	vendorID, err := optionalUUID(queryPtr(c, "vendor_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	orders, err := h.svc.ListPurchaseOrders(c.Request.Context(), domain.POFilter{
		StoreID:  storeID,
		VendorID: vendorID,
		Status:   domain.POStatus(c.Query("status")),
		Limit:    intQuery(c, "limit", 0),
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"purchase_orders": orders})
}

func (h *Purchasing) GetPurchaseOrder(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	po, items, err := h.svc.GetPurchaseOrder(c.Request.Context(), id, storeID)
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"purchase_order": po, "items": items})
}

func (h *Purchasing) CreatePurchaseOrder(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req purchasingOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	vendorID, err := uuid.Parse(req.VendorID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	terminalID, err := uuid.Parse(req.TerminalID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("terminal_id is not a valid identifier"))
		return
	}
	orderDate, err := optionalDate(req.OrderDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("order_date must look like 2026-04-01"))
		return
	}
	expectedDate, err := optionalDate(req.ExpectedDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("expected_date must look like 2026-04-30"))
		return
	}

	lines := make([]domain.LineInput, len(req.Items))
	for i, item := range req.Items {
		productID, err := uuid.Parse(item.ProductID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_id is not a valid identifier"))
			return
		}
		uomID, err := optionalUUID(item.UOMID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item uom_id is not a valid identifier"))
			return
		}
		productUnitID, err := purchasingUnitID(item.ProductUnitID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_unit_id is not a valid identifier"))
			return
		}
		lines[i] = domain.LineInput{
			ProductID:     productID,
			ProductUnitID: productUnitID,
			UnitQty:       item.qty(),
			UOMID:         uomID,
			Rate:          item.Rate,
			Discount:      item.Discount,
			ClaimedTotal:  item.Total,
		}
	}

	po, items, err := h.svc.CreatePurchaseOrder(c.Request.Context(), service.CreatePurchaseOrderInput{
		StoreID:      storeID,
		VendorID:     vendorID,
		TerminalID:   terminalID,
		OrderDate:    purchasingDateOrZero(orderDate),
		ExpectedDate: expectedDate,
		Lines:        lines,
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"purchase_order": po, "items": items})
}

func (h *Purchasing) CancelPurchaseOrder(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.CancelPurchaseOrder(c.Request.Context(), id, storeID); err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "status": domain.StatusCancelled})
}

// ---------------------------------------------------------------------------
// Goods receipts
// ---------------------------------------------------------------------------

// purchasingReceiptLineRequest is one line of a delivery arriving against a
// purchase order.
//
// Quantity and UnitQty are accepted as aliases for ReceivedQty because the two
// legacy endpoints spelled the same field differently, and both are still
// called. AcceptedQty is a pointer because "not stated" and "zero" are
// different facts: a delivery of 100 with all 100 rejected must book nothing
// into stock, which is only expressible if the type can tell a stated zero from
// an absent value.
//
// Every quantity here is counted in the line's unit. Ten sacks received with
// two rejected is received_qty 10 and rejected_qty 2, and the 250 kg that
// reaches the shelf is worked out from the unit rather than stated.
type purchasingReceiptLineRequest struct {
	ProductID string `json:"product_id" binding:"required"`
	// ProductUnitID names the unit the delivery is counted in. Absent means the
	// unit the purchase order line was placed in, which is what a clerk
	// confirming an order against its paperwork means.
	ProductUnitID *string         `json:"product_unit_id"`
	ReceivedQty   *money.Quantity `json:"received_qty"`
	UnitQty       *money.Quantity `json:"unit_qty"`
	Quantity      *money.Quantity `json:"quantity"`
	AcceptedQty   *money.Quantity `json:"accepted_qty"`
	RejectedQty   money.Quantity  `json:"rejected_qty"`
	Reason        string          `json:"reason"`
	BatchNo       string          `json:"batch_no"`
	// CostPrice is what one buying unit landed at — what one sack cost, not one
	// kilo. Zero means "use the store's recorded cost for the unit".
	CostPrice money.Money `json:"cost_price"`
}

// received resolves the three spellings. A line stating none is left at zero,
// which the domain refuses with "received quantity must be greater than zero".
func (r purchasingReceiptLineRequest) received() money.Quantity {
	switch {
	case r.ReceivedQty != nil:
		return *r.ReceivedQty
	case r.UnitQty != nil:
		return *r.UnitQty
	case r.Quantity != nil:
		return *r.Quantity
	default:
		return money.ZeroQuantity()
	}
}

type purchasingReceiptRequest struct {
	POID         string                         `json:"po_id" binding:"required"`
	ReceivedDate *string                        `json:"received_date"`
	Notes        string                         `json:"notes"`
	Items        []purchasingReceiptLineRequest `json:"items" binding:"required"`
}

// ReceiveGoods records a goods receipt, books the accepted quantities into
// stock and advances the order — one operation, whether it is reached through
// /purchases/receive or /grn.
func (h *Purchasing) ReceiveGoods(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := purchasingActor(c)
	if !ok {
		return
	}

	var req purchasingReceiptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	poID, err := uuid.Parse(req.POID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("po_id is not a valid identifier"))
		return
	}
	receivedDate, err := optionalDate(req.ReceivedDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("received_date must look like 2026-04-30"))
		return
	}

	lines := make([]domain.ReceiptLineInput, len(req.Items))
	for i, item := range req.Items {
		productID, err := uuid.Parse(item.ProductID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_id is not a valid identifier"))
			return
		}
		productUnitID, err := purchasingUnitID(item.ProductUnitID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_unit_id is not a valid identifier"))
			return
		}
		lines[i] = domain.ReceiptLineInput{
			ProductID:     productID,
			ProductUnitID: productUnitID,
			ReceivedQty:   item.received(),
			RejectedQty:   item.RejectedQty,
			AcceptedQty:   item.AcceptedQty,
			Reason:        item.Reason,
			BatchNo:       item.BatchNo,
			CostPrice:     item.CostPrice,
		}
	}

	// An unstated received date means "today": one of the two legacy clients
	// never sent one, and the goods are in the shop either way.
	when := time.Now()
	if receivedDate != nil {
		when = *receivedDate
	}

	receipt, items, err := h.svc.ReceiveGoods(c.Request.Context(), service.ReceiveGoodsInput{
		POID:         poID,
		StoreID:      storeID,
		UserID:       userID,
		ReceivedDate: when,
		Notes:        req.Notes,
		Lines:        lines,
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"goods_receipt": receipt, "items": items})
}

func (h *Purchasing) ListGoodsReceipts(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	poID, err := optionalUUID(queryPtr(c, "po_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("po_id is not a valid identifier"))
		return
	}
	receipts, err := h.svc.ListGoodsReceipts(c.Request.Context(), storeID, poID, intQuery(c, "limit", 0))
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"goods_receipts": receipts})
}

func (h *Purchasing) GetGoodsReceipt(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	receipt, items, err := h.svc.GetGoodsReceipt(c.Request.Context(), id, storeID)
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"goods_receipt": receipt, "items": items})
}

// ---------------------------------------------------------------------------
// Purchase returns
// ---------------------------------------------------------------------------

type purchasingReturnLineRequest struct {
	ProductID string         `json:"product_id" binding:"required"`
	Quantity  money.Quantity `json:"quantity" binding:"required"`
	Rate      money.Money    `json:"rate"`
}

type purchasingReturnRequest struct {
	POID       *string                       `json:"po_id"`
	VendorID   string                        `json:"vendor_id" binding:"required"`
	ReturnDate *string                       `json:"return_date"`
	Reason     string                        `json:"reason"`
	Items      []purchasingReturnLineRequest `json:"items" binding:"required"`
}

func (h *Purchasing) ListPurchaseReturns(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	vendorID, err := optionalUUID(queryPtr(c, "vendor_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	returns, err := h.svc.ListPurchaseReturns(c.Request.Context(), domain.ReturnFilter{
		StoreID:  storeID,
		VendorID: vendorID,
		Status:   domain.ReturnStatus(c.Query("status")),
		Limit:    intQuery(c, "limit", 0),
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"purchase_returns": returns})
}

func (h *Purchasing) GetPurchaseReturn(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	pr, items, err := h.svc.GetPurchaseReturn(c.Request.Context(), id, storeID)
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"purchase_return": pr, "items": items})
}

// CreatePurchaseReturn raises a pending return. No stock moves here: stock
// moves on approval, once.
func (h *Purchasing) CreatePurchaseReturn(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := purchasingActor(c)
	if !ok {
		return
	}

	var req purchasingReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	vendorID, err := uuid.Parse(req.VendorID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	poID, err := optionalUUID(req.POID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("po_id is not a valid identifier"))
		return
	}
	returnDate, err := optionalDate(req.ReturnDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("return_date must look like 2026-04-30"))
		return
	}

	lines := make([]domain.ReturnLineInput, len(req.Items))
	for i, item := range req.Items {
		productID, err := uuid.Parse(item.ProductID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_id is not a valid identifier"))
			return
		}
		lines[i] = domain.ReturnLineInput{
			ProductID: productID,
			Quantity:  item.Quantity,
			Rate:      item.Rate,
		}
	}

	pr, items, err := h.svc.CreatePurchaseReturn(c.Request.Context(), service.CreatePurchaseReturnInput{
		POID:       poID,
		VendorID:   vendorID,
		StoreID:    storeID,
		UserID:     userID,
		ReturnDate: purchasingDateOrZero(returnDate),
		Reason:     req.Reason,
		Lines:      lines,
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"purchase_return": pr, "items": items})
}

func (h *Purchasing) ApprovePurchaseReturn(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	approved, err := h.svc.ApprovePurchaseReturn(c.Request.Context(), id, storeID)
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, approved)
}

// ---------------------------------------------------------------------------
// Vendor payments
// ---------------------------------------------------------------------------

type purchasingPaymentRequest struct {
	VendorID    string      `json:"vendor_id" binding:"required"`
	POID        *string     `json:"po_id"`
	Amount      money.Money `json:"amount" binding:"required"`
	PaymentMode string      `json:"payment_mode"`
	Reference   string      `json:"reference"`
	PaymentDate *string     `json:"payment_date"`
	Notes       string      `json:"notes"`
}

func (h *Purchasing) ListVendorPayments(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	vendorID, err := optionalUUID(queryPtr(c, "vendor_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	payments, err := h.svc.ListVendorPayments(c.Request.Context(), storeID, vendorID, intQuery(c, "limit", 0))
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payments": payments})
}

// RecordVendorPayment records money paid out, which is what makes a vendor's
// exposure fall as bills are settled.
func (h *Purchasing) RecordVendorPayment(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := purchasingActor(c)
	if !ok {
		return
	}

	var req purchasingPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	vendorID, err := uuid.Parse(req.VendorID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	poID, err := optionalUUID(req.POID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("po_id is not a valid identifier"))
		return
	}
	paymentDate, err := optionalDate(req.PaymentDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("payment_date must look like 2026-04-30"))
		return
	}

	payment, err := h.svc.RecordVendorPayment(c.Request.Context(), domain.PaymentInput{
		VendorID:    vendorID,
		POID:        poID,
		Amount:      req.Amount,
		PaymentMode: req.PaymentMode,
		Reference:   req.Reference,
		PaymentDate: purchasingDateOrZero(paymentDate),
		Notes:       req.Notes,
		UserID:      userID,
		StoreID:     storeID,
	})
	if err != nil {
		respondPurchasing(c, err)
		return
	}
	c.JSON(http.StatusCreated, payment)
}

// purchasingPaymentSummaryRow is one vendor's share of the payments in view.
type purchasingPaymentSummaryRow struct {
	VendorID     uuid.UUID   `json:"vendor_id"`
	TotalPaid    money.Money `json:"total_paid"`
	PaymentCount int         `json:"payment_count"`
}

// VendorPaymentSummary totals payments per vendor.
//
// The service exposes no aggregate of its own, so this folds the payment
// listing it does expose. That listing is bounded — the repository clamps the
// limit — so the response says how many payments the figures were computed
// from rather than implying it covered every payment ever made. `limit` raises
// the window; start_date and end_date narrow it, inclusive of both days.
func (h *Purchasing) VendorPaymentSummary(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	vendorID, err := optionalUUID(queryPtr(c, "vendor_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("vendor_id is not a valid identifier"))
		return
	}
	from, err := optionalDate(queryPtr(c, "start_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("start_date must look like 2026-04-01"))
		return
	}
	to, err := optionalDate(queryPtr(c, "end_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("end_date must look like 2026-04-30"))
		return
	}

	payments, err := h.svc.ListVendorPayments(c.Request.Context(), storeID, vendorID, intQuery(c, "limit", 0))
	if err != nil {
		respondPurchasing(c, err)
		return
	}

	byVendor := make(map[uuid.UUID]*purchasingPaymentSummaryRow)
	grandTotal := money.Zero()
	counted := 0
	for _, p := range payments {
		if from != nil && p.PaymentDate.Before(*from) {
			continue
		}
		if to != nil && !p.PaymentDate.Before(to.AddDate(0, 0, 1)) {
			continue
		}
		row, ok := byVendor[p.VendorID]
		if !ok {
			row = &purchasingPaymentSummaryRow{VendorID: p.VendorID, TotalPaid: money.Zero()}
			byVendor[p.VendorID] = row
		}
		row.TotalPaid = row.TotalPaid.Add(p.Amount)
		row.PaymentCount++
		grandTotal = grandTotal.Add(p.Amount)
		counted++
	}

	summary := make([]purchasingPaymentSummaryRow, 0, len(byVendor))
	for _, row := range byVendor {
		summary = append(summary, *row)
	}
	// Largest first, then by id so the order is stable across calls rather than
	// following the map's iteration order.
	sort.Slice(summary, func(i, j int) bool {
		if c := summary[i].TotalPaid.Cmp(summary[j].TotalPaid); c != 0 {
			return c > 0
		}
		return summary[i].VendorID.String() < summary[j].VendorID.String()
	})

	c.JSON(http.StatusOK, gin.H{
		"summary":             summary,
		"grand_total":         grandTotal,
		"payments_considered": counted,
	})
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// purchasingActor returns the user a document is attributed to. A receipt, a
// return and a payment all record who did it, and the domain refuses to build
// one without.
func purchasingActor(c *gin.Context) (uuid.UUID, bool) {
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return uuid.Nil, false
	}
	return session.UserID, true
}

// purchasingUnitID parses an optional product unit. An absent one is the nil
// uuid, which the service reads as "whatever unit this line would otherwise be
// in" — the product's default on an order, and the order's own unit on a
// receipt.
func purchasingUnitID(s *string) (uuid.UUID, error) {
	if s == nil || *s == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(*s)
}

// purchasingDateOrZero flattens an absent date to the zero time, which every
// service entry point that requires a date rejects with its own message. The
// handler does not invent one.
func purchasingDateOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// respondPurchasing maps the purchasing-specific errors to statuses a buyer can
// act on, before falling through to the shared envelope.
func respondPurchasing(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, domain.ErrCreditLimitExceeded):
		// 409: the order is well-formed and would succeed once the vendor is
		// paid down, so it is a conflict with the current state rather than a
		// bad request the caller should not repeat.
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, domain.ErrInvalidTransition):
		// 409: a cancelled order being received, a return approved twice. The
		// guard lives in the UPDATE, so this is what a lost race looks like.
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, domain.ErrOverReceipt):
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, stockdomain.ErrInsufficientStock):
		// Approving a return takes goods out of stock, so the stock aggregate's
		// refusal surfaces here.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
