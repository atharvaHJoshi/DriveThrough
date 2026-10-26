package vehicles

import (
	"context"

	"github.com/google/uuid"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/validator"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) AddVehicle(ctx context.Context, userID uuid.UUID, input models.Vehicle) (*models.Vehicle, error) {
	if input.Make == "" || input.Model == "" {
		return nil, apperr.BadRequest("make and model are required", nil)
	}
	if input.Year < 1990 || input.Year > 2100 {
		return nil, apperr.BadRequest("invalid vehicle year", nil)
	}

	reg := validator.NormalizeRegNumber(input.RegistrationNumber)
	if !validator.IsValidRegistrationNumber(reg) {
		return nil, apperr.BadRequest("invalid registration number", nil)
	}
	input.RegistrationNumber = reg
	input.UserID = userID

	vehicles, err := s.repo.GetAllByUser(ctx, userID)
	if err != nil {
		return nil, apperr.Wrap(500, "VEHICLE_LIST_FAILED", "failed to list vehicles", err, nil)
	}

	input.IsDefault = len(vehicles) == 0

	return s.repo.Create(ctx, &input)
}

func (s *Service) ListVehicles(ctx context.Context, userID uuid.UUID) ([]models.Vehicle, error) {
	return s.repo.GetAllByUser(ctx, userID)
}

func (s *Service) GetVehicle(ctx context.Context, id uuid.UUID) (*models.Vehicle, error) {
	return s.repo.GetByID(ctx, id)
}

// GetVehicleForOwner returns a vehicle only to the customer who owns it.
func (s *Service) GetVehicleForOwner(ctx context.Context, id, userID uuid.UUID) (*models.Vehicle, error) {
	vehicle, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if vehicle.UserID != userID {
		return nil, apperr.Forbidden("vehicle does not belong to you")
	}
	return vehicle, nil
}

// UpdateVehicleInput is a partial update. A nil field means "leave unchanged",
// which is what PATCH requires. The previous handler decoded straight into
// models.Vehicle and assigned every field, so a body carrying only {"year":
// 2021} silently blanked the make, model, transmission, fuel type and class.
type UpdateVehicleInput struct {
	RegistrationNumber *string                  `json:"registration_number"`
	Make               *string                  `json:"make"`
	Model              *string                  `json:"model"`
	Year               *int                     `json:"year"`
	Transmission       *models.TransmissionType `json:"transmission"`
	FuelType           *models.FuelType         `json:"fuel_type"`
	Class              *models.VehicleClass     `json:"class"`
}

var validTransmissions = map[models.TransmissionType]bool{
	models.TransmissionManual:    true,
	models.TransmissionAutomatic: true,
}

var validFuelTypes = map[models.FuelType]bool{
	models.FuelPetrol:   true,
	models.FuelDiesel:   true,
	models.FuelCNG:      true,
	models.FuelElectric: true,
	models.FuelHybrid:   true,
}

var validClasses = map[models.VehicleClass]bool{
	models.VehicleClassHatchback: true,
	models.VehicleClassSedan:     true,
	models.VehicleClassSUV:       true,
	models.VehicleClassLuxury:    true,
	models.VehicleClassMUV:       true,
}

func (s *Service) UpdateVehicle(ctx context.Context, id, userID uuid.UUID, input UpdateVehicleInput) (*models.Vehicle, error) {
	if input.RegistrationNumber == nil && input.Make == nil && input.Model == nil &&
		input.Year == nil && input.Transmission == nil && input.FuelType == nil && input.Class == nil {
		return nil, apperr.BadRequest("no updatable fields supplied", nil)
	}

	// Validate everything before touching the row so a rejected field cannot
	// leave the vehicle half-updated.
	if input.Year != nil && (*input.Year < 1990 || *input.Year > 2100) {
		return nil, apperr.BadRequest("invalid vehicle year", nil)
	}
	if input.Transmission != nil && !validTransmissions[*input.Transmission] {
		return nil, apperr.BadRequest("invalid transmission type", nil)
	}
	if input.FuelType != nil && !validFuelTypes[*input.FuelType] {
		return nil, apperr.BadRequest("invalid fuel type", nil)
	}
	if input.Class != nil && !validClasses[*input.Class] {
		return nil, apperr.BadRequest("invalid vehicle class", nil)
	}
	if input.RegistrationNumber != nil {
		reg := validator.NormalizeRegNumber(*input.RegistrationNumber)
		if !validator.IsValidRegistrationNumber(reg) {
			return nil, apperr.BadRequest("invalid registration number", nil)
		}
		input.RegistrationNumber = &reg
	}
	if input.Make != nil && *input.Make == "" {
		return nil, apperr.BadRequest("make cannot be empty", nil)
	}
	if input.Model != nil && *input.Model == "" {
		return nil, apperr.BadRequest("model cannot be empty", nil)
	}

	// The update is scoped to the owner in SQL, so ownership is enforced by the
	// write itself rather than only by the read above it.
	if err := s.repo.UpdateOwned(ctx, id, userID, input); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

func (s *Service) DeleteVehicle(ctx context.Context, id, userID uuid.UUID) error {
	vehicle, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if vehicle.UserID != userID {
		return apperr.Forbidden("you do not own this vehicle")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Wrap(500, "VEHICLE_DELETE_FAILED", "failed to delete vehicle", err, nil)
	}
	return nil
}

func (s *Service) SetDefaultVehicle(ctx context.Context, id, userID uuid.UUID) error {
	owns, err := s.repo.BelongsToUser(ctx, id, userID)
	if err != nil {
		return apperr.Wrap(500, "VEHICLE_LOOKUP_FAILED", "failed to look up vehicle", err, nil)
	}
	if !owns {
		return apperr.Forbidden("you do not own this vehicle")
	}
	if err := s.repo.SetDefault(ctx, id, userID); err != nil {
		return apperr.Wrap(500, "DEFAULT_UPDATE_FAILED", "failed to set default vehicle", err, nil)
	}
	return nil
}
