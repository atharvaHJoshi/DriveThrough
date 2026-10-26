package models

import (
	"time"

	"github.com/google/uuid"
)

type NotificationType string

const (
	NotifBookingCreated   NotificationType = "BOOKING_CREATED"
	NotifBookingSearching NotificationType = "BOOKING_SEARCHING"
	NotifBookingAssigned  NotificationType = "DRIVER_ACCEPTED"
	NotifDriverEnRoute    NotificationType = "DRIVER_EN_ROUTE"
	NotifDriverArrived    NotificationType = "DRIVER_ARRIVED"
	NotifDriverNoShow     NotificationType = "DRIVER_NO_SHOW"
	NotifOTPVerified      NotificationType = "OTP_VERIFIED"
	NotifTripStarted      NotificationType = "TRIP_STARTED"
	NotifTripCompleted    NotificationType = "TRIP_COMPLETED"
	NotifPaymentSuccess   NotificationType = "PAYMENT_SUCCESS"
	NotifPaymentRefunded  NotificationType = "PAYMENT_REFUNDED"
	NotifNewBooking       NotificationType = "NEW_BOOKING"
	NotifBookingCancelled NotificationType = "BOOKING_CANCELLED"
	NotifDriverRated      NotificationType = "DRIVER_RATED"
	NotifCustomerRated    NotificationType = "CUSTOMER_RATED"
	NotifSOS              NotificationType = "SOS_TRIGGERED"
)

type Notification struct {
	ID        uuid.UUID         `json:"id" db:"id"`
	UserID    uuid.UUID         `json:"user_id" db:"user_id"`
	Type      NotificationType  `json:"type" db:"type"`
	Title     string            `json:"title" db:"title"`
	Body      string            `json:"body" db:"body"`
	Data      map[string]string `json:"data,omitempty" db:"data"`
	IsRead    bool              `json:"is_read" db:"is_read"`
	CreatedAt time.Time         `json:"created_at" db:"created_at"`
}

type DeviceToken struct {
	ID        uuid.UUID `json:"id" db:"id"`
	UserID    uuid.UUID `json:"user_id" db:"user_id"`
	Token     string    `json:"token" db:"token"`
	Platform  string    `json:"platform" db:"platform"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
