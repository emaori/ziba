-- Retained email messages are separate from the article/roundup work queues.
CREATE TABLE newsletter_messages (
    id           BIGSERIAL PRIMARY KEY,
    source_id    BIGINT NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    message_key  TEXT NOT NULL CHECK (message_key <> ''),
    title        TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    text         TEXT NOT NULL DEFAULT '',
    extracted_at TIMESTAMPTZ,
    UNIQUE (source_id, message_key)
);

CREATE TABLE newsletter_message_items (
    message_id  BIGINT NOT NULL REFERENCES newsletter_messages (id) ON DELETE RESTRICT,
    raw_item_id BIGINT NOT NULL REFERENCES raw_items (id) ON DELETE RESTRICT,
    PRIMARY KEY (message_id, raw_item_id)
);
CREATE INDEX newsletter_message_items_raw_idx ON newsletter_message_items (raw_item_id);

CREATE TABLE article_origins (
    raw_item_id BIGINT PRIMARY KEY REFERENCES raw_items (id) ON DELETE RESTRICT,
    article_id  BIGINT NOT NULL REFERENCES articles (id) ON DELETE CASCADE
);
CREATE INDEX article_origins_article_idx ON article_origins (article_id);

-- Preserve old email bodies without guessing their original message keys or
-- reconstructing email-to-article relationships. NULL extraction time marks
-- these historical records; newly collected messages use stable identity keys.
INSERT INTO newsletter_messages
    (source_id, message_key, title, author, published_at, collected_at, text)
SELECT source_id, 'legacy:' || id, title, author, published_at, collected_at, text
FROM raw_items WHERE kind = 'provenance';

DELETE FROM raw_items WHERE kind = 'provenance';
ALTER TABLE raw_items DROP CONSTRAINT raw_items_kind_check;
ALTER TABLE raw_items ADD CONSTRAINT raw_items_kind_check
    CHECK (kind IN ('article', 'roundup'));
