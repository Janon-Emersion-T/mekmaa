package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) payrollManagementHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	runs, err := a.listPayrollRuns()
	if err != nil {
		log.Printf("list payroll runs: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)
	selectedStatus := strings.TrimSpace(r.URL.Query().Get("status"))
	selectedYear := strings.TrimSpace(r.URL.Query().Get("year"))
	yearSeen := make(map[string]struct{})
	filteredRuns := make([]PayrollRun, 0, len(runs))
	summary := PayrollPortfolioSummary{}
	for _, run := range runs {
		year := ""
		if len(run.PeriodStart) >= 4 {
			year = run.PeriodStart[:4]
			if _, ok := yearSeen[year]; !ok {
				yearSeen[year] = struct{}{}
				data.PayrollRunYears = append(data.PayrollRunYears, year)
			}
		}
		if selectedStatus != "" && run.Status != selectedStatus {
			continue
		}
		if selectedYear != "" && year != selectedYear {
			continue
		}
		filteredRuns = append(filteredRuns, run)
		summary.RunCount++
		summary.StaffCount += run.StaffCount
		summary.NetTotal += run.NetTotal
		summary.PaidTotal += run.PaidTotal
		summary.OutstandingTotal += run.OutstandingTotal
		if run.Status != PayrollRunStatusClosed {
			summary.OpenRunCount++
		}
	}
	summary.NetTotal = normalizeMoney(summary.NetTotal)
	summary.PaidTotal = normalizeMoney(summary.PaidTotal)
	summary.OutstandingTotal = normalizeMoney(summary.OutstandingTotal)
	data.Title = "Salary Payments"
	data.Description =
		"Calculate what is due, approve salaries, and record staff payments by period."
	data.PayrollRuns = filteredRuns
	data.PayrollPortfolioSummary = summary
	data.SelectedPayrollStatus = selectedStatus
	data.SelectedPayrollYear = selectedYear

	a.render(
		w,
		"payroll",
		data,
		http.StatusOK,
	)
}

func (a *App) createPayrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		a.setFlash(
			w,
			"Unable to read payroll form.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-payments",
			http.StatusSeeOther,
		)
		return
	}

	periodStart :=
		strings.TrimSpace(
			r.FormValue("period_start"),
		)

	periodEnd :=
		strings.TrimSpace(
			r.FormValue("period_end"),
		)

	label :=
		strings.TrimSpace(
			r.FormValue("label"),
		)

	actorUserID := int64(0)

	if user != nil {
		actorUserID = user.ID
	}

	runID, err := a.createPayrollRun(
		periodStart,
		periodEnd,
		label,
		actorUserID,
	)
	if err != nil {
		log.Printf("create payroll run: %v", err)

		a.setFlash(
			w,
			err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-payments",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Payroll period created.",
	)

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) payrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.URL.Query().Get("id"),
		),
		10,
		64,
	)

	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	run, err := a.findPayrollRunByID(runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}

		log.Printf(
			"find payroll run %d: %v",
			runID,
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	data := a.newTemplateData(w, r, user)

	data.Title = run.Label
	data.Description =
		"Review staff salary calculations, incentives, deductions and payment status."

	data.PayrollRun = run
	data.PayrollPayments = run.Payments

	accounts, err := a.listFinanceAccounts(true)
	if err != nil {
		log.Printf("list payroll finance accounts: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data.FinanceAccounts = accounts

	a.render(
		w,
		"payroll-run",
		data,
		http.StatusOK,
	)
}

func (a *App) generatePayrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("id"),
		),
		10,
		64,
	)

	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	actorUserID := int64(0)

	if user != nil {
		actorUserID = user.ID
	}

	if err := a.generatePayrollRunPayments(
		runID,
		actorUserID,
	); err != nil {
		log.Printf(
			"generate payroll run %d: %v",
			runID,
			err,
		)

		a.setFlash(
			w,
			err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/payroll/run?id="+
				strconv.FormatInt(runID, 10),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Payroll calculations generated.",
	)

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) recalculatePayrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("id"),
		),
		10,
		64,
	)
	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	user, _ := a.currentUser(r.Context())
	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	if err := a.recalculatePayrollRun(
		runID,
		actorUserID,
	); err != nil {
		log.Printf(
			"recalculate payroll run %d: %v",
			runID,
			err,
		)

		a.setFlash(
			w,
			err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/payroll/run?id="+
				strconv.FormatInt(runID, 10),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Payroll recalculated.",
	)

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) updatePayrollQuantityHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	paymentID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("payment_id")),
		10,
		64,
	)
	if err != nil || paymentID <= 0 {
		http.Error(
			w,
			"invalid salary payment",
			http.StatusBadRequest,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("run_id")),
		10,
		64,
	)
	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	quantity, err := strconv.ParseFloat(
		strings.TrimSpace(r.FormValue("quantity")),
		64,
	)
	if err != nil {
		a.setFlash(
			w,
			"Approved quantity must be a valid number.",
		)

		http.Redirect(
			w,
			r,
			"/admin/payroll/run?id="+
				strconv.FormatInt(runID, 10),
			http.StatusSeeOther,
		)
		return
	}

	if err := a.updatePayrollPaymentQuantity(
		paymentID,
		quantity,
	); err != nil {
		log.Printf(
			"update payroll quantity: %v",
			err,
		)

		a.setFlash(w, err.Error())
	} else {
		a.setFlash(
			w,
			"Approved quantity updated.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) addPayrollAdjustmentHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	paymentID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("payment_id")),
		10,
		64,
	)
	if err != nil || paymentID <= 0 {
		http.Error(
			w,
			"invalid salary payment",
			http.StatusBadRequest,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("run_id")),
		10,
		64,
	)
	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	amount, err := strconv.ParseFloat(
		strings.TrimSpace(r.FormValue("amount")),
		64,
	)
	if err != nil {
		a.setFlash(
			w,
			"Adjustment amount must be a valid number.",
		)

		http.Redirect(
			w,
			r,
			"/admin/payroll/run?id="+
				strconv.FormatInt(runID, 10),
			http.StatusSeeOther,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	err = a.addPayrollAdjustment(
		paymentID,
		r.FormValue("adjustment_type"),
		r.FormValue("direction"),
		r.FormValue("description"),
		amount,
		actorUserID,
	)

	if err != nil {
		log.Printf(
			"add payroll adjustment: %v",
			err,
		)

		a.setFlash(w, err.Error())
	} else {
		a.setFlash(
			w,
			"Salary adjustment added.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) deletePayrollAdjustmentHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	adjustmentID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("adjustment_id")),
		10,
		64,
	)

	runID, runErr := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("run_id")),
		10,
		64,
	)

	if err != nil ||
		adjustmentID <= 0 ||
		runErr != nil ||
		runID <= 0 {
		http.Error(
			w,
			"invalid payroll adjustment",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.deletePayrollAdjustment(
		adjustmentID,
	); err != nil {
		log.Printf(
			"delete payroll adjustment: %v",
			err,
		)

		a.setFlash(w, err.Error())
	} else {
		a.setFlash(
			w,
			"Salary adjustment removed.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) approvePayrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	runID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("id")),
		10,
		64,
	)
	if err != nil || runID <= 0 {
		http.Error(
			w,
			"invalid payroll run",
			http.StatusBadRequest,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	if err := a.approvePayrollRun(
		runID,
		actorUserID,
	); err != nil {
		log.Printf(
			"approve payroll run %d: %v",
			runID,
			err,
		)

		a.setFlash(w, err.Error())
	} else {
		a.setFlash(
			w,
			"Payroll approved.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) approvePayrollPaymentHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	paymentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("payment_id")), 10, 64)
	runID, runErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("run_id")), 10, 64)
	if err != nil || paymentID <= 0 || runErr != nil || runID <= 0 {
		http.Error(w, "invalid salary approval request", http.StatusBadRequest)
		return
	}

	user, _ := a.currentUser(r.Context())
	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}
	if err := a.approvePayrollPayment(paymentID, actorUserID); err != nil {
		log.Printf("approve payroll payment %d: %v", paymentID, err)
		a.setFlash(w, err.Error())
	} else {
		a.setFlash(w, "Salary approved individually. You can now record payment.")
	}

	http.Redirect(w, r, "/admin/payroll/run?id="+strconv.FormatInt(runID, 10), http.StatusSeeOther)
}

func (a *App) rollbackPayrollPaymentApprovalHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	paymentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("payment_id")), 10, 64)
	runID, runErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("run_id")), 10, 64)
	if err != nil || paymentID <= 0 || runErr != nil || runID <= 0 {
		http.Error(w, "invalid salary approval rollback request", http.StatusBadRequest)
		return
	}

	user, _ := a.currentUser(r.Context())
	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}
	if err := a.rollbackPayrollPaymentApproval(paymentID, actorUserID); err != nil {
		log.Printf("rollback payroll payment approval %d: %v", paymentID, err)
		a.setFlash(w, err.Error())
	} else {
		a.setFlash(w, "Individual salary approval rolled back. Adjustments are available again.")
	}

	http.Redirect(w, r, "/admin/payroll/run?id="+strconv.FormatInt(runID, 10), http.StatusSeeOther)
}

func (a *App) payPayrollPaymentHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	paymentID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("payment_id"),
		),
		10,
		64,
	)

	runID, runErr := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("run_id"),
		),
		10,
		64,
	)

	accountID, accountErr := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("finance_account_id"),
		),
		10,
		64,
	)

	if err != nil ||
		paymentID <= 0 ||
		runErr != nil ||
		runID <= 0 ||
		accountErr != nil ||
		accountID <= 0 {
		http.Error(
			w,
			"invalid salary payment request",
			http.StatusBadRequest,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	if err := a.payPayrollPayment(
		paymentID,
		accountID,
		r.FormValue("payment_reference"),
		actorUserID,
	); err != nil {
		log.Printf(
			"pay payroll payment %d: %v",
			paymentID,
			err,
		)

		a.setFlash(w, err.Error())
	} else {
		a.setFlash(
			w,
			"Salary payment recorded.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/payroll/run?id="+
			strconv.FormatInt(runID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) closePayrollRunHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || runID <= 0 {
		http.Error(w, "invalid payroll run", http.StatusBadRequest)
		return
	}

	user, _ := a.currentUser(r.Context())
	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	if err := a.closePayrollRun(runID, actorUserID); err != nil {
		log.Printf("close payroll run %d: %v", runID, err)
		a.setFlash(w, err.Error())
	} else {
		a.setFlash(w, "Payroll closed.")
	}

	http.Redirect(w, r, "/admin/payroll/run?id="+strconv.FormatInt(runID, 10), http.StatusSeeOther)
}

func (a *App) payrollSalarySlipHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	paymentID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.URL.Query().Get("id"),
		),
		10,
		64,
	)

	if err != nil || paymentID <= 0 {
		http.Error(
			w,
			"invalid salary payment",
			http.StatusBadRequest,
		)
		return
	}

	payment, run, err :=
		a.findPayrollPaymentByID(paymentID)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}

		log.Printf(
			"find payroll payment for slip %d: %v",
			paymentID,
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if payment.Status != PayrollPaymentStatusPaid {
		if payment.Status != PayrollPaymentStatusApproved &&
			payment.Status != PayrollPaymentStatusVoid {
			http.Error(
				w,
				"salary slip is available after payroll approval",
				http.StatusConflict,
			)
			return
		}
	}

	user, _ := a.currentUser(r.Context())

	data := a.newTemplateData(
		w,
		r,
		user,
	)

	data.Title = "Salary Slip"
	data.Description =
		"Printable staff salary payment slip."
	data.HideChrome = true

	data.PayrollRun = run
	data.PayrollPayment = payment

	if payment.FinanceTransactionID > 0 {
		transaction, err :=
			a.findFinanceTransactionByID(
				payment.FinanceTransactionID,
			)

		if err != nil {
			log.Printf(
				"find payroll finance transaction %d: %v",
				payment.FinanceTransactionID,
				err,
			)
		} else {
			data.PayrollFinanceTransaction =
				transaction
		}
	}

	a.render(
		w,
		"payroll-slip",
		data,
		http.StatusOK,
	)
}

func (a *App) payrollCalculationReportHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	paymentID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.URL.Query().Get("id"),
		),
		10,
		64,
	)

	if err != nil || paymentID <= 0 {
		http.Error(
			w,
			"invalid salary payment",
			http.StatusBadRequest,
		)
		return
	}

	payment, run, err := a.findPayrollPaymentByID(paymentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}

		log.Printf(
			"find payroll payment for report %d: %v",
			paymentID,
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	data := a.newTemplateData(
		w,
		r,
		user,
	)

	data.Title = "Salary Calculation Report"
	data.Description =
		"A4 payroll calculation breakdown for one employee."
	data.HideChrome = true
	data.PayrollRun = run
	data.PayrollPayment = payment

	if payment.FinanceTransactionID > 0 {
		transaction, txErr := a.findFinanceTransactionByID(
			payment.FinanceTransactionID,
		)
		if txErr != nil {
			log.Printf(
				"find payroll finance transaction for report %d: %v",
				payment.FinanceTransactionID,
				txErr,
			)
		} else {
			data.PayrollFinanceTransaction = transaction
		}
	}

	a.render(
		w,
		"payroll-report",
		data,
		http.StatusOK,
	)
}

func (a *App) voidPayrollPaymentHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	paymentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("payment_id")), 10, 64)
	runID, runErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("run_id")), 10, 64)
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	if err != nil || paymentID <= 0 || runErr != nil || runID <= 0 {
		http.Error(w, "invalid salary payment request", http.StatusBadRequest)
		return
	}

	user, _ := a.currentUser(r.Context())
	if user == nil || !containsPermission(user.Permissions, "finance_transactions.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := a.voidPayrollPayment(paymentID, reason, user.ID); err != nil {
		log.Printf("void payroll payment %d: %v", paymentID, err)
		a.setFlash(w, err.Error())
	} else {
		a.setFlash(w, "Salary payment voided. Re-payment is not reopened automatically; the original finance audit trail is preserved.")
	}

	http.Redirect(w, r, "/admin/payroll/run?id="+strconv.FormatInt(runID, 10), http.StatusSeeOther)
}
