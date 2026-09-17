package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttendanceSearchFilters(t *testing.T) {
	groups := []StudentGroup{{ID: 1, TrainingProgramID: 10}, {ID: 2, TrainingProgramID: 20}}
	history := []StudentAttendanceHistoryRow{
		{ID: 1, GroupID: 1, AttendanceDate: "2026-08-01", Status: "present"},
		{ID: 2, GroupID: 1, AttendanceDate: "2026-08-31", Status: "absent"},
		{ID: 3, GroupID: 2, AttendanceDate: "2026-08-15", Status: "late"},
		{ID: 4, GroupID: 1, AttendanceDate: "2026-09-01", Status: "present"},
	}
	for _, tc := range []struct {
		name, query string
		want        []int64
	}{
		{"all", "", []int64{1, 2, 3, 4}},
		{"class", "programme_id=20", []int64{3}},
		{"inclusive dates and group", "from=2026-08-01&to=2026-08-31&group_id=1", []int64{1, 2}},
		{"combined", "programme_id=10&status=present&to=2026-08-31", []int64{1}},
		{"no matches", "programme_id=20&group_id=1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseAttendanceSearchFilter(httptest.NewRequest("GET", "/?"+tc.query, nil))
			if err != nil {
				t.Fatal(err)
			}
			got := filterAttendanceSearchHistory(history, f, groups)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want IDs %v", got, tc.want)
			}
			for i, row := range got {
				if row.ID != tc.want[i] {
					t.Fatalf("got ID %d, want %d", row.ID, tc.want[i])
				}
			}
			if summarizeStudentAttendanceHistory(got).TotalEntries != len(tc.want) {
				t.Fatal("incorrect filtered summary")
			}
		})
	}
	for _, query := range []string{"from=invalid", "to=2026-02-30", "from=2026-09-01&to=2026-08-01", "status=unknown", "group_id=-1", "programme_id=invalid"} {
		if _, err := parseAttendanceSearchFilter(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Errorf("accepted invalid filter %s", query)
		}
	}
}

func TestAttendanceSearchTemplatePreservesFilters(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	body := renderTemplateToString(t, templates, "attendance-search", TemplateData{
		AttendanceSearchStudentID: "Alex",
		AttendanceSearchFilter:    AttendanceSearchFilter{ProgrammeID: 10, GroupID: 1, From: "2026-08-01", To: "2026-08-31", Status: "absent"},
		AttendanceSearchMatches:   []Admission{{StudentID: "MKM-001", FullName: "Alex"}},
		TrainingPrograms:          []TrainingProgram{{ID: 10, Name: "Maths"}},
		StudentGroups:             []StudentGroup{{ID: 1, Name: "Monday", TrainingProgramID: 10}},
		SelectedDivisionScope:     "all",
	})
	for _, want := range []string{`name="programme_id"`, `value="10" selected`, `value="1" selected`, `value="absent" selected`, `programme_id=10`, `group_id=1`, `from=2026-08-01`, `to=2026-08-31`, `status=absent`, `division=all`, `student_id=MKM-001`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestAttendanceStudentSuggestions(t *testing.T) {
	groups := []StudentGroup{
		{Students: []Admission{{ID: 1, StudentID: "MKM-001", FullName: "Zara"}, {ID: 2, StudentID: "MKM-002", FullName: "Alex"}}},
		{Students: []Admission{{ID: 1, StudentID: "MKM-001", FullName: "Zara"}, {ID: 3, StudentID: "MKM-003", FullName: "Alex"}, {ID: 0, StudentID: "invalid"}}},
	}
	got := attendanceStudentSuggestions(groups)
	if len(got) != 3 || got[0].StudentID != "MKM-002" || got[1].StudentID != "MKM-003" || got[2].StudentID != "MKM-001" {
		t.Fatalf("unexpected suggestions: %v", got)
	}
}

func TestAttendanceSearchPrintAndSuggestions(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		SelectedAttendanceStudent:   &Admission{ID: 1, StudentID: "MKM-001", FullName: "Alex"},
		AttendanceSearchSuggestions: []AttendanceStudentSuggestion{{StudentID: "MKM-001", FullName: "Alex"}},
		AttendanceSearchFilter:      AttendanceSearchFilter{From: "2026-08-01", To: "2026-08-31", Status: "present", ProgrammeID: 10, GroupID: 20},
		TrainingPrograms:            []TrainingProgram{{ID: 10, Name: "Maths"}},
		StudentGroups:               []StudentGroup{{ID: 20, Name: "Monday"}},
		SelectedDivisionScope:       "all",
		StudentAttendanceHistory:    []StudentAttendanceHistoryRow{{AttendanceDate: "2026-08-03", Status: "present", GroupName: "Monday", TrainingProgramName: "Maths"}},
		StudentAttendanceSummary:    StudentAttendanceSummary{TotalEntries: 1, PresentCount: 1, AttendanceRate: 100},
	}
	body := renderTemplateToString(t, templates, "attendance-search", data)
	for _, want := range []string{`list="attendance-student-suggestions"`, `label="Alex · MKM-001"`, `format=pdf`, `student_id=MKM-001`, `from=2026-08-01`, `to=2026-08-31`, `status=present`, `programme_id=10`, `group_id=20`, `division=all`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	data.HideChrome = true
	body = renderTemplateToString(t, templates, "attendance-search", data)
	for _, want := range []string{"Student attendance report", "Alex", "MKM-001", "Maths", "Monday", "100.0", `onclick="window.print()"`} {
		if !strings.Contains(body, want) {
			t.Errorf("print view missing %s", want)
		}
	}
	if strings.Contains(body, `id="attendance-student-query"`) || strings.Contains(body, `data-sidebar`) && strings.Contains(body, `<aside`) {
		t.Fatal("print view contains search controls or sidebar")
	}
}
