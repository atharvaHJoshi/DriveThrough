-- 000003_booking_events_actor_nullable.up.sql
--
-- System-driven transitions (payment failure, search start, expiry sweeps) have
-- no human actor, and the callers pass uuid.Nil for them. With actor_id NOT NULL
-- plus the users(id) foreign key, every one of those transitions failed with a
-- constraint violation. A null actor is the correct representation of "this
-- happened on its own".

ALTER TABLE booking_events ALTER COLUMN actor_id DROP NOT NULL;
