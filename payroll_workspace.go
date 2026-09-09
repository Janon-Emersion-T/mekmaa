package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type StaffSalaryPeriod struct {
	RunID     int64
	UserID    int64
	UserName  string
	Label     string
	Start     string
	End       string
	Status    string
	NetAmount float64
}

func (a *App) listStaffSalaryPeriods(staff []User) ([]StaffSalaryPeriod, error) {
	ids := make([]int64, 0, len(staff))
	for _, user := range staff {
		ids = append(ids, user.ID)
	}
	placeholders, args := int64ScopePlaceholders(ids)
	if placeholders == "" {
		return []StaffSalaryPeriod{}, nil
	}
	rows, err := a.queryDB(`
 SELECT pr.id, pp.user_id, u.name, COALESCE(NULLIF(pp.period_label, ''), pr.label),
 CAST(pr.period_start AS TEXT), CAST(pr.period_end AS TEXT), pp.status, SUM(pp.net_amount)
 FROM payroll_payments pp
 JOIN payroll_runs pr ON pr.id = pp.payroll_run_id
 JOIN users u ON u.id = pp.user_id
 WHERE pp.user_id IN (`+placeholders+`)
 GROUP BY pr.id, pp.user_id, u.name, COALESCE(NULLIF(pp.period_label, ''), pr.label), pr.period_start, pr.period_end, pp.status
 ORDER BY pr.period_start DESC, u.name, pr.id DESC
 `, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	periods := []StaffSalaryPeriod{}
	for rows.Next() {
		var period StaffSalaryPeriod
		if err := rows.Scan(&period.RunID, &period.UserID, &period.UserName, &period.Label, &period.Start, &period.End, &period.Status, &period.NetAmount); err != nil {
			return nil, err
		}
		periods = append(periods, period)
	}
	return periods, rows.Err()
}

func validateStaffPayrollDates(start, end string) error {
	from, err := time.Parse("2006-01-02", start)
	if err != nil {
		return errors.New("select a valid period start date")
	}
	to, err := time.Parse("2006-01-02", end)
	if err != nil {
		return errors.New("select a valid period end date")
	}
	if to.Before(from) {
		return errors.New("period end cannot be before its start")
	}
	return nil
}

func staffPayrollOverlapQuery() string {
	return `SELECT COUNT(*) FROM payroll_payments pp
 JOIN payroll_runs pr ON pr.id = pp.payroll_run_id
 WHERE pp.user_id = ? AND pp.status <> 'void'
 AND pr.period_start <= ? AND pr.period_end >= ? AND pr.id <> ?`
}

func (a *App) checkStaffPayrollOverlap(userID int64, start, end string) error {
	var count int
	if err := a.queryRowDB(staffPayrollOverlapQuery(), userID, end, start, 0).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("this staff member already has a salary for part or all of this period; review the existing salary below")
	}
	return nil
}

func (a *App) checkStaffPayrollOverlapTx(tx *sql.Tx, userID int64, start, end string, exceptRunID int64) error {
	// Serialize generation for the same staff member across PostgreSQL runs.
	if a.runtimeConfig.DBDriver == databaseDriverPostgres {
		var lockedID int64
		if err := a.queryRowTxDB(tx, "SELECT id FROM users WHERE id = ? FOR UPDATE", userID).Scan(&lockedID); err != nil {
			return err
		}
	}
	var count int
	if err := a.queryRowTxDB(tx, staffPayrollOverlapQuery(), userID, end, start, exceptRunID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("this staff member already has a salary for part or all of this period")
	}
	return nil
}

func salaryWorkspaceURL(runID, userID int64) string {
	query := url.Values{"run_id": {strconv.FormatInt(runID, 10)}, "user_id": {strconv.FormatInt(userID, 10)}}
	return "/admin/staff/salary-payments?" + query.Encode() + "#salary-review"
}

func payrollActionReturnURL(r *http.Request, runID int64) string {
	if r.PostFormValue("return_to") == "salary-workspace" {
		userID, _ := strconv.ParseInt(r.PostFormValue("workspace_staff_id"), 10, 64)
		if userID > 0 {
			return salaryWorkspaceURL(runID, userID)
		}
	}
	return "/admin/payroll/run?id=" + strconv.FormatInt(runID, 10)
}

func (a *App) loadSalaryWorkspace(w http.ResponseWriter, r *http.Request, data *TemplateData, staff []User) bool {
	periods, err := a.listStaffSalaryPeriods(staff)
	if err != nil {
		http.Error(w, "could not load staff salary periods", 500)
		return false
	}
	data.StaffSalaryPeriods = periods
	data.SalaryWorkspace = true
	data.SalaryPeriodLabel = strings.TrimSpace(r.URL.Query().Get("label"))
	selectedID, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	for _, member := range staff {
		if member.ID == selectedID {
			data.SelectedSalaryStaffID = selectedID
			break
		}
	}
	if selectedID > 0 && data.SelectedSalaryStaffID == 0 {
		http.Error(w, "staff member not available", 403)
		return false
	}
	runID, _ := strconv.ParseInt(r.URL.Query().Get("run_id"), 10, 64)
	if runID <= 0 || selectedID <= 0 {
		return true
	}
	run, err := a.findPayrollRunByID(runID)
	if err != nil {
		http.Error(w, "salary period not found", 404)
		return false
	}
	for _, payment := range run.Payments {
		if payment.UserID == selectedID {
			data.PayrollPayments = append(data.PayrollPayments, payment)
		}
	}
	if len(data.PayrollPayments) == 0 {
		http.Error(w, "staff salary not found", 404)
		return false
	}
	for _, period := range periods {
		if period.UserID == selectedID && period.RunID == runID {
			run.Label = period.Label
			break
		}
	}
	data.PayrollRun = run
	data.FinanceAccounts, err = a.listFinanceAccounts(true)
	if err != nil {
		http.Error(w, "could not load payment accounts", 500)
		return false
	}
	return true
}
