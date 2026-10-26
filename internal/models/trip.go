package models

import (
	"time"

	"github.com/google/uuid"
)

// Trip lifecycle states. These mirror the trips.status CHECK constraint; they
// exist so callers compare against a named constant instead of a bare literal
// that a schema change would silently invalidate.
const (
	TripStatusActive    = "ACTIVE"
	TripStatusPaused    = "PAUSED"
	TripStatusCompleted = "COMPLETED"
)

type Trip struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	BookingID     uuid.UUID  `json:"booking_id" db:"booking_id"`
	DriverID      uuid.UUID  `json:"driver_id" db:"driver_id"`
	VehicleID     uuid.UUID  `json:"vehicle_id" db:"vehicle_id"`
	StartedAt     time.Time  `json:"started_at" db:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty" db:"ended_at"`
	StartOdometer int        `json:"start_odometer" db:"start_odometer"`
	EndOdometer   *int       `json:"end_odometer,omitempty" db:"end_odometer"`
	StartFuel     int        `json:"start_fuel" db:"start_fuel"`
	EndFuel       *int       `json:"end_fuel,omitempty" db:"end_fuel"`
	TotalDistance float64    `json:"total_distance" db:"total_distance"`
	Status        string     `json:"status" db:"status"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

type LocationUpdate struct {
	TripID    uuid.UUID `json:"trip_id"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Accuracy  float64   `json:"accuracy"`
	Heading   float64   `json:"heading"`
	Speed     float64   `json:"speed"`
	Timestamp time.Time `json:"timestamp"`
}

type VehicleInspection struct {
	ID          uuid.UUID `json:"id" db:"id"`
	TripID      uuid.UUID `json:"trip_id" db:"trip_id"`
	Type        string    `json:"type" db:"type"`
	Odometer    int       `json:"odometer" db:"odometer"`
	FuelLevel   int       `json:"fuel_level" db:"fuel_level"`
	Photos      []string  `json:"photos" db:"photos"`
	DamageNotes *string   `json:"damage_notes,omitempty" db:"damage_notes"`
	HasDamage   bool      `json:"has_damage" db:"has_damage"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}
