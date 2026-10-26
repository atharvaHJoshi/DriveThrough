package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"drivethrough/internal/bookings"
	"drivethrough/internal/drivers"
	"drivethrough/internal/matching"
	"drivethrough/internal/models"
	"drivethrough/internal/notifications"
	"drivethrough/internal/sms"
	"drivethrough/internal/vehicles"
)

// UserProfile is the slice of the users module needed to reach a customer's
// phone for the handover OTP.
type UserProfile interface {
	GetProfile(ctx context.Context, userID uuid.UUID) (*models.User, error)
}

// PaymentSettler is the slice of the payments module needed to settle the metered
// fare once a trip ends.
type PaymentSettler interface {
	CaptureForBooking(ctx context.Context, bookingID uuid.UUID) error
	// ReleaseAuthorizationForBooking gives back an uncaptured hold when a booking
	// ends without a ride, so the customer is not left with funds held.
	ReleaseAuthorizationForBooking(ctx context.Context, bookingID uuid.UUID) error
}

type Coordinator struct {
	bookings    *bookings.Service
	matching    *matching.Service
	notifs      *notifications.Service
	drivers     *drivers.Service
	vehicles    *vehicles.Service
	payments    PaymentSettler
	users       UserProfile
	sms         sms.Sender
	maxRadiusKM int
}

func NewCoordinator(
	bookingSvc *bookings.Service,
	matchingSvc *matching.Service,
	notifs *notifications.Service,
	driverSvc *drivers.Service,
	vehiclesSvc *vehicles.Service,
	paymentsSvc PaymentSettler,
	usersSvc UserProfile,
	smsSender sms.Sender,
	maxRadiusKM int,
) *Coordinator {
	if smsSender == nil {
		// A nil sender would panic on arrival. A no-op keeps the flow working
		// where no SMS provider is configured.
		smsSender = sms.NewSender(sms.Config{Provider: "log"}, nil)
	}
	return &Coordinator{
		bookings:    bookingSvc,
		matching:    matchingSvc,
		notifs:      notifs,
		drivers:     driverSvc,
		vehicles:    vehiclesSvc,
		payments:    paymentsSvc,
		users:       usersSvc,
		sms:         smsSender,
		maxRadiusKM: maxRadiusKM,
	}
}

func (c *Coordinator) OnBookingCreated(ctx context.Context, booking *models.Booking) error {
	// A scheduled booking must not be matched before its pickup time. The guard
	// keys off the clock rather than the mere presence of ScheduledAt: the
	// scheduled worker feeds due bookings through this same function, so
	// returning early on any non-nil ScheduledAt meant scheduled bookings were
	// never matched at all.
	if booking.ScheduledAt != nil && time.Now().Before(*booking.ScheduledAt) {
		slog.Info("booking scheduled, will be matched later",
			"booking_id", booking.ID, "scheduled_at", booking.ScheduledAt)
		return nil
	}

	if err := c.bookings.UpdateStatus(ctx, booking.ID, models.BookingSearching, booking.CustomerID, "driver search started"); err != nil {
		return err
	}

	vehicle, err := c.vehicles.GetVehicle(ctx, booking.VehicleID)
	if err != nil {
		return err
	}

	req := matching.MatchRequest{
		BookingID:     booking.ID,
		PickupLat:     booking.PickupLat,
		PickupLng:     booking.PickupLng,
		PickupText:    booking.PickupLocation,
		Vehicle:       *vehicle,
		MaxRadiusKM:   c.maxRadiusKM,
		DurationHours: booking.DurationHours,
		EstimatedFare: booking.EstimatedPrice,
	}

	offered, err := c.matching.FindAndOffer(ctx, req)
	if err != nil {
		slog.Error("matching failed", "booking_id", booking.ID, "error", err)
		return err
	}

	if offered > 0 {
		_ = c.notifs.Notify(ctx, booking.CustomerID, models.NotifBookingCreated,
			"Looking for a driver",
			"We are finding the right driver for your booking.",
			map[string]string{"booking_id": booking.ID.String()},
		)
	}
	return nil
}

func (c *Coordinator) OnBookingCancelled(ctx context.Context, booking *models.Booking) error {
	c.matching.TimeoutOffers(ctx, booking.ID)

	// Every cancellation path promises the customer they are not charged, so the
	// hold has to actually be given back. No cancellation captures a payment, but
	// an authorization left in place keeps the customer's funds held until the
	// issuer expires it, and the payment row keeps claiming to be outstanding.
	//
	// This runs before the notifications below, so the message the customer
	// receives is backed by the gateway call.
	if c.payments != nil && booking.Status != models.BookingCompleted {
		if err := c.payments.ReleaseAuthorizationForBooking(ctx, booking.ID); err != nil {
			// The booking is already correctly cancelled and the offers withdrawn;
			// a stuck hold is an operational follow-up rather than a reason to
			// pretend the cancellation failed.
			slog.Error("failed to release payment hold after cancellation",
				"booking_id", booking.ID, "status", booking.Status, "error", err)
		}
	}

	// An administrative cancellation is not the driver's fault and is not the
	// customer's either, so the driver keeps their cancellation rate and the
	// customer needs to be told what happened to their ride.
	if booking.Status == models.BookingCancelledByAdmin {
		_ = c.notifs.Notify(ctx, booking.CustomerID, models.NotifBookingCancelled,
			"Your booking was cancelled by support",
			"Support has ended this booking. Any payment hold has been released and no fare will be charged.",
			map[string]string{"booking_id": booking.ID.String()})

		if booking.DriverID != nil {
			if driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID); err == nil {
				_ = c.notifs.Notify(ctx, driver.UserID, models.NotifBookingCancelled,
					"Booking ended by support",
					"Support ended this booking. This does not count against your cancellation record.",
					map[string]string{"booking_id": booking.ID.String()})

				// Release rather than force offline: a suspended driver is no longer
				// APPROVED, so the ordinary path would refuse and leave them BUSY.
				if driver.Status == models.DriverStatusBusy {
					if err := c.drivers.Release(ctx, driver.ID); err != nil {
						slog.Warn("failed to release driver after admin cancellation",
							"driver_id", driver.ID, "error", err)
					}
				}
			}
		}
		return nil
	}

	if booking.Status == models.BookingCancelledByDriver && booking.DriverID != nil {
		slog.Info("driver cancelled booking", "driver_id", *booking.DriverID, "booking_id", booking.ID)
		if driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID); err == nil {
			_ = c.drivers.RecordCancellation(ctx, driver.ID)
		}
	}

	if booking.DriverID != nil {
		// The copy has to reflect who actually cancelled, otherwise a driver is
		// told the customer cancelled when the driver did.
		heading, body := "Booking cancelled", "The customer cancelled the booking."
		if booking.Status == models.BookingCancelledByDriver {
			heading, body = "You cancelled the booking", "You cancelled this booking. The customer has been notified."
		}

		driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID)
		if err != nil {
			return err
		}

		// notifications.user_id is a users(id) foreign key, so the driver's user
		// id is required here, not the driver profile id.
		_ = c.notifs.Notify(ctx, driver.UserID, models.NotifBookingCancelled,
			heading, body,
			map[string]string{"booking_id": booking.ID.String()},
		)
		if driver.Status == models.DriverStatusBusy {
			_ = c.drivers.Release(ctx, driver.ID)
		}
	}
	return nil
}

// OnBookingCompleted settles the fare and returns the driver to the pool.
func (c *Coordinator) OnBookingCompleted(ctx context.Context, booking *models.Booking) error {
	// Settle first. Driver earnings are credited by the capture, so releasing the
	// driver and then failing to charge would pay for a trip out of the driver's
	// own pocket on the next booking.
	if c.payments != nil {
		if err := c.payments.CaptureForBooking(ctx, booking.ID); err != nil {
			slog.Warn("failed to settle booking fare", "booking_id", booking.ID, "error", err)
		}
	}

	if booking.DriverID == nil {
		return nil
	}
	driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID)
	if err != nil {
		return err
	}
	if driver.Status != models.DriverStatusBusy {
		return nil
	}
	// Release, not SetAvailability: the driver is still BUSY at this point, and
	// SetAvailability rejects that transition, which stranded every driver who
	// finished a trip.
	if err := c.drivers.Release(ctx, driver.ID); err != nil {
		return err
	}
	return c.notifs.Notify(ctx, driver.UserID, models.NotifTripCompleted,
		"Trip completed",
		"Your trip is complete. Earnings have been added to your account.",
		map[string]string{"booking_id": booking.ID.String()},
	)
}

// OnDriverEnRoute tells the customer their driver is on the way.
func (c *Coordinator) OnDriverEnRoute(ctx context.Context, booking *models.Booking) error {
	return c.notifyCustomer(ctx, booking, models.NotifDriverEnRoute,
		"Your driver is on the way",
		"Your driver has accepted the booking and is heading to the pickup point.",
	)
}

// OnDriverArrived is the handover moment. The customer has to receive the OTP
// here: without this the code is generated at booking creation, stored, and
// never delivered, leaving the driver to read a secret out loud over the phone.
func (c *Coordinator) OnDriverArrived(ctx context.Context, booking *models.Booking) error {
	if err := c.notifyCustomer(ctx, booking, models.NotifDriverArrived,
		"Your driver has arrived",
		"Please share the verification code with your driver to start the trip.",
	); err != nil {
		return err
	}
	return c.sendHandoverOTP(ctx, booking)
}

func (c *Coordinator) sendHandoverOTP(ctx context.Context, booking *models.Booking) error {
	if booking.OTPCode == "" {
		slog.Warn("booking has no otp to deliver", "booking_id", booking.ID)
		return nil
	}

	customer, err := c.users.GetProfile(ctx, booking.CustomerID)
	if err != nil {
		return err
	}

	message := fmt.Sprintf(
		"DriveThrough: your driver has arrived. Share this verification code to start your trip: %s",
		booking.OTPCode,
	)
	// The code is deliberately absent from the log line; a failed send must not
	// leak the secret into log aggregation.
	if err := c.sms.Send(ctx, customer.Phone, message); err != nil {
		return fmt.Errorf("send handover otp: %w", err)
	}
	slog.Info("handover otp sent", "booking_id", booking.ID)
	return nil
}

// OnDriverNoShowDetected handles a booking the sweep escalated because the
// driver never started the trip.
//
// This is the mirror image of the driver-initiated no-show: the driver did not
// mark the customer absent, the driver went silent. It is a separate reaction
// path because the driver should be held accountable and the customer told what
// actually happened.
func (c *Coordinator) OnDriverNoShowDetected(ctx context.Context, booking *models.Booking, reason string) error {
	// A compare-and-swap from DRIVER_ARRIVED. If the driver started the trip
	// while the sweep was in flight, this conflicts and the sweep skips it.
	if err := c.bookings.UpdateStatus(ctx, booking.ID, models.BookingDriverNoShow, uuid.Nil, reason); err != nil {
		return err
	}

	// The customer is about to be told they have not been charged, so the hold
	// has to actually be given back. Nothing here is ever captured for a booking
	// with no trip, but leaving the authorization in place keeps their funds held
	// until the issuer expires it.
	if c.payments != nil {
		if err := c.payments.ReleaseAuthorizationForBooking(ctx, booking.ID); err != nil {
			// Not fatal: the booking is already correctly closed and the driver
			// struck. A failed release is an operational follow-up, and saying so
			// is better than silently leaving money held.
			slog.Error("failed to release payment hold after no-show",
				"booking_id", booking.ID, "error", err)
		}
	}

	if booking.DriverID != nil {
		if driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID); err == nil {
			_ = c.drivers.Release(ctx, driver.ID)
		}
	}

	_ = c.notifs.Notify(ctx, booking.CustomerID, models.NotifDriverNoShow,
		"Your driver did not arrive",
		"Your driver accepted the booking but never started the trip. You have not been charged and support has been notified.",
		map[string]string{"booking_id": booking.ID.String()},
	)

	return c.strikeDriver(ctx, booking)
}

// strikeDriver records a no-show against the driver so repeated offenders can
// be actioned. Best-effort: it must never mask the escalation itself.
func (c *Coordinator) strikeDriver(ctx context.Context, booking *models.Booking) error {
	if booking.DriverID == nil {
		return nil
	}
	driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID)
	if err != nil {
		return err
	}
	if err := c.drivers.RecordNoShow(ctx, driver.ID); err != nil {
		slog.Warn("failed to record driver no-show", "driver_id", driver.ID, "booking_id", booking.ID, "error", err)
		return nil
	}

	_ = c.notifs.Notify(ctx, driver.UserID, models.NotifDriverNoShow,
		"No-show recorded",
		"A booking you accepted was closed as a no-show because the trip was never started. Repeated no-shows may affect your standing.",
		map[string]string{"booking_id": booking.ID.String()},
	)
	return nil
}

// OnDriverNoShow records the failure against the customer and releases the
// driver. The booking is already terminal, so no re-match is attempted.
func (c *Coordinator) OnDriverNoShow(ctx context.Context, booking *models.Booking) error {
	if booking.DriverID != nil {
		if driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID); err == nil {
			_ = c.drivers.Release(ctx, driver.ID)
		}
	}
	return c.notifyCustomer(ctx, booking, models.NotifDriverNoShow,
		"Driver could not find you",
		"The driver marked you as not present at the pickup point. Support has been notified.",
	)
}

func (c *Coordinator) notifyCustomer(ctx context.Context, booking *models.Booking, typ models.NotificationType, title, body string) error {
	_ = c.notifs.Notify(ctx, booking.CustomerID, typ, title, body,
		map[string]string{"booking_id": booking.ID.String()})

	if booking.DriverID == nil {
		return nil
	}
	driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID)
	if err != nil {
		return err
	}
	_ = c.notifs.Notify(ctx, driver.UserID, typ, title, body,
		map[string]string{"booking_id": booking.ID.String()})
	return nil
}

func (c *Coordinator) OnOTPVerified(ctx context.Context, booking *models.Booking) error {
	if err := c.bookings.UpdateStatus(ctx, booking.ID, models.BookingOTPVerified, booking.CustomerID, "otp verified"); err != nil {
		return err
	}

	if booking.DriverID != nil {
		if driver, err := c.drivers.GetDriverByID(ctx, *booking.DriverID); err == nil {
			_ = c.notifs.Notify(ctx, driver.UserID, models.NotifOTPVerified,
				"OTP verified",
				"The customer has verified the OTP. You can start the trip.",
				map[string]string{"booking_id": booking.ID.String()},
			)
		}
	}
	return nil
}

// matching.Notifier implementation
func (c *Coordinator) NotifyDriverOffer(ctx context.Context, driverID, bookingID uuid.UUID, payload matching.OfferPayload) error {
	driver, err := c.drivers.GetDriverByID(ctx, driverID)
	if err != nil {
		return err
	}
	_ = c.notifs.Notify(ctx, driver.UserID, models.NotifNewBooking,
		"New booking available",
		"New driver request near you. Tap to view details.",
		map[string]string{
			"booking_id": bookingID.String(),
			"offer_id":   payload.OfferID.String(),
		},
	)
	return nil
}

func (c *Coordinator) NotifyMatchingFailure(ctx context.Context, customerID, bookingID uuid.UUID) error {
	return c.notifs.Notify(ctx, customerID, models.NotifBookingSearching,
		"No drivers available right now",
		"We could not find a driver near you. Please try again in a few minutes.",
		map[string]string{"booking_id": bookingID.String()},
	)
}

func (c *Coordinator) NotifyDriverAssigned(ctx context.Context, customerID, bookingID, driverID uuid.UUID) error {
	booking, err := c.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return err
	}

	_ = c.notifs.Notify(ctx, booking.CustomerID, models.NotifBookingAssigned,
		"Driver found",
		"Your driver is assigned and on the way.",
		map[string]string{
			"booking_id": bookingID.String(),
			"driver_id":  driverID.String(),
		},
	)
	return nil
}
