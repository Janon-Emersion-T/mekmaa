package main

import "database/sql"

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
