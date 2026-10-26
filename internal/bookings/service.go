package bookings

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/validator"
)

// MaxOTPAttempts caps handover-code guesses per booking. Five is enough for
// fat-fingered customers while keeping the 10^6 code space out of reach.
const MaxOTPAttempts = 5

type CreateBookingInput struct {
	CustomerID     uuid.UUID
	VehicleID      uuid.UUID
	BookingType    models.BookingType
	DurationHours  int
	PickupLocation string
	PickupLat      float64
	PickupLng      float64
	Destination    *string
	DestLat        *float64
	DestLng        *float64
	ScheduledAt    *time.Time
}

// VehicleLookup is the slice of the vehicles module that bookings needs,
// declared here so bookings does not import vehicles.
type VehicleLookup interface {
	GetVehicle(ctx context.Context, id uuid.UUID) (*models.Vehicle, error)
}

// DriverLookup resolves an authenticated user to their driver profile.
// bookings.driver_id references drivers(id) while a JWT carries users(id), so
// driver-scoped endpoints have to translate before comparing identifiers.
type DriverLookup interface {
	GetDriverProfile(ctx context.Context, userID uuid.UUID) (*models.Driver, error)
}

// TripLookup resolves a trip to its booking. Declared as an interface and
// injected late so bookings does not import trips, which would form a cycle
// because trips already depends on bookings.
type TripLookup interface {
	GetByID(ctx context.Context, tripID uuid.UUID) (*models.Trip, error)
	// GetActiveByBooking finds the trip for a booking. A booking carries no trip
	// id, so this is the only way a caller holding just a booking id can learn
	// which trip to observe.
	GetActiveByBooking(ctx context.Context, bookingID uuid.UUID) (*models.Trip, error)
}

// CancellationReactions runs the cross-module cleanup that follows a booking
// being cancelled. It is declared here and injected late because the coordinator
// depends on bookings, so bookings cannot import it.
//
// The HTTP cancel endpoints already call the coordinator directly. The bulk
// administrative path did not, which is why suspending a driver left their
// offers dangling, both parties unnotified, and the driver stuck in BUSY.
type CancellationReactions interface {
	OnBookingCancelled(ctx context.Context, booking *models.Booking) error
}

type Service struct {
	repo        *Repository
	pricing     *PricingService
	vehiclesSvc VehicleLookup
	driversSvc  DriverLookup
	trips       TripLookup
	reactions   CancellationReactions
	otpLen      int
}

func NewService(repo *Repository, pricing *PricingService, vehiclesSvc VehicleLookup, driversSvc DriverLookup, otpLen int) *Service {
	return &Service{
		repo:        repo,
		pricing:     pricing,
		vehiclesSvc: vehiclesSvc,
		driversSvc:  driversSvc,
		otpLen:      otpLen,
	}
}

// SetTripLookup injects the trip reader used to authorise live subscriptions.
func (s *Service) SetTripLookup(t TripLookup) { s.trips = t }

func (s *Service) CreateBooking(ctx context.Context, in CreateBookingInput) (*models.Booking, error) {
	if !validator.IsValidCoordinate(in.PickupLat, in.PickupLng) {
		return nil, apperr.BadRequest("invalid pickup coordinates", nil)
	}
	if !validator.IsValidDurationHours(in.DurationHours) {
		return nil, apperr.BadRequest("duration must be between 1 and 24 hours", nil)
	}

	vehicle, err := s.vehiclesSvc.GetVehicle(ctx, in.VehicleID)
	if err != nil {
		return nil, err
	}
	if vehicle.UserID != in.CustomerID {
		return nil, apperr.Forbidden("vehicle does not belong to you")
	}

	active, err := s.repo.CountActiveByCustomer(ctx, in.CustomerID)
	if err != nil {
		return nil, apperr.Wrap(500, "BOOKING_COUNT_FAILED", "failed to check active bookings", err, nil)
	}
	if active > 0 {
		return nil, apperr.Conflict("you already have an active booking", nil)
	}

	otp, err := generateOTP(s.otpLen)
	if err != nil {
		return nil, apperr.Wrap(500, "OTP_FAILED", "failed to generate otp", err, nil)
	}

	est := s.ComputeEstimate(in.DurationHours, in.ScheduledAt)

	booking := &models.Booking{
		CustomerID:     in.CustomerID,
		VehicleID:      in.VehicleID,
		BookingType:    in.BookingType,
		DurationHours:  in.DurationHours,
		PickupLocation: in.PickupLocation,
		PickupLat:      in.PickupLat,
		PickupLng:      in.PickupLng,
		Destination:    in.Destination,
		DestLat:        in.DestLat,
		DestLng:        in.DestLng,
		ScheduledAt:    in.ScheduledAt,
		Status:         models.BookingRequested,
		OTPCode:        otp,
		EstimatedPrice: est.Total,
		DriverFee:      est.DriverFee,
		PlatformFee:    est.PlatformFee,
		ProtectionFee:  est.ProtectionFee,
		Tax:            est.Tax,
	}

	if err := s.repo.Create(ctx, booking); err != nil {
		return nil, apperr.Wrap(500, "BOOKING_CREATE_FAILED", "failed to create booking", err, nil)
	}

	return booking, nil
}

func (s *Service) ComputeEstimate(durationHours int, scheduledAt *time.Time) PriceEstimate {
	night := false
	if scheduledAt != nil {
		hour := scheduledAt.Hour()
		night = hour >= 22 || hour < 6
	}
	return s.pricing.Estimate(durationHours, night)
}

func (s *Service) GetBooking(ctx context.Context, bookingID uuid.UUID) (*models.Booking, error) {
	return s.repo.GetByID(ctx, bookingID)
}

// resolveAssignedBooking loads a booking and asserts the caller's driver profile
// is the one assigned to it.
//
// The driver-scoped transitions (en route, arrived, no show) all mutate state
// that the customer is waiting on, so "any authenticated driver" is not an
// acceptable caller. Without this check any driver could mark someone else's
// customer as a no-show and end their booking.
func (s *Service) resolveAssignedBooking(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
	driver, err := s.driversSvc.GetDriverProfile(ctx, userID)
	if err != nil {
		return nil, err
	}

	booking, err := s.repo.GetByID(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	if booking.DriverID == nil || *booking.DriverID != driver.ID {
		return nil, apperr.Forbidden("you are not the driver of this booking")
	}
	return booking, nil
}

// MarkEnRoute records that the assigned driver is travelling to the pickup.
func (s *Service) MarkEnRoute(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
	if _, err := s.resolveAssignedBooking(ctx, bookingID, userID); err != nil {
		return nil, err
	}
	if err := s.UpdateStatus(ctx, bookingID, models.BookingEnRoute, userID, "driver is on the way"); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, bookingID)
}

// MarkArrived records that the driver reached the pickup point. This is the
// point at which the customer is sent the handover OTP.
func (s *Service) MarkArrived(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
	if _, err := s.resolveAssignedBooking(ctx, bookingID, userID); err != nil {
		return nil, err
	}
	if err := s.UpdateStatus(ctx, bookingID, models.BookingArrived, userID, "driver has arrived"); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, bookingID)
}

// MarkDriverNoShow ends the booking with the customer marked absent. Charges for
// the booking are left to the admin/payment path, which is a deliberate
// separation: the driver asserting an absence must not be able to trigger a
// charge on their own.
func (s *Service) MarkDriverNoShow(ctx context.Context, bookingID, userID uuid.UUID, reason string) (*models.Booking, error) {
	if _, err := s.resolveAssignedBooking(ctx, bookingID, userID); err != nil {
		return nil, err
	}
	if reason == "" {
		reason = "customer did not arrive at the pickup point"
	}
	if err := s.UpdateStatus(ctx, bookingID, models.BookingDriverNoShow, userID, reason); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, bookingID)
}

// AuthorizeTripAccess reports whether a user is a party to a trip.
//
// This backs the WebSocket subscription check. The trip is resolved to its
// booking because the booking is what records the two identities allowed to
// observe it.
func (s *Service) AuthorizeTripAccess(ctx context.Context, tripID, userID uuid.UUID) error {
	if s.trips == nil {
		// Fail closed: without the lookup there is no way to establish that the
		// caller is a party to the trip.
		return apperr.Forbidden("trip access is not configured")
	}
	trip, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		return err
	}
	booking, err := s.repo.GetByID(ctx, trip.BookingID)
	if err != nil {
		return err
	}
	return s.assertParty(ctx, booking, userID)
}

// AuthorizeBookingAccess is the same check for callers holding a booking id.
func (s *Service) AuthorizeBookingAccess(ctx context.Context, bookingID, userID uuid.UUID) error {
	booking, err := s.repo.GetByID(ctx, bookingID)
	if err != nil {
		return err
	}
	return s.assertParty(ctx, booking, userID)
}

// TripForBooking returns the trip belonging to a booking, for a caller who has
// already been established as a party to it.
//
// A booking row has no trip id, and the live-tracking socket is addressed by
// trip id. Without this lookup a customer holding a booking id from their own
// booking list has no way to subscribe to the trip, so live tracking would be
// unreachable from the customer's own client.
//
// Authorisation is checked first: the lookup itself is unscoped, so it must never
// be reachable without the party check that AuthorizeBookingAccess performs.
//
// A booking that has not started a trip yet legitimately has none, so "no trip"
// is reported as a not-found rather than an error the caller must special-case
// while polling a booking that has not reached OTP verification.
func (s *Service) TripForBooking(ctx context.Context, bookingID, userID uuid.UUID) (*models.Trip, error) {
	if s.trips == nil {
		return nil, apperr.Forbidden("trip lookup is not configured")
	}
	if err := s.AuthorizeBookingAccess(ctx, bookingID, userID); err != nil {
		return nil, err
	}
	return s.trips.GetActiveByBooking(ctx, bookingID)
}

// assertParty allows the customer who booked and the driver who was assigned,
// and nobody else. Admins are deliberately excluded: operational access to live
// location data is a separate, audited capability, not a side effect of the role.
func (s *Service) assertParty(ctx context.Context, booking *models.Booking, userID uuid.UUID) error {
	if booking.CustomerID == userID {
		return nil
	}

	if booking.DriverID != nil {
		driver, err := s.driversSvc.GetDriverProfile(ctx, userID)
		if err == nil && driver.ID == *booking.DriverID {
			return nil
		}
	}

	return apperr.Forbidden("you are not a party to this trip")
}

func (s *Service) GetBookingForCustomer(ctx context.Context, bookingID, customerID uuid.UUID) (*models.Booking, error) {
	return s.repo.GetForCustomer(ctx, bookingID, customerID)
}

func (s *Service) ListCustomerBookings(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]models.Booking, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.repo.ListForCustomer(ctx, customerID, limit, offset)
}

func (s *Service) ListDriverBookings(ctx context.Context, userID uuid.UUID, limit, offset int) ([]models.Booking, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// bookings.driver_id references drivers(id), so the JWT subject has to be
	// resolved to a driver profile before it can be used as a filter.
	driver, err := s.driversSvc.GetDriverProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListForDriver(ctx, driver.ID, limit, offset)
}

func (s *Service) AssignDriver(ctx context.Context, bookingID, driverID uuid.UUID) error {
	return s.repo.AssignDriver(ctx, bookingID, driverID)
}

func (s *Service) UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error {
	booking, err := s.repo.GetByID(ctx, bookingID)
	if err != nil {
		return err
	}

	sm := NewStateMachine()
	if err := sm.Transition(booking.Status, status); err != nil {
		return apperr.BadRequest(err.Error(), nil)
	}

	// The status observed above is passed as the expected value so the write is
	// a compare-and-swap. Without it, two concurrent transitions both pass the
	// state machine check and the second silently overwrites the first.
	return s.repo.UpdateStatus(ctx, bookingID, status, actorID, notes, booking.Status)
}

func (s *Service) CancelBooking(ctx context.Context, bookingID, customerID uuid.UUID, reason string) error {
	_, err := s.repo.GetForCustomer(ctx, bookingID, customerID)
	if err != nil {
		return err
	}

	if err := s.UpdateStatus(ctx, bookingID, models.BookingCancelled, customerID, reason); err != nil {
		return err
	}
	return nil
}

func (s *Service) CancelForDriver(ctx context.Context, bookingID, userID uuid.UUID, reason string) error {
	driver, err := s.driversSvc.GetDriverProfile(ctx, userID)
	if err != nil {
		return err
	}

	booking, err := s.repo.GetByID(ctx, bookingID)
	if err != nil {
		return err
	}
	if booking.DriverID == nil || *booking.DriverID != driver.ID {
		return apperr.Forbidden("you are not the driver of this booking")
	}

	// The audit actor is the user, not the driver profile row.
	if err := s.UpdateStatus(ctx, bookingID, models.BookingCancelledByDriver, userID, reason); err != nil {
		return err
	}
	return nil
}

// VerifyOTP confirms the handover code the customer gave the driver.
//
// customerID is the authenticated customer, used both to prove the booking is
// theirs and to count failed attempts. Previously the handler looked the
// booking up by id alone, so any authenticated user could submit guesses against
// any booking's code, and nothing stopped a brute force of the 6-digit space.
func (s *Service) VerifyOTP(ctx context.Context, bookingID, customerID uuid.UUID, otp string) error {
	booking, err := s.repo.GetForCustomer(ctx, bookingID, customerID)
	if err != nil {
		return err
	}
	if IsTerminal(booking.Status) {
		return apperr.Conflict("booking already closed", nil)
	}
	if booking.Status != models.BookingArrived {
		return apperr.Conflict("driver has not arrived yet", nil)
	}
	if booking.OTPCode == "" {
		return apperr.Conflict("no otp was issued for this booking", nil)
	}

	// Bound the guess space with a persistent counter on the booking. The OTP is
	// 6 digits, so an unthrottled endpoint is brute-forceable; the counter is
	// cleared by issuing a new code.
	if booking.OTPAttempts >= MaxOTPAttempts {
		return apperr.Forbidden("too many incorrect otp attempts, contact support")
	}

	// Constant-time comparison keeps the response time from leaking how many
	// leading digits were correct.
	if subtle.ConstantTimeCompare([]byte(booking.OTPCode), []byte(otp)) == 0 {
		if err := s.repo.IncrementOTPAttempts(ctx, bookingID); err != nil {
			return apperr.Wrap(500, "OTP_UPDATE_FAILED", "failed to record otp attempt", err, nil)
		}
		return apperr.BadRequest("invalid otp", nil)
	}

	// The counter records every guess, including a correct one, so the audit
	// trail reflects all attempts against this code. On success the booking
	// immediately moves to OTP_VERIFIED, so the remaining budget is moot.
	if err := s.repo.IncrementOTPAttempts(ctx, bookingID); err != nil {
		return apperr.Wrap(500, "OTP_UPDATE_FAILED", "failed to record otp attempt", err, nil)
	}
	return nil
}

func (s *Service) FindActiveForDriver(ctx context.Context, driverID uuid.UUID) (*models.Booking, error) {
	return s.repo.FindAssignedBooking(ctx, driverID)
}

// CancelBookingsForDriver closes every non-terminal booking assigned to a
// driver and returns how many were affected.
//
// This backs driver suspension. Without it, suspending a driver leaves their
// in-flight booking live: they can still start the trip, complete it, and
// collect the fare while their verification says SUSPENDED.
//
// Best-effort per booking — one that has already reached a terminal state is
// skipped rather than failing the whole sweep.
// CancelBookingsForDriver terminates every open booking assigned to a driver.
//
// It is used when an administrator suspends a driver, so every booking moves to
// CANCELLED_BY_ADMIN rather than CANCELLED_BY_DRIVER. That distinction is
// load-bearing twice over: an administrative revocation is not the driver's
// choice and must not feed their cancellation rate, and it has to be legal from
// the states past the vehicle handover, which is exactly where a suspension is
// most likely to land and where CANCELLED_BY_DRIVER is refused. Using the driver
// status here meant post-OTP bookings were silently skipped, leaving a suspended
// driver holding a live trip they could still complete and be paid for.
// SetCancellationReactions registers the post-cancellation cleanup.
func (s *Service) SetCancellationReactions(r CancellationReactions) { s.reactions = r }

func (s *Service) CancelBookingsForDriver(ctx context.Context, driverID, adminID uuid.UUID, reason string) (int, error) {
	active, err := s.repo.ListActiveForDriver(ctx, driverID, 50)
	if err != nil {
		return 0, apperr.Wrap(500, "DRIVER_BOOKINGS_FAILED", "failed to list driver bookings", err, nil)
	}

	cancelled := 0
	for i := range active {
		b := active[i]
		if err := s.UpdateStatus(ctx, b.ID, models.BookingCancelledByAdmin, adminID, reason); err != nil {
			// A concurrent terminal transition is expected and not a failure.
			slog.Warn("skipping booking that could not be cancelled for suspended driver",
				"booking_id", b.ID, "driver_id", driverID, "error", err)
			continue
		}
		cancelled++

		// Run the same cleanup a cancellation gets when it arrives over HTTP, so
		// the suspended driver's offers are withdrawn, both parties are told, and
		// the driver is not left in BUSY.
		if s.reactions != nil {
			closed, err := s.GetBooking(ctx, b.ID)
			if err != nil {
				slog.Warn("failed to reload cancelled booking for cleanup",
					"booking_id", b.ID, "error", err)
			} else if err := s.reactions.OnBookingCancelled(ctx, closed); err != nil {
				slog.Warn("cancellation cleanup failed for suspended driver's booking",
					"booking_id", b.ID, "error", err)
			}
		}
	}
	return cancelled, nil
}

func (s *Service) SetFinalPrice(ctx context.Context, bookingID uuid.UUID, finalPrice int) error {
	if err := s.repo.UpdateFinalPrice(ctx, bookingID, finalPrice); err != nil {
		return apperr.Wrap(500, "PRICE_UPDATE_FAILED", "failed to update final price", err, nil)
	}
	return nil
}

func generateOTP(length int) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	n := int(uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3])) % 1000000
	return fmt.Sprintf("%0*d", length, n), nil
}
