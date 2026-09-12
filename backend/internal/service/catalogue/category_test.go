package catalogue_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "gbos/internal/domain/catalogue"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/catalogue"

	"github.com/google/uuid"
)

// namedCategory creates a category, optionally under a parent, and fails the
// test rather than returning an error — every call below is setup, not the
// thing under test.
func namedCategory(t *testing.T, svc *service.Service, name string, parent *uuid.UUID) domain.Category {
	t.Helper()
	c, err := svc.CreateCategory(context.Background(), name, "", "", "1001", parent)
	if err != nil {
		t.Fatalf("create category %q: %v", name, err)
	}
	return c
}

func TestRenameCategoryKeepsItsPlace(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Grocery", nil)
	child := namedCategory(t, svc, "Attaa", &parent.ID)

	updated, err := svc.UpdateCategory(ctx, child.ID, domain.CategoryUpdate{
		Name:     "Atta",
		HSNCode:  "1101",
		ParentID: &parent.ID,
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.ID != child.ID {
		t.Errorf("id = %s, want the category that was edited (%s)", updated.ID, child.ID)
	}
	if updated.Name != "Atta" || updated.HSNCode != "1101" {
		t.Errorf("after rename = %q/%q, want Atta/1101", updated.Name, updated.HSNCode)
	}
	if updated.ParentName != "Grocery" {
		t.Errorf("parent name = %q, want Grocery", updated.ParentName)
	}
}

// A category moved to the top level is a move, not a missing field. Sending no
// parent must clear the one it had.
func TestClearingTheParentMakesItTopLevel(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Grocery", nil)
	child := namedCategory(t, svc, "Atta", &parent.ID)

	updated, err := svc.UpdateCategory(ctx, child.ID, domain.CategoryUpdate{Name: "Atta", HSNCode: "1101"})
	if err != nil {
		t.Fatalf("move to top level: %v", err)
	}
	if updated.ParentID != nil {
		t.Errorf("parent = %v, want none", *updated.ParentID)
	}
}

func TestCategoryCannotBeItsOwnParent(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)

	c := namedCategory(t, svc, "Grocery", nil)
	_, err := svc.UpdateCategory(context.Background(), c.ID,
		domain.CategoryUpdate{Name: "Grocery", HSNCode: "1001", ParentID: &c.ID})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("self-parent = %v, want a validation error", err)
	}
}

// The case a single-level check misses: A > B > C, and A is asked to move under
// C. Nothing about A's own row says that is wrong.
func TestCategoryCannotMoveInsideItsOwnSubtree(t *testing.T) {
	t.Parallel()
	svc, db, _, _ := newService(t)
	ctx := context.Background()

	a := namedCategory(t, svc, "Grocery", nil)
	b := namedCategory(t, svc, "Flour", &a.ID)
	c := namedCategory(t, svc, "Wheat Flour", &b.ID)

	_, err := svc.UpdateCategory(ctx, a.ID,
		domain.CategoryUpdate{Name: "Grocery", HSNCode: "1001", ParentID: &c.ID})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("moving a grandparent under its grandchild = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "Grocery") {
		t.Errorf("message = %q, want it to name the category being moved", err)
	}

	// And the refusal left the tree as it was.
	var parent *uuid.UUID
	if err := db.QueryRowContext(ctx, `SELECT parent_id FROM categories WHERE id = $1`, a.ID).Scan(&parent); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if parent != nil {
		t.Errorf("parent = %v, want the refused move to have changed nothing", *parent)
	}
}

func TestCategoryCannotBeCreatedUnderARetiredParent(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Seasonal", nil)
	if err := svc.SetCategoryActive(ctx, parent.ID, false); err != nil {
		t.Fatalf("retire the parent: %v", err)
	}

	_, err := svc.CreateCategory(ctx, "Diwali", "", "", "1001", &parent.ID)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("create under a retired parent = %v, want a validation error", err)
	}
}

func TestRetiringACategoryWithProductsIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	c := namedCategory(t, svc, "Grocery", nil)
	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(c.ID, uomID, "Wheat Flour 1kg", "8901001100101"),
		StoreID: storeID,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	err := svc.SetCategoryActive(ctx, c.ID, false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("retire a category in use = %v, want a validation error", err)
	}
	// The count is the useful part of the message: it tells the shopkeeper how
	// much work the remedy is.
	if !strings.Contains(err.Error(), "1 product") {
		t.Errorf("message = %q, want it to say how many products are in the way", err)
	}
}

func TestRetiringACategoryWithChildrenIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Grocery", nil)
	namedCategory(t, svc, "Flour", &parent.ID)

	err := svc.SetCategoryActive(ctx, parent.ID, false)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("retire a category with children = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "1 category") {
		t.Errorf("message = %q, want it to count the sub-categories", err)
	}
}

func TestReactivatingUnderARetiredParentIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Seasonal", nil)
	child := namedCategory(t, svc, "Diwali", &parent.ID)

	if err := svc.SetCategoryActive(ctx, child.ID, false); err != nil {
		t.Fatalf("retire the child: %v", err)
	}
	if err := svc.SetCategoryActive(ctx, parent.ID, false); err != nil {
		t.Fatalf("retire the parent: %v", err)
	}

	err := svc.SetCategoryActive(ctx, child.ID, true)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("reactivate under a retired parent = %v, want a validation error", err)
	}

	// Bring the parent back and the child can follow.
	if err := svc.SetCategoryActive(ctx, parent.ID, true); err != nil {
		t.Fatalf("reactivate the parent: %v", err)
	}
	if err := svc.SetCategoryActive(ctx, child.ID, true); err != nil {
		t.Fatalf("reactivate the child: %v", err)
	}
}

func TestMergeMovesProductsAndChildrenThenRetiresTheSource(t *testing.T) {
	t.Parallel()
	svc, db, storeID, uomID := newService(t)
	ctx := context.Background()

	// The duplicate a till operator makes: "Biscuit" beside "Biscuits".
	keep := namedCategory(t, svc, "Biscuits", nil)
	dupe := namedCategory(t, svc, "Biscuit", nil)
	orphan := namedCategory(t, svc, "Cream Biscuits", &dupe.ID)

	p, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(dupe.ID, uomID, "Marie 200g", "8901001100201"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}

	// A discontinued product has to move too. Leaving it behind would file it
	// under a category that no longer appears anywhere, which is the exact
	// orphaning the merge exists to prevent.
	withdrawn, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(dupe.ID, uomID, "Marie 100g", "8901001100202"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create the withdrawn product: %v", err)
	}
	if err := svc.SetProductActive(ctx, withdrawn.ID, false); err != nil {
		t.Fatalf("withdraw it: %v", err)
	}

	result, err := svc.MergeCategory(ctx, dupe.ID, keep.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.ProductsMoved != 2 || result.CategoriesMoved != 1 {
		t.Errorf("merge moved %d products and %d categories, want 2 and 1",
			result.ProductsMoved, result.CategoriesMoved)
	}
	if result.From != "Biscuit" || result.Into != "Biscuits" {
		t.Errorf("merge reported %q into %q, want Biscuit into Biscuits", result.From, result.Into)
	}

	var categoryID uuid.UUID
	if err := db.QueryRowContext(ctx, `SELECT category_id FROM products WHERE id = $1`, p.ID).Scan(&categoryID); err != nil {
		t.Fatalf("read the product back: %v", err)
	}
	if categoryID != keep.ID {
		t.Errorf("product sits in %s, want %s", categoryID, keep.ID)
	}

	moved, err := svc.GetCategory(ctx, orphan.ID)
	if err != nil {
		t.Fatalf("read the sub-category back: %v", err)
	}
	if moved.ParentID == nil || *moved.ParentID != keep.ID {
		t.Errorf("sub-category parent = %v, want %s", moved.ParentID, keep.ID)
	}

	gone, err := svc.GetCategory(ctx, dupe.ID)
	if err != nil {
		t.Fatalf("read the merged category back: %v", err)
	}
	if gone.IsActive {
		t.Error("the merged-away category is still active")
	}
}

// Merging a parent into its own child is the "I only ever used the sub-category"
// cleanup. It is the case that would loop if the child were not lifted out of
// the subtree first.
func TestMergeIntoOwnChildLiftsTheChildOut(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Beverages", nil)
	child := namedCategory(t, svc, "Soft Drinks", &parent.ID)
	sibling := namedCategory(t, svc, "Juices", &parent.ID)

	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(parent.ID, uomID, "Soda 600ml", "8901001100301"),
		StoreID: storeID,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	result, err := svc.MergeCategory(ctx, parent.ID, child.ID)
	if err != nil {
		t.Fatalf("merge a parent into its child: %v", err)
	}
	if !result.TargetWasInsideIt {
		t.Error("the merge did not report that the target was inside the source")
	}

	got, err := svc.GetCategory(ctx, child.ID)
	if err != nil {
		t.Fatalf("read the target back: %v", err)
	}
	if got.ParentID != nil {
		t.Errorf("target parent = %v, want it lifted to the top level", *got.ParentID)
	}
	if got.ProductCount != 1 {
		t.Errorf("target holds %d products, want the 1 that moved", got.ProductCount)
	}

	// The sibling came across with everything else, and is not its own ancestor.
	movedSibling, err := svc.GetCategory(ctx, sibling.ID)
	if err != nil {
		t.Fatalf("read the sibling back: %v", err)
	}
	if movedSibling.ParentID == nil || *movedSibling.ParentID != child.ID {
		t.Errorf("sibling parent = %v, want %s", movedSibling.ParentID, child.ID)
	}
}

func TestMergeRefusesItselfAndRetiredTargets(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	a := namedCategory(t, svc, "Grocery", nil)
	b := namedCategory(t, svc, "Snacks", nil)

	if _, err := svc.MergeCategory(ctx, a.ID, a.ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("merge into itself = %v, want a validation error", err)
	}
	if err := svc.SetCategoryActive(ctx, b.ID, false); err != nil {
		t.Fatalf("retire the target: %v", err)
	}
	if _, err := svc.MergeCategory(ctx, a.ID, b.ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("merge into a retired category = %v, want a validation error", err)
	}
	if _, err := svc.MergeCategory(ctx, a.ID, uuid.New()); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("merge into a category that does not exist = %v, want not found", err)
	}
}

// The listing is what the screen draws, so the counts on it have to be right:
// active products only, and the parent's name alongside so a tree renders
// without a second query per row.
func TestListCategoriesCountsOnlyLiveProducts(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	parent := namedCategory(t, svc, "Zz Parent", nil)
	child := namedCategory(t, svc, "Zz Child", &parent.ID)

	live, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(child.ID, uomID, "Kept 1kg", "8901001100401"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	retired, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(child.ID, uomID, "Discontinued 1kg", "8901001100402"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create the second product: %v", err)
	}
	if err := svc.SetProductActive(ctx, retired.ID, false); err != nil {
		t.Fatalf("retire the second product: %v", err)
	}
	_ = live

	all, err := svc.ListCategories(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got domain.Category
	for _, c := range all {
		if c.ID == child.ID {
			got = c
		}
	}
	if got.ID == uuid.Nil {
		t.Fatal("the child category is missing from the listing")
	}
	if got.ProductCount != 1 {
		t.Errorf("product count = %d, want 1 — a discontinued product is not something the shop still sells", got.ProductCount)
	}
	// But it is still filed here, and a merge would move it, so the screen that
	// offers the merge has to be able to say how many.
	if got.RetiredProductCount != 1 {
		t.Errorf("retired product count = %d, want 1", got.RetiredProductCount)
	}
	if got.ParentName != "Zz Parent" {
		t.Errorf("parent name = %q, want Zz Parent", got.ParentName)
	}
}

// A retired category has to stay reachable, or there is no way to bring it back
// and no way to see where its products went.
func TestRetiredCategoriesAreListedOnRequest(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	ctx := context.Background()

	c := namedCategory(t, svc, "Zz Retired", nil)
	if err := svc.SetCategoryActive(ctx, c.ID, false); err != nil {
		t.Fatalf("retire: %v", err)
	}

	for _, tc := range []struct {
		includeInactive bool
		want            bool
	}{{false, false}, {true, true}} {
		list, err := svc.ListCategories(ctx, tc.includeInactive)
		if err != nil {
			t.Fatalf("list(include_inactive=%v): %v", tc.includeInactive, err)
		}
		found := false
		for _, got := range list {
			if got.ID == c.ID {
				found = true
			}
		}
		if found != tc.want {
			t.Errorf("list(include_inactive=%v) contains the retired category = %v, want %v",
				tc.includeInactive, found, tc.want)
		}
	}
}

// A product's name inside its category identifies it.
//
// This is not tidiness. The catalogue import matches a sheet row to a stored
// product by name within category — which is how most rows match, because a
// shop's own sheet rarely carries a barcode — and the lookup takes the first
// row it finds. Two products sharing a name make that arbitrary: which of them
// a price revision lands on is decided by the planner. The export-edit-import
// loop a shop uses to revise prices is only sound if the pair names one thing.
func TestTwoProductsCannotShareANameInOneCategory(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	grocery := namedCategory(t, svc, "Zz Dup Grocery", nil)
	snacks := namedCategory(t, svc, "Zz Dup Snacks", nil)

	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(grocery.ID, uomID, "Marie Gold", "8901055100001"),
		StoreID: storeID,
	}); err != nil {
		t.Fatalf("first product: %v", err)
	}

	// Case only. The easy one to make and the hard one to see.
	_, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(grocery.ID, uomID, "marie gold", "8901055100002"),
		StoreID: storeID,
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a name differing only in case = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "Zz Dup Grocery") || !strings.Contains(err.Error(), "Marie Gold") {
		t.Errorf("message = %q, want it to name the category and what is already there", err)
	}

	// The same name in a different category is a different product and is fine:
	// the pair is the identity, not the name on its own.
	if _, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(snacks.ID, uomID, "Marie Gold", "8901055100003"),
		StoreID: storeID,
	}); err != nil {
		t.Fatalf("the same name in another category: %v", err)
	}
}

func TestAProductCannotBeRenamedOntoAnother(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	category := namedCategory(t, svc, "Zz Rename", nil)
	first, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(category.ID, uomID, "Zz Atta", "8901055100011"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(category.ID, uomID, "Zz Maida", "8901055100012"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	onto := productInput(category.ID, uomID, "Zz Atta", "8901055100012")
	if _, err := svc.UpdateProduct(ctx, second.ID, onto); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("renaming onto another product = %v, want a validation error", err)
	}

	// Renaming a product to what it is already called is not a collision with
	// itself.
	same := productInput(category.ID, uomID, "Zz Atta", "8901055100011")
	if _, err := svc.UpdateProduct(ctx, first.ID, same); err != nil {
		t.Fatalf("saving a product under its own name: %v", err)
	}
}

// A retired product does not hold its name, so a shop can reuse one it stopped
// selling. That means bringing the old one back can collide, and the refusal
// has to be a sentence rather than an index name.
func TestBringingBackAProductWhoseNameWasReusedIsRefused(t *testing.T) {
	t.Parallel()
	svc, _, storeID, uomID := newService(t)
	ctx := context.Background()

	category := namedCategory(t, svc, "Zz Reuse", nil)
	old, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(category.ID, uomID, "Zz Seasonal Item", "8901055100021"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.SetProductActive(ctx, old.ID, false); err != nil {
		t.Fatalf("retire: %v", err)
	}

	// The name is free again.
	replacement, err := svc.CreateProduct(ctx, service.CreateProductInput{
		Product: productInput(category.ID, uomID, "Zz Seasonal Item", "8901055100022"),
		StoreID: storeID,
	})
	if err != nil {
		t.Fatalf("reuse the name of a retired product: %v", err)
	}

	err = svc.SetProductActive(ctx, old.ID, true)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("bring back a product whose name was reused = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "Zz Seasonal Item") {
		t.Errorf("message = %q, want it to name what took the name", err)
	}

	// Rename the replacement and the original can come back.
	if _, err := svc.UpdateProduct(ctx, replacement.ID,
		productInput(category.ID, uomID, "Zz Seasonal Item 2024", "8901055100022")); err != nil {
		t.Fatalf("rename the replacement: %v", err)
	}
	if err := svc.SetProductActive(ctx, old.ID, true); err != nil {
		t.Fatalf("bring the original back: %v", err)
	}
}
