-- Some feeds publish issues, not articles.
--
-- A link roundup — ".NET Ketchup - Week 32, 2026" and its kind — carries one
-- feed entry per week, and that entry points at a page listing ten other
-- people's articles. Followed like an ordinary entry it yields a single stored
-- item that is a table of contents: scored, categorised and useless.
--
-- Such an entry is collected as a roundup instead. It is fetched once so the
-- links can be extracted, each of which becomes an article in its own right,
-- and the issue itself never becomes one.

ALTER TABLE raw_items DROP CONSTRAINT raw_items_kind_check;

ALTER TABLE raw_items
    ADD CONSTRAINT raw_items_kind_check
        CHECK (kind IN ('article', 'provenance', 'roundup'));

-- Roundups are queued the same way articles are, but drained by a different
-- stage, so they get their own partial index rather than widening the existing
-- one — the article backlog is read far more often and should stay narrow.
CREATE INDEX raw_items_unexpanded_idx
    ON raw_items (collected_at)
    WHERE processed_at IS NULL AND kind = 'roundup';
