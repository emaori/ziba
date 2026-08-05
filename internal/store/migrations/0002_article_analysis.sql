-- Results of the AI pipeline. Categories, summary and score already exist; this
-- adds what extraction and scoring produce alongside them.

ALTER TABLE articles
    ADD COLUMN entities     TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN tone         TEXT   NOT NULL DEFAULT '',

    -- Why the article scored what it scored. Kept because a score without a
    -- reason is impossible to argue with when the ranking looks wrong.
    ADD COLUMN score_reason TEXT   NOT NULL DEFAULT '';

-- Finding the analysis backlog stays cheap as the archive grows.
CREATE INDEX articles_unanalyzed_idx
    ON articles (collected_at)
    WHERE processed_at IS NULL;
