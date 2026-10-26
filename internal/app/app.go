// Package app is the composition root. It owns object construction and nothing
// else: no business rules, no SQL, no HTTP handlers. Swapping a dependency means
// editing exactly one file in this package.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"drivethrough/internal/admin"
	"drivethrough/internal/auth"
	"drivethrough/internal/bookings"
	"drivethrough/internal/config"
	"drivethrough/internal/drivers"
	"drivethrough/internal/incidents"
	"drivethrough/internal/location"
	"drivethrough/internal/matching"
	"drivethrough/internal/notifications"
	"drivethrough/internal/payments"
	"drivethrough/internal/ratings"
	"drivethrough/internal/sms"
	"drivethrough/internal/trips"
	"drivethrough/internal/users"
	"drivethrough/internal/vehicles"
	"drivethrough/internal/websocket"
	"drivethrough/internal/webui"
	"drivethrough/pkg/httpx"
)

// Application holds every constructed dependency plus the long-lived
// background workers that must be started and stopped with the process.
type Application struct {
	Handler http.Handler
	Workers *WorkerSet
	Hub     *websocket.Hub
}

// Deps are the external resources the application needs. Every one of them is
// supplied by the caller, so tests can run against fakes and nothing is
// constructed implicitly.
type Deps struct {
	Cfg   *config.Config
	DB    *pgxpool.Pool
	Redis *redis.Client
	JWT   *auth.JWTManager
	Hub   *websocket.Hub
}

// New wires the dependency graph and returns a ready-to-serve application.
// It performs no I/O beyond what the supplied clients already hold open, and
// it starts no goroutines; call Start for that.
func New(deps Deps) *Application {
	cfg := deps.Cfg

	// Shared outbound HTTP client: one connection pool, one timeout policy.
	httpClient := httpx.NewClient(cfg.HTTP.Timeout)

	otpManager := auth.NewOTPManager(deps.Redis, cfg.OTP.Expiry, cfg.OTP.Length)

	usersRepo := users.NewRepository(deps.DB)
	driversRepo := drivers.NewRepository(deps.DB)
	vehiclesRepo := vehicles.NewRepository(deps.DB)
	bookingsRepo := bookings.NewRepository(deps.DB)
	tripsRepo := trips.NewRepository(deps.DB)
	paymentsRepo := payments.NewRepository(deps.DB)
	ratingsRepo := ratings.NewRepository(deps.DB)
	incidentsRepo := incidents.NewRepository(deps.DB)
	adminRepo := admin.NewRepository(deps.DB)
	offersRepo := matching.NewRepository(deps.DB)

	locSvc := location.NewService(deps.Redis)
	smsSender := sms.NewSender(sms.Config{
		Provider: cfg.SMS.Provider,
		BaseURL:  cfg.SMS.BaseURL,
		APIKey:   cfg.SMS.APIKey,
		SenderID: cfg.SMS.SenderID,
	}, httpClient)

	// Services
	usersSvc := users.NewService(usersRepo, deps.JWT, otpManager, smsSender)
	driversSvc := drivers.NewService(driversRepo, usersRepo, locSvc)
	vehiclesSvc := vehicles.NewService(vehiclesRepo)
	pricingSvc := bookings.NewPricingService(cfg.Pricing)
	bookingSvc := bookings.NewService(bookingsRepo, pricingSvc, vehiclesSvc, driversSvc, cfg.OTP.Length)
	notifsSvc := notifications.NewService(deps.DB, cfg.FCM, httpClient)
	tripsSvc := trips.NewService(tripsRepo, bookingSvc, driversSvc, deps.Hub, cfg.Pricing.BaseDriverFeePerHour)

	// matching talks to bookings and drivers through their services, so module
	// boundaries stay one-way: matching imports neither package.
	matchingSvc := matching.NewService(bookingSvc, driversSvc, offersRepo, deps.Redis, cfg.Matching)

	// Payments is constructed before the coordinator because settlement on trip
	// completion is one of the coordinator's reactions.
	paymentsSvc := payments.NewService(paymentsRepo, payments.NewRazorpayClient(cfg.Razorpay, httpClient), driversSvc, bookingSvc, notifsSvc)

	// The coordinator implements matching.Notifier, so matching is constructed
	// first and then handed the coordinator via setter injection. This breaks
	// the construction cycle without exporting internals.
	coordinator := NewCoordinator(bookingSvc, matchingSvc, notifsSvc, driversSvc, vehiclesSvc, paymentsSvc, usersSvc, smsSender, cfg.Matching.MaxSearchRadiusKM)
	matchingSvc.SetNotifier(coordinator)
	tripsSvc.SetCompletionListener(coordinator)
	bookingSvc.SetCancellationReactions(coordinator)

	// Live trip subscriptions are authorised by the bookings module, which knows
	// both the customer and the assigned driver. Wired after construction to
	// avoid the trips -> bookings -> trips cycle.
	bookingSvc.SetTripLookup(tripsSvc)
	deps.Hub.SetAuthorizer(bookingSvc)

	ratingsSvc := ratings.NewService(ratingsRepo, bookingSvc.GetBooking, driversSvc)
	incidentsSvc := incidents.NewService(incidentsRepo, bookingSvc, notifsSvc, smsSender)
	incidentsSvc.SetTripLookup(tripsSvc)
	incidentsSvc.SetDriverLookup(driversRepo)
	adminSvc := admin.NewService(adminRepo, driversSvc, incidentsSvc, bookingSvc)

	// Handlers
	usersHandler := users.NewHandler(usersSvc)
	driversHandler := drivers.NewHandler(driversSvc)
	vehiclesHandler := vehicles.NewHandler(vehiclesSvc)
	bookingsHandler := bookings.NewHandler(bookingSvc, coordinator)
	matchingHandler := matching.NewHandler(matchingSvc, driversSvc, bookingSvc)
	tripsHandler := trips.NewHandler(tripsSvc)
	paymentsHandler := payments.NewHandler(paymentsSvc)
	ratingsHandler := ratings.NewHandler(ratingsSvc, ratingsRepo, bookingSvc)
	incidentsHandler := incidents.NewHandler(incidentsSvc)
	notifsHandler := notifications.NewHandler(notifsSvc)
	adminHandler := admin.NewHandler(adminSvc)

	routes := Routes{
		Users:         usersHandler,
		Drivers:       driversHandler,
		Vehicles:      vehiclesHandler,
		Bookings:      bookingsHandler,
		Matching:      matchingHandler,
		Trips:         tripsHandler,
		Payments:      paymentsHandler,
		Ratings:       ratingsHandler,
		Incidents:     incidentsHandler,
		Notifications: notifsHandler,
		Admin:         adminHandler,
		JWT:           deps.JWT,
		Hub:           deps.Hub,
		Cfg:           cfg,
		Ready:         readinessCheck(deps.DB, deps.Redis),
		SPA:           webui.Handler(),
	}

	workers := &WorkerSet{
		coordinator: coordinator,
		bookingRepo: bookingsRepo,
		offerRepo:   offersRepo,
		cfg:         cfg.Workers,
	}

	return &Application{
		Handler: NewRouter(routes),
		Workers: workers,
		Hub:     deps.Hub,
	}
}

// Start launches background goroutines. The caller owns ctx: cancelling it
// stops every worker, which is what makes graceful shutdown possible.
func (a *Application) Start(ctx context.Context) {
	go a.Hub.EventLoop()

	if !a.Workers.enabled() {
		slog.Info("background workers disabled")
		return
	}
	a.Workers.Start(ctx)
}

// readinessCheck reports whether both datastores answer, so a load balancer
// stops routing to a replica that has lost Postgres or Redis.
func readinessCheck(db *pgxpool.Pool, rdb *redis.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		return nil
	}
}
