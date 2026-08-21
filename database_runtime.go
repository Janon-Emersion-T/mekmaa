package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type DatabaseDriver string

const (
	databaseDriverSQLite   DatabaseDriver = "sqlite"
	databaseDriverPostgres DatabaseDriver = "postgres"
)

type DatabaseConfig struct {
	Driver DatabaseDriver
	URL    string
	Path   string
}

func loadDatabaseConfig() (DatabaseConfig, error) {
	rawDriver := strings.ToLower(strings.TrimSpace(os.Getenv("DB_DRIVER")))

	if rawDriver == "" {
		if strings.TrimSpace(os.Getenv("DATABASE_URL")) != "" {
			rawDriver = string(databaseDriverPostgres)
		} else {
			rawDriver = string(databaseDriverSQLite)
		}
	}

	switch DatabaseDriver(rawDriver) {
	case databaseDriverSQLite:
		return DatabaseConfig{
			Driver: databaseDriverSQLite,
			Path:   strings.TrimSpace(os.Getenv("DB_PATH")),
		}, nil

	case databaseDriverPostgres:
		databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
		if databaseURL == "" {
			return DatabaseConfig{}, errors.New(
				"DATABASE_URL is required when DB_DRIVER=postgres",
			)
		}

		return DatabaseConfig{
			Driver: databaseDriverPostgres,
			URL:    databaseURL,
		}, nil

	default:
		return DatabaseConfig{}, fmt.Errorf(
			"unsupported DB_DRIVER %q: expected sqlite or postgres",
			rawDriver,
		)
	}
}

func openDatabase(config DatabaseConfig) (*sql.DB, error) {
	switch config.Driver {
	case databaseDriverSQLite:
		return openSQLiteDatabase(config.Path)

	case databaseDriverPostgres:
		return openPostgresDatabase(config.URL)

	default:
		return nil, fmt.Errorf(
			"unsupported database driver %q",
			config.Driver,
		)
	}
}

func openSQLiteDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteRuntimeDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	if err := enableSQLiteForeignKeys(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return db, nil
}

func openPostgresDatabase(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres database: %w", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres database: %w", err)
	}

	return db, nil
}

func isPostgresDatabase(config DatabaseConfig) bool {
	return config.Driver == databaseDriverPostgres
}

func isSQLiteDatabase(config DatabaseConfig) bool {
	return config.Driver == databaseDriverSQLite
}

func rebindDatabaseQuery(
	driver DatabaseDriver,
	query string,
) string {
	if driver != databaseDriverPostgres {
		return query
	}

	var b strings.Builder
	b.Grow(len(query) + 16)

	arg := 1
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			b.WriteByte(query[i])
			continue
		}

		b.WriteString(fmt.Sprintf("$%d", arg))
		arg++
	}

	return b.String()
}

func (a *App) dbQuery(query string) string {
	if a == nil {
		return query
	}

	return rebindDatabaseQuery(
		a.runtimeConfig.DBDriver,
		query,
	)
}

func (a *App) queryRowDB(
	query string,
	args ...any,
) *sql.Row {
	return a.db.QueryRow(
		a.dbQuery(query),
		args...,
	)
}

func (a *App) queryDB(
	query string,
	args ...any,
) (*sql.Rows, error) {
	return a.db.Query(
		a.dbQuery(query),
		args...,
	)
}

func (a *App) execDB(
	query string,
	args ...any,
) (sql.Result, error) {
	return a.db.Exec(
		a.dbQuery(query),
		args...,
	)
}

// insertAndReturnID executes an INSERT and returns its generated integer ID
// across both PostgreSQL and SQLite.
//
// PostgreSQL does not support sql.Result.LastInsertId(), so it must use
// INSERT ... RETURNING id instead.
func (a *App) insertAndReturnID(
	query string,
	args ...any,
) (int64, error) {
	driver := strings.ToLower(
		strings.TrimSpace(string(a.runtimeConfig.DBDriver)),
	)

	if driver == "postgres" || driver == "postgresql" {
		query = strings.TrimSpace(query)
		query = strings.TrimSuffix(query, ";")

		var id int64

		err := a.queryRowDB(
			query+" RETURNING id",
			args...,
		).Scan(&id)

		return id, err
	}

	result, err := a.execDB(
		query,
		args...,
	)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// insertAndReturnIDTx executes an INSERT inside an existing transaction
// and returns its generated integer ID across PostgreSQL and SQLite.
//
// PostgreSQL requires INSERT ... RETURNING id, while SQLite supports
// sql.Result.LastInsertId().
func (a *App) insertAndReturnIDTx(
	tx *sql.Tx,
	query string,
	args ...any,
) (int64, error) {
	driver := strings.ToLower(
		strings.TrimSpace(string(a.runtimeConfig.DBDriver)),
	)

	if driver == "postgres" || driver == "postgresql" {
		query = strings.TrimSpace(query)
		query = strings.TrimSuffix(query, ";")

		var id int64
		err := a.queryRowTxDB(
			tx,
			query+" RETURNING id",
			args...,
		).Scan(&id)

		return id, err
	}

	result, err := tx.Exec(
		a.dbQuery(query),
		args...,
	)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

func (a *App) queryRowTxDB(
	tx *sql.Tx,
	query string,
	args ...any,
) *sql.Row {
	return tx.QueryRow(
		a.dbQuery(query),
		args...,
	)
}

func (a *App) queryTxDB(
	tx *sql.Tx,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return tx.Query(
		a.dbQuery(query),
		args...,
	)
}

func (a *App) execTxDB(
	tx *sql.Tx,
	query string,
	args ...any,
) (sql.Result, error) {
	return tx.Exec(
		a.dbQuery(query),
		args...,
	)
}

func (a *App) queryContextDB(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return a.db.QueryContext(
		ctx,
		a.dbQuery(query),
		args...,
	)
}

func (a *App) queryRowContextDB(
	ctx context.Context,
	query string,
	args ...any,
) *sql.Row {
	return a.db.QueryRowContext(
		ctx,
		a.dbQuery(query),
		args...,
	)
}

func (a *App) execContextDB(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	return a.db.ExecContext(
		ctx,
		a.dbQuery(query),
		args...,
	)
}
