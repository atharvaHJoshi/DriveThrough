package vehicles

import (
	"encoding/json"
	"net/http"

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

func (h *Handler) CreateVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req models.Vehicle
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	vehicle, err := h.service.AddVehicle(r.Context(), claims.UserID, req)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, vehicle)
}

func (h *Handler) ListVehicles(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	vehicles, err := h.service.ListVehicles(r.Context(), claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, vehicles)
}

func (h *Handler) GetVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid vehicle id", nil)
		return
	}

	// Scoped to the owner. Reading a vehicle by id alone exposed registration
	// numbers and documents belonging to other customers to any signed-in user.
	vehicle, err := h.service.GetVehicleForOwner(r.Context(), id, claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, vehicle)
}

func (h *Handler) UpdateVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid vehicle id", nil)
		return
	}

	// Decoded into the patch struct rather than models.Vehicle so that omitted
	// fields stay nil and are left untouched.
	var req UpdateVehicleInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	vehicle, err := h.service.UpdateVehicle(r.Context(), id, claims.UserID, req)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, vehicle)
}

func (h *Handler) DeleteVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid vehicle id", nil)
		return
	}

	if err := h.service.DeleteVehicle(r.Context(), id, claims.UserID); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) SetDefault(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid vehicle id", nil)
		return
	}

	if err := h.service.SetDefaultVehicle(r.Context(), id, claims.UserID); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "default set"})
}
