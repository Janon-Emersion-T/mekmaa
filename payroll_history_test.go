package main

import (
	"database/sql"
	"testing"
)

type membershipHistoryRow struct {
	AdmissionID   int64
	EffectiveFrom string
	EffectiveTo   sql.NullString
}

type staffHistoryRow struct {
	UserID            int64
	AssignmentRole    string
	PrimaryAssignment bool
	EffectiveFrom     string
	EffectiveTo       sql.NullString
}

type enrollmentHistoryRow struct {
	Active        bool
	EffectiveFrom string
	EffectiveTo   sql.NullString
}

func listMembershipHistoryRows(t *testing.T, app *App, groupID int64) []membershipHistoryRow {
	t.Helper()

	rows, err := app.db.Query(`
		SELECT admission_id, effective_from, effective_to
		FROM student_group_membership_history
		WHERE group_id = ?
		ORDER BY admission_id ASC, effective_from ASC, id ASC
	`, groupID)
	if err != nil {
		t.Fatalf("list membership history: %v", err)
	}
	defer rows.Close()

	result := make([]membershipHistoryRow, 0)
	for rows.Next() {
		var row membershipHistoryRow
		if err := rows.Scan(&row.AdmissionID, &row.EffectiveFrom, &row.EffectiveTo); err != nil {
			t.Fatalf("scan membership history: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("membership history rows: %v", err)
	}

	return result
}

func listStaffHistoryRows(t *testing.T, app *App, groupID int64) []staffHistoryRow {
	t.Helper()

	rows, err := app.db.Query(`
		SELECT
			user_id,
			assignment_role,
			COALESCE(primary_assignment, 0),
			effective_from,
			effective_to
		FROM student_group_staff_assignment_history
		WHERE group_id = ?
		ORDER BY user_id ASC, effective_from ASC, id ASC
	`, groupID)
	if err != nil {
		t.Fatalf("list staff history: %v", err)
	}
	defer rows.Close()

	result := make([]staffHistoryRow, 0)
	for rows.Next() {
		var row staffHistoryRow
		var primary int
		if err := rows.Scan(&row.UserID, &row.AssignmentRole, &primary, &row.EffectiveFrom, &row.EffectiveTo); err != nil {
			t.Fatalf("scan staff history: %v", err)
		}
		row.PrimaryAssignment = primary == 1
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("staff history rows: %v", err)
	}

	return result
}

func listEnrollmentHistoryRows(t *testing.T, app *App, enrollmentID int64) []enrollmentHistoryRow {
	t.Helper()

	rows, err := app.db.Query(`
		SELECT active, effective_from, effective_to
		FROM student_enrollment_status_history
		WHERE enrollment_id = ?
		ORDER BY effective_from ASC, id ASC
	`, enrollmentID)
	if err != nil {
		t.Fatalf("list enrollment history: %v", err)
	}
	defer rows.Close()

	result := make([]enrollmentHistoryRow, 0)
	for rows.Next() {
		var row enrollmentHistoryRow
		var active int
		if err := rows.Scan(&active, &row.EffectiveFrom, &row.EffectiveTo); err != nil {
			t.Fatalf("scan enrollment history: %v", err)
		}
		row.Active = active == 1
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("enrollment history rows: %v", err)
	}

	return result
}

func TestStudentGroupMembershipHistorySemantics(t *testing.T) {
	app := newAuthorizationTestApp(t)

	programID := createPayrollTestProgram(t, app, "Membership Program", "membership-program")
	firstAdmissionID := createPayrollTestAdmission(t, app, "STD-HIST-M-001", "Membership One")
	secondAdmissionID := createPayrollTestAdmission(t, app, "STD-HIST-M-002", "Membership Two")
	groupID := createPayrollTestGroup(t, app, "Membership Group", "MEMBERSHIP-GRP", programID, nil, nil, nil)

	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := syncStudentGroupMembershipHistoryTx(app, tx, groupID, []int64{firstAdmissionID}, "2026-08-01"); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit initial tx: %v", err)
	}

	rows := listMembershipHistoryRows(t, app, groupID)
	if len(rows) != 1 || rows[0].EffectiveFrom != "2026-08-01" || rows[0].EffectiveTo.Valid {
		t.Fatalf("initial membership history = %#v", rows)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin unchanged tx: %v", err)
	}
	if err := syncStudentGroupMembershipHistoryTx(app, tx, groupID, []int64{firstAdmissionID}, "2026-08-01"); err != nil {
		t.Fatalf("unchanged sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit unchanged tx: %v", err)
	}
	if got := len(listMembershipHistoryRows(t, app, groupID)); got != 1 {
		t.Fatalf("membership history row count after unchanged sync = %d, want 1", got)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin replacement tx: %v", err)
	}
	if err := syncStudentGroupMembershipHistoryTx(app, tx, groupID, []int64{secondAdmissionID}, "2026-09-01"); err != nil {
		t.Fatalf("replacement sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit replacement tx: %v", err)
	}

	rows = listMembershipHistoryRows(t, app, groupID)
	if len(rows) != 2 {
		t.Fatalf("membership history row count after replacement = %d, want 2", len(rows))
	}
	if !rows[0].EffectiveTo.Valid || rows[0].EffectiveTo.String != "2026-09-01" {
		t.Fatalf("closed membership row = %#v", rows[0])
	}
	if rows[1].AdmissionID != secondAdmissionID || rows[1].EffectiveFrom != "2026-09-01" || rows[1].EffectiveTo.Valid {
		t.Fatalf("replacement membership row = %#v", rows[1])
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin same-day tx: %v", err)
	}
	if err := syncStudentGroupMembershipHistoryTx(app, tx, groupID, nil, "2026-09-01"); err != nil {
		t.Fatalf("same-day removal sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit same-day tx: %v", err)
	}

	rows = listMembershipHistoryRows(t, app, groupID)
	if len(rows) != 1 {
		t.Fatalf("membership history row count after same-day removal = %d, want 1", len(rows))
	}
	if rows[0].AdmissionID != firstAdmissionID || !rows[0].EffectiveTo.Valid || rows[0].EffectiveTo.String != "2026-09-01" {
		t.Fatalf("remaining membership history = %#v", rows)
	}
}

func TestStudentGroupStaffHistorySemantics(t *testing.T) {
	app := newAuthorizationTestApp(t)

	programID := createPayrollTestProgram(t, app, "Staff Program", "staff-program")
	groupID := createPayrollTestGroup(t, app, "Staff Group", "STAFF-GRP", programID, nil, nil, nil)
	teacher, err := app.createManagedUser("Teacher", "teacher-history@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create teacher: %v", err)
	}
	coach, err := app.createManagedUser("Coach", "coach-history@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}
	replacement, err := app.createManagedUser("Replacement", "replacement-history@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}

	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, []GroupStaffAssignmentInput{{UserID: teacher.ID, AssignmentRole: groupStaffRoleTeacher}}, "2026-08-01"); err != nil {
		t.Fatalf("initial staff sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit initial tx: %v", err)
	}

	rows := listStaffHistoryRows(t, app, groupID)
	if len(rows) != 1 || rows[0].UserID != teacher.ID || rows[0].AssignmentRole != groupStaffRoleTeacher || rows[0].EffectiveFrom != "2026-08-01" || rows[0].EffectiveTo.Valid {
		t.Fatalf("initial staff history = %#v", rows)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin unchanged tx: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, []GroupStaffAssignmentInput{{UserID: teacher.ID, AssignmentRole: groupStaffRoleTeacher}}, "2026-08-01"); err != nil {
		t.Fatalf("unchanged staff sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit unchanged tx: %v", err)
	}
	if got := len(listStaffHistoryRows(t, app, groupID)); got != 1 {
		t.Fatalf("staff history row count after unchanged sync = %d, want 1", got)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin role change tx: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, []GroupStaffAssignmentInput{{UserID: teacher.ID, AssignmentRole: groupStaffRoleCoach, PrimaryAssignment: true}}, "2026-09-01"); err != nil {
		t.Fatalf("role change sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit role change tx: %v", err)
	}

	rows = listStaffHistoryRows(t, app, groupID)
	if len(rows) != 2 {
		t.Fatalf("staff history row count after role change = %d, want 2", len(rows))
	}
	if !rows[0].EffectiveTo.Valid || rows[0].EffectiveTo.String != "2026-09-01" {
		t.Fatalf("closed staff row = %#v", rows[0])
	}
	if rows[1].AssignmentRole != groupStaffRoleCoach || !rows[1].PrimaryAssignment || rows[1].EffectiveFrom != "2026-09-01" {
		t.Fatalf("replacement staff row = %#v", rows[1])
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin removal tx: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, nil, "2026-10-01"); err != nil {
		t.Fatalf("staff removal sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit removal tx: %v", err)
	}

	rows = listStaffHistoryRows(t, app, groupID)
	if len(rows) != 2 || !rows[1].EffectiveTo.Valid || rows[1].EffectiveTo.String != "2026-10-01" {
		t.Fatalf("staff history after removal = %#v", rows)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin legacy coach tx: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, []GroupStaffAssignmentInput{{UserID: teacher.ID, AssignmentRole: groupStaffRoleTeacher}}, "2026-10-15"); err != nil {
		t.Fatalf("restore teacher staff history: %v", err)
	}
	if err := syncStudentGroupCoachHistoryTx(app, tx, groupID, []int64{coach.ID}, "2026-10-15"); err != nil {
		t.Fatalf("open coach history: %v", err)
	}
	if err := syncStudentGroupCoachHistoryTx(app, tx, groupID, []int64{replacement.ID}, "2026-11-01"); err != nil {
		t.Fatalf("replace coach history: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy coach tx: %v", err)
	}

	rows = listStaffHistoryRows(t, app, groupID)
	teacherOpen := false
	for _, row := range rows {
		if row.UserID == teacher.ID && row.AssignmentRole == groupStaffRoleTeacher && row.EffectiveFrom == "2026-10-15" && !row.EffectiveTo.Valid {
			teacherOpen = true
		}
	}
	if !teacherOpen {
		t.Fatalf("teacher history should remain open after legacy coach changes: %#v", rows)
	}
}

func TestStudentEnrollmentStatusHistorySemantics(t *testing.T) {
	app := newAuthorizationTestApp(t)

	programID := createPayrollTestProgram(t, app, "Enrollment Program", "enrollment-program")
	admissionID := createPayrollTestAdmission(t, app, "STD-HIST-E-001", "Enrollment History")
	enrollmentID := createPayrollTestEnrollment(t, app, admissionID, programID, "2026-08-05")

	rows := listEnrollmentHistoryRows(t, app, enrollmentID)
	if len(rows) != 1 || !rows[0].Active || rows[0].EffectiveFrom != "2026-08-05" || rows[0].EffectiveTo.Valid {
		t.Fatalf("initial enrollment history = %#v", rows)
	}

	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin inactive tx: %v", err)
	}
	if err := syncStudentEnrollmentStatusHistoryTx(app, tx, enrollmentID, false, "2026-09-01"); err != nil {
		t.Fatalf("inactive sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit inactive tx: %v", err)
	}

	rows = listEnrollmentHistoryRows(t, app, enrollmentID)
	if len(rows) != 2 || !rows[0].EffectiveTo.Valid || rows[0].EffectiveTo.String != "2026-09-01" || rows[1].Active || rows[1].EffectiveFrom != "2026-09-01" {
		t.Fatalf("inactive enrollment history = %#v", rows)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin active tx: %v", err)
	}
	if err := syncStudentEnrollmentStatusHistoryTx(app, tx, enrollmentID, true, "2026-10-01"); err != nil {
		t.Fatalf("reactivate sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit active tx: %v", err)
	}

	rows = listEnrollmentHistoryRows(t, app, enrollmentID)
	if len(rows) != 3 || !rows[1].EffectiveTo.Valid || rows[1].EffectiveTo.String != "2026-10-01" || !rows[2].Active || rows[2].EffectiveFrom != "2026-10-01" {
		t.Fatalf("reactivated enrollment history = %#v", rows)
	}

	tx, err = app.db.Begin()
	if err != nil {
		t.Fatalf("begin same-day tx: %v", err)
	}
	if err := syncStudentEnrollmentStatusHistoryTx(app, tx, enrollmentID, false, "2026-10-01"); err != nil {
		t.Fatalf("same-day inactive sync: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit same-day tx: %v", err)
	}

	rows = listEnrollmentHistoryRows(t, app, enrollmentID)
	if len(rows) != 3 || rows[2].Active || rows[2].EffectiveFrom != "2026-10-01" || rows[2].EffectiveTo.Valid {
		t.Fatalf("same-day enrollment transition should replace the open row without zero-length history: %#v", rows)
	}
}

func TestUpdateStudentEnrollmentAdjustsInitialHistoryStart(t *testing.T) {
	app := newAuthorizationTestApp(t)

	programID := createPayrollTestProgram(t, app, "Enrollment Update Program", "enrollment-update-program")
	admissionID := createPayrollTestAdmission(t, app, "STD-HIST-E-002", "Enrollment Update")
	enrollmentID := createPayrollTestEnrollment(t, app, admissionID, programID, "2026-08-01")

	if err := app.updateStudentEnrollment(StudentEnrollment{
		ID:                enrollmentID,
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
		EnrollmentDate:    "2026-08-06",
	}); err != nil {
		t.Fatalf("update student enrollment: %v", err)
	}

	rows := listEnrollmentHistoryRows(t, app, enrollmentID)
	if len(rows) != 1 || rows[0].EffectiveFrom != "2026-08-06" || !rows[0].Active {
		t.Fatalf("updated enrollment history = %#v", rows)
	}
}

func TestCurrentBusinessDateUsesSriLankaTimezone(t *testing.T) {
	got := currentBusinessTime()
	if got.Location().String() != sriLankaLocation.String() {
		t.Fatalf("business location = %q, want %q", got.Location().String(), sriLankaLocation.String())
	}
	if currentBusinessDate() != got.Format("2006-01-02") {
		t.Fatalf("business date = %q, want %q", currentBusinessDate(), got.Format("2006-01-02"))
	}
}
