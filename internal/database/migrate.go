// Package database owns connection lifecycle and schema versioning.
package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationFS embed.FS

const migrationsDir = "migrations"

// Migration is a single versioned schema change loaded from the embedded
// migrations directory.
type Migration struct {
	Version  string
	SQL      string
	Checksum string
}

// LoadMigrations reads every *.up.sql file from the embedded filesystem and
// returns them ordered by version. The binary is self-contained, so schema
// changes ship with the code and need no bind-mounted SQL directory.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var out []Migration
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		data, err := migrationFS.ReadFile(migrationsDir + "/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}

		version := strings.TrimSuffix(name, ".up.sql")
		sum := sha256.Sum256(data)
		out = append(out, Migration{
			Version:  version,
			SQL:      string(data),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations found in %s", migrationsDir)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// RunMigrations applies every pending migration inside a single transaction
// per version, recording the version and its checksum so an already-applied
// file that later changes is reported as drift rather than silently ignored.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			checksum   TEXT NOT NULL DEFAULT '',
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, pool)
	if err != nil {
		return err
	}

	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if existing, ok := applied[m.Version]; ok {
			if existing != "" && existing != m.Checksum {
				slog.Warn("migration checksum drift", "version", m.Version)
			}
			continue
		}

		if err := applyMigration(ctx, pool, m); err != nil {
			return err
		}
	}

	return nil
}

func appliedMigrations(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	rows, err := pool.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		out[version] = checksum
	}
	return out, rows.Err()
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, m Migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.Version, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)
		 ON CONFLICT (version) DO UPDATE SET checksum = EXCLUDED.checksum`,
		m.Version, m.Checksum,
	); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	slog.Info("applied migration", "version", m.Version)
	return nil
}

// EnsureSchema verifies the database is reachable and migrated.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var ok bool
	if err := pool.QueryRow(ctx, `SELECT true`).Scan(&ok); err != nil {
		return fmt.Errorf("schema check failed: %w", err)
	}
	return nil
}
