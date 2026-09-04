package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type enrollmentDeleteBlockedError struct {
	block EnrollmentDeleteBlock
}

func (e *enrollmentDeleteBlockedError) Error() string {
	if e == nil {
		return ""
	}
	return e.block.Message
}

func (e *enrollmentDeleteBlockedError) DeleteBlock() EnrollmentDeleteBlock {
	if e == nil {
		return EnrollmentDeleteBlock{}
	}
	return e.block
}

func (a *App) listFinanceTransactions() ([]FinanceTransaction, error) {
	return a.listFinanceTransactionsWithOptions(context.Background(), FinanceFilter{}, false, false)
}

func (a *App) listFinanceTransactionsFiltered(filter FinanceFilter) ([]FinanceTransaction, error) {
	return a.listFinanceTransactionsWithOptions(context.Background(), filter, false, false)
}

func (a *App) listFinanceTransactionsPage(ctx context.Context, filter FinanceFilter) ([]FinanceTransaction, int, error) {
	transactions, err := a.listFinanceTransactionsWithOptions(ctx, filter, true, true)
	if err != nil {
		return nil, 0, err
	}
	total, err := a.countFinanceTransactions(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return transactions, total, nil
}

func (a *App) listFinanceTransactionsWithOptions(ctx context.Context, filter FinanceFilter, paginate bool, includeVoidState bool) ([]FinanceTransaction, error) {
	query, args := financeTransactionsBaseQuery(filter)
	query += ` ORDER BY ft.recorded_at DESC, ft.id DESC`
	if paginate {
		page, limit := normalizedFinancePage(filter.Page, filter.Limit)
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, (page-1)*limit)
	}

	rows, err := a.queryContextDB(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transactions []FinanceTransaction
	for rows.Next() {
		var transaction FinanceTransaction
		var approvedAt sql.NullTime
		var voidedAt sql.NullTime
		var updatedAtRaw string
		if err := rows.Scan(
			&transaction.ID,
			&transaction.ReceiptNumber,
			&transaction.ReferenceNumber,
			&transaction.DivisionID,
			&transaction.DivisionCode,
			&transaction.DivisionName,
			&transaction.Category,
			&transaction.ApprovalStatus,
			&transaction.TransactionType,
			&transaction.ReferenceType,
			&transaction.ReferenceID,
			&transaction.SourceType,
			&transaction.SourceID,
			&transaction.FinanceAccountID,
			&transaction.FinanceAccountCode,
			&transaction.FinanceAccountName,
			&transaction.FinanceAccountType,
			&transaction.TransferGroupID,
			&transaction.StudentName,
			&transaction.StudentID,
			&transaction.AdmissionID,
			&transaction.TrainingProgramName,
			&transaction.BookingActivity,
			&transaction.OneToOneOfferingID,
			&transaction.OneToOneOfferingName,
			&transaction.PersonName,
			&transaction.Description,
			&transaction.Notes,
			&transaction.PaymentMethod,
			&transaction.Amount,
			&transaction.RecordedByUser,
			&transaction.RecordedByUserName,
			&transaction.ApprovedByUserID,
			&transaction.ApprovedByUserName,
			&transaction.RecordedAt,
			&transaction.CreatedAt,
			&updatedAtRaw,
			&approvedAt,
			&voidedAt,
			&transaction.VoidedByUserID,
			&transaction.VoidedByUserName,
			&transaction.VoidReason,
		); err != nil {
			return nil, err
		}
		if voidedAt.Valid {
			transaction.Voided = true
			transaction.VoidedAt = voidedAt.Time
		}
		if approvedAt.Valid {
			transaction.ApprovedAt = approvedAt.Time
		}
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(updatedAtRaw)); err == nil {
			transaction.UpdatedAt = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", strings.TrimSpace(updatedAtRaw)); err == nil {
			transaction.UpdatedAt = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(updatedAtRaw)); err == nil {
			transaction.UpdatedAt = parsed
		} else {
			transaction.UpdatedAt = transaction.CreatedAt
		}
		transaction.MoneyIn, transaction.MoneyOut = financeAmountParts(transaction.Amount)
		transactions = append(transactions, transaction)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if includeVoidState {
		if err := populateFinanceTransactionVoidStates(ctx, a.db, transactions); err != nil {
			return nil, err
		}
	}
	return transactions, nil
}

func (a *App) listOutstandingBookingFinancials() ([]BookingFinancial, error) {
	return a.listOutstandingBookingFinancialsByDivisionIDs(nil)
}

func (a *App) listOutstandingBookingFinancialsByDivisionIDs(divisionIDs []int64) ([]BookingFinancial, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, nil
	}
	financials, err := a.listBookingFinancials()
	if err != nil {
		return nil, err
	}
	oneToOneScheduleIDs, err := a.oneToOneScheduleIDSet()
	if err != nil {
		return nil, err
	}
	filtered := make([]BookingFinancial, 0, len(financials))
	for _, financial := range financials {
		if financial.QuotedAmount <= 0 {
			continue
		}
		if !bookingPaymentCollectibleStatus(financial.Status) {
			continue
		}
		if financial.OutstandingAmount <= 0.004 {
			continue
		}
		if _, isOneToOne := oneToOneScheduleIDs[financial.ScheduleID]; isOneToOne {
			continue
		}
		filtered = append(filtered, financial)
	}
	return filtered, nil
}

func (a *App) oneToOneScheduleIDSet() (map[int64]struct{}, error) {
	rows, err := a.queryDB(`SELECT schedule_id FROM one_to_one_bookings WHERE schedule_id > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[int64]struct{})
	for rows.Next() {
		var scheduleID int64
		if err := rows.Scan(&scheduleID); err != nil {
			return nil, err
		}
		ids[scheduleID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func overlappingLeaveDaysForMonth(leaves []StudentEnrollmentLeave, monthStart time.Time) (int, error) {
	if len(leaves) == 0 {
		return 0, nil
	}
	monthStart = monthStart.UTC()
	monthEnd := monthStart.AddDate(0, 1, -1)
	covered := make([]bool, monthEnd.Day())

	for _, leave := range leaves {
		startDate, err := time.Parse("2006-01-02", strings.TrimSpace(leave.StartDate))
		if err != nil {
			return 0, err
		}
		endDate, err := time.Parse("2006-01-02", strings.TrimSpace(leave.EndDate))
		if err != nil {
			return 0, err
		}
		if endDate.Before(monthStart) || startDate.After(monthEnd) {
			continue
		}
		if startDate.Before(monthStart) {
			startDate = monthStart
		}
		if endDate.After(monthEnd) {
			endDate = monthEnd
		}
		for day := startDate; !day.After(endDate); day = day.AddDate(0, 0, 1) {
			covered[day.Day()-1] = true
		}
	}

	days := 0
	for _, present := range covered {
		if present {
			days++
		}
	}
	return days, nil
}

func proratedMonthlyFee(baseAmount float64, leaveDays int, monthDays int) (float64, float64) {
	if baseAmount <= 0 || leaveDays <= 0 || monthDays <= 0 {
		return baseAmount, 0
	}
	if leaveDays >= monthDays {
		return 0, baseAmount
	}
	leaveAmount := math.Round((baseAmount*float64(leaveDays)/float64(monthDays))*100) / 100
	dueAmount := math.Round((baseAmount-leaveAmount)*100) / 100
	if dueAmount < 0 {
		dueAmount = 0
	}
	return dueAmount, leaveAmount
}

func paymentMonthCollectible(paymentMonth string, now time.Time) bool {
	currentMonth := now.Format("2006-01")
	return paymentMonth <= currentMonth
}

func latestCollectiblePaymentMonth(now time.Time) string {
	return now.Format("2006-01")
}

func monthlyPaymentCollectionNotice(paymentMonth string, now time.Time) string {
	if paymentMonthCollectible(paymentMonth, now) {
		return ""
	}
	return "Monthly payments can only be collected for the current month or earlier."
}

func paymentBillingStartDate(
	enrollment *StudentEnrollment,
	admission *Admission,
) (time.Time, error) {
	if enrollment != nil {
		if value := strings.TrimSpace(enrollment.EnrollmentDate); value != "" {
			start, err := time.ParseInLocation(
				"2006-01-02",
				value,
				time.Local,
			)
			if err != nil {
				return time.Time{}, err
			}

			return start, nil
		}

		// Legacy fallback only. New enrollments must have EnrollmentDate.
		if !enrollment.CreatedAt.IsZero() {
			start := enrollment.CreatedAt.In(time.Local)

			return time.Date(
				start.Year(),
				start.Month(),
				start.Day(),
				0,
				0,
				0,
				0,
				time.Local,
			), nil
		}
	}

	if admission == nil ||
		strings.TrimSpace(admission.AdmissionDate) == "" {
		return time.Time{}, errors.New(
			"programme enrollment date is required for monthly billing",
		)
	}

	start, err := time.ParseInLocation(
		"2006-01-02",
		strings.TrimSpace(admission.AdmissionDate),
		time.Local,
	)
	if err != nil {
		return time.Time{}, err
	}

	return start, nil
}

func applyFirstMonthEnrollmentDiscount(
	baseAmount float64,
	billingStart time.Time,
	paymentMonth string,
	monthDays int,
) (float64, float64) {
	if baseAmount <= 0 || billingStart.IsZero() || monthDays <= 0 {
		return baseAmount, 0
	}

	// Proration applies only to the enrollment month.
	if billingStart.Format("2006-01") != paymentMonth {
		return baseAmount, 0
	}

	enrollmentDay := billingStart.Day()

	// Final 5 calendar days of the enrollment month are free.
	//
	// Examples:
	//   31-day month -> 27-31 free
	//   30-day month -> 26-30 free
	//   28-day month -> 24-28 free
	//   29-day month -> 25-29 free
	lastFiveDaysStart := monthDays - 4

	if enrollmentDay >= lastFiveDaysStart {
		return 0, normalizeMoney(baseAmount)
	}

	// Day 1 through day 15 pays the full monthly fee.
	if enrollmentDay <= 15 {
		return baseAmount, 0
	}

	// From the 16th until the final-five-day window, charge half.
	discounted := normalizeMoney(baseAmount * 0.5)

	return discounted, normalizeMoney(baseAmount - discounted)
}

func (a *App) listStudentPaymentRows(paymentMonth string) ([]StudentPaymentRow, error) {
	return a.listStudentPaymentRowsByDivisionIDs(paymentMonth, nil)
}

func (a *App) listStudentPaymentRowsByDivisionIDs(paymentMonth string, divisionIDs []int64) ([]StudentPaymentRow, error) {
	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		return nil, err
	}
	monthDays := monthDate.AddDate(0, 1, -1).Day()
	monthEnd := monthDate.AddDate(0, 1, -1).Format("2006-01-02")
	query := `
		SELECT
			payment_rows.enrollment_id,
			payment_rows.admission_id,
			payment_rows.student_id,
			payment_rows.full_name,
			payment_rows.admission_date,
			payment_rows.date_of_birth,
			payment_rows.gender,
			payment_rows.photo_path,
			payment_rows.qr_code_path,
			payment_rows.qr_code_value,
			payment_rows.training_program_id,
			payment_rows.training_program_name,
			payment_rows.division_id,
			payment_rows.division_code,
			payment_rows.division_name,
			payment_rows.free_monthly_fee,
			payment_rows.discounted_monthly_fee,
			payment_rows.original_monthly_fee,
			payment_rows.enrollment_date,
			payment_rows.enrollment_created_at
		FROM (
			SELECT
				se.id AS enrollment_id,
				a.id AS admission_id,
				a.student_id,
				a.full_name,
				COALESCE(a.admission_date, '') AS admission_date,
				a.date_of_birth,
				a.gender,
				COALESCE(a.photo_path, '') AS photo_path,
				COALESCE(a.qr_code_path, '') AS qr_code_path,
				COALESCE(a.qr_code_value, '') AS qr_code_value,
				tp.id AS training_program_id,
				tp.name AS training_program_name,
				COALESCE(tp.division_id, 0) AS division_id,
				COALESCE(d.code, '') AS division_code,
				COALESCE(d.name, '') AS division_name,
				COALESCE(se.free_monthly_fee, 0) AS free_monthly_fee,
				COALESCE(se.discounted_monthly_fee, 0) AS discounted_monthly_fee,
				COALESCE(tp.monthly_fee, 0) AS original_monthly_fee,
				COALESCE(CAST(se.enrollment_date AS TEXT), '') AS enrollment_date,
				COALESCE(CAST(se.created_at AS TEXT), '') AS enrollment_created_at,
				COALESCE(tp.sort_order, 0) AS program_sort_order
			FROM student_enrollments se
			JOIN admissions a
				ON a.id = se.admission_id
			JOIN training_programs tp
				ON tp.id = se.training_program_id
			LEFT JOIN divisions d
				ON d.id = tp.division_id
			WHERE se.enrollment_date <= ?
			  AND COALESCE(se.active, 1) = 1
	`
	args := []any{monthEnd}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}

	query += `
		) AS payment_rows
		ORDER BY
			LOWER(payment_rows.full_name),
			payment_rows.program_sort_order ASC,
			payment_rows.training_program_name ASC,
			payment_rows.enrollment_id,
			payment_rows.admission_id
	`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paymentRows []StudentPaymentRow
	enrollmentIDs := make([]int64, 0)
	for rows.Next() {
		var (
			row                  StudentPaymentRow
			enrollmentID         int64
			freeMonthlyFee       int
			discountedMonthlyFee float64
			enrollmentDate       string
			enrollmentCreatedAt  string
		)
		if err := rows.Scan(
			&enrollmentID,
			&row.Admission.ID, &row.Admission.StudentID, &row.Admission.FullName, &row.Admission.AdmissionDate,
			&row.Admission.DateOfBirth,
			&row.Admission.Gender,
			&row.Admission.PhotoPath,
			&row.Admission.QRCodePath,
			&row.Admission.QRCodeValue,
			&row.Enrollment.TrainingProgramID,
			&row.Enrollment.TrainingProgramName,
			&row.Enrollment.DivisionID,
			&row.Enrollment.DivisionCode,
			&row.Enrollment.DivisionName,
			&freeMonthlyFee,
			&discountedMonthlyFee,
			&row.OriginalMonthlyFee,
			&enrollmentDate,
			&enrollmentCreatedAt,
		); err != nil {
			return nil, err
		}
		row.Enrollment.ID = enrollmentID
		row.Enrollment.AdmissionID = row.Admission.ID
		row.Enrollment.Student = row.Admission
		row.Enrollment.FreeMonthlyFee = freeMonthlyFee == 1
		row.Enrollment.DiscountedMonthlyFee = normalizeMoney(discountedMonthlyFee)
		row.Enrollment.EnrollmentDate = strings.TrimSpace(enrollmentDate)
		if strings.TrimSpace(enrollmentCreatedAt) != "" {
			rawCreatedAt := strings.TrimSpace(enrollmentCreatedAt)

			var (
				createdAt time.Time
				err       error
			)

			layouts := []string{
				"2006-01-02 15:04:05",
				"2006-01-02 15:04:05.999999999",
				"2006-01-02 15:04:05Z07:00",
				"2006-01-02 15:04:05.999999999Z07:00",
				"2006-01-02 15:04:05-07",
				"2006-01-02 15:04:05.999999999-07",
				time.RFC3339Nano,
				"2006-01-02 15:04:05.999999999 -0700 MST",
			}

			for _, layout := range layouts {
				createdAt, err = time.Parse(layout, rawCreatedAt)
				if err == nil {
					break
				}
			}

			if err != nil {
				return nil, err
			}

			row.Enrollment.CreatedAt = createdAt
		}
		row.MonthDays = monthDays
		row.BillableDays = monthDays
		row.MonthlyFee = effectiveMonthlyFee(row.Admission, row.OriginalMonthlyFee)
		if row.Enrollment.FreeMonthlyFee {
			row.Admission.FreeMonthlyFee = true
			row.MonthlyFee = 0
		} else if row.Enrollment.DiscountedMonthlyFee > 0 {
			row.MonthlyFee = row.Enrollment.DiscountedMonthlyFee
		}
		if enrollmentID > 0 {
			enrollmentIDs = append(enrollmentIDs, enrollmentID)
		}
		row.Admission.TrainingProgramName = row.Enrollment.TrainingProgramName
		row.Admission.TrainingProgramNames = row.Enrollment.TrainingProgramName
		paymentRows = append(paymentRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	activePayments, err := a.listActiveStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth, divisionIDs)
	if err != nil {
		return nil, err
	}
	allPayments, err := a.listStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth, divisionIDs, true)
	if err != nil {
		return nil, err
	}
	activePaymentMap := make(map[string][]StudentMonthlyPayment)
	for _, payment := range activePayments {
		activePaymentMap[studentMonthlyPaymentKey(payment.AdmissionID, payment.EnrollmentID)] = append(activePaymentMap[studentMonthlyPaymentKey(payment.AdmissionID, payment.EnrollmentID)], payment)
	}
	paymentHistoryMap := make(map[string][]StudentMonthlyPayment)
	for _, payment := range allPayments {
		paymentHistoryMap[studentMonthlyPaymentKey(payment.AdmissionID, payment.EnrollmentID)] = append(paymentHistoryMap[studentMonthlyPaymentKey(payment.AdmissionID, payment.EnrollmentID)], payment)
	}

	leaveMap, err := a.listStudentEnrollmentLeavesByEnrollmentIDs(enrollmentIDs)
	if err != nil {
		return nil, err
	}
	for i := range paymentRows {
		if paymentRows[i].MonthlyFee > 0 {
			billingStart, err := paymentBillingStartDate(func() *StudentEnrollment {
				if paymentRows[i].Enrollment.ID > 0 {
					return &paymentRows[i].Enrollment
				}
				return nil
			}(), &paymentRows[i].Admission)
			if err != nil {
				return nil, err
			}
			paymentRows[i].MonthlyFee, paymentRows[i].EnrollmentProrationAmount = applyFirstMonthEnrollmentDiscount(paymentRows[i].MonthlyFee, billingStart, paymentMonth, paymentRows[i].MonthDays)
		}
		if paymentRows[i].Enrollment.ID > 0 {
			paymentRows[i].Leaves = leaveMap[paymentRows[i].Enrollment.ID]
			if !paymentRows[i].Enrollment.FreeMonthlyFee && paymentRows[i].OriginalMonthlyFee > 0 {
				leaveDays, err := overlappingLeaveDaysForMonth(paymentRows[i].Leaves, monthDate)
				if err != nil {
					return nil, err
				}
				paymentRows[i].LeaveDays = leaveDays
				paymentRows[i].BillableDays = paymentRows[i].MonthDays - leaveDays
				if paymentRows[i].BillableDays < 0 {
					paymentRows[i].BillableDays = 0
				}
				paymentRows[i].MonthlyFee, paymentRows[i].LeaveAmount = proratedMonthlyFee(paymentRows[i].MonthlyFee, leaveDays, paymentRows[i].MonthDays)
			}
		}
		key := studentMonthlyPaymentKey(paymentRows[i].Admission.ID, paymentRows[i].Enrollment.ID)
		paymentRows[i].Payments = paymentHistoryMap[key]
		for _, payment := range activePaymentMap[key] {
			paymentRows[i].CollectedAmount = normalizeMoney(paymentRows[i].CollectedAmount + payment.Amount)
			paymentRows[i].DiscountAmount = normalizeMoney(paymentRows[i].DiscountAmount + payment.DiscountAmount)
			if paymentRows[i].Payment == nil || payment.CollectedAt.After(paymentRows[i].Payment.CollectedAt) || (payment.CollectedAt.Equal(paymentRows[i].Payment.CollectedAt) && payment.ID > paymentRows[i].Payment.ID) {
				latest := payment
				paymentRows[i].Payment = &latest
			}
		}
		paymentRows[i].OutstandingAmount = normalizeMoney(paymentRows[i].MonthlyFee - paymentRows[i].CollectedAmount - paymentRows[i].DiscountAmount)
		if paymentRows[i].OutstandingAmount < 0 {
			paymentRows[i].OutstandingAmount = 0
		}
	}
	return paymentRows, nil
}

func (a *App) createStudentEnrollmentLeave(enrollmentID int64, startDate string, endDate string, reason string) error {
	startDate = strings.TrimSpace(startDate)
	endDate = strings.TrimSpace(endDate)
	reason = strings.TrimSpace(reason)
	if enrollmentID <= 0 {
		return errors.New("select a valid enrollment")
	}
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return errors.New("enter a valid leave start date")
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return errors.New("enter a valid leave end date")
	}
	if end.Before(start) {
		return errors.New("leave end date must be on or after the start date")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		return err
	}
	if enrollment.Student.AdmissionDate != "" && startDate < enrollment.Student.AdmissionDate {
		return errors.New("leave cannot start before the student admission date")
	}

	var overlapCount int
	if err := tx.QueryRow(rebindDatabaseQuery(a.runtimeConfig.DBDriver, `
		SELECT COUNT(*)
		FROM student_enrollment_leaves
		WHERE enrollment_id = ?
		  AND COALESCE(active, 1) = 1
		  AND NOT (end_date < ? OR start_date > ?)
	`), enrollmentID, startDate, endDate).Scan(&overlapCount); err != nil {
		return err
	}
	if overlapCount > 0 {
		return ErrStudentLeaveOverlap
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(rebindDatabaseQuery(a.runtimeConfig.DBDriver, `
		INSERT INTO student_enrollment_leaves (
			enrollment_id, start_date, end_date, reason, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, 1, ?, ?)
	`), enrollmentID, startDate, endDate, reason, now, now); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deleteStudentEnrollmentLeave(leaveID int64, enrollmentID int64) error {
	if leaveID <= 0 || enrollmentID <= 0 {
		return errors.New("select a valid leave record")
	}
	result, err := a.execDB(`DELETE FROM student_enrollment_leaves WHERE id = ? AND enrollment_id = ?`, leaveID, enrollmentID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) getPricingSettings() (*PricingSettings, error) {
	return getPricingSettingsQuery(a.db)
}

func getPricingSettingsQuery(queryer sqlQueryer) (*PricingSettings, error) {
	row := queryer.QueryRow(`
		SELECT id, peak_start_hour, peak_end_hour, COALESCE(referral_commission_amount, 0), created_at, updated_at
		FROM pricing_settings
		ORDER BY id ASC
		LIMIT 1
	`)

	var settings PricingSettings
	if err := row.Scan(
		&settings.ID,
		&settings.PeakStartHour,
		&settings.PeakEndHour,
		&settings.ReferralCommissionAmount,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (a *App) listReferralPartners(activeOnly bool) ([]ReferralPartner, error) {
	query := `
		SELECT id, name, code, email, phone, active, created_at, updated_at
		FROM referral_partners
	`
	if activeOnly {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY active DESC, LOWER(name), id`
	rows, err := a.queryDB(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var partners []ReferralPartner
	for rows.Next() {
		var partner ReferralPartner
		var active int
		if err := rows.Scan(&partner.ID, &partner.Name, &partner.Code, &partner.Email, &partner.Phone, &active, &partner.CreatedAt, &partner.UpdatedAt); err != nil {
			return nil, err
		}
		partner.Active = active == 1
		partners = append(partners, partner)
	}
	return partners, rows.Err()
}

func (a *App) listBookingReferrals() ([]BookingReferral, error) {
	return a.listBookingReferralsByDivisionIDs(nil)
}

func (a *App) listBookingReferralsByDivisionIDs(divisionIDs []int64) ([]BookingReferral, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, nil
	}
	rows, err := a.queryDB(`
		SELECT br.id, br.schedule_id, br.partner_id, rp.name, rp.code, br.commission_amount,
		       s.status, s.title, s.slot_date, br.paid, br.paid_at, br.payment_method,
		       COALESCE(br.finance_transaction_id, 0), br.created_at
		FROM booking_referrals br
		JOIN referral_partners rp ON rp.id = br.partner_id
		JOIN space_schedules s ON s.id = br.schedule_id
		ORDER BY
			CASE WHEN s.status = 'confirmed' AND br.paid = 0 THEN 0 WHEN s.status = 'pending' THEN 1 ELSE 2 END,
			br.created_at DESC, br.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var referrals []BookingReferral
	for rows.Next() {
		var referral BookingReferral
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&referral.ID, &referral.ScheduleID, &referral.PartnerID, &referral.PartnerName,
			&referral.PartnerCode, &referral.CommissionAmount, &referral.BookingStatus,
			&referral.BookingTitle, &referral.SlotDate, &paid, &paidAt, &referral.PaymentMethod,
			&referral.FinanceTransactionID, &referral.CreatedAt,
		); err != nil {
			return nil, err
		}
		referral.BookingReference = bookingReference(referral.ScheduleID)
		referral.Paid = paid == 1
		if paidAt.Valid {
			referral.PaidAt = paidAt.Time
		}
		referrals = append(referrals, referral)
	}
	return referrals, rows.Err()
}

func (a *App) listBookingReferralsForScheduleIDs(scheduleIDs []int64) ([]BookingReferral, error) {
	return listBookingReferralsForScheduleIDsQuery(
		a.db,
		a.runtimeConfig.DBDriver,
		scheduleIDs,
	)
}

func (a *App) listBookingPaymentCollectionsForScheduleIDs(scheduleIDs []int64) ([]BookingPaymentCollection, error) {
	return listBookingPaymentCollectionsForScheduleIDsQuery(
		a.db,
		a.runtimeConfig.DBDriver,
		scheduleIDs,
	)
}

func (a *App) listRecentBookingPaymentCollectionsByDivisionIDs(divisionIDs []int64, limit int) ([]BookingPaymentCollection, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return nil, err
	}
	if !allowed || limit <= 0 {
		return nil, nil
	}

	query := `
		SELECT
			bpc.id,
			bpc.schedule_id,
			bpc.finance_transaction_id,
			ft.receipt_number,
			ft.person_name,
			ft.description,
			bpc.amount,
			bpc.payment_method,
			COALESCE(bpc.payment_note, ''),
			COALESCE(bpc.collected_by_user_id, 0),
			COALESCE(collector.name, ''),
			bpc.collected_at,
			bpc.created_at,
			bpc.voided,
			COALESCE(bpc.void_reason, ''),
			COALESCE(bpc.voided_by_user_id, 0),
			COALESCE(voider.name, ''),
			bpc.voided_at
		FROM booking_payment_collections bpc
		JOIN finance_transactions ft
			ON ft.id = bpc.finance_transaction_id
		LEFT JOIN users collector
			ON collector.id = bpc.collected_by_user_id
		LEFT JOIN users voider
			ON voider.id = bpc.voided_by_user_id
		WHERE COALESCE(bpc.voided, 0) = 0
		  AND ft.category = 'booking_payment'
	`
	args := make([]any, 0, len(divisionIDs)+1)
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(ft.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY bpc.collected_at DESC, bpc.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collections []BookingPaymentCollection
	for rows.Next() {
		var collection BookingPaymentCollection
		var voided int
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&collection.ID,
			&collection.ScheduleID,
			&collection.FinanceTransactionID,
			&collection.ReceiptNumber,
			&collection.PersonName,
			&collection.Description,
			&collection.Amount,
			&collection.PaymentMethod,
			&collection.PaymentNote,
			&collection.CollectedByUserID,
			&collection.CollectedByUserName,
			&collection.CollectedAt,
			&collection.CreatedAt,
			&voided,
			&collection.VoidReason,
			&collection.VoidedByUserID,
			&collection.VoidedByUserName,
			&voidedAt,
		); err != nil {
			return nil, err
		}
		collection.Voided = voided == 1
		if voidedAt.Valid {
			collection.VoidedAt = voidedAt.Time
		}
		collections = append(collections, collection)
	}
	return collections, rows.Err()
}

func (a *App) listBookingFinancials() ([]BookingFinancial, error) {
	rows, err := a.queryDB(`SELECT schedule_id FROM booking_financials ORDER BY schedule_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scheduleIDs []int64
	for rows.Next() {
		var scheduleID int64
		if err := rows.Scan(&scheduleID); err != nil {
			return nil, err
		}
		scheduleIDs = append(scheduleIDs, scheduleID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return listBookingFinancialsForScheduleIDsQuery(
		a.db,
		a.runtimeConfig.DBDriver,
		scheduleIDs,
	)
}

func (a *App) listBookingFinancialsForScheduleIDs(scheduleIDs []int64) ([]BookingFinancial, error) {
	return listBookingFinancialsForScheduleIDsQuery(
		a.db,
		a.runtimeConfig.DBDriver,
		scheduleIDs,
	)
}

func studentMonthlyPaymentKey(admissionID, enrollmentID int64) string {
	return fmt.Sprintf("%d:%d", admissionID, enrollmentID)
}

func (a *App) listActiveStudentMonthlyPaymentsForMonth(paymentMonth string) ([]StudentMonthlyPayment, error) {
	return a.listActiveStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth, nil)
}

func (a *App) listActiveStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth string, divisionIDs []int64) ([]StudentMonthlyPayment, error) {
	return a.listStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth, divisionIDs, false)
}

func (a *App) listStudentMonthlyPaymentsForMonthByDivisionIDs(paymentMonth string, divisionIDs []int64, includeVoided bool) ([]StudentMonthlyPayment, error) {
	hasDiscountAmount, err := tableHasColumn(a.db, "student_monthly_payments", "discount_amount")
	if err != nil {
		return nil, err
	}
	hasAdjustmentReason, err := tableHasColumn(a.db, "student_monthly_payments", "adjustment_reason")
	if err != nil {
		return nil, err
	}
	query := `
		SELECT
			smp.id,
			smp.admission_id,
			COALESCE(smp.enrollment_id, 0),
			COALESCE(ft.receipt_number, ''),
			COALESCE(tp.name, adm_tp.name, '') AS training_program_name,
			COALESCE(d.name, adm_d.name, '') AS division_name,
			smp.payment_month,
			smp.amount,
	`
	if hasDiscountAmount {
		query += ` COALESCE(smp.discount_amount, 0),`
	} else {
		query += ` 0,`
	}
	if hasAdjustmentReason {
		query += ` COALESCE(smp.adjustment_reason, ''),`
	} else {
		query += ` '',`
	}
	query += `
			smp.payment_method,
			smp.finance_transaction_id,
			COALESCE(smp.collected_by_user_id, 0),
			COALESCE(u.name, '') AS collected_by_user_name,
			COALESCE(smp.voided, 0),
			COALESCE(smp.void_reason, ''),
			COALESCE(smp.voided_by_user_id, 0),
			COALESCE(vu.name, '') AS voided_by_user_name,
			smp.voided_at,
			smp.collected_at,
			smp.created_at
		FROM student_monthly_payments smp
		LEFT JOIN finance_transactions ft ON ft.id = smp.finance_transaction_id
		LEFT JOIN student_enrollments se ON se.id = smp.enrollment_id
		LEFT JOIN training_programs se_tp ON se_tp.id = se.training_program_id
		LEFT JOIN admissions adm ON adm.id = smp.admission_id
		LEFT JOIN training_programs adm_tp ON adm_tp.id = adm.training_program_id
		LEFT JOIN training_programs tp ON tp.id = COALESCE(se.training_program_id, adm.training_program_id)
		LEFT JOIN divisions d ON d.id = se_tp.division_id
		LEFT JOIN divisions adm_d ON adm_d.id = adm_tp.division_id
		LEFT JOIN users u ON u.id = smp.collected_by_user_id
		LEFT JOIN users vu ON vu.id = smp.voided_by_user_id
		WHERE smp.payment_month = ?
	`
	args := []any{paymentMonth}
	if !includeVoided {
		query += ` AND COALESCE(smp.voided, 0) = 0`
	}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(se_tp.division_id, adm_tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY smp.collected_at ASC, smp.id ASC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payments []StudentMonthlyPayment
	for rows.Next() {
		var payment StudentMonthlyPayment
		var voided int
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&payment.ID,
			&payment.AdmissionID,
			&payment.EnrollmentID,
			&payment.ReceiptNumber,
			&payment.TrainingProgramName,
			&payment.DivisionName,
			&payment.PaymentMonth,
			&payment.Amount,
			&payment.DiscountAmount,
			&payment.AdjustmentReason,
			&payment.PaymentMethod,
			&payment.FinanceTransactionID,
			&payment.CollectedByUserID,
			&payment.CollectedByUserName,
			&voided,
			&payment.VoidReason,
			&payment.VoidedByUserID,
			&payment.VoidedByUserName,
			&voidedAt,
			&payment.CollectedAt,
			&payment.CreatedAt,
		); err != nil {
			return nil, err
		}
		payment.Voided = voided == 1
		if voidedAt.Valid {
			payment.VoidedAt = voidedAt.Time
		}
		payments = append(payments, payment)
	}
	return payments, rows.Err()
}

func (a *App) listStudentMonthlyPaymentActivityByDivisionIDs(
	fromMonth string,
	toMonth string,
	divisionIDs []int64,
) ([]StudentMonthlyPaymentActivityRow, error) {
	if _, err := parsePaymentMonth(fromMonth); err != nil {
		return nil, err
	}
	if _, err := parsePaymentMonth(toMonth); err != nil {
		return nil, err
	}
	hasDiscountAmount, err := tableHasColumn(a.db, "student_monthly_payments", "discount_amount")
	if err != nil {
		return nil, err
	}
	hasAdjustmentReason, err := tableHasColumn(a.db, "student_monthly_payments", "adjustment_reason")
	if err != nil {
		return nil, err
	}

	query := `
		SELECT
			smp.id,
			smp.admission_id,
			COALESCE(smp.enrollment_id, 0),
			COALESCE(ft.receipt_number, ''),
			COALESCE(adm.student_id, ''),
			COALESCE(adm.full_name, ''),
			COALESCE(tp.name, adm_tp.name, '') AS training_program_name,
			COALESCE(d.name, adm_d.name, '') AS division_name,
			smp.payment_month,
			smp.amount,
	`
	if hasDiscountAmount {
		query += ` COALESCE(smp.discount_amount, 0),`
	} else {
		query += ` 0,`
	}
	if hasAdjustmentReason {
		query += ` COALESCE(smp.adjustment_reason, ''),`
	} else {
		query += ` '',`
	}
	query += `
			smp.payment_method,
			smp.finance_transaction_id,
			COALESCE(smp.collected_by_user_id, 0),
			COALESCE(u.name, ''),
			COALESCE(smp.voided, 0),
			COALESCE(smp.void_reason, ''),
			COALESCE(smp.voided_by_user_id, 0),
			COALESCE(vu.name, ''),
			smp.voided_at,
			smp.collected_at,
			smp.created_at
		FROM student_monthly_payments smp
		LEFT JOIN finance_transactions ft ON ft.id = smp.finance_transaction_id
		LEFT JOIN admissions adm ON adm.id = smp.admission_id
		LEFT JOIN student_enrollments se ON se.id = smp.enrollment_id
		LEFT JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN training_programs adm_tp ON adm_tp.id = adm.training_program_id
		LEFT JOIN divisions d ON d.id = tp.division_id
		LEFT JOIN divisions adm_d ON adm_d.id = adm_tp.division_id
		LEFT JOIN users u ON u.id = smp.collected_by_user_id
		LEFT JOIN users vu ON vu.id = smp.voided_by_user_id
		WHERE smp.payment_month >= ?
		  AND smp.payment_month <= ?
	`
	args := []any{fromMonth, toMonth}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(tp.division_id, adm_tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY smp.payment_month DESC, smp.collected_at DESC, smp.id DESC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	activityRows := make([]StudentMonthlyPaymentActivityRow, 0)
	for rows.Next() {
		var row StudentMonthlyPaymentActivityRow
		var voided int
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&row.Payment.ID,
			&row.Payment.AdmissionID,
			&row.Payment.EnrollmentID,
			&row.Payment.ReceiptNumber,
			&row.StudentID,
			&row.StudentName,
			&row.TrainingProgramName,
			&row.DivisionName,
			&row.Payment.PaymentMonth,
			&row.Payment.Amount,
			&row.Payment.DiscountAmount,
			&row.Payment.AdjustmentReason,
			&row.Payment.PaymentMethod,
			&row.Payment.FinanceTransactionID,
			&row.Payment.CollectedByUserID,
			&row.Payment.CollectedByUserName,
			&voided,
			&row.Payment.VoidReason,
			&row.Payment.VoidedByUserID,
			&row.Payment.VoidedByUserName,
			&voidedAt,
			&row.Payment.CollectedAt,
			&row.Payment.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Payment.TrainingProgramName = row.TrainingProgramName
		row.Payment.DivisionName = row.DivisionName
		row.Payment.Voided = voided == 1
		if voidedAt.Valid {
			row.Payment.VoidedAt = voidedAt.Time
		}
		row.SettledAmount = normalizeMoney(row.Payment.Amount + row.Payment.DiscountAmount)
		activityRows = append(activityRows, row)
	}
	return activityRows, rows.Err()
}

func aggregateBookingCustomerBalances(financials []BookingFinancial, search string) []BookingCustomerBalance {
	search = strings.ToLower(strings.TrimSpace(search))
	type bucket struct {
		name  string
		email string
		items []BookingFinancial
	}
	byCustomer := make(map[string]*bucket)
	for _, financial := range financials {
		name := bookingFinancialDisplayName(financial)
		email := strings.TrimSpace(financial.RequesterEmail)
		haystack := strings.ToLower(strings.TrimSpace(name + " " + email))
		if search != "" && !strings.Contains(haystack, search) {
			continue
		}
		key := strings.ToLower(name) + "|" + strings.ToLower(email)
		entry, ok := byCustomer[key]
		if !ok {
			entry = &bucket{name: name, email: email}
			byCustomer[key] = entry
		}
		entry.items = append(entry.items, financial)
	}

	balances := make([]BookingCustomerBalance, 0, len(byCustomer))
	for _, entry := range byCustomer {
		balance := BookingCustomerBalance{
			CustomerName:  entry.name,
			CustomerEmail: entry.email,
			Bookings:      entry.items,
		}
		for _, booking := range entry.items {
			balance.BookingCount++
			balance.QuotedAmount = normalizeMoney(balance.QuotedAmount + booking.QuotedAmount)
			balance.CollectedAmount = normalizeMoney(balance.CollectedAmount + booking.TotalCollected)
			balance.OutstandingAmount = normalizeMoney(balance.OutstandingAmount + booking.OutstandingAmount)
			if booking.OutstandingAmount > 0.004 {
				balance.OutstandingCount++
			}
		}
		sort.Slice(balance.Bookings, func(i, j int) bool {
			if balance.Bookings[i].OutstandingAmount == balance.Bookings[j].OutstandingAmount {
				if balance.Bookings[i].SlotDate == balance.Bookings[j].SlotDate {
					return balance.Bookings[i].SlotHour < balance.Bookings[j].SlotHour
				}
				return balance.Bookings[i].SlotDate < balance.Bookings[j].SlotDate
			}
			return balance.Bookings[i].OutstandingAmount > balance.Bookings[j].OutstandingAmount
		})
		balances = append(balances, balance)
	}

	sort.Slice(balances, func(i, j int) bool {
		if balances[i].OutstandingAmount == balances[j].OutstandingAmount {
			return balances[i].CustomerName < balances[j].CustomerName
		}
		return balances[i].OutstandingAmount > balances[j].OutstandingAmount
	})
	return balances
}

func bookingFinancialDisplayName(financial BookingFinancial) string {
	name := strings.TrimSpace(financial.RequesterName)
	if name != "" {
		return name
	}
	title := strings.TrimSpace(financial.Title)
	if title != "" {
		return title
	}
	if financial.ScheduleID > 0 {
		return bookingReference(financial.ScheduleID)
	}
	return "Booking"
}

func (a *App) listBookingRequestChanges() ([]BookingRequestChange, error) {
	rows, err := a.queryDB(`
		SELECT
			brch.id,
			brch.schedule_id,
			brch.previous_slot_date,
			brch.previous_slot_hour,
			brch.previous_activity,
			brch.previous_quantity,
			brch.previous_quoted_price,
			brch.new_slot_date,
			brch.new_slot_hour,
			brch.new_activity,
			brch.new_quantity,
			brch.new_quoted_price,
			brch.action_type,
			COALESCE(brch.previous_status, ''),
			COALESCE(brch.new_status, ''),
			COALESCE(brch.change_source, ''),
			COALESCE(brch.finance_note, ''),
			brch.review_note,
			COALESCE(brch.customer_message, ''),
			COALESCE(brch.changed_by_user_id, 0),
			COALESCE(u.name, ''),
			brch.changed_at
		FROM booking_request_changes brch
		LEFT JOIN users u
			ON u.id = brch.changed_by_user_id
		ORDER BY brch.changed_at DESC, brch.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []BookingRequestChange
	for rows.Next() {
		var change BookingRequestChange
		if err := rows.Scan(
			&change.ID,
			&change.ScheduleID,
			&change.PreviousSlotDate,
			&change.PreviousSlotHour,
			&change.PreviousActivity,
			&change.PreviousQuantity,
			&change.PreviousQuote,
			&change.NewSlotDate,
			&change.NewSlotHour,
			&change.NewActivity,
			&change.NewQuantity,
			&change.NewQuote,
			&change.ActionType,
			&change.PreviousStatus,
			&change.NewStatus,
			&change.ChangeSource,
			&change.FinanceNote,
			&change.ReviewNote,
			&change.CustomerMessage,
			&change.ChangedByUserID,
			&change.ChangedByUserName,
			&change.ChangedAt,
		); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}

	return changes, rows.Err()
}

func (a *App) listBookingRequestChangesForScheduleIDs(scheduleIDs []int64) ([]BookingRequestChange, error) {
	return listBookingRequestChangesForScheduleIDsQuery(
		a.db,
		a.runtimeConfig.DBDriver,
		scheduleIDs,
	)
}

func (a *App) listActiveSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.queryDB(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE status IN ('pending', 'confirmed')
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		if err := rows.Scan(
			&schedule.ID,
			&schedule.SlotDate,
			&schedule.SlotHour,
			&schedule.EntryType,
			&schedule.Activity,
			&schedule.Quantity,
			&schedule.Title,
			&schedule.Notes,
			&schedule.Status,
			&schedule.RequesterName,
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) listPendingSpaceSchedules() ([]SpaceSchedule, error) {
	return a.listPendingSpaceSchedulesByDivisionIDs(nil)
}

func (a *App) listPendingSpaceSchedulesByDivisionIDs(divisionIDs []int64) ([]SpaceSchedule, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, nil
	}
	rows, err := a.queryDB(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		WHERE status IN ('pending', 'held', 'reschedule_pending')
		ORDER BY slot_date ASC, slot_hour ASC, created_at ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		var statusChangedAt sql.NullTime
		if err := rows.Scan(
			&schedule.ID,
			&schedule.SlotDate,
			&schedule.SlotHour,
			&schedule.EntryType,
			&schedule.Activity,
			&schedule.Quantity,
			&schedule.Title,
			&schedule.Notes,
			&schedule.Status,
			&schedule.RequesterName,
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CustomerMessage,
			&statusChangedAt,
			&schedule.StatusChangedBy,
			&schedule.StatusSource,
			&schedule.CancellationReason,
			&schedule.CancellationFinanceNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if statusChangedAt.Valid {
			schedule.StatusChangedAt = statusChangedAt.Time
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) countPendingSpaceSchedules() (int, error) {
	return a.countPendingSpaceSchedulesByDivisionIDs(nil)
}

func (a *App) countPendingSpaceSchedulesByDivisionIDs(divisionIDs []int64) (int, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, nil
	}
	row := a.queryRowDB(`
		SELECT COUNT(*)
		FROM space_schedules
		WHERE entry_type = 'booking' AND status = 'pending'
	`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *App) countHeldSpaceSchedules() (int, error) {
	return a.countHeldSpaceSchedulesByDivisionIDs(nil)
}

func (a *App) countHeldSpaceSchedulesByDivisionIDs(divisionIDs []int64) (int, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, nil
	}
	row := a.queryRowDB(`
		SELECT COUNT(*)
		FROM space_schedules
		WHERE entry_type = 'booking' AND status = 'held'
	`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *App) listOneToOneOfferings(includeInactive bool) ([]OneToOneOffering, error) {
	query := `
		SELECT id, name, game, audience, COALESCE(occurrence, 'per_day'), COALESCE(session_count, 1), price, active, created_at, updated_at
		FROM one_to_one_offerings`
	args := make([]any, 0, 1)
	if !includeInactive {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY active DESC, name, id`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var offerings []OneToOneOffering
	for rows.Next() {
		var offering OneToOneOffering
		if err := rows.Scan(
			&offering.ID,
			&offering.Name,
			&offering.Game,
			&offering.Audience,
			&offering.Occurrence,
			&offering.SessionCount,
			&offering.Price,
			&offering.Active,
			&offering.CreatedAt,
			&offering.UpdatedAt,
		); err != nil {
			return nil, err
		}
		offerings = append(offerings, offering)
	}
	return offerings, rows.Err()
}

func (a *App) findOneToOneOfferingByID(id int64) (*OneToOneOffering, error) {
	var offering OneToOneOffering
	if err := a.queryRowDB(`
		SELECT id, name, game, audience, COALESCE(occurrence, 'per_day'), COALESCE(session_count, 1), price, active, created_at, updated_at
		FROM one_to_one_offerings
		WHERE id = ?
	`, id).Scan(
		&offering.ID,
		&offering.Name,
		&offering.Game,
		&offering.Audience,
		&offering.Occurrence,
		&offering.SessionCount,
		&offering.Price,
		&offering.Active,
		&offering.CreatedAt,
		&offering.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &offering, nil
}

func (a *App) createOneToOneOffering(offering OneToOneOffering) (int64, error) {
	if strings.TrimSpace(offering.Occurrence) == "" {
		offering.Occurrence = "per_day"
	}
	if offering.Occurrence == "per_day" || offering.SessionCount <= 0 {
		offering.SessionCount = 1
	}
	now := time.Now().UTC()

	return a.insertAndReturnID(`
		INSERT INTO one_to_one_offerings (
			name,
			game,
			audience,
			occurrence,
			session_count,
			price,
			active,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		offering.Name,
		offering.Game,
		offering.Audience,
		offering.Occurrence,
		offering.SessionCount,
		offering.Price,
		boolToInt(offering.Active),
		now,
		now,
	)
}

func (a *App) createGame(game Game) (int64, error) {
	now := time.Now().UTC()

	return a.insertAndReturnID(`
		INSERT INTO games (
			name,
			activity,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		game.Name,
		game.Activity,
		game.Description,
		boolToInt(game.Active),
		game.SortOrder,
		now,
		now,
	)
}

func (a *App) updateGame(game Game) error {
	result, err := a.execDB(`
		UPDATE games
		SET
			name = ?,
			activity = ?,
			description = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
	`,
		game.Name,
		game.Activity,
		game.Description,
		boolToInt(game.Active),
		game.SortOrder,
		time.Now().UTC(),
		game.ID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) deleteGame(id int64) error {
	var offerings int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM one_to_one_offerings o
		JOIN games g
			ON g.activity = o.game
		WHERE g.id = ?
	`, id).Scan(&offerings); err != nil {
		return err
	}
	if offerings > 0 {
		return errors.New("this game is already used by 1 to 1 offerings; set it inactive instead of deleting it")
	}

	result, err := a.execDB(`DELETE FROM games WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) updateOneToOneOffering(offering OneToOneOffering) error {
	if strings.TrimSpace(offering.Occurrence) == "" {
		offering.Occurrence = "per_day"
	}
	if offering.Occurrence == "per_day" || offering.SessionCount <= 0 {
		offering.SessionCount = 1
	}
	result, err := a.execDB(`
		UPDATE one_to_one_offerings
		SET name = ?, game = ?, audience = ?, occurrence = ?, session_count = ?, price = ?, active = ?, updated_at = ?
		WHERE id = ?
	`,
		offering.Name,
		offering.Game,
		offering.Audience,
		offering.Occurrence,
		offering.SessionCount,
		offering.Price,
		boolToInt(offering.Active),
		time.Now().UTC(),
		offering.ID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) deleteOneToOneOffering(id int64) error {
	var bookings int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM one_to_one_bookings
		WHERE offering_id = ?
	`, id).Scan(&bookings); err != nil {
		return err
	}
	if bookings > 0 {
		return errors.New("this 1 to 1 setup already has bookings; set it inactive instead of deleting it")
	}
	result, err := a.execDB(`DELETE FROM one_to_one_offerings WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) listOneToOneBookings() ([]OneToOneBooking, error) {
	rows, err := a.queryDB(`
		SELECT
			ob.id,
			ob.schedule_id,
			ob.offering_id,
			ob.customer_name,
			ob.offering_name,
			ob.game,
			ob.audience,
			COALESCE(ob.occurrence, 'per_day'),
			COALESCE(ob.max_sessions, 1),
			ob.price,
			CASE WHEN COALESCE(ob.discounted_price, -1) < 0 THEN ob.price ELSE ob.discounted_price END,
			COALESCE(ob.coach_fee, 0),
			COALESCE(ob.sessions, 1),
			s.slot_date,
			s.slot_hour,
			s.status,
			s.title,
			s.notes,
			ob.created_at,
			ob.updated_at
		FROM one_to_one_bookings ob
		JOIN space_schedules s
			ON s.id = ob.schedule_id
		ORDER BY s.slot_date DESC, s.slot_hour DESC, ob.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bookings []OneToOneBooking
	for rows.Next() {
		var booking OneToOneBooking
		if err := rows.Scan(
			&booking.ID,
			&booking.ScheduleID,
			&booking.OfferingID,
			&booking.CustomerName,
			&booking.OfferingName,
			&booking.Game,
			&booking.Audience,
			&booking.Occurrence,
			&booking.MaxSessions,
			&booking.Price,
			&booking.DiscountedPrice,
			&booking.CoachFee,
			&booking.Sessions,
			&booking.SlotDate,
			&booking.SlotHour,
			&booking.Status,
			&booking.Title,
			&booking.Notes,
			&booking.CreatedAt,
			&booking.UpdatedAt,
		); err != nil {
			return nil, err
		}
		bookings = append(bookings, booking)
	}
	return bookings, rows.Err()
}

func (a *App) findOneToOneBookingByID(bookingID int64) (*OneToOneBooking, error) {
	row := a.queryRowDB(`
		SELECT
			ob.id,
			ob.schedule_id,
			ob.offering_id,
			ob.customer_name,
			ob.offering_name,
			ob.game,
			ob.audience,
			COALESCE(ob.occurrence, 'per_day'),
			COALESCE(ob.max_sessions, 1),
			ob.price,
			CASE WHEN COALESCE(ob.discounted_price, -1) < 0 THEN ob.price ELSE ob.discounted_price END,
			COALESCE(ob.coach_fee, 0),
			COALESCE(ob.sessions, 1),
			COALESCE(ob.coach_user_id, 0),
			COALESCE(coach.name, ''),
			COALESCE(ob.package_status, ''),
			COALESCE(ob.completed_sessions, 0),
			COALESCE(ob.cancelled_sessions, 0),
			s.slot_date,
			s.slot_hour,
			s.status,
			s.title,
			s.notes,
			ob.created_at,
			ob.updated_at
		FROM one_to_one_bookings ob
		JOIN space_schedules s
			ON s.id = ob.schedule_id
		LEFT JOIN users coach
			ON coach.id = ob.coach_user_id
		WHERE ob.id = ?
	`, bookingID)

	var booking OneToOneBooking
	if err := row.Scan(
		&booking.ID,
		&booking.ScheduleID,
		&booking.OfferingID,
		&booking.CustomerName,
		&booking.OfferingName,
		&booking.Game,
		&booking.Audience,
		&booking.Occurrence,
		&booking.MaxSessions,
		&booking.Price,
		&booking.DiscountedPrice,
		&booking.CoachFee,
		&booking.Sessions,
		&booking.CoachUserID,
		&booking.CoachName,
		&booking.PackageStatus,
		&booking.CompletedSessions,
		&booking.CancelledSessions,
		&booking.SlotDate,
		&booking.SlotHour,
		&booking.Status,
		&booking.Title,
		&booking.Notes,
		&booking.CreatedAt,
		&booking.UpdatedAt,
	); err != nil {
		return nil, err
	}

	return &booking, nil
}

func resolveOneToOneCourtActivity(
	offering OneToOneOffering,
	games []Game,
	activities []CourtActivity,
) (string, error) {
	gameSlug := strings.TrimSpace(offering.Game)
	if gameSlug == "" {
		return "", errors.New("1 to 1 offering has no game configured")
	}

	// First preserve the existing/direct configuration model.
	// Many games use the same internal slug as the court activity
	// (for example badminton -> badminton, tennis -> tennis).
	for _, activity := range activities {
		if !activity.Active {
			continue
		}
		if strings.EqualFold(
			strings.TrimSpace(activity.Activity),
			gameSlug,
		) {
			return strings.TrimSpace(activity.Activity), nil
		}
	}

	// If there is no direct activity match, resolve through the Game record.
	// This supports games whose product slug differs from the physical
	// court activity, for example:
	// cricket -> full_indoor_cricket.
	var gameID int64
	for _, game := range games {
		if !game.Active {
			continue
		}
		if strings.EqualFold(
			strings.TrimSpace(game.Activity),
			gameSlug,
		) {
			gameID = game.ID
			break
		}
	}

	if gameID <= 0 {
		return "", fmt.Errorf(
			"the selected 1 to 1 game %q is no longer available",
			gameSlug,
		)
	}

	for _, activity := range activities {
		if !activity.Active {
			continue
		}
		if activity.GameID != gameID {
			continue
		}

		courtActivity := strings.TrimSpace(activity.Activity)
		if courtActivity != "" {
			return courtActivity, nil
		}
	}

	return "", fmt.Errorf(
		"the selected 1 to 1 game %q is not linked to an active court activity",
		gameSlug,
	)
}

func (a *App) createOneToOneBooking(offering OneToOneOffering, customerName, slotDate, slotHour string, sessions int, discountedPrice float64, coachUserID int64, coachFee float64, notes string, referralCode string) (int64, int64, error) {
	if sessions <= 0 {
		sessions = 1
	}
	if offering.Occurrence == "per_day" {
		sessions = 1
	}
	if offering.SessionCount <= 0 {
		offering.SessionCount = 1
	}
	if sessions > offering.SessionCount {
		return 0, 0, fmt.Errorf("sessions cannot exceed the configured limit of %d", offering.SessionCount)
	}
	if discountedPrice < 0 {
		discountedPrice = offering.Price
	}
	if discountedPrice > offering.Price {
		return 0, 0, errors.New("final package price cannot exceed the standard price")
	}
	if coachUserID <= 0 {
		return 0, 0, errors.New("select a coach")
	}
	if coachFee < 0 {
		return 0, 0, errors.New("coach fee must be zero or greater")
	}
	courtActivities, courtLayouts, err := a.activeBookingConfiguration()
	if err != nil {
		return 0, 0, fmt.Errorf("load active court configuration: %w", err)
	}

	games, err := a.listGames(false)
	if err != nil {
		return 0, 0, fmt.Errorf("load games for 1 to 1 booking: %w", err)
	}

	courtActivity, err := resolveOneToOneCourtActivity(
		offering,
		games,
		courtActivities,
	)
	if err != nil {
		return 0, 0, err
	}

	sessionCoachFee := oneToOnePackageCoachFeePerSession(
		coachFee,
		sessions,
	)

	schedule := SpaceSchedule{
		SlotDate:      slotDate,
		SlotHour:      slotHour,
		EntryType:     "booking",
		Activity:      courtActivity,
		Quantity:      1,
		Title:         fmt.Sprintf("1 to 1 · %s · %s", offering.Name, customerName),
		Notes:         buildOneToOneBookingNotes(offering, sessions, discountedPrice, coachFee, notes),
		RequesterName: customerName,
		ReferralCode:  strings.ToUpper(strings.TrimSpace(referralCode)),
		QuotedPrice:   discountedPrice,
	}
	if err := validateConfiguredBookingOption(schedule, courtActivities, courtLayouts); err != nil {
		return 0, 0, err
	}
	courtClosures, err := a.listActiveCourtClosures()
	if err != nil {
		return 0, 0, fmt.Errorf("load active court closures: %w", err)
	}
	if err := validateScheduleAgainstClosures(schedule, courtClosures); err != nil {
		return 0, 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	existing, err := querySchedulesForSlot(
		tx,
		a.runtimeConfig.DBDriver,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
	)
	if err != nil {
		return 0, 0, err
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, schedule, courtLayouts); err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC()
	scheduleID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO space_schedules (
			slot_date,
			slot_hour,
			entry_type,
			activity,
			quantity,
			title,
			notes,
			status,
			requester_name,
			requester_email,
			requester_phone,
			requested_by_user_id,
			review_note,
			customer_message,
			status_changed_at,
			status_changed_by_user_id,
			status_change_source,
			cancellation_reason,
			cancellation_finance_note,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', NULL, '', '', NULL, NULL, '', '', '', ?, ?)
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		bookingStatusConfirmed,
		schedule.RequesterName,
		now,
		now,
	)
	if err != nil {
		return 0, 0, err
	}

	if err != nil {
		return 0, 0, err
	}
	if _, err := a.execTxDB(tx, `
		INSERT INTO booking_financials (
			schedule_id,
			quoted_amount,
			paid,
			payment_method,
			created_at,
			updated_at
		)
		VALUES (?, ?, 0, '', ?, ?)
	`, scheduleID, discountedPrice, now, now); err != nil {
		return 0, 0, err
	}
	if err := a.createBookingReferralTx(tx, scheduleID, schedule.ReferralCode, now); err != nil {
		return 0, 0, err
	}
	bookingID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO one_to_one_bookings (
			schedule_id,
			offering_id,
			customer_name,
			offering_name,
			game,
			audience,
			price,
			discounted_price,
			coach_fee,
			sessions,
			occurrence,
			max_sessions,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, scheduleID, offering.ID, customerName, offering.Name, offering.Game, offering.Audience, offering.Price, discountedPrice, coachFee, sessions, offering.Occurrence, offering.SessionCount, now, now)
	if err != nil {
		return 0, 0, err
	}

	// A 1-to-1 booking represents the purchased package. Each actual
	// appointment is tracked separately as a booking session.
	// Creation schedules session #1 only; the remaining purchased sessions
	// stay unscheduled until their individual dates/times are selected.
	if _, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO one_to_one_booking_sessions (
			booking_id,
			schedule_id,
			session_number,
			coach_user_id,
			coach_fee,
			status,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		bookingID,
		scheduleID,
		1,
		coachUserID,
		sessionCoachFee,
		"scheduled",
		now,
		now,
	); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return bookingID, scheduleID, nil
}

func (a *App) listOneToOneBookingSessions(bookingID int64) ([]OneToOneBookingSession, error) {
	rows, err := a.queryDB(`
		SELECT
			ots.id,
			ots.booking_id,
			ots.schedule_id,
			ots.session_number,
			COALESCE(ots.coach_user_id, 0),
			COALESCE(u.name, ''),
			CASE
				WHEN COALESCE(ob.sessions, 0) > 0
					THEN COALESCE(ob.coach_fee, 0) / ob.sessions
				ELSE COALESCE(ob.coach_fee, 0)
			END,
			ss.slot_date,
			ss.slot_hour,
			ots.status,
			COALESCE(ots.attendance_status, ''),
			COALESCE(ots.attendance_note, ''),
			ots.attendance_marked_at,
			COALESCE(ots.attendance_marked_by_user_id, 0),
			COALESCE(attendance_user.name, ''),
			COALESCE(ots.notes, ''),
			ots.completed_at,
			COALESCE(ots.completed_by_user_id, 0),
			ots.cancelled_at,
			ots.created_at,
			ots.updated_at
		FROM one_to_one_booking_sessions ots
		JOIN one_to_one_bookings ob
			ON ob.id = ots.booking_id
		JOIN space_schedules ss
			ON ss.id = ots.schedule_id
		LEFT JOIN users u
			ON u.id = ots.coach_user_id
		LEFT JOIN users attendance_user
			ON attendance_user.id = ots.attendance_marked_by_user_id
		WHERE ots.booking_id = ?
		ORDER BY ots.session_number ASC, ots.id ASC
	`, bookingID)
	if err != nil {
		return a.listOneToOneBookingSessionsCompatibility(
			bookingID,
			err,
		)
	}
	defer rows.Close()

	var sessions []OneToOneBookingSession

	for rows.Next() {
		var session OneToOneBookingSession
		var completedAt sql.NullTime
		var cancelledAt sql.NullTime
		var attendanceMarkedAt sql.NullTime

		if err := rows.Scan(
			&session.ID,
			&session.BookingID,
			&session.ScheduleID,
			&session.SessionNumber,
			&session.CoachUserID,
			&session.CoachName,
			&session.CoachFee,
			&session.SlotDate,
			&session.SlotHour,
			&session.Status,
			&session.AttendanceStatus,
			&session.AttendanceNote,
			&attendanceMarkedAt,
			&session.AttendanceMarkedByUserID,
			&session.AttendanceMarkedByUserName,
			&session.Notes,
			&completedAt,
			&session.CompletedByUserID,
			&cancelledAt,
			&session.CreatedAt,
			&session.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if completedAt.Valid {
			session.CompletedAt = completedAt.Time
		}
		if attendanceMarkedAt.Valid {
			session.AttendanceMarkedAt = attendanceMarkedAt.Time
		}
		if cancelledAt.Valid {
			session.CancelledAt = cancelledAt.Time
		}

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sessions, nil
}

func (a *App) listOneToOneBookingSessionsCompatibility(
	bookingID int64,
	queryErr error,
) ([]OneToOneBookingSession, error) {
	legacySessions, err := a.listOneToOneBookingSessionsLegacy(
		bookingID,
	)
	if err == nil {
		return legacySessions, nil
	}

	derivedSessions, deriveErr := a.listOneToOneBookingSessionsDerived(
		bookingID,
	)
	if deriveErr == nil {
		return derivedSessions, nil
	}

	return nil, queryErr
}

func (a *App) listOneToOneBookingSessionsLegacy(
	bookingID int64,
) ([]OneToOneBookingSession, error) {
	rows, err := a.queryDB(`
		SELECT
			ots.id,
			ots.booking_id,
			ots.schedule_id,
			ots.session_number,
			COALESCE(ots.coach_user_id, 0),
			COALESCE(u.name, ''),
			CASE
				WHEN COALESCE(ob.sessions, 0) > 0
					THEN COALESCE(ob.coach_fee, 0) / ob.sessions
				ELSE COALESCE(ob.coach_fee, 0)
			END,
			ss.slot_date,
			ss.slot_hour,
			ots.status,
			COALESCE(ots.notes, ''),
			ots.completed_at,
			COALESCE(ots.completed_by_user_id, 0),
			ots.cancelled_at,
			ots.created_at,
			ots.updated_at
		FROM one_to_one_booking_sessions ots
		JOIN one_to_one_bookings ob
			ON ob.id = ots.booking_id
		JOIN space_schedules ss
			ON ss.id = ots.schedule_id
		LEFT JOIN users u
			ON u.id = ots.coach_user_id
		WHERE ots.booking_id = ?
		ORDER BY ots.session_number ASC, ots.id ASC
	`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]OneToOneBookingSession, 0)

	for rows.Next() {
		var session OneToOneBookingSession
		var completedAt sql.NullTime
		var cancelledAt sql.NullTime

		if err := rows.Scan(
			&session.ID,
			&session.BookingID,
			&session.ScheduleID,
			&session.SessionNumber,
			&session.CoachUserID,
			&session.CoachName,
			&session.CoachFee,
			&session.SlotDate,
			&session.SlotHour,
			&session.Status,
			&session.Notes,
			&completedAt,
			&session.CompletedByUserID,
			&cancelledAt,
			&session.CreatedAt,
			&session.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if completedAt.Valid {
			session.CompletedAt = completedAt.Time
		}
		if cancelledAt.Valid {
			session.CancelledAt = cancelledAt.Time
		}

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sessions, nil
}

func (a *App) listOneToOneBookingSessionsDerived(
	bookingID int64,
) ([]OneToOneBookingSession, error) {
	var session OneToOneBookingSession

	err := a.queryRowDB(`
		SELECT
			ob.id,
			ob.id,
			ob.schedule_id,
			1,
			0,
			'',
			CASE
				WHEN COALESCE(ob.sessions, 0) > 0
					THEN COALESCE(ob.coach_fee, 0) / ob.sessions
				ELSE COALESCE(ob.coach_fee, 0)
			END,
			ss.slot_date,
			ss.slot_hour,
			ss.status,
			ss.notes,
			ss.created_at,
			ss.updated_at
		FROM one_to_one_bookings ob
		JOIN space_schedules ss
			ON ss.id = ob.schedule_id
		WHERE ob.id = ?
	`, bookingID).Scan(
		&session.ID,
		&session.BookingID,
		&session.ScheduleID,
		&session.SessionNumber,
		&session.CoachUserID,
		&session.CoachName,
		&session.CoachFee,
		&session.SlotDate,
		&session.SlotHour,
		&session.Status,
		&session.Notes,
		&session.CreatedAt,
		&session.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(session.Status)) {
	case "completed":
		session.CompletedAt = session.UpdatedAt
	case "cancelled":
		session.CancelledAt = session.UpdatedAt
	}

	return []OneToOneBookingSession{session}, nil
}

func (a *App) saveOneToOneSessionAttendance(
	sessionID int64,
	attendanceStatus string,
	attendanceNote string,
	recordedByUserID int64,
) error {
	if sessionID <= 0 {
		return errors.New("invalid 1 to 1 session")
	}

	attendanceStatus = normalizeOneToOneAttendanceStatus(attendanceStatus)
	if attendanceStatus == "" {
		return errors.New("select a valid attendance status")
	}
	attendanceNote = strings.TrimSpace(attendanceNote)

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		bookingID     int64
		scheduleID    int64
		sessionStatus string
	)

	if err := a.queryRowTxDB(tx, `
		SELECT
			booking_id,
			schedule_id,
			status
		FROM one_to_one_booking_sessions
		WHERE id = ?
	`, sessionID).Scan(
		&bookingID,
		&scheduleID,
		&sessionStatus,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("1 to 1 session was not found")
		}
		return err
	}

	if sessionStatus == "cancelled" {
		return errors.New("attendance cannot be recorded for a cancelled 1 to 1 session")
	}

	now := time.Now().UTC()

	var recordedBy any
	if recordedByUserID > 0 {
		recordedBy = recordedByUserID
	}

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_booking_sessions
		SET
			attendance_status = ?,
			attendance_note = ?,
			attendance_marked_at = ?,
			attendance_marked_by_user_id = ?,
			updated_at = ?
		WHERE id = ?
	`, attendanceStatus, attendanceNote, now, recordedBy, now, sessionID); err != nil {
		return err
	}

	if sessionStatus == "scheduled" || sessionStatus == bookingStatusConfirmed {
		if _, err := a.execTxDB(tx, `
			UPDATE one_to_one_booking_sessions
			SET
				status = 'completed',
				completed_at = ?,
				completed_by_user_id = ?,
				cancelled_at = NULL,
				updated_at = ?
			WHERE id = ?
		`, now, recordedBy, now, sessionID); err != nil {
			return err
		}

		if _, err := a.execTxDB(tx, `
			UPDATE space_schedules
			SET
				status = 'completed',
				status_changed_at = ?,
				status_changed_by_user_id = ?,
				status_change_source = 'one_to_one_session',
				updated_at = ?
			WHERE id = ?
		`, now, recordedBy, now, scheduleID); err != nil {
			return err
		}

		if err := a.refreshOneToOnePackageProgressTx(tx, bookingID, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) scheduleNextOneToOneSession(
	bookingID int64,
	slotDate string,
	slotHour string,
	coachUserID int64,
	notes string,
) (int64, int64, error) {
	if bookingID <= 0 {
		return 0, 0, errors.New("invalid 1 to 1 booking")
	}

	var booking OneToOneBooking

	err := a.queryRowDB(`
		SELECT
			id,
			schedule_id,
			offering_id,
			customer_name,
			offering_name,
			game,
			audience,
			price,
			discounted_price,
			coach_fee,
			sessions,
			occurrence,
			max_sessions,
			COALESCE(coach_user_id, 0),
			package_status,
			completed_sessions,
			cancelled_sessions,
			created_at,
			updated_at
		FROM one_to_one_bookings
		WHERE id = ?
	`, bookingID).Scan(
		&booking.ID,
		&booking.ScheduleID,
		&booking.OfferingID,
		&booking.CustomerName,
		&booking.OfferingName,
		&booking.Game,
		&booking.Audience,
		&booking.Price,
		&booking.DiscountedPrice,
		&booking.CoachFee,
		&booking.Sessions,
		&booking.Occurrence,
		&booking.MaxSessions,
		&booking.CoachUserID,
		&booking.PackageStatus,
		&booking.CompletedSessions,
		&booking.CancelledSessions,
		&booking.CreatedAt,
		&booking.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, errors.New("1 to 1 booking was not found")
		}
		return 0, 0, err
	}

	if booking.PackageStatus != "active" {
		return 0, 0, errors.New("this 1 to 1 package is not active")
	}

	// Cancelled appointments do not consume the purchased package allowance.
	// Keep their rows for audit history, but permit a replacement appointment.
	var consumingSessionCount int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM one_to_one_booking_sessions
		WHERE booking_id = ?
			AND status <> 'cancelled'
	`, bookingID).Scan(&consumingSessionCount); err != nil {
		return 0, 0, err
	}

	if consumingSessionCount >= booking.Sessions {
		return 0, 0, errors.New("all purchased sessions have already been scheduled")
	}

	// Session numbers identify appointment records and must never be reused.
	// A cancelled session therefore remains #N and its replacement receives
	// the next sequence number.
	var maxSessionNumber int
	if err := a.queryRowDB(`
		SELECT COALESCE(MAX(session_number), 0)
		FROM one_to_one_booking_sessions
		WHERE booking_id = ?
	`, bookingID).Scan(&maxSessionNumber); err != nil {
		return 0, 0, err
	}

	nextSessionNumber := maxSessionNumber + 1

	courtActivities, courtLayouts, err := a.activeBookingConfiguration()
	if err != nil {
		return 0, 0, fmt.Errorf("load active court configuration: %w", err)
	}

	games, err := a.listGames(false)
	if err != nil {
		return 0, 0, fmt.Errorf("load games for 1 to 1 session: %w", err)
	}

	offering := OneToOneOffering{
		ID:   booking.OfferingID,
		Game: booking.Game,
	}

	courtActivity, err := resolveOneToOneCourtActivity(
		offering,
		games,
		courtActivities,
	)
	if err != nil {
		return 0, 0, err
	}

	schedule := SpaceSchedule{
		SlotDate:      strings.TrimSpace(slotDate),
		SlotHour:      strings.TrimSpace(slotHour),
		EntryType:     "booking",
		Activity:      courtActivity,
		Quantity:      1,
		Title:         fmt.Sprintf("1 to 1 · %s · %s · Session %d", booking.OfferingName, booking.CustomerName, nextSessionNumber),
		Notes:         strings.TrimSpace(notes),
		RequesterName: booking.CustomerName,
		QuotedPrice:   0,
	}

	if err := validateSpaceScheduleInput(schedule); err != nil {
		return 0, 0, err
	}

	if err := validateAdminScheduleDate(schedule); err != nil {
		return 0, 0, err
	}

	if err := validateConfiguredBookingOption(schedule, courtActivities, courtLayouts); err != nil {
		return 0, 0, err
	}

	courtClosures, err := a.listActiveCourtClosures()
	if err != nil {
		return 0, 0, fmt.Errorf("load active court closures: %w", err)
	}

	if err := validateScheduleAgainstClosures(schedule, courtClosures); err != nil {
		return 0, 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	existing, err := querySchedulesForSlot(
		tx,
		a.runtimeConfig.DBDriver,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
	)
	if err != nil {
		return 0, 0, err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(existing, schedule, courtLayouts); err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC()
	sessionCoachFee := oneToOnePackageCoachFeePerSession(
		booking.CoachFee,
		booking.Sessions,
	)

	scheduleID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO space_schedules (
			slot_date,
			slot_hour,
			entry_type,
			activity,
			quantity,
			title,
			notes,
			status,
			requester_name,
			requester_email,
			requester_phone,
			requested_by_user_id,
			review_note,
			customer_message,
			status_changed_at,
			status_changed_by_user_id,
			status_change_source,
			cancellation_reason,
			cancellation_finance_note,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', NULL, '', '', NULL, NULL, '', '', '', ?, ?)
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		bookingStatusConfirmed,
		schedule.RequesterName,
		now,
		now,
	)
	if err != nil {
		return 0, 0, err
	}

	var sessionCoachUserID any
	if coachUserID > 0 {
		sessionCoachUserID = coachUserID
	}

	sessionID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO one_to_one_booking_sessions (
			booking_id,
			schedule_id,
			session_number,
			coach_user_id,
			coach_fee,
			status,
			notes,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		bookingID,
		scheduleID,
		nextSessionNumber,
		sessionCoachUserID,
		sessionCoachFee,
		"scheduled",
		strings.TrimSpace(notes),
		now,
		now,
	)
	if err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	return sessionID, scheduleID, nil
}

func (a *App) updateOneToOneBookingPackage(
	bookingID int64,
	customerName string,
	sessions int,
	discountedPrice float64,
	coachUserID int64,
	coachFee float64,
	notes string,
) error {
	if bookingID <= 0 {
		return errors.New("invalid 1 to 1 package")
	}

	customerName = strings.TrimSpace(customerName)
	if customerName == "" {
		return errors.New("customer name is required")
	}

	if sessions <= 0 {
		return errors.New("valid sessions count is required")
	}

	if coachUserID <= 0 {
		return errors.New("select a coach")
	}

	if coachFee < 0 {
		return errors.New("coach fee must be zero or greater")
	}

	notes = strings.TrimSpace(notes)

	var booking OneToOneBooking
	if err := a.queryRowDB(`
		SELECT
			id,
			schedule_id,
			offering_id,
			customer_name,
			offering_name,
			game,
			audience,
			COALESCE(occurrence, 'per_day'),
			COALESCE(max_sessions, 1),
			price,
			CASE WHEN COALESCE(discounted_price, -1) < 0 THEN price ELSE discounted_price END,
			COALESCE(coach_fee, 0),
			COALESCE(sessions, 1),
			COALESCE(coach_user_id, 0),
			COALESCE(package_status, ''),
			COALESCE(completed_sessions, 0),
			COALESCE(cancelled_sessions, 0),
			created_at,
			updated_at
		FROM one_to_one_bookings
		WHERE id = ?
	`, bookingID).Scan(
		&booking.ID,
		&booking.ScheduleID,
		&booking.OfferingID,
		&booking.CustomerName,
		&booking.OfferingName,
		&booking.Game,
		&booking.Audience,
		&booking.Occurrence,
		&booking.MaxSessions,
		&booking.Price,
		&booking.DiscountedPrice,
		&booking.CoachFee,
		&booking.Sessions,
		&booking.CoachUserID,
		&booking.PackageStatus,
		&booking.CompletedSessions,
		&booking.CancelledSessions,
		&booking.CreatedAt,
		&booking.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("1 to 1 package was not found")
		}
		return err
	}

	if booking.Occurrence == "per_day" {
		sessions = 1
	}

	if booking.MaxSessions <= 0 {
		booking.MaxSessions = 1
	}

	if sessions > booking.MaxSessions {
		return fmt.Errorf(
			"sessions cannot exceed the configured limit of %d",
			booking.MaxSessions,
		)
	}

	if discountedPrice < 0 {
		return errors.New("final package price must be zero or greater")
	}

	if discountedPrice > booking.Price {
		return errors.New("final package price cannot exceed the standard price")
	}

	var consumingSessionCount int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM one_to_one_booking_sessions
		WHERE booking_id = ?
		  AND status <> 'cancelled'
	`, bookingID).Scan(&consumingSessionCount); err != nil {
		return err
	}

	if sessions < consumingSessionCount {
		return fmt.Errorf(
			"sessions cannot be reduced below %d because that many appointments already exist on this package",
			consumingSessionCount,
		)
	}

	if sessions < booking.CompletedSessions {
		return fmt.Errorf(
			"sessions cannot be reduced below %d completed appointments",
			booking.CompletedSessions,
		)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	packageNotes := buildOneToOneBookingNotes(
		OneToOneOffering{
			Name:         booking.OfferingName,
			Game:         booking.Game,
			Audience:     booking.Audience,
			Occurrence:   booking.Occurrence,
			SessionCount: booking.MaxSessions,
			Price:        booking.Price,
		},
		sessions,
		discountedPrice,
		coachFee,
		notes,
	)

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_bookings
		SET
			customer_name = ?,
			discounted_price = ?,
			coach_user_id = ?,
			coach_fee = ?,
			sessions = ?,
			updated_at = ?
		WHERE id = ?
	`,
		customerName,
		discountedPrice,
		coachUserID,
		coachFee,
		sessions,
		now,
		bookingID,
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE space_schedules
		SET
			title = ?,
			notes = ?,
			requester_name = ?,
			updated_at = ?
		WHERE id = ?
	`,
		fmt.Sprintf("1 to 1 · %s · %s", booking.OfferingName, customerName),
		packageNotes,
		customerName,
		now,
		booking.ScheduleID,
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE booking_financials
		SET
			quoted_amount = ?,
			updated_at = ?
		WHERE schedule_id = ?
	`,
		discountedPrice,
		now,
		booking.ScheduleID,
	); err != nil {
		return err
	}

	sessionCoachFee := oneToOnePackageCoachFeePerSession(
		coachFee,
		sessions,
	)

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_booking_sessions
		SET
			coach_user_id = ?,
			coach_fee = ?,
			updated_at = ?
		WHERE booking_id = ?
	`,
		coachUserID,
		sessionCoachFee,
		now,
		bookingID,
	); err != nil {
		return err
	}

	if err := a.refreshOneToOnePackageProgressTx(tx, bookingID, now); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) refreshOneToOnePackageProgressTx(
	tx *sql.Tx,
	bookingID int64,
	now time.Time,
) error {
	var (
		purchasedSessions int
		completedSessions int
		cancelledSessions int
	)

	if err := a.queryRowTxDB(tx, `
		SELECT sessions
		FROM one_to_one_bookings
		WHERE id = ?
	`, bookingID).Scan(&purchasedSessions); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("1 to 1 booking was not found")
		}
		return err
	}

	if err := a.queryRowTxDB(tx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END), 0)
		FROM one_to_one_booking_sessions
		WHERE booking_id = ?
	`, bookingID).Scan(
		&completedSessions,
		&cancelledSessions,
	); err != nil {
		return err
	}

	packageStatus := "active"
	if purchasedSessions > 0 && completedSessions >= purchasedSessions {
		packageStatus = "completed"
	}

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_bookings
		SET
			completed_sessions = ?,
			cancelled_sessions = ?,
			package_status = ?,
			updated_at = ?
		WHERE id = ?
	`,
		completedSessions,
		cancelledSessions,
		packageStatus,
		now,
		bookingID,
	); err != nil {
		return err
	}

	return nil
}

func (a *App) completeOneToOneSession(
	sessionID int64,
	completedByUserID int64,
) error {
	if sessionID <= 0 {
		return errors.New("invalid 1 to 1 session")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		bookingID  int64
		scheduleID int64
		status     string
	)

	if err := a.queryRowTxDB(tx, `
		SELECT
			booking_id,
			schedule_id,
			status
		FROM one_to_one_booking_sessions
		WHERE id = ?
	`, sessionID).Scan(
		&bookingID,
		&scheduleID,
		&status,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("1 to 1 session was not found")
		}
		return err
	}

	if status != "scheduled" && status != bookingStatusConfirmed {
		if status == "completed" {
			return errors.New("this 1 to 1 session is already completed")
		}
		if status == "cancelled" {
			return errors.New("a cancelled 1 to 1 session cannot be completed")
		}
		return fmt.Errorf("1 to 1 session cannot be completed from status %q", status)
	}

	now := time.Now().UTC()

	var completedBy any
	if completedByUserID > 0 {
		completedBy = completedByUserID
	}

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_booking_sessions
		SET
			status = 'completed',
			completed_at = ?,
			completed_by_user_id = ?,
			cancelled_at = NULL,
			updated_at = ?
		WHERE id = ?
	`, now, completedBy, now, sessionID); err != nil {
		return err
	}

	// Keep the linked appointment in the central schedule history while
	// marking it as completed so it no longer represents an outstanding
	// appointment.
	if _, err := a.execTxDB(tx, `
		UPDATE space_schedules
		SET
			status = 'completed',
			status_changed_at = ?,
			status_changed_by_user_id = ?,
			status_change_source = 'one_to_one_session',
			updated_at = ?
		WHERE id = ?
	`, now, completedBy, now, scheduleID); err != nil {
		return err
	}

	if err := a.refreshOneToOnePackageProgressTx(tx, bookingID, now); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) cancelOneToOneSession(
	sessionID int64,
	cancelledByUserID int64,
	reason string,
) error {
	if sessionID <= 0 {
		return errors.New("invalid 1 to 1 session")
	}

	reason = strings.TrimSpace(reason)

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		bookingID  int64
		scheduleID int64
		status     string
	)

	if err := a.queryRowTxDB(tx, `
		SELECT
			booking_id,
			schedule_id,
			status
		FROM one_to_one_booking_sessions
		WHERE id = ?
	`, sessionID).Scan(
		&bookingID,
		&scheduleID,
		&status,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("1 to 1 session was not found")
		}
		return err
	}

	if status != "scheduled" && status != bookingStatusConfirmed {
		if status == "cancelled" {
			return errors.New("this 1 to 1 session is already cancelled")
		}
		if status == "completed" {
			return errors.New("a completed 1 to 1 session cannot be cancelled")
		}
		return fmt.Errorf("1 to 1 session cannot be cancelled from status %q", status)
	}

	now := time.Now().UTC()

	var cancelledBy any
	if cancelledByUserID > 0 {
		cancelledBy = cancelledByUserID
	}

	if _, err := a.execTxDB(tx, `
		UPDATE one_to_one_booking_sessions
		SET
			status = 'cancelled',
			cancelled_at = ?,
			completed_at = NULL,
			completed_by_user_id = NULL,
			updated_at = ?
		WHERE id = ?
	`, now, now, sessionID); err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE space_schedules
		SET
			status = 'cancelled',
			status_changed_at = ?,
			status_changed_by_user_id = ?,
			status_change_source = 'one_to_one_session',
			cancellation_reason = ?,
			updated_at = ?
		WHERE id = ?
	`,
		now,
		cancelledBy,
		reason,
		now,
		scheduleID,
	); err != nil {
		return err
	}

	if err := a.refreshOneToOnePackageProgressTx(tx, bookingID, now); err != nil {
		return err
	}

	return tx.Commit()
}

func buildOneToOneBookingNotes(offering OneToOneOffering, sessions int, discountedPrice float64, coachFee float64, notes string) string {
	base := fmt.Sprintf(
		"1 to 1 booking\nProgramme: %s\nGame: %s\nWho: %s\nOccurrence: %s\nAllowed sessions: %d\nBooked sessions: %d\nStandard price: %.2f\nDiscounted price: %.2f\nCoach fee total: %.2f\nCoach fee per session: %.2f",
		offering.Name,
		offering.Game,
		offering.Audience,
		offering.Occurrence,
		offering.SessionCount,
		sessions,
		normalizeMoney(offering.Price),
		normalizeMoney(discountedPrice),
		normalizeMoney(coachFee),
		oneToOnePackageCoachFeePerSession(coachFee, sessions),
	)
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return base
	}
	return base + "\nNotes: " + notes
}

func oneToOnePackageCoachFeePerSession(
	coachFee float64,
	sessions int,
) float64 {
	coachFee = normalizeMoney(coachFee)
	if sessions <= 0 {
		return coachFee
	}

	return normalizeMoney(coachFee / float64(sessions))
}

func extractOneToOneBookingNote(notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return ""
	}

	const marker = "\nNotes: "
	idx := strings.LastIndex(notes, marker)
	if idx == -1 {
		return ""
	}

	return strings.TrimSpace(notes[idx+len(marker):])
}

func (a *App) countReschedulePendingSpaceSchedules() (int, error) {
	return a.countReschedulePendingSpaceSchedulesByDivisionIDs(nil)
}

func (a *App) countReschedulePendingSpaceSchedulesByDivisionIDs(divisionIDs []int64) (int, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, nil
	}
	row := a.queryRowDB(`
		SELECT COUNT(*)
		FROM space_schedules
		WHERE entry_type = 'booking' AND status = 'reschedule_pending'
	`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *App) schedulesForSlot(slotDate, slotHour string, excludeID int64) ([]SpaceSchedule, error) {
	return querySchedulesForSlot(
		a.db,
		a.runtimeConfig.DBDriver,
		slotDate,
		slotHour,
		excludeID,
	)
}

type scheduleQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func querySchedulesForSlot(
	queryer scheduleQueryer,
	driver DatabaseDriver,
	slotDate,
	slotHour string,
	excludeID int64,
) ([]SpaceSchedule, error) {
	query := `
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		WHERE slot_date = ? AND id != ? AND status IN ('pending', 'held', 'confirmed', 'reschedule_pending')
		ORDER BY id ASC
	`

	// queryer may be a raw *sql.DB or *sql.Tx, so rebind SQLite-style
	// placeholders before executing against PostgreSQL.
	query = rebindDatabaseQuery(driver, query)

	rows, err := queryer.Query(query, slotDate, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		var statusChangedAt sql.NullTime
		if err := rows.Scan(
			&schedule.ID,
			&schedule.SlotDate,
			&schedule.SlotHour,
			&schedule.EntryType,
			&schedule.Activity,
			&schedule.Quantity,
			&schedule.Title,
			&schedule.Notes,
			&schedule.Status,
			&schedule.RequesterName,
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CustomerMessage,
			&statusChangedAt,
			&schedule.StatusChangedBy,
			&schedule.StatusSource,
			&schedule.CancellationReason,
			&schedule.CancellationFinanceNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if statusChangedAt.Valid {
			schedule.StatusChangedAt = statusChangedAt.Time
		}
		if scheduleOverlapsSlot(schedule, slotDate, slotHour) {
			schedules = append(schedules, schedule)
		}
	}
	return schedules, rows.Err()
}

func (a *App) listActiveCoaches() ([]User, error) {
	// Coach activation belongs to coach_profiles, not users.
	// Reuse the canonical coach-directory query so 1-to-1 scheduling
	// follows the same role/profile/active semantics as coach management.
	return a.listCoachUsersDetailed(false)
}

func (a *App) listCoachesForGroup(groupID int64) ([]User, error) {
	rows, err := a.queryDB(`
		SELECT
			u.id,
			u.email,
			u.name,
			u.email_verified_at,
			u.created_at
		FROM users u
		JOIN student_group_coaches sgc
			ON sgc.user_id = u.id
		JOIN user_roles ur
			ON ur.user_id = u.id
		JOIN roles r
			ON r.id = ur.role_id
		WHERE sgc.group_id = ?
			AND r.name = 'coach'
		ORDER BY LOWER(u.name) ASC, u.id ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var coaches []User

	for rows.Next() {
		var coach User
		var verifiedAt sql.NullTime

		if err := rows.Scan(
			&coach.ID,
			&coach.Email,
			&coach.Name,
			&verifiedAt,
			&coach.CreatedAt,
		); err != nil {
			return nil, err
		}

		coach.Verified = verifiedAt.Valid
		coach.Roles = []string{"coach"}
		coaches = append(coaches, coach)
	}

	return coaches, rows.Err()
}

func (a *App) listStudentsForGroup(groupID int64) ([]Admission, error) {
	rows, err := a.queryDB(`
		SELECT a.id, a.student_id, a.full_name, COALESCE(a.admission_date, ''), a.date_of_birth, a.gender, a.address, a.passport_number, a.school,
		       a.guardian_name, a.guardian_relationship, a.guardian_contact_number, a.guardian_alternative_contact_number,
		       a.medical_information, a.created_at
		FROM admissions a
		JOIN student_group_members sgm ON sgm.admission_id = a.id
		WHERE sgm.group_id = ?
		ORDER BY a.full_name ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var students []Admission
	for rows.Next() {
		var admission Admission
		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.Address,
			&admission.PassportNumber,
			&admission.School,
			&admission.GuardianName,
			&admission.GuardianRelationship,
			&admission.GuardianContactNumber,
			&admission.GuardianAlternativePhone,
			&admission.MedicalInformation,
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		students = append(students, admission)
	}
	return students, rows.Err()
}

func (a *App) createAdmission(admission Admission) error {
	_, _, err := a.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	return err
}
func replaceStudentGroupCoachesTx(
	a *App,
	tx *sql.Tx,
	driver DatabaseDriver,
	groupID int64,
	coachIDs []int64,
) error {
	if err := syncStudentGroupCoachHistoryTx(
		a,
		tx,
		groupID,
		coachIDs,
		currentBusinessDate(),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		rebindDatabaseQuery(
			driver,
			`DELETE FROM student_group_coaches WHERE group_id = ?`,
		),
		groupID,
	); err != nil {
		return err
	}

	now := time.Now().UTC()

	for _, coachID := range coachIDs {
		result, err := tx.Exec(
			rebindDatabaseQuery(
				driver,
				`
			INSERT INTO student_group_coaches (
				group_id,
				user_id,
				created_at
			)
			SELECT ?, u.id, ?
			FROM users u
			JOIN user_roles ur
				ON ur.user_id = u.id
			JOIN roles r
				ON r.id = ur.role_id
			WHERE u.id = ?
				AND r.name = 'coach'
			LIMIT 1
		`,
			),
			groupID,
			now,
			coachID,
		)
		if err != nil {
			return err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if affected != 1 {
			return errors.New("selected coach is invalid")
		}
	}

	return nil
}

func replaceStudentGroupSessionsTx(
	tx *sql.Tx,
	driver DatabaseDriver,
	groupID int64,
	sessions []StudentGroupSession,
) error {
	if _, err := tx.Exec(
		rebindDatabaseQuery(
			driver,
			`DELETE FROM student_group_sessions WHERE group_id = ?`,
		),
		groupID,
	); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, session := range sessions {
		if _, err := tx.Exec(
			rebindDatabaseQuery(
				driver,
				`
				INSERT INTO student_group_sessions (
					group_id, title, day_of_week, start_time, end_time, active, created_at, updated_at
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				`,
			),
			groupID,
			session.Title,
			session.DayOfWeek,
			session.StartTime,
			session.EndTime,
			boolToInt(session.Active),
			now,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) createStudentGroup(
	group StudentGroup,
	admissionIDs []int64,
	coachIDs []int64,
	sessions []StudentGroupSession,
) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	var groupID int64

	if err := a.queryRowTxDB(
		tx,
		`
		INSERT INTO student_groups (
			name,
			code,
			description,
			training_program_id,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id
		`,
		group.Name,
		group.Code,
		group.Description,
		nullIfZero(group.TrainingProgramID),
		now,
		now,
	).Scan(&groupID); err != nil {
		return err
	}

	for _, admissionID := range admissionIDs {
		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_members (
				group_id,
				admission_id
			)
			VALUES (?, ?)
			`,
			groupID,
			admissionID,
		); err != nil {
			return err
		}
	}

	if err := syncStudentGroupMembershipHistoryTx(
		a,
		tx,
		groupID,
		admissionIDs,
		currentBusinessDate(),
	); err != nil {
		return err
	}

	// Legacy coach assignments are retained here for compatibility with
	// existing attendance/group workflows.
	if err := replaceStudentGroupCoachesTx(
		a,
		tx,
		a.runtimeConfig.DBDriver,
		groupID,
		coachIDs,
	); err != nil {
		return err
	}

	if err := replaceStudentGroupSessionsTx(
		tx,
		a.runtimeConfig.DBDriver,
		groupID,
		sessions,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) replaceAttendanceRecords(groupID int64, sessionID int64, attendanceDate string, records []AttendanceRecord) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		rebindDatabaseQuery(
			a.runtimeConfig.DBDriver,
			`DELETE FROM attendance_records
			 WHERE group_id = ?
			   AND COALESCE(session_id, 0) = ?
			   AND attendance_date = ?`,
		),
		groupID,
		sessionID,
		attendanceDate,
	); err != nil {
		return err
	}

	for _, record := range records {
		if _, err := tx.Exec(
			rebindDatabaseQuery(
				a.runtimeConfig.DBDriver,
				`
					INSERT INTO attendance_records (
						group_id,
						session_id,
						admission_id,
						attendance_date,
						status,
						note,
						recorded_by_user_id,
						recorded_at,
						updated_at
					)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				`,
			),
			record.GroupID,
			nullIfZero(record.SessionID),
			record.AdmissionID,
			record.AttendanceDate,
			record.Status,
			record.Note,
			nullIfZero(record.RecordedByUserID),
			time.Now().UTC(),
			time.Now().UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) createCourtLayout(
	layout CourtLayout,
) (int64, error) {
	activities, err := a.listCourtActivities(
		layout.CourtID,
		false,
	)
	if err != nil {
		return 0, err
	}

	if err := validateCourtLayout(
		layout,
		activities,
	); err != nil {
		return 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	layoutID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO court_layouts (
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		layout.CourtID,
		layout.Name,
		layout.Description,
		layout.Active,
		layout.SortOrder,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	for _, item := range layout.Items {
		_, err := a.execTxDB(tx, `
			INSERT INTO court_layout_items (
				layout_id,
				activity,
				quantity
			)
			VALUES (?, ?, ?)
		`,
			layoutID,
			item.Activity,
			item.Quantity,
		)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return layoutID, nil
}

func (a *App) createCourtActivity(
	activity CourtActivity,
) (int64, error) {
	if err := validateCourtActivity(activity); err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	return a.insertAndReturnID(`
		INSERT INTO court_activities (
			court_id,
			game_id,
			activity,
			display_name,
			max_quantity,
			auto_accept,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		activity.CourtID,
		activity.GameID,
		activity.Activity,
		activity.DisplayName,
		activity.MaxQuantity,
		boolToInt(activity.AutoAccept),
		boolToInt(activity.Active),
		activity.SortOrder,
		now,
		now,
	)
}

func (a *App) createCourt(court Court) (int64, error) {
	now := time.Now().UTC()
	return a.insertAndReturnID(`
		INSERT INTO courts (
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		court.Name,
		court.Code,
		court.Description,
		boolToInt(court.Active),
		court.SortOrder,
		now,
		now,
	)
}

func (a *App) updateCourt(court Court) error {
	result, err := a.execDB(`
		UPDATE courts
		SET
			name = ?,
			code = ?,
			description = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
	`,
		court.Name,
		court.Code,
		court.Description,
		boolToInt(court.Active),
		court.SortOrder,
		time.Now().UTC(),
		court.ID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) updateCourtLayout(
	layout CourtLayout,
) error {
	if layout.ID <= 0 {
		return errors.New("valid court layout is required")
	}

	activities, err := a.listCourtActivities(
		layout.CourtID,
		false,
	)
	if err != nil {
		return err
	}

	if err := validateCourtLayout(
		layout,
		activities,
	); err != nil {
		return err
	}

	if !layout.Active {
		var otherActiveLayouts int
		if err := a.queryRowDB(`
			SELECT COUNT(*)
			FROM court_layouts
			WHERE active = 1
			  AND id <> ?
		`, layout.ID).Scan(&otherActiveLayouts); err != nil {
			return err
		}
		if otherActiveLayouts == 0 {
			return errors.New("at least one active court layout must remain available")
		}
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := a.execTxDB(tx, `
		UPDATE court_layouts
		SET
			name = ?,
			description = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
		  AND court_id = ?
	`,
		layout.Name,
		layout.Description,
		layout.Active,
		layout.SortOrder,
		time.Now().UTC(),
		layout.ID,
		layout.CourtID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	if _, err := a.execTxDB(tx, `
		DELETE FROM court_layout_items
		WHERE layout_id = ?
	`, layout.ID); err != nil {
		return err
	}

	for _, item := range layout.Items {
		_, err := a.execTxDB(tx, `
			INSERT INTO court_layout_items (
				layout_id,
				activity,
				quantity
			)
			VALUES (?, ?, ?)
		`,
			layout.ID,
			item.Activity,
			item.Quantity,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) toggleCourtLayout(
	layoutID int64,
) error {
	if layoutID <= 0 {
		return errors.New("valid court layout is required")
	}

	var active bool

	err := a.queryRowDB(`
		SELECT active
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(&active)
	if err != nil {
		return err
	}

	if active {
		var activeLayoutCount int
		if err := a.queryRowDB(`
			SELECT COUNT(*)
			FROM court_layouts
			WHERE active = 1
		`).Scan(&activeLayoutCount); err != nil {
			return err
		}
		if activeLayoutCount <= 1 {
			return errors.New("at least one active court layout must remain available")
		}
	}

	_, err = a.execDB(`
		UPDATE court_layouts
		SET
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		!active,
		time.Now().UTC(),
		layoutID,
	)

	return err
}

func (a *App) deleteCourtLayout(
	layoutID int64,
) error {
	if layoutID <= 0 {
		return errors.New("valid court layout is required")
	}

	var activeLayoutCount int

	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM court_layouts
		WHERE active = 1
	`).Scan(&activeLayoutCount); err != nil {
		return err
	}

	var deletingActive bool

	if err := a.queryRowDB(`
		SELECT active
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(&deletingActive); err != nil {
		return err
	}

	if deletingActive && activeLayoutCount <= 1 {
		return errors.New(
			"the final active court layout cannot be deleted",
		)
	}

	result, err := a.execDB(`
		DELETE FROM court_layouts
		WHERE id = ?
	`, layoutID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) createSpaceSchedule(
	schedule SpaceSchedule,
) error {
	return a.createSpaceSchedules(schedule, 1)
}

func (a *App) createSpaceSchedules(
	schedule SpaceSchedule,
	durationHours int,
) error {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return err
	}
	candidates, err := consecutiveBookingSchedules(schedule, durationHours)
	if err != nil {
		return err
	}
	for i := range candidates {
		if candidates[i].EntryType != "booking" {
			continue
		}
		if candidates[i].QuotedPrice > 0 {
			candidates[i].QuotedPrice = normalizeMoney(candidates[i].QuotedPrice)
			continue
		}
		quotedPrice, err := a.bookingQuote(candidates[i])
		if err != nil {
			return err
		}
		candidates[i].QuotedPrice = quotedPrice
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	existingBySlot := make(map[string][]SpaceSchedule, len(candidates))
	for _, candidate := range candidates {
		if err := validateScheduleAgainstClosures(
			candidate,
			courtClosures,
		); err != nil {
			return err
		}
		existing, seen := existingBySlot[candidate.SlotDate+" "+candidate.SlotHour]
		if !seen {
			existing, err = querySchedulesForSlot(
				tx,
				a.runtimeConfig.DBDriver,
				candidate.SlotDate,
				candidate.SlotHour,
				0,
			)
			if err != nil {
				return err
			}
		}

		if err := validateSpaceScheduleSlotAgainstLayouts(
			existing,
			candidate,
			courtLayouts,
		); err != nil {
			return err
		}

		scheduleID, err := a.insertAndReturnIDTx(tx, `
			INSERT INTO space_schedules (
				slot_date,
				slot_hour,
				entry_type,
				activity,
				quantity,
				title,
				notes,
				status,
				requester_name,
				requester_email,
				requester_phone,
				requested_by_user_id,
				review_note,
				customer_message,
				status_changed_at,
				status_changed_by_user_id,
				status_change_source,
				cancellation_reason,
				cancellation_finance_note,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			candidate.SlotDate,
			candidate.SlotHour,
			candidate.EntryType,
			candidate.Activity,
			candidate.Quantity,
			candidate.Title,
			candidate.Notes,
			bookingStatusConfirmed,
			candidate.RequesterName,
			candidate.RequesterEmail,
			candidate.RequesterPhone,
			nil,
			"",
			"",
			nil,
			nil,
			"",
			"",
			"",
			now,
			now,
		)
		if err != nil {
			return err
		}

		if candidate.EntryType == "booking" {
			if _, err := a.execTxDB(tx, `
				INSERT INTO booking_financials (
					schedule_id,
					quoted_amount,
					paid,
					payment_method,
					created_at,
					updated_at
				)
				VALUES (?, ?, 0, '', ?, ?)
			`,
				scheduleID,
				candidate.QuotedPrice,
				now,
				now,
			); err != nil {
				return err
			}
			if err := a.createBookingReferralTx(tx, scheduleID, candidate.ReferralCode, now); err != nil {
				return err
			}
		}

		candidate.ID = scheduleID
		existingBySlot[candidate.SlotDate+" "+candidate.SlotHour] = append(existing, candidate)
	}

	return tx.Commit()
}

type bookingPaymentAdjustment struct {
	OverpaymentAmount float64
	DiscountAmount    float64
	AdjustmentReason  string
}

func joinPaymentNotes(parts ...string) string {
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines = append(lines, part)
	}
	return strings.Join(lines, "\n")
}

func (a *App) createBookingReferralTx(tx *sql.Tx, scheduleID int64, referralCode string, createdAt time.Time) error {
	referralCode = strings.ToUpper(strings.TrimSpace(referralCode))
	if referralCode == "" {
		return nil
	}

	var partnerID int64
	if err := a.queryRowTxDB(tx, `
		SELECT id
		FROM referral_partners
		WHERE code = ?
		  AND active = 1
	`,
		referralCode,
	).Scan(&partnerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("the referral code is invalid or inactive")
		}
		return err
	}

	var commissionAmount float64
	if err := a.queryRowTxDB(tx, `
		SELECT COALESCE(referral_commission_amount, 0)
		FROM pricing_settings
		WHERE id = 1
	`).Scan(&commissionAmount); err != nil {
		return err
	}
	if commissionAmount <= 0 {
		return errors.New("referral commission is not configured")
	}

	_, err := a.execTxDB(tx, `
		INSERT INTO booking_referrals (
			schedule_id,
			partner_id,
			commission_amount,
			paid,
			paid_at,
			payment_method,
			finance_transaction_id,
			created_at
		)
		VALUES (?, ?, ?, 0, NULL, '', NULL, ?)
	`,
		scheduleID,
		partnerID,
		commissionAmount,
		createdAt,
	)
	return err
}

func (a *App) createPricingRule(rule PricingRule) error {
	_, err := a.execDB(`
		INSERT INTO pricing_rules (
			game_id, activity, quantity, weekday_offpeak_price, weekday_peak_price,
			weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rule.GameID,
		rule.Activity,
		rule.Quantity,
		rule.WeekdayOffPeak,
		rule.WeekdayPeak,
		rule.WeekendOffPeak,
		rule.WeekendPeak,
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) createEvent(event Event) error {
	_, err := a.execDB(`
		INSERT INTO events (
			game_id, title, category, event_date, start_time, end_time, registration_deadline, venue, summary,
			image_path, cta_label, cta_link, published, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.GameID,
		event.Title,
		event.Category,
		event.EventDate,
		event.StartTime,
		event.EndTime,
		nullIfBlank(event.RegistrationDeadline),
		event.Venue,
		event.Summary,
		event.ImagePath,
		event.CTALabel,
		event.CTALink,
		boolToInt(event.Published),
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) createPublicBookingRequest(
	schedule SpaceSchedule,
) (int64, error) {
	created, _, err := a.createPublicBookingRequestDetailed(schedule)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (a *App) createPublicBookingRequestDetailed(
	schedule SpaceSchedule,
) (*SpaceSchedule, int64, error) {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return nil, 0, fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return nil, 0, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return nil, 0, fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return nil, 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	existing, err := querySchedulesForSlot(
		tx,
		a.runtimeConfig.DBDriver,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
	)
	if err != nil {
		return nil, 0, err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		schedule,
		courtLayouts,
	); err != nil {
		return nil, 0, err
	}

	var requestedBy any

	if schedule.RequestedByUser > 0 {
		requestedBy = schedule.RequestedByUser
	}

	now := time.Now().UTC()
	requestStatus := bookingStatusPending
	statusSource := ""
	changeSource := "customer"
	actionType := bookingStatusPending

	requestID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO space_schedules (
			slot_date,
			slot_hour,
			entry_type,
			activity,
			quantity,
			title,
			notes,
			status,
			requester_name,
			requester_email,
			requester_phone,
			requested_by_user_id,
			review_note,
			customer_message,
			status_changed_at,
			status_changed_by_user_id,
			status_change_source,
			cancellation_reason,
			cancellation_finance_note,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		"booking",
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		requestStatus,
		schedule.RequesterName,
		schedule.RequesterEmail,
		schedule.RequesterPhone,
		requestedBy,
		"",
		"",
		now,
		nil,
		statusSource,
		"",
		"",
		now,
		now,
	)
	if err != nil {
		return nil, 0, err
	}

	if err != nil {
		return nil, 0, err
	}
	schedule.ID = requestID
	schedule.EntryType = "booking"
	schedule.Status = requestStatus
	schedule.CreatedAt = now
	schedule.UpdatedAt = now
	schedule.StatusChangedAt = now
	schedule.StatusSource = statusSource

	if _, err := a.execTxDB(tx, `
		INSERT INTO booking_financials (
			schedule_id,
			quoted_amount,
			paid,
			payment_method,
			created_at,
			updated_at
		)
		VALUES (?, ?, 0, '', ?, ?)
	`,
		requestID,
		schedule.QuotedPrice,
		now,
		now,
	); err != nil {
		return nil, 0, err
	}

	if err := a.createBookingReferralTx(tx, requestID, schedule.ReferralCode, now); err != nil {
		return nil, 0, err
	}

	changeID, err := a.recordBookingLifecycleChangeTx(
		tx,
		&schedule,
		actionType,
		"",
		requestStatus,
		"",
		"",
		"",
		changeSource,
		0,
	)
	if err != nil {
		return nil, 0, err
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}

	return &schedule, changeID, nil
}

func (a *App) updateAdmission(admission Admission) error {
	result, err := a.execDB(`
		UPDATE admissions
		SET
			student_id = ?,
			full_name = ?,
			admission_date = ?,
			date_of_birth = ?,
			gender = ?,
			practice_type = ?,
			address = ?,
			passport_number = ?,
			school = ?,
			guardian_name = ?,
			guardian_relationship = ?,
			guardian_contact_number = ?,
			guardian_alternative_contact_number = ?,
			medical_information = ?,
			photo_path = ?,
			qr_code_path = ?,
			qr_code_value = ?,
			updated_at = ?
		WHERE id = ?
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		admission.PhotoPath,
		admission.QRCodePath,
		admission.QRCodeValue,
		time.Now().UTC(),
		admission.ID,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func syncAdmissionTrainingProgramsTx(
	tx *sql.Tx,
	driver DatabaseDriver,
	admissionID int64,
	programIDs []int64,
	createdAt time.Time,
) error {
	if _, err := tx.Exec(
		rebindDatabaseQuery(
			driver,
			`DELETE FROM admission_training_programs WHERE admission_id = ?`,
		),
		admissionID,
	); err != nil {
		return err
	}

	for _, programID := range programIDs {
		if _, err := tx.Exec(
			rebindDatabaseQuery(
				driver,
				`
					INSERT INTO admission_training_programs (
						admission_id,
						training_program_id,
						created_at
					)
					VALUES (?, ?, ?)
				`,
			),
			admissionID,
			programID,
			createdAt,
		); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) createStudentEnrollmentWithOptionalPayment(enrollment StudentEnrollment, collectPayment bool, paymentMethod string, recordedByUserID int64) (int64, int64, error) {
	return a.createStudentEnrollmentWithOptionalPaymentAt(enrollment, collectPayment, paymentMethod, time.Now().UTC(), recordedByUserID)
}

func (a *App) createStudentEnrollmentWithOptionalPaymentAt(enrollment StudentEnrollment, collectPayment bool, paymentMethod string, collectedAt time.Time, recordedByUserID int64) (int64, int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	if strings.TrimSpace(enrollment.EnrollmentDate) == "" {
		if err := a.queryRowTxDB(
			tx,
			`SELECT COALESCE(admission_date, '') FROM admissions WHERE id = ?`,
			enrollment.AdmissionID,
		).Scan(&enrollment.EnrollmentDate); err != nil {
			return 0, 0, err
		}
	}

	enrollment.EnrollmentDate =
		strings.TrimSpace(enrollment.EnrollmentDate)

	var enrollmentID int64

	if err := a.queryRowTxDB(
		tx,
		`
		INSERT INTO student_enrollments (
			admission_id,
			training_program_id,
			enrollment_date,
			free_admission,
			free_monthly_fee,
			discounted_monthly_fee,
			payment_collected,
			payment_collected_at,
			admission_payment_amount,
			finance_transaction_id,
			active,
			created_at,
			updated_at
		)
		VALUES (
			?, ?, ?, ?, ?, ?,
			0, NULL, 0, NULL, 1, ?, ?
		)
		RETURNING id
		`,
		enrollment.AdmissionID,
		enrollment.TrainingProgramID,
		enrollment.EnrollmentDate,
		boolToInt(enrollment.FreeAdmission),
		boolToInt(enrollment.FreeMonthlyFee),
		normalizeMoney(enrollment.DiscountedMonthlyFee),
		now,
		now,
	).Scan(&enrollmentID); err != nil {
		return 0, 0, err
	}

	enrollment.ID = enrollmentID

	if err := syncStudentEnrollmentStatusHistoryTx(
		a,
		tx,
		enrollmentID,
		true,
		enrollment.EnrollmentDate,
	); err != nil {
		return 0, 0, err
	}

	if _, err := a.execTxDB(
		tx,
		`
		INSERT INTO admission_training_programs (
			admission_id,
			training_program_id,
			created_at
		)
		SELECT ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM admission_training_programs
			WHERE admission_id = ?
			  AND training_program_id = ?
		)
		`,
		enrollment.AdmissionID,
		enrollment.TrainingProgramID,
		now,
		enrollment.AdmissionID,
		enrollment.TrainingProgramID,
	); err != nil {
		return 0, 0, err
	}

	var financeTransactionID int64
	if collectPayment && !enrollment.FreeAdmission {
		financeTransactionID, err = a.collectEnrollmentAdmissionPaymentAtTx(tx, enrollment, paymentMethod, collectedAt, recordedByUserID)
		if err != nil {
			return 0, 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return enrollmentID, financeTransactionID, nil
}

func (a *App) updateStudentEnrollment(enrollment StudentEnrollment) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollment.ID)
	if err != nil {
		return err
	}

	var monthlyPaymentCount int
	if err := a.queryRowTxDB(
		tx,
		`
		SELECT COUNT(*)
		FROM student_monthly_payments
		WHERE enrollment_id = ?
		`,
		enrollment.ID,
	).Scan(&monthlyPaymentCount); err != nil {
		return err
	}

	if strings.TrimSpace(enrollment.EnrollmentDate) == "" {
		enrollment.EnrollmentDate = existing.EnrollmentDate
	}

	if existing.AdmissionPaymentPaid {
		enrollment.TrainingProgramID = existing.TrainingProgramID
		enrollment.TrainingProgramName = existing.TrainingProgramName
		enrollment.FreeAdmission = existing.FreeAdmission
	}
	if monthlyPaymentCount > 0 {
		enrollment.TrainingProgramID = existing.TrainingProgramID
		enrollment.TrainingProgramName = existing.TrainingProgramName
	}

	result, err := a.execTxDB(
		tx,
		`
		UPDATE student_enrollments
		SET
			training_program_id = ?,
			enrollment_date = ?,
			free_admission = ?,
			free_monthly_fee = ?,
			discounted_monthly_fee = ?,
			updated_at = ?
		WHERE id = ?
		`,
		enrollment.TrainingProgramID,
		enrollment.EnrollmentDate,
		boolToInt(enrollment.FreeAdmission),
		boolToInt(enrollment.FreeMonthlyFee),
		normalizeMoney(enrollment.DiscountedMonthlyFee),
		time.Now().UTC(),
		enrollment.ID,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	if err := syncStudentEnrollmentActiveStartDateTx(
		a,
		tx,
		enrollment.ID,
		existing.EnrollmentDate,
		enrollment.EnrollmentDate,
	); err != nil {
		return err
	}

	if enrollment.TrainingProgramID != existing.TrainingProgramID {
		if _, err := a.execTxDB(
			tx,
			`
			DELETE FROM admission_training_programs
			WHERE admission_id = ?
			  AND training_program_id = ?
			`,
			existing.AdmissionID,
			existing.TrainingProgramID,
		); err != nil {
			return err
		}
		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO admission_training_programs (
				admission_id,
				training_program_id,
				created_at
			)
			SELECT ?, ?, ?
			WHERE NOT EXISTS (
				SELECT 1
				FROM admission_training_programs
				WHERE admission_id = ?
				  AND training_program_id = ?
			)
			`,
			existing.AdmissionID,
			enrollment.TrainingProgramID,
			time.Now().UTC(),
			existing.AdmissionID,
			enrollment.TrainingProgramID,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deactivateStudentEnrollment(enrollmentID int64, effectiveDate string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		return err
	}
	if !enrollment.Active {
		return nil
	}

	effectiveDate = strings.TrimSpace(effectiveDate)
	if effectiveDate == "" {
		effectiveDate = time.Now().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", effectiveDate); err != nil {
		return errors.New("invalid unenrollment date")
	}
	if err := validateHistoricalEntryDateValue(effectiveDate, "unenrollment date"); err != nil {
		return err
	}
	if enrollment.EnrollmentDate != "" && effectiveDate < enrollment.EnrollmentDate {
		return errors.New("unenrollment date cannot be before the enrollment date")
	}
	if effectiveDate > time.Now().Format("2006-01-02") {
		return errors.New("unenrollment date cannot be in the future")
	}

	result, err := a.execTxDB(
		tx,
		`
		UPDATE student_enrollments
		SET
			active = 0,
			updated_at = ?
		WHERE id = ?
		`,
		time.Now().UTC(),
		enrollmentID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	if err := syncStudentEnrollmentStatusHistoryTx(a, tx, enrollmentID, false, effectiveDate); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deleteStudentEnrollment(enrollmentID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		return err
	}

	rows, err := a.queryTxDB(
		tx,
		`
		SELECT id, receipt_number, amount, recorded_at
		FROM finance_transactions
		WHERE id = ?
		   OR (
			reference_type = 'student_enrollment'
			AND reference_id = ?
		)
		ORDER BY recorded_at DESC, id DESC
		`,
		enrollment.FinanceTransactionID,
		enrollmentID,
	)
	if err != nil {
		return err
	}

	blockers := make([]EnrollmentDeleteBlocker, 0)
	for rows.Next() {
		var (
			id            int64
			receiptNumber string
			amount        float64
			recordedAt    time.Time
		)
		if err := rows.Scan(&id, &receiptNumber, &amount, &recordedAt); err != nil {
			return err
		}

		detail := "Transaction #" + strconv.FormatInt(id, 10)
		if strings.TrimSpace(receiptNumber) != "" {
			detail = "Receipt " + receiptNumber
		}
		detail += " • " + money(amount) + " • " + recordedAt.In(time.Local).Format("2006-01-02")
		blockers = append(blockers, EnrollmentDeleteBlocker{
			Kind:   "finance_transaction",
			Label:  "Admission fee transaction",
			Detail: detail,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(blockers) == 0 && (enrollment.AdmissionPaymentPaid || enrollment.FinanceTransactionID > 0) {
		detail := "Admission payment history exists"
		if enrollment.FinanceTransactionID > 0 {
			detail += " • Transaction #" + strconv.FormatInt(enrollment.FinanceTransactionID, 10)
		}
		blockers = append(blockers, EnrollmentDeleteBlocker{
			Kind:   "finance_transaction",
			Label:  "Admission fee transaction",
			Detail: detail,
		})
	}

	rows, err = a.queryTxDB(
		tx,
		`
		SELECT id, payment_month, amount, collected_at
		FROM student_monthly_payments
		WHERE enrollment_id = ?
		ORDER BY payment_month DESC, id DESC
		`,
		enrollmentID,
	)
	if err != nil {
		return err
	}

	for rows.Next() {
		var (
			id           int64
			paymentMonth string
			amount       float64
			collectedAt  time.Time
		)
		if err := rows.Scan(&id, &paymentMonth, &amount, &collectedAt); err != nil {
			return err
		}

		detail := paymentMonthLabel(paymentMonth) + " • " + money(amount)
		if !collectedAt.IsZero() {
			detail += " • collected " + collectedAt.In(time.Local).Format("2006-01-02")
		}
		blockers = append(blockers, EnrollmentDeleteBlocker{
			Kind:   "monthly_payment",
			Label:  "Monthly payment history",
			Detail: detail + " • Payment #" + strconv.FormatInt(id, 10),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if len(blockers) > 0 {
		return &enrollmentDeleteBlockedError{
			block: EnrollmentDeleteBlock{
				Title:    "Enrollment cannot be deleted",
				Message:  "This enrollment has linked finance history. Remove or void the linked records first.",
				Blockers: blockers,
			},
		}
	}

	result, err := a.execTxDB(tx, `DELETE FROM student_enrollments WHERE id = ?`, enrollmentID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key") ||
			strings.Contains(strings.ToLower(err.Error()), "violates foreign key") {
			return &enrollmentDeleteBlockedError{
				block: EnrollmentDeleteBlock{
					Title:   "Enrollment cannot be deleted",
					Message: "This enrollment is still linked to other records.",
					Blockers: []EnrollmentDeleteBlocker{
						{
							Kind:   "foreign_key",
							Label:  "Linked record",
							Detail: err.Error(),
						},
					},
				},
			}
		}
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	if _, err := a.execTxDB(
		tx,
		`
		DELETE FROM admission_training_programs
		WHERE admission_id = ?
		  AND training_program_id = ?
	`,
		enrollment.AdmissionID,
		enrollment.TrainingProgramID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) collectEnrollmentAdmissionPaymentTx(
	tx *sql.Tx,
	enrollment StudentEnrollment,
	paymentMethod string,
	recordedByUserID int64,
) (int64, error) {
	return a.collectEnrollmentAdmissionPaymentAtTx(tx, enrollment, paymentMethod, time.Now().UTC(), recordedByUserID)
}

func (a *App) collectEnrollmentAdmissionPaymentAtTx(
	tx *sql.Tx,
	enrollment StudentEnrollment,
	paymentMethod string,
	recordedAt time.Time,
	recordedByUserID int64,
) (int64, error) {
	var studentName string

	if err := a.queryRowTxDB(
		tx,
		`SELECT full_name FROM admissions WHERE id = ?`,
		enrollment.AdmissionID,
	).Scan(&studentName); err != nil {
		return 0, err
	}

	var admissionFee float64

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT COALESCE(admission_fee, 0)
		FROM training_programs
		WHERE id = ?
		`,
		enrollment.TrainingProgramID,
	).Scan(&admissionFee); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrAdmissionFeeNotConfigured
		}
		return 0, err
	}

	if enrollment.FreeAdmission {
		admissionFee = 0
	}

	if admissionFee <= 0 {
		return 0, ErrAdmissionFeeNotConfigured
	}

	recordedAt = recordedAt.UTC()
	now := time.Now().UTC()

	receiptNumber := fmt.Sprintf(
		"ENR-%s-%06d",
		recordedAt.Format("20060102150405"),
		enrollment.ID,
	)

	paymentMethod = normalizePaymentMethod(paymentMethod)

	if !validPaymentMethod(paymentMethod) {
		return 0, errors.New("invalid payment method")
	}

	divisionID, err := financeDivisionIDForEntryTx(
		tx,
		financeTransactionCreate{
			ReferenceType: "student_enrollment",
			ReferenceID:   enrollment.ID,
			SourceType:    "student_enrollment",
			SourceID:      enrollment.ID,
		},
	)
	if err != nil {
		return 0, err
	}

	account, err := findFinanceAccountForPaymentMethodTx(
		tx,
		divisionID,
		paymentMethod,
	)
	if err != nil {
		return 0, err
	}

	description := fmt.Sprintf(
		"Admission payment for %s - %s",
		studentName,
		enrollment.TrainingProgramName,
	)

	transactionID, err := insertFinanceTransactionTx(
		tx,
		financeTransactionCreate{
			ReceiptNumber:    receiptNumber,
			ReferenceNumber:  receiptNumber,
			Category:         "admission_payment",
			TransactionType:  financeTxnTypeIncome,
			ReferenceType:    "student_enrollment",
			ReferenceID:      enrollment.ID,
			SourceType:       "student_enrollment",
			SourceID:         enrollment.ID,
			FinanceAccountID: account.ID,
			PersonName:       studentName,
			Description:      description,
			PaymentMethod:    paymentMethod,
			Amount:           admissionFee,
			RecordedByUserID: recordedByUserID,
			RecordedAt:       recordedAt,
		},
	)
	if err != nil {
		return 0, err
	}

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE student_enrollments
		SET
			payment_collected = 1,
			payment_collected_at = ?,
			admission_payment_amount = ?,
			finance_transaction_id = ?,
			updated_at = ?
		WHERE id = ?
		`,
		recordedAt,
		admissionFee,
		transactionID,
		now,
		enrollment.ID,
	); err != nil {
		return 0, err
	}

	return transactionID, nil
}

func (a *App) createAdmissionWithOptionalPayment(
	admission Admission,
	collectPayment bool,
	paymentMethod string,
	recordedByUserID int64,
) (int64, int64, error) {
	return a.createAdmissionWithOptionalPaymentAt(admission, collectPayment, paymentMethod, time.Now().UTC(), recordedByUserID)
}

func (a *App) createAdmissionWithOptionalPaymentAt(
	admission Admission,
	collectPayment bool,
	paymentMethod string,
	collectedAt time.Time,
	recordedByUserID int64,
) (int64, int64, error) {
	if strings.TrimSpace(admission.StudentID) == "" {
		studentID, err := a.nextStudentID(admission.AdmissionDate)
		if err != nil {
			return 0, 0, err
		}
		admission.StudentID = studentID
		if strings.TrimSpace(admission.QRCodeValue) == "" {
			admission.QRCodeValue = studentID
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	insertQuery := `
		INSERT INTO admissions (
			student_id,
			full_name,
			admission_date,
			date_of_birth,
			gender,
			practice_type,
			training_program_id,
			address,
			passport_number,
			school,
			guardian_name,
			guardian_relationship,
			guardian_contact_number,
			guardian_alternative_contact_number,
			medical_information,
			photo_path,
			qr_code_path,
			qr_code_value,
			free_admission,
			free_monthly_fee,
			payment_collected,
			payment_collected_at,
			admission_payment_amount,
			finance_transaction_id,
			created_at,
			updated_at
		)
		VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?
		)
	`

	insertArgs := []any{
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.TrainingProgramID,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		admission.PhotoPath,
		admission.QRCodePath,
		admission.QRCodeValue,
		boolToInt(admission.FreeAdmission),
		boolToInt(admission.FreeMonthlyFee),
		0,
		nil,
		0,
		nil,
		now,
		now,
	}

	admissionID, err := a.insertAndReturnIDTx(
		tx,
		insertQuery,
		insertArgs...,
	)
	if err != nil {
		return 0, 0, err
	}

	admission.ID = admissionID
	if err := syncAdmissionTrainingProgramsTx(tx, a.runtimeConfig.DBDriver, admissionID, admission.TrainingProgramIDs, now); err != nil {
		return 0, 0, err
	}

	var financeTransactionID int64

	if collectPayment && !admission.FreeAdmission {
		financeTransactionID, err = a.collectAdmissionPaymentAtTx(
			tx,
			admission,
			paymentMethod,
			collectedAt,
			recordedByUserID,
		)
		if err != nil {
			return 0, 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	return admissionID, financeTransactionID, nil
}

func (a *App) nextStudentID(admissionDate string) (string, error) {
	date, err := time.Parse("2006-01-02", strings.TrimSpace(admissionDate))
	if err != nil {
		return "", errors.New("admission date must be a valid date before assigning a student number")
	}
	prefix := fmt.Sprintf("MEK/%d/", date.Year())
	rows, err := a.queryDB(`SELECT student_id FROM admissions WHERE UPPER(student_id) LIKE UPPER(?)`, prefix+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	lastNumber := 0
	for rows.Next() {
		var studentID string
		if err := rows.Scan(&studentID); err != nil {
			return "", err
		}
		suffix := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(studentID)), strings.ToUpper(prefix))
		if len(suffix) == 0 || len(suffix) > 4 {
			continue
		}
		number, err := strconv.Atoi(suffix)
		if err == nil && number > 0 && number > lastNumber {
			lastNumber = number
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%04d", prefix, lastNumber+1), nil
}

func (a *App) updateAdmissionWithOptionalPayment(
	admission Admission,
	collectPayment bool,
	recordedByUserID int64,
) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	existing, err := a.findAdmissionByIDTx(tx, admission.ID)
	if err != nil {
		return 0, err
	}

	result, err := tx.Exec(
		rebindDatabaseQuery(
			a.runtimeConfig.DBDriver,
			`
		UPDATE admissions
		SET
			student_id = ?,
			full_name = ?,
			admission_date = ?,
			date_of_birth = ?,
			gender = ?,
			practice_type = ?,
			address = ?,
			passport_number = ?,
			school = ?,
			guardian_name = ?,
			guardian_relationship = ?,
			guardian_contact_number = ?,
			guardian_alternative_contact_number = ?,
			medical_information = ?,
			photo_path = ?,
			qr_code_path = ?,
			qr_code_value = ?,
			updated_at = ?
		WHERE id = ?
			`,
		),
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		admission.PhotoPath,
		admission.QRCodePath,
		admission.QRCodeValue,
		time.Now().UTC(),
		admission.ID,
	)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if rowsAffected == 0 {
		return 0, sql.ErrNoRows
	}
	var financeTransactionID int64

	if collectPayment && !existing.PaymentCollected && !admission.FreeAdmission {
		financeTransactionID, err = a.collectAdmissionPaymentTx(
			tx,
			admission,
			"cash",
			recordedByUserID,
		)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return financeTransactionID, nil
}

func (a *App) updateStudentGroup(
	group StudentGroup,
	admissionIDs []int64,
	coachIDs []int64,
	sessions []StudentGroupSession,
) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE student_groups
		SET
			name = ?,
			code = ?,
			description = ?,
			training_program_id = ?,
			updated_at = ?
		WHERE id = ?
		`,
		group.Name,
		group.Code,
		group.Description,
		nullIfZero(group.TrainingProgramID),
		time.Now().UTC(),
		group.ID,
	); err != nil {
		return err
	}

	if err := syncStudentGroupMembershipHistoryTx(
		a,
		tx,
		group.ID,
		admissionIDs,
		currentBusinessDate(),
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`DELETE FROM student_group_members WHERE group_id = ?`,
		group.ID,
	); err != nil {
		return err
	}

	for _, admissionID := range admissionIDs {
		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_members (
				group_id,
				admission_id
			)
			VALUES (?, ?)
			`,
			group.ID,
			admissionID,
		); err != nil {
			return err
		}
	}

	if err := replaceStudentGroupCoachesTx(
		a,
		tx,
		a.runtimeConfig.DBDriver,
		group.ID,
		coachIDs,
	); err != nil {
		return err
	}

	if err := replaceStudentGroupSessionsTx(
		tx,
		a.runtimeConfig.DBDriver,
		group.ID,
		sessions,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) updateSpaceSchedule(
	schedule SpaceSchedule,
) error {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentSlotDate string
	var currentSlotHour string
	var currentEntryType string
	var currentActivity string
	var currentQuantity int
	if err := tx.QueryRow(`
		SELECT slot_date, slot_hour, entry_type, activity, quantity
		FROM space_schedules
		WHERE id = ?
	`, schedule.ID).Scan(&currentSlotDate, &currentSlotHour, &currentEntryType, &currentActivity, &currentQuantity); err != nil {
		return err
	}

	existing, err := querySchedulesForSlot(
		tx,
		a.runtimeConfig.DBDriver,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.ID,
	)
	if err != nil {
		return err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		schedule,
		courtLayouts,
	); err != nil {
		return err
	}

	result, err := tx.Exec(`
		UPDATE space_schedules
		SET
			slot_date = ?,
			slot_hour = ?,
			entry_type = ?,
			activity = ?,
			quantity = ?,
			title = ?,
			notes = ?,
			updated_at = ?
		WHERE id = ?
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		time.Now().UTC(),
		schedule.ID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	var financial struct {
		ID           int64
		QuotedAmount float64
		Paid         bool
	}
	financialErr := tx.QueryRow(`
		SELECT id, quoted_amount, paid
		FROM booking_financials
		WHERE schedule_id = ?
	`, schedule.ID).Scan(&financial.ID, &financial.QuotedAmount, &financial.Paid)
	if financialErr != nil && !errors.Is(financialErr, sql.ErrNoRows) {
		return financialErr
	}

	billingFieldsChanged := currentSlotDate != schedule.SlotDate ||
		currentSlotHour != schedule.SlotHour ||
		currentEntryType != schedule.EntryType ||
		currentActivity != schedule.Activity ||
		currentQuantity != schedule.Quantity

	if financial.Paid && billingFieldsChanged {
		return errors.New("paid bookings cannot change date, hour, entry type, activity, or quantity")
	}

	now := time.Now().UTC()

	switch schedule.EntryType {
	case "booking":
		if financialErr == nil {
			// Preserve the original quoted-price snapshot for existing bookings.
			schedule.QuotedPrice = financial.QuotedAmount
		} else {
			if _, err := tx.Exec(`
				INSERT INTO booking_financials (
					schedule_id,
					quoted_amount,
					paid,
					payment_method,
					created_at,
					updated_at
				)
				VALUES (?, ?, 0, '', ?, ?)
			`, schedule.ID, schedule.QuotedPrice, now, now); err != nil {
				return err
			}
		}
	case "training":
		if financialErr == nil {
			if financial.Paid {
				return errors.New("paid bookings cannot be converted to internal training")
			}
			if _, err := tx.Exec(`
				DELETE FROM booking_financials
				WHERE id = ?
			`, financial.ID); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (a *App) updatePricingRule(rule PricingRule) error {
	_, err := a.execDB(`
		UPDATE pricing_rules
		SET game_id = ?, activity = ?, quantity = ?, weekday_offpeak_price = ?, weekday_peak_price = ?,
		    weekend_offpeak_price = ?, weekend_peak_price = ?, updated_at = ?
		WHERE id = ?
	`,
		rule.GameID,
		rule.Activity,
		rule.Quantity,
		rule.WeekdayOffPeak,
		rule.WeekdayPeak,
		rule.WeekendOffPeak,
		rule.WeekendPeak,
		time.Now().UTC(),
		rule.ID,
	)
	return err
}

func (a *App) updateCourtActivity(
	activity CourtActivity,
) error {
	if err := validateCourtActivity(activity); err != nil {
		return err
	}

	result, err := a.execDB(`
		UPDATE court_activities
		SET
			display_name = ?,
			max_quantity = ?,
			auto_accept = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
		  AND court_id = ?
	`,
		activity.DisplayName,
		activity.MaxQuantity,
		boolToInt(activity.AutoAccept),
		boolToInt(activity.Active),
		activity.SortOrder,
		time.Now().UTC(),
		activity.ID,
		activity.CourtID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) updateEvent(event Event) error {
	_, err := a.execDB(`
		UPDATE events
		SET game_id = ?, title = ?, category = ?, event_date = ?, start_time = ?, end_time = ?, registration_deadline = ?, venue = ?, summary = ?,
		    image_path = ?, cta_label = ?, cta_link = ?, published = ?, updated_at = ?
		WHERE id = ?
	`,
		event.GameID,
		event.Title,
		event.Category,
		event.EventDate,
		event.StartTime,
		event.EndTime,
		nullIfBlank(event.RegistrationDeadline),
		event.Venue,
		event.Summary,
		event.ImagePath,
		event.CTALabel,
		event.CTALink,
		boolToInt(event.Published),
		time.Now().UTC(),
		event.ID,
	)
	return err
}

func (a *App) updatePricingSettings(settings PricingSettings) error {
	_, err := a.execDB(`
		UPDATE pricing_settings
		SET peak_start_hour = ?, peak_end_hour = ?, updated_at = ?
		WHERE id = 1
	`, settings.PeakStartHour, settings.PeakEndHour, time.Now().UTC())
	return err
}

func (a *App) updateReferralCommissionAmount(amount float64) error {
	_, err := a.execDB(`
		UPDATE pricing_settings
		SET referral_commission_amount = ?, updated_at = ?
		WHERE id = 1
	`, amount, time.Now().UTC())
	return err
}

func (a *App) createReferralPartner(partner ReferralPartner) error {
	_, err := a.execDB(`
		INSERT INTO referral_partners (name, code, email, phone, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
	`, partner.Name, partner.Code, partner.Email, partner.Phone, time.Now().UTC(), time.Now().UTC())
	return err
}

func (a *App) updateReferralPartner(partner ReferralPartner) error {
	result, err := a.execDB(`
		UPDATE referral_partners
		SET name = ?, code = ?, email = ?, phone = ?, updated_at = ?
		WHERE id = ?
	`, partner.Name, partner.Code, partner.Email, partner.Phone, time.Now().UTC(), partner.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("referral partner not found")
	}
	return nil
}

func (a *App) toggleReferralPartner(partnerID int64) error {
	result, err := a.execDB(`
		UPDATE referral_partners
		SET active = CASE active WHEN 1 THEN 0 ELSE 1 END, updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), partnerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("referral partner not found")
	}
	return nil
}

func (a *App) payReferralCommission(referralID int64, paymentMethod string, recordedByUserID int64) (int64, error) {
	paymentMethod = normalizePaymentMethod(paymentMethod)
	if !validFinancePaymentMethod(paymentMethod) {
		return 0, errors.New("invalid referral payment method")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var referral BookingReferral
	var paid int
	if err := tx.QueryRow(`
		SELECT br.id, br.schedule_id, rp.name, br.commission_amount, s.status, br.paid
		FROM booking_referrals br
		JOIN referral_partners rp ON rp.id = br.partner_id
		JOIN space_schedules s ON s.id = br.schedule_id
		WHERE br.id = ?
	`, referralID).Scan(
		&referral.ID, &referral.ScheduleID, &referral.PartnerName,
		&referral.CommissionAmount, &referral.BookingStatus, &paid,
	); err != nil {
		return 0, err
	}
	if referral.BookingStatus != "confirmed" {
		return 0, errors.New("commission is not earned until the booking is confirmed")
	}
	if paid == 1 {
		return 0, errors.New("commission has already been paid")
	}
	if referral.CommissionAmount <= 0 {
		return 0, errors.New("commission amount is invalid")
	}

	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("REF-%s-%06d", now.Format("20060102150405"), referral.ID)
	divisionID, err := financeDivisionIDForEntryTx(tx, financeTransactionCreate{
		ReferenceType: "booking_referral",
		ReferenceID:   referral.ID,
		SourceType:    "booking_referral_payment",
		SourceID:      referral.ID,
	})
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	if account.Name != financeAccountCashInHand && account.Name != financeAccountMainBank {
		return 0, errors.New("referral commissions may be paid only from the cash or main bank account")
	}
	description := fmt.Sprintf("Referral commission for %s", bookingReference(referral.ScheduleID))
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    receiptNumber,
		ReferenceNumber:  receiptNumber,
		Category:         "referral_commission_payment",
		TransactionType:  financeTxnTypeExpense,
		ReferenceType:    "booking_referral",
		ReferenceID:      referral.ID,
		SourceType:       "booking_referral_payment",
		SourceID:         referral.ID,
		FinanceAccountID: account.ID,
		PersonName:       referral.PartnerName,
		Description:      description,
		PaymentMethod:    paymentMethod,
		Amount:           -referral.CommissionAmount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       now,
	})
	if err != nil {
		return 0, err
	}
	updateResult, err := tx.Exec(`
		UPDATE booking_referrals
		SET paid = 1, paid_at = ?, payment_method = ?, finance_transaction_id = ?
		WHERE id = ? AND paid = 0
	`, now, paymentMethod, transactionID, referral.ID)
	if err != nil {
		return 0, err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil || affected != 1 {
		return 0, errors.New("commission payment could not be finalized")
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) nextReceiptNumberTx(tx *sql.Tx, scope string, now time.Time) (string, error) {
	year := now.UTC().Year()
	if _, err := tx.Exec(a.dbQuery(`
		INSERT INTO receipt_sequences (scope, year, next_value)
		VALUES (?, ?, 2)
		ON CONFLICT(scope, year) DO UPDATE
		SET next_value = receipt_sequences.next_value + 1
	`), scope, year); err != nil {
		return "", err
	}
	var nextValue int
	if err := a.queryRowTxDB(
		tx,
		`SELECT next_value - 1 FROM receipt_sequences WHERE scope = ? AND year = ?`,
		scope,
		year,
	).Scan(&nextValue); err != nil {
		return "", err
	}
	if scope == "booking_payment" {
		return fmt.Sprintf("MKM-BKG-%d-%06d", year, nextValue), nil
	}
	return fmt.Sprintf("%s-%d-%06d", strings.ToUpper(scope), year, nextValue), nil
}

func normalizeMoney(value float64) float64 {
	return math.Round(value*100) / 100
}

func moneyEquals(left, right float64) bool {
	return math.Abs(normalizeMoney(left)-normalizeMoney(right)) < 0.005
}

func bookingPaymentCollectibleStatus(status string) bool {
	switch canonicalBookingStatus(status) {
	case bookingStatusConfirmed, bookingStatusCompleted, bookingStatusCancelled:
		return true
	default:
		return false
	}
}

func (a *App) syncBookingFinancialSnapshotTx(tx *sql.Tx, scheduleID int64) error {
	financials, err := listBookingFinancialsForScheduleIDsQuery(tx, a.runtimeConfig.DBDriver, []int64{scheduleID})
	if err != nil {
		return err
	}
	financial := bookingFinancialForSchedule(financials, scheduleID)
	if financial == nil {
		return nil
	}
	var paidAt any
	var paymentMethod any
	var financeTransactionID any
	if financial.ActivePaymentCount > 0 {
		paidAt = financial.LastPaymentDate.UTC()
		collections, err := listBookingPaymentCollectionsForScheduleIDsQuery(tx, a.runtimeConfig.DBDriver, []int64{scheduleID})
		if err != nil {
			return err
		}
		var latestActive *BookingPaymentCollection
		for _, collection := range collections {
			if !collection.Voided {
				if latestActive == nil || collection.CollectedAt.After(latestActive.CollectedAt) {
					copy := collection
					latestActive = &copy
				}
			}
		}
		if latestActive != nil {
			financeTransactionID = latestActive.FinanceTransactionID
			paymentMethod = latestActive.PaymentMethod
		}
	}
	paidFlag := 0
	if financial.ActivePaymentCount > 0 {
		paidFlag = 1
	}
	_, err = tx.Exec(
		a.dbQuery(`
			UPDATE booking_financials
			SET paid = ?,
			    paid_at = ?,
			    payment_method = COALESCE(?, ''),
			    finance_transaction_id = ?,
			    updated_at = ?
			WHERE schedule_id = ?
		`),
		paidFlag,
		paidAt,
		paymentMethod,
		financeTransactionID,
		time.Now().UTC(),
		scheduleID,
	)
	return err
}

func nullableExistingUserIDTx(tx *sql.Tx, userID int64) any {
	if userID <= 0 {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = $1`, userID).Scan(&exists); err != nil {
		return nil
	}
	return userID
}

func (a *App) collectBookingPayment(scheduleID int64, paymentMethod string, amount float64, paymentNote string, recordedByUserID int64, allowOverpayment bool, discountedSettlement ...bool) (int64, error) {
	return a.collectBookingPaymentAt(scheduleID, paymentMethod, amount, paymentNote, time.Now().UTC(), recordedByUserID, allowOverpayment, discountedSettlement...)
}

func (a *App) collectBookingPaymentAt(scheduleID int64, paymentMethod string, amount float64, paymentNote string, collectedAt time.Time, recordedByUserID int64, allowOverpayment bool, discountedSettlement ...bool) (int64, error) {
	var adjustment bookingPaymentAdjustment
	if len(discountedSettlement) > 0 && discountedSettlement[0] {
		adjustment.DiscountAmount = -1
	}
	return a.collectBookingPaymentAtWithAdjustment(scheduleID, paymentMethod, amount, paymentNote, collectedAt, recordedByUserID, allowOverpayment, adjustment)
}

func (a *App) collectBookingPaymentAtWithAdjustment(scheduleID int64, paymentMethod string, amount float64, paymentNote string, collectedAt time.Time, recordedByUserID int64, allowOverpayment bool, adjustment bookingPaymentAdjustment) (int64, error) {
	paymentMethod = normalizePaymentMethod(paymentMethod)
	if !validPaymentMethod(paymentMethod) {
		return 0, errors.New("booking payment method is invalid")
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, errors.New("booking payment amount is invalid")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return 0, errors.New("booking payment amount must be greater than zero")
	}
	if amount > maxBookingCashCollection {
		return 0, errors.New("booking payment amount exceeds the allowed limit")
	}
	if math.IsNaN(adjustment.OverpaymentAmount) || math.IsInf(adjustment.OverpaymentAmount, 0) {
		return 0, errors.New("booking overpayment amount is invalid")
	}
	if math.IsNaN(adjustment.DiscountAmount) || math.IsInf(adjustment.DiscountAmount, 0) {
		return 0, errors.New("booking discount amount is invalid")
	}
	adjustment.OverpaymentAmount = normalizeMoney(adjustment.OverpaymentAmount)
	adjustment.DiscountAmount = normalizeMoney(adjustment.DiscountAmount)
	adjustment.AdjustmentReason = strings.TrimSpace(adjustment.AdjustmentReason)
	if adjustment.OverpaymentAmount > 0 && adjustment.DiscountAmount > 0 {
		return 0, errors.New("overpayment and discount cannot be applied together")
	}
	legacyDiscountSettlement := adjustment.DiscountAmount < 0
	if legacyDiscountSettlement {
		adjustment.DiscountAmount = 0
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var financial BookingFinancial
	var paid int
	if err := a.queryRowTxDB(tx, `
		SELECT bf.id, bf.quoted_amount, bf.paid, s.status, COALESCE(s.requester_name, ''), s.activity, s.quantity
		FROM booking_financials bf
		JOIN space_schedules s ON s.id = bf.schedule_id
		WHERE bf.schedule_id = ?
	`, scheduleID).Scan(
		&financial.ID, &financial.QuotedAmount, &paid, &financial.Status,
		&financial.RequesterName, &financial.Activity, &financial.Quantity,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("booking receivable was not found")
		}
		return 0, err
	}
	if !bookingPaymentCollectibleStatus(financial.Status) {
		return 0, errors.New("this booking cannot receive a payment in its current state")
	}
	if financial.QuotedAmount <= 0 {
		return 0, errors.New("booking has no collectible price")
	}
	financials, err := listBookingFinancialsForScheduleIDsQuery(tx, a.runtimeConfig.DBDriver, []int64{scheduleID})
	if err != nil {
		return 0, err
	}
	current := bookingFinancialForSchedule(financials, scheduleID)
	outstanding := financial.QuotedAmount
	if current != nil {
		outstanding = current.OutstandingAmount
		if current.OutstandingAmount < 0.005 && !allowOverpayment {
			return 0, ErrBookingPaymentAlreadyCollected
		}
	}
	if amount > outstanding+0.005 && adjustment.OverpaymentAmount <= 0 && !allowOverpayment {
		return 0, ErrBookingPaymentNeedsOverpayApproval
	}
	if legacyDiscountSettlement && amount >= outstanding-0.005 {
		legacyDiscountSettlement = false
	}
	if adjustment.OverpaymentAmount > 0 {
		if adjustment.AdjustmentReason == "" {
			return 0, errors.New("overpayment reason is required")
		}
		if amount < outstanding-0.005 || amount > outstanding+0.005 {
			return 0, errors.New("payment amount must match the outstanding balance before adding overpayment")
		}
		if !allowOverpayment {
			return 0, ErrBookingPaymentNeedsOverpayApproval
		}
	}
	if adjustment.DiscountAmount > 0 {
		if adjustment.AdjustmentReason == "" {
			return 0, errors.New("discount reason is required")
		}
		if amount+adjustment.DiscountAmount > outstanding+0.005 || amount+adjustment.DiscountAmount < outstanding-0.005 {
			return 0, errors.New("payment amount plus discount must match the outstanding balance")
		}
	}
	applyDiscountedSettlement := legacyDiscountSettlement || adjustment.DiscountAmount > 0
	_ = paid
	personName := bookingFinancialDisplayName(financial)
	collectedAt = collectedAt.UTC()
	now := time.Now().UTC()
	recordedByRef := nullableExistingUserIDTx(tx, recordedByUserID)
	receiptNumber, err := a.nextReceiptNumberTx(tx, "booking_payment", collectedAt)
	if err != nil {
		return 0, err
	}
	divisionID, err := financeDivisionIDForEntryTx(tx, financeTransactionCreate{
		ReferenceType: "space_schedule",
		ReferenceID:   scheduleID,
	})
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	description := fmt.Sprintf("%s payment for %s", bookingProductLabel(financial.Activity, financial.Quantity), bookingReference(scheduleID))
	transactionAmount := amount + adjustment.OverpaymentAmount
	if transactionAmount > maxBookingCashCollection {
		return 0, errors.New("booking payment amount exceeds the allowed limit")
	}
	transactionNotes := strings.TrimSpace(paymentNote)
	if adjustment.OverpaymentAmount > 0 {
		transactionNotes = joinPaymentNotes(transactionNotes, fmt.Sprintf("Overpayment: %.2f", adjustment.OverpaymentAmount), "Reason: "+adjustment.AdjustmentReason)
	}
	if adjustment.DiscountAmount > 0 {
		transactionNotes = joinPaymentNotes(transactionNotes, fmt.Sprintf("Discount: %.2f", adjustment.DiscountAmount), "Reason: "+adjustment.AdjustmentReason)
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    receiptNumber,
		ReferenceNumber:  receiptNumber,
		Category:         "booking_payment",
		TransactionType:  financeTxnTypeIncome,
		ReferenceType:    "space_schedule",
		ReferenceID:      scheduleID,
		FinanceAccountID: account.ID,
		PersonName:       personName,
		Description:      description,
		Notes:            transactionNotes,
		PaymentMethod:    paymentMethod,
		Amount:           transactionAmount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       collectedAt,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(a.dbQuery(`
		INSERT INTO booking_payment_collections (
			schedule_id,
			finance_transaction_id,
			amount,
			payment_method,
			payment_note,
			collected_by_user_id,
			collected_at,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`), scheduleID, transactionID, transactionAmount, paymentMethod, transactionNotes, recordedByRef, collectedAt, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(a.dbQuery(`
		UPDATE finance_transactions
		SET source_type = 'booking_payment_collection',
		    source_id = (
				SELECT id FROM booking_payment_collections
				WHERE finance_transaction_id = ?
				LIMIT 1
			),
		    updated_at = ?
		WHERE id = ?
	`), transactionID, now, transactionID); err != nil {
		return 0, err
	}
	if applyDiscountedSettlement {
		discountedQuotedAmount := amount
		if current != nil {
			discountedQuotedAmount = normalizeMoney(current.TotalCollected + amount)
		}
		if adjustment.DiscountAmount > 0 {
			discountedQuotedAmount = normalizeMoney(discountedQuotedAmount)
		}
		if _, err := tx.Exec(a.dbQuery(`
			UPDATE booking_financials
			SET quoted_amount = ?, updated_at = ?
			WHERE schedule_id = ?
		`), discountedQuotedAmount, now, scheduleID); err != nil {
			return 0, err
		}
	}
	if err := a.syncBookingFinancialSnapshotTx(tx, scheduleID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) voidBookingPayment(collectionID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var scheduleID int64
	var financeTransactionID int64
	var voided int
	if err := a.queryRowTxDB(tx, `
		SELECT schedule_id, finance_transaction_id, voided
		FROM booking_payment_collections
		WHERE id = ?
	`, collectionID).Scan(&scheduleID, &financeTransactionID, &voided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("booking payment was not found")
		}
		return err
	}
	if voided == 1 {
		return errors.New("booking payment has already been voided")
	}
	voidedByRef := nullableExistingUserIDTx(tx, voidedByUserID)
	if _, err := a.execTxDB(tx, `
		UPDATE booking_payment_collections
		SET voided = 1, void_reason = ?, voided_by_user_id = ?, voided_at = ?
		WHERE id = ? AND voided = 0
	`, reason, voidedByRef, time.Now().UTC(), collectionID); err != nil {
		return err
	}
	if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, voidedByUserID); err != nil {
		return err
	}
	if err := a.syncBookingFinancialSnapshotTx(tx, scheduleID); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) updateBookingRequestStatus(
	scheduleID int64,
	status string,
	reviewNote string,
	customerMessage string,
) (*SpaceSchedule, error) {
	updated, _, err := a.transitionBookingRequestStatus(scheduleID, status, reviewNote, customerMessage, "admin", 0)
	return updated, err
}

func (a *App) transitionBookingRequestStatus(
	scheduleID int64,
	status string,
	reviewNote string,
	customerMessage string,
	changeSource string,
	changedByUserID int64,
) (*SpaceSchedule, int64, error) {
	switch status {
	case bookingStatusConfirmed, bookingStatusRejected, bookingStatusHeld, bookingStatusCancelled, bookingStatusExpired:
	default:
		return nil, 0, errors.New("invalid booking request status")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	schedule, err := findSpaceScheduleByIDQuery(
		tx,
		a.runtimeConfig.DBDriver,
		scheduleID,
	)
	if err != nil {
		return nil, 0, err
	}
	if schedule.EntryType != "booking" {
		return nil, 0, errors.New("only customer bookings can use this workflow")
	}
	if schedule.Status != bookingStatusPending && schedule.Status != bookingStatusHeld && schedule.Status != bookingStatusReschedulePending {
		return nil, 0, errors.New("booking request is no longer awaiting action")
	}
	if status == bookingStatusRejected && strings.TrimSpace(customerMessage) == "" {
		return nil, 0, errors.New("customer message is required")
	}
	if status == bookingStatusRejected && strings.TrimSpace(reviewNote) == "" {
		return nil, 0, errors.New("review note is required")
	}

	if status == bookingStatusConfirmed {
		courtActivities, courtLayouts, err := activeBookingConfigurationQuery(tx)
		if err != nil {
			return nil, 0, fmt.Errorf("load active court configuration: %w", err)
		}
		courtClosures, err := listActiveCourtClosuresQuery(tx)
		if err != nil {
			return nil, 0, fmt.Errorf("load active court closures: %w", err)
		}
		if err := validateBookableScheduleTime(*schedule, time.Now()); err != nil {
			return nil, 0, err
		}
		if err := validateConfiguredBookingOption(*schedule, courtActivities, courtLayouts); err != nil {
			return nil, 0, err
		}
		if err := validateScheduleAgainstClosures(*schedule, courtClosures); err != nil {
			return nil, 0, err
		}
		existing, err := querySchedulesForSlot(
			tx,
			a.runtimeConfig.DBDriver,
			schedule.SlotDate,
			schedule.SlotHour,
			schedule.ID,
		)
		if err != nil {
			return nil, 0, err
		}
		if err := validateSpaceScheduleSlotAgainstLayouts(existing, *schedule, courtLayouts); err != nil {
			return nil, 0, err
		}
	}

	now := time.Now().UTC()
	result, err := a.execTxDB(tx, `
		UPDATE space_schedules
		SET
			status = ?,
			review_note = ?,
			customer_message = ?,
			status_changed_at = ?,
			status_changed_by_user_id = ?,
			status_change_source = ?,
			updated_at = ?
		WHERE id = ?
		  AND status = ?
	`,
		status,
		reviewNote,
		customerMessage,
		now,
		nullIfZero(changedByUserID),
		changeSource,
		now,
		scheduleID,
		schedule.Status,
	)
	if err != nil {
		return nil, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	if affected == 0 {
		return nil, 0, errors.New("booking request is no longer awaiting action")
	}

	financial := bookingFinancialForSchedule(mustListBookingFinancialsTx(
		tx,
		a.runtimeConfig.DBDriver,
		scheduleID,
	), scheduleID)
	if financial != nil {
		schedule.QuotedPrice = financial.QuotedAmount
	}
	changeID, err := a.recordBookingLifecycleChangeTx(tx, schedule, status, schedule.Status, status, reviewNote, customerMessage, "", changeSource, changedByUserID)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}

	updated := *schedule
	updated.Status = status
	updated.ReviewNote = reviewNote
	updated.CustomerMessage = customerMessage
	updated.StatusChangedAt = now
	updated.StatusChangedBy = changedByUserID
	updated.StatusSource = changeSource
	updated.UpdatedAt = now
	return &updated, changeID, nil
}

func (a *App) rescheduleBookingRequest(
	scheduleID int64,
	proposed SpaceSchedule,
	reviewNote string,
	customerMessage string,
	actionType string,
	confirm bool,
	changedByUserID int64,
) (*BookingRequestRescheduleResult, error) {
	if actionType != "rescheduled" &&
		actionType != "rescheduled_confirmed" {
		return nil, errors.New("invalid booking request change action")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := findSpaceScheduleByIDQuery(
		tx,
		a.runtimeConfig.DBDriver,
		scheduleID,
	)
	if err != nil {
		return nil, err
	}

	if current.Status != bookingStatusPending && current.Status != bookingStatusHeld && current.Status != bookingStatusReschedulePending {
		return nil, errors.New("booking request is no longer awaiting action")
	}
	if current.EntryType != "booking" {
		return nil, errors.New("only customer booking requests can be rescheduled")
	}
	if current.RequesterName == "" &&
		current.RequesterEmail == "" &&
		current.RequestedByUser == 0 {
		return nil, errors.New("only customer booking requests can be rescheduled")
	}

	var financial struct {
		ID           int64
		QuotedAmount float64
		Paid         bool
	}
	financialErr := a.queryRowTxDB(tx, `
		SELECT id, quoted_amount, paid
		FROM booking_financials
		WHERE schedule_id = ?
	`, scheduleID).Scan(&financial.ID, &financial.QuotedAmount, &financial.Paid)
	if errors.Is(financialErr, sql.ErrNoRows) {
		return nil, errors.New("booking financial record was not found for this request")
	}
	if financialErr != nil {
		return nil, financialErr
	}
	if financial.Paid {
		return nil, errors.New("paid bookings cannot be rescheduled through the request workflow")
	}

	courtActivities, courtLayouts, err := activeBookingConfigurationQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load active court configuration: %w", err)
	}
	courtClosures, err := listActiveCourtClosuresQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load active court closures: %w", err)
	}
	pricings, err := listPricingRulesQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load pricing rules: %w", err)
	}
	settings, err := getPricingSettingsQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load pricing settings: %w", err)
	}

	updated := *current
	updated.SlotDate = proposed.SlotDate
	updated.SlotHour = proposed.SlotHour
	updated.EntryType = "booking"
	updated.Activity = proposed.Activity
	updated.Quantity = proposed.Quantity
	updated.ReviewNote = reviewNote
	updated.CustomerMessage = customerMessage
	if confirm {
		updated.Status = bookingStatusConfirmed
	} else {
		updated.Status = bookingStatusReschedulePending
	}

	slotChanged := current.SlotDate != updated.SlotDate ||
		current.SlotHour != updated.SlotHour ||
		current.Activity != updated.Activity ||
		current.Quantity != updated.Quantity

	if slotChanged && strings.TrimSpace(reviewNote) == "" {
		return nil, errors.New("review note is required when changing the requested slot")
	}
	if slotChanged && strings.TrimSpace(customerMessage) == "" {
		return nil, errors.New("customer message is required when changing the requested slot")
	}

	if err := validateBookableScheduleTime(updated, time.Now()); err != nil {
		return nil, err
	}
	if err := validateConfiguredBookingOption(updated, courtActivities, courtLayouts); err != nil {
		return nil, err
	}
	if err := validateScheduleAgainstClosures(updated, courtClosures); err != nil {
		return nil, err
	}

	existing, err := querySchedulesForSlot(
		tx,
		a.runtimeConfig.DBDriver,
		updated.SlotDate,
		updated.SlotHour,
		updated.ID,
	)
	if err != nil {
		return nil, err
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, updated, courtLayouts); err != nil {
		return nil, err
	}

	rule := pricingRuleForOption(pricings, updated.Activity, updated.Quantity)
	if rule == nil {
		return nil, errors.New("pricing is not configured for this booking")
	}
	updated.QuotedPrice = priceForRuleSlot(*rule, settings, updated.SlotDate, updated.SlotHour)
	if updated.QuotedPrice <= 0 {
		return nil, errors.New("a positive price is required before confirming this booking")
	}

	now := time.Now().UTC()
	result, err := a.execTxDB(tx, `
		UPDATE space_schedules
		SET
			slot_date = ?,
			slot_hour = ?,
			activity = ?,
			quantity = ?,
			status = ?,
			review_note = ?,
			customer_message = ?,
			status_changed_at = ?,
			status_changed_by_user_id = ?,
			status_change_source = ?,
			updated_at = ?
		WHERE id = ?
		  AND status = ?
	`,
		updated.SlotDate,
		updated.SlotHour,
		updated.Activity,
		updated.Quantity,
		updated.Status,
		reviewNote,
		customerMessage,
		now,
		nullIfZero(changedByUserID),
		actionType,
		now,
		scheduleID,
		current.Status,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, errors.New("booking request is no longer awaiting action")
	}

	if _, err := a.execTxDB(tx, `
		UPDATE booking_financials
		SET quoted_amount = ?, updated_at = ?
		WHERE id = ?
	`, updated.QuotedPrice, now, financial.ID); err != nil {
		return nil, err
	}

	var changeID int64
	if slotChanged || financial.QuotedAmount != updated.QuotedPrice {
		var changedBy any
		if changedByUserID > 0 {
			changedBy = changedByUserID
		}
		changeID, err = a.insertAndReturnIDTx(tx, `
			INSERT INTO booking_request_changes (
				schedule_id,
				previous_slot_date,
				previous_slot_hour,
				previous_activity,
				previous_quantity,
				previous_quoted_price,
				new_slot_date,
				new_slot_hour,
				new_activity,
				new_quantity,
				new_quoted_price,
				action_type,
				previous_status,
				new_status,
				change_source,
				review_note,
				customer_message,
				changed_by_user_id,
				changed_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			scheduleID,
			current.SlotDate,
			current.SlotHour,
			current.Activity,
			current.Quantity,
			financial.QuotedAmount,
			updated.SlotDate,
			updated.SlotHour,
			updated.Activity,
			updated.Quantity,
			updated.QuotedPrice,
			actionType,
			current.Status,
			updated.Status,
			actionType,
			reviewNote,
			customerMessage,
			changedBy,
			now,
		)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &BookingRequestRescheduleResult{
		Schedule: &updated,
		ChangeID: changeID,
	}, nil
}

func (a *App) deleteAdmission(admissionID int64) error {
	var monthlyPaymentCount int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM student_monthly_payments
		WHERE admission_id = ?
	`, admissionID).Scan(&monthlyPaymentCount); err != nil {
		return err
	}
	if monthlyPaymentCount > 0 {
		return ErrAdmissionHasMonthlyPaymentHistory
	}
	result, err := a.execDB(`DELETE FROM admissions WHERE id = ?`, admissionID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) deleteStudentGroup(groupID int64) error {
	_, err := a.execDB(`DELETE FROM student_groups WHERE id = ?`, groupID)
	return err
}

func (a *App) deleteSpaceSchedule(scheduleID int64) error {
	var activeCollections int
	if err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM booking_payment_collections
		WHERE schedule_id = ?
		  AND voided = 0
	`, scheduleID).Scan(&activeCollections); err != nil {
		return err
	}
	if activeCollections > 0 {
		return errors.New("this booking already has collected payments and cannot be deleted")
	}
	result, err := a.execDB(`DELETE FROM space_schedules WHERE id = ?`, scheduleID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) deletePricingRule(pricingID int64) error {
	_, err := a.execDB(`DELETE FROM pricing_rules WHERE id = ?`, pricingID)
	return err
}

func (a *App) deleteCourtActivity(activityID int64) error {
	var activity string
	if err := a.queryRowDB(`
		SELECT activity
		FROM court_activities
		WHERE id = ?
	`, activityID).Scan(&activity); err != nil {
		return err
	}

	checks := []struct {
		query           string
		message         string
		needsActivityID bool
	}{
		{
			query: `
				SELECT COUNT(*)
				FROM court_layout_items cli
				JOIN court_layouts cl
					ON cl.id = cli.layout_id
				WHERE cli.activity = ?
				  AND cl.court_id = (
					  SELECT court_id
					  FROM court_activities
					  WHERE id = ?
				  )
			`,
			message:         "this activity is used by one or more court layouts",
			needsActivityID: true,
		},
		{
			query: `
				SELECT COUNT(*)
				FROM court_closures
				WHERE activity = ?
				  AND court_id = (
					  SELECT court_id
					  FROM court_activities
					  WHERE id = ?
				  )
			`,
			message:         "this activity is used by one or more court closures",
			needsActivityID: true,
		},
		{
			query: `
				SELECT COUNT(*)
				FROM space_schedules
				WHERE activity = ?
			`,
			message: "this activity is already used by bookings or internal schedules",
		},
		{
			query: `
				SELECT COUNT(*)
				FROM pricing_rules
				WHERE activity = ?
			`,
			message: "this activity is linked to one or more pricing rules",
		},
	}

	for _, check := range checks {
		var count int
		args := []any{activity}
		if check.needsActivityID {
			args = append(args, activityID)
		}
		if err := a.queryRowDB(
			check.query,
			args...,
		).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return errors.New(check.message)
		}
	}

	result, err := a.execDB(
		`DELETE FROM court_activities WHERE id = ?`,
		activityID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) deleteEvent(eventID int64) error {
	_, err := a.execDB(`DELETE FROM events WHERE id = ?`, eventID)
	return err
}
