-- 000003_booking_events_actor_nullable.down.sql -- Rollback
--
-- Restoring NOT NULL is only possible once every system-generated row is gone.
-- Attribute them to the booking's customer so no history is silently discarded.

UPDATE booking_events b
   SET actor_id = bk.customer_id
  FROM bookings bk
 WHERE bk.id = b.booking_id
   AND b.actor_id IS NULL;

DELETE FROM booking_events WHERE actor_id IS NULL;

ALTER TABLE booking_events ALTER COLUMN actor_id SET NOT NULL;
