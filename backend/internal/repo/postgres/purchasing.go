package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gbos/internal/domain/money"
	"gbos/internal/domain/purchasing"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Listing bounds for the purchasing screens.
const (
	defaultPurchasingLimit = 100
	maxPurchasingLimit     = 500
)

// Purchasing reads and writes vendors, purchase orders, goods receipts,
// purchase returns and vendor payments.
//
// Two conventions run through this file and are load-bearing rather than
// stylistic:
//
//  1. Every status transition carries its precondition inside the UPDATE — the
//     shape the old PO-cancel path was alone in getting right — and returns
//     whether a row actually changed. Reading a status in Go and then writing
//     unconditionally is a lost update: two concurrent approvals both read
//     'pending', both write 'approved', and the goods leave the building twice.
//
//  2. Nothing here iterates a *sql.Rows while writing. pgx multiplexes one
//     connection per transaction, so an Exec issued inside a for rows.Next()
//     loop fails outright with "conn busy". Rows are drained into a slice and
//     the writes follow.
type Purchasing struct{}

// NewPurchasing returns a purchasing repository.
func NewPurchasing() *Purchasing { return &Purchasing{} }

// ---------------------------------------------------------------------------
// Vendors
// ---------------------------------------------------------------------------

const vendorColumns = `id, name, COALESCE(phone, ''), COALESCE(address, ''),
	COALESCE(gstin, ''), COALESCE(state, ''), credit_days, credit_limit, is_active`

func scanVendor(s interface{ Scan(...any) error }) (purchasing.Vendor, error) {
	var v purchasing.Vendor
	err := s.Scan(&v.ID, &v.Name, &v.Phone, &v.Address, &v.GSTIN, &v.State,
		&v.CreditDays, &v.CreditLimit, &v.IsActive)
	return v, err
}

// CreateVendor inserts a vendor.
func (r *Purchasing) CreateVendor(ctx context.Context, q platform.Querier, v purchasing.Vendor) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO vendors (id, name, phone, address, gstin, state, credit_days, credit_limit, is_active)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		v.ID, v.Name, nullIfBlank(v.Phone), nullIfBlank(v.Address), nullIfBlank(v.GSTIN),
		nullIfBlank(v.State), v.CreditDays, v.CreditLimit, v.IsActive)
	return platform.MapError(err)
}

// UpdateVendor replaces a vendor's editable fields and reports whether the
// vendor was found.
func (r *Purchasing) UpdateVendor(ctx context.Context, q platform.Querier, v purchasing.Vendor) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE vendors SET name = $2, phone = $3, address = $4, gstin = $5, state = $6,
			credit_days = $7, credit_limit = $8, updated_at = now()
		  WHERE id = $1`,
		v.ID, v.Name, nullIfBlank(v.Phone), nullIfBlank(v.Address), nullIfBlank(v.GSTIN),
		nullIfBlank(v.State), v.CreditDays, v.CreditLimit)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// SetVendorActive deactivates or reactivates a vendor.
func (r *Purchasing) SetVendorActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE vendors SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// GetVendor returns one vendor, or platform.ErrNotFound.
func (r *Purchasing) GetVendor(ctx context.Context, q platform.Querier, id uuid.UUID) (purchasing.Vendor, error) {
	v, err := scanVendor(q.QueryRowContext(ctx, `SELECT `+vendorColumns+` FROM vendors WHERE id = $1`, id))
	if err != nil {
		return purchasing.Vendor{}, platform.MapError(err)
	}
	return v, nil
}

// ListVendors returns vendors matching the filter.
func (r *Purchasing) ListVendors(ctx context.Context, q platform.Querier, f purchasing.VendorFilter) ([]purchasing.Vendor, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + vendorColumns + ` FROM vendors WHERE true`)
	args := make([]any, 0, 2)

	if !f.IncludeInactive {
		sb.WriteString(` AND is_active`)
	}
	if f.Search != "" {
		// ILIKE, not LIKE: SQLite's LIKE is case-insensitive for ASCII and
		// Postgres's is not, so a literal port would silently return nothing
		// for a lower-case search term.
		args = append(args, "%"+escapeLike(f.Search)+"%")
		fmt.Fprintf(&sb, ` AND (name ILIKE $%d ESCAPE '\' OR phone ILIKE $%d ESCAPE '\')`, len(args), len(args))
	}
	args = append(args, clampPurchasingLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY name LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	vendors := make([]purchasing.Vendor, 0)
	for rows.Next() {
		v, err := scanVendor(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vendor: %w", platform.MapError(err))
		}
		vendors = append(vendors, v)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return vendors, nil
}

// VendorExposure returns what the vendor is owed: every non-cancelled purchase
// order raised on them, and every payment made to them.
//
// Both halves matter. The implementation this replaces summed only 'pending'
// and 'partial' orders and never read vendor_payments, so an order dropped out
// of the vendor's exposure the moment its last carton was booked in — which is
// precisely when the money became due. A shop could then order without limit
// from a vendor it had never paid.
//
// The two aggregates are taken in one statement so they are read at one
// snapshot; done as two round trips a payment landing between them would make
// the vendor look better funded than they are.
func (r *Purchasing) VendorExposure(ctx context.Context, q platform.Querier, vendorID uuid.UUID) (purchasing.Exposure, error) {
	var e purchasing.Exposure
	err := q.QueryRowContext(ctx,
		`SELECT
		   COALESCE((SELECT SUM(total) FROM purchase_orders
		              WHERE vendor_id = $1 AND status <> 'cancelled'), 0),
		   COALESCE((SELECT SUM(amount) FROM vendor_payments
		              WHERE vendor_id = $1), 0)`, vendorID).Scan(&e.Ordered, &e.Paid)
	if err != nil {
		return purchasing.Exposure{}, platform.MapError(err)
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// Supporting lookups
// ---------------------------------------------------------------------------

// StoreGSTIN returns the store's GSTIN, which decides whether a supply from a
// vendor is inter-state.
func (r *Purchasing) StoreGSTIN(ctx context.Context, q platform.Querier, storeID uuid.UUID) (string, error) {
	var gstin string
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(gstin, '') FROM stores WHERE id = $1`, storeID).Scan(&gstin); err != nil {
		return "", platform.MapError(err)
	}
	return gstin, nil
}

// TerminalCode returns the series prefix of an active terminal in a store. It
// returns platform.ErrNotFound when the terminal does not exist, is inactive,
// or belongs to another store — all three are the same answer to the caller:
// this terminal may not allocate a number in this store.
func (r *Purchasing) TerminalCode(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID) (string, error) {
	var code string
	if err := q.QueryRowContext(ctx,
		`SELECT code FROM terminals WHERE id = $1 AND store_id = $2 AND is_active`,
		terminalID, storeID).Scan(&code); err != nil {
		return "", platform.MapError(err)
	}
	return code, nil
}

// PurchaseProduct is what purchasing needs to know about a product to price an
// order line and to book a delivery of it into stock.
type PurchaseProduct struct {
	ID      uuid.UUID
	Name    string
	GSTRate money.Rate
	// TracksExpiry and ShelfLifeDays are what the product knows about its own
	// dating, and receiving is where that has to be asked. A product that
	// expires needs a date on every lot; one with a shelf life can have it
	// counted from the manufacture date instead of read off the packet.
	TracksExpiry  bool
	ShelfLifeDays *int
	// Units are every unit the product may be transacted in, with this store's
	// recorded cost for each, in the order a buyer should be offered them.
	Units []PurchaseUnit
}

// PurchaseUnit is one way a product may be bought.
type PurchaseUnit struct {
	ID    uuid.UUID
	Label string
	// QtyInBase is how many of the product's stocking unit one of these is: a
	// loose kilo 1, a 25 kg sack 25.
	QtyInBase     money.Quantity
	IsDefault     bool
	IsPurchasable bool
	IsActive      bool
	// CostPrice is what one of this unit costs — what the store paid for a
	// sack, not for a kilo. It is zero when the store has no price row for the
	// unit, which is not an error on the buy side: a receipt states its own
	// landed cost and this is only the fallback.
	CostPrice money.Money
}

// ProductsForPurchase returns the GST rate, the units and the recorded costs of
// each active product named, keyed by product id.
//
// A product missing from the result is unknown or inactive, which the caller
// reports as a bad request rather than silently taxing at zero — the old code
// discarded the lookup error and carried on with a rate of 0.
//
// The rate, the units and the fallback cost come back together because every
// caller that needs one needs the others, and because the cost is now per unit:
// a query keyed only by product would find several price rows for one product
// and take whichever the plan produced first.
func (r *Purchasing) ProductsForPurchase(ctx context.Context, q platform.Querier, storeID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]PurchaseProduct, error) {
	out := make(map[uuid.UUID]PurchaseProduct, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// The store is $1 and the ids follow it. The units are LEFT JOINed so a
	// product with none still comes back, and the caller can say that the
	// product has no unit set up rather than that it does not exist.
	placeholders, args := purchasingIDArgs(ids, 1)
	rows, err := q.QueryContext(ctx,
		`SELECT p.id, p.name, p.gst_rate, p.tracks_expiry, p.shelf_life_days,
		        pu.id, COALESCE(pu.label, ''), COALESCE(pu.qty_in_base, 0),
		        COALESCE(pu.is_default, false), COALESCE(pu.is_purchasable, false),
		        COALESCE(pu.is_active, false), COALESCE(pp.cost_price, 0)
		   FROM products p
		   LEFT JOIN product_units pu ON pu.product_id = p.id
		   LEFT JOIN product_prices pp
		          ON pp.product_unit_id = pu.id AND pp.store_id = $1
		  WHERE p.is_active AND p.id IN (`+placeholders+`)
		  ORDER BY p.id, pu.is_default DESC, pu.qty_in_base, pu.sort_order`,
		append([]any{storeID}, args...)...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	for rows.Next() {
		var p PurchaseProduct
		var u PurchaseUnit
		var unitID uuid.NullUUID
		if err := rows.Scan(&p.ID, &p.Name, &p.GSTRate, &p.TracksExpiry, &p.ShelfLifeDays,
			&unitID, &u.Label, &u.QtyInBase, &u.IsDefault, &u.IsPurchasable, &u.IsActive,
			&u.CostPrice); err != nil {
			return nil, fmt.Errorf("scan purchase product: %w", platform.MapError(err))
		}
		if existing, seen := out[p.ID]; seen {
			p.Units = existing.Units
		}
		if unitID.Valid {
			u.ID = unitID.UUID
			p.Units = append(p.Units, u)
		}
		out[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// UserName returns a user's username, for the received_by field on a goods
// receipt.
func (r *Purchasing) UserName(ctx context.Context, q platform.Querier, userID uuid.UUID) (string, error) {
	var name string
	if err := q.QueryRowContext(ctx,
		`SELECT username FROM users WHERE id = $1`, userID).Scan(&name); err != nil {
		return "", platform.MapError(err)
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// Document numbering
// ---------------------------------------------------------------------------

// NextDocumentNumber allocates the next value in a terminal's series for a
// document type and financial year, and must be called inside the same
// transaction as the document it numbers.
//
// The row is taken with SELECT ... FOR UPDATE so concurrent tills serialise on
// it, and the increment rolls back with the rest of the transaction if the
// document is not written — a Postgres SEQUENCE would leave a gap there, and
// CGST Rule 46(b) requires the series to be consecutive.
//
// The old purchasing code numbered orders "PO-<timestamp>-<uuid fragment>",
// which is unique but not consecutive and therefore not a series at all.
func (r *Purchasing) NextDocumentNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, docType, finYear string) (int64, error) {
	return nextDocumentNumber(ctx, q, storeID, terminalID, docType, finYear)
}

// ---------------------------------------------------------------------------
// Purchase orders
// ---------------------------------------------------------------------------

const purchaseOrderColumns = `po.id, po.po_no, po.store_id, po.vendor_id, v.name,
	po.order_date, po.expected_date, po.sub_total, po.cgst, po.sgst, po.igst,
	po.discount, po.total, po.status`

func scanPurchaseOrder(s interface{ Scan(...any) error }) (purchasing.PurchaseOrder, error) {
	var po purchasing.PurchaseOrder
	var expected sql.NullTime
	var status string
	err := s.Scan(&po.ID, &po.PONo, &po.StoreID, &po.VendorID, &po.VendorName,
		&po.OrderDate, &expected, &po.SubTotal, &po.CGST, &po.SGST, &po.IGST,
		&po.Discount, &po.Total, &status)
	if err != nil {
		return purchasing.PurchaseOrder{}, err
	}
	if expected.Valid {
		t := expected.Time
		po.ExpectedDate = &t
	}
	po.Status = purchasing.POStatus(status)
	return po, nil
}

// CreatePurchaseOrder inserts a purchase order header.
func (r *Purchasing) CreatePurchaseOrder(ctx context.Context, q platform.Querier, po purchasing.PurchaseOrder) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO purchase_orders
			(id, po_no, store_id, vendor_id, order_date, expected_date,
			 sub_total, cgst, sgst, igst, discount, total, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::purchase_order_status)`,
		po.ID, po.PONo, po.StoreID, po.VendorID, po.OrderDate, po.ExpectedDate,
		po.SubTotal, po.CGST, po.SGST, po.IGST, po.Discount, po.Total, string(po.Status))
	return platform.MapError(err)
}

// CreatePurchaseItem inserts one purchase order line.
//
// Both quantities are written: unit_qty and product_unit_id are what was
// ordered — ten sacks — and quantity is the same amount in the product's base
// stocking unit, which is what a receipt is booked against and what stock
// counts. The invariant between them is quantity = unit_qty * qty_in_base.
//
// The rate is written through Decimal() rather than as a Money so the full
// NUMERIC(14,4) precision of the column survives; Money.Value emits at the
// paisa, which would silently truncate a four-place rate on the way in and make
// the stored order disagree with the totals computed from it.
func (r *Purchasing) CreatePurchaseItem(ctx context.Context, q platform.Querier, it purchasing.PurchaseItem) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO purchase_items
			(id, po_id, product_id, product_unit_id, unit_qty, quantity, uom_id,
			 received_qty, rate, discount, cgst, sgst, igst, total)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		it.ID, it.POID, it.ProductID, nullIfNilUUID(it.ProductUnitID), it.UnitQty,
		it.Quantity, it.UOMID, it.ReceivedQty,
		it.Rate.Decimal(), it.Discount, it.CGST, it.SGST, it.IGST, it.Total)
	return platform.MapError(err)
}

// GetPurchaseOrder returns one order in a store, or platform.ErrNotFound.
func (r *Purchasing) GetPurchaseOrder(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (purchasing.PurchaseOrder, error) {
	po, err := scanPurchaseOrder(q.QueryRowContext(ctx,
		`SELECT `+purchaseOrderColumns+`
		   FROM purchase_orders po JOIN vendors v ON v.id = po.vendor_id
		  WHERE po.id = $1 AND po.store_id = $2`, id, storeID))
	if err != nil {
		return purchasing.PurchaseOrder{}, platform.MapError(err)
	}
	return po, nil
}

// ListPurchaseOrders returns the orders of one store, newest first.
func (r *Purchasing) ListPurchaseOrders(ctx context.Context, q platform.Querier, f purchasing.POFilter) ([]purchasing.PurchaseOrder, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + purchaseOrderColumns + `
		FROM purchase_orders po JOIN vendors v ON v.id = po.vendor_id
		WHERE po.store_id = $1`)
	args := []any{f.StoreID}

	if f.VendorID != nil {
		args = append(args, *f.VendorID)
		fmt.Fprintf(&sb, ` AND po.vendor_id = $%d`, len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		fmt.Fprintf(&sb, ` AND po.status = $%d::purchase_order_status`, len(args))
	}
	args = append(args, clampPurchasingLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY po.order_date DESC, po.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	orders := make([]purchasing.PurchaseOrder, 0)
	for rows.Next() {
		po, err := scanPurchaseOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan purchase order: %w", platform.MapError(err))
		}
		orders = append(orders, po)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return orders, nil
}

// ListPurchaseItems returns an order's lines, ordered by id so that receipt
// allocation across duplicate product lines is deterministic.
//
// forUpdate takes a row lock on each line. The receiving path passes true: it
// reads the outstanding quantities, decides how much may be booked in, and then
// writes — a read-then-write that without the lock lets two concurrent
// receipts each see the same outstanding quantity and both book against it.
func (r *Purchasing) ListPurchaseItems(ctx context.Context, q platform.Querier, poID uuid.UUID, forUpdate bool) ([]purchasing.PurchaseItem, error) {
	// The unit is joined in so an order prints "10 x 25 kg sack" rather than
	// "250". It is a LEFT JOIN and a nullable read because the column was added
	// to a table that already had rows; the migration backfilled every one of
	// them to the product's default unit, and a line without a unit stays
	// readable rather than disappearing from its own order.
	query := `SELECT pi.id, pi.po_id, pi.product_id, p.name,
			pi.product_unit_id, COALESCE(pu.label, ''), pi.unit_qty, pi.quantity, pi.uom_id,
			pi.received_qty, pi.rate, pi.discount, pi.cgst, pi.sgst, pi.igst, pi.total
		   FROM purchase_items pi
		   JOIN products p ON p.id = pi.product_id
		   LEFT JOIN product_units pu ON pu.id = pi.product_unit_id
		  WHERE pi.po_id = $1
		  ORDER BY pi.id`
	if forUpdate {
		// Only purchase_items is locked; products and product_units are
		// read-only here and OF keeps the joins from taking pointless locks on
		// the catalogue.
		query += ` FOR UPDATE OF pi`
	}

	rows, err := q.QueryContext(ctx, query, poID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	items := make([]purchasing.PurchaseItem, 0)
	for rows.Next() {
		var it purchasing.PurchaseItem
		var uom, unit uuid.NullUUID
		if err := rows.Scan(&it.ID, &it.POID, &it.ProductID, &it.ProductName,
			&unit, &it.UnitLabel, &it.UnitQty, &it.Quantity, &uom,
			&it.ReceivedQty, &it.Rate, &it.Discount, &it.CGST, &it.SGST, &it.IGST, &it.Total); err != nil {
			return nil, fmt.Errorf("scan purchase item: %w", platform.MapError(err))
		}
		if uom.Valid {
			id := uom.UUID
			it.UOMID = &id
		}
		if unit.Valid {
			it.ProductUnitID = unit.UUID
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return items, nil
}

// AddReceivedQty books qty against one purchase order line and reports whether
// the line took it.
//
// The over-receipt cap is in the WHERE clause, not only in the caller's
// arithmetic. The service checks it too, with a message naming the numbers, but
// that check reads before it writes and two concurrent receipts can both pass
// it; this guard is what makes the second one fail.
func (r *Purchasing) AddReceivedQty(ctx context.Context, q platform.Querier, itemID uuid.UUID, qty money.Quantity) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE purchase_items SET received_qty = received_qty + $2
		  WHERE id = $1 AND received_qty + $2 <= quantity`, itemID, qty)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// SetPurchaseOrderStatus moves an order to next, but only from one of the
// states in from, and reports whether a row actually changed.
//
// This is the shape the old Cancel path was alone in getting right: the
// precondition is in the UPDATE and RowsAffected is checked. Every other
// transition in the code this replaces wrote `SET status = ?` with no guard,
// which is how a cancelled order could be resurrected to 'received' by a goods
// receipt, and how a fully received one could be knocked back to 'partial'.
func (r *Purchasing) SetPurchaseOrderStatus(ctx context.Context, q platform.Querier, id, storeID uuid.UUID, next purchasing.POStatus, from []purchasing.POStatus) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("postgres: SetPurchaseOrderStatus needs at least one permitted source status")
	}
	allowed := make([]string, 0, len(from))
	for _, s := range from {
		allowed = append(allowed, string(s))
	}

	var sb strings.Builder
	sb.WriteString(`UPDATE purchase_orders SET status = $3::purchase_order_status, updated_at = now()
		 WHERE id = $1 AND store_id = $2 AND status IN (`)
	args := []any{id, storeID, string(next)}
	for i, s := range allowed {
		if i > 0 {
			sb.WriteString(", ")
		}
		args = append(args, s)
		fmt.Fprintf(&sb, `$%d::purchase_order_status`, len(args))
	}
	sb.WriteString(`)`)

	res, err := q.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// LockPurchaseOrder takes a row lock on an order and returns its current
// status, so the receiving path decides on a state nobody can change under it.
//
// The order number comes back from the same locked row: the receipt built
// against it carries the number, and reading it here costs nothing beyond a
// column in a query that had to run anyway.
func (r *Purchasing) LockPurchaseOrder(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (status purchasing.POStatus, poNo string, err error) {
	var raw string
	if err := q.QueryRowContext(ctx,
		`SELECT status, po_no FROM purchase_orders WHERE id = $1 AND store_id = $2 FOR UPDATE`,
		id, storeID).Scan(&raw, &poNo); err != nil {
		return "", "", platform.MapError(err)
	}
	return purchasing.POStatus(raw), poNo, nil
}

// ---------------------------------------------------------------------------
// Goods receipts
// ---------------------------------------------------------------------------

// goodsReceiptColumns is the read model of a receipt header. Every path that
// selects it already joins purchase_orders to scope the receipt to a store, so
// po.po_no comes free; the line count is a correlated subquery rather than a
// join and a GROUP BY, which would otherwise have to name every column here.
const goodsReceiptColumns = `gr.id, gr.po_id, po.po_no, gr.received_date, gr.received_by,
	COALESCE(gr.notes, ''),
	(SELECT count(*) FROM goods_receipt_items gri WHERE gri.gr_id = gr.id),
	gr.created_at`

func scanGoodsReceipt(s interface{ Scan(...any) error }) (purchasing.GoodsReceipt, error) {
	var g purchasing.GoodsReceipt
	err := s.Scan(&g.ID, &g.POID, &g.PONo, &g.ReceivedDate, &g.ReceivedBy, &g.Notes,
		&g.ItemCount, &g.CreatedAt)
	return g, err
}

// CreateGoodsReceipt inserts a goods receipt header.
func (r *Purchasing) CreateGoodsReceipt(ctx context.Context, q platform.Querier, g purchasing.GoodsReceipt) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO goods_receipts (id, po_id, received_date, received_by, notes)
		 VALUES ($1,$2,$3,$4,$5)`,
		g.ID, g.POID, g.ReceivedDate, g.ReceivedBy, nullIfBlank(g.Notes))
	return platform.MapError(err)
}

// CreateGoodsReceiptItem inserts one goods receipt line.
func (r *Purchasing) CreateGoodsReceiptItem(ctx context.Context, q platform.Querier, it purchasing.GoodsReceiptItem) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO goods_receipt_items
			(id, gr_id, product_id, ordered_qty, received_qty, accepted_qty, rejected_qty, reason)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		it.ID, it.GRID, it.ProductID, it.OrderedQty, it.ReceivedQty,
		it.AcceptedQty, it.RejectedQty, nullIfBlank(it.Reason))
	return platform.MapError(err)
}

// GetGoodsReceipt returns one receipt visible to a store.
func (r *Purchasing) GetGoodsReceipt(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (purchasing.GoodsReceipt, error) {
	g, err := scanGoodsReceipt(q.QueryRowContext(ctx,
		`SELECT `+goodsReceiptColumns+`
		   FROM goods_receipts gr JOIN purchase_orders po ON po.id = gr.po_id
		  WHERE gr.id = $1 AND po.store_id = $2`, id, storeID))
	if err != nil {
		return purchasing.GoodsReceipt{}, platform.MapError(err)
	}
	return g, nil
}

// ListGoodsReceipts returns a store's receipts, optionally for one order.
func (r *Purchasing) ListGoodsReceipts(ctx context.Context, q platform.Querier, storeID uuid.UUID, poID *uuid.UUID, limit int) ([]purchasing.GoodsReceipt, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + goodsReceiptColumns + `
		FROM goods_receipts gr JOIN purchase_orders po ON po.id = gr.po_id
		WHERE po.store_id = $1`)
	args := []any{storeID}
	if poID != nil {
		args = append(args, *poID)
		fmt.Fprintf(&sb, ` AND gr.po_id = $%d`, len(args))
	}
	args = append(args, clampPurchasingLimit(limit))
	fmt.Fprintf(&sb, ` ORDER BY gr.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	receipts := make([]purchasing.GoodsReceipt, 0)
	for rows.Next() {
		g, err := scanGoodsReceipt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan goods receipt: %w", platform.MapError(err))
		}
		receipts = append(receipts, g)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return receipts, nil
}

// ListGoodsReceiptItems returns a receipt's lines.
func (r *Purchasing) ListGoodsReceiptItems(ctx context.Context, q platform.Querier, grID uuid.UUID) ([]purchasing.GoodsReceiptItem, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT gri.id, gri.gr_id, gri.product_id, p.name, COALESCE(gri.ordered_qty, 0), gri.received_qty,
			COALESCE(gri.accepted_qty, 0), COALESCE(gri.rejected_qty, 0), COALESCE(gri.reason, '')
		   FROM goods_receipt_items gri JOIN products p ON p.id = gri.product_id
		  WHERE gri.gr_id = $1 ORDER BY gri.id`, grID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	items := make([]purchasing.GoodsReceiptItem, 0)
	for rows.Next() {
		var it purchasing.GoodsReceiptItem
		if err := rows.Scan(&it.ID, &it.GRID, &it.ProductID, &it.ProductName, &it.OrderedQty, &it.ReceivedQty,
			&it.AcceptedQty, &it.RejectedQty, &it.Reason); err != nil {
			return nil, fmt.Errorf("scan goods receipt item: %w", platform.MapError(err))
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// Purchase returns
// ---------------------------------------------------------------------------

const purchaseReturnColumns = `pr.id, pr.po_id, pr.vendor_id, v.name, pr.store_id,
	pr.return_date, pr.total, pr.status, COALESCE(pr.reason, ''), pr.user_id, pr.created_at`

func scanPurchaseReturn(s interface{ Scan(...any) error }) (purchasing.PurchaseReturn, error) {
	var pr purchasing.PurchaseReturn
	var poID uuid.NullUUID
	var status string
	err := s.Scan(&pr.ID, &poID, &pr.VendorID, &pr.VendorName, &pr.StoreID,
		&pr.ReturnDate, &pr.Total, &status, &pr.Reason, &pr.UserID, &pr.CreatedAt)
	if err != nil {
		return purchasing.PurchaseReturn{}, err
	}
	if poID.Valid {
		id := poID.UUID
		pr.POID = &id
	}
	pr.Status = purchasing.ReturnStatus(status)
	return pr, nil
}

// CreatePurchaseReturn inserts a purchase return header.
func (r *Purchasing) CreatePurchaseReturn(ctx context.Context, q platform.Querier, pr purchasing.PurchaseReturn) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO purchase_returns
			(id, po_id, vendor_id, store_id, return_date, total, status, reason, user_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7::purchase_return_status,$8,$9)`,
		pr.ID, pr.POID, pr.VendorID, pr.StoreID, pr.ReturnDate, pr.Total,
		string(pr.Status), nullIfBlank(pr.Reason), pr.UserID)
	return platform.MapError(err)
}

// CreatePurchaseReturnItem inserts one purchase return line.
func (r *Purchasing) CreatePurchaseReturnItem(ctx context.Context, q platform.Querier, it purchasing.PurchaseReturnItem) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO purchase_return_items (id, return_id, product_id, quantity, rate, total)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		it.ID, it.ReturnID, it.ProductID, it.Quantity, it.Rate.Decimal(), it.Total)
	return platform.MapError(err)
}

// GetPurchaseReturn returns one return in a store.
func (r *Purchasing) GetPurchaseReturn(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (purchasing.PurchaseReturn, error) {
	pr, err := scanPurchaseReturn(q.QueryRowContext(ctx,
		`SELECT `+purchaseReturnColumns+`
		   FROM purchase_returns pr JOIN vendors v ON v.id = pr.vendor_id
		  WHERE pr.id = $1 AND pr.store_id = $2`, id, storeID))
	if err != nil {
		return purchasing.PurchaseReturn{}, platform.MapError(err)
	}
	return pr, nil
}

// ListPurchaseReturns returns a store's purchase returns, newest first.
func (r *Purchasing) ListPurchaseReturns(ctx context.Context, q platform.Querier, f purchasing.ReturnFilter) ([]purchasing.PurchaseReturn, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + purchaseReturnColumns + `
		FROM purchase_returns pr JOIN vendors v ON v.id = pr.vendor_id
		WHERE pr.store_id = $1`)
	args := []any{f.StoreID}
	if f.VendorID != nil {
		args = append(args, *f.VendorID)
		fmt.Fprintf(&sb, ` AND pr.vendor_id = $%d`, len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		fmt.Fprintf(&sb, ` AND pr.status = $%d::purchase_return_status`, len(args))
	}
	args = append(args, clampPurchasingLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY pr.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	returns := make([]purchasing.PurchaseReturn, 0)
	for rows.Next() {
		pr, err := scanPurchaseReturn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan purchase return: %w", platform.MapError(err))
		}
		returns = append(returns, pr)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return returns, nil
}

// ListPurchaseReturnItems returns a return's lines.
//
// The approval path calls this and drains the whole result before it writes
// anything. That is not tidiness: the implementation this replaces held this
// cursor open and issued the stock UPDATE inside for rows.Next(), which under
// pgx fails with "conn busy" because a transaction owns one connection and a
// streaming query has it.
func (r *Purchasing) ListPurchaseReturnItems(ctx context.Context, q platform.Querier, returnID uuid.UUID) ([]purchasing.PurchaseReturnItem, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, return_id, product_id, quantity, rate, total
		   FROM purchase_return_items WHERE return_id = $1 ORDER BY id`, returnID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	items := make([]purchasing.PurchaseReturnItem, 0)
	for rows.Next() {
		var it purchasing.PurchaseReturnItem
		if err := rows.Scan(&it.ID, &it.ReturnID, &it.ProductID, &it.Quantity, &it.Rate, &it.Total); err != nil {
			return nil, fmt.Errorf("scan purchase return item: %w", platform.MapError(err))
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return items, nil
}

// ApprovePurchaseReturn moves a return from pending to approved and reports
// whether it was this call that did it.
//
// The guard is in the UPDATE. Approval is what moves the stock, so a second
// concurrent approval that also succeeded would move it twice; the old code
// read the status in Go and then wrote unconditionally, which is exactly that
// race.
func (r *Purchasing) ApprovePurchaseReturn(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE purchase_returns SET status = 'approved'
		  WHERE id = $1 AND store_id = $2 AND status = 'pending'`, id, storeID)
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
// Vendor payments
// ---------------------------------------------------------------------------

// CreateVendorPayment records a payment to a vendor.
func (r *Purchasing) CreateVendorPayment(ctx context.Context, q platform.Querier, p purchasing.VendorPayment) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO vendor_payments
			(id, vendor_id, po_id, amount, payment_mode, reference, payment_date, notes, user_id, store_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		p.ID, p.VendorID, p.POID, p.Amount, p.PaymentMode, nullIfBlank(p.Reference),
		p.PaymentDate, nullIfBlank(p.Notes), p.UserID, p.StoreID)
	return platform.MapError(err)
}

// ListVendorPayments returns a store's payments, newest first.
//
// The vendor is joined for its name, the same way ListPurchaseReturns does it.
// Without the join the payments screen had only a vendor id to render and
// showed a raw UUID in the Vendor column. The join is an inner one because
// vendor_payments.vendor_id is NOT NULL and REFERENCES vendors, so a payment
// with no vendor row cannot exist and a LEFT JOIN would only suggest it could.
func (r *Purchasing) ListVendorPayments(ctx context.Context, q platform.Querier, storeID uuid.UUID, vendorID *uuid.UUID, limit int) ([]purchasing.VendorPayment, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT vp.id, vp.vendor_id, v.name, vp.po_id, vp.amount, vp.payment_mode,
			COALESCE(vp.reference, ''), vp.payment_date, COALESCE(vp.notes, ''),
			vp.user_id, vp.store_id, vp.created_at
		  FROM vendor_payments vp JOIN vendors v ON v.id = vp.vendor_id
		 WHERE vp.store_id = $1`)
	args := []any{storeID}
	if vendorID != nil {
		args = append(args, *vendorID)
		fmt.Fprintf(&sb, ` AND vp.vendor_id = $%d`, len(args))
	}
	args = append(args, clampPurchasingLimit(limit))
	fmt.Fprintf(&sb, ` ORDER BY vp.payment_date DESC, vp.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	payments := make([]purchasing.VendorPayment, 0)
	for rows.Next() {
		var p purchasing.VendorPayment
		var poID uuid.NullUUID
		if err := rows.Scan(&p.ID, &p.VendorID, &p.VendorName, &poID, &p.Amount, &p.PaymentMode,
			&p.Reference, &p.PaymentDate, &p.Notes, &p.UserID, &p.StoreID, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor payment: %w", platform.MapError(err))
		}
		if poID.Valid {
			id := poID.UUID
			p.POID = &id
		}
		payments = append(payments, p)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return payments, nil
}

// ---------------------------------------------------------------------------
// Helpers local to purchasing
// ---------------------------------------------------------------------------

// clampPurchasingLimit bounds a caller-supplied listing size.
func clampPurchasingLimit(limit int) int {
	if limit <= 0 {
		return defaultPurchasingLimit
	}
	if limit > maxPurchasingLimit {
		return maxPurchasingLimit
	}
	return limit
}

// purchasingIDArgs renders an IN list's placeholders — "$1, $2, ...", starting
// after offset existing parameters — together with the matching argument slice.
//
// Placeholders are generated rather than the ids being interpolated into the
// statement. The ids are internal uuids and could not carry an injection today,
// but a query that is safe only because of where its values happen to come from
// is one refactor away from not being.
func purchasingIDArgs(ids []uuid.UUID, offset int) (string, []any) {
	var sb strings.Builder
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(", ")
		}
		args = append(args, id)
		fmt.Fprintf(&sb, "$%d", offset+i+1)
	}
	return sb.String(), args
}
