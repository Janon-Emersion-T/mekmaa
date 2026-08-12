package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

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

	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transactions []FinanceTransaction
	for rows.Next() {
		var transaction FinanceTransaction
		var voidedAt sql.NullTime
		var updatedAtRaw string
		if err := rows.Scan(
			&transaction.ID,
			&transaction.ReceiptNumber,
			&transaction.ReferenceNumber,
			&transaction.Category,
			&transaction.TransactionType,
			&transaction.ReferenceType,
			&transaction.ReferenceID,
			&transaction.SourceType,
			&transaction.SourceID,
			&transaction.FinanceAccountID,
			&transaction.FinanceAccountName,
			&transaction.FinanceAccountType,
			&transaction.TransferGroupID,
			&transaction.PersonName,
			&transaction.Description,
			&transaction.Notes,
			&transaction.PaymentMethod,
			&transaction.Amount,
			&transaction.RecordedByUser,
			&transaction.RecordedByUserName,
			&transaction.RecordedAt,
			&transaction.CreatedAt,
			&updatedAtRaw,
			&voidedAt,
			&transaction.VoidedByUserID,
			&transaction.VoidReason,
		); err != nil {
			return nil, err
		}
		if voidedAt.Valid {
			transaction.Voided = true
			transaction.VoidedAt = voidedAt.Time
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

func financeTransactionsBaseQuery(filter FinanceFilter) (string, []any) {
	query := `
		SELECT ft.id,
		       ft.receipt_number,
		       COALESCE(ft.reference_number, ft.receipt_number),
		       ft.category,
		       COALESCE(ft.transaction_type, CASE WHEN ft.amount < 0 THEN 'expense' ELSE 'income' END),
		       ft.reference_type,
		       COALESCE(ft.reference_id, 0),
		       COALESCE(ft.source_type, ''),
		       COALESCE(ft.source_id, 0),
		       COALESCE(ft.finance_account_id, 0),
		       COALESCE(fa.name, ''),
		       COALESCE(fa.account_type, ''),
		       COALESCE(ft.transfer_group_id, ''),
		       ft.person_name,
		       ft.description,
		       COALESCE(ft.notes, ''),
		       ft.payment_method,
		       ft.amount,
		       COALESCE(ft.recorded_by_user_id, 0),
		       COALESCE(u.name, ''),
		       ft.recorded_at,
		       ft.created_at,
		       COALESCE(CAST(ft.updated_at AS TEXT), CAST(ft.created_at AS TEXT), ''),
		       ft.voided_at,
		       COALESCE(ft.voided_by_user_id, 0),
		       COALESCE(ft.void_reason, '')
		FROM finance_transactions ft
		LEFT JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		LEFT JOIN users u ON u.id = ft.recorded_by_user_id
		WHERE 1 = 1`
	args := make([]any, 0, 12)
	if filter.From != "" {
		query += ` AND SUBSTR(TRIM(CAST(ft.recorded_at AS TEXT)), 1, 10) >= ?`
		args = append(args, filter.From)
	}
	if filter.To != "" {
		query += ` AND SUBSTR(TRIM(CAST(ft.recorded_at AS TEXT)), 1, 10) <= ?`
		args = append(args, filter.To)
	}
	switch filter.Direction {
	case "income":
		query += ` AND ft.amount > 0`
	case "expense":
		query += ` AND ft.amount < 0`
	}
	if filter.Category != "" {
		query += ` AND ft.category = ?`
		args = append(args, filter.Category)
	}
	if filter.AccountID > 0 {
		query += ` AND ft.finance_account_id = ?`
		args = append(args, filter.AccountID)
	}
	if filter.TransactionType != "" {
		query += ` AND ft.transaction_type = ?`
		args = append(args, filter.TransactionType)
	}
	if filter.SourceType != "" {
		query += ` AND ft.source_type = ?`
		args = append(args, filter.SourceType)
	}
	if filter.PaymentMethod != "" {
		query += ` AND ft.payment_method = ?`
		args = append(args, filter.PaymentMethod)
	}
	if filter.RecordedUserID > 0 {
		query += ` AND ft.recorded_by_user_id = ?`
		args = append(args, filter.RecordedUserID)
	}
	switch filter.Status {
	case "active":
		query += ` AND ft.voided_at IS NULL`
	case "voided":
		query += ` AND ft.voided_at IS NOT NULL`
	}
	if filter.Reference != "" {
		query += ` AND (LOWER(COALESCE(ft.reference_number, '')) LIKE ? OR LOWER(COALESCE(ft.receipt_number, '')) LIKE ?)`
		term := "%" + strings.ToLower(filter.Reference) + "%"
		args = append(args, term, term)
	}
	if filter.Search != "" {
		query += ` AND (
			LOWER(COALESCE(ft.receipt_number, '')) LIKE ?
			OR LOWER(COALESCE(ft.reference_number, '')) LIKE ?
			OR LOWER(COALESCE(ft.person_name, '')) LIKE ?
			OR LOWER(COALESCE(ft.description, '')) LIKE ?
			OR LOWER(COALESCE(ft.notes, '')) LIKE ?
			OR LOWER(COALESCE(fa.name, '')) LIKE ?
		)`
		term := "%" + strings.ToLower(filter.Search) + "%"
		args = append(args, term, term, term, term, term, term)
	}
	return query, args
}

func (a *App) countFinanceTransactions(ctx context.Context, filter FinanceFilter) (int, error) {
	query, args := financeTransactionsBaseQuery(filter)
	var count int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`) AS finance_transaction_count`, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *App) listOutstandingBookingFinancials() ([]BookingFinancial, error) {
	financials, err := a.listBookingFinancials()
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
		filtered = append(filtered, financial)
	}
	return filtered, nil
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
	if paymentMonth < currentMonth {
		return true
	}
	if paymentMonth > currentMonth {
		return false
	}
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	return now.Day() >= lastDay
}

func latestCollectiblePaymentMonth(now time.Time) string {
	currentMonth := now.Format("2006-01")
	if paymentMonthCollectible(currentMonth, now) {
		return currentMonth
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -1, 0).Format("2006-01")
}

func monthlyPaymentCollectionNotice(paymentMonth string, now time.Time) string {
	if paymentMonthCollectible(paymentMonth, now) {
		return ""
	}
	lastCollectibleDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location())
	return "Monthly payments for " + paymentMonthLabel(paymentMonth) + " can only be collected on " + lastCollectibleDay.Format("January 2, 2006") + " or later."
}

func paymentBillingStartDate(enrollment *StudentEnrollment, admission *Admission) (time.Time, error) {
	if enrollment != nil && !enrollment.CreatedAt.IsZero() {
		start := enrollment.CreatedAt.In(time.Local)
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local), nil
	}
	if admission == nil || strings.TrimSpace(admission.AdmissionDate) == "" {
		return time.Time{}, errors.New("student admission date is required for monthly billing")
	}
	start, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(admission.AdmissionDate), time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return start, nil
}

func applyFirstMonthEnrollmentDiscount(baseAmount float64, billingStart time.Time, paymentMonth string, monthDays int) (float64, float64) {
	if baseAmount <= 0 || billingStart.IsZero() || monthDays <= 0 {
		return baseAmount, 0
	}
	if billingStart.Format("2006-01") != paymentMonth {
		return baseAmount, 0
	}
	secondHalfStartDay := (monthDays / 2) + 1
	if billingStart.Day() < secondHalfStartDay {
		return baseAmount, 0
	}
	discounted := math.Round((baseAmount*0.5)*100) / 100
	return discounted, math.Round((baseAmount-discounted)*100) / 100
}

func (a *App) listStudentPaymentRows(paymentMonth string) ([]StudentPaymentRow, error) {
	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		return nil, err
	}
	monthDays := monthDate.AddDate(0, 1, -1).Day()
	monthEnd := monthDate.AddDate(0, 1, -1).Format("2006-01-02")
	rows, err := a.db.Query(`
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
			payment_rows.free_monthly_fee,
			payment_rows.original_monthly_fee,
			payment_rows.payment_id,
			payment_rows.payment_amount,
			payment_rows.payment_method,
			payment_rows.finance_transaction_id,
			payment_rows.collected_by_user_id,
			payment_rows.collected_at,
			payment_rows.payment_created_at,
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
				COALESCE(se.free_monthly_fee, 0) AS free_monthly_fee,
				COALESCE(tp.monthly_fee, 0) AS original_monthly_fee,
				smp.id AS payment_id,
				smp.amount AS payment_amount,
				smp.payment_method AS payment_method,
				smp.finance_transaction_id AS finance_transaction_id,
				COALESCE(smp.collected_by_user_id, 0) AS collected_by_user_id,
				smp.collected_at AS collected_at,
				smp.created_at AS payment_created_at,
				COALESCE(CAST(se.created_at AS TEXT), '') AS enrollment_created_at,
				COALESCE(tp.sort_order, 0) AS program_sort_order
			FROM student_enrollments se
			JOIN admissions a
				ON a.id = se.admission_id
			JOIN training_programs tp
				ON tp.id = se.training_program_id
			LEFT JOIN student_monthly_payments smp
				ON smp.enrollment_id = se.id AND smp.payment_month = ? AND COALESCE(smp.voided, 0) = 0
			WHERE a.admission_date <= ?
			  AND COALESCE(se.active, 1) = 1

			UNION ALL

			SELECT
				0 AS enrollment_id,
				a.id AS admission_id,
				a.student_id,
				a.full_name,
				COALESCE(a.admission_date, '') AS admission_date,
				a.date_of_birth,
				a.gender,
				COALESCE(a.photo_path, '') AS photo_path,
				COALESCE(a.qr_code_path, '') AS qr_code_path,
				COALESCE(a.qr_code_value, '') AS qr_code_value,
				COALESCE(tp.id, 0) AS training_program_id,
				COALESCE(
					tp.name,
					CASE
						WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
						ELSE ''
					END
				) AS training_program_name,
				COALESCE(a.free_monthly_fee, 0) AS free_monthly_fee,
				COALESCE(tp.monthly_fee, ap.monthly_fee, 0) AS original_monthly_fee,
				smp.id AS payment_id,
				smp.amount AS payment_amount,
				smp.payment_method AS payment_method,
				smp.finance_transaction_id AS finance_transaction_id,
				COALESCE(smp.collected_by_user_id, 0) AS collected_by_user_id,
				smp.collected_at AS collected_at,
				smp.created_at AS payment_created_at,
				'' AS enrollment_created_at,
				COALESCE(tp.sort_order, 0) AS program_sort_order
			FROM admissions a
			LEFT JOIN training_programs tp
				ON tp.id = a.training_program_id
			LEFT JOIN admission_pricing ap
				ON ap.practice_type = a.practice_type
			LEFT JOIN student_monthly_payments smp
				ON smp.admission_id = a.id
				AND (smp.enrollment_id IS NULL OR smp.enrollment_id = 0)
				AND smp.payment_month = ?
				AND COALESCE(smp.voided, 0) = 0
			WHERE a.admission_date <= ?
			  AND NOT EXISTS (
				SELECT 1
				FROM student_enrollments se
				WHERE se.admission_id = a.id
				  AND COALESCE(se.active, 1) = 1
			  )
		) AS payment_rows
		ORDER BY
			CASE WHEN payment_rows.payment_id IS NULL THEN 0 ELSE 1 END,
			payment_rows.full_name COLLATE NOCASE,
			payment_rows.program_sort_order ASC,
			payment_rows.training_program_name ASC,
			payment_rows.enrollment_id,
			payment_rows.admission_id
	`, paymentMonth, monthEnd, paymentMonth, monthEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paymentRows []StudentPaymentRow
	enrollmentIDs := make([]int64, 0)
	for rows.Next() {
		var (
			row                 StudentPaymentRow
			enrollmentID        int64
			freeMonthlyFee      int
			paymentID           sql.NullInt64
			paymentAmount       sql.NullFloat64
			paymentMethod       sql.NullString
			transactionID       sql.NullInt64
			collectedByUserID   sql.NullInt64
			collectedAt         sql.NullTime
			paymentCreatedAt    sql.NullTime
			enrollmentCreatedAt string
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
			&freeMonthlyFee, &row.OriginalMonthlyFee, &paymentID, &paymentAmount, &paymentMethod,
			&transactionID, &collectedByUserID, &collectedAt, &paymentCreatedAt, &enrollmentCreatedAt,
		); err != nil {
			return nil, err
		}
		row.Enrollment.ID = enrollmentID
		row.Enrollment.AdmissionID = row.Admission.ID
		row.Enrollment.Student = row.Admission
		row.Enrollment.FreeMonthlyFee = freeMonthlyFee == 1
		if strings.TrimSpace(enrollmentCreatedAt) != "" {
			createdAt, err := time.Parse("2006-01-02 15:04:05", enrollmentCreatedAt)
			if err != nil {
				createdAt, err = time.Parse(time.RFC3339Nano, enrollmentCreatedAt)
			}
			if err != nil {
				createdAt, err = time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", enrollmentCreatedAt)
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
		}
		if enrollmentID > 0 {
			enrollmentIDs = append(enrollmentIDs, enrollmentID)
		}
		row.Admission.TrainingProgramName = row.Enrollment.TrainingProgramName
		row.Admission.TrainingProgramNames = row.Enrollment.TrainingProgramName
		if paymentID.Valid {
			row.Payment = &StudentMonthlyPayment{
				ID:                   paymentID.Int64,
				AdmissionID:          row.Admission.ID,
				EnrollmentID:         row.Enrollment.ID,
				PaymentMonth:         paymentMonth,
				Amount:               paymentAmount.Float64,
				PaymentMethod:        paymentMethod.String,
				FinanceTransactionID: transactionID.Int64,
				CollectedByUserID:    collectedByUserID.Int64,
				CollectedAt:          collectedAt.Time,
				CreatedAt:            paymentCreatedAt.Time,
			}
		}
		paymentRows = append(paymentRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
		if paymentRows[i].Enrollment.ID <= 0 {
			continue
		}
		paymentRows[i].Leaves = leaveMap[paymentRows[i].Enrollment.ID]
		if paymentRows[i].Enrollment.FreeMonthlyFee || paymentRows[i].OriginalMonthlyFee <= 0 {
			continue
		}
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

	enrollment, err := findStudentEnrollmentByIDTx(tx, enrollmentID)
	if err != nil {
		return err
	}
	if enrollment.Student.AdmissionDate != "" && startDate < enrollment.Student.AdmissionDate {
		return errors.New("leave cannot start before the student admission date")
	}

	var overlapCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM student_enrollment_leaves
		WHERE enrollment_id = ?
		  AND COALESCE(active, 1) = 1
		  AND NOT (end_date < ? OR start_date > ?)
	`, enrollmentID, startDate, endDate).Scan(&overlapCount); err != nil {
		return err
	}
	if overlapCount > 0 {
		return ErrStudentLeaveOverlap
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(`
		INSERT INTO student_enrollment_leaves (
			enrollment_id, start_date, end_date, reason, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, 1, ?, ?)
	`, enrollmentID, startDate, endDate, reason, now, now); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deleteStudentEnrollmentLeave(leaveID int64, enrollmentID int64) error {
	if leaveID <= 0 || enrollmentID <= 0 {
		return errors.New("select a valid leave record")
	}
	result, err := a.db.Exec(`DELETE FROM student_enrollment_leaves WHERE id = ? AND enrollment_id = ?`, leaveID, enrollmentID)
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
	query += ` ORDER BY active DESC, name COLLATE NOCASE, id`
	rows, err := a.db.Query(query)
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
	rows, err := a.db.Query(`
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
	return listBookingReferralsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingPaymentCollectionsForScheduleIDs(scheduleIDs []int64) ([]BookingPaymentCollection, error) {
	return listBookingPaymentCollectionsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingFinancials() ([]BookingFinancial, error) {
	rows, err := a.db.Query(`SELECT schedule_id FROM booking_financials ORDER BY schedule_id`)
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
	return listBookingFinancialsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingFinancialsForScheduleIDs(scheduleIDs []int64) ([]BookingFinancial, error) {
	return listBookingFinancialsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingRequestChanges() ([]BookingRequestChange, error) {
	rows, err := a.db.Query(`
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
	return listBookingRequestChangesForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listActiveSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
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
	rows, err := a.db.Query(`
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
	row := a.db.QueryRow(`
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
	row := a.db.QueryRow(`
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
		SELECT id, name, game, audience, price, active, created_at, updated_at
		FROM one_to_one_offerings`
	args := make([]any, 0, 1)
	if !includeInactive {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY active DESC, name, id`
	rows, err := a.db.Query(query, args...)
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
	if err := a.db.QueryRow(`
		SELECT id, name, game, audience, price, active, created_at, updated_at
		FROM one_to_one_offerings
		WHERE id = ?
	`, id).Scan(
		&offering.ID,
		&offering.Name,
		&offering.Game,
		&offering.Audience,
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
	result, err := a.db.Exec(`
		INSERT INTO one_to_one_offerings (
			name,
			game,
			audience,
			price,
			active,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		offering.Name,
		offering.Game,
		offering.Audience,
		offering.Price,
		boolToInt(offering.Active),
		time.Now().UTC(),
		time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (a *App) updateOneToOneOffering(offering OneToOneOffering) error {
	result, err := a.db.Exec(`
		UPDATE one_to_one_offerings
		SET name = ?, game = ?, audience = ?, price = ?, active = ?, updated_at = ?
		WHERE id = ?
	`,
		offering.Name,
		offering.Game,
		offering.Audience,
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
	if err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM one_to_one_bookings
		WHERE offering_id = ?
	`, id).Scan(&bookings); err != nil {
		return err
	}
	if bookings > 0 {
		return errors.New("this 1 to 1 setup already has bookings; set it inactive instead of deleting it")
	}
	result, err := a.db.Exec(`DELETE FROM one_to_one_offerings WHERE id = ?`, id)
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
	rows, err := a.db.Query(`
		SELECT
			ob.id,
			ob.schedule_id,
			ob.offering_id,
			ob.customer_name,
			ob.offering_name,
			ob.game,
			ob.audience,
			ob.price,
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
			&booking.Price,
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

func (a *App) createOneToOneBooking(offering OneToOneOffering, customerName, slotDate, slotHour, notes string) (int64, int64, error) {
	schedule := SpaceSchedule{
		SlotDate:      slotDate,
		SlotHour:      slotHour,
		EntryType:     "booking",
		Activity:      offering.Game,
		Quantity:      1,
		Title:         fmt.Sprintf("1 to 1 · %s · %s", offering.Name, customerName),
		Notes:         buildOneToOneBookingNotes(offering, notes),
		RequesterName: customerName,
		QuotedPrice:   offering.Price,
	}

	courtActivities, courtLayouts, err := a.activeBookingConfiguration()
	if err != nil {
		return 0, 0, fmt.Errorf("load active court configuration: %w", err)
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

	existing, err := querySchedulesForSlot(tx, schedule.SlotDate, schedule.SlotHour, 0)
	if err != nil {
		return 0, 0, err
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, schedule, courtLayouts); err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC()
	result, err := tx.Exec(`
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

	scheduleID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
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
	`, scheduleID, offering.Price, now, now); err != nil {
		return 0, 0, err
	}
	result, err = tx.Exec(`
		INSERT INTO one_to_one_bookings (
			schedule_id,
			offering_id,
			customer_name,
			offering_name,
			game,
			audience,
			price,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, scheduleID, offering.ID, customerName, offering.Name, offering.Game, offering.Audience, offering.Price, now, now)
	if err != nil {
		return 0, 0, err
	}
	bookingID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return bookingID, scheduleID, nil
}

func buildOneToOneBookingNotes(offering OneToOneOffering, notes string) string {
	base := fmt.Sprintf("1 to 1 booking\nProgramme: %s\nGame: %s\nWho: %s\nPrice: %.2f", offering.Name, offering.Game, offering.Audience, normalizeMoney(offering.Price))
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return base
	}
	return base + "\nNotes: " + notes
}

func (a *App) countReschedulePendingSpaceSchedules() (int, error) {
	row := a.db.QueryRow(`
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
	return querySchedulesForSlot(a.db, slotDate, slotHour, excludeID)
}

type scheduleQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func querySchedulesForSlot(queryer scheduleQueryer, slotDate, slotHour string, excludeID int64) ([]SpaceSchedule, error) {
	rows, err := queryer.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		WHERE slot_date = ? AND slot_hour = ? AND id != ? AND status IN ('pending', 'held', 'confirmed', 'reschedule_pending')
		ORDER BY id ASC
	`, slotDate, slotHour, excludeID)
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

func (a *App) listCoachesForGroup(groupID int64) ([]User, error) {
	rows, err := a.db.Query(`
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
		ORDER BY u.name COLLATE NOCASE ASC, u.id ASC
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
	rows, err := a.db.Query(`
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
	_, _, err := a.createAdmissionWithOptionalPayment(admission, false, 0)
	return err
}
func replaceStudentGroupCoachesTx(
	tx *sql.Tx,
	groupID int64,
	coachIDs []int64,
) error {
	if _, err := tx.Exec(
		`DELETE FROM student_group_coaches WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	now := time.Now().UTC()

	for _, coachID := range coachIDs {
		result, err := tx.Exec(`
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
	groupID int64,
	sessions []StudentGroupSession,
) error {
	if _, err := tx.Exec(`DELETE FROM student_group_sessions WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, session := range sessions {
		if _, err := tx.Exec(`
			INSERT INTO student_group_sessions (
				group_id, title, day_of_week, start_time, end_time, active, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, groupID, session.Title, session.DayOfWeek, session.StartTime, session.EndTime, boolToInt(session.Active), now, now); err != nil {
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

	result, err := tx.Exec(`
		INSERT INTO student_groups (name, code, description, training_program_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, group.Name, group.Code, group.Description, nullIfZero(group.TrainingProgramID), time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	groupID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, admissionID := range admissionIDs {
		if _, err := tx.Exec(`INSERT INTO student_group_members (group_id, admission_id) VALUES (?, ?)`, groupID, admissionID); err != nil {
			return err
		}
	}

	if err := replaceStudentGroupCoachesTx(tx, groupID, coachIDs); err != nil {
		return err
	}
	if err := replaceStudentGroupSessionsTx(tx, groupID, sessions); err != nil {
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

	sessionColumnExists, err := tableHasColumn(a.db, "attendance_records", "session_id")
	if err != nil {
		return err
	}

	if sessionColumnExists {
		if _, err := tx.Exec(`DELETE FROM attendance_records WHERE group_id = ? AND COALESCE(session_id, 0) = ? AND attendance_date = ?`, groupID, sessionID, attendanceDate); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM attendance_records WHERE group_id = ? AND attendance_date = ?`, groupID, attendanceDate); err != nil {
			return err
		}
	}

	for _, record := range records {
		if sessionColumnExists {
			if _, err := tx.Exec(`
				INSERT INTO attendance_records (
					group_id, session_id, admission_id, attendance_date, status, note, recorded_by_user_id, recorded_at, updated_at
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
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
		} else {
			if _, err := tx.Exec(`
				INSERT INTO attendance_records (
					group_id, admission_id, attendance_date, status, note, recorded_by_user_id, recorded_at, updated_at
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`,
				record.GroupID,
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

	result, err := tx.Exec(`
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

	layoutID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, item := range layout.Items {
		_, err := tx.Exec(`
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

func (a *App) createCourt(court Court) (int64, error) {
	now := time.Now().UTC()
	result, err := a.db.Exec(`
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
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (a *App) updateCourt(court Court) error {
	result, err := a.db.Exec(`
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
		if err := a.db.QueryRow(`
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

	result, err := tx.Exec(`
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

	if _, err := tx.Exec(`
		DELETE FROM court_layout_items
		WHERE layout_id = ?
	`, layout.ID); err != nil {
		return err
	}

	for _, item := range layout.Items {
		_, err := tx.Exec(`
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

	err := a.db.QueryRow(`
		SELECT active
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(&active)
	if err != nil {
		return err
	}

	if active {
		var activeLayoutCount int
		if err := a.db.QueryRow(`
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

	_, err = a.db.Exec(`
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

	if err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts
		WHERE active = 1
	`).Scan(&activeLayoutCount); err != nil {
		return err
	}

	var deletingActive bool

	if err := a.db.QueryRow(`
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

	result, err := a.db.Exec(`
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

	existing, err := querySchedulesForSlot(
		tx,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
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
	now := time.Now().UTC()

	result, err := tx.Exec(`
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
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		bookingStatusConfirmed,
		schedule.RequesterName,
		schedule.RequesterEmail,
		schedule.RequesterPhone,
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

	scheduleID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	if schedule.EntryType == "booking" {
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
		`,
			scheduleID,
			schedule.QuotedPrice,
			now,
			now,
		); err != nil {
			return err
		}
		if err := a.createBookingReferralTx(tx, scheduleID, schedule.ReferralCode, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) createBookingReferralTx(tx *sql.Tx, scheduleID int64, referralCode string, createdAt time.Time) error {
	referralCode = strings.ToUpper(strings.TrimSpace(referralCode))
	if referralCode == "" {
		return nil
	}

	var partnerID int64
	if err := tx.QueryRow(`
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
	if err := tx.QueryRow(`
		SELECT COALESCE(referral_commission_amount, 0)
		FROM pricing_settings
		WHERE id = 1
	`).Scan(&commissionAmount); err != nil {
		return err
	}
	if commissionAmount <= 0 {
		return errors.New("referral commission is not configured")
	}

	_, err := tx.Exec(`
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
	_, err := a.db.Exec(`
		INSERT INTO pricing_rules (
			activity, quantity, weekday_offpeak_price, weekday_peak_price,
			weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
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
	_, err := a.db.Exec(`
		INSERT INTO events (
			title, category, event_date, start_time, end_time, registration_deadline, venue, summary,
			image_path, cta_label, cta_link, published, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
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
	if activity := courtActivityFor(courtActivities, schedule.Activity); activity != nil && activity.AutoAccept {
		requestStatus = bookingStatusConfirmed
		statusSource = "system_auto_accept"
		changeSource = "system_auto_accept"
		actionType = "auto_confirmed"
	}

	result, err := tx.Exec(`
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

	requestID, err := result.LastInsertId()
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
	result, err := a.db.Exec(`
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

func syncAdmissionTrainingProgramsTx(tx *sql.Tx, admissionID int64, programIDs []int64, createdAt time.Time) error {
	if _, err := tx.Exec(`DELETE FROM admission_training_programs WHERE admission_id = ?`, admissionID); err != nil {
		return err
	}
	for _, programID := range programIDs {
		if _, err := tx.Exec(`
			INSERT INTO admission_training_programs (
				admission_id,
				training_program_id,
				created_at
			) VALUES (?, ?, ?)
		`, admissionID, programID, createdAt); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) createStudentEnrollmentWithOptionalPayment(enrollment StudentEnrollment, collectPayment bool, recordedByUserID int64) (int64, int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	result, err := tx.Exec(`
		INSERT INTO student_enrollments (
			admission_id,
			training_program_id,
			free_admission,
			free_monthly_fee,
			payment_collected,
			payment_collected_at,
			admission_payment_amount,
			finance_transaction_id,
			active,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, 0, NULL, 0, NULL, 1, ?, ?)
	`,
		enrollment.AdmissionID,
		enrollment.TrainingProgramID,
		boolToInt(enrollment.FreeAdmission),
		boolToInt(enrollment.FreeMonthlyFee),
		now,
		now,
	)
	if err != nil {
		return 0, 0, err
	}
	enrollmentID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	enrollment.ID = enrollmentID

	if _, err := tx.Exec(`
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
	`, enrollment.AdmissionID, enrollment.TrainingProgramID, now, enrollment.AdmissionID, enrollment.TrainingProgramID); err != nil {
		return 0, 0, err
	}

	var financeTransactionID int64
	if collectPayment && !enrollment.FreeAdmission {
		financeTransactionID, err = a.collectEnrollmentAdmissionPaymentTx(tx, enrollment, recordedByUserID)
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

	existing, err := findStudentEnrollmentByIDTx(tx, enrollment.ID)
	if err != nil {
		return err
	}

	var monthlyPaymentCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM student_monthly_payments
		WHERE enrollment_id = ?
		  AND COALESCE(voided, 0) = 0
	`, enrollment.ID).Scan(&monthlyPaymentCount); err != nil {
		return err
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

	result, err := tx.Exec(`
		UPDATE student_enrollments
		SET
			training_program_id = ?,
			free_admission = ?,
			free_monthly_fee = ?,
			updated_at = ?
		WHERE id = ?
	`, enrollment.TrainingProgramID, boolToInt(enrollment.FreeAdmission), boolToInt(enrollment.FreeMonthlyFee), time.Now().UTC(), enrollment.ID)
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

	if enrollment.TrainingProgramID != existing.TrainingProgramID {
		if _, err := tx.Exec(`
			DELETE FROM admission_training_programs
			WHERE admission_id = ?
			  AND training_program_id = ?
		`, existing.AdmissionID, existing.TrainingProgramID); err != nil {
			return err
		}
		if _, err := tx.Exec(`
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
		`, existing.AdmissionID, enrollment.TrainingProgramID, time.Now().UTC(), existing.AdmissionID, enrollment.TrainingProgramID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deleteStudentEnrollment(enrollmentID int64) (bool, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, enrollmentID)
	if err != nil {
		return false, err
	}

	var admissionPaymentCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM finance_transactions
		WHERE reference_type = 'student_enrollment'
		  AND reference_id = ?
	`, enrollmentID).Scan(&admissionPaymentCount); err != nil {
		return false, err
	}

	var monthlyPaymentCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM student_monthly_payments
		WHERE enrollment_id = ?
		  AND COALESCE(voided, 0) = 0
	`, enrollmentID).Scan(&monthlyPaymentCount); err != nil {
		return false, err
	}

	if admissionPaymentCount > 0 || monthlyPaymentCount > 0 {
		result, err := tx.Exec(`
			UPDATE student_enrollments
			SET active = 0,
			    updated_at = ?
			WHERE id = ?
		`, time.Now().UTC(), enrollmentID)
		if err != nil {
			return false, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if rowsAffected == 0 {
			return false, sql.ErrNoRows
		}
		return true, tx.Commit()
	}

	result, err := tx.Exec(`DELETE FROM student_enrollments WHERE id = ?`, enrollmentID)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		return false, sql.ErrNoRows
	}

	if _, err := tx.Exec(`
		DELETE FROM admission_training_programs
		WHERE admission_id = ?
		  AND training_program_id = ?
	`, enrollment.AdmissionID, enrollment.TrainingProgramID); err != nil {
		return false, err
	}

	return false, tx.Commit()
}

func (a *App) collectEnrollmentAdmissionPaymentTx(tx *sql.Tx, enrollment StudentEnrollment, recordedByUserID int64) (int64, error) {
	var studentName string
	if err := tx.QueryRow(`SELECT full_name FROM admissions WHERE id = ?`, enrollment.AdmissionID).Scan(&studentName); err != nil {
		return 0, err
	}
	var admissionFee float64
	if err := tx.QueryRow(`SELECT COALESCE(admission_fee, 0) FROM training_programs WHERE id = ?`, enrollment.TrainingProgramID).Scan(&admissionFee); err != nil {
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
	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("ENR-%s-%06d", now.Format("20060102150405"), enrollment.ID)
	account, err := findFinanceAccountForPaymentMethodTx(tx, "cash")
	if err != nil {
		return 0, err
	}
	description := fmt.Sprintf("Admission payment for %s - %s", studentName, enrollment.TrainingProgramName)
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
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
		PaymentMethod:    "cash",
		Amount:           admissionFee,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       now,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE student_enrollments
		SET payment_collected = 1,
		    payment_collected_at = ?,
		    admission_payment_amount = ?,
		    finance_transaction_id = ?,
		    updated_at = ?
		WHERE id = ?
	`, now, admissionFee, transactionID, now, enrollment.ID); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) createAdmissionWithOptionalPayment(
	admission Admission,
	collectPayment bool,
	recordedByUserID int64,
) (int64, int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	result, err := tx.Exec(`
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
	`,
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
	)
	if err != nil {
		return 0, 0, err
	}

	admissionID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}

	admission.ID = admissionID
	if err := syncAdmissionTrainingProgramsTx(tx, admissionID, admission.TrainingProgramIDs, now); err != nil {
		return 0, 0, err
	}

	var financeTransactionID int64

	if collectPayment && !admission.FreeAdmission {
		financeTransactionID, err = a.collectAdmissionPaymentTx(
			tx,
			admission,
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

	result, err := tx.Exec(`
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

	if _, err := tx.Exec(`
		UPDATE student_groups
		SET name = ?, code = ?, description = ?, training_program_id = ?, updated_at = ?
		WHERE id = ?
	`, group.Name, group.Code, group.Description, nullIfZero(group.TrainingProgramID), time.Now().UTC(), group.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM student_group_members WHERE group_id = ?`, group.ID); err != nil {
		return err
	}
	for _, admissionID := range admissionIDs {
		if _, err := tx.Exec(`INSERT INTO student_group_members (group_id, admission_id) VALUES (?, ?)`, group.ID, admissionID); err != nil {
			return err
		}
	}
	if err := replaceStudentGroupCoachesTx(tx, group.ID, coachIDs); err != nil {
		return err
	}
	if err := replaceStudentGroupSessionsTx(tx, group.ID, sessions); err != nil {
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
	_, err := a.db.Exec(`
		UPDATE pricing_rules
		SET activity = ?, quantity = ?, weekday_offpeak_price = ?, weekday_peak_price = ?,
		    weekend_offpeak_price = ?, weekend_peak_price = ?, updated_at = ?
		WHERE id = ?
	`,
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

func (a *App) updateEvent(event Event) error {
	_, err := a.db.Exec(`
		UPDATE events
		SET title = ?, category = ?, event_date = ?, start_time = ?, end_time = ?, registration_deadline = ?, venue = ?, summary = ?,
		    image_path = ?, cta_label = ?, cta_link = ?, published = ?, updated_at = ?
		WHERE id = ?
	`,
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
	_, err := a.db.Exec(`
		UPDATE pricing_settings
		SET peak_start_hour = ?, peak_end_hour = ?, updated_at = ?
		WHERE id = 1
	`, settings.PeakStartHour, settings.PeakEndHour, time.Now().UTC())
	return err
}

func (a *App) updateReferralCommissionAmount(amount float64) error {
	_, err := a.db.Exec(`
		UPDATE pricing_settings
		SET referral_commission_amount = ?, updated_at = ?
		WHERE id = 1
	`, amount, time.Now().UTC())
	return err
}

func (a *App) createReferralPartner(partner ReferralPartner) error {
	_, err := a.db.Exec(`
		INSERT INTO referral_partners (name, code, email, phone, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
	`, partner.Name, partner.Code, partner.Email, partner.Phone, time.Now().UTC(), time.Now().UTC())
	return err
}

func (a *App) updateReferralPartner(partner ReferralPartner) error {
	result, err := a.db.Exec(`
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
	result, err := a.db.Exec(`
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
	account, err := findFinanceAccountForPaymentMethodTx(tx, paymentMethod)
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
	if _, err := tx.Exec(`
		INSERT INTO receipt_sequences (scope, year, next_value)
		VALUES (?, ?, 2)
		ON CONFLICT(scope, year) DO UPDATE
		SET next_value = next_value + 1
	`, scope, year); err != nil {
		return "", err
	}
	var nextValue int
	if err := tx.QueryRow(`SELECT next_value - 1 FROM receipt_sequences WHERE scope = ? AND year = ?`, scope, year).Scan(&nextValue); err != nil {
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
	switch status {
	case bookingStatusConfirmed, bookingStatusCompleted, bookingStatusNoShow, bookingStatusCancelled:
		return true
	default:
		return false
	}
}

func (a *App) syncBookingFinancialSnapshotTx(tx *sql.Tx, scheduleID int64) error {
	financials, err := listBookingFinancialsForScheduleIDsQuery(tx, []int64{scheduleID})
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
		paymentMethod = "cash"
		collections, err := listBookingPaymentCollectionsForScheduleIDsQuery(tx, []int64{scheduleID})
		if err != nil {
			return err
		}
		for _, collection := range collections {
			if !collection.Voided {
				financeTransactionID = collection.FinanceTransactionID
				break
			}
		}
	}
	paidFlag := 0
	if financial.ActivePaymentCount > 0 {
		paidFlag = 1
	}
	_, err = tx.Exec(`
		UPDATE booking_financials
		SET paid = ?, paid_at = ?, payment_method = COALESCE(?, ''), finance_transaction_id = ?, updated_at = ?
		WHERE schedule_id = ?
	`, paidFlag, paidAt, paymentMethod, financeTransactionID, time.Now().UTC(), scheduleID)
	return err
}

func nullableExistingUserIDTx(tx *sql.Tx, userID int64) any {
	if userID <= 0 {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return nil
	}
	return userID
}

func (a *App) collectBookingPayment(scheduleID int64, paymentMethod string, amount float64, paymentNote string, recordedByUserID int64, allowOverpayment bool) (int64, error) {
	if paymentMethod != "cash" {
		return 0, errors.New("booking payments must be recorded in cash")
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
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var financial BookingFinancial
	var paid int
	if err := tx.QueryRow(`
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
	financials, err := listBookingFinancialsForScheduleIDsQuery(tx, []int64{scheduleID})
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
	if amount > outstanding+0.005 && !allowOverpayment {
		return 0, ErrBookingPaymentNeedsOverpayApproval
	}
	_ = paid
	personName := financial.RequesterName
	if personName == "" {
		personName = "Booking customer"
	}
	now := time.Now().UTC()
	recordedByRef := nullableExistingUserIDTx(tx, recordedByUserID)
	receiptNumber, err := a.nextReceiptNumberTx(tx, "booking_payment", now)
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, paymentMethod)
	if err != nil {
		return 0, err
	}
	description := fmt.Sprintf("%s cash collection for %s", bookingProductLabel(financial.Activity, financial.Quantity), bookingReference(scheduleID))
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
		Notes:            strings.TrimSpace(paymentNote),
		PaymentMethod:    paymentMethod,
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       now,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
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
	`, scheduleID, transactionID, amount, paymentMethod, strings.TrimSpace(paymentNote), recordedByRef, now, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'booking_payment_collection',
		    source_id = (
				SELECT id FROM booking_payment_collections
				WHERE finance_transaction_id = ?
				LIMIT 1
			),
		    updated_at = ?
		WHERE id = ?
	`, transactionID, now, transactionID); err != nil {
		return 0, err
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
	if err := tx.QueryRow(`
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
	if _, err := tx.Exec(`
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

	schedule, err := findSpaceScheduleByIDQuery(tx, scheduleID)
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
		existing, err := querySchedulesForSlot(tx, schedule.SlotDate, schedule.SlotHour, schedule.ID)
		if err != nil {
			return nil, 0, err
		}
		if err := validateSpaceScheduleSlotAgainstLayouts(existing, *schedule, courtLayouts); err != nil {
			return nil, 0, err
		}
	}

	now := time.Now().UTC()
	result, err := tx.Exec(`
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

	financial := bookingFinancialForSchedule(mustListBookingFinancialsTx(tx, scheduleID), scheduleID)
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

	current, err := findSpaceScheduleByIDQuery(tx, scheduleID)
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
	financialErr := tx.QueryRow(`
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
	result, err := tx.Exec(`
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

	if _, err := tx.Exec(`
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
		changeResult, err := tx.Exec(`
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
		changeID, err = changeResult.LastInsertId()
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
	if err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM student_monthly_payments
		WHERE admission_id = ?
		  AND COALESCE(voided, 0) = 0
	`, admissionID).Scan(&monthlyPaymentCount); err != nil {
		return err
	}
	if monthlyPaymentCount > 0 {
		return ErrAdmissionHasMonthlyPaymentHistory
	}
	result, err := a.db.Exec(`DELETE FROM admissions WHERE id = ?`, admissionID)
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
	_, err := a.db.Exec(`DELETE FROM student_groups WHERE id = ?`, groupID)
	return err
}

func (a *App) deleteSpaceSchedule(scheduleID int64) error {
	var activeCollections int
	if err := a.db.QueryRow(`
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
	result, err := a.db.Exec(`DELETE FROM space_schedules WHERE id = ?`, scheduleID)
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
	_, err := a.db.Exec(`DELETE FROM pricing_rules WHERE id = ?`, pricingID)
	return err
}

func (a *App) deleteEvent(eventID int64) error {
	_, err := a.db.Exec(`DELETE FROM events WHERE id = ?`, eventID)
	return err
}
