package trips

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/response"
)

type Handler struct {
	service *Service
}

// actor resolves the caller into a TripAction. A JWT carries users(id) while
// trip and booking rows reference drivers(id), so the driver profile has to be
// looked up rather than reusing the token subject as the driver id.
func (h *Handler) actor(r *http.Request) (TripAction, error) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		return TripAction{}, apperr.Unauthorized("authentication required")
	}
	driver, err := h.service.DriverForUser(r.Context(), claims.UserID)
	if err != nil {
		return TripAction{}, err
	}
	return TripAction{DriverID: driver.ID, ActorID: claims.UserID}, nil
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type startTripRequest struct {
	BookingID     uuid.UUID          `json:"booking_id"`
	StartOdometer int                `json:"start_odometer"`
	StartFuel     int                `json:"start_fuel"`
	Inspection    *inspectionRequest `json:"pre_inspection"`
}

type inspectionRequest struct {
	Odometer    int      `json:"odometer"`
	FuelLevel   int      `json:"fuel_level"`
	Photos      []string `json:"photos"`
	DamageNotes *string  `json:"damage_notes"`
	HasDamage   bool     `json:"has_damage"`
}

func (h *Handler) StartTrip(w http.ResponseWriter, r *http.Request) {
	var req startTripRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	inspection := parseInspection(req.Inspection)

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	trip, err := h.service.StartTrip(r.Context(), StartInput{
		BookingID:     req.BookingID,
		DriverID:      act.DriverID,
		ActorID:       act.ActorID,
		StartOdometer: req.StartOdometer,
		StartFuel:     req.StartFuel,
		PreInspection: inspection,
	})
	if err != nil {
		response.AppError(w, err)
		return
	}

	response.JSON(w, http.StatusCreated, trip)
}

func parseInspection(req *inspectionRequest) *models.VehicleInspection {
	if req == nil {
		return nil
	}
	return &models.VehicleInspection{
		Type:        "PRE_TRIP",
		Odometer:    req.Odometer,
		FuelLevel:   req.FuelLevel,
		Photos:      req.Photos,
		DamageNotes: req.DamageNotes,
		HasDamage:   req.HasDamage,
	}
}

func (h *Handler) PauseTrip(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid trip id", nil)
		return
	}

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.PauseTrip(r.Context(), tripID, act); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (h *Handler) ResumeTrip(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid trip id", nil)
		return
	}

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.ResumeTrip(r.Context(), tripID, act); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

type endTripRequest struct {
	EndOdometer int                `json:"end_odometer"`
	EndFuel     int                `json:"end_fuel"`
	Inspection  *inspectionRequest `json:"post_inspection"`
}

func (h *Handler) EndTrip(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid trip id", nil)
		return
	}

	var req endTripRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	var inspection *models.VehicleInspection
	if req.Inspection != nil {
		insp := parseInspection(req.Inspection)
		insp.Type = "POST_TRIP"
		inspection = insp
	}

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	result, err := h.service.EndTrip(r.Context(), EndInput{
		TripID:         tripID,
		DriverID:       act.DriverID,
		ActorID:        act.ActorID,
		EndOdometer:    req.EndOdometer,
		EndFuel:        req.EndFuel,
		PostInspection: inspection,
	})
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

type locationUpdateRequest struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
	Accuracy  float64 `json:"accuracy"`
	Heading   float64 `json:"heading"`
	Speed     float64 `json:"speed"`
}

func (h *Handler) RecordLocation(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid trip id", nil)
		return
	}

	var req locationUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	loc := models.LocationUpdate{
		TripID:    tripID,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
		Accuracy:  req.Accuracy,
		Heading:   req.Heading,
		Speed:     req.Speed,
		Timestamp: time.Now(),
	}

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	if err := h.service.RecordLocation(r.Context(), tripID, loc, act); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (h *Handler) Inspections(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid trip id", nil)
		return
	}

	act, err := h.actor(r)
	if err != nil {
		response.AppError(w, err)
		return
	}

	inspections, err := h.service.Inspections(r.Context(), tripID, act)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, inspections)
}
