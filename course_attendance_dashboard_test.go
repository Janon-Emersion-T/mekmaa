package main

import (
	"os"
	"strings"
	"testing"
)

func TestCourseAttendanceUsesDashboardLayout(t *testing.T) {
	source, err := os.ReadFile(
		"templates/dashboard/attendance-management.html",
	)
	if err != nil {
		t.Fatal(err)
	}

	html := string(source)

	required := []string{
		`{{template "base" .}}`,
		`{{define "content"}}`,
		`CourseAttendance.Programs`,
		`CourseAttendance.Students`,
		`id="student-search"`,
		`id="course-attendance-table"`,
		`action="/admin/attendance/course/save"`,
	}

	for _, item := range required {
		if !strings.Contains(html, item) {
			t.Errorf("missing dashboard attendance element: %s", item)
		}
	}

	for _, legacy := range []string{
		`data-group-select`,
		`data-session-select`,
		`Open shared attendance`,
		`Monthly Report`,
	} {
		if strings.Contains(html, legacy) {
			t.Errorf("legacy attendance element remains: %s", legacy)
		}
	}
}
