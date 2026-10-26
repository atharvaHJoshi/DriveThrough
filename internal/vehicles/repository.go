package vehicles

import (
	"context"
	"errors"

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

const vehicleColumns = `id, user_id, registration_number, make, model, year, transmission, fuel_type, class, is_default, created_at, updated_at`

// Create inserts a vehicle and clears the previous default in one transaction.
//
// Previously the clear and the insert were separate statements, so a rejected
// insert (a duplicate registration number, say) left the user with no default
// vehicle at all.
func (r *Repository) Create(ctx context.Context, v *models.Vehicle) (*models.Vehicle, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if v.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE vehicles SET is_default = FALSE, updated_at = NOW() WHERE user_id = $1`, v.UserID); err != nil {
			return nil, err
		}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+vehicleColumns,
		v.UserID, v.RegistrationNumber, v.Make, v.Model, v.Year, v.Transmission, v.FuelType, v.Class, v.IsDefault,
	).Scan(
		&v.ID, &v.UserID, &v.RegistrationNumber, &v.Make, &v.Model, &v.Year,
		&v.Transmission, &v.FuelType, &v.Class, &v.IsDefault, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

func (r *Repository) GetAllByUser(ctx context.Context, userID uuid.UUID) ([]models.Vehicle, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+vehicleColumns+` FROM vehicles WHERE user_id = $1 ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Vehicle
	for rows.Next() {
		var v models.Vehicle
		if err := rows.Scan(
			&v.ID, &v.UserID, &v.RegistrationNumber, &v.Make, &v.Model, &v.Year,
			&v.Transmission, &v.FuelType, &v.Class, &v.IsDefault, &v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*models.Vehicle, error) {
	var v models.Vehicle
	err := r.pool.QueryRow(ctx, `
		SELECT `+vehicleColumns+` FROM vehicles WHERE id = $1
	`, id).Scan(
		&v.ID, &v.UserID, &v.RegistrationNumber, &v.Make, &v.Model, &v.Year,
		&v.Transmission, &v.FuelType, &v.Class, &v.IsDefault, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("vehicle not found")
		}
		return nil, err
	}
	return &v, nil
}

// UpdateOwned applies a partial update to a vehicle the caller owns.
//
// The ownership predicate lives in the UPDATE itself, so a vehicle reassigned or
// deleted between the read and the write cannot be written to by the previous
// owner. Only the COALESCE'd, non-nil fields are written.
func (r *Repository) UpdateOwned(ctx context.Context, id, userID uuid.UUID, in UpdateVehicleInput) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE vehicles SET
			registration_number = COALESCE($3, registration_number),
			make                = COALESCE($4, make),
			model               = COALESCE($5, model),
			year                = COALESCE($6, year),
			transmission        = COALESCE($7, transmission),
			fuel_type           = COALESCE($8, fuel_type),
			class               = COALESCE($9, class),
			updated_at          = NOW()
		WHERE id = $1 AND user_id = $2
	`, id, userID, in.RegistrationNumber, in.Make, in.Model, in.Year, in.Transmission, in.FuelType, in.Class)
	if err != nil {
		return apperr.Wrap(500, "VEHICLE_UPDATE_FAILED", "failed to update vehicle", err, nil)
	}

	switch tag.RowsAffected() {
	case 1:
		return nil
	case 0:
		// Either it does not exist or it is not this caller's. Distinguishing the
		// two would leak the existence of other users' vehicles, so report a
		// single not-found.
		return apperr.NotFound("vehicle not found")
	default:
		return apperr.Wrap(500, "VEHICLE_UPDATE_FAILED", "unexpected row count updating vehicle", nil, nil)
	}
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM vehicles WHERE id = $1`, id)
	return err
}

func (r *Repository) SetDefault(ctx context.Context, id, userID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE vehicles SET is_default = FALSE WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE vehicles SET is_default = TRUE WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) BelongsToUser(ctx context.Context, id, userID uuid.UUID) (bool, error) {
	var owned bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM vehicles WHERE id = $1 AND user_id = $2)
	`, id, userID).Scan(&owned)
	if err != nil {
		return false, err
	}
	return owned, nil
}
