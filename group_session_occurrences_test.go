package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidSalaryTypeIncludesPhaseOneTypes(t *testing.T) {
	for _, salaryType := range []string{
		SalaryTypeHourly,
		SalaryTypeDaily,
		SalaryTypeWeekly,
		SalaryTypeMonthly,
		SalaryTypePerStudent,
		SalaryTypePerSession,
	} {
		if !validSalaryType(salaryType) {
			t.Fatalf("expected salary type %q to be valid", salaryType)
		}
	}
}

func TestRunMigrationsCreatesStudentGroupSessionOccurrenceTables(t *testing.T) {
	app := newAuthorizationTestApp(t)

	for _, table := range []string{
		"student_group_session_occurrences",
		"student_group_session_staff",
	} {
		var count int
		if err := app.db.QueryRow(`
			SELECT COUNT(*)
			FROM sqlite_master
			WHERE type = 'table'
			  AND name = ?
		`, table).Scan(&count); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist once, got %d", table, count)
		}
	}
}

func TestStudentGroupSessionOccurrenceLifecycle(t *testing.T) {
	app := newAuthorizationTestApp(t)

	actor, err := app.createManagedUser("Payroll Admin", "occ-admin@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	staffA, err := app.createManagedUser("Sub Coach A", "sub-a@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create staff A: %v", err)
	}
	staffB, err := app.createManagedUser("Sub Coach B", "sub-b@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create staff B: %v", err)
	}

	if err := app.createStudentGroup(
		StudentGroup{
			Name:        "Cricket Group A",
			Code:        "CR-A",
			Description: "Session occurrence tests",
		},
		nil,
		nil,
		[]StudentGroupSession{
			{
				Title:     "Saturday Session",
				DayOfWeek: "saturday",
				StartTime: "16:00",
				EndTime:   "18:00",
				Active:    true,
			},
		},
	); err != nil {
		t.Fatalf("create student group: %v", err)
	}

	var groupID int64
	if err := app.db.QueryRow(`SELECT id FROM student_groups WHERE code = 'CR-A'`).Scan(&groupID); err != nil {
		t.Fatalf("lookup group id: %v", err)
	}

	group, err := app.findStudentGroupByID(groupID)
	if err != nil {
		t.Fatalf("find group: %v", err)
	}
	if len(group.Sessions) != 1 {
		t.Fatalf("group session count = %d, want 1", len(group.Sessions))
	}

	occurrenceID, err := app.saveStudentGroupSessionOccurrence(
		StudentGroupSessionOccurrenceInput{
			GroupID:            group.ID,
			TimetableSessionID: group.Sessions[0].ID,
			OccurrenceDate:     "2026-08-29",
			ActualStartTime:    "16:00",
			ActualEndTime:      "18:00",
			Status:             GroupSessionOccurrenceStatusCompleted,
			StaffAssignments: []StudentGroupSessionStaffAssignmentInput{
				{
					UserID:         staffA.ID,
					AssignmentRole: groupStaffRoleAssistantCoach,
					WorkStatus:     GroupSessionWorkStatusWorked,
					Notes:          "Worked full session",
				},
				{
					UserID:         staffB.ID,
					AssignmentRole: groupStaffRoleAssistantCoach,
					WorkStatus:     GroupSessionWorkStatusAbsent,
					Notes:          "Was absent",
				},
			},
		},
		actor.ID,
	)
	if err != nil {
		t.Fatalf("save occurrence: %v", err)
	}
	if occurrenceID <= 0 {
		t.Fatalf("occurrence id = %d", occurrenceID)
	}

	occurrences, err := app.listStudentGroupSessionOccurrencesByGroupAndDate(group.ID, "2026-08-29")
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	if len(occurrences) != 1 {
		t.Fatalf("occurrence count = %d, want 1", len(occurrences))
	}
	if occurrences[0].Status != GroupSessionOccurrenceStatusCompleted {
		t.Fatalf("occurrence status = %q, want completed", occurrences[0].Status)
	}
	if len(occurrences[0].StaffAssignments) != 2 {
		t.Fatalf("staff assignment count = %d, want 2", len(occurrences[0].StaffAssignments))
	}

	if _, err := app.saveStudentGroupSessionOccurrence(
		StudentGroupSessionOccurrenceInput{
			GroupID:            group.ID,
			TimetableSessionID: group.Sessions[0].ID,
			OccurrenceDate:     "2026-08-29",
			Status:             GroupSessionOccurrenceStatusScheduled,
		},
		actor.ID,
	); err == nil {
		t.Fatal("expected duplicate normal occurrence to fail")
	} else if !isUniqueConstraintError(err) {
		t.Fatalf("expected unique constraint error, got %v", err)
	}

	adHocID, err := app.saveStudentGroupSessionOccurrence(
		StudentGroupSessionOccurrenceInput{
			GroupID:            group.ID,
			TimetableSessionID: group.Sessions[0].ID,
			OccurrenceDate:     "2026-08-29",
			Status:             GroupSessionOccurrenceStatusScheduled,
			IsAdHoc:            true,
		},
		actor.ID,
	)
	if err != nil {
		t.Fatalf("save ad hoc occurrence: %v", err)
	}
	if adHocID <= 0 || adHocID == occurrenceID {
		t.Fatalf("unexpected ad hoc occurrence id %d", adHocID)
	}

	if _, err := app.saveStudentGroupSessionOccurrence(
		StudentGroupSessionOccurrenceInput{
			ID:                 occurrenceID,
			GroupID:            group.ID,
			TimetableSessionID: group.Sessions[0].ID,
			OccurrenceDate:     "2026-08-29",
			Status:             GroupSessionOccurrenceStatusCancelled,
		},
		actor.ID,
	); err != nil {
		t.Fatalf("cancel occurrence: %v", err)
	}

	occurrences, err = app.listStudentGroupSessionOccurrencesByGroupAndDate(group.ID, "2026-08-29")
	if err != nil {
		t.Fatalf("reload occurrences: %v", err)
	}
	if len(occurrences) != 2 {
		t.Fatalf("occurrence count after ad hoc create = %d, want 2", len(occurrences))
	}
	if occurrences[0].ID == occurrenceID {
		if occurrences[0].Status != GroupSessionOccurrenceStatusCancelled {
			t.Fatalf("updated occurrence status = %q, want cancelled", occurrences[0].Status)
		}
		if len(occurrences[0].StaffAssignments) != 0 {
			t.Fatalf("cancelled occurrence assignments = %d, want 0 after replacement update", len(occurrences[0].StaffAssignments))
		}
	}
}

func TestSaveStudentGroupSessionOccurrenceHandlerRequiresPermission(t *testing.T) {
	app := newAuthorizationTestApp(t)

	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	user, err := app.createManagedUser("Coach User", "coach-occ@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	protected := app.requirePermission(
		http.HandlerFunc(app.saveStudentGroupSessionOccurrenceHandler),
		"student_groups.update",
	)

	form := url.Values{
		"group_id":         {"1"},
		"occurrence_date":  {"2026-08-29"},
		"status":           {"scheduled"},
		"csrf_token":       {"test"},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/student-groups/occurrences/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()

	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
