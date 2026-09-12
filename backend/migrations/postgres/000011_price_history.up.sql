-- Why is this ₹45 when it was ₹42 last week?
--
-- Nothing could answer that. product_prices holds one row per product, store and
-- unit, and every write replaced it: the price the shop is charging today was
-- the only price the system had ever heard of. A customer disputing a rate, a
-- shopkeeper wondering who marked the atta up, an owner asking whether the
-- margin on rice has been squeezed all year — none of it had anywhere to look.
--
-- This records the state after every change rather than the change itself. The
-- difference between two rows is derived by the reader, which means there is
-- exactly one source of truth per row and no chance of a stored delta
-- disagreeing with the values either side of it. The earliest row for a product
-- is where its price started, which is a fact worth having and not an absence.

CREATE TYPE price_change_source AS ENUM (
    -- The price a product was created with.
    'opening',
    -- Somebody typed it: the product form or the price screen.
    'manual',
    -- A bulk catalogue sheet.
    'import'
);

CREATE TABLE product_price_history (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           uuid NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    store_id             uuid NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    product_unit_id      uuid NOT NULL REFERENCES product_units(id) ON DELETE RESTRICT,

    -- All four, on every row, even when only one of them moved. A cost change
    -- with no selling price beside it cannot be read as a margin, and the
    -- margin is the question this table exists to answer.
    mrp                  numeric(14,2) NOT NULL,
    cost_price           numeric(14,2) NOT NULL,
    sell_price           numeric(14,2) NOT NULL,
    wholesale_sell_price numeric(14,2) NOT NULL,

    -- Nullable: a price written by the first-run bootstrap has no person behind
    -- it, and recording a fictional one would be worse than recording none.
    changed_by           uuid REFERENCES users(id) ON DELETE RESTRICT,
    source               price_change_source NOT NULL,
    -- Why, when somebody says why. A shop that writes "vendor raised the rate"
    -- here answers next month's question without anyone having to remember.
    note                 text NOT NULL DEFAULT '',

    created_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT price_history_non_negative CHECK (
        mrp >= 0 AND cost_price >= 0 AND sell_price >= 0 AND wholesale_sell_price >= 0
    )
);

-- The one query this table has: the history of one price, newest first.
CREATE INDEX idx_price_history_series
    ON product_price_history (product_id, store_id, product_unit_id, created_at DESC);

-- And the one report: what changed in this store lately.
CREATE INDEX idx_price_history_store_recent
    ON product_price_history (store_id, created_at DESC);

-- Seed the history with what is already on the shelf.
--
-- Without this, every price in an existing shop appears to have no beginning,
-- and the first change made after this migration would look like the first
-- price the product ever had. There is no created_at on product_prices worth
-- trusting for this, so these rows carry the price row's own updated_at: it is
-- when the figure last moved, which is the truest thing available.
INSERT INTO product_price_history
    (product_id, store_id, product_unit_id, mrp, cost_price, sell_price,
     wholesale_sell_price, source, note, created_at)
SELECT product_id, store_id, product_unit_id, mrp, cost_price, sell_price,
       wholesale_sell_price, 'opening', 'in the catalogue before price history was kept',
       COALESCE(updated_at, now())
  FROM product_prices;
