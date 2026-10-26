-- 000004_integrity_indexes.down.sql -- Rollback
--
-- Constraints and indexes added in 000004 are dropped in reverse order.
-- The CHECK constraints are removed rather than reversed: re-deriving the
-- pre-migration (unvalidated) state would mean accepting the corrupt values
-- those constraints exist to reject.

-- Value constraints.
ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS chk_driver_doc_number;
ALTER TABLE sos DROP CONSTRAINT IF EXISTS chk_sos_lng;
ALTER TABLE sos DROP CONSTRAINT IF EXISTS chk_sos_lat;
ALTER TABLE trip_locations DROP CONSTRAINT IF EXISTS chk_trip_location_lng;
ALTER TABLE trip_locations DROP CONSTRAINT IF EXISTS chk_trip_location_lat;
ALTER TABLE vehicle_inspections DROP CONSTRAINT IF EXISTS chk_inspection_odometer;
ALTER TABLE vehicle_inspections DROP CONSTRAINT IF EXISTS chk_inspection_fuel;
ALTER TABLE trips DROP CONSTRAINT IF EXISTS chk_trips_fuel;
ALTER TABLE trips DROP CONSTRAINT IF EXISTS chk_trips_odometer;
ALTER TABLE ratings DROP CONSTRAINT IF EXISTS chk_ratings_not_self;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_breakdown;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_amount;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_dest_lng;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_dest_lat;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_pickup_lng;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_pickup_lat;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_duration;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_final_price;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_estimated_price;

-- Handover OTP throttling. The counter is dropped with the column; any in-flight
-- lockout state is lost, which is the pre-migration behaviour.
ALTER TABLE bookings DROP COLUMN IF EXISTS otp_attempts;

-- Index coverage.
DROP INDEX IF EXISTS idx_notifications_unread;
DROP INDEX IF EXISTS idx_device_tokens_user;
DROP INDEX IF EXISTS idx_emergency_contacts_user;
DROP INDEX IF EXISTS idx_sos_user;
DROP INDEX IF EXISTS idx_sos_open;
DROP INDEX IF EXISTS idx_driver_documents_driver;
DROP INDEX IF EXISTS idx_driver_earnings_driver;
DROP INDEX IF EXISTS idx_ratings_to_user;
DROP INDEX IF EXISTS idx_vehicle_inspections_trip;
DROP INDEX IF EXISTS idx_trip_locations_trip_time;
DROP INDEX IF EXISTS idx_matching_offers_expiring;
DROP INDEX IF EXISTS idx_bookings_active_driver_lookup;
DROP INDEX IF EXISTS idx_bookings_searching_age;
DROP INDEX IF EXISTS idx_bookings_scheduled_ready;

-- Webhook idempotency. Records are the dedupe history, so they are dropped with
-- the table; replaying an already-processed event afterwards is a no-op against
-- the gateway because the underlying payment status is itself idempotent.
DROP TABLE IF EXISTS gateway_events;

-- Concurrency constraints.
DROP INDEX IF EXISTS uq_driver_earnings_booking;
DROP INDEX IF EXISTS uq_refunds_live_payment;
DROP INDEX IF EXISTS uq_payments_active_booking;
DROP INDEX IF EXISTS uq_bookings_active_driver;
DROP INDEX IF EXISTS uq_matching_offers_pending;
