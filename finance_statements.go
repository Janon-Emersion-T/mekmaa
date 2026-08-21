package main

import (
	"errors"
	"sort"
	"strings"
	"time"
)

func financeProfitAndLossPeriod(fromRaw, toRaw string, now time.Time) (time.Time, time.Time, time.Time, time.Time, string, string, string, string, error) {
	localNow := now.In(time.Local)
	defaultFrom := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, time.Local)
	defaultTo := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)

	fromDate := defaultFrom
	toDate := defaultTo
	var err error
	if strings.TrimSpace(fromRaw) != "" {
		fromDate, err = time.ParseInLocation("2006-01-02", strings.TrimSpace(fromRaw), time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, time.Time{}, time.Time{}, "", "", "", "", errors.New("a valid report start date is required")
		}
	}
	if strings.TrimSpace(toRaw) != "" {
		toDate, err = time.ParseInLocation("2006-01-02", strings.TrimSpace(toRaw), time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, time.Time{}, time.Time{}, "", "", "", "", errors.New("a valid report end date is required")
		}
	}
	if toDate.Before(fromDate) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, "", "", "", "", errors.New("report end date must be on or after the start date")
	}

	dayCount := int(toDate.Sub(fromDate).Hours()/24) + 1
	previousTo := fromDate.AddDate(0, 0, -1)
	previousFrom := previousTo.AddDate(0, 0, -(dayCount - 1))

	return fromDate, toDate, previousFrom, previousTo,
		fromDate.Format("2006-01-02"),
		toDate.Format("2006-01-02"),
		previousFrom.Format("2006-01-02"),
		previousTo.Format("2006-01-02"),
		nil
}

func financeBalanceSheetAsOfDate(asOfRaw string, now time.Time) (time.Time, string, error) {
	if strings.TrimSpace(asOfRaw) == "" {
		asOf := now.In(time.Local)
		asOf = time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.Local)
		return asOf, asOf.Format("2006-01-02"), nil
	}
	asOf, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(asOfRaw), time.Local)
	if err != nil {
		return time.Time{}, "", errors.New("a valid balance sheet date is required")
	}
	return asOf, asOf.Format("2006-01-02"), nil
}

func financeTransactionWithinLocalDates(recordedAt time.Time, fromDate, toDate time.Time) bool {
	local := recordedAt.In(time.Local)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	return !day.Before(fromDate) && !day.After(toDate)
}

func financeOperatingTransaction(transaction FinanceTransaction) bool {
	if !financeTransactionPosted(transaction) {
		return false
	}
	switch transaction.TransactionType {
	case financeTxnTypeTransferIn, financeTxnTypeTransferOut, financeTxnTypeOpeningBalance, financeTxnTypeAdjustment:
		return false
	default:
		return true
	}
}

func financeBuildStatementItems(amounts map[string]float64, comparisons map[string]float64, labels map[string]string, positiveOnly bool) []FinanceStatementItem {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := normalizeMoney(amounts[keys[i]])
		right := normalizeMoney(amounts[keys[j]])
		if left == right {
			return labels[keys[i]] < labels[keys[j]]
		}
		return left > right
	})

	items := make([]FinanceStatementItem, 0, len(keys))
	for _, key := range keys {
		amount := normalizeMoney(amounts[key])
		comparison := normalizeMoney(comparisons[key])
		if positiveOnly {
			amount = normalizeMoney(absMoney(amount))
			comparison = normalizeMoney(absMoney(comparison))
		}
		if moneyEquals(amount, 0) && moneyEquals(comparison, 0) {
			continue
		}
		items = append(items, FinanceStatementItem{
			Code:             key,
			Label:            labels[key],
			Amount:           amount,
			ComparisonAmount: comparison,
			Delta:            normalizeMoney(amount - comparison),
		})
	}
	return items
}

func absMoney(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func (a *App) buildFinanceProfitAndLoss(fromRaw, toRaw string, divisionIDs []int64) (*FinanceProfitAndLoss, error) {
	fromDate, toDate, previousFrom, previousTo, from, to, prevFrom, prevTo, err := financeProfitAndLossPeriod(fromRaw, toRaw, time.Now())
	if err != nil {
		return nil, err
	}
	filter := FinanceFilter{}
	if len(divisionIDs) > 0 {
		filter.DivisionIDs = append([]int64(nil), divisionIDs...)
	}
	transactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		return nil, err
	}

	report := &FinanceProfitAndLoss{
		From:         from,
		To:           to,
		PreviousFrom: prevFrom,
		PreviousTo:   prevTo,
	}
	revenueAmounts := map[string]float64{}
	revenueComparisons := map[string]float64{}
	revenueLabels := map[string]string{}
	expenseAmounts := map[string]float64{}
	expenseComparisons := map[string]float64{}
	expenseLabels := map[string]string{}
	otherAmounts := map[string]float64{}
	otherComparisons := map[string]float64{}
	otherLabels := map[string]string{}

	for _, transaction := range transactions {
		if !financeTransactionPosted(transaction) {
			continue
		}
		inCurrent := financeTransactionWithinLocalDates(transaction.RecordedAt, fromDate, toDate)
		inPrevious := financeTransactionWithinLocalDates(transaction.RecordedAt, previousFrom, previousTo)
		if !inCurrent && !inPrevious {
			continue
		}

		switch transaction.TransactionType {
		case financeTxnTypeTransferIn, financeTxnTypeTransferOut, financeTxnTypeOpeningBalance:
			continue
		case financeTxnTypeAdjustment:
			key := "cash_adjustment"
			otherLabels[key] = "Cash adjustments and corrections"
			if inCurrent {
				otherAmounts[key] += transaction.Amount
				report.OtherNet += transaction.Amount
			}
			if inPrevious {
				otherComparisons[key] += transaction.Amount
				report.ComparisonOtherNet += transaction.Amount
			}
		default:
			key := transaction.Category
			label := financeCategoryLabel(transaction.Category)
			if transaction.Amount >= 0 {
				revenueLabels[key] = label
				if inCurrent {
					revenueAmounts[key] += transaction.Amount
					report.TotalRevenue += transaction.Amount
				}
				if inPrevious {
					revenueComparisons[key] += transaction.Amount
					report.ComparisonRevenue += transaction.Amount
				}
			} else {
				expenseLabels[key] = label
				if inCurrent {
					expenseAmounts[key] += -transaction.Amount
					report.TotalExpenses += -transaction.Amount
				}
				if inPrevious {
					expenseComparisons[key] += -transaction.Amount
					report.ComparisonExpenses += -transaction.Amount
				}
			}
		}
	}

	report.RevenueItems = financeBuildStatementItems(revenueAmounts, revenueComparisons, revenueLabels, true)
	report.ExpenseItems = financeBuildStatementItems(expenseAmounts, expenseComparisons, expenseLabels, true)
	report.OtherItems = financeBuildStatementItems(otherAmounts, otherComparisons, otherLabels, false)
	report.TotalRevenue = normalizeMoney(report.TotalRevenue)
	report.TotalExpenses = normalizeMoney(report.TotalExpenses)
	report.ComparisonRevenue = normalizeMoney(report.ComparisonRevenue)
	report.ComparisonExpenses = normalizeMoney(report.ComparisonExpenses)
	report.OperatingProfit = normalizeMoney(report.TotalRevenue - report.TotalExpenses)
	report.ComparisonOperating = normalizeMoney(report.ComparisonRevenue - report.ComparisonExpenses)
	report.OtherNet = normalizeMoney(report.OtherNet)
	report.ComparisonOtherNet = normalizeMoney(report.ComparisonOtherNet)
	report.NetProfit = normalizeMoney(report.OperatingProfit + report.OtherNet)
	report.ComparisonNetProfit = normalizeMoney(report.ComparisonOperating + report.ComparisonOtherNet)
	return report, nil
}

func (a *App) buildFinanceBalanceSheet(asOfRaw string, divisionIDs []int64) (*FinanceBalanceSheet, error) {
	asOfDate, asOf, err := financeBalanceSheetAsOfDate(asOfRaw, time.Now())
	if err != nil {
		return nil, err
	}
	accounts, err := a.listFinanceAccountsByDivisionIDs(divisionIDs, false)
	if err != nil {
		return nil, err
	}
	filter := FinanceFilter{}
	if len(accounts) > 0 {
		filter.AccountIDs = make([]int64, 0, len(accounts))
		for _, account := range accounts {
			filter.AccountIDs = append(filter.AccountIDs, account.ID)
		}
	}
	transactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		return nil, err
	}
	report := &FinanceBalanceSheet{AsOf: asOf}
	cutoff := asOfDate.AddDate(0, 0, 1).Add(-time.Nanosecond)

	balancesByAccount := make(map[int64]float64, len(accounts))
	for _, transaction := range transactions {
		if !financeTransactionPosted(transaction) || transaction.RecordedAt.After(cutoff) {
			continue
		}
		balancesByAccount[transaction.FinanceAccountID] = normalizeMoney(balancesByAccount[transaction.FinanceAccountID] + transaction.Amount)
		switch transaction.TransactionType {
		case financeTxnTypeOpeningBalance:
			report.TotalEquity += transaction.Amount
		case financeTxnTypeAdjustment:
			// Captured separately below.
		case financeTxnTypeTransferIn, financeTxnTypeTransferOut:
			// Transfers do not affect equity.
		default:
			report.TotalEquity += transaction.Amount
		}
	}

	openingCapital := 0.0
	retainedEarnings := 0.0
	adjustmentReserve := 0.0
	for _, transaction := range transactions {
		if !financeTransactionPosted(transaction) || transaction.RecordedAt.After(cutoff) {
			continue
		}
		switch transaction.TransactionType {
		case financeTxnTypeOpeningBalance:
			openingCapital += transaction.Amount
		case financeTxnTypeAdjustment:
			adjustmentReserve += transaction.Amount
		case financeTxnTypeTransferIn, financeTxnTypeTransferOut:
		default:
			retainedEarnings += transaction.Amount
		}
	}

	for _, account := range accounts {
		balance := normalizeMoney(balancesByAccount[account.ID])
		if moneyEquals(balance, 0) {
			continue
		}
		item := FinanceStatementItem{
			Code:   account.AccountCode,
			Label:  account.AccountCode + " · " + account.Name,
			Amount: absMoney(balance),
		}
		if balance > 0 {
			report.AssetItems = append(report.AssetItems, item)
			report.TotalAssets += balance
		} else {
			report.LiabilityItems = append(report.LiabilityItems, item)
			report.TotalLiabilities += -balance
		}
	}

	sort.Slice(report.AssetItems, func(i, j int) bool { return report.AssetItems[i].Label < report.AssetItems[j].Label })
	sort.Slice(report.LiabilityItems, func(i, j int) bool { return report.LiabilityItems[i].Label < report.LiabilityItems[j].Label })

	report.EquityItems = append(report.EquityItems,
		FinanceStatementItem{Code: "opening_capital", Label: "Opening capital introduced", Amount: normalizeMoney(openingCapital)},
		FinanceStatementItem{Code: "retained_earnings", Label: "Retained earnings from operations", Amount: normalizeMoney(retainedEarnings)},
	)
	if !moneyEquals(adjustmentReserve, 0) {
		report.EquityItems = append(report.EquityItems, FinanceStatementItem{Code: "adjustments", Label: "Adjustment reserve", Amount: normalizeMoney(adjustmentReserve)})
	}

	report.TotalAssets = normalizeMoney(report.TotalAssets)
	report.TotalLiabilities = normalizeMoney(report.TotalLiabilities)
	report.TotalEquity = normalizeMoney(openingCapital + retainedEarnings + adjustmentReserve)
	report.TotalLiabilitiesAndEquity = normalizeMoney(report.TotalLiabilities + report.TotalEquity)
	report.BalancingDifference = normalizeMoney(report.TotalAssets - report.TotalLiabilitiesAndEquity)
	report.WorkingCapital = normalizeMoney(report.TotalAssets - report.TotalLiabilities)
	if report.TotalLiabilities > 0 {
		report.CurrentRatio = report.TotalAssets / report.TotalLiabilities
	}

	outstandingBookings, err := a.listOutstandingBookingFinancialsByDivisionIDs(divisionIDs)
	if err == nil {
		for _, item := range outstandingBookings {
			if item.OutstandingAmount > 0 {
				report.MemoOutstandingBookingReceivables += item.OutstandingAmount
			}
		}
		report.MemoOutstandingBookingReceivables = normalizeMoney(report.MemoOutstandingBookingReceivables)
	}
	paymentMonth := latestCollectiblePaymentMonth(time.Now())
	monthlyRows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, divisionIDs)
	if err == nil {
		for _, row := range monthlyRows {
			if row.Payment == nil {
				report.MemoCurrentMonthStudentDues += row.MonthlyFee
			}
		}
		report.MemoCurrentMonthStudentDues = normalizeMoney(report.MemoCurrentMonthStudentDues)
	}
	referrals, err := a.listBookingReferralsByDivisionIDs(divisionIDs)
	if err == nil {
		for _, referral := range referrals {
			if bookingReferralIsPayable(referral) {
				report.MemoUnpaidReferralCommissions += referral.CommissionAmount
			}
		}
		report.MemoUnpaidReferralCommissions = normalizeMoney(report.MemoUnpaidReferralCommissions)
	}

	return report, nil
}
