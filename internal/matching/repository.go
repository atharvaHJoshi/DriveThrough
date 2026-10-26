package matching

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Offer struct {
	ID        uuid.UUID    `json:"id"`
	BookingID uuid.UUID    `json:"booking_id"`
	DriverID  uuid.UUID    `json:"driver_id"`
	Status    string       `json:"status"`
	DistanceM float64      `json:"distance_m"`
	Driver    any          `json:"driver,omitempty"`
	Booking   OfferBooking `json:"booking"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// OfferBooking is the slice of a booking a driver needs to judge an offer.
//
// A driver cannot read the booking through the customer endpoint, because
// authorisation there requires already being an assigned party, which an
// unaccepted offer is not. This carries just the pickup, duration, and their own
// fee for that offer, and deliberately omits the customer id, the OTP state, and
// destination coordinates.
type OfferBooking struct {
	PickupLocation string  `json:"pickup_location"`
	PickupLat      float64 `json:"pickup_lat"`
	PickupLng      float64 `json:"pickup_lng"`
	Destination    *string `json:"destination,omitempty"`
	DurationHours  int     `json:"duration_hours"`
	DriverFee      int     `json:"driver_fee"`
	BookingType    string  `json:"booking_type"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateOffer upserts against the partial unique index on pending offers. The
// re-drive worker retries a booking whose offers all expired, and without this
// every retry stacked a second PENDING row for the same (booking, driver) pair.
// A previously declined or expired offer is not covered by the index, so it is
// replaced by a fresh row as intended.
func (r *Repository) CreateOffer(ctx context.Context, bookingID, driverID uuid.UUID, expiresAt time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO matching_offers (booking_id, driver_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (booking_id, driver_id) WHERE status = 'PENDING'
		DO UPDATE SET expires_at = EXCLUDED.expires_at, created_at = NOW()
		RETURNING id
	`, bookingID, driverID, expiresAt).Scan(&id)
	return id, err
}

// GetOfferByBookingAndDriver returns the driver's live offer for a booking.
//
// The expiry filter is load-bearing: an offer that timed out was never removed,
// so without it a driver could accept a booking hours after the customer gave
// up and it was cancelled.
func (r *Repository) GetOfferByBookingAndDriver(ctx context.Context, bookingID, driverID uuid.UUID) (*Offer, error) {
	var o Offer
	err := r.pool.QueryRow(ctx, `
		SELECT id, booking_id, driver_id, status FROM matching_offers
		WHERE booking_id = $1 AND driver_id = $2
		  AND status = 'PENDING'
		  AND expires_at > NOW()
		ORDER BY created_at DESC LIMIT 1
	`, bookingID, driverID).Scan(&o.ID, &o.BookingID, &o.DriverID, &o.Status)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ClaimOffer performs the accept as a single conditional write and reports
// whether this caller won the race. Two drivers tapping accept at the same
// moment both read a PENDING offer; only one of these UPDATEs can match, so the
// loser is rejected instead of also claiming the booking. The Redis lock alone
// cannot guarantee this, since it is released as soon as the offer is sent.
func (r *Repository) ClaimOffer(ctx context.Context, offerID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE matching_offers
		SET status = 'ACCEPTED', responded_at = NOW()
		WHERE id = $1 AND status = 'PENDING' AND expires_at > NOW()
	`, offerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseOffer returns a claim to PENDING so a failed assignment does not
// permanently strand the offer.
func (r *Repository) ReleaseOffer(ctx context.Context, offerID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE matching_offers
		SET status = 'PENDING', responded_at = NULL
		WHERE id = $1 AND status = 'ACCEPTED'
	`, offerID)
	return err
}

func (r *Repository) UpdateOfferStatus(ctx context.Context, offerID uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE matching_offers SET status = $1, responded_at = NOW() WHERE id = $2
	`, status, offerID)
	return err
}

func (r *Repository) ExpireOffers(ctx context.Context, bookingID, exceptDriverID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE matching_offers SET status = 'EXPIRED'
		WHERE booking_id = $1 AND status = 'PENDING' AND driver_id <> $2
	`, bookingID, exceptDriverID)
	return err
}

func (r *Repository) PendingOffers(ctx context.Context, bookingID uuid.UUID) ([]Offer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, booking_id, driver_id, status FROM matching_offers
		WHERE booking_id = $1 AND status = 'PENDING'
	`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var offers []Offer
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.ID, &o.BookingID, &o.DriverID, &o.Status); err != nil {
			return nil, err
		}
		offers = append(offers, o)
	}
	return offers, rows.Err()
}

// PendingOffersForDriver lists the live offers a driver has been sent, joined
// with enough booking detail to decide whether to accept.
//
// The driver's own list of assigned bookings cannot serve this: an offer exists
// before the driver is assigned, and bookings.driver_id is only set on accept.
// The driver therefore had no way to see a pending offer at all, and accept was
// only reachable from the notification's payload.
//
// Expiry is filtered for the same reason GetOfferByBookingAndDriver does: a row
// left PENDING past its deadline would otherwise appear as if it were live.
func (r *Repository) PendingOffersForDriver(ctx context.Context, driverID uuid.UUID) ([]Offer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.booking_id, o.driver_id, o.status,
		       b.pickup_location, b.pickup_lat, b.pickup_lng,
		       b.destination, b.duration_hours, b.driver_fee, b.booking_type
		FROM matching_offers o
		JOIN bookings b ON b.id = o.booking_id
		WHERE o.driver_id = $1
		  AND o.status = 'PENDING'
		  AND o.expires_at > NOW()
		  AND b.status IN ('REQUESTED', 'SEARCHING')
		ORDER BY o.created_at DESC
	`, driverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var offers []Offer
	for rows.Next() {
		var o Offer
		var b OfferBooking
		if err := rows.Scan(
			&o.ID, &o.BookingID, &o.DriverID, &o.Status,
			&b.PickupLocation, &b.PickupLat, &b.PickupLng,
			&b.Destination, &b.DurationHours, &b.DriverFee, &b.BookingType,
		); err != nil {
			return nil, err
		}
		o.Booking = b
		offers = append(offers, o)
	}
	return offers, rows.Err()
}

func (r *Repository) ExpiredOffers(ctx context.Context) ([]Offer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, booking_id, driver_id, status FROM matching_offers
		WHERE status = 'PENDING' AND expires_at <= NOW()
		LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var offers []Offer
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.ID, &o.BookingID, &o.DriverID, &o.Status); err != nil {
			return nil, err
		}
		offers = append(offers, o)
	}
	return offers, rows.Err()
}
