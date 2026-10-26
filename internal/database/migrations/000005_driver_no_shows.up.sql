-- Driver no-show accountability.
--
-- The no-show sweep closes bookings where a driver was marked as arrived but
-- never started the trip. Without a counter on the driver there is no record
-- that it happened, so an offender cannot be identified or actioned.
ALTER TABLE drivers
    ADD COLUMN IF NOT EXISTS total_no_shows INTEGER NOT NULL DEFAULT 0;

-- The public driver card exposes a rating and a trip count; a no-show count on
-- a public endpoint would be a privacy and safety concern, so it stays off the
-- projection and is only readable by the driver and by admins.
CREATE INDEX IF NOT EXISTS idx_drivers_no_shows
    ON drivers (total_no_shows)
    WHERE total_no_shows > 0;
