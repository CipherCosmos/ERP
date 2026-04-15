package models

import (
	"database/sql"
	"github.com/google/uuid"
)

var DB *sql.DB

type UserRole string

const (
	RoleOwner     UserRole = "owner"
	RoleManager   UserRole = "manager"
	RoleCashier   UserRole = "cashier"
	RoleHelper    UserRole = "helper"
	RoleWarehouse UserRole = "warehouse"
)

type User struct {
	ID           uuid.UUID `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         UserRole  `json:"role"`
	StoreID      uuid.UUID `json:"store_id"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    string`json:"created_at"`
	UpdatedAt    string`json:"updated_at"`
}

type Store struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	Phone        string    `json:"phone"`
	GSTIN        string    `json:"gstin"`
	LocationType string    `json:"location_type"` // store | warehouse
	IsActive     bool      `json:"is_active"`
	CreatedAt    string`json:"created_at"`
	UpdatedAt    string`json:"updated_at"`
}

type Category struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	NameHi    string     `json:"name_hi"`
	NameKn    string     `json:"name_kn"`
	ParentID  *uuid.UUID `json:"parent_id"`
	HSNCode   string     `json:"hsn_code"`
	IsActive  bool       `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type UnitOfMeasure struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	DecimalPlaces int       `json:"decimal_places"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     string`json:"created_at"`
	UpdatedAt     string`json:"updated_at"`
}

type Product struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	NameHi       string    `json:"name_hi"`
	NameKn       string    `json:"name_kn"`
	CategoryID   uuid.UUID `json:"category_id"`
	HSNCode      string    `json:"hsn_code"`
	Barcode      string    `json:"barcode"`
	UOMID        uuid.UUID `json:"uom_id"`
	BaseUnitQty  float64   `json:"base_unit_qty"`
	GSTRate      float64   `json:"gst_rate"`
	IsFree       bool      `json:"is_free"`
	Is_active    bool      `json:"is_active"`
	AllowDecimal bool      `json:"allow_decimal"`
	ImageURL     string    `json:"image_url"`
	MinStock     float64   `json:"min_stock"`
	CreatedAt    string`json:"created_at"`
	UpdatedAt    string`json:"updated_at"`
}

type ProductPrice struct {
	ID                 uuid.UUID `json:"id"`
	ProductID          uuid.UUID `json:"product_id"`
	StoreID            uuid.UUID `json:"store_id"`
	MRP                float64   `json:"mrp"`
	CostPrice          float64   `json:"cost_price"`
	SellPrice          float64   `json:"sell_price"`
	WholesaleSellPrice float64   `json:"wholesale_sell_price"`
	IsActive           bool      `json:"is_active"`
	CreatedAt          string`json:"created_at"`
	UpdatedAt          string`json:"updated_at"`
}

type Stock struct {
	ID          uuid.UUID  `json:"id"`
	ProductID   uuid.UUID  `json:"product_id"`
	StoreID     uuid.UUID  `json:"store_id"`
	BatchNo     string     `json:"batch_no"`
	Quantity    float64    `json:"quantity"`
	ReservedQty float64    `json:"reserved_qty"`
	ExpiryDate  *string `json:"expiry_date"`
	MfgDate     *string `json:"mfg_date"`
	CostPrice   float64    `json:"cost_price"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Customer struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	NameHi      string    `json:"name_hi"`
	Phone       string    `json:"phone"`
	Address     string    `json:"address"`
	GSTIN       string    `json:"gstin"`
	CreditLimit float64   `json:"credit_limit"`
	IsWholesale bool      `json:"is_wholesale"`
	StoreID     uuid.UUID `json:"store_id"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   string`json:"created_at"`
	UpdatedAt   string`json:"updated_at"`
}

type Vendor struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Phone       string    `json:"phone"`
	Address     string    `json:"address"`
	GSTIN       string    `json:"gstin"`
	State       string    `json:"state"`
	CreditDays  int       `json:"credit_days"`
	CreditLimit float64   `json:"credit_limit"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   string`json:"created_at"`
	UpdatedAt   string`json:"updated_at"`
}

type Invoice struct {
	ID           uuid.UUID  `json:"id"`
	InvoiceNo    string     `json:"invoice_no"`
	StoreID      uuid.UUID  `json:"store_id"`
	UserID       uuid.UUID  `json:"user_id"`
	CustomerID   *uuid.UUID `json:"customer_id"`
	InvoiceDate  string `json:"invoice_date"`
	SubTotal     float64    `json:"sub_total"`
	CGST         float64    `json:"cgst"`
	SGST         float64    `json:"sgst"`
	IGST         float64    `json:"igst"`
	Discount     float64    `json:"discount"`
	RoundOff     float64    `json:"round_off"`
	Total        float64    `json:"total"`
	Status       string     `json:"status"`
	PaymentMode  string     `json:"payment_mode"`
	IsGSTR1Filed bool       `json:"is_gstr1_filed"`
	GSTR1Month   *string `json:"gstr1_month"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type InvoiceItem struct {
	ID             uuid.UUID `json:"id"`
	InvoiceID      uuid.UUID `json:"invoice_id"`
	ProductID      uuid.UUID `json:"product_id"`
	BatchNo        string    `json:"batch_no"`
	Quantity       float64   `json:"quantity"`
	Rate           float64   `json:"rate"`
	Discount       float64   `json:"discount"`
	CGST           float64   `json:"cgst"`
	SGST           float64   `json:"sgst"`
	IGST           float64   `json:"igst"`
	Total          float64   `json:"total"`
	UnitCostAtSale float64   `json:"unit_cost_at_sale"`
	CreatedAt      string`json:"created_at"`
}

type PurchaseOrder struct {
	ID           uuid.UUID  `json:"id"`
	PO_NO        string     `json:"po_no"`
	StoreID      uuid.UUID  `json:"store_id"`
	VendorID     uuid.UUID  `json:"vendor_id"`
	OrderDate    string `json:"order_date"`
	ExpectedDate *string `json:"expected_date"`
	SubTotal     float64    `json:"sub_total"`
	CGST         float64    `json:"cgst"`
	SGST         float64    `json:"sgst"`
	IGST         float64    `json:"igst"`
	Discount     float64    `json:"discount"`
	Total        float64    `json:"total"`
	Status       string     `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type PurchaseItem struct {
	ID          uuid.UUID `json:"id"`
	PO_ID       uuid.UUID `json:"po_id"`
	ProductID   uuid.UUID `json:"product_id"`
	Quantity    float64   `json:"quantity"`
	UOMID       uuid.UUID `json:"uom_id"`
	ReceivedQty float64   `json:"received_qty"`
	Rate        float64   `json:"rate"`
	Discount    float64   `json:"discount"`
	CGST        float64   `json:"cgst"`
	SGST        float64   `json:"sgst"`
	IGST        float64   `json:"igst"`
	Total       float64   `json:"total"`
	CreatedAt   string`json:"created_at"`
}

type CreditEntry struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID uuid.UUID  `json:"customer_id"`
	InvoiceID  *uuid.UUID `json:"invoice_id"`
	Amount     float64    `json:"amount"`
	Type       string     `json:"type"`
	DueDate    *string `json:"due_date"`
	PaidAmount float64    `json:"paid_amount"`
	Status     string     `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type SyncRecord struct {
	ID        uuid.UUID  `json:"id"`
	TableName string     `json:"table_name"`
	RecordID  uuid.UUID  `json:"record_id"`
	Operation string     `json:"operation"`
	StoreID   uuid.UUID  `json:"store_id"`
	Status    string     `json:"status"`
	CreatedAt string `json:"created_at"`
	SyncedAt  *string `json:"synced_at"`
}

type AuditLog struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	StoreID   uuid.UUID `json:"store_id"`
	Action    string    `json:"action"`
	TableName string    `json:"table_name"`
	RecordID  uuid.UUID `json:"record_id"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	CreatedAt string`json:"created_at"`
}

type InvoicePayment struct {
	ID          uuid.UUID `json:"id"`
	InvoiceID   uuid.UUID `json:"invoice_id"`
	PaymentMode string    `json:"payment_mode"`
	Amount      float64   `json:"amount"`
	Reference   string    `json:"reference"`
	CreatedAt   string`json:"created_at"`
}

type Return struct {
	ID         uuid.UUID  `json:"id"`
	InvoiceID  *uuid.UUID `json:"invoice_id"`
	ProductID  uuid.UUID  `json:"product_id"`
	Quantity   float64    `json:"quantity"`
	Amount     float64    `json:"amount"`
	Reason     string     `json:"reason"`
	RefundMode string     `json:"refund_mode"`
	Status     string     `json:"status"`
	CreatedAt  string `json:"created_at"`
}

type InventoryLoss struct {
	ID        uuid.UUID `json:"id"`
	ProductID uuid.UUID `json:"product_id"`
	StoreID   uuid.UUID `json:"store_id"`
	BatchNo   string    `json:"batch_no"`
	Quantity  float64   `json:"quantity"`
	LossType  string    `json:"loss_type"`
	Reason    string    `json:"reason"`
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt string`json:"created_at"`
}

type GoodsReceipt struct {
	ID           uuid.UUID `json:"id"`
	POID         uuid.UUID `json:"po_id"`
	ReceivedDate string`json:"received_date"`
	ReceivedBy   string    `json:"received_by"`
	Notes        string    `json:"notes"`
	CreatedAt    string`json:"created_at"`
}

type GoodsReceiptItem struct {
	ID          uuid.UUID `json:"id"`
	GRID        uuid.UUID `json:"gr_id"`
	ProductID   uuid.UUID `json:"product_id"`
	OrderedQty  float64   `json:"ordered_qty"`
	ReceivedQty float64   `json:"received_qty"`
	AcceptedQty *float64  `json:"accepted_qty"`
	RejectedQty *float64  `json:"rejected_qty"`
	Reason      string    `json:"reason"`
}

type ExpenseCategory struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	CreatedAt string`json:"created_at"`
}

type Expense struct {
	ID          uuid.UUID `json:"id"`
	StoreID     uuid.UUID `json:"store_id"`
	CategoryID  uuid.UUID `json:"category_id"`
	Amount      float64   `json:"amount"`
	Description string    `json:"description"`
	ExpenseDate string`json:"expense_date"`
	UserID      uuid.UUID `json:"user_id"`
	CreatedAt   string`json:"created_at"`
}
