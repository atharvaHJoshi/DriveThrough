package drivers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type registerRequest struct {
	ExperienceYears int                      `json:"experience_years"`
	Capabilities    models.VehicleCapability `json:"capabilities"`
}

type availabilityRequest struct {
	Online bool `json:"online"`
}

type capabilityRequest struct {
	Capabilities models.VehicleCapability `json:"capabilities"`
}

type locationRequest struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

type documentRequest struct {
	Type           models.DocumentType `json:"type"`
	DocumentNumber string              `json:"document_number"`
	ExpiryDate     *time.Time          `json:"expiry_date"`
	DocumentURL    string              `json:"document_url"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	driver, err := h.service.RegisterDriver(r.Context(), RegistrationInput{
		UserID:          claims.UserID,
		ExperienceYears: req.ExperienceYears,
		Capabilities:    req.Capabilities,
	})
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, driver)
}

func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, driver)
}

func (h *Handler) GoOnline(w http.ResponseWriter, r *http.Request) {
	h.setAvailability(w, r, true)
}

func (h *Handler) GoOffline(w http.ResponseWriter, r *http.Request) {
	h.setAvailability(w, r, false)
}

func (h *Handler) setAvailability(w http.ResponseWriter, r *http.Request, online bool) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	updated, err := h.service.SetAvailability(r.Context(), driver.ID, online)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, updated)
}

func (h *Handler) SetAvailability(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req availabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	updated, err := h.service.SetAvailability(r.Context(), driver.ID, req.Online)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, updated)
}

func (h *Handler) UpdateCapabilities(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req capabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.UpdateCapabilities(r.Context(), driver.ID, req.Capabilities); err != nil {
		response.AppError(w, err)
		return
	}

	updated, _ := h.service.GetDriverProfile(r.Context(), claims.UserID)
	response.JSON(w, http.StatusOK, updated)
}

func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req locationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.UpdateLocation(r.Context(), driver.ID, req.Latitude, req.Longitude); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) SubmitDocument(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req documentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.SubmitDocument(r.Context(), DocumentInput{
		DriverID:       driver.ID,
		Type:           req.Type,
		DocumentNumber: req.DocumentNumber,
		ExpiryDate:     req.ExpiryDate,
		DocumentURL:    req.DocumentURL,
	}); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "submitted"})
}

func (h *Handler) Documents(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	driver, err := h.service.GetDriverProfile(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}

	docs, err := h.service.ListDocuments(r.Context(), driver.ID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, docs)
}

// GetByID serves the customer-facing driver card. It is deliberately distinct
// from Profile, which returns the caller's own full record including location and
// earnings.
func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	driverID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid driver id", nil)
		return
	}

	driver, err := h.service.GetDriverByID(r.Context(), driverID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, driver.Public())
}

func (h *Handler) Earnings(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	total, entries, err := h.service.Earnings(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}
