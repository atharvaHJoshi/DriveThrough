DROP INDEX IF EXISTS idx_drivers_no_shows;

ALTER TABLE drivers
    DROP COLUMN IF EXISTS total_no_shows;
