-- Reverses 000010_product_shelf_life.
--
-- The flag goes, and with it the only record of which goods a shop watches for
-- expiry. The dates on the batches survive; what is lost is knowing which
-- products should have had one.

DROP INDEX IF EXISTS idx_products_tracks_expiry;

ALTER TABLE products
    DROP CONSTRAINT IF EXISTS products_shelf_life_needs_tracking,
    DROP CONSTRAINT IF EXISTS products_shelf_life_positive,
    DROP COLUMN IF EXISTS tracks_expiry,
    DROP COLUMN IF EXISTS shelf_life_days,
    DROP COLUMN IF EXISTS preferred_vendor_id;
