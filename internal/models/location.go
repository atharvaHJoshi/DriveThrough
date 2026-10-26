package models

// Location is a WGS-84 coordinate pair.
//
// PostGIS stores driver positions as geography(POINT, 4326), which pgx has no
// codec for, so reads project the point back to plain float8 columns with
// ST_Y/ST_X. Keeping the domain type as lat/lng also matches every other
// coordinate in the API surface.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
