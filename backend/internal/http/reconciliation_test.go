package http_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	domain "gbos/internal/domain/user"
)

// TestAnOfflineSaleRoundTripsAndRaisesItsException drives the whole offline path
// over HTTP, the way a till does when the connection comes back: a sale the till
// numbered itself, dated when the customer was served, for more of the goods than
// the shelf knows about.
//
// It is the shape of the rule that offline billing rests on. The sale lands, the
// customer's bill is for what they took, the shelf cannot go negative, and
// somebody is told to count.
func TestAnOfflineSaleRoundTripsAndRaisesItsException(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	store := h.storeID.String()

	product, unit := h.seedSellableProduct(t, "Basmati Rice", "10063020", 5)
	terminal := h.terminalID(t)
	h.putOnShelf(t, product, unit, "3", "90.00")

	// The till numbered this itself while it could not reach the server, and
	// served the customer two hours ago.
	served := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	number := "INV/" + h.terminalCode(t, terminal) + "/" + financialYear() + "/00001"

	sold := h.request(http.MethodPost, "/api/v1/invoices?store_id="+store, h.token, map[string]any{
		"terminal_id":    terminal,
		"payment_mode":   "cash",
		"invoice_no":     number,
		"billed_offline": true,
		"billed_at":      served,
		"items": []map[string]any{{
			"product_id": product, "product_unit_id": unit, "unit_qty": 5,
		}},
	})
	if sold.Code != http.StatusCreated {
		t.Fatalf("an offline sale was refused on sync: status %d body %s", sold.Code, sold.Body.String())
	}
	var result struct {
		Invoice struct {
			ID            string      `json:"id"`
			InvoiceNo     string      `json:"invoice_no"`
			SubTotal      json.Number `json:"sub_total"`
			BilledOffline bool        `json:"billed_offline"`
		} `json:"invoice"`
	}
	mustDecode(t, sold.Body.Bytes(), &result)

	if result.Invoice.InvoiceNo != number {
		t.Errorf("invoice number = %s, want the till's %s", result.Invoice.InvoiceNo, number)
	}
	if !result.Invoice.BilledOffline {
		t.Error("the invoice does not record that the till was offline")
	}
	// The customer took five kilos at Rs.120 and paid for five.
	if result.Invoice.SubTotal.String() != "600.00" {
		t.Errorf("sub total = %s, want 600.00", result.Invoice.SubTotal)
	}

	// The shelf floors at zero rather than going negative: it cannot hold minus
	// two kilos of rice.
	stock := h.request(http.MethodGet, "/api/v1/stock/product/"+product+"?store_id="+store, h.token, nil)
	var batches struct {
		Batches []struct {
			Quantity json.Number `json:"quantity"`
		} `json:"batches"`
	}
	mustDecode(t, stock.Body.Bytes(), &batches)
	for _, b := range batches.Batches {
		if b.Quantity.String() != "0.000" {
			t.Errorf("on hand = %s, want 0.000", b.Quantity)
		}
	}

	// And somebody is told to count.
	listed := h.request(http.MethodGet, "/api/v1/reconciliation?store_id="+store, h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("read the reconciliation list: status %d body %s", listed.Code, listed.Body.String())
	}
	var open struct {
		Exceptions []struct {
			ID          string  `json:"id"`
			Kind        string  `json:"kind"`
			Detail      string  `json:"detail"`
			Expected    string  `json:"expected"`
			Actual      string  `json:"actual"`
			InvoiceNo   string  `json:"invoice_no"`
			ProductName string  `json:"product_name"`
			ResolvedAt  *string `json:"resolved_at"`
		} `json:"exceptions"`
	}
	mustDecode(t, listed.Body.Bytes(), &open)
	if len(open.Exceptions) != 1 {
		t.Fatalf("%d exceptions, want 1: %+v", len(open.Exceptions), open.Exceptions)
	}
	e := open.Exceptions[0]
	if e.Kind != "oversold" {
		t.Errorf("kind = %s, want oversold", e.Kind)
	}
	// The screen has to be able to name what it is talking about without a
	// lookup per row.
	if e.InvoiceNo != number || e.ProductName != "Basmati Rice" {
		t.Errorf("the exception does not name its subject: invoice %q product %q", e.InvoiceNo, e.ProductName)
	}
	if e.Detail == "" || e.Expected == "" || e.Actual == "" {
		t.Errorf("the exception does not say what happened or what to do: %+v", e)
	}

	// --- resolving it ---------------------------------------------------------
	resolved := h.request(http.MethodPost, "/api/v1/reconciliation/"+e.ID+"/resolve?store_id="+store,
		h.token, map[string]any{"note": "counted the shelf, two kilos short as expected"})
	if resolved.Code != http.StatusOK {
		t.Fatalf("resolve: status %d body %s", resolved.Code, resolved.Body.String())
	}

	// Resolving twice tells the second person rather than silently doing
	// nothing: they believe they dealt with it, and their note is not stored.
	again := h.request(http.MethodPost, "/api/v1/reconciliation/"+e.ID+"/resolve?store_id="+store,
		h.token, map[string]any{"note": "also me"})
	if again.Code != http.StatusConflict {
		t.Errorf("resolving twice: status %d, want 409 — body %s", again.Code, again.Body.String())
	}

	// It leaves the working list but is not deleted: how many of these a shop
	// raises in a month is the signal that its offline window is too long.
	listed = h.request(http.MethodGet, "/api/v1/reconciliation?store_id="+store, h.token, nil)
	mustDecode(t, listed.Body.Bytes(), &open)
	if len(open.Exceptions) != 0 {
		t.Errorf("%d exceptions still open after resolving", len(open.Exceptions))
	}
	listed = h.request(http.MethodGet, "/api/v1/reconciliation?resolved=true&store_id="+store, h.token, nil)
	mustDecode(t, listed.Body.Bytes(), &open)
	if len(open.Exceptions) != 1 || open.Exceptions[0].ResolvedAt == nil {
		t.Errorf("a resolved exception should still be readable: %+v", open.Exceptions)
	}
}

// TestReconciliationIsNotACashiersJob: an exception asks somebody to count a
// shelf or decide about a debt. Neither is work a cashier does.
func TestReconciliationIsNotACashiersJob(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cashier := h.tokenFor(domain.RoleCashier)

	got := h.request(http.MethodGet, "/api/v1/reconciliation?store_id="+h.storeID.String(), cashier, nil)
	if got.Code != http.StatusForbidden {
		t.Errorf("a cashier reading the reconciliation list: status %d, want 403", got.Code)
	}
}

// financialYear is the Indian financial year now, in the form the numbers use.
func financialYear() string {
	now := time.Now().In(time.FixedZone("IST", 5*3600+1800))
	year := now.Year()
	if now.Month() < time.April {
		year--
	}
	return fmtYear(year)
}

func fmtYear(y int) string {
	return string(rune('0'+y/1000%10)) + string(rune('0'+y/100%10)) +
		string(rune('0'+y/10%10)) + string(rune('0'+y%10)) + "-" +
		string(rune('0'+(y+1)/10%10)) + string(rune('0'+(y+1)%10))
}
