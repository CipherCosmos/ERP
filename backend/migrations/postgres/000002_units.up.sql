-- Units: selling the same product loose and in bulk.
--
-- The schema this replaces could not express the business at all. A sale line
-- was a product and a bare quantity, so "250 g" and "one 25 kg sack" of the
-- same rice were indistinguishable; and product_prices was UNIQUE on
-- (product_id, store_id), so one product had exactly one price — which makes a
-- sack necessarily cost 25 times the loose kilo. Bulk being cheaper per kilo is
-- the entire point of selling it, so the model forbade the trade it exists for.
--
-- Three ideas, in order of how much they carry:
--
--   1. A unit is a row, not an enum. Families (weight, volume, count, length)
--      hold units that convert to the family's canonical unit by a stored
--      factor. A new unit or a whole new family is an INSERT — no deploy.
--
--   2. product_units lists what one product may be transacted AS: loose by the
--      kilo, a 5 kg bag, a 25 kg sack. Each carries qty_in_base — how many of
--      the product's own stocking unit it amounts to — and may carry its own
--      barcode, so the sack scans differently from the packet.
--
--   3. Stock stays in ONE pool, in the product's base unit. Receiving ten
--      sacks adds 250 kg; selling a sack removes 25; selling 250 g removes
--      0.25. Nothing has to be reconciled between a "sack" count and a "loose"
--      count, and the shop can never be out of loose stock while sacks sit on
--      the shelf.
--
-- Packaging is deliberately NOT a unit family. A "sack" is not a unit of
-- measure — it is 25 kg *of this product* and 50 kg of another. Families carry
-- universal factors (a kilo is a thousand grams everywhere); per-product packs
-- carry qty_in_base. Conflating them is what forces either a fake global
-- "sack" unit or a per-product duplicate of the whole unit table.

-- ---------------------------------------------------------------------------
-- Unit families
-- ---------------------------------------------------------------------------

CREATE TABLE unit_families (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text        NOT NULL UNIQUE,
    name       text        NOT NULL,
    -- The canonical unit every member converts through. Nullable only because
    -- of the chicken-and-egg with units_of_measure.family_id; it is set below
    -- and enforced by the application.
    base_uom_id uuid,
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO unit_families (code, name) VALUES
    ('WEIGHT', 'Weight'),
    ('VOLUME', 'Volume'),
    ('COUNT',  'Count'),
    ('LENGTH', 'Length'),
    ('AREA',   'Area');

-- ---------------------------------------------------------------------------
-- Units of measure gain a family and a conversion factor
-- ---------------------------------------------------------------------------

ALTER TABLE units_of_measure
    ADD COLUMN family_id uuid REFERENCES unit_families (id) ON DELETE RESTRICT,
    -- How many of the family's canonical unit one of these is: 1 kg = 1 when
    -- the canonical weight unit is the kilo, 1 g = 0.001, 1 quintal = 100.
    -- 9 decimal places so an imperial factor is exact rather than rounded:
    -- a foot is 0.3048 m and a square foot 0.09290304 m2, both stored whole.
    ADD COLUMN factor_to_base numeric(20,9) NOT NULL DEFAULT 1;

-- The two units the old seed created, placed in their families. Done by code
-- rather than by id so this runs against any database that has them.
UPDATE units_of_measure SET family_id = (SELECT id FROM unit_families WHERE code = 'WEIGHT'),
    factor_to_base = 1 WHERE code = 'KG';
UPDATE units_of_measure SET family_id = (SELECT id FROM unit_families WHERE code = 'COUNT'),
    factor_to_base = 1 WHERE code = 'NOS';

-- A starter set. Every one of these is ordinary Indian retail vocabulary, and
-- the point of the table is that this list is a starting position rather than
-- a ceiling.
INSERT INTO units_of_measure (code, name, decimal_places, family_id, factor_to_base)
SELECT v.code, v.name, v.dp, f.id, v.factor
FROM (VALUES
    -- Weight, canonical kilogram. KG and NOS are listed even though the
    -- application's first-run bootstrap also creates them: on a fresh database
    -- the migration runs first, and a WEIGHT family whose canonical unit does
    -- not exist yet converts nothing. The NOT EXISTS below makes this a no-op
    -- where they are already present.
    ('KG',    'Kilogram',    3, 'WEIGHT', 1),
    ('MG',    'Milligram',   3, 'WEIGHT', 0.000001),
    ('G',     'Gram',        3, 'WEIGHT', 0.001),
    ('QTL',   'Quintal',     3, 'WEIGHT', 100),
    ('TON',   'Tonne',       3, 'WEIGHT', 1000),
    -- Volume, canonical litre.
    ('ML',    'Millilitre',  3, 'VOLUME', 0.001),
    ('L',     'Litre',       3, 'VOLUME', 1),
    ('KL',    'Kilolitre',   3, 'VOLUME', 1000),
    -- Count, canonical piece. Zero decimal places: half a dozen eggs is three
    -- eggs, not 0.5 of anything, and the scale is what refuses to record it.
    ('NOS',   'Numbers',     0, 'COUNT',  1),
    ('PAIR',  'Pair',        0, 'COUNT',  2),
    ('DOZ',   'Dozen',       0, 'COUNT',  12),
    ('GROSS', 'Gross',       0, 'COUNT',  144),
    -- Length, canonical metre.
    ('MM',    'Millimetre',  3, 'LENGTH', 0.001),
    ('CM',    'Centimetre',  3, 'LENGTH', 0.01),
    ('M',     'Metre',       3, 'LENGTH', 1),
    ('IN',    'Inch',        3, 'LENGTH', 0.0254),
    ('FT',    'Foot',        3, 'LENGTH', 0.3048),
    ('YD',    'Yard',        3, 'LENGTH', 0.9144),
    ('KM',    'Kilometre',   3, 'LENGTH', 1000),
    -- Area, canonical square metre.
    ('SQM',   'Square metre',3, 'AREA',   1),
    ('SQFT',  'Square foot', 3, 'AREA',   0.09290304)
) AS v(code, name, dp, family, factor)
JOIN unit_families f ON f.code = v.family
WHERE NOT EXISTS (SELECT 1 FROM units_of_measure u WHERE u.code = v.code);

-- Every unit belongs to a family from here on. A unit with no family cannot be
-- converted, which makes it useless for the one job the table has.
ALTER TABLE units_of_measure
    ALTER COLUMN family_id SET NOT NULL,
    ADD CONSTRAINT uom_factor_positive CHECK (factor_to_base > 0);

-- Each family points at its canonical unit.
UPDATE unit_families f SET base_uom_id = u.id
FROM units_of_measure u
WHERE u.family_id = f.id
  AND u.code = CASE f.code
      WHEN 'WEIGHT' THEN 'KG'
      WHEN 'VOLUME' THEN 'L'
      WHEN 'COUNT'  THEN 'NOS'
      WHEN 'LENGTH' THEN 'M'
      WHEN 'AREA'   THEN 'SQM'
  END;

ALTER TABLE unit_families
    ADD CONSTRAINT unit_families_base_uom_fk
        FOREIGN KEY (base_uom_id) REFERENCES units_of_measure (id) ON DELETE RESTRICT;

CREATE INDEX idx_uom_family ON units_of_measure (family_id);

-- ---------------------------------------------------------------------------
-- product_units becomes the list of units a product may be transacted in
-- ---------------------------------------------------------------------------
--
-- It was a tree: parent_unit_id plus qty_in_parent, so a case held cartons and
-- a carton held pieces. The tree is dropped in favour of a flat list where
-- every entry states its size in the product's base unit directly.
--
-- Flat is not a simplification here, it is a correction. With one stock pool
-- there is no per-level quantity for a hierarchy to describe, and the tree's
-- only remaining job was to derive a size by multiplying up the chain — a
-- repeated division at read time, and a size that silently changes for every
-- descendant when one ancestor is edited. A case is 144 pieces whether or not
-- anyone models the carton in between.
--
-- The table is empty in every known database, so this reshapes rather than
-- migrates.

ALTER TABLE product_units
    DROP COLUMN parent_unit_id,
    DROP COLUMN qty_in_parent,
    -- The unit of measure this is expressed in. NULL means a pure pack — a
    -- "sack" is not a unit of measure, it is 25 kg of this particular product.
    ADD COLUMN uom_id uuid REFERENCES units_of_measure (id) ON DELETE RESTRICT,
    -- How many of the PRODUCT's base unit one of these is: loose kilo 1,
    -- 5 kg bag 5, 25 kg sack 25, 250 g pouch 0.25.
    ADD COLUMN qty_in_base numeric(14,4) NOT NULL DEFAULT 1,
    -- The unit the counter offers first, and the one a bare quantity means.
    ADD COLUMN is_default boolean NOT NULL DEFAULT false,
    -- A unit can be bought but not sold (a supplier's pallet) or the reverse.
    ADD COLUMN is_sellable boolean NOT NULL DEFAULT true,
    ADD COLUMN is_purchasable boolean NOT NULL DEFAULT true,
    ADD COLUMN is_active boolean NOT NULL DEFAULT true,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT product_units_qty_in_base_positive CHECK (qty_in_base > 0);

-- One default per product, enforced rather than hoped for: two defaults means
-- a bare quantity is ambiguous, which is the bug this whole change removes.
CREATE UNIQUE INDEX idx_product_units_one_default
    ON product_units (product_id) WHERE is_default;

-- A barcode must identify one thing. The sack and the packet scan differently,
-- so uniqueness is global across product units, and shares the products table's
-- convention of treating '' as absent.
CREATE UNIQUE INDEX idx_product_units_barcode_unique
    ON product_units (barcode) WHERE barcode IS NOT NULL AND barcode <> '';

-- Every existing product gets its base unit as a sellable default, so nothing
-- that worked before this migration stops working.
INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default, sort_order)
SELECT p.id, p.uom_id, COALESCE(u.name, 'Unit'), 1, true, 0
FROM products p
LEFT JOIN units_of_measure u ON u.id = p.uom_id
WHERE NOT EXISTS (SELECT 1 FROM product_units pu WHERE pu.product_id = p.id);

-- ---------------------------------------------------------------------------
-- A price per unit
-- ---------------------------------------------------------------------------

ALTER TABLE product_prices
    ADD COLUMN product_unit_id uuid REFERENCES product_units (id) ON DELETE CASCADE;

UPDATE product_prices pp
SET product_unit_id = pu.id
FROM product_units pu
WHERE pu.product_id = pp.product_id AND pu.is_default;

ALTER TABLE product_prices
    ALTER COLUMN product_unit_id SET NOT NULL,
    DROP CONSTRAINT product_prices_product_store_key,
    ADD CONSTRAINT product_prices_product_store_unit_key
        UNIQUE (product_id, store_id, product_unit_id);

CREATE INDEX idx_product_prices_unit ON product_prices (product_unit_id);

-- ---------------------------------------------------------------------------
-- Transaction lines say which unit was transacted
-- ---------------------------------------------------------------------------
--
-- quantity keeps its meaning throughout: the amount in the product's BASE
-- unit. Stock, COGS and every existing report go on reading it unchanged.
--
-- unit_qty and product_unit_id are what the customer actually asked for, and
-- what the bill prints. The invariant is
--
--     quantity = unit_qty * product_units.qty_in_base
--
-- and the line total is rate * unit_qty — a multiplication, never a division,
-- so two sacks at 1,050 is exactly 2,100 rather than 25 kg at a rate of
-- 41.999999 per kilo.

ALTER TABLE invoice_items
    ADD COLUMN product_unit_id uuid REFERENCES product_units (id) ON DELETE RESTRICT,
    ADD COLUMN unit_qty numeric(14,3) NOT NULL DEFAULT 0,
    ADD CONSTRAINT invoice_items_unit_qty_non_negative CHECK (unit_qty >= 0);

UPDATE invoice_items ii
SET product_unit_id = pu.id, unit_qty = ii.quantity
FROM product_units pu
WHERE pu.product_id = ii.product_id AND pu.is_default;

ALTER TABLE purchase_items
    ADD COLUMN product_unit_id uuid REFERENCES product_units (id) ON DELETE RESTRICT,
    ADD COLUMN unit_qty numeric(14,3) NOT NULL DEFAULT 0,
    ADD CONSTRAINT purchase_items_unit_qty_non_negative CHECK (unit_qty >= 0);

UPDATE purchase_items pi
SET product_unit_id = pu.id, unit_qty = pi.quantity
FROM product_units pu
WHERE pu.product_id = pi.product_id AND pu.is_default;

CREATE INDEX idx_invoice_items_unit ON invoice_items (product_unit_id);
CREATE INDEX idx_purchase_items_unit ON purchase_items (product_unit_id);
