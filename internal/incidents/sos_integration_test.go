package incidents_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/incidents"
	"drivethrough/internal/models"
	"drivethrough/internal/sms"
	"drivethrough/internal/testsupport"
	"drivethrough/pkg/apperr"
)

// --- fakes -----------------------------------------------------------------

type fakeBookings struct {
	booking *models.Booking
	err     error
	// updates records every status change the service attempts.
	updates []string
}

func (f *fakeBookings) GetBooking(context.Context, uuid.UUID) (*models.Booking, error) {
	return f.booking, f.err
}

func (f *fakeBookings) UpdateStatus(_ context.Context, _ uuid.UUID, status models.BookingStatus, _ uuid.UUID, _ string) error {
	f.updates = append(f.updates, string(status))
	return nil
}

type fakeTrips struct {
	trip *models.Trip
	err  error
}

func (f *fakeTrips) GetByID(context.Context, uuid.UUID) (*models.Trip, error) { return f.trip, f.err }

type fakeDrivers struct {
	byID map[uuid.UUID]uuid.UUID // driver profile id -> user id
}

func (f *fakeDrivers) GetByID(_ context.Context, id uuid.UUID) (*models.Driver, error) {
	uid, ok := f.byID[id]
	if !ok {
		return nil, apperr.NotFound("driver not found")
	}
	return &models.Driver{ID: id, UserID: uid}, nil
}

// recordingSMS captures the numbers alerted and can stall the first send, which
// is how the per-contact timeout budget is exercised.
type recordingSMS struct {
	mu       sync.Mutex
	sent     []string
	stall    map[string]bool
	stallFor time.Duration
}

func (s *recordingSMS) Send(ctx context.Context, phone, _ string) error {
	if s.stall[phone] {
		select {
		case <-time.After(s.stallFor):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.sent = append(s.sent, phone)
	s.mu.Unlock()
	return nil
}

func (s *recordingSMS) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.sent))
	copy(out, s.sent)
	return out
}

// newService builds the service over the caller's already-seeded database. It
// deliberately does not truncate, since each test seeds its own users first and
// a second truncate would erase them.
func newService(t *testing.T, db *testsupport.DB, bk *fakeBookings, tr *fakeTrips, dr *fakeDrivers, sender sms.Sender) *incidents.Service {
	t.Helper()
	s := incidents.NewService(incidents.NewRepository(db.Pool), bk, nil, sender)
	if tr != nil {
		s.SetTripLookup(tr)
	}
	if dr != nil {
		s.SetDriverLookup(dr)
	}
	return s
}

func addContact(t *testing.T, db *testsupport.DB, userID testsupport.UUID, phone string) {
	t.Helper()
	db.Exec(t,
		`INSERT INTO emergency_contacts (user_id, name, phone, relation) VALUES ($1, 'Kin', $2, 'SISTER')`,
		userID, phone)
}

// --- tests -----------------------------------------------------------------

// TestSOSRejectsNonParty is the security invariant. A user who is neither the
// customer nor the assigned driver must not be able to raise an SOS against a
// trip, because doing so pages emergency contacts and support on a stranger's
// behalf.
func TestSOSRejectsNonParty(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	owner := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Owner', $1, 'CUSTOMER') RETURNING id`, "+919000000001")
	outsider := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Outsider', $1, 'CUSTOMER') RETURNING id`, "+919000000002")
	driverUser := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Driver', $1, 'DRIVER') RETURNING id`, "+919000000003")
	driverID := db.UUID(t, `INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`, driverUser)

	addContact(t, db, owner, "+919111111111")

	vehicleID := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		owner, "KA01AB1234")
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 'OTP_VERIFIED') RETURNING id`, owner, vehicleID)
	tripID := db.UUID(t,
		`INSERT INTO trips (booking_id, driver_id, vehicle_id, status) VALUES ($1, $2, $3, 'ACTIVE') RETURNING id`,
		bookingID, driverID, vehicleID)

	sender := &recordingSMS{}
	s := newService(t, db,
		&fakeBookings{booking: &models.Booking{ID: bookingID, CustomerID: owner}},
		&fakeTrips{trip: &models.Trip{ID: tripID, BookingID: bookingID, DriverID: driverID}},
		&fakeDrivers{byID: map[uuid.UUID]uuid.UUID{driverID: driverUser}},
		sender)

	_, err := s.TriggerSOS(t.Context(), incidents.SOSInput{
		UserID: outsider,
		TripID: &tripID,
	})
	if err == nil {
		t.Fatal("a user with no connection to the trip raised an SOS")
	}
	if !apperr.IsForbidden(err) {
		t.Errorf("expected forbidden, got %v", err)
	}
	if len(sender.recipients()) != 0 {
		t.Error("a rejected SOS still alerted emergency contacts")
	}

	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM sos`).Scan(&count); err != nil {
		t.Fatalf("count sos: %v", err)
	}
	if count != 0 {
		t.Errorf("persisted %d SOS records for a rejected request", count)
	}
}

// TestSOSAdmitsAssignedDriver is the other half: the driver is as much in danger
// as the customer, and was previously refused because only the customer was
// recognised as a party.
func TestSOSAdmitsAssignedDriver(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	owner := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Owner', $1, 'CUSTOMER') RETURNING id`, "+919000000011")
	driverUser := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Driver', $1, 'DRIVER') RETURNING id`, "+919000000012")
	driverID := db.UUID(t, `INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`, driverUser)

	addContact(t, db, driverUser, "+919222222222")

	vehicleID := db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2022, 'AUTOMATIC', 'DIESEL', 'SUV') RETURNING id`,
		owner, "KA01AB1234")
	bookingID := db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status)
		 VALUES ($1, $2, 'HOURLY', 'A', 12.9, 77.6, 'OTP_VERIFIED') RETURNING id`, owner, vehicleID)
	tripID := db.UUID(t,
		`INSERT INTO trips (booking_id, driver_id, vehicle_id, status) VALUES ($1, $2, $3, 'ACTIVE') RETURNING id`,
		bookingID, driverID, vehicleID)

	sender := &recordingSMS{}
	s := newService(t, db,
		&fakeBookings{booking: &models.Booking{ID: bookingID, CustomerID: owner}},
		&fakeTrips{trip: &models.Trip{ID: tripID, BookingID: bookingID, DriverID: driverID}},
		&fakeDrivers{byID: map[uuid.UUID]uuid.UUID{driverID: driverUser}},
		sender)

	if _, err := s.TriggerSOS(t.Context(), incidents.SOSInput{
		UserID: driverUser,
		TripID: &tripID,
	}); err != nil {
		t.Fatalf("the assigned driver could not raise an SOS on their own trip: %v", err)
	}
	if len(sender.recipients()) == 0 {
		t.Error("the driver's emergency contacts were not alerted")
	}
}

// TestSOSWithoutTripIsAlwaysAllowed: a person in danger with no active trip must
// never be blocked. The scoping check only applies when a trip is named.
func TestSOSWithoutTripIsAlwaysAllowed(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	user := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Solo', $1, 'CUSTOMER') RETURNING id`, "+919000000021")
	addContact(t, db, user, "+919333333333")

	sender := &recordingSMS{}
	s := newService(t, db, &fakeBookings{}, nil, nil, sender)

	if _, err := s.TriggerSOS(t.Context(), incidents.SOSInput{
		UserID:    user,
		Latitude:  12.9,
		Longitude: 77.6,
	}); err != nil {
		t.Fatalf("a personal SOS with no trip was blocked: %v", err)
	}
	if got := sender.recipients(); len(got) != 1 {
		t.Errorf("alerts = %v, want the one registered contact", got)
	}
}

// TestSOSFailsClosedWhenScopeIsUnavailable: if the collaborators needed to verify
// a trip are missing, the request must be refused rather than accepted unchecked.
func TestSOSFailsClosedWhenScopeIsUnavailable(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	user := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Solo', $1, 'CUSTOMER') RETURNING id`, "+919000000031")
	tripID := testsupport.UUID{}

	s := newService(t, db, &fakeBookings{}, nil, nil, &recordingSMS{})
	if _, err := s.TriggerSOS(t.Context(), incidents.SOSInput{UserID: user, TripID: &tripID}); err == nil {
		t.Fatal("an SOS was accepted with no way to verify trip access")
	}
}

// TestEveryEmergencyContactGetsAnAlert is the reliability invariant. A single
// shared deadline let one slow gateway call consume the whole window and skip
// every later contact.
func TestEveryEmergencyContactGetsAnAlert(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	user := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Solo', $1, 'CUSTOMER') RETURNING id`, "+919000000041")

	// The first number stalls past the per-contact budget.
	stalling := "+919400000001"
	for _, p := range []string{stalling, "+919400000002", "+919400000003"} {
		addContact(t, db, user, p)
	}

	sender := &recordingSMS{stall: map[string]bool{stalling: true}, stallFor: 3 * time.Second}
	s := newService(t, db, &fakeBookings{}, nil, nil, sender)
	// A short per-contact budget keeps the test fast while preserving the
	// property under test: the stall is longer than one contact's allowance but
	// shorter than the sum of all of them.
	s.SetSMSTimeout(50 * time.Millisecond)

	if _, err := s.TriggerSOS(t.Context(), incidents.SOSInput{UserID: user, Latitude: 12.9, Longitude: 77.6}); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	got := sender.recipients()
	if len(got) != 2 {
		t.Errorf("alerted %v, want the two contacts after the stalled one", got)
	}
}

// TestSOSIsIdempotentWhileActive stops a stuck client from stacking duplicate
// emergency alerts on the same incident.
func TestSOSIsIdempotentWhileActive(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)

	user := db.UUID(t, `INSERT INTO users (name, phone, role) VALUES ('Solo', $1, 'CUSTOMER') RETURNING id`, "+919000000051")
	addContact(t, db, user, "+919500000001")

	sender := &recordingSMS{}
	s := newService(t, db, &fakeBookings{}, nil, nil, sender)

	first, err := s.TriggerSOS(t.Context(), incidents.SOSInput{UserID: user, Latitude: 12.9, Longitude: 77.6})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	second, err := s.TriggerSOS(t.Context(), incidents.SOSInput{UserID: user, Latitude: 12.9, Longitude: 77.6})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("a second SOS created a new record %s, want the active one %s", second.ID, first.ID)
	}
	if got := len(sender.recipients()); got != 1 {
		t.Errorf("contacts alerted %d times, want 1", got)
	}
}
