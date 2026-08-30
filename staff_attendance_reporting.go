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

type StaffAttendanceReportRow struct {
	User                 User
	PresentCount         int
	AbsentCount          int
	LateCount            int
	ExcusedCount         int
	TotalRecords         int
	CountedDays          int
	AttendedDays         int
	AttendancePercentage float64
}

type StaffAttendanceReportSummary struct {
	StaffCount           int
	PresentCount         int
	AbsentCount          int
	LateCount            int
	ExcusedCount         int
	TotalRecords         int
	CountedDays          int
	AttendedDays         int
	AttendancePercentage float64
}

type StaffAttendanceHistoryRow struct {
	ID               int64
	UserID           int64
	AttendanceDate   string
	Status           string
	Note             string
	RecordedByUserID int64
	RecordedAt       time.Time
	UpdatedAt        time.Time
}

func normalizeStaffAttendanceMonth(
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

	currentMonth := time.Now().
		Format("2006-01")

	normalized := parsed.Format(
		"2006-01",
	)

	if normalized > currentMonth {
		return "", errors.New(
			"future attendance months cannot be reported",
		)
	}

	return normalized, nil
}

func staffAttendanceMonthBounds(
	month string,
) (string, string, error) {
	month, err :=
		normalizeStaffAttendanceMonth(
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

	end := start.AddDate(
		0,
		1,
		0,
	)

	return start.Format("2006-01-02"),
		end.Format("2006-01-02"),
		nil
}

func (a *App) listStaffAttendanceRecordsForMonthByUserIDs(
	month string,
	userIDs []int64,
) ([]CoachAttendanceRecord, error) {
	startDate, endDate, err :=
		staffAttendanceMonthBounds(
			month,
		)
	if err != nil {
		return nil, err
	}

	userIDs =
		uniquePositiveInt64Values(
			userIDs,
		)

	if len(userIDs) == 0 {
		return nil, nil
	}

	placeholders, args :=
		int64ScopePlaceholders(
			userIDs,
		)

	if placeholders == "" {
		return nil, nil
	}

	queryArgs := make(
		[]any,
		0,
		len(args)+2,
	)

	queryArgs = append(
		queryArgs,
		startDate,
		endDate,
	)

	queryArgs = append(
		queryArgs,
		args...,
	)

	rows, err := a.queryDB(`
		SELECT
			id,
			user_id,
			attendance_date,
			COALESCE(status, ''),
			COALESCE(note, ''),
			COALESCE(recorded_by_user_id, 0),
			recorded_at,
			updated_at
		FROM coach_attendance_records
		WHERE attendance_date >= ?
		  AND attendance_date < ?
		  AND user_id IN (`+placeholders+`)
		ORDER BY
			attendance_date DESC,
			user_id ASC,
			id DESC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records :=
		make(
			[]CoachAttendanceRecord,
			0,
		)

	for rows.Next() {
		var record CoachAttendanceRecord

		if err := rows.Scan(
			&record.ID,
			&record.UserID,
			&record.AttendanceDate,
			&record.Status,
			&record.Note,
			&record.RecordedByUserID,
			&record.RecordedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}

		records = append(
			records,
			record,
		)
	}

	return records, rows.Err()
}

func buildStaffAttendanceReportRows(
	users []User,
	records []CoachAttendanceRecord,
) []StaffAttendanceReportRow {
	rows := make(
		[]StaffAttendanceReportRow,
		0,
		len(users),
	)

	indexByUserID :=
		make(
			map[int64]int,
			len(users),
		)

	for _, user := range users {
		if user.ID <= 0 ||
			!user.Active {
			continue
		}

		indexByUserID[user.ID] =
			len(rows)

		rows = append(
			rows,
			StaffAttendanceReportRow{
				User: user,
			},
		)
	}

	for _, record := range records {
		index, ok :=
			indexByUserID[record.UserID]

		if !ok {
			continue
		}

		row := &rows[index]

		switch normalizeStaffAttendanceStatus(
			record.Status,
		) {
		case "present":
			row.PresentCount++
			row.AttendedDays++
			row.CountedDays++

		case "late":
			row.LateCount++
			row.AttendedDays++
			row.CountedDays++

		case "absent":
			row.AbsentCount++
			row.CountedDays++

		case "excused":
			row.ExcusedCount++
		}

		row.TotalRecords++
	}

	for i := range rows {
		if rows[i].CountedDays > 0 {
			rows[i].AttendancePercentage =
				float64(
					rows[i].AttendedDays,
				) /
					float64(
						rows[i].CountedDays,
					) *
					100
		}
	}

	return rows
}

func summarizeStaffAttendanceReport(
	rows []StaffAttendanceReportRow,
) StaffAttendanceReportSummary {
	var summary StaffAttendanceReportSummary

	summary.StaffCount =
		len(rows)

	for _, row := range rows {
		summary.PresentCount +=
			row.PresentCount

		summary.AbsentCount +=
			row.AbsentCount

		summary.LateCount +=
			row.LateCount

		summary.ExcusedCount +=
			row.ExcusedCount

		summary.TotalRecords +=
			row.TotalRecords

		summary.CountedDays +=
			row.CountedDays

		summary.AttendedDays +=
			row.AttendedDays
	}

	if summary.CountedDays > 0 {
		summary.AttendancePercentage =
			float64(
				summary.AttendedDays,
			) /
				float64(
					summary.CountedDays,
				) *
				100
	}

	return summary
}

func staffAttendanceHistoryForUser(
	records []CoachAttendanceRecord,
	userID int64,
) []StaffAttendanceHistoryRow {
	history := make(
		[]StaffAttendanceHistoryRow,
		0,
	)

	for _, record := range records {
		if record.UserID != userID {
			continue
		}

		history = append(
			history,
			StaffAttendanceHistoryRow{
				ID:             record.ID,
				UserID:         record.UserID,
				AttendanceDate: record.AttendanceDate,
				Status: normalizeStaffAttendanceStatus(
					record.Status,
				),
				Note:             record.Note,
				RecordedByUserID: record.RecordedByUserID,
				RecordedAt:       record.RecordedAt,
				UpdatedAt:        record.UpdatedAt,
			},
		)
	}

	return history
}

func staffAttendanceUserByID(
	users []User,
	userID int64,
) *User {
	for i := range users {
		if users[i].ID == userID {
			return &users[i]
		}
	}

	return nil
}

func staffAttendanceReportURL(
	month string,
	division string,
	userID int64,
	format string,
) string {
	target :=
		"/admin/staff/attendance/report"

	if strings.TrimSpace(month) != "" {
		target =
			withQueryValue(
				target,
				"month",
				strings.TrimSpace(
					month,
				),
			)
	}

	if strings.TrimSpace(division) != "" {
		target =
			withQueryValue(
				target,
				"division",
				strings.TrimSpace(
					division,
				),
			)
	}

	if userID > 0 {
		target =
			withQueryValue(
				target,
				"user_id",
				strconv.FormatInt(
					userID,
					10,
				),
			)
	}

	if strings.TrimSpace(format) != "" {
		target =
			withQueryValue(
				target,
				"format",
				strings.TrimSpace(
					format,
				),
			)
	}

	return target
}

func writeStaffAttendanceReportCSV(
	w http.ResponseWriter,
	month string,
	userName string,
	rows []StaffAttendanceReportRow,
) error {
	filename := fmt.Sprintf(
		"staff-attendance-%s.csv",
		month,
	)

	writer := newCSVReportWriter(
		w,
		filename,
	)
	defer writer.Flush()

	if err := writeCSVReportPreamble(
		writer,
		"Mekmaa Staff Attendance Report",
		CSVReportMetaRow{
			Section: "report",
			Field:   "Month",
			Value:   month,
		},
		CSVReportMetaRow{
			Section: "filter",
			Field:   "Staff Member",
			Value:   fallbackReportValue(userName, "All staff"),
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
			"Staff",
			"Email",
			"Present",
			"Absent",
			"Late",
			"Excused",
			"Total Records",
			"Counted Days",
			"Attended Days",
			"Attendance Percentage",
		},
	); err != nil {
		return err
	}

	for _, row := range rows {
		if err := writer.Write(
			[]string{
				row.User.Name,
				row.User.Email,
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
					row.TotalRecords,
				),
				strconv.Itoa(
					row.CountedDays,
				),
				strconv.Itoa(
					row.AttendedDays,
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

func (a *App) staffAttendanceReportHandler(
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

	currentUser, _ :=
		a.currentUser(
			r.Context(),
		)

	divisionIDs, ok :=
		a.studentGroupDivisionScope(
			w,
			r,
			currentUser,
		)
	if !ok {
		return
	}

	staff, err :=
		a.listAssignableGroupStaffByDivisionIDs(
			divisionIDs,
		)
	if err != nil {
		log.Printf(
			"list staff attendance report users: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if err := a.hydrateStaffDirectoryUserDivisions(
		staff,
	); err != nil {
		log.Printf(
			"hydrate staff attendance report divisions: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
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

	month, err =
		normalizeStaffAttendanceMonth(
			month,
		)
	if err != nil {
		month =
			time.Now().
				Format("2006-01")
	}

	records, err :=
		a.listStaffAttendanceRecordsForMonthByUserIDs(
			month,
			staffAttendanceUserIDs(
				staff,
			),
		)
	if err != nil {
		log.Printf(
			"list monthly staff attendance: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	reportRows :=
		buildStaffAttendanceReportRows(
			staff,
			records,
		)

	selectedUserID := int64(0)

	if raw :=
		strings.TrimSpace(
			r.URL.Query().Get(
				"user_id",
			),
		); raw != "" {

		parsed, err :=
			strconv.ParseInt(
				raw,
				10,
				64,
			)

		if err == nil &&
			parsed > 0 {
			selectedUserID =
				parsed
		}
	}

	var selectedUser *User
	if selectedUserID > 0 {
		selectedUser =
			staffAttendanceUserByID(
				staff,
				selectedUserID,
			)

		if selectedUser == nil {
			http.Error(
				w,
				"staff member not found in this workspace",
				http.StatusNotFound,
			)
			return
		}
	}

	if strings.EqualFold(
		strings.TrimSpace(
			r.URL.Query().Get(
				"format",
			),
		),
		"csv",
	) {
		if err :=
			writeStaffAttendanceReportCSV(
				w,
				month,
				func() string {
					if selectedUser == nil {
						return ""
					}
					return selectedUser.Name
				}(),
				reportRows,
			); err != nil {

			log.Printf(
				"write staff attendance csv: %v",
				err,
			)
		}

		return
	}

	var history []StaffAttendanceHistoryRow

	if selectedUserID > 0 {
		history =
			staffAttendanceHistoryForUser(
				records,
				selectedUserID,
			)
	}

	data :=
		a.newTemplateData(
			w,
			r,
			currentUser,
		)

	data.Title =
		"Staff Attendance Report"

	data.Description =
		"Review monthly attendance performance and individual staff history."

	data.StaffAttendanceUsers =
		staff

	data.StaffAttendanceReportRows =
		reportRows

	data.StaffAttendanceReportSummary =
		summarizeStaffAttendanceReport(
			reportRows,
		)

	data.StaffAttendanceMonth =
		month

	data.SelectedStaffAttendanceUser =
		selectedUser

	data.StaffAttendanceHistory =
		history

	a.render(
		w,
		"staff-attendance-report",
		data,
		http.StatusOK,
	)
}
