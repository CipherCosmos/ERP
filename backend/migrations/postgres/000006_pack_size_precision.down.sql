-- Reverses 000006_pack_size_precision.
--
-- Widening is lossless, so this restores the column type without touching data.
-- It also restores the hazard: a pack size with a fourth decimal becomes storable
-- again while quantities remain at three places, so such a size is silently
-- doubled or halved the first time it is sold.

ALTER TABLE product_units
    ALTER COLUMN qty_in_base TYPE numeric(14,4);
