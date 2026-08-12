package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName               = "mekmaa3_session"
	csrfCookieName                  = "mekmaa3_csrf"
	flashCookieName                 = "mekmaa3_flash"
	sessionTTL                      = 24 * time.Hour
	otpTTL                          = 10 * time.Minute
	maxEventImageSize               = 8 << 20
	maxEventFormSize                = maxEventImageSize + (1 << 20)
	maxStudentPhotoSize             = 5 << 20
	defaultUploadDir                = "./data/uploads"
	defaultBookingAccessTokenSecret = "MEKMAA_DEV_BOOKING_ACCESS_TOKEN_SECRET_CHANGE_ME"
	financeAccountTypeCash          = "cash"
	financeAccountTypeBank          = "bank"
	financeAccountCashInHand        = "Cash in Hand"
	financeAccountMainBank          = "Main Bank Account"
	financeTxnTypeIncome            = "income"
	financeTxnTypeExpense           = "expense"
	financeTxnTypeTransferIn        = "transfer_in"
	financeTxnTypeTransferOut       = "transfer_out"
	financeTxnTypeOpeningBalance    = "opening_balance"
	financeTxnTypeAdjustment        = "adjustment"
)

var (
	emailPattern        = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	passwordPattern     = regexp.MustCompile(`^.{10,}$`)
	otpPattern          = regexp.MustCompile(`^\d{6}$`)
	referralCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{3,24}$`)
	roleNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
	eventImagePattern   = regexp.MustCompile(`^event-[a-z0-9_-]{12,64}\.(jpg|png|webp)$`)
	storedEventPattern  = regexp.MustCompile(`^event-[a-z0-9_-]{12,64}\.(jpg|png|gif|webp)$`)
	studentPhotoPattern = regexp.MustCompile(`^student-photo-[a-z0-9_-]{12,64}\.(jpg|png|webp)$`)
	studentQRPattern    = regexp.MustCompile(`^student-qr-[a-z0-9_-]{12,64}\.png$`)
	allRoles            = []string{"superadmin", "admin", "editor", "coach", "customer"}
	allPermissions      = []string{
		"dashboard.view",
		"editor.access",
		"users.manage",
		"roles.manage",
		"admissions.manage",
		"coaches.manage",
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
			Key:         "coaches.manage",
			Label:       "Manage coaches",
			Description: "Create, update, remove, and track coach attendance records.",
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

func parseAppEnvironment(raw string) (AppEnvironment, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(appEnvDevelopment):
		return appEnvDevelopment, nil
	case string(appEnvTest):
		return appEnvTest, nil
	case string(appEnvProduction):
		return appEnvProduction, nil
	default:
		return "", fmt.Errorf("APP_ENV must be one of development, test, or production")
	}
}

func envValue(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func validateBookingAccessSecret(env AppEnvironment, secret string) []string {
	if env != appEnvProduction {
		return nil
	}
	secret = strings.TrimSpace(secret)
	switch {
	case secret == "":
		return []string{"BOOKING_ACCESS_TOKEN_SECRET is required in production"}
	case secret == defaultBookingAccessTokenSecret:
		return []string{"BOOKING_ACCESS_TOKEN_SECRET must not use the development default in production"}
	case len(secret) < 32:
		return []string{"BOOKING_ACCESS_TOKEN_SECRET must be at least 32 characters in production"}
	default:
		return nil
	}
}

func validatePublicBaseURL(env AppEnvironment, raw string) []string {
	if env != appEnvProduction {
		return nil
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return []string{"MEKMAA_PUBLIC_BASE_URL is required in production"}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return []string{"MEKMAA_PUBLIC_BASE_URL must be a valid HTTPS URL in production"}
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case parsed.Scheme != "https":
		return []string{"MEKMAA_PUBLIC_BASE_URL must use HTTPS in production"}
	case host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".localhost"):
		return []string{"MEKMAA_PUBLIC_BASE_URL must not point to localhost in production"}
	case strings.Contains(host, "dev") || strings.Contains(host, "local"):
		return []string{"MEKMAA_PUBLIC_BASE_URL must not use an obvious development host in production"}
	default:
		return nil
	}
}

func validateCookieSecurity(env AppEnvironment, cookieSecure bool) []string {
	if env == appEnvProduction && !cookieSecure {
		return []string{"COOKIE_SECURE=true is required in production"}
	}
	return nil
}

func validateSMTPConfiguration(messages BookingCommunicationSettings, smtp SMTPConfig) []string {
	if !messages.EmailEnabled {
		return nil
	}
	var errs []string
	if strings.TrimSpace(smtp.Host) == "" {
		errs = append(errs, "SMTP_HOST is required when booking email delivery is enabled")
	}
	port, err := strconv.Atoi(strings.TrimSpace(smtp.Port))
	if err != nil || port <= 0 || port > 65535 {
		errs = append(errs, "SMTP_PORT must be a valid TCP port when booking email delivery is enabled")
	}
	if strings.TrimSpace(smtp.Username) == "" {
		errs = append(errs, "SMTP_USER is required when booking email delivery is enabled")
	}
	if strings.TrimSpace(smtp.Password) == "" {
		errs = append(errs, "SMTP_PASS is required when booking email delivery is enabled")
	}
	if !emailPattern.MatchString(strings.TrimSpace(smtp.From)) {
		errs = append(errs, "SMTP_FROM must be a valid sender address when booking email delivery is enabled")
	}
	return errs
}

func validateSMSConfiguration(messages BookingCommunicationSettings, sms SMSConfig) []string {
	if !messages.SMSEnabled {
		return nil
	}
	var errs []string
	if strings.TrimSpace(sms.UserID) == "" {
		errs = append(errs, "SMS_USER_ID is required when booking SMS delivery is enabled")
	}
	if strings.TrimSpace(sms.APIKey) == "" {
		errs = append(errs, "SMS_API_KEY is required when booking SMS delivery is enabled")
	}
	if strings.TrimSpace(sms.SenderID) == "" {
		errs = append(errs, "SMS_SENDER_ID is required when booking SMS delivery is enabled")
	}
	return errs
}

func validateRuntimeConfiguration(config AppRuntimeConfig, bookingMessages BookingCommunicationSettings, bookingAccess BookingAccessSettings, smtp SMTPConfig, sms SMSConfig) []string {
	var errs []string
	errs = append(errs, validateBookingAccessSecret(config.Env, bookingAccess.TokenSecret)...)
	errs = append(errs, validatePublicBaseURL(config.Env, bookingAccess.BaseURL)...)
	errs = append(errs, validateCookieSecurity(config.Env, config.CookieSecure)...)
	errs = append(errs, validateSMTPConfiguration(bookingMessages, smtp)...)
	errs = append(errs, validateSMSConfiguration(bookingMessages, sms)...)
	return errs
}

func isTemporaryPath(path string) bool {
	cleaned := filepath.Clean(path)
	tempRoot := filepath.Clean(os.TempDir())
	return cleaned == tempRoot || strings.HasPrefix(cleaned, tempRoot+string(os.PathSeparator))
}

func validateDatabasePath(env AppEnvironment, configured string) (string, []string) {
	var errs []string
	raw := strings.TrimSpace(configured)
	if env == appEnvProduction && raw == "" {
		errs = append(errs, "DB_PATH is required in production")
	}
	if raw == "" {
		raw = "app.db"
	}
	if strings.Contains(raw, "mode=memory") || raw == ":memory:" {
		if env == appEnvProduction {
			errs = append(errs, "production DB_PATH must not use an in-memory SQLite database")
		}
		return raw, errs
	}
	resolved, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return raw, append(errs, "DB_PATH could not be resolved")
	}
	if env == appEnvProduction && isTemporaryPath(resolved) {
		errs = append(errs, "production DB_PATH must not point to a temporary directory")
	}
	return resolved, errs
}

func prepareDatabasePath(dbPath string) error {
	if dbPath == "" || dbPath == ":memory:" || strings.Contains(dbPath, "mode=memory") {
		return nil
	}
	parent := filepath.Dir(dbPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create database directory %s: %w", parent, err)
	}
	probe, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("database path is not writable %s: %w", dbPath, err)
	}
	return probe.Close()
}

func validateUploadPath(env AppEnvironment, root string) []string {
	if env == appEnvProduction && isTemporaryPath(root) {
		return []string{"production UPLOAD_DIR must not point to a temporary directory"}
	}
	return nil
}

func enableSQLiteForeignKeys(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	return nil
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
	bookingAccess   BookingAccessSettings
	runtimeConfig   AppRuntimeConfig
}

type UploadStorage struct {
	Root            string
	EventDir        string
	StudentPhotoDir string
	StudentQRDir    string
}

type AppEnvironment string

const (
	appEnvDevelopment AppEnvironment = "development"
	appEnvTest        AppEnvironment = "test"
	appEnvProduction  AppEnvironment = "production"
)

type AppRuntimeConfig struct {
	Env           AppEnvironment
	Addr          string
	DBPath        string
	UploadRoot    string
	PublicBaseURL string
	CookieSecure  bool
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

type BookingAccessSettings struct {
	BaseURL     string
	TokenSecret string
	TokenTTL    time.Duration
}

type SetupWarning struct {
	Title     string
	Body      string
	Href      string
	LinkLabel string
}

type BookingPricingIssue struct {
	Activity string
	Quantity int
	Label    string
}

type User struct {
	ID              int64
	Email           string
	Name            string
	Roles           []string
	Permissions     []string
	Verified        bool
	Phone           string
	Address         string
	Specialties     string
	Notes           string
	Active          bool
	CoachType       string
	ParentCoachID   int64
	ParentCoachName string
	CreatedAt       time.Time
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
	FreeAdmission            bool
	FreeMonthlyFee           bool
	PaymentCollected         bool
	PaymentCollectedAt       time.Time
	AdmissionPaymentAmount   float64
	FinanceTransactionID     int64
	PaymentVoidReason        string
	PaymentVoidedByUserID    int64
	PaymentVoidedByUserName  string
	PaymentVoidedAt          time.Time
	CreatedAt                time.Time
	TrainingProgramID        int64
	TrainingProgramName      string
	TrainingProgramIDs       []int64
	TrainingPrograms         []TrainingProgram
	TrainingProgramNames     string
	PhotoPath                string
	QRCodePath               string
	QRCodeValue              string
}

type StudentEnrollment struct {
	ID                      int64
	AdmissionID             int64
	TrainingProgramID       int64
	TrainingProgramName     string
	TrainingProgram         *TrainingProgram
	Student                 Admission
	FreeAdmission           bool
	FreeMonthlyFee          bool
	AdmissionPaymentAmount  float64
	AdmissionPaymentPaid    bool
	AdmissionPaymentPaidAt  time.Time
	FinanceTransactionID    int64
	PaymentVoidReason       string
	PaymentVoidedByUserID   int64
	PaymentVoidedByUserName string
	PaymentVoidedAt         time.Time
	Active                  bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type FinanceTransaction struct {
	ID                 int64
	ReceiptNumber      string
	ReferenceNumber    string
	Category           string
	TransactionType    string
	ReferenceType      string
	ReferenceID        int64
	SourceType         string
	SourceID           int64
	FinanceAccountID   int64
	FinanceAccountName string
	FinanceAccountType string
	TransferGroupID    string
	PersonName         string
	Description        string
	Notes              string
	PaymentMethod      string
	Amount             float64
	MoneyIn            float64
	MoneyOut           float64
	RunningBalance     float64
	RecordedByUser     int64
	RecordedByUserName string
	Voided             bool
	GeneralVoidAllowed bool
	OrphanedSource     bool
	VoidedAt           time.Time
	VoidedByUserID     int64
	VoidReason         string
	RecordedAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type FinanceAccount struct {
	ID              int64
	Name            string
	AccountType     string
	Description     string
	OpeningBalance  float64
	CurrentBalance  float64
	LastCountedCash float64
	LastCashDelta   float64
	LastAuditDate   string
	LastAuditStatus string
	IsSystem        bool
	IsActive        bool
	CreatedByUserID int64
	UpdatedByUserID int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type FinanceTransfer struct {
	GroupID            string
	ReferenceNumber    string
	FromAccountID      int64
	FromAccountName    string
	ToAccountID        int64
	ToAccountName      string
	Amount             float64
	TransferDate       time.Time
	Description        string
	Notes              string
	RecordedByUserID   int64
	RecordedByUserName string
	Voided             bool
	VoidedAt           time.Time
	VoidReason         string
	VoidedByUserID     int64
	CreatedAt          time.Time
	TransferOutID      int64
	TransferInID       int64
}

type CashReconciliation struct {
	ID                 int64
	FinanceAccountID   int64
	FinanceAccountName string
	ReconciliationDate string
	ExpectedBalance    float64
	CountedBalance     float64
	Difference         float64
	Notes              string
	Status             string
	ReconciledByUserID int64
	ReconciledByName   string
	Voided             bool
	VoidReason         string
	VoidedByUserID     int64
	VoidedByName       string
	VoidedAt           time.Time
	SupersededByID     int64
	CreatedAt          time.Time
}

type BookingPaymentCollection struct {
	ID                   int64
	ScheduleID           int64
	FinanceTransactionID int64
	ReceiptNumber        string
	PersonName           string
	Description          string
	Amount               float64
	PaymentMethod        string
	PaymentNote          string
	CollectedByUserID    int64
	CollectedByUserName  string
	CollectedAt          time.Time
	CreatedAt            time.Time
	Voided               bool
	VoidReason           string
	VoidedByUserID       int64
	VoidedByUserName     string
	VoidedAt             time.Time
}

type FinanceFilter struct {
	From            string
	To              string
	Direction       string
	Category        string
	AccountID       int64
	TransactionType string
	SourceType      string
	PaymentMethod   string
	RecordedUserID  int64
	Status          string
	Reference       string
	Search          string
	ExportKind      string
	Page            int
	Limit           int
}

type AdmissionsFilter struct {
	Search    string
	Direction string
	Page      int
	Limit     int
}

const (
	defaultFinanceLedgerPageSize = 50
	maxFinanceLedgerPageSize     = 200
	defaultAdmissionsPageSize    = 25
	maxAdmissionsPageSize        = 100
)

type FinanceSummary struct {
	CashBalance              float64
	BankBalance              float64
	TotalAvailableFunds      float64
	GrossIncome              float64
	TotalExpenses            float64
	NetCash                  float64
	NetOperatingCashFlow     float64
	OutstandingBooking       float64
	OutstandingMonthly       float64
	PayableReferrals         float64
	UnreconciledCashDelta    float64
	LastCashReconciliationOn string
}

type BookingFinancial struct {
	ID                    int64
	ScheduleID            int64
	QuotedAmount          float64
	Paid                  bool
	PaidAt                time.Time
	PaymentMethod         string
	FinanceTransactionID  int64
	SlotDate              string
	SlotHour              string
	Activity              string
	Quantity              int
	Status                string
	RequesterName         string
	RequesterEmail        string
	RecordedByUserID      int64
	TotalCollected        float64
	OutstandingAmount     float64
	PaymentStatus         string
	LastPaymentDate       time.Time
	LastPaymentByUserID   int64
	LastPaymentByUserName string
	ActivePaymentCount    int
	VoidedPaymentCount    int
}

type BookingCustomerBalance struct {
	CustomerName      string
	CustomerEmail     string
	BookingCount      int
	OutstandingCount  int
	QuotedAmount      float64
	CollectedAmount   float64
	OutstandingAmount float64
	Bookings          []BookingFinancial
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
	EnrollmentID         int64
	PaymentMonth         string
	Amount               float64
	PaymentMethod        string
	FinanceTransactionID int64
	CollectedByUserID    int64
	CollectedAt          time.Time
	CreatedAt            time.Time
}

type StudentEnrollmentLeave struct {
	ID           int64
	EnrollmentID int64
	StartDate    string
	EndDate      string
	Reason       string
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type StudentPaymentRow struct {
	Admission                 Admission
	Enrollment                StudentEnrollment
	MonthlyFee                float64
	OriginalMonthlyFee        float64
	EnrollmentProrationAmount float64
	LeaveDays                 int
	BillableDays              int
	MonthDays                 int
	LeaveAmount               float64
	Leaves                    []StudentEnrollmentLeave
	CollectedAmount           float64
	OutstandingAmount         float64
	Payment                   *StudentMonthlyPayment
	Payments                  []StudentMonthlyPayment
}

type StudentGroup struct {
	ID                  int64
	Name                string
	Code                string
	Description         string
	TrainingProgramID   int64
	TrainingProgram     *TrainingProgram
	TrainingProgramName string
	Sessions            []StudentGroupSession
	Students            []Admission
	StudentCount        int
	Coaches             []User
	CoachCount          int
	CreatedAt           time.Time
}

type StudentGroupSession struct {
	ID        int64
	GroupID   int64
	Title     string
	DayOfWeek string
	StartTime string
	EndTime   string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CoachAttendanceRecord struct {
	ID               int64
	UserID           int64
	AttendanceDate   string
	Status           string
	Note             string
	RecordedByUserID int64
	RecordedAt       time.Time
	UpdatedAt        time.Time
}

type AttendanceSummary struct {
	SessionCount int
	PresentCount int
	AbsentCount  int
	LateCount    int
	ExcusedCount int
	TotalEntries int
}

type AttendanceLimitWarning struct {
	AdmissionID         int64
	StudentID           string
	FullName            string
	TrainingProgramName string
	SessionCount        int
	Limit               int
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
	AutoAccept  bool
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
	ID                      int64
	SlotDate                string
	SlotHour                string
	EntryType               string
	Activity                string
	Quantity                int
	Title                   string
	Notes                   string
	Status                  string
	RequesterName           string
	RequesterEmail          string
	RequesterPhone          string
	RequestedByUser         int64
	ReviewNote              string
	CustomerMessage         string
	StatusChangedAt         time.Time
	StatusChangedBy         int64
	StatusSource            string
	CancellationReason      string
	CancellationFinanceNote string
	ReferralCode            string
	QuotedPrice             float64
	CreatedAt               time.Time
	UpdatedAt               time.Time
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

type OneToOneOffering struct {
	ID           int64
	Name         string
	Game         string
	Audience     string
	Occurrence   string
	SessionCount int
	Price        float64
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type OneToOneBooking struct {
	ID              int64
	ScheduleID      int64
	OfferingID      int64
	OfferingName    string
	Game            string
	Audience        string
	Occurrence      string
	MaxSessions     int
	Price           float64
	DiscountedPrice float64
	CoachFee        float64
	Sessions        int
	CustomerName    string
	SlotDate        string
	SlotHour        string
	Status          string
	Title           string
	Notes           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinancialStatus string
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
	PreviousStatus    string
	NewStatus         string
	ChangeSource      string
	FinanceNote       string
	ReviewNote        string
	CustomerMessage   string
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

type BookingAccessToken struct {
	ID             int64
	ScheduleID     int64
	PublicID       string
	TokenHash      string
	Purpose        string
	Active         bool
	ExpiresAt      time.Time
	LastAccessedAt time.Time
	CreatedAt      time.Time
	RevokedAt      time.Time
}

type BookingCancellationRequest struct {
	ID               int64
	ScheduleID       int64
	Status           string
	RequestReason    string
	RequestedAt      time.Time
	TokenID          int64
	ReviewNote       string
	ReviewedAt       time.Time
	ReviewedByUserID int64
}

type CustomerBookingTimelineItem struct {
	Label   string
	Detail  string
	When    time.Time
	IsEmail bool
}

type BookingStatusView struct {
	Reference                  string
	StatusLabel                string
	StatusTone                 string
	StatusSummary              string
	NextSteps                  string
	MaskedIdentity             string
	CustomerMessage            string
	CurrentSlotLabel           string
	PreviousSlotLabel          string
	ActivityLabel              string
	QuotedAmount               string
	PaymentStatus              string
	PaidAtLabel                string
	TotalCollected             string
	OutstandingAmount          string
	Title                      string
	ContactPhone               string
	ContactEmail               string
	VenueName                  string
	VenueAddress               string
	CanPrint                   bool
	CanRequestCancellation     bool
	PendingCancellationRequest bool
}

type BookingReminder struct {
	Schedule            SpaceSchedule
	MinutesUntilStart   int
	UrgencyLabel        string
	UrgencyTone         string
	RemainingLabel      string
	IsOverdue           bool
	IsApproachingWindow bool
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
	SessionID        int64
	SessionTitle     string
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
	Title                           string
	Description                     string
	CurrentPath                     string
	User                            *User
	Viewer                          *User
	HideChrome                      bool
	CSRFToken                       string
	Flash                           string
	Error                           string
	Stats                           []Stat
	Features                        []Feature
	Users                           []User
	Available                       []string
	Roles                           []Role
	Permissions                     []string
	PermissionGroups                []PermissionGroup
	Admissions                      []Admission
	AdmissionsTotal                 int
	AdmissionsStart                 int
	AdmissionsEnd                   int
	AdmissionsTotalPages            int
	AdmissionsPageNumbers           []int
	AdmissionsHasPreviousPage       bool
	AdmissionsHasNextPage           bool
	AdmissionsPreviousPageURL       string
	AdmissionsNextPageURL           string
	AdmissionsPageBaseURL           string
	AdmissionsFilter                AdmissionsFilter
	SelectedAdmission               *Admission
	AdmissionMode                   string
	StudentGroups                   []StudentGroup
	SelectedGroup                   *StudentGroup
	GroupMode                       string
	GroupSessions                   []StudentGroupSession
	SelectedGroupSessionID          int64
	AvailableCoaches                []User
	Coaches                         []User
	CoachAttendanceRecords          []CoachAttendanceRecord
	AttendanceRecords               []AttendanceRecord
	AttendanceDate                  string
	RecentDates                     []string
	AttendanceSummary               AttendanceSummary
	AttendanceLimitWarnings         []AttendanceLimitWarning
	Courts                          []Court
	SelectedCourt                   *Court
	CourtMode                       string
	CourtActivities                 []CourtActivity
	CourtLayouts                    []CourtLayout
	SelectedCourtLayout             *CourtLayout
	CourtLayoutMode                 string
	CourtClosures                   []CourtClosure
	SelectedCourtClosure            *CourtClosure
	CourtClosureMode                string
	Schedules                       []SpaceSchedule
	DaySchedules                    []SpaceSchedule
	PendingSchedules                []SpaceSchedule
	BookingRequests                 []SpaceSchedule
	SelectedSchedule                *SpaceSchedule
	DraftSchedule                   *SpaceSchedule
	ScheduleMode                    string
	Pricings                        []PricingRule
	TrainingPrograms                []TrainingProgram
	SelectedTrainingProgram         *TrainingProgram
	TrainingProgramMode             string
	Enrollments                     []StudentEnrollment
	SelectedEnrollment              *StudentEnrollment
	EnrollmentMode                  string
	PricingSettings                 *PricingSettings
	OneToOneOfferings               []OneToOneOffering
	SelectedOneToOneOffering        *OneToOneOffering
	OneToOneMode                    string
	OneToOneBookings                []OneToOneBooking
	ReferralPartners                []ReferralPartner
	ReferralPartnerRows             []ReferralPartnerSummary
	BookingReferrals                []BookingReferral
	BookingRequestChanges           []BookingRequestChange
	ReferralStats                   []Stat
	SelectedPricing                 *PricingRule
	PricingMode                     string
	Events                          []Event
	SelectedEvent                   *Event
	EventMode                       string
	FinanceTransactions             []FinanceTransaction
	FinanceTransactionsTotal        int
	FinanceAccounts                 []FinanceAccount
	FinanceTransfers                []FinanceTransfer
	CashReconciliations             []CashReconciliation
	SelectedFinanceAccount          *FinanceAccount
	SelectedFinance                 *FinanceTransaction
	FinanceFilter                   FinanceFilter
	FinancePage                     string
	FinanceLedgerHasPreviousPage    bool
	FinanceLedgerHasNextPage        bool
	FinanceLedgerPreviousPageURL    string
	FinanceLedgerNextPageURL        string
	FinanceSummary                  FinanceSummary
	FinanceCustomerSearch           string
	StatementOpeningBalance         float64
	StatementClosingBalance         float64
	StatementMoneyIn                float64
	StatementMoneyOut               float64
	StatementNetMovement            float64
	BookingFinancials               []BookingFinancial
	BookingCustomerBalances         []BookingCustomerBalance
	BookingPaymentCollections       []BookingPaymentCollection
	BookingCommunications           []BookingCommunication
	BookingAccessTokens             []BookingAccessToken
	BookingCancellationRequests     []BookingCancellationRequest
	SelectedAccessToken             *BookingAccessToken
	BookingStatusView               *BookingStatusView
	BookingStatusToken              string
	BookingStatusTimeline           []CustomerBookingTimelineItem
	BookingStatusUnavailable        bool
	BookingStatusUnavailableMessage string
	Report                          *OperationalReport
	ReceiptAdmission                *Admission
	ReceiptEnrollment               *StudentEnrollment
	ReceiptBookingPayment           *BookingPaymentCollection
	ReceiptBookingSchedule          *SpaceSchedule
	ReceiptBookingFinancial         *BookingFinancial
	StudentPaymentRows              []StudentPaymentRow
	EnrollmentLeaves                []StudentEnrollmentLeave
	PaymentMonth                    string
	PaymentMonthLabel               string
	PaymentCollectionOpen           bool
	PaymentCollectionNotice         string
	PaymentTotalDue                 float64
	PaymentCollected                float64
	PaymentOutstanding              float64
	PaymentPaidCount                int
	PaymentPendingCount             int
	BookingSlots                    []BookingSlotAvailability
	AdminCalendarHours              []AdminCalendarHour
	AdminBookingOptions             []AdminBookingOption
	AdminBookingBlockedReason       string
	SetupWarnings                   []SetupWarning
	WeekDays                        []CalendarDay
	BookingOptions                  []BookingOption
	Activities                      []string
	Hours                           []string
	CalendarDate                    string
	PreviousDate                    string
	NextDate                        string
	TodayDate                       string
	SelectedCoach                   *User
	DailyStats                      []Stat
	BookingRequestStats             []Stat
	PendingRequestCount             int
	HeldRequestCount                int
	BookingReminders                []BookingReminder
	BookingAttentionStats           []Stat
	CalendarCanGoBack               bool
	PendingEmail                    string
	OTPCodeLength                   int
	ResendAction                    string
	SportsCatalog                   []SportPage
	SelectedSport                   *SportPage
	FAQItems                        []FAQItem
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
	ErrEmailTaken                        = errors.New("email already exists")
	ErrInvalidOTP                        = errors.New("invalid verification code")
	ErrMonthlyFeeNotConfigured           = errors.New("monthly fee is not configured for this training programme")
	ErrStudentLeaveOverlap               = errors.New("this leave overlaps an existing leave period for the selected enrollment")
	ErrStudentLeaveCoversMonth           = errors.New("student is fully on leave for the selected month")
	ErrStudentPaymentMonthNotCollectible = errors.New("monthly payments can only be collected after the month ends or on the final day of that month")
	ErrAdmissionFeeNotConfigured         = errors.New(
		"admission fee is not configured for the selected training programme",
	)
	ErrStudentPaymentAlreadyCollected     = errors.New("student payment already collected")
	ErrStudentNotAdmittedForMonth         = errors.New("student was not admitted for the selected month")
	ErrBookingPaymentAlreadyCollected     = errors.New("booking payment already collected")
	ErrBookingPaymentNeedsOverpayApproval = errors.New("booking payment exceeds the outstanding balance")
	ErrRoleAssigned                       = errors.New("role is assigned to one or more users")
	ErrSystemRoleProtected                = errors.New("system roles are protected")
	ErrAdmissionHasMonthlyPaymentHistory  = errors.New("this student has monthly fee history and cannot be deleted")
	ErrCoachHasOtherRoles                 = errors.New("this coach account has other roles and cannot be deleted here")
	ErrCoachRequiresMainCoach             = errors.New("a sub coach must be assigned to a main coach")
	ErrCoachParentMustBeMain              = errors.New("a sub coach can only be assigned under a main coach")
	ErrCoachHasSubCoaches                 = errors.New("this main coach still has sub coaches assigned")
)

const maxBookingCashCollection = 1000000

const (
	bookingCommEventRequestReceived       = "booking_request_received"
	bookingCommEventConfirmed             = "booking_confirmed"
	bookingCommEventHeld                  = "booking_held"
	bookingCommEventRejected              = "booking_rejected"
	bookingCommEventRescheduledPending    = "booking_rescheduled_pending"
	bookingCommEventRescheduledConfirmed  = "booking_rescheduled_confirmed"
	bookingCommEventResent                = "booking_message_resent"
	bookingCommEventCancellationRequested = "booking_cancellation_requested"
	bookingCommEventCancellationApproved  = "booking_cancellation_approved"
	bookingCommEventCancellationRejected  = "booking_cancellation_rejected"
	bookingCommEventCancelledByAdmin      = "booking_cancelled_by_admin"
	bookingCommEventCompleted             = "booking_completed"
	bookingCommEventNoShow                = "booking_no_show"
	bookingCommEventExpired               = "booking_expired"
	bookingCommChannelEmail               = "email"
	bookingCommChannelSMS                 = "sms"
	bookingCommStatusPending              = "pending"
	bookingCommStatusSent                 = "sent"
	bookingCommStatusFailed               = "failed"
	bookingStatusPending                  = "pending"
	bookingStatusHeld                     = "held"
	bookingStatusConfirmed                = "confirmed"
	bookingStatusRejected                 = "rejected"
	bookingStatusReschedulePending        = "reschedule_pending"
	bookingStatusCancelled                = "cancelled"
	bookingStatusCompleted                = "completed"
	bookingStatusNoShow                   = "no_show"
	bookingStatusExpired                  = "expired"
)
