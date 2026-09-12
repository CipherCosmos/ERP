package store_test

import (
	"context"
	"errors"
	"testing"

	domain "gbos/internal/domain/store"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/store"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

func newService(t *testing.T) *service.Service {
	t.Helper()
	db := testsupport.OpenDB(t)
	return service.New(db, repo.NewStore())
}

func TestStoreIsActiveAnswersTheScopingMiddleware(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	open, err := svc.Create(ctx, "Open Shop", "Main Road", "9990001111", "29ABCDE1234F1Z5", domain.LocationStore)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	active, err := svc.StoreIsActive(ctx, open.ID)
	if err != nil {
		t.Fatalf("check active: %v", err)
	}
	if !active {
		t.Error("a newly created store should be active")
	}

	if err := svc.SetActive(ctx, open.ID, false); err != nil {
		t.Fatalf("close store: %v", err)
	}
	active, err = svc.StoreIsActive(ctx, open.ID)
	if err != nil {
		t.Fatalf("check active after closing: %v", err)
	}
	if active {
		t.Error("a closed store should not be active")
	}

	// An unknown store is not an error — the answer to "may this owner act on
	// it" is simply no. Returning an error here would fail the request instead
	// of denying it.
	active, err = svc.StoreIsActive(ctx, uuid.New())
	if err != nil {
		t.Errorf("an unknown store should not produce an error, got %v", err)
	}
	if active {
		t.Error("an unknown store should not be active")
	}
}

func TestGSTINIsValidated(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	for _, bad := range []string{"NOTAGSTIN", "29ABCDE1234F1Q5", "12345", "29abcde1234f1z"} {
		if _, err := svc.Create(ctx, "Bad GSTIN Shop", "", "", bad, domain.LocationStore); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("GSTIN %q: expected ErrValidation, got %v", bad, err)
		}
	}

	// A shop below the registration threshold has no GSTIN, and must still be
	// able to use the system.
	if _, err := svc.Create(ctx, "Unregistered Shop", "", "", "", domain.LocationStore); err != nil {
		t.Errorf("a store with no GSTIN should be allowed: %v", err)
	}
}

func TestStateCodeDrivesTheTaxSplit(t *testing.T) {
	t.Parallel()
	s := domain.Store{GSTIN: "29ABCDE1234F1Z5"}
	if got := s.StateCode(); got != "29" {
		t.Errorf("state code = %q, want 29", got)
	}
	// An unregistered store has no state code; the caller treats that as
	// intra-state rather than guessing.
	if got := (domain.Store{}).StateCode(); got != "" {
		t.Errorf("state code of an unregistered store = %q, want empty", got)
	}
}

func TestTerminalCodesAreUniquePerStoreOnly(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	first, err := svc.Create(ctx, "Shop One", "", "", "", domain.LocationStore)
	if err != nil {
		t.Fatalf("create first store: %v", err)
	}
	second, err := svc.Create(ctx, "Shop Two", "", "", "", domain.LocationStore)
	if err != nil {
		t.Fatalf("create second store: %v", err)
	}

	// Both shops name their first counter T1. That must be allowed, because
	// each store runs its own invoice series.
	if _, err := svc.RegisterTerminal(ctx, first.ID, "T1", "Counter 1"); err != nil {
		t.Fatalf("register T1 at the first shop: %v", err)
	}
	if _, err := svc.RegisterTerminal(ctx, second.ID, "T1", "Counter 1"); err != nil {
		t.Fatalf("register T1 at the second shop: %v", err)
	}

	// Within one store the code must be unique, or two counters would issue
	// the same invoice numbers.
	if _, err := svc.RegisterTerminal(ctx, first.ID, "T1", "Counter 1 again"); !errors.Is(err, platform.ErrConflict) {
		t.Errorf("expected ErrConflict for a duplicate code in one store, got %v", err)
	}
}

func TestTerminalCodeMustBeUsableInAnInvoiceNumber(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	shop, err := svc.Create(ctx, "Shop", "", "", "", domain.LocationStore)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// A code with a separator in it would make INV/T-1/00001 ambiguous to read
	// and to parse back.
	for _, bad := range []string{"", "T 1", "T/1", "T-1", "TERMINAL-ONE"} {
		if _, err := svc.RegisterTerminal(ctx, shop.ID, bad, "Counter"); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("terminal code %q: expected ErrValidation, got %v", bad, err)
		}
	}
	if _, err := svc.RegisterTerminal(ctx, shop.ID, "t1", "Counter"); err != nil {
		t.Errorf("a lowercase code should be accepted and upper-cased: %v", err)
	}
}

func TestUpdateAndSetActiveReportMissingStores(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	if _, err := svc.Update(ctx, uuid.New(), "Ghost", "", "", "", domain.LocationStore); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("update of an unknown store: expected ErrNotFound, got %v", err)
	}
	if err := svc.SetActive(ctx, uuid.New(), false); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("closing an unknown store: expected ErrNotFound, got %v", err)
	}
}

func TestLocationTypeIsConstrained(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "Odd Place", "", "", "", domain.LocationType("factory")); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for an unknown location type, got %v", err)
	}

	// An unset type defaults to a shop rather than being rejected.
	created, err := svc.Create(ctx, "Default Place", "", "", "", "")
	if err != nil {
		t.Fatalf("create with no location type: %v", err)
	}
	if created.LocationType != domain.LocationStore {
		t.Errorf("location type = %q, want %q", created.LocationType, domain.LocationStore)
	}
}
