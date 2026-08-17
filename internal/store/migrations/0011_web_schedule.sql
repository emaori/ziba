-- Collection timing becomes user configuration managed through Settings.
-- NULL means the one-time compatibility import has not run yet.

ALTER TABLE app_settings
    ADD COLUMN collect_every TEXT,
    ADD COLUMN collect_at    TEXT;
