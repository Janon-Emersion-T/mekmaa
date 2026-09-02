package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) staffAdvanceManagementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r.Context())
	advances, err := a.listStaffAdvances()
	if err != nil {
		log.Printf("list staff advances: %v", err)
		http.Error(w, "internal server error", 500)
		return
	}
	staff, err := a.listPayrollEligibleUsersVisibleTo(user)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	divisions, err := a.listDivisions(false)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	accounts, err := a.listFinanceAccounts(true)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "Staff Advances"
	data.Description = "Issue staff salary advances and track recovery through payroll or direct repayments."
	data.StaffAdvances = advances
	data.Users = staff
	data.Divisions = divisions
	data.FinanceAccounts = accounts
	a.render(w, "staff-advances", data, http.StatusOK)
}

func (a *App) createStaffAdvanceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	user, _ := a.currentUser(r.Context())
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", 403)
		return
	}
	amount, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	divisionID, _ := strconv.ParseInt(r.FormValue("division_id"), 10, 64)
	accountID, _ := strconv.ParseInt(r.FormValue("finance_account_id"), 10, 64)
	installment, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("installment_amount")), 64)
	err := a.createStaffAdvance(StaffAdvance{UserID: userID, DivisionID: divisionID, FinanceAccountID: accountID, Amount: amount, RecoveryMode: strings.TrimSpace(r.FormValue("recovery_mode")), InstallmentAmount: installment, Notes: r.FormValue("notes")}, user.ID)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/staff/advances", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Staff advance issued and recorded in finance.")
	http.Redirect(w, r, "/admin/staff/advances", http.StatusSeeOther)
}

func (a *App) collectStaffAdvanceRepaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	user, _ := a.currentUser(r.Context())
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", 403)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("advance_id"), 10, 64)
	amount, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err := a.collectStaffAdvanceRepayment(id, amount, r.FormValue("payment_method"), r.FormValue("notes"), user.ID); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/staff/advances", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Advance repayment recorded in finance.")
	http.Redirect(w, r, "/admin/staff/advances", http.StatusSeeOther)
}
