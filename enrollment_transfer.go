package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errEnrollmentTransfer = errors.New("enrollment transfer cannot be completed")

func transferValidation(message string) error {
	return fmt.Errorf("%w: %s", errEnrollmentTransfer, message)
}

// A transfer creates a new finance track and closes only the old programme's
// current memberships. All operations commit together, including status history.
func (a *App) transferStudentEnrollment(enrollmentID int64, destination StudentEnrollment) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if a.runtimeConfig.DBDriver == databaseDriverPostgres {
		var id int64
		if err := a.queryRowTxDB(tx, `SELECT id FROM student_enrollments WHERE id = ? FOR UPDATE`, enrollmentID).Scan(&id); err != nil {
			return 0, err
		}
	}
	old, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		return 0, err
	}
	if !old.Active {
		return 0, transferValidation("Archived enrollments cannot be transferred.")
	}
	if destination.TrainingProgramID == old.TrainingProgramID {
		return 0, transferValidation("Select a different programme.")
	}
	date := strings.TrimSpace(destination.EnrollmentDate)
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return 0, transferValidation("Select a valid transfer date.")
	}
	if date < old.EnrollmentDate || date > time.Now().Format("2006-01-02") {
		return 0, transferValidation("Transfer date must be between the enrollment date and today.")
	}
	if err := validateHistoricalEntryDateValue(date, "transfer date"); err != nil {
		return 0, transferValidation(err.Error())
	}
	var divisionID int64
	var monthlyFee float64
	var active bool
	if err := a.queryRowTxDB(tx, `SELECT COALESCE(division_id, 0), monthly_fee, active FROM training_programs WHERE id = ?`, destination.TrainingProgramID).Scan(&divisionID, &monthlyFee, &active); err != nil {
		return 0, err
	}
	if !active || divisionID != old.DivisionID {
		return 0, transferValidation("Select an active programme in the same division.")
	}
	if destination.DiscountedMonthlyFee < 0 || destination.DiscountedMonthlyFee > normalizeMoney(monthlyFee) {
		return 0, transferValidation("Discounted monthly fee must be between zero and the new programme's monthly fee.")
	}
	if destination.FreeMonthlyFee {
		destination.DiscountedMonthlyFee = 0
	}
	var duplicate bool
	if err := a.queryRowTxDB(tx, `SELECT EXISTS(SELECT 1 FROM student_enrollments WHERE admission_id = ? AND training_program_id = ?)`, old.AdmissionID, destination.TrainingProgramID).Scan(&duplicate); err != nil {
		return 0, err
	}
	if duplicate {
		return 0, transferValidation("This student already has an enrollment in the selected programme, including archived enrollments.")
	}
	var laterAttendance bool
	if err := a.queryRowTxDB(tx, `SELECT EXISTS(SELECT 1 FROM attendance_records ar JOIN student_groups g ON g.id = ar.group_id WHERE ar.admission_id = ? AND g.training_program_id = ? AND ar.attendance_date >= ?)`, old.AdmissionID, old.TrainingProgramID, date).Scan(&laterAttendance); err != nil {
		return 0, err
	}
	if laterAttendance {
		return 0, transferValidation("Choose a transfer date after the last recorded attendance in the old programme.")
	}
	// Refuse to backdate over a membership that started later.
	var laterMembership bool
	if err := a.queryRowTxDB(tx, `SELECT EXISTS(SELECT 1 FROM student_group_membership_history h JOIN student_groups g ON g.id = h.group_id WHERE h.admission_id = ? AND g.training_program_id = ? AND h.effective_from > ?)`, old.AdmissionID, old.TrainingProgramID, date).Scan(&laterMembership); err != nil {
		return 0, err
	}
	if laterMembership {
		return 0, transferValidation("Transfer date cannot precede an existing group assignment.")
	}
	destination.AdmissionID = old.AdmissionID
	destination.EnrollmentDate = date
	newID, _, err := a.createStudentEnrollmentTx(tx, destination, false, "cash", time.Now().UTC(), 0)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if _, err := a.execTxDB(tx, `UPDATE student_enrollments SET active = 0, updated_at = ? WHERE id = ?`, now, old.ID); err != nil {
		return 0, err
	}
	if err := syncStudentEnrollmentStatusHistoryTx(a, tx, old.ID, false, date); err != nil {
		return 0, err
	}
	// Same-day membership intervals have zero length and follow the existing
	// membership-history convention of removing the empty interval.
	if _, err := a.execTxDB(tx, `DELETE FROM student_group_membership_history WHERE admission_id = ? AND group_id IN (SELECT id FROM student_groups WHERE training_program_id = ?) AND effective_from = ?`, old.AdmissionID, old.TrainingProgramID, date); err != nil {
		return 0, err
	}
	if _, err := a.execTxDB(tx, `UPDATE student_group_membership_history SET effective_to = ?, updated_at = ? WHERE admission_id = ? AND group_id IN (SELECT id FROM student_groups WHERE training_program_id = ?) AND effective_from < ? AND (effective_to IS NULL OR effective_to > ?)`, date, now, old.AdmissionID, old.TrainingProgramID, date, date); err != nil {
		return 0, err
	}
	if _, err := a.execTxDB(tx, `DELETE FROM student_group_members WHERE admission_id = ? AND group_id IN (SELECT id FROM student_groups WHERE training_program_id = ?)`, old.AdmissionID, old.TrainingProgramID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}

func (a *App) transferEnrollmentHandler(w http.ResponseWriter, r *http.Request) {
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
	id, err := parsePositiveInt64(r.FormValue("enrollment_id"))
	if err != nil {
		http.Error(w, "invalid enrollment", http.StatusBadRequest)
		return
	}
	old, err := a.findStudentEnrollmentByID(id)
	if err != nil {
		http.Error(w, "enrollment not found", http.StatusNotFound)
		return
	}
	user, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, user, old.DivisionID) {
		return
	}
	target := "/admin/enrollments?admission_id=" + strconv.FormatInt(old.AdmissionID, 10)
	fail := func(message string) {
		a.setFlash(w, message)
		http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(id, 10), http.StatusSeeOther)
	}
	programID, err := parsePositiveInt64(r.FormValue("training_program_id"))
	if err != nil {
		fail("Select a valid destination programme.")
		return
	}
	fee, err := parseNonNegativeFloat(r.FormValue("discounted_monthly_fee"))
	if err != nil {
		fail("Enter a valid discounted monthly fee.")
		return
	}
	if r.FormValue("reviewed") != "true" {
		fail("Review the transfer date, fees and group assignment before transferring.")
		return
	}
	newID, err := a.transferStudentEnrollment(id, StudentEnrollment{TrainingProgramID: programID, EnrollmentDate: r.FormValue("transfer_date"), FreeAdmission: r.FormValue("free_admission") == "true", FreeMonthlyFee: r.FormValue("free_monthly_fee") == "true", DiscountedMonthlyFee: normalizeMoney(fee)})
	if err != nil {
		if errors.Is(err, errEnrollmentTransfer) {
			fail(strings.TrimPrefix(err.Error(), errEnrollmentTransfer.Error()+": "))
			return
		}
		if isUniqueConstraintError(err) {
			fail("This student already has an enrollment in the selected programme.")
			return
		}
		log.Printf("transfer enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Programme transferred. Past payments and attendance are preserved. Assign the student to a group in the new programme and review any outstanding fees.")
	http.Redirect(w, r, target+"&action=view&id="+strconv.FormatInt(newID, 10), http.StatusSeeOther)
}
