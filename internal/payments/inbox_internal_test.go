package payments

import (
	"sync"
	"testing"
	"time"

	"drivethrough/internal/testsupport"
)

// These tests live in-package because the claim lease is deliberately a
// parameter of the unexported claim function. Exercising expiry honestly means
// shortening the lease rather than sleeping for the production value, and an
// external test package cannot reach it.

// TestAnUnprocessedEventIsNotReclaimedWhileItsLeaseIsLive is the deduplication
// guarantee.
//
// "Unprocessed" on its own is satisfied by every concurrent redelivery at once,
// so they would all process the same capture and credit earnings more than once.
// Only the holder of a live lease proceeds.
func TestAnUnprocessedEventIsNotReclaimedWhileItsLeaseIsLive(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := NewRepository(db.Pool)

	claimed, err := repo.claimGatewayEvent(t.Context(), "evt_live", "razorpay", "payment.captured", []byte(`{}`), time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}

	// A redelivery arriving while the first attempt is still in flight must lose.
	claimed, err = repo.claimGatewayEvent(t.Context(), "evt_live", "razorpay", "payment.captured", []byte(`{}`), time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Error("a live claim was stolen, so a redelivery would process a capture twice")
	}
}

// TestAnAbandonedClaimExpires covers the recovery half: a worker that dies
// mid-processing must not block the event forever.
func TestAnAbandonedClaimExpires(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := NewRepository(db.Pool)

	// Claimed with a lease that is already effectively expired.
	claimed, err := repo.claimGatewayEvent(t.Context(), "evt_expired", "razorpay", "payment.captured", []byte(`{}`), time.Nanosecond)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}

	time.Sleep(5 * time.Millisecond)

	claimed, err = repo.claimGatewayEvent(t.Context(), "evt_expired", "razorpay", "payment.captured", []byte(`{}`), time.Nanosecond)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !claimed {
		t.Error("an abandoned claim never expired, so a gateway retry would be dropped forever")
	}
}

// TestACompletedEventIsNeverReclaimed is the strongest guarantee: once the work
// is done, no delivery of that event can ever repeat it.
func TestACompletedEventIsNeverReclaimed(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := NewRepository(db.Pool)

	claimed, err := repo.ClaimGatewayEvent(t.Context(), "evt_done", "razorpay", "payment.captured", []byte(`{}`))
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if err := repo.CompleteGatewayEvent(t.Context(), "evt_done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Even with no lease at all, a completed event stays closed.
	claimed, err = repo.claimGatewayEvent(t.Context(), "evt_done", "razorpay", "payment.captured", []byte(`{}`), time.Nanosecond)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if claimed {
		t.Error("a completed event was re-claimed, so a redelivery would repeat settled work")
	}
}

// TestConcurrentDeliveriesElectOneWinner is the concurrency invariant at the
// level the race actually occurs: the gateway delivering the same event twice at
// the same moment.
func TestConcurrentDeliveriesElectOneWinner(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := NewRepository(db.Pool)

	const racers = 8
	type result struct {
		claimed bool
		err     error
	}
	results := make(chan result, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := repo.claimGatewayEvent(t.Context(), "evt_race", "razorpay", "payment.captured", []byte(`{}`), time.Minute)
			results <- result{claimed, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for r := range results {
		if r.err != nil {
			t.Fatalf("claim: %v", r.err)
		}
		if r.claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}
}

// TestClaimingRecordsTheEventOnlyWhenItSucceeds confirms a claim does not leave
// a row behind that claims to be processed.
func TestClaimingRecordsTheEventOnlyWhenItSucceeds(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Truncate(t)
	repo := NewRepository(db.Pool)

	if _, err := repo.claimGatewayEvent(t.Context(), "evt_pending", "razorpay", "payment.captured", []byte(`{}`), time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	var processedAt *time.Time
	if err := db.Pool.QueryRow(t.Context(),
		`SELECT processed_at FROM gateway_events WHERE id = $1`, "evt_pending").Scan(&processedAt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if processedAt != nil {
		t.Errorf("processed_at = %v, want NULL until the work actually succeeds", *processedAt)
	}
}
