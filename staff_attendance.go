package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type StaffAttendanceInput struct {
	UserID int64
	Status string
	Note   string
}

func normalizeStaffAttendanceStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validStaffAttendanceStatus(value string) bool {
	switch normalizeStaffAttendanceStatus(value) {
	case "present", "absent", "late", "excused":
		return true
	default:
		return false
	}
}

func normalizeStaffAttendanceDate(value string) (string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return "", errors.New("attendance date is required")
	}

	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", errors.New("invalid attendance date")
	}

	today := currentBusinessDate()

	if parsed.Format("2006-01-02") > today {
		return "", errors.New(
			"future staff attendance cannot be recorded",
		)
	}
	if err := validateHistoricalEntryTime(parsed, "staff attendance date"); err != nil {
		return "", err
	}

	return parsed.Format("2006-01-02"), nil
}

// listStaffAttendanceRecordsByUserIDs intentionally reads the existing
// coach_attendance_records table. That table is user_id based and is now
// treated as the legacy physical storage for operational staff attendance.
// This preserves all historical coach attendance without duplicating data.
func (a *App) listStaffAttendanceRecordsByUserIDs(
	attendanceDate string,
	userIDs []int64,
) ([]CoachAttendanceRecord, error) {
	userIDs = uniquePositiveInt64Values(userIDs)

	if len(userIDs) == 0 {
		return nil, nil
	}

	placeholders, args :=
		int64ScopePlaceholders(userIDs)

	if placeholders == "" {
		return nil, nil
	}

	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, attendanceDate)
	queryArgs = append(queryArgs, args...)

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
		WHERE attendance_date = ?
		  AND user_id IN (`+placeholders+`)
		ORDER BY user_id ASC, id ASC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make(
		[]CoachAttendanceRecord,
		0,
		len(userIDs),
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

		records = append(records, record)
	}

	return records, rows.Err()
}

func (a *App) saveStaffAttendanceRecords(
	attendanceDate string,
	inputs []StaffAttendanceInput,
	recordedByUserID int64,
) error {
	if recordedByUserID <= 0 {
		return errors.New(
			"attendance recorder is required",
		)
	}

	attendanceDate, err :=
		normalizeStaffAttendanceDate(attendanceDate)
	if err != nil {
		return err
	}

	if len(inputs) == 0 {
		return nil
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	seen := make(map[int64]struct{}, len(inputs))

	for _, input := range inputs {
		if input.UserID <= 0 {
			return errors.New("invalid staff member")
		}

		if _, exists := seen[input.UserID]; exists {
			return errors.New(
				"duplicate staff attendance submission",
			)
		}

		seen[input.UserID] = struct{}{}

		status :=
			normalizeStaffAttendanceStatus(
				input.Status,
			)

		if !validStaffAttendanceStatus(status) {
			return fmt.Errorf(
				"invalid attendance status for staff member %d",
				input.UserID,
			)
		}

		note := strings.TrimSpace(input.Note)

		// The legacy table does not rely on a unique(user,date)
		// constraint, so replacement is explicit and deterministic.
		if _, err := tx.Exec(`
			DELETE FROM coach_attendance_records
			WHERE user_id = ?
			  AND attendance_date = ?
		`,
			input.UserID,
			attendanceDate,
		); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO coach_attendance_records (
				user_id,
				attendance_date,
				status,
				note,
				recorded_by_user_id,
				recorded_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
			input.UserID,
			attendanceDate,
			status,
			note,
			recordedByUserID,
			now,
			now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func staffAttendanceUserIDs(
	users []User,
) []int64 {
	ids := make([]int64, 0, len(users))

	for _, user := range users {
		if user.ID <= 0 || !user.Active {
			continue
		}

		ids = append(ids, user.ID)
	}

	return uniquePositiveInt64Values(ids)
}

func staffAttendanceInputsFromRequest(
	r *http.Request,
	users []User,
) ([]StaffAttendanceInput, error) {
	inputs := make(
		[]StaffAttendanceInput,
		0,
		len(users),
	)

	for _, user := range users {
		if user.ID <= 0 || !user.Active {
			continue
		}

		id := strconv.FormatInt(user.ID, 10)

		status :=
			normalizeStaffAttendanceStatus(
				r.FormValue("status_" + id),
			)

		if !validStaffAttendanceStatus(status) {
			return nil, fmt.Errorf(
				"select a valid attendance status for %s",
				user.Name,
			)
		}

		inputs = append(
			inputs,
			StaffAttendanceInput{
				UserID: user.ID,
				Status: status,
				Note: strings.TrimSpace(
					r.FormValue("note_" + id),
				),
			},
		)
	}

	return inputs, nil
}

func staffAttendanceRedirect(
	attendanceDate string,
	division string,
) string {
	target := "/admin/staff/attendance"

	if strings.TrimSpace(attendanceDate) != "" {
		target = withQueryValue(
			target,
			"date",
			strings.TrimSpace(attendanceDate),
		)
	}

	if strings.TrimSpace(division) != "" {
		target = withQueryValue(
			target,
			"division",
			strings.TrimSpace(division),
		)
	}

	return target
}

func (a *App) staffAttendanceManagementHandler(
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

	divisionIDs, ok :=
		a.studentGroupDivisionScope(
			w,
			r,
			user,
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
			"list operational staff for attendance: %v",
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
			"hydrate staff attendance divisions: %v",
			err,
		)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	attendanceDate := strings.TrimSpace(
		r.URL.Query().Get("date"),
	)

	if attendanceDate == "" {
		attendanceDate =
			time.Now().Format("2006-01-02")
	}

	attendanceDate, err =
		normalizeStaffAttendanceDate(
			attendanceDate,
		)
	if err != nil {
		attendanceDate =
			time.Now().Format("2006-01-02")
	}

	records, err :=
		a.listStaffAttendanceRecordsByUserIDs(
			attendanceDate,
			staffAttendanceUserIDs(staff),
		)
	if err != nil {
		log.Printf(
			"list staff attendance records: %v",
			err,
		)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)

	data.Title = "Staff Attendance"
	data.Description =
		"Record daily attendance for operational staff."
	data.StaffAttendanceUsers = staff
	data.StaffAttendanceRecords = records
	data.AttendanceDate = attendanceDate
	data.TodayDate =
		time.Now().Format("2006-01-02")

	a.render(
		w,
		"staff-attendance",
		data,
		http.StatusOK,
	)
}

func (a *App) saveStaffAttendanceHandler(
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

	currentUser, _ :=
		a.currentUser(r.Context())

	divisionScope :=
		strings.TrimSpace(
			r.FormValue("division"),
		)

	// studentGroupDivisionScope already contains the operational
	// division authorization rules. Feed the submitted division
	// into a cloned request so POSTs use the same policy as GETs.
	scopeRequest := r.Clone(r.Context())
	urlCopy := *r.URL
	query := urlCopy.Query()

	if divisionScope != "" {
		query.Set(
			"division",
			divisionScope,
		)
	}

	urlCopy.RawQuery = query.Encode()
	scopeRequest.URL = &urlCopy

	divisionIDs, ok :=
		a.studentGroupDivisionScope(
			w,
			scopeRequest,
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
			"list operational staff for attendance save: %v",
			err,
		)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	attendanceDate, err :=
		normalizeStaffAttendanceDate(
			r.FormValue("attendance_date"),
		)
	if err != nil {
		a.setFlash(
			w,
			"Staff attendance could not be saved: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			staffAttendanceRedirect(
				r.FormValue("attendance_date"),
				divisionScope,
			),
			http.StatusSeeOther,
		)
		return
	}

	inputs, err :=
		staffAttendanceInputsFromRequest(
			r,
			staff,
		)
	if err != nil {
		a.setFlash(
			w,
			"Staff attendance could not be saved: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			staffAttendanceRedirect(
				attendanceDate,
				divisionScope,
			),
			http.StatusSeeOther,
		)
		return
	}

	recordedByUserID := int64(0)

	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}

	if err := a.saveStaffAttendanceRecords(
		attendanceDate,
		inputs,
		recordedByUserID,
	); err != nil {
		log.Printf(
			"save staff attendance: %v",
			err,
		)

		a.setFlash(
			w,
			"Staff attendance could not be saved: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			staffAttendanceRedirect(
				attendanceDate,
				divisionScope,
			),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Staff attendance saved successfully.",
	)

	http.Redirect(
		w,
		r,
		staffAttendanceRedirect(
			attendanceDate,
			divisionScope,
		),
		http.StatusSeeOther,
	)
}

// Compile-time reminder: the generic attendance layer deliberately
// retains the legacy CoachAttendanceRecord model/table for history.
var _ = sql.ErrNoRows
