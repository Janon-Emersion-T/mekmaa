package main

import "testing"

func TestStudentGroupUpdatedMembersAllowRemovingUnenrolledStudent(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	previous := []int64{101, 102}

	err := app.validateStudentGroupUpdatedMembers(
		1, 1, previous, []int64{101},
	)
	if err != nil {
		t.Fatalf("removing an unenrolled member should succeed: %v", err)
	}
}

func TestStudentGroupUpdatedMembersAllowRemovingAllStudents(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	previous := []int64{101}

	err := app.validateStudentGroupUpdatedMembers(
		1, 1, previous, nil,
	)
	if err != nil {
		t.Fatalf("empty roster after removal should succeed: %v", err)
	}
}

func TestStudentGroupUpdatedMembersRejectNewUnenrolledStudent(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	previous := []int64{101}

	err := app.validateStudentGroupUpdatedMembers(
		1, 1, previous, []int64{101, 999999},
	)
	if err == nil {
		t.Fatal("new unenrolled student must be rejected")
	}
}

func TestStudentGroupUpdatedMembersRevalidateOnProgrammeChange(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	previous := []int64{101}

	err := app.validateStudentGroupUpdatedMembers(
		2, 1, previous, []int64{101},
	)
	if err == nil {
		t.Fatal("programme change must revalidate existing students")
	}
}
