package main

import (
	"strings"
	"testing"
	"time"
)

func TestMonthlyFeeReminderDue(t *testing.T) {
	location, err := time.LoadLocation("Asia/Colombo")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		date time.Time
		want bool
	}{
		{
			name: "15th at 9 AM",
			date: time.Date(2026, 10, 15, 9, 0, 0, 0, location),
			want: true,
		},
		{
			name: "15th at 8 AM",
			date: time.Date(2026, 10, 15, 8, 0, 0, 0, location),
			want: false,
		},
		{
			name: "14th at 9 AM",
			date: time.Date(2026, 10, 14, 9, 0, 0, 0, location),
			want: false,
		},
		{
			name: "16th at 9 AM",
			date: time.Date(2026, 10, 16, 9, 0, 0, 0, location),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monthlyFeeReminderDue(tt.date)

			if got != tt.want {
				t.Errorf(
					"monthlyFeeReminderDue() = %v, want %v",
					got,
					tt.want,
				)
			}
		})
	}
}

func TestMonthlyFeeReminderMonths(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	months := monthlyFeeReminderMonths(start, end)

	expected := []string{
		"2026-07",
		"2026-08",
		"2026-09",
		"2026-10",
	}

	if len(months) != len(expected) {
		t.Fatalf(
			"got %d months, want %d",
			len(months),
			len(expected),
		)
	}

	for i := range expected {
		if months[i] != expected[i] {
			t.Errorf(
				"month[%d] = %s, want %s",
				i,
				months[i],
				expected[i],
			)
		}
	}
}

func TestBuildMonthlyFeeReminderSMS(t *testing.T) {
	reminder := MonthlyFeeReminder{
		AdmissionID:       1,
		StudentName:       "Test Student",
		GuardianPhone:     "0771234567",
		OutstandingAmount: 4500.50,
	}

	message := buildMonthlyFeeReminderSMS(reminder)

	if !strings.Contains(message, "Test Student") {
		t.Error("SMS does not contain student name")
	}

	if !strings.Contains(message, "4500.50") {
		t.Error("SMS does not contain outstanding amount")
	}

	if !strings.Contains(message, "Mekmaa") {
		t.Error("SMS does not contain organization name")
	}
}
