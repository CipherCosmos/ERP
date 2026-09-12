package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Billing reads and writes sales invoices.
type Billing struct{}

// NewBilling returns a billing repository.
func NewBilling() *Billing { return &Billing{} }

const billingMaxLimit = 500

// nullIfNilUUID writes a NULL rather than the nil uuid for an optional foreign
// key. The nil uuid is a real value as far as Postgres is concerned, so it
// fails the reference with a message about a key that does not exist instead of
// saying that no key was given.
func nullIfNilUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

const invoiceColumns = `i.id, i.invoice_no, i.store_id, i.user_id, i.terminal_id, i.customer_id,
	i.invoice_date, i.sub_total, i.cgst, i.sgst, i.igst, i.discount, i.round_off, i.total,
	i.status, i.payment_mode, i.is_gstr1_filed, i.gstr1_month, i.created_at`

func scanInvoice(s interface{ Scan(...any) error }) (billing.Invoice, error) {
	var inv billing.Invoice
	err := s.Scan(&inv.ID, &inv.InvoiceNo, &inv.StoreID, &inv.UserID, &inv.TerminalID, &inv.CustomerID,
		&inv.InvoiceDate, &inv.SubTotal, &inv.CGST, &inv.SGST, &inv.IGST, &inv.Discount,
		&inv.RoundOff, &inv.Total, &inv.Status, &inv.PaymentMode, &inv.IsGSTR1Filed,
		&inv.GSTR1Month, &inv.CreatedAt)
	return inv, err
}

// NextInvoiceNumber allocates the next number in a terminal's invoice series.
// It must run inside the transaction that writes the invoice, so that a rolled
// back sale returns its number instead of leaving a gap in the series.
func (r *Billing) NextInvoiceNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, finYear string) (int64, error) {
	return nextDocumentNumber(ctx, q, storeID, terminalID, "invoice", finYear)
}

// InsertInvoice writes the invoice header.
func (r *Billing) InsertInvoice(ctx context.Context, q platform.Querier, inv billing.Invoice) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO invoices (id, invoice_no, store_id, user_id, terminal_id, customer_id,
			invoice_date, sub_total, cgst, sgst, igst, discount, round_off, total,
			status, payment_mode)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::invoice_status,$16)`,
		inv.ID, inv.InvoiceNo, inv.StoreID, inv.UserID, inv.TerminalID, inv.CustomerID,
		inv.InvoiceDate, inv.SubTotal, inv.CGST, inv.SGST, inv.IGST, inv.Discount,
		inv.RoundOff, inv.Total, string(inv.Status), string(inv.PaymentMode))
	return platform.MapError(err)
}

// InsertItems writes the invoice lines.
//
// Both quantities are written. unit_qty and product_unit_id are what the
// customer asked for and what the bill prints; quantity is the same amount in
// the product's base stocking unit, which is what stock, COGS and every
// existing report read. The invariant between them —
// quantity = unit_qty * qty_in_base — is established by the caller and stated
// in the migration that added the columns.
func (r *Billing) InsertItems(ctx context.Context, q platform.Querier, items []billing.Item) error {
	for _, it := range items {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO invoice_items (id, invoice_id, product_id, batch_no, product_unit_id,
				unit_qty, quantity, rate, discount, cgst, sgst, igst, total, unit_cost_at_sale)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			it.ID, it.InvoiceID, it.ProductID, it.BatchNo, nullIfNilUUID(it.ProductUnitID),
			it.UnitQty, it.Quantity, it.Rate,
			it.Discount, it.CGST, it.SGST, it.IGST, it.Total, it.UnitCostAtSale); err != nil {
			return platform.MapError(err)
		}
	}
	return nil
}

// GetInvoice returns one invoice, scoped to a store.
func (r *Billing) GetInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (billing.Invoice, error) {
	inv, err := scanInvoice(q.QueryRowContext(ctx,
		`SELECT `+invoiceColumns+` FROM invoices i WHERE i.id = $1 AND i.store_id = $2`, id, storeID))
	if err != nil {
		return billing.Invoice{}, platform.MapError(err)
	}
	return inv, nil
}

// LockInvoice reads an invoice FOR UPDATE inside a transaction, so a decision
// made on its status cannot be raced by a concurrent void.
func (r *Billing) LockInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (billing.Invoice, error) {
	inv, err := scanInvoice(q.QueryRowContext(ctx,
		`SELECT `+invoiceColumns+` FROM invoices i WHERE i.id = $1 AND i.store_id = $2 FOR UPDATE`, id, storeID))
	if err != nil {
		return billing.Invoice{}, platform.MapError(err)
	}
	return inv, nil
}

// ItemsForInvoice returns an invoice's lines.
//
// The unit is joined in so a bill can print "2 x 25 kg sack" rather than "50".
// The join is a LEFT one and product_unit_id is read as a nullable: the column
// was added by migration to a table that already had rows, and although the
// backfill set every one of them to the product's default unit, a LEFT JOIN is
// what keeps a line with no unit readable rather than invisible.
func (r *Billing) ItemsForInvoice(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) ([]billing.Item, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT ii.id, ii.invoice_id, ii.product_id, COALESCE(p.name, ''), COALESCE(ii.batch_no, ''),
			ii.product_unit_id, COALESCE(pu.label, ''), ii.unit_qty, ii.quantity, ii.rate,
			ii.discount, ii.cgst, ii.sgst, ii.igst, ii.total, ii.unit_cost_at_sale
		   FROM invoice_items ii
		   LEFT JOIN product_units pu ON pu.id = ii.product_unit_id
		   -- LEFT JOIN, not JOIN: product_id is ON DELETE RESTRICT so a row
		   -- should always match, but a line that somehow lost its product must
		   -- still appear on the bill it was charged on.
		   LEFT JOIN products p ON p.id = ii.product_id
		  WHERE ii.invoice_id = $1 ORDER BY ii.created_at, ii.id`, invoiceID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	items := make([]billing.Item, 0)
	for rows.Next() {
		var it billing.Item
		var unitID uuid.NullUUID
		if err := rows.Scan(&it.ID, &it.InvoiceID, &it.ProductID, &it.ProductName, &it.BatchNo,
			&unitID, &it.UnitLabel, &it.UnitQty, &it.Quantity,
			&it.Rate, &it.Discount, &it.CGST, &it.SGST, &it.IGST, &it.Total, &it.UnitCostAtSale); err != nil {
			return nil, fmt.Errorf("scan invoice item: %w", platform.MapError(err))
		}
		if unitID.Valid {
			it.ProductUnitID = unitID.UUID
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return items, nil
}

// InvoiceFilter narrows a listing.
type InvoiceFilter struct {
	StoreID    uuid.UUID
	CustomerID *uuid.UUID
	TerminalID *uuid.UUID
	From       *time.Time
	To         *time.Time
	// Status empty means every status. The listing screen wants voided
	// invoices visible; the revenue queries emphatically do not.
	Status billing.Status
	Limit  int
}

// ListInvoices returns invoices matching a filter.
func (r *Billing) ListInvoices(ctx context.Context, q platform.Querier, f InvoiceFilter) ([]billing.Invoice, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + invoiceColumns + ` FROM invoices i WHERE i.store_id = $1`)
	args := []any{f.StoreID}

	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		fmt.Fprintf(&sb, ` AND i.customer_id = $%d`, len(args))
	}
	if f.TerminalID != nil {
		args = append(args, *f.TerminalID)
		fmt.Fprintf(&sb, ` AND i.terminal_id = $%d`, len(args))
	}
	if f.From != nil {
		args = append(args, *f.From)
		fmt.Fprintf(&sb, ` AND i.invoice_date >= $%d`, len(args))
	}
	if f.To != nil {
		args = append(args, *f.To)
		fmt.Fprintf(&sb, ` AND i.invoice_date <= $%d`, len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		fmt.Fprintf(&sb, ` AND i.status = $%d::invoice_status`, len(args))
	}

	limit := f.Limit
	if limit <= 0 || limit > billingMaxLimit {
		limit = billingMaxLimit
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, ` ORDER BY i.invoice_date DESC, i.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	invoices := make([]billing.Invoice, 0)
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan invoice: %w", platform.MapError(err))
		}
		invoices = append(invoices, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return invoices, nil
}

// VoidInvoice marks an invoice voided and reports whether it changed.
//
// The guard is in the UPDATE, not in a preceding read: the implementation this
// replaces read the status outside the transaction and then updated without a
// guard, so two clicks on Void both passed the check and restored the stock
// twice.
func (r *Billing) VoidInvoice(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE invoices SET status = 'voided', updated_at = now()
		  WHERE id = $1 AND store_id = $2 AND status = 'completed'`, id, storeID)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n == 1, nil
}

// GSTR1Row is one line of the outward supplies return.
type GSTR1Row struct {
	InvoiceNo     string      `json:"invoice_no"`
	InvoiceDate   time.Time   `json:"invoice_date"`
	CustomerName  string      `json:"customer_name"`
	CustomerGSTIN string      `json:"customer_gstin"`
	Taxable       money.Money `json:"taxable_value"`
	CGST          money.Money `json:"cgst"`
	SGST          money.Money `json:"sgst"`
	IGST          money.Money `json:"igst"`
	Total         money.Money `json:"total"`
}

// GSTR1Rows returns the unfiled outward supplies for a month.
//
// The status filter is the point. Without it — as shipped — voided invoices
// were filed to the GST portal as real supplies, overstating the liability the
// business declared.
func (r *Billing) GSTR1Rows(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) ([]GSTR1Row, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT i.invoice_no, i.invoice_date,
		        COALESCE(c.name, ''), COALESCE(c.gstin, ''),
		        i.sub_total, i.cgst, i.sgst, i.igst, i.total
		   FROM invoices i
		   LEFT JOIN customers c ON c.id = i.customer_id
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND NOT i.is_gstr1_filed
		    AND i.invoice_date >= $2 AND i.invoice_date < $3
		  ORDER BY i.invoice_no`, storeID, from, to)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]GSTR1Row, 0)
	for rows.Next() {
		var row GSTR1Row
		if err := rows.Scan(&row.InvoiceNo, &row.InvoiceDate, &row.CustomerName, &row.CustomerGSTIN,
			&row.Taxable, &row.CGST, &row.SGST, &row.IGST, &row.Total); err != nil {
			return nil, fmt.Errorf("scan gstr1 row: %w", platform.MapError(err))
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// MarkGSTR1Filed flags a month's completed invoices as filed and returns how
// many were marked.
func (r *Billing) MarkGSTR1Filed(ctx context.Context, q platform.Querier, storeID uuid.UUID, month string, from, to time.Time) (int64, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE invoices SET is_gstr1_filed = true, gstr1_month = $2, updated_at = now()
		  WHERE store_id = $1
		    AND status = 'completed'
		    AND NOT is_gstr1_filed
		    AND invoice_date >= $3 AND invoice_date < $4`,
		storeID, month, from, to)
	if err != nil {
		return 0, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}

// StoreGSTINForBilling returns a store's GSTIN, which decides whether a supply
// is inter-state.
func (r *Billing) StoreGSTINForBilling(ctx context.Context, q platform.Querier, storeID uuid.UUID) (string, error) {
	var gstin string
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(gstin, '') FROM stores WHERE id = $1`, storeID).Scan(&gstin); err != nil {
		return "", platform.MapError(err)
	}
	return gstin, nil
}

// SaleProduct is what billing needs to know about a product to price a line.
//
// It carries every unit the product may be sold in rather than one price,
// because one price per product forces a 25 kg sack to cost exactly 25 times
// the loose kilo — and a wholesaler's entire trade is that it does not.
type SaleProduct struct {
	ID           uuid.UUID
	Name         string
	GSTRate      money.Rate
	AllowDecimal bool
	IsActive     bool
	// Units are the product's units with this store's price for each, in the
	// order the counter offers them.
	Units []SaleUnit
}

// SaleUnit is one way a product may be sold, with what it costs.
type SaleUnit struct {
	ID    uuid.UUID
	Label string
	// QtyInBase is how many of the product's stocking unit one of these is:
	// loose kilo 1, 25 kg sack 25, 250 g pouch 0.25.
	QtyInBase  money.Quantity
	IsDefault  bool
	IsSellable bool
	IsActive   bool
	// HasPrice distinguishes a unit the store has priced at zero — a free
	// sample — from one it has not priced at all. Without the distinction an
	// unpriced sack is sold for nothing, which is the failure mode a COALESCE
	// to zero produces silently.
	HasPrice       bool
	SellPrice      money.Money
	WholesalePrice money.Money
}

// ProductsForSale loads pricing and tax data for the products on an invoice,
// in one query rather than one per line.
//
// The rate and the GST percentage come from here, never from the request: a
// client that could name its own price could post a Rs.5,000 sale as Rs.1.
//
// One query still, now that each product carries several units: the join
// produces a row per (product, unit) and they are folded into the map here. An
// invoice names a handful of products with a handful of units each, so this
// stays one round trip however many lines the bill has.
func (r *Billing) ProductsForSale(ctx context.Context, q platform.Querier, storeID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]SaleProduct, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]SaleProduct{}, nil
	}
	// The product row is LEFT JOINed to its units so a product with no unit set
	// up still comes back — as a product with no units, which the caller
	// refuses by name rather than reporting as an unknown product.
	rows, err := q.QueryContext(ctx,
		`SELECT p.id, p.name, p.gst_rate, p.allow_decimal, p.is_active,
		        pu.id, COALESCE(pu.label, ''), COALESCE(pu.qty_in_base, 0),
		        COALESCE(pu.is_default, false), COALESCE(pu.is_sellable, false),
		        COALESCE(pu.is_active, false),
		        pp.id IS NOT NULL,
		        COALESCE(pp.sell_price, 0), COALESCE(pp.wholesale_sell_price, 0)
		   FROM products p
		   LEFT JOIN product_units pu ON pu.product_id = p.id
		   LEFT JOIN product_prices pp
		          ON pp.product_unit_id = pu.id AND pp.store_id = $1
		  WHERE p.id = ANY($2)
		  ORDER BY p.id, pu.is_default DESC, pu.qty_in_base, pu.sort_order`, storeID, ids)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make(map[uuid.UUID]SaleProduct, len(ids))
	for rows.Next() {
		var p SaleProduct
		var u SaleUnit
		var unitID uuid.NullUUID
		if err := rows.Scan(&p.ID, &p.Name, &p.GSTRate, &p.AllowDecimal, &p.IsActive,
			&unitID, &u.Label, &u.QtyInBase, &u.IsDefault, &u.IsSellable, &u.IsActive,
			&u.HasPrice, &u.SellPrice, &u.WholesalePrice); err != nil {
			return nil, fmt.Errorf("scan sale product: %w", platform.MapError(err))
		}
		existing, seen := out[p.ID]
		if seen {
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

// CustomerForSale is what billing needs to know about the buyer.
type CustomerForSale struct {
	ID          uuid.UUID
	Name        string
	GSTIN       string
	IsWholesale bool
	StoreID     uuid.UUID
}

// GetCustomerForSale loads the buyer, scoped to the store.
func (r *Billing) GetCustomerForSale(ctx context.Context, q platform.Querier, storeID, customerID uuid.UUID) (CustomerForSale, error) {
	var c CustomerForSale
	err := q.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(gstin, ''), is_wholesale, store_id
		   FROM customers WHERE id = $1 AND store_id = $2 AND is_active`,
		customerID, storeID).Scan(&c.ID, &c.Name, &c.GSTIN, &c.IsWholesale, &c.StoreID)
	if err != nil {
		return CustomerForSale{}, platform.MapError(err)
	}
	return c, nil
}

// FindProcessed returns a previously recorded response for an idempotency key.
func (r *Billing) FindProcessed(ctx context.Context, q platform.Querier, key string) ([]byte, bool, error) {
	var body []byte
	err := q.QueryRowContext(ctx,
		`SELECT response_body FROM processed_requests WHERE key = $1`, key).Scan(&body)
	if err != nil {
		mapped := platform.MapError(err)
		if errors.Is(mapped, platform.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, mapped
	}
	return body, true, nil
}

// RecordProcessed stores the response for an idempotency key.
//
// It is written inside the same transaction as the sale, which is what makes
// the guarantee real: a replayed request cannot be applied twice, because
// applying it and recording that it was applied either both happen or neither
// does. A cache outside the transaction could not promise that.
func (r *Billing) RecordProcessed(ctx context.Context, q platform.Querier, key, path string, status int, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode idempotent response: %w", err)
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO processed_requests (key, request_path, response_status, response_body)
		 VALUES ($1, $2, $3, $4)`, key, path, status, body)
	return platform.MapError(err)
}
