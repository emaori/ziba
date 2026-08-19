-- Keep the provider's answer separate from the personalized score and record
-- one mutable directional correction per article.

ALTER TABLE articles
    ADD COLUMN base_score SMALLINT CHECK (base_score BETWEEN 0 AND 100);

UPDATE articles SET base_score = score WHERE score IS NOT NULL;

CREATE TABLE article_score_feedback (
    article_id BIGINT PRIMARY KEY REFERENCES articles (id) ON DELETE CASCADE,
    direction  TEXT NOT NULL CHECK (direction IN ('higher', 'lower')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
