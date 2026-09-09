package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestStaffSalaryWorkspaceLifecycle(t *testing.T) {
	app := newAuthorizationTestApp(t)
	staff, err := app.createManagedUser("Workspace Staff", "workspace-staff@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.createManagedUser("Second Workspace Staff", "workspace-second@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	divisionID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatal(err)
	}
	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{UserID: staff.ID, DivisionID: divisionID, CompensationType: SalaryTypeMonthly, Rate: 1000, EffectiveFrom: "2026-01-01", Active: true}, staff.ID)
	createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{UserID: second.ID, DivisionID: divisionID, CompensationType: SalaryTypeMonthly, Rate: 2000, EffectiveFrom: "2026-01-01", Active: true}, staff.ID)
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "First staff August", staff.ID, staff.ID)
	if err != nil {
		t.Fatal(err)
	}
	payment := payrollPaymentForProfile(t, app, runID, profileID)
	accountID, err := app.createFinanceAccount(divisionID, "", "Workspace Bank", financeAccountTypeBank, "", staff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.payPayrollPayment(payment.ID, accountID, "PAY-001", staff.ID); err == nil {
		t.Fatal("payment accepted before approval")
	}
	if err := app.addPayrollAdjustment(payment.ID, PayrollAdjustmentBonus, PayrollDirectionAddition, "Extra sessions", 100, staff.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.approvePayrollPayment(payment.ID, staff.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.payPayrollPayment(payment.ID, accountID, "PAY-001", staff.ID); err != nil {
		t.Fatal(err)
	}
	for _, period := range [][2]string{{"2026-08-01", "2026-08-31"}, {"2026-08-15", "2026-09-15"}, {"2026-07-15", "2026-08-01"}, {"2026-08-10", "2026-08-11"}} {
		if _, err := app.generateStaffPayroll(period[0], period[1], "Duplicate", staff.ID, staff.ID); err == nil {
			t.Fatalf("accepted paid overlap %v", period)
		}
	}
	var runCount int
	if err := app.queryRowDB("SELECT COUNT(*) FROM payroll_runs").Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("rejected generation created empty periods: %d, %v", runCount, err)
	}
	// Another staff member can still receive salary for the same dates.
	if _, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Second staff August", second.ID, staff.ID); err != nil {
		t.Fatal(err)
	}
	periods, err := app.listStaffSalaryPeriods([]User{*staff, *second})
	if err != nil || len(periods) != 2 {
		t.Fatalf("periods: %+v %v", periods, err)
	}
	labels := map[int64]string{}
	for _, period := range periods {
		labels[period.UserID] = period.Label
	}
	if labels[staff.ID] != "First staff August" || labels[second.ID] != "Second staff August" {
		t.Fatalf("staff labels lost: %v", labels)
	}
	paid, slipRun, err := app.findPayrollPaymentByID(payment.ID)
	if err != nil || paid.Status != PayrollPaymentStatusPaid || paid.NetAmount != 1100 || slipRun.Label != "First staff August" {
		t.Fatalf("paid salary: %+v, %+v, %v", paid, slipRun, err)
	}
	req := httptest.NewRequest(http.MethodGet, strings.Split(salaryWorkspaceURL(runID, staff.ID), "#")[0], nil)
	data := TemplateData{}
	rec := httptest.NewRecorder()
	if !app.loadSalaryWorkspace(rec, req, &data, []User{*staff, *second}) {
		t.Fatal(rec.Body.String())
	}
	if len(data.PayrollPayments) != 1 || data.PayrollPayments[0].UserID != staff.ID {
		t.Fatalf("review included other staff: %+v", data.PayrollPayments)
	}
	data = TemplateData{}
	rec = httptest.NewRecorder()
	if app.loadSalaryWorkspace(rec, req, &data, []User{*second}) || rec.Code != http.StatusForbidden {
		t.Fatal("review exposed staff outside visible scope")
	}
	if _, err := app.generateStaffPayroll("2026-09-01", "2026-09-30", "Next month", staff.ID, staff.ID); err != nil {
		t.Fatalf("adjacent unpaid month rejected: %v", err)
	}
}

func TestStaffSalaryPeriodValidationAndReturn(t *testing.T) {
	for _, dates := range [][2]string{{"", ""}, {"invalid", "2026-09-30"}, {"2026-09-30", "2026-09-01"}, {"2026-02-30", "2026-03-01"}} {
		if validateStaffPayrollDates(dates[0], dates[1]) == nil {
			t.Fatalf("accepted invalid dates: %v", dates)
		}
	}
	form := url.Values{"return_to": {"salary-workspace"}, "workspace_staff_id": {"5"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/payroll/payment/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := payrollActionReturnURL(req, 8); got != salaryWorkspaceURL(8, 5) {
		t.Fatalf("return URL: %s", got)
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/payroll/pay", nil)
	if got := payrollActionReturnURL(req, 8); got != "/admin/payroll/run?id=8" {
		t.Fatalf("legacy return URL: %s", got)
	}
}

func TestStaffSalaryWorkspaceTemplate(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, canEdit := range []bool{false, true} {
		user := &User{ID: 1, Permissions: []string{"payroll.view"}}
		if canEdit {
			user.Permissions = append(user.Permissions, "payroll.create", "payroll.update")
		}
		data := TemplateData{
			User: user, SalaryWorkspace: true, SelectedSalaryStaffID: 5, CSRFToken: "csrf",
			Users:           []User{{ID: 5, Name: "Test staff", Active: true}},
			PayrollRun:      &PayrollRun{ID: 8, Label: "September", Status: PayrollRunStatusCalculated},
			PayrollPayments: []PayrollPayment{{ID: 9, UserID: 5, UserName: "Test staff", Status: PayrollPaymentStatusCalculated, NetAmount: 1000}},
		}
		html := renderTemplateToString(t, templates, "payroll", data)
		if !strings.Contains(html, "Review, approve and pay") {
			t.Fatal("review section missing")
		}
		for _, action := range []string{"/admin/payroll/generate-staff", "/admin/payroll/adjustment/add", "/admin/payroll/payment/approve"} {
			if strings.Contains(html, "action=\""+action+"\"") != canEdit {
				t.Fatalf("wrong permission for %s", action)
			}
		}
		if canEdit && !strings.Contains(html, "name=\"workspace_staff_id\" value=\""+strconv.Itoa(5)+"\"") {
			t.Fatal("review forms lose selected staff")
		}
	}
}
