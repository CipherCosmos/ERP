-- A product's name within its category identifies it, and nothing enforced it.
--
-- This is not tidiness. The catalogue import matches a sheet row to a stored
-- product by name within category — which is how most rows match, because a
-- shop's own sheet rarely carries a barcode — and the lookup takes the first
-- row it finds. Two products sharing a name in one category make that match
-- arbitrary: which of them a price revision lands on is decided by the planner.
-- The export/import round trip a shop uses to revise prices is only sound if
-- the pair identifies exactly one product.
--
-- It is also the mess that merging categories exists to clean up, one level
-- down. "Biscuit" beside "Biscuits" splits a shop's reporting in two; so does
-- "Marie Gold" beside "Marie gold", and the second is easier to make and
-- harder to see.
--
-- Active products only. A withdrawn line keeps its name in the history that
-- references it, and holding a retired product's name against a new one would
-- mean a shop could never reuse a name it had stopped using.

DO $$
DECLARE
    clash text;
BEGIN
    -- Named, not identified: whoever reads this has to find the products in
    -- the app, and the app shows them by name.
    SELECT string_agg(format('%s x%s in %s', d.name, d.n, COALESCE(c.name, 'no category')), '; ')
      INTO clash
      FROM (
        SELECT min(p.name) AS name, p.category_id, count(*) AS n
          FROM products p
         WHERE p.is_active
         GROUP BY p.category_id, lower(p.name)
        HAVING count(*) > 1
         LIMIT 20
      ) d
      LEFT JOIN categories c ON c.id = d.category_id;

    IF clash IS NOT NULL THEN
        RAISE EXCEPTION
            'products share a name inside one category and must be merged or renamed first: %', clash
            USING HINT = 'Rename or deactivate the duplicates, then run the migration again.';
    END IF;
END $$;

CREATE UNIQUE INDEX idx_products_name_in_category
    ON products (category_id, lower(name))
    WHERE is_active;
