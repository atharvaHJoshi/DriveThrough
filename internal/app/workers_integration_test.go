package app_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/bookings"
	"drivethrough/internal/models"
	"drivethrough/internal/testsupport"
)

// TestNoShowSweepSelectionFindsOnlyAbandonedArrivals covers the query that
// decides which bookings the sweep escalates.
//
// A false positive here is expensive: a customer is told their driver did not
// show and a driver is struck for a no-show they did not commit. So the query
// must only return bookings that sat in DRIVER_ARRIVED with no trip ever
// started, and must ignore anything that progressed.
func TestNoShowSweepSelectionFindsOnlyAbandonedArrivals(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := newBookingRepo(db)

	customerID := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Customer', $1, 'CUSTOMER') RETURNING id`,
		phone(t, "cust"))
	vehicleID := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customerID, reg(t))

	// uq_bookings_active_driver allows a driver only one active booking, so each
	// scenario needs its own driver.
	newDriver := func() testsupport.UUID {
		uid := db.UUID(t,
			`INSERT INTO users (name, phone, role) VALUES ('Driver', $1, 'DRIVER') RETURNING id`,
			phone(t, "drv"))
		return db.UUID(t,
			`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
			uid)
	}

	// Abandoned: arrived, then left alone.
	driverID := newDriver()
	abandoned := db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status, updated_at)
		 VALUES ($1, $2, $3, 'HOURLY', 'A', 12.9, 77.6, 'DRIVER_ARRIVED', NOW() - INTERVAL '30 minutes') RETURNING id`,
		customerID, driverID, vehicleID)

	// Arrived but the driver actually started the trip: must not be swept.
	withTripDriver := newDriver()
	withTrip := db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status, updated_at)
		 VALUES ($1, $2, $3, 'HOURLY', 'B', 12.9, 77.6, 'DRIVER_ARRIVED', NOW() - INTERVAL '30 minutes') RETURNING id`,
		customerID, withTripDriver, vehicleID)
	db.Exec(t, `INSERT INTO trips (booking_id, driver_id, vehicle_id, status) VALUES ($1, $2, $3, 'ACTIVE')`,
		withTrip, withTripDriver, vehicleID)

	// Arrived too recently: still within the grace period.
	freshDriver := newDriver()
	fresh := db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status, updated_at)
		 VALUES ($1, $2, $3, 'HOURLY', 'C', 12.9, 77.6, 'DRIVER_ARRIVED', NOW() - INTERVAL '2 minutes') RETURNING id`,
		customerID, freshDriver, vehicleID)

	// Not arrived at all.
	enrouteDriver := newDriver()
	enroute := db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status, updated_at)
		 VALUES ($1, $2, $3, 'HOURLY', 'D', 12.9, 77.6, 'DRIVER_EN_ROUTE', NOW() - INTERVAL '30 minutes') RETURNING id`,
		customerID, enrouteDriver, vehicleID)

	found, err := repo.ListStaleArrived(t.Context(), 10*time.Minute, 50)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	got := map[string]bool{}
	for _, b := range found {
		got[b.ID.String()] = true
	}

	if !got[abandoned.String()] {
		t.Error("the genuinely abandoned arrival was not selected")
	}
	for _, b := range []struct {
		name string
		id   testsupport.UUID
	}{
		{"a booking whose trip started", withTrip},
		{"an arrival still inside the grace period", fresh},
		{"a booking that never arrived", enroute},
	} {
		if got[b.id.String()] {
			t.Errorf("%s must not be swept for no-show", b.name)
		}
	}
}

// TestConcurrentTerminalTransitionsElectOneWinner asserts that when a sweep and
// a driver race to close the same booking, exactly one transition is recorded.
//
// The sweep's compare-and-swap is the guard: if it did not exist, both would
// write and the audit trail would show two contradictory terminal events.
func TestConcurrentTerminalTransitionsElectOneWinner(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := newBookingRepo(db)

	customerID := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Customer', $1, 'CUSTOMER') RETURNING id`,
		phone(t, "cust"))
	driverUser := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Driver', $1, 'DRIVER') RETURNING id`,
		phone(t, "drv"))
	driverID := db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		driverUser)
	vehicleID := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customerID, reg(t))

	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, driver_id, vehicle_id, booking_type, pickup_location,
		                       pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, $3, 'HOURLY', 'A', 12.9, 77.6, 'DRIVER_ARRIVED') RETURNING id`,
		customerID, driverID, vehicleID)

	// Both racers read DRIVER_ARRIVED first, exactly as the sweep and the
	// driver's own request would.
	racers := []struct {
		name   string
		target models.BookingStatus
		actor  testsupport.UUID
	}{
		{"sweep", models.BookingDriverNoShow, testsupport.UUID{}},
		{"driver", models.BookingDriverNoShow, driverUser},
		{"support", models.BookingDriverNoShow, customerID},
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   int
		start = make(chan struct{})
	)

	for _, r := range racers {
		wg.Add(1)
		go func(racer struct {
			name   string
			target models.BookingStatus
			actor  testsupport.UUID
		}) {
			defer wg.Done()
			<-start
			// Each racer asserts the status it believes it observed.
			err := repo.UpdateStatus(t.Context(), bookingID, racer.target, racer.actor,
				"racer: "+racer.name, models.BookingArrived)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
			}
		}(r)
	}
	close(start)
	wg.Wait()

	if won != 1 {
		t.Errorf("winners = %d, want exactly 1", won)
	}

	var events int
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM booking_events WHERE booking_id = $1 AND status = 'DRIVER_NO_SHOW'`,
		bookingID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("no-show events = %d, want exactly 1", events)
	}
}

func newBookingRepo(db *testsupport.DB) *bookings.Repository { return bookings.NewRepository(db.Pool) }

func phone(t *testing.T, tag string) string {
	t.Helper()
	phoneSeq++
	return "+9190" + pad(phoneSeq) + uuid.New().String()[:4]
}

func reg(t *testing.T) string {
	t.Helper()
	regSeq++
	return "KA07QR" + pad(1000+regSeq)
}

func pad(n int) string {
	s := "0000"
	d := ""
	for n > 0 {
		d = string(rune('0'+n%10)) + d
		n /= 10
	}
	return s[len(d):] + d
}

var (
	phoneSeq int
	regSeq   int
)
