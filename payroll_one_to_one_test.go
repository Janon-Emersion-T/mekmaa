package main

import (
	"strings"
	"testing"
)

func createPayrollOneToOneSession(t *testing.T, app *App, coachID int64, date string, fee float64, status string) int64 {
	t.Helper()
	offeringID, err := app.createOneToOneOffering(OneToOneOffering{
		Name: "Payroll private coaching " + date + status, Game: "badminton", Audience: "local", Occurrence: "per_week", SessionCount: 4, Price: 4000, Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	offering, err := app.findOneToOneOfferingByID(offeringID)
	if err != nil {
		t.Fatal(err)
	}
	bookingID, _, err := app.createOneToOneBooking(*offering, "Coaching Customer", date, "18:00", 4, 4000, coachID, fee*4, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var sessionID int64
	if err := app.queryRowDB("SELECT id FROM one_to_one_booking_sessions WHERE booking_id = ?", bookingID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if status == "completed" {
		if err := app.completeOneToOneSession(sessionID, coachID); err != nil {
			t.Fatal(err)
		}
	} else if status != "scheduled" {
		if _, err := app.execDB("UPDATE one_to_one_booking_sessions SET status = ? WHERE id = ?", status, sessionID); err != nil {
			t.Fatal(err)
		}
	}
	return sessionID
}

func oneToOneSalaryPayment(t *testing.T, app *App, runID, userID int64) PayrollPayment {
	t.Helper()
	payments, err := app.listPayrollPaymentsForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, payment := range payments {
		if payment.UserID == userID && payment.EarningSource == payrollEarningOneToOne {
			return payment
		}
	}
	t.Fatalf("missing coaching salary for user %d", userID)
	return PayrollPayment{}
}

func TestOneToOneFeesAddedToSalaryAndRecalculatedOnce(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	coachID := createOneToOneTestCoach(t, app)
	other, err := app.createManagedUser("Other Coach", "other-fee@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{UserID: coachID, CompensationType: SalaryTypeMonthly, Rate: 1000, EffectiveFrom: "2026-01-01", Active: true}, coachID)
	first := createPayrollOneToOneSession(t, app, coachID, "2026-08-01", 200, "completed")
	second := createPayrollOneToOneSession(t, app, coachID, "2026-08-31", 350, "completed")
	createPayrollOneToOneSession(t, app, coachID, "2026-08-10", 400, "scheduled")
	createPayrollOneToOneSession(t, app, coachID, "2026-08-11", 500, "cancelled")
	createPayrollOneToOneSession(t, app, coachID, "2026-09-01", 600, "completed")
	createPayrollOneToOneSession(t, app, other.ID, "2026-08-12", 700, "completed")
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Combined salary", coachID, coachID)
	if err != nil {
		t.Fatal(err)
	}
	base := payrollPaymentForProfile(t, app, runID, profileID)
	coaching := oneToOneSalaryPayment(t, app, runID, coachID)
	if base.NetAmount != 1000 || coaching.BaseAmount != 550 || coaching.Quantity != 2 || len(coaching.CalculationDetails) != 2 {
		t.Fatalf("base=%+v coaching=%+v", base, coaching)
	}
	for i, sourceID := range []int64{first, second} {
		detail := coaching.CalculationDetails[i]
		if detail.SourceID != sourceID || detail.SourceType != payrollSourceOneToOneSession || !strings.Contains(detail.DetailNote, "Coaching Customer") {
			t.Fatalf("missing source detail: %+v", detail)
		}
	}
	if _, err := app.execDB("UPDATE one_to_one_booking_sessions SET coach_fee = 250 WHERE id = ?", first); err != nil {
		t.Fatal(err)
	}
	if err := app.addPayrollAdjustment(coaching.ID, PayrollAdjustmentBonus, PayrollDirectionAddition, "Coaching bonus", 25, coachID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := app.recalculatePayrollRun(runID, coachID); err != nil {
			t.Fatal(err)
		}
		got := oneToOneSalaryPayment(t, app, runID, coachID)
		if got.ID != coaching.ID || got.BaseAmount != 600 || got.NetAmount != 625 || len(got.CalculationDetails) != 2 {
			t.Fatalf("recalculation duplicated or lost fees: %+v", got)
		}
	}
	var otherCount int
	if err := app.queryRowDB("SELECT COUNT(*) FROM payroll_payments WHERE payroll_run_id = ? AND user_id = ?", runID, other.ID).Scan(&otherCount); err != nil || otherCount != 0 {
		t.Fatalf("recalculation generated unrelated staff: %d %v", otherCount, err)
	}
	if err := app.approvePayrollPayment(coaching.ID, coachID); err != nil {
		t.Fatal(err)
	}
	if err := app.recalculatePayrollRun(runID, coachID); err == nil {
		t.Fatal("approved fees were recalculated")
	}
}

func TestOneToOneOnlyCoachCanGenerateAndPay(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	coachID := createOneToOneTestCoach(t, app)
	sourceID := createPayrollOneToOneSession(t, app, coachID, "2026-08-15", 750, "completed")
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Private coaching only", coachID, coachID)
	if err != nil {
		t.Fatal(err)
	}
	payment := oneToOneSalaryPayment(t, app, runID, coachID)
	if payment.SalaryProfileID != 0 || payment.NetAmount != 750 {
		t.Fatalf("wrong session-only salary: %+v", payment)
	}
	if err := app.approvePayrollPayment(payment.ID, coachID); err != nil {
		t.Fatal(err)
	}
	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := app.createFinanceAccount(sportsID, "", "Coach payroll bank", financeAccountTypeBank, "", coachID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.payPayrollPayment(payment.ID, accountID, "COACH-PAY", coachID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.generateStaffPayroll("2026-08-10", "2026-08-20", "Duplicate", coachID, coachID); err == nil {
		t.Fatal("paid session duplicated in overlapping period")
	}
	// Moving the source date must not make an already-paid session payable again.
	if _, err := app.execDB("UPDATE space_schedules SET slot_date = '2026-09-15' WHERE id = (SELECT schedule_id FROM one_to_one_booking_sessions WHERE id = ?)", sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.generateStaffPayroll("2026-09-01", "2026-09-30", "Already claimed source", coachID, coachID); err == nil {
		t.Fatal("paid source duplicated after date change")
	}
	paid, _, err := app.findPayrollPaymentByID(payment.ID)
	if err != nil || paid.CalculationDetails[0].AmountSnapshot != 750 || !strings.Contains(paid.CalculationDetails[0].Label, "2026-08-15") {
		t.Fatalf("paid snapshot changed: %+v, %v", paid, err)
	}
	if _, err := app.execDB("UPDATE space_schedules SET slot_date = '2026-08-15' WHERE id = (SELECT schedule_id FROM one_to_one_booking_sessions WHERE id = ?)", sourceID); err != nil {
		t.Fatal(err)
	}
	if err := app.voidPayrollPayment(payment.ID, "Payment reversed", coachID); err != nil {
		t.Fatal(err)
	}
	if err := app.recalculatePayrollRun(runID, coachID); err != nil {
		t.Fatal(err)
	}
	voided := oneToOneSalaryPayment(t, app, runID, coachID)
	if voided.Status != PayrollPaymentStatusVoid || voided.FinanceTransactionID == 0 {
		t.Fatalf("recalculation reopened a voided payment: %+v", voided)
	}

}

func TestOneToOneRecalculationDropsIneligibleSessions(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	coachID := createOneToOneTestCoach(t, app)
	sourceID := createPayrollOneToOneSession(t, app, coachID, "2026-08-15", 250, "completed")
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Coaching", coachID, coachID)
	if err != nil {
		t.Fatal(err)
	}
	original := oneToOneSalaryPayment(t, app, runID, coachID)
	if _, err := app.execDB("UPDATE one_to_one_booking_sessions SET status = 'cancelled' WHERE id = ?", sourceID); err != nil {
		t.Fatal(err)
	}
	if err := app.recalculatePayrollRun(runID, coachID); err != nil {
		t.Fatal(err)
	}
	payment := oneToOneSalaryPayment(t, app, runID, coachID)
	if payment.Status != PayrollPaymentStatusVoid {
		t.Fatalf("cancelled work remains payable: %+v", payment)
	}
	if _, err := app.execDB("UPDATE one_to_one_booking_sessions SET status = 'completed' WHERE id = ?", sourceID); err != nil {
		t.Fatal(err)
	}
	if err := app.recalculatePayrollRun(runID, coachID); err != nil {
		t.Fatal(err)
	}
	payment = oneToOneSalaryPayment(t, app, runID, coachID)
	if payment.ID != original.ID || payment.Status != PayrollPaymentStatusCalculated || payment.NetAmount != 250 {
		t.Fatalf("restored work duplicated: %+v", payment)
	}
}

func TestRefreshSelectedStaffAddsCoachingWithoutChangingOtherApprovals(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	coachID := createOneToOneTestCoach(t, app)
	other, err := app.createManagedUser("Approved Staff", "approved-other@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{coachID, other.ID} {
		createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{UserID: id, CompensationType: SalaryTypeMonthly, Rate: 1000, EffectiveFrom: "2026-01-01", Active: true}, coachID)
	}
	runID, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Original shared label", other.ID, coachID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.generateStaffPayroll("2026-08-01", "2026-08-31", "Coach custom label", coachID, coachID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.execDB("UPDATE payroll_payments SET status = 'approved' WHERE payroll_run_id = ? AND user_id = ?", runID, other.ID); err != nil {
		t.Fatal(err)
	}
	createPayrollOneToOneSession(t, app, coachID, "2026-08-20", 300, "completed")
	if err := app.recalculatePayrollRun(runID, coachID); err == nil {
		t.Fatal("whole period recalculation changed approved staff")
	}
	if err := app.recalculatePayrollRun(runID, coachID, coachID); err != nil {
		t.Fatal(err)
	}
	coaching := oneToOneSalaryPayment(t, app, runID, coachID)
	if coaching.NetAmount != 300 || coaching.PeriodLabel != "Coach custom label" {
		t.Fatalf("refresh lost staff label or fees: %+v", coaching)
	}
	var otherStatus string
	if err := app.queryRowDB("SELECT status FROM payroll_payments WHERE payroll_run_id = ? AND user_id = ?", runID, other.ID).Scan(&otherStatus); err != nil || otherStatus != "approved" {
		t.Fatalf("other approval changed: %s %v", otherStatus, err)
	}
	if err := app.recalculatePayrollRun(runID, coachID, 99999); err == nil {
		t.Fatal("refresh accepted staff without salary in this run")
	}
}
