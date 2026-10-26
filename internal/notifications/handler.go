package notifications

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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}

	notifs, err := h.service.List(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, notifs)
}

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	notifID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.BadRequest(w, "invalid notification id", nil)
		return
	}

	if err := h.service.MarkRead(r.Context(), claims.UserID, notifID); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "read"})
}

type deviceTokenRequest struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

func (h *Handler) RegisterDeviceToken(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req deviceTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}
	if req.Token == "" {
		response.BadRequest(w, "token is required", nil)
		return
	}
	if req.Platform == "" {
		req.Platform = "android"
	}

	if err := h.service.RegisterDeviceToken(r.Context(), claims.UserID, req.Token, req.Platform); err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "registered"})
}
