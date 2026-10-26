package users

import (
	"encoding/json"
	"net/http"
	"strings"

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

type sendOTPRequest struct {
	Phone string `json:"phone"`
}

type verifyOTPRequest struct {
	Phone string          `json:"phone"`
	Code  string          `json:"code"`
	Name  string          `json:"name"`
	Role  models.UserRole `json:"role"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type updateProfileRequest struct {
	Name string `json:"name"`
}

func (h *Handler) SendOTP(w http.ResponseWriter, r *http.Request) {
	var req sendOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if err := h.service.RequestOTP(r.Context(), req.Phone); err != nil {
		writeAppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"message": "otp sent"})
}

func (h *Handler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req verifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	if req.Role == "" {
		req.Role = models.RoleCustomer
	}

	user, tokens, err := h.service.LoginWithOTP(r.Context(), req.Phone, req.Code, req.Name, req.Role)
	if err != nil {
		writeAppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"user":   user,
		"tokens": tokens,
	})
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	tokens, err := h.service.RefreshToken(r.Context(), req.RefreshToken)
	if err != nil {
		response.Unauthorized(w, "invalid refresh token")
		return
	}

	response.JSON(w, http.StatusOK, tokens)
}

func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	user, err := h.service.GetProfile(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, user)
}

func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}

	user, err := h.service.UpdateProfile(r.Context(), claims.UserID, req.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, user)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	token := ""
	if len(parts) == 2 {
		token = parts[1]
	}

	if err := h.service.Logout(r.Context(), claims.UserID, token); err != nil {
		writeAppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

func writeAppError(w http.ResponseWriter, err error) {
	response.AppError(w, err)
}
