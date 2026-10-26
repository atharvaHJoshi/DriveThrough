package payments_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"drivethrough/internal/config"
	"drivethrough/internal/payments"
	"drivethrough/internal/testsupport"
	"drivethrough/pkg/apperr"
)

// newService wires a payments service against a stub gateway so the HTTP calls
// can be observed and made to fail on demand.
type stubEnv struct {
	db      *testsupport.DB
	svc     *payments.Service
	calls   []string
	failOn  string
	failing *stubServer
}

type stubServer struct {
	*httptest.Server
	calls  []string
	failOn string
}

func (s *stubServer) handler(w http.ResponseWriter, r *http.Request) {
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	if s.failOn != "" && r.URL.Path == s.failOn {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"pay_stub_1","status":"captured"}`))
}

func setupGateway(t *testing.T) (*stubServer, func()) {
	stub := &stubServer{}
	stub.Server = httptest.NewServer(http.HandlerFunc(stub.handler))
	return stub, stub.Close
}

func (e *stubEnv) callsTo(path string) int {
	n := 0
	for _, c := range e.calls {
		if c == "POST "+path {
			n++
		}
	}
	return n
}

// seedPayment creates a payment row in the given status for a fresh booking.
func seedPayment(t *testing.T, db *testsupport.DB, status string) (bookingID, paymentID testsupport.UUID) {
	t.Helper()
	customer := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	vehicle := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customer, uniqReg())
	bookingID = db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 'DRIVER_NO_SHOW') RETURNING id`,
		customer, vehicle)
	paymentID = db.UUID(t,
		// chk_payments_breakdown requires the components to sum to the total.
		`INSERT INTO payments (booking_id, customer_id, amount, driver_amount, status, gateway_reference)
		 VALUES ($1, $2, 500, 500, $3, 'pay_authorized_1') RETURNING id`,
		bookingID, customer, status)
	return bookingID, paymentID
}

func newService(t *testing.T, db *testsupport.DB, stub *stubServer) *payments.Service {
	t.Helper()
	client := payments.NewRazorpayClient(config.RazorpayConfig{
		BaseURL: stub.URL, KeyID: "key_id", KeySecret: "key_secret",
	}, stub.Client())
	repo := payments.NewRepository(db.Pool)
	return payments.NewService(repo, client, nil, nil, nil)
}

// TestReleasingAnAuthorizationVoidsItAtTheGateway is the money-path invariant for
// every cancellation and no-show.
//
// No cancellation captures a payment, so the customer is never charged — but the
// authorization is a hold on their funds, and leaving it in place keeps the money
// reserved until the issuer expires it, sometimes for days. The record also sat
// in AUTHORIZED, which reads as "still owed" to both support and the customer.
func TestReleasingAnAuthorizationVoidsItAtTheGateway(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	stub, closeStub := setupGateway(t)
	defer closeStub()

	bookingID, paymentID := seedPayment(t, db, "AUTHORIZED")
	svc := newService(t, db, stub)

	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Fatalf("release: %v", err)
	}

	if got := callsTo(stub.calls, "/payments/pay_authorized_1/void"); got != 1 {
		t.Errorf("gateway void calls = %d, want exactly 1 (calls: %v)", got, stub.calls)
	}

	var status string
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&status); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if status != "VOIDED" {
		t.Errorf("payment status = %q, want VOIDED", status)
	}
}

// TestAReleasedPaymentIsNotVoidedTwice keeps a repeated cleanup — a cancellation
// that is retried, or both a no-show sweep and a manual cancel firing — from
// double-calling the gateway, which the gateway rejects and which would surface
// as a spurious error.
func TestAReleasedPaymentIsNotVoidedTwice(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	stub, closeStub := setupGateway(t)
	defer closeStub()

	bookingID, _ := seedPayment(t, db, "AUTHORIZED")
	svc := newService(t, db, stub)

	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Fatalf("second release: %v", err)
	}

	if got := callsTo(stub.calls, "/payments/pay_authorized_1/void"); got != 1 {
		t.Errorf("gateway void calls = %d, want 1 across two attempts (calls: %v)", got, stub.calls)
	}
}

// TestACapturedPaymentIsNeverVoided is the important negative case. Once money
// has moved, a cancellation must not quietly hand it back: that is a refund, and
// it is a separate, audited decision.
func TestACapturedPaymentIsNeverVoided(t *testing.T) {
	for _, status := range []string{"CAPTURED", "REFUNDED", "SETTLED", "FAILED", "VOIDED"} {
		t.Run(status, func(t *testing.T) {
			db := testsupport.NewDB(t)
			db.Truncate(t)
			stub, closeStub := setupGateway(t)
			defer closeStub()

			bookingID, paymentID := seedPayment(t, db, status)
			svc := newService(t, db, stub)

			if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
				t.Fatalf("release: %v", err)
			}

			if len(stub.calls) != 0 {
				t.Errorf("a %s payment triggered gateway calls: %v", status, stub.calls)
			}

			var after string
			if err := db.Pool.QueryRow(t.Context(),
				`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&after); err != nil {
				t.Fatalf("read payment: %v", err)
			}
			if after != status {
				t.Errorf("a %s payment was rewritten to %s", status, after)
			}
		})
	}
}

// TestAnUnauthorisedPaymentNeedsNoGatewayCall avoids a pointless round trip: an
// order that was created but never authorised holds no funds.
func TestAnUnauthorisedPaymentNeedsNoGatewayCall(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	stub, closeStub := setupGateway(t)
	defer closeStub()

	bookingID, paymentID := seedPayment(t, db, "INITIATED")
	svc := newService(t, db, stub)

	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("an unauthorised payment triggered gateway calls: %v", stub.calls)
	}

	var status string
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&status); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if status != "INITIATED" {
		t.Errorf("status = %q, want INITIATED left alone: no funds were ever held", status)
	}
}

// TestAFailedVoidIsReportedRatherThanSwallowed protects the record's integrity.
// Marking the payment VOIDED when the gateway refused would claim the customer's
// money is back when it is still held at the gateway.
func TestAFailedVoidIsReportedRatherThanSwallowed(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	stub, closeStub := setupGateway(t)
	defer closeStub()
	stub.failOn = "/payments/pay_authorized_1/void"

	bookingID, paymentID := seedPayment(t, db, "AUTHORIZED")
	svc := newService(t, db, stub)

	err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID)
	if err == nil {
		t.Fatal("a rejected void was reported as a success")
	}
	if !apperr.Is(err, 502) {
		t.Errorf("expected a 502 so the failure is retried rather than swallowed, got %v", err)
	}

	var status string
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&status); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if status != "AUTHORIZED" {
		t.Errorf("status = %q, want AUTHORIZED retained so the hold stays visible and retryable", status)
	}
}

// TestReleasingAnUnpaidBookingIsNotAnError keeps the cleanup safe to call
// unconditionally. Most cancellations never involved a payment at all.
func TestReleasingAnUnpaidBookingIsNotAnError(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	stub, closeStub := setupGateway(t)
	defer closeStub()

	customer := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	vehicle := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customer, uniqReg())
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 'CANCELLED') RETURNING id`,
		customer, vehicle)

	svc := newService(t, db, stub)
	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Errorf("a booking with no payment returned %v, want nil", err)
	}
}

func callsTo(calls []string, path string) int {
	n := 0
	for _, c := range calls {
		if c == "POST "+path {
			n++
		}
	}
	return n
}

func uniqPhone() string { seq++; return "+9177" + padStr(seq) }
func uniqReg() string   { seq++; return "KA11WW" + padStr(1000+seq) }

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
