package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func createPayrollTestAdmission(t *testing.T, app *App, studentID, fullName string) int64 {
	t.Helper()

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:                studentID,
		FullName:                 fullName,
		AdmissionDate:            "2026-01-10",
		DateOfBirth:              "2012-06-01",
		Gender:                   "male",
		PracticeType:             "group_practice",
		Address:                  "Jaffna",
		School:                   "Mekmaa College",
		GuardianName:             "Guardian",
		GuardianRelationship:     "Parent",
		GuardianContactNumber:    "0771234567",
		GuardianAlternativePhone: "0771234568",
		MedicalInformation:       "",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission %s: %v", studentID, err)
	}

	return admissionID
}

func createPayrollTestProgram(t *testing.T, app *App, name, activity string) int64 {
	t.Helper()

	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           name,
		Activity:       activity,
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     3000,
		Active:         true,
		SortOrder:      1,
	})
	if err != nil {
		t.Fatalf("create training program %s: %v", name, err)
	}

	return programID
}

func createPayrollTestEnrollment(t *testing.T, app *App, admissionID, programID int64, enrollmentDate string) int64 {
	t.Helper()

	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
		EnrollmentDate:    enrollmentDate,
		Active:            true,
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create enrollment admission=%d program=%d: %v", admissionID, programID, err)
	}

	return enrollmentID
}

func createPayrollTestGroup(t *testing.T, app *App, name, code string, programID int64, admissionIDs []int64, coachIDs []int64, sessions []StudentGroupSession) int64 {
	t.Helper()

	if err := app.createStudentGroup(StudentGroup{
		Name:              name,
		Code:              code,
		Description:       name,
		TrainingProgramID: programID,
	}, admissionIDs, coachIDs, sessions); err != nil {
		t.Fatalf("create group %s: %v", code, err)
	}

	var groupID int64
	if err := app.db.QueryRow(`SELECT id FROM student_groups WHERE code = ?`, code).Scan(&groupID); err != nil {
		t.Fatalf("lookup group %s: %v", code, err)
	}
	// Payroll fixtures calculate historical periods. Make the group membership
	// and coach assignment effective before those periods instead of inheriting
	// the wall-clock date at test execution.
	if _, err := app.db.Exec(`UPDATE student_group_membership_history SET effective_from = '2026-01-01' WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("backdate group membership history: %v", err)
	}
	if _, err := app.db.Exec(`UPDATE student_group_staff_assignment_history SET effective_from = '2026-01-01' WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("backdate group staff history: %v", err)
	}
	return groupID
}

func createPayrollTestSalaryProfile(t *testing.T, app *App, profile StaffSalaryProfile, actorUserID int64) int64 {
	t.Helper()

	profileID, err := app.createStaffSalaryProfile(profile, actorUserID)
	if err != nil {
		t.Fatalf("create salary profile: %v", err)
	}
	return profileID
}

func payrollPaymentForProfile(t *testing.T, app *App, runID, profileID int64) PayrollPayment {
	t.Helper()

	run, err := app.findPayrollRunByID(runID)
	if err != nil {
		t.Fatalf("find payroll run: %v", err)
	}
	for _, payment := range run.Payments {
		if payment.SalaryProfileID == profileID {
			return payment
		}
	}
	t.Fatalf("payment for profile %d not found", profileID)
	return PayrollPayment{}
}

func TestPayrollPhase2PerStudentActiveEnrollmentCalculation(t *testing.T) {
	app := newAuthorizationTestApp(t)

	coach, err := app.createManagedUser("Main Coach", "main-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	programID := createPayrollTestProgram(t, app, "Cricket Monthly", "cricket")
	otherProgramID := createPayrollTestProgram(t, app, "Badminton Monthly", "badminton")

	activeID := createPayrollTestAdmission(t, app, "STD-PAY-001", "Active Student")
	createPayrollTestEnrollment(t, app, activeID, programID, "2026-07-01")

	lateEnrollID := createPayrollTestAdmission(t, app, "STD-PAY-002", "Late Student")
	createPayrollTestEnrollment(t, app, lateEnrollID, programID, "2026-09-01")

	fullLeaveID := createPayrollTestAdmission(t, app, "STD-PAY-003", "Full Leave Student")
	fullLeaveEnrollmentID := createPayrollTestEnrollment(t, app, fullLeaveID, programID, "2026-06-15")
	if err := app.createStudentEnrollmentLeave(fullLeaveEnrollmentID, "2026-08-01", "2026-08-31", "Medical leave"); err != nil {
		t.Fatalf("create full leave: %v", err)
	}

	spanningLeaveID := createPayrollTestAdmission(t, app, "STD-PAY-004", "Spanning Leave Student")
	spanningLeaveEnrollmentID := createPayrollTestEnrollment(t, app, spanningLeaveID, programID, "2026-05-01")
	if err := app.createStudentEnrollmentLeave(spanningLeaveEnrollmentID, "2026-07-15", "2026-09-10", "Extended leave"); err != nil {
		t.Fatalf("create spanning leave: %v", err)
	}

	partialLeaveID := createPayrollTestAdmission(t, app, "STD-PAY-005", "Partial Leave Student")
	partialLeaveEnrollmentID := createPayrollTestEnrollment(t, app, partialLeaveID, programID, "2026-04-10")
	if err := app.createStudentEnrollmentLeave(partialLeaveEnrollmentID, "2026-08-15", "2026-08-20", "Short leave"); err != nil {
		t.Fatalf("create partial leave: %v", err)
	}

	duplicateID := createPayrollTestAdmission(t, app, "STD-PAY-006", "Duplicate Group Student")
	createPayrollTestEnrollment(t, app, duplicateID, programID, "2026-02-01")

	unrelatedProgramID := createPayrollTestAdmission(t, app, "STD-PAY-007", "Unrelated Programme Student")
	createPayrollTestEnrollment(t, app, unrelatedProgramID, otherProgramID, "2026-02-01")

	unassignedGroupID := createPayrollTestAdmission(t, app, "STD-PAY-008", "Same Programme Unassigned Group")
	createPayrollTestEnrollment(t, app, unassignedGroupID, programID, "2026-02-01")

	sessions := []StudentGroupSession{{Title: "Saturday", DayOfWeek: "saturday", StartTime: "16:00", EndTime: "18:00", Active: true}}
	createPayrollTestGroup(t, app, "Assigned Group A", "PAY-GRP-A", programID, []int64{activeID, lateEnrollID, fullLeaveID, spanningLeaveID, partialLeaveID, duplicateID}, []int64{coach.ID}, sessions)
	createPayrollTestGroup(t, app, "Assigned Group B", "PAY-GRP-B", programID, []int64{duplicateID}, []int64{coach.ID}, sessions)
	createPayrollTestGroup(t, app, "Unassigned Group", "PAY-GRP-C", programID, []int64{unassignedGroupID}, nil, sessions)
	createPayrollTestGroup(t, app, "Other Program Group", "PAY-GRP-D", otherProgramID, []int64{unrelatedProgramID}, []int64{coach.ID}, sessions)

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            coach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypePerStudent,
		Rate:              1100,
		StudentBasis:      SalaryStudentBasisActiveEnrollment,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, coach.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", coach.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}

	if err := app.generatePayrollRunPayments(runID, coach.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payment.Quantity != 3 {
		t.Fatalf("quantity = %.2f, want 3", payment.Quantity)
	}
	if payment.BaseAmount != 3300 {
		t.Fatalf("base amount = %.2f, want 3300", payment.BaseAmount)
	}
	if payment.QuantityLabel != "3 eligible students" {
		t.Fatalf("quantity label = %q", payment.QuantityLabel)
	}

	included := 0
	excluded := 0
	for _, detail := range payment.CalculationDetails {
		switch detail.DetailType {
		case payrollDetailTypePerStudentIncluded:
			included++
		case payrollDetailTypePerStudentExcludedFullLeave:
			excluded++
		}
	}
	if included != 3 {
		t.Fatalf("included student details = %d, want 3", included)
	}
	if excluded != 2 {
		t.Fatalf("excluded leave details = %d, want 2", excluded)
	}
}

func TestPayrollPhase2PerStudentManualFallbacks(t *testing.T) {
	app := newAuthorizationTestApp(t)

	coach, err := app.createManagedUser("Coach", "manual-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	noProgrammeProfileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:           coach.ID,
		CompensationType: SalaryTypePerStudent,
		Rate:             1100,
		StudentBasis:     SalaryStudentBasisActiveEnrollment,
		EffectiveFrom:    "2026-01-01",
		Active:           true,
	}, coach.ID)

	attendanceProfileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            coach.ID,
		CompensationType:  SalaryTypePerStudent,
		Rate:              1100,
		StudentBasis:      SalaryStudentBasisAttendance,
		TrainingProgramID: createPayrollTestProgram(t, app, "Attendance Program", "cricket-attendance"),
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, coach.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", coach.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, coach.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	noProgramme := payrollPaymentForProfile(t, app, runID, noProgrammeProfileID)
	if noProgramme.Status != PayrollPaymentStatusDraft || !payrollPaymentAllowsManualQuantity(noProgramme) {
		t.Fatalf("no-programme per-student payment should be manual draft: %#v", noProgramme)
	}

	attendance := payrollPaymentForProfile(t, app, runID, attendanceProfileID)
	if attendance.Status != PayrollPaymentStatusCalculated || attendance.Quantity != 0 || attendance.BaseAmount != 0 {
		t.Fatalf("attendance-basis per-student payment without records should auto-calculate to zero: %#v", attendance)
	}
}

func TestPayrollPhase2DailyCalculation(t *testing.T) {
	app := newAuthorizationTestApp(t)

	staff, err := app.createManagedUser("Daily Staff", "daily-staff@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}

	if err := app.saveStaffAttendanceRecords("2026-08-03", []StaffAttendanceInput{{UserID: staff.ID, Status: "present"}}, staff.ID); err != nil {
		t.Fatalf("save first attendance: %v", err)
	}
	if err := app.saveStaffAttendanceRecords("2026-08-04", []StaffAttendanceInput{{UserID: staff.ID, Status: "late"}}, staff.ID); err != nil {
		t.Fatalf("save second attendance: %v", err)
	}
	if err := app.saveStaffAttendanceRecords("2026-08-05", []StaffAttendanceInput{{UserID: staff.ID, Status: "absent"}}, staff.ID); err != nil {
		t.Fatalf("save absent attendance: %v", err)
	}
	if err := app.saveStaffAttendanceRecords("2026-08-06", []StaffAttendanceInput{{UserID: staff.ID, Status: "excused"}}, staff.ID); err != nil {
		t.Fatalf("save excused attendance: %v", err)
	}

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:           staff.ID,
		CompensationType: SalaryTypeDaily,
		Rate:             2500,
		EffectiveFrom:    "2026-01-01",
		Active:           true,
	}, staff.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", staff.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, staff.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payment.Status != PayrollPaymentStatusCalculated || payment.Quantity != 2 || payment.BaseAmount != 5000 {
		t.Fatalf("daily payment = %#v", payment)
	}

	detailCount := 0
	for _, detail := range payment.CalculationDetails {
		if detail.DetailType == payrollDetailTypeDailyAttendance {
			detailCount++
		}
	}
	if detailCount != 2 {
		t.Fatalf("daily detail count = %d, want 2", detailCount)
	}
}

func TestPayrollPhase2HourlyCalculation(t *testing.T) {
	app := newAuthorizationTestApp(t)

	staff, err := app.createManagedUser("Hourly Staff", "hourly-staff@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}

	now := time.Now().UTC()
	for _, record := range []struct {
		workDate     string
		clockIn      string
		clockOut     string
		breakMinutes int
	}{
		{"2026-08-02", "09:00", "17:00", 60},
		{"2026-08-03", "22:00", "01:00", 30},
	} {
		if _, err := app.db.Exec(`
			INSERT INTO staff_work_time_records (
				user_id,
				work_date,
				clock_in,
				clock_out,
				break_minutes,
				note,
				recorded_by_user_id,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, '', ?, ?, ?)
		`,
			staff.ID,
			record.workDate,
			record.clockIn,
			record.clockOut,
			record.breakMinutes,
			staff.ID,
			now,
			now,
		); err != nil {
			t.Fatalf("insert work time record: %v", err)
		}
	}

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:           staff.ID,
		CompensationType: SalaryTypeHourly,
		Rate:             1000,
		EffectiveFrom:    "2026-01-01",
		Active:           true,
	}, staff.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", staff.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, staff.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payment.Status != PayrollPaymentStatusCalculated {
		t.Fatalf("hourly payment status = %q", payment.Status)
	}
	if payment.Quantity != 9.5 {
		t.Fatalf("hourly quantity = %.2f, want 9.50", payment.Quantity)
	}
	if payment.BaseAmount != 9500 {
		t.Fatalf("hourly base amount = %.2f, want 9500", payment.BaseAmount)
	}

	detailCount := 0
	for _, detail := range payment.CalculationDetails {
		if detail.DetailType == payrollDetailTypeHourlyWorkRecord {
			detailCount++
		}
	}
	if detailCount != 2 {
		t.Fatalf("hourly detail count = %d, want 2", detailCount)
	}
}

func TestPayrollPhase2HistoricalRecalculationUsesHistory(t *testing.T) {
	app := newAuthorizationTestApp(t)

	coach, err := app.createManagedUser("History Coach", "history-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}
	replacement, err := app.createManagedUser("Replacement Coach", "replacement-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create replacement coach: %v", err)
	}

	programID := createPayrollTestProgram(t, app, "History Program", "history-program")
	admissionID := createPayrollTestAdmission(t, app, "STD-HIST-001", "Historical Student")
	createPayrollTestEnrollment(t, app, admissionID, programID, "2026-07-15")
	groupID := createPayrollTestGroup(
		t,
		app,
		"History Group",
		"HISTORY-GRP",
		programID,
		[]int64{admissionID},
		[]int64{coach.ID},
		[]StudentGroupSession{{Title: "Saturday", DayOfWeek: "saturday", StartTime: "16:00", EndTime: "18:00", Active: true}},
	)

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            coach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypePerStudent,
		Rate:              1000,
		StudentBasis:      SalaryStudentBasisActiveEnrollment,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, coach.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", coach.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, coach.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	initial := payrollPaymentForProfile(t, app, runID, profileID)
	if initial.Quantity != 1 {
		t.Fatalf("initial historical payment = %#v", initial)
	}

	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin history tx: %v", err)
	}
	if err := syncStudentGroupMembershipHistoryTx(app, tx, groupID, nil, "2026-09-01"); err != nil {
		t.Fatalf("close membership history: %v", err)
	}
	if err := syncStudentGroupStaffHistoryTx(app, tx, groupID, []GroupStaffAssignmentInput{{UserID: replacement.ID, AssignmentRole: groupStaffRoleCoach}}, "2026-09-01"); err != nil {
		t.Fatalf("replace staff history: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit history tx: %v", err)
	}

	if _, err := app.db.Exec(`DELETE FROM student_group_members WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("delete current membership: %v", err)
	}
	if _, err := app.db.Exec(`DELETE FROM student_group_staff WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("delete current staff: %v", err)
	}
	if _, err := app.db.Exec(`DELETE FROM student_group_coaches WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("delete current coach: %v", err)
	}
	if _, err := app.db.Exec(`INSERT INTO student_group_staff (group_id, user_id, assignment_role, primary_assignment, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, groupID, replacement.ID, groupStaffRoleCoach, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("insert replacement staff: %v", err)
	}
	if _, err := app.db.Exec(`INSERT INTO student_group_coaches (group_id, user_id, created_at) VALUES (?, ?, ?)`, groupID, replacement.ID, time.Now().UTC()); err != nil {
		t.Fatalf("insert replacement coach: %v", err)
	}

	if err := app.recalculatePayrollRun(runID, coach.ID); err != nil {
		t.Fatalf("recalculate payroll run: %v", err)
	}

	recalculated := payrollPaymentForProfile(t, app, runID, profileID)
	if recalculated.Quantity != 1 || recalculated.BaseAmount != 1000 {
		t.Fatalf("recalculated historical payment = %#v", recalculated)
	}
}

func TestPayrollPhase2HistoricalRecalculationIgnoresLaterEnrollmentDeactivation(t *testing.T) {
	app := newAuthorizationTestApp(t)

	coach, err := app.createManagedUser("Enrollment History Coach", "enrollment-history-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	programID := createPayrollTestProgram(t, app, "Enrollment History Program", "enrollment-history-program")
	admissionID := createPayrollTestAdmission(t, app, "STD-HIST-ENR-001", "Enrollment Stable Student")
	enrollmentID := createPayrollTestEnrollment(t, app, admissionID, programID, "2026-07-01")
	createPayrollTestGroup(
		t,
		app,
		"Enrollment History Group",
		"ENROLLMENT-HISTORY-GRP",
		programID,
		[]int64{admissionID},
		[]int64{coach.ID},
		nil,
	)

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            coach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypePerStudent,
		Rate:              800,
		StudentBasis:      SalaryStudentBasisActiveEnrollment,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, coach.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", coach.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, coach.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	initial := payrollPaymentForProfile(t, app, runID, profileID)
	if initial.Quantity != 1 {
		t.Fatalf("initial payment = %#v", initial)
	}

	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin enrollment history tx: %v", err)
	}
	if err := syncStudentEnrollmentStatusHistoryTx(app, tx, enrollmentID, false, "2026-09-01"); err != nil {
		t.Fatalf("deactivate enrollment history: %v", err)
	}
	if _, err := tx.Exec(`UPDATE student_enrollments SET active = 0, updated_at = ? WHERE id = ?`, time.Now().UTC(), enrollmentID); err != nil {
		t.Fatalf("update current enrollment active flag: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit enrollment history tx: %v", err)
	}

	if err := app.recalculatePayrollRun(runID, coach.ID); err != nil {
		t.Fatalf("recalculate payroll run: %v", err)
	}

	recalculated := payrollPaymentForProfile(t, app, runID, profileID)
	if recalculated.Quantity != 1 || recalculated.BaseAmount != 800 {
		t.Fatalf("recalculated payment after later deactivation = %#v", recalculated)
	}
}

func TestPayrollPhase2PerSessionCalculation(t *testing.T) {
	app := newAuthorizationTestApp(t)

	subCoach, err := app.createManagedUser("Sub Coach", "sub-coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create sub coach: %v", err)
	}

	programID := createPayrollTestProgram(t, app, "Cricket Sessions", "cricket-session")
	otherProgramID := createPayrollTestProgram(t, app, "Other Sessions", "other-session")
	sessions := []StudentGroupSession{{Title: "Saturday", DayOfWeek: "saturday", StartTime: "16:00", EndTime: "18:00", Active: true}}
	groupID := createPayrollTestGroup(t, app, "Session Group", "SESSION-GRP-A", programID, nil, []int64{subCoach.ID}, sessions)
	otherGroupID := createPayrollTestGroup(t, app, "Other Group", "SESSION-GRP-B", otherProgramID, nil, nil, sessions)

	group, err := app.findStudentGroupByID(groupID)
	if err != nil {
		t.Fatalf("find group: %v", err)
	}
	otherGroup, err := app.findStudentGroupByID(otherGroupID)
	if err != nil {
		t.Fatalf("find other group: %v", err)
	}

	actor, err := app.createManagedUser("Payroll Admin", "session-admin@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}

	save := func(groupID int64, timetableSessionID int64, date, status, workStatus string, adHoc bool) {
		t.Helper()
		_, err := app.saveStudentGroupSessionOccurrence(StudentGroupSessionOccurrenceInput{
			GroupID:            groupID,
			TimetableSessionID: timetableSessionID,
			OccurrenceDate:     date,
			Status:             status,
			IsAdHoc:            adHoc,
			StaffAssignments: []StudentGroupSessionStaffAssignmentInput{
				{
					UserID:         subCoach.ID,
					AssignmentRole: groupStaffRoleAssistantCoach,
					WorkStatus:     workStatus,
				},
			},
		}, actor.ID)
		if err != nil {
			t.Fatalf("save occurrence %s %s: %v", date, status, err)
		}
	}

	save(group.ID, group.Sessions[0].ID, "2026-08-02", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusWorked, false)
	save(group.ID, group.Sessions[0].ID, "2026-08-09", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusAbsent, false)
	save(group.ID, group.Sessions[0].ID, "2026-08-16", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusExcused, false)
	save(group.ID, group.Sessions[0].ID, "2026-08-23", GroupSessionOccurrenceStatusCancelled, GroupSessionWorkStatusWorked, false)
	save(group.ID, group.Sessions[0].ID, "2026-08-30", GroupSessionOccurrenceStatusScheduled, GroupSessionWorkStatusWorked, false)
	save(group.ID, group.Sessions[0].ID, "2026-07-26", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusWorked, false)
	save(group.ID, group.Sessions[0].ID, "2026-08-05", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusWorked, true)
	save(otherGroup.ID, otherGroup.Sessions[0].ID, "2026-08-12", GroupSessionOccurrenceStatusCompleted, GroupSessionWorkStatusWorked, false)

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            subCoach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypePerSession,
		Rate:              300,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, actor.ID)
	hourlyProfileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:            subCoach.ID,
		TrainingProgramID: programID,
		CompensationType:  SalaryTypeHourly,
		Rate:              250,
		EffectiveFrom:     "2026-01-01",
		Active:            true,
	}, actor.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", actor.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, actor.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payment.Quantity != 2 {
		t.Fatalf("per-session quantity = %.2f, want 2", payment.Quantity)
	}
	if payment.BaseAmount != 600 {
		t.Fatalf("per-session base = %.2f, want 600", payment.BaseAmount)
	}
	if payment.QuantityLabel != "2 sessions worked" {
		t.Fatalf("per-session quantity label = %q", payment.QuantityLabel)
	}

	hourlyPayment := payrollPaymentForProfile(t, app, runID, hourlyProfileID)
	if hourlyPayment.Quantity != 4 {
		t.Fatalf("assistant coach hourly quantity = %.2f, want 4", hourlyPayment.Quantity)
	}
	if hourlyPayment.BaseAmount != 1000 {
		t.Fatalf("assistant coach hourly base = %.2f, want 1000", hourlyPayment.BaseAmount)
	}
	hourlySessionDetails := 0
	for _, detail := range hourlyPayment.CalculationDetails {
		if detail.DetailType == payrollDetailTypeHourlySessionOccurrence {
			hourlySessionDetails++
		}
	}
	if hourlySessionDetails != 2 {
		t.Fatalf("assistant coach hourly session details = %d, want 2", hourlySessionDetails)
	}

	occurrenceDetails := 0
	for _, detail := range payment.CalculationDetails {
		if detail.DetailType == payrollDetailTypePerSessionOccurrence {
			occurrenceDetails++
		}
	}
	if occurrenceDetails != 2 {
		t.Fatalf("per-session detail count = %d, want 2", occurrenceDetails)
	}
}

func TestPayrollPhase2MonthlyCalculationAndRecalculationSnapshot(t *testing.T) {
	app := newAuthorizationTestApp(t)

	manager, err := app.createManagedUser("Manager", "manager@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:           manager.ID,
		CompensationType: SalaryTypeMonthly,
		Rate:             50000,
		EffectiveFrom:    "2026-01-01",
		Active:           true,
	}, manager.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", manager.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, manager.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payment.Quantity != 1 || payment.BaseAmount != 50000 {
		t.Fatalf("monthly payment = %#v", payment)
	}

	if _, err := app.db.Exec(`UPDATE staff_salary_profiles SET rate = 55000 WHERE id = ?`, profileID); err != nil {
		t.Fatalf("update salary rate: %v", err)
	}

	stillSnapshotted := payrollPaymentForProfile(t, app, runID, profileID)
	if stillSnapshotted.RateSnapshot != 50000 || stillSnapshotted.BaseAmount != 50000 {
		t.Fatalf("payment changed without recalculation: %#v", stillSnapshotted)
	}

	if err := app.addPayrollAdjustment(payment.ID, PayrollAdjustmentBonus, PayrollDirectionAddition, "Performance bonus", 1000, manager.ID); err != nil {
		t.Fatalf("add payroll adjustment: %v", err)
	}

	if err := app.recalculatePayrollRun(runID, manager.ID); err != nil {
		t.Fatalf("recalculate payroll run: %v", err)
	}

	recalculated := payrollPaymentForProfile(t, app, runID, profileID)
	if recalculated.RateSnapshot != 55000 || recalculated.BaseAmount != 55000 || recalculated.Quantity != 1 {
		t.Fatalf("recalculated payment = %#v", recalculated)
	}
	if recalculated.AdditionsTotal != 1000 || recalculated.NetAmount != 56000 {
		t.Fatalf("recalculated totals = additions %.2f net %.2f", recalculated.AdditionsTotal, recalculated.NetAmount)
	}

	if err := app.approvePayrollRun(runID, manager.ID); err != nil {
		t.Fatalf("approve payroll run: %v", err)
	}
	if err := app.recalculatePayrollRun(runID, manager.ID); err == nil {
		t.Fatal("expected approved payroll recalculation to fail")
	}
}

func TestPayrollPhase2PaymentRequiresApprovedSalary(t *testing.T) {
	app := newAuthorizationTestApp(t)

	manager, err := app.createManagedUser("Paid Manager", "paid-manager@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	var divisionID int64
	if err := app.db.QueryRow(`SELECT id FROM divisions ORDER BY id ASC LIMIT 1`).Scan(&divisionID); err != nil {
		t.Fatalf("find division: %v", err)
	}

	accountID, err := app.createFinanceAccount(divisionID, "", "Payroll Bank", financeAccountTypeBank, "payroll bank", manager.ID)
	if err != nil {
		t.Fatalf("create finance account: %v", err)
	}

	profileID := createPayrollTestSalaryProfile(t, app, StaffSalaryProfile{
		UserID:           manager.ID,
		DivisionID:       divisionID,
		CompensationType: SalaryTypeMonthly,
		Rate:             40000,
		EffectiveFrom:    "2026-01-01",
		Active:           true,
	}, manager.ID)

	runID, err := app.createPayrollRun("2026-08-01", "2026-08-31", "August 2026", manager.ID)
	if err != nil {
		t.Fatalf("create payroll run: %v", err)
	}
	if err := app.generatePayrollRunPayments(runID, manager.ID); err != nil {
		t.Fatalf("generate payroll: %v", err)
	}

	payment := payrollPaymentForProfile(t, app, runID, profileID)
	if payrollPaymentAllowsPayment(payment) {
		t.Fatal("calculated salary must not be eligible for payment")
	}
	if err := app.payPayrollPayment(payment.ID, accountID, "BANK-REF-2026-08-29", manager.ID); err == nil {
		t.Fatal("expected calculated salary payment to be rejected")
	}

	if err := app.approvePayrollPayment(payment.ID, manager.ID); err != nil {
		t.Fatalf("approve salary individually: %v", err)
	}
	individuallyApproved := payrollPaymentForProfile(t, app, runID, profileID)
	if individuallyApproved.Status != PayrollPaymentStatusApproved || !payrollPaymentAllowsPayment(individuallyApproved) {
		t.Fatalf("individually approved payment eligibility = %#v", individuallyApproved)
	}
	if err := app.rollbackPayrollPaymentApproval(payment.ID, manager.ID); err != nil {
		t.Fatalf("rollback individual salary approval: %v", err)
	}
	rolledBack := payrollPaymentForProfile(t, app, runID, profileID)
	if rolledBack.Status != PayrollPaymentStatusCalculated || !payrollPaymentAllowsAdjustments(rolledBack) {
		t.Fatalf("rolled back payment eligibility = %#v", rolledBack)
	}

	if err := app.approvePayrollPayment(payment.ID, manager.ID); err != nil {
		t.Fatalf("approve salary individually again: %v", err)
	}
	if err := app.approvePayrollRun(runID, manager.ID); err != nil {
		t.Fatalf("approve payroll run with individually approved salary: %v", err)
	}
	if err := app.rollbackPayrollPaymentApproval(payment.ID, manager.ID); err == nil {
		t.Fatal("expected individual approval rollback after payroll approval to fail")
	}

	approvedPayment := payrollPaymentForProfile(t, app, runID, profileID)
	if approvedPayment.Status != PayrollPaymentStatusApproved || !payrollPaymentAllowsPayment(approvedPayment) {
		t.Fatalf("approved payment eligibility = %#v", approvedPayment)
	}

	if err := app.payPayrollPayment(payment.ID, accountID, "BANK-REF-2026-08-29", manager.ID); err != nil {
		t.Fatalf("pay approved payroll payment: %v", err)
	}

	paidPayment, _, err := app.findPayrollPaymentByID(payment.ID)
	if err != nil {
		t.Fatalf("find paid payroll payment: %v", err)
	}
	if paidPayment.Status != PayrollPaymentStatusPaid {
		t.Fatalf("payment status = %q, want paid", paidPayment.Status)
	}
	if payrollPaymentAllowsPayment(*paidPayment) {
		t.Fatal("paid salary must not be eligible for payment")
	}
	if err := app.recalculatePayrollRun(runID, manager.ID); err == nil {
		t.Fatal("expected paid payroll recalculation to fail")
	}
}

func TestPayrollPaymentEligibilityByStatus(t *testing.T) {
	base := PayrollPayment{NetAmount: 1000}
	cases := []struct {
		name              string
		payment           PayrollPayment
		allowsAdjustments bool
		allowsPayment     bool
		allowsVoid        bool
	}{
		{name: "draft", payment: PayrollPayment{Status: PayrollPaymentStatusDraft, NetAmount: base.NetAmount}, allowsAdjustments: true},
		{name: "calculated", payment: PayrollPayment{Status: PayrollPaymentStatusCalculated, NetAmount: base.NetAmount}, allowsAdjustments: true},
		{name: "approved", payment: PayrollPayment{Status: PayrollPaymentStatusApproved, NetAmount: base.NetAmount}, allowsPayment: true},
		{name: "paid", payment: PayrollPayment{Status: PayrollPaymentStatusPaid, NetAmount: base.NetAmount, FinanceTransactionID: 1}, allowsVoid: true},
		{name: "void", payment: PayrollPayment{Status: PayrollPaymentStatusVoid, NetAmount: base.NetAmount, FinanceTransactionID: 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payrollPaymentAllowsAdjustments(tc.payment); got != tc.allowsAdjustments {
				t.Fatalf("adjustments allowed = %t, want %t", got, tc.allowsAdjustments)
			}
			if got := payrollPaymentAllowsPayment(tc.payment); got != tc.allowsPayment {
				t.Fatalf("payment allowed = %t, want %t", got, tc.allowsPayment)
			}
			if got := payrollPaymentAllowsVoid(tc.payment); got != tc.allowsVoid {
				t.Fatalf("void allowed = %t, want %t", got, tc.allowsVoid)
			}
		})
	}

	calculatedRun := &PayrollRun{Status: PayrollRunStatusCalculated}
	calculatedPayment := PayrollPayment{Status: PayrollPaymentStatusCalculated, NetAmount: base.NetAmount}
	if !payrollPaymentAllowsIndividualApproval(calculatedRun, calculatedPayment) {
		t.Fatal("calculated salary in a calculated run must allow individual approval")
	}
	approvedPayment := PayrollPayment{Status: PayrollPaymentStatusApproved, NetAmount: base.NetAmount}
	if !payrollPaymentAllowsApprovalRollback(calculatedRun, approvedPayment) {
		t.Fatal("individually approved salary in a calculated run must allow rollback")
	}
	approvedRun := &PayrollRun{Status: PayrollRunStatusApproved}
	if payrollPaymentAllowsIndividualApproval(approvedRun, calculatedPayment) || payrollPaymentAllowsApprovalRollback(approvedRun, approvedPayment) {
		t.Fatal("payroll-level approval must lock individual approval actions")
	}
}

func TestPayrollPaymentEligibleFinanceAccounts(t *testing.T) {
	payment := PayrollPayment{DivisionID: 10}
	accounts := []FinanceAccount{
		{ID: 1, DivisionID: 10, AccountType: financeAccountTypeCash, IsActive: true},
		{ID: 2, DivisionID: 10, AccountType: financeAccountTypeBank, IsActive: true},
		{ID: 3, DivisionID: 11, AccountType: financeAccountTypeBank, IsActive: true},
		{ID: 4, DivisionID: 10, AccountType: financeAccountTypeCash, IsActive: false},
		{ID: 5, DivisionID: 10, AccountType: "other", IsActive: true},
	}

	eligible := payrollPaymentEligibleFinanceAccounts(accounts, payment)
	if len(eligible) != 2 || eligible[0].ID != 1 || eligible[1].ID != 2 {
		t.Fatalf("eligible finance accounts = %#v", eligible)
	}
}

func TestPayrollPhase2RecalculateRouteRequiresPermission(t *testing.T) {
	app := newAuthorizationTestApp(t)

	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	user, err := app.createManagedUser("Coach User", "coach-recalc@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	protected := app.requirePermission(
		http.HandlerFunc(app.recalculatePayrollRunHandler),
		"payroll.update",
	)

	req := httptest.NewRequest(http.MethodPost, "/admin/payroll/recalculate", strings.NewReader("id=1&csrf_token=test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()

	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPayrollPhase2SQLiteBootstrapIncludesPayrollTables(t *testing.T) {
	app := newAuthorizationTestApp(t)

	for _, table := range []string{
		"staff_salary_profiles",
		"payroll_runs",
		"payroll_payments",
		"payroll_adjustments",
		"payroll_payment_calculation_details",
		"staff_work_time_records",
		"student_enrollment_status_history",
		"student_group_membership_history",
		"student_group_staff_assignment_history",
	} {
		var count int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist once, got %d", table, count)
		}
	}
}
