-- Reverses 000007_offline_sync.
--
-- The exceptions go with it. They are a record of sales the server could not
-- fully verify, so losing them means losing the only note that anyone needed to
-- look at those sales.

ALTER TABLE invoices
    DROP COLUMN IF EXISTS billed_offline,
    DROP COLUMN IF EXISTS billed_at;

DROP TABLE IF EXISTS sync_exceptions;
DROP TYPE IF EXISTS sync_exception_kind;
