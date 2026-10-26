package location

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"drivethrough/pkg/geolocation"
)

const (
	driverLocationKey = "drv:loc"
	driverStateKey    = "drv:state"
)

type DriverLocation struct {
	DriverID  uuid.UUID `json:"driver_id"`
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Service struct {
	client *redis.Client
}

func NewService(client *redis.Client) *Service {
	return &Service{client: client}
}

func (s *Service) UpdateDriverLocation(ctx context.Context, driverID uuid.UUID, lat, lng float64) error {
	// Redis GEO: GEOFADD driverLocationKey, lng lat member
	return s.client.GeoAdd(ctx, driverLocationKey, &redis.GeoLocation{
		Name:      driverID.String(),
		Longitude: lng,
		Latitude:  lat,
	}).Err()
}

func (s *Service) NearbyDrivers(ctx context.Context, p geolocation.Point, radiusMeters float64) ([]DriverLocation, error) {
	res, err := s.client.GeoSearchLocation(ctx, driverLocationKey, &redis.GeoSearchLocationQuery{
		GeoSearchQuery: redis.GeoSearchQuery{
			Longitude:  p.Lng,
			Latitude:   p.Lat,
			Radius:     radiusMeters,
			RadiusUnit: "m",
			Sort:       "ASC",
		},
	}).Result()
	if err != nil {
		return nil, err
	}

	result := make([]DriverLocation, 0, len(res))
	for _, loc := range res {
		did, err := uuid.Parse(loc.Name)
		if err != nil {
			continue
		}
		result = append(result, DriverLocation{
			DriverID: did,
			Lat:      loc.Latitude,
			Lng:      loc.Longitude,
		})
	}
	return result, nil
}

func (s *Service) RemoveDriver(ctx context.Context, driverID uuid.UUID) error {
	return s.client.ZRem(ctx, driverLocationKey, driverID.String()).Err()
}

func (s *Service) GetDriverLocation(ctx context.Context, driverID uuid.UUID) (*geolocation.Point, error) {
	res, err := s.client.GeoPos(ctx, driverLocationKey, driverID.String()).Result()
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0] == nil {
		return nil, nil
	}
	return &geolocation.Point{Lat: res[0].Latitude, Lng: res[0].Longitude}, nil
}

type DriverState struct {
	Status string  `json:"status"`
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
}

func (s *Service) SetDriverState(ctx context.Context, driverID uuid.UUID, state DriverState, ttl time.Duration) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, driverStateKey+":"+driverID.String(), data, ttl).Err()
}

func (s *Service) GetDriverState(ctx context.Context, driverID uuid.UUID) (*DriverState, error) {
	data, err := s.client.Get(ctx, driverStateKey+":"+driverID.String()).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state DriverState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
