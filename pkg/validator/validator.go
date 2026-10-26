package validator

import (
	"regexp"
	"strings"
)

var (
	phoneRegex  = regexp.MustCompile(`^\+?[0-9]{10,15}$`)
	regNumRegex = regexp.MustCompile(`^[A-Z]{2}[0-9]{1,2}[A-Z]{1,3}[0-9]{1,4}$`)
	emailRegex  = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

func IsValidPhone(phone string) bool {
	return phoneRegex.MatchString(phone)
}

func IsValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

func IsValidRegistrationNumber(reg string) bool {
	clean := strings.ReplaceAll(strings.ToUpper(reg), " ", "")
	return regNumRegex.MatchString(clean)
}

func NormalizeRegNumber(reg string) string {
	return strings.ToUpper(strings.ReplaceAll(reg, " ", ""))
}

func IsValidLat(lat float64) bool {
	return lat >= -90 && lat <= 90
}

func IsValidLng(lng float64) bool {
	return lng >= -180 && lng <= 180
}

func IsValidCoordinate(lat, lng float64) bool {
	return IsValidLat(lat) && IsValidLng(lng)
}

func IsValidRating(rating int) bool {
	return rating >= 1 && rating <= 5
}

func IsValidDurationHours(hours int) bool {
	return hours >= 1 && hours <= 24
}
