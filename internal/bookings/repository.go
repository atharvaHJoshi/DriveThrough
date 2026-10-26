package bookings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

const bookingColumns = `id, customer_id, driver_id, vehicle_id, booking_type, duration_hours,
	pickup_location, pickup_lat, pickup_lng, destination, dest_lat, dest_lng,
	scheduled_at, status, otp_code, otp_attempts, estimated_price, final_price,
	driver_fee, platform_fee, protection_fee, tax, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, b *models.Booking) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO bookings (customer_id, vehicle_id, booking_type, duration_hours,
			pickup_location, pickup_lat, pickup_lng, destination, dest_lat, dest_lng,
			scheduled_at, otp_code, estimated_price, driver_fee, platform_fee, protection_fee, tax)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING `+bookingColumns,
		b.CustomerID, b.VehicleID, b.BookingType, b.DurationHours,
		b.PickupLocation, b.PickupLat, b.PickupLng, b.Destination, b.DestLat, b.DestLng,
		b.ScheduledAt, b.OTPCode, b.EstimatedPrice, b.DriverFee, b.PlatformFee, b.ProtectionFee, b.Tax,
	).Scan(
		&b.ID, &b.CustomerID, &b.DriverID, &b.VehicleID, &b.BookingType, &b.DurationHours,
		&b.PickupLocation, &b.PickupLat, &b.PickupLng, &b.Destination, &b.DestLat, &b.DestLng,
		&b.ScheduledAt, &b.Status, &b.OTPCode, &b.OTPAttempts, &b.EstimatedPrice, &b.FinalPrice,
		&b.DriverFee, &b.PlatformFee, &b.ProtectionFee, &b.Tax, &b.CreatedAt, &b.UpdatedAt,
	)
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*models.Booking, error) {
	var b models.Booking
	err := r.pool.QueryRow(ctx, `SELECT `+bookingColumns+` FROM bookings WHERE id = $1`, id).Scan(
		&b.ID, &b.CustomerID, &b.DriverID, &b.VehicleID, &b.BookingType, &b.DurationHours,
		&b.PickupLocation, &b.PickupLat, &b.PickupLng, &b.Destination, &b.DestLat, &b.DestLng,
		&b.ScheduledAt, &b.Status, &b.OTPCode, &b.OTPAttempts, &b.EstimatedPrice, &b.FinalPrice,
		&b.DriverFee, &b.PlatformFee, &b.ProtectionFee, &b.Tax, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("booking not found")
		}
		return nil, err
	}
	return &b, nil
}

func (r *Repository) GetForCustomer(ctx context.Context, bookingID, customerID uuid.UUID) (*models.Booking, error) {
	b, err := r.GetByID(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	if b.CustomerID != customerID {
		return nil, apperr.Forbidden("not your booking")
	}
	return b, nil
}

// UpdateStatus moves a booking to a new status and records the audit event in
// one transaction.
//
// expected is the set of statuses the caller observed as current. The UPDATE is
// guarded on it, so a transition that raced with another one loses instead of
// silently overwriting it. Pass nil to mean "from any state" for system-driven
// transitions (payment failure, expiry sweeps) that are intentionally not
// state-checked.
func (r *Repository) UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string, expected ...models.BookingStatus) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		tag  pgconn.CommandTag
		sql  string
		args []any
	)

	if len(expected) == 0 {
		sql = `UPDATE bookings SET status = $1, updated_at = NOW() WHERE id = $2`
		args = []any{string(status), bookingID}
	} else {
		texts := make([]string, len(expected))
		for i, s := range expected {
			texts[i] = string(s)
		}
		// $3::text[] keeps the empty-array case well typed.
		sql = `UPDATE bookings SET status = $1, updated_at = NOW()
		       WHERE id = $2 AND status = ANY($3::text[])`
		args = []any{string(status), bookingID, texts}
	}

	tag, err = tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		// Either the booking does not exist, or it is no longer in a state this
		// transition is allowed from. Report a conflict so the caller retries
		// against fresh state instead of overwriting someone else's transition.
		return apperr.Conflict("booking status changed concurrently, retry", nil)
	}

	// A nil actor marks a system-driven transition (payment failure, search
	// start, expiry sweep). Writing uuid.Nil instead would violate the
	// users(id) foreign key, so it is normalised to SQL NULL here.
	var actor any
	if actorID != uuid.Nil {
		actor = actorID
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO booking_events (booking_id, status, notes, actor_id)
		VALUES ($1, $2, $3, $4)
	`, bookingID, string(status), notes, actor)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// AssignDriver claims a booking for a driver inside one transaction.
//
// The conditional UPDATE is the concurrency control: two drivers accepting the
// same offer concurrently both pass a prior SELECT, and only one can match
// `driver_id IS NULL`. The status predicate additionally stops a driver being
// attached to a booking that was cancelled or already picked up while the offer
// was in flight, which the driver_id check alone would not catch.
func (r *Repository) AssignDriver(ctx context.Context, bookingID, driverID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE bookings SET driver_id = $1, status = 'DRIVER_ASSIGNED', updated_at = NOW()
		WHERE id = $2
		  AND driver_id IS NULL
		  AND status IN ('REQUESTED', 'SEARCHING')
	`, driverID, bookingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "someone beat you to it" from "this booking is gone", since
		// the two need different client behaviour (retry vs. give up).
		var current string
		if err := tx.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.NotFound("booking not found")
			}
			return err
		}
		if current == "CANCELLED" || current == "CANCELLED_BY_DRIVER" || current == "COMPLETED" {
			return apperr.Conflict("booking is no longer open for assignment", nil)
		}
		return apperr.Conflict("driver already assigned", nil)
	}

	// The assignment is the single most important entry in the customer's status
	// timeline, so it has to be recorded alongside the status change or the
	// history silently skips the moment a driver was found.
	//
	// booking_events.actor_id references users(id), while the caller only has the
	// driver profile id from drivers(id). Writing the profile id directly violated
	// the foreign key, which rolled back the whole transaction — so every driver
	// assignment in the system failed and the booking stayed SEARCHING forever.
	// The user id is resolved from the drivers row inside this same transaction.
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_events (booking_id, status, notes, actor_id)
		SELECT $1, 'DRIVER_ASSIGNED', 'driver assigned', d.user_id
		FROM drivers d WHERE d.id = $2
	`, bookingID, driverID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *Repository) SetOTP(ctx context.Context, bookingID uuid.UUID, otp string) error {
	// Issuing a code clears the failure counter, so a customer who mistyped five
	// times is not locked out of the newly issued one.
	_, err := r.pool.Exec(ctx,
		`UPDATE bookings SET otp_code = $1, otp_attempts = 0, updated_at = NOW() WHERE id = $2`, otp, bookingID)
	return err
}

// IncrementOTPAttempts records a handover-code guess. It is incremented on both
// success and failure so the stored value is always "guesses used".
func (r *Repository) IncrementOTPAttempts(ctx context.Context, bookingID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE bookings SET otp_attempts = otp_attempts + 1, updated_at = NOW() WHERE id = $1`, bookingID)
	return err
}

func (r *Repository) ListForCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings WHERE customer_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, customerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

func (r *Repository) ListForDriver(ctx context.Context, driverID uuid.UUID, limit, offset int) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings WHERE driver_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, driverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

func (r *Repository) VisibleStatusesForDriver() []models.BookingStatus {
	return []models.BookingStatus{
		models.BookingAssigned,
		models.BookingEnRoute,
		models.BookingArrived,
		models.BookingOTPVerified,
		models.BookingStarted,
		models.BookingPaused,
		models.BookingResumed,
	}
}

func (r *Repository) FindAssignedBooking(ctx context.Context, driverID uuid.UUID) (*models.Booking, error) {
	var b models.Booking
	statuses := r.VisibleStatusesForDriver()
	arr := make([]string, len(statuses))
	for i, s := range statuses {
		arr[i] = string(s)
	}

	err := r.pool.QueryRow(ctx, `
		SELECT `+bookingColumns+` FROM bookings WHERE driver_id = $1 AND status = ANY($2)
		ORDER BY created_at DESC LIMIT 1
	`, driverID, arr).Scan(
		&b.ID, &b.CustomerID, &b.DriverID, &b.VehicleID, &b.BookingType, &b.DurationHours,
		&b.PickupLocation, &b.PickupLat, &b.PickupLng, &b.Destination, &b.DestLat, &b.DestLng,
		&b.ScheduledAt, &b.Status, &b.OTPCode, &b.OTPAttempts, &b.EstimatedPrice, &b.FinalPrice,
		&b.DriverFee, &b.PlatformFee, &b.ProtectionFee, &b.Tax, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("no active booking")
		}
		return nil, err
	}
	return &b, nil
}

func (r *Repository) UpdateFinalPrice(ctx context.Context, bookingID uuid.UUID, finalPrice int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE bookings SET final_price = $1, updated_at = NOW() WHERE id = $2
	`, finalPrice, bookingID)
	return err
}

func (r *Repository) CountActiveByCustomer(ctx context.Context, customerID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bookings
		WHERE customer_id = $1 AND status IN ('REQUESTED', 'SEARCHING', 'DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE', 'DRIVER_ARRIVED')
	`, customerID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active bookings: %w", err)
	}
	return count, nil
}

func scanBookings(rows pgx.Rows) ([]models.Booking, error) {
	var result []models.Booking
	for rows.Next() {
		var b models.Booking
		if err := rows.Scan(
			&b.ID, &b.CustomerID, &b.DriverID, &b.VehicleID, &b.BookingType, &b.DurationHours,
			&b.PickupLocation, &b.PickupLat, &b.PickupLng, &b.Destination, &b.DestLat, &b.DestLng,
			&b.ScheduledAt, &b.Status, &b.OTPCode, &b.OTPAttempts, &b.EstimatedPrice, &b.FinalPrice,
			&b.DriverFee, &b.PlatformFee, &b.ProtectionFee, &b.Tax, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (r *Repository) ListSearchingStale(ctx context.Context, cutoff time.Time) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings
		WHERE status = 'SEARCHING' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

// ListSearchingWithoutLiveOffer returns bookings that are still waiting for a
// driver and have no offer a driver can still act on. Matching runs inline at
// creation, so a booking that found no candidates then would otherwise sit in
// SEARCHING until the stale-retry threshold. Re-polling these on the short
// scheduled cadence means a driver who comes online nearby picks up the waiting
// customer within a poll interval instead of ten minutes.
//
// minAge keeps the very first tick from re-running the inline attempt while it
// is still in flight.
func (r *Repository) ListSearchingWithoutLiveOffer(ctx context.Context, minAge time.Duration) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings b
		WHERE b.status = 'SEARCHING'
		  AND b.created_at < NOW() - make_interval(secs => $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM matching_offers o
		      WHERE o.booking_id = b.id
		        AND o.status = 'PENDING'
		        AND o.expires_at > NOW()
		  )
		ORDER BY b.created_at ASC
		LIMIT 50
	`, minAge.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

// ListActiveForDriver returns a driver's bookings that have not reached a
// terminal state.
//
// Used when an administrator suspends a driver: any booking still assigned to
// them has to be closed out, or a suspended driver keeps an active booking they
// can still complete and be paid for.
func (r *Repository) ListActiveForDriver(ctx context.Context, driverID uuid.UUID, limit int) ([]models.Booking, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings
		WHERE driver_id = $1
		  AND status NOT IN (
		      'TRIP_COMPLETED', 'CANCELLED', 'CANCELLED_BY_DRIVER',
		      'DRIVER_NO_SHOW', 'PAYMENT_FAILED', 'ACCIDENT'
		  )
		ORDER BY created_at ASC
		LIMIT $2
	`, driverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

// ListStaleArrived returns bookings where the driver was marked as having
// arrived but the trip never started.
//
// Without this sweep a customer who reached the pickup point is stuck on
// DRIVER_ARRIVED forever: the trip is only ever started by the driver's own
// device, so a driver who simply stops responding leaves the booking with no
// path forward and no refund.
func (r *Repository) ListStaleArrived(ctx context.Context, olderThan time.Duration, limit int) ([]models.Booking, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings b
		WHERE b.status = 'DRIVER_ARRIVED'
		  AND b.updated_at < NOW() - make_interval(secs => $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM trips t
		      WHERE t.booking_id = b.id
		        AND t.status IN ('ACTIVE', 'PAUSED', 'COMPLETED')
		  )
		ORDER BY b.updated_at ASC
		LIMIT $2
	`, olderThan.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}

func (r *Repository) ListScheduledReady(ctx context.Context) ([]models.Booking, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+bookingColumns+` FROM bookings
		WHERE status = 'REQUESTED' AND scheduled_at IS NOT NULL AND scheduled_at <= NOW()
		ORDER BY scheduled_at ASC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBookings(rows)
}
