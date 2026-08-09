-- What became of each collected item.
--
-- Until now "processed" was the whole story, and it hid three quite different
-- endings behind one timestamp: the item became an article, the article was
-- already known under the same address and this was a second sighting, or the
-- link turned out to lead somewhere not worth storing at all. The counts did not
-- add up and there was no way to say why — 457 collected links against 443
-- articles, with no account of the fourteen.
--
-- NULL means not finished yet, which is what an unprocessed item and a
-- provenance row both are: provenance is never processed by design.

ALTER TABLE raw_items
    ADD COLUMN outcome TEXT
        CHECK (outcome IN ('stored', 'duplicate', 'skipped', 'expanded'));

-- Existing rows finished before this column existed. Marking them 'stored'
-- would be a guess; leaving them NULL says honestly that we do not know, and
-- the statistics page counts them separately.
COMMENT ON COLUMN raw_items.outcome IS
    'stored | duplicate | skipped | expanded; NULL when unfinished or predating the column';
