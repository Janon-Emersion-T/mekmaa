package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func sharedAttendanceFixture(t *testing.T) (*App, *StudentEnrollment, StudentGroup) {
	t.Helper()
	a, e, _ := programmeTestFixture(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a.templates = templates
	err = a.createStudentGroup(StudentGroup{Name: "Attendance class", Code: "SHARED", TrainingProgramID: e.TrainingProgramID}, []int64{e.AdmissionID}, nil, []StudentGroupSession{{Title: "Monday class", DayOfWeek: "monday", StartTime: "10:00", EndTime: "11:00", Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := a.listStudentGroups()
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups: %v %v", groups, err)
	}
	return a, e, groups[0]
}

func sharedAttendanceRequest(a *App, method, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.SetBasicAuth("attendance", "asdf@1234")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-token"})
	}
	rec := httptest.NewRecorder()
	a.sharedAttendanceHandler(rec, req)
	return rec
}

func TestSharedAttendanceAuthentication(t *testing.T) {
	t.Setenv("ATTENDANCE_USERNAME", "attendance")
	t.Setenv("ATTENDANCE_PASSWORD", "")
	a := &App{}
	for _, path := range []string{"/attendance", "/attendance/save"} {
		for _, password := range []string{"", "wrong"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if password != "" {
				req.SetBasicAuth("attendance", password)
			}
			rec := httptest.NewRecorder()
			a.sharedAttendanceHandler(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("unprotected endpoint: %d", rec.Code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/attendance", nil)
	req.SetBasicAuth("attendance", "asdf@1234")
	if !sharedAttendanceAuthenticated(req) {
		t.Fatal("default credential rejected")
	}
	t.Setenv("ATTENDANCE_PASSWORD", "replacement-password")
	if sharedAttendanceAuthenticated(req) {
		t.Fatal("default still works after override")
	}
	req.SetBasicAuth("attendance", "replacement-password")
	if !sharedAttendanceAuthenticated(req) {
		t.Fatal("override rejected")
	}
}

func TestSharedAttendanceWorkflow(t *testing.T) {
	t.Setenv("ATTENDANCE_USERNAME", "attendance")
	t.Setenv("ATTENDANCE_PASSWORD", "asdf@1234")
	a, e, group := sharedAttendanceFixture(t)
	session := group.Sessions[0]
	query := url.Values{"course_id": {fmt.Sprint(e.TrainingProgramID)}, "group_id": {fmt.Sprint(group.ID)}, "session_id": {fmt.Sprint(session.ID)}, "date": {"2026-08-03"}}
	page := sharedAttendanceRequest(a, http.MethodGet, "/attendance?"+query.Encode(), nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `action="/attendance/save"`) {
		t.Fatalf("sheet: %d %s", page.Code, page.Body.String())
	}
	form := url.Values{"csrf_token": {"test-token"}, "course_id": {fmt.Sprint(e.TrainingProgramID)}, "group_id": {fmt.Sprint(group.ID)}, "session_id": {fmt.Sprint(session.ID)}, "attendance_date": {"2026-08-03"}, fmt.Sprintf("status_%d", e.AdmissionID): {"present"}}
	for _, tc := range []struct {
		name, key, value string
		code             int
	}{
		{"csrf", "csrf_token", "wrong", http.StatusForbidden},
		{"course mismatch", "course_id", "99999", http.StatusBadRequest},
		{"session mismatch", "session_id", "99999", http.StatusBadRequest},
		{"wrong day", "attendance_date", "2026-08-04", http.StatusBadRequest},
		{"future date", "attendance_date", "2999-08-03", http.StatusBadRequest},
		{"missing status", fmt.Sprintf("status_%d", e.AdmissionID), "", http.StatusBadRequest},
		{"invalid status", fmt.Sprintf("status_%d", e.AdmissionID), "unknown", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := url.Values{}
			for k, v := range form {
				bad[k] = append([]string(nil), v...)
			}
			bad.Set(tc.key, tc.value)
			rec := sharedAttendanceRequest(a, http.MethodPost, "/attendance/save", bad)
			if rec.Code != tc.code {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	rec := sharedAttendanceRequest(a, http.MethodPost, "/attendance/save", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	page = sharedAttendanceRequest(a, http.MethodGet, rec.Header().Get("Location"), nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Attendance already saved") || strings.Contains(page.Body.String(), `action="/attendance/save"`) {
		t.Fatalf("saved sheet not locked: %s", page.Body.String())
	}
	form.Set(fmt.Sprintf("status_%d", e.AdmissionID), "absent")
	rec = sharedAttendanceRequest(a, http.MethodPost, "/attendance/save", form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("overwrite accepted: %d %s", rec.Code, rec.Body.String())
	}
	records, err := a.listAttendanceRecords(group.ID, session.ID, "2026-08-03")
	if err != nil || len(records) != 1 || records[0].Status != "present" {
		t.Fatalf("saved data changed: %v %v", records, err)
	}
	// Existing admin sheets must also be immutable through the shared link.
	records[0].AttendanceDate = "2026-08-10"
	if err := a.replaceAttendanceRecords(group.ID, session.ID, "2026-08-10", records); err != nil {
		t.Fatal(err)
	}
	form.Set("attendance_date", "2026-08-10")
	rec = sharedAttendanceRequest(a, http.MethodPost, "/attendance/save", form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("admin sheet overwrite accepted: %d", rec.Code)
	}
}

func TestSharedAttendanceConcurrentSaves(t *testing.T) {
	a, e, group := sharedAttendanceFixture(t)
	records := []AttendanceRecord{{GroupID: group.ID, SessionID: group.Sessions[0].ID, AdmissionID: e.AdmissionID, AttendanceDate: "2026-08-03", Status: "present"}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.writeAttendanceRecords(group.ID, group.Sessions[0].ID, "2026-08-03", records, true)
		}()
	}
	wg.Wait()
	close(results)
	saved, blocked := 0, 0
	for err := range results {
		if err == nil {
			saved++
		} else if err == errAttendanceAlreadySaved {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || blocked != 1 {
		t.Fatalf("saved %d blocked %d", saved, blocked)
	}
}

func TestSharedAttendancePostgresConcurrentSaves(t *testing.T) {
	if os.Getenv("MEKMAA_SHARED_ATTENDANCE_PG_TEST") != "1" {
		if _, err := exec.LookPath("pg_virtualenv"); err != nil {
			t.Skip("pg_virtualenv is not available")
		}
		cmd := exec.Command("pg_virtualenv", os.Args[0], "-test.run=^TestSharedAttendancePostgresConcurrentSaves$")
		cmd.Env = append(os.Environ(), "MEKMAA_SHARED_ATTENDANCE_PG_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("PostgreSQL attendance test: %v\n%s", err, output)
		}
		return
	}
	db, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE student_groups (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO student_groups VALUES (1, 'Shared group')`,
		`CREATE TABLE attendance_records (group_id BIGINT, session_id BIGINT, admission_id BIGINT, attendance_date TEXT, status TEXT, note TEXT, recorded_by_user_id BIGINT, recorded_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{db: db}
	a.runtimeConfig.DBDriver = databaseDriverPostgres
	records := []AttendanceRecord{{GroupID: 1, SessionID: 1, AdmissionID: 1, AttendanceDate: "2026-08-03", Status: "present"}}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- a.writeAttendanceRecords(1, 1, "2026-08-03", records, true) }()
	}
	close(start)
	saved, blocked := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			saved++
		} else if err == errAttendanceAlreadySaved {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || blocked != 1 {
		t.Fatalf("saved %d, blocked %d", saved, blocked)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attendance_records`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("records %d: %v", count, err)
	}
}
