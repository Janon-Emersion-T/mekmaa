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
	allPermissions      = permissionCatalogKeys(permissionGroups)
)

var permissionGroups = buildPermissionGroups()

func permissionCatalogKeys(groups []PermissionGroup) []string {
	keys := make([]string, 0)
	for _, group := range groups {
		for _, permission := range group.Permissions {
			keys = append(keys, permission.Key)
		}
	}
	return normalizePermissions(keys)
}

func permissionDef(
	key string,
	label string,
	description string,
	sensitive bool,
) PermissionDefinition {
	return PermissionDefinition{
		Key:         key,
		Label:       label,
		Description: description,
		Sensitive:   sensitive,
	}
}

func crudPermissionSet(
	prefix string,
	label string,
	subject string,
	sensitive bool,
) []PermissionDefinition {
	return []PermissionDefinition{
		permissionDef(prefix+".manage", "Manage "+label, "Full control over "+subject+".", sensitive),
		permissionDef(prefix+".view", "View "+label, "Open and review "+subject+".", sensitive),
		permissionDef(prefix+".create", "Create "+label, "Create new "+subject+".", sensitive),
		permissionDef(prefix+".update", "Update "+label, "Edit and change existing "+subject+".", sensitive),
		permissionDef(prefix+".delete", "Delete "+label, "Delete or permanently remove "+subject+".", sensitive),
	}
}

func viewUpdatePermissionSet(
	prefix string,
	label string,
	subject string,
	sensitive bool,
) []PermissionDefinition {
	return []PermissionDefinition{
		permissionDef(prefix+".manage", "Manage "+label, "Full control over "+subject+".", sensitive),
		permissionDef(prefix+".view", "View "+label, "Open and review "+subject+".", sensitive),
		permissionDef(prefix+".update", "Update "+label, "Change existing "+subject+".", sensitive),
	}
}

func buildPermissionGroups() []PermissionGroup {
	return []PermissionGroup{
		{
			Name:        "Workspace",
			Description: "Core authenticated workspace access.",
			Permissions: []PermissionDefinition{
				permissionDef("dashboard.view", "View dashboard", "Open the authenticated dashboard and account overview.", false),
				permissionDef("editor.access", "Access editor", "Open the protected content editor workspace.", false),
			},
		},
		{
			Name:        "Administration",
			Description: "High-trust identity and authorization controls.",
			Permissions: append(
				append(
					append(
						crudPermissionSet("users", "users", "user accounts and their assigned roles", true),
						crudPermissionSet("roles", "roles", "authorization roles", true)...,
					),
					crudPermissionSet("user_divisions", "user divisions", "user division assignments", true)...,
				),
				crudPermissionSet("divisions", "divisions", "division records and availability", true)...,
			),
		},
		{
			Name:        "Students",
			Description: "Student intake, programmes, staff assignments, attendance, and billing operations.",
			Permissions: append(
				append(
					append(
						append(
							append(
								append(
									crudPermissionSet("admissions", "admissions", "student admissions", false),
									crudPermissionSet("enrollments", "enrollments", "student enrollment records", false)...,
								),
								crudPermissionSet("student_leaves", "student leaves", "student leave records", false)...,
							),
							crudPermissionSet("coaches", "coaches", "coach and staff records", false)...,
						),
						crudPermissionSet("payroll", "payroll", "payroll runs, salary profiles, and salary payments", true)...,
					),
					crudPermissionSet("training_programs", "training programmes", "training programme records", false)...,
				),
				append(
					crudPermissionSet("student_groups", "student groups", "student groups and class rosters", false),
					viewUpdatePermissionSet("attendance", "attendance", "attendance records and attendance reports", false)...,
				)...,
			),
		},
		{
			Name:        "Bookings",
			Description: "Court configuration, facility scheduling, and customer booking operations.",
			Permissions: append(
				append(
					append(
						append(
							append(
								append(
									crudPermissionSet("courts", "courts", "court layouts, activities, and closures", false),
									crudPermissionSet("games", "games", "bookable games and activities", false)...,
								),
								crudPermissionSet("space_bookings", "booking calendar", "facility booking calendar entries", false)...,
							),
							viewUpdatePermissionSet("booking_requests", "booking requests", "incoming booking requests and their state changes", false)...,
						),
						crudPermissionSet("one_to_one", "1 to 1 offerings", "1 to 1 setup packages", false)...,
					),
					crudPermissionSet("one_to_one_bookings", "1 to 1 bookings", "1 to 1 booking packages and sessions", false)...,
				),
				crudPermissionSet("mcp", "monthly court plans", "monthly court plan records and sessions", false)...,
			),
		},
		{
			Name:        "Finance",
			Description: "Pricing, collections, ledger, referrals, tournaments, and reporting.",
			Permissions: append(
				append(
					append(
						append(
							append(
								append(
									append(
										append(
											append(
												append(
													crudPermissionSet("mcp_pricing", "MCP pricing", "monthly court plan pricing bands", false),
													crudPermissionSet("mcp_receivables", "MCP receivables", "monthly court plan receivables and collections", false)...,
												),
												crudPermissionSet("pricing", "booking pricing", "booking pricing rules and pricing settings", false)...,
											),
											crudPermissionSet("finance", "finance", "shared finance pages and ledger workflows", true)...,
										),
										crudPermissionSet("finance_transactions", "finance transactions", "manual finance entries and ledger transactions", true)...,
									),
									crudPermissionSet("finance_accounts", "finance accounts", "finance accounts and balances", true)...,
								),
								crudPermissionSet("finance_categories", "finance categories", "finance categories", true)...,
							),
							crudPermissionSet("finance_transfers", "finance transfers", "cash and bank transfer records", true)...,
						),
						crudPermissionSet("finance_reconciliations", "finance reconciliations", "cash reconciliation records", true)...,
					),
					append(
						append(
							append(
								crudPermissionSet("student_payments", "student payments", "student payment collections and voids", true),
								crudPermissionSet("referrals", "referrals", "referral partners and referral payouts", true)...,
							),
							crudPermissionSet("tournaments", "tournaments", "tournament ledgers, sponsorships, and expenses", true)...,
						),
						permissionDef("finance.consolidated", "View consolidated finance", "Access cross-division and shared finance views.", true),
					)...,
				),
				[]PermissionDefinition{
					permissionDef("reports.view", "View reports", "Open operational reports.", false),
					permissionDef("reports.export", "Export reports", "Export operational reports.", false),
				}...,
			),
		},
		{
			Name:        "Content",
			Description: "Public website content operations.",
			Permissions: crudPermissionSet("events", "events", "public event content", false),
		},
	}
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

func sqliteRuntimeDSN(dbPath string) string {
	if strings.Contains(dbPath, "_pragma=") {
		return dbPath
	}

	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}

	// Apply pragmas through the DSN so every pooled connection gets the same
	// runtime settings instead of only the first opened connection.
	return dbPath +
		separator +
		"_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
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
	DBDriver      DatabaseDriver
	DatabaseURL   string
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
	UserID     string
	APIKey     string
	SenderID   string
	AlertPhone string
	Enabled    bool
}

type BookingCommunicationSettings struct {
	EmailEnabled bool
	SMSEnabled   bool
	ContactPhone string
	ContactEmail string
	VenueName    string
	VenueAddress string
}

type SMSGatewayAdminView struct {
	GatewayEnabled    bool
	BookingSMSEnabled bool
	SenderID          string
	AlertPhone        string
	BalanceKnown      bool
	LatestBalance     float64
	ChargedFrom       string
	Alerted200        bool
	Alerted100        bool
	UpdatedAt         time.Time
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
	DivisionIDs     []int64
	DivisionCodes   []string
	Divisions       []Division
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
	DivisionIDs              []int64
	DivisionCodes            []string
	Divisions                []Division
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
	EnrollmentDate          string
	TrainingProgramID       int64
	TrainingProgramName     string
	DivisionID              int64
	DivisionCode            string
	DivisionName            string
	Division                *Division
	TrainingProgram         *TrainingProgram
	Student                 Admission
	FreeAdmission           bool
	FreeMonthlyFee          bool
	DiscountedMonthlyFee    float64
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
	ID                   int64
	ReceiptNumber        string
	ReferenceNumber      string
	DivisionID           int64
	DivisionCode         string
	DivisionName         string
	Category             string
	ApprovalStatus       string
	TransactionType      string
	ReferenceType        string
	ReferenceID          int64
	SourceType           string
	SourceID             int64
	FinanceAccountID     int64
	FinanceAccountCode   string
	FinanceAccountName   string
	FinanceAccountType   string
	TransferGroupID      string
	StudentName          string
	StudentID            string
	AdmissionID          int64
	TrainingProgramName  string
	BookingActivity      string
	OneToOneOfferingID   int64
	OneToOneOfferingName string
	PersonName           string
	Description          string
	Notes                string
	PaymentMethod        string
	Amount               float64
	MoneyIn              float64
	MoneyOut             float64
	RunningBalance       float64
	RecordedByUser       int64
	RecordedByUserName   string
	ApprovedByUserID     int64
	ApprovedByUserName   string
	Voided               bool
	GeneralVoidAllowed   bool
	OrphanedSource       bool
	ApprovedAt           time.Time
	VoidedAt             time.Time
	VoidedByUserID       int64
	VoidedByUserName     string
	VoidReason           string
	RecordedAt           time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type FinanceCategory struct {
	ID                     int64
	Code                   string
	Name                   string
	Direction              string
	Active                 bool
	LinkedTransactionCount int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type FinanceAccount struct {
	ID              int64
	DivisionID      int64
	DivisionCode    string
	DivisionName    string
	AccountCode     string
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

type FinancePeriodLock struct {
	ID                int64
	LockedUntil       string
	Notes             string
	UpdatedByUserID   int64
	UpdatedByUserName string
	UpdatedAt         time.Time
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
	From                string
	To                  string
	DivisionID          int64
	DivisionIDs         []int64
	Direction           string
	Category            string
	Categories          []string
	AccountID           int64
	AccountIDs          []int64
	TransactionType     string
	TransactionTypes    []string
	SourceType          string
	SourceTypes         []string
	ReferenceType       string
	ReferenceTypes      []string
	PaymentMethod       string
	PaymentMethods      []string
	ApprovalStatuses    []string
	DetailMode          string
	TrainingProgramIDs  []int64
	BookingActivities   []string
	OneToOneOfferingIDs []int64
	RecordedUserID      int64
	Status              string
	Reference           string
	Search              string
	ExportKind          string
	Page                int
	Limit               int
}

type AdmissionsFilter struct {
	Search      string
	Division    string
	DivisionIDs []int64
	Direction   string
	Page        int
	Limit       int
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

type FinanceStatementItem struct {
	Code             string
	Label            string
	Amount           float64
	ComparisonAmount float64
	Delta            float64
}

type FinanceSpecifiedLedgerEntry struct {
	TransactionID      int64
	RecordedAt         time.Time
	ReferenceNumber    string
	Counterparty       string
	Description        string
	DivisionName       string
	FinanceAccountName string
	DebitAmount        float64
	CreditAmount       float64
}

type FinanceSpecifiedLedger struct {
	Key           string
	Title         string
	Description   string
	Nature        string
	DebitEntries  []FinanceSpecifiedLedgerEntry
	CreditEntries []FinanceSpecifiedLedgerEntry
	DebitTotal    float64
	CreditTotal   float64
	NetBalance    float64
	BalanceLabel  string
	EntryCount    int
}

type FinanceProfitAndLoss struct {
	From                string
	To                  string
	PreviousFrom        string
	PreviousTo          string
	RevenueItems        []FinanceStatementItem
	ExpenseItems        []FinanceStatementItem
	OtherItems          []FinanceStatementItem
	TotalRevenue        float64
	TotalExpenses       float64
	OperatingProfit     float64
	OtherNet            float64
	NetProfit           float64
	ComparisonRevenue   float64
	ComparisonExpenses  float64
	ComparisonOperating float64
	ComparisonOtherNet  float64
	ComparisonNetProfit float64
}

type FinanceBalanceSheet struct {
	AsOf                              string
	AssetItems                        []FinanceStatementItem
	LiabilityItems                    []FinanceStatementItem
	EquityItems                       []FinanceStatementItem
	TotalAssets                       float64
	TotalLiabilities                  float64
	TotalEquity                       float64
	TotalLiabilitiesAndEquity         float64
	BalancingDifference               float64
	WorkingCapital                    float64
	CurrentRatio                      float64
	MemoOutstandingBookingReceivables float64
	MemoCurrentMonthStudentDues       float64
	MemoUnpaidReferralCommissions     float64
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
	Title                 string
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

type FinanceReceivableSummaryCard struct {
	Key               string
	Label             string
	Description       string
	ActionURL         string
	ActionLabel       string
	Count             int
	OutstandingAmount float64
}

type FinanceReceivableOverviewRow struct {
	TypeKey           string
	TypeLabel         string
	Reference         string
	DisplayName       string
	Context           string
	StatusLabel       string
	PaymentLabel      string
	CollectedAmount   float64
	OutstandingAmount float64
	ActionURL         string
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

type ReportDomainOption struct {
	Key         string
	Label       string
	Description string
}

type ReportMetric struct {
	Label string
	Value string
	Note  string
	Tone  string
}

type FinanceDomainReport struct {
	Metrics      []ReportMetric
	Breakdown    []ReportBreakdown
	Transactions []FinanceTransaction
}

type PayrollReportRunRow struct {
	Run                PayrollRun
	PaymentCount       int
	DraftPayments      int
	CalculatedPayments int
	ApprovedPayments   int
	PaidPayments       int
}

type PayrollReportCompensationRow struct {
	CompensationType string
	PaymentCount     int
	Quantity         float64
	NetAmount        float64
}

type PayrollDomainReport struct {
	Metrics      []ReportMetric
	RunRows      []PayrollReportRunRow
	Compensation []PayrollReportCompensationRow
}

type AttendanceDomainGroupRow struct {
	GroupID             int64
	GroupName           string
	GroupCode           string
	TrainingProgramName string
	PresentCount        int
	AbsentCount         int
	LateCount           int
	ExcusedCount        int
	TotalEntries        int
	AttendanceRate      float64
}

type AttendanceDomainStaffRow struct {
	UserID         int64
	UserName       string
	PresentCount   int
	AbsentCount    int
	LateCount      int
	ExcusedCount   int
	TotalRecords   int
	AttendanceRate float64
}

type AttendanceDomainReport struct {
	Metrics   []ReportMetric
	GroupRows []AttendanceDomainGroupRow
	StaffRows []AttendanceDomainStaffRow
}

type StudentProgramReportRow struct {
	TrainingProgramID   int64
	TrainingProgramName string
	DivisionName        string
	TotalEnrollments    int
	ActiveEnrollments   int
	NewEnrollments      int
	StudentPayments     int
	PaymentCollected    float64
	PaymentOutstanding  float64
}

type StudentDomainReport struct {
	Metrics      []ReportMetric
	Programs     []StudentProgramReportRow
	PaymentMonth string
}

type ReportCenter struct {
	Domain     string
	Domains    []ReportDomainOption
	Period     ReportPeriod
	Overview   *OperationalReport
	Finance    *FinanceDomainReport
	Payroll    *PayrollDomainReport
	Attendance *AttendanceDomainReport
	Students   *StudentDomainReport
}

type StudentMonthlyPayment struct {
	ID                   int64
	AdmissionID          int64
	EnrollmentID         int64
	ReceiptNumber        string
	TrainingProgramName  string
	DivisionName         string
	PaymentMonth         string
	Amount               float64
	DiscountAmount       float64
	AdjustmentReason     string
	PaymentMethod        string
	FinanceTransactionID int64
	CollectedByUserID    int64
	CollectedByUserName  string
	Voided               bool
	VoidReason           string
	VoidedByUserID       int64
	VoidedByUserName     string
	VoidedAt             time.Time
	CollectedAt          time.Time
	CreatedAt            time.Time
}

type EnrollmentDeleteBlocker struct {
	Kind   string
	Label  string
	Detail string
}

type EnrollmentDeleteBlock struct {
	Title    string
	Message  string
	Blockers []EnrollmentDeleteBlocker
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
	DiscountAmount            float64
	OutstandingAmount         float64
	Payment                   *StudentMonthlyPayment
	Payments                  []StudentMonthlyPayment
}

type StudentMonthlyPaymentActivityRow struct {
	Payment             StudentMonthlyPayment
	StudentID           string
	StudentName         string
	TrainingProgramName string
	DivisionName        string
	SettledAmount       float64
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

	// Generic operational staff assignments.
	AssignedStaff []GroupStaffAssignment
	StaffCount    int

	// Legacy compatibility for Sports coach attendance and existing flows.
	Coaches    []User
	CoachCount int

	CreatedAt time.Time
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

type StudentGroupSessionOccurrence struct {
	ID int64

	GroupID int64

	TimetableSessionID    int64
	TimetableSessionTitle string

	OccurrenceDate  string
	ActualStartTime string
	ActualEndTime   string

	Status  string
	IsAdHoc bool
	Notes   string

	CreatedByUserID int64
	UpdatedByUserID int64

	CreatedAt time.Time
	UpdatedAt time.Time

	StaffAssignments []StudentGroupSessionStaffAssignment
}

type StudentGroupSessionStaffAssignment struct {
	OccurrenceID int64
	UserID       int64

	UserName  string
	UserEmail string

	AssignmentRole string
	WorkStatus     string
	Notes          string

	RecordedByUserID int64
	RecordedAt       time.Time
	UpdatedAt        time.Time
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
	GameID      int64
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
	GameID         int64
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

type Game struct {
	ID          int64
	Name        string
	Activity    string
	Description string
	Active      bool
	SortOrder   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type OneToOneBooking struct {
	ID                int64
	ScheduleID        int64
	OfferingID        int64
	OfferingName      string
	Game              string
	Audience          string
	Occurrence        string
	MaxSessions       int
	Price             float64
	DiscountedPrice   float64
	CoachFee          float64
	Sessions          int
	CustomerName      string
	CoachUserID       int64
	CoachName         string
	PackageStatus     string
	CompletedSessions int
	CancelledSessions int
	SlotDate          string
	SlotHour          string
	Status            string
	Title             string
	Notes             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	FinancialStatus   string
	BookingSessions   []OneToOneBookingSession
}

type OneToOneReceivable struct {
	Booking   OneToOneBooking
	Financial BookingFinancial
}

type OneToOneBookingSession struct {
	ID                         int64
	BookingID                  int64
	ScheduleID                 int64
	SessionNumber              int
	CoachUserID                int64
	CoachName                  string
	CoachFee                   float64
	SlotDate                   string
	SlotHour                   string
	Status                     string
	AttendanceStatus           string
	AttendanceNote             string
	AttendanceMarkedAt         time.Time
	AttendanceMarkedByUserID   int64
	AttendanceMarkedByUserName string
	Notes                      string
	CompletedAt                time.Time
	CompletedByUserID          int64
	CancelledAt                time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type Tournament struct {
	ID                           int64
	Name                         string
	GameID                       int64
	GameName                     string
	ParticipantCount             int
	EntryFee                     float64
	TournamentDate               string
	EntryFeeFinanceTransactionID int64
	EntryFeeFinanceAccountID     int64
	EntryFeeFinanceAccountName   string
	EntryFeeRecordedAt           time.Time
	Notes                        string
	EntryIncomeTotal             float64
	SponsorshipIncomeTotal       float64
	OfficialExpenseTotal         float64
	OtherExpenseTotal            float64
	TotalIncome                  float64
	TotalExpense                 float64
	NetIncome                    float64
	Sponsorships                 []TournamentSponsorship
	OfficialPayments             []TournamentOfficialPayment
	Expenses                     []TournamentExpense
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type TournamentSponsorship struct {
	ID                   int64
	TournamentID         int64
	SponsorName          string
	Description          string
	Amount               float64
	FinanceTransactionID int64
	FinanceAccountID     int64
	FinanceAccountName   string
	Voided               bool
	RecordedAt           time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type TournamentOfficialPayment struct {
	ID                   int64
	TournamentID         int64
	PersonName           string
	Role                 string
	Description          string
	Amount               float64
	FinanceTransactionID int64
	FinanceAccountID     int64
	FinanceAccountName   string
	Voided               bool
	RecordedAt           time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type TournamentExpense struct {
	ID                   int64
	TournamentID         int64
	ExpenseType          string
	ItemName             string
	Description          string
	Amount               float64
	FinanceTransactionID int64
	FinanceAccountID     int64
	FinanceAccountName   string
	Voided               bool
	RecordedAt           time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
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
	GameID         int64
	DivisionID     int64
	DivisionCode   string
	DivisionName   string
	Division       *Division
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
	GameID               int64
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

type QueryField struct {
	Key   string
	Value string
}

type TemplateData struct {
	Title                        string
	Description                  string
	CurrentPath                  string
	CurrentQueryFields           []QueryField
	User                         *User
	Viewer                       *User
	HideChrome                   bool
	CSRFToken                    string
	Flash                        string
	Error                        string
	Stats                        []Stat
	Features                     []Feature
	Users                        []User
	Available                    []string
	Divisions                    []Division
	ActiveDivisions              []Division
	AvailableDivisions           []Division
	SelectedDivision             *Division
	DivisionScopeOptions         []DivisionScopeOption
	SelectedDivisionScope        string
	Roles                        []Role
	Permissions                  []string
	PermissionGroups             []PermissionGroup
	Admissions                   []Admission
	AdmissionsTotal              int
	AdmissionsStart              int
	AdmissionsEnd                int
	AdmissionsTotalPages         int
	AdmissionsPageNumbers        []int
	AdmissionsHasPreviousPage    bool
	AdmissionsHasNextPage        bool
	AdmissionsPreviousPageURL    string
	AdmissionsNextPageURL        string
	AdmissionsPageBaseURL        string
	AdmissionsFilter             AdmissionsFilter
	SelectedAdmission            *Admission
	SelectedStaff                *User
	StaffIDCardQRCodeDataURI     string
	AdmissionMode                string
	StudentGroups                []StudentGroup
	SelectedGroup                *StudentGroup
	GroupMode                    string
	GroupSessions                []StudentGroupSession
	GroupSessionOccurrenceDate   string
	GroupSessionOccurrences      []StudentGroupSessionOccurrence
	SelectedGroupSessionID       int64
	AvailableCoaches             []User
	AvailableGroupStaff          []User
	GroupStaffRoles              []GroupStaffRoleOption
	StaffDirectoryRows           []StaffDirectoryRow
	SalaryProfiles               []StaffSalaryProfile
	StaffAdvances                []StaffAdvance
	PayrollRuns                  []PayrollRun
	PayrollPortfolioSummary      PayrollPortfolioSummary
	PayrollRunYears              []string
	SelectedPayrollStatus        string
	SelectedPayrollYear          string
	PayrollRun                   *PayrollRun
	PayrollPayments              []PayrollPayment
	PayrollPayment               *PayrollPayment
	PayrollFinanceTransaction    *FinanceTransaction
	Coaches                      []User
	CoachAttendanceRecords       []CoachAttendanceRecord
	StaffAttendanceUsers         []User
	StaffAttendanceRecords       []CoachAttendanceRecord
	StaffAttendanceReportRows    []StaffAttendanceReportRow
	StaffAttendanceReportSummary StaffAttendanceReportSummary
	StaffAttendanceHistory       []StaffAttendanceHistoryRow
	StaffAttendanceMonth         string
	SelectedStaffAttendanceUser  *User
	AttendanceRecords            []AttendanceRecord
	AttendanceDate               string
	RecentDates                  []string
	AttendanceSummary            AttendanceSummary
	AttendanceLimitWarnings      []AttendanceLimitWarning
	SelectedAttendanceStudent    *Admission
	StudentAttendanceHistory     []StudentAttendanceHistoryRow
	StudentAttendanceSummary     StudentAttendanceSummary

	// Monthly student attendance reporting.
	StudentAttendanceReportRows      []StudentAttendanceReportRow
	StudentAttendanceGroupReportRows []StudentAttendanceGroupReportRow
	StudentAttendanceReportSummary   StudentAttendanceReportSummary
	StudentAttendanceReportMonth     string
	StudentAttendanceReportGroupID   int64
	StudentAttendanceReportQuery     string
	AttendanceSheets                 []AttendanceSheetSummary
	AttendanceSearchStudentID        string
	AttendanceSearchMatches          []Admission
	AttendanceSearchNotFound         bool
	PublicStudentSearchStudentID     string
	PublicStudentSearchNotFound      bool
	PublicStudentPaymentHistory      []StudentMonthlyPayment
	Courts                           []Court
	SelectedCourt                    *Court
	CourtMode                        string
	CourtActivities                  []CourtActivity
	SelectedCourtActivity            *CourtActivity
	CourtActivityMode                string
	Games                            []Game
	SelectedGame                     *Game
	GameMode                         string
	CourtLayouts                     []CourtLayout
	SelectedCourtLayout              *CourtLayout
	CourtLayoutMode                  string
	CourtClosures                    []CourtClosure
	SelectedCourtClosure             *CourtClosure
	CourtClosureMode                 string
	Schedules                        []SpaceSchedule
	DaySchedules                     []SpaceSchedule
	PendingSchedules                 []SpaceSchedule
	BookingRequests                  []SpaceSchedule
	SelectedSchedule                 *SpaceSchedule
	DraftSchedule                    *SpaceSchedule
	ScheduleMode                     string
	Pricings                         []PricingRule
	TrainingPrograms                 []TrainingProgram
	SelectedTrainingProgram          *TrainingProgram
	TrainingProgramMode              string
	Enrollments                      []StudentEnrollment
	SelectedEnrollment               *StudentEnrollment
	EnrollmentMode                   string
	EnrollmentDeleteBlock            *EnrollmentDeleteBlock
	PricingSettings                  *PricingSettings
	OneToOneOfferings                []OneToOneOffering
	SelectedOneToOneOffering         *OneToOneOffering
	OneToOneMode                     string
	OneToOneBookings                 []OneToOneBooking
	SelectedOneToOneBooking          *OneToOneBooking
	OneToOneReceivables              []OneToOneReceivable
	OneToOneCoaches                  []User
	Tournaments                      []Tournament
	SelectedTournament               *Tournament
	ReferralPartners                 []ReferralPartner
	ReferralPartnerRows              []ReferralPartnerSummary
	BookingReferrals                 []BookingReferral
	BookingRequestChanges            []BookingRequestChange
	ReferralStats                    []Stat
	SelectedPricing                  *PricingRule
	PricingMode                      string
	Events                           []Event
	SelectedEvent                    *Event
	EventMode                        string
	FinanceTransactions              []FinanceTransaction
	FinanceTransactionsTotal         int
	FinanceCategories                []FinanceCategory
	FinanceAccounts                  []FinanceAccount
	FinancePeriodLock                *FinancePeriodLock
	FinanceTransfers                 []FinanceTransfer
	CashReconciliations              []CashReconciliation
	SelectedFinanceAccount           *FinanceAccount
	SelectedFinance                  *FinanceTransaction
	FinanceFilter                    FinanceFilter
	FinancePage                      string
	FinanceLedgerHasPreviousPage     bool
	FinanceLedgerHasNextPage         bool
	FinanceLedgerPreviousPageURL     string
	FinanceLedgerNextPageURL         string
	FinanceSummary                   FinanceSummary
	FinanceProfitAndLoss             *FinanceProfitAndLoss
	FinanceBalanceSheet              *FinanceBalanceSheet
	FinanceSpecifiedLedgers          []FinanceSpecifiedLedger
	SelectedFinanceSpecifiedLedger   *FinanceSpecifiedLedger
	FinanceSpecifiedLedgerFrom       string
	FinanceSpecifiedLedgerTo         string
	FinanceCustomerSearch            string
	FinanceReceivableSummaryCards    []FinanceReceivableSummaryCard
	FinanceReceivableOverviewRows    []FinanceReceivableOverviewRow
	StatementOpeningBalance          float64
	StatementClosingBalance          float64
	StatementMoneyIn                 float64
	StatementMoneyOut                float64
	StatementNetMovement             float64
	BookingFinancials                []BookingFinancial
	BookingCustomerBalances          []BookingCustomerBalance
	BookingPaymentCollections        []BookingPaymentCollection
	BookingCommunications            []BookingCommunication
	BookingAccessTokens              []BookingAccessToken
	BookingCancellationRequests      []BookingCancellationRequest
	SelectedAccessToken              *BookingAccessToken
	BookingStatusView                *BookingStatusView
	BookingStatusToken               string
	BookingStatusTimeline            []CustomerBookingTimelineItem
	BookingStatusUnavailable         bool
	BookingStatusUnavailableMessage  string
	Report                           *OperationalReport
	ReportCenter                     *ReportCenter
	ReceiptAdmission                 *Admission
	ReceiptEnrollment                *StudentEnrollment
	ReceiptBookingPayment            *BookingPaymentCollection
	ReceiptBookingSchedule           *SpaceSchedule
	ReceiptBookingFinancial          *BookingFinancial
	StudentPaymentRows               []StudentPaymentRow
	EnrollmentLeaves                 []StudentEnrollmentLeave
	PaymentMonth                     string
	PaymentMonthLabel                string
	PaymentCollectionOpen            bool
	PaymentCollectionNotice          string
	PaymentSearch                    string
	PaymentStatusFilter              string
	PaymentProgramFilter             string
	PaymentMethodFilter              string
	PaymentActivityFrom              string
	PaymentActivityTo                string
	PaymentProgramOptions            []string
	PaymentTotalDue                  float64
	PaymentCollected                 float64
	PaymentOutstanding               float64
	PaymentPaidCount                 int
	PaymentPartialCount              int
	PaymentFreeCount                 int
	PaymentUnconfiguredCount         int
	PaymentPendingCount              int
	PaymentActivityRows              []StudentMonthlyPaymentActivityRow
	PaymentActivityCollected         float64
	PaymentActivityDiscounted        float64
	PaymentActivityVoidedCount       int
	PaymentPDFOrientation            string
	PaymentPDFPaperSize              string
	PaymentPDFDensity                string
	PaymentPDFIncludeSummary         bool
	PaymentPDFIncludeRegister        bool
	PaymentPDFIncludeActivity        bool
	PaymentPDFIncludeFilters         bool
	PaymentPDFAutoPrint              bool
	BookingSlots                     []BookingSlotAvailability
	AdminCalendarHours               []AdminCalendarHour
	AdminBookingOptions              []AdminBookingOption
	AdminBookingBlockedReason        string
	SetupWarnings                    []SetupWarning
	SMSGateway                       *SMSGatewayAdminView
	WeekDays                         []CalendarDay
	BookingOptions                   []BookingOption
	Activities                       []string
	Hours                            []string
	BookingDurationHours             int
	CalendarDate                     string
	PreviousDate                     string
	NextDate                         string
	TodayDate                        string
	HistoricalStartDate              string
	SelectedCoach                    *User
	DailyStats                       []Stat
	BookingRequestStats              []Stat
	PendingRequestCount              int
	HeldRequestCount                 int
	BookingReminders                 []BookingReminder
	BookingAttentionStats            []Stat
	BookingRequestFilterStatus       string
	BookingRequestSearch             string
	CalendarCanGoBack                bool
	PendingEmail                     string
	OTPCodeLength                    int
	ResendAction                     string
	SportsCatalog                    []SportPage
	SelectedSport                    *SportPage
	FAQItems                         []FAQItem
	MCPCustomers                     []MCPMonthlyCustomer
	SelectedMCPCustomer              *MCPMonthlyCustomer
	MCPPlans                         []MCPMonthlyPlan
	SelectedMCPPlan                  *MCPMonthlyPlan
	MCPPricingBands                  []MCPPricingBand
	SelectedMCPPricingBand           *MCPPricingBand
	MCPReceivables                   []MCPReceivable
	MCPPreview                       *MCPPlanPreview
	MCPConflicts                     []MCPPlanConflict
	MCPPage                          string
	MCPPortal                        bool
	MCPSelectedMonth                 string
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
	ErrStudentPaymentDiscountInvalid     = errors.New("discount amount must be greater than zero and cannot exceed the outstanding balance")
	ErrStudentPaymentDiscountReason      = errors.New("enter a reason for the discount")
	ErrStudentPaymentAmountExceedsDue    = errors.New("payment amount exceeds the outstanding balance")
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
