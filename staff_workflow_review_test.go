package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestHourlyPayrollUsesStaffAttendanceSessions(t *testing.T) {
	app := newAuthorizationTestApp(t)
	staff, err := app.createManagedUser("Session Payroll", "session-payroll@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	date := "2026-08-02"
	if err := app.saveStaffAttendanceRecords(date, []StaffAttendanceInput{{UserID: staff.ID, Status: "present", Sessions: []StaffAttendanceSession{{"09:00", "10:30"}, {"14:00", "16:00"}}}}, staff.ID); err != nil {
		t.Fatal(err)
	}
	profile := StaffSalaryProfile{UserID: staff.ID, CompensationType: SalaryTypeHourly, Rate: 1000, EffectiveFrom: "2026-01-01", Active: true}
	snapshot, err := app.buildHourlyPayrollSnapshot(profile, "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Quantity != 3.5 || snapshot.BaseAmount != 3500 {
		t.Fatalf("incorrect hourly salary: %+v", snapshot)
	}
	now := time.Now().UTC()
	if _, err := app.db.Exec(`INSERT INTO staff_work_time_records (user_id,work_date,clock_in,clock_out,break_minutes,note,recorded_by_user_id,created_at,updated_at) VALUES (?,?,'09:00','10:30',0,'',?,?,?)`, staff.ID, date, staff.ID, now, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err = app.buildHourlyPayrollSnapshot(profile, "2026-08-01", "2026-08-31")
	if err != nil || snapshot.Quantity != 3.5 {
		t.Fatalf("duplicate hours counted: %+v, %v", snapshot, err)
	}
	if _, err := app.db.Exec("UPDATE staff_work_time_records SET clock_out='11:00' WHERE user_id=?", staff.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.buildHourlyPayrollSnapshot(profile, "2026-08-01", "2026-08-31"); err == nil {
		t.Fatal("overlapping sources must require reconciliation")
	}
}

func TestStaffFinancialInputsRejectNonFiniteAmounts(t *testing.T) {
	app := &App{}
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := validateStaffSalaryProfile(StaffSalaryProfile{UserID: 1, CompensationType: SalaryTypeHourly, Rate: amount, EffectiveFrom: "2026-01-01"}); err == nil {
			t.Error("invalid salary rate accepted")
		}
		if err := app.createStaffAdvance(StaffAdvance{UserID: 1, DivisionID: 1, FinanceAccountID: 1, Amount: amount, RecoveryMode: StaffAdvanceRecoveryDirect}, 1); err == nil {
			t.Error("invalid advance accepted")
		}
		if err := app.collectStaffAdvanceRepayment(1, amount, "cash", "", 1); err == nil {
			t.Error("invalid repayment accepted")
		}
	}
}

func TestStaffWorkspaceNavigationAndReadOnlyControls(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	user := &User{ID: 1, Name: "Staff viewer", Permissions: []string{"coaches.view", "payroll.view"}}
	data := TemplateData{User: user, SelectedDivisionScope: "all"}
	for _, page := range []string{"staff-directory", "staff-attendance", "staff-attendance-report", "payroll", "salary-profiles", "staff-advances"} {
		html := renderTemplateToString(t, templates, page, data)
		for _, path := range []string{"/admin/staff?division=all", "/admin/staff/attendance?division=all", "/admin/staff/attendance/report?division=all", "/admin/staff/salary-payments", "/admin/staff/salary-profiles", "/admin/staff/advances"} {
			if !strings.Contains(html, path) {
				t.Errorf("%s missing navigation %s", page, path)
			}
		}
		if page == "salary-profiles" && strings.Contains(html, `action="/admin/staff/salary-profiles/create"`) {
			t.Error("read-only user sees salary creation form")
		}
		if page == "payroll" && strings.Contains(html, `action="/admin/payroll/create"`) {
			t.Error("read-only user sees payroll creation form")
		}
	}
}
