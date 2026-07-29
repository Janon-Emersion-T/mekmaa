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
