package main

import (
	"testing"
	"time"
)

func TestBookingDurationHalfHourIncrements(t *testing.T) {
	for _, value := range []string{"", "1", "1.5", "2", "2.5", "12"} {
		if _, err := parseBookingDurationHours(value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"0", "0.5", "-1", "1.25", "12.5", "NaN", "Inf", "abc"} {
		if _, err := parseBookingDurationHours(value); err == nil {
			t.Errorf("accepted invalid duration %q", value)
		}
	}
}

func TestCreateSpaceSchedulesHalfHourPricingAndAvailability(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	date := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	schedule := SpaceSchedule{SlotDate: date, SlotHour: "18:00", EntryType: "booking", Activity: "full_indoor_cricket", Quantity: 1, Title: "90 minute booking"}
	if err := app.createSpaceSchedules(schedule, 1.5); err != nil {
		t.Fatal(err)
	}
	schedules, err := app.listSpaceSchedules()
	if err != nil {
		t.Fatal(err)
	}
	var totalMinutes int
	var tail *SpaceSchedule
	for i := range schedules {
		if schedules[i].Title != schedule.Title {
			continue
		}
		totalMinutes += schedules[i].DurationMinutes
		if schedules[i].SlotHour == "19:00" {
			tail = &schedules[i]
		}
	}
	if totalMinutes != 90 || tail == nil || tail.DurationMinutes != 30 {
		t.Fatalf("wrong saved duration: %d, tail: %+v", totalMinutes, tail)
	}
	fullHour := *tail
	fullHour.DurationMinutes = 60
	hourlyPrice, err := app.bookingQuote(fullHour)
	if err != nil {
		t.Fatal(err)
	}
	financials, err := app.listBookingFinancialsForScheduleIDs([]int64{tail.ID})
	if err != nil {
		t.Fatal(err)
	}
	financial := bookingFinancialForSchedule(financials, tail.ID)
	if financial == nil || financial.QuotedAmount != normalizeMoney(hourlyPrice/2) {
		t.Fatalf("incorrect half-hour charge: %+v", financial)
	}
	loaded, err := app.findSpaceScheduleByID(tail.ID)
	if err != nil || loaded.DurationMinutes != 30 {
		t.Fatalf("load half-hour booking: %+v, %v", loaded, err)
	}
	// Editing a saved half-hour segment must preserve its duration and price.
	edited := *loaded
	edited.DurationMinutes = 0 // Forms do not submit the stored duration.
	edited.Notes = "Updated notes"
	if err := app.updateSpaceSchedule(edited); err != nil {
		t.Fatalf("edit half-hour booking: %v", err)
	}
	loaded, err = app.findSpaceScheduleByID(tail.ID)
	if err != nil || loaded.DurationMinutes != 30 {
		t.Fatalf("duration changed after edit: %+v, %v", loaded, err)
	}
	next := schedule
	next.Title = "overlap"
	next.SlotHour = "19:15"
	if err := app.createSpaceSchedule(next); err == nil {
		t.Fatal("accepted overlapping booking")
	}
	next.Title = "adjacent"
	next.SlotHour = "19:30"
	if err := app.createSpaceSchedule(next); err != nil {
		t.Fatalf("adjacent booking: %v", err)
	}
}

func TestHalfHourBookingClosures(t *testing.T) {
	schedule := SpaceSchedule{SlotDate: "2026-10-01", SlotHour: "19:00", DurationMinutes: 30, Activity: "full_indoor_cricket"}
	closure := CourtClosure{ClosureDate: schedule.SlotDate, StartHour: "19:30", EndHour: "20:00", Active: true}
	if err := validateScheduleAgainstClosures(schedule, []CourtClosure{closure}); err != nil {
		t.Fatalf("closure after booking: %v", err)
	}
	closure.StartHour = "19:15"
	if err := validateScheduleAgainstClosures(schedule, []CourtClosure{closure}); err == nil {
		t.Fatal("accepted closure overlapping final half-hour")
	}
}

func TestHalfHourBookingRollsBackOnFinalConflict(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	schedule := SpaceSchedule{SlotDate: time.Now().AddDate(0, 0, 3).Format("2006-01-02"), SlotHour: "19:15", EntryType: "booking", Activity: "full_indoor_cricket", Quantity: 1, Title: "existing"}
	if err := app.createSpaceSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	schedule.SlotHour, schedule.Title = "18:00", "rollback"
	if err := app.createSpaceSchedules(schedule, 1.5); err == nil {
		t.Fatal("accepted conflicting final half-hour")
	}
	var count int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM space_schedules WHERE title = ?", schedule.Title).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("left %d bookings after failure", count)
	}
}
