-- What a product is, beyond its name and its price.
--
-- Three things a catalogue has to be able to say and could not.
--
-- 1. Whether expiry matters.
--
-- A batch carries an expiry date, but nothing said which products need one.
-- Rice does, salt does not, and the difference was left to whoever was typing at
-- the receiving bay. So the expiry report was only ever as complete as the
-- clerk's memory: goods that should have been watched were invisible on it
-- because nobody had entered a date, and nothing anywhere said a date was
-- missing. A shop finds that out when a customer brings back something that
-- expired in the stockroom.
--
-- tracks_expiry makes it the product's property, so receiving can insist on a
-- date for the goods that need one and stop asking for the goods that do not.
-- shelf_life_days is how long it lasts from manufacture, which lets a receipt
-- work out the expiry the clerk would otherwise be reading off a packet.
--
-- 2. Who normally supplies it.
--
-- The reorder level has been there from the start and there was no way to act on
-- it: a report could say "you are low on these forty things" and somebody still
-- had to work out who sells each one. The preferred vendor is what makes that a
-- purchase order instead of a list.
--
-- RESTRICT, not SET NULL: losing which vendor supplies something because the
-- vendor row was removed is the same class of silent gap as the expiry dates
-- above. A vendor that is no longer used is deactivated, and deactivating one
-- leaves this pointing at it, which is correct — it is where the goods came
-- from.
--
-- 3. What it is called in the language the customer reads.
--
-- name_hi and name_kn have been in this table since the beginning and the bill
-- can print them. Nothing could enter them. A shop in Bangalore printing
-- "Basmati Rice" to a customer who reads Kannada has the column, the renderer
-- and no way to fill it in — which is not a schema change, but it belongs in the
-- same pass, and this comment is the record of why it was left so long.

ALTER TABLE products
    ADD COLUMN tracks_expiry boolean NOT NULL DEFAULT false,
    ADD COLUMN shelf_life_days integer,
    ADD COLUMN preferred_vendor_id uuid REFERENCES vendors (id) ON DELETE RESTRICT,
    ADD CONSTRAINT products_shelf_life_positive
        CHECK (shelf_life_days IS NULL OR shelf_life_days > 0),
    -- A shelf life on something that does not expire is a contradiction, and the
    -- pair is read together everywhere: receiving computes an expiry from it,
    -- the expiry report filters on it.
    ADD CONSTRAINT products_shelf_life_needs_tracking
        CHECK (shelf_life_days IS NULL OR tracks_expiry);

-- Anything that already has a batch with an expiry date plainly expires. This is
-- the one inference worth making: the shop has been recording dates for these,
-- so the flag is describing what it already does rather than deciding for it.
UPDATE products p
   SET tracks_expiry = true
 WHERE EXISTS (
   SELECT 1 FROM stock s
    WHERE s.product_id = p.id AND s.expiry_date IS NOT NULL);

-- The expiry report reads this to decide what it should be complaining about.
CREATE INDEX idx_products_tracks_expiry ON products (tracks_expiry) WHERE tracks_expiry;

COMMENT ON COLUMN products.tracks_expiry IS
    'This product has a shelf life, so receiving asks for an expiry date and the '
    'expiry report covers it. Set per product, never inferred per batch.';
COMMENT ON COLUMN products.shelf_life_days IS
    'How long it lasts from manufacture. Lets a receipt work out the expiry date '
    'instead of a clerk reading it off the packet. NULL means the date is stated.';
COMMENT ON COLUMN products.preferred_vendor_id IS
    'Who normally supplies this, so a low-stock list can become a purchase order.';
