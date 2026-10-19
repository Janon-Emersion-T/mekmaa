package main

import (
	"os"
	"strings"
	"testing"
)

func TestCourseAttendancePostgresMigrationDiscovered(t *testing.T) {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, migration := range migrations {
		if migration.Version != 30 {
			continue
		}
		found = true
		if migration.Filename != "000030_course_attendance.sql" {
			t.Fatalf("unexpected migration: %s", migration.Filename)
		}
		if migration.Checksum == "" {
			t.Fatal("migration checksum missing")
		}
	}
	if !found {
		t.Fatal("course attendance migration 30 not discovered")
	}
}

func TestCourseAttendanceDatabaseConstraints(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)

	_, err := app.db.Exec(`
		INSERT INTO course_attendance_records
			(enrollment_id, attendance_date, status)
		VALUES (?, ?, ?)
	`, enrollmentID, "2026-08-10", "present")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.db.Exec(`
		INSERT INTO course_attendance_records
			(enrollment_id, attendance_date, status)
		VALUES (?, ?, ?)
	`, enrollmentID, "2026-08-10", "absent")
	if err == nil {
		t.Fatal("duplicate attendance record was accepted")
	}

	_, err = app.db.Exec(`
		INSERT INTO course_attendance_records
			(enrollment_id, attendance_date, status)
		VALUES (?, ?, ?)
	`, enrollmentID, "2026-08-11", "invalid")
	if err == nil {
		t.Fatal("invalid attendance status was accepted")
	}
}

func TestCourseAttendanceRoutesRegistered(t *testing.T) {
	// Verify that the main router source contains protected registrations.
	// HTTP authorization still requires a separate authenticated request test.
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}

	source := string(sourceBytes)
	for _, route := range []string{
		`"/admin/attendance/course"`,
		`"/admin/attendance/course/save"`,
		`"attendance.view"`,
		`"attendance.update"`,
	} {
		if !strings.Contains(source, route) {
			t.Errorf("missing route or permission: %s", route)
		}
	}

}
