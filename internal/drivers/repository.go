package drivers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/database"
	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type Location struct {
	Lat float64
	Lng float64
}

func isUniqueViolation(err error) bool { return database.IsUniqueViolation(err) }

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateDriverProfile(ctx context.Context, userID uuid.UUID, experienceYears int) (*models.Driver, error) {
	var driver models.Driver
	// current_location is NULL for a brand new profile, and ST_Y/ST_X of NULL is
	// NULL, so the coordinates have to land in nullable targets.
	lat, lng := pgtype.Float8{}, pgtype.Float8{}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO drivers (user_id, experience_years)
		VALUES ($1, $2)
		RETURNING id, user_id, status, verification_status, rating, total_trips,
		          total_ratings, experience_years, acceptance_rate, cancellation_rate,
		          total_offers, accepted_offers, total_cancellations,
		          can_drive_manual, can_drive_automatic, can_drive_hatchback,
		          can_drive_sedan, can_drive_suv, can_drive_luxury,
		          ST_Y(current_location::geometry) AS lat,
		          ST_X(current_location::geometry) AS lng,
		          earnings_total, created_at, updated_at
	`, userID, experienceYears).Scan(
		&driver.ID, &driver.UserID, &driver.Status, &driver.VerificationStatus,
		&driver.Rating, &driver.TotalTrips, &driver.TotalRatings, &driver.ExperienceYears,
		&driver.AcceptanceRate, &driver.CancellationRate,
		&driver.TotalOffers, &driver.AcceptedOffers, &driver.TotalCancellations,
		&driver.CanDriveManual, &driver.CanDriveAutomatic, &driver.CanDriveHatchback,
		&driver.CanDriveSedan, &driver.CanDriveSUV, &driver.CanDriveLuxury,
		&lat, &lng, &driver.EarningsTotal, &driver.CreatedAt, &driver.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apperr.Conflict("a driver profile already exists for this account", nil)
		}
		return nil, err
	}
	driver.CurrentLocation = locationFrom(lat, lng)
	return &driver, nil
}

func (r *Repository) GetByUserID(ctx context.Context, userID uuid.UUID) (*models.Driver, error) {
	return r.scanOne(ctx, `WHERE d.user_id = $1`, userID)
}

func (r *Repository) GetByID(ctx context.Context, driverID uuid.UUID) (*models.Driver, error) {
	return r.scanOne(ctx, `WHERE d.id = $1`, driverID)
}

func (r *Repository) scanOne(ctx context.Context, where string, arg any) (*models.Driver, error) {
	driver, err := scanDriver(ctx, r.pool, fmt.Sprintf(`
		SELECT d.id, d.user_id, d.status, d.verification_status, d.rating, d.total_trips,
		       d.total_ratings, d.experience_years, d.acceptance_rate, d.cancellation_rate,
		       d.total_offers, d.accepted_offers, d.total_cancellations,
		       d.can_drive_manual, d.can_drive_automatic, d.can_drive_hatchback,
		       d.can_drive_sedan, d.can_drive_suv, d.can_drive_luxury,
		       ST_Y(d.current_location::geometry) AS lat,
		       ST_X(d.current_location::geometry) AS lng,
		       d.earnings_total, d.created_at, d.updated_at,
		       u.name, u.phone
		FROM drivers d JOIN users u ON u.id = d.user_id
		%s
	`, where), arg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("driver not found")
		}
		return nil, err
	}
	return driver, nil
}

func scanDriver(ctx context.Context, pool *pgxpool.Pool, query string, arg any) (*models.Driver, error) {
	var driver models.Driver
	var name, phone string
	lat, lng := pgtype.Float8{}, pgtype.Float8{}

	err := pool.QueryRow(ctx, query, arg).Scan(
		&driver.ID, &driver.UserID, &driver.Status, &driver.VerificationStatus,
		&driver.Rating, &driver.TotalTrips, &driver.TotalRatings, &driver.ExperienceYears,
		&driver.AcceptanceRate, &driver.CancellationRate,
		&driver.TotalOffers, &driver.AcceptedOffers, &driver.TotalCancellations,
		&driver.CanDriveManual, &driver.CanDriveAutomatic, &driver.CanDriveHatchback,
		&driver.CanDriveSedan, &driver.CanDriveSUV, &driver.CanDriveLuxury,
		&lat, &lng, &driver.EarningsTotal, &driver.CreatedAt, &driver.UpdatedAt,
		&name, &phone,
	)
	if err != nil {
		return nil, err
	}

	driver.CurrentLocation = locationFrom(lat, lng)
	_ = name
	_ = phone
	return &driver, nil
}

// locationFrom converts the nullable float8 pair projected out of PostGIS into
// a domain Location, keeping nil when the driver has never reported a position.
func locationFrom(lat, lng pgtype.Float8) *models.Location {
	if !lat.Valid || !lng.Valid {
		return nil
	}
	return &models.Location{Latitude: lat.Float64, Longitude: lng.Float64}
}

func (r *Repository) UpdateStatus(ctx context.Context, driverID uuid.UUID, status models.DriverStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE drivers SET status = $1, updated_at = NOW() WHERE id = $2`, string(status), driverID)
	return err
}

func (r *Repository) UpdateCapabilities(ctx context.Context, driverID uuid.UUID, caps models.VehicleCapability) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE drivers SET
			can_drive_manual = $1,
			can_drive_automatic = $2,
			can_drive_hatchback = $3,
			can_drive_sedan = $4,
			can_drive_suv = $5,
			can_drive_luxury = $6,
			updated_at = NOW()
		WHERE id = $7
	`, caps.Manual, caps.Automatic, caps.Hatchback, caps.Sedan, caps.SUV, caps.Luxury, driverID)
	return err
}

func (r *Repository) UpdateLocation(ctx context.Context, driverID uuid.UUID, lat, lng float64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE drivers SET
			current_location = ST_SetSRID(ST_MakePoint($2, $1), 4326)::geography,
			updated_at = NOW()
		WHERE id = $3
	`, lat, lng, driverID)
	return err
}

func (r *Repository) SetVerificationStatus(ctx context.Context, driverID uuid.UUID, status models.VerificationStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE drivers SET verification_status = $1, updated_at = NOW() WHERE id = $2`,
		string(status), driverID)
	return err
}

func (r *Repository) AddDocument(ctx context.Context, doc *models.DriverDocument) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, type, document_number, status, expiry_date, document_url)
		VALUES ($1, $2, $3, 'PENDING', $4, $5)
		ON CONFLICT (driver_id, type)
		DO UPDATE SET document_number = EXCLUDED.document_number,
		              document_url = EXCLUDED.document_url,
		              expiry_date = EXCLUDED.expiry_date,
		              status = 'PENDING'
	`, doc.DriverID, doc.Type, doc.DocumentNumber, doc.ExpiryDate, doc.DocumentURL)
	return err
}

func (r *Repository) ListDocuments(ctx context.Context, driverID uuid.UUID) ([]models.DriverDocument, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, driver_id, type, document_number, status, expiry_date, document_url, verified_at, created_at
		FROM driver_documents WHERE driver_id = $1 ORDER BY created_at
	`, driverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []models.DriverDocument
	for rows.Next() {
		var d models.DriverDocument
		if err := rows.Scan(&d.ID, &d.DriverID, &d.Type, &d.DocumentNumber, &d.Status, &d.ExpiryDate, &d.DocumentURL, &d.VerifiedAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (r *Repository) VerifyDocument(ctx context.Context, docID uuid.UUID, approved bool) error {
	status := "REJECTED"
	if approved {
		status = "VERIFIED"
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE driver_documents SET status = $1, verified_at = CASE WHEN $1 = 'VERIFIED' THEN NOW() ELSE NULL END
		WHERE id = $2
	`, status, docID)
	return err
}

// EarningEntry is one row of the driver's earnings ledger.
type EarningEntry struct {
	ID        uuid.UUID `json:"id"`
	BookingID uuid.UUID `json:"booking_id"`
	Amount    int       `json:"amount"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordNoShow increments a driver's no-show count.
//
// Called from the sweep that detects a driver who was marked as arrived but
// never started the trip, and from the driver-initiated no-show path.
func (r *Repository) RecordNoShow(ctx context.Context, driverID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE drivers SET total_no_shows = total_no_shows + 1, updated_at = NOW()
		WHERE id = $1
	`, driverID)
	return err
}

// EarningsLedger lists a driver's per-booking earnings, newest first.
//
// The running total on the driver row is not enough on its own: a driver needs
// to see what each trip earned and whether it has been paid out.
func (r *Repository) EarningsLedger(ctx context.Context, driverID uuid.UUID, limit, offset int) ([]EarningEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, booking_id, amount, status, created_at
		FROM driver_earnings
		WHERE driver_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, driverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []EarningEntry{}
	for rows.Next() {
		var e EarningEntry
		if err := rows.Scan(&e.ID, &e.BookingID, &e.Amount, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// RecordEarning credits a driver for a completed trip.
//
// Idempotent on the booking: the unique index on driver_earnings(booking_id)
// means a replayed capture conflicts here instead of paying twice, and the
// running total is only advanced when the ledger row was actually inserted.
// The two writes share a transaction so the total can never be advanced without
// the ledger row that justifies it.
// CreditEarning records what a driver earned for a ride and counts the ride.
//
// All three effects -- the ledger row, the running earnings total, and the
// completed trip count -- commit in a single transaction behind the unique
// booking_id. That unique key is the exactly-once gate: the insert either wins
// the race and the counters move, or it loses because an earlier attempt already
// credited this ride, in which case the rollback leaves the counters untouched.
//
// Folding the counters into the same transaction is what makes the credit safe to
// retry. The capture path runs whenever a gateway event is applied, including
// retries of an attempt that failed after the payment was marked captured, so
// separately issued counter updates would double-count on every retry.
func (r *Repository) CreditEarning(ctx context.Context, driverID, bookingID uuid.UUID, amount int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO driver_earnings (driver_id, booking_id, amount) VALUES ($1, $2, $3)
		ON CONFLICT (booking_id) DO NOTHING
	`, driverID, bookingID, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Already credited by an earlier attempt.
		return nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE drivers
		SET earnings_total = earnings_total + $1,
		    total_trips    = total_trips + 1
		WHERE id = $2
	`, amount, driverID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *Repository) NearbyOnline(ctx context.Context, lat, lng float64, radiusMeters int, caps models.VehicleCapability) ([]models.Driver, error) {
	const q = `
		SELECT d.id, d.user_id, d.status, d.verification_status, d.rating, d.total_trips,
		       d.total_ratings, d.experience_years, d.acceptance_rate, d.cancellation_rate,
		       d.total_offers, d.accepted_offers, d.total_cancellations,
		       d.can_drive_manual, d.can_drive_automatic, d.can_drive_hatchback,
		       d.can_drive_sedan, d.can_drive_suv, d.can_drive_luxury,
		       ST_Y(d.current_location::geometry) AS lat,
		       ST_X(d.current_location::geometry) AS lng,
		       d.earnings_total, d.created_at, d.updated_at,
		       ST_Distance(d.current_location, ST_SetSRID(ST_MakePoint($2, $1), 4326)::geography) AS dist_m
		FROM drivers d
		WHERE d.status = 'ONLINE'
		  AND d.verification_status = 'APPROVED'
		  AND ST_DWithin(d.current_location, ST_SetSRID(ST_MakePoint($2, $1), 4326)::geography, $3)
		  AND ($4 = FALSE OR d.can_drive_manual = TRUE)
		  AND ($5 = FALSE OR d.can_drive_automatic = TRUE)
		  AND ($6 = FALSE OR d.can_drive_hatchback = TRUE)
		  AND ($7 = FALSE OR d.can_drive_sedan = TRUE)
		  AND ($8 = FALSE OR d.can_drive_suv = TRUE)
		  AND ($9 = FALSE OR d.can_drive_luxury = TRUE)
		ORDER BY dist_m ASC
		LIMIT 50
	`
	rows, err := r.pool.Query(ctx, q,
		lat, lng, radiusMeters,
		caps.Manual, caps.Automatic, caps.Hatchback, caps.Sedan, caps.SUV, caps.Luxury,
	)
	if err != nil {
		return nil, fmt.Errorf("nearby online drivers: %w", err)
	}
	defer rows.Close()

	var result []models.Driver
	for rows.Next() {
		var d models.Driver
		lat, lng := pgtype.Float8{}, pgtype.Float8{}
		var distM float64
		if err := rows.Scan(
			&d.ID, &d.UserID, &d.Status, &d.VerificationStatus, &d.Rating, &d.TotalTrips,
			&d.TotalRatings, &d.ExperienceYears, &d.AcceptanceRate, &d.CancellationRate,
			&d.TotalOffers, &d.AcceptedOffers, &d.TotalCancellations,
			&d.CanDriveManual, &d.CanDriveAutomatic, &d.CanDriveHatchback,
			&d.CanDriveSedan, &d.CanDriveSUV, &d.CanDriveLuxury,
			&lat, &lng, &d.EarningsTotal, &d.CreatedAt, &d.UpdatedAt, &distM,
		); err != nil {
			return nil, err
		}
		d.CurrentLocation = locationFrom(lat, lng)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (r *Repository) ListPending(ctx context.Context, limit, offset int) ([]models.Driver, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, status, verification_status, rating, total_trips,
		       total_ratings, experience_years, acceptance_rate, cancellation_rate,
		       total_offers, accepted_offers, total_cancellations,
		       can_drive_manual, can_drive_automatic, can_drive_hatchback,
		       can_drive_sedan, can_drive_suv, can_drive_luxury,
		       ST_Y(current_location::geometry) AS lat,
		       ST_X(current_location::geometry) AS lng,
		       earnings_total, created_at, updated_at
		FROM drivers
		WHERE verification_status = 'PENDING' OR verification_status = 'SUBMITTED'
		ORDER BY created_at ASC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Driver
	for rows.Next() {
		var d models.Driver
		lat, lng := pgtype.Float8{}, pgtype.Float8{}
		if err := rows.Scan(
			&d.ID, &d.UserID, &d.Status, &d.VerificationStatus, &d.Rating, &d.TotalTrips,
			&d.TotalRatings, &d.ExperienceYears, &d.AcceptanceRate, &d.CancellationRate,
			&d.TotalOffers, &d.AcceptedOffers, &d.TotalCancellations,
			&d.CanDriveManual, &d.CanDriveAutomatic, &d.CanDriveHatchback,
			&d.CanDriveSedan, &d.CanDriveSUV, &d.CanDriveLuxury,
			&lat, &lng, &d.EarningsTotal, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, err
		}
		d.CurrentLocation = locationFrom(lat, lng)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (r *Repository) RecordOffer(ctx context.Context, driverID uuid.UUID, accepted bool) error {
	if accepted {
		_, err := r.pool.Exec(ctx, `
			UPDATE drivers SET
				total_offers = total_offers + 1,
				accepted_offers = accepted_offers + 1,
				acceptance_rate = ROUND((accepted_offers + 1)::numeric / NULLIF(total_offers + 1, 0) * 100, 2),
				updated_at = NOW()
			WHERE id = $1
		`, driverID)
		return err
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE drivers SET
			total_offers = total_offers + 1,
			acceptance_rate = ROUND(accepted_offers::numeric / NULLIF(total_offers + 1, 0) * 100, 2),
			updated_at = NOW()
		WHERE id = $1
	`, driverID)
	return err
}

func (r *Repository) RecordDriverCancellation(ctx context.Context, driverID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE drivers SET
			total_cancellations = total_cancellations + 1,
			cancellation_rate = ROUND((total_cancellations + 1)::numeric / NULLIF(total_trips + total_cancellations + 1, 0) * 100, 2),
			updated_at = NOW()
		WHERE id = $1
	`, driverID)
	return err
}
