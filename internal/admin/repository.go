package admin

import (
	"context"

	"github.com/google/uuid"
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

type DashboardStats struct {
	ActiveTrips      int `json:"active_trips"`
	AvailableDrivers int `json:"available_drivers"`
	OnlineDrivers    int `json:"online_drivers"`
	BookingsToday    int `json:"bookings_today"`
	Cancellations    int `json:"cancellations"`
	OpenIncidents    int `json:"open_incidents"`
	OpenSOS          int `json:"open_sos"`
	PendingDrivers   int `json:"pending_drivers"`
}

func (r *Repository) Dashboard(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}

	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM bookings WHERE status IN ('TRIP_STARTED','TRIP_PAUSED','TRIP_RESUMED')) AS active_trips,
			(SELECT COUNT(*) FROM drivers WHERE status = 'ONLINE' AND verification_status = 'APPROVED') AS online_drivers,
			(SELECT COUNT(*) FROM bookings WHERE created_at::date = CURRENT_DATE) AS bookings_today,
			(SELECT COUNT(*) FROM bookings WHERE status IN ('CANCELLED','CANCELLED_BY_DRIVER') AND created_at::date = CURRENT_DATE) AS cancellations,
			(SELECT COUNT(*) FROM incidents WHERE status IN ('OPEN','INVESTIGATING')) AS open_incidents,
			(SELECT COUNT(*) FROM sos WHERE status IN ('OPEN','RESPONDED')) AS open_sos,
			(SELECT COUNT(*) FROM drivers WHERE verification_status IN ('PENDING','SUBMITTED')) AS pending_drivers
	`).Scan(
		&stats.ActiveTrips,
		&stats.OnlineDrivers,
		&stats.BookingsToday,
		&stats.Cancellations,
		&stats.OpenIncidents,
		&stats.OpenSOS,
		&stats.PendingDrivers,
	)
	if err != nil {
		return nil, err
	}
	stats.AvailableDrivers = stats.OnlineDrivers
	return stats, nil
}

func (r *Repository) DriverDocs(ctx context.Context, driverID uuid.UUID) ([]models.DriverDocument, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, driver_id, type, document_number, status, expiry_date, document_url, verified_at, created_at
		FROM driver_documents WHERE driver_id = $1 ORDER BY type
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

func (r *Repository) Bookings(ctx context.Context, status string, limit, offset int) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, customer_id, driver_id, vehicle_id, booking_type, duration_hours,
			pickup_location, pickup_lat, pickup_lng, destination, dest_lat, dest_lng,
			scheduled_at, status, otp_code, estimated_price, final_price,
			driver_fee, platform_fee, protection_fee, tax, created_at, updated_at
		FROM bookings
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Booking
	for rows.Next() {
		var b models.Booking
		if err := rows.Scan(
			&b.ID, &b.CustomerID, &b.DriverID, &b.VehicleID, &b.BookingType, &b.DurationHours,
			&b.PickupLocation, &b.PickupLat, &b.PickupLng, &b.Destination, &b.DestLat, &b.DestLng,
			&b.ScheduledAt, &b.Status, &b.OTPCode, &b.EstimatedPrice, &b.FinalPrice,
			&b.DriverFee, &b.PlatformFee, &b.ProtectionFee, &b.Tax, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (r *Repository) DriverEarnings(ctx context.Context, driverID uuid.UUID, limit, offset int) ([]models.DriverEarning, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, driver_id, booking_id, amount, status, created_at
		FROM driver_earnings WHERE driver_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, driverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.DriverEarning
	for rows.Next() {
		var e models.DriverEarning
		if err := rows.Scan(&e.ID, &e.DriverID, &e.BookingID, &e.Amount, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (r *Repository) TripDetail(ctx context.Context, tripID uuid.UUID) (*models.Trip, error) {
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
		return nil, apperr.NotFound("trip not found")
	}
	return &t, nil
}
