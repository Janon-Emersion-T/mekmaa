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

const mcpPostgresHelperEnv = "GO_WANT_MCP_POSTGRES_HELPER"

func TestMCPPostgresWorkflow(t *testing.T) {
	if _, err := exec.LookPath("pg_virtualenv"); err != nil {
		t.Skip("pg_virtualenv is not available")
	}

	cmd := exec.Command(
		"pg_virtualenv",
		os.Args[0],
		"-test.run=^TestMCPPostgresHelperProcess$",
	)
	cmd.Env = append(os.Environ(), mcpPostgresHelperEnv+"=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PostgreSQL MCP helper failed: %v\n%s", err, output)
	}
}

func TestMCPPostgresHelperProcess(t *testing.T) {
	if os.Getenv(mcpPostgresHelperEnv) != "1" {
		return
	}

	if err := runMCPPostgresWorkflow(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

func runMCPPostgresWorkflow() error {
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
	if err := seedMCPPostgresFinanceAccounts(db); err != nil {
		return err
	}

	app := &App{
		db: db,
		runtimeConfig: AppRuntimeConfig{
			DBDriver: databaseDriverPostgres,
		},
		bookingMessages: BookingCommunicationSettings{
			VenueName:    "Mekmaa",
			VenueAddress: "No. 64, Temple Road, Jaffna 40000",
			ContactPhone: "+94772207297",
			ContactEmail: "bookings@mekmaa.example",
		},
		bookingAccess: BookingAccessSettings{
			BaseURL:     "http://localhost:8080",
			TokenSecret: "test-secret",
			TokenTTL:    180 * 24 * time.Hour,
		},
	}

	customers, err := app.listMCPMonthlyCustomers()
	if err != nil {
		return fmt.Errorf("list MCP customers before create: %w", err)
	}
	if len(customers) != 0 {
		return fmt.Errorf("expected no MCP customers before create, got %d", len(customers))
	}

	customerID, err := app.createMCPMonthlyCustomer(
		"Postgres MCP Customer",
		"postgres-mcp@example.com",
		"0700000000",
		"password-123",
		"postgres workflow",
		true,
	)
	if err != nil {
		return fmt.Errorf("create MCP customer: %w", err)
	}
	if customerID <= 0 {
		return fmt.Errorf("create MCP customer returned invalid id %d", customerID)
	}

	user, _, err := app.findUserByEmail("postgres-mcp@example.com")
	if err != nil {
		return fmt.Errorf("find MCP user by email: %w", err)
	}

	customerByUser, err := app.findMCPMonthlyCustomerByUserID(user.ID)
	if err != nil {
		return fmt.Errorf("find MCP customer by user id: %w", err)
	}
	if customerByUser.ID != customerID {
		return fmt.Errorf("customer by user id = %d, want %d", customerByUser.ID, customerID)
	}

	customerByID, err := app.findMCPMonthlyCustomerByID(customerID)
	if err != nil {
		return fmt.Errorf("find MCP customer by id: %w", err)
	}
	if customerByID.Email != "postgres-mcp@example.com" {
		return fmt.Errorf("customer email = %q, want postgres-mcp@example.com", customerByID.Email)
	}

	customers, err = app.listMCPMonthlyCustomers()
	if err != nil {
		return fmt.Errorf("list MCP customers after create: %w", err)
	}
	if len(customers) != 1 {
		return fmt.Errorf("expected 1 MCP customer after create, got %d", len(customers))
	}

	bandID, err := app.createMCPPricingBand(MCPPricingBand{
		Tier:            mcpTierWeekdayOffPeak,
		MinimumSessions: 1,
		MaximumSessions: 10,
		PricePerSession: 2500,
		Active:          true,
	})
	if err != nil {
		return fmt.Errorf("create MCP pricing band: %w", err)
	}
	if bandID <= 0 {
		return fmt.Errorf("create MCP pricing band returned invalid id %d", bandID)
	}

	bands, err := app.listMCPPricingBands()
	if err != nil {
		return fmt.Errorf("list MCP pricing bands: %w", err)
	}
	if len(bands) != 1 {
		return fmt.Errorf("expected 1 MCP pricing band, got %d", len(bands))
	}
	if bands[0].ID != bandID {
		return fmt.Errorf("listed MCP pricing band id = %d, want %d", bands[0].ID, bandID)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	directBands, err := listMCPPricingBandsQuery(tx)
	_ = tx.Rollback()
	if err != nil {
		return fmt.Errorf("list MCP pricing bands via tx queryer: %w", err)
	}
	if len(directBands) != 1 || directBands[0].ID != bandID {
		return fmt.Errorf("tx queryer MCP pricing bands mismatch: %#v", directBands)
	}

	planID, err := app.createMCPMonthlyPlan(
		customerID,
		"2026-09",
		"badminton",
		1,
		"Postgres Workflow Plan",
		"",
		[]MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}},
		user.ID,
		false,
	)
	if err != nil {
		return fmt.Errorf("create MCP monthly plan: %w", err)
	}
	if planID <= 0 {
		return fmt.Errorf("create MCP monthly plan returned invalid id %d", planID)
	}

	plans, err := app.listMCPMonthlyPlans(customerID)
	if err != nil {
		return fmt.Errorf("list MCP monthly plans by customer: %w", err)
	}
	if len(plans) != 1 || plans[0].ID != planID {
		return fmt.Errorf("unexpected customer plan list: %#v", plans)
	}

	plan, err := app.findMCPMonthlyPlanByID(planID)
	if err != nil {
		return fmt.Errorf("find MCP monthly plan by id: %w", err)
	}
	if plan.Status != mcpPlanStatusPending {
		return fmt.Errorf("plan status = %q, want %q", plan.Status, mcpPlanStatusPending)
	}
	if len(plan.Rules) != 1 || plan.Rules[0].ID <= 0 {
		return fmt.Errorf("unexpected plan rules: %#v", plan.Rules)
	}
	if len(plan.Sessions) != 4 {
		return fmt.Errorf("plan sessions len = %d, want 4", len(plan.Sessions))
	}
	for _, session := range plan.Sessions {
		if session.ID <= 0 {
			return fmt.Errorf("plan session has invalid id: %#v", session)
		}
	}

	tx, err = db.Begin()
	if err != nil {
		return err
	}
	planByQueryer, err := findMCPMonthlyPlanByIDQuery(tx, planID)
	_ = tx.Rollback()
	if err != nil {
		return fmt.Errorf("find MCP monthly plan by tx queryer: %w", err)
	}
	if planByQueryer.ID != planID {
		return fmt.Errorf("tx queryer plan id = %d, want %d", planByQueryer.ID, planID)
	}

	if err := app.confirmMCPMonthlyPlan(planID, user.ID); err != nil {
		return fmt.Errorf("confirm MCP monthly plan: %w", err)
	}

	plan, err = app.findMCPMonthlyPlanByID(planID)
	if err != nil {
		return fmt.Errorf("reload confirmed MCP plan: %w", err)
	}
	if plan.Status != mcpPlanStatusConfirmed {
		return fmt.Errorf("confirmed plan status = %q, want %q", plan.Status, mcpPlanStatusConfirmed)
	}

	transactionID, err := app.collectMCPPayment(planID, "cash", 2500, "Deposit", user.ID)
	if err != nil {
		return fmt.Errorf("collect MCP payment: %w", err)
	}
	if transactionID <= 0 {
		return fmt.Errorf("collect MCP payment returned invalid transaction id %d", transactionID)
	}

	plan, err = app.findMCPMonthlyPlanByID(planID)
	if err != nil {
		return fmt.Errorf("reload plan after MCP payment: %w", err)
	}
	if plan.TotalCollected <= 0 || plan.OutstandingAmount >= plan.GrossAmount {
		return fmt.Errorf("unexpected plan totals after payment: %#v", plan)
	}

	var sourceType string
	var sourceID int64
	if err := db.QueryRow(`
		SELECT source_type, COALESCE(source_id, 0)
		FROM finance_transactions
		WHERE id = $1
	`, transactionID).Scan(&sourceType, &sourceID); err != nil {
		return fmt.Errorf("load MCP finance transaction linkage: %w", err)
	}
	if sourceType != "mcp_payment_collection" || sourceID <= 0 {
		return fmt.Errorf("unexpected MCP finance linkage source_type=%q source_id=%d", sourceType, sourceID)
	}

	nextPlanID, err := app.continueMCPMonthlyPlan(planID, "2026-10", user.ID)
	if err != nil {
		return fmt.Errorf("continue MCP monthly plan: %w", err)
	}
	if nextPlanID <= 0 || nextPlanID == planID {
		return fmt.Errorf("continue MCP monthly plan returned invalid next id %d", nextPlanID)
	}

	allPlans, err := app.listMCPMonthlyPlans(0)
	if err != nil {
		return fmt.Errorf("list all MCP monthly plans: %w", err)
	}
	if len(allPlans) != 2 {
		return fmt.Errorf("expected 2 MCP monthly plans after continue, got %d", len(allPlans))
	}

	return nil
}

func seedMCPPostgresFinanceAccounts(db *sql.DB) error {
	var sportsDivisionID int64
	if err := db.QueryRow(`
		SELECT id
		FROM divisions
		WHERE code = $1
	`, divisionCodeSports).Scan(&sportsDivisionID); err != nil {
		return fmt.Errorf("find sports division for MCP PostgreSQL finance accounts: %w", err)
	}

	now := time.Now().UTC()
	accounts := []struct {
		name        string
		accountType string
	}{
		{name: financeAccountCashInHand, accountType: financeAccountTypeCash},
		{name: financeAccountMainBank, accountType: financeAccountTypeBank},
	}

	for _, account := range accounts {
		if _, err := db.Exec(`
			INSERT INTO finance_accounts (
				division_id, account_code, name, account_type, description, opening_balance, is_system, is_active,
				created_at, updated_at, created_by_user_id, updated_by_user_id
			) VALUES (
				$1, $2, $3, $4, $5, 0, 1, 1, $6, $6, NULL, NULL
			)
		`,
			sportsDivisionID,
			financeSystemAccountCode(divisionCodeSports, account.accountType),
			account.name,
			account.accountType,
			financeSystemAccountDescription("Sports", account.accountType),
			now,
		); err != nil {
			return fmt.Errorf("insert MCP PostgreSQL finance account %q: %w", account.name, err)
		}
	}

	return nil
}
