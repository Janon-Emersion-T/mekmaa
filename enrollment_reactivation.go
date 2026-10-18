package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) reactivateStudentEnrollment(enrollmentID int64, effectiveDate string) error {
	effectiveDate = strings.TrimSpace(effectiveDate)
	parsed, err := time.Parse("2006-01-02", effectiveDate)
	if err != nil || parsed.Format("2006-01-02") != effectiveDate {
		return errors.New("select a valid re-enrollment date")
	}
	if effectiveDate > time.Now().Format("2006-01-02") {
		return errors.New("re-enrollment date cannot be in the future")
	}
	if err := validateHistoricalEntryDateValue(effectiveDate, "re-enrollment date"); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if a.runtimeConfig.DBDriver == databaseDriverPostgres {
		var lockedID int64
		if err := a.queryRowTxDB(tx,
			"SELECT id FROM student_enrollments WHERE id = ? FOR UPDATE",
			enrollmentID,
		).Scan(&lockedID); err != nil {
			return err
		}
	}

	enrollment, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		return err
	}
	if enrollment.Active {
		return errors.New("enrollment is already active")
	}
	if effectiveDate < enrollment.EnrollmentDate {
		return errors.New("re-enrollment cannot precede original enrollment")
	}

	var programmeActive bool
	if err := a.queryRowTxDB(tx,
		"SELECT active FROM training_programs WHERE id = ?",
		enrollment.TrainingProgramID,
	).Scan(&programmeActive); err != nil {
		return err
	}
	if !programmeActive {
		return errors.New("the programme is inactive")
	}

	var lastEnd string
	err = a.queryRowTxDB(tx, `
        SELECT CAST(effective_from AS TEXT)
        FROM student_enrollment_status_history
        WHERE enrollment_id = ? AND active = 0 AND effective_to IS NULL
        ORDER BY effective_from DESC, id DESC LIMIT 1
    `, enrollmentID).Scan(&lastEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("archived enrollment has no valid unenrollment history")
	}
	if err != nil {
		return err
	}
	if effectiveDate <= lastEnd {
		return errors.New("re-enrollment date must be after the unenrollment date")
	}

	result, err := a.execTxDB(tx, `
        UPDATE student_enrollments
        SET active = 1, updated_at = ?
        WHERE id = ? AND active = 0
    `, time.Now().UTC(), enrollmentID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("enrollment status changed; please refresh")
	}

	if err := syncStudentEnrollmentStatusHistoryTx(
		a, tx, enrollmentID, true, effectiveDate,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) reenrollEnrollmentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	enrollmentID, err := parsePositiveInt64(r.FormValue("enrollment_id"))
	if err != nil {
		http.Error(w, "invalid enrollment", http.StatusBadRequest)
		return
	}
	enrollment, err := a.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		http.Error(w, "enrollment not found", http.StatusNotFound)
		return
	}
	user, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, user, enrollment.DivisionID) {
		return
	}
	target := "/admin/enrollments?admission_id=" +
		strconv.FormatInt(enrollment.AdmissionID, 10)
	if division := strings.TrimSpace(r.FormValue("division")); division != "" {
		target = withDivisionQuery(target, division)
	}
	target += "&action=view&id=" + strconv.FormatInt(enrollmentID, 10)

	if err := a.reactivateStudentEnrollment(
		enrollmentID, r.FormValue("reenrollment_date"),
	); err != nil {
		log.Printf("re-enrollment rejected: %v", err)
		a.setFlash(w, err.Error())
	} else {
		a.setFlash(w,
			"Student re-enrolled. Review monthly fees and assign the student to a group.")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
