package trips

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/geolocation"
	"drivethrough/pkg/validator"
)

type WebSocketPublisher interface {
	BroadcastTripLocation(tripID, bookingID uuid.UUID, loc models.LocationUpdate)
	PublishTripEnded(tripID, bookingID uuid.UUID)
}

// BookingLifecycle is the slice of the bookings module that trips needs.
// Declaring it here keeps the dependency one-way: trips does not import
// bookings, so either module can be tested or replaced in isolation.
type BookingLifecycle interface {
	GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
	UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error
	SetFinalPrice(ctx context.Context, bookingID uuid.UUID, price int) error
}

// DriverLookup resolves an authenticated user to their driver profile. trips
// needs it because bookings.driver_id and trips.driver_id both reference
// drivers(id), while a JWT carries users(id) — the two identifier spaces are
// easy to confuse and the mismatch silently breaks every driver action.
type DriverLookup interface {
	GetDriverProfile(ctx context.Context, userID uuid.UUID) (*models.Driver, error)
}

// CompletionListener reacts once a booking reaches its terminal state. The app
// coordinator implements it to return the driver to the available pool; declared
// here so trips stays unaware of the coordinator.
type CompletionListener interface {
	OnBookingCompleted(ctx context.Context, booking *models.Booking) error
}

type Service struct {
	repo                 *Repository
	bookings             BookingLifecycle
	drivers              DriverLookup
	publisher            WebSocketPublisher
	onCompleted          CompletionListener
	baseDriverFeePerHour int
}

func NewService(repo *Repository, bookingsSvc BookingLifecycle, drivers DriverLookup, publisher WebSocketPublisher, baseDriverFeePerHour int) *Service {
	return &Service{
		repo:                 repo,
		bookings:             bookingsSvc,
		drivers:              drivers,
		publisher:            publisher,
		baseDriverFeePerHour: baseDriverFeePerHour,
	}
}

// SetCompletionListener registers the post-completion reaction.
func (s *Service) SetCompletionListener(l CompletionListener) {
	s.onCompleted = l
}

// DriverForUser maps a JWT subject to the driver profile used by booking and
// trip rows.
func (s *Service) DriverForUser(ctx context.Context, userID uuid.UUID) (*models.Driver, error) {
	return s.drivers.GetDriverProfile(ctx, userID)
}

// GetByID exposes a trip read for callers outside the module, currently the
// booking module resolving who may observe a trip over WebSocket. It performs no
// authorisation of its own; the caller decides what the reader is allowed to do
// with it.
func (s *Service) GetByID(ctx context.Context, tripID uuid.UUID) (*models.Trip, error) {
	return s.repo.GetByID(ctx, tripID)
}

type StartInput struct {
	BookingID     uuid.UUID
	DriverID      uuid.UUID
	ActorID       uuid.UUID
	StartOdometer int
	StartFuel     int
	PreInspection *models.VehicleInspection
}

func (s *Service) StartTrip(ctx context.Context, in StartInput) (*models.Trip, error) {
	booking, err := s.bookings.GetBooking(ctx, in.BookingID)
	if err != nil {
		return nil, err
	}

	if booking.DriverID == nil || *booking.DriverID != in.DriverID {
		return nil, apperr.Forbidden("you are not assigned to this booking")
	}

	if booking.Status != models.BookingOTPVerified {
		return nil, apperr.Conflict("booking must be OTP verified before starting", nil)
	}

	trip := &models.Trip{
		BookingID:     in.BookingID,
		DriverID:      in.DriverID,
		VehicleID:     booking.VehicleID,
		StartOdometer: in.StartOdometer,
		StartFuel:     in.StartFuel,
		Status:        "ACTIVE",
	}

	// A nil inspection is allowed; the pre-trip check is recorded when supplied.
	var insp *models.VehicleInspection
	if in.PreInspection != nil {
		i := *in.PreInspection
		i.Type = "PRE_TRIP"
		insp = &i
	}

	// The trip row and its inspection are written together. The booking status
	// flip is a separate transaction in the bookings module; if it fails, the
	// compensating delete below keeps the two consistent.
	if err := s.repo.CreateTripWithInspection(ctx, trip, insp); err != nil {
		// A concurrent start is a client-visible race, so its 409 is preserved
		// rather than being flattened into a 500.
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			return nil, err
		}
		return nil, apperr.Wrap(500, "TRIP_CREATE_FAILED", "failed to create trip", err, nil)
	}

	if err := s.bookings.UpdateStatus(ctx, in.BookingID, models.BookingStarted, in.ActorID, "trip started"); err != nil {
		slog.Error("trip started but booking status update failed, rolling back trip",
			"trip_id", trip.ID, "booking_id", in.BookingID, "error", err)
		if rerr := s.repo.DeleteTrip(ctx, trip.ID); rerr != nil {
			slog.Error("failed to roll back orphaned trip", "trip_id", trip.ID, "error", rerr)
			return nil, apperr.Wrap(500, "TRIP_CREATE_FAILED",
				"trip started but booking could not be updated; support required", err, nil)
		}
		return nil, err
	}

	return trip, nil
}

// TripAction identifies who is acting on a trip. DriverID is the driver profile
// id stored on the trip row; ActorID is the authenticated user recorded on the
// booking audit trail.
type TripAction struct {
	DriverID uuid.UUID
	ActorID  uuid.UUID
}

func (s *Service) PauseTrip(ctx context.Context, tripID uuid.UUID, actor TripAction) error {
	trip, err := s.repo.GetByID(ctx, tripID)
	if err != nil {
		return err
	}
	if trip.DriverID != actor.DriverID {
		return apperr.Forbidden("you are not the driver of this trip")
	}
	if trip.Status != "ACTIVE" {
		return apperr.Conflict("trip is not active", nil)
	}

	if err := s.repo.updateStatus(ctx, tripID, "PAUSED"); err != nil {
		return err
	}
	return s.bookings.UpdateStatus(ctx, trip.BookingID, models.BookingPaused, actor.ActorID, "trip paused")
}

func (s *Service) ResumeTrip(ctx context.Context, tripID uuid.UUID, actor TripAction) error {
	trip, err := s.repo.GetByID(ctx, tripID)
	if err != nil {
		return err
	}
	if trip.DriverID != actor.DriverID {
		return apperr.Forbidden("you are not the driver of this trip")
	}
	if trip.Status != "PAUSED" {
		return apperr.Conflict("trip is not paused", nil)
	}

	if err := s.repo.updateStatus(ctx, tripID, "ACTIVE"); err != nil {
		return err
	}
	return s.bookings.UpdateStatus(ctx, trip.BookingID, models.BookingResumed, actor.ActorID, "trip resumed")
}

type EndInput struct {
	TripID         uuid.UUID
	DriverID       uuid.UUID
	ActorID        uuid.UUID
	EndOdometer    int
	EndFuel        int
	PostInspection *models.VehicleInspection
}

type EndResult struct {
	Trip           models.Trip
	Distance       float64               `json:"distance_km"`
	Duration       time.Duration         `json:"duration"`
	FinalPrice     int                   `json:"final_price"`
	PriceBreakdown models.PriceBreakdown `json:"price_breakdown"`
}

func (s *Service) EndTrip(ctx context.Context, in EndInput) (*EndResult, error) {
	trip, err := s.repo.GetByID(ctx, in.TripID)
	if err != nil {
		return nil, err
	}
	if trip.DriverID != in.DriverID {
		return nil, apperr.Forbidden("you are not the driver of this trip")
	}

	booking, err := s.bookings.GetBooking(ctx, trip.BookingID)
	if err != nil {
		return nil, err
	}

	// The guard is on the booking, not the trip. A trip closed by a request that
	// then failed before the booking flip lands here again and is allowed to
	// finish the job; a booking already completed is a genuine duplicate.
	if booking.Status == models.BookingCompleted {
		return nil, apperr.Conflict("trip already completed", nil)
	}

	// Closing the trip and completing the booking are two separate writes, so a
	// failure between them leaves the trip COMPLETED with the booking still open.
	// The retry has to be able to finish that, but CompleteTrip only matches an
	// open trip and would reject the retry as a conflict — which is what actually
	// happened, stranding the booking in a non-terminal state that no endpoint
	// could advance. An already-closed trip therefore skips straight to the
	// booking side, which is the step that is actually outstanding.
	var distance float64

	if trip.Status != models.TripStatusCompleted {
		distance = s.tripDistance(ctx, trip.ID)

		if err := s.repo.CompleteTrip(ctx, trip.ID, in.EndOdometer, in.EndFuel, distance, in.PostInspection); err != nil {
			// A trip that is already closed means a concurrent request won the race.
			var appErr *apperr.AppError
			if errors.As(err, &appErr) {
				return nil, err
			}
			return nil, apperr.Wrap(500, "TRIP_COMPLETE_FAILED", "failed to complete trip", err, nil)
		}
	} else {
		// The trip was closed by an earlier attempt, so its recorded distance and
		// end readings are authoritative. Reusing them keeps the retry from
		// settling a different amount than the first attempt did.
		trip, err = s.repo.GetByID(ctx, trip.ID)
		if err != nil {
			return nil, err
		}
		distance = trip.TotalDistance
	}

	var finalPrice int
	duration := time.Since(trip.StartedAt)

	baseCost := s.baseDriverFeePerHour * booking.DurationHours
	extraMinutes := int(duration.Minutes()) - booking.DurationHours*60
	if extraMinutes > 15 {
		extraHours := (extraMinutes + 59) / 60
		baseCost += s.baseDriverFeePerHour * extraHours
	}

	finalPrice = baseCost + booking.PlatformFee + booking.ProtectionFee + booking.Tax

	// The final price is written before the status flip. Payment settlement reads
	// it when the booking completes, so the reverse order could complete a
	// booking with no price and settle the wrong amount. Both writes are
	// idempotent, so the recovery path above can safely repeat them.
	if err := s.bookings.SetFinalPrice(ctx, trip.BookingID, finalPrice); err != nil {
		return nil, err
	}

	if err := s.bookings.UpdateStatus(ctx, trip.BookingID, models.BookingCompleted, in.ActorID, "trip completed"); err != nil {
		return nil, err
	}

	// Returning the driver to the available pool is a cross-module reaction, so
	// it is best-effort here: a failure must not fail an already-completed trip.
	if s.onCompleted != nil {
		if booking, err := s.bookings.GetBooking(ctx, trip.BookingID); err == nil {
			if err := s.onCompleted.OnBookingCompleted(ctx, booking); err != nil {
				slog.Warn("failed to run post-completion reactions", "booking_id", trip.BookingID, "error", err)
			}
		}
	}

	if s.publisher != nil {
		s.publisher.PublishTripEnded(trip.ID, trip.BookingID)
	}

	completed := *trip
	completed.Status = "COMPLETED"
	completed.EndOdometer = &in.EndOdometer
	completed.EndFuel = &in.EndFuel

	return &EndResult{
		Trip:       completed,
		Distance:   distance / 1000.0,
		Duration:   duration,
		FinalPrice: finalPrice,
		PriceBreakdown: models.PriceBreakdown{
			DriverFee:  baseCost,
			Platform:   booking.PlatformFee,
			Protection: booking.ProtectionFee,
			Tax:        booking.Tax,
			Total:      finalPrice,
		},
	}, nil
}

func (s *Service) RecordLocation(ctx context.Context, tripID uuid.UUID, loc models.LocationUpdate, actor TripAction) error {
	trip, err := s.repo.GetByID(ctx, tripID)
	if err != nil {
		return err
	}
	if trip.DriverID != actor.DriverID {
		return apperr.Forbidden("you are not the driver of this trip")
	}
	if trip.Status != "ACTIVE" {
		return apperr.Conflict("trip not active", nil)
	}

	// Rejected here rather than by a database constraint so a malformed ping is
	// a 400 and does not poison the distance calculation with NaN.
	if math.IsNaN(loc.Latitude) || math.IsNaN(loc.Longitude) ||
		!validator.IsValidCoordinate(loc.Latitude, loc.Longitude) {
		return apperr.BadRequest("invalid coordinates", nil)
	}
	if loc.Speed < 0 || loc.Speed > 200 {
		return apperr.BadRequest("invalid speed", nil)
	}

	if err := s.repo.RecordLocation(ctx, tripID, loc); err != nil {
		return apperr.Wrap(500, "LOCATION_RECORD_FAILED", "failed to record location", err, nil)
	}

	if s.publisher != nil {
		s.publisher.BroadcastTripLocation(tripID, trip.BookingID, loc)
	}
	return nil
}

func (s *Service) tripDistance(ctx context.Context, tripID uuid.UUID) float64 {
	points, err := s.repo.TripPoints(ctx, tripID)
	if err != nil || len(points) < 2 {
		return 0
	}

	var meters float64
	prev := geolocation.Point{Lat: points[0].Latitude, Lng: points[0].Longitude}
	for _, p := range points[1:] {
		cur := geolocation.Point{Lat: p.Latitude, Lng: p.Longitude}
		meters += geolocation.HaversineMeters(prev, cur)
		prev = cur
	}
	return meters
}

func (s *Service) Inspections(ctx context.Context, tripID uuid.UUID, actor TripAction) ([]models.VehicleInspection, error) {
	trip, err := s.repo.GetByID(ctx, tripID)
	if err != nil {
		return nil, err
	}
	if trip.DriverID != actor.DriverID {
		return nil, apperr.Forbidden("you are not the driver of this trip")
	}
	return s.repo.InspectionsForTrip(ctx, tripID)
}

func (s *Service) CurrentTrip(ctx context.Context, bookingID uuid.UUID) (*models.Trip, error) {
	return s.repo.GetActiveByBooking(ctx, bookingID)
}

// GetActiveByBooking satisfies the bookings module's TripLookup so a caller
// holding only a booking id can resolve the trip to observe. The bookings module
// performs the party authorisation before calling this; the lookup itself is
// unscoped and must not be exposed without it.
func (s *Service) GetActiveByBooking(ctx context.Context, bookingID uuid.UUID) (*models.Trip, error) {
	return s.repo.GetActiveByBooking(ctx, bookingID)
}
