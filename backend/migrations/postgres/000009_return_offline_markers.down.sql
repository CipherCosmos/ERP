-- Reverses 000009_return_offline_markers.

ALTER TABLE returns
    DROP COLUMN IF EXISTS raised_offline,
    DROP COLUMN IF EXISTS raised_at;
