package incidents

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

func (r *Repository) CreateIncident(ctx context.Context, in *models.Incident) error {
	// A nil Go slice is encoded as SQL NULL, which violates the NOT NULL on
	// incidents.photos; an explicit empty array matches the column default.
	photos := in.Photos
	if photos == nil {
		photos = []string{}
	}

	return r.pool.QueryRow(ctx, `
		INSERT INTO incidents (booking_id, trip_id, type, severity, reported_by,
			latitude, longitude, description, photos, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'OPEN')
		RETURNING id, booking_id, trip_id, type, severity, reported_by,
		          latitude, longitude, description, photos, status, resolution, created_at, updated_at
	`, in.BookingID, in.TripID, in.Type, in.Severity, in.ReportedBy,
		in.Latitude, in.Longitude, in.Description, photos).Scan(
		&in.ID, &in.BookingID, &in.TripID, &in.Type, &in.Severity, &in.ReportedBy,
		&in.Latitude, &in.Longitude, &in.Description, &in.Photos, &in.Status, &in.Resolution,
		&in.CreatedAt, &in.UpdatedAt,
	)
}

func (r *Repository) GetIncident(ctx context.Context, id uuid.UUID) (*models.Incident, error) {
	var in models.Incident
	err := r.pool.QueryRow(ctx, `
		SELECT id, booking_id, trip_id, type, severity, reported_by,
		       latitude, longitude, description, photos, status, resolution, created_at, updated_at
		FROM incidents WHERE id = $1
	`, id).Scan(
		&in.ID, &in.BookingID, &in.TripID, &in.Type, &in.Severity, &in.ReportedBy,
		&in.Latitude, &in.Longitude, &in.Description, &in.Photos, &in.Status, &in.Resolution,
		&in.CreatedAt, &in.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("incident not found")
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repository) ListIncidents(ctx context.Context, status string, limit, offset int) ([]models.Incident, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, booking_id, trip_id, type, severity, reported_by,
		       latitude, longitude, description, photos, status, resolution, created_at, updated_at
		FROM incidents
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Incident
	for rows.Next() {
		var in models.Incident
		if err := rows.Scan(
			&in.ID, &in.BookingID, &in.TripID, &in.Type, &in.Severity, &in.ReportedBy,
			&in.Latitude, &in.Longitude, &in.Description, &in.Photos, &in.Status, &in.Resolution,
			&in.CreatedAt, &in.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, in)
	}
	return result, rows.Err()
}

func (r *Repository) UpdateIncidentStatus(ctx context.Context, id uuid.UUID, status, resolution string, adminID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE incidents SET status = $1, resolution = $2, updated_at = NOW()
		WHERE id = $3
	`, status, resolution, id)
	return err
}

func (r *Repository) CreateSOS(ctx context.Context, sos *models.SOS) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO sos (user_id, trip_id, latitude, longitude, kind)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, trip_id, latitude, longitude, kind, status,
		          responded_by, response_note, created_at, resolved_at
	`, sos.UserID, sos.TripID, sos.Latitude, sos.Longitude, sos.Kind).Scan(
		&sos.ID, &sos.UserID, &sos.TripID, &sos.Latitude, &sos.Longitude, &sos.Kind, &sos.Status,
		&sos.RespondedBy, &sos.ResponseNote, &sos.CreatedAt, &sos.ResolvedAt,
	)
}

func (r *Repository) ActiveSOS(ctx context.Context, userID uuid.UUID) (*models.SOS, error) {
	var sos models.SOS
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, trip_id, latitude, longitude, kind, status,
		       responded_by, response_note, created_at, resolved_at
		FROM sos WHERE user_id = $1 AND status IN ('OPEN', 'RESPONDED')
		ORDER BY created_at DESC LIMIT 1
	`, userID).Scan(
		&sos.ID, &sos.UserID, &sos.TripID, &sos.Latitude, &sos.Longitude, &sos.Kind, &sos.Status,
		&sos.RespondedBy, &sos.ResponseNote, &sos.CreatedAt, &sos.ResolvedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("no active sos")
		}
		return nil, err
	}
	return &sos, nil
}

// ActiveSOSByTrip returns an unresolved SOS raised against a trip, if any.
func (r *Repository) ActiveSOSByTrip(ctx context.Context, tripID uuid.UUID) (*models.SOS, error) {
	var sos models.SOS
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, trip_id, latitude, longitude, kind, status,
		       responded_by, response_note, created_at, resolved_at
		FROM sos WHERE trip_id = $1 AND status IN ('OPEN', 'RESPONDED')
		ORDER BY created_at DESC LIMIT 1
	`, tripID).Scan(
		&sos.ID, &sos.UserID, &sos.TripID, &sos.Latitude, &sos.Longitude, &sos.Kind, &sos.Status,
		&sos.RespondedBy, &sos.ResponseNote, &sos.CreatedAt, &sos.ResolvedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("no active sos for trip")
		}
		return nil, err
	}
	return &sos, nil
}

func (r *Repository) ResolveSOS(ctx context.Context, sosID uuid.UUID, adminID uuid.UUID, note string) error {
	// Conditional on the row still being unresolved so a double resolution is a
	// no-op rather than an error, and the original responder is preserved.
	_, err := r.pool.Exec(ctx, `
		UPDATE sos
		SET status = 'RESOLVED',
		    responded_by = COALESCE(responded_by, $2),
		    response_note = COALESCE(response_note, $3),
		    resolved_at = NOW()
		WHERE id = $1 AND status <> 'RESOLVED'
	`, sosID, adminID, note)
	return err
}

func (r *Repository) ListEmergencyContacts(ctx context.Context, userID uuid.UUID) ([]models.EmergencyContact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, name, phone, relation, created_at
		FROM emergency_contacts WHERE user_id = $1 ORDER BY created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.EmergencyContact
	for rows.Next() {
		var c models.EmergencyContact
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.Phone, &c.Relation, &c.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (r *Repository) AddEmergencyContact(ctx context.Context, c *models.EmergencyContact) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO emergency_contacts (user_id, name, phone, relation)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, name, phone, relation, created_at
	`, c.UserID, c.Name, c.Phone, c.Relation).Scan(
		&c.ID, &c.UserID, &c.Name, &c.Phone, &c.Relation, &c.CreatedAt,
	)
}
