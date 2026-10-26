package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/pkg/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := h.service.Dashboard(r.Context())
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, stats)
}

func (h *Handler) PendingDrivers(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginate(r)
	drivers, err := h.service.PendingDrivers(r.Context(), limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, drivers)
}

func (h *Handler) DriverDetail(w http.ResponseWriter, r *http.Request) {
	driverID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid driver id", nil)
		return
	}

	driver, docs, err := h.service.DriverDetail(r.Context(), driverID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"driver":    driver,
		"documents": docs,
	})
}

type verifyRequest struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

func (h *Handler) VerifyDriver(w http.ResponseWriter, r *http.Request) {
	driverID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid driver id", nil)
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.VerifyDriver(r.Context(), driverID, req.Approved, req.Reason); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) SuspendDriver(w http.ResponseWriter, r *http.Request) {
	driverID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid driver id", nil)
		return
	}

	var req struct {
		Suspend bool `json:"suspend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.SuspendDriver(r.Context(), driverID, req.Suspend); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) VerifyDocument(w http.ResponseWriter, r *http.Request) {
	docID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid document id", nil)
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.VerifyDocument(r.Context(), docID, req.Approved); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) Bookings(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, offset := paginate(r)
	bookings, err := h.service.Bookings(r.Context(), status, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, bookings)
}

func (h *Handler) DriverEarnings(w http.ResponseWriter, r *http.Request) {
	driverID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid driver id", nil)
		return
	}

	limit, offset := paginate(r)
	earnings, err := h.service.DriverEarnings(r.Context(), driverID, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, earnings)
}

func (h *Handler) Incidents(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, offset := paginate(r)
	incidents, err := h.service.Incidents(r.Context(), status, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, incidents)
}

func (h *Handler) IncidentDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid incident id", nil)
		return
	}

	incident, err := h.service.IncidentDetail(r.Context(), id)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, incident)
}

type resolveRequest struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

func (h *Handler) ResolveIncident(w http.ResponseWriter, r *http.Request) {
	adminID, ok := adminUserID(r)
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid incident id", nil)
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}
	if req.Status == "" {
		req.Status = "RESOLVED"
	}

	if err := h.service.ResolveIncident(r.Context(), id, req.Status, req.Resolution, adminID); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func paginate(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	return limit, offset
}

func adminUserID(r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		return uuid.Nil, false
	}
	return claims.UserID, true
}
