package main

import (
	"testing"
)

func TestCourseAttendanceRosterHistoricalEnrollment(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		date    string
		present bool
	}{
		{"before enrollment", "2026-07-31", false},
		{"first active day", "2026-08-01", true},
		{"during enrollment", "2026-08-10", true},
		{"after unenrollment", "2026-08-21", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			roster, err := app.courseAttendanceRoster(
				enrollment.TrainingProgramID, tc.date,
			)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, student := range roster {
				if student.EnrollmentID == enrollmentID {
					found = true
				}
			}
			if found != tc.present {
				t.Fatalf(
					"date %s: expected enrolled=%t, got %t",
					tc.date, tc.present, found,
				)
			}
		})
	}
}

func TestCourseAttendanceRejectsInvalidSubmissions(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)
	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		program int64
		date    string
		entries []CourseAttendanceEntry
	}{
		{
			name:    "invalid course",
			program: 0,
			date:    "2026-08-10",
		},
		{
			name:    "invalid date",
			program: enrollment.TrainingProgramID,
			date:    "invalid",
		},
		{
			name:    "inactive enrollment",
			program: enrollment.TrainingProgramID,
			date:    "2026-08-21",
			entries: []CourseAttendanceEntry{
				{EnrollmentID: enrollmentID, Status: "present"},
			},
		},
		{
			name:    "invalid status",
			program: enrollment.TrainingProgramID,
			date:    "2026-08-10",
			entries: []CourseAttendanceEntry{
				{EnrollmentID: enrollmentID, Status: "unknown"},
			},
		},
		{
			name:    "duplicate enrollment",
			program: enrollment.TrainingProgramID,
			date:    "2026-08-10",
			entries: []CourseAttendanceEntry{
				{EnrollmentID: enrollmentID, Status: "present"},
				{EnrollmentID: enrollmentID, Status: "absent"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := app.saveCourseAttendance(
				tc.program, tc.date, tc.entries, 1,
			)
			if err == nil {
				t.Fatal("expected attendance submission rejection")
			}
		})
	}
}

func TestCourseAttendanceSaveAndReload(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}

	// Create a recorder in this test's isolated database.
	recorder, err := app.createUser(
		"Attendance Test Recorder",
		"course-attendance-recorder@example.com",
		"SecureTestPassword123!",
	)
	if err != nil {
		t.Fatal(err)
	}
	userID := recorder.ID

	date := "2026-08-10"
	programID := enrollment.TrainingProgramID

	err = app.saveCourseAttendance(programID, date, []CourseAttendanceEntry{
		{EnrollmentID: enrollmentID, Status: "present", Note: "First save"},
	}, userID)
	if err != nil {
		t.Fatal(err)
	}

	roster, err := app.courseAttendanceRoster(programID, date)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, student := range roster {
		if student.EnrollmentID == enrollmentID {
			found = true
			if student.Status != "present" || student.Note != "First save" {
				t.Fatalf("incorrect saved attendance: %+v", student)
			}
		}
	}
	if !found {
		t.Fatal("saved student missing from course roster")
	}

	err = app.saveCourseAttendance(programID, date, []CourseAttendanceEntry{
		{EnrollmentID: enrollmentID, Status: "late", Note: "Updated"},
	}, userID)
	if err != nil {
		t.Fatal(err)
	}

	var status, note string
	err = app.db.QueryRow(`
		SELECT status, note FROM course_attendance_records
		WHERE enrollment_id = ? AND attendance_date = ?
	`, enrollmentID, date).Scan(&status, &note)
	if err != nil {
		t.Fatal(err)
	}
	if status != "late" || note != "Updated" {
		t.Fatalf("incorrect updated attendance: %q %q", status, note)
	}

	var count int
	err = app.db.QueryRow(`
		SELECT COUNT(*) FROM course_attendance_records
		WHERE enrollment_id = ? AND attendance_date = ?
	`, enrollmentID, date).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one attendance record, got %d", count)
	}
}
