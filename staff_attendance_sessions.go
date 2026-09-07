package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type StaffAttendanceSession struct {
	StartTime string
	EndTime   string
}

func (s StaffAttendanceSession) Minutes() int {
	start, err := time.Parse("15:04", s.StartTime)
	if err != nil {
		return 0
	}
	end, err := time.Parse("15:04", s.EndTime)
	if err != nil {
		return 0
	}
	return int(end.Sub(start).Minutes())
}

func (r CoachAttendanceRecord) SessionHours() float64 {
	minutes := 0
	for _, session := range r.Sessions {
		minutes += session.Minutes()
	}
	return float64(minutes) / 60
}

func normalizeStaffAttendanceSessions(sessions []StaffAttendanceSession, status string) ([]StaffAttendanceSession, error) {
	if sessions == nil {
		return nil, nil
	}
	if len(sessions) > 24 {
		return nil, errors.New("a staff member can have at most 24 sessions per day")
	}
	normalized := make([]StaffAttendanceSession, 0, len(sessions))
	for _, session := range sessions {
		session.StartTime = strings.TrimSpace(session.StartTime)
		session.EndTime = strings.TrimSpace(session.EndTime)
		start, startErr := time.Parse("15:04", session.StartTime)
		end, endErr := time.Parse("15:04", session.EndTime)
		if startErr != nil || endErr != nil || start.Format("15:04") != session.StartTime || end.Format("15:04") != session.EndTime {
			return nil, errors.New("each session needs valid arrival and leaving times")
		}
		if !end.After(start) {
			return nil, errors.New("leaving time must be after arrival on the same day")
		}
		normalized = append(normalized, session)
	}
	if len(normalized) > 0 && status != "present" && status != "late" {
		return nil, errors.New("staff with work sessions must be marked present or late")
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].StartTime < normalized[j].StartTime })
	for i := 1; i < len(normalized); i++ {
		if normalized[i].StartTime < normalized[i-1].EndTime {
			return nil, errors.New("work sessions for the same staff member must not overlap")
		}
	}
	return normalized, nil
}

// Attach session details without changing the legacy one-record-per-day register.
func (a *App) hydrateStaffAttendanceSessions(records []CoachAttendanceRecord) error {
	if len(records) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(records))
	startDate, endDate := records[0].AttendanceDate, records[0].AttendanceDate
	index := make(map[string]int, len(records))
	for i, record := range records {
		userIDs = append(userIDs, record.UserID)
		if record.AttendanceDate < startDate {
			startDate = record.AttendanceDate
		}
		if record.AttendanceDate > endDate {
			endDate = record.AttendanceDate
		}
		index[fmt.Sprintf("%d/%s", record.UserID, record.AttendanceDate)] = i
	}
	placeholders, args := int64ScopePlaceholders(uniquePositiveInt64Values(userIDs))
	args = append([]any{startDate, endDate}, args...)
	rows, err := a.queryDB(`
		SELECT user_id, attendance_date, start_time, end_time
		FROM staff_attendance_sessions
		WHERE attendance_date >= ? AND attendance_date <= ? AND user_id IN (`+placeholders+`)
		ORDER BY attendance_date, user_id, start_time
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var date string
		var session StaffAttendanceSession
		if err := rows.Scan(&userID, &date, &session.StartTime, &session.EndTime); err != nil {
			return err
		}
		if i, ok := index[fmt.Sprintf("%d/%s", userID, date)]; ok {
			records[i].Sessions = append(records[i].Sessions, session)
		}
	}
	return rows.Err()
}
