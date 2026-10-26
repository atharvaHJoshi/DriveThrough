package payments

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// paymentColumns quotes "authorization" because it is a reserved word in
// Postgres. The JSON tag on models.Payment stays unquoted.
//
// gateway_order_id is COALESCEd because it is nullable for payments that predate
// the split, and scanning a NULL into a plain string fails at runtime rather than
// at compile time.
const paymentColumns = `id, booking_id, customer_id, amount, platform_fee, driver_amount,
	"authorization", protection_fee, tax, status, method, gateway_reference,
	COALESCE(gateway_order_id, ''), metadata, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, p *models.Payment) error {
	meta, _ := json.Marshal(p.Metadata)

	return r.pool.QueryRow(ctx, `
		INSERT INTO payments (booking_id, customer_id, amount, platform_fee, driver_amount,
			"authorization", protection_fee, tax, status, method, gateway_reference,
			gateway_order_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+paymentColumns,
		p.BookingID, p.CustomerID, p.Amount, p.PlatformFee, p.DriverAmount,
		p.Authorization, p.ProtectionFee, p.Tax, string(p.Status), p.Method,
		p.GatewayRef, p.GatewayOrderID, meta,
	).Scan(
		&p.ID, &p.BookingID, &p.CustomerID, &p.Amount, &p.PlatformFee, &p.DriverAmount,
		&p.Authorization, &p.ProtectionFee, &p.Tax, &p.Status, &p.Method,
		&p.GatewayRef, &p.GatewayOrderID, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
	)
}

func (r *Repository) GetByBooking(ctx context.Context, bookingID uuid.UUID) (*models.Payment, error) {
	var p models.Payment
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		SELECT `+paymentColumns+` FROM payments WHERE booking_id = $1 ORDER BY created_at DESC LIMIT 1
	`, bookingID).Scan(
		&p.ID, &p.BookingID, &p.CustomerID, &p.Amount, &p.PlatformFee, &p.DriverAmount,
		&p.Authorization, &p.ProtectionFee, &p.Tax, &p.Status, &p.Method,
		&p.GatewayRef, &p.GatewayOrderID, &metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("payment not found for booking")
		}
		return nil, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &p.Metadata)
	}
	return &p, nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*models.Payment, error) {
	var p models.Payment
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		SELECT `+paymentColumns+` FROM payments WHERE id = $1
	`, id).Scan(
		&p.ID, &p.BookingID, &p.CustomerID, &p.Amount, &p.PlatformFee, &p.DriverAmount,
		&p.Authorization, &p.ProtectionFee, &p.Tax, &p.Status, &p.Method,
		&p.GatewayRef, &p.GatewayOrderID, &metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("payment not found")
		}
		return nil, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &p.Metadata)
	}
	return &p, nil
}

// GetByGatewayRef resolves a payment from either gateway identifier.
//
// A webhook names the payment by its payment id, but before any payment exists
// it carries only the order id we generated. Both are accepted so an event that
// arrives with either identifier resolves to the same row.
func (r *Repository) GetByGatewayRef(ctx context.Context, gatewayRef string) (*models.Payment, error) {
	var p models.Payment
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		SELECT `+paymentColumns+` FROM payments
		WHERE gateway_reference = $1 OR gateway_order_id = $1
	`, gatewayRef).Scan(
		&p.ID, &p.BookingID, &p.CustomerID, &p.Amount, &p.PlatformFee, &p.DriverAmount,
		&p.Authorization, &p.ProtectionFee, &p.Tax, &p.Status, &p.Method,
		&p.GatewayRef, &p.GatewayOrderID, &metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("payment not found")
		}
		return nil, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &p.Metadata)
	}
	return &p, nil
}

// RecordGatewayPaymentID stores the gateway's payment id against the payment.
//
// This is the step that makes capture, void, and refund address the right
// object. Those calls take a payment id, and the row only has the order id until
// a webhook reveals the payment id, so without persisting it here they would be
// sent the order id and rejected by the gateway.
//
// A no-op when the id is unchanged or absent, which keeps it safe to call on
// every event and makes redelivery harmless.
func (r *Repository) RecordGatewayPaymentID(ctx context.Context, id uuid.UUID, paymentID string) error {
	if paymentID == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE payments SET gateway_reference = $2, updated_at = NOW()
		WHERE id = $1 AND (gateway_reference IS NULL OR gateway_reference <> $2)
	`, id, paymentID)
	return err
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status models.PaymentStatus) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE payments SET status = $1, updated_at = NOW() WHERE id = $2
	`, string(status), id)
	return err
}

func (r *Repository) UpdateGatewayRefAndMethod(ctx context.Context, id uuid.UUID, status models.PaymentStatus, method models.PaymentMethod, gatewayRef string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE payments SET status = $1, method = $2, gateway_reference = $3, updated_at = NOW()
		WHERE id = $4
	`, string(status), string(method), gatewayRef, id)
	return err
}

// MarkCaptured records that the gateway holds the funds, and reports whether
// this caller was the one that moved the row.
//
// Restricting the write to the pre-capture statuses keeps a late or reordered
// event from resurrecting a settled payment.
//
// The boolean is not a "you own the follow-on work" signal and must not be used
// as one. Winning this write does not mean the driver was credited: an attempt
// can take the transition and then fail, leaving the payment CAPTURED with no
// earnings recorded. Treating a lost race as permission to skip the credit
// stranded drivers permanently, because the CAPTURED status is exactly what
// suppresses the credit on the next attempt. The credit is keyed on the booking
// and is safe to apply on every delivery.
func (r *Repository) MarkCaptured(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payments SET status = 'CAPTURED', updated_at = NOW()
		WHERE id = $1 AND status IN ('INITIATED', 'AUTHORIZED')
	`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimGatewayEvent takes ownership of a webhook delivery, reporting whether the
// caller should process it.
//
// This is a leased inbox, and the lease is what makes it correct in both
// directions:
//
//   - Without processed_at being set on insert, a delivery whose processing
//     failed transiently stays claimable, so the gateway's own retry is processed
//     rather than discarded as a duplicate. Previously the event was marked
//     handled before any work was done, so a single dropped connection left the
//     local payment state permanently diverged from the gateway's.
//   - Without a lease, "unprocessed" would be satisfied by every concurrent
//     redelivery at once and they would all process the same capture, crediting
//     earnings repeatedly. Only the holder of a live claim proceeds; an abandoned
//     claim expires so a retry can still recover it.
//
// A duplicate delivery is reported as not-claimed rather than as an error,
// because from the gateway's point of view a redelivery is a success.
func (r *Repository) ClaimGatewayEvent(ctx context.Context, eventID, provider, eventType string, raw []byte) (bool, error) {
	claimed, err := r.claimGatewayEvent(ctx, eventID, provider, eventType, raw, claimLease)
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// claimLease bounds how long one worker may hold an unprocessed event before
// another delivery is allowed to take it over. It has to exceed the slowest
// plausible processing step and stay far below the gateway's retry interval, so
// that a claim is never stolen from a worker that is still making progress.
const claimLease = 30 * time.Second

// claimGatewayEvent performs one claim attempt. The lease is a parameter so
// tests can exercise expiry without waiting.
func (r *Repository) claimGatewayEvent(ctx context.Context, eventID, provider, eventType string, raw []byte, lease time.Duration) (bool, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO gateway_events (id, provider, event_type, payload, claimed_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE
			SET claimed_at = NOW()
			WHERE gateway_events.processed_at IS NULL
			  AND (gateway_events.claimed_at IS NULL
			       OR gateway_events.claimed_at < NOW() - make_interval(secs => $5))
		RETURNING id
	`, eventID, provider, eventType, raw, lease.Seconds()).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already processed, or another delivery holds a live claim.
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CompleteGatewayEvent marks a claimed event as finished. It is called only
// after the work has succeeded, so a failure anywhere before this point leaves
// the event claimable for the gateway's next retry.
func (r *Repository) CompleteGatewayEvent(ctx context.Context, eventID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE gateway_events SET processed_at = NOW() WHERE id = $1`, eventID)
	return err
}

func (r *Repository) CreateRefund(ctx context.Context, refund *models.Refund) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO refunds (payment_id, booking_id, amount, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id, payment_id, booking_id, amount, status, gateway_reference, reason, created_at
	`, refund.PaymentID, refund.BookingID, refund.Amount, refund.Reason).Scan(
		&refund.ID, &refund.PaymentID, &refund.BookingID, &refund.Amount,
		&refund.Status, &refund.GatewayRef, &refund.Reason, &refund.CreatedAt,
	)
}

func (r *Repository) UpdateRefundStatus(ctx context.Context, refundID uuid.UUID, status, gatewayRef string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE refunds SET status = $1,
			gateway_reference = CASE WHEN $2 = '' THEN gateway_reference ELSE $2 END
		WHERE id = $3
	`, status, gatewayRef, refundID)
	return err
}

func (r *Repository) GetRefundByPayment(ctx context.Context, paymentID uuid.UUID) (*models.Refund, error) {
	var rf models.Refund
	err := r.pool.QueryRow(ctx, `
		SELECT id, payment_id, booking_id, amount, status, gateway_reference, reason, created_at
		FROM refunds WHERE payment_id = $1 ORDER BY created_at DESC LIMIT 1
	`, paymentID).Scan(
		&rf.ID, &rf.PaymentID, &rf.BookingID, &rf.Amount,
		&rf.Status, &rf.GatewayRef, &rf.Reason, &rf.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("refund not found")
		}
		return nil, err
	}
	return &rf, nil
}

func (r *Repository) UpdateStatusByGatewayRef(ctx context.Context, gatewayRef string, status models.PaymentStatus) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payments SET status = $1, updated_at = NOW() WHERE gateway_reference = $2
	`, string(status), gatewayRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("payment not found for gateway ref")
	}
	return nil
}
