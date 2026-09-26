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
