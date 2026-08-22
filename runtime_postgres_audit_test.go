package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const runtimePostgresAuditHelperEnv = "GO_WANT_RUNTIME_POSTGRES_AUDIT_HELPER"

func TestRuntimePostgresAuditWorkflow(t *testing.T) {
	if _, err := exec.LookPath("pg_virtualenv"); err != nil {
		t.Skip("pg_virtualenv is not available")
	}

	cmd := exec.Command(
		"pg_virtualenv",
		os.Args[0],
		"-test.run=^TestRuntimePostgresAuditHelperProcess$",
	)
	cmd.Env = append(os.Environ(), runtimePostgresAuditHelperEnv+"=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PostgreSQL runtime audit helper failed: %v\n%s", err, output)
	}
}

func TestRuntimePostgresAuditHelperProcess(t *testing.T) {
	if os.Getenv(runtimePostgresAuditHelperEnv) != "1" {
		return
	}

	if err := runRuntimePostgresAuditWorkflow(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

func runRuntimePostgresAuditWorkflow() error {
	db, err := sql.Open("pgx", "")
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return err
	}
	if err := runPostgresMigrations(db); err != nil {
		return err
	}
	if err := applyPostgresBootstrapData(db); err != nil {
		return err
	}

	app := &App{
		db: db,
		runtimeConfig: AppRuntimeConfig{
			DBDriver: databaseDriverPostgres,
		},
	}

	var sportsDivisionID int64
	if err := db.QueryRow(`SELECT id FROM divisions WHERE code = $1`, divisionCodeSports).Scan(&sportsDivisionID); err != nil {
		return fmt.Errorf("find sports division: %w", err)
	}

	user, err := app.createManagedUser(
		"Runtime Audit User",
		"runtime-audit@example.com",
		"password-123",
		[]string{"admin"},
		true,
	)
	if err != nil {
		return fmt.Errorf("create managed user: %w", err)
	}
	if user.ID <= 0 {
		return fmt.Errorf("managed user id = %d", user.ID)
	}

	if err := app.createFinanceCategory("Runtime Audit Income", financeTxnTypeIncome, true); err != nil {
		return fmt.Errorf("create finance category: %w", err)
	}

	accountID, err := app.createFinanceAccount(
		sportsDivisionID,
		"",
		"Runtime Audit Cash",
		financeAccountTypeCash,
		"runtime audit",
		user.ID,
	)
	if err != nil {
		return fmt.Errorf("create finance account: %w", err)
	}
	if accountID <= 0 {
		return fmt.Errorf("finance account id = %d", accountID)
	}

	badmintonGameID, err := app.createGame(Game{
		Name:        "Runtime Audit Badminton",
		Activity:    "badminton",
		Description: "runtime audit",
		Active:      true,
		SortOrder:   999,
	})
	if err != nil {
		return fmt.Errorf("create game: %w", err)
	}
	if badmintonGameID <= 0 {
		return fmt.Errorf("game id = %d", badmintonGameID)
	}

	courtID, err := app.createCourt(Court{
		Name:        "Runtime Audit Court",
		Code:        "RUNTIME_AUDIT_COURT",
		Description: "runtime audit",
		Active:      true,
		SortOrder:   999,
	})
	if err != nil {
		return fmt.Errorf("create court: %w", err)
	}
	if courtID <= 0 {
		return fmt.Errorf("court id = %d", courtID)
	}

	activityID, err := app.createCourtActivity(CourtActivity{
		CourtID:     courtID,
		GameID:      0,
		Activity:    "badminton",
		DisplayName: "Badminton",
		MaxQuantity: 1,
		AutoAccept:  true,
		Active:      true,
		SortOrder:   1,
	})
	if err != nil {
		return fmt.Errorf("create court activity: %w", err)
	}
	if activityID <= 0 {
		return fmt.Errorf("court activity id = %d", activityID)
	}

	closureID, err := app.createCourtClosure(CourtClosure{
		CourtID:     courtID,
		ClosureDate: "2026-08-01",
		StartHour:   "08:00",
		EndHour:     "09:00",
		Activity:    "badminton",
		Title:       "Runtime Audit Closure",
		Reason:      "runtime audit",
		Active:      true,
	})
	if err != nil {
		return fmt.Errorf("create court closure: %w", err)
	}
	if closureID <= 0 {
		return fmt.Errorf("court closure id = %d", closureID)
	}

	salaryProfileID, err := app.createStaffSalaryProfile(StaffSalaryProfile{
		UserID:           user.ID,
		DivisionID:       sportsDivisionID,
		CompensationType: SalaryTypeHourly,
		Rate:             2500,
		StudentBasis:     SalaryStudentBasisActiveEnrollment,
		EffectiveFrom:    "2026-08-01",
		EffectiveTo:      "",
		Active:           true,
		Notes:            "runtime audit",
	}, user.ID)
	if err != nil {
		return fmt.Errorf("create salary profile: %w", err)
	}
	if salaryProfileID <= 0 {
		return fmt.Errorf("salary profile id = %d", salaryProfileID)
	}

	payrollRunID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "Runtime Audit Payroll", user.ID)
	if err != nil {
		return fmt.Errorf("create payroll run: %w", err)
	}
	if payrollRunID <= 0 {
		return fmt.Errorf("payroll run id = %d", payrollRunID)
	}

	tournamentID, err := app.createTournament(
		"Runtime Audit Tournament",
		badmintonGameID,
		"2026-08-10",
		8,
		2500,
		accountID,
		time.Date(2026, time.August, 10, 9, 0, 0, 0, time.UTC),
		"runtime audit",
		user.ID,
	)
	if err != nil {
		return fmt.Errorf("create tournament: %w", err)
	}
	if tournamentID <= 0 {
		return fmt.Errorf("tournament id = %d", tournamentID)
	}

	if err := app.createTournamentSponsorship(
		tournamentID,
		"Runtime Audit Sponsor",
		"runtime audit",
		5000,
		accountID,
		time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC),
		user.ID,
	); err != nil {
		return fmt.Errorf("create tournament sponsorship: %w", err)
	}

	if err := app.createTournamentOfficialPayment(
		tournamentID,
		"Runtime Audit Referee",
		"Referee",
		"runtime audit",
		1800,
		accountID,
		time.Date(2026, time.August, 12, 11, 0, 0, 0, time.UTC),
		user.ID,
	); err != nil {
		return fmt.Errorf("create tournament official payment: %w", err)
	}

	if err := app.createTournamentExpense(
		tournamentID,
		"refreshments",
		"Runtime Audit Drinks",
		"runtime audit",
		1200,
		accountID,
		time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC),
		user.ID,
	); err != nil {
		return fmt.Errorf("create tournament expense: %w", err)
	}

	reconciliationID, err := app.createCashReconciliation(
		accountID,
		"2026-08-14",
		22000,
		"runtime audit reconciliation",
		user.ID,
	)
	if err != nil {
		return fmt.Errorf("create cash reconciliation: %w", err)
	}
	if reconciliationID <= 0 {
		return fmt.Errorf("cash reconciliation id = %d", reconciliationID)
	}

	return nil
}
