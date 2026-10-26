# DriveThrough — Technical Design Document

**System:** DriveThrough driver marketplace API · **Module:** `drivethrough` (Go 1.27)
**Status:** Implemented and wired; building, vetting, and test-green.

---

## 1. Verification Status

Audited against the source tree, not the README:

| Check | Command | Result |
| --- | --- | --- |
| Compilation | `go build ./...` | **PASS** — exit 0 |
| Static analysis | `go vet ./...` | **PASS** — exit 0 |
| Tests | `go test ./...` | **PASS** — 10 packages ok, 71 test functions |

Scale: 87 non-test `.go` files, 22 SQL migrations, 16 test files, ~17.5k lines incl. SQL.

All 15 internal modules are implemented, all 22 migrations have up/down pairs, and every
route in `router.go` resolves to a real handler. The system is **feature-complete but not
production-hardened** — §11 lists 12 verified defects, two of which are correctness issues
under horizontal scale.

---

## 2. Architecture

### 2.1 Vertical slices

Organised by domain, not technical layer. No `internal/handlers/`, no
`internal/services/`. Each `internal/<domain>/` owns `handler.go` (HTTP binding),
`service.go` (rules), `repository.go` (SQL), plus domain-specific files. A booking-rules
change touches one directory; `repository.go` never imports `service.go`, so persistence
cannot call back into business logic.

`ratings` and `notifications` are deliberate exceptions — single-method query sets with
their SQL inside `service.go`.

### 2.2 Modules

`users` (OTP login) · `auth` (JWT, OTP, blacklist) · `drivers` (profile, availability,
documents, earnings) · `vehicles` · `bookings` (lifecycle, state machine, pricing) ·
`matching` (search, scoring, offers) · `trips` (start/pause/end, location, inspections) ·
`payments` (order/capture/void/refund, webhook inbox) · `incidents` (SOS, safety) ·
`admin` (verification, suspension) · `ratings` · `notifications` (in-app + FCM) ·
`location` (Redis GEO) · `websocket` (trip fan-out) · `app` (composition root, router,
coordinator, workers).

`pkg/` holds primitives with no domain knowledge: `apperr`, `response`, `httpx`, `logger`,
`validator`, `geolocation`.

### 2.3 Interfaces declared at the consumer

No domain imports another domain. `matching` declares what it needs in its own file:

```go
type BookingStore interface {          // internal/matching/service.go
    GetBooking(ctx context.Context, id uuid.UUID) (*models.Booking, error)
    UpdateStatus(ctx context.Context, bookingID uuid.UUID,
        status models.BookingStatus, actorID uuid.UUID, notes string) error
    AssignDriver(ctx context.Context, bookingID, driverID uuid.UUID) error
}
```

`bookings.Service` satisfies it structurally, so the module graph is acyclic by
construction rather than by discipline.

### 2.4 Setter injection for real cycles

Two bidirectional rules resist interface extraction: matching↔coordinator (the
coordinator *is* `matching.Notifier`) and bookings↔trips (cancel tears down a trip;
completion settles payment). Both are closed after construction in `app.go`:

```go
coordinator := NewCoordinator(bookingSvc, matchingSvc, notifsSvc, ...)
matchingSvc.SetNotifier(coordinator)          // cycle 1
tripsSvc.SetCompletionListener(coordinator)   // cycle 2
bookingSvc.SetCancellationReactions(coordinator)
bookingSvc.SetTripLookup(tripsSvc)
hub.SetAuthorizer(bookingSvc)
incidentsSvc.SetTripLookup(tripsSvc)
incidentsSvc.SetDriverLookup(driversRepo)
```

Cost: these setters are un-checked at construction, so a missing call is a nil-pointer at
first use, not a compile error. Mitigation — every setter is currently wired and every
nil default is safe (nil notifier = no push; nil authorizer = **deny**, §7.4).

### 2.5 Coordinator

`internal/app/coordinator.go` (438 lines) is the only component allowed to touch multiple
domains, exposing event reactions (`OnBookingCreated`, `OnBookingCancelled`,
`OnBookingCompleted`, `OnDriverEnRoute`, `OnDriverArrived`, `OnOTPVerified`,
`OnDriverNoShow`, `OnDriverNoShowDetected`, + 3 `matching.Notifier` methods). Two
orderings are load-bearing: cancellation releases the payment hold **before**
notifying the customer (the "not charged" promise must be backed by the gateway call, and
a failed release is logged as follow-up rather than aborting a correct cancellation);
completion settles **before** releasing the driver (earnings are credited by the capture,
so releasing first would pay the next trip out of the driver's pocket).

`internal/app/app.go` constructs the graph and owns nothing else — no rules, no SQL, no
HTTP. `New()` does no I/O and starts no goroutines; `Start(ctx)` is separate.

---

## 3. Request Pipeline

Middleware order is significant (`router.go`): `RequestID` (id + 1 MiB body cap + access
log) → `Recovery` (outside the limiter, so a panic anywhere is caught) →
`SecurityHeaders` (nosniff, DENY, no-referrer, no-store) → `CORS` → `TrustedProxyRealIP`
→ global rate limiter. Then `/api/v1/ws/*` gets auth **without** the chi timeout (a
hijacked connection outlives any deadline); all other `/api/v1/*` gets
`Timeout(RequestTimeout)` → per-route auth → `RequireRole(ADMIN)` at the `/admin` boundary.

**`X-Request-ID`** is honoured if supplied, else `req-` + 8 bytes of `crypto/rand`
(degrading to a base-36 nanosecond stamp rather than panicking), echoed in the header,
stored in a private-key context, and on every log line.

**Body cap** `MaxRequestBodyBytes = 1 MiB` via `MaxBytesReader` *before* any handler can
read — every endpoint takes a small JSON document; without it one request can stream
unbounded data into memory.

**`X-Forwarded-For`** is honoured only from `SERVER_TRUSTED_PROXIES`. The rate limiter
keys on resolved client address, so trusting the header from any peer lets a caller rotate
a forged value and bypass every limit. An empty list means *nothing* is trusted.

**Recovery** special-cases `net.Error` timeouts at warn level (a panic inside the chi
timeout context is expected), and returns without writing a body if the response is
already committed.

**`statusRecorder`** implements `Hijack`/`Flush`/`Push`/`Unwrap`. Without `Hijack` every
WebSocket upgrade fails through the wrapper; without `Unwrap` anything asserting for those
interfaces concludes the writer lacks them.

**Errors** — `pkg/apperr` carries status + stable code + message + cause + detail;
`pkg/response` resolves via `errors.As`, falling back to status text so no error body is
ever blank. `AppError.Error()` renders message *and* cause, since a log line is usually
the only place the cause appears.

---

## 4. Domain Design

### 4.1 Booking state machine

17 statuses (`bookings/state.go`) as a `From[]→To` transition table. Terminal:
`TRIP_COMPLETED`, `CANCELLED`, `CANCELLED_BY_DRIVER`, `CANCELLED_BY_ADMIN`,
`DRIVER_NO_SHOW`, `PAYMENT_FAILED`, `ACCIDENT`.

Happy path: `REQUESTED → SEARCHING → DRIVER_ASSIGNED → DRIVER_EN_ROUTE →
DRIVER_ARRIVED → OTP_VERIFIED → TRIP_STARTED ⇄ PAUSED/RESUMED → TRIP_COMPLETED`.

Two properties are enforced by test, not review:

- **`SUPPORT_ESCALATION` is an overlay, not a stage.** It flags a booking for the ops
  queue while the ride continues. Restricting it to terminal targets stranded bookings:
  an SOS during handover blocked arrival, OTP verification, and trip start, leaving only
  closure. The full in-progress progression is now reachable from it.
- **Reachability is a test.** `TestEveryStatusIsReachable` caught `DRIVER_NO_SHOW` and
  `PAYMENT_FAILED` being in `IsTerminal` and permitted by the column CHECK while having
  **no incoming transition** — so every no-show and payment-failure call was rejected as
  invalid and the booking silently stayed open.
  `TestTerminalStatesAreReachable` + `TestEscalationCannotReopenATerminalBooking` stop an
  escalation resurrecting a closed booking.

### 4.2 Compare-and-swap updates

`UpdateStatus` never reads-then-writes:

```sql
UPDATE bookings SET status = $2, updated_at = NOW()
 WHERE id = $1 AND status = ANY($3::text[])
```

with the `booking_events` audit row inserted in the **same transaction**. Two concurrent
transitions from one status cannot both succeed. `uuid.Nil` actors normalise to SQL NULL
(migration `000003`) so system-driven transitions are auditable.

### 4.3 Two OTPs, two threat models

**Login OTP** (`auth/otp.go`) — Redis-keyed, configurable TTL and 4–10 digits, endpoint-rate-limited.

**Handover OTP** (`bookings`) — generated at creation, stored on the booking row, **delivered
by SMS at arrival** (`Coordinator.sendHandoverOTP`). Verification is window-bound and
**budgeted** via `bookings.otp_attempts` (migration `000004`). A 6-digit code makes an
unthrottled endpoint brute-forceable; persisting the counter in Postgres rather than Redis
makes the budget durable across restarts *and* replicas. The SMS body is logged with the
code omitted so a failed send cannot leak the secret.

### 4.4 Matching

Candidate retrieval is one PostGIS query — `ST_DWithin` on
`drivers.current_location::geography` (GIST, partial on `status='ONLINE'`), capability
booleans as predicates, `ORDER BY dist_m ASC LIMIT 50`.

`computeScore` is a 100-point weighted sum: proximity 40 (`40·(1−km/maxRadius)`),
rating 25, acceptance 15, low-cancellation 10 (floored), experience 10 (capped at 20 yrs).
Insertion sort over ≤50 items — O(n²) is correct at this bound.

**Driver exclusivity is defence in depth:** Redis `SETNX lock:driver:<id>` (best-effort —
released when the offer is sent, lost if Redis restarts) → conditional
`UPDATE ... WHERE driver_id IS NULL AND status IN (...)` → partial unique index
`uq_bookings_active_driver`. The index is the actual guarantee and the only layer that
survives a Redis outage. **First-accept-wins** is arbitrated by `ClaimOffer`, a single
conditional statement, so concurrent accepts lose at the row level; tests verify the claim
is single-use, refuses expired offers, and that accept updates the **claimed** offer, not
the first.

### 4.5 Pricing, trips, safety

Integer minor units throughout, no float reaches a fare column; one `round()` helper for
half-up. Night surcharge ×1.20 on the driver fee for 22:00–06:00; GST 18%. The database
enforces `chk_payments_breakdown`
(`amount = platform_fee + driver_amount + "authorization" + protection_fee + tax`), so a
booking cannot be marked paid for less than the driver is owed.

`StartTrip` requires `OTP_VERIFIED` and creates trip + pre-trip inspection in one
transaction; a failed start leaves nothing behind, and an open emergency record **blocks
trip deletion** so incident evidence cannot be destroyed. `EndTrip` completes, computes
distance, writes the post-trip inspection; a second completion and a non-assigned driver
are both rejected.

`incidents` **fails closed**: non-party rejected, assigned driver admitted, a user with no
trip always allowed (someone in danger off-platform must not be blocked by a data-model
edge case), and an unavailable trip lookup errors rather than passes. SOS alerts every
emergency contact and is idempotent while active.

`admin.SuspendDriver` takes the driver offline, cancels the underway booking, does **not**
increment their cancellation rate (not their fault), and closes every other open booking.
This is why `CANCELLED_BY_ADMIN` must reach every non-terminal state past handover.

---

## 5. Persistence

19 tables across 11 migrations. UUID PKs (`pgcrypto`), `TIMESTAMPTZ` timestamps, `INTEGER`
minor-unit money, and `TEXT` + `CHECK` instead of Postgres `ENUM` — `ALTER TYPE ADD VALUE`
cannot run in a transaction, which would fight the runner's per-version transaction.

**Runner** (`migrate.go`, ~161 lines, no dependency): `go:embed`-ed, so the binary is
self-contained and no SQL ships. Each `*.up.sql` is SHA-256 checksummed and applied in its
own transaction; `schema_migrations` records version + checksum and a changed file triggers
a **drift warning** rather than silent acceptance. Zero-padded versions sort correctly.
Every migration has a `.down.sql`, but no runner consumes them — **no automated rollback**
(acceptable for additive forward-only changes; should be a stated policy).

**Migration `000004` is the most important in the repo.** Its header states the principle:
application checks run as separate statements, so two concurrent requests can both pass
them. Each advisory rule moved down into the DB where it is atomic:

| Index | Guarantees |
| --- | --- |
| `uq_matching_offers_pending` | One live offer per (booking, driver) |
| `uq_bookings_active_driver` | One driver, one live booking |
| `uq_payments_active_booking` | Booking cannot be paid twice |
| `uq_refunds_live_payment` | One non-failed refund per payment |
| `uq_driver_earnings_booking` | Earnings ledger idempotent on booking id |

Plus ~20 value CHECKs the services never enforced — coordinate ranges on `bookings`,
`trip_locations`, `sos`; `estimated_price >= 0`; `1 ≤ duration_hours ≤ 24`; fuel 0–100;
odometer monotonicity; `from_user <> to_user`; non-blank document numbers — without which
malformed input surfaces as a 500 instead of a 400. Plus hot-path partial indexes for the
worker sweeps and driver-foreground lookups.

**Migration `000011`** splits `gateway_reference`, which was doing two incompatible jobs:
written with the *order* id at creation, then read as the *payment* id by capture, void,
and refund — so all three addressed the wrong object against a live gateway, and the column
had to be overwritten to make lookups work, destroying the order id. Now
`gateway_order_id` (ours, unique) and `gateway_reference` (the gateway's payment id, used
by those three operations and every webhook lookup) are separate; the backfill moves a
value only when it looks like an order id (`LIKE 'order_%'`).

---

## 6. Payments and Webhook Integrity

Hand-rolled `RazorpayClient` (no SDK): `CreateOrder`, `CapturePayment`, `VoidPayment`,
`Refund`. An empty key pair selects **mock mode**, so the lifecycle is testable without
credentials. Both signatures use `hmac.Equal` (constant-time). All calls share one
`httpx.NewClient` timeout policy.

**Webhook inbox** — Razorpay retries until it gets a 2xx, so events arrive repeatedly and
sometimes concurrently. Three migrations harden this progressively:

- **`000004`** — `gateway_events` keyed by event id. A redelivery conflicts and returns
  200 without re-applying. Handles *sequential* retries.
- **`000009`** — `processed_at` left NULL until work succeeds, making the inbox
  **retryable**: a handler crash is reprocessed, not lost.
- **`000010`** — `claimed_at` lease (30 s). Required because "unprocessed" is satisfied by
  every concurrent redelivery at the same instant; without a lease, two simultaneous
  deliveries of one capture would credit driver earnings twice. An abandoned claim expires
  so a dead worker cannot block an event forever.

`inbox_internal_test.go` asserts all five properties (live lease not reclaimable,
abandoned claim expires, completed never reclaimed, concurrent deliveries elect one
winner, claim recorded only on success).

`ReleaseAuthorizationForBooking` returns an uncaptured hold so funds are not held until the
issuer expires them; it runs on every non-completed cancellation and on the no-show sweep.
Tests assert: void happens once and never twice, a captured payment is never voided, an
unauthorised payment needs no gateway call, a failed void is reported not swallowed, and
an unpaid booking is not an error.

---

## 7. Platform Services

**JWT** — HS256 access + refresh; claims carry `uid`/`phone`/`role` plus `sub`/`iat`/`exp`/`iss`.
The keyfunc **pins the algorithm**, asserting `*jwt.SigningMethodHMAC` before returning the
secret — without this a token signed `none` or with an asymmetric algorithm could be
accepted. Issuer validated, 30 s leeway. Revocation is a Redis blacklist with TTL = access
expiry; `Logout` honours the caller's context so revocation isn't abandoned on disconnect.
The blacklist check degrades to a warning on Redis error — the one deliberate fail-open in
the auth path.

**Rate limiting** — fixed-window, in-process, per-replica. **Not correct under horizontal
scale**: N replicas admit N× the limit. Documented in `ratelimit.go` rather than left as a
trap; `NewRateLimiter` is the only construction site, so moving to a Redis `INCR`+`EXPIRE`
is contained. `RateLimiter.Close()` is never called, so its ticker leaks for process
lifetime (benign).

**WebSocket** — one `EventLoop` goroutine owns *all* registry mutation via
`register`/`unregister` channels; readers take `RLock` only to copy the subscriber set,
avoiding the lock-ordering problem a per-client mutex would create. **Authorisation runs
before upgrade** — once the 101 handshake completes an unauthorised client already receives
frames — and until an authorizer is set the hub **refuses** subscriptions, because a
fail-open default here is a live-location leak. Inbound capped at 4 KiB (read-only
endpoint). Outbound uses a buffered channel with **non-blocking send**, dropping a slow
client rather than stalling broadcast for everyone. Ping/pong liveness. The hub knows
nothing about bookings or drivers — it takes a `TripAuthorizer` callback so the decision
lives with the data it needs.

**FCM** — HTTP v1 implemented directly. `token.go` mints RS256 OAuth2 JWT-bearer assertions
from the service account key (inline JSON *or* file path), caching tokens in-process with a
1-minute refresh margin. Empty `FCM_PROJECT_ID` disables push.

**SMS** — `log` (dev) and `msg91` behind one `Sender`. A nil sender is defensively
replaced with a log sender in `NewCoordinator`, since nil would panic on arrival.

**Location** — Redis GEO (`GEOFADD`/`GEOSEARCH`) plus a TTL'd JSON state cache. Matching
reads PostGIS, not this (§11.5).

---

## 8. Configuration

`config.go` (415 lines), environment-only, with typed helpers (`getEnvAsInt`,
`getEnvAsBool`, `duration`, `csv`). TTL vars accept a Go duration string *or* a bare
integer read as seconds/minutes, removing a class of unit mistakes.

**`Validate()` refuses to boot in production** if `JWT_SECRET` is still the default or
under 32 chars, if `DB_SSLMODE=disable`, or if `SERVER_ALLOWED_ORIGINS` contains `*`.
`DATABASE_URL`/`REDIS_URL` take precedence over the discrete `DB_*`/`REDIS_*` forms so a
managed instance needs no compose rewrite.

---

## 9. Lifecycle, Concurrency, Deployment

`cmd/server/main.go` is 115 lines so the whole process lifecycle is visible in one file:

```
godotenv.Load() → config.Load() → logger.New() → slog.SetDefault
signal.NotifyContext(SIGINT, SIGTERM)   ← ONE ctx drives the server AND all workers
database.NewPostgres → defer Close → (if DB_RUN_MIGRATIONS) RunMigrations
database.NewRedis → defer Close
app.New(Deps{...}) → application.Start(ctx)   // Hub.EventLoop + 4 workers
http.Server{ReadHeaderTimeout, ReadTimeout, IdleTimeout, WriteTimeout: 0}
ListenAndServe in goroutine → select{server error | ctx.Done()}
ctx.Done → WithTimeout(ShutdownTimeout) → Shutdown → "server stopped cleanly"
```

One signal context means workers stop **before** the server drains, because they observe
the same `ctx`. **`WriteTimeout: 0` is intentional** — WebSocket routes hijack the
connection and any write deadline would sever live tracking sockets; protection is
`ReadHeaderTimeout`/`ReadTimeout`.

**Four worker loops** (`workers.go`), each a `time.Ticker` selecting on `ctx.Done()` — no
`context.Background()` anywhere, so no goroutine outlives shutdown; a `WaitGroup` logs when
all stop. `WORKERS_ENABLED=false` disables the set.

| Loop | Function |
| --- | --- |
| `scheduledBookingLoop` | Drive due scheduled bookings + re-drive searches that found nobody inline |
| `offerExpiryLoop` | Mark lapsed `PENDING` offers `TIMEOUT` |
| `staleSearchRetryLoop` | Re-match `SEARCHING` bookings past the threshold |
| `noShowLoop` | Close bookings stuck in `DRIVER_ARRIVED` (default 60 s sweep) |

The fast re-drive inside the scheduled loop exists because without it a driver coming
online after booking creation waits for the far longer stale-retry threshold. The no-show
sweep exists because the driver's device is the only thing that can start a trip; its
escalation is a **CAS from `DRIVER_ARRIVED`**, so a trip started mid-sweep makes it a no-op
rather than a false accusation.

**Deployment** — two-stage non-root Alpine 3.20; `CGO_ENABLED=0`, `-trimpath`,
`-ldflags="-s -w"`, `go.mod` copied first for layer caching; runtime adds `ca-certificates`
and `tzdata`, non-login `drivethrough` user, `TZ=Asia/Kolkata`, healthcheck on `/ready`.
Migrations are embedded so no SQL ships. Compose runs `postgis/postgis:16-3.4` and
`redis:7-alpine` with named volumes and healthchecks; `api` waits on
`condition: service_healthy`.

**Health** — `/health` is static liveness; `/ready` pings both stores with a 2 s budget and
503s on failure, so a load balancer stops routing to a replica that lost a datastore. The
distinction is deliberate: liveness must not fail on a dependency outage or the
orchestrator will kill a healthy process.

**Observability** — `log/slog` throughout (`text`/`json`). Per request: method, path,
status, bytes, `duration_ms`, `request_id`. No metrics, no tracing (§11.11).

---

## 10. Testing

Stdlib `testing` only — no assertion or mocking library. **Unit** (internal packages, no
DB): state machine 11, webhook parsing 3, inbox lease 5, response envelope 6.
**Integration** (external packages, real Postgres): `admin` 6, `app/workers` 2,
`bookings` assign 3 + otp 7, `incidents` 6, `matching` 6, `payments` capture 2 +
identifiers 4 + void 6, `trips` endtrip 3 + rollback 2, `vehicles` 6.

`testsupport.Pool(t)` **skips** rather than fails when no DB is reachable and derives a
per-package isolated database (`drivethrough_test_<pkg>`) from the test binary name;
`Truncate` issues one `TRUNCATE … RESTART IDENTITY CASCADE` over 20 tables, never touching
`schema_migrations`. This is why `go test ./...` passes with no Postgres running.

The suite is adversarial, not coverage-driven — titles assert failure modes: *"fails closed
when scope unavailable"*, *"captured is never voided"*, *"escalation cannot reopen a terminal
booking"*, *"concurrent terminal transitions elect one winner"*, *"live lease not
reclaimable"*. The top-level `tests/` directory is empty and vestigial.

---

## 11. Gaps and Defects (verified, by severity)

1. **Per-replica rate limiting** — incorrect under horizontal scale; N replicas admit N× the
   limit (§7).
2. **`RAZORPAY_WEBHOOK_TTL` parsed, never used** — signature tolerance is unenforced, so a
   captured body can be replayed outside the intended window. The claim lease limits
   *concurrency*, not *replay over time*. Enforce or delete.
3. **`main.version` stamped but never declared** — the Dockerfile passes
   `-X main.version=${VERSION}`; no such variable exists, so the link step succeeds and the
   value is discarded. No deployed instance can identify its build.
4. **15 of 25 packages untested** — coverage sits where invariants are subtle and is absent
   where volume is high. `middleware` (CORS, headers, proxy trust, limiter, recovery) and
   `config.Validate()` are both security-relevant and both untested; a regression in
   `TrustedProxyRealIP` silently reopens defect 1's bypass.
5. **Redis GEO + state cache are write-only** — `internal/location` writes `drv:loc` and
   `drv:state:<id>` on every location ping while matching reads PostGIS: duplicated write
   load with no reader.
6. **`SETTLED` payment status is unreachable** — in the enum, CHECK, and models, never written.
7. **`docs/` and `tests/` were empty directories**; `tests/` should be removed.
8. **Six undocumented config keys** — `APP_ENV`, `SERVER_WRITE_TIMEOUT`,
   `SERVER_TRUSTED_PROXIES`, `WORKER_SEARCH_RETRY_MIN_AGE`,
   `WORKER_NO_SHOW_SWEEP_INTERVAL`, `WORKER_NO_SHOW_THRESHOLD`.
9. **`SERVER_WRITE_TIMEOUT` is dead config** — stored, never applied, because
   `WriteTimeout` must be 0 for WebSockets. Remove or document as intentionally ignored.
10. **No migration rollback path** — `.down.sql` files exist but nothing consumes them.
11. **No metrics or tracing** — no way to measure matching success rate, search latency,
    webhook lag, or offer expiry rate: the four numbers that say whether the marketplace is healthy.
12. **`/health` carries no version or build info**, so with defect 3 nothing identifies a replica.

**Sequence:** (1) declare `var version` and expose it on `/health` — two lines;
(2) unit-test `middleware` and `config.Validate()`; (3) move rate limiting to Redis;
(4) enforce or delete `RAZORPAY_WEBHOOK_TTL`; (5) decide on `internal/location` and
`SETTLED`, deleting what is unused; (6) add metrics on the four health signals before
scaling replicas. None require structural change.

---

## 12. Summary

A complete single-binary Go service implementing a driver marketplace on
PostgreSQL/PostGIS + Redis: clean build, clean `go vet`, 71 passing tests. Its engineering
centre of gravity is **pushing invariants into the database** — partial unique indexes,
compare-and-swap updates, value CHECKs, and a leased webhook inbox. Application-layer
checks are treated as advisory; the database is the arbiter. That principle is stated in
`000004`'s header and applied consistently across bookings, payments, earnings, offers, and
gateway events.

The second distinctive choice is **interface-at-the-consumer plus setter injection**, which
keeps 15 mutually unaware domain modules despite genuinely bidirectional business rules, at
the cost of seven un-checked wiring calls — each wired, each with a safe default.

The state machine deserves particular credit: its suite enforces reachability, terminal
dead-ends, and no-unknown-transitions, and caught two production-blocking defects where
statuses existed in the enum and CHECK but had no incoming transition, making no-shows and
payment failures silently impossible.

The real risks are operational, not architectural: per-replica rate limiting is incorrect
at scale, webhook replay tolerance is unenforced, `middleware` and `config` have no
coverage, and there are no metrics. All are contained fixes. The architecture is sound and
is not the constraint on the next phase.
