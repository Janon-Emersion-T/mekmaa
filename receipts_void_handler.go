package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (a *App) voidReceiptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	currentUser, ok := a.currentUser(r.Context())
	if !ok || !financeHighRiskAuthorized(currentUser, "finance_transactions.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	transactionID := parseInt64Query(r.FormValue("transaction_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))

	if transactionID <= 0 || reason == "" {
		http.Error(w, "receipt ID and void reason are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	receipt, err := a.findFinanceTransactionByIDContext(ctx, transactionID)
	if err != nil {
		http.Error(w, "receipt not found", http.StatusNotFound)
		return
	}

	if !a.requireDivisionAccessForDivision(w, r, currentUser, receipt.DivisionID) {
		return
	}

	if !receiptSourceVoidAuthorized(currentUser, receipt) {
		http.Error(w, "forbidden for this receipt source", http.StatusForbidden)
		return
	}

	if err := a.voidReceiptFromSource(receipt, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Receipt could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/receipts", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Receipt voided successfully. Linked financial records were synchronized.")
	http.Redirect(w, r, "/admin/receipts", http.StatusSeeOther)
}
