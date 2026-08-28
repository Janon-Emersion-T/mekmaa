package main

import (
	"sort"
	"strings"
)

func pendingStudentPaymentRows(rows []StudentPaymentRow) []StudentPaymentRow {
	filtered := make([]StudentPaymentRow, 0, len(rows))
	for _, row := range rows {
		if studentPaymentRowStatus(row) == "free" || studentPaymentRowStatus(row) == "unconfigured" {
			continue
		}
		if row.CollectedAmount+0.004 >= row.MonthlyFee {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func studentPaymentRowStatus(row StudentPaymentRow) string {
	settledAmount := row.CollectedAmount + row.DiscountAmount
	if settledAmount > 0 && settledAmount+0.004 >= row.MonthlyFee {
		return "paid"
	}
	if settledAmount > 0 {
		return "partial"
	}
	if row.Enrollment.FreeMonthlyFee || (row.MonthlyFee == 0 && row.LeaveDays > 0) {
		return "free"
	}
	if row.MonthlyFee == 0 {
		return "unconfigured"
	}
	return "pending"
}

func studentPaymentRowMatchesMethod(row StudentPaymentRow, method string) bool {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "", "all":
		return true
	case "unpaid":
		return row.CollectedAmount <= 0
	case "cash", "bank_transfer", "qr_pay":
		for _, payment := range row.Payments {
			if strings.EqualFold(strings.TrimSpace(payment.PaymentMethod), method) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func filterStudentPaymentRows(rows []StudentPaymentRow, search string, status string, program string, method string) []StudentPaymentRow {
	search = strings.ToLower(strings.TrimSpace(search))
	status = strings.ToLower(strings.TrimSpace(status))
	program = strings.ToLower(strings.TrimSpace(program))

	filtered := make([]StudentPaymentRow, 0, len(rows))
	for _, row := range rows {
		if search != "" {
			haystack := strings.ToLower(strings.TrimSpace(strings.Join([]string{
				row.Admission.FullName,
				row.Admission.StudentID,
				row.Enrollment.TrainingProgramName,
				row.Enrollment.DivisionName,
			}, " ")))
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		if status != "" && status != "all" && studentPaymentRowStatus(row) != status {
			continue
		}
		if program != "" && program != "all" && strings.ToLower(strings.TrimSpace(row.Enrollment.TrainingProgramName)) != program {
			continue
		}
		if !studentPaymentRowMatchesMethod(row, method) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func studentPaymentProgramOptions(rows []StudentPaymentRow) []string {
	seen := make(map[string]struct{}, len(rows))
	options := make([]string, 0)
	for _, row := range rows {
		name := strings.TrimSpace(row.Enrollment.TrainingProgramName)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, name)
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i]) < strings.ToLower(options[j])
	})
	return options
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
