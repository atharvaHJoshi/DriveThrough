package ratings

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

func (r *Repository) Create(ctx context.Context, rating *models.Rating) error {
	// A nil Go slice is encoded as SQL NULL, which violates the NOT NULL on
	// ratings.tags; an explicit empty array matches the column default.
	tags := rating.Tags
	if tags == nil {
		tags = []string{}
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO ratings (booking_id, from_user, to_user, rating, comment, tags)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (booking_id, from_user) DO UPDATE SET
			rating = EXCLUDED.rating,
			comment = EXCLUDED.comment,
			tags = EXCLUDED.tags
	`, rating.BookingID, rating.FromUser, rating.ToUser, rating.Rating, rating.Comment, tags)
	return err
}

func (r *Repository) ByBooking(ctx context.Context, bookingID uuid.UUID) ([]models.Rating, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, booking_id, from_user, to_user, rating, comment, tags, created_at
		FROM ratings WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Rating
	for rows.Next() {
		var rt models.Rating
		if err := rows.Scan(&rt.ID, &rt.BookingID, &rt.FromUser, &rt.ToUser, &rt.Rating, &rt.Comment, &rt.Tags, &rt.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, rt)
	}
	return result, rows.Err()
}

func (r *Repository) Exists(ctx context.Context, bookingID, fromUser uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM ratings WHERE booking_id = $1 AND from_user = $2)
	`, bookingID, fromUser).Scan(&exists)
	return exists, err
}

func (r *Repository) UpdateDriverRating(ctx context.Context, driverUserID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		WITH agg AS (
			SELECT rr.to_user, AVG(rr.rating)::numeric as avg_rating, COUNT(*) as cnt
			FROM ratings rr
			WHERE rr.to_user = $1
			GROUP BY rr.to_user
		)
		UPDATE drivers d SET
			rating = agg.avg_rating,
			total_ratings = agg.cnt::int,
			updated_at = NOW()
		FROM agg WHERE d.user_id = agg.to_user
	`, driverUserID)
	return err
}

// DriverResolver maps a driver profile id to the user account behind it.
// ratings.to_user references users(id) while bookings.driver_id references
// drivers(id), so the driver side of a rating has to be translated.
type DriverResolver interface {
	GetDriverByID(ctx context.Context, driverID uuid.UUID) (*models.Driver, error)
}

type Service struct {
	repo         *Repository
	bookingQuery func(ctx context.Context, bookingID uuid.UUID) (*models.Booking, error)
	drivers      DriverResolver
}

func NewService(repo *Repository, getBooking func(ctx context.Context, bookingID uuid.UUID) (*models.Booking, error), drivers DriverResolver) *Service {
	return &Service{repo: repo, bookingQuery: getBooking, drivers: drivers}
}

func (s *Service) RateBooking(ctx context.Context, bookingID, fromUser uuid.UUID, rating int, comment string, tags []string) error {
	if rating < 1 || rating > 5 {
		return apperr.BadRequest("rating must be between 1 and 5", nil)
	}

	booking, err := s.bookingQuery(ctx, bookingID)
	if err != nil {
		return err
	}

	// A rating is only meaningful once the ride actually happened. Without this
	// gate a booking could be rated while still in progress, or after it was
	// cancelled, and still count towards a driver's aggregate.
	if booking.Status != models.BookingCompleted {
		return apperr.Conflict("booking must be completed before it can be rated", nil)
	}

	var toUser *uuid.UUID
	// Whether the driver is the recipient, tracked explicitly. The old
	// isDriver(fromUser, booking) compared a users.id against a drivers.id and
	// was therefore always false, so driver aggregates were never refreshed.
	var ratedDriver bool

	switch {
	case fromUser == booking.CustomerID && booking.DriverID != nil:
		driver, err := s.drivers.GetDriverByID(ctx, *booking.DriverID)
		if err != nil {
			return err
		}
		toUser = &driver.UserID
		ratedDriver = true
	case booking.DriverID != nil:
		// The other party has to be the driver of this booking. Resolve the
		// caller's profile first because booking.DriverID is a drivers(id).
		driver, err := s.drivers.GetDriverByID(ctx, *booking.DriverID)
		if err != nil {
			return err
		}
		if driver.UserID != fromUser {
			return apperr.Forbidden("you are not part of this booking")
		}
		toUser = &booking.CustomerID
	default:
		return apperr.Forbidden("you are not part of this booking")
	}

	if toUser == nil {
		return apperr.Conflict("booking has no driver to rate yet", nil)
	}

	rate := &models.Rating{
		BookingID: bookingID,
		FromUser:  fromUser,
		ToUser:    *toUser,
		Rating:    rating,
		Comment:   strPtr(comment),
		Tags:      tags,
	}

	if err := s.repo.Create(ctx, rate); err != nil {
		return apperr.Wrap(500, "RATING_FAILED", "failed to save rating", err, nil)
	}

	// Only a rating of the driver feeds the driver's public aggregate. A rating
	// of the customer has no driver row to update.
	if !ratedDriver {
		return nil
	}

	if err := s.repo.UpdateDriverRating(ctx, *toUser); err != nil {
		return apperr.Wrap(500, "RATING_AGG_FAILED", "failed to update driver rating", err, nil)
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
