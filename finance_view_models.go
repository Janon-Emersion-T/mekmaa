package main

func pendingStudentPaymentRows(rows []StudentPaymentRow) []StudentPaymentRow {
	filtered := make([]StudentPaymentRow, 0, len(rows))
	for _, row := range rows {
		if row.MonthlyFee <= 0 {
			continue
		}
		if row.CollectedAmount+0.004 >= row.MonthlyFee {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func financeAccountsWithBalances(accounts []FinanceAccount, transactions []FinanceTransaction, reconciliations []CashReconciliation) []FinanceAccount {
	if len(accounts) == 0 {
		return accounts
	}
	balances := make(map[int64]float64, len(accounts))
	for _, transaction := range transactions {
		if transaction.Voided {
			continue
		}
		balances[transaction.FinanceAccountID] = normalizeMoney(balances[transaction.FinanceAccountID] + transaction.Amount)
	}
	lastByAccount := make(map[int64]CashReconciliation, len(reconciliations))
	for _, item := range reconciliations {
		if item.Voided {
			continue
		}
		current, exists := lastByAccount[item.FinanceAccountID]
		if !exists || item.ReconciliationDate > current.ReconciliationDate || (item.ReconciliationDate == current.ReconciliationDate && item.ID > current.ID) {
			lastByAccount[item.FinanceAccountID] = item
		}
	}
	enriched := make([]FinanceAccount, len(accounts))
	copy(enriched, accounts)
	for i := range enriched {
		enriched[i].CurrentBalance = balances[enriched[i].ID]
		if item, ok := lastByAccount[enriched[i].ID]; ok {
			enriched[i].LastAuditDate = item.ReconciliationDate
			enriched[i].LastAuditStatus = item.Status
			enriched[i].LastCashDelta = item.Difference
			enriched[i].LastCountedCash = item.CountedBalance
		}
	}
	return enriched
}
