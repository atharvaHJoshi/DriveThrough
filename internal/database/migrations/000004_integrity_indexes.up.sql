-- Data integrity, concurrency control, and index coverage.
--
-- The application-level checks in the services are not sufficient on their own:
-- several of them run as separate statements, so two concurrent requests can
-- both pass them. Each constraint below moves a rule that was only advisory in
-- Go down into the database, where it is enforced atomically.

-- ---------------------------------------------------------------------------
-- Concurrency
-- ---------------------------------------------------------------------------

-- At most one live offer per (booking, driver). CreateOffer upserts against this
-- index so the re-drive worker cannot stack duplicate PENDING rows each poll.
CREATE UNIQUE INDEX IF NOT EXISTS uq_matching_offers_pending
    ON matching_offers (booking_id, driver_id)
    WHERE status = 'PENDING';

-- A driver can only be committed to one live booking at a time. The Redis lock
-- is best-effort (it is released as soon as the offer is sent, and is lost if
-- Redis restarts), so this is the backstop that stops one driver being assigned
-- to two simultaneous customers.
CREATE UNIQUE INDEX IF NOT EXISTS uq_bookings_active_driver
    ON bookings (driver_id)
    WHERE driver_id IS NOT NULL
      AND status IN (
          'DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE', 'DRIVER_ARRIVED', 'OTP_VERIFIED',
          'TRIP_STARTED', 'TRIP_PAUSED', 'TRIP_RESUMED'
      );

-- One payment per booking. Without this a retried capture or a duplicate
-- customer.pay webhook inserts a second row, and the booking is then paid
-- twice. Failed attempts are excluded so a retry after failure is still allowed.
CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_active_booking
    ON payments (booking_id)
    WHERE status <> 'FAILED';

-- One refund per payment, for the same reason.
CREATE UNIQUE INDEX IF NOT EXISTS uq_refunds_live_payment
    ON refunds (payment_id)
    WHERE status <> 'FAILED';

-- driver_earnings is a ledger written by the completion path. Crediting is
-- retried on ambiguous gateway responses, so the booking is the idempotency key.
CREATE UNIQUE INDEX IF NOT EXISTS uq_driver_earnings_booking
    ON driver_earnings (booking_id);

-- ---------------------------------------------------------------------------
-- Gateway webhook idempotency
-- ---------------------------------------------------------------------------

-- Razorpay retries a webhook until it gets a 2xx, so the same event arrives
-- repeatedly. Recording the event id makes handling idempotent: the second
-- delivery conflicts here and the handler returns 200 without re-applying it.
CREATE TABLE IF NOT EXISTS gateway_events (
    id             TEXT PRIMARY KEY,
    provider       TEXT NOT NULL,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL DEFAULT '{}',
    processed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_gateway_events_processed
    ON gateway_events (processed_at DESC);

-- ---------------------------------------------------------------------------
-- Handover OTP throttling
-- ---------------------------------------------------------------------------

-- Counts handover-code guesses per booking. The code is 6 digits, so an
-- unthrottled verify endpoint is brute-forceable; this makes the budget durable
-- across restarts and across the multiple app instances behind a load balancer.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS otp_attempts INTEGER NOT NULL DEFAULT 0;

-- ---------------------------------------------------------------------------
-- Index coverage for hot queries
-- ---------------------------------------------------------------------------

-- The scheduled worker scans for due scheduled bookings on every tick; without
-- this it seq-scans bookings once per poll interval.
CREATE INDEX IF NOT EXISTS idx_bookings_scheduled_ready
    ON bookings (scheduled_at)
    WHERE status = 'REQUESTED' AND scheduled_at IS NOT NULL;

-- Supports ListSearchingWithoutLiveOffer, which filters on status then age.
CREATE INDEX IF NOT EXISTS idx_bookings_searching_age
    ON bookings (created_at)
    WHERE status = 'SEARCHING';

-- FindActiveForDriver is hit on every driver app foreground.
CREATE INDEX IF NOT EXISTS idx_bookings_active_driver_lookup
    ON bookings (driver_id)
    WHERE driver_id IS NOT NULL;

-- Offer expiry sweep: pending offers ordered by when they lapse.
CREATE INDEX IF NOT EXISTS idx_matching_offers_expiring
    ON matching_offers (expires_at)
    WHERE status = 'PENDING';

-- TripPoints downsamples with `id % 5 = 0`, which is unusable as an index
-- predicate. Sampling by time window over the recorded_at index below keeps the
-- polyline read bounded without a full scan.
CREATE INDEX IF NOT EXISTS idx_trip_locations_trip_time
    ON trip_locations (trip_id, recorded_at DESC);

CREATE INDEX IF NOT EXISTS idx_vehicle_inspections_trip
    ON vehicle_inspections (trip_id, created_at);

CREATE INDEX IF NOT EXISTS idx_ratings_to_user
    ON ratings (to_user, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_driver_earnings_driver
    ON driver_earnings (driver_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_driver_documents_driver
    ON driver_documents (driver_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_sos_open
    ON sos (created_at DESC)
    WHERE status <> 'RESOLVED';

CREATE INDEX IF NOT EXISTS idx_sos_user
    ON sos (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_emergency_contacts_user
    ON emergency_contacts (user_id);

CREATE INDEX IF NOT EXISTS idx_device_tokens_user
    ON device_tokens (user_id);

CREATE INDEX IF NOT EXISTS idx_notifications_unread
    ON notifications (user_id, created_at DESC)
    WHERE is_read = FALSE;

-- ---------------------------------------------------------------------------
-- Value constraints the services do not enforce
-- ---------------------------------------------------------------------------

-- These columns have no CHECK, so a malformed request reaches Postgres and is
-- reported as a 500 instead of a 400. Rejecting them here keeps bad input from
-- corrupting fare arithmetic.
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_estimated_price;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_estimated_price
    CHECK (estimated_price >= 0);

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_final_price;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_final_price
    CHECK (final_price IS NULL OR final_price >= 0);

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_duration;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_duration
    CHECK (duration_hours IS NULL OR (duration_hours > 0 AND duration_hours <= 24));

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_pickup_lat;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_pickup_lat
    CHECK (pickup_lat BETWEEN -90 AND 90);

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_pickup_lng;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_pickup_lng
    CHECK (pickup_lng BETWEEN -180 AND 180);

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_dest_lat;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_dest_lat
    CHECK (dest_lat IS NULL OR dest_lat BETWEEN -90 AND 90);

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_dest_lng;
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_dest_lng
    CHECK (dest_lng IS NULL OR dest_lng BETWEEN -180 AND 180);

ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_amount;
ALTER TABLE payments ADD CONSTRAINT chk_payments_amount
    CHECK (amount >= 0);

-- Fare components must sum to the total, otherwise a booking can be marked paid
-- for less than the driver is owed.
ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_breakdown;
ALTER TABLE payments ADD CONSTRAINT chk_payments_breakdown
    CHECK (amount = platform_fee + driver_amount + "authorization" + protection_fee + tax);

ALTER TABLE ratings DROP CONSTRAINT IF EXISTS chk_ratings_not_self;
ALTER TABLE ratings ADD CONSTRAINT chk_ratings_not_self
    CHECK (from_user <> to_user);

ALTER TABLE trips DROP CONSTRAINT IF EXISTS chk_trips_odometer;
ALTER TABLE trips ADD CONSTRAINT chk_trips_odometer
    CHECK (
        (start_odometer >= 0)
        AND (end_odometer IS NULL OR end_odometer >= start_odometer)
    );

ALTER TABLE trips DROP CONSTRAINT IF EXISTS chk_trips_fuel;
ALTER TABLE trips ADD CONSTRAINT chk_trips_fuel
    CHECK (
        (start_fuel BETWEEN 0 AND 100)
        AND (end_fuel IS NULL OR end_fuel BETWEEN 0 AND 100)
    );

ALTER TABLE vehicle_inspections DROP CONSTRAINT IF EXISTS chk_inspection_fuel;
ALTER TABLE vehicle_inspections ADD CONSTRAINT chk_inspection_fuel
    CHECK (fuel_level BETWEEN 0 AND 100);

ALTER TABLE vehicle_inspections DROP CONSTRAINT IF EXISTS chk_inspection_odometer;
ALTER TABLE vehicle_inspections ADD CONSTRAINT chk_inspection_odometer
    CHECK (odometer >= 0);

ALTER TABLE trip_locations DROP CONSTRAINT IF EXISTS chk_trip_location_lat;
ALTER TABLE trip_locations ADD CONSTRAINT chk_trip_location_lat
    CHECK (latitude BETWEEN -90 AND 90);

ALTER TABLE trip_locations DROP CONSTRAINT IF EXISTS chk_trip_location_lng;
ALTER TABLE trip_locations ADD CONSTRAINT chk_trip_location_lng
    CHECK (longitude BETWEEN -180 AND 180);

ALTER TABLE sos DROP CONSTRAINT IF EXISTS chk_sos_lat;
ALTER TABLE sos ADD CONSTRAINT chk_sos_lat CHECK (latitude BETWEEN -90 AND 90);

ALTER TABLE sos DROP CONSTRAINT IF EXISTS chk_sos_lng;
ALTER TABLE sos ADD CONSTRAINT chk_sos_lng CHECK (longitude BETWEEN -180 AND 180);

-- A driver document type outside the enum is rejected by the existing CHECK;
-- an empty document number is not, and it is the field reviewers key off.
ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS chk_driver_doc_number;
ALTER TABLE driver_documents ADD CONSTRAINT chk_driver_doc_number
    CHECK (length(btrim(document_number)) > 0);
