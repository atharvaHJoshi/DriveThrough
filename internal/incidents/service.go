package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/models"
	"drivethrough/internal/sms"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/validator"
)

type BookingAccessor interface {
	GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
	UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error
}

// TripLookup resolves a trip to its booking, injected to keep incidents from
// importing trips.
type TripLookup interface {
	GetByID(ctx context.Context, tripID uuid.UUID) (*models.Trip, error)
}

// DriverLookup resolves a driver profile to its owning user, so the assigned
// driver counts as a party to the trip. The driver is as exposed to danger as
// the customer, and in practice more so.
type DriverLookup interface {
	GetByID(ctx context.Context, driverID uuid.UUID) (*models.Driver, error)
}

type EmergencyNotifier interface {
	Notify(ctx context.Context, userID uuid.UUID, notifType models.NotificationType, title, body string, data map[string]string) error
}

type Service struct {
	repo     *Repository
	bookings BookingAccessor
	trips    TripLookup
	drivers  DriverLookup
	notifier EmergencyNotifier
	sms      sms.Sender
	// smsTimeout bounds each emergency alert so a slow gateway cannot stall the
	// SOS response, which has already been persisted by this point.
	smsTimeout time.Duration
}

func NewService(repo *Repository, bookingsSvc BookingAccessor, notifier EmergencyNotifier, smsSender sms.Sender) *Service {
	if smsSender == nil {
		smsSender = sms.NewSender(sms.Config{Provider: "log"}, nil)
	}
	return &Service{
		repo:       repo,
		bookings:   bookingsSvc,
		notifier:   notifier,
		sms:        smsSender,
		smsTimeout: 5 * time.Second,
	}
}

// SetTripLookup injects the trip reader used for SOS scope and escalation.
func (s *Service) SetTripLookup(t TripLookup) { s.trips = t }

// SetDriverLookup injects the driver reader used to admit the assigned driver as
// a party to the trip.
func (s *Service) SetDriverLookup(d DriverLookup) { s.drivers = d }

// SetSMSTimeout bounds how long a single emergency-contact alert may take.
//
// This is per contact, not per incident: with several registered contacts a
// shared budget meant a slow gateway consumed the whole window and the later
// numbers were never dialled. The value is configurable because a gateway that
// is merely slow should not silently suppress the rest of an alert list.
func (s *Service) SetSMSTimeout(d time.Duration) {
	if d > 0 {
		s.smsTimeout = d
	}
}

type SOSInput struct {
	UserID    uuid.UUID
	TripID    *uuid.UUID
	Latitude  float64
	Longitude float64
	Kind      string
}

func (s *Service) TriggerSOS(ctx context.Context, in SOSInput) (*models.SOS, error) {
	if !validator.IsValidCoordinate(in.Latitude, in.Longitude) {
		return nil, apperr.BadRequest("invalid coordinates", nil)
	}

	// A second tap on the SOS button must not open a second incident, and must
	// not look like a failure to someone in an emergency. The existing alert is
	// returned as-is so the client can confirm that help is already being
	// dispatched.
	//
	// A lookup failure is distinguished from "no active alert": treating a
	// database error as "no active SOS" would open a duplicate incident and dial
	// the emergency contacts twice, so anything other than a clean not-found
	// aborts the request.
	active, err := s.repo.ActiveSOS(ctx, in.UserID)
	switch {
	case err == nil:
		return active, nil
	case apperr.IsNotFound(err):
		// No alert in flight; fall through and raise one.
	default:
		return nil, apperr.Wrap(500, "SOS_LOOKUP_FAILED", "failed to check for an active sos", err, nil)
	}

	if in.Kind == "" {
		in.Kind = "EMERGENCY"
	}

	sos := &models.SOS{
		UserID:    in.UserID,
		TripID:    in.TripID,
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
		Kind:      in.Kind,
		Status:    "OPEN",
	}

	if in.TripID != nil && s.bookings != nil {
		// An SOS may only be raised against a trip the caller is a party to.
		// This is enforced rather than logged: an unverified trigger would let any
		// authenticated user SMS a stranger's emergency contacts and page support,
		// which is both an abuse channel and a denial of service on the very queue
		// a real emergency depends on. A genuine emergency is never blocked by
		// this check, because the customer and the assigned driver are both
		// admitted, and a caller with no trip can always raise a personal SOS.
		if err := s.assertTripParticipant(ctx, *in.TripID, in.UserID); err != nil {
			return nil, err
		}
	}

	if err := s.repo.CreateSOS(ctx, sos); err != nil {
		return nil, apperr.Wrap(500, "SOS_CREATE_FAILED", "failed to create sos", err, nil)
	}

	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, in.UserID, models.NotifSOS,
			"SOS triggered",
			"Help is on the way. Your live location and trip details have been shared with our support team.",
			map[string]string{"sos_id": sos.ID.String()},
		)
	}

	s.alertEmergencyContacts(ctx, in.UserID, sos)

	// Flag the booking so support sees it in the operational queue alongside
	// other escalations.
	if in.TripID != nil && s.bookings != nil {
		s.escalateBooking(ctx, *in.TripID, in.UserID, sos)
	}

	return sos, nil
}

func (s *Service) assertTripParticipant(ctx context.Context, tripID, userID uuid.UUID) error {
	if s.trips == nil || s.bookings == nil {
		// Without the collaborators the check cannot be satisfied. Failing closed
		// is the only safe option: a skipped check is an open SOS endpoint.
		return apperr.Wrap(500, "SOS_SCOPE_UNAVAILABLE", "unable to verify trip access", nil, nil)
	}
	trip, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		return err
	}
	return s.assertBookingParty(ctx, trip.BookingID, userID, trip.DriverID)
}

// assertBookingParty admits the customer who booked the ride and the driver
// assigned to it. Both are equally entitled to call for help.
func (s *Service) assertBookingParty(ctx context.Context, bookingID, userID, driverID uuid.UUID) error {
	booking, err := s.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return err
	}
	if booking.CustomerID == userID {
		return nil
	}

	// The trip carries the driver profile id, while the caller is a user id, so
	// the profile is resolved rather than compared directly. Without this the
	// assigned driver could never raise an SOS on their own ride.
	if driverID != uuid.Nil && s.drivers != nil {
		driver, err := s.drivers.GetByID(ctx, driverID)
		if err != nil {
			return err
		}
		if driver.UserID == userID {
			return nil
		}
	}

	return apperr.Forbidden("you are not a party to this trip")
}

// alertEmergencyContacts notifies the user's registered emergency contacts.
//
// This is the entire point of collecting them: previously the contacts were
// fetched and the result discarded, so an SOS reached nobody outside the app.
func (s *Service) alertEmergencyContacts(ctx context.Context, userID uuid.UUID, sos *models.SOS) {
	contacts, err := s.repo.ListEmergencyContacts(ctx, userID)
	if err != nil {
		slog.Error("failed to load emergency contacts for sos", "user_id", userID, "sos_id", sos.ID, "error", err)
		return
	}
	if len(contacts) == 0 {
		slog.Warn("sos raised by a user with no emergency contacts", "user_id", userID, "sos_id", sos.ID)
		return
	}

	// The request context is likely already cancelled by the time we get here on
	// a client disconnect, so alerts run on a detached context.

	// The location is included deliberately. The whole purpose of a registered
	// emergency contact is that they can actually find the person, and a coarse
	// reference ("somewhere in the city") is not actionable during an emergency.
	// The user opted these numbers in explicitly for this event, and the message
	// carries a short reference so a recipient can be traced to the exact incident
	// without the SMS itself being a tracking record.
	message := fmt.Sprintf(
		"DriveThrough SOS: an emergency was triggered in app at %.4f, %.4f. Please contact them. Reference %s.",
		sos.Latitude, sos.Longitude, sos.ID.String()[:8],
	)

	// Each contact gets its own budget. A single shared deadline meant a slow
	// gateway call for the first number consumed the whole window and silently
	// skipped every remaining contact, which is precisely the situation where a
	// partial alert list is most dangerous.
	for _, c := range contacts {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.smsTimeout)
		err := s.sms.Send(sendCtx, c.Phone, message)
		cancel()
		if err != nil {
			slog.Error("failed to alert emergency contact", "sos_id", sos.ID, "contact_id", c.ID, "error", err)
			continue
		}
		slog.Info("emergency contact alerted", "sos_id", sos.ID, "contact_id", c.ID)
	}
}

// escalateBooking moves the booking into the support queue so the SOS is
// visible to operations alongside the booking itself.
func (s *Service) escalateBooking(ctx context.Context, tripID, userID uuid.UUID, sos *models.SOS) {
	trip, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		return
	}
	// The status is already terminal in some flows, where escalation is illegal;
	// that is expected and not worth failing the request over.
	if err := s.bookings.UpdateStatus(ctx, trip.BookingID, models.BookingSupportEscalated, userID,
		"SOS triggered: "+sos.ID.String()); err != nil {
		slog.Warn("failed to escalate booking for sos", "booking_id", trip.BookingID, "sos_id", sos.ID, "error", err)
	}
}

func (s *Service) ResolveSOS(ctx context.Context, sosID, adminID uuid.UUID, note string) error {
	return s.repo.ResolveSOS(ctx, sosID, adminID, note)
}

func (s *Service) ReportIncident(ctx context.Context, in *models.Incident) (*models.Incident, error) {
	if in.Description == "" {
		return nil, apperr.BadRequest("description is required", nil)
	}
	if in.Type == "" {
		in.Type = models.IncidentOther
	}
	if in.Severity == "" {
		in.Severity = models.SeverityMedium
	}

	if err := s.repo.CreateIncident(ctx, in); err != nil {
		return nil, apperr.Wrap(500, "INCIDENT_CREATE_FAILED", "failed to create incident", err, nil)
	}

	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, in.ReportedBy, models.NotifSOS,
			"Incident reported",
			fmt.Sprintf("Your %s incident (#%s) has been logged. Our team is reviewing it.", string(in.Type), in.ID.String()[:8]),
			map[string]string{"incident_id": in.ID.String()},
		)
	}

	return in, nil
}

func (s *Service) List(ctx context.Context, status string, limit, offset int) ([]models.Incident, error) {
	return s.repo.ListIncidents(ctx, status, limit, offset)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*models.Incident, error) {
	return s.repo.GetIncident(ctx, id)
}

// UpdateStatus advances an incident through its review workflow.
//
// The status is validated here so an unsupported value is a 400 rather than a
// CHECK violation surfacing to the operator as a 500.
func (s *Service) UpdateStatus(ctx context.Context, id uuid.UUID, status, resolution string, adminID uuid.UUID) error {
	switch status {
	case "OPEN", "INVESTIGATING", "RESOLVED", "CLOSED":
	default:
		return apperr.BadRequest("invalid incident status", nil)
	}
	if err := s.repo.UpdateIncidentStatus(ctx, id, status, resolution, adminID); err != nil {
		return err
	}

	// The SOS is only closed out once the case reaches a terminal state.
	// Resolving it on every status change meant an admin moving a case to
	// INVESTIGATING — actively working an emergency — silently closed the
	// emergency record, so the open-alert view disagreed with the response that
	// was still in progress.
	if status != "RESOLVED" && status != "CLOSED" {
		return nil
	}

	incident, err := s.repo.GetIncident(ctx, id)
	if err != nil || incident.TripID == nil {
		return nil
	}
	sos, err := s.repo.ActiveSOSByTrip(ctx, *incident.TripID)
	if err == nil && sos != nil {
		if err := s.repo.ResolveSOS(ctx, sos.ID, adminID, resolution); err != nil {
			slog.Warn("failed to close sos for resolved incident", "incident_id", id, "error", err)
		}
	}
	return nil
}

func (s *Service) ListContacts(ctx context.Context, userID uuid.UUID) ([]models.EmergencyContact, error) {
	return s.repo.ListEmergencyContacts(ctx, userID)
}

func (s *Service) AddContact(ctx context.Context, userID uuid.UUID, name, phone, relation string) (*models.EmergencyContact, error) {
	if !validator.IsValidPhone(phone) {
		return nil, apperr.BadRequest("invalid phone number", nil)
	}
	if name == "" {
		return nil, apperr.BadRequest("name is required", nil)
	}

	c := &models.EmergencyContact{UserID: userID, Name: name, Phone: phone, Relation: relation}
	if err := s.repo.AddEmergencyContact(ctx, c); err != nil {
		return nil, apperr.Wrap(500, "CONTACT_ADD_FAILED", "failed to add emergency contact", err, nil)
	}
	return c, nil
}
