package payments_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"drivethrough/internal/config"
	"drivethrough/internal/models"
	"drivethrough/internal/payments"
	"drivethrough/internal/testsupport"
)

// The order id and the payment id are different objects at Razorpay. Capture,
// void, and refund all address a payment, so they must be given the payment id.
// These tests pin that separation, because a single column holding both meant
// every one of those calls went to /payments/{order_id}/... and failed.

// recordingGateway captures the paths the service requests so a test can assert
// which identifier was used.
type recordingGateway struct {
	*httptest.Server
	paths []string
}

func newRecordingGateway(t *testing.T) *recordingGateway {
	t.Helper()
	g := &recordingGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.paths = append(g.paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rfnd_1","status":"processed"}`))
	}))
	t.Cleanup(g.Close)
	return g
}

func newServiceOn(t *testing.T, db *testsupport.DB, g *recordingGateway) *payments.Service {
	t.Helper()
	client := payments.NewRazorpayClient(config.RazorpayConfig{
		BaseURL: g.URL, KeyID: "key_id", KeySecret: "key_secret",
	}, g.Client())
	return payments.NewService(
		payments.NewRepository(db.Pool), client, nil, bookingLookup{db: db}, nil)
}

// seedOrderOnlyPayment creates a payment that knows only its order id, which is
// the state between creating an order and the gateway reporting a payment.
func seedOrderOnlyPayment(t *testing.T, db *testsupport.DB, status string) (bookingID, paymentID testsupport.UUID, orderID string) {
	t.Helper()
	customer := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	vehicle := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customer, uniqReg())
	orderID = "order_seed_" + uniqReg()
	bookingID = db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 'CANCELLED') RETURNING id`,
		customer, vehicle)
	paymentID = db.UUID(t,
		`INSERT INTO payments (booking_id, customer_id, amount, platform_fee, driver_amount, status, gateway_order_id)
		 VALUES ($1, $2, 500, 100, 400, $3, $4) RETURNING id`,
		bookingID, customer, status, orderID)
	return bookingID, paymentID, orderID
}

func readIdentifiers(t *testing.T, db *testsupport.DB, paymentID testsupport.UUID) (orderID, paymentRef string) {
	t.Helper()
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT COALESCE(gateway_order_id, ''), COALESCE(gateway_reference, '')
		 FROM payments WHERE id = $1`, paymentID).Scan(&orderID, &paymentRef); err != nil {
		t.Fatalf("read identifiers: %v", err)
	}
	return orderID, paymentRef
}

// TestInitiateStoresTheOrderIdInItsOwnColumn is the origin of the confusion: the
// order id must not be written into the column the gateway calls read.
func TestInitiateStoresTheOrderIdInItsOwnColumn(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	g := newRecordingGateway(t)
	svc := newServiceOn(t, db, g)

	customer := db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Cus', $1, 'CUSTOMER') RETURNING id`, uniqPhone())
	vehicle := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		customer, uniqReg())
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng,
			estimated_price, platform_fee, driver_fee, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 500, 100, 400, 'REQUESTED') RETURNING id`,
		customer, vehicle)

	res, err := svc.Initiate(t.Context(), bookingID, customer)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if res.OrderID == "" {
		t.Fatal("no order id returned to the client")
	}

	orderID, ref := readIdentifiers(t, db, res.Payment.ID)
	if orderID != res.OrderID {
		t.Errorf("gateway_order_id = %q, want the order id %q", orderID, res.OrderID)
	}
	if ref != "" {
		t.Errorf("gateway_reference = %q, want empty: it must not hold the order id", ref)
	}
}

// TestAWebhookPersistsThePaymentIdSoGatewayCallsAddressThePayment is the core
// invariant. A void is the cheapest way to observe it, because the call path
// carries the id straight into the URL.
func TestAWebhookPersistsThePaymentIdSoGatewayCallsAddressThePayment(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	g := newRecordingGateway(t)
	svc := newServiceOn(t, db, g)

	bookingID, paymentID, orderID := seedOrderOnlyPayment(t, db, "AUTHORIZED")

	// A failure event discloses the payment id without settling the funds, so the
	// authorisation hold is still there to be released afterwards.
	if err := svc.ConfirmFromWebhook(t.Context(), payments.GatewayEvent{
		EventID:   "evt_payid_1",
		Event:     "payment.failed",
		OrderID:   orderID,
		PaymentID: "pay_real_1",
		Raw:       []byte(`{}`),
	}); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	// Restore the authorisation so there is a live hold to release.
	if err := db.Pool.QueryRow(t.Context(),
		`UPDATE payments SET status = 'AUTHORIZED' WHERE id = $1 RETURNING id`, paymentID).
		Scan(new(testsupport.UUID)); err != nil {
		t.Fatalf("restore authorization: %v", err)
	}

	// The payment id is now durable alongside the order id, not instead of it.
	gotOrder, gotRef := readIdentifiers(t, db, paymentID)
	if gotOrder != orderID {
		t.Errorf("gateway_order_id = %q, want %q preserved", gotOrder, orderID)
	}
	if gotRef != "pay_real_1" {
		t.Errorf("gateway_reference = %q, want the payment id pay_real_1", gotRef)
	}

	// And a later void addresses the payment, not the order.
	if err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID); err != nil {
		t.Fatalf("void: %v", err)
	}
	want := "/payments/pay_real_1/void"
	if len(g.paths) == 0 || g.paths[len(g.paths)-1] != want {
		t.Errorf("gateway paths = %v, want the last call to be %s", g.paths, want)
	}
	for _, p := range g.paths {
		if strings.Contains(p, orderID) {
			t.Errorf("gateway was called with the order id: %s", p)
		}
	}
}

// TestAnEventCarryingOnlyTheOrderIdStillResolves keeps resolution working before
// any payment id exists, which is the case a payment-only lookup would miss.
func TestAnEventCarryingOnlyTheOrderIdStillResolves(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	g := newRecordingGateway(t)
	svc := newServiceOn(t, db, g)

	_, paymentID, orderID := seedOrderOnlyPayment(t, db, "AUTHORIZED")

	if err := svc.ConfirmFromWebhook(t.Context(), payments.GatewayEvent{
		EventID: "evt_orderonly_1",
		Event:   "payment.authorized",
		OrderID: orderID,
		Raw:     []byte(`{}`),
	}); err != nil {
		t.Fatalf("order-only event did not resolve: %v", err)
	}

	if _, ref := readIdentifiers(t, db, paymentID); ref != "" {
		t.Errorf("gateway_reference = %q, want empty: no payment id was disclosed", ref)
	}
}

// TestAVoidWithNoPaymentIdIsReportedRatherThanFaked closes the other half.
//
// An AUTHORIZED payment is holding real customer funds. With no payment id the
// void cannot be issued, and marking the row VOIDED would tell support and the
// customer the money is back while the hold is still live at the gateway.
func TestAVoidWithNoPaymentIdIsReportedRatherThanFaked(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	g := newRecordingGateway(t)
	svc := newServiceOn(t, db, g)

	bookingID, paymentID, _ := seedOrderOnlyPayment(t, db, "AUTHORIZED")

	err := svc.ReleaseAuthorizationForBooking(t.Context(), bookingID)
	if err == nil {
		t.Fatal("void with no payment id reported success, so the record would claim a hold is released when it is not")
	}
	if len(g.paths) != 0 {
		t.Errorf("gateway was called without a payment id: %v", g.paths)
	}

	var status string
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(models.PaymentAuthorized) {
		t.Errorf("payment status = %q, want it left AUTHORIZED so the void stays retryable", status)
	}
}
