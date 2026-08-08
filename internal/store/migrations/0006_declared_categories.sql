-- Some sources do not need to be classified: the reader already knows what
-- they are about.
--
-- A .NET newsletter publishes .NET articles. Asking a model which interests
-- each one belongs to spends tokens rediscovering something already known, and
-- gets it wrong often enough to matter — the offline analyzer filed a piece
-- about FastEndpoints as uncategorised because it never wrote ".NET", and the
-- interest filter then hid it.
--
-- A source may therefore declare its categories. They are assigned rather than
-- inferred, the assessment scores how interesting the piece is instead of
-- whether it is on topic, and the relevance threshold does not apply: the
-- reader subscribed to this, so it is shown.
--
-- The configuration file remains the source of truth. This column is written
-- from it on every run, like name and enabled, and exists only because the
-- reading queries need it at query time.

ALTER TABLE sources ADD COLUMN categories TEXT[] NOT NULL DEFAULT '{}';
