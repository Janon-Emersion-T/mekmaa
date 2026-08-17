package main

import (
	"context"
	"strings"
)

func financeTransactionsBaseQuery(filter FinanceFilter) (string, []any) {
	query := `
		SELECT ft.id,
		       ft.receipt_number,
		       COALESCE(ft.reference_number, ft.receipt_number),
		       COALESCE(ft.division_id, 0),
		       COALESCE(fd.code, ''),
		       COALESCE(fd.name, ''),
		       ft.category,
		       COALESCE(ft.approval_status, 'approved'),
		       COALESCE(ft.transaction_type, CASE WHEN ft.amount < 0 THEN 'expense' ELSE 'income' END),
		       ft.reference_type,
		       COALESCE(ft.reference_id, 0),
		       COALESCE(ft.source_type, ''),
		       COALESCE(ft.source_id, 0),
		       COALESCE(ft.finance_account_id, 0),
		       COALESCE(fa.account_code, ''),
		       COALESCE(fa.name, ''),
		       COALESCE(fa.account_type, ''),
		       COALESCE(ft.transfer_group_id, ''),
		       COALESCE(CASE
		       	WHEN ft.reference_type = 'admission' THEN adm.full_name
		       	WHEN ft.reference_type = 'student_enrollment' THEN sea.full_name
		       	WHEN ft.source_type = 'student_monthly_payment' THEN COALESCE(smp_adm.full_name, sea.full_name, adm.full_name)
		       	ELSE ''
		       END, ''),
		       COALESCE(CASE
		       	WHEN ft.reference_type = 'student_enrollment' THEN tp.name
		       	WHEN ft.reference_type = 'admission' THEN adm_tp.name
		       	WHEN ft.source_type = 'student_monthly_payment' THEN COALESCE(smp_tp.name, tp.name, adm_tp.name)
		       	ELSE ''
		       END, ''),
		       COALESCE(ss.activity, ''),
		       COALESCE(o2oo.id, 0),
		       COALESCE(o2oo.name, ''),
		       ft.person_name,
		       ft.description,
		       COALESCE(ft.notes, ''),
		       ft.payment_method,
		       ft.amount,
		       COALESCE(ft.recorded_by_user_id, 0),
		       COALESCE(u.name, ''),
		       COALESCE(ft.approved_by_user_id, 0),
		       COALESCE(au.name, ''),
		       ft.recorded_at,
		       ft.created_at,
		       COALESCE(CAST(ft.updated_at AS TEXT), CAST(ft.created_at AS TEXT), ''),
		       ft.approved_at,
		       ft.voided_at,
		       COALESCE(ft.voided_by_user_id, 0),
		       COALESCE(ft.void_reason, '')
		FROM finance_transactions ft
		LEFT JOIN divisions fd ON fd.id = ft.division_id
		LEFT JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		LEFT JOIN users u ON u.id = ft.recorded_by_user_id
		LEFT JOIN users au ON au.id = ft.approved_by_user_id
		LEFT JOIN space_schedules ss ON ft.reference_type = 'space_schedule' AND ss.id = ft.reference_id
		LEFT JOIN one_to_one_bookings o2ob ON o2ob.schedule_id = ss.id
		LEFT JOIN one_to_one_offerings o2oo ON o2oo.id = o2ob.offering_id
		LEFT JOIN student_enrollments se ON ft.reference_type = 'student_enrollment' AND se.id = ft.reference_id
		LEFT JOIN admissions sea ON sea.id = se.admission_id
		LEFT JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN admissions adm ON ft.reference_type = 'admission' AND adm.id = ft.reference_id
		LEFT JOIN training_programs adm_tp ON adm_tp.id = adm.training_program_id
		LEFT JOIN student_monthly_payments smp ON ft.source_type = 'student_monthly_payment' AND smp.id = ft.source_id
		LEFT JOIN student_enrollments smp_se ON smp_se.id = smp.enrollment_id
		LEFT JOIN training_programs smp_tp ON smp_tp.id = smp_se.training_program_id
		LEFT JOIN admissions smp_adm ON smp_adm.id = smp.admission_id
		WHERE 1 = 1`
	args := make([]any, 0, 24)
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
	if len(filter.Categories) > 0 {
		query += ` AND ` + financeStringInClause("ft.category", len(filter.Categories))
		for _, value := range filter.Categories {
			args = append(args, value)
		}
	} else if filter.Category != "" {
		query += ` AND ft.category = ?`
		args = append(args, filter.Category)
	}
	if len(filter.AccountIDs) > 0 {
		query += ` AND ` + financeInt64InClause("ft.finance_account_id", len(filter.AccountIDs))
		for _, value := range filter.AccountIDs {
			args = append(args, value)
		}
	} else if filter.AccountID > 0 {
		query += ` AND ft.finance_account_id = ?`
		args = append(args, filter.AccountID)
	}
	if len(filter.TransactionTypes) > 0 {
		query += ` AND ` + financeStringInClause("ft.transaction_type", len(filter.TransactionTypes))
		for _, value := range filter.TransactionTypes {
			args = append(args, value)
		}
	} else if filter.TransactionType != "" {
		query += ` AND ft.transaction_type = ?`
		args = append(args, filter.TransactionType)
	}
	if len(filter.SourceTypes) > 0 {
		query += ` AND ` + financeStringInClause("ft.source_type", len(filter.SourceTypes))
		for _, value := range filter.SourceTypes {
			args = append(args, value)
		}
	} else if filter.SourceType != "" {
		query += ` AND ft.source_type = ?`
		args = append(args, filter.SourceType)
	}
	if len(filter.ReferenceTypes) > 0 {
		query += ` AND ` + financeStringInClause("ft.reference_type", len(filter.ReferenceTypes))
		for _, value := range filter.ReferenceTypes {
			args = append(args, value)
		}
	} else if filter.ReferenceType != "" {
		query += ` AND ft.reference_type = ?`
		args = append(args, filter.ReferenceType)
	}
	if len(filter.PaymentMethods) > 0 {
		query += ` AND ` + financeStringInClause("ft.payment_method", len(filter.PaymentMethods))
		for _, value := range filter.PaymentMethods {
			args = append(args, value)
		}
	} else if filter.PaymentMethod != "" {
		query += ` AND ft.payment_method = ?`
		args = append(args, filter.PaymentMethod)
	}
	if len(filter.ApprovalStatuses) > 0 {
		query += ` AND ` + financeStringInClause("COALESCE(ft.approval_status, 'approved')", len(filter.ApprovalStatuses))
		for _, value := range filter.ApprovalStatuses {
			args = append(args, value)
		}
	}
	if len(filter.TrainingProgramIDs) > 0 {
		query += ` AND ` + financeInt64InClause("COALESCE(smp_se.training_program_id, se.training_program_id, adm.training_program_id, 0)", len(filter.TrainingProgramIDs))
		for _, value := range filter.TrainingProgramIDs {
			args = append(args, value)
		}
	}
	if len(filter.DivisionIDs) > 0 {
		query += ` AND ` + financeInt64InClause("COALESCE(ft.division_id, 0)", len(filter.DivisionIDs))
		for _, value := range filter.DivisionIDs {
			args = append(args, value)
		}
	} else if filter.DivisionID > 0 {
		query += ` AND COALESCE(ft.division_id, 0) = ?`
		args = append(args, filter.DivisionID)
	}
	if len(filter.BookingActivities) > 0 {
		query += ` AND ` + financeStringInClause("LOWER(COALESCE(ss.activity, ''))", len(filter.BookingActivities))
		for _, value := range filter.BookingActivities {
			args = append(args, strings.ToLower(value))
		}
	}
	if len(filter.OneToOneOfferingIDs) > 0 {
		query += ` AND ` + financeInt64InClause("COALESCE(o2oo.id, 0)", len(filter.OneToOneOfferingIDs))
		for _, value := range filter.OneToOneOfferingIDs {
			args = append(args, value)
		}
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
			OR LOWER(COALESCE(fa.account_code, '')) LIKE ?
			OR LOWER(COALESCE(u.name, '')) LIKE ?
			OR LOWER(COALESCE(au.name, '')) LIKE ?
			OR LOWER(COALESCE(ft.reference_type, '')) LIKE ?
			OR LOWER(COALESCE(ss.title, '')) LIKE ?
			OR LOWER(COALESCE(ss.activity, '')) LIKE ?
			OR LOWER(COALESCE(o2oo.name, '')) LIKE ?
			OR LOWER(COALESCE(sea.full_name, '')) LIKE ?
			OR LOWER(COALESCE(adm.full_name, '')) LIKE ?
			OR LOWER(COALESCE(smp_adm.full_name, '')) LIKE ?
			OR LOWER(COALESCE(tp.name, '')) LIKE ?
			OR LOWER(COALESCE(adm_tp.name, '')) LIKE ?
			OR LOWER(COALESCE(smp_tp.name, '')) LIKE ?
		)`
		term := "%" + strings.ToLower(filter.Search) + "%"
		args = append(args, term, term, term, term, term, term, term, term, term, term, term, term, term, term, term, term, term, term, term)
	}
	return query, args
}

func financeStringInClause(column string, size int) string {
	placeholders := make([]string, size)
	for i := range placeholders {
		placeholders[i] = "?"
	}
	return column + " IN (" + strings.Join(placeholders, ", ") + ")"
}

func financeInt64InClause(column string, size int) string {
	return financeStringInClause(column, size)
}

func (a *App) listFinanceBookingActivities() ([]CourtActivity, error) {
	rows, err := a.db.Query(`
		SELECT activity, MAX(display_name)
		FROM (
			SELECT activity, COALESCE(NULLIF(display_name, ''), '') AS display_name
			FROM court_activities
			WHERE COALESCE(active, 1) = 1
			UNION ALL
			SELECT activity, ''
			FROM space_schedules
			WHERE entry_type = 'booking'
		)
		WHERE TRIM(COALESCE(activity, '')) <> ''
		GROUP BY activity
		ORDER BY activity ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var activities []CourtActivity
	for rows.Next() {
		var item CourtActivity
		if err := rows.Scan(&item.Activity, &item.DisplayName); err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.DisplayName) == "" {
			item.DisplayName = activityLabel(item.Activity)
		}
		activities = append(activities, item)
	}
	return activities, rows.Err()
}

func (a *App) countFinanceTransactions(ctx context.Context, filter FinanceFilter) (int, error) {
	query, args := financeTransactionsBaseQuery(filter)
	var count int
	if err := a.queryRowContextDB(ctx, `SELECT COUNT(*) FROM (`+query+`) AS finance_transaction_count`, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
