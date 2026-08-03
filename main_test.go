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

func renderTemplateToString(t *testing.T, templates map[string]*template.Template, name string, data TemplateData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := templates[name].ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
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
			MonthlyFee: 0,
		}},
		PaymentMonth:      "2026-07",
		PaymentMonthLabel: "July 2026",
		TodayDate:         "2026-07",
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
	pending.Status = bookingStatusPending
	pending.RequesterEmail = "pending@example.com"
	pending.RequesterPhone = "0700000002"
	held := confirmed
	held.ID = 103
	held.Title = "Held Booking"
	held.Status = bookingStatusHeld
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
	if !strings.Contains(confirmedHTML, `name="payment_method" value="cash"`) {
		t.Fatal("confirmed booking request did not post cash only")
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
	if !strings.Contains(pendingHTML, "Cash collection becomes available after the booking is confirmed.") {
		t.Fatal("pending booking request should explain that collection is unavailable")
	}

	heldHTML := renderTemplateToString(t, templates, "booking-requests", bookingTestDataForRequestPage(bookingStaff, []SpaceSchedule{held}, financials, nil))
	if strings.Contains(heldHTML, `action="/admin/bookings/payments/collect"`) {
		t.Fatal("held booking request should not render active payment form")
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
		if !strings.Contains(html, "Cash was previously collected") && status == bookingStatusCancelled {
			t.Fatal("cancelled booking should warn when cash was previously collected")
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

func TestAutoAcceptedRequestDoesNotCreateDuplicateStatusCommunication(t *testing.T) {
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
		"title":           {"Auto Accepted"},
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
	if err := app.db.QueryRow(`SELECT id, status FROM space_schedules WHERE title = 'Auto Accepted'`).Scan(&scheduleID, &status); err != nil {
		t.Fatalf("load auto accepted booking: %v", err)
	}
	if status != bookingStatusConfirmed {
		t.Fatalf("expected confirmed status, got %s", status)
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
	if len(events) != 1 || events[0] != bookingCommEventConfirmed {
		t.Fatalf("unexpected auto-accept communication events: %v", events)
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
	if err := os.Chmod(app.uploads.EventDir, 0o500); err != nil {
		t.Fatalf("chmod upload dir: %v", err)
	}
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

func TestPublicBookingRejectsUnpricedActivityWithClearMessage(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
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
	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {"2026-08-10"},
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
	form := url.Values{
		"csrf_token":      {"token"},
		"entry_type":      {"booking"},
		"slot_date":       {"2026-08-10"},
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
	if second.Code != http.StatusConflict {
		t.Fatalf("expected duplicate cancellation request conflict, got %d", second.Code)
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
	report, err := app.buildOperationalReport(period)
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
