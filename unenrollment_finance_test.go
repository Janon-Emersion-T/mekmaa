package main

import "testing"

func TestUnenrollmentFinanceAllowsZeroFinalFee(t *testing.T) {
	d := UnenrollmentFinancialDecision{}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnenrollmentFinanceDetectsExcess(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 3000,
		ExcessAction:    ExcessPaymentRetain,
		ExcessReason:    "Approved additional income",
	}
	if got := d.ExcessAmount(); got != 1500 {
		t.Fatalf("excess = %.2f, want 1500", got)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnenrollmentFinanceRequiresRetainReason(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 3000,
		ExcessAction:    ExcessPaymentRetain,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected missing reason to be rejected")
	}
}

func TestUnenrollmentFinanceRequiresTransferTarget(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 3000,
		ExcessAction:    ExcessPaymentTransfer,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected missing target enrollment to be rejected")
	}
}

func TestUnenrollmentFinanceRejectsNegativeFee(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: -100,
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected negative fee to be rejected")
	}
}

func TestUnenrollmentFinanceDiscountReducesPayable(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 1200,
		DiscountAmount:  500,
		ExcessAction:    ExcessPaymentRefund,
	}

	if got := d.ExcessAmount(); got != 200 {
		t.Fatalf("excess = %.2f, want 200", got)
	}

	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnenrollmentFinanceDiscountAloneIsNotRefundable(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 0,
		DiscountAmount:  2000,
	}

	if got := d.ExcessAmount(); got != 0 {
		t.Fatalf("excess = %.2f, want 0", got)
	}
}

func TestUnenrollmentFinanceRefundUsesCashAfterDiscount(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		CollectedAmount: 3000,
		DiscountAmount:  500,
		ExcessAction:    ExcessPaymentRefund,
	}

	if got := d.ExcessAmount(); got != 2000 {
		t.Fatalf("excess = %.2f, want 2000", got)
	}

	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnenrollmentFinanceDiscountCannotCreateNegativePayable(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 500,
		CollectedAmount: 300,
		DiscountAmount:  1000,
		ExcessAction:    ExcessPaymentRetain,
		ExcessReason:    "Approved additional income",
	}

	if got := d.ExcessAmount(); got != 300 {
		t.Fatalf("excess = %.2f, want 300", got)
	}
}

func TestUnenrollmentFinancePayableAfterDiscount(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 1500,
		DiscountAmount:  500,
	}
	if got := d.PayableAmount(); got != 1000 {
		t.Fatalf("payable = %.2f, want 1000", got)
	}
}

func TestUnenrollmentFinanceExcessNeverExceedsCollectedCash(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 500,
		DiscountAmount:  1000,
		CollectedAmount: 300,
	}
	if got := d.ExcessAmount(); got != 300 {
		t.Fatalf("excess = %.2f, want 300", got)
	}
}

func TestUnenrollmentFinanceNoExcessWithPartialPayment(t *testing.T) {
	d := UnenrollmentFinancialDecision{
		FinalMonthlyFee: 2000,
		DiscountAmount:  250,
		CollectedAmount: 1000,
	}
	if got := d.ExcessAmount(); got != 0 {
		t.Fatalf("excess = %.2f, want 0", got)
	}
}
