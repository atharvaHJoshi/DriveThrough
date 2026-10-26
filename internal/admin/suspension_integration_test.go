package admin_test

import (
	"testing"

	"drivethrough/internal/admin"
	"drivethrough/internal/bookings"
	"drivethrough/internal/config"
	"drivethrough/internal/drivers"
	"drivethrough/internal/incidents"
	"drivethrough/internal/sms"
	"drivethrough/internal/testsupport"
	"drivethrough/internal/users"
	"drivethrough/pkg/apperr"
)

type env struct {
	db         *testsupport.DB
	adminSvc   *admin.Service
	bookingSvc *bookings.Service
	driverSvc  *drivers.Service
	customerID testsupport.UUID
	driverUser testsupport.UUID
	driverID   testsupport.UUID
	vehicleID  testsupport.UUID
}

func setup(t *testing.T) *env {
	t.Helper()
	db := testsupport.NewDB(t)
	db.Truncate(t)

	driversRepo := drivers.NewRepository(db.Pool)
	usersRepo := users.NewRepository(db.Pool)
	driverSvc := drivers.NewService(driversRepo, usersRepo, nil)
	bookingsSvc := bookings.NewService(
		bookings.NewRepository(db.Pool),
		bookings.NewPricingService(config.PricingConfig{BaseDriverFeePerHour: 200}),
		nil, driverSvc, 4,
	)

	incidentsSvc := incidents.NewService(incidents.NewRepository(db.Pool), bookingsSvc, nil,
		sms.NewSender(sms.Config{Provider: "log"}, nil))

	e := &env{
		db:         db,
		adminSvc:   admin.NewService(admin.NewRepository(db.Pool), driverSvc, incidentsSvc, bookingsSvc),
		bookingSvc: bookingsSvc,
		driverSvc:  driverSvc,
	}

	e.customerID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	e.driverUser = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Drv', $1, 'DRIVER') RETURNING id`, uniqPhone())
	e.driverID = db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		e.driverUser)
	e.vehicleID = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		e.customerID, uniqReg())

	return e
}

func (e *env) booking(t *testing.T, status string) testsupport.UUID {
	t.Helper()
	return e.db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, $3, 'HOURLY', 'A', 12.9, 77.6, $4) RETURNING id`,
		e.customerID, e.driverID, e.vehicleID, status)
}

// TestSuspensionTakesTheDriverOffline is the core invariant. A suspended driver
// must not remain ONLINE.
//
// The cleanup used to call SetAvailability(false), which deliberately refuses to
// act on a driver who is no longer APPROVED, and the resulting error was
// discarded. So the driver stayed ONLINE in the database, kept streaming their
// location, and could still accept an offer that was already in flight.
func TestSuspensionTakesTheDriverOffline(t *testing.T) {
	e := setup(t)

	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	var status, verification string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status, verification_status FROM drivers WHERE id = $1`, e.driverID).
		Scan(&status, &verification); err != nil {
		t.Fatalf("read driver: %v", err)
	}

	if verification != "SUSPENDED" {
		t.Errorf("verification = %q, want SUSPENDED", verification)
	}
	if status != "OFFLINE" {
		t.Errorf("driver status = %q, want OFFLINE", status)
	}
}

// TestSuspensionCancelsABookingThatIsAlreadyUnderway covers the case the cleanup
// used to silently skip.
//
// CANCELLED_BY_DRIVER is illegal once the vehicle has been handed over, so a
// suspended driver past the OTP kept their booking, the trip kept running, and
// the driver could still complete it and collect the fare — the exact outcome
// the suspension is meant to prevent.
func TestSuspensionCancelsABookingThatIsAlreadyUnderway(t *testing.T) {
	e := setup(t)
	bookingID := e.booking(t, "OTP_VERIFIED")

	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	var status string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&status); err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if status != "CANCELLED_BY_ADMIN" {
		t.Errorf("booking status = %q, want CANCELLED_BY_ADMIN", status)
	}
}

// TestSuspensionDoesNotCountAgainstTheDriver is the reason the administrative
// status is separate: a revocation is not the driver's decision.
func TestSuspensionDoesNotCountAgainstTheDriver(t *testing.T) {
	e := setup(t)
	e.booking(t, "DRIVER_ASSIGNED")

	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	var cancellations int
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT total_cancellations FROM drivers WHERE id = $1`, e.driverID).Scan(&cancellations); err != nil {
		t.Fatalf("read cancellations: %v", err)
	}
	if cancellations != 0 {
		t.Errorf("total_cancellations = %d, want 0: an administrative suspension is not a driver cancellation", cancellations)
	}
}

// TestSuspensionClosesEveryOpenBookingAndLeavesSettledOnes confirms the sweep is
// exhaustive in the safe direction: everything open is closed, and anything
// already finished is left exactly as it was.
func TestSuspensionClosesEveryOpenBookingAndLeavesSettledOnes(t *testing.T) {
	e := setup(t)

	// uq_bookings_active_driver permits only one active booking per driver, so
	// the other scenarios use a second driver.
	secondUser := e.db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Drv2', $1, 'DRIVER') RETURNING id`, uniqPhone())
	secondDriver := e.db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		secondUser)
	secondCustomer := e.db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus2', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	secondVehicle := e.db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Honda', 'City', 2021, 'AUTOMATIC', 'PETROL', 'SEDAN') RETURNING id`,
		secondCustomer, uniqReg())

	// Already completed by the second driver: must survive untouched.
	settled := e.db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, $3, 'HOURLY', 'A', 12.9, 77.6, 'TRIP_COMPLETED') RETURNING id`,
		secondCustomer, secondDriver, secondVehicle)

	open := e.booking(t, "TRIP_STARTED")

	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	var openStatus, settledStatus string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, open).Scan(&openStatus); err != nil {
		t.Fatalf("read open booking: %v", err)
	}
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, settled).Scan(&settledStatus); err != nil {
		t.Fatalf("read settled booking: %v", err)
	}

	if openStatus != "CANCELLED_BY_ADMIN" {
		t.Errorf("open booking = %q, want CANCELLED_BY_ADMIN", openStatus)
	}
	if settledStatus != "TRIP_COMPLETED" {
		t.Errorf("a completed booking was rewritten to %q by an unrelated suspension", settledStatus)
	}
}

// TestReinstatementRestoresAPPROVEDWithoutSideEffects confirms the un-suspend
// path does not close anything, since there is nothing to clean up.
func TestReinstatementRestoresAPPROVEDWithoutSideEffects(t *testing.T) {
	e := setup(t)
	bookingID := e.booking(t, "DRIVER_ASSIGNED")

	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := e.adminSvc.SuspendDriver(t.Context(), e.driverID, false); err != nil {
		t.Fatalf("reinstate: %v", err)
	}

	var verification string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT verification_status FROM drivers WHERE id = $1`, e.driverID).Scan(&verification); err != nil {
		t.Fatalf("read driver: %v", err)
	}
	if verification != "APPROVED" {
		t.Errorf("verification = %q, want APPROVED", verification)
	}

	// The booking closed by the suspension stays closed; reinstating a driver
	// does not resurrect a customer's cancelled ride.
	var status string
	if err := e.db.Pool.QueryRow(t.Context(),
		`SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&status); err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if status != "CANCELLED_BY_ADMIN" {
		t.Errorf("booking = %q, want it to remain CANCELLED_BY_ADMIN", status)
	}
}

// TestSuspendUnknownDriverIsNotFound keeps the endpoint honest.
func TestSuspendUnknownDriverIsNotFound(t *testing.T) {
	e := setup(t)
	err := e.adminSvc.SuspendDriver(t.Context(), testsupport.UUID{}, true)
	if err == nil {
		t.Fatal("suspending a driver that does not exist succeeded")
	}
	if !apperr.IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}

func uniqPhone() string { seq++; return "+9166" + padStr(seq) }
func uniqReg() string   { seq++; return "KA09SS" + padStr(1000+seq) }

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
