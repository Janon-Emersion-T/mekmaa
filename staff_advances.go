package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	StaffAdvanceRecoverySalary = "salary_deduction"
	StaffAdvanceRecoveryDirect = "direct_repayment"
)

type StaffAdvance struct {
	ID                   int64
	UserID               int64
	UserName             string
	DivisionID           int64
	DivisionName         string
	FinanceAccountID     int64
	FinanceAccountName   string
	Amount               float64
	RepaidAmount         float64
	OutstandingAmount    float64
	RecoveryMode         string
	InstallmentAmount    float64
	Status               string
	IssuedAt             time.Time
	Notes                string
	FinanceTransactionID int64
}

func applyStaffAdvanceDeductionsTx(a *App, tx *sql.Tx, paymentID, userID int64, grossAmount float64, actorUserID int64) error {
	if grossAmount <= 0 {
		return nil
	}
	rows, err := a.queryTxDB(tx, `SELECT sa.id, sa.amount, sa.installment_amount, COALESCE(SUM(CASE WHEN sar.voided_at IS NULL THEN sar.amount ELSE 0 END),0), COALESCE((SELECT SUM(pa.amount) FROM payroll_adjustments pa JOIN payroll_payments pp ON pp.id=pa.payroll_payment_id WHERE pa.adjustment_type='salary_advance' AND pa.direction='deduction' AND pa.description='Staff advance #' || sa.id || ' salary recovery' AND pp.status IN ('draft','calculated','approved')),0) FROM staff_advances sa LEFT JOIN staff_advance_repayments sar ON sar.staff_advance_id=sa.id WHERE sa.user_id=? AND sa.status='active' AND sa.recovery_mode='salary_deduction' GROUP BY sa.id ORDER BY sa.issued_at,sa.id`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	remaining := grossAmount
	now := time.Now().UTC()
	for rows.Next() {
		var id int64
		var amount, installment, repaid, reserved float64
		if err := rows.Scan(&id, &amount, &installment, &repaid, &reserved); err != nil {
			return err
		}
		outstanding := normalizeMoney(amount - repaid - reserved)
		deduction := installment
		if deduction > outstanding {
			deduction = outstanding
		}
		if deduction > remaining {
			deduction = remaining
		}
		deduction = normalizeMoney(deduction)
		if deduction <= 0 {
			continue
		}
		_, err = a.execTxDB(tx, `INSERT INTO payroll_adjustments (payroll_payment_id,adjustment_type,direction,description,amount,created_by_user_id,created_at,updated_at) VALUES (?, 'salary_advance','deduction',?, ?,?,?,?)`, paymentID, fmt.Sprintf("Staff advance #%d salary recovery", id), deduction, nullIfZero(actorUserID), now, now)
		if err != nil {
			return err
		}
		if err := recalculatePayrollPaymentTx(a, tx, paymentID); err != nil {
			return err
		}
		remaining = normalizeMoney(remaining - deduction)
	}
	return rows.Err()
}

func finalizeStaffAdvancePayrollRepaymentsTx(a *App, tx *sql.Tx, paymentID, actorUserID int64) error {
	rows, err := a.queryTxDB(tx, `SELECT description,amount FROM payroll_adjustments WHERE payroll_payment_id=? AND adjustment_type='salary_advance' AND direction='deduction'`, paymentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var description string
		var amount float64
		if err := rows.Scan(&description, &amount); err != nil {
			return err
		}
		var id int64
		if _, err := fmt.Sscanf(description, "Staff advance #%d salary recovery", &id); err != nil || id <= 0 {
			continue
		}
		result, err := a.execTxDB(tx, `UPDATE staff_advance_repayments SET amount=?, repaid_at=?, voided_at=NULL, voided_by_user_id=NULL, void_reason='', created_by_user_id=? WHERE staff_advance_id=? AND payroll_payment_id=?`, amount, now, nullIfZero(actorUserID), id, paymentID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			_, err = a.execTxDB(tx, `INSERT INTO staff_advance_repayments (staff_advance_id,payroll_payment_id,amount,repayment_method,repaid_at,created_by_user_id,created_at) VALUES (?, ?, ?, 'salary_deduction', ?, ?, ?)`, id, paymentID, amount, now, nullIfZero(actorUserID), now)
			if err != nil {
				return err
			}
		}
		var total, settled float64
		err = a.queryRowTxDB(tx, `SELECT sa.amount,COALESCE(SUM(CASE WHEN sar.voided_at IS NULL THEN sar.amount ELSE 0 END),0) FROM staff_advances sa LEFT JOIN staff_advance_repayments sar ON sar.staff_advance_id=sa.id WHERE sa.id=? GROUP BY sa.id`, id).Scan(&total, &settled)
		if err != nil {
			return err
		}
		if normalizeMoney(total-settled) == 0 {
			if _, err = a.execTxDB(tx, `UPDATE staff_advances SET status='settled',updated_at=? WHERE id=?`, now, id); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func voidStaffAdvancePayrollRepaymentsTx(a *App, tx *sql.Tx, paymentID, actorUserID int64, reason string) error {
	now := time.Now().UTC()
	_, err := a.execTxDB(tx, `UPDATE staff_advance_repayments SET voided_at=?,voided_by_user_id=?,void_reason=? WHERE payroll_payment_id=? AND voided_at IS NULL`, now, nullIfZero(actorUserID), strings.TrimSpace(reason), paymentID)
	if err != nil {
		return err
	}
	_, err = a.execTxDB(tx, `UPDATE staff_advances SET status='active',updated_at=? WHERE id IN (SELECT staff_advance_id FROM staff_advance_repayments WHERE payroll_payment_id=?) AND status='settled'`, now, paymentID)
	return err
}

func validStaffAdvanceRecoveryMode(value string) bool {
	return value == StaffAdvanceRecoverySalary || value == StaffAdvanceRecoveryDirect
}

func (a *App) createStaffAdvance(advance StaffAdvance, actorUserID int64) error {
	if advance.UserID <= 0 || advance.DivisionID <= 0 || advance.FinanceAccountID <= 0 {
		return errors.New("staff member, division, and payment account are required")
	}
	advance.Amount = normalizeMoney(advance.Amount)
	advance.InstallmentAmount = normalizeMoney(advance.InstallmentAmount)
	if advance.Amount <= 0 || !validStaffAdvanceRecoveryMode(advance.RecoveryMode) {
		return errors.New("a positive amount and valid recovery method are required")
	}
	if advance.RecoveryMode == StaffAdvanceRecoverySalary && advance.InstallmentAmount <= 0 {
		return errors.New("salary recovery requires a positive installment amount")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, advance.FinanceAccountID)
	if err != nil {
		return errors.New("payment account was not found")
	}
	if !account.IsActive || account.DivisionID != advance.DivisionID {
		return errors.New("payment account must be active and belong to the selected division")
	}
	var name string
	if err := a.queryRowTxDB(tx, `SELECT name FROM users WHERE id = ?`, advance.UserID).Scan(&name); err != nil {
		return errors.New("staff member was not found")
	}
	now := time.Now().UTC()
	id, err := a.insertAndReturnIDTx(tx, `INSERT INTO staff_advances (user_id, division_id, finance_account_id, amount, recovery_mode, installment_amount, status, issued_at, notes, created_by_user_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`, advance.UserID, advance.DivisionID, advance.FinanceAccountID, advance.Amount, advance.RecoveryMode, advance.InstallmentAmount, now, strings.TrimSpace(advance.Notes), nullIfZero(actorUserID), now, now)
	if err != nil {
		return err
	}
	financeID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{DivisionID: advance.DivisionID, Category: "staff_advance", ApprovalStatus: financeApprovalApproved, TransactionType: financeTxnTypeExpense, ReferenceType: "staff_advance", ReferenceID: id, SourceType: "staff_advance", SourceID: id, FinanceAccountID: advance.FinanceAccountID, PersonName: name, Description: "Staff advance - " + strings.TrimSpace(name), Notes: strings.TrimSpace(advance.Notes), PaymentMethod: financePaymentMethodForAccount(account.AccountType), Amount: -advance.Amount, RecordedByUserID: actorUserID, ApprovedByUserID: actorUserID, RecordedAt: now, ApprovedAt: now})
	if err != nil {
		return err
	}
	_, err = a.execTxDB(tx, `UPDATE staff_advances SET finance_transaction_id = ?, updated_at = ? WHERE id = ?`, financeID, now, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) listStaffAdvances() ([]StaffAdvance, error) {
	rows, err := a.queryDB(`SELECT sa.id, sa.user_id, COALESCE(u.name,''), sa.division_id, COALESCE(d.name,''), sa.finance_account_id, COALESCE(fa.name,''), sa.amount, COALESCE(SUM(CASE WHEN sar.voided_at IS NULL THEN sar.amount ELSE 0 END),0), sa.recovery_mode, sa.installment_amount, sa.status, sa.issued_at, sa.notes, COALESCE(sa.finance_transaction_id,0) FROM staff_advances sa JOIN users u ON u.id=sa.user_id JOIN divisions d ON d.id=sa.division_id JOIN finance_accounts fa ON fa.id=sa.finance_account_id LEFT JOIN staff_advance_repayments sar ON sar.staff_advance_id=sa.id GROUP BY sa.id,u.name,d.name,fa.name ORDER BY sa.status='active' DESC, sa.issued_at DESC, sa.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var advances []StaffAdvance
	for rows.Next() {
		var x StaffAdvance
		if err := rows.Scan(&x.ID, &x.UserID, &x.UserName, &x.DivisionID, &x.DivisionName, &x.FinanceAccountID, &x.FinanceAccountName, &x.Amount, &x.RepaidAmount, &x.RecoveryMode, &x.InstallmentAmount, &x.Status, &x.IssuedAt, &x.Notes, &x.FinanceTransactionID); err != nil {
			return nil, err
		}
		x.Amount = normalizeMoney(x.Amount)
		x.RepaidAmount = normalizeMoney(x.RepaidAmount)
		x.OutstandingAmount = normalizeMoney(x.Amount - x.RepaidAmount)
		advances = append(advances, x)
	}
	return advances, rows.Err()
}

func (a *App) collectStaffAdvanceRepayment(advanceID int64, amount float64, paymentMethod, note string, actorUserID int64) error {
	amount = normalizeMoney(amount)
	if advanceID <= 0 || amount <= 0 {
		return errors.New("a valid advance and positive repayment amount are required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var advance StaffAdvance
	err = a.queryRowTxDB(tx, `SELECT sa.user_id,COALESCE(u.name,''),sa.division_id,sa.amount,sa.recovery_mode,sa.status FROM staff_advances sa JOIN users u ON u.id=sa.user_id WHERE sa.id=?`, advanceID).Scan(&advance.UserID, &advance.UserName, &advance.DivisionID, &advance.Amount, &advance.RecoveryMode, &advance.Status)
	if err != nil {
		return err
	}
	if advance.Status != "active" {
		return errors.New("only active advances can be repaid")
	}
	var repaid float64
	if err = a.queryRowTxDB(tx, `SELECT COALESCE(SUM(amount),0) FROM staff_advance_repayments WHERE staff_advance_id=? AND voided_at IS NULL`, advanceID).Scan(&repaid); err != nil {
		return err
	}
	if amount > normalizeMoney(advance.Amount-repaid) {
		return errors.New("repayment exceeds outstanding advance balance")
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, advance.DivisionID, paymentMethod)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	repaymentID, err := a.insertAndReturnIDTx(tx, `INSERT INTO staff_advance_repayments (staff_advance_id,amount,repayment_method,repaid_at,created_by_user_id,created_at) VALUES (?, ?, ?, ?, ?, ?)`, advanceID, amount, normalizePaymentMethod(paymentMethod), now, nullIfZero(actorUserID), now)
	if err != nil {
		return err
	}
	financeID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{DivisionID: advance.DivisionID, Category: "staff_advance_repayment", ApprovalStatus: financeApprovalApproved, TransactionType: financeTxnTypeIncome, ReferenceType: "staff_advance_repayment", ReferenceID: repaymentID, SourceType: "staff_advance_repayment", SourceID: repaymentID, FinanceAccountID: account.ID, PersonName: advance.UserName, Description: fmt.Sprintf("Staff advance repayment - %s", advance.UserName), Notes: strings.TrimSpace(note), PaymentMethod: normalizePaymentMethod(paymentMethod), Amount: amount, RecordedByUserID: actorUserID, ApprovedByUserID: actorUserID, RecordedAt: now, ApprovedAt: now})
	if err != nil {
		return err
	}
	_, err = a.execTxDB(tx, `UPDATE staff_advance_repayments SET finance_transaction_id=? WHERE id=?`, financeID, repaymentID)
	if err != nil {
		return err
	}
	if normalizeMoney(advance.Amount-repaid-amount) == 0 {
		_, err = a.execTxDB(tx, `UPDATE staff_advances SET status='settled', updated_at=? WHERE id=?`, now, advanceID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
