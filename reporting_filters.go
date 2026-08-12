package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func reportPeriodFromRequest(r *http.Request) ReportPeriod {
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("period")))
	if kind != "day" && kind != "week" && kind != "month" {
		kind = "day"
	}
	anchor, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.URL.Query().Get("date")), time.Local)
	if err != nil {
		now := time.Now().In(time.Local)
		anchor = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	var start, end, previous, next time.Time
	switch kind {
	case "week":
		daysSinceMonday := (int(anchor.Weekday()) + 6) % 7
		start = anchor.AddDate(0, 0, -daysSinceMonday)
		end = start.AddDate(0, 0, 6)
		previous = anchor.AddDate(0, 0, -7)
		next = anchor.AddDate(0, 0, 7)
	case "month":
		start = time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 1, -1)
		previous = start.AddDate(0, -1, 0)
		next = start.AddDate(0, 1, 0)
	default:
		start = anchor
		end = anchor
		previous = anchor.AddDate(0, 0, -1)
		next = anchor.AddDate(0, 0, 1)
	}
	label := start.Format("Monday, " + displayDateLayout)
	if kind == "week" {
		label = start.Format(displayDateLayout) + " - " + end.Format(displayDateLayout)
	}
	if kind == "month" {
		label = start.Format("January 2006")
	}
	return ReportPeriod{
		Kind:         kind,
		Anchor:       anchor.Format("2006-01-02"),
		Start:        start.Format("2006-01-02"),
		End:          end.Format("2006-01-02"),
		Label:        label,
		PreviousDate: previous.Format("2006-01-02"),
		NextDate:     next.Format("2006-01-02"),
	}
}

func (a *App) buildOperationalReport(period ReportPeriod) (*OperationalReport, error) {
	report := &OperationalReport{Period: period}
	points := make(map[string]*ReportSeriesPoint)
	start, _ := time.ParseInLocation("2006-01-02", period.Start, time.Local)
	end, _ := time.ParseInLocation("2006-01-02", period.End, time.Local)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		report.Series = append(report.Series, ReportSeriesPoint{Date: date, Label: day.Format("Mon 02")})
	}
	for i := range report.Series {
		points[report.Series[i].Date] = &report.Series[i]
	}

	transactions, err := a.listFinanceTransactionsFiltered(FinanceFilter{From: period.Start, To: period.End})
	if err != nil {
		return nil, err
	}
	report.Transactions = transactions
	financeBreakdown := map[string]*ReportBreakdown{}
	for _, transaction := range transactions {
		date := transaction.RecordedAt.Format("2006-01-02")
		point := points[date]
		if transaction.Amount >= 0 {
			report.Summary.Income += transaction.Amount
			if point != nil {
				point.Income += transaction.Amount
			}
		} else {
			expense := -transaction.Amount
			report.Summary.Expenses += expense
			if point != nil {
				point.Expenses += expense
			}
		}
		switch transaction.Category {
		case "booking_payment":
			report.Summary.BookingRevenue += transaction.Amount
		case "student_monthly_payment":
			report.Summary.StudentRevenue += transaction.Amount
			report.Summary.StudentPayments++
		case "admission_payment":
			report.Summary.AdmissionRevenue += transaction.Amount
		}
		item := financeBreakdown[transaction.Category]
		if item == nil {
			item = &ReportBreakdown{Key: transaction.Category, Label: financeCategoryLabel(transaction.Category)}
			financeBreakdown[transaction.Category] = item
		}
		item.Count++
		item.Amount += transaction.Amount
	}
	report.Summary.NetCash = report.Summary.Income - report.Summary.Expenses
	for _, item := range financeBreakdown {
		report.FinanceBreakdown = append(report.FinanceBreakdown, *item)
	}
	sort.Slice(report.FinanceBreakdown, func(i, j int) bool {
		return math.Abs(report.FinanceBreakdown[i].Amount) > math.Abs(report.FinanceBreakdown[j].Amount)
	})

	scheduleRows, err := a.db.Query(`
		SELECT slot_date, slot_hour, entry_type, activity, quantity, status
		FROM space_schedules
		WHERE slot_date BETWEEN ? AND ?
		ORDER BY slot_date, slot_hour, id
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	bookingBreakdown := map[string]*ReportBreakdown{}
	occupied := map[string]struct{}{}
	for scheduleRows.Next() {
		var slotDate, slotHour, entryType, activity, status string
		var quantity int
		if err := scheduleRows.Scan(&slotDate, &slotHour, &entryType, &activity, &quantity, &status); err != nil {
			scheduleRows.Close()
			return nil, err
		}
		if entryType == "booking" {
			if status == "confirmed" {
				report.Summary.ConfirmedBookings++
				if point := points[slotDate]; point != nil {
					point.Bookings++
				}
				item := bookingBreakdown[activity]
				if item == nil {
					item = &ReportBreakdown{Key: activity, Label: activityLabel(activity)}
					bookingBreakdown[activity] = item
				}
				item.Count++
			} else if status == "pending" {
				report.Summary.PendingBookings++
			}
		}
		if status == "confirmed" {
			occupied[slotDate+"|"+slotHour] = struct{}{}
		}
	}
	if err := scheduleRows.Err(); err != nil {
		scheduleRows.Close()
		return nil, err
	}
	if err := scheduleRows.Close(); err != nil {
		return nil, err
	}
	for _, item := range bookingBreakdown {
		report.BookingBreakdown = append(report.BookingBreakdown, *item)
	}
	sort.Slice(report.BookingBreakdown, func(i, j int) bool {
		return report.BookingBreakdown[i].Count > report.BookingBreakdown[j].Count
	})
	report.Summary.OccupiedSlotHours = len(occupied)
	report.Summary.AvailableSlotHours = len(report.Series) * len(bookingHours())
	if report.Summary.AvailableSlotHours > 0 {
		report.Summary.UtilizationRate = float64(report.Summary.OccupiedSlotHours) / float64(report.Summary.AvailableSlotHours) * 100
	}

	admissionRows, err := a.db.Query(`
		SELECT admission_date, COUNT(*)
		FROM admissions
		WHERE admission_date BETWEEN ? AND ?
		GROUP BY admission_date
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	for admissionRows.Next() {
		var date string
		var count int
		if err := admissionRows.Scan(&date, &count); err != nil {
			admissionRows.Close()
			return nil, err
		}
		report.Summary.NewAdmissions += count
		if point := points[date]; point != nil {
			point.Admissions += count
		}
	}
	if err := admissionRows.Err(); err != nil {
		admissionRows.Close()
		return nil, err
	}
	if err := admissionRows.Close(); err != nil {
		return nil, err
	}

	attendanceRows, err := a.db.Query(`
		SELECT attendance_date, status, COUNT(*)
		FROM attendance_records
		WHERE attendance_date BETWEEN ? AND ?
		GROUP BY attendance_date, status
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	for attendanceRows.Next() {
		var date, status string
		var count int
		if err := attendanceRows.Scan(&date, &status, &count); err != nil {
			attendanceRows.Close()
			return nil, err
		}
		report.Summary.AttendanceTotal += count
		if status == "present" {
			report.Summary.AttendancePresent += count
		}
		if point := points[date]; point != nil {
			point.Attendance += count
			if status == "present" {
				point.Present += count
			}
		}
	}
	if err := attendanceRows.Err(); err != nil {
		attendanceRows.Close()
		return nil, err
	}
	if err := attendanceRows.Close(); err != nil {
		return nil, err
	}
	if report.Summary.AttendanceTotal > 0 {
		report.Summary.AttendanceRate = float64(report.Summary.AttendancePresent) / float64(report.Summary.AttendanceTotal) * 100
	}
	for i := range report.Series {
		report.Series[i].NetCash = report.Series[i].Income - report.Series[i].Expenses
		dailyCash := math.Max(report.Series[i].Income, report.Series[i].Expenses)
		if dailyCash > report.MaxDailyCash {
			report.MaxDailyCash = dailyCash
		}
	}
	return report, nil
}

func formatReportNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func reportBarWidth(value, maxValue float64) string {
	if value <= 0 || maxValue <= 0 {
		return "0%"
	}
	percent := value / maxValue * 100
	if percent < 3 {
		percent = 3
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%.1f%%", percent)
}

func financeFilterFromRequest(r *http.Request) FinanceFilter {
	page, limit := normalizedFinancePage(
		parseIntQuery(r.URL.Query().Get("page")),
		parseIntQuery(r.URL.Query().Get("limit")),
	)
	filter := FinanceFilter{
		From:            strings.TrimSpace(r.URL.Query().Get("from")),
		To:              strings.TrimSpace(r.URL.Query().Get("to")),
		Direction:       strings.ToLower(strings.TrimSpace(r.URL.Query().Get("direction"))),
		Category:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category"))),
		AccountID:       parseInt64Query(r.URL.Query().Get("account_id")),
		TransactionType: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("transaction_type"))),
		SourceType:      strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source_type"))),
		PaymentMethod:   normalizePaymentMethod(r.URL.Query().Get("payment_method")),
		RecordedUserID:  parseInt64Query(r.URL.Query().Get("recorded_user_id")),
		Status:          strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))),
		Reference:       strings.TrimSpace(r.URL.Query().Get("reference")),
		Search:          strings.TrimSpace(r.URL.Query().Get("search")),
		ExportKind:      strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind"))),
		Page:            page,
		Limit:           limit,
	}
	if _, err := time.Parse("2006-01-02", filter.From); err != nil {
		filter.From = ""
	}
	if _, err := time.Parse("2006-01-02", filter.To); err != nil {
		filter.To = ""
	}
	if filter.Direction != "income" && filter.Direction != "expense" {
		filter.Direction = ""
	}
	if filter.Status != "active" && filter.Status != "voided" {
		filter.Status = ""
	}
	if !validFinancePaymentMethod(filter.PaymentMethod) {
		filter.PaymentMethod = ""
	}
	return filter
}

func admissionsFilterFromRequest(r *http.Request) AdmissionsFilter {
	page, limit := normalizedAdmissionsPage(
		parseIntQuery(r.URL.Query().Get("page")),
		parseIntQuery(r.URL.Query().Get("limit")),
	)
	filter := AdmissionsFilter{
		Search:    strings.TrimSpace(r.URL.Query().Get("search")),
		Direction: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("direction"))),
		Page:      page,
		Limit:     limit,
	}
	if filter.Direction != "desc" {
		filter.Direction = "asc"
	}
	return filter
}

func normalizedFinancePage(page, limit int) (int, int) {
	if limit <= 0 {
		limit = defaultFinanceLedgerPageSize
	}
	if limit > maxFinanceLedgerPageSize {
		limit = maxFinanceLedgerPageSize
	}
	if page <= 0 {
		page = 1
	}
	return page, limit
}

func normalizedAdmissionsPage(page, limit int) (int, int) {
	if limit <= 0 {
		limit = defaultAdmissionsPageSize
	}
	if limit > maxAdmissionsPageSize {
		limit = maxAdmissionsPageSize
	}
	if page <= 0 {
		page = 1
	}
	return page, limit
}

func financeFilterPageURL(r *http.Request, filter FinanceFilter, page int) string {
	if page < 1 {
		page = 1
	}
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page))
	query.Set("limit", strconv.Itoa(filter.Limit))
	return "/admin/finance/ledger?" + query.Encode() + "#ledger"
}

func admissionsFilterPageURL(r *http.Request, filter AdmissionsFilter, page int) string {
	if page < 1 {
		page = 1
	}
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page))
	query.Set("limit", strconv.Itoa(filter.Limit))
	query.Set("direction", filter.Direction)
	if strings.TrimSpace(filter.Search) == "" {
		query.Del("search")
	} else {
		query.Set("search", filter.Search)
	}
	return "/admin/admissions?" + query.Encode() + "#admissions-directory"
}

func admissionsFilterBaseURL(r *http.Request, filter AdmissionsFilter) string {
	query := r.URL.Query()
	query.Del("page")
	query.Set("limit", strconv.Itoa(filter.Limit))
	query.Set("direction", filter.Direction)
	if strings.TrimSpace(filter.Search) == "" {
		query.Del("search")
	} else {
		query.Set("search", filter.Search)
	}
	encoded := query.Encode()
	if encoded == "" {
		return "/admin/admissions"
	}
	return "/admin/admissions?" + encoded
}

func admissionsTotalPages(total, limit int) int {
	if total <= 0 || limit <= 0 {
		return 1
	}
	pages := total / limit
	if total%limit != 0 {
		pages++
	}
	if pages <= 0 {
		return 1
	}
	return pages
}

func admissionsPageWindow(currentPage, totalPages int) []int {
	if totalPages <= 0 {
		return []int{1}
	}
	if currentPage <= 0 {
		currentPage = 1
	}
	start := currentPage - 2
	if start < 1 {
		start = 1
	}
	end := start + 4
	if end > totalPages {
		end = totalPages
	}
	if end-start < 4 {
		start = end - 4
		if start < 1 {
			start = 1
		}
	}
	pages := make([]int, 0, end-start+1)
	for page := start; page <= end; page++ {
		pages = append(pages, page)
	}
	return pages
}

func admissionsPageURL(baseURL string, page int) string {
	if page < 1 {
		page = 1
	}
	separator := "&"
	if !strings.Contains(baseURL, "?") {
		separator = "?"
	} else if strings.HasSuffix(baseURL, "?") || strings.HasSuffix(baseURL, "&") {
		separator = ""
	}
	return baseURL + separator + "page=" + strconv.Itoa(page) + "#admissions-directory"
}

func parseIntQuery(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func validManualFinanceCategory(direction, category string) bool {
	income := map[string]bool{"manual_income": true, "sponsorship_income": true, "other_income": true}
	expense := map[string]bool{
		"facility_expense": true, "utilities_expense": true, "maintenance_expense": true,
		"staff_expense": true, "equipment_expense": true, "sports_supplies_expense": true,
		"refreshments_expense": true, "prizes_expense": true, "marketing_expense": true,
		"transport_expense": true, "event_expense": true, "bank_charges_expense": true,
		"other_expense": true,
	}
	if direction == "income" {
		return income[category]
	}
	if direction == "expense" {
		return expense[category]
	}
	return false
}

func csvSafeCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
