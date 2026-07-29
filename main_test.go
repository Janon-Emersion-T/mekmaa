package main

import (
	"database/sql"
	"errors"
	"io"
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
