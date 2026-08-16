package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
)

type StudentAttendanceHistoryRow struct {
	ID                  int64
	AdmissionID         int64
	AttendanceDate      string
	Status              string
	Note                string
	GroupID             int64
	GroupName           string
	SessionID           int64
	SessionTitle        string
	TrainingProgramName string
	DivisionName        string
}

type StudentAttendanceSummary struct {
	TotalEntries   int
	PresentCount   int
	AbsentCount    int
	LateCount      int
	ExcusedCount   int
	AttendedCount  int
	AttendanceRate float64
}

func attendanceGroupIDs(groups []StudentGroup) []int64 {
	groupIDs := make([]int64, 0, len(groups))

	for _, group := range groups {
		if group.ID > 0 {
			groupIDs = append(groupIDs, group.ID)
		}
	}

	return normalizeScopedDivisionIDs(groupIDs)
}

func admissionVisibleInAttendanceGroups(
	groups []StudentGroup,
	admissionID int64,
) bool {
	if admissionID <= 0 {
		return false
	}

	for _, group := range groups {
		for _, student := range group.Students {
			if student.ID == admissionID {
				return true
			}
		}
	}

	return false
}

func (a *App) listStudentAttendanceHistory(
	admissionID int64,
	groupIDs []int64,
) ([]StudentAttendanceHistoryRow, error) {
	if admissionID <= 0 {
		return nil, sql.ErrNoRows
	}

	placeholders, groupArgs := int64ScopePlaceholders(groupIDs)
	if placeholders == "" {
		return nil, nil
	}

	args := make([]any, 0, 1+len(groupArgs))
	args = append(args, admissionID)
	args = append(args, groupArgs...)

	rows, err := a.db.Query(`
		SELECT
			ar.id,
			ar.admission_id,
			ar.attendance_date,
			ar.status,
			COALESCE(ar.note, ''),
			ar.group_id,
			COALESCE(sg.name, ''),
			COALESCE(ar.session_id, 0),
			COALESCE(sgs.title, ''),
			COALESCE(tp.name, ''),
			COALESCE(d.name, '')
		FROM attendance_records ar
		LEFT JOIN student_groups sg
			ON sg.id = ar.group_id
		LEFT JOIN student_group_sessions sgs
			ON sgs.id = ar.session_id
		LEFT JOIN training_programs tp
			ON tp.id = sg.training_program_id
		LEFT JOIN divisions d
			ON d.id = tp.division_id
		WHERE ar.admission_id = ?
		  AND ar.group_id IN (`+placeholders+`)
		ORDER BY
			ar.attendance_date DESC,
			COALESCE(sgs.start_time, '') DESC,
			ar.id DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history := make([]StudentAttendanceHistoryRow, 0)

	for rows.Next() {
		var row StudentAttendanceHistoryRow

		if err := rows.Scan(
			&row.ID,
			&row.AdmissionID,
			&row.AttendanceDate,
			&row.Status,
			&row.Note,
			&row.GroupID,
			&row.GroupName,
			&row.SessionID,
			&row.SessionTitle,
			&row.TrainingProgramName,
			&row.DivisionName,
		); err != nil {
			return nil, err
		}

		history = append(history, row)
	}

	return history, rows.Err()
}

func summarizeStudentAttendanceHistory(
	history []StudentAttendanceHistoryRow,
) StudentAttendanceSummary {
	var summary StudentAttendanceSummary

	for _, row := range history {
		summary.TotalEntries++

		switch row.Status {
		case "present":
			summary.PresentCount++
			summary.AttendedCount++

		case "absent":
			summary.AbsentCount++

		case "late":
			summary.LateCount++
			summary.AttendedCount++

		case "excused":
			summary.ExcusedCount++
		}
	}

	// Excused sessions are intentionally removed from the attendance-rate
	// denominator because they are approved non-attendance rather than absence.
	eligibleSessions :=
		summary.PresentCount +
			summary.AbsentCount +
			summary.LateCount

	if eligibleSessions > 0 {
		summary.AttendanceRate =
			(float64(summary.AttendedCount) / float64(eligibleSessions)) * 100
	}

	return summary
}

func (a *App) loadStudentAttendanceView(
	groups []StudentGroup,
	admissionID int64,
) (
	*Admission,
	[]StudentAttendanceHistoryRow,
	StudentAttendanceSummary,
	error,
) {
	if !admissionVisibleInAttendanceGroups(groups, admissionID) {
		return nil, nil, StudentAttendanceSummary{}, sql.ErrNoRows
	}

	student, err := a.findAdmissionIdentityByID(admissionID)
	if err != nil {
		return nil, nil, StudentAttendanceSummary{}, err
	}

	history, err := a.listStudentAttendanceHistory(
		admissionID,
		attendanceGroupIDs(groups),
	)
	if err != nil {
		return nil, nil, StudentAttendanceSummary{}, err
	}

	return student, history, summarizeStudentAttendanceHistory(history), nil
}

type AttendanceSheetSummary struct {
	GroupID             int64
	GroupName           string
	GroupCode           string
	SessionID           int64
	SessionTitle        string
	AttendanceDate      string
	TrainingProgramName string
	DivisionName        string
	EntryCount          int
	PresentCount        int
	AbsentCount         int
	LateCount           int
	ExcusedCount        int
}

func (a *App) attendanceGroupsForUser(
	w http.ResponseWriter,
	r *http.Request,
	user *User,
) ([]StudentGroup, bool) {
	divisionIDs := []int64(nil)

	if !canViewAllDivisions(user) {
		var ok bool
		divisionIDs, ok = a.requireOperationalDivisionScope(
			w,
			r,
			user,
		)
		if !ok {
			return nil, false
		}
	}

	var (
		groups []StudentGroup
		err    error
	)

	if userHasRole(user, "coach") &&
		!userHasRole(user, "admin") &&
		!userHasRole(user, "superadmin") {
		groups, err = a.listStudentGroupsForCoachByDivisionIDs(
			user.ID,
			divisionIDs,
		)
	} else {
		groups, err = a.listStudentGroupsByDivisionIDs(
			divisionIDs,
		)
	}

	if err != nil {
		log.Printf(
			"list student groups for attendance scope: %v",
			err,
		)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return nil, false
	}

	return groups, true
}

func findAttendanceStudentByStudentID(
	groups []StudentGroup,
	studentID string,
) *Admission {
	studentID = strings.TrimSpace(studentID)
	if studentID == "" {
		return nil
	}

	seen := make(map[int64]struct{})

	for _, group := range groups {
		for i := range group.Students {
			student := &group.Students[i]

			if student.ID <= 0 {
				continue
			}

			if _, exists := seen[student.ID]; exists {
				continue
			}
			seen[student.ID] = struct{}{}

			if strings.EqualFold(
				strings.TrimSpace(student.StudentID),
				studentID,
			) {
				copyStudent := *student
				return &copyStudent
			}
		}
	}

	return nil
}

func (a *App) listAttendanceSheets(
	groupIDs []int64,
	limit int,
) ([]AttendanceSheetSummary, error) {
	if limit <= 0 {
		limit = 30
	}

	if limit > 200 {
		limit = 200
	}

	placeholders, args := int64ScopePlaceholders(groupIDs)
	if placeholders == "" {
		return nil, nil
	}

	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)

	rows, err := a.db.Query(`
		SELECT
			ar.group_id,
			COALESCE(sg.name, ''),
			COALESCE(sg.code, ''),
			COALESCE(ar.session_id, 0),
			COALESCE(sgs.title, ''),
			ar.attendance_date,
			COALESCE(tp.name, ''),
			COALESCE(d.name, ''),
			COUNT(*),
			COALESCE(SUM(
				CASE WHEN ar.status = 'present' THEN 1 ELSE 0 END
			), 0),
			COALESCE(SUM(
				CASE WHEN ar.status = 'absent' THEN 1 ELSE 0 END
			), 0),
			COALESCE(SUM(
				CASE WHEN ar.status = 'late' THEN 1 ELSE 0 END
			), 0),
			COALESCE(SUM(
				CASE WHEN ar.status = 'excused' THEN 1 ELSE 0 END
			), 0)
		FROM attendance_records ar
		LEFT JOIN student_groups sg
			ON sg.id = ar.group_id
		LEFT JOIN student_group_sessions sgs
			ON sgs.id = ar.session_id
		LEFT JOIN training_programs tp
			ON tp.id = sg.training_program_id
		LEFT JOIN divisions d
			ON d.id = tp.division_id
		WHERE ar.group_id IN (`+placeholders+`)
		GROUP BY
			ar.group_id,
			sg.name,
			sg.code,
			COALESCE(ar.session_id, 0),
			sgs.title,
			ar.attendance_date,
			tp.name,
			d.name
		ORDER BY
			ar.attendance_date DESC,
			ar.group_id ASC,
			COALESCE(ar.session_id, 0) ASC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sheets := make([]AttendanceSheetSummary, 0)

	for rows.Next() {
		var sheet AttendanceSheetSummary

		if err := rows.Scan(
			&sheet.GroupID,
			&sheet.GroupName,
			&sheet.GroupCode,
			&sheet.SessionID,
			&sheet.SessionTitle,
			&sheet.AttendanceDate,
			&sheet.TrainingProgramName,
			&sheet.DivisionName,
			&sheet.EntryCount,
			&sheet.PresentCount,
			&sheet.AbsentCount,
			&sheet.LateCount,
			&sheet.ExcusedCount,
		); err != nil {
			return nil, err
		}

		sheets = append(sheets, sheet)
	}

	return sheets, rows.Err()
}

func (a *App) attendanceSearchHandler(
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

	groups, ok := a.attendanceGroupsForUser(
		w,
		r,
		user,
	)
	if !ok {
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Search Attendance"
	data.Description = "Search student attendance by Student ID."
	data.StudentGroups = groups

	studentID := strings.TrimSpace(
		r.URL.Query().Get("student_id"),
	)
	data.AttendanceSearchStudentID = studentID

	if studentID != "" {
		student := findAttendanceStudentByStudentID(
			groups,
			studentID,
		)

		if student == nil {
			data.AttendanceSearchNotFound = true
		} else {
			selectedStudent,
				history,
				summary,
				err := a.loadStudentAttendanceView(
				groups,
				student.ID,
			)

			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					data.AttendanceSearchNotFound = true
				} else {
					log.Printf(
						"load searched student attendance: %v",
						err,
					)
					http.Error(
						w,
						"internal server error",
						http.StatusInternalServerError,
					)
					return
				}
			} else {
				data.SelectedAttendanceStudent =
					selectedStudent
				data.StudentAttendanceHistory =
					history
				data.StudentAttendanceSummary =
					summary
			}
		}
	}

	a.render(
		w,
		"attendance-search",
		data,
		http.StatusOK,
	)
}
