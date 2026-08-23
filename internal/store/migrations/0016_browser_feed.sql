-- Some publishers protect even their RSS endpoints with browser-oriented bot
-- rules. Browser fetching is an explicit per-feed compatibility option; every
-- existing source keeps the direct HTTP path.

ALTER TABLE sources
    ADD COLUMN browser_fetch BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT sources_browser_fetch_rss
        CHECK (NOT browser_fetch OR type = 'rss');
