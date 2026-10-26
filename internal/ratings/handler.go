package ratings

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
)

// RatingReader is the read-side dependency, declared by the consumer so the
// handler never touches the database pool directly.
type RatingReader interface {
	ByBooking(ctx context.Context, bookingID uuid.UUID) ([]models.Rating, error)
}

// BookingAuthorizer decides whether a caller may see a booking's ratings.
type BookingAuthorizer interface {
	AuthorizeBookingAccess(ctx context.Context, bookingID, userID uuid.UUID) error
}

type Handler struct {
	service    *Service
	reader     RatingReader
	authorizer BookingAuthorizer
}

func NewHandler(service *Service, reader RatingReader, authorizer BookingAuthorizer) *Handler {
	return &Handler{service: service, reader: reader, authorizer: authorizer}
}

type rateRequest struct {
	Rating  int      `json:"rating"`
	Comment string   `json:"comment"`
	Tags    []string `json:"tags"`
}

func (h *Handler) Rate(w http.ResponseWriter, r *http.Request) {
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

	var req rateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.RateBooking(r.Context(), bookingID, claims.UserID, req.Rating, req.Comment, req.Tags); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "rated"})
}

func (h *Handler) ByBooking(w http.ResponseWriter, r *http.Request) {
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

	// Ratings carry the rater's identity and comment, so they are visible only to
	// the two parties to the booking.
	if err := h.authorizer.AuthorizeBookingAccess(r.Context(), bookingID, claims.UserID); err != nil {
		response.AppError(w, err)
		return
	}

	ratings, err := h.reader.ByBooking(r.Context(), bookingID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, ratings)
}
