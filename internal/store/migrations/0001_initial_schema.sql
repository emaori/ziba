-- Initial schema: the four entities of the domain glossary.

CREATE TABLE sources (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL,
    type       TEXT        NOT NULL CHECK (type IN ('rss', 'website', 'newsletter', 'pdf')),
    url        TEXT        NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The same address may legitimately be read in two ways (a site both
    -- scraped and followed by feed), but not twice the same way.
    UNIQUE (type, url)
);

-- Raw collected elements, kept as provenance. A newsletter stays here as the
-- item it was, while the articles it linked to live in `articles`.
CREATE TABLE raw_items (
    id           BIGSERIAL PRIMARY KEY,
    source_id    BIGINT      NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    title        TEXT        NOT NULL DEFAULT '',
    url          TEXT        NOT NULL,
    author       TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    text         TEXT        NOT NULL DEFAULT '',

    -- NULL until the pipeline has turned this item into articles. This is what
    -- makes re-running collection cheap: already processed items are skipped.
    processed_at TIMESTAMPTZ,

    -- A feed repeats its entries on every poll; this is what makes collection
    -- idempotent.
    UNIQUE (source_id, url)
);

CREATE INDEX raw_items_unprocessed_idx
    ON raw_items (collected_at)
    WHERE processed_at IS NULL;

-- The central entity. Identity is the normalized URL, never the title.
CREATE TABLE articles (
    id           BIGSERIAL PRIMARY KEY,
    source_id    BIGINT      NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    url          TEXT        NOT NULL UNIQUE,
    title        TEXT        NOT NULL,
    author       TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    full_text    TEXT        NOT NULL DEFAULT '',
    categories   TEXT[]      NOT NULL DEFAULT '{}',
    summary      TEXT        NOT NULL DEFAULT '',

    -- NULL until the pipeline has scored it. A summary is only produced above
    -- threshold, so it stays empty for most articles.
    score        SMALLINT    CHECK (score BETWEEN 0 AND 100),
    processed_at TIMESTAMPTZ
);

-- Serves the digest query: the most relevant articles of a given day.
CREATE INDEX articles_ranking_idx ON articles (collected_at DESC, score DESC NULLS LAST);

-- The daily selection. Stored rather than recomputed, so that past digests stay
-- exactly as they were read even after interests or thresholds change.
CREATE TABLE digests (
    id           BIGSERIAL PRIMARY KEY,
    date         DATE        NOT NULL UNIQUE,
    threshold    SMALLINT    NOT NULL CHECK (threshold BETWEEN 0 AND 100),
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE digest_articles (
    digest_id  BIGINT  NOT NULL REFERENCES digests (id) ON DELETE CASCADE,
    article_id BIGINT  NOT NULL REFERENCES articles (id) ON DELETE CASCADE,

    -- Position in the digest, 1-based. Frozen at generation time.
    ordinal    INTEGER NOT NULL,

    PRIMARY KEY (digest_id, article_id),
    UNIQUE (digest_id, ordinal)
);
