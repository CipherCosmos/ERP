package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A delivery of something perishable, the whole way through: order, receive
// with a date, and find it on the expiry report.
//
// This is the chain that was broken, and it was broken in the middle. The
// product field saying a product expires existed; the stock table's expiry
// column existed; the expiry report queried it correctly. The movement type
// between purchasing and stock had nowhere to put a date, so every batch that
// arrived through a purchase receipt carried none — and the expiry screen
// showed only the batches somebody had entered by hand.
//
// It is asserted over HTTP rather than in the purchasing service's own tests
// because those stop at a fake stock writer, and the adapter between the two is
// exactly where the date was being dropped.
func TestAPerishableDeliveryReachesTheExpiryReport(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	store := h.storeID.String()
	ist := time.FixedZone("IST", 5*3600+1800)
	today := time.Now().In(ist)

	product := h.seedPerishable(t, "Toned Milk 500 ml", "04012000", 3)

	vendor := h.request(http.MethodPost, "/api/v1/vendors?store_id="+store, h.token, map[string]any{
		"name": "Zz Dairy", "state": "Karnataka",
	})
	if vendor.Code != http.StatusCreated {
		t.Fatalf("create vendor: status %d body %s", vendor.Code, vendor.Body.String())
	}
	var v struct{ ID string }
	mustDecode(t, vendor.Body.Bytes(), &v)

	order := h.request(http.MethodPost, "/api/v1/purchases?store_id="+store, h.token, map[string]any{
		"vendor_id": v.ID, "terminal_id": h.terminalID(t),
		"order_date": today.Format("2006-01-02"),
		"items": []map[string]any{
			{"product_id": product, "unit_qty": 40, "rate": "30.00"},
		},
	})
	if order.Code != http.StatusCreated {
		t.Fatalf("raise order: status %d body %s", order.Code, order.Body.String())
	}
	// The response wraps the order alongside its lines.
	var po struct {
		PurchaseOrder struct{ ID string } `json:"purchase_order"`
	}
	mustDecode(t, order.Body.Bytes(), &po)

	// Receiving it with no date at all is refused, and the message names the
	// product because a receipt has many lines.
	bare := h.request(http.MethodPost, "/api/v1/purchases/receive?store_id="+store, h.token,
		map[string]any{
			"po_id": po.PurchaseOrder.ID, "received_date": today.Format("2006-01-02"),
			"items": []map[string]any{{"product_id": product, "received_qty": 40}},
		})
	if bare.Code < 400 {
		t.Fatalf("a perishable was received with no date: status %d body %s", bare.Code, bare.Body.String())
	}
	if body := bare.Body.String(); !containsAll(body, "Toned Milk 500 ml", "expiry date") {
		t.Errorf("refusal said %s, want it to name the product and what is missing", body)
	}

	// Packed yesterday, and the product's own three day shelf life does the
	// arithmetic.
	made := today.AddDate(0, 0, -1)
	received := h.request(http.MethodPost, "/api/v1/purchases/receive?store_id="+store, h.token,
		map[string]any{
			"po_id": po.PurchaseOrder.ID, "received_date": today.Format("2006-01-02"),
			"items": []map[string]any{{
				"product_id": product, "received_qty": 40,
				"mfg_date": made.Format("2006-01-02"),
			}},
		})
	if received.Code != http.StatusCreated && received.Code != http.StatusOK {
		t.Fatalf("receive: status %d body %s", received.Code, received.Body.String())
	}

	// And there it is on the report the whole thing exists to fill.
	expiring := h.request(http.MethodGet, "/api/v1/expiry/expiring?store_id="+store+"&days=7", h.token, nil)
	if expiring.Code != http.StatusOK {
		t.Fatalf("expiry report: status %d body %s", expiring.Code, expiring.Body.String())
	}
	var report struct {
		Batches []struct {
			ProductName     string `json:"product_name"`
			ExpiryDate      string `json:"expiry_date"`
			DaysUntilExpiry int    `json:"days_until_expiry"`
			Quantity        any    `json:"quantity"`
		} `json:"batches"`
	}
	mustDecode(t, expiring.Body.Bytes(), &report)

	var found bool
	for _, b := range report.Batches {
		if b.ProductName != "Toned Milk 500 ml" {
			continue
		}
		found = true
		// Packed yesterday plus three days is two days from today.
		if b.DaysUntilExpiry != 2 {
			t.Errorf("days until expiry = %d, want 2", b.DaysUntilExpiry)
		}
	}
	if !found {
		t.Fatalf("the delivery is not on the expiry report: %s", expiring.Body.String())
	}
}

func containsAll(body string, want ...string) bool {
	for _, w := range want {
		if !strings.Contains(body, w) {
			return false
		}
	}
	return true
}

// seedPerishable creates a product that says it expires, with a shelf life, and
// gives it a unit it can be bought and sold in.
func (h *harness) seedPerishable(t *testing.T, name, hsn string, shelfLifeDays int) string {
	t.Helper()
	store := h.storeID.String()

	category := h.request(http.MethodPost, "/api/v1/categories?store_id="+store, h.token,
		map[string]any{"name": "Chilled " + name})
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
	var nos string
	for _, u := range units.Units {
		if u.Code == "NOS" {
			nos = u.ID
		}
	}
	if nos == "" {
		t.Fatal("the seeded units of measure have no NOS")
	}

	product := h.request(http.MethodPost, "/api/v1/products?store_id="+store, h.token, map[string]any{
		"name": name, "category_id": cat.ID, "uom_id": nos,
		"hsn_code": hsn, "gst_rate": 5,
		"tracks_expiry": true, "shelf_life_days": shelfLifeDays,
	})
	if product.Code != http.StatusCreated {
		t.Fatalf("create product: status %d body %s", product.Code, product.Body.String())
	}
	var p struct {
		ID          string `json:"id"`
		DefaultUnit struct {
			ID string `json:"id"`
		} `json:"default_unit"`
	}
	mustDecode(t, product.Body.Bytes(), &p)

	priced := h.request(http.MethodPost, "/api/v1/products/price?store_id="+store, h.token,
		map[string]any{
			"product_id": p.ID, "product_unit_id": p.DefaultUnit.ID,
			"mrp": "36.00", "cost_price": "30.00", "sell_price": "35.00",
		})
	if priced.Code != http.StatusOK {
		t.Fatalf("set price: status %d body %s", priced.Code, priced.Body.String())
	}
	return p.ID
}
