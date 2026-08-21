package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const postgresMigrationHelperEnv = "GO_WANT_POSTGRES_MIGRATION_HELPER"

func TestPostgresMigrationDiscoveryIncludesMCP(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration

	for i := range migrations {
		if migrations[i].Version == 6 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000006_mcp.sql")
	}

	if found.Filename != "000006_mcp.sql" {
		t.Fatalf(
			"migration 6 filename = %q, want %q",
			found.Filename,
			"000006_mcp.sql",
		)
	}

	if found.Name != "mcp" {
		t.Fatalf(
			"migration 6 name = %q, want %q",
			found.Name,
			"mcp",
		)
	}

	if found.Checksum == "" {
		t.Fatal("migration 6 checksum must not be empty")
	}
}

func TestPostgresMCPMigrationAppliesCleanly(t *testing.T) {
	runPostgresMigrationHelper(t, "apply_all")
}

func TestPostgresMCPMigrationUpgradeFrom000005(t *testing.T) {
	runPostgresMigrationHelper(t, "upgrade_from_000005")
}

func runPostgresMigrationHelper(t *testing.T, action string) {
	t.Helper()

	if _, err := exec.LookPath("pg_virtualenv"); err != nil {
		t.Skip("pg_virtualenv is not available")
	}

	cmd := exec.Command(
		"pg_virtualenv",
		os.Args[0],
		"-test.run=^TestPostgresMigrationHelperProcess$",
	)

	cmd.Env = append(
		os.Environ(),
		postgresMigrationHelperEnv+"=1",
		"POSTGRES_MIGRATION_HELPER_ACTION="+action,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"PostgreSQL migration helper %s failed: %v\n%s",
			action,
			err,
			output,
		)
	}
}

func TestPostgresMigrationHelperProcess(t *testing.T) {
	if os.Getenv(postgresMigrationHelperEnv) != "1" {
		return
	}

	action := os.Getenv("POSTGRES_MIGRATION_HELPER_ACTION")

	if err := runPostgresMigrationHelperAction(action); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

func runPostgresMigrationHelperAction(action string) error {
	db, err := sql.Open("pgx", "")
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return err
	}

	ctx := context.Background()

	switch action {
	case "apply_all":
		if err := runPostgresMigrations(db); err != nil {
			return err
		}

	case "upgrade_from_000005":
		migrations, err := loadPostgresMigrations()
		if err != nil {
			return err
		}

		if err := ensurePostgresMigrationTable(ctx, db); err != nil {
			return err
		}

		for _, migration := range migrations {
			if migration.Version > 5 {
				continue
			}

			if err := applyPostgresMigration(
				ctx,
				db,
				migration,
			); err != nil {
				return err
			}
		}

		// This now represents a real database already migrated through 000005.
		// Running the normal production migration engine must apply 000006 only.
		if err := runPostgresMigrations(db); err != nil {
			return err
		}

	default:
		return fmt.Errorf(
			"unknown PostgreSQL migration helper action %q",
			action,
		)
	}

	for _, relation := range []string{
		"mcp_customers",
		"mcp_pricing_bands",
		"mcp_monthly_plans",
		"mcp_plan_rules",
		"mcp_plan_sessions",
		"mcp_payment_collections",
	} {
		if err := postgresRelationMustExist(db, relation); err != nil {
			return err
		}
	}

	var appliedCount int

	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 6
	`).Scan(&appliedCount); err != nil {
		return err
	}

	if appliedCount != 1 {
		return fmt.Errorf(
			"expected migration 000006 to be recorded once, got %d",
			appliedCount,
		)
	}

	var migrationName string

	if err := db.QueryRow(`
		SELECT name
		FROM schema_migrations
		WHERE version = 6
	`).Scan(&migrationName); err != nil {
		return err
	}

	if migrationName != "mcp" {
		return fmt.Errorf(
			"migration 000006 recorded as %q, want mcp",
			migrationName,
		)
	}

	return nil
}

func postgresRelationMustExist(
	db *sql.DB,
	relation string,
) error {
	var actual sql.NullString

	if err := db.QueryRow(
		`SELECT to_regclass($1)::text`,
		"public."+relation,
	).Scan(&actual); err != nil {
		return err
	}

	if !actual.Valid || actual.String == "" {
		return fmt.Errorf(
			"expected PostgreSQL relation %s to exist",
			relation,
		)
	}

	return nil
}
