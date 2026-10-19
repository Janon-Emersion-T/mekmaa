package main

import (
	"errors"
	"strings"
	"time"
)

type CourseAttendanceEnrollmentPeriod struct {
	EffectiveFrom string
	EffectiveTo   string
	Active        bool
}

func validCourseAttendanceDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

// courseAttendanceEnrollmentEligible evaluates historical enrollment
// periods. An empty EffectiveTo represents an open-ended period.
//
// EffectiveTo is treated as exclusive: the student is not eligible
// on the date that the next enrollment status begins.
func courseAttendanceEnrollmentEligible(
	attendanceDate string,
	enrollmentDate string,
	currentlyActive bool,
	periods []CourseAttendanceEnrollmentPeriod,
) (bool, error) {
	attendanceDate = strings.TrimSpace(attendanceDate)
	enrollmentDate = strings.TrimSpace(enrollmentDate)

	if !validCourseAttendanceDate(attendanceDate) {
		return false, errors.New("invalid attendance date")
	}
	if !validCourseAttendanceDate(enrollmentDate) {
		return false, errors.New("invalid enrollment date")
	}
	if attendanceDate < enrollmentDate {
		return false, nil
	}

	// Older enrollment records may predate the status-history
	// feature. Do not fabricate historical status transitions.
	if len(periods) == 0 {
		return currentlyActive, nil
	}

	for _, period := range periods {
		if !validCourseAttendanceDate(period.EffectiveFrom) {
			return false, errors.New("invalid enrollment history start")
		}
		if period.EffectiveTo != "" &&
			!validCourseAttendanceDate(period.EffectiveTo) {
			return false, errors.New("invalid enrollment history end")
		}
		if period.EffectiveTo != "" &&
			period.EffectiveTo <= period.EffectiveFrom {
			return false, errors.New("invalid enrollment history period")
		}

		if attendanceDate >= period.EffectiveFrom &&
			(period.EffectiveTo == "" ||
				attendanceDate < period.EffectiveTo) {
			return period.Active, nil
		}
	}

	return false, nil
}
