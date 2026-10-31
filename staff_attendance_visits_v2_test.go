package main

import "testing"

func TestStaffAttendanceVisitDaysAreUniquePerCoachAndDate(t *testing.T) {
	users := []User{
		{ID: 1, Name: "Coach One", Active: true},
		{ID: 2, Name: "Coach Two", Active: true},
		{ID: 3, Name: "Coach Three", Active: true},
	}
	records := []CoachAttendanceRecord{
		{UserID: 1, AttendanceDate: "2026-08-02", Status: "present"},
		{UserID: 1, AttendanceDate: "2026-08-02", Status: "present"},
		{UserID: 1, AttendanceDate: "2026-08-05", Status: "late"},
		{UserID: 1, AttendanceDate: "2026-08-06", Status: "absent"},
		{UserID: 1, AttendanceDate: "2026-08-07", Status: "excused"},
		{UserID: 2, AttendanceDate: "2026-08-03", Status: "present"},
	}
	rows := buildStaffAttendanceReportRows(users, records)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].VisitDays != 2 ||
		rows[0].FirstVisit != "2026-08-02" ||
		rows[0].LastVisit != "2026-08-05" {
		t.Fatalf("incorrect first coach visits: %+v", rows[0])
	}
	if rows[1].VisitDays != 1 || rows[2].VisitDays != 0 {
		t.Fatalf("incorrect other coach visits: %+v", rows)
	}
	summary := summarizeStaffAttendanceReport(rows)
	if summary.TotalVisitDays != 3 || summary.CoachesAttended != 2 ||
		summary.AverageVisits != 1.5 || summary.StaffCount != 3 {
		t.Fatalf("incorrect visit summary: %+v", summary)
	}
}
