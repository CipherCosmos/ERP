-- Reverses 000002_units.
--
-- This loses information and cannot help it: a database that has recorded a
-- sale of two 25 kg sacks has no way to say that in the old shape, which knows
-- only "50". The base quantity survives, so stock and revenue stay correct;
-- what goes is the record of which unit the customer was served in.

ALTER TABLE purchase_items
    DROP CONSTRAINT IF EXISTS purchase_items_unit_qty_non_negative,
    DROP COLUMN IF EXISTS product_unit_id,
    DROP COLUMN IF EXISTS unit_qty;

ALTER TABLE invoice_items
    DROP CONSTRAINT IF EXISTS invoice_items_unit_qty_non_negative,
    DROP COLUMN IF EXISTS product_unit_id,
    DROP COLUMN IF EXISTS unit_qty;

-- Collapse back to one price per product per store. The default unit's price
-- is the one kept, because that is the price the old schema would have held;
-- prices for other units are dropped.
DELETE FROM product_prices pp
USING product_units pu
WHERE pu.id = pp.product_unit_id AND NOT pu.is_default;

ALTER TABLE product_prices
    DROP CONSTRAINT IF EXISTS product_prices_product_store_unit_key,
    DROP COLUMN IF EXISTS product_unit_id,
    ADD CONSTRAINT product_prices_product_store_key UNIQUE (product_id, store_id);

DROP INDEX IF EXISTS idx_product_units_one_default;
DROP INDEX IF EXISTS idx_product_units_barcode_unique;

-- The rows inserted by the up migration were derived from products, so they go
-- back where they came from rather than being left as orphaned tree nodes.
DELETE FROM product_units;

ALTER TABLE product_units
    DROP CONSTRAINT IF EXISTS product_units_qty_in_base_positive,
    DROP COLUMN IF EXISTS uom_id,
    DROP COLUMN IF EXISTS qty_in_base,
    DROP COLUMN IF EXISTS is_default,
    DROP COLUMN IF EXISTS is_sellable,
    DROP COLUMN IF EXISTS is_purchasable,
    DROP COLUMN IF EXISTS is_active,
    DROP COLUMN IF EXISTS updated_at,
    ADD COLUMN parent_unit_id uuid REFERENCES product_units (id) ON DELETE CASCADE,
    ADD COLUMN qty_in_parent numeric(14,3) NOT NULL DEFAULT 1,
    ADD CONSTRAINT product_units_qty_in_parent_positive CHECK (qty_in_parent > 0);

DROP INDEX IF EXISTS idx_uom_family;

ALTER TABLE unit_families DROP CONSTRAINT IF EXISTS unit_families_base_uom_fk;

-- The seeded units go before the families they point at.
DELETE FROM units_of_measure WHERE code IN (
    'MG','G','QTL','TON','ML','L','KL','PAIR','DOZ','GROSS',
    'MM','CM','M','IN','FT','YD','KM','SQM','SQFT'
);

ALTER TABLE units_of_measure
    DROP CONSTRAINT IF EXISTS uom_factor_positive,
    DROP COLUMN IF EXISTS family_id,
    DROP COLUMN IF EXISTS factor_to_base;

DROP TABLE IF EXISTS unit_families;
