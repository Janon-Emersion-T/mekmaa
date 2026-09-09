package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestStudentStatusLifecycleAndFilters(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	id, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID: "STATUS-001", FullName: "Status Student", AdmissionDate: "2026-09-01",
		DateOfBirth: "2012-01-01", Gender: "male", PracticeType: "group_practice",
	}, false, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	student, err := app.findAdmissionIdentityByID(id)
	if err != nil || student.Status != "active" {
		t.Fatalf("default student: %+v, %v", student, err)
	}
	for _, status := range []string{"inactive", "active"} {
		if err := app.setStudentStatus(id, status); err != nil {
			t.Fatal(err)
		}
		student, err = app.findAdmissionByID(id)
		if err != nil || student.Status != status {
			t.Fatalf("persisted student: %+v, %v", student, err)
		}
		// Editing other profile fields must preserve the current status.
		student.FullName = "Updated Student"
		if err := app.updateAdmission(*student); err != nil {
			t.Fatal(err)
		}
		for _, filterStatus := range []string{"", "active", "inactive"} {
			rows, total, err := app.listAdmissionsFiltered(AdmissionsFilter{
				Status: filterStatus, Division: "all", Search: "STATUS-001", Page: 1, Limit: 25,
			})
			want := 0
			if filterStatus == "" || filterStatus == status {
				want = 1
			}
			if err != nil || total != want || len(rows) != want {
				t.Fatalf("status %s filter %s: rows=%d total=%d err=%v", status, filterStatus, len(rows), total, err)
			}
			if want == 1 && rows[0].Status != status {
				t.Fatalf("wrong row status: %q", rows[0].Status)
			}
		}
	}
	if err := app.setStudentStatus(id, "deleted"); err == nil {
		t.Fatal("accepted invalid status")
	}
	if err := app.setStudentStatus(id+999, "inactive"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing student: %v", err)
	}
}

func TestStudentStatusHandler(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	id, _, err := app.createAdmissionWithOptionalPayment(Admission{StudentID: "STATUS-HTTP", FullName: "Student", PracticeType: "group_practice"}, false, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, status, csrf string
		user                       *User
		want                       int
	}{
		{"method", http.MethodGet, "inactive", "token", &User{Roles: []string{"superadmin"}}, http.StatusMethodNotAllowed},
		{"csrf", http.MethodPost, "inactive", "", &User{Roles: []string{"superadmin"}}, http.StatusForbidden},
		{"invalid", http.MethodPost, "invalid", "token", &User{Roles: []string{"superadmin"}}, http.StatusBadRequest},
		{"scope", http.MethodPost, "inactive", "token", &User{ID: 999, Roles: []string{"admin"}, DivisionIDs: []int64{1}}, http.StatusForbidden},
		{"deactivate", http.MethodPost, "inactive", "token", &User{Roles: []string{"superadmin"}}, http.StatusSeeOther},
		{"reactivate", http.MethodPost, "active", "token", &User{Roles: []string{"superadmin"}}, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"admission_id": {strconv.FormatInt(id, 10)}, "status": {tc.status}, "csrf_token": {tc.csrf}}
			req := httptest.NewRequest(tc.method, "/admin/students/status?division=all&status=active&search=STATUS&page=2&limit=10", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
			req = req.WithContext(context.WithValue(req.Context(), userContextKey, tc.user))
			rec := httptest.NewRecorder()
			app.updateStudentStatusHandler(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
			}
			student, err := app.findAdmissionIdentityByID(id)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "active"
			if tc.name == "deactivate" {
				wantStatus = "inactive"
			}
			if student.Status != wantStatus {
				t.Fatalf("status = %s, want %s", student.Status, wantStatus)
			}
			if tc.want == http.StatusSeeOther {
				location, err := url.Parse(rec.Header().Get("Location"))
				if err != nil || location.Path != "/admin/students" || location.Query().Get("division") != "all" || location.Query().Get("status") != "active" || location.Query().Get("page") != "2" {
					t.Fatalf("redirect lost filters: %v %v", location, err)
				}
			}
		})
	}
}

func TestStudentStatusFilterLinksAndTemplate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/students?division=all&status=INACTIVE&search=Ann", nil)
	filter := admissionsFilterFromRequest(req)
	if filter.Status != "inactive" {
		t.Fatalf("status: %q", filter.Status)
	}
	for _, link := range []string{admissionsFilterPageURL(req, filter, 2), admissionsFilterBaseURL(req, filter)} {
		parsed, err := url.Parse(link)
		if err != nil || parsed.Query().Get("status") != "inactive" {
			t.Fatalf("status lost in %q", link)
		}
	}
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, editable := range []bool{false, true} {
		user := &User{Name: "Reviewer"}
		if editable {
			user.Permissions = []string{"admissions.update"}
		}
		html := renderTemplateToString(t, templates, "admission-management", TemplateData{
			User: user, CSRFToken: "token", AdmissionsFilter: filter,
			Admissions: []Admission{{ID: 1, FullName: "Ann", Status: "inactive"}},
		})
		if !strings.Contains(html, "Inactive") || !strings.Contains(html, "All statuses") {
			t.Fatal("missing status UI")
		}
		if strings.Contains(html, "/admin/students/status?") != editable {
			t.Fatal("status control permission mismatch")
		}
	}
	rec := httptest.NewRecorder()
	if err := writeAdmissionsCSV(rec, []Admission{{StudentID: "S1", Status: "inactive"}}, filter); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "Student ID,Name,Status,") || !strings.Contains(rec.Body.String(), "S1,,Inactive,") {
		t.Fatal("missing status in CSV")
	}
}
