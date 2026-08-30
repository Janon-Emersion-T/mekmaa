package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	reportDomainOverview   = "overview"
	reportDomainFinance    = "finance"
	reportDomainPayroll    = "payroll"
	reportDomainAttendance = "attendance"
	reportDomainStudents   = "students"
)

func reportDomainOptions() []ReportDomainOption {
	return []ReportDomainOption{
		{Key: reportDomainOverview, Label: "Overview", Description: "Cross-function operational summary."},
		{Key: reportDomainFinance, Label: "Finance", Description: "Revenue, expenses, and reconciled transactions."},
		{Key: reportDomainPayroll, Label: "Payroll", Description: "Runs, payment status, and compensation mix."},
		{Key: reportDomainAttendance, Label: "Attendance", Description: "Student and staff attendance performance."},
		{Key: reportDomainStudents, Label: "Students", Description: "Programme enrollments and payment coverage."},
	}
}

func reportDomainFromRequest(r *http.Request) string {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	switch value {
	case reportDomainFinance, reportDomainPayroll, reportDomainAttendance, reportDomainStudents:
		return value
	default:
		return reportDomainOverview
	}
}

func reportMetric(label, value, note, tone string) ReportMetric {
	return ReportMetric{
		Label: strings.TrimSpace(label),
		Value: strings.TrimSpace(value),
		Note:  strings.TrimSpace(note),
		Tone:  strings.TrimSpace(tone),
	}
}

func buildReportCenter(
	period ReportPeriod,
	domain string,
	overview *OperationalReport,
	finance *FinanceDomainReport,
	payroll *PayrollDomainReport,
	attendance *AttendanceDomainReport,
	students *StudentDomainReport,
) *ReportCenter {
	return &ReportCenter{
		Domain:     domain,
		Domains:    reportDomainOptions(),
		Period:     period,
		Overview:   overview,
		Finance:    finance,
		Payroll:    payroll,
		Attendance: attendance,
		Students:   students,
	}
}

func (a *App) buildFinanceDomainReport(
	period ReportPeriod,
	divisionIDs []int64,
) (*FinanceDomainReport, error) {
	report, err := a.buildOperationalReport(period, divisionIDs)
	if err != nil {
		return nil, err
	}

	return &FinanceDomainReport{
		Metrics: []ReportMetric{
			reportMetric("Gross income", money(report.Summary.Income), fmt.Sprintf("%d ledger transactions", len(report.Transactions)), "positive"),
			reportMetric("Expenses", money(report.Summary.Expenses), "Posted outflow during the selected period.", "negative"),
			reportMetric("Net cash", money(report.Summary.NetCash), "Income minus expenses.", "neutral"),
			reportMetric("Student fees", money(report.Summary.StudentRevenue), "Posted student monthly collections.", "positive"),
		},
		Breakdown:    report.FinanceBreakdown,
		Transactions: report.Transactions,
	}, nil
}

func (a *App) buildPayrollDomainReport(
	period ReportPeriod,
	divisionIDs []int64,
) (*PayrollDomainReport, error) {
	query := `
		SELECT
			pr.id,
			CAST(pr.period_start AS TEXT),
			CAST(pr.period_end AS TEXT),
			COALESCE(pr.label, ''),
			pr.status,
			COUNT(pp.id),
			COALESCE(SUM(pp.base_amount), 0),
			COALESCE(SUM(pp.additions_total), 0),
			COALESCE(SUM(pp.deductions_total), 0),
			COALESCE(SUM(pp.net_amount), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'draft' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'calculated' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'approved' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'paid' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pp.status = 'paid' THEN pp.net_amount ELSE 0 END), 0)
		FROM payroll_runs pr
		JOIN payroll_payments pp
			ON pp.payroll_run_id = pr.id
		   AND pp.status <> 'void'
		LEFT JOIN training_programs tp
			ON tp.id = pp.training_program_id
		WHERE CAST(pr.period_start AS TEXT) <= ?
		  AND CAST(pr.period_end AS TEXT) >= ?
	`
	args := []any{period.End, period.Start}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND (pp.division_id IN (` + placeholders + `) OR tp.division_id IN (` + placeholders + `))`
		args = append(args, scopeArgs...)
		args = append(args, scopeArgs...)
	}
	query += `
		GROUP BY pr.id, pr.period_start, pr.period_end, pr.label, pr.status
		ORDER BY pr.period_start DESC, pr.id DESC
	`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runRows := make([]PayrollReportRunRow, 0)
	totalNet := 0.0
	totalPaid := 0.0
	totalPayments := 0
	for rows.Next() {
		var row PayrollReportRunRow
		if err := rows.Scan(
			&row.Run.ID,
			&row.Run.PeriodStart,
			&row.Run.PeriodEnd,
			&row.Run.Label,
			&row.Run.Status,
			&row.PaymentCount,
			&row.Run.BaseTotal,
			&row.Run.AdditionsTotal,
			&row.Run.DeductionsTotal,
			&row.Run.NetTotal,
			&row.DraftPayments,
			&row.CalculatedPayments,
			&row.ApprovedPayments,
			&row.PaidPayments,
			&row.Run.PaidTotal,
		); err != nil {
			return nil, err
		}
		totalNet += row.Run.NetTotal
		totalPaid += row.Run.PaidTotal
		totalPayments += row.PaymentCount
		runRows = append(runRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	compQuery := `
		SELECT
			COALESCE(pp.compensation_type, ''),
			COUNT(pp.id),
			COALESCE(SUM(pp.quantity), 0),
			COALESCE(SUM(pp.net_amount), 0)
		FROM payroll_runs pr
		JOIN payroll_payments pp
			ON pp.payroll_run_id = pr.id
		   AND pp.status <> 'void'
		LEFT JOIN training_programs tp
			ON tp.id = pp.training_program_id
		WHERE CAST(pr.period_start AS TEXT) <= ?
		  AND CAST(pr.period_end AS TEXT) >= ?
	`
	compArgs := []any{period.End, period.Start}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		compQuery += ` AND (pp.division_id IN (` + placeholders + `) OR tp.division_id IN (` + placeholders + `))`
		compArgs = append(compArgs, scopeArgs...)
		compArgs = append(compArgs, scopeArgs...)
	}
	compQuery += `
		GROUP BY pp.compensation_type
		ORDER BY SUM(pp.net_amount) DESC, pp.compensation_type ASC
	`

	compRows, err := a.queryDB(compQuery, compArgs...)
	if err != nil {
		return nil, err
	}
	defer compRows.Close()

	compensation := make([]PayrollReportCompensationRow, 0)
	for compRows.Next() {
		var row PayrollReportCompensationRow
		if err := compRows.Scan(
			&row.CompensationType,
			&row.PaymentCount,
			&row.Quantity,
			&row.NetAmount,
		); err != nil {
			return nil, err
		}
		compensation = append(compensation, row)
	}
	if err := compRows.Err(); err != nil {
		return nil, err
	}

	return &PayrollDomainReport{
		Metrics: []ReportMetric{
			reportMetric("Payroll runs", strconv.Itoa(len(runRows)), "Runs overlapping the selected period.", "neutral"),
			reportMetric("Payroll payments", strconv.Itoa(totalPayments), "Non-void payroll payments in scope.", "neutral"),
			reportMetric("Net payroll", money(totalNet), "Base plus adjustments after deductions.", "negative"),
			reportMetric("Paid amount", money(totalPaid), "Payments already marked paid.", "neutral"),
		},
		RunRows:      runRows,
		Compensation: compensation,
	}, nil
}

func (a *App) buildAttendanceDomainReport(
	period ReportPeriod,
	divisionIDs []int64,
) (*AttendanceDomainReport, error) {
	groupQuery := `
		SELECT
			sg.id,
			COALESCE(sg.name, ''),
			COALESCE(sg.code, ''),
			COALESCE(tp.name, ''),
			COALESCE(SUM(CASE WHEN ar.status = 'present' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ar.status = 'absent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ar.status = 'late' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ar.status = 'excused' THEN 1 ELSE 0 END), 0),
			COUNT(ar.id)
		FROM attendance_records ar
		JOIN student_groups sg
			ON sg.id = ar.group_id
		LEFT JOIN training_programs tp
			ON tp.id = sg.training_program_id
		WHERE ar.attendance_date >= ?
		  AND ar.attendance_date <= ?
	`
	args := []any{period.Start, period.End}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		groupQuery += ` AND tp.division_id IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	groupQuery += `
		GROUP BY sg.id, sg.name, sg.code, tp.name
		ORDER BY COUNT(ar.id) DESC, sg.name ASC, sg.id ASC
	`

	groupRowsDB, err := a.queryDB(groupQuery, args...)
	if err != nil {
		return nil, err
	}
	defer groupRowsDB.Close()

	groupRows := make([]AttendanceDomainGroupRow, 0)
	studentPresent := 0
	studentLate := 0
	studentTotal := 0
	for groupRowsDB.Next() {
		var row AttendanceDomainGroupRow
		if err := groupRowsDB.Scan(
			&row.GroupID,
			&row.GroupName,
			&row.GroupCode,
			&row.TrainingProgramName,
			&row.PresentCount,
			&row.AbsentCount,
			&row.LateCount,
			&row.ExcusedCount,
			&row.TotalEntries,
		); err != nil {
			return nil, err
		}
		attended := row.PresentCount + row.LateCount
		if row.TotalEntries > 0 {
			row.AttendanceRate = float64(attended) / float64(row.TotalEntries) * 100
		}
		studentPresent += row.PresentCount
		studentLate += row.LateCount
		studentTotal += row.TotalEntries
		groupRows = append(groupRows, row)
	}
	if err := groupRowsDB.Err(); err != nil {
		return nil, err
	}

	staffQuery := `
		SELECT
			u.id,
			COALESCE(u.name, ''),
			COALESCE(SUM(CASE WHEN car.status = 'present' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN car.status = 'absent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN car.status = 'late' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN car.status = 'excused' THEN 1 ELSE 0 END), 0),
			COUNT(car.id)
		FROM coach_attendance_records car
		JOIN users u
			ON u.id = car.user_id
		WHERE car.attendance_date >= ?
		  AND car.attendance_date <= ?
	`
	staffArgs := []any{period.Start, period.End}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		staffQuery += ` AND EXISTS (
			SELECT 1
			FROM user_divisions ud
			WHERE ud.user_id = car.user_id
			  AND ud.division_id IN (` + placeholders + `)
		)`
		staffArgs = append(staffArgs, scopeArgs...)
	}
	staffQuery += `
		GROUP BY u.id, u.name
		ORDER BY COUNT(car.id) DESC, u.name ASC, u.id ASC
	`

	staffRowsDB, err := a.queryDB(staffQuery, staffArgs...)
	if err != nil {
		return nil, err
	}
	defer staffRowsDB.Close()

	staffRows := make([]AttendanceDomainStaffRow, 0)
	staffPresent := 0
	staffLate := 0
	staffTotal := 0
	for staffRowsDB.Next() {
		var row AttendanceDomainStaffRow
		if err := staffRowsDB.Scan(
			&row.UserID,
			&row.UserName,
			&row.PresentCount,
			&row.AbsentCount,
			&row.LateCount,
			&row.ExcusedCount,
			&row.TotalRecords,
		); err != nil {
			return nil, err
		}
		attended := row.PresentCount + row.LateCount
		if row.TotalRecords > 0 {
			row.AttendanceRate = float64(attended) / float64(row.TotalRecords) * 100
		}
		staffPresent += row.PresentCount
		staffLate += row.LateCount
		staffTotal += row.TotalRecords
		staffRows = append(staffRows, row)
	}
	if err := staffRowsDB.Err(); err != nil {
		return nil, err
	}

	return &AttendanceDomainReport{
		Metrics: []ReportMetric{
			reportMetric("Student attendance", strconv.Itoa(studentPresent+studentLate), fmt.Sprintf("%d present or late statuses from %d total", studentPresent+studentLate, studentTotal), "neutral"),
			reportMetric("Student rate", fmt.Sprintf("%.1f%%", percentOf(studentPresent+studentLate, studentTotal)), "Present and late count as attended.", "neutral"),
			reportMetric("Staff attendance", strconv.Itoa(staffPresent+staffLate), fmt.Sprintf("%d present or late statuses from %d total", staffPresent+staffLate, staffTotal), "neutral"),
			reportMetric("Staff rate", fmt.Sprintf("%.1f%%", percentOf(staffPresent+staffLate, staffTotal)), "Present and late count as attended.", "neutral"),
		},
		GroupRows: groupRows,
		StaffRows: staffRows,
	}, nil
}

func percentOf(numerator int, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func (a *App) buildStudentDomainReport(
	period ReportPeriod,
	divisionIDs []int64,
) (*StudentDomainReport, error) {
	enrollments, err := a.listStudentEnrollmentsByDivisionIDs(divisionIDs)
	if err != nil {
		return nil, err
	}

	paymentMonth := period.End[:7]
	paymentRows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, divisionIDs)
	if err != nil {
		return nil, err
	}

	byProgram := make(map[int64]*StudentProgramReportRow)
	getRow := func(id int64, name string, division string) *StudentProgramReportRow {
		row := byProgram[id]
		if row == nil {
			row = &StudentProgramReportRow{
				TrainingProgramID:   id,
				TrainingProgramName: strings.TrimSpace(name),
				DivisionName:        strings.TrimSpace(division),
			}
			byProgram[id] = row
		}
		return row
	}

	totalActive := 0
	newEnrollments := 0
	for _, enrollment := range enrollments {
		row := getRow(enrollment.TrainingProgramID, enrollment.TrainingProgramName, enrollment.DivisionName)
		row.TotalEnrollments++
		if enrollment.Active {
			row.ActiveEnrollments++
			totalActive++
		}
		if enrollment.EnrollmentDate >= period.Start && enrollment.EnrollmentDate <= period.End {
			row.NewEnrollments++
			newEnrollments++
		}
	}

	paymentCoverage := 0
	collected := 0.0
	outstanding := 0.0
	for _, paymentRow := range paymentRows {
		row := getRow(
			paymentRow.Enrollment.TrainingProgramID,
			paymentRow.Enrollment.TrainingProgramName,
			paymentRow.Enrollment.DivisionName,
		)
		if paymentRow.CollectedAmount > 0 {
			row.StudentPayments++
			paymentCoverage++
		}
		row.PaymentCollected = normalizeMoney(row.PaymentCollected + paymentRow.CollectedAmount)
		row.PaymentOutstanding = normalizeMoney(row.PaymentOutstanding + paymentRow.OutstandingAmount)
		collected = normalizeMoney(collected + paymentRow.CollectedAmount)
		outstanding = normalizeMoney(outstanding + paymentRow.OutstandingAmount)
	}

	rows := make([]StudentProgramReportRow, 0, len(byProgram))
	for _, row := range byProgram {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].DivisionName != rows[j].DivisionName {
			return rows[i].DivisionName < rows[j].DivisionName
		}
		return rows[i].TrainingProgramName < rows[j].TrainingProgramName
	})

	return &StudentDomainReport{
		Metrics: []ReportMetric{
			reportMetric("Active enrollments", strconv.Itoa(totalActive), "Currently active student enrollments in scope.", "neutral"),
			reportMetric("New enrollments", strconv.Itoa(newEnrollments), "Enrollment dates inside the selected period.", "positive"),
			reportMetric("Collected fees", money(collected), fmt.Sprintf("Monthly student payments for %s", paymentMonth), "positive"),
			reportMetric("Outstanding fees", money(outstanding), fmt.Sprintf("Open student balances for %s", paymentMonth), "negative"),
		},
		Programs:     rows,
		PaymentMonth: paymentMonth,
	}, nil
}

func (a *App) writeReportCenterCSV(
	w http.ResponseWriter,
	center *ReportCenter,
) error {
	filename := fmt.Sprintf("mekmaa-%s-%s-report-%s.csv", center.Domain, center.Period.Kind, center.Period.Anchor)
	writer := newCSVReportWriter(w, filename)
	defer writer.Flush()

	if err := writeCSVReportPreamble(
		writer,
		"Mekmaa "+reportDomainLabel(center.Domain)+" Report",
		CSVReportMetaRow{Section: "report", Field: "Domain", Value: reportDomainLabel(center.Domain)},
		CSVReportMetaRow{Section: "report", Field: "Cadence", Value: reportPeriodKindLabel(center.Period.Kind)},
		CSVReportMetaRow{Section: "period", Field: "Label", Value: center.Period.Label},
		CSVReportMetaRow{Section: "period", Field: "From", Value: center.Period.Start},
		CSVReportMetaRow{Section: "period", Field: "To", Value: center.Period.End},
	); err != nil {
		return err
	}

	_ = writer.Write([]string{"Mekmaa report", reportDomainLabel(center.Domain)})
	_ = writer.Write([]string{"Period", center.Period.Label, center.Period.Start, center.Period.End})
	_ = writer.Write([]string{})

	switch center.Domain {
	case reportDomainFinance:
		if center.Finance == nil {
			break
		}
		_ = writer.Write([]string{"METRIC", "VALUE", "NOTE"})
		for _, metric := range center.Finance.Metrics {
			_ = writer.Write([]string{metric.Label, metric.Value, metric.Note})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"BREAKDOWN", "CATEGORY", "TRANSACTIONS", "AMOUNT"})
		for _, row := range center.Finance.Breakdown {
			_ = writer.Write([]string{"", row.Label, strconv.Itoa(row.Count), formatReportNumber(row.Amount)})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"TRANSACTIONS", "RECEIPT", "DATE", "CATEGORY", "PARTY", "DESCRIPTION", "METHOD", "AMOUNT"})
		for _, row := range center.Finance.Transactions {
			_ = writer.Write([]string{"", csvSafeCell(row.ReceiptNumber), formatDateTime(row.RecordedAt), financeCategoryLabel(row.Category), csvSafeCell(row.PersonName), csvSafeCell(row.Description), csvSafeCell(row.PaymentMethod), formatReportNumber(row.Amount)})
		}
	case reportDomainPayroll:
		if center.Payroll == nil {
			break
		}
		_ = writer.Write([]string{"METRIC", "VALUE", "NOTE"})
		for _, metric := range center.Payroll.Metrics {
			_ = writer.Write([]string{metric.Label, metric.Value, metric.Note})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"RUNS", "LABEL", "START", "END", "STATUS", "PAYMENTS", "NET", "PAID", "DRAFT", "CALCULATED", "APPROVED", "PAID PAYMENTS"})
		for _, row := range center.Payroll.RunRows {
			_ = writer.Write([]string{"", row.Run.Label, row.Run.PeriodStart, row.Run.PeriodEnd, row.Run.Status, strconv.Itoa(row.PaymentCount), formatReportNumber(row.Run.NetTotal), formatReportNumber(row.Run.PaidTotal), strconv.Itoa(row.DraftPayments), strconv.Itoa(row.CalculatedPayments), strconv.Itoa(row.ApprovedPayments), strconv.Itoa(row.PaidPayments)})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"COMPENSATION", "TYPE", "PAYMENTS", "QUANTITY", "NET"})
		for _, row := range center.Payroll.Compensation {
			_ = writer.Write([]string{"", salaryTypeLabel(row.CompensationType), strconv.Itoa(row.PaymentCount), formatReportNumber(row.Quantity), formatReportNumber(row.NetAmount)})
		}
	case reportDomainAttendance:
		if center.Attendance == nil {
			break
		}
		_ = writer.Write([]string{"METRIC", "VALUE", "NOTE"})
		for _, metric := range center.Attendance.Metrics {
			_ = writer.Write([]string{metric.Label, metric.Value, metric.Note})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"STUDENT GROUPS", "GROUP", "PROGRAMME", "PRESENT", "LATE", "ABSENT", "EXCUSED", "TOTAL", "RATE"})
		for _, row := range center.Attendance.GroupRows {
			_ = writer.Write([]string{"", csvSafeCell(row.GroupName), csvSafeCell(row.TrainingProgramName), strconv.Itoa(row.PresentCount), strconv.Itoa(row.LateCount), strconv.Itoa(row.AbsentCount), strconv.Itoa(row.ExcusedCount), strconv.Itoa(row.TotalEntries), fmt.Sprintf("%.1f%%", row.AttendanceRate)})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"STAFF", "NAME", "PRESENT", "LATE", "ABSENT", "EXCUSED", "TOTAL", "RATE"})
		for _, row := range center.Attendance.StaffRows {
			_ = writer.Write([]string{"", csvSafeCell(row.UserName), strconv.Itoa(row.PresentCount), strconv.Itoa(row.LateCount), strconv.Itoa(row.AbsentCount), strconv.Itoa(row.ExcusedCount), strconv.Itoa(row.TotalRecords), fmt.Sprintf("%.1f%%", row.AttendanceRate)})
		}
	case reportDomainStudents:
		if center.Students == nil {
			break
		}
		_ = writer.Write([]string{"METRIC", "VALUE", "NOTE"})
		for _, metric := range center.Students.Metrics {
			_ = writer.Write([]string{metric.Label, metric.Value, metric.Note})
		}
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"PROGRAMMES", "DIVISION", "PROGRAMME", "TOTAL ENROLLMENTS", "ACTIVE", "NEW IN PERIOD", "PAYING STUDENTS", "COLLECTED", "OUTSTANDING"})
		for _, row := range center.Students.Programs {
			_ = writer.Write([]string{"", csvSafeCell(row.DivisionName), csvSafeCell(row.TrainingProgramName), strconv.Itoa(row.TotalEnrollments), strconv.Itoa(row.ActiveEnrollments), strconv.Itoa(row.NewEnrollments), strconv.Itoa(row.StudentPayments), formatReportNumber(row.PaymentCollected), formatReportNumber(row.PaymentOutstanding)})
		}
	default:
		if center.Overview == nil {
			break
		}
		report := center.Overview
		_ = writer.Write([]string{"Mekmaa operational report", report.Period.Label})
		_ = writer.Write([]string{"Period", report.Period.Start, report.Period.End})
		_ = writer.Write([]string{})
		_ = writer.Write([]string{"SUMMARY", "VALUE"})
		for _, row := range [][]string{
			{"Gross income (LKR)", formatReportNumber(report.Summary.Income)},
			{"Expenses (LKR)", formatReportNumber(report.Summary.Expenses)},
			{"Net cash (LKR)", formatReportNumber(report.Summary.NetCash)},
			{"Confirmed bookings", strconv.Itoa(report.Summary.ConfirmedBookings)},
			{"Pending bookings", strconv.Itoa(report.Summary.PendingBookings)},
			{"New admissions", strconv.Itoa(report.Summary.NewAdmissions)},
			{"Student payments", strconv.Itoa(report.Summary.StudentPayments)},
			{"Attendance rate", fmt.Sprintf("%.1f%%", report.Summary.AttendanceRate)},
			{"Facility utilization", fmt.Sprintf("%.1f%%", report.Summary.UtilizationRate)},
		} {
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
	}

	writer.Flush()
	return writer.Error()
}

func reportDomainLabel(
	domain string,
) string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case reportDomainFinance:
		return "Finance"
	case reportDomainPayroll:
		return "Payroll"
	case reportDomainAttendance:
		return "Attendance"
	case reportDomainStudents:
		return "Students"
	default:
		return "Overview"
	}
}
