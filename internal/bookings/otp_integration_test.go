package bookings_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"drivethrough/internal/bookings"
	"drivethrough/internal/models"
	"drivethrough/internal/testsupport"
	"drivethrough/pkg/apperr"
)

// fixtures creates the user, driver and vehicle rows a booking depends on, so
// each test starts from a known, minimal graph.
type fixtures struct {
	customerID uuid.UUID
	driverUser uuid.UUID
	driverID   uuid.UUID
	vehicleID  uuid.UUID
	bookingID  uuid.UUID
}

func setup(t *testing.T) (*testsupport.DB, fixtures) {
	t.Helper()
	db := testsupport.NewDB(t)
	db.Truncate(t)

	var f fixtures

	f.customerID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Customer', $1, 'CUSTOMER') RETURNING id`,
		uniquePhone("cust"))
	f.driverUser = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Driver', $1, 'DRIVER') RETURNING id`,
		uniquePhone("drv"))
	f.vehicleID = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		f.customerID, uniqueReg("test"))
	// driverID is the profile row; bookings.driver_id and trips.driver_id
	// reference drivers(id), while a JWT carries the users(id) above.
	f.driverID = db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		f.driverUser)

	f.bookingID = db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'Pickup', 12.9716, 77.5946, 'DRIVER_ARRIVED') RETURNING id`,
		f.customerID, f.vehicleID)

	return db, f
}

func newService(t *testing.T, db *testsupport.DB) *bookings.Service {
	t.Helper()
	repo := bookings.NewRepository(db.Pool)
	return bookings.NewService(repo, nil, nil, nil, 4)
}

// TestOTPBruteForceIsBounded is the security regression test.
//
// The handover OTP is the only thing standing between a stranger with a booking
// id and someone else's vehicle, and the code space is small. Before the attempt
// counter existed, VerifyOTP accepted unlimited guesses.
func TestOTPBruteForceIsBounded(t *testing.T) {
	db, f := setup(t)
	svc := newService(t, db)
	ctx := context.Background()

	const code = "4821"
	if _, err := db.Pool.Exec(ctx, `UPDATE bookings SET otp_code = $1 WHERE id = $2`, code, f.bookingID); err != nil {
		t.Fatalf("seed otp: %v", err)
	}

	wrong := "0000"
	if wrong == code {
		wrong = "1111"
	}

	// Every guess before the cap must be a plain rejection, and the counter must
	// climb by exactly one each time.
	for i := 1; i <= bookings.MaxOTPAttempts; i++ {
		err := svc.VerifyOTP(ctx, f.bookingID, f.customerID, wrong)
		if err == nil {
			t.Fatalf("guess %d was accepted", i)
		}
		if !apperr.IsBadRequest(err) {
			t.Fatalf("guess %d: expected a bad request, got %v", i, err)
		}

		var attempts int
		if err := db.Pool.QueryRow(ctx,
			`SELECT otp_attempts FROM bookings WHERE id = $1`, f.bookingID).Scan(&attempts); err != nil {
			t.Fatalf("read attempts: %v", err)
		}
		if attempts != i {
			t.Fatalf("after %d guesses the counter is %d, want %d", i, attempts, i)
		}
	}

	// The budget is now spent, so even the correct code is refused. Without this
	// the throttle would only slow an attacker who kept guessing.
	err := svc.VerifyOTP(ctx, f.bookingID, f.customerID, code)
	if err == nil {
		t.Fatal("the correct code was accepted after exhausting the attempt budget")
	}
	if !apperr.IsForbidden(err) {
		t.Errorf("expected a forbidden error once the budget is spent, got %v", err)
	}
}

// TestOTPCorrectCodeIsAccepted confirms the throttle does not break the happy
// path, and that the counter records the successful guess.
func TestOTPCorrectCodeIsAccepted(t *testing.T) {
	db, f := setup(t)
	svc := newService(t, db)
	ctx := context.Background()

	const code = "7351"
	if _, err := db.Pool.Exec(ctx, `UPDATE bookings SET otp_code = $1 WHERE id = $2`, code, f.bookingID); err != nil {
		t.Fatalf("seed otp: %v", err)
	}

	if err := svc.VerifyOTP(ctx, f.bookingID, f.customerID, code); err != nil {
		t.Fatalf("the correct code was rejected: %v", err)
	}

	var attempts int
	if err := db.Pool.QueryRow(ctx,
		`SELECT otp_attempts FROM bookings WHERE id = $1`, f.bookingID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 1 {
		t.Errorf("otp_attempts = %d, want 1", attempts)
	}
}

// TestOTPRequiresCustomerOwnership covers the IDOR: the endpoint takes a booking
// id, so without the ownership check anyone signed in could try codes against
// somebody else's ride.
func TestOTPRequiresCustomerOwnership(t *testing.T) {
	db, f := setup(t)
	svc := newService(t, db)
	ctx := context.Background()

	attacker := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Attacker', $1, 'CUSTOMER') RETURNING id`,
		uniquePhone("att"))

	if _, err := db.Pool.Exec(ctx, `UPDATE bookings SET otp_code = '1234' WHERE id = $1`, f.bookingID); err != nil {
		t.Fatalf("seed otp: %v", err)
	}

	err := svc.VerifyOTP(ctx, f.bookingID, attacker, "1234")
	if err == nil {
		t.Fatal("a non-party was allowed to verify the OTP")
	}
	if !apperr.IsForbidden(err) {
		t.Errorf("expected forbidden, got %v", err)
	}

	// A rejected attempt must not consume the legitimate customer's budget.
	var attempts int
	if err := db.Pool.QueryRow(ctx,
		`SELECT otp_attempts FROM bookings WHERE id = $1`, f.bookingID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("a non-party consumed the attempt budget: otp_attempts = %d", attempts)
	}
}

// TestOTPIssueResetsBudget ensures a customer who burned their five guesses is
// not locked out of a freshly issued code.
func TestOTPIssueResetsBudget(t *testing.T) {
	db, f := setup(t)
	svc := newService(t, db)
	ctx := context.Background()

	if _, err := db.Pool.Exec(ctx,
		`UPDATE bookings SET otp_code = '1111', otp_attempts = 5 WHERE id = $1`, f.bookingID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := bookings.NewRepository(db.Pool)
	if err := repo.SetOTP(ctx, f.bookingID, "2222"); err != nil {
		t.Fatalf("issue otp: %v", err)
	}

	if err := svc.VerifyOTP(ctx, f.bookingID, f.customerID, "2222"); err != nil {
		t.Fatalf("the newly issued code was rejected: %v", err)
	}
}

// TestOTPRejectedOutsideArrivalWindow confirms a customer cannot confirm the
// handover before the driver has actually arrived.
func TestOTPRejectedOutsideArrivalWindow(t *testing.T) {
	db, f := setup(t)
	svc := newService(t, db)
	ctx := context.Background()

	if _, err := db.Pool.Exec(ctx, `UPDATE bookings SET otp_code = '1234' WHERE id = $1`, f.bookingID); err != nil {
		t.Fatalf("seed otp: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE bookings SET status = 'DRIVER_ASSIGNED' WHERE id = $1`, f.bookingID); err != nil {
		t.Fatalf("set status: %v", err)
	}

	err := svc.VerifyOTP(ctx, f.bookingID, f.customerID, "1234")
	if err == nil {
		t.Fatal("the OTP was accepted before the driver arrived")
	}
	if !apperr.IsConflict(err) {
		t.Errorf("expected a conflict, got %v", err)
	}
}

// TestStatusCompareAndSwapRejectsStaleWriter covers the race the CAS was added
// for: two readers observe the same status and both try to transition it. The
// second write must lose rather than overwrite the first.
func TestStatusCompareAndSwapRejectsStaleWriter(t *testing.T) {
	db, f := setup(t)
	repo := bookings.NewRepository(db.Pool)
	ctx := context.Background()

	// Two callers both read DRIVER_ARRIVED.
	stale := models.BookingArrived

	first := repo.UpdateStatus(ctx, f.bookingID, models.BookingOTPVerified, f.customerID, "first", stale)
	if first != nil {
		t.Fatalf("the first transition failed: %v", first)
	}

	// The second still believes the booking is DRIVER_ARRIVED and must be
	// rejected instead of clobbering the first.
	second := repo.UpdateStatus(ctx, f.bookingID, models.BookingStarted, f.customerID, "second", stale)
	if second == nil {
		t.Fatal("a stale compare-and-swap write was accepted")
	}
	if !apperr.IsConflict(second) {
		t.Errorf("expected a conflict for the stale write, got %v", second)
	}

	var status string
	if err := db.Pool.QueryRow(ctx,
		`SELECT status FROM bookings WHERE id = $1`, f.bookingID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(models.BookingOTPVerified) {
		t.Errorf("status = %q, want the first writer's %q", status, models.BookingOTPVerified)
	}
}

// TestStatusChangeRecordsAuditEvent verifies the transition and its audit row
// are written together, since the event is what support reconstructs a dispute
// from.
func TestStatusChangeRecordsAuditEvent(t *testing.T) {
	db, f := setup(t)
	repo := bookings.NewRepository(db.Pool)
	ctx := context.Background()

	if err := repo.UpdateStatus(ctx, f.bookingID, models.BookingOTPVerified, f.customerID, "handover", models.BookingArrived); err != nil {
		t.Fatalf("transition: %v", err)
	}

	var notes string
	var actor uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT notes, actor_id FROM booking_events WHERE booking_id = $1 AND status = $2`,
		f.bookingID, models.BookingOTPVerified).Scan(&notes, &actor); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if notes != "handover" {
		t.Errorf("notes = %q, want %q", notes, "handover")
	}
	if actor != f.customerID {
		t.Errorf("actor = %s, want the acting customer %s", actor, f.customerID)
	}
}

// TestSystemTransitionRecordsNullActor covers a system-driven transition. The
// actor is a users(id) foreign key, so uuid.Nil would violate it; the column has
// to be NULL instead.
func TestSystemTransitionRecordsNullActor(t *testing.T) {
	db, f := setup(t)
	repo := bookings.NewRepository(db.Pool)
	ctx := context.Background()

	if err := repo.UpdateStatus(ctx, f.bookingID, models.BookingOTPVerified, uuid.Nil, "system", models.BookingArrived); err != nil {
		t.Fatalf("transition: %v", err)
	}

	var actor *uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT actor_id FROM booking_events WHERE booking_id = $1`, f.bookingID).Scan(&actor); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if actor != nil {
		t.Errorf("actor_id = %v, want NULL for a system transition", *actor)
	}
}

var phoneSeq int

func uniquePhone(prefix string) string {
	phoneSeq++
	return "+9198" + itoa(10000000+phoneSeq) + uniqueSuffix()
}

var regSeq int

func uniqueReg(prefix string) string {
	regSeq++
	return "KA01AB" + itoa(1000+regSeq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func uniqueSuffix() string {
	return uuid.New().String()[:6]
}

// dbCtx is a shorthand for a test context. The harness uses t.Context() for its
// own calls; the concurrent tests need a plain context they can pass into
// goroutines without racing on t.
func dbCtx(t *testing.T) context.Context {
	t.Helper()
	return t.Context()
}
