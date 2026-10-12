package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

type monthlyFeeSMSJob struct {
	ID            int64
	AdmissionID   int64
	ReminderMonth string
	GuardianPhone string
}

func (a *App) deliverMonthlyFeeSMSReminders(now time.Time) (int, error) {
	if a.runtimeConfig.DBDriver != databaseDriverPostgres {
		return 0, fmt.Errorf("monthly fee SMS delivery requires PostgreSQL")
	}

	if !a.sms.Enabled {
		return 0, fmt.Errorf("SMSLenz gateway is not configured")
	}

	location, err := monthlyFeeReminderLocation()
	if err != nil {
		return 0, err
	}

	if !monthlyFeeReminderDue(now) {
		return 0, fmt.Errorf(
			"monthly fee SMS delivery refused: outside scheduled reminder window",
		)
	}

	month := now.In(location).Format("2006-01")

	rows, err := a.db.Query(`
		SELECT id, admission_id, reminder_month, guardian_phone
		FROM monthly_fee_sms_reminders
		WHERE reminder_month = $1
		  AND status = 'pending'
		ORDER BY id
	`, month)
	if err != nil {
		return 0, err
	}

	var jobs []monthlyFeeSMSJob
	for rows.Next() {
		var job monthlyFeeSMSJob
		if err := rows.Scan(
			&job.ID,
			&job.AdmissionID,
			&job.ReminderMonth,
			&job.GuardianPhone,
		); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	sent := 0

	for _, job := range jobs {
		// Refresh balances and eligibility immediately before each attempt.
		currentReminders, err := a.listMonthlyFeeReminders(now)
		if err != nil {
			return sent, err
		}

		var reminder MonthlyFeeReminder
		ok := false
		for _, candidate := range currentReminders {
			if candidate.AdmissionID == job.AdmissionID {
				reminder = candidate
				ok = true
				break
			}
		}

		if !ok || reminder.OutstandingAmount <= 0.004 ||
			strings.TrimSpace(reminder.GuardianPhone) == "" {
			_, err := a.db.Exec(`
				UPDATE monthly_fee_sms_reminders
				SET status = 'failed',
				    last_error = 'Student no longer eligible'
				WHERE id = $1 AND status = 'pending'
			`, job.ID)
			if err != nil {
				return sent, err
			}
			continue
		}

		message := buildMonthlyFeeReminderSMS(reminder)

		if message == "" {
			_, err := a.db.Exec(`
				UPDATE monthly_fee_sms_reminders
				SET status = 'failed',
				    last_error = 'SMS message is empty'
				WHERE id = $1 AND status = 'pending'
			`, job.ID)
			if err != nil {
				return sent, err
			}
			continue
		}

		if _, err := normalizeSMSPhone(reminder.GuardianPhone); err != nil {
			_, updateErr := a.db.Exec(`
				UPDATE monthly_fee_sms_reminders
				SET status = 'failed',
				    last_error = $2
				WHERE id = $1 AND status = 'pending'
			`, job.ID, "Invalid guardian phone: "+err.Error())
			if updateErr != nil {
				return sent, updateErr
			}

			log.Printf(
				"monthly fee SMS rejected: invalid guardian number for admission=%d",
				job.AdmissionID,
			)
			continue
		}

		// Atomically claim the reminder.
		var claimedID int64
		err = a.db.QueryRow(`
			UPDATE monthly_fee_sms_reminders
			SET status = 'sending',
			    attempt_count = attempt_count + 1,
			    attempted_at = CURRENT_TIMESTAMP,
			    guardian_phone = $2,
			    outstanding_amount = $3,
			    message = $4,
			    last_error = ''
			WHERE id = $1
			  AND status = 'pending'
			RETURNING id
		`,
			job.ID,
			reminder.GuardianPhone,
			reminder.OutstandingAmount,
			message,
		).Scan(&claimedID)

		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return sent, err
		}

		// A failed or timed-out gateway request is treated as
		// uncertain to avoid automatic duplicate SMS messages.
		sendErr := a.sendSMSMessage(reminder.GuardianPhone, message)

		if sendErr != nil {
			_, updateErr := a.db.Exec(`
				UPDATE monthly_fee_sms_reminders
				SET status = 'uncertain',
				    last_error = $2
				WHERE id = $1 AND status = 'sending'
			`, claimedID, sendErr.Error())

			if updateErr != nil {
				return sent, updateErr
			}

			log.Printf(
				"monthly fee SMS outcome uncertain: admission=%d: %v",
				job.AdmissionID,
				sendErr,
			)
			continue
		}

		_, err = a.db.Exec(`
			UPDATE monthly_fee_sms_reminders
			SET status = 'sent',
			    sent_at = CURRENT_TIMESTAMP,
			    last_error = ''
			WHERE id = $1 AND status = 'sending'
		`, claimedID)

		if err != nil {
			return sent, err
		}

		sent++
	}

	return sent, nil
}
