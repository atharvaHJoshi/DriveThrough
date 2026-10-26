-- Remove the administrative cancellation status.
--
-- The inverse of the CHECK constraint. Any booking still holding
-- CANCELLED_BY_ADMIN will block this, which is the intended signal rather than
-- a silent rewrite of historical data.
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS bookings_status_check;

ALTER TABLE bookings ADD CONSTRAINT bookings_status_check
    CHECK (status IN (
        'REQUESTED', 'SEARCHING', 'DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE',
        'DRIVER_ARRIVED', 'OTP_VERIFIED', 'TRIP_STARTED', 'TRIP_PAUSED',
        'TRIP_RESUMED', 'TRIP_COMPLETED', 'CANCELLED', 'CANCELLED_BY_DRIVER',
        'DRIVER_NO_SHOW', 'PAYMENT_FAILED', 'SUPPORT_ESCALATION', 'ACCIDENT'
    ));
