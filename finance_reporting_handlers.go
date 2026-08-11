package main

import (
	"context"
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

	needAccounts := page == "ledger" || page == "transfers" || page == "reconciliations" || page == "accounts"
	needAllTransactions := page == "ledger" || page == "accounts"
	needBookingFinancials := page == "receivables"
	needMonthlyRows := page == "receivables"
	needTransfers := page == "transfers"
	needReconciliations := page == "reconciliations"

	if needAccounts {
		accounts, err := a.listFinanceAccounts(true)
		if err != nil {
			log.Printf("finance %s load failed: op=list finance accounts duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.FinanceAccounts = accounts
	}

	if needAllTransactions {
		allTransactions, err := a.listFinanceTransactions()
		if err != nil {
			log.Printf("finance %s load failed: op=list finance summary transactions duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.Stats = buildFinanceStats(allTransactions)
	}

	if page == "ledger" {
		filter := financeFilterFromRequest(r)
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
	}

	if needBookingFinancials {
		bookingFinancials, err := a.listOutstandingBookingFinancials()
		if err != nil {
			log.Printf("finance %s load failed: op=list booking financials duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.BookingFinancials = bookingFinancials
		data.BookingPaymentCollections, _ = a.listBookingPaymentCollectionsForScheduleIDs(scheduleIDsFromFinancials(bookingFinancials))
	}

	if needMonthlyRows {
		monthlyRows, err := a.listStudentPaymentRows(time.Now().Format("2006-01"))
		if err != nil {
			log.Printf("finance %s load failed: op=list monthly receivables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.StudentPaymentRows = monthlyRows
	}

	if page == "receivables" {
		referrals, err := a.listBookingReferrals()
		if err != nil {
			log.Printf("finance %s load failed: op=list referral payables duration=%s err=%v", page, time.Since(started), err)
			return data, err
		}
		data.BookingReferrals = referrals
		data.FinanceSummary = buildFinanceSummary(nil, nil, data.BookingFinancials, data.StudentPaymentRows, referrals, nil)
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
	if !validManualFinanceCategory(direction, category) {
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
	if personName == "" || description == "" || accountID <= 0 {
		http.Error(w, "person, description, and finance account are required", http.StatusBadRequest)
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
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.createManualFinanceTransactionForAccount(category, personName, description, strings.TrimSpace(r.FormValue("notes")), accountID, amount, recordedAt, recordedBy)
	if err != nil {
		log.Printf("create manual finance transaction: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
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
	if err != nil || amountErr != nil || scheduleID <= 0 || paymentMethod != "cash" {
		http.Error(w, "booking payments are recorded in cash only", http.StatusBadRequest)
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
	filter := financeFilterFromRequest(r)
	filter.Page = 0
	filter.Limit = 0
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
			csvSafeCell(transaction.ReceiptNumber), transaction.RecordedAt.Format("2006-01-02 15:04"), direction,
			financeCategoryLabel(transaction.Category), csvSafeCell(transaction.PersonName), csvSafeCell(transaction.Description),
			csvSafeCell(transaction.PaymentMethod), strconv.FormatFloat(transaction.Amount, 'f', 2, 64),
		})
	}
	writer.Flush()
}

func (a *App) reportsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	period := reportPeriodFromRequest(r)
	report, err := a.buildOperationalReport(period)
	if err != nil {
		log.Printf("build operational report: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Reports"
	data.Description = "Daily, weekly, and monthly performance reporting."
	data.Report = report
	a.render(w, "reports", data, http.StatusOK)
}

func (a *App) reportsExportHandler(w http.ResponseWriter, r *http.Request) {
	period := reportPeriodFromRequest(r)
	report, err := a.buildOperationalReport(period)
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
			"", csvSafeCell(transaction.ReceiptNumber), transaction.RecordedAt.Format("2006-01-02 15:04"),
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
	if _, err := parsePaymentMonth(paymentMonth); err != nil || paymentMonth > currentMonth {
		paymentMonth = time.Now().Format("2006-01")
	}

	rows, err := a.listStudentPaymentRows(paymentMonth)
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
	data.TodayDate = time.Now().Format("2006-01")
	selectedEnrollmentID := parseInt64Query(r.URL.Query().Get("enrollment_id"))
	if selectedEnrollmentID > 0 {
		selectedEnrollment, err := a.findStudentEnrollmentByID(selectedEnrollmentID)
		if err == nil {
			data.SelectedEnrollment = selectedEnrollment
			leaves, err := a.listStudentEnrollmentLeaves(selectedEnrollmentID)
			if err == nil {
				data.EnrollmentLeaves = leaves
			}
		}
	}
	for _, row := range rows {
		if row.MonthlyFee > 0 {
			data.PaymentTotalDue += row.MonthlyFee
		}
		collectedAmount := 0.0
		if row.Payment != nil {
			collectedAmount = row.Payment.Amount
			data.PaymentCollected += collectedAmount
		}
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
