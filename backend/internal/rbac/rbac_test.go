package rbac

import "testing"

func TestRoleHas(t *testing.T) {
	if !RoleHas("owner", PermInvoiceCreate) {
		t.Fatal("owner wildcard")
	}
	if !RoleHas("manager", PermCreditPay) {
		t.Fatal("manager credit pay")
	}
	if RoleHas("helper", PermInvoiceCreate) {
		t.Fatal("helper must not create invoices")
	}
	if !RoleHas("helper", PermReportView) {
		t.Fatal("helper reads reports")
	}
	if RoleHas("helper", PermStockMove) {
		t.Fatal("helper must not move stock")
	}
	if !RoleHas("cashier", PermInvoiceCreate) {
		t.Fatal("cashier POS")
	}
	if RoleHas("cashier", PermCreditPay) {
		t.Fatal("cashier must not post credit payments")
	}
	if !RoleHas("warehouse", PermPurchaseWrite) {
		t.Fatal("warehouse PO")
	}
	if RoleHas("warehouse", PermInvoiceCreate) {
		t.Fatal("warehouse must not bill")
	}
	if !RoleHas("owner", PermUserRegister) {
		t.Fatal("owner registers users")
	}
	if RoleHas("manager", PermUserRegister) {
		t.Fatal("manager must not register users")
	}
}
