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
	"fmt"
	"strings"

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

const categoryColumns = `id, name, COALESCE(name_hi, ''), COALESCE(name_kn, ''),
	parent_id, COALESCE(hsn_code, ''), is_active`

// ListCategories returns the active categories in display order.
func (r *Catalogue) ListCategories(ctx context.Context, q platform.Querier) ([]catalogue.Category, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+categoryColumns+` FROM categories WHERE is_active ORDER BY name`)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	categories := make([]catalogue.Category, 0)
	for rows.Next() {
		var c catalogue.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.NameHi, &c.NameKn,
			&c.ParentID, &c.HSNCode, &c.IsActive); err != nil {
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
const familyColumns = `id, code, name, base_uom_id, is_active`

func scanFamily(s interface{ Scan(...any) error }) (catalogue.Family, error) {
	var f catalogue.Family
	var baseUOM *uuid.UUID
	if err := s.Scan(&f.ID, &f.Code, &f.Name, &baseUOM, &f.IsActive); err != nil {
		return catalogue.Family{}, err
	}
	if baseUOM != nil {
		f.BaseUOMID = *baseUOM
	}
	return f, nil
}

// ListFamilies returns the unit families in code order.
func (r *Catalogue) ListFamilies(ctx context.Context, q platform.Querier, includeInactive bool) ([]catalogue.Family, error) {
	where := ` WHERE is_active`
	if includeInactive {
		where = ``
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+familyColumns+` FROM unit_families`+where+` ORDER BY code`)
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
		`SELECT `+familyColumns+` FROM unit_families WHERE id = $1`, id))
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
func (r *Catalogue) UpsertPrice(ctx context.Context, q platform.Querier, p catalogue.UnitPrice) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO product_prices
		    (product_id, store_id, product_unit_id, mrp, cost_price, sell_price, wholesale_sell_price)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT ON CONSTRAINT product_prices_product_store_unit_key DO UPDATE SET
			mrp = excluded.mrp,
			cost_price = excluded.cost_price,
			sell_price = excluded.sell_price,
			wholesale_sell_price = excluded.wholesale_sell_price,
			updated_at = now()`,
		p.ProductID, p.StoreID, p.ProductUnitID, p.MRP, p.CostPrice, p.SellPrice, p.WholesaleSellPrice)
	return platform.MapError(err)
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
