package main

import (
	"errors"
	"html/template"
	"os"
	"strings"
)

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
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN photo_path") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN qr_code_path") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN qr_code_value") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN payment_collected") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN payment_collected_at") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN admission_payment_amount") ||
		strings.Contains(stmt, "ALTER TABLE admissions ADD COLUMN finance_transaction_id") ||
		strings.Contains(stmt, "ALTER TABLE student_monthly_payments ADD COLUMN enrollment_id") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN status") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_name") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_email") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requester_phone") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN requested_by_user_id") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN review_note") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN customer_message") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN status_changed_at") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN status_changed_by_user_id") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN status_change_source") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN cancellation_reason") ||
		strings.Contains(stmt, "ALTER TABLE space_schedules ADD COLUMN cancellation_finance_note") ||
		strings.Contains(stmt, "ALTER TABLE booking_request_changes ADD COLUMN customer_message") ||
		strings.Contains(stmt, "ALTER TABLE booking_request_changes ADD COLUMN previous_status") ||
		strings.Contains(stmt, "ALTER TABLE booking_request_changes ADD COLUMN new_status") ||
		strings.Contains(stmt, "ALTER TABLE booking_request_changes ADD COLUMN change_source") ||
		strings.Contains(stmt, "ALTER TABLE booking_request_changes ADD COLUMN finance_note")) &&
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
		"containsInt64": func(values []int64, target int64) bool {
			for _, value := range values {
				if value == target {
					return true
				}
			}
			return false
		},
		"hasPermission": func(user *User, permission string) bool {
			if user == nil {
				return false
			}
			return containsPermission(user.Permissions, permission)
		},
		"admissionSelected":                         admissionSelected,
		"userSelected":                              userSelected,
		"admissionAge":                              admissionAge,
		"attendanceCount":                           attendanceCount,
		"attendanceRecordFor":                       attendanceRecordFor,
		"attendanceStatus":                          attendanceStatus,
		"enrollmentsForAdmission":                   enrollmentsForAdmission,
		"enrollmentCountForAdmission":               enrollmentCountForAdmission,
		"coachAttendanceCount":                      coachAttendanceCount,
		"coachAttendanceRecordFor":                  coachAttendanceRecordFor,
		"activityLabel":                             activityLabel,
		"gameNameFor":                               gameNameFor,
		"gameNameByID":                              gameNameByID,
		"courtActivityGameName":                     courtActivityGameName,
		"courtActivitiesLinkedCount":                courtActivitiesLinkedCount,
		"courtActivitiesUnlinkedCount":              courtActivitiesUnlinkedCount,
		"bookingProductLabel":                       bookingProductLabel,
		"bookingProductLabelForGames":               bookingProductLabelForGames,
		"optionSummary":                             optionSummary,
		"bookingOptionSelected":                     bookingOptionSelected,
		"bookingReference":                          bookingReference,
		"bookingOpenHourCount":                      bookingOpenHourCount,
		"bookingReferralFor":                        bookingReferralFor,
		"bookingRequestHistoryFor":                  bookingRequestHistoryFor,
		"bookingRequestOriginalSnapshot":            bookingRequestOriginalSnapshot,
		"bookingRequestActionLabel":                 bookingRequestActionLabel,
		"bookingCommunicationEventLabel":            bookingCommunicationEventLabel,
		"bookingCommunicationStatusTone":            bookingCommunicationStatusTone,
		"bookingCommunicationsFor":                  bookingCommunicationsFor,
		"bookingAccessTokenFor":                     bookingAccessTokenFor,
		"bookingFinancialForSchedule":               bookingFinancialForSchedule,
		"bookingPaymentsForSchedule":                bookingPaymentsForSchedule,
		"activeBookingPaymentsForSchedule":          activeBookingPaymentsForSchedule,
		"customerVisibleBookingPaymentsForSchedule": customerVisibleBookingPaymentsForSchedule,
		"bookingCanCollectPayment":                  bookingCanCollectPayment,
		"bookingCanVoidPayment":                     bookingCanVoidPayment,
		"bookingPaymentInactiveMessage":             bookingPaymentInactiveMessage,
		"bookingPaymentStatusBadge":                 bookingPaymentStatusBadge,
		"bookingPaymentStatusTone":                  bookingPaymentStatusTone,
		"pendingCancellationRequestFor":             pendingCancellationRequestFor,
		"bookingStatusTone":                         bookingStatusTone,
		"quotedPriceForSchedule":                    quotedPriceForSchedule,
		"courtLayoutHasActivity":                    courtLayoutHasActivity,
		"courtLayoutActivityQuantity":               courtLayoutActivityQuantity,
		"pricingForOption":                          pricingForOption,
		"pricingForSchedule":                        pricingForSchedule,
		"pricingTierLabel":                          pricingTierLabel,
		"financeAccountTypeLabel":                   financeAccountTypeLabel,
		"financeTransactionTypeLabel":               financeTransactionTypeLabel,
		"financeTransactionStatusLabel":             financeTransactionStatusLabel,
		"financeDirectionForTransaction":            financeDirectionForTransaction,
		"financeTransactionAllowsGeneralVoid":       financeTransactionAllowsGeneralVoid,
		"financeVoidWorkflowMessage":                financeVoidWorkflowMessage,
		"financeAccountTone":                        financeAccountTone,
		"financeCategoryLabel":                      financeCategoryLabel,
		"financeSourceTypeLabel":                    financeSourceTypeLabel,
		"financeActiveCategoriesForDirection":       financeActiveCategoriesForDirection,
		"financeCategoriesForDirection":             financeCategoriesForDirection,
		"financeFilterExportURL":                    financeFilterExportURL,
		"admissionsPageURL":                         admissionsPageURL,
		"paymentMonthLabel":                         paymentMonthLabel,
		"formatDateTime":                            formatDateTime,
		"relativeTime":                              relativeTime,
		"formatCalendarDate":                        formatCalendarDate,
		"formatClockTime":                           formatClockTime,
		"paymentMethodLabel":                        paymentMethodLabel,
		"formatEventTiming":                         formatEventTiming,
		"eventScheduleLabel":                        eventScheduleLabel,
		"hasTime":                                   hasTime,
		"hasRegistrationDeadline":                   hasRegistrationDeadline,
		"isPastEventDate":                           isPastEventDate,
		"money":                                     money,
		"negate":                                    negate,
		"reportBarWidth":                            reportBarWidth,
		"registrationDeadlineLabel":                 registrationDeadlineLabel,
		"scheduleToneClasses":                       scheduleToneClasses,
		"scheduleBadgeClasses":                      scheduleBadgeClasses,
		"schedulesForCalendarSlot":                  schedulesForCalendarSlot,
		"scheduleSummary":                           scheduleSummary,
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
		"subFloat": func(a, b float64) float64 {
			return normalizeMoney(a - b)
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
		"booking-status":              "templates/pages/booking-status.html",
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
		"student-leave-management":    "templates/dashboard/student-leave-management.html",
		"student-id-card":             "templates/dashboard/student-id-card.html",
		"enrollment-management":       "templates/dashboard/enrollment-management.html",
		"coach-management":            "templates/dashboard/coach-management.html",
		"training-program-management": "templates/dashboard/training-program-management.html",
		"student-group-management":    "templates/dashboard/student-group-management.html",
		"attendance-management":       "templates/dashboard/attendance-management.html",
		"court-management":            "templates/dashboard/court-management.html",
		"games-management":            "templates/dashboard/games-management.html",
		"one-to-one-management":       "templates/dashboard/one-to-one-management.html",
		"one-to-one-bookings":         "templates/dashboard/one-to-one-bookings.html",
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
		"templates/dashboard/src/receipt-print.html",
		"templates/dashboard/src/finance-shell.html",
		"templates/dashboard/src/finance-receivables.html",
		"templates/dashboard/src/finance-customers.html",
		"templates/dashboard/src/finance-ledger.html",
		"templates/dashboard/src/finance-specified-ledgers.html",
		"templates/dashboard/src/finance-ledger-filters.html",
		"templates/dashboard/src/finance-ledger-results.html",
		"templates/dashboard/src/finance-statements.html",
		"templates/dashboard/src/finance-operations.html",
		"templates/dashboard/src/finance-categories.html",
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
