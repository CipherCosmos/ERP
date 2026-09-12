package http_test

import (
	"encoding/json"
	"net/http"
	"testing"

	domain "gbos/internal/domain/user"
)

// TestStoresAndTerminalsRoundTripThroughHTTP drives the shop administration
// endpoints the way the settings screen does: open a shop, correct its details,
// register the counter it bills from, retire that counter, close the shop.
//
// Terminals matter more than they look. A terminal's code is a segment of every
// invoice number the counter issues — INV/T1/2026-27/00001 — so registering one
// is what makes a till able to bill at all, and the series it owns is what makes
// gap-free numbering possible without the counters coordinating.
func TestStoresAndTerminalsRoundTripThroughHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// --- open a shop ---------------------------------------------------------
	created := h.request(http.MethodPost, "/api/v1/stores", h.token, map[string]any{
		"name":          "Jayanagar Branch",
		"address":       "4th Block, Bangalore",
		"phone":         "9000000001",
		"gstin":         "29AABCU9603R1ZX",
		"location_type": "store",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create store: status %d body %s", created.Code, created.Body.String())
	}
	var shop struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		GSTIN        string `json:"gstin"`
		LocationType string `json:"location_type"`
		IsActive     bool   `json:"is_active"`
	}
	mustDecode(t, created.Body.Bytes(), &shop)
	if shop.ID == "" || !shop.IsActive || shop.LocationType != "store" {
		t.Fatalf("created store reads back wrong: %+v", shop)
	}

	// --- a malformed GSTIN is refused, with the reason ------------------------
	bad := h.request(http.MethodPost, "/api/v1/stores", h.token, map[string]any{
		"name": "Bad GSTIN", "gstin": "NOT-A-GSTIN",
	})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Errorf("a malformed GSTIN: status %d, want 422 — body %s", bad.Code, bad.Body.String())
	}
	// A shop below the registration threshold has none, and refusing that would
	// stop it using the system at all.
	noGSTIN := h.request(http.MethodPost, "/api/v1/stores", h.token, map[string]any{"name": "Small Shop"})
	if noGSTIN.Code != http.StatusCreated {
		t.Errorf("a shop with no GSTIN: status %d, want 201 — body %s", noGSTIN.Code, noGSTIN.Body.String())
	}

	// --- read it back and correct it ----------------------------------------
	got := h.request(http.MethodGet, "/api/v1/stores/"+shop.ID+"?store_id="+shop.ID, h.token, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get store: status %d body %s", got.Code, got.Body.String())
	}

	updated := h.request(http.MethodPut, "/api/v1/stores/"+shop.ID+"?store_id="+shop.ID, h.token,
		map[string]any{"name": "Jayanagar Main", "phone": "9000000002", "gstin": "29AABCU9603R1ZX"})
	if updated.Code != http.StatusOK {
		t.Fatalf("update store: status %d body %s", updated.Code, updated.Body.String())
	}
	got = h.request(http.MethodGet, "/api/v1/stores/"+shop.ID+"?store_id="+shop.ID, h.token, nil)
	mustDecode(t, got.Body.Bytes(), &shop)
	if shop.Name != "Jayanagar Main" {
		t.Errorf("name = %q after the update, want Jayanagar Main", shop.Name)
	}

	// --- the path never widens what a caller may reach -----------------------
	//
	// The id in the path is intent, not authorisation: it is compared with the
	// store the middleware resolved. Naming a different one is refused with the
	// same words RequireStore uses, so a caller cannot learn which shops exist
	// by watching the refusals differ.
	crossed := h.request(http.MethodGet,
		"/api/v1/stores/"+shop.ID+"?store_id="+h.storeID.String(), h.token, nil)
	if crossed.Code != http.StatusForbidden {
		t.Errorf("a path naming another store: status %d, want 403 — body %s",
			crossed.Code, crossed.Body.String())
	}

	// --- register the counter the shop bills from ---------------------------
	terminal := h.request(http.MethodPost, "/api/v1/stores/"+shop.ID+"/terminals?store_id="+shop.ID,
		h.token, map[string]any{"code": "T2", "name": "Counter 2"})
	if terminal.Code != http.StatusCreated {
		t.Fatalf("register terminal: status %d body %s", terminal.Code, terminal.Body.String())
	}
	var counter struct {
		ID       string `json:"id"`
		Code     string `json:"code"`
		IsActive bool   `json:"is_active"`
	}
	mustDecode(t, terminal.Body.Bytes(), &counter)
	if counter.Code != "T2" || !counter.IsActive {
		t.Fatalf("terminal reads back wrong: %+v", counter)
	}

	// A code is unique within its shop: two counters sharing one would share an
	// invoice series, and the series is what has to be consecutive.
	dup := h.request(http.MethodPost, "/api/v1/stores/"+shop.ID+"/terminals?store_id="+shop.ID,
		h.token, map[string]any{"code": "T2", "name": "Counter 2 again"})
	if dup.Code != http.StatusConflict {
		t.Errorf("a duplicate terminal code: status %d, want 409 — body %s", dup.Code, dup.Body.String())
	}

	listed := h.request(http.MethodGet, "/api/v1/stores/"+shop.ID+"/terminals?store_id="+shop.ID, h.token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list terminals: status %d body %s", listed.Code, listed.Body.String())
	}
	var terminals struct {
		Terminals []struct {
			ID       string `json:"id"`
			Code     string `json:"code"`
			IsActive bool   `json:"is_active"`
		} `json:"terminals"`
	}
	mustDecode(t, listed.Body.Bytes(), &terminals)
	if len(terminals.Terminals) != 1 || terminals.Terminals[0].Code != "T2" {
		t.Fatalf("terminals = %+v, want the one just registered", terminals.Terminals)
	}

	// --- retire the counter, then close the shop -----------------------------
	off := h.request(http.MethodPost,
		"/api/v1/stores/"+shop.ID+"/terminals/"+counter.ID+"/deactivate?store_id="+shop.ID, h.token, nil)
	if off.Code != http.StatusOK {
		t.Fatalf("deactivate terminal: status %d body %s", off.Code, off.Body.String())
	}

	// A terminal belonging to another shop cannot be retired through this shop's
	// path, whatever the id says.
	foreign := h.request(http.MethodPost,
		"/api/v1/stores/"+shop.ID+"/terminals/00000000-0000-0000-0000-000000000000/deactivate?store_id="+shop.ID,
		h.token, nil)
	if foreign.Code != http.StatusNotFound {
		t.Errorf("retiring an unknown terminal: status %d, want 404 — body %s",
			foreign.Code, foreign.Body.String())
	}

	closed := h.request(http.MethodPost, "/api/v1/stores/"+shop.ID+"/deactivate?store_id="+shop.ID, h.token, nil)
	if closed.Code != http.StatusOK {
		t.Fatalf("deactivate store: status %d body %s", closed.Code, closed.Body.String())
	}
}

// TestOnlyAnOwnerSeesEveryShop pins what the store listing shows to whom.
//
// Everybody but an owner sees the one shop their session was issued for. Listing
// the rest leaks the group's shape — names, addresses and GSTINs of shops they
// cannot read a single figure from — and RequireStore would refuse them any of
// those stores anyway, so the listing would only be a catalogue of refusals.
func TestOnlyAnOwnerSeesEveryShop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for _, name := range []string{"Second Shop", "Third Shop"} {
		got := h.request(http.MethodPost, "/api/v1/stores", h.token, map[string]any{"name": name})
		if got.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d body %s", name, got.Code, got.Body.String())
		}
	}

	count := func(token string) int {
		t.Helper()
		got := h.request(http.MethodGet, "/api/v1/stores", token, nil)
		if got.Code != http.StatusOK {
			t.Fatalf("list stores: status %d body %s", got.Code, got.Body.String())
		}
		var body struct {
			Stores []json.RawMessage `json:"stores"`
		}
		mustDecode(t, got.Body.Bytes(), &body)
		return len(body.Stores)
	}

	if got := count(h.token); got < 3 {
		t.Errorf("the owner sees %d shops, want all three", got)
	}
	if got := count(h.tokenFor(domain.RoleManager)); got != 1 {
		t.Errorf("a manager sees %d shops, want only their own", got)
	}
}
