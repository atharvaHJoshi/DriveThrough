package bookings

import (
	"testing"

	"drivethrough/internal/models"
)

// TestEveryStatusIsReachable pins down that every status the database CHECK allows is
// actually usable.
//
// PAYMENT_FAILED was terminal and permitted by the column CHECK while having no
// incoming transition, so a failed payment was rejected as an invalid transition
// and the booking stayed payable. A table-driven reachability test catches that
// class of gap.
func TestEveryStatusIsReachable(t *testing.T) {
	sm := NewStateMachine()

	reachable := map[models.BookingStatus]bool{}
	for from, targets := range sm.valid {
		if len(targets) == 0 {
			t.Errorf("status %q has no outgoing transitions and can never be left", from)
		}
		for to := range targets {
			reachable[to] = true
		}
	}

	// REQUESTED is the entry point for a new booking, so it is reachable without
	// an incoming transition. It is the only status allowed to be in that
	// position: any other would be a status nothing can ever produce.
	reachable[models.BookingRequested] = true

	for _, s := range allStatuses() {
		if !reachable[s] {
			t.Errorf("status %q is not the target of any transition; it is unreachable", s)
		}
	}
}

// TestOnlyRequestedLacksAnIncomingTransition documents the entry-point rule.
// Any second status without an incoming transition is an orphan the code can
// never put a booking into.
func TestOnlyRequestedLacksAnIncomingTransition(t *testing.T) {
	sm := NewStateMachine()

	incoming := map[models.BookingStatus]bool{}
	for _, targets := range sm.valid {
		for to := range targets {
			incoming[to] = true
		}
	}

	for _, s := range allStatuses() {
		if s == models.BookingRequested {
			continue
		}
		if !incoming[s] {
			t.Errorf("status %q has no incoming transition and is not the entry point", s)
		}
	}
}

// TestTerminalStatusesAreReachable guards the specific gap found above: a
// terminal status that no transition can produce.
func TestTerminalStatusesAreReachable(t *testing.T) {
	sm := NewStateMachine()

	reachable := map[models.BookingStatus]bool{}
	for _, targets := range sm.valid {
		for to := range targets {
			reachable[to] = true
		}
	}

	for _, s := range allStatuses() {
		if !IsTerminal(s) {
			continue
		}
		if !reachable[s] {
			t.Errorf("terminal status %q is unreachable: no transition can produce it", s)
		}
	}
}

// TestTerminalStatesAreDeadEnds verifies a completed or cancelled booking cannot
// be moved on. A terminal status with outgoing transitions would let a late
// webhook reopen a finished ride.
func TestTerminalStatesAreDeadEnds(t *testing.T) {
	sm := NewStateMachine()

	for _, s := range allStatuses() {
		if !IsTerminal(s) {
			continue
		}
		for _, to := range allStatuses() {
			if to == s {
				continue
			}
			if sm.CanTransition(s, to) {
				t.Errorf("terminal status %q must not transition to %q", s, to)
			}
		}
	}
}

// TestHappyPath covers the normal ride from request to completion, including the
// pause/resume cycle.
func TestHappyPath(t *testing.T) {
	sm := NewStateMachine()

	path := []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
		models.BookingCompleted,
	}

	for i := 0; i < len(path)-1; i++ {
		from, to := path[i], path[i+1]
		if !sm.CanTransition(from, to) {
			t.Errorf("expected %s -> %s to be allowed", from, to)
		}
	}
}

// TestHandoverIsIrreversible is the safety property at the centre of the OTP
// flow: once the customer has handed over the vehicle, the ride cannot be
// cancelled or written off as a no-show.
func TestHandoverIsIrreversible(t *testing.T) {
	sm := NewStateMachine()

	post := []models.BookingStatus{
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
	}

	for _, from := range post {
		for _, to := range []models.BookingStatus{
			models.BookingCancelled,
			models.BookingCancelledByDriver,
			models.BookingDriverNoShow,
		} {
			if sm.CanTransition(from, to) {
				t.Errorf("booking must not go %s -> %s after the handover", from, to)
			}
		}
	}
}

// TestNoShowIsOnlyBeforeHandover pins the complement: a driver can still mark the
// customer absent while waiting at the pickup point.
func TestNoShowIsOnlyBeforeHandover(t *testing.T) {
	sm := NewStateMachine()

	pre := []models.BookingStatus{
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
	}
	for _, from := range pre {
		if !sm.CanTransition(from, models.BookingDriverNoShow) {
			t.Errorf("expected %s -> DRIVER_NO_SHOW to be allowed", from)
		}
	}

	if sm.CanTransition(models.BookingOTPVerified, models.BookingDriverNoShow) {
		t.Error("DRIVER_NO_SHOW must not be possible once the OTP is confirmed")
	}
}

// TestSupportEscalationDoesNotFreeze is the other gap found: SUPPORT_ESCALATED is
// not terminal, so an SOS must still leave the ride able to finish or be
// cancelled. Previously it had no outgoing transitions at all, so raising an SOS
// left the booking permanently stuck.
func TestSupportEscalationDoesNotFreeze(t *testing.T) {
	sm := NewStateMachine()

	from := []models.BookingStatus{models.BookingSupportEscalated}
	to := []models.BookingStatus{
		models.BookingCancelled,
		models.BookingCancelledByDriver,
		models.BookingCompleted,
		models.BookingAccident,
		models.BookingDriverNoShow,
	}

	if IsTerminal(models.BookingSupportEscalated) {
		t.Error("SUPPORT_ESCALATED must not be terminal: the ride still has to be able to finish")
	}
	for _, target := range to {
		if !sm.CanTransition(from[0], target) {
			t.Errorf("expected SUPPORT_ESCALATED -> %s to be allowed", target)
		}
	}
}

// TestPaymentFailureOnlyBeforeHandover asserts a gateway failure cannot close a
// ride that is already under way.
func TestPaymentFailureOnlyBeforeHandover(t *testing.T) {
	sm := NewStateMachine()

	pre := []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
	}
	for _, from := range pre {
		if !sm.CanTransition(from, models.BookingPaymentFailed) {
			t.Errorf("expected %s -> PAYMENT_FAILED to be allowed", from)
		}
	}

	for _, from := range []models.BookingStatus{models.BookingStarted, models.BookingPaused, models.BookingResumed} {
		if sm.CanTransition(from, models.BookingPaymentFailed) {
			t.Errorf("an under-way booking must not go %s -> PAYMENT_FAILED", from)
		}
	}
}

// TestNoUnknownStatusTransitions fails if the machine is ever handed a status it
// has never heard of, rather than silently permitting it.
func TestNoUnknownStatusTransitions(t *testing.T) {
	sm := NewStateMachine()

	if sm.CanTransition("NOT_A_STATUS", models.BookingAssigned) {
		t.Error("an unknown status must not permit any transition")
	}
	if sm.CanTransition(models.BookingRequested, "NOT_A_STATUS") {
		t.Error("must not transition to an unknown status")
	}
	if err := sm.Transition(models.BookingCompleted, models.BookingAssigned); err == nil {
		t.Error("expected an error transitioning out of a terminal state")
	}
}

// allStatuses lists every booking status the system defines.
func allStatuses() []models.BookingStatus {
	return []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
		models.BookingCompleted,
		models.BookingCancelled,
		models.BookingCancelledByDriver,
		models.BookingDriverNoShow,
		models.BookingPaymentFailed,
		models.BookingAccident,
		models.BookingSupportEscalated,
	}
}

// TestSupportEscalationDoesNotStrandTheRide is a regression test for the SOS
// path.
//
// An SOS escalates the booking to SUPPORT_ESCALATED. When that state could only
// reach terminal targets, a ride interrupted during handover could never report
// arrival, never verify the OTP, and therefore could never start — the customer
// and driver were left in a car with no way to complete or cancel the booking.
// Support "resolving" the incident silently ended the ride's life.
func TestSupportEscalationDoesNotStrandTheRide(t *testing.T) {
	sm := NewStateMachine()

	// Where an SOS can realistically be raised, mid-handover.
	interrupted := []models.BookingStatus{
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingStarted,
	}

	for _, from := range interrupted {
		if !sm.CanTransition(from, models.BookingSupportEscalated) {
			t.Errorf("an SOS cannot escalate a %s booking", from)
		}

		// The ride must still be completable.
		if !sm.CanTransition(models.BookingSupportEscalated, models.BookingCompleted) {
			t.Errorf("a %s booking escalated by SOS cannot be completed", from)
		}
		// And still cancellable by either party, so nobody is trapped.
		if !sm.CanTransition(models.BookingSupportEscalated, models.BookingCancelled) {
			t.Errorf("a %s booking escalated by SOS cannot be cancelled", from)
		}
		if !sm.CanTransition(models.BookingSupportEscalated, models.BookingAccident) {
			t.Errorf("a %s booking escalated by SOS cannot be reported as an accident", from)
		}
		// And it must not be treated as finished while the case is open.
		if IsTerminal(models.BookingSupportEscalated) {
			t.Errorf("SUPPORT_ESCALATED is treated as terminal")
		}
	}

	// A handover interrupted at the most common point must be able to finish the
	// normal arrival sequence.
	for _, step := range []models.BookingStatus{
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
	} {
		if !sm.CanTransition(models.BookingSupportEscalated, step) {
			t.Errorf("a ride escalated by SOS cannot still move to %s", step)
		}
	}
}

// TestEscalationCannotReopenATerminalBooking guards the other direction: an
// operations flag must not resurrect a ride that already ended.
func TestEscalationCannotReopenATerminalBooking(t *testing.T) {
	sm := NewStateMachine()
	for _, terminal := range []models.BookingStatus{
		models.BookingCompleted,
		models.BookingCancelled,
		models.BookingCancelledByDriver,
		models.BookingDriverNoShow,
		models.BookingPaymentFailed,
		models.BookingAccident,
	} {
		if sm.CanTransition(terminal, models.BookingSupportEscalated) {
			t.Errorf("a %s booking can be escalated back into the live flow", terminal)
		}
	}
}
