package rbac

// Permission constants for table-driven RBAC (Phase A).
const (
	PermAll = "*"

	PermStoreCreate = "store.create"
	PermStoreUpdate = "store.update"

	PermCategoryWrite = "category.write"

	PermProductCatalogWrite = "product.catalog_write"
	PermProductPrice        = "product.price"

	PermStockMove = "stock.move"

	PermSyncMark     = "sync.mark"
	PermSyncPush     = "sync.push"
	PermSyncConflict = "sync.conflict"

	PermInvoiceCreate = "invoice.create"

	PermCustomerCreate = "customer.create"
	PermCustomerAdmin  = "customer.admin"

	PermCreditView = "credit.view"
	PermCreditPay  = "credit.pay"

	PermVendorWrite = "vendor.write"

	PermPurchaseWrite = "purchase.write"

	PermAuditPost = "audit.post"

	PermUOMWrite = "uom.write"

	PermBarcodeGenerate = "barcode.generate"

	PermReturnCreate  = "return.create"
	PermReturnApprove = "return.approve"

	PermLossWrite = "loss.write"
	PermGRNWrite  = "grn.write"

	PermExpenseWrite = "expense.write"

	PermPaymentSplit = "payment.split"
)

// rolePermissions maps role -> permissions. Owner has wildcard.
var rolePermissions = map[string][]string{
	"owner": {PermAll},

	"manager": {
		PermStoreUpdate,
		PermCategoryWrite,
		PermProductCatalogWrite,
		PermProductPrice,
		PermStockMove,
		PermSyncMark,
		PermSyncPush,
		PermSyncConflict,
		PermInvoiceCreate,
		PermCustomerCreate,
		PermCustomerAdmin,
		PermCreditView,
		PermCreditPay,
		PermVendorWrite,
		PermPurchaseWrite,
		PermAuditPost,
		PermUOMWrite,
		PermBarcodeGenerate,
		PermReturnCreate,
		PermReturnApprove,
		PermLossWrite,
		PermGRNWrite,
		PermExpenseWrite,
		PermPaymentSplit,
	},

	"cashier": {
		PermInvoiceCreate,
		PermCustomerCreate,
		PermReturnCreate,
		PermPaymentSplit,
		PermSyncMark,
	},

	// Helper: read-only in SRS — no mutating permissions except none.
	"helper": {},

	"warehouse": {
		PermStockMove,
		PermSyncMark,
		PermSyncPush,
		PermPurchaseWrite,
		PermLossWrite,
		PermGRNWrite,
	},
}

// RoleHas returns true if role may perform permission.
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
