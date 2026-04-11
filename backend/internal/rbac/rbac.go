package rbac

const (
	PermAll = "*"

	PermUserRegister = "user.register"

	PermStoreCreate = "store.create"
	PermStoreUpdate = "store.update"

	PermCategoryWrite = "category.write"

	PermProductCatalogWrite = "product.catalog_write"
	PermProductPrice        = "product.price"

	PermCatalogRead = "catalog.read"

	PermStockMove = "stock.move"

	PermSyncMark     = "sync.mark"
	PermSyncPush     = "sync.push"
	PermSyncConflict = "sync.conflict"
	PermSyncRead     = "sync.read"

	PermInvoiceCreate = "invoice.create"
	PermInvoiceRead   = "invoice.read"

	PermCustomerCreate = "customer.create"
	PermCustomerAdmin  = "customer.admin"

	PermCreditView = "credit.view"
	PermCreditPay  = "credit.pay"

	PermVendorWrite = "vendor.write"

	PermPurchaseWrite = "purchase.write"
	PermPurchaseRead  = "purchase.read"

	PermAuditPost = "audit.post"
	PermAuditView = "audit.view"

	PermUOMWrite = "uom.write"

	PermBarcodeGenerate = "barcode.generate"

	PermReturnCreate  = "return.create"
	PermReturnApprove = "return.approve"
	PermReturnRead    = "return.read"

	PermLossWrite = "loss.write"
	PermLossRead  = "loss.read"

	PermGRNWrite = "grn.write"
	PermGRNRead  = "grn.read"

	PermExpenseWrite = "expense.write"
	PermExpenseRead  = "expense.read"

	PermPaymentSplit = "payment.split"
	PermPaymentRead  = "payment.read"

	PermAnalyticsView = "analytics.view"
	PermReportView    = "report.view"

	PermExpiryRead = "expiry.read"
)

var rolePermissions = map[string][]string{
	"owner": {PermAll},

	"manager": {
		PermCatalogRead,
		PermStoreUpdate,
		PermCategoryWrite,
		PermProductCatalogWrite,
		PermProductPrice,
		PermStockMove,
		PermSyncMark,
		PermSyncPush,
		PermSyncConflict,
		PermSyncRead,
		PermInvoiceCreate,
		PermInvoiceRead,
		PermCustomerCreate,
		PermCustomerAdmin,
		PermCreditView,
		PermCreditPay,
		PermVendorWrite,
		PermPurchaseWrite,
		PermPurchaseRead,
		PermAuditPost,
		PermAuditView,
		PermUOMWrite,
		PermBarcodeGenerate,
		PermReturnCreate,
		PermReturnApprove,
		PermReturnRead,
		PermLossWrite,
		PermLossRead,
		PermGRNWrite,
		PermGRNRead,
		PermExpenseWrite,
		PermExpenseRead,
		PermPaymentSplit,
		PermPaymentRead,
		PermAnalyticsView,
		PermReportView,
		PermExpiryRead,
	},

	"cashier": {
		PermCatalogRead,
		PermInvoiceCreate,
		PermInvoiceRead,
		PermCustomerCreate,
		PermReturnCreate,
		PermReturnRead,
		PermPaymentSplit,
		PermPaymentRead,
		PermSyncMark,
		PermSyncRead,
		PermExpiryRead,
	},

	"helper": {
		PermCatalogRead,
		PermInvoiceRead,
		PermPurchaseRead,
		PermReturnRead,
		PermLossRead,
		PermGRNRead,
		PermExpenseRead,
		PermPaymentRead,
		PermSyncRead,
		PermAnalyticsView,
		PermReportView,
		PermAuditView,
		PermExpiryRead,
	},

	"warehouse": {
		PermCatalogRead,
		PermStockMove,
		PermSyncMark,
		PermSyncPush,
		PermSyncRead,
		PermPurchaseWrite,
		PermPurchaseRead,
		PermLossWrite,
		PermLossRead,
		PermGRNWrite,
		PermGRNRead,
		PermReturnRead,
		PermExpiryRead,
	},
}

func RoleHas(role, permission string) bool {
	if permission == "" {
		return false
	}
	perms, ok := rolePermissions[role]
	if !ok {
		return false
	}
	for _, p := range perms {
		if p == PermAll {
			return true
		}
		if p == permission {
			return true
		}
	}
	return false
}
