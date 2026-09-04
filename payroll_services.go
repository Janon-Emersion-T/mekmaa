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

	StaffCount       int
	BaseTotal        float64
	AdditionsTotal   float64
	DeductionsTotal  float64
	NetTotal         float64
	PaidTotal        float64
	OutstandingTotal float64
}

type PayrollPortfolioSummary struct {
	RunCount         int
	OpenRunCount     int
	StaffCount       int
	NetTotal         float64
	PaidTotal        float64
	OutstandingTotal float64
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

	Adjustments        []PayrollAdjustment
	CalculationDetails []PayrollPaymentCalculationDetail
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

type PayrollPaymentCalculationDetail struct {
	ID int64

	PayrollPaymentID int64

	DetailType string
	SourceType string
	SourceID   int64

	Label      string
	DetailNote string

	Quantity       float64
	RateSnapshot   float64
	AmountSnapshot float64

	SortOrder int
	CreatedAt time.Time
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

func isCalendarMonthPeriod(start, end time.Time) bool {
	if start.Day() != 1 {
		return false
	}
	return end.Equal(start.AddDate(0, 1, 0).AddDate(0, 0, -1))
}

func payrollUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") ||
		strings.Contains(message, "duplicate key")
}

func payrollRunAllowsGenerate(run *PayrollRun) bool {
	return run != nil &&
		run.Status == PayrollRunStatusDraft &&
		run.StaffCount == 0
}

func payrollRunAllowsRecalculate(run *PayrollRun) bool {
	if run == nil || len(run.Payments) == 0 ||
		(run.Status != PayrollRunStatusDraft &&
			run.Status != PayrollRunStatusCalculated) {
		return false
	}

	// Recalculation would change a salary already recorded as paid.
	for _, payment := range run.Payments {
		if payment.Status == PayrollPaymentStatusPaid {
			return false
		}
	}

	return true
}

func payrollRunAllowsApprove(run *PayrollRun) bool {
	return run != nil &&
		run.Status == PayrollRunStatusCalculated &&
		len(run.Payments) > 0
}

func payrollRunAllowsClose(run *PayrollRun) bool {
	if run == nil || run.Status != PayrollRunStatusApproved || len(run.Payments) == 0 {
		return false
	}
	for _, payment := range run.Payments {
		switch payment.Status {
		case PayrollPaymentStatusPaid, PayrollPaymentStatusVoid:
		default:
			return false
		}
	}
	return true
}

func payrollPaymentAllowsAdjustments(payment PayrollPayment) bool {
	switch payment.Status {
	case PayrollPaymentStatusDraft, PayrollPaymentStatusCalculated:
		return true
	default:
		return false
	}
}

func payrollPaymentAllowsPay(payment PayrollPayment) bool {
	return (payment.Status == PayrollPaymentStatusCalculated ||
		payment.Status == PayrollPaymentStatusApproved) &&
		payment.NetAmount > 0 &&
		payment.FinanceTransactionID <= 0
}

func payrollPaymentAllowsVoid(payment PayrollPayment) bool {
	return payment.Status == PayrollPaymentStatusPaid &&
		payment.FinanceTransactionID > 0
}

func payrollPaymentReferenceCode(payment PayrollPayment) string {
	if payment.ID <= 0 {
		return ""
	}
	return fmt.Sprintf("PAY-%06d", payment.ID)
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

	var exactCount int
	if err := a.queryRowDB(
		`SELECT COUNT(*) FROM payroll_runs WHERE period_start = ? AND period_end = ?`,
		periodStart,
		periodEnd,
	).Scan(&exactCount); err != nil {
		return 0, err
	}
	if exactCount > 0 {
		return 0, errors.New("a payroll run already exists for this exact period")
	}

	if isCalendarMonthPeriod(start, end) {
		var overlapCount int
		if err := a.queryRowDB(
			`
			SELECT COUNT(*)
			FROM payroll_runs
			WHERE period_start <= ?
			  AND period_end >= ?
			`,
			periodEnd,
			periodStart,
		).Scan(&overlapCount); err != nil {
			return 0, err
		}
		if overlapCount > 0 {
			return 0, errors.New("a payroll run already overlaps this calendar month")
		}
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

		if payrollUniqueConstraintError(err) {
			return 0, errors.New("a payroll run already exists for this exact period")
		}
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
		if payrollUniqueConstraintError(err) {
			return 0, errors.New("a payroll run already exists for this exact period")
		}
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
			COALESCE(SUM(pp.net_amount), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'paid' THEN pp.net_amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pp.status IN ('draft', 'calculated', 'approved') THEN pp.net_amount ELSE 0 END), 0)
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
			&run.PaidTotal,
			&run.OutstandingTotal,
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
	if err := finalizeStaffAdvancePayrollRepaymentsTx(a, tx, paymentID, actorUserID); err != nil {
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
		if payment.Status == PayrollPaymentStatusPaid {
			run.PaidTotal += payment.NetAmount
		} else {
			run.OutstandingTotal += payment.NetAmount
		}
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

		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for index := range payments {
		adjustments, err := a.listPayrollAdjustments(payments[index].ID)
		if err != nil {
			return nil, err
		}
		payments[index].Adjustments = adjustments

		details, err := a.listPayrollPaymentCalculationDetails(payments[index].ID)
		if err != nil {
			return nil, err
		}
		payments[index].CalculationDetails = details
	}

	return payments, nil
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

	case SalaryTypeDaily:
		return 0, "days - manual entry required", nil

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

	case SalaryTypePerSession:
		count, err := a.countPayrollWorkedSessions(
			profile,
			periodStart,
			periodEnd,
		)
		if err != nil {
			return 0, "", err
		}

		return float64(count), "worked sessions", nil

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
		count, err := a.countPayrollActiveEnrollments(
			profile,
			periodStart,
			periodEnd,
		)
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
	periodStart string,
	periodEnd string,
) (int, error) {
	query := `
		SELECT COUNT(DISTINCT se.admission_id)
		FROM student_enrollments se
		JOIN training_programs tp
			ON tp.id = se.training_program_id
		WHERE COALESCE(se.active, 1) = 1
		  AND se.enrollment_date <= ?
		  AND EXISTS (
			  SELECT 1
			  FROM student_group_members sgm
			  JOIN student_groups sg
			    ON sg.id = sgm.group_id
			  WHERE sgm.admission_id = se.admission_id
			    AND sg.training_program_id = se.training_program_id
			    AND (
			        EXISTS (
			            SELECT 1
			            FROM student_group_staff sgs
			            WHERE sgs.group_id = sg.id
			              AND sgs.user_id = ?
			        )
			        OR EXISTS (
			            SELECT 1
			            FROM student_group_coaches sgc
			            WHERE sgc.group_id = sg.id
			              AND sgc.user_id = ?
			        )
			    )
		  )
		  AND NOT EXISTS (
			  SELECT 1
			  FROM student_enrollment_leaves sel
			  WHERE sel.enrollment_id = se.id
			    AND COALESCE(sel.active, 1) = 1
			    AND sel.start_date <= ?
			    AND sel.end_date >= ?
		  )
	`

	args := []any{
		periodEnd,
		profile.UserID,
		profile.UserID,
		periodStart,
		periodEnd,
	}

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

func (a *App) countPayrollWorkedSessions(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (int, error) {
	query := `
		SELECT COUNT(DISTINCT sso.id)
		FROM student_group_session_occurrences sso
		JOIN student_group_session_staff ssos
			ON ssos.occurrence_id = sso.id
		JOIN student_groups sg
			ON sg.id = sso.group_id
		LEFT JOIN training_programs tp
			ON tp.id = sg.training_program_id
		WHERE ssos.user_id = ?
		  AND ssos.work_status = 'worked'
		  AND sso.status = 'completed'
		  AND sso.occurrence_date >= ?
		  AND sso.occurrence_date <= ?
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

	return a.syncPayrollRunPayments(
		*run,
		actorUserID,
		false,
	)
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

	quantityLabel := ""
	switch normalizeSalaryType(compensationType) {
	case SalaryTypeHourly, SalaryTypeDaily:
	default:
		if err := a.queryRowTxDB(
			tx,
			`
			SELECT COALESCE(quantity_label, '')
			FROM payroll_payments
			WHERE id = ?
			`,
			paymentID,
		).Scan(&quantityLabel); err != nil {
			return err
		}
	}

	if !payrollPaymentAllowsManualQuantity(PayrollPayment{
		Status:           status,
		CompensationType: compensationType,
		QuantityLabel:    quantityLabel,
	}) {
		return errors.New(
			"manual quantity editing is currently allowed only for payroll rows that require manual entry",
		)
	}

	baseAmount := rate * quantity

	quantityLabel = "approved quantity"
	switch normalizeSalaryType(compensationType) {
	case SalaryTypeHourly:
		quantityLabel = "approved hours"
	case SalaryTypeDaily:
		quantityLabel = "approved days"
	case SalaryTypePerSession:
		quantityLabel = "approved sessions"
	case SalaryTypePerStudent:
		quantityLabel = "approved students"
	}

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
		quantityLabel,
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
	if status == PayrollRunStatusDraft {
		return errors.New("calculate payroll before approval")
	}

	var paymentCount int
	var draftCount int
	var unresolvedCount int

	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'draft'),
			COUNT(*) FILTER (
			WHERE status NOT IN ('calculated', 'paid', 'void')
			)
		FROM payroll_payments
		WHERE payroll_run_id = ?
		`,
		runID,
	).Scan(
		&paymentCount,
		&draftCount,
		&unresolvedCount,
	); err != nil {
		return err
	}

	if paymentCount == 0 {
		return errors.New(
			"generate payroll before approval",
		)
	}

	if draftCount > 0 {
		return errors.New(
			"complete all manual salary quantities before approval",
		)
	}

	if unresolvedCount > 0 {
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

	categoryExists, err := a.financeCategoryExists("expense", "staff_salary_expense", true)
	if err != nil {
		return err
	}
	if !categoryExists {
		return errors.New("finance category staff_salary_expense is missing or inactive")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
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

	if status != PayrollPaymentStatusCalculated &&
		status != PayrollPaymentStatusApproved {
		return errors.New(
			"salary must be calculated before payment",
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
		if payrollUniqueConstraintError(err) {
			var linkedID int64
			queryErr := a.queryRowTxDB(
				tx,
				`
				SELECT id
				FROM finance_transactions
				WHERE source_type = 'payroll_payment'
				  AND source_id = ?
				ORDER BY id ASC
				LIMIT 1
				`,
				paymentID,
			).Scan(&linkedID)
			if queryErr == nil && linkedID > 0 {
				return errors.New("salary payment was already recorded")
			}
		}
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

	_ = userID

	return tx.Commit()
}

func (a *App) closePayrollRun(
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
		`SELECT status FROM payroll_runs WHERE id = ?`,
		runID,
	).Scan(&status); err != nil {
		return err
	}
	if status == PayrollRunStatusClosed {
		return errors.New("payroll is already closed")
	}
	if status != PayrollRunStatusApproved {
		return errors.New("only approved payroll can be closed")
	}

	var paymentCount int
	var unresolvedCount int
	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status NOT IN ('paid', 'void'))
		FROM payroll_payments
		WHERE payroll_run_id = ?
		`,
		runID,
	).Scan(&paymentCount, &unresolvedCount); err != nil {
		return err
	}
	if paymentCount == 0 {
		return errors.New("generate payroll before closing")
	}
	if unresolvedCount > 0 {
		return errors.New("all salary payments must be paid or void before closing payroll")
	}

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
		time.Now().UTC(),
		runID,
	); err != nil {
		return err
	}

	_ = actorUserID
	return tx.Commit()
}

func (a *App) voidPayrollPayment(
	paymentID int64,
	reason string,
	actorUserID int64,
) error {
	if paymentID <= 0 {
		return errors.New("invalid salary payment")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var financeTransactionID int64
	var existingNotes string
	if err := a.queryRowTxDB(
		tx,
		`
		SELECT
			status,
			COALESCE(finance_transaction_id, 0),
			COALESCE(notes, '')
		FROM payroll_payments
		WHERE id = ?
		`,
		paymentID,
	).Scan(&status, &financeTransactionID, &existingNotes); err != nil {
		return err
	}
	if status == PayrollPaymentStatusVoid {
		return errors.New("salary payment is already void")
	}
	if status != PayrollPaymentStatusPaid {
		return errors.New("only paid salary payments can be voided")
	}
	if financeTransactionID <= 0 {
		return errors.New("paid salary payment is missing its finance transaction")
	}

	if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, actorUserID); err != nil {
		return err
	}
	if err := voidStaffAdvancePayrollRepaymentsTx(a, tx, paymentID, actorUserID, reason); err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`
		UPDATE payroll_payments
		SET
			status = ?,
			notes = ?,
			updated_at = ?
		WHERE id = ?
		`,
		PayrollPaymentStatusVoid,
		appendPayrollCalculationNote(existingNotes, "Payment voided: "+reason),
		time.Now().UTC(),
		paymentID,
	); err != nil {
		return err
	}

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

	details, err := a.listPayrollPaymentCalculationDetails(payment.ID)
	if err != nil {
		return nil, nil, err
	}
	payment.CalculationDetails = details

	run, err := a.findPayrollRunByID(payment.PayrollRunID)
	if err != nil {
		return nil, nil, err
	}

	return &payment, run, nil
}
