package models

import (
	"time"

	"github.com/google/uuid"
)

type PaymentStatus string

const (
	PaymentInitiated  PaymentStatus = "INITIATED"
	PaymentAuthorized PaymentStatus = "AUTHORIZED"
	PaymentCaptured   PaymentStatus = "CAPTURED"
	// PaymentVoided means an authorization was deliberately released because the
	// booking ended without a ride. No money was ever taken, so this is not a
	// refund: it records that the hold was given back rather than left to expire.
	PaymentVoided   PaymentStatus = "VOIDED"
	PaymentFailed   PaymentStatus = "FAILED"
	PaymentRefunded PaymentStatus = "REFUNDED"
	PaymentSettled  PaymentStatus = "SETTLED"
)

type PaymentMethod string

const (
	PaymentMethodCard    PaymentMethod = "CARD"
	PaymentMethodUPI     PaymentMethod = "UPI"
	PaymentMethodNetBank PaymentMethod = "NETBANKING"
	PaymentMethodWallet  PaymentMethod = "WALLET"
)

type Payment struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	BookingID     uuid.UUID      `json:"booking_id" db:"booking_id"`
	CustomerID    uuid.UUID      `json:"customer_id" db:"customer_id"`
	Amount        int            `json:"amount" db:"amount"`
	PlatformFee   int            `json:"platform_fee" db:"platform_fee"`
	DriverAmount  int            `json:"driver_amount" db:"driver_amount"`
	Authorization int            `json:"authorization" db:"authorization"`
	ProtectionFee int            `json:"protection_fee" db:"protection_fee"`
	Tax           int            `json:"tax" db:"tax"`
	Status        PaymentStatus  `json:"status" db:"status"`
	Method        *PaymentMethod `json:"method,omitempty" db:"method"`
	// GatewayRef is the gateway's id for the payment, once one exists. The
	// capture, void, and refund calls all address the payment by this id, so it
	// must never hold the order id.
	GatewayRef string `json:"gateway_reference" db:"gateway_reference"`
	// GatewayOrderID is the id the order was created with, and is what a webhook
	// carries before any payment id exists.
	GatewayOrderID string    `json:"gateway_order_id" db:"gateway_order_id"`
	Metadata       any       `json:"metadata,omitempty" db:"metadata"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

type Refund struct {
	ID         uuid.UUID `json:"id" db:"id"`
	PaymentID  uuid.UUID `json:"payment_id" db:"payment_id"`
	BookingID  uuid.UUID `json:"booking_id" db:"booking_id"`
	Amount     int       `json:"amount" db:"amount"`
	Status     string    `json:"status" db:"status"`
	GatewayRef string    `json:"gateway_reference" db:"gateway_reference"`
	Reason     string    `json:"reason" db:"reason"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
}
