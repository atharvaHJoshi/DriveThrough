package matching

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"drivethrough/internal/config"
	"drivethrough/internal/models"

	"drivethrough/pkg/apperr"

	"drivethrough/pkg/geolocation"
)

type MatchRequest struct {
	BookingID     uuid.UUID
	PickupLat     float64
	PickupLng     float64
	PickupText    string
	Vehicle       models.Vehicle
	MaxRadiusKM   int
	DurationHours int
	EstimatedFare int
}

type OfferPayload struct {
	BookingID  uuid.UUID         `json:"booking_id"`
	OfferID    uuid.UUID         `json:"offer_id"`
	Pickup     geolocation.Point `json:"pickup"`
	PickupText string            `json:"pickup_text"`
	Vehicle    models.Vehicle    `json:"vehicle"`
	Duration   int               `json:"duration_hours"`
	Fare       int               `json:"fare"`
	ExpiresIn  int               `json:"expires_in_seconds"`
}

type notifier interface {
	NotifyDriverOffer(ctx context.Context, driverID uuid.UUID, bookingID uuid.UUID, payload OfferPayload) error
	NotifyMatchingFailure(ctx context.Context, customerID uuid.UUID, bookingID uuid.UUID) error
	NotifyDriverAssigned(ctx context.Context, customerID uuid.UUID, bookingID uuid.UUID, driverID uuid.UUID) error
}

// BookingStore is the slice of the bookings module that matching needs.
// Declared here so matching depends on behaviour, not on another package's
// repository type, and can be tested against a stub.
type BookingStore interface {
	GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
	UpdateStatus(ctx context.Context, bookingID uuid.UUID, status models.BookingStatus, actorID uuid.UUID, notes string) error
	AssignDriver(ctx context.Context, bookingID, driverID uuid.UUID) error
}

// DriverStore is the slice of the drivers module that matching needs.
type DriverStore interface {
	NearbyOnline(ctx context.Context, lat, lng float64, radiusMeters int, caps models.VehicleCapability) ([]models.Driver, error)
	RecordOffer(ctx context.Context, driverID uuid.UUID, accepted bool) error
	SetStatus(ctx context.Context, driverID uuid.UUID, status models.DriverStatus) error
}

type Service struct {
	bookingStore BookingStore
	driverStore  DriverStore
	offerRepo    *Repository
	redis        *redis.Client
	notifier     notifier
	cfg          config.MatchingConfig
}

func NewService(
	bookingStore BookingStore,
	driverStore DriverStore,
	offerRepo *Repository,
	redis *redis.Client,
	cfg config.MatchingConfig,
) *Service {
	return &Service{
		bookingStore: bookingStore,
		driverStore:  driverStore,
		offerRepo:    offerRepo,
		redis:        redis,
		cfg:          cfg,
	}
}

func (s *Service) SetNotifier(n notifier) {
	s.notifier = n
}

type scoredDriver struct {
	driver models.Driver
	score  float64
}

func (s *Service) FindAndOffer(ctx context.Context, req MatchRequest) (int, error) {
	radiusM := req.MaxRadiusKM * 1000

	caps := models.VehicleCapability{
		Automatic: req.Vehicle.Transmission == models.TransmissionAutomatic,
		Manual:    req.Vehicle.Transmission == models.TransmissionManual,
		Hatchback: req.Vehicle.Class == models.VehicleClassHatchback,
		Sedan:     req.Vehicle.Class == models.VehicleClassSedan,
		SUV:       req.Vehicle.Class == models.VehicleClassSUV,
		Luxury:    req.Vehicle.Class == models.VehicleClassLuxury,
	}

	nearby, err := s.driverStore.NearbyOnline(ctx, req.PickupLat, req.PickupLng, radiusM, caps)
	if err != nil {
		return 0, fmt.Errorf("find nearby drivers: %w", err)
	}

	candidates := s.rank(ctx, nearby, req)
	slog.Info("matching candidates found", "booking_id", req.BookingID, "count", len(candidates))

	notify := s.cfg.MaxDriversToNotify
	if len(candidates) < notify {
		notify = len(candidates)
	}

	offered := 0
	for i := 0; i < notify; i++ {
		if err := s.offer(ctx, req.BookingID, candidates[i].driver.ID, req); err != nil {
			slog.Warn("failed to offer booking to driver", "driver_id", candidates[i].driver.ID, "error", err)
			continue
		}
		offered++
	}

	if offered == 0 {
		s.markSearchFailed(ctx, req.BookingID)
		return 0, nil
	}

	return offered, nil
}

func (s *Service) rank(ctx context.Context, nearby []models.Driver, req MatchRequest) []scoredDriver {
	candidates := make([]scoredDriver, 0, len(nearby))
	pickup := geolocation.Point{Lat: req.PickupLat, Lng: req.PickupLng}

	for _, d := range nearby {
		distance := math.MaxFloat64
		if d.CurrentLocation != nil {
			distance = geolocation.HaversineMeters(
				pickup,
				geolocation.Point{Lat: d.CurrentLocation.Latitude, Lng: d.CurrentLocation.Longitude},
			)
		}

		score := s.computeScore(d, distance)
		candidates = append(candidates, scoredDriver{driver: d, score: score})
	}

	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j-1].score < candidates[j].score; j-- {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
		}
	}

	return candidates
}

func (s *Service) computeScore(d models.Driver, distance float64) float64 {
	// 40% proximity, 25% rating, 15% acceptance, 10% low cancellation, 10% experience
	maxRadiusKM := float64(s.cfg.MaxSearchRadiusKM)

	proxScore := 40.0
	if distance > 0 {
		km := distance / 1000.0
		if km > maxRadiusKM {
			km = maxRadiusKM
		}
		proxScore = 40.0 * (1 - km/maxRadiusKM)
	}

	ratingScore := 25.0 * (d.Rating / 5.0)

	acceptanceScore := 15.0 * (d.AcceptanceRate / 100.0)

	cancellationPenalty := 10.0
	if d.CancellationRate > 0 {
		cancellationPenalty = 10.0 * (1 - d.CancellationRate/100.0)
		if cancellationPenalty < 0 {
			cancellationPenalty = 0
		}
	}

	experienceScore := 10.0 * (float64(d.ExperienceYears) / 20.0)
	if experienceScore > 10 {
		experienceScore = 10
	}

	return proxScore + ratingScore + acceptanceScore + cancellationPenalty + experienceScore
}

func (s *Service) offer(ctx context.Context, bookingID, driverID uuid.UUID, req MatchRequest) error {
	locked, err := s.acquireDriverLock(ctx, driverID)
	if err != nil {
		return err
	}
	if !locked {
		return apperr.Conflict("you are already handling another booking", nil)
	}
	defer s.releaseDriverLock(ctx, driverID)

	expires := time.Now().Add(time.Duration(s.cfg.DriverAcceptTimeout) * time.Second)
	offerID, err := s.offerRepo.CreateOffer(ctx, bookingID, driverID, expires)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}

	if s.notifier != nil {
		payload := OfferPayload{
			BookingID:  bookingID,
			OfferID:    offerID,
			Pickup:     geolocation.Point{Lat: req.PickupLat, Lng: req.PickupLng},
			PickupText: req.PickupText,
			Vehicle:    req.Vehicle,
			Duration:   req.DurationHours,
			Fare:       req.EstimatedFare,
			ExpiresIn:  s.cfg.DriverAcceptTimeout,
		}
		if err := s.notifier.NotifyDriverOffer(ctx, driverID, bookingID, payload); err != nil {
			slog.Warn("failed to notify driver of offer", "driver_id", driverID, "error", err)
		}
	}

	return nil
}

func (s *Service) acquireDriverLock(ctx context.Context, driverID uuid.UUID) (bool, error) {
	key := "lock:driver:" + driverID.String()
	ok, err := s.redis.SetNX(ctx, key, "1", time.Duration(s.cfg.DriverAcceptTimeout)*time.Second).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

func (s *Service) releaseDriverLock(ctx context.Context, driverID uuid.UUID) error {
	return s.redis.Del(ctx, "lock:driver:"+driverID.String()).Err()
}

func (s *Service) MarkBookingSearching(ctx context.Context, bookingID uuid.UUID, actorID uuid.UUID) error {
	return s.bookingStore.UpdateStatus(ctx, bookingID, models.BookingSearching, actorID, "driver search started")
}

func (s *Service) markSearchFailed(ctx context.Context, bookingID uuid.UUID) {
	if err := s.bookingStore.UpdateStatus(ctx, bookingID, models.BookingSearching, uuid.Nil, "no drivers found"); err != nil {
		slog.Warn("failed to mark search failed", "booking_id", bookingID, "error", err)
	}

	booking, err := s.bookingStore.GetBooking(ctx, bookingID)
	if err != nil {
		return
	}
	if s.notifier != nil {
		if err := s.notifier.NotifyMatchingFailure(ctx, booking.CustomerID, bookingID); err != nil {
			slog.Warn("failed to notify matching failure", "error", err)
		}
	}
}

func (s *Service) AcceptOffer(ctx context.Context, driverID, bookingID, offerID uuid.UUID) error {
	offer, err := s.offerRepo.GetOfferByBookingAndDriver(ctx, bookingID, driverID)
	if err != nil {
		return apperr.Conflict("offer expired or already answered", nil)
	}
	// The offer is already resolved from (booking, driver), so offer_id is an
	// optional optimistic-concurrency check rather than a required field: clients
	// that omit it should not have to construct a UUID.
	if offerID != uuid.Nil && offer.ID != offerID {
		return apperr.Conflict("offer no longer current", nil)
	}

	locked, err := s.acquireDriverLock(ctx, driverID)
	if err != nil {
		return err
	}
	if !locked {
		return apperr.Conflict("you are already handling another booking", nil)
	}
	defer s.releaseDriverLock(ctx, driverID)

	// Claim the offer first. This is the arbiter when two drivers accept
	// simultaneously: without it, both would proceed to AssignDriver and the
	// second would overwrite the first. Uses offer.ID rather than the caller's
	// optional offerID, which is uuid.Nil when the field is omitted.
	claimed, err := s.offerRepo.ClaimOffer(ctx, offer.ID)
	if err != nil {
		return fmt.Errorf("claim offer: %w", err)
	}
	if !claimed {
		return apperr.Conflict("offer expired or already answered", nil)
	}

	if err := s.bookingStore.AssignDriver(ctx, bookingID, driverID); err != nil {
		// Hand the offer back so a transient failure does not burn it.
		if relErr := s.offerRepo.ReleaseOffer(ctx, offer.ID); relErr != nil {
			slog.Warn("failed to release claimed offer", "offer_id", offer.ID, "error", relErr)
		}
		return err
	}

	if err := s.driverStore.RecordOffer(ctx, driverID, true); err != nil {
		slog.Warn("failed to record offer acceptance", "driver_id", driverID, "error", err)
	}

	if err := s.driverStore.SetStatus(ctx, driverID, models.DriverStatusBusy); err != nil {
		slog.Warn("failed to mark driver busy", "driver_id", driverID, "error", err)
	}

	if err := s.offerRepo.ExpireOffers(ctx, bookingID, driverID); err != nil {
		slog.Warn("failed to expire other offers", "error", err)
	}

	if s.notifier != nil {
		if err := s.notifier.NotifyDriverAssigned(ctx, uuid.Nil, bookingID, driverID); err != nil {
			slog.Warn("failed to notify customer of assignment", "error", err)
		}
	}

	return nil
}

// PendingOffersForDriver lists the offers awaiting this driver's answer.
//
// offer_id is not required to accept or decline — the backend resolves the offer
// from (booking, driver) and treats a stale id as a no-op — so the row is
// identified by its booking.
func (s *Service) PendingOffersForDriver(ctx context.Context, driverID uuid.UUID) ([]Offer, error) {
	offers, err := s.offerRepo.PendingOffersForDriver(ctx, driverID)
	if err != nil {
		return nil, err
	}
	if offers == nil {
		// An empty list marshals as null otherwise, which the client has to tell
		// apart from "not loaded yet".
		return []Offer{}, nil
	}
	return offers, nil
}

func (s *Service) RejectOffer(ctx context.Context, driverID, bookingID, offerID uuid.UUID) error {
	offer, err := s.offerRepo.GetOfferByBookingAndDriver(ctx, bookingID, driverID)
	if err != nil {
		return nil
	}
	// Mirrors AcceptOffer: offer_id is optional, and a stale value is treated as
	// a no-op rather than an error. Comparing against the caller's optional
	// offerID while writing to it meant an omitted offer_id silently declined
	// nothing while still replying "declined" to the driver.
	if offerID != uuid.Nil && offer.ID != offerID {
		return nil
	}

	if err := s.driverStore.RecordOffer(ctx, driverID, false); err != nil {
		slog.Warn("failed to record offer rejection", "driver_id", driverID, "error", err)
	}

	return s.offerRepo.UpdateOfferStatus(ctx, offer.ID, "DECLINED")
}

func (s *Service) TimeoutOffers(ctx context.Context, bookingID uuid.UUID) {
	offers, err := s.offerRepo.PendingOffers(ctx, bookingID)
	if err != nil {
		return
	}
	for _, o := range offers {
		_ = s.offerRepo.UpdateOfferStatus(ctx, o.ID, "TIMEOUT")
	}
}
