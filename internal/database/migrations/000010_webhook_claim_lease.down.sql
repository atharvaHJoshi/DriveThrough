-- Drop the claim lease column and its index.
ALTER TABLE gateway_events DROP INDEX IF EXISTS idx_gateway_events_stale_claims;

ALTER TABLE gateway_events DROP COLUMN IF EXISTS claimed_at;
