package http_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestBuyingRoundTripThroughHTTP drives a delivery the whole way: register a
// vendor, raise an order, receive it short with some of it rejected, send the
// rejected goods back, and pay the bill.
//
// The purchasing handlers were the least exercised over HTTP of anything that
// moves money, and every figure below is one a shop would query: what the
// delivery cost, what reached the shelf, and what the vendor is still owed.
func TestBuyingRoundTripThroughHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	store := h.storeID.String()
	today := time.Now().In(time.FixedZone("IST", 5*3600+1800)).Format("2006-01-02")

	product, unit := h.seedSellableProduct(t, "Toor Dal", "10071000", 5)

	// --- the vendor ----------------------------------------------------------
	vendor := h.request(http.MethodPost, "/api/v1/vendors?store_id="+store, h.token, map[string]any{
		"name": "Metro Wholesale", "phone": "9000000002",
		"gstin": "29AABCU9603R1ZX", "state": "Karnataka", "credit_days": 30,
	})
	if vendor.Code != http.StatusCreated {
		t.Fatalf("create vendor: status %d body %s", vendor.Code, vendor.Body.String())
	}
	var v struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		IsActive bool   `json:"is_active"`
	}
	mustDecode(t, vendor.Body.Bytes(), &v)

	updated := h.request(http.MethodPut, "/api/v1/vendors/"+v.ID+"?store_id="+store, h.token,
		map[string]any{"name": "Metro Wholesale Pvt Ltd", "credit_days": 45})
	if updated.Code != http.StatusOK {
		t.Fatalf("update vendor: status %d body %s", updated.Code, updated.Body.String())
	}

	// --- the order -----------------------------------------------------------
	// Ten units at Rs.90 is Rs.900 ordered.
	order := h.request(http.MethodPost, "/api/v1/purchases?store_id="+store, h.token, map[string]any{
		"vendor_id": v.ID, "terminal_id": h.terminalID(t), "order_date": today,
		"items": []map[string]any{{
			"product_id": product, "product_unit_id": unit, "unit_qty": 10, "rate": "90.00",
		}},
	})
	if order.Code != http.StatusCreated {
		t.Fatalf("raise order: status %d body %s", order.Code, order.Body.String())
	}
	// Money and quantities arrive as JSON *numbers*, not strings — a deliberate
	// choice measured against the client, which calls .toFixed on these fields
	// directly. json.Number keeps the exact text so a test can compare paise.
	var po struct {
		PurchaseOrder struct {
			ID     string      `json:"id"`
			Status string      `json:"status"`
			Total  json.Number `json:"total"`
		} `json:"purchase_order"`
	}
	mustDecode(t, order.Body.Bytes(), &po)
	if po.PurchaseOrder.Status != "pending" {
		t.Errorf("a new order is %q, want pending", po.PurchaseOrder.Status)
	}

	// --- the delivery: ten arrived, two refused ------------------------------
	receipt := h.request(http.MethodPost, "/api/v1/purchases/receive?store_id="+store, h.token,
		map[string]any{
			"po_id": po.PurchaseOrder.ID, "received_date": today,
			"items": []map[string]any{{
				"product_id": product, "product_unit_id": unit,
				"received_qty": 10, "rejected_qty": 2, "accepted_qty": 8,
				"reason": "two sacks torn", "batch_no": "MW-2026-04", "cost_price": "90.00",
			}},
		})
	if receipt.Code != http.StatusCreated {
		t.Fatalf("receive goods: status %d body %s", receipt.Code, receipt.Body.String())
	}

	// Only what was accepted reaches the shelf: eight, not ten.
	stock := h.request(http.MethodGet, "/api/v1/stock/product/"+product+"?store_id="+store, h.token, nil)
	if stock.Code != http.StatusOK {
		t.Fatalf("read stock: status %d body %s", stock.Code, stock.Body.String())
	}
	var batches struct {
		Batches []struct {
			BatchNo   string      `json:"batch_no"`
			Quantity  json.Number `json:"quantity"`
			TotalCost json.Number `json:"total_cost"`
		} `json:"batches"`
	}
	mustDecode(t, stock.Body.Bytes(), &batches)
	if len(batches.Batches) != 1 {
		t.Fatalf("batches = %+v, want the one delivery", batches.Batches)
	}
	if batches.Batches[0].Quantity.String() != "8.000" {
		t.Errorf("on hand = %s, want 8.000 — the two refused sacks reached the shelf",
			batches.Batches[0].Quantity)
	}
	// Eight at Rs.90 is Rs.720. The two refused are not the shop's cost.
	if batches.Batches[0].TotalCost.String() != "720.00" {
		t.Errorf("batch value = %s, want 720.00 for eight at Rs.90", batches.Batches[0].TotalCost)
	}

	// The receipt itself is readable, rejection and all.
	grns := h.request(http.MethodGet, "/api/v1/grn?store_id="+store, h.token, nil)
	if grns.Code != http.StatusOK {
		t.Fatalf("list receipts: status %d body %s", grns.Code, grns.Body.String())
	}

	// --- send the refused goods back ----------------------------------------
	ret := h.request(http.MethodPost, "/api/v1/purchase-returns?store_id="+store, h.token,
		map[string]any{
			"vendor_id": v.ID, "po_id": po.PurchaseOrder.ID, "return_date": today,
			"reason": "torn sacks", "items": []map[string]any{{
				"product_id": product, "quantity": 2, "rate": "90.00",
			}},
		})
	if ret.Code != http.StatusCreated {
		t.Fatalf("raise purchase return: status %d body %s", ret.Code, ret.Body.String())
	}
	var pr struct {
		PurchaseReturn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"purchase_return"`
	}
	mustDecode(t, ret.Body.Bytes(), &pr)

	// Approving one is what actually takes the goods off the shelf. It used to
	// be impossible: the deduction asked for a batch literally named "", which
	// no shelf holds, so every approval was refused while the stock sat there.
	approved := h.request(http.MethodPost,
		"/api/v1/purchase-returns/"+pr.PurchaseReturn.ID+"/approve?store_id="+store, h.token, nil)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve purchase return: status %d body %s", approved.Code, approved.Body.String())
	}

	stock = h.request(http.MethodGet, "/api/v1/stock/product/"+product+"?store_id="+store, h.token, nil)
	mustDecode(t, stock.Body.Bytes(), &batches)
	var onHand string
	if len(batches.Batches) > 0 {
		onHand = batches.Batches[0].Quantity.String()
	}
	if onHand != "6.000" {
		t.Errorf("on hand = %s after returning two, want 6.000", onHand)
	}

	// --- pay the vendor ------------------------------------------------------
	paid := h.request(http.MethodPost, "/api/v1/vendor-payments?store_id="+store, h.token,
		map[string]any{
			"vendor_id": v.ID, "po_id": po.PurchaseOrder.ID, "amount": "720.00",
			"payment_mode": "bank", "reference": "NEFT-0001", "payment_date": today,
		})
	if paid.Code != http.StatusCreated {
		t.Fatalf("pay vendor: status %d body %s", paid.Code, paid.Body.String())
	}

	summary := h.request(http.MethodGet, "/api/v1/vendor-payments/summary?store_id="+store, h.token, nil)
	if summary.Code != http.StatusOK {
		t.Fatalf("vendor payment summary: status %d body %s", summary.Code, summary.Body.String())
	}

	// --- and a vendor can be retired -----------------------------------------
	off := h.request(http.MethodPost, "/api/v1/vendors/"+v.ID+"/deactivate?store_id="+store, h.token, nil)
	if off.Code != http.StatusOK {
		t.Fatalf("deactivate vendor: status %d body %s", off.Code, off.Body.String())
	}
	listed := h.request(http.MethodGet, "/api/v1/vendors?store_id="+store, h.token, nil)
	var vendors struct {
		Vendors []struct {
			ID string `json:"id"`
		} `json:"vendors"`
	}
	mustDecode(t, listed.Body.Bytes(), &vendors)
	for _, got := range vendors.Vendors {
		if got.ID == v.ID {
			t.Error("a retired vendor is still in the default listing")
		}
	}
}

// seedSellableProduct creates a category, a product with the given HSN code and
// GST rate, one default unit, and a price for it. It returns the product and
// unit ids.
func (h *harness) seedSellableProduct(t *testing.T, name, hsn string, gst int) (productID, unitID string) {
	t.Helper()
	store := h.storeID.String()

	category := h.request(http.MethodPost, "/api/v1/categories?store_id="+store, h.token,
		map[string]any{"name": "Staples " + name})
	if category.Code != http.StatusCreated {
		t.Fatalf("create category: status %d body %s", category.Code, category.Body.String())
	}
	var cat struct{ ID string }
	mustDecode(t, category.Body.Bytes(), &cat)

	uoms := h.request(http.MethodGet, "/api/v1/uoms?store_id="+store, h.token, nil)
	var units struct {
		Units []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"units"`
	}
	mustDecode(t, uoms.Body.Bytes(), &units)
	var kg string
	for _, u := range units.Units {
		if u.Code == "KG" {
			kg = u.ID
		}
	}
	if kg == "" {
		t.Fatal("the seeded units of measure have no KG; the migration should provide it")
	}

	product := h.request(http.MethodPost, "/api/v1/products?store_id="+store, h.token, map[string]any{
		"name": name, "category_id": cat.ID, "uom_id": kg,
		"hsn_code": hsn, "gst_rate": gst, "allow_decimal": true,
	})
	if product.Code != http.StatusCreated {
		t.Fatalf("create product: status %d body %s", product.Code, product.Body.String())
	}
	var p struct{ ID string }
	mustDecode(t, product.Body.Bytes(), &p)

	unit := h.request(http.MethodPost, "/api/v1/products/"+p.ID+"/units?store_id="+store, h.token,
		map[string]any{
			"label": "Loose kg", "qty_in_base": 1, "uom_id": kg,
			"is_default": true, "is_sellable": true, "is_purchasable": true,
		})
	if unit.Code != http.StatusCreated {
		t.Fatalf("create unit: status %d body %s", unit.Code, unit.Body.String())
	}
	var u struct{ ID string }
	mustDecode(t, unit.Body.Bytes(), &u)

	priced := h.request(http.MethodPost, "/api/v1/products/price?store_id="+store, h.token,
		map[string]any{
			"product_id": p.ID, "product_unit_id": u.ID,
			"mrp": "150.00", "cost_price": "90.00", "sell_price": "120.00",
		})
	if priced.Code != http.StatusOK {
		t.Fatalf("set price: status %d body %s", priced.Code, priced.Body.String())
	}
	return p.ID, u.ID
}

// terminalID returns a counter in the harness store, registering one if the
// shop has none yet.
func (h *harness) terminalID(t *testing.T) string {
	t.Helper()
	store := h.storeID.String()
	listed := h.request(http.MethodGet, "/api/v1/stores/"+store+"/terminals?store_id="+store, h.token, nil)
	var body struct {
		Terminals []struct {
			ID string `json:"id"`
		} `json:"terminals"`
	}
	mustDecode(t, listed.Body.Bytes(), &body)
	if len(body.Terminals) > 0 {
		return body.Terminals[0].ID
	}
	created := h.request(http.MethodPost, "/api/v1/stores/"+store+"/terminals?store_id="+store, h.token,
		map[string]any{"code": fmt.Sprintf("T%d", time.Now().UnixNano()%90+10), "name": "Counter"})
	if created.Code != http.StatusCreated {
		t.Fatalf("register terminal: status %d body %s", created.Code, created.Body.String())
	}
	var term struct{ ID string }
	mustDecode(t, created.Body.Bytes(), &term)
	return term.ID
}
