-- GBOS ERP — PostgreSQL schema, initial migration.
--
-- This is a deliberate redesign of the SQLite schema that lives in
-- internal/db/db.go createTables() plus migrations/0001_add_product_fields_and_tables.sql,
-- not a mechanical translation. Divergences are commented inline and marked
-- with "DIVERGENCE:".
--
-- Conventions applied throughout:
--   * Ids that hold UUIDs are native `uuid`, not TEXT.
--   * Money amounts are NUMERIC(14,2); per-unit rates/costs are NUMERIC(14,4)
--     because they are multiplied by fractional quantities before rounding;
--     quantities are NUMERIC(14,3) because the shop genuinely sells 2.5 kg.
--     REAL/float is never used for money — binary floating point cannot
--     represent 0.10 and the SQLite schema used REAL for every rupee value.
--   * Timestamps are TIMESTAMPTZ NOT NULL DEFAULT now(); date-only business
--     dates are DATE.
--   * Flags stored as INTEGER 0/1 in SQLite are BOOLEAN.
--   * uuid primary keys carry DEFAULT gen_random_uuid() (built in on PG 13+,
--     no pgcrypto extension needed) so an insert that omits the id still works.

BEGIN;

-- ---------------------------------------------------------------------------
-- Enumerated types
-- ---------------------------------------------------------------------------
-- DIVERGENCE: every status column below was free-text in SQLite with no
-- constraint of any kind. The vocabulary had drifted and at least one write
-- path bound a client-supplied string straight into invoices.status, which
-- allowed a caller to create an invoice that was already 'voided' — it still
-- deducted stock but stayed out of every status-filtered report. Enums make
-- that unrepresentable at the storage layer regardless of what the Go code does.

CREATE TYPE invoice_status AS ENUM ('completed', 'voided');

-- NOTE: 'completed' is DELIBERATELY ABSENT from credit_entry_status.
-- The SQLite code writes a bogus status = 'completed' for both `payment`
-- entries (credit.go, the payment insert) and `advance` entries (credit.go,
-- the overpayment insert). 'completed' is not a real state of a credit
-- ledger row — an issue row is pending/partial/paid/voided, and a payment or
-- advance row is fully settled the moment it is written, i.e. 'paid'.
-- Both call sites must be changed to write 'paid'. That is a separate
-- workstream; this type is what they must map onto.
CREATE TYPE credit_entry_status AS ENUM ('pending', 'partial', 'paid', 'voided');

CREATE TYPE credit_entry_type AS ENUM ('issue', 'payment', 'advance');

CREATE TYPE purchase_order_status AS ENUM ('pending', 'partial', 'received', 'cancelled');

CREATE TYPE return_status AS ENUM ('pending', 'completed');

CREATE TYPE purchase_return_status AS ENUM ('pending', 'approved');

CREATE TYPE sync_status AS ENUM ('pending', 'synced', 'failed');

-- Kinds of document that draw from a gap-free numbering series.
CREATE TYPE document_type AS ENUM ('invoice', 'purchase_order');

-- ---------------------------------------------------------------------------
-- Stores, users, terminals
-- ---------------------------------------------------------------------------

CREATE TABLE stores (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text        NOT NULL,
    address       text,
    phone         text,
    gstin         text,
    location_type text        NOT NULL DEFAULT 'store',
    is_active     boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    role          text        NOT NULL,
    -- ON DELETE RESTRICT: a store that still has users must not disappear.
    store_id      uuid        REFERENCES stores (id) ON DELETE RESTRICT,
    is_active     boolean     NOT NULL DEFAULT true,
    -- NEW COLUMN: any JWT issued at or before this instant is rejected.
    -- Gives "log out everywhere" and makes deactivating a user take effect
    -- immediately instead of after the 24-hour token TTL expires.
    sessions_invalidated_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_store ON users (store_id);

-- NEW TABLE: a billing device registered to a store. `code` is the invoice
-- series prefix (e.g. 'T1'), which is why it is unique per store rather than
-- globally — two stores may each have a terminal called T1.
CREATE TABLE terminals (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- ON DELETE RESTRICT: a terminal owns a document series, and a series is
    -- a statutory artefact. Retire a terminal by clearing is_active, never
    -- by deleting the store out from under it.
    store_id   uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    code       text        NOT NULL,
    name       text        NOT NULL,
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT terminals_code_not_blank CHECK (code <> ''),
    CONSTRAINT terminals_store_code_key UNIQUE (store_id, code)
);

CREATE INDEX idx_terminals_store ON terminals (store_id);

-- ---------------------------------------------------------------------------
-- Catalogue
-- ---------------------------------------------------------------------------

CREATE TABLE categories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    name_hi    text,
    name_kn    text,
    -- ON DELETE RESTRICT rather than SET NULL: silently promoting a subtree
    -- to the root would rewrite the catalogue's shape without anyone asking.
    parent_id  uuid        REFERENCES categories (id) ON DELETE RESTRICT,
    hsn_code   text,
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_categories_parent ON categories (parent_id);

CREATE TABLE units_of_measure (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- DIVERGENCE: `code` was NOT NULL but not unique, so nothing stopped a
    -- second 'KG'. The seed does `SELECT id FROM units_of_measure
    -- WHERE code = 'NOS' LIMIT 1`, which would then pick one arbitrarily.
    code           text        NOT NULL UNIQUE,
    name           text        NOT NULL,
    decimal_places integer     NOT NULL DEFAULT 0,
    is_active      boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uom_decimal_places_sane CHECK (decimal_places BETWEEN 0 AND 6)
);

CREATE TABLE products (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text        NOT NULL,
    name_hi       text,
    name_kn       text,
    category_id   uuid        REFERENCES categories (id) ON DELETE RESTRICT,
    hsn_code      text,
    barcode       text,
    uom_id        uuid        REFERENCES units_of_measure (id) ON DELETE RESTRICT,
    base_unit_qty numeric(14,3) NOT NULL DEFAULT 1,
    gst_rate      numeric(5,2)  NOT NULL DEFAULT 0,
    is_free       boolean     NOT NULL DEFAULT false,
    is_active     boolean     NOT NULL DEFAULT true,
    allow_decimal boolean     NOT NULL DEFAULT false,
    image_url     text,
    min_stock     numeric(14,3) NOT NULL DEFAULT 0,
    -- Added to products by migrations/0001_add_product_fields_and_tables.sql.
    brand         text        NOT NULL DEFAULT '',
    description   text        NOT NULL DEFAULT '',
    sac_code      text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- base_unit_qty is a divisor in the unit-conversion path; zero would be a
    -- division by zero, negative is meaningless.
    CONSTRAINT products_base_unit_qty_positive CHECK (base_unit_qty > 0),
    CONSTRAINT products_min_stock_non_negative CHECK (min_stock >= 0),
    CONSTRAINT products_gst_rate_valid CHECK (gst_rate BETWEEN 0 AND 100)
);

-- DIVERGENCE: barcode had a plain (non-unique) index, so two products could
-- share a barcode and a scan-to-sell lookup would return an arbitrary one.
-- A PARTIAL unique index is used rather than a plain UNIQUE constraint:
-- the column stays nullable, and the app's own convention (products.go uses
-- COALESCE(NULLIF(?, ''), barcode) on update) treats the empty string as
-- "no barcode". Postgres already treats NULLs as distinct, but it does NOT
-- treat '' as distinct — a plain UNIQUE would reject the second blank-barcode
-- product. Excluding both sentinels gets uniqueness where it means something.
-- This index also replaces the old idx_products_barcode for the equality
-- lookup (`WHERE p.barcode = ?`); the `barcode LIKE ?` search is a leading
-- wildcard and could not use either index.
CREATE UNIQUE INDEX idx_products_barcode_unique
    ON products (barcode)
    WHERE barcode IS NOT NULL AND barcode <> '';

CREATE INDEX idx_products_category ON products (category_id);
CREATE INDEX idx_products_uom ON products (uom_id);

CREATE TABLE product_units (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The only ON DELETE CASCADE in the SQLite schema, and it is correct:
    -- a unit definition has no meaning without its product.
    product_id     uuid        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- CASCADE too, so deleting a product tears down the whole unit tree in one
    -- pass. RESTRICT here would abort that cascade when a parent and its child
    -- are removed together.
    parent_unit_id uuid        REFERENCES product_units (id) ON DELETE CASCADE,
    label          text        NOT NULL,
    qty_in_parent  numeric(14,3) NOT NULL DEFAULT 1,
    barcode        text,
    sort_order     integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- inventory.go divides by qty_in_parent when converting a batch.
    CONSTRAINT product_units_qty_in_parent_positive CHECK (qty_in_parent > 0)
);

CREATE INDEX idx_product_units_product ON product_units (product_id);
CREATE INDEX idx_product_units_parent ON product_units (parent_unit_id);

CREATE TABLE product_prices (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    store_id             uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    mrp                  numeric(14,2) NOT NULL DEFAULT 0,
    -- cost_price is a per-unit cost, not a total: 4dp like the other rates.
    cost_price           numeric(14,4) NOT NULL DEFAULT 0,
    sell_price           numeric(14,2) NOT NULL DEFAULT 0,
    wholesale_sell_price numeric(14,2) NOT NULL DEFAULT 0,
    is_active            boolean     NOT NULL DEFAULT true,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT product_prices_non_negative CHECK (
        mrp >= 0 AND cost_price >= 0 AND sell_price >= 0 AND wholesale_sell_price >= 0
    ),
    -- Was a standalone unique index; a table constraint is equivalent and
    -- makes the one-price-row-per-product-per-store rule visible in \d.
    CONSTRAINT product_prices_product_store_key UNIQUE (product_id, store_id)
);

CREATE INDEX idx_product_prices_store ON product_prices (store_id);

-- ---------------------------------------------------------------------------
-- Stock
-- ---------------------------------------------------------------------------

CREATE TABLE stock (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id  uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    store_id    uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    -- DIVERGENCE, and this one is a live bug fix. batch_no was nullable while
    -- participating in UNIQUE (product_id, store_id, batch_no). NULLs are
    -- distinct in a unique index, so rows with a NULL batch never collided and
    -- the `ON CONFLICT (product_id, store_id, batch_no) DO UPDATE SET
    -- quantity = quantity + excluded.quantity` upsert in vendor.go (GRN
    -- receipt) and inventory.go (unit conversion) silently inserted a
    -- duplicate stock row instead of adding to the existing one — quietly
    -- fragmenting on-hand quantity. NOT NULL DEFAULT '' makes the sentinel a
    -- real value so the constraint bites and the upsert works.
    batch_no    text        NOT NULL DEFAULT '',
    quantity    numeric(14,3) NOT NULL DEFAULT 0,
    reserved_qty numeric(14,3) NOT NULL DEFAULT 0,
    expiry_date date,
    mfg_date    date,
    cost_price  numeric(14,4) NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- The schema previously had no CHECK constraints at all. On-hand stock
    -- going negative, or reserving more than exists, is an oversell.
    CONSTRAINT stock_quantity_non_negative CHECK (quantity >= 0),
    CONSTRAINT stock_reserved_non_negative CHECK (reserved_qty >= 0),
    CONSTRAINT stock_reserved_le_quantity CHECK (reserved_qty <= quantity),
    CONSTRAINT stock_cost_price_non_negative CHECK (cost_price >= 0),
    CONSTRAINT stock_product_store_batch_key UNIQUE (product_id, store_id, batch_no)
);

-- The old idx_stock_product ON stock(product_id, store_id) is not recreated
-- separately: stock_product_store_batch_key indexes (product_id, store_id,
-- batch_no) and Postgres uses a leading-column prefix of a composite index for
-- exactly the lookups that index served. A second index on the prefix would
-- only add write cost on the hottest table in the system.
CREATE INDEX idx_stock_store ON stock (store_id);
CREATE INDEX idx_stock_expiry ON stock (expiry_date) WHERE expiry_date IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Parties
-- ---------------------------------------------------------------------------

CREATE TABLE customers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    name_hi      text,
    phone        text,
    address      text,
    gstin        text,
    credit_limit numeric(14,2) NOT NULL DEFAULT 0,
    is_wholesale boolean     NOT NULL DEFAULT false,
    store_id     uuid        REFERENCES stores (id) ON DELETE RESTRICT,
    is_active    boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT customers_credit_limit_non_negative CHECK (credit_limit >= 0)
);

CREATE INDEX idx_customers_store ON customers (store_id);
CREATE INDEX idx_customers_phone ON customers (phone);

CREATE TABLE vendors (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    phone        text,
    address      text,
    gstin        text,
    state        text,
    credit_days  integer     NOT NULL DEFAULT 0,
    credit_limit numeric(14,2) NOT NULL DEFAULT 0,
    is_active    boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vendors_credit_days_non_negative CHECK (credit_days >= 0),
    CONSTRAINT vendors_credit_limit_non_negative CHECK (credit_limit >= 0)
);

-- ---------------------------------------------------------------------------
-- Sales
-- ---------------------------------------------------------------------------

CREATE TABLE invoices (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Scoped per store, NOT globally unique. Terminal codes are unique per
    -- store (terminals_store_code_key), so a global constraint would make two
    -- stores that each name a counter 'T1' collide on INV/T1/00001 — a
    -- legitimate sale failing the moment the second store opens. Each store is
    -- its own GSTIN and legitimately runs its own series.
    invoice_no     text        NOT NULL,
    -- RESTRICT everywhere below: an invoice is financial history. Deleting the
    -- store, cashier or customer must not be able to take it with them.
    store_id       uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Which counter issued this bill. Nullable because a correction raised
    -- server-side has no terminal. Needed to prove a terminal's series is
    -- consecutive, which is what CGST Rule 46(b) requires of each series, and
    -- to spot a terminal whose offline bills never synced.
    terminal_id    uuid        REFERENCES terminals (id) ON DELETE RESTRICT,
    customer_id    uuid        REFERENCES customers (id) ON DELETE RESTRICT,
    invoice_date   date        NOT NULL,
    sub_total      numeric(14,2) NOT NULL DEFAULT 0,
    cgst           numeric(14,2) NOT NULL DEFAULT 0,
    sgst           numeric(14,2) NOT NULL DEFAULT 0,
    igst           numeric(14,2) NOT NULL DEFAULT 0,
    discount       numeric(14,2) NOT NULL DEFAULT 0,
    -- round_off is the ONE money column that is legitimately signed: rounding
    -- the grand total to the nearest rupee moves it either way. No CHECK.
    round_off      numeric(14,2) NOT NULL DEFAULT 0,
    total          numeric(14,2) NOT NULL DEFAULT 0,
    status         invoice_status NOT NULL DEFAULT 'completed',
    payment_mode   text        NOT NULL DEFAULT 'cash',
    is_gstr1_filed boolean     NOT NULL DEFAULT false,
    gstr1_month    text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invoices_amounts_non_negative CHECK (
        sub_total >= 0 AND cgst >= 0 AND sgst >= 0 AND igst >= 0
        AND discount >= 0 AND total >= 0
    ),
    CONSTRAINT invoices_store_no_key UNIQUE (store_id, invoice_no)
);

CREATE INDEX idx_invoices_date ON invoices (invoice_date);
-- Verifying a terminal's series has no gaps is an ordered walk of its numbers.
CREATE INDEX idx_invoices_terminal_no ON invoices (terminal_id, invoice_no);
-- The analytics queries all filter store_id + an invoice_date range +
-- status = 'completed'; the composite serves them in one index scan.
CREATE INDEX idx_invoices_store_date ON invoices (store_id, invoice_date);
CREATE INDEX idx_invoices_customer ON invoices (customer_id);
CREATE INDEX idx_invoices_user ON invoices (user_id);

CREATE TABLE invoice_items (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- CASCADE: a line item is a compositional part of its invoice and has no
    -- independent existence. Contrast with the RESTRICT on product_id — the
    -- product is referenced master data, not a part.
    invoice_id        uuid        NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    product_id        uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    batch_no          text        NOT NULL DEFAULT '',
    quantity          numeric(14,3) NOT NULL DEFAULT 0,
    -- rate and unit_cost_at_sale are per-unit and get multiplied by a
    -- fractional quantity, so they carry 2 more decimal places than the
    -- amounts they produce.
    rate              numeric(14,4) NOT NULL DEFAULT 0,
    discount          numeric(14,2) NOT NULL DEFAULT 0,
    cgst              numeric(14,2) NOT NULL DEFAULT 0,
    sgst              numeric(14,2) NOT NULL DEFAULT 0,
    igst              numeric(14,2) NOT NULL DEFAULT 0,
    total             numeric(14,2) NOT NULL DEFAULT 0,
    unit_cost_at_sale numeric(14,4) NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invoice_items_non_negative CHECK (
        quantity >= 0 AND rate >= 0 AND discount >= 0 AND cgst >= 0
        AND sgst >= 0 AND igst >= 0 AND total >= 0 AND unit_cost_at_sale >= 0
    )
);

CREATE INDEX idx_invoice_items_invoice ON invoice_items (invoice_id);
CREATE INDEX idx_invoice_items_product ON invoice_items (product_id);

CREATE TABLE invoice_payments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id   uuid        NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    payment_mode text        NOT NULL,
    amount       numeric(14,2) NOT NULL,
    reference    text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invoice_payments_amount_non_negative CHECK (amount >= 0)
);

CREATE INDEX idx_invoice_payments_invoice ON invoice_payments (invoice_id);

-- ---------------------------------------------------------------------------
-- Purchasing
-- ---------------------------------------------------------------------------

CREATE TABLE purchase_orders (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Per store, for the same reason as invoices.invoice_no: each store runs
    -- its own document series and must not be able to collide with another's.
    po_no         text        NOT NULL,
    store_id      uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    vendor_id     uuid        NOT NULL REFERENCES vendors (id) ON DELETE RESTRICT,
    order_date    date        NOT NULL,
    expected_date date,
    sub_total     numeric(14,2) NOT NULL DEFAULT 0,
    cgst          numeric(14,2) NOT NULL DEFAULT 0,
    sgst          numeric(14,2) NOT NULL DEFAULT 0,
    igst          numeric(14,2) NOT NULL DEFAULT 0,
    discount      numeric(14,2) NOT NULL DEFAULT 0,
    total         numeric(14,2) NOT NULL DEFAULT 0,
    status        purchase_order_status NOT NULL DEFAULT 'pending',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT purchase_orders_amounts_non_negative CHECK (
        sub_total >= 0 AND cgst >= 0 AND sgst >= 0 AND igst >= 0
        AND discount >= 0 AND total >= 0
    ),
    CONSTRAINT purchase_orders_store_no_key UNIQUE (store_id, po_no)
);

CREATE INDEX idx_purchase_orders_store ON purchase_orders (store_id);
CREATE INDEX idx_purchase_orders_vendor ON purchase_orders (vendor_id);
CREATE INDEX idx_purchase_orders_status ON purchase_orders (status);

CREATE TABLE purchase_items (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    po_id        uuid        NOT NULL REFERENCES purchase_orders (id) ON DELETE CASCADE,
    product_id   uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    quantity     numeric(14,3) NOT NULL DEFAULT 0,
    uom_id       uuid        REFERENCES units_of_measure (id) ON DELETE RESTRICT,
    received_qty numeric(14,3) NOT NULL DEFAULT 0,
    rate         numeric(14,4) NOT NULL DEFAULT 0,
    discount     numeric(14,2) NOT NULL DEFAULT 0,
    cgst         numeric(14,2) NOT NULL DEFAULT 0,
    sgst         numeric(14,2) NOT NULL DEFAULT 0,
    igst         numeric(14,2) NOT NULL DEFAULT 0,
    total        numeric(14,2) NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- No received_qty <= quantity check: over-receipt against a PO is a real
    -- thing that happens and the business accepts it.
    CONSTRAINT purchase_items_non_negative CHECK (
        quantity >= 0 AND received_qty >= 0 AND rate >= 0 AND discount >= 0
        AND cgst >= 0 AND sgst >= 0 AND igst >= 0 AND total >= 0
    )
);

CREATE INDEX idx_purchase_items_po ON purchase_items (po_id);
CREATE INDEX idx_purchase_items_product ON purchase_items (product_id);

CREATE TABLE goods_receipts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- RESTRICT, not CASCADE: a goods receipt is its own document that records
    -- what physically arrived. It outlives the PO that prompted it.
    po_id         uuid        NOT NULL REFERENCES purchase_orders (id) ON DELETE RESTRICT,
    received_date date        NOT NULL,
    received_by   text        NOT NULL,
    notes         text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_goods_receipts_po ON goods_receipts (po_id);

CREATE TABLE goods_receipt_items (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    gr_id        uuid        NOT NULL REFERENCES goods_receipts (id) ON DELETE CASCADE,
    product_id   uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    ordered_qty  numeric(14,3),
    received_qty numeric(14,3) NOT NULL,
    accepted_qty numeric(14,3),
    rejected_qty numeric(14,3),
    reason       text,
    -- Nullable columns are compared with IS NOT NULL guards so a NULL does not
    -- make the whole CHECK evaluate to unknown-and-therefore-pass by accident.
    CONSTRAINT goods_receipt_items_non_negative CHECK (
        received_qty >= 0
        AND (ordered_qty  IS NULL OR ordered_qty  >= 0)
        AND (accepted_qty IS NULL OR accepted_qty >= 0)
        AND (rejected_qty IS NULL OR rejected_qty >= 0)
    )
);

CREATE INDEX idx_goods_receipt_items_gr ON goods_receipt_items (gr_id);
CREATE INDEX idx_goods_receipt_items_product ON goods_receipt_items (product_id);

CREATE TABLE purchase_returns (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    po_id       uuid        REFERENCES purchase_orders (id) ON DELETE RESTRICT,
    vendor_id   uuid        NOT NULL REFERENCES vendors (id) ON DELETE RESTRICT,
    store_id    uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    return_date date        NOT NULL,
    total       numeric(14,2) NOT NULL DEFAULT 0,
    status      purchase_return_status NOT NULL DEFAULT 'pending',
    reason      text,
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT purchase_returns_total_non_negative CHECK (total >= 0)
);

CREATE INDEX idx_purchase_returns_vendor ON purchase_returns (vendor_id);
CREATE INDEX idx_purchase_returns_store ON purchase_returns (store_id);
CREATE INDEX idx_purchase_returns_po ON purchase_returns (po_id);

CREATE TABLE purchase_return_items (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    return_id  uuid        NOT NULL REFERENCES purchase_returns (id) ON DELETE CASCADE,
    product_id uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    quantity   numeric(14,3) NOT NULL,
    rate       numeric(14,4) NOT NULL DEFAULT 0,
    total      numeric(14,2) NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT purchase_return_items_non_negative CHECK (
        quantity >= 0 AND rate >= 0 AND total >= 0
    )
);

CREATE INDEX idx_purchase_return_items_return ON purchase_return_items (return_id);
CREATE INDEX idx_purchase_return_items_product ON purchase_return_items (product_id);

CREATE TABLE vendor_payments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_id    uuid        NOT NULL REFERENCES vendors (id) ON DELETE RESTRICT,
    po_id        uuid        REFERENCES purchase_orders (id) ON DELETE RESTRICT,
    amount       numeric(14,2) NOT NULL,
    payment_mode text        NOT NULL DEFAULT 'bank',
    reference    text,
    payment_date date        NOT NULL,
    notes        text,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    store_id     uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vendor_payments_amount_non_negative CHECK (amount >= 0)
);

CREATE INDEX idx_vendor_payments_vendor ON vendor_payments (vendor_id);
CREATE INDEX idx_vendor_payments_po ON vendor_payments (po_id);
CREATE INDEX idx_vendor_payments_store ON vendor_payments (store_id);

-- ---------------------------------------------------------------------------
-- Customer credit ledger
-- ---------------------------------------------------------------------------

CREATE TABLE credit_entries (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid        NOT NULL REFERENCES customers (id) ON DELETE RESTRICT,
    -- RESTRICT rather than CASCADE: unlike a line item, a ledger row is an
    -- independent financial record. It must block deletion of the invoice,
    -- not evaporate with it.
    invoice_id  uuid        REFERENCES invoices (id) ON DELETE RESTRICT,
    amount      numeric(14,2) NOT NULL DEFAULT 0,
    type        credit_entry_type NOT NULL,
    due_date    date,
    paid_amount numeric(14,2) NOT NULL DEFAULT 0,
    status      credit_entry_status NOT NULL DEFAULT 'pending',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- The payment allocation loop in credit.go computes paid_amount by hand
    -- and nothing stopped it overshooting the entry it was settling.
    CONSTRAINT credit_entries_amount_non_negative CHECK (amount >= 0),
    CONSTRAINT credit_entries_paid_non_negative CHECK (paid_amount >= 0),
    CONSTRAINT credit_entries_paid_le_amount CHECK (paid_amount <= amount)
);

CREATE INDEX idx_credit_customer ON credit_entries (customer_id);
-- The allocation query is
--   WHERE customer_id = ? AND type = 'issue' AND status IN ('pending','partial')
--   ORDER BY created_at
CREATE INDEX idx_credit_customer_type_status ON credit_entries (customer_id, type, status, created_at);
CREATE INDEX idx_credit_invoice ON credit_entries (invoice_id);

-- ---------------------------------------------------------------------------
-- Returns, losses, adjustments
-- ---------------------------------------------------------------------------

CREATE TABLE returns (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id  uuid        REFERENCES invoices (id) ON DELETE RESTRICT,
    product_id  uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    store_id    uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    quantity    numeric(14,3) NOT NULL,
    amount      numeric(14,2) NOT NULL,
    reason      text        NOT NULL,
    refund_mode text,
    status      return_status NOT NULL DEFAULT 'pending',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT returns_non_negative CHECK (quantity >= 0 AND amount >= 0)
);

CREATE INDEX idx_returns_invoice ON returns (invoice_id);
CREATE INDEX idx_returns_store ON returns (store_id);
CREATE INDEX idx_returns_product ON returns (product_id);

CREATE TABLE inventory_losses (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    store_id   uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    batch_no   text        NOT NULL DEFAULT '',
    quantity   numeric(14,3) NOT NULL,
    loss_type  text        NOT NULL,
    reason     text,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- A loss is recorded as a positive magnitude; loss_type says what kind.
    CONSTRAINT inventory_losses_quantity_non_negative CHECK (quantity >= 0)
);

CREATE INDEX idx_inventory_losses_store_product ON inventory_losses (store_id, product_id);
CREATE INDEX idx_inventory_losses_user ON inventory_losses (user_id);

CREATE TABLE stock_adjustments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id      uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    store_id        uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    batch_no        text        NOT NULL DEFAULT '',
    -- DELIBERATELY UNCONSTRAINED. Unlike every other quantity in the schema,
    -- an adjustment is a signed delta against on-hand stock: a stock-take that
    -- finds less than the books say is a negative correction. adjustment_type
    -- ('add' / 'remove' / 'set' / 'conversion') carries the intent today, but
    -- a >= 0 CHECK here would rule out the correction case forever.
    quantity        numeric(14,3) NOT NULL,
    adjustment_type text        NOT NULL,
    reason          text,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_stock_adjustments_store_product ON stock_adjustments (store_id, product_id);
CREATE INDEX idx_stock_adjustments_user ON stock_adjustments (user_id);

-- ---------------------------------------------------------------------------
-- Expenses
-- ---------------------------------------------------------------------------

CREATE TABLE expense_categories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE expenses (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id     uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    category_id  uuid        NOT NULL REFERENCES expense_categories (id) ON DELETE RESTRICT,
    amount       numeric(14,2) NOT NULL,
    description  text,
    expense_date date        NOT NULL,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT expenses_amount_non_negative CHECK (amount >= 0)
);

CREATE INDEX idx_expenses_store_date ON expenses (store_id, expense_date);
CREATE INDEX idx_expenses_category ON expenses (category_id);
CREATE INDEX idx_expenses_user ON expenses (user_id);

-- ---------------------------------------------------------------------------
-- Sync outbox and audit trail
-- ---------------------------------------------------------------------------

CREATE TABLE sync_records (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    table_name text        NOT NULL,
    -- record_id stays text, NOT uuid: it is a polymorphic pointer written from
    -- a request body and there is no guarantee every table it can name uses a
    -- uuid key. Casting it would turn a bad client payload into a 500.
    record_id  text        NOT NULL,
    operation  text        NOT NULL,
    -- CASCADE is right here and only here among the store references: the sync
    -- outbox is transient queue state, not a financial record. Rows for a
    -- deleted store are garbage, not history worth protecting.
    store_id   uuid        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    status     sync_status NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    synced_at  timestamptz
);

CREATE INDEX idx_sync_pending ON sync_records (status, created_at);
CREATE INDEX idx_sync_store ON sync_records (store_id);

CREATE TABLE audit_logs (
    -- ACTION REQUIRED IN THE GO CODE (separate workstream, do not fix here):
    -- this column is now uuid, but internal/handlers/analytics.go writes its id
    -- as fmt.Sprintf("AUD-%d", time.Now().UnixNano()) while auditutil.go and
    -- the mutation middleware both write uuid.New().String(). The "AUD-"
    -- generator will fail on insert against this type and must be changed to
    -- uuid.New().String() before cutover. Two ids for one table was also a
    -- correctness problem in its own right — a nanosecond clock is not unique
    -- across processes.
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- RESTRICT on both: an audit trail that a deletion can erase is not an
    -- audit trail.
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    store_id   uuid        NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    action     text        NOT NULL,
    table_name text,
    record_id  text,
    old_value  text,
    new_value  text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_store_created ON audit_logs (store_id, created_at);
CREATE INDEX idx_audit_logs_user ON audit_logs (user_id);
CREATE INDEX idx_audit_logs_record ON audit_logs (table_name, record_id);

-- ---------------------------------------------------------------------------
-- Document numbering and idempotency
-- ---------------------------------------------------------------------------

-- NEW TABLE: gap-free per-terminal, per-financial-year document numbering.
--
-- CGST Rule 46(b) requires a tax invoice to carry a consecutive serial number
-- unique for a financial year. Multiple series are permitted, so each terminal
-- owns its own — that is what makes offline-capable billing across several
-- counters legal without a central allocator.
--
-- The row is taken with SELECT ... FOR UPDATE inside the same transaction that
-- writes the sale, so concurrent tills serialise on it and a rolled-back sale
-- returns its number instead of burning it. A Postgres SEQUENCE would be the
-- obvious tool but is explicitly wrong here: sequences are non-transactional by
-- design and leave gaps on rollback, which is the one thing the rule forbids.
CREATE TABLE document_sequences (
    store_id    uuid          NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    terminal_id uuid          NOT NULL REFERENCES terminals (id) ON DELETE RESTRICT,
    doc_type    document_type NOT NULL,
    -- Indian financial year, April to March, e.g. '2026-27'.
    fin_year    text          NOT NULL,
    next_value  bigint        NOT NULL DEFAULT 1,
    created_at  timestamptz   NOT NULL DEFAULT now(),
    updated_at  timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT document_sequences_next_value_positive CHECK (next_value >= 1),
    CONSTRAINT document_sequences_fin_year_format CHECK (fin_year ~ '^[0-9]{4}-[0-9]{2}$'),
    -- The identifying columns are the key: one counter per terminal per
    -- document type per year, and no surrogate id to accidentally duplicate it.
    CONSTRAINT document_sequences_pkey PRIMARY KEY (store_id, terminal_id, doc_type, fin_year)
);

-- NEW TABLE: idempotency keys.
--
-- Written in the SAME transaction as the business write, so a replayed offline
-- sale either commits once with its key or not at all — the primary key is what
-- makes the second attempt fail instead of applying twice. The stored response
-- lets the replay be answered with the original result rather than a conflict.
CREATE TABLE processed_requests (
    -- text, not uuid, on purpose: the key is client-generated and a malformed
    -- one should be rejected by validation with a 400, not blow up as a type
    -- error at insert time deep inside the sale transaction.
    key             text        PRIMARY KEY,
    request_path    text        NOT NULL,
    response_status integer     NOT NULL,
    response_body   jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT processed_requests_key_not_blank CHECK (key <> ''),
    CONSTRAINT processed_requests_status_valid CHECK (response_status BETWEEN 100 AND 599)
);

-- Retention: these rows are only useful for as long as a client might retry.
-- The index makes the periodic "DELETE WHERE created_at < now() - interval"
-- prune a range scan instead of a full table scan.
CREATE INDEX idx_processed_requests_created ON processed_requests (created_at);

COMMIT;
