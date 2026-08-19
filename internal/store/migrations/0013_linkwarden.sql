-- Linkwarden is optional user configuration. Secrets are write-only in the UI.
ALTER TABLE app_settings
    ADD COLUMN linkwarden_enabled  BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN linkwarden_url      TEXT NOT NULL DEFAULT '',
    ADD COLUMN linkwarden_auth     TEXT NOT NULL DEFAULT 'credentials'
        CHECK (linkwarden_auth IN ('credentials', 'token')),
    ADD COLUMN linkwarden_username TEXT NOT NULL DEFAULT '',
    ADD COLUMN linkwarden_password TEXT NOT NULL DEFAULT '',
    ADD COLUMN linkwarden_token    TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN app_settings.linkwarden_password IS
    'write-only in the web UI; never returned to a rendered form';
COMMENT ON COLUMN app_settings.linkwarden_token IS
    'write-only in the web UI; never returned to a rendered form';
