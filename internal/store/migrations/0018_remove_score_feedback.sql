-- Restore provider scores before removing the retired feedback state.
-- A missing base score leaves the stored score unchanged; zero is a valid score.
UPDATE articles
SET score = base_score
WHERE base_score IS NOT NULL
  AND score IS DISTINCT FROM base_score;

DROP TABLE article_score_feedback;
ALTER TABLE articles DROP COLUMN base_score;
