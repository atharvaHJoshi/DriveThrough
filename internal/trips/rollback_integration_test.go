package trips_test

import (
	"testing"

	"drivethrough/internal/models"
	"drivethrough/internal/testsupport"
	"drivethrough/internal/trips"
	"drivethrough/pkg/apperr"
)

type env struct {
	db         *testsupport.DB
	customerID testsupport.UUID
	driverUser testsupport.UUID
	driverID   testsupport.UUID
	vehicleID  testsupport.UUID
	bookingID  testsupport.UUID
}

func setup(t *testing.T) *env {
	t.Helper()
	db := testsupport.NewDB(t)
	db.Truncate(t)

	e := &env{db: db}
	e.customerID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`,
		uniqPhone())
	e.driverUser = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Drv', $1, 'DRIVER') RETURNING id`,
		uniqPhone())
	e.driverID = db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		e.driverUser)
	e.vehicleID = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		e.customerID, uniqReg())
	e.bookingID = db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, $3, 'HOURLY', 'A', 12.9, 77.6, 'OTP_VERIFIED') RETURNING id`,
		e.customerID, e.driverID, e.vehicleID)

	return e
}

// TestRollbackOfAFailedTripStartRemovesEverything is the regression test for the
// compensation path in StartTrip.
//
// The trip and its pre-trip inspection are created first, and the booking status
// is advanced second. If that second step fails, the service deletes the trip to
// leave no trace. The delete used to violate the vehicle_inspections foreign key
// because the column had no ON DELETE CASCADE, so the rollback did nothing: an
// ACTIVE trip row survived with no booking behind it, and the duplicate-start
// guard then refused every future attempt on that booking forever.
func TestRollbackOfAFailedTripStartRemovesEverything(t *testing.T) {
	e := setup(t)
	repo := trips.NewRepository(e.db.Pool)

	trip := &models.Trip{
		BookingID:     e.bookingID,
		DriverID:      e.driverID,
		VehicleID:     e.vehicleID,
		StartOdometer: 1000,
		StartFuel:     80,
	}
	inspection := &models.VehicleInspection{Type: "PRE_TRIP", Odometer: 1000, FuelLevel: 80}

	if err := repo.CreateTripWithInspection(t.Context(), trip, inspection); err != nil {
		t.Fatalf("create trip: %v", err)
	}

	// The inspection really is attached, which is what used to block the delete.
	var inspections int
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM vehicle_inspections WHERE trip_id = $1`, trip.ID).Scan(&inspections); err != nil {
		t.Fatalf("count inspections: %v", err)
	}
	if inspections != 1 {
		t.Fatalf("inspections = %d, want 1 for the rollback to be meaningful", inspections)
	}

	if err := repo.DeleteTrip(t.Context(), trip.ID); err != nil {
		t.Fatalf("the compensating delete failed, so a failed StartTrip would orphan a trip: %v", err)
	}

	var tripsLeft, inspectionsLeft, pointsLeft int
	if err := e.db.Pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM trips WHERE id = $1`, trip.ID).Scan(&tripsLeft); err != nil {
		t.Fatalf("count trips: %v", err)
	}
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM vehicle_inspections WHERE trip_id = $1`, trip.ID).Scan(&inspectionsLeft); err != nil {
		t.Fatalf("count inspections: %v", err)
	}
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM trip_locations WHERE trip_id = $1`, trip.ID).Scan(&pointsLeft); err != nil {
		t.Fatalf("count points: %v", err)
	}

	if tripsLeft != 0 {
		t.Errorf("the trip survived the rollback")
	}
	if inspectionsLeft != 0 {
		t.Errorf("%d inspection rows survived the rollback", inspectionsLeft)
	}
	if pointsLeft != 0 {
		t.Errorf("%d location points survived the rollback", pointsLeft)
	}
}

// TestAnEmergencyRecordBlocksTripDeletion pins the deliberate exception to the
// cascade added by 000006.
//
// vehicle_inspections and trip_locations cascade, because they describe the trip
// and are worthless without it. An SOS or an incident is the opposite: it is the
// evidence that something went wrong on a specific ride, so the foreign key is
// left as RESTRICT. The consequence is that a trip carrying an emergency record
// cannot be deleted at all, which is the point — an operator cannot quietly
// remove the trip a live SOS points at and leave the alert dangling.
//
// The rollback path is unaffected because a trip that failed to start cannot
// already have an SOS raised against it.
func TestAnEmergencyRecordBlocksTripDeletion(t *testing.T) {
	e := setup(t)
	repo := trips.NewRepository(e.db.Pool)

	trip := &models.Trip{
		BookingID:     e.bookingID,
		DriverID:      e.driverID,
		VehicleID:     e.vehicleID,
		StartOdometer: 1000,
		StartFuel:     80,
	}
	if err := repo.CreateTripWithInspection(t.Context(), trip, &models.VehicleInspection{
		Type: "PRE_TRIP", Odometer: 1000, FuelLevel: 80,
	}); err != nil {
		t.Fatalf("create trip: %v", err)
	}

	sosID := e.db.UUID(t,
		`INSERT INTO sos (user_id, trip_id, latitude, longitude) VALUES ($1, $2, 12.9, 77.6) RETURNING id`,
		e.customerID, trip.ID)

	err := repo.DeleteTrip(t.Context(), trip.ID)
	if err == nil {
		t.Fatal("a trip referenced by an SOS was deleted, orphaning the emergency record")
	}
	// The caller must be told what went wrong, not handed a raw foreign key
	// violation that surfaces as an unexplained 500.
	if !apperr.IsConflict(err) {
		t.Errorf("expected a conflict explaining the reference, got %v", err)
	}

	var count int
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM sos WHERE id = $1`, sosID).Scan(&count); err != nil {
		t.Fatalf("count sos: %v", err)
	}
	if count != 1 {
		t.Error("the SOS record was lost")
	}
}

func uniqPhone() string { seq++; return "+9155" + padStr(seq) }
func uniqReg() string   { seq++; return "KA08TT" + padStr(1000+seq) }

func padStr(n int) string {
	d := ""
	for n > 0 {
		d = string(rune('0'+n%10)) + d
		n /= 10
	}
	for len(d) < 4 {
		d = "0" + d
	}
	return d
}

var seq int
