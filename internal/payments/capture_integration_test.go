package payments_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"drivethrough/internal/config"
	"drivethrough/internal/drivers"
	"drivethrough/internal/models"
	"drivethrough/internal/payments"
	"drivethrough/internal/testsupport"
)

// The capture path is retried whenever the gateway redelivers an event, and the
// gateway is free to redeliver after our own failures. These tests pin the two
// properties that makes safe: the driver is credited exactly once, and a credit
// that was stranded by a partial failure is still delivered on retry.

// realDriverService wires the payments service to the real drivers repository, so
// the assertions below observe the actual ledger and counters rather than a stub
// that would merely agree with whatever the code did.
type realDriverService struct{ svc *drivers.Service }

func (r realDriverService) CreditEarning(ctx context.Context, driverID, bookingID uuid.UUID, amount int) error {
	return r.svc.CreditEarning(ctx, driverID, bookingID, amount)
}

func (r realDriverService) DriverUserID(ctx context.Context, driverID uuid.UUID) (uuid.UUID, error) {
	return r.svc.DriverUserID(ctx, driverID)
}

// bookingLookup resolves bookings from the database for the capture path.
type bookingLookup struct{ db *testsupport.DB }

func (b bookingLookup) GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error) {
	var booking models.Booking
	if err := b.db.Pool.QueryRow(ctx,
		`SELECT id, customer_id, driver_id, status FROM bookings WHERE id = $1`, id).
		Scan(&booking.ID, &booking.CustomerID, &booking.DriverID, &booking.Status); err != nil {
		return nil, err
	}
	return &booking, nil
}

func (b bookingLookup) UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error {
	_, err := b.db.Pool.Exec(ctx,
		`UPDATE bookings SET status = $2, updated_at = NOW() WHERE id = $1`, bookingID, status)
	return err
}

// capturedFixture returns a payment already marked CAPTURED, which is the state
// an attempt leaves behind when it fails after the status write but before the
// driver is credited.
type capturedFixture struct {
	driverID  uuid.UUID
	bookingID uuid.UUID
	paymentID uuid.UUID
}

func seedCapturedWithoutEarnings(t *testing.T, db *testsupport.DB) capturedFixture {
	t.Helper()
	customer := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	driverUser := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Drv', $1, 'DRIVER') RETURNING id`, uniqPhone())
	driverID := db.UUID(t,
		`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
		driverUser)
	vehicle := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customer, uniqReg())
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, driver_id, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, $3, 'DRIVER_ARRIVED') RETURNING id`,
		customer, vehicle, driverID)
	paymentID := db.UUID(t,
		`INSERT INTO payments (booking_id, customer_id, amount, driver_amount, status, gateway_reference)
		 VALUES ($1, $2, 500, 500, 'CAPTURED', 'pay_captured_1') RETURNING id`,
		bookingID, customer)
	return capturedFixture{driverID: driverID, bookingID: bookingID, paymentID: paymentID}
}

func newCaptureService(t *testing.T, db *testsupport.DB) *payments.Service {
	t.Helper()
	// A payment.captured event records what the gateway already did, so it never
	// calls the gateway itself. The client is wired to satisfy the constructor.
	client := payments.NewRazorpayClient(config.RazorpayConfig{
		BaseURL: "http://127.0.0.1:0", KeyID: "key_id", KeySecret: "key_secret",
	}, nil)
	return payments.NewService(
		payments.NewRepository(db.Pool),
		client,
		realDriverService{svc: drivers.NewService(drivers.NewRepository(db.Pool), nil, nil)},
		bookingLookup{db: db},
		nil,
	)
}

type driverCounters struct {
	earnings int
	trips    int
	ledger   int
}

func readCounters(t *testing.T, db *testsupport.DB, driverID, bookingID uuid.UUID) driverCounters {
	t.Helper()
	var c driverCounters
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT earnings_total, total_trips FROM drivers WHERE id = $1`, driverID).
		Scan(&c.earnings, &c.trips); err != nil {
		t.Fatalf("read driver counters: %v", err)
	}
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT COUNT(*) FROM driver_earnings WHERE booking_id = $1`, bookingID).
		Scan(&c.ledger); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return c
}

// TestARedeliveryCreditsADriverStrandedByAPartialFailure is the recovery
// invariant.
//
// An attempt can mark the payment CAPTURED and then fail before crediting the
// driver. The retry re-reads the payment, sees CAPTURED, and used to return
// immediately — leaving the driver permanently unpaid with no remaining trigger
// to credit them, because the status that said "done" was the very thing that
// suppressed the work.
func TestARedeliveryCreditsADriverStrandedByAPartialFailure(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	fx := seedCapturedWithoutEarnings(t, db)
	svc := newCaptureService(t, db)

	// The payment is already CAPTURED, so the status transition is a no-op here.
	if err := svc.ConfirmFromWebhook(t.Context(), payments.GatewayEvent{
		EventID:   "evt_stranded_1",
		Event:     "payment.captured",
		PaymentID: "pay_captured_1",
		Raw:       []byte(`{}`),
	}); err != nil {
		t.Fatalf("redelivery: %v", err)
	}

	got := readCounters(t, db, fx.driverID, fx.bookingID)
	if got.ledger != 1 {
		t.Fatalf("ledger rows = %d, want 1: the driver was never credited", got.ledger)
	}
	if got.earnings != 500 {
		t.Errorf("earnings_total = %d, want 500", got.earnings)
	}
	if got.trips != 1 {
		t.Errorf("total_trips = %d, want 1: the completed ride was never counted", got.trips)
	}
}

// TestRepeatedDeliveriesCreditTheDriverExactlyOnce keeps that recovery from
// turning into a double payment, and checks both layers of the guard.
//
// Redelivery under the same event id is stopped at the webhook inbox. The credit
// itself is a second, independent line of defence: it has to hold when the same
// capture arrives under a different event id, which happens whenever the gateway
// treats a redelivery as a new event or a payment is reported by two event
// types. That is the case a status check alone would not cover.
func TestRepeatedDeliveriesCreditTheDriverExactlyOnce(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	fx := seedCapturedWithoutEarnings(t, db)
	svc := newCaptureService(t, db)

	deliver := func(eventID string) error {
		return svc.ConfirmFromWebhook(t.Context(), payments.GatewayEvent{
			EventID:   eventID,
			Event:     "payment.captured",
			PaymentID: "pay_captured_1",
			Raw:       []byte(`{}`),
		})
	}

	if err := deliver("evt_repeat_1"); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	// Same event id again: refused by the inbox, and the handler treats that as
	// success, so it must not credit a second time.
	if err := deliver("evt_repeat_1"); err == nil {
		t.Error("redelivery of a completed event was not reported as a duplicate")
	}

	// A different event id for the same underlying capture gets past the inbox
	// and must still be absorbed by the booking-keyed credit.
	if err := deliver("evt_repeat_2"); err != nil {
		t.Fatalf("second event id: %v", err)
	}

	got := readCounters(t, db, fx.driverID, fx.bookingID)
	if got.ledger != 1 {
		t.Errorf("ledger rows = %d, want 1", got.ledger)
	}
	if got.earnings != 500 {
		t.Errorf("earnings_total = %d, want 500 (the driver was double-credited)", got.earnings)
	}
	if got.trips != 1 {
		t.Errorf("total_trips = %d, want 1 (the ride was double-counted)", got.trips)
	}
}
