package main

import (
	"strings"
	"testing"
)

func TestReceiptVoidStudentMonthlyPaymentExpectedTransaction(t *testing.T) {
	tests := []struct {
		name             string
		expectedIDOffset int64
		useOriginal      bool
		shouldReject     bool
	}{
		{
			name:             "matching receipt ID",
			expectedIDOffset: 0,
		},
		{
			name:             "mismatched receipt ID",
			expectedIDOffset: 1000000,
			shouldReject:     true,
		},
		{
			name:        "original payment workflow",
			useOriginal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newBookingWorkflowTestApp(t)
			userID := int64(9)

			if _, err := app.db.Exec(
				`UPDATE admission_pricing
				 SET price = 1500, monthly_fee = 3200
				 WHERE practice_type = 'group_practice'`,
			); err != nil {
				t.Fatalf("configure admission pricing: %v", err)
			}

			admission := Admission{
				StudentID:             "STD-RECEIPT-VOID-001",
				FullName:              "Receipt Void Test Student",
				AdmissionDate:         "2026-07-15",
				DateOfBirth:           "2011-01-20",
				Gender:                "male",
				PracticeType:          "group_practice",
				Address:               "Jaffna",
				GuardianName:          "Test Guardian",
				GuardianRelationship:  "Parent",
				GuardianContactNumber: "0770000002",
			}

			admissionID, _, err := app.createAdmissionWithOptionalPayment(
				admission, true, "cash", userID,
			)
			if err != nil {
				t.Fatalf("create admission: %v", err)
			}

			monthDate, _ := parsePaymentMonth("2026-08")
			transactionID, err := app.collectStudentMonthlyPayment(
				admissionID, "2026-08", monthDate, "cash", userID,
			)
			if err != nil {
				t.Fatalf("collect monthly payment: %v", err)
			}

			var paymentID int64
			if err := app.db.QueryRow(
				`SELECT id FROM student_monthly_payments
				 WHERE finance_transaction_id = ?`,
				transactionID,
			).Scan(&paymentID); err != nil {
				t.Fatalf("find payment: %v", err)
			}

			if tt.useOriginal {
				err = app.voidStudentMonthlyPayment(
					paymentID, "regression test", userID,
				)
			} else {
				err = app.voidStudentMonthlyPaymentExpected(
					paymentID,
					transactionID+tt.expectedIDOffset,
					"regression test",
					userID,
				)
			}

			if tt.shouldReject {
				if err == nil || !strings.Contains(err.Error(), "ownership changed") {
					t.Fatalf("expected ownership mismatch, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("void monthly payment: %v", err)
			}

			transaction, err := app.findFinanceTransactionByID(transactionID)
			if err != nil {
				t.Fatalf("reload transaction: %v", err)
			}

			if transaction.Voided == tt.shouldReject {
				t.Fatalf(
					"unexpected ledger void state: voided=%v rejected=%v",
					transaction.Voided,
					tt.shouldReject,
				)
			}

			var paymentVoided int
			if err := app.db.QueryRow(
				`SELECT voided FROM student_monthly_payments WHERE id = ?`,
				paymentID,
			).Scan(&paymentVoided); err != nil {
				t.Fatalf("reload payment: %v", err)
			}

			expectedVoided := 1
			if tt.shouldReject {
				expectedVoided = 0
			}
			if paymentVoided != expectedVoided {
				t.Fatalf(
					"payment voided=%d, expected=%d",
					paymentVoided,
					expectedVoided,
				)
			}
		})
	}
}

func TestReceiptVoidBookingPaymentExpectedTransaction(t *testing.T) {
	tests := []struct {
		name         string
		mismatch     bool
		useOriginal  bool
		shouldReject bool
	}{
		{name: "matching receipt ID"},
		{name: "mismatched receipt ID", mismatch: true, shouldReject: true},
		{name: "original booking workflow", useOriginal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newBookingWorkflowTestApp(t)
			userID := int64(9)

			scheduleID := createConfirmedFutureBooking(t, app, 1, "10:00")

			transactionID, err := app.collectBookingPayment(
				scheduleID, "cash", 2500, "", userID, false,
			)
			if err != nil {
				t.Fatalf("collect booking payment: %v", err)
			}

			var collectionID int64
			if err := app.db.QueryRow(
				`SELECT id FROM booking_payment_collections
				 WHERE finance_transaction_id = ?`,
				transactionID,
			).Scan(&collectionID); err != nil {
				t.Fatalf("find booking collection: %v", err)
			}

			if tt.useOriginal {
				err = app.voidBookingPayment(
					collectionID, "booking regression test", userID,
				)
			} else {
				expectedID := transactionID
				if tt.mismatch {
					expectedID += 1000000
				}

				err = app.voidBookingPaymentExpected(
					collectionID, expectedID, "booking regression test", userID,
				)
			}

			if tt.shouldReject {
				if err == nil || !strings.Contains(err.Error(), "ownership changed") {
					t.Fatalf("expected ownership mismatch, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("void booking payment: %v", err)
			}

			transaction, err := app.findFinanceTransactionByID(transactionID)
			if err != nil {
				t.Fatalf("reload finance transaction: %v", err)
			}

			expectedVoided := !tt.shouldReject
			if transaction.Voided != expectedVoided {
				t.Fatalf(
					"ledger voided=%v, expected=%v",
					transaction.Voided, expectedVoided,
				)
			}

			var collectionVoided int
			if err := app.db.QueryRow(
				`SELECT voided FROM booking_payment_collections WHERE id = ?`,
				collectionID,
			).Scan(&collectionVoided); err != nil {
				t.Fatalf("reload booking collection: %v", err)
			}

			expectedCollectionVoided := 1
			if tt.shouldReject {
				expectedCollectionVoided = 0
			}

			if collectionVoided != expectedCollectionVoided {
				t.Fatalf(
					"collection voided=%d, expected=%d",
					collectionVoided, expectedCollectionVoided,
				)
			}
		})
	}
}
