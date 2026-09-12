-- A tax invoice must keep the particulars it was issued with.
--
-- invoice_items stored a product_id and nothing describing the goods, so the
-- read path joined the live products table for the name. A reprint therefore
-- showed the catalogue as it is today, not the document as it was issued:
-- rename "Milk 500ml" to "Toned Milk 500 ml" and every past bill silently
-- reprints under the new name. CGST Rule 46 requires the description, the HSN
-- code and the rate of tax to be *on the invoice*; a join is not that.
--
-- The HSN code was worse than stale — it was absent. Nothing on the line
-- carried it and nothing on the read path produced it, so a reprint from the
-- invoice list could not be a compliant tax invoice at all. The counter worked
-- around it by looking the code up from the catalogue at the moment of sale,
-- which is exactly the staleness above, just earlier.
--
-- Rate of tax has the same problem with a sharper edge: GST rates change by
-- notification. A product moved from 12% to 5% would make every older invoice
-- reprint at 5%, understating tax the shop has already collected and remitted.
--
-- Three snapshots, written once when the line is created and never updated:
--
--   description  the goods as named on the document
--   hsn_code     the classification as declared on the document
--   gst_rate     the rate as charged on the document
--
-- What deliberately stays joined is the *unit label*. "2 x 25 kg sack" is a
-- presentation of the unit, and a shop that renames "sack" to "gunny" has
-- renamed it everywhere rather than forked it per bill. The figures do not move
-- with it: qty_in_base is pinned on product_units, and unit_qty and quantity
-- are both on the line.

ALTER TABLE invoice_items
    ADD COLUMN description text         NOT NULL DEFAULT '',
    ADD COLUMN hsn_code    text         NOT NULL DEFAULT '',
    ADD COLUMN gst_rate    numeric(5,2) NOT NULL DEFAULT 0,
    ADD CONSTRAINT invoice_items_gst_rate_valid CHECK (gst_rate BETWEEN 0 AND 100);

-- Backfill from the catalogue. This is the best available reading of history
-- and is not claimed to be more: for a product renamed or reclassified before
-- this migration ran, the old document is already unrecoverable. It is correct
-- from here on, which is the point of snapshotting.
UPDATE invoice_items ii
   SET description = p.name,
       hsn_code    = COALESCE(p.hsn_code, ''),
       gst_rate    = p.gst_rate
  FROM products p
 WHERE p.id = ii.product_id;

-- Every line now names its goods. A line that does not is a line nobody can
-- defend to an officer, so the default stops applying to new rows.
ALTER TABLE invoice_items
    ALTER COLUMN description DROP DEFAULT;
