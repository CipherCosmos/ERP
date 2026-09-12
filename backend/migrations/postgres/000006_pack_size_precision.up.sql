-- A pack size has to be a size stock can hold.
--
-- product_units.qty_in_base was numeric(14,4) while every quantity column is
-- numeric(14,3), and the Go type that carries a quantity renders three places on
-- the way to the database. So a shop defining a 0.5 g sachet — qty_in_base
-- 0.0005 — had it accepted and stored as 0.001. Doubled, with no error anywhere:
-- every later sale of that pack took twice the stock off the shelf and attributed
-- twice the cost.
--
-- The extra place bought nothing. quantity = unit_qty * qty_in_base, and both
-- factors round at three places, so a fourth place in one of them cannot survive
-- the multiplication either. Narrowing the column makes the type say what the
-- system can actually hold, and the Go side now refuses a finer value rather than
-- rounding it.
--
-- Three places is grams within a kilogram and millilitres within a litre, which
-- is the finest division any stocked unit of measure is sold in. A trade that
-- genuinely needs milligrams needs a base unit of grams, not a fourth decimal —
-- and that is an INSERT into units_of_measure, which is why units are rows.

-- Fail loudly rather than round. If any pack size carries a fourth decimal, the
-- figure somebody typed is not the figure stored, and a migration is the wrong
-- place to decide which of the two they meant.
DO $$
DECLARE offending int;
BEGIN
    SELECT count(*) INTO offending
      FROM product_units
     WHERE qty_in_base <> round(qty_in_base, 3);
    IF offending > 0 THEN
        RAISE EXCEPTION
            '% product unit(s) have a pack size finer than 3 decimal places. '
            'Narrowing the column would round them silently. List them with: '
            'SELECT id, product_id, label, qty_in_base FROM product_units '
            'WHERE qty_in_base <> round(qty_in_base, 3); '
            'then correct each one, or give the product a finer base unit of measure.',
            offending;
    END IF;
END $$;

ALTER TABLE product_units
    ALTER COLUMN qty_in_base TYPE numeric(14,3);

COMMENT ON COLUMN product_units.qty_in_base IS
    'How many of the product''s own stocking unit one of these amounts to: a loose '
    'kilo 1, a 25 kg sack 25, a 250 g pouch 0.25. Three decimal places, the same as '
    'every quantity column, because quantity = unit_qty * qty_in_base.';
