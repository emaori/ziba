-- Retain historical source rows and their IDs so articles, raw items and
-- digest membership stay intact. They are no longer configured or collected.
UPDATE sources SET enabled = FALSE WHERE type NOT IN ('rss', 'newsletter');

ALTER TABLE sources DROP CONSTRAINT sources_type_check;

-- NOT VALID preserves the historical rows while enforcing the supported
-- types on every new insert or update, including attempts to re-enable them.
ALTER TABLE sources ADD CONSTRAINT sources_type_check
    CHECK (type IN ('rss', 'newsletter')) NOT VALID;
