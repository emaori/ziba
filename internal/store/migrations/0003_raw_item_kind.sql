-- Not every collected item is destined to become an article.
--
-- A newsletter is a list of links: what belongs in Ziba are the articles it
-- points at, not the email itself. The email is still kept, because knowing
-- where a link came from is worth having — but it is provenance, not reading
-- material, and must never reach the archive as an article of its own.

ALTER TABLE raw_items
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'article'
        CHECK (kind IN ('article', 'provenance'));

-- The backlog query filters on kind as well as processing state, so the partial
-- index has to agree with it or it stops being used.
DROP INDEX IF EXISTS raw_items_unprocessed_idx;

CREATE INDEX raw_items_unprocessed_idx
    ON raw_items (collected_at)
    WHERE processed_at IS NULL AND kind = 'article';
