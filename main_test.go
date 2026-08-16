package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testUploadFile(t *testing.T, content []byte, originalName string) (multipart.File, *multipart.FileHeader) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file, &multipart.FileHeader{Filename: originalName, Size: int64(len(content))}
}

func seedBookingEngine(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := seedCourtManager(db); err != nil {
		t.Fatalf("seed court manager: %v", err)
	}
	if err := seedPricingRules(db); err != nil {
		t.Fatalf("seed pricing rules: %v", err)
	}
	if err := seedPricingSettings(db); err != nil {
		t.Fatalf("seed pricing settings: %v", err)
	}
}

func createConfirmedFutureBooking(t *testing.T, app *App, daysFromNow int, hour string) int64 {
	t.Helper()
	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, daysFromNow).Format("2006-01-02"),
		SlotHour:       hour,
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Lifecycle Booking",
		RequesterName:  "Lifecycle Customer",
		RequesterEmail: "lifecycle@example.com",
		RequesterPhone: "0700000000",
		QuotedPrice:    5000,
	}
	scheduleID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create booking request: %v", err)
	}
	if _, err := app.updateBookingRequestStatus(scheduleID, bookingStatusConfirmed, "", ""); err != nil {
		t.Fatalf("confirm booking request: %v", err)
	}
	return scheduleID
}

func createConfirmedBookingForTests(t *testing.T, app *App, schedule SpaceSchedule) int64 {
	t.Helper()
	if err := app.createSpaceSchedule(schedule); err != nil {
		t.Fatalf("create confirmed booking: %v", err)
	}
	var scheduleID int64
	if err := app.db.QueryRow(`SELECT id FROM space_schedules WHERE title = ? ORDER BY id DESC LIMIT 1`, schedule.Title).Scan(&scheduleID); err != nil {
		t.Fatalf("lookup created booking id: %v", err)
	}
	return scheduleID
}

func newBookingWorkflowTestApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedFinanceCategories(db); err != nil {
		t.Fatalf("seed finance categories: %v", err)
	}
	seedBookingEngine(t, db)
	if _, err := db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 2500, weekday_peak_price = 3000,
		    weekend_offpeak_price = 2800, weekend_peak_price = 3200
		WHERE activity IN ('full_indoor_cricket', 'badminton', 'table_tennis', 'cricket_net')
	`); err != nil {
		t.Fatalf("configure pricing: %v", err)
	}
	if err := seedRoles(db); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	return &App{
		db: db,
		bookingMessages: BookingCommunicationSettings{
			VenueName:    "Mekmaa",
			VenueAddress: "No. 64, Temple Road, Jaffna 40000",
			ContactPhone: "+94772207297",
			ContactEmail: "bookings@mekmaa.example",
		},
		bookingAccess: BookingAccessSettings{
			BaseURL:     "http://localhost:8080",
			TokenSecret: "test-secret",
			TokenTTL:    180 * 24 * time.Hour,
		},
	}
}

func newReadinessTestApp(t *testing.T) *App {
	t.Helper()
	app := newBookingWorkflowTestApp(t)
	storage, err := prepareUploadStorage(t.TempDir())
	if err != nil {
		t.Fatalf("prepare upload storage: %v", err)
	}
	app.uploads = storage
	app.runtimeConfig = AppRuntimeConfig{
		Env:           appEnvDevelopment,
		Addr:          ":8080",
		DBPath:        filepath.Join(t.TempDir(), "app.db"),
		UploadRoot:    storage.Root,
		PublicBaseURL: "http://localhost:8080",
		CookieSecure:  false,
	}
	app.bookingMessages.EmailEnabled = false
	app.bookingMessages.SMSEnabled = false
	return app
}

func newProductionReadinessTestApp(t *testing.T) *App {
	t.Helper()
	app := newBookingWorkflowTestApp(t)
	root, err := os.MkdirTemp(".", "mekmaa-prod-test-*")
	if err != nil {
		t.Fatalf("create production-style test root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	storage, err := prepareUploadStorage(filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("prepare production-style upload storage: %v", err)
	}
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 2500, weekday_peak_price = 3000,
		    weekend_offpeak_price = 2800, weekend_peak_price = 3200
	`); err != nil {
		t.Fatalf("configure production-style pricing: %v", err)
	}
	app.uploads = storage
	app.runtimeConfig = AppRuntimeConfig{
		Env:           appEnvProduction,
		Addr:          ":8080",
		DBPath:        filepath.Join(root, "app.db"),
		UploadRoot:    storage.Root,
		PublicBaseURL: "https://mekmaa.com",
		CookieSecure:  true,
	}
	app.bookingAccess = BookingAccessSettings{
		BaseURL:     "https://mekmaa.com",
		TokenSecret: strings.Repeat("s", 32),
		TokenTTL:    180 * 24 * time.Hour,
	}
	app.bookingMessages.EmailEnabled = false
	app.bookingMessages.SMSEnabled = false
	return app
}

func TestDivisionSeedingIsIdempotentAndBackfillsPrograms(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	if err := seedDivisions(app.db); err != nil {
		t.Fatalf("seed divisions first pass: %v", err)
	}
	if err := seedDivisions(app.db); err != nil {
		t.Fatalf("seed divisions second pass: %v", err)
	}

	var divisionCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM divisions`).Scan(&divisionCount); err != nil {
		t.Fatalf("count divisions: %v", err)
	}
	if divisionCount != 4 {
		t.Fatalf("expected 4 seeded divisions, got %d", divisionCount)
	}

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division id: %v", err)
	}

	programID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Test Sports Programme",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training program: %v", err)
	}

	if _, err := app.db.Exec(`UPDATE training_programs SET division_id = NULL WHERE id = ?`, programID); err != nil {
		t.Fatalf("clear training program division: %v", err)
	}
	if err := migrateDivisions(app.db); err != nil {
		t.Fatalf("rerun division migration: %v", err)
	}

	var backfilledDivisionID int64
	if err := app.db.QueryRow(`SELECT COALESCE(division_id, 0) FROM training_programs WHERE id = ?`, programID).Scan(&backfilledDivisionID); err != nil {
		t.Fatalf("load backfilled division id: %v", err)
	}
	if backfilledDivisionID != sportsID {
		t.Fatalf("expected program division backfill to %d, got %d", sportsID, backfilledDivisionID)
	}
}

func TestAdmissionsFilterSupportsMultipleDivisionsWithoutDuplicatingStudent(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	sportsProgramID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Sports Starter",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create sports program: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     chessID,
		Name:           "Chess Beginner",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "one_to_one",
		AdmissionFee:   1500,
		MonthlyFee:     2500,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess program: %v", err)
	}

	admission := Admission{
		StudentID:                "STD-MULTI-001",
		FullName:                 "Arun Kumar",
		AdmissionDate:            "2026-08-01",
		DateOfBirth:              "2012-04-03",
		Gender:                   "male",
		PracticeType:             "group_practice",
		Address:                  "Jaffna",
		PassportNumber:           "P123456",
		School:                   "Mekmaa School",
		GuardianName:             "Parent Kumar",
		GuardianRelationship:     "father",
		GuardianContactNumber:    "0771234567",
		GuardianAlternativePhone: "0771234568",
		MedicalInformation:       "None",
		TrainingProgramID:        sportsProgramID,
		TrainingProgramIDs:       []int64{sportsProgramID},
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}

	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:         admissionID,
		TrainingProgramID:   chessProgramID,
		TrainingProgramName: "Chess Beginner",
	}, false, "cash", 0); err != nil {
		t.Fatalf("create chess enrollment: %v", err)
	}

	allAdmissions, total, err := app.listAdmissionsFiltered(AdmissionsFilter{Page: 1, Limit: 25, Direction: "asc"})
	if err != nil {
		t.Fatalf("list all admissions: %v", err)
	}
	if total != 1 || len(allAdmissions) != 1 {
		t.Fatalf("expected one global admission row, got total=%d len=%d", total, len(allAdmissions))
	}
	if len(allAdmissions[0].Divisions) != 2 {
		t.Fatalf("expected two divisions on admission, got %d", len(allAdmissions[0].Divisions))
	}

	chessAdmissions, total, err := app.listAdmissionsFiltered(AdmissionsFilter{Page: 1, Limit: 25, Direction: "asc", Division: "chess"})
	if err != nil {
		t.Fatalf("list chess admissions: %v", err)
	}
	if total != 1 || len(chessAdmissions) != 1 {
		t.Fatalf("expected one chess admission row, got total=%d len=%d", total, len(chessAdmissions))
	}

	kecAdmissions, total, err := app.listAdmissionsFiltered(AdmissionsFilter{Page: 1, Limit: 25, Direction: "asc", Division: "kec"})
	if err != nil {
		t.Fatalf("list kec admissions: %v", err)
	}
	if total != 0 || len(kecAdmissions) != 0 {
		t.Fatalf("expected no KEC admissions, got total=%d len=%d", total, len(kecAdmissions))
	}
}

func TestCollectStudentPaymentHandlerRejectsUnauthorizedDivision(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division: %v", err)
	}
	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find KEC division: %v", err)
	}

	programID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Sports Monthly",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2500,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-DIV-PAY-001",
		FullName:              "Scoped Payment Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000100",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}

	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}

	form := url.Values{
		"csrf_token":     {"token"},
		"enrollment_id":  {strconv.FormatInt(enrollmentID, 10)},
		"payment_month":  {"2026-07"},
		"payment_method": {"cash"},
		"amount":         {"2500"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/student-payments/collect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          91,
		Name:        "KEC Finance",
		Roles:       []string{"admin"},
		Permissions: []string{"finance.manage"},
		DivisionIDs: []int64{kecID},
	}))
	rec := httptest.NewRecorder()

	app.collectStudentPaymentHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("student payment unauthorized status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var paymentCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM student_monthly_payments WHERE enrollment_id = ?`, enrollmentID).Scan(&paymentCount); err != nil {
		t.Fatalf("count student monthly payments: %v", err)
	}
	if paymentCount != 0 {
		t.Fatalf("expected no student payment rows, got %d", paymentCount)
	}
}

func TestSaveAttendanceHandlerRejectsUnauthorizedDivision(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division: %v", err)
	}
	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find KEC division: %v", err)
	}

	programID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Sports Attendance",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-DIV-ATT-001",
		FullName:              "Scoped Attendance Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2011-02-02",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000101",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}

	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}

	if err := app.createStudentGroup(StudentGroup{
		Name:              "Sports Group",
		Code:              "SPORTS-G1",
		TrainingProgramID: programID,
	}, []int64{admissionID}, nil, []StudentGroupSession{{
		Title:     "Monday Session",
		DayOfWeek: "monday",
		StartTime: "09:00",
		EndTime:   "10:00",
		Active:    true,
	}}); err != nil {
		t.Fatalf("create student group: %v", err)
	}

	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list student groups: %v", err)
	}
	var group StudentGroup
	for _, item := range groups {
		if item.Code == "SPORTS-G1" {
			group = item
			break
		}
	}
	if group.ID == 0 || len(group.Sessions) == 0 {
		t.Fatalf("expected sports group with session, got %#v", group)
	}

	form := url.Values{
		"csrf_token":                          {"token"},
		"group_id":                            {strconv.FormatInt(group.ID, 10)},
		"session_id":                          {strconv.FormatInt(group.Sessions[0].ID, 10)},
		"attendance_date":                     {"2026-08-10"},
		fmt.Sprintf("status_%d", admissionID): {"present"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/attendance/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          92,
		Name:        "KEC Staff",
		Roles:       []string{"admin"},
		Permissions: []string{"attendance.manage"},
		DivisionIDs: []int64{kecID},
	}))
	rec := httptest.NewRecorder()

	app.saveAttendanceHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("attendance unauthorized status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var attendanceCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM attendance_records WHERE group_id = ?`, group.ID).Scan(&attendanceCount); err != nil {
		t.Fatalf("count attendance records: %v", err)
	}
	if attendanceCount != 0 {
		t.Fatalf("expected no attendance records, got %d", attendanceCount)
	}
}

func renderTemplateToString(t *testing.T, templates map[string]*template.Template, name string, data TemplateData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := templates[name].ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func statValue(stats []Stat, label string) string {
	for _, stat := range stats {
		if stat.Label == label {
			return stat.Value
		}
	}
	return ""
}

func hasStatLabel(stats []Stat, label string) bool {
	return statValue(stats, label) != ""
}

func TestNewTemplateDataPreservesNonDivisionQueryFields(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/student-payments?month=2026-07&division=chess&action=view&id=7", nil)

	data := app.newTemplateData(httptest.NewRecorder(), req, nil)

	if data.SelectedDivisionScope != "chess" {
		t.Fatalf("selected division scope = %q, want %q", data.SelectedDivisionScope, "chess")
	}
	fields := make(map[string][]string)
	for _, field := range data.CurrentQueryFields {
		fields[field.Key] = append(fields[field.Key], field.Value)
	}
	if _, ok := fields["division"]; ok {
		t.Fatal("division should not be duplicated in preserved query fields")
	}
	if got := strings.Join(fields["month"], ","); got != "2026-07" {
		t.Fatalf("preserved month = %q, want %q", got, "2026-07")
	}
	if got := strings.Join(fields["action"], ","); got != "view" {
		t.Fatalf("preserved action = %q, want %q", got, "view")
	}
	if got := strings.Join(fields["id"], ","); got != "7" {
		t.Fatalf("preserved id = %q, want %q", got, "7")
	}
}

func TestNewTemplateDataHidesDivisionSwitcherForSingleDivisionUser(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find KEC division: %v", err)
	}
	division, err := app.findDivisionByID(kecID)
	if err != nil {
		t.Fatalf("load KEC division: %v", err)
	}
	user := &User{
		ID:          10,
		Name:        "KEC User",
		Roles:       []string{"admin"},
		Permissions: []string{"dashboard.view"},
	}
	fillUserDivisions(user, []Division{*division})
	data := app.newTemplateData(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dashboard", nil), user)
	if len(data.DivisionScopeOptions) != 0 {
		t.Fatalf("expected no division switcher options for single-division user, got %#v", data.DivisionScopeOptions)
	}
	if data.SelectedDivisionScope != division.Slug {
		t.Fatalf("selected division scope = %q, want %q", data.SelectedDivisionScope, division.Slug)
	}
}

func TestCanViewAllDivisionsDoesNotGrantLegacyAdminGlobalScope(t *testing.T) {
	user := &User{
		ID:          11,
		Name:        "Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"dashboard.view", "finance.manage"},
	}
	if canViewAllDivisions(user) {
		t.Fatal("plain admin without explicit elevation should not have global division scope")
	}
}

func TestSidebarHidesSportsBookingModulesForNonSportsDivision(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	data := TemplateData{
		Title:       "Dashboard",
		CurrentPath: "/dashboard",
		User: &User{
			Name:          "KEC Staff",
			Email:         "kec@example.com",
			Roles:         []string{"admin"},
			Permissions:   []string{"dashboard.view", "courts.manage", "space_bookings.manage", "pricing.manage", "booking_requests.manage"},
			DivisionCodes: []string{divisionCodeKEC},
		},
	}
	html := renderTemplateToString(t, templates, "dashboard", data)
	if strings.Contains(html, "Booking Setup") || strings.Contains(html, "Booking Operations") {
		t.Fatal("non-sports division user should not see sports booking navigation")
	}
}

func financeAccountIDByDivisionAndName(t *testing.T, app *App, divisionCode, name string) int64 {
	t.Helper()
	divisionID, err := divisionIDByCode(app.db, divisionCode)
	if err != nil {
		t.Fatalf("lookup %s division: %v", divisionCode, err)
	}
	accounts, err := app.listFinanceAccountsByDivisionIDs([]int64{divisionID}, false)
	if err != nil {
		t.Fatalf("list finance accounts: %v", err)
	}
	for _, account := range accounts {
		if strings.EqualFold(account.Name, name) {
			return account.ID
		}
	}
	t.Fatalf("finance account %q not found", name)
	return 0
}

func financeAccountIDByName(t *testing.T, app *App, name string) int64 {
	t.Helper()
	return financeAccountIDByDivisionAndName(t, app, divisionCodeSports, name)
}

func financeAccountBalanceByName(t *testing.T, app *App, name string) float64 {
	t.Helper()
	accountID := financeAccountIDByName(t, app, name)
	balance, err := app.financeAccountBalance(accountID)
	if err != nil {
		t.Fatalf("finance account balance for %q: %v", name, err)
	}
	return balance
}

func mustFinanceAccounts(t *testing.T, app *App) []FinanceAccount {
	t.Helper()
	accounts, err := app.listFinanceAccounts(false)
	if err != nil {
		t.Fatalf("list finance accounts: %v", err)
	}
	return accounts
}

func bookingTestDataForRequestPage(user *User, schedules []SpaceSchedule, financials []BookingFinancial, collections []BookingPaymentCollection) TemplateData {
	return TemplateData{
		User:                      user,
		CSRFToken:                 "test-token",
		BookingRequestStats:       buildBookingRequestStats(schedules),
		BookingRequests:           schedules,
		BookingFinancials:         financials,
		BookingPaymentCollections: collections,
	}
}

func TestAdmissionManagementTemplateRenders(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}

	html := renderTemplateToString(t, templates, "admission-management", TemplateData{
		Title:                     "Admissions Management",
		Description:               "Manage admissions.",
		CurrentPath:               "/admin/admissions",
		User:                      &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		CSRFToken:                 "test-token",
		Admissions:                []Admission{{ID: 1, StudentID: "STU-0001", FullName: "Test Student", AdmissionDate: "2026-08-01", DateOfBirth: "2012-01-15", Gender: "male", TrainingProgramName: "Cricket", School: "Test School", GuardianName: "Test Guardian", GuardianRelationship: "father", GuardianContactNumber: "0771234567", GuardianAlternativePhone: "0777654321"}},
		AdmissionsTotal:           1,
		AdmissionsStart:           1,
		AdmissionsEnd:             1,
		AdmissionsTotalPages:      1,
		AdmissionsPageNumbers:     []int{1},
		AdmissionsPageBaseURL:     "/admin/admissions?limit=25&direction=asc",
		AdmissionsPreviousPageURL: "/admin/admissions?page=1&limit=25&direction=asc#admissions-directory",
		AdmissionsNextPageURL:     "/admin/admissions?page=1&limit=25&direction=asc#admissions-directory",
		AdmissionsFilter:          AdmissionsFilter{Direction: "asc", Page: 1, Limit: 25},
		TrainingPrograms:          []TrainingProgram{{ID: 1, Name: "Cricket", Active: true, AdmissionFee: 2500}},
	})

	if !strings.Contains(html, "Student directory") {
		t.Fatalf("expected student directory to render")
	}
}

func TestStudentIDCardTemplateRenders(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}

	html := renderTemplateToString(t, templates, "student-id-card", TemplateData{
		Title:       "Student ID",
		Description: "Printable student identity card.",
		HideChrome:  true,
		SelectedAdmission: &Admission{
			ID:            1,
			StudentID:     "STU-0001",
			FullName:      "Test Student",
			DateOfBirth:   "2012-01-15",
			AdmissionDate: "2026-08-01",
			PhotoPath:     "/uploads/students/photos/student-photo-test.jpg",
			QRCodePath:    "/uploads/students/qr/student-qr-test.png",
		},
	})

	if !strings.Contains(html, "Student ID Card") || !strings.Contains(html, "Test Student") || !strings.Contains(html, "STU-0001") {
		t.Fatalf("expected student id card content to render")
	}
}

func TestUploadStorageConfigurationAndCreation(t *testing.T) {
	defaultRoot, err := resolveUploadRoot("")
	if err != nil {
		t.Fatalf("resolve default upload root: %v", err)
	}
	expectedDefault, err := filepath.Abs(defaultUploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if defaultRoot != expectedDefault {
		t.Fatalf("default upload root = %q, want %q", defaultRoot, expectedDefault)
	}

	configured := filepath.Join(t.TempDir(), "mekmaa-uploads")
	t.Setenv("UPLOAD_DIR", configured)
	storage, err := prepareUploadStorage(os.Getenv("UPLOAD_DIR"))
	if err != nil {
		t.Fatalf("prepare configured upload storage: %v", err)
	}
	expectedConfigured, _ := filepath.Abs(configured)
	if storage.Root != expectedConfigured || storage.EventDir != filepath.Join(expectedConfigured, "events") {
		t.Fatalf("unexpected configured storage: %#v", storage)
	}
	for _, directory := range []string{storage.Root, storage.EventDir} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat created directory %s: %v", directory, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", directory)
		}
	}
	if _, err := prepareUploadStorage(os.Getenv("UPLOAD_DIR")); err != nil {
		t.Fatalf("idempotent storage preparation failed: %v", err)
	}
}

func TestEventImageFilenameAndPublicPathSafety(t *testing.T) {
	filename, err := newEventImageFilename(".jpg")
	if err != nil {
		t.Fatalf("generate filename: %v", err)
	}
	if !regexp.MustCompile(`^event-[a-z0-9_-]+\.jpg$`).MatchString(filename) {
		t.Fatalf("unsafe generated filename %q", filename)
	}
	publicPath, err := eventImagePublicPath(filename)
	if err != nil {
		t.Fatalf("generate public path: %v", err)
	}
	if publicPath != "/uploads/events/"+filename {
		t.Fatalf("public path = %q", publicPath)
	}
	for _, unsafe := range []string{"../event-abcdefghijkl.jpg", "event-abcdefghijkl.gif", "/tmp/event-abcdefghijkl.jpg"} {
		if _, err := eventImagePublicPath(unsafe); err == nil {
			t.Fatalf("expected unsafe filename %q to be rejected", unsafe)
		}
	}
}

func TestEventImageSupportedAndUnsupportedTypes(t *testing.T) {
	storage, err := prepareUploadStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	supported := []struct {
		name    string
		content []byte
		ext     string
	}{
		{name: "photo.txt", content: append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("jpeg-content")...), ext: ".jpg"},
		{name: "photo.bin", content: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("png-content")...), ext: ".png"},
		{name: "photo.jpg", content: []byte{'R', 'I', 'F', 'F', 0x04, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}, ext: ".webp"},
	}
	for _, test := range supported {
		file, header := testUploadFile(t, test.content, "../../"+test.name)
		publicPath, err := storage.saveEventImage(file, header)
		if err != nil {
			t.Fatalf("save detected %s image: %v", test.ext, err)
		}
		if !strings.HasPrefix(publicPath, "/uploads/events/event-") || !strings.HasSuffix(publicPath, test.ext) {
			t.Fatalf("unexpected public path %q", publicPath)
		}
		filename := strings.TrimPrefix(publicPath, "/uploads/events/")
		saved, err := os.ReadFile(filepath.Join(storage.EventDir, filename))
		if err != nil {
			t.Fatalf("read saved image: %v", err)
		}
		if !bytes.Equal(saved, test.content) {
			t.Fatalf("saved %s content changed", test.ext)
		}
	}

	for _, unsupported := range [][]byte{
		[]byte("plain text pretending to be an image"),
		append([]byte("GIF89a"), make([]byte, 32)...),
	} {
		file, header := testUploadFile(t, unsupported, "photo.jpg")
		if _, err := storage.saveEventImage(file, header); err == nil {
			t.Fatal("unsupported detected MIME type was accepted")
		}
	}
}

func TestEventImageSizeTraversalDeleteAndReplace(t *testing.T) {
	storage, err := prepareUploadStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oversized := append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, maxEventImageSize)...)
	file, header := testUploadFile(t, oversized, "large.jpg")
	header.Size = 0 // Ensure the streaming limit, not only metadata, rejects it.
	if _, err := storage.saveEventImage(file, header); err == nil {
		t.Fatal("oversized streaming upload was accepted")
	}
	entries, err := os.ReadDir(storage.EventDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed upload left %d incomplete file(s)", len(entries))
	}

	firstFile, firstHeader := testUploadFile(t, append([]byte{0xff, 0xd8, 0xff}, []byte("first")...), "../../escape.jpg")
	firstPath, err := storage.saveEventImage(firstFile, firstHeader)
	if err != nil {
		t.Fatal(err)
	}
	secondFile, secondHeader := testUploadFile(t, append([]byte{0xff, 0xd8, 0xff}, []byte("replacement")...), "replacement.jpg")
	secondPath, err := storage.saveEventImage(secondFile, secondHeader)
	if err != nil {
		t.Fatal(err)
	}
	firstName := strings.TrimPrefix(firstPath, "/uploads/events/")
	secondName := strings.TrimPrefix(secondPath, "/uploads/events/")
	if err := storage.deleteEventImage("/uploads/events/../" + firstName); err == nil {
		t.Fatal("path traversal deletion was not rejected")
	}
	if _, err := os.Stat(filepath.Join(storage.EventDir, firstName)); err != nil {
		t.Fatalf("traversal attempt affected original image: %v", err)
	}
	if err := storage.deleteEventImage(firstPath); err != nil {
		t.Fatalf("delete replaced image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage.EventDir, firstName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced image still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage.EventDir, secondName)); err != nil {
		t.Fatalf("replacement image missing: %v", err)
	}
	if err := storage.deleteEventImage("/event-images/" + secondName); err != nil {
		t.Fatalf("delete legacy public path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage.EventDir, secondName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy-path deletion did not remove image: %v", err)
	}
}

func TestEventImageHTTPServing(t *testing.T) {
	storage, err := prepareUploadStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("served-image")...)
	file, header := testUploadFile(t, content, "served.jpg")
	publicPath, err := storage.saveEventImage(file, header)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerUploadRoutes(mux, storage)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, publicPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("serve uploaded image status = %d", recorder.Code)
	}
	if !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Fatal("served upload content differs from saved content")
	}
}

func newAuthorizationTestApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedFinanceCategories(db); err != nil {
		t.Fatalf("seed finance categories: %v", err)
	}
	if err := seedRoles(db); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	return &App{db: db}
}

func TestCoachOnlyLoadsAssignedStudentGroups(t *testing.T) {
	app := newAuthorizationTestApp(t)

	coach, err := app.createManagedUser(
		"Test Coach",
		"coach@example.com",
		"password-123",
		[]string{"coach"},
		true,
	)
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	assignedGroup := StudentGroup{
		Name:        "Assigned Group",
		Code:        "ASSIGNED",
		Description: "Group assigned to the coach.",
	}

	if err := app.createStudentGroup(
		assignedGroup,
		nil,
		[]int64{coach.ID},
		[]StudentGroupSession{
			{
				Title:     "Morning Session",
				DayOfWeek: "monday",
				StartTime: "09:00",
				EndTime:   "10:00",
				Active:    true,
			},
		},
	); err != nil {
		t.Fatalf("create assigned group: %v", err)
	}

	unassignedGroup := StudentGroup{
		Name:        "Unassigned Group",
		Code:        "UNASSIGNED",
		Description: "Group not assigned to the coach.",
	}

	if err := app.createStudentGroup(
		unassignedGroup,
		nil,
		nil,
		[]StudentGroupSession{
			{
				Title:     "Evening Session",
				DayOfWeek: "tuesday",
				StartTime: "16:00",
				EndTime:   "17:00",
				Active:    true,
			},
		},
	); err != nil {
		t.Fatalf("create unassigned group: %v", err)
	}

	groups, err := app.listStudentGroupsForCoach(coach.ID)
	if err != nil {
		t.Fatalf("list coach groups: %v", err)
	}

	if len(groups) != 1 {
		t.Fatalf("coach group count = %d, want 1", len(groups))
	}

	if groups[0].Code != "ASSIGNED" {
		t.Fatalf(
			"coach received group %q, want ASSIGNED",
			groups[0].Code,
		)
	}

	assigned, err := app.coachAssignedToGroup(
		coach.ID,
		groups[0].ID,
	)
	if err != nil {
		t.Fatalf("check assigned group: %v", err)
	}

	if !assigned {
		t.Fatal("coach assignment was not detected")
	}

	var unassignedGroupID int64

	if err := app.db.QueryRow(`
		SELECT id
		FROM student_groups
		WHERE code = 'UNASSIGNED'
	`).Scan(&unassignedGroupID); err != nil {
		t.Fatalf("find unassigned group: %v", err)
	}

	assigned, err = app.coachAssignedToGroup(
		coach.ID,
		unassignedGroupID,
	)
	if err != nil {
		t.Fatalf("check unassigned group: %v", err)
	}

	if assigned {
		t.Fatal("coach was incorrectly assigned to unassigned group")
	}
}

func TestCustomRolesCanBeAssignedAndAuthorizeRoutes(t *testing.T) {
	app := newAuthorizationTestApp(t)
	if err := app.createRole("finance-officer", []string{"dashboard.view", "finance.manage"}); err != nil {
		t.Fatalf("create custom role: %v", err)
	}
	roles, err := app.normalizeExistingRoles([]string{"finance-officer"})
	if err != nil {
		t.Fatalf("normalize custom role: %v", err)
	}
	user, err := app.createManagedUser("Finance Officer", "finance@example.com", "password-123", roles, true)
	if err != nil {
		t.Fatalf("create custom-role user: %v", err)
	}
	if !containsRole(user.Roles, "finance-officer") || !containsPermission(user.Permissions, "finance.manage") {
		t.Fatalf("custom role was not applied: %#v", user)
	}

	called := false
	protected := app.requirePermission(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}), "finance.manage")
	request := httptest.NewRequest(http.MethodGet, "/admin/finance", nil)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, user))
	protected.ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("custom role permission did not authorize protected route")
	}
}

func TestProtectedRolesCannotBeChangedOrDeleted(t *testing.T) {
	app := newAuthorizationTestApp(t)
	adminID, err := queryRoleID(app.db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.updateRole(adminID, "admin", []string{"dashboard.view"}); !errors.Is(err, ErrSystemRoleProtected) {
		t.Fatalf("expected protected system role error, got %v", err)
	}

	coach, err := app.dbRoleByName("coach")
	if err != nil {
		t.Fatal(err)
	}
	if !coach.System {
		t.Fatal("expected coach to be treated as a system role")
	}
	user, err := app.createManagedUser("Coach", "coach@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = user
	if err := app.deleteRole(coach.ID); !errors.Is(err, ErrSystemRoleProtected) {
		t.Fatalf("expected protected system role error, got %v", err)
	}
	if err := app.updateRole(coach.ID, "coach", []string{"dashboard.view", "attendance.manage"}); !errors.Is(err, ErrSystemRoleProtected) {
		t.Fatalf("expected assigned role error, got %v", err)
	}
}

func TestCoachRolePermissions(t *testing.T) {
	app := newAuthorizationTestApp(t)
	user, err := app.createManagedUser("Coach", "coach-role@example.com", "password-123", []string{"coach"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPermission(user.Permissions, "dashboard.view") {
		t.Fatal("coach should be able to view the dashboard")
	}
	if !containsPermission(user.Permissions, "attendance.manage") {
		t.Fatal("coach should be able to manage attendance")
	}
	if containsPermission(user.Permissions, "student_groups.manage") {
		t.Fatal("coach should not be able to manage student groups")
	}
	if containsPermission(user.Permissions, "users.manage") {
		t.Fatal("coach should not be able to manage users")
	}
}

func TestCoachCRUDLifecycle(t *testing.T) {
	app := newAuthorizationTestApp(t)

	mainCoach, err := app.createCoach(User{
		Name:      "Main Coach",
		Email:     "main-coach@example.com",
		CoachType: "main",
		Active:    true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create main coach: %v", err)
	}

	created, err := app.createCoach(User{
		Name:          "Coach One",
		Email:         "coach-one@example.com",
		Phone:         "0771234567",
		Address:       "Jaffna",
		Specialties:   "Cricket",
		Notes:         "Senior coach",
		CoachType:     "sub",
		ParentCoachID: mainCoach.ID,
		Active:        true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create coach: %v", err)
	}

	found, err := app.findCoachByID(created.ID)
	if err != nil {
		t.Fatalf("find coach: %v", err)
	}
	if found.Phone != "0771234567" || found.Specialties != "Cricket" || !found.Active {
		t.Fatalf("unexpected created coach profile: %#v", found)
	}
	if found.CoachType != "sub" || found.ParentCoachID != mainCoach.ID {
		t.Fatalf("unexpected coach hierarchy: %#v", found)
	}

	if err := app.updateCoach(User{
		ID:          created.ID,
		Name:        "Coach One Updated",
		Email:       "coach-one-updated@example.com",
		Phone:       "0710000000",
		Address:     "Colombo",
		Specialties: "Cricket, Fitness",
		Notes:       "Updated note",
		CoachType:   "main",
		Active:      false,
	}); err != nil {
		t.Fatalf("update coach: %v", err)
	}

	updated, err := app.findCoachByID(created.ID)
	if err != nil {
		t.Fatalf("find updated coach: %v", err)
	}
	if updated.Name != "Coach One Updated" || updated.Email != "coach-one-updated@example.com" {
		t.Fatalf("coach identity was not updated: %#v", updated)
	}
	if updated.Phone != "0710000000" || updated.Address != "Colombo" || updated.Active {
		t.Fatalf("coach profile was not updated: %#v", updated)
	}
	if updated.CoachType != "main" || updated.ParentCoachID != 0 {
		t.Fatalf("coach hierarchy was not updated: %#v", updated)
	}

	activeCoaches, err := app.listCoachUsers()
	if err != nil {
		t.Fatalf("list active coaches: %v", err)
	}
	for _, coach := range activeCoaches {
		if coach.ID == created.ID {
			t.Fatalf("inactive coach should not appear in active coach list: %#v", coach)
		}
	}

	if err := app.deleteCoach(created.ID); err != nil {
		t.Fatalf("delete coach: %v", err)
	}
	if _, err := app.findCoachByID(created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted coach should not be found, got %v", err)
	}
}

func TestSubCoachRequiresMainCoach(t *testing.T) {
	app := newAuthorizationTestApp(t)

	if _, err := app.createCoach(User{
		Name:      "Sub Coach",
		Email:     "sub-no-parent@example.com",
		CoachType: "sub",
		Active:    true,
	}, "password-123"); !errors.Is(err, ErrCoachRequiresMainCoach) {
		t.Fatalf("expected missing main coach error, got %v", err)
	}

	parent, err := app.createCoach(User{
		Name:      "Actual Main",
		Email:     "actual-main@example.com",
		CoachType: "main",
		Active:    true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create main coach: %v", err)
	}
	subParent, err := app.createCoach(User{
		Name:          "Existing Sub",
		Email:         "existing-sub@example.com",
		CoachType:     "sub",
		ParentCoachID: parent.ID,
		Active:        true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create existing sub coach: %v", err)
	}

	if _, err := app.createCoach(User{
		Name:          "Invalid Child",
		Email:         "invalid-child@example.com",
		CoachType:     "sub",
		ParentCoachID: subParent.ID,
		Active:        true,
	}, "password-123"); !errors.Is(err, ErrCoachParentMustBeMain) {
		t.Fatalf("expected parent must be main error, got %v", err)
	}
}

func TestDeleteCoachRejectsAccountsWithOtherRoles(t *testing.T) {
	app := newAuthorizationTestApp(t)

	user, err := app.createManagedUser("Admin Coach", "admin-coach@example.com", "password-123", []string{"coach", "admin"}, true)
	if err != nil {
		t.Fatalf("create mixed-role coach: %v", err)
	}
	if err := app.upsertCoachProfile(user.ID, User{Phone: "0770000000", Active: true}); err != nil {
		t.Fatalf("create mixed-role coach profile: %v", err)
	}

	if err := app.deleteCoach(user.ID); !errors.Is(err, ErrCoachHasOtherRoles) {
		t.Fatalf("expected mixed-role delete rejection, got %v", err)
	}
}

func TestMainCoachWithSubCoachesCannotBeDeletedOrDowngraded(t *testing.T) {
	app := newAuthorizationTestApp(t)

	mainCoach, err := app.createCoach(User{
		Name:      "Main Coach",
		Email:     "main-has-sub@example.com",
		CoachType: "main",
		Active:    true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create main coach: %v", err)
	}
	_, err = app.createCoach(User{
		Name:          "Sub Coach",
		Email:         "sub-under-main@example.com",
		CoachType:     "sub",
		ParentCoachID: mainCoach.ID,
		Active:        true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create sub coach: %v", err)
	}

	if err := app.deleteCoach(mainCoach.ID); !errors.Is(err, ErrCoachHasSubCoaches) {
		t.Fatalf("expected main coach delete rejection, got %v", err)
	}
	if err := app.updateCoach(User{
		ID:            mainCoach.ID,
		Name:          "Main Coach",
		Email:         "main-has-sub@example.com",
		CoachType:     "sub",
		ParentCoachID: mainCoach.ID,
		Active:        true,
	}); !errors.Is(err, ErrCoachHasSubCoaches) {
		t.Fatalf("expected main coach downgrade rejection, got %v", err)
	}
}

func TestSaveCoachAttendanceHandlerPersistsActiveCoachesOnly(t *testing.T) {
	app := newAuthorizationTestApp(t)

	admin, err := app.createManagedUser("Admin", "admin-coach-attendance@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	activeCoach, err := app.createCoach(User{
		Name:   "Active Coach",
		Email:  "active-coach@example.com",
		Active: true,
	}, "password-123")
	if err != nil {
		t.Fatalf("create active coach: %v", err)
	}
	inactiveCoach, err := app.createCoach(User{
		Name:   "Inactive Coach",
		Email:  "inactive-coach@example.com",
		Active: false,
	}, "password-123")
	if err != nil {
		t.Fatalf("create inactive coach: %v", err)
	}

	form := url.Values{
		"csrf_token":                               {"test-csrf"},
		"attendance_date":                          {"2026-08-06"},
		fmt.Sprintf("status_%d", activeCoach.ID):   {"present"},
		fmt.Sprintf("note_%d", activeCoach.ID):     {"On time"},
		fmt.Sprintf("status_%d", inactiveCoach.ID): {"late"},
		fmt.Sprintf("note_%d", inactiveCoach.ID):   {"Should be ignored"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/coaches/attendance/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
	rec := httptest.NewRecorder()

	app.saveCoachAttendanceHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("coach attendance save status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	records, err := app.listCoachAttendanceRecords("2026-08-06")
	if err != nil {
		t.Fatalf("list coach attendance records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("coach attendance record count = %d, want 1", len(records))
	}
	if records[0].UserID != activeCoach.ID || records[0].Status != "present" || records[0].Note != "On time" {
		t.Fatalf("unexpected saved coach attendance record: %#v", records[0])
	}
	if records[0].RecordedByUserID != admin.ID {
		t.Fatalf("coach attendance recorder = %d, want %d", records[0].RecordedByUserID, admin.ID)
	}
}

func TestNonSuperadminCannotGrantAdministratorRoles(t *testing.T) {
	app := newAuthorizationTestApp(t)
	actor, err := app.createManagedUser("Admin", "admin@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for i, privilegedRole := range []string{"admin", "superadmin"} {
		target, err := app.createManagedUser(
			"Customer",
			fmt.Sprintf("customer-%d@example.com", i),
			"password-123",
			[]string{"customer"},
			true,
		)
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{
			"csrf_token": {"test-csrf"},
			"user_id":    {fmt.Sprintf("%d", target.ID)},
			"roles":      {privilegedRole},
		}
		request := httptest.NewRequest(http.MethodPost, "/admin/users/roles", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
		request = request.WithContext(context.WithValue(request.Context(), userContextKey, actor))
		recorder := httptest.NewRecorder()
		app.updateRolesHandler(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("expected forbidden %s escalation response, got %d", privilegedRole, recorder.Code)
		}
		roles, err := app.rolesForUser(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if containsRole(roles, privilegedRole) {
			t.Fatalf("non-superadmin granted the %s role", privilegedRole)
		}
	}
}

func TestAuthorizationCatalogAndManagementTemplates(t *testing.T) {
	var catalogKeys []string
	for _, group := range permissionGroups {
		for _, permission := range group.Permissions {
			catalogKeys = append(catalogKeys, permission.Key)
		}
	}
	if fmt.Sprint(normalizePermissions(catalogKeys)) != fmt.Sprint(normalizePermissions(allPermissions)) {
		t.Fatalf("permission catalog does not match enforced permissions: catalog=%v enforced=%v", catalogKeys, allPermissions)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	admin := &User{ID: 1, Name: "Super Admin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: allPermissions, Verified: true}
	roles := []Role{
		{ID: 1, Name: "admin", System: true, Permissions: allPermissions, UserCount: 1},
		{ID: 2, Name: "coach", Permissions: []string{"attendance.manage"}, UserCount: 2},
	}
	common := TemplateData{
		User:             admin,
		CSRFToken:        "test-token",
		Roles:            roles,
		Available:        []string{"admin", "coach", "superadmin"},
		Permissions:      allPermissions,
		PermissionGroups: permissionGroups,
		Users: []User{
			{ID: 1, Name: "Super Admin", Email: "admin@example.com", Roles: []string{"superadmin"}, Verified: true},
			{ID: 2, Name: "Coach", Email: "coach@example.com", Roles: []string{"coach"}, Verified: true},
		},
	}
	for _, name := range []string{"role-management", "user-management"} {
		if err := templates[name].ExecuteTemplate(io.Discard, "base", common); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
	}
}

func (a *App) dbRoleByName(name string) (*Role, error) {
	var roleID int64
	if err := a.db.QueryRow(`SELECT id FROM roles WHERE name = ?`, name).Scan(&roleID); err != nil {
		return nil, err
	}
	return a.findRoleByID(roleID)
}

func TestCollectStudentMonthlyPayment(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	if err := templates["student-payments"].ExecuteTemplate(io.Discard, "base", TemplateData{
		User: &User{Name: "Test Admin", Email: "admin@example.com"},
		StudentPaymentRows: []StudentPaymentRow{{
			Admission:  Admission{ID: 1, StudentID: "STD-TEST", FullName: "Test Student", PracticeType: "group_practice"},
			Enrollment: StudentEnrollment{ID: 1, TrainingProgramName: "Academy"},
			MonthlyFee: 0,
		}},
		SelectedEnrollment: &StudentEnrollment{ID: 1, TrainingProgramName: "Academy", Student: Admission{FullName: "Test Student"}},
		PaymentMonth:       "2026-07",
		PaymentMonthLabel:  "July 2026",
		TodayDate:          "2026-07",
	}); err != nil {
		t.Fatalf("render student payments template: %v", err)
	}

	db, err := sql.Open("sqlite", "file:student-payment-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE admission_pricing
		SET monthly_fee = 7500
		WHERE practice_type = 'group_practice'
	`); err != nil {
		t.Fatalf("configure pricing: %v", err)
	}

	now := time.Now().UTC()
	result, err := db.Exec(`
		INSERT INTO admissions (
			student_id, full_name, admission_date, date_of_birth, gender, practice_type, address,
			passport_number, school, guardian_name, guardian_relationship, guardian_contact_number,
			guardian_alternative_contact_number, medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"STD-TEST", "Test Student", "2026-01-15", "2012-05-10", "male", "group_practice",
		"Test address", "P-TEST", "Test school", "Test guardian", "Parent", "0700000000",
		"0710000000", "None", now, now,
	)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	admissionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	app := &App{db: db}
	monthDate, _ := parsePaymentMonth("2026-07")
	transactionID, err := app.collectStudentMonthlyPayment(admissionID, "2026-07", monthDate, "cash", 0)
	if err != nil {
		t.Fatalf("collect payment: %v", err)
	}
	if transactionID <= 0 {
		t.Fatal("expected a finance transaction")
	}

	var category string
	var amount float64
	if err := db.QueryRow(`
		SELECT category, amount
		FROM finance_transactions
		WHERE id = ?
	`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatalf("find finance transaction: %v", err)
	}
	if category != "student_monthly_payment" || amount != 7500 {
		t.Fatalf("unexpected transaction: category=%q amount=%.2f", category, amount)
	}

	rows, err := app.listStudentPaymentRows("2026-07")
	if err != nil {
		t.Fatalf("list student payment rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Payment == nil || rows[0].Payment.Amount != 7500 {
		t.Fatalf("monthly register did not include collected payment: %#v", rows)
	}

	_, err = app.collectStudentMonthlyPayment(admissionID, "2026-07", monthDate, "card", 0)
	if !errors.Is(err, ErrStudentPaymentAlreadyCollected) {
		t.Fatalf("expected duplicate payment error, got %v", err)
	}

	var transactionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finance_transactions`).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 1 {
		t.Fatalf("duplicate attempt created a transaction; count=%d", transactionCount)
	}
}

func TestBookingSystemTemplatesRender(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	settings := &PricingSettings{PeakStartHour: "17:00", PeakEndHour: "21:00"}
	pricings := []PricingRule{{
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		WeekdayOffPeak: 5000,
		WeekdayPeak:    7000,
		WeekendOffPeak: 6000,
		WeekendPeak:    8000,
	}}
	request := SpaceSchedule{
		ID:             12,
		SlotDate:       futureDate,
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Template Test",
		Status:         "pending",
		RequesterName:  "Test Customer",
		RequesterEmail: "customer@example.com",
		RequesterPhone: "0700000000",
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now().Add(-time.Hour),
	}
	common := TemplateData{
		User:            &User{Name: "Test Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		CSRFToken:       "test-token",
		CalendarDate:    futureDate,
		TodayDate:       time.Now().Format("2006-01-02"),
		PreviousDate:    time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		NextDate:        time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		Hours:           []string{"18:00"},
		Activities:      bookingActivities(),
		Pricings:        pricings,
		PricingSettings: settings,
		WeekDays: []CalendarDay{{
			Date: futureDate, DayLabel: "Fri", MonthLabel: "Jul", DayNumber: "31", OpenSlotCount: 1, IsSelected: true,
		}},
		BookingSlots: []BookingSlotAvailability{{
			Hour: "18:00", Options: []BookingOption{{Activity: "full_indoor_cricket", Quantity: 1}},
		}},
		DailyStats:          []Stat{{Label: "Open hours", Value: "1"}},
		BookingRequestStats: buildBookingRequestStats([]SpaceSchedule{request}),
		BookingRequests:     []SpaceSchedule{request},
		PendingSchedules:    []SpaceSchedule{request},
	}

	for _, name := range []string{"book", "booking-management", "booking-requests"} {
		data := common
		if name == "book" {
			data.User = nil
		}
		if err := templates[name].ExecuteTemplate(io.Discard, "base", data); err != nil {
			t.Fatalf("render %s template: %v", name, err)
		}
	}

	selected := common
	selected.User = nil
	selected.DraftSchedule = &request
	var rendered bytes.Buffer
	if err := templates["book"].ExecuteTemplate(&rendered, "base", selected); err != nil {
		t.Fatalf("render selected booking template: %v", err)
	}
	html := rendered.String()
	for _, marker := range []string{`id="public-booking-form"`, `form="public-booking-form"`, `data-booking-progress`, `fixed inset-x-0 bottom-0`} {
		if !strings.Contains(html, marker) {
			t.Fatalf("selected booking experience is missing %q", marker)
		}
	}
}

func TestBookingRequestPaymentUIByStatusAndPermission(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	now := time.Now()
	confirmed := SpaceSchedule{
		ID:             101,
		SlotDate:       now.AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "Confirmed Booking",
		Status:         bookingStatusConfirmed,
		RequesterName:  "Confirmed Customer",
		RequesterEmail: "confirmed@example.com",
		RequesterPhone: "0700000001",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
	}
	pending := confirmed
	pending.ID = 102
	pending.Title = "Pending Booking"
	pending.Status = "Pending"
	pending.RequesterEmail = "pending@example.com"
	pending.RequesterPhone = "0700000002"
	held := confirmed
	held.ID = 103
	held.Title = "Held Booking"
	held.Status = "Held"
	held.RequesterEmail = "held@example.com"
	held.RequesterPhone = "0700000003"

	financials := []BookingFinancial{
		{ScheduleID: confirmed.ID, QuotedAmount: 5000, TotalCollected: 2000, OutstandingAmount: 3000, PaymentStatus: "partially_paid", LastPaymentDate: now.Add(-30 * time.Minute)},
		{ScheduleID: pending.ID, QuotedAmount: 2500, TotalCollected: 0, OutstandingAmount: 2500, PaymentStatus: "unpaid"},
		{ScheduleID: held.ID, QuotedAmount: 2500, TotalCollected: 0, OutstandingAmount: 2500, PaymentStatus: "unpaid"},
	}
	collections := []BookingPaymentCollection{
		{ID: 1, ScheduleID: confirmed.ID, FinanceTransactionID: 501, ReceiptNumber: "MKM-BKG-2026-000501", Amount: 2000, PaymentMethod: "cash", CollectedAt: now.Add(-30 * time.Minute)},
		{ID: 2, ScheduleID: confirmed.ID, FinanceTransactionID: 502, ReceiptNumber: "MKM-BKG-2026-000502", Amount: 3000, PaymentMethod: "cash", CollectedAt: now.Add(-20 * time.Minute), Voided: true, VoidReason: "Duplicate cash entry", VoidedAt: now.Add(-10 * time.Minute)},
	}

	bookingStaff := &User{Name: "Booking Staff", Permissions: []string{"space_bookings.manage"}}
	financeUser := &User{Name: "Finance Staff", Permissions: []string{"finance.manage"}}

	confirmedHTML := renderTemplateToString(t, templates, "booking-requests", bookingTestDataForRequestPage(bookingStaff, []SpaceSchedule{confirmed}, financials, collections))
	if !strings.Contains(confirmedHTML, "LKR 5000.00") {
		t.Fatal("confirmed booking request did not render quoted amount")
	}
	if !strings.Contains(confirmedHTML, `action="/admin/bookings/payments/collect"`) {
		t.Fatal("confirmed booking request did not render booking payment form")
	}
	if !strings.Contains(confirmedHTML, `name="payment_method"`) || !strings.Contains(confirmedHTML, `value="bank_transfer"`) || !strings.Contains(confirmedHTML, `value="qr_pay"`) {
		t.Fatal("confirmed booking request did not render the full payment-method selector")
	}
	if !strings.Contains(confirmedHTML, "MKM-BKG-2026-000501") || !strings.Contains(confirmedHTML, "MKM-BKG-2026-000502") {
		t.Fatal("confirmed booking request did not render multiple payment collections")
	}
	if !strings.Contains(confirmedHTML, "Voided") {
		t.Fatal("confirmed booking request did not render voided payment state")
	}
	if strings.Contains(confirmedHTML, "Void</button>") {
		t.Fatal("booking staff should not see void action")
	}

	financeHTML := renderTemplateToString(t, templates, "booking-requests", bookingTestDataForRequestPage(financeUser, []SpaceSchedule{confirmed}, financials, collections))
	if !strings.Contains(financeHTML, "Void</button>") {
		t.Fatal("finance user should see void action on booking requests")
	}

	pendingHTML := renderTemplateToString(t, templates, "booking-requests", bookingTestDataForRequestPage(bookingStaff, []SpaceSchedule{pending}, financials, nil))
	if strings.Contains(pendingHTML, `action="/admin/bookings/payments/collect"`) {
		t.Fatal("pending booking request should not render active payment form")
	}
	if !strings.Contains(pendingHTML, "Payment collection becomes available after the booking is confirmed.") {
		t.Fatal("pending booking request should explain that collection is unavailable")
	}
	if !strings.Contains(pendingHTML, "Accept booking") || !strings.Contains(pendingHTML, "Reject request") {
		t.Fatal("pending booking request should render accept and reject actions")
	}
	if !strings.Contains(pendingHTML, "If SMS or email is unavailable, accept or reject here and then call the customer manually.") {
		t.Fatal("pending booking request should explain that messaging failures do not block acceptance")
	}

	heldHTML := renderTemplateToString(t, templates, "booking-requests", bookingTestDataForRequestPage(bookingStaff, []SpaceSchedule{held}, financials, nil))
	if strings.Contains(heldHTML, `action="/admin/bookings/payments/collect"`) {
		t.Fatal("held booking request should not render active payment form")
	}
}

func TestFilterBookingRequestsByStatusAndSearch(t *testing.T) {
	now := time.Now()
	requests := []SpaceSchedule{
		{ID: 1, Title: "Pending Booking", Activity: "badminton", RequesterName: "Alice", RequesterEmail: "alice@example.com", RequesterPhone: "0700000001", Status: "Pending", CreatedAt: now},
		{ID: 2, Title: "Held Booking", Activity: "tennis", RequesterName: "Bob", RequesterEmail: "bob@example.com", RequesterPhone: "0700000002", Status: "held", CreatedAt: now},
		{ID: 3, Title: "Confirmed Booking", Activity: "futsal", RequesterName: "Carol", RequesterEmail: "carol@example.com", RequesterPhone: "0700000003", Status: "confirmed", CreatedAt: now},
	}

	pending := filterBookingRequests(requests, "pending", "")
	if len(pending) != 1 || pending[0].ID != 1 {
		t.Fatalf("expected only pending request, got %+v", pending)
	}

	held := filterBookingRequests(requests, "held", "")
	if len(held) != 1 || held[0].ID != 2 {
		t.Fatalf("expected only held request, got %+v", held)
	}

	all := filterBookingRequests(requests, "all", "carol")
	if len(all) != 1 || all[0].ID != 3 {
		t.Fatalf("expected search to match confirmed booking, got %+v", all)
	}
}

func TestBookingManagementPaymentHistoryAcrossLifecycleStatuses(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	now := time.Now()
	user := &User{Name: "Finance Staff", Permissions: []string{"finance.manage"}}
	statuses := []string{bookingStatusCancelled, bookingStatusCompleted, bookingStatusNoShow}
	for _, status := range statuses {
		schedule := SpaceSchedule{
			ID:             200,
			SlotDate:       now.AddDate(0, 0, 2).Format("2006-01-02"),
			SlotHour:       "19:00",
			EntryType:      "booking",
			Activity:       "full_indoor_cricket",
			Quantity:       1,
			Title:          "Lifecycle " + status,
			Status:         status,
			RequesterName:  "Lifecycle Customer",
			RequesterEmail: "lifecycle@example.com",
			RequesterPhone: "0700000000",
			CreatedAt:      now.Add(-3 * time.Hour),
			UpdatedAt:      now.Add(-time.Hour),
		}
		financial := BookingFinancial{ScheduleID: schedule.ID, QuotedAmount: 5000, TotalCollected: 2000, OutstandingAmount: 3000, PaymentStatus: "partially_paid", LastPaymentDate: now.Add(-25 * time.Minute)}
		collections := []BookingPaymentCollection{
			{ID: 1, ScheduleID: schedule.ID, FinanceTransactionID: 601, ReceiptNumber: "MKM-BKG-2026-000601", Amount: 2000, PaymentMethod: "cash", CollectedAt: now.Add(-25 * time.Minute)},
			{ID: 2, ScheduleID: schedule.ID, FinanceTransactionID: 602, ReceiptNumber: "MKM-BKG-2026-000602", Amount: 3000, PaymentMethod: "cash", CollectedAt: now.Add(-20 * time.Minute), Voided: true, VoidReason: "Entry error", VoidedAt: now.Add(-10 * time.Minute)},
		}
		html := renderTemplateToString(t, templates, "booking-management", TemplateData{
			User:                      user,
			CSRFToken:                 "test-token",
			CalendarDate:              schedule.SlotDate,
			TodayDate:                 now.Format("2006-01-02"),
			PreviousDate:              now.AddDate(0, 0, 1).Format("2006-01-02"),
			NextDate:                  now.AddDate(0, 0, 3).Format("2006-01-02"),
			Hours:                     []string{"19:00"},
			WeekDays:                  []CalendarDay{{Date: schedule.SlotDate, DayLabel: "Mon", MonthLabel: "Aug", DayNumber: "03", OpenSlotCount: 1, IsSelected: true}},
			DailyStats:                []Stat{{Label: "Open hours", Value: "1"}},
			ScheduleMode:              "view",
			SelectedSchedule:          &schedule,
			DaySchedules:              []SpaceSchedule{schedule},
			BookingFinancials:         []BookingFinancial{financial},
			BookingPaymentCollections: collections,
			AdminCalendarHours:        []AdminCalendarHour{},
		})
		if !strings.Contains(html, "Booking payments") {
			t.Fatalf("%s booking detail did not render payment panel", status)
		}
		if !strings.Contains(html, "MKM-BKG-2026-000601") || !strings.Contains(html, "MKM-BKG-2026-000602") {
			t.Fatalf("%s booking detail did not retain visible payment history", status)
		}
		if !strings.Contains(html, "Payment was previously collected") && status == bookingStatusCancelled {
			t.Fatal("cancelled booking should warn when payment was previously collected")
		}
	}
}

func TestBookingFinanceReceiptTemplateRendersBookingPaymentDetails(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	now := time.Now()
	html := renderTemplateToString(t, templates, "finance-receipt", TemplateData{
		User: &User{Name: "Finance Staff", Permissions: []string{"finance.manage"}},
		SelectedFinance: &FinanceTransaction{
			ID:            701,
			ReceiptNumber: "MKM-BKG-2026-000701",
			Category:      "booking_payment",
			Amount:        3500,
			PaymentMethod: "cash",
			RecordedAt:    now,
		},
		ReceiptBookingPayment: &BookingPaymentCollection{
			ID:                   1,
			ScheduleID:           300,
			FinanceTransactionID: 701,
			ReceiptNumber:        "MKM-BKG-2026-000701",
			Amount:               3500,
			PaymentMethod:        "cash",
			PaymentNote:          "Collected at venue",
			CollectedAt:          now,
			Voided:               true,
			VoidReason:           "Duplicate receipt",
			VoidedAt:             now.Add(5 * time.Minute),
			VoidedByUserName:     "Finance Lead",
		},
		ReceiptBookingSchedule: &SpaceSchedule{
			ID:             300,
			SlotDate:       now.AddDate(0, 0, 2).Format("2006-01-02"),
			SlotHour:       "18:00",
			Activity:       "badminton",
			Quantity:       1,
			Status:         bookingStatusConfirmed,
			RequesterName:  "Receipt Customer",
			RequesterEmail: "receipt@example.com",
			RequesterPhone: "0700000000",
		},
		ReceiptBookingFinancial: &BookingFinancial{
			ScheduleID:        300,
			QuotedAmount:      5000,
			TotalCollected:    3500,
			OutstandingAmount: 1500,
			PaymentStatus:     "partially_paid",
		},
		BookingStatusView: &BookingStatusView{ContactPhone: "+94772207297", ContactEmail: "bookings@mekmaa.example"},
	})
	for _, marker := range []string{"MKM-BKG-2026-000701", "Booking Cash Receipt", bookingReference(300), "LKR 3500.00", "LKR 1500.00", "Voided receipt"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("booking receipt is missing %q", marker)
		}
	}
}

func TestCustomerBookingStatusPaymentVisibilityAndTotals(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	scheduleID := createConfirmedFutureBooking(t, app, 3, "18:00")
	if _, err := app.collectBookingPayment(scheduleID, "cash", 2000, "safe note", 0, false); err != nil {
		t.Fatalf("collect active payment: %v", err)
	}
	if _, err := app.collectBookingPayment(scheduleID, "cash", 3000, "internal note should stay hidden", 0, true); err != nil {
		t.Fatalf("collect second payment: %v", err)
	}
	collections, err := app.listBookingPaymentCollectionsForScheduleIDs([]int64{scheduleID})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	if len(collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(collections))
	}
	if err := app.voidBookingPayment(collections[0].ID, "staff correction reason", 0); err != nil {
		t.Fatalf("void payment: %v", err)
	}
	_, rawToken, err := app.ensureActiveBookingAccessToken(scheduleID, "status")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/booking/status?token="+url.QueryEscape(rawToken), nil)
	rec := httptest.NewRecorder()
	app.publicBookingStatusHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected booking status page, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{"LKR 3000.00", "LKR 2000.00", "MKM-BKG-", "A previous payment record was corrected."} {
		if !strings.Contains(body, marker) {
			t.Fatalf("customer status page missing %q", marker)
		}
	}
	for _, forbidden := range []string{"internal note should stay hidden", "staff correction reason", "safe note"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("customer status page exposed %q", forbidden)
		}
	}
}

func TestPublicBookingRequestRedirectsToStatusWhenCommunicationUnavailable(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	app.bookingMessages.EmailEnabled = false
	app.bookingMessages.SMSEnabled = false

	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {"2026-08-16"},
		"slot_hour":       {"18:00"},
		"booking_option":  {"badminton:1"},
		"title":           {"Status Link Fallback"},
		"requester_name":  {"Fallback Customer"},
		"requester_email": {"fallback@example.com"},
		"requester_phone": {"0772207297"},
	}
	req := httptest.NewRequest(http.MethodPost, "/book/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.publicBookingRequestHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("public booking request status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/booking/status?token=") {
		t.Fatalf("public booking request redirect = %q", location)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), flashCookieName+"=") {
		t.Fatal("expected flash cookie for public booking fallback")
	}
}

func TestCollectBookingPaymentHandlerOverpaymentFlashAndReturnURL(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	scheduleID := createConfirmedFutureBooking(t, app, 4, "20:00")

	form := url.Values{
		"csrf_token":     {"test-csrf"},
		"schedule_id":    {fmt.Sprint(scheduleID)},
		"payment_method": {"cash"},
		"amount":         {"6000"},
		"payment_note":   {"counter cash"},
		"return_to":      {"/admin/bookings?action=view&id=" + fmt.Sprint(scheduleID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/bookings/payments/collect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{ID: 1, Name: "Booking Staff", Permissions: []string{"space_bookings.manage"}}))
	rec := httptest.NewRecorder()
	app.collectBookingPaymentHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/admin/bookings?action=view&id="+fmt.Sprint(scheduleID) {
		t.Fatalf("unexpected return location: %s", location)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "flash=") {
		t.Fatal("overpayment flash message was not set")
	}
}

func TestBookingRequestsPreventPastAndConflictingSlots(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-system-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)

	now := time.Now()

	past := SpaceSchedule{
		SlotDate: now.AddDate(0, 0, -1).Format("2006-01-02"),
		SlotHour: "06:00",
	}

	if err := validateBookableScheduleTime(past, now); err == nil {
		t.Fatal("expected a past booking slot to be rejected")
	}

	activities := []CourtActivity{
		{
			ID:          1,
			CourtID:     1,
			Activity:    "badminton",
			DisplayName: "Badminton",
			MaxQuantity: 1,
			Active:      true,
		},
	}

	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Badminton",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "badminton",
					Quantity: 1,
				},
			},
		},
	}

	days := buildBookingWeekDays(
		nil,
		now,
		bookingHours(),
		activities,
		layouts,
		nil,
	)

	if len(days) != 7 || days[0].IsPast {
		t.Fatalf(
			"expected a forward-looking seven-day calendar, got %#v",
			days,
		)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	request := SpaceSchedule{
		SlotDate:       futureDate,
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "First Request",
		Status:         "pending",
		RequesterName:  "First Customer",
		RequesterEmail: "first@example.com",
		RequesterPhone: "0700000000",
	}
	app := &App{db: db}
	requestID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create first booking request: %v", err)
	}
	if bookingReference(requestID) != "BK-000001" {
		t.Fatalf("unexpected booking reference: %s", bookingReference(requestID))
	}

	request.Title = "Conflicting Request"
	request.RequesterEmail = "second@example.com"
	if _, err := app.createPublicBookingRequest(request); err == nil {
		t.Fatal("expected conflicting booking request to be rejected")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM space_schedules`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflicting request was persisted; count=%d", count)
	}
}

func TestPublicBookingShowsVacantSlotsWithConfiguredPrices(t *testing.T) {
	db, err := sql.Open("sqlite", "file:public-booking-availability-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedCourtManager(db); err != nil {
		t.Fatalf("seed court manager: %v", err)
	}

	if err := seedPricingRules(db); err != nil {
		t.Fatalf("seed pricing rules: %v", err)
	}

	if err := seedPricingSettings(db); err != nil {
		t.Fatalf("seed pricing settings: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 2500, weekday_peak_price = 2500,
		    weekend_offpeak_price = 2500, weekend_peak_price = 2500
		WHERE activity = 'full_indoor_cricket' AND quantity = 1
	`); err != nil {
		t.Fatalf("configure public booking price: %v", err)
	}

	futureDate := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	request := httptest.NewRequest("GET", "/book?date="+futureDate, nil)
	recorder := httptest.NewRecorder()
	app := &App{db: db}
	data, err := app.buildPublicBookingData(recorder, request, nil)
	if err != nil {
		t.Fatalf("build public booking data: %v", err)
	}
	if len(data.BookingSlots) != len(bookingHours()) {
		t.Fatalf("expected all operating hours, got %d", len(data.BookingSlots))
	}
	for _, slot := range data.BookingSlots {
		if len(slot.Options) != 1 || slot.Options[0].Activity != "full_indoor_cricket" {
			t.Fatalf("vacant slot %s did not expose its configured option: %#v", slot.Hour, slot.Options)
		}
		if price := pricingForOption(data.Pricings, data.PricingSettings, futureDate, slot.Hour, "full_indoor_cricket", 1); price != "LKR 2500.00" {
			t.Fatalf("slot %s did not use admin pricing: %s", slot.Hour, price)
		}
	}
	if bookingOpenHourCount(data.BookingSlots) != len(bookingHours()) {
		t.Fatalf("expected every vacant hour to be bookable")
	}
}

func TestStandalonePartialFacilityBookingsAreValid(t *testing.T) {
	badminton := SpaceSchedule{EntryType: "booking", Activity: "badminton", Quantity: 1}
	net := SpaceSchedule{EntryType: "booking", Activity: "cricket_net", Quantity: 1}
	if err := validateSpaceScheduleSlot(nil, badminton); err != nil {
		t.Fatalf("standalone badminton should be bookable: %v", err)
	}
	if err := validateSpaceScheduleSlot(nil, net); err != nil {
		t.Fatalf("standalone cricket net should be bookable: %v", err)
	}
	if err := validateSpaceScheduleSlot([]SpaceSchedule{badminton}, net); err != nil {
		t.Fatalf("badminton and one cricket net should share capacity: %v", err)
	}
	fullFacility := SpaceSchedule{EntryType: "booking", Activity: "full_indoor_cricket", Quantity: 1}
	if err := validateSpaceScheduleSlot([]SpaceSchedule{badminton}, fullFacility); err == nil {
		t.Fatal("full facility booking should not overlap a partial booking")
	}
}

func TestReferralCommissionLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", "file:referral-commission-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)

	app := &App{db: db}
	if err := app.updateReferralCommissionAmount(500); err != nil {
		t.Fatalf("configure referral commission: %v", err)
	}
	partner := ReferralPartner{Name: "Referral Partner", Code: "COACH-01", Phone: "0700000000", Active: true}
	if err := app.createReferralPartner(partner); err != nil {
		t.Fatalf("create referral partner: %v", err)
	}

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "19:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Referral Booking",
		Status:         "pending",
		RequesterName:  "Referred Customer",
		RequesterEmail: "referred@example.com",
		RequesterPhone: "0710000000",
		ReferralCode:   "COACH-01",
	}
	requestID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create referred booking: %v", err)
	}
	referrals, err := app.listBookingReferrals()
	if err != nil || len(referrals) != 1 {
		t.Fatalf("list booking referrals: referrals=%#v err=%v", referrals, err)
	}
	if referrals[0].CommissionAmount != 500 || referrals[0].BookingStatus != "pending" {
		t.Fatalf("unexpected referral snapshot: %#v", referrals[0])
	}
	invalidReferral := request
	invalidReferral.SlotHour = "20:00"
	invalidReferral.ReferralCode = "UNKNOWN"
	if _, err := app.createPublicBookingRequest(invalidReferral); err == nil {
		t.Fatal("expected an unknown referral code to be rejected")
	}
	var scheduleCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM space_schedules`).Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if scheduleCount != 1 {
		t.Fatalf("invalid referral request was not rolled back; count=%d", scheduleCount)
	}
	if _, err := app.payReferralCommission(referrals[0].ID, "cash", 0); err == nil {
		t.Fatal("expected pending referral commission payment to be rejected")
	}
	if _, err := app.updateBookingRequestStatus(requestID, "confirmed", "", ""); err != nil {
		t.Fatalf("confirm referred booking: %v", err)
	}
	transactionID, err := app.payReferralCommission(referrals[0].ID, "bank_transfer", 0)
	if err != nil {
		t.Fatalf("pay referral commission: %v", err)
	}
	if _, err := app.payReferralCommission(referrals[0].ID, "cash", 0); err == nil {
		t.Fatal("expected duplicate referral commission payment to be rejected")
	}

	var category string
	var amount float64
	if err := db.QueryRow(`SELECT category, amount FROM finance_transactions WHERE id = ?`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatal(err)
	}
	if category != "referral_commission_payment" || amount != -500 {
		t.Fatalf("unexpected referral finance transaction: category=%q amount=%.2f", category, amount)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	referrals, err = app.listBookingReferrals()
	if err != nil {
		t.Fatal(err)
	}
	partners, err := app.listReferralPartners(false)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := app.getPricingSettings()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:                &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		BookingReferrals:    referrals,
		ReferralPartners:    partners,
		ReferralPartnerRows: buildReferralPartnerSummaries(partners, referrals),
		ReferralStats:       buildReferralStats(referrals),
		PricingSettings:     settings,
		CSRFToken:           "test-token",
	}
	if err := templates["referral-commissions"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render referral commissions: %v", err)
	}
}

func TestCreateSpaceScheduleCreatesReferralCommissionForDirectBooking(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := app.updateReferralCommissionAmount(750); err != nil {
		t.Fatalf("configure referral commission: %v", err)
	}
	partner := ReferralPartner{
		Name:   "Direct Booking Partner",
		Code:   "DIRECT-01",
		Email:  "direct@example.com",
		Phone:  "0700000001",
		Active: true,
	}
	if err := app.createReferralPartner(partner); err != nil {
		t.Fatalf("create referral partner: %v", err)
	}

	schedule := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Direct Referred Booking",
		RequesterName:  "Walk-in Customer",
		RequesterEmail: "walkin@example.com",
		RequesterPhone: "0711111111",
		QuotedPrice:    3000,
		ReferralCode:   "DIRECT-01",
	}

	scheduleID := createConfirmedBookingForTests(t, app, schedule)
	referrals, err := app.listBookingReferrals()
	if err != nil {
		t.Fatalf("list booking referrals: %v", err)
	}
	if len(referrals) != 1 {
		t.Fatalf("expected one booking referral, got %d", len(referrals))
	}
	if referrals[0].ScheduleID != scheduleID || referrals[0].PartnerCode != "DIRECT-01" {
		t.Fatalf("unexpected referral linkage: %#v", referrals[0])
	}
	if referrals[0].CommissionAmount != 750 || referrals[0].BookingStatus != bookingStatusConfirmed {
		t.Fatalf("unexpected referral state: %#v", referrals[0])
	}

	invalid := schedule
	invalid.Title = "Invalid Direct Referral"
	invalid.SlotHour = "19:00"
	invalid.ReferralCode = "UNKNOWN"
	if err := app.createSpaceSchedule(invalid); err == nil {
		t.Fatal("expected invalid direct booking referral code to be rejected")
	}

	var scheduleCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM space_schedules WHERE title IN (?, ?)`, schedule.Title, invalid.Title).Scan(&scheduleCount); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if scheduleCount != 1 {
		t.Fatalf("invalid direct booking referral should rollback schedule insert; count=%d", scheduleCount)
	}
}

func TestCancelledBookingReferralIsNotPayable(t *testing.T) {
	db, err := sql.Open("sqlite", "file:cancelled-referral-commission-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)

	app := &App{db: db}
	if err := app.updateReferralCommissionAmount(500); err != nil {
		t.Fatalf("configure referral commission: %v", err)
	}
	if err := app.createReferralPartner(ReferralPartner{Name: "Referral Partner", Code: "COACH-02", Phone: "0700000002", Active: true}); err != nil {
		t.Fatalf("create referral partner: %v", err)
	}

	requestID, err := app.createPublicBookingRequest(SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "19:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Cancelled Referral Booking",
		Status:         bookingStatusPending,
		RequesterName:  "Cancelled Customer",
		RequesterEmail: "cancelled@example.com",
		RequesterPhone: "0710000002",
		ReferralCode:   "COACH-02",
	})
	if err != nil {
		t.Fatalf("create referred booking: %v", err)
	}
	if _, err := app.updateBookingRequestStatus(requestID, bookingStatusConfirmed, "", ""); err != nil {
		t.Fatalf("confirm referred booking: %v", err)
	}
	updated, _, err := app.transitionManagedBookingStatus(requestID, bookingStatusCancelled, "Customer cancelled", "", "Customer cancelled", "", "admin", 0)
	if err != nil {
		t.Fatalf("cancel referred booking: %v", err)
	}
	if updated.Status != bookingStatusCancelled {
		t.Fatalf("unexpected status after cancellation: %s", updated.Status)
	}

	referrals, err := app.listBookingReferrals()
	if err != nil {
		t.Fatalf("list booking referrals: %v", err)
	}
	if len(referrals) != 1 {
		t.Fatalf("expected one booking referral, got %d", len(referrals))
	}
	if bookingReferralIsPayable(referrals[0]) {
		t.Fatalf("cancelled referral should not be payable: %#v", referrals[0])
	}
	if buildReferralStats(referrals)[2].Value != money(0) {
		t.Fatalf("expected zero payable referral stats, got %s", buildReferralStats(referrals)[2].Value)
	}
	summary := buildFinanceSummary(nil, nil, nil, nil, referrals, nil)
	if summary.PayableReferrals != 0 {
		t.Fatalf("expected zero payable referrals in finance summary, got %.2f", summary.PayableReferrals)
	}
}

func TestReferralPartnerManagementUsesSharedRate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:referral-partner-management-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{db: db}
	if err := app.updateReferralCommissionAmount(750); err != nil {
		t.Fatalf("set shared commission: %v", err)
	}
	for _, partner := range []ReferralPartner{
		{Name: "Partner One", Code: "PARTNER-ONE", Phone: "0700000001"},
		{Name: "Partner Two", Code: "PARTNER-TWO", Phone: "0700000002"},
	} {
		if err := app.createReferralPartner(partner); err != nil {
			t.Fatalf("create partner: %v", err)
		}
	}
	partners, err := app.listReferralPartners(false)
	if err != nil || len(partners) != 2 {
		t.Fatalf("unexpected partners: %#v err=%v", partners, err)
	}
	settings, err := app.getPricingSettings()
	if err != nil || settings.ReferralCommissionAmount != 750 {
		t.Fatalf("shared rate was not persisted: %#v err=%v", settings, err)
	}
	first := partners[0]
	first.Name = "Updated Partner"
	first.Code = "UPDATED-CODE"
	first.Email = "partner@example.com"
	if err := app.updateReferralPartner(first); err != nil {
		t.Fatalf("update partner: %v", err)
	}
	if err := app.toggleReferralPartner(first.ID); err != nil {
		t.Fatalf("deactivate partner: %v", err)
	}
	partners, err = app.listReferralPartners(false)
	if err != nil {
		t.Fatal(err)
	}
	var updated *ReferralPartner
	for i := range partners {
		if partners[i].ID == first.ID {
			updated = &partners[i]
		}
	}
	if updated == nil || updated.Name != "Updated Partner" || updated.Code != "UPDATED-CODE" || updated.Active {
		t.Fatalf("partner changes were not persisted: %#v", updated)
	}

	summaries := buildReferralPartnerSummaries(partners, []BookingReferral{
		{PartnerID: first.ID, BookingStatus: "confirmed", CommissionAmount: 750},
		{PartnerID: first.ID, BookingStatus: "confirmed", CommissionAmount: 750, Paid: true},
	})
	for _, summary := range summaries {
		if summary.Partner.ID == first.ID {
			if summary.ReferralCount != 2 || summary.PayableAmount != 750 || summary.PaidAmount != 750 {
				t.Fatalf("unexpected partner summary: %#v", summary)
			}
			return
		}
	}
	t.Fatal("updated partner summary not found")
}

func TestFinanceBookingAndManualTransactionLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-system-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{db: db}

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		SlotHour:       "20:00",
		EntryType:      "booking",
		Activity:       "full_indoor_cricket",
		Quantity:       1,
		Title:          "Finance Test Booking",
		RequesterName:  "Finance Customer",
		RequesterEmail: "finance@example.com",
		RequesterPhone: "0700000000",
		QuotedPrice:    7250,
	}
	scheduleID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create booking receivable: %v", err)
	}
	var quotedAmount float64
	if err := db.QueryRow(`SELECT quoted_amount FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&quotedAmount); err != nil {
		t.Fatalf("find booking price snapshot: %v", err)
	}
	if quotedAmount != 7250 {
		t.Fatalf("unexpected booking price snapshot: %.2f", quotedAmount)
	}
	if _, err := app.collectBookingPayment(scheduleID, "cash", 7250, "", 0, false); err == nil {
		t.Fatal("expected pending booking collection to be rejected")
	}
	if _, err := app.updateBookingRequestStatus(scheduleID, "confirmed", "", ""); err != nil {
		t.Fatalf("confirm booking: %v", err)
	}
	transactionID, err := app.collectBookingPayment(scheduleID, "cash", 7250, "", 0, false)
	if err != nil {
		t.Fatalf("collect booking payment: %v", err)
	}
	var category string
	var amount float64
	if err := db.QueryRow(`SELECT category, amount FROM finance_transactions WHERE id = ?`, transactionID).Scan(&category, &amount); err != nil {
		t.Fatal(err)
	}
	if category != "booking_payment" || amount != 7250 {
		t.Fatalf("unexpected booking transaction: category=%q amount=%.2f", category, amount)
	}
	if _, err := app.collectBookingPayment(scheduleID, "cash", 7250, "", 0, false); !errors.Is(err, ErrBookingPaymentAlreadyCollected) {
		t.Fatalf("expected duplicate booking payment error, got %v", err)
	}

	expenseID, err := app.createManualFinanceTransaction(
		"utilities_expense", "Electricity Board", "July electricity", "bank_transfer", -3200, time.Now(), 0,
	)
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}
	var expenseAmount float64
	if err := db.QueryRow(`SELECT amount FROM finance_transactions WHERE id = ?`, expenseID).Scan(&expenseAmount); err != nil {
		t.Fatal(err)
	}
	if expenseAmount != -3200 {
		t.Fatalf("expense sign was not preserved: %.2f", expenseAmount)
	}
	expenses, err := app.listFinanceTransactionsFiltered(FinanceFilter{Direction: "expense", Search: "electricity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(expenses) != 1 || expenses[0].ID != expenseID {
		t.Fatalf("unexpected filtered ledger: %#v", expenses)
	}
	multiFiltered, err := app.listFinanceTransactionsFiltered(FinanceFilter{
		Categories:       []string{"booking_payment", "utilities_expense"},
		SourceTypes:      []string{"booking_payment_collection", "manual"},
		PaymentMethods:   []string{"cash", "bank_transfer"},
		TransactionTypes: []string{"income", "expense"},
		ReferenceTypes:   []string{"space_schedule", "manual"},
		Search:           "finance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(multiFiltered) != 1 || multiFiltered[0].ID != transactionID {
		t.Fatalf("unexpected multi-filtered ledger result: %#v", multiFiltered)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:                &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		CSRFToken:           "test-token",
		TodayDate:           time.Now().Format("2006-01-02"),
		FinanceTransactions: expenses,
		FinanceSummary:      FinanceSummary{GrossIncome: 7250, TotalExpenses: 3200, NetCash: 4050},
	}
	if err := templates["finance-management"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render finance template: %v", err)
	}
}

func TestFinanceSystemAccountsAreCreatedOnce(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-accounts-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations first pass: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations second pass: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finance_accounts WHERE LOWER(name) IN ('cash in hand', 'main bank account')`).Scan(&count); err != nil {
		t.Fatalf("count finance accounts: %v", err)
	}
	if count != 8 {
		t.Fatalf("expected exactly 8 required per-division finance accounts, got %d", count)
	}
}

func TestFinanceTransactionBackfillIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-backfill-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	now := time.Now().UTC()
	result, err := db.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "LEGACY-001", "manual_income", "manual", nil, "Sponsor", "Legacy income", "bank_transfer", 1000, nil, now, now)
	if err != nil {
		t.Fatalf("insert legacy transaction: %v", err)
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	if _, err := db.Exec(`UPDATE finance_transactions SET finance_account_id = NULL, transaction_type = '', source_type = '', source_id = NULL, reference_number = '', updated_at = NULL WHERE id = ?`, transactionID); err != nil {
		t.Fatalf("reset finance extensions: %v", err)
	}
	if err := migrateFinanceCashbook(db); err != nil {
		t.Fatalf("migrate finance cashbook first pass: %v", err)
	}
	if err := migrateFinanceCashbook(db); err != nil {
		t.Fatalf("migrate finance cashbook second pass: %v", err)
	}
	var (
		accountID       int64
		transactionType string
		sourceType      string
		referenceNumber string
	)
	if err := db.QueryRow(`SELECT COALESCE(finance_account_id, 0), transaction_type, source_type, reference_number FROM finance_transactions WHERE id = ?`, transactionID).Scan(&accountID, &transactionType, &sourceType, &referenceNumber); err != nil {
		t.Fatalf("load backfilled transaction: %v", err)
	}
	if accountID == 0 || transactionType != financeTxnTypeIncome || sourceType != "manual" || referenceNumber == "" {
		t.Fatalf("unexpected backfill result: account=%d type=%q source=%q ref=%q", accountID, transactionType, sourceType, referenceNumber)
	}
}

func TestBookingPaymentPostsCashToCashInHandExactlyOnce(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	scheduleID := createConfirmedFutureBooking(t, app, 3, "18:00")
	if _, err := app.collectBookingPayment(scheduleID, "cash", 5000, "", 0, false); err != nil {
		t.Fatalf("collect booking payment: %v", err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 5000 {
		t.Fatalf("cash in hand balance = %.2f, want 5000.00", balance)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE source_type = 'booking_payment_collection'`).Scan(&count); err != nil {
		t.Fatalf("count booking payment ledger rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one booking-payment ledger row, got %d", count)
	}
}

func TestAdmissionPaymentPostsCashToCashInHandExactlyOnce(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-TEST-001",
		FullName:              "Admission Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-05-10",
		Gender:                "female",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000000",
	}
	if _, transactionID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", 0); err != nil {
		t.Fatalf("create admission with payment: %v", err)
	} else if transactionID <= 0 {
		t.Fatal("expected admission payment ledger transaction")
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 1500 {
		t.Fatalf("cash in hand balance = %.2f, want 1500.00", balance)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE source_type = 'admission' AND category = 'admission_payment'`).Scan(&count); err != nil {
		t.Fatalf("count admission ledger rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one admission ledger row, got %d", count)
	}
}

func TestStudentMonthlyPaymentPostsCashToCashInHandExactlyOnce(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure monthly fee: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-TEST-002",
		FullName:              "Monthly Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000001",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	if _, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", 0); err != nil {
		t.Fatalf("collect monthly payment: %v", err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 3200 {
		t.Fatalf("cash in hand balance = %.2f, want 3200.00", balance)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE source_type = 'student_monthly_payment' AND category = 'student_monthly_payment'`).Scan(&count); err != nil {
		t.Fatalf("count monthly-payment ledger rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one monthly-payment ledger row, got %d", count)
	}
}

func TestCashAndBankExpensesAffectCorrectAccounts(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.createManualFinanceTransaction("manual_income", "Cash Sale", "Seed cash", "cash", 2000, time.Now(), 0); err != nil {
		t.Fatalf("seed cash income: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("manual_income", "Wire", "Seed bank", "bank_transfer", 3000, time.Now(), 0); err != nil {
		t.Fatalf("seed bank income: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("utilities_expense", "Vendor", "Cash expense", "cash", -500, time.Now(), 0); err != nil {
		t.Fatalf("cash expense: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("bank_charges_expense", "Bank", "Bank expense", "bank_transfer", -750, time.Now(), 0); err != nil {
		t.Fatalf("bank expense: %v", err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 1500 {
		t.Fatalf("cash in hand balance = %.2f, want 1500.00", balance)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountMainBank); balance != 2250 {
		t.Fatalf("main bank balance = %.2f, want 2250.00", balance)
	}
}

func TestCashToBankTransferLifecycleAndSummaryExclusion(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.createManualFinanceTransaction("manual_income", "Cash Sale", "Seed cash", "cash", 5000, time.Now(), 0); err != nil {
		t.Fatalf("seed cash income: %v", err)
	}
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)
	groupID, err := app.createFinanceTransfer(cashID, bankID, 2000, time.Now(), "DEP-2026-08-03", "Cash deposit", "", 0)
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 3000 {
		t.Fatalf("cash in hand after transfer = %.2f, want 3000.00", balance)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountMainBank); balance != 2000 {
		t.Fatalf("main bank after transfer = %.2f, want 2000.00", balance)
	}
	allTransactions, err := app.listFinanceTransactions()
	if err != nil {
		t.Fatalf("list finance transactions: %v", err)
	}
	summary := buildFinanceSummary(mustFinanceAccounts(t, app), allTransactions, nil, nil, nil, nil)
	if summary.GrossIncome != 5000 || summary.TotalExpenses != 0 || summary.NetOperatingCashFlow != 5000 {
		t.Fatalf("transfer should not change operating totals: %#v", summary)
	}
	if err := app.voidFinanceTransferGroup(groupID, "banked twice", 0); err != nil {
		t.Fatalf("void transfer: %v", err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 5000 {
		t.Fatalf("cash in hand after void = %.2f, want 5000.00", balance)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountMainBank); balance != 0 {
		t.Fatalf("main bank after void = %.2f, want 0.00", balance)
	}
}

func TestCashTransferValidationRules(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)
	if _, err := app.createFinanceTransfer(cashID, cashID, 100, time.Now(), "", "", "", 0); err == nil {
		t.Fatal("expected same-account transfer to be rejected")
	}
	if _, err := app.createFinanceTransfer(cashID, bankID, 100, time.Now(), "", "", "", 0); err == nil {
		t.Fatal("expected overdraft transfer to be rejected")
	}
}

func TestVoidedTransactionsOpeningBalancesAndAdjustmentsAffectBalancesSafely(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	openingID, err := app.createFinanceOpeningBalance(cashID, 1000, time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local), "initial float", 0)
	if err != nil {
		t.Fatalf("create opening balance: %v", err)
	}
	adjustmentID, err := app.createFinanceAdjustment(cashID, -250, time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local), "count correction", 0)
	if err != nil {
		t.Fatalf("create adjustment: %v", err)
	}
	tx, err := app.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := voidFinanceTransactionTx(tx, adjustmentID, "wrong correction", 0); err != nil {
		t.Fatalf("void adjustment: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if balance := financeAccountBalanceByName(t, app, financeAccountCashInHand); balance != 1000 {
		t.Fatalf("cash balance = %.2f, want 1000.00", balance)
	}
	allTransactions, err := app.listFinanceTransactions()
	if err != nil {
		t.Fatalf("list finance transactions: %v", err)
	}
	summary := buildFinanceSummary(mustFinanceAccounts(t, app), allTransactions, nil, nil, nil, nil)
	if summary.GrossIncome != 0 || summary.TotalExpenses != 0 || summary.NetOperatingCashFlow != 0 {
		t.Fatalf("opening balances and adjustments must not affect operating KPIs: %#v", summary)
	}
	if openingID <= 0 {
		t.Fatal("expected opening balance transaction id")
	}
}

func TestCashReconciliationCalculatesStatusesAndRequiresNotes(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.createManualFinanceTransaction("manual_income", "Cash Sale", "Seed cash", "cash", 1000, time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed cash income: %v", err)
	}
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	if _, err := app.createCashReconciliation(cashID, "2026-08-01", 1000, "", 0); err != nil {
		t.Fatalf("balanced reconciliation: %v", err)
	}
	if _, err := app.createCashReconciliation(cashID, "2026-08-02", 900, "", 0); err == nil {
		t.Fatal("expected non-zero reconciliation without notes to be rejected")
	}
	if _, err := app.createCashReconciliation(cashID, "2026-08-02", 900, "short count", 0); err != nil {
		t.Fatalf("short reconciliation: %v", err)
	}
	if _, err := app.createCashReconciliation(cashID, "2026-08-03", 1100, "over count", 0); err != nil {
		t.Fatalf("over reconciliation: %v", err)
	}
	rows, err := app.listCashReconciliations(10)
	if err != nil {
		t.Fatalf("list reconciliations: %v", err)
	}
	if len(rows) != 3 || rows[0].Status != "over" || rows[1].Status != "short" || rows[2].Status != "balanced" {
		t.Fatalf("unexpected reconciliation statuses: %#v", rows)
	}
}

func TestCashReconciliationUsesHistoricalBalanceAsOfSelectedDate(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	if _, err := app.createManualFinanceTransaction("manual_income", "Cash Sale", "Day one", "cash", 1000, time.Date(2026, 8, 1, 9, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed first cash entry: %v", err)
	}
	reconciliationID, err := app.createCashReconciliation(cashID, "2026-08-01", 1000, "", 0)
	if err != nil {
		t.Fatalf("create historical reconciliation: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("manual_income", "Cash Sale", "Day three", "cash", 500, time.Date(2026, 8, 3, 11, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed later cash entry: %v", err)
	}
	var expected float64
	if err := app.db.QueryRow(`SELECT expected_balance FROM cash_reconciliations WHERE id = ?`, reconciliationID).Scan(&expected); err != nil {
		t.Fatalf("load stored expected balance: %v", err)
	}
	if !moneyEquals(expected, 1000) {
		t.Fatalf("stored expected balance = %.2f, want 1000.00", expected)
	}
	backdatedID, err := app.createCashReconciliation(cashID, "2026-07-31", 0, "", 0)
	if err != nil {
		t.Fatalf("create backdated reconciliation: %v", err)
	}
	if err := app.db.QueryRow(`SELECT expected_balance FROM cash_reconciliations WHERE id = ?`, backdatedID).Scan(&expected); err != nil {
		t.Fatalf("load backdated expected balance: %v", err)
	}
	if !moneyEquals(expected, 0) {
		t.Fatalf("backdated expected balance = %.2f, want 0.00", expected)
	}
}

func TestOpeningBalanceMetadataSyncsOnVoidAndReplacement(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	openingID, err := app.createFinanceOpeningBalance(cashID, 1000, time.Date(2026, 8, 1, 9, 0, 0, 0, time.Local), "initial", 0)
	if err != nil {
		t.Fatalf("create opening balance: %v", err)
	}
	if _, err := app.createFinanceOpeningBalance(cashID, 500, time.Date(2026, 8, 2, 9, 0, 0, 0, time.Local), "duplicate", 0); err == nil {
		t.Fatal("expected duplicate active opening balance to be rejected")
	}
	tx, err := app.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := voidFinanceTransactionTx(tx, openingID, "replace opening", 0); err != nil {
		t.Fatalf("void opening balance: %v", err)
	}
	if err := syncFinanceAccountOpeningBalanceMetadataTx(tx, cashID); err != nil {
		t.Fatalf("sync opening balance metadata: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	account, err := app.findFinanceAccountByID(cashID)
	if err != nil {
		t.Fatalf("find finance account: %v", err)
	}
	if !moneyEquals(account.OpeningBalance, 0) {
		t.Fatalf("opening balance metadata = %.2f, want 0.00 after void", account.OpeningBalance)
	}
	if _, err := app.createFinanceOpeningBalance(cashID, 750, time.Date(2026, 8, 2, 9, 0, 0, 0, time.Local), "replacement", 0); err != nil {
		t.Fatalf("create replacement opening balance: %v", err)
	}
	account, err = app.findFinanceAccountByID(cashID)
	if err != nil {
		t.Fatalf("find replacement finance account: %v", err)
	}
	if !moneyEquals(account.OpeningBalance, 750) {
		t.Fatalf("opening balance metadata = %.2f, want 750.00 after replacement", account.OpeningBalance)
	}
}

func TestUpdateFinanceAccountRejectsRenamingWhenHistoryExists(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("lookup sports division: %v", err)
	}
	accountID, err := app.createFinanceAccount(sportsID, "CASH-900", "Tournament Wallet", financeAccountTypeCash, "Temporary collections", 0)
	if err != nil {
		t.Fatalf("create finance account: %v", err)
	}
	if _, err := app.createManualFinanceTransactionForAccount("manual_income", "Desk", "Seed history", "", accountID, 500, time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed finance history: %v", err)
	}
	if err := app.updateFinanceAccount(accountID, "BANK-900", "Renamed Wallet", financeAccountTypeBank, "Changed", true, 0); err == nil {
		t.Fatal("expected account rename/type change with history to be rejected")
	}
	account, err := app.findFinanceAccountByID(accountID)
	if err != nil {
		t.Fatalf("reload finance account: %v", err)
	}
	if account.Name != "Tournament Wallet" || account.AccountType != financeAccountTypeCash {
		t.Fatalf("finance account mutated despite rejection: %#v", account)
	}
}

func TestDeleteFinanceAccountAllowsOnlyUnlinkedAccounts(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("lookup sports division: %v", err)
	}

	deletableID, err := app.createFinanceAccount(sportsID, "CASH-910", "Delete Me", financeAccountTypeCash, "Unused", 0)
	if err != nil {
		t.Fatalf("create deletable finance account: %v", err)
	}
	if err := app.deleteFinanceAccount(deletableID); err != nil {
		t.Fatalf("delete unlinked finance account: %v", err)
	}
	if _, err := app.findFinanceAccountByID(deletableID); err == nil {
		t.Fatal("expected deleted finance account lookup to fail")
	}

	linkedID, err := app.createFinanceAccount(sportsID, "CASH-911", "Keep Me", financeAccountTypeCash, "Linked", 0)
	if err != nil {
		t.Fatalf("create linked finance account: %v", err)
	}
	if _, err := app.createManualFinanceTransactionForAccount("manual_income", "Desk", "Seed history", "", linkedID, 250, time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed linked finance history: %v", err)
	}
	if err := app.deleteFinanceAccount(linkedID); err == nil {
		t.Fatal("expected linked finance account delete to be rejected")
	}
	if _, err := app.findFinanceAccountByID(linkedID); err != nil {
		t.Fatalf("linked finance account should still exist: %v", err)
	}
}

func TestFinanceStatementRunningBalance(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	if _, err := app.createFinanceOpeningBalance(cashID, 1000, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), "opening", 0); err != nil {
		t.Fatalf("opening balance: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("utilities_expense", "Vendor", "Expense", "cash", -200, time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatalf("expense: %v", err)
	}
	if _, err := app.createFinanceAdjustment(cashID, -300, time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), "count correction", 0); err != nil {
		t.Fatalf("adjustment: %v", err)
	}
	statement, err := app.buildFinanceStatement(cashID, "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("build statement: %v", err)
	}
	if statement.OpeningBalance != 0 || statement.ClosingBalance != 500 {
		t.Fatalf("unexpected statement balances: opening=%.2f closing=%.2f", statement.OpeningBalance, statement.ClosingBalance)
	}
	if len(statement.Rows) != 3 || statement.Rows[0].RunningBalance != 1000 || statement.Rows[1].RunningBalance != 800 || statement.Rows[2].RunningBalance != 500 {
		t.Fatalf("unexpected running balances: %#v", statement.Rows)
	}
}

func TestBuildFinanceProfitAndLossAndBalanceSheet(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)

	if _, err := app.createFinanceOpeningBalance(cashID, 1000, time.Date(2026, 8, 1, 9, 0, 0, 0, time.Local), "opening", 0); err != nil {
		t.Fatalf("opening balance: %v", err)
	}
	if _, err := app.createManualFinanceTransactionForAccount("manual_income", "Sponsor", "Sponsorship received", "", cashID, 500, time.Date(2026, 8, 5, 10, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("manual income: %v", err)
	}
	if _, err := app.createManualFinanceTransactionForAccount("utilities_expense", "Utility Board", "Electricity bill", "", cashID, -200, time.Date(2026, 8, 6, 11, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("manual expense: %v", err)
	}
	if _, err := app.createFinanceAdjustment(cashID, -50, time.Date(2026, 8, 7, 12, 0, 0, 0, time.Local), "cash variance", 0); err != nil {
		t.Fatalf("adjustment: %v", err)
	}
	if _, err := app.createFinanceTransfer(cashID, bankID, 300, time.Date(2026, 8, 8, 13, 0, 0, 0, time.Local), "TRF-001", "deposit", "", 0); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	profitAndLoss, err := app.buildFinanceProfitAndLoss("2026-08-01", "2026-08-31", nil)
	if err != nil {
		t.Fatalf("build profit and loss: %v", err)
	}
	if !moneyEquals(profitAndLoss.TotalRevenue, 500) {
		t.Fatalf("profit and loss revenue = %.2f, want 500.00", profitAndLoss.TotalRevenue)
	}
	if !moneyEquals(profitAndLoss.TotalExpenses, 200) {
		t.Fatalf("profit and loss expenses = %.2f, want 200.00", profitAndLoss.TotalExpenses)
	}
	if !moneyEquals(profitAndLoss.OperatingProfit, 300) {
		t.Fatalf("profit and loss operating profit = %.2f, want 300.00", profitAndLoss.OperatingProfit)
	}
	if !moneyEquals(profitAndLoss.OtherNet, -50) {
		t.Fatalf("profit and loss other net = %.2f, want -50.00", profitAndLoss.OtherNet)
	}
	if !moneyEquals(profitAndLoss.NetProfit, 250) {
		t.Fatalf("profit and loss net profit = %.2f, want 250.00", profitAndLoss.NetProfit)
	}

	balanceSheet, err := app.buildFinanceBalanceSheet("2026-08-31", nil)
	if err != nil {
		t.Fatalf("build balance sheet: %v", err)
	}
	if !moneyEquals(balanceSheet.TotalAssets, 1250) {
		t.Fatalf("balance sheet total assets = %.2f, want 1250.00", balanceSheet.TotalAssets)
	}
	if !moneyEquals(balanceSheet.TotalLiabilities, 0) {
		t.Fatalf("balance sheet total liabilities = %.2f, want 0.00", balanceSheet.TotalLiabilities)
	}
	if !moneyEquals(balanceSheet.TotalEquity, 1250) {
		t.Fatalf("balance sheet total equity = %.2f, want 1250.00", balanceSheet.TotalEquity)
	}
	if !moneyEquals(balanceSheet.BalancingDifference, 0) {
		t.Fatalf("balance sheet balancing difference = %.2f, want 0.00", balanceSheet.BalancingDifference)
	}
	if len(balanceSheet.AssetItems) != 2 {
		t.Fatalf("balance sheet asset items = %d, want 2", len(balanceSheet.AssetItems))
	}
}

func TestFinanceRoutesRequirePermissionAndCSRFAuth(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/admin/finance", nil)
	forbiddenReq = forbiddenReq.WithContext(context.WithValue(forbiddenReq.Context(), userContextKey, &User{ID: 1, Name: "No Finance", Permissions: []string{"dashboard.view"}}))
	forbiddenRec := httptest.NewRecorder()
	app.requirePermission(http.HandlerFunc(app.financeManagementHandler), "finance.manage").ServeHTTP(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden finance page, got %d", forbiddenRec.Code)
	}

	csrfForm := url.Values{
		"from_account_id": {"1"},
		"to_account_id":   {"2"},
		"amount":          {"100"},
		"transfer_date":   {"2026-08-03"},
	}
	csrfReq := httptest.NewRequest(http.MethodPost, "/admin/finance/transfers/create", strings.NewReader(csrfForm.Encode()))
	csrfReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	csrfReq = csrfReq.WithContext(context.WithValue(csrfReq.Context(), userContextKey, &User{ID: 1, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}))
	csrfRec := httptest.NewRecorder()
	app.createFinanceTransferHandler(csrfRec, csrfReq)
	if csrfRec.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF failure, got %d", csrfRec.Code)
	}
}

func TestGeneralFinanceVoidRejectsSourceLinkedTransactions(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	scheduleID := createConfirmedFutureBooking(t, app, 1, "10:00")
	transactionID, err := app.collectBookingPayment(scheduleID, "cash", 2500, "", user.ID, false)
	if err != nil {
		t.Fatalf("collect booking payment: %v", err)
	}
	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"attempted direct void"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("general void redirect status = %d", rec.Code)
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload finance transaction: %v", err)
	}
	if transaction.Voided {
		t.Fatal("source-linked booking payment should not be voided through general finance void")
	}
}

func TestGeneralFinanceVoidAllowsStandaloneManualTransactions(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	transactionID, err := app.createManualFinanceTransactionForAccount(
		"manual_income",
		"Walk-in",
		"Manual ledger entry",
		"",
		financeAccountIDByName(t, app, financeAccountCashInHand),
		1000,
		time.Date(2026, time.August, 4, 9, 0, 0, 0, time.Local),
		user.ID,
	)
	if err != nil {
		t.Fatalf("create manual finance entry: %v", err)
	}
	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"entry mistake"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("general void redirect status = %d body=%s", rec.Code, rec.Body.String())
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload manual finance transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("manual finance transaction should be voided: %#v", transaction)
	}
}

func TestGeneralFinanceVoidAllowsOrphanedAdmissionTransactionRepair(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-ORPHAN-001",
		FullName:              "Orphan Invoice Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, transactionID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", user.ID)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	if _, err := app.db.Exec(`DELETE FROM admissions WHERE id = ?`, admissionID); err != nil {
		t.Fatalf("delete admission directly to simulate orphaned source: %v", err)
	}
	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"member deleted before invoice void"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("orphan repair void redirect status = %d body=%s", rec.Code, rec.Body.String())
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload orphaned finance transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("orphaned admission finance transaction should be voided: %#v", transaction)
	}
}

func TestGeneralFinanceVoidAllowsBrokenAdmissionLinkageRepair(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-BROKEN-001",
		FullName:              "Broken Link Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, transactionID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", user.ID)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	if _, err := app.db.Exec(`
		UPDATE admissions
		SET payment_collected = 0,
		    payment_collected_at = NULL,
		    admission_payment_amount = 0,
		    finance_transaction_id = NULL,
		    updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), admissionID); err != nil {
		t.Fatalf("break admission linkage: %v", err)
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("load broken-link finance transaction: %v", err)
	}
	if !transaction.GeneralVoidAllowed || !transaction.OrphanedSource {
		t.Fatalf("broken admission linkage should be ledger-repairable: %#v", transaction)
	}
	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"broken linkage repair"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("broken-link repair redirect status = %d body=%s", rec.Code, rec.Body.String())
	}
	transaction, err = app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload repaired finance transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("broken-link finance transaction should be voided: %#v", transaction)
	}
}

func TestGeneralFinanceVoidAllowsBrokenEnrollmentRegistrationLinkageRepair(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	programID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     kecID,
		Name:           "KEC Broken Registration",
		Activity:       "reading",
		TrainingFormat: "group",
		AdmissionFee:   1800,
		MonthlyFee:     1900,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ENR-BROKEN-001",
		FullName:              "Broken Enrollment Link Student",
		AdmissionDate:         "2026-08-02",
		DateOfBirth:           "2012-09-10",
		Gender:                "male",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000099",
	}, false, "cash", user.ID)
	if err != nil {
		t.Fatalf("create shared student: %v", err)
	}
	enrollmentID, transactionID, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, true, "cash", user.ID)
	if err != nil {
		t.Fatalf("create paid enrollment: %v", err)
	}

	if _, err := app.db.Exec(`
		UPDATE student_enrollments
		SET payment_collected = 0,
		    payment_collected_at = NULL,
		    admission_payment_amount = 0,
		    finance_transaction_id = NULL,
		    updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), enrollmentID); err != nil {
		t.Fatalf("break enrollment linkage: %v", err)
	}

	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("load broken enrollment transaction: %v", err)
	}
	if !transaction.GeneralVoidAllowed || !transaction.OrphanedSource {
		t.Fatalf("broken enrollment linkage should be ledger-repairable: %#v", transaction)
	}

	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"broken enrollment linkage repair"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("broken enrollment-link repair redirect status = %d body=%s", rec.Code, rec.Body.String())
	}

	transaction, err = app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload repaired finance transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("broken enrollment-link finance transaction should be voided: %#v", transaction)
	}
}

func TestFinanceTransactionVoidStateKeepsValidMonthlyPaymentLinked(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500, monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-MONTHLY-VALID-001",
		FullName:              "Valid Monthly Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000010",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	transactionID, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", 0)
	if err != nil {
		t.Fatalf("collect student monthly payment: %v", err)
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("find monthly payment transaction: %v", err)
	}
	if transaction.GeneralVoidAllowed || transaction.OrphanedSource {
		t.Fatalf("valid monthly payment linkage should not be generally voidable: %#v", transaction)
	}
}

func TestGeneralFinanceVoidAllowsBrokenMonthlyPaymentLinkageRepair(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500, monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-MONTHLY-BROKEN-001",
		FullName:              "Broken Monthly Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000011",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", user.ID)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	transactionID, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", user.ID)
	if err != nil {
		t.Fatalf("collect student monthly payment: %v", err)
	}
	var paymentID int64
	if err := app.db.QueryRow(`SELECT id FROM student_monthly_payments WHERE finance_transaction_id = ?`, transactionID).Scan(&paymentID); err != nil {
		t.Fatalf("lookup student payment row: %v", err)
	}
	if _, err := app.db.Exec(`
		UPDATE student_monthly_payments
		SET voided = 1
		WHERE id = ?
	`, paymentID); err != nil {
		t.Fatalf("break student monthly payment linkage: %v", err)
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("find broken monthly payment transaction: %v", err)
	}
	if !transaction.GeneralVoidAllowed || !transaction.OrphanedSource {
		t.Fatalf("broken monthly payment linkage should be repairable: %#v", transaction)
	}
	form := url.Values{
		"transaction_id": {strconv.FormatInt(transactionID, 10)},
		"void_reason":    {"broken monthly payment linkage repair"},
		"csrf_token":     {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/finance/transactions/void", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.voidFinanceTransactionHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("broken monthly repair redirect status = %d body=%s", rec.Code, rec.Body.String())
	}
	transaction, err = app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload repaired monthly payment transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("broken monthly payment transaction should be voided: %#v", transaction)
	}
}

func TestFinanceManagementHandlerPaginatesLargeLedgerWithoutHanging(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	user := &User{ID: 7, Name: "Finance", Roles: []string{"superadmin"}, Permissions: []string{"finance.manage"}}
	accountID := financeAccountIDByName(t, app, financeAccountCashInHand)
	for i := 0; i < 120; i++ {
		recordedAt := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.Local).Add(-time.Duration(i) * time.Minute)
		if _, err := app.createManualFinanceTransactionForAccount(
			"manual_income",
			fmt.Sprintf("Person %03d", i),
			fmt.Sprintf("Manual ledger entry %03d", i),
			"",
			accountID,
			float64(i+1),
			recordedAt,
			user.ID,
		); err != nil {
			t.Fatalf("create manual ledger transaction %d: %v", i, err)
		}
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/admin/finance/ledger?page=2&limit=50", nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
		rec := httptest.NewRecorder()
		app.financeLedgerHandler(rec, req)
		done <- rec
	}()

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("finance management status = %d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "50 shown of 120 matching transactions") {
			t.Fatalf("expected paginated ledger summary, body=%s", body)
		}
		if !strings.Contains(body, "Manual ledger entry 050") || !strings.Contains(body, "Manual ledger entry 099") {
			t.Fatalf("expected second page entries in body=%s", body)
		}
		if strings.Contains(body, "Manual ledger entry 000") || strings.Contains(body, "Manual ledger entry 119") {
			t.Fatalf("unexpected out-of-page ledger entries in body=%s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finance management handler hung while rendering paginated ledger")
	}
}

func TestSourceLevelVoidWorkflowsSynchronizeLedger(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	userID := int64(9)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500, monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-VOID-001",
		FullName:              "Voidable Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, admissionTxnID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", userID)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	if err := app.voidAdmissionPayment(admissionID, "entered twice", userID); err != nil {
		t.Fatalf("void admission payment: %v", err)
	}
	admissionRow, err := app.findAdmissionByID(admissionID)
	if err != nil {
		t.Fatalf("reload admission: %v", err)
	}
	if admissionRow.PaymentCollected || admissionRow.PaymentVoidedAt.IsZero() || admissionRow.PaymentVoidReason == "" {
		t.Fatalf("admission void state not recorded: %#v", admissionRow)
	}
	admissionTxn, err := app.findFinanceTransactionByID(admissionTxnID)
	if err != nil {
		t.Fatalf("reload admission ledger transaction: %v", err)
	}
	if !admissionTxn.Voided {
		t.Fatal("admission ledger transaction should be voided")
	}

	monthDate, _ := parsePaymentMonth("2026-08")
	studentTxnID, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", userID)
	if err != nil {
		t.Fatalf("collect student payment: %v", err)
	}
	var paymentID int64
	if err := app.db.QueryRow(`SELECT id FROM student_monthly_payments WHERE finance_transaction_id = ?`, studentTxnID).Scan(&paymentID); err != nil {
		t.Fatalf("lookup student payment row: %v", err)
	}
	if err := app.voidStudentMonthlyPayment(paymentID, "duplicate month", userID); err != nil {
		t.Fatalf("void student payment: %v", err)
	}
	studentTxn, err := app.findFinanceTransactionByID(studentTxnID)
	if err != nil {
		t.Fatalf("reload student ledger transaction: %v", err)
	}
	if !studentTxn.Voided {
		t.Fatal("student monthly ledger transaction should be voided")
	}
	var studentVoided int
	if err := app.db.QueryRow(`SELECT voided FROM student_monthly_payments WHERE id = ?`, paymentID).Scan(&studentVoided); err != nil {
		t.Fatalf("reload student payment row: %v", err)
	}
	if studentVoided != 1 {
		t.Fatal("student monthly payment row should be marked voided")
	}
}

func TestAdmissionVoidResolvesEnrollmentOwnedRegistrationPayment(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	userID := int64(17)

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	kecProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     kecID,
		Name:           "KEC Registration Paid",
		Activity:       "reading",
		TrainingFormat: "group",
		AdmissionFee:   2000,
		MonthlyFee:     1800,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create kec training programme: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Registration Unpaid",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2200,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ENR-VOID-001",
		FullName:              "Enrollment Void Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2013-04-05",
		Gender:                "female",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771111234",
	}, false, "cash", userID)
	if err != nil {
		t.Fatalf("create shared student: %v", err)
	}

	paidEnrollmentID, transactionID, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: kecProgramID,
	}, true, "cash", userID)
	if err != nil {
		t.Fatalf("create paid enrollment: %v", err)
	}
	unpaidEnrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: chessProgramID,
	}, false, "cash", userID)
	if err != nil {
		t.Fatalf("create unpaid enrollment: %v", err)
	}

	if err := app.voidAdmissionPayment(admissionID, "entered twice", userID); err != nil {
		t.Fatalf("void enrollment-owned registration payment: %v", err)
	}

	paidEnrollment, err := app.findStudentEnrollmentByID(paidEnrollmentID)
	if err != nil {
		t.Fatalf("reload paid enrollment: %v", err)
	}
	if paidEnrollment.AdmissionPaymentPaid {
		t.Fatalf("paid enrollment should be cleared after void: %#v", paidEnrollment)
	}
	if paidEnrollment.FinanceTransactionID != 0 {
		t.Fatalf("paid enrollment finance transaction should be cleared, got %d", paidEnrollment.FinanceTransactionID)
	}

	unpaidEnrollment, err := app.findStudentEnrollmentByID(unpaidEnrollmentID)
	if err != nil {
		t.Fatalf("reload unpaid enrollment: %v", err)
	}
	if unpaidEnrollment.AdmissionPaymentPaid {
		t.Fatalf("unpaid enrollment should remain unpaid: %#v", unpaidEnrollment)
	}

	admissionRow, err := app.findAdmissionByID(admissionID)
	if err != nil {
		t.Fatalf("reload student identity row: %v", err)
	}
	if admissionRow.PaymentCollected {
		t.Fatalf("shared student record should not become globally paid: %#v", admissionRow)
	}

	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload registration transaction: %v", err)
	}
	if !transaction.Voided {
		t.Fatalf("enrollment-owned registration transaction should be voided: %#v", transaction)
	}
}

func TestDeleteAdmissionAllowsAdmissionPaymentOnlyAndLeavesLedgerVoidable(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-DEL-001",
		FullName:              "Delete Allowed Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, transactionID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", 0)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	if err := app.deleteAdmission(admissionID); err != nil {
		t.Fatalf("delete admission: %v", err)
	}
	if _, err := app.findAdmissionByID(admissionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted admission to be missing, got %v", err)
	}
	transaction, err := app.findFinanceTransactionByID(transactionID)
	if err != nil {
		t.Fatalf("reload admission ledger transaction: %v", err)
	}
	if !transaction.GeneralVoidAllowed {
		t.Fatalf("expected deleted student's advance payment to become voidable, transaction=%#v", transaction)
	}
	if !transaction.OrphanedSource {
		t.Fatalf("expected deleted student's advance payment to be marked orphaned, transaction=%#v", transaction)
	}
}

func TestDeleteAdmissionRejectsActiveMonthlyPaymentHistory(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500, monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-KEEP-001",
		FullName:              "Protected Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", 0)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	if _, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", 0); err != nil {
		t.Fatalf("collect student payment: %v", err)
	}
	if err := app.deleteAdmission(admissionID); !errors.Is(err, ErrAdmissionHasMonthlyPaymentHistory) {
		t.Fatalf("expected monthly-payment delete rejection, got %v", err)
	}
}

func TestDeleteAdmissionHandlerRedirectsInsteadOfReturningInternalServerError(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500, monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-HANDLER-001",
		FullName:              "Handler Protected Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", 0)
	if err != nil {
		t.Fatalf("create admission with payment: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	if _, err := app.collectStudentMonthlyPayment(admissionID, "2026-08", monthDate, "cash", 0); err != nil {
		t.Fatalf("collect student payment: %v", err)
	}

	form := url.Values{
		"admission_id": {strconv.FormatInt(admissionID, 10)},
		"csrf_token":   {"token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/admissions/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	rec := httptest.NewRecorder()

	app.deleteAdmissionHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect instead of internal server error, got %d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/admissions" {
		t.Fatalf("expected redirect to admissions page, got %q", location)
	}
	flashCookie := rec.Result().Cookies()
	flashFound := false
	for _, cookie := range flashCookie {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			flashFound = true
			break
		}
	}
	if !flashFound {
		t.Fatal("expected flash message cookie on delete failure")
	}
}

func TestCreateAdmissionWithFreeAdmissionSkipsFinanceCollection(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET price = 1500 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-FREE-ADM-001",
		FullName:              "Free Admission Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
		FreeAdmission:         true,
	}
	admissionID, financeTransactionID, err := app.createAdmissionWithOptionalPayment(admission, true, "cash", 0)
	if err != nil {
		t.Fatalf("create admission with free admission: %v", err)
	}
	if financeTransactionID != 0 {
		t.Fatalf("expected no finance transaction for free admission, got %d", financeTransactionID)
	}
	stored, err := app.findAdmissionByID(admissionID)
	if err != nil {
		t.Fatalf("reload admission: %v", err)
	}
	if !stored.FreeAdmission {
		t.Fatal("expected free admission flag to persist")
	}
	if stored.PaymentCollected {
		t.Fatal("free admission should not mark payment collected")
	}
	if stored.FinanceTransactionID != 0 {
		t.Fatalf("expected no admission finance transaction, got %d", stored.FinanceTransactionID)
	}
}

func TestListStudentPaymentRowsTreatsFreeMonthlyFeeAsNonPayable(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE admission_pricing SET monthly_fee = 3200 WHERE practice_type = 'group_practice'`); err != nil {
		t.Fatalf("configure admission pricing: %v", err)
	}
	admission := Admission{
		StudentID:             "STD-FREE-MON-001",
		FullName:              "Free Monthly Student",
		AdmissionDate:         "2026-07-15",
		DateOfBirth:           "2011-01-20",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0770000002",
		FreeMonthlyFee:        true,
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission with free monthly fee: %v", err)
	}
	rows, err := app.listStudentPaymentRows("2026-08")
	if err != nil {
		t.Fatalf("list student payment rows: %v", err)
	}
	for _, row := range rows {
		if row.Admission.ID != admissionID {
			continue
		}
		if !row.Admission.FreeMonthlyFee {
			t.Fatal("expected free monthly fee flag on payment row")
		}
		if row.OriginalMonthlyFee <= 0 {
			t.Fatalf("expected original monthly fee to stay configured, got %v", row.OriginalMonthlyFee)
		}
		if row.MonthlyFee != 0 {
			t.Fatalf("expected effective monthly fee to be zero for waived student, got %v", row.MonthlyFee)
		}
		if row.Payment != nil {
			t.Fatal("expected no monthly payment record for waived student")
		}
		return
	}
	t.Fatalf("expected payment row for admission %d", admissionID)
}

func TestListStudentPaymentRowsProratesEnrollmentLeave(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Leave Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     6200,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-LEAVE-001",
		FullName:              "Leave Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000000",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	enrollments, err := app.listStudentEnrollments()
	if err != nil {
		t.Fatalf("list enrollments: %v", err)
	}
	var enrollmentID int64
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID && enrollment.TrainingProgramID == programID {
			enrollmentID = enrollment.ID
			break
		}
	}
	if enrollmentID == 0 {
		t.Fatal("expected enrollment to exist")
	}
	if err := app.createStudentEnrollmentLeave(enrollmentID, "2026-08-10", "2026-08-19", "Family travel"); err != nil {
		t.Fatalf("create leave: %v", err)
	}

	rows, err := app.listStudentPaymentRows("2026-08")
	if err != nil {
		t.Fatalf("list student payment rows: %v", err)
	}
	for _, row := range rows {
		if row.Enrollment.ID != enrollmentID {
			continue
		}
		if row.LeaveDays != 10 {
			t.Fatalf("leave days = %d, want 10", row.LeaveDays)
		}
		if row.MonthDays != 31 {
			t.Fatalf("month days = %d, want 31", row.MonthDays)
		}
		if row.MonthlyFee != 4200 {
			t.Fatalf("monthly fee = %.2f, want 4200.00", row.MonthlyFee)
		}
		if row.LeaveAmount != 2000 {
			t.Fatalf("leave amount = %.2f, want 2000.00", row.LeaveAmount)
		}
		return
	}
	t.Fatalf("expected payment row for enrollment %d", enrollmentID)
}

func TestListStudentPaymentRowsDiscountsSecondHalfEnrollmentMonth(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Second Half Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     5000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-HALF-001",
		FullName:              "Second Half Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000002",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	enrollments, err := app.listStudentEnrollments()
	if err != nil {
		t.Fatalf("list enrollments: %v", err)
	}
	var enrollmentID int64
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID && enrollment.TrainingProgramID == programID {
			enrollmentID = enrollment.ID
			break
		}
	}
	if enrollmentID == 0 {
		t.Fatal("expected enrollment to exist")
	}
	if _, err := app.db.Exec(`UPDATE student_enrollments SET created_at = ?, updated_at = ? WHERE id = ?`, "2026-07-20 09:00:00", "2026-07-20 09:00:00", enrollmentID); err != nil {
		t.Fatalf("update enrollment created_at: %v", err)
	}

	rows, err := app.listStudentPaymentRows("2026-07")
	if err != nil {
		t.Fatalf("list student payment rows: %v", err)
	}
	for _, row := range rows {
		if row.Enrollment.ID != enrollmentID {
			continue
		}
		if row.OriginalMonthlyFee != 5000 {
			t.Fatalf("original monthly fee = %.2f, want 5000.00", row.OriginalMonthlyFee)
		}
		if row.MonthlyFee != 2500 {
			t.Fatalf("monthly fee = %.2f, want 2500.00", row.MonthlyFee)
		}
		if row.EnrollmentProrationAmount != 2500 {
			t.Fatalf("enrollment proration amount = %.2f, want 2500.00", row.EnrollmentProrationAmount)
		}
		return
	}
	t.Fatalf("expected payment row for enrollment %d", enrollmentID)
}

func TestCollectStudentMonthlyPaymentRejectsFullMonthLeave(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Full Leave Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     5000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-LEAVE-002",
		FullName:              "Full Leave Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000001",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	enrollments, err := app.listStudentEnrollments()
	if err != nil {
		t.Fatalf("list enrollments: %v", err)
	}
	var enrollmentID int64
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID && enrollment.TrainingProgramID == programID {
			enrollmentID = enrollment.ID
			break
		}
	}
	if enrollmentID == 0 {
		t.Fatal("expected enrollment to exist")
	}
	if err := app.createStudentEnrollmentLeave(enrollmentID, "2026-08-01", "2026-08-31", "Medical leave"); err != nil {
		t.Fatalf("create leave: %v", err)
	}
	monthDate, _ := parsePaymentMonth("2026-08")
	if _, err := app.collectStudentMonthlyPayment(enrollmentID, "2026-08", monthDate, "cash", 0); !errors.Is(err, ErrStudentLeaveCoversMonth) {
		t.Fatalf("expected full-month leave error, got %v", err)
	}
}

func TestCollectStudentPaymentHandlerRejectsCurrentMonthBeforeMonthEnd(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	programID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Collection Timing Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     4000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-TIMING-001",
		FullName:              "Timing Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2012-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771000003",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: programID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	enrollments, err := app.listStudentEnrollments()
	if err != nil {
		t.Fatalf("list enrollments: %v", err)
	}
	var enrollmentID int64
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID && enrollment.TrainingProgramID == programID {
			enrollmentID = enrollment.ID
			break
		}
	}
	if enrollmentID == 0 {
		t.Fatal("expected enrollment to exist")
	}

	form := url.Values{
		"csrf_token":     {"token"},
		"enrollment_id":  {strconv.FormatInt(enrollmentID, 10)},
		"payment_month":  {"2026-08"},
		"payment_method": {"cash"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/student-payments/collect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.collectStudentPaymentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("student payment collect status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/admin/student-payments?month=2026-08" {
		t.Fatalf("student payment collect redirect = %q", got)
	}
	flashFound := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			flashFound = true
			break
		}
	}
	if !flashFound {
		t.Fatal("expected flash cookie for non-collectible current month")
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM student_monthly_payments WHERE enrollment_id = ? AND payment_month = '2026-08' AND COALESCE(voided, 0) = 0`, enrollmentID).Scan(&count); err != nil {
		t.Fatalf("count blocked monthly payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no monthly payment row to be created, got %d", count)
	}
}

func TestAttendanceLimitWarningsScopeToTrainingProgramme(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	programA, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Programme A",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create programme A: %v", err)
	}
	programB, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Programme B",
		Activity:       "badminton",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create programme B: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ATT-001",
		FullName:              "Attendance Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2011-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Parent",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771231234",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if err := app.createStudentGroup(StudentGroup{
		Name:              "Group A",
		Code:              "G-A",
		TrainingProgramID: programA,
	}, []int64{admissionID}, nil, []StudentGroupSession{{Title: "A Session", DayOfWeek: "monday", StartTime: "09:00", EndTime: "10:00", Active: true}}); err != nil {
		t.Fatalf("create group A: %v", err)
	}
	if err := app.createStudentGroup(StudentGroup{
		Name:              "Group B",
		Code:              "G-B",
		TrainingProgramID: programB,
	}, []int64{admissionID}, nil, []StudentGroupSession{{Title: "B Session", DayOfWeek: "tuesday", StartTime: "09:00", EndTime: "10:00", Active: true}}); err != nil {
		t.Fatalf("create group B: %v", err)
	}
	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list student groups: %v", err)
	}
	var groupA, groupB StudentGroup
	for _, group := range groups {
		switch group.Code {
		case "G-A":
			groupA = group
		case "G-B":
			groupB = group
		}
	}
	if groupA.ID == 0 || groupB.ID == 0 {
		t.Fatalf("expected both groups to exist: %#v", groups)
	}
	sessionA := groupA.Sessions[0]
	sessionB := groupB.Sessions[0]
	for i := 0; i < 9; i++ {
		date := time.Date(2026, 8, 3+i, 9, 0, 0, 0, time.Local).Format("2006-01-02")
		if err := app.replaceAttendanceRecords(groupA.ID, sessionA.ID, date, []AttendanceRecord{{
			GroupID:        groupA.ID,
			SessionID:      sessionA.ID,
			SessionTitle:   sessionA.Title,
			AdmissionID:    admissionID,
			AttendanceDate: date,
			Status:         "present",
		}}); err != nil {
			t.Fatalf("seed programme A attendance %d: %v", i, err)
		}
	}
	dateB := "2026-08-11"
	if err := app.replaceAttendanceRecords(groupB.ID, sessionB.ID, dateB, []AttendanceRecord{{
		GroupID:        groupB.ID,
		SessionID:      sessionB.ID,
		SessionTitle:   sessionB.Title,
		AdmissionID:    admissionID,
		AttendanceDate: dateB,
		Status:         "present",
	}}); err != nil {
		t.Fatalf("seed programme B attendance: %v", err)
	}
	warnings, err := app.listAttendanceLimitWarnings(groupB.ID, sessionB.ID, dateB, 8)
	if err != nil {
		t.Fatalf("list attendance limit warnings: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for separate programme, got %#v", warnings)
	}
}

func TestAttendanceQueriesAreScopedToSelectedSession(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ATT-003",
		FullName:              "Multi Session Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2011-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Parent",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0775550001",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if err := app.createStudentGroup(StudentGroup{
		Name: "Multi Session Group",
		Code: "MULTI-ATT",
	}, []int64{admissionID}, nil, []StudentGroupSession{
		{Title: "Monday Session", DayOfWeek: "monday", StartTime: "09:00", EndTime: "10:00", Active: true},
		{Title: "Wednesday Session", DayOfWeek: "wednesday", StartTime: "09:00", EndTime: "10:00", Active: true},
	}); err != nil {
		t.Fatalf("create multi-session group: %v", err)
	}

	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list student groups: %v", err)
	}
	var group StudentGroup
	for _, item := range groups {
		if item.Code == "MULTI-ATT" {
			group = item
			break
		}
	}
	if group.ID == 0 || len(group.Sessions) != 2 {
		t.Fatalf("expected multi-session group, got %#v", group)
	}

	mondaySession := group.Sessions[0]
	wednesdaySession := group.Sessions[1]
	if mondaySession.DayOfWeek != "monday" {
		mondaySession, wednesdaySession = wednesdaySession, mondaySession
	}

	seedRecords := []AttendanceRecord{
		{GroupID: group.ID, SessionID: mondaySession.ID, SessionTitle: mondaySession.Title, AdmissionID: admissionID, AttendanceDate: "2026-08-03", Status: "present"},
		{GroupID: group.ID, SessionID: mondaySession.ID, SessionTitle: mondaySession.Title, AdmissionID: admissionID, AttendanceDate: "2026-08-10", Status: "late"},
		{GroupID: group.ID, SessionID: wednesdaySession.ID, SessionTitle: wednesdaySession.Title, AdmissionID: admissionID, AttendanceDate: "2026-08-05", Status: "absent"},
		{GroupID: group.ID, SessionID: wednesdaySession.ID, SessionTitle: wednesdaySession.Title, AdmissionID: admissionID, AttendanceDate: "2026-08-12", Status: "excused"},
	}
	for i, record := range seedRecords {
		if _, err := app.db.Exec(`
			INSERT INTO attendance_records (
				group_id, session_id, admission_id, attendance_date, status, note, recorded_at, updated_at
			)
			VALUES (?, ?, ?, ?, ?, '', ?, ?)
		`, record.GroupID, record.SessionID, record.AdmissionID, record.AttendanceDate, record.Status, time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatalf("seed attendance %d: %v", i, err)
		}
	}

	recentDates, err := app.listRecentAttendanceDates(group.ID, mondaySession.ID, 8)
	if err != nil {
		t.Fatalf("list recent attendance dates: %v", err)
	}
	if len(recentDates) != 2 || recentDates[0] != "2026-08-10" || recentDates[1] != "2026-08-03" {
		t.Fatalf("unexpected session-scoped recent dates: %#v", recentDates)
	}

	summary, err := app.getAttendanceSummary(group.ID, mondaySession.ID)
	if err != nil {
		t.Fatalf("get attendance summary: %v", err)
	}
	if summary.SessionCount != 2 || summary.PresentCount != 1 || summary.LateCount != 1 || summary.AbsentCount != 0 || summary.ExcusedCount != 0 || summary.TotalEntries != 2 {
		t.Fatalf("unexpected session-scoped attendance summary: %#v", summary)
	}
}

func TestSaveAttendanceHandlerRejectsMismatchedSessionDate(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ATT-002",
		FullName:              "Session Match Student",
		AdmissionDate:         "2026-07-01",
		DateOfBirth:           "2011-01-01",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Parent",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0775550000",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if err := app.createStudentGroup(StudentGroup{
		Name: "Mismatch Group",
		Code: "MIS-DATE",
	}, []int64{admissionID}, nil, []StudentGroupSession{{Title: "Monday Session", DayOfWeek: "monday", StartTime: "09:00", EndTime: "10:00", Active: true}}); err != nil {
		t.Fatalf("create mismatch group: %v", err)
	}
	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list student groups: %v", err)
	}
	var group StudentGroup
	for _, item := range groups {
		if item.Code == "MIS-DATE" {
			group = item
			break
		}
	}
	if group.ID == 0 || len(group.Sessions) == 0 {
		t.Fatalf("expected mismatch group session, got %#v", group)
	}
	form := url.Values{
		"csrf_token":                          {"token"},
		"group_id":                            {strconv.FormatInt(group.ID, 10)},
		"session_id":                          {strconv.FormatInt(group.Sessions[0].ID, 10)},
		"attendance_date":                     {"2026-08-11"},
		fmt.Sprintf("status_%d", admissionID): {"present"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/attendance/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.saveAttendanceHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect for mismatched session date, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/admin/attendance?date=2026-08-11&group_id="+strconv.FormatInt(group.ID, 10)+"&session_id="+strconv.FormatInt(group.Sessions[0].ID, 10) {
		t.Fatalf("attendance redirect = %q", got)
	}
	flashFound := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			flashFound = true
			break
		}
	}
	if !flashFound {
		t.Fatal("expected flash cookie for mismatched attendance date")
	}
}

func TestCollectEnrollmentAdmissionPaymentHandlerRedirectsOnMissingEnrollment(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	form := url.Values{
		"csrf_token":    {"token"},
		"enrollment_id": {"999999"},
		"division":      {"kec"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/collect-admission", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.collectEnrollmentAdmissionPaymentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("collect enrollment admission status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/admin/enrollments?division=kec" {
		t.Fatalf("collect enrollment admission redirect = %q", got)
	}
	flashFound := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			flashFound = true
			break
		}
	}
	if !flashFound {
		t.Fatal("expected flash cookie for missing enrollment")
	}
}

func TestWithQueryHelpersPreserveExistingParameters(t *testing.T) {
	if got := withDivisionQuery("/admin/student-payments", "kec"); got != "/admin/student-payments?division=kec" {
		t.Fatalf("withDivisionQuery simple = %q", got)
	}
	if got := withMonthQuery("/admin/student-payments", "2026-08"); got != "/admin/student-payments?month=2026-08" {
		t.Fatalf("withMonthQuery simple = %q", got)
	}
	if got := withMonthQuery(withDivisionQuery("/admin/student-payments", "kec-north"), "2026-08"); got != "/admin/student-payments?division=kec-north&month=2026-08" {
		t.Fatalf("combined division/month = %q", got)
	}
	if got := withDivisionQuery("/admin/student-payments?month=2026-08&action=view", "chess"); got != "/admin/student-payments?action=view&division=chess&month=2026-08" {
		t.Fatalf("withDivisionQuery existing params = %q", got)
	}
	if got := withMonthQuery("/admin/student-payments?division=sports&action=view", "2026-07"); got != "/admin/student-payments?action=view&division=sports&month=2026-07" {
		t.Fatalf("withMonthQuery existing params = %q", got)
	}
}

func TestCollectStudentPaymentHandlerPreservesDivisionAndMonthOnRedirect(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	form := url.Values{
		"csrf_token":     {"token"},
		"enrollment_id":  {"123"},
		"payment_month":  {"2026-12"},
		"payment_method": {"cash"},
		"amount":         {"2500"},
		"division":       {"kec"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/student-payments/collect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	rec := httptest.NewRecorder()

	app.collectStudentPaymentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("student payment redirect status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/admin/student-payments?division=kec&month=2026-12" {
		t.Fatalf("student payment redirect = %q", got)
	}
}

func TestSaveAttendanceHandlerPreservesDivisionOnValidationRedirect(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division: %v", err)
	}
	programID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Sports Attendance Redirect",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create programme: %v", err)
	}
	if err := app.createStudentGroup(
		StudentGroup{Name: "Sports Group", Code: "SG-RED", TrainingProgramID: programID},
		nil,
		nil,
		[]StudentGroupSession{{Title: "Monday Session", DayOfWeek: "monday", StartTime: "09:00", EndTime: "10:00", Active: true}},
	); err != nil {
		t.Fatalf("create group: %v", err)
	}
	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	var group StudentGroup
	for _, item := range groups {
		if item.Code == "SG-RED" {
			group = item
			break
		}
	}
	if len(group.Sessions) == 0 {
		t.Fatal("expected seeded group session")
	}

	form := url.Values{
		"csrf_token":      {"token"},
		"group_id":        {strconv.FormatInt(group.ID, 10)},
		"session_id":      {strconv.FormatInt(group.Sessions[0].ID, 10)},
		"attendance_date": {"2026-08-11"},
		"division":        {"sports"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/attendance/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.saveAttendanceHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attendance redirect status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	expected := "/admin/attendance?date=2026-08-11&division=sports&group_id=" + strconv.FormatInt(group.ID, 10) + "&session_id=" + strconv.FormatInt(group.Sessions[0].ID, 10)
	if got := rec.Header().Get("Location"); got != expected {
		t.Fatalf("attendance redirect = %q, want %q", got, expected)
	}
}

func TestBuildDashboardStatsRespectsDivisionScopeForSharedStudent(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		t.Fatalf("find sports division: %v", err)
	}
	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find KEC division: %v", err)
	}
	sportsDivision, err := app.findDivisionByID(sportsID)
	if err != nil {
		t.Fatalf("load sports division: %v", err)
	}
	kecDivision, err := app.findDivisionByID(kecID)
	if err != nil {
		t.Fatalf("load kec division: %v", err)
	}
	sportsProgramID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     sportsID,
		Name:           "Sports KPI Programme",
		Activity:       "full_indoor_cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2200,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create sports programme: %v", err)
	}
	kecProgramID, err := app.createTrainingProgram(TrainingProgram{
		GameID:         1,
		DivisionID:     kecID,
		Name:           "KEC KPI Class",
		Activity:       "badminton",
		TrainingFormat: "group",
		AdmissionFee:   1200,
		MonthlyFee:     1800,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create kec programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-KPI-001",
		FullName:              "Shared KPI Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-02-01",
		Gender:                "female",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Parent",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771234567",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{AdmissionID: admissionID, TrainingProgramID: sportsProgramID}, false, "cash", 0); err != nil {
		t.Fatalf("create sports enrollment: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{AdmissionID: admissionID, TrainingProgramID: kecProgramID}, false, "cash", 0); err != nil {
		t.Fatalf("create kec enrollment: %v", err)
	}

	user := &User{ID: 500, Name: "Superadmin", Roles: []string{"superadmin"}, Permissions: []string{"dashboard.view"}, Verified: true}
	allStats := app.buildDashboardStats(user, nil, nil, time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local))
	if got := statValue(allStats, "Active students"); got != "1" {
		t.Fatalf("global active students = %q, want %q", got, "1")
	}
	if got := statValue(allStats, "Active enrollments"); got != "2" {
		t.Fatalf("global active enrollments = %q, want %q", got, "2")
	}

	sportsStats := app.buildDashboardStats(user, sportsDivision, []int64{sportsID}, time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local))
	if got := statValue(sportsStats, "Active students"); got != "1" {
		t.Fatalf("sports active students = %q, want %q", got, "1")
	}
	if got := statValue(sportsStats, "Active enrollments"); got != "1" {
		t.Fatalf("sports active enrollments = %q, want %q", got, "1")
	}
	if !hasStatLabel(sportsStats, "Pending bookings") {
		t.Fatalf("expected sports booking metric in %#v", sportsStats)
	}

	kecStats := app.buildDashboardStats(user, kecDivision, []int64{kecID}, time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local))
	if got := statValue(kecStats, "Active students"); got != "1" {
		t.Fatalf("kec active students = %q, want %q", got, "1")
	}
	if got := statValue(kecStats, "Active enrollments"); got != "1" {
		t.Fatalf("kec active enrollments = %q, want %q", got, "1")
	}
	if hasStatLabel(kecStats, "Pending bookings") {
		t.Fatalf("did not expect booking metric in KEC stats: %#v", kecStats)
	}
	if got := statValue(kecStats, "Classes"); got != "1" {
		t.Fatalf("kec classes stat = %q, want %q", got, "1")
	}
}

func TestReportsTemplatePreservesDivisionScopeLinks(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	body := renderTemplateToString(t, templates, "reports", TemplateData{
		User:                  &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"superadmin"}, Permissions: []string{"reports.view"}},
		Title:                 "Reports",
		SelectedDivision:      &Division{ID: 2, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center"},
		SelectedDivisionScope: "kec",
		Report: &OperationalReport{
			Period: ReportPeriod{
				Kind:         "day",
				Anchor:       "2026-08-15",
				Label:        "Saturday, August 15, 2026",
				PreviousDate: "2026-08-14",
				NextDate:     "2026-08-16",
			},
		},
	})
	if !strings.Contains(body, "/admin/reports/export?date=2026-08-15&amp;division=kec&amp;period=day") {
		t.Fatalf("expected scoped export link in %s", body)
	}
	if !strings.Contains(body, "/admin/reports?date=2026-08-14&amp;division=kec&amp;period=day") {
		t.Fatalf("expected scoped previous-period link in %s", body)
	}
}

func TestFinanceOperationIdempotencyPreventsDuplicatePosts(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	recordedAt := time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local)
	firstID, err := app.createManualFinanceTransactionForAccount("manual_income", "Walk-in", "Same request", "same token path", cashID, 1200, recordedAt, 5)
	if err != nil {
		t.Fatalf("create manual finance entry: %v", err)
	}
	secondID, err := app.createManualFinanceTransactionForAccount("manual_income", "Walk-in", "Same request", "same token path", cashID, 1200, recordedAt, 5)
	if err != nil {
		t.Fatalf("repeat manual finance entry: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("manual transaction ids differ: first=%d second=%d", firstID, secondID)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE description = ?`, "Same request").Scan(&count); err != nil {
		t.Fatalf("count manual transactions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one manual transaction row, got %d", count)
	}
}

func TestFinanceDateValidationRejectsFutureDates(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	tomorrow := time.Now().Add(24 * time.Hour)
	if _, err := app.createManualFinanceTransactionForAccount("manual_income", "Future", "Future entry", "", cashID, 100, tomorrow, 0); err == nil {
		t.Fatal("expected future manual entry date to be rejected")
	}
	if _, err := app.createFinanceAdjustment(cashID, 50, tomorrow, "future correction", 0); err == nil {
		t.Fatal("expected future adjustment date to be rejected")
	}
}

func TestFinanceCashbookMigrationSkipsLegacyDuplicateSourceUniqueIndex(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-legacy-duplicates?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`,
		`CREATE TABLE admissions (id INTEGER PRIMARY KEY AUTOINCREMENT, full_name TEXT NOT NULL, payment_collected INTEGER NOT NULL DEFAULT 0, payment_collected_at DATETIME, admission_payment_amount REAL NOT NULL DEFAULT 0, finance_transaction_id INTEGER, updated_at DATETIME NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE student_monthly_payments (id INTEGER PRIMARY KEY AUTOINCREMENT, admission_id INTEGER NOT NULL, payment_month TEXT NOT NULL, amount REAL NOT NULL DEFAULT 0, payment_method TEXT NOT NULL DEFAULT 'cash', finance_transaction_id INTEGER NOT NULL, collected_by_user_id INTEGER, collected_at DATETIME NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE booking_referrals (id INTEGER PRIMARY KEY AUTOINCREMENT, finance_transaction_id INTEGER, paid INTEGER NOT NULL DEFAULT 0, paid_at DATETIME, payment_method TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL)`,
		`CREATE TABLE booking_payment_collections (id INTEGER PRIMARY KEY AUTOINCREMENT, finance_transaction_id INTEGER NOT NULL UNIQUE, voided INTEGER NOT NULL DEFAULT 0, void_reason TEXT NOT NULL DEFAULT '', voided_by_user_id INTEGER, voided_at DATETIME)`,
		`CREATE TABLE finance_transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_number TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL,
			reference_type TEXT NOT NULL,
			reference_id INTEGER,
			person_name TEXT NOT NULL,
			description TEXT NOT NULL,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			amount REAL NOT NULL DEFAULT 0,
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO finance_transactions (receipt_number, category, reference_type, reference_id, person_name, description, payment_method, amount, recorded_by_user_id, recorded_at, created_at)
		VALUES
			('ADM-LEGACY-1', 'admission_payment', 'admission', 1, 'Student One', 'Legacy admission payment A', 'cash', 1500, NULL, ?, ?),
			('ADM-LEGACY-2', 'admission_payment', 'admission', 1, 'Student One', 'Legacy admission payment B', 'cash', 1500, NULL, ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatalf("seed duplicate legacy finance rows: %v", err)
	}
	if err := migrateFinanceCashbook(db); err != nil {
		t.Fatalf("migrate finance cashbook with duplicate legacy rows: %v", err)
	}
	duplicateCount, err := financeSourceDuplicateCount(db, "admission")
	if err != nil {
		t.Fatalf("count duplicate admission source links: %v", err)
	}
	if duplicateCount != 1 {
		t.Fatalf("duplicate admission source link groups = %d, want 1", duplicateCount)
	}
	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_finance_transactions_source_admission'`).Scan(&indexCount); err != nil {
		t.Fatalf("lookup skipped unique index: %v", err)
	}
	if indexCount != 0 {
		t.Fatal("admission unique index should be skipped when legacy duplicates exist")
	}
}

func TestFinanceCashbookMigrationUpgradesLegacyCashReconciliationsBeforePartialIndex(t *testing.T) {
	db, err := sql.Open("sqlite", "file:finance-legacy-cash-reconciliation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	now := time.Now().UTC()
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`,
		`CREATE TABLE finance_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			account_type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			opening_balance REAL NOT NULL DEFAULT 0,
			is_system INTEGER NOT NULL DEFAULT 0,
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			created_by_user_id INTEGER,
			updated_by_user_id INTEGER
		)`,
		`CREATE TABLE finance_transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_number TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL,
			reference_type TEXT NOT NULL,
			reference_id INTEGER,
			person_name TEXT NOT NULL,
			description TEXT NOT NULL,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			amount REAL NOT NULL DEFAULT 0,
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE student_monthly_payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			payment_month TEXT NOT NULL,
			amount REAL NOT NULL DEFAULT 0,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			finance_transaction_id INTEGER NOT NULL,
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE admissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			full_name TEXT NOT NULL,
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			updated_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE booking_referrals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			finance_transaction_id INTEGER,
			paid INTEGER NOT NULL DEFAULT 0,
			paid_at DATETIME,
			payment_method TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE booking_payment_collections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			finance_transaction_id INTEGER NOT NULL UNIQUE,
			voided INTEGER NOT NULL DEFAULT 0,
			void_reason TEXT NOT NULL DEFAULT '',
			voided_by_user_id INTEGER,
			voided_at DATETIME
		)`,
		`CREATE TABLE cash_reconciliations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			finance_account_id INTEGER NOT NULL,
			reconciliation_date TEXT NOT NULL,
			expected_balance REAL NOT NULL DEFAULT 0,
			counted_balance REAL NOT NULL DEFAULT 0,
			difference REAL NOT NULL DEFAULT 0,
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'balanced',
			reconciled_by_user_id INTEGER,
			created_at DATETIME NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_cash_reconciliations_account_date ON cash_reconciliations(finance_account_id, reconciliation_date)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy reconciliation schema: %v", err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO finance_accounts (name, account_type, description, opening_balance, is_system, is_active, created_at, updated_at)
		VALUES ('Legacy Cash', 'cash', 'Legacy account', 0, 0, 1, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed finance account: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := migrateFinanceCashbook(db); err != nil {
			t.Fatalf("migrate finance cashbook run %d: %v", i+1, err)
		}
	}

	for _, column := range []string{"void_reason", "voided_by_user_id", "voided_at", "superseded_by_reconciliation_id"} {
		exists, err := tableHasColumn(db, "cash_reconciliations", column)
		if err != nil {
			t.Fatalf("check migrated column %s: %v", column, err)
		}
		if !exists {
			t.Fatalf("expected migrated cash_reconciliations column %s", column)
		}
	}

	var oldIndexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_cash_reconciliations_account_date'`).Scan(&oldIndexCount); err != nil {
		t.Fatalf("lookup old reconciliation index: %v", err)
	}
	if oldIndexCount != 0 {
		t.Fatal("expected legacy idx_cash_reconciliations_account_date index to be removed")
	}

	var newIndexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_cash_reconciliations_account_date_active'`).Scan(&newIndexCount); err != nil {
		t.Fatalf("lookup new reconciliation index: %v", err)
	}
	if newIndexCount != 1 {
		t.Fatal("expected idx_cash_reconciliations_account_date_active index to exist")
	}
}

func TestListFinanceTransfersQualifiesAmbiguousColumns(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)
	if _, err := app.createManualFinanceTransactionForAccount("manual_income", "Seed Cash", "Initial float", "", cashID, 2000, time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local), 0); err != nil {
		t.Fatalf("seed cash balance: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO cash_reconciliations (
			finance_account_id, reconciliation_date, expected_balance, counted_balance, difference,
			notes, status, reconciled_by_user_id, created_at
		) VALUES (?, '2026-08-01', 0, 0, 0, 'legacy note', 'balanced', NULL, ?)
	`, cashID, time.Now().UTC()); err != nil {
		t.Fatalf("seed cash reconciliation: %v", err)
	}
	groupID, err := app.createFinanceTransfer(
		cashID,
		bankID,
		1500,
		time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local),
		"TRF-REG-001",
		"Cash deposit for ambiguity test",
		"banking run",
		0,
	)
	if err != nil {
		t.Fatalf("create finance transfer: %v", err)
	}
	transfers, err := app.listFinanceTransfers()
	if err != nil {
		t.Fatalf("list finance transfers: %v", err)
	}
	if len(transfers) != 1 {
		t.Fatalf("transfer count = %d, want 1", len(transfers))
	}
	transfer := transfers[0]
	if transfer.GroupID != groupID {
		t.Fatalf("transfer group id = %q, want %q", transfer.GroupID, groupID)
	}
	if transfer.Description != "Cash deposit for ambiguity test" {
		t.Fatalf("transfer description = %q", transfer.Description)
	}
	if transfer.FromAccountName != financeAccountCashInHand || transfer.ToAccountName != financeAccountMainBank {
		t.Fatalf("unexpected transfer accounts: %#v", transfer)
	}
}

func TestBookingCancellationReleasesCapacityAndPreservesPayment(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-cancellation-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{db: db}

	scheduleID := createConfirmedFutureBooking(t, app, 5, "18:00")
	if _, err := app.collectBookingPayment(scheduleID, "cash", 2500, "", 42, false); err != nil {
		t.Fatalf("collect booking payment: %v", err)
	}
	updated, _, err := app.transitionManagedBookingStatus(scheduleID, bookingStatusCancelled, "Customer cannot attend", "", "Customer cannot attend", "cash retained", "admin", 0)
	if err != nil {
		t.Fatalf("cancel booking: %v", err)
	}
	if updated.Status != bookingStatusCancelled {
		t.Fatalf("unexpected status after cancellation: %s", updated.Status)
	}

	candidate := SpaceSchedule{
		SlotDate:  updated.SlotDate,
		SlotHour:  updated.SlotHour,
		EntryType: "booking",
		Activity:  updated.Activity,
		Quantity:  updated.Quantity,
		Status:    bookingStatusPending,
	}
	layouts, err := app.listActiveCourtLayouts()
	if err != nil {
		t.Fatalf("list court layouts: %v", err)
	}
	existing, err := app.schedulesForSlot(updated.SlotDate, updated.SlotHour, 0)
	if err != nil {
		t.Fatalf("load schedules for slot: %v", err)
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, candidate, layouts); err != nil {
		t.Fatalf("cancelled booking should release capacity: %v", err)
	}

	var paid int
	var paymentMethod string
	var transactionID int64
	if err := db.QueryRow(`SELECT paid, payment_method, COALESCE(finance_transaction_id, 0) FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&paid, &paymentMethod, &transactionID); err != nil {
		t.Fatalf("load booking financial after cancellation: %v", err)
	}
	if paid != 1 || paymentMethod != "cash" || transactionID == 0 {
		t.Fatalf("payment was not preserved after cancellation: paid=%d method=%q transaction=%d", paid, paymentMethod, transactionID)
	}
	var categoryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE reference_id = ? AND category = 'booking_payment'`, scheduleID).Scan(&categoryCount); err != nil {
		t.Fatal(err)
	}
	if categoryCount != 1 {
		t.Fatalf("unexpected booking payment transaction count after cancellation: %d", categoryCount)
	}
	if _, _, err := app.transitionManagedBookingStatus(scheduleID, bookingStatusCancelled, "Duplicate", "", "Duplicate", "", "admin", 0); err == nil {
		t.Fatal("expected duplicate cancellation to be rejected")
	}
}

func TestHeldBookingReservesCapacityAndReleasesOnFinalStates(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	layouts, err := app.listActiveCourtLayouts()
	if err != nil {
		t.Fatalf("list active court layouts: %v", err)
	}

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "Held Request",
		RequesterName:  "Held Customer",
		RequesterEmail: "held@example.com",
		RequesterPhone: "0700000000",
		QuotedPrice:    2500,
	}
	scheduleID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create booking request: %v", err)
	}
	if _, _, err := app.transitionBookingRequestStatus(scheduleID, bookingStatusHeld, "Reviewing slot", "We are reviewing your request.", "admin", 0); err != nil {
		t.Fatalf("hold booking request: %v", err)
	}

	conflict := request
	conflict.Status = bookingStatusPending
	existing, err := app.schedulesForSlot(request.SlotDate, request.SlotHour, 0)
	if err != nil {
		t.Fatalf("load schedules for held slot: %v", err)
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, conflict, layouts); err == nil {
		t.Fatal("expected held booking to reserve capacity")
	}

	for _, finalStatus := range []string{bookingStatusRejected, bookingStatusCancelled, bookingStatusExpired} {
		currentApp := newBookingWorkflowTestApp(t)
		currentLayouts, err := currentApp.listActiveCourtLayouts()
		if err != nil {
			t.Fatalf("list active court layouts for %s: %v", finalStatus, err)
		}
		currentID, err := currentApp.createPublicBookingRequest(request)
		if err != nil {
			t.Fatalf("create booking request for %s: %v", finalStatus, err)
		}
		if _, _, err := currentApp.transitionBookingRequestStatus(currentID, bookingStatusHeld, "Reviewing slot", "We are reviewing your request.", "admin", 0); err != nil {
			t.Fatalf("hold booking request for %s: %v", finalStatus, err)
		}
		if finalStatus == bookingStatusCancelled {
			if _, _, err := currentApp.transitionManagedBookingStatus(currentID, bookingStatusCancelled, "Cancelled by staff", "Cancelled", "Cancelled by staff", "", "admin", 0); err != nil {
				t.Fatalf("cancel held booking: %v", err)
			}
		} else {
			if _, _, err := currentApp.transitionBookingRequestStatus(currentID, finalStatus, "Closed", "Closed", "admin", 0); err != nil {
				t.Fatalf("transition held booking to %s: %v", finalStatus, err)
			}
		}

		replacement := request
		replacement.Status = bookingStatusPending
		releasedSchedules, err := currentApp.schedulesForSlot(request.SlotDate, request.SlotHour, 0)
		if err != nil {
			t.Fatalf("load schedules after %s: %v", finalStatus, err)
		}
		if err := validateSpaceScheduleSlotAgainstLayouts(releasedSchedules, replacement, currentLayouts); err != nil {
			t.Fatalf("%s should release capacity: %v", finalStatus, err)
		}
	}
}

func TestPublicBookingRequestStaysPendingEvenWhenCourtActivityAutoAcceptIsEnabled(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if _, err := app.db.Exec(`UPDATE court_activities SET auto_accept = 1 WHERE activity = 'badminton'`); err != nil {
		t.Fatalf("enable auto accept: %v", err)
	}

	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {time.Now().AddDate(0, 0, 4).Format("2006-01-02")},
		"slot_hour":       {"18:00"},
		"activity":        {"badminton"},
		"quantity":        {"1"},
		"title":           {"Pending Review Booking"},
		"requester_name":  {"Auto Customer"},
		"requester_email": {"auto@example.com"},
		"requester_phone": {"+94770000000"},
	}
	req := httptest.NewRequest(http.MethodPost, "/book/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()
	app.publicBookingRequestHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}

	var scheduleID int64
	var status string
	if err := app.db.QueryRow(`SELECT id, status FROM space_schedules WHERE title = 'Pending Review Booking'`).Scan(&scheduleID, &status); err != nil {
		t.Fatalf("load pending review booking: %v", err)
	}
	if status != bookingStatusPending {
		t.Fatalf("expected pending status, got %s", status)
	}

	rows, err := app.db.Query(`SELECT DISTINCT event_type FROM booking_communications WHERE schedule_id = ?`, scheduleID)
	if err != nil {
		t.Fatalf("list communication events: %v", err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		events = append(events, eventType)
	}
	if len(events) != 1 || events[0] != bookingCommEventRequestReceived {
		t.Fatalf("unexpected booking-request communication events: %v", events)
	}
}

func TestBookingReminderBoundariesAndMidnight(t *testing.T) {
	now := time.Date(2026, time.August, 3, 22, 30, 0, 0, time.Local)
	requests := []SpaceSchedule{
		{ID: 1, SlotDate: "2026-08-04", SlotHour: "00:30", EntryType: "booking", Activity: "badminton", Quantity: 1, Status: bookingStatusPending},
		{ID: 2, SlotDate: "2026-08-03", SlotHour: "23:30", EntryType: "booking", Activity: "badminton", Quantity: 1, Status: bookingStatusHeld},
		{ID: 3, SlotDate: "2026-08-03", SlotHour: "23:00", EntryType: "booking", Activity: "badminton", Quantity: 1, Status: bookingStatusReschedulePending},
	}
	reminders := buildBookingReminders(requests, now)
	if len(reminders) != 3 {
		t.Fatalf("expected 3 reminders, got %d", len(reminders))
	}

	labelsByID := map[int64]string{}
	for _, reminder := range reminders {
		labelsByID[reminder.Schedule.ID] = reminder.UrgencyLabel
	}
	if labelsByID[1] != "Attention" {
		t.Fatalf("expected exactly 120 minutes to be Attention, got %q", labelsByID[1])
	}
	if labelsByID[2] != "Urgent" {
		t.Fatalf("expected exactly 60 minutes to be Urgent, got %q", labelsByID[2])
	}
	if labelsByID[3] != "Urgent" {
		t.Fatalf("expected 30 minutes remaining to be Urgent, got %q", labelsByID[3])
	}

	ninety := buildBookingReminders([]SpaceSchedule{{ID: 4, SlotDate: "2026-08-04", SlotHour: "00:00", EntryType: "booking", Activity: "badminton", Quantity: 1, Status: bookingStatusPending}}, now)
	if len(ninety) != 1 || ninety[0].UrgencyLabel != "Important" {
		t.Fatalf("expected exactly 90 minutes to be Important, got %#v", ninety)
	}
	sixty := buildBookingReminders([]SpaceSchedule{{ID: 5, SlotDate: "2026-08-03", SlotHour: "23:30", EntryType: "booking", Activity: "badminton", Quantity: 1, Status: bookingStatusPending}}, time.Date(2026, time.August, 3, 22, 30, 0, 0, time.Local))
	if len(sixty) != 1 || sixty[0].UrgencyLabel != "Urgent" {
		t.Fatalf("expected exactly 60 minutes to be Urgent, got %#v", sixty)
	}
}

func TestBookingAttentionCountersIgnoreNonBookingSchedules(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	now := time.Now().UTC()
	rows := []struct {
		slotHour  string
		entryType string
		status    string
		title     string
	}{
		{slotHour: "18:00", entryType: "booking", status: bookingStatusPending, title: "Pending Booking"},
		{slotHour: "19:00", entryType: "booking", status: bookingStatusHeld, title: "Held Booking"},
		{slotHour: "20:00", entryType: "booking", status: bookingStatusReschedulePending, title: "Reschedule Booking"},
		{slotHour: "18:30", entryType: "event", status: bookingStatusPending, title: "Pending Event"},
		{slotHour: "19:30", entryType: "training", status: bookingStatusHeld, title: "Held Training"},
		{slotHour: "20:30", entryType: "maintenance", status: bookingStatusReschedulePending, title: "Reschedule Maintenance"},
	}
	for _, row := range rows {
		if _, err := app.db.Exec(`
			INSERT INTO space_schedules (
				slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
				requester_name, requester_email, requester_phone, created_at, updated_at
			) VALUES (?, ?, ?, 'badminton', 1, ?, '', ?, 'Requester', 'requester@example.com', '0700000000', ?, ?)
		`, "2026-08-05", row.slotHour, row.entryType, row.title, row.status, now, now); err != nil {
			t.Fatalf("insert %s %s schedule: %v", row.entryType, row.status, err)
		}
	}

	pendingCount, err := app.countPendingSpaceSchedules()
	if err != nil {
		t.Fatalf("count pending booking schedules: %v", err)
	}
	if pendingCount != 1 {
		t.Fatalf("pending booking count = %d, want 1", pendingCount)
	}

	heldCount, err := app.countHeldSpaceSchedules()
	if err != nil {
		t.Fatalf("count held booking schedules: %v", err)
	}
	if heldCount != 1 {
		t.Fatalf("held booking count = %d, want 1", heldCount)
	}

	reschedulePendingCount, err := app.countReschedulePendingSpaceSchedules()
	if err != nil {
		t.Fatalf("count reschedule pending booking schedules: %v", err)
	}
	if reschedulePendingCount != 1 {
		t.Fatalf("reschedule pending booking count = %d, want 1", reschedulePendingCount)
	}
}

func TestValidateRuntimeConfigurationProductionRejectsDevelopmentSecret(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvProduction, CookieSecure: true},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "https://mekmaa.com", TokenSecret: defaultBookingAccessTokenSecret},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "development default") {
		t.Fatalf("expected development secret validation error, got %v", errs)
	}
}

func TestValidateRuntimeConfigurationProductionRejectsMissingSecret(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvProduction, CookieSecure: true},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "https://mekmaa.com", TokenSecret: ""},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "BOOKING_ACCESS_TOKEN_SECRET") {
		t.Fatalf("expected missing secret validation error, got %v", errs)
	}
}

func TestValidateRuntimeConfigurationProductionRejectsLocalhostPublicURL(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvProduction, CookieSecure: true},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "https://localhost:8080", TokenSecret: strings.Repeat("x", 32)},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "localhost") {
		t.Fatalf("expected localhost URL validation error, got %v", errs)
	}
}

func TestValidateRuntimeConfigurationProductionRejectsHTTPPublicURL(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvProduction, CookieSecure: true},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "http://mekmaa.com", TokenSecret: strings.Repeat("x", 32)},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "HTTPS") {
		t.Fatalf("expected HTTPS URL validation error, got %v", errs)
	}
}

func TestValidateRuntimeConfigurationProductionRejectsInsecureCookies(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvProduction, CookieSecure: false},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "https://mekmaa.com", TokenSecret: strings.Repeat("x", 32)},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "COOKIE_SECURE") {
		t.Fatalf("expected cookie security validation error, got %v", errs)
	}
}

func TestValidateRuntimeConfigurationDevelopmentPermitsLocalhostAndInsecureCookies(t *testing.T) {
	errs := validateRuntimeConfiguration(
		AppRuntimeConfig{Env: appEnvDevelopment, CookieSecure: false},
		BookingCommunicationSettings{},
		BookingAccessSettings{BaseURL: "http://localhost:8080", TokenSecret: defaultBookingAccessTokenSecret},
		SMTPConfig{},
		SMSConfig{},
	)
	if len(errs) != 0 {
		t.Fatalf("expected development configuration to pass, got %v", errs)
	}
}

func TestValidateSMTPConfigurationDisabledEmailDoesNotRequireCredentials(t *testing.T) {
	errs := validateSMTPConfiguration(BookingCommunicationSettings{EmailEnabled: false}, SMTPConfig{})
	if len(errs) != 0 {
		t.Fatalf("expected disabled email to skip credential validation, got %v", errs)
	}
}

func TestValidateSMTPConfigurationEnabledEmailRequiresCredentials(t *testing.T) {
	errs := validateSMTPConfiguration(BookingCommunicationSettings{EmailEnabled: true}, SMTPConfig{})
	if len(errs) == 0 {
		t.Fatal("expected enabled email validation errors")
	}
}

func TestValidateSMSConfigurationDisabledSMSDoesNotRequireCredentials(t *testing.T) {
	errs := validateSMSConfiguration(BookingCommunicationSettings{SMSEnabled: false}, SMSConfig{})
	if len(errs) != 0 {
		t.Fatalf("expected disabled SMS to skip credential validation, got %v", errs)
	}
}

func TestValidateSMSConfigurationEnabledSMSRequiresCredentials(t *testing.T) {
	errs := validateSMSConfiguration(BookingCommunicationSettings{SMSEnabled: true}, SMSConfig{})
	if len(errs) == 0 {
		t.Fatal("expected enabled SMS validation errors")
	}
}

func TestReadyHandlerSucceedsWithValidDatabaseAndUploadDirectory(t *testing.T) {
	app := newReadinessTestApp(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready body missing ready status: %s", rec.Body.String())
	}
}

func TestReadyHandlerProductionSucceedsWithFullyPricedActivities(t *testing.T) {
	app := newProductionReadinessTestApp(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"ready"`) {
		t.Fatalf("ready body missing ready status: %s", body)
	}
	if strings.Contains(body, `"warnings"`) {
		t.Fatalf("ready body unexpectedly included warnings: %s", body)
	}
}

func TestReadyHandlerProductionWarnsButStaysReadyWhenActivityIsUnpriced(t *testing.T) {
	app := newProductionReadinessTestApp(t)
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"ready"`) {
		t.Fatalf("ready body missing ready status: %s", body)
	}
	if !strings.Contains(body, `"warnings":["1 active booking options are missing complete public pricing"]`) {
		t.Fatalf("ready body missing safe pricing warning: %s", body)
	}
	if !strings.Contains(body, `"name":"booking_pricing","status":"ok"`) {
		t.Fatalf("booking pricing check should stay operationally ok: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "badminton") || strings.Contains(body, `"id"`) || strings.Contains(body, app.bookingAccess.TokenSecret) {
		t.Fatalf("ready body exposed internal pricing details: %s", body)
	}
}

func TestReadyHandlerFailsWhenDatabaseIsUnavailable(t *testing.T) {
	app := newReadinessTestApp(t)
	if err := app.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rec.Body.String(), "sql:") {
		t.Fatalf("ready body exposed raw database error: %s", rec.Body.String())
	}
}

func TestReadyHandlerFailsWhenUploadDirectoryIsUnwritable(t *testing.T) {
	app := newReadinessTestApp(t)
	app.uploads.EventDir = filepath.Join(t.TempDir(), "missing-events")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestSetupWarningsIncludeUnpricedActiveActivity(t *testing.T) {
	app := newReadinessTestApp(t)
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	warnings := app.setupWarningsForUser(&User{Permissions: []string{"pricing.manage"}})
	if len(warnings) == 0 || !strings.Contains(warnings[0].Body, "Badminton") {
		t.Fatalf("expected badminton warning, got %#v", warnings)
	}
}

func TestSetupWarningsExcludeFullyPricedActivity(t *testing.T) {
	app := newReadinessTestApp(t)
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 2500, weekday_peak_price = 3000, weekend_offpeak_price = 2800, weekend_peak_price = 3200
	`); err != nil {
		t.Fatalf("price all activities: %v", err)
	}
	warnings := app.setupWarningsForUser(&User{Permissions: []string{"pricing.manage"}})
	if len(warnings) != 0 {
		t.Fatalf("expected no setup warnings once all pricing is configured, got %#v", warnings)
	}
}

func TestDashboardHandlerDisplaysPricingWarningToAuthorizedStaff(t *testing.T) {
	app := newReadinessTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	user := &User{
		ID:          1,
		Email:       "pricing@example.com",
		Name:        "Pricing Manager",
		Roles:       []string{"Staff"},
		Permissions: []string{"dashboard.view", "pricing.manage"},
		Verified:    true,
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.dashboardHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Booking pricing setup incomplete") || !strings.Contains(body, "pricing-unavailable message") {
		t.Fatalf("dashboard did not render pricing setup warning: %s", body)
	}
}

func TestPricingManagementHandlerDisplaysPricingWarningToAuthorizedStaff(t *testing.T) {
	app := newReadinessTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	user := &User{
		ID:          1,
		Email:       "pricing@example.com",
		Name:        "Pricing Manager",
		Roles:       []string{"Staff"},
		Permissions: []string{"pricing.manage"},
		Verified:    true,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/pricing", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.pricingManagementHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pricing management status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Booking pricing setup incomplete") || !strings.Contains(body, "pricing-unavailable message") {
		t.Fatalf("pricing management page did not render pricing setup warning: %s", body)
	}
}

func TestPublicBookingRejectsUnpricedActivityWithClearMessage(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	slotDate := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {slotDate},
		"slot_hour":       {"18:00"},
		"activity":        {"badminton"},
		"quantity":        {"1"},
		"title":           {"Unpriced Booking"},
		"requester_name":  {"Customer"},
		"requester_email": {"customer@example.com"},
		"requester_phone": {"0770000000"},
	}
	req := httptest.NewRequest(http.MethodPost, "/book/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()
	app.publicBookingRequestHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Pricing is currently unavailable for Badminton") {
		t.Fatalf("expected clear pricing message, got %s", body)
	}
}

func TestPublicBookingAcceptsPricedActivity(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	slotDate := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {slotDate},
		"slot_hour":       {"18:00"},
		"activity":        {"badminton"},
		"quantity":        {"1"},
		"title":           {"Priced Booking"},
		"requester_name":  {"Customer"},
		"requester_email": {"customer@example.com"},
		"requester_phone": {"0770000000"},
	}
	req := httptest.NewRequest(http.MethodPost, "/book/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()
	app.publicBookingRequestHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM space_schedules WHERE title = 'Priced Booking'`).Scan(&count); err != nil {
		t.Fatalf("count priced booking: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected priced booking to be created, got %d", count)
	}
}

func TestAdmissionAndEnrollmentManagementHandlersRender(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	admission := Admission{
		StudentID:                "STD-ENR-001",
		FullName:                 "Enrollment Student",
		AdmissionDate:            "2026-08-01",
		DateOfBirth:              "2012-04-10",
		Gender:                   "male",
		PracticeType:             "student",
		Address:                  "Jaffna",
		PassportNumber:           "TP001",
		School:                   "Test School",
		GuardianName:             "Guardian",
		GuardianRelationship:     "Parent",
		GuardianContactNumber:    "0770000000",
		GuardianAlternativePhone: "0770000001",
		MedicalInformation:       "None",
		QRCodeValue:              "STD-ENR-001",
		QRCodePath:               "/uploads/students/qr/std-enr-001.png",
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(admission, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	trainingProgramID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Handler Test Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
		SortOrder:      10,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: trainingProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create enrollment: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{name: "admissions", path: "/admin/admissions", handler: app.admissionManagementHandler},
		{name: "student-id", path: "/admin/admissions/student-id?id=" + strconv.FormatInt(admissionID, 10), handler: app.studentIDCardHandler},
		{name: "enrollments", path: "/admin/enrollments", handler: app.enrollmentManagementHandler},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			tt.handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAttendanceManagementHandlerLoadsSheet(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-ATT-LOAD-001",
		FullName:              "Attendance Load Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2012-03-11",
		Gender:                "male",
		PracticeType:          "group_practice",
		Address:               "Jaffna",
		GuardianName:          "Guardian",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771002000",
		QRCodeValue:           "STD-ATT-LOAD-001",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}

	if err := app.createStudentGroup(StudentGroup{
		Name: "Attendance Load Group",
		Code: "ATT-LOAD",
	}, []int64{admissionID}, nil, []StudentGroupSession{{
		Title:     "Wednesday Session",
		DayOfWeek: "wednesday",
		StartTime: "09:00",
		EndTime:   "10:00",
		Active:    true,
	}}); err != nil {
		t.Fatalf("create attendance group: %v", err)
	}

	groups, err := app.listStudentGroups()
	if err != nil {
		t.Fatalf("list student groups: %v", err)
	}
	var group StudentGroup
	for _, item := range groups {
		if item.Code == "ATT-LOAD" {
			group = item
			break
		}
	}
	if group.ID == 0 || len(group.Sessions) == 0 {
		t.Fatalf("expected attendance group with session, got %#v", group)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/attendance?group_id="+strconv.FormatInt(group.ID, 10)+"&session_id="+strconv.FormatInt(group.Sessions[0].ID, 10)+"&date=2026-08-12", nil)
	app.attendanceManagementHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attendance load status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionMiddlewareRefreshesActiveSession(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	user, err := app.createManagedUser("Session User", "session-user@example.com", "password-123", []string{"admin"}, true)
	if err != nil {
		t.Fatalf("create managed user: %v", err)
	}

	loginRec := httptest.NewRecorder()
	if err := app.createSession(loginRec, user.ID); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var sessionCookie *http.Cookie
	for _, cookie := range loginRec.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	nextCalled := false

	app.sessionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatalf("expected next handler to be called, got status %d body=%s", rec.Code, rec.Body.String())
	}

	refreshed := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value == sessionCookie.Value {
			refreshed = true
			break
		}
	}
	if !refreshed {
		t.Fatal("expected session cookie to be refreshed")
	}
}

func TestOneToOneBookingCreatesScheduleAndFinancial(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	offeringID, err := app.createOneToOneOffering(OneToOneOffering{
		Name:         "Private Badminton",
		Game:         "badminton",
		Audience:     "local",
		Occurrence:   "per_week",
		SessionCount: 6,
		Price:        3500,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("create 1 to 1 offering: %v", err)
	}
	offering, err := app.findOneToOneOfferingByID(offeringID)
	if err != nil {
		t.Fatalf("find 1 to 1 offering: %v", err)
	}

	slotDate := time.Now().AddDate(0, 0, 4).Format("2006-01-02")
	bookingID, scheduleID, err := app.createOneToOneBooking(*offering, "Test Customer", slotDate, "18:00", 4, 3000, 800, "High-priority session", "")
	if err != nil {
		t.Fatalf("create 1 to 1 booking: %v", err)
	}
	if bookingID <= 0 || scheduleID <= 0 {
		t.Fatalf("expected persisted ids, got booking=%d schedule=%d", bookingID, scheduleID)
	}

	var schedule SpaceSchedule
	if err := app.db.QueryRow(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status, requester_name
		FROM space_schedules
		WHERE id = ?
	`, scheduleID).Scan(
		&schedule.ID,
		&schedule.SlotDate,
		&schedule.SlotHour,
		&schedule.EntryType,
		&schedule.Activity,
		&schedule.Quantity,
		&schedule.Title,
		&schedule.Notes,
		&schedule.Status,
		&schedule.RequesterName,
	); err != nil {
		t.Fatalf("load created schedule: %v", err)
	}
	if schedule.EntryType != "booking" || schedule.Activity != "badminton" || schedule.Quantity != 1 {
		t.Fatalf("unexpected schedule core fields: %#v", schedule)
	}
	if schedule.RequesterName != "Test Customer" {
		t.Fatalf("unexpected requester name: %q", schedule.RequesterName)
	}
	if !strings.Contains(schedule.Title, "Private Badminton") || !strings.Contains(schedule.Notes, "High-priority session") {
		t.Fatalf("expected title/notes to include 1 to 1 details: title=%q notes=%q", schedule.Title, schedule.Notes)
	}

	var quotedAmount float64
	if err := app.db.QueryRow(`SELECT quoted_amount FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&quotedAmount); err != nil {
		t.Fatalf("load booking financial: %v", err)
	}
	if quotedAmount != 3000 {
		t.Fatalf("unexpected quoted amount: %v", quotedAmount)
	}
}

func TestOneToOneBookingCreatesReferralCommissionWhenReferrerSelected(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	if err := app.updateReferralCommissionAmount(600); err != nil {
		t.Fatalf("configure referral commission: %v", err)
	}
	if err := app.createReferralPartner(ReferralPartner{
		Name:   "1 to 1 Partner",
		Code:   "OTO-REF-01",
		Email:  "oto@example.com",
		Phone:  "0700000999",
		Active: true,
	}); err != nil {
		t.Fatalf("create referral partner: %v", err)
	}

	offeringID, err := app.createOneToOneOffering(OneToOneOffering{
		Name:         "Private Tennis",
		Game:         "tennis",
		Audience:     "local",
		Occurrence:   "per_week",
		SessionCount: 4,
		Price:        4000,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("create 1 to 1 offering: %v", err)
	}
	offering, err := app.findOneToOneOfferingByID(offeringID)
	if err != nil {
		t.Fatalf("find 1 to 1 offering: %v", err)
	}

	slotDate := time.Now().AddDate(0, 0, 4).Format("2006-01-02")
	_, scheduleID, err := app.createOneToOneBooking(*offering, "Referral Customer", slotDate, "18:15", 2, 3600, 700, "", "OTO-REF-01")
	if err != nil {
		t.Fatalf("create referred 1 to 1 booking: %v", err)
	}

	referrals, err := app.listBookingReferrals()
	if err != nil {
		t.Fatalf("list booking referrals: %v", err)
	}
	if len(referrals) != 1 {
		t.Fatalf("expected one booking referral, got %d", len(referrals))
	}
	if referrals[0].ScheduleID != scheduleID || referrals[0].PartnerCode != "OTO-REF-01" {
		t.Fatalf("unexpected 1 to 1 referral linkage: %#v", referrals[0])
	}
	if referrals[0].CommissionAmount != 600 {
		t.Fatalf("unexpected 1 to 1 referral commission: %#v", referrals[0])
	}
}

func TestOneToOneBookingRejectsConsumedCapacity(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	offeringID, err := app.createOneToOneOffering(OneToOneOffering{
		Name:         "Private Badminton",
		Game:         "badminton",
		Audience:     "foreign",
		Occurrence:   "per_month",
		SessionCount: 8,
		Price:        4500,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("create 1 to 1 offering: %v", err)
	}
	offering, err := app.findOneToOneOfferingByID(offeringID)
	if err != nil {
		t.Fatalf("find 1 to 1 offering: %v", err)
	}

	slotDate := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	createConfirmedBookingForTests(t, app, SpaceSchedule{
		SlotDate:      slotDate,
		SlotHour:      "18:00",
		EntryType:     "booking",
		Activity:      "badminton",
		Quantity:      1,
		Title:         "Existing Badminton Booking",
		RequesterName: "Existing Customer",
		QuotedPrice:   2500,
	})

	_, _, err = app.createOneToOneBooking(*offering, "Blocked Customer", slotDate, "18:00", 3, 4200, 900, "", "")
	if err == nil {
		t.Fatal("expected 1 to 1 booking conflict error")
	}
	if !strings.Contains(err.Error(), "remaining capacity") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}

func TestOneToOneBookingRejectsSessionsAboveConfiguredLimit(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	offeringID, err := app.createOneToOneOffering(OneToOneOffering{
		Name:         "Private Cricket",
		Game:         "cricket_net",
		Audience:     "local",
		Occurrence:   "per_week",
		SessionCount: 3,
		Price:        6000,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("create 1 to 1 offering: %v", err)
	}
	offering, err := app.findOneToOneOfferingByID(offeringID)
	if err != nil {
		t.Fatalf("find 1 to 1 offering: %v", err)
	}

	slotDate := time.Now().AddDate(0, 0, 6).Format("2006-01-02")
	_, _, err = app.createOneToOneBooking(*offering, "Blocked Customer", slotDate, "19:00", 4, 5500, 1200, "", "")
	if err == nil {
		t.Fatal("expected sessions limit error")
	}
	if !strings.Contains(err.Error(), "configured limit of 3") {
		t.Fatalf("unexpected sessions limit error: %v", err)
	}
}

func TestOneToOneManagementHandlersRender(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	if _, err := app.createOneToOneOffering(OneToOneOffering{
		Name:         "Private Badminton",
		Game:         "badminton",
		Audience:     "local",
		Occurrence:   "per_week",
		SessionCount: 4,
		Price:        3200,
		Active:       true,
	}); err != nil {
		t.Fatalf("create 1 to 1 offering: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
		mustSee string
	}{
		{name: "catalogue", path: "/admin/one-to-one", handler: app.oneToOneManagementHandler, mustSee: "1 to 1 setup"},
		{name: "bookings", path: "/admin/one-to-one-bookings", handler: app.oneToOneBookingManagementHandler, mustSee: "1 to 1 bookings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			tt.handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.mustSee) {
				t.Fatalf("expected %q in response body, got %s", tt.mustSee, rec.Body.String())
			}
		})
	}
}

func TestBookingHoursUseFifteenMinuteIncrements(t *testing.T) {
	hours := bookingHours()
	if len(hours) == 0 {
		t.Fatal("expected booking hours")
	}
	expectedPrefix := []string{"06:00", "06:15", "06:30", "06:45", "07:00"}
	for i, value := range expectedPrefix {
		if hours[i] != value {
			t.Fatalf("expected hours[%d] = %s, got %s", i, value, hours[i])
		}
	}
	last := hours[len(hours)-1]
	if last != "22:45" {
		t.Fatalf("expected last slot to be 22:45, got %s", last)
	}
}

func TestQuarterHourBookingConflictsWithOverlappingHour(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	slotDate := time.Now().AddDate(0, 0, 7).Format("2006-01-02")

	createConfirmedBookingForTests(t, app, SpaceSchedule{
		SlotDate:      slotDate,
		SlotHour:      "18:00",
		EntryType:     "booking",
		Activity:      "badminton",
		Quantity:      1,
		Title:         "Base Booking",
		RequesterName: "Existing Customer",
		QuotedPrice:   2500,
	})

	existing, err := app.schedulesForSlot(slotDate, "18:15", 0)
	if err != nil {
		t.Fatalf("load overlapping slot schedules: %v", err)
	}
	if len(existing) != 1 || existing[0].SlotHour != "18:00" {
		t.Fatalf("expected 18:00 booking to overlap 18:15 slot, got %#v", existing)
	}

	layouts, err := app.listActiveCourtLayouts()
	if err != nil {
		t.Fatalf("list active layouts: %v", err)
	}
	err = validateSpaceScheduleSlotAgainstLayouts(existing, SpaceSchedule{
		SlotDate:  slotDate,
		SlotHour:  "18:15",
		EntryType: "booking",
		Activity:  "badminton",
		Quantity:  1,
		Status:    bookingStatusPending,
	}, layouts)
	if err == nil {
		t.Fatal("expected overlapping quarter-hour booking to be rejected")
	}
}

func TestCreateEnrollmentHandlerRejectsDuplicateEnrollment(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:                "STD-DUP-001",
		FullName:                 "Duplicate Enrollment Student",
		AdmissionDate:            "2026-08-01",
		DateOfBirth:              "2012-02-10",
		Gender:                   "male",
		PracticeType:             "student",
		Address:                  "Jaffna",
		PassportNumber:           "TP-DUP-001",
		School:                   "Test School",
		GuardianName:             "Guardian",
		GuardianRelationship:     "Parent",
		GuardianContactNumber:    "0770000000",
		GuardianAlternativePhone: "0770000001",
		MedicalInformation:       "None",
		QRCodeValue:              "STD-DUP-001",
		QRCodePath:               "/uploads/students/qr/student-qr-dup.png",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}

	trainingProgramID, err := app.createTrainingProgram(TrainingProgram{
		Name:           "Duplicate Programme",
		Activity:       "cricket",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     2000,
		Active:         true,
		SortOrder:      10,
	})
	if err != nil {
		t.Fatalf("create training programme: %v", err)
	}

	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: trainingProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("seed first enrollment: %v", err)
	}

	form := url.Values{
		"csrf_token":          {"token"},
		"admission_id":        {strconv.FormatInt(admissionID, 10)},
		"training_program_id": {strconv.FormatInt(trainingProgramID, 10)},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()

	app.createEnrollmentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect for duplicate enrollment, got %d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/enrollments?admission_id=1" {
		t.Fatalf("expected redirect to enrollments, got %q", location)
	}
	foundFlash := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			foundFlash = true
			break
		}
	}
	if !foundFlash {
		t.Fatal("expected flash cookie for duplicate enrollment message")
	}
}

func TestEnrollmentManagementHandlerScopesOperationalDataByDivision(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	kecProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     kecID,
		Name:           "KEC Reading",
		Activity:       "reading",
		TrainingFormat: "group",
		AdmissionFee:   1200,
		MonthlyFee:     1800,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create kec training programme: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Elite",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2200,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}

	kecAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-KEC-ENR-001",
		FullName:              "KEC Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2014-01-02",
		Gender:                "female",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian KEC",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771110001",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create kec admission: %v", err)
	}
	chessAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-CHESS-ENR-001",
		FullName:              "Chess Student",
		AdmissionDate:         "2026-08-02",
		DateOfBirth:           "2013-03-04",
		Gender:                "male",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian Chess",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771110002",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create chess admission: %v", err)
	}

	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       kecAdmissionID,
		TrainingProgramID: kecProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create kec enrollment: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       chessAdmissionID,
		TrainingProgramID: chessProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create chess enrollment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/enrollments?admission_id="+strconv.FormatInt(kecAdmissionID, 10), nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          101,
		Name:        "KEC Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"students.manage"},
		DivisionIDs: []int64{kecID},
		Divisions:   []Division{{ID: kecID, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center", Active: true}},
	}))
	rec := httptest.NewRecorder()

	app.enrollmentManagementHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("enrollment management status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "KEC Reading") {
		t.Fatalf("expected KEC programme in scoped view, got %s", body)
	}
	if strings.Contains(body, "Chess Elite") {
		t.Fatalf("did not expect chess programme in KEC-scoped view, got %s", body)
	}
}

func TestEnrollmentManagementHandlerForbidsCrossDivisionEnrollmentIDAccess(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Restricted",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     1500,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}
	chessAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-CHESS-LOCK-001",
		FullName:              "Locked Chess Student",
		AdmissionDate:         "2026-08-03",
		DateOfBirth:           "2015-05-06",
		Gender:                "male",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian Chess",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771110003",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create chess admission: %v", err)
	}
	chessEnrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       chessAdmissionID,
		TrainingProgramID: chessProgramID,
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create chess enrollment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/enrollments?action=view&id="+strconv.FormatInt(chessEnrollmentID, 10)+"&admission_id="+strconv.FormatInt(chessAdmissionID, 10), nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          102,
		Name:        "KEC Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"students.manage"},
		DivisionIDs: []int64{kecID},
		Divisions:   []Division{{ID: kecID, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center", Active: true}},
	}))
	rec := httptest.NewRecorder()

	app.enrollmentManagementHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-division enrollment access status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestStudentLeaveManagementHandlerScopesOperationalDataByDivision(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	kecProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     kecID,
		Name:           "KEC Leave Programme",
		Activity:       "reading",
		TrainingFormat: "group",
		AdmissionFee:   1200,
		MonthlyFee:     1800,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create kec training programme: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Leave Programme",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1500,
		MonthlyFee:     2200,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}

	kecAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-KEC-LEAVE-001",
		FullName:              "KEC Leave Student",
		AdmissionDate:         "2026-08-01",
		DateOfBirth:           "2014-01-02",
		Gender:                "female",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian KEC",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771111001",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create kec student: %v", err)
	}
	chessAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-CHESS-LEAVE-001",
		FullName:              "Chess Leave Student",
		AdmissionDate:         "2026-08-02",
		DateOfBirth:           "2013-03-04",
		Gender:                "male",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian Chess",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771111002",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create chess student: %v", err)
	}

	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       kecAdmissionID,
		TrainingProgramID: kecProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create kec enrollment: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       chessAdmissionID,
		TrainingProgramID: chessProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create chess enrollment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/student-leaves?admission_id="+strconv.FormatInt(kecAdmissionID, 10), nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          103,
		Name:        "KEC Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"students.manage"},
		DivisionIDs: []int64{kecID},
		Divisions:   []Division{{ID: kecID, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center", Active: true}},
	}))
	rec := httptest.NewRecorder()

	app.studentLeaveManagementHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("student leave status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "KEC Leave Programme") {
		t.Fatalf("expected kec programme in scoped leave view, got %s", body)
	}
	if strings.Contains(body, "Chess Leave Programme") {
		t.Fatalf("did not expect chess programme in KEC-scoped leave view, got %s", body)
	}
}

func TestStudentIDCardHandlerForbidsCrossDivisionStudentAccess(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Identity Programme",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1000,
		MonthlyFee:     1500,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}
	chessAdmissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-CHESS-ID-001",
		FullName:              "Chess Identity Student",
		AdmissionDate:         "2026-08-03",
		DateOfBirth:           "2015-05-06",
		Gender:                "male",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian Chess",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771111003",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create chess student: %v", err)
	}
	if _, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       chessAdmissionID,
		TrainingProgramID: chessProgramID,
	}, false, "cash", 0); err != nil {
		t.Fatalf("create chess enrollment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/students/student-id?id="+strconv.FormatInt(chessAdmissionID, 10), nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          104,
		Name:        "KEC Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"students.manage"},
		DivisionIDs: []int64{kecID},
		Divisions:   []Division{{ID: kecID, Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center", Active: true}},
	}))
	rec := httptest.NewRecorder()

	app.studentIDCardHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-division student id status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestUpdateEnrollmentHandlerRejectsCrossDivisionMove(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	kecID, err := divisionIDByCode(app.db, divisionCodeKEC)
	if err != nil {
		t.Fatalf("find kec division: %v", err)
	}
	chessID, err := divisionIDByCode(app.db, divisionCodeChess)
	if err != nil {
		t.Fatalf("find chess division: %v", err)
	}

	kecProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     kecID,
		Name:           "KEC Starter",
		Activity:       "reading",
		TrainingFormat: "group",
		AdmissionFee:   1100,
		MonthlyFee:     1700,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create kec training programme: %v", err)
	}
	chessProgramID, err := app.createTrainingProgram(TrainingProgram{
		DivisionID:     chessID,
		Name:           "Chess Switch Target",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   1300,
		MonthlyFee:     2100,
		Active:         true,
	})
	if err != nil {
		t.Fatalf("create chess training programme: %v", err)
	}
	admissionID, _, err := app.createAdmissionWithOptionalPayment(Admission{
		StudentID:             "STD-MOVE-001",
		FullName:              "Move Block Student",
		AdmissionDate:         "2026-08-04",
		DateOfBirth:           "2014-04-04",
		Gender:                "female",
		PracticeType:          "student",
		Address:               "Jaffna",
		GuardianName:          "Guardian Move",
		GuardianRelationship:  "Parent",
		GuardianContactNumber: "0771110004",
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create admission: %v", err)
	}
	enrollmentID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
		AdmissionID:       admissionID,
		TrainingProgramID: kecProgramID,
	}, false, "cash", 0)
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}

	form := url.Values{
		"csrf_token":          {"token"},
		"enrollment_id":       {strconv.FormatInt(enrollmentID, 10)},
		"training_program_id": {strconv.FormatInt(chessProgramID, 10)},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrollments/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &User{
		ID:          103,
		Name:        "Multi Division Admin",
		Roles:       []string{"admin"},
		Permissions: []string{"students.manage"},
		DivisionIDs: []int64{kecID, chessID},
	}))
	rec := httptest.NewRecorder()

	app.updateEnrollmentHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("cross-division update status = %d, want %d body=%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/admin/enrollments?admission_id="+strconv.FormatInt(admissionID, 10)+"&action=edit&id="+strconv.FormatInt(enrollmentID, 10) {
		t.Fatalf("cross-division update redirect = %q", got)
	}
	updated, err := app.findStudentEnrollmentByID(enrollmentID)
	if err != nil {
		t.Fatalf("reload enrollment: %v", err)
	}
	if updated.TrainingProgramID != kecProgramID {
		t.Fatalf("expected enrollment to remain on original programme %d, got %d", kecProgramID, updated.TrainingProgramID)
	}
}

func TestRunMigrationsHandlesLegacyAdmissionsWithoutQRCodeColumns(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL, password_hash TEXT NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE roles (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_id INTEGER NOT NULL, PRIMARY KEY (user_id, role_id))`,
		`CREATE TABLE role_permissions (role_id INTEGER NOT NULL, permission TEXT NOT NULL, PRIMARY KEY (role_id, permission))`,
		`CREATE TABLE sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE email_verifications (user_id INTEGER PRIMARY KEY, otp_hash TEXT NOT NULL, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE admissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			student_id TEXT NOT NULL UNIQUE,
			full_name TEXT NOT NULL,
			admission_date TEXT NOT NULL,
			date_of_birth TEXT NOT NULL,
			gender TEXT NOT NULL,
			practice_type TEXT NOT NULL DEFAULT 'group_practice',
			address TEXT NOT NULL,
			passport_number TEXT NOT NULL,
			school TEXT NOT NULL,
			guardian_name TEXT NOT NULL,
			guardian_relationship TEXT NOT NULL,
			guardian_contact_number TEXT NOT NULL,
			guardian_alternative_contact_number TEXT NOT NULL,
			medical_information TEXT NOT NULL,
			free_admission INTEGER NOT NULL DEFAULT 0,
			free_monthly_fee INTEGER NOT NULL DEFAULT 0,
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			training_program_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE student_monthly_payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			payment_month TEXT NOT NULL,
			amount REAL NOT NULL,
			payment_method TEXT NOT NULL,
			finance_transaction_id INTEGER,
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations on legacy admissions schema: %v", err)
	}

	for _, column := range []string{"photo_path", "qr_code_path", "qr_code_value"} {
		exists, err := tableHasColumn(db, "admissions", column)
		if err != nil {
			t.Fatalf("check admissions %s column: %v", column, err)
		}
		if !exists {
			t.Fatalf("expected admissions column %s to exist after migration", column)
		}
	}
	exists, err := tableHasColumn(db, "student_monthly_payments", "enrollment_id")
	if err != nil {
		t.Fatalf("check student_monthly_payments enrollment_id column: %v", err)
	}
	if !exists {
		t.Fatal("expected student_monthly_payments.enrollment_id to exist after migration")
	}
}

func TestHealthHandlerExposesNoSecrets(t *testing.T) {
	app := newReadinessTestApp(t)
	app.bookingAccess.TokenSecret = "super-secret-production-token"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	app.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, app.bookingAccess.TokenSecret) {
		t.Fatalf("health body exposed booking secret: %s", body)
	}
}

func TestHealthHandlerStaysOKWhenPricingWarningsExist(t *testing.T) {
	app := newReadinessTestApp(t)
	if _, err := app.db.Exec(`
		UPDATE pricing_rules
		SET weekday_offpeak_price = 0, weekday_peak_price = 0, weekend_offpeak_price = 0, weekend_peak_price = 0
		WHERE activity = 'badminton' AND quantity = 1
	`); err != nil {
		t.Fatalf("zero badminton pricing: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	app.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("health body missing ok status: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "pricing") {
		t.Fatalf("health body should not include pricing state: %s", body)
	}
}

func TestReadyHandlerExposesNoSecrets(t *testing.T) {
	app := newReadinessTestApp(t)
	app.bookingAccess.TokenSecret = "super-secret-production-token"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	app.readyHandler(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, app.bookingAccess.TokenSecret) || strings.Contains(body, "SMTP_PASS") || strings.Contains(body, "SMS_API_KEY") {
		t.Fatalf("ready body exposed secret data: %s", body)
	}
}

func TestPublicBookingStatusHandlerExpiresOverdueUnresolvedBooking(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	schedule := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "Overdue Request",
		RequesterName:  "Overdue Customer",
		RequesterEmail: "overdue@example.com",
		RequesterPhone: "+94771111111",
		QuotedPrice:    2500,
	}
	detailed, _, err := app.createPublicBookingRequestDetailed(schedule)
	if err != nil {
		t.Fatalf("create overdue request: %v", err)
	}
	_, rawToken, err := app.ensureActiveBookingAccessToken(detailed.ID, "status")
	if err != nil {
		t.Fatalf("issue booking status token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/booking/status?token="+url.QueryEscape(rawToken), nil)
	rec := httptest.NewRecorder()
	app.publicBookingStatusHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected booking status page, got %d", rec.Code)
	}

	updated, err := app.findSpaceScheduleByID(detailed.ID)
	if err != nil {
		t.Fatalf("reload expired booking: %v", err)
	}
	if updated.Status != bookingStatusExpired {
		t.Fatalf("expected expired status, got %s", updated.Status)
	}
	var expiryCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM booking_request_changes WHERE schedule_id = ? AND new_status = ?`, detailed.ID, bookingStatusExpired).Scan(&expiryCount); err != nil {
		t.Fatal(err)
	}
	if expiryCount != 1 {
		t.Fatalf("expected one expiry history record, got %d", expiryCount)
	}
}

func TestRequirePermissionBlocksUnauthorizedBookingAutoAcceptAndHold(t *testing.T) {
	app := newAuthorizationTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	if err := seedCourtManager(app.db); err != nil {
		t.Fatalf("seed court manager: %v", err)
	}
	user, err := app.createManagedUser("Customer", "customer@example.com", "password-123", []string{"customer"}, true)
	if err != nil {
		t.Fatal(err)
	}

	autoForm := url.Values{
		"csrf_token":  {"token"},
		"activity_id": {"1"},
		"court_id":    {"1"},
		"auto_accept": {"1"},
	}
	autoReq := httptest.NewRequest(http.MethodPost, "/admin/courts/activities/auto-accept", strings.NewReader(autoForm.Encode()))
	autoReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	autoReq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	autoReq.PostForm = autoForm
	autoReq = autoReq.WithContext(context.WithValue(autoReq.Context(), userContextKey, user))
	autoRec := httptest.NewRecorder()
	app.requirePermission(http.HandlerFunc(app.updateCourtActivityAutoAcceptHandler), "courts.manage").ServeHTTP(autoRec, autoReq)
	if autoRec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden auto-accept update, got %d", autoRec.Code)
	}

	if _, err := app.db.Exec(`
		INSERT INTO space_schedules (
			slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
			requester_name, requester_email, requester_phone, created_at, updated_at
		) VALUES (?, '18:00', 'booking', 'badminton', 1, 'Pending', '', 'pending', 'Requester', 'requester@example.com', '0700000000', ?, ?)
	`, time.Now().AddDate(0, 0, 2).Format("2006-01-02"), time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("insert pending request: %v", err)
	}
	var scheduleID int64
	if err := app.db.QueryRow(`SELECT id FROM space_schedules WHERE title = 'Pending'`).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	holdForm := url.Values{
		"csrf_token":  {"token"},
		"schedule_id": {fmt.Sprintf("%d", scheduleID)},
	}
	holdReq := httptest.NewRequest(http.MethodPost, "/admin/booking-requests/hold", strings.NewReader(holdForm.Encode()))
	holdReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	holdReq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	holdReq.PostForm = holdForm
	holdReq = holdReq.WithContext(context.WithValue(holdReq.Context(), userContextKey, user))
	holdRec := httptest.NewRecorder()
	app.requirePermission(http.HandlerFunc(app.holdBookingRequestHandler), "booking_requests.manage").ServeHTTP(holdRec, holdReq)
	if holdRec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden hold action, got %d", holdRec.Code)
	}
}

func TestRunMigrationsPreservesExistingConfirmedBookingAndAutoAcceptDefaults(t *testing.T) {
	db, err := sql.Open("sqlite", "file:migration-compat-booking-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	statements := []string{
		`CREATE TABLE courts (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, code TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE court_activities (id INTEGER PRIMARY KEY AUTOINCREMENT, court_id INTEGER NOT NULL, activity TEXT NOT NULL, display_name TEXT NOT NULL, max_quantity INTEGER NOT NULL DEFAULT 1, active INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE space_schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, slot_date TEXT NOT NULL, slot_hour TEXT NOT NULL, entry_type TEXT NOT NULL, activity TEXT NOT NULL, quantity INTEGER NOT NULL, title TEXT NOT NULL, notes TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'confirmed', requester_name TEXT NOT NULL DEFAULT '', requester_email TEXT NOT NULL DEFAULT '', requester_phone TEXT NOT NULL DEFAULT '', requested_by_user_id INTEGER, review_note TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE booking_financials (id INTEGER PRIMARY KEY AUTOINCREMENT, schedule_id INTEGER NOT NULL UNIQUE, quoted_amount REAL NOT NULL DEFAULT 0, paid INTEGER NOT NULL DEFAULT 0, paid_at DATETIME, payment_method TEXT NOT NULL DEFAULT '', finance_transaction_id INTEGER, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE booking_communications (id INTEGER PRIMARY KEY AUTOINCREMENT, schedule_id INTEGER NOT NULL, event_type TEXT NOT NULL, related_event_type TEXT NOT NULL DEFAULT '', event_key TEXT NOT NULL, channel TEXT NOT NULL, recipient TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '', body_preview TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', provider TEXT NOT NULL DEFAULT '', provider_message TEXT NOT NULL DEFAULT '', attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at DATETIME, sent_at DATETIME, created_at DATETIME NOT NULL, created_by_user_id INTEGER)`,
		`CREATE TABLE booking_access_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, schedule_id INTEGER NOT NULL, public_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, purpose TEXT NOT NULL DEFAULT 'status', active INTEGER NOT NULL DEFAULT 1, expires_at DATETIME NOT NULL, last_accessed_at DATETIME, created_at DATETIME NOT NULL, revoked_at DATETIME)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO courts (id, name, code, description, active, sort_order, created_at, updated_at) VALUES (1, 'Court', 'COURT', '', 1, 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO court_activities (id, court_id, activity, display_name, max_quantity, active, sort_order, created_at, updated_at) VALUES (1, 1, 'badminton', 'Badminton', 1, 1, 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO space_schedules (id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status, requester_name, requester_email, requester_phone, requested_by_user_id, review_note, created_at, updated_at) VALUES (1, '2026-08-10', '18:00', 'booking', 'badminton', 1, 'Confirmed', '', 'confirmed', 'Customer', 'customer@example.com', '0700000000', NULL, '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO booking_financials (schedule_id, quoted_amount, paid, payment_method, created_at, updated_at) VALUES (1, 2500, 1, 'cash', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations on legacy schema: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM space_schedules WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != bookingStatusConfirmed {
		t.Fatalf("confirmed booking changed during migration: %s", status)
	}
	var autoAccept int
	if err := db.QueryRow(`SELECT auto_accept FROM court_activities WHERE id = 1`).Scan(&autoAccept); err != nil {
		t.Fatal(err)
	}
	if autoAccept != 0 {
		t.Fatalf("expected auto_accept default 0 for legacy activity, got %d", autoAccept)
	}
}

func TestRunMigrationsNormalizesLegacyBookingStatuses(t *testing.T) {
	db, err := sql.Open("sqlite", "file:migration-normalize-booking-status-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE space_schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, slot_date TEXT NOT NULL, slot_hour TEXT NOT NULL, entry_type TEXT NOT NULL, activity TEXT NOT NULL, quantity INTEGER NOT NULL, title TEXT NOT NULL, notes TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'confirmed', requester_name TEXT NOT NULL DEFAULT '', requester_email TEXT NOT NULL DEFAULT '', requester_phone TEXT NOT NULL DEFAULT '', requested_by_user_id INTEGER, review_note TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO space_schedules (slot_date, slot_hour, entry_type, activity, quantity, title, notes, status, created_at, updated_at) VALUES (?, ?, 'booking', 'badminton', 1, 'Legacy Pending', '', 'Pending', ?, ?)`, now.Format("2006-01-02"), "18:00", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO space_schedules (slot_date, slot_hour, entry_type, activity, quantity, title, notes, status, created_at, updated_at) VALUES (?, ?, 'booking', 'badminton', 1, 'Legacy Reschedule', '', 'Reschedule Pending', ?, ?)`, now.Format("2006-01-02"), "19:00", now, now); err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	rows, err := db.Query(`SELECT status FROM space_schedules ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0] != bookingStatusPending {
		t.Fatalf("expected pending status, got %q", statuses[0])
	}
	if statuses[1] != bookingStatusReschedulePending {
		t.Fatalf("expected reschedule_pending status, got %q", statuses[1])
	}
}

func TestCustomerBookingStatusPageDoesNotExposeInternalReviewNotes(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates

	request := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "Customer Status Review",
		RequesterName:  "Customer",
		RequesterEmail: "customer@example.com",
		RequesterPhone: "+94771112233",
		QuotedPrice:    2500,
	}
	scheduleID, err := app.createPublicBookingRequest(request)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, _, err := app.transitionBookingRequestStatus(scheduleID, bookingStatusHeld, "Internal-only review note", "Customer-safe hold message", "admin", 0); err != nil {
		t.Fatalf("hold request: %v", err)
	}
	_, rawToken, err := app.ensureActiveBookingAccessToken(scheduleID, "status")
	if err != nil {
		t.Fatalf("issue booking status token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/booking/status?token="+url.QueryEscape(rawToken), nil)
	rec := httptest.NewRecorder()
	app.publicBookingStatusHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected booking status page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Internal-only review note") {
		t.Fatal("customer status page exposed internal review note")
	}
	if !strings.Contains(body, "Customer-safe hold message") {
		t.Fatal("customer status page did not include customer-facing message")
	}
}

func TestBookingEditPreservesQuotedPriceSnapshot(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-edit-quote-snapshot-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{db: db}

	scheduleID := createConfirmedFutureBooking(t, app, 6, "18:00")
	schedule, err := app.findSpaceScheduleByID(scheduleID)
	if err != nil {
		t.Fatalf("find booking: %v", err)
	}

	var originalQuoted float64
	if err := db.QueryRow(`SELECT quoted_amount FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&originalQuoted); err != nil {
		t.Fatalf("load original quote snapshot: %v", err)
	}

	schedule.Title = "Retitled Booking"
	schedule.Notes = "Updated notes"
	schedule.QuotedPrice = originalQuoted + 1234
	if err := app.updateSpaceSchedule(*schedule); err != nil {
		t.Fatalf("update booking: %v", err)
	}

	var preservedQuoted float64
	if err := db.QueryRow(`SELECT quoted_amount FROM booking_financials WHERE schedule_id = ?`, scheduleID).Scan(&preservedQuoted); err != nil {
		t.Fatalf("load preserved quote snapshot: %v", err)
	}
	if preservedQuoted != originalQuoted {
		t.Fatalf("quoted snapshot mutated after edit: got %.2f want %.2f", preservedQuoted, originalQuoted)
	}
}

func TestCompletedAndNoShowStatusValidation(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-complete-no-show-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{db: db}

	futureID := createConfirmedFutureBooking(t, app, 5, "19:00")
	if _, _, err := app.transitionManagedBookingStatus(futureID, bookingStatusCompleted, "", "", "", "", "admin", 0); err == nil {
		t.Fatal("expected future booking completion to be rejected")
	}
	if _, _, err := app.transitionManagedBookingStatus(futureID, bookingStatusNoShow, "", "", "", "", "admin", 0); err == nil {
		t.Fatal("expected future booking no-show to be rejected")
	}

	pastRequest := SpaceSchedule{
		SlotDate:       time.Now().AddDate(0, 0, -2).Format("2006-01-02"),
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "Past Confirmed Booking",
		RequesterName:  "Past Customer",
		RequesterEmail: "past@example.com",
		RequesterPhone: "0711111111",
		QuotedPrice:    2500,
	}
	pastID := createConfirmedBookingForTests(t, app, pastRequest)
	if _, _, err := app.transitionManagedBookingStatus(pastID, bookingStatusCompleted, "", "", "", "", "admin", 0); err != nil {
		t.Fatalf("mark past booking completed: %v", err)
	}

	pastNoShow := pastRequest
	pastNoShow.SlotHour = "19:00"
	pastNoShow.Title = "Past No Show Booking"
	pastNoShowID := createConfirmedBookingForTests(t, app, pastNoShow)
	if _, _, err := app.transitionManagedBookingStatus(pastNoShowID, bookingStatusNoShow, "", "", "", "", "admin", 0); err != nil {
		t.Fatalf("mark past booking no-show: %v", err)
	}
}

func TestCustomerCancellationRequestRequiresValidActiveToken(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-cancel-request-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{
		db: db,
		bookingAccess: BookingAccessSettings{
			BaseURL:     "http://localhost:8080",
			TokenSecret: "test-secret",
			TokenTTL:    180 * 24 * time.Hour,
		},
	}
	scheduleID := createConfirmedFutureBooking(t, app, 4, "20:00")
	_, rawToken, err := app.ensureActiveBookingAccessToken(scheduleID, "status")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	form := url.Values{}
	form.Set("token", rawToken)
	form.Set("request_reason", "Need to cancel")
	req := httptest.NewRequest(http.MethodPost, "/booking/status/cancellation-request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
	req.PostForm = form
	rec := httptest.NewRecorder()
	app.publicBookingCancellationRequestHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected csrf rejection without matching form token handling, got %d", rec.Code)
	}

	if err := app.revokeBookingAccessToken(scheduleID, "status"); err != nil {
		t.Fatalf("revoke access token: %v", err)
	}
	if _, _, err := app.findActiveBookingByAccessToken(rawToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected revoked token lookup to fail, got %v", err)
	}
}

func TestCustomerCancellationRequestCreatesPendingRequestAndBlocksDuplicate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:booking-cancel-request-success-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	seedBookingEngine(t, db)
	app := &App{
		db: db,
		bookingAccess: BookingAccessSettings{
			BaseURL:     "http://localhost:8080",
			TokenSecret: "test-secret",
			TokenTTL:    180 * 24 * time.Hour,
		},
	}

	scheduleID := createConfirmedFutureBooking(t, app, 4, "21:00")
	_, rawToken, err := app.ensureActiveBookingAccessToken(scheduleID, "status")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	sendRequest := func(reason string) *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("token", rawToken)
		form.Set("csrf_token", "token")
		form.Set("request_reason", reason)
		req := httptest.NewRequest(http.MethodPost, "/booking/status/cancellation-request", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
		req.PostForm = form
		rec := httptest.NewRecorder()
		app.publicBookingCancellationRequestHandler(rec, req)
		return rec
	}

	first := sendRequest("Travel issue")
	if first.Code != http.StatusSeeOther {
		t.Fatalf("expected first cancellation request redirect, got %d", first.Code)
	}

	var requestCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM booking_cancellation_requests WHERE schedule_id = ? AND status = 'pending'`, scheduleID).Scan(&requestCount); err != nil {
		t.Fatalf("count pending cancellation requests: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected one pending cancellation request, got %d", requestCount)
	}

	var actionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM booking_request_changes WHERE schedule_id = ? AND action_type = 'cancellation_requested'`, scheduleID).Scan(&actionCount); err != nil {
		t.Fatalf("count cancellation request history: %v", err)
	}
	if actionCount != 1 {
		t.Fatalf("expected one cancellation request history entry, got %d", actionCount)
	}

	second := sendRequest("Second attempt")
	if second.Code != http.StatusSeeOther {
		t.Fatalf("expected duplicate cancellation request redirect, got %d", second.Code)
	}
	if got := second.Header().Get("Location"); got != "/booking/status?token="+url.QueryEscape(rawToken) {
		t.Fatalf("duplicate cancellation redirect = %q", got)
	}
	flashFound := false
	for _, cookie := range second.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			flashFound = true
			break
		}
	}
	if !flashFound {
		t.Fatal("expected flash cookie for duplicate cancellation request")
	}
}

func TestOperationalReportsAndCSVExport(t *testing.T) {
	db, err := sql.Open("sqlite", "file:operational-report-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	app := &App{db: db}
	reportDate := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)

	if _, err := app.createManualFinanceTransaction("manual_income", "Sponsor", "Community sponsorship", "bank_transfer", 10000, reportDate, 0); err != nil {
		t.Fatalf("create report income: %v", err)
	}
	if _, err := app.createManualFinanceTransaction("utilities_expense", "Utility provider", "Electricity", "cash", -2000, reportDate, 0); err != nil {
		t.Fatalf("create report expense: %v", err)
	}
	now := time.Now().UTC()
	admissionResult, err := db.Exec(`
		INSERT INTO admissions (
			student_id, full_name, admission_date, date_of_birth, gender, practice_type, address,
			passport_number, school, guardian_name, guardian_relationship, guardian_contact_number,
			guardian_alternative_contact_number, medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "STD-REPORT", "Report Student", "2026-07-15", "2012-05-10", "male", "group_practice",
		"Address", "P-REPORT", "School", "Guardian", "Parent", "0700000000", "0710000000", "None", now, now)
	if err != nil {
		t.Fatalf("create report admission: %v", err)
	}
	admissionID, _ := admissionResult.LastInsertId()
	groupResult, err := db.Exec(`
		INSERT INTO student_groups (name, code, description, created_at, updated_at)
		VALUES ('Report Group', 'REPORT', 'Report test group', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	groupID, _ := groupResult.LastInsertId()
	for index, status := range []string{"present", "absent"} {
		if _, err := db.Exec(`
			INSERT INTO attendance_records (group_id, admission_id, attendance_date, status, note, recorded_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, ?)
		`, groupID, admissionID, fmt.Sprintf("2026-07-%02d", 15+index), status, now, now); err != nil {
			t.Fatalf("create attendance record: %v", err)
		}
	}
	for index, status := range []string{"confirmed", "pending"} {
		if _, err := db.Exec(`
			INSERT INTO space_schedules (
				slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
				requester_name, requester_email, requester_phone, review_note, created_at, updated_at
			) VALUES ('2026-07-15', ?, 'booking', 'badminton', 1, 'Report booking', '', ?, 'Customer', 'customer@example.com', '0700000000', '', ?, ?)
		`, fmt.Sprintf("%02d:00", 10+index), status, now, now); err != nil {
			t.Fatalf("create report booking: %v", err)
		}
	}

	request := httptest.NewRequest("GET", "/admin/reports?period=week&date=2026-07-15", nil)
	period := reportPeriodFromRequest(request)
	if period.Start != "2026-07-13" || period.End != "2026-07-19" {
		t.Fatalf("unexpected weekly period: %#v", period)
	}
	report, err := app.buildOperationalReport(period, nil)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	if report.Summary.Income != 10000 || report.Summary.Expenses != 2000 || report.Summary.NetCash != 8000 {
		t.Fatalf("unexpected cash summary: %#v", report.Summary)
	}
	if report.Summary.ConfirmedBookings != 1 || report.Summary.PendingBookings != 1 || report.Summary.NewAdmissions != 1 {
		t.Fatalf("unexpected operations summary: %#v", report.Summary)
	}
	if report.Summary.AttendancePresent != 1 || report.Summary.AttendanceTotal != 2 || report.Summary.AttendanceRate != 50 {
		t.Fatalf("unexpected attendance summary: %#v", report.Summary)
	}
	if report.Summary.OccupiedSlotHours != 1 || len(report.Series) != 7 {
		t.Fatalf("unexpected utilization or series: %#v", report.Summary)
	}

	recorder := httptest.NewRecorder()
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, &User{
		ID:          500,
		Name:        "Superadmin",
		Roles:       []string{"superadmin"},
		Permissions: []string{"reports.view"},
	}))
	app.reportsExportHandler(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("unexpected export response: %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Mekmaa operational report") || !strings.Contains(body, "Community sponsorship") || !strings.Contains(body, "8000.00") {
		t.Fatalf("export is missing report data: %s", body)
	}

	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{
		User:   &User{Name: "Admin", Email: "admin@example.com", Roles: []string{"admin"}, Permissions: allPermissions},
		Report: report,
	}
	if err := templates["reports"].ExecuteTemplate(io.Discard, "base", data); err != nil {
		t.Fatalf("render reports template: %v", err)
	}
}
func TestValidateSpaceScheduleSlotAgainstLayoutsAllowsBadmintonAndCricketNet(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Badminton and Cricket Net",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "badminton",
					Quantity: 1,
				},
				{
					Activity: "cricket_net",
					Quantity: 1,
				},
			},
		},
	}

	existing := []SpaceSchedule{
		{
			EntryType: "booking",
			Activity:  "badminton",
			Quantity:  1,
			Status:    "confirmed",
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "cricket_net",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		candidate,
		layouts,
	)

	if err != nil {
		t.Fatalf(
			"expected badminton and one cricket net to be allowed, got %v",
			err,
		)
	}
}

func TestValidateSpaceScheduleSlotAgainstLayoutsRejectsExtraCricketNet(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Badminton and Cricket Net",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "badminton",
					Quantity: 1,
				},
				{
					Activity: "cricket_net",
					Quantity: 1,
				},
			},
		},
	}

	existing := []SpaceSchedule{
		{
			EntryType: "booking",
			Activity:  "badminton",
			Quantity:  1,
			Status:    "confirmed",
		},
		{
			EntryType: "booking",
			Activity:  "cricket_net",
			Quantity:  1,
			Status:    "confirmed",
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "cricket_net",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		candidate,
		layouts,
	)

	if err == nil {
		t.Fatal(
			"expected an additional cricket net to be rejected",
		)
	}
}

func TestValidateSpaceScheduleSlotAgainstLayoutsAllowsThreeCricketNets(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Three Cricket Nets",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "cricket_net",
					Quantity: 3,
				},
			},
		},
	}

	existing := []SpaceSchedule{
		{
			EntryType: "booking",
			Activity:  "cricket_net",
			Quantity:  1,
			Status:    "confirmed",
		},
		{
			EntryType: "booking",
			Activity:  "cricket_net",
			Quantity:  1,
			Status:    "confirmed",
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "cricket_net",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		candidate,
		layouts,
	)

	if err != nil {
		t.Fatalf(
			"expected three separate cricket-net bookings to be allowed, got %v",
			err,
		)
	}
}

func TestValidateSpaceScheduleSlotAgainstLayoutsRejectsFutsalWithBadminton(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Futsal",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "futsal",
					Quantity: 1,
				},
			},
		},
		{
			ID:      2,
			CourtID: 1,
			Name:    "Badminton and Cricket Net",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "badminton",
					Quantity: 1,
				},
				{
					Activity: "cricket_net",
					Quantity: 1,
				},
			},
		},
	}

	existing := []SpaceSchedule{
		{
			EntryType: "booking",
			Activity:  "futsal",
			Quantity:  1,
			Status:    "confirmed",
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "badminton",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		candidate,
		layouts,
	)

	if err == nil {
		t.Fatal(
			"expected badminton to be rejected when futsal occupies the slot",
		)
	}
}

func TestValidateSpaceScheduleSlotAgainstLayoutsIgnoresRejectedBookings(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Futsal",
			Active:  true,
			Items: []CourtLayoutItem{
				{
					Activity: "futsal",
					Quantity: 1,
				},
			},
		},
	}

	existing := []SpaceSchedule{
		{
			EntryType: "booking",
			Activity:  "badminton",
			Quantity:  1,
			Status:    "rejected",
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "futsal",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		candidate,
		layouts,
	)

	if err != nil {
		t.Fatalf(
			"expected rejected bookings not to consume capacity, got %v",
			err,
		)
	}
}

func TestValidateSpaceScheduleSlotAgainstLayoutsRejectsInactiveLayout(t *testing.T) {
	layouts := []CourtLayout{
		{
			ID:      1,
			CourtID: 1,
			Name:    "Badminton",
			Active:  false,
			Items: []CourtLayoutItem{
				{
					Activity: "badminton",
					Quantity: 1,
				},
			},
		},
	}

	candidate := SpaceSchedule{
		EntryType: "booking",
		Activity:  "badminton",
		Quantity:  1,
		Status:    "pending",
	}

	err := validateSpaceScheduleSlotAgainstLayouts(
		nil,
		candidate,
		layouts,
	)

	if err == nil {
		t.Fatal(
			"expected an inactive layout not to permit bookings",
		)
	}
}

func TestCourtLayoutSupportsUsageRejectsUnknownActivity(t *testing.T) {
	layout := CourtLayout{
		ID:      1,
		CourtID: 1,
		Name:    "Badminton",
		Active:  true,
		Items: []CourtLayoutItem{
			{
				Activity: "badminton",
				Quantity: 1,
			},
		},
	}

	usage := map[string]int{
		"badminton": 1,
		"futsal":    1,
	}

	if courtLayoutSupportsUsage(layout, usage) {
		t.Fatal(
			"expected layout to reject an activity it does not contain",
		)
	}
}

func TestCourtLayoutSupportsUsageAllowsUnusedCapacity(t *testing.T) {
	layout := CourtLayout{
		ID:      1,
		CourtID: 1,
		Name:    "Badminton and Cricket Net",
		Active:  true,
		Items: []CourtLayoutItem{
			{
				Activity: "badminton",
				Quantity: 1,
			},
			{
				Activity: "cricket_net",
				Quantity: 1,
			},
		},
	}

	usage := map[string]int{
		"badminton": 1,
	}

	if !courtLayoutSupportsUsage(layout, usage) {
		t.Fatal(
			"expected badminton alone to fit within the combined layout",
		)
	}
}

func TestGameCRUD(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	if _, err := app.db.Exec(`
		INSERT INTO court_activities (
			court_id, activity, display_name, max_quantity, auto_accept, active, sort_order, created_at, updated_at
		)
		VALUES (1, 'pickleball', 'Pickleball', 1, 0, 1, 90, ?, ?)
	`, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("create court activity for game: %v", err)
	}

	gameID, err := app.createGame(Game{
		Name:        "Pickleball",
		Activity:    "pickleball",
		Description: "Test game",
		Active:      true,
		SortOrder:   90,
	})
	if err != nil {
		t.Fatalf("create game: %v", err)
	}

	game, err := app.findGameByID(gameID)
	if err != nil {
		t.Fatalf("find game: %v", err)
	}
	if game.Name != "Pickleball" || game.Activity != "pickleball" || !game.Active {
		t.Fatalf("unexpected game after create: %#v", game)
	}

	if err := app.updateGame(Game{
		ID:          gameID,
		Name:        "Pickleball Plus",
		Activity:    "pickleball",
		Description: "Updated",
		Active:      false,
		SortOrder:   91,
	}); err != nil {
		t.Fatalf("update game: %v", err)
	}

	updated, err := app.findGameByID(gameID)
	if err != nil {
		t.Fatalf("find updated game: %v", err)
	}
	if updated.Name != "Pickleball Plus" || updated.Description != "Updated" || updated.Active || updated.SortOrder != 91 {
		t.Fatalf("unexpected game after update: %#v", updated)
	}

	if err := app.deleteGame(gameID); err != nil {
		t.Fatalf("delete game: %v", err)
	}
	if _, err := app.findGameByID(gameID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted game to be missing, got %v", err)
	}
}

func TestDeleteCourtActivityAllowsOnlyUnlinkedActivities(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	now := time.Now().UTC()
	result, err := app.db.Exec(`
		INSERT INTO court_activities (
			court_id, game_id, activity, display_name, max_quantity, auto_accept, active, sort_order, created_at, updated_at
		)
		VALUES (1, 0, 'pickleball', 'Pickleball', 1, 0, 1, 90, ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("create activity: %v", err)
	}
	activityID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("activity last insert id: %v", err)
	}

	gameID, err := app.createGame(Game{
		Name:        "Pickleball",
		Activity:    "pickleball",
		Description: "Test game",
		Active:      true,
		SortOrder:   90,
	})
	if err != nil {
		t.Fatalf("create linked game: %v", err)
	}

	if _, err := app.db.Exec(`
		INSERT INTO pricing_rules (
			game_id, activity, quantity, weekday_offpeak_price, weekday_peak_price,
			weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		)
		VALUES (?, 'pickleball', 1, 1000, 1000, 1000, 1000, ?, ?)
	`, gameID, now, now); err != nil {
		t.Fatalf("create linked pricing rule: %v", err)
	}

	if err := app.deleteCourtActivity(activityID); err == nil || !strings.Contains(err.Error(), "pricing rules") {
		t.Fatalf("expected delete to be blocked by pricing rule, got %v", err)
	}

	if _, err := app.db.Exec(`DELETE FROM pricing_rules WHERE activity = 'pickleball'`); err != nil {
		t.Fatalf("remove linked pricing rule: %v", err)
	}

	if err := app.deleteCourtActivity(activityID); err != nil {
		t.Fatalf("delete unlinked activity: %v", err)
	}
}

func TestValidatePricingRuleAgainstOptionsRejectsUnavailableQuantity(t *testing.T) {
	app := newBookingWorkflowTestApp(t)

	activities, layouts, err := app.activeBookingConfiguration()
	if err != nil {
		t.Fatalf("load active booking configuration: %v", err)
	}

	valid := PricingRule{
		GameID:   1,
		Activity: "full_indoor_cricket",
		Quantity: 1,
	}
	if err := validatePricingRuleAgainstOptions(valid, activities, layouts); err != nil {
		t.Fatalf("expected configured quantity to be valid, got %v", err)
	}

	invalid := PricingRule{
		GameID:   1,
		Activity: "full_indoor_cricket",
		Quantity: 2,
	}
	if err := validatePricingRuleAgainstOptions(invalid, activities, layouts); err == nil {
		t.Fatal("expected unavailable quantity to be rejected")
	}
}

func TestBuildFinanceSpecifiedLedgersGroupsCoreLedgers(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)

	recordedAt := time.Date(2026, time.August, 10, 9, 0, 0, 0, time.Local)
	cases := []struct {
		category    string
		person      string
		description string
		amount      float64
	}{
		{"booking_payment", "Booking Customer", "Badminton booking", 2500},
		{"admission_payment", "Admission Student", "Admission fee", 1500},
		{"student_monthly_payment", "Monthly Student", "Monthly fee", 3200},
		{"facility_expense", "Landlord", "August rent", -50000},
		{"electricity_bills_expense", "Power Board", "Electricity bill", -12000},
		{"staff_salary_expense", "Coach", "Salary payout", -30000},
		{"loan_repayment_expense", "Bank", "Loan installment", -9000},
	}
	for i, item := range cases {
		if _, err := app.createManualFinanceTransactionForAccountWithApproval(
			item.category,
			item.person,
			item.description,
			"",
			cashID,
			item.amount,
			recordedAt.Add(time.Duration(i)*time.Minute),
			0,
			financeApprovalApproved,
		); err != nil {
			t.Fatalf("create finance transaction for %s: %v", item.category, err)
		}
	}

	ledgers, from, to, err := app.buildFinanceSpecifiedLedgers("2026-08-01", "2026-08-31", nil)
	if err != nil {
		t.Fatalf("build specified ledgers: %v", err)
	}
	if from != "2026-08-01" || to != "2026-08-31" {
		t.Fatalf("unexpected period: from=%s to=%s", from, to)
	}

	found := make(map[string]FinanceSpecifiedLedger)
	for _, ledger := range ledgers {
		found[ledger.Key] = ledger
	}

	if ledger := found["bookings_all_games"]; ledger.CreditTotal != 2500 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected bookings ledger: %#v", ledger)
	}
	if ledger := found["admissions"]; ledger.CreditTotal != 1500 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected admissions ledger: %#v", ledger)
	}
	if ledger := found["class_monthly_fees"]; ledger.CreditTotal != 3200 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected monthly fees ledger: %#v", ledger)
	}
	if ledger := found["facility_expense"]; ledger.DebitTotal != 50000 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected facility expense ledger: %#v", ledger)
	}
	if ledger := found["electricity_bills_expense"]; ledger.DebitTotal != 12000 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected electricity expense ledger: %#v", ledger)
	}
	if ledger := found["staff_salary_expense"]; ledger.DebitTotal != 30000 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected salary expense ledger: %#v", ledger)
	}
	if ledger := found["loan_repayment_expense"]; ledger.DebitTotal != 9000 || ledger.EntryCount != 1 {
		t.Fatalf("unexpected loan repayment ledger: %#v", ledger)
	}
	if ledger := found["marketing_expense"]; ledger.EntryCount != 0 {
		t.Fatalf("expected empty category ledger to exist, got %#v", ledger)
	}
}

func TestBuildFinanceSpecifiedLedgersBankingUsesDebitCreditAssetView(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	cashID := financeAccountIDByName(t, app, financeAccountCashInHand)
	bankID := financeAccountIDByName(t, app, financeAccountMainBank)

	recordedAt := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.Local)

	if _, err := app.createFinanceOpeningBalance(cashID, 50000, recordedAt.Add(-time.Minute), "Cash opening", 0); err != nil {
		t.Fatalf("create cash opening balance: %v", err)
	}
	if _, err := app.createFinanceOpeningBalance(bankID, 100000, recordedAt, "Bank opening", 0); err != nil {
		t.Fatalf("create bank opening balance: %v", err)
	}
	if _, err := app.createFinanceTransfer(cashID, bankID, 15000, recordedAt, "TRF-1", "Cash to bank", "", 0); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if _, err := app.createManualFinanceTransactionForAccountWithApproval(
		"bank_charges_expense",
		"Bank",
		"Monthly charge",
		"",
		bankID,
		-500,
		recordedAt,
		0,
		financeApprovalApproved,
	); err != nil {
		t.Fatalf("create bank charge: %v", err)
	}

	ledgers, _, _, err := app.buildFinanceSpecifiedLedgers("2026-08-01", "2026-08-31", nil)
	if err != nil {
		t.Fatalf("build specified ledgers: %v", err)
	}

	var banking FinanceSpecifiedLedger
	for _, ledger := range ledgers {
		if ledger.Key == "banking" {
			banking = ledger
			break
		}
	}

	if banking.EntryCount != 2 {
		t.Fatalf("expected 2 banking entries, got %#v", banking)
	}
	if banking.DebitTotal != 115000 {
		t.Fatalf("unexpected banking debit total: %#v", banking)
	}
	if banking.CreditTotal != 0 {
		t.Fatalf("unexpected banking credit total: %#v", banking)
	}
	if banking.NetBalance != 115000 || banking.BalanceLabel != "Debit balance" {
		t.Fatalf("unexpected banking balance: %#v", banking)
	}

	var bankCharges FinanceSpecifiedLedger
	for _, ledger := range ledgers {
		if ledger.Key == "bank_charges_expense" {
			bankCharges = ledger
			break
		}
	}
	if bankCharges.EntryCount != 1 || bankCharges.DebitTotal != 500 {
		t.Fatalf("unexpected bank charges ledger: %#v", bankCharges)
	}
}

func TestSummarizeStudentAttendanceHistory(t *testing.T) {
	history := []StudentAttendanceHistoryRow{
		{Status: "present"},
		{Status: "present"},
		{Status: "late"},
		{Status: "absent"},
		{Status: "excused"},
	}

	summary := summarizeStudentAttendanceHistory(history)

	if summary.TotalEntries != 5 {
		t.Fatalf("TotalEntries = %d, want 5", summary.TotalEntries)
	}

	if summary.PresentCount != 2 {
		t.Fatalf("PresentCount = %d, want 2", summary.PresentCount)
	}

	if summary.AbsentCount != 1 {
		t.Fatalf("AbsentCount = %d, want 1", summary.AbsentCount)
	}

	if summary.LateCount != 1 {
		t.Fatalf("LateCount = %d, want 1", summary.LateCount)
	}

	if summary.ExcusedCount != 1 {
		t.Fatalf("ExcusedCount = %d, want 1", summary.ExcusedCount)
	}

	if summary.AttendedCount != 3 {
		t.Fatalf("AttendedCount = %d, want 3", summary.AttendedCount)
	}

	if summary.AttendanceRate != 75 {
		t.Fatalf(
			"AttendanceRate = %.2f, want 75",
			summary.AttendanceRate,
		)
	}
}
