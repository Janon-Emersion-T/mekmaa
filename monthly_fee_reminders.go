package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const monthlyFeeReminderDay = 15
const monthlyFeeReminderHour = 9

type MonthlyFeeReminder struct {
	AdmissionID       int64
	StudentName       string
	GuardianPhone     string
	OutstandingAmount float64
	UnpaidMonths      []string
}

func monthlyFeeReminderLocation() (*time.Location, error) {
	return time.LoadLocation("Asia/Colombo")
}

func monthlyFeeReminderDue(now time.Time) bool {
	location, err := monthlyFeeReminderLocation()
	if err != nil {
		return false
	}

	local := now.In(location)

	return local.Day() == monthlyFeeReminderDay &&
		local.Hour() == monthlyFeeReminderHour
}

func monthlyFeeReminderMonths(
	start time.Time,
	end time.Time,
) []string {
	start = time.Date(
		start.Year(), start.Month(), 1,
		0, 0, 0, 0, time.UTC,
	)

	end = time.Date(
		end.Year(), end.Month(), 1,
		0, 0, 0, 0, time.UTC,
	)

	var months []string

	for month := start; !month.After(end); month = month.AddDate(0, 1, 0) {
		months = append(months, month.Format("2006-01"))
	}

	return months
}

func (a *App) listMonthlyFeeReminders(
	asOf time.Time,
) ([]MonthlyFeeReminder, error) {
	location, err := monthlyFeeReminderLocation()
	if err != nil {
		return nil, err
	}

	localNow := asOf.In(location)
	currentMonth := localNow.Format("2006-01")

	var earliestDate string

	err = a.queryRowDB(`
		SELECT COALESCE(MIN(enrollment_date), '')
		FROM student_enrollments
		WHERE COALESCE(active, 1) = 1
		  AND COALESCE(free_monthly_fee, 0) = 0
		  AND enrollment_date <> ''
		  AND enrollment_date <= ?
	`, localNow.Format("2006-01-02")).Scan(&earliestDate)

	if err != nil {
		return nil, fmt.Errorf(
			"find earliest enrollment: %w",
			err,
		)
	}

	if len(earliestDate) < 7 {
		return nil, nil
	}

	start, err := time.Parse(
		"2006-01",
		earliestDate[:7],
	)
	if err != nil {
		return nil, err
	}

	end, err := time.Parse("2006-01", currentMonth)
	if err != nil {
		return nil, err
	}

	remindersByStudent := make(map[int64]*MonthlyFeeReminder)

	for _, month := range monthlyFeeReminderMonths(start, end) {
		paymentRows, err := a.listStudentPaymentRows(month)
		if err != nil {
			return nil, fmt.Errorf(
				"calculate outstanding fees for %s: %w",
				month,
				err,
			)
		}

		for _, row := range paymentRows {
			if row.Enrollment.FreeMonthlyFee {
				continue
			}

			if row.OutstandingAmount <= 0.004 {
				continue
			}

			admissionID := row.Admission.ID

			reminder := remindersByStudent[admissionID]

			if reminder == nil {
				reminder = &MonthlyFeeReminder{
					AdmissionID: admissionID,
					StudentName: strings.TrimSpace(
						row.Admission.FullName,
					),
				}

				remindersByStudent[admissionID] = reminder
			}

			reminder.OutstandingAmount = normalizeMoney(
				reminder.OutstandingAmount +
					row.OutstandingAmount,
			)

			reminder.UnpaidMonths = append(
				reminder.UnpaidMonths,
				month,
			)
		}
	}

	var reminders []MonthlyFeeReminder

	for _, reminder := range remindersByStudent {
		if reminder.OutstandingAmount <= 0.004 {
			continue
		}

		var status string
		var guardianPhone string

		err := a.queryRowDB(`
			SELECT
				COALESCE(status, 'active'),
				COALESCE(guardian_contact_number, '')
			FROM admissions
			WHERE id = ?
		`, reminder.AdmissionID).Scan(
			&status,
			&guardianPhone,
		)
		if err != nil {
			return nil, err
		}

		if !strings.EqualFold(
			strings.TrimSpace(status),
			"active",
		) {
			continue
		}

		reminder.GuardianPhone = strings.TrimSpace(guardianPhone)

		if reminder.GuardianPhone == "" {
			continue
		}

		reminders = append(reminders, *reminder)
	}

	sort.Slice(reminders, func(i, j int) bool {
		return reminders[i].AdmissionID < reminders[j].AdmissionID
	})

	return reminders, nil
}

func buildMonthlyFeeReminderSMS(
	reminder MonthlyFeeReminder,
) string {
	return fmt.Sprintf(
		"Dear Parent, outstanding monthly fees for %s at Mekmaa: Rs. %.2f. Please settle the payment. Thank you. - Mekmaa",
		reminder.StudentName,
		reminder.OutstandingAmount,
	)
}
