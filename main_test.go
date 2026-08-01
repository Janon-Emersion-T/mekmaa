package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	now := time.Now()

	past := SpaceSchedule{
		SlotDate: now.AddDate(0, 0, -1).Format("2006-01-02"),
		SlotHour: "06:00",
	}

	if err := validateBookableScheduleTime(past, now); err == nil {
		t.Fatal("expected a past booking slot to be rejected")
	}
	days := buildBookingWeekDays(nil, now, bookingHours())
	if len(days) != 7 || days[0].IsPast {
		t.Fatalf("expected a forward-looking seven-day calendar, got %#v", days)
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
	if err := app.updateBookingRequestStatus(requestID, "confirmed", ""); err != nil {
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
	if _, err := app.collectBookingPayment(scheduleID, "cash", 0); err == nil {
		t.Fatal("expected pending booking collection to be rejected")
	}
	if err := app.updateBookingRequestStatus(scheduleID, "confirmed", ""); err != nil {
		t.Fatalf("confirm booking: %v", err)
	}
	transactionID, err := app.collectBookingPayment(scheduleID, "card", 0)
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
	if _, err := app.collectBookingPayment(scheduleID, "cash", 0); !errors.Is(err, ErrBookingPaymentAlreadyCollected) {
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
