package main

import (
	"log"
	"net/http"
)

func (a *App) staffDirectoryHandler(
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

	divisionIDs, ok := a.studentGroupDivisionScope(
		w,
		r,
		user,
	)
	if !ok {
		return
	}

	staff, err := a.listAssignableGroupStaffByDivisionIDs(
		divisionIDs,
	)
	if err != nil {
		log.Printf("list staff directory users: %v", err)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if err := a.hydrateStaffDirectoryUserDivisions(staff); err != nil {
		log.Printf(
			"hydrate staff directory divisions: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	groups, err := a.listStudentGroupsByDivisionIDs(
		divisionIDs,
	)
	if err != nil {
		log.Printf("list staff directory groups: %v", err)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)

	data.Title = "Staff Directory"
	data.Description =
		"Review division staff and their operational assignments."

	data.StaffDirectoryRows = buildStaffDirectoryRows(
		staff,
		groups,
	)

	a.render(
		w,
		"staff-directory",
		data,
		http.StatusOK,
	)
}
