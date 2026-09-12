-- Reverses 000004_batch_value.
--
-- cost_price goes back to being a plain, writable column holding what the
-- generated one last computed, rounded to the four decimal places it used to
-- have. The conserved total is discarded, so the value of a partly sold batch
-- once again cannot be recovered exactly.

ALTER TABLE stock DROP COLUMN cost_price;

ALTER TABLE stock ADD COLUMN cost_price numeric(14,4) NOT NULL DEFAULT 0;

UPDATE stock
   SET cost_price = CASE WHEN quantity > 0 THEN round(total_cost / quantity, 4) ELSE 0 END;

ALTER TABLE stock
    DROP CONSTRAINT IF EXISTS stock_total_cost_non_negative,
    DROP COLUMN total_cost,
    ADD CONSTRAINT stock_cost_price_non_negative CHECK (cost_price >= 0);

ALTER TABLE invoice_items
    DROP CONSTRAINT IF EXISTS invoice_items_cost_of_goods_non_negative,
    DROP COLUMN cost_of_goods;
