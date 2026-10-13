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

func TestPostgresHistoricalMigrationChecksumsRemainStable(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	expected := map[int64]string{
		1:  "c1b2ea5369ed956943e8f01e470fdeab9c5032570ef3061d2ab7263f5383b817",
		2:  "c4fb4401db2b5552f592d118e5ce9d8bd47ee54bf6271fd12899d28e0b9bd1bf",
		3:  "f54f86c803efb65a52a54dc5be0678f7eb1347d9bdd753a57155812ff917cd7f",
		4:  "322f7057a5c75680e6c46ef69f58fb5461ba6b27ae70198a98398db3a7175eff",
		5:  "4d4d42ee0f0dc8c7037c4bc37993b397da0a1e1cb197ef3eb38c3d5101704bdc",
		6:  "a3e0bcf28bd372987adf55787efe69f92c60e2b9f5573688fc413bb7c0c87a7a",
		7:  "eeff337219640e2db77bb41bab7e98b8c81d52d7bf08b50b070ebe93bc54bea1",
		8:  "3941a1007cdeb57563aee31ea21f38d2e076e16037fcf12fca04a0ce7ea54b32",
		9:  "b764fcd5cfe0f6093ca016986d0252c3f5c8bdcf3cc9197deec9ec05f57c5265",
		10: "dcea40e33c85caf1075aa011c44561aa68a1b4c93e96ce1e5a3128bfd0e0587c",
		11: "400248458e8f040d4e081ed48b137af895a8968da317f70de190a88f4472d352",
		12: "abf2b22659ef89d7c660749f0ba3d27d10de1dee676eee4bedc334b1b41f29d7",
		13: "ce5a4152208c1394a9e00435825397dd216061317c3df8b778aaab480044f997",
		14: "4356cdc16c55b9cf7058d00e151fb78dc11128fd908dced6087f049be4a04d34",
		15: "25818b97aeed4beb924f1f1ff17fff1a56f1615cf510643afcad4c3e7f68310e",
		16: "d4430ef2fa5c36f4d3a83ab1b4bb8aea1dfb8cd266074456acd4f240c0c73a93",
		17: "3238fc732e4f0c3832ae8efc970af3ad8e1dd9f7204a3216cf5198eb10f45b6b",
		18: "d60c66822ea9e4bdd065bda620e5072287fa92b01306ca8750209e8d651776e3",
		19: "b6af8cf1273b4b3a939b9d3f77ea8850081d742d09e962be95bfb007cd42cbc3",
		20: "784528841d61fb3a444464eeb8485a95c92e601c284f4d4de326910d494e1759",
		21: "47925db13ca0f6bde078aadeeecf2e7fc4a0c132386354a56c798e3fe30f3225",
		22: "691897b18a35290719654088f4dd5e65c15cb475907496a07e9c922f075cdd9a",
		23: "db1eb04a7f4114050e134045548095b97fe4250f2b4432b1875010ce19cc8f4a",
	}

	byVersion := make(map[int64]postgresMigration, len(migrations))
	for _, migration := range migrations {
		byVersion[migration.Version] = migration
	}

	for version, wantChecksum := range expected {
		migration, ok := byVersion[version]
		if !ok {
			t.Fatalf("expected PostgreSQL migration %06d to exist", version)
		}
		if migration.Checksum != wantChecksum {
			t.Fatalf(
				"migration %s checksum = %s, want %s",
				migration.Filename,
				migration.Checksum,
				wantChecksum,
			)
		}
	}
}

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

func TestPostgresMigrationDiscoveryIncludesStudentMonthlyPaymentAdjustments(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 14 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000014_student_monthly_payment_adjustments.sql")
	}

	if found.Filename != "000014_student_monthly_payment_adjustments.sql" {
		t.Fatalf("migration 14 filename = %q, want %q", found.Filename, "000014_student_monthly_payment_adjustments.sql")
	}

	if found.Name != "student_monthly_payment_adjustments" {
		t.Fatalf("migration 14 name = %q, want %q", found.Name, "student_monthly_payment_adjustments")
	}

	if found.Checksum == "" {
		t.Fatal("migration 14 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesPayrollSessionFoundation(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 15 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000015_payroll_session_foundation.sql")
	}

	if found.Filename != "000015_payroll_session_foundation.sql" {
		t.Fatalf("migration 15 filename = %q, want %q", found.Filename, "000015_payroll_session_foundation.sql")
	}

	if found.Name != "payroll_session_foundation" {
		t.Fatalf("migration 15 name = %q, want %q", found.Name, "payroll_session_foundation")
	}

	if found.Checksum == "" {
		t.Fatal("migration 15 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesPayrollPaymentCalculationDetails(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 16 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000016_payroll_payment_calculation_details.sql")
	}

	if found.Filename != "000016_payroll_payment_calculation_details.sql" {
		t.Fatalf("migration 16 filename = %q, want %q", found.Filename, "000016_payroll_payment_calculation_details.sql")
	}

	if found.Name != "payroll_payment_calculation_details" {
		t.Fatalf("migration 16 name = %q, want %q", found.Name, "payroll_payment_calculation_details")
	}

	if found.Checksum == "" {
		t.Fatal("migration 16 checksum must not be empty")
	}
}

func TestPostgresMigrationDiscoveryIncludesPayrollWorkHistory(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}

	var found *postgresMigration
	for i := range migrations {
		if migrations[i].Version == 17 {
			found = &migrations[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected PostgreSQL migration 000017_payroll_work_history.sql")
	}

	if found.Filename != "000017_payroll_work_history.sql" {
		t.Fatalf("migration 17 filename = %q, want %q", found.Filename, "000017_payroll_work_history.sql")
	}

	if found.Name != "payroll_work_history" {
		t.Fatalf("migration 17 name = %q, want %q", found.Name, "payroll_work_history")
	}

	if found.Checksum == "" {
		t.Fatal("migration 17 checksum must not be empty")
	}
}

func TestPostgresMCPMigrationAppliesCleanly(t *testing.T) {
	runPostgresMigrationHelper(t, "apply_all")
}

func TestPostgresMCPMigrationUpgradeFrom000005(t *testing.T) {
	runPostgresMigrationHelper(t, "upgrade_from_000005")
}

func TestPostgresMigrationUpgradeFrom000023(t *testing.T) {
	runPostgresMigrationHelper(t, "upgrade_from_000023")
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
		if err := applyPostgresMigrationsThroughVersion(ctx, db, 5); err != nil {
			return err
		}

		// This now represents a real database already migrated through 000005.
		// Running the normal production migration engine must apply 000006+
		// exactly as production startup would.
		if err := runPostgresMigrations(db); err != nil {
			return err
		}

	case "upgrade_from_000023":
		if err := applyPostgresMigrationsThroughVersion(ctx, db, 23); err != nil {
			return err
		}

		// This models a production database at the last successful deployment:
		// historical migrations are already recorded, and only 000024+ should run.
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
		"student_group_session_occurrences",
		"student_group_session_staff",
		"payroll_payment_calculation_details",
		"staff_work_time_records",
		"student_enrollment_status_history",
		"student_group_membership_history",
		"student_group_staff_assignment_history",
		"enrollment_financial_closures",
		"enrollment_excess_resolutions",
		"monthly_fee_sms_reminders",
	} {
		if err := postgresRelationMustExist(db, relation); err != nil {
			return err
		}
	}

	migrations, err := loadPostgresMigrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		var appliedCount int

		if err := db.QueryRow(`
			SELECT COUNT(*)
			FROM schema_migrations
			WHERE version = $1
		`, migration.Version).Scan(&appliedCount); err != nil {
			return err
		}

		if appliedCount != 1 {
			return fmt.Errorf(
				"expected migration %06d to be recorded once, got %d",
				migration.Version,
				appliedCount,
			)
		}
	}

	expectedNames := map[int]string{
		6:  "mcp",
		7:  "one_to_one_session_attendance",
		9:  "tournaments",
		14: "student_monthly_payment_adjustments",
		15: "payroll_session_foundation",
		16: "payroll_payment_calculation_details",
		17: "payroll_work_history",
		24: "enrollment_financial_closures",
		25: "enrollment_excess_resolutions",
		26: "monthly_fee_sms_reminders",
		27: "student_leave_fee_policy",
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
		{"student_monthly_payments", "discount_amount"},
		{"student_monthly_payments", "adjustment_reason"},
		{"student_group_session_occurrences", "timetable_session_id"},
		{"student_group_session_occurrences", "occurrence_date"},
		{"student_group_session_occurrences", "is_ad_hoc"},
		{"student_group_session_staff", "assignment_role"},
		{"student_group_session_staff", "work_status"},
		{"payroll_payments", "finance_transaction_id"},
		{"payroll_payment_calculation_details", "detail_type"},
		{"payroll_payment_calculation_details", "source_type"},
		{"payroll_payment_calculation_details", "amount_snapshot"},
		{"staff_work_time_records", "break_minutes"},
		{"student_enrollment_status_history", "effective_to"},
		{"student_group_membership_history", "effective_to"},
		{"student_group_staff_assignment_history", "assignment_role"},
		{"student_enrollment_leaves", "fee_policy"},
		{"enrollment_financial_closures", "excess_action"},
		{"enrollment_excess_resolutions", "resolution_payment_id"},
		{"monthly_fee_sms_reminders", "status"},
	} {
		if err := postgresColumnMustExist(db, column.table, column.column); err != nil {
			return err
		}
	}

	if err := postgresConstraintMustExist(db, "payroll_payments_finance_transaction_id_fkey"); err != nil {
		return err
	}
	if err := postgresConstraintMustExist(db, "student_enrollment_leaves_fee_policy_check"); err != nil {
		return err
	}

	for _, indexName := range []string{
		"idx_student_group_session_occurrences_normal_unique",
		"idx_finance_transactions_source_payroll_payment",
		"idx_enrollment_status_history_open",
		"idx_group_membership_history_open",
		"idx_group_staff_history_open",
		"idx_enrollment_financial_closures_date",
		"idx_enrollment_excess_resolutions_enrollment",
		"idx_monthly_fee_sms_reminders_status",
	} {
		if err := postgresRelationMustExist(db, indexName); err != nil {
			return err
		}
	}

	if err := postgresColumnMustBeNullable(db, "payroll_payments", "finance_transaction_id"); err != nil {
		return err
	}

	return nil
}

func applyPostgresMigrationsThroughVersion(
	ctx context.Context,
	db *sql.DB,
	maxVersion int64,
) error {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		return err
	}

	if err := ensurePostgresMigrationTable(ctx, db); err != nil {
		return err
	}

	for _, migration := range migrations {
		if migration.Version > maxVersion {
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

func postgresConstraintMustExist(
	db *sql.DB,
	constraintName string,
) error {
	var exists bool

	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conname = $1
		)
	`, constraintName).Scan(&exists); err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("expected PostgreSQL constraint %s to exist", constraintName)
	}

	return nil
}

func postgresColumnMustBeNullable(
	db *sql.DB,
	table string,
	column string,
) error {
	var nullable string

	if err := db.QueryRow(`
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1
		  AND column_name = $2
	`, table, column).Scan(&nullable); err != nil {
		return err
	}

	if nullable != "YES" {
		return fmt.Errorf("expected PostgreSQL column %s.%s to remain nullable", table, column)
	}

	return nil
}
