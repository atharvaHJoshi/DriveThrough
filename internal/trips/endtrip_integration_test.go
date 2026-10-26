package trips_test

import (
	"testing"

	"drivethrough/internal/bookings"
	"drivethrough/internal/config"
	"drivethrough/internal/drivers"
	"drivethrough/internal/models"
	"drivethrough/internal/trips"
	"drivethrough/pkg/apperr"
)

func newService(t *testing.T, e *env) *trips.Service {
	t.Helper()
	repo := trips.NewRepository(e.db.Pool)
	driversSvc := drivers.NewService(drivers.NewRepository(e.db.Pool), nil, nil)
	bookingsSvc := bookings.NewService(
		bookings.NewRepository(e.db.Pool),
		bookings.NewPricingService(config.PricingConfig{
			BaseDriverFeePerHour: 200, PlatformFee: 50, ProtectionFee: 20,
		}),
		nil, driversSvc, 4,
	)
	return trips.NewService(repo, bookingsSvc, driversSvc, nil, 200)
}

// TestEndTripRecoversAfterAPartialFailure is the regression test for the
// two-write completion sequence.
//
// Completing a trip is two separate writes: the trip is closed, then the booking
// is marked COMPLETED. A failure between them — a transient database error, a
// dropped connection — left the trip closed and the booking open, and no
// endpoint could finish the job. The retry hit the "trip is not open" guard and
// was rejected as a conflict, so the customer was charged nothing, the driver
// had no completed ride on record, and the booking sat in a non-terminal state
// forever. The retry has to complete the booking when the trip is already shut.
func TestEndTripRecoversAfterAPartialFailure(t *testing.T) {
	e := setup(t)
	s := newService(t, e)

	// Set up a trip that is already closed while its booking is still open, which
	// is exactly the state a failed first attempt leaves behind.
	trip := &models.Trip{
		BookingID:     e.bookingID,
		DriverID:      e.driverID,
		VehicleID:     e.vehicleID,
		StartOdometer: 1000,
		StartFuel:     80,
	}
	if err := trips.NewRepository(e.db.Pool).CreateTripWithInspection(t.Context(), trip,
		&models.VehicleInspection{Type: "PRE_TRIP", Odometer: 1000, FuelLevel: 80}); err != nil {
		t.Fatalf("create trip: %v", err)
	}

	e.db.Exec(t, `UPDATE bookings SET status = 'TRIP_STARTED' WHERE id = $1`, e.bookingID)
	e.db.Exec(t, `UPDATE trips SET status = 'COMPLETED', ended_at = NOW(), end_odometer = 1200,
	                                 end_fuel = 60, total_distance = 15000 WHERE id = $1`, trip.ID)

	// The booking is still open, which is the precondition for the retry.
	var status string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, e.bookingID).Scan(&status); err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if status == "TRIP_COMPLETED" {
		t.Fatal("test setup is wrong: the booking is already completed")
	}

	res, err := s.EndTrip(t.Context(), trips.EndInput{
		TripID:      trip.ID,
		DriverID:    e.driverID,
		ActorID:     e.driverUser,
		EndOdometer: 1200,
		EndFuel:     60,
	})
	if err != nil {
		t.Fatalf("the retry could not finish a partially completed trip: %v", err)
	}
	if res.FinalPrice <= 0 {
		t.Errorf("final price = %d, want a settled amount", res.FinalPrice)
	}

	// The recorded distance from the first attempt must be reused, not
	// recomputed, or a retry could settle a different amount.
	if res.Distance != 15 {
		t.Errorf("distance = %v km, want the 15 km recorded by the first attempt", res.Distance)
	}

	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, e.bookingID).Scan(&status); err != nil {
		t.Fatalf("read booking after: %v", err)
	}
	if status != "TRIP_COMPLETED" {
		t.Errorf("booking status = %q, want COMPLETED after the retry", status)
	}
}

// TestEndTripRejectsASecondCompletionOnceTheBookingIsDone keeps the recovery path
// from turning into a way to re-settle a finished ride.
func TestEndTripRejectsASecondCompletionOnceTheBookingIsDone(t *testing.T) {
	e := setup(t)
	s := newService(t, e)

	trip := &models.Trip{
		BookingID:     e.bookingID,
		DriverID:      e.driverID,
		VehicleID:     e.vehicleID,
		StartOdometer: 1000,
		StartFuel:     80,
	}
	repo := trips.NewRepository(e.db.Pool)
	if err := repo.CreateTripWithInspection(t.Context(), trip,
		&models.VehicleInspection{Type: "PRE_TRIP", Odometer: 1000, FuelLevel: 80}); err != nil {
		t.Fatalf("create trip: %v", err)
	}
	e.db.Exec(t, `UPDATE bookings SET status = 'TRIP_STARTED' WHERE id = $1`, e.bookingID)
	e.db.Exec(t, `UPDATE trips SET status = 'COMPLETED', ended_at = NOW(), total_distance = 15000 WHERE id = $1`, trip.ID)

	// First call finishes the booking.
	if _, err := s.EndTrip(t.Context(), trips.EndInput{
		TripID: trip.ID, DriverID: e.driverID, ActorID: e.driverUser, EndOdometer: 1200, EndFuel: 60,
	}); err != nil {
		t.Fatalf("first completion: %v", err)
	}

	// A second call must be refused rather than settling again.
	_, err := s.EndTrip(t.Context(), trips.EndInput{
		TripID: trip.ID, DriverID: e.driverID, ActorID: e.driverUser, EndOdometer: 1200, EndFuel: 60,
	})
	if err == nil {
		t.Fatal("a completed booking was settled a second time")
	}
	if !apperr.IsConflict(err) {
		t.Errorf("expected a conflict, got %v", err)
	}
}

// TestEndTripRejectsAnotherDriver stops a bystander from closing someone else's
// ride, which would let them inject the end odometer and fuel readings that feed
// the fare.
func TestEndTripRejectsAnotherDriver(t *testing.T) {
	e := setup(t)
	s := newService(t, e)

	otherUser := e.db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Other', $1, 'DRIVER') RETURNING id`, uniqPhone())
	otherDriver := e.db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		otherUser)

	trip := &models.Trip{
		BookingID:     e.bookingID,
		DriverID:      e.driverID,
		VehicleID:     e.vehicleID,
		StartOdometer: 1000,
		StartFuel:     80,
	}
	if err := trips.NewRepository(e.db.Pool).CreateTripWithInspection(t.Context(), trip,
		&models.VehicleInspection{Type: "PRE_TRIP", Odometer: 1000, FuelLevel: 80}); err != nil {
		t.Fatalf("create trip: %v", err)
	}
	e.db.Exec(t, `UPDATE bookings SET status = 'TRIP_STARTED' WHERE id = $1`, e.bookingID)

	_, err := s.EndTrip(t.Context(), trips.EndInput{
		TripID: trip.ID, DriverID: otherDriver, ActorID: otherUser, EndOdometer: 99999, EndFuel: 1,
	})
	if err == nil {
		t.Fatal("a driver who was not on the trip closed it")
	}
	if !apperr.IsForbidden(err) {
		t.Errorf("expected forbidden, got %v", err)
	}
}
