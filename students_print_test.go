package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStudentsPrintIncludesAllPagesAndStatuses(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	var err error
	app.templates, err = buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, student := range []struct{ id, name, status string }{
		{"PRINT-1", "First Print Student", "active"},
		{"PRINT-2", "Second Print Student", "inactive"},
	} {
		id, _, err := app.createAdmissionWithOptionalPayment(Admission{StudentID: student.id, FullName: student.name, PracticeType: "group_practice"}, false, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.setStudentStatus(id, student.status); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query         string
		first, second bool
	}{
		{"", true, true},
		{"&status=inactive", false, true},
		{"&status=active", true, false},
		{"&search=Second", false, true},
		{"&search=missing", false, false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/students?division=all&format=pdf&page=2&limit=1"+tc.query, nil)
			req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{Roles: []string{"superadmin"}}))
			rec := httptest.NewRecorder()
			app.admissionManagementHandler(rec, req)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, body)
			}
			if strings.Contains(body, "First Print Student") != tc.first || strings.Contains(body, "Second Print Student") != tc.second {
				t.Fatal("print rows do not match filters or were paginated")
			}
			if tc.first && !strings.Contains(body, "<strong>Active</strong>") {
				t.Fatal("missing active status")
			}
			if tc.second && !strings.Contains(body, "<strong>Inactive</strong>") {
				t.Fatal("missing inactive status")
			}
			if !tc.first && !tc.second && !strings.Contains(body, "No students match") {
				t.Fatal("missing empty state")
			}
			if !strings.Contains(body, "window.print()") || strings.Contains(body, "data-sidebar-open") && strings.Contains(body, "<aside") {
				t.Fatal("incorrect print view")
			}
		})
	}
}
