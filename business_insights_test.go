package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBusinessInsightsBuilderExportAndTemplate(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	anchor := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.Local)
	if _, err := app.createManualFinanceTransaction("manual_income", "Sponsor", "August sponsorship", "bank_transfer", 10000, anchor, 0); err != nil {
		t.Fatalf("create current income: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("utilities_expense", "Utility provider", "August utilities", "cash", -2500, anchor, 0); err != nil {
		t.Fatalf("create current expense: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("manual_income", "Sponsor", "July sponsorship", "bank_transfer", 7000, anchor.AddDate(0, -1, 0), 0); err != nil {
		t.Fatalf("create previous income: %v", err)
	}

	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Insights Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     4000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "INSIGHT-001",
		FullName:              "Insight Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771234500",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
		EnrollmentDate:    "2026-08-01",
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}

	staff, err := app.createUser("Insight Coach", "insight-coach@example.com", "SecurePass123!")
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}
	runID, err := app.insertAndReturnID(`
		INSERT INTO payroll_runs (period_start, period_end, label, status, created_at, updated_at)
		VALUES ('2026-08-01', '2026-08-31', 'August payroll', 'calculated', ?, ?)
	`, anchor, anchor)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if _, err := app.execDB(`
		INSERT INTO payroll_payments (
			payroll_run_id, user_id, compensation_type, rate_snapshot, quantity, base_amount,
			additions_total, deductions_total, net_amount, status, created_at, updated_at
		) VALUES (?, ?, 'monthly', 3000, 1, 3000, 0, 0, 3000, 'approved', ?, ?)
	`, runID, staff.ID, anchor, anchor); err != nil {
		t.Fatalf("create payroll payment: %v", err)
	}

	scheduleID, err := app.insertAndReturnID(`
		INSERT INTO space_schedules (
			slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
			requester_name, requester_email, requester_phone, created_at, updated_at
		) VALUES ('2026-08-20', '10:00', 'booking', 'badminton', 1, 'Insight pending booking', '', 'pending',
			'Pipeline Customer', 'pipeline@example.com', '0770000000', ?, ?)
	`, anchor, anchor)
	if err != nil {
		t.Fatalf("create pending schedule: %v", err)
	}
	if _, err := app.execDB(`
		INSERT INTO booking_financials (schedule_id, quoted_amount, paid, created_at, updated_at)
		VALUES (?, 5000, 0, ?, ?)
	`, scheduleID, anchor, anchor); err != nil {
		t.Fatalf("create booking financial: %v", err)
	}

	user := &User{Name: "Superadmin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: allPermissions}
	insights, err := app.buildBusinessInsights(user, nil, nil, anchor)
	if err != nil {
		t.Fatalf("build business insights: %v", err)
	}
	if insights.StudentOutstanding != 4000 {
		t.Fatalf("student outstanding = %.2f, want 4000.00", insights.StudentOutstanding)
	}
	if insights.PayrollCommitments != 3000 {
		t.Fatalf("payroll commitments = %.2f, want 3000.00", insights.PayrollCommitments)
	}
	if insights.BookingPipeline != 5000 {
		t.Fatalf("booking pipeline = %.2f, want 5000.00", insights.BookingPipeline)
	}
	if len(insights.Forecasts) < 4 || !strings.Contains(insights.Forecasts[0].Driver, "booking pipeline") {
		t.Fatalf("forecast inputs not reflected: %#v", insights.Forecasts)
	}

	recorder := httptest.NewRecorder()
	if err := writeBusinessInsightsCSV(recorder, insights); err != nil {
		t.Fatalf("write business insights csv: %v", err)
	}
	csvBody := recorder.Body.String()
	for _, want := range []string{"Mekmaa Business Insights", "STRATEGIC INPUT", "Student receivables", "Payroll commitments", "Booking pipeline value"} {
		if !strings.Contains(csvBody, want) {
			t.Fatalf("business insights csv missing %q in %s", want, csvBody)
		}
	}

	data := TemplateData{
		User:             user,
		BusinessInsights: insights,
	}
	if err := templates["business-insights"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render business insights template: %v", err)
	}
}

func TestBusinessInsightsHandlersRenderPDFAndCSV(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	user := &User{ID: 500, Name: "Superadmin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: allPermissions}

	req := httptest.NewRequest(http.MethodGet, "/admin/business-insights?format=pdf&date=2026-08-15", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.businessInsightsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("business insights pdf status = %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Business Insights") || !strings.Contains(body, "window.print") {
		t.Fatalf("business insights pdf body missing print view markers: %s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/business-insights/export?date=2026-08-15", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec = httptest.NewRecorder()
	app.businessInsightsExportHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("business insights export status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/csv") {
		t.Fatalf("business insights export content type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "Mekmaa Business Insights") {
		t.Fatalf("business insights export missing title: %s", rec.Body.String())
	}
}

func TestBusinessBreakdownBuilderHandlerAndTemplate(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	anchor := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.Local)
	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Breakdown Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   6000,
		MonthlyFee:     4000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	if _, _, err := app.createAdmissionWithOptionalPaymentAt(Admission{
		StudentID:             "BREAK-001",
		FullName:              "Breakdown Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		TrainingProgramID:     programID,
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771234500",
	}, true, "cash", anchor, 0); err != nil {
		t.Fatalf("create paid admission: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("staff_salary_expense", "Coach", "August salary", "cash", -2500, anchor, 0); err != nil {
		t.Fatalf("create salary expense: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("manual_income", "Sponsor", "July sponsorship", "cash", 2000, anchor.AddDate(0, -1, 0), 0); err != nil {
		t.Fatalf("create previous income: %v", err)
	}

	user := &User{ID: 501, Name: "Superadmin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: allPermissions}
	breakdown, err := app.buildBusinessBreakdown(user, nil, nil, "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("build business breakdown: %v", err)
	}
	if !moneyEquals(breakdown.TotalRevenue, 6000) {
		t.Fatalf("breakdown revenue = %.2f, want 6000.00", breakdown.TotalRevenue)
	}
	if !moneyEquals(breakdown.TotalExpenses, 2500) {
		t.Fatalf("breakdown expenses = %.2f, want 2500.00", breakdown.TotalExpenses)
	}
	if !strings.Contains(breakdown.ExecutiveSummary, "largest income line") {
		t.Fatalf("summary missing management signal: %s", breakdown.ExecutiveSummary)
	}
	if len(breakdown.RevenueLines) == 0 || breakdown.RevenueLines[0].Label != "Admission payment" {
		t.Fatalf("unexpected revenue lines: %#v", breakdown.RevenueLines)
	}
	if len(breakdown.ExpenseLines) == 0 || breakdown.ExpenseLines[0].Label != "Staff salary" {
		t.Fatalf("unexpected expense lines: %#v", breakdown.ExpenseLines)
	}
	if len(breakdown.SourceLines) == 0 || breakdown.SourceLines[0].Label != "Admissions" {
		t.Fatalf("unexpected source lines: %#v", breakdown.SourceLines)
	}

	data := TemplateData{
		User:              user,
		BusinessBreakdown: breakdown,
	}
	if err := templates["business-breakdown"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render business breakdown template: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/business-insights/breakdown?from=2026-08-01&to=2026-08-31", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.businessBreakdownHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("business breakdown status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Business Breakdown", "Where money is earned", "Staff salary", "Admissions"} {
		if !strings.Contains(body, want) {
			t.Fatalf("business breakdown body missing %q in %s", want, body)
		}
	}
}

func TestBusinessBreakdownDetailShowsStudentPaymentMonth(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Monthly Breakdown Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   0,
		MonthlyFee:     4000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "BREAK-MONTH-001",
		FullName:              "Collected Later Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771234500",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
		EnrollmentDate:    "2026-07-01",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	julyDate, _ := parsePaymentMonth("2026-07")
	collectedAt := time.Date(2026, time.September, 5, 10, 30, 0, 0, time.Local)
	if _, err := app.collectStudentMonthlyPaymentAmountAt(enrollmentID, "2026-07", julyDate, "cash", 4000, collectedAt, 0); err != nil {
		t.Fatalf("collect July payment in September: %v", err)
	}

	user := &User{ID: 502, Name: "Superadmin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: allPermissions}
	breakdown, err := app.buildBusinessBreakdown(user, nil, nil, "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatalf("build business breakdown: %v", err)
	}
	if len(breakdown.RevenueLines) == 0 || breakdown.RevenueLines[0].Href == "" || strings.Contains(breakdown.RevenueLines[0].Href, "/admin/finance/ledger") {
		t.Fatalf("monthly payment breakdown should link to detail page, got %#v", breakdown.RevenueLines)
	}
	detail, err := app.buildBusinessBreakdownDetail(user, nil, nil, "2026-09-01", "2026-09-30", "income", "student_monthly_payment")
	if err != nil {
		t.Fatalf("build business breakdown detail: %v", err)
	}
	if detail.EntryCount != 1 || len(detail.Rows) != 1 {
		t.Fatalf("unexpected detail rows: %#v", detail)
	}
	if detail.Rows[0].PaymentForMonth != "2026-07" || detail.Rows[0].PaymentForLabel != "July 2026" {
		t.Fatalf("payment month detail = %#v, want July 2026", detail.Rows[0])
	}
	if len(detail.PaymentMonthMix) != 1 || detail.PaymentMonthMix[0].Label != "July 2026" {
		t.Fatalf("payment month mix = %#v, want July 2026", detail.PaymentMonthMix)
	}

	data := TemplateData{
		User:                    user,
		BusinessBreakdownDetail: detail,
	}
	if err := templates["business-breakdown-detail"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render business breakdown detail template: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/business-insights/breakdown/detail?from=2026-09-01&to=2026-09-30&type=income&key=student_monthly_payment", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.businessBreakdownDetailHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("business breakdown detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Student monthly payment", "July 2026", "Collected Later Student", "Which months these collections paid for"} {
		if !strings.Contains(body, want) {
			t.Fatalf("business breakdown detail body missing %q in %s", want, body)
		}
	}
}
