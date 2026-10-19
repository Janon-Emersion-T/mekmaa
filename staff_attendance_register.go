package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type StaffAttendanceRegisterRow struct {
	UserName       string
	UserEmail      string
	UserID         int64
	AttendanceDate string
	Status         string
	Note           string
	Sessions       []StaffAttendanceSession
	WorkedHours    float64
}

func staffAttendanceRegisterBounds(r *http.Request) (string, string, error) {
	today := currentBusinessDate()
	from := strings.TrimSpace(r.URL.Query().Get("records_from"))
	to := strings.TrimSpace(r.URL.Query().Get("records_to"))

	if from == "" {
		from = today[:7] + "-01"
	}
	if to == "" {
		to = today
	}

	for _, value := range []string{from, to} {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil || parsed.Format("2006-01-02") != value {
			return "", "", fmt.Errorf("invalid records date range")
		}
	}
	if from > to {
		return "", "", fmt.Errorf("records start date must not exceed end date")
	}
	if to > today {
		return "", "", fmt.Errorf("records end date cannot be in the future")
	}
	return from, to, nil
}

func (a *App) listStaffAttendanceRegister(
	users []User,
	from, to string,
	selectedUserID int64,
) ([]StaffAttendanceRegisterRow, error) {
	names := make(map[int64]User, len(users))
	ids := make([]int64, 0, len(users))

	for _, user := range users {
		if user.ID <= 0 || !user.Active {
			continue
		}
		names[user.ID] = user
		if selectedUserID == 0 || selectedUserID == user.ID {
			ids = append(ids, user.ID)
		}
	}
	ids = uniquePositiveInt64Values(ids)
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders, args := int64ScopePlaceholders(ids)
	queryArgs := append([]any{from, to}, args...)

	rows, err := a.queryDB(`
		SELECT id, user_id, attendance_date,
			COALESCE(status, ''), COALESCE(note, ''),
			COALESCE(recorded_by_user_id, 0),
			recorded_at, updated_at
		FROM coach_attendance_records
		WHERE attendance_date >= ?
		  AND attendance_date <= ?
		  AND user_id IN (`+placeholders+`)
		ORDER BY attendance_date DESC, user_id ASC, id DESC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}

	var records []CoachAttendanceRecord
	for rows.Next() {
		var record CoachAttendanceRecord
		if err := rows.Scan(
			&record.ID, &record.UserID, &record.AttendanceDate,
			&record.Status, &record.Note, &record.RecordedByUserID,
			&record.RecordedAt, &record.UpdatedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := a.hydrateStaffAttendanceSessions(records); err != nil {
		return nil, err
	}

	result := make([]StaffAttendanceRegisterRow, 0, len(records))
	for _, record := range records {
		user, ok := names[record.UserID]
		if !ok {
			continue
		}
		result = append(result, StaffAttendanceRegisterRow{
			UserName:       user.Name,
			UserEmail:      user.Email,
			UserID:         record.UserID,
			AttendanceDate: record.AttendanceDate,
			Status:         record.Status,
			Note:           record.Note,
			Sessions:       record.Sessions,
			WorkedHours:    record.SessionHours(),
		})
	}
	return result, nil
}

func selectedStaffAttendanceRegisterUser(
	r *http.Request,
	users []User,
) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("records_user_id"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid staff filter")
	}
	for _, user := range users {
		if user.ID == id && user.Active {
			return id, nil
		}
	}
	return 0, fmt.Errorf("staff member is not available in this workspace")
}
