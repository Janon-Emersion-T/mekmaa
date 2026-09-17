package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

const payrollEarningOneToOne = "one_to_one"
const payrollSourceOneToOneSession = "one_to_one_booking_session"

type oneToOnePayrollEarning struct {
	UserID     int64
	DivisionID int64
	Snapshot   payrollCalculatedSnapshot
}

// Session dates determine the pay period; each session carries its assigned coach fee.
func (a *App) listOneToOnePayrollEarnings(start, end string, userID, runID int64) ([]oneToOnePayrollEarning, error) {
	rows, err := a.queryDB(`
 SELECT ots.id, COALESCE(ots.coach_user_id, ob.coach_user_id, 0),
 ss.slot_date, ss.slot_hour, ob.customer_name, ob.offering_name,
 ots.session_number, ots.coach_fee
 FROM one_to_one_booking_sessions ots
 JOIN one_to_one_bookings ob ON ob.id = ots.booking_id
 JOIN space_schedules ss ON ss.id = ots.schedule_id
 WHERE ots.status = 'completed' AND ss.status <> 'cancelled'
 AND ss.slot_date >= ? AND ss.slot_date <= ? AND ots.coach_fee > 0
 AND COALESCE(ots.coach_user_id, ob.coach_user_id, 0) > 0
 AND (? = 0 OR COALESCE(ots.coach_user_id, ob.coach_user_id, 0) = ?)
 AND NOT EXISTS (
  SELECT 1 FROM payroll_payment_calculation_details detail
  JOIN payroll_payments pp ON pp.id = detail.payroll_payment_id
  WHERE detail.source_type = 'one_to_one_booking_session' AND detail.source_id = ots.id
  AND pp.status <> 'void' AND (pp.payroll_run_id <> ? OR pp.user_id <> COALESCE(ots.coach_user_id, ob.coach_user_id, 0))
 )
 ORDER BY COALESCE(ots.coach_user_id, ob.coach_user_id, 0), ss.slot_date, ss.slot_hour, ots.id
 `, start, end, userID, userID, runID)
	if err != nil {
		return nil, err
	}
	byUser := map[int64]*oneToOnePayrollEarning{}
	var ordered []int64
	for rows.Next() {
		var sessionID, coachID int64
		var date, hour, customer, offering string
		var number int
		var fee float64
		if err := rows.Scan(&sessionID, &coachID, &date, &hour, &customer, &offering, &number, &fee); err != nil {
			rows.Close()
			return nil, err
		}
		earning := byUser[coachID]
		if earning == nil {
			earning = &oneToOnePayrollEarning{UserID: coachID, Snapshot: payrollCalculatedSnapshot{
				Status: PayrollPaymentStatusCalculated,
				Notes:  "Coach fees from completed 1-to-1 sessions within the salary period.",
			}}
			byUser[coachID] = earning
			ordered = append(ordered, coachID)
		}
		amount := normalizeMoney(fee)
		earning.Snapshot.Quantity++
		earning.Snapshot.BaseAmount = normalizeMoney(earning.Snapshot.BaseAmount + amount)
		earning.Snapshot.Details = append(earning.Snapshot.Details, PayrollPaymentCalculationDetail{
			DetailType: payrollDetailTypeSummary, SourceType: payrollSourceOneToOneSession, SourceID: sessionID,
			Label:      fmt.Sprintf("%s %s · %s · session %d", date, hour, offering, number),
			DetailNote: fmt.Sprintf("Customer: %s. Assigned coach fee: %.2f.", customer, amount),
			Quantity:   1, RateSnapshot: amount, AmountSnapshot: amount,
		})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ordered) == 0 {
		return nil, nil
	}
	divisionID, err := divisionIDByCode(a.db, divisionCodeSports)
	if err != nil {
		return nil, err
	}
	earnings := make([]oneToOnePayrollEarning, 0, len(ordered))
	for _, id := range ordered {
		earning := byUser[id]
		earning.DivisionID = divisionID
		earning.Snapshot.QuantityLabel = fmt.Sprintf("%.0f completed 1-to-1 sessions", earning.Snapshot.Quantity)
		earnings = append(earnings, *earning)
	}
	return earnings, nil
}

func (a *App) syncOneToOnePayrollEarningsTx(tx *sql.Tx, run PayrollRun, earnings []oneToOnePayrollEarning, selectedUserID, actorID int64, allowExisting bool) error {
	rows, err := a.queryTxDB(tx, `SELECT id, user_id, status, COALESCE(finance_transaction_id, 0) FROM payroll_payments
 WHERE payroll_run_id = ? AND earning_source = 'one_to_one' AND (? = 0 OR user_id = ?)`,
		run.ID, selectedUserID, selectedUserID)
	if err != nil {
		return err
	}
	existing := map[int64]int64{}
	terminalVoid := map[int64]bool{}
	for rows.Next() {
		var id, userID, financeID int64
		var status string
		if err := rows.Scan(&id, &userID, &status, &financeID); err != nil {
			rows.Close()
			return err
		}
		if status == PayrollPaymentStatusApproved || status == PayrollPaymentStatusPaid {
			rows.Close()
			return errors.New("approved or paid coaching earnings cannot be recalculated")
		}
		existing[userID] = id
		terminalVoid[userID] = status == PayrollPaymentStatusVoid && financeID > 0
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(existing) > 0 && !allowExisting {
		return errors.New("coaching salary has already been generated")
	}
	now := time.Now().UTC()
	seen := map[int64]bool{}
	for _, earning := range earnings {
		if terminalVoid[earning.UserID] {
			continue
		}
		snapshot := earning.Snapshot
		paymentID := existing[earning.UserID]
		// Lock in source order so simultaneous generation cannot claim the same session.
		sourceIDs := make([]int64, 0, len(snapshot.Details))
		for _, detail := range snapshot.Details {
			sourceIDs = append(sourceIDs, detail.SourceID)
		}
		sort.Slice(sourceIDs, func(i, j int) bool { return sourceIDs[i] < sourceIDs[j] })
		for _, sourceID := range sourceIDs {
			if a.runtimeConfig.DBDriver == databaseDriverPostgres {
				var locked int64
				if err := a.queryRowTxDB(tx, "SELECT id FROM one_to_one_booking_sessions WHERE id = ? FOR UPDATE", sourceID).Scan(&locked); err != nil {
					return err
				}
			}
			var claims int
			if err := a.queryRowTxDB(tx, `SELECT COUNT(*) FROM payroll_payment_calculation_details d
    JOIN payroll_payments pp ON pp.id = d.payroll_payment_id
    WHERE d.source_type = 'one_to_one_booking_session' AND d.source_id = ?
    AND pp.status <> 'void' AND pp.id <> ?`, sourceID, paymentID).Scan(&claims); err != nil {
				return err
			}
			if claims > 0 {
				return errors.New("a 1-to-1 session is already included in another salary; regenerate to refresh the available sessions")
			}
		}
		if paymentID > 0 {
			_, err = a.execTxDB(tx, `UPDATE payroll_payments SET division_id = ?, quantity = ?, quantity_label = ?,
    base_amount = ?, status = 'calculated', notes = ?, updated_at = ? WHERE id = ?`,
				earning.DivisionID, snapshot.Quantity, snapshot.QuantityLabel, snapshot.BaseAmount, snapshot.Notes, now, paymentID)
		} else {
			periodLabel := run.GenerationLabel
			if periodLabel == "" {
				if err := a.queryRowTxDB(tx, `SELECT COALESCE(MAX(NULLIF(period_label, '')), ?) FROM payroll_payments WHERE payroll_run_id = ? AND user_id = ?`, run.Label, run.ID, earning.UserID).Scan(&periodLabel); err != nil {
					return err
				}
			}
			paymentID, err = a.insertAndReturnIDTx(tx, `INSERT INTO payroll_payments
    (payroll_run_id, user_id, division_id, compensation_type, earning_source, period_label,
     quantity, quantity_label, base_amount, net_amount, status, notes, created_at, updated_at)
    VALUES (?, ?, ?, 'per_session', 'one_to_one', ?, ?, ?, ?, ?, 'calculated', ?, ?, ?)`,
				run.ID, earning.UserID, earning.DivisionID, periodLabel, snapshot.Quantity, snapshot.QuantityLabel,
				snapshot.BaseAmount, snapshot.BaseAmount, snapshot.Notes, now, now)
			if err == nil {
				err = applyStaffAdvanceDeductionsTx(a, tx, paymentID, earning.UserID, snapshot.BaseAmount, actorID)
			}
		}
		if err != nil {
			return err
		}
		if err := replacePayrollPaymentCalculationDetailsTx(a, tx, paymentID, snapshot.Details); err != nil {
			return err
		}
		if err := recalculatePayrollPaymentTx(a, tx, paymentID); err != nil {
			return err
		}
		seen[earning.UserID] = true
	}
	for userID, paymentID := range existing {
		if seen[userID] || terminalVoid[userID] {
			continue
		}
		// Keep the record and its previous snapshot for audit when source sessions no longer qualify.
		if _, err := a.execTxDB(tx, `UPDATE payroll_payments SET status = 'void', updated_at = ?
   WHERE id = ?`, now, paymentID); err != nil {
			return err
		}
	}
	return nil
}
