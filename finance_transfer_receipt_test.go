package main

import (
	"strings"
	"testing"
	"time"
)

func TestFinanceTransfersAllowRepeatedExternalReferences(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	if _, err := app.createManualFinanceTransaction(
		"manual_income",
		"Transfer test funding",
		"Seed cash balance",
		"cash",
		10000,
		time.Now(),
		0,
	); err != nil {
		t.Fatalf("seed cash balance: %v", err)
	}

	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)

	reference := "REUSED-BANK-REFERENCE"
	now := time.Now()

	firstGroup, err := app.createFinanceTransfer(
		cashID, bankID, 1000, now,
		reference, "First transfer", "", 0,
	)
	if err != nil {
		t.Fatalf("first transfer: %v", err)
	}

	secondGroup, err := app.createFinanceTransfer(
		cashID, bankID, 1500, now,
		reference, "Second transfer", "", 0,
	)
	if err != nil {
		t.Fatalf("second transfer using same reference: %v", err)
	}

	if firstGroup == secondGroup {
		t.Fatal("distinct transfers received identical group IDs")
	}

	rows, err := app.listFinanceTransactions()
	if err != nil {
		t.Fatalf("list finance transactions: %v", err)
	}

	groups := map[string]bool{
		firstGroup:  true,
		secondGroup: true,
	}

	receipts := make(map[string]bool)
	directions := make(map[string]map[string]bool)
	found := 0

	for _, row := range rows {
		if !groups[row.TransferGroupID] {
			continue
		}

		found++

		if row.ReferenceNumber != reference {
			t.Errorf(
				"reference = %q, want %q",
				row.ReferenceNumber,
				reference,
			)
		}

		if !strings.HasPrefix(row.ReceiptNumber, "MKM-TRF-") {
			t.Errorf("unexpected receipt number: %q", row.ReceiptNumber)
		}

		if receipts[row.ReceiptNumber] {
			t.Errorf("duplicate receipt number: %q", row.ReceiptNumber)
		}
		receipts[row.ReceiptNumber] = true

		if directions[row.TransferGroupID] == nil {
			directions[row.TransferGroupID] = make(map[string]bool)
		}
		directions[row.TransferGroupID][row.TransactionType] = true
	}

	if found != 4 {
		t.Fatalf("transfer transaction count = %d, want 4", found)
	}

	for group := range groups {
		if !directions[group][financeTxnTypeTransferOut] ||
			!directions[group][financeTxnTypeTransferIn] {
			t.Errorf("transfer group %q is missing a direction", group)
		}
	}

	if balance := financeAccountBalanceByName(
		t, app, financeAccountCashInHand,
	); balance != 7500 {
		t.Errorf("cash balance = %.2f, want 7500", balance)
	}

	if balance := financeAccountBalanceByName(
		t, app, financeAccountMainBank,
	); balance != 2500 {
		t.Errorf("bank balance = %.2f, want 2500", balance)
	}
}
