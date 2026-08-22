package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type readinessCheckResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type readinessResponse struct {
	Status   string                   `json:"status"`
	Checks   []readinessCheckResponse `json:"checks"`
	Warnings []string                 `json:"warnings,omitempty"`
}

func (a *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) readyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	response, ready := a.readinessResponse()
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write json: %v", err)
	}
}

func (a *App) readinessResponse() (readinessResponse, bool) {
	checks := make([]readinessCheckResponse, 0, 5)
	warnings := make([]string, 0)
	ready := true

	configErrs := validateRuntimeConfiguration(
		a.runtimeConfig,
		a.bookingMessages,
		a.bookingAccess,
		a.smtp,
		a.sms,
	)
	configErrs = append(
		configErrs,
		validateUploadPath(
			a.runtimeConfig.Env,
			a.uploads.Root,
		)...,
	)

	if a.runtimeConfig.DBDriver == databaseDriverSQLite {
		_, dbPathErrs := validateDatabasePath(
			a.runtimeConfig.Env,
			a.runtimeConfig.DBPath,
		)
		configErrs = append(configErrs, dbPathErrs...)
	}
	if len(configErrs) == 0 {
		checks = append(checks, readinessCheckResponse{Name: "config", Status: "ok"})
	} else {
		checks = append(checks, readinessCheckResponse{Name: "config", Status: "error"})
		ready = false
	}

	if err := a.checkDatabaseReadiness(); err != nil {
		checks = append(checks, readinessCheckResponse{Name: "database", Status: "error"})
		ready = false
	} else {
		checks = append(checks, readinessCheckResponse{Name: "database", Status: "ok"})
	}

	if err := a.checkUploadReadiness(); err != nil {
		checks = append(checks, readinessCheckResponse{Name: "uploads", Status: "error"})
		ready = false
	} else {
		checks = append(checks, readinessCheckResponse{Name: "uploads", Status: "ok"})
	}

	if err := a.checkMigrationReadiness(); err != nil {
		checks = append(checks, readinessCheckResponse{Name: "migrations", Status: "error"})
		ready = false
	} else {
		checks = append(checks, readinessCheckResponse{Name: "migrations", Status: "ok"})
	}

	unpricedOptions, err := a.listActiveUnpricedBookingOptions()
	if err != nil {
		checks = append(checks, readinessCheckResponse{Name: "booking_pricing", Status: "error"})
		ready = false
	} else {
		checks = append(checks, readinessCheckResponse{Name: "booking_pricing", Status: "ok"})
		if len(unpricedOptions) > 0 {
			warnings = append(warnings, fmt.Sprintf("%d active booking options are missing complete public pricing", len(unpricedOptions)))
		}
	}

	status := "ready"
	if !ready {
		status = "not_ready"
	}
	return readinessResponse{
		Status:   status,
		Checks:   checks,
		Warnings: warnings,
	}, ready
}

func (a *App) checkDatabaseReadiness() error {
	if a.db == nil {
		return errors.New("database unavailable")
	}

	if err := a.db.Ping(); err != nil {
		return err
	}

	if a.runtimeConfig.DBDriver == databaseDriverSQLite {
		var foreignKeys int

		if err := a.queryRowDB(
			`PRAGMA foreign_keys`,
		).Scan(&foreignKeys); err != nil {
			return err
		}

		if foreignKeys != 1 {
			return errors.New(
				"sqlite foreign keys are disabled",
			)
		}
	}

	var one int

	if err := a.queryRowDB(
		`SELECT 1`,
	).Scan(&one); err != nil {
		return err
	}

	if one != 1 {
		return errors.New("database query failed")
	}

	return nil
}

func (a *App) checkMigrationReadiness() error {
	requiredTables := []string{
		"users",
		"roles",
		"space_schedules",
		"pricing_rules",
		"booking_financials",
	}

	for _, tableName := range requiredTables {
		var count int
		var err error

		switch a.runtimeConfig.DBDriver {
		case databaseDriverPostgres:
			err = a.queryRowDB(`
				SELECT COUNT(*)
				FROM information_schema.tables
				WHERE table_schema = 'public'
				  AND table_type = 'BASE TABLE'
				  AND table_name = $1
			`, tableName).Scan(&count)

		case databaseDriverSQLite:
			err = a.queryRowDB(`
				SELECT COUNT(*)
				FROM sqlite_master
				WHERE type = 'table'
				  AND name = ?
			`, tableName).Scan(&count)

		default:
			return fmt.Errorf(
				"unsupported database driver %q",
				a.runtimeConfig.DBDriver,
			)
		}

		if err != nil {
			return err
		}

		if count == 0 {
			return fmt.Errorf(
				"required table %s is missing",
				tableName,
			)
		}
	}

	return nil
}

func (a *App) checkUploadReadiness() error {
	for _, dir := range []string{a.uploads.EventDir, a.uploads.StudentPhotoDir, a.uploads.StudentQRDir} {
		probe, err := os.CreateTemp(dir, ".mekmaa-ready-*")
		if err != nil {
			return err
		}
		probeName := probe.Name()
		if err := probe.Close(); err != nil {
			_ = os.Remove(probeName)
			return err
		}
		if err := os.Remove(probeName); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) setupWarningsForUser(user *User) []SetupWarning {
	if user == nil || !containsPermission(user.Permissions, "pricing.view") {
		return nil
	}
	unpricedOptions, err := a.listActiveUnpricedBookingOptions()
	if err != nil || len(unpricedOptions) == 0 {
		return nil
	}
	labels := make([]string, 0, len(unpricedOptions))
	for _, issue := range unpricedOptions {
		labels = append(labels, issue.Label)
	}
	body := fmt.Sprintf(
		"Public booking pricing is incomplete for %s. Customers will see a pricing-unavailable message until these rates are configured.",
		strings.Join(labels, ", "),
	)
	return []SetupWarning{{
		Title:     "Booking pricing setup incomplete",
		Body:      body,
		Href:      "/admin/pricing",
		LinkLabel: "Open pricing",
	}}
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
		a.writePublicBookingError(w, r, &schedule, bookingPricingUnavailableMessage(schedule.Activity, schedule.Quantity), http.StatusBadRequest)
		return
	}
	schedule.QuotedPrice = priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour)
	created, _, err := a.createPublicBookingRequestDetailed(schedule)
	if err != nil {
		a.writePublicBookingError(w, r, &schedule, err.Error(), http.StatusBadRequest)
		return
	}
	requestID := created.ID
	_, rawToken, tokenErr := a.ensureActiveBookingAccessToken(requestID, "status")
	if tokenErr != nil {
		log.Printf("issue booking status token: %v", tokenErr)
	}
	eventType := bookingCommEventRequestReceived
	eventKey := fmt.Sprintf("schedule:%d:%s", requestID, bookingCommEventRequestReceived)
	flashMessage := "Booking request " + bookingReference(requestID) + " received. Keep this reference while our team reviews the request."

	results, commErr := a.sendBookingCommunicationEvent(
		requestID,
		eventType,
		"",
		eventKey,
		0,
	)
	if commErr != nil {
		log.Printf("send booking request communication: %v", commErr)
	}
	emailSent := communicationDelivered(results, bookingCommChannelEmail)
	smsSent := communicationDelivered(results, bookingCommChannelSMS)
	if smsSent {
		a.setFlash(w, flashMessage)
	} else {
		a.setFlash(w, flashMessage+" We could not confirm SMS delivery automatically.")
	}
	if strings.TrimSpace(rawToken) != "" && (!emailSent && !smsSent) {
		http.Redirect(w, r, "/booking/status?token="+url.QueryEscape(rawToken), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/book?date="+url.QueryEscape(schedule.SlotDate), http.StatusSeeOther)
}

func (a *App) publicBookingStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/booking/status" {
		http.NotFound(w, r)
		return
	}
	a.expireOverdueBookingRequests(time.Now())
	w.Header().Set("Cache-Control", "no-store, private, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")

	data := a.newTemplateData(w, r, nil)
	data.Title = "Booking Status"
	data.Description = "Secure Mekmaa booking status"

	schedule, token, err := a.findActiveBookingByAccessToken(r.URL.Query().Get("token"))
	if err != nil {
		data.BookingStatusUnavailable = true
		data.BookingStatusUnavailableMessage = "This booking link is unavailable. Please contact Mekmaa and request a fresh status link."
		a.render(w, "booking-status", data, http.StatusNotFound)
		return
	}

	financials, err := a.listBookingFinancialsForScheduleIDs([]int64{schedule.ID})
	if err != nil {
		log.Printf("load booking status financials: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	changes, err := a.listBookingRequestChangesForScheduleIDs([]int64{schedule.ID})
	if err != nil {
		log.Printf("load booking status changes: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	communications, err := a.listBookingCommunicationsForScheduleIDs([]int64{schedule.ID})
	if err != nil {
		log.Printf("load booking status communications: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data.SelectedSchedule = schedule
	data.SelectedAccessToken = token
	data.BookingStatusToken = strings.TrimSpace(r.URL.Query().Get("token"))
	data.BookingFinancials = financials
	data.BookingRequestChanges = changes
	data.BookingCommunications = communications
	data.BookingPaymentCollections, err = a.listBookingPaymentCollectionsForScheduleIDs([]int64{schedule.ID})
	if err != nil {
		log.Printf("load booking payment collections: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	requests, reqErr := a.listBookingCancellationRequestsForScheduleIDs([]int64{schedule.ID})
	if reqErr != nil {
		log.Printf("load booking cancellation requests: %v", reqErr)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.BookingCancellationRequests = requests
	data.BookingStatusView = a.buildBookingStatusView(schedule, bookingFinancialForSchedule(financials, schedule.ID), changes, requests)
	data.BookingStatusTimeline = buildCustomerBookingTimeline(*schedule, changes, communications)
	a.render(w, "booking-status", data, http.StatusOK)
}

func (a *App) redirectPublicBookingStatus(w http.ResponseWriter, r *http.Request, rawToken string, message string) {
	if strings.TrimSpace(message) != "" {
		a.setFlash(w, message)
	}
	http.Redirect(w, r, "/booking/status?token="+url.QueryEscape(strings.TrimSpace(rawToken)), http.StatusSeeOther)
}

func (a *App) publicBookingCancellationRequestHandler(w http.ResponseWriter, r *http.Request) {
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
	rawToken := strings.TrimSpace(r.FormValue("token"))
	reason := strings.TrimSpace(r.FormValue("request_reason"))
	if reason == "" {
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation reason is required.")
		return
	}
	schedule, token, err := a.findActiveBookingByAccessToken(rawToken)
	if err != nil {
		a.redirectPublicBookingStatus(w, r, rawToken, "This booking status link is unavailable. Please contact Mekmaa and request a fresh status link.")
		return
	}
	if !bookingEligibleForCustomerCancellation(schedule, time.Now()) {
		a.redirectPublicBookingStatus(w, r, rawToken, "This booking cannot accept a cancellation request right now.")
		return
	}
	existing, err := a.listBookingCancellationRequestsForScheduleIDs([]int64{schedule.ID})
	if err != nil {
		log.Printf("load booking cancellation requests: %v", err)
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request could not be submitted right now.")
		return
	}
	if pendingCancellationRequestFor(existing, schedule.ID) != nil {
		a.redirectPublicBookingStatus(w, r, rawToken, "A cancellation request is already pending for this booking.")
		return
	}

	tx, err := a.db.Begin()
	if err != nil {
		log.Printf("begin booking cancellation request: %v", err)
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request could not be submitted right now.")
		return
	}
	defer tx.Rollback()
	requestID, err := a.insertAndReturnIDTx(tx, `
		INSERT INTO booking_cancellation_requests (
			schedule_id, status, request_reason, requested_at, token_id, review_note, reviewed_at, reviewed_by_user_id
		)
		VALUES (?, 'pending', ?, ?, ?, '', NULL, NULL)
	`, schedule.ID, reason, time.Now().UTC(), nullIfZero(token.ID))
	if err != nil {
		if isUniqueConstraintError(err) {
			a.redirectPublicBookingStatus(w, r, rawToken, "A cancellation request is already pending for this booking.")
			return
		}
		log.Printf("insert booking cancellation request: %v", err)
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request could not be submitted right now.")
		return
	}
	financial := bookingFinancialForSchedule(mustListBookingFinancialsTx(
		tx,
		a.runtimeConfig.DBDriver,
		schedule.ID,
	), schedule.ID)
	if financial != nil {
		schedule.QuotedPrice = financial.QuotedAmount
	}
	if _, err := a.recordBookingLifecycleChangeTx(tx, schedule, "cancellation_requested", schedule.Status, schedule.Status, "", reason, "", "customer", 0); err != nil {
		log.Printf("record booking cancellation request history: %v", err)
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request could not be submitted right now.")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("commit booking cancellation request: %v", err)
		a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request could not be submitted right now.")
		return
	}
	_, commErr := a.sendBookingCommunicationEvent(schedule.ID, bookingCommEventCancellationRequested, "", fmt.Sprintf("schedule:%d:%s:req:%d", schedule.ID, bookingCommEventCancellationRequested, requestID), 0)
	if commErr != nil {
		log.Printf("send cancellation requested communication: %v", commErr)
	}
	a.redirectPublicBookingStatus(w, r, rawToken, "Cancellation request sent. Mekmaa will review it and update this status page.")
}

func (a *App) registerHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user := a.optionalUser(r)
		if user != nil {
			http.Redirect(w, r, a.customerRedirectAfterLogin(user), http.StatusSeeOther)
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
		http.Redirect(w, r, a.customerRedirectAfterLogin(user), http.StatusSeeOther)
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
	now := time.Now()
	a.expireOverdueBookingRequests(now)
	data := a.newTemplateData(w, r, user)
	data.Title = "Dashboard"
	data.Description = "Authenticated user dashboard."
	data.SetupWarnings = a.setupWarningsForUser(user)
	allowedDivisionIDs, err := a.scopedDivisionIDsForUser(user, true)
	if err != nil {
		allowedDivisionIDs = nil
	}
	var selectedDivision *Division
	if divisionValue := strings.TrimSpace(r.URL.Query().Get("division")); divisionValue != "" || canViewAllDivisions(user) || len(user.DivisionIDs) > 0 {
		selectedDivision, err = a.resolveAuthorizedDivisionFromRequest(r, canViewAllDivisions(user))
		if errors.Is(err, ErrForbiddenDivision) {
			a.writeDivisionForbidden(w, r, user)
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	scopeDivisionIDs := []int64(nil)
	if selectedDivision != nil {
		scopeDivisionIDs = []int64{selectedDivision.ID}
		data.SelectedDivision = selectedDivision
		data.SelectedDivisionScope = selectedDivision.Slug
	} else if !canViewAllDivisions(user) {
		scopeDivisionIDs = append([]int64(nil), allowedDivisionIDs...)
	}

	data.Stats = a.buildDashboardStats(user, selectedDivision, scopeDivisionIDs, now)
	if containsPermission(user.Permissions, "booking_requests.view") || containsPermission(user.Permissions, "space_bookings.view") {
		requests, err := a.listPendingSpaceSchedulesByDivisionIDs(scopeDivisionIDs)
		if err == nil {
			pendingCount, _ := a.countPendingSpaceSchedulesByDivisionIDs(scopeDivisionIDs)
			heldCount, _ := a.countHeldSpaceSchedulesByDivisionIDs(scopeDivisionIDs)
			reschedulePendingCount, _ := a.countReschedulePendingSpaceSchedulesByDivisionIDs(scopeDivisionIDs)
			data.PendingRequestCount = pendingCount
			data.HeldRequestCount = heldCount
			data.BookingReminders = buildBookingReminders(requests, now)
			data.BookingAttentionStats = buildBookingAttentionStats(data.BookingReminders, pendingCount, heldCount, reschedulePendingCount)
		}
	}
	a.render(w, "dashboard", data, http.StatusOK)
}

func (a *App) buildDashboardStats(user *User, selectedDivision *Division, divisionIDs []int64, now time.Time) []Stat {
	stats := make([]Stat, 0, 8)

	admissions, totalStudents, err := a.listAdmissionsFiltered(AdmissionsFilter{
		Page:        1,
		Limit:       1,
		Direction:   "asc",
		DivisionIDs: append([]int64(nil), divisionIDs...),
	})
	_ = admissions
	if err == nil {
		stats = append(stats, Stat{Value: strconv.Itoa(totalStudents), Label: "Active students"})
	}

	enrollments, err := a.listStudentEnrollmentsByDivisionIDs(divisionIDs)
	if err == nil {
		activeEnrollments := 0
		for _, enrollment := range enrollments {
			if enrollment.Active {
				activeEnrollments++
			}
		}
		stats = append(stats, Stat{Value: strconv.Itoa(activeEnrollments), Label: "Active enrollments"})
	}

	programs, err := a.listTrainingProgramsByDivisionIDs(divisionIDs, false, true)
	if err == nil {
		stats = append(stats, Stat{
			Value: strconv.Itoa(len(programs)),
			Label: workspaceProgramLabel(user, selectedDivision, ""),
		})
	}

	groups, err := a.listStudentGroupsByDivisionIDs(divisionIDs)
	if err == nil {
		stats = append(stats, Stat{
			Value: strconv.Itoa(len(groups)),
			Label: workspaceGroupLabel(user, selectedDivision, ""),
		})
	}

	attendanceCount, err := a.countAttendanceEntriesByDivisionIDs(now.Format("2006-01-02"), divisionIDs)
	if err == nil {
		stats = append(stats, Stat{Value: strconv.Itoa(attendanceCount), Label: "Attendance today"})
	}

	paymentMonth := latestCollectiblePaymentMonth(now)
	paymentRows, err := a.listStudentPaymentRowsByDivisionIDs(paymentMonth, divisionIDs)
	if err == nil {
		outstanding := 0.0
		for _, row := range paymentRows {
			if row.OutstandingAmount > 0.004 {
				outstanding += row.OutstandingAmount
				continue
			}
			if row.CollectedAmount+0.004 < row.MonthlyFee {
				outstanding += row.MonthlyFee - row.CollectedAmount
			}
		}
		stats = append(stats, Stat{Value: money(outstanding), Label: "Outstanding fees"})
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	monthEnd := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	transactions, err := a.listFinanceTransactionsFiltered(FinanceFilter{
		From:        monthStart,
		To:          monthEnd,
		DivisionIDs: append([]int64(nil), divisionIDs...),
	})
	if err == nil {
		income := 0.0
		expense := 0.0
		for _, transaction := range transactions {
			if !financeTransactionPosted(transaction) {
				continue
			}
			if transaction.Amount >= 0 {
				income += transaction.Amount
			} else {
				expense += -transaction.Amount
			}
		}
		stats = append(stats, Stat{Value: money(income), Label: "Income this month"})
		stats = append(stats, Stat{Value: money(expense), Label: "Expenses this month"})
	}

	if allowed, err := a.scopeIncludesSportsDivision(divisionIDs); err == nil && allowed {
		pendingCount, _ := a.countPendingSpaceSchedulesByDivisionIDs(divisionIDs)
		heldCount, _ := a.countHeldSpaceSchedulesByDivisionIDs(divisionIDs)
		stats = append(stats,
			Stat{Value: strconv.Itoa(pendingCount), Label: "Pending bookings"},
			Stat{Value: strconv.Itoa(heldCount), Label: "Held bookings"},
		)
	}

	if len(stats) == 0 {
		stats = append(stats,
			Stat{Value: verifiedLabel(user.Verified), Label: "Session"},
			Stat{Value: strconv.Itoa(len(user.Roles)), Label: "Assigned roles"},
		)
	}
	return stats
}

func (a *App) countAttendanceEntriesByDivisionIDs(attendanceDate string, divisionIDs []int64) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM attendance_records ar
		JOIN student_groups sg ON sg.id = ar.group_id
		JOIN training_programs tp ON tp.id = sg.training_program_id
		WHERE ar.attendance_date = ?
	`
	args := []any{attendanceDate}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND tp.division_id IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	var count int
	err := a.queryRowDB(query, args...).Scan(&count)
	return count, err
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
		{"users.view", "/admin/users"},
		{"roles.view", "/admin/roles"},
		{"admissions.view", "/admin/admissions"},
		{"coaches.view", "/admin/coaches"},
		{"training_programs.view", "/admin/training-programs"},
		{"student_groups.view", "/admin/student-groups"},
		{"attendance.view", "/admin/attendance"},
		{"courts.view", "/admin/courts"},
		{"space_bookings.view", "/admin/bookings"},
		{"booking_requests.view", "/admin/booking-requests"},
		{"finance.view", "/admin/finance/ledger"},
		{"pricing.view", "/admin/pricing"},
		{"reports.view", "/admin/reports"},
		{"events.view", "/admin/events"},
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
	users, err := a.listUsersVisibleToManager(user)
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
	data.Title = "User Access"
	data.Description = "Manage staff access inside authorized divisions."
	data.Users = users
	data.ActiveDivisions, _ = a.accessibleDivisionsForUser(user, false)
	data.AvailableDivisions = data.ActiveDivisions
	for _, role := range roles {
		if isPrivilegedRole(role.Name) && !containsRole(user.Roles, "superadmin") {
			continue
		}
		data.Available = append(data.Available, role.Name)
	}
	data.Roles = roles
	a.render(w, "user-management", data, http.StatusOK)
}
