package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	PayrollRunStatusDraft      = "draft"
	PayrollRunStatusCalculated = "calculated"
	PayrollRunStatusApproved   = "approved"
	PayrollRunStatusClosed     = "closed"
)

const (
	PayrollPaymentStatusDraft      = "draft"
	PayrollPaymentStatusCalculated = "calculated"
	PayrollPaymentStatusApproved   = "approved"
	PayrollPaymentStatusPaid       = "paid"
	PayrollPaymentStatusVoid       = "void"
)

const (
	PayrollAdjustmentIncentive     = "incentive"
	PayrollAdjustmentBonus         = "bonus"
	PayrollAdjustmentAllowance     = "allowance"
	PayrollAdjustmentOvertime      = "overtime"
	PayrollAdjustmentDeduction     = "deduction"
	PayrollAdjustmentSalaryAdvance = "salary_advance"
	PayrollAdjustmentCorrection    = "correction"
	PayrollAdjustmentOther         = "other"
)

const (
	PayrollDirectionAddition  = "addition"
	PayrollDirectionDeduction = "deduction"
)

type PayrollRun struct {
	ID int64

	PeriodStart string
	PeriodEnd   string
	Label       string
	Status      string

	CreatedByUserID  int64
	ApprovedByUserID int64

	ApprovedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time

	Payments []PayrollPayment

	StaffCount      int
	BaseTotal       float64
	AdditionsTotal  float64
	DeductionsTotal float64
	NetTotal        float64
}

type PayrollPayment struct {
	ID int64

	PayrollRunID int64
	UserID       int64

	UserName  string
	UserEmail string

	SalaryProfileID int64

	DivisionID   int64
	DivisionName string

	TrainingProgramID   int64
	TrainingProgramName string

	CompensationType string

	RateSnapshot  float64
	Quantity      float64
	QuantityLabel string

	BaseAmount      float64
	AdditionsTotal  float64
	DeductionsTotal float64
	NetAmount       float64

	Status string

	PaymentMethod    string
	PaymentReference string

	PaidAt       time.Time
	PaidByUserID int64

	FinanceTransactionID int64

	Notes string

	CreatedAt time.Time
	UpdatedAt time.Time

	Adjustments []PayrollAdjustment
}

type PayrollAdjustment struct {
	ID int64

	PayrollPaymentID int64

	AdjustmentType string
	Direction      string
	Description    string
	Amount         float64

	CreatedByUserID int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

func payrollAdjustmentTypeLabel(value string) string {
	switch strings.TrimSpace(value) {
	case PayrollAdjustmentIncentive:
		return "Incentive"
	case PayrollAdjustmentBonus:
		return "Bonus"
	case PayrollAdjustmentAllowance:
		return "Allowance"
	case PayrollAdjustmentOvertime:
		return "Overtime"
	case PayrollAdjustmentDeduction:
		return "Deduction"
	case PayrollAdjustmentSalaryAdvance:
		return "Salary advance"
	case PayrollAdjustmentCorrection:
		return "Correction"
	case PayrollAdjustmentOther:
		return "Other"
	default:
		return strings.TrimSpace(value)
	}
}

func validPayrollAdjustmentType(value string) bool {
	switch strings.TrimSpace(value) {
	case PayrollAdjustmentIncentive,
		PayrollAdjustmentBonus,
		PayrollAdjustmentAllowance,
		PayrollAdjustmentOvertime,
		PayrollAdjustmentDeduction,
		PayrollAdjustmentSalaryAdvance,
		PayrollAdjustmentCorrection,
		PayrollAdjustmentOther:
		return true
	default:
		return false
	}
}

func validPayrollDirection(value string) bool {
	switch strings.TrimSpace(value) {
	case PayrollDirectionAddition,
		PayrollDirectionDeduction:
		return true
	default:
		return false
	}
}

func (a *App) createPayrollRun(
	periodStart,
	periodEnd,
	label string,
	actorUserID int64,
) (int64, error) {
	periodStart = strings.TrimSpace(periodStart)
	periodEnd = strings.TrimSpace(periodEnd)
	label = strings.TrimSpace(label)

	start, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		return 0, errors.New("invalid payroll start date")
	}

	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return 0, errors.New("invalid payroll end date")
	}

	if end.Before(start) {
		return 0, errors.New(
			"payroll end date cannot be before start date",
		)
	}

	if label == "" {
		label = fmt.Sprintf(
			"%s to %s",
			periodStart,
			periodEnd,
		)
	}

	now := time.Now().UTC()

	if a.runtimeConfig.DBDriver == databaseDriverPostgres {
		var runID int64

		err := a.queryRowDB(
			`
			INSERT INTO payroll_runs (
				period_start,
				period_end,
				label,
				status,
				created_by_user_id,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			RETURNING id
			`,
			periodStart,
			periodEnd,
			label,
			PayrollRunStatusDraft,
			nullIfZero(actorUserID),
			now,
			now,
		).Scan(&runID)

		return runID, err
	}

	payrollRunID, err := a.insertAndReturnID(
		`
		INSERT INTO payroll_runs (
			period_start,
			period_end,
			label,
			status,
			created_by_user_id,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
		periodStart,
		periodEnd,
		label,
		PayrollRunStatusDraft,
		nullIfZero(actorUserID),
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	return payrollRunID, nil
}

func (a *App) listPayrollRuns() ([]PayrollRun, error) {
	rows, err := a.queryDB(`
		SELECT
			pr.id,
			CAST(pr.period_start AS TEXT),
			CAST(pr.period_end AS TEXT),
			COALESCE(pr.label, ''),
			pr.status,
			COALESCE(pr.created_by_user_id, 0),
			COALESCE(pr.approved_by_user_id, 0),
			pr.approved_at,
			pr.created_at,
			pr.updated_at,
			COUNT(pp.id),
			COALESCE(SUM(pp.base_amount), 0),
			COALESCE(SUM(pp.additions_total), 0),
			COALESCE(SUM(pp.deductions_total), 0),
			COALESCE(SUM(pp.net_amount), 0)
		FROM payroll_runs pr
		LEFT JOIN payroll_payments pp
			ON pp.payroll_run_id = pr.id
			AND pp.status <> 'void'
		GROUP BY
			pr.id,
			pr.period_start,
			pr.period_end,
			pr.label,
			pr.status,
			pr.created_by_user_id,
			pr.approved_by_user_id,
			pr.approved_at,
			pr.created_at,
			pr.updated_at
		ORDER BY pr.period_start DESC, pr.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]PayrollRun, 0)

	for rows.Next() {
		var run PayrollRun
		var approvedAt sql.NullTime

		if err := rows.Scan(
			&run.ID,
			&run.PeriodStart,
			&run.PeriodEnd,
			&run.Label,
			&run.Status,
			&run.CreatedByUserID,
			&run.ApprovedByUserID,
			&approvedAt,
			&run.CreatedAt,
			&run.UpdatedAt,
			&run.StaffCount,
			&run.BaseTotal,
			&run.AdditionsTotal,
			&run.DeductionsTotal,
			&run.NetTotal,
		); err != nil {
			return nil, err
		}

		if approvedAt.Valid {
			run.ApprovedAt = approvedAt.Time
		}

		runs = append(runs, run)
	}

	return runs, rows.Err()
}

func (a *App) addPayrollAdjustment(
	paymentID int64,
	adjustmentType,
	direction,
	description string,
	amount float64,
	actorUserID int64,
) error {
	if paymentID <= 0 {
		return errors.New("salary payment is required")
	}

	adjustmentType =
		strings.TrimSpace(adjustmentType)

	direction =
		strings.TrimSpace(direction)

	description =
		strings.TrimSpace(description)

	if !validPayrollAdjustmentType(adjustmentType) {
		return errors.New("invalid payroll adjustment type")
	}

	if !validPayrollDirection(direction) {
		return errors.New("invalid payroll adjustment direction")
	}

	if amount <= 0 {
		return errors.New(
			"adjustment amount must be greater than zero",
		)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var paymentStatus string

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT status
		FROM payroll_payments
		WHERE id = ?
		`,
		paymentID,
	).Scan(&paymentStatus); err != nil {
		return err
	}

	if paymentStatus == PayrollPaymentStatusApproved ||
		paymentStatus == PayrollPaymentStatusPaid ||
		paymentStatus == PayrollPaymentStatusVoid {
		return errors.New(
			"adjustments cannot be changed after salary approval",
		)
	}

	now := time.Now().UTC()

	if _, err := a.execTxDB(
		tx,
		`
		INSERT INTO payroll_adjustments (
			payroll_payment_id,
			adjustment_type,
			direction,
			description,
			amount,
			created_by_user_id,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
		paymentID,
		adjustmentType,
		direction,
		description,
		amount,
		nullIfZero(actorUserID),
		now,
		now,
	); err != nil {
		return err
	}

	if err := recalculatePayrollPaymentTx(
		a,
		tx,
		paymentID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func recalculatePayrollPaymentTx(
	a *App,
	tx *sql.Tx,
	paymentID int64,
) error {
	var baseAmount float64
	var additions float64
	var deductions float64

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			base_amount,
			COALESCE((
				SELECT SUM(amount)
				FROM payroll_adjustments
				WHERE payroll_payment_id = payroll_payments.id
				  AND direction = 'addition'
			), 0),
			COALESCE((
				SELECT SUM(amount)
				FROM payroll_adjustments
				WHERE payroll_payment_id = payroll_payments.id
				  AND direction = 'deduction'
			), 0)
		FROM payroll_payments
		WHERE id = ?
		`,
		paymentID,
	).Scan(
		&baseAmount,
		&additions,
		&deductions,
	); err != nil {
		return err
	}

	netAmount :=
		baseAmount + additions - deductions

	if netAmount < 0 {
		return errors.New(
			"deductions cannot exceed salary and additions",
		)
	}

	_, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_payments
		SET
			additions_total = ?,
			deductions_total = ?,
			net_amount = ?,
			updated_at = ?
		WHERE id = ?
		`,
		additions,
		deductions,
		netAmount,
		time.Now().UTC(),
		paymentID,
	)

	return err
}

func (a *App) findPayrollRunByID(
	runID int64,
) (*PayrollRun, error) {
	if runID <= 0 {
		return nil, sql.ErrNoRows
	}

	var run PayrollRun
	var approvedAt sql.NullTime

	err := a.queryRowDB(
		`
		SELECT
			pr.id,
			CAST(pr.period_start AS TEXT),
			CAST(pr.period_end AS TEXT),
			COALESCE(pr.label, ''),
			pr.status,
			COALESCE(pr.created_by_user_id, 0),
			COALESCE(pr.approved_by_user_id, 0),
			pr.approved_at,
			pr.created_at,
			pr.updated_at
		FROM payroll_runs pr
		WHERE pr.id = ?
		`,
		runID,
	).Scan(
		&run.ID,
		&run.PeriodStart,
		&run.PeriodEnd,
		&run.Label,
		&run.Status,
		&run.CreatedByUserID,
		&run.ApprovedByUserID,
		&approvedAt,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if approvedAt.Valid {
		run.ApprovedAt = approvedAt.Time
	}

	payments, err :=
		a.listPayrollPaymentsForRun(runID)
	if err != nil {
		return nil, err
	}

	run.Payments = payments
	run.StaffCount = len(payments)

	for _, payment := range payments {
		if payment.Status == PayrollPaymentStatusVoid {
			continue
		}

		run.BaseTotal += payment.BaseAmount
		run.AdditionsTotal += payment.AdditionsTotal
		run.DeductionsTotal += payment.DeductionsTotal
		run.NetTotal += payment.NetAmount
	}

	return &run, nil
}

func (a *App) listPayrollPaymentsForRun(
	runID int64,
) ([]PayrollPayment, error) {
	rows, err := a.queryDB(
		`
		SELECT
			pp.id,
			pp.payroll_run_id,
			pp.user_id,
			COALESCE(u.name, ''),
			COALESCE(u.email, ''),
			COALESCE(pp.salary_profile_id, 0),
			COALESCE(pp.division_id, 0),
			COALESCE(d.name, ''),
			COALESCE(pp.training_program_id, 0),
			COALESCE(tp.name, ''),
			pp.compensation_type,
			COALESCE(pp.rate_snapshot, 0),
			COALESCE(pp.quantity, 0),
			COALESCE(pp.quantity_label, ''),
			COALESCE(pp.base_amount, 0),
			COALESCE(pp.additions_total, 0),
			COALESCE(pp.deductions_total, 0),
			COALESCE(pp.net_amount, 0),
			pp.status,
			COALESCE(pp.payment_method, ''),
			COALESCE(pp.payment_reference, ''),
			pp.paid_at,
			COALESCE(pp.paid_by_user_id, 0),
			COALESCE(pp.finance_transaction_id, 0),
			COALESCE(pp.notes, ''),
			pp.created_at,
			pp.updated_at
		FROM payroll_payments pp
		JOIN users u
			ON u.id = pp.user_id
		LEFT JOIN divisions d
			ON d.id = pp.division_id
		LEFT JOIN training_programs tp
			ON tp.id = pp.training_program_id
		WHERE pp.payroll_run_id = ?
		ORDER BY
			LOWER(COALESCE(u.name, '')) ASC,
			pp.id ASC
		`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := make([]PayrollPayment, 0)

	for rows.Next() {
		var payment PayrollPayment
		var paidAt sql.NullTime

		if err := rows.Scan(
			&payment.ID,
			&payment.PayrollRunID,
			&payment.UserID,
			&payment.UserName,
			&payment.UserEmail,
			&payment.SalaryProfileID,
			&payment.DivisionID,
			&payment.DivisionName,
			&payment.TrainingProgramID,
			&payment.TrainingProgramName,
			&payment.CompensationType,
			&payment.RateSnapshot,
			&payment.Quantity,
			&payment.QuantityLabel,
			&payment.BaseAmount,
			&payment.AdditionsTotal,
			&payment.DeductionsTotal,
			&payment.NetAmount,
			&payment.Status,
			&payment.PaymentMethod,
			&payment.PaymentReference,
			&paidAt,
			&payment.PaidByUserID,
			&payment.FinanceTransactionID,
			&payment.Notes,
			&payment.CreatedAt,
			&payment.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if paidAt.Valid {
			payment.PaidAt = paidAt.Time
		}

		adjustments, err :=
			a.listPayrollAdjustments(payment.ID)
		if err != nil {
			return nil, err
		}

		payment.Adjustments = adjustments

		payments = append(payments, payment)
	}

	return payments, rows.Err()
}

func (a *App) listPayrollAdjustments(
	paymentID int64,
) ([]PayrollAdjustment, error) {
	rows, err := a.queryDB(
		`
		SELECT
			id,
			payroll_payment_id,
			adjustment_type,
			direction,
			COALESCE(description, ''),
			amount,
			COALESCE(created_by_user_id, 0),
			created_at,
			updated_at
		FROM payroll_adjustments
		WHERE payroll_payment_id = ?
		ORDER BY id ASC
		`,
		paymentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	adjustments := make([]PayrollAdjustment, 0)

	for rows.Next() {
		var adjustment PayrollAdjustment

		if err := rows.Scan(
			&adjustment.ID,
			&adjustment.PayrollPaymentID,
			&adjustment.AdjustmentType,
			&adjustment.Direction,
			&adjustment.Description,
			&adjustment.Amount,
			&adjustment.CreatedByUserID,
			&adjustment.CreatedAt,
			&adjustment.UpdatedAt,
		); err != nil {
			return nil, err
		}

		adjustments = append(
			adjustments,
			adjustment,
		)
	}

	return adjustments, rows.Err()
}

// calculatePayrollProfileQuantity determines the payroll quantity for one
// salary profile within a payroll period.
//
// Monthly:
//
//	one salary unit per payroll run.
//
// Weekly:
//
//	number of calendar weeks represented by the payroll period.
//	Partial weeks are prorated by days / 7.
//
// Hourly:
//
//	intentionally returns zero for now. Scheduled group hours are not proof
//	that the staff member actually worked those hours. Actual payable hours
//	will be entered/confirmed during payroll processing until staff
//	session-level attendance is available.
//
// Per student:
//
//	calculated from the configured student basis.
func (a *App) calculatePayrollProfileQuantity(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (float64, string, error) {
	start, err := time.Parse("2006-01-02", strings.TrimSpace(periodStart))
	if err != nil {
		return 0, "", errors.New("invalid payroll period start")
	}

	end, err := time.Parse("2006-01-02", strings.TrimSpace(periodEnd))
	if err != nil {
		return 0, "", errors.New("invalid payroll period end")
	}

	if end.Before(start) {
		return 0, "", errors.New("payroll period end cannot be before start")
	}

	switch normalizeSalaryType(profile.CompensationType) {
	case SalaryTypeMonthly:
		return 1, "month", nil

	case SalaryTypeWeekly:
		days := int(end.Sub(start).Hours()/24) + 1
		return float64(days) / 7.0, "weeks", nil

	case SalaryTypeHourly:
		return 0, "hours - manual entry required", nil

	case SalaryTypePerStudent:
		return a.calculatePerStudentPayrollQuantity(
			profile,
			periodStart,
			periodEnd,
		)

	default:
		return 0, "", errors.New("unsupported salary type")
	}
}

func (a *App) calculatePerStudentPayrollQuantity(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (float64, string, error) {
	switch normalizeSalaryStudentBasis(profile.StudentBasis) {

	case SalaryStudentBasisActiveEnrollment:
		count, err := a.countPayrollActiveEnrollments(profile)
		if err != nil {
			return 0, "", err
		}

		return float64(count), "active students", nil

	case SalaryStudentBasisGroupMembership:
		count, err := a.countPayrollGroupMembers(profile)
		if err != nil {
			return 0, "", err
		}

		return float64(count), "group students", nil

	case SalaryStudentBasisAttendance:
		count, err := a.countPayrollAttendingStudents(
			profile,
			periodStart,
			periodEnd,
		)
		if err != nil {
			return 0, "", err
		}

		return float64(count), "students attended", nil

	default:
		return 0, "", errors.New("unsupported per-student calculation basis")
	}
}

func (a *App) countPayrollActiveEnrollments(
	profile StaffSalaryProfile,
) (int, error) {
	query := `
		SELECT COUNT(DISTINCT se.admission_id)
		FROM student_enrollments se
		JOIN training_programs tp
			ON tp.id = se.training_program_id
		WHERE COALESCE(se.active, 1) = 1
	`

	args := make([]any, 0, 2)

	if profile.TrainingProgramID > 0 {
		query += `
			AND se.training_program_id = ?
		`
		args = append(args, profile.TrainingProgramID)
	}

	if profile.DivisionID > 0 {
		query += `
			AND tp.division_id = ?
		`
		args = append(args, profile.DivisionID)
	}

	var count int

	if err := a.queryRowDB(query, args...).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (a *App) countPayrollGroupMembers(
	profile StaffSalaryProfile,
) (int, error) {
	query := `
		SELECT COUNT(DISTINCT sgm.admission_id)
		FROM student_group_members sgm
		JOIN student_groups sg
			ON sg.id = sgm.group_id
		JOIN training_programs tp
			ON tp.id = sg.training_program_id
		JOIN student_group_staff sgs
			ON sgs.group_id = sg.id
		WHERE sgs.user_id = ?
	`

	args := []any{profile.UserID}

	if profile.TrainingProgramID > 0 {
		query += `
			AND sg.training_program_id = ?
		`
		args = append(args, profile.TrainingProgramID)
	}

	if profile.DivisionID > 0 {
		query += `
			AND tp.division_id = ?
		`
		args = append(args, profile.DivisionID)
	}

	var count int

	if err := a.queryRowDB(query, args...).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (a *App) countPayrollAttendingStudents(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (int, error) {
	query := `
		SELECT COUNT(DISTINCT ar.admission_id)
		FROM attendance_records ar
		JOIN student_groups sg
			ON sg.id = ar.group_id
		JOIN training_programs tp
			ON tp.id = sg.training_program_id
		JOIN student_group_staff sgs
			ON sgs.group_id = sg.id
		WHERE sgs.user_id = ?
		  AND ar.attendance_date >= ?
		  AND ar.attendance_date <= ?
		  AND LOWER(ar.status) IN ('present', 'late')
	`

	args := []any{
		profile.UserID,
		periodStart,
		periodEnd,
	}

	if profile.TrainingProgramID > 0 {
		query += `
			AND sg.training_program_id = ?
		`
		args = append(args, profile.TrainingProgramID)
	}

	if profile.DivisionID > 0 {
		query += `
			AND tp.division_id = ?
		`
		args = append(args, profile.DivisionID)
	}

	var count int

	if err := a.queryRowDB(query, args...).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func salaryProfileAppliesToPayrollPeriod(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) bool {
	if !profile.Active {
		return false
	}

	from := strings.TrimSpace(profile.EffectiveFrom)
	to := strings.TrimSpace(profile.EffectiveTo)

	if from != "" && from > periodEnd {
		return false
	}

	if to != "" && to < periodStart {
		return false
	}

	return true
}

func (a *App) generatePayrollRunPayments(
	runID int64,
	actorUserID int64,
) error {
	if runID <= 0 {
		return errors.New("invalid payroll run")
	}

	run, err := a.findPayrollRunByID(runID)
	if err != nil {
		return err
	}

	if run.Status == PayrollRunStatusApproved ||
		run.Status == PayrollRunStatusClosed {
		return errors.New(
			"approved or closed payroll cannot be regenerated",
		)
	}

	var existingCount int

	if err := a.queryRowDB(
		`
		SELECT COUNT(*)
		FROM payroll_payments
		WHERE payroll_run_id = ?
		`,
		runID,
	).Scan(&existingCount); err != nil {
		return err
	}

	if existingCount > 0 {
		return errors.New(
			"payroll has already been generated for this period",
		)
	}

	profiles, err := a.listStaffSalaryProfiles()
	if err != nil {
		return err
	}

	applicable := make(
		[]StaffSalaryProfile,
		0,
		len(profiles),
	)

	for _, profile := range profiles {
		if salaryProfileAppliesToPayrollPeriod(
			profile,
			run.PeriodStart,
			run.PeriodEnd,
		) {
			applicable = append(
				applicable,
				profile,
			)
		}
	}

	if len(applicable) == 0 {
		return errors.New(
			"no active salary profiles apply to this payroll period",
		)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	for _, profile := range applicable {
		quantity, quantityLabel, err :=
			a.calculatePayrollProfileQuantity(
				profile,
				run.PeriodStart,
				run.PeriodEnd,
			)
		if err != nil {
			return fmt.Errorf(
				"calculate salary for %s: %w",
				profile.UserName,
				err,
			)
		}

		baseAmount :=
			profile.Rate * quantity

		paymentStatus :=
			PayrollPaymentStatusCalculated

		notes := strings.TrimSpace(profile.Notes)

		if normalizeSalaryType(
			profile.CompensationType,
		) == SalaryTypeHourly {
			paymentStatus =
				PayrollPaymentStatusDraft

			if notes != "" {
				notes += "\n"
			}

			notes +=
				"Hourly quantity requires manual approval before salary payment."
		}

		_, err = a.execTxDB(
			tx,
			`
			INSERT INTO payroll_payments (
				payroll_run_id,
				user_id,
				salary_profile_id,
				division_id,
				training_program_id,
				compensation_type,
				rate_snapshot,
				quantity,
				quantity_label,
				base_amount,
				additions_total,
				deductions_total,
				net_amount,
				status,
				notes,
				created_at,
				updated_at
			)
			VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?,
				?, 0, 0, ?, ?, ?, ?, ?
			)
			`,
			runID,
			profile.UserID,
			profile.ID,
			nullIfZero(profile.DivisionID),
			nullIfZero(profile.TrainingProgramID),
			normalizeSalaryType(
				profile.CompensationType,
			),
			profile.Rate,
			quantity,
			quantityLabel,
			baseAmount,
			baseAmount,
			paymentStatus,
			notes,
			now,
			now,
		)
		if err != nil {
			return err
		}
	}

	_, err = a.execTxDB(
		tx,
		`
		UPDATE payroll_runs
		SET
			status = ?,
			updated_at = ?
		WHERE id = ?
		`,
		PayrollRunStatusCalculated,
		now,
		runID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) updatePayrollPaymentQuantity(
	paymentID int64,
	quantity float64,
) error {
	if paymentID <= 0 {
		return errors.New("invalid salary payment")
	}

	if quantity < 0 {
		return errors.New("quantity cannot be negative")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		compensationType string
		rate             float64
		status           string
	)

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			compensation_type,
			rate_snapshot,
			status
		FROM payroll_payments
		WHERE id = ?
		`,
		paymentID,
	).Scan(
		&compensationType,
		&rate,
		&status,
	); err != nil {
		return err
	}

	if status == PayrollPaymentStatusPaid ||
		status == PayrollPaymentStatusVoid ||
		status == PayrollPaymentStatusApproved {
		return errors.New(
			"approved, paid or void salary payments cannot be edited",
		)
	}

	if normalizeSalaryType(compensationType) != SalaryTypeHourly {
		return errors.New(
			"manual quantity editing is currently allowed only for hourly salary profiles",
		)
	}

	baseAmount := rate * quantity

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_payments
		SET
			quantity = ?,
			quantity_label = ?,
			base_amount = ?,
			status = ?,
			updated_at = ?
		WHERE id = ?
		`,
		quantity,
		"approved hours",
		baseAmount,
		PayrollPaymentStatusCalculated,
		time.Now().UTC(),
		paymentID,
	); err != nil {
		return err
	}

	if err := recalculatePayrollPaymentTx(
		a,
		tx,
		paymentID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deletePayrollAdjustment(
	adjustmentID int64,
) error {
	if adjustmentID <= 0 {
		return errors.New("invalid payroll adjustment")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		paymentID int64
		status    string
	)

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			pa.payroll_payment_id,
			pp.status
		FROM payroll_adjustments pa
		JOIN payroll_payments pp
			ON pp.id = pa.payroll_payment_id
		WHERE pa.id = ?
		`,
		adjustmentID,
	).Scan(
		&paymentID,
		&status,
	); err != nil {
		return err
	}

	if status == PayrollPaymentStatusApproved ||
		status == PayrollPaymentStatusPaid ||
		status == PayrollPaymentStatusVoid {
		return errors.New(
			"adjustments cannot be changed after salary approval",
		)
	}

	if _, err := a.execTxDB(
		tx,
		`
		DELETE FROM payroll_adjustments
		WHERE id = ?
		`,
		adjustmentID,
	); err != nil {
		return err
	}

	if err := recalculatePayrollPaymentTx(
		a,
		tx,
		paymentID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) approvePayrollRun(
	runID int64,
	actorUserID int64,
) error {
	if runID <= 0 {
		return errors.New("invalid payroll run")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT status
		FROM payroll_runs
		WHERE id = ?
		`,
		runID,
	).Scan(&status); err != nil {
		return err
	}

	if status == PayrollRunStatusApproved {
		return errors.New("payroll is already approved")
	}

	if status == PayrollRunStatusClosed {
		return errors.New("closed payroll cannot be approved")
	}

	var paymentCount int
	var incompleteCount int

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			COUNT(*),
			COUNT(*) FILTER (
				WHERE status <> 'calculated'
			)
		FROM payroll_payments
		WHERE payroll_run_id = ?
		  AND status <> 'void'
		`,
		runID,
	).Scan(
		&paymentCount,
		&incompleteCount,
	); err != nil {
		return err
	}

	if paymentCount == 0 {
		return errors.New(
			"generate payroll before approval",
		)
	}

	if incompleteCount > 0 {
		return errors.New(
			"all salary rows must be calculated before payroll approval",
		)
	}

	now := time.Now().UTC()

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_payments
		SET
			status = ?,
			updated_at = ?
		WHERE payroll_run_id = ?
		  AND status = ?
		`,
		PayrollPaymentStatusApproved,
		now,
		runID,
		PayrollPaymentStatusCalculated,
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_runs
		SET
			status = ?,
			approved_by_user_id = ?,
			approved_at = ?,
			updated_at = ?
		WHERE id = ?
		`,
		PayrollRunStatusApproved,
		nullIfZero(actorUserID),
		now,
		now,
		runID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) payPayrollPayment(
	paymentID int64,
	financeAccountID int64,
	paymentReference string,
	actorUserID int64,
) error {
	if paymentID <= 0 {
		return errors.New("invalid salary payment")
	}

	if financeAccountID <= 0 {
		return errors.New("finance account is required")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		runID             int64
		userID            int64
		userName          string
		divisionID        int64
		netAmount         float64
		status            string
		existingFinanceID int64
	)

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			pp.payroll_run_id,
			pp.user_id,
			COALESCE(u.name, ''),
			COALESCE(pp.division_id, 0),
			pp.net_amount,
			pp.status,
			COALESCE(pp.finance_transaction_id, 0)
		FROM payroll_payments pp
		JOIN users u
			ON u.id = pp.user_id
		WHERE pp.id = ?
		`,
		paymentID,
	).Scan(
		&runID,
		&userID,
		&userName,
		&divisionID,
		&netAmount,
		&status,
		&existingFinanceID,
	); err != nil {
		return err
	}

	if status == PayrollPaymentStatusPaid {
		return errors.New("salary has already been paid")
	}

	if status != PayrollPaymentStatusApproved {
		return errors.New(
			"salary must be approved before payment",
		)
	}

	if existingFinanceID > 0 {
		return errors.New(
			"salary is already linked to a finance transaction",
		)
	}

	if netAmount <= 0 {
		return errors.New(
			"net salary must be greater than zero",
		)
	}

	account, err :=
		findFinanceAccountByIDQuery(tx, financeAccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New(
				"finance account was not found",
			)
		}

		return err
	}

	if !account.IsActive {
		return errors.New(
			"selected finance account is inactive",
		)
	}

	if divisionID > 0 &&
		account.DivisionID != divisionID {
		return errors.New(
			"selected finance account belongs to a different division",
		)
	}

	paymentMethod :=
		financePaymentMethodForAccount(
			account.AccountType,
		)

	now := time.Now().UTC()

	description := fmt.Sprintf(
		"Salary payment - %s",
		strings.TrimSpace(userName),
	)

	transactionID, err := insertFinanceTransactionTx(
		tx,
		financeTransactionCreate{
			DivisionID:       divisionID,
			Category:         "staff_salary_expense",
			ApprovalStatus:   financeApprovalApproved,
			TransactionType:  financeTxnTypeExpense,
			ReferenceType:    "payroll_payment",
			ReferenceID:      paymentID,
			SourceType:       "payroll_payment",
			SourceID:         paymentID,
			FinanceAccountID: financeAccountID,
			PersonName:       userName,
			Description:      description,
			Notes: strings.TrimSpace(
				paymentReference,
			),
			PaymentMethod: paymentMethod,

			// Expenses are stored as negative movements.
			Amount: -normalizeMoney(netAmount),

			RecordedByUserID: actorUserID,
			ApprovedByUserID: actorUserID,
			RecordedAt:       now,
			ApprovedAt:       now,
		},
	)
	if err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_payments
		SET
			status = ?,
			payment_method = ?,
			payment_reference = ?,
			paid_at = ?,
			paid_by_user_id = ?,
			finance_transaction_id = ?,
			updated_at = ?
		WHERE id = ?
		`,
		PayrollPaymentStatusPaid,
		paymentMethod,
		strings.TrimSpace(paymentReference),
		now,
		nullIfZero(actorUserID),
		transactionID,
		now,
		paymentID,
	); err != nil {
		return err
	}

	var unpaidCount int

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT COUNT(*)
		FROM payroll_payments
		WHERE payroll_run_id = ?
		  AND status NOT IN ('paid', 'void')
		`,
		runID,
	).Scan(&unpaidCount); err != nil {
		return err
	}

	if unpaidCount == 0 {
		if _, err := a.execTxDB(
			tx,
			`
			UPDATE payroll_runs
			SET
				status = ?,
				updated_at = ?
			WHERE id = ?
			`,
			PayrollRunStatusClosed,
			now,
			runID,
		); err != nil {
			return err
		}
	}

	_ = userID

	return tx.Commit()
}

// findPayrollPaymentByID returns one payroll payment together with its
// payroll-run context and adjustments. It is used by the printable
// salary-slip workflow.
func (a *App) findPayrollPaymentByID(
	paymentID int64,
) (*PayrollPayment, *PayrollRun, error) {
	if paymentID <= 0 {
		return nil, nil, sql.ErrNoRows
	}

	var payment PayrollPayment
	var paidAt sql.NullTime

	err := a.queryRowDB(
		`
		SELECT
			pp.id,
			pp.payroll_run_id,
			pp.user_id,
			COALESCE(u.name, ''),
			COALESCE(u.email, ''),
			COALESCE(pp.salary_profile_id, 0),
			COALESCE(pp.division_id, 0),
			COALESCE(d.name, ''),
			COALESCE(pp.training_program_id, 0),
			COALESCE(tp.name, ''),
			pp.compensation_type,
			pp.rate_snapshot,
			pp.quantity,
			pp.quantity_label,
			pp.base_amount,
			pp.additions_total,
			pp.deductions_total,
			pp.net_amount,
			pp.status,
			pp.payment_method,
			pp.payment_reference,
			pp.paid_at,
			COALESCE(pp.paid_by_user_id, 0),
			COALESCE(pp.finance_transaction_id, 0),
			pp.notes,
			pp.created_at,
			pp.updated_at
		FROM payroll_payments pp
		JOIN users u
			ON u.id = pp.user_id
		LEFT JOIN divisions d
			ON d.id = pp.division_id
		LEFT JOIN training_programs tp
			ON tp.id = pp.training_program_id
		WHERE pp.id = ?
		`,
		paymentID,
	).Scan(
		&payment.ID,
		&payment.PayrollRunID,
		&payment.UserID,
		&payment.UserName,
		&payment.UserEmail,
		&payment.SalaryProfileID,
		&payment.DivisionID,
		&payment.DivisionName,
		&payment.TrainingProgramID,
		&payment.TrainingProgramName,
		&payment.CompensationType,
		&payment.RateSnapshot,
		&payment.Quantity,
		&payment.QuantityLabel,
		&payment.BaseAmount,
		&payment.AdditionsTotal,
		&payment.DeductionsTotal,
		&payment.NetAmount,
		&payment.Status,
		&payment.PaymentMethod,
		&payment.PaymentReference,
		&paidAt,
		&payment.PaidByUserID,
		&payment.FinanceTransactionID,
		&payment.Notes,
		&payment.CreatedAt,
		&payment.UpdatedAt,
	)
	if err != nil {
		return nil, nil, err
	}

	if paidAt.Valid {
		payment.PaidAt = paidAt.Time
	}

	adjustments, err :=
		a.listPayrollAdjustments(payment.ID)
	if err != nil {
		return nil, nil, err
	}

	payment.Adjustments = adjustments

	run, err := a.findPayrollRunByID(payment.PayrollRunID)
	if err != nil {
		return nil, nil, err
	}

	return &payment, run, nil
}
