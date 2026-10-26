package admin

import (
	"context"

	"github.com/google/uuid"

	"log/slog"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type DriverOps interface {
	GetDriverByID(ctx context.Context, id uuid.UUID) (*models.Driver, error)
	SetVerification(ctx context.Context, driverID uuid.UUID, status models.VerificationStatus) error
	VerifyDocument(ctx context.Context, docID uuid.UUID, approved bool) error
	ListPendingDrivers(ctx context.Context, limit, offset int) ([]models.Driver, error)
	SetAvailability(ctx context.Context, driverID uuid.UUID, online bool) (*models.Driver, error)
	ForceOffline(ctx context.Context, driverID uuid.UUID) error
	ListDocuments(ctx context.Context, driverID uuid.UUID) ([]models.DriverDocument, error)
}

type IncidentOps interface {
	List(ctx context.Context, status string, limit, offset int) ([]models.Incident, error)
	Get(ctx context.Context, id uuid.UUID) (*models.Incident, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status, resolution string, adminID uuid.UUID) error
}

// BookingOps is the slice of the bookings module admin needs to close out a
// suspended driver's work. Declared here so admin does not import bookings.
type BookingOps interface {
	CancelBookingsForDriver(ctx context.Context, driverID, adminID uuid.UUID, reason string) (int, error)
}

type Service struct {
	repo      *Repository
	drivers   DriverOps
	incidents IncidentOps
	bookings  BookingOps
}

func NewService(repo *Repository, driverService DriverOps, incidentService IncidentOps, bookingService BookingOps) *Service {
	return &Service{repo: repo, drivers: driverService, incidents: incidentService, bookings: bookingService}
}

func (s *Service) Dashboard(ctx context.Context) (*DashboardStats, error) {
	stats, err := s.repo.Dashboard(ctx)
	if err != nil {
		return nil, apperr.Wrap(500, "DASHBOARD_FAILED", "failed to load dashboard", err, nil)
	}
	return stats, nil
}

func (s *Service) PendingDrivers(ctx context.Context, limit, offset int) ([]models.Driver, error) {
	return s.drivers.ListPendingDrivers(ctx, limit, offset)
}

func (s *Service) DriverDetail(ctx context.Context, driverID uuid.UUID) (*models.Driver, []models.DriverDocument, error) {
	driver, err := s.drivers.GetDriverByID(ctx, driverID)
	if err != nil {
		return nil, nil, err
	}
	docs, err := s.repo.DriverDocs(ctx, driverID)
	if err != nil {
		return nil, nil, apperr.Wrap(500, "DOCS_FAILED", "failed to load documents", err, nil)
	}
	return driver, docs, nil
}

func (s *Service) VerifyDriver(ctx context.Context, driverID uuid.UUID, approved bool, reason string) error {
	driver, err := s.drivers.GetDriverByID(ctx, driverID)
	if err != nil {
		return err
	}

	if approved {
		docs, err := s.drivers.ListDocuments(ctx, driverID)
		if err != nil {
			return err
		}
		if !hasVerifiedLicence(docs) {
			return apperr.Conflict("driver has no verified driving licence", nil)
		}
	}

	status := models.VerificationRejected
	if approved {
		status = models.VerificationApproved
	}
	if err := s.drivers.SetVerification(ctx, driver.ID, status); err != nil {
		return apperr.Wrap(500, "VERIFY_FAILED", "failed to update verification status", err, nil)
	}
	return nil
}

func hasVerifiedLicence(docs []models.DriverDocument) bool {
	for _, d := range docs {
		if d.Type == models.DocDrivingLic && d.Status == "VERIFIED" {
			return true
		}
	}
	return false
}

func (s *Service) SuspendDriver(ctx context.Context, driverID uuid.UUID, suspend bool) error {
	driver, err := s.drivers.GetDriverByID(ctx, driverID)
	if err != nil {
		return err
	}

	status := models.VerificationApproved
	if suspend {
		status = models.VerificationSuspended
	}
	if err := s.drivers.SetVerification(ctx, driver.ID, status); err != nil {
		return apperr.Wrap(500, "SUSPEND_FAILED", "failed to update driver status", err, nil)
	}

	if !suspend {
		return nil
	}

	// Close out any booking still assigned to them. Setting them offline is not
	// enough: a suspended driver could otherwise still start and complete the
	// trip they were mid-way through and collect the fare.
	if s.bookings != nil {
		cancelled, err := s.bookings.CancelBookingsForDriver(ctx, driver.ID, uuid.Nil,
			"driver suspended by administrator")
		if err != nil {
			return apperr.Wrap(500, "SUSPEND_FAILED", "failed to close suspended driver's bookings", err, nil)
		}
		if cancelled > 0 {
			slog.Info("closed bookings for suspended driver", "driver_id", driver.ID, "bookings", cancelled)
		}
	}

	// Take them out of the matchable pool. This has to bypass the verification
	// gate in SetAvailability, which refuses to act on a driver who is no longer
	// APPROVED — so calling it here always failed and left a suspended driver
	// ONLINE, still streaming their location and still able to accept an offer
	// that was already in flight.
	if err := s.drivers.ForceOffline(ctx, driver.ID); err != nil {
		// The suspension itself has already been committed, so this is reported
		// rather than swallowed: an operator needs to know the driver is still
		// shown as online.
		return apperr.Wrap(500, "SUSPEND_FAILED", "driver was suspended but could not be taken offline", err, nil)
	}
	return nil
}

func (s *Service) VerifyDocument(ctx context.Context, docID uuid.UUID, approved bool) error {
	return s.drivers.VerifyDocument(ctx, docID, approved)
}

func (s *Service) Bookings(ctx context.Context, status string, limit, offset int) ([]models.Booking, error) {
	return s.repo.Bookings(ctx, status, limit, offset)
}

func (s *Service) DriverEarnings(ctx context.Context, driverID uuid.UUID, limit, offset int) ([]models.DriverEarning, error) {
	return s.repo.DriverEarnings(ctx, driverID, limit, offset)
}

func (s *Service) Incidents(ctx context.Context, status string, limit, offset int) ([]models.Incident, error) {
	return s.incidents.List(ctx, status, limit, offset)
}

func (s *Service) IncidentDetail(ctx context.Context, id uuid.UUID) (*models.Incident, error) {
	return s.incidents.Get(ctx, id)
}

func (s *Service) ResolveIncident(ctx context.Context, id uuid.UUID, status, resolution string, adminID uuid.UUID) error {
	return s.incidents.UpdateStatus(ctx, id, status, resolution, adminID)
}
