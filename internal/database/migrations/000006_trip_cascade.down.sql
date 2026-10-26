-- Restore the original restrictive foreign keys.
--
-- Only safe to run when no trip has dependent rows that rely on the cascade;
-- on a populated database the ALTER back will fail if any trip was removed
-- while the cascade was in place, which is the intended signal.
ALTER TABLE vehicle_inspections
    DROP CONSTRAINT IF EXISTS vehicle_inspections_trip_id_fkey;

ALTER TABLE vehicle_inspections
    ADD CONSTRAINT vehicle_inspections_trip_id_fkey
    FOREIGN KEY (trip_id) REFERENCES trips(id);

ALTER TABLE trip_locations
    DROP CONSTRAINT IF EXISTS trip_locations_trip_id_fkey;

ALTER TABLE trip_locations
    ADD CONSTRAINT trip_locations_trip_id_fkey
    FOREIGN KEY (trip_id) REFERENCES trips(id);
