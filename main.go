package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	sessionCookieName = "mekmaa3_session"
	csrfCookieName    = "mekmaa3_csrf"
	flashCookieName   = "mekmaa3_flash"
	sessionTTL        = 24 * time.Hour
	otpTTL            = 10 * time.Minute
)

var (
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	passwordPattern = regexp.MustCompile(`^.{10,}$`)
	otpPattern      = regexp.MustCompile(`^\d{6}$`)
	allRoles        = []string{"superadmin", "admin", "editor", "customer"}
	allPermissions  = []string{"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage", "student_groups.manage", "attendance.manage", "space_bookings.manage", "booking_requests.manage", "pricing.manage"}
)

type contextKey string

const userContextKey contextKey = "currentUser"

type App struct {
	db           *sql.DB
	templates    map[string]*template.Template
	cookieSecure bool
	smtp         SMTPConfig
	sms          SMSConfig
}

type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	Enabled  bool
}

type SMSConfig struct {
	UserID   string
	APIKey   string
	SenderID string
	Enabled  bool
}

type User struct {
	ID          int64
	Email       string
	Name        string
	Roles       []string
	Permissions []string
	Verified    bool
	CreatedAt   time.Time
}

type Role struct {
	ID          int64
	Name        string
	Permissions []string
	System      bool
}

type Admission struct {
	ID                       int64
	StudentID                string
	FullName                 string
	AdmissionDate            string
	DateOfBirth              string
	Gender                   string
	PracticeType             string
	Address                  string
	PassportNumber           string
	School                   string
	GuardianName             string
	GuardianRelationship     string
	GuardianContactNumber    string
	GuardianAlternativePhone string
	MedicalInformation       string
	CreatedAt                time.Time
}

type StudentGroup struct {
	ID           int64
	Name         string
	Code         string
	Description  string
	Students     []Admission
	StudentCount int
	CreatedAt    time.Time
}

type SpaceSchedule struct {
	ID              int64
	SlotDate        string
	SlotHour        string
	EntryType       string
	Activity        string
	Quantity        int
	Title           string
	Notes           string
	Status          string
	RequesterName   string
	RequesterEmail  string
	RequesterPhone  string
	RequestedByUser int64
	ReviewNote      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type BookingOption struct {
	Activity string
	Quantity int
	Label    string
}

type PricingRule struct {
	ID             int64
	Activity       string
	Quantity       int
	WeekdayOffPeak float64
	WeekdayPeak    float64
	WeekendOffPeak float64
	WeekendPeak    float64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type PricingSettings struct {
	ID            int64
	PeakStartHour string
	PeakEndHour   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type AdmissionPricing struct {
	ID           int64
	PracticeType string
	Price        float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AttendanceRecord struct {
	ID             int64
	GroupID        int64
	AdmissionID    int64
	AttendanceDate string
	Status         string
	Note           string
	RecordedAt     time.Time
	UpdatedAt      time.Time
}

type BookingSlotAvailability struct {
	Hour          string
	Schedules     []SpaceSchedule
	Options       []BookingOption
	BlockedReason string
}

type CalendarDay struct {
	Date          string
	DayLabel      string
	MonthLabel    string
	DayNumber     string
	OpenSlotCount int
	BusySlotCount int
	IsToday       bool
	IsSelected    bool
}

type SportPage struct {
	Slug             string
	Name             string
	Kicker           string
	Summary          string
	ShortDescription string
	Detail           string
	Accent           string
	PrimaryCTA       string
	PrimaryLabel     string
	Highlights       []string
}

type FAQItem struct {
	Question string
	Answer   string
}

type TemplateData struct {
	Title             string
	Description       string
	CurrentPath       string
	User              *User
	Viewer            *User
	HideChrome        bool
	CSRFToken         string
	Flash             string
	Error             string
	Stats             []Stat
	Features          []Feature
	Users             []User
	Available         []string
	Roles             []Role
	Permissions       []string
	Admissions        []Admission
	SelectedAdmission *Admission
	AdmissionMode     string
	StudentGroups     []StudentGroup
	SelectedGroup     *StudentGroup
	GroupMode         string
	AttendanceRecords []AttendanceRecord
	AttendanceDate    string
	RecentDates       []string
	Schedules         []SpaceSchedule
	DaySchedules      []SpaceSchedule
	PendingSchedules  []SpaceSchedule
	SelectedSchedule  *SpaceSchedule
	DraftSchedule     *SpaceSchedule
	ScheduleMode      string
	Pricings          []PricingRule
	AdmissionPricings []AdmissionPricing
	PricingSettings   *PricingSettings
	SelectedPricing   *PricingRule
	PricingMode       string
	BookingSlots      []BookingSlotAvailability
	WeekDays          []CalendarDay
	BookingOptions    []BookingOption
	Activities        []string
	Hours             []string
	CalendarDate      string
	PreviousDate      string
	NextDate          string
	TodayDate         string
	DailyStats        []Stat
	PendingEmail      string
	OTPCodeLength     int
	ResendAction      string
	SportsCatalog     []SportPage
	SelectedSport     *SportPage
	FAQItems          []FAQItem
}

type Stat struct {
	Label string
	Value string
}

type Feature struct {
	Title string
	Body  string
}

var (
	ErrEmailTaken = errors.New("email already exists")
	ErrInvalidOTP = errors.New("invalid verification code")
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	addr := envOrDefault("ADDR", ":8080")
	dbPath := envOrDefault("DB_PATH", "app.db")
	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"

	smtpConfig := SMTPConfig{
		Host:     envOrDefault("SMTP_HOST", "smtp.gmail.com"),
		Port:     envOrDefault("SMTP_PORT", "587"),
		Username: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASS"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
	}
	if smtpConfig.From == "" {
		smtpConfig.From = smtpConfig.Username
	}
	smtpConfig.Enabled = smtpConfig.Username != "" && smtpConfig.Password != "" && smtpConfig.From != ""
	log.Printf("smtp enabled=%t host=%s port=%s from=%s", smtpConfig.Enabled, smtpConfig.Host, smtpConfig.Port, smtpConfig.From)

	smsConfig := SMSConfig{
		UserID:   strings.TrimSpace(os.Getenv("SMSLENZ_USER_ID")),
		APIKey:   strings.TrimSpace(os.Getenv("SMSLENZ_API_KEY")),
		SenderID: strings.TrimSpace(os.Getenv("SMSLENZ_SENDER_ID")),
	}
	smsConfig.Enabled = smsConfig.UserID != "" && smsConfig.APIKey != "" && smsConfig.SenderID != ""
	log.Printf("sms enabled=%t provider=smslenz sender_id=%s", smsConfig.Enabled, smsConfig.SenderID)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	if err := seedRoles(db); err != nil {
		log.Fatalf("seed roles: %v", err)
	}
	if err := bootstrapSuperadmin(db); err != nil {
		log.Fatalf("bootstrap superadmin: %v", err)
	}

	templates, err := buildTemplates()
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	app := &App{
		db:           db,
		templates:    templates,
		cookieSecure: cookieSecure,
		smtp:         smtpConfig,
		sms:          smsConfig,
	}

	mux := http.NewServeMux()
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir("static/images"))))
	mux.HandleFunc("/", app.homeHandler)
	mux.HandleFunc("/about", app.aboutHandler)
	mux.HandleFunc("/book", app.publicBookingHandler)
	mux.HandleFunc("/book/request", app.publicBookingRequestHandler)
	mux.HandleFunc("/booking", app.legacyBookingRedirectHandler)
	mux.HandleFunc("/contact", app.contactHandler)
	mux.HandleFunc("/faq", app.faqHandler)
	mux.HandleFunc("/gallery", app.galleryHandler)
	mux.HandleFunc("/coaching", app.coachingHandler)
	mux.HandleFunc("/coaching/", app.legacyCoachingRedirectHandler)
	mux.HandleFunc("/privacy-policy", app.privacyPolicyHandler)
	mux.HandleFunc("/refund-policy", app.refundPolicyHandler)
	mux.HandleFunc("/register", app.registerHandler)
	mux.HandleFunc("/login", app.loginHandler)
	mux.HandleFunc("/sports", app.sportsHandler)
	mux.HandleFunc("/sports/", app.sportDetailHandler)
	mux.HandleFunc("/terms-and-conditions", app.termsHandler)
	mux.HandleFunc("/verify-email", app.verifyEmailHandler)
	mux.HandleFunc("/verify-email/resend", app.resendVerificationHandler)
	mux.HandleFunc("/logout", app.logoutHandler)
	mux.Handle("/dashboard", app.sessionMiddleware(http.HandlerFunc(app.dashboardHandler)))
	mux.Handle("/editor", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.editorHandler), "editor", "admin", "superadmin")))
	mux.Handle("/admin", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.adminRedirectHandler), "admin", "superadmin")))
	mux.Handle("/admin/users", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.userManagementHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.roleManagementHandler), "admin", "superadmin")))
	mux.Handle("/admin/users/create", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.createManagedUserHandler), "admin", "superadmin")))
	mux.Handle("/admin/users/roles", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.updateRolesHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/create", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.createRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/update", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.updateRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/delete", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.deleteRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/admissions", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.admissionManagementHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/student-groups", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentGroupManagementHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/attendance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.attendanceManagementHandler), "attendance.manage")))
	mux.Handle("/admin/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveAttendanceHandler), "attendance.manage")))
	mux.Handle("/admin/bookings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingManagementHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/booking-requests", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingRequestsHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/confirm", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.confirmBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/reject", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.rejectBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/pricing", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.pricingManagementHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createPricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updatePricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deletePricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/settings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updatePricingSettingsHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/admissions/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveAdmissionPricingHandler), "pricing.manage")))

	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, app.securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := a.newTemplateData(w, r, nil)
	data.Title = "Mekmaa | Indoor Sports and Coaching in Jaffna"
	data.Description = "Book cricket nets, futsal, badminton, table tennis and tennis at Mekmaa in Jaffna, with coaching programmes for kids, teens and adults."

	a.render(w, "home", data, http.StatusOK)
}

func (a *App) aboutHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/about" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "About Mekmaa"
	data.Description = "Learn more about Mekmaa."
	a.render(w, "about", data, http.StatusOK)
}

func (a *App) publicBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/book" {
		http.NotFound(w, r)
		return
	}

	viewer := a.optionalUser(r)
	data, err := a.buildPublicBookingData(w, r, viewer)
	if err != nil {
		log.Printf("build public booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.render(w, "book", data, http.StatusOK)
}

func (a *App) contactHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/contact" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data := a.newTemplateData(w, r, nil)
		data.Title = "Contact Mekmaa"
		data.Description = "Contact Mekmaa."
		a.render(w, "contact", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form submission", http.StatusBadRequest)
			return
		}
		a.setFlash(w, "Your message has been received.")
		http.Redirect(w, r, "/contact", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) sportsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/sports" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Sports at Mekmaa"
	data.Description = "Explore cricket nets, futsal, badminton, table tennis and tennis at Mekmaa in Jaffna."
	data.SportsCatalog = sportsCatalog()
	a.render(w, "sports", data, http.StatusOK)
}

func (a *App) sportDetailHandler(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/sports/")
	if slug == "" || strings.Contains(slug, "/") {
		http.NotFound(w, r)
		return
	}

	sport, ok := sportBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := a.newTemplateData(w, r, nil)
	data.Title = sport.Name + " at Mekmaa"
	data.Description = sport.Summary
	data.SportsCatalog = sportsCatalog()
	data.SelectedSport = &sport
	a.render(w, "sports", data, http.StatusOK)
}

func (a *App) coachingHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/coaching" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Coaching"
	data.Description = "Explore Mekmaa coaching programs."
	a.render(w, "coaching", data, http.StatusOK)
}

func (a *App) galleryHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/gallery" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Gallery"
	data.Description = "A look at the Mekmaa brand, indoor sports atmosphere and coaching culture."
	a.render(w, "gallery", data, http.StatusOK)
}

func (a *App) faqHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/faq" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Frequently Asked Questions"
	data.Description = "Answers to common questions about bookings, coaching and indoor sports at Mekmaa."
	data.FAQItems = homeFAQItems()
	a.render(w, "faq", data, http.StatusOK)
}

func (a *App) privacyPolicyHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/privacy-policy" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Privacy Policy"
	data.Description = "How Mekmaa handles personal information submitted through bookings, contact forms and account access."
	a.render(w, "privacy-policy", data, http.StatusOK)
}

func (a *App) termsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/terms-and-conditions" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Terms and Conditions"
	data.Description = "Terms and conditions for using the Mekmaa website, facilities and coaching services."
	a.render(w, "terms-and-conditions", data, http.StatusOK)
}

func (a *App) refundPolicyHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/refund-policy" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Booking and Refund Policy"
	data.Description = "Booking, cancellation and refund expectations for sessions reserved with Mekmaa."
	a.render(w, "refund-policy", data, http.StatusOK)
}

func (a *App) legacyBookingRedirectHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/book", http.StatusMovedPermanently)
}

func (a *App) legacyCoachingRedirectHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/coaching", http.StatusMovedPermanently)
}

func (a *App) publicBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		a.writePublicBookingError(w, r, nil, "Invalid session token. Refresh and try again.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.writePublicBookingError(w, r, nil, "Invalid form submission.", http.StatusBadRequest)
		return
	}

	viewer := a.optionalUser(r)
	schedule := scheduleFromRequest(r)
	schedule.EntryType = "booking"
	schedule.Status = "pending"
	schedule.RequesterName = strings.TrimSpace(r.FormValue("requester_name"))
	schedule.RequesterEmail = strings.ToLower(strings.TrimSpace(r.FormValue("requester_email")))
	schedule.RequesterPhone = strings.TrimSpace(r.FormValue("requester_phone"))
	if viewer != nil {
		schedule.RequestedByUser = viewer.ID
		if schedule.RequesterName == "" {
			schedule.RequesterName = viewer.Name
		}
		if schedule.RequesterEmail == "" {
			schedule.RequesterEmail = viewer.Email
		}
	}

	if schedule.RequesterName == "" || !emailPattern.MatchString(schedule.RequesterEmail) {
		a.writePublicBookingError(w, r, &schedule, "Name and a valid email are required.", http.StatusBadRequest)
		return
	}
	if schedule.RequesterPhone == "" {
		a.writePublicBookingError(w, r, &schedule, "Contact number is required.", http.StatusBadRequest)
		return
	}
	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writePublicBookingError(w, r, &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createPublicBookingRequest(schedule); err != nil {
		a.writePublicBookingError(w, r, &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Booking request sent. We will review it soon.")
	http.Redirect(w, r, "/book?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) registerHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user := a.optionalUser(r)
		if user != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Create account"
		data.Description = "Register a new account."
		data.HideChrome = true
		a.render(w, "register", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeFormError(w, r, "register", "Create account", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeFormError(w, r, "register", "Create account", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")
		passwordConfirm := r.FormValue("password_confirm")

		if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
			a.writeFormError(w, r, "register", "Create account", "Use a valid email and a password with at least 10 characters.", http.StatusBadRequest)
			return
		}
		if password != passwordConfirm {
			a.writeFormError(w, r, "register", "Create account", "Passwords do not match.", http.StatusBadRequest)
			return
		}

		user, err := a.createUser(name, email, password)
		if err != nil {
			if errors.Is(err, ErrEmailTaken) {
				a.writeFormError(w, r, "register", "Create account", "That email is already registered.", http.StatusConflict)
				return
			}
			log.Printf("create user: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		otp, err := a.issueVerificationCode(user.ID)
		if err != nil {
			log.Printf("issue verification code: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.sendVerificationEmail(user, otp); err != nil {
			log.Printf("send verification email: %v", err)
			a.setFlash(w, "Account created, but the verification email could not be sent automatically. Configure SMTP, then resend the code on the next screen.")
		} else {
			a.setFlash(w, "Account created. Enter the 6-digit code we sent to your email.")
		}
		http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user := a.optionalUser(r)
		if user != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Sign in"
		data.Description = "Access your account."
		data.HideChrome = true
		a.render(w, "login", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeFormError(w, r, "login", "Sign in", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeFormError(w, r, "login", "Sign in", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")

		user, passwordHash, err := a.findUserByEmail(email)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
			a.writeFormError(w, r, "login", "Sign in", "Invalid email or password.", http.StatusUnauthorized)
			return
		}

		if !user.Verified {
			otp, err := a.issueVerificationCode(user.ID)
			if err != nil {
				log.Printf("issue verification code: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if err := a.sendVerificationEmail(user, otp); err != nil {
				log.Printf("send verification email: %v", err)
				a.setFlash(w, "Your account is not verified. Configure SMTP if needed, then resend the code on the next screen.")
			} else {
				a.setFlash(w, "Your account is not verified. Enter the 6-digit code we sent to your email.")
			}
			http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
			return
		}

		if err := a.deleteSessionsForUser(user.ID); err != nil {
			log.Printf("delete old sessions: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.createSession(w, user.ID); err != nil {
			log.Printf("create session: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		a.setFlash(w, "Signed in successfully.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) verifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if user := a.optionalUser(r); user != nil && user.Verified {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, nil)
		data.Title = "Verify your email"
		data.Description = "Confirm your email with a 6-digit code."
		data.HideChrome = true
		data.PendingEmail = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
		data.ResendAction = "/verify-email/resend"
		a.render(w, "verify-email", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeVerificationError(w, r, "", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeVerificationError(w, r, "", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		otp := strings.TrimSpace(r.FormValue("otp"))
		if !emailPattern.MatchString(email) || !otpPattern.MatchString(otp) {
			a.writeVerificationError(w, r, email, "Enter the 6-digit verification code.", http.StatusBadRequest)
			return
		}

		user, _, err := a.findUserByEmail(email)
		if err != nil {
			a.writeVerificationError(w, r, email, "Invalid verification attempt.", http.StatusBadRequest)
			return
		}
		if user.Verified {
			a.setFlash(w, "Your email is already verified. Sign in to continue.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if err := a.consumeVerificationCode(user.ID, otp); err != nil {
			if errors.Is(err, ErrInvalidOTP) {
				a.writeVerificationError(w, r, email, "The verification code is invalid or expired.", http.StatusBadRequest)
				return
			}
			log.Printf("consume verification code: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if err := a.deleteSessionsForUser(user.ID); err != nil {
			log.Printf("delete old sessions: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.createSession(w, user.ID); err != nil {
			log.Printf("create session: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		a.setFlash(w, "Email verified. You are now signed in.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) resendVerificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if emailPattern.MatchString(email) {
		if user, _, err := a.findUserByEmail(email); err == nil && !user.Verified {
			otp, err := a.issueVerificationCode(user.ID)
			if err != nil {
				log.Printf("issue verification code: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if err := a.sendVerificationEmail(user, otp); err != nil {
				log.Printf("send verification email: %v", err)
				a.setFlash(w, "A new code was generated, but email delivery is not configured correctly yet.")
			} else {
				a.setFlash(w, "A new verification code has been sent.")
			}
		} else {
			a.setFlash(w, "If the account exists and still needs verification, a new code has been sent.")
		}
	} else {
		a.setFlash(w, "If the account exists and still needs verification, a new code has been sent.")
	}

	http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(email), http.StatusSeeOther)
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.deleteSessionByToken(cookie.Value)
	}

	a.clearCookie(w, sessionCookieName)
	a.clearCookieWithOptions(w, csrfCookieName, false)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = "Dashboard"
	data.Description = "Authenticated user dashboard."
	data.Stats = []Stat{
		{Value: strconv.FormatInt(user.ID, 10), Label: "User ID"},
		{Value: strings.Join(user.Roles, ", "), Label: "Assigned roles"},
		{Value: verifiedLabel(user.Verified), Label: "Email status"},
	}
	a.render(w, "dashboard", data, http.StatusOK)
}

func verifiedLabel(verified bool) string {
	if verified {
		return "Verified"
	}
	return "Pending"
}

func (a *App) editorHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = "Editor Area"
	data.Description = "Editor and admin access only."
	a.render(w, "editor", data, http.StatusOK)
}

func (a *App) adminRedirectHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *App) userManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	users, err := a.listUsers()
	if err != nil {
		log.Printf("list users: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	roles, err := a.listRoles()
	if err != nil {
		log.Printf("list roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "User Management"
	data.Description = "Manage users."
	data.Users = users
	data.Available = allRoles
	data.Roles = roles
	a.render(w, "user-management", data, http.StatusOK)
}

func (a *App) roleManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	roles, err := a.listRoles()
	if err != nil {
		log.Printf("list roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Role Management"
	data.Description = "Manage roles."
	data.Roles = roles
	data.Permissions = allPermissions
	a.render(w, "role-management", data, http.StatusOK)
}

func (a *App) admissionManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Admissions Management"
	data.Description = "Manage admissions."
	data.Admissions = admissions
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.AdmissionMode = mode
	}
	if data.AdmissionMode == "view" || data.AdmissionMode == "edit" {
		admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && admissionID > 0 {
			selectedAdmission, err := a.findAdmissionByID(admissionID)
			if err == nil {
				data.SelectedAdmission = selectedAdmission
			}
		}
	}
	a.render(w, "admission-management", data, http.StatusOK)
}

func (a *App) studentGroupManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	groups, err := a.listStudentGroups()
	if err != nil {
		log.Printf("list student groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions for groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Groups"
	data.Description = "Manage student groups."
	data.StudentGroups = groups
	data.Admissions = admissions
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.GroupMode = mode
	}
	if data.GroupMode == "view" || data.GroupMode == "edit" {
		groupID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && groupID > 0 {
			selectedGroup, err := a.findStudentGroupByID(groupID)
			if err == nil {
				data.SelectedGroup = selectedGroup
			}
		}
	}
	a.render(w, "student-group-management", data, http.StatusOK)
}

func (a *App) attendanceManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	groups, err := a.listStudentGroups()
	if err != nil {
		log.Printf("list student groups for attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Attendance"
	data.Description = "Manage student attendance by group."
	data.StudentGroups = groups
	data.AttendanceDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.AttendanceDate == "" {
		data.AttendanceDate = time.Now().Format("2006-01-02")
	}

	groupID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("group_id")), 10, 64)
	if err == nil && groupID > 0 {
		selectedGroup, err := a.findStudentGroupByID(groupID)
		if err == nil {
			data.SelectedGroup = selectedGroup
			records, err := a.listAttendanceRecords(groupID, data.AttendanceDate)
			if err == nil {
				data.AttendanceRecords = records
			}
			recentDates, err := a.listRecentAttendanceDates(groupID, 8)
			if err == nil {
				data.RecentDates = recentDates
			}
		}
	}

	a.render(w, "attendance-management", data, http.StatusOK)
}

func (a *App) bookingManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.ScheduleMode = mode
	}
	if data.ScheduleMode == "view" || data.ScheduleMode == "edit" {
		scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && scheduleID > 0 {
			selectedSchedule, err := a.findSpaceScheduleByID(scheduleID)
			if err == nil {
				data.SelectedSchedule = selectedSchedule
			}
		}
	}
	a.render(w, "booking-management", data, http.StatusOK)
}

func (a *App) bookingRequestsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "Booking Requests"
	data.Description = "Review pending booking requests."
	a.render(w, "booking-requests", data, http.StatusOK)
}

func (a *App) pricingManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	pricings, err := a.listPricingRules()
	if err != nil {
		log.Printf("list pricing rules: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		log.Printf("get pricing settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	admissionPricings, err := a.listAdmissionPricings()
	if err != nil {
		log.Printf("list admission pricing: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Pricing"
	data.Description = "Manage booking pricing."
	data.Pricings = pricings
	data.AdmissionPricings = admissionPricings
	data.PricingSettings = settings
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.PricingMode = mode
	}
	if data.PricingMode == "view" || data.PricingMode == "edit" {
		pricingID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && pricingID > 0 {
			selectedPricing, err := a.findPricingRuleByID(pricingID)
			if err == nil {
				data.SelectedPricing = selectedPricing
			}
		}
	}
	a.render(w, "pricing-management", data, http.StatusOK)
}

func (a *App) updatePricingSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	settings := PricingSettings{
		PeakStartHour: strings.TrimSpace(r.FormValue("peak_start_hour")),
		PeakEndHour:   strings.TrimSpace(r.FormValue("peak_end_hour")),
	}
	if err := validatePricingSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updatePricingSettings(settings); err != nil {
		log.Printf("update pricing settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing settings updated.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) saveAdmissionPricingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	groupPrice, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("group_practice_price")), 64)
	if err != nil || groupPrice < 0 {
		http.Error(w, "valid group practice price is required", http.StatusBadRequest)
		return
	}
	oneToOnePrice, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("one_to_one_practice_price")), 64)
	if err != nil || oneToOnePrice < 0 {
		http.Error(w, "valid one to one practice price is required", http.StatusBadRequest)
		return
	}

	pricings := []AdmissionPricing{
		{PracticeType: "group_practice", Price: groupPrice},
		{PracticeType: "one_to_one_practice", Price: oneToOnePrice},
	}
	if err := a.saveAdmissionPricings(pricings); err != nil {
		log.Printf("save admission pricing: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission pricing updated.")
	http.Redirect(w, r, "/admin/pricing#admission-pricing", http.StatusSeeOther)
}

func (a *App) confirmBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	if err := a.updateBookingRequestStatus(scheduleID, "confirmed", ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		log.Printf("find confirmed booking request: %v", err)
		a.setFlash(w, "Booking request confirmed.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	if err := a.sendBookingConfirmationSMS(schedule); err != nil {
		log.Printf("send booking confirmation sms: %v", err)
		a.setFlash(w, "Booking request confirmed, but SMS delivery failed or is not configured.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Booking request confirmed and SMS sent.")
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
}

func (a *App) rejectBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))
	if err := a.updateBookingRequestStatus(scheduleID, "rejected", reviewNote); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setFlash(w, "Booking request rejected.")
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
}

func (a *App) createPricingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	pricing, err := pricingRuleFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validatePricingRule(pricing); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createPricingRule(pricing); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "pricing already exists for that option", http.StatusConflict)
			return
		}
		log.Printf("create pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing created.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) updatePricingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	pricingID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("pricing_id")), 10, 64)
	if err != nil || pricingID <= 0 {
		http.Error(w, "invalid pricing id", http.StatusBadRequest)
		return
	}
	pricing, err := pricingRuleFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pricing.ID = pricingID
	if err := validatePricingRule(pricing); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updatePricingRule(pricing); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "pricing already exists for that option", http.StatusConflict)
			return
		}
		log.Printf("update pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing updated.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) deletePricingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	pricingID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("pricing_id")), 10, 64)
	if err != nil || pricingID <= 0 {
		http.Error(w, "invalid pricing id", http.StatusBadRequest)
		return
	}
	if err := a.deletePricingRule(pricingID); err != nil {
		log.Printf("delete pricing rule: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Pricing deleted.")
	http.Redirect(w, r, "/admin/pricing", http.StatusSeeOther)
}

func (a *App) buildBookingTemplateData(w http.ResponseWriter, r *http.Request, user *User) (TemplateData, error) {
	schedules, err := a.listSpaceSchedules()
	if err != nil {
		return TemplateData{}, err
	}
	pending, err := a.listPendingSpaceSchedules()
	if err != nil {
		return TemplateData{}, err
	}
	pricings, err := a.listPricingRules()
	if err != nil {
		return TemplateData{}, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Booking Manager"
	data.Description = "Manage bookings and training sessions."
	data.Schedules = schedules
	data.PendingSchedules = pending
	data.Pricings = pricings
	data.PricingSettings = settings
	data.Activities = bookingActivities()
	data.Hours = bookingHours()
	data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.CalendarDate == "" {
		data.CalendarDate = time.Now().Format("2006-01-02")
	}
	selectedDate, err := time.Parse("2006-01-02", data.CalendarDate)
	if err != nil {
		selectedDate = time.Now()
		data.CalendarDate = selectedDate.Format("2006-01-02")
	}
	data.PreviousDate = selectedDate.AddDate(0, 0, -1).Format("2006-01-02")
	data.NextDate = selectedDate.AddDate(0, 0, 1).Format("2006-01-02")
	data.DaySchedules = schedulesForDate(schedules, data.CalendarDate)
	data.DailyStats = buildDailyBookingStats(data.DaySchedules, data.Hours)
	return data, nil
}

func (a *App) buildPublicBookingData(w http.ResponseWriter, r *http.Request, viewer *User) (TemplateData, error) {
	schedules, err := a.listActiveSpaceSchedules()
	if err != nil {
		return TemplateData{}, err
	}
	pricings, err := a.listPricingRules()
	if err != nil {
		return TemplateData{}, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return TemplateData{}, err
	}
	data := a.newTemplateData(w, r, nil)
	data.Viewer = viewer
	data.Title = "Book a Slot"
	data.Description = "Check availability and request a booking."
	data.Schedules = schedules
	data.Pricings = pricings
	data.PricingSettings = settings
	data.Activities = bookingActivities()
	data.Hours = bookingHours()
	data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.CalendarDate == "" {
		data.CalendarDate = time.Now().Format("2006-01-02")
	}
	selectedDate, err := time.Parse("2006-01-02", data.CalendarDate)
	if err != nil {
		selectedDate = time.Now()
		data.CalendarDate = selectedDate.Format("2006-01-02")
	}
	data.TodayDate = time.Now().Format("2006-01-02")
	data.PreviousDate = selectedDate.AddDate(0, 0, -1).Format("2006-01-02")
	data.NextDate = selectedDate.AddDate(0, 0, 1).Format("2006-01-02")
	data.BookingSlots = buildBookingSlotAvailability(schedules, data.CalendarDate, data.Hours)
	data.WeekDays = buildBookingWeekDays(schedules, selectedDate, data.Hours)
	data.DraftSchedule = prefillPublicBookingDraft(r, viewer, data.CalendarDate)
	return data, nil
}

func (a *App) writePublicBookingError(w http.ResponseWriter, r *http.Request, draft *SpaceSchedule, message string, status int) {
	viewer := a.optionalUser(r)
	data, err := a.buildPublicBookingData(w, r, viewer)
	if err != nil {
		log.Printf("build public booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Error = message
	data.DraftSchedule = draft
	a.render(w, "book", data, status)
}

func (a *App) writeBookingError(w http.ResponseWriter, r *http.Request, mode string, selected *SpaceSchedule, message string, status int) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Error = message
	data.ScheduleMode = mode
	if mode == "edit" {
		data.SelectedSchedule = selected
	} else if mode == "new" {
		data.DraftSchedule = selected
	}
	a.render(w, "booking-management", data, status)
}

func (a *App) createManagedUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	roles := normalizeRoles(r.Form["roles"])
	verified := r.FormValue("verified") == "true"

	if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
		http.Error(w, "invalid user fields", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	if _, err := a.createManagedUser(name, email, password, roles, verified); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		log.Printf("create managed user: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "User created.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *App) createRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if name == "" {
		http.Error(w, "role name is required", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	if err := a.createRole(name, permissions); err != nil {
		log.Printf("create role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role created.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if name == "" {
		http.Error(w, "role name is required", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}

	if err := a.updateRole(roleID, name, permissions); err != nil {
		log.Printf("update role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role updated.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) deleteRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	if err := a.deleteRole(roleID); err != nil {
		log.Printf("delete role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role deleted.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRolesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	targetID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	roles := normalizeRoles(r.Form["roles"])
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	current, _ := a.currentUser(r.Context())
	if current != nil && current.ID == targetID && containsRole(current.Roles, "superadmin") && !containsRole(roles, "superadmin") {
		http.Error(w, "you cannot remove your own superadmin access", http.StatusBadRequest)
		return
	}
	if current != nil && current.ID == targetID && containsRole(current.Roles, "admin") && !containsRole(roles, "admin") && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "you cannot remove your own admin access", http.StatusBadRequest)
		return
	}

	if err := a.replaceUserRoles(targetID, roles); err != nil {
		log.Printf("replace roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	redirectTo := r.FormValue("return_to")
	if redirectTo != "/admin/users" && redirectTo != "/admin/roles" {
		redirectTo = "/admin/users"
	}
	a.setFlash(w, "Roles updated.")
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (a *App) createAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admission := admissionFromRequest(r)
	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createAdmission(admission); err != nil {
		log.Printf("create admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission created.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) updateAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}

	admission := admissionFromRequest(r)
	admission.ID = admissionID
	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateAdmission(admission); err != nil {
		log.Printf("update admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission updated.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) deleteAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}
	if err := a.deleteAdmission(admissionID); err != nil {
		log.Printf("delete admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission deleted.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) createStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	group := studentGroupFromRequest(r)
	admissionIDs := normalizeAdmissionIDs(r.Form["admission_ids"])
	if err := validateStudentGroup(group); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createStudentGroup(group, admissionIDs); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "group code already exists", http.StatusConflict)
			return
		}
		log.Printf("create student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group created.")
	http.Redirect(w, r, "/admin/student-groups", http.StatusSeeOther)
}

func (a *App) updateStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		http.Error(w, "invalid group id", http.StatusBadRequest)
		return
	}

	group := studentGroupFromRequest(r)
	group.ID = groupID
	admissionIDs := normalizeAdmissionIDs(r.Form["admission_ids"])
	if err := validateStudentGroup(group); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateStudentGroup(group, admissionIDs); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "group code already exists", http.StatusConflict)
			return
		}
		log.Printf("update student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group updated.")
	http.Redirect(w, r, "/admin/student-groups", http.StatusSeeOther)
}

func (a *App) deleteStudentGroupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		http.Error(w, "invalid group id", http.StatusBadRequest)
		return
	}
	if err := a.deleteStudentGroup(groupID); err != nil {
		log.Printf("delete student group: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Student group deleted.")
	http.Redirect(w, r, "/admin/student-groups", http.StatusSeeOther)
}

func (a *App) saveAttendanceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	groupID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("group_id")), 10, 64)
	if err != nil || groupID <= 0 {
		http.Error(w, "invalid group id", http.StatusBadRequest)
		return
	}
	attendanceDate := strings.TrimSpace(r.FormValue("attendance_date"))
	if _, err := time.Parse("2006-01-02", attendanceDate); err != nil {
		http.Error(w, "invalid attendance date", http.StatusBadRequest)
		return
	}

	group, err := a.findStudentGroupByID(groupID)
	if err != nil {
		http.Error(w, "group not found", http.StatusBadRequest)
		return
	}

	records := make([]AttendanceRecord, 0, len(group.Students))
	for _, student := range group.Students {
		status := normalizeAttendanceStatus(r.FormValue(fmt.Sprintf("status_%d", student.ID)))
		note := strings.TrimSpace(r.FormValue(fmt.Sprintf("note_%d", student.ID)))
		records = append(records, AttendanceRecord{
			GroupID:        groupID,
			AdmissionID:    student.ID,
			AttendanceDate: attendanceDate,
			Status:         status,
			Note:           note,
		})
	}

	if err := a.replaceAttendanceRecords(groupID, attendanceDate, records); err != nil {
		log.Printf("save attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Attendance saved.")
	http.Redirect(w, r, "/admin/attendance?group_id="+strconv.FormatInt(groupID, 10)+"&date="+url.QueryEscape(attendanceDate), http.StatusSeeOther)
}

func (a *App) createBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		a.writeBookingError(w, r, "new", nil, "Invalid session token. Refresh and try again.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.writeBookingError(w, r, "new", nil, "Invalid form submission.", http.StatusBadRequest)
		return
	}

	schedule := scheduleFromRequest(r)
	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createSpaceSchedule(schedule); err != nil {
		log.Printf("create booking: %v", err)
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule created.")
	http.Redirect(w, r, "/admin/bookings", http.StatusSeeOther)
}

func (a *App) updateBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		a.writeBookingError(w, r, "edit", nil, "Invalid session token. Refresh and try again.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.writeBookingError(w, r, "edit", nil, "Invalid form submission.", http.StatusBadRequest)
		return
	}

	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		a.writeBookingError(w, r, "edit", nil, "Invalid schedule id.", http.StatusBadRequest)
		return
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateSpaceSchedule(schedule); err != nil {
		log.Printf("update booking: %v", err)
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule updated.")
	http.Redirect(w, r, "/admin/bookings", http.StatusSeeOther)
}

func (a *App) deleteBookingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	scheduleID, err := strconv.ParseInt(r.FormValue("schedule_id"), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	if err := a.deleteSpaceSchedule(scheduleID); err != nil {
		log.Printf("delete booking: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Schedule deleted.")
	http.Redirect(w, r, "/admin/bookings", http.StatusSeeOther)
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.tailwindcss.com; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			a.setFlash(w, "Sign in to continue.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := a.userFromSessionToken(cookie.Value)
		if err != nil {
			a.clearCookie(w, sessionCookieName)
			a.clearCookieWithOptions(w, csrfCookieName, false)
			a.setFlash(w, "Your session has expired. Sign in again.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !user.Verified {
			a.clearCookie(w, sessionCookieName)
			a.clearCookieWithOptions(w, csrfCookieName, false)
			a.setFlash(w, "Verify your email to continue.")
			http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) requireRoles(next http.Handler, roles ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.currentUser(r.Context())
		if !ok || !userHasAnyRole(user, roles...) {
			data := a.newTemplateData(w, r, user)
			data.Title = "Forbidden"
			data.Description = "You do not have permission to view this page."
			data.Error = "You do not have permission to view this page."
			a.render(w, "forbidden", data, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *App) requirePermission(next http.Handler, permission string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.currentUser(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		permissions, err := a.permissionsForUser(user.ID)
		if err != nil {
			log.Printf("permissions for user: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !containsPermission(permissions, permission) {
			data := a.newTemplateData(w, r, user)
			data.Title = "Forbidden"
			data.Description = "You do not have permission to view this page."
			data.Error = "You do not have permission to view this page."
			a.render(w, "forbidden", data, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *App) currentUser(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(userContextKey).(*User)
	return user, ok
}

func (a *App) optionalUser(r *http.Request) *User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	user, err := a.userFromSessionToken(cookie.Value)
	if err != nil {
		return nil
	}
	return user
}

func (a *App) render(w http.ResponseWriter, name string, data TemplateData, status int) {
	tmpl, ok := a.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data.Flash != "" {
		a.clearCookieWithOptions(w, flashCookieName, true)
	}
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (a *App) newTemplateData(w http.ResponseWriter, r *http.Request, user *User) TemplateData {
	csrfToken := a.ensureCSRFCookie(w, r)
	return TemplateData{
		CurrentPath:   r.URL.Path,
		User:          user,
		CSRFToken:     csrfToken,
		Flash:         a.consumeFlash(r),
		OTPCodeLength: 6,
	}
}

func (a *App) writeFormError(w http.ResponseWriter, r *http.Request, tmplName, title, message string, status int) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = title
	data.Description = title
	if tmplName == "login" || tmplName == "register" || tmplName == "verify-email" {
		data.HideChrome = true
	}
	data.Error = message
	if tmplName == "verify-email" {
		data.ResendAction = "/verify-email/resend"
	}
	a.render(w, tmplName, data, status)
}

func (a *App) writeVerificationError(w http.ResponseWriter, r *http.Request, email, message string, status int) {
	data := a.newTemplateData(w, r, nil)
	data.Title = "Verify your email"
	data.Description = "Confirm your email with a 6-digit code."
	data.HideChrome = true
	data.PendingEmail = email
	data.ResendAction = "/verify-email/resend"
	data.Error = message
	a.render(w, "verify-email", data, status)
}

func (a *App) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	token, err := generateToken(24)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
		Expires:  time.Now().UTC().Add(sessionTTL),
	})
	return token
}

func (a *App) verifyCSRF(r *http.Request) error {
	formToken := r.FormValue("csrf_token")
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" || formToken == "" || cookie.Value != formToken {
		return errors.New("csrf verification failed")
	}
	return nil
}

func (a *App) createSession(w http.ResponseWriter, userID int64) error {
	rawToken, err := generateToken(32)
	if err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(rawToken))
	expiresAt := time.Now().UTC().Add(sessionTTL)
	if _, err := a.db.Exec(`
		INSERT INTO sessions (user_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
	`, userID, fmt.Sprintf("%x", hash[:]), expiresAt, time.Now().UTC()); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	csrfToken, err := generateToken(24)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	return nil
}

func (a *App) userFromSessionToken(token string) (*User, error) {
	hash := sha256.Sum256([]byte(token))
	row := a.db.QueryRow(`
		SELECT u.id, u.email, u.name, u.email_verified_at, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?
	`, fmt.Sprintf("%x", hash[:]), time.Now().UTC())

	var user User
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, err
	}
	user.Verified = verifiedAt.Valid

	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Permissions = permissions
	return &user, nil
}

func (a *App) createUser(name, email, password string) (*User, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existingUsers int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&existingUsers); err != nil {
		return nil, err
	}

	result, err := tx.Exec(`
		INSERT INTO users (email, name, password_hash, created_at)
		VALUES (?, ?, ?, ?)
	`, email, name, string(passwordHash), time.Now().UTC())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	rolesToAssign := []string{"customer"}
	for _, role := range rolesToAssign {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	roles, err := a.rolesForUser(userID)
	if err != nil {
		return nil, err
	}
	permissions, err := a.permissionsForUser(userID)
	if err != nil {
		return nil, err
	}
	return &User{
		ID:          userID,
		Email:       email,
		Name:        name,
		Roles:       roles,
		Permissions: permissions,
		Verified:    false,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (a *App) createManagedUser(name, email, password string, roles []string, verified bool) (*User, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var verifiedAt any
	if verified {
		verifiedAt = time.Now().UTC()
	}

	result, err := tx.Exec(`
		INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
		VALUES (?, ?, ?, ?, ?)
	`, email, name, string(passwordHash), time.Now().UTC(), verifiedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.findUserByID(userID)
}

func (a *App) findUserByEmail(email string) (*User, string, error) {
	row := a.db.QueryRow(`
		SELECT id, email, name, password_hash, email_verified_at, created_at
		FROM users
		WHERE email = ?
	`, email)

	var user User
	var passwordHash string
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &passwordHash, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, "", err
	}
	user.Verified = verifiedAt.Valid

	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, "", err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, "", err
	}
	user.Permissions = permissions
	return &user, passwordHash, nil
}

func (a *App) findUserByID(userID int64) (*User, error) {
	row := a.db.QueryRow(`
		SELECT id, email, name, email_verified_at, created_at
		FROM users
		WHERE id = ?
	`, userID)

	var user User
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, err
	}
	user.Verified = verifiedAt.Valid
	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Permissions = permissions
	return &user, nil
}

func (a *App) listUsers() ([]User, error) {
	rows, err := a.db.Query(`
		SELECT id, email, name, email_verified_at, created_at
		FROM users
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var verifiedAt sql.NullTime
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
			return nil, err
		}
		user.Verified = verifiedAt.Valid
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range users {
		roles, err := a.rolesForUser(users[i].ID)
		if err != nil {
			return nil, err
		}
		users[i].Roles = roles
	}

	return users, nil
}

func (a *App) rolesForUser(userID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT r.name
		FROM roles r
		JOIN user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = ?
		ORDER BY r.name ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (a *App) listRoles() ([]Role, error) {
	rows, err := a.db.Query(`SELECT id, name FROM roles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name); err != nil {
			return nil, err
		}
		role.System = isSystemRole(role.Name)
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range roles {
		permissions, err := a.permissionsForRole(roles[i].ID)
		if err != nil {
			return nil, err
		}
		roles[i].Permissions = permissions
	}
	return roles, nil
}

func (a *App) permissionsForUser(userID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT DISTINCT rp.permission
		FROM role_permissions rp
		JOIN user_roles ur ON ur.role_id = rp.role_id
		WHERE ur.user_id = ?
		ORDER BY rp.permission ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (a *App) permissionsForRole(roleID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT permission
		FROM role_permissions
		WHERE role_id = ?
		ORDER BY permission ASC
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (a *App) createRole(name string, permissions []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`INSERT INTO roles (name) VALUES (?)`, name)
	if err != nil {
		return err
	}
	roleID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, permission := range permissions {
		if _, err := tx.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) updateRole(roleID int64, name string, permissions []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentName string
	if err := tx.QueryRow(`SELECT name FROM roles WHERE id = ?`, roleID).Scan(&currentName); err != nil {
		return err
	}
	if isSystemRole(currentName) {
		name = currentName
	}

	if _, err := tx.Exec(`UPDATE roles SET name = ? WHERE id = ?`, name, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	for _, permission := range permissions {
		if _, err := tx.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) deleteRole(roleID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var roleName string
	if err := tx.QueryRow(`SELECT name FROM roles WHERE id = ?`, roleID).Scan(&roleName); err != nil {
		return err
	}
	if isSystemRole(roleName) {
		return errors.New("system role cannot be deleted")
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_roles WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM roles WHERE id = ?`, roleID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) replaceUserRoles(userID int64, roles []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, role := range roles {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deleteSessionsForUser(userID int64) error {
	_, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (a *App) listAdmissions() ([]Admission, error) {
	rows, err := a.db.Query(`
		SELECT id, student_id, full_name, admission_date, date_of_birth, gender, practice_type, address, passport_number, school,
		       guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
		       medical_information, created_at
		FROM admissions
		ORDER BY admission_date DESC, created_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admissions []Admission
	for rows.Next() {
		var admission Admission
		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.PracticeType,
			&admission.Address,
			&admission.PassportNumber,
			&admission.School,
			&admission.GuardianName,
			&admission.GuardianRelationship,
			&admission.GuardianContactNumber,
			&admission.GuardianAlternativePhone,
			&admission.MedicalInformation,
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		admissions = append(admissions, admission)
	}
	return admissions, rows.Err()
}

func (a *App) listStudentGroups() ([]StudentGroup, error) {
	rows, err := a.db.Query(`
		SELECT id, name, code, description, created_at
		FROM student_groups
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []StudentGroup
	for rows.Next() {
		var group StudentGroup
		if err := rows.Scan(&group.ID, &group.Name, &group.Code, &group.Description, &group.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range groups {
		students, err := a.listStudentsForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}
		groups[i].Students = students
		groups[i].StudentCount = len(students)
	}

	return groups, nil
}

func (a *App) listAttendanceRecords(groupID int64, attendanceDate string) ([]AttendanceRecord, error) {
	rows, err := a.db.Query(`
		SELECT id, group_id, admission_id, attendance_date, status, note, recorded_at, updated_at
		FROM attendance_records
		WHERE group_id = ? AND attendance_date = ?
		ORDER BY admission_id ASC, id ASC
	`, groupID, attendanceDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []AttendanceRecord
	for rows.Next() {
		var record AttendanceRecord
		if err := rows.Scan(
			&record.ID,
			&record.GroupID,
			&record.AdmissionID,
			&record.AttendanceDate,
			&record.Status,
			&record.Note,
			&record.RecordedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (a *App) listRecentAttendanceDates(groupID int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := a.db.Query(`
		SELECT DISTINCT attendance_date
		FROM attendance_records
		WHERE group_id = ?
		ORDER BY attendance_date DESC
		LIMIT ?
	`, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	return dates, rows.Err()
}

func (a *App) listSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		if err := rows.Scan(
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
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) listPricingRules() ([]PricingRule, error) {
	rows, err := a.db.Query(`
		SELECT id, activity, quantity, weekday_offpeak_price, weekday_peak_price,
		       weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		FROM pricing_rules
		ORDER BY activity ASC, quantity ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []PricingRule
	for rows.Next() {
		var rule PricingRule
		if err := rows.Scan(
			&rule.ID,
			&rule.Activity,
			&rule.Quantity,
			&rule.WeekdayOffPeak,
			&rule.WeekdayPeak,
			&rule.WeekendOffPeak,
			&rule.WeekendPeak,
			&rule.CreatedAt,
			&rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (a *App) listAdmissionPricings() ([]AdmissionPricing, error) {
	rows, err := a.db.Query(`
		SELECT id, practice_type, price, created_at, updated_at
		FROM admission_pricing
		ORDER BY practice_type ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pricings []AdmissionPricing
	for rows.Next() {
		var pricing AdmissionPricing
		if err := rows.Scan(
			&pricing.ID,
			&pricing.PracticeType,
			&pricing.Price,
			&pricing.CreatedAt,
			&pricing.UpdatedAt,
		); err != nil {
			return nil, err
		}
		pricings = append(pricings, pricing)
	}
	return pricings, rows.Err()
}

func (a *App) getPricingSettings() (*PricingSettings, error) {
	row := a.db.QueryRow(`
		SELECT id, peak_start_hour, peak_end_hour, created_at, updated_at
		FROM pricing_settings
		ORDER BY id ASC
		LIMIT 1
	`)

	var settings PricingSettings
	if err := row.Scan(
		&settings.ID,
		&settings.PeakStartHour,
		&settings.PeakEndHour,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (a *App) listActiveSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE status IN ('pending', 'confirmed')
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		if err := rows.Scan(
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
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) listPendingSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE status = 'pending'
		ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		if err := rows.Scan(
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
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) schedulesForSlot(slotDate, slotHour string, excludeID int64) ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE slot_date = ? AND slot_hour = ? AND id != ? AND status != 'rejected'
		ORDER BY id ASC
	`, slotDate, slotHour, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		if err := rows.Scan(
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
			&schedule.RequesterEmail,
			&schedule.RequesterPhone,
			&schedule.RequestedByUser,
			&schedule.ReviewNote,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) listStudentsForGroup(groupID int64) ([]Admission, error) {
	rows, err := a.db.Query(`
		SELECT a.id, a.student_id, a.full_name, a.admission_date, a.date_of_birth, a.gender, a.practice_type, a.address, a.passport_number, a.school,
		       a.guardian_name, a.guardian_relationship, a.guardian_contact_number, a.guardian_alternative_contact_number,
		       a.medical_information, a.created_at
		FROM admissions a
		JOIN student_group_members sgm ON sgm.admission_id = a.id
		WHERE sgm.group_id = ?
		ORDER BY a.full_name ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var students []Admission
	for rows.Next() {
		var admission Admission
		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.PracticeType,
			&admission.Address,
			&admission.PassportNumber,
			&admission.School,
			&admission.GuardianName,
			&admission.GuardianRelationship,
			&admission.GuardianContactNumber,
			&admission.GuardianAlternativePhone,
			&admission.MedicalInformation,
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		students = append(students, admission)
	}
	return students, rows.Err()
}

func (a *App) createAdmission(admission Admission) error {
	_, err := a.db.Exec(`
		INSERT INTO admissions (
			student_id, full_name, admission_date, date_of_birth, gender, practice_type, address, passport_number, school,
			guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
			medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) createStudentGroup(group StudentGroup, admissionIDs []int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		INSERT INTO student_groups (name, code, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, group.Name, group.Code, group.Description, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	groupID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, admissionID := range admissionIDs {
		if _, err := tx.Exec(`INSERT INTO student_group_members (group_id, admission_id) VALUES (?, ?)`, groupID, admissionID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) replaceAttendanceRecords(groupID int64, attendanceDate string, records []AttendanceRecord) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM attendance_records WHERE group_id = ? AND attendance_date = ?`, groupID, attendanceDate); err != nil {
		return err
	}

	for _, record := range records {
		if _, err := tx.Exec(`
			INSERT INTO attendance_records (
				group_id, admission_id, attendance_date, status, note, recorded_at, updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
			record.GroupID,
			record.AdmissionID,
			record.AttendanceDate,
			record.Status,
			record.Note,
			time.Now().UTC(),
			time.Now().UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) createSpaceSchedule(schedule SpaceSchedule) error {
	existing, err := a.schedulesForSlot(schedule.SlotDate, schedule.SlotHour, 0)
	if err != nil {
		return err
	}
	if err := validateSpaceScheduleSlot(existing, schedule); err != nil {
		return err
	}

	_, err = a.db.Exec(`
		INSERT INTO space_schedules (
			slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
			requester_name, requester_email, requester_phone, requested_by_user_id, review_note, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		"confirmed",
		schedule.RequesterName,
		schedule.RequesterEmail,
		schedule.RequesterPhone,
		nil,
		"",
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) createPricingRule(rule PricingRule) error {
	_, err := a.db.Exec(`
		INSERT INTO pricing_rules (
			activity, quantity, weekday_offpeak_price, weekday_peak_price,
			weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rule.Activity,
		rule.Quantity,
		rule.WeekdayOffPeak,
		rule.WeekdayPeak,
		rule.WeekendOffPeak,
		rule.WeekendPeak,
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) saveAdmissionPricings(pricings []AdmissionPricing) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, pricing := range pricings {
		if _, err := tx.Exec(`
			INSERT INTO admission_pricing (practice_type, price, created_at, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(practice_type) DO UPDATE SET
				price = excluded.price,
				updated_at = excluded.updated_at
		`,
			pricing.PracticeType,
			pricing.Price,
			time.Now().UTC(),
			time.Now().UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) createPublicBookingRequest(schedule SpaceSchedule) error {
	existing, err := a.schedulesForSlot(schedule.SlotDate, schedule.SlotHour, 0)
	if err != nil {
		return err
	}
	if err := validateSpaceScheduleSlot(existing, schedule); err != nil {
		return err
	}

	var requestedBy any
	if schedule.RequestedByUser > 0 {
		requestedBy = schedule.RequestedByUser
	}

	_, err = a.db.Exec(`
		INSERT INTO space_schedules (
			slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
			requester_name, requester_email, requester_phone, requested_by_user_id, review_note, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		"booking",
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		"pending",
		schedule.RequesterName,
		schedule.RequesterEmail,
		schedule.RequesterPhone,
		requestedBy,
		"",
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) updateAdmission(admission Admission) error {
	_, err := a.db.Exec(`
		UPDATE admissions
		SET student_id = ?, full_name = ?, admission_date = ?, date_of_birth = ?, gender = ?, practice_type = ?, address = ?, passport_number = ?, school = ?,
		    guardian_name = ?, guardian_relationship = ?, guardian_contact_number = ?, guardian_alternative_contact_number = ?,
		    medical_information = ?, updated_at = ?
		WHERE id = ?
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		time.Now().UTC(),
		admission.ID,
	)
	return err
}

func (a *App) updateStudentGroup(group StudentGroup, admissionIDs []int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE student_groups
		SET name = ?, code = ?, description = ?, updated_at = ?
		WHERE id = ?
	`, group.Name, group.Code, group.Description, time.Now().UTC(), group.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM student_group_members WHERE group_id = ?`, group.ID); err != nil {
		return err
	}
	for _, admissionID := range admissionIDs {
		if _, err := tx.Exec(`INSERT INTO student_group_members (group_id, admission_id) VALUES (?, ?)`, group.ID, admissionID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) updateSpaceSchedule(schedule SpaceSchedule) error {
	existing, err := a.schedulesForSlot(schedule.SlotDate, schedule.SlotHour, schedule.ID)
	if err != nil {
		return err
	}
	if err := validateSpaceScheduleSlot(existing, schedule); err != nil {
		return err
	}

	_, err = a.db.Exec(`
		UPDATE space_schedules
		SET slot_date = ?, slot_hour = ?, entry_type = ?, activity = ?, quantity = ?, title = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.EntryType,
		schedule.Activity,
		schedule.Quantity,
		schedule.Title,
		schedule.Notes,
		time.Now().UTC(),
		schedule.ID,
	)
	return err
}

func (a *App) updatePricingRule(rule PricingRule) error {
	_, err := a.db.Exec(`
		UPDATE pricing_rules
		SET activity = ?, quantity = ?, weekday_offpeak_price = ?, weekday_peak_price = ?,
		    weekend_offpeak_price = ?, weekend_peak_price = ?, updated_at = ?
		WHERE id = ?
	`,
		rule.Activity,
		rule.Quantity,
		rule.WeekdayOffPeak,
		rule.WeekdayPeak,
		rule.WeekendOffPeak,
		rule.WeekendPeak,
		time.Now().UTC(),
		rule.ID,
	)
	return err
}

func (a *App) updatePricingSettings(settings PricingSettings) error {
	_, err := a.db.Exec(`
		UPDATE pricing_settings
		SET peak_start_hour = ?, peak_end_hour = ?, updated_at = ?
		WHERE id = 1
	`, settings.PeakStartHour, settings.PeakEndHour, time.Now().UTC())
	return err
}

func (a *App) updateBookingRequestStatus(scheduleID int64, status, reviewNote string) error {
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		return err
	}
	if schedule.Status != "pending" {
		return errors.New("booking request is no longer pending")
	}
	if status == "confirmed" {
		existing, err := a.schedulesForSlot(schedule.SlotDate, schedule.SlotHour, schedule.ID)
		if err != nil {
			return err
		}
		if err := validateSpaceScheduleSlot(existing, *schedule); err != nil {
			return err
		}
	}
	_, err = a.db.Exec(`
		UPDATE space_schedules
		SET status = ?, review_note = ?, updated_at = ?
		WHERE id = ?
	`, status, reviewNote, time.Now().UTC(), scheduleID)
	return err
}

func (a *App) deleteAdmission(admissionID int64) error {
	_, err := a.db.Exec(`DELETE FROM admissions WHERE id = ?`, admissionID)
	return err
}

func (a *App) deleteStudentGroup(groupID int64) error {
	_, err := a.db.Exec(`DELETE FROM student_groups WHERE id = ?`, groupID)
	return err
}

func (a *App) deleteSpaceSchedule(scheduleID int64) error {
	_, err := a.db.Exec(`DELETE FROM space_schedules WHERE id = ?`, scheduleID)
	return err
}

func (a *App) deletePricingRule(pricingID int64) error {
	_, err := a.db.Exec(`DELETE FROM pricing_rules WHERE id = ?`, pricingID)
	return err
}

func (a *App) findAdmissionByID(admissionID int64) (*Admission, error) {
	row := a.db.QueryRow(`
		SELECT id, student_id, full_name, admission_date, date_of_birth, gender, practice_type, address, passport_number, school,
		       guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
		       medical_information, created_at
		FROM admissions
		WHERE id = ?
	`, admissionID)

	var admission Admission
	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &admission, nil
}

func (a *App) findStudentGroupByID(groupID int64) (*StudentGroup, error) {
	row := a.db.QueryRow(`
		SELECT id, name, code, description, created_at
		FROM student_groups
		WHERE id = ?
	`, groupID)

	var group StudentGroup
	if err := row.Scan(&group.ID, &group.Name, &group.Code, &group.Description, &group.CreatedAt); err != nil {
		return nil, err
	}
	students, err := a.listStudentsForGroup(group.ID)
	if err != nil {
		return nil, err
	}
	group.Students = students
	group.StudentCount = len(students)
	return &group, nil
}

func (a *App) findSpaceScheduleByID(scheduleID int64) (*SpaceSchedule, error) {
	row := a.db.QueryRow(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE id = ?
	`, scheduleID)

	var schedule SpaceSchedule
	if err := row.Scan(
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
		&schedule.RequesterEmail,
		&schedule.RequesterPhone,
		&schedule.RequestedByUser,
		&schedule.ReviewNote,
		&schedule.CreatedAt,
		&schedule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &schedule, nil
}

func (a *App) findPricingRuleByID(pricingID int64) (*PricingRule, error) {
	row := a.db.QueryRow(`
		SELECT id, activity, quantity, weekday_offpeak_price, weekday_peak_price,
		       weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		FROM pricing_rules
		WHERE id = ?
	`, pricingID)

	var rule PricingRule
	if err := row.Scan(
		&rule.ID,
		&rule.Activity,
		&rule.Quantity,
		&rule.WeekdayOffPeak,
		&rule.WeekdayPeak,
		&rule.WeekendOffPeak,
		&rule.WeekendPeak,
		&rule.CreatedAt,
		&rule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (a *App) deleteSessionByToken(token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, fmt.Sprintf("%x", hash[:]))
	return err
}

func (a *App) setFlash(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(message)),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10,
	})
}

func (a *App) consumeFlash(r *http.Request) string {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	a.clearCookieWithOptions(w, name, true)
}

func (a *App) clearCookieWithOptions(w http.ResponseWriter, name string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: httpOnly,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func runMigrations(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`ALTER TABLE users ADD COLUMN email_verified_at DATETIME`,
		`CREATE TABLE IF NOT EXISTS roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id INTEGER NOT NULL,
			role_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, role_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS role_permissions (
			role_id INTEGER NOT NULL,
			permission TEXT NOT NULL,
			PRIMARY KEY (role_id, permission),
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS email_verifications (
			user_id INTEGER PRIMARY KEY,
			otp_hash TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS admissions (
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
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS student_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS student_group_members (
			group_id INTEGER NOT NULL,
			admission_id INTEGER NOT NULL,
			PRIMARY KEY (group_id, admission_id),
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS attendance_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL,
			admission_id INTEGER NOT NULL,
			attendance_date TEXT NOT NULL,
			status TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			recorded_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS pricing_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			weekday_offpeak_price REAL NOT NULL DEFAULT 0,
			weekday_peak_price REAL NOT NULL DEFAULT 0,
			weekend_offpeak_price REAL NOT NULL DEFAULT 0,
			weekend_peak_price REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pricing_settings (
			id INTEGER PRIMARY KEY,
			peak_start_hour TEXT NOT NULL,
			peak_end_hour TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admission_pricing (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			practice_type TEXT NOT NULL UNIQUE,
			price REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS space_schedules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slot_date TEXT NOT NULL,
			slot_hour TEXT NOT NULL,
			entry_type TEXT NOT NULL,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			title TEXT NOT NULL,
			notes TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'confirmed',
			requester_name TEXT NOT NULL DEFAULT '',
			requester_email TEXT NOT NULL DEFAULT '',
			requester_phone TEXT NOT NULL DEFAULT '',
			requested_by_user_id INTEGER,
			review_note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_role_permissions_role_id ON role_permissions(role_id)`,
		`CREATE INDEX IF NOT EXISTS idx_email_verifications_expires_at ON email_verifications(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_admissions_created_at ON admissions(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_student_groups_created_at ON student_groups(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_members_group_id ON student_group_members(group_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_members_admission_id ON student_group_members(admission_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_attendance_group_student_date ON attendance_records(group_id, admission_id, attendance_date)`,
		`CREATE INDEX IF NOT EXISTS idx_attendance_group_date ON attendance_records(group_id, attendance_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_rules_option ON pricing_rules(activity, quantity)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_admission_pricing_type ON admission_pricing(practice_type)`,
		`CREATE INDEX IF NOT EXISTS idx_space_schedules_slot ON space_schedules(slot_date, slot_hour)`,
		`ALTER TABLE admissions ADD COLUMN student_id TEXT`,
		`ALTER TABLE admissions ADD COLUMN admission_date TEXT`,
		`ALTER TABLE admissions ADD COLUMN practice_type TEXT NOT NULL DEFAULT 'group_practice'`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil && !isIgnorableMigrationError(err, stmt) {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE admissions SET student_id = 'STD-' || printf('%05d', id) WHERE student_id IS NULL OR TRIM(student_id) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET admission_date = DATE(created_at) WHERE admission_date IS NULL OR TRIM(admission_date) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET practice_type = 'group_practice' WHERE practice_type IS NULL OR TRIM(practice_type) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admissions_admission_date ON admissions(admission_date)`); err != nil {
		return err
	}

	bookingColumns := []struct {
		name       string
		definition string
	}{
		{name: "status", definition: "TEXT NOT NULL DEFAULT 'confirmed'"},
		{name: "requester_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requester_email", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requester_phone", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requested_by_user_id", definition: "INTEGER"},
		{name: "review_note", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range bookingColumns {
		exists, err := tableHasColumn(db, "space_schedules", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE space_schedules ADD COLUMN %s %s", column.name, column.definition)); err != nil {
			return err
		}
	}
	statusExists, err := tableHasColumn(db, "space_schedules", "status")
	if err != nil {
		return err
	}
	if statusExists {
		if _, err := db.Exec(`UPDATE space_schedules SET status = 'confirmed' WHERE status IS NULL OR TRIM(status) = ''`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_space_schedules_status ON space_schedules(status)`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_admissions_student_id ON admissions(student_id)`); err != nil {
		return err
	}
	if err := seedPricingRules(db); err != nil {
		return err
	}
	if err := seedAdmissionPricing(db); err != nil {
		return err
	}
	if err := seedPricingSettings(db); err != nil {
		return err
	}

	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM email_verifications WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

func tableHasColumn(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func seedRoles(db *sql.DB) error {
	for _, role := range allRoles {
		if _, err := db.Exec(`INSERT OR IGNORE INTO roles (name) VALUES (?)`, role); err != nil {
			return err
		}
	}
	rolePermissions := map[string][]string{
		"customer":   {"dashboard.view"},
		"editor":     {"dashboard.view", "editor.access"},
		"admin":      {"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage", "student_groups.manage", "attendance.manage", "space_bookings.manage", "booking_requests.manage", "pricing.manage"},
		"superadmin": {"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage", "student_groups.manage", "attendance.manage", "space_bookings.manage", "booking_requests.manage", "pricing.manage"},
	}
	for roleName, permissions := range rolePermissions {
		roleID, err := queryRoleID(db, roleName)
		if err != nil {
			return err
		}
		for _, permission := range permissions {
			if _, err := db.Exec(`INSERT OR IGNORE INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedPricingRules(db *sql.DB) error {
	for _, option := range bookingOptionCatalog() {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO pricing_rules (
				activity, quantity, weekday_offpeak_price, weekday_peak_price,
				weekend_offpeak_price, weekend_peak_price, created_at, updated_at
			)
			VALUES (?, ?, 0, 0, 0, 0, ?, ?)
		`, option.Activity, option.Quantity, time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func seedAdmissionPricing(db *sql.DB) error {
	now := time.Now().UTC()
	defaults := []AdmissionPricing{
		{PracticeType: "group_practice", Price: 0},
		{PracticeType: "one_to_one_practice", Price: 0},
	}
	for _, pricing := range defaults {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO admission_pricing (practice_type, price, created_at, updated_at)
			VALUES (?, ?, ?, ?)
		`, pricing.PracticeType, pricing.Price, now, now); err != nil {
			return err
		}
	}
	return nil
}

func seedPricingSettings(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO pricing_settings (id, peak_start_hour, peak_end_hour, created_at, updated_at)
		VALUES (1, '17:00', '23:00', ?, ?)
	`, time.Now().UTC(), time.Now().UTC())
	return err
}

func bootstrapSuperadmin(db *sql.DB) error {
	const (
		superadminName     = "Janon Emersion T"
		superadminEmail    = "janon@lkprofessionals.com"
		superadminPassword = "Jj112112@!@!"
	)

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(superadminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT id FROM users WHERE email = ?`, superadminEmail)
	var userID int64
	switch err := row.Scan(&userID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE users
			SET name = ?, password_hash = ?, email_verified_at = ?
			WHERE id = ?
		`, superadminName, string(passwordHash), now, userID); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
			VALUES (?, ?, ?, ?, ?)
		`, superadminEmail, superadminName, string(passwordHash), now, now)
		if err != nil {
			return err
		}
		userID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	default:
		return err
	}

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}

	for _, role := range []string{"superadmin", "admin", "editor"} {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func roleIDByName(tx *sql.Tx, role string) (int64, error) {
	row := tx.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func queryRoleID(db *sql.DB, role string) (int64, error) {
	row := db.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func generateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (a *App) issueVerificationCode(userID int64) (string, error) {
	otp, err := generateOTP()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = a.db.Exec(`
		INSERT INTO email_verifications (user_id, otp_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			otp_hash = excluded.otp_hash,
			expires_at = excluded.expires_at,
			created_at = excluded.created_at
	`, userID, hashValue(otp), now.Add(otpTTL), now)
	if err != nil {
		return "", err
	}
	return otp, nil
}

func (a *App) sendVerificationEmail(user *User, otp string) error {
	if !a.smtp.Enabled {
		return errors.New("smtp is not configured")
	}

	headers := "" +
		"From: " + a.smtp.From + "\r\n" +
		"To: " + user.Email + "\r\n" +
		"Subject: Verify your email address\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n"
	body := fmt.Sprintf(
		"Hi %s,\r\n\r\nYour mekmaa3 verification code is %s.\r\nIt expires in 10 minutes.\r\n\r\nIf you did not create this account, you can ignore this email.\r\n",
		user.Name,
		otp,
	)
	auth := smtp.PlainAuth("", a.smtp.Username, a.smtp.Password, a.smtp.Host)
	return smtp.SendMail(a.smtp.Host+":"+a.smtp.Port, auth, a.smtp.From, []string{user.Email}, []byte(headers+body))
}

func (a *App) sendBookingConfirmationSMS(schedule *SpaceSchedule) error {
	if schedule == nil {
		return errors.New("schedule is required")
	}
	if !a.sms.Enabled {
		return errors.New("sms is not configured")
	}

	phone, err := normalizeSMSPhone(schedule.RequesterPhone)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("user_id", a.sms.UserID)
	form.Set("api_key", a.sms.APIKey)
	form.Set("sender_id", a.sms.SenderID)
	form.Set("contact", phone)
	form.Set("message", buildBookingConfirmationSMSBody(schedule))

	endpoint := "https://smslenz.lk/api/send-sms"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sms send failed with status %s", resp.Status)
	}

	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	if !payload.Success {
		if payload.Message != "" {
			return errors.New(payload.Message)
		}
		return errors.New("sms send failed")
	}
	return nil
}

func (a *App) consumeVerificationCode(userID int64, otp string) error {
	row := a.db.QueryRow(`SELECT otp_hash, expires_at FROM email_verifications WHERE user_id = ?`, userID)
	var otpHash string
	var expiresAt time.Time
	if err := row.Scan(&otpHash, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidOTP
		}
		return err
	}

	if expiresAt.Before(time.Now().UTC()) || otpHash != hashValue(otp) {
		return ErrInvalidOTP
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE users SET email_verified_at = ? WHERE id = ?`, time.Now().UTC(), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_verifications WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func normalizeSMSPhone(phone string) (string, error) {
	trimmed := strings.TrimSpace(phone)
	if trimmed == "" {
		return "", errors.New("customer phone number is missing")
	}

	var builder strings.Builder
	for i, r := range trimmed {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
			continue
		}
		if r == '+' && i == 0 {
			builder.WriteRune(r)
		}
	}

	normalized := builder.String()
	if strings.HasPrefix(normalized, "+") {
		digits := strings.TrimPrefix(normalized, "+")
		if len(digits) < 8 || len(digits) > 15 {
			return "", errors.New("customer phone number must be in E.164 format")
		}
		return normalized, nil
	}
	return "", errors.New("customer phone number must include country code, for example +9477xxxxxxx")
}

func buildBookingConfirmationSMSBody(schedule *SpaceSchedule) string {
	return fmt.Sprintf(
		"Booking confirmed: %s on %s at %s for %s. We look forward to seeing you.",
		schedule.Title,
		schedule.SlotDate,
		schedule.SlotHour,
		scheduleSummary(*schedule),
	)
}

func userHasAnyRole(user *User, roles ...string) bool {
	if user == nil {
		return false
	}
	for _, candidate := range roles {
		for _, assigned := range user.Roles {
			if assigned == candidate {
				return true
			}
		}
	}
	return false
}

func userHasRole(user *User, role string) bool {
	return userHasAnyRole(user, role)
}

func containsRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func normalizeRoles(roles []string) []string {
	allowed := make(map[string]struct{}, len(allRoles))
	for _, role := range allRoles {
		allowed[role] = struct{}{}
	}

	seen := map[string]struct{}{}
	var normalized []string
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if _, ok := allowed[role]; !ok {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		normalized = append(normalized, role)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizePermissions(permissions []string) []string {
	allowed := make(map[string]struct{}, len(allPermissions))
	for _, permission := range allPermissions {
		allowed[permission] = struct{}{}
	}

	seen := map[string]struct{}{}
	var normalized []string
	for _, permission := range permissions {
		permission = strings.ToLower(strings.TrimSpace(permission))
		if _, ok := allowed[permission]; !ok {
			continue
		}
		if _, ok := seen[permission]; ok {
			continue
		}
		seen[permission] = struct{}{}
		normalized = append(normalized, permission)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeAdmissionIDs(values []string) []int64 {
	seen := map[int64]struct{}{}
	var ids []int64
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func bookingActivities() []string {
	return []string{"training", "full_indoor_cricket", "futsal", "badminton", "table_tennis", "cricket_net", "tennis"}
}

func sportsCatalog() []SportPage {
	return []SportPage{
		{
			Slug:             "cricket",
			Name:             "Cricket Nets",
			Kicker:           "Indoor Cricket",
			Summary:          "Train with dedicated cricket net sessions at Mekmaa in Jaffna.",
			ShortDescription: "Practice lanes, repeatable drills and indoor focus for batting and bowling sessions.",
			Detail:           "Mekmaa gives players a dependable indoor cricket environment for technical repetition, small-group practice and structured improvement sessions.",
			Accent:           "bg-amber",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Cricket",
			Highlights:       []string{"Net-based repetition", "Indoor weather-proof practice", "Suitable for individual and small-group sessions"},
		},
		{
			Slug:             "futsal",
			Name:             "Futsal",
			Kicker:           "Indoor Team Play",
			Summary:          "Reserve indoor futsal sessions for teams and fast-paced match play at Mekmaa.",
			ShortDescription: "Clean indoor conditions for training games, competitive sessions and energetic group play.",
			Detail:           "The Mekmaa futsal setup is designed for teams that want consistent indoor conditions, easier planning and a strong environment for recreational or competitive sessions.",
			Accent:           "bg-emerald-500",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Futsal",
			Highlights:       []string{"Team-friendly sessions", "Fast indoor play", "Ideal for regular weekly bookings"},
		},
		{
			Slug:             "badminton",
			Name:             "Badminton",
			Kicker:           "Indoor Court Sessions",
			Summary:          "Play badminton in a comfortable indoor environment at Mekmaa.",
			ShortDescription: "Flexible bookings for casual rallies, match preparation and routine skill work.",
			Detail:           "Badminton sessions at Mekmaa are suited to players who want dependable indoor court time, whether that means social games, coaching support or repeated technical practice.",
			Accent:           "bg-aqua",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Badminton",
			Highlights:       []string{"Indoor comfort", "Casual and competitive use", "Strong option for repeated practice"},
		},
		{
			Slug:             "table-tennis",
			Name:             "Table Tennis",
			Kicker:           "Reflex and Focus",
			Summary:          "Book table tennis sessions at Mekmaa for fast, focused indoor play.",
			ShortDescription: "Indoor tables for quick games, reflex work and flexible training blocks.",
			Detail:           "Mekmaa supports table tennis sessions that reward concentration, timing and repetition, with an easy path for casual games or more focused improvement work.",
			Accent:           "bg-blush",
			PrimaryCTA:       "/book",
			PrimaryLabel:     "Book Table Tennis",
			Highlights:       []string{"Flexible session formats", "Good for individuals and pairs", "Strong indoor setup for focus-based training"},
		},
		{
			Slug:             "tennis",
			Name:             "Tennis",
			Kicker:           "Tennis at Mekmaa",
			Summary:          "Explore tennis opportunities through Mekmaa's indoor sports offering in Jaffna.",
			ShortDescription: "A tennis pathway for players who want structured sport access and want to enquire directly.",
			Detail:           "Tennis is now part of the public sports catalogue at Mekmaa. For current session formats, availability and coaching-related enquiries, players can contact the team directly.",
			Accent:           "bg-lime-200",
			PrimaryCTA:       "/contact?subject=Tennis%20Enquiry",
			PrimaryLabel:     "Enquire About Tennis",
			Highlights:       []string{"Included in the sports catalogue", "Direct enquiry path for availability", "Suitable for players seeking structured access"},
		},
	}
}

func sportBySlug(slug string) (SportPage, bool) {
	for _, sport := range sportsCatalog() {
		if sport.Slug == slug {
			return sport, true
		}
	}
	return SportPage{}, false
}

func homeFAQItems() []FAQItem {
	return []FAQItem{
		{Question: "How do I book a session?", Answer: "Use the booking page to review available slots and choose the activity that fits your session. If you need help with a special request, contact the team directly."},
		{Question: "Which sports are available at Mekmaa?", Answer: "Mekmaa currently features cricket nets, futsal, badminton, table tennis and tennis as part of its public sports offering."},
		{Question: "Is coaching available for children and teenagers?", Answer: "Yes. Mekmaa Cricket Academy provides structured coaching with a strong focus on skill development, discipline and confidence for kids and teens."},
		{Question: "Can adults also use the facility?", Answer: "Yes. The facility is positioned as suitable for kids, teens and adults across general bookings and sport sessions."},
		{Question: "How do I enquire about tennis?", Answer: "Tennis is available inside the sports section. Use the tennis sport page or the contact page to ask about session formats and availability."},
	}
}

func bookingHours() []string {
	var hours []string
	for hour := 6; hour <= 23; hour++ {
		hours = append(hours, fmt.Sprintf("%02d:00", hour))
	}
	return hours
}

func admissionFromRequest(r *http.Request) Admission {
	return Admission{
		StudentID:                strings.ToUpper(strings.TrimSpace(r.FormValue("student_id"))),
		FullName:                 strings.TrimSpace(r.FormValue("full_name")),
		AdmissionDate:            strings.TrimSpace(r.FormValue("admission_date")),
		DateOfBirth:              strings.TrimSpace(r.FormValue("date_of_birth")),
		Gender:                   strings.ToLower(strings.TrimSpace(r.FormValue("gender"))),
		PracticeType:             strings.ToLower(strings.TrimSpace(r.FormValue("practice_type"))),
		Address:                  strings.TrimSpace(r.FormValue("address")),
		PassportNumber:           strings.TrimSpace(r.FormValue("passport_number")),
		School:                   strings.TrimSpace(r.FormValue("school")),
		GuardianName:             strings.TrimSpace(r.FormValue("guardian_name")),
		GuardianRelationship:     strings.TrimSpace(r.FormValue("guardian_relationship")),
		GuardianContactNumber:    strings.TrimSpace(r.FormValue("guardian_contact_number")),
		GuardianAlternativePhone: strings.TrimSpace(r.FormValue("guardian_alternative_contact_number")),
		MedicalInformation:       strings.TrimSpace(r.FormValue("medical_information")),
	}
}

func scheduleFromRequest(r *http.Request) SpaceSchedule {
	entryType := strings.ToLower(strings.TrimSpace(r.FormValue("entry_type")))
	activity := strings.ToLower(strings.TrimSpace(r.FormValue("activity")))
	if entryType == "training" {
		activity = "training"
	}
	quantity, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if quantity <= 0 {
		quantity = 1
	}
	return SpaceSchedule{
		SlotDate:  strings.TrimSpace(r.FormValue("slot_date")),
		SlotHour:  strings.TrimSpace(r.FormValue("slot_hour")),
		EntryType: entryType,
		Activity:  activity,
		Quantity:  quantity,
		Title:     strings.TrimSpace(r.FormValue("title")),
		Notes:     strings.TrimSpace(r.FormValue("notes")),
	}
}

func pricingRuleFromRequest(r *http.Request) (PricingRule, error) {
	quantity, err := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if err != nil {
		return PricingRule{}, errors.New("valid quantity is required")
	}
	weekdayOffPeak, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("weekday_offpeak_price")), 64)
	if err != nil {
		return PricingRule{}, errors.New("valid weekday off-peak price is required")
	}
	weekdayPeak, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("weekday_peak_price")), 64)
	if err != nil {
		return PricingRule{}, errors.New("valid weekday peak price is required")
	}
	weekendOffPeak, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("weekend_offpeak_price")), 64)
	if err != nil {
		return PricingRule{}, errors.New("valid weekend off-peak price is required")
	}
	weekendPeak, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("weekend_peak_price")), 64)
	if err != nil {
		return PricingRule{}, errors.New("valid weekend peak price is required")
	}

	return PricingRule{
		Activity:       strings.ToLower(strings.TrimSpace(r.FormValue("activity"))),
		Quantity:       quantity,
		WeekdayOffPeak: weekdayOffPeak,
		WeekdayPeak:    weekdayPeak,
		WeekendOffPeak: weekendOffPeak,
		WeekendPeak:    weekendPeak,
	}, nil
}

func prefillPublicBookingDraft(r *http.Request, viewer *User, calendarDate string) *SpaceSchedule {
	draft := &SpaceSchedule{
		EntryType: "booking",
		SlotDate:  calendarDate,
		SlotHour:  strings.TrimSpace(r.URL.Query().Get("hour")),
		Activity:  strings.ToLower(strings.TrimSpace(r.URL.Query().Get("activity"))),
		Quantity:  1,
	}
	if quantity, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("quantity"))); err == nil && quantity > 0 {
		draft.Quantity = quantity
	}
	if viewer != nil {
		draft.RequesterName = viewer.Name
		draft.RequesterEmail = viewer.Email
	}
	return draft
}

func studentGroupFromRequest(r *http.Request) StudentGroup {
	return StudentGroup{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Code:        strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
}

func validateAdmission(admission Admission) error {
	switch {
	case admission.StudentID == "":
		return errors.New("student id is required")
	case admission.FullName == "":
		return errors.New("full name is required")
	case admission.AdmissionDate == "":
		return errors.New("admission date is required")
	case admission.DateOfBirth == "":
		return errors.New("date of birth is required")
	case admission.Gender != "male" && admission.Gender != "female":
		return errors.New("gender is required")
	case admission.PracticeType != "group_practice" && admission.PracticeType != "one_to_one_practice":
		return errors.New("practice type is required")
	case admission.Address == "":
		return errors.New("address is required")
	case admission.PassportNumber == "":
		return errors.New("p.p. no is required")
	case admission.School == "":
		return errors.New("school is required")
	case admission.GuardianName == "":
		return errors.New("parent or guardian name is required")
	case admission.GuardianRelationship == "":
		return errors.New("relationship is required")
	case admission.GuardianContactNumber == "":
		return errors.New("contact number is required")
	case admission.GuardianAlternativePhone == "":
		return errors.New("alternative contact number is required")
	case admission.MedicalInformation == "":
		return errors.New("medical information is required")
	default:
		return nil
	}
}

func validateStudentGroup(group StudentGroup) error {
	switch {
	case group.Name == "":
		return errors.New("group name is required")
	case group.Code == "":
		return errors.New("group code is required")
	case group.Description == "":
		return errors.New("description is required")
	default:
		return nil
	}
}

func validatePricingRule(rule PricingRule) error {
	switch {
	case rule.Activity == "":
		return errors.New("activity is required")
	case rule.Quantity <= 0:
		return errors.New("quantity must be greater than 0")
	case rule.WeekdayOffPeak < 0 || rule.WeekdayPeak < 0 || rule.WeekendOffPeak < 0 || rule.WeekendPeak < 0:
		return errors.New("prices cannot be negative")
	}

	for _, option := range bookingOptionCatalog() {
		if option.Activity == rule.Activity && option.Quantity == rule.Quantity {
			return nil
		}
	}
	return errors.New("unsupported booking option")
}

func validatePricingSettings(settings PricingSettings) error {
	start, err := time.Parse("15:04", settings.PeakStartHour)
	if err != nil {
		return errors.New("valid peak start hour is required")
	}
	end, err := time.Parse("15:04", settings.PeakEndHour)
	if err != nil {
		return errors.New("valid peak end hour is required")
	}
	if !start.Before(end) {
		return errors.New("peak end hour must be after peak start hour")
	}
	return nil
}

func validateSpaceScheduleInput(schedule SpaceSchedule) error {
	if schedule.EntryType != "booking" && schedule.EntryType != "training" {
		return errors.New("entry type is required")
	}
	if schedule.Title == "" {
		return errors.New("title is required")
	}
	if _, err := time.Parse("2006-01-02", schedule.SlotDate); err != nil {
		return errors.New("valid slot date is required")
	}
	if _, err := time.Parse("15:04", schedule.SlotHour); err != nil {
		return errors.New("valid slot hour is required")
	}
	if schedule.EntryType == "training" {
		schedule.Activity = "training"
	}
	switch schedule.Activity {
	case "training":
		if schedule.EntryType != "training" {
			return errors.New("training activity must use training entry type")
		}
		if schedule.Quantity != 1 {
			return errors.New("training quantity must be 1")
		}
	case "full_indoor_cricket", "futsal", "badminton", "tennis":
		if schedule.Quantity != 1 {
			return errors.New("selected activity supports only quantity 1")
		}
	case "table_tennis":
		if schedule.Quantity < 1 || schedule.Quantity > 2 {
			return errors.New("table tennis quantity must be 1 or 2")
		}
	case "cricket_net":
		if schedule.Quantity < 1 || schedule.Quantity > 3 {
			return errors.New("cricket net quantity must be between 1 and 3")
		}
	default:
		return errors.New("activity is required")
	}
	return nil
}

func validateSpaceScheduleSlot(existing []SpaceSchedule, candidate SpaceSchedule) error {
	schedules := append([]SpaceSchedule{}, existing...)
	schedules = append(schedules, candidate)

	var trainings int
	var fullIndoorCricket int
	var futsal int
	var badminton int
	var tableTennis int
	var cricketNets int
	var tennis int

	for _, schedule := range schedules {
		switch schedule.Activity {
		case "training":
			trainings += schedule.Quantity
		case "full_indoor_cricket":
			fullIndoorCricket += schedule.Quantity
		case "futsal":
			futsal += schedule.Quantity
		case "badminton":
			badminton += schedule.Quantity
		case "table_tennis":
			tableTennis += schedule.Quantity
		case "cricket_net":
			cricketNets += schedule.Quantity
		case "tennis":
			tennis += schedule.Quantity
		}
	}

	if trainings > 0 {
		if len(schedules) > 1 || fullIndoorCricket > 0 || futsal > 0 || badminton > 0 || tableTennis > 0 || cricketNets > 0 || tennis > 0 {
			return errors.New("training time blocks the full slot")
		}
		return nil
	}

	if fullIndoorCricket == 1 && futsal == 0 && badminton == 0 && tableTennis == 0 && cricketNets == 0 && tennis == 0 {
		return nil
	}
	if futsal == 1 && fullIndoorCricket == 0 && badminton == 0 && tableTennis == 0 && cricketNets == 0 && tennis == 0 {
		return nil
	}
	if badminton == 1 && cricketNets == 1 && fullIndoorCricket == 0 && futsal == 0 && tableTennis == 0 && tennis == 0 {
		return nil
	}
	if tableTennis >= 1 && tableTennis <= 2 && fullIndoorCricket == 0 && futsal == 0 && badminton == 0 && cricketNets == 0 && tennis == 0 {
		return nil
	}
	if badminton == 1 && tableTennis == 1 && fullIndoorCricket == 0 && futsal == 0 && cricketNets == 0 && tennis == 0 {
		return nil
	}
	if cricketNets == 3 && fullIndoorCricket == 0 && futsal == 0 && badminton == 0 && tableTennis == 0 && tennis == 0 {
		return nil
	}
	if tennis == 1 && fullIndoorCricket == 0 && futsal == 0 && badminton == 0 && tableTennis == 0 && cricketNets == 0 {
		return nil
	}

	return errors.New("that slot combination is not allowed")
}

func bookingOptionCatalog() []BookingOption {
	return []BookingOption{
		{Activity: "full_indoor_cricket", Quantity: 1, Label: "Full Indoor Cricket"},
		{Activity: "futsal", Quantity: 1, Label: "Futsal"},
		{Activity: "badminton", Quantity: 1, Label: "Badminton"},
		{Activity: "table_tennis", Quantity: 1, Label: "Table Tennis x1"},
		{Activity: "table_tennis", Quantity: 2, Label: "Table Tennis x2"},
		{Activity: "cricket_net", Quantity: 1, Label: "Cricket Net x1"},
		{Activity: "cricket_net", Quantity: 3, Label: "Cricket Nets x3"},
		{Activity: "tennis", Quantity: 1, Label: "Tennis"},
	}
}

func buildBookingSlotAvailability(schedules []SpaceSchedule, slotDate string, hours []string) []BookingSlotAvailability {
	var availability []BookingSlotAvailability
	for _, hour := range hours {
		existing := schedulesForCalendarSlot(schedules, slotDate, hour)
		slot := BookingSlotAvailability{
			Hour:      hour,
			Schedules: existing,
		}

		hasTraining := false
		for _, schedule := range existing {
			if schedule.EntryType == "training" || schedule.Activity == "training" {
				hasTraining = true
				break
			}
		}
		if hasTraining {
			slot.BlockedReason = "Training session"
			availability = append(availability, slot)
			continue
		}

		for _, option := range bookingOptionCatalog() {
			candidate := SpaceSchedule{
				EntryType: "booking",
				Activity:  option.Activity,
				Quantity:  option.Quantity,
				SlotDate:  slotDate,
				SlotHour:  hour,
				Status:    "pending",
			}
			if err := validateSpaceScheduleSlot(existing, candidate); err == nil {
				slot.Options = append(slot.Options, option)
			}
		}
		if len(slot.Options) == 0 {
			slot.BlockedReason = "No bookable combinations available"
		}
		availability = append(availability, slot)
	}
	return availability
}

func buildBookingWeekDays(schedules []SpaceSchedule, selectedDate time.Time, hours []string) []CalendarDay {
	start := selectedDate.AddDate(0, 0, -3)
	today := time.Now().Format("2006-01-02")
	days := make([]CalendarDay, 0, 7)

	for offset := 0; offset < 7; offset++ {
		day := start.AddDate(0, 0, offset)
		date := day.Format("2006-01-02")
		availability := buildBookingSlotAvailability(schedules, date, hours)

		openCount := 0
		busyCount := 0
		for _, slot := range availability {
			if len(slot.Options) > 0 {
				openCount++
			} else {
				busyCount++
			}
		}

		days = append(days, CalendarDay{
			Date:          date,
			DayLabel:      day.Format("Mon"),
			MonthLabel:    day.Format("Jan"),
			DayNumber:     day.Format("02"),
			OpenSlotCount: openCount,
			BusySlotCount: busyCount,
			IsToday:       date == today,
			IsSelected:    date == selectedDate.Format("2006-01-02"),
		})
	}

	return days
}

func containsPermission(permissions []string, target string) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func isUniqueConstraintError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique")
}

func admissionSelected(admissions []Admission, admissionID int64) bool {
	for _, admission := range admissions {
		if admission.ID == admissionID {
			return true
		}
	}
	return false
}

func normalizeAttendanceStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "present":
		return "present"
	case "late":
		return "late"
	case "excused":
		return "excused"
	default:
		return "absent"
	}
}

func attendanceStatus(record AttendanceRecord) string {
	return normalizeAttendanceStatus(record.Status)
}

func attendanceRecordFor(records []AttendanceRecord, admissionID int64) AttendanceRecord {
	for _, record := range records {
		if record.AdmissionID == admissionID {
			return record
		}
	}
	return AttendanceRecord{AdmissionID: admissionID, Status: "absent"}
}

func attendanceCount(records []AttendanceRecord, status string) int {
	total := 0
	for _, record := range records {
		if attendanceStatus(record) == status {
			total++
		}
	}
	return total
}

func scheduleSummary(schedule SpaceSchedule) string {
	switch schedule.Activity {
	case "training":
		return "Training"
	case "full_indoor_cricket":
		return "Full Indoor Cricket"
	case "futsal":
		return "Futsal"
	case "badminton":
		return "Badminton"
	case "table_tennis":
		if schedule.Quantity > 1 {
			return fmt.Sprintf("Table Tennis x%d", schedule.Quantity)
		}
		return "Table Tennis"
	case "cricket_net":
		if schedule.Quantity > 1 {
			return fmt.Sprintf("Cricket Nets x%d", schedule.Quantity)
		}
		return "Cricket Net"
	case "tennis":
		return "Tennis"
	default:
		return schedule.Activity
	}
}

func optionSummary(option BookingOption) string {
	return scheduleSummary(SpaceSchedule{Activity: option.Activity, Quantity: option.Quantity})
}

func bookingOptionSelected(draft *SpaceSchedule, slotHour, activity string, quantity int) bool {
	if draft == nil {
		return false
	}
	return draft.SlotHour == slotHour && draft.Activity == activity && draft.Quantity == quantity
}

func activityLabel(activity string) string {
	return scheduleSummary(SpaceSchedule{Activity: activity, Quantity: 1})
}

func bookingProductLabel(activity string, quantity int) string {
	return scheduleSummary(SpaceSchedule{Activity: activity, Quantity: quantity})
}

func isPeakHour(slotHour string, settings *PricingSettings) bool {
	if settings == nil {
		return false
	}
	slot, err := time.Parse("15:04", slotHour)
	if err != nil {
		return false
	}
	start, err := time.Parse("15:04", settings.PeakStartHour)
	if err != nil {
		return false
	}
	end, err := time.Parse("15:04", settings.PeakEndHour)
	if err != nil {
		return false
	}
	return (slot.Equal(start) || slot.After(start)) && (slot.Equal(end) || slot.Before(end))
}

func isWeekendDate(slotDate string) bool {
	parsed, err := time.Parse("2006-01-02", slotDate)
	if err != nil {
		return false
	}
	return parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday
}

func pricingTierLabel(settings *PricingSettings, slotDate, slotHour string) string {
	dayType := "Weekday"
	if isWeekendDate(slotDate) {
		dayType = "Weekend"
	}
	hourType := "Off-peak"
	if isPeakHour(slotHour, settings) {
		hourType = "Peak"
	}
	return dayType + " " + hourType
}

func priceForRuleSlot(rule PricingRule, settings *PricingSettings, slotDate, slotHour string) float64 {
	if isWeekendDate(slotDate) {
		if isPeakHour(slotHour, settings) {
			return rule.WeekendPeak
		}
		return rule.WeekendOffPeak
	}
	if isPeakHour(slotHour, settings) {
		return rule.WeekdayPeak
	}
	return rule.WeekdayOffPeak
}

func pricingRuleForOption(pricings []PricingRule, activity string, quantity int) *PricingRule {
	for i := range pricings {
		if pricings[i].Activity == activity && pricings[i].Quantity == quantity {
			return &pricings[i]
		}
	}
	return nil
}

func pricingForOption(pricings []PricingRule, settings *PricingSettings, slotDate, slotHour, activity string, quantity int) string {
	rule := pricingRuleForOption(pricings, activity, quantity)
	if rule == nil {
		return "Set pricing"
	}
	if rule.WeekdayOffPeak == 0 && rule.WeekdayPeak == 0 && rule.WeekendOffPeak == 0 && rule.WeekendPeak == 0 {
		return "Set pricing"
	}
	return money(priceForRuleSlot(*rule, settings, slotDate, slotHour))
}

func pricingForSchedule(pricings []PricingRule, settings *PricingSettings, schedule *SpaceSchedule) string {
	if schedule == nil || schedule.SlotDate == "" || schedule.SlotHour == "" || schedule.Activity == "" || schedule.Quantity <= 0 {
		return "Choose a combination"
	}
	return pricingForOption(pricings, settings, schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)
}

func money(value float64) string {
	return fmt.Sprintf("LKR %.2f", value)
}

func practiceTypeLabel(value string) string {
	switch value {
	case "group_practice":
		return "Group practice"
	case "one_to_one_practice":
		return "One to one practice"
	default:
		return "Unknown"
	}
}

func admissionAge(dateOfBirth string) string {
	dob, err := time.Parse("2006-01-02", strings.TrimSpace(dateOfBirth))
	if err != nil {
		return "—"
	}

	now := time.Now()
	age := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		age--
	}
	if age < 0 {
		return "—"
	}

	return strconv.Itoa(age)
}

func scheduleToneClasses(schedule SpaceSchedule) string {
	switch schedule.Activity {
	case "training":
		return "border-amber-200 bg-amber-50 text-amber-900"
	case "full_indoor_cricket":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case "futsal":
		return "border-sky-200 bg-sky-50 text-sky-900"
	case "badminton":
		return "border-violet-200 bg-violet-50 text-violet-900"
	case "table_tennis":
		return "border-cyan-200 bg-cyan-50 text-cyan-900"
	case "cricket_net":
		return "border-lime-200 bg-lime-50 text-lime-900"
	case "tennis":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	default:
		return "border-slate/10 bg-white text-slate"
	}
}

func scheduleBadgeClasses(schedule SpaceSchedule) string {
	switch schedule.Activity {
	case "training":
		return "bg-amber-100 text-amber-800"
	case "full_indoor_cricket":
		return "bg-emerald-100 text-emerald-800"
	case "futsal":
		return "bg-sky-100 text-sky-800"
	case "badminton":
		return "bg-violet-100 text-violet-800"
	case "table_tennis":
		return "bg-cyan-100 text-cyan-800"
	case "cricket_net":
		return "bg-lime-100 text-lime-800"
	case "tennis":
		return "bg-emerald-100 text-emerald-800"
	default:
		return "bg-slate-100 text-slate-800"
	}
}

func schedulesForCalendarSlot(schedules []SpaceSchedule, slotDate, slotHour string) []SpaceSchedule {
	var filtered []SpaceSchedule
	for _, schedule := range schedules {
		if schedule.SlotDate == slotDate && schedule.SlotHour == slotHour {
			filtered = append(filtered, schedule)
		}
	}
	return filtered
}

func schedulesForDate(schedules []SpaceSchedule, slotDate string) []SpaceSchedule {
	var filtered []SpaceSchedule
	for _, schedule := range schedules {
		if schedule.SlotDate == slotDate {
			filtered = append(filtered, schedule)
		}
	}
	return filtered
}

func buildDailyBookingStats(schedules []SpaceSchedule, hours []string) []Stat {
	occupiedHours := map[string]struct{}{}
	trainingHours := map[string]struct{}{}
	bookingEntries := 0
	for _, schedule := range schedules {
		occupiedHours[schedule.SlotHour] = struct{}{}
		if schedule.EntryType == "training" {
			trainingHours[schedule.SlotHour] = struct{}{}
		}
		if schedule.EntryType == "booking" {
			bookingEntries++
		}
	}

	return []Stat{
		{Label: "Total slots used", Value: strconv.Itoa(len(occupiedHours))},
		{Label: "Training hours", Value: strconv.Itoa(len(trainingHours))},
		{Label: "Booking entries", Value: strconv.Itoa(bookingEntries)},
		{Label: "Open hours", Value: strconv.Itoa(len(hours) - len(occupiedHours))},
	}
}

func normalizeRoleName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isSystemRole(name string) bool {
	switch name {
	case "customer", "editor", "admin", "superadmin":
		return true
	default:
		return false
	}
}

func isIgnorableMigrationError(err error, stmt string) bool {
	lowerErr := strings.ToLower(err.Error())
	return (strings.Contains(stmt, "ALTER TABLE users ADD COLUMN email_verified_at") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN student_id") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN admission_date") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN practice_type") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN status") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_name") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_email") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_phone") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requested_by_user_id") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN review_note")) &&
		strings.Contains(lowerErr, "duplicate column name")
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loadDotEnv(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return nil
}

func buildTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"hasRole": func(user *User, role string) bool {
			return userHasAnyRole(user, role)
		},
		"hasAnyRole": func(user *User, roles ...string) bool {
			return userHasAnyRole(user, roles...)
		},
		"isCurrentPath": func(current string, paths ...string) bool {
			for _, path := range paths {
				if current == path {
					return true
				}
			}
			return false
		},
		"contains": func(roles []string, role string) bool {
			return containsRole(roles, role)
		},
		"containsPermission": func(permissions []string, permission string) bool {
			return containsPermission(permissions, permission)
		},
		"hasPermission": func(user *User, permission string) bool {
			if user == nil {
				return false
			}
			return containsPermission(user.Permissions, permission)
		},
		"admissionSelected":        admissionSelected,
		"admissionAge":             admissionAge,
		"attendanceCount":          attendanceCount,
		"attendanceRecordFor":      attendanceRecordFor,
		"attendanceStatus":         attendanceStatus,
		"activityLabel":            activityLabel,
		"bookingProductLabel":      bookingProductLabel,
		"optionSummary":            optionSummary,
		"bookingOptionSelected":    bookingOptionSelected,
		"pricingForOption":         pricingForOption,
		"pricingForSchedule":       pricingForSchedule,
		"pricingTierLabel":         pricingTierLabel,
		"practiceTypeLabel":        practiceTypeLabel,
		"money":                    money,
		"scheduleToneClasses":      scheduleToneClasses,
		"scheduleBadgeClasses":     scheduleBadgeClasses,
		"schedulesForCalendarSlot": schedulesForCalendarSlot,
		"scheduleSummary":          scheduleSummary,
		"seq": func(n int) []int {
			if n <= 0 {
				return nil
			}
			values := make([]int, n)
			for i := 0; i < n; i++ {
				values[i] = i
			}
			return values
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"isSystemRole": isSystemRole,
	}

	base, err := template.New("base.html").Funcs(funcs).ParseFiles("templates/base.html")
	if err != nil {
		return nil, err
	}
	publicPartials := []string{
		"templates/partials/header.html",
		"templates/partials/footer.html",
		"templates/partials/home-style.html",
		"templates/partials/home-hero.html",
		"templates/partials/home-sports-grid.html",
		"templates/partials/home-coaching-strip.html",
		"templates/partials/home-highlights.html",
		"templates/partials/home-booking-flow.html",
		"templates/partials/home-cta-band.html",
		"templates/partials/home-script.html",
	}

	pages := map[string]string{
		"home":                     "templates/pages/home.html",
		"about":                    "templates/pages/about.html",
		"book":                     "templates/pages/book.html",
		"contact":                  "templates/pages/contact.html",
		"coaching":                 "templates/pages/coaching.html",
		"faq":                      "templates/pages/faq.html",
		"gallery":                  "templates/pages/gallery.html",
		"login":                    "templates/login.html",
		"privacy-policy":           "templates/pages/privacy-policy.html",
		"register":                 "templates/register.html",
		"refund-policy":            "templates/pages/refund-policy.html",
		"sports":                   "templates/pages/sports.html",
		"terms-and-conditions":     "templates/pages/terms-and-conditions.html",
		"verify-email":             "templates/verify-email.html",
		"dashboard":                "templates/dashboard/dashboard.html",
		"editor":                   "templates/dashboard/editor.html",
		"user-management":          "templates/dashboard/user-management.html",
		"role-management":          "templates/dashboard/role-management.html",
		"admission-management":     "templates/dashboard/admission-management.html",
		"student-group-management": "templates/dashboard/student-group-management.html",
		"attendance-management":    "templates/dashboard/attendance-management.html",
		"booking-management":       "templates/dashboard/booking-management.html",
		"booking-requests":         "templates/dashboard/booking-requests.html",
		"pricing-management":       "templates/dashboard/pricing-management.html",
		"forbidden":                "templates/dashboard/forbidden.html",
	}
	dashboardPartials := []string{
		"templates/dashboard/src/sidebar.html",
		"templates/dashboard/src/header.html",
		"templates/dashboard/src/footer.html",
	}
	templates := make(map[string]*template.Template, len(pages))
	for page, path := range pages {
		tmpl, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := tmpl.ParseFiles(publicPartials...); err != nil {
			return nil, err
		}
		if _, err := tmpl.ParseFiles(dashboardPartials...); err != nil {
			return nil, err
		}
		if _, err := tmpl.ParseFiles(path); err != nil {
			return nil, err
		}
		templates[page] = tmpl
	}
	return templates, nil
}
