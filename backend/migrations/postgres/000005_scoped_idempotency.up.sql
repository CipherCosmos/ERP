-- An idempotency key belongs to a shop, not to the world.
--
-- processed_requests was keyed on the client's string alone, so every shop drew
-- from one key space. A key only has to be unique to whoever generates it, and
-- the keys a till generates are per-terminal counters — "till-1-0001" — which two
-- shops produce on their first day.
--
-- The consequence had two halves, and the second is the one a shop would notice:
--
--   1. A replay could cross the boundary and hand one shop a copy of another's
--      invoice while silently dropping the sale actually asked for. That half was
--      already closed, by comparing the store on the stored response and
--      refusing a mismatch.
--   2. Refusing is not success. A shop whose key had been used by another shop
--      could not raise its sale under that key at all: it got a 409 at the till,
--      with a customer waiting, for a collision it had no way to see or avoid.
--      Only a new key got it through, and a till with a counter has no other key
--      to offer.
--
-- Scoping the key to the store makes both halves impossible rather than handled.
-- Two shops sending "till-1-0001" now address two different rows, and each gets
-- its own sale.
--
-- Rows that cannot be attributed to a store are removed. They are retry records
-- with a short useful life — there is a prune job for exactly that reason — and
-- one that names no store can never match a request again, because every request
-- now names one. The window this opens is narrow and worth stating plainly: a
-- retry already in flight when this migration runs will not find its original
-- and will raise a second document. Run it when the tills are closed.

ALTER TABLE processed_requests
    ADD COLUMN store_id uuid REFERENCES stores (id) ON DELETE RESTRICT;

-- A billing row carries its invoice in the stored response; a payment row names
-- the invoice it settled. Either way the invoice knows the store.
UPDATE processed_requests pr
   SET store_id = i.store_id
  FROM invoices i
 WHERE i.id = COALESCE(
        NULLIF(pr.response_body -> 'invoice' ->> 'id', '')::uuid,
        NULLIF(pr.response_body ->> 'invoice_id', '')::uuid);

DELETE FROM processed_requests WHERE store_id IS NULL;

ALTER TABLE processed_requests
    ALTER COLUMN store_id SET NOT NULL,
    DROP CONSTRAINT processed_requests_pkey,
    ADD PRIMARY KEY (store_id, key);

COMMENT ON COLUMN processed_requests.store_id IS
    'The shop the key belongs to. Part of the primary key: a client only has to '
    'make its keys unique to itself, and two shops both counting from 1 must not '
    'collide.';
