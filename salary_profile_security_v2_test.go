package main

import "testing"

func TestSalaryProfilesVisibleToStaff(t *testing.T) {
	profiles := []StaffSalaryProfile{
		{ID: 1, UserID: 10, Rate: 50000},
		{ID: 2, UserID: 20, Rate: 60000},
		{ID: 3, UserID: 10, Rate: 1500},
	}

	staff := []User{
		{ID: 10, Name: "Visible Coach"},
	}

	filtered := salaryProfilesVisibleToStaff(profiles, staff)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 visible profiles, got %d", len(filtered))
	}

	for _, profile := range filtered {
		if profile.UserID != 10 {
			t.Fatalf("unauthorized profile exposed: %+v", profile)
		}
	}
}

func TestSalaryProfilesVisibleToStaffEmptyScope(t *testing.T) {
	profiles := []StaffSalaryProfile{
		{ID: 1, UserID: 10},
	}

	filtered := salaryProfilesVisibleToStaff(profiles, nil)

	if len(filtered) != 0 {
		t.Fatalf("expected no visible profiles, got %d", len(filtered))
	}
}

func TestSalaryProfilesVisibleToStaffPreservesOrder(t *testing.T) {
	profiles := []StaffSalaryProfile{
		{ID: 3, UserID: 10},
		{ID: 2, UserID: 20},
		{ID: 1, UserID: 10},
	}

	filtered := salaryProfilesVisibleToStaff(
		profiles,
		[]User{{ID: 10}, {ID: 20}},
	)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(filtered))
	}

	for index, expected := range []int64{3, 2, 1} {
		if filtered[index].ID != expected {
			t.Fatalf("profile order changed at %d", index)
		}
	}
}
