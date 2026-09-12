package rbac

import "sort"

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

	PermUserList       = "user.list"
	PermUserDeactivate = "user.deactivate"
	PermPasswordChange = "password.change"
	PermProductUpdate  = "product.update"
	PermVendorUpdate   = "vendor.update"
	PermGSTR1File      = "gstr1.file"
	PermInvoiceVoid    = "invoice.void"
	PermVendorPayment  = "vendor.payment"
	PermPurchaseReturn = "purchase.return"
)

var rolePermissions = map[string][]string{
	"owner": {PermAll},

	"manager": {
		PermCatalogRead,
		PermStoreUpdate,
		PermCategoryWrite,
		PermProductCatalogWrite,
		PermProductPrice,
		PermProductUpdate,
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
		PermVendorUpdate,
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
		PermUserList,
		PermUserDeactivate,
		PermPasswordChange,
		PermGSTR1File,
		PermInvoiceVoid,
		PermVendorPayment,
		PermPurchaseReturn,
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
		PermPasswordChange,
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

// PermissionsFor returns everything a role may do, sorted, so a client can hide
// the controls it would only be refused on.
//
// The wildcard is expanded rather than returned as "*": a client that has to
// special-case a wildcard will get it wrong somewhere, and an owner holding the
// full list behaves the same as an owner holding the wildcard. An unknown role
// returns an empty list, never nil, because a caller ranging over it should get
// nothing rather than a surprise.
//
// This exists so the permission matrix lives in one place. The web client used to
// keep its own copy as a role list per screen, and the two had drifted in both
// directions: a warehouse hand could write off damage but had no menu entry for
// it, and a helper whose entire job is reading the reports could not see them.
func PermissionsFor(role string) []string {
	perms, ok := rolePermissions[role]
	if !ok {
		return []string{}
	}
	for _, p := range perms {
		if p == PermAll {
			out := append([]string(nil), All...)
			sort.Strings(out)
			return out
		}
	}
	out := append([]string(nil), perms...)
	sort.Strings(out)
	return out
}

// All is every permission this system defines, and it is what the owner's
// wildcard expands to.
//
// It has to be written out. Go cannot enumerate its own constants, and the
// obvious substitute — the union of what the other roles name — silently omits
// anything only the owner holds. user.register is exactly that: no role but the
// owner may create users, so deriving the list from the role table told an owner
// they could not, and the web client hid the screen. TestAllCoversTheRoleTable
// keeps this list and the table in step.
var All = []string{
	PermAnalyticsView,
	PermAuditPost,
	PermAuditView,
	PermBarcodeGenerate,
	PermCatalogRead,
	PermCategoryWrite,
	PermCreditPay,
	PermCreditView,
	PermCustomerAdmin,
	PermCustomerCreate,
	PermExpenseRead,
	PermExpenseWrite,
	PermExpiryRead,
	PermGRNRead,
	PermGRNWrite,
	PermGSTR1File,
	PermInvoiceCreate,
	PermInvoiceRead,
	PermInvoiceVoid,
	PermLossRead,
	PermLossWrite,
	PermPasswordChange,
	PermPaymentRead,
	PermPaymentSplit,
	PermProductCatalogWrite,
	PermProductPrice,
	PermProductUpdate,
	PermPurchaseRead,
	PermPurchaseReturn,
	PermPurchaseWrite,
	PermReportView,
	PermReturnApprove,
	PermReturnCreate,
	PermReturnRead,
	PermStockMove,
	PermStoreCreate,
	PermStoreUpdate,
	PermSyncConflict,
	PermSyncMark,
	PermSyncPush,
	PermSyncRead,
	PermUOMWrite,
	PermUserDeactivate,
	PermUserList,
	PermUserRegister,
	PermVendorPayment,
	PermVendorUpdate,
	PermVendorWrite,
}
