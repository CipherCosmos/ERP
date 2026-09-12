// Package catalogue is the service layer for the product catalogue: it owns
// transactions, enforces the rules that span more than one row, and is the
// only place that decides what a caller is allowed to do.
//
// The Repository interface is declared here, by the consumer, so that this
// package states what it needs rather than accepting whatever a database
// package happens to offer.
package catalogue

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when an operation that is inherently
// store-scoped is called without one.
var ErrStoreRequired = errors.New("store is required")

// Repository is the persistence this service needs. Every method takes a
// Querier so the service decides whether it runs in a transaction.
type Repository interface {
	ListCategories(ctx context.Context, q platform.Querier, includeInactive bool) ([]domain.Category, error)
	GetCategory(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Category, error)
	CreateCategory(ctx context.Context, q platform.Querier, c domain.Category) error
	UpdateCategory(ctx context.Context, q platform.Querier, c domain.Category) (bool, error)
	SetCategoryActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)
	LockCategoryTree(ctx context.Context, q platform.Querier) error
	CategoryWouldLoop(ctx context.Context, q platform.Querier, id, parentID uuid.UUID) (bool, error)
	MoveProductsToCategory(ctx context.Context, q platform.Querier, from, to uuid.UUID) (int64, error)
	MoveChildCategories(ctx context.Context, q platform.Querier, from, to uuid.UUID) (int64, error)

	ListFamilies(ctx context.Context, q platform.Querier, includeInactive bool) ([]domain.Family, error)
	GetFamily(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Family, error)
	CreateFamily(ctx context.Context, q platform.Querier, f domain.Family) error
	UpdateFamily(ctx context.Context, q platform.Querier, f domain.Family) (bool, error)
	SetFamilyBaseUnit(ctx context.Context, q platform.Querier, familyID, uomID uuid.UUID) (bool, error)
	SetFamilyActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)

	ListUnits(ctx context.Context, q platform.Querier, f domain.UnitFilter) ([]domain.UnitOfMeasure, error)
	GetUnit(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.UnitOfMeasure, error)
	CreateUnit(ctx context.Context, q platform.Querier, u domain.UnitOfMeasure) error
	UpdateUnit(ctx context.Context, q platform.Querier, u domain.UnitOfMeasure) (bool, error)
	SetUnitActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)

	ListProducts(ctx context.Context, q platform.Querier, f domain.ProductFilter) ([]domain.PricedProduct, error)
	GetProduct(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (domain.PricedProduct, error)
	FindByBarcode(ctx context.Context, q platform.Querier, barcode string, storeID uuid.UUID) (domain.ScannedProduct, error)
	CreateProduct(ctx context.Context, q platform.Querier, p domain.Product) error
	UpdateProduct(ctx context.Context, q platform.Querier, p domain.Product) (bool, error)
	SetProductActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)
	ProductExists(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error)

	UpsertPrice(ctx context.Context, q platform.Querier, p domain.UnitPrice, by domain.PriceAuthor) error
	PriceHistory(ctx context.Context, q platform.Querier, productID, storeID, productUnitID uuid.UUID, limit int) ([]domain.PriceChange, error)
	ProductPriceHistory(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, limit int) ([]domain.PriceChange, error)
	RecentPriceChanges(ctx context.Context, q platform.Querier, storeID uuid.UUID, since time.Time, limit int) ([]domain.PriceChange, error)
	GetPrice(ctx context.Context, q platform.Querier, productID, storeID, productUnitID uuid.UUID) (domain.UnitPrice, error)
	SellableUnitsWithPrices(ctx context.Context, q platform.Querier, storeID uuid.UUID,
		productIDs []uuid.UUID) (map[uuid.UUID][]domain.SellableUnitsWithPrice, error)

	// Lookups by the names a person types. Bulk import matches a sheet against
	// what is stored, and a sheet has names in it, not identifiers.
	FindCategoryByName(ctx context.Context, q platform.Querier, name string, parentID *uuid.UUID) (domain.Category, bool, error)
	FindUnitByCodeOrName(ctx context.Context, q platform.Querier, text string) (domain.UnitOfMeasure, bool, error)
	FindProductByBarcode(ctx context.Context, q platform.Querier, barcode string) (domain.Product, bool, error)
	FindProductByName(ctx context.Context, q platform.Querier, name string, categoryID uuid.UUID) (domain.Product, bool, error)
	DefaultProductUnitID(ctx context.Context, q platform.Querier, productID uuid.UUID) (uuid.UUID, bool, error)

	ListProductUnits(ctx context.Context, q platform.Querier, productID uuid.UUID) ([]domain.ProductUnit, error)
	GetProductUnit(ctx context.Context, q platform.Querier, id, productID uuid.UUID) (domain.ProductUnit, error)
	CreateProductUnit(ctx context.Context, q platform.Querier, u domain.ProductUnit) error
	UpdateProductUnit(ctx context.Context, q platform.Querier, u domain.ProductUnit) (bool, error)
	SetProductUnitActive(ctx context.Context, q platform.Querier, id, productID uuid.UUID, active bool) (bool, error)
	ClearDefaultProductUnit(ctx context.Context, q platform.Querier, productID, exceptID uuid.UUID) error
	SetDefaultProductUnit(ctx context.Context, q platform.Querier, id, productID uuid.UUID) (bool, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies catalogue rules.
type Service struct {
	db   DB
	repo Repository
}

// New returns a catalogue service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

// ListCategories returns categories in display order.
func (s *Service) ListCategories(ctx context.Context, includeInactive bool) ([]domain.Category, error) {
	return s.repo.ListCategories(ctx, s.db, includeInactive)
}

// GetCategory returns one category with its product and child counts.
func (s *Service) GetCategory(ctx context.Context, id uuid.UUID) (domain.Category, error) {
	return s.repo.GetCategory(ctx, s.db, id)
}

// CreateCategory validates and stores a new category.
//
// A category that names a parent inherits that parent's fate, so a retired
// parent is refused: the new category would be invisible from the moment it was
// made, and the person who made it would have no way of telling why.
func (s *Service) CreateCategory(ctx context.Context, name, nameHi, nameKn, hsnCode string, parentID *uuid.UUID) (domain.Category, error) {
	c, err := domain.NewCategory(name, nameHi, nameKn, hsnCode, parentID)
	if err != nil {
		return domain.Category{}, err
	}
	if parentID != nil {
		if err := s.requireLiveParent(ctx, s.db, *parentID); err != nil {
			return domain.Category{}, err
		}
	}
	if err := s.repo.CreateCategory(ctx, s.db, c); err != nil {
		return domain.Category{}, fmt.Errorf("create category: %w", err)
	}
	return c, nil
}

// UpdateCategory renames a category and, where asked, moves it elsewhere in the
// tree.
//
// The move is the part that needs the transaction. Checking that a category is
// not being put inside its own subtree is a question about the whole tree, not
// about the row being written, so it is asked and answered under the tree lock
// with the write that depends on it.
func (s *Service) UpdateCategory(ctx context.Context, id uuid.UUID, in domain.CategoryUpdate) (domain.Category, error) {
	var updated domain.Category
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		if err := s.repo.LockCategoryTree(ctx, tx); err != nil {
			return err
		}
		existing, err := s.repo.GetCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		next, err := existing.ApplyUpdate(in)
		if err != nil {
			return err
		}
		if next.ParentID != nil && !sameParent(existing.ParentID, next.ParentID) {
			if err := s.requireLiveParent(ctx, tx, *next.ParentID); err != nil {
				return err
			}
			loops, err := s.repo.CategoryWouldLoop(ctx, tx, id, *next.ParentID)
			if err != nil {
				return fmt.Errorf("check category tree: %w", err)
			}
			if loops {
				return fmt.Errorf("%w: %s cannot be moved inside itself", domain.ErrValidation, existing.Name)
			}
		}
		found, err := s.repo.UpdateCategory(ctx, tx, next)
		if err != nil {
			return fmt.Errorf("update category: %w", err)
		}
		if !found {
			return platform.ErrNotFound
		}
		refreshed, err := s.repo.GetCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		updated = refreshed
		return nil
	})
	if err != nil {
		return domain.Category{}, err
	}
	return updated, nil
}

// SetCategoryActive retires a category or brings it back.
//
// Retiring one that still holds products is refused rather than done quietly.
// Deactivating a category does not deactivate what is in it, so the products
// would stay on sale under a heading nobody can see or pick — findable only by
// barcode, and missing from every report that groups by category. MergeCategory
// is the way to retire a category that is still in use: it says where the
// contents go.
func (s *Service) SetCategoryActive(ctx context.Context, id uuid.UUID, active bool) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		c, err := s.repo.GetCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.IsActive == active {
			return nil
		}
		if !active {
			if c.ProductCount > 0 {
				return fmt.Errorf("%w: %s still holds %s — move them to another category first",
					domain.ErrValidation, c.Name, plural(c.ProductCount, "product", "products"))
			}
			if c.ChildCount > 0 {
				return fmt.Errorf("%w: %s still has %s under it — move or retire %s first",
					domain.ErrValidation, c.Name,
					plural(c.ChildCount, "category", "categories"),
					map[bool]string{true: "it", false: "them"}[c.ChildCount == 1])
			}
		} else if c.ParentID != nil {
			// Bringing a child back under a retired parent puts it somewhere
			// that does not appear in the tree, which looks from the outside
			// exactly like the reactivation having failed.
			if err := s.requireLiveParent(ctx, tx, *c.ParentID); err != nil {
				return err
			}
		}
		found, err := s.repo.SetCategoryActive(ctx, tx, id, active)
		if err != nil {
			return fmt.Errorf("set category active: %w", err)
		}
		if !found {
			return platform.ErrNotFound
		}
		return nil
	})
}

// MergeCategory moves everything in one category into another and retires the
// first. It is how a duplicate created at the till ("Biscuit" beside
// "Biscuits") is cleaned up without touching a single product by hand.
//
// Merging a category into one of its own children is allowed and is a real
// case: a shop that made Beverages > Soft Drinks and only ever used the second
// one wants the first to go away. That merge is what needs the extra step —
// the child is lifted out of the subtree first, so that when the remaining
// children move onto it none of them is its own ancestor.
func (s *Service) MergeCategory(ctx context.Context, from, into uuid.UUID) (MergeResult, error) {
	if from == into {
		return MergeResult{}, fmt.Errorf("%w: a category cannot be merged into itself", domain.ErrValidation)
	}

	var result MergeResult
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		if err := s.repo.LockCategoryTree(ctx, tx); err != nil {
			return err
		}
		source, err := s.repo.GetCategory(ctx, tx, from)
		if err != nil {
			return err
		}
		target, err := s.repo.GetCategory(ctx, tx, into)
		if err != nil {
			return err
		}
		if !target.IsActive {
			return fmt.Errorf("%w: %s has been retired, so nothing can be moved into it",
				domain.ErrValidation, target.Name)
		}

		// Is the target inside the source's subtree? Lift it out before
		// anything else moves, onto whatever the source's own parent is —
		// which may be nothing, making it a root.
		insideSource, err := s.repo.CategoryWouldLoop(ctx, tx, from, into)
		if err != nil {
			return fmt.Errorf("check category tree: %w", err)
		}
		if insideSource {
			lifted := target
			lifted.ParentID = source.ParentID
			if _, err := s.repo.UpdateCategory(ctx, tx, lifted); err != nil {
				return fmt.Errorf("move %s out of %s: %w", target.Name, source.Name, err)
			}
		}

		products, err := s.repo.MoveProductsToCategory(ctx, tx, from, into)
		if err != nil {
			return fmt.Errorf("move products: %w", err)
		}
		children, err := s.repo.MoveChildCategories(ctx, tx, from, into)
		if err != nil {
			return fmt.Errorf("move sub-categories: %w", err)
		}
		found, err := s.repo.SetCategoryActive(ctx, tx, from, false)
		if err != nil {
			return fmt.Errorf("retire %s: %w", source.Name, err)
		}
		if !found {
			return platform.ErrNotFound
		}
		result = MergeResult{
			From:              source.Name,
			Into:              target.Name,
			ProductsMoved:     int(products),
			CategoriesMoved:   int(children),
			TargetWasInsideIt: insideSource,
		}
		return nil
	})
	if err != nil {
		return MergeResult{}, err
	}
	return result, nil
}

// MergeResult says what a merge actually did, so the screen can report it
// instead of saying "done".
type MergeResult struct {
	From              string `json:"from"`
	Into              string `json:"into"`
	ProductsMoved     int    `json:"products_moved"`
	CategoriesMoved   int    `json:"categories_moved"`
	TargetWasInsideIt bool   `json:"target_was_inside_it"`
}

// requireLiveParent refuses a parent that does not exist or has been retired.
func (s *Service) requireLiveParent(ctx context.Context, q platform.Querier, parentID uuid.UUID) error {
	parent, err := s.repo.GetCategory(ctx, q, parentID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return fmt.Errorf("%w: no such parent category", domain.ErrValidation)
		}
		return err
	}
	if !parent.IsActive {
		return fmt.Errorf("%w: %s has been retired and cannot hold categories",
			domain.ErrValidation, parent.Name)
	}
	return nil
}

func sameParent(a, b *uuid.UUID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---------------------------------------------------------------------------
// Unit families
// ---------------------------------------------------------------------------

// ListFamilies returns the unit families.
func (s *Service) ListFamilies(ctx context.Context, includeInactive bool) ([]domain.Family, error) {
	return s.repo.ListFamilies(ctx, s.db, includeInactive)
}

// CreateFamily validates and stores a unit family.
//
// The family is created without a canonical unit, because its canonical unit is
// one of its own members and none exists yet. CreateUnit adopts the first
// factor-one unit as the base, or SetFamilyBaseUnit names one outright.
func (s *Service) CreateFamily(ctx context.Context, code, name string) (domain.Family, error) {
	f, err := domain.NewFamily(code, name)
	if err != nil {
		return domain.Family{}, err
	}
	if err := s.repo.CreateFamily(ctx, s.db, f); err != nil {
		return domain.Family{}, fmt.Errorf("create unit family: %w", err)
	}
	return f, nil
}

// UpdateFamily renames a family.
func (s *Service) UpdateFamily(ctx context.Context, id uuid.UUID, name string) (domain.Family, error) {
	// NewFamily is reused for its validation; the code is not writable, so the
	// stored one is read back rather than taken from the caller.
	existing, err := s.repo.GetFamily(ctx, s.db, id)
	if err != nil {
		return domain.Family{}, err
	}
	validated, err := domain.NewFamily(existing.Code, name)
	if err != nil {
		return domain.Family{}, err
	}
	existing.Name = validated.Name

	found, err := s.repo.UpdateFamily(ctx, s.db, existing)
	if err != nil {
		return domain.Family{}, fmt.Errorf("update unit family: %w", err)
	}
	if !found {
		return domain.Family{}, platform.ErrNotFound
	}
	return existing, nil
}

// SetFamilyBaseUnit names the unit every member of a family converts through.
//
// The repository refuses a unit from another family, so a "weight" family
// cannot end up converting through a litre — which would silently corrupt every
// conversion in the family rather than failing at the one that is wrong.
func (s *Service) SetFamilyBaseUnit(ctx context.Context, familyID, uomID uuid.UUID) error {
	ok, err := s.repo.SetFamilyBaseUnit(ctx, s.db, familyID, uomID)
	if err != nil {
		return fmt.Errorf("set family base unit: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: no such family, or that unit belongs to a different family",
			domain.ErrValidation)
	}
	return nil
}

// SetFamilyActive deactivates or reactivates a unit family.
func (s *Service) SetFamilyActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetFamilyActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set unit family active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Units of measure
// ---------------------------------------------------------------------------

// ListUnits returns units of measure.
func (s *Service) ListUnits(ctx context.Context, f domain.UnitFilter) ([]domain.UnitOfMeasure, error) {
	return s.repo.ListUnits(ctx, s.db, f)
}

// CreateUnitInput is a new unit of measure. A unit belongs to a family and
// carries the factor that converts it to that family's canonical unit; without
// both it converts nothing, which is the only job the table has.
type CreateUnitInput struct {
	Code          string
	Name          string
	DecimalPlaces int
	FamilyID      uuid.UUID
	FactorToBase  money.Rate
}

// CreateUnit validates and stores a unit of measure.
//
// A family whose canonical unit does not exist yet converts nothing, so a
// factor-one unit created into an empty family is adopted as that family's base
// in the same transaction. That makes "add a family, add its units" work in the
// obvious order without a separate administrative step nobody would know to
// take. A family that already has a base keeps it.
func (s *Service) CreateUnit(ctx context.Context, in CreateUnitInput) (domain.UnitOfMeasure, error) {
	u, err := domain.NewUnitOfMeasure(in.Code, in.Name, in.DecimalPlaces, in.FamilyID, in.FactorToBase)
	if err != nil {
		return domain.UnitOfMeasure{}, err
	}

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		family, err := s.repo.GetFamily(ctx, tx, u.FamilyID)
		if err != nil {
			if errors.Is(err, platform.ErrNotFound) {
				return fmt.Errorf("%w: no unit family %s", domain.ErrValidation, u.FamilyID)
			}
			return fmt.Errorf("read unit family: %w", err)
		}
		if err := s.repo.CreateUnit(ctx, tx, u); err != nil {
			return err
		}
		if family.BaseUOMID == uuid.Nil && u.FactorToBase.Equal(money.RateFromInt(1)) {
			if _, err := s.repo.SetFamilyBaseUnit(ctx, tx, u.FamilyID, u.ID); err != nil {
				return fmt.Errorf("adopt %s as the base unit of %s: %w", u.Code, family.Code, err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.UnitOfMeasure{}, fmt.Errorf("create unit: %w", err)
	}
	return u, nil
}

// UpdateUnitInput is the editable part of a unit of measure. Neither the code
// nor the family is here: see the repository for why moving a unit between
// families cannot be undone.
type UpdateUnitInput struct {
	Name          string
	DecimalPlaces int
	FactorToBase  money.Rate
}

// UpdateUnit replaces a unit's name, precision and conversion factor.
func (s *Service) UpdateUnit(ctx context.Context, id uuid.UUID, in UpdateUnitInput) (domain.UnitOfMeasure, error) {
	existing, err := s.repo.GetUnit(ctx, s.db, id)
	if err != nil {
		return domain.UnitOfMeasure{}, err
	}
	// Validated through the constructor so an update cannot write a value a
	// create would have refused — a factor of zero, most of all.
	validated, err := domain.NewUnitOfMeasure(existing.Code, in.Name, in.DecimalPlaces,
		existing.FamilyID, in.FactorToBase)
	if err != nil {
		return domain.UnitOfMeasure{}, err
	}
	validated.ID = existing.ID
	validated.IsActive = existing.IsActive

	found, err := s.repo.UpdateUnit(ctx, s.db, validated)
	if err != nil {
		return domain.UnitOfMeasure{}, fmt.Errorf("update unit: %w", err)
	}
	if !found {
		return domain.UnitOfMeasure{}, platform.ErrNotFound
	}
	return validated, nil
}

// SetUnitActive deactivates or reactivates a unit of measure.
func (s *Service) SetUnitActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetUnitActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set unit active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Products
// ---------------------------------------------------------------------------

// ListProducts returns the catalogue priced for one store.
func (s *Service) ListProducts(ctx context.Context, f domain.ProductFilter) ([]domain.PricedProduct, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.ListProducts(ctx, s.db, f)
}

// GetProduct returns one product priced for a store.
func (s *Service) GetProduct(ctx context.Context, id, storeID uuid.UUID) (domain.PricedProduct, error) {
	if storeID == uuid.Nil {
		return domain.PricedProduct{}, ErrStoreRequired
	}
	return s.repo.GetProduct(ctx, s.db, id, storeID)
}

// FindByBarcode resolves a scanned barcode for one store, to a product and the
// unit that was scanned.
func (s *Service) FindByBarcode(ctx context.Context, barcode string, storeID uuid.UUID) (domain.ScannedProduct, error) {
	if storeID == uuid.Nil {
		return domain.ScannedProduct{}, ErrStoreRequired
	}
	if barcode == "" {
		return domain.ScannedProduct{}, fmt.Errorf("%w: barcode is required", domain.ErrValidation)
	}
	return s.repo.FindByBarcode(ctx, s.db, barcode, storeID)
}

// CreateProductInput carries a new product and, optionally, its opening price
// in one store.
type CreateProductInput struct {
	Product domain.NewProductInput
	// StoreID and Price are optional. When StoreID is set the price is written
	// in the same transaction as the product, so a product can never exist
	// briefly without the price the POS needs to sell it.
	StoreID uuid.UUID
	Price   *domain.UnitPrice
	// UserID is who is doing this, recorded against the opening price. Nil is
	// allowed and means the first-run bootstrap, which has nobody behind it.
	UserID uuid.UUID
}

// CreatedProduct is a new product together with the unit it may be transacted
// in from the moment it exists.
type CreatedProduct struct {
	domain.Product
	// DefaultUnit is created alongside the product. It is what a bare quantity
	// means, and what an opening price is attached to.
	DefaultUnit domain.ProductUnit `json:"default_unit"`
}

// CreateProduct stores a product, its default unit and its opening price
// atomically.
//
// The default unit is not optional and not deferred. A price row must name a
// product unit, stock is counted in the product's base unit, and a bare
// quantity has to mean something — so a product with no units is a product
// nothing can be done with. Creating it here is the same thing the migration
// did for every product that already existed.
func (s *Service) CreateProduct(ctx context.Context, in CreateProductInput) (CreatedProduct, error) {
	p, err := domain.NewProduct(in.Product)
	if err != nil {
		return CreatedProduct{}, err
	}
	if in.Price != nil && in.StoreID == uuid.Nil {
		return CreatedProduct{}, ErrStoreRequired
	}

	var created CreatedProduct
	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		made, err := s.createProduct(ctx, tx, p, in)
		created = made
		return err
	})
	if err != nil {
		return CreatedProduct{}, fmt.Errorf("create product: %w", err)
	}
	return created, nil
}

// CreateProductTx is CreateProduct inside a transaction the caller owns.
//
// Bulk import needs it: two thousand products are one transaction, not two
// thousand, and the rules that make a product usable the moment it exists — it
// has a default unit, its opening price is written with it — must be the same
// rules, not a second implementation that drifts.
func (s *Service) CreateProductTx(ctx context.Context, tx platform.Tx, in CreateProductInput) (CreatedProduct, error) {
	p, err := domain.NewProduct(in.Product)
	if err != nil {
		return CreatedProduct{}, err
	}
	if in.Price != nil && in.StoreID == uuid.Nil {
		return CreatedProduct{}, ErrStoreRequired
	}
	return s.createProduct(ctx, tx, p, in)
}

// createProduct is the body both entry points share: the product, the default
// unit it cannot be used without, and the opening price, all in the caller's
// transaction.
func (s *Service) createProduct(ctx context.Context, tx platform.Tx, p domain.Product, in CreateProductInput) (CreatedProduct, error) {
	if err := s.repo.CreateProduct(ctx, tx, p); err != nil {
		return CreatedProduct{}, err
	}

	// The label comes from the unit of measure the product is stocked in, so
	// a kilo product offers "Kilogram" rather than a nameless "Unit". A
	// product whose unit has been removed still gets a usable default.
	label := "Unit"
	if uom, err := s.repo.GetUnit(ctx, tx, p.UOMID); err == nil {
		label = uom.Name
	} else if !errors.Is(err, platform.ErrNotFound) {
		return CreatedProduct{}, fmt.Errorf("read unit of measure: %w", err)
	}

	uomID := p.UOMID
	unit, err := domain.NewProductUnit(domain.NewProductUnitInput{
		ProductID: p.ID,
		UOMID:     &uomID,
		Label:     label,
		// One of the default unit is one of the stocking unit. Every other
		// unit of this product is then expressed against the same pool.
		QtyInBase:     money.QuantityFromInt(1),
		IsDefault:     true,
		IsSellable:    true,
		IsPurchasable: true,
	})
	if err != nil {
		return CreatedProduct{}, err
	}
	if err := s.repo.CreateProductUnit(ctx, tx, unit); err != nil {
		return CreatedProduct{}, err
	}

	if in.Price != nil {
		price := *in.Price
		price.ProductID = p.ID
		price.StoreID = in.StoreID
		price.ProductUnitID = unit.ID
		price.Label = unit.Label
		price.QtyInBase = unit.QtyInBase
		if err := price.Validate(); err != nil {
			return CreatedProduct{}, err
		}
		if err := s.repo.UpsertPrice(ctx, tx, price, domain.PriceAuthor{
			UserID: in.UserID,
			// Where the price started, which is not the same as somebody
			// later changing it and is worth telling apart in the history.
			Source: domain.PriceOpening,
		}); err != nil {
			return CreatedProduct{}, err
		}
	}

	return CreatedProduct{Product: p, DefaultUnit: unit}, nil
}

// UpdateProduct replaces a product's editable fields.
func (s *Service) UpdateProduct(ctx context.Context, id uuid.UUID, in domain.NewProductInput) (domain.Product, error) {
	p, err := domain.NewProduct(in)
	if err != nil {
		return domain.Product{}, err
	}
	p.ID = id

	found, err := s.repo.UpdateProduct(ctx, s.db, p)
	if err != nil {
		return domain.Product{}, fmt.Errorf("update product: %w", err)
	}
	if !found {
		return domain.Product{}, platform.ErrNotFound
	}
	return p, nil
}

// SetProductActive deactivates or reactivates a product. Deactivation is the
// only removal the catalogue offers: a product that has been sold is
// referenced by invoice history and must outlive its usefulness on the shelf.
func (s *Service) SetProductActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetProductActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set product active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Prices, per unit
// ---------------------------------------------------------------------------

// SetPrice sets what one unit of a product sells for in one store.
//
// Prices are independent, not derived. A 25 kg sack at Rs.1,050 alongside loose
// rice at Rs.45 a kilo is the ordinary case and not an error: bulk being
// cheaper per kilo is the whole reason a shop sells it that way, and the model
// this replaces could not express it.
func (s *Service) SetPrice(ctx context.Context, p domain.UnitPrice, by domain.PriceAuthor) (domain.UnitPrice, error) {
	if p.StoreID == uuid.Nil {
		return domain.UnitPrice{}, ErrStoreRequired
	}
	if p.ProductID == uuid.Nil {
		return domain.UnitPrice{}, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}
	if p.ProductUnitID == uuid.Nil {
		return domain.UnitPrice{}, fmt.Errorf("%w: the unit being priced is required", domain.ErrValidation)
	}

	// The unit is read back rather than trusted, both because it must belong to
	// this product — pricing another product's sack against this one is a
	// foreign key the schema cannot express — and because Validate's messages
	// name the unit, which is the only way "selling price is above the MRP"
	// tells a shopkeeper which row to go and fix.
	unit, err := s.repo.GetProductUnit(ctx, s.db, p.ProductUnitID, p.ProductID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return domain.UnitPrice{}, fmt.Errorf("%w: that unit does not belong to this product",
				domain.ErrValidation)
		}
		return domain.UnitPrice{}, fmt.Errorf("read product unit: %w", err)
	}
	p.Label = unit.Label
	p.QtyInBase = unit.QtyInBase
	if err := p.Validate(); err != nil {
		return domain.UnitPrice{}, err
	}

	if err := s.repo.UpsertPrice(ctx, s.db, p, by); err != nil {
		return domain.UnitPrice{}, fmt.Errorf("set price: %w", err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Price history
// ---------------------------------------------------------------------------

// PriceHistory returns what one price has been, newest first and paired so each
// entry carries the one before it.
//
// Pairing happens here rather than in the database because the difference
// between two figures is not a fact worth storing: derived, it cannot disagree
// with the numbers either side of it.
func (s *Service) PriceHistory(ctx context.Context, productID, storeID, productUnitID uuid.UUID, limit int) ([]domain.PriceMovement, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if productID == uuid.Nil {
		return nil, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}

	var history []domain.PriceChange
	var err error
	if productUnitID == uuid.Nil {
		// Every unit of the product together: a product sold loose and by the
		// sack has two price series, and looking at one of them alone is how a
		// shopkeeper concludes the wrong thing about the other.
		history, err = s.repo.ProductPriceHistory(ctx, s.db, productID, storeID, limit)
	} else {
		history, err = s.repo.PriceHistory(ctx, s.db, productID, storeID, productUnitID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("read price history: %w", err)
	}
	return domain.AsMovements(history), nil
}

// RecentPriceChanges is every price that moved in this store lately.
func (s *Service) RecentPriceChanges(ctx context.Context, storeID uuid.UUID, since time.Time, limit int) ([]domain.PriceMovement, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	history, err := s.repo.RecentPriceChanges(ctx, s.db, storeID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("read recent price changes: %w", err)
	}
	// Not paired: these entries are from different products, so the row below
	// one of them is not what it used to be. Pairing them would invent a
	// comparison between two unrelated prices.
	movements := make([]domain.PriceMovement, len(history))
	for i, entry := range history {
		movements[i] = domain.PriceMovement{PriceChange: entry}
	}
	return movements, nil
}

// GetPrice returns the stored price of one unit of a product, cost price
// included.
func (s *Service) GetPrice(ctx context.Context, productID, storeID, productUnitID uuid.UUID) (domain.UnitPrice, error) {
	if storeID == uuid.Nil {
		return domain.UnitPrice{}, ErrStoreRequired
	}
	if productUnitID == uuid.Nil {
		return domain.UnitPrice{}, fmt.Errorf("%w: the unit being priced is required", domain.ErrValidation)
	}
	return s.repo.GetPrice(ctx, s.db, productID, storeID, productUnitID)
}

// ListPrices returns every sellable unit of a product with its price in this
// store, default first and then smallest to largest.
//
// A unit with no price row is listed with HasPrice false rather than omitted.
// Hiding it would look to a cashier exactly like a missing product; showing it
// unpriced says what to go and fix.
func (s *Service) ListPrices(ctx context.Context, productID, storeID uuid.UUID) ([]domain.SellableUnitsWithPrice, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if productID == uuid.Nil {
		return nil, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}
	byProduct, err := s.repo.SellableUnitsWithPrices(ctx, s.db, storeID, []uuid.UUID{productID})
	if err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}
	prices := byProduct[productID]
	if prices == nil {
		prices = []domain.SellableUnitsWithPrice{}
	}
	return prices, nil
}

// ---------------------------------------------------------------------------
// Product units
// ---------------------------------------------------------------------------

// ListProductUnits returns every way a product may be transacted.
//
// Units are not store-scoped: a 25 kg sack is 25 kg in every shop. The price is
// what differs between stores, and that lives on product_prices.
func (s *Service) ListProductUnits(ctx context.Context, productID uuid.UUID) ([]domain.ProductUnit, error) {
	if productID == uuid.Nil {
		return nil, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}
	return s.repo.ListProductUnits(ctx, s.db, productID)
}

// AddProductUnit validates and stores a way of transacting a product.
//
// The product is checked first so an unknown one is reported as missing rather
// than as a foreign key violation, which on this table could equally have meant
// the unit of measure.
//
// A unit created as the default clears the previous default in the same
// transaction; see SetDefaultUnit for why that cannot be two separate writes.
func (s *Service) AddProductUnit(ctx context.Context, in domain.NewProductUnitInput) (domain.ProductUnit, error) {
	u, err := domain.NewProductUnit(in)
	if err != nil {
		return domain.ProductUnit{}, err
	}

	exists, err := s.repo.ProductExists(ctx, s.db, u.ProductID)
	if err != nil {
		return domain.ProductUnit{}, fmt.Errorf("check product: %w", err)
	}
	if !exists {
		return domain.ProductUnit{}, platform.ErrNotFound
	}

	err = s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		if u.IsDefault {
			if err := s.repo.ClearDefaultProductUnit(ctx, tx, u.ProductID, u.ID); err != nil {
				return fmt.Errorf("clear previous default unit: %w", err)
			}
		}
		return s.repo.CreateProductUnit(ctx, tx, u)
	})
	if err != nil {
		return domain.ProductUnit{}, fmt.Errorf("create product unit: %w", err)
	}
	return u, nil
}

// UpdateProductUnit replaces a unit's editable fields.
//
// It cannot change which unit is the default — SetDefaultUnit does that, in a
// transaction — so an ordinary edit can never trip the one-default index.
func (s *Service) UpdateProductUnit(ctx context.Context, id uuid.UUID, in domain.NewProductUnitInput) (domain.ProductUnit, error) {
	existing, err := s.repo.GetProductUnit(ctx, s.db, id, in.ProductID)
	if err != nil {
		return domain.ProductUnit{}, err
	}
	// The stored default flag is carried through the constructor, so that
	// editing the current default does not have to restate it and cannot
	// accidentally clear it.
	in.IsDefault = existing.IsDefault
	u, err := domain.NewProductUnit(in)
	if err != nil {
		return domain.ProductUnit{}, err
	}
	u.ID = existing.ID
	u.IsActive = existing.IsActive

	found, err := s.repo.UpdateProductUnit(ctx, s.db, u)
	if err != nil {
		return domain.ProductUnit{}, fmt.Errorf("update product unit: %w", err)
	}
	if !found {
		return domain.ProductUnit{}, platform.ErrNotFound
	}
	return u, nil
}

// SetProductUnitActive deactivates or reactivates one unit of a product.
func (s *Service) SetProductUnitActive(ctx context.Context, id, productID uuid.UUID, active bool) error {
	found, err := s.repo.SetProductUnitActive(ctx, s.db, id, productID, active)
	if err != nil {
		return fmt.Errorf("set product unit active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// SetDefaultUnit makes one unit the product's default, clearing the previous
// one.
//
// Both writes happen in ONE transaction, and the order is not incidental.
// idx_product_units_one_default is a partial unique index over
// (product_id) WHERE is_default, so setting the new default before clearing the
// old one is rejected outright by the database. Clearing first and committing
// separately would be worse than the error: between the two commits the product
// has no default at all, and a bare quantity arriving in that window means
// nothing — which is exactly the ambiguity this whole change removes.
func (s *Service) SetDefaultUnit(ctx context.Context, id, productID uuid.UUID) (domain.ProductUnit, error) {
	if productID == uuid.Nil {
		return domain.ProductUnit{}, fmt.Errorf("%w: product is required", domain.ErrValidation)
	}

	var unit domain.ProductUnit
	err := s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		if err := s.repo.ClearDefaultProductUnit(ctx, tx, productID, id); err != nil {
			return fmt.Errorf("clear previous default unit: %w", err)
		}
		ok, err := s.repo.SetDefaultProductUnit(ctx, tx, id, productID)
		if err != nil {
			return fmt.Errorf("set default unit: %w", err)
		}
		if !ok {
			// The guard lives in the UPDATE's WHERE clause, so this covers three
			// cases at once: no such unit, a unit of another product, and a unit
			// that is inactive or not sellable. A read follows only to say which.
			existing, readErr := s.repo.GetProductUnit(ctx, tx, id, productID)
			if readErr != nil {
				if errors.Is(readErr, platform.ErrNotFound) {
					return platform.ErrNotFound
				}
				return readErr
			}
			return fmt.Errorf("%w: %s cannot be the default because it is %s",
				domain.ErrValidation, existing.Label, unusableReason(existing))
		}
		refreshed, err := s.repo.GetProductUnit(ctx, tx, id, productID)
		if err != nil {
			return fmt.Errorf("read default unit: %w", err)
		}
		unit = refreshed
		return nil
	})
	if err != nil {
		return domain.ProductUnit{}, err
	}
	return unit, nil
}

// unusableReason says why a unit cannot be made the default.
func unusableReason(u domain.ProductUnit) string {
	if !u.IsActive {
		return "not active"
	}
	return "not sellable, and the default is what a sale charges for"
}
