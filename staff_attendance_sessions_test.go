package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStaffWorkSessionsValidation(t *testing.T) {
	tests := []struct {
		name, status string
		sessions     []StaffAttendanceSession
		valid        bool
	}{
		{"short shifts", "present", []StaffAttendanceSession{{"09:00", "10:00"}, {"14:00", "16:00"}}, true},
		{"adjacent", "late", []StaffAttendanceSession{{"10:00", "11:30"}, {"09:00", "10:00"}}, true},
		{"overlap", "present", []StaffAttendanceSession{{"09:00", "11:00"}, {"10:00", "12:00"}}, false},
		{"reversed", "present", []StaffAttendanceSession{{"11:00", "09:00"}}, false},
		{"zero", "present", []StaffAttendanceSession{{"09:00", "09:00"}}, false},
		{"missing end", "present", []StaffAttendanceSession{{"09:00", ""}}, false},
		{"invalid clock", "present", []StaffAttendanceSession{{"09:00", "25:00"}}, false},
		{"absent", "absent", []StaffAttendanceSession{{"09:00", "10:00"}}, false},
		{"excused", "excused", []StaffAttendanceSession{{"09:00", "10:00"}}, false},
		{"legacy", "present", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeStaffAttendanceSessions(tt.sessions, tt.status)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestStaffWorkSessionsPersistReportAndReplace(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user, err := app.createUser("Session Staff", "sessions@example.com", "SecurePass123!")
	if err != nil {
		t.Fatal(err)
	}
	date := time.Now().Format("2006-01-02")
	inputs := []StaffAttendanceInput{{UserID: user.ID, Status: "present", Sessions: []StaffAttendanceSession{{"09:00", "10:30"}, {"14:00", "16:00"}}}}
	save := func() {
		t.Helper()
		if err := app.saveStaffAttendanceRecords(date, inputs, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	save()
	records, err := app.listStaffAttendanceRecordsByUserIDs(date, []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Sessions) != 2 || records[0].SessionHours() != 3.5 {
		t.Fatalf("wrong records: %+v", records)
	}
	monthly, err := app.listStaffAttendanceRecordsForMonthByUserIDs(date[:7], []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	user.Active = true
	rows := buildStaffAttendanceReportRows([]User{*user}, monthly)
	if len(rows) != 1 || rows[0].SessionCount != 2 || rows[0].WorkedHours != 3.5 || rows[0].PresentCount != 1 {
		t.Fatalf("wrong report: %+v", rows)
	}
	history := staffAttendanceHistoryForUser(monthly, user.ID)
	if len(history) != 1 || len(history[0].Sessions) != 2 || history[0].WorkedHours != 3.5 {
		t.Fatalf("wrong history: %+v", history)
	}
	csv := httptest.NewRecorder()
	if err := writeStaffAttendanceReportCSV(csv, date[:7], user.Name, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv.Body.String(), "Recorded Hours") || !strings.Contains(csv.Body.String(), ",2,3.50") {
		t.Fatalf("session totals missing from CSV: %s", csv.Body.String())
	}
	// Invalid replacement must leave both the daily record and sessions intact.
	inputs[0].Sessions = []StaffAttendanceSession{{"09:00", "11:00"}, {"10:00", "12:00"}}
	if err := app.saveStaffAttendanceRecords(date, inputs, user.ID); err == nil {
		t.Fatal("accepted overlapping sessions")
	}
	records, err = app.listStaffAttendanceRecordsByUserIDs(date, []int64{user.ID})
	if err != nil || records[0].SessionHours() != 3.5 {
		t.Fatalf("invalid update changed sessions: %+v, %v", records, err)
	}
	inputs[0].Sessions = []StaffAttendanceSession{{"10:00", "11:00"}}
	save()
	records, err = app.listStaffAttendanceRecordsByUserIDs(date, []int64{user.ID})
	if err != nil || len(records[0].Sessions) != 1 || records[0].SessionHours() != 1 {
		t.Fatalf("replace sessions: %+v, %v", records, err)
	}
	inputs[0].Sessions = []StaffAttendanceSession{}
	save()
	records, err = app.listStaffAttendanceRecordsByUserIDs(date, []int64{user.ID})
	if err != nil || len(records[0].Sessions) != 0 {
		t.Fatalf("remove sessions: %+v, %v", records, err)
	}
}

func TestStaffWorkSessionsRequestScopeAndUnrecorded(t *testing.T) {
	form := url.Values{
		"status_1": {"present"}, "session_start_1": {"09:00", "14:00"}, "session_end_1": {"10:00", "16:00"},
		"status_2": {""}, "sessions_submitted_2": {"1"},
		"status_3": {"present"}, "session_start_3": {"09:00"}, "session_end_3": {"11:00"},
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	inputs, err := staffAttendanceInputsFromRequest(req, []User{{ID: 1, Active: true}, {ID: 2, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].UserID != 1 || len(inputs[0].Sessions) != 2 {
		t.Fatalf("scope/unrecorded handling: %+v", inputs)
	}
	req.Form["session_end_1"] = []string{"10:00"}
	if _, err := staffAttendanceInputsFromRequest(req, []User{{ID: 1, Active: true}}); err == nil {
		t.Fatal("accepted unmatched session fields")
	}
}

func TestStaffWorkSessionsTemplatesRender(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	staff := User{ID: 42, Name: "Part-time staff", Active: true}
	records := []CoachAttendanceRecord{{ID: 1, UserID: staff.ID, AttendanceDate: "2026-09-01", Status: "present", Sessions: []StaffAttendanceSession{{"09:00", "10:30"}, {"14:00", "15:00"}}}}
	data := TemplateData{
		CSRFToken: "test", AttendanceDate: "2026-09-01", TodayDate: "2026-09-07",
		StaffAttendanceUsers: []User{staff}, StaffAttendanceRecords: records,
		StaffAttendanceReportRows:   buildStaffAttendanceReportRows([]User{staff}, records),
		StaffAttendanceHistory:      staffAttendanceHistoryForUser(records, staff.ID),
		SelectedStaffAttendanceUser: &staff, StaffAttendanceMonth: "2026-09",
	}
	html := renderTemplateToString(t, templates, "staff-attendance", data)
	for _, expected := range []string{"+ Add session", "session_start_42", "session_end_42", "10:30", ">2.5</span> hours"} {
		if !strings.Contains(html, expected) {
			t.Errorf("attendance missing %q", expected)
		}
	}
	for _, name := range []string{"staff-attendance-report", "staff-attendance-report-print"} {
		html := renderTemplateToString(t, templates, name, data)
		for _, expected := range []string{"Recorded hours", "2.50", "09:00–10:30", "14:00–15:00"} {
			if !strings.Contains(html, expected) {
				t.Errorf("%s missing %q", name, expected)
			}
		}
	}
}
