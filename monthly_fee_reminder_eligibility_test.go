package main

import (
	"testing"
	"time"
)

func TestMonthlyFeeReminderBillingRules(t *testing.T) {
	tests := []struct {
		name        string
		monthlyFee  float64
		collected   float64
		discount    float64
		wantBalance float64
	}{
		{"unpaid", 3000, 0, 0, 3000},
		{"fully paid", 3000, 3000, 0, 0},
		{"partially paid", 3000, 1000, 0, 2000},
		{"discounted", 3000, 0, 500, 2500},
		{"overpaid", 3000, 3500, 0, 0},
		{"exempt", 0, 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			balance := normalizeMoney(
				tt.monthlyFee - tt.collected - tt.discount,
			)
			if balance < 0 {
				balance = 0
			}

			if balance != tt.wantBalance {
				t.Fatalf(
					"balance = %.2f, want %.2f",
					balance,
					tt.wantBalance,
				)
			}
		})
	}
}

func TestMonthlyFeeReminderExemption(t *testing.T) {
	admission := Admission{
		FreeMonthlyFee: true,
	}

	if fee := effectiveMonthlyFee(admission, 3000); fee != 0 {
		t.Fatalf("exempt student fee = %.2f, want 0", fee)
	}
}

func TestMonthlyFeeReminderFirstMonthProration(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	fee, discount := applyFirstMonthEnrollmentDiscount(
		3000, start, "2026-08", 31,
	)

	if fee != 3000 || discount != 0 {
		t.Fatalf(
			"first month fee = %.2f, discount = %.2f",
			fee, discount,
		)
	}
}
