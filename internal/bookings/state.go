package bookings

import (
	"fmt"

	"drivethrough/internal/models"
)

type StateTransition struct {
	From []models.BookingStatus
	To   models.BookingStatus
}

var transitions = []StateTransition{
	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
	}, To: models.BookingAssigned},
	{From: []models.BookingStatus{models.BookingAssigned}, To: models.BookingEnRoute},
	{From: []models.BookingStatus{models.BookingEnRoute}, To: models.BookingArrived},
	{From: []models.BookingStatus{models.BookingArrived}, To: models.BookingOTPVerified},
	{From: []models.BookingStatus{models.BookingOTPVerified}, To: models.BookingStarted},
	{From: []models.BookingStatus{models.BookingStarted}, To: models.BookingPaused},
	{From: []models.BookingStatus{models.BookingPaused}, To: models.BookingResumed},
	{From: []models.BookingStatus{models.BookingResumed}, To: models.BookingPaused},
	{From: []models.BookingStatus{models.BookingStarted, models.BookingPaused, models.BookingResumed}, To: models.BookingCompleted},

	// A driver declares the customer never showed up. Legal only before the
	// handover, since once the OTP is confirmed the customer is in the car.
	// DRIVER_NO_SHOW was previously listed in IsTerminal and permitted by the
	// column CHECK, but no transition reached it, so the status was unreachable
	// and a customer could not be marked absent at all.
	{From: []models.BookingStatus{
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
	}, To: models.BookingDriverNoShow},

	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
	}, To: models.BookingCancelled},

	// A driver can cancel from the moment they are assigned. This is distinct
	// from BookingCancelled and feeds the driver's cancellation rate, so it stays
	// legal through ARRIVED. Once the customer has handed over the vehicle
	// (OTP_VERIFIED) a plain cancellation is no longer appropriate.
	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
	}, To: models.BookingCancelledByDriver},

	// An administrator revoking a booking reaches every non-terminal state,
	// including the ones past the handover point that CANCELLED_BY_DRIVER cannot.
	// Without this, suspending a driver who was mid-ride left the booking open:
	// the cleanup skipped it as an illegal transition, the trip kept running, and
	// the suspended driver could still complete it and collect the fare.
	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
		models.BookingSupportEscalated,
	}, To: models.BookingCancelledByAdmin},

	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
	}, To: models.BookingSearching},

	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
	}, To: models.BookingSupportEscalated},

	// A payment can only fail before the vehicle changes hands. Once the customer
	// has confirmed the OTP they are in the car, so a gateway failure must not
	// close the ride; the outstanding balance is collected afterwards.
	//
	// PAYMENT_FAILED is terminal and is permitted by the column CHECK, but it
	// previously had no incoming transition at all, so every call to mark a
	// failed payment was rejected by the state machine as an invalid transition
	// and the booking silently stayed payable.
	{From: []models.BookingStatus{
		models.BookingRequested,
		models.BookingSearching,
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
	}, To: models.BookingPaymentFailed},

	// SUPPORT_ESCALATED is an operational overlay, not a lifecycle stage. It
	// flags a booking for the operations queue after an SOS or a support case,
	// but the ride underneath it is usually still in progress and must keep
	// working.
	//
	// Allowing only the terminal targets meant an SOS raised during handover
	// stranded the booking: the driver could not report arrival, the customer
	// could not confirm the OTP, and StartTrip requires OTP_VERIFIED, so the one
	// thing the booking could still do was be closed. Support resolving an
	// incident therefore left riders with a permanently stuck ride.
	//
	// The full in-progress progression is therefore reachable from here, so
	// operations can hand a ride back to normal once the case is handled.
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingEnRoute},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingArrived},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingOTPVerified},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingStarted},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingPaused},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingResumed},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingCancelled},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingCancelledByDriver},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingCompleted},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingAccident},
	{From: []models.BookingStatus{models.BookingSupportEscalated}, To: models.BookingDriverNoShow},

	{From: []models.BookingStatus{
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
		models.BookingEnRoute,
		models.BookingAssigned,
	}, To: models.BookingAccident},
}

type StateMachine struct {
	valid map[models.BookingStatus]map[models.BookingStatus]bool
}

func NewStateMachine() *StateMachine {
	sm := &StateMachine{
		valid: make(map[models.BookingStatus]map[models.BookingStatus]bool),
	}

	for _, t := range transitions {
		for _, from := range t.From {
			if sm.valid[from] == nil {
				sm.valid[from] = make(map[models.BookingStatus]bool)
			}
			sm.valid[from][t.To] = true
		}
	}

	return sm
}

func (sm *StateMachine) CanTransition(from, to models.BookingStatus) bool {
	if sm.valid[from] == nil {
		return false
	}
	return sm.valid[from][to]
}

func (sm *StateMachine) Transition(from, to models.BookingStatus) error {
	if !sm.CanTransition(from, to) {
		return fmt.Errorf("invalid booking state transition: %s -> %s", from, to)
	}
	return nil
}

func IsTerminal(status models.BookingStatus) bool {
	switch status {
	case models.BookingCompleted,
		models.BookingCancelled,
		models.BookingCancelledByDriver,
		models.BookingCancelledByAdmin,
		models.BookingDriverNoShow,
		models.BookingPaymentFailed,
		models.BookingAccident:
		return true
	}
	return false
}
