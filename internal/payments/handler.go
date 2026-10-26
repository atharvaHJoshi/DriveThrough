package payments

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

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

func (h *Handler) Initiate(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "booking_id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	result, err := h.service.Initiate(r.Context(), bookingID, claims.UserID)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

// webhookEvent mirrors the Razorpay webhook envelope.
//
// The identifiers live one level deeper than a flat read suggests: every entity
// sits under payload.<kind>.entity, and the payment's own id is
// payload.payment.entity.id. Reading payload.payment.id silently yields "", so
// the handler used to look the payment up by an empty gateway ref, miss, and
// return an error — which makes Razorpay retry the event indefinitely.
type webhookEvent struct {
	Entity string `json:"entity"`
	Event  string `json:"event"`
	// Contains is a JSON array (["payment"]), not a string. Declaring it as a
	// string made encoding/json fail the entire unmarshal with a type error, so
	// every real webhook was rejected as malformed and no payment was ever
	// captured.
	Contains []string `json:"contains"`
	Payload  struct {
		Payment struct {
			Entity struct {
				ID     string `json:"id"`
				Order  string `json:"order_id"`
				Method string `json:"method"`
				Amount int    `json:"amount"`
				Status string `json:"status"`
			} `json:"entity"`
		} `json:"payment"`
		Order struct {
			Entity struct {
				ID     string `json:"id"`
				Amount int    `json:"amount"`
				Status string `json:"status"`
			} `json:"entity"`
		} `json:"order"`
		Refund struct {
			Entity struct {
				ID        string `json:"id"`
				PaymentID string `json:"payment_id"`
				Amount    int    `json:"amount"`
				Status    string `json:"status"`
			} `json:"entity"`
		} `json:"refund"`
	} `json:"payload"`
}

// identifiers returns the order and payment ids carried by whichever entity the
// event is about. Different events populate different parts, so the resolution
// is deliberately permissive and returns both.
//
// For payment events the order id is payload.payment.entity.order_id; there is
// no payload.order key at all. Reading only payload.order.entity.id therefore
// left the order unresolved for every payment.captured and payment.failed
// delivery, so the payment was never matched to its booking.
func (e *webhookEvent) identifiers() (orderID, paymentID string) {
	if e.Payload.Order.Entity.ID != "" {
		orderID = e.Payload.Order.Entity.ID
	}
	if e.Payload.Payment.Entity.Order != "" {
		orderID = e.Payload.Payment.Entity.Order
	}
	if e.Payload.Payment.Entity.ID != "" {
		paymentID = e.Payload.Payment.Entity.ID
	}
	// Refund events reference the payment directly and carry no order id.
	if e.Payload.Refund.Entity.PaymentID != "" && paymentID == "" {
		paymentID = e.Payload.Refund.Entity.PaymentID
	}
	return orderID, paymentID
}

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	// The signature covers the exact bytes received, so the body is read once
	// and both verified and parsed from that same buffer.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		response.BadRequest(w, "could not read body", nil)
		return
	}

	signature := r.Header.Get("X-Razorpay-Signature")
	if signature == "" {
		response.Unauthorized(w, "missing webhook signature")
		return
	}
	if !h.service.client.VerifyWebhookSignature(body, signature, "") {
		response.Unauthorized(w, "invalid webhook signature")
		return
	}

	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		response.BadRequest(w, "invalid webhook payload", nil)
		return
	}

	// The event id is the idempotency key. Razorpay redelivers an event until it
	// receives a 2xx, so without this a single capture is applied repeatedly and
	// driver earnings are credited more than once.
	eventID := r.Header.Get("X-Razorpay-Event-Id")
	orderID, paymentID := event.identifiers()

	err = h.service.ConfirmFromWebhook(r.Context(), GatewayEvent{
		EventID:   eventID,
		Event:     event.Event,
		OrderID:   orderID,
		PaymentID: paymentID,
		Raw:       body,
	})
	if err != nil && !errors.Is(err, ErrDuplicateEvent) {
		response.AppError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

type refundRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) Refund(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "booking_id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	var req refundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body", nil)
		return
	}
	if req.Reason == "" {
		req.Reason = "customer requested cancellation"
	}

	refund, err := h.service.RefundBooking(r.Context(), bookingID, claims.UserID, req.Reason)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, refund)
}

func (h *Handler) GetByBooking(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	bookingID, err := uuid.Parse(chi.URLParam(r, "booking_id"))
	if err != nil {
		response.BadRequest(w, "invalid booking id", nil)
		return
	}

	// The payment is fetched scoped to the caller. Reading it by booking id alone
	// let any authenticated user see the amount, breakdown, and gateway
	// reference of somebody else's booking.
	payment, err := h.service.GetByBookingForUser(r.Context(), bookingID, claims.UserID, claims.Role)
	if err != nil {
		response.AppError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, payment)
}
