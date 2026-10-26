# DriveThrough

Driver-as-a-service marketplace platform. Customers book professional drivers for their own vehicles — hourly, one-way, or round trips — with driver matching, live tracking, payments, and safety features.

## Tech Stack

- **Language:** Go 1.27
- **HTTP:** go-chi/chi v5
- **Database:** PostgreSQL + PostGIS
- **Cache/Realtime:** Redis
- **WebSocket:** gorilla/websocket
- **Auth:** JWT (golang-jwt/v5) + OTP phone login
- **Payments:** Razorpay
- **Push:** Firebase Cloud Messaging

## Architecture

```
cmd/server          Entry point (config, DB/Redis, HTTP server)
internal/app        Router wiring + background workers + coordinator
internal/auth       JWT issue/parse/logout + OTP + token blacklist
internal/users      OTP login/signup, profiles, token refresh
internal/drivers    Profiles, availability, docs, location, nearby search
internal/vehicles   Customer vehicle CRUD, default vehicle
internal/bookings   Booking lifecycle, state machine, pricing
internal/matching   Driver discovery, weighted scoring, offers
internal/trips      Trip lifecycle, live location, inspections
internal/payments   Razorpay orders, webhooks, refunds
internal/ratings    Bidirectional ratings + driver aggregation
internal/incidents  SOS, incident reporting, emergency contacts
internal/notifications  In-app + FCM push
internal/admin      Dashboard, driver verification, suspensions
internal/websocket  Live trip location broadcasting
pkg/                Shared: apperr, response, validator, logger, geolocation
```

## Requirements

- Go 1.27+
- PostgreSQL 14+ with PostGIS
- Redis 6+

## Running locally

```bash
cp .env.example .env
# start dependencies via Docker
docker compose -f docker/docker-compose.yml up -d postgres redis
go run ./cmd/server
```

Or run everything with Docker:

```bash
docker compose -f docker/docker-compose.yml up --build
```

## Key Flows

1. **Login** — `POST /api/v1/auth/send-otp` → SMS (or dev log) → `POST /api/v1/auth/verify-otp` → tokens
2. **Booking** — customer creates a booking → coordinator runs driver matching → offers pushed to drivers → first accept wins
3. **Trip** — driver arrives → customer verifies OTP → trip starts → live location via WebSocket → trip ends with inspections → payment captures
4. **Safety** — SOS and incident endpoints, admin resolution
5. **Refunds** — customer requests refund on cancelled/overcharged bookings; Razorpay refund webhooks update status

## Environment Variables

See `.env.example` for all supported variables. Key ones:

| Variable | Description | Default |
|---|---|---|
| `SERVER_PORT` | HTTP port | `8080` |
| `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME` | Postgres conn | localhost:5432 |
| `REDIS_HOST`/`REDIS_PORT` | Redis conn | localhost:6379 |
| `JWT_SECRET` | Signing secret | change-me |
| `SMS_PROVIDER` | `log` (dev), `msg91` | `log` |
| `RAZORPAY_KEY_ID`/`RAZORPAY_KEY_SECRET` | Live payments | empty → mock mode |
| `FCM_PROJECT_ID` | Push notifications | empty → disabled |
| `BASE_DRIVER_FEE_PER_HOUR` | Pricing base (INR) | `150` |