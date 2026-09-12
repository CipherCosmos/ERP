package catalogue_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	service "gbos/internal/service/catalogue"
)

// newFamilyWithUnits makes a family and gives it a base unit plus, optionally,
// a second member measured against it.
func newFamilyWithUnits(t *testing.T, svc *service.Service, code string, withSibling bool) (domain.Family, domain.UnitOfMeasure, domain.UnitOfMeasure) {
	t.Helper()
	ctx := context.Background()

	family, err := svc.CreateFamily(ctx, code, "Zz "+code)
	if err != nil {
		t.Fatalf("create family %s: %v", code, err)
	}
	base, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: code + "B", Name: "Zz base " + code, DecimalPlaces: 3,
		FamilyID: family.ID, FactorToBase: money.RateFromInt(1),
	})
	if err != nil {
		t.Fatalf("create base unit: %v", err)
	}
	var sibling domain.UnitOfMeasure
	if withSibling {
		sibling, err = svc.CreateUnit(ctx, service.CreateUnitInput{
			Code: code + "S", Name: "Zz small " + code, DecimalPlaces: 3,
			FamilyID: family.ID, FactorToBase: money.MustRateFromString("0.001"),
		})
		if err != nil {
			t.Fatalf("create sibling unit: %v", err)
		}
	}
	return family, base, sibling
}

// The first unit in a family becomes its canonical one, because a family's
// canonical unit is one of its own members and none exists when it is made.
func TestTheFirstUnitBecomesTheFamilysBase(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)

	family, base, _ := newFamilyWithUnits(t, svc, "ZZA", false)
	families, err := svc.ListFamilies(context.Background(), false)
	if err != nil {
		t.Fatalf("list families: %v", err)
	}
	var got domain.Family
	for _, f := range families {
		if f.ID == family.ID {
			got = f
		}
	}
	if got.BaseUOMID != base.ID {
		t.Errorf("base unit = %s, want the first unit created (%s)", got.BaseUOMID, base.ID)
	}
	// The read model carries what the screen renders, so a list of families
	// needs neither a lookup for the base unit's code nor a count per row.
	if got.BaseUOMCode != base.Code {
		t.Errorf("base unit code = %q, want %q", got.BaseUOMCode, base.Code)
	}
	if got.UnitCount != 1 {
		t.Errorf("unit count = %d, want 1", got.UnitCount)
	}
}

// Retiring the unit a family converts through is not removing one row, it is
// removing the anchor every other member's factor is expressed against.
func TestTheFamilysBaseUnitCannotBeRetiredWhileOthersUseIt(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	_, base, sibling := newFamilyWithUnits(t, svc, "ZZB", true)

	err := svc.SetUnitActive(ctx, base.ID, false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("retire the base unit = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "convert through") {
		t.Errorf("message = %q, want it to say what the unit is for", err)
	}

	// Retire the sibling and the base is free: with nothing left to convert,
	// being the anchor costs nothing.
	if err := svc.SetUnitActive(ctx, sibling.ID, false); err != nil {
		t.Fatalf("retire the sibling: %v", err)
	}
	if err := svc.SetUnitActive(ctx, base.ID, false); err != nil {
		t.Fatalf("retire the base once nothing converts through it: %v", err)
	}
}

// A product stocked in a unit is the commonest reason a unit cannot go. The
// screens resolve the code from the active list, so retiring it does not fall
// back to anything — the shelf figure loses its unit.
func TestAUnitInUseByAProductCannotBeRetired(t *testing.T) {
	t.Parallel()
	svc, _, storeID, _ := newService(t)
	ctx := context.Background()

	_, base, sibling := newFamilyWithUnits(t, svc, "ZZC", true)
	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(newCategory(t, svc), sibling.ID, "Zz Unit User", "8901099200001"),
		StoreID: storeID,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	err := svc.SetUnitActive(ctx, sibling.ID, false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("retire a unit in use = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "1 product is stocked") {
		t.Errorf("message = %q, want it to count what is in the way", err)
	}
	_ = base
}

func TestARetiredFamilyCannotHoldALiveUnit(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	family, base, _ := newFamilyWithUnits(t, svc, "ZZD", false)

	err := svc.SetFamilyActive(ctx, family.ID, false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("retire a family with units = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "1 unit") {
		t.Errorf("message = %q, want it to count the units", err)
	}

	// Retire the unit and the family follows.
	if err := svc.SetUnitActive(ctx, base.ID, false); err != nil {
		t.Fatalf("retire the unit: %v", err)
	}
	if err := svc.SetFamilyActive(ctx, family.ID, false); err != nil {
		t.Fatalf("retire the empty family: %v", err)
	}

	// And a unit cannot be brought back under a retired family: it would be
	// usable and invisible, which looks like the reactivation having failed.
	err = svc.SetUnitActive(ctx, base.ID, true)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("reactivate under a retired family = %v, want a validation error", err)
	}
	if err := svc.SetFamilyActive(ctx, family.ID, true); err != nil {
		t.Fatalf("bring the family back: %v", err)
	}
	if err := svc.SetUnitActive(ctx, base.ID, true); err != nil {
		t.Fatalf("bring the unit back: %v", err)
	}
}

// Units convert only within a family, which is what makes selling rice by the
// litre impossible rather than merely discouraged.
func TestUnitsConvertOnlyWithinTheirFamily(t *testing.T) {
	t.Parallel()
	svc, db, _, _ := newService(t)
	ctx := context.Background()

	all, err := svc.ListUnits(ctx, domain.UnitFilter{})
	if err != nil {
		t.Fatalf("list units: %v", err)
	}
	byCode := func(code string) domain.UnitOfMeasure {
		t.Helper()
		for _, u := range all {
			if u.Code == code {
				return u
			}
		}
		t.Fatalf("the migration did not seed %s", code)
		return domain.UnitOfMeasure{}
	}
	kg, g, litre := byCode("KG"), byCode("G"), byCode("L")
	_ = db

	half, err := domain.ConvertWithin(money.MustQuantityFromString("500"), g, kg)
	if err != nil {
		t.Fatalf("grams to kilograms: %v", err)
	}
	if !half.Equal(money.MustQuantityFromString("0.5")) {
		t.Errorf("500 g = %s kg, want 0.5", half)
	}
	if _, err := domain.ConvertWithin(money.QuantityFromInt(1), kg, litre); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("kilograms to litres = %v, want a refusal", err)
	}
}

// Retiring every unit in a family leaves its base pointer aimed at a retired
// row. A family brought back into use would then say it converts through a unit
// nothing can see or offer, and the next unit added would state its size against
// one nobody can pick.
func TestAFamilyAdoptsANewBaseWhenTheOldOneIsRetired(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	family, first, _ := newFamilyWithUnits(t, svc, "ZZE", false)
	if err := svc.SetUnitActive(ctx, first.ID, false); err != nil {
		t.Fatalf("retire the only unit: %v", err)
	}

	second, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "ZZE2", Name: "Zz replacement", DecimalPlaces: 3,
		FamilyID: family.ID, FactorToBase: money.RateFromInt(1),
	})
	if err != nil {
		t.Fatalf("add a replacement unit: %v", err)
	}

	families, err := svc.ListFamilies(ctx, true)
	if err != nil {
		t.Fatalf("list families: %v", err)
	}
	var got domain.Family
	for _, f := range families {
		if f.ID == family.ID {
			got = f
		}
	}
	if got.BaseUOMID != second.ID {
		t.Errorf("base unit = %s, want the live replacement (%s) rather than the retired one (%s)",
			got.BaseUOMID, second.ID, first.ID)
	}
	if got.BaseUOMCode != "ZZE2" {
		t.Errorf("base unit code = %q, want ZZE2", got.BaseUOMCode)
	}
}

// And a family with a live base keeps it. Adopting on every factor-one unit
// would silently move the anchor the moment somebody added a second unit of the
// same size.
func TestAFamilyKeepsALiveBaseUnit(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	family, base, _ := newFamilyWithUnits(t, svc, "ZZF", false)
	if _, err := svc.CreateUnit(ctx, service.CreateUnitInput{
		Code: "ZZF2", Name: "Zz same size", DecimalPlaces: 3,
		FamilyID: family.ID, FactorToBase: money.RateFromInt(1),
	}); err != nil {
		t.Fatalf("add a second factor-one unit: %v", err)
	}

	families, err := svc.ListFamilies(ctx, false)
	if err != nil {
		t.Fatalf("list families: %v", err)
	}
	for _, f := range families {
		if f.ID == family.ID && f.BaseUOMID != base.ID {
			t.Errorf("base unit moved to %s; it was %s", f.BaseUOMID, base.ID)
		}
	}
}
