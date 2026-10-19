package main

import (
	"os"
	"strings"
	"testing"
)

func TestStaffAttendanceSimpleForm(t *testing.T) {
	b, err := os.ReadFile("templates/dashboard/staff-attendance.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	required := []string{
		`{{template "base" .}}`,
		`id="staff-member"`,
		`name="attendance_date"`,
		`id="attendance-from"`,
		`id="attendance-to"`,
		`action="/admin/staff/attendance/save"`,
		`addField("status_" + staff.value, "present")`,
		`"sessions_submitted_"`,
		`record.sessions.length > 1`,
	}
	for _, item := range required {
		if !strings.Contains(html, item) {
			t.Errorf("missing %q", item)
		}
	}
	for _, old := range []string{
		`data-add-session`,
		`data-staff-sessions`,
		`staff_status_`,
		`staff_note_`,
	} {
		if strings.Contains(html, old) {
			t.Errorf("legacy control remains: %s", old)
		}
	}
}
