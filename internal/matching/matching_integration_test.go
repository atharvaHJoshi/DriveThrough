package matching_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"drivethrough/internal/matching"
	"drivethrough/internal/testsupport"
)

type fixture struct {
	db         *testsupport.DB
	customerID testsupport.UUID
	driverIDs  []testsupport.UUID
	vehicleID  testsupport.UUID
	bookingID  testsupport.UUID
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := testsupport.NewDB(t)
	db.Truncate(t)

	f := &fixture{db: db}
	f.customerID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Customer', $1, 'CUSTOMER') RETURNING id`,
		fmt.Sprintf("+9190%09d", time.Now().UnixNano()%1000000000))
	f.vehicleID = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Honda', 'City', 2023, 'MANUAL', 'PETROL', 'SEDAN') RETURNING id`,
		f.customerID, fmt.Sprintf("MH14XY%04d", time.Now().UnixNano()%10000))
	f.bookingID = db.UUID(t,
		`INSERT INTO bookings (customer_id, vehicle_id, booking_type, pickup_location, pickup_lat, pickup_lng, status, duration_hours)
		 VALUES ($1, $2, 'HOURLY', 'Koramangala', 12.9352, 77.6245, 'SEARCHING', 2) RETURNING id`,
		f.customerID, f.vehicleID)

	// Three verified, online drivers so a single booking can be contested.
	for i := 0; i < 3; i++ {
		uid := db.UUID(t,
			`INSERT INTO users (name, phone, role) VALUES ($1, $2, 'DRIVER') RETURNING id`,
			fmt.Sprintf("Driver %d", i), fmt.Sprintf("+9180%09d", time.Now().UnixNano()+int64(i)))
		did := db.UUID(t,
			`INSERT INTO drivers (user_id, status, verification_status) VALUES ($1, 'ONLINE', 'APPROVED') RETURNING id`,
			uid)
		f.driverIDs = append(f.driverIDs, did)
	}

	return f
}

// TestCreateOfferIsIdempotent covers the re-drive loop: the scheduled worker
// re-runs matching for the same booking on a timer, and each pass must not
// create a second live offer for the same driver, or the driver sees duplicates.
func TestCreateOfferIsIdempotent(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := repo.CreateOffer(ctx, f.bookingID, f.driverIDs[0], time.Now().Add(2*time.Minute)); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	var count int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM matching_offers WHERE booking_id = $1 AND driver_id = $2 AND status = 'PENDING'`,
		f.bookingID, f.driverIDs[0]).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("pending offers = %d, want exactly 1 after repeated creation", count)
	}
}

// TestClaimOfferRefusesExpired confirms a driver cannot accept an offer that
// timed out while the app sat on a notification. A refusal is reported as
// won=false with no error, not as an exception.
func TestClaimOfferRefusesExpired(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	offerID, err := repo.CreateOffer(ctx, f.bookingID, f.driverIDs[0], time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}

	won, err := repo.ClaimOffer(ctx, offerID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if won {
		t.Error("an expired offer was claimed")
	}

	var status string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status FROM matching_offers WHERE id = $1`, offerID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("status = %q, want the expired offer left PENDING", status)
	}
}

// TestClaimOfferIsSingleUse confirms a second claim of the same offer loses.
func TestClaimOfferIsSingleUse(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	offerID, err := repo.CreateOffer(ctx, f.bookingID, f.driverIDs[0], time.Now().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}

	won, err := repo.ClaimOffer(ctx, offerID)
	if err != nil || !won {
		t.Fatalf("the first claim should have won: won=%v err=%v", won, err)
	}

	won, err = repo.ClaimOffer(ctx, offerID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won {
		t.Error("the same offer was claimed twice")
	}
}

// TestGetOfferFiltersExpired guards the read path the driver app uses. Returning
// a stale offer invites an accept that the claim then rejects, which surfaces to
// the driver as a confusing error.
func TestGetOfferFiltersExpired(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	if _, err := repo.CreateOffer(ctx, f.bookingID, f.driverIDs[0], time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create offer: %v", err)
	}

	if _, err := repo.GetOfferByBookingAndDriver(ctx, f.bookingID, f.driverIDs[0]); err == nil {
		t.Error("an expired offer was returned as live")
	}
}

// TestReleaseOfferReturnsItToPending covers a driver declining, which must free
// the slot for the next candidate rather than leaving the booking with one fewer
// driver than it started with.
func TestReleaseOfferReturnsItToPending(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	offerID, err := repo.CreateOffer(ctx, f.bookingID, f.driverIDs[0], time.Now().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	if err := repo.ReleaseOffer(ctx, offerID); err != nil {
		t.Fatalf("release: %v", err)
	}

	var status string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status FROM matching_offers WHERE booking_id = $1 AND driver_id = $2`,
		f.bookingID, f.driverIDs[0]).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("status = %q, want PENDING after a release", status)
	}
}

// TestAcceptUpdatesTheClaimedOfferNotTheFirst is the bug the offer id fix
// addressed: updating "the first offer for this booking" claimed a different
// driver's offer and left the real winner's offer pending.
func TestAcceptUpdatesTheClaimedOfferNotTheFirst(t *testing.T) {
	f := setup(t)
	repo := matching.NewRepository(f.db.Pool)
	ctx := context.Background()

	// The winner is deliberately not the first offer created.
	winner, other := f.driverIDs[2], f.driverIDs[0]
	offerIDs := map[testsupport.UUID]testsupport.UUID{}
	for _, d := range f.driverIDs {
		id, err := repo.CreateOffer(ctx, f.bookingID, d, time.Now().Add(2*time.Minute))
		if err != nil {
			t.Fatalf("create offer: %v", err)
		}
		offerIDs[d] = id
	}

	claimed, err := repo.ClaimOffer(ctx, offerIDs[winner])
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("the claim did not win an unclaimed offer")
	}

	var acceptedDriver testsupport.UUID
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT driver_id FROM matching_offers WHERE booking_id = $1 AND status = 'ACCEPTED'`,
		f.bookingID).Scan(&acceptedDriver); err != nil {
		t.Fatalf("read accepted: %v", err)
	}
	if acceptedDriver != winner {
		t.Errorf("accepted offer belongs to %s, want the claiming driver %s", acceptedDriver, winner)
	}

	// The other driver's offer must be untouched, not silently accepted.
	var otherStatus string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status FROM matching_offers WHERE booking_id = $1 AND driver_id = $2`,
		f.bookingID, other).Scan(&otherStatus); err != nil {
		t.Fatalf("read other: %v", err)
	}
	if otherStatus != "PENDING" {
		t.Errorf("the unclaimed driver's offer is %q, want it left PENDING", otherStatus)
	}
}
