-- What each article's analysis cost, in tokens, as the provider reported it.
--
-- On the article rather than in a table of calls. Every question the statistics
-- page asks is grouped by something the article already carries — its
-- categories, the day it was analyzed — so a separate table would be joined
-- back to this one every time and would buy only a history of individual calls
-- that nothing asks for.
--
-- Zero is the honest value for the articles already here: they were analyzed
-- offline, by a matcher that spent nothing. It is not a stand-in for unknown,
-- and processed_at already distinguishes an article that was never analyzed at
-- all.
ALTER TABLE articles
    ADD COLUMN input_tokens  INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0);

-- The by-day figures group on the analysis date, which is not the collection
-- date any other page uses: tokens are spent when the model runs, and a backfill
-- spends them all on one day for articles gathered over weeks.
CREATE INDEX articles_processed_at_idx ON articles (processed_at)
    WHERE processed_at IS NOT NULL;
