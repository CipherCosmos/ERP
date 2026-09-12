// Package postgres contains every SQL statement in the application.
//
// Nothing outside this package writes SQL. That is the whole point: the
// implementation this replaces had 219 queries embedded directly in HTTP
// handlers, which is why the same defects — a discarded scan error here, a
// missing status filter there — recurred in nine different files with nobody
// owning the rule.
//
// Every method takes a postgres.Querier rather than a pool, so the same code
// runs inside a transaction or outside one, and the service layer decides
// which. Errors come back already translated to the platform sentinels, so a
// caller tests errors.Is(err, postgres.ErrConflict) and never sees a driver
// type.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/catalogue"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// defaultProductLimit bounds an unfiltered catalogue listing.
const defaultProductLimit = 100

// maxProductLimit is the ceiling a caller cannot exceed.
const maxProductLimit = 500

// Catalogue reads and writes categories, units, products and prices.
type Catalogue struct{}

// NewCatalogue returns a catalogue repository.
func NewCatalogue() *Catalogue { return &Catalogue{} }

const categoryColumns = `c.id, c.name, COALESCE(c.name_hi, ''), COALESCE(c.name_kn, ''),
	c.parent_id, COALESCE(c.hsn_code, ''), c.is_active,
	COALESCE(parent.name, ''),
	-- The two product counts are separate on purpose. The first decides whether
	-- a category is in use: a discontinued product is not something the shop is
	-- still selling, and counting it would refuse to retire a category whose
	-- only contents were withdrawn last year. The second exists because a merge
	-- moves discontinued products too — it has to, or they would be left
	-- pointing at a category that no longer appears — so the screen that offers
	-- the merge has to be able to say so before it happens.
	(SELECT count(*) FROM products p WHERE p.category_id = c.id AND p.is_active),
	(SELECT count(*) FROM products p WHERE p.category_id = c.id AND NOT p.is_active),
	(SELECT count(*) FROM categories child WHERE child.parent_id = c.id AND child.is_active)`

const categoryFrom = `
	FROM categories c
	LEFT JOIN categories parent ON parent.id = c.parent_id`

func scanCategory(sc interface{ Scan(...any) error }) (catalogue.Category, error) {
	var c catalogue.Category
	err := sc.Scan(&c.ID, &c.Name, &c.NameHi, &c.NameKn,
		&c.ParentID, &c.HSNCode, &c.IsActive,
		&c.ParentName, &c.ProductCount, &c.RetiredProductCount, &c.ChildCount)
	return c, err
}

// ListCategories returns categories in display order, active ones unless the
// caller asks for the rest.
//
// Ordered by the parent's name then its own, so a tree renders without the
// caller sorting it again: every child sits under the heading it belongs to, and
// the roots — whose parent name is blank — come first.
func (r *Catalogue) ListCategories(ctx context.Context, q platform.Querier, includeInactive bool) ([]catalogue.Category, error) {
	where := `WHERE c.is_active`
	if includeInactive {
		where = `WHERE true`
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+categoryColumns+categoryFrom+` `+where+
			` ORDER BY COALESCE(parent.name, ''), c.name`)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	categories := make([]catalogue.Category, 0)
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			// Never skip a row that failed to scan. The implementation this
			// replaces logged and continued, silently returning a short list
			// that looked like a complete one.
			return nil, fmt.Errorf("scan category: %w", platform.MapError(err))
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return categories, nil
}

// GetCategory returns one category with its counts.
func (r *Catalogue) GetCategory(ctx context.Context, q platform.Querier, id uuid.UUID) (catalogue.Category, error) {
	c, err := scanCategory(q.QueryRowContext(ctx,
		`SELECT `+categoryColumns+categoryFrom+` WHERE c.id = $1`, id))
	if err != nil {
		return catalogue.Category{}, platform.MapError(err)
	}
	return c, nil
}

// UpdateCategory replaces the editable fields and reports whether it existed.
func (r *Catalogue) UpdateCategory(ctx context.Context, q platform.Querier, c catalogue.Category) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE categories SET name = $2, name_hi = $3, name_kn = $4,
			parent_id = $5, hsn_code = $6, updated_at = now()
		  WHERE id = $1`,
		c.ID, c.Name, c.NameHi, c.NameKn, c.ParentID, c.HSNCode)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// maxCategoryDepth bounds the ancestor walk. Nothing should ever approach it —
// the deepest real catalogue is three or four levels — but a recursive query
// against a table that somehow holds a cycle does not return, and a hung query
// on the shop's database at four in the afternoon is far worse than a reparent
// that is refused.
const maxCategoryDepth = "32"

// categoryTreeLock is an arbitrary constant naming one lock. Every reparent and
// merge takes it, so those operations run one at a time.
const categoryTreeLock = 4820_1001

// LockCategoryTree serialises the operations that reshape the tree.
//
// The cycle check reads the tree and then writes to it, and READ COMMITTED does
// not hold those together: two people reparenting different categories at the
// same moment can each read a tree in which their own move is fine, and commit
// a pair of moves that closes a loop. Row locks cannot help, because the rows
// that would have to be locked are exactly the ones the other transaction is
// about to create a path through.
//
// The lock is released when the transaction ends, whichever way it ends.
// Reparenting happens a handful of times in a shop's life, so serialising it
// costs nothing worth measuring.
func (r *Catalogue) LockCategoryTree(ctx context.Context, q platform.Querier) error {
	_, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(categoryTreeLock))
	return platform.MapError(err)
}

// CategoryWouldLoop reports whether making parentID the parent of id puts the
// category inside its own subtree.
//
// It is one recursive query rather than a walk in Go, and it runs inside the
// transaction that does the write. Two people reparenting different categories
// at once can each read a tree that is perfectly fine and, between them, make a
// loop — and a loop in this table is not a display problem: the listing orders
// by parent, the product form walks it, and a cycle makes both of those run
// until something gives up.
//
// The recursion walks UP from the proposed parent. If it reaches the category
// being moved, the category is an ancestor of its own proposed parent.
func (r *Catalogue) CategoryWouldLoop(ctx context.Context, q platform.Querier, id, parentID uuid.UUID) (bool, error) {
	var loops bool
	err := q.QueryRowContext(ctx,
		`WITH RECURSIVE ancestors AS (
			SELECT id, parent_id, 1 AS depth FROM categories WHERE id = $2
			UNION ALL
			SELECT c.id, c.parent_id, a.depth + 1
			  FROM categories c
			  JOIN ancestors a ON c.id = a.parent_id
			 WHERE a.depth < `+maxCategoryDepth+`
		 )
		 SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $1)`,
		id, parentID).Scan(&loops)
	if err != nil {
		return false, platform.MapError(err)
	}
	return loops, nil
}

// MoveProductsToCategory reassigns every product in one category to another and
// returns how many moved. It is how a category is retired without orphaning what
// was in it.
func (r *Catalogue) MoveProductsToCategory(ctx context.Context, q platform.Querier, from, to uuid.UUID) (int64, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE products SET category_id = $2, updated_at = now() WHERE category_id = $1`, from, to)
	if err != nil {
		return 0, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}

// MoveChildCategories reparents every child of one category onto another.
func (r *Catalogue) MoveChildCategories(ctx context.Context, q platform.Querier, from, to uuid.UUID) (int64, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE categories SET parent_id = $2, updated_at = now() WHERE parent_id = $1`, from, to)
	if err != nil {
		return 0, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}

// CreateCategory inserts a category.
func (r *Catalogue) CreateCategory(ctx context.Context, q platform.Querier, c catalogue.Category) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO categories (id, name, name_hi, name_kn, parent_id, hsn_code, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, c.Name, c.NameHi, c.NameKn, c.ParentID, c.HSNCode, c.IsActive)
	return platform.MapError(err)
}

// SetCategoryActive activates or deactivates a category and reports whether a
// row changed, so the caller can tell "not found" from "no change".
func (r *Catalogue) SetCategoryActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE categories SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// Unit families
// ---------------------------------------------------------------------------

// familyColumns is the read model the units management screen renders.
//
// base_uom_id is nullable in the schema — a family and its canonical unit
// cannot both be inserted first — so it is scanned through a pointer and left
// as uuid.Nil when absent rather than COALESCEd to a magic identifier.
const familyColumns = `f.id, f.code, f.name, f.base_uom_id, f.is_active,
	COALESCE(base.code, ''),
	(SELECT count(*) FROM units_of_measure m WHERE m.family_id = f.id AND m.is_active)`

const familyFrom = `
	FROM unit_families f
	LEFT JOIN units_of_measure base ON base.id = f.base_uom_id`

func scanFamily(s interface{ Scan(...any) error }) (catalogue.Family, error) {
	var f catalogue.Family
	var baseUOM *uuid.UUID
	if err := s.Scan(&f.ID, &f.Code, &f.Name, &baseUOM, &f.IsActive,
		&f.BaseUOMCode, &f.UnitCount); err != nil {
		return catalogue.Family{}, err
	}
	if baseUOM != nil {
		f.BaseUOMID = *baseUOM
	}
	return f, nil
}

// UnitUsage reports what depends on a unit of measure.
//
// One query rather than three, because the answer is read to decide a write and
// the three counts have to describe the same moment.
func (r *Catalogue) UnitUsage(ctx context.Context, q platform.Querier, id uuid.UUID) (catalogue.UnitUsage, error) {
	var u catalogue.UnitUsage
	err := q.QueryRowContext(ctx,
		`SELECT
			(SELECT count(*) FROM products p WHERE p.uom_id = $1 AND p.is_active),
			(SELECT count(*) FROM product_units pu WHERE pu.uom_id = $1 AND pu.is_active),
			EXISTS (SELECT 1 FROM unit_families f WHERE f.base_uom_id = $1),
			(SELECT count(*) FROM units_of_measure m
			   JOIN units_of_measure self ON self.id = $1
			  WHERE m.family_id = self.family_id AND m.id <> $1 AND m.is_active)`,
		id).Scan(&u.Products, &u.ProductUnits, &u.IsFamilyBase, &u.SiblingCount)
	if err != nil {
		return catalogue.UnitUsage{}, platform.MapError(err)
	}
	return u, nil
}

// FamilyUnitCount is how many active units a family still holds. It is what
// decides whether the family can be retired.
func (r *Catalogue) FamilyUnitCount(ctx context.Context, q platform.Querier, familyID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM units_of_measure WHERE family_id = $1 AND is_active`, familyID).Scan(&n)
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}

// ListFamilies returns the unit families in code order.
func (r *Catalogue) ListFamilies(ctx context.Context, q platform.Querier, includeInactive bool) ([]catalogue.Family, error) {
	where := ` WHERE f.is_active`
	if includeInactive {
		where = ``
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+familyColumns+familyFrom+where+` ORDER BY f.code`)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	families := make([]catalogue.Family, 0)
	for rows.Next() {
		f, err := scanFamily(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unit family: %w", platform.MapError(err))
		}
		families = append(families, f)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return families, nil
}

// GetFamily returns one family, or platform.ErrNotFound.
func (r *Catalogue) GetFamily(ctx context.Context, q platform.Querier, id uuid.UUID) (catalogue.Family, error) {
	f, err := scanFamily(q.QueryRowContext(ctx,
		`SELECT `+familyColumns+familyFrom+` WHERE f.id = $1`, id))
	if err != nil {
		return catalogue.Family{}, platform.MapError(err)
	}
	return f, nil
}

// CreateFamily inserts a unit family. base_uom_id is deliberately not written
// here: the family's canonical unit is one of its own members, so it cannot
// exist yet.
func (r *Catalogue) CreateFamily(ctx context.Context, q platform.Querier, f catalogue.Family) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO unit_families (id, code, name, is_active) VALUES ($1, $2, $3, $4)`,
		f.ID, f.Code, f.Name, f.IsActive)
	return platform.MapError(err)
}

// UpdateFamily replaces a family's name and reports whether it was found. The
// code is immutable: it is what the seed data and any import file join on.
func (r *Catalogue) UpdateFamily(ctx context.Context, q platform.Querier, f catalogue.Family) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE unit_families SET name = $2, updated_at = now() WHERE id = $1`, f.ID, f.Name))
}

// SetFamilyBaseUnit points a family at its canonical unit — the one every
// member converts through. It refuses a unit from another family, because a
// family whose base belongs elsewhere converts every one of its members wrongly
// and silently.
func (r *Catalogue) SetFamilyBaseUnit(ctx context.Context, q platform.Querier, familyID, uomID uuid.UUID) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE unit_families SET base_uom_id = $2, updated_at = now()
		  WHERE id = $1
		    AND EXISTS (SELECT 1 FROM units_of_measure u WHERE u.id = $2 AND u.family_id = $1)`,
		familyID, uomID))
}

// SetFamilyActive activates or deactivates a family.
func (r *Catalogue) SetFamilyActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE unit_families SET is_active = $2, updated_at = now() WHERE id = $1`, id, active))
}

// ---------------------------------------------------------------------------
// Units of measure
// ---------------------------------------------------------------------------

const unitColumns = `id, code, name, family_id, factor_to_base, decimal_places, is_active`

func scanUnit(s interface{ Scan(...any) error }) (catalogue.UnitOfMeasure, error) {
	var u catalogue.UnitOfMeasure
	err := s.Scan(&u.ID, &u.Code, &u.Name, &u.FamilyID, &u.FactorToBase,
		&u.DecimalPlaces, &u.IsActive)
	return u, err
}

// ListUnits returns units of measure, ordered by family and then by size, so a
// picker reads milligram-gram-kilogram rather than gram-kilogram-milligram.
func (r *Catalogue) ListUnits(ctx context.Context, q platform.Querier, f catalogue.UnitFilter) ([]catalogue.UnitOfMeasure, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + unitColumns + ` FROM units_of_measure WHERE true`)
	args := []any{}

	if !f.IncludeInactive {
		sb.WriteString(` AND is_active`)
	}
	if f.FamilyID != nil {
		args = append(args, *f.FamilyID)
		fmt.Fprintf(&sb, ` AND family_id = $%d`, len(args))
	}
	sb.WriteString(` ORDER BY family_id, factor_to_base, code`)

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	units := make([]catalogue.UnitOfMeasure, 0)
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unit: %w", platform.MapError(err))
		}
		units = append(units, u)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return units, nil
}

// GetUnit returns one unit of measure, or platform.ErrNotFound.
func (r *Catalogue) GetUnit(ctx context.Context, q platform.Querier, id uuid.UUID) (catalogue.UnitOfMeasure, error) {
	u, err := scanUnit(q.QueryRowContext(ctx,
		`SELECT `+unitColumns+` FROM units_of_measure WHERE id = $1`, id))
	if err != nil {
		return catalogue.UnitOfMeasure{}, platform.MapError(err)
	}
	return u, nil
}

// CreateUnit inserts a unit of measure.
func (r *Catalogue) CreateUnit(ctx context.Context, q platform.Querier, u catalogue.UnitOfMeasure) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO units_of_measure (id, code, name, family_id, factor_to_base, decimal_places, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		u.ID, u.Code, u.Name, u.FamilyID, u.FactorToBase, u.DecimalPlaces, u.IsActive)
	return platform.MapError(err)
}

// UpdateUnit replaces a unit's editable fields and reports whether it was
// found.
//
// The code is immutable and the family is not reassignable. Moving a unit
// between families would silently reinterpret every conversion that has ever
// gone through it — a gram that became a unit of volume would convert rice into
// litres — and there is no repair for the readings already taken.
func (r *Catalogue) UpdateUnit(ctx context.Context, q platform.Querier, u catalogue.UnitOfMeasure) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE units_of_measure
		    SET name = $2, factor_to_base = $3, decimal_places = $4, updated_at = now()
		  WHERE id = $1`,
		u.ID, u.Name, u.FactorToBase, u.DecimalPlaces))
}

// SetUnitActive activates or deactivates a unit of measure. There is no
// delete: products and product units reference it with ON DELETE RESTRICT.
func (r *Catalogue) SetUnitActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE units_of_measure SET is_active = $2, updated_at = now() WHERE id = $1`, id, active))
}

// pricedProductFrom is the FROM clause the read model needs, shared by the
// list, get-one and barcode paths so the three cannot drift apart.
//
// The store is always $1. Every join is a LEFT join for the same reason: a
// product with no price row in this store, and a product whose category has
// been removed, must still appear in the listing rather than vanish from it.
//
// The price join now goes THROUGH the default unit, and that is load-bearing.
// product_prices used to be unique on (product_id, store_id), so joining on
// those two columns returned at most one row. It is now unique on
// (product_id, store_id, product_unit_id), so the same join returns one row per
// priced unit — a product sold loose, by the bag and by the sack would appear
// in the listing three times, at three different prices, in no defined order.
// Restricting the join to the default unit restores one row per product and
// makes the four headline price columns mean something specific: what one of
// the default unit costs. idx_product_units_one_default guarantees there is at
// most one of those to find.
const pricedProductFrom = `
	FROM products p
	LEFT JOIN product_units dpu ON dpu.product_id = p.id AND dpu.is_default
	LEFT JOIN product_prices pp
	       ON pp.product_id = p.id AND pp.store_id = $1 AND pp.product_unit_id = dpu.id
	LEFT JOIN categories c ON c.id = p.category_id
	LEFT JOIN vendors v ON v.id = p.preferred_vendor_id`

// pricedProductColumns is the read model shared by the list and get paths.
//
// Stock is a correlated subquery rather than a fourth join. A join to `stock`
// returns one row per batch, so the listing would repeat a product once per lot
// it is held in; aggregating it away would mean a GROUP BY over every column
// above. The subquery gives the same figure with the row shape unchanged, and
// (product_id, store_id) is the leading prefix of stock_product_store_batch_key
// so it is an index lookup. COALESCE makes a product with no stock row read 0,
// which is what "none on the shelf" means - the alternative is a NULL the
// screen would render as blank next to genuine zeroes.
//
// The figure is on-hand quantity, matching what LowStock reports as
// current_stock, so the products screen and the reorder report never disagree
// about how much of something the shop has.
const pricedProductColumns = `
	p.id, p.name, COALESCE(p.name_hi, ''), COALESCE(p.name_kn, ''), p.category_id,
	COALESCE(p.hsn_code, ''), COALESCE(p.sac_code, ''), COALESCE(p.barcode, ''),
	p.uom_id, p.base_unit_qty, p.gst_rate, p.is_free, p.is_active, p.allow_decimal,
	p.min_stock, COALESCE(p.image_url, ''), COALESCE(p.brand, ''), COALESCE(p.description, ''),
	p.tracks_expiry, p.shelf_life_days, p.preferred_vendor_id,
	COALESCE(c.name, ''), COALESCE(v.name, ''),
	COALESCE(pp.mrp, 0), COALESCE(pp.cost_price, 0), COALESCE(pp.sell_price, 0),
	COALESCE(pp.wholesale_sell_price, 0),
	COALESCE((SELECT SUM(s.quantity) FROM stock s
	           WHERE s.product_id = p.id AND s.store_id = $1), 0),
	dpu.id`

func scanPricedProduct(s interface{ Scan(...any) error }) (catalogue.PricedProduct, error) {
	var p catalogue.PricedProduct
	// Nullable because the join is a LEFT one: a product whose units were all
	// deactivated has no default to find, and must still be listed so somebody
	// can go and fix it.
	var defaultUnitID *uuid.UUID
	err := s.Scan(&p.ID, &p.Name, &p.NameHi, &p.NameKn, &p.CategoryID,
		&p.HSNCode, &p.SACCode, &p.Barcode,
		&p.UOMID, &p.BaseUnitQty, &p.GSTRate, &p.IsFree, &p.IsActive, &p.AllowDecimal,
		&p.MinStock, &p.ImageURL, &p.Brand, &p.Description,
		&p.TracksExpiry, &p.ShelfLifeDays, &p.PreferredVendorID,
		&p.CategoryName, &p.PreferredVendorName,
		&p.MRP, &p.CostPrice, &p.SellPrice, &p.WholesaleSellPrice,
		&p.Stock, &defaultUnitID)
	if err != nil {
		return catalogue.PricedProduct{}, err
	}
	if defaultUnitID != nil {
		p.DefaultProductUnitID = *defaultUnitID
	}
	p.Units = []catalogue.SellableUnitsWithPrice{}
	return p, nil
}

// ListProducts returns products with the price that applies in filter.StoreID.
func (r *Catalogue) ListProducts(ctx context.Context, q platform.Querier, f catalogue.ProductFilter) ([]catalogue.PricedProduct, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + pricedProductColumns + pricedProductFrom + `
		WHERE true`)
	args := []any{f.StoreID}

	if !f.IncludeInactive {
		sb.WriteString(` AND p.is_active`)
	}
	if f.CategoryID != nil {
		args = append(args, *f.CategoryID)
		fmt.Fprintf(&sb, ` AND p.category_id = $%d`, len(args))
	}
	if f.Barcode != "" {
		args = append(args, f.Barcode)
		fmt.Fprintf(&sb, ` AND p.barcode = $%d`, len(args))
	}
	if f.Unclassified {
		// COALESCE because both columns are nullable and a blank is the same
		// absence as a NULL here.
		sb.WriteString(` AND COALESCE(p.hsn_code, '') = '' AND COALESCE(p.sac_code, '') = ''`)
	}
	if f.Search != "" {
		// ILIKE, not LIKE. SQLite's LIKE is case-insensitive for ASCII and
		// Postgres's is not, so a straight port would have made every product
		// search silently case-sensitive — no error, just missing results.
		args = append(args, "%"+escapeLike(f.Search)+"%")
		fmt.Fprintf(&sb, ` AND (p.name ILIKE $%d ESCAPE '\' OR p.barcode ILIKE $%d ESCAPE '\')`, len(args), len(args))
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultProductLimit
	}
	if limit > maxProductLimit {
		limit = maxProductLimit
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, ` ORDER BY p.name LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	products := make([]catalogue.PricedProduct, 0)
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		p, err := scanPricedProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("scan product: %w", platform.MapError(err))
		}
		products = append(products, p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}

	// The cursor above is drained before this runs. pgx refuses a second
	// operation on a connection while a result set is open, so reading the
	// units inside the loop would fail as "conn busy" on the first multi-product
	// listing.
	if err := r.attachUnits(ctx, q, f.StoreID, ids, products); err != nil {
		return nil, err
	}
	return products, nil
}

// attachUnits fills in the sellable units and their prices for a page of
// products, in ONE query rather than one per product.
//
// A hundred-product listing would otherwise issue a hundred-and-one queries,
// which is the shape of slowness that only shows up once a real catalogue is
// loaded.
func (r *Catalogue) attachUnits(ctx context.Context, q platform.Querier, storeID uuid.UUID,
	ids []uuid.UUID, products []catalogue.PricedProduct) error {
	if len(ids) == 0 {
		return nil
	}
	byProduct, err := r.SellableUnitsWithPrices(ctx, q, storeID, ids)
	if err != nil {
		return err
	}
	for i := range products {
		if units, ok := byProduct[products[i].ID]; ok {
			products[i].Units = units
		}
	}
	return nil
}

// SellableUnitsWithPrices returns, for each product named, the units it may be
// sold in together with each unit's price in this store.
//
// The order is the one a counter works in: the default first, then smallest to
// largest by qty_in_base. A shop reaches for loose rice far more often than for
// a sack, so the sack must never be the first thing under the cursor.
//
// A unit with no price row is returned with has_price false rather than
// dropped. LEFT JOIN, not JOIN, is what makes that so.
func (r *Catalogue) SellableUnitsWithPrices(ctx context.Context, q platform.Querier,
	storeID uuid.UUID, productIDs []uuid.UUID) (map[uuid.UUID][]catalogue.SellableUnitsWithPrice, error) {
	out := make(map[uuid.UUID][]catalogue.SellableUnitsWithPrice, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}

	rows, err := q.QueryContext(ctx,
		`SELECT `+productUnitColumns+`,
		        pp.product_unit_id IS NOT NULL,
		        COALESCE(pp.mrp, 0), COALESCE(pp.cost_price, 0), COALESCE(pp.sell_price, 0),
		        COALESCE(pp.wholesale_sell_price, 0), COALESCE(pp.is_active, false)
		   FROM product_units pu
		   LEFT JOIN product_prices pp
		          ON pp.product_id = pu.product_id AND pp.store_id = $1
		         AND pp.product_unit_id = pu.id
		  WHERE pu.product_id = ANY($2) AND pu.is_active AND pu.is_sellable
		  ORDER BY pu.product_id, pu.is_default DESC, pu.qty_in_base, pu.label`,
		storeID, productIDs)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	for rows.Next() {
		var entry catalogue.SellableUnitsWithPrice
		u := &entry.Unit
		p := &entry.Price
		if err := rows.Scan(&u.ID, &u.ProductID, &u.UOMID, &u.Label, &u.QtyInBase,
			&u.Barcode, &u.IsDefault, &u.IsSellable, &u.IsPurchasable, &u.IsActive, &u.SortOrder,
			&entry.HasPrice,
			&p.MRP, &p.CostPrice, &p.SellPrice, &p.WholesaleSellPrice, &p.IsActive); err != nil {
			return nil, fmt.Errorf("scan sellable unit: %w", platform.MapError(err))
		}
		// The price carries its unit's identity and size so a price list renders
		// without a second lookup — that is what UnitPrice.Label and QtyInBase
		// are for — and so that EffectiveRatePerBase can show a buyer the sack
		// works out at Rs.42/kg against Rs.45 loose.
		p.ProductID = u.ProductID
		p.ProductUnitID = u.ID
		p.StoreID = storeID
		p.Label = u.Label
		p.QtyInBase = u.QtyInBase
		out[u.ProductID] = append(out[u.ProductID], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// GetProduct returns one product priced for a store. It returns
// platform.ErrNotFound when the product does not exist.
func (r *Catalogue) GetProduct(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (catalogue.PricedProduct, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+pricedProductColumns+pricedProductFrom+`
		  WHERE p.id = $2`, storeID, id)
	p, err := scanPricedProduct(row)
	if err != nil {
		return catalogue.PricedProduct{}, platform.MapError(err)
	}

	byProduct, err := r.SellableUnitsWithPrices(ctx, q, storeID, []uuid.UUID{p.ID})
	if err != nil {
		return catalogue.PricedProduct{}, err
	}
	if units, ok := byProduct[p.ID]; ok {
		p.Units = units
	}
	return p, nil
}

// FindByBarcode resolves a scanned barcode to a product AND the unit that was
// scanned.
//
// A barcode identifies a unit, not a product. The 25 kg sack and the 250 g
// pouch of one rice carry different barcodes so the till can charge the sack's
// own price instead of twenty-five times the loose kilo, and
// idx_product_units_barcode_unique makes that identification unambiguous.
//
// Two places a barcode can live, in priority order:
//
//   - a product unit's own barcode, which names the unit outright;
//   - the product's barcode, which names no unit, so the default one is used.
//
// The UNION ALL asks both questions in one round trip and the ORDER BY settles
// the priority. A product whose own barcode happens to equal one of its units'
// resolves to the unit, which is the more specific answer.
func (r *Catalogue) FindByBarcode(ctx context.Context, q platform.Querier, barcode string, storeID uuid.UUID) (catalogue.ScannedProduct, error) {
	var productID, unitID uuid.UUID
	var viaUnit bool
	err := q.QueryRowContext(ctx,
		`SELECT product_id, unit_id, via_unit FROM (
		     SELECT pu.product_id, pu.id AS unit_id, true AS via_unit
		       FROM product_units pu
		       JOIN products p ON p.id = pu.product_id
		      WHERE pu.barcode = $1 AND pu.is_active AND p.is_active
		     UNION ALL
		     SELECT p.id, dpu.id, false
		       FROM products p
		       JOIN product_units dpu ON dpu.product_id = p.id AND dpu.is_default
		      WHERE p.barcode = $1 AND p.is_active
		 ) AS matched
		 ORDER BY via_unit DESC
		 LIMIT 1`, barcode).Scan(&productID, &unitID, &viaUnit)
	if err != nil {
		return catalogue.ScannedProduct{}, platform.MapError(err)
	}

	product, err := r.GetProduct(ctx, q, productID, storeID)
	if err != nil {
		return catalogue.ScannedProduct{}, err
	}

	scanned := catalogue.ScannedProduct{PricedProduct: product, MatchedUnitBarcode: viaUnit}
	unit, price, hasPrice, err := r.unitWithPrice(ctx, q, unitID, productID, storeID)
	if err != nil {
		return catalogue.ScannedProduct{}, err
	}
	scanned.ScannedUnit = unit
	scanned.ScannedPrice = price
	scanned.ScannedHasPrice = hasPrice
	return scanned, nil
}

// unitWithPrice reads one product unit together with its price in a store.
//
// The unit is read even when no price exists — a LEFT JOIN, not a JOIN —
// because a scan that finds the goods but no price must say "this unit is
// unpriced" rather than "no such barcode". The two send a shopkeeper to
// completely different screens.
func (r *Catalogue) unitWithPrice(ctx context.Context, q platform.Querier, unitID, productID, storeID uuid.UUID) (
	catalogue.ProductUnit, catalogue.UnitPrice, bool, error) {
	var u catalogue.ProductUnit
	var p catalogue.UnitPrice
	var hasPrice bool
	err := q.QueryRowContext(ctx,
		`SELECT `+productUnitColumns+`,
		        pp.product_unit_id IS NOT NULL,
		        COALESCE(pp.mrp, 0), COALESCE(pp.cost_price, 0), COALESCE(pp.sell_price, 0),
		        COALESCE(pp.wholesale_sell_price, 0), COALESCE(pp.is_active, false)
		   FROM product_units pu
		   LEFT JOIN product_prices pp
		          ON pp.product_id = pu.product_id AND pp.store_id = $3
		         AND pp.product_unit_id = pu.id
		  WHERE pu.id = $1 AND pu.product_id = $2`, unitID, productID, storeID).
		Scan(&u.ID, &u.ProductID, &u.UOMID, &u.Label, &u.QtyInBase, &u.Barcode,
			&u.IsDefault, &u.IsSellable, &u.IsPurchasable, &u.IsActive, &u.SortOrder,
			&hasPrice,
			&p.MRP, &p.CostPrice, &p.SellPrice, &p.WholesaleSellPrice, &p.IsActive)
	if err != nil {
		return catalogue.ProductUnit{}, catalogue.UnitPrice{}, false, platform.MapError(err)
	}
	p.ProductID = u.ProductID
	p.ProductUnitID = u.ID
	p.StoreID = storeID
	p.Label = u.Label
	p.QtyInBase = u.QtyInBase
	return u, p, hasPrice, nil
}

// CreateProduct inserts a product.
func (r *Catalogue) CreateProduct(ctx context.Context, q platform.Querier, p catalogue.Product) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO products (id, name, name_hi, name_kn, category_id, hsn_code, sac_code,
			barcode, uom_id, base_unit_qty, gst_rate, is_free, is_active, allow_decimal,
			min_stock, image_url, brand, description,
			tracks_expiry, shelf_life_days, preferred_vendor_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		p.ID, p.Name, p.NameHi, p.NameKn, p.CategoryID, p.HSNCode, p.SACCode,
		nullIfBlank(p.Barcode), p.UOMID, p.BaseUnitQty, p.GSTRate, p.IsFree, p.IsActive,
		p.AllowDecimal, p.MinStock, p.ImageURL, p.Brand, p.Description,
		p.TracksExpiry, p.ShelfLifeDays, p.PreferredVendorID)
	return platform.MapError(err)
}

// UpdateProduct replaces the editable fields of an existing product and
// reports whether the product was found.
func (r *Catalogue) UpdateProduct(ctx context.Context, q platform.Querier, p catalogue.Product) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE products SET
			name = $2, name_hi = $3, name_kn = $4, category_id = $5, hsn_code = $6,
			sac_code = $7, barcode = $8, uom_id = $9, base_unit_qty = $10, gst_rate = $11,
			is_free = $12, allow_decimal = $13, min_stock = $14, image_url = $15,
			brand = $16, description = $17,
			tracks_expiry = $18, shelf_life_days = $19, preferred_vendor_id = $20,
			updated_at = now()
		  WHERE id = $1`,
		p.ID, p.Name, p.NameHi, p.NameKn, p.CategoryID, p.HSNCode,
		p.SACCode, nullIfBlank(p.Barcode), p.UOMID, p.BaseUnitQty, p.GSTRate,
		p.IsFree, p.AllowDecimal, p.MinStock, p.ImageURL, p.Brand, p.Description,
		p.TracksExpiry, p.ShelfLifeDays, p.PreferredVendorID)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// SetProductActive deactivates or reactivates a product. There is no delete:
// a product that has ever been sold is referenced by invoice history, so
// removing it would either fail on a foreign key or destroy the audit trail.
func (r *Catalogue) SetProductActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE products SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// Prices, per unit
// ---------------------------------------------------------------------------

// UpsertPrice sets what one product UNIT sells for in one store, replacing any
// existing price for that unit.
//
// The conflict target is the new constraint. The old
// product_prices_product_store_key was dropped by the units migration, so an
// ON CONFLICT still naming (product_id, store_id) is not a stale comment but a
// runtime error on every price save. Naming the constraint rather than the
// column list means a future rename fails loudly here instead of quietly
// inserting a duplicate.
// It also writes the history entry, in the same statement, which is the only
// way to be sure there is one. There are three paths that set a price — a new
// product's opening figures, the price screen, a bulk sheet — and a rule that
// every one of them must remember to record is a rule that lasts until the
// fourth is written.
//
// Two things about the statement are load-bearing.
//
// The `before` CTE reads the row as it was. Every part of a statement sees the
// same snapshot in Postgres, taken before any of it ran, so `before` is not
// racing the upsert beside it — it is the previous price, reliably, without a
// separate read and the window that would open between them.
//
// History is written only when something actually moved. Re-saving a product
// with its prices untouched, or re-running yesterday's catalogue sheet, is not
// a price change, and a history full of entries that changed nothing is a
// history nobody can read.
func (r *Catalogue) UpsertPrice(ctx context.Context, q platform.Querier,
	p catalogue.UnitPrice, by catalogue.PriceAuthor) error {
	if err := by.Validate(); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`WITH before AS (
		    SELECT mrp, cost_price, sell_price, wholesale_sell_price
		      FROM product_prices
		     WHERE product_id = $1 AND store_id = $2 AND product_unit_id = $3
		 ), upserted AS (
		    INSERT INTO product_prices
		        (product_id, store_id, product_unit_id, mrp, cost_price, sell_price, wholesale_sell_price)
		    VALUES ($1, $2, $3, $4, $5, $6, $7)
		    ON CONFLICT ON CONSTRAINT product_prices_product_store_unit_key DO UPDATE SET
		        mrp = excluded.mrp,
		        cost_price = excluded.cost_price,
		        sell_price = excluded.sell_price,
		        wholesale_sell_price = excluded.wholesale_sell_price,
		        updated_at = now()
		    RETURNING mrp, cost_price, sell_price, wholesale_sell_price
		 )
		 INSERT INTO product_price_history
		    (product_id, store_id, product_unit_id, mrp, cost_price, sell_price,
		     wholesale_sell_price, changed_by, source, note)
		 SELECT $1, $2, $3, u.mrp, u.cost_price, u.sell_price, u.wholesale_sell_price,
		        $8, $9::price_change_source, $10
		   FROM upserted u
		  WHERE NOT EXISTS (
		        SELECT 1 FROM before b
		         WHERE b.mrp = u.mrp
		           AND b.cost_price = u.cost_price
		           AND b.sell_price = u.sell_price
		           AND b.wholesale_sell_price = u.wholesale_sell_price)`,
		p.ProductID, p.StoreID, p.ProductUnitID, p.MRP, p.CostPrice, p.SellPrice, p.WholesaleSellPrice,
		nullIfNilUUID(by.UserID), string(by.Source), by.Note)
	return platform.MapError(err)
}

const priceHistoryColumns = `h.id, h.product_id, h.store_id, h.product_unit_id,
	COALESCE(p.name, ''), COALESCE(pu.label, ''),
	h.mrp, h.cost_price, h.sell_price, h.wholesale_sell_price,
	h.changed_by, COALESCE(u.username, ''), h.source, h.note, h.created_at`

// priceHistoryFrom carries the joins the columns above need: the product's
// name and the unit's label, so a store-wide list of price changes reads
// without a lookup per row, and the username so it says who.
const priceHistoryFrom = `
	FROM product_price_history h
	LEFT JOIN products p ON p.id = h.product_id
	LEFT JOIN product_units pu ON pu.id = h.product_unit_id
	LEFT JOIN users u ON u.id = h.changed_by`

// PriceHistory returns what one price has been, newest first.
//
// Newest first is the order the screen wants and the order the index is built
// in, and it is also what catalogue.AsMovements expects — the entry before a
// given one is the NEXT element, not the previous one.
func (r *Catalogue) PriceHistory(ctx context.Context, q platform.Querier,
	productID, storeID, productUnitID uuid.UUID, limit int) ([]catalogue.PriceChange, error) {
	if limit <= 0 || limit > maxPriceHistory {
		limit = maxPriceHistory
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+priceHistoryColumns+priceHistoryFrom+`
		  WHERE h.product_id = $1 AND h.store_id = $2 AND h.product_unit_id = $3
		  ORDER BY h.created_at DESC, h.id DESC
		  LIMIT $4`, productID, storeID, productUnitID, limit)
	if err != nil {
		return nil, platform.MapError(err)
	}
	return scanPriceHistory(rows)
}

// ProductPriceHistory returns the history of every unit of one product, so a
// product sold loose and by the sack shows both series together.
func (r *Catalogue) ProductPriceHistory(ctx context.Context, q platform.Querier,
	productID, storeID uuid.UUID, limit int) ([]catalogue.PriceChange, error) {
	if limit <= 0 || limit > maxPriceHistory {
		limit = maxPriceHistory
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+priceHistoryColumns+priceHistoryFrom+`
		  WHERE h.product_id = $1 AND h.store_id = $2
		  ORDER BY h.created_at DESC, h.id DESC
		  LIMIT $3`, productID, storeID, limit)
	if err != nil {
		return nil, platform.MapError(err)
	}
	return scanPriceHistory(rows)
}

// RecentPriceChanges is the store's price movements, newest first — what a
// shopkeeper opens when a customer says the rate went up.
func (r *Catalogue) RecentPriceChanges(ctx context.Context, q platform.Querier,
	storeID uuid.UUID, since time.Time, limit int) ([]catalogue.PriceChange, error) {
	if limit <= 0 || limit > maxPriceHistory {
		limit = maxPriceHistory
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+priceHistoryColumns+priceHistoryFrom+`
		  WHERE h.store_id = $1 AND h.created_at >= $2
		  ORDER BY h.created_at DESC, h.id DESC
		  LIMIT $3`, storeID, since, limit)
	if err != nil {
		return nil, platform.MapError(err)
	}
	return scanPriceHistory(rows)
}

// maxPriceHistory bounds a history read. A price moves a handful of times a
// year, so this is a guard against a runaway query rather than a page size
// anyone will reach.
const maxPriceHistory = 500

func scanPriceHistory(rows *sql.Rows) ([]catalogue.PriceChange, error) {
	defer rows.Close()

	history := make([]catalogue.PriceChange, 0)
	for rows.Next() {
		var c catalogue.PriceChange
		var source string
		if err := rows.Scan(&c.ID, &c.ProductID, &c.StoreID, &c.ProductUnitID, &c.ProductName, &c.UnitLabel,
			&c.MRP, &c.CostPrice, &c.SellPrice, &c.WholesaleSellPrice,
			&c.ChangedBy, &c.ChangedByName, &source, &c.Note, &c.At); err != nil {
			return nil, fmt.Errorf("scan price history: %w", platform.MapError(err))
		}
		c.Source = catalogue.PriceSource(source)
		history = append(history, c)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return history, nil
}

// GetPrice returns the stored price row for one unit of a product in a store,
// or platform.ErrNotFound.
//
// It joins product_units for the label and size, so the caller can render
// "25 kg sack — Rs.1,050" without a second query, and so that a price row whose
// unit belongs to another product cannot be returned at all.
func (r *Catalogue) GetPrice(ctx context.Context, q platform.Querier, productID, storeID, productUnitID uuid.UUID) (catalogue.UnitPrice, error) {
	var p catalogue.UnitPrice
	err := q.QueryRowContext(ctx,
		`SELECT pp.product_id, pp.store_id, pp.product_unit_id, pu.label, pu.qty_in_base,
		        pp.mrp, pp.cost_price, pp.sell_price, pp.wholesale_sell_price, pp.is_active
		   FROM product_prices pp
		   JOIN product_units pu ON pu.id = pp.product_unit_id AND pu.product_id = pp.product_id
		  WHERE pp.product_id = $1 AND pp.store_id = $2 AND pp.product_unit_id = $3`,
		productID, storeID, productUnitID).Scan(&p.ProductID, &p.StoreID, &p.ProductUnitID,
		&p.Label, &p.QtyInBase,
		&p.MRP, &p.CostPrice, &p.SellPrice, &p.WholesaleSellPrice, &p.IsActive)
	if err != nil {
		return catalogue.UnitPrice{}, platform.MapError(err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Product units
// ---------------------------------------------------------------------------

// productUnitColumns is the read model the product's units screen renders. The
// pu alias is required: SellableUnitsWithPrices joins a second table.
const productUnitColumns = `pu.id, pu.product_id, pu.uom_id, pu.label, pu.qty_in_base,
	COALESCE(pu.barcode, ''), pu.is_default, pu.is_sellable, pu.is_purchasable,
	pu.is_active, pu.sort_order`

func scanProductUnit(s interface{ Scan(...any) error }) (catalogue.ProductUnit, error) {
	var u catalogue.ProductUnit
	err := s.Scan(&u.ID, &u.ProductID, &u.UOMID, &u.Label, &u.QtyInBase,
		&u.Barcode, &u.IsDefault, &u.IsSellable, &u.IsPurchasable, &u.IsActive, &u.SortOrder)
	return u, err
}

// ListProductUnits returns every way one product may be transacted, in display
// order.
//
// The default comes first because it is what a bare quantity means and what the
// counter offers first, then the shop's own sort_order, then size. Inactive
// units are included: this is the management screen's read, and a unit that
// vanished when it was deactivated could never be brought back.
func (r *Catalogue) ListProductUnits(ctx context.Context, q platform.Querier, productID uuid.UUID) ([]catalogue.ProductUnit, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+productUnitColumns+`
		   FROM product_units pu WHERE pu.product_id = $1
		  ORDER BY pu.is_default DESC, pu.sort_order, pu.qty_in_base, pu.label`, productID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	units := make([]catalogue.ProductUnit, 0)
	for rows.Next() {
		u, err := scanProductUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan product unit: %w", platform.MapError(err))
		}
		units = append(units, u)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return units, nil
}

// GetProductUnit returns one unit of one product, or platform.ErrNotFound.
//
// The product is part of the key rather than trusted from the caller, so a unit
// id belonging to a different product cannot be priced or transacted against
// this one.
func (r *Catalogue) GetProductUnit(ctx context.Context, q platform.Querier, id, productID uuid.UUID) (catalogue.ProductUnit, error) {
	u, err := scanProductUnit(q.QueryRowContext(ctx,
		`SELECT `+productUnitColumns+`
		   FROM product_units pu WHERE pu.id = $1 AND pu.product_id = $2`, id, productID))
	if err != nil {
		return catalogue.ProductUnit{}, platform.MapError(err)
	}
	return u, nil
}

// CreateProductUnit inserts a way of transacting a product.
func (r *Catalogue) CreateProductUnit(ctx context.Context, q platform.Querier, u catalogue.ProductUnit) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO product_units (id, product_id, uom_id, label, qty_in_base, barcode,
			is_default, is_sellable, is_purchasable, is_active, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		u.ID, u.ProductID, u.UOMID, u.Label, u.QtyInBase, nullIfBlank(u.Barcode),
		u.IsDefault, u.IsSellable, u.IsPurchasable, u.IsActive, u.SortOrder)
	return platform.MapError(err)
}

// UpdateProductUnit replaces a unit's editable fields and reports whether it
// was found.
//
// is_default is not among them. Making a unit the default has to clear the
// previous one in the same transaction, so it is SetDefaultProductUnit's job
// alone; allowing it here would let a plain edit trip the partial unique index.
func (r *Catalogue) UpdateProductUnit(ctx context.Context, q platform.Querier, u catalogue.ProductUnit) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE product_units
		    SET uom_id = $3, label = $4, qty_in_base = $5, barcode = $6,
		        is_sellable = $7, is_purchasable = $8, sort_order = $9, updated_at = now()
		  WHERE id = $1 AND product_id = $2`,
		u.ID, u.ProductID, u.UOMID, u.Label, u.QtyInBase, nullIfBlank(u.Barcode),
		u.IsSellable, u.IsPurchasable, u.SortOrder))
}

// SetProductUnitActive deactivates or reactivates a unit. There is no delete:
// invoice and purchase lines reference product_units with ON DELETE RESTRICT,
// so a unit that has ever been transacted has to outlive its usefulness.
//
// Deactivating the default also clears the default flag, in this one statement.
// Leaving it set would keep a hidden unit as the meaning of a bare quantity and
// block any other unit from taking the role, because the partial unique index
// does not care that the row is inactive.
func (r *Catalogue) SetProductUnitActive(ctx context.Context, q platform.Querier, id, productID uuid.UUID, active bool) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE product_units
		    SET is_active = $3, is_default = is_default AND $3, updated_at = now()
		  WHERE id = $1 AND product_id = $2`,
		id, productID, active))
}

// ClearDefaultProductUnit takes the default flag off every unit of a product
// except the one named.
//
// It is half of SetDefaultProductUnit and is never called alone: between the
// two statements a product briefly has no default at all, which is why the
// service runs them inside one transaction.
func (r *Catalogue) ClearDefaultProductUnit(ctx context.Context, q platform.Querier, productID, exceptID uuid.UUID) error {
	_, err := q.ExecContext(ctx,
		`UPDATE product_units SET is_default = false, updated_at = now()
		  WHERE product_id = $1 AND is_default AND id <> $2`, productID, exceptID)
	return platform.MapError(err)
}

// SetDefaultProductUnit marks one unit as the product's default and reports
// whether it was found.
//
// The guard is in the WHERE clause: only an active, sellable unit of this
// product can become the default, because the default is what a bare quantity
// means and a sale must be able to use it. A caller that names an inactive or
// purchase-only unit gets false, not a corrupt default.
func (r *Catalogue) SetDefaultProductUnit(ctx context.Context, q platform.Querier, id, productID uuid.UUID) (bool, error) {
	return rowChanged(q.ExecContext(ctx,
		`UPDATE product_units SET is_default = true, updated_at = now()
		  WHERE id = $1 AND product_id = $2 AND is_active AND is_sellable`, id, productID))
}

// ProductExists reports whether a product is on file, so the service can
// answer "no such product" rather than letting the foreign key produce a 422
// that could equally have meant the parent unit.
func (r *Catalogue) ProductExists(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return false, platform.MapError(err)
	}
	return exists, nil
}

// rowChanged turns an ExecContext result into "did that find anything", so a
// caller can tell a missing row from a write that changed nothing. Both errors
// are returned rather than discarded: RowsAffected can fail on its own.
func rowChanged(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// nullIfBlank keeps the barcode uniqueness index meaningful: many products
// legitimately have no barcode, and NULLs are distinct in a unique index
// whereas repeated empty strings are not.
func nullIfBlank(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// escapeLike neutralises the wildcards a user types into a search box, so that
// searching for "50%" does not match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Lookups the bulk import needs
// ---------------------------------------------------------------------------
//
// An import matches a sheet against what is already stored, and it matches the
// way a person would: by the name they typed, not by an identifier they have
// never seen. That makes these lookups case-insensitive and accent-exact, which
// is the right trade — "ATTA" and "Atta" are the same product, "Aata" is a
// different spelling and a human has to decide.

// FindCategoryByName returns an active category with this name under this
// parent. parentID nil means a top-level category.
//
// The parent is part of the key because two different "Flour" categories under
// two different parents are two different categories, and an import that
// collapsed them would file one shop's baking flour with its atta.
func (r *Catalogue) FindCategoryByName(ctx context.Context, q platform.Querier,
	name string, parentID *uuid.UUID) (catalogue.Category, bool, error) {
	c, err := scanCategory(q.QueryRowContext(ctx,
		`SELECT `+categoryColumns+categoryFrom+
			` WHERE c.is_active AND lower(c.name) = lower($1) AND c.parent_id IS NOT DISTINCT FROM $2
			  LIMIT 1`, name, parentID))
	if err != nil {
		if errors.Is(platform.MapError(err), platform.ErrNotFound) {
			return catalogue.Category{}, false, nil
		}
		return catalogue.Category{}, false, platform.MapError(err)
	}
	return c, true, nil
}

// FindUnitByCodeOrName resolves what somebody typed in a unit column.
//
// Code first: "KG" is unambiguous, and a shop's sheet uses codes far more often
// than names. The name is tried second so "Kilogram" also works.
func (r *Catalogue) FindUnitByCodeOrName(ctx context.Context, q platform.Querier,
	text string) (catalogue.UnitOfMeasure, bool, error) {
	u, err := scanUnit(q.QueryRowContext(ctx,
		`SELECT `+unitColumns+` FROM units_of_measure
		  WHERE is_active AND (lower(code) = lower($1) OR lower(name) = lower($1))
		  ORDER BY (lower(code) = lower($1)) DESC
		  LIMIT 1`, text))
	if err != nil {
		if errors.Is(platform.MapError(err), platform.ErrNotFound) {
			return catalogue.UnitOfMeasure{}, false, nil
		}
		return catalogue.UnitOfMeasure{}, false, platform.MapError(err)
	}
	return u, true, nil
}

// FindProductByBarcode returns the product carrying this barcode, in any store
// and whether or not it is still active.
//
// Inactive products are deliberately included. A barcode is unique across the
// whole table, so a discontinued product still holds its own: reporting "no
// such product" and then failing on the unique index is the worst of both.
func (r *Catalogue) FindProductByBarcode(ctx context.Context, q platform.Querier,
	barcode string) (catalogue.Product, bool, error) {
	return oneProduct(q.QueryRowContext(ctx,
		`SELECT `+plainProductColumns+` FROM products WHERE barcode = $1`, barcode))
}

// FindProductByName returns an active product with this name in this category.
// It is how a row without a barcode is matched — which is most of them, because
// a shop's own sheet rarely carries one.
func (r *Catalogue) FindProductByName(ctx context.Context, q platform.Querier,
	name string, categoryID uuid.UUID) (catalogue.Product, bool, error) {
	return oneProduct(q.QueryRowContext(ctx,
		`SELECT `+plainProductColumns+` FROM products
		  WHERE is_active AND category_id = $2 AND lower(name) = lower($1)
		  LIMIT 1`, name, categoryID))
}

// DefaultProductUnitID returns the unit a bare quantity of this product means,
// which is also the unit an imported price attaches to.
func (r *Catalogue) DefaultProductUnitID(ctx context.Context, q platform.Querier,
	productID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := q.QueryRowContext(ctx,
		`SELECT id FROM product_units WHERE product_id = $1 AND is_default AND is_active`,
		productID).Scan(&id)
	if err != nil {
		if errors.Is(platform.MapError(err), platform.ErrNotFound) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, platform.MapError(err)
	}
	return id, true, nil
}

const plainProductColumns = `id, name, COALESCE(name_hi, ''), COALESCE(name_kn, ''),
	category_id, COALESCE(hsn_code, ''), COALESCE(sac_code, ''), COALESCE(barcode, ''),
	uom_id, base_unit_qty, gst_rate, is_free, is_active, allow_decimal, min_stock,
	COALESCE(image_url, ''), COALESCE(brand, ''), COALESCE(description, ''),
	tracks_expiry, shelf_life_days, preferred_vendor_id`

func oneProduct(row interface{ Scan(...any) error }) (catalogue.Product, bool, error) {
	var p catalogue.Product
	err := row.Scan(&p.ID, &p.Name, &p.NameHi, &p.NameKn, &p.CategoryID,
		&p.HSNCode, &p.SACCode, &p.Barcode, &p.UOMID, &p.BaseUnitQty, &p.GSTRate,
		&p.IsFree, &p.IsActive, &p.AllowDecimal, &p.MinStock,
		&p.ImageURL, &p.Brand, &p.Description,
		&p.TracksExpiry, &p.ShelfLifeDays, &p.PreferredVendorID)
	if err != nil {
		if errors.Is(platform.MapError(err), platform.ErrNotFound) {
			return catalogue.Product{}, false, nil
		}
		return catalogue.Product{}, false, platform.MapError(err)
	}
	return p, true, nil
}

// ---------------------------------------------------------------------------
// Catalogue export
// ---------------------------------------------------------------------------

// categoryPaths builds each category's full "Parent > Child" name.
//
// Recursive, walking up from every category at once and keeping the longest
// path found for each — which is the one that reached a root. The depth guard
// is the same one the cycle check uses and for the same reason: a table that
// somehow holds a loop must refuse an export rather than hang the database.
const categoryPaths = `
WITH RECURSIVE up AS (
    SELECT id, parent_id, name::text AS path, 1 AS depth FROM categories
    UNION ALL
    SELECT u.id, c.parent_id, c.name || ' > ' || u.path, u.depth + 1
      FROM up u JOIN categories c ON c.id = u.parent_id
     WHERE u.depth < ` + maxCategoryDepth + `
), full_path AS (
    SELECT DISTINCT ON (id) id, path FROM up ORDER BY id, depth DESC
)`

// ExportProducts returns the catalogue as rows the importer can read back.
//
// One row per product with its default unit's prices, because that is what an
// import row carries. Ordered by category path then name, so the sheet a
// shopkeeper opens is grouped the way they think about the shop rather than by
// whatever order the rows happen to sit in.
func (r *Catalogue) ExportProducts(ctx context.Context, q platform.Querier,
	storeID uuid.UUID, includeInactive bool, limit int) ([]catalogue.ImportRow, error) {
	where := `WHERE p.is_active`
	if includeInactive {
		where = `WHERE true`
	}
	if limit <= 0 || limit > catalogue.MaxImportRows {
		limit = catalogue.MaxImportRows
	}

	rows, err := q.QueryContext(ctx, categoryPaths+`
		SELECT p.name, COALESCE(p.name_hi, ''), COALESCE(p.name_kn, ''),
		       COALESCE(fp.path, ''), COALESCE(uom.code, ''), COALESCE(p.barcode, ''),
		       COALESCE(p.hsn_code, ''), COALESCE(p.sac_code, ''), p.gst_rate,
		       COALESCE(p.brand, ''), COALESCE(p.description, ''),
		       p.min_stock, p.allow_decimal, p.tracks_expiry, p.shelf_life_days,
		       COALESCE(pp.mrp, 0), COALESCE(pp.cost_price, 0),
		       COALESCE(pp.sell_price, 0), COALESCE(pp.wholesale_sell_price, 0),
		       pp.product_id IS NOT NULL AS has_price
		  FROM products p
		  LEFT JOIN full_path fp ON fp.id = p.category_id
		  LEFT JOIN units_of_measure uom ON uom.id = p.uom_id
		  -- The default unit, which is the one an import row prices. A product
		  -- with no default unit still exports; it comes out unpriced rather
		  -- than not at all, which is the state somebody needs to see.
		  LEFT JOIN product_units pu
		         ON pu.product_id = p.id AND pu.is_default AND pu.is_active
		  LEFT JOIN product_prices pp
		         ON pp.product_id = p.id AND pp.product_unit_id = pu.id AND pp.store_id = $1
		 `+where+`
		 ORDER BY COALESCE(fp.path, ''), p.name
		 LIMIT $2`, storeID, limit)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	exported := make([]catalogue.ImportRow, 0)
	for rows.Next() {
		var row catalogue.ImportRow
		var path string
		if err := rows.Scan(&row.Name, &row.NameHi, &row.NameKn, &path, &row.Unit, &row.Barcode,
			&row.HSNCode, &row.SACCode, &row.GSTRate, &row.Brand, &row.Description,
			&row.MinStock, &row.AllowDecimal, &row.TracksExpiry, &row.ShelfLifeDays,
			&row.MRP, &row.CostPrice, &row.SellPrice, &row.WholesaleSellPrice,
			&row.HasPrice); err != nil {
			return nil, fmt.Errorf("scan export row: %w", platform.MapError(err))
		}
		for _, part := range strings.Split(path, ">") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				row.CategoryPath = append(row.CategoryPath, trimmed)
			}
		}
		exported = append(exported, row)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return exported, nil
}
