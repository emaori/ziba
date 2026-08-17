-- A durable one-time request lets setup trigger its first collection without
-- coupling the web handler to the running scheduler.

ALTER TABLE app_settings
    ADD COLUMN collection_requested BOOLEAN NOT NULL DEFAULT FALSE;
