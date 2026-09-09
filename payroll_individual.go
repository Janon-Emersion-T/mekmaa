package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) generateStaffPayroll(periodStart, periodEnd, label string, userID, actorID int64) (int64, error) {
	if userID <= 0 {
		return 0, errors.New("select a staff member")
	}
	if err := validateStaffPayrollDates(periodStart, periodEnd); err != nil {
		return 0, err
	}
	if err := a.checkStaffPayrollOverlap(userID, periodStart, periodEnd); err != nil {
		return 0, err
	}
	profiles, err := a.listStaffSalaryProfiles()
	if err != nil {
		return 0, err
	}
	applicable := false
	for _, profile := range profiles {
		if profile.UserID == userID && salaryProfileAppliesToPayrollPeriod(profile, periodStart, periodEnd) {
			applicable = true
			break
		}
	}
	if !applicable {
		return 0, errors.New("no active salary profiles apply to this staff member and period")
	}
	var runID int64
	err = a.queryRowDB("SELECT id FROM payroll_runs WHERE period_start = ? AND period_end = ?", periodStart, periodEnd).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		runID, err = a.createPayrollRun(periodStart, periodEnd, label, actorID)
	}
	if err != nil {
		return 0, err
	}
	run, err := a.findPayrollRunByID(runID)
	if err != nil {
		return runID, err
	}
	if run.Status == PayrollRunStatusApproved || run.Status == PayrollRunStatusClosed {
		return runID, errors.New("this payroll period is approved or closed")
	}
	run.GenerationLabel = strings.TrimSpace(label)
	return runID, a.syncPayrollRunPayments(*run, actorID, false, userID)
}

func (a *App) generateStaffPayrollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	user, _ := a.currentUser(r.Context())
	id, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || id <= 0 {
		a.setFlash(w, "Select a staff member.")
		http.Redirect(w, r, "/admin/staff/salary-payments", 303)
		return
	}
	staff, err := a.listPayrollEligibleUsersVisibleTo(user)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	allowed := false
	for _, member := range staff {
		if member.ID == id && member.Active {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "staff member not available", 403)
		return
	}
	runID, err := a.generateStaffPayroll(strings.TrimSpace(r.FormValue("period_start")), strings.TrimSpace(r.FormValue("period_end")), strings.TrimSpace(r.FormValue("label")), id, user.ID)
	if err != nil {
		a.setFlash(w, err.Error())
		query := url.Values{"user_id": {strconv.FormatInt(id, 10)}, "label": {r.FormValue("label")}}
		http.Redirect(w, r, "/admin/staff/salary-payments?"+query.Encode()+"#salary-review", 303)
		return
	}
	a.setFlash(w, "Salary generated. Review the calculation below, make any changes, then approve and pay.")
	http.Redirect(w, r, salaryWorkspaceURL(runID, id), 303)
}
