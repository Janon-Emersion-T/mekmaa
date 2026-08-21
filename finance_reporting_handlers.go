package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) financeManagementHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
}

func (a *App) financeLedgerHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "ledger")
}

func (a *App) financeReceivablesHandler(w http.ResponseWriter, r *http.Request) {
	a.financeSectionHandler(w, r, "receivables")
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
		"Reference",
		"Counterparty",
		"Description",
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
				entry.ReferenceNumber,
				entry.Counterparty,
				entry.Description,
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
		if len(allowedDivisionIDs) == 1 {
			accountDivisionIDs = append(accountDivisionIDs, allowedDivisionIDs[0])
		} else if primary := userPrimaryDivision(user); primary != nil {
			accountDivisionIDs = []int64{primary.ID}
			if data.SelectedDivision == nil {
				data.SelectedDivision = primary
				data.SelectedDivisionScope = primary.Slug
			}
		}
	}

	needOperationalSummary := page == "ledger" || page == "specified-ledgers" || page == "transfers" || page == "reconciliations" || page == "accounts" || page == "profit-loss" || page == "balance-sheet"
	needAccounts := page == "ledger" || page == "transfers" || page == "reconciliations" || page == "accounts" || page == "balance-sheet"
	needAllTransactions := page == "ledger" || page == "specified-ledgers" || page == "accounts" || page == "transfers" || page == "reconciliations" || page == "profit-loss" || page == "balance-sheet"
	needBookingFinancials := page == "receivables" || page == "customers"
	needMonthlyRows := false // Student monthly fees are managed exclusively from /admin/student-payments.
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
		data.BookingPaymentCollections, _ = a.listBookingPaymentCollectionsForScheduleIDs(scheduleIDsFromFinancials(bookingFinancials))
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
		referrals, err := a.listBookingReferralsByDivisionIDs(scopeDivisionIDs)
		if err != nil {
			log.Printf("finance %s load failed: op=list referral payables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.BookingReferrals = referrals
		data.FinanceSummary = buildFinanceSummary(nil, nil, data.BookingFinancials, allMonthlyRows, referrals, nil)
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
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	paymentNote := strings.TrimSpace(r.FormValue("payment_note"))
	allowOverpayment := r.FormValue("allow_overpayment") == "1" || r.FormValue("allow_overpayment") == "on"
	returnTo := strings.TrimSpace(r.FormValue("return_to"))
	if err != nil || amountErr != nil || scheduleID <= 0 || !validPaymentMethod(paymentMethod) {
		http.Error(w, "select a valid booking payment method", http.StatusBadRequest)
		return
	}
	if returnTo == "" {
		returnTo = "/admin/finance/receivables"
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.collectBookingPayment(scheduleID, paymentMethod, amount, paymentNote, recordedBy, allowOverpayment)
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}
	report, err := a.buildOperationalReport(period, scopeDivisionIDs)
	if err != nil {
		log.Printf("build operational report: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Reports"
	data.Description = "Daily, weekly, and monthly performance reporting."
	data.SelectedDivision = selectedDivision
	if selectedDivision != nil {
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	data.Report = report
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}
	report, err := a.buildOperationalReport(period, scopeDivisionIDs)
	if err != nil {
		http.Error(w, "could not export report", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("mekmaa-%s-report-%s.csv", period.Kind, period.Anchor)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Mekmaa operational report", report.Period.Label})
	_ = writer.Write([]string{"Period", report.Period.Start, report.Period.End})
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"SUMMARY", "VALUE"})
	summaryRows := [][]string{
		{"Gross income (LKR)", formatReportNumber(report.Summary.Income)},
		{"Expenses (LKR)", formatReportNumber(report.Summary.Expenses)},
		{"Net cash (LKR)", formatReportNumber(report.Summary.NetCash)},
		{"Confirmed bookings", strconv.Itoa(report.Summary.ConfirmedBookings)},
		{"Pending bookings", strconv.Itoa(report.Summary.PendingBookings)},
		{"New admissions", strconv.Itoa(report.Summary.NewAdmissions)},
		{"Student payments", strconv.Itoa(report.Summary.StudentPayments)},
		{"Attendance rate", fmt.Sprintf("%.1f%%", report.Summary.AttendanceRate)},
		{"Facility utilization", fmt.Sprintf("%.1f%%", report.Summary.UtilizationRate)},
	}
	for _, row := range summaryRows {
		_ = writer.Write(row)
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"DAILY TREND", "DATE", "INCOME", "EXPENSES", "NET CASH", "BOOKINGS", "ADMISSIONS", "PRESENT", "ATTENDANCE RECORDS"})
	for _, point := range report.Series {
		_ = writer.Write([]string{
			"", point.Date, formatReportNumber(point.Income), formatReportNumber(point.Expenses),
			formatReportNumber(point.NetCash), strconv.Itoa(point.Bookings), strconv.Itoa(point.Admissions),
			strconv.Itoa(point.Present), strconv.Itoa(point.Attendance),
		})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"FINANCE BREAKDOWN", "CATEGORY", "TRANSACTIONS", "AMOUNT"})
	for _, item := range report.FinanceBreakdown {
		_ = writer.Write([]string{"", item.Label, strconv.Itoa(item.Count), formatReportNumber(item.Amount)})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"BOOKING MIX", "ACTIVITY", "CONFIRMED BOOKINGS"})
	for _, item := range report.BookingBreakdown {
		_ = writer.Write([]string{"", item.Label, strconv.Itoa(item.Count)})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"TRANSACTIONS", "RECEIPT", "DATE", "DIRECTION", "CATEGORY", "PARTY", "DESCRIPTION", "METHOD", "AMOUNT"})
	for _, transaction := range report.Transactions {
		direction := "Income"
		if transaction.Amount < 0 {
			direction = "Expense"
		}
		_ = writer.Write([]string{
			"", csvSafeCell(transaction.ReceiptNumber), formatDateTime(transaction.RecordedAt),
			direction, financeCategoryLabel(transaction.Category), csvSafeCell(transaction.PersonName),
			csvSafeCell(transaction.Description), csvSafeCell(transaction.PaymentMethod), formatReportNumber(transaction.Amount),
		})
	}
	writer.Flush()
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
	currentMonth := time.Now().Format("2006-01")
	latestMonth := latestCollectiblePaymentMonth(time.Now())
	if _, err := parsePaymentMonth(paymentMonth); err != nil || paymentMonth > currentMonth {
		paymentMonth = latestMonth
	}

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

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Payments"
	data.Description = "Collect and track individual monthly student payments."
	data.StudentPaymentRows = rows
	data.PaymentMonth = paymentMonth
	data.PaymentMonthLabel = paymentMonthLabel(paymentMonth)
	data.PaymentCollectionOpen = paymentMonthCollectible(paymentMonth, time.Now())
	data.PaymentCollectionNotice = monthlyPaymentCollectionNotice(paymentMonth, time.Now())
	data.TodayDate = time.Now().Format("2006-01")
	if selectedDivision != nil {
		data.SelectedDivision = selectedDivision
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	for _, row := range rows {
		if row.MonthlyFee > 0 {
			data.PaymentTotalDue += row.MonthlyFee
		}
		collectedAmount := row.CollectedAmount
		data.PaymentCollected += collectedAmount
		if row.MonthlyFee <= 0 {
			continue
		}
		if collectedAmount+0.004 >= row.MonthlyFee {
			data.PaymentPaidCount++
			continue
		}
		data.PaymentOutstanding += row.MonthlyFee - collectedAmount
		data.PaymentPendingCount++
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
	canViewAdmission := transaction.Category == "admission_payment" && containsPermission(user.Permissions, "admissions.manage")
	canViewBooking := transaction.Category == "booking_payment" && (containsPermission(user.Permissions, "finance.manage") || containsPermission(user.Permissions, "space_bookings.manage") || containsPermission(user.Permissions, "booking_requests.manage"))
	canViewFinance := containsPermission(user.Permissions, "finance.manage")
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
