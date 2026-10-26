-- Give an in-flight webhook claim an owner, so concurrent redeliveries of the
-- same event cannot all process it.
--
-- 000009 made gateway_events a retryable inbox by leaving processed_at NULL
-- until the work succeeded. That alone is not sufficient: "unprocessed" is
-- satisfied by every concurrent redelivery at the same instant, so a gateway
-- delivering the same capture twice at once would have both deliveries process
-- it, crediting driver earnings more than once.
--
-- claimed_at records which worker holds the claim. A redelivery only proceeds
-- when no live claim exists; an abandoned claim expires after the lease, so a
-- worker that dies mid-processing does not block the event forever.
ALTER TABLE gateway_events
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;

-- Events that were claimed but never completed carry no useful claim record and
-- would otherwise sit in the index indefinitely.
CREATE INDEX IF NOT EXISTS idx_gateway_events_stale_claims
    ON gateway_events (claimed_at)
    WHERE processed_at IS NULL AND claimed_at IS NOT NULL;
