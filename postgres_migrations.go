package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/postgres/*.sql
var postgresMigrationFiles embed.FS

type postgresMigration struct {
	Version  int64
	Name     string
	Filename string
	SQL      string
	Checksum string
}

func runPostgresMigrations(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := ensurePostgresMigrationTable(ctx, db); err != nil {
		return err
	}

	migrations, err := loadPostgresMigrations()
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if err := applyPostgresMigration(ctx, db, migration); err != nil {
			return err
		}
	}

	return nil
}

func ensurePostgresMigrationTable(ctx context.Context, db *sql.DB) error {
	const query = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version BIGINT PRIMARY KEY,
	name TEXT NOT NULL,
	checksum TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create PostgreSQL schema_migrations table: %w", err)
	}

	return nil
}

func loadPostgresMigrations() ([]postgresMigration, error) {
	entries, err := fs.ReadDir(postgresMigrationFiles, "migrations/postgres")
	if err != nil {
		return nil, fmt.Errorf("read embedded PostgreSQL migrations: %w", err)
	}

	migrations := make([]postgresMigration, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, name, err := parsePostgresMigrationFilename(entry.Name())
		if err != nil {
			return nil, err
		}

		filename := filepath.ToSlash(
			filepath.Join("migrations/postgres", entry.Name()),
		)

		body, err := postgresMigrationFiles.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf(
				"read PostgreSQL migration %s: %w",
				entry.Name(),
				err,
			)
		}

		sum := sha256.Sum256(body)

		migrations = append(migrations, postgresMigration{
			Version:  version,
			Name:     name,
			Filename: entry.Name(),
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for i := 1; i < len(migrations); i++ {
		if migrations[i-1].Version == migrations[i].Version {
			return nil, fmt.Errorf(
				"duplicate PostgreSQL migration version %d",
				migrations[i].Version,
			)
		}
	}

	return migrations, nil
}

func parsePostgresMigrationFilename(filename string) (int64, string, error) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	parts := strings.SplitN(base, "_", 2)

	if len(parts) != 2 {
		return 0, "", fmt.Errorf(
			"invalid PostgreSQL migration filename %q",
			filename,
		)
	}

	version, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf(
			"invalid PostgreSQL migration version in %q: %w",
			filename,
			err,
		)
	}

	name := strings.TrimSpace(parts[1])
	if name == "" {
		return 0, "", fmt.Errorf(
			"PostgreSQL migration %q has an empty name",
			filename,
		)
	}

	return version, name, nil
}

func applyPostgresMigration(
	ctx context.Context,
	db *sql.DB,
	migration postgresMigration,
) error {
	var existingChecksum string

	err := db.QueryRowContext(
		ctx,
		`
SELECT checksum
FROM schema_migrations
WHERE version = $1
`,
		migration.Version,
	).Scan(&existingChecksum)

	switch {
	case err == nil:
		if existingChecksum != migration.Checksum {
			return fmt.Errorf(
				"PostgreSQL migration %s checksum mismatch",
				migration.Filename,
			)
		}

		return nil

	case err != sql.ErrNoRows:
		return fmt.Errorf(
			"inspect PostgreSQL migration %s: %w",
			migration.Filename,
			err,
		)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf(
			"begin PostgreSQL migration %s: %w",
			migration.Filename,
			err,
		)
	}

	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf(
			"apply PostgreSQL migration %s: %w",
			migration.Filename,
			err,
		)
	}

	if _, err := tx.ExecContext(
		ctx,
		`
INSERT INTO schema_migrations (
	version,
	name,
	checksum
)
VALUES ($1, $2, $3)
`,
		migration.Version,
		migration.Name,
		migration.Checksum,
	); err != nil {
		return fmt.Errorf(
			"record PostgreSQL migration %s: %w",
			migration.Filename,
			err,
		)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit PostgreSQL migration %s: %w",
			migration.Filename,
			err,
		)
	}

	return nil
}
