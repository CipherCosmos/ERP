-- A return records whether the till was offline when it was taken, and when the
-- customer was actually served. Same reasoning as an invoice: the server hears
-- about it later and cannot know either, and a day's refunds are counted by when
-- they happened.

ALTER TABLE returns
    ADD COLUMN raised_offline boolean NOT NULL DEFAULT false,
    ADD COLUMN raised_at timestamptz;

UPDATE returns SET raised_at = created_at WHERE raised_at IS NULL;

COMMENT ON COLUMN returns.raised_offline IS
    'The till took this while it could not reach the server. Stated by the till, '
    'never inferred.';
COMMENT ON COLUMN returns.raised_at IS
    'When the customer was served, by the till''s clock. created_at is when the '
    'server heard about it.';
