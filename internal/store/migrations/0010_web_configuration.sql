-- Sources and interests become application data managed through the web UI.
-- Existing source rows keep their IDs, so collected articles remain attached.

CREATE TABLE app_settings (
    singleton  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    configured BOOLEAN NOT NULL DEFAULT FALSE,
    threshold  SMALLINT NOT NULL DEFAULT 60 CHECK (threshold BETWEEN 0 AND 100),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO app_settings (singleton) VALUES (TRUE);

CREATE TABLE interests (
    id         BIGSERIAL PRIMARY KEY,
    topic      TEXT NOT NULL UNIQUE,
    priority   INTEGER NOT NULL CHECK (priority >= 1),
    subtopics  TEXT[] NOT NULL DEFAULT '{}',
    note       TEXT NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0
);

ALTER TABLE sources
    ADD COLUMN roundup             BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN collect_from        TEXT NOT NULL DEFAULT '',
    ADD COLUMN newsletter_folder   TEXT NOT NULL DEFAULT '',
    ADD COLUMN newsletter_username TEXT NOT NULL DEFAULT '',
    ADD COLUMN newsletter_password TEXT NOT NULL DEFAULT '',
    ADD COLUMN newsletter_days     INTEGER NOT NULL DEFAULT 1 CHECK (newsletter_days >= 0),
    ADD COLUMN newsletter_max      INTEGER NOT NULL DEFAULT 0 CHECK (newsletter_max >= 0);

COMMENT ON COLUMN sources.newsletter_password IS
    'write-only in the web UI; never returned to a rendered form';
