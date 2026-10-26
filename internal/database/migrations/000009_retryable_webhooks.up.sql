-- Make gateway event deduplication retry-safe.
--
-- gateway_events.processed_at was NOT NULL DEFAULT NOW(), and the webhook
-- handler inserted the event *before* doing any work. The column name claimed a
-- processed event while the row only recorded that processing had been
-- attempted.
--
-- The consequence was that any transient failure — a dropped connection to the
-- gateway, a database error mid-update — left the event marked as handled. The
-- gateway's own retry then hit the duplicate check, was discarded, and the local
-- payment state silently diverged from the gateway's, with no way to recover
-- short of manual intervention.
--
-- Making the column nullable turns the table into a proper inbox: a row is
-- claimed on arrival, marked processed only once the work has actually
-- succeeded, and remains claimable by any retry until then.
ALTER TABLE gateway_events
    ALTER COLUMN processed_at DROP NOT NULL,
    ALTER COLUMN processed_at DROP DEFAULT;

-- A partial index over only the unprocessed rows. Deduplication lookups are the
-- only reads that care, and completed events vastly outnumber pending ones, so
-- indexing the NULL rows keeps the index small.
DROP INDEX IF EXISTS idx_gateway_events_processed;

CREATE INDEX IF NOT EXISTS idx_gateway_events_unprocessed
    ON gateway_events (id)
    WHERE processed_at IS NULL;
