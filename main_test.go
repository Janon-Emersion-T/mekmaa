package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCollectStudentMonthlyPayment(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	if err := templates["student-payments"].ExecuteTemplate(io.Discard, "base", TemplateData{
		User: &User{Name: "Test Admin", Email: "admin@example.com"},
		StudentPaymentRows: []StudentPaymentRow{{
			Admission:  Admission{ID: 1, StudentID: "STD-TEST", FullName: "Test Student", PracticeType: "group_practice"},
			MonthlyFee: 0,
		}},
		PaymentMonth:      "2026-07",
		PaymentMonthLabel: "July 2026",
		TodayDate:         "2026-07",
	}); err != nil {
		t.Fatalf("render student payments template: %v", err)
	}

	db, err := sql.Open("sqlite", "file:student-payment-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE admission_pricing
		SET monthly_fee = 7500
		WHERE practice_type = 'group_practice'
	`); err != nil {
		t.Fatalf("configure pricing: %v", err)
	}

	now := time.Now().UTC()
	result, err := db.Exec(`
		INSERT INTO admissions (
			student_id, full_name, admission_date, date_of_birth, gender, practice_type, address,
			passport_number, school, guardian_name, guardian_relationship, guardian_contact_number,
			guardian_alternative_contact_number, medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"STD-TEST", "Test Student", "2026-01-15", "2012-05-10", "male", "group_practice",
		"Test address", "P-TEST", "Test school", "Test guardian", "Parent", "0700000000",
		"0710000000", "None", now, now,
	)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	admissionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	app := &App{db: db}
	monthDate, _ := parsePaymentMonth("2026-07")
	transactionID, err := app.collectStudentMonthlyPayment(admissionID, "2026-07", monthDate, "cash", 0)
	if err != nil {
		t.Fatalf("collect payment: %v", err)
	}
	if transactionID <= 0 {
		t.Fatal("expected a finance transaction")
	}

	var category string
	var amount float64
	if err := db.QueryRow(`
		SELECT category, amount
		FROM finance_transactions
		WHERE id = ?
	`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatalf("find finance transaction: %v", err)
	}
	if category != "student_monthly_payment" || amount != 7500 {
		t.Fatalf("unexpected transaction: category=%q amount=%.2f", category, amount)
	}

	rows, err := app.listStudentPaymentRows("2026-07")
	if err != nil {
		t.Fatalf("list student payment rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Payment == nil || rows[0].Payment.Amount != 7500 {
		t.Fatalf("monthly register did not include collected payment: %#v", rows)
	}

	_, err = app.collectStudentMonthlyPayment(admissionID, "2026-07", monthDate, "card", 0)
	if !errors.Is(err, ErrStudentPaymentAlreadyCollected) {
		t.Fatalf("expected duplicate payment error, got %v", err)
	}

	var transactionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finance_transactions`).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 1 {
		t.Fatalf("duplicate attempt created a transaction; count=%d", transactionCount)
	}
}

func TestBookingSystemTemplatesRender(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	settings := &PricingSettings{PeakStartHour: "17:00", PeakEndHour: "21:00"}
	pricings := []PricingRule{{
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		WeekdayOffPeak: 5000,
		WeekdayPeak:    7000,
		WeekendOffPeak: 6000,
		WeekendPeak:    8000,
	}}
	request := SpaceSchedule{
		ID:             12,
		SlotDate:       futureDate,
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Template Test",
		Status:         "pending",
		RequesterName:  "Test Customer",
		RequesterEmail: "customer@example.com",
		RequesterPhone: "0700000000",
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now().Add(-time.Hour),
	}
	common := TemplateData{
		User:            &User{Name: "Test Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		CSRFToken:       "test-token",
		CalendarDate:    futureDate,
		TodayDate:       time.Now().Format("2006-01-02"),
		PreviousDate:    time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		NextDate:        time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		Hours:           []string{"18:00"},
		Activities:      bookingActivities(),
		Pricings:        pricings,
		PricingSettings: settings,
		WeekDays: []CalendarDay{{
			Date: futureDate, DayLabel: "Fri", MonthLabel: "Jul", DayNumber: "31", OpenSlotCount: 1, IsSelected: true,
		}},
		BookingSlots: []BookingSlotAvailability{{
			Hour: "18:00", Options: []BookingOption{{Activity: "full_indoor_cricket", Quantity: 1}},
		}},
		DailyStats:          []Stat{{Label: "Open hours", Value: "1"}},
		BookingRequestStats: buildBookingRequestStats([]SpaceSchedule{request}),
		BookingRequests:     []SpaceSchedule{request},
		PendingSchedules:    []SpaceSchedule{request},
	}

	for _, name := range []string{"book", "booking-management", "booking-requests"} {
		data := common
		if name == "book" {
			data.User = nil
		}
		if err := templates[name].ExecuteTemplate(io.Discard, "base", data); err != nil {
			t.Fatalf("render %s template: %v", name, err)
		}
	}
}

func TestBookingRequestsPreventPastAndConflictingSlots(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-system-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	past := SpaceSchedule{SlotDate: time.Now().Add(-time.Hour).Format("2006-01-02"), SlotHour: "06:00"}
	if err := validateBookableScheduleTime(past, time.Now()); err == nil {
		t.Fatal("expected a past booking slot to be rejected")
	}
	days := buildBookingWeekDays(nil, time.Now(), bookingHours())
	if len(days) != 7 || days[0].IsPast {
		t.Fatalf("expected a forward-looking seven-day calendar, got %#v", days)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	request := SpaceSchedule{
		SlotDate:       futureDate,
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "First Request",
		Status:         "pending",
		RequesterName:  "First Customer",
		RequesterEmail: "first@example.com",
		RequesterPhone: "0700000000",
	}
	app := &App{db: db}
	requestID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create first booking request: %v", err)
	}
	if bookingReference(requestID) != "BK-000001" {
		t.Fatalf("unexpected booking reference: %s", bookingReference(requestID))
	}

	request.Title = "Conflicting Request"
	request.RequesterEmail = "second@example.com"
	if _, err := app.createPublicBookingRequest(request); err == nil {
		t.Fatal("expected conflicting booking request to be rejected")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM space_schedules`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflicting request was persisted; count=%d", count)
	}
}

func TestPublicBookingShowsVacantSlotsWithoutConfiguredPricing(t *testing.T) {
	db, err := sql.Open("sqlite", "file:public-booking-availability-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	request := httptest.NewRequest("GET", "/book?date="+futureDate, nil)
	recorder := httptest.NewRecorder()
	app := &App{db: db}
	data, err := app.buildPublicBookingData(recorder, request, nil)
	if err != nil {
		t.Fatalf("build public booking data: %v", err)
	}
	if len(data.BookingSlots) != len(bookingHours()) {
		t.Fatalf("expected all operating hours, got %d", len(data.BookingSlots))
	}
	for _, slot := range data.BookingSlots {
		if len(slot.Options) == 0 {
			t.Fatalf("vacant slot %s was hidden because pricing is not configured", slot.Hour)
		}
	}
	if bookingOpenHourCount(data.BookingSlots) != len(bookingHours()) {
		t.Fatalf("expected every vacant hour to be bookable")
	}
}

func TestReferralCommissionLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", "file:referral-commission-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	app := &App{db: db}
	if err := app.updateReferralCommissionAmount(500); err != nil {
		t.Fatalf("configure referral commission: %v", err)
	}
	partner := ReferralPartner{Name: "Referral Partner", Code: "COACH-01", Phone: "0700000000", Active: true}
	if err := app.createReferralPartner(partner); err != nil {
		t.Fatalf("create referral partner: %v", err)
	}

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "19:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Referral Booking",
		Status:         "pending",
		RequesterName:  "Referred Customer",
		RequesterEmail: "referred@example.com",
		RequesterPhone: "0710000000",
		ReferralCode:   "COACH-01",
	}
	requestID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create referred booking: %v", err)
	}
	referrals, err := app.listBookingReferrals()
	if err != nil || len(referrals) != 1 {
		t.Fatalf("list booking referrals: referrals=%#v err=%v", referrals, err)
	}
	if referrals[0].CommissionAmount != 500 || referrals[0].BookingStatus != "pending" {
		t.Fatalf("unexpected referral snapshot: %#v", referrals[0])
	}
	invalidReferral := request
	invalidReferral.SlotHour = "20:00"
	invalidReferral.ReferralCode = "UNKNOWN"
	if _, err := app.createPublicBookingRequest(invalidReferral); err == nil {
		t.Fatal("expected an unknown referral code to be rejected")
	}
	var scheduleCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM space_schedules`).Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if scheduleCount != 1 {
		t.Fatalf("invalid referral request was not rolled back; count=%d", scheduleCount)
	}
	if _, err := app.payReferralCommission(referrals[0].ID, "cash", 0); err == nil {
		t.Fatal("expected pending referral commission payment to be rejected")
	}
	if err := app.updateBookingRequestStatus(requestID, "confirmed", ""); err != nil {
		t.Fatalf("confirm referred booking: %v", err)
	}
	transactionID, err := app.payReferralCommission(referrals[0].ID, "bank_transfer", 0)
	if err != nil {
		t.Fatalf("pay referral commission: %v", err)
	}
	if _, err := app.payReferralCommission(referrals[0].ID, "cash", 0); err == nil {
		t.Fatal("expected duplicate referral commission payment to be rejected")
	}

	var category string
	var amount float64
	if err := db.QueryRow(`SELECT category, amount FROM finance_transactions WHERE id = ?`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatal(err)
	}
	if category != "referral_commission_payment" || amount != -500 {
		t.Fatalf("unexpected referral finance transaction: category=%q amount=%.2f", category, amount)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	referrals, err = app.listBookingReferrals()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:             &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		BookingReferrals: referrals,
		ReferralStats:    buildReferralStats(referrals),
		CSRFToken:        "test-token",
	}
	if err := templates["referral-commissions"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render referral commissions: %v", err)
	}
}

func TestFinanceBookingAndManualTransactionLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-system-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	app := &App{db: db}

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "20:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Finance Test Booking",
		RequesterName:  "Finance Customer",
		RequesterEmail: "finance@example.com",
		RequesterPhone: "0700000000",
		QuotedPrice:    7250,
	}
	scheduleID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create booking receivable: %v", err)
	}
	var quotedAmount float64
	if err := db.QueryRow(`SELECT quoted_amount FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&quotedAmount); err != nil {
		t.Fatalf("find booking price snapshot: %v", err)
	}
	if quotedAmount != 7250 {
		t.Fatalf("unexpected booking price snapshot: %.2f", quotedAmount)
	}
	if _, err := app.collectBookingPayment(scheduleID, "cash", 0); err == nil {
		t.Fatal("expected pending booking collection to be rejected")
	}
	if err := app.updateBookingRequestStatus(scheduleID, "confirmed", ""); err != nil {
		t.Fatalf("confirm booking: %v", err)
	}
	transactionID, err := app.collectBookingPayment(scheduleID, "card", 0)
	if err != nil {
		t.Fatalf("collect booking payment: %v", err)
	}
	var category string
	var amount float64
	if err := db.QueryRow(`SELECT category, amount FROM finance_transactions WHERE id = ?`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatal(err)
	}
	if category != "booking_payment" || amount != 7250 {
		t.Fatalf("unexpected booking transaction: category=%q amount=%.2f", category, amount)
	}
	if _, err := app.collectBookingPayment(scheduleID, "cash", 0); !errors.Is(err, ErrBookingPaymentAlreadyCollected) {
		t.Fatalf("expected duplicate booking payment error, got %v", err)
	}

	expenseID, err := app.createManualFinanceTransaction(
		"utilities_expense", "Electricity Board", "July electricity", "bank_transfer", -3200, time.Now(), 0,
	)
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}
	var expenseAmount float64
	if err := db.QueryRow(`SELECT amount FROM finance_transactions WHERE id = ?`, expenseID).Scan(&expenseAmount); err != nil {
		t.Fatal(err)
	}
	if expenseAmount != -3200 {
		t.Fatalf("expense sign was not preserved: %.2f", expenseAmount)
	}
	expenses, err := app.listFinanceTransactionsFiltered(FinanceFilter{Direction: "expense", Search: "electricity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(expenses) != 1 || expenses[0].ID != expenseID {
		t.Fatalf("unexpected filtered ledger: %#v", expenses)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:                &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		CSRFToken:           "test-token",
		TodayDate:           time.Now().Format("2006-01-02"),
		FinanceTransactions: expenses,
		FinanceSummary:      FinanceSummary{GrossIncome: 7250, TotalExpenses: 3200, NetCash: 4050},
	}
	if err := templates["finance-management"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render finance template: %v", err)
	}
}

func TestOperationalReportsAndCSVExport(t *testing.T) {
	db, err := sql.Open("sqlite", "file:operational-report-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	app := &App{db: db}
	reportDate := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)

	if _, err := app.createManualFinanceTransaction("manual_income", "Sponsor", "Community sponsorship", "bank_transfer", 10000, reportDate, 0); err != nil {
		t.Fatalf("create report income: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("utilities_expense", "Utility provider", "Electricity", "cash", -2000, reportDate, 0); err != nil {
		t.Fatalf("create report expense: %v", err)
	}
	now := time.Now().UTC()
	admissionResult, err := db.Exec(`
		INSERT INTO admissions (
			student_id, full_name, admission_date, date_of_birth, gender, practice_type, address,
			passport_number, school, guardian_name, guardian_relationship, guardian_contact_number,
			guardian_alternative_contact_number, medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "STD-REPORT", "Report Student", "2026-07-15", "2012-05-10", "male", "group_practice",
		"Address", "P-REPORT", "School", "Guardian", "Parent", "0700000000", "0710000000", "None", now, now)
	if err != nil {
		t.Fatalf("create report admission: %v", err)
	}
	admissionID, _ := admissionResult.LastInsertId()
	groupResult, err := db.Exec(`
		INSERT INTO student_groups (name, code, description, created_at, updated_at)
		VALUES ('Report Group', 'REPORT', 'Report test group', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	groupID, _ := groupResult.LastInsertId()
	for index, status := range []string{"present", "absent"} {
		if _, err := db.Exec(`
			INSERT INTO attendance_records (group_id, admission_id, attendance_date, status, note, recorded_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, ?)
		`, groupID, admissionID, fmt.Sprintf("2026-07-%02d", 15+index), status, now, now); err != nil {
			t.Fatalf("create attendance record: %v", err)
		}
	}
	for index, status := range []string{"confirmed", "pending"} {
		if _, err := db.Exec(`
			INSERT INTO space_schedules (
				slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
				requester_name, requester_email, requester_phone, review_note, created_at, updated_at
			) VALUES ('2026-07-15', ?, 'booking', 'badminton', 1, 'Report booking', '', ?, 'Customer', 'customer@example.com', '0700000000', '', ?, ?)
		`, fmt.Sprintf("%02d:00", 10+index), status, now, now); err != nil {
			t.Fatalf("create report booking: %v", err)
		}
	}

	request := httptest.NewRequest("GET", "/admin/reports?period=week&date=2026-07-15", nil)
	period := reportPeriodFromRequest(request)
	if period.Start != "2026-07-13" || period.End != "2026-07-19" {
		t.Fatalf("unexpected weekly period: %#v", period)
	}
	report, err := app.buildOperationalReport(period)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	if report.Summary.Income != 10000 || report.Summary.Expenses != 2000 || report.Summary.NetCash != 8000 {
		t.Fatalf("unexpected cash summary: %#v", report.Summary)
	}
	if report.Summary.ConfirmedBookings != 1 || report.Summary.PendingBookings != 1 || report.Summary.NewAdmissions != 1 {
		t.Fatalf("unexpected operations summary: %#v", report.Summary)
	}
	if report.Summary.AttendancePresent != 1 || report.Summary.AttendanceTotal != 2 || report.Summary.AttendanceRate != 50 {
		t.Fatalf("unexpected attendance summary: %#v", report.Summary)
	}
	if report.Summary.OccupiedSlotHours != 1 || len(report.Series) != 7 {
		t.Fatalf("unexpected utilization or series: %#v", report.Summary)
	}

	recorder := httptest.NewRecorder()
	app.reportsExportHandler(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("unexpected export response: %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Mekmaa operational report") || !strings.Contains(body, "Community sponsorship") || !strings.Contains(body, "8000.00") {
		t.Fatalf("export is missing report data: %s", body)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:   &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		Report: report,
	}
	if err := templates["reports"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render reports template: %v", err)
	}
}
