package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteStudentAttendanceReportCSVIncludesReportHeadings(t *testing.T) {
	rec := httptest.NewRecorder()

	err := writeStudentAttendanceReportCSV(
		rec,
		"2026-08",
		"Reporting Group",
		"STD-REPORTING-001",
		[]StudentAttendanceReportRow{
			{
				Admission: Admission{
					StudentID: "STD-REPORTING-001",
					FullName:  "Reporting Student",
				},
				GroupName:            "Reporting Group",
				TrainingProgramName:  "Reporting Programme",
				PresentCount:         3,
				AbsentCount:          1,
				EligibleSessions:     4,
				AttendedCount:        3,
				AttendancePercentage: 75,
			},
		},
	)
	if err != nil {
		t.Fatalf("write student attendance csv: %v", err)
	}

	body := rec.Body.String()
	for _, needle := range []string{
		"Section,Field,Value",
		"Mekmaa Student Attendance Report",
		"filter,Group,Reporting Group",
		"filter,Student,STD-REPORTING-001",
		"Student ID,Student,Group / Class / Batch,Programme",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("student attendance csv missing %q in %s", needle, body)
		}
	}
}

func TestWriteStaffAttendanceReportCSVIncludesReportHeadings(t *testing.T) {
	rec := httptest.NewRecorder()

	err := writeStaffAttendanceReportCSV(
		rec,
		"2026-08",
		"Report Coach",
		[]StaffAttendanceReportRow{
			{
				User: User{
					Name:  "Report Coach",
					Email: "report-coach@example.com",
				},
				PresentCount:         8,
				AbsentCount:          1,
				CountedDays:          9,
				AttendedDays:         8,
				AttendancePercentage: 88.89,
			},
		},
	)
	if err != nil {
		t.Fatalf("write staff attendance csv: %v", err)
	}

	body := rec.Body.String()
	for _, needle := range []string{
		"Section,Field,Value",
		"Mekmaa Staff Attendance Report",
		"filter,Staff Member,Report Coach",
		"Staff,Email,Present,Absent,Late,Excused",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("staff attendance csv missing %q in %s", needle, body)
		}
	}
}
