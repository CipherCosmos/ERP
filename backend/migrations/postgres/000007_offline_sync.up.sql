-- What a till records while the connection is down, and what the server makes of
-- it when it comes back.
--
-- Two things change here.
--
-- 1. A terminal's number series moves onto the till.
--
-- The counter stays, but it becomes a high-water mark rather than the allocator.
-- A till that cannot reach the server still has to hand the customer a tax
-- invoice, and a tax invoice has a number; a provisional slip followed later by
-- a real invoice is not what Rule 46 asks for. So the till allocates from its own
-- terminal's series, online and offline alike — the same path every day, rather
-- than a second path that only runs when the network is down and is therefore
-- never exercised.
--
-- Uniqueness is already enforced: invoices_store_no_key is UNIQUE (store_id,
-- invoice_no). Two devices billing on one terminal code collide there, loudly, on
-- sync — which is the right failure. One till per terminal code is the rule, and
-- the system already models a terminal per counter.
--
-- 2. A sale that happened is never rejected, only flagged.
--
-- The goods left the shop. If the last unit was sold at two counters at once,
-- both sales are real and the shelf is what is wrong; if a customer went over a
-- credit limit the till could not check, the debt is real and the limit is what
-- needs attention. Rejecting a completed sale on sync would mean a shop whose
-- books disagree with its cash drawer, which is the one thing the system exists
-- to prevent. So the sale lands and the exception is recorded for somebody to
-- work through.

CREATE TYPE sync_exception_kind AS ENUM (
    -- Stock went negative, or would have: the same goods were sold twice.
    'oversold',
    -- A credit sale took the customer past the limit the till last saw.
    'credit_limit_exceeded',
    -- The till priced from a catalogue snapshot that had since changed.
    'price_changed',
    -- The till's snapshot was older than the shop's staleness window allows.
    'stale_catalogue'
);

CREATE TABLE sync_exceptions (
    id          uuid                PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id    uuid                NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    terminal_id uuid                REFERENCES terminals (id) ON DELETE RESTRICT,
    -- The document this is about. RESTRICT, like everything else that points at
    -- an invoice: an exception whose subject can be deleted is not a record.
    invoice_id  uuid                REFERENCES invoices (id) ON DELETE RESTRICT,
    product_id  uuid                REFERENCES products (id) ON DELETE RESTRICT,
    customer_id uuid                REFERENCES customers (id) ON DELETE RESTRICT,
    kind        sync_exception_kind NOT NULL,
    -- What the till believed and what the server found, both as text, because
    -- the pair is read by a person and not computed with.
    expected    text                NOT NULL DEFAULT '',
    actual      text                NOT NULL DEFAULT '',
    detail      text                NOT NULL DEFAULT '',
    -- Resolution is a person saying they have dealt with it. It is deliberately
    -- not a delete: an exception that can be made to disappear is not a record
    -- either, and the count of them over a month is the signal that a shop's
    -- offline window is too long or its stock counts are wrong.
    resolved_at timestamptz,
    resolved_by uuid                REFERENCES users (id) ON DELETE RESTRICT,
    resolved_note text              NOT NULL DEFAULT '',
    created_at  timestamptz         NOT NULL DEFAULT now(),
    CONSTRAINT sync_exceptions_resolution_complete CHECK (
        (resolved_at IS NULL AND resolved_by IS NULL)
        OR (resolved_at IS NOT NULL AND resolved_by IS NOT NULL)
    )
);

-- The screen that reads these wants one store's unresolved ones, newest first.
CREATE INDEX idx_sync_exceptions_open ON sync_exceptions (store_id, created_at DESC)
    WHERE resolved_at IS NULL;
CREATE INDEX idx_sync_exceptions_invoice ON sync_exceptions (invoice_id);

-- An invoice records whether it was raised while the till was offline, so a
-- reconciliation can tell an ordinary sale from one the server never saw
-- happen. It is set by the till and never inferred: the server cannot know, and
-- guessing from a timestamp would be wrong on every slow connection.
ALTER TABLE invoices
    ADD COLUMN billed_offline boolean NOT NULL DEFAULT false,
    -- When the till recorded it, in the till's own clock. invoice_date is the
    -- document date and created_at is when the server heard about it; neither is
    -- when the customer was actually served, and a day's takings are counted by
    -- that.
    ADD COLUMN billed_at timestamptz;

UPDATE invoices SET billed_at = invoice_date WHERE billed_at IS NULL;

COMMENT ON COLUMN invoices.billed_offline IS
    'The till raised this while it could not reach the server. Set by the till, '
    'never inferred.';
COMMENT ON COLUMN invoices.billed_at IS
    'When the customer was served, by the till''s clock. created_at is when the '
    'server heard about it, which for an offline sale is later.';
