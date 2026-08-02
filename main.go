package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"math/big"
	"mime"
	"mime/multipart"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
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
	maxEventImageSize = 8 << 20
	maxEventFormSize  = maxEventImageSize + (1 << 20)
	defaultUploadDir  = "./data/uploads"
)

var (
	emailPattern        = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	passwordPattern     = regexp.MustCompile(`^.{10,}$`)
	otpPattern          = regexp.MustCompile(`^\d{6}$`)
	referralCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{3,24}$`)
	roleNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
	eventImagePattern   = regexp.MustCompile(`^event-[a-z0-9_-]{12,64}\.(jpg|png|webp)$`)
	storedEventPattern  = regexp.MustCompile(`^event-[a-z0-9_-]{12,64}\.(jpg|png|gif|webp)$`)
	allRoles            = []string{"superadmin", "admin", "editor", "coach", "customer"}
	allPermissions      = []string{
		"dashboard.view",
		"editor.access",
		"users.manage",
		"roles.manage",
		"admissions.manage",
		"training_programs.manage",
		"student_groups.manage",
		"attendance.manage",
		"courts.manage",
		"space_bookings.manage",
		"booking_requests.manage",
		"pricing.manage",
		"finance.manage",
		"reports.view",
		"events.manage",
	}
)

var permissionGroups = []PermissionGroup{
	{Name: "Workspace", Description: "Core authenticated workspace access.", Permissions: []PermissionDefinition{
		{Key: "dashboard.view", Label: "View dashboard", Description: "Open the authenticated dashboard and account overview."},
		{Key: "editor.access", Label: "Access editor", Description: "Open the protected content editor workspace."},
	}},
	{Name: "Administration", Description: "High-trust identity and authorization controls.", Permissions: []PermissionDefinition{
		{Key: "users.manage", Label: "Manage users", Description: "Create accounts and change user role assignments.", Sensitive: true},
		{Key: "roles.manage", Label: "Manage roles", Description: "Create, update, and remove custom authorization roles.", Sensitive: true},
	}},
	{Name: "Students", Description: "Student intake, training programmes, grouping, attendance, and billing operations.", Permissions: []PermissionDefinition{
		{
			Key:         "admissions.manage",
			Label:       "Manage admissions",
			Description: "Create, update, archive, and collect admission payments.",
		},
		{
			Key:         "training_programs.manage",
			Label:       "Manage training programmes",
			Description: "Create, update, activate, deactivate, and price training programmes.",
		},
		{
			Key:         "student_groups.manage",
			Label:       "Manage student groups",
			Description: "Create groups and maintain student memberships.",
		},
		{
			Key:         "attendance.manage",
			Label:       "Manage attendance",
			Description: "Record and update daily attendance registers.",
		},
	}},
	{Name: "Bookings", Description: "Court configuration, facility scheduling, and customer booking operations.", Permissions: []PermissionDefinition{
		{
			Key:         "courts.manage",
			Label:       "Manage courts",
			Description: "Configure courts, activities, and the combinations that may operate simultaneously.",
		},
		{
			Key:         "space_bookings.manage",
			Label:       "Manage booking calendar",
			Description: "Create, update, and remove facility schedule entries.",
		},
		{
			Key:         "booking_requests.manage",
			Label:       "Manage booking requests",
			Description: "Review, confirm, and reject customer booking requests.",
		},
	}},
	{Name: "Finance", Description: "Pricing, collections, ledger, referrals, and reporting.", Permissions: []PermissionDefinition{
		{
			Key:         "pricing.manage",
			Label:       "Manage booking pricing",
			Description: "Maintain facility booking rates and peak-hour pricing.",
		},
		{Key: "finance.manage", Label: "Manage finance", Description: "Manage payments, expenses, receipts, referrals, and exports."},
		{Key: "reports.view", Label: "View and export reports", Description: "Open operational reports and export report data."},
	}},
	{Name: "Content", Description: "Public website content operations.", Permissions: []PermissionDefinition{
		{Key: "events.manage", Label: "Manage events", Description: "Create, publish, update, and remove public events."},
	}},
}

type contextKey string

const userContextKey contextKey = "currentUser"

type App struct {
	db              *sql.DB
	templates       map[string]*template.Template
	cookieSecure    bool
	smtp            SMTPConfig
	sms             SMSConfig
	uploads         UploadStorage
	bookingMessages BookingCommunicationSettings
}

type UploadStorage struct {
	Root     string
	EventDir string
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

type BookingCommunicationSettings struct {
	EmailEnabled bool
	SMSEnabled   bool
	ContactPhone string
	ContactEmail string
	VenueName    string
	VenueAddress string
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
	UserCount   int
}

type PermissionDefinition struct {
	Key         string
	Label       string
	Description string
	Sensitive   bool
}

type PermissionGroup struct {
	Name        string
	Description string
	Permissions []PermissionDefinition
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
	PaymentCollected         bool
	PaymentCollectedAt       time.Time
	AdmissionPaymentAmount   float64
	FinanceTransactionID     int64
	CreatedAt                time.Time
	TrainingProgramID        int64
	TrainingProgramName      string
}

type FinanceTransaction struct {
	ID             int64
	ReceiptNumber  string
	Category       string
	ReferenceType  string
	ReferenceID    int64
	PersonName     string
	Description    string
	PaymentMethod  string
	Amount         float64
	RecordedByUser int64
	RecordedAt     time.Time
	CreatedAt      time.Time
}

type FinanceFilter struct {
	From      string
	To        string
	Direction string
	Category  string
	Search    string
}

type FinanceSummary struct {
	GrossIncome        float64
	TotalExpenses      float64
	NetCash            float64
	OutstandingBooking float64
	OutstandingMonthly float64
	PayableReferrals   float64
}

type BookingFinancial struct {
	ID                   int64
	ScheduleID           int64
	QuotedAmount         float64
	Paid                 bool
	PaidAt               time.Time
	PaymentMethod        string
	FinanceTransactionID int64
	SlotDate             string
	SlotHour             string
	Activity             string
	Quantity             int
	Status               string
	RequesterName        string
	RequesterEmail       string
}

type ReportPeriod struct {
	Kind         string
	Anchor       string
	Start        string
	End          string
	Label        string
	PreviousDate string
	NextDate     string
}

type ReportSummary struct {
	Income             float64
	Expenses           float64
	NetCash            float64
	BookingRevenue     float64
	StudentRevenue     float64
	AdmissionRevenue   float64
	ConfirmedBookings  int
	PendingBookings    int
	NewAdmissions      int
	StudentPayments    int
	AttendancePresent  int
	AttendanceTotal    int
	AttendanceRate     float64
	OccupiedSlotHours  int
	AvailableSlotHours int
	UtilizationRate    float64
}

type ReportSeriesPoint struct {
	Date       string
	Label      string
	Income     float64
	Expenses   float64
	NetCash    float64
	Bookings   int
	Admissions int
	Present    int
	Attendance int
}

type ReportBreakdown struct {
	Key    string
	Label  string
	Count  int
	Amount float64
}

type OperationalReport struct {
	Period           ReportPeriod
	Summary          ReportSummary
	Series           []ReportSeriesPoint
	FinanceBreakdown []ReportBreakdown
	BookingBreakdown []ReportBreakdown
	Transactions     []FinanceTransaction
	MaxDailyCash     float64
}

type StudentMonthlyPayment struct {
	ID                   int64
	AdmissionID          int64
	PaymentMonth         string
	Amount               float64
	PaymentMethod        string
	FinanceTransactionID int64
	CollectedByUserID    int64
	CollectedAt          time.Time
	CreatedAt            time.Time
}

type StudentPaymentRow struct {
	Admission  Admission
	MonthlyFee float64
	Payment    *StudentMonthlyPayment
}

type StudentGroup struct {
	ID           int64
	Name         string
	Code         string
	Description  string
	Students     []Admission
	StudentCount int
	Coaches      []User
	CoachCount   int
	CreatedAt    time.Time
}

type AttendanceSummary struct {
	SessionCount int
	PresentCount int
	AbsentCount  int
	LateCount    int
	ExcusedCount int
	TotalEntries int
}

type Court struct {
	ID          int64
	Name        string
	Code        string
	Description string
	Active      bool
	SortOrder   int
	Activities  []CourtActivity
	Layouts     []CourtLayout
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CourtActivity struct {
	ID          int64
	CourtID     int64
	Activity    string
	DisplayName string
	MaxQuantity int
	Active      bool
	SortOrder   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CourtLayout struct {
	ID          int64
	CourtID     int64
	Name        string
	Description string
	Active      bool
	SortOrder   int
	Items       []CourtLayoutItem
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CourtLayoutItem struct {
	ID          int64
	LayoutID    int64
	Activity    string
	DisplayName string
	Quantity    int
}

type CourtClosure struct {
	ID          int64
	CourtID     int64
	CourtName   string
	ClosureDate string
	StartHour   string
	EndHour     string
	Activity    string
	Title       string
	Reason      string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	ReferralCode    string
	QuotedPrice     float64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type BookingOption struct {
	Activity string
	Quantity int
	Label    string
}

type AdminBookingOption struct {
	Activity               string
	Quantity               int
	Label                  string
	PriceLabel             string
	AvailabilityState      string
	RemainingCapacity      int
	RemainingCapacityLabel string
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
	ID                       int64
	PeakStartHour            string
	PeakEndHour              string
	ReferralCommissionAmount float64
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type ReferralPartner struct {
	ID        int64
	Name      string
	Code      string
	Email     string
	Phone     string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ReferralPartnerSummary struct {
	Partner       ReferralPartner
	ReferralCount int
	PendingCount  int
	PayableCount  int
	PaidCount     int
	PayableAmount float64
	PaidAmount    float64
}

type BookingReferral struct {
	ID                   int64
	ScheduleID           int64
	PartnerID            int64
	PartnerName          string
	PartnerCode          string
	CommissionAmount     float64
	BookingStatus        string
	BookingReference     string
	BookingTitle         string
	SlotDate             string
	Paid                 bool
	PaidAt               time.Time
	PaymentMethod        string
	FinanceTransactionID int64
	CreatedAt            time.Time
}

type BookingRequestChange struct {
	ID                int64
	ScheduleID        int64
	PreviousSlotDate  string
	PreviousSlotHour  string
	PreviousActivity  string
	PreviousQuantity  int
	PreviousQuote     float64
	NewSlotDate       string
	NewSlotHour       string
	NewActivity       string
	NewQuantity       int
	NewQuote          float64
	ActionType        string
	ReviewNote        string
	ChangedByUserID   int64
	ChangedByUserName string
	ChangedAt         time.Time
}

type BookingCommunication struct {
	ID               int64
	ScheduleID       int64
	EventType        string
	RelatedEventType string
	EventKey         string
	Channel          string
	Recipient        string
	Subject          string
	BodyPreview      string
	Status           string
	Provider         string
	ProviderMessage  string
	AttemptCount     int
	LastAttemptAt    time.Time
	SentAt           time.Time
	CreatedAt        time.Time
	CreatedByUserID  int64
}

type BookingRequestSnapshot struct {
	SlotDate    string
	SlotHour    string
	Activity    string
	Quantity    int
	QuotedPrice float64
}

type BookingRequestRescheduleResult struct {
	Schedule *SpaceSchedule
	ChangeID int64
}

type AdmissionPricing struct {
	ID           int64
	PracticeType string
	Price        float64
	MonthlyFee   float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type TrainingProgram struct {
	ID             int64
	Name           string
	Activity       string
	TrainingFormat string
	AdmissionFee   float64
	MonthlyFee     float64
	Active         bool
	SortOrder      int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Event struct {
	ID                   int64
	Title                string
	Category             string
	EventDate            string
	StartTime            string
	EndTime              string
	RegistrationDeadline string
	Venue                string
	Summary              string
	ImagePath            string
	CTALabel             string
	CTALink              string
	Published            bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type AttendanceRecord struct {
	ID               int64
	GroupID          int64
	AdmissionID      int64
	AttendanceDate   string
	Status           string
	Note             string
	RecordedByUserID int64
	RecordedAt       time.Time
	UpdatedAt        time.Time
}

type BookingSlotAvailability struct {
	Hour          string
	Schedules     []SpaceSchedule
	Options       []BookingOption
	BlockedReason string
	IsPast        bool
}

type AdminCalendarItem struct {
	ID             int64
	Reference      string
	Title          string
	Summary        string
	Status         string
	EntryType      string
	RequesterName  string
	RequesterPhone string
	PriceLabel     string
	PaymentLabel   string
	PaymentTone    string
	ReferralCode   string
	ReviewNote     string
	ViewURL        string
	EditURL        string
	RequestURL     string
	CanConfirm     bool
	CanReschedule  bool
	IsPending      bool
	IsTraining     bool
	IsUnpaid       bool
}

type AdminCalendarHour struct {
	Hour              string
	State             string
	StateLabel        string
	StateClasses      string
	RemainingSummary  string
	BlockedReason     string
	IsPast            bool
	ConfirmedCount    int
	PendingCount      int
	TrainingCount     int
	UnpaidCount       int
	ExpectedRevenue   float64
	CollectedRevenue  float64
	Confirmed         []AdminCalendarItem
	Pending           []AdminCalendarItem
	Training          []AdminCalendarItem
	Closures          []CourtClosure
	AvailableOptions  []AdminBookingOption
	CanAddDirect      bool
	CanAddTraining    bool
	AddDirectURL      string
	AddTrainingURL    string
	ManageClosuresURL string
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
	IsPast        bool
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
	Title                     string
	Description               string
	CurrentPath               string
	User                      *User
	Viewer                    *User
	HideChrome                bool
	CSRFToken                 string
	Flash                     string
	Error                     string
	Stats                     []Stat
	Features                  []Feature
	Users                     []User
	Available                 []string
	Roles                     []Role
	Permissions               []string
	PermissionGroups          []PermissionGroup
	Admissions                []Admission
	SelectedAdmission         *Admission
	AdmissionMode             string
	StudentGroups             []StudentGroup
	SelectedGroup             *StudentGroup
	GroupMode                 string
	AvailableCoaches          []User
	AttendanceRecords         []AttendanceRecord
	AttendanceDate            string
	RecentDates               []string
	AttendanceSummary         AttendanceSummary
	Courts                    []Court
	SelectedCourt             *Court
	CourtMode                 string
	CourtActivities           []CourtActivity
	CourtLayouts              []CourtLayout
	SelectedCourtLayout       *CourtLayout
	CourtLayoutMode           string
	CourtClosures             []CourtClosure
	SelectedCourtClosure      *CourtClosure
	CourtClosureMode          string
	Schedules                 []SpaceSchedule
	DaySchedules              []SpaceSchedule
	PendingSchedules          []SpaceSchedule
	BookingRequests           []SpaceSchedule
	SelectedSchedule          *SpaceSchedule
	DraftSchedule             *SpaceSchedule
	ScheduleMode              string
	Pricings                  []PricingRule
	TrainingPrograms          []TrainingProgram
	SelectedTrainingProgram   *TrainingProgram
	TrainingProgramMode       string
	PricingSettings           *PricingSettings
	ReferralPartners          []ReferralPartner
	ReferralPartnerRows       []ReferralPartnerSummary
	BookingReferrals          []BookingReferral
	BookingRequestChanges     []BookingRequestChange
	ReferralStats             []Stat
	SelectedPricing           *PricingRule
	PricingMode               string
	Events                    []Event
	SelectedEvent             *Event
	EventMode                 string
	FinanceTransactions       []FinanceTransaction
	SelectedFinance           *FinanceTransaction
	FinanceFilter             FinanceFilter
	FinanceSummary            FinanceSummary
	BookingFinancials         []BookingFinancial
	BookingCommunications     []BookingCommunication
	Report                    *OperationalReport
	ReceiptAdmission          *Admission
	StudentPaymentRows        []StudentPaymentRow
	PaymentMonth              string
	PaymentMonthLabel         string
	PaymentTotalDue           float64
	PaymentCollected          float64
	PaymentOutstanding        float64
	PaymentPaidCount          int
	PaymentPendingCount       int
	BookingSlots              []BookingSlotAvailability
	AdminCalendarHours        []AdminCalendarHour
	AdminBookingOptions       []AdminBookingOption
	AdminBookingBlockedReason string
	WeekDays                  []CalendarDay
	BookingOptions            []BookingOption
	Activities                []string
	Hours                     []string
	CalendarDate              string
	PreviousDate              string
	NextDate                  string
	TodayDate                 string
	DailyStats                []Stat
	BookingRequestStats       []Stat
	PendingRequestCount       int
	CalendarCanGoBack         bool
	PendingEmail              string
	OTPCodeLength             int
	ResendAction              string
	SportsCatalog             []SportPage
	SelectedSport             *SportPage
	FAQItems                  []FAQItem
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
	ErrEmailTaken                = errors.New("email already exists")
	ErrInvalidOTP                = errors.New("invalid verification code")
	ErrMonthlyFeeNotConfigured   = errors.New("monthly fee is not configured for this training programme")
	ErrAdmissionFeeNotConfigured = errors.New(
		"admission fee is not configured for the selected training programme",
	)
	ErrStudentPaymentAlreadyCollected = errors.New("student payment already collected")
	ErrStudentNotAdmittedForMonth     = errors.New("student was not admitted for the selected month")
	ErrBookingPaymentAlreadyCollected = errors.New("booking payment already collected")
	ErrRoleAssigned                   = errors.New("role is assigned to one or more users")
	ErrSystemRoleProtected            = errors.New("system roles are protected")
)

const (
	bookingCommEventRequestReceived      = "booking_request_received"
	bookingCommEventConfirmed            = "booking_confirmed"
	bookingCommEventRejected             = "booking_rejected"
	bookingCommEventRescheduledPending   = "booking_rescheduled_pending"
	bookingCommEventRescheduledConfirmed = "booking_rescheduled_confirmed"
	bookingCommEventResent               = "booking_message_resent"
	bookingCommChannelEmail              = "email"
	bookingCommChannelSMS                = "sms"
	bookingCommStatusPending             = "pending"
	bookingCommStatusSent                = "sent"
	bookingCommStatusFailed              = "failed"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	addr := envOrDefault("ADDR", ":8080")
	dbPath := envOrDefault("DB_PATH", "app.db")
	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"
	uploadStorage, err := prepareUploadStorage(os.Getenv("UPLOAD_DIR"))
	if err != nil {
		log.Fatalf("prepare upload storage: %v", err)
	}
	log.Printf("event upload storage=%s", uploadStorage.EventDir)

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

	bookingMessageSettings := BookingCommunicationSettings{
		EmailEnabled: envOrDefault("BOOKING_EMAIL_ENABLED", "true") != "false",
		SMSEnabled:   envOrDefault("BOOKING_SMS_ENABLED", "true") != "false",
		ContactPhone: envOrDefault("MEKMAA_CONTACT_PHONE", "077 220 7297"),
		ContactEmail: envOrDefault("MEKMAA_CONTACT_EMAIL", "mekmaa.jo@gmail.com"),
		VenueName:    envOrDefault("MEKMAA_VENUE_NAME", "Mekmaa (Private Limited)"),
		VenueAddress: envOrDefault("MEKMAA_VENUE_ADDRESS", "No. 64, Temple Road, Jaffna - 40000, Sri Lanka"),
	}

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
	if err := seedCourtManager(db); err != nil {
		log.Fatalf("seed court manager: %v", err)
	}
	if err := verifyCourtManagerConfiguration(db); err != nil {
		log.Fatalf("verify court manager: %v", err)
	}
	if err := seedTrainingPrograms(db); err != nil {
		log.Fatalf("seed training programmes: %v", err)
	}
	if err := bootstrapSuperadmin(db); err != nil {
		log.Fatalf("bootstrap superadmin: %v", err)
	}

	templates, err := buildTemplates()
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	app := &App{
		db:              db,
		templates:       templates,
		cookieSecure:    cookieSecure,
		smtp:            smtpConfig,
		sms:             smsConfig,
		uploads:         uploadStorage,
		bookingMessages: bookingMessageSettings,
	}

	mux := http.NewServeMux()
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir("static/images"))))
	registerUploadRoutes(mux, uploadStorage)
	mux.HandleFunc("/", app.homeHandler)
	mux.HandleFunc("/about", app.aboutHandler)
	mux.HandleFunc("/book", app.publicBookingHandler)
	mux.HandleFunc("/book/request", app.publicBookingRequestHandler)
	mux.HandleFunc("/booking", app.legacyBookingRedirectHandler)
	mux.HandleFunc("/contact", app.contactHandler)
	mux.HandleFunc("/faq", app.faqHandler)
	mux.HandleFunc("/events", app.eventsHandler)
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
	mux.Handle("/dashboard", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.dashboardHandler), "dashboard.view")))
	mux.Handle("/editor", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.editorHandler), "editor.access")))
	mux.Handle("/admin", app.sessionMiddleware(http.HandlerFunc(app.adminRedirectHandler)))
	mux.Handle("/admin/users", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.userManagementHandler), "users.manage")))
	mux.Handle("/admin/roles", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.roleManagementHandler), "roles.manage")))
	mux.Handle("/admin/users/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createManagedUserHandler), "users.manage")))
	mux.Handle("/admin/users/roles", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateRolesHandler), "users.manage")))
	mux.Handle("/admin/roles/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createRoleHandler), "roles.manage")))
	mux.Handle("/admin/roles/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateRoleHandler), "roles.manage")))
	mux.Handle("/admin/roles/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteRoleHandler), "roles.manage")))
	mux.Handle("/admin/admissions", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.admissionManagementHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteAdmissionHandler), "admissions.manage")))
	mux.Handle(
		"/admin/training-programs",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.trainingProgramManagementHandler),
				"training_programs.manage",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/create",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.createTrainingProgramHandler),
				"training_programs.manage",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/update",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.updateTrainingProgramHandler),
				"training_programs.manage",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/toggle",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.toggleTrainingProgramHandler),
				"training_programs.manage",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/delete",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.deleteTrainingProgramHandler),
				"training_programs.manage",
			),
		),
	)
	mux.Handle("/admin/student-groups", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentGroupManagementHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/student-groups/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteStudentGroupHandler), "student_groups.manage")))
	mux.Handle("/admin/attendance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.attendanceManagementHandler), "attendance.manage")))
	mux.Handle("/admin/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveAttendanceHandler), "attendance.manage")))
	mux.Handle(
		"/admin/courts",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.courtManagementHandler),
				"courts.manage",
			),
		),
	)
	mux.Handle(
		"/admin/courts/layouts/create",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.createCourtLayoutHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/update",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.updateCourtLayoutHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/toggle",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.toggleCourtLayoutHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/delete",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.deleteCourtLayoutHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/create",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.createCourtClosureHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/update",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.updateCourtClosureHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/toggle",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.toggleCourtClosureHandler),
				"courts.manage",
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/delete",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.deleteCourtClosureHandler),
				"courts.manage",
			),
		),
	)
	mux.Handle("/admin/bookings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingManagementHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/options", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.adminBookingOptionsHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/communications/resend", app.sessionMiddleware(http.HandlerFunc(app.resendBookingCommunicationHandler)))
	mux.Handle("/admin/bookings/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/booking-requests", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingRequestsHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/reschedule", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.rescheduleBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/reschedule-confirm", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.rescheduleAndConfirmBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/confirm", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.confirmBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/reject", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.rejectBookingRequestHandler), "booking_requests.manage")))
	mux.Handle("/admin/pricing", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.pricingManagementHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createPricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updatePricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deletePricingHandler), "pricing.manage")))
	mux.Handle("/admin/pricing/settings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updatePricingSettingsHandler), "pricing.manage")))
	mux.Handle("/admin/events", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.eventManagementHandler), "events.manage")))
	mux.Handle("/admin/events/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createEventHandler), "events.manage")))
	mux.Handle("/admin/events/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateEventHandler), "events.manage")))
	mux.Handle("/admin/events/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteEventHandler), "events.manage")))
	mux.Handle("/admin/finance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeManagementHandler), "finance.manage")))
	mux.Handle("/admin/finance/transactions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceTransactionHandler), "finance.manage")))
	mux.Handle("/admin/finance/bookings/collect", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectBookingPaymentHandler), "finance.manage")))
	mux.Handle("/admin/finance/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeExportHandler), "finance.manage")))
	mux.Handle("/admin/reports", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsHandler), "reports.view")))
	mux.Handle("/admin/reports/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsExportHandler), "reports.view")))
	mux.Handle("/admin/referrals", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.referralCommissionsHandler), "finance.manage")))
	mux.Handle("/admin/referrals/settings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateReferralSettingsHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/toggle", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.toggleReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/pay", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payReferralCommissionHandler), "finance.manage")))
	mux.Handle("/admin/student-payments", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentPaymentsHandler), "finance.manage")))
	mux.Handle("/admin/student-payments/collect", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectStudentPaymentHandler), "finance.manage")))
	mux.Handle("/admin/finance/receipt", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.financeReceiptHandler), "admissions.manage", "finance.manage")))

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
	events, err := a.listPublishedEvents()
	if err != nil {
		log.Printf("list published events for home: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Events = upcomingEvents(events, 3)

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
	tmplName, ok := sportTemplateNameBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.render(w, tmplName, data, http.StatusOK)
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

func (a *App) eventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/events" {
		http.NotFound(w, r)
		return
	}
	data := a.newTemplateData(w, r, nil)
	data.Title = "Events at Mekmaa"
	data.Description = "Explore upcoming sports, coaching and community events at Mekmaa."
	events, err := a.listPublishedEvents()
	if err != nil {
		log.Printf("list published events: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Events = events
	a.render(w, "events", data, http.StatusOK)
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
	schedule.ReferralCode = strings.ToUpper(strings.TrimSpace(r.FormValue("referral_code")))
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
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writePublicBookingError(w, r, &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	pricings, err := a.listPricingRules()
	if err != nil {
		log.Printf("list pricing for booking request: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		log.Printf("get pricing settings for booking request: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	rule := pricingRuleForOption(pricings, schedule.Activity, schedule.Quantity)
	if rule == nil || priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour) <= 0 {
		a.writePublicBookingError(w, r, &schedule, "This session does not currently have a configured online price. Please choose another available option.", http.StatusBadRequest)
		return
	}
	schedule.QuotedPrice = priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour)
	requestID, err := a.createPublicBookingRequest(schedule)
	if err != nil {
		a.writePublicBookingError(w, r, &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	results, commErr := a.sendBookingCommunicationEvent(
		requestID,
		bookingCommEventRequestReceived,
		"",
		fmt.Sprintf("schedule:%d:%s", requestID, bookingCommEventRequestReceived),
		0,
	)
	if commErr != nil {
		log.Printf("send booking request received communication: %v", commErr)
	}
	emailSent := communicationDelivered(results, bookingCommChannelEmail)
	if emailSent {
		a.setFlash(w, "Booking request "+bookingReference(requestID)+" received. We emailed the pending request details to you.")
	} else {
		a.setFlash(w, "Booking request "+bookingReference(requestID)+" received. Keep this reference; our team will review the request shortly. We could not confirm email delivery automatically.")
	}
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
	user, _ := a.currentUser(r.Context())
	destinations := []struct {
		permission string
		path       string
	}{
		{"users.manage", "/admin/users"},
		{"roles.manage", "/admin/roles"},
		{"admissions.manage", "/admin/admissions"},
		{"training_programs.manage", "/admin/training-programs"},
		{"student_groups.manage", "/admin/student-groups"},
		{"attendance.manage", "/admin/attendance"},
		{"courts.manage", "/admin/courts"},
		{"space_bookings.manage", "/admin/bookings"},
		{"booking_requests.manage", "/admin/booking-requests"},
		{"finance.manage", "/admin/finance"},
		{"pricing.manage", "/admin/pricing"},
		{"reports.view", "/admin/reports"},
		{"events.manage", "/admin/events"},
	}
	for _, destination := range destinations {
		if containsPermission(user.Permissions, destination.permission) {
			http.Redirect(w, r, destination.path, http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
	for _, role := range roles {
		if isPrivilegedRole(role.Name) && !containsRole(user.Roles, "superadmin") {
			continue
		}
		data.Available = append(data.Available, role.Name)
	}
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
	data.PermissionGroups = permissionGroups
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
	trainingPrograms, err := a.listTrainingPrograms(true)
	if err != nil {
		log.Printf("list training programmes for admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Admissions Management"
	data.Description = "Manage admissions."
	data.Admissions = admissions
	data.TrainingPrograms = trainingPrograms
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

func (a *App) trainingProgramManagementHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())

	trainingPrograms, err := a.listTrainingPrograms(true)
	if err != nil {
		log.Printf("list training programmes: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Training Manager"
	data.Description = "Manage training programmes and student fees."
	data.TrainingPrograms = trainingPrograms

	mode := strings.ToLower(
		strings.TrimSpace(r.URL.Query().Get("action")),
	)

	switch mode {
	case "new", "view", "edit":
		data.TrainingProgramMode = mode
	}

	if data.TrainingProgramMode == "view" ||
		data.TrainingProgramMode == "edit" {
		programID, err := strconv.ParseInt(
			strings.TrimSpace(r.URL.Query().Get("id")),
			10,
			64,
		)
		if err != nil || programID <= 0 {
			http.Error(
				w,
				"invalid training programme id",
				http.StatusBadRequest,
			)
			return
		}

		selectedProgram, err := a.findTrainingProgramByID(programID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(
					w,
					"training programme not found",
					http.StatusNotFound,
				)
				return
			}

			log.Printf("find training programme: %v", err)
			http.Error(
				w,
				"internal server error",
				http.StatusInternalServerError,
			)
			return
		}

		data.SelectedTrainingProgram = selectedProgram
	}

	a.render(
		w,
		"training-program-management",
		data,
		http.StatusOK,
	)
}

func (a *App) createTrainingProgramHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
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

	program, err := trainingProgramFromRequest(r)
	if err != nil {
		a.setFlash(w, "Training programme could not be created: "+err.Error())
		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=new",
			http.StatusSeeOther,
		)
		return
	}

	programID, err := a.createTrainingProgram(program)
	if err != nil {
		log.Printf("create training programme: %v", err)

		message := "Training programme could not be created."

		if isUniqueConstraintError(err) {
			message = "A programme already exists for this activity and training format."
		}

		a.setFlash(w, message)
		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=new",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme created successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs?action=view&id="+
			strconv.FormatInt(programID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) updateTrainingProgramHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
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

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}

	program, err := trainingProgramFromRequest(r)
	if err != nil {
		a.setFlash(w, "Training programme could not be updated: "+err.Error())

		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=edit&id="+
				strconv.FormatInt(programID, 10),
			http.StatusSeeOther,
		)
		return
	}

	program.ID = programID

	if err := a.updateTrainingProgram(program); err != nil {
		log.Printf("update training programme: %v", err)

		message := "Training programme could not be updated."

		if isUniqueConstraintError(err) {
			message = "A programme already exists for this activity and training format."
		}

		a.setFlash(w, message)

		http.Redirect(
			w,
			r,
			"/admin/training-programs?action=edit&id="+
				strconv.FormatInt(programID, 10),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme updated successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs?action=view&id="+
			strconv.FormatInt(programID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) toggleTrainingProgramHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
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

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}

	active, err := strconv.ParseBool(
		strings.TrimSpace(r.FormValue("active")),
	)
	if err != nil {
		http.Error(w, "invalid programme status", http.StatusBadRequest)
		return
	}

	if err := a.setTrainingProgramActive(programID, active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "training programme not found", http.StatusNotFound)
			return
		}

		log.Printf("toggle training programme: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if active {
		a.setFlash(w, "Training programme activated successfully.")
	} else {
		a.setFlash(w, "Training programme deactivated successfully.")
	}

	http.Redirect(
		w,
		r,
		"/admin/training-programs",
		http.StatusSeeOther,
	)
}

func (a *App) deleteTrainingProgramHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
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

	programID, err := parsePositiveInt64(r.FormValue("id"))
	if err != nil {
		http.Error(w, "invalid training programme id", http.StatusBadRequest)
		return
	}

	if err := a.deleteTrainingProgram(programID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "training programme not found", http.StatusNotFound)
			return
		}

		a.setFlash(w, "Training programme could not be deleted: "+err.Error())

		http.Redirect(
			w,
			r,
			"/admin/training-programs",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Training programme deleted successfully.")

	http.Redirect(
		w,
		r,
		"/admin/training-programs",
		http.StatusSeeOther,
	)
}

func trainingProgramFromRequest(
	r *http.Request,
) (TrainingProgram, error) {
	name := strings.TrimSpace(r.FormValue("name"))
	activity := normalizeTrainingActivity(
		r.FormValue("activity"),
	)
	trainingFormat := strings.ToLower(
		strings.TrimSpace(r.FormValue("training_format")),
	)

	admissionFee, err := parseNonNegativeFloat(
		r.FormValue("admission_fee"),
	)
	if err != nil {
		return TrainingProgram{}, errors.New(
			"enter a valid admission fee",
		)
	}

	monthlyFee, err := parseNonNegativeFloat(
		r.FormValue("monthly_fee"),
	)
	if err != nil {
		return TrainingProgram{}, errors.New(
			"enter a valid monthly fee",
		)
	}

	sortOrder := 0

	if value := strings.TrimSpace(r.FormValue("sort_order")); value != "" {
		sortOrder, err = strconv.Atoi(value)
		if err != nil || sortOrder < 0 || sortOrder > 100000 {
			return TrainingProgram{}, errors.New(
				"sort order must be between 0 and 100000",
			)
		}
	}

	program := TrainingProgram{
		Name:           name,
		Activity:       activity,
		TrainingFormat: trainingFormat,
		AdmissionFee:   admissionFee,
		MonthlyFee:     monthlyFee,
		Active:         r.FormValue("active") == "on",
		SortOrder:      sortOrder,
	}

	if err := validateTrainingProgram(program); err != nil {
		return TrainingProgram{}, err
	}

	return program, nil
}

func validateTrainingProgram(program TrainingProgram) error {
	if program.Name == "" {
		return errors.New("programme name is required")
	}

	if len(program.Name) > 120 {
		return errors.New(
			"programme name must not exceed 120 characters",
		)
	}

	if program.Activity == "" {
		return errors.New("activity is required")
	}

	if len(program.Activity) > 60 {
		return errors.New(
			"activity must not exceed 60 characters",
		)
	}

	switch program.TrainingFormat {
	case "one_to_one", "group":
	default:
		return errors.New(
			"training format must be one-to-one or group",
		)
	}

	if math.IsNaN(program.AdmissionFee) ||
		math.IsInf(program.AdmissionFee, 0) ||
		program.AdmissionFee < 0 {
		return errors.New("admission fee cannot be negative")
	}

	if math.IsNaN(program.MonthlyFee) ||
		math.IsInf(program.MonthlyFee, 0) ||
		program.MonthlyFee < 0 {
		return errors.New("monthly fee cannot be negative")
	}

	return nil
}

func normalizeTrainingActivity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "&", " and ")

	var result strings.Builder
	previousSeparator := false

	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			result.WriteRune(character)
			previousSeparator = false

		case character >= '0' && character <= '9':
			result.WriteRune(character)
			previousSeparator = false

		case !previousSeparator:
			result.WriteRune('_')
			previousSeparator = true
		}
	}

	return strings.Trim(
		result.String(),
		"_",
	)
}

func parseNonNegativeFloat(value string) (float64, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return 0, nil
	}

	number, err := strconv.ParseFloat(value, 64)
	if err != nil ||
		math.IsNaN(number) ||
		math.IsInf(number, 0) ||
		number < 0 {
		return 0, errors.New("invalid non-negative number")
	}

	return number, nil
}

func parsePositiveInt64(value string) (int64, error) {
	number, err := strconv.ParseInt(
		strings.TrimSpace(value),
		10,
		64,
	)
	if err != nil || number <= 0 {
		return 0, errors.New("invalid positive integer")
	}

	return number, nil
}

func legacyPracticeTypeForTrainingFormat(trainingFormat string) string {
	switch trainingFormat {
	case "one_to_one":
		return "one_to_one_practice"
	case "group":
		return "group_practice"
	default:
		return ""
	}
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())

	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "is not unique")
}

func (a *App) financeManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	filter := financeFilterFromRequest(r)
	financeTransactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		log.Printf("list finance transactions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	allTransactions, err := a.listFinanceTransactions()
	if err != nil {
		log.Printf("list finance summary transactions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	bookingFinancials, err := a.listOutstandingBookingFinancials()
	if err != nil {
		log.Printf("list booking receivables: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	monthlyRows, err := a.listStudentPaymentRows(time.Now().Format("2006-01"))
	if err != nil {
		log.Printf("list monthly receivables: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	referrals, err := a.listBookingReferrals()
	if err != nil {
		log.Printf("list referral payables: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Finance"
	data.Description = "Monitor cash flow, receivables, expenses, and payment history."
	data.FinanceTransactions = financeTransactions
	data.FinanceFilter = filter
	data.BookingFinancials = bookingFinancials
	data.FinanceSummary = buildFinanceSummary(allTransactions, bookingFinancials, monthlyRows, referrals)
	data.Stats = buildFinanceStats(allTransactions)
	data.TodayDate = time.Now().Format("2006-01-02")
	a.render(w, "finance-management", data, http.StatusOK)
}

func (a *App) createFinanceTransactionHandler(w http.ResponseWriter, r *http.Request) {
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
	direction := strings.ToLower(strings.TrimSpace(r.FormValue("direction")))
	category := strings.ToLower(strings.TrimSpace(r.FormValue("category")))
	if !validManualFinanceCategory(direction, category) {
		http.Error(w, "invalid finance category", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil || amount <= 0 {
		http.Error(w, "amount must be greater than zero", http.StatusBadRequest)
		return
	}
	personName := strings.TrimSpace(r.FormValue("person_name"))
	description := strings.TrimSpace(r.FormValue("description"))
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	if personName == "" || description == "" || !validPaymentMethod(paymentMethod) {
		http.Error(w, "person, description, and valid payment method are required", http.StatusBadRequest)
		return
	}
	recordedAt := time.Now()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil || recordedAt.After(time.Now().Add(24*time.Hour)) {
			http.Error(w, "invalid recorded date", http.StatusBadRequest)
			return
		}
	}
	if direction == "expense" {
		amount = -amount
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.createManualFinanceTransaction(category, personName, description, paymentMethod, amount, recordedAt, recordedBy)
	if err != nil {
		log.Printf("create manual finance transaction: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) collectBookingPaymentHandler(w http.ResponseWriter, r *http.Request) {
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
	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	if err != nil || scheduleID <= 0 || !validPaymentMethod(paymentMethod) {
		http.Error(w, "valid booking and payment method are required", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.collectBookingPayment(scheduleID, paymentMethod, recordedBy)
	if err != nil {
		a.setFlash(w, "Booking payment could not be collected: "+err.Error())
		http.Redirect(w, r, "/admin/finance#booking-receivables", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) financeExportHandler(w http.ResponseWriter, r *http.Request) {
	transactions, err := a.listFinanceTransactionsFiltered(financeFilterFromRequest(r))
	if err != nil {
		http.Error(w, "could not export finance transactions", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mekmaa-finance.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Receipt", "Date", "Direction", "Category", "Person", "Description", "Payment method", "Amount (LKR)"})
	for _, transaction := range transactions {
		direction := "Income"
		if transaction.Amount < 0 {
			direction = "Expense"
		}
		_ = writer.Write([]string{
			csvSafeCell(transaction.ReceiptNumber), transaction.RecordedAt.Format("2006-01-02 15:04"), direction,
			financeCategoryLabel(transaction.Category), csvSafeCell(transaction.PersonName), csvSafeCell(transaction.Description),
			csvSafeCell(transaction.PaymentMethod), strconv.FormatFloat(transaction.Amount, 'f', 2, 64),
		})
	}
	writer.Flush()
}

func (a *App) reportsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	period := reportPeriodFromRequest(r)
	report, err := a.buildOperationalReport(period)
	if err != nil {
		log.Printf("build operational report: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Reports"
	data.Description = "Daily, weekly, and monthly performance reporting."
	data.Report = report
	a.render(w, "reports", data, http.StatusOK)
}

func (a *App) reportsExportHandler(w http.ResponseWriter, r *http.Request) {
	period := reportPeriodFromRequest(r)
	report, err := a.buildOperationalReport(period)
	if err != nil {
		http.Error(w, "could not export report", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("mekmaa-%s-report-%s.csv", period.Kind, period.Anchor)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Mekmaa operational report", report.Period.Label})
	_ = writer.Write([]string{"Period", report.Period.Start, report.Period.End})
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"SUMMARY", "VALUE"})
	summaryRows := [][]string{
		{"Gross income (LKR)", formatReportNumber(report.Summary.Income)},
		{"Expenses (LKR)", formatReportNumber(report.Summary.Expenses)},
		{"Net cash (LKR)", formatReportNumber(report.Summary.NetCash)},
		{"Confirmed bookings", strconv.Itoa(report.Summary.ConfirmedBookings)},
		{"Pending bookings", strconv.Itoa(report.Summary.PendingBookings)},
		{"New admissions", strconv.Itoa(report.Summary.NewAdmissions)},
		{"Student payments", strconv.Itoa(report.Summary.StudentPayments)},
		{"Attendance rate", fmt.Sprintf("%.1f%%", report.Summary.AttendanceRate)},
		{"Facility utilization", fmt.Sprintf("%.1f%%", report.Summary.UtilizationRate)},
	}
	for _, row := range summaryRows {
		_ = writer.Write(row)
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"DAILY TREND", "DATE", "INCOME", "EXPENSES", "NET CASH", "BOOKINGS", "ADMISSIONS", "PRESENT", "ATTENDANCE RECORDS"})
	for _, point := range report.Series {
		_ = writer.Write([]string{
			"", point.Date, formatReportNumber(point.Income), formatReportNumber(point.Expenses),
			formatReportNumber(point.NetCash), strconv.Itoa(point.Bookings), strconv.Itoa(point.Admissions),
			strconv.Itoa(point.Present), strconv.Itoa(point.Attendance),
		})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"FINANCE BREAKDOWN", "CATEGORY", "TRANSACTIONS", "AMOUNT"})
	for _, item := range report.FinanceBreakdown {
		_ = writer.Write([]string{"", item.Label, strconv.Itoa(item.Count), formatReportNumber(item.Amount)})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"BOOKING MIX", "ACTIVITY", "CONFIRMED BOOKINGS"})
	for _, item := range report.BookingBreakdown {
		_ = writer.Write([]string{"", item.Label, strconv.Itoa(item.Count)})
	}
	_ = writer.Write([]string{})
	_ = writer.Write([]string{"TRANSACTIONS", "RECEIPT", "DATE", "DIRECTION", "CATEGORY", "PARTY", "DESCRIPTION", "METHOD", "AMOUNT"})
	for _, transaction := range report.Transactions {
		direction := "Income"
		if transaction.Amount < 0 {
			direction = "Expense"
		}
		_ = writer.Write([]string{
			"", csvSafeCell(transaction.ReceiptNumber), transaction.RecordedAt.Format("2006-01-02 15:04"),
			direction, financeCategoryLabel(transaction.Category), csvSafeCell(transaction.PersonName),
			csvSafeCell(transaction.Description), csvSafeCell(transaction.PaymentMethod), formatReportNumber(transaction.Amount),
		})
	}
	writer.Flush()
}

func (a *App) referralCommissionsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	referrals, err := a.listBookingReferrals()
	if err != nil {
		log.Printf("list booking referrals: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	partners, err := a.listReferralPartners(false)
	if err != nil {
		log.Printf("list referral partners: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		log.Printf("get referral settings: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Referral Management"
	data.Description = "Manage the shared commission rate, referral partners, earnings, and payouts."
	data.BookingReferrals = referrals
	data.ReferralPartners = partners
	data.ReferralPartnerRows = buildReferralPartnerSummaries(partners, referrals)
	data.ReferralStats = buildReferralStats(referrals)
	data.PricingSettings = settings
	a.render(w, "referral-commissions", data, http.StatusOK)
}

func (a *App) studentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	paymentMonth := strings.TrimSpace(r.URL.Query().Get("month"))
	currentMonth := time.Now().Format("2006-01")
	if _, err := parsePaymentMonth(paymentMonth); err != nil || paymentMonth > currentMonth {
		paymentMonth = time.Now().Format("2006-01")
	}

	rows, err := a.listStudentPaymentRows(paymentMonth)
	if err != nil {
		log.Printf("list student payments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Payments"
	data.Description = "Collect and track individual monthly student payments."
	data.StudentPaymentRows = rows
	data.PaymentMonth = paymentMonth
	data.PaymentMonthLabel = paymentMonthLabel(paymentMonth)
	data.TodayDate = time.Now().Format("2006-01")
	for _, row := range rows {
		if row.Payment != nil {
			data.PaymentTotalDue += row.Payment.Amount
			data.PaymentCollected += row.Payment.Amount
			data.PaymentPaidCount++
		} else {
			data.PaymentTotalDue += row.MonthlyFee
			data.PaymentOutstanding += row.MonthlyFee
			data.PaymentPendingCount++
		}
	}
	a.render(w, "student-payments", data, http.StatusOK)
}

func (a *App) financeReceiptHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	transactionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("transaction_id")), 10, 64)
	if err != nil || transactionID <= 0 {
		http.Error(w, "invalid transaction id", http.StatusBadRequest)
		return
	}

	transaction, err := a.findFinanceTransactionByID(transactionID)
	if err != nil {
		http.Error(w, "receipt not found", http.StatusNotFound)
		return
	}
	if user == nil || (!containsPermission(user.Permissions, "finance.manage") && transaction.Category != "admission_payment") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var receiptAdmission *Admission
	if transaction.ReferenceType == "admission" && transaction.ReferenceID > 0 {
		receiptAdmission, _ = a.findAdmissionByID(transaction.ReferenceID)
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Payment Receipt"
	data.Description = "Printable receipt."
	data.HideChrome = true
	data.SelectedFinance = transaction
	data.ReceiptAdmission = receiptAdmission
	a.render(w, "finance-receipt", data, http.StatusOK)
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

	coaches, err := a.listCoachUsers()
	if err != nil {
		log.Printf("list coach users for student groups: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Student Groups"
	data.Description = "Manage student groups."
	data.StudentGroups = groups
	data.Admissions = admissions
	data.AvailableCoaches = coaches
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

	var (
		groups []StudentGroup
		err    error
	)

	if userHasRole(user, "coach") &&
		!userHasRole(user, "admin") &&
		!userHasRole(user, "superadmin") {
		groups, err = a.listStudentGroupsForCoach(user.ID)
	} else {
		groups, err = a.listStudentGroups()
	}

	if err != nil {
		log.Printf("list student groups for attendance: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Attendance"
	data.Description = "Manage student attendance by group."
	data.StudentGroups = groups
	data.TodayDate = time.Now().Format("2006-01-02")
	data.AttendanceDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.AttendanceDate == "" {
		data.AttendanceDate = data.TodayDate
	} else if parsedDate, err := time.Parse("2006-01-02", data.AttendanceDate); err != nil {
		data.AttendanceDate = data.TodayDate
	} else if parsedDate.Format("2006-01-02") > data.TodayDate {
		data.AttendanceDate = data.TodayDate
	} else {
		data.AttendanceDate = parsedDate.Format("2006-01-02")
	}

	groupID, err := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("group_id")),
		10,
		64,
	)

	if err == nil && groupID > 0 {
		var selectedGroup *StudentGroup

		for i := range groups {
			if groups[i].ID == groupID {
				selectedGroup = &groups[i]
				break
			}
		}

		if selectedGroup != nil {
			data.SelectedGroup = selectedGroup

			records, err := a.listAttendanceRecords(
				groupID,
				data.AttendanceDate,
			)
			if err != nil {
				log.Printf("list attendance records: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.AttendanceRecords = records

			recentDates, err := a.listRecentAttendanceDates(groupID, 8)
			if err != nil {
				log.Printf("list recent attendance dates: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.RecentDates = recentDates

			summary, err := a.getAttendanceSummary(groupID)
			if err != nil {
				log.Printf("get attendance summary: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.AttendanceSummary = summary
		}
	}

	a.render(w, "attendance-management", data, http.StatusOK)
}

func (a *App) courtManagementHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	courts, err := a.listCourts(true)
	if err != nil {
		log.Printf("list courts: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Court Manager"
	data.Description = "Manage court activities and simultaneous-use configurations."
	data.Courts = courts

	courtID, err := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("court_id")),
		10,
		64,
	)
	if err != nil || courtID <= 0 {
		if len(courts) > 0 {
			courtID = courts[0].ID
		}
	}

	if courtID > 0 {
		selectedCourt, err := a.findCourtByID(courtID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				log.Printf("find court: %v", err)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}
		} else {
			data.SelectedCourt = selectedCourt
			data.CourtActivities = selectedCourt.Activities
			data.CourtLayouts = selectedCourt.Layouts

			closures, err := a.listCourtClosures(
				selectedCourt.ID,
				true,
			)
			if err != nil {
				log.Printf(
					"list court closures: %v",
					err,
				)
				http.Error(
					w,
					"internal server error",
					http.StatusInternalServerError,
				)
				return
			}

			data.CourtClosures = closures

			mode := strings.ToLower(
				strings.TrimSpace(
					r.URL.Query().Get("action"),
				),
			)

			closureAction := strings.ToLower(
				strings.TrimSpace(
					r.URL.Query().Get("closure_action"),
				),
			)

			switch closureAction {
			case "new", "edit":
				data.CourtClosureMode = closureAction
			}

			if data.CourtClosureMode == "edit" {
				closureID, err := strconv.ParseInt(
					strings.TrimSpace(
						r.URL.Query().Get("closure_id"),
					),
					10,
					64,
				)

				if err == nil && closureID > 0 {
					closure, err :=
						a.findCourtClosureByID(
							closureID,
						)

					if err == nil &&
						closure.CourtID ==
							selectedCourt.ID {
						data.SelectedCourtClosure =
							closure
					}
				}
			}

			switch mode {
			case "new", "edit":
				data.CourtLayoutMode = mode
			}

			if data.CourtLayoutMode == "edit" {
				layoutID, err := strconv.ParseInt(
					strings.TrimSpace(
						r.URL.Query().Get("layout_id"),
					),
					10,
					64,
				)

				if err == nil && layoutID > 0 {
					layout, err := a.findCourtLayoutByID(
						layoutID,
					)
					if err == nil &&
						layout.CourtID == selectedCourt.ID {
						data.SelectedCourtLayout = layout
					}
				}
			}
		}
	}

	a.render(
		w,
		"court-management",
		data,
		http.StatusOK,
	)
}

func (a *App) createCourtLayoutHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layout, err := courtLayoutFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(r.FormValue("court_id"))+
				"&action=new#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	layoutID, err := a.createCourtLayout(layout)
	if err != nil {
		log.Printf("create court layout: %v", err)
		a.setFlash(
			w,
			"Unable to create the court layout: "+
				err.Error(),
		)
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(layout.CourtID, 10)+
				"&action=new#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Court layout created successfully.")

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(layout.CourtID, 10)+
			"&action=edit&layout_id="+
			strconv.FormatInt(layoutID, 10)+
			"#layout-form",
		http.StatusSeeOther,
	)
}

func (a *App) updateCourtLayoutHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	layout, err := courtLayoutFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(r.FormValue("court_id"))+
				"&action=edit&layout_id="+
				strconv.FormatInt(layoutID, 10)+
				"#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	layout.ID = layoutID

	if err := a.updateCourtLayout(layout); err != nil {
		log.Printf("update court layout: %v", err)
		a.setFlash(
			w,
			"Unable to update the court layout: "+
				err.Error(),
		)
		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(layout.CourtID, 10)+
				"&action=edit&layout_id="+
				strconv.FormatInt(layout.ID, 10)+
				"#layout-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Court layout updated successfully.")

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(layout.CourtID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) toggleCourtLayoutHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.toggleCourtLayout(layoutID); err != nil {
		log.Printf("toggle court layout: %v", err)
		a.setFlash(
			w,
			"Unable to update the court layout.",
		)
	} else {
		a.setFlash(
			w,
			"Court layout status updated.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(r.FormValue("court_id")),
		http.StatusSeeOther,
	)
}

func (a *App) deleteCourtLayoutHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	layoutID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("layout_id"),
		),
		10,
		64,
	)
	if err != nil || layoutID <= 0 {
		http.Error(
			w,
			"invalid court layout",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.deleteCourtLayout(layoutID); err != nil {
		log.Printf("delete court layout: %v", err)
		a.setFlash(
			w,
			"Unable to delete the court layout: "+
				err.Error(),
		)
	} else {
		a.setFlash(
			w,
			"Court layout deleted successfully.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(r.FormValue("court_id")),
		http.StatusSeeOther,
	)
}

func (a *App) createCourtClosureHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closure, err :=
		courtClosureFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(
					r.FormValue("court_id"),
				)+
				"&closure_action=new"+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	closureID, err :=
		a.createCourtClosure(closure)
	if err != nil {
		log.Printf(
			"create court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to create the closure: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(
					closure.CourtID,
					10,
				)+
				"&closure_action=new"+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Court closure created successfully.",
	)

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(
				closure.CourtID,
				10,
			)+
			"&closure_action=edit"+
			"&closure_id="+
			strconv.FormatInt(
				closureID,
				10,
			)+
			"#closure-form",
		http.StatusSeeOther,
	)
}

func (a *App) updateCourtClosureHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	closure, err :=
		courtClosureFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				url.QueryEscape(
					r.FormValue("court_id"),
				)+
				"&closure_action=edit"+
				"&closure_id="+
				strconv.FormatInt(
					closureID,
					10,
				)+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	closure.ID = closureID

	if err := a.updateCourtClosure(
		closure,
	); err != nil {
		log.Printf(
			"update court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to update the closure: "+
				err.Error(),
		)

		http.Redirect(
			w,
			r,
			"/admin/courts?court_id="+
				strconv.FormatInt(
					closure.CourtID,
					10,
				)+
				"&closure_action=edit"+
				"&closure_id="+
				strconv.FormatInt(
					closure.ID,
					10,
				)+
				"#closure-form",
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(
		w,
		"Court closure updated successfully.",
	)

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			strconv.FormatInt(
				closure.CourtID,
				10,
			),
		http.StatusSeeOther,
	)
}

func (a *App) toggleCourtClosureHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.toggleCourtClosure(
		closureID,
	); err != nil {
		log.Printf(
			"toggle court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to update the closure.",
		)
	} else {
		a.setFlash(
			w,
			"Court closure status updated.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(
				r.FormValue("court_id"),
			),
		http.StatusSeeOther,
	)
}

func (a *App) deleteCourtClosureHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid csrf token",
			http.StatusForbidden,
		)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(
			w,
			"invalid form submission",
			http.StatusBadRequest,
		)
		return
	}

	closureID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("closure_id"),
		),
		10,
		64,
	)
	if err != nil || closureID <= 0 {
		http.Error(
			w,
			"invalid court closure",
			http.StatusBadRequest,
		)
		return
	}

	if err := a.deleteCourtClosure(
		closureID,
	); err != nil {
		log.Printf(
			"delete court closure: %v",
			err,
		)

		a.setFlash(
			w,
			"Unable to delete the closure: "+
				err.Error(),
		)
	} else {
		a.setFlash(
			w,
			"Court closure deleted successfully.",
		)
	}

	http.Redirect(
		w,
		r,
		"/admin/courts?court_id="+
			url.QueryEscape(
				r.FormValue("court_id"),
			),
		http.StatusSeeOther,
	)
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
	if data.ScheduleMode == "new" {
		data.DraftSchedule = prefillAdminBookingDraft(r, data.CalendarDate)
	}
	if data.ScheduleMode == "view" || data.ScheduleMode == "edit" {
		scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && scheduleID > 0 {
			selectedSchedule, err := a.findSpaceScheduleByID(scheduleID)
			if err == nil {
				if data.ScheduleMode == "edit" {
					applyAdminBookingQueryDraft(r, selectedSchedule)
				}
				data.SelectedSchedule = selectedSchedule
			}
		}
	}
	a.render(w, "booking-management", data, http.StatusOK)
}

func (a *App) adminBookingOptionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slotDate := strings.TrimSpace(r.URL.Query().Get("slot_date"))
	slotHour := strings.TrimSpace(r.URL.Query().Get("slot_hour"))
	entryType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("entry_type")))
	if entryType == "" {
		entryType = "booking"
	}

	candidate := SpaceSchedule{
		EntryType: entryType,
		SlotDate:  slotDate,
		SlotHour:  slotHour,
	}

	scheduleID, _ := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("schedule_id")),
		10,
		64,
	)

	if _, err := time.Parse("2006-01-02", slotDate); err != nil {
		http.Error(w, "invalid slot date", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("15:04", slotHour); err != nil {
		http.Error(w, "invalid slot hour", http.StatusBadRequest)
		return
	}
	if entryType != "booking" && entryType != "training" {
		http.Error(w, "invalid entry type", http.StatusBadRequest)
		return
	}

	options, blockedReason, err := a.adminBookingOptionsForSchedule(candidate, scheduleID)
	if err != nil {
		log.Printf("build admin booking options: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"options":        options,
		"blocked_reason": blockedReason,
	}); err != nil {
		log.Printf("encode admin booking options: %v", err)
	}
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
	data.BookingCommunications, err = a.listBookingCommunicationsForScheduleIDs(scheduleIDs(data.BookingRequests))
	if err != nil {
		log.Printf("list booking communications: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("action")), "reschedule") {
		if requestID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64); err == nil && requestID > 0 {
			if selectedRequest, err := a.findSpaceScheduleByID(requestID); err == nil &&
				selectedRequest.Status == "pending" &&
				selectedRequest.EntryType == "booking" &&
				(selectedRequest.RequesterName != "" || selectedRequest.RequesterEmail != "" || selectedRequest.RequestedByUser > 0) {
				draft := *selectedRequest
				applyAdminBookingQueryDraft(r, &draft)
				draft.ReviewNote = strings.TrimSpace(r.URL.Query().Get("review_note"))
				data.SelectedSchedule = selectedRequest
				data.DraftSchedule = &draft

				options, blockedReason, optionErr := a.adminBookingOptionsForSchedule(draft, selectedRequest.ID)
				if optionErr != nil {
					log.Printf("build booking request reschedule options: %v", optionErr)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}

				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		}
	}

	a.render(w, "booking-requests", data, http.StatusOK)
}

func (a *App) resendBookingCommunicationHandler(w http.ResponseWriter, r *http.Request) {
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

	currentUser, _ := a.currentUser(r.Context())
	if currentUser == nil || (!containsPermission(currentUser.Permissions, "space_bookings.manage") && !containsPermission(currentUser.Permissions, "booking_requests.manage")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}

	communications, err := a.listBookingCommunicationsForScheduleIDs([]int64{scheduleID})
	if err != nil {
		log.Printf("list booking communications for resend: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	relatedEventType := latestResendableEventType(schedule, communications)
	if relatedEventType == "" {
		a.setFlash(w, "No customer communication template is available for this booking state.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
		return
	}

	eventKey := fmt.Sprintf("schedule:%d:%s:%s:%d", scheduleID, bookingCommEventResent, relatedEventType, time.Now().UTC().UnixNano())
	results, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventResent, relatedEventType, eventKey, currentUser.ID)
	if commErr != nil {
		log.Printf("resend booking communication: %v", commErr)
		a.setFlash(w, "The communication resend could not be prepared automatically.")
		http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
		return
	}

	a.setFlash(w, communicationFlashMessage("Customer communication resent.", results))
	http.Redirect(w, r, adminBookingCommunicationRedirect(scheduleID, schedule.Status, schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) rescheduleBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	changedByUserID := int64(0)
	if currentUser != nil {
		changedByUserID = currentUser.ID
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	schedule.EntryType = "booking"
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))

	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := a.rescheduleBookingRequest(scheduleID, schedule, reviewNote, "rescheduled", false, changedByUserID)
	if err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}

	flashMessage := "Pending booking request updated with the proposed slot."
	if result != nil && result.ChangeID > 0 {
		communications, commErr := a.sendBookingCommunicationEvent(
			scheduleID,
			bookingCommEventRescheduledPending,
			"",
			fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventRescheduledPending, result.ChangeID),
			changedByUserID,
		)
		if commErr != nil {
			log.Printf("send pending reschedule communication: %v", commErr)
			flashMessage = "Pending booking request updated, but the customer communication could not be prepared automatically."
		} else if !communicationDelivered(communications, bookingCommChannelEmail) {
			flashMessage = "Pending booking request updated, but email delivery failed or is not configured."
		}
	}
	a.setFlash(w, flashMessage)
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
}

func (a *App) rescheduleAndConfirmBookingRequestHandler(w http.ResponseWriter, r *http.Request) {
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

	scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("schedule_id")), 10, 64)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	changedByUserID := int64(0)
	if currentUser != nil {
		changedByUserID = currentUser.ID
	}

	schedule := scheduleFromRequest(r)
	schedule.ID = scheduleID
	schedule.EntryType = "booking"
	reviewNote := strings.TrimSpace(r.FormValue("review_note"))

	if err := validateSpaceScheduleInput(schedule); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := a.rescheduleBookingRequest(scheduleID, schedule, reviewNote, "rescheduled_confirmed", true, changedByUserID)
	if err != nil {
		a.writeBookingRequestRescheduleError(w, r, scheduleID, &schedule, reviewNote, err.Error(), http.StatusBadRequest)
		return
	}

	eventType := bookingCommEventConfirmed
	eventKey := fmt.Sprintf("schedule:%d:%s", scheduleID, bookingCommEventConfirmed)
	if result != nil && result.ChangeID > 0 {
		eventType = bookingCommEventRescheduledConfirmed
		eventKey = fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventRescheduledConfirmed, result.ChangeID)
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		eventType,
		"",
		eventKey,
		changedByUserID,
	)
	if commErr != nil {
		log.Printf("send reschedule confirm communication: %v", commErr)
		a.setFlash(w, "Booking request was rescheduled and confirmed, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	a.setFlash(w, communicationFlashMessage("Booking request rescheduled and confirmed.", communications))
	http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
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
	data := a.newTemplateData(w, r, user)
	data.Title = "Booking Pricing"
	data.Description = "Manage booking pricing."
	data.Pricings = pricings
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

func (a *App) eventManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	events, err := a.listEvents()
	if err != nil {
		log.Printf("list events: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Events"
	data.Description = "Manage public events."
	data.Events = events
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.EventMode = mode
	}
	if data.EventMode == "view" || data.EventMode == "edit" {
		eventID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && eventID > 0 {
			selectedEvent, err := a.findEventByID(eventID)
			if err == nil {
				data.SelectedEvent = selectedEvent
			}
		}
	}
	a.render(w, "events-management", data, http.StatusOK)
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

func (a *App) updateReferralSettingsHandler(w http.ResponseWriter, r *http.Request) {
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
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("referral_commission_amount")), 64)
	if err != nil || amount < 0 {
		http.Error(w, "a valid referral commission amount is required", http.StatusBadRequest)
		return
	}
	if err := a.updateReferralCommissionAmount(amount); err != nil {
		log.Printf("update referral commission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral commission updated.")
	http.Redirect(w, r, "/admin/referrals#programme-settings", http.StatusSeeOther)
}

func (a *App) createReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partner := ReferralPartner{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Code:   strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Email:  strings.ToLower(strings.TrimSpace(r.FormValue("email"))),
		Phone:  strings.TrimSpace(r.FormValue("phone")),
		Active: true,
	}
	if err := validateReferralPartner(partner); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createReferralPartner(partner); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "that referral code is already in use", http.StatusConflict)
			return
		}
		log.Printf("create referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner created.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) updateReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partnerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("partner_id")), 10, 64)
	if err != nil || partnerID <= 0 {
		http.Error(w, "invalid referral partner", http.StatusBadRequest)
		return
	}
	partner := ReferralPartner{
		ID:    partnerID,
		Name:  strings.TrimSpace(r.FormValue("name")),
		Code:  strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Email: strings.ToLower(strings.TrimSpace(r.FormValue("email"))),
		Phone: strings.TrimSpace(r.FormValue("phone")),
	}
	if err := validateReferralPartner(partner); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateReferralPartner(partner); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "that referral code is already in use", http.StatusConflict)
			return
		}
		log.Printf("update referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner updated.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) toggleReferralPartnerHandler(w http.ResponseWriter, r *http.Request) {
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
	partnerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("partner_id")), 10, 64)
	if err != nil || partnerID <= 0 {
		http.Error(w, "invalid referral partner", http.StatusBadRequest)
		return
	}
	if err := a.toggleReferralPartner(partnerID); err != nil {
		log.Printf("toggle referral partner: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Referral partner status updated.")
	http.Redirect(w, r, "/admin/referrals#partners", http.StatusSeeOther)
}

func (a *App) payReferralCommissionHandler(w http.ResponseWriter, r *http.Request) {
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
	referralID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("referral_id")), 10, 64)
	if err != nil || referralID <= 0 {
		http.Error(w, "invalid referral commission", http.StatusBadRequest)
		return
	}
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	if paymentMethod != "cash" && paymentMethod != "bank_transfer" {
		http.Error(w, "invalid payment method", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	transactionID, err := a.payReferralCommission(referralID, paymentMethod, recordedBy)
	if err != nil {
		a.setFlash(w, "Commission could not be paid: "+err.Error())
		http.Redirect(w, r, "/admin/referrals", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
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
	if _, err := a.updateBookingRequestStatus(scheduleID, "confirmed", ""); err != nil {
		a.setFlash(w, "Booking could not be confirmed and remains pending: "+err.Error())
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		bookingCommEventConfirmed,
		"",
		fmt.Sprintf("schedule:%d:%s", scheduleID, bookingCommEventConfirmed),
		currentUserID(r),
	)
	if commErr != nil {
		log.Printf("send booking confirmation communication: %v", commErr)
		a.setFlash(w, "Booking request confirmed, but the customer communication could not be prepared automatically.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	a.setFlash(w, communicationFlashMessage("Booking request confirmed.", communications))
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
	if reviewNote == "" {
		a.setFlash(w, "Add a clear rejection reason before closing the request.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	if _, err := a.updateBookingRequestStatus(scheduleID, "rejected", reviewNote); err != nil {
		a.setFlash(w, "Booking could not be rejected: "+err.Error())
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	communications, commErr := a.sendBookingCommunicationEvent(
		scheduleID,
		bookingCommEventRejected,
		"",
		fmt.Sprintf("schedule:%d:%s", scheduleID, bookingCommEventRejected),
		currentUserID(r),
	)
	if commErr != nil {
		log.Printf("send booking rejection communication: %v", commErr)
		a.setFlash(w, "Booking request rejected, but the rejection email could not be prepared automatically.")
		http.Redirect(w, r, "/admin/booking-requests", http.StatusSeeOther)
		return
	}
	if communicationDelivered(communications, bookingCommChannelEmail) {
		a.setFlash(w, "Booking request rejected and the customer was notified by email.")
	} else {
		a.setFlash(w, "Booking request rejected, but the rejection email failed or is not configured.")
	}
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

func (a *App) createEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventFormSize)
	if err := r.ParseMultipartForm(maxEventImageSize); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	event, err := a.eventFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateEvent(event); err != nil {
		a.removeUploadedEventImage(event.ImagePath)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createEvent(event); err != nil {
		a.removeUploadedEventImage(event.ImagePath)
		log.Printf("create event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Event created.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) updateEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventFormSize)
	if err := r.ParseMultipartForm(maxEventImageSize); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	eventID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("event_id")), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	existingEvent, err := a.findEventByID(eventID)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	event, err := a.eventFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	event.ID = eventID
	uploadedReplacement := event.ImagePath
	if event.ImagePath == "" {
		event.ImagePath = existingEvent.ImagePath
	}
	deleteOldImage := false
	if r.FormValue("remove_image") == "true" && uploadedReplacement == "" {
		event.ImagePath = ""
		deleteOldImage = true
	}
	if err := validateEvent(event); err != nil {
		if uploadedReplacement != "" {
			a.removeUploadedEventImage(uploadedReplacement)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateEvent(event); err != nil {
		if uploadedReplacement != "" {
			a.removeUploadedEventImage(uploadedReplacement)
		}
		log.Printf("update event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if uploadedReplacement != "" && existingEvent.ImagePath != "" && existingEvent.ImagePath != uploadedReplacement {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}
	if deleteOldImage && existingEvent.ImagePath != "" && uploadedReplacement == "" {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}

	a.setFlash(w, "Event updated.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) deleteEventHandler(w http.ResponseWriter, r *http.Request) {
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

	eventID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("event_id")), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	existingEvent, _ := a.findEventByID(eventID)
	if err := a.deleteEvent(eventID); err != nil {
		log.Printf("delete event: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if existingEvent != nil {
		a.removeUploadedEventImage(existingEvent.ImagePath)
	}

	a.setFlash(w, "Event deleted.")
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

func (a *App) buildBookingTemplateData(w http.ResponseWriter, r *http.Request, user *User) (TemplateData, error) {
	isBookingCalendar := r.URL.Path == "/admin/bookings"

	pricings, err := a.listPricingRules()
	if err != nil {
		return TemplateData{}, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return TemplateData{}, err
	}

	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return TemplateData{}, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Booking Manager"
	data.Description = "Manage bookings and training sessions."
	data.Pricings = pricings
	data.PricingSettings = settings
	data.CourtActivities = courtActivities
	data.CourtLayouts = courtLayouts
	data.CourtClosures = courtClosures
	data.Activities = bookingActivities()
	data.Hours = bookingHours()
	data.TodayDate = time.Now().Format("2006-01-02")
	data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("date"))
	if data.CalendarDate == "" {
		data.CalendarDate = strings.TrimSpace(r.URL.Query().Get("slot_date"))
	}
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

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	selectedScheduleID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)

	if isBookingCalendar {
		weekStart, weekEnd := bookingCalendarWindow(selectedDate)
		activeSchedules, err := a.listActiveSpaceSchedulesBetween(
			weekStart.Format("2006-01-02"),
			weekEnd.Format("2006-01-02"),
		)
		if err != nil {
			return TemplateData{}, err
		}
		pendingCount, err := a.countPendingSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		filteredClosures := courtClosuresBetween(
			courtClosures,
			weekStart.Format("2006-01-02"),
			weekEnd.Format("2006-01-02"),
		)

		data.Schedules = activeSchedules
		data.PendingRequestCount = pendingCount
		data.DaySchedules = schedulesForDate(activeSchedules, data.CalendarDate)
		data.BookingRequests = customerBookingRequests(data.DaySchedules)

		relevantScheduleIDs := scheduleIDs(data.DaySchedules)
		if selectedScheduleID > 0 {
			relevantScheduleIDs = appendInt64Unique(relevantScheduleIDs, selectedScheduleID)
		}

		data.BookingFinancials, err = a.listBookingFinancialsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingReferrals, err = a.listBookingReferralsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingRequestChanges, err = a.listBookingRequestChangesForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.BookingCommunications, err = a.listBookingCommunicationsForScheduleIDs(relevantScheduleIDs)
		if err != nil {
			return TemplateData{}, err
		}
		data.WeekDays = buildBookingWeekDays(
			activeSchedules,
			selectedDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			filteredClosures,
		)
		data.BookingSlots = buildBookingSlotAvailability(
			activeSchedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			filteredClosures,
		)
		data.AdminCalendarHours = buildAdminCalendarHours(
			data.CalendarDate,
			data.Hours,
			data.DaySchedules,
			courtActivities,
			courtLayouts,
			filteredClosures,
			pricings,
			settings,
			data.BookingFinancials,
			data.BookingReferrals,
			data.BookingRequestChanges,
		)
		data.DailyStats = buildAdminCalendarStats(data.AdminCalendarHours)
	} else {
		schedules, err := a.listSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		pending, err := a.listPendingSpaceSchedules()
		if err != nil {
			return TemplateData{}, err
		}
		bookingFinancials, err := a.listBookingFinancials()
		if err != nil {
			return TemplateData{}, err
		}
		bookingReferrals, err := a.listBookingReferrals()
		if err != nil {
			return TemplateData{}, err
		}
		bookingRequestChanges, err := a.listBookingRequestChanges()
		if err != nil {
			return TemplateData{}, err
		}

		data.Schedules = schedules
		data.PendingSchedules = pending
		data.PendingRequestCount = len(pending)
		data.BookingRequests = customerBookingRequests(schedules)
		data.BookingRequestStats = buildBookingRequestStats(data.BookingRequests)
		data.BookingFinancials = bookingFinancials
		data.BookingReferrals = bookingReferrals
		data.BookingRequestChanges = bookingRequestChanges

		activeSchedules := activeSchedulesOnly(schedules)
		data.DaySchedules = schedulesForDate(activeSchedules, data.CalendarDate)
		data.DailyStats = buildDailyBookingStats(data.DaySchedules, data.Hours)
		data.WeekDays = buildBookingWeekDays(
			activeSchedules,
			selectedDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		)
		data.BookingSlots = buildBookingSlotAvailability(
			activeSchedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		)
	}

	activeSchedulesForOptions := activeSchedulesOnly(data.Schedules)

	switch mode {
	case "new":
		draft := prefillAdminBookingDraft(r, data.CalendarDate)
		options, blockedReason, err := buildAdminBookingOptions(
			activeSchedulesForOptions,
			draft,
			0,
			courtActivities,
			courtLayouts,
			courtClosures,
			pricings,
			settings,
		)
		if err != nil {
			return TemplateData{}, err
		}
		data.DraftSchedule = draft
		data.AdminBookingOptions = options
		data.AdminBookingBlockedReason = blockedReason
	case "edit":
		scheduleID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && scheduleID > 0 {
			selectedSchedule, err := a.findSpaceScheduleByID(scheduleID)
			if err == nil {
				draft := *selectedSchedule
				applyAdminBookingQueryDraft(r, &draft)
				options, blockedReason, err := buildAdminBookingOptions(
					activeSchedulesForOptions,
					&draft,
					draft.ID,
					courtActivities,
					courtLayouts,
					courtClosures,
					pricings,
					settings,
				)
				if err != nil {
					return TemplateData{}, err
				}
				data.SelectedSchedule = &draft
				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		}
	}
	return data, nil
}

func (a *App) buildPublicBookingData(
	w http.ResponseWriter,
	r *http.Request,
	viewer *User,
) (TemplateData, error) {
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

	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return TemplateData{}, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return TemplateData{}, err
	}

	data := a.newTemplateData(w, r, nil)
	data.Viewer = viewer
	data.Title = "Book a Slot"
	data.Description =
		"Check availability and request a booking."
	data.Schedules = schedules
	data.Pricings = pricings
	data.PricingSettings = settings
	data.CourtActivities = courtActivities
	data.CourtLayouts = courtLayouts
	data.Activities = bookingActivities()
	data.Hours = bookingHours()

	data.CalendarDate = strings.TrimSpace(
		r.URL.Query().Get("date"),
	)

	if data.CalendarDate == "" {
		data.CalendarDate =
			time.Now().Format("2006-01-02")
	}

	selectedDate, err := time.Parse(
		"2006-01-02",
		data.CalendarDate,
	)
	if err != nil {
		selectedDate = time.Now()
		data.CalendarDate =
			selectedDate.Format("2006-01-02")
	}

	data.TodayDate =
		time.Now().Format("2006-01-02")

	if data.CalendarDate < data.TodayDate {
		selectedDate = time.Now()
		data.CalendarDate = data.TodayDate
	}

	data.PreviousDate = selectedDate.
		AddDate(0, 0, -1).
		Format("2006-01-02")

	data.NextDate = selectedDate.
		AddDate(0, 0, 1).
		Format("2006-01-02")

	data.CalendarCanGoBack =
		data.CalendarDate > data.TodayDate

	data.BookingSlots = filterPricedBookingSlots(
		buildBookingSlotAvailability(
			schedules,
			data.CalendarDate,
			data.Hours,
			courtActivities,
			courtLayouts,
			courtClosures,
		),
		data.CalendarDate,
		pricings,
		settings,
	)

	data.WeekDays = buildPricedBookingWeekDays(
		schedules,
		selectedDate,
		data.Hours,
		pricings,
		settings,
		courtActivities,
		courtLayouts,
		courtClosures,
	)

	data.DraftSchedule =
		prefillPublicBookingDraft(
			r,
			viewer,
			data.CalendarDate,
		)

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
	if selected != nil {
		closures, closureErr := a.listActiveCourtClosures()
		if closureErr == nil {
			options, blockedReason, optionErr := buildAdminBookingOptions(
				activeSchedulesOnly(data.Schedules),
				selected,
				selected.ID,
				data.CourtActivities,
				data.CourtLayouts,
				closures,
				data.Pricings,
				data.PricingSettings,
			)
			if optionErr == nil {
				data.AdminBookingOptions = options
				data.AdminBookingBlockedReason = blockedReason
			}
		} else {
			log.Printf("load booking error closures: %v", closureErr)
		}
	}
	a.render(w, "booking-management", data, status)
}

func (a *App) writeBookingRequestRescheduleError(
	w http.ResponseWriter,
	r *http.Request,
	scheduleID int64,
	proposed *SpaceSchedule,
	reviewNote string,
	message string,
	status int,
) {
	user, _ := a.currentUser(r.Context())
	data, err := a.buildBookingTemplateData(w, r, user)
	if err != nil {
		log.Printf("build booking request data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data.Title = "Booking Requests"
	data.Description = "Review pending booking requests."
	data.Error = message

	selectedRequest, findErr := a.findSpaceScheduleByID(scheduleID)
	if findErr == nil {
		draft := *selectedRequest
		if proposed != nil {
			draft.SlotDate = proposed.SlotDate
			draft.SlotHour = proposed.SlotHour
			draft.Activity = proposed.Activity
			draft.Quantity = proposed.Quantity
		}
		draft.ReviewNote = reviewNote
		data.SelectedSchedule = selectedRequest
		data.DraftSchedule = &draft

		options, blockedReason, optionErr := a.adminBookingOptionsForSchedule(draft, selectedRequest.ID)
		if optionErr == nil {
			data.AdminBookingOptions = options
			data.AdminBookingBlockedReason = blockedReason
		}
	}

	a.render(w, "booking-requests", data, status)
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
	roles, err := a.normalizeExistingRoles(r.Form["roles"])
	verified := r.FormValue("verified") == "true"
	if err != nil {
		http.Error(w, "one or more selected roles are invalid", http.StatusBadRequest)
		return
	}

	if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
		http.Error(w, "invalid user fields", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsPrivilegedRole(roles) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can assign administrator roles", http.StatusForbidden)
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
	if !roleNamePattern.MatchString(name) || isSystemRole(name) {
		http.Error(w, "role name must be 3-32 lowercase letters, numbers, hyphens, or underscores and cannot be a system role", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsSensitivePermission(permissions) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can grant identity administration permissions", http.StatusForbidden)
		return
	}
	if err := a.createRole(name, permissions); err != nil {
		if isUniqueConstraintError(err) {
			http.Error(w, "a role with this name already exists", http.StatusConflict)
			return
		}
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
	if !roleNamePattern.MatchString(name) || isSystemRole(name) {
		http.Error(w, "role name must be 3-32 lowercase letters, numbers, hyphens, or underscores and cannot be a system role", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	role, err := a.findRoleByID(roleID)
	if err != nil {
		http.Error(w, "role not found", http.StatusNotFound)
		return
	}
	if role.System {
		http.Error(w, "system roles are protected and cannot be changed", http.StatusForbidden)
		return
	}
	current, _ := a.currentUser(r.Context())
	if containsSensitivePermission(permissions) && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "only a superadmin can grant identity administration permissions", http.StatusForbidden)
		return
	}
	if !containsRole(current.Roles, "superadmin") {
		assigned, err := a.userHasRole(current.ID, role.Name)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if assigned {
			http.Error(w, "you cannot change a role assigned to your own account", http.StatusForbidden)
			return
		}
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
		if errors.Is(err, ErrRoleAssigned) || errors.Is(err, ErrSystemRoleProtected) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
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

	roles, err := a.normalizeExistingRoles(r.Form["roles"])
	if err != nil {
		http.Error(w, "one or more selected roles are invalid", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	current, _ := a.currentUser(r.Context())
	if current.ID == targetID {
		http.Error(w, "you cannot change roles on your own account", http.StatusForbidden)
		return
	}
	target, err := a.findUserByID(targetID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	currentIsSuperadmin := containsRole(current.Roles, "superadmin")
	if (containsPrivilegedRole(target.Roles) || containsPrivilegedRole(roles)) && !currentIsSuperadmin {
		http.Error(w, "only a superadmin can manage administrator accounts", http.StatusForbidden)
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

	trainingProgramID, err := parsePositiveInt64(
		r.FormValue("training_program_id"),
	)
	if err != nil {
		http.Error(
			w,
			"select a valid training programme",
			http.StatusBadRequest,
		)
		return
	}

	trainingProgram, err := a.findTrainingProgramByID(trainingProgramID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(
				w,
				"training programme not found",
				http.StatusBadRequest,
			)
			return
		}

		log.Printf("find training programme for admission: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if !trainingProgram.Active {
		http.Error(
			w,
			"the selected training programme is inactive",
			http.StatusBadRequest,
		)
		return
	}

	admission := admissionFromRequest(r)
	admission.TrainingProgramID = trainingProgram.ID
	admission.TrainingProgramName = trainingProgram.Name
	admission.PracticeType = legacyPracticeTypeForTrainingFormat(
		trainingProgram.TrainingFormat,
	)

	collectPayment := r.FormValue("payment_collected") == "true"

	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())

	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}

	_, financeTransactionID, err := a.createAdmissionWithOptionalPayment(
		admission,
		collectPayment,
		recordedByUserID,
	)
	if err != nil {
		if errors.Is(err, ErrAdmissionFeeNotConfigured) {
			http.Error(
				w,
				err.Error(),
				http.StatusBadRequest,
			)
			return
		}

		log.Printf("create admission: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if collectPayment && financeTransactionID > 0 {
		http.Redirect(
			w,
			r,
			"/admin/finance/receipt?transaction_id="+
				strconv.FormatInt(financeTransactionID, 10),
			http.StatusSeeOther,
		)
		return
	}

	a.setFlash(w, "Admission created.")
	http.Redirect(
		w,
		r,
		"/admin/admissions",
		http.StatusSeeOther,
	)
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

	trainingProgramID, err := parsePositiveInt64(
		r.FormValue("training_program_id"),
	)
	if err != nil {
		http.Error(
			w,
			"select a valid training programme",
			http.StatusBadRequest,
		)
		return
	}

	trainingProgram, err := a.findTrainingProgramByID(trainingProgramID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(
				w,
				"training programme not found",
				http.StatusBadRequest,
			)
			return
		}

		log.Printf("find training programme for admission update: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	admission := admissionFromRequest(r)
	admission.ID = admissionID
	admission.TrainingProgramID = trainingProgram.ID
	admission.TrainingProgramName = trainingProgram.Name
	admission.PracticeType = legacyPracticeTypeForTrainingFormat(
		trainingProgram.TrainingFormat,
	)

	collectPayment := r.FormValue("payment_collected") == "true"

	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	financeTransactionID, err := a.updateAdmissionWithOptionalPayment(admission, collectPayment, recordedByUserID)
	if err != nil {
		log.Printf("update admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if collectPayment && financeTransactionID > 0 {
		http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(financeTransactionID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Admission updated.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) collectStudentPaymentHandler(w http.ResponseWriter, r *http.Request) {
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

	admissionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("admission_id")), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}
	paymentMonth := strings.TrimSpace(r.FormValue("payment_month"))
	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if paymentMonth > time.Now().Format("2006-01") {
		http.Error(w, "payments cannot be collected for a future month", http.StatusBadRequest)
		return
	}
	paymentMethod := strings.ToLower(strings.TrimSpace(r.FormValue("payment_method")))
	if !validPaymentMethod(paymentMethod) {
		http.Error(w, "invalid payment method", http.StatusBadRequest)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	recordedByUserID := int64(0)
	if currentUser != nil {
		recordedByUserID = currentUser.ID
	}
	transactionID, err := a.collectStudentMonthlyPayment(admissionID, paymentMonth, monthDate, paymentMethod, recordedByUserID)
	if err != nil {
		if errors.Is(err, ErrStudentPaymentAlreadyCollected) {
			a.setFlash(w, "That student's payment has already been collected for "+paymentMonthLabel(paymentMonth)+".")
			http.Redirect(w, r, "/admin/student-payments?month="+url.QueryEscape(paymentMonth), http.StatusSeeOther)
			return
		}
		if errors.Is(err, ErrStudentNotAdmittedForMonth) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrMonthlyFeeNotConfigured) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("collect student monthly payment: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
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
	admissionIDs := normalizePositiveIDs(r.Form["admission_ids"])
	coachIDs := normalizePositiveIDs(r.Form["coach_ids"])
	if err := validateStudentGroup(group); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createStudentGroup(group, admissionIDs, coachIDs); err != nil {
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
	admissionIDs := normalizePositiveIDs(r.Form["admission_ids"])
	coachIDs := normalizePositiveIDs(r.Form["coach_ids"])
	if err := validateStudentGroup(group); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateStudentGroup(group, admissionIDs, coachIDs); err != nil {
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
	currentUser, _ := a.currentUser(r.Context())

	if userHasRole(currentUser, "coach") &&
		!userHasRole(currentUser, "admin") &&
		!userHasRole(currentUser, "superadmin") {
		assigned, err := a.coachAssignedToGroup(currentUser.ID, groupID)
		if err != nil {
			log.Printf("check coach group assignment: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if !assigned {
			http.Error(w, "you are not assigned to this group", http.StatusForbidden)
			return
		}
	}
	attendanceDate := strings.TrimSpace(r.FormValue("attendance_date"))
	parsedAttendanceDate, err := time.Parse("2006-01-02", attendanceDate)
	if err != nil {
		http.Error(w, "invalid attendance date", http.StatusBadRequest)
		return
	}
	today := time.Now().Format("2006-01-02")
	if parsedAttendanceDate.Format("2006-01-02") > today {
		http.Error(w, "attendance date cannot be in the future", http.StatusBadRequest)
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
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if schedule.EntryType == "booking" {
		quotedPrice, err := a.bookingQuote(schedule)
		if err != nil {
			a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
			return
		}
		schedule.QuotedPrice = quotedPrice
	}
	if err := a.createSpaceSchedule(schedule); err != nil {
		log.Printf("create booking: %v", err)
		a.writeBookingError(w, r, "new", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule created.")
	http.Redirect(w, r, "/admin/bookings?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
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
	if err := validateBookableScheduleTime(schedule, time.Now()); err != nil {
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	if schedule.EntryType == "booking" {
		quotedPrice, err := a.bookingQuote(schedule)
		if err != nil {
			a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
			return
		}
		schedule.QuotedPrice = quotedPrice
	}
	if err := a.updateSpaceSchedule(schedule); err != nil {
		log.Printf("update booking: %v", err)
		a.writeBookingError(w, r, "edit", &schedule, err.Error(), http.StatusBadRequest)
		return
	}

	a.setFlash(w, "Schedule updated.")
	http.Redirect(w, r, "/admin/bookings?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
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
	schedule, _ := a.findSpaceScheduleByID(scheduleID)
	if err := a.deleteSpaceSchedule(scheduleID); err != nil {
		log.Printf("delete booking: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Schedule deleted.")
	redirectTo := "/admin/bookings"
	if schedule != nil {
		redirectTo += "?date=" + url.QueryEscape(schedule.SlotDate)
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
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
		if containsRole(user.Roles, "superadmin") {
			next.ServeHTTP(w, r)
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

func (a *App) requireAnyPermission(next http.Handler, required ...string) http.Handler {
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
		for _, permission := range required {
			if containsPermission(permissions, permission) {
				next.ServeHTTP(w, r)
				return
			}
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Forbidden"
		data.Description = "You do not have permission to view this page."
		data.Error = "You do not have permission to view this page."
		a.render(w, "forbidden", data, http.StatusForbidden)
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

func (a *App) listCoachUsers() ([]User, error) {
	rows, err := a.db.Query(`
		SELECT DISTINCT
			u.id,
			u.email,
			u.name,
			u.email_verified_at,
			u.created_at
		FROM users u
		JOIN user_roles ur
			ON ur.user_id = u.id
		JOIN roles r
			ON r.id = ur.role_id
		WHERE r.name = 'coach'
		ORDER BY u.name COLLATE NOCASE ASC, u.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var coaches []User

	for rows.Next() {
		var coach User
		var verifiedAt sql.NullTime

		if err := rows.Scan(
			&coach.ID,
			&coach.Email,
			&coach.Name,
			&verifiedAt,
			&coach.CreatedAt,
		); err != nil {
			return nil, err
		}

		coach.Verified = verifiedAt.Valid
		coach.Roles = []string{"coach"}
		coaches = append(coaches, coach)
	}

	return coaches, rows.Err()
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
	rows, err := a.db.Query(`
		SELECT r.id, r.name, COUNT(ur.user_id)
		FROM roles r
		LEFT JOIN user_roles ur ON ur.role_id = r.id
		GROUP BY r.id, r.name
		ORDER BY r.name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name, &role.UserCount); err != nil {
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

func (a *App) findRoleByID(roleID int64) (*Role, error) {
	var role Role
	if err := a.db.QueryRow(`
		SELECT r.id, r.name, COUNT(ur.user_id)
		FROM roles r
		LEFT JOIN user_roles ur ON ur.role_id = r.id
		WHERE r.id = ?
		GROUP BY r.id, r.name
	`, roleID).Scan(&role.ID, &role.Name, &role.UserCount); err != nil {
		return nil, err
	}
	role.System = isSystemRole(role.Name)
	permissions, err := a.permissionsForRole(role.ID)
	if err != nil {
		return nil, err
	}
	role.Permissions = permissions
	return &role, nil
}

func (a *App) userHasRole(userID int64, roleName string) (bool, error) {
	var count int
	err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.name = ?
	`, userID, roleName).Scan(&count)
	return count > 0, err
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
		return ErrSystemRoleProtected
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
		return ErrSystemRoleProtected
	}
	var userCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM user_roles WHERE role_id = ?`, roleID).Scan(&userCount); err != nil {
		return err
	}
	if userCount > 0 {
		return ErrRoleAssigned
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
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
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number,
			a.guardian_alternative_contact_number,
			a.medical_information,
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		ORDER BY
			a.admission_date DESC,
			a.created_at DESC,
			a.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admissions []Admission

	for rows.Next() {
		var admission Admission
		var paymentCollected int
		var paymentCollectedAt sql.NullTime

		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.PracticeType,
			&admission.TrainingProgramID,
			&admission.TrainingProgramName,
			&admission.Address,
			&admission.PassportNumber,
			&admission.School,
			&admission.GuardianName,
			&admission.GuardianRelationship,
			&admission.GuardianContactNumber,
			&admission.GuardianAlternativePhone,
			&admission.MedicalInformation,
			&paymentCollected,
			&paymentCollectedAt,
			&admission.AdmissionPaymentAmount,
			&admission.FinanceTransactionID,
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}

		admission.PaymentCollected = paymentCollected == 1

		if paymentCollectedAt.Valid {
			admission.PaymentCollectedAt = paymentCollectedAt.Time
		}

		admissions = append(admissions, admission)
	}

	return admissions, rows.Err()
}

func (a *App) listEvents() ([]Event, error) {
	rows, err := a.db.Query(`
		SELECT id, title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		ORDER BY event_date ASC, start_time ASC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var published int
		if err := rows.Scan(
			&event.ID,
			&event.Title,
			&event.Category,
			&event.EventDate,
			&event.StartTime,
			&event.EndTime,
			&event.RegistrationDeadline,
			&event.Venue,
			&event.Summary,
			&event.ImagePath,
			&event.CTALabel,
			&event.CTALink,
			&published,
			&event.CreatedAt,
			&event.UpdatedAt,
		); err != nil {
			return nil, err
		}
		event.Published = published == 1
		events = append(events, event)
	}
	return events, rows.Err()
}

func (a *App) listPublishedEvents() ([]Event, error) {
	rows, err := a.db.Query(`
		SELECT id, title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		WHERE published = 1
		ORDER BY event_date ASC, start_time ASC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var published int
		if err := rows.Scan(
			&event.ID,
			&event.Title,
			&event.Category,
			&event.EventDate,
			&event.StartTime,
			&event.EndTime,
			&event.RegistrationDeadline,
			&event.Venue,
			&event.Summary,
			&event.ImagePath,
			&event.CTALabel,
			&event.CTALink,
			&published,
			&event.CreatedAt,
			&event.UpdatedAt,
		); err != nil {
			return nil, err
		}
		event.Published = published == 1
		events = append(events, event)
	}
	return events, rows.Err()
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

		coaches, err := a.listCoachesForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		groups[i].Students = students
		groups[i].StudentCount = len(students)
		groups[i].Coaches = coaches
		groups[i].CoachCount = len(coaches)
	}

	return groups, nil
}

func (a *App) listStudentGroupsForCoach(userID int64) ([]StudentGroup, error) {
	rows, err := a.db.Query(`
		SELECT
			sg.id,
			sg.name,
			sg.code,
			sg.description,
			sg.created_at
		FROM student_groups sg
		JOIN student_group_coaches sgc
			ON sgc.group_id = sg.id
		WHERE sgc.user_id = ?
		ORDER BY sg.created_at DESC, sg.id DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []StudentGroup

	for rows.Next() {
		var group StudentGroup

		if err := rows.Scan(
			&group.ID,
			&group.Name,
			&group.Code,
			&group.Description,
			&group.CreatedAt,
		); err != nil {
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

		coaches, err := a.listCoachesForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		groups[i].Students = students
		groups[i].StudentCount = len(students)
		groups[i].Coaches = coaches
		groups[i].CoachCount = len(coaches)
	}

	return groups, nil
}

func (a *App) coachAssignedToGroup(
	userID int64,
	groupID int64,
) (bool, error) {
	var assigned int

	err := a.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM student_group_coaches
			WHERE user_id = ?
				AND group_id = ?
		)
	`, userID, groupID).Scan(&assigned)
	if err != nil {
		return false, err
	}

	return assigned == 1, nil
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

func (a *App) getAttendanceSummary(
	groupID int64,
) (AttendanceSummary, error) {
	var summary AttendanceSummary

	err := a.db.QueryRow(`
		SELECT
			COUNT(DISTINCT attendance_date),
			COALESCE(SUM(CASE WHEN status = 'present' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'absent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'late' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'excused' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM attendance_records
		WHERE group_id = ?
	`, groupID).Scan(
		&summary.SessionCount,
		&summary.PresentCount,
		&summary.AbsentCount,
		&summary.LateCount,
		&summary.ExcusedCount,
		&summary.TotalEntries,
	)
	if err != nil {
		return AttendanceSummary{}, err
	}

	return summary, nil
}

func (a *App) listCourts(includeInactive bool) ([]Court, error) {
	query := `
		SELECT
			id,
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM courts
	`

	if !includeInactive {
		query += ` WHERE active = 1`
	}

	query += ` ORDER BY sort_order, name, id`

	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}

	var courts []Court

	for rows.Next() {
		var court Court

		if err := rows.Scan(
			&court.ID,
			&court.Name,
			&court.Code,
			&court.Description,
			&court.Active,
			&court.SortOrder,
			&court.CreatedAt,
			&court.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		courts = append(courts, court)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range courts {
		layouts, err := a.listCourtLayouts(
			courts[i].ID,
			includeInactive,
		)
		if err != nil {
			return nil, err
		}

		courts[i].Layouts = layouts
	}

	return courts, nil
}

func (a *App) findCourtByID(courtID int64) (*Court, error) {
	var court Court

	err := a.db.QueryRow(`
		SELECT
			id,
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM courts
		WHERE id = ?
	`, courtID).Scan(
		&court.ID,
		&court.Name,
		&court.Code,
		&court.Description,
		&court.Active,
		&court.SortOrder,
		&court.CreatedAt,
		&court.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	activities, err := a.listCourtActivities(court.ID, true)
	if err != nil {
		return nil, err
	}

	layouts, err := a.listCourtLayouts(court.ID, true)
	if err != nil {
		return nil, err
	}

	court.Activities = activities
	court.Layouts = layouts

	return &court, nil
}

func (a *App) listCourtActivities(
	courtID int64,
	includeInactive bool,
) ([]CourtActivity, error) {
	query := `
		SELECT
			id,
			court_id,
			activity,
			display_name,
			max_quantity,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_activities
		WHERE court_id = ?
	`

	if !includeInactive {
		query += ` AND active = 1`
	}

	query += ` ORDER BY sort_order, display_name, id`

	rows, err := a.db.Query(query, courtID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []CourtActivity

	for rows.Next() {
		var activity CourtActivity

		err := rows.Scan(
			&activity.ID,
			&activity.CourtID,
			&activity.Activity,
			&activity.DisplayName,
			&activity.MaxQuantity,
			&activity.Active,
			&activity.SortOrder,
			&activity.CreatedAt,
			&activity.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		activities = append(activities, activity)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return activities, nil
}

func (a *App) listCourtLayouts(
	courtID int64,
	includeInactive bool,
) ([]CourtLayout, error) {
	query := `
		SELECT
			id,
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_layouts
		WHERE court_id = ?
	`

	if !includeInactive {
		query += ` AND active = 1`
	}

	query += ` ORDER BY sort_order, name, id`

	rows, err := a.db.Query(query, courtID)
	if err != nil {
		return nil, err
	}

	var layouts []CourtLayout

	for rows.Next() {
		var layout CourtLayout

		if err := rows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		layouts = append(layouts, layout)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range layouts {
		items, err := a.listCourtLayoutItems(layouts[i].ID)
		if err != nil {
			return nil, err
		}

		layouts[i].Items = items
	}

	return layouts, nil
}

func (a *App) findCourtLayoutByID(layoutID int64) (*CourtLayout, error) {
	var layout CourtLayout

	err := a.db.QueryRow(`
		SELECT
			id,
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(
		&layout.ID,
		&layout.CourtID,
		&layout.Name,
		&layout.Description,
		&layout.Active,
		&layout.SortOrder,
		&layout.CreatedAt,
		&layout.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	items, err := a.listCourtLayoutItems(layout.ID)
	if err != nil {
		return nil, err
	}

	layout.Items = items

	return &layout, nil
}

func (a *App) listCourtLayoutItems(
	layoutID int64,
) ([]CourtLayoutItem, error) {
	rows, err := a.db.Query(`
		SELECT
			cli.id,
			cli.layout_id,
			cli.activity,
			COALESCE(ca.display_name, cli.activity),
			cli.quantity
		FROM court_layout_items cli
		LEFT JOIN court_layouts cl
			ON cl.id = cli.layout_id
		LEFT JOIN court_activities ca
			ON ca.court_id = cl.court_id
			AND ca.activity = cli.activity
		WHERE cli.layout_id = ?
		ORDER BY
			COALESCE(ca.sort_order, 9999),
			COALESCE(ca.display_name, cli.activity),
			cli.id
	`, layoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CourtLayoutItem

	for rows.Next() {
		var item CourtLayoutItem

		err := rows.Scan(
			&item.ID,
			&item.LayoutID,
			&item.Activity,
			&item.DisplayName,
			&item.Quantity,
		)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (a *App) listCourtClosures(
	courtID int64,
	includeInactive bool,
) ([]CourtClosure, error) {
	query := `
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.court_id = ?
	`

	if !includeInactive {
		query += ` AND cc.active = 1`
	}

	query += `
		ORDER BY
			cc.closure_date DESC,
			cc.start_hour,
			cc.id DESC
	`

	rows, err := a.db.Query(
		query,
		courtID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure

	for rows.Next() {
		var closure CourtClosure

		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}

		closures = append(
			closures,
			closure,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return closures, nil
}

func (a *App) findCourtClosureByID(
	closureID int64,
) (*CourtClosure, error) {
	var closure CourtClosure

	err := a.db.QueryRow(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.id = ?
	`, closureID).Scan(
		&closure.ID,
		&closure.CourtID,
		&closure.CourtName,
		&closure.ClosureDate,
		&closure.StartHour,
		&closure.EndHour,
		&closure.Activity,
		&closure.Title,
		&closure.Reason,
		&closure.Active,
		&closure.CreatedAt,
		&closure.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &closure, nil
}

func (a *App) listActiveCourtClosures() (
	[]CourtClosure,
	error,
) {
	return listActiveCourtClosuresQuery(a.db)
}

type sqlQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func listActiveCourtClosuresQuery(queryer sqlQueryer) (
	[]CourtClosure,
	error,
) {
	rows, err := queryer.Query(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.active = 1
		  AND c.active = 1
		ORDER BY
			cc.closure_date,
			cc.start_hour,
			cc.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure
	for rows.Next() {
		var closure CourtClosure
		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}
		closures = append(closures, closure)
	}

	return closures, rows.Err()
}

func activeBookingConfigurationQuery(
	queryer sqlQueryer,
) ([]CourtActivity, []CourtLayout, error) {
	activitiesRows, err := queryer.Query(`
		SELECT
			ca.id,
			ca.court_id,
			ca.activity,
			ca.display_name,
			ca.max_quantity,
			ca.active,
			ca.sort_order,
			ca.created_at,
			ca.updated_at
		FROM court_activities ca
		JOIN courts c
			ON c.id = ca.court_id
		WHERE ca.active = 1
		  AND c.active = 1
		ORDER BY
			ca.sort_order,
			ca.display_name,
			ca.id
	`)
	if err != nil {
		return nil, nil, err
	}
	defer activitiesRows.Close()

	var activities []CourtActivity
	for activitiesRows.Next() {
		var activity CourtActivity
		if err := activitiesRows.Scan(
			&activity.ID,
			&activity.CourtID,
			&activity.Activity,
			&activity.DisplayName,
			&activity.MaxQuantity,
			&activity.Active,
			&activity.SortOrder,
			&activity.CreatedAt,
			&activity.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		activities = append(activities, activity)
	}
	if err := activitiesRows.Err(); err != nil {
		return nil, nil, err
	}

	layoutRows, err := queryer.Query(`
		SELECT
			cl.id,
			cl.court_id,
			cl.name,
			cl.description,
			cl.active,
			cl.sort_order,
			cl.created_at,
			cl.updated_at,
			COALESCE(cli.id, 0),
			COALESCE(cli.activity, ''),
			COALESCE(ca.display_name, cli.activity, ''),
			COALESCE(cli.quantity, 0)
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		LEFT JOIN court_layout_items cli
			ON cli.layout_id = cl.id
		LEFT JOIN court_activities ca
			ON ca.court_id = cl.court_id
			AND ca.activity = cli.activity
		WHERE cl.active = 1
		  AND c.active = 1
		ORDER BY
			cl.sort_order,
			cl.name,
			cl.id,
			COALESCE(ca.sort_order, 9999),
			COALESCE(ca.display_name, cli.activity),
			cli.id
	`)
	if err != nil {
		return nil, nil, err
	}
	defer layoutRows.Close()

	layoutMap := make(map[int64]*CourtLayout)
	layoutOrder := make([]int64, 0)

	for layoutRows.Next() {
		var (
			layout          CourtLayout
			itemID          int64
			itemActivity    string
			itemDisplayName string
			itemQuantity    int
		)

		if err := layoutRows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
			&itemID,
			&itemActivity,
			&itemDisplayName,
			&itemQuantity,
		); err != nil {
			return nil, nil, err
		}

		existing := layoutMap[layout.ID]
		if existing == nil {
			layoutCopy := layout
			layoutMap[layout.ID] = &layoutCopy
			layoutOrder = append(layoutOrder, layout.ID)
			existing = &layoutCopy
		}

		if itemID > 0 {
			existing.Items = append(existing.Items, CourtLayoutItem{
				ID:          itemID,
				LayoutID:    layout.ID,
				Activity:    itemActivity,
				DisplayName: itemDisplayName,
				Quantity:    itemQuantity,
			})
		}
	}
	if err := layoutRows.Err(); err != nil {
		return nil, nil, err
	}

	layouts := make([]CourtLayout, 0, len(layoutOrder))
	for _, layoutID := range layoutOrder {
		layouts = append(layouts, *layoutMap[layoutID])
	}

	if len(layouts) == 0 {
		return nil, nil, errors.New("no active court layouts are configured")
	}

	return activities, layouts, nil
}

func (a *App) activeCourtClosuresForDate(
	closureDate string,
) ([]CourtClosure, error) {
	rows, err := a.db.Query(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.active = 1
		  AND c.active = 1
		  AND cc.closure_date = ?
		ORDER BY
			c.sort_order,
			cc.start_hour,
			cc.id
	`, closureDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure

	for rows.Next() {
		var closure CourtClosure

		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}

		closures = append(
			closures,
			closure,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return closures, nil
}

func (a *App) createCourtClosure(
	closure CourtClosure,
) (int64, error) {
	activities, err := a.listCourtActivities(
		closure.CourtID,
		false,
	)
	if err != nil {
		return 0, err
	}

	if err := validateCourtClosure(
		closure,
		activities,
	); err != nil {
		return 0, err
	}

	now := time.Now().UTC()

	result, err := a.db.Exec(`
		INSERT INTO court_closures (
			court_id,
			closure_date,
			start_hour,
			end_hour,
			activity,
			title,
			reason,
			active,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		closure.CourtID,
		closure.ClosureDate,
		closure.StartHour,
		closure.EndHour,
		closure.Activity,
		closure.Title,
		closure.Reason,
		closure.Active,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

func (a *App) updateCourtClosure(
	closure CourtClosure,
) error {
	if closure.ID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	activities, err := a.listCourtActivities(
		closure.CourtID,
		false,
	)
	if err != nil {
		return err
	}

	if err := validateCourtClosure(
		closure,
		activities,
	); err != nil {
		return err
	}

	result, err := a.db.Exec(`
		UPDATE court_closures
		SET
			court_id = ?,
			closure_date = ?,
			start_hour = ?,
			end_hour = ?,
			activity = ?,
			title = ?,
			reason = ?,
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		closure.CourtID,
		closure.ClosureDate,
		closure.StartHour,
		closure.EndHour,
		closure.Activity,
		closure.Title,
		closure.Reason,
		closure.Active,
		time.Now().UTC(),
		closure.ID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) toggleCourtClosure(
	closureID int64,
) error {
	if closureID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	var active bool

	if err := a.db.QueryRow(`
		SELECT active
		FROM court_closures
		WHERE id = ?
	`, closureID).Scan(&active); err != nil {
		return err
	}

	_, err := a.db.Exec(`
		UPDATE court_closures
		SET
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		!active,
		time.Now().UTC(),
		closureID,
	)

	return err
}

func (a *App) deleteCourtClosure(
	closureID int64,
) error {
	if closureID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	result, err := a.db.Exec(`
		DELETE FROM court_closures
		WHERE id = ?
	`, closureID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) listActiveCourtLayouts() ([]CourtLayout, error) {
	rows, err := a.db.Query(`
		SELECT
			cl.id,
			cl.court_id,
			cl.name,
			cl.description,
			cl.active,
			cl.sort_order,
			cl.created_at,
			cl.updated_at
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		WHERE cl.active = 1
		  AND c.active = 1
		ORDER BY
			c.sort_order,
			cl.sort_order,
			cl.name,
			cl.id
	`)
	if err != nil {
		return nil, err
	}

	var layouts []CourtLayout

	for rows.Next() {
		var layout CourtLayout

		if err := rows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		layouts = append(layouts, layout)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range layouts {
		items, err := a.listCourtLayoutItems(layouts[i].ID)
		if err != nil {
			return nil, err
		}

		layouts[i].Items = items
	}

	return layouts, nil
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

func (a *App) listActiveSpaceSchedulesBetween(
	startDate string,
	endDate string,
) ([]SpaceSchedule, error) {
	rows, err := a.db.Query(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE status IN ('pending', 'confirmed')
		  AND slot_date >= ?
		  AND slot_date <= ?
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`, startDate, endDate)
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
	return listPricingRulesQuery(a.db)
}

func listPricingRulesQuery(queryer sqlQueryer) ([]PricingRule, error) {
	rows, err := queryer.Query(`
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

func (a *App) listTrainingPrograms(includeInactive bool) ([]TrainingProgram, error) {
	query := `
		SELECT
			id,
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at
		FROM training_programs
	`

	if !includeInactive {
		query += ` WHERE active = 1`
	}

	query += ` ORDER BY sort_order ASC, name ASC, id ASC`

	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var programs []TrainingProgram

	for rows.Next() {
		var program TrainingProgram
		var active int

		if err := rows.Scan(
			&program.ID,
			&program.Name,
			&program.Activity,
			&program.TrainingFormat,
			&program.AdmissionFee,
			&program.MonthlyFee,
			&active,
			&program.SortOrder,
			&program.CreatedAt,
			&program.UpdatedAt,
		); err != nil {
			return nil, err
		}

		program.Active = active == 1
		programs = append(programs, program)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return programs, nil
}

func (a *App) findTrainingProgramByID(programID int64) (*TrainingProgram, error) {
	row := a.db.QueryRow(`
		SELECT
			id,
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at
		FROM training_programs
		WHERE id = ?
	`, programID)

	var program TrainingProgram
	var active int

	if err := row.Scan(
		&program.ID,
		&program.Name,
		&program.Activity,
		&program.TrainingFormat,
		&program.AdmissionFee,
		&program.MonthlyFee,
		&active,
		&program.SortOrder,
		&program.CreatedAt,
		&program.UpdatedAt,
	); err != nil {
		return nil, err
	}

	program.Active = active == 1

	return &program, nil
}

func (a *App) createTrainingProgram(program TrainingProgram) (int64, error) {
	now := time.Now().UTC()

	result, err := a.db.Exec(`
		INSERT INTO training_programs (
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		program.Name,
		program.Activity,
		program.TrainingFormat,
		program.AdmissionFee,
		program.MonthlyFee,
		boolToInt(program.Active),
		program.SortOrder,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	programID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return programID, nil
}

func (a *App) updateTrainingProgram(program TrainingProgram) error {
	_, err := a.db.Exec(`
		UPDATE training_programs
		SET
			name = ?,
			activity = ?,
			training_format = ?,
			admission_fee = ?,
			monthly_fee = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
	`,
		program.Name,
		program.Activity,
		program.TrainingFormat,
		program.AdmissionFee,
		program.MonthlyFee,
		boolToInt(program.Active),
		program.SortOrder,
		time.Now().UTC(),
		program.ID,
	)

	return err
}

func (a *App) setTrainingProgramActive(programID int64, active bool) error {
	result, err := a.db.Exec(`
		UPDATE training_programs
		SET active = ?, updated_at = ?
		WHERE id = ?
	`,
		boolToInt(active),
		time.Now().UTC(),
		programID,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) deleteTrainingProgram(programID int64) error {
	var admissionCount int

	err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM admissions
		WHERE training_program_id = ?
	`, programID).Scan(&admissionCount)
	if err != nil {
		return err
	}

	if admissionCount > 0 {
		return errors.New(
			"this training programme is assigned to students and cannot be deleted",
		)
	}

	result, err := a.db.Exec(`
		DELETE FROM training_programs
		WHERE id = ?
	`, programID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) listFinanceTransactions() ([]FinanceTransaction, error) {
	return a.listFinanceTransactionsFiltered(FinanceFilter{})
}

func (a *App) listFinanceTransactionsFiltered(filter FinanceFilter) ([]FinanceTransaction, error) {
	query := `
		SELECT id, receipt_number, category, reference_type, COALESCE(reference_id, 0), person_name, description,
		       payment_method, amount, COALESCE(recorded_by_user_id, 0), recorded_at, created_at
		FROM finance_transactions
		WHERE 1 = 1`
	args := make([]any, 0, 6)
	if filter.From != "" {
		query += ` AND SUBSTR(TRIM(CAST(recorded_at AS TEXT)), 1, 10) >= ?`
		args = append(args, filter.From)
	}
	if filter.To != "" {
		query += ` AND SUBSTR(TRIM(CAST(recorded_at AS TEXT)), 1, 10) <= ?`
		args = append(args, filter.To)
	}
	switch filter.Direction {
	case "income":
		query += ` AND amount > 0`
	case "expense":
		query += ` AND amount < 0`
	}
	if filter.Category != "" {
		query += ` AND category = ?`
		args = append(args, filter.Category)
	}
	if filter.Search != "" {
		query += ` AND (LOWER(receipt_number) LIKE ? OR LOWER(person_name) LIKE ? OR LOWER(description) LIKE ?)`
		term := "%" + strings.ToLower(filter.Search) + "%"
		args = append(args, term, term, term)
	}
	query += ` ORDER BY recorded_at DESC, id DESC`

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transactions []FinanceTransaction
	for rows.Next() {
		var transaction FinanceTransaction
		if err := rows.Scan(
			&transaction.ID,
			&transaction.ReceiptNumber,
			&transaction.Category,
			&transaction.ReferenceType,
			&transaction.ReferenceID,
			&transaction.PersonName,
			&transaction.Description,
			&transaction.PaymentMethod,
			&transaction.Amount,
			&transaction.RecordedByUser,
			&transaction.RecordedAt,
			&transaction.CreatedAt,
		); err != nil {
			return nil, err
		}
		transactions = append(transactions, transaction)
	}
	return transactions, rows.Err()
}

func (a *App) listOutstandingBookingFinancials() ([]BookingFinancial, error) {
	rows, err := a.db.Query(`
		SELECT bf.id, bf.schedule_id, bf.quoted_amount, bf.paid, bf.paid_at,
		       bf.payment_method, COALESCE(bf.finance_transaction_id, 0),
		       s.slot_date, s.slot_hour, s.activity, s.quantity, s.status,
		       COALESCE(s.requester_name, ''), COALESCE(s.requester_email, '')
		FROM booking_financials bf
		JOIN space_schedules s ON s.id = bf.schedule_id
		WHERE bf.paid = 0 AND bf.quoted_amount > 0 AND s.status = 'confirmed'
		ORDER BY s.slot_date ASC, s.slot_hour ASC, bf.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var financials []BookingFinancial
	for rows.Next() {
		var financial BookingFinancial
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&financial.ID, &financial.ScheduleID, &financial.QuotedAmount, &paid, &paidAt,
			&financial.PaymentMethod, &financial.FinanceTransactionID, &financial.SlotDate,
			&financial.SlotHour, &financial.Activity, &financial.Quantity, &financial.Status,
			&financial.RequesterName, &financial.RequesterEmail,
		); err != nil {
			return nil, err
		}
		financial.Paid = paid == 1
		if paidAt.Valid {
			financial.PaidAt = paidAt.Time
		}
		financials = append(financials, financial)
	}
	return financials, rows.Err()
}

func (a *App) listStudentPaymentRows(paymentMonth string) ([]StudentPaymentRow, error) {
	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		return nil, err
	}
	monthEnd := monthDate.AddDate(0, 1, -1).Format("2006-01-02")
	rows, err := a.db.Query(`
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number, a.guardian_alternative_contact_number, a.medical_information,
			COALESCE(a.payment_collected, 0), a.payment_collected_at, COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0), a.created_at,
			COALESCE(tp.monthly_fee, ap.monthly_fee, 0),
			smp.id, smp.amount, smp.payment_method, smp.finance_transaction_id,
			COALESCE(smp.collected_by_user_id, 0), smp.collected_at, smp.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		LEFT JOIN admission_pricing ap
			ON a.training_program_id IS NULL
			AND ap.practice_type = a.practice_type
		LEFT JOIN student_monthly_payments smp
			ON smp.admission_id = a.id AND smp.payment_month = ?
		WHERE a.admission_date <= ?
		ORDER BY
			CASE WHEN smp.id IS NULL THEN 0 ELSE 1 END,
			a.full_name COLLATE NOCASE,
			a.id
	`, paymentMonth, monthEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paymentRows []StudentPaymentRow
	for rows.Next() {
		var (
			row               StudentPaymentRow
			admissionPaid     int
			admissionPaidAt   sql.NullTime
			paymentID         sql.NullInt64
			paymentAmount     sql.NullFloat64
			paymentMethod     sql.NullString
			transactionID     sql.NullInt64
			collectedByUserID sql.NullInt64
			collectedAt       sql.NullTime
			paymentCreatedAt  sql.NullTime
		)
		if err := rows.Scan(
			&row.Admission.ID, &row.Admission.StudentID, &row.Admission.FullName, &row.Admission.AdmissionDate,
			&row.Admission.DateOfBirth,
			&row.Admission.Gender,
			&row.Admission.PracticeType,
			&row.Admission.TrainingProgramID,
			&row.Admission.TrainingProgramName,
			&row.Admission.Address,
			&row.Admission.PassportNumber, &row.Admission.School, &row.Admission.GuardianName,
			&row.Admission.GuardianRelationship, &row.Admission.GuardianContactNumber,
			&row.Admission.GuardianAlternativePhone, &row.Admission.MedicalInformation, &admissionPaid,
			&admissionPaidAt, &row.Admission.AdmissionPaymentAmount, &row.Admission.FinanceTransactionID,
			&row.Admission.CreatedAt, &row.MonthlyFee, &paymentID, &paymentAmount, &paymentMethod,
			&transactionID, &collectedByUserID, &collectedAt, &paymentCreatedAt,
		); err != nil {
			return nil, err
		}
		row.Admission.PaymentCollected = admissionPaid == 1
		if admissionPaidAt.Valid {
			row.Admission.PaymentCollectedAt = admissionPaidAt.Time
		}
		if paymentID.Valid {
			row.Payment = &StudentMonthlyPayment{
				ID:                   paymentID.Int64,
				AdmissionID:          row.Admission.ID,
				PaymentMonth:         paymentMonth,
				Amount:               paymentAmount.Float64,
				PaymentMethod:        paymentMethod.String,
				FinanceTransactionID: transactionID.Int64,
				CollectedByUserID:    collectedByUserID.Int64,
				CollectedAt:          collectedAt.Time,
				CreatedAt:            paymentCreatedAt.Time,
			}
		}
		paymentRows = append(paymentRows, row)
	}
	return paymentRows, rows.Err()
}

func (a *App) getPricingSettings() (*PricingSettings, error) {
	return getPricingSettingsQuery(a.db)
}

func getPricingSettingsQuery(queryer sqlQueryer) (*PricingSettings, error) {
	row := queryer.QueryRow(`
		SELECT id, peak_start_hour, peak_end_hour, COALESCE(referral_commission_amount, 0), created_at, updated_at
		FROM pricing_settings
		ORDER BY id ASC
		LIMIT 1
	`)

	var settings PricingSettings
	if err := row.Scan(
		&settings.ID,
		&settings.PeakStartHour,
		&settings.PeakEndHour,
		&settings.ReferralCommissionAmount,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (a *App) listReferralPartners(activeOnly bool) ([]ReferralPartner, error) {
	query := `
		SELECT id, name, code, email, phone, active, created_at, updated_at
		FROM referral_partners
	`
	if activeOnly {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY active DESC, name COLLATE NOCASE, id`
	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var partners []ReferralPartner
	for rows.Next() {
		var partner ReferralPartner
		var active int
		if err := rows.Scan(&partner.ID, &partner.Name, &partner.Code, &partner.Email, &partner.Phone, &active, &partner.CreatedAt, &partner.UpdatedAt); err != nil {
			return nil, err
		}
		partner.Active = active == 1
		partners = append(partners, partner)
	}
	return partners, rows.Err()
}

func (a *App) listBookingReferrals() ([]BookingReferral, error) {
	rows, err := a.db.Query(`
		SELECT br.id, br.schedule_id, br.partner_id, rp.name, rp.code, br.commission_amount,
		       s.status, s.title, s.slot_date, br.paid, br.paid_at, br.payment_method,
		       COALESCE(br.finance_transaction_id, 0), br.created_at
		FROM booking_referrals br
		JOIN referral_partners rp ON rp.id = br.partner_id
		JOIN space_schedules s ON s.id = br.schedule_id
		ORDER BY
			CASE WHEN s.status = 'confirmed' AND br.paid = 0 THEN 0 WHEN s.status = 'pending' THEN 1 ELSE 2 END,
			br.created_at DESC, br.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var referrals []BookingReferral
	for rows.Next() {
		var referral BookingReferral
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&referral.ID, &referral.ScheduleID, &referral.PartnerID, &referral.PartnerName,
			&referral.PartnerCode, &referral.CommissionAmount, &referral.BookingStatus,
			&referral.BookingTitle, &referral.SlotDate, &paid, &paidAt, &referral.PaymentMethod,
			&referral.FinanceTransactionID, &referral.CreatedAt,
		); err != nil {
			return nil, err
		}
		referral.BookingReference = bookingReference(referral.ScheduleID)
		referral.Paid = paid == 1
		if paidAt.Valid {
			referral.PaidAt = paidAt.Time
		}
		referrals = append(referrals, referral)
	}
	return referrals, rows.Err()
}

func (a *App) listBookingReferralsForScheduleIDs(scheduleIDs []int64) ([]BookingReferral, error) {
	return listBookingReferralsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingFinancials() ([]BookingFinancial, error) {
	rows, err := a.db.Query(`
		SELECT
			bf.id,
			bf.schedule_id,
			bf.quoted_amount,
			bf.paid,
			bf.paid_at,
			bf.payment_method,
			COALESCE(bf.finance_transaction_id, 0),
			s.slot_date,
			s.slot_hour,
			s.activity,
			s.quantity,
			s.status,
			COALESCE(s.requester_name, ''),
			COALESCE(s.requester_email, '')
		FROM booking_financials bf
		JOIN space_schedules s
			ON s.id = bf.schedule_id
		ORDER BY s.slot_date ASC, s.slot_hour ASC, bf.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var financials []BookingFinancial
	for rows.Next() {
		var financial BookingFinancial
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&financial.ID,
			&financial.ScheduleID,
			&financial.QuotedAmount,
			&paid,
			&paidAt,
			&financial.PaymentMethod,
			&financial.FinanceTransactionID,
			&financial.SlotDate,
			&financial.SlotHour,
			&financial.Activity,
			&financial.Quantity,
			&financial.Status,
			&financial.RequesterName,
			&financial.RequesterEmail,
		); err != nil {
			return nil, err
		}
		financial.Paid = paid == 1
		if paidAt.Valid {
			financial.PaidAt = paidAt.Time
		}
		financials = append(financials, financial)
	}

	return financials, rows.Err()
}

func (a *App) listBookingFinancialsForScheduleIDs(scheduleIDs []int64) ([]BookingFinancial, error) {
	return listBookingFinancialsForScheduleIDsQuery(a.db, scheduleIDs)
}

func (a *App) listBookingRequestChanges() ([]BookingRequestChange, error) {
	rows, err := a.db.Query(`
		SELECT
			brch.id,
			brch.schedule_id,
			brch.previous_slot_date,
			brch.previous_slot_hour,
			brch.previous_activity,
			brch.previous_quantity,
			brch.previous_quoted_price,
			brch.new_slot_date,
			brch.new_slot_hour,
			brch.new_activity,
			brch.new_quantity,
			brch.new_quoted_price,
			brch.action_type,
			brch.review_note,
			COALESCE(brch.changed_by_user_id, 0),
			COALESCE(u.name, ''),
			brch.changed_at
		FROM booking_request_changes brch
		LEFT JOIN users u
			ON u.id = brch.changed_by_user_id
		ORDER BY brch.changed_at DESC, brch.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []BookingRequestChange
	for rows.Next() {
		var change BookingRequestChange
		if err := rows.Scan(
			&change.ID,
			&change.ScheduleID,
			&change.PreviousSlotDate,
			&change.PreviousSlotHour,
			&change.PreviousActivity,
			&change.PreviousQuantity,
			&change.PreviousQuote,
			&change.NewSlotDate,
			&change.NewSlotHour,
			&change.NewActivity,
			&change.NewQuantity,
			&change.NewQuote,
			&change.ActionType,
			&change.ReviewNote,
			&change.ChangedByUserID,
			&change.ChangedByUserName,
			&change.ChangedAt,
		); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}

	return changes, rows.Err()
}

func (a *App) listBookingRequestChangesForScheduleIDs(scheduleIDs []int64) ([]BookingRequestChange, error) {
	return listBookingRequestChangesForScheduleIDsQuery(a.db, scheduleIDs)
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

func (a *App) countPendingSpaceSchedules() (int, error) {
	row := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM space_schedules
		WHERE status = 'pending'
	`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *App) schedulesForSlot(slotDate, slotHour string, excludeID int64) ([]SpaceSchedule, error) {
	return querySchedulesForSlot(a.db, slotDate, slotHour, excludeID)
}

type scheduleQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func querySchedulesForSlot(queryer scheduleQueryer, slotDate, slotHour string, excludeID int64) ([]SpaceSchedule, error) {
	rows, err := queryer.Query(`
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

func (a *App) listCoachesForGroup(groupID int64) ([]User, error) {
	rows, err := a.db.Query(`
		SELECT
			u.id,
			u.email,
			u.name,
			u.email_verified_at,
			u.created_at
		FROM users u
		JOIN student_group_coaches sgc
			ON sgc.user_id = u.id
		JOIN user_roles ur
			ON ur.user_id = u.id
		JOIN roles r
			ON r.id = ur.role_id
		WHERE sgc.group_id = ?
			AND r.name = 'coach'
		ORDER BY u.name COLLATE NOCASE ASC, u.id ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var coaches []User

	for rows.Next() {
		var coach User
		var verifiedAt sql.NullTime

		if err := rows.Scan(
			&coach.ID,
			&coach.Email,
			&coach.Name,
			&verifiedAt,
			&coach.CreatedAt,
		); err != nil {
			return nil, err
		}

		coach.Verified = verifiedAt.Valid
		coach.Roles = []string{"coach"}
		coaches = append(coaches, coach)
	}

	return coaches, rows.Err()
}

func (a *App) listStudentsForGroup(groupID int64) ([]Admission, error) {
	rows, err := a.db.Query(`
		SELECT a.id, a.student_id, a.full_name, COALESCE(a.admission_date, ''), a.date_of_birth, a.gender, a.address, a.passport_number, a.school,
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
	_, _, err := a.createAdmissionWithOptionalPayment(admission, false, 0)
	return err
}
func replaceStudentGroupCoachesTx(
	tx *sql.Tx,
	groupID int64,
	coachIDs []int64,
) error {
	if _, err := tx.Exec(
		`DELETE FROM student_group_coaches WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	now := time.Now().UTC()

	for _, coachID := range coachIDs {
		result, err := tx.Exec(`
			INSERT INTO student_group_coaches (
				group_id,
				user_id,
				created_at
			)
			SELECT ?, u.id, ?
			FROM users u
			JOIN user_roles ur
				ON ur.user_id = u.id
			JOIN roles r
				ON r.id = ur.role_id
			WHERE u.id = ?
				AND r.name = 'coach'
			LIMIT 1
		`,
			groupID,
			now,
			coachID,
		)
		if err != nil {
			return err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if affected != 1 {
			return errors.New("selected coach is invalid")
		}
	}

	return nil
}

func (a *App) createStudentGroup(
	group StudentGroup,
	admissionIDs []int64,
	coachIDs []int64,
) error {
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

	if err := replaceStudentGroupCoachesTx(tx, groupID, coachIDs); err != nil {
		return err
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

func (a *App) createCourtLayout(
	layout CourtLayout,
) (int64, error) {
	activities, err := a.listCourtActivities(
		layout.CourtID,
		false,
	)
	if err != nil {
		return 0, err
	}

	if err := validateCourtLayout(
		layout,
		activities,
	); err != nil {
		return 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	result, err := tx.Exec(`
		INSERT INTO court_layouts (
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		layout.CourtID,
		layout.Name,
		layout.Description,
		layout.Active,
		layout.SortOrder,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	layoutID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, item := range layout.Items {
		_, err := tx.Exec(`
			INSERT INTO court_layout_items (
				layout_id,
				activity,
				quantity
			)
			VALUES (?, ?, ?)
		`,
			layoutID,
			item.Activity,
			item.Quantity,
		)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return layoutID, nil
}

func (a *App) updateCourtLayout(
	layout CourtLayout,
) error {
	if layout.ID <= 0 {
		return errors.New("valid court layout is required")
	}

	activities, err := a.listCourtActivities(
		layout.CourtID,
		false,
	)
	if err != nil {
		return err
	}

	if err := validateCourtLayout(
		layout,
		activities,
	); err != nil {
		return err
	}

	if !layout.Active {
		var otherActiveLayouts int
		if err := a.db.QueryRow(`
			SELECT COUNT(*)
			FROM court_layouts
			WHERE active = 1
			  AND id <> ?
		`, layout.ID).Scan(&otherActiveLayouts); err != nil {
			return err
		}
		if otherActiveLayouts == 0 {
			return errors.New("at least one active court layout must remain available")
		}
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE court_layouts
		SET
			name = ?,
			description = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
		  AND court_id = ?
	`,
		layout.Name,
		layout.Description,
		layout.Active,
		layout.SortOrder,
		time.Now().UTC(),
		layout.ID,
		layout.CourtID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	if _, err := tx.Exec(`
		DELETE FROM court_layout_items
		WHERE layout_id = ?
	`, layout.ID); err != nil {
		return err
	}

	for _, item := range layout.Items {
		_, err := tx.Exec(`
			INSERT INTO court_layout_items (
				layout_id,
				activity,
				quantity
			)
			VALUES (?, ?, ?)
		`,
			layout.ID,
			item.Activity,
			item.Quantity,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) toggleCourtLayout(
	layoutID int64,
) error {
	if layoutID <= 0 {
		return errors.New("valid court layout is required")
	}

	var active bool

	err := a.db.QueryRow(`
		SELECT active
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(&active)
	if err != nil {
		return err
	}

	if active {
		var activeLayoutCount int
		if err := a.db.QueryRow(`
			SELECT COUNT(*)
			FROM court_layouts
			WHERE active = 1
		`).Scan(&activeLayoutCount); err != nil {
			return err
		}
		if activeLayoutCount <= 1 {
			return errors.New("at least one active court layout must remain available")
		}
	}

	_, err = a.db.Exec(`
		UPDATE court_layouts
		SET
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		!active,
		time.Now().UTC(),
		layoutID,
	)

	return err
}

func (a *App) deleteCourtLayout(
	layoutID int64,
) error {
	if layoutID <= 0 {
		return errors.New("valid court layout is required")
	}

	var activeLayoutCount int

	if err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts
		WHERE active = 1
	`).Scan(&activeLayoutCount); err != nil {
		return err
	}

	var deletingActive bool

	if err := a.db.QueryRow(`
		SELECT active
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(&deletingActive); err != nil {
		return err
	}

	if deletingActive && activeLayoutCount <= 1 {
		return errors.New(
			"the final active court layout cannot be deleted",
		)
	}

	result, err := a.db.Exec(`
		DELETE FROM court_layouts
		WHERE id = ?
	`, layoutID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) createSpaceSchedule(
	schedule SpaceSchedule,
) error {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := querySchedulesForSlot(
		tx,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
	)
	if err != nil {
		return err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		schedule,
		courtLayouts,
	); err != nil {
		return err
	}

	result, err := tx.Exec(`
		INSERT INTO space_schedules (
			slot_date,
			slot_hour,
			entry_type,
			activity,
			quantity,
			title,
			notes,
			status,
			requester_name,
			requester_email,
			requester_phone,
			requested_by_user_id,
			review_note,
			created_at,
			updated_at
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
	if err != nil {
		return err
	}

	scheduleID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	if schedule.EntryType == "booking" {
		if _, err := tx.Exec(`
			INSERT INTO booking_financials (
				schedule_id,
				quoted_amount,
				paid,
				payment_method,
				created_at,
				updated_at
			)
			VALUES (?, ?, 0, '', ?, ?)
		`,
			scheduleID,
			schedule.QuotedPrice,
			time.Now().UTC(),
			time.Now().UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
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

func (a *App) createEvent(event Event) error {
	_, err := a.db.Exec(`
		INSERT INTO events (
			title, category, event_date, start_time, end_time, registration_deadline, venue, summary,
			image_path, cta_label, cta_link, published, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.Title,
		event.Category,
		event.EventDate,
		event.StartTime,
		event.EndTime,
		nullIfBlank(event.RegistrationDeadline),
		event.Venue,
		event.Summary,
		event.ImagePath,
		event.CTALabel,
		event.CTALink,
		boolToInt(event.Published),
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) createPublicBookingRequest(
	schedule SpaceSchedule,
) (int64, error) {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return 0, fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return 0, err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return 0, fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	existing, err := querySchedulesForSlot(
		tx,
		schedule.SlotDate,
		schedule.SlotHour,
		0,
	)
	if err != nil {
		return 0, err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		schedule,
		courtLayouts,
	); err != nil {
		return 0, err
	}

	var requestedBy any

	if schedule.RequestedByUser > 0 {
		requestedBy = schedule.RequestedByUser
	}

	now := time.Now().UTC()

	result, err := tx.Exec(`
		INSERT INTO space_schedules (
			slot_date,
			slot_hour,
			entry_type,
			activity,
			quantity,
			title,
			notes,
			status,
			requester_name,
			requester_email,
			requester_phone,
			requested_by_user_id,
			review_note,
			created_at,
			updated_at
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
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	requestID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		INSERT INTO booking_financials (
			schedule_id,
			quoted_amount,
			paid,
			payment_method,
			created_at,
			updated_at
		)
		VALUES (?, ?, 0, '', ?, ?)
	`,
		requestID,
		schedule.QuotedPrice,
		now,
		now,
	); err != nil {
		return 0, err
	}

	if schedule.ReferralCode != "" {
		var partnerID int64

		if err := tx.QueryRow(`
			SELECT id
			FROM referral_partners
			WHERE code = ?
			  AND active = 1
		`,
			schedule.ReferralCode,
		).Scan(&partnerID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, errors.New(
					"the referral code is invalid or inactive",
				)
			}

			return 0, err
		}

		var commissionAmount float64

		if err := tx.QueryRow(`
			SELECT
				COALESCE(
					referral_commission_amount,
					0
				)
			FROM pricing_settings
			WHERE id = 1
		`).Scan(&commissionAmount); err != nil {
			return 0, err
		}

		if commissionAmount <= 0 {
			return 0, errors.New(
				"referral commission is not configured",
			)
		}

		if _, err := tx.Exec(`
			INSERT INTO booking_referrals (
				schedule_id,
				partner_id,
				commission_amount,
				paid,
				paid_at,
				payment_method,
				finance_transaction_id,
				created_at
			)
			VALUES (?, ?, ?, 0, NULL, '', NULL, ?)
		`,
			requestID,
			partnerID,
			commissionAmount,
			now,
		); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return requestID, nil
}

func (a *App) updateAdmission(admission Admission) error {
	result, err := a.db.Exec(`
		UPDATE admissions
		SET
			student_id = ?,
			full_name = ?,
			admission_date = ?,
			date_of_birth = ?,
			gender = ?,
			practice_type = ?,
			training_program_id = ?,
			address = ?,
			passport_number = ?,
			school = ?,
			guardian_name = ?,
			guardian_relationship = ?,
			guardian_contact_number = ?,
			guardian_alternative_contact_number = ?,
			medical_information = ?,
			updated_at = ?
		WHERE id = ?
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.TrainingProgramID,
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
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) createAdmissionWithOptionalPayment(
	admission Admission,
	collectPayment bool,
	recordedByUserID int64,
) (int64, int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	result, err := tx.Exec(`
		INSERT INTO admissions (
			student_id,
			full_name,
			admission_date,
			date_of_birth,
			gender,
			practice_type,
			training_program_id,
			address,
			passport_number,
			school,
			guardian_name,
			guardian_relationship,
			guardian_contact_number,
			guardian_alternative_contact_number,
			medical_information,
			payment_collected,
			payment_collected_at,
			admission_payment_amount,
			finance_transaction_id,
			created_at,
			updated_at
		)
		VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			0, NULL, 0, NULL, ?, ?
		)
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.TrainingProgramID,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		now,
		now,
	)
	if err != nil {
		return 0, 0, err
	}

	admissionID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}

	admission.ID = admissionID

	var financeTransactionID int64

	if collectPayment {
		financeTransactionID, err = a.collectAdmissionPaymentTx(
			tx,
			admission,
			recordedByUserID,
		)
		if err != nil {
			return 0, 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	return admissionID, financeTransactionID, nil
}

func (a *App) updateAdmissionWithOptionalPayment(
	admission Admission,
	collectPayment bool,
	recordedByUserID int64,
) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	existing, err := a.findAdmissionByIDTx(tx, admission.ID)
	if err != nil {
		return 0, err
	}

	result, err := tx.Exec(`
		UPDATE admissions
		SET
			student_id = ?,
			full_name = ?,
			admission_date = ?,
			date_of_birth = ?,
			gender = ?,
			practice_type = ?,
			training_program_id = ?,
			address = ?,
			passport_number = ?,
			school = ?,
			guardian_name = ?,
			guardian_relationship = ?,
			guardian_contact_number = ?,
			guardian_alternative_contact_number = ?,
			medical_information = ?,
			updated_at = ?
		WHERE id = ?
	`,
		admission.StudentID,
		admission.FullName,
		admission.AdmissionDate,
		admission.DateOfBirth,
		admission.Gender,
		admission.PracticeType,
		admission.TrainingProgramID,
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
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if rowsAffected == 0 {
		return 0, sql.ErrNoRows
	}

	var financeTransactionID int64

	if collectPayment && !existing.PaymentCollected {
		financeTransactionID, err = a.collectAdmissionPaymentTx(
			tx,
			admission,
			recordedByUserID,
		)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return financeTransactionID, nil
}

func (a *App) updateStudentGroup(
	group StudentGroup,
	admissionIDs []int64,
	coachIDs []int64,
) error {
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
	if err := replaceStudentGroupCoachesTx(tx, group.ID, coachIDs); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) updateSpaceSchedule(
	schedule SpaceSchedule,
) error {
	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	if err := validateConfiguredBookingOption(
		schedule,
		courtActivities,
		courtLayouts,
	); err != nil {
		return err
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	if err := validateScheduleAgainstClosures(
		schedule,
		courtClosures,
	); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentSlotDate string
	var currentSlotHour string
	var currentEntryType string
	var currentActivity string
	var currentQuantity int
	if err := tx.QueryRow(`
		SELECT slot_date, slot_hour, entry_type, activity, quantity
		FROM space_schedules
		WHERE id = ?
	`, schedule.ID).Scan(&currentSlotDate, &currentSlotHour, &currentEntryType, &currentActivity, &currentQuantity); err != nil {
		return err
	}

	existing, err := querySchedulesForSlot(
		tx,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.ID,
	)
	if err != nil {
		return err
	}

	if err := validateSpaceScheduleSlotAgainstLayouts(
		existing,
		schedule,
		courtLayouts,
	); err != nil {
		return err
	}

	result, err := tx.Exec(`
		UPDATE space_schedules
		SET
			slot_date = ?,
			slot_hour = ?,
			entry_type = ?,
			activity = ?,
			quantity = ?,
			title = ?,
			notes = ?,
			updated_at = ?
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
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	var financial struct {
		ID           int64
		QuotedAmount float64
		Paid         bool
	}
	financialErr := tx.QueryRow(`
		SELECT id, quoted_amount, paid
		FROM booking_financials
		WHERE schedule_id = ?
	`, schedule.ID).Scan(&financial.ID, &financial.QuotedAmount, &financial.Paid)
	if financialErr != nil && !errors.Is(financialErr, sql.ErrNoRows) {
		return financialErr
	}

	billingFieldsChanged := currentSlotDate != schedule.SlotDate ||
		currentSlotHour != schedule.SlotHour ||
		currentEntryType != schedule.EntryType ||
		currentActivity != schedule.Activity ||
		currentQuantity != schedule.Quantity

	if financial.Paid && billingFieldsChanged {
		return errors.New("paid bookings cannot change date, hour, entry type, activity, or quantity")
	}

	now := time.Now().UTC()

	switch schedule.EntryType {
	case "booking":
		if financialErr == nil {
			if _, err := tx.Exec(`
				UPDATE booking_financials
				SET quoted_amount = ?, updated_at = ?
				WHERE id = ?
			`, schedule.QuotedPrice, now, financial.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`
				INSERT INTO booking_financials (
					schedule_id,
					quoted_amount,
					paid,
					payment_method,
					created_at,
					updated_at
				)
				VALUES (?, ?, 0, '', ?, ?)
			`, schedule.ID, schedule.QuotedPrice, now, now); err != nil {
				return err
			}
		}
	case "training":
		if financialErr == nil {
			if financial.Paid {
				return errors.New("paid bookings cannot be converted to internal training")
			}
			if _, err := tx.Exec(`
				DELETE FROM booking_financials
				WHERE id = ?
			`, financial.ID); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
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

func (a *App) updateEvent(event Event) error {
	_, err := a.db.Exec(`
		UPDATE events
		SET title = ?, category = ?, event_date = ?, start_time = ?, end_time = ?, registration_deadline = ?, venue = ?, summary = ?,
		    image_path = ?, cta_label = ?, cta_link = ?, published = ?, updated_at = ?
		WHERE id = ?
	`,
		event.Title,
		event.Category,
		event.EventDate,
		event.StartTime,
		event.EndTime,
		nullIfBlank(event.RegistrationDeadline),
		event.Venue,
		event.Summary,
		event.ImagePath,
		event.CTALabel,
		event.CTALink,
		boolToInt(event.Published),
		time.Now().UTC(),
		event.ID,
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

func (a *App) updateReferralCommissionAmount(amount float64) error {
	_, err := a.db.Exec(`
		UPDATE pricing_settings
		SET referral_commission_amount = ?, updated_at = ?
		WHERE id = 1
	`, amount, time.Now().UTC())
	return err
}

func (a *App) createReferralPartner(partner ReferralPartner) error {
	_, err := a.db.Exec(`
		INSERT INTO referral_partners (name, code, email, phone, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
	`, partner.Name, partner.Code, partner.Email, partner.Phone, time.Now().UTC(), time.Now().UTC())
	return err
}

func (a *App) updateReferralPartner(partner ReferralPartner) error {
	result, err := a.db.Exec(`
		UPDATE referral_partners
		SET name = ?, code = ?, email = ?, phone = ?, updated_at = ?
		WHERE id = ?
	`, partner.Name, partner.Code, partner.Email, partner.Phone, time.Now().UTC(), partner.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("referral partner not found")
	}
	return nil
}

func (a *App) toggleReferralPartner(partnerID int64) error {
	result, err := a.db.Exec(`
		UPDATE referral_partners
		SET active = CASE active WHEN 1 THEN 0 ELSE 1 END, updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), partnerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("referral partner not found")
	}
	return nil
}

func (a *App) payReferralCommission(referralID int64, paymentMethod string, recordedByUserID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var referral BookingReferral
	var paid int
	if err := tx.QueryRow(`
		SELECT br.id, br.schedule_id, rp.name, br.commission_amount, s.status, br.paid
		FROM booking_referrals br
		JOIN referral_partners rp ON rp.id = br.partner_id
		JOIN space_schedules s ON s.id = br.schedule_id
		WHERE br.id = ?
	`, referralID).Scan(
		&referral.ID, &referral.ScheduleID, &referral.PartnerName,
		&referral.CommissionAmount, &referral.BookingStatus, &paid,
	); err != nil {
		return 0, err
	}
	if referral.BookingStatus != "confirmed" {
		return 0, errors.New("commission is not earned until the booking is confirmed")
	}
	if paid == 1 {
		return 0, errors.New("commission has already been paid")
	}
	if referral.CommissionAmount <= 0 {
		return 0, errors.New("commission amount is invalid")
	}

	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("REF-%s-%06d", now.Format("20060102150405"), referral.ID)
	description := fmt.Sprintf("Referral commission for %s", bookingReference(referral.ScheduleID))
	result, err := tx.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, receiptNumber, "referral_commission_payment", "booking_referral", referral.ID, referral.PartnerName,
		description, paymentMethod, -referral.CommissionAmount, recordedByUserID, now, now)
	if err != nil {
		return 0, err
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	updateResult, err := tx.Exec(`
		UPDATE booking_referrals
		SET paid = 1, paid_at = ?, payment_method = ?, finance_transaction_id = ?
		WHERE id = ? AND paid = 0
	`, now, paymentMethod, transactionID, referral.ID)
	if err != nil {
		return 0, err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil || affected != 1 {
		return 0, errors.New("commission payment could not be finalized")
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) createManualFinanceTransaction(category, personName, description, paymentMethod string, amount float64, recordedAt time.Time, recordedByUserID int64) (int64, error) {
	now := time.Now().UTC()
	prefix := "INC"
	if amount < 0 {
		prefix = "EXP"
	}
	receiptNumber := fmt.Sprintf("%s-%s-%09d", prefix, now.Format("20060102150405"), now.Nanosecond())
	result, err := a.db.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, ?, 'manual', NULL, ?, ?, ?, ?, ?, ?, ?)
	`, receiptNumber, category, personName, description, paymentMethod, amount, nullIfZero(recordedByUserID), recordedAt.UTC(), now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (a *App) collectBookingPayment(scheduleID int64, paymentMethod string, recordedByUserID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var financial BookingFinancial
	var paid int
	if err := tx.QueryRow(`
		SELECT bf.id, bf.quoted_amount, bf.paid, s.status, COALESCE(s.requester_name, ''), s.activity, s.quantity
		FROM booking_financials bf
		JOIN space_schedules s ON s.id = bf.schedule_id
		WHERE bf.schedule_id = ?
	`, scheduleID).Scan(
		&financial.ID, &financial.QuotedAmount, &paid, &financial.Status,
		&financial.RequesterName, &financial.Activity, &financial.Quantity,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("booking receivable was not found")
		}
		return 0, err
	}
	if financial.Status != "confirmed" {
		return 0, errors.New("only confirmed bookings can be paid")
	}
	if paid == 1 {
		return 0, ErrBookingPaymentAlreadyCollected
	}
	if financial.QuotedAmount <= 0 {
		return 0, errors.New("booking has no collectible price")
	}
	personName := financial.RequesterName
	if personName == "" {
		personName = "Booking customer"
	}
	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("BKG-%s-%06d", now.Format("20060102150405"), scheduleID)
	description := fmt.Sprintf("%s payment for %s", bookingProductLabel(financial.Activity, financial.Quantity), bookingReference(scheduleID))
	result, err := tx.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, 'booking_payment', 'space_schedule', ?, ?, ?, ?, ?, ?, ?, ?)
	`, receiptNumber, scheduleID, personName, description, paymentMethod, financial.QuotedAmount, nullIfZero(recordedByUserID), now, now)
	if err != nil {
		return 0, err
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	update, err := tx.Exec(`
		UPDATE booking_financials
		SET paid = 1, paid_at = ?, payment_method = ?, finance_transaction_id = ?, updated_at = ?
		WHERE id = ? AND paid = 0
	`, now, paymentMethod, transactionID, now, financial.ID)
	if err != nil {
		return 0, err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, ErrBookingPaymentAlreadyCollected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}
func (a *App) updateBookingRequestStatus(
	scheduleID int64,
	status string,
	reviewNote string,
) (*SpaceSchedule, error) {
	if status != "confirmed" &&
		status != "rejected" {
		return nil, errors.New(
			"invalid booking request status",
		)
	}

	courtActivities, courtLayouts, err :=
		a.activeBookingConfiguration()
	if err != nil {
		return nil, fmt.Errorf(
			"load active court configuration: %w",
			err,
		)
	}

	courtClosures, err :=
		a.listActiveCourtClosures()
	if err != nil {
		return nil, fmt.Errorf(
			"load active court closures: %w",
			err,
		)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	schedule, err :=
		findSpaceScheduleByIDQuery(
			tx,
			scheduleID,
		)
	if err != nil {
		return nil, err
	}

	if schedule.Status != "pending" {
		return nil, errors.New(
			"booking request is no longer pending",
		)
	}

	if status == "confirmed" {
		if err := validateBookableScheduleTime(
			*schedule,
			time.Now(),
		); err != nil {
			return nil, err
		}

		if err := validateConfiguredBookingOption(
			*schedule,
			courtActivities,
			courtLayouts,
		); err != nil {
			return nil, err
		}

		if err := validateScheduleAgainstClosures(
			*schedule,
			courtClosures,
		); err != nil {
			return nil, err
		}

		existing, err :=
			querySchedulesForSlot(
				tx,
				schedule.SlotDate,
				schedule.SlotHour,
				schedule.ID,
			)
		if err != nil {
			return nil, err
		}

		if err :=
			validateSpaceScheduleSlotAgainstLayouts(
				existing,
				*schedule,
				courtLayouts,
			); err != nil {
			return nil, err
		}
	}

	result, err := tx.Exec(`
		UPDATE space_schedules
		SET
			status = ?,
			review_note = ?,
			updated_at = ?
		WHERE id = ?
		  AND status = 'pending'
	`,
		status,
		reviewNote,
		time.Now().UTC(),
		scheduleID,
	)
	if err != nil {
		return nil, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}

	if affected == 0 {
		return nil, errors.New(
			"booking request is no longer pending",
		)
	}

	updated := *schedule
	updated.Status = status
	updated.ReviewNote = reviewNote
	updated.UpdatedAt = time.Now().UTC()

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &updated, nil
}

func (a *App) rescheduleBookingRequest(
	scheduleID int64,
	proposed SpaceSchedule,
	reviewNote string,
	actionType string,
	confirm bool,
	changedByUserID int64,
) (*BookingRequestRescheduleResult, error) {
	if actionType != "rescheduled" &&
		actionType != "rescheduled_confirmed" {
		return nil, errors.New("invalid booking request change action")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := findSpaceScheduleByIDQuery(tx, scheduleID)
	if err != nil {
		return nil, err
	}

	if current.Status != "pending" {
		return nil, errors.New("booking request is no longer pending")
	}
	if current.EntryType != "booking" {
		return nil, errors.New("only pending customer booking requests can be rescheduled")
	}
	if current.RequesterName == "" &&
		current.RequesterEmail == "" &&
		current.RequestedByUser == 0 {
		return nil, errors.New("only pending customer booking requests can be rescheduled")
	}

	var financial struct {
		ID           int64
		QuotedAmount float64
		Paid         bool
	}
	financialErr := tx.QueryRow(`
		SELECT id, quoted_amount, paid
		FROM booking_financials
		WHERE schedule_id = ?
	`, scheduleID).Scan(&financial.ID, &financial.QuotedAmount, &financial.Paid)
	if errors.Is(financialErr, sql.ErrNoRows) {
		return nil, errors.New("booking financial record was not found for this request")
	}
	if financialErr != nil {
		return nil, financialErr
	}
	if financial.Paid {
		return nil, errors.New("paid bookings cannot be rescheduled through the request workflow")
	}

	courtActivities, courtLayouts, err := activeBookingConfigurationQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load active court configuration: %w", err)
	}
	courtClosures, err := listActiveCourtClosuresQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load active court closures: %w", err)
	}
	pricings, err := listPricingRulesQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load pricing rules: %w", err)
	}
	settings, err := getPricingSettingsQuery(tx)
	if err != nil {
		return nil, fmt.Errorf("load pricing settings: %w", err)
	}

	updated := *current
	updated.SlotDate = proposed.SlotDate
	updated.SlotHour = proposed.SlotHour
	updated.EntryType = "booking"
	updated.Activity = proposed.Activity
	updated.Quantity = proposed.Quantity
	updated.ReviewNote = reviewNote
	if confirm {
		updated.Status = "confirmed"
	} else {
		updated.Status = "pending"
	}

	slotChanged := current.SlotDate != updated.SlotDate ||
		current.SlotHour != updated.SlotHour ||
		current.Activity != updated.Activity ||
		current.Quantity != updated.Quantity

	if slotChanged && strings.TrimSpace(reviewNote) == "" {
		return nil, errors.New("review note is required when changing the requested slot")
	}

	if err := validateBookableScheduleTime(updated, time.Now()); err != nil {
		return nil, err
	}
	if err := validateConfiguredBookingOption(updated, courtActivities, courtLayouts); err != nil {
		return nil, err
	}
	if err := validateScheduleAgainstClosures(updated, courtClosures); err != nil {
		return nil, err
	}

	existing, err := querySchedulesForSlot(
		tx,
		updated.SlotDate,
		updated.SlotHour,
		updated.ID,
	)
	if err != nil {
		return nil, err
	}
	if err := validateSpaceScheduleSlotAgainstLayouts(existing, updated, courtLayouts); err != nil {
		return nil, err
	}

	rule := pricingRuleForOption(pricings, updated.Activity, updated.Quantity)
	if rule == nil {
		return nil, errors.New("pricing is not configured for this booking")
	}
	updated.QuotedPrice = priceForRuleSlot(*rule, settings, updated.SlotDate, updated.SlotHour)
	if updated.QuotedPrice <= 0 {
		return nil, errors.New("a positive price is required before confirming this booking")
	}

	now := time.Now().UTC()
	result, err := tx.Exec(`
		UPDATE space_schedules
		SET
			slot_date = ?,
			slot_hour = ?,
			activity = ?,
			quantity = ?,
			status = ?,
			review_note = ?,
			updated_at = ?
		WHERE id = ?
		  AND status = 'pending'
	`,
		updated.SlotDate,
		updated.SlotHour,
		updated.Activity,
		updated.Quantity,
		updated.Status,
		reviewNote,
		now,
		scheduleID,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, errors.New("booking request is no longer pending")
	}

	if _, err := tx.Exec(`
		UPDATE booking_financials
		SET quoted_amount = ?, updated_at = ?
		WHERE id = ?
	`, updated.QuotedPrice, now, financial.ID); err != nil {
		return nil, err
	}

	var changeID int64
	if slotChanged || financial.QuotedAmount != updated.QuotedPrice {
		var changedBy any
		if changedByUserID > 0 {
			changedBy = changedByUserID
		}
		changeResult, err := tx.Exec(`
			INSERT INTO booking_request_changes (
				schedule_id,
				previous_slot_date,
				previous_slot_hour,
				previous_activity,
				previous_quantity,
				previous_quoted_price,
				new_slot_date,
				new_slot_hour,
				new_activity,
				new_quantity,
				new_quoted_price,
				action_type,
				review_note,
				changed_by_user_id,
				changed_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			scheduleID,
			current.SlotDate,
			current.SlotHour,
			current.Activity,
			current.Quantity,
			financial.QuotedAmount,
			updated.SlotDate,
			updated.SlotHour,
			updated.Activity,
			updated.Quantity,
			updated.QuotedPrice,
			actionType,
			reviewNote,
			changedBy,
			now,
		)
		if err != nil {
			return nil, err
		}
		changeID, err = changeResult.LastInsertId()
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &BookingRequestRescheduleResult{
		Schedule: &updated,
		ChangeID: changeID,
	}, nil
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

func (a *App) deleteEvent(eventID int64) error {
	_, err := a.db.Exec(`DELETE FROM events WHERE id = ?`, eventID)
	return err
}

func (a *App) findAdmissionByID(
	admissionID int64,
) (*Admission, error) {
	row := a.db.QueryRow(`
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number,
			a.guardian_alternative_contact_number,
			a.medical_information,
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		WHERE a.id = ?
	`, admissionID)

	var admission Admission
	var paymentCollected int
	var paymentCollectedAt sql.NullTime

	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
		&admission.TrainingProgramID,
		&admission.TrainingProgramName,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&paymentCollected,
		&paymentCollectedAt,
		&admission.AdmissionPaymentAmount,
		&admission.FinanceTransactionID,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}

	admission.PaymentCollected = paymentCollected == 1

	if paymentCollectedAt.Valid {
		admission.PaymentCollectedAt = paymentCollectedAt.Time
	}

	return &admission, nil
}

func (a *App) findAdmissionByIDTx(
	tx *sql.Tx,
	admissionID int64,
) (*Admission, error) {
	row := tx.QueryRow(`
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number,
			a.guardian_alternative_contact_number,
			a.medical_information,
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		WHERE a.id = ?
	`, admissionID)

	var admission Admission
	var paymentCollected int
	var paymentCollectedAt sql.NullTime

	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
		&admission.TrainingProgramID,
		&admission.TrainingProgramName,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&paymentCollected,
		&paymentCollectedAt,
		&admission.AdmissionPaymentAmount,
		&admission.FinanceTransactionID,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}

	admission.PaymentCollected = paymentCollected == 1

	if paymentCollectedAt.Valid {
		admission.PaymentCollectedAt = paymentCollectedAt.Time
	}

	return &admission, nil
}

func (a *App) findFinanceTransactionByID(transactionID int64) (*FinanceTransaction, error) {
	row := a.db.QueryRow(`
		SELECT id, receipt_number, category, reference_type, COALESCE(reference_id, 0), person_name, description,
		       payment_method, amount, COALESCE(recorded_by_user_id, 0), recorded_at, created_at
		FROM finance_transactions
		WHERE id = ?
	`, transactionID)

	var transaction FinanceTransaction
	if err := row.Scan(
		&transaction.ID,
		&transaction.ReceiptNumber,
		&transaction.Category,
		&transaction.ReferenceType,
		&transaction.ReferenceID,
		&transaction.PersonName,
		&transaction.Description,
		&transaction.PaymentMethod,
		&transaction.Amount,
		&transaction.RecordedByUser,
		&transaction.RecordedAt,
		&transaction.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &transaction, nil
}

func (a *App) collectAdmissionPaymentTx(tx *sql.Tx, admission Admission, recordedByUserID int64) (int64, error) {
	admissionFee, _, err := trainingProgramFeesForAdmissionTx(
		tx,
		admission,
	)
	if err != nil {
		return 0, err
	}

	if admissionFee <= 0 {
		return 0, ErrAdmissionFeeNotConfigured
	}

	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("ADM-%s-%06d", now.Format("20060102150405"), admission.ID)
	description := fmt.Sprintf("Admission payment for %s", admission.FullName)
	result, err := tx.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		receiptNumber,
		"admission_payment",
		"admission",
		admission.ID,
		admission.FullName,
		description,
		"cash",
		admissionFee,
		recordedByUserID,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	transactionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		UPDATE admissions
		SET payment_collected = 1,
		    payment_collected_at = ?,
		    admission_payment_amount = ?,
		    finance_transaction_id = ?,
		    updated_at = ?
		WHERE id = ?
	`,
		now,
		admissionFee,
		transactionID,
		now,
		admission.ID,
	); err != nil {
		return 0, err
	}

	return transactionID, nil
}

func trainingProgramFeesForAdmissionTx(
	tx *sql.Tx,
	admission Admission,
) (float64, float64, error) {
	if admission.TrainingProgramID > 0 {
		var admissionFee float64
		var monthlyFee float64

		err := tx.QueryRow(`
			SELECT
				COALESCE(admission_fee, 0),
				COALESCE(monthly_fee, 0)
			FROM training_programs
			WHERE id = ?
		`,
			admission.TrainingProgramID,
		).Scan(
			&admissionFee,
			&monthlyFee,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, errors.New(
					"the training programme assigned to this student was not found",
				)
			}

			return 0, 0, err
		}

		return admissionFee, monthlyFee, nil
	}

	// Temporary backward-compatibility path for admissions created
	// before training_program_id was introduced.
	pricing, err := admissionPricingByPracticeTypeTx(
		tx,
		admission.PracticeType,
	)
	if err != nil {
		return 0, 0, err
	}

	return pricing.Price, pricing.MonthlyFee, nil
}

func admissionPricingByPracticeTypeTx(
	tx *sql.Tx,
	practiceType string,
) (*AdmissionPricing, error) {
	row := tx.QueryRow(`
		SELECT
			id,
			practice_type,
			price,
			COALESCE(monthly_fee, 0),
			created_at,
			updated_at
		FROM admission_pricing
		WHERE practice_type = ?
		LIMIT 1
	`,
		practiceType,
	)

	var pricing AdmissionPricing

	if err := row.Scan(
		&pricing.ID,
		&pricing.PracticeType,
		&pricing.Price,
		&pricing.MonthlyFee,
		&pricing.CreatedAt,
		&pricing.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New(
				"legacy admission pricing is not configured for this student",
			)
		}

		return nil, err
	}

	return &pricing, nil
}

func (a *App) collectStudentMonthlyPayment(admissionID int64, paymentMonth string, monthDate time.Time, paymentMethod string, recordedByUserID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	admission, err := a.findAdmissionByIDTx(tx, admissionID)
	if err != nil {
		return 0, err
	}
	if admission.AdmissionDate > monthDate.AddDate(0, 1, -1).Format("2006-01-02") {
		return 0, ErrStudentNotAdmittedForMonth
	}

	var existingID int64
	err = tx.QueryRow(`
		SELECT id
		FROM student_monthly_payments
		WHERE admission_id = ? AND payment_month = ?
	`, admissionID, paymentMonth).Scan(&existingID)
	if err == nil {
		return 0, ErrStudentPaymentAlreadyCollected
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	_, monthlyFee, err := trainingProgramFeesForAdmissionTx(
		tx,
		*admission,
	)
	if err != nil {
		return 0, err
	}

	if monthlyFee <= 0 {
		return 0, ErrMonthlyFeeNotConfigured
	}
	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("STU-%s-%06d-%s", strings.ReplaceAll(paymentMonth, "-", ""), admission.ID, now.Format("150405"))
	description := fmt.Sprintf("%s monthly payment for %s", paymentMonthLabel(paymentMonth), admission.FullName)
	result, err := tx.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, category, reference_type, reference_id, person_name, description,
			payment_method, amount, recorded_by_user_id, recorded_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		receiptNumber,
		"student_monthly_payment",
		"admission",
		admission.ID,
		admission.FullName,
		description,
		paymentMethod,
		monthlyFee,
		recordedByUserID,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		INSERT INTO student_monthly_payments (
			admission_id, payment_month, amount, payment_method, finance_transaction_id,
			collected_by_user_id, collected_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, admission.ID, paymentMonth, monthlyFee, paymentMethod, transactionID, recordedByUserID, now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, ErrStudentPaymentAlreadyCollected
		}
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
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
	return findSpaceScheduleByIDQuery(a.db, scheduleID)
}

type scheduleRowQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func findSpaceScheduleByIDQuery(queryer scheduleRowQueryer, scheduleID int64) (*SpaceSchedule, error) {
	row := queryer.QueryRow(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       created_at, updated_at
		FROM space_schedules
		WHERE id = ?
	`, scheduleID)

	return scanSpaceSchedule(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanSpaceSchedule(row rowScanner) (*SpaceSchedule, error) {
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

func (a *App) findEventByID(eventID int64) (*Event, error) {
	row := a.db.QueryRow(`
		SELECT id, title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		WHERE id = ?
	`, eventID)

	var event Event
	var published int
	if err := row.Scan(
		&event.ID,
		&event.Title,
		&event.Category,
		&event.EventDate,
		&event.StartTime,
		&event.EndTime,
		&event.RegistrationDeadline,
		&event.Venue,
		&event.Summary,
		&event.ImagePath,
		&event.CTALabel,
		&event.CTALink,
		&published,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, err
	}
	event.Published = published == 1
	return &event, nil
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
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			training_program_id INTEGER,
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
		`CREATE TABLE IF NOT EXISTS student_group_coaches (
			group_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (group_id, user_id),
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS attendance_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL,
			admission_id INTEGER NOT NULL,
			attendance_date TEXT NOT NULL,
			status TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (recorded_by_user_id) REFERENCES users(id)
		)`,

		`CREATE TABLE IF NOT EXISTS courts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS court_activities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			court_id INTEGER NOT NULL,
			activity TEXT NOT NULL,
			display_name TEXT NOT NULL,
			max_quantity INTEGER NOT NULL DEFAULT 1,
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(court_id, activity),
			FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_layouts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			court_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(court_id, name),
			FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_layout_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			layout_id INTEGER NOT NULL,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			UNIQUE(layout_id, activity),
			FOREIGN KEY (layout_id) REFERENCES court_layouts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_closures (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	court_id INTEGER NOT NULL,
	closure_date TEXT NOT NULL,
	start_hour TEXT NOT NULL,
	end_hour TEXT NOT NULL,
	activity TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	active INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
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
			referral_commission_amount REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admission_pricing (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			practice_type TEXT NOT NULL UNIQUE,
			price REAL NOT NULL DEFAULT 0,
			monthly_fee REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS training_programs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			activity TEXT NOT NULL,
			training_format TEXT NOT NULL,
			admission_fee REAL NOT NULL DEFAULT 0,
			monthly_fee REAL NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(activity, training_format)
		)`,
		`CREATE TABLE IF NOT EXISTS finance_transactions (
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
		`CREATE TABLE IF NOT EXISTS student_monthly_payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			payment_month TEXT NOT NULL,
			amount REAL NOT NULL DEFAULT 0,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			finance_transaction_id INTEGER NOT NULL,
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			category TEXT NOT NULL,
			event_date TEXT NOT NULL,
			start_time TEXT,
			end_time TEXT,
			registration_deadline TEXT,
			venue TEXT NOT NULL,
			summary TEXT NOT NULL,
			image_path TEXT NOT NULL DEFAULT '',
			cta_label TEXT NOT NULL DEFAULT '',
			cta_link TEXT NOT NULL DEFAULT '',
			published INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS referral_partners (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
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
		`CREATE TABLE IF NOT EXISTS booking_referrals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL UNIQUE,
			partner_id INTEGER NOT NULL,
			commission_amount REAL NOT NULL,
			paid INTEGER NOT NULL DEFAULT 0,
			paid_at DATETIME,
			payment_method TEXT NOT NULL DEFAULT '',
			finance_transaction_id INTEGER,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (partner_id) REFERENCES referral_partners(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_financials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL UNIQUE,
			quoted_amount REAL NOT NULL DEFAULT 0,
			paid INTEGER NOT NULL DEFAULT 0,
			paid_at DATETIME,
			payment_method TEXT NOT NULL DEFAULT '',
			finance_transaction_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_request_changes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			previous_slot_date TEXT NOT NULL,
			previous_slot_hour TEXT NOT NULL,
			previous_activity TEXT NOT NULL,
			previous_quantity INTEGER NOT NULL,
			previous_quoted_price REAL NOT NULL DEFAULT 0,
			new_slot_date TEXT NOT NULL,
			new_slot_hour TEXT NOT NULL,
			new_activity TEXT NOT NULL,
			new_quantity INTEGER NOT NULL,
			new_quoted_price REAL NOT NULL DEFAULT 0,
			action_type TEXT NOT NULL,
			review_note TEXT NOT NULL DEFAULT '',
			changed_by_user_id INTEGER,
			changed_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (changed_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_communications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			related_event_type TEXT NOT NULL DEFAULT '',
			event_key TEXT NOT NULL,
			channel TEXT NOT NULL,
			recipient TEXT NOT NULL,
			subject TEXT NOT NULL DEFAULT '',
			body_preview TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			provider TEXT NOT NULL DEFAULT '',
			provider_message TEXT NOT NULL DEFAULT '',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at DATETIME,
			sent_at DATETIME,
			created_at DATETIME NOT NULL,
			created_by_user_id INTEGER,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by_user_id) REFERENCES users(id)
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
		`CREATE INDEX IF NOT EXISTS idx_courts_active_order
		ON courts(active, sort_order, name)`,

		`CREATE INDEX IF NOT EXISTS idx_court_activities_court
		ON court_activities(court_id, active, sort_order)`,

		`CREATE INDEX IF NOT EXISTS idx_court_layouts_court
		ON court_layouts(court_id, active, sort_order)`,

		`CREATE INDEX IF NOT EXISTS idx_court_layout_items_layout
		ON court_layout_items(layout_id)`,
		`CREATE INDEX IF NOT EXISTS idx_court_closures_date
ON court_closures(closure_date, start_hour, end_hour)`,

		`CREATE INDEX IF NOT EXISTS idx_court_closures_court
ON court_closures(court_id, active, closure_date)`,

		`CREATE INDEX IF NOT EXISTS idx_court_closures_activity
ON court_closures(activity, active, closure_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_rules_option ON pricing_rules(activity, quantity)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_admission_pricing_type ON admission_pricing(practice_type)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_recorded_at ON finance_transactions(recorded_at)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_reference ON finance_transactions(reference_type, reference_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_student_monthly_payment_student_month ON student_monthly_payments(admission_id, payment_month)`,
		`CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_month ON student_monthly_payments(payment_month, collected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_date ON events(event_date, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_events_published ON events(published, event_date)`,
		`CREATE INDEX IF NOT EXISTS idx_space_schedules_slot ON space_schedules(slot_date, slot_hour)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_referrals_partner ON booking_referrals(partner_id, paid)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_financials_paid ON booking_financials(paid, schedule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_request_changes_schedule ON booking_request_changes(schedule_id, changed_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_booking_communications_event_channel ON booking_communications(event_key, channel)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_schedule ON booking_communications(schedule_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_status ON booking_communications(status, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_event_type ON booking_communications(event_type, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_created_at ON booking_communications(created_at DESC)`,
		`ALTER TABLE events ADD COLUMN registration_deadline TEXT`,
		`ALTER TABLE events ADD COLUMN image_path TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admissions ADD COLUMN student_id TEXT`,
		`ALTER TABLE admissions ADD COLUMN admission_date TEXT`,
		`ALTER TABLE admissions ADD COLUMN practice_type TEXT NOT NULL DEFAULT 'group_practice'`,
		`ALTER TABLE admissions ADD COLUMN payment_collected INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE admissions ADD COLUMN payment_collected_at DATETIME`,
		`ALTER TABLE admissions ADD COLUMN admission_payment_amount REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE admissions ADD COLUMN finance_transaction_id INTEGER`,
		`CREATE INDEX IF NOT EXISTS idx_training_programs_active
		ON training_programs(active)`,
		`CREATE INDEX IF NOT EXISTS idx_training_programs_sort_order
		ON training_programs(sort_order, name)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil && !isIgnorableMigrationError(err, stmt) {
			return err
		}
	}

	trainingProgramIDExists, err := tableHasColumn(
		db,
		"admissions",
		"training_program_id",
	)
	if err != nil {
		return fmt.Errorf(
			"check admissions training_program_id column: %w",
			err,
		)
	}

	if !trainingProgramIDExists {
		if _, err := db.Exec(`
			ALTER TABLE admissions
			ADD COLUMN training_program_id INTEGER
		`); err != nil {
			return fmt.Errorf(
				"add admissions training_program_id column: %w",
				err,
			)
		}
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_admissions_training_program_id
		ON admissions(training_program_id)
	`); err != nil {
		return fmt.Errorf(
			"create admissions training programme index: %w",
			err,
		)
	}

	if _, err := db.Exec(`UPDATE admissions SET student_id = 'STD-' || printf('%05d', id) WHERE student_id IS NULL OR TRIM(student_id) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions
		SET admission_date = CASE
			WHEN created_at IS NOT NULL AND LENGTH(TRIM(CAST(created_at AS TEXT))) >= 10 THEN SUBSTR(TRIM(CAST(created_at AS TEXT)), 1, 10)
			ELSE ''
		END
		WHERE admission_date IS NULL OR TRIM(admission_date) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET practice_type = 'group_practice' WHERE practice_type IS NULL OR TRIM(practice_type) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET payment_collected = 0 WHERE payment_collected IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET admission_payment_amount = 0 WHERE admission_payment_amount IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admissions_admission_date ON admissions(admission_date)`); err != nil {
		return err
	}
	attendanceRecordedByExists, err := tableHasColumn(
		db,
		"attendance_records",
		"recorded_by_user_id",
	)
	if err != nil {
		return fmt.Errorf(
			"check attendance recorded_by_user_id column: %w",
			err,
		)
	}

	if !attendanceRecordedByExists {
		if _, err := db.Exec(`
			ALTER TABLE attendance_records
			ADD COLUMN recorded_by_user_id INTEGER
		`); err != nil {
			return fmt.Errorf(
				"add attendance recorded_by_user_id column: %w",
				err,
			)
		}
	}

	admissionPricingMonthlyFeeExists, err := tableHasColumn(db, "admission_pricing", "monthly_fee")
	if err != nil {
		return err
	}
	if !admissionPricingMonthlyFeeExists {
		if _, err := db.Exec(`ALTER TABLE admission_pricing ADD COLUMN monthly_fee REAL NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE admission_pricing SET monthly_fee = 0 WHERE monthly_fee IS NULL`); err != nil {
		return err
	}
	referralCommissionExists, err := tableHasColumn(db, "pricing_settings", "referral_commission_amount")
	if err != nil {
		return err
	}
	if !referralCommissionExists {
		if _, err := db.Exec(`ALTER TABLE pricing_settings ADD COLUMN referral_commission_amount REAL NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE pricing_settings SET referral_commission_amount = 0 WHERE referral_commission_amount IS NULL`); err != nil {
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
	if err := backfillBookingFinancials(db); err != nil {
		return err
	}

	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM email_verifications WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

func seedTrainingPrograms(db *sql.DB) error {
	now := time.Now().UTC()

	programs := []TrainingProgram{
		{
			Name:           "1 to 1 Cricket Practice",
			Activity:       "cricket",
			TrainingFormat: "one_to_one",
			SortOrder:      10,
		},
		{
			Name:           "Group Practice - Cricket",
			Activity:       "cricket",
			TrainingFormat: "group",
			SortOrder:      20,
		},
		{
			Name:           "1 to 1 Zumba Practice",
			Activity:       "zumba",
			TrainingFormat: "one_to_one",
			SortOrder:      30,
		},
		{
			Name:           "Group Practice - Zumba",
			Activity:       "zumba",
			TrainingFormat: "group",
			SortOrder:      40,
		},
		{
			Name:           "1 to 1 Badminton Practice",
			Activity:       "badminton",
			TrainingFormat: "one_to_one",
			SortOrder:      50,
		},
		{
			Name:           "Group Practice - Badminton",
			Activity:       "badminton",
			TrainingFormat: "group",
			SortOrder:      60,
		},
	}

	for _, program := range programs {
		_, err := db.Exec(`
			INSERT INTO training_programs (
				name,
				activity,
				training_format,
				admission_fee,
				monthly_fee,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, 0, 0, 1, ?, ?, ?)
			ON CONFLICT(activity, training_format) DO NOTHING
		`,
			program.Name,
			program.Activity,
			program.TrainingFormat,
			program.SortOrder,
			now,
			now,
		)
		if err != nil {
			return fmt.Errorf(
				"seed training programme %q: %w",
				program.Name,
				err,
			)
		}
	}

	return nil
}

func backfillBookingFinancials(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT s.id, s.slot_date, s.slot_hour, s.activity, s.quantity,
		       CASE
		         WHEN CAST(strftime('%w', s.slot_date) AS INTEGER) IN (0, 6) THEN
		           CASE WHEN s.slot_hour BETWEEN ps.peak_start_hour AND ps.peak_end_hour
		                THEN pr.weekend_peak_price ELSE pr.weekend_offpeak_price END
		         ELSE
		           CASE WHEN s.slot_hour BETWEEN ps.peak_start_hour AND ps.peak_end_hour
		                THEN pr.weekday_peak_price ELSE pr.weekday_offpeak_price END
		       END AS quoted_amount
		FROM space_schedules s
		JOIN pricing_rules pr ON pr.activity = s.activity AND pr.quantity = s.quantity
		JOIN pricing_settings ps ON ps.id = 1
		LEFT JOIN booking_financials bf ON bf.schedule_id = s.id
		WHERE s.entry_type = 'booking' AND bf.id IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		scheduleID int64
		amount     float64
	}
	var missing []row
	for rows.Next() {
		var item row
		var slotDate, slotHour, activity string
		var quantity int
		if err := rows.Scan(&item.scheduleID, &slotDate, &slotHour, &activity, &quantity, &item.amount); err != nil {
			return err
		}
		missing = append(missing, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, item := range missing {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO booking_financials (
				schedule_id, quoted_amount, paid, payment_method, created_at, updated_at
			) VALUES (?, ?, 0, '', ?, ?)
		`, item.scheduleID, item.amount, now, now); err != nil {
			return err
		}
	}
	return nil
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
		"customer": {"dashboard.view"},
		"editor":   {"dashboard.view", "editor.access"},
		"coach":    {"dashboard.view", "attendance.manage"},
		"admin": {
			"dashboard.view",
			"editor.access",
			"users.manage",
			"roles.manage",
			"admissions.manage",
			"training_programs.manage",
			"student_groups.manage",
			"attendance.manage",
			"space_bookings.manage",
			"booking_requests.manage",
			"pricing.manage",
			"finance.manage",
			"reports.view",
			"events.manage",
		},
		"superadmin": allPermissions,
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

func verifyCourtManagerConfiguration(db *sql.DB) error {
	var activeCourtCount int

	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM courts
		WHERE active = 1
	`).Scan(&activeCourtCount)
	if err != nil {
		return err
	}

	if activeCourtCount == 0 {
		return errors.New("court manager has no active courts")
	}

	var activeLayoutCount int

	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		WHERE cl.active = 1
		  AND c.active = 1
	`).Scan(&activeLayoutCount)
	if err != nil {
		return err
	}

	if activeLayoutCount == 0 {
		return errors.New("court manager has no active court layouts")
	}

	var emptyLayoutCount int

	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts cl
		WHERE cl.active = 1
		AND NOT EXISTS (
			SELECT 1
			FROM court_layout_items cli
			WHERE cli.layout_id = cl.id
		)
	`).Scan(&emptyLayoutCount)
	if err != nil {
		return err
	}

	if emptyLayoutCount > 0 {
		return errors.New("court manager contains an empty active layout")
	}

	return nil
}

func seedCourtManager(db *sql.DB) error {
	var existingCourtCount int

	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM courts
	`).Scan(&existingCourtCount); err != nil {
		return fmt.Errorf(
			"count existing courts before seed: %w",
			err,
		)
	}

	if existingCourtCount > 0 {
		return nil
	}
	tx, err := db.Begin()

	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO courts (
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, 1, 10, ?, ?)
	`,
		"Main Indoor Court",
		"MAIN_INDOOR",
		"Shared multipurpose indoor court used by badminton, cricket nets, table tennis, futsal, tennis, indoor cricket, and training.",
		now,
		now,
	)
	if err != nil {
		return err
	}

	var courtID int64
	err = tx.QueryRow(`
		SELECT id
		FROM courts
		WHERE code = ?
	`, "MAIN_INDOOR").Scan(&courtID)
	if err != nil {
		return err
	}

	activities := []struct {
		Activity    string
		DisplayName string
		MaxQuantity int
		SortOrder   int
	}{
		{
			Activity:    "full_indoor_cricket",
			DisplayName: "Full Indoor Cricket",
			MaxQuantity: 1,
			SortOrder:   10,
		},
		{
			Activity:    "futsal",
			DisplayName: "Futsal",
			MaxQuantity: 1,
			SortOrder:   20,
		},
		{
			Activity:    "badminton",
			DisplayName: "Badminton",
			MaxQuantity: 1,
			SortOrder:   30,
		},
		{
			Activity:    "cricket_net",
			DisplayName: "Cricket Net",
			MaxQuantity: 3,
			SortOrder:   40,
		},
		{
			Activity:    "table_tennis",
			DisplayName: "Table Tennis",
			MaxQuantity: 2,
			SortOrder:   50,
		},
		{
			Activity:    "tennis",
			DisplayName: "Tennis",
			MaxQuantity: 1,
			SortOrder:   60,
		},
		{
			Activity:    "training",
			DisplayName: "Training Session",
			MaxQuantity: 1,
			SortOrder:   70,
		},
	}

	for _, activity := range activities {
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO court_activities (
				court_id,
				activity,
				display_name,
				max_quantity,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?)
		`,
			courtID,
			activity.Activity,
			activity.DisplayName,
			activity.MaxQuantity,
			activity.SortOrder,
			now,
			now,
		)
		if err != nil {
			return err
		}
	}

	type seedLayoutItem struct {
		Activity string
		Quantity int
	}

	type seedLayout struct {
		Name        string
		Description string
		SortOrder   int
		Items       []seedLayoutItem
	}

	layouts := []seedLayout{
		{
			Name:        "Full Indoor Cricket",
			Description: "Full-court indoor cricket configuration.",
			SortOrder:   10,
			Items: []seedLayoutItem{
				{Activity: "full_indoor_cricket", Quantity: 1},
			},
		},
		{
			Name:        "Futsal",
			Description: "Full-court futsal configuration.",
			SortOrder:   20,
			Items: []seedLayoutItem{
				{Activity: "futsal", Quantity: 1},
			},
		},
		{
			Name:        "Badminton and Cricket Net",
			Description: "One badminton booking and one cricket-net booking may operate simultaneously.",
			SortOrder:   30,
			Items: []seedLayoutItem{
				{Activity: "badminton", Quantity: 1},
				{Activity: "cricket_net", Quantity: 1},
			},
		},
		{
			Name:        "Badminton and Table Tennis",
			Description: "One badminton booking and one table-tennis booking may operate simultaneously.",
			SortOrder:   40,
			Items: []seedLayoutItem{
				{Activity: "badminton", Quantity: 1},
				{Activity: "table_tennis", Quantity: 1},
			},
		},
		{
			Name:        "Three Cricket Nets",
			Description: "Up to three cricket nets may operate simultaneously.",
			SortOrder:   50,
			Items: []seedLayoutItem{
				{Activity: "cricket_net", Quantity: 3},
			},
		},
		{
			Name:        "Two Table Tennis Tables",
			Description: "Up to two table-tennis bookings may operate simultaneously.",
			SortOrder:   60,
			Items: []seedLayoutItem{
				{Activity: "table_tennis", Quantity: 2},
			},
		},
		{
			Name:        "Tennis",
			Description: "Full-court tennis configuration.",
			SortOrder:   70,
			Items: []seedLayoutItem{
				{Activity: "tennis", Quantity: 1},
			},
		},
		{
			Name:        "Training Session",
			Description: "Training session that reserves the complete configured court.",
			SortOrder:   80,
			Items: []seedLayoutItem{
				{Activity: "training", Quantity: 1},
			},
		},
	}

	for _, layout := range layouts {
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO court_layouts (
				court_id,
				name,
				description,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, 1, ?, ?, ?)
		`,
			courtID,
			layout.Name,
			layout.Description,
			layout.SortOrder,
			now,
			now,
		)
		if err != nil {
			return err
		}

		var layoutID int64
		err = tx.QueryRow(`
			SELECT id
			FROM court_layouts
			WHERE court_id = ?
			  AND name = ?
		`,
			courtID,
			layout.Name,
		).Scan(&layoutID)
		if err != nil {
			return err
		}

		for _, item := range layout.Items {
			_, err = tx.Exec(`
				INSERT OR IGNORE INTO court_layout_items (
					layout_id,
					activity,
					quantity
				)
				VALUES (?, ?, ?)
			`,
				layoutID,
				item.Activity,
				item.Quantity,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (a *App) activeBookingConfiguration() (
	[]CourtActivity,
	[]CourtLayout,
	error,
) {
	return activeBookingConfigurationQuery(a.db)
}

func seedPricingRules(db *sql.DB) error {
	for _, option := range defaultBookingOptionCatalog() {
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
		{PracticeType: "group_practice", Price: 0, MonthlyFee: 0},
		{PracticeType: "one_to_one_practice", Price: 0, MonthlyFee: 0},
	}
	for _, pricing := range defaults {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO admission_pricing (practice_type, price, monthly_fee, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, pricing.PracticeType, pricing.Price, pricing.MonthlyFee, now, now); err != nil {
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
	body := fmt.Sprintf(
		"Hi %s,\r\n\r\nYour mekmaa3 verification code is %s.\r\nIt expires in 10 minutes.\r\n\r\nIf you did not create this account, you can ignore this email.\r\n",
		user.Name,
		otp,
	)
	return a.sendEmailMessage(user.Email, "Verify your email address", body, "")
}

func (a *App) sendBookingConfirmationSMS(schedule *SpaceSchedule) error {
	if schedule == nil {
		return errors.New("schedule is required")
	}
	return a.sendSMSMessage(schedule.RequesterPhone, buildBookingConfirmationSMSBody(schedule))
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

type bookingCommunicationDispatch struct {
	Channel     string
	Recipient   string
	Subject     string
	TextBody    string
	HTMLBody    string
	BodyPreview string
}

type bookingEmailFact struct {
	Label string
	Value string
}

type bookingCommunicationContent struct {
	Subject string
	Heading string
	Intro   string
	Facts   []bookingEmailFact
	Notes   []string
	SMSBody string
}

func (a *App) sendEmailMessage(recipient string, subject string, textBody string, htmlBody string) error {
	if !a.smtp.Enabled {
		return errors.New("smtp is not configured")
	}
	if !emailPattern.MatchString(strings.TrimSpace(recipient)) {
		return errors.New("recipient email address is invalid")
	}

	var message bytes.Buffer
	message.WriteString("From: " + a.smtp.From + "\r\n")
	message.WriteString("To: " + recipient + "\r\n")
	message.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")

	if strings.TrimSpace(htmlBody) == "" {
		message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		message.WriteString(textBody)
	} else {
		boundary := fmt.Sprintf("mekmaa-%d", time.Now().UTC().UnixNano())
		message.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n")
		message.WriteString("--" + boundary + "\r\n")
		message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		message.WriteString(textBody + "\r\n")
		message.WriteString("--" + boundary + "\r\n")
		message.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		message.WriteString(htmlBody + "\r\n")
		message.WriteString("--" + boundary + "--\r\n")
	}

	auth := smtp.PlainAuth("", a.smtp.Username, a.smtp.Password, a.smtp.Host)
	return smtp.SendMail(a.smtp.Host+":"+a.smtp.Port, auth, a.smtp.From, []string{recipient}, message.Bytes())
}

func (a *App) sendSMSMessage(phone string, message string) error {
	if !a.sms.Enabled {
		return errors.New("sms is not configured")
	}

	normalizedPhone, err := normalizeSMSPhone(phone)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("user_id", a.sms.UserID)
	form.Set("api_key", a.sms.APIKey)
	form.Set("sender_id", a.sms.SenderID)
	form.Set("contact", normalizedPhone)
	form.Set("message", message)

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

func (a *App) sendBookingCommunicationEvent(
	scheduleID int64,
	eventType string,
	relatedEventType string,
	eventKey string,
	createdByUserID int64,
) ([]BookingCommunication, error) {
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		return nil, err
	}
	financials, err := a.listBookingFinancialsForScheduleIDs([]int64{scheduleID})
	if err != nil {
		return nil, err
	}
	referrals, err := a.listBookingReferralsForScheduleIDs([]int64{scheduleID})
	if err != nil {
		return nil, err
	}
	changes, err := a.listBookingRequestChangesForScheduleIDs([]int64{scheduleID})
	if err != nil {
		return nil, err
	}

	dispatches, err := a.buildBookingCommunicationDispatches(*schedule, bookingFinancialForSchedule(financials, scheduleID), bookingReferralFor(referrals, scheduleID), changes, eventType, relatedEventType)
	if err != nil {
		return nil, err
	}
	if len(dispatches) == 0 {
		return nil, errors.New("no customer communication recipient is available")
	}

	results := make([]BookingCommunication, 0, len(dispatches))
	for _, dispatch := range dispatches {
		record, duplicate, err := a.createPendingBookingCommunication(BookingCommunication{
			ScheduleID:       scheduleID,
			EventType:        eventType,
			RelatedEventType: relatedEventType,
			EventKey:         eventKey,
			Channel:          dispatch.Channel,
			Recipient:        dispatch.Recipient,
			Subject:          dispatch.Subject,
			BodyPreview:      dispatch.BodyPreview,
			CreatedByUserID:  createdByUserID,
		})
		if err != nil {
			return results, err
		}
		if duplicate {
			results = append(results, *record)
			continue
		}

		provider, providerMessage, sendErr := a.deliverBookingCommunicationDispatch(dispatch)
		status := bookingCommStatusSent
		if sendErr != nil {
			status = bookingCommStatusFailed
		}
		if err := a.completeBookingCommunicationAttempt(record.ID, status, provider, providerMessage); err != nil {
			return results, err
		}
		record.Status = status
		record.Provider = provider
		record.ProviderMessage = truncateString(strings.TrimSpace(providerMessage), 300)
		record.AttemptCount = 1
		record.LastAttemptAt = time.Now().UTC()
		if status == bookingCommStatusSent {
			record.SentAt = record.LastAttemptAt
		}
		results = append(results, *record)
	}

	return results, nil
}

func (a *App) deliverBookingCommunicationDispatch(dispatch bookingCommunicationDispatch) (string, string, error) {
	switch dispatch.Channel {
	case bookingCommChannelEmail:
		if !a.bookingMessages.EmailEnabled {
			return "smtp", "booking email delivery is disabled by configuration", errors.New("booking email delivery is disabled by configuration")
		}
		if err := a.sendEmailMessage(dispatch.Recipient, dispatch.Subject, dispatch.TextBody, dispatch.HTMLBody); err != nil {
			return "smtp", err.Error(), err
		}
		return "smtp", "", nil
	case bookingCommChannelSMS:
		if !a.bookingMessages.SMSEnabled {
			return "smslenz", "booking sms delivery is disabled by configuration", errors.New("booking sms delivery is disabled by configuration")
		}
		if err := a.sendSMSMessage(dispatch.Recipient, dispatch.TextBody); err != nil {
			return "smslenz", err.Error(), err
		}
		return "smslenz", "", nil
	default:
		return "", "unsupported communication channel", errors.New("unsupported communication channel")
	}
}

func (a *App) buildBookingCommunicationDispatches(
	schedule SpaceSchedule,
	financial *BookingFinancial,
	referral *BookingReferral,
	changes []BookingRequestChange,
	eventType string,
	relatedEventType string,
) ([]bookingCommunicationDispatch, error) {
	effectiveEventType := eventType
	if eventType == bookingCommEventResent {
		effectiveEventType = relatedEventType
	}

	content, err := a.buildBookingCommunicationContent(schedule, financial, referral, changes, effectiveEventType)
	if err != nil {
		return nil, err
	}

	dispatches := make([]bookingCommunicationDispatch, 0, 2)
	if emailPattern.MatchString(strings.TrimSpace(schedule.RequesterEmail)) {
		dispatches = append(dispatches, bookingCommunicationDispatch{
			Channel:     bookingCommChannelEmail,
			Recipient:   schedule.RequesterEmail,
			Subject:     content.Subject,
			TextBody:    renderBookingEmailText(content),
			HTMLBody:    renderBookingEmailHTML(content),
			BodyPreview: truncateString(renderBookingEmailText(content), 240),
		})
	}
	if strings.TrimSpace(content.SMSBody) != "" && strings.TrimSpace(schedule.RequesterPhone) != "" {
		dispatches = append(dispatches, bookingCommunicationDispatch{
			Channel:     bookingCommChannelSMS,
			Recipient:   schedule.RequesterPhone,
			Subject:     content.Subject,
			TextBody:    content.SMSBody,
			BodyPreview: truncateString(content.SMSBody, 240),
		})
	}
	return dispatches, nil
}

func (a *App) buildBookingCommunicationContent(
	schedule SpaceSchedule,
	financial *BookingFinancial,
	referral *BookingReferral,
	changes []BookingRequestChange,
	eventType string,
) (bookingCommunicationContent, error) {
	customerName := strings.TrimSpace(schedule.RequesterName)
	if customerName == "" {
		customerName = "Customer"
	}
	reference := bookingReference(schedule.ID)
	amountLabel := quotedAmountLabel(financial)
	paymentLabel := bookingPaymentStatusLabel(financial, schedule.Status)
	title := strings.TrimSpace(schedule.Title)
	if title == "" {
		title = bookingProductLabel(schedule.Activity, schedule.Quantity)
	}

	content := bookingCommunicationContent{}
	switch eventType {
	case bookingCommEventRequestReceived:
		content.Subject = "Mekmaa booking request received - " + reference
		content.Heading = "Your booking request is pending review"
		content.Intro = "Mekmaa has received your booking request. The requested slot is still pending and has not been confirmed for payment."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Pending"},
			{Label: "Booking title", Value: title},
			{Label: "Customer", Value: customerName},
			{Label: "Date", Value: formatCalendarDate(schedule.SlotDate)},
			{Label: "Time", Value: formatClockTime(schedule.SlotHour)},
			{Label: "Activity", Value: bookingProductLabel(schedule.Activity, schedule.Quantity)},
			{Label: "Quoted amount", Value: amountLabel},
		}
		if strings.TrimSpace(schedule.Notes) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Customer notes", Value: schedule.Notes})
		}
		if referral != nil {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Referral code", Value: referral.PartnerCode})
		}
		content.Notes = []string{
			"No payment has been confirmed at this stage.",
			"We will contact you once the request has been reviewed.",
			"Mekmaa contact: " + a.bookingMessages.ContactPhone + " • " + a.bookingMessages.ContactEmail,
		}
	case bookingCommEventConfirmed:
		content.Subject = "Mekmaa booking confirmed - " + reference
		content.Heading = "Your booking is confirmed"
		content.Intro = "Your booking has been confirmed by the Mekmaa team."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Confirmed"},
			{Label: "Customer", Value: customerName},
			{Label: "Date", Value: formatCalendarDate(schedule.SlotDate)},
			{Label: "Time", Value: formatClockTime(schedule.SlotHour)},
			{Label: "Activity", Value: bookingProductLabel(schedule.Activity, schedule.Quantity)},
			{Label: "Quoted amount", Value: amountLabel},
			{Label: "Payment status", Value: paymentLabel},
		}
		if strings.TrimSpace(schedule.ReviewNote) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Administrator note", Value: schedule.ReviewNote})
		}
		content.Notes = []string{
			"Venue: " + a.bookingMessages.VenueName,
			"Address: " + a.bookingMessages.VenueAddress,
			"Please arrive 10 to 15 minutes early and keep your booking reference ready.",
			"Need help before arrival? Contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
		content.SMSBody = fmt.Sprintf(
			"Mekmaa confirmed %s. Ref %s. %s, %s. %s. %s.",
			title,
			reference,
			schedule.SlotDate,
			schedule.SlotHour,
			bookingProductLabel(schedule.Activity, schedule.Quantity),
			amountLabel,
		)
	case bookingCommEventRejected:
		content.Subject = "Mekmaa booking request update - " + reference
		content.Heading = "Your booking request was not approved"
		content.Intro = "Your requested booking could not be approved for the selected slot."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Rejected"},
			{Label: "Customer", Value: customerName},
			{Label: "Original requested date", Value: formatCalendarDate(schedule.SlotDate)},
			{Label: "Original requested time", Value: formatClockTime(schedule.SlotHour)},
			{Label: "Activity", Value: bookingProductLabel(schedule.Activity, schedule.Quantity)},
			{Label: "Rejection reason", Value: strings.TrimSpace(schedule.ReviewNote)},
		}
		content.Notes = []string{
			"If you would like help with another booking request, contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
	case bookingCommEventRescheduledPending:
		change, err := latestRelevantBookingRequestChange(changes, "rescheduled")
		if err != nil {
			return bookingCommunicationContent{}, err
		}
		content.Subject = "Mekmaa proposed a new pending booking slot - " + reference
		content.Heading = "Your request has a new proposed slot"
		content.Intro = "The Mekmaa team updated your requested slot, but the booking is still pending and is not confirmed yet."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Pending"},
			{Label: "Previous slot", Value: formatCalendarDate(change.PreviousSlotDate) + " at " + formatClockTime(change.PreviousSlotHour) + " • " + bookingProductLabel(change.PreviousActivity, change.PreviousQuantity)},
			{Label: "New proposed slot", Value: formatCalendarDate(change.NewSlotDate) + " at " + formatClockTime(change.NewSlotHour) + " • " + bookingProductLabel(change.NewActivity, change.NewQuantity)},
			{Label: "Previous quoted amount", Value: money(change.PreviousQuote)},
			{Label: "New quoted amount", Value: money(change.NewQuote)},
			{Label: "Administrator note", Value: strings.TrimSpace(change.ReviewNote)},
		}
		content.Notes = []string{
			"The new slot remains pending until Mekmaa confirms it.",
			"Contact us at " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + " if you need assistance.",
		}
	case bookingCommEventRescheduledConfirmed:
		change, err := latestRelevantBookingRequestChange(changes, "rescheduled_confirmed")
		if err != nil {
			return bookingCommunicationContent{}, err
		}
		content.Subject = "Mekmaa booking confirmed after reschedule - " + reference
		content.Heading = "Your rescheduled booking is confirmed"
		content.Intro = "Mekmaa has confirmed the final slot after rescheduling your request."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Confirmed"},
			{Label: "Previous slot", Value: formatCalendarDate(change.PreviousSlotDate) + " at " + formatClockTime(change.PreviousSlotHour) + " • " + bookingProductLabel(change.PreviousActivity, change.PreviousQuantity)},
			{Label: "Final confirmed slot", Value: formatCalendarDate(change.NewSlotDate) + " at " + formatClockTime(change.NewSlotHour) + " • " + bookingProductLabel(change.NewActivity, change.NewQuantity)},
			{Label: "Final quoted amount", Value: amountLabel},
			{Label: "Payment status", Value: paymentLabel},
		}
		if strings.TrimSpace(change.ReviewNote) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Administrator note", Value: change.ReviewNote})
		}
		content.Notes = []string{
			"Venue: " + a.bookingMessages.VenueName,
			"Address: " + a.bookingMessages.VenueAddress,
			"Please arrive 10 to 15 minutes early and keep your booking reference ready.",
			"Need help before arrival? Contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
		content.SMSBody = fmt.Sprintf(
			"Mekmaa confirmed your rescheduled booking. Ref %s. %s, %s. %s. %s.",
			reference,
			schedule.SlotDate,
			schedule.SlotHour,
			bookingProductLabel(schedule.Activity, schedule.Quantity),
			amountLabel,
		)
	default:
		return bookingCommunicationContent{}, fmt.Errorf("unsupported booking communication event: %s", eventType)
	}

	return content, nil
}

func renderBookingEmailText(content bookingCommunicationContent) string {
	var builder strings.Builder
	builder.WriteString("Mekmaa\n\n")
	builder.WriteString(content.Heading + "\n\n")
	builder.WriteString(content.Intro + "\n\n")
	for _, fact := range content.Facts {
		builder.WriteString(fact.Label + ": " + fact.Value + "\n")
	}
	if len(content.Notes) > 0 {
		builder.WriteString("\n")
		for _, note := range content.Notes {
			builder.WriteString("- " + note + "\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func renderBookingEmailHTML(content bookingCommunicationContent) string {
	var facts strings.Builder
	for _, fact := range content.Facts {
		facts.WriteString("<tr>")
		facts.WriteString("<td style=\"padding:10px 12px;border-bottom:1px solid #e5e7eb;font-weight:600;color:#0f172a;vertical-align:top;width:34%;\">" + template.HTMLEscapeString(fact.Label) + "</td>")
		facts.WriteString("<td style=\"padding:10px 12px;border-bottom:1px solid #e5e7eb;color:#334155;vertical-align:top;\">" + template.HTMLEscapeString(fact.Value) + "</td>")
		facts.WriteString("</tr>")
	}

	var notes strings.Builder
	if len(content.Notes) > 0 {
		notes.WriteString("<ul style=\"padding-left:18px;margin:20px 0 0;color:#334155;\">")
		for _, note := range content.Notes {
			notes.WriteString("<li style=\"margin:0 0 10px;\">" + template.HTMLEscapeString(note) + "</li>")
		}
		notes.WriteString("</ul>")
	}

	return "<!DOCTYPE html><html><body style=\"margin:0;padding:0;background:#f8fafc;font-family:Arial,sans-serif;color:#0f172a;\">" +
		"<table role=\"presentation\" width=\"100%\" cellspacing=\"0\" cellpadding=\"0\" style=\"background:#f8fafc;padding:24px 12px;\"><tr><td align=\"center\">" +
		"<table role=\"presentation\" width=\"100%\" cellspacing=\"0\" cellpadding=\"0\" style=\"max-width:640px;background:#ffffff;border-radius:18px;overflow:hidden;border:1px solid #e2e8f0;\">" +
		"<tr><td style=\"padding:28px 28px 20px;background:#0f172a;color:#f8fafc;\"><div style=\"font-size:12px;font-weight:700;letter-spacing:0.18em;text-transform:uppercase;color:#67e8f9;\">Mekmaa</div><h1 style=\"margin:12px 0 0;font-size:28px;line-height:1.2;\">" + template.HTMLEscapeString(content.Heading) + "</h1></td></tr>" +
		"<tr><td style=\"padding:28px;\"><p style=\"margin:0 0 18px;font-size:15px;line-height:1.7;color:#334155;\">" + template.HTMLEscapeString(content.Intro) + "</p>" +
		"<table role=\"presentation\" width=\"100%\" cellspacing=\"0\" cellpadding=\"0\" style=\"border-collapse:collapse;border:1px solid #e5e7eb;border-radius:14px;overflow:hidden;\">" + facts.String() + "</table>" +
		notes.String() +
		"<p style=\"margin:22px 0 0;font-size:13px;line-height:1.7;color:#64748b;\">This message was sent by Mekmaa regarding your booking record.</p>" +
		"</td></tr></table></td></tr></table></body></html>"
}

func latestRelevantBookingRequestChange(changes []BookingRequestChange, actionType string) (*BookingRequestChange, error) {
	for i := range changes {
		if changes[i].ActionType == actionType {
			return &changes[i], nil
		}
	}
	if len(changes) == 0 {
		return nil, errors.New("booking request change history was not found")
	}
	return nil, fmt.Errorf("booking request change history for %s was not found", actionType)
}

func quotedAmountLabel(financial *BookingFinancial) string {
	if financial == nil {
		return "Unquoted"
	}
	return money(financial.QuotedAmount)
}

func bookingPaymentStatusLabel(financial *BookingFinancial, scheduleStatus string) string {
	switch {
	case financial == nil:
		return "No finance record"
	case financial.Paid && strings.TrimSpace(financial.PaymentMethod) != "":
		return "Paid via " + financial.PaymentMethod
	case financial.Paid:
		return "Paid"
	case scheduleStatus == "confirmed":
		return "Unpaid"
	default:
		return "No payment confirmed"
	}
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func communicationDelivered(communications []BookingCommunication, channel string) bool {
	for _, communication := range communications {
		if communication.Channel == channel && communication.Status == bookingCommStatusSent {
			return true
		}
	}
	return false
}

func communicationFailed(communications []BookingCommunication) bool {
	for _, communication := range communications {
		if communication.Status == bookingCommStatusFailed {
			return true
		}
	}
	return false
}

func communicationFlashMessage(base string, communications []BookingCommunication) string {
	emailSent := communicationDelivered(communications, bookingCommChannelEmail)
	smsSent := communicationDelivered(communications, bookingCommChannelSMS)
	switch {
	case emailSent && smsSent:
		return base + " Email and SMS were sent."
	case emailSent:
		if communicationFailed(communications) {
			return base + " Email was sent, but another communication channel failed."
		}
		return base + " Email was sent."
	case smsSent:
		return base + " SMS was sent, but email failed or is not configured."
	case communicationFailed(communications):
		return base + " Customer communication failed or is not configured."
	default:
		return base
	}
}

func latestResendableEventType(schedule *SpaceSchedule, communications []BookingCommunication) string {
	if schedule == nil {
		return ""
	}
	switch schedule.Status {
	case "confirmed":
		for _, communication := range communications {
			if communication.EventType == bookingCommEventRescheduledConfirmed ||
				(communication.EventType == bookingCommEventResent && communication.RelatedEventType == bookingCommEventRescheduledConfirmed) {
				return bookingCommEventRescheduledConfirmed
			}
		}
		return bookingCommEventConfirmed
	case "rejected":
		return bookingCommEventRejected
	case "pending":
		for _, communication := range communications {
			if communication.EventType == bookingCommEventRescheduledPending ||
				(communication.EventType == bookingCommEventResent && communication.RelatedEventType == bookingCommEventRescheduledPending) {
				return bookingCommEventRescheduledPending
			}
		}
		return bookingCommEventRequestReceived
	default:
		return ""
	}
}

func adminBookingCommunicationRedirect(scheduleID int64, status string, slotDate string) string {
	if status == "pending" {
		return "/admin/booking-requests"
	}
	return fmt.Sprintf("/admin/bookings?action=view&id=%d&date=%s#schedule-view", scheduleID, url.QueryEscape(slotDate))
}

func currentUserID(r *http.Request) int64 {
	user, _ := currentUserFromRequest(r)
	if user == nil {
		return 0
	}
	return user.ID
}

func currentUserFromRequest(r *http.Request) (*User, bool) {
	if r == nil {
		return nil, false
	}
	user, ok := r.Context().Value(userContextKey).(*User)
	return user, ok
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

func containsPrivilegedRole(roles []string) bool {
	for _, role := range roles {
		if isPrivilegedRole(role) {
			return true
		}
	}
	return false
}

func normalizeRoleNames(roles []string) []string {
	seen := map[string]struct{}{}
	var normalized []string
	for _, role := range roles {
		role = normalizeRoleName(role)
		if role == "" {
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

func (a *App) normalizeExistingRoles(roles []string) ([]string, error) {
	normalized := normalizeRoleNames(roles)
	if len(normalized) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
	args := make([]any, len(normalized))
	for i, role := range normalized {
		args[i] = role
	}
	rows, err := a.db.Query(`SELECT name FROM roles WHERE name IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	existing := make(map[string]struct{}, len(normalized))
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		existing[role] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(existing) != len(normalized) {
		return nil, errors.New("unknown role")
	}
	return normalized, nil
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

func containsSensitivePermission(permissions []string) bool {
	return containsPermission(permissions, "users.manage") || containsPermission(permissions, "roles.manage")
}

func normalizePositiveIDs(values []string) []int64 {
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

func sportTemplateNameBySlug(slug string) (string, bool) {
	switch slug {
	case "cricket":
		return "sports-cricket", true
	case "futsal":
		return "sports-futsal", true
	case "badminton":
		return "sports-badminton", true
	case "table-tennis":
		return "sports-table-tennis", true
	case "tennis":
		return "sports-tennis", true
	default:
		return "", false
	}
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
	if optionValue := strings.TrimSpace(r.FormValue("booking_option")); optionValue != "" {
		parts := strings.SplitN(optionValue, ":", 2)
		if len(parts) == 2 {
			activity = strings.ToLower(strings.TrimSpace(parts[0]))
			if parsedQuantity, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && parsedQuantity > 0 {
				quantity = parsedQuantity
			}
		}
	}
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

func (a *App) eventFromRequest(r *http.Request) (Event, error) {
	imagePath, err := a.uploadedEventImagePath(r)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Title:                strings.TrimSpace(r.FormValue("title")),
		Category:             strings.TrimSpace(r.FormValue("category")),
		EventDate:            strings.TrimSpace(r.FormValue("event_date")),
		StartTime:            strings.TrimSpace(r.FormValue("start_time")),
		EndTime:              strings.TrimSpace(r.FormValue("end_time")),
		RegistrationDeadline: strings.TrimSpace(r.FormValue("registration_deadline")),
		Venue:                strings.TrimSpace(r.FormValue("venue")),
		Summary:              strings.TrimSpace(r.FormValue("summary")),
		ImagePath:            imagePath,
		CTALabel:             strings.TrimSpace(r.FormValue("cta_label")),
		CTALink:              strings.TrimSpace(r.FormValue("cta_link")),
		Published:            r.FormValue("published") == "true",
	}, nil
}

func prefillPublicBookingDraft(r *http.Request, viewer *User, calendarDate string) *SpaceSchedule {
	draft := &SpaceSchedule{
		EntryType:    "booking",
		SlotDate:     calendarDate,
		SlotHour:     strings.TrimSpace(r.URL.Query().Get("hour")),
		Activity:     strings.ToLower(strings.TrimSpace(r.URL.Query().Get("activity"))),
		Quantity:     1,
		ReferralCode: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("ref"))),
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

func prefillAdminBookingDraft(r *http.Request, calendarDate string) *SpaceSchedule {
	draft := &SpaceSchedule{
		EntryType: "booking",
		SlotDate:  calendarDate,
		SlotHour:  strings.TrimSpace(r.URL.Query().Get("hour")),
		Activity:  strings.ToLower(strings.TrimSpace(r.URL.Query().Get("activity"))),
		Quantity:  1,
		Title:     strings.TrimSpace(r.URL.Query().Get("title")),
		Notes:     strings.TrimSpace(r.URL.Query().Get("notes")),
	}
	applyAdminBookingQueryDraft(r, draft)
	if entryType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("entry_type"))); entryType == "booking" || entryType == "training" {
		draft.EntryType = entryType
	}
	if quantity, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("quantity"))); err == nil && quantity > 0 {
		draft.Quantity = quantity
	}
	if draft.EntryType == "training" {
		draft.Activity = "training"
		draft.Quantity = 1
	}
	if draft.Activity == "" {
		if draft.EntryType == "training" {
			draft.Activity = "training"
		} else {
			draft.Activity = "full_indoor_cricket"
		}
	}
	return draft
}

func applyAdminBookingQueryDraft(r *http.Request, schedule *SpaceSchedule) {
	if schedule == nil {
		return
	}

	query := r.URL.Query()

	if slotDate := strings.TrimSpace(query.Get("slot_date")); slotDate != "" {
		schedule.SlotDate = slotDate
	}
	if slotHour := strings.TrimSpace(query.Get("slot_hour")); slotHour != "" {
		schedule.SlotHour = slotHour
	}
	if entryType := strings.ToLower(strings.TrimSpace(query.Get("entry_type"))); entryType == "booking" || entryType == "training" {
		schedule.EntryType = entryType
	}
	if title := strings.TrimSpace(query.Get("title")); title != "" {
		schedule.Title = title
	}
	if notes := strings.TrimSpace(query.Get("notes")); notes != "" {
		schedule.Notes = notes
	}

	optionValue := strings.TrimSpace(query.Get("booking_option"))
	if optionValue == "" {
		activity := strings.ToLower(strings.TrimSpace(query.Get("activity")))
		if activity != "" {
			schedule.Activity = activity
		}
		if quantity, err := strconv.Atoi(strings.TrimSpace(query.Get("quantity"))); err == nil && quantity > 0 {
			schedule.Quantity = quantity
		}
	} else {
		parts := strings.SplitN(optionValue, ":", 2)
		if len(parts) == 2 {
			schedule.Activity = strings.ToLower(strings.TrimSpace(parts[0]))
			if quantity, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && quantity > 0 {
				schedule.Quantity = quantity
			}
		}
	}

	if schedule.EntryType == "training" {
		schedule.Activity = "training"
		schedule.Quantity = 1
	}
}

func studentGroupFromRequest(r *http.Request) StudentGroup {
	return StudentGroup{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Code:        strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
}

func validateAdmission(admission Admission) error {
	if admission.TrainingProgramID <= 0 {
		return errors.New("training programme is required")
	}

	if strings.TrimSpace(admission.TrainingProgramName) == "" {
		return errors.New("training programme name is required")
	}
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

	for _, option := range defaultBookingOptionCatalog() {
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

func validateEvent(event Event) error {
	switch {
	case event.Title == "":
		return errors.New("title is required")
	case event.Category == "":
		return errors.New("category is required")
	case event.Venue == "":
		return errors.New("venue is required")
	case event.Summary == "":
		return errors.New("summary is required")
	}

	eventDate, err := time.Parse("2006-01-02", event.EventDate)
	if err != nil {
		return errors.New("valid event date is required")
	}
	if eventDate.Year() < 2000 {
		return errors.New("valid event date is required")
	}
	if event.StartTime != "" {
		if _, err := time.Parse("15:04", event.StartTime); err != nil {
			return errors.New("valid start time is required")
		}
	}
	if event.EndTime != "" {
		endTime, err := time.Parse("15:04", event.EndTime)
		if err != nil {
			return errors.New("valid end time is required")
		}
		if event.StartTime == "" {
			return errors.New("start time is required when end time is provided")
		}
		startTime, _ := time.Parse("15:04", event.StartTime)
		if !startTime.Before(endTime) {
			return errors.New("end time must be after start time")
		}
	}
	if event.RegistrationDeadline != "" {
		deadline, err := time.Parse("2006-01-02", event.RegistrationDeadline)
		if err != nil {
			return errors.New("valid registration before date is required")
		}
		if deadline.After(eventDate) {
			return errors.New("registration before date cannot be after the event date")
		}
	}
	if (event.CTALabel == "") != (event.CTALink == "") {
		return errors.New("cta label and cta link must both be provided")
	}
	return nil
}
func courtClosureFromRequest(
	r *http.Request,
) (CourtClosure, error) {
	courtID, err := strconv.ParseInt(
		strings.TrimSpace(
			r.FormValue("court_id"),
		),
		10,
		64,
	)
	if err != nil || courtID <= 0 {
		return CourtClosure{},
			errors.New("valid court is required")
	}

	closure := CourtClosure{
		CourtID: courtID,
		ClosureDate: strings.TrimSpace(
			r.FormValue("closure_date"),
		),
		StartHour: strings.TrimSpace(
			r.FormValue("start_hour"),
		),
		EndHour: strings.TrimSpace(
			r.FormValue("end_hour"),
		),
		Activity: strings.TrimSpace(
			r.FormValue("activity"),
		),
		Title: strings.TrimSpace(
			r.FormValue("title"),
		),
		Reason: strings.TrimSpace(
			r.FormValue("reason"),
		),
		Active: r.FormValue("active") == "1",
	}

	return closure, nil
}

func courtLayoutFromRequest(
	r *http.Request,
) (CourtLayout, error) {
	courtID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("court_id")),
		10,
		64,
	)
	if err != nil || courtID <= 0 {
		return CourtLayout{}, errors.New("valid court is required")
	}

	sortOrder, err := strconv.Atoi(
		strings.TrimSpace(r.FormValue("sort_order")),
	)
	if err != nil {
		return CourtLayout{}, errors.New("valid sort order is required")
	}

	layout := CourtLayout{
		CourtID:     courtID,
		Name:        strings.TrimSpace(r.FormValue("name")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Active:      r.FormValue("active") == "1",
		SortOrder:   sortOrder,
	}

	for _, activity := range r.Form["activity"] {
		activity = strings.TrimSpace(activity)
		if activity == "" {
			continue
		}

		quantityValue := strings.TrimSpace(
			r.FormValue("quantity_" + activity),
		)

		quantity, err := strconv.Atoi(quantityValue)
		if err != nil || quantity <= 0 {
			continue
		}

		layout.Items = append(
			layout.Items,
			CourtLayoutItem{
				Activity: activity,
				Quantity: quantity,
			},
		)
	}

	return layout, nil
}

func courtClosureCoversSlot(
	closure CourtClosure,
	slotDate string,
	slotHour string,
) bool {
	if !closure.Active {
		return false
	}

	if closure.ClosureDate != slotDate {
		return false
	}

	slotHour = strings.TrimSpace(slotHour)

	return slotHour >= closure.StartHour &&
		slotHour < closure.EndHour
}

func closureBlocksActivity(
	closure CourtClosure,
	activity string,
) bool {
	if strings.TrimSpace(closure.Activity) == "" {
		return true
	}

	return closure.Activity ==
		strings.TrimSpace(activity)
}

func validateScheduleAgainstClosures(
	schedule SpaceSchedule,
	closures []CourtClosure,
) error {
	for _, closure := range closures {
		if !courtClosureCoversSlot(
			closure,
			schedule.SlotDate,
			schedule.SlotHour,
		) {
			continue
		}

		if !closureBlocksActivity(
			closure,
			schedule.Activity,
		) {
			continue
		}

		if strings.TrimSpace(
			closure.Activity,
		) == "" {
			return fmt.Errorf(
				"the court is unavailable at this time: %s",
				closure.Title,
			)
		}

		return fmt.Errorf(
			"%s is unavailable at this time: %s",
			schedule.Activity,
			closure.Title,
		)
	}

	return nil
}

func filterBookingOptionsForClosures(
	options []BookingOption,
	slotDate string,
	slotHour string,
	closures []CourtClosure,
) (
	[]BookingOption,
	string,
) {
	for _, closure := range closures {
		if !courtClosureCoversSlot(
			closure,
			slotDate,
			slotHour,
		) {
			continue
		}

		if strings.TrimSpace(
			closure.Activity,
		) == "" {
			return nil, closure.Title
		}
	}

	filtered := make(
		[]BookingOption,
		0,
		len(options),
	)

	for _, option := range options {
		blocked := false

		for _, closure := range closures {
			if !courtClosureCoversSlot(
				closure,
				slotDate,
				slotHour,
			) {
				continue
			}

			if closure.Activity ==
				option.Activity {
				blocked = true
				break
			}
		}

		if !blocked {
			filtered = append(
				filtered,
				option,
			)
		}
	}

	return filtered, ""
}

func validateCourtClosure(
	closure CourtClosure,
	activities []CourtActivity,
) error {
	closure.ClosureDate = strings.TrimSpace(
		closure.ClosureDate,
	)
	closure.StartHour = strings.TrimSpace(
		closure.StartHour,
	)
	closure.EndHour = strings.TrimSpace(
		closure.EndHour,
	)
	closure.Activity = strings.TrimSpace(
		closure.Activity,
	)
	closure.Title = strings.TrimSpace(
		closure.Title,
	)
	closure.Reason = strings.TrimSpace(
		closure.Reason,
	)

	if closure.CourtID <= 0 {
		return errors.New("court is required")
	}

	if closure.ClosureDate == "" {
		return errors.New("closure date is required")
	}

	if _, err := time.Parse(
		"2006-01-02",
		closure.ClosureDate,
	); err != nil {
		return errors.New("valid closure date is required")
	}

	start, err := time.Parse(
		"15:04",
		closure.StartHour,
	)
	if err != nil {
		return errors.New("valid start hour is required")
	}

	end, err := time.Parse(
		"15:04",
		closure.EndHour,
	)
	if err != nil {
		return errors.New("valid end hour is required")
	}

	if !start.Before(end) {
		return errors.New(
			"closure end hour must be after the start hour",
		)
	}

	if closure.Title == "" {
		return errors.New("closure title is required")
	}

	if closure.Activity != "" {
		validActivity := false

		for _, activity := range activities {
			if activity.Active &&
				activity.Activity == closure.Activity {
				validActivity = true
				break
			}
		}

		if !validActivity {
			return errors.New(
				"selected closure activity is not available for this court",
			)
		}
	}

	return nil
}

func validateCourtLayout(
	layout CourtLayout,
	activities []CourtActivity,
) error {
	layout.Name = strings.TrimSpace(layout.Name)
	layout.Description = strings.TrimSpace(layout.Description)

	if layout.CourtID <= 0 {
		return errors.New("court is required")
	}

	if layout.Name == "" {
		return errors.New("layout name is required")
	}

	if len(layout.Items) == 0 {
		return errors.New("at least one court activity is required")
	}

	allowedActivities := make(map[string]CourtActivity)

	for _, activity := range activities {
		if !activity.Active {
			continue
		}

		allowedActivities[activity.Activity] = activity
	}

	seen := make(map[string]bool)

	for _, item := range layout.Items {
		item.Activity = strings.TrimSpace(item.Activity)

		if item.Activity == "" {
			return errors.New("layout activity is required")
		}

		if seen[item.Activity] {
			return errors.New("an activity cannot appear twice in the same layout")
		}

		activity, exists := allowedActivities[item.Activity]
		if !exists {
			return fmt.Errorf(
				"%s is not an active activity for this court",
				item.Activity,
			)
		}

		if item.Quantity <= 0 {
			return fmt.Errorf(
				"%s quantity must be at least 1",
				activity.DisplayName,
			)
		}

		if item.Quantity > activity.MaxQuantity {
			return fmt.Errorf(
				"%s quantity cannot exceed %d",
				activity.DisplayName,
				activity.MaxQuantity,
			)
		}

		seen[item.Activity] = true
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
	if schedule.Activity == "" {
		return errors.New("activity is required")
	}
	if schedule.Activity == "training" {
		if schedule.EntryType != "training" {
			return errors.New("training activity must use training entry type")
		}
		if schedule.Quantity != 1 {
			return errors.New("training quantity must be 1")
		}
		return nil
	}
	if schedule.EntryType != "booking" {
		return errors.New("booking activity must use direct booking entry type")
	}
	if schedule.Quantity <= 0 {
		return errors.New("quantity must be at least 1")
	}
	return nil
}

func validateReferralPartner(partner ReferralPartner) error {
	switch {
	case partner.Name == "":
		return errors.New("referral partner name is required")
	case !referralCodePattern.MatchString(partner.Code):
		return errors.New("referral code must be 3 to 24 letters, numbers, dashes or underscores")
	case partner.Phone == "":
		return errors.New("referral partner phone is required")
	case partner.Email != "" && !emailPattern.MatchString(partner.Email):
		return errors.New("a valid referral partner email is required")
	default:
		return nil
	}
}

func validateBookableScheduleTime(schedule SpaceSchedule, now time.Time) error {
	slotTime, err := time.ParseInLocation("2006-01-02 15:04", schedule.SlotDate+" "+schedule.SlotHour, time.Local)
	if err != nil {
		return errors.New("valid booking date and time are required")
	}
	if !slotTime.After(now.In(time.Local)) {
		return errors.New("the selected booking time has already started")
	}
	return nil
}

func validateSpaceScheduleSlotAgainstLayouts(
	existing []SpaceSchedule,
	candidate SpaceSchedule,
	layouts []CourtLayout,
) error {
	if len(layouts) == 0 {
		return errors.New("no active court configurations are available")
	}

	usage := make(map[string]int)

	for _, schedule := range existing {
		if !scheduleConsumesCourtCapacity(schedule) {
			continue
		}

		usage[schedule.Activity] += schedule.Quantity
	}

	if scheduleConsumesCourtCapacity(candidate) {
		usage[candidate.Activity] += candidate.Quantity
	}

	candidateUsage := make(map[string]int)
	if scheduleConsumesCourtCapacity(candidate) {
		candidateUsage[candidate.Activity] = candidate.Quantity
	}

	candidateFitsAnyLayout := len(candidateUsage) == 0

	for _, layout := range layouts {
		if !layout.Active {
			continue
		}

		if !candidateFitsAnyLayout &&
			courtLayoutSupportsUsage(layout, candidateUsage) {
			candidateFitsAnyLayout = true
		}

		if courtLayoutSupportsUsage(layout, usage) {
			return nil
		}
	}

	if !candidateFitsAnyLayout {
		return errors.New("no active court layout supports the selected booking combination")
	}

	return errors.New("another booking already consumed the remaining capacity for that slot")
}

func scheduleConsumesCourtCapacity(schedule SpaceSchedule) bool {
	switch strings.ToLower(strings.TrimSpace(schedule.Status)) {
	case "rejected", "cancelled", "expired":
		return false
	default:
		return true
	}
}

func courtLayoutSupportsUsage(
	layout CourtLayout,
	usage map[string]int,
) bool {
	if len(layout.Items) == 0 {
		return false
	}

	capacity := make(map[string]int)

	for _, item := range layout.Items {
		if item.Quantity <= 0 {
			continue
		}

		capacity[item.Activity] += item.Quantity
	}

	for activity, usedQuantity := range usage {
		if usedQuantity <= 0 {
			continue
		}

		if capacity[activity] < usedQuantity {
			return false
		}
	}

	return true
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
	if fullIndoorCricket == 0 && futsal == 0 && tennis == 0 {
		if badminton == 0 && tableTennis == 0 && cricketNets >= 1 && cricketNets <= 3 {
			return nil
		}
		if badminton == 0 && cricketNets == 0 && tableTennis >= 1 && tableTennis <= 2 {
			return nil
		}
		if badminton == 1 && tableTennis == 0 && cricketNets >= 0 && cricketNets <= 1 {
			return nil
		}
		if badminton == 1 && cricketNets == 0 && tableTennis >= 0 && tableTennis <= 1 {
			return nil
		}
	}
	if tennis == 1 && fullIndoorCricket == 0 && futsal == 0 && badminton == 0 && tableTennis == 0 && cricketNets == 0 {
		return nil
	}

	return errors.New("that slot combination is not allowed")
}

func defaultBookingOptionCatalog() []BookingOption {
	return []BookingOption{
		{
			Activity: "full_indoor_cricket",
			Quantity: 1,
			Label:    "Full Indoor Cricket",
		},
		{
			Activity: "futsal",
			Quantity: 1,
			Label:    "Futsal",
		},
		{
			Activity: "badminton",
			Quantity: 1,
			Label:    "Badminton",
		},
		{
			Activity: "table_tennis",
			Quantity: 1,
			Label:    "Table Tennis ×1",
		},
		{
			Activity: "table_tennis",
			Quantity: 2,
			Label:    "Table Tennis ×2",
		},
		{
			Activity: "cricket_net",
			Quantity: 1,
			Label:    "Cricket Net ×1",
		},
		{
			Activity: "cricket_net",
			Quantity: 2,
			Label:    "Cricket Nets ×2",
		},
		{
			Activity: "cricket_net",
			Quantity: 3,
			Label:    "Cricket Nets ×3",
		},
		{
			Activity: "tennis",
			Quantity: 1,
			Label:    "Tennis",
		},
	}
}

func bookingOptionCatalog(
	activities []CourtActivity,
	layouts []CourtLayout,
) []BookingOption {

	maxLayoutCapacity := make(map[string]int)

	for _, layout := range layouts {
		if !layout.Active {
			continue
		}

		for _, item := range layout.Items {
			if item.Quantity > maxLayoutCapacity[item.Activity] {
				maxLayoutCapacity[item.Activity] = item.Quantity
			}
		}
	}

	var options []BookingOption

	for _, activity := range activities {
		if !activity.Active {
			continue
		}

		maxQuantity := maxLayoutCapacity[activity.Activity]
		if maxQuantity <= 0 {
			continue
		}

		if activity.MaxQuantity > 0 &&
			maxQuantity > activity.MaxQuantity {
			maxQuantity = activity.MaxQuantity
		}

		for quantity := 1; quantity <= maxQuantity; quantity++ {
			label := activity.DisplayName

			if maxQuantity > 1 || quantity > 1 {
				label = fmt.Sprintf(
					"%s ×%d",
					activity.DisplayName,
					quantity,
				)
			}

			options = append(
				options,
				BookingOption{
					Activity: activity.Activity,
					Quantity: quantity,
					Label:    label,
				},
			)
		}
	}

	return options
}

func bookingOptionExists(
	activity string,
	quantity int,
	activities []CourtActivity,
	layouts []CourtLayout,
) bool {
	activity = strings.TrimSpace(activity)

	if activity == "" || quantity <= 0 {
		return false
	}

	for _, option := range bookingOptionCatalog(
		activities,
		layouts,
	) {
		if option.Activity == activity &&
			option.Quantity == quantity {
			return true
		}
	}

	return false
}

func validateConfiguredBookingOption(
	schedule SpaceSchedule,
	activities []CourtActivity,
	layouts []CourtLayout,
) error {
	if schedule.EntryType == "training" ||
		schedule.Activity == "training" {
		return nil
	}

	if !bookingOptionExists(
		schedule.Activity,
		schedule.Quantity,
		activities,
		layouts,
	) {
		return errors.New(
			"the selected booking option is no longer available",
		)
	}

	return nil
}

func buildBookingSlotAvailability(
	schedules []SpaceSchedule,
	slotDate string,
	hours []string,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
) []BookingSlotAvailability {
	var availability []BookingSlotAvailability
	now := time.Now()

	options := bookingOptionCatalog(
		activities,
		layouts,
	)

	for _, hour := range hours {
		existing := schedulesForCalendarSlot(
			schedules,
			slotDate,
			hour,
		)

		slot := BookingSlotAvailability{
			Hour:      hour,
			Schedules: existing,
		}

		if validateBookableScheduleTime(
			SpaceSchedule{
				SlotDate: slotDate,
				SlotHour: hour,
			},
			now,
		) != nil {
			slot.IsPast = true
			slot.BlockedReason =
				"This time has already started"

			availability = append(
				availability,
				slot,
			)

			continue
		}

		for _, option := range options {
			candidate := SpaceSchedule{
				EntryType: "booking",
				Activity:  option.Activity,
				Quantity:  option.Quantity,
				SlotDate:  slotDate,
				SlotHour:  hour,
				Status:    "pending",
			}

			if err := validateSpaceScheduleSlotAgainstLayouts(
				existing,
				candidate,
				layouts,
			); err == nil {
				slot.Options = append(
					slot.Options,
					option,
				)
			}
		}

		var closureReason string

		slot.Options, closureReason =
			filterBookingOptionsForClosures(
				slot.Options,
				slotDate,
				hour,
				closures,
			)

		if closureReason != "" {
			slot.BlockedReason =
				"Unavailable: " + closureReason
		}

		if len(slot.Options) == 0 &&
			slot.BlockedReason == "" {
			slot.BlockedReason =
				"No bookable combinations available"
		}

		availability = append(
			availability,
			slot,
		)
	}

	return availability
}

func buildBookingWeekDays(
	schedules []SpaceSchedule,
	selectedDate time.Time,
	hours []string,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
) []CalendarDay {
	start := selectedDate.AddDate(0, 0, -3)

	todayDate := time.Now()
	today := todayDate.Format("2006-01-02")

	if selectedDate.Format("2006-01-02") >= today &&
		start.Format("2006-01-02") < today {
		start = todayDate
	}

	days := make([]CalendarDay, 0, 7)

	for offset := 0; offset < 7; offset++ {
		day := start.AddDate(0, 0, offset)
		date := day.Format("2006-01-02")

		availability := buildBookingSlotAvailability(
			schedules,
			date,
			hours,
			activities,
			layouts,
			closures,
		)

		openCount := 0
		busyCount := 0

		for _, slot := range availability {
			if len(slot.Options) > 0 {
				openCount++
			} else {
				busyCount++
			}
		}

		days = append(
			days,
			CalendarDay{
				Date:          date,
				DayLabel:      day.Format("Mon"),
				MonthLabel:    day.Format("Jan"),
				DayNumber:     day.Format("02"),
				OpenSlotCount: openCount,
				BusySlotCount: busyCount,
				IsToday:       date == today,
				IsSelected: date ==
					selectedDate.Format("2006-01-02"),
				IsPast: date < today,
			},
		)
	}

	return days
}

func bookingCalendarWindow(selectedDate time.Time) (time.Time, time.Time) {
	start := selectedDate.AddDate(0, 0, -3)
	today := time.Now()
	if selectedDate.Format("2006-01-02") >= today.Format("2006-01-02") &&
		start.Format("2006-01-02") < today.Format("2006-01-02") {
		start = today
	}
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	end := start.AddDate(0, 0, 6)
	return start, end
}

func filterPricedBookingSlots(slots []BookingSlotAvailability, slotDate string, pricings []PricingRule, settings *PricingSettings) []BookingSlotAvailability {
	filtered := make([]BookingSlotAvailability, len(slots))
	for i, slot := range slots {
		filtered[i] = slot
		filtered[i].Options = nil
		for _, option := range slot.Options {
			rule := pricingRuleForOption(pricings, option.Activity, option.Quantity)
			if rule != nil && priceForRuleSlot(*rule, settings, slotDate, slot.Hour) > 0 {
				filtered[i].Options = append(filtered[i].Options, option)
			}
		}
		if len(slot.Options) > 0 && len(filtered[i].Options) == 0 {
			filtered[i].BlockedReason = "Online pricing is not configured"
		}
	}
	return filtered
}

func buildAdminBookingOptions(
	schedules []SpaceSchedule,
	schedule *SpaceSchedule,
	excludeID int64,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
	pricings []PricingRule,
	settings *PricingSettings,
) ([]AdminBookingOption, string, error) {
	if schedule == nil {
		return nil, "", nil
	}
	if schedule.SlotDate == "" || schedule.SlotHour == "" {
		return nil, "Choose a date and hour to review valid booking options.", nil
	}

	if _, err := time.Parse("2006-01-02", schedule.SlotDate); err != nil {
		return nil, "Choose a valid booking date to review valid options.", nil
	}
	if _, err := time.Parse("15:04", schedule.SlotHour); err != nil {
		return nil, "Choose a valid booking hour to review valid options.", nil
	}

	slotSchedules := make([]SpaceSchedule, 0)
	for _, existing := range schedules {
		if existing.ID == excludeID {
			continue
		}
		if existing.SlotDate == schedule.SlotDate &&
			existing.SlotHour == schedule.SlotHour &&
			scheduleConsumesCourtCapacity(existing) {
			slotSchedules = append(slotSchedules, existing)
		}
	}

	if err := validateBookableScheduleTime(
		SpaceSchedule{
			SlotDate: schedule.SlotDate,
			SlotHour: schedule.SlotHour,
		},
		time.Now(),
	); err != nil {
		return nil, err.Error(), nil
	}

	if schedule.EntryType == "training" {
		candidate := SpaceSchedule{
			EntryType: "training",
			Activity:  "training",
			Quantity:  1,
			SlotDate:  schedule.SlotDate,
			SlotHour:  schedule.SlotHour,
			Status:    "pending",
		}
		if err := validateScheduleAgainstClosures(candidate, closures); err != nil {
			return nil, err.Error(), nil
		}
		if err := validateSpaceScheduleSlotAgainstLayouts(slotSchedules, candidate, layouts); err != nil {
			return nil, err.Error(), nil
		}
		return []AdminBookingOption{
			{
				Activity:          "training",
				Quantity:          1,
				Label:             "Training Session",
				PriceLabel:        "Internal",
				AvailabilityState: "Available",
			},
		}, "", nil
	}

	options := bookingOptionCatalog(activities, layouts)
	adminOptions := make([]AdminBookingOption, 0, len(options))

	for _, option := range options {
		candidate := SpaceSchedule{
			EntryType: "booking",
			Activity:  option.Activity,
			Quantity:  option.Quantity,
			SlotDate:  schedule.SlotDate,
			SlotHour:  schedule.SlotHour,
			Status:    "pending",
		}

		if err := validateScheduleAgainstClosures(candidate, closures); err != nil {
			continue
		}
		if err := validateSpaceScheduleSlotAgainstLayouts(slotSchedules, candidate, layouts); err != nil {
			continue
		}

		rule := pricingRuleForOption(pricings, option.Activity, option.Quantity)
		if rule == nil {
			continue
		}
		price := priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour)
		if price <= 0 {
			continue
		}

		maxQuantity := maxAvailableQuantityForActivity(
			slotSchedules,
			schedule.SlotDate,
			schedule.SlotHour,
			option.Activity,
			activities,
			layouts,
			closures,
		)
		remainingCapacity := maxQuantity - option.Quantity

		adminOption := AdminBookingOption{
			Activity:          option.Activity,
			Quantity:          option.Quantity,
			Label:             option.Label,
			PriceLabel:        money(price),
			AvailabilityState: "Available",
			RemainingCapacity: remainingCapacity,
		}
		if remainingCapacity > 0 {
			adminOption.RemainingCapacityLabel = fmt.Sprintf("%d more can still fit in this hour", remainingCapacity)
		}

		adminOptions = append(adminOptions, adminOption)
	}

	if len(adminOptions) == 0 {
		blockedReason := "No valid booking options remain for this slot."
		for _, closure := range closures {
			if courtClosureCoversSlot(closure, schedule.SlotDate, schedule.SlotHour) && strings.TrimSpace(closure.Activity) == "" {
				blockedReason = fmt.Sprintf("The court is unavailable at this time: %s", closure.Title)
				break
			}
		}
		return nil, blockedReason, nil
	}

	return adminOptions, "", nil
}

func buildAdminCalendarHours(
	slotDate string,
	hours []string,
	daySchedules []SpaceSchedule,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
	pricings []PricingRule,
	settings *PricingSettings,
	financials []BookingFinancial,
	referrals []BookingReferral,
	changes []BookingRequestChange,
) []AdminCalendarHour {
	hoursView := make([]AdminCalendarHour, 0, len(hours))
	for _, hour := range hours {
		slotSchedules := schedulesForCalendarSlot(daySchedules, slotDate, hour)
		slotClosures := closuresForSlot(closures, slotDate, hour)

		bookingDraft := &SpaceSchedule{
			EntryType: "booking",
			SlotDate:  slotDate,
			SlotHour:  hour,
		}
		bookingOptions, blockedReason, _ := buildAdminBookingOptions(
			daySchedules,
			bookingDraft,
			0,
			activities,
			layouts,
			closures,
			pricings,
			settings,
		)

		trainingDraft := &SpaceSchedule{
			EntryType: "training",
			SlotDate:  slotDate,
			SlotHour:  hour,
		}
		trainingOptions, _, _ := buildAdminBookingOptions(
			daySchedules,
			trainingDraft,
			0,
			activities,
			layouts,
			closures,
			pricings,
			settings,
		)

		row := AdminCalendarHour{
			Hour:              hour,
			BlockedReason:     blockedReason,
			Closures:          slotClosures,
			AvailableOptions:  bookingOptions,
			CanAddDirect:      len(bookingOptions) > 0,
			CanAddTraining:    len(trainingOptions) > 0,
			AddDirectURL:      adminCalendarActionURL(slotDate, hour, "booking", bookingOptions),
			AddTrainingURL:    adminCalendarActionURL(slotDate, hour, "training", trainingOptions),
			ManageClosuresURL: "/admin/courts",
		}

		for _, schedule := range slotSchedules {
			item := buildAdminCalendarItem(schedule, financials, referrals, changes)
			switch {
			case item.IsTraining:
				row.Training = append(row.Training, item)
				row.TrainingCount++
			case item.IsPending:
				row.Pending = append(row.Pending, item)
				row.PendingCount++
			default:
				row.Confirmed = append(row.Confirmed, item)
				row.ConfirmedCount++
			}
			if item.IsUnpaid {
				row.UnpaidCount++
			}
			if financial := bookingFinancialForSchedule(financials, schedule.ID); financial != nil {
				row.ExpectedRevenue += financial.QuotedAmount
				if financial.Paid {
					row.CollectedRevenue += financial.QuotedAmount
				}
			}
		}

		row.IsPast = validateBookableScheduleTime(
			SpaceSchedule{SlotDate: slotDate, SlotHour: hour},
			time.Now(),
		) != nil
		row.CanAddDirect = row.CanAddDirect && !row.IsPast
		row.CanAddTraining = row.CanAddTraining && !row.IsPast
		if !row.CanAddDirect {
			row.AddDirectURL = ""
		}
		if !row.CanAddTraining {
			row.AddTrainingURL = ""
		}

		row.State, row.StateLabel, row.StateClasses = adminCalendarState(row, bookingOptions, trainingOptions, activities, layouts)
		row.RemainingSummary = adminCalendarRemainingSummary(row, bookingOptions, trainingOptions)
		hoursView = append(hoursView, row)
	}
	return hoursView
}

func buildAdminCalendarItem(
	schedule SpaceSchedule,
	financials []BookingFinancial,
	referrals []BookingReferral,
	changes []BookingRequestChange,
) AdminCalendarItem {
	item := AdminCalendarItem{
		ID:             schedule.ID,
		Title:          schedule.Title,
		Summary:        scheduleSummary(schedule),
		Status:         schedule.Status,
		EntryType:      schedule.EntryType,
		RequesterName:  schedule.RequesterName,
		RequesterPhone: schedule.RequesterPhone,
		ReviewNote:     schedule.ReviewNote,
		ViewURL:        fmt.Sprintf("/admin/bookings?action=view&id=%d&date=%s#schedule-view", schedule.ID, url.QueryEscape(schedule.SlotDate)),
		EditURL:        fmt.Sprintf("/admin/bookings?action=edit&id=%d&date=%s#schedule-edit", schedule.ID, url.QueryEscape(schedule.SlotDate)),
		IsPending:      schedule.Status == "pending",
		IsTraining:     schedule.EntryType == "training",
	}
	if schedule.RequesterName != "" || schedule.RequesterEmail != "" || schedule.RequestedByUser > 0 {
		item.Reference = bookingReference(schedule.ID)
	} else {
		item.Reference = fmt.Sprintf("INTERNAL-%06d", schedule.ID)
	}
	if referral := bookingReferralFor(referrals, schedule.ID); referral != nil {
		item.ReferralCode = referral.PartnerCode
	}
	if history := bookingRequestHistoryFor(changes, schedule.ID); len(history) > 0 {
		item.CanReschedule = true
	}
	if item.IsPending && !item.IsTraining && (schedule.RequesterName != "" || schedule.RequesterEmail != "" || schedule.RequestedByUser > 0) {
		item.RequestURL = fmt.Sprintf("/admin/booking-requests?action=reschedule&id=%d", schedule.ID)
		item.CanConfirm = true
		item.CanReschedule = true
	}

	if item.IsTraining {
		item.PriceLabel = "Internal"
		item.PaymentLabel = "No payment"
		item.PaymentTone = "text-slate/55"
		return item
	}

	financial := bookingFinancialForSchedule(financials, schedule.ID)
	if financial == nil {
		item.PriceLabel = "Unquoted"
		item.PaymentLabel = "No finance record"
		item.PaymentTone = "text-slate/55"
		return item
	}

	item.PriceLabel = money(financial.QuotedAmount)
	switch {
	case financial.Paid:
		item.PaymentLabel = "Paid"
		if strings.TrimSpace(financial.PaymentMethod) != "" {
			item.PaymentLabel += " • " + financial.PaymentMethod
		}
		item.PaymentTone = "text-emerald-700"
	case schedule.Status == "confirmed":
		item.PaymentLabel = "Unpaid"
		item.PaymentTone = "text-red-700"
		item.IsUnpaid = true
	default:
		item.PaymentLabel = "Quoted"
		item.PaymentTone = "text-amber-700"
	}
	return item
}

func adminCalendarState(
	row AdminCalendarHour,
	bookingOptions []AdminBookingOption,
	trainingOptions []AdminBookingOption,
	activities []CourtActivity,
	layouts []CourtLayout,
) (string, string, string) {
	if row.IsPast {
		return "past_hour", "Past hour", "border-slate/12 bg-slate-50"
	}
	if len(activities) == 0 || len(layouts) == 0 {
		return "configuration_unavailable", "Configuration unavailable", "border-amber-200 bg-amber-50"
	}
	if len(row.Closures) > 0 {
		if len(bookingOptions) == 0 && len(trainingOptions) == 0 {
			return "fully_closed", "Fully closed", "border-rose-200 bg-rose-50"
		}
		return "partially_closed", "Partially closed", "border-orange-200 bg-orange-50"
	}
	if len(bookingOptions) == 0 && len(trainingOptions) == 0 {
		if strings.Contains(strings.ToLower(row.BlockedReason), "pricing") ||
			strings.Contains(strings.ToLower(row.BlockedReason), "configured") {
			return "configuration_unavailable", "Configuration unavailable", "border-amber-200 bg-amber-50"
		}
		return "fully_occupied", "Fully occupied", "border-red-200 bg-red-50"
	}
	if row.ConfirmedCount > 0 || row.PendingCount > 0 || row.TrainingCount > 0 {
		return "partially_occupied", "Partially occupied", "border-sky-200 bg-sky-50/60"
	}
	return "fully_open", "Fully open", "border-emerald-200 bg-emerald-50"
}

func adminCalendarRemainingSummary(
	row AdminCalendarHour,
	bookingOptions []AdminBookingOption,
	trainingOptions []AdminBookingOption,
) string {
	switch {
	case row.IsPast:
		return "This hour has already started."
	case len(bookingOptions) > 0:
		summary := fmt.Sprintf("%d direct booking option", len(bookingOptions))
		if len(bookingOptions) != 1 {
			summary += "s"
		}
		summary += " remain."
		if len(trainingOptions) > 0 {
			summary += " Internal training can still fit."
		}
		return summary
	case row.BlockedReason != "":
		return row.BlockedReason
	case len(row.Closures) > 0:
		return "Active closures block the remaining combinations."
	default:
		return "No valid booking combinations remain for this hour."
	}
}

func buildAdminCalendarStats(hours []AdminCalendarHour) []Stat {
	openHours := 0
	pending := 0
	training := 0
	unpaid := 0
	expectedRevenue := 0.0
	for _, hour := range hours {
		if hour.CanAddDirect {
			openHours++
		}
		pending += hour.PendingCount
		training += hour.TrainingCount
		unpaid += hour.UnpaidCount
		expectedRevenue += hour.ExpectedRevenue
	}
	return []Stat{
		{Label: "Open booking hours", Value: strconv.Itoa(openHours)},
		{Label: "Pending requests today", Value: strconv.Itoa(pending)},
		{Label: "Internal training today", Value: strconv.Itoa(training)},
		{Label: "Unpaid confirmed bookings", Value: strconv.Itoa(unpaid)},
		{Label: "Quoted value on day", Value: money(expectedRevenue)},
	}
}

func maxAvailableQuantityForActivity(
	existing []SpaceSchedule,
	slotDate string,
	slotHour string,
	activity string,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
) int {
	maxConfiguredQuantity := 0
	for _, option := range bookingOptionCatalog(activities, layouts) {
		if option.Activity == activity && option.Quantity > maxConfiguredQuantity {
			maxConfiguredQuantity = option.Quantity
		}
	}

	maxAvailableQuantity := 0
	for quantity := 1; quantity <= maxConfiguredQuantity; quantity++ {
		candidate := SpaceSchedule{
			EntryType: "booking",
			Activity:  activity,
			Quantity:  quantity,
			SlotDate:  slotDate,
			SlotHour:  slotHour,
			Status:    "pending",
		}
		if validateScheduleAgainstClosures(candidate, closures) != nil {
			continue
		}
		if validateSpaceScheduleSlotAgainstLayouts(existing, candidate, layouts) == nil {
			maxAvailableQuantity = quantity
		}
	}

	return maxAvailableQuantity
}

func (a *App) adminBookingOptionsForSchedule(
	schedule SpaceSchedule,
	excludeID int64,
) ([]AdminBookingOption, string, error) {
	schedules, err := a.listSpaceSchedules()
	if err != nil {
		return nil, "", err
	}
	activities, layouts, err := a.activeBookingConfiguration()
	if err != nil {
		return nil, "", err
	}
	closures, err := a.listActiveCourtClosures()
	if err != nil {
		return nil, "", err
	}
	pricings, err := a.listPricingRules()
	if err != nil {
		return nil, "", err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return nil, "", err
	}

	return buildAdminBookingOptions(
		activeSchedulesOnly(schedules),
		&schedule,
		excludeID,
		activities,
		layouts,
		closures,
		pricings,
		settings,
	)
}

func buildPricedBookingWeekDays(
	schedules []SpaceSchedule,
	selectedDate time.Time,
	hours []string,
	pricings []PricingRule,
	settings *PricingSettings,
	activities []CourtActivity,
	layouts []CourtLayout,
	closures []CourtClosure,
) []CalendarDay {
	days := buildBookingWeekDays(
		schedules,
		selectedDate,
		hours,
		activities,
		layouts,
		closures,
	)

	for i := range days {
		slots := buildBookingSlotAvailability(
			schedules,
			days[i].Date,
			hours,
			activities,
			layouts,
			closures,
		)

		slots = filterPricedBookingSlots(
			slots,
			days[i].Date,
			pricings,
			settings,
		)

		days[i].OpenSlotCount =
			bookingOpenHourCount(slots)

		days[i].BusySlotCount =
			len(slots) - days[i].OpenSlotCount
	}

	return days
}

func activeSchedulesOnly(schedules []SpaceSchedule) []SpaceSchedule {
	active := make([]SpaceSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		if schedule.Status == "pending" || schedule.Status == "confirmed" {
			active = append(active, schedule)
		}
	}
	return active
}

func customerBookingRequests(schedules []SpaceSchedule) []SpaceSchedule {
	requests := make([]SpaceSchedule, 0)
	for _, schedule := range schedules {
		if schedule.EntryType == "booking" && (schedule.RequesterName != "" || schedule.RequesterEmail != "" || schedule.RequestedByUser > 0) {
			requests = append(requests, schedule)
		}
	}
	sort.SliceStable(requests, func(i, j int) bool {
		iPending := requests[i].Status == "pending"
		jPending := requests[j].Status == "pending"
		if iPending != jPending {
			return iPending
		}
		return requests[i].CreatedAt.After(requests[j].CreatedAt)
	})
	return requests
}

func buildBookingRequestStats(requests []SpaceSchedule) []Stat {
	pending := 0
	confirmed := 0
	rejected := 0
	receivedToday := 0
	today := time.Now().Format("2006-01-02")
	for _, request := range requests {
		switch request.Status {
		case "pending":
			pending++
		case "confirmed":
			confirmed++
		case "rejected":
			rejected++
		}
		if request.CreatedAt.In(time.Local).Format("2006-01-02") == today {
			receivedToday++
		}
	}
	return []Stat{
		{Label: "Awaiting review", Value: strconv.Itoa(pending)},
		{Label: "Confirmed", Value: strconv.Itoa(confirmed)},
		{Label: "Rejected", Value: strconv.Itoa(rejected)},
		{Label: "Received today", Value: strconv.Itoa(receivedToday)},
	}
}

func bookingReference(scheduleID int64) string {
	return fmt.Sprintf("BK-%06d", scheduleID)
}

func adminCalendarActionURL(
	slotDate string,
	slotHour string,
	entryType string,
	options []AdminBookingOption,
) string {
	values := url.Values{}
	values.Set("action", "new")
	values.Set("date", slotDate)
	values.Set("slot_date", slotDate)
	values.Set("hour", slotHour)
	values.Set("slot_hour", slotHour)
	values.Set("entry_type", entryType)
	if len(options) == 1 {
		values.Set("activity", options[0].Activity)
		values.Set("quantity", strconv.Itoa(options[0].Quantity))
	}
	return "/admin/bookings?" + values.Encode() + "#schedule-form"
}

func closuresForSlot(closures []CourtClosure, slotDate string, slotHour string) []CourtClosure {
	filtered := make([]CourtClosure, 0)
	for _, closure := range closures {
		if courtClosureCoversSlot(closure, slotDate, slotHour) {
			filtered = append(filtered, closure)
		}
	}
	return filtered
}

func courtClosuresBetween(closures []CourtClosure, startDate string, endDate string) []CourtClosure {
	filtered := make([]CourtClosure, 0, len(closures))
	for _, closure := range closures {
		if closure.ClosureDate >= startDate && closure.ClosureDate <= endDate {
			filtered = append(filtered, closure)
		}
	}
	return filtered
}

func relativeTime(value time.Time) string {
	if value.IsZero() {
		return "Unknown"
	}
	elapsed := time.Since(value)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return "Just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d min ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(elapsed.Hours()/24))
	}
}

func bookingStatusTone(status string) string {
	switch status {
	case "pending":
		return "border-amber-200 bg-amber-50 text-amber-900"
	case "confirmed":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case "rejected":
		return "border-red-200 bg-red-50 text-red-800"
	default:
		return "border-slate/10 bg-cloud text-slate"
	}
}

func bookingOpenHourCount(slots []BookingSlotAvailability) int {
	count := 0
	for _, slot := range slots {
		if len(slot.Options) > 0 {
			count++
		}
	}
	return count
}

func bookingReferralFor(referrals []BookingReferral, scheduleID int64) *BookingReferral {
	for i := range referrals {
		if referrals[i].ScheduleID == scheduleID {
			return &referrals[i]
		}
	}
	return nil
}

func bookingRequestHistoryFor(changes []BookingRequestChange, scheduleID int64) []BookingRequestChange {
	history := make([]BookingRequestChange, 0)
	for _, change := range changes {
		if change.ScheduleID == scheduleID {
			history = append(history, change)
		}
	}
	return history
}

func bookingFinancialForSchedule(financials []BookingFinancial, scheduleID int64) *BookingFinancial {
	for i := range financials {
		if financials[i].ScheduleID == scheduleID {
			return &financials[i]
		}
	}
	return nil
}

func bookingCommunicationsFor(communications []BookingCommunication, scheduleID int64) []BookingCommunication {
	filtered := make([]BookingCommunication, 0)
	for _, communication := range communications {
		if communication.ScheduleID == scheduleID {
			filtered = append(filtered, communication)
		}
	}
	return filtered
}

func bookingCommunicationEventLabel(communication BookingCommunication) string {
	eventType := communication.EventType
	if communication.EventType == bookingCommEventResent && communication.RelatedEventType != "" {
		return "Manual resend: " + bookingCommunicationEventTypeLabel(communication.RelatedEventType)
	}
	return bookingCommunicationEventTypeLabel(eventType)
}

func bookingCommunicationEventTypeLabel(eventType string) string {
	switch eventType {
	case bookingCommEventRequestReceived:
		return "Request received"
	case bookingCommEventConfirmed:
		return "Booking confirmed"
	case bookingCommEventRejected:
		return "Booking rejected"
	case bookingCommEventRescheduledPending:
		return "Pending reschedule"
	case bookingCommEventRescheduledConfirmed:
		return "Rescheduled and confirmed"
	case bookingCommEventResent:
		return "Manual resend"
	default:
		return strings.ReplaceAll(strings.TrimSpace(eventType), "_", " ")
	}
}

func bookingCommunicationStatusTone(status string) string {
	switch status {
	case bookingCommStatusSent:
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case bookingCommStatusFailed:
		return "border-red-200 bg-red-50 text-red-900"
	default:
		return "border-slate/10 bg-cloud text-slate"
	}
}

func listBookingFinancialsForScheduleIDsQuery(queryer sqlQueryer, scheduleIDs []int64) ([]BookingFinancial, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT
			bf.id,
			bf.schedule_id,
			bf.quoted_amount,
			bf.paid,
			bf.paid_at,
			bf.payment_method,
			COALESCE(bf.finance_transaction_id, 0),
			s.slot_date,
			s.slot_hour,
			s.activity,
			s.quantity,
			s.status,
			COALESCE(s.requester_name, ''),
			COALESCE(s.requester_email, '')
		FROM booking_financials bf
		JOIN space_schedules s
			ON s.id = bf.schedule_id
		WHERE bf.schedule_id IN (%s)
		ORDER BY s.slot_date ASC, s.slot_hour ASC, bf.id ASC
	`, scheduleIDs)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var financials []BookingFinancial
	for rows.Next() {
		var financial BookingFinancial
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&financial.ID,
			&financial.ScheduleID,
			&financial.QuotedAmount,
			&paid,
			&paidAt,
			&financial.PaymentMethod,
			&financial.FinanceTransactionID,
			&financial.SlotDate,
			&financial.SlotHour,
			&financial.Activity,
			&financial.Quantity,
			&financial.Status,
			&financial.RequesterName,
			&financial.RequesterEmail,
		); err != nil {
			return nil, err
		}
		financial.Paid = paid == 1
		if paidAt.Valid {
			financial.PaidAt = paidAt.Time
		}
		financials = append(financials, financial)
	}
	return financials, rows.Err()
}

func listBookingReferralsForScheduleIDsQuery(queryer sqlQueryer, scheduleIDs []int64) ([]BookingReferral, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT br.id, br.schedule_id, br.partner_id, rp.name, rp.code, br.commission_amount,
		       s.status, s.title, s.slot_date, br.paid, br.paid_at, br.payment_method,
		       COALESCE(br.finance_transaction_id, 0), br.created_at
		FROM booking_referrals br
		JOIN referral_partners rp ON rp.id = br.partner_id
		JOIN space_schedules s ON s.id = br.schedule_id
		WHERE br.schedule_id IN (%s)
		ORDER BY br.created_at DESC, br.id DESC
	`, scheduleIDs)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var referrals []BookingReferral
	for rows.Next() {
		var referral BookingReferral
		var paid int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&referral.ID, &referral.ScheduleID, &referral.PartnerID, &referral.PartnerName,
			&referral.PartnerCode, &referral.CommissionAmount, &referral.BookingStatus,
			&referral.BookingTitle, &referral.SlotDate, &paid, &paidAt, &referral.PaymentMethod,
			&referral.FinanceTransactionID, &referral.CreatedAt,
		); err != nil {
			return nil, err
		}
		referral.BookingReference = bookingReference(referral.ScheduleID)
		referral.Paid = paid == 1
		if paidAt.Valid {
			referral.PaidAt = paidAt.Time
		}
		referrals = append(referrals, referral)
	}
	return referrals, rows.Err()
}

func listBookingRequestChangesForScheduleIDsQuery(queryer sqlQueryer, scheduleIDs []int64) ([]BookingRequestChange, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT
			brch.id,
			brch.schedule_id,
			brch.previous_slot_date,
			brch.previous_slot_hour,
			brch.previous_activity,
			brch.previous_quantity,
			brch.previous_quoted_price,
			brch.new_slot_date,
			brch.new_slot_hour,
			brch.new_activity,
			brch.new_quantity,
			brch.new_quoted_price,
			brch.action_type,
			brch.review_note,
			COALESCE(brch.changed_by_user_id, 0),
			COALESCE(u.name, ''),
			brch.changed_at
		FROM booking_request_changes brch
		LEFT JOIN users u
			ON u.id = brch.changed_by_user_id
		WHERE brch.schedule_id IN (%s)
		ORDER BY brch.changed_at DESC, brch.id DESC
	`, scheduleIDs)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []BookingRequestChange
	for rows.Next() {
		var change BookingRequestChange
		if err := rows.Scan(
			&change.ID,
			&change.ScheduleID,
			&change.PreviousSlotDate,
			&change.PreviousSlotHour,
			&change.PreviousActivity,
			&change.PreviousQuantity,
			&change.PreviousQuote,
			&change.NewSlotDate,
			&change.NewSlotHour,
			&change.NewActivity,
			&change.NewQuantity,
			&change.NewQuote,
			&change.ActionType,
			&change.ReviewNote,
			&change.ChangedByUserID,
			&change.ChangedByUserName,
			&change.ChangedAt,
		); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (a *App) listBookingCommunicationsForScheduleIDs(scheduleIDs []int64) ([]BookingCommunication, error) {
	return listBookingCommunicationsForScheduleIDsQuery(a.db, scheduleIDs)
}

func listBookingCommunicationsForScheduleIDsQuery(queryer sqlQueryer, scheduleIDs []int64) ([]BookingCommunication, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT
			id,
			schedule_id,
			event_type,
			related_event_type,
			event_key,
			channel,
			recipient,
			subject,
			body_preview,
			status,
			provider,
			provider_message,
			attempt_count,
			last_attempt_at,
			sent_at,
			created_at,
			COALESCE(created_by_user_id, 0)
		FROM booking_communications
		WHERE schedule_id IN (%s)
		ORDER BY created_at DESC, id DESC
	`, scheduleIDs)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var communications []BookingCommunication
	for rows.Next() {
		communication, err := scanBookingCommunication(rows)
		if err != nil {
			return nil, err
		}
		communications = append(communications, communication)
	}
	return communications, rows.Err()
}

func (a *App) findBookingCommunicationByEventKeyChannel(eventKey string, channel string) (*BookingCommunication, error) {
	row := a.db.QueryRow(`
		SELECT
			id,
			schedule_id,
			event_type,
			related_event_type,
			event_key,
			channel,
			recipient,
			subject,
			body_preview,
			status,
			provider,
			provider_message,
			attempt_count,
			last_attempt_at,
			sent_at,
			created_at,
			COALESCE(created_by_user_id, 0)
		FROM booking_communications
		WHERE event_key = ?
		  AND channel = ?
		LIMIT 1
	`, eventKey, channel)
	communication, err := scanBookingCommunication(row)
	if err != nil {
		return nil, err
	}
	return &communication, nil
}

func scanBookingCommunication(row rowScanner) (BookingCommunication, error) {
	var (
		communication BookingCommunication
		lastAttempt   sql.NullTime
		sentAt        sql.NullTime
	)
	if err := row.Scan(
		&communication.ID,
		&communication.ScheduleID,
		&communication.EventType,
		&communication.RelatedEventType,
		&communication.EventKey,
		&communication.Channel,
		&communication.Recipient,
		&communication.Subject,
		&communication.BodyPreview,
		&communication.Status,
		&communication.Provider,
		&communication.ProviderMessage,
		&communication.AttemptCount,
		&lastAttempt,
		&sentAt,
		&communication.CreatedAt,
		&communication.CreatedByUserID,
	); err != nil {
		return BookingCommunication{}, err
	}
	if lastAttempt.Valid {
		communication.LastAttemptAt = lastAttempt.Time
	}
	if sentAt.Valid {
		communication.SentAt = sentAt.Time
	}
	return communication, nil
}

func (a *App) createPendingBookingCommunication(communication BookingCommunication) (*BookingCommunication, bool, error) {
	now := time.Now().UTC()
	result, err := a.db.Exec(`
		INSERT INTO booking_communications (
			schedule_id,
			event_type,
			related_event_type,
			event_key,
			channel,
			recipient,
			subject,
			body_preview,
			status,
			provider,
			provider_message,
			attempt_count,
			last_attempt_at,
			sent_at,
			created_at,
			created_by_user_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 0, NULL, NULL, ?, ?)
	`,
		communication.ScheduleID,
		communication.EventType,
		communication.RelatedEventType,
		communication.EventKey,
		communication.Channel,
		communication.Recipient,
		communication.Subject,
		communication.BodyPreview,
		bookingCommStatusPending,
		now,
		nullIfZero(communication.CreatedByUserID),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			existing, findErr := a.findBookingCommunicationByEventKeyChannel(communication.EventKey, communication.Channel)
			if findErr != nil {
				return nil, false, findErr
			}
			return existing, true, nil
		}
		return nil, false, err
	}
	communicationID, err := result.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	communication.ID = communicationID
	communication.Status = bookingCommStatusPending
	communication.CreatedAt = now
	return &communication, false, nil
}

func (a *App) completeBookingCommunicationAttempt(
	communicationID int64,
	status string,
	provider string,
	providerMessage string,
) error {
	now := time.Now().UTC()
	var sentAt any
	if status == bookingCommStatusSent {
		sentAt = now
	}
	_, err := a.db.Exec(`
		UPDATE booking_communications
		SET
			status = ?,
			provider = ?,
			provider_message = ?,
			attempt_count = attempt_count + 1,
			last_attempt_at = ?,
			sent_at = COALESCE(?, sent_at)
		WHERE id = ?
	`,
		status,
		provider,
		truncateString(strings.TrimSpace(providerMessage), 300),
		now,
		sentAt,
		communicationID,
	)
	return err
}

func scheduleIDScopedQuery(base string, scheduleIDs []int64) (string, []any) {
	placeholders := make([]string, 0, len(scheduleIDs))
	args := make([]any, 0, len(scheduleIDs))
	for _, scheduleID := range scheduleIDs {
		placeholders = append(placeholders, "?")
		args = append(args, scheduleID)
	}
	return fmt.Sprintf(base, strings.Join(placeholders, ",")), args
}

func scheduleIDs(schedules []SpaceSchedule) []int64 {
	ids := make([]int64, 0, len(schedules))
	for _, schedule := range schedules {
		ids = appendInt64Unique(ids, schedule.ID)
	}
	return ids
}

func appendInt64Unique(values []int64, value int64) []int64 {
	if value <= 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func quotedPriceForSchedule(financials []BookingFinancial, scheduleID int64) string {
	financial := bookingFinancialForSchedule(financials, scheduleID)
	if financial == nil {
		return "Unquoted"
	}
	return money(financial.QuotedAmount)
}

func bookingRequestOriginalSnapshot(
	schedule *SpaceSchedule,
	changes []BookingRequestChange,
	financials []BookingFinancial,
) BookingRequestSnapshot {
	snapshot := BookingRequestSnapshot{}
	if schedule == nil {
		return snapshot
	}

	snapshot.SlotDate = schedule.SlotDate
	snapshot.SlotHour = schedule.SlotHour
	snapshot.Activity = schedule.Activity
	snapshot.Quantity = schedule.Quantity
	if financial := bookingFinancialForSchedule(financials, schedule.ID); financial != nil {
		snapshot.QuotedPrice = financial.QuotedAmount
	}

	history := bookingRequestHistoryFor(changes, schedule.ID)
	if len(history) == 0 {
		return snapshot
	}

	oldest := history[len(history)-1]
	snapshot.SlotDate = oldest.PreviousSlotDate
	snapshot.SlotHour = oldest.PreviousSlotHour
	snapshot.Activity = oldest.PreviousActivity
	snapshot.Quantity = oldest.PreviousQuantity
	snapshot.QuotedPrice = oldest.PreviousQuote
	return snapshot
}

func bookingRequestActionLabel(actionType string) string {
	switch actionType {
	case "rescheduled_confirmed":
		return "Rescheduled and confirmed"
	case "rescheduled":
		return "Rescheduled"
	default:
		return strings.ReplaceAll(strings.TrimSpace(actionType), "_", " ")
	}
}

func buildReferralStats(referrals []BookingReferral) []Stat {
	referredBookings := len(referrals)
	pendingBookings := 0
	payable := 0.0
	paid := 0.0
	for _, referral := range referrals {
		switch {
		case referral.Paid:
			paid += referral.CommissionAmount
		case referral.BookingStatus == "confirmed":
			payable += referral.CommissionAmount
		case referral.BookingStatus == "pending":
			pendingBookings++
		}
	}
	return []Stat{
		{Label: "Referred bookings", Value: strconv.Itoa(referredBookings)},
		{Label: "Awaiting confirmation", Value: strconv.Itoa(pendingBookings)},
		{Label: "Commission payable", Value: money(payable)},
		{Label: "Commission paid", Value: money(paid)},
	}
}

func buildReferralPartnerSummaries(partners []ReferralPartner, referrals []BookingReferral) []ReferralPartnerSummary {
	summaries := make([]ReferralPartnerSummary, len(partners))
	positions := make(map[int64]int, len(partners))
	for i, partner := range partners {
		summaries[i].Partner = partner
		positions[partner.ID] = i
	}
	for _, referral := range referrals {
		position, ok := positions[referral.PartnerID]
		if !ok {
			continue
		}
		summary := &summaries[position]
		summary.ReferralCount++
		switch {
		case referral.Paid:
			summary.PaidCount++
			summary.PaidAmount += referral.CommissionAmount
		case referral.BookingStatus == "confirmed":
			summary.PayableCount++
			summary.PayableAmount += referral.CommissionAmount
		case referral.BookingStatus == "pending":
			summary.PendingCount++
		}
	}
	return summaries
}

func containsPermission(permissions []string, target string) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func admissionSelected(admissions []Admission, admissionID int64) bool {
	for _, admission := range admissions {
		if admission.ID == admissionID {
			return true
		}
	}
	return false
}
func userSelected(users []User, userID int64) bool {
	for _, user := range users {
		if user.ID == userID {
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

func optionSummary(option any) string {
	switch value := option.(type) {
	case BookingOption:
		return scheduleSummary(SpaceSchedule{Activity: value.Activity, Quantity: value.Quantity})
	case *BookingOption:
		if value != nil {
			return scheduleSummary(SpaceSchedule{Activity: value.Activity, Quantity: value.Quantity})
		}
	case AdminBookingOption:
		return scheduleSummary(SpaceSchedule{Activity: value.Activity, Quantity: value.Quantity})
	case *AdminBookingOption:
		if value != nil {
			return scheduleSummary(SpaceSchedule{Activity: value.Activity, Quantity: value.Quantity})
		}
	}
	return ""
}

func bookingOptionSelected(draft *SpaceSchedule, slotHour, activity string, quantity int) bool {
	if draft == nil {
		return false
	}
	return draft.SlotHour == slotHour && draft.Activity == activity && draft.Quantity == quantity
}

func courtLayoutHasActivity(
	layout *CourtLayout,
	activity string,
) bool {
	if layout == nil {
		return false
	}

	for _, item := range layout.Items {
		if item.Activity == activity {
			return true
		}
	}

	return false
}

func courtLayoutActivityQuantity(
	layout *CourtLayout,
	activity string,
	defaultQuantity int,
) int {
	if layout == nil {
		return defaultQuantity
	}

	for _, item := range layout.Items {
		if item.Activity == activity {
			return item.Quantity
		}
	}

	return defaultQuantity
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
		return "Unavailable"
	}
	if rule.WeekdayOffPeak == 0 && rule.WeekdayPeak == 0 && rule.WeekendOffPeak == 0 && rule.WeekendPeak == 0 {
		return "Unavailable"
	}
	return money(priceForRuleSlot(*rule, settings, slotDate, slotHour))
}

func pricingForSchedule(pricings []PricingRule, settings *PricingSettings, schedule *SpaceSchedule) string {
	if schedule == nil || schedule.SlotDate == "" || schedule.SlotHour == "" || schedule.Activity == "" || schedule.Quantity <= 0 {
		return "Choose a combination"
	}
	return pricingForOption(pricings, settings, schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)
}

func (a *App) bookingQuote(schedule SpaceSchedule) (float64, error) {
	pricings, err := a.listPricingRules()
	if err != nil {
		return 0, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return 0, err
	}
	rule := pricingRuleForOption(pricings, schedule.Activity, schedule.Quantity)
	if rule == nil {
		return 0, errors.New("pricing is not configured for this booking")
	}
	amount := priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour)
	if amount <= 0 {
		return 0, errors.New("a positive price is required before creating this booking")
	}
	return amount, nil
}

func money(value float64) string {
	return fmt.Sprintf("LKR %.2f", value)
}

func negate(value float64) float64 {
	return -value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfBlank(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullIfZero(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func prepareUploadStorage(configuredRoot string) (UploadStorage, error) {
	resolvedRoot, err := resolveUploadRoot(configuredRoot)
	if err != nil {
		return UploadStorage{}, err
	}

	storage := UploadStorage{
		Root:     resolvedRoot,
		EventDir: filepath.Join(resolvedRoot, "events"),
	}
	if err := os.MkdirAll(storage.Root, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create upload directory %s: %w", storage.Root, err)
	}
	if err := os.MkdirAll(storage.EventDir, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create event upload directory %s: %w", storage.EventDir, err)
	}

	probe, err := os.CreateTemp(storage.EventDir, ".mekmaa-write-check-*")
	if err != nil {
		return UploadStorage{}, fmt.Errorf("event upload directory is not writable %s: %w", storage.EventDir, err)
	}
	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probeName)
		return UploadStorage{}, fmt.Errorf("close event upload write check %s: %w", probeName, err)
	}
	if err := os.Remove(probeName); err != nil {
		return UploadStorage{}, fmt.Errorf("remove event upload write check %s: %w", probeName, err)
	}
	return storage, nil
}

func registerUploadRoutes(mux *http.ServeMux, storage UploadStorage) {
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(storage.Root))))
	// Keep previously persisted /event-images/ paths available during migration.
	mux.Handle("/event-images/", http.StripPrefix("/event-images/", http.FileServer(http.Dir(storage.EventDir))))
}

func resolveUploadRoot(configuredRoot string) (string, error) {
	root := strings.TrimSpace(configuredRoot)
	if root == "" {
		root = defaultUploadDir
	}
	resolvedRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve upload directory %q: %w", root, err)
	}
	return resolvedRoot, nil
}

func (a *App) uploadedEventImagePath(r *http.Request) (string, error) {
	file, header, err := r.FormFile("image")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return "", nil
		}
		return "", errors.New("invalid event image upload")
	}
	defer file.Close()

	return a.uploads.saveEventImage(file, header)
}

func (s UploadStorage) saveEventImage(file multipart.File, header *multipart.FileHeader) (string, error) {
	if header != nil && header.Size > maxEventImageSize {
		return "", errors.New("event image must be 8MB or smaller")
	}

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read uploaded event image: %w", err)
	}
	if n == 0 {
		return "", errors.New("event image is empty")
	}
	contentType := http.DetectContentType(buf[:n])
	ext, ok := eventImageExtension(contentType)
	if !ok {
		return "", errors.New("event image must be a JPEG, PNG or WebP file")
	}

	if err := os.MkdirAll(s.EventDir, 0o755); err != nil {
		return "", fmt.Errorf("prepare event image directory %s: %w", s.EventDir, err)
	}

	filename, err := newEventImageFilename(ext)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(s.EventDir, filename)

	dst, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create uploaded event image %s: %w", targetPath, err)
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			_ = dst.Close()
		}
		if !complete {
			_ = os.Remove(targetPath)
		}
	}()

	if _, err := dst.Write(buf[:n]); err != nil {
		return "", fmt.Errorf("write uploaded event image %s: %w", targetPath, err)
	}
	remaining := int64(maxEventImageSize - n)
	copied, err := io.Copy(dst, io.LimitReader(file, remaining+1))
	if err != nil {
		return "", fmt.Errorf("copy uploaded event image %s: %w", targetPath, err)
	}
	if int64(n)+copied > maxEventImageSize {
		return "", errors.New("event image must be 8MB or smaller")
	}
	if err := dst.Close(); err != nil {
		closed = true
		return "", fmt.Errorf("close uploaded event image %s: %w", targetPath, err)
	}
	closed = true
	complete = true

	return eventImagePublicPath(filename)
}

func newEventImageFilename(extension string) (string, error) {
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		return "", errors.New("unsupported event image extension")
	}
	token, err := generateToken(18)
	if err != nil {
		return "", fmt.Errorf("generate event image filename: %w", err)
	}
	filename := "event-" + strings.ToLower(token) + extension
	if !eventImagePattern.MatchString(filename) {
		return "", errors.New("generated event image filename is invalid")
	}
	return filename, nil
}

func eventImagePublicPath(filename string) (string, error) {
	if !eventImagePattern.MatchString(filename) || filepath.Base(filename) != filename {
		return "", errors.New("invalid event image filename")
	}
	return "/uploads/events/" + filename, nil
}

func eventImageExtension(contentType string) (string, bool) {
	switch contentType {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}

func (s UploadStorage) deleteEventImage(imagePath string) error {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return nil
	}

	filename := ""
	for _, prefix := range []string{"/uploads/events/", "/event-images/"} {
		if strings.HasPrefix(trimmed, prefix) {
			filename = strings.TrimPrefix(trimmed, prefix)
			break
		}
	}
	if filename == "" {
		return nil
	}
	if !storedEventPattern.MatchString(filename) || filepath.Base(filename) != filename {
		return errors.New("invalid event image path")
	}
	localPath := filepath.Join(s.EventDir, filename)
	if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete event image %s: %w", localPath, err)
	}
	return nil
}

func (a *App) removeUploadedEventImage(imagePath string) {
	if err := a.uploads.deleteEventImage(imagePath); err != nil {
		log.Printf("remove uploaded event image: %v", err)
	}
}

func financeCategoryLabel(value string) string {
	switch value {
	case "admission_payment":
		return "Admission payment"
	case "student_monthly_payment":
		return "Student monthly payment"
	case "booking_payment":
		return "Booking payment"
	case "referral_commission_payment":
		return "Referral commission"
	case "manual_income":
		return "General income"
	case "sponsorship_income":
		return "Sponsorship income"
	case "other_income":
		return "Other income"
	case "facility_expense":
		return "Facility expense"
	case "utilities_expense":
		return "Utilities expense"
	case "maintenance_expense":
		return "Maintenance expense"
	case "staff_expense":
		return "Staff expense"
	case "equipment_expense":
		return "Equipment expense"
	case "marketing_expense":
		return "Marketing expense"
	case "other_expense":
		return "Other expense"
	default:
		return "Transaction"
	}
}

func parsePaymentMonth(value string) (time.Time, error) {
	if len(value) != 7 {
		return time.Time{}, errors.New("a valid payment month is required")
	}
	parsed, err := time.Parse("2006-01", value)
	if err != nil || parsed.Format("2006-01") != value {
		return time.Time{}, errors.New("a valid payment month is required")
	}
	return parsed, nil
}

func paymentMonthLabel(value string) string {
	parsed, err := parsePaymentMonth(value)
	if err != nil {
		return value
	}
	return parsed.Format("January 2006")
}

func validPaymentMethod(value string) bool {
	switch value {
	case "cash", "card", "bank_transfer":
		return true
	default:
		return false
	}
}

func formatDateTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.In(time.Local).Format("2006-01-02 15:04")
}

func formatCalendarDate(value string) string {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed.Format("02 Jan 2006")
}

func formatClockTime(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Time to be announced"
	}
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed.Format("3:04 PM")
}

func formatEventTiming(event Event) string {
	switch {
	case event.StartTime != "" && event.EndTime != "":
		return formatClockTime(event.StartTime) + " to " + formatClockTime(event.EndTime)
	case event.StartTime != "":
		return "Starts at " + formatClockTime(event.StartTime)
	default:
		return "Date only"
	}
}

func eventScheduleLabel(event Event) string {
	base := formatCalendarDate(event.EventDate)
	switch {
	case event.StartTime != "" && event.EndTime != "":
		return base + " • " + formatClockTime(event.StartTime) + " to " + formatClockTime(event.EndTime)
	case event.StartTime != "":
		return base + " • " + formatClockTime(event.StartTime)
	default:
		return base
	}
}

func hasRegistrationDeadline(event Event) bool {
	return strings.TrimSpace(event.RegistrationDeadline) != ""
}

func registrationDeadlineLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "Register before " + formatCalendarDate(value)
}

func isPastEventDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return false
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return parsed.Before(today)
}

func upcomingEvents(events []Event, limit int) []Event {
	var filtered []Event
	for _, event := range events {
		if !isPastEventDate(event.EventDate) {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		return filtered[:limit]
	}
	return filtered
}

func hasTime(value time.Time) bool {
	return !value.IsZero()
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

func buildFinanceStats(transactions []FinanceTransaction) []Stat {
	totalIncome := 0.0
	admissionPayments := 0
	studentPayments := 0
	referralPayouts := 0.0

	for _, transaction := range transactions {
		totalIncome += transaction.Amount
		if transaction.Category == "admission_payment" {
			admissionPayments++
		}
		if transaction.Category == "student_monthly_payment" {
			studentPayments++
		}
		if transaction.Category == "referral_commission_payment" {
			referralPayouts += -transaction.Amount
		}
	}

	return []Stat{
		{Label: "Net recorded cash", Value: money(totalIncome)},
		{Label: "Admission payments", Value: strconv.Itoa(admissionPayments)},
		{Label: "Student payments", Value: strconv.Itoa(studentPayments)},
		{Label: "Referral commission paid", Value: money(referralPayouts)},
	}
}

func buildFinanceSummary(transactions []FinanceTransaction, bookings []BookingFinancial, monthly []StudentPaymentRow, referrals []BookingReferral) FinanceSummary {
	var summary FinanceSummary
	for _, transaction := range transactions {
		if transaction.Amount >= 0 {
			summary.GrossIncome += transaction.Amount
		} else {
			summary.TotalExpenses += -transaction.Amount
		}
	}
	summary.NetCash = summary.GrossIncome - summary.TotalExpenses
	for _, booking := range bookings {
		summary.OutstandingBooking += booking.QuotedAmount
	}
	for _, row := range monthly {
		if row.Payment == nil {
			summary.OutstandingMonthly += row.MonthlyFee
		}
	}
	for _, referral := range referrals {
		if referral.BookingStatus == "confirmed" && !referral.Paid {
			summary.PayableReferrals += referral.CommissionAmount
		}
	}
	return summary
}

func reportPeriodFromRequest(r *http.Request) ReportPeriod {
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("period")))
	if kind != "day" && kind != "week" && kind != "month" {
		kind = "day"
	}
	anchor, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.URL.Query().Get("date")), time.Local)
	if err != nil {
		now := time.Now().In(time.Local)
		anchor = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	var start, end, previous, next time.Time
	switch kind {
	case "week":
		daysSinceMonday := (int(anchor.Weekday()) + 6) % 7
		start = anchor.AddDate(0, 0, -daysSinceMonday)
		end = start.AddDate(0, 0, 6)
		previous = anchor.AddDate(0, 0, -7)
		next = anchor.AddDate(0, 0, 7)
	case "month":
		start = time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 1, -1)
		previous = start.AddDate(0, -1, 0)
		next = start.AddDate(0, 1, 0)
	default:
		start = anchor
		end = anchor
		previous = anchor.AddDate(0, 0, -1)
		next = anchor.AddDate(0, 0, 1)
	}
	label := start.Format("Monday, 02 January 2006")
	if kind == "week" {
		label = start.Format("02 Jan") + " - " + end.Format("02 Jan 2006")
	}
	if kind == "month" {
		label = start.Format("January 2006")
	}
	return ReportPeriod{
		Kind:         kind,
		Anchor:       anchor.Format("2006-01-02"),
		Start:        start.Format("2006-01-02"),
		End:          end.Format("2006-01-02"),
		Label:        label,
		PreviousDate: previous.Format("2006-01-02"),
		NextDate:     next.Format("2006-01-02"),
	}
}

func (a *App) buildOperationalReport(period ReportPeriod) (*OperationalReport, error) {
	report := &OperationalReport{Period: period}
	points := make(map[string]*ReportSeriesPoint)
	start, _ := time.ParseInLocation("2006-01-02", period.Start, time.Local)
	end, _ := time.ParseInLocation("2006-01-02", period.End, time.Local)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		report.Series = append(report.Series, ReportSeriesPoint{Date: date, Label: day.Format("Mon 02")})
	}
	for i := range report.Series {
		points[report.Series[i].Date] = &report.Series[i]
	}

	transactions, err := a.listFinanceTransactionsFiltered(FinanceFilter{From: period.Start, To: period.End})
	if err != nil {
		return nil, err
	}
	report.Transactions = transactions
	financeBreakdown := map[string]*ReportBreakdown{}
	for _, transaction := range transactions {
		date := transaction.RecordedAt.Format("2006-01-02")
		point := points[date]
		if transaction.Amount >= 0 {
			report.Summary.Income += transaction.Amount
			if point != nil {
				point.Income += transaction.Amount
			}
		} else {
			expense := -transaction.Amount
			report.Summary.Expenses += expense
			if point != nil {
				point.Expenses += expense
			}
		}
		switch transaction.Category {
		case "booking_payment":
			report.Summary.BookingRevenue += transaction.Amount
		case "student_monthly_payment":
			report.Summary.StudentRevenue += transaction.Amount
			report.Summary.StudentPayments++
		case "admission_payment":
			report.Summary.AdmissionRevenue += transaction.Amount
		}
		item := financeBreakdown[transaction.Category]
		if item == nil {
			item = &ReportBreakdown{Key: transaction.Category, Label: financeCategoryLabel(transaction.Category)}
			financeBreakdown[transaction.Category] = item
		}
		item.Count++
		item.Amount += transaction.Amount
	}
	report.Summary.NetCash = report.Summary.Income - report.Summary.Expenses
	for _, item := range financeBreakdown {
		report.FinanceBreakdown = append(report.FinanceBreakdown, *item)
	}
	sort.Slice(report.FinanceBreakdown, func(i, j int) bool {
		return math.Abs(report.FinanceBreakdown[i].Amount) > math.Abs(report.FinanceBreakdown[j].Amount)
	})

	scheduleRows, err := a.db.Query(`
		SELECT slot_date, slot_hour, entry_type, activity, quantity, status
		FROM space_schedules
		WHERE slot_date BETWEEN ? AND ?
		ORDER BY slot_date, slot_hour, id
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	bookingBreakdown := map[string]*ReportBreakdown{}
	occupied := map[string]struct{}{}
	for scheduleRows.Next() {
		var slotDate, slotHour, entryType, activity, status string
		var quantity int
		if err := scheduleRows.Scan(&slotDate, &slotHour, &entryType, &activity, &quantity, &status); err != nil {
			scheduleRows.Close()
			return nil, err
		}
		if entryType == "booking" {
			if status == "confirmed" {
				report.Summary.ConfirmedBookings++
				if point := points[slotDate]; point != nil {
					point.Bookings++
				}
				item := bookingBreakdown[activity]
				if item == nil {
					item = &ReportBreakdown{Key: activity, Label: activityLabel(activity)}
					bookingBreakdown[activity] = item
				}
				item.Count++
			} else if status == "pending" {
				report.Summary.PendingBookings++
			}
		}
		if status == "confirmed" {
			occupied[slotDate+"|"+slotHour] = struct{}{}
		}
	}
	if err := scheduleRows.Err(); err != nil {
		scheduleRows.Close()
		return nil, err
	}
	if err := scheduleRows.Close(); err != nil {
		return nil, err
	}
	for _, item := range bookingBreakdown {
		report.BookingBreakdown = append(report.BookingBreakdown, *item)
	}
	sort.Slice(report.BookingBreakdown, func(i, j int) bool {
		return report.BookingBreakdown[i].Count > report.BookingBreakdown[j].Count
	})
	report.Summary.OccupiedSlotHours = len(occupied)
	report.Summary.AvailableSlotHours = len(report.Series) * len(bookingHours())
	if report.Summary.AvailableSlotHours > 0 {
		report.Summary.UtilizationRate = float64(report.Summary.OccupiedSlotHours) / float64(report.Summary.AvailableSlotHours) * 100
	}

	admissionRows, err := a.db.Query(`
		SELECT admission_date, COUNT(*)
		FROM admissions
		WHERE admission_date BETWEEN ? AND ?
		GROUP BY admission_date
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	for admissionRows.Next() {
		var date string
		var count int
		if err := admissionRows.Scan(&date, &count); err != nil {
			admissionRows.Close()
			return nil, err
		}
		report.Summary.NewAdmissions += count
		if point := points[date]; point != nil {
			point.Admissions += count
		}
	}
	if err := admissionRows.Err(); err != nil {
		admissionRows.Close()
		return nil, err
	}
	if err := admissionRows.Close(); err != nil {
		return nil, err
	}

	attendanceRows, err := a.db.Query(`
		SELECT attendance_date, status, COUNT(*)
		FROM attendance_records
		WHERE attendance_date BETWEEN ? AND ?
		GROUP BY attendance_date, status
	`, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	for attendanceRows.Next() {
		var date, status string
		var count int
		if err := attendanceRows.Scan(&date, &status, &count); err != nil {
			attendanceRows.Close()
			return nil, err
		}
		report.Summary.AttendanceTotal += count
		if status == "present" {
			report.Summary.AttendancePresent += count
		}
		if point := points[date]; point != nil {
			point.Attendance += count
			if status == "present" {
				point.Present += count
			}
		}
	}
	if err := attendanceRows.Err(); err != nil {
		attendanceRows.Close()
		return nil, err
	}
	if err := attendanceRows.Close(); err != nil {
		return nil, err
	}
	if report.Summary.AttendanceTotal > 0 {
		report.Summary.AttendanceRate = float64(report.Summary.AttendancePresent) / float64(report.Summary.AttendanceTotal) * 100
	}
	for i := range report.Series {
		report.Series[i].NetCash = report.Series[i].Income - report.Series[i].Expenses
		dailyCash := math.Max(report.Series[i].Income, report.Series[i].Expenses)
		if dailyCash > report.MaxDailyCash {
			report.MaxDailyCash = dailyCash
		}
	}
	return report, nil
}

func formatReportNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func reportBarWidth(value, maxValue float64) string {
	if value <= 0 || maxValue <= 0 {
		return "0%"
	}
	percent := value / maxValue * 100
	if percent < 3 {
		percent = 3
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%.1f%%", percent)
}

func financeFilterFromRequest(r *http.Request) FinanceFilter {
	filter := FinanceFilter{
		From:      strings.TrimSpace(r.URL.Query().Get("from")),
		To:        strings.TrimSpace(r.URL.Query().Get("to")),
		Direction: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("direction"))),
		Category:  strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category"))),
		Search:    strings.TrimSpace(r.URL.Query().Get("search")),
	}
	if _, err := time.Parse("2006-01-02", filter.From); err != nil {
		filter.From = ""
	}
	if _, err := time.Parse("2006-01-02", filter.To); err != nil {
		filter.To = ""
	}
	if filter.Direction != "income" && filter.Direction != "expense" {
		filter.Direction = ""
	}
	return filter
}

func validManualFinanceCategory(direction, category string) bool {
	income := map[string]bool{"manual_income": true, "sponsorship_income": true, "other_income": true}
	expense := map[string]bool{
		"facility_expense": true, "utilities_expense": true, "maintenance_expense": true,
		"staff_expense": true, "equipment_expense": true, "marketing_expense": true, "other_expense": true,
	}
	if direction == "income" {
		return income[category]
	}
	if direction == "expense" {
		return expense[category]
	}
	return false
}

func csvSafeCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}

func normalizeRoleName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isSystemRole(name string) bool {
	switch name {
	case "customer", "editor", "coach", "admin", "superadmin":
		return true
	default:
		return false
	}
}

func isPrivilegedRole(name string) bool {
	return name == "admin" || name == "superadmin"
}

func isIgnorableMigrationError(err error, stmt string) bool {
	lowerErr := strings.ToLower(err.Error())
	return (strings.Contains(stmt, "ALTER TABLE users ADD COLUMN email_verified_at") ||
		strings.Contains(stmt, "ALTER TABLE events ADD COLUMN registration_deadline") ||
		strings.Contains(stmt, "ALTER TABLE events ADD COLUMN image_path") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN student_id") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN admission_date") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN practice_type") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN payment_collected") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN payment_collected_at") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN admission_payment_amount") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN finance_transaction_id") ||
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
		"admissionSelected":              admissionSelected,
		"userSelected":                   userSelected,
		"admissionAge":                   admissionAge,
		"attendanceCount":                attendanceCount,
		"attendanceRecordFor":            attendanceRecordFor,
		"attendanceStatus":               attendanceStatus,
		"activityLabel":                  activityLabel,
		"bookingProductLabel":            bookingProductLabel,
		"optionSummary":                  optionSummary,
		"bookingOptionSelected":          bookingOptionSelected,
		"bookingReference":               bookingReference,
		"bookingOpenHourCount":           bookingOpenHourCount,
		"bookingReferralFor":             bookingReferralFor,
		"bookingRequestHistoryFor":       bookingRequestHistoryFor,
		"bookingRequestOriginalSnapshot": bookingRequestOriginalSnapshot,
		"bookingRequestActionLabel":      bookingRequestActionLabel,
		"bookingCommunicationEventLabel": bookingCommunicationEventLabel,
		"bookingCommunicationStatusTone": bookingCommunicationStatusTone,
		"bookingCommunicationsFor":       bookingCommunicationsFor,
		"bookingStatusTone":              bookingStatusTone,
		"quotedPriceForSchedule":         quotedPriceForSchedule,
		"courtLayoutHasActivity":         courtLayoutHasActivity,
		"courtLayoutActivityQuantity":    courtLayoutActivityQuantity,
		"pricingForOption":               pricingForOption,
		"pricingForSchedule":             pricingForSchedule,
		"pricingTierLabel":               pricingTierLabel,
		"financeCategoryLabel":           financeCategoryLabel,
		"paymentMonthLabel":              paymentMonthLabel,
		"formatDateTime":                 formatDateTime,
		"relativeTime":                   relativeTime,
		"formatCalendarDate":             formatCalendarDate,
		"formatClockTime":                formatClockTime,
		"formatEventTiming":              formatEventTiming,
		"eventScheduleLabel":             eventScheduleLabel,
		"hasTime":                        hasTime,
		"hasRegistrationDeadline":        hasRegistrationDeadline,
		"isPastEventDate":                isPastEventDate,
		"money":                          money,
		"negate":                         negate,
		"reportBarWidth":                 reportBarWidth,
		"registrationDeadlineLabel":      registrationDeadlineLabel,
		"scheduleToneClasses":            scheduleToneClasses,
		"scheduleBadgeClasses":           scheduleBadgeClasses,
		"schedulesForCalendarSlot":       schedulesForCalendarSlot,
		"scheduleSummary":                scheduleSummary,
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
		"templates/partials/sport-detail-content.html",
		"templates/partials/home-coaching-strip.html",
		"templates/partials/home-highlights.html",
		"templates/partials/home-events-strip.html",
		"templates/partials/home-booking-flow.html",
		"templates/partials/home-cta-band.html",
		"templates/partials/home-script.html",
	}

	pages := map[string]string{
		"home":                        "templates/pages/home.html",
		"about":                       "templates/pages/about.html",
		"book":                        "templates/pages/book.html",
		"contact":                     "templates/pages/contact.html",
		"coaching":                    "templates/pages/coaching.html",
		"faq":                         "templates/pages/faq.html",
		"gallery":                     "templates/pages/gallery.html",
		"events":                      "templates/pages/events.html",
		"login":                       "templates/login.html",
		"privacy-policy":              "templates/pages/privacy-policy.html",
		"register":                    "templates/register.html",
		"refund-policy":               "templates/pages/refund-policy.html",
		"sports":                      "templates/pages/sports.html",
		"sports-cricket":              "templates/pages/sports-cricket.html",
		"sports-futsal":               "templates/pages/sports-futsal.html",
		"sports-badminton":            "templates/pages/sports-badminton.html",
		"sports-table-tennis":         "templates/pages/sports-table-tennis.html",
		"sports-tennis":               "templates/pages/sports-tennis.html",
		"terms-and-conditions":        "templates/pages/terms-and-conditions.html",
		"verify-email":                "templates/verify-email.html",
		"dashboard":                   "templates/dashboard/dashboard.html",
		"editor":                      "templates/dashboard/editor.html",
		"user-management":             "templates/dashboard/user-management.html",
		"role-management":             "templates/dashboard/role-management.html",
		"admission-management":        "templates/dashboard/admission-management.html",
		"training-program-management": "templates/dashboard/training-program-management.html",
		"student-group-management":    "templates/dashboard/student-group-management.html",
		"attendance-management":       "templates/dashboard/attendance-management.html",
		"court-management":            "templates/dashboard/court-management.html",
		"booking-management":          "templates/dashboard/booking-management.html",
		"booking-requests":            "templates/dashboard/booking-requests.html",
		"pricing-management":          "templates/dashboard/pricing-management.html",
		"events-management":           "templates/dashboard/events-management.html",
		"finance-management":          "templates/dashboard/finance-management.html",
		"finance-receipt":             "templates/dashboard/finance-receipt.html",
		"student-payments":            "templates/dashboard/student-payments.html",
		"referral-commissions":        "templates/dashboard/referral-commissions.html",
		"reports":                     "templates/dashboard/reports.html",
		"forbidden":                   "templates/dashboard/forbidden.html",
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
