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

func TestPostgresMigrationDiscoveryIncludesOneToOneAttendance(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration

	for i := range migrations {
		if migrations[i].Version == 7 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000007_one_to_one_session_attendance.sql")
	}

	if found.Filename != "000007_one_to_one_session_attendance.sql" {
		t.Fatalf(
			"migration 7 filename = %q, want %q",
			found.Filename,
			"000007_one_to_one_session_attendance.sql",
		)
	}

	if found.Name != "one_to_one_session_attendance" {
		t.Fatalf(
			"migration 7 name = %q, want %q",
			found.Name,
			"one_to_one_session_attendance",
		)
	}

	if found.Checksum == "" {
		t.Fatal("migration 7 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesTournaments(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 9 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000009_tournaments.sql")
	}

	if found.Filename != "000009_tournaments.sql" {
		t.Fatalf("migration 9 filename = %q, want %q", found.Filename, "000009_tournaments.sql")
	}

	if found.Name != "tournaments" {
		t.Fatalf("migration 9 name = %q, want %q", found.Name, "tournaments")
	}

	if found.Checksum == "" {
		t.Fatal("migration 9 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesEnrollmentEditCompat(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 11 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000011_enrollment_edit_compat.sql")
	}

	if found.Filename != "000011_enrollment_edit_compat.sql" {
		t.Fatalf("migration 11 filename = %q, want %q", found.Filename, "000011_enrollment_edit_compat.sql")
	}

	if found.Name != "enrollment_edit_compat" {
		t.Fatalf("migration 11 name = %q, want %q", found.Name, "enrollment_edit_compat")
	}

	if found.Checksum == "" {
		t.Fatal("migration 11 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesTrainingProgramGroupDelivery(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 12 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000012_training_program_group_delivery.sql")
	}

	if found.Filename != "000012_training_program_group_delivery.sql" {
		t.Fatalf("migration 12 filename = %q, want %q", found.Filename, "000012_training_program_group_delivery.sql")
	}

	if found.Name != "training_program_group_delivery" {
		t.Fatalf("migration 12 name = %q, want %q", found.Name, "training_program_group_delivery")
	}

	if found.Checksum == "" {
		t.Fatal("migration 12 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesTrainingProgramDivisionNameUniqueness(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 13 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000013_training_program_division_name_uniqueness.sql")
	}

	if found.Filename != "000013_training_program_division_name_uniqueness.sql" {
		t.Fatalf("migration 13 filename = %q, want %q", found.Filename, "000013_training_program_division_name_uniqueness.sql")
	}

	if found.Name != "training_program_division_name_uniqueness" {
		t.Fatalf("migration 13 name = %q, want %q", found.Name, "training_program_division_name_uniqueness")
	}

	if found.Checksum == "" {
		t.Fatal("migration 13 checksum must not be empty")
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
		// Running the normal production migration engine must apply 000006+
		// exactly as production startup would.
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
		"one_to_one_booking_sessions",
		"tournaments",
		"tournament_sponsorships",
		"tournament_official_payments",
		"tournament_expenses",
	} {
		if err := postgresRelationMustExist(db, relation); err != nil {
			return err
		}
	}

	for _, version := range []int{6, 7, 9} {
		var appliedCount int

		if err := db.QueryRow(`
			SELECT COUNT(*)
			FROM schema_migrations
			WHERE version = $1
		`, version).Scan(&appliedCount); err != nil {
			return err
		}

		if appliedCount != 1 {
			return fmt.Errorf(
				"expected migration %06d to be recorded once, got %d",
				version,
				appliedCount,
			)
		}
	}

	expectedNames := map[int]string{
		6: "mcp",
		7: "one_to_one_session_attendance",
		9: "tournaments",
	}
	for version, wantName := range expectedNames {
		var migrationName string

		if err := db.QueryRow(`
			SELECT name
			FROM schema_migrations
			WHERE version = $1
		`, version).Scan(&migrationName); err != nil {
			return err
		}

		if migrationName != wantName {
			return fmt.Errorf(
				"migration %06d recorded as %q, want %q",
				version,
				migrationName,
				wantName,
			)
		}
	}

	for _, column := range []struct {
		table  string
		column string
	}{
		{"one_to_one_bookings", "coach_user_id"},
		{"one_to_one_bookings", "package_status"},
		{"one_to_one_bookings", "completed_sessions"},
		{"one_to_one_bookings", "cancelled_sessions"},
		{"one_to_one_booking_sessions", "coach_user_id"},
		{"one_to_one_booking_sessions", "coach_fee"},
		{"one_to_one_booking_sessions", "status"},
		{"one_to_one_booking_sessions", "attendance_status"},
		{"one_to_one_booking_sessions", "attendance_note"},
		{"one_to_one_booking_sessions", "attendance_marked_at"},
		{"one_to_one_booking_sessions", "attendance_marked_by_user_id"},
		{"one_to_one_booking_sessions", "completed_at"},
		{"one_to_one_booking_sessions", "completed_by_user_id"},
		{"one_to_one_booking_sessions", "cancelled_at"},
		{"one_to_one_booking_sessions", "notes"},
		{"tournaments", "tournament_date"},
		{"tournaments", "entry_fee_finance_transaction_id"},
		{"tournament_sponsorships", "finance_transaction_id"},
		{"tournament_official_payments", "finance_transaction_id"},
		{"tournament_expenses", "expense_type"},
	} {
		if err := postgresColumnMustExist(db, column.table, column.column); err != nil {
			return err
		}
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

func postgresColumnMustExist(
	db *sql.DB,
	table string,
	column string,
) error {
	var exists bool

	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf(
			"expected PostgreSQL column %s.%s to exist",
			table,
			column,
		)
	}

	return nil
}
