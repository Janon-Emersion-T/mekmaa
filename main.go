package main

import (
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"strings"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	deps, err := loadRuntimeDependencies()
	if err != nil {
		log.Fatal(err)
	}
	for _, errMsg := range deps.ConfigurationErrors {
		log.Printf("startup configuration error: %s", errMsg)
	}
	if envOrDefault("BOOKING_SMS_ENABLED", "false") == "true" && !deps.SMSConfig.Enabled {
		log.Printf("startup booking SMS disabled: SMS credentials are incomplete")
	}
	if deps.AppEnv == appEnvProduction && len(deps.ConfigurationErrors) > 0 {
		log.Fatal("production startup validation failed")
	}

	db, err := openConfiguredDatabase(DatabaseConfig{
		Driver: deps.RuntimeConfig.DBDriver,
		URL:    deps.RuntimeConfig.DatabaseURL,
		Path:   deps.RuntimeConfig.DBPath,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := applyBootstrapDataForDatabase(
		db,
		deps.RuntimeConfig.DBDriver,
	); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "seed-uat":
			if err := runSeedUAT(db, deps.AppEnv); err != nil {
				log.Fatal(err)
			}
			return
		default:
			log.Fatalf("unknown command: %s", os.Args[1])
		}
	}

	templates, err := buildTemplates()
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	app := &App{
		db:              db,
		templates:       templates,
		cookieSecure:    deps.CookieSecure,
		smtp:            deps.SMTPConfig,
		sms:             deps.SMSConfig,
		uploads:         deps.UploadStorage,
		bookingMessages: deps.BookingMessages,
		bookingAccess:   deps.BookingAccess,
		runtimeConfig:   deps.RuntimeConfig,
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
		"startup summary: env=%s addr=%s db_driver=%s db_path=%s upload_path=%s public_base_url=%s cookie_secure=%t booking_email_enabled=%t booking_sms_enabled=%t active_unpriced_booking_options=%d",
		deps.RuntimeConfig.Env,
		deps.RuntimeConfig.Addr,
		deps.RuntimeConfig.DBDriver,
		deps.RuntimeConfig.DBPath,
		deps.RuntimeConfig.UploadRoot,
		deps.RuntimeConfig.PublicBaseURL,
		deps.RuntimeConfig.CookieSecure,
		deps.BookingMessages.EmailEnabled,
		deps.BookingMessages.SMSEnabled,
		len(unpricedOptions),
	)

	mux := http.NewServeMux()
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir("static/images"))))
	registerUploadRoutes(mux, deps.UploadStorage)
	mux.HandleFunc("/health", app.healthHandler)
	mux.HandleFunc("/ready", app.readyHandler)
	mux.HandleFunc("/", app.homeHandler)
	mux.HandleFunc("/about", app.aboutHandler)
	mux.HandleFunc("/book", app.publicBookingHandler)
	mux.HandleFunc("/book/request", app.publicBookingRequestHandler)
	mux.HandleFunc("/booking/status", app.publicBookingStatusHandler)
	mux.HandleFunc("/b", app.publicBookingStatusShortHandler)
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
	mux.HandleFunc("/mystudent", app.publicStudentHandler)
	mux.HandleFunc("/register", app.registerHandler)
	mux.HandleFunc("/login", app.loginHandler)
	mux.HandleFunc("/sports", app.sportsHandler)
	mux.HandleFunc("/sports/", app.sportDetailHandler)
	mux.HandleFunc("/terms-and-conditions", app.termsHandler)
	mux.HandleFunc("/verify-email", app.verifyEmailHandler)
	mux.HandleFunc("/verify-email/resend", app.resendVerificationHandler)
	mux.HandleFunc("/logout", app.logoutHandler)
	mux.HandleFunc("/customer/login", app.customerMCPLoginHandler)
	mux.Handle("/customer/mcp", app.sessionMiddleware(http.HandlerFunc(app.customerMCPRouter)))
	mux.Handle("/customer/mcp/", app.sessionMiddleware(http.HandlerFunc(app.customerMCPRouter)))
	mux.Handle("/dashboard", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.dashboardHandler), "dashboard.view")))
	mux.Handle("/editor", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.editorHandler), "editor.access")))
	mux.Handle("/admin", app.sessionMiddleware(http.HandlerFunc(app.adminRedirectHandler)))
	mux.Handle("/admin/users", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.userManagementHandler), "users.view")))
	mux.Handle("/admin/roles", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.roleManagementHandler), "roles.view")))
	mux.Handle("/admin/sms-gateway", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.smsGatewayManagementHandler), "users.update")))
	mux.Handle("/admin/sms-gateway/test", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.smsGatewayTestHandler), "users.update")))
	mux.Handle("/admin/users/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createManagedUserHandler), "users.create")))
	mux.Handle("/admin/users/roles", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateRolesHandler), "users.update")))
	mux.Handle("/admin/roles/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createRoleHandler), "roles.create")))
	mux.Handle("/admin/roles/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateRoleHandler), "roles.update")))
	mux.Handle("/admin/roles/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteRoleHandler), "roles.delete")))
	mux.Handle("/admin/admissions", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.admissionManagementHandler), "admissions.view")))
	mux.Handle("/admin/students", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.admissionManagementHandler), "admissions.view")))
	mux.Handle("/admin/admissions/student-id", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentIDCardHandler), "admissions.view")))
	mux.Handle("/admin/students/student-id", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentIDCardHandler), "admissions.view")))
	mux.Handle("/admin/student-leaves", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentLeaveManagementHandler), "student_leaves.view")))
	mux.Handle("/admin/admissions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createAdmissionHandler), "admissions.create")))
	mux.Handle("/admin/students/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createAdmissionHandler), "admissions.create")))
	mux.Handle("/admin/admissions/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateAdmissionHandler), "admissions.update")))
	mux.Handle("/admin/students/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateAdmissionHandler), "admissions.update")))
	mux.Handle("/admin/admissions/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteAdmissionHandler), "admissions.delete")))
	mux.Handle("/admin/students/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteAdmissionHandler), "admissions.delete")))
	mux.Handle("/admin/enrollments", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.enrollmentManagementHandler), "enrollments.view")))
	mux.Handle("/admin/enrollments/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createEnrollmentHandler), "enrollments.create")))
	mux.Handle("/admin/enrollments/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateEnrollmentHandler), "enrollments.update")))
	mux.Handle("/admin/enrollments/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteEnrollmentHandler), "enrollments.delete")))
	mux.Handle("/admin/enrollments/collect-admission", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectEnrollmentAdmissionPaymentHandler), "enrollments.update")))
	mux.Handle("/admin/staff", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.staffDirectoryHandler), "coaches.view")))
	mux.Handle("/admin/payroll", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payrollManagementHandler), "payroll.view")))
	mux.Handle("/admin/payroll/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createPayrollRunHandler), "payroll.create")))
	mux.Handle("/admin/payroll/run", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payrollRunHandler), "payroll.view")))
	mux.Handle("/admin/payroll/generate", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.generatePayrollRunHandler), "payroll.create")))
	mux.Handle("/admin/payroll/quantity", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updatePayrollQuantityHandler), "payroll.update")))
	mux.Handle("/admin/payroll/adjustment/add", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.addPayrollAdjustmentHandler), "payroll.create")))
	mux.Handle("/admin/payroll/adjustment/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deletePayrollAdjustmentHandler), "payroll.delete")))
	mux.Handle("/admin/payroll/approve", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.approvePayrollRunHandler), "payroll.update")))
	mux.Handle("/admin/payroll/pay", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payPayrollPaymentHandler), "payroll.update")))
	mux.Handle("/admin/payroll/slip", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.payrollSalarySlipHandler), "payroll.view")))
	mux.Handle("/admin/staff/salary-profiles", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.salaryProfileManagementHandler), "payroll.view")))
	mux.Handle("/admin/staff/salary-profiles/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createSalaryProfileHandler), "payroll.create")))
	mux.Handle("/admin/staff/salary-profiles/toggle", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.toggleSalaryProfileHandler), "payroll.update")))
	mux.Handle("/admin/staff/attendance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.staffAttendanceManagementHandler), "coaches.view")))
	mux.Handle("/admin/staff/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveStaffAttendanceHandler), "coaches.update")))
	mux.Handle("/admin/staff/attendance/report", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.staffAttendanceReportHandler), "coaches.view")))
	mux.Handle("/admin/coaches", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.coachManagementHandler), "coaches.view")))
	mux.Handle("/admin/coaches/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createCoachHandler), "coaches.create")))
	mux.Handle("/admin/coaches/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateCoachHandler), "coaches.update")))
	mux.Handle("/admin/coaches/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteCoachHandler), "coaches.delete")))
	mux.Handle("/admin/coaches/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveCoachAttendanceHandler), "coaches.update")))
	mux.Handle(
		"/admin/training-programs",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.trainingProgramManagementHandler),
				"training_programs.view",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/create",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.createTrainingProgramHandler),
				"training_programs.create",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/update",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.updateTrainingProgramHandler),
				"training_programs.update",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/toggle",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.toggleTrainingProgramHandler),
				"training_programs.update",
			),
		),
	)

	mux.Handle(
		"/admin/training-programs/delete",
		app.sessionMiddleware(
			app.requirePermission(
				http.HandlerFunc(app.deleteTrainingProgramHandler),
				"training_programs.delete",
			),
		),
	)
	mux.Handle("/admin/student-groups", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentGroupManagementHandler), "student_groups.view")))
	mux.Handle("/admin/training-groups", app.sessionMiddleware(app.requirePermission(app.studentGroupFriendlyHandler(divisionCodeSports), "student_groups.view")))
	mux.Handle("/admin/classes", app.sessionMiddleware(app.requirePermission(app.studentGroupFriendlyHandler(divisionCodeKEC), "student_groups.view")))
	mux.Handle("/admin/batches", app.sessionMiddleware(app.requirePermission(app.studentGroupFriendlyHandler(divisionCodeChess), "student_groups.view")))
	mux.Handle("/admin/student-groups/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createStudentGroupHandler), "student_groups.create")))
	mux.Handle("/admin/student-groups/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateStudentGroupHandler), "student_groups.update")))
	mux.Handle("/admin/student-groups/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteStudentGroupHandler), "student_groups.delete")))
	mux.Handle("/admin/attendance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.attendanceManagementHandler), "attendance.view")))
	mux.Handle("/admin/attendance/search", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.attendanceSearchHandler), "attendance.view")))
	mux.Handle("/admin/attendance/report", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentAttendanceReportHandler), "attendance.view")))
	mux.Handle("/admin/attendance/save", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.saveAttendanceHandler), "attendance.update")))
	mux.Handle(
		"/admin/courts",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.courtManagementHandler),
					"courts.view",
				),
			),
		),
	)
	mux.Handle(
		"/admin/courts/create",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.createCourtHandler),
					"courts.create",
				),
			),
		),
	)
	mux.Handle(
		"/admin/courts/update",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.updateCourtHandler),
					"courts.update",
				),
			),
		),
	)
	mux.Handle(
		"/admin/courts/layouts/create",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.createCourtLayoutHandler),
					"courts.update",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/update",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.updateCourtLayoutHandler),
					"courts.update",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/toggle",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.toggleCourtLayoutHandler),
					"courts.update",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/layouts/delete",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.deleteCourtLayoutHandler),
					"courts.delete",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/create",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.createCourtClosureHandler),
					"courts.create",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/update",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.updateCourtClosureHandler),
					"courts.update",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/toggle",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.toggleCourtClosureHandler),
					"courts.update",
				),
			),
		),
	)

	mux.Handle(
		"/admin/courts/closures/delete",
		app.sessionMiddleware(
			app.requireSportsOperationalAccess(
				app.requirePermission(
					http.HandlerFunc(app.deleteCourtClosureHandler),
					"courts.delete",
				),
			),
		),
	)
	mux.Handle("/admin/courts/activities/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createCourtActivityHandler), "courts.create"))))
	mux.Handle("/admin/courts/activities/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateCourtActivityHandler), "courts.update"))))
	mux.Handle("/admin/courts/activities/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deleteCourtActivityHandler), "courts.delete"))))
	mux.Handle("/admin/courts/activities/auto-accept", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateCourtActivityAutoAcceptHandler), "courts.update"))))
	mux.Handle("/admin/courts/activities/game", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateCourtActivityGameHandler), "courts.update"))))
	mux.Handle("/admin/bookings", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.bookingManagementHandler), "space_bookings.view"))))
	mux.Handle("/admin/games", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.gameManagementHandler), "games.view"))))
	mux.Handle("/admin/games/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createGameHandler), "games.create"))))
	mux.Handle("/admin/games/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateGameHandler), "games.update"))))
	mux.Handle("/admin/games/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deleteGameHandler), "games.delete"))))
	mux.Handle("/admin/one-to-one", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.oneToOneManagementHandler), "one_to_one.view"))))
	mux.Handle("/admin/one-to-one/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createOneToOneOfferingHandler), "one_to_one.create"))))
	mux.Handle("/admin/one-to-one/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateOneToOneOfferingHandler), "one_to_one.update"))))
	mux.Handle("/admin/one-to-one/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deleteOneToOneOfferingHandler), "one_to_one.delete"))))
	mux.Handle("/admin/one-to-one-bookings", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.oneToOneBookingManagementHandler), "one_to_one_bookings.view"))))
	mux.Handle("/admin/one-to-one-bookings/view", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.oneToOneBookingDetailHandler), "one_to_one_bookings.view"))))
	mux.Handle("/admin/one-to-one-bookings/export", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.oneToOneBookingExportHandler), "one_to_one_bookings.view"))))
	mux.Handle("/admin/one-to-one-receivables", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.oneToOneReceivablesHandler), "finance.view", "one_to_one_bookings.view"))))
	mux.Handle("/admin/tournaments", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.tournamentManagementHandler), "tournaments.view"))))
	mux.Handle("/admin/tournaments/view", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.tournamentDetailHandler), "tournaments.view"))))
	mux.Handle("/admin/tournaments/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createTournamentHandler), "tournaments.create"))))
	mux.Handle("/admin/tournaments/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateTournamentHandler), "tournaments.update"))))
	mux.Handle("/admin/tournaments/sponsorships/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createTournamentSponsorshipHandler), "tournaments.create"))))
	mux.Handle("/admin/tournaments/official-payments/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createTournamentOfficialPaymentHandler), "tournaments.create"))))
	mux.Handle("/admin/tournaments/expenses/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createTournamentExpenseHandler), "tournaments.create"))))
	mux.Handle("/admin/one-to-one-bookings/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createOneToOneBookingHandler), "one_to_one_bookings.create"))))
	mux.Handle("/admin/one-to-one-bookings/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateOneToOneBookingHandler), "one_to_one_bookings.update"))))
	mux.Handle("/admin/one-to-one-bookings/sessions/schedule", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.scheduleNextOneToOneSessionHandler), "one_to_one_bookings.update"))))
	mux.Handle("/admin/one-to-one-bookings/sessions/complete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.completeOneToOneSessionHandler), "one_to_one_bookings.update"))))
	mux.Handle("/admin/one-to-one-bookings/sessions/cancel", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.cancelOneToOneSessionHandler), "one_to_one_bookings.update"))))
	mux.Handle("/admin/one-to-one-bookings/sessions/attendance", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.saveOneToOneSessionAttendanceHandler), "one_to_one_bookings.update"))))
	mux.Handle("/admin/one-to-one-bookings/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deleteOneToOneBookingHandler), "one_to_one_bookings.delete"))))
	mux.Handle("/admin/bookings/options", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.adminBookingOptionsHandler), "space_bookings.view"))))
	mux.Handle("/admin/mcp", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.adminMCPManagementHandler), "mcp.view"))))
	mux.Handle("/admin/mcp/pricing", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.adminMCPPricingHandler), "mcp_pricing.view"))))
	mux.Handle("/admin/mcp-receivables", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.adminMCPReceivablesHandler), "mcp_receivables.view"))))
	mux.Handle("/admin/bookings/communications/resend", app.sessionMiddleware(app.requireSportsOperationalAccess(http.HandlerFunc(app.resendBookingCommunicationHandler))))
	mux.Handle("/admin/bookings/access/rotate", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.rotateBookingAccessHandler), "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/access/revoke", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.revokeBookingAccessHandler), "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/cancel", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.cancelBookingHandler), "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/complete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.completeBookingHandler), "space_bookings.update"))))
	mux.Handle("/admin/bookings/no-show", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.noShowBookingHandler), "space_bookings.update"))))
	mux.Handle("/admin/bookings/cancellation-requests/approve", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.approveBookingCancellationRequestHandler), "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/cancellation-requests/reject", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.rejectBookingCancellationRequestHandler), "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/payments/collect", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.collectBookingPaymentHandler), "finance_transactions.create", "space_bookings.update", "booking_requests.update"))))
	mux.Handle("/admin/bookings/payments/void", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.voidBookingPaymentHandler), "finance_transactions.delete"))))
	mux.Handle("/admin/bookings/payments/receipt", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requireAnyPermission(http.HandlerFunc(app.financeReceiptHandler), "admissions.view", "finance_transactions.view", "space_bookings.view", "booking_requests.view"))))
	mux.Handle("/admin/admissions/payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidAdmissionPaymentHandler), "finance_transactions.delete")))
	mux.Handle("/admin/bookings/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createBookingHandler), "space_bookings.create"))))
	mux.Handle("/admin/bookings/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateBookingHandler), "space_bookings.update"))))
	mux.Handle("/admin/bookings/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deleteBookingHandler), "space_bookings.delete"))))
	mux.Handle("/admin/booking-requests", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.bookingRequestsHandler), "booking_requests.view"))))
	mux.Handle("/admin/booking-requests/hold", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.holdBookingRequestHandler), "booking_requests.update"))))
	mux.Handle("/admin/booking-requests/reschedule", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.rescheduleBookingRequestHandler), "booking_requests.update"))))
	mux.Handle("/admin/booking-requests/reschedule-confirm", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.rescheduleAndConfirmBookingRequestHandler), "booking_requests.update"))))
	mux.Handle("/admin/booking-requests/confirm", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.confirmBookingRequestHandler), "booking_requests.update"))))
	mux.Handle("/admin/booking-requests/reject", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.rejectBookingRequestHandler), "booking_requests.update"))))
	mux.Handle("/admin/pricing", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.pricingManagementHandler), "pricing.view"))))
	mux.Handle("/admin/pricing/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createPricingHandler), "pricing.create"))))
	mux.Handle("/admin/pricing/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updatePricingHandler), "pricing.update"))))
	mux.Handle("/admin/pricing/delete", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.deletePricingHandler), "pricing.delete"))))
	mux.Handle("/admin/pricing/settings", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updatePricingSettingsHandler), "pricing.update"))))
	mux.Handle("/admin/events", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.eventManagementHandler), "events.view")))
	mux.Handle("/admin/events/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createEventHandler), "events.create")))
	mux.Handle("/admin/events/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateEventHandler), "events.update")))
	mux.Handle("/admin/events/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteEventHandler), "events.delete")))
	mux.Handle("/admin/finance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeManagementHandler), "finance.view")))
	mux.Handle("/admin/finance/ledger", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeLedgerHandler), "finance.view")))
	mux.Handle("/admin/finance/specified-ledgers", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeSpecifiedLedgersHandler), "finance.view")))
	mux.Handle("/admin/finance/specified-ledgers/", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeSpecifiedLedgerDetailHandler), "finance.view")))
	mux.Handle("/admin/finance/profit-and-loss", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeProfitAndLossHandler), "finance.view")))
	mux.Handle("/admin/finance/balance-sheet", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeBalanceSheetHandler), "finance.view")))
	mux.Handle("/admin/finance/receivables", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeReceivablesHandler), "finance.view")))
	mux.Handle("/admin/finance/customers", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeCustomersHandler), "finance.view")))
	mux.Handle("/admin/finance/transfers", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeTransfersHandler), "finance_transfers.view")))
	mux.Handle("/admin/finance/reconciliations", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeReconciliationsHandler), "finance_reconciliations.view")))
	mux.Handle("/admin/finance/accounts", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeAccountsHandler), "finance_accounts.view")))
	mux.Handle("/admin/finance/categories", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeCategoriesHandler), "finance_categories.view")))
	mux.Handle("/admin/finance/transactions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceTransactionHandler), "finance_transactions.create")))
	mux.Handle("/admin/finance/transactions/approve", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.approveFinanceTransactionHandler), "finance_transactions.update")))
	mux.Handle("/admin/finance/transactions/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidFinanceTransactionHandler), "finance_transactions.delete")))
	mux.Handle("/admin/finance/period-lock", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateFinancePeriodLockHandler), "finance.update")))
	mux.Handle("/admin/finance/categories/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceCategoryHandler), "finance_categories.create")))
	mux.Handle("/admin/finance/categories/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateFinanceCategoryHandler), "finance_categories.update")))
	mux.Handle("/admin/finance/categories/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteFinanceCategoryHandler), "finance_categories.delete")))
	mux.Handle("/admin/finance/transfers/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceTransferHandler), "finance_transfers.create")))
	mux.Handle("/admin/finance/transfers/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidFinanceTransferHandler), "finance_transfers.delete")))
	mux.Handle("/admin/finance/accounts/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceAccountHandler), "finance_accounts.create")))
	mux.Handle("/admin/finance/accounts/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateFinanceAccountHandler), "finance_accounts.update")))
	mux.Handle("/admin/finance/accounts/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteFinanceAccountHandler), "finance_accounts.delete")))
	mux.Handle("/admin/finance/accounts/opening-balance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceOpeningBalanceHandler), "finance_accounts.create")))
	mux.Handle("/admin/finance/accounts/adjustment", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createFinanceAdjustmentHandler), "finance_accounts.update")))
	mux.Handle("/admin/finance/accounts/statement", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeAccountStatementHandler), "finance_accounts.view")))
	mux.Handle("/admin/finance/reconciliations/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createCashReconciliationHandler), "finance_reconciliations.create")))
	mux.Handle("/admin/finance/reconciliations/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidCashReconciliationHandler), "finance_reconciliations.delete")))
	mux.Handle("/admin/finance/bookings/collect", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.collectBookingPaymentHandler), "finance_transactions.create", "space_bookings.update", "booking_requests.update")))
	mux.Handle("/admin/finance/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.financeExportHandler), "finance.view")))
	mux.Handle("/admin/reports", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsHandler), "reports.view")))
	mux.Handle("/admin/reports/export", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.reportsExportHandler), "reports.export")))
	mux.Handle("/admin/referrals", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.referralCommissionsHandler), "referrals.view"))))
	mux.Handle("/admin/referrals/settings", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateReferralSettingsHandler), "referrals.update"))))
	mux.Handle("/admin/referrals/partners/create", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.createReferralPartnerHandler), "referrals.create"))))
	mux.Handle("/admin/referrals/partners/update", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.updateReferralPartnerHandler), "referrals.update"))))
	mux.Handle("/admin/referrals/partners/toggle", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.toggleReferralPartnerHandler), "referrals.update"))))
	mux.Handle("/admin/referrals/pay", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.payReferralCommissionHandler), "referrals.update"))))
	mux.Handle("/admin/referrals/payments/void", app.sessionMiddleware(app.requireSportsOperationalAccess(app.requirePermission(http.HandlerFunc(app.voidReferralCommissionPaymentHandler), "referrals.delete"))))
	mux.Handle("/admin/student-payments", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.studentPaymentsHandler), "student_payments.view")))
	mux.Handle("/admin/student-payments/collect", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.collectStudentPaymentHandler), "student_payments.create")))
	mux.Handle("/admin/student-leaves/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createStudentEnrollmentLeaveHandler), "student_leaves.create")))
	mux.Handle("/admin/student-leaves/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteStudentEnrollmentLeaveHandler), "student_leaves.delete")))
	mux.Handle("/admin/student-payments/void", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.voidStudentPaymentHandler), "student_payments.delete")))
	mux.Handle("/admin/finance/receipt", app.sessionMiddleware(app.requireAnyPermission(http.HandlerFunc(app.financeReceiptHandler), "admissions.view", "finance_transactions.view", "space_bookings.view", "booking_requests.view")))

	log.Printf("server listening on %s", deps.RuntimeConfig.Addr)
	if err := http.ListenAndServe(deps.RuntimeConfig.Addr, app.securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}
