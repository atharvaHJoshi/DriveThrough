package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"drivethrough/internal/config"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type RazorpayClient struct {
	baseURL    string
	keyID      string
	keySecret  string
	httpClient *http.Client
}

func NewRazorpayClient(cfg config.RazorpayConfig, httpClient *http.Client) *RazorpayClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &RazorpayClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		keyID:      cfg.KeyID,
		keySecret:  cfg.KeySecret,
		httpClient: httpClient,
	}
}

func (c *RazorpayClient) Enabled() bool {
	return c.keyID != "" && c.keySecret != ""
}

func (c *RazorpayClient) CreateOrder(ctx context.Context, amount int, reference string, notes map[string]string) (string, error) {
	if !c.Enabled() {
		return "mock_order_" + reference, nil
	}

	payload := map[string]any{
		"amount":          amount * 100,
		"currency":        "INR",
		"receipt":         reference,
		"payment_capture": 1,
		"notes":           notes,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/orders", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("razorpay create order failed: status %d", resp.StatusCode)
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *RazorpayClient) CapturePayment(ctx context.Context, paymentID string, amount int) error {
	if !c.Enabled() {
		return nil
	}

	payload := map[string]any{"amount": amount * 100}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/payments/"+url.PathEscape(paymentID)+"/capture", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("razorpay capture failed: status %d", resp.StatusCode)
	}
	return nil
}

// VoidPayment releases an authorized-but-uncaptured payment.
//
// A booking that ends without a ride — cancelled, or closed as a no-show — must
// not leave the customer's funds held. The authorization is never captured, so
// the customer is not charged, but the hold itself stays on their card until the
// issuer expires it, which can be days. Explicitly voiding it releases the money
// straight away and records the intent, so the payment row does not sit in
// AUTHORIZED forever looking like something still owed.
func (c *RazorpayClient) VoidPayment(ctx context.Context, paymentID string) error {
	if !c.Enabled() {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/payments/"+url.PathEscape(paymentID)+"/void", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("razorpay void failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *RazorpayClient) VerifyWebhookSignature(body []byte, signature string, secret string) bool {
	if secret == "" {
		secret = c.keySecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (c *RazorpayClient) VerifyPaymentSignature(orderID, paymentID, signature string) bool {
	message := orderID + "|" + paymentID
	mac := hmac.New(sha256.New, []byte(c.keySecret))
	mac.Write([]byte(message))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (c *RazorpayClient) Refund(ctx context.Context, paymentID, reason string, amount int) (string, error) {
	if !c.Enabled() {
		return "mock_refund_" + paymentID, nil
	}

	payload := map[string]any{
		"amount":               amount * 100,
		"speed":                "normal",
		"notes":                map[string]string{"reason": reason},
		"queue_if_low_balance": true,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/payments/"+url.PathEscape(paymentID)+"/refund", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("razorpay refund failed: status %d", resp.StatusCode)
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

type EarningsRecorder interface {
	// CreditEarning is exactly-once per booking, so it can be retried safely.
	CreditEarning(ctx context.Context, driverID, bookingID uuid.UUID, amount int) error
	// DriverUserID resolves a driver profile to its user id, because
	// notifications.user_id references users(id) while bookings reference
	// drivers(id).
	DriverUserID(ctx context.Context, driverID uuid.UUID) (uuid.UUID, error)
}

type BookingAccessor interface {
	GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
	UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error
}

type Notifier interface {
	Notify(ctx context.Context, userID uuid.UUID, notifType models.NotificationType, title, body string, data map[string]string) error
}

type Service struct {
	repo      *Repository
	client    *RazorpayClient
	driverSvc EarningsRecorder
	bookings  BookingAccessor
	notifier  Notifier
}

func NewService(repo *Repository, client *RazorpayClient, driverSvc EarningsRecorder, bookingsSvc BookingAccessor, notifier Notifier) *Service {
	return &Service{
		repo:      repo,
		client:    client,
		driverSvc: driverSvc,
		bookings:  bookingsSvc,
		notifier:  notifier,
	}
}

type InitiateResult struct {
	Payment models.Payment `json:"payment"`
	OrderID string         `json:"order_id"`
	KeyID   string         `json:"key_id"`
	IsMock  bool           `json:"is_mock"`
}

func (s *Service) Initiate(ctx context.Context, bookingID uuid.UUID, customerID uuid.UUID) (*InitiateResult, error) {
	booking, err := s.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	if booking.CustomerID != customerID {
		return nil, apperr.Forbidden("not your booking")
	}

	existing, err := s.repo.GetByBooking(ctx, bookingID)
	// Only a genuine miss justifies creating a second payment. Swallowing every
	// error meant a transient database failure produced a duplicate charge row.
	if err == nil {
		return s.toResult(ctx, existing)
	}
	if !apperr.IsNotFound(err) {
		return nil, apperr.Wrap(500, "PAYMENT_LOOKUP_FAILED", "failed to look up existing payment", err, nil)
	}

	amount := booking.EstimatedPrice
	if booking.FinalPrice != nil {
		amount = *booking.FinalPrice
	}

	driverAmount := booking.DriverFee

	payment := &models.Payment{
		BookingID:     bookingID,
		CustomerID:    customerID,
		Amount:        amount,
		PlatformFee:   booking.PlatformFee,
		DriverAmount:  driverAmount,
		Authorization: 0,
		ProtectionFee: booking.ProtectionFee,
		Tax:           booking.Tax,
		Status:        models.PaymentInitiated,
		Metadata: map[string]any{
			"booking_id": bookingID.String(),
		},
	}

	orderID, err := s.client.CreateOrder(ctx, amount, bookingID.String(), map[string]string{
		"booking_id": bookingID.String(),
	})
	if err != nil {
		return nil, apperr.Wrap(500, "ORDER_CREATE_FAILED", "failed to create payment order", err, nil)
	}

	// The order id is recorded in its own column. gateway_reference is reserved
	// for the gateway's payment id, which does not exist until the gateway
	// reports one, and is the id the capture, void, and refund calls need.
	payment.GatewayOrderID = orderID

	if err := s.repo.Create(ctx, payment); err != nil {
		return nil, apperr.Wrap(500, "PAYMENT_CREATE_FAILED", "failed to create payment", err, nil)
	}

	return s.toResult(ctx, payment)
}

func (s *Service) toResult(ctx context.Context, payment *models.Payment) (*InitiateResult, error) {
	return &InitiateResult{
		Payment: *payment,
		OrderID: payment.GatewayOrderID,
		KeyID:   s.client.keyID,
		IsMock:  !s.client.Enabled(),
	}, nil
}

// GatewayEvent is a verified webhook delivery.
type GatewayEvent struct {
	// EventID is the gateway's delivery id, used as the idempotency key. It is
	// empty for gateways that do not send one, in which case the status guards
	// below still make the common cases safe.
	EventID   string
	Event     string
	OrderID   string
	PaymentID string
	Raw       []byte
}

// ErrDuplicateEvent signals that this delivery was already applied. The handler
// still answers 200, because a redelivery is a success from the gateway's
// perspective and answering anything else makes it retry forever.
var ErrDuplicateEvent = errors.New("gateway event already processed")

// ConfirmFromWebhook applies a verified gateway event.
//
// The event is claimed first so concurrent deliveries of the same event
// cannot both do the work, and completed only after the work succeeds. If
// processing fails, the claim is abandoned rather than marked done, so the
// gateway's retry is processed instead of being discarded as a duplicate.
func (s *Service) ConfirmFromWebhook(ctx context.Context, ev GatewayEvent) error {
	claimed := ev.EventID == ""
	if ev.EventID != "" {
		var err error
		claimed, err = s.repo.ClaimGatewayEvent(ctx, ev.EventID, "razorpay", ev.Event, ev.Raw)
		if err != nil {
			return apperr.Wrap(500, "WEBHOOK_RECORD_FAILED", "failed to record gateway event", err, nil)
		}
		if !claimed {
			slog.Info("ignoring duplicate gateway event", "event", ev.Event, "event_id", ev.EventID)
			return ErrDuplicateEvent
		}
	}

	if err := s.applyEvent(ctx, ev); err != nil {
		return err
	}

	if claimed {
		if err := s.repo.CompleteGatewayEvent(ctx, ev.EventID); err != nil {
			// The work is already done, so this is a bookkeeping failure rather
			// than a processing failure. Reporting it is still correct: without
			// the mark the event stays claimable and a later redelivery would
			// repeat work that the status guards already make idempotent.
			slog.Warn("failed to mark gateway event processed",
				"event", ev.Event, "event_id", ev.EventID, "error", err)
		}
	}
	return nil
}

// applyEvent performs the work a claimed event describes.
func (s *Service) applyEvent(ctx context.Context, ev GatewayEvent) error {
	switch ev.Event {
	case "order.paid", "payment.authorized":
		// Auto-capture is requested when the order is created, so the gateway
		// has already moved the money by the time either event arrives.
		// Capturing again would be rejected by the gateway.
		return s.markCaptured(ctx, ev)

	case "payment.captured":
		// The gateway completed the capture itself. Record it without asking the
		// gateway to do the work again.
		return s.markCaptured(ctx, ev)

	case "payment.failed":
		return s.markFailed(ctx, ev)

	case "refund.processed", "refund.updated":
		return s.confirmRefundFromWebhook(ctx, ev)

	default:
		// Unknown event types are acknowledged, not retried: a 4xx/5xx would put
		// the gateway in a backoff loop against an event that will never be
		// handled here.
		slog.Debug("ignoring razorpay webhook event", "event", ev.Event)
		return nil
	}
}

// resolvePayment finds the payment an event refers to.
//
// The stored gateway ref is the *order* id, but refund events identify the
// payment directly, so both have to be tried. A payment event may carry only
// one of the two, depending on the event type and the gateway version.
func (s *Service) resolvePayment(ctx context.Context, ev GatewayEvent) (*models.Payment, error) {
	candidates := []string{ev.OrderID, ev.PaymentID}
	for _, ref := range candidates {
		if ref == "" {
			continue
		}
		payment, err := s.repo.GetByGatewayRef(ctx, ref)
		if err == nil {
			// The event is the first place the gateway's payment id is known.
			// Persisting it is what lets capture, void, and refund address the
			// payment rather than the order.
			if err := s.repo.RecordGatewayPaymentID(ctx, payment.ID, ev.PaymentID); err != nil {
				return nil, err
			}
			payment.GatewayRef = ev.PaymentID
			return payment, nil
		}
		if !apperr.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, apperr.NotFound("no payment matches gateway reference")
}

func (s *Service) markCaptured(ctx context.Context, ev GatewayEvent) error {
	payment, err := s.resolvePayment(ctx, ev)
	if err != nil {
		return err
	}
	return s.captureSuccess(ctx, payment)
}

func (s *Service) markFailed(ctx context.Context, ev GatewayEvent) error {
	payment, err := s.resolvePayment(ctx, ev)
	if err != nil {
		return err
	}
	if payment.Status == models.PaymentCaptured {
		// A late failure event after a successful capture must not undo it.
		slog.Warn("ignoring failure event for an already captured payment",
			"payment_id", payment.ID, "event", ev.Event)
		return nil
	}
	if err := s.repo.UpdateStatus(ctx, payment.ID, models.PaymentFailed); err != nil {
		return err
	}
	return s.bookings.UpdateStatus(ctx, payment.BookingID, models.BookingPaymentFailed, uuid.Nil, "payment failed")
}

// captureSuccess finalises a payment whose funds have been taken by the gateway,
// and credits the driver.
//
// The credit is deliberately not gated on winning the status transition. An
// attempt can mark the payment CAPTURED and then fail before crediting the
// driver, and a retry would otherwise re-read the payment, see CAPTURED, return
// immediately, and strand the driver unpaid with nothing left to trigger the
// credit. Instead the status is settled first and the credit is then applied
// idempotently on every delivery, which is safe because it is keyed on the
// booking.
func (s *Service) captureSuccess(ctx context.Context, payment *models.Payment) error {
	if payment.Status == models.PaymentRefunded {
		// The money was returned, so there is nothing to credit.
		return nil
	}

	if payment.Status != models.PaymentCaptured {
		// A single conditional write settles the status. A concurrent delivery
		// that already moved the row out of AUTHORIZED/INITIATED wins this race,
		// but that does not mean it credited the driver, so the work below still
		// runs.
		if _, err := s.repo.MarkCaptured(ctx, payment.ID); err != nil {
			return err
		}
	}

	// Re-read rather than trusting the pre-transition snapshot: a lost race can
	// mean the payment ended up FAILED or SETTLED, and crediting a driver for a
	// payment that is not actually captured would be wrong.
	current, err := s.repo.GetByID(ctx, payment.ID)
	if err != nil {
		return err
	}
	if current.Status != models.PaymentCaptured {
		slog.Info("payment is not captured, skipping driver credit",
			"payment_id", payment.ID, "status", current.Status)
		return nil
	}

	booking, err := s.bookings.GetBooking(ctx, current.BookingID)
	if err != nil {
		return err
	}

	if booking.DriverID != nil && s.driverSvc != nil {
		if err := s.driverSvc.CreditEarning(ctx, *booking.DriverID, current.BookingID, current.DriverAmount); err != nil {
			return apperr.Wrap(500, "EARNINGS_RECORD_FAILED", "failed to record driver earnings", err, nil)
		}
	}

	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, booking.CustomerID, models.NotifPaymentSuccess,
			"Payment successful",
			fmt.Sprintf("Your payment of ₹%d for the driver booking was successful.", current.Amount),
			map[string]string{"booking_id": booking.ID.String(), "payment_id": current.ID.String()},
		)
		if booking.DriverID != nil {
			// notifications.user_id references users(id); the booking holds a
			// drivers(id), so the driver's user id has to be resolved first.
			if driver, err := s.driverSvc.DriverUserID(ctx, *booking.DriverID); err == nil {
				_ = s.notifier.Notify(ctx, driver, models.NotifPaymentSuccess,
					"Earnings credited",
					fmt.Sprintf("₹%d has been added to your earnings for this trip.", payment.DriverAmount),
					map[string]string{"booking_id": booking.ID.String()},
				)
			}
		}
	}

	return nil
}

func (s *Service) confirmRefundFromWebhook(ctx context.Context, ev GatewayEvent) error {
	// A refund names its payment; falling back to the order id keeps handling
	// working for gateway versions that include one.
	ref := ev.PaymentID
	if ref == "" {
		ref = ev.OrderID
	}
	if ref == "" {
		return apperr.BadRequest("refund event carried no payment reference", nil)
	}

	payment, err := s.repo.GetByGatewayRef(ctx, ref)
	if err != nil {
		return err
	}
	if err := s.repo.UpdateStatusByGatewayRef(ctx, ref, models.PaymentRefunded); err != nil {
		return err
	}

	refund, err := s.repo.GetRefundByPayment(ctx, payment.ID)
	if err != nil {
		// The gateway refunded something we have no local record of; the payment
		// status is still corrected above.
		slog.Warn("no local refund row for gateway refund", "payment_id", payment.ID)
		return nil
	}
	if refund.Status == "COMPLETED" {
		return nil
	}
	if err := s.repo.UpdateRefundStatus(ctx, refund.ID, "COMPLETED", refund.GatewayRef); err != nil {
		return err
	}
	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, payment.CustomerID, models.NotifPaymentRefunded,
			"Refund completed",
			fmt.Sprintf("Your refund of ₹%d has been credited.", refund.Amount),
			map[string]string{"booking_id": payment.BookingID.String()},
		)
	}
	return nil
}

func (s *Service) GetByBooking(ctx context.Context, bookingID uuid.UUID) (*models.Payment, error) {
	return s.repo.GetByBooking(ctx, bookingID)
}

// GetByBookingForUser returns the payment only to a party to the booking.
func (s *Service) GetByBookingForUser(ctx context.Context, bookingID, userID uuid.UUID, role models.UserRole) (*models.Payment, error) {
	if role == models.RoleAdmin {
		return s.repo.GetByBooking(ctx, bookingID)
	}

	booking, err := s.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}

	if booking.CustomerID == userID {
		return s.repo.GetByBooking(ctx, bookingID)
	}

	// The driver of the booking may see the payout, since it is their earnings.
	if booking.DriverID != nil && s.driverSvc != nil {
		driverUserID, err := s.driverSvc.DriverUserID(ctx, *booking.DriverID)
		if err == nil && driverUserID == userID {
			return s.repo.GetByBooking(ctx, bookingID)
		}
	}

	return nil, apperr.Forbidden("not your booking")
}

// ReleaseAuthorizationForBooking gives back an uncaptured hold when a booking
// ends without a ride.
//
// This is the counterpart to CaptureForBooking for the terminal states that are
// not a completed trip: cancellation, and a booking closed as a no-show. Nothing
// was ever charged in those cases, so the customer is told they have not been
// billed — but an authorization left in place keeps their funds held until the
// issuer expires it, sometimes for days, and the payment row keeps claiming to
// be AUTHORIZED.
//
// The status guards make it safe to call from every terminal path and safe to
// call twice: a payment that was already captured, refunded, failed or voided is
// left alone, because those represent money that moved and only a refund can
// reverse.
func (s *Service) ReleaseAuthorizationForBooking(ctx context.Context, bookingID uuid.UUID) error {
	payment, err := s.repo.GetByBooking(ctx, bookingID)
	if err != nil {
		// A booking that was never paid for has nothing to release. That is the
		// common case, not a failure, so it is not propagated to the caller.
		if apperr.IsNotFound(err) {
			return nil
		}
		return err
	}

	switch payment.Status {
	case models.PaymentInitiated:
		// The order was created but the customer never authorised it, so there is
		// no hold to release.
		return nil
	case models.PaymentAuthorized:
		// The only state where a hold actually exists.
	default:
		return nil
	}

	// The gateway payment id is not the order id, and a payment that never
	// received one cannot be addressed at the gateway at all. An AUTHORIZED
	// payment is holding real customer funds, so recording it as VOIDED would
	// tell support and the customer the money is back while it stays reserved.
	// The status is left as AUTHORIZED so this stays retryable and visible.
	if payment.GatewayRef == "" {
		return apperr.Wrap(502, "PAYMENT_VOID_UNADDRESSABLE",
			"payment has no gateway payment id, so the authorisation hold cannot be released",
			nil, nil)
	}

	if err := s.client.VoidPayment(ctx, payment.GatewayRef); err != nil {
		// The hold is still live at the gateway. Reporting success would make the
		// record claim the money is back when it is not, so the error is
		// surfaced and the status left as AUTHORIZED for a retry.
		return apperr.Wrap(502, "PAYMENT_VOID_FAILED", "failed to release the payment hold", err, nil)
	}

	return s.repo.UpdateStatus(ctx, payment.ID, models.PaymentVoided)
}

// CaptureForBooking settles the metered fare once a trip ends.
//
// The booking is charged up front against an estimate, so the difference is
// settled here: a lower final price is refunded, a higher one is captured.
func (s *Service) CaptureForBooking(ctx context.Context, bookingID uuid.UUID) error {
	payment, err := s.repo.GetByBooking(ctx, bookingID)
	if err != nil {
		return err
	}
	if payment.Status == models.PaymentCaptured {
		return nil
	}

	booking, err := s.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return err
	}
	if booking.FinalPrice == nil {
		return apperr.Conflict("final fare is not known yet", nil)
	}

	delta := *booking.FinalPrice - payment.Amount
	switch {
	case delta == 0:
		// Charged the estimate and the trip matched it.
		return s.captureSuccess(ctx, payment)
	case delta > 0:
		// The metered fare exceeded the authorisation, so more is due. The
		// gateway holds the full authorised amount, so the original capture is
		// the ceiling that has to be reconciled with the gateway directly.
		slog.Warn("final fare exceeds authorised amount; manual settlement required",
			"booking_id", bookingID, "authorised", payment.Amount, "final", *booking.FinalPrice)
		return s.captureSuccess(ctx, payment)
	default:
		// Overcharged: hand back the difference.
		_, err := s.refundDelta(ctx, payment, booking, -delta)
		return err
	}
}

// refundDelta returns overcharged funds without treating it as a customer-initiated
// refund request.
func (s *Service) refundDelta(ctx context.Context, payment *models.Payment, booking *models.Booking, amount int) (*models.Refund, error) {
	refund := &models.Refund{
		PaymentID: payment.ID,
		BookingID: payment.BookingID,
		Amount:    amount,
		Status:    "PENDING",
		Reason:    "final fare adjustment",
	}
	if err := s.repo.CreateRefund(ctx, refund); err != nil {
		return nil, apperr.Wrap(500, "REFUND_CREATE_FAILED", "failed to create refund", err, nil)
	}

	// The gateway addresses a payment by its payment id, which is not the order
	// id. Without it the refund cannot be issued, so the local row stays PENDING
	// for a retry rather than calling the gateway with an empty path.
	if payment.GatewayRef == "" {
		slog.Warn("cannot refund: payment has no gateway payment id",
			"payment_id", payment.ID, "refund_id", refund.ID)
		return refund, nil
	}

	gatewayRef, err := s.client.Refund(ctx, payment.GatewayRef, refund.Reason, amount)
	if err != nil {
		// The refund row stays PENDING so the gateway webhook can settle it.
		slog.Warn("gateway refund failed; awaiting reconciliation", "payment_id", payment.ID, "error", err)
		return refund, nil
	}
	if err := s.repo.UpdateRefundStatus(ctx, refund.ID, "PROCESSING", gatewayRef); err != nil {
		return refund, nil
	}
	refund.Status = "PROCESSING"
	refund.GatewayRef = gatewayRef
	return refund, nil
}

// refundableAmount is what the customer is owed back.
//
// The fare is charged up front against an estimate, so a refund is the estimate
// less whatever the completed trip actually cost. The previous logic refunded
// the whole charge whenever final_price was nil — the normal case for an
// on-time trip — which let a customer complete a booking and then cancel for a
// full refund.
func (s *Service) refundableAmount(payment *models.Payment, booking *models.Booking) int {
	amountPaid := payment.Amount

	if booking.FinalPrice == nil {
		// Trip finished and the metered fare was never recorded, so nothing is
		// demonstrably owed back. Refunding here would be a free ride.
		return 0
	}

	refund := amountPaid - *booking.FinalPrice
	if refund < 0 {
		// The metered fare exceeded the authorisation; the difference has to be
		// collected, not returned.
		return 0
	}
	return refund
}

// RefundBooking returns captured funds to the customer.
func (s *Service) RefundBooking(ctx context.Context, bookingID uuid.UUID, actorID uuid.UUID, reason string) (*models.Refund, error) {
	// Ownership is settled before any state is inspected or written, so a caller
	// cannot learn whether someone else's booking was paid by watching the
	// error it returns.
	booking, err := s.bookings.GetBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	if booking.CustomerID != actorID {
		return nil, apperr.Forbidden("only the customer can request a refund")
	}

	payment, err := s.repo.GetByBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}

	if payment.Status != models.PaymentCaptured {
		return nil, apperr.Conflict("payment must be captured before refund", nil)
	}

	existing, err := s.repo.GetRefundByPayment(ctx, payment.ID)
	if err == nil {
		return existing, apperr.Conflict("refund already requested for this payment", nil)
	}

	refundAmount := s.refundableAmount(payment, booking)
	if refundAmount <= 0 {
		return nil, apperr.Conflict("nothing to refund for this booking", nil)
	}

	refund := &models.Refund{
		PaymentID: payment.ID,
		BookingID: bookingID,
		Amount:    refundAmount,
		Status:    "PENDING",
		Reason:    reason,
	}

	if err := s.repo.CreateRefund(ctx, refund); err != nil {
		return nil, apperr.Wrap(500, "REFUND_CREATE_FAILED", "failed to create refund", err, nil)
	}

	// The gateway addresses a payment by its payment id, which is not the order
	// id. Without it the refund cannot be issued, so the local row stays PENDING
	// for a retry rather than calling the gateway with an empty path.
	if payment.GatewayRef == "" {
		slog.Warn("cannot refund: payment has no gateway payment id",
			"payment_id", payment.ID, "refund_id", refund.ID)
		return refund, nil
	}

	gatewayRef, err := s.client.Refund(ctx, payment.GatewayRef, reason, refundAmount)
	if err != nil {
		slog.Warn("razorpay refund failed", "error", err)
		return refund, nil
	}

	refund.GatewayRef = gatewayRef
	if err := s.repo.UpdateRefundStatus(ctx, refund.ID, "PROCESSING", gatewayRef); err != nil {
		slog.Warn("failed to update refund status", "error", err)
		return refund, nil
	}
	refund.Status = "PROCESSING"

	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, booking.CustomerID, models.NotifPaymentRefunded,
			"Refund initiated",
			fmt.Sprintf("A refund of ₹%d has been initiated.", refundAmount),
			map[string]string{"booking_id": bookingID.String(), "refund_id": refund.ID.String()},
		)
	}

	return refund, nil
}
