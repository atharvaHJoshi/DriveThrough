-- 000001_init.up.sql -- Initial schema for the driver marketplace
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    phone       TEXT NOT NULL UNIQUE,
    email       TEXT UNIQUE,
    role        TEXT NOT NULL DEFAULT 'CUSTOMER'
                CHECK (role IN ('CUSTOMER', 'DRIVER', 'ADMIN')),
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS drivers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL UNIQUE REFERENCES users(id),
    status              TEXT NOT NULL DEFAULT 'OFFLINE'
                        CHECK (status IN ('ONLINE', 'OFFLINE', 'BUSY', 'INACTIVE')),
    verification_status TEXT NOT NULL DEFAULT 'PENDING'
                        CHECK (verification_status IN ('PENDING', 'SUBMITTED', 'APPROVED', 'REJECTED', 'SUSPENDED')),
    rating              NUMERIC(3,2) NOT NULL DEFAULT 0,
    total_trips         INTEGER NOT NULL DEFAULT 0,
    total_ratings       INTEGER NOT NULL DEFAULT 0,
    experience_years    INTEGER NOT NULL DEFAULT 0,
    acceptance_rate     NUMERIC(5,2) NOT NULL DEFAULT 0,
    cancellation_rate   NUMERIC(5,2) NOT NULL DEFAULT 0,
    can_drive_manual    BOOLEAN NOT NULL DEFAULT FALSE,
    can_drive_automatic BOOLEAN NOT NULL DEFAULT FALSE,
    can_drive_hatchback BOOLEAN NOT NULL DEFAULT FALSE,
    can_drive_sedan     BOOLEAN NOT NULL DEFAULT FALSE,
    can_drive_suv       BOOLEAN NOT NULL DEFAULT FALSE,
    can_drive_luxury    BOOLEAN NOT NULL DEFAULT FALSE,
    current_location    geography(POINT, 4326),
    earnings_total      INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_drivers_status ON drivers(status) WHERE status = 'ONLINE';
CREATE INDEX IF NOT EXISTS idx_drivers_location ON drivers USING GIST (current_location) WHERE status = 'ONLINE';

CREATE TABLE IF NOT EXISTS driver_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id       UUID NOT NULL REFERENCES drivers(id),
    type            TEXT NOT NULL
                    CHECK (type IN ('AADHAAR', 'PAN', 'DRIVING_LICENCE', 'ADDRESS_PROOF')),
    document_number TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING'
                    CHECK (status IN ('PENDING', 'VERIFIED', 'REJECTED')),
    expiry_date     DATE,
    document_url    TEXT NOT NULL DEFAULT '',
    verified_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (driver_id, type)
);

CREATE TABLE IF NOT EXISTS vehicles (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id),
    registration_number TEXT NOT NULL UNIQUE,
    make                TEXT NOT NULL,
    model               TEXT NOT NULL,
    year                INTEGER NOT NULL,
    transmission        TEXT NOT NULL CHECK (transmission IN ('MANUAL', 'AUTOMATIC')),
    fuel_type           TEXT NOT NULL CHECK (fuel_type IN ('PETROL', 'DIESEL', 'CNG', 'ELECTRIC', 'HYBRID')),
    class               TEXT NOT NULL CHECK (class IN ('HATCHBACK', 'SEDAN', 'SUV', 'LUXURY', 'MUV')),
    is_default          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vehicles_user ON vehicles(user_id);

CREATE TABLE IF NOT EXISTS bookings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL REFERENCES users(id),
    driver_id       UUID REFERENCES drivers(id),
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id),
    booking_type    TEXT NOT NULL CHECK (booking_type IN ('HOURLY', 'TRIP', 'ROUND_TRIP')),
    duration_hours  INTEGER NOT NULL DEFAULT 1,
    pickup_location TEXT NOT NULL,
    pickup_lat      DOUBLE PRECISION NOT NULL,
    pickup_lng      DOUBLE PRECISION NOT NULL,
    destination     TEXT,
    dest_lat        DOUBLE PRECISION,
    dest_lng        DOUBLE PRECISION,
    scheduled_at    TIMESTAMPTZ,
    status          TEXT NOT NULL DEFAULT 'REQUESTED'
                    CHECK (status IN (
                        'REQUESTED', 'SEARCHING', 'DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE',
                        'DRIVER_ARRIVED', 'OTP_VERIFIED', 'TRIP_STARTED', 'TRIP_PAUSED',
                        'TRIP_RESUMED', 'TRIP_COMPLETED', 'CANCELLED', 'CANCELLED_BY_DRIVER',
                        'DRIVER_NO_SHOW', 'PAYMENT_FAILED', 'SUPPORT_ESCALATION', 'ACCIDENT'
                    )),
    otp_code        TEXT NOT NULL DEFAULT '',
    estimated_price INTEGER NOT NULL DEFAULT 0,
    final_price     INTEGER,
    driver_fee      INTEGER NOT NULL DEFAULT 0,
    platform_fee    INTEGER NOT NULL DEFAULT 0,
    protection_fee  INTEGER NOT NULL DEFAULT 0,
    tax             INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bookings_customer ON bookings(customer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bookings_driver ON bookings(driver_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bookings_status ON bookings(status);
CREATE INDEX IF NOT EXISTS idx_bookings_pickup ON bookings USING GIST (
    ST_SetSRID(ST_MakePoint(pickup_lng, pickup_lat), 4326)
);

CREATE TABLE IF NOT EXISTS booking_events (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id UUID NOT NULL REFERENCES bookings(id),
    status     TEXT NOT NULL,
    notes      TEXT,
    actor_id   UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_booking_events_booking ON booking_events(booking_id, created_at DESC);

CREATE TABLE IF NOT EXISTS trips (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id     UUID NOT NULL UNIQUE REFERENCES bookings(id),
    driver_id      UUID NOT NULL REFERENCES drivers(id),
    vehicle_id     UUID NOT NULL REFERENCES vehicles(id),
    started_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at       TIMESTAMPTZ,
    start_odometer INTEGER NOT NULL DEFAULT 0,
    end_odometer   INTEGER,
    start_fuel     INTEGER NOT NULL DEFAULT 0,
    end_fuel       INTEGER,
    total_distance DOUBLE PRECISION NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'PAUSED', 'COMPLETED', 'CANCELLED')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS trip_locations (
    id         BIGSERIAL PRIMARY KEY,
    trip_id    UUID NOT NULL REFERENCES trips(id),
    latitude   DOUBLE PRECISION NOT NULL,
    longitude  DOUBLE PRECISION NOT NULL,
    accuracy   DOUBLE PRECISION,
    heading    DOUBLE PRECISION,
    speed      DOUBLE PRECISION,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_trip_locations ON trip_locations(trip_id, recorded_at);

CREATE TABLE IF NOT EXISTS vehicle_inspections (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id       UUID NOT NULL REFERENCES trips(id),
    type          TEXT NOT NULL CHECK (type IN ('PRE_TRIP', 'POST_TRIP')),
    odometer      INTEGER NOT NULL,
    fuel_level    INTEGER NOT NULL,
    photos        TEXT[] NOT NULL DEFAULT '{}',
    damage_notes  TEXT,
    has_damage    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS payments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id        UUID NOT NULL REFERENCES bookings(id),
    customer_id       UUID NOT NULL REFERENCES users(id),
    amount            INTEGER NOT NULL,
    platform_fee      INTEGER NOT NULL DEFAULT 0,
    driver_amount     INTEGER NOT NULL DEFAULT 0,
    -- "authorization" is reserved in Postgres, hence the quoted identifier.
    "authorization"   INTEGER NOT NULL DEFAULT 0,
    protection_fee    INTEGER NOT NULL DEFAULT 0,
    tax               INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'INITIATED'
                      CHECK (status IN ('INITIATED', 'AUTHORIZED', 'CAPTURED', 'FAILED', 'REFUNDED', 'SETTLED')),
    method            TEXT CHECK (method IN ('CARD', 'UPI', 'NETBANKING', 'WALLET')),
    gateway_reference TEXT NOT NULL DEFAULT '',
    metadata          JSONB NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payments_booking ON payments(booking_id);
CREATE INDEX IF NOT EXISTS idx_payments_customer ON payments(customer_id, created_at DESC);

CREATE TABLE IF NOT EXISTS refunds (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id        UUID NOT NULL REFERENCES payments(id),
    booking_id        UUID NOT NULL REFERENCES bookings(id),
    amount            INTEGER NOT NULL,
    status            TEXT NOT NULL DEFAULT 'PENDING',
    gateway_reference TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ratings (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id UUID NOT NULL REFERENCES bookings(id),
    from_user  UUID NOT NULL REFERENCES users(id),
    to_user    UUID NOT NULL REFERENCES users(id),
    rating     INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment    TEXT,
    tags       TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (booking_id, from_user)
);

CREATE TABLE IF NOT EXISTS driver_earnings (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id  UUID NOT NULL REFERENCES drivers(id),
    booking_id UUID NOT NULL REFERENCES bookings(id),
    amount     INTEGER NOT NULL,
    status     TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PAID', 'FAILED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS notifications (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    type       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    data       JSONB NOT NULL DEFAULT '{}',
    is_read    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS device_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    token      TEXT NOT NULL,
    platform   TEXT NOT NULL DEFAULT 'android',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, token)
);

CREATE TABLE IF NOT EXISTS incidents (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id  UUID REFERENCES bookings(id),
    trip_id     UUID REFERENCES trips(id),
    type        TEXT NOT NULL CHECK (type IN ('ACCIDENT', 'SOS', 'DAMAGE', 'DISPUTE', 'FRAUD', 'OTHER')),
    severity    TEXT NOT NULL DEFAULT 'MEDIUM'
                CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    reported_by UUID NOT NULL REFERENCES users(id),
    latitude    DOUBLE PRECISION,
    longitude   DOUBLE PRECISION,
    description TEXT NOT NULL,
    photos      TEXT[] NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'INVESTIGATING', 'RESOLVED', 'CLOSED')),
    resolution  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);
CREATE INDEX IF NOT EXISTS idx_incidents_trip ON incidents(trip_id) WHERE trip_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS sos (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id),
    trip_id       UUID REFERENCES trips(id),
    latitude      DOUBLE PRECISION NOT NULL,
    longitude     DOUBLE PRECISION NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'EMERGENCY',
    status        TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESPONDED', 'RESOLVED')),
    responded_by  UUID REFERENCES users(id),
    response_note TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at   TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS emergency_contacts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    name       TEXT NOT NULL,
    phone      TEXT NOT NULL,
    relation   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS matching_offers (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id UUID NOT NULL REFERENCES bookings(id),
    driver_id  UUID NOT NULL REFERENCES drivers(id),
    status     TEXT NOT NULL DEFAULT 'PENDING'
               CHECK (status IN ('PENDING', 'ACCEPTED', 'DECLINED', 'EXPIRED', 'TIMEOUT')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    responded_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_matching_offers_booking ON matching_offers(booking_id);
CREATE INDEX IF NOT EXISTS idx_matching_offers_driver ON matching_offers(driver_id, status);