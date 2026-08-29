package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) salaryProfileManagementHandler(
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

	staff, err := a.listPayrollEligibleUsersVisibleTo(user)
	if err != nil {
		log.Printf("list salary profile staff: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	profiles, err := a.listStaffSalaryProfiles()
	if err != nil {
		log.Printf("list salary profiles: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	divisions, err := a.listDivisions(false)
	if err != nil {
		log.Printf("list salary profile divisions: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	programmes, err := a.listTrainingPrograms(false)
	if err != nil {
		log.Printf("list salary profile programmes: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)

	data.Title = "Salary Profiles"
	data.Description =
		"Configure staff compensation rates and payroll calculation rules."

	data.SalaryProfiles = profiles
	data.Users = staff
	data.Divisions = divisions
	data.TrainingPrograms = programmes

	a.render(
		w,
		"salary-profiles",
		data,
		http.StatusOK,
	)
}

func (a *App) createSalaryProfileHandler(
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

	if err := r.ParseForm(); err != nil {
		a.setFlash(
			w,
			"Unable to read salary profile form.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	userID, _ := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("user_id")),
		10,
		64,
	)

	divisionID, _ := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("division_id")),
		10,
		64,
	)

	trainingProgramID, _ := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("training_program_id"),
		),
		10,
		64,
	)

	rate, err := strconv.ParseFloat(
		strings.TrimSpace(r.FormValue("rate")),
		64,
	)

	if err != nil {
		a.setFlash(
			w,
			"Salary rate must be a valid number.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	profile := StaffSalaryProfile{
		UserID:            userID,
		DivisionID:        divisionID,
		TrainingProgramID: trainingProgramID,

		CompensationType: r.FormValue("compensation_type"),

		Rate: rate,

		StudentBasis: r.FormValue("student_basis"),

		EffectiveFrom: r.FormValue("effective_from"),

		EffectiveTo: r.FormValue("effective_to"),

		Active: r.FormValue("active") == "1",

		Notes: r.FormValue("notes"),
	}

	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	selectedUser, err := a.findUserByID(profile.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(
				w,
				"Selected staff member was not found.",
			)
		} else {
			log.Printf("find salary profile staff %d: %v", profile.UserID, err)
			a.setFlash(
				w,
				"Unable to validate the selected staff member.",
			)
		}

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	if !payrollEligibleUser(*selectedUser) {
		a.setFlash(
			w,
			"Selected account is not eligible for staff payroll.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	if user != nil &&
		!canViewAllDivisions(user) &&
		len(user.DivisionIDs) > 0 &&
		!divisionSlicesOverlap(user.DivisionIDs, selectedUser.DivisionIDs) {
		a.setFlash(
			w,
			"You do not have access to create salary profiles for that staff member.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	_, err = a.createStaffSalaryProfile(
		profile,
		actorUserID,
	)
	if err != nil {
		log.Printf("create salary profile: %v", err)

		a.setFlash(
			w,
			err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Salary profile created.",
	)

	http.Redirect(
		w,
		r,
		"/admin/staff/salary-profiles",
		http.StatusSeeOther,
	)
}

func (a *App) toggleSalaryProfileHandler(
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

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusBadRequest,
		)
		return
	}

	profileID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("id")),
		10,
		64,
	)

	if err != nil || profileID <= 0 {
		http.Error(
			w,
			"invalid salary profile",
			http.StatusBadRequest,
		)
		return
	}

	profile, err :=
		a.findStaffSalaryProfileByID(profileID)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}

		log.Printf(
			"find salary profile for toggle: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	actorUserID := int64(0)
	if user != nil {
		actorUserID = user.ID
	}

	_, err = a.execDB(
		`
		UPDATE staff_salary_profiles
		SET
			active = ?,
			updated_by_user_id = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		`,
		boolToInt(!profile.Active),
		nullIfZero(actorUserID),
		profileID,
	)
	if err != nil {
		log.Printf("toggle salary profile: %v", err)

		a.setFlash(
			w,
			"Unable to update salary profile.",
		)

		http.Redirect(
			w,
			r,
			"/admin/staff/salary-profiles",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Salary profile updated.",
	)

	http.Redirect(
		w,
		r,
		"/admin/staff/salary-profiles",
		http.StatusSeeOther,
	)
}
