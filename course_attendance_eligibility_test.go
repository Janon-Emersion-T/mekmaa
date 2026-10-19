package main

import "testing"

func TestCourseAttendanceEnrollmentEligibility(t *testing.T) {
	periods := []CourseAttendanceEnrollmentPeriod{
		{
			EffectiveFrom: "2026-01-01",
			EffectiveTo:   "2026-03-01",
			Active:        true,
		},
		{
			EffectiveFrom: "2026-03-01",
			EffectiveTo:   "2026-05-10",
			Active:        false,
		},
		{
			EffectiveFrom: "2026-05-10",
			Active:        true,
		},
	}

	tests := []struct {
		name string
		date string
		want bool
	}{
		{"before enrollment", "2025-12-31", false},
		{"original enrollment", "2026-01-01", true},
		{"before unenrollment", "2026-02-28", true},
		{"unenrollment starts", "2026-03-01", false},
		{"during unenrollment", "2026-04-01", false},
		{"reenrollment starts", "2026-05-10", true},
		{"after reenrollment", "2026-06-01", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := courseAttendanceEnrollmentEligible(
				tt.date, "2026-01-01", true, periods,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("eligible=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestCourseAttendanceEnrollmentInvalidDate(t *testing.T) {
	if _, err := courseAttendanceEnrollmentEligible(
		"2026-02-30", "2026-01-01", true, nil,
	); err == nil {
		t.Fatal("expected invalid date error")
	}
}

func TestCourseAttendanceEnrollmentLegacyFallback(t *testing.T) {
	eligible, err := courseAttendanceEnrollmentEligible(
		"2026-06-01", "2026-01-01", true, nil,
	)
	if err != nil || !eligible {
		t.Fatalf("legacy active enrollment: eligible=%v err=%v",
			eligible, err)
	}
}
