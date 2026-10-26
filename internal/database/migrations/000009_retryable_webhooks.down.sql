-- Restore the non-nullable, always-set processed_at column.
--
-- Any row left unprocessed will block this, which is the intended signal rather
-- than a silent reclassification of in-flight events as handled.
DELETE FROM gateway_events WHERE processed_at IS NULL;

ALTER TABLE gateway_events
    ALTER COLUMN processed_at SET DEFAULT NOW(),
    ALTER COLUMN processed_at SET NOT NULL;

DROP INDEX IF EXISTS idx_gateway_events_unprocessed;

CREATE INDEX IF NOT EXISTS idx_gateway_events_processed
    ON gateway_events (processed_at DESC);
