package drivers

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/location"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/validator"
)

// UserRolePromoter is the slice of the users module that drivers needs.
// Declared here so drivers does not reach into the users repository.
type UserRolePromoter interface {
	PromoteToDriver(ctx context.Context, userID uuid.UUID) error
}

// LocationStore is the geospatial cache the drivers module writes availability
// state to. Implemented by internal/location.
type LocationStore interface {
	UpdateDriverLocation(ctx context.Context, driverID uuid.UUID, lat, lng float64) error
	RemoveDriver(ctx context.Context, driverID uuid.UUID) error
	SetDriverState(ctx context.Context, driverID uuid.UUID, state location.DriverState, ttl time.Duration) error
}

type Service struct {
	repo       *Repository
	usersRepo  UserRolePromoter
	locationDB LocationStore
}

func NewService(repo *Repository, usersRepo UserRolePromoter, locationDB LocationStore) *Service {
	return &Service{repo: repo, usersRepo: usersRepo, locationDB: locationDB}
}

// driverStateTTL bounds how long an online driver's cached state survives
// without a refresh. Drivers post location on a regular cadence, so a gap
// longer than this means the driver is treated as offline.
const driverStateTTL = 2 * time.Minute

type RegistrationInput struct {
	UserID          uuid.UUID
	ExperienceYears int
	Capabilities    models.VehicleCapability
}

func (s *Service) RegisterDriver(ctx context.Context, input RegistrationInput) (*models.Driver, error) {
	if input.ExperienceYears < 0 || input.ExperienceYears > 60 {
		return nil, apperr.BadRequest("invalid experience years", nil)
	}

	driver, err := s.repo.CreateDriverProfile(ctx, input.UserID, input.ExperienceYears)
	if err != nil {
		return nil, apperr.Wrap(500, "DRIVER_REGISTRATION_FAILED", "failed to create driver profile", err, nil)
	}

	if err := s.repo.UpdateCapabilities(ctx, driver.ID, input.Capabilities); err != nil {
		return nil, apperr.Wrap(500, "CAPABILITY_UPDATE_FAILED", "failed to update capabilities", err, nil)
	}

	if err := s.usersRepo.PromoteToDriver(ctx, input.UserID); err != nil {
		return nil, apperr.Wrap(500, "ROLE_UPDATE_FAILED", "failed to update user role", err, nil)
	}

	return driver, nil
}

func (s *Service) GetDriverProfile(ctx context.Context, userID uuid.UUID) (*models.Driver, error) {
	return s.repo.GetByUserID(ctx, userID)
}

func (s *Service) GetDriverByID(ctx context.Context, driverID uuid.UUID) (*models.Driver, error) {
	return s.repo.GetByID(ctx, driverID)
}

func (s *Service) SetAvailability(ctx context.Context, driverID uuid.UUID, online bool) (*models.Driver, error) {
	driver, err := s.repo.GetByID(ctx, driverID)
	if err != nil {
		return nil, err
	}

	if driver.VerificationStatus != models.VerificationApproved {
		return nil, apperr.Forbidden("driver not approved for bookings")
	}

	// A driver mid-trip must not become matchable, otherwise they can be offered
	// a second booking while still driving a customer.
	if online && driver.Status == models.DriverStatusBusy {
		return nil, apperr.Conflict("finish your current trip before going online", nil)
	}

	status := models.DriverStatusOffline
	if online {
		status = models.DriverStatusOnline
	}

	if err := s.SetStatus(ctx, driverID, status); err != nil {
		return nil, err
	}

	// Going online also seeds the cached state that the live-location flow
	// updates. Postgres stays the source of truth; a Redis failure is logged
	// rather than surfaced, since the driver row is already correct.
	if s.locationDB != nil && online {
		lat, lng := 0.0, 0.0
		if driver.CurrentLocation != nil {
			lat, lng = driver.CurrentLocation.Latitude, driver.CurrentLocation.Longitude
		}
		if err := s.locationDB.SetDriverState(ctx, driverID, location.DriverState{
			Status: string(status),
			Lat:    lat,
			Lng:    lng,
		}, driverStateTTL); err != nil {
			slog.Warn("failed to publish driver state", "driver_id", driverID, "error", err)
		}
	}

	return s.repo.GetByID(ctx, driverID)
}

// SetStatus moves a driver between lifecycle states and keeps the Redis geo
// index in step: only ONLINE drivers are discoverable by matching.
func (s *Service) SetStatus(ctx context.Context, driverID uuid.UUID, status models.DriverStatus) error {
	if err := s.repo.UpdateStatus(ctx, driverID, status); err != nil {
		return apperr.Wrap(500, "STATUS_UPDATE_FAILED", "failed to update driver status", err, nil)
	}

	if s.locationDB != nil && status != models.DriverStatusOnline {
		if err := s.locationDB.RemoveDriver(ctx, driverID); err != nil {
			slog.Warn("failed to remove driver from geo index", "driver_id", driverID, "error", err)
		}
	}
	return nil
}

// Release returns a driver to the matchable pool after the system decides their
// trip is over (completed or cancelled).
//
// It deliberately bypasses the BUSY guard in SetAvailability: that guard exists
// to stop a *driver* from abandoning a customer mid-trip, but the coordinator
// calls this after the trip has already terminated. Routing completion through
// SetAvailability made every finished driver permanently stuck in BUSY, unable
// to accept their next booking. Verification is still enforced, so a suspended
// driver cannot be re-released into the pool.
func (s *Service) Release(ctx context.Context, driverID uuid.UUID) error {
	driver, err := s.repo.GetByID(ctx, driverID)
	if err != nil {
		return err
	}

	if driver.VerificationStatus != models.VerificationApproved {
		return apperr.Forbidden("driver not approved for bookings")
	}

	return s.SetStatus(ctx, driverID, models.DriverStatusOnline)
}

// ForceOffline takes a driver out of the matchable pool regardless of their
// verification status.
//
// It exists because SetAvailability deliberately refuses to act on a driver who
// is not APPROVED, which is the right rule when a driver is toggling their own
// availability but the wrong one for an administrator revoking it. Suspension
// sets the verification status first and then needs to set the status to
// OFFLINE; routing that through SetAvailability meant the call always failed
// with "not approved" and the error was discarded, leaving a suspended driver
// visibly ONLINE, still publishing their location, and still able to accept an
// offer that was already in flight.
func (s *Service) ForceOffline(ctx context.Context, driverID uuid.UUID) error {
	return s.SetStatus(ctx, driverID, models.DriverStatusOffline)
}

func (s *Service) UpdateCapabilities(ctx context.Context, driverID uuid.UUID, caps models.VehicleCapability) error {
	if err := s.repo.UpdateCapabilities(ctx, driverID, caps); err != nil {
		return apperr.Wrap(500, "CAPABILITY_UPDATE_FAILED", "failed to update capabilities", err, nil)
	}
	return nil
}

func (s *Service) UpdateLocation(ctx context.Context, driverID uuid.UUID, lat, lng float64) error {
	if !validator.IsValidCoordinate(lat, lng) {
		return apperr.BadRequest("invalid coordinates", nil)
	}
	if err := s.repo.UpdateLocation(ctx, driverID, lat, lng); err != nil {
		return apperr.Wrap(500, "LOCATION_UPDATE_FAILED", "failed to update location", err, nil)
	}

	// Feed the Redis GEO index used for fast nearby-driver lookups.
	if s.locationDB != nil {
		if err := s.locationDB.UpdateDriverLocation(ctx, driverID, lat, lng); err != nil {
			slog.Warn("failed to update geo index", "driver_id", driverID, "error", err)
		}
	}
	return nil
}

type DocumentInput struct {
	DriverID       uuid.UUID
	Type           models.DocumentType
	DocumentNumber string
	ExpiryDate     *time.Time
	DocumentURL    string
}

func (s *Service) SubmitDocument(ctx context.Context, input DocumentInput) error {
	if input.DocumentNumber == "" {
		return apperr.BadRequest("document number required", nil)
	}

	doc := &models.DriverDocument{
		DriverID:       input.DriverID,
		Type:           input.Type,
		DocumentNumber: input.DocumentNumber,
		ExpiryDate:     input.ExpiryDate,
		DocumentURL:    input.DocumentURL,
	}
	if err := s.repo.AddDocument(ctx, doc); err != nil {
		return apperr.Wrap(500, "DOCUMENT_SUBMIT_FAILED", "failed to submit document", err, nil)
	}

	if err := s.repo.SetVerificationStatus(ctx, input.DriverID, models.VerificationSubmitted); err != nil {
		return apperr.Wrap(500, "STATUS_UPDATE_FAILED", "failed to update verification status", err, nil)
	}
	return nil
}

func (s *Service) ListDocuments(ctx context.Context, driverID uuid.UUID) ([]models.DriverDocument, error) {
	return s.repo.ListDocuments(ctx, driverID)
}

// CreditEarning records what a driver earned for a ride. It is exactly-once per
// booking, so callers may retry freely after a partial failure.
func (s *Service) CreditEarning(ctx context.Context, driverID, bookingID uuid.UUID, amount int) error {
	return s.repo.CreditEarning(ctx, driverID, bookingID, amount)
}

// DriverUserID maps a driver profile to its owning user account. Callers that
// address a user (notifications, device tokens) need this, because booking and
// trip rows reference drivers(id) while everything user-facing references
// users(id).
func (s *Service) DriverUserID(ctx context.Context, driverID uuid.UUID) (uuid.UUID, error) {
	driver, err := s.repo.GetByID(ctx, driverID)
	if err != nil {
		return uuid.Nil, err
	}
	return driver.UserID, nil
}

func (s *Service) SetVerification(ctx context.Context, driverID uuid.UUID, status models.VerificationStatus) error {
	return s.repo.SetVerificationStatus(ctx, driverID, status)
}

func (s *Service) VerifyDocument(ctx context.Context, docID uuid.UUID, approved bool) error {
	return s.repo.VerifyDocument(ctx, docID, approved)
}

func (s *Service) ListPendingDrivers(ctx context.Context, limit, offset int) ([]models.Driver, error) {
	return s.repo.ListPending(ctx, limit, offset)
}

func (s *Service) NearbyOnline(ctx context.Context, lat, lng float64, radiusMeters int, caps models.VehicleCapability) ([]models.Driver, error) {
	return s.repo.NearbyOnline(ctx, lat, lng, radiusMeters, caps)
}

func (s *Service) RecordOffer(ctx context.Context, driverID uuid.UUID, accepted bool) error {
	return s.repo.RecordOffer(ctx, driverID, accepted)
}

func (s *Service) RecordCancellation(ctx context.Context, driverID uuid.UUID) error {
	return s.repo.RecordDriverCancellation(ctx, driverID)
}

// RecordNoShow increments the driver's no-show count.
func (s *Service) RecordNoShow(ctx context.Context, driverID uuid.UUID) error {
	return s.repo.RecordNoShow(ctx, driverID)
}

// Earnings returns the caller's running total alongside their per-booking
// ledger, resolved from the authenticated user rather than a client-supplied
// driver id.
func (s *Service) Earnings(ctx context.Context, userID uuid.UUID, limit, offset int) (int, []EarningEntry, error) {
	driver, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return 0, nil, err
	}
	entries, err := s.repo.EarningsLedger(ctx, driver.ID, limit, offset)
	if err != nil {
		return 0, nil, err
	}
	return driver.EarningsTotal, entries, nil
}
