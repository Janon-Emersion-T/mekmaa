package main

import (
	"database/sql"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	appEnv, err := parseAppEnvironment(os.Getenv("APP_ENV"))
	if err != nil {
		log.Fatal(err)
	}
	addr := envOrDefault("ADDR", ":8080")
	dbPath, dbPathErrs := validateDatabasePath(appEnv, os.Getenv("DB_PATH"))
	for _, errMsg := range dbPathErrs {
		log.Printf("startup configuration error: %s", errMsg)
	}
	if err := prepareDatabasePath(dbPath); err != nil {
		log.Fatalf("prepare database path: %v", err)
	}
	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"
	uploadStorage, err := prepareUploadStorage(os.Getenv("UPLOAD_DIR"))
	if err != nil {
		log.Fatalf("prepare upload storage: %v", err)
	}

	smtpConfig := SMTPConfig{
		Host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		Port:     envOrDefault("SMTP_PORT", "587"),
		Username: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASS"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
	}
	if smtpConfig.From == "" {
		smtpConfig.From = smtpConfig.Username
	}
	smtpConfig.Enabled = smtpConfig.Username != "" && smtpConfig.Password != "" && smtpConfig.From != ""

	smsConfig := SMSConfig{
		UserID:   envValue("SMS_USER_ID", "SMSLENZ_USER_ID"),
		APIKey:   envValue("SMS_API_KEY", "SMSLENZ_API_KEY"),
		SenderID: envValue("SMS_SENDER_ID", "SMSLENZ_SENDER_ID"),
	}
	smsConfig.Enabled = smsConfig.UserID != "" && smsConfig.APIKey != "" && smsConfig.SenderID != ""

	bookingMessageSettings := BookingCommunicationSettings{
		EmailEnabled: envOrDefault("BOOKING_EMAIL_ENABLED", "true") != "false",
		SMSEnabled:   envOrDefault("BOOKING_SMS_ENABLED", "false") == "true" && smsConfig.Enabled,
		ContactPhone: envOrDefault("MEKMAA_CONTACT_PHONE", "077 220 7297"),
		ContactEmail: envOrDefault("MEKMAA_CONTACT_EMAIL", "mekmaa.jo@gmail.com"),
		VenueName:    envOrDefault("MEKMAA_VENUE_NAME", "Mekmaa (Private Limited)"),
		VenueAddress: envOrDefault("MEKMAA_VENUE_ADDRESS", "No. 64, Temple Road, Jaffna - 40000, Sri Lanka"),
	}
	if envOrDefault("BOOKING_SMS_ENABLED", "false") == "true" && !smsConfig.Enabled {
		log.Printf("startup booking SMS disabled: SMS credentials are incomplete")
	}
	tokenTTLDays, err := strconv.Atoi(envOrDefault("BOOKING_ACCESS_TOKEN_TTL_DAYS", "180"))
	if err != nil || tokenTTLDays <= 0 {
		tokenTTLDays = 180
	}
	bookingAccessSettings := BookingAccessSettings{
		BaseURL:     envOrDefault("MEKMAA_PUBLIC_BASE_URL", "http://localhost:8080"),
		TokenSecret: envOrDefault("BOOKING_ACCESS_TOKEN_SECRET", defaultBookingAccessTokenSecret),
		TokenTTL:    time.Duration(tokenTTLDays) * 24 * time.Hour,
	}
	runtimeConfig := AppRuntimeConfig{
		Env:           appEnv,
		Addr:          addr,
		DBPath:        dbPath,
		UploadRoot:    uploadStorage.Root,
		PublicBaseURL: bookingAccessSettings.BaseURL,
		CookieSecure:  cookieSecure,
	}

	configErrs := validateRuntimeConfiguration(runtimeConfig, bookingMessageSettings, bookingAccessSettings, smtpConfig, smsConfig)
	configErrs = append(configErrs, validateUploadPath(appEnv, uploadStorage.Root)...)
	configErrs = append(configErrs, dbPathErrs...)
	for _, errMsg := range configErrs {
		log.Printf("startup configuration error: %s", errMsg)
	}
	if appEnv == appEnvProduction && len(configErrs) > 0 {
		log.Fatal("production startup validation failed")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := enableSQLiteForeignKeys(db); err != nil {
		log.Fatalf("enable sqlite foreign keys: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("ping database: %v", err)
	}

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
		bookingAccess:   bookingAccessSettings,
		runtimeConfig:   runtimeConfig,
	}
	unpricedOptions, err := app.listActiveUnpricedBookingOptions()
	if err != nil {
		log.Printf("startup pricing validation warning: could not inspect booking pricing readiness")
	} else if len(unpricedOptions) > 0 {
		labels := make([]string, 0, len(unpricedOptions))
		for _, issue := range unpricedOptions {
			labels = append(labels, issue.Label)
		}
		log.Printf("startup booking pricing warning: %d active booking options are unpriced (%s)", len(unpricedOptions), strings.Join(labels, ", "))
	}
	log.Printf(
		"startup summary: env=%s addr=%s db_path=%s upload_path=%s public_base_url=%s cookie_secure=%t booking_email_enabled=%t booking_sms_enabled=%t active_unpriced_booking_options=%d",
		runtimeConfig.Env,
		runtimeConfig.Addr,
		runtimeConfig.DBPath,
		runtimeConfig.UploadRoot,
		runtimeConfig.PublicBaseURL,
		runtimeConfig.CookieSecure,
		bookingMessageSettings.EmailEnabled,
		bookingMessageSettings.SMSEnabled,
		len(unpricedOptions),
	)

	mux := http.NewServeMux()
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir("static/images"))))
	registerUploadRoutes(mux, uploadStorage)
	mux.HandleFunc("/health", app.healthHandler)
	mux.HandleFunc("/ready", app.readyHandler)
	mux.HandleFunc("/", app.homeHandler)
	mux.HandleFunc("/about", app.aboutHandler)
	mux.HandleFunc("/book", app.publicBookingHandler)
	mux.HandleFunc("/book/request", app.publicBookingRequestHandler)
	mux.HandleFunc("/booking/status", app.publicBookingStatusHandler)
	mux.HandleFunc("/booking/status/cancellation-request", app.publicBookingCancellationRequestHandler)
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
	mux.Handle("/admin/enrollments", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.enrollmentManagementHandler), "admissions.manage")))
	mux.Handle("/admin/enrollments/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createEnrollmentHandler), "admissions.manage")))
	mux.Handle("/admin/enrollments/collect-admission", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectEnrollmentAdmissionPaymentHandler), "admissions.manage")))
	mux.Handle("/admin/coaches", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.coachManagementHandler), "coaches.manage")))
	mux.Handle("/admin/coaches/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createCoachHandler), "coaches.manage")))
	mux.Handle("/admin/coaches/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateCoachHandler), "coaches.manage")))
	mux.Handle("/admin/coaches/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteCoachHandler), "coaches.manage")))
	mux.Handle("/admin/coaches/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveCoachAttendanceHandler), "coaches.manage")))
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
	mux.Handle("/admin/courts/activities/auto-accept", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateCourtActivityAutoAcceptHandler), "courts.manage")))
	mux.Handle("/admin/bookings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingManagementHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/options", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.adminBookingOptionsHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/communications/resend", app.sessionMiddleware(http.HandlerFunc(app.resendBookingCommunicationHandler)))
	mux.Handle("/admin/bookings/access/rotate", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.rotateBookingAccessHandler), "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/access/revoke", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.revokeBookingAccessHandler), "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/cancel", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.cancelBookingHandler), "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/complete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.completeBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/no-show", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.noShowBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/cancellation-requests/approve", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.approveBookingCancellationRequestHandler), "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/cancellation-requests/reject", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.rejectBookingCancellationRequestHandler), "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/payments/collect", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.collectBookingPaymentHandler), "finance.manage", "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/bookings/payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidBookingPaymentHandler), "finance.manage")))
	mux.Handle("/admin/bookings/payments/receipt", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.financeReceiptHandler), "admissions.manage", "finance.manage", "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/admissions/payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidAdmissionPaymentHandler), "finance.manage")))
	mux.Handle("/admin/bookings/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/bookings/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteBookingHandler), "space_bookings.manage")))
	mux.Handle("/admin/booking-requests", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.bookingRequestsHandler), "booking_requests.manage")))
	mux.Handle("/admin/booking-requests/hold", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.holdBookingRequestHandler), "booking_requests.manage")))
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
	mux.Handle("/admin/finance/ledger", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeLedgerHandler), "finance.manage")))
	mux.Handle("/admin/finance/receivables", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeReceivablesHandler), "finance.manage")))
	mux.Handle("/admin/finance/transfers", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeTransfersHandler), "finance.manage")))
	mux.Handle("/admin/finance/reconciliations", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeReconciliationsHandler), "finance.manage")))
	mux.Handle("/admin/finance/accounts", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeAccountsHandler), "finance.manage")))
	mux.Handle("/admin/finance/transactions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceTransactionHandler), "finance.manage")))
	mux.Handle("/admin/finance/transactions/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidFinanceTransactionHandler), "finance.manage")))
	mux.Handle("/admin/finance/transfers/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceTransferHandler), "finance.manage")))
	mux.Handle("/admin/finance/transfers/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidFinanceTransferHandler), "finance.manage")))
	mux.Handle("/admin/finance/accounts/opening-balance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceOpeningBalanceHandler), "finance.manage")))
	mux.Handle("/admin/finance/accounts/adjustment", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceAdjustmentHandler), "finance.manage")))
	mux.Handle("/admin/finance/accounts/statement", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeAccountStatementHandler), "finance.manage")))
	mux.Handle("/admin/finance/reconciliations/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createCashReconciliationHandler), "finance.manage")))
	mux.Handle("/admin/finance/reconciliations/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidCashReconciliationHandler), "finance.manage")))
	mux.Handle("/admin/finance/bookings/collect", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.collectBookingPaymentHandler), "finance.manage", "space_bookings.manage", "booking_requests.manage")))
	mux.Handle("/admin/finance/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeExportHandler), "finance.manage")))
	mux.Handle("/admin/reports", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsHandler), "reports.view")))
	mux.Handle("/admin/reports/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsExportHandler), "reports.view")))
	mux.Handle("/admin/referrals", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.referralCommissionsHandler), "finance.manage")))
	mux.Handle("/admin/referrals/settings", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateReferralSettingsHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/partners/toggle", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.toggleReferralPartnerHandler), "finance.manage")))
	mux.Handle("/admin/referrals/pay", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payReferralCommissionHandler), "finance.manage")))
	mux.Handle("/admin/referrals/payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidReferralCommissionPaymentHandler), "finance.manage")))
	mux.Handle("/admin/student-payments", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentPaymentsHandler), "finance.manage")))
	mux.Handle("/admin/student-payments/collect", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectStudentPaymentHandler), "finance.manage")))
	mux.Handle("/admin/student-payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidStudentPaymentHandler), "finance.manage")))
	mux.Handle("/admin/finance/receipt", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.financeReceiptHandler), "admissions.manage", "finance.manage", "space_bookings.manage", "booking_requests.manage")))

	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, app.securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}
