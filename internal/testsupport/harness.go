package testsupport

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UUID is a shorthand for the identifier type used throughout the schema.
type UUID = uuid.UUID

// DB is the per-test handle integration tests use: a shared pool plus a
// truncation helper.
type DB struct {
	Pool *pgxpool.Pool
}

// NewDB returns a database handle, skipping the test when none is reachable.
func NewDB(t *testing.T) *DB {
	t.Helper()
	return &DB{Pool: Pool(t)}
}

// Truncate clears the tables the suite writes to. Call it at the start of each
// test so cases cannot leak state into one another.
func (d *DB) Truncate(t *testing.T) {
	t.Helper()
	Truncate(t, d.Pool)
}

// Exec runs a statement and fails the test on error.
func (d *DB) Exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// MustExec runs an INSERT ... RETURNING and returns the single returned column.
// It keeps fixtures readable, since most setup rows have one generated id.
func (d *DB) MustExec(t *testing.T, sql string, args ...any) pgx.Row {
	t.Helper()
	row := d.Pool.QueryRow(t.Context(), sql, args...)
	return row
}

// UUID runs a statement returning a single uuid and returns it, failing the
// test if the scan fails.
func (d *DB) UUID(t *testing.T, sql string, args ...any) (id UUID) {
	t.Helper()
	var out UUID
	if err := d.MustExec(t, sql, args...).Scan(&out); err != nil {
		t.Fatalf("scan uuid from %q: %v", sql, err)
	}
	return out
}
