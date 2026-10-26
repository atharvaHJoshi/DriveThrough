package bookings_test

import (
	"sync"
	"testing"

	"drivethrough/internal/bookings"
	"drivethrough/internal/testsupport"
	"drivethrough/pkg/apperr"
)

// TestAssignDriverIsExclusive is the concurrency invariant the whole marketplace
// rests on: one booking, one driver.
//
// Every candidate is offered the same booking and they all tap accept at once.
// Each driver claims their own offer successfully — offers are per driver, so a
// claim cannot arbitrate between them. The booking assignment is the single
// point where exactly one writer must win, and it is guarded on
// driver_id IS NULL.
func TestAssignDriverIsExclusive(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	customerID := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Customer', $1, 'CUSTOMER') RETURNING id`,
		uniquePhone("cust"))
	vehicleID := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customerID, uniqueReg("excl"))
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'Koramangala', 12.9352, 77.6245, 'SEARCHING') RETURNING id`,
		customerID, vehicleID)

	const contenders = 6
	driverIDs := make([]testsupport.UUID, 0, contenders)
	for i := 0; i < contenders; i++ {
		uid := db.UUID(t,
			`INSERT INTO users (name, phone, role) VALUES ($1, $2, 'DRIVER') RETURNING id`,
			uniquePhone("drv"), uniqueReg("cdrv"))
		driverIDs = append(driverIDs, db.UUID(t,
			`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
			uid))
	}

	repo := bookings.NewRepository(db.Pool)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []testsupport.UUID
		errors  []error
		start   = make(chan struct{})
	)

	for _, d := range driverIDs {
		wg.Add(1)
		go func(driverID testsupport.UUID) {
			defer wg.Done()
			<-start
			err := repo.AssignDriver(dbCtx(t), bookingID, driverID)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errors = append(errors, err)
				return
			}
			winners = append(winners, driverID)
		}(d)
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("winners = %d (%v), want exactly 1", len(winners), winners)
	}

	// The booking must point at the winner, and only the winner's assignment
	// event may have been written.
	var assigned testsupport.UUID
	if err := db.Pool.QueryRow(dbCtx(t),
		`SELECT driver_id FROM bookings WHERE id = $1`, bookingID).Scan(&assigned); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assigned != winners[0] {
		t.Errorf("booking is assigned to %s, want the winner %s", assigned, winners[0])
	}

	var events int
	if err := db.Pool.QueryRow(dbCtx(t),
		`SELECT COUNT(*) FROM booking_events WHERE booking_id = $1 AND status = 'DRIVER_ASSIGNED'`,
		bookingID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("assignment events = %d, want exactly 1", events)
	}

	// Losers must be told they lost the race, not handed a server error.
	for _, err := range errors {
		if !apperr.IsConflict(err) {
			t.Errorf("losing assignment returned %v, want a conflict", err)
		}
	}
}

// TestAssignDriverRejectsNonSearchingBooking covers the status guard: a booking
// that already moved on must not be handed to a late responder.
func TestAssignDriverRejectsNonSearchingBooking(t *testing.T) {
	db, f := setup(t)
	repo := bookings.NewRepository(db.Pool)

	db.Exec(t, `UPDATE bookings SET status = 'DRIVER_ASSIGNED' WHERE id = $1`, f.bookingID)

	err := repo.AssignDriver(dbCtx(t), f.bookingID, f.driverID)
	if err == nil {
		t.Fatal("a booking past SEARCHING was assigned")
	}
	if !apperr.IsConflict(err) {
		t.Errorf("expected a conflict, got %v", err)
	}
}

// TestAssignDriverIsIdempotentForTheSameDriver guards the retry path: a driver
// whose response timed out may re-send the accept, and that must not fail or
// create a second assignment event.
func TestAssignDriverIsIdempotentForTheSameDriver(t *testing.T) {
	db, f := setup(t)
	repo := bookings.NewRepository(db.Pool)

	db.Exec(t, `UPDATE bookings SET status = 'SEARCHING' WHERE id = $1`, f.bookingID)

	if err := repo.AssignDriver(dbCtx(t), f.bookingID, f.driverID); err != nil {
		t.Fatalf("first assignment: %v", err)
	}

	var assigned testsupport.UUID
	if err := db.Pool.QueryRow(dbCtx(t),
		`SELECT driver_id FROM bookings WHERE id = $1`, f.bookingID).Scan(&assigned); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assigned != f.driverID {
		t.Errorf("assigned to %s, want %s", assigned, f.driverID)
	}

	var events int
	if err := db.Pool.QueryRow(dbCtx(t),
		`SELECT COUNT(*) FROM booking_events WHERE booking_id = $1 AND status = 'DRIVER_ASSIGNED'`,
		f.bookingID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("assignment events = %d, want 1 after a single successful assignment", events)
	}
}
