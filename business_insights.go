package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) businessInsightsHandler(w http.ResponseWriter, r *http.Request) {
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	anchor := time.Now().In(time.Local)
	if parsed, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
		anchor = parsed
	}
	insights, err := a.buildBusinessInsights(user, selectedDivision, scopeDivisionIDs, anchor)
	if err != nil {
		log.Printf("business insights: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Business Insights"
	data.Description = "Executive business intelligence, forecasts, risks, and action priorities."
	if r.URL.Query().Get("format") == "pdf" {
		data.HideChrome = true
	}
	data.SelectedDivision = selectedDivision
	if selectedDivision != nil {
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	data.BusinessInsights = insights
	a.render(w, "business-insights", data, http.StatusOK)
}

func (a *App) businessInsightsExportHandler(w http.ResponseWriter, r *http.Request) {
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}
	anchor := time.Now().In(time.Local)
	if parsed, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
		anchor = parsed
	}
	insights, err := a.buildBusinessInsights(user, selectedDivision, scopeDivisionIDs, anchor)
	if err != nil {
		log.Printf("business insights export: %v", err)
		http.Error(w, "could not export business insights", http.StatusInternalServerError)
		return
	}
	if err := writeBusinessInsightsCSV(w, insights); err != nil {
		log.Printf("write business insights export: %v", err)
	}
}

func (a *App) businessBreakdownHandler(w http.ResponseWriter, r *http.Request) {
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	breakdown, err := a.buildBusinessBreakdown(user, selectedDivision, scopeDivisionIDs, strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to")))
	if err != nil {
		log.Printf("business breakdown: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Business Breakdown"
	data.Description = "Income, expense, margin, and loss-pressure breakdown for the selected period."
	data.SelectedDivision = selectedDivision
	if selectedDivision != nil {
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	data.BusinessBreakdown = breakdown
	a.render(w, "business-breakdown", data, http.StatusOK)
}

func (a *App) businessBreakdownDetailHandler(w http.ResponseWriter, r *http.Request) {
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
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	detail, err := a.buildBusinessBreakdownDetail(
		user,
		selectedDivision,
		scopeDivisionIDs,
		strings.TrimSpace(r.URL.Query().Get("from")),
		strings.TrimSpace(r.URL.Query().Get("to")),
		strings.TrimSpace(r.URL.Query().Get("type")),
		strings.TrimSpace(r.URL.Query().Get("key")),
	)
	if err != nil {
		if errors.Is(err, errBusinessBreakdownDetailNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("business breakdown detail: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = detail.Label + " Breakdown"
	data.Description = "Complete transaction detail for a business breakdown line."
	data.SelectedDivision = selectedDivision
	if selectedDivision != nil {
		data.SelectedDivisionScope = selectedDivision.Slug
	}
	data.BusinessBreakdownDetail = detail
	a.render(w, "business-breakdown-detail", data, http.StatusOK)
}

func (a *App) buildBusinessInsights(user *User, selectedDivision *Division, divisionIDs []int64, anchor time.Time) (*BusinessInsights, error) {
	currentPeriod := reportMonthPeriod(anchor)
	previousPeriod := reportMonthPeriod(anchor.AddDate(0, -1, 0))
	current, err := a.buildOperationalReport(currentPeriod, divisionIDs)
	if err != nil {
		return nil, err
	}
	previous, err := a.buildOperationalReport(previousPeriod, divisionIDs)
	if err != nil {
		return nil, err
	}

	reports := make([]*OperationalReport, 0, 6)
	for i := 5; i >= 0; i-- {
		period := reportMonthPeriod(anchor.AddDate(0, -i, 0))
		report, err := a.buildOperationalReport(period, divisionIDs)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	studentOutstanding, err := a.businessStudentOutstanding(currentPeriod, divisionIDs)
	if err != nil {
		return nil, err
	}
	payrollCommitments, err := a.businessPayrollCommitments(currentPeriod, divisionIDs)
	if err != nil {
		return nil, err
	}
	bookingPipeline, err := a.businessBookingPipeline(currentPeriod, divisionIDs)
	if err != nil {
		return nil, err
	}

	insights := &BusinessInsights{
		PeriodLabel:        currentPeriod.Label,
		GeneratedAt:        time.Now().In(time.Local).Format("02 Jan 2006 15:04"),
		ScopeLabel:         businessInsightScopeLabel(user, selectedDivision),
		Current:            current,
		Previous:           previous,
		StudentOutstanding: studentOutstanding,
		PayrollCommitments: payrollCommitments,
		BookingPipeline:    bookingPipeline,
	}
	insights.KPIs = buildBusinessInsightKPIs(current, previous, studentOutstanding, payrollCommitments, bookingPipeline)
	insights.Trends = buildBusinessInsightTrends(current, previous)
	insights.Forecasts = buildBusinessInsightForecasts(reports, studentOutstanding, payrollCommitments, bookingPipeline)
	insights.Months = buildBusinessInsightMonths(reports)
	insights.RevenueMix = buildBusinessRevenueMix(current)
	insights.OperationalMix = buildBusinessOperationalMix(current)
	insights.StrategicInputs = buildBusinessStrategicInputs(studentOutstanding, payrollCommitments, bookingPipeline)
	insights.Risks = buildBusinessInsightRisks(current, previous, studentOutstanding, payrollCommitments)
	insights.Actions = buildBusinessInsightActions(current)
	insights.ExecutiveSummary = buildBusinessExecutiveSummary(current, previous)
	insights.ForecastNarrative = "Forecasts use recent monthly run-rate, latest direction, open receivables, booking pipeline, and unpaid payroll commitments. Treat them as a planning signal for staffing, cash control, collections, and capacity decisions."
	return insights, nil
}

func (a *App) buildBusinessBreakdown(user *User, selectedDivision *Division, divisionIDs []int64, fromRaw, toRaw string) (*BusinessBreakdown, error) {
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
	studentPaymentMonths, err := a.businessStudentPaymentMonths(businessBreakdownStudentPaymentSourceIDs(transactions))
	if err != nil {
		return nil, err
	}

	breakdown := &BusinessBreakdown{
		From:            from,
		To:              to,
		PreviousFrom:    prevFrom,
		PreviousTo:      prevTo,
		PeriodLabel:     fmt.Sprintf("%s to %s", fromDate.Format("02 Jan 2006"), toDate.Format("02 Jan 2006")),
		ComparisonLabel: fmt.Sprintf("%s to %s", previousFrom.Format("02 Jan 2006"), previousTo.Format("02 Jan 2006")),
		GeneratedAt:     time.Now().In(time.Local).Format("02 Jan 2006 15:04"),
		ScopeLabel:      businessInsightScopeLabel(user, selectedDivision),
	}
	revenueGroups := map[string]*businessBreakdownAccumulator{}
	expenseGroups := map[string]*businessBreakdownAccumulator{}
	sourceGroups := map[string]*businessBreakdownAccumulator{}

	for _, transaction := range transactions {
		if !financeOperatingTransaction(transaction) {
			continue
		}
		inCurrent := financeTransactionWithinLocalDates(transaction.RecordedAt, fromDate, toDate)
		inPrevious := financeTransactionWithinLocalDates(transaction.RecordedAt, previousFrom, previousTo)
		if !inCurrent && !inPrevious {
			continue
		}

		amount := absMoney(transaction.Amount)
		categoryKey := strings.TrimSpace(transaction.Category)
		if categoryKey == "" {
			categoryKey = "uncategorised"
		}
		sourceKey := businessBreakdownSourceKey(transaction)
		sourceLabel := businessBreakdownSourceLabel(transaction)
		if transaction.Amount >= 0 {
			acc := businessBreakdownGetAccumulator(revenueGroups, categoryKey, financeCategoryLabel(categoryKey), businessBreakdownDetailHref("income", categoryKey, from, to))
			sourceAcc := businessBreakdownGetAccumulator(sourceGroups, sourceKey, sourceLabel, businessBreakdownDetailHref("source", sourceKey, from, to))
			if inCurrent {
				acc.Amount += amount
				acc.Count++
				sourceAcc.Amount += amount
				sourceAcc.Count++
				if transaction.SourceType == "student_monthly_payment" {
					paymentMonth := studentPaymentMonths[transaction.SourceID]
					businessBreakdownAddPaymentMonth(acc, paymentMonth, amount)
					businessBreakdownAddPaymentMonth(sourceAcc, paymentMonth, amount)
				}
				breakdown.TotalRevenue += amount
			}
			if inPrevious {
				acc.PreviousAmount += amount
				sourceAcc.PreviousAmount += amount
				breakdown.PreviousRevenue += amount
			}
		} else {
			acc := businessBreakdownGetAccumulator(expenseGroups, categoryKey, financeCategoryLabel(categoryKey), businessBreakdownDetailHref("expense", categoryKey, from, to))
			if inCurrent {
				acc.Amount += amount
				acc.Count++
				breakdown.TotalExpenses += amount
			}
			if inPrevious {
				acc.PreviousAmount += amount
				breakdown.PreviousExpenses += amount
			}
		}
	}

	breakdown.TotalRevenue = normalizeMoney(breakdown.TotalRevenue)
	breakdown.TotalExpenses = normalizeMoney(breakdown.TotalExpenses)
	breakdown.NetProfit = normalizeMoney(breakdown.TotalRevenue - breakdown.TotalExpenses)
	breakdown.PreviousRevenue = normalizeMoney(breakdown.PreviousRevenue)
	breakdown.PreviousExpenses = normalizeMoney(breakdown.PreviousExpenses)
	breakdown.PreviousNetProfit = normalizeMoney(breakdown.PreviousRevenue - breakdown.PreviousExpenses)
	breakdown.RevenueDelta = normalizeMoney(breakdown.TotalRevenue - breakdown.PreviousRevenue)
	breakdown.ExpenseDelta = normalizeMoney(breakdown.TotalExpenses - breakdown.PreviousExpenses)
	breakdown.NetProfitDelta = normalizeMoney(breakdown.NetProfit - breakdown.PreviousNetProfit)
	if breakdown.TotalRevenue > 0 {
		breakdown.ProfitMargin = normalizeMoney((breakdown.NetProfit / breakdown.TotalRevenue) * 100)
	}
	breakdown.RevenueLines = businessBreakdownLines(revenueGroups, breakdown.TotalRevenue, true)
	breakdown.ExpenseLines = businessBreakdownLines(expenseGroups, breakdown.TotalExpenses, false)
	breakdown.SourceLines = businessBreakdownLines(sourceGroups, breakdown.TotalRevenue, true)
	breakdown.LossPressureLines = businessLossPressureLines(breakdown.ExpenseLines, breakdown.TotalRevenue)
	if len(breakdown.RevenueLines) > 0 {
		breakdown.LargestRevenueLine = breakdown.RevenueLines[0].Label
	}
	if len(breakdown.ExpenseLines) > 0 {
		breakdown.LargestExpenseLine = breakdown.ExpenseLines[0].Label
	}
	if len(breakdown.LossPressureLines) > 0 {
		breakdown.LargestLossPressure = breakdown.LossPressureLines[0].Label
	}
	breakdown.Recommendations = buildBusinessBreakdownRecommendations(breakdown)
	breakdown.ExecutiveSummary = buildBusinessBreakdownSummary(breakdown)
	return breakdown, nil
}

var errBusinessBreakdownDetailNotFound = errors.New("business breakdown detail not found")

func (a *App) buildBusinessBreakdownDetail(user *User, selectedDivision *Division, divisionIDs []int64, fromRaw, toRaw, detailType, key string) (*BusinessBreakdownDetail, error) {
	fromDate, toDate, _, _, from, to, _, _, err := financeProfitAndLossPeriod(fromRaw, toRaw, time.Now())
	if err != nil {
		return nil, err
	}
	detailType = strings.ToLower(strings.TrimSpace(detailType))
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errBusinessBreakdownDetailNotFound
	}
	switch detailType {
	case "income", "expense", "source":
	default:
		return nil, errBusinessBreakdownDetailNotFound
	}

	filter := FinanceFilter{}
	if len(divisionIDs) > 0 {
		filter.DivisionIDs = append([]int64(nil), divisionIDs...)
	}
	transactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		return nil, err
	}

	rows := make([]BusinessBreakdownDetailRow, 0)
	sourceIDs := make([]int64, 0)
	for _, transaction := range transactions {
		if !financeOperatingTransaction(transaction) || !financeTransactionWithinLocalDates(transaction.RecordedAt, fromDate, toDate) {
			continue
		}
		if !businessBreakdownDetailMatches(transaction, detailType, key) {
			continue
		}
		amount := absMoney(transaction.Amount)
		rows = append(rows, BusinessBreakdownDetailRow{
			Transaction:      transaction,
			Amount:           amount,
			CollectedAtLabel: transaction.RecordedAt.In(time.Local).Format("02 Jan 2006 15:04"),
			PaymentForMonth:  "",
			PaymentForLabel:  "",
		})
		if transaction.SourceType == "student_monthly_payment" && transaction.SourceID > 0 {
			sourceIDs = append(sourceIDs, transaction.SourceID)
		}
	}

	paymentMonths, err := a.businessStudentPaymentMonths(sourceIDs)
	if err != nil {
		return nil, err
	}
	monthGroups := map[string]*businessBreakdownAccumulator{}
	total := 0.0
	for i := range rows {
		total += rows[i].Amount
		if rows[i].Transaction.SourceType != "student_monthly_payment" {
			continue
		}
		paymentMonth := paymentMonths[rows[i].Transaction.SourceID]
		if paymentMonth == "" {
			continue
		}
		rows[i].PaymentForMonth = paymentMonth
		rows[i].PaymentForLabel = paymentMonthLabel(paymentMonth)
		acc := businessBreakdownGetAccumulator(monthGroups, paymentMonth, paymentMonthLabel(paymentMonth), "")
		acc.Amount += rows[i].Amount
		acc.Count++
	}

	total = normalizeMoney(total)
	average := 0.0
	if len(rows) > 0 {
		average = normalizeMoney(total / float64(len(rows)))
	}
	detail := &BusinessBreakdownDetail{
		Type:            detailType,
		Key:             key,
		Label:           businessBreakdownDetailLabel(detailType, key, rows),
		From:            from,
		To:              to,
		PeriodLabel:     fmt.Sprintf("%s to %s", fromDate.Format("02 Jan 2006"), toDate.Format("02 Jan 2006")),
		ScopeLabel:      businessInsightScopeLabel(user, selectedDivision),
		TotalAmount:     total,
		EntryCount:      len(rows),
		AverageAmount:   average,
		PaymentMonthMix: businessBreakdownLines(monthGroups, total, true),
		Rows:            rows,
	}
	return detail, nil
}

func businessBreakdownDetailMatches(transaction FinanceTransaction, detailType, key string) bool {
	categoryKey := strings.TrimSpace(transaction.Category)
	if categoryKey == "" {
		categoryKey = "uncategorised"
	}
	switch detailType {
	case "income":
		return transaction.Amount >= 0 && categoryKey == key
	case "expense":
		return transaction.Amount < 0 && categoryKey == key
	case "source":
		return transaction.Amount >= 0 && businessBreakdownSourceKey(transaction) == key
	default:
		return false
	}
}

func businessBreakdownDetailLabel(detailType, key string, rows []BusinessBreakdownDetailRow) string {
	switch detailType {
	case "source":
		for _, row := range rows {
			if businessBreakdownSourceKey(row.Transaction) == key {
				return businessBreakdownSourceLabel(row.Transaction)
			}
		}
		return financeSourceTypeLabel(key)
	case "income", "expense":
		return financeCategoryLabel(key)
	default:
		return key
	}
}

func businessBreakdownDetailHref(detailType, key, from, to string) string {
	query := url.Values{}
	query.Set("type", strings.TrimSpace(detailType))
	query.Set("key", strings.TrimSpace(key))
	if strings.TrimSpace(from) != "" {
		query.Set("from", strings.TrimSpace(from))
	}
	if strings.TrimSpace(to) != "" {
		query.Set("to", strings.TrimSpace(to))
	}
	return "/admin/business-insights/breakdown/detail?" + query.Encode()
}

func (a *App) businessStudentPaymentMonths(ids []int64) (map[int64]string, error) {
	result := make(map[int64]string)
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(normalized))
	for _, id := range normalized {
		args = append(args, id)
	}
	rows, err := a.queryDB(`SELECT id, payment_month FROM student_monthly_payments WHERE `+financeInt64InClause("id", len(normalized)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var month string
		if err := rows.Scan(&id, &month); err != nil {
			return nil, err
		}
		result[id] = strings.TrimSpace(month)
	}
	return result, rows.Err()
}

func businessBreakdownStudentPaymentSourceIDs(transactions []FinanceTransaction) []int64 {
	ids := make([]int64, 0)
	for _, transaction := range transactions {
		if transaction.SourceType != "student_monthly_payment" || transaction.SourceID <= 0 {
			continue
		}
		ids = append(ids, transaction.SourceID)
	}
	return ids
}

func reportMonthPeriod(anchor time.Time) ReportPeriod {
	start := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, -1)
	return ReportPeriod{
		Kind:         "month",
		Anchor:       start.Format("2006-01-02"),
		Start:        start.Format("2006-01-02"),
		End:          end.Format("2006-01-02"),
		Label:        start.Format("January 2006"),
		PreviousDate: start.AddDate(0, -1, 0).Format("2006-01-02"),
		NextDate:     start.AddDate(0, 1, 0).Format("2006-01-02"),
	}
}

func businessInsightScopeLabel(user *User, selectedDivision *Division) string {
	if selectedDivision != nil {
		return selectedDivision.Name
	}
	if canViewAllDivisions(user) {
		return "All Mekmaa divisions"
	}
	return "Authorized workspace"
}

func (a *App) businessStudentOutstanding(period ReportPeriod, divisionIDs []int64) (float64, error) {
	paymentMonth := period.Start[:7]
	rows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, divisionIDs)
	if err != nil {
		return 0, err
	}
	return financeStudentOutstanding(pendingStudentPaymentRows(rows)), nil
}

func (a *App) businessPayrollCommitments(period ReportPeriod, divisionIDs []int64) (float64, error) {
	query := `
		SELECT COALESCE(SUM(pp.net_amount), 0)
		FROM payroll_payments pp
		JOIN payroll_runs pr ON pr.id = pp.payroll_run_id
		LEFT JOIN training_programs tp ON tp.id = pp.training_program_id
		WHERE pp.status IN ('draft', 'calculated', 'approved')
		  AND CAST(pr.period_start AS TEXT) <= ?
		  AND CAST(pr.period_end AS TEXT) >= ?
	`
	args := []any{period.End, period.Start}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND (pp.division_id IN (` + placeholders + `) OR tp.division_id IN (` + placeholders + `))`
		args = append(args, scopeArgs...)
		args = append(args, scopeArgs...)
	}
	var total float64
	if err := a.queryRowDB(query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return normalizeMoney(total), nil
}

func (a *App) businessBookingPipeline(period ReportPeriod, divisionIDs []int64) (float64, error) {
	allowed, err := a.scopeIncludesSportsDivision(divisionIDs)
	if err != nil || !allowed {
		return 0, err
	}
	var total float64
	if err := a.queryRowDB(`
		SELECT COALESCE(SUM(bf.quoted_amount), 0)
		FROM booking_financials bf
		JOIN space_schedules ss ON ss.id = bf.schedule_id
		WHERE ss.status IN ('pending', 'held', 'reschedule_pending')
		  AND ss.slot_date BETWEEN ? AND ?
	`, period.Start, period.End).Scan(&total); err != nil {
		return 0, err
	}
	return normalizeMoney(total), nil
}

func buildBusinessInsightKPIs(current, previous *OperationalReport, studentOutstanding float64, payrollCommitments float64, bookingPipeline float64) []BusinessInsightKPI {
	return []BusinessInsightKPI{
		{Label: "Revenue", Value: money(current.Summary.Income), Note: changeSentence("vs previous month", current.Summary.Income, previous.Summary.Income), Tone: positiveWhenUp(current.Summary.Income, previous.Summary.Income)},
		{Label: "Net cash", Value: money(current.Summary.NetCash), Note: changeSentence("operating movement", current.Summary.NetCash, previous.Summary.NetCash), Tone: positiveWhenUp(current.Summary.NetCash, previous.Summary.NetCash)},
		{Label: "Utilization", Value: fmt.Sprintf("%.1f%%", current.Summary.UtilizationRate), Note: fmt.Sprintf("%d of %d slot hours", current.Summary.OccupiedSlotHours, current.Summary.AvailableSlotHours), Tone: utilizationTone(current.Summary.UtilizationRate)},
		{Label: "Attendance", Value: fmt.Sprintf("%.1f%%", current.Summary.AttendanceRate), Note: fmt.Sprintf("%d present from %d records", current.Summary.AttendancePresent, current.Summary.AttendanceTotal), Tone: attendanceTone(current.Summary.AttendanceRate)},
		{Label: "Receivables", Value: money(studentOutstanding), Note: "Outstanding student fee exposure.", Tone: riskTone(studentOutstanding > 0)},
		{Label: "Commitments", Value: money(payrollCommitments), Note: "Draft, calculated, and approved unpaid payroll.", Tone: riskTone(payrollCommitments > current.Summary.NetCash && payrollCommitments > 0)},
		{Label: "Pipeline", Value: money(bookingPipeline), Note: fmt.Sprintf("%d pending bookings to convert.", current.Summary.PendingBookings), Tone: positiveWhenUp(bookingPipeline, 0)},
		{Label: "New admissions", Value: fmt.Sprintf("%d", current.Summary.NewAdmissions), Note: changeSentence("enrollment momentum", float64(current.Summary.NewAdmissions), float64(previous.Summary.NewAdmissions)), Tone: positiveWhenUp(float64(current.Summary.NewAdmissions), float64(previous.Summary.NewAdmissions))},
	}
}

func buildBusinessInsightTrends(current, previous *OperationalReport) []BusinessInsightTrend {
	return []BusinessInsightTrend{
		trend("Revenue", current.Summary.Income, previous.Summary.Income, true),
		trend("Expenses", current.Summary.Expenses, previous.Summary.Expenses, false),
		trend("Net cash", current.Summary.NetCash, previous.Summary.NetCash, true),
		trend("Booking revenue", current.Summary.BookingRevenue, previous.Summary.BookingRevenue, true),
		trend("Student fee revenue", current.Summary.StudentRevenue, previous.Summary.StudentRevenue, true),
	}
}

func buildBusinessInsightForecasts(reports []*OperationalReport, studentOutstanding float64, payrollCommitments float64, bookingPipeline float64) []BusinessInsightForecast {
	income := make([]float64, 0, len(reports))
	expenses := make([]float64, 0, len(reports))
	bookings := make([]float64, 0, len(reports))
	admissions := make([]float64, 0, len(reports))
	for _, report := range reports {
		income = append(income, report.Summary.Income)
		expenses = append(expenses, report.Summary.Expenses)
		bookings = append(bookings, float64(report.Summary.ConfirmedBookings))
		admissions = append(admissions, float64(report.Summary.NewAdmissions))
	}
	revenueForecast := forecast("Revenue forecast", income, "LKR", "Recent income run-rate, current growth direction, open booking pipeline, and collectible student balances.")
	revenueForecast.ForecastValue = normalizeMoney(revenueForecast.ForecastValue + bookingPipeline*0.35 + studentOutstanding*0.2)
	revenueForecast.Value = money(revenueForecast.ForecastValue)
	revenueForecast.ChangePercent = percentChange(revenueForecast.ForecastValue, revenueForecast.CurrentValue)
	revenueForecast.ProgressWidth = reportBarWidth(revenueForecast.ForecastValue, math.Max(revenueForecast.ForecastValue, revenueForecast.CurrentValue))

	expenseForecast := forecast("Expense forecast", expenses, "LKR", "Recurring cost pattern, latest outflow direction, and unpaid payroll commitments.")
	expenseForecast.ForecastValue = normalizeMoney(expenseForecast.ForecastValue + payrollCommitments)
	expenseForecast.Value = money(expenseForecast.ForecastValue)
	expenseForecast.ChangePercent = percentChange(expenseForecast.ForecastValue, expenseForecast.CurrentValue)
	expenseForecast.ProgressWidth = reportBarWidth(expenseForecast.ForecastValue, math.Max(expenseForecast.ForecastValue, expenseForecast.CurrentValue))

	return []BusinessInsightForecast{
		revenueForecast,
		expenseForecast,
		forecast("Booking demand forecast", bookings, "count", "Confirmed booking volume across the recent monthly trend."),
		forecast("Admission forecast", admissions, "count", "New student acquisition momentum."),
	}
}

func buildBusinessInsightMonths(reports []*OperationalReport) []BusinessInsightMonth {
	maxIncome := 0.0
	maxNet := 0.0
	for _, report := range reports {
		maxIncome = math.Max(maxIncome, report.Summary.Income)
		maxNet = math.Max(maxNet, math.Abs(report.Summary.NetCash))
	}
	months := make([]BusinessInsightMonth, 0, len(reports))
	for _, report := range reports {
		start, _ := time.ParseInLocation("2006-01-02", report.Period.Start, time.Local)
		months = append(months, BusinessInsightMonth{
			Label:       start.Format("Jan"),
			Income:      report.Summary.Income,
			Expenses:    report.Summary.Expenses,
			NetCash:     report.Summary.NetCash,
			Bookings:    report.Summary.ConfirmedBookings,
			Admissions:  report.Summary.NewAdmissions,
			Attendance:  report.Summary.AttendanceRate,
			IncomeWidth: reportBarWidth(report.Summary.Income, maxIncome),
			NetWidth:    reportBarWidth(math.Abs(report.Summary.NetCash), maxNet),
		})
	}
	return months
}

func buildBusinessRevenueMix(report *OperationalReport) []BusinessInsightSegment {
	total := report.Summary.Income
	return []BusinessInsightSegment{
		{Label: "Bookings", Value: money(report.Summary.BookingRevenue), Note: percentOfRevenue(report.Summary.BookingRevenue, total), Tone: "positive"},
		{Label: "Student fees", Value: money(report.Summary.StudentRevenue), Note: percentOfRevenue(report.Summary.StudentRevenue, total), Tone: "neutral"},
		{Label: "Admissions", Value: money(report.Summary.AdmissionRevenue), Note: percentOfRevenue(report.Summary.AdmissionRevenue, total), Tone: "neutral"},
	}
}

func buildBusinessOperationalMix(report *OperationalReport) []BusinessInsightSegment {
	return []BusinessInsightSegment{
		{Label: "Confirmed bookings", Value: fmt.Sprintf("%d", report.Summary.ConfirmedBookings), Note: "Revenue-generating facility activity.", Tone: "positive"},
		{Label: "Pending bookings", Value: fmt.Sprintf("%d", report.Summary.PendingBookings), Note: "Conversion queue for operations.", Tone: riskTone(report.Summary.PendingBookings > 0)},
		{Label: "Student payments", Value: fmt.Sprintf("%d", report.Summary.StudentPayments), Note: "Monthly fee collection events.", Tone: "neutral"},
		{Label: "Attendance records", Value: fmt.Sprintf("%d", report.Summary.AttendanceTotal), Note: "Student attendance signal depth.", Tone: "neutral"},
	}
}

func buildBusinessStrategicInputs(studentOutstanding float64, payrollCommitments float64, bookingPipeline float64) []BusinessInsightSegment {
	return []BusinessInsightSegment{
		{Label: "Student receivables", Value: money(studentOutstanding), Note: "Collectible fee exposure in the current monthly register.", Tone: riskTone(studentOutstanding > 0)},
		{Label: "Payroll commitments", Value: money(payrollCommitments), Note: "Unpaid draft, calculated, or approved payroll inside the month.", Tone: riskTone(payrollCommitments > 0)},
		{Label: "Booking pipeline value", Value: money(bookingPipeline), Note: "Quoted value attached to pending, held, or reschedule-sensitive bookings.", Tone: "positive"},
	}
}

func buildBusinessInsightRisks(current, previous *OperationalReport, studentOutstanding float64, payrollCommitments float64) []BusinessInsightRisk {
	risks := make([]BusinessInsightRisk, 0, 5)
	if current.Summary.NetCash < 0 {
		risks = append(risks, BusinessInsightRisk{Severity: "High", Title: "Negative monthly cash movement", Body: "Expenses are above posted income for the selected month.", Action: "Review ledger and cost categories", Href: "/admin/finance/ledger"})
	}
	if current.Summary.UtilizationRate < 45 {
		risks = append(risks, BusinessInsightRisk{Severity: "Medium", Title: "Capacity under-utilization", Body: "Available court or session capacity is not converting into confirmed usage.", Action: "Inspect booking calendar", Href: "/admin/bookings"})
	}
	if current.Summary.AttendanceTotal > 0 && current.Summary.AttendanceRate < 80 {
		risks = append(risks, BusinessInsightRisk{Severity: "Medium", Title: "Attendance below healthy operating level", Body: "Attendance softness can lead to churn, payment friction, and weaker programme outcomes.", Action: "Open attendance reports", Href: "/admin/reports?domain=attendance&period=month"})
	}
	if current.Summary.PendingBookings > 0 {
		risks = append(risks, BusinessInsightRisk{Severity: "Low", Title: "Pending demand waiting for action", Body: "Booking requests are visible but not yet converted into confirmed revenue.", Action: "Open request inbox", Href: "/admin/booking-requests"})
	}
	if studentOutstanding > 0 {
		risks = append(risks, BusinessInsightRisk{Severity: "Medium", Title: "Student receivables need collection focus", Body: "Open student balances are part of the next cash conversion plan.", Action: "Open student payments", Href: "/admin/student-payments"})
	}
	if payrollCommitments > current.Summary.NetCash && payrollCommitments > 0 {
		risks = append(risks, BusinessInsightRisk{Severity: "High", Title: "Payroll commitments exceed current net cash", Body: "Unpaid payroll exposure is above the current month net cash movement.", Action: "Open salary payments", Href: "/admin/staff/salary-payments"})
	}
	if previous.Summary.Income > 0 && current.Summary.Income < previous.Summary.Income*0.9 {
		risks = append(risks, BusinessInsightRisk{Severity: "Medium", Title: "Revenue dropped more than 10%", Body: "The current month is materially behind the previous month.", Action: "Review business reports", Href: "/admin/reports?period=month"})
	}
	if len(risks) == 0 {
		risks = append(risks, BusinessInsightRisk{Severity: "Stable", Title: "No critical operating risk detected", Body: "Core finance, attendance, and booking signals are within normal review range.", Action: "Continue monitoring", Href: "/admin/reports"})
	}
	return risks
}

func writeBusinessInsightsCSV(w http.ResponseWriter, insights *BusinessInsights) error {
	writer := newCSVReportWriter(w, "mekmaa-business-insights-"+insights.Current.Period.Anchor+".csv")
	defer writer.Flush()

	if err := writeCSVReportPreamble(
		writer,
		"Mekmaa Business Insights",
		CSVReportMetaRow{Section: "report", Field: "Scope", Value: insights.ScopeLabel},
		CSVReportMetaRow{Section: "period", Field: "Label", Value: insights.PeriodLabel},
		CSVReportMetaRow{Section: "period", Field: "From", Value: insights.Current.Period.Start},
		CSVReportMetaRow{Section: "period", Field: "To", Value: insights.Current.Period.End},
	); err != nil {
		return err
	}

	_ = writer.Write([]string{})
	_ = writer.Write([]string{"EXECUTIVE SUMMARY", insights.ExecutiveSummary})
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"KPI", "VALUE", "NOTE", "TONE"})
	for _, row := range insights.KPIs {
		_ = writer.Write([]string{row.Label, row.Value, row.Note, row.Tone})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"FORECAST", "VALUE", "DIRECTION", "CONFIDENCE", "CHANGE %", "DRIVER"})
	for _, row := range insights.Forecasts {
		_ = writer.Write([]string{row.Label, row.Value, row.Direction, row.Confidence, formatReportNumber(row.ChangePercent), row.Driver})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"STRATEGIC INPUT", "VALUE", "NOTE", "TONE"})
	for _, row := range insights.StrategicInputs {
		_ = writer.Write([]string{row.Label, row.Value, row.Note, row.Tone})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"MONTH", "INCOME", "EXPENSES", "NET CASH", "BOOKINGS", "ADMISSIONS", "ATTENDANCE RATE"})
	for _, row := range insights.Months {
		_ = writer.Write([]string{row.Label, formatReportNumber(row.Income), formatReportNumber(row.Expenses), formatReportNumber(row.NetCash), strconv.Itoa(row.Bookings), strconv.Itoa(row.Admissions), formatReportNumber(row.Attendance)})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"RISK", "SEVERITY", "BODY", "ACTION"})
	for _, row := range insights.Risks {
		_ = writer.Write([]string{row.Title, row.Severity, row.Body, row.Action})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"ACTION", "BODY", "LINK"})
	for _, row := range insights.Actions {
		_ = writer.Write([]string{row.Title, row.Body, row.Href})
	}
	return writer.Error()
}

func buildBusinessInsightActions(report *OperationalReport) []BusinessInsightAction {
	return []BusinessInsightAction{
		{Title: "Protect cash position", Body: "Review the largest outflows and confirm every manual or operational posting is categorized correctly.", Label: "Open ledger", Href: "/admin/finance/ledger", Tone: "finance"},
		{Title: "Convert open demand", Body: "Move pending booking requests into confirmed schedules before customers lose intent.", Label: "Open requests", Href: "/admin/booking-requests", Tone: "operations"},
		{Title: "Lift programme retention", Body: "Use attendance softness and payment coverage to identify students who need staff follow-up.", Label: "Attendance view", Href: "/admin/reports?domain=attendance&period=month", Tone: "people"},
		{Title: "Plan next month capacity", Body: fmt.Sprintf("Use %.1f%% utilization and %d confirmed bookings to tune staffing, court availability, and campaign focus.", report.Summary.UtilizationRate, report.Summary.ConfirmedBookings), Label: "Booking calendar", Href: "/admin/bookings", Tone: "capacity"},
	}
}

type businessBreakdownAccumulator struct {
	Code           string
	Label          string
	Href           string
	Amount         float64
	PreviousAmount float64
	Count          int
	PaymentMonths  map[string]*businessBreakdownAccumulator
}

func businessBreakdownGetAccumulator(groups map[string]*businessBreakdownAccumulator, code, label, href string) *businessBreakdownAccumulator {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "uncategorised"
	}
	if existing, ok := groups[code]; ok {
		return existing
	}
	acc := &businessBreakdownAccumulator{Code: code, Label: label, Href: href}
	groups[code] = acc
	return acc
}

func businessBreakdownAddPaymentMonth(group *businessBreakdownAccumulator, paymentMonth string, amount float64) {
	if group == nil {
		return
	}
	paymentMonth = strings.TrimSpace(paymentMonth)
	if paymentMonth == "" {
		return
	}
	if group.PaymentMonths == nil {
		group.PaymentMonths = map[string]*businessBreakdownAccumulator{}
	}
	acc := businessBreakdownGetAccumulator(group.PaymentMonths, paymentMonth, paymentMonthLabel(paymentMonth), "")
	acc.Amount += amount
	acc.Count++
}

func businessBreakdownSourceKey(transaction FinanceTransaction) string {
	if strings.TrimSpace(transaction.SourceType) != "" {
		return strings.TrimSpace(transaction.SourceType)
	}
	if strings.TrimSpace(transaction.ReferenceType) != "" {
		return strings.TrimSpace(transaction.ReferenceType)
	}
	return strings.TrimSpace(transaction.Category)
}

func businessBreakdownSourceLabel(transaction FinanceTransaction) string {
	switch businessBreakdownSourceKey(transaction) {
	case "admission":
		return "Admissions"
	case "student_enrollment", "student_monthly_payment":
		return "Student fees"
	case "booking_payment_collection", "space_schedule":
		return "Bookings"
	case "mcp_payment":
		return "MCP payments"
	case "tournament", "tournament_entry", "tournament_payment":
		return "Tournaments"
	case "manual":
		return "Manual income"
	default:
		return financeCategoryLabel(transaction.Category)
	}
}

func businessBreakdownLines(groups map[string]*businessBreakdownAccumulator, total float64, goodWhenUp bool) []BusinessBreakdownLine {
	lines := make([]BusinessBreakdownLine, 0, len(groups))
	maxAmount := 0.0
	for _, group := range groups {
		maxAmount = math.Max(maxAmount, group.Amount)
	}
	for _, group := range groups {
		amount := normalizeMoney(group.Amount)
		previous := normalizeMoney(group.PreviousAmount)
		delta := normalizeMoney(amount - previous)
		share := 0.0
		if total > 0 {
			share = normalizeMoney((amount / total) * 100)
		}
		average := 0.0
		if group.Count > 0 {
			average = normalizeMoney(amount / float64(group.Count))
		}
		lines = append(lines, BusinessBreakdownLine{
			Code:           group.Code,
			Label:          group.Label,
			Amount:         amount,
			PreviousAmount: previous,
			Delta:          delta,
			SharePercent:   share,
			Width:          reportBarWidth(amount, maxAmount),
			Count:          group.Count,
			Average:        average,
			Note:           businessBreakdownLineNote(amount, previous, goodWhenUp),
			Tone:           businessBreakdownDeltaTone(delta, goodWhenUp),
			Href:           group.Href,
			PaymentMonths:  businessBreakdownLines(group.PaymentMonths, amount, true),
		})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Amount == lines[j].Amount {
			return lines[i].Label < lines[j].Label
		}
		return lines[i].Amount > lines[j].Amount
	})
	return lines
}

func businessLossPressureLines(expenseLines []BusinessBreakdownLine, revenue float64) []BusinessBreakdownLine {
	lines := make([]BusinessBreakdownLine, 0, len(expenseLines))
	for _, line := range expenseLines {
		pressure := 0.0
		if revenue > 0 {
			pressure = normalizeMoney((line.Amount / revenue) * 100)
		}
		note := "No posted revenue in this period."
		tone := "negative"
		if revenue > 0 {
			note = fmt.Sprintf("Consumes %.1f%% of period revenue.", pressure)
			if pressure < 15 {
				tone = "positive"
			} else if pressure < 35 {
				tone = "neutral"
			}
		}
		line.SharePercent = pressure
		line.Width = reportBarWidth(pressure, 100)
		line.Note = note
		line.Tone = tone
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].SharePercent == lines[j].SharePercent {
			return lines[i].Label < lines[j].Label
		}
		return lines[i].SharePercent > lines[j].SharePercent
	})
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return lines
}

func businessBreakdownLineNote(amount, previous float64, goodWhenUp bool) string {
	if moneyEquals(previous, 0) {
		if moneyEquals(amount, 0) {
			return "No movement in either period."
		}
		return "New movement versus the comparison period."
	}
	change := percentChange(amount, previous)
	if goodWhenUp {
		return fmt.Sprintf("%.1f%% versus comparison period.", change)
	}
	return fmt.Sprintf("%.1f%% expense movement versus comparison period.", change)
}

func businessBreakdownDeltaTone(delta float64, goodWhenUp bool) string {
	if moneyEquals(delta, 0) {
		return "neutral"
	}
	if goodWhenUp {
		if delta > 0 {
			return "positive"
		}
		return "negative"
	}
	if delta > 0 {
		return "negative"
	}
	return "positive"
}

func buildBusinessBreakdownRecommendations(breakdown *BusinessBreakdown) []BusinessBreakdownRecommendation {
	recommendations := make([]BusinessBreakdownRecommendation, 0, 4)
	if breakdown.NetProfit < 0 {
		recommendations = append(recommendations, BusinessBreakdownRecommendation{Title: "Stop the loss first", Body: "This period is running below break-even. Start with the largest expense line and any revenue source that fell versus comparison.", Href: "/admin/finance/ledger", Tone: "negative"})
	}
	if len(breakdown.LossPressureLines) > 0 {
		top := breakdown.LossPressureLines[0]
		recommendations = append(recommendations, BusinessBreakdownRecommendation{Title: "Review " + top.Label, Body: fmt.Sprintf("%s is the biggest cost pressure at %.1f%% of revenue.", top.Label, top.SharePercent), Href: top.Href, Tone: top.Tone})
	}
	if len(breakdown.RevenueLines) > 0 {
		top := breakdown.RevenueLines[0]
		recommendations = append(recommendations, BusinessBreakdownRecommendation{Title: "Protect " + top.Label, Body: fmt.Sprintf("%s contributes %.1f%% of period revenue. Keep collection, follow-up, and capacity aligned around this line.", top.Label, top.SharePercent), Href: top.Href, Tone: "positive"})
	}
	recommendations = append(recommendations, BusinessBreakdownRecommendation{Title: "Audit uncategorised movement", Body: "Open the ledger and make sure every material transaction has the right category before management decisions are made.", Href: "/admin/finance/ledger", Tone: "neutral"})
	return recommendations
}

func buildBusinessBreakdownSummary(breakdown *BusinessBreakdown) string {
	result := "profit"
	if breakdown.NetProfit < 0 {
		result = "loss"
	}
	return fmt.Sprintf("For %s, %s generated %s revenue, spent %s, and closed at a %s of %s with a %.1f%% margin. The largest income line is %s and the largest expense pressure is %s.",
		breakdown.PeriodLabel,
		breakdown.ScopeLabel,
		money(breakdown.TotalRevenue),
		money(breakdown.TotalExpenses),
		result,
		money(absMoney(breakdown.NetProfit)),
		breakdown.ProfitMargin,
		emptyFallback(breakdown.LargestRevenueLine, "not available"),
		emptyFallback(breakdown.LargestExpenseLine, "not available"),
	)
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func buildBusinessExecutiveSummary(current, previous *OperationalReport) string {
	return fmt.Sprintf("This month shows %s in posted revenue, %s in expenses, and %s net cash. Revenue is %s compared with the previous month, with %.1f%% utilization and %.1f%% attendance forming the core operating health signals.",
		money(current.Summary.Income),
		money(current.Summary.Expenses),
		money(current.Summary.NetCash),
		changeLabel(current.Summary.Income, previous.Summary.Income),
		current.Summary.UtilizationRate,
		current.Summary.AttendanceRate,
	)
}

func trend(label string, current, previous float64, higherIsGood bool) BusinessInsightTrend {
	tone := "neutral"
	if current != previous {
		if (current > previous) == higherIsGood {
			tone = "positive"
		} else {
			tone = "negative"
		}
	}
	return BusinessInsightTrend{Label: label, CurrentValue: money(current), PreviousValue: money(previous), ChangeLabel: changeLabel(current, previous), Tone: tone}
}

func forecast(label string, values []float64, unit string, driver string) BusinessInsightForecast {
	current := 0.0
	if len(values) > 0 {
		current = values[len(values)-1]
	}
	window := values
	if len(values) > 3 {
		window = values[len(values)-3:]
	}
	average := avg(window)
	growth := 0.0
	if len(values) >= 2 && values[len(values)-2] != 0 {
		growth = (values[len(values)-1] - values[len(values)-2]) / math.Abs(values[len(values)-2])
	}
	if growth > 0.25 {
		growth = 0.25
	}
	if growth < -0.25 {
		growth = -0.25
	}
	projected := average * (1 + growth)
	confidence := "Medium"
	if len(values) >= 4 && volatility(values) < 0.25 {
		confidence = "High"
	} else if len(values) < 3 || volatility(values) > 0.6 {
		confidence = "Low"
	}
	value := fmt.Sprintf("%.0f", projected)
	if unit == "LKR" {
		value = money(projected)
	}
	direction := "Flat"
	if projected > current*1.05 {
		direction = "Up"
	} else if projected < current*0.95 {
		direction = "Down"
	}
	return BusinessInsightForecast{
		Label: label, Value: value, Confidence: confidence, Direction: direction, Driver: driver,
		CurrentValue: current, ForecastValue: projected, ChangePercent: percentChange(projected, current),
		ProgressWidth: reportBarWidth(projected, math.Max(projected, current)),
	}
}

func avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func volatility(values []float64) float64 {
	if len(values) < 2 {
		return 1
	}
	mean := avg(values)
	if mean == 0 {
		return 1
	}
	total := 0.0
	for _, value := range values {
		delta := value - mean
		total += delta * delta
	}
	return math.Sqrt(total/float64(len(values))) / math.Abs(mean)
}

func percentChange(current, previous float64) float64 {
	if previous == 0 {
		if current == 0 {
			return 0
		}
		return 100
	}
	return (current - previous) / math.Abs(previous) * 100
}

func changeLabel(current, previous float64) string {
	change := percentChange(current, previous)
	if math.Abs(change) < 0.1 {
		return "flat"
	}
	if change > 0 {
		return fmt.Sprintf("+%.1f%%", change)
	}
	return fmt.Sprintf("%.1f%%", change)
}

func changeSentence(prefix string, current, previous float64) string {
	return fmt.Sprintf("%s: %s", prefix, changeLabel(current, previous))
}

func percentOfRevenue(value, total float64) string {
	if total <= 0 {
		return "0.0% of revenue"
	}
	return fmt.Sprintf("%.1f%% of revenue", value/total*100)
}

func positiveWhenUp(current, previous float64) string {
	if current > previous {
		return "positive"
	}
	if current < previous {
		return "negative"
	}
	return "neutral"
}

func utilizationTone(value float64) string {
	if value >= 70 {
		return "positive"
	}
	if value < 45 {
		return "negative"
	}
	return "neutral"
}

func attendanceTone(value float64) string {
	if value >= 90 {
		return "positive"
	}
	if value > 0 && value < 80 {
		return "negative"
	}
	return "neutral"
}

func riskTone(risky bool) string {
	if risky {
		return "negative"
	}
	return "neutral"
}
