-- Reverses 000003_invoice_line_snapshot.
--
-- This discards the only record of how each invoice described its goods. After
-- a down migration the read path is back to joining the catalogue, so a reprint
-- again shows today's names, codes and rates rather than the document's.

ALTER TABLE invoice_items
    DROP CONSTRAINT IF EXISTS invoice_items_gst_rate_valid,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS hsn_code,
    DROP COLUMN IF EXISTS gst_rate;
