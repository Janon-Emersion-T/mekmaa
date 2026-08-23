package main

import (
	"database/sql"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) coachManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	divisionIDs, ok := a.requireOperationalDivisionScope(w, r, user)
	if !ok {
		return
	}
	coaches, err := a.listCoachUsersDetailedByDivisionIDs(divisionIDs, true)
	if err != nil {
		log.Printf("list coaches: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	selectedDate := strings.TrimSpace(r.URL.Query().Get("date"))
	if selectedDate == "" {
		selectedDate = time.Now().Format("2006-01-02")
	}
	parsedDate, err := time.Parse("2006-01-02", selectedDate)
	if err != nil || parsedDate.Format("2006-01-02") > time.Now().Format("2006-01-02") {
		selectedDate = time.Now().Format("2006-01-02")
	} else if err := validateHistoricalEntryTime(parsedDate, "attendance date"); err != nil {
		selectedDate = companyHistoricalEntryStartDate
	}

	coachIDs := make([]int64, 0, len(coaches))
	for _, coach := range coaches {
		coachIDs = append(coachIDs, coach.ID)
	}
	records, err := a.listCoachAttendanceRecordsByUserIDs(selectedDate, coachIDs)
	if err != nil {
		log.Printf("list coach attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Coach Management"
	data.Description = "Manage coaches and coach attendance."
	data.Coaches = coaches
	if canViewAllDivisions(user) || userHasDivisionCode(user, divisionCodeSports) {
		data.Games, _ = a.listGames(false)
	}
	for _, coach := range coaches {
		if coach.CoachType == "main" {
			data.AvailableCoaches = append(data.AvailableCoaches, coach)
		}
	}
	data.CoachAttendanceRecords = records
	data.AttendanceDate = selectedDate
	data.TodayDate = time.Now().Format("2006-01-02")
	a.render(w, "coach-management", data, http.StatusOK)
}

func (a *App) roleManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	roles, err := a.listRoles()
	if err != nil {
		log.Printf("list roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Role Management"
	data.Description = "Manage roles."
	data.Roles = roles
	data.Permissions = allPermissions
	data.PermissionGroups = permissionGroups
	a.render(w, "role-management", data, http.StatusOK)
}

func (a *App) admissionManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	filter := admissionsFilterFromRequest(r)
	if !canViewAllDivisions(user) {
		operationalDivisionIDs, ok := a.requireOperationalDivisionScope(w, r, user)
		if !ok {
			return
		}
		if filter.Division != "" {
			selectedDivision, err := a.findDivisionBySlugOrCode(filter.Division)
			allowed := false
			if err == nil {
				for _, divisionID := range operationalDivisionIDs {
					if divisionID == selectedDivision.ID {
						allowed = true
						break
					}
				}
			}
			if err != nil || !allowed {
				a.writeDivisionForbidden(w, r, user)
				return
			}
			filter.DivisionIDs = []int64{selectedDivision.ID}
		} else {
			filter.DivisionIDs = operationalDivisionIDs
		}
	} else if filter.Division != "" {
		selectedDivision, err := a.findDivisionBySlugOrCode(filter.Division)
		if err == nil {
			filter.DivisionIDs = []int64{selectedDivision.ID}
		}
	}
	admissions, totalAdmissions, err := a.listAdmissionsFiltered(filter)
	if err != nil {
		log.Printf("list admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingPrograms, err := a.listTrainingProgramsByDivisionIDs(filter.DivisionIDs, true, false)
	if err != nil {
		log.Printf("list training programmes for admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Students"
	data.Description = "Manage the shared student master and division enrollments."
	data.Admissions = admissions
	data.AdmissionsTotal = totalAdmissions
	data.AdmissionsFilter = filter
	data.AdmissionsTotalPages = admissionsTotalPages(totalAdmissions, filter.Limit)
	data.AdmissionsPageNumbers = admissionsPageWindow(filter.Page, data.AdmissionsTotalPages)
	data.AdmissionsHasPreviousPage = filter.Page > 1
	data.AdmissionsHasNextPage = filter.Page < data.AdmissionsTotalPages
	data.AdmissionsPreviousPageURL = admissionsFilterPageURL(r, filter, filter.Page-1)
	data.AdmissionsNextPageURL = admissionsFilterPageURL(r, filter, filter.Page+1)
	data.AdmissionsPageBaseURL = admissionsFilterBaseURL(r, filter)
	if totalAdmissions > 0 {
		data.AdmissionsStart = (filter.Page-1)*filter.Limit + 1
		data.AdmissionsEnd = data.AdmissionsStart + len(admissions) - 1
	}
	data.TrainingPrograms = trainingPrograms
	if filter.Division != "" {
		if selectedDivision, err := a.findDivisionBySlugOrCode(filter.Division); err == nil {
			data.SelectedDivision = selectedDivision
			data.SelectedDivisionScope = selectedDivision.Slug
		}
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.AdmissionMode = mode
	}
	if data.AdmissionMode == "view" || data.AdmissionMode == "edit" {
		admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && admissionID > 0 {
			selectedAdmission, err := a.findAdmissionIdentityByIDForDivisionIDs(admissionID, filter.DivisionIDs)
			if err == nil {
				data.SelectedAdmission = selectedAdmission
			} else if errors.Is(err, sql.ErrNoRows) && !canViewAllDivisions(user) {
				a.writeDivisionForbidden(w, r, user)
				return
			}
		}
	}
	a.render(w, "admission-management", data, http.StatusOK)
}

func (a *App) studentIDCardHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}

	divisionIDs, ok := a.requireOperationalDivisionScope(w, r, user)
	if !ok {
		return
	}
	admission, err := a.findAdmissionIdentityByIDForDivisionIDs(admissionID, divisionIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) && !canViewAllDivisions(user) {
			a.writeDivisionForbidden(w, r, user)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "student not found", http.StatusNotFound)
			return
		}
		log.Printf("find admission for student id card: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student ID"
	data.Description = "Printable student identity card."
	data.HideChrome = true
	data.SelectedAdmission = admission
	a.render(w, "student-id-card", data, http.StatusOK)
}

func (a *App) enrollmentManagementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	var err error
	enrollmentDivisionIDs, ok := a.requireOperationalDivisionScope(w, r, user)
	if !ok {
		return
	}
	enrollments, err := a.listStudentEnrollmentsByDivisionIDs(enrollmentDivisionIDs)
	if err != nil {
		log.Printf("list student enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admissions, err := a.listAdmissionIdentities()
	if err != nil {
		log.Printf("list admissions for enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingPrograms, err := a.listTrainingProgramsByDivisionIDs(enrollmentDivisionIDs, false, true)
	if err != nil {
		log.Printf("list training programmes for enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Enrollment Manager"
	data.Description = "Assign students to programmes and collect programme-level fees."
	data.Enrollments = enrollments
	data.Admissions = admissions
	data.TrainingPrograms = trainingPrograms

	selectedAdmissionID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("admission_id")), 10, 64)
	if selectedAdmissionID > 0 {
		selectedAdmission, err := a.findAdmissionIdentityByID(selectedAdmissionID)
		if err == nil {
			data.SelectedAdmission = selectedAdmission
		}
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.EnrollmentMode = mode
	}
	if data.EnrollmentMode == "view" || data.EnrollmentMode == "edit" {
		enrollmentID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && enrollmentID > 0 {
			if !canViewAllDivisions(user) {
				divisionID, err := a.findStudentEnrollmentDivisionByID(enrollmentID)
				if err != nil {
					if !errors.Is(err, sql.ErrNoRows) {
						log.Printf("find enrollment division for management: %v", err)
						http.Error(w, "internal server error", http.StatusInternalServerError)
						return
					}
				} else if !a.requireDivisionAccessForDivision(w, r, user, divisionID) {
					return
				}
			}
			selectedEnrollment, err := a.findStudentEnrollmentByIDForDivisionIDs(enrollmentID, enrollmentDivisionIDs)
			if err == nil {
				if data.SelectedAdmission != nil && data.SelectedAdmission.ID != selectedEnrollment.AdmissionID {
					data.SelectedAdmission = nil
				}
				data.SelectedEnrollment = selectedEnrollment
				if data.SelectedAdmission == nil {
					selectedAdmission, admissionErr := a.findAdmissionIdentityByID(selectedEnrollment.AdmissionID)
					if admissionErr == nil {
						data.SelectedAdmission = selectedAdmission
					}
				}
			}
		}
	}
	a.render(w, "enrollment-management", data, http.StatusOK)
}

func (a *App) createEnrollmentHandler(w http.ResponseWriter, r *http.Request) {
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

	admissionID, err := parsePositiveInt64(r.FormValue("admission_id"))
	if err != nil {
		http.Error(w, "select a valid student", http.StatusBadRequest)
		return
	}
	target := "/admin/enrollments?admission_id=" + strconv.FormatInt(admissionID, 10)
	if _, err := a.findAdmissionIdentityByID(admissionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "student not found", http.StatusBadRequest)
			return
		}
		log.Printf("find student identity for enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingProgramID, err := parsePositiveInt64(r.FormValue("training_program_id"))
	if err != nil {
		http.Error(w, "select a valid training programme", http.StatusBadRequest)
		return
	}
	trainingProgram, err := a.findTrainingProgramByID(trainingProgramID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "training programme not found", http.StatusBadRequest)
			return
		}
		log.Printf("find training programme for enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, currentUser, trainingProgram.DivisionID) {
		return
	}
	if division := strings.TrimSpace(r.FormValue("division")); division != "" {
		target = withDivisionQuery(target, division)
	}

	enrollmentDate := strings.TrimSpace(
		r.FormValue("enrollment_date"),
	)
	discountedMonthlyFee, err := parseNonNegativeFloat(
		r.FormValue("discounted_monthly_fee"),
	)
	if err != nil {
		a.setFlash(w, "Enter a valid discounted monthly fee.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	discountedMonthlyFee = normalizeMoney(discountedMonthlyFee)
	if discountedMonthlyFee > normalizeMoney(trainingProgram.MonthlyFee) {
		a.setFlash(w, "Discounted monthly fee cannot exceed the programme monthly fee.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	if _, err := time.Parse(
		"2006-01-02",
		enrollmentDate,
	); err != nil {
		a.setFlash(w, "Select a valid enrollment date.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	if enrollmentDate > time.Now().Format("2006-01-02") {
		a.setFlash(w, "Enrollment date cannot be in the future.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := validateHistoricalEntryDateValue(enrollmentDate, "enrollment date"); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	enrollment := StudentEnrollment{
		AdmissionID:          admissionID,
		EnrollmentDate:       enrollmentDate,
		TrainingProgramID:    trainingProgramID,
		TrainingProgramName:  trainingProgram.Name,
		FreeAdmission:        r.FormValue("free_admission") == "true",
		FreeMonthlyFee:       r.FormValue("free_monthly_fee") == "true",
		DiscountedMonthlyFee: discountedMonthlyFee,
	}
	if enrollment.FreeMonthlyFee {
		enrollment.DiscountedMonthlyFee = 0
	}
	if err := validateEnrollment(enrollment); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	collectPayment := r.FormValue("payment_collected") == "true" && !enrollment.FreeAdmission
	paymentMethod := normalizePaymentMethod(r.FormValue("payment_method"))
	collectedAt, collectedAtErr := parseFinanceRecordedAtDate(
		r.FormValue("payment_collected_at"),
		time.Now(),
		"Payment collection date",
	)
	if collectPayment && !validPaymentMethod(paymentMethod) {
		a.setFlash(w, "Select a valid payment method.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if collectPayment && collectedAtErr != nil {
		a.setFlash(w, collectedAtErr.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	_, financeTransactionID, err := a.createStudentEnrollmentWithOptionalPaymentAt(enrollment, collectPayment, paymentMethod, collectedAt, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrAdmissionFeeNotConfigured) {
			a.setFlash(w, err.Error())
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if isUniqueConstraintError(err) {
			a.setFlash(w, "This student is already enrolled in the selected training programme.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		log.Printf("create student enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if collectPayment && financeTransactionID > 0 {
		http.Redirect(w, r, withDivisionQuery("/admin/finance/receipt?transaction_id="+strconv.FormatInt(financeTransactionID, 10), strings.TrimSpace(r.FormValue("division"))), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Enrollment created.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) collectEnrollmentAdmissionPaymentHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/enrollments"
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
	if division := strings.TrimSpace(r.FormValue("division")); division != "" {
		target = withDivisionQuery(target, division)
	}
	enrollmentID, err := parsePositiveInt64(r.FormValue("enrollment_id"))
	if err != nil {
		a.setFlash(w, "Select a valid enrollment.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	enrollment, err := a.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Enrollment not found.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		log.Printf("find enrollment for fee collection: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, currentUser, enrollment.DivisionID) {
		return
	}
	target = "/admin/enrollments?admission_id=" + strconv.FormatInt(enrollment.AdmissionID, 10)
	if division := strings.TrimSpace(r.FormValue("division")); division != "" {
		target = withDivisionQuery(target, division)
	}
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	paymentMethod := normalizePaymentMethod(r.FormValue("payment_method"))
	collectedAt, collectedAtErr := parseFinanceRecordedAtDate(
		r.FormValue("payment_collected_at"),
		time.Now(),
		"Payment collection date",
	)
	if !validPaymentMethod(paymentMethod) {
		a.setFlash(w, "Select a valid payment method.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if collectedAtErr != nil {
		a.setFlash(w, collectedAtErr.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	transactionID, err := a.collectEnrollmentAdmissionPaymentAtTx(tx, *enrollment, paymentMethod, collectedAt, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrAdmissionFeeNotConfigured) {
			a.setFlash(w, err.Error())
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		log.Printf("collect enrollment admission fee: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, withDivisionQuery("/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), strings.TrimSpace(r.FormValue("division"))), http.StatusSeeOther)
}

func (a *App) updateEnrollmentHandler(w http.ResponseWriter, r *http.Request) {
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
		a.setFlash(w, "Select a valid enrollment.")
		http.Redirect(w, r, "/admin/enrollments", http.StatusSeeOther)
		return
	}
	existing, err := a.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Enrollment not found.")
			http.Redirect(w, r, "/admin/enrollments", http.StatusSeeOther)
			return
		}
		log.Printf("find enrollment for update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !a.requireDivisionAccessForDivision(w, r, currentUser, existing.DivisionID) {
		return
	}
	target := "/admin/enrollments?admission_id=" + strconv.FormatInt(existing.AdmissionID, 10)
	if !existing.Active {
		a.setFlash(w, "Archived enrollments cannot be edited.")
		http.Redirect(w, r, target+"&action=view&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
		return
	}

	trainingProgramID, err := parsePositiveInt64(r.FormValue("training_program_id"))
	if err != nil {
		a.setFlash(w, "Select a valid training programme.")
		http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
		return
	}
	trainingProgram, err := a.findTrainingProgramByID(trainingProgramID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Training programme not found.")
			http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
			return
		}
		log.Printf("find training programme for enrollment update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, trainingProgram.DivisionID) {
		return
	}
	if trainingProgram.DivisionID != existing.DivisionID {
		a.setFlash(w, "Enrollments cannot be moved between divisions.")
		http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
		return
	}

	enrollmentDate := strings.TrimSpace(
		r.FormValue("enrollment_date"),
	)
	discountedMonthlyFee, err := parseNonNegativeFloat(
		r.FormValue("discounted_monthly_fee"),
	)
	if err != nil {
		a.setFlash(w, "Enter a valid discounted monthly fee.")
		http.Redirect(
			w,
			r,
			target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10),
			http.StatusSeeOther,
		)
		return
	}
	discountedMonthlyFee = normalizeMoney(discountedMonthlyFee)
	if discountedMonthlyFee > normalizeMoney(trainingProgram.MonthlyFee) {
		a.setFlash(w, "Discounted monthly fee cannot exceed the programme monthly fee.")
		http.Redirect(
			w,
			r,
			target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10),
			http.StatusSeeOther,
		)
		return
	}

	if _, err := time.Parse(
		"2006-01-02",
		enrollmentDate,
	); err != nil {
		a.setFlash(w, "Select a valid enrollment date.")
		http.Redirect(
			w,
			r,
			target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10),
			http.StatusSeeOther,
		)
		return
	}

	if enrollmentDate > time.Now().Format("2006-01-02") {
		a.setFlash(w, "Enrollment date cannot be in the future.")
		http.Redirect(
			w,
			r,
			target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10),
			http.StatusSeeOther,
		)
		return
	}
	if err := validateHistoricalEntryDateValue(enrollmentDate, "enrollment date"); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10),
			http.StatusSeeOther,
		)
		return
	}

	enrollment := StudentEnrollment{
		ID:                   enrollmentID,
		AdmissionID:          existing.AdmissionID,
		EnrollmentDate:       enrollmentDate,
		TrainingProgramID:    trainingProgramID,
		TrainingProgramName:  trainingProgram.Name,
		FreeAdmission:        r.FormValue("free_admission") == "true",
		FreeMonthlyFee:       r.FormValue("free_monthly_fee") == "true",
		DiscountedMonthlyFee: discountedMonthlyFee,
	}
	if enrollment.FreeMonthlyFee {
		enrollment.DiscountedMonthlyFee = 0
	}
	if err := validateEnrollment(enrollment); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
		return
	}

	if err := a.updateStudentEnrollment(enrollment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Enrollment not found.")
			http.Redirect(w, r, "/admin/enrollments", http.StatusSeeOther)
			return
		}
		if isUniqueConstraintError(err) {
			a.setFlash(w, "This student is already enrolled in the selected training programme.")
			http.Redirect(w, r, target+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
			return
		}
		log.Printf("update student enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Enrollment updated.")
	http.Redirect(w, r, target+"&action=view&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
}

func (a *App) deleteEnrollmentHandler(w http.ResponseWriter, r *http.Request) {
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
		a.setFlash(w, "Select a valid enrollment.")
		http.Redirect(w, r, "/admin/enrollments", http.StatusSeeOther)
		return
	}
	target := "/admin/enrollments"
	if enrollment, findErr := a.findStudentEnrollmentByID(enrollmentID); findErr == nil {
		currentUser, _ := a.currentUser(r.Context())
		if !a.requireDivisionAccessForDivision(w, r, currentUser, enrollment.DivisionID) {
			return
		}
		target = "/admin/enrollments?admission_id=" + strconv.FormatInt(enrollment.AdmissionID, 10)
	}

	archived, err := a.deleteStudentEnrollment(enrollmentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Enrollment not found.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	if archived {
		a.setFlash(w, "Enrollment archived because finance history exists.")
		http.Redirect(w, r, target+"&action=view&id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Enrollment deleted.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) trainingProgramManagementHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())

	trainingPrograms, err := a.listTrainingPrograms(true)
	if err != nil {
		log.Printf("list training programmes: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Training Manager"
	data.Description = "Manage training programmes and student fees."
	data.TrainingPrograms = trainingPrograms
	data.ActiveDivisions, _ = a.listDivisions(true)
	if divisionSlug := strings.TrimSpace(r.URL.Query().Get("division")); divisionSlug != "" {
		division, err := a.findDivisionBySlugOrCode(divisionSlug)
		if err == nil {
			if !a.requireDivisionAccessForDivision(w, r, user, division.ID) {
				return
			}
			data.SelectedDivision = division
			data.SelectedDivisionScope = division.Slug

			if strings.EqualFold(
				strings.TrimSpace(division.Code),
				divisionCodeSports,
			) {
				data.Games, _ = a.listGames(false)
			}

			filteredPrograms := make([]TrainingProgram, 0, len(data.TrainingPrograms))
			for _, program := range data.TrainingPrograms {
				if program.DivisionID == division.ID {
					filteredPrograms = append(filteredPrograms, program)
				}
			}
			data.TrainingPrograms = filteredPrograms
		}
	}
	if data.SelectedDivision == nil &&
		(canViewAllDivisions(user) ||
			userHasDivisionCode(user, divisionCodeSports)) {
		data.Games, _ = a.listGames(false)
	}

	if !canViewAllDivisions(user) {
		filteredPrograms := make([]TrainingProgram, 0, len(trainingPrograms))
		for _, program := range trainingPrograms {
			if userCanAccessDivision(user, program.DivisionID) {
				filteredPrograms = append(filteredPrograms, program)
			}
		}
		data.TrainingPrograms = filteredPrograms
	}

	mode := strings.ToLower(
		strings.TrimSpace(r.URL.Query().Get("action")),
	)

	switch mode {
	case "new", "view", "edit":
		data.TrainingProgramMode = mode
	}

	if data.TrainingProgramMode == "view" ||
		data.TrainingProgramMode == "edit" {
		programID, err := strconv.ParseInt(
			strings.TrimSpace(r.URL.Query().Get("id")),
			10,
			64,
		)
		if err != nil || programID <= 0 {
			http.Error(
				w,
				"invalid training programme id",
				http.StatusBadRequest,
			)
			return
		}

		selectedProgram, err := a.findTrainingProgramByID(programID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(
					w,
					"training programme not found",
					http.StatusNotFound,
				)
				return
			}

			log.Printf("find training programme: %v", err)
			http.Error(
				w,
				"internal server error",
				http.StatusInternalServerError,
			)
			return
		}

		data.SelectedTrainingProgram = selectedProgram
		if !a.requireDivisionAccessForDivision(w, r, user, selectedProgram.DivisionID) {
			return
		}
	}

	a.render(
		w,
		"training-program-management",
		data,
		http.StatusOK,
	)
}

func (a *App) createTrainingProgramHandler(
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
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	target := "/admin/training-programs?action=new"

	if division := strings.TrimSpace(
		r.FormValue("division"),
	); division != "" {
		target = withQueryValue(
			target,
			"division",
			division,
		)
	}

	program, err := a.trainingProgramFromRequest(r)
	if err != nil {
		a.setFlash(w, "Training programme could not be created: "+err.Error())
		http.Redirect(
			w,
			r,
			target,
			http.StatusSeeOther,
		)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if program.DivisionCode == divisionCodeCorporate {
		a.setFlash(w, "Corporate/shared cannot be used for student programmes.")
		http.Redirect(w, r, "/admin/training-programs?action=new", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, program.DivisionID) {
		return
	}

	programID, err := a.createTrainingProgram(program)
	if err != nil {
		log.Printf("create training programme: %v", err)

		message := "Training programme could not be created."

		if isUniqueConstraintError(err) {
			message = "A programme already exists for this activity and training format."
		}

		a.setFlash(w, message)
		http.Redirect(
			w,
			r,
			target,
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme created successfully.")

	successTarget := "/admin/training-programs?action=view&id=" +
		strconv.FormatInt(programID, 10)

	if program.DivisionName != "" {
		if division, err := a.findDivisionByID(program.DivisionID); err == nil {
			successTarget = withQueryValue(
				successTarget,
				"division",
				division.Slug,
			)
		}
	}

	http.Redirect(
		w,
		r,
		successTarget,
		http.StatusSeeOther,
	)
}

func (a *App) updateTrainingProgramHandler(
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
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}

	program, err := a.trainingProgramFromRequest(r)
	if err != nil {
		a.setFlash(w, "Training programme could not be updated: "+err.Error())

		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=edit&id="+
				strconv.FormatInt(programID, 10),
			http.StatusSeeOther,
		)
		return
	}

	program.ID = programID
	currentUser, _ := a.currentUser(r.Context())
	existingProgram, err := a.findTrainingProgramByID(programID)
	if err != nil {
		http.Error(w, "training programme not found", http.StatusNotFound)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, existingProgram.DivisionID) {
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, program.DivisionID) {
		return
	}
	if program.DivisionCode == divisionCodeCorporate {
		a.setFlash(w, "Corporate/shared cannot be used for student programmes.")
		http.Redirect(w, r, "/admin/training-programs?action=edit&id="+strconv.FormatInt(programID, 10), http.StatusSeeOther)
		return
	}

	if err := a.updateTrainingProgram(program); err != nil {
		log.Printf("update training programme: %v", err)

		message := "Training programme could not be updated."

		if isUniqueConstraintError(err) {
			message = "A programme already exists for this activity and training format."
		}

		a.setFlash(w, message)

		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=edit&id="+
				strconv.FormatInt(programID, 10),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme updated successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs?action=view&id="+
			strconv.FormatInt(programID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) toggleTrainingProgramHandler(
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
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}

	active, err := strconv.ParseBool(
		strings.TrimSpace(r.FormValue("active")),
	)
	if err != nil {
		http.Error(w, "invalid programme status", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	program, err := a.findTrainingProgramByID(programID)
	if err != nil {
		http.Error(w, "training programme not found", http.StatusNotFound)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, program.DivisionID) {
		return
	}

	if err := a.setTrainingProgramActive(programID, active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "training programme not found", http.StatusNotFound)
			return
		}

		log.Printf("toggle training programme: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if active {
		a.setFlash(w, "Training programme activated successfully.")
	} else {
		a.setFlash(w, "Training programme deactivated successfully.")
	}

	http.Redirect(
		w,
		r,
		"/admin/training-programs",
		http.StatusSeeOther,
	)
}

func (a *App) deleteTrainingProgramHandler(
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
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	program, err := a.findTrainingProgramByID(programID)
	if err != nil {
		http.Error(w, "training programme not found", http.StatusNotFound)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, program.DivisionID) {
		return
	}

	if err := a.deleteTrainingProgram(programID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "training programme not found", http.StatusNotFound)
			return
		}

		a.setFlash(w, "Training programme could not be deleted: "+err.Error())

		http.Redirect(
			w,
			r,
			"/admin/training-programs",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme deleted successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs",
		http.StatusSeeOther,
	)
}

func (a *App) trainingProgramFromRequest(
	r *http.Request,
) (TrainingProgram, error) {
	name := strings.TrimSpace(r.FormValue("name"))

	divisionID, err := parsePositiveInt64(r.FormValue("division_id"))
	if err != nil {
		return TrainingProgram{}, errors.New("valid division is required")
	}

	division, err := a.findDivisionByID(divisionID)
	if err != nil {
		return TrainingProgram{}, errors.New("selected division was not found")
	}

	if !division.Active {
		return TrainingProgram{}, errors.New("selected division is inactive")
	}

	if strings.EqualFold(
		strings.TrimSpace(division.Code),
		divisionCodeCorporate,
	) {
		return TrainingProgram{}, errors.New(
			"Corporate/shared cannot be used for student programmes",
		)
	}

	trainingFormat := strings.ToLower(
		strings.TrimSpace(r.FormValue("training_format")),
	)

	admissionFee, err := parseNonNegativeFloat(
		r.FormValue("admission_fee"),
	)
	if err != nil {
		return TrainingProgram{}, errors.New(
			"enter a valid admission fee",
		)
	}

	monthlyFee, err := parseNonNegativeFloat(
		r.FormValue("monthly_fee"),
	)
	if err != nil {
		return TrainingProgram{}, errors.New(
			"enter a valid monthly fee",
		)
	}

	sortOrder := 0

	if value := strings.TrimSpace(r.FormValue("sort_order")); value != "" {
		sortOrder, err = strconv.Atoi(value)
		if err != nil || sortOrder < 0 || sortOrder > 100000 {
			return TrainingProgram{}, errors.New(
				"sort order must be between 0 and 100000",
			)
		}
	}

	program := TrainingProgram{
		DivisionID:     divisionID,
		DivisionCode:   division.Code,
		DivisionName:   division.Name,
		Name:           name,
		TrainingFormat: trainingFormat,
		AdmissionFee:   admissionFee,
		MonthlyFee:     monthlyFee,
		Active:         r.FormValue("active") == "on",
		SortOrder:      sortOrder,
	}

	switch strings.ToUpper(strings.TrimSpace(division.Code)) {
	case divisionCodeSports:
		gameID, err := parsePositiveInt64(r.FormValue("game_id"))
		if err != nil {
			return TrainingProgram{}, errors.New(
				"valid game is required",
			)
		}

		game, err := a.findGameByID(gameID)
		if err != nil {
			return TrainingProgram{}, errors.New(
				"selected game was not found",
			)
		}

		program.GameID = gameID
		program.Activity = normalizeTrainingActivity(game.Activity)

	case divisionCodeKEC, divisionCodeChess:
		activity := strings.TrimSpace(r.FormValue("activity"))
		if activity == "" {
			if strings.EqualFold(
				division.Code,
				divisionCodeKEC,
			) {
				return TrainingProgram{}, errors.New(
					"subject is required",
				)
			}

			return TrainingProgram{}, errors.New(
				"programme focus is required",
			)
		}

		program.GameID = 0
		program.Activity = normalizeTrainingActivity(activity)

	default:
		return TrainingProgram{}, errors.New(
			"unsupported programme division",
		)
	}

	if err := validateTrainingProgram(program); err != nil {
		return TrainingProgram{}, err
	}

	return program, nil
}

func validateTrainingProgram(program TrainingProgram) error {
	if program.Name == "" {
		return errors.New("programme name is required")
	}

	if len(program.Name) > 120 {
		return errors.New(
			"programme name must not exceed 120 characters",
		)
	}

	if program.Activity == "" {
		return errors.New("activity is required")
	}

	if strings.EqualFold(
		strings.TrimSpace(program.DivisionCode),
		divisionCodeSports,
	) && program.GameID <= 0 {
		return errors.New("game is required")
	}

	if len(program.Activity) > 60 {
		return errors.New(
			"activity must not exceed 60 characters",
		)
	}

	switch program.TrainingFormat {
	case "one_to_one", "group":
	default:
		return errors.New(
			"training format must be one-to-one or group",
		)
	}

	if math.IsNaN(program.AdmissionFee) ||
		math.IsInf(program.AdmissionFee, 0) ||
		program.AdmissionFee < 0 {
		return errors.New("admission fee cannot be negative")
	}

	if math.IsNaN(program.MonthlyFee) ||
		math.IsInf(program.MonthlyFee, 0) ||
		program.MonthlyFee < 0 {
		return errors.New("monthly fee cannot be negative")
	}

	return nil
}

func normalizeTrainingActivity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "&", " and ")

	var result strings.Builder
	previousSeparator := false

	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			result.WriteRune(character)
			previousSeparator = false

		case character >= '0' && character <= '9':
			result.WriteRune(character)
			previousSeparator = false

		case !previousSeparator:
			result.WriteRune('_')
			previousSeparator = true
		}
	}

	return strings.Trim(
		result.String(),
		"_",
	)
}

func parseNonNegativeFloat(value string) (float64, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return 0, nil
	}

	number, err := strconv.ParseFloat(value, 64)
	if err != nil ||
		math.IsNaN(number) ||
		math.IsInf(number, 0) ||
		number < 0 {
		return 0, errors.New("invalid non-negative number")
	}

	return number, nil
}

func parsePositiveInt64(value string) (int64, error) {
	number, err := strconv.ParseInt(
		strings.TrimSpace(value),
		10,
		64,
	)
	if err != nil || number <= 0 {
		return 0, errors.New("invalid positive integer")
	}

	return number, nil
}

func legacyPracticeTypeForTrainingFormat(trainingFormat string) string {
	switch trainingFormat {
	case "one_to_one":
		return "one_to_one_practice"
	case "group":
		return "group_practice"
	default:
		return ""
	}
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())

	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "is not unique")
}
