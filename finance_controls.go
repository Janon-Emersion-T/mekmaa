package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	financeApprovalPending  = "pending"
	financeApprovalApproved = "approved"
)

func validFinanceApprovalStatus(value string) bool {
	return value == financeApprovalPending || value == financeApprovalApproved
}

func financeTransactionPosted(transaction FinanceTransaction) bool {
	return !transaction.Voided && transaction.ApprovalStatus != financeApprovalPending
}

func listFinanceBalancesInclude(transaction FinanceTransaction) bool {
	return financeTransactionPosted(transaction)
}

func (a *App) currentFinancePeriodLock() (*FinancePeriodLock, error) {
	row := a.db.QueryRow(`
		SELECT fpl.id, COALESCE(locked_until, ''), COALESCE(notes, ''), COALESCE(updated_by_user_id, 0), COALESCE(u.name, ''), updated_at
		FROM finance_period_locks fpl
		LEFT JOIN users u ON u.id = fpl.updated_by_user_id
		ORDER BY fpl.id DESC
		LIMIT 1
	`)
	var lock FinancePeriodLock
	if err := row.Scan(&lock.ID, &lock.LockedUntil, &lock.Notes, &lock.UpdatedByUserID, &lock.UpdatedByUserName, &lock.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &lock, nil
}

func (a *App) financeDateLocked(recordedAt time.Time) (bool, *FinancePeriodLock, error) {
	lock, err := a.currentFinancePeriodLock()
	if err != nil || lock == nil || strings.TrimSpace(lock.LockedUntil) == "" {
		return false, lock, err
	}
	lockedUntil, err := time.ParseInLocation("2006-01-02", lock.LockedUntil, time.Local)
	if err != nil {
		return false, lock, err
	}
	date := time.Date(recordedAt.In(time.Local).Year(), recordedAt.In(time.Local).Month(), recordedAt.In(time.Local).Day(), 0, 0, 0, 0, time.Local)
	return !date.After(lockedUntil), lock, nil
}

func (a *App) ensureFinanceDateUnlocked(recordedAt time.Time, label string) error {
	locked, lock, err := a.financeDateLocked(recordedAt)
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	message := label + " falls inside a locked finance period"
	if lock != nil && strings.TrimSpace(lock.LockedUntil) != "" {
		message += " (locked through " + formatCalendarDate(lock.LockedUntil) + ")"
	}
	return errors.New(message)
}

func ensureFinanceDateUnlockedTx(tx *sql.Tx, recordedAt time.Time, label string) error {
	row := tx.QueryRow(`SELECT COALESCE(locked_until, '') FROM finance_period_locks ORDER BY id DESC LIMIT 1`)
	var lockedUntilRaw string
	if err := row.Scan(&lockedUntilRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	lockedUntilRaw = strings.TrimSpace(lockedUntilRaw)
	if lockedUntilRaw == "" {
		return nil
	}
	lockedUntil, err := time.ParseInLocation("2006-01-02", lockedUntilRaw, time.Local)
	if err != nil {
		return err
	}
	date := time.Date(recordedAt.In(time.Local).Year(), recordedAt.In(time.Local).Month(), recordedAt.In(time.Local).Day(), 0, 0, 0, 0, time.Local)
	if date.After(lockedUntil) {
		return nil
	}
	return errors.New(label + " falls inside a locked finance period (locked through " + formatCalendarDate(lockedUntilRaw) + ")")
}

func (a *App) updateFinancePeriodLock(lockedUntil, notes string, updatedByUserID int64) error {
	lockedUntil = strings.TrimSpace(lockedUntil)
	notes = strings.TrimSpace(notes)
	if lockedUntil != "" {
		if _, err := time.ParseInLocation("2006-01-02", lockedUntil, time.Local); err != nil {
			return errors.New("a valid lock date is required")
		}
	}
	now := time.Now().UTC()
	lock, err := a.currentFinancePeriodLock()
	if err != nil {
		return err
	}
	if lock == nil {
		_, err = a.db.Exec(`
			INSERT INTO finance_period_locks (locked_until, notes, updated_by_user_id, updated_at)
			VALUES (?, ?, ?, ?)
		`, nullIfBlank(lockedUntil), notes, nullIfZero(updatedByUserID), now)
		return err
	}
	_, err = a.db.Exec(`
		UPDATE finance_period_locks
		SET locked_until = ?, notes = ?, updated_by_user_id = ?, updated_at = ?
		WHERE id = ?
	`, nullIfBlank(lockedUntil), notes, nullIfZero(updatedByUserID), now, lock.ID)
	return err
}

func (a *App) approveFinanceTransaction(transactionID, approvedByUserID int64) error {
	if transactionID <= 0 {
		return errors.New("finance transaction not found")
	}
	result, err := a.db.Exec(`
		UPDATE finance_transactions
		SET approval_status = ?, approved_by_user_id = ?, approved_at = ?, updated_at = ?
		WHERE id = ? AND approval_status = ? AND voided_at IS NULL
	`, financeApprovalApproved, nullIfZero(approvedByUserID), time.Now().UTC(), time.Now().UTC(), transactionID, financeApprovalPending)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("pending finance transaction not found")
	}
	return nil
}

func nullIfZeroTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
