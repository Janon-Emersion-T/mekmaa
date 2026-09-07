package main

import "testing"

func TestGenerateStaffPayrollSeparately(t *testing.T) {
	app := newAuthorizationTestApp(t)
	first, err := app.createManagedUser("First Staff", "first-pay@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.createManagedUser("Second Staff", "second-pay@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{UserID: id, CompensationType: SalaryTypeMonthly, Rate: 1000, EffectiveFrom: "2026-01-01", Active: true}, first.ID)
	}
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "August", first.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		var count int
		if err := app.queryRowDB("SELECT COUNT(DISTINCT user_id) FROM payroll_payments WHERE payroll_run_id=?", runID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("staff count=%d want=%d", count, want)
		}
	}
	assertCount(1)
	if err := app.recalculatePayrollRun(runID, first.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	if _, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "August", first.ID, first.ID); err == nil {
		t.Fatal("duplicate generation accepted")
	}
	if _, err := app.execDB("UPDATE payroll_payments SET status = 'approved' WHERE payroll_run_id = ? AND user_id = ?", runID, first.ID); err != nil {
		t.Fatal(err)
	}
	secondRunID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "August", second.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondRunID != runID {
		t.Fatal("same period should retain its existing run")
	}
	assertCount(2)
}
