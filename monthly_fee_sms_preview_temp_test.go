package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMonthlyFeeSMSPreview(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MONTHLY_FEE_SMS_PREVIEW_DATABASE_URL"))
	if dsn == "" {
		t.Skip("Preview database URL not configured")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}

	var databaseName string
	if err := db.QueryRow(
		"SELECT current_database()",
	).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}

	if databaseName != "mekmaa_dev" &&
		databaseName != "mekmaa_sms_test" {
		t.Fatalf("Refusing preview against database %q", databaseName)
	}

	app := &App{
		db: db,
		runtimeConfig: AppRuntimeConfig{
			DBDriver: databaseDriverPostgres,
		},
	}

	location, err := monthlyFeeReminderLocation()
	if err != nil {
		t.Fatal(err)
	}

	asOf := time.Now().In(location)

	reminders, err := app.listMonthlyFeeReminders(asOf)
	if err != nil {
		t.Fatal(err)
	}

	var total float64

	fmt.Printf("\nMONTHLY FEE SMS PREVIEW\n")
	fmt.Printf("Database: %s\n", databaseName)
	fmt.Printf("As of: %s\n", asOf.Format("2006-01-02"))
	fmt.Printf("Eligible students: %d\n\n", len(reminders))

	for _, reminder := range reminders {
		message := buildMonthlyFeeReminderSMS(reminder)

		fmt.Printf(
			"Admission ID: %d | Outstanding: Rs. %.2f | Unpaid months: %d | SMS length: %d\n",
			reminder.AdmissionID,
			reminder.OutstandingAmount,
			len(reminder.UnpaidMonths),
			len([]rune(message)),
		)

		total += reminder.OutstandingAmount
	}

	fmt.Printf("\nTotal outstanding: Rs. %.2f\n", total)
	fmt.Println("NO SMS MESSAGES SENT")
}
