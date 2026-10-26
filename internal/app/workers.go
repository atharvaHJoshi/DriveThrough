package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"drivethrough/internal/bookings"
	"drivethrough/internal/config"
	"drivethrough/internal/matching"
)

// WorkerSet owns the background loops. All of them observe the caller's
// context, so cancelling it during shutdown stops every loop — no
// context.Background() leaks, no orphaned goroutines after the server exits.
type WorkerSet struct {
	coordinator *Coordinator
	bookingRepo *bookings.Repository
	offerRepo   *matching.Repository
	cfg         config.WorkerConfig
}

func (w *WorkerSet) enabled() bool {
	return w.cfg.Enabled
}

// Start launches all loops and returns immediately. Each loop honours ctx.
func (w *WorkerSet) Start(ctx context.Context) {
	if !w.enabled() {
		slog.Info("background workers disabled")
		return
	}

	wg := &sync.WaitGroup{}
	wg.Add(4)
	go func() { defer wg.Done(); w.scheduledBookingLoop(ctx) }()
	go func() { defer wg.Done(); w.offerExpiryLoop(ctx) }()
	go func() { defer wg.Done(); w.staleSearchRetryLoop(ctx) }()
	go func() { defer wg.Done(); w.noShowLoop(ctx) }()

	go func() {
		wg.Wait()
		slog.Info("background workers stopped")
	}()

	slog.Info("background workers started",
		"scheduled_poll", w.cfg.ScheduledPollInterval,
		"offer_expiry", w.cfg.OfferExpiryInterval,
		"stale_search", w.cfg.StaleSearchInterval,
		"no_show_sweep", w.cfg.NoShowSweepInterval,
	)
}

func (w *WorkerSet) scheduledBookingLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.ScheduledPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.matchScheduledBookings(ctx)
		}
	}
}

func (w *WorkerSet) matchScheduledBookings(ctx context.Context) {
	ready, err := w.bookingRepo.ListScheduledReady(ctx)
	if err != nil {
		slog.Warn("failed to list scheduled bookings", "error", err)
	} else {
		for i := range ready {
			b := ready[i]
			slog.Info("matching scheduled booking", "booking_id", b.ID)
			if err := w.coordinator.OnBookingCreated(ctx, &b); err != nil {
				slog.Error("failed to match scheduled booking", "booking_id", b.ID, "error", err)
			}
		}
	}

	// Re-drive searches that found nobody on the inline attempt. Without this a
	// driver who comes online after a booking was created waits for the far
	// longer stale-retry threshold before the customer is matched.
	waiting, err := w.bookingRepo.ListSearchingWithoutLiveOffer(ctx, w.cfg.SearchRetryMinAge)
	if err != nil {
		slog.Warn("failed to list bookings awaiting a driver", "error", err)
		return
	}
	for i := range waiting {
		b := waiting[i]
		slog.Debug("retrying search for waiting booking", "booking_id", b.ID)
		if err := w.coordinator.OnBookingCreated(ctx, &b); err != nil {
			slog.Error("failed to retry matching", "booking_id", b.ID, "error", err)
		}
	}
}

func (w *WorkerSet) offerExpiryLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.OfferExpiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.expireOffers(ctx)
		}
	}
}

func (w *WorkerSet) expireOffers(ctx context.Context) {
	offers, err := w.offerRepo.ExpiredOffers(ctx)
	if err != nil {
		slog.Warn("failed to list expired offers", "error", err)
		return
	}
	for _, o := range offers {
		if err := w.offerRepo.UpdateOfferStatus(ctx, o.ID, "TIMEOUT"); err != nil {
			slog.Warn("failed to expire offer", "offer_id", o.ID, "error", err)
			continue
		}
		slog.Debug("offer expired", "offer_id", o.ID, "booking_id", o.BookingID)
	}
}

func (w *WorkerSet) staleSearchRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.StaleSearchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.retryStaleSearches(ctx)
		}
	}
}

// noShowLoop escalates bookings whose driver arrived but never started the
// trip. The driver's device is the only thing that can start a trip, so without
// this sweep a single unresponsive driver strands the customer indefinitely.
func (w *WorkerSet) noShowLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.NoShowSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweepNoShows(ctx)
		}
	}
}

func (w *WorkerSet) sweepNoShows(ctx context.Context) {
	stale, err := w.bookingRepo.ListStaleArrived(ctx, w.cfg.NoShowThreshold, 50)
	if err != nil {
		slog.Warn("failed to list stale arrived bookings", "error", err)
		return
	}

	for i := range stale {
		b := stale[i]
		// The transition is a compare-and-swap on DRIVER_ARRIVED, so if the
		// driver started the trip between the query and here this is a no-op
		// rather than a false accusation.
		if err := w.coordinator.OnDriverNoShowDetected(ctx, &b, "driver arrived but did not start the trip"); err != nil {
			slog.Error("failed to escalate no-show", "booking_id", b.ID, "error", err)
			continue
		}
		slog.Info("escalated no-show", "booking_id", b.ID, "driver_id", b.DriverID)
	}
}

func (w *WorkerSet) retryStaleSearches(ctx context.Context) {
	cutoff := time.Now().Add(-w.cfg.StaleSearchThreshold)
	stale, err := w.bookingRepo.ListSearchingStale(ctx, cutoff)
	if err != nil {
		slog.Warn("failed to list stale searching bookings", "error", err)
		return
	}
	for i := range stale {
		b := stale[i]
		slog.Info("retrying stale search", "booking_id", b.ID)
		if err := w.coordinator.OnBookingCreated(ctx, &b); err != nil {
			slog.Error("failed to retry matching", "booking_id", b.ID, "error", err)
		}
	}
}
