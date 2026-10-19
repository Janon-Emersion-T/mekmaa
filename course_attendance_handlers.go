package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CourseAttendancePage struct {
	CSRFToken string
	Division  string
	Date      string
	Today     string
	ProgramID int64
	Programs  []TrainingProgram
	Students  []CourseAttendanceStudent
	Error     string
}

const courseAttendancePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Course Attendance | Mekmaa</title>
<style>
body{font-family:system-ui,sans-serif;background:#f4f7f8;color:#17252a;margin:0}
main{max-width:1150px;margin:32px auto;padding:0 18px}
header,.panel{background:white;border-radius:18px;padding:24px;margin-bottom:20px;box-shadow:0 3px 18px #0000000b}
h1{margin:0 0 10px}
a{color:#087e69}
.filters{display:flex;flex-wrap:wrap;gap:12px;align-items:end}
label{display:flex;flex-direction:column;gap:6px;font-weight:600}
select,input,button{padding:11px;border:1px solid #ccd8dc;border-radius:9px;font:inherit}
button{background:#087e69;color:white;border:0;cursor:pointer}
table{width:100%;border-collapse:collapse}
th,td{padding:12px;text-align:left;border-bottom:1px solid #e6ebed}
th{background:#f5f8f8}
small{color:#65767c}
.error{color:#a32121}
@media(max-width:700px){.table-wrap{overflow-x:auto}}
</style>
</head>
<body>
<main>
<header>
<a href="/admin/attendance">← Existing attendance</a>
<h1>Course Attendance</h1>
<p>Mark attendance for students enrolled in a course, including students without a group.</p>
</header>
<section class="panel">
<form method="get" action="/admin/attendance/course" class="filters">
<input type="hidden" name="division" value="{{.Division}}">
<label>Course
<select name="program_id" required>
<option value="">Select course</option>
{{range .Programs}}
<option value="{{.ID}}" {{if eq .ID $.ProgramID}}selected{{end}}>{{.Name}} — {{.DivisionName}}</option>
{{end}}
</select></label>
<label>Date<input type="date" name="date" value="{{.Date}}" max="{{.Today}}" required></label>
<button type="submit">Load students</button>
</form>
</section>
{{if .Error}}<section class="panel error">{{.Error}}</section>{{end}}
{{if .ProgramID}}
<section class="panel">
<h2>Attendance for {{.Date}}</h2>
<p>{{len .Students}} enrolled students eligible on this date.</p>
{{if .Students}}
<form method="post" action="/admin/attendance/course/save">
<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
<input type="hidden" name="division" value="{{.Division}}">
<input type="hidden" name="program_id" value="{{.ProgramID}}">
<input type="hidden" name="attendance_date" value="{{.Date}}">
<div class="table-wrap">
<table>
<thead><tr><th>Student ID</th><th>Name</th><th>Status</th><th>Note</th></tr></thead>
<tbody>
{{range .Students}}
<tr>
<td>{{.StudentID}}</td>
<td>{{.Name}}</td>
<td>
<select name="status_{{.EnrollmentID}}">
<option value="">Not marked</option>
<option value="present" {{if eq .Status "present"}}selected{{end}}>Present</option>
<option value="absent" {{if eq .Status "absent"}}selected{{end}}>Absent</option>
<option value="late" {{if eq .Status "late"}}selected{{end}}>Late</option>
<option value="excused" {{if eq .Status "excused"}}selected{{end}}>Excused</option>
</select>
</td>
<td><input name="note_{{.EnrollmentID}}" value="{{.Note}}" maxlength="2000"></td>
</tr>
{{end}}
</tbody>
</table>
</div>
<p><small>Only students with a selected status are saved. Previously saved records remain unless updated.</small></p>
<button type="submit">Save attendance</button>
</form>
{{end}}
</section>
{{end}}
</main>
</body>
</html>`

func (a *App) courseAttendanceAdminAllowed(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user, _ := a.currentUser(r.Context())
	if user == nil || !(userHasRole(user, "admin") || userHasRole(user, "superadmin")) {
		http.Error(w, "course attendance requires administrator access", http.StatusForbidden)
		return nil, false
	}
	if _, ok := a.requireOperationalDivisionScope(w, r, user); !ok {
		return nil, false
	}
	return user, true
}

func (a *App) courseAttendanceProgramAllowed(
	w http.ResponseWriter, r *http.Request, user *User, programID int64,
) bool {
	if programID <= 0 {
		http.Error(w, "invalid course", http.StatusBadRequest)
		return false
	}
	var divisionID int64
	err := a.queryRowDB(
		"SELECT division_id FROM training_programs WHERE id = ?",
		programID,
	).Scan(&divisionID)
	if err != nil {
		http.Error(w, "course not found", http.StatusNotFound)
		return false
	}
	return a.requireDivisionAccessForDivision(w, r, user, divisionID)
}

func (a *App) courseAttendancePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := a.courseAttendanceAdminAllowed(w, r)
	if !ok {
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	today := time.Now().Format("2006-01-02")
	if date == "" {
		date = today
	}
	if !validCourseAttendanceDate(date) || date > today {
		http.Error(w, "invalid attendance date", http.StatusBadRequest)
		return
	}
	if err := validateHistoricalEntryDateValue(date, "attendance date"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	programID, _ := strconv.ParseInt(r.URL.Query().Get("program_id"), 10, 64)
	division := strings.TrimSpace(r.URL.Query().Get("division"))
	if division == "" {
		division = "all"
	}

	programs, err := a.listTrainingPrograms(false)
	if err != nil {
		log.Printf("course attendance programs: %v", err)
		http.Error(w, "unable to load courses", http.StatusInternalServerError)
		return
	}

	page := CourseAttendancePage{
		CSRFToken: a.newTemplateData(w, r, user).CSRFToken,
		Division:  division,
		Date:      date,
		Today:     today,
		ProgramID: programID,
	}
	for _, program := range programs {
		if canViewAllDivisions(user) || userCanAccessDivision(user, program.DivisionID) {
			page.Programs = append(page.Programs, program)
		}
	}

	if programID > 0 {
		if !a.courseAttendanceProgramAllowed(w, r, user, programID) {
			return
		}
		page.Students, err = a.courseAttendanceRoster(programID, date)
		if err != nil {
			log.Printf("course attendance roster: %v", err)
			http.Error(w, "unable to load attendance", http.StatusInternalServerError)
			return
		}
	}

	tmpl, err := template.New("course-attendance").Parse(courseAttendancePageHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, page); err != nil {
		log.Printf("course attendance template: %v", err)
	}
}

func (a *App) courseAttendanceSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	user, ok := a.courseAttendanceAdminAllowed(w, r)
	if !ok {
		return
	}
	programID, err := strconv.ParseInt(r.FormValue("program_id"), 10, 64)
	if err != nil || !a.courseAttendanceProgramAllowed(w, r, user, programID) {
		return
	}
	date := strings.TrimSpace(r.FormValue("attendance_date"))
	roster, err := a.courseAttendanceRoster(programID, date)
	if err != nil {
		http.Error(w, "invalid course attendance roster", http.StatusBadRequest)
		return
	}
	var entries []CourseAttendanceEntry
	for _, student := range roster {
		key := strconv.FormatInt(student.EnrollmentID, 10)
		status := strings.TrimSpace(r.FormValue("status_" + key))
		if status == "" {
			continue
		}
		entries = append(entries, CourseAttendanceEntry{
			EnrollmentID: student.EnrollmentID,
			Status:       status,
			Note:         r.FormValue("note_" + key),
		})
	}
	if err := a.saveCourseAttendance(programID, date, entries, user.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := "/admin/attendance/course?program_id=" +
		strconv.FormatInt(programID, 10) + "&date=" + date
	http.Redirect(w, r, target, http.StatusSeeOther)
}
