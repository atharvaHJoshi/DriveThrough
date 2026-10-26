package models

import (
	"time"

	"github.com/google/uuid"
)

type BookingType string

const (
	BookingTypeHourly    BookingType = "HOURLY"
	BookingTypeTrip      BookingType = "TRIP"
	BookingTypeRoundTrip BookingType = "ROUND_TRIP"
)

type BookingStatus string

const (
	BookingRequested         BookingStatus = "REQUESTED"
	BookingSearching         BookingStatus = "SEARCHING"
	BookingAssigned          BookingStatus = "DRIVER_ASSIGNED"
	BookingEnRoute           BookingStatus = "DRIVER_EN_ROUTE"
	BookingArrived           BookingStatus = "DRIVER_ARRIVED"
	BookingOTPVerified       BookingStatus = "OTP_VERIFIED"
	BookingStarted           BookingStatus = "TRIP_STARTED"
	BookingPaused            BookingStatus = "TRIP_PAUSED"
	BookingResumed           BookingStatus = "TRIP_RESUMED"
	BookingCompleted         BookingStatus = "TRIP_COMPLETED"
	BookingCancelled         BookingStatus = "CANCELLED"
	BookingCancelledByDriver BookingStatus = "CANCELLED_BY_DRIVER"
	// BookingCancelledByAdmin is distinct from a driver cancelling: it is an
	// administrator revoking a booking, so it does not count against the driver's
	// cancellation rate, and unlike CANCELLED_BY_DRIVER it is reachable after the
	// vehicle has been handed over.
	BookingCancelledByAdmin BookingStatus = "CANCELLED_BY_ADMIN"
	BookingDriverNoShow     BookingStatus = "DRIVER_NO_SHOW"
	BookingPaymentFailed    BookingStatus = "PAYMENT_FAILED"
	BookingSupportEscalated BookingStatus = "SUPPORT_ESCALATION"
	BookingAccident         BookingStatus = "ACCIDENT"
)

type GeoPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type PriceBreakdown struct {
	DriverFee  int `json:"driver_fee"`
	Platform   int `json:"platform_fee"`
	Protection int `json:"protection_fee"`
	Tax        int `json:"tax"`
	Total      int `json:"total"`
}

type Booking struct {
	ID             uuid.UUID     `json:"id" db:"id"`
	CustomerID     uuid.UUID     `json:"customer_id" db:"customer_id"`
	DriverID       *uuid.UUID    `json:"driver_id,omitempty" db:"driver_id"`
	VehicleID      uuid.UUID     `json:"vehicle_id" db:"vehicle_id"`
	BookingType    BookingType   `json:"booking_type" db:"booking_type"`
	DurationHours  int           `json:"duration_hours" db:"duration_hours"`
	PickupLocation string        `json:"pickup_location" db:"pickup_location"`
	PickupLat      float64       `json:"pickup_lat" db:"pickup_lat"`
	PickupLng      float64       `json:"pickup_lng" db:"pickup_lng"`
	Destination    *string       `json:"destination,omitempty" db:"destination"`
	DestLat        *float64      `json:"dest_lat,omitempty" db:"dest_lat"`
	DestLng        *float64      `json:"dest_lng,omitempty" db:"dest_lng"`
	ScheduledAt    *time.Time    `json:"scheduled_at,omitempty" db:"scheduled_at"`
	Status         BookingStatus `json:"status" db:"status"`
	OTPCode        string        `json:"-" db:"otp_code"`
	OTPAttempts    int           `json:"-" db:"otp_attempts"`
	EstimatedPrice int           `json:"estimated_price" db:"estimated_price"`
	FinalPrice     *int          `json:"final_price,omitempty" db:"final_price"`
	DriverFee      int           `json:"driver_fee" db:"driver_fee"`
	PlatformFee    int           `json:"platform_fee" db:"platform_fee"`
	ProtectionFee  int           `json:"protection_fee" db:"protection_fee"`
	Tax            int           `json:"tax" db:"tax"`
	CreatedAt      time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at" db:"updated_at"`
}

func (b *Booking) PriceBreakdown() PriceBreakdown {
	return PriceBreakdown{
		DriverFee:  b.DriverFee,
		Platform:   b.PlatformFee,
		Protection: b.ProtectionFee,
		Tax:        b.Tax,
		Total:      b.EstimatedPrice,
	}
}

type BookingEvent struct {
	ID        uuid.UUID `json:"id" db:"id"`
	BookingID uuid.UUID `json:"booking_id" db:"booking_id"`
	Status    string    `json:"status" db:"status"`
	Notes     string    `json:"notes,omitempty" db:"notes"`
	ActorID   uuid.UUID `json:"actor_id" db:"actor_id"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
