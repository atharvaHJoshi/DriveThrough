-- 000002_driver_rates.down.sql -- Rollback

ALTER TABLE drivers DROP COLUMN IF EXISTS total_cancellations;
ALTER TABLE drivers DROP COLUMN IF EXISTS accepted_offers;
ALTER TABLE drivers DROP COLUMN IF EXISTS total_offers;
