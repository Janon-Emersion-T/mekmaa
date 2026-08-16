package main

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLoadRuntimeDependenciesAllowsProductionWithoutBootstrapSuperadmin(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("ADDR", ":8080")
	t.Setenv("DB_PATH", t.TempDir()+"/prod.db")
	t.Setenv("UPLOAD_DIR", t.TempDir()+"/uploads")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("MEKMAA_PUBLIC_BASE_URL", "https://mekmaa.example")
	t.Setenv("BOOKING_ACCESS_TOKEN_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("BOOKING_EMAIL_ENABLED", "false")
	t.Setenv("BOOKING_SMS_ENABLED", "false")
	t.Setenv("BOOTSTRAP_SUPERADMIN_NAME", "")
	t.Setenv("BOOTSTRAP_SUPERADMIN_EMAIL", "")
	t.Setenv("BOOTSTRAP_SUPERADMIN_PASSWORD", "")

	deps, err := loadRuntimeDependencies()
	if err != nil {
		t.Fatalf("load runtime dependencies: %v", err)
	}
	for _, issue := range deps.ConfigurationErrors {
		if strings.Contains(issue, "BOOTSTRAP_SUPERADMIN") {
			t.Fatalf("unexpected bootstrap configuration error: %s", issue)
		}
	}
}

func TestBootstrapSuperadminSkipsWhenNoSeedProvided(t *testing.T) {
	db, err := sql.Open("sqlite", "file:bootstrap-skip?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedRoles(db); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	if err := bootstrapSuperadmin(db, nil); err != nil {
		t.Fatalf("bootstrap superadmin: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE r.name = 'superadmin'`).Scan(&count); err != nil {
		t.Fatalf("count superadmins: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no superadmin role assignments, got %d", count)
	}
}

func TestBootstrapSuperadminCreatesOrUpdatesIdempotently(t *testing.T) {
	db, err := sql.Open("sqlite", "file:bootstrap-idempotent?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedRoles(db); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	seed := &superadminBootstrapSeed{
		Name:     "Release Gate Admin",
		Email:    "release-admin@example.com",
		Password: "BootstrapPass123!",
	}
	if err := bootstrapSuperadmin(db, seed); err != nil {
		t.Fatalf("bootstrap first pass: %v", err)
	}
	if err := bootstrapSuperadmin(db, seed); err != nil {
		t.Fatalf("bootstrap second pass: %v", err)
	}
	var userCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, seed.Email).Scan(&userCount); err != nil {
		t.Fatalf("count bootstrap user: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("expected one bootstrap user, got %d", userCount)
	}
	var roleCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM user_roles ur
		JOIN users u ON u.id = ur.user_id
		WHERE u.email = ?
	`, seed.Email).Scan(&roleCount); err != nil {
		t.Fatalf("count bootstrap roles: %v", err)
	}
	if roleCount != 3 {
		t.Fatalf("expected three bootstrap roles, got %d", roleCount)
	}
}

func TestRunSeedUATDoesNotLogPassword(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer log.SetOutput(previousWriter)
	defer log.SetFlags(previousFlags)

	if err := runSeedUAT(app.db, appEnvDevelopment); err != nil {
		t.Fatalf("run seed uat: %v", err)
	}
	if strings.Contains(buf.String(), uatDefaultPassword) {
		t.Fatalf("seed-uat log exposed password: %s", buf.String())
	}
}

func TestRunSeedUATRefusesProduction(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := runSeedUAT(app.db, appEnvProduction); err == nil {
		t.Fatal("expected production seed-uat refusal")
	}
}

func TestRunSeedUATCreatesSharedStudentAcrossDivisions(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := runSeedUAT(app.db, appEnvDevelopment); err != nil {
		t.Fatalf("run seed uat: %v", err)
	}
	var admissionID int64
	if err := app.db.QueryRow(`SELECT id FROM admissions WHERE student_id = ?`, uatSharedStudentID).Scan(&admissionID); err != nil {
		t.Fatalf("find shared student: %v", err)
	}
	var enrollmentCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM student_enrollments WHERE admission_id = ?`, admissionID).Scan(&enrollmentCount); err != nil {
		t.Fatalf("count shared student enrollments: %v", err)
	}
	if enrollmentCount != 3 {
		t.Fatalf("expected 3 enrollments for shared student, got %d", enrollmentCount)
	}
}

func TestRunSeedUATMonthlyPaymentsStayInEnrollmentDivision(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := runSeedUAT(app.db, appEnvDevelopment); err != nil {
		t.Fatalf("run seed uat: %v", err)
	}
	rows, err := app.db.Query(`
		SELECT tp.name, d.code, recorded.code
		FROM student_monthly_payments smp
		JOIN student_enrollments se ON se.id = smp.enrollment_id
		JOIN training_programs tp ON tp.id = se.training_program_id
		JOIN divisions d ON d.id = tp.division_id
		JOIN finance_transactions ft ON ft.id = smp.finance_transaction_id
		JOIN divisions recorded ON recorded.id = ft.division_id
		ORDER BY smp.id
	`)
	if err != nil {
		t.Fatalf("query monthly payment divisions: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			name                 string
			expectedDivisionCode string
			recordedDivisionCode string
		)
		if err := rows.Scan(&name, &expectedDivisionCode, &recordedDivisionCode); err != nil {
			t.Fatalf("scan monthly payment division: %v", err)
		}
		if recordedDivisionCode != expectedDivisionCode {
			t.Fatalf("monthly payment for %s posted to %s, want %s", name, recordedDivisionCode, expectedDivisionCode)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate monthly payment divisions: %v", err)
	}
}

func TestCorporateOnlyUserCannotOpenEnrollmentManagement(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := runSeedUAT(app.db, appEnvDevelopment); err != nil {
		t.Fatalf("run seed uat: %v", err)
	}
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	basicUser, _, err := app.findUserByEmail("corporate-finance+uat@mekmaa.local")
	if err != nil {
		t.Fatalf("find corporate user: %v", err)
	}
	user, err := app.findUserByID(basicUser.ID)
	if err != nil {
		t.Fatalf("load corporate user with divisions: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/enrollments?division=corporate", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.enrollmentManagementHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("corporate enrollments status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestDivisionScopedUserCannotOpenCrossDivisionFinanceAccountStatement(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := runSeedUAT(app.db, appEnvDevelopment); err != nil {
		t.Fatalf("run seed uat: %v", err)
	}
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	basicUser, _, err := app.findUserByEmail("kec-admin+uat@mekmaa.local")
	if err != nil {
		t.Fatalf("find kec user: %v", err)
	}
	user, err := app.findUserByID(basicUser.ID)
	if err != nil {
		t.Fatalf("load kec user with divisions: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/finance/accounts/statement?account_id=3", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.financeAccountStatementHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-division finance account statement status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestRunMigrationsSupportsLegacyFinanceTransactionsBeforeEnrollmentBackfill(t *testing.T) {
	sourcePath := filepath.Join(".prod-sim", "mekmaa.db")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read legacy db fixture: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	if err := os.WriteFile(dbPath, raw, 0o600); err != nil {
		t.Fatalf("copy legacy db fixture: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations on legacy finance schema: %v", err)
	}
}
