package incidents

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
	"drivethrough/pkg/validator"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type sosRequest struct {
	TripID    *uuid.UUID `json:"trip_id"`
	Latitude  float64    `json:"lat"`
	Longitude float64    `json:"lng"`
	Kind      string     `json:"kind"`
}

func (h *Handler) TriggerSOS(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req sosRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	sos, err := h.service.TriggerSOS(r.Context(), SOSInput{
		UserID:    claims.UserID,
		TripID:    req.TripID,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
		Kind:      req.Kind,
	})
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, sos)
}

type reportRequest struct {
	BookingID   *uuid.UUID              `json:"booking_id"`
	TripID      *uuid.UUID              `json:"trip_id"`
	Type        models.IncidentType     `json:"type"`
	Severity    models.IncidentSeverity `json:"severity"`
	Latitude    *float64                `json:"lat"`
	Longitude   *float64                `json:"lng"`
	Description string                  `json:"description"`
	Photos      []string                `json:"photos"`
}

func (h *Handler) ReportIncident(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if req.Latitude != nil && req.Longitude != nil && !validator.IsValidCoordinate(*req.Latitude, *req.Longitude) {
		response.BadRequest(w, "invalid coordinates", nil)
		return
	}

	incident := &models.Incident{
		BookingID:   req.BookingID,
		TripID:      req.TripID,
		Type:        req.Type,
		Severity:    req.Severity,
		ReportedBy:  claims.UserID,
		Latitude:    req.Latitude,
		Longitude:   req.Longitude,
		Description: req.Description,
		Photos:      req.Photos,
	}

	result, err := h.service.ReportIncident(r.Context(), incident)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, result)
}

type contactRequest struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Relation string `json:"relation"`
}

func (h *Handler) AddContact(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req contactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	contact, err := h.service.AddContact(r.Context(), claims.UserID, req.Name, req.Phone, req.Relation)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, contact)
}

func (h *Handler) ListContacts(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	contacts, err := h.service.ListContacts(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, contacts)
}
