package main

import (
	"strings"
	"testing"
)

func TestCourseAttendanceLiveSearchAndCourseOnlyPage(t *testing.T) {
	html := courseAttendancePageHTML

	required := []string{
		`id="student-search"`,
		`id="course-attendance-table"`,
		`id="student-search-count"`,
		`id="student-search-empty"`,
		`id="clear-student-search"`,
		`search.addEventListener("input", filterStudents)`,
		`student.row.hidden = !matches`,
		`name="status_{{.EnrollmentID}}"`,
		`name="note_{{.EnrollmentID}}"`,
		`action="/admin/attendance/course/save"`,
	}

	for _, value := range required {
		if !strings.Contains(html, value) {
			t.Errorf("missing required attendance feature: %s", value)
		}
	}

	if strings.Contains(html, `href="/admin/attendance"`) {
		t.Error("legacy attendance navigation should be removed")
	}

	if strings.Contains(html, `action="/admin/attendance/save"`) {
		t.Error("legacy group attendance form should not appear")
	}
}
