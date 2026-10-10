package main

import (
	"fmt"
	"strings"
	"testing"
)

func createFinancialClosureTestEnrollment(t *testing.T, app *App) int64 {
	t.Helper()

	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Financial Closure Test Program",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2500,
		Active:         true,
	})
	if err != nil {
		t.Fatal(err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-FIN-CLOSE-001",
		FullName:              "Financial Closure Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000202",
	}, false, "cash", 0)
	if err != nil {
		t.Fatal(err)
	}

	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(
		StudentEnrollment{
			AdmissionID:       admissionID,
			TrainingProgramID: programID,
			EnrollmentDate:    "2026-08-01",
		},
		false,
		"cash",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	return enrollmentID
}

func TestFinancialClosureServiceArchivesEnrollment(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	err := app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 1200,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Active {
		t.Fatal("enrollment should be inactive")
	}

	var finalFee float64
	var effectiveDate string

	err = app.db.QueryRow(`
		SELECT final_monthly_fee, effective_date
		FROM enrollment_financial_closures
		WHERE enrollment_id = ?
	`, enrollmentID).Scan(&finalFee, &effectiveDate)
	if err != nil {
		t.Fatal(err)
	}

	if finalFee != 1200 || effectiveDate != "2026-08-20" {
		t.Fatalf("unexpected closure: fee=%.2f date=%s", finalFee, effectiveDate)
	}

	var inactiveHistoryCount int
	err = app.db.QueryRow(`
		SELECT COUNT(*)
		FROM student_enrollment_status_history
		WHERE enrollment_id = ?
		  AND active = 0
		  AND effective_from = ?
	`, enrollmentID, "2026-08-20").Scan(&inactiveHistoryCount)
	if err != nil {
		t.Fatal(err)
	}
	if inactiveHistoryCount != 1 {
		t.Fatalf("inactive history count = %d, want 1", inactiveHistoryCount)
	}
}

func TestFinancialClosureServiceRejectsDuplicate(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	request := EnrollmentFinancialClosureRequest{
		EnrollmentID:    enrollmentID,
		EffectiveDate:   "2026-08-20",
		FinalMonthlyFee: 1200,
	}

	if err := app.closeStudentEnrollmentFinancially(request); err != nil {
		t.Fatal(err)
	}

	if err := app.closeStudentEnrollmentFinancially(request); err == nil {
		t.Fatal("duplicate financial closure should fail")
	}

	var count int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM enrollment_financial_closures
		WHERE enrollment_id = ?
	`, enrollmentID).Scan(&count); err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Fatalf("closure count = %d, want 1", count)
	}
}

func TestFinancialClosureServiceRejectsExcessPayment(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID,
		"2026-08",
		monthDate,
		"cash",
		1000,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
		},
	)

	if err == nil || !strings.Contains(err.Error(), "excess payment") {
		t.Fatalf("expected excess payment rejection, got %v", err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("enrollment must remain active after rejection")
	}

	var closureCount int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM enrollment_financial_closures
		WHERE enrollment_id = ?
	`, enrollmentID).Scan(&closureCount); err != nil {
		t.Fatal(err)
	}
	if closureCount != 0 {
		t.Fatalf("closure count = %d, want 0", closureCount)
	}
}

func TestFinancialClosureServiceRollsBackOnHistoryFailure(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	// Simulate a database failure during status-history recording.
	_, err := app.db.Exec(`
		CREATE TRIGGER reject_financial_closure_history
		BEFORE INSERT ON student_enrollment_status_history
		WHEN NEW.enrollment_id = ` + fmt.Sprint(enrollmentID) + `
		BEGIN
			SELECT RAISE(ABORT, 'simulated history failure');
		END
	`)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 1200,
		},
	)
	if err == nil {
		t.Fatal("expected simulated database failure")
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("enrollment was archived despite transaction failure")
	}

	var count int
	err = app.db.QueryRow(`
		SELECT COUNT(*)
		FROM enrollment_financial_closures
		WHERE enrollment_id = ?
	`, enrollmentID).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("closure records = %d, want 0 after rollback", count)
	}
}

func TestFinancialClosureServicePreservesOriginalReceipt(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	transactionID, err := app.collectStudentMonthlyPaymentAmount(
		enrollmentID,
		"2026-08",
		monthDate,
		"cash",
		1000,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	var originalAmount float64
	var originalReceipt string

	err = app.db.QueryRow(`
		SELECT amount, receipt_number
		FROM finance_transactions
		WHERE id = ?
	`, transactionID).Scan(&originalAmount, &originalReceipt)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
		},
	)
	if err == nil {
		t.Fatal("expected excess-payment rejection")
	}

	var currentAmount float64
	var currentReceipt string

	err = app.db.QueryRow(`
		SELECT amount, receipt_number
		FROM finance_transactions
		WHERE id = ?
	`, transactionID).Scan(&currentAmount, &currentReceipt)
	if err != nil {
		t.Fatal(err)
	}

	if currentAmount != originalAmount ||
		currentReceipt != originalReceipt {
		t.Fatal("original finance receipt was modified")
	}

	var paymentAmount float64
	var voided int

	err = app.db.QueryRow(`
		SELECT amount, COALESCE(voided, 0)
		FROM student_monthly_payments
		WHERE finance_transaction_id = ?
	`, transactionID).Scan(&paymentAmount, &voided)
	if err != nil {
		t.Fatal(err)
	}

	if paymentAmount != 1000 || voided != 0 {
		t.Fatalf(
			"original payment changed: amount=%.2f voided=%d",
			paymentAmount,
			voided,
		)
	}
}

func TestFinancialClosureServiceRejectsRetainWithoutReason(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID, "2026-08", monthDate, "cash", 1000, 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
			ExcessAction:    ExcessPaymentRetain,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("expected mandatory reason validation, got %v", err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("invalid decision archived enrollment")
	}
}

func TestFinancialClosureServiceRejectsTransferWithoutTarget(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID, "2026-08", monthDate, "cash", 1000, 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
			ExcessAction:    ExcessPaymentTransfer,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "another enrollment") {
		t.Fatalf("expected transfer target validation, got %v", err)
	}
}

func TestFinancialClosureServiceDoesNotPrematurelyResolveExcess(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)

	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID, "2026-08", monthDate, "cash", 1000, 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
			ExcessAction:    ExcessPaymentRetain,
			ExcessReason:    "Approved additional income",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "requires revenue allocation integration") {
		t.Fatalf("expected safe resolution guard, got %v", err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("unimplemented resolution archived enrollment")
	}
}

func TestFinancialClosureServiceRejectsUnimplementedRefund(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)
	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID, "2026-08", monthDate, "cash", 1000, 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:    enrollmentID,
			EffectiveDate:   "2026-08-20",
			FinalMonthlyFee: 500,
			ExcessAction:    ExcessPaymentRefund,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "requires cash-account integration") {
		t.Fatalf("expected refund safety guard, got %v", err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("unimplemented refund archived enrollment")
	}
}

func TestFinancialClosureServiceRejectsUnimplementedTransfer(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.runtimeConfig.DBDriver = databaseDriverSQLite

	enrollmentID := createFinancialClosureTestEnrollment(t, app)
	monthDate, err := parsePaymentMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.collectStudentMonthlyPaymentAmount(
		enrollmentID, "2026-08", monthDate, "cash", 1000, 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = app.closeStudentEnrollmentFinancially(
		EnrollmentFinancialClosureRequest{
			EnrollmentID:       enrollmentID,
			EffectiveDate:      "2026-08-20",
			FinalMonthlyFee:    500,
			ExcessAction:       ExcessPaymentTransfer,
			TargetEnrollmentID: enrollmentID + 1,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "requires target-payment integration") {
		t.Fatalf("expected transfer safety guard, got %v", err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("unimplemented transfer archived enrollment")
	}
}
