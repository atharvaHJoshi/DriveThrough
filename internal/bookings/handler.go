package bookings

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
	"drivethrough/pkg/validator"
)

type BookingCoordinator interface {
	OnBookingCreated(ctx context.Context, booking *models.Booking) error
	OnBookingCancelled(ctx context.Context, booking *models.Booking) error
	OnOTPVerified(ctx context.Context, booking *models.Booking) error
	OnDriverEnRoute(ctx context.Context, booking *models.Booking) error
	OnDriverArrived(ctx context.Context, booking *models.Booking) error
	OnDriverNoShow(ctx context.Context, booking *models.Booking) error
}

type Handler struct {
	service     *Service
	coordinator BookingCoordinator
}

func NewHandler(service *Service, coordinator BookingCoordinator) *Handler {
	return &Handler{service: service, coordinator: coordinator}
}

type createBookingRequest struct {
	VehicleID      uuid.UUID          `json:"vehicle_id"`
	BookingType    models.BookingType `json:"booking_type"`
	DurationHours  int                `json:"duration_hours"`
	PickupLocation string             `json:"pickup_location"`
	PickupLat      float64            `json:"pickup_lat"`
	PickupLng      float64            `json:"pickup_lng"`
	Destination    *string            `json:"destination"`
	DestLat        *float64           `json:"dest_lat"`
	DestLng        *float64           `json:"dest_lng"`
	ScheduledAt    *time.Time         `json:"scheduled_at"`
}

func (h *Handler) CreateBooking(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}
	if claims.Role != models.RoleCustomer {
		response.Forbidden(w, "only customers can book drivers")
		return
	}

	var req createBookingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if !validator.IsValidDurationHours(req.DurationHours) {
		response.BadRequest(w, "duration must be between 1 and 24 hours", nil)
		return
	}
	if req.BookingType == "" {
		req.BookingType = models.BookingTypeHourly
	}

	booking, err := h.service.CreateBooking(r.Context(), CreateBookingInput{
		CustomerID:     claims.UserID,
		VehicleID:      req.VehicleID,
		BookingType:    req.BookingType,
		DurationHours:  req.DurationHours,
		PickupLocation: req.PickupLocation,
		PickupLat:      req.PickupLat,
		PickupLng:      req.PickupLng,
		Destination:    req.Destination,
		DestLat:        req.DestLat,
		DestLng:        req.DestLng,
		ScheduledAt:    req.ScheduledAt,
	})
	if err != nil {
		response.AppError(w, err)
		return
	}

	if h.coordinator != nil {
		if err := h.coordinator.OnBookingCreated(r.Context(), booking); err != nil {
			response.AppError(w, err)
			return
		}
	}

	bookingResp, err := h.service.GetBooking(r.Context(), booking.ID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, map[string]any{
		"booking": bookingResp,
		"price":   bookingResp.PriceBreakdown(),
	})
}

func (h *Handler) Estimate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DurationHours int        `json:"duration_hours"`
		ScheduledAt   *time.Time `json:"scheduled_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if !validator.IsValidDurationHours(req.DurationHours) {
		response.BadRequest(w, "duration must be between 1 and 24 hours", nil)
		return
	}

	est := h.service.ComputeEstimate(req.DurationHours, req.ScheduledAt)
	response.JSON(w, http.StatusOK, est)
}

func (h *Handler) GetBooking(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	// Scoped to the parties. Reading by id alone exposed the pickup coordinates,
	// the destination, and the handover OTP state of every booking in the system
	// to any signed-in user.
	if err := h.service.AuthorizeBookingAccess(r.Context(), bookingID, claims.UserID); err != nil {
		response.AppError(w, err)
		return
	}

	booking, err := h.service.GetBooking(r.Context(), bookingID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"booking": booking,
		"price":   booking.PriceBreakdown(),
	})
}

// TripForBooking returns the trip started for a booking, so a party to it can
// subscribe to live tracking.
//
// The socket is addressed by trip id and a booking carries none, so this is the
// step that makes live tracking reachable at all. Authorisation is enforced in
// the service, before the unscoped trip lookup runs.
func (h *Handler) TripForBooking(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	trip, err := h.service.TripForBooking(r.Context(), bookingID, claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, trip)
}

func (h *Handler) CancelBooking(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}

	if err := h.service.CancelBooking(r.Context(), bookingID, claims.UserID, req.Reason); err != nil {
		response.AppError(w, err)
		return
	}

	if h.coordinator != nil {
		if booking, err := h.service.GetBooking(r.Context(), bookingID); err == nil {
			_ = h.coordinator.OnBookingCancelled(r.Context(), booking)
		}
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *Handler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req struct {
		OTP string `json:"otp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if req.OTP == "" {
		response.BadRequest(w, "otp is required", nil)
		return
	}

	// The booking is loaded only for the notification fan-out; ownership is
	// enforced inside VerifyOTP, which re-reads it scoped to the customer.
	booking, err := h.service.GetBooking(r.Context(), bookingID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.VerifyOTP(r.Context(), bookingID, claims.UserID, req.OTP); err != nil {
		response.AppError(w, err)
		return
	}

	if h.coordinator != nil {
		_ = h.coordinator.OnOTPVerified(r.Context(), booking)
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

// MarkEnRoute records the assigned driver heading to the pickup point.
func (h *Handler) MarkEnRoute(w http.ResponseWriter, r *http.Request) {
	h.driverTransition(w, r, func(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
		return h.service.MarkEnRoute(ctx, bookingID, userID)
	}, h.coordinator.OnDriverEnRoute, "en_route")
}

// MarkArrived records the driver reaching the customer and triggers handover OTP
// delivery.
func (h *Handler) MarkArrived(w http.ResponseWriter, r *http.Request) {
	h.driverTransition(w, r, func(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
		return h.service.MarkArrived(ctx, bookingID, userID)
	}, h.coordinator.OnDriverArrived, "arrived")
}

// MarkNoShow records that the customer never reached the pickup point.
func (h *Handler) MarkNoShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	// The body is optional, so a decode failure on an empty body is tolerated.
	_ = json.NewDecoder(r.Body).Decode(&req)

	h.driverTransition(w, r, func(ctx context.Context, bookingID, userID uuid.UUID) (*models.Booking, error) {
		return h.service.MarkDriverNoShow(ctx, bookingID, userID, req.Reason)
	}, h.coordinator.OnDriverNoShow, "no_show")
}

// driverTransition backs the three driver-driven status endpoints, which differ
// only in the service call and the coordinator reaction. Ownership, transition
// legality, and error mapping are identical, so they share one implementation.
func (h *Handler) driverTransition(
	w http.ResponseWriter,
	r *http.Request,
	apply func(context.Context, uuid.UUID, uuid.UUID) (*models.Booking, error),
	react func(context.Context, *models.Booking) error,
	outcome string,
) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	booking, err := apply(r.Context(), bookingID, claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if h.coordinator != nil && react != nil {
		// Reactions are best-effort: the status change is already durable, and
		// failing the request would leave the driver unable to retry a
		// notification that was merely slow.
		if err := react(r.Context(), booking); err != nil {
			slog.Warn("coordinator reaction failed", "booking_id", bookingID, "outcome", outcome, "error", err)
		}
	}

	response.JSON(w, http.StatusOK, map[string]any{"status": outcome, "booking": booking})
}

func (h *Handler) ListCustomerBookings(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	limit, offset := pagination(r)
	bookings, err := h.service.ListCustomerBookings(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, bookings)
}

func (h *Handler) ListDriverBookings(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	limit, offset := pagination(r)
	bookings, err := h.service.ListDriverBookings(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, bookings)
}

func pagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	return limit, offset
}

func (h *Handler) DriverCancel(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}

	if err := h.service.CancelForDriver(r.Context(), bookingID, claims.UserID, req.Reason); err != nil {
		response.AppError(w, err)
		return
	}

	// Without this the driver is never released from BUSY, their live offers are
	// left dangling, and neither side is notified.
	if h.coordinator != nil {
		if booking, err := h.service.GetBooking(r.Context(), bookingID); err == nil {
			_ = h.coordinator.OnBookingCancelled(r.Context(), booking)
		}
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
