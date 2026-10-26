package models

import (
	"time"

	"github.com/google/uuid"
)

type TransmissionType string

const (
	TransmissionManual    TransmissionType = "MANUAL"
	TransmissionAutomatic TransmissionType = "AUTOMATIC"
)

type FuelType string

const (
	FuelPetrol   FuelType = "PETROL"
	FuelDiesel   FuelType = "DIESEL"
	FuelCNG      FuelType = "CNG"
	FuelElectric FuelType = "ELECTRIC"
	FuelHybrid   FuelType = "HYBRID"
)

type VehicleClass string

const (
	VehicleClassHatchback VehicleClass = "HATCHBACK"
	VehicleClassSedan     VehicleClass = "SEDAN"
	VehicleClassSUV       VehicleClass = "SUV"
	VehicleClassLuxury    VehicleClass = "LUXURY"
	VehicleClassMUV       VehicleClass = "MUV"
)

type Vehicle struct {
	ID                 uuid.UUID        `json:"id" db:"id"`
	UserID             uuid.UUID        `json:"user_id" db:"user_id"`
	RegistrationNumber string           `json:"registration_number" db:"registration_number"`
	Make               string           `json:"make" db:"make"`
	Model              string           `json:"model" db:"model"`
	Year               int              `json:"year" db:"year"`
	Transmission       TransmissionType `json:"transmission" db:"transmission"`
	FuelType           FuelType         `json:"fuel_type" db:"fuel_type"`
	Class              VehicleClass     `json:"class" db:"class"`
	IsDefault          bool             `json:"is_default" db:"is_default"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at" db:"updated_at"`
}

func (v *Vehicle) IsVerified() bool {
	return v.RegistrationNumber != ""
}
