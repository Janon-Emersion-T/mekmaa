package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) studentGroupManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	groups, err := a.listStudentGroups()
	if err != nil {
		log.Printf("list student groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions for groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	coaches, err := a.listCoachUsers()
	if err != nil {
		log.Printf("list coach users for student groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	trainingPrograms, err := a.listTrainingPrograms(false)
	if err != nil {
		log.Printf("list training programmes for student groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Groups"
	data.Description = "Manage student groups."
	data.StudentGroups = groups
	data.Admissions = admissions
	data.AvailableCoaches = coaches
	data.TrainingPrograms = trainingPrograms
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.GroupMode = mode
	}
	if data.GroupMode == "view" || data.GroupMode == "edit" {
		groupID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && groupID > 0 {
			selectedGroup, err := a.findStudentGroupByID(groupID)
			if err == nil {
				data.SelectedGroup = selectedGroup
			}
		}
	}
	a.render(w, "student-group-management", data, http.StatusOK)
}

func (a *App) attendanceManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())

	var (
		groups []StudentGroup
		err    error
	)

	if userHasRole(user, "coach") &&
		!userHasRole(user, "admin") &&
		!userHasRole(user, "superadmin") {
		groups, err = a.listStudentGroupsForCoach(user.ID)
	} else {
		groups, err = a.listStudentGroups()
	}

	if err != nil {
		log.Printf("list student groups for attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Attendance"
	data.Description = "Manage student attendance by group."
	data.StudentGroups = groups
	data.TodayDate = time.Now().Format("2006-01-02")
	data.AttendanceDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.AttendanceDate == "" {
		data.AttendanceDate = data.TodayDate
	} else if parsedDate, err := time.Parse("2006-01-02", data.AttendanceDate); err != nil {
		data.AttendanceDate = data.TodayDate
	} else if parsedDate.Format("2006-01-02") > data.TodayDate {
		data.AttendanceDate = data.TodayDate
	} else {
		data.AttendanceDate = parsedDate.Format("2006-01-02")
	}

	groupID, err := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("group_id")),
		10,
		64,
	)

	if err == nil && groupID > 0 {
		var selectedGroup *StudentGroup

		for i := range groups {
			if groups[i].ID == groupID {
				selectedGroup = &groups[i]
				break
			}
		}

		if selectedGroup != nil {
			data.SelectedGroup = selectedGroup
			if len(selectedGroup.Sessions) > 0 {
				data.GroupSessions = selectedGroup.Sessions
				sessionID := parseInt64Query(r.URL.Query().Get("session_id"))
				if sessionID <= 0 {
					sessionID = selectedGroup.Sessions[0].ID
				}
				for _, session := range selectedGroup.Sessions {
					if session.ID == sessionID {
						data.SelectedGroupSessionID = sessionID
						break
					}
				}
				if data.SelectedGroupSessionID == 0 {
					data.SelectedGroupSessionID = selectedGroup.Sessions[0].ID
				}
			}

			records, err := a.listAttendanceRecords(
				groupID,
				data.SelectedGroupSessionID,
				data.AttendanceDate,
			)
			if err != nil {
				log.Printf("list attendance records: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.AttendanceRecords = records
			if data.SelectedGroupSessionID > 0 {
				warnings, err := a.listAttendanceLimitWarnings(groupID, data.SelectedGroupSessionID, data.AttendanceDate, 8)
				if err != nil {
					log.Printf("list attendance warnings: %v", err)
					http.Error(
						w,
						"internal server error",
						http.StatusInternalServerError,
					)
					return
				}
				data.AttendanceLimitWarnings = warnings
			}

			recentDates, err := a.listRecentAttendanceDates(groupID, data.SelectedGroupSessionID, 8)
			if err != nil {
				log.Printf("list recent attendance dates: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.RecentDates = recentDates

			summary, err := a.getAttendanceSummary(groupID, data.SelectedGroupSessionID)
			if err != nil {
				log.Printf("get attendance summary: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.AttendanceSummary = summary
		}
	}

	a.render(w, "attendance-management", data, http.StatusOK)
}

func weekdayNameForDate(day time.Time) string {
	switch day.Weekday() {
	case time.Monday:
		return "monday"
	case time.Tuesday:
		return "tuesday"
	case time.Wednesday:
		return "wednesday"
	case time.Thursday:
		return "thursday"
	case time.Friday:
		return "friday"
	case time.Saturday:
		return "saturday"
	default:
		return "sunday"
	}
}

func (a *App) courtManagementHandler(
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

	courts, err := a.listCourts(true)
	if err != nil {
		log.Printf("list courts: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Court Manager"
	data.Description = "Manage court activities and simultaneous-use configurations."
	data.Courts = courts
	data.Games, _ = a.listGames(true)
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("court_action")))
	switch mode {
	case "new", "edit":
		data.CourtMode = mode
	}

	courtID, err := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("court_id")),
		10,
		64,
	)
	if err != nil || courtID <= 0 {
		if len(courts) > 0 {
			courtID = courts[0].ID
		}
	}

	if courtID > 0 {
		selectedCourt, err := a.findCourtByID(courtID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				log.Printf("find court: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}
		} else {
			data.SelectedCourt = selectedCourt
			data.CourtActivities = selectedCourt.Activities
			data.CourtLayouts = selectedCourt.Layouts

			closures, err := a.listCourtClosures(
				selectedCourt.ID,
				true,
			)
			if err != nil {
				log.Printf(
					"list court closures: %v",
					err,
				)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.CourtClosures = closures

			mode := strings.ToLower(
				strings.TrimSpace(
					r.URL.Query().Get("action"),
				),
			)
			activityAction := strings.ToLower(
				strings.TrimSpace(
					r.URL.Query().Get("activity_action"),
				),
			)

			closureAction := strings.ToLower(
				strings.TrimSpace(
					r.URL.Query().Get("closure_action"),
				),
			)

			switch closureAction {
			case "new", "edit":
				data.CourtClosureMode = closureAction
			}

			if data.CourtClosureMode == "edit" {
				closureID, err := strconv.ParseInt(
					strings.TrimSpace(
						r.URL.Query().Get("closure_id"),
					),
					10,
					64,
				)

				if err == nil && closureID > 0 {
					closure, err :=
						a.findCourtClosureByID(
							closureID,
						)

					if err == nil &&
						closure.CourtID ==
							selectedCourt.ID {
						data.SelectedCourtClosure =
							closure
					}
				}
			}

			switch mode {
			case "new", "edit":
				data.CourtLayoutMode = mode
			}
			switch activityAction {
			case "new", "edit":
				data.CourtActivityMode = activityAction
			}
			if data.CourtActivityMode == "edit" {
				activityID, err := strconv.ParseInt(
					strings.TrimSpace(
						r.URL.Query().Get("activity_id"),
					),
					10,
					64,
				)
				if err == nil && activityID > 0 {
					activity, err := a.findCourtActivityByID(
						activityID,
					)
					if err == nil &&
						activity.CourtID == selectedCourt.ID {
						data.SelectedCourtActivity = activity
					}
				}
			}

			if data.CourtLayoutMode == "edit" {
				layoutID, err := strconv.ParseInt(
					strings.TrimSpace(
						r.URL.Query().Get("layout_id"),
					),
					10,
					64,
				)

				if err == nil && layoutID > 0 {
					layout, err := a.findCourtLayoutByID(
						layoutID,
					)
					if err == nil &&
						layout.CourtID == selectedCourt.ID {
						data.SelectedCourtLayout = layout
					}
				}
			}
		}
	}

	if data.CourtMode == "edit" && data.SelectedCourt == nil {
		data.CourtMode = ""
	}
	if data.CourtActivityMode == "edit" &&
		data.SelectedCourtActivity == nil {
		data.CourtActivityMode = ""
	}

	if data.SelectedCourt != nil {
		data.BookingOptions = bookingOptionCatalog(
			data.CourtActivities,
			data.CourtLayouts,
		)
	}

	a.render(
		w,
		"court-management",
		data,
		http.StatusOK,
	)
}

func courtFromRequest(r *http.Request) (Court, error) {
	var court Court

	court.Name = strings.TrimSpace(r.FormValue("name"))
	court.Code = strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	court.Description = strings.TrimSpace(r.FormValue("description"))
	court.Active = r.FormValue("active") == "true"
	sortOrder, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	if err != nil {
		return court, errors.New("display order must be a valid number")
	}
	court.SortOrder = sortOrder

	if err := validateCourt(court); err != nil {
		return court, err
	}

	return court, nil
}

func (a *App) createCourtHandler(w http.ResponseWriter, r *http.Request) {
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

	court, err := courtFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/courts?court_action=new#court-form", http.StatusSeeOther)
		return
	}

	courtID, err := a.createCourt(court)
	if err != nil {
		log.Printf("create court: %v", err)
		if isUniqueConstraintError(err) {
			a.setFlash(w, "Court code must be unique.")
			http.Redirect(w, r, "/admin/courts?court_action=new#court-form", http.StatusSeeOther)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Court created successfully.")
	http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(courtID, 10), http.StatusSeeOther)
}

func (a *App) updateCourtHandler(w http.ResponseWriter, r *http.Request) {
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

	courtID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("court_id")), 10, 64)
	if err != nil || courtID <= 0 {
		http.Error(w, "invalid court", http.StatusBadRequest)
		return
	}

	court, err := courtFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(courtID, 10)+"&court_action=edit#court-form", http.StatusSeeOther)
		return
	}
	court.ID = courtID

	if err := a.updateCourt(court); err != nil {
		log.Printf("update court: %v", err)
		if isUniqueConstraintError(err) {
			a.setFlash(w, "Court code must be unique.")
			http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(courtID, 10)+"&court_action=edit#court-form", http.StatusSeeOther)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Court not found.")
			http.Redirect(w, r, "/admin/courts", http.StatusSeeOther)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Court updated successfully.")
	http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(courtID, 10), http.StatusSeeOther)
}

func (a *App) createCourtLayoutHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layout, err := courtLayoutFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(r.FormValue("court_id"))+
				"&action=new#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	layoutID, err := a.createCourtLayout(layout)
	if err != nil {
		log.Printf("create court layout: %v", err)
		a.setFlash(
			w,
			"Unable to create the court layout: "+
				err.Error(),
		)
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(layout.CourtID, 10)+
				"&action=new#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Court layout created successfully.")

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(layout.CourtID, 10)+
			"&action=edit&layout_id="+
			strconv.FormatInt(layoutID, 10)+
			"#layout-form",
		http.StatusSeeOther,
	)
}

func (a *App) updateCourtLayoutHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	layout, err := courtLayoutFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(r.FormValue("court_id"))+
				"&action=edit&layout_id="+
				strconv.FormatInt(layoutID, 10)+
				"#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	layout.ID = layoutID

	if err := a.updateCourtLayout(layout); err != nil {
		log.Printf("update court layout: %v", err)
		a.setFlash(
			w,
			"Unable to update the court layout: "+
				err.Error(),
		)
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(layout.CourtID, 10)+
				"&action=edit&layout_id="+
				strconv.FormatInt(layout.ID, 10)+
				"#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Court layout updated successfully.")

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(layout.CourtID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) toggleCourtLayoutHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.toggleCourtLayout(layoutID); err != nil {
		log.Printf("toggle court layout: %v", err)
		a.setFlash(
			w,
			"Unable to update the court layout.",
		)
	} else {
		a.setFlash(
			w,
			"Court layout status updated.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(r.FormValue("court_id")),
		http.StatusSeeOther,
	)
}

func (a *App) deleteCourtLayoutHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.deleteCourtLayout(layoutID); err != nil {
		log.Printf("delete court layout: %v", err)
		a.setFlash(
			w,
			"Unable to delete the court layout: "+
				err.Error(),
		)
	} else {
		a.setFlash(
			w,
			"Court layout deleted successfully.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(r.FormValue("court_id")),
		http.StatusSeeOther,
	)
}

func (a *App) createCourtClosureHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closure, err :=
		courtClosureFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(
					r.FormValue("court_id"),
				)+
				"&closure_action=new"+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	closureID, err :=
		a.createCourtClosure(closure)
	if err != nil {
		log.Printf(
			"create court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to create the closure: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(
					closure.CourtID,
					10,
				)+
				"&closure_action=new"+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Court closure created successfully.",
	)

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(
				closure.CourtID,
				10,
			)+
			"&closure_action=edit"+
			"&closure_id="+
			strconv.FormatInt(
				closureID,
				10,
			)+
			"#closure-form",
		http.StatusSeeOther,
	)
}

func (a *App) updateCourtClosureHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	closure, err :=
		courtClosureFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(
					r.FormValue("court_id"),
				)+
				"&closure_action=edit"+
				"&closure_id="+
				strconv.FormatInt(
					closureID,
					10,
				)+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	closure.ID = closureID

	if err := a.updateCourtClosure(
		closure,
	); err != nil {
		log.Printf(
			"update court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to update the closure: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(
					closure.CourtID,
					10,
				)+
				"&closure_action=edit"+
				"&closure_id="+
				strconv.FormatInt(
					closure.ID,
					10,
				)+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Court closure updated successfully.",
	)

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(
				closure.CourtID,
				10,
			),
		http.StatusSeeOther,
	)
}

func (a *App) toggleCourtClosureHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.toggleCourtClosure(
		closureID,
	); err != nil {
		log.Printf(
			"toggle court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to update the closure.",
		)
	} else {
		a.setFlash(
			w,
			"Court closure status updated.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(
				r.FormValue("court_id"),
			),
		http.StatusSeeOther,
	)
}

func (a *App) deleteCourtClosureHandler(
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
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.deleteCourtClosure(
		closureID,
	); err != nil {
		log.Printf(
			"delete court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to delete the closure: "+
				err.Error(),
		)
	} else {
		a.setFlash(
			w,
			"Court closure deleted successfully.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(
				r.FormValue("court_id"),
			),
		http.StatusSeeOther,
	)
}

func (a *App) bookingManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.ScheduleMode = mode
	}
	if data.ScheduleMode == "new" {
		data.DraftSchedule = prefillAdminBookingDraft(r, data.CalendarDate)
	}
	if data.ScheduleMode == "view" || data.ScheduleMode == "edit" {
		scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && scheduleID > 0 {
			selectedSchedule, err := a.findSpaceScheduleByID(scheduleID)
			if err == nil {
				if data.ScheduleMode == "edit" {
					applyAdminBookingQueryDraft(r, selectedSchedule)
				}
				data.SelectedSchedule = selectedSchedule
			}
		}
	}
	a.render(w, "booking-management", data, http.StatusOK)
}

func (a *App) buildOneToOneTemplateData(w http.ResponseWriter, r *http.Request, user *User) (TemplateData, error) {
	offerings, err := a.listOneToOneOfferings(true)
	if err != nil {
		return TemplateData{}, err
	}
	referralPartners, err := a.listReferralPartners(true)
	if err != nil {
		return TemplateData{}, err
	}
	bookings, err := a.listOneToOneBookings()
	if err != nil {
		return TemplateData{}, err
	}
	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		return TemplateData{}, err
	}
	games, err := a.listGames(false)
	if err != nil {
		return TemplateData{}, err
	}
	scheduleIDs := make([]int64, 0, len(bookings))
	for _, booking := range bookings {
		scheduleIDs = append(scheduleIDs, booking.ScheduleID)
	}
	financials, err := a.listBookingFinancialsForScheduleIDs(scheduleIDs)
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "1 to 1 Scheduling"
	data.Description = "Manage 1 to 1 offerings and bookings."
	data.OneToOneOfferings = offerings
	data.OneToOneBookings = bookings
	data.ReferralPartners = referralPartners
	data.BookingFinancials = financials
	data.BookingReferrals, _ = a.listBookingReferralsForScheduleIDs(scheduleIDs)
	data.CourtActivities = courtActivities
	data.Games = games
	data.Hours = bookingHours()
	data.TodayDate = time.Now().Format("2006-01-02")
	return data, nil
}

func (a *App) gameManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	games, err := a.listGames(true)
	if err != nil {
		log.Printf("list games: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load activities for games: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Games"
	data.Description = "Manage the game list used by 1 to 1 products."
	data.Games = games
	data.CourtActivities = courtActivities

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "edit":
		data.GameMode = mode
	}
	if data.GameMode == "edit" {
		gameID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && gameID > 0 {
			selectedGame, err := a.findGameByID(gameID)
			if err == nil {
				data.SelectedGame = selectedGame
			}
		}
		if data.SelectedGame == nil {
			data.GameMode = ""
		}
	}

	a.render(w, "games-management", data, http.StatusOK)
}

func (a *App) oneToOneManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildOneToOneTemplateData(w, r, user)
	if err != nil {
		log.Printf("build 1 to 1 data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "edit":
		data.OneToOneMode = mode
	}
	if data.OneToOneMode == "edit" {
		offeringID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && offeringID > 0 {
			selected, err := a.findOneToOneOfferingByID(offeringID)
			if err == nil {
				data.SelectedOneToOneOffering = selected
			}
		}
		if data.SelectedOneToOneOffering == nil {
			data.OneToOneMode = ""
		}
	}
	a.render(w, "one-to-one-management", data, http.StatusOK)
}

func (a *App) createOneToOneOfferingHandler(w http.ResponseWriter, r *http.Request) {
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
	offering, err := oneToOneOfferingFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load activities for 1 to 1 create: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	games, err := a.listGames(false)
	if err != nil {
		log.Printf("load games for 1 to 1 create: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validateOneToOneOffering(offering, courtActivities, games); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := a.createOneToOneOffering(offering); err != nil {
		log.Printf("create 1 to 1 offering: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "1 to 1 offering created.")
	http.Redirect(w, r, "/admin/one-to-one", http.StatusSeeOther)
}

func (a *App) updateOneToOneOfferingHandler(w http.ResponseWriter, r *http.Request) {
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
	offeringID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("offering_id")), 10, 64)
	if err != nil || offeringID <= 0 {
		http.Error(w, "invalid offering id", http.StatusBadRequest)
		return
	}
	offering, err := oneToOneOfferingFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	offering.ID = offeringID
	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load activities for 1 to 1 update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	games, err := a.listGames(false)
	if err != nil {
		log.Printf("load games for 1 to 1 update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validateOneToOneOffering(offering, courtActivities, games); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateOneToOneOffering(offering); err != nil {
		log.Printf("update 1 to 1 offering: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "1 to 1 offering updated.")
	http.Redirect(w, r, "/admin/one-to-one", http.StatusSeeOther)
}

func (a *App) deleteOneToOneOfferingHandler(w http.ResponseWriter, r *http.Request) {
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
	offeringID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("offering_id")), 10, 64)
	if err != nil || offeringID <= 0 {
		http.Error(w, "invalid offering id", http.StatusBadRequest)
		return
	}
	if err := a.deleteOneToOneOffering(offeringID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setFlash(w, "1 to 1 offering deleted.")
	http.Redirect(w, r, "/admin/one-to-one", http.StatusSeeOther)
}

func (a *App) createGameHandler(w http.ResponseWriter, r *http.Request) {
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

	game, err := gameFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	game.Activity = normalizeGameActivitySlug(game.Name)
	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load activities for game create: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validateGame(game, courtActivities); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := a.createGame(game); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "that game name or linked activity already exists", http.StatusConflict)
			return
		}
		log.Printf("create game: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Game created.")
	http.Redirect(w, r, "/admin/games", http.StatusSeeOther)
}

func (a *App) updateGameHandler(w http.ResponseWriter, r *http.Request) {
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

	gameID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("game_id")), 10, 64)
	if err != nil || gameID <= 0 {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}
	game, err := gameFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	game.ID = gameID
	existingGame, err := a.findGameByID(gameID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	game.Activity = existingGame.Activity

	courtActivities, _, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load activities for game update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validateGame(game, courtActivities); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateGame(game); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "that game name or linked activity already exists", http.StatusConflict)
			return
		}
		log.Printf("update game: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Game updated.")
	http.Redirect(w, r, "/admin/games", http.StatusSeeOther)
}

func (a *App) deleteGameHandler(w http.ResponseWriter, r *http.Request) {
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

	gameID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("game_id")), 10, 64)
	if err != nil || gameID <= 0 {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}
	if err := a.deleteGame(gameID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Game deleted.")
	http.Redirect(w, r, "/admin/games", http.StatusSeeOther)
}

func (a *App) oneToOneBookingManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildOneToOneTemplateData(w, r, user)
	if err != nil {
		log.Printf("build 1 to 1 booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "1 to 1 Bookings"
	data.Description = "Book and monitor 1 to 1 sessions."
	a.render(w, "one-to-one-bookings", data, http.StatusOK)
}

func (a *App) createOneToOneBookingHandler(w http.ResponseWriter, r *http.Request) {
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
	offeringID, customerName, slotDate, slotHour, sessions, discountedPrice, coachFee, notes, referralCode, err := oneToOneBookingFormValues(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if customerName == "" {
		http.Error(w, "customer name is required", http.StatusBadRequest)
		return
	}
	offering, err := a.findOneToOneOfferingByID(offeringID)
	if err != nil {
		http.Error(w, "selected 1 to 1 setup was not found", http.StatusBadRequest)
		return
	}
	if !offering.Active {
		http.Error(w, "selected 1 to 1 setup is inactive", http.StatusBadRequest)
		return
	}
	if offering.Occurrence == "per_day" {
		sessions = 1
	}
	if sessions > offering.SessionCount {
		http.Error(w, fmt.Sprintf("sessions cannot exceed the configured limit of %d", offering.SessionCount), http.StatusBadRequest)
		return
	}
	schedule := SpaceSchedule{
		SlotDate:  slotDate,
		SlotHour:  slotHour,
		EntryType: "booking",
		Activity:  offering.Game,
		Quantity:  1,
		Title:     customerName,
	}
	if err := validateSpaceScheduleInput(schedule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, _, err := a.createOneToOneBooking(*offering, customerName, slotDate, slotHour, sessions, discountedPrice, coachFee, notes, referralCode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setFlash(w, "1 to 1 booking created.")
	http.Redirect(w, r, "/admin/one-to-one-bookings", http.StatusSeeOther)
}

func (a *App) deleteOneToOneBookingHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid booking id", http.StatusBadRequest)
		return
	}
	if err := a.deleteSpaceSchedule(scheduleID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setFlash(w, "1 to 1 booking deleted.")
	http.Redirect(w, r, "/admin/one-to-one-bookings", http.StatusSeeOther)
}

func (a *App) adminBookingOptionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slotDate := strings.TrimSpace(r.URL.Query().Get("slot_date"))
	slotHour := strings.TrimSpace(r.URL.Query().Get("slot_hour"))
	entryType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("entry_type")))
	if entryType == "" {
		entryType = "booking"
	}

	candidate := SpaceSchedule{
		EntryType: entryType,
		SlotDate:  slotDate,
		SlotHour:  slotHour,
	}

	scheduleID, _ := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("schedule_id")),
		10,
		64,
	)

	if _, err := time.Parse("2006-01-02", slotDate); err != nil {
		http.Error(w, "invalid slot date", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("15:04", slotHour); err != nil {
		http.Error(w, "invalid slot hour", http.StatusBadRequest)
		return
	}
	if entryType != "booking" && entryType != "training" {
		http.Error(w, "invalid entry type", http.StatusBadRequest)
		return
	}

	options, blockedReason, err := a.adminBookingOptionsForSchedule(candidate, scheduleID)
	if err != nil {
		log.Printf("build admin booking options: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"options":        options,
		"blocked_reason": blockedReason,
	}); err != nil {
		log.Printf("encode admin booking options: %v", err)
	}
}

func (a *App) bookingRequestsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "Booking Requests"
	data.Description = "Review unresolved booking requests."
	data.BookingRequestFilterStatus = normalizeBookingRequestFilterStatus(r.URL.Query().Get("status"))
	data.BookingRequestSearch = strings.TrimSpace(r.URL.Query().Get("q"))
	data.BookingRequests = filterBookingRequests(data.BookingRequests, data.BookingRequestFilterStatus, data.BookingRequestSearch)
	data.BookingCommunications, err = a.listBookingCommunicationsForScheduleIDs(scheduleIDs(data.BookingRequests))
	if err != nil {
		log.Printf("list booking communications: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("action")), "reschedule") {
		if requestID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64); err == nil && requestID > 0 {
			if selectedRequest, err := a.findSpaceScheduleByID(requestID); err == nil &&
				unresolvedBookingRequestStatus(selectedRequest.Status) &&
				selectedRequest.EntryType == "booking" &&
				(selectedRequest.RequesterName != "" || selectedRequest.RequesterEmail != "" || selectedRequest.RequestedByUser > 0) {
				draft := *selectedRequest
				applyAdminBookingQueryDraft(r, &draft)
				draft.ReviewNote = strings.TrimSpace(r.URL.Query().Get("review_note"))
				data.SelectedSchedule = selectedRequest
				data.DraftSchedule = &draft

				options, blockedReason, optionErr := a.adminBookingOptionsForSchedule(draft, selectedRequest.ID)
				if optionErr != nil {
					log.Printf("build booking request reschedule options: %v", optionErr)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}

				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		}
	}

	a.render(w, "booking-requests", data, http.StatusOK)
}

func (a *App) resendBookingCommunicationHandler(w http.ResponseWriter, r *http.Request) {
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

	currentUser, _ := a.currentUser(r.Context())
	if currentUser == nil || (!containsPermission(currentUser.Permissions, "space_bookings.manage") && !containsPermission(currentUser.Permissions, "booking_requests.manage")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}

	communications, err := a.listBookingCommunicationsForScheduleIDs([]int64{scheduleID})
	if err != nil {
		log.Printf("list booking communications for resend: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	relatedEventType := latestResendableEventType(schedule, communications)
	if relatedEventType == "" {
		a.setFlash(w, "No customer communication template is available for this booking state.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
		return
	}

	eventKey := fmt.Sprintf("schedule:%d:%s:%s:%d", scheduleID, bookingCommEventResent, relatedEventType, time.Now().UTC().UnixNano())
	results, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventResent, relatedEventType, eventKey, currentUser.ID)
	if commErr != nil {
		log.Printf("resend booking communication: %v", commErr)
		a.setFlash(w, "The communication resend could not be prepared automatically.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
		return
	}

	a.setFlash(w, communicationFlashMessage("Customer communication resent.", results))
	http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) rotateBookingAccessHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	rawToken, err := a.rotateBookingAccessToken(scheduleID, "status")
	if err != nil {
		log.Printf("rotate booking access token: %v", err)
		a.setFlash(w, "Unable to rotate the customer status link.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Customer status link rotated. One-time URL: "+a.bookingTrackingURL(rawToken))
	http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) revokeBookingAccessHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	if err := a.revokeBookingAccessToken(scheduleID, "status"); err != nil {
		log.Printf("revoke booking access token: %v", err)
		a.setFlash(w, "Unable to revoke the customer status link.")
	} else {
		a.setFlash(w, "Customer status link revoked.")
	}
	http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) cancelBookingHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("cancellation_reason"))
	financeNote := strings.TrimSpace(r.FormValue("cancellation_finance_note"))
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	updated, changeID, err := a.transitionManagedBookingStatus(scheduleID, bookingStatusCancelled, reason, customerMessage, reason, financeNote, "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be cancelled: "+err.Error())
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, bookingStatusConfirmed, ""), http.StatusSeeOther)
		return
	}
	requests, _ := a.listBookingCancellationRequestsForScheduleIDs([]int64{scheduleID})
	if pending := pendingCancellationRequestFor(requests, scheduleID); pending != nil {
		_, _ = a.db.Exec(`
			UPDATE booking_cancellation_requests
			SET status = 'approved', review_note = ?, reviewed_at = ?, reviewed_by_user_id = ?
			WHERE id = ? AND status = 'pending'
		`, reason, time.Now().UTC(), nullIfZero(currentUserID(r)), pending.ID)
	}
	_, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventCancelledByAdmin, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventCancelledByAdmin, changeID), currentUserID(r))
	if commErr != nil {
		log.Printf("send admin cancellation communication: %v", commErr)
	}
	a.setFlash(w, "Booking cancelled.")
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) completeBookingHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	updated, changeID, err := a.transitionManagedBookingStatus(scheduleID, bookingStatusCompleted, "", customerMessage, "", "", "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be marked completed: "+err.Error())
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, bookingStatusConfirmed, ""), http.StatusSeeOther)
		return
	}
	_, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventCompleted, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventCompleted, changeID), currentUserID(r))
	if commErr != nil {
		log.Printf("send booking completed communication: %v", commErr)
	}
	a.setFlash(w, "Booking marked completed.")
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) noShowBookingHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	updated, changeID, err := a.transitionManagedBookingStatus(scheduleID, bookingStatusNoShow, "", customerMessage, "", "", "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be marked no-show: "+err.Error())
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, bookingStatusConfirmed, ""), http.StatusSeeOther)
		return
	}
	_, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventNoShow, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventNoShow, changeID), currentUserID(r))
	if commErr != nil {
		log.Printf("send booking no-show communication: %v", commErr)
		a.setFlash(w, "Booking marked no-show, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Booking marked no-show.")
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) holdBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
	redirectTo := safeReturnPath(r.FormValue("return_to"), "/admin/booking-requests")
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	if customerMessage == "" {
		a.setFlash(w, "Add the customer-facing hold message that should appear on the booking status page.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	updated, changeID, err := a.transitionBookingRequestStatus(scheduleID, bookingStatusHeld, reviewNote, customerMessage, "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be placed on hold: "+err.Error())
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	communications, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventHeld, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventHeld, changeID), currentUserID(r))
	if commErr != nil {
		log.Printf("send booking hold communication: %v", commErr)
		a.setFlash(w, "Booking request placed on hold, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	a.setFlash(w, communicationFlashMessage("Booking request placed on hold.", communications))
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) approveBookingCancellationRequestHandler(w http.ResponseWriter, r *http.Request) {
	redirectTo := safeReturnPath(r.FormValue("return_to"), "/admin/bookings")
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
	requestID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("request_id")), 10, 64)
	if err != nil || requestID <= 0 {
		a.setFlash(w, "Select a valid cancellation request.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	row := a.db.QueryRow(`SELECT schedule_id, request_reason, status FROM booking_cancellation_requests WHERE id = ?`, requestID)
	var scheduleID int64
	var reason string
	var status string
	if err := row.Scan(&scheduleID, &reason, &status); err != nil {
		a.setFlash(w, "Cancellation request not found.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	if status != bookingStatusPending {
		a.setFlash(w, "Cancellation request is no longer pending.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	if customerMessage == "" {
		a.setFlash(w, "Add the customer-facing approval message before approving the cancellation.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	financeNote := strings.TrimSpace(r.FormValue("cancellation_finance_note"))
	updated, changeID, err := a.transitionManagedBookingStatus(scheduleID, bookingStatusCancelled, reason, customerMessage, reason, financeNote, "customer_request_approved", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Cancellation request could not be approved: "+err.Error())
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, bookingStatusConfirmed, ""), http.StatusSeeOther)
		return
	}
	if _, err := a.db.Exec(`
		UPDATE booking_cancellation_requests
		SET status = 'approved', review_note = ?, reviewed_at = ?, reviewed_by_user_id = ?
		WHERE id = ? AND status = 'pending'
	`, strings.TrimSpace(r.FormValue("review_note")), time.Now().UTC(), nullIfZero(currentUserID(r)), requestID); err != nil {
		log.Printf("approve cancellation request row: %v", err)
	}
	_, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventCancellationApproved, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventCancellationApproved, changeID), currentUserID(r))
	if commErr != nil {
		log.Printf("send cancellation approved communication: %v", commErr)
	}
	a.setFlash(w, "Cancellation request approved.")
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) rejectBookingCancellationRequestHandler(w http.ResponseWriter, r *http.Request) {
	redirectTo := safeReturnPath(r.FormValue("return_to"), "/admin/bookings")
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
	requestID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("request_id")), 10, 64)
	if err != nil || requestID <= 0 {
		a.setFlash(w, "Select a valid cancellation request.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	row := a.db.QueryRow(`SELECT schedule_id, request_reason, status FROM booking_cancellation_requests WHERE id = ?`, requestID)
	var scheduleID int64
	var reason string
	var status string
	if err := row.Scan(&scheduleID, &reason, &status); err != nil {
		a.setFlash(w, "Cancellation request not found.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	if status != bookingStatusPending {
		a.setFlash(w, "Cancellation request is no longer pending.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	if reviewNote == "" {
		a.setFlash(w, "Add the internal rejection note before rejecting the cancellation request.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	if customerMessage == "" {
		a.setFlash(w, "Add the customer-facing rejection message before rejecting the cancellation request.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	if _, err := a.db.Exec(`
		UPDATE booking_cancellation_requests
		SET status = 'rejected', review_note = ?, reviewed_at = ?, reviewed_by_user_id = ?
		WHERE id = ? AND status = 'pending'
	`, reviewNote, time.Now().UTC(), nullIfZero(currentUserID(r)), requestID); err != nil {
		log.Printf("reject cancellation request row: %v", err)
		a.setFlash(w, "Cancellation request could not be rejected right now.")
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
		return
	}
	schedule, _ := a.findSpaceScheduleByID(scheduleID)
	if schedule != nil {
		financial := bookingFinancialForSchedule(mustListBookingFinancialsTxMust(a.db, scheduleID), scheduleID)
		if financial != nil {
			schedule.QuotedPrice = financial.QuotedAmount
		}
		if _, err := a.recordBookingLifecycleChange(schedule, "cancellation_request_rejected", schedule.Status, schedule.Status, reviewNote, customerMessage, "", "admin", currentUserID(r)); err != nil {
			log.Printf("record cancellation rejection history: %v", err)
		}
	}
	_, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventCancellationRejected, "", fmt.Sprintf("schedule:%d:%s:req:%d", scheduleID, bookingCommEventCancellationRejected, requestID), currentUserID(r))
	if commErr != nil {
		log.Printf("send cancellation rejected communication: %v", commErr)
	}
	a.setFlash(w, "Cancellation request rejected.")
	http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, bookingStatusConfirmed, ""), http.StatusSeeOther)
}

func (a *App) rescheduleBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	changedByUserID := int64(0)
	if currentUser != nil {
		changedByUserID = currentUser.ID
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	schedule.EntryType = "booking"
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))

	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, strings.TrimSpace(r.FormValue("customer_message")), err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, strings.TrimSpace(r.FormValue("customer_message")), err.Error(), http.StatusBadRequest)
		return
	}

	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	result, err := a.rescheduleBookingRequest(scheduleID, schedule, reviewNote, customerMessage, "rescheduled", false, changedByUserID)
	if err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, customerMessage, err.Error(), http.StatusBadRequest)
		return
	}

	flashMessage := "Pending booking request updated with the proposed slot."
	if result != nil && result.ChangeID > 0 {
		communications, commErr := a.sendBookingCommunicationEvent(
			scheduleID,
			bookingCommEventRescheduledPending,
			"",
			fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventRescheduledPending, result.ChangeID),
			changedByUserID,
		)
		if commErr != nil {
			log.Printf("send pending reschedule communication: %v", commErr)
			flashMessage = "Pending booking request updated, but the customer communication could not be prepared automatically."
		} else if !communicationDelivered(communications, bookingCommChannelEmail) {
			flashMessage = "Pending booking request updated, but email delivery failed or is not configured."
		}
	}
	a.setFlash(w, flashMessage)
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
}

func (a *App) rescheduleAndConfirmBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	changedByUserID := int64(0)
	if currentUser != nil {
		changedByUserID = currentUser.ID
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	schedule.EntryType = "booking"
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))

	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, strings.TrimSpace(r.FormValue("customer_message")), err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, strings.TrimSpace(r.FormValue("customer_message")), err.Error(), http.StatusBadRequest)
		return
	}

	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	result, err := a.rescheduleBookingRequest(scheduleID, schedule, reviewNote, customerMessage, "rescheduled_confirmed", true, changedByUserID)
	if err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, customerMessage, err.Error(), http.StatusBadRequest)
		return
	}

	eventType := bookingCommEventConfirmed
	eventKey := fmt.Sprintf("schedule:%d:%s", scheduleID, bookingCommEventConfirmed)
	if result != nil && result.ChangeID > 0 {
		eventType = bookingCommEventRescheduledConfirmed
		eventKey = fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventRescheduledConfirmed, result.ChangeID)
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		eventType,
		"",
		eventKey,
		changedByUserID,
	)
	if commErr != nil {
		log.Printf("send reschedule confirm communication: %v", commErr)
		a.setFlash(w, "Booking request was rescheduled and confirmed, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	a.setFlash(w, communicationFlashMessage("Booking request rescheduled and confirmed.", communications))
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
}

func (a *App) pricingManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	pricings, err := a.listPricingRules()
	if err != nil {
		log.Printf("list pricing rules: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		log.Printf("get pricing settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "Booking Pricing"
	data.Description = "Manage booking pricing."
	data.Pricings = pricings
	data.PricingSettings = settings
	data.Games, _ = a.listGames(true)
	data.SetupWarnings = a.setupWarningsForUser(user)
	activities, layouts, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load active booking configuration: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.CourtActivities = activities
	data.CourtLayouts = layouts
	data.BookingOptions = bookingOptionCatalog(activities, layouts)
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.PricingMode = mode
	}
	if data.PricingMode == "view" || data.PricingMode == "edit" {
		pricingID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && pricingID > 0 {
			selectedPricing, err := a.findPricingRuleByID(pricingID)
			if err == nil {
				data.SelectedPricing = selectedPricing
			}
		}
	}
	a.render(w, "pricing-management", data, http.StatusOK)
}

func (a *App) eventManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	events, err := a.listEvents()
	if err != nil {
		log.Printf("list events: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Events"
	data.Description = "Manage public events."
	data.Events = events
	data.Games, _ = a.listGames(true)
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.EventMode = mode
	}
	if data.EventMode == "view" || data.EventMode == "edit" {
		eventID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && eventID > 0 {
			selectedEvent, err := a.findEventByID(eventID)
			if err == nil {
				data.SelectedEvent = selectedEvent
			}
		}
	}
	a.render(w, "events-management", data, http.StatusOK)
}

func (a *App) updatePricingSettingsHandler(w http.ResponseWriter, r *http.Request) {
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

	settings := PricingSettings{
		PeakStartHour: strings.TrimSpace(r.FormValue("peak_start_hour")),
		PeakEndHour:   strings.TrimSpace(r.FormValue("peak_end_hour")),
	}
	if err := validatePricingSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updatePricingSettings(settings); err != nil {
		log.Printf("update pricing settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing settings updated.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) updateReferralSettingsHandler(w http.ResponseWriter, r *http.Request) {
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
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("referral_commission_amount")), 64)
	if err != nil || amount < 0 {
		http.Error(w, "a valid referral commission amount is required", http.StatusBadRequest)
		return
	}
	if err := a.updateReferralCommissionAmount(amount); err != nil {
		log.Printf("update referral commission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral commission updated.")
	http.Redirect(w, r, "/admin/referrals#programme-settings", http.StatusSeeOther)
}

func (a *App) createReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partner := ReferralPartner{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Code:   strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Email:  strings.ToLower(strings.TrimSpace(r.FormValue("email"))),
		Phone:  strings.TrimSpace(r.FormValue("phone")),
		Active: true,
	}
	if err := validateReferralPartner(partner); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createReferralPartner(partner); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "that referral code is already in use", http.StatusConflict)
			return
		}
		log.Printf("create referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner created.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) updateReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partnerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("partner_id")), 10, 64)
	if err != nil || partnerID <= 0 {
		http.Error(w, "invalid referral partner", http.StatusBadRequest)
		return
	}
	partner := ReferralPartner{
		ID:    partnerID,
		Name:  strings.TrimSpace(r.FormValue("name")),
		Code:  strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Email: strings.ToLower(strings.TrimSpace(r.FormValue("email"))),
		Phone: strings.TrimSpace(r.FormValue("phone")),
	}
	if err := validateReferralPartner(partner); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateReferralPartner(partner); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "that referral code is already in use", http.StatusConflict)
			return
		}
		log.Printf("update referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner updated.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) toggleReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partnerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("partner_id")), 10, 64)
	if err != nil || partnerID <= 0 {
		http.Error(w, "invalid referral partner", http.StatusBadRequest)
		return
	}
	if err := a.toggleReferralPartner(partnerID); err != nil {
		log.Printf("toggle referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner status updated.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) payReferralCommissionHandler(w http.ResponseWriter, r *http.Request) {
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
	referralID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("referral_id")), 10, 64)
	if err != nil || referralID <= 0 {
		http.Error(w, "invalid referral commission", http.StatusBadRequest)
		return
	}
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	if paymentMethod != "cash" && paymentMethod != "bank_transfer" {
		http.Error(w, "invalid payment method", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.payReferralCommission(referralID, paymentMethod, recordedBy)
	if err != nil {
		a.setFlash(w, "Commission could not be paid: "+err.Error())
		http.Redirect(w, r, "/admin/referrals", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) confirmBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	updated, changeID, err := a.transitionBookingRequestStatus(scheduleID, bookingStatusConfirmed, "", customerMessage, "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be confirmed and remains pending: "+err.Error())
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		bookingCommEventConfirmed,
		"",
		fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventConfirmed, changeID),
		currentUserID(r),
	)
	if commErr != nil {
		log.Printf("send booking confirmation communication: %v", commErr)
		a.setFlash(w, "Booking request confirmed, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
		return
	}
	a.setFlash(w, communicationFlashMessage("Booking request confirmed.", communications))
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) rejectBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))
	customerMessage := strings.TrimSpace(r.FormValue("customer_message"))
	if reviewNote == "" {
		a.setFlash(w, "Add a clear rejection reason before closing the request.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	if customerMessage == "" {
		a.setFlash(w, "Add the customer-facing message that should appear on the booking status page.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	updated, changeID, err := a.transitionBookingRequestStatus(scheduleID, bookingStatusRejected, reviewNote, customerMessage, "admin", currentUserID(r))
	if err != nil {
		a.setFlash(w, "Booking could not be rejected: "+err.Error())
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		bookingCommEventRejected,
		"",
		fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventRejected, changeID),
		currentUserID(r),
	)
	if commErr != nil {
		log.Printf("send booking rejection communication: %v", commErr)
		a.setFlash(w, "Booking request rejected, but the rejection email could not be prepared automatically.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
		return
	}
	if communicationDelivered(communications, bookingCommChannelEmail) {
		a.setFlash(w, "Booking request rejected and the customer was notified by email.")
	} else {
		a.setFlash(w, "Booking request rejected, but the rejection email failed or is not configured.")
	}
	http.Redirect(w, r, adminBookingCommunicationRedirect(updated.ID, updated.Status, updated.SlotDate), http.StatusSeeOther)
}

func (a *App) createPricingHandler(w http.ResponseWriter, r *http.Request) {
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

	pricing, err := pricingRuleFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	game, err := a.findGameByID(pricing.GameID)
	if err != nil {
		http.Error(w, "selected game was not found", http.StatusBadRequest)
		return
	}
	pricing.Activity = game.Activity
	if err := validatePricingRule(pricing); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	activities, layouts, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load active booking configuration: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validatePricingRuleAgainstOptions(
		pricing,
		activities,
		layouts,
	); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createPricingRule(pricing); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "pricing already exists for that option", http.StatusConflict)
			return
		}
		log.Printf("create pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing created.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) updatePricingHandler(w http.ResponseWriter, r *http.Request) {
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

	pricingID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("pricing_id")), 10, 64)
	if err != nil || pricingID <= 0 {
		http.Error(w, "invalid pricing id", http.StatusBadRequest)
		return
	}
	pricing, err := pricingRuleFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pricing.ID = pricingID
	game, err := a.findGameByID(pricing.GameID)
	if err != nil {
		http.Error(w, "selected game was not found", http.StatusBadRequest)
		return
	}
	pricing.Activity = game.Activity
	if err := validatePricingRule(pricing); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	activities, layouts, err := a.activeBookingConfiguration()
	if err != nil {
		log.Printf("load active booking configuration: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := validatePricingRuleAgainstOptions(
		pricing,
		activities,
		layouts,
	); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updatePricingRule(pricing); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "pricing already exists for that option", http.StatusConflict)
			return
		}
		log.Printf("update pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing updated.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) deletePricingHandler(w http.ResponseWriter, r *http.Request) {
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

	pricingID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("pricing_id")), 10, 64)
	if err != nil || pricingID <= 0 {
		http.Error(w, "invalid pricing id", http.StatusBadRequest)
		return
	}
	if err := a.deletePricingRule(pricingID); err != nil {
		log.Printf("delete pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing deleted.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) createEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventFormSize)
	if err := r.ParseMultipartForm(maxEventImageSize); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	event, err := a.eventFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	game, err := a.findGameByID(event.GameID)
	if err != nil {
		a.removeUploadedEventImage(event.ImagePath)
		http.Error(w, "selected game was not found", http.StatusBadRequest)
		return
	}
	event.Category = game.Name
	if err := validateEvent(event); err != nil {
		a.removeUploadedEventImage(event.ImagePath)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createEvent(event); err != nil {
		a.removeUploadedEventImage(event.ImagePath)
		log.Printf("create event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Event created.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) updateEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventFormSize)
	if err := r.ParseMultipartForm(maxEventImageSize); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	eventID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("event_id")), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	existingEvent, err := a.findEventByID(eventID)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	event, err := a.eventFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	event.ID = eventID
	game, err := a.findGameByID(event.GameID)
	if err != nil {
		if event.ImagePath != "" {
			a.removeUploadedEventImage(event.ImagePath)
		}
		http.Error(w, "selected game was not found", http.StatusBadRequest)
		return
	}
	event.Category = game.Name
	uploadedReplacement := event.ImagePath
	if event.ImagePath == "" {
		event.ImagePath = existingEvent.ImagePath
	}
	deleteOldImage := false
	if r.FormValue("remove_image") == "true" && uploadedReplacement == "" {
		event.ImagePath = ""
		deleteOldImage = true
	}
	if err := validateEvent(event); err != nil {
		if uploadedReplacement != "" {
			a.removeUploadedEventImage(uploadedReplacement)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateEvent(event); err != nil {
		if uploadedReplacement != "" {
			a.removeUploadedEventImage(uploadedReplacement)
		}
		log.Printf("update event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if uploadedReplacement != "" && existingEvent.ImagePath != "" && existingEvent.ImagePath != uploadedReplacement {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}
	if deleteOldImage && existingEvent.ImagePath != "" && uploadedReplacement == "" {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}

	a.setFlash(w, "Event updated.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) deleteEventHandler(w http.ResponseWriter, r *http.Request) {
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

	eventID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("event_id")), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	existingEvent, _ := a.findEventByID(eventID)
	if err := a.deleteEvent(eventID); err != nil {
		log.Printf("delete event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if existingEvent != nil {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}

	a.setFlash(w, "Event deleted.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) buildBookingTemplateData(w http.ResponseWriter, r *http.Request, user *User) (TemplateData, error) {
	isBookingCalendar := r.URL.Path == "/admin/bookings"
	now := time.Now()
	a.expireOverdueBookingRequests(now)

	pricings, err := a.listPricingRules()
	if err != nil {
		return TemplateData{}, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return TemplateData{}, err
	}

	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return TemplateData{}, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Booking Manager"
	data.Description = "Manage bookings and training sessions."
	data.Pricings = pricings
	data.PricingSettings = settings
	data.CourtActivities = courtActivities
	data.CourtLayouts = courtLayouts
	data.CourtClosures = courtClosures
	data.Games, _ = a.listGames(false)
	data.Activities = bookingActivities()
	data.Hours = bookingHours()
	data.TodayDate = time.Now().Format("2006-01-02")
	data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.CalendarDate == "" {
		data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("slot_date"))
	}
	if data.CalendarDate == "" {
		data.CalendarDate = time.Now().Format("2006-01-02")
	}
	selectedDate, err := time.Parse("2006-01-02", data.CalendarDate)
	if err != nil {
		selectedDate = time.Now()
		data.CalendarDate = selectedDate.Format("2006-01-02")
	}
	data.PreviousDate = selectedDate.AddDate(0, 0, -1).Format("2006-01-02")
	data.NextDate = selectedDate.AddDate(0, 0, 1).Format("2006-01-02")

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	selectedScheduleID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)

	if isBookingCalendar {
		weekStart, weekEnd := bookingCalendarWindow(selectedDate)
		rangeSchedules, err := a.listActiveSpaceSchedulesBetween(
			weekStart.Format("2006-01-02"),
			weekEnd.Format("2006-01-02"),
		)
		if err != nil {
			return TemplateData{}, err
		}
		pendingCount, err := a.countPendingSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		heldCount, err := a.countHeldSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		reschedulePendingCount, err := a.countReschedulePendingSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		filteredClosures := courtClosuresBetween(
			courtClosures,
			weekStart.Format("2006-01-02"),
			weekEnd.Format("2006-01-02"),
		)

		activeSchedules := activeSchedulesOnly(rangeSchedules)
		data.Schedules = rangeSchedules
		data.PendingRequestCount = pendingCount
		data.HeldRequestCount = heldCount
		data.DaySchedules = schedulesForDate(rangeSchedules, data.CalendarDate)
		data.BookingRequests = customerBookingRequests(data.DaySchedules)
		data.BookingReminders = buildBookingReminders(customerBookingRequests(rangeSchedules), now)
		data.BookingAttentionStats = buildBookingAttentionStats(data.BookingReminders, pendingCount, heldCount, reschedulePendingCount)

		relevantScheduleIDs := scheduleIDs(data.DaySchedules)
		if selectedScheduleID > 0 {
			relevantScheduleIDs = appendInt64Unique(relevantScheduleIDs, selectedScheduleID)
		}

		data.BookingFinancials, err = a.listBookingFinancialsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingPaymentCollections, err = a.listBookingPaymentCollectionsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingReferrals, err = a.listBookingReferralsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingRequestChanges, err = a.listBookingRequestChangesForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingCommunications, err = a.listBookingCommunicationsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingAccessTokens, err = a.listBookingAccessTokensForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingCancellationRequests, err = a.listBookingCancellationRequestsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.WeekDays = buildBookingWeekDays(
			activeSchedules,
			selectedDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			filteredClosures,
		)
		data.BookingSlots = buildBookingSlotAvailability(
			activeSchedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			filteredClosures,
		)
		data.AdminCalendarHours = buildAdminCalendarHours(
			data.CalendarDate,
			data.Hours,
			data.DaySchedules,
			courtActivities,
			courtLayouts,
			filteredClosures,
			pricings,
			settings,
			data.BookingFinancials,
			data.BookingReferrals,
			data.BookingRequestChanges,
		)
		data.DailyStats = buildAdminCalendarStats(data.AdminCalendarHours)
	} else {
		schedules, err := a.listSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		pending, err := a.listPendingSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		bookingFinancials, err := a.listBookingFinancials()
		if err != nil {
			return TemplateData{}, err
		}
		bookingReferrals, err := a.listBookingReferrals()
		if err != nil {
			return TemplateData{}, err
		}
		bookingRequestChanges, err := a.listBookingRequestChanges()
		if err != nil {
			return TemplateData{}, err
		}

		data.Schedules = schedules
		data.PendingSchedules = pending
		data.PendingRequestCount = 0
		data.HeldRequestCount = 0
		reschedulePendingCount := 0
		for _, schedule := range pending {
			if schedule.Status == bookingStatusPending {
				data.PendingRequestCount++
			}
			if schedule.Status == bookingStatusHeld {
				data.HeldRequestCount++
			}
			if schedule.Status == bookingStatusReschedulePending {
				reschedulePendingCount++
			}
		}
		data.BookingRequests = customerBookingRequests(schedules)
		data.BookingRequestStats = buildBookingRequestStats(data.BookingRequests)
		data.BookingReminders = buildBookingReminders(data.BookingRequests, now)
		data.BookingAttentionStats = buildBookingAttentionStats(data.BookingReminders, data.PendingRequestCount, data.HeldRequestCount, reschedulePendingCount)
		data.BookingFinancials = bookingFinancials
		data.BookingPaymentCollections, err = a.listBookingPaymentCollectionsForScheduleIDs(scheduleIDsFromFinancials(bookingFinancials))
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingReferrals = bookingReferrals
		data.BookingRequestChanges = bookingRequestChanges
		data.BookingCommunications, err = a.listBookingCommunicationsForScheduleIDs(scheduleIDs(data.BookingRequests))
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingAccessTokens, err = a.listBookingAccessTokensForScheduleIDs(scheduleIDs(data.BookingRequests))
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingCancellationRequests, err = a.listBookingCancellationRequestsForScheduleIDs(scheduleIDs(data.BookingRequests))
		if err != nil {
			return TemplateData{}, err
		}

		activeSchedules := activeSchedulesOnly(schedules)
		data.DaySchedules = schedulesForDate(activeSchedules, data.CalendarDate)
		data.DailyStats = buildDailyBookingStats(data.DaySchedules, data.Hours)
		data.WeekDays = buildBookingWeekDays(
			activeSchedules,
			selectedDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		)
		data.BookingSlots = buildBookingSlotAvailability(
			activeSchedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		)
	}

	activeSchedulesForOptions := activeSchedulesOnly(data.Schedules)

	switch mode {
	case "new":
		draft := prefillAdminBookingDraft(r, data.CalendarDate)
		options, blockedReason, err := buildAdminBookingOptions(
			activeSchedulesForOptions,
			draft,
			0,
			courtActivities,
			courtLayouts,
			courtClosures,
			pricings,
			settings,
		)
		if err != nil {
			return TemplateData{}, err
		}
		data.DraftSchedule = draft
		data.AdminBookingOptions = options
		data.AdminBookingBlockedReason = blockedReason
	case "edit":
		scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && scheduleID > 0 {
			selectedSchedule, err := a.findSpaceScheduleByID(scheduleID)
			if err == nil {
				draft := *selectedSchedule
				applyAdminBookingQueryDraft(r, &draft)
				options, blockedReason, err := buildAdminBookingOptions(
					activeSchedulesForOptions,
					&draft,
					draft.ID,
					courtActivities,
					courtLayouts,
					courtClosures,
					pricings,
					settings,
				)
				if err != nil {
					return TemplateData{}, err
				}
				data.SelectedSchedule = &draft
				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		}
	}
	return data, nil
}

func (a *App) buildPublicBookingData(
	w http.ResponseWriter,
	r *http.Request,
	viewer *User,
) (TemplateData, error) {
	schedules, err := a.listActiveSpaceSchedules()
	if err != nil {
		return TemplateData{}, err
	}

	pricings, err := a.listPricingRules()
	if err != nil {
		return TemplateData{}, err
	}

	settings, err := a.getPricingSettings()
	if err != nil {
		return TemplateData{}, err
	}

	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return TemplateData{}, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, nil)
	data.Viewer = viewer
	data.Title = "Book a Slot"
	data.Description =
		"Check availability and request a booking."
	data.Schedules = schedules
	data.Pricings = pricings
	data.PricingSettings = settings
	data.CourtActivities = courtActivities
	data.CourtLayouts = courtLayouts
	data.Activities = bookingActivities()
	data.Hours = bookingHours()

	data.CalendarDate = strings.TrimSpace(
		r.URL.Query().Get("date"),
	)

	if data.CalendarDate == "" {
		data.CalendarDate =
			time.Now().Format("2006-01-02")
	}

	selectedDate, err := time.Parse(
		"2006-01-02",
		data.CalendarDate,
	)
	if err != nil {
		selectedDate = time.Now()
		data.CalendarDate =
			selectedDate.Format("2006-01-02")
	}

	data.TodayDate =
		time.Now().Format("2006-01-02")

	if data.CalendarDate < data.TodayDate {
		selectedDate = time.Now()
		data.CalendarDate = data.TodayDate
	}

	data.PreviousDate = selectedDate.
		AddDate(0, 0, -1).
		Format("2006-01-02")

	data.NextDate = selectedDate.
		AddDate(0, 0, 1).
		Format("2006-01-02")

	data.CalendarCanGoBack =
		data.CalendarDate > data.TodayDate

	data.BookingSlots = filterPricedBookingSlots(
		buildBookingSlotAvailability(
			schedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		),
		data.CalendarDate,
		pricings,
		settings,
	)

	data.WeekDays = buildPricedBookingWeekDays(
		schedules,
		selectedDate,
		data.Hours,
		pricings,
		settings,
		courtActivities,
		courtLayouts,
		courtClosures,
	)

	data.DraftSchedule =
		prefillPublicBookingDraft(
			r,
			viewer,
			data.CalendarDate,
		)

	return data, nil
}

func (a *App) writePublicBookingError(w http.ResponseWriter, r *http.Request, draft *SpaceSchedule, message string, status int) {
	viewer := a.optionalUser(r)
	data, err := a.buildPublicBookingData(w, r, viewer)
	if err != nil {
		log.Printf("build public booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Error = message
	data.DraftSchedule = draft
	a.render(w, "book", data, status)
}

func (a *App) writeBookingError(w http.ResponseWriter, r *http.Request, mode string, selected *SpaceSchedule, message string, status int) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Error = message
	data.ScheduleMode = mode
	if mode == "edit" {
		data.SelectedSchedule = selected
	} else if mode == "new" {
		data.DraftSchedule = selected
	}
	if selected != nil {
		closures, closureErr := a.listActiveCourtClosures()
		if closureErr == nil {
			options, blockedReason, optionErr := buildAdminBookingOptions(
				activeSchedulesOnly(data.Schedules),
				selected,
				selected.ID,
				data.CourtActivities,
				data.CourtLayouts,
				closures,
				data.Pricings,
				data.PricingSettings,
			)
			if optionErr == nil {
				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		} else {
			log.Printf("load booking error closures: %v", closureErr)
		}
	}
	a.render(w, "booking-management", data, status)
}

func (a *App) writeBookingRequestRescheduleError(
	w http.ResponseWriter,
	r *http.Request,
	scheduleID int64,
	proposed *SpaceSchedule,
	reviewNote string,
	customerMessage string,
	message string,
	status int,
) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking request data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data.Title = "Booking Requests"
	data.Description = "Review pending booking requests."
	data.Error = message

	selectedRequest, findErr := a.findSpaceScheduleByID(scheduleID)
	if findErr == nil {
		draft := *selectedRequest
		if proposed != nil {
			draft.SlotDate = proposed.SlotDate
			draft.SlotHour = proposed.SlotHour
			draft.Activity = proposed.Activity
			draft.Quantity = proposed.Quantity
		}
		draft.ReviewNote = reviewNote
		draft.CustomerMessage = customerMessage
		data.SelectedSchedule = selectedRequest
		data.DraftSchedule = &draft

		options, blockedReason, optionErr := a.adminBookingOptionsForSchedule(draft, selectedRequest.ID)
		if optionErr == nil {
			data.AdminBookingOptions = options
			data.AdminBookingBlockedReason = blockedReason
		}
	}

	a.render(w, "booking-requests", data, status)
}

func (a *App) createManagedUserHandler(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	roles, err := a.normalizeExistingRoles(r.Form["roles"])
	divisionIDs := normalizeDivisionIDs(r.Form["division_ids"])
	verified := r.FormValue("verified") == "true"
	if err != nil {
		http.Error(w, "one or more selected roles are invalid", http.StatusBadRequest)
		return
	}

	if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
		http.Error(w, "invalid user fields", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsPrivilegedRole(roles) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can assign administrator roles", http.StatusForbidden)
		return
	}

	createdUser, err := a.createManagedUser(name, email, password, roles, verified)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		log.Printf("create managed user: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := a.replaceUserDivisions(createdUser.ID, divisionIDs); err != nil {
		log.Printf("assign divisions to new user: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "User created.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *App) createCoachHandler(w http.ResponseWriter, r *http.Request) {
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

	coach, password, err := coachFromRequest(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := a.createCoach(coach, password); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		if errors.Is(err, ErrCoachRequiresMainCoach) || errors.Is(err, ErrCoachParentMustBeMain) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("create coach: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Coach created.")
	http.Redirect(w, r, "/admin/coaches", http.StatusSeeOther)
}

func (a *App) updateCoachHandler(w http.ResponseWriter, r *http.Request) {
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

	coach, _, err := coachFromRequest(r, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if coach.ID <= 0 {
		http.Error(w, "invalid coach id", http.StatusBadRequest)
		return
	}
	if err := a.updateCoach(coach); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "coach not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		if errors.Is(err, ErrCoachRequiresMainCoach) || errors.Is(err, ErrCoachParentMustBeMain) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrCoachHasSubCoaches) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("update coach: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Coach updated.")
	http.Redirect(w, r, "/admin/coaches", http.StatusSeeOther)
}

func (a *App) deleteCoachHandler(w http.ResponseWriter, r *http.Request) {
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

	coachID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("coach_id")), 10, 64)
	if err != nil || coachID <= 0 {
		http.Error(w, "invalid coach id", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if currentUser != nil && currentUser.ID == coachID {
		http.Error(w, "you cannot delete your own account", http.StatusForbidden)
		return
	}
	if err := a.deleteCoach(coachID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "coach not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrCoachHasOtherRoles) || errors.Is(err, ErrCoachHasSubCoaches) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("delete coach: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Coach deleted.")
	http.Redirect(w, r, "/admin/coaches", http.StatusSeeOther)
}

func (a *App) createRoleHandler(w http.ResponseWriter, r *http.Request) {
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

	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if !roleNamePattern.MatchString(name) || isSystemRole(name) {
		http.Error(w, "role name must be 3-32 lowercase letters, numbers, hyphens, or underscores and cannot be a system role", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsSensitivePermission(permissions) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can grant identity administration permissions", http.StatusForbidden)
		return
	}
	if err := a.createRole(name, permissions); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "a role with this name already exists", http.StatusConflict)
			return
		}
		log.Printf("create role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role created.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRoleHandler(w http.ResponseWriter, r *http.Request) {
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

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if !roleNamePattern.MatchString(name) || isSystemRole(name) {
		http.Error(w, "role name must be 3-32 lowercase letters, numbers, hyphens, or underscores and cannot be a system role", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	role, err := a.findRoleByID(roleID)
	if err != nil {
		http.Error(w, "role not found", http.StatusNotFound)
		return
	}
	if role.System {
		http.Error(w, "system roles are protected and cannot be changed", http.StatusForbidden)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsSensitivePermission(permissions) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can grant identity administration permissions", http.StatusForbidden)
		return
	}
	if !containsRole(current.Roles, "superadmin") {
		assigned, err := a.userHasRole(current.ID, role.Name)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if assigned {
			http.Error(w, "you cannot change a role assigned to your own account", http.StatusForbidden)
			return
		}
	}

	if err := a.updateRole(roleID, name, permissions); err != nil {
		log.Printf("update role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role updated.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) deleteRoleHandler(w http.ResponseWriter, r *http.Request) {
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

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	if err := a.deleteRole(roleID); err != nil {
		if errors.Is(err, ErrRoleAssigned) || errors.Is(err, ErrSystemRoleProtected) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("delete role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role deleted.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRolesHandler(w http.ResponseWriter, r *http.Request) {
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

	targetID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	roles, err := a.normalizeExistingRoles(r.Form["roles"])
	if err != nil {
		http.Error(w, "one or more selected roles are invalid", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	current, _ := a.currentUser(r.Context())
	if current.ID == targetID {
		http.Error(w, "you cannot change roles on your own account", http.StatusForbidden)
		return
	}
	target, err := a.findUserByID(targetID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	currentIsSuperadmin := containsRole(current.Roles, "superadmin")
	if (containsPrivilegedRole(target.Roles) || containsPrivilegedRole(roles)) && !currentIsSuperadmin {
		http.Error(w, "only a superadmin can manage administrator accounts", http.StatusForbidden)
		return
	}

	if err := a.replaceUserRoles(targetID, roles); err != nil {
		log.Printf("replace roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := a.replaceUserDivisions(targetID, normalizeDivisionIDs(r.Form["division_ids"])); err != nil {
		log.Printf("replace user divisions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	redirectTo := r.FormValue("return_to")
	if redirectTo != "/admin/users" && redirectTo != "/admin/roles" {
		redirectTo = "/admin/users"
	}
	a.setFlash(w, "Roles updated.")
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (a *App) updateCourtActivityAutoAcceptHandler(w http.ResponseWriter, r *http.Request) {
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
	activityID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("activity_id")), 10, 64)
	if err != nil || activityID <= 0 {
		http.Error(w, "invalid activity id", http.StatusBadRequest)
		return
	}
	courtID := strings.TrimSpace(r.FormValue("court_id"))
	autoAccept := r.FormValue("auto_accept") == "1"
	if _, err := a.db.Exec(`
		UPDATE court_activities
		SET auto_accept = ?, updated_at = ?
		WHERE id = ?
	`, boolToInt(autoAccept), time.Now().UTC(), activityID); err != nil {
		log.Printf("update court activity auto accept: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Activity auto-accept setting updated.")
	http.Redirect(w, r, "/admin/courts?court_id="+url.QueryEscape(courtID), http.StatusSeeOther)
}

func (a *App) updateCourtActivityGameHandler(w http.ResponseWriter, r *http.Request) {
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

	activityID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("activity_id")), 10, 64)
	if err != nil || activityID <= 0 {
		http.Error(w, "invalid activity id", http.StatusBadRequest)
		return
	}
	courtID := strings.TrimSpace(r.FormValue("court_id"))

	var currentActivity string
	if err := a.db.QueryRow(`SELECT activity FROM court_activities WHERE id = ?`, activityID).Scan(&currentActivity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "activity not found", http.StatusNotFound)
			return
		}
		log.Printf("find court activity: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	gameID := int64(0)
	if rawGameID := strings.TrimSpace(r.FormValue("game_id")); rawGameID != "" {
		gameID, err = strconv.ParseInt(rawGameID, 10, 64)
		if err != nil || gameID <= 0 {
			http.Error(w, "invalid game id", http.StatusBadRequest)
			return
		}
		if currentActivity == "training" {
			http.Error(w, "training activity cannot be linked to a public game", http.StatusBadRequest)
			return
		}
		if _, err := a.findGameByID(gameID); err != nil {
			http.Error(w, "selected game was not found", http.StatusBadRequest)
			return
		}
	}

	if _, err := a.db.Exec(`
		UPDATE court_activities
		SET game_id = ?, updated_at = ?
		WHERE id = ?
	`, gameID, time.Now().UTC(), activityID); err != nil {
		log.Printf("update court activity game: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Court activity game updated.")
	http.Redirect(w, r, "/admin/courts?court_id="+url.QueryEscape(courtID), http.StatusSeeOther)
}

func (a *App) createCourtActivityHandler(
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

	games, _ := a.listGames(true)
	activity, err := courtActivityFromRequest(r, games)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/courts?court_id="+r.FormValue("court_id")+"&activity_action=new#activity-form", http.StatusSeeOther)
		return
	}

	activityID, err := a.createCourtActivity(activity)
	if err != nil {
		log.Printf("create court activity: %v", err)
		if isUniqueConstraintError(err) {
			a.setFlash(w, "That activity already exists on this court.")
			http.Redirect(w, r, "/admin/courts?court_id="+r.FormValue("court_id")+"&activity_action=new#activity-form", http.StatusSeeOther)
			return
		}
		a.setFlash(w, "Court activity could not be created.")
		http.Redirect(w, r, "/admin/courts?court_id="+r.FormValue("court_id")+"&activity_action=new#activity-form", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Court activity created.")
	http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(activity.CourtID, 10)+"&activity_id="+strconv.FormatInt(activityID, 10), http.StatusSeeOther)
}

func (a *App) updateCourtActivityHandler(
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

	activityID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("activity_id")),
		10,
		64,
	)
	if err != nil || activityID <= 0 {
		http.Error(w, "invalid activity id", http.StatusBadRequest)
		return
	}

	existing, err := a.findCourtActivityByID(activityID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Court activity not found.")
			http.Redirect(w, r, "/admin/courts?court_id="+r.FormValue("court_id"), http.StatusSeeOther)
			return
		}
		log.Printf("find court activity: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	updated := *existing
	updated.DisplayName = strings.TrimSpace(r.FormValue("display_name"))
	maxQuantity, err := strconv.Atoi(strings.TrimSpace(r.FormValue("max_quantity")))
	if err != nil {
		a.setFlash(w, "Valid maximum quantity is required.")
		http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(existing.CourtID, 10)+"&activity_action=edit&activity_id="+strconv.FormatInt(existing.ID, 10)+"#activity-form", http.StatusSeeOther)
		return
	}
	sortOrder, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	if err != nil {
		a.setFlash(w, "Valid sort order is required.")
		http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(existing.CourtID, 10)+"&activity_action=edit&activity_id="+strconv.FormatInt(existing.ID, 10)+"#activity-form", http.StatusSeeOther)
		return
	}
	updated.MaxQuantity = maxQuantity
	updated.SortOrder = sortOrder
	updated.AutoAccept = r.FormValue("auto_accept") == "1"
	updated.Active = r.FormValue("active") == "1"

	if err := a.updateCourtActivity(updated); err != nil {
		log.Printf("update court activity: %v", err)
		a.setFlash(w, "Court activity could not be updated.")
		http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(existing.CourtID, 10)+"&activity_action=edit&activity_id="+strconv.FormatInt(existing.ID, 10)+"#activity-form", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Court activity updated.")
	http.Redirect(w, r, "/admin/courts?court_id="+strconv.FormatInt(existing.CourtID, 10), http.StatusSeeOther)
}

func (a *App) deleteCourtActivityHandler(
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

	activityID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("activity_id")),
		10,
		64,
	)
	if err != nil || activityID <= 0 {
		http.Error(w, "invalid activity id", http.StatusBadRequest)
		return
	}

	courtID := strings.TrimSpace(r.FormValue("court_id"))
	if err := a.deleteCourtActivity(activityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Court activity not found.")
		} else {
			a.setFlash(w, err.Error())
		}
		http.Redirect(w, r, "/admin/courts?court_id="+url.QueryEscape(courtID), http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Court activity deleted.")
	http.Redirect(w, r, "/admin/courts?court_id="+url.QueryEscape(courtID), http.StatusSeeOther)
}

func (a *App) createAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	if err := r.ParseMultipartForm(maxStudentPhotoSize + (1 << 20)); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admission := admissionFromRequest(r)
	admission.PracticeType = "student"
	admission.QRCodeValue = admission.StudentID
	photoPath, err := a.uploadedStudentPhotoPath(r, "photo")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	admission.PhotoPath = photoPath
	qrPath, err := a.uploads.saveStudentQRCode(admission.StudentID, admission.QRCodeValue)
	if err != nil {
		if admission.PhotoPath != "" {
			a.removeUploadedStudentPhoto(admission.PhotoPath)
		}
		log.Printf("generate student qr: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admission.QRCodePath = qrPath

	if err := validateAdmission(admission); err != nil {
		if admission.PhotoPath != "" {
			a.removeUploadedStudentPhoto(admission.PhotoPath)
		}
		if admission.QRCodePath != "" {
			a.removeUploadedStudentQRCode(admission.QRCodePath)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}

	_, _, err = a.createAdmissionWithOptionalPayment(admission, false, "cash", recordedByUserID)
	if err != nil {
		if admission.PhotoPath != "" {
			a.removeUploadedStudentPhoto(admission.PhotoPath)
		}
		if admission.QRCodePath != "" {
			a.removeUploadedStudentQRCode(admission.QRCodePath)
		}
		log.Printf("create admission: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	a.setFlash(w, "Admission created.")
	http.Redirect(
		w,
		r,
		"/admin/admissions",
		http.StatusSeeOther,
	)
}

func (a *App) updateAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(maxStudentPhotoSize + (1 << 20)); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}

	existing, err := a.findAdmissionByID(admissionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "student not found", http.StatusNotFound)
			return
		}
		log.Printf("find admission for update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admission := admissionFromRequest(r)
	admission.ID = admissionID
	admission.PracticeType = existing.PracticeType
	admission.PhotoPath = existing.PhotoPath
	admission.QRCodePath = existing.QRCodePath
	admission.QRCodeValue = admission.StudentID
	photoPath, err := a.uploadedStudentPhotoPath(r, "photo")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if photoPath != "" {
		admission.PhotoPath = photoPath
	}
	if admission.StudentID != existing.StudentID || admission.QRCodePath == "" {
		qrPath, err := a.uploads.saveStudentQRCode(admission.StudentID, admission.QRCodeValue)
		if err != nil {
			if photoPath != "" {
				a.removeUploadedStudentPhoto(photoPath)
			}
			log.Printf("regenerate student qr: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		admission.QRCodePath = qrPath
	}

	if err := validateAdmission(admission); err != nil {
		if photoPath != "" {
			a.removeUploadedStudentPhoto(photoPath)
		}
		if admission.QRCodePath != existing.QRCodePath {
			a.removeUploadedStudentQRCode(admission.QRCodePath)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	financeTransactionID, err := a.updateAdmissionWithOptionalPayment(admission, false, 0)
	if err != nil {
		if photoPath != "" {
			a.removeUploadedStudentPhoto(photoPath)
		}
		if admission.QRCodePath != existing.QRCodePath {
			a.removeUploadedStudentQRCode(admission.QRCodePath)
		}
		log.Printf("update admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if photoPath != "" && existing.PhotoPath != "" && existing.PhotoPath != photoPath {
		a.removeUploadedStudentPhoto(existing.PhotoPath)
	}
	if existing.QRCodePath != "" && existing.QRCodePath != admission.QRCodePath {
		a.removeUploadedStudentQRCode(existing.QRCodePath)
	}
	_ = financeTransactionID
	a.setFlash(w, "Admission updated.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) collectStudentPaymentHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/student-payments"
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

	enrollmentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("enrollment_id")), 10, 64)
	if err != nil || enrollmentID <= 0 {
		a.setFlash(w, "Select a valid enrollment.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	paymentMonth := strings.TrimSpace(r.FormValue("payment_month"))
	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if paymentMonth > time.Now().Format("2006-01") {
		a.setFlash(w, "Payments cannot be collected for a future month.")
		http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
		return
	}
	if !paymentMonthCollectible(paymentMonth, time.Now()) {
		a.setFlash(w, monthlyPaymentCollectionNotice(paymentMonth, time.Now()))
		http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
		return
	}
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	amount, amountErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if amountErr != nil {
		a.setFlash(w, "Enter a valid payment amount.")
		http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
		return
	}
	if !validPaymentMethod(paymentMethod) {
		a.setFlash(w, "Select a valid payment method.")
		http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	transactionID, err := a.collectStudentMonthlyPaymentAmount(enrollmentID, paymentMonth, monthDate, paymentMethod, amount, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrStudentPaymentAlreadyCollected) {
			a.setFlash(w, "That enrollment payment has already been collected for "+paymentMonthLabel(paymentMonth)+".")
			http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
			return
		}
		if errors.Is(err, ErrStudentNotAdmittedForMonth) {
			a.setFlash(w, err.Error())
			http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
			return
		}
		if errors.Is(err, ErrMonthlyFeeNotConfigured) {
			a.setFlash(w, err.Error())
			http.Redirect(w, r, target+"?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
			return
		}
		if errors.Is(err, ErrStudentLeaveCoversMonth) {
			a.setFlash(w, "This student is fully on leave for "+paymentMonthLabel(paymentMonth)+", so no monthly fee is due.")
			admissionID := enrollmentID
			if enrollment, findErr := a.findStudentEnrollmentByID(enrollmentID); findErr == nil {
				admissionID = enrollment.AdmissionID
			}
			http.Redirect(w, r, "/admin/student-leaves?admission_id="+strconv.FormatInt(admissionID, 10)+"&enrollment_id="+strconv.FormatInt(enrollmentID, 10), http.StatusSeeOther)
			return
		}
		log.Printf("collect student monthly payment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) studentLeaveManagementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions for student leaves: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	allEnrollments, err := a.listStudentEnrollments()
	if err != nil {
		log.Printf("list student enrollments for student leaves: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Leave"
	data.Description = "Manage temporary leave periods for student enrollments."
	data.Admissions = admissions

	admissionID := parseInt64Query(r.URL.Query().Get("admission_id"))
	selectedEnrollmentID := parseInt64Query(r.URL.Query().Get("enrollment_id"))

	if admissionID > 0 {
		for i := range admissions {
			if admissions[i].ID == admissionID {
				data.SelectedAdmission = &admissions[i]
				break
			}
		}
	}

	if data.SelectedAdmission != nil {
		filtered := make([]StudentEnrollment, 0)
		for _, enrollment := range allEnrollments {
			if enrollment.AdmissionID == data.SelectedAdmission.ID {
				filtered = append(filtered, enrollment)
			}
		}
		data.Enrollments = filtered
		if selectedEnrollmentID <= 0 && len(filtered) > 0 {
			selectedEnrollmentID = filtered[0].ID
		}
		if selectedEnrollmentID > 0 {
			for i := range filtered {
				if filtered[i].ID == selectedEnrollmentID {
					data.SelectedEnrollment = &filtered[i]
					break
				}
			}
		}
		if data.SelectedEnrollment != nil {
			leaves, err := a.listStudentEnrollmentLeaves(data.SelectedEnrollment.ID)
			if err != nil {
				log.Printf("list enrollment leaves: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			data.EnrollmentLeaves = leaves
		}
	}

	a.render(w, "student-leave-management", data, http.StatusOK)
}

func (a *App) createStudentEnrollmentLeaveHandler(w http.ResponseWriter, r *http.Request) {
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

	enrollmentID := parseInt64Query(r.FormValue("enrollment_id"))
	admissionID := parseInt64Query(r.FormValue("admission_id"))
	target := "/admin/student-leaves"
	if admissionID > 0 {
		target += "?admission_id=" + strconv.FormatInt(admissionID, 10)
		if enrollmentID > 0 {
			target += "&enrollment_id=" + strconv.FormatInt(enrollmentID, 10)
		}
	}
	if err := a.createStudentEnrollmentLeave(
		enrollmentID,
		r.FormValue("start_date"),
		r.FormValue("end_date"),
		r.FormValue("reason"),
	); err != nil {
		a.setFlash(w, "Leave could not be saved: "+err.Error())
		http.Redirect(w, r, target+"#leave-manager", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Student leave saved.")
	http.Redirect(w, r, target+"#leave-manager", http.StatusSeeOther)
}

func (a *App) deleteStudentEnrollmentLeaveHandler(w http.ResponseWriter, r *http.Request) {
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

	leaveID := parseInt64Query(r.FormValue("leave_id"))
	enrollmentID := parseInt64Query(r.FormValue("enrollment_id"))
	admissionID := parseInt64Query(r.FormValue("admission_id"))
	target := "/admin/student-leaves"
	if admissionID > 0 {
		target += "?admission_id=" + strconv.FormatInt(admissionID, 10)
		if enrollmentID > 0 {
			target += "&enrollment_id=" + strconv.FormatInt(enrollmentID, 10)
		}
	}
	if err := a.deleteStudentEnrollmentLeave(leaveID, enrollmentID); err != nil {
		a.setFlash(w, "Leave could not be deleted: "+err.Error())
		http.Redirect(w, r, target+"#leave-manager", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Student leave deleted.")
	http.Redirect(w, r, target+"#leave-manager", http.StatusSeeOther)
}

func (a *App) voidAdmissionPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	admissionID := parseInt64Query(r.FormValue("admission_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	if err := a.voidAdmissionPayment(admissionID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Admission payment could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Admission payment was voided.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) voidStudentPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	paymentID := parseInt64Query(r.FormValue("payment_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	redirectMonth := strings.TrimSpace(r.FormValue("payment_month"))
	if err := a.voidStudentMonthlyPayment(paymentID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Student payment could not be voided: "+err.Error())
		target := "/admin/student-payments"
		if redirectMonth != "" {
			target += "?month=" + url.QueryEscape(redirectMonth)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Student payment was voided.")
	target := "/admin/student-payments"
	if redirectMonth != "" {
		target += "?month=" + url.QueryEscape(redirectMonth)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) voidReferralCommissionPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	referralID := parseInt64Query(r.FormValue("referral_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	if err := a.voidReferralCommissionPayment(referralID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Referral payment could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/referrals", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Referral payment was voided.")
	http.Redirect(w, r, "/admin/referrals", http.StatusSeeOther)
}

func (a *App) deleteAdmissionHandler(w http.ResponseWriter, r *http.Request) {
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

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}
	if err := a.deleteAdmission(admissionID); err != nil {
		log.Printf("delete admission: %v", err)
		message := "Student could not be deleted: " + err.Error()
		if errors.Is(err, sql.ErrNoRows) {
			message = "Student could not be deleted: student was not found."
		}
		a.setFlash(w, message)
		http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Student deleted.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) createStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/student-groups"
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

	group := studentGroupFromRequest(r)
	admissionIDs := normalizePositiveIDs(r.Form["admission_ids"])
	coachIDs := normalizePositiveIDs(r.Form["coach_ids"])
	sessions := studentGroupSessionsFromRequest(r)
	if err := validateStudentGroup(group); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := validateStudentGroupSessions(sessions); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := a.createStudentGroup(group, admissionIDs, coachIDs, sessions); err != nil {
		if isUniqueConstraintError(err) {
			a.setFlash(w, "Group code already exists.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		log.Printf("create student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group created.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) updateStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/student-groups"
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

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		a.setFlash(w, "Select a valid student group.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	group := studentGroupFromRequest(r)
	group.ID = groupID
	admissionIDs := normalizePositiveIDs(r.Form["admission_ids"])
	coachIDs := normalizePositiveIDs(r.Form["coach_ids"])
	sessions := studentGroupSessionsFromRequest(r)
	if err := validateStudentGroup(group); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := validateStudentGroupSessions(sessions); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := a.updateStudentGroup(group, admissionIDs, coachIDs, sessions); err != nil {
		if isUniqueConstraintError(err) {
			a.setFlash(w, "Group code already exists.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		log.Printf("update student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group updated.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) deleteStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/student-groups"
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

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		a.setFlash(w, "Select a valid student group.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err := a.deleteStudentGroup(groupID); err != nil {
		log.Printf("delete student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group deleted.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) saveAttendanceHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/attendance"
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

	groupID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("group_id")), 10, 64)
	if err != nil || groupID <= 0 {
		a.setFlash(w, "Select a valid student group.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	currentUser, _ := a.currentUser(r.Context())

	if userHasRole(currentUser, "coach") &&
		!userHasRole(currentUser, "admin") &&
		!userHasRole(currentUser, "superadmin") {
		assigned, err := a.coachAssignedToGroup(currentUser.ID, groupID)
		if err != nil {
			log.Printf("check coach group assignment: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if !assigned {
			http.Error(w, "you are not assigned to this group", http.StatusForbidden)
			return
		}
	}
	attendanceDate := strings.TrimSpace(r.FormValue("attendance_date"))
	sessionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("session_id")), 10, 64)
	if err != nil || sessionID <= 0 {
		a.setFlash(w, "Select a valid session.")
		http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10), http.StatusSeeOther)
		return
	}
	parsedAttendanceDate, err := time.Parse("2006-01-02", attendanceDate)
	if err != nil {
		a.setFlash(w, "Select a valid attendance date.")
		http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10)+"&session_id="+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
		return
	}
	today := time.Now().Format("2006-01-02")
	if parsedAttendanceDate.Format("2006-01-02") > today {
		a.setFlash(w, "Attendance date cannot be in the future.")
		http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10)+"&session_id="+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
		return
	}

	group, err := a.findStudentGroupByID(groupID)
	if err != nil {
		a.setFlash(w, "Student group not found.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	session, err := a.findStudentGroupSessionByID(sessionID)
	if err != nil || session.GroupID != groupID {
		a.setFlash(w, "Session not found.")
		http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10), http.StatusSeeOther)
		return
	}
	if strings.ToLower(strings.TrimSpace(session.DayOfWeek)) != weekdayNameForDate(parsedAttendanceDate) {
		a.setFlash(w, "Attendance date does not match the selected session day.")
		http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10)+"&session_id="+strconv.FormatInt(sessionID, 10)+"&date="+url.QueryEscape(attendanceDate), http.StatusSeeOther)
		return
	}

	records := make([]AttendanceRecord, 0, len(group.Students))
	for _, student := range group.Students {
		status := normalizeAttendanceStatus(r.FormValue(fmt.Sprintf("status_%d", student.ID)))
		note := strings.TrimSpace(r.FormValue(fmt.Sprintf("note_%d", student.ID)))
		records = append(records, AttendanceRecord{
			GroupID:          groupID,
			SessionID:        sessionID,
			SessionTitle:     session.Title,
			AdmissionID:      student.ID,
			AttendanceDate:   attendanceDate,
			Status:           status,
			Note:             note,
			RecordedByUserID: currentUser.ID,
		})
	}

	if err := a.replaceAttendanceRecords(groupID, sessionID, attendanceDate, records); err != nil {
		log.Printf("save attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	warnings, err := a.listAttendanceLimitWarnings(groupID, sessionID, attendanceDate, 8)
	if err != nil {
		log.Printf("list attendance warnings after save: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	message := "Attendance saved."
	if len(warnings) > 0 {
		message = fmt.Sprintf("Attendance saved. %d student(s) exceeded the 8-session monthly limit.", len(warnings))
	}
	a.setFlash(w, message)
	http.Redirect(w, r, target+"?group_id="+strconv.FormatInt(groupID, 10)+"&session_id="+strconv.FormatInt(sessionID, 10)+"&date="+url.QueryEscape(attendanceDate), http.StatusSeeOther)
}

func (a *App) saveCoachAttendanceHandler(w http.ResponseWriter, r *http.Request) {
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

	attendanceDate := strings.TrimSpace(r.FormValue("attendance_date"))
	parsedAttendanceDate, err := time.Parse("2006-01-02", attendanceDate)
	if err != nil {
		http.Error(w, "invalid attendance date", http.StatusBadRequest)
		return
	}
	today := time.Now().Format("2006-01-02")
	if parsedAttendanceDate.Format("2006-01-02") > today {
		http.Error(w, "attendance date cannot be in the future", http.StatusBadRequest)
		return
	}

	coaches, err := a.listCoachUsersDetailed(false)
	if err != nil {
		log.Printf("list active coaches for attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	records := make([]CoachAttendanceRecord, 0, len(coaches))
	for _, coach := range coaches {
		records = append(records, CoachAttendanceRecord{
			UserID:           coach.ID,
			AttendanceDate:   attendanceDate,
			Status:           normalizeAttendanceStatus(r.FormValue(fmt.Sprintf("status_%d", coach.ID))),
			Note:             strings.TrimSpace(r.FormValue(fmt.Sprintf("note_%d", coach.ID))),
			RecordedByUserID: currentUser.ID,
		})
	}

	if err := a.replaceCoachAttendanceRecords(attendanceDate, records); err != nil {
		log.Printf("save coach attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Coach attendance saved.")
	http.Redirect(w, r, "/admin/coaches?date="+url.QueryEscape(attendanceDate), http.StatusSeeOther)
}

func (a *App) createBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		a.writeBookingError(w, r, "new", nil, "Invalid session token. Refresh and try again.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.writeBookingError(w, r, "new", nil, "Invalid form submission.", http.StatusBadRequest)
		return
	}

	schedule := scheduleFromRequest(r)
	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if schedule.EntryType == "booking" {
		quotedPrice, err := a.bookingQuote(schedule)
		if err != nil {
			a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
			return
		}
		schedule.QuotedPrice = quotedPrice
	}
	if err := a.createSpaceSchedule(schedule); err != nil {
		log.Printf("create booking: %v", err)
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule created.")
	http.Redirect(w, r, "/admin/bookings?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) updateBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		a.writeBookingError(w, r, "edit", nil, "Invalid session token. Refresh and try again.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.writeBookingError(w, r, "edit", nil, "Invalid form submission.", http.StatusBadRequest)
		return
	}

	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		a.writeBookingError(w, r, "edit", nil, "Invalid schedule id.", http.StatusBadRequest)
		return
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if schedule.EntryType == "booking" {
		quotedPrice, err := a.bookingQuote(schedule)
		if err != nil {
			a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
			return
		}
		schedule.QuotedPrice = quotedPrice
	}
	if err := a.updateSpaceSchedule(schedule); err != nil {
		log.Printf("update booking: %v", err)
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule updated.")
	http.Redirect(w, r, "/admin/bookings?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) deleteBookingHandler(w http.ResponseWriter, r *http.Request) {
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

	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	schedule, _ := a.findSpaceScheduleByID(scheduleID)
	if err := a.deleteSpaceSchedule(scheduleID); err != nil {
		log.Printf("delete booking: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Schedule deleted.")
	redirectTo := "/admin/bookings"
	if schedule != nil {
		redirectTo += "?date=" + url.QueryEscape(schedule.SlotDate)
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}
