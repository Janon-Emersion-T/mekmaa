package main

import "testing"

func TestPreviousEnrollmentDetailsRespectScopeAndMissingDates(t *testing.T) {
	a, e, destination := programmeTestFixture(t)
	newID, err := a.transferStudentEnrollment(e.ID, StudentEnrollment{TrainingProgramID: destination, EnrollmentDate: "2026-08-20", FreeAdmission: true})
	if err != nil {
		t.Fatal(err)
	}
	enrollments, err := a.listStudentEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	history, err := a.previousEnrollmentDetails(enrollments, e.AdmissionID)
	if err != nil || len(history) != 1 || history[0].ID != e.ID || history[0].EndDate != "2026-08-20" {
		t.Fatalf("history %v: %v", history, err)
	}
	// A caller's division scope must not be expanded when loading history.
	var activeOnly []StudentEnrollment
	for _, row := range enrollments {
		if row.ID == newID {
			activeOnly = append(activeOnly, row)
		}
	}
	history, err = a.previousEnrollmentDetails(activeOnly, e.AdmissionID)
	if err != nil || len(history) != 0 {
		t.Fatalf("history outside supplied scope %v: %v", history, err)
	}
	history, err = a.previousEnrollmentDetails(enrollments, e.AdmissionID+1)
	if err != nil || len(history) != 0 {
		t.Fatalf("another student's history %v: %v", history, err)
	}
	programmeTestExec(t, a, `DELETE FROM student_enrollment_status_history WHERE enrollment_id = ?`, e.ID)
	history, err = a.previousEnrollmentDetails(enrollments, e.AdmissionID)
	if err != nil || len(history) != 1 || history[0].EndDate != "" {
		t.Fatalf("missing date must remain unknown: %v %v", history, err)
	}
}
