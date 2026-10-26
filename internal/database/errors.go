package database

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres surfaces a SQLSTATE class rather than a typed error, so every
// constraint that a handler is expected to translate into a 4xx has to be
// recognised here. Without these helpers a CHECK violation reaches the client
// as a 500 and the caller learns nothing about what to fix.
const (
	CodeUniqueViolation     = "23505"
	CodeForeignKeyViolation = "23503"
	CodeCheckViolation      = "23514"
	CodeNotNullViolation    = "23502"
	CodeSerializationFail   = "40001"
	CodeDeadlockDetected    = "40P01"
)

func pgErr(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

func IsUniqueViolation(err error) bool {
	pgErr := pgErr(err)
	return pgErr != nil && pgErr.Code == CodeUniqueViolation
}

func IsForeignKeyViolation(err error) bool {
	pgErr := pgErr(err)
	return pgErr != nil && pgErr.Code == CodeForeignKeyViolation
}

func IsCheckViolation(err error) bool {
	pgErr := pgErr(err)
	return pgErr != nil && pgErr.Code == CodeCheckViolation
}

// IsRetryable reports whether the failure is a transient conflict that a caller
// may safely retry, such as a serialization failure or a deadlock.
func IsRetryable(err error) bool {
	pgErr := pgErr(err)
	if pgErr == nil {
		return false
	}
	return pgErr.Code == CodeSerializationFail || pgErr.Code == CodeDeadlockDetected
}

// ConstraintName returns the violated constraint, which is the only way to tell
// *which* rule rejected the write when several exist on one table.
func ConstraintName(err error) string {
	pgErr := pgErr(err)
	if pgErr == nil {
		return ""
	}
	return pgErr.ConstraintName
}
