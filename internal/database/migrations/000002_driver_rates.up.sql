-- 000002_driver_rates.up.sql -- counters used by the driver matching ranking

ALTER TABLE drivers ADD COLUMN IF NOT EXISTS total_offers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS accepted_offers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS total_cancellations INTEGER NOT NULL DEFAULT 0;
