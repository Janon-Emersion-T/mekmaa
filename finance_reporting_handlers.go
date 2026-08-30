package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func normalizeStudentPaymentMonthRange(fromMonth string, toMonth string, fallbackMonth string, currentMonth string) (string, string) {
	if _, err := parsePaymentMonth(fallbackMonth); err != nil {
		fallbackMonth = latestCollectiblePaymentMonth(time.Now())
	}
	if _, err := parsePaymentMonth(fromMonth); err != nil || fromMonth > currentMonth {
		fromMonth = fallbackMonth
	}
	if _, err := parsePaymentMonth(toMonth); err != nil || toMonth > currentMonth {
		toMonth = fallbackMonth
	}
	if fromMonth > toMonth {
		fromMonth, toMonth = toMonth, fromMonth
	}
	return fromMonth, toMonth
}

func filterStudentPaymentActivityRows(rows []StudentMonthlyPaymentActivityRow, search string, program string, method string) []StudentMonthlyPaymentActivityRow {
	search = strings.ToLower(strings.TrimSpace(search))
	program = strings.ToLower(strings.TrimSpace(program))
	method = strings.ToLower(strings.TrimSpace(method))

	filtered := make([]StudentMonthlyPaymentActivityRow, 0, len(rows))
	for _, row := range rows {
		if search != "" {
			haystack := strings.ToLower(strings.TrimSpace(strings.Join([]string{
				row.StudentName,
				row.StudentID,
				row.TrainingProgramName,
				row.DivisionName,
				row.Payment.ReceiptNumber,
			}, " ")))
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		if program != "" && program != "all" && strings.ToLower(strings.TrimSpace(row.TrainingProgramName)) != program {
			continue
		}
		if method != "" && method != "all" && method != "unpaid" && strings.ToLower(strings.TrimSpace(row.Payment.PaymentMethod)) != method {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func studentPaymentsPDFChoice(value string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, option := range allowed {
		if value == option {
			return value
		}
	}
	if len(allowed) == 0 {
		return ""
	}
	return allowed[0]
}

func studentPaymentsPDFBool(r *http.Request, key string, fallback bool) bool {
	values, ok := r.URL.Query()[key]
	if !ok {
		return fallback
	}
	if len(values) == 0 {
		return fallback
	}
	value := strings.ToLower(strings.TrimSpace(values[len(values)-1]))
	switch value {
	case "", "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return fallback
	}
}

func (a *App) exportStudentPaymentActivityCSV(
	w http.ResponseWriter,
	rows []StudentMonthlyPaymentActivityRow,
	paymentMonth string,
	fromMonth string,
	toMonth string,
) {
	filename := fmt.Sprintf("mekmaa-student-payments-%s-%s-to-%s.csv", paymentMonth, fromMonth, toMonth)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	writer := csv.NewWriter(w)
	defer writer.Flush()

	_ = writer.Write([]string{"Register Month", paymentMonthLabel(paymentMonth)})
	_ = writer.Write([]string{"Activity Range", paymentMonthLabel(fromMonth), paymentMonthLabel(toMonth)})
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"Payment Month", "Collected At", "Receipt", "Student ID", "Student Name", "Programme", "Division", "Payment Method", "Amount (LKR)", "Discount (LKR)", "Settled (LKR)", "Status", "Voided At", "Voided By", "Void Reason"})
	for _, row := range rows {
		status := "Active"
		voidedAt := ""
		voidedBy := ""
		voidReason := ""
		if row.Payment.Voided {
			status = "Voided"
			if !row.Payment.VoidedAt.IsZero() {
				voidedAt = formatDateTime(row.Payment.VoidedAt)
			}
			voidedBy = row.Payment.VoidedByUserName
			voidReason = row.Payment.VoidReason
		}
		_ = writer.Write([]string{
			paymentMonthLabel(row.Payment.PaymentMonth),
			formatDateTime(row.Payment.CollectedAt),
			csvSafeCell(row.Payment.ReceiptNumber),
			csvSafeCell(row.StudentID),
			csvSafeCell(row.StudentName),
			csvSafeCell(row.TrainingProgramName),
			csvSafeCell(row.DivisionName),
			csvSafeCell(paymentMethodLabel(row.Payment.PaymentMethod)),
			fmt.Sprintf("%.2f", row.Payment.Amount),
			fmt.Sprintf("%.2f", row.Payment.DiscountAmount),
			fmt.Sprintf("%.2f", row.SettledAmount),
			status,
			voidedAt,
			csvSafeCell(voidedBy),
			csvSafeCell(voidReason),
		})
	}
}

func (a *App) financeManagementHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
}

func (a *App) financeLedgerHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "ledger")
}

func (a *App) financeReceivablesHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "receivables")
}

func (a *App) financeBookingReceivablesHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "receivables-bookings")
}

func (a *App) financeTransfersHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "transfers")
}

func (a *App) financeReconciliationsHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "reconciliations")
}

func (a *App) financeAccountsHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "accounts")
}

func (a *App) financeCategoriesHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "categories")
}

func (a *App) financeCustomersHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "customers")
}

func (a *App) financeProfitAndLossHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "profit-loss")
}

func (a *App) financeBalanceSheetHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "balance-sheet")
}

func (a *App) financeSpecifiedLedgersHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	a.financeSectionHandler(w, r, "specified-ledgers")
}

func (a *App) financeSpecifiedLedgerDetailHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	user, _ := a.currentUser(r.Context())
	key := strings.TrimPrefix(
		r.URL.Path,
		"/admin/finance/specified-ledgers/",
	)
	key = strings.Trim(key, "/")
	if key == "" {
		http.NotFound(w, r)
		return
	}
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	ledgers, from, to, err := a.buildFinanceSpecifiedLedgers(
		strings.TrimSpace(r.URL.Query().Get("from")),
		strings.TrimSpace(r.URL.Query().Get("to")),
		scopeDivisionIDs,
	)
	if err != nil {
		log.Printf("finance specified ledger detail: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	selected := findFinanceSpecifiedLedger(ledgers, key)
	if selected == nil {
		http.NotFound(w, r)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "csv":
		a.exportFinanceSpecifiedLedgerCSV(w, *selected, from, to)
		return
	case "pdf":
		data := a.newTemplateData(w, r, user)
		data.HideChrome = true
		data.Title = selected.Title + " | Specified Ledger"
		data.Description = selected.Description
		data.FinancePage = "specified-ledgers"
		data.SelectedDivision = selectedDivision
		if selectedDivision != nil {
			data.SelectedDivisionScope = selectedDivision.Slug
		}
		data.SelectedFinanceSpecifiedLedger = selected
		data.FinanceSpecifiedLedgerFrom = from
		data.FinanceSpecifiedLedgerTo = to
		a.render(w, "finance-specified-ledger-print", data, http.StatusOK)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data, err := a.buildFinanceSectionData(
		w,
		r,
		user,
		ctx,
		time.Now(),
		"specified-ledgers",
	)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.SelectedFinanceSpecifiedLedger = selected
	data.FinanceSpecifiedLedgerFrom = from
	data.FinanceSpecifiedLedgerTo = to
	a.render(w, "finance-management", data, http.StatusOK)
}

func (a *App) exportFinanceSpecifiedLedgerCSV(
	w http.ResponseWriter,
	ledger FinanceSpecifiedLedger,
	from string,
	to string,
) {
	filename := fmt.Sprintf(
		"mekmaa-specified-ledger-%s-%s-to-%s.csv",
		ledger.Key,
		from,
		to,
	)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, filename),
	)

	writer := csv.NewWriter(w)
	defer writer.Flush()

	_ = writer.Write([]string{
		"Ledger",
		"Period From",
		"Period To",
		"Side",
		"Date",
		"Transaction ID",
		"Reference",
		"Counterparty",
		"Description",
		"Division",
		"Account",
		"Debit",
		"Credit",
	})

	writeEntries := func(side string, entries []FinanceSpecifiedLedgerEntry) {
		for _, entry := range entries {
			_ = writer.Write([]string{
				ledger.Title,
				from,
				to,
				side,
				entry.RecordedAt.In(time.Local).Format("2006-01-02"),
				strconv.FormatInt(entry.TransactionID, 10),
				entry.ReferenceNumber,
				entry.Counterparty,
				entry.Description,
				entry.DivisionName,
				entry.FinanceAccountName,
				fmt.Sprintf("%.2f", entry.DebitAmount),
				fmt.Sprintf("%.2f", entry.CreditAmount),
			})
		}
	}

	writeEntries("debit", ledger.DebitEntries)
	writeEntries("credit", ledger.CreditEntries)
	_ = writer.Write([]string{
		ledger.Title,
		from,
		to,
		"totals",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		fmt.Sprintf("%.2f", ledger.DebitTotal),
		fmt.Sprintf("%.2f", ledger.CreditTotal),
	})
}

func (a *App) financeSectionHandler(w http.ResponseWriter, r *http.Request, page string) {
	user, _ := a.currentUser(r.Context())
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data, err := a.buildFinanceSectionData(w, r, user, ctx, started, page)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.render(w, "finance-management", data, http.StatusOK)
}

func (a *App) buildFinanceSectionData(w http.ResponseWriter, r *http.Request, user *User, ctx context.Context, started time.Time, page string) (TemplateData, error) {
	data := a.newTemplateData(w, r, user)
	data.Title = "Finance"
	data.Description = "Monitor cash, bank, receivables, expenses, transfers, reconciliations, and payment history."
	data.FinancePage = page
	data.TodayDate = time.Now().Format("2006-01-02")
	data.FinancePeriodLock, _ = a.currentFinancePeriodLock()
	allowedDivisionIDs, err := a.scopedDivisionIDsForUser(user, true)
	if err != nil {
		return data, err
	}
	selectedDivision, err := a.resolveAuthorizedDivisionFromRequest(r, canViewAllDivisions(user))
	if errors.Is(err, ErrForbiddenDivision) {
		a.writeDivisionForbidden(w, r, user)
		return data, err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return data, err
	}
	if selectedDivision != nil {
		data.SelectedDivision = selectedDivision
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}
	accountDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		accountDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		accountDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	needOperationalSummary := page == "ledger" || page == "specified-ledgers" || page == "transfers" || page == "reconciliations" || page == "accounts" || page == "profit-loss" || page == "balance-sheet"
	needAccounts := needOperationalSummary
	needAllTransactions := page == "ledger" || page == "specified-ledgers" || page == "accounts" || page == "transfers" || page == "reconciliations" || page == "profit-loss" || page == "balance-sheet"
	needBookingFinancials := page == "receivables" || page == "receivables-bookings" || page == "customers"
	needMonthlyRows := page == "receivables"
	needTransfers := page == "transfers"
	needReconciliations := page == "reconciliations"
	needCategories := page == "ledger" || page == "categories" || page == "profit-loss"
	needWorkflowOptions := page == "ledger"

	var allTransactions []FinanceTransaction
	var allMonthlyRows []StudentPaymentRow

	if needAccounts {
		activeOnly := page != "accounts" && page != "balance-sheet"
		accounts, err := a.listFinanceAccountsByDivisionIDs(accountDivisionIDs, activeOnly)
		if err != nil {
			log.Printf("finance %s load failed: op=list finance accounts duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceAccounts = accounts
	}

	if needCategories {
		categories, err := a.listFinanceCategories(false)
		if err != nil {
			log.Printf("finance %s load failed: op=list finance categories duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceCategories = categories
	}

	if needWorkflowOptions {
		programDivisionIDs := []int64(nil)
		if selectedDivision != nil {
			programDivisionIDs = []int64{selectedDivision.ID}
		} else if !canViewAllDivisions(user) {
			programDivisionIDs = allowedDivisionIDs
		}
		trainingPrograms, err := a.listTrainingProgramsByDivisionIDs(programDivisionIDs, true, true)
		if err != nil {
			log.Printf("finance %s load failed: op=list training programs duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.TrainingPrograms = trainingPrograms
		activities, err := a.listFinanceBookingActivities()
		if err != nil {
			log.Printf("finance %s load failed: op=list booking activities duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.CourtActivities = activities
		offerings, err := a.listOneToOneOfferings(true)
		if err != nil {
			log.Printf("finance %s load failed: op=list one-to-one offerings duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.OneToOneOfferings = offerings
	}

	if needAllTransactions {
		var summaryFilter FinanceFilter
		if selectedDivision != nil {
			summaryFilter.DivisionID = selectedDivision.ID
			summaryFilter.DivisionIDs = []int64{selectedDivision.ID}
		} else if !canViewAllDivisions(user) {
			summaryFilter.DivisionIDs = allowedDivisionIDs
		}
		allTransactions, err = a.listFinanceTransactionsFiltered(summaryFilter)
		if err != nil {
			log.Printf("finance %s load failed: op=list finance summary transactions duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.Stats = buildFinanceStats(allTransactions)
	}

	if page == "ledger" {
		filter := financeFilterFromRequest(r)
		if selectedDivision != nil {
			filter.DivisionID = selectedDivision.ID
			filter.DivisionIDs = []int64{selectedDivision.ID}
		} else if !canViewAllDivisions(user) {
			filter.DivisionIDs = allowedDivisionIDs
		}
		financeTransactions, totalTransactions, err := a.listFinanceTransactionsPage(ctx, filter)
		if err != nil {
			log.Printf("finance ledger load failed: op=list finance transactions duration=%s page=%d limit=%d err=%v", time.Since(started), filter.Page, filter.Limit, err)
			return data, err
		}
		data.FinanceTransactions = financeTransactions
		data.FinanceTransactionsTotal = totalTransactions
		data.FinanceFilter = filter
		data.FinanceLedgerHasPreviousPage = filter.Page > 1
		data.FinanceLedgerHasNextPage = filter.Page*filter.Limit < totalTransactions
		data.FinanceLedgerPreviousPageURL = financeFilterPageURL(r, filter, filter.Page-1)
		data.FinanceLedgerNextPageURL = financeFilterPageURL(r, filter, filter.Page+1)

		filteredTransactions, err := a.listFinanceTransactionsFiltered(filter)
		if err != nil {
			log.Printf("finance ledger load failed: op=list filtered finance transactions duration=%s err=%v", time.Since(started), err)
			return data, err
		}
		data.Stats = buildLedgerStats(filteredTransactions)
		for _, row := range filteredTransactions {
			if !financeTransactionPosted(row) {
				continue
			}
			data.StatementMoneyIn += row.MoneyIn
			data.StatementMoneyOut += row.MoneyOut
		}
		data.StatementNetMovement = normalizeMoney(data.StatementMoneyIn - data.StatementMoneyOut)
		if filter.AccountID > 0 {
			account, err := a.findFinanceAccountByID(filter.AccountID)
			if err != nil {
				log.Printf("finance ledger load failed: op=find finance account duration=%s account_id=%d err=%v", time.Since(started), filter.AccountID, err)
				return data, err
			}
			if len(accountDivisionIDs) > 0 {
				allowed := false
				for _, divisionID := range accountDivisionIDs {
					if divisionID == account.DivisionID {
						allowed = true
						break
					}
				}
				if !allowed {
					a.writeDivisionForbidden(w, r, user)
					return data, ErrForbiddenDivision
				}
			}
			data.SelectedFinanceAccount = account
			statement, err := a.buildFinanceStatement(filter.AccountID, filter.From, filter.To)
			if err != nil {
				log.Printf("finance ledger load failed: op=build finance statement duration=%s account_id=%d err=%v", time.Since(started), filter.AccountID, err)
				return data, err
			}
			data.StatementOpeningBalance = statement.OpeningBalance
			data.StatementClosingBalance = statement.ClosingBalance
			runningByID := make(map[int64]float64, len(statement.Rows))
			for _, row := range statement.Rows {
				runningByID[row.ID] = row.RunningBalance
			}
			for i := range data.FinanceTransactions {
				if running, ok := runningByID[data.FinanceTransactions[i].ID]; ok {
					data.FinanceTransactions[i].RunningBalance = running
				}
			}
		}
	}

	if needBookingFinancials {
		bookingFinancials, err := a.listOutstandingBookingFinancialsByDivisionIDs(scopeDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=list booking financials duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.BookingFinancials = bookingFinancials
		if page == "receivables-bookings" {
			data.BookingPaymentCollections, _ = a.listRecentBookingPaymentCollectionsByDivisionIDs(scopeDivisionIDs, 6)
		} else if page == "customers" {
			data.BookingPaymentCollections, _ = a.listBookingPaymentCollectionsForScheduleIDs(scheduleIDsFromFinancials(bookingFinancials))
		}
	}

	if page == "customers" {
		data.FinanceCustomerSearch = strings.TrimSpace(r.URL.Query().Get("search"))
		data.BookingCustomerBalances = aggregateBookingCustomerBalances(data.BookingFinancials, data.FinanceCustomerSearch)
	}

	if page == "profit-loss" {
		report, err := a.buildFinanceProfitAndLoss(strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to")), scopeDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=build profit and loss duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceProfitAndLoss = report
	}

	if page == "balance-sheet" {
		report, err := a.buildFinanceBalanceSheet(strings.TrimSpace(r.URL.Query().Get("as_of")), scopeDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=build balance sheet duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceBalanceSheet = report
	}

	if page == "specified-ledgers" {
		ledgers, from, to, err := a.buildFinanceSpecifiedLedgers(
			strings.TrimSpace(r.URL.Query().Get("from")),
			strings.TrimSpace(r.URL.Query().Get("to")),
			scopeDivisionIDs,
		)
		if err != nil {
			log.Printf("finance %s load failed: op=build specified ledgers duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceSpecifiedLedgers = ledgers
		data.FinanceSpecifiedLedgerFrom = from
		data.FinanceSpecifiedLedgerTo = to
	}

	if needMonthlyRows {
		paymentMonth := latestCollectiblePaymentMonth(time.Now())
		rowDivisionIDs := []int64(nil)
		if selectedDivision != nil {
			rowDivisionIDs = []int64{selectedDivision.ID}
		} else if !canViewAllDivisions(user) {
			rowDivisionIDs = allowedDivisionIDs
		}
		monthlyRows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, rowDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=list monthly receivables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		allMonthlyRows = monthlyRows
		data.PaymentMonth = paymentMonth
		data.PaymentMonthLabel = paymentMonthLabel(paymentMonth)
		data.PaymentCollectionOpen = paymentMonthCollectible(paymentMonth, time.Now())
		data.PaymentCollectionNotice = monthlyPaymentCollectionNotice(paymentMonth, time.Now())
		data.StudentPaymentRows = pendingStudentPaymentRows(monthlyRows)
	}

	if page == "receivables" {
		oneToOneReceivables, err := a.financeOneToOneReceivables()
		if err != nil {
			log.Printf("finance %s load failed: op=list one-to-one receivables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.OneToOneReceivables = oneToOneReceivables
		mcpReceivables, err := a.financeMCPReceivables()
		if err != nil {
			log.Printf("finance %s load failed: op=list mcp receivables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.MCPReceivables = mcpReceivables
		referrals, err := a.listBookingReferralsByDivisionIDs(scopeDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=list referral payables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.BookingReferrals = referrals
		data.FinanceSummary = buildFinanceSummary(nil, nil, data.BookingFinancials, allMonthlyRows, referrals, nil)
		data.FinanceReceivableSummaryCards = buildFinanceReceivableSummaryCards(data)
		data.FinanceReceivableOverviewRows = buildFinanceReceivableOverviewRows(data)
	}

	if needTransfers {
		transfers, err := a.listFinanceTransfers()
		if err != nil {
			log.Printf("finance %s load failed: op=list finance transfers duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceTransfers = transfers
	}

	if needReconciliations {
		reconciliations, err := a.listCashReconciliations(10)
		if err != nil {
			log.Printf("finance %s load failed: op=list cash reconciliations duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.CashReconciliations = reconciliations
	}

	if needOperationalSummary {
		reconciliations := data.CashReconciliations
		if len(reconciliations) == 0 {
			var err error
			reconciliations, err = a.listCashReconciliations(0)
			if err != nil {
				log.Printf("finance %s load failed: op=list finance overview reconciliations duration=%s err=%v", page, time.Since(started), err)
				return data, err
			}
			if page == "reconciliations" {
				data.CashReconciliations = reconciliations
			}
		}
		data.FinanceSummary = buildFinanceSummary(data.FinanceAccounts, allTransactions, nil, nil, nil, reconciliations)
		data.FinanceAccounts = financeAccountsWithBalances(data.FinanceAccounts, allTransactions, reconciliations)
	}

	return data, nil
}

func (a *App) financeOneToOneReceivables() ([]OneToOneReceivable, error) {
	bookings, err := a.listOneToOneBookings()
	if err != nil {
		return nil, err
	}
	scheduleIDs := make([]int64, 0, len(bookings))
	for _, booking := range bookings {
		if booking.ScheduleID > 0 {
			scheduleIDs = append(scheduleIDs, booking.ScheduleID)
		}
	}
	financials, err := a.listBookingFinancialsForScheduleIDs(scheduleIDs)
	if err != nil {
		return nil, err
	}
	financialBySchedule := make(map[int64]BookingFinancial, len(financials))
	for _, financial := range financials {
		financialBySchedule[financial.ScheduleID] = financial
	}
	rows := make([]OneToOneReceivable, 0, len(bookings))
	for _, booking := range bookings {
		financial, ok := financialBySchedule[booking.ScheduleID]
		if !ok || financial.OutstandingAmount <= 0.004 {
			continue
		}
		rows = append(rows, OneToOneReceivable{Booking: booking, Financial: financial})
	}
	return rows, nil
}

func (a *App) financeMCPReceivables() ([]MCPReceivable, error) {
	plans, err := a.listMCPMonthlyPlans(0)
	if err != nil {
		return nil, err
	}
	receivables := make([]MCPReceivable, 0, len(plans))
	for _, plan := range plans {
		if plan.GrossAmount > 0 && plan.OutstandingAmount > 0.004 && plan.Status != mcpPlanStatusCancelled {
			receivables = append(receivables, MCPReceivable{Plan: plan})
		}
	}
	return receivables, nil
}

func buildFinanceReceivableSummaryCards(data TemplateData) []FinanceReceivableSummaryCard {
	cards := []FinanceReceivableSummaryCard{
		{
			Key:               "bookings",
			Label:             "Bookings",
			Description:       "Public court bookings and walk-in slot payments.",
			ActionURL:         "/admin/finance/receivables/bookings",
			ActionLabel:       "Open bookings",
			Count:             len(data.BookingFinancials),
			OutstandingAmount: data.FinanceSummary.OutstandingBooking,
		},
		{
			Key:               "students",
			Label:             "Students",
			Description:       "Monthly student fees for the latest collectible month.",
			ActionURL:         "/admin/student-payments",
			ActionLabel:       "Open students",
			Count:             len(data.StudentPaymentRows),
			OutstandingAmount: financeStudentOutstanding(data.StudentPaymentRows),
		},
		{
			Key:               "one_to_one",
			Label:             "1 to 1",
			Description:       "Outstanding one-to-one packages and follow-up collections.",
			ActionURL:         "/admin/one-to-one-receivables",
			ActionLabel:       "Open 1 to 1",
			Count:             len(data.OneToOneReceivables),
			OutstandingAmount: financeOneToOneOutstanding(data.OneToOneReceivables),
		},
		{
			Key:               "mcp",
			Label:             "MCP",
			Description:       "Monthly court plan balances that still need collection.",
			ActionURL:         "/admin/mcp-receivables",
			ActionLabel:       "Open MCP",
			Count:             len(data.MCPReceivables),
			OutstandingAmount: financeMCPOutstanding(data.MCPReceivables),
		},
	}
	return cards
}

func buildFinanceReceivableOverviewRows(data TemplateData) []FinanceReceivableOverviewRow {
	rows := make([]FinanceReceivableOverviewRow, 0, len(data.BookingFinancials)+len(data.StudentPaymentRows)+len(data.OneToOneReceivables)+len(data.MCPReceivables))
	for _, financial := range data.BookingFinancials {
		rows = append(rows, FinanceReceivableOverviewRow{
			TypeKey:           "bookings",
			TypeLabel:         "Booking",
			Reference:         bookingReference(financial.ScheduleID),
			DisplayName:       bookingFinancialDisplayName(financial),
			Context:           strings.TrimSpace(financial.Title + " · " + formatCalendarDate(financial.SlotDate) + " at " + formatClockTime(financial.SlotHour)),
			StatusLabel:       financial.Status,
			PaymentLabel:      bookingPaymentStatusBadge(financial.PaymentStatus),
			CollectedAmount:   financial.TotalCollected,
			OutstandingAmount: financial.OutstandingAmount,
			ActionURL:         "/admin/finance/receivables/bookings",
		})
	}
	for _, row := range data.StudentPaymentRows {
		rows = append(rows, FinanceReceivableOverviewRow{
			TypeKey:           "students",
			TypeLabel:         "Student",
			Reference:         row.Admission.StudentID,
			DisplayName:       row.Admission.FullName,
			Context:           strings.TrimSpace(row.Enrollment.TrainingProgramName + " · " + data.PaymentMonthLabel),
			StatusLabel:       "active",
			PaymentLabel:      financeStudentPaymentLabel(row),
			CollectedAmount:   row.CollectedAmount,
			OutstandingAmount: row.OutstandingAmount,
			ActionURL:         "/admin/student-payments?month=" + data.PaymentMonth,
		})
	}
	for _, row := range data.OneToOneReceivables {
		rows = append(rows, FinanceReceivableOverviewRow{
			TypeKey:           "one_to_one",
			TypeLabel:         "1 to 1",
			Reference:         fmt.Sprintf("Package #%d", row.Booking.ID),
			DisplayName:       row.Booking.CustomerName,
			Context:           strings.TrimSpace(row.Booking.OfferingName + " · " + formatCalendarDate(row.Booking.SlotDate) + " at " + formatClockTime(row.Booking.SlotHour)),
			StatusLabel:       row.Booking.PackageStatus,
			PaymentLabel:      bookingPaymentStatusBadge(row.Financial.PaymentStatus),
			CollectedAmount:   row.Financial.TotalCollected,
			OutstandingAmount: row.Financial.OutstandingAmount,
			ActionURL:         "/admin/one-to-one-receivables",
		})
	}
	for _, row := range data.MCPReceivables {
		rows = append(rows, FinanceReceivableOverviewRow{
			TypeKey:           "mcp",
			TypeLabel:         "MCP",
			Reference:         row.Plan.PlanMonth,
			DisplayName:       row.Plan.CustomerName,
			Context:           strings.TrimSpace(row.Plan.Title + " · " + strconv.Itoa(row.Plan.TotalSessions) + " sessions"),
			StatusLabel:       row.Plan.Status,
			PaymentLabel:      "Outstanding",
			CollectedAmount:   row.Plan.TotalCollected,
			OutstandingAmount: row.Plan.OutstandingAmount,
			ActionURL:         "/admin/mcp-receivables",
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OutstandingAmount == rows[j].OutstandingAmount {
			if rows[i].TypeLabel == rows[j].TypeLabel {
				return rows[i].DisplayName < rows[j].DisplayName
			}
			return rows[i].TypeLabel < rows[j].TypeLabel
		}
		return rows[i].OutstandingAmount > rows[j].OutstandingAmount
	})
	return rows
}

func financeStudentOutstanding(rows []StudentPaymentRow) float64 {
	total := 0.0
	for _, row := range rows {
		total = normalizeMoney(total + row.OutstandingAmount)
	}
	return total
}

func financeOneToOneOutstanding(rows []OneToOneReceivable) float64 {
	total := 0.0
	for _, row := range rows {
		total = normalizeMoney(total + row.Financial.OutstandingAmount)
	}
	return total
}

func financeMCPOutstanding(rows []MCPReceivable) float64 {
	total := 0.0
	for _, row := range rows {
		total = normalizeMoney(total + row.Plan.OutstandingAmount)
	}
	return total
}

func financeStudentPaymentLabel(row StudentPaymentRow) string {
	if row.CollectedAmount+0.004 >= row.MonthlyFee {
		return "Paid"
	}
	if row.CollectedAmount > 0.004 {
		return "Partial"
	}
	return "Pending"
}

func (a *App) createFinanceTransactionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	direction := strings.ToLower(strings.TrimSpace(r.FormValue("direction")))
	category := strings.ToLower(strings.TrimSpace(r.FormValue("category")))
	if !validFinanceCategoryDirection(direction) {
		http.Error(w, "invalid finance direction", http.StatusBadRequest)
		return
	}
	exists, err := a.financeCategoryExists(direction, category, true)
	if err != nil {
		http.Error(w, "could not validate finance category", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "invalid finance category", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil || amount <= 0 {
		http.Error(w, "amount must be greater than zero", http.StatusBadRequest)
		return
	}
	personName := strings.TrimSpace(r.FormValue("person_name"))
	description := strings.TrimSpace(r.FormValue("description"))
	accountID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("account_id")), 10, 64)
	divisionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("division_id")), 10, 64)
	if personName == "" || description == "" || accountID <= 0 {
		http.Error(w, "person, description, and finance account are required", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, currentUser, divisionID) {
		return
	}
	division, err := a.findDivisionByID(divisionID)
	if err != nil || !division.Active {
		http.Error(w, "a valid active division is required", http.StatusBadRequest)
		return
	}
	recordedAt := time.Now()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil || recordedAt.After(time.Now().Add(24*time.Hour)) {
			http.Error(w, "invalid recorded date", http.StatusBadRequest)
			return
		}
	}
	if direction == "expense" {
		amount = -amount
	}
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	approvalStatus := financeApprovalApproved
	if strings.TrimSpace(r.FormValue("submit_action")) == "pending" {
		approvalStatus = financeApprovalPending
	}
	transactionID, err := a.createManualFinanceTransactionForAccountWithApprovalInDivision(category, personName, description, strings.TrimSpace(r.FormValue("notes")), accountID, amount, divisionID, recordedAt, recordedBy, approvalStatus)
	if err != nil {
		log.Printf("create manual finance transaction: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if approvalStatus == financeApprovalPending {
		http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createFinanceCategoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if err := a.createFinanceCategory(
		r.FormValue("name"),
		r.FormValue("direction"),
		r.FormValue("active") == "1",
	); err != nil {
		a.setFlash(w, "Finance category could not be created: "+err.Error())
		http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance category created.")
	http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
}

func (a *App) updateFinanceCategoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	err := a.updateFinanceCategory(
		parseInt64Query(r.FormValue("category_id")),
		r.FormValue("name"),
		r.FormValue("direction"),
		r.FormValue("active") == "1",
	)
	if err != nil {
		if errors.Is(err, errFinanceCategoryLocked) {
			a.setFlash(w, "This category already has linked finance records and cannot be edited.")
		} else {
			a.setFlash(w, "Finance category could not be updated: "+err.Error())
		}
		http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance category updated.")
	http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
}

func (a *App) deleteFinanceCategoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	err := a.deleteFinanceCategory(parseInt64Query(r.FormValue("category_id")))
	if err != nil {
		if errors.Is(err, errFinanceCategoryLocked) {
			a.setFlash(w, "This category already has linked finance records and cannot be deleted.")
		} else {
			a.setFlash(w, "Finance category could not be deleted: "+err.Error())
		}
		http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance category deleted.")
	http.Redirect(w, r, "/admin/finance/categories", http.StatusSeeOther)
}

func (a *App) collectBookingPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	amount, amountErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	rawOverpaymentAmount := strings.TrimSpace(r.FormValue("overpayment_amount"))
	if rawOverpaymentAmount == "" {
		rawOverpaymentAmount = "0"
	}
	overpaymentAmount, overpaymentErr := strconv.ParseFloat(rawOverpaymentAmount, 64)
	rawDiscountAmount := strings.TrimSpace(r.FormValue("discount_amount"))
	if rawDiscountAmount == "" {
		rawDiscountAmount = "0"
	}
	discountAmount, discountErr := strconv.ParseFloat(rawDiscountAmount, 64)
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	paymentNote := strings.TrimSpace(r.FormValue("payment_note"))
	adjustmentReason := strings.TrimSpace(r.FormValue("adjustment_reason"))
	collectedAt, collectedAtErr := parseFinanceRecordedAtDate(
		r.FormValue("payment_collected_at"),
		time.Now(),
		"Payment collection date",
	)
	allowOverpayment := r.FormValue("allow_overpayment") == "1" || r.FormValue("allow_overpayment") == "on"
	applyDiscount := r.FormValue("settle_as_discounted") == "1" || r.FormValue("settle_as_discounted") == "on"
	returnTo := strings.TrimSpace(r.FormValue("return_to"))
	if err != nil || amountErr != nil || overpaymentErr != nil || discountErr != nil || collectedAtErr != nil || scheduleID <= 0 || !validPaymentMethod(paymentMethod) {
		if collectedAtErr != nil {
			http.Error(w, collectedAtErr.Error(), http.StatusBadRequest)
			return
		}
		if overpaymentErr != nil || discountErr != nil {
			http.Error(w, "enter valid payment adjustment amounts", http.StatusBadRequest)
			return
		}
		http.Error(w, "select a valid booking payment method", http.StatusBadRequest)
		return
	}
	if returnTo == "" {
		returnTo = "/admin/finance/receivables"
	}
	if allowOverpayment && applyDiscount {
		a.setFlash(w, "Choose either overpayment or discount for the booking payment.")
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	if !allowOverpayment {
		overpaymentAmount = 0
	}
	if !applyDiscount {
		discountAmount = 0
	}
	if allowOverpayment && overpaymentAmount <= 0 {
		a.setFlash(w, "Enter the overpayment amount.")
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	if applyDiscount && discountAmount <= 0 {
		a.setFlash(w, "Enter the discount amount.")
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	if (allowOverpayment || applyDiscount) && adjustmentReason == "" {
		a.setFlash(w, "Enter the reason for the overpayment or discount.")
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.collectBookingPaymentAtWithAdjustment(scheduleID, paymentMethod, amount, paymentNote, collectedAt, recordedBy, allowOverpayment, bookingPaymentAdjustment{
		OverpaymentAmount: overpaymentAmount,
		DiscountAmount:    discountAmount,
		AdjustmentReason:  adjustmentReason,
	})
	if err != nil {
		if errors.Is(err, ErrBookingPaymentNeedsOverpayApproval) {
			a.setFlash(w, "Booking payment exceeds the current balance. Tick the overpayment confirmation box to continue.")
		} else {
			a.setFlash(w, "Booking payment could not be collected: "+err.Error())
		}
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/bookings/payments/receipt?id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) voidBookingPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	collectionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("collection_id")), 10, 64)
	if err != nil || collectionID <= 0 {
		http.Error(w, "invalid payment entry", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	voidedBy := int64(0)
	if currentUser != nil {
		voidedBy = currentUser.ID
	}
	returnTo := strings.TrimSpace(r.FormValue("return_to"))
	if returnTo == "" {
		returnTo = "/admin/finance/receivables"
	}
	if err := a.voidBookingPayment(collectionID, strings.TrimSpace(r.FormValue("void_reason")), voidedBy); err != nil {
		a.setFlash(w, "Booking payment could not be voided: "+err.Error())
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Booking payment was voided and the balance was recalculated.")
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (a *App) financeExportHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	filter := financeFilterFromRequest(r)
	filter.Page = 0
	filter.Limit = 0
	if user != nil && !canViewAllDivisions(user) {
		allowedDivisionIDs, err := a.scopedDivisionIDsForUser(user, true)
		if err != nil {
			http.Error(w, "could not validate division scope", http.StatusInternalServerError)
			return
		}
		if len(filter.DivisionIDs) > 0 {
			for _, divisionID := range filter.DivisionIDs {
				if !userCanAccessDivision(user, divisionID) {
					a.writeDivisionForbidden(w, r, user)
					return
				}
			}
		} else {
			filter.DivisionIDs = allowedDivisionIDs
		}
	}
	transactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		http.Error(w, "could not export finance transactions", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mekmaa-finance.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Receipt", "Date", "Direction", "Category", "Person", "Description", "Payment method", "Amount (LKR)"})
	for _, transaction := range transactions {
		direction := "Income"
		if transaction.Amount < 0 {
			direction = "Expense"
		}
		_ = writer.Write([]string{
			csvSafeCell(transaction.ReceiptNumber), formatDateTime(transaction.RecordedAt), direction,
			financeCategoryLabel(transaction.Category), csvSafeCell(transaction.PersonName), csvSafeCell(transaction.Description),
			csvSafeCell(transaction.PaymentMethod), strconv.FormatFloat(transaction.Amount, 'f', 2, 64),
		})
	}
	writer.Flush()
}

func (a *App) reportsHandler(w http.ResponseWriter, r *http.Request) {
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	period := reportPeriodFromRequest(r)
	domain := reportDomainFromRequest(r)
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	var (
		report     *OperationalReport
		finance    *FinanceDomainReport
		payroll    *PayrollDomainReport
		attendance *AttendanceDomainReport
		students   *StudentDomainReport
	)

	switch domain {
	case reportDomainFinance:
		finance, err = a.buildFinanceDomainReport(period, scopeDivisionIDs)
	case reportDomainPayroll:
		payroll, err = a.buildPayrollDomainReport(period, scopeDivisionIDs)
	case reportDomainAttendance:
		attendance, err = a.buildAttendanceDomainReport(period, scopeDivisionIDs)
	case reportDomainStudents:
		students, err = a.buildStudentDomainReport(period, scopeDivisionIDs)
	default:
		report, err = a.buildOperationalReport(period, scopeDivisionIDs)
	}
	if err != nil {
		log.Printf("build report domain %s: %v", domain, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	reportCenter := buildReportCenter(period, domain, report, finance, payroll, attendance, students)

	data := a.newTemplateData(w, r, user)
	data.Title = "Reports"
	data.Description = "Operational, finance, payroll, attendance, and student reporting."
	data.SelectedDivision = selectedDivision
	if selectedDivision != nil {
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	data.Report = report
	data.ReportCenter = reportCenter
	a.render(w, "reports", data, http.StatusOK)
}

func (a *App) reportsExportHandler(w http.ResponseWriter, r *http.Request) {
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	period := reportPeriodFromRequest(r)
	domain := reportDomainFromRequest(r)
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	var (
		report     *OperationalReport
		finance    *FinanceDomainReport
		payroll    *PayrollDomainReport
		attendance *AttendanceDomainReport
		students   *StudentDomainReport
	)

	switch domain {
	case reportDomainFinance:
		finance, err = a.buildFinanceDomainReport(period, scopeDivisionIDs)
	case reportDomainPayroll:
		payroll, err = a.buildPayrollDomainReport(period, scopeDivisionIDs)
	case reportDomainAttendance:
		attendance, err = a.buildAttendanceDomainReport(period, scopeDivisionIDs)
	case reportDomainStudents:
		students, err = a.buildStudentDomainReport(period, scopeDivisionIDs)
	default:
		report, err = a.buildOperationalReport(period, scopeDivisionIDs)
	}
	if err != nil {
		http.Error(w, "could not export report", http.StatusInternalServerError)
		return
	}
	center := buildReportCenter(period, domain, report, finance, payroll, attendance, students)
	if err := a.writeReportCenterCSV(w, center); err != nil {
		log.Printf("write report export: %v", err)
	}
}

func (a *App) referralCommissionsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	referrals, err := a.listBookingReferrals()
	if err != nil {
		log.Printf("list booking referrals: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	partners, err := a.listReferralPartners(false)
	if err != nil {
		log.Printf("list referral partners: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		log.Printf("get referral settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Referral Management"
	data.Description = "Manage the shared commission rate, referral partners, earnings, and payouts."
	data.BookingReferrals = referrals
	data.ReferralPartners = partners
	data.ReferralPartnerRows = buildReferralPartnerSummaries(partners, referrals)
	data.ReferralStats = buildReferralStats(referrals)
	data.PricingSettings = settings
	a.render(w, "referral-commissions", data, http.StatusOK)
}

func (a *App) studentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	paymentMonth := strings.TrimSpace(r.URL.Query().Get("month"))
	paymentSearch := strings.TrimSpace(r.URL.Query().Get("search"))
	paymentStatus := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	paymentProgram := strings.TrimSpace(r.URL.Query().Get("program"))
	paymentMethod := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("method")))
	paymentActivityFrom := strings.TrimSpace(r.URL.Query().Get("from_month"))
	paymentActivityTo := strings.TrimSpace(r.URL.Query().Get("to_month"))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	pdfOrientation := studentPaymentsPDFChoice(r.URL.Query().Get("pdf_orientation"), "landscape", "portrait")
	pdfPaperSize := studentPaymentsPDFChoice(r.URL.Query().Get("pdf_paper"), "a4", "letter")
	pdfDensity := studentPaymentsPDFChoice(r.URL.Query().Get("pdf_density"), "comfortable", "compact")
	pdfIncludeSummary := studentPaymentsPDFBool(r, "pdf_summary", true)
	pdfIncludeRegister := studentPaymentsPDFBool(r, "pdf_register", true)
	pdfIncludeActivity := studentPaymentsPDFBool(r, "pdf_activity", true)
	pdfIncludeFilters := studentPaymentsPDFBool(r, "pdf_filters", true)
	pdfAutoPrint := studentPaymentsPDFBool(r, "pdf_autoprint", true)
	currentMonth := time.Now().Format("2006-01")
	latestMonth := latestCollectiblePaymentMonth(time.Now())
	if _, err := parsePaymentMonth(paymentMonth); err != nil || paymentMonth > currentMonth {
		paymentMonth = latestMonth
	}
	paymentActivityFrom, paymentActivityTo = normalizeStudentPaymentMonthRange(paymentActivityFrom, paymentActivityTo, paymentMonth, currentMonth)

	selectedDivision, err := a.resolveAuthorizedDivisionFromRequest(r, canViewAllDivisions(user))
	if err != nil {
		if errors.Is(err, ErrForbiddenDivision) {
			a.writeDivisionForbidden(w, r, user)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	rowDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		rowDivisionIDs = []int64{selectedDivision.ID}
	} else if user != nil && !canViewAllDivisions(user) {
		rowDivisionIDs, err = a.scopedDivisionIDsForUser(user, true)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	rows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, rowDivisionIDs)
	if err != nil {
		log.Printf("list student payments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	activityRows, err := a.listStudentMonthlyPaymentActivityByDivisionIDs(paymentActivityFrom, paymentActivityTo, rowDivisionIDs)
	if err != nil {
		log.Printf("list student payment activity: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	programOptions := studentPaymentProgramOptions(rows)
	filteredRows := filterStudentPaymentRows(rows, paymentSearch, paymentStatus, paymentProgram, paymentMethod)
	filteredActivityRows := filterStudentPaymentActivityRows(activityRows, paymentSearch, paymentProgram, paymentMethod)

	if format == "csv" {
		a.exportStudentPaymentActivityCSV(w, filteredActivityRows, paymentMonth, paymentActivityFrom, paymentActivityTo)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Payments"
	data.Description = "Collect and track individual monthly student payments."
	data.StudentPaymentRows = filteredRows
	data.PaymentMonth = paymentMonth
	data.PaymentMonthLabel = paymentMonthLabel(paymentMonth)
	data.PaymentCollectionOpen = paymentMonthCollectible(paymentMonth, time.Now())
	data.PaymentCollectionNotice = monthlyPaymentCollectionNotice(paymentMonth, time.Now())
	data.PaymentSearch = paymentSearch
	data.PaymentStatusFilter = paymentStatus
	data.PaymentProgramFilter = paymentProgram
	data.PaymentMethodFilter = paymentMethod
	data.PaymentActivityFrom = paymentActivityFrom
	data.PaymentActivityTo = paymentActivityTo
	data.PaymentProgramOptions = programOptions
	data.PaymentActivityRows = filteredActivityRows
	data.TodayDate = time.Now().Format("2006-01")
	data.PaymentPDFOrientation = pdfOrientation
	data.PaymentPDFPaperSize = pdfPaperSize
	data.PaymentPDFDensity = pdfDensity
	data.PaymentPDFIncludeSummary = pdfIncludeSummary
	data.PaymentPDFIncludeRegister = pdfIncludeRegister
	data.PaymentPDFIncludeActivity = pdfIncludeActivity
	data.PaymentPDFIncludeFilters = pdfIncludeFilters
	data.PaymentPDFAutoPrint = pdfAutoPrint
	if selectedDivision != nil {
		data.SelectedDivision = selectedDivision
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	for _, row := range filteredRows {
		if row.MonthlyFee > 0 {
			data.PaymentTotalDue = normalizeMoney(data.PaymentTotalDue + row.MonthlyFee)
		}
		data.PaymentCollected = normalizeMoney(data.PaymentCollected + row.CollectedAmount)
		switch studentPaymentRowStatus(row) {
		case "paid":
			data.PaymentPaidCount++
		case "partial":
			data.PaymentPartialCount++
		case "free":
			data.PaymentFreeCount++
		case "unconfigured":
			data.PaymentUnconfiguredCount++
		default:
			data.PaymentPendingCount++
		}
		if row.OutstandingAmount > 0 {
			data.PaymentOutstanding = normalizeMoney(data.PaymentOutstanding + row.OutstandingAmount)
		}
	}
	for _, row := range filteredActivityRows {
		if row.Payment.Voided {
			data.PaymentActivityVoidedCount++
			continue
		}
		data.PaymentActivityCollected = normalizeMoney(data.PaymentActivityCollected + row.Payment.Amount)
		data.PaymentActivityDiscounted = normalizeMoney(data.PaymentActivityDiscounted + row.Payment.DiscountAmount)
	}
	if !data.PaymentPDFIncludeSummary && !data.PaymentPDFIncludeRegister && !data.PaymentPDFIncludeActivity {
		data.PaymentPDFIncludeSummary = true
		data.PaymentPDFIncludeRegister = true
		data.PaymentPDFIncludeActivity = true
	}
	if format == "pdf" {
		data.HideChrome = true
		data.Title = "Student Payments Report"
		a.render(w, "student-payments-print", data, http.StatusOK)
		return
	}
	a.render(w, "student-payments", data, http.StatusOK)
}

func (a *App) financeReceiptHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	rawID := strings.TrimSpace(r.URL.Query().Get("transaction_id"))
	if rawID == "" {
		rawID = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	transactionID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || transactionID <= 0 {
		http.Error(w, "invalid transaction id", http.StatusBadRequest)
		return
	}

	transaction, err := a.findFinanceTransactionByID(transactionID)
	if err != nil {
		log.Printf(
			"find finance transaction for receipt %d: %v",
			transactionID,
			err,
		)
		http.Error(w, "receipt not found", http.StatusNotFound)
		return
	}
	canViewAdmission := transaction.Category == "admission_payment" && containsPermission(user.Permissions, "admissions.view")
	canViewBooking := transaction.Category == "booking_payment" && (containsPermission(user.Permissions, "finance.view") || containsPermission(user.Permissions, "space_bookings.view") || containsPermission(user.Permissions, "booking_requests.view"))
	canViewFinance := containsPermission(user.Permissions, "finance.view")
	if user == nil || (!canViewAdmission && !canViewBooking && !canViewFinance) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, user, transaction.DivisionID) {
		return
	}

	var receiptAdmission *Admission
	var receiptEnrollment *StudentEnrollment
	if transaction.ReferenceType == "admission" && transaction.ReferenceID > 0 {
		receiptAdmission, _ = a.findAdmissionByID(transaction.ReferenceID)
	} else if transaction.ReferenceType == "student_enrollment" && transaction.ReferenceID > 0 {
		receiptEnrollment, _ = a.findStudentEnrollmentByID(transaction.ReferenceID)
		if receiptEnrollment != nil {
			receiptAdmission = &receiptEnrollment.Student
		}
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Payment Receipt"
	data.Description = "Printable receipt."
	data.HideChrome = true
	data.SelectedFinance = transaction
	data.ReceiptAdmission = receiptAdmission
	data.ReceiptEnrollment = receiptEnrollment
	if transaction.Category == "booking_payment" && transaction.ReferenceID > 0 {
		collections, _ := a.listBookingPaymentCollectionsForScheduleIDs([]int64{transaction.ReferenceID})
		for _, collection := range collections {
			if collection.FinanceTransactionID == transaction.ID {
				data.ReceiptBookingPayment = &collection
				break
			}
		}
		data.ReceiptBookingSchedule, _ = a.findSpaceScheduleByID(transaction.ReferenceID)
		financials, _ := a.listBookingFinancialsForScheduleIDs([]int64{transaction.ReferenceID})
		data.ReceiptBookingFinancial = bookingFinancialForSchedule(financials, transaction.ReferenceID)
		data.BookingStatusView = &BookingStatusView{
			ContactPhone: a.bookingMessages.ContactPhone,
			ContactEmail: a.bookingMessages.ContactEmail,
			VenueName:    a.bookingMessages.VenueName,
			VenueAddress: a.bookingMessages.VenueAddress,
		}
	}
	a.render(w, "finance-receipt", data, http.StatusOK)
}
