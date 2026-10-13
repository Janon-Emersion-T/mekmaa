package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMonthlyFeeReminderDatabaseConstraints(t *testing.T) {
	dsn := strings.TrimSpace(
		os.Getenv("MONTHLY_FEE_SMS_TEST_DATABASE_URL"),
	)
	if dsn == "" {
		t.Skip("Dedicated test database URL not configured")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var databaseName string
	if err := db.QueryRow(
		"SELECT current_database()",
	).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}

	if databaseName != "mekmaa_sms_test" {
		t.Fatalf("Refusing to test database %q", databaseName)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var admissionID int64
	err = tx.QueryRow(`
		INSERT INTO admissions (
			student_id,
			full_name,
			admission_date,
			date_of_birth,
			gender,
			practice_type,
			address,
			passport_number,
			school,
			guardian_name,
			guardian_relationship,
			guardian_contact_number,
			guardian_alternative_contact_number,
			medical_information,
			created_at,
			updated_at
		)
		VALUES (
			'SMS-TEST-2099',
			'Synthetic SMS Student',
			'2099-01-01',
			'2015-01-01',
			'female',
			'group_practice',
			'Test Address',
			'',
			'Test School',
			'Synthetic Guardian',
			'Parent',
			'0770000000',
			'',
			'',
			CURRENT_TIMESTAMP,
			CURRENT_TIMESTAMP
		)
		RETURNING id
	`).Scan(&admissionID)
	if err != nil {
		t.Fatalf("create synthetic admission: %v", err)
	}

	var reminderID int64
	err = tx.QueryRow(`
		INSERT INTO monthly_fee_sms_reminders (
			admission_id,
			reminder_month,
			guardian_phone,
			outstanding_amount,
			message,
			status
		)
		VALUES ($1, '2099-12', '0770000000',
		        100.00, 'Synthetic test message', 'pending')
		RETURNING id
	`, admissionID).Scan(&reminderID)
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	result, err := tx.Exec(`
		INSERT INTO monthly_fee_sms_reminders (
			admission_id,
			reminder_month,
			guardian_phone,
			outstanding_amount,
			message,
			status
		)
		VALUES ($1, '2099-12', '0770000000',
		        100.00, 'Duplicate test', 'pending')
		ON CONFLICT (admission_id, reminder_month)
		DO NOTHING
	`, admissionID)
	if err != nil {
		t.Fatalf("duplicate insert: %v", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("duplicate reminder inserted: affected=%d", affected)
	}

	var claimedID int64
	err = tx.QueryRow(`
		UPDATE monthly_fee_sms_reminders
		SET status = 'sending',
		    attempt_count = attempt_count + 1
		WHERE id = $1 AND status = 'pending'
		RETURNING id
	`, reminderID).Scan(&claimedID)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	err = tx.QueryRow(`
		UPDATE monthly_fee_sms_reminders
		SET status = 'sending',
		    attempt_count = attempt_count + 1
		WHERE id = $1 AND status = 'pending'
		RETURNING id
	`, reminderID).Scan(&claimedID)
	if err != sql.ErrNoRows {
		t.Fatalf("second claim should fail: %v", err)
	}

	_, err = tx.Exec(`
		UPDATE monthly_fee_sms_reminders
		SET status = 'sent',
		    sent_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'sending'
	`, reminderID)
	if err != nil {
		t.Fatal(err)
	}

	err = tx.QueryRow(`
		UPDATE monthly_fee_sms_reminders
		SET status = 'sending'
		WHERE id = $1 AND status = 'pending'
		RETURNING id
	`, reminderID).Scan(&claimedID)
	if err != sql.ErrNoRows {
		t.Fatalf("sent reminder should not be claimable: %v", err)
	}

	var attempts int
	err = tx.QueryRow(`
		SELECT attempt_count
		FROM monthly_fee_sms_reminders
		WHERE id = $1
	`, reminderID).Scan(&attempts)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected one attempt, got %d", attempts)
	}

	t.Log("Synthetic admission created")
	t.Log("Duplicate reminder prevented")
	t.Log("Second delivery claim prevented")
	t.Log("Sent reminder cannot be claimed again")
	t.Log("Transaction will roll back all test records")
}
