package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReportDomainFromRequestDefaultsAndAllowsKnownDomains(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"/admin/reports", reportDomainOverview},
		{"/admin/reports?domain=finance", reportDomainFinance},
		{"/admin/reports?domain=payroll", reportDomainPayroll},
		{"/admin/reports?domain=attendance", reportDomainAttendance},
		{"/admin/reports?domain=students", reportDomainStudents},
		{"/admin/reports?domain=unknown", reportDomainOverview},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.url, nil)
		if got := reportDomainFromRequest(req); got != tt.want {
			t.Fatalf("%s domain = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestReportsTemplatePreservesDivisionScopeAndDomainLinks(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}

	body := renderTemplateToString(t, templates, "reports", TemplateData{
		User:                  &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: []string{"reports.view"}},
		Title:                 "Reports",
		SelectedDivision:      &Division{ID: 2, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center"},
		SelectedDivisionScope: "kec",
		ReportCenter: &ReportCenter{
			Domain: reportDomainPayroll,
			Period: ReportPeriod{
				Kind:         "day",
				Anchor:       "2026-08-15",
				Label:        "Saturday, 15-Aug-2026",
				PreviousDate: "2026-08-14",
				NextDate:     "2026-08-16",
			},
			Domains: reportDomainOptions(),
			Payroll: &PayrollDomainReport{},
		},
	})

	for _, needle := range []string{
		"/admin/reports/export?date=2026-08-15&amp;division=kec&amp;domain=payroll&amp;period=day",
		"/admin/reports?date=2026-08-14&amp;division=kec&amp;domain=payroll&amp;period=day",
		"/admin/reports?date=2026-08-15&amp;division=kec&amp;domain=finance&amp;period=day",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected %q in template body %s", needle, body)
		}
	}
}

func TestReportsExportSupportsMultipleDomains(t *testing.T) {
	app := newAuthorizationTestApp(t)

	manager, err := app.createManagedUser("Report Manager", "report-manager@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	coach, err := app.createManagedUser("Report Coach", "report-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	programID := createPayrollTestProgram(t, app, "Reporting Program", "reporting-program")
	admissionID := createPayrollTestAdmission(t, app, "STD-REPORTING-001", "Reporting Student")
	createPayrollTestEnrollment(t, app, admissionID, programID, "2026-08-01")
	groupID := createPayrollTestGroup(t, app, "Reporting Group", "REPORTING-GRP", programID, []int64{admissionID}, []int64{coach.ID}, nil)

	if err := app.saveStaffAttendanceRecords("2026-08-10", []StaffAttendanceInput{{UserID: manager.ID, Status: "present"}}, manager.ID); err != nil {
		t.Fatalf("save staff attendance: %v", err)
	}
	now := time.Now().UTC()
	if _, err := app.db.Exec(`
		INSERT INTO attendance_records (
			group_id, admission_id, attendance_date, status, note, recorded_by_user_id, recorded_at, updated_at
		) VALUES (?, ?, ?, 'present', '', ?, ?, ?)
	`, groupID, admissionID, "2026-08-10", manager.ID, now, now); err != nil {
		t.Fatalf("insert student attendance: %v", err)
	}

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            coach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypePerStudent,
		Rate:              1000,
		StudentBasis:      SalaryStudentBasisGroupMembership,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, manager.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", manager.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, manager.ID); err != nil {
		t.Fatalf("generate payroll run: %v", err)
	}
	if payment := payrollPaymentForProfile(t, app, runID, profileID); payment.NetAmount != 1000 {
		t.Fatalf("unexpected payroll payment: %#v", payment)
	}

	for _, domain := range []string{
		reportDomainOverview,
		reportDomainFinance,
		reportDomainPayroll,
		reportDomainAttendance,
		reportDomainStudents,
	} {
		req := httptest.NewRequest("GET", "/admin/reports/export?domain="+domain+"&period=month&date=2026-08-15", nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
			ID:          manager.ID,
			Name:        manager.Name,
			Roles:       []string{"superadmin"},
			Permissions: []string{"reports.view", "reports.export"},
		}))
		rec := httptest.NewRecorder()

		app.reportsExportHandler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s export status = %d", domain, rec.Code)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Section,Field,Value") {
			t.Fatalf("%s export missing report headings: %s", domain, body)
		}
		switch domain {
		case reportDomainFinance:
			if !strings.Contains(body, "Gross income") || !strings.Contains(body, "Mekmaa Finance Report") {
				t.Fatalf("finance export missing metric rows: %s", body)
			}
		case reportDomainPayroll:
			if !strings.Contains(body, "COMPENSATION") || !strings.Contains(body, "Per student") || !strings.Contains(body, "Mekmaa Payroll Report") {
				t.Fatalf("payroll export missing payroll data: %s", body)
			}
		case reportDomainAttendance:
			if !strings.Contains(body, "STUDENT GROUPS") || !strings.Contains(body, "STAFF") || !strings.Contains(body, "Mekmaa Attendance Report") {
				t.Fatalf("attendance export missing attendance data: %s", body)
			}
		case reportDomainStudents:
			if !strings.Contains(body, "PROGRAMMES") || !strings.Contains(body, "Reporting Program") || !strings.Contains(body, "Mekmaa Students Report") {
				t.Fatalf("students export missing student data: %s", body)
			}
		default:
			if !strings.Contains(body, "SUMMARY") || !strings.Contains(body, "Mekmaa Overview Report") {
				t.Fatalf("overview export missing summary: %s", body)
			}
		}
	}
}
