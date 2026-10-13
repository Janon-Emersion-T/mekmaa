package main

import (
	"errors"
	"math"
	"strings"
)

const (
	ExcessPaymentRetain   = "retain"
	ExcessPaymentRefund   = "refund"
	ExcessPaymentTransfer = "transfer"
)

type UnenrollmentFinancialDecision struct {
	FinalMonthlyFee float64
	CollectedAmount float64
	DiscountAmount  float64

	ExcessAction       string
	ExcessReason       string
	TargetEnrollmentID int64
}

func (d UnenrollmentFinancialDecision) Validate() error {
	for _, amount := range []float64{
		d.FinalMonthlyFee,
		d.CollectedAmount,
		d.DiscountAmount,
	} {
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return errors.New("financial amounts must be finite and non-negative")
		}
	}

	if d.FinalMonthlyFee > 999999999 {
		return errors.New("final monthly fee exceeds the allowed maximum")
	}

	if d.ExcessAmount() <= 0 {
		if strings.TrimSpace(d.ExcessAction) != "" {
			return errors.New("excess payment action is not applicable")
		}
		return nil
	}

	switch d.ExcessAction {
	case ExcessPaymentRetain:
		if strings.TrimSpace(d.ExcessReason) == "" {
			return errors.New("reason is required to retain excess payment")
		}
	case ExcessPaymentRefund:
	case ExcessPaymentTransfer:
		if d.TargetEnrollmentID <= 0 {
			return errors.New("select another enrollment for excess payment")
		}
	default:
		return errors.New("select how to handle the excess payment")
	}

	return nil
}

func (d UnenrollmentFinancialDecision) PayableAmount() float64 {
	payable := normalizeMoney(d.FinalMonthlyFee - d.DiscountAmount)
	if payable < 0 {
		return 0
	}
	return payable
}

func (d UnenrollmentFinancialDecision) ExcessAmount() float64 {
	// Only actual collected cash can become refundable excess.
	excess := normalizeMoney(d.CollectedAmount - d.PayableAmount())
	if excess < 0 {
		return 0
	}
	return excess
}
