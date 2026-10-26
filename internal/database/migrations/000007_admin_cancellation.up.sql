-- Distinguish an administrative cancellation from a driver cancelling.
--
-- CANCELLED_BY_DRIVER is deliberately illegal once the customer has handed over
-- the vehicle, and it feeds the driver's cancellation rate. Neither is correct
-- when an administrator suspends a driver mid-ride: the cancellation is not the
-- driver's decision and must not count against them, but the booking still has
-- to reach a terminal state or the suspended driver keeps an open trip, can
-- complete it, and collects the fare.
--
-- With no administrative status available, the cleanup path silently skipped
-- every post-OTP booking, which is precisely the case an administrator suspending
-- a driver cares about.
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS bookings_status_check;

ALTER TABLE bookings ADD CONSTRAINT bookings_status_check
    CHECK (status IN (
        'REQUESTED', 'SEARCHING', 'DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE',
        'DRIVER_ARRIVED', 'OTP_VERIFIED', 'TRIP_STARTED', 'TRIP_PAUSED',
        'TRIP_RESUMED', 'TRIP_COMPLETED', 'CANCELLED', 'CANCELLED_BY_DRIVER',
        'CANCELLED_BY_ADMIN', 'DRIVER_NO_SHOW', 'PAYMENT_FAILED',
        'SUPPORT_ESCALATION', 'ACCIDENT'
    ));
