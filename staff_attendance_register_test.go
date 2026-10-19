package main

import (
	"net/http/httptest"
	"testing"
)

func TestStaffAttendanceRegisterBoundsIndependentOfEntryDate(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/admin/staff/attendance?date=2026-07-16&records_from=2026-07-01&records_to=2026-07-31",
		nil)

	from, to, err := staffAttendanceRegisterBounds(r)
	if err != nil {
		t.Fatal(err)
	}
	if from != "2026-07-01" || to != "2026-07-31" {
		t.Fatalf("unexpected records range: %s to %s", from, to)
	}
}

func TestStaffAttendanceRegisterRejectsInvertedRange(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/admin/staff/attendance?records_from=2026-07-20&records_to=2026-07-01",
		nil)

	if _, _, err := staffAttendanceRegisterBounds(r); err == nil {
		t.Fatal("expected invalid range rejection")
	}
}

func TestStaffAttendanceRegisterRejectsUnauthorizedStaffFilter(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/admin/staff/attendance?records_user_id=999",
		nil)

	if _, err := selectedStaffAttendanceRegisterUser(
		r, []User{{ID: 1, Active: true}},
	); err == nil {
		t.Fatal("expected staff scope rejection")
	}
}
