-- A batch holds value, not a rate.
--
-- stock stored cost_price per base unit, derived by dividing what a delivery
-- cost by how much of it arrived. That division cannot be stored: a 30 kg sack
-- at Rs.940 is Rs.31.3333... a kilo, and no figure rounded to paise multiplies
-- back up to Rs.940 — thirty times 31.33 is Rs.939.90. Extra decimal places do
-- not help, because the quotient does not terminate.
--
-- So ten paise went missing on every such sack, always in the same direction,
-- between buying the goods and selling them. Measured on the pack sizes a grain
-- or oil trade actually uses:
--
--     30 kg at 940     cost of goods sold 939.90   short by 0.10
--     3 x 30 kg at 940                    2819.70          0.30
--     14 kg at 1850                       1849.96          0.04
--     7 kg at 505                          504.98          0.02
--     25 kg at 940                        3760.00          exact (divides evenly)
--
-- The amounts are small. The broken invariant is not: gross profit could no
-- longer be tied back to what was bought, so nobody could tell a rounding
-- artefact from a real discrepancy — which is the whole job of a cost ledger.
--
-- total_cost is now the authoritative figure and it is conserved. Receiving adds
-- what was paid, with no division at all. Consuming part of a batch moves a
-- proportional share of the value out and leaves the remainder, so the residue
-- of a rounding stays on the shelf instead of evaporating; consuming all of it
-- moves exactly what is left.
--
-- cost_price survives as a GENERATED column so every reader, report and API
-- response that asks a batch what it cost per unit still gets an answer — but it
-- cannot drift from the value it is derived from, and it cannot be written.

ALTER TABLE stock ADD COLUMN total_cost numeric(14,2) NOT NULL DEFAULT 0;

-- The best available reading of history: what the rows say they are worth now.
-- It cannot recover the paise already lost — those were never written down — but
-- it is exactly the figure every report has been using, so no number moves on
-- the day this runs.
UPDATE stock SET total_cost = round(quantity * cost_price, 2);

ALTER TABLE stock
    DROP CONSTRAINT IF EXISTS stock_cost_price_non_negative,
    DROP COLUMN cost_price;

ALTER TABLE stock
    ADD COLUMN cost_price numeric(20,8)
        GENERATED ALWAYS AS (
            CASE WHEN quantity > 0 THEN total_cost / quantity ELSE 0 END
        ) STORED,
    ADD CONSTRAINT stock_total_cost_non_negative CHECK (total_cost >= 0);

COMMENT ON COLUMN stock.total_cost IS
    'What this batch cost, in full. Authoritative and conserved: receiving adds '
    'it, consumption moves a proportional share out. Never derived from a rate.';
COMMENT ON COLUMN stock.cost_price IS
    'Derived: total_cost / quantity. For display and valuation reports. Generated, '
    'so it cannot drift and cannot be written.';

-- An invoice line keeps the cost that actually left the shelf.
--
-- unit_cost_at_sale is a weighted average over the batches consumed, and the
-- cost-of-goods report multiplied it back up by the quantity — reintroducing
-- exactly the rounding above at the other end. The line now carries the total
-- too, and that is what the report sums.
ALTER TABLE invoice_items
    ADD COLUMN cost_of_goods numeric(14,2) NOT NULL DEFAULT 0,
    ADD CONSTRAINT invoice_items_cost_of_goods_non_negative CHECK (cost_of_goods >= 0);

UPDATE invoice_items
   SET cost_of_goods = round(unit_cost_at_sale * quantity, 2)
 WHERE unit_cost_at_sale <> 0;

COMMENT ON COLUMN invoice_items.cost_of_goods IS
    'The cost that left the shelf on this line, in full. What the COGS report sums. '
    'unit_cost_at_sale remains the per-unit figure, for display.';
