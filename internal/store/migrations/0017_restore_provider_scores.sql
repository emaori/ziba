-- Category-wide feedback propagation is retired. Restore every analyzed
-- article from its preserved provider score without touching feedback rows.

UPDATE articles
SET score = base_score
WHERE base_score BETWEEN 0 AND 100
  AND score IS DISTINCT FROM base_score;
