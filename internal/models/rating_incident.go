package models

import (
	"time"

	"github.com/google/uuid"
)

type Rating struct {
	ID        uuid.UUID `json:"id" db:"id"`
	BookingID uuid.UUID `json:"booking_id" db:"booking_id"`
	FromUser  uuid.UUID `json:"from_user" db:"from_user"`
	ToUser    uuid.UUID `json:"to_user" db:"to_user"`
	Rating    int       `json:"rating" db:"rating"`
	Comment   *string   `json:"comment,omitempty" db:"comment"`
	Tags      []string  `json:"tags,omitempty" db:"tags"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type IncidentType string

const (
	IncidentAccident IncidentType = "ACCIDENT"
	IncidentSOS      IncidentType = "SOS"
	IncidentDamage   IncidentType = "DAMAGE"
	IncidentDispute  IncidentType = "DISPUTE"
	IncidentFraud    IncidentType = "FRAUD"
	IncidentOther    IncidentType = "OTHER"
)

type IncidentSeverity string

const (
	SeverityLow      IncidentSeverity = "LOW"
	SeverityMedium   IncidentSeverity = "MEDIUM"
	SeverityHigh     IncidentSeverity = "HIGH"
	SeverityCritical IncidentSeverity = "CRITICAL"
)

type Incident struct {
	ID          uuid.UUID        `json:"id" db:"id"`
	BookingID   *uuid.UUID       `json:"booking_id,omitempty" db:"booking_id"`
	TripID      *uuid.UUID       `json:"trip_id,omitempty" db:"trip_id"`
	Type        IncidentType     `json:"type" db:"type"`
	Severity    IncidentSeverity `json:"severity" db:"severity"`
	ReportedBy  uuid.UUID        `json:"reported_by" db:"reported_by"`
	Latitude    *float64         `json:"latitude,omitempty" db:"latitude"`
	Longitude   *float64         `json:"longitude,omitempty" db:"longitude"`
	Description string           `json:"description" db:"description"`
	Photos      []string         `json:"photos,omitempty" db:"photos"`
	Status      string           `json:"status" db:"status"`
	Resolution  *string          `json:"resolution,omitempty" db:"resolution"`
	CreatedAt   time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at" db:"updated_at"`
}

type SOS struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	UserID       uuid.UUID  `json:"user_id" db:"user_id"`
	TripID       *uuid.UUID `json:"trip_id,omitempty" db:"trip_id"`
	Latitude     float64    `json:"latitude" db:"latitude"`
	Longitude    float64    `json:"longitude" db:"longitude"`
	Kind         string     `json:"kind" db:"kind"`
	Status       string     `json:"status" db:"status"`
	RespondedBy  *uuid.UUID `json:"responded_by,omitempty" db:"responded_by"`
	ResponseNote *string    `json:"response_note,omitempty" db:"response_note"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty" db:"resolved_at"`
}

type EmergencyContact struct {
	ID        uuid.UUID `json:"id" db:"id"`
	UserID    uuid.UUID `json:"user_id" db:"user_id"`
	Name      string    `json:"name" db:"name"`
	Phone     string    `json:"phone" db:"phone"`
	Relation  string    `json:"relation" db:"relation"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
