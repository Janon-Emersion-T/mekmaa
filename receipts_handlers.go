package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) unpaidStudentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	selectedDivision, err := a.resolveAuthorizedDivisionFromRequest(r, canViewAllDivisions(user))
	if errors.Is(err, ErrForbiddenDivision) {
		a.writeDivisionForbidden(w, r, user)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	divisionIDs := []int64(nil)
	if selectedDivision != nil {
		divisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		divisionIDs, err = a.scopedDivisionIDsForUser(user, true)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	paymentMonth := latestCollectiblePaymentMonth(time.Now())
	rows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, divisionIDs)
	if err != nil {
		log.Printf("unpaid student payments load failed: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("search"))
	program := strings.TrimSpace(r.URL.Query().Get("program"))
	programOptions := studentPaymentProgramOptions(rows)
	rows = pendingStudentPaymentRows(filterStudentPaymentRows(rows, search, "", program, ""))

	data := a.newTemplateData(w, r, user)
	data.Title = "Pending Student Payments"
	data.Description = "Review outstanding monthly payments for students."
	data.StudentPaymentRows = rows
	data.PaymentMonth = paymentMonth
	data.PaymentMonthLabel = paymentMonthLabel(paymentMonth)
	data.PaymentSearch = search
	data.PaymentProgramFilter = program
	data.PaymentProgramOptions = programOptions
	for _, row := range rows {
		data.PaymentTotalDue = normalizeMoney(data.PaymentTotalDue + row.MonthlyFee)
		data.PaymentCollected = normalizeMoney(data.PaymentCollected + row.CollectedAmount)
		data.PaymentOutstanding = normalizeMoney(data.PaymentOutstanding + row.OutstandingAmount)
		if studentPaymentRowStatus(row) == "partial" {
			data.PaymentPartialCount++
		} else {
			data.PaymentPendingCount++
		}
	}

	if selectedDivision != nil {
		data.SelectedDivision = selectedDivision
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	a.render(w, "unpaid-student-payments", data, http.StatusOK)
}

func (a *App) receiptsManagementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	allowedDivisionIDs, err := a.scopedDivisionIDsForUser(user, true)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	selectedDivision, err := a.resolveAuthorizedDivisionFromRequest(r, canViewAllDivisions(user))
	if errors.Is(err, ErrForbiddenDivision) {
		a.writeDivisionForbidden(w, r, user)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	filter := financeFilterFromRequest(r)
	filter.DetailMode = "detailed"
	if selectedDivision != nil {
		filter.DivisionID = selectedDivision.ID
		filter.DivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		filter.DivisionIDs = allowedDivisionIDs
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	transactions, total, err := a.listFinanceTransactionsPage(ctx, filter)
	if err != nil {
		log.Printf("receipts page load failed: op=list receipts page=%d limit=%d err=%v", filter.Page, filter.Limit, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	filteredTransactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		log.Printf("receipts page load failed: op=list receipt stats err=%v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	for i := range transactions {
		receipt := &transactions[i]

		if receipt.Voided ||
			receipt.GeneralVoidAllowed ||
			receipt.OrphanedSource {
			continue
		}

		if !userCanAccessDivision(user, receipt.DivisionID) {
			continue
		}

		receipt.ReceiptSourceVoidAllowed =
			receiptSourceVoidAuthorized(user, receipt)
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Receipts"
	data.Description = "Search, print and safely void finance receipts."
	data.FinanceTransactions = transactions
	data.FinanceTransactionsTotal = total
	data.FinanceFilter = filter
	data.FinanceLedgerHasPreviousPage = filter.Page > 1
	data.FinanceLedgerHasNextPage = filter.Page*filter.Limit < total
	data.FinanceLedgerPreviousPageURL = financeFilterPageURL(r, filter, filter.Page-1)
	data.FinanceLedgerNextPageURL = financeFilterPageURL(r, filter, filter.Page+1)
	data.Stats = buildReceiptStats(filteredTransactions)

	a.render(w, "receipts", data, http.StatusOK)
}

func buildReceiptStats(transactions []FinanceTransaction) []Stat {
	var activeCount int
	var voidedCount int
	var moneyIn float64
	var moneyOut float64

	for _, transaction := range transactions {
		if transaction.Voided {
			voidedCount++
			continue
		}
		activeCount++
		moneyIn = normalizeMoney(moneyIn + transaction.MoneyIn)
		moneyOut = normalizeMoney(moneyOut + transaction.MoneyOut)
	}

	return []Stat{
		{Label: "Total receipts", Value: strconv.Itoa(len(transactions))},
		{Label: "Active receipts", Value: strconv.Itoa(activeCount)},
		{Label: "Voided receipts", Value: strconv.Itoa(voidedCount)},
		{Label: "Net posted value", Value: money(normalizeMoney(moneyIn - moneyOut))},
	}
}
