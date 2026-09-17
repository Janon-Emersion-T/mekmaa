package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// This credential grants access only to the shared attendance endpoints.
const defaultAttendancePasswordHash = "$2a$10$I9qtQG2HvmT9hCfObPyk9ebMgDCze06kdHJKgZu1BrVOTMBc2S/XC"

func sharedAttendanceAuthenticated(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	expectedUser := envOrDefault("ATTENDANCE_USERNAME", "attendance")
	got, want := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(expectedUser))
	passwordOK := false
	if configured := os.Getenv("ATTENDANCE_PASSWORD"); configured != "" {
		supplied, expected := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(configured))
		passwordOK = subtle.ConstantTimeCompare(supplied[:], expected[:]) == 1
	} else {
		passwordOK = bcrypt.CompareHashAndPassword([]byte(defaultAttendancePasswordHash), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1 && passwordOK
}

func (a *App) sharedAttendanceHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "same-origin")
	if !sharedAttendanceAuthenticated(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Mekmaa Attendance", charset="UTF-8"`)
		http.Error(w, "Sign in with the attendance username and password.", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/attendance/save" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid attendance form.", http.StatusBadRequest)
			return
		}
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		a.saveSharedAttendance(w, r)
		return
	}
	if r.URL.Path != "/attendance" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.HideChrome = true
	data.Title = "Mark attendance | Mekmaa"
	data.Description = "Select a course, group and session to mark attendance."
	data.TodayDate = time.Now().Format("2006-01-02")
	data.AttendanceDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.AttendanceDate == "" {
		data.AttendanceDate = data.TodayDate
	}
	data.SharedAttendanceCourseID = parseInt64Query(r.URL.Query().Get("course_id"))
	programs, err := a.listTrainingProgramsByDivisionIDs(nil, false, false)
	if err != nil {
		sharedAttendanceError(w, err)
		return
	}
	data.TrainingPrograms = programs
	groups, err := a.listStudentGroupsByDivisionIDs(nil)
	if err != nil {
		sharedAttendanceError(w, err)
		return
	}
	courseAvailable := false
	for _, program := range programs {
		if program.ID == data.SharedAttendanceCourseID {
			courseAvailable = true
			break
		}
	}
	groupID := parseInt64Query(r.URL.Query().Get("group_id"))
	sessionID := parseInt64Query(r.URL.Query().Get("session_id"))
	if courseAvailable {
		for _, group := range groups {
			if group.TrainingProgramID != data.SharedAttendanceCourseID {
				continue
			}
			data.StudentGroups = append(data.StudentGroups, group)
			if group.ID == groupID {
				selected := group
				data.SelectedGroup = &selected
			}
		}
	}
	if data.SelectedGroup != nil {
		for _, session := range data.SelectedGroup.Sessions {
			if !session.Active {
				continue
			}
			data.GroupSessions = append(data.GroupSessions, session)
			if session.ID == sessionID {
				data.SelectedGroupSessionID = sessionID
			}
		}
		if data.SelectedGroupSessionID > 0 {
			if err := validateSharedAttendanceDate(data.AttendanceDate, data.GroupSessions, sessionID); err != nil {
				data.Error = err.Error()
			} else {
				records, err := a.listAttendanceRecords(groupID, sessionID, data.AttendanceDate)
				if err != nil {
					sharedAttendanceError(w, err)
					return
				}
				data.SharedAttendanceSaved = len(records) > 0
			}
		}
	}
	if r.URL.Query().Get("saved") == "1" && data.SharedAttendanceSaved {
		data.Flash = "Attendance saved successfully."
	}
	a.render(w, "shared-attendance", data, http.StatusOK)
}

func validateSharedAttendanceDate(date string, sessions []StudentGroupSession, sessionID int64) error {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return errors.New("Select a valid attendance date.")
	}
	if date > time.Now().Format("2006-01-02") {
		return errors.New("Attendance date cannot be in the future.")
	}
	if err := validateHistoricalEntryTime(parsed, "attendance date"); err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID == sessionID && session.Active {
			if strings.ToLower(session.DayOfWeek) != weekdayNameForDate(parsed) {
				return errors.New("Attendance date does not match the selected session day.")
			}
			return nil
		}
	}
	return errors.New("Select an active session belonging to this group.")
}

func sharedAttendanceError(w http.ResponseWriter, err error) {
	log.Printf("shared attendance: %v", err)
	http.Error(w, "Could not load or save attendance. Please try again.", http.StatusInternalServerError)
}

func (a *App) saveSharedAttendance(w http.ResponseWriter, r *http.Request) {
	courseID := parseInt64Query(r.PostForm.Get("course_id"))
	groupID := parseInt64Query(r.PostForm.Get("group_id"))
	sessionID := parseInt64Query(r.PostForm.Get("session_id"))
	date := strings.TrimSpace(r.PostForm.Get("attendance_date"))
	if courseID <= 0 || groupID <= 0 || sessionID <= 0 {
		http.Error(w, "Select a course, group and session.", http.StatusBadRequest)
		return
	}
	group, err := a.findStudentGroupByIDForDivisionIDs(groupID, nil)
	if err != nil || group.TrainingProgramID != courseID {
		http.Error(w, "Select a group belonging to this course.", http.StatusBadRequest)
		return
	}
	programs, err := a.listTrainingProgramsByDivisionIDs(nil, false, false)
	if err != nil {
		sharedAttendanceError(w, err)
		return
	}
	active := false
	for _, program := range programs {
		if program.ID == courseID {
			active = true
			break
		}
	}
	if !active {
		http.Error(w, "Select an active course.", http.StatusBadRequest)
		return
	}
	if err := validateSharedAttendanceDate(date, group.Sessions, sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(group.Students) == 0 {
		http.Error(w, "This group has no students to mark.", http.StatusBadRequest)
		return
	}
	records := make([]AttendanceRecord, 0, len(group.Students))
	for _, student := range group.Students {
		status := r.PostForm.Get(fmt.Sprintf("status_%d", student.ID))
		switch status {
		case "present", "absent", "late", "excused":
		default:
			http.Error(w, "Select attendance for every student before saving.", http.StatusBadRequest)
			return
		}
		note := strings.TrimSpace(r.PostForm.Get(fmt.Sprintf("note_%d", student.ID)))
		if len(note) > 1000 {
			http.Error(w, "Attendance notes must be at most 1000 characters.", http.StatusBadRequest)
			return
		}
		records = append(records, AttendanceRecord{GroupID: groupID, SessionID: sessionID, AdmissionID: student.ID, AttendanceDate: date, Status: status, Note: note})
	}
	if err := a.writeAttendanceRecords(groupID, sessionID, date, records, true); err != nil {
		if errors.Is(err, errAttendanceAlreadySaved) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		sharedAttendanceError(w, err)
		return
	}
	query := url.Values{"course_id": {strconv.FormatInt(courseID, 10)}, "group_id": {strconv.FormatInt(groupID, 10)}, "session_id": {strconv.FormatInt(sessionID, 10)}, "date": {date}, "saved": {"1"}}
	http.Redirect(w, r, "/attendance?"+query.Encode(), http.StatusSeeOther)
}
