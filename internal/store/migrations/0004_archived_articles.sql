-- Marking an article read takes it out of circulation.
--
-- "Archived" here means the reader pressed a button, not that time passed. An
-- archived article disappears from the per-interest tabs and from the daily
-- selection, but remains in the day-by-day view, which deliberately shows
-- everything. The column is nullable rather than a boolean so it records *when*
-- as well as *whether*, which costs nothing and answers "what did I read on
-- Tuesday" later.
ALTER TABLE articles ADD COLUMN archived_at TIMESTAMPTZ;

-- The tab queries all ask the same shape: one interest, above threshold, not
-- archived, newest first. The partial index covers the unarchived majority and
-- shrinks as things are read.
CREATE INDEX articles_unarchived_idx
    ON articles (published_at DESC)
    WHERE archived_at IS NULL AND processed_at IS NOT NULL;

-- Filtering by interest means asking whether an array contains a value, which
-- only a GIN index can answer without reading every row.
CREATE INDEX articles_categories_idx ON articles USING GIN (categories);
