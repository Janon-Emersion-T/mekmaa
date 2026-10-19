package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
)

func courseSearchStudents(enrollments []StudentEnrollment, query string) []Admission {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	seen := map[int64]bool{}
	var matches []Admission
	for _, enrollment := range enrollments {
		student := enrollment.Student
		if student.ID <= 0 || seen[student.ID] {
			continue
		}
		seen[student.ID] = true
		if strings.EqualFold(student.StudentID, query) {
			return []Admission{student}
		}
		if strings.Contains(strings.ToLower(student.FullName), strings.ToLower(query)) {
			matches = append(matches, student)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].FullName < matches[j].FullName
	})
	return matches
}

func courseSearchSuggestions(enrollments []StudentEnrollment) []AttendanceStudentSuggestion {
	seen := map[int64]bool{}
	var result []AttendanceStudentSuggestion
	for _, enrollment := range enrollments {
		student := enrollment.Student
		if student.ID <= 0 || seen[student.ID] || student.StudentID == "" {
			continue
		}
		seen[student.ID] = true
		result = append(result, AttendanceStudentSuggestion{
			StudentID: student.StudentID,
			FullName:  student.FullName,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FullName == result[j].FullName {
			return result[i].StudentID < result[j].StudentID
		}
		return result[i].FullName < result[j].FullName
	})
	return result
}

func (a *App) listCourseSearchHistory(
	admissionID int64,
	allowedPrograms map[int64]bool,
	filter AttendanceSearchFilter,
) ([]StudentAttendanceHistoryRow, error) {
	if admissionID <= 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := a.queryDB(`
		SELECT car.id, se.admission_id, car.attendance_date,
		       car.status, COALESCE(car.note, ''),
		       tp.id, COALESCE(tp.name, ''), COALESCE(d.name, '')
		FROM course_attendance_records car
		JOIN student_enrollments se ON se.id = car.enrollment_id
		JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN divisions d ON d.id = tp.division_id
		WHERE se.admission_id = ?
		ORDER BY car.attendance_date DESC, car.id DESC
	`, admissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history := make([]StudentAttendanceHistoryRow, 0)
	for rows.Next() {
		var row StudentAttendanceHistoryRow
		var programID int64
		var programName, divisionName string
		if err := rows.Scan(
			&row.ID, &row.AdmissionID, &row.AttendanceDate,
			&row.Status, &row.Note, &programID,
			&programName, &divisionName,
		); err != nil {
			return nil, err
		}
		if !allowedPrograms[programID] {
			continue
		}
		if filter.ProgrammeID != 0 && programID != filter.ProgrammeID {
			continue
		}
		if filter.From != "" && row.AttendanceDate < filter.From {
			continue
		}
		if filter.To != "" && row.AttendanceDate > filter.To {
			continue
		}
		if filter.Status != "" && row.Status != filter.Status {
			continue
		}
		row.TrainingProgramName = programName
		row.DivisionName = divisionName
		history = append(history, row)
	}
	return history, rows.Err()
}

func (a *App) attendanceSearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	if user == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	var divisionIDs []int64
	if !canViewAllDivisions(user) {
		var ok bool
		divisionIDs, ok = a.requireOperationalDivisionScope(w, r, user)
		if !ok {
			return
		}
	}

	enrollments, err := a.listStudentEnrollmentsByDivisionIDs(divisionIDs)
	if err != nil {
		log.Printf("course attendance search enrollments: %v", err)
		http.Error(w, "unable to load students", http.StatusInternalServerError)
		return
	}

	filter, err := parseAttendanceSearchFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if filter.GroupID != 0 {
		http.Error(w, "Group filtering is no longer supported. Select a course.", http.StatusBadRequest)
		return
	}

	allowedPrograms := map[int64]bool{}
	programs := []TrainingProgram{}
	for _, enrollment := range enrollments {
		id := enrollment.TrainingProgramID
		if id <= 0 || allowedPrograms[id] {
			continue
		}
		allowedPrograms[id] = true
		programs = append(programs, TrainingProgram{
			ID: id, Name: enrollment.TrainingProgramName,
		})
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].Name < programs[j].Name
	})
	if filter.ProgrammeID != 0 && !allowedPrograms[filter.ProgrammeID] {
		http.Error(w, "Course unavailable in this workspace.", http.StatusBadRequest)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.HideChrome = strings.EqualFold(r.URL.Query().Get("format"), "pdf")
	data.Title = "Search Course Attendance"
	data.Description = "Search saved student attendance by course."
	data.TrainingPrograms = programs
	data.AttendanceSearchFilter = filter
	if !data.HideChrome {
		data.AttendanceSearchSuggestions = courseSearchSuggestions(enrollments)
	}

	query := strings.TrimSpace(r.URL.Query().Get("student_id"))
	data.AttendanceSearchStudentID = query
	if query != "" {
		matches := courseSearchStudents(enrollments, query)
		switch len(matches) {
		case 0:
			data.AttendanceSearchNotFound = true
		case 1:
			student := matches[0]
			selected, err := a.findAdmissionIdentityByID(student.ID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					data.AttendanceSearchNotFound = true
					break
				}
				log.Printf("course attendance search student: %v", err)
				http.Error(w, "unable to load student", http.StatusInternalServerError)
				return
			}
			history, err := a.listCourseSearchHistory(student.ID, allowedPrograms, filter)
			if err != nil {
				log.Printf("course attendance search history: %v", err)
				http.Error(w, "unable to load attendance", http.StatusInternalServerError)
				return
			}
			data.SelectedAttendanceStudent = selected
			data.StudentAttendanceHistory = history
			data.StudentAttendanceSummary = summarizeStudentAttendanceHistory(history)
		default:
			data.AttendanceSearchMatches = matches
		}
	}
	a.render(w, "attendance-search", data, http.StatusOK)
}
