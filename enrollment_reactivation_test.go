package main

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

var reenrollmentTestSequence atomic.Int64

func createReactivationTestEnrollment(t *testing.T) (*App, int64) {
	t.Helper()

	app := newBookingWorkflowTestApp(t)

	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Re-enrollment Test Programme",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2500,
		Active:         true,
	})
	if err != nil {
		t.Fatal(err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(
		Admission{
			StudentID:             fmt.Sprintf("REENROLL-TEST-%d", reenrollmentTestSequence.Add(1)),
			FullName:              "Returning Student",
			AdmissionDate:         "2026-08-01",
			DateOfBirth:           "2012-01-01",
			Gender:                "male",
			PracticeType:          "group_practice",
			Address:               "Jaffna",
			GuardianName:          "Guardian",
			GuardianRelationship:  "Parent",
			GuardianContactNumber: "0771000202",
		},
		false, "cash", 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(
		StudentEnrollment{
			AdmissionID:       admissionID,
			TrainingProgramID: programID,
			EnrollmentDate:    "2026-08-01",
		},
		false, "cash", 0,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.deactivateStudentEnrollment(
		enrollmentID, "2026-08-20",
	); err != nil {
		t.Fatal(err)
	}

	return app, enrollmentID
}

func TestReenrollmentRestoresArchivedEnrollment(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)

	if err := app.reactivateStudentEnrollment(
		enrollmentID, "2026-09-01",
	); err != nil {
		t.Fatal(err)
	}

	enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Active {
		t.Fatal("enrollment should be active")
	}

	rows, err := app.db.Query(`
        SELECT active, CAST(effective_from AS TEXT),
               COALESCE(CAST(effective_to AS TEXT), '')
        FROM student_enrollment_status_history
        WHERE enrollment_id = ?
        ORDER BY effective_from, id
    `, enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type period struct {
		active int
		from   string
		to     string
	}
	var periods []period
	for rows.Next() {
		var p period
		if err := rows.Scan(&p.active, &p.from, &p.to); err != nil {
			t.Fatal(err)
		}
		periods = append(periods, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(periods) != 3 {
		t.Fatalf("expected 3 historical periods, got %+v", periods)
	}
	if periods[0].active != 1 ||
		periods[0].to != "2026-08-20" ||
		periods[1].active != 0 ||
		periods[1].to != "2026-09-01" ||
		periods[2].active != 1 ||
		periods[2].from != "2026-09-01" ||
		periods[2].to != "" {
		t.Fatalf("incorrect re-enrollment history: %+v", periods)
	}
}

func TestReenrollmentRejectsInvalidDates(t *testing.T) {
	for _, date := range []string{
		"",
		"invalid",
		"2026-08-01",
		"2026-08-20",
		"2099-01-01",
	} {
		t.Run(strings.ReplaceAll(date, "/", "_"), func(t *testing.T) {
			app, enrollmentID := createReactivationTestEnrollment(t)

			if err := app.reactivateStudentEnrollment(
				enrollmentID, date,
			); err == nil {
				t.Fatalf("expected rejection for %q", date)
			}

			enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
			if err != nil {
				t.Fatal(err)
			}
			if enrollment.Active {
				t.Fatal("invalid request changed enrollment status")
			}
		})
	}
}

func TestReenrollmentRejectsRepeatedActivation(t *testing.T) {
	app, enrollmentID := createReactivationTestEnrollment(t)

	if err := app.reactivateStudentEnrollment(
		enrollmentID, "2026-09-01",
	); err != nil {
		t.Fatal(err)
	}

	if err := app.reactivateStudentEnrollment(
		enrollmentID, "2026-09-02",
	); err == nil {
		t.Fatal("second activation must be rejected")
	}
}

func TestReenrollmentRouteAndTemplatePresent(t *testing.T) {
	content, err := os.ReadFile("templates/dashboard/enrollment-management.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(content)

	if !strings.Contains(
		html, `action="/admin/enrollments/reenroll"`,
	) {
		t.Fatal("re-enrollment form missing")
	}

	if !strings.Contains(
		html, `name="reenrollment_date"`,
	) {
		t.Fatal("re-enrollment date missing")
	}
}
