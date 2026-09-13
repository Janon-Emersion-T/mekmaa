package main

import (
	"database/sql"
	"strings"
)

type enrollmentProgrammeLockedError struct{ reason string }

func (e *enrollmentProgrammeLockedError) Error() string { return e.reason }

// Use the same checks for the edit form and the transactional write. Keep
// voided payments and historical memberships: they still describe this programme.
func (a *App) enrollmentProgrammeLockReason(tx *sql.Tx, enrollment *StudentEnrollment) (string, error) {
	if !enrollment.Active {
		return "Archived enrollments cannot be edited.", nil
	}
	query := `SELECT
		EXISTS(SELECT 1 FROM finance_transactions WHERE id = ? OR (reference_type = 'student_enrollment' AND reference_id = ?)),
		EXISTS(SELECT 1 FROM student_monthly_payments WHERE enrollment_id = ?),
		EXISTS(SELECT 1 FROM student_group_members m JOIN student_groups g ON g.id = m.group_id WHERE m.admission_id = ? AND g.training_program_id = ?),
		EXISTS(SELECT 1 FROM student_group_membership_history m JOIN student_groups g ON g.id = m.group_id WHERE m.admission_id = ? AND g.training_program_id = ?),
		EXISTS(SELECT 1 FROM attendance_records ar JOIN student_groups g ON g.id = ar.group_id WHERE ar.admission_id = ? AND g.training_program_id = ?),
		EXISTS(SELECT 1 FROM student_enrollment_leaves WHERE enrollment_id = ?)`
	args := []any{enrollment.FinanceTransactionID, enrollment.ID, enrollment.ID,
		enrollment.AdmissionID, enrollment.TrainingProgramID, enrollment.AdmissionID, enrollment.TrainingProgramID,
		enrollment.AdmissionID, enrollment.TrainingProgramID, enrollment.ID}
	var finance, monthly, groups, history, attendance, leaves bool
	var row *sql.Row
	if tx == nil {
		row = a.queryRowDB(query, args...)
	} else {
		row = a.queryRowTxDB(tx, query, args...)
	}
	if err := row.Scan(&finance, &monthly, &groups, &history, &attendance, &leaves); err != nil {
		return "", err
	}
	var reasons []string
	if finance || enrollment.AdmissionPaymentPaid || enrollment.FinanceTransactionID > 0 {
		reasons = append(reasons, "admission payment history")
	}
	if monthly {
		reasons = append(reasons, "monthly payment history")
	}
	if groups || history {
		reasons = append(reasons, "group assignments")
	}
	if attendance {
		reasons = append(reasons, "attendance history")
	}
	if leaves {
		reasons = append(reasons, "leave history")
	}
	if len(reasons) == 0 {
		return "", nil
	}
	return "Programme is locked because this enrollment has " + strings.Join(reasons, ", ") + ". Use Transfer programme to move the student.", nil
}

func programmeLockError(reason string) error {
	if reason == "" {
		return nil
	}
	return &enrollmentProgrammeLockedError{reason: reason}
}
