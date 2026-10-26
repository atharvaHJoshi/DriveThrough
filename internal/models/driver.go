package models

import (
	"time"

	"github.com/google/uuid"
)

type DriverStatus string

const (
	DriverStatusOffline  DriverStatus = "OFFLINE"
	DriverStatusOnline   DriverStatus = "ONLINE"
	DriverStatusBusy     DriverStatus = "BUSY"
	DriverStatusInactive DriverStatus = "INACTIVE"
)

type VerificationStatus string

const (
	VerificationPending   VerificationStatus = "PENDING"
	VerificationSubmitted VerificationStatus = "SUBMITTED"
	VerificationApproved  VerificationStatus = "APPROVED"
	VerificationRejected  VerificationStatus = "REJECTED"
	VerificationSuspended VerificationStatus = "SUSPENDED"
)

type DocumentType string

const (
	DocAadhaar      DocumentType = "AADHAAR"
	DocPAN          DocumentType = "PAN"
	DocDrivingLic   DocumentType = "DRIVING_LICENCE"
	DocAddressProof DocumentType = "ADDRESS_PROOF"
)

type VehicleCapability struct {
	Manual    bool `json:"manual" db:"manual"`
	Automatic bool `json:"automatic" db:"automatic"`
	Hatchback bool `json:"hatchback" db:"hatchback"`
	Sedan     bool `json:"sedan" db:"sedan"`
	SUV       bool `json:"suv" db:"suv"`
	Luxury    bool `json:"luxury" db:"luxury"`
}

type Driver struct {
	ID                 uuid.UUID          `json:"id" db:"id"`
	UserID             uuid.UUID          `json:"user_id" db:"user_id"`
	Status             DriverStatus       `json:"status" db:"status"`
	VerificationStatus VerificationStatus `json:"verification_status" db:"verification_status"`
	Rating             float64            `json:"rating" db:"rating"`
	TotalTrips         int                `json:"total_trips" db:"total_trips"`
	TotalRatings       int                `json:"total_ratings" db:"total_ratings"`
	ExperienceYears    int                `json:"experience_years" db:"experience_years"`
	AcceptanceRate     float64            `json:"acceptance_rate" db:"acceptance_rate"`
	CancellationRate   float64            `json:"cancellation_rate" db:"cancellation_rate"`
	TotalOffers        int                `json:"total_offers" db:"total_offers"`
	AcceptedOffers     int                `json:"accepted_offers" db:"accepted_offers"`
	TotalCancellations int                `json:"total_cancellations" db:"total_cancellations"`
	CanDriveManual     bool               `json:"can_drive_manual" db:"can_drive_manual"`
	CanDriveAutomatic  bool               `json:"can_drive_automatic" db:"can_drive_automatic"`
	CanDriveHatchback  bool               `json:"can_drive_hatchback" db:"can_drive_hatchback"`
	CanDriveSedan      bool               `json:"can_drive_sedan" db:"can_drive_sedan"`
	CanDriveSUV        bool               `json:"can_drive_suv" db:"can_drive_suv"`
	CanDriveLuxury     bool               `json:"can_drive_luxury" db:"can_drive_luxury"`
	CurrentLocation    *Location          `json:"current_location" db:"current_location"`
	EarningsTotal      int                `json:"earnings_total" db:"earnings_total"`
	CreatedAt          time.Time          `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at" db:"updated_at"`
}

// PublicDriver is the subset of a driver profile that other users may read.
//
// The full Driver row carries the linked account id, the driver's exact live
// position, their earnings, and internal offer accounting. None of that belongs
// to a customer browsing available drivers, so the cross-user endpoint returns
// this instead of the raw model.
type PublicDriver struct {
	ID                 uuid.UUID          `json:"id"`
	Status             DriverStatus       `json:"status"`
	VerificationStatus VerificationStatus `json:"verification_status"`
	Rating             float64            `json:"rating"`
	TotalTrips         int                `json:"total_trips"`
	TotalRatings       int                `json:"total_ratings"`
	ExperienceYears    int                `json:"experience_years"`
	AcceptanceRate     float64            `json:"acceptance_rate"`
	CancellationRate   float64            `json:"cancellation_rate"`
	CanDriveManual     bool               `json:"can_drive_manual"`
	CanDriveAutomatic  bool               `json:"can_drive_automatic"`
	CanDriveHatchback  bool               `json:"can_drive_hatchback"`
	CanDriveSedan      bool               `json:"can_drive_sedan"`
	CanDriveSUV        bool               `json:"can_drive_suv"`
	CanDriveLuxury     bool               `json:"can_drive_luxury"`
}

// Public projects a driver onto the customer-visible fields.
func (d *Driver) Public() PublicDriver {
	return PublicDriver{
		ID:                 d.ID,
		Status:             d.Status,
		VerificationStatus: d.VerificationStatus,
		Rating:             d.Rating,
		TotalTrips:         d.TotalTrips,
		TotalRatings:       d.TotalRatings,
		ExperienceYears:    d.ExperienceYears,
		AcceptanceRate:     d.AcceptanceRate,
		CancellationRate:   d.CancellationRate,
		CanDriveManual:     d.CanDriveManual,
		CanDriveAutomatic:  d.CanDriveAutomatic,
		CanDriveHatchback:  d.CanDriveHatchback,
		CanDriveSedan:      d.CanDriveSedan,
		CanDriveSUV:        d.CanDriveSUV,
		CanDriveLuxury:     d.CanDriveLuxury,
	}
}

type DriverDocument struct {
	ID             uuid.UUID    `json:"id" db:"id"`
	DriverID       uuid.UUID    `json:"driver_id" db:"driver_id"`
	Type           DocumentType `json:"type" db:"type"`
	DocumentNumber string       `json:"document_number" db:"document_number"`
	Status         string       `json:"status" db:"status"`
	ExpiryDate     *time.Time   `json:"expiry_date,omitempty" db:"expiry_date"`
	DocumentURL    string       `json:"document_url" db:"document_url"`
	VerifiedAt     *time.Time   `json:"verified_at,omitempty" db:"verified_at"`
	CreatedAt      time.Time    `json:"created_at" db:"created_at"`
}

type DriverEarning struct {
	ID        uuid.UUID `json:"id" db:"id"`
	DriverID  uuid.UUID `json:"driver_id" db:"driver_id"`
	BookingID uuid.UUID `json:"booking_id" db:"booking_id"`
	Amount    int       `json:"amount" db:"amount"`
	Status    string    `json:"status" db:"status"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
