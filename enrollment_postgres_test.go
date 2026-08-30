package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const enrollmentPostgresHelperEnv = "GO_WANT_ENROLLMENT_POSTGRES_HELPER"

func TestEnrollmentUpdatePostgresWorkflow(t *testing.T) {
	if _, err := exec.LookPath("pg_virtualenv"); err != nil {
		t.Skip("pg_virtualenv is not available")
	}

	cmd := exec.Command(
		"pg_virtualenv",
		os.Args[0],
		"-test.run=^TestEnrollmentUpdatePostgresHelperProcess$",
	)
	cmd.Env = append(os.Environ(), enrollmentPostgresHelperEnv+"=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PostgreSQL enrollment helper failed: %v\n%s", err, output)
	}
}

func TestEnrollmentUpdatePostgresHelperProcess(t *testing.T) {
	if os.Getenv(enrollmentPostgresHelperEnv) != "1" {
		return
	}

	if err := runEnrollmentUpdatePostgresWorkflow(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

func runEnrollmentUpdatePostgresWorkflow() error {
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

	sportsDivisionID, err := divisionIDByCode(db, divisionCodeSports)
	if err != nil {
		return fmt.Errorf("find sports division: %w", err)
	}

	programID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     sportsDivisionID,
		Name:           "Postgres Enrollment Edit",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     3000,
		Active:         true,
	})
	if err != nil {
		return fmt.Errorf("create training programme: %w", err)
	}
	secondProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     sportsDivisionID,
		Name:           "Postgres Enrollment Edit 2",
		Activity:       "badminton",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     3200,
		Active:         true,
	})
	if err != nil {
		return fmt.Errorf("create second training programme: %w", err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-PG-EDIT-001",
		FullName:              "Postgres Edit Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2014-01-01",
		Gender:                "female",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian Postgres",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771110030",
	}, false, "cash", 0)
	if err != nil {
		return fmt.Errorf("create admission: %w", err)
	}

	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
		EnrollmentDate:    "2026-08-01",
	}, false, "cash", 0)
	if err != nil {
		return fmt.Errorf("create enrollment: %w", err)
	}

	if err := app.updateStudentEnrollment(StudentEnrollment{
		ID:                   enrollmentID,
		AdmissionID:          admissionID,
		TrainingProgramID:    programID,
		TrainingProgramName:  "Postgres Enrollment Edit",
		EnrollmentDate:       "2026-08-05",
		FreeAdmission:        false,
		FreeMonthlyFee:       false,
		DiscountedMonthlyFee: 2200,
	}); err != nil {
		return fmt.Errorf("update enrollment: %w", err)
	}

	updated, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		return fmt.Errorf("reload enrollment: %w", err)
	}
	if updated.EnrollmentDate != "2026-08-05" {
		return fmt.Errorf("enrollment date = %q, want 2026-08-05", updated.EnrollmentDate)
	}
	if updated.DiscountedMonthlyFee != 2200 {
		return fmt.Errorf("discounted monthly fee = %.2f, want 2200.00", updated.DiscountedMonthlyFee)
	}
	if updated.UpdatedAt.Before(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		return fmt.Errorf("updated_at was not refreshed: %s", updated.UpdatedAt)
	}

	var initialHistoryFrom string
	if err := db.QueryRow(`
		SELECT CAST(effective_from AS TEXT)
		FROM student_enrollment_status_history
		WHERE enrollment_id = $1
		  AND effective_to IS NULL
	`, enrollmentID).Scan(&initialHistoryFrom); err != nil {
		return fmt.Errorf("load enrollment history after service update: %w", err)
	}
	if initialHistoryFrom != "2026-08-05" {
		return fmt.Errorf("service update history effective_from = %q, want 2026-08-05", initialHistoryFrom)
	}

	form := url.Values{
		"csrf_token":             {"token"},
		"enrollment_id":          {fmt.Sprintf("%d", enrollmentID)},
		"training_program_id":    {fmt.Sprintf("%d", secondProgramID)},
		"enrollment_date":        {"2026-08-06"},
		"free_admission":         {"false"},
		"discounted_monthly_fee": {"2100"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:            501,
		Name:          "Postgres Enrollment Editor",
		Roles:         []string{"admin"},
		Permissions:   []string{"students.manage"},
		DivisionIDs:   []int64{sportsDivisionID},
		DivisionCodes: []string{divisionCodeSports},
	}))
	rec := httptest.NewRecorder()

	app.updateEnrollmentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		return fmt.Errorf("handler update status = %d body=%s", rec.Code, rec.Body.String())
	}

	updated, err = app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		return fmt.Errorf("reload enrollment after handler update: %w", err)
	}
	if updated.TrainingProgramID != secondProgramID {
		return fmt.Errorf("training program id = %d, want %d", updated.TrainingProgramID, secondProgramID)
	}
	if updated.EnrollmentDate != "2026-08-06" {
		return fmt.Errorf("handler enrollment date = %q, want 2026-08-06", updated.EnrollmentDate)
	}

	var handlerHistoryFrom string
	if err := db.QueryRow(`
		SELECT CAST(effective_from AS TEXT)
		FROM student_enrollment_status_history
		WHERE enrollment_id = $1
		  AND effective_to IS NULL
	`, enrollmentID).Scan(&handlerHistoryFrom); err != nil {
		return fmt.Errorf("load enrollment history after handler update: %w", err)
	}
	if handlerHistoryFrom != "2026-08-06" {
		return fmt.Errorf("handler update history effective_from = %q, want 2026-08-06", handlerHistoryFrom)
	}

	return nil
}
