package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (a *App) prepareMonthlyFeeSMSReminders(now time.Time) (int, error) {
	if a.runtimeConfig.DBDriver != databaseDriverPostgres {
		return 0, fmt.Errorf("monthly fee SMS reminders require PostgreSQL")
	}

	reminders, err := a.listMonthlyFeeReminders(now)
	if err != nil {
		return 0, err
	}

	location, err := monthlyFeeReminderLocation()
	if err != nil {
		return 0, err
	}

	reminderMonth := now.In(location).Format("2006-01")
	prepared := 0

	for _, reminder := range reminders {
		message := strings.TrimSpace(buildMonthlyFeeReminderSMS(reminder))
		if message == "" {
			continue
		}

		result, err := a.db.Exec(`
			INSERT INTO monthly_fee_sms_reminders (
				admission_id,
				reminder_month,
				guardian_phone,
				outstanding_amount,
				message,
				status
			)
			VALUES ($1, $2, $3, $4, $5, 'pending')
			ON CONFLICT (admission_id, reminder_month)
			DO NOTHING
		`,
			reminder.AdmissionID,
			reminderMonth,
			reminder.GuardianPhone,
			reminder.OutstandingAmount,
			message,
		)
		if err != nil {
			return prepared, fmt.Errorf(
				"prepare reminder for admission %d: %w",
				reminder.AdmissionID,
				err,
			)
		}

		affected, err := result.RowsAffected()
		if err != nil && err != sql.ErrNoRows {
			return prepared, err
		}

		prepared += int(affected)
	}

	return prepared, nil
}
