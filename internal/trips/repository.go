package trips

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/database"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateTripWithInspection creates the trip and its optional pre-trip
// inspection in a single transaction.
//
// Previously the trip was inserted, then the inspection, then the booking status
// was updated by the caller. A failure on either later step left an ACTIVE trip
// attached to a booking that never started, which is unrecoverable without
// manual intervention. Booking status is still the caller's responsibility, but
// the trip's own rows are now all-or-nothing.
func (r *Repository) CreateTripWithInspection(ctx context.Context, t *models.Trip, insp *models.VehicleInspection) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO trips (booking_id, driver_id, vehicle_id, start_odometer, start_fuel, status)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE')
		RETURNING id, booking_id, driver_id, vehicle_id, started_at, ended_at,
		          start_odometer, end_odometer, start_fuel, end_fuel, total_distance, status, created_at, updated_at
	`, t.BookingID, t.DriverID, t.VehicleID, t.StartOdometer, t.StartFuel).Scan(
		&t.ID, &t.BookingID, &t.DriverID, &t.VehicleID, &t.StartedAt, &t.EndedAt,
		&t.StartOdometer, &t.EndOdometer, &t.StartFuel, &t.EndFuel, &t.TotalDistance, &t.Status, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		// trips.booking_id is UNIQUE, so a concurrent start for the same booking
		// lands here. It is a client-visible race, not a server fault.
		if database.IsUniqueViolation(err) {
			return apperr.Conflict("a trip is already running for this booking", nil)
		}
		return err
	}

	if insp != nil {
		insp.TripID = t.ID
		if err := insertInspection(ctx, tx, insp); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// insertInspection writes a pre- or post-trip inspection. The caller supplies
// the transaction so it composes with the surrounding unit of work.
func insertInspection(ctx context.Context, tx pgx.Tx, insp *models.VehicleInspection) error {
	// A nil Go slice encodes as SQL NULL, which violates the NOT NULL on
	// vehicle_inspections.photos; an explicit empty array matches the default.
	photos := insp.Photos
	if photos == nil {
		photos = []string{}
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO vehicle_inspections (trip_id, type, odometer, fuel_level, photos, damage_notes, has_damage)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, insp.TripID, insp.Type, insp.Odometer, insp.FuelLevel, photos, insp.DamageNotes, insp.HasDamage)
	return err
}

// DeleteTrip removes a trip that was created but never committed to by the
// booking status change. Only ever called on the compensating path, immediately
// after a failed start.
// DeleteTrip removes a trip that never really started, which is how StartTrip
// compensates when the booking status update fails.
//
// Only ACTIVE trips may be removed: a completed trip is a financial and
// safety record and is never deleted through this path.
//
// vehicle_inspections and trip_locations cascade, so the rollback really does
// leave nothing behind. An SOS or incident referencing the trip is RESTRICT and
// blocks the delete instead, because silently discarding an emergency report
// would be far worse than a refused cleanup. That refusal is translated into a
// conflict, since a bare foreign key violation would surface to the operator as
// an unexplained server error.
func (r *Repository) DeleteTrip(ctx context.Context, tripID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM trips WHERE id = $1 AND status = 'ACTIVE'`, tripID)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperr.Conflict("trip is referenced by an emergency record and cannot be removed", nil)
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Conflict("trip is no longer active; refusing to roll back", nil)
	}
	return nil
}

func (r *Repository) GetActiveByBooking(ctx context.Context, bookingID uuid.UUID) (*models.Trip, error) {
	var t models.Trip
	err := r.pool.QueryRow(ctx, `
		SELECT id, booking_id, driver_id, vehicle_id, started_at, ended_at,
		       start_odometer, end_odometer, start_fuel, end_fuel, total_distance, status, created_at, updated_at
		FROM trips WHERE booking_id = $1 AND status IN ('ACTIVE', 'PAUSED')
	`, bookingID).Scan(
		&t.ID, &t.BookingID, &t.DriverID, &t.VehicleID, &t.StartedAt, &t.EndedAt,
		&t.StartOdometer, &t.EndOdometer, &t.StartFuel, &t.EndFuel, &t.TotalDistance, &t.Status, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("no active trip for booking")
		}
		return nil, err
	}
	return &t, nil
}

func (r *Repository) GetByID(ctx context.Context, tripID uuid.UUID) (*models.Trip, error) {
	var t models.Trip
	err := r.pool.QueryRow(ctx, `
		SELECT id, booking_id, driver_id, vehicle_id, started_at, ended_at,
		       start_odometer, end_odometer, start_fuel, end_fuel, total_distance, status, created_at, updated_at
		FROM trips WHERE id = $1
	`, tripID).Scan(
		&t.ID, &t.BookingID, &t.DriverID, &t.VehicleID, &t.StartedAt, &t.EndedAt,
		&t.StartOdometer, &t.EndOdometer, &t.StartFuel, &t.EndFuel, &t.TotalDistance, &t.Status, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("trip not found")
		}
		return nil, err
	}
	return &t, nil
}

// CompleteTrip closes a trip and records its post-trip inspection atomically.
//
// The UPDATE is conditional on the trip still being open, so a double end
// request cannot recompute and overwrite an already-recorded distance.
func (r *Repository) CompleteTrip(ctx context.Context, tripID uuid.UUID, endOdometer, endFuel int, distance float64, post *models.VehicleInspection) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE trips SET
			end_odometer = $2,
			end_fuel = $3,
			total_distance = $4,
			status = 'COMPLETED',
			ended_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 AND status IN ('ACTIVE', 'PAUSED')
	`, tripID, endOdometer, endFuel, distance)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Conflict("trip is not open", nil)
	}

	if post != nil {
		p := *post
		p.TripID = tripID
		p.Type = "POST_TRIP"
		if err := insertInspection(ctx, tx, &p); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (r *Repository) RecordLocation(ctx context.Context, tripID uuid.UUID, loc models.LocationUpdate) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trip_locations (trip_id, latitude, longitude, accuracy, heading, speed, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, tripID, loc.Latitude, loc.Longitude, loc.Accuracy, loc.Heading, loc.Speed, loc.Timestamp)
	return err
}

func (r *Repository) AddInspection(ctx context.Context, insp *models.VehicleInspection) error {
	// A nil Go slice is encoded as SQL NULL, which violates the NOT NULL on
	// vehicle_inspections.photos; an explicit empty array matches the default.
	photos := insp.Photos
	if photos == nil {
		photos = []string{}
	}

	return r.pool.QueryRow(ctx, `
		INSERT INTO vehicle_inspections (trip_id, type, odometer, fuel_level, photos, damage_notes, has_damage)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, trip_id, type, odometer, fuel_level, photos, damage_notes, has_damage, created_at
	`, insp.TripID, insp.Type, insp.Odometer, insp.FuelLevel, photos, insp.DamageNotes, insp.HasDamage).Scan(
		&insp.ID, &insp.TripID, &insp.Type, &insp.Odometer, &insp.FuelLevel, &insp.Photos, &insp.DamageNotes, &insp.HasDamage, &insp.CreatedAt,
	)
}

func (r *Repository) InspectionsForTrip(ctx context.Context, tripID uuid.UUID) ([]models.VehicleInspection, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, trip_id, type, odometer, fuel_level, photos, damage_notes, has_damage, created_at
		FROM vehicle_inspections WHERE trip_id = $1 ORDER BY created_at
	`, tripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.VehicleInspection
	for rows.Next() {
		var i models.VehicleInspection
		if err := rows.Scan(&i.ID, &i.TripID, &i.Type, &i.Odometer, &i.FuelLevel, &i.Photos, &i.DamageNotes, &i.HasDamage, &i.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, rows.Err()
}

func (r *Repository) updateStatus(ctx context.Context, tripID uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE trips SET status = $1, updated_at = NOW() WHERE id = $2`, status, tripID)
	return err
}

func (r *Repository) TripPoints(ctx context.Context, tripID uuid.UUID) ([]models.LocationUpdate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT latitude, longitude, accuracy, heading, speed, recorded_at
		FROM trip_locations WHERE trip_id = $1 AND id % 5 = 0 ORDER BY id
	`, tripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []models.LocationUpdate
	for rows.Next() {
		var p models.LocationUpdate
		p.TripID = tripID
		if err := rows.Scan(&p.Latitude, &p.Longitude, &p.Accuracy, &p.Heading, &p.Speed, &p.Timestamp); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}
