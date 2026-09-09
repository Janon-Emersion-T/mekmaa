package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (admission Admission) StatusLabel() string {
	if admission.Status == "inactive" {
		return "Inactive"
	}
	return "Active"
}

func (a *App) setStudentStatus(admissionID int64, status string) error {
	if status != "active" && status != "inactive" {
		return errors.New("invalid student status")
	}
	result, err := a.execDB("UPDATE admissions SET status = ?, updated_at = ? WHERE id = ?", status, time.Now().UTC(), admissionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) updateStudentStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	admissionID, err := strconv.ParseInt(r.PostFormValue("admission_id"), 10, 64)
	status := r.PostFormValue("status")
	if err != nil || admissionID <= 0 || (status != "active" && status != "inactive") {
		http.Error(w, "invalid student id or status", http.StatusBadRequest)
		return
	}
	user, _ := a.currentUser(r.Context())
	var divisionIDs []int64
	if !canViewAllDivisions(user) {
		var ok bool
		divisionIDs, ok = a.requireOperationalDivisionScope(w, r, user)
		if !ok {
			return
		}
	}
	if _, err := a.findAdmissionIdentityByIDForDivisionIDs(admissionID, divisionIDs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "student not found", http.StatusNotFound)
		} else {
			log.Printf("find student for status update: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	if err := a.setStudentStatus(admissionID, status); err != nil {
		log.Printf("update student status: %v", err)
		http.Error(w, "could not update student status", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Student marked "+status+".")
	// Only copy directory filters into the return URL.
	query := url.Values{}
	for _, key := range []string{"search", "division", "direction", "limit", "page", "status"} {
		if value := r.URL.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}
	http.Redirect(w, r, "/admin/students?"+query.Encode()+"#admissions-directory", http.StatusSeeOther)
}
