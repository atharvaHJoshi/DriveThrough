package matching

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/response"
)

// BookingReader is the read-only view of bookings that the matching handler
// needs to authorise a driver response.
type BookingReader interface {
	GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
	FindActiveForDriver(ctx context.Context, driverID uuid.UUID) (*models.Booking, error)
}

// DriverProfileReader resolves the authenticated user to a driver profile.
type DriverProfileReader interface {
	GetDriverProfile(ctx context.Context, userID uuid.UUID) (*models.Driver, error)
}

type Handler struct {
	service    *Service
	driverSvc  DriverProfileReader
	bookingSvc BookingReader
}

func NewHandler(service *Service, driverSvc DriverProfileReader, bookingSvc BookingReader) *Handler {
	return &Handler{service: service, driverSvc: driverSvc, bookingSvc: bookingSvc}
}

type respondRequest struct {
	OfferID uuid.UUID `json:"offer_id"`
}

func (h *Handler) Accept(w http.ResponseWriter, r *http.Request) {
	driver, err := h.currentDriver(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req respondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.AcceptOffer(r.Context(), driver.ID, bookingID, req.OfferID); err != nil {
		response.AppError(w, err)
		return
	}

	booking, err := h.bookingSvc.GetBooking(r.Context(), bookingID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"status":     "accepted",
		"booking_id": booking.ID,
	})
}

func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	driver, err := h.currentDriver(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req respondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.RejectOffer(r.Context(), driver.ID, bookingID, req.OfferID); err != nil {
		response.AppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "declined"})
}

func (h *Handler) CurrentBooking(w http.ResponseWriter, r *http.Request) {
	driver, err := h.currentDriver(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	booking, err := h.bookingSvc.FindActiveForDriver(r.Context(), driver.ID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, booking)
}

// Offers lists the pending offers for the authenticated driver.
//
// This is the driver's entry point to matching. The assigned-bookings list is
// empty until an offer is accepted, so without this a driver has no way to see
// the work available to them other than a notification payload.
func (h *Handler) Offers(w http.ResponseWriter, r *http.Request) {
	driver, err := h.currentDriver(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	offers, err := h.service.PendingOffersForDriver(r.Context(), driver.ID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, offers)
}

func (h *Handler) currentDriver(r *http.Request) (*models.Driver, error) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		return nil, apperr.Unauthorized("authentication required")
	}
	return h.driverSvc.GetDriverProfile(r.Context(), claims.UserID)
}
