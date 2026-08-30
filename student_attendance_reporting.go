package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type StudentAttendanceReportRow struct {
	Admission            Admission
	GroupID              int64
	GroupName            string
	GroupCode            string
	TrainingProgramName  string
	PresentCount         int
	AbsentCount          int
	LateCount            int
	ExcusedCount         int
	TotalEntries         int
	EligibleSessions     int
	AttendedCount        int
	AttendancePercentage float64
}

type StudentAttendanceGroupReportRow struct {
	GroupID              int64
	GroupName            string
	GroupCode            string
	TrainingProgramName  string
	StudentCount         int
	PresentCount         int
	AbsentCount          int
	LateCount            int
	ExcusedCount         int
	TotalEntries         int
	EligibleSessions     int
	AttendedCount        int
	AttendancePercentage float64
}

type StudentAttendanceReportSummary struct {
	StudentCount         int
	GroupCount           int
	PresentCount         int
	AbsentCount          int
	LateCount            int
	ExcusedCount         int
	TotalEntries         int
	EligibleSessions     int
	AttendedCount        int
	AttendancePercentage float64
}

func normalizeStudentAttendanceMonth(
	value string,
) (string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return "", errors.New(
			"attendance month is required",
		)
	}

	parsed, err := time.Parse(
		"2006-01",
		value,
	)
	if err != nil {
		return "", errors.New(
			"invalid attendance month",
		)
	}

	normalized :=
		parsed.Format("2006-01")

	currentMonth :=
		time.Now().Format("2006-01")

	if normalized > currentMonth {
		return "", errors.New(
			"future attendance months cannot be reported",
		)
	}

	return normalized, nil
}

func studentAttendanceMonthBounds(
	month string,
) (string, string, error) {
	month, err :=
		normalizeStudentAttendanceMonth(
			month,
		)
	if err != nil {
		return "", "", err
	}

	start, err := time.Parse(
		"2006-01-02",
		month+"-01",
	)
	if err != nil {
		return "", "", err
	}

	end :=
		start.AddDate(
			0,
			1,
			0,
		)

	return start.Format("2006-01-02"),
		end.Format("2006-01-02"),
		nil
}

func (a *App) listStudentAttendanceHistoryForMonth(
	groupIDs []int64,
	month string,
) ([]StudentAttendanceHistoryRow, error) {
	groupIDs =
		uniquePositiveInt64Values(
			groupIDs,
		)

	if len(groupIDs) == 0 {
		return nil, nil
	}

	startDate, endDate, err :=
		studentAttendanceMonthBounds(
			month,
		)
	if err != nil {
		return nil, err
	}

	placeholders, groupArgs :=
		int64ScopePlaceholders(
			groupIDs,
		)

	if placeholders == "" {
		return nil, nil
	}

	args := make(
		[]any,
		0,
		len(groupArgs)+2,
	)

	args = append(
		args,
		startDate,
		endDate,
	)

	args = append(
		args,
		groupArgs...,
	)

	rows, err := a.db.Query(`
		SELECT
			ar.id,
			ar.admission_id,
			ar.attendance_date,
			COALESCE(ar.status, ''),
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
		WHERE ar.attendance_date >= ?
		  AND ar.attendance_date < ?
		  AND ar.group_id IN (`+placeholders+`)
		ORDER BY
			ar.attendance_date DESC,
			ar.group_id ASC,
			ar.admission_id ASC,
			ar.id DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history :=
		make(
			[]StudentAttendanceHistoryRow,
			0,
		)

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

		history = append(
			history,
			row,
		)
	}

	return history, rows.Err()
}

type studentAttendanceReportKey struct {
	GroupID     int64
	AdmissionID int64
}

func buildStudentAttendanceReportRows(
	groups []StudentGroup,
	history []StudentAttendanceHistoryRow,
) []StudentAttendanceReportRow {
	rows :=
		make(
			[]StudentAttendanceReportRow,
			0,
		)

	indexByKey :=
		make(
			map[studentAttendanceReportKey]int,
		)

	for _, group := range groups {
		if group.ID <= 0 {
			continue
		}

		for _, student := range group.Students {
			if student.ID <= 0 {
				continue
			}

			key :=
				studentAttendanceReportKey{
					GroupID:     group.ID,
					AdmissionID: student.ID,
				}

			if _, exists :=
				indexByKey[key]; exists {
				continue
			}

			indexByKey[key] =
				len(rows)

			rows = append(
				rows,
				StudentAttendanceReportRow{
					Admission:           student,
					GroupID:             group.ID,
					GroupName:           group.Name,
					GroupCode:           group.Code,
					TrainingProgramName: group.TrainingProgramName,
				},
			)
		}
	}

	for _, record := range history {
		key :=
			studentAttendanceReportKey{
				GroupID:     record.GroupID,
				AdmissionID: record.AdmissionID,
			}

		index, ok :=
			indexByKey[key]

		if !ok {
			continue
		}

		row := &rows[index]

		status :=
			strings.ToLower(
				strings.TrimSpace(
					record.Status,
				),
			)

		switch status {
		case "present":
			row.PresentCount++
			row.AttendedCount++
			row.EligibleSessions++

		case "late":
			row.LateCount++
			row.AttendedCount++
			row.EligibleSessions++

		case "absent":
			row.AbsentCount++
			row.EligibleSessions++

		case "excused":
			row.ExcusedCount++
		}

		row.TotalEntries++
	}

	for i := range rows {
		if rows[i].EligibleSessions <= 0 {
			continue
		}

		rows[i].AttendancePercentage =
			float64(
				rows[i].AttendedCount,
			) /
				float64(
					rows[i].EligibleSessions,
				) *
				100
	}

	return rows
}

func buildStudentAttendanceGroupReportRows(
	groups []StudentGroup,
	rows []StudentAttendanceReportRow,
) []StudentAttendanceGroupReportRow {
	groupRows :=
		make(
			[]StudentAttendanceGroupReportRow,
			0,
			len(groups),
		)

	indexByGroupID :=
		make(
			map[int64]int,
			len(groups),
		)

	for _, group := range groups {
		if group.ID <= 0 {
			continue
		}

		if _, exists :=
			indexByGroupID[group.ID]; exists {
			continue
		}

		indexByGroupID[group.ID] =
			len(groupRows)

		groupRows = append(
			groupRows,
			StudentAttendanceGroupReportRow{
				GroupID:             group.ID,
				GroupName:           group.Name,
				GroupCode:           group.Code,
				TrainingProgramName: group.TrainingProgramName,
			},
		)
	}

	for _, row := range rows {
		index, ok :=
			indexByGroupID[row.GroupID]

		if !ok {
			continue
		}

		groupRow :=
			&groupRows[index]

		groupRow.StudentCount++
		groupRow.PresentCount +=
			row.PresentCount
		groupRow.AbsentCount +=
			row.AbsentCount
		groupRow.LateCount +=
			row.LateCount
		groupRow.ExcusedCount +=
			row.ExcusedCount
		groupRow.TotalEntries +=
			row.TotalEntries
		groupRow.EligibleSessions +=
			row.EligibleSessions
		groupRow.AttendedCount +=
			row.AttendedCount
	}

	for i := range groupRows {
		if groupRows[i].EligibleSessions <= 0 {
			continue
		}

		groupRows[i].AttendancePercentage =
			float64(
				groupRows[i].AttendedCount,
			) /
				float64(
					groupRows[i].EligibleSessions,
				) *
				100
	}

	return groupRows
}

func summarizeStudentAttendanceReport(
	rows []StudentAttendanceReportRow,
	groupRows []StudentAttendanceGroupReportRow,
) StudentAttendanceReportSummary {
	var summary StudentAttendanceReportSummary

	summary.StudentCount =
		len(rows)

	summary.GroupCount =
		len(groupRows)

	for _, row := range rows {
		summary.PresentCount +=
			row.PresentCount
		summary.AbsentCount +=
			row.AbsentCount
		summary.LateCount +=
			row.LateCount
		summary.ExcusedCount +=
			row.ExcusedCount
		summary.TotalEntries +=
			row.TotalEntries
		summary.EligibleSessions +=
			row.EligibleSessions
		summary.AttendedCount +=
			row.AttendedCount
	}

	if summary.EligibleSessions > 0 {
		summary.AttendancePercentage =
			float64(
				summary.AttendedCount,
			) /
				float64(
					summary.EligibleSessions,
				) *
				100
	}

	return summary
}

func filterStudentAttendanceReportRows(
	rows []StudentAttendanceReportRow,
	groupID int64,
	studentQuery string,
) []StudentAttendanceReportRow {
	studentQuery =
		strings.ToLower(
			strings.TrimSpace(
				studentQuery,
			),
		)

	filtered :=
		make(
			[]StudentAttendanceReportRow,
			0,
			len(rows),
		)

	for _, row := range rows {
		if groupID > 0 &&
			row.GroupID != groupID {
			continue
		}

		if studentQuery != "" {
			studentID :=
				strings.ToLower(
					strings.TrimSpace(
						row.Admission.StudentID,
					),
				)

			fullName :=
				strings.ToLower(
					strings.TrimSpace(
						row.Admission.FullName,
					),
				)

			if !strings.Contains(
				studentID,
				studentQuery,
			) &&
				!strings.Contains(
					fullName,
					studentQuery,
				) {
				continue
			}
		}

		filtered = append(
			filtered,
			row,
		)
	}

	return filtered
}

func studentAttendanceGroupInScope(
	groups []StudentGroup,
	groupID int64,
) bool {
	if groupID <= 0 {
		return true
	}

	for _, group := range groups {
		if group.ID == groupID {
			return true
		}
	}

	return false
}

func writeStudentAttendanceReportCSV(
	w http.ResponseWriter,
	month string,
	groupName string,
	studentQuery string,
	rows []StudentAttendanceReportRow,
) error {
	filename :=
		fmt.Sprintf(
			"student-attendance-%s.csv",
			month,
		)

	writer := newCSVReportWriter(
		w,
		filename,
	)

	defer writer.Flush()

	if err := writeCSVReportPreamble(
		writer,
		"Mekmaa Student Attendance Report",
		CSVReportMetaRow{
			Section: "report",
			Field:   "Month",
			Value:   month,
		},
		CSVReportMetaRow{
			Section: "filter",
			Field:   "Group",
			Value:   fallbackReportValue(groupName, "All groups"),
		},
		CSVReportMetaRow{
			Section: "filter",
			Field:   "Student",
			Value:   fallbackReportValue(studentQuery, "All students"),
		},
		CSVReportMetaRow{
			Section: "report",
			Field:   "Rows",
			Value:   strconv.Itoa(len(rows)),
		},
	); err != nil {
		return err
	}

	if err := writer.Write(
		[]string{
			"Student ID",
			"Student",
			"Group / Class / Batch",
			"Programme",
			"Present",
			"Absent",
			"Late",
			"Excused",
			"Eligible Sessions",
			"Attended",
			"Attendance Percentage",
		},
	); err != nil {
		return err
	}

	for _, row := range rows {
		if err := writer.Write(
			[]string{
				row.Admission.StudentID,
				row.Admission.FullName,
				row.GroupName,
				row.TrainingProgramName,
				strconv.Itoa(
					row.PresentCount,
				),
				strconv.Itoa(
					row.AbsentCount,
				),
				strconv.Itoa(
					row.LateCount,
				),
				strconv.Itoa(
					row.ExcusedCount,
				),
				strconv.Itoa(
					row.EligibleSessions,
				),
				strconv.Itoa(
					row.AttendedCount,
				),
				fmt.Sprintf(
					"%.2f",
					row.AttendancePercentage,
				),
			},
		); err != nil {
			return err
		}
	}

	writer.Flush()

	return writer.Error()
}

func (a *App) studentAttendanceReportHandler(
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

	user, _ :=
		a.currentUser(
			r.Context(),
		)

	groups, ok :=
		a.attendanceGroupsForUser(
			w,
			r,
			user,
		)
	if !ok {
		return
	}

	month :=
		strings.TrimSpace(
			r.URL.Query().Get(
				"month",
			),
		)

	if month == "" {
		month =
			time.Now().
				Format("2006-01")
	}

	var err error

	month, err =
		normalizeStudentAttendanceMonth(
			month,
		)
	if err != nil {
		month =
			time.Now().
				Format("2006-01")
	}

	groupID := int64(0)

	if raw :=
		strings.TrimSpace(
			r.URL.Query().Get(
				"group_id",
			),
		); raw != "" {

		parsed, parseErr :=
			strconv.ParseInt(
				raw,
				10,
				64,
			)

		if parseErr == nil &&
			parsed > 0 {
			groupID = parsed
		}
	}

	if !studentAttendanceGroupInScope(
		groups,
		groupID,
	) {
		http.Error(
			w,
			"group is outside the permitted attendance scope",
			http.StatusForbidden,
		)
		return
	}

	groupIDs :=
		attendanceGroupIDs(
			groups,
		)

	history, err :=
		a.listStudentAttendanceHistoryForMonth(
			groupIDs,
			month,
		)
	if err != nil {
		log.Printf(
			"list monthly student attendance history: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	allRows :=
		buildStudentAttendanceReportRows(
			groups,
			history,
		)

	studentQuery :=
		strings.TrimSpace(
			r.URL.Query().Get(
				"student",
			),
		)

	rows :=
		filterStudentAttendanceReportRows(
			allRows,
			groupID,
			studentQuery,
		)

	reportGroups := groups

	if groupID > 0 {
		reportGroups =
			make(
				[]StudentGroup,
				0,
				1,
			)

		for _, group := range groups {
			if group.ID == groupID {
				reportGroups =
					append(
						reportGroups,
						group,
					)
				break
			}
		}
	}

	groupRows :=
		buildStudentAttendanceGroupReportRows(
			reportGroups,
			rows,
		)

	summary :=
		summarizeStudentAttendanceReport(
			rows,
			groupRows,
		)

	if strings.EqualFold(
		strings.TrimSpace(
			r.URL.Query().Get(
				"format",
			),
		),
		"csv",
	) {
		selectedGroupName := ""
		if groupID > 0 {
			for _, group := range groups {
				if group.ID == groupID {
					selectedGroupName = group.Name
					break
				}
			}
		}
		if err :=
			writeStudentAttendanceReportCSV(
				w,
				month,
				selectedGroupName,
				studentQuery,
				rows,
			); err != nil {

			log.Printf(
				"write student attendance csv: %v",
				err,
			)
		}

		return
	}

	data :=
		a.newTemplateData(
			w,
			r,
			user,
		)

	data.Title =
		"Student Attendance Report"

	data.Description =
		"Review monthly attendance by student and group."

	data.StudentGroups =
		groups

	data.StudentAttendanceReportRows =
		rows

	data.StudentAttendanceGroupReportRows =
		groupRows

	data.StudentAttendanceReportSummary =
		summary

	data.StudentAttendanceReportMonth =
		month

	data.StudentAttendanceReportGroupID =
		groupID

	data.StudentAttendanceReportQuery =
		studentQuery

	a.render(
		w,
		"student-attendance-report",
		data,
		http.StatusOK,
	)
}
