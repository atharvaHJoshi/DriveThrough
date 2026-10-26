package geolocation

import "math"

const EarthRadiusM = 6371000.0

type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func HaversineMeters(a, b Point) float64 {
	lat1 := toRad(a.Lat)
	lat2 := toRad(b.Lat)
	dLat := lat2 - lat1
	dLng := toRad(b.Lng - a.Lng)

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * EarthRadiusM * math.Asin(math.Sqrt(h))
}

func toRad(deg float64) float64 {
	return deg * math.Pi / 180
}
