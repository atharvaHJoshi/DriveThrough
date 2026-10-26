package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"drivethrough/internal/admin"
	"drivethrough/internal/auth"
	"drivethrough/internal/bookings"
	"drivethrough/internal/config"
	"drivethrough/internal/drivers"
	"drivethrough/internal/incidents"
	"drivethrough/internal/matching"
	mdw "drivethrough/internal/middleware"
	"drivethrough/internal/models"
	"drivethrough/internal/notifications"
	"drivethrough/internal/payments"
	"drivethrough/internal/ratings"
	"drivethrough/internal/trips"
	"drivethrough/internal/users"
	"drivethrough/internal/vehicles"
	"drivethrough/internal/websocket"
)

// Routes is the complete set of HTTP handlers plus the middleware
// collaborators, so route registration stays declarative and testable.
type Routes struct {
	Users         *users.Handler
	Drivers       *drivers.Handler
	Vehicles      *vehicles.Handler
	Bookings      *bookings.Handler
	Matching      *matching.Handler
	Trips         *trips.Handler
	Payments      *payments.Handler
	Ratings       *ratings.Handler
	Incidents     *incidents.Handler
	Notifications *notifications.Handler
	Admin         *admin.Handler
	JWT           *auth.JWTManager
	Hub           *websocket.Hub
	Cfg           *config.Config
	Ready         func(ctx context.Context) error
	// SPA serves the compiled frontend. It is mounted as the router's NotFound
	// handler, so the API routes always take precedence and only unclaimed paths
	// fall through to the single-page app. Nil disables UI serving (API-only).
	SPA http.Handler
}

func NewRouter(r Routes) http.Handler {
	limiter := mdw.NewRateLimiter(r.Cfg.RateLimit.GlobalLimit, r.Cfg.RateLimit.GlobalWindow)
	otpLimiter := mdw.NewRateLimiter(r.Cfg.RateLimit.OTPLimit, r.Cfg.RateLimit.OTPWindow)

	router := chi.NewRouter()

	// Order matters here. RequestID runs first so that everything inside it,
	// including the recovery handler, can report the id of the request that
	// failed. Recovery must sit outside the rate limiter and every route so a
	// panic anywhere is caught rather than taking the process down.
	router.Use(mdw.RequestIDMiddleware)
	router.Use(mdw.RecoveryMiddleware)
	router.Use(mdw.SecurityHeaders)
	router.Use(mdw.CORSMiddleware(r.Cfg.Server.AllowedOrigins))
	// X-Forwarded-For is only honoured from explicitly configured peers, since
	// the rate limiter keys on the resolved client address.
	router.Use(mdw.TrustedProxyRealIP(r.Cfg.Server.TrustedProxies))
	router.Use(limiter.Middleware)

	router.Get("/health", healthHandler)
	router.Get("/ready", readyHandler(r.Ready))

	router.Route("/api/v1", func(api chi.Router) {
		// WebSocket lives outside the timeout middleware: a hijacked
		// connection outlives any request deadline, and cancelling the
		// context would tear down a live trip-tracking socket.
		api.Group(func(ws chi.Router) {
			ws.Use(mdw.AuthMiddleware(r.JWT))
			ws.Get("/ws/live", r.Hub.ServeWS)
		})

		api.Group(func(api chi.Router) {
			api.Use(middleware.Timeout(r.Cfg.Server.RequestTimeout))

			// Public
			api.With(otpLimiter.Middleware).Post("/auth/send-otp", r.Users.SendOTP)
			api.Post("/auth/verify-otp", r.Users.VerifyOTP)
			api.Post("/auth/refresh", r.Users.Refresh)
			api.Post("/payments/webhook/razorpay", r.Payments.Webhook)

			api.Group(func(private chi.Router) {
				private.Use(mdw.AuthMiddleware(r.JWT))

				// Customer: vehicles
				private.Post("/vehicles", r.Vehicles.CreateVehicle)
				private.Get("/vehicles", r.Vehicles.ListVehicles)
				private.Get("/vehicles/{id}", r.Vehicles.GetVehicle)
				private.Patch("/vehicles/{id}", r.Vehicles.UpdateVehicle)
				private.Delete("/vehicles/{id}", r.Vehicles.DeleteVehicle)
				private.Post("/vehicles/{id}/default", r.Vehicles.SetDefault)

				// Customer: bookings
				private.Post("/bookings/estimate", r.Bookings.Estimate)
				private.Post("/bookings", r.Bookings.CreateBooking)
				private.Get("/bookings", r.Bookings.ListCustomerBookings)
				private.Get("/bookings/{id}", r.Bookings.GetBooking)
				// Live tracking is addressed by trip id, which a booking does not
				// carry, so this resolves the two for a party to the booking.
				private.Get("/bookings/{id}/trip", r.Bookings.TripForBooking)
				private.Post("/bookings/{id}/cancel", r.Bookings.CancelBooking)
				private.Post("/bookings/{id}/verify-otp", r.Bookings.VerifyOTP)

				// Ratings
				private.Post("/bookings/{id}/rate", r.Ratings.Rate)
				private.Get("/bookings/{id}/ratings", r.Ratings.ByBooking)

				// Payments
				private.Post("/bookings/{booking_id}/payment/initiate", r.Payments.Initiate)
				private.Get("/bookings/{booking_id}/payment", r.Payments.GetByBooking)
				private.Post("/bookings/{booking_id}/refund", r.Payments.Refund)

				// Driver flows
				private.Post("/driver/register", r.Drivers.Register)
				private.Get("/driver/profile", r.Drivers.Profile)
				private.Post("/driver/online", r.Drivers.GoOnline)
				private.Post("/driver/offline", r.Drivers.GoOffline)
				private.Patch("/driver/capabilities", r.Drivers.UpdateCapabilities)
				private.Post("/driver/location", r.Drivers.UpdateLocation)
				private.Post("/driver/documents", r.Drivers.SubmitDocument)
				private.Get("/driver/documents", r.Drivers.Documents)
				private.Get("/driver/earnings", r.Drivers.Earnings)
				private.Get("/driver/bookings", r.Bookings.ListDriverBookings)
				private.Get("/driver/bookings/current", r.Matching.CurrentBooking)
				// Pending offers. Assigned-bookings only shows work after an
				// offer is accepted, so this is how a driver sees new requests.
				private.Get("/driver/offers", r.Matching.Offers)
				private.Post("/driver/bookings/{id}/accept", r.Matching.Accept)
				private.Post("/driver/bookings/{id}/reject", r.Matching.Reject)
				private.Post("/driver/bookings/{id}/cancel", r.Bookings.DriverCancel)
				// Arrival workflow. Without these the booking could never leave
				// DRIVER_ASSIGNED, so no trip could ever start.
				private.Post("/driver/bookings/{id}/en-route", r.Bookings.MarkEnRoute)
				private.Post("/driver/bookings/{id}/arrived", r.Bookings.MarkArrived)
				private.Post("/driver/bookings/{id}/no-show", r.Bookings.MarkNoShow)
				private.Get("/driver/{id}", r.Drivers.GetByID)

				// Trips
				private.Post("/trips/start", r.Trips.StartTrip)
				private.Post("/trips/{id}/pause", r.Trips.PauseTrip)
				private.Post("/trips/{id}/resume", r.Trips.ResumeTrip)
				private.Post("/trips/{id}/end", r.Trips.EndTrip)
				private.Post("/trips/{id}/location", r.Trips.RecordLocation)
				private.Get("/trips/{id}/inspections", r.Trips.Inspections)

				// Safety
				private.Post("/sos", r.Incidents.TriggerSOS)
				private.Post("/incidents", r.Incidents.ReportIncident)
				private.Get("/emergency-contacts", r.Incidents.ListContacts)
				private.Post("/emergency-contacts", r.Incidents.AddContact)

				// Notifications
				private.Get("/notifications", r.Notifications.List)
				private.Post("/notifications/{id}/read", r.Notifications.MarkRead)
				private.Post("/notifications/device-token", r.Notifications.RegisterDeviceToken)

				// Account
				private.Get("/auth/me", r.Users.Profile)
				private.Post("/auth/logout", r.Users.Logout)
				private.Patch("/users/me", r.Users.UpdateProfile)

				// Admin
				private.Route("/admin", func(adminRoutes chi.Router) {
					adminRoutes.Use(mdw.RequireRole(models.RoleAdmin))
					adminRoutes.Get("/dashboard", r.Admin.Dashboard)
					adminRoutes.Get("/drivers/pending", r.Admin.PendingDrivers)
					adminRoutes.Get("/drivers/{id}", r.Admin.DriverDetail)
					adminRoutes.Post("/drivers/{id}/verify", r.Admin.VerifyDriver)
					adminRoutes.Post("/drivers/{id}/suspend", r.Admin.SuspendDriver)
					adminRoutes.Post("/documents/{id}/verify", r.Admin.VerifyDocument)
					adminRoutes.Get("/drivers/{id}/earnings", r.Admin.DriverEarnings)
					adminRoutes.Get("/bookings", r.Admin.Bookings)
					adminRoutes.Get("/incidents", r.Admin.Incidents)
					adminRoutes.Get("/incidents/{id}", r.Admin.IncidentDetail)
					adminRoutes.Post("/incidents/{id}/resolve", r.Admin.ResolveIncident)
				})
			})
		})
	})

	// The SPA is the fallback for every path the API did not claim. Registering
	// it as NotFound (rather than a "/*" route) keeps the API authoritative:
	// chi only invokes this after exhausting its own route table.
	if r.SPA != nil {
		router.NotFound(r.SPA.ServeHTTP)
	}

	return router
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// readyHandler reports whether dependencies are actually reachable, so an
// orchestrator can hold traffic back until Postgres and Redis respond.
func readyHandler(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := check(ctx); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}
