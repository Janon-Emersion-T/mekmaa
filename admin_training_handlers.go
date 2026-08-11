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
	coaches, err := a.listCoachUsersDetailed(true)
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
	}

	records, err := a.listCoachAttendanceRecords(selectedDate)
	if err != nil {
		log.Printf("list coach attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Coach Management"
	data.Description = "Manage coaches and coach attendance."
	data.Coaches = coaches
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
	admissions, totalAdmissions, err := a.listAdmissionsFiltered(filter)
	if err != nil {
		log.Printf("list admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingPrograms, err := a.listTrainingPrograms(true)
	if err != nil {
		log.Printf("list training programmes for admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Admissions Management"
	data.Description = "Manage admissions."
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
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.AdmissionMode = mode
	}
	if data.AdmissionMode == "view" || data.AdmissionMode == "edit" {
		admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && admissionID > 0 {
			selectedAdmission, err := a.findAdmissionByID(admissionID)
			if err == nil {
				data.SelectedAdmission = selectedAdmission
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

	admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}

	admission, err := a.findAdmissionByID(admissionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "student not found", http.StatusNotFound)
			return
		}
		log.Printf("find admission for student id card: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, nil)
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
	enrollments, err := a.listStudentEnrollments()
	if err != nil {
		log.Printf("list student enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions for enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingPrograms, err := a.listTrainingPrograms(false)
	if err != nil {
		log.Printf("list training programmes for enrollments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Enrollment Manager"
	data.Description = "Assign students to training programmes and collect programme-level fees."
	data.Enrollments = enrollments
	data.Admissions = admissions
	data.TrainingPrograms = trainingPrograms
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

	enrollment := StudentEnrollment{
		AdmissionID:         admissionID,
		TrainingProgramID:   trainingProgramID,
		TrainingProgramName: trainingProgram.Name,
		FreeAdmission:       r.FormValue("free_admission") == "true",
		FreeMonthlyFee:      r.FormValue("free_monthly_fee") == "true",
	}
	if err := validateEnrollment(enrollment); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	collectPayment := r.FormValue("payment_collected") == "true" && !enrollment.FreeAdmission
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	_, financeTransactionID, err := a.createStudentEnrollmentWithOptionalPayment(enrollment, collectPayment, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrAdmissionFeeNotConfigured) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if isUniqueConstraintError(err) {
			http.Error(w, "this student is already enrolled in the selected training programme", http.StatusConflict)
			return
		}
		log.Printf("create student enrollment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if collectPayment && financeTransactionID > 0 {
		http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(financeTransactionID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Enrollment created.")
	http.Redirect(w, r, "/admin/enrollments", http.StatusSeeOther)
}

func (a *App) collectEnrollmentAdmissionPaymentHandler(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "invalid enrollment id", http.StatusBadRequest)
		return
	}
	enrollment, err := a.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "enrollment not found", http.StatusNotFound)
			return
		}
		log.Printf("find enrollment for fee collection: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	tx, err := a.db.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	transactionID, err := a.collectEnrollmentAdmissionPaymentTx(tx, *enrollment, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrAdmissionFeeNotConfigured) {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
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

	program, err := trainingProgramFromRequest(r)
	if err != nil {
		a.setFlash(w, "Training programme could not be created: "+err.Error())
		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=new",
			http.StatusSeeOther,
		)
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
			"/admin/training-programs?action=new",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme created successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs?action=view&id="+
			strconv.FormatInt(programID, 10),
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

	program, err := trainingProgramFromRequest(r)
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

func trainingProgramFromRequest(
	r *http.Request,
) (TrainingProgram, error) {
	name := strings.TrimSpace(r.FormValue("name"))
	activity := normalizeTrainingActivity(
		r.FormValue("activity"),
	)
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
		Name:           name,
		Activity:       activity,
		TrainingFormat: trainingFormat,
		AdmissionFee:   admissionFee,
		MonthlyFee:     monthlyFee,
		Active:         r.FormValue("active") == "on",
		SortOrder:      sortOrder,
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
