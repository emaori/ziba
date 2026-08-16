-- Retry transient processing failures without letting permanently broken rows
-- occupy the front of a queue forever.

ALTER TABLE raw_items
    ADD COLUMN failure_count   INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_error      TEXT NOT NULL DEFAULT '',
    ADD COLUMN failed_at       TIMESTAMPTZ;

ALTER TABLE articles
    ADD COLUMN failure_count   INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_error      TEXT NOT NULL DEFAULT '',
    ADD COLUMN failed_at       TIMESTAMPTZ;

DROP INDEX IF EXISTS raw_items_unprocessed_idx;
DROP INDEX IF EXISTS raw_items_unexpanded_idx;
DROP INDEX IF EXISTS articles_unanalyzed_idx;

CREATE INDEX raw_items_unprocessed_idx
    ON raw_items (last_attempt_at, collected_at)
    WHERE processed_at IS NULL AND failed_at IS NULL AND kind = 'article';

CREATE INDEX raw_items_unexpanded_idx
    ON raw_items (last_attempt_at, collected_at)
    WHERE processed_at IS NULL AND failed_at IS NULL AND kind = 'roundup';

CREATE INDEX articles_unanalyzed_idx
    ON articles (last_attempt_at, collected_at)
    WHERE processed_at IS NULL AND failed_at IS NULL;
