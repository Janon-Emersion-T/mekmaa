package main

import (
	"database/sql"
	"errors"
	"strings"
)

func (a *App) archiveBookingReceivable(scheduleID, actorUserID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if scheduleID <= 0 || actorUserID <= 0 || reason == "" {
		return errors.New("booking, administrator and archive reason are required")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	if err := a.queryRowTxDB(tx,
		`SELECT id FROM space_schedules WHERE id = ? FOR UPDATE`,
		scheduleID,
	).Scan(&id); err != nil {
		return err
	}

	result, err := a.execTxDB(tx, `
        INSERT INTO booking_receivables_archives
            (schedule_id, archived_by_user_id, archive_reason)
        VALUES (?, ?, ?)
        ON CONFLICT (schedule_id) DO NOTHING
    `, scheduleID, actorUserID, reason)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("booking is already archived")
	}

	if _, err := a.execTxDB(tx, `
        INSERT INTO booking_receivables_archive_events
            (schedule_id, action, actor_user_id, reason)
        VALUES (?, 'archive', ?, ?)
    `, scheduleID, actorUserID, reason); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) restoreBookingReceivable(scheduleID, actorUserID int64) error {
	if scheduleID <= 0 || actorUserID <= 0 {
		return errors.New("booking and administrator are required")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	if err := a.queryRowTxDB(tx,
		`SELECT id FROM space_schedules WHERE id = ? FOR UPDATE`,
		scheduleID,
	).Scan(&id); err != nil {
		return err
	}

	result, err := a.execTxDB(tx, `
        DELETE FROM booking_receivables_archives WHERE schedule_id = ?
    `, scheduleID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}

	if _, err := a.execTxDB(tx, `
        INSERT INTO booking_receivables_archive_events
            (schedule_id, action, actor_user_id, reason)
        VALUES (?, 'restore', ?, '')
    `, scheduleID, actorUserID); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) isBookingReceivableArchived(scheduleID int64) (bool, error) {
	var count int

	err := a.queryRowDB(`
        SELECT COUNT(*)
        FROM booking_receivables_archives
        WHERE schedule_id = ?
    `, scheduleID).Scan(&count)

	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// listBookingReceivablesRegister returns historical booking financials
// for the management register without changing accounting receivables.
func (a *App) listBookingReceivablesRegister(
	divisionIDs []int64,
	archived bool,
) ([]BookingFinancial, error) {
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

	// Include customer bookings without a booking_financials row.
	// Existing financial rows retain their established payment calculations.
	schedules, err := a.listSpaceSchedules()
	if err != nil {
		return nil, err
	}
	financialBySchedule := make(map[int64]struct{}, len(financials))
	for _, financial := range financials {
		financialBySchedule[financial.ScheduleID] = struct{}{}
	}
	for _, schedule := range schedules {
		if schedule.EntryType != "booking" {
			continue
		}
		if _, exists := financialBySchedule[schedule.ID]; exists {
			continue
		}
		financials = append(financials, BookingFinancial{
			ScheduleID:        schedule.ID,
			QuotedAmount:      0,
			SlotDate:          schedule.SlotDate,
			SlotHour:          schedule.SlotHour,
			Activity:          schedule.Activity,
			Quantity:          schedule.Quantity,
			Status:            schedule.Status,
			Title:             schedule.Title,
			RequesterName:     schedule.RequesterName,
			RequesterEmail:    schedule.RequesterEmail,
			OutstandingAmount: 0,
			PaymentStatus:     "unpaid",
		})
	}

	oneToOneIDs, err := a.oneToOneScheduleIDSet()
	if err != nil {
		return nil, err
	}

	rows, err := a.queryDB(
		`SELECT schedule_id FROM booking_receivables_archives`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	archivedIDs := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		archivedIDs[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]BookingFinancial, 0, len(financials))

	for _, financial := range financials {
		if _, isOneToOne := oneToOneIDs[financial.ScheduleID]; isOneToOne {
			continue
		}

		_, isArchived := archivedIDs[financial.ScheduleID]
		if isArchived != archived {
			continue
		}

		result = append(result, financial)
	}

	return result, nil
}
