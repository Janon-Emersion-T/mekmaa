package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func programmeTestFixture(t *testing.T) (*App, *StudentEnrollment, int64) {
	t.Helper()
	a := newBookingWorkflowTestApp(t)
	programmeTestExec(t, a, `INSERT INTO finance_transactions (id,receipt_number,category,reference_type,person_name,description,amount,recorded_at,created_at) VALUES (999999,'TRANSFER-RECEIPT','monthly_fee','student_monthly_payment','Transfer Student','Monthly payment',3000,?,?)`, time.Now(), time.Now())
	divisionID, err := divisionIDByCode(a.db, divisionCodeSports)
	if err != nil {
		t.Fatal(err)
	}
	var programs []int64
	for _, name := range []string{"Original", "Destination"} {
		id, err := a.createTrainingProgram(TrainingProgram{DivisionID: divisionID, Name: name, Activity: "badminton", TrainingFormat: "group", AdmissionFee: 1500, MonthlyFee: 3000, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		programs = append(programs, id)
	}
	admission, _, err := a.createAdmissionWithOptionalPayment(Admission{StudentID: "TRANSFER-001", FullName: "Transfer Student", AdmissionDate: "2026-08-01", DateOfBirth: "2014-01-01", Gender: "female", PracticeType: "group_practice", GuardianName: "Guardian", GuardianRelationship: "Parent", GuardianContactNumber: "0771110020"}, false, "cash", 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := a.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{AdmissionID: admission, TrainingProgramID: programs[0], EnrollmentDate: "2026-08-01"}, false, "cash", 0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := a.findStudentEnrollmentByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return a, e, programs[1]
}

func programmeTestExec(t *testing.T, a *App, query string, args ...any) {
	t.Helper()
	if _, err := a.execDB(query, args...); err != nil {
		t.Fatal(err)
	}
}

func programmeTestGroup(t *testing.T, a *App, e *StudentEnrollment) int64 {
	t.Helper()
	var id int64
	err := a.queryRowDB(`INSERT INTO student_groups (name, code, description, training_program_id, created_at, updated_at) VALUES ('Original group', 'TRANSFER-G', '', ?, ?, ?) RETURNING id`, e.TrainingProgramID, time.Now(), time.Now()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnrollmentProgrammeActivityLock(t *testing.T) {
	for _, activity := range []string{"admission", "monthly", "group", "group history", "attendance", "leave"} {
		t.Run(activity, func(t *testing.T) {
			a, e, destination := programmeTestFixture(t)
			switch activity {
			case "admission":
				programmeTestExec(t, a, `UPDATE student_enrollments SET payment_collected = 1 WHERE id = ?`, e.ID)
			case "monthly":
				// A retained voided monthly record must still lock programme identity.
				programmeTestExec(t, a, `INSERT INTO student_monthly_payments (admission_id,enrollment_id,payment_month,amount,finance_transaction_id,collected_at,created_at,voided) VALUES (?,?,'2026-08',3000,999999,?,?,1)`, e.AdmissionID, e.ID, time.Now(), time.Now())
			case "group", "group history", "attendance":
				group := programmeTestGroup(t, a, e)
				if activity == "group" {
					programmeTestExec(t, a, `INSERT INTO student_group_members (group_id, admission_id) VALUES (?,?)`, group, e.AdmissionID)
				}
				if activity == "group history" {
					programmeTestExec(t, a, `INSERT INTO student_group_membership_history (group_id, admission_id,effective_from,effective_to,created_at,updated_at) VALUES (?,?,'2026-08-01','2026-08-10',?,?)`, group, e.AdmissionID, time.Now(), time.Now())
				}
				if activity == "attendance" {
					programmeTestExec(t, a, `INSERT INTO attendance_records (group_id,admission_id,attendance_date,status,recorded_at,updated_at) VALUES (?,?,'2026-08-03','present',?,?)`, group, e.AdmissionID, time.Now(), time.Now())
				}
			case "leave":
				programmeTestExec(t, a, `INSERT INTO student_enrollment_leaves (enrollment_id,start_date,end_date,created_at,updated_at) VALUES (?,'2026-08-03','2026-08-04',?,?)`, e.ID, time.Now(), time.Now())
			}
			e, _ = a.findStudentEnrollmentByID(e.ID)
			reason, err := a.enrollmentProgrammeLockReason(nil, e)
			if err != nil || reason == "" {
				t.Fatalf("lock = %q, %v", reason, err)
			}
			changed := *e
			changed.TrainingProgramID = destination
			changed.DiscountedMonthlyFee = 1234
			var locked *enrollmentProgrammeLockedError
			if err := a.updateStudentEnrollment(changed); !errors.As(err, &locked) {
				t.Fatalf("expected lock, got %v", err)
			}
			stored, err := a.findStudentEnrollmentByID(e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.TrainingProgramID != e.TrainingProgramID || stored.DiscountedMonthlyFee != e.DiscountedMonthlyFee {
				t.Fatal("rejected edit changed the record")
			}
			changed.TrainingProgramID = e.TrainingProgramID
			if err := a.updateStudentEnrollment(changed); err != nil {
				t.Fatalf("ordinary edit blocked: %v", err)
			}
		})
	}
}

func TestEnrollmentProgrammeUnusedCorrection(t *testing.T) {
	a, e, destination := programmeTestFixture(t)
	e.TrainingProgramID = destination
	if err := a.updateStudentEnrollment(*e); err != nil {
		t.Fatal(err)
	}
	stored, err := a.findStudentEnrollmentByID(e.ID)
	if err != nil || stored.TrainingProgramID != destination {
		t.Fatalf("correction failed: %v", err)
	}
}

func TestEnrollmentTransferPreservesHistory(t *testing.T) {
	a, e, destination := programmeTestFixture(t)
	group := programmeTestGroup(t, a, e)
	programmeTestExec(t, a, `INSERT INTO student_group_members (group_id,admission_id) VALUES (?,?)`, group, e.AdmissionID)
	programmeTestExec(t, a, `INSERT INTO student_group_membership_history (group_id,admission_id,effective_from,created_at,updated_at) VALUES (?,?,'2026-08-01',?,?)`, group, e.AdmissionID, time.Now(), time.Now())
	programmeTestExec(t, a, `INSERT INTO attendance_records (group_id,admission_id,attendance_date,status,recorded_at,updated_at) VALUES (?,?,'2026-08-03','present',?,?)`, group, e.AdmissionID, time.Now(), time.Now())
	programmeTestExec(t, a, `INSERT INTO student_monthly_payments (admission_id,enrollment_id,payment_month,amount,finance_transaction_id,collected_at,created_at) VALUES (?,?,'2026-08',3000,999999,?,?)`, e.AdmissionID, e.ID, time.Now(), time.Now())
	next := StudentEnrollment{TrainingProgramID: destination, EnrollmentDate: "2026-08-20", FreeAdmission: true, DiscountedMonthlyFee: 2500}
	id, err := a.transferStudentEnrollment(e.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	old, err := a.findStudentEnrollmentByID(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	newEnrollment, err := a.findStudentEnrollmentByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if old.Active || old.TrainingProgramID != e.TrainingProgramID || !newEnrollment.Active || newEnrollment.TrainingProgramID != destination || newEnrollment.DiscountedMonthlyFee != 2500 || !newEnrollment.FreeAdmission {
		t.Fatal("incorrect transfer result")
	}
	var count int
	for query, want := range map[string]int{
		fmt.Sprintf("SELECT COUNT(*) FROM student_group_members WHERE admission_id=%d", e.AdmissionID):                                                0,
		fmt.Sprintf("SELECT COUNT(*) FROM attendance_records WHERE admission_id=%d AND group_id=%d", e.AdmissionID, group):                            1,
		fmt.Sprintf("SELECT COUNT(*) FROM student_monthly_payments WHERE enrollment_id=%d", e.ID):                                                     1,
		fmt.Sprintf("SELECT COUNT(*) FROM student_monthly_payments WHERE enrollment_id=%d", id):                                                       0,
		fmt.Sprintf("SELECT COUNT(*) FROM student_group_membership_history WHERE admission_id=%d AND effective_to='2026-08-20'", e.AdmissionID):       1,
		fmt.Sprintf("SELECT COUNT(*) FROM student_enrollment_status_history WHERE enrollment_id=%d AND active=1 AND effective_to='2026-08-20'", e.ID): 1,
	} {
		if err := a.queryRowDB(query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s = %d, want %d; %v", query, count, want, err)
		}
	}
	if _, err := a.transferStudentEnrollment(e.ID, next); !errors.Is(err, errEnrollmentTransfer) {
		t.Fatalf("repeat transfer: %v", err)
	}
}

func TestEnrollmentTransferRejectsInvalidDestinationAndRollsBack(t *testing.T) {
	for _, problem := range []string{"same programme", "duplicate", "inactive", "date", "fee", "attendance", "write failure"} {
		t.Run(problem, func(t *testing.T) {
			a, e, destination := programmeTestFixture(t)
			next := StudentEnrollment{TrainingProgramID: destination, EnrollmentDate: "2026-08-20"}
			switch problem {
			case "same programme":
				next.TrainingProgramID = e.TrainingProgramID
			case "duplicate":
				if _, _, err := a.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{AdmissionID: e.AdmissionID, TrainingProgramID: destination, EnrollmentDate: "2026-08-01"}, false, "cash", 0); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				programmeTestExec(t, a, `UPDATE training_programs SET active=0 WHERE id=?`, destination)
			case "date":
				next.EnrollmentDate = "2026-07-31"
			case "fee":
				next.DiscountedMonthlyFee = 4000
			case "attendance":
				group := programmeTestGroup(t, a, e)
				programmeTestExec(t, a, `INSERT INTO attendance_records (group_id,admission_id,attendance_date,status,recorded_at,updated_at) VALUES (?,?,'2026-08-20','present',?,?)`, group, e.AdmissionID, time.Now(), time.Now())
			case "write failure":
				programmeTestExec(t, a, `CREATE TRIGGER fail_archive BEFORE UPDATE OF active ON student_enrollments BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
			}
			if _, err := a.transferStudentEnrollment(e.ID, next); err == nil {
				t.Fatal("invalid transfer succeeded")
			}
			old, err := a.findStudentEnrollmentByID(e.ID)
			if err != nil || !old.Active {
				t.Fatalf("original enrollment changed: %v", err)
			}
			var count int
			if err := a.queryRowDB(`SELECT COUNT(*) FROM student_enrollments WHERE admission_id=?`, e.AdmissionID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if problem == "duplicate" {
				want = 2
			}
			if count != want {
				t.Fatal("partial destination enrollment created")
			}
		})
	}
}

func TestEnrollmentProgrammeBlockedHandlerAndTemplate(t *testing.T) {
	a, e, destination := programmeTestFixture(t)
	programmeTestExec(t, a, `INSERT INTO student_monthly_payments (admission_id,enrollment_id,payment_month,finance_transaction_id,collected_at,created_at) VALUES (?,?,'2026-08',999999,?,?)`, e.AdmissionID, e.ID, time.Now(), time.Now())
	form := url.Values{"csrf_token": {"token"}, "enrollment_id": {fmt.Sprint(e.ID)}, "training_program_id": {fmt.Sprint(destination)}, "enrollment_date": {e.EnrollmentDate}, "discounted_monthly_fee": {"0"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{Roles: []string{"superadmin"}}))
	rec := httptest.NewRecorder()
	a.updateEnrollmentHandler(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "action=edit") {
		t.Fatalf("unexpected response %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var err error
	e.ProgrammeLockReason, err = a.enrollmentProgrammeLockReason(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	html := renderTemplateToString(t, templates, "enrollment-management", TemplateData{SelectedEnrollment: e, SelectedAdmission: &e.Student, EnrollmentMode: "edit"})
	if !strings.Contains(html, `name="training_program_id" required disabled`) || !strings.Contains(html, "monthly payment history") || !strings.Contains(html, `action="/admin/enrollments/transfer"`) {
		t.Fatal("missing lock explanation or transfer form")
	}
}

func TestEnrollmentTransferHandlerAccessAndReview(t *testing.T) {
	for _, scenario := range []string{"success", "csrf", "division access", "review required", "cross division"} {
		t.Run(scenario, func(t *testing.T) {
			a, e, destination := programmeTestFixture(t)
			form := url.Values{"csrf_token": {"token"}, "enrollment_id": {fmt.Sprint(e.ID)}, "training_program_id": {fmt.Sprint(destination)}, "transfer_date": {"2026-08-20"}, "discounted_monthly_fee": {"2000"}, "reviewed": {"true"}}
			user := &User{Roles: []string{"superadmin"}}
			want := http.StatusSeeOther
			if scenario == "csrf" {
				form.Set("csrf_token", "wrong")
				want = http.StatusForbidden
			}
			if scenario == "division access" {
				user = &User{Roles: []string{"admin"}}
				want = http.StatusForbidden
			}
			if scenario == "review required" {
				form.Del("reviewed")
			}
			if scenario == "cross division" {
				other, err := divisionIDByCode(a.db, divisionCodeChess)
				if err != nil {
					t.Fatal(err)
				}
				programmeTestExec(t, a, `UPDATE training_programs SET division_id=? WHERE id=?`, other, destination)
			}
			req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/transfer", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
			req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
			rec := httptest.NewRecorder()
			a.transferEnrollmentHandler(rec, req)
			if rec.Code != want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
			}
			old, err := a.findStudentEnrollmentByID(e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if old.Active != (scenario != "success") {
				t.Fatal("unexpected enrollment status after handler")
			}
		})
	}
}
