// Package testsupport provides the shared harness for integration tests that
// run against a real PostgreSQL instance.
//
// Integration tests talk to a real database rather than a mock, because most of
// the defects found in this codebase lived in the SQL itself: a scan arity
// mismatch, a missing column, a transaction that silently rolled back, a unique
// index that turned a race into a 500, a foreign key that wrote the wrong
// identifier. A mocked repository cannot catch any of those.
//
// # Isolation
//
// Each test package gets its own database, created on demand and migrated once.
// This matters for correctness, not just tidiness. `go test ./...` runs packages
// concurrently, and they previously shared one database and truncated it per
// test, which deadlocked on the table locks and let one package's TRUNCATE
// silently delete another package's fixtures mid-test.
//
// It also means the suite no longer touches the development database. The
// previous arrangement pointed at the same database the server uses and
// truncated it, so running the tests destroyed whatever was in it.
package testsupport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/database"
)

// Pool returns this package's isolated connection pool, or skips the test when
// no database is reachable. Skipping rather than failing keeps `go test ./...`
// usable on a machine without Docker.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := sharedPool()
	if pool == nil {
		t.Skip("no database available; set DATABASE_URL or start the docker compose stack")
	}
	return pool
}

var (
	sharedOnce sync.Once
	pool       *pgxpool.Pool
)

func sharedPool() *pgxpool.Pool {
	sharedOnce.Do(func() { pool = openIsolated() })
	return pool
}

// openIsolated creates (or reuses) a database dedicated to the current test
// package, migrates it, and returns a pool bound to it.
//
// The database name is derived from the test binary, which `go test` names
// after the package under test, so concurrent packages never collide.
func openIsolated() *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	baseDSN := databaseURL()
	if baseDSN == "" {
		return nil
	}

	name := testDatabaseName()

	// Connect to the maintenance database to create the test database, then to
	// the test database itself. The maintenance database is a good choice for
	// the first hop because it always exists.
	admin, err := pgxpool.New(ctx, withDatabase(baseDSN, "postgres"))
	if err != nil {
		return nil
	}
	defer admin.Close()

	if err := admin.Ping(ctx); err != nil {
		return nil
	}

	// CREATE DATABASE has no IF NOT EXISTS, and a leftover database from a
	// previous run is perfectly reusable, so a duplicate is not an error.
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+quoteIdent(name)); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return nil
	}

	pool, err := pgxpool.New(ctx, withDatabase(baseDSN, name))
	if err != nil {
		return nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil
	}

	// The isolated database is created empty, so it has to be migrated before
	// use. Importing database here is safe: internal/database does not depend on
	// testsupport, and its own tests live in the separate database_test package.
	if err := database.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil
	}

	return pool
}

// testDatabaseName derives a per-package database name from the test binary.
// go test names the binary "<package>.test", so this yields names such as
// "drivethrough_test_bookings".
func testDatabaseName() string {
	bin := filepath.Base(os.Args[0])
	bin = strings.TrimSuffix(bin, ".test")
	bin = strings.TrimSuffix(bin, ".exe")

	clean := make([]rune, 0, len(bin))
	for _, r := range bin {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			clean = append(clean, r)
		case r >= 'A' && r <= 'Z':
			clean = append(clean, r+('a'-'A'))
		default:
			clean = append(clean, '_')
		}
	}
	if len(clean) == 0 {
		clean = []rune("pkg")
	}
	return "drivethrough_test_" + string(clean)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// withDatabase rewrites a connection string to target a different database,
// keeping the host, port, credentials and options intact.
func withDatabase(dsn, dbName string) string {
	// Split off any query parameters so they are not confused with the path.
	base, query := dsn, ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		base, query = dsn[:i], dsn[i:]
	}

	slash := strings.LastIndex(base, "/")
	if slash < 0 {
		return dsn
	}
	return base[:slash+1] + dbName + query
}

// databaseURL resolves the connection string, preferring DATABASE_URL and
// falling back to the discrete DB_* variables so the suite runs against the
// docker compose defaults without extra configuration.
func databaseURL() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}

	host := envOr("DB_HOST", "localhost")
	port := envOr("DB_PORT", "5432")
	user := envOr("DB_USER", "drivethrough")
	pass := envOr("DB_PASSWORD", "drivethrough_secret")
	name := envOr("DB_NAME", "drivethrough")

	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, name)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Truncate clears the tables an integration test writes to.
//
// Order does not matter because CASCADE removes dependents. schema_migrations is
// never truncated: the suite runs against the migrated schema and must not
// invalidate the migration ledger.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()

	if len(tables) == 0 {
		tables = []string{
			// Ordered so CASCADE clears dependants first. Tables the suite does
			// not exercise are omitted so an unrelated schema change does not
			// fail every test in the suite.
			"booking_events", "trips", "trip_locations", "vehicle_inspections",
			"driver_earnings", "ratings", "payments", "refunds", "gateway_events",
			"matching_offers", "notifications", "device_tokens",
			"sos", "incidents", "emergency_contacts",
			"bookings", "vehicles", "driver_documents", "drivers", "users",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A single TRUNCATE ... CASCADE is one catalog operation, so it is far
	// faster than issuing a DELETE per table and still atomic.
	stmt := "TRUNCATE TABLE " + join(tables) + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func join(items []string) string {
	return strings.Join(items, ", ")
}
