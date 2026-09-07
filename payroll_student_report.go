package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func payrollStudentNames(details []PayrollPaymentCalculationDetail) []string {
	names := make([]string, 0)
	seen := make(map[string]bool)
	for _, detail := range details {
		if detail.DetailType != payrollDetailTypePerStudentIncluded && detail.DetailType != payrollDetailTypePerStudentAttendance {
			continue
		}
		name := strings.TrimSpace(detail.Label)
		if _, suffix, ok := strings.Cut(name, " · "); ok {
			name = strings.TrimSpace(suffix)
		}
		if name == "" {
			continue
		}
		key := "label:" + detail.Label
		if detail.SourceID > 0 {
			key = fmt.Sprintf("%s:%d", detail.SourceType, detail.SourceID)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

func (a *App) payrollStudentReportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid salary payment", http.StatusBadRequest)
		return
	}
	payment, run, err := a.findPayrollPaymentByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		log.Printf("load payroll student report %d: %v", id, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = "Student Names"
	data.HideChrome = true
	data.PayrollPayment = payment
	data.PayrollRun = run
	data.PayrollStudentNames = payrollStudentNames(payment.CalculationDetails)
	a.render(w, "payroll-student-report", data, http.StatusOK)
}
