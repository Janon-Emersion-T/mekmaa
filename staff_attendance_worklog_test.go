package main

import (
	"strings"
	"testing"
)

func TestStaffAttendanceWorkLogFiltersAndHours(t *testing.T) {
	users := []User{
		{ID: 1, Name: "Alex"},
		{ID: 2, Name: "Sam"},
	}
	records := []CoachAttendanceRecord{
		{
			UserID: 1, AttendanceDate: "2026-10-19",
			Status: "present",
			Sessions: []StaffAttendanceSession{
				{StartTime: "09:00", EndTime: "17:00"},
			},
		},
		{
			UserID: 2, AttendanceDate: "2026-10-18",
			Status: "absent",
		},
	}

	all := buildStaffAttendanceWorkLog(users, records, 0)
	if len(all) != 2 {
		t.Fatalf("all staff: got %d records, want 2", len(all))
	}
	if all[0].WorkedHours != 8 || all[0].UserName != "Alex" {
		t.Fatalf("unexpected timed record: %+v", all[0])
	}
	if all[1].WorkedHours != 0 || len(all[1].Sessions) != 0 {
		t.Fatalf("legacy hours incorrectly populated: %+v", all[1])
	}

	selected := buildStaffAttendanceWorkLog(users, records, 1)
	if len(selected) != 1 || selected[0].UserName != "Alex" {
		t.Fatalf("selected staff filter failed: %+v", selected)
	}
}

func TestStaffAttendanceReportIncludesDailyWorkLog(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	body := renderTemplateToString(t, templates, "staff-attendance-report", TemplateData{
		StaffAttendanceMonth: "2026-10",
		StaffAttendanceWorkLog: []StaffAttendanceWorkLogRow{
			{
				UserName:       "Alex",
				AttendanceDate: "2026-10-19",
				Status:         "present",
				Sessions: []StaffAttendanceSession{
					{StartTime: "09:00", EndTime: "17:00"},
				},
				WorkedHours: 8,
			},
		},
		SelectedDivisionScope: "all",
	})
	for _, expected := range []string{
		"Daily work log", "Alex", "2026-10-19",
		"09:00", "17:00", "8.00 h",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q", expected)
		}
	}
}
