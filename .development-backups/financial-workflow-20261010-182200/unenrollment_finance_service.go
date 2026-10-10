package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EnrollmentFinancialClosureRequest struct {
	EnrollmentID     int64
	EffectiveDate    string
	FinalMonthlyFee  float64
	RecordedByUserID int64
}

func (a *App) closeStudentEnrollmentFinancially(
	request EnrollmentFinancialClosureRequest,
) error {
	if request.EnrollmentID <= 0 {
		return errors.New("select a valid enrollment")
	}

	effectiveDate := strings.TrimSpace(request.EffectiveDate)
	parsedDate, err := time.Parse("2006-01-02", effectiveDate)
	if err != nil || parsedDate.Format("2006-01-02") != effectiveDate {
		return errors.New("enter a valid unenrollment date")
	}

	if err := validateHistoricalEntryDateValue(
		effectiveDate,
		"unenrollment date",
	); err != nil {
		return err
	}

	if effectiveDate > time.Now().Format("2006-01-02") {
		return errors.New("unenrollment date cannot be in the future")
	}

	decision := UnenrollmentFinancialDecision{
		FinalMonthlyFee: request.FinalMonthlyFee,
	}
	if err := decision.Validate(); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(
		tx,
		a.runtimeConfig.DBDriver,
		request.EnrollmentID,
	)
	if err != nil {
		return err
	}

	if !enrollment.Active {
		return errors.New("enrollment is already archived")
	}

	if enrollment.EnrollmentDate != "" &&
		effectiveDate < enrollment.EnrollmentDate {
		return errors.New(
			"unenrollment date cannot be before enrollment date",
		)
	}

	paymentMonth := effectiveDate[:7]

	var collectedAmount float64
	var discountAmount float64

	err = a.queryRowTxDB(tx, `
		SELECT
			COALESCE(SUM(amount), 0),
			COALESCE(SUM(discount_amount), 0)
		FROM student_monthly_payments
		WHERE enrollment_id = ?
		  AND payment_month = ?
		  AND COALESCE(voided, 0) = 0
	`, request.EnrollmentID, paymentMonth).Scan(
		&collectedAmount,
		&discountAmount,
	)
	if err != nil {
		return fmt.Errorf("load existing payments: %w", err)
	}

	decision.CollectedAmount = normalizeMoney(collectedAmount)
	decision.DiscountAmount = normalizeMoney(discountAmount)

	if decision.ExcessAmount() > 0.004 {
		return errors.New(
			"excess payment requires a financial resolution; " +
				"unenrollment has not been saved",
		)
	}

	if err := decision.Validate(); err != nil {
		return err
	}

	now := time.Now().UTC()

	_, err = a.execTxDB(tx, `
		INSERT INTO enrollment_financial_closures (
			enrollment_id,
			effective_date,
			final_monthly_fee,
			collected_amount,
			discount_amount,
			excess_amount,
			excess_action,
			excess_reason,
			recorded_by_user_id,
			created_at
		)
		VALUES (?, ?, ?, ?, ?, 0, '', '', ?, ?)
	`,
		request.EnrollmentID,
		effectiveDate,
		normalizeMoney(request.FinalMonthlyFee),
		decision.CollectedAmount,
		decision.DiscountAmount,
		nullIfZero(request.RecordedByUserID),
		now,
	)
	if err != nil {
		return fmt.Errorf("record financial closure: %w", err)
	}

	result, err := a.execTxDB(tx, `
		UPDATE student_enrollments
		SET active = 0,
		    updated_at = ?
		WHERE id = ?
		  AND COALESCE(active, 1) = 1
	`, now, request.EnrollmentID)
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

	if err := syncStudentEnrollmentStatusHistoryTx(
		a,
		tx,
		request.EnrollmentID,
		false,
		effectiveDate,
	); err != nil {
		return err
	}

	return tx.Commit()
}
