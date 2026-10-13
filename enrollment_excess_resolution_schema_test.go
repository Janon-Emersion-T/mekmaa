package main

import "testing"

func TestExcessResolutionAuditTableExists(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	var name string
	err := app.db.QueryRow(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table'
		  AND name = 'enrollment_excess_resolutions'
	`).Scan(&name)

	if err != nil {
		t.Fatal(err)
	}

	if name != "enrollment_excess_resolutions" {
		t.Fatalf("unexpected table: %s", name)
	}
}

func TestExcessResolutionAuditConstraints(t *testing.T) {
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

	var closureID int64
	err = app.db.QueryRow(`
		SELECT id
		FROM enrollment_financial_closures
		WHERE enrollment_id = ?
	`, enrollmentID).Scan(&closureID)
	if err != nil {
		t.Fatal(err)
	}

	// A retained excess must have a non-empty reason.
	_, err = app.db.Exec(`
		INSERT INTO enrollment_excess_resolutions
			(closure_id, enrollment_id, action, amount, reason)
		VALUES (?, ?, 'retain', 100, '')
	`, closureID, enrollmentID)
	if err == nil {
		t.Fatal("expected missing-reason constraint failure")
	}

	// A transfer must identify a destination enrollment.
	_, err = app.db.Exec(`
		INSERT INTO enrollment_excess_resolutions
			(closure_id, enrollment_id, action, amount)
		VALUES (?, ?, 'transfer', 100)
	`, closureID, enrollmentID)
	if err == nil {
		t.Fatal("expected missing-target constraint failure")
	}

	// Valid audit record.
	_, err = app.db.Exec(`
		INSERT INTO enrollment_excess_resolutions
			(closure_id, enrollment_id, action, amount, reason)
		VALUES (?, ?, 'retain', 100, 'Approved reclassification')
	`, closureID, enrollmentID)
	if err != nil {
		t.Fatal(err)
	}

	// The same financial closure must not be resolved twice.
	_, err = app.db.Exec(`
		INSERT INTO enrollment_excess_resolutions
			(closure_id, enrollment_id, action, amount, reason)
		VALUES (?, ?, 'retain', 100, 'Duplicate')
	`, closureID, enrollmentID)
	if err == nil {
		t.Fatal("duplicate financial resolution was accepted")
	}
}
