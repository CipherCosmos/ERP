-- Reverses 000005_scoped_idempotency.
--
-- The key space becomes global again. Where two shops had used the same key,
-- only one row can survive that: the oldest is kept, because it is the one a
-- retry of the original request would be looking for.

DELETE FROM processed_requests pr
 WHERE EXISTS (
   SELECT 1 FROM processed_requests older
    WHERE older.key = pr.key
      AND (older.created_at, older.store_id) < (pr.created_at, pr.store_id));

ALTER TABLE processed_requests
    DROP CONSTRAINT processed_requests_pkey,
    DROP COLUMN store_id,
    ADD PRIMARY KEY (key);
