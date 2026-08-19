-- Extraction can return a large, non-empty body that belongs to an index,
-- login page, or another article. Keep the model's structured judgement beside
-- the summary so readers never have to infer reliability from its wording.
--
-- Existing analyzed articles predate this check. Treat them exactly as before:
-- complete is the backward-compatible default, and only a new analysis can
-- mark an article as degraded.
ALTER TABLE articles
    ADD COLUMN content_quality TEXT NOT NULL DEFAULT 'complete'
        CHECK (content_quality IN ('complete', 'limited', 'mismatched', 'unavailable')),
    ADD COLUMN content_quality_reason TEXT NOT NULL DEFAULT '';
