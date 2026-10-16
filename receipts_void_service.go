package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// voidReceiptFromSource delegates receipt voiding to the original
// payment workflow. Source-specific services are responsible for
// keeping their business records and finance ledger synchronized.
func (a *App) voidReceiptFromSource(
	receipt *FinanceTransaction,
	reason string,
	actorUserID int64,
) error {
	if receipt == nil || receipt.ID <= 0 {
		return errors.New("invalid receipt")
	}

	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}

	if receipt.Voided {
		return errors.New("receipt has already been voided")
	}

	if actorUserID <= 0 {
		return errors.New("authenticated administrator is required")
	}

	if financeTransactionAllowsGeneralVoid(receipt) {
		return errors.New("standalone receipts must use the general finance void workflow")
	}

	if receipt.SourceID <= 0 && receipt.SourceType != "finance_transfer" {
		return errors.New("receipt has no valid source reference")
	}

	if err := a.verifyReceiptSourceOwnership(receipt); err != nil {
		return err
	}

	switch receipt.SourceType {
	case "student_monthly_payment":
		return a.voidStudentMonthlyPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "admission":
		return a.voidAdmissionPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "student_enrollment":
		return a.voidEnrollmentAdmissionPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "booking_payment_collection":
		return a.voidBookingPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "booking_referral_payment":
		return a.voidReferralCommissionPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "mcp_payment_collection":
		return a.voidMCPPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "payroll_payment":
		return a.voidPayrollPaymentExpected(receipt.SourceID, receipt.ID, reason, actorUserID)

	case "finance_transfer":
		if strings.TrimSpace(receipt.TransferGroupID) == "" {
			return errors.New("receipt has no transfer group")
		}
		return a.voidFinanceTransferGroupExpected(
			receipt.TransferGroupID, receipt.ID, reason, actorUserID,
		)

	case "tournament_sponsorship":
		_, err := a.voidTournamentFinanceRecordExpected(
			"sponsorship", receipt.SourceID, receipt.ID, reason, actorUserID,
		)
		return err

	case "tournament_official_payment":
		_, err := a.voidTournamentFinanceRecordExpected(
			"official_payment", receipt.SourceID, receipt.ID, reason, actorUserID,
		)
		return err

	case "tournament_expense":
		_, err := a.voidTournamentFinanceRecordExpected(
			"expense", receipt.SourceID, receipt.ID, reason, actorUserID,
		)
		return err

	default:
		return fmt.Errorf(
			"receipt source %q does not support centralized voiding",
			receipt.SourceType,
		)
	}
}

func (a *App) verifyReceiptSourceOwnership(receipt *FinanceTransaction) error {
	if receipt == nil || receipt.ID <= 0 {
		return errors.New("invalid receipt")
	}

	var query string

	switch receipt.SourceType {
	case "student_monthly_payment":
		query = "SELECT finance_transaction_id FROM student_monthly_payments WHERE id = ?"

	case "admission":
		query = "SELECT finance_transaction_id FROM admissions WHERE id = ?"

	case "student_enrollment":
		query = "SELECT finance_transaction_id FROM student_enrollments WHERE id = ?"

	case "booking_payment_collection":
		query = "SELECT finance_transaction_id FROM booking_payment_collections WHERE id = ?"

	case "booking_referral_payment":
		query = "SELECT finance_transaction_id FROM booking_referrals WHERE id = ?"

	case "mcp_payment_collection":
		query = "SELECT finance_transaction_id FROM mcp_payment_collections WHERE id = ?"

	case "payroll_payment":
		query = "SELECT finance_transaction_id FROM payroll_payments WHERE id = ?"

	case "tournament_sponsorship":
		query = "SELECT finance_transaction_id FROM tournament_sponsorships WHERE id = ?"

	case "tournament_official_payment":
		query = "SELECT finance_transaction_id FROM tournament_official_payments WHERE id = ?"

	case "tournament_expense":
		query = "SELECT finance_transaction_id FROM tournament_expenses WHERE id = ?"

	case "finance_transfer":
		if strings.TrimSpace(receipt.TransferGroupID) == "" {
			return errors.New("transfer group is missing")
		}

		var count int
		err := a.queryRowDB(
			"SELECT COUNT(*) FROM finance_transactions WHERE id = ? AND transfer_group_id = ? AND source_type = 'finance_transfer'",
			receipt.ID,
			receipt.TransferGroupID,
		).Scan(&count)

		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("receipt transfer ownership mismatch")
		}
		return nil

	default:
		return fmt.Errorf("unsupported receipt source: %s", receipt.SourceType)
	}

	var linkedID sql.NullInt64
	err := a.queryRowDB(query, receipt.SourceID).Scan(&linkedID)

	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("receipt source record was not found")
	}
	if err != nil {
		return err
	}

	if !linkedID.Valid || linkedID.Int64 != receipt.ID {
		return errors.New("receipt does not match its original payment record")
	}

	return nil
}

// receiptSourceVoidAuthorized requires the original workflow's
// deletion permission in addition to the receipt endpoint's
// finance_transactions.delete permission.
func receiptSourceVoidAuthorized(user *User, receipt *FinanceTransaction) bool {
	if user == nil || receipt == nil {
		return false
	}

	if !financeHighRiskAuthorized(user, "finance_transactions.delete") {
		return false
	}

	switch receipt.SourceType {
	case "student_monthly_payment", "admission", "student_enrollment":
		return containsPermission(user.Permissions, "student_payments.delete")

	case "payroll_payment":
		return containsPermission(user.Permissions, "payroll.delete")

	case "finance_transfer":
		return containsPermission(user.Permissions, "finance_transfers.delete")

	case "booking_referral_payment":
		return userCanAccessSportsSilo(user) &&
			containsPermission(user.Permissions, "referrals.delete")

	case "booking_payment_collection",
		"mcp_payment_collection",
		"tournament_sponsorship",
		"tournament_official_payment",
		"tournament_expense":
		return userCanAccessSportsSilo(user)

	default:
		return false
	}
}
