package bookings

import (
	"drivethrough/internal/config"
	"drivethrough/internal/models"
)

type Taxes struct {
	GSTRate float64
}

type PricingService struct {
	baseDriverFeePerHour int
	platformFee          int
	protectionFee        int
	taxRate              float64
}

func NewPricingService(cfg config.PricingConfig) *PricingService {
	return &PricingService{
		baseDriverFeePerHour: cfg.BaseDriverFeePerHour,
		platformFee:          cfg.PlatformFee,
		protectionFee:        cfg.ProtectionFee,
		taxRate:              0.18,
	}
}

const (
	NightSurchargeStartHour = 22
	NightSurchargeEndHour   = 6
	NightSurchargeRate      = 1.20
)

func isNightHour(hour int) bool {
	return hour >= NightSurchargeStartHour || hour < NightSurchargeEndHour
}

type PriceEstimate struct {
	DriverFee     int                   `json:"driver_fee"`
	PlatformFee   int                   `json:"platform_fee"`
	ProtectionFee int                   `json:"protection_fee"`
	Tax           int                   `json:"tax"`
	Total         int                   `json:"total"`
	Breakdown     models.PriceBreakdown `json:"breakdown"`
}

func (p *PricingService) Estimate(durationHours int, night bool) PriceEstimate {
	if durationHours < 1 {
		durationHours = 1
	}

	driverFee := p.baseDriverFeePerHour * durationHours
	if night {
		driverFee = round(float64(driverFee) * NightSurchargeRate)
	}

	platformFee := p.platformFee
	protectionFee := p.protectionFee

	beforeTax := driverFee + platformFee + protectionFee
	tax := round(float64(beforeTax) * p.taxRate)
	total := beforeTax + tax

	breakdown := models.PriceBreakdown{
		DriverFee:  driverFee,
		Platform:   platformFee,
		Protection: protectionFee,
		Tax:        tax,
		Total:      total,
	}

	return PriceEstimate{
		DriverFee:     driverFee,
		PlatformFee:   platformFee,
		ProtectionFee: protectionFee,
		Tax:           tax,
		Total:         total,
		Breakdown:     breakdown,
	}
}

func round(f float64) int {
	rf := float64(int(f))
	if f-rf >= 0.5 {
		return int(f) + 1
	}
	return int(f)
}
