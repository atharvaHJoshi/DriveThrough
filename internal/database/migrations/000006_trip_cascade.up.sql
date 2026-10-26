-- A trip owns its inspection records.
--
-- StartTrip creates the trip and its pre-trip inspection in one transaction,
-- then deletes the trip to compensate if the booking status update fails. Without
-- a cascade that delete always violated the foreign key, so the compensating
-- rollback silently failed and left an ACTIVE trip row behind. The customer was
-- then permanently unable to start a trip on that booking, because the
-- duplicate-start guard saw the orphaned row.
--
-- CASCADE is the correct semantic rather than a workaround: the inspection
-- describes a trip, and a trip that was rolled back never happened, so its
-- inspection is equally invalid. Incidents and SOS records deliberately do NOT
-- cascade, since an emergency report must survive the removal of the trip it
-- refers to.
ALTER TABLE vehicle_inspections
    DROP CONSTRAINT IF EXISTS vehicle_inspections_trip_id_fkey;

ALTER TABLE vehicle_inspections
    ADD CONSTRAINT vehicle_inspections_trip_id_fkey
    FOREIGN KEY (trip_id) REFERENCES trips(id) ON DELETE CASCADE;

-- The same reasoning applies to the location trail: a rolled-back trip must not
-- leave orphan points behind.
ALTER TABLE trip_locations
    DROP CONSTRAINT IF EXISTS trip_locations_trip_id_fkey;

ALTER TABLE trip_locations
    ADD CONSTRAINT trip_locations_trip_id_fkey
    FOREIGN KEY (trip_id) REFERENCES trips(id) ON DELETE CASCADE;
