package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/smtp"
	"net/url"
	"sort"
	"strings"
	"time"
)

func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func bookingStatusConsumesCapacity(status string) bool {
	// Unresolved requests continue to reserve the slot in this application.
	// `held` follows the same behavior as `pending` so staff do not accidentally
	// promise a slot twice while a request is under review.
	switch canonicalBookingStatus(status) {
	case bookingStatusPending, bookingStatusHeld, bookingStatusConfirmed, bookingStatusReschedulePending:
		return true
	default:
		return false
	}
}

func isInactiveBookingStatus(status string) bool {
	switch canonicalBookingStatus(status) {
	case bookingStatusRejected, bookingStatusCancelled, bookingStatusCompleted, bookingStatusNoShow, bookingStatusExpired:
		return true
	default:
		return false
	}
}

func validBookingStatus(status string) bool {
	switch canonicalBookingStatus(status) {
	case bookingStatusPending, bookingStatusHeld, bookingStatusConfirmed, bookingStatusRejected, bookingStatusReschedulePending, bookingStatusCancelled, bookingStatusCompleted, bookingStatusNoShow, bookingStatusExpired:
		return true
	default:
		return false
	}
}

func slotStartTime(schedule *SpaceSchedule) (time.Time, error) {
	if schedule == nil {
		return time.Time{}, errors.New("schedule is required")
	}
	return time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(schedule.SlotDate)+" "+strings.TrimSpace(schedule.SlotHour), time.Local)
}

func normalizePublicBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (a *App) bookingTrackingURL(rawToken string) string {
	base := normalizePublicBaseURL(a.bookingAccess.BaseURL)
	if base == "" {
		base = "http://localhost:8080"
	}
	return base + "/booking/status?token=" + url.QueryEscape(rawToken)
}

func (a *App) bookingAccessTokenExpiry(schedule *SpaceSchedule) time.Time {
	now := time.Now().UTC()
	expiresAt := now.Add(a.bookingAccess.TokenTTL)
	if schedule == nil {
		return expiresAt
	}
	slotDate, err := time.Parse("2006-01-02", strings.TrimSpace(schedule.SlotDate))
	if err != nil {
		return expiresAt
	}
	scheduleExpiry := time.Date(slotDate.Year(), slotDate.Month(), slotDate.Day(), 23, 59, 59, 0, time.UTC).Add(a.bookingAccess.TokenTTL)
	if scheduleExpiry.After(expiresAt) {
		return scheduleExpiry
	}
	return expiresAt
}

func (a *App) bookingAccessTokenSignature(publicID string, scheduleID int64, purpose string) string {
	mac := hmac.New(sha256.New, []byte(a.bookingAccess.TokenSecret))
	fmt.Fprintf(mac, "%d:%s:%s", scheduleID, purpose, publicID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) buildBookingAccessToken(scheduleID int64, publicID string, purpose string) string {
	return publicID + "." + a.bookingAccessTokenSignature(publicID, scheduleID, purpose)
}

func redactSensitiveValue(value string, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[secure booking status link]")
}

func compareHashConstantTime(raw string, expectedHash string) bool {
	actual := hashValue(raw)
	if len(actual) != len(expectedHash) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expectedHash)) == 1
}

func (a *App) listBookingAccessTokensForScheduleIDs(scheduleIDs []int64) ([]BookingAccessToken, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT id, schedule_id, public_id, token_hash, purpose, active, expires_at, last_accessed_at, created_at, revoked_at
		FROM booking_access_tokens
		WHERE schedule_id IN (%s)
		ORDER BY created_at DESC, id DESC
	`, scheduleIDs)
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []BookingAccessToken
	for rows.Next() {
		token, err := scanBookingAccessToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func scanBookingAccessToken(row rowScanner) (BookingAccessToken, error) {
	var token BookingAccessToken
	var active int
	var lastAccessedAt sql.NullTime
	var revokedAt sql.NullTime
	if err := row.Scan(
		&token.ID,
		&token.ScheduleID,
		&token.PublicID,
		&token.TokenHash,
		&token.Purpose,
		&active,
		&token.ExpiresAt,
		&lastAccessedAt,
		&token.CreatedAt,
		&revokedAt,
	); err != nil {
		return BookingAccessToken{}, err
	}
	token.Active = active == 1
	if lastAccessedAt.Valid {
		token.LastAccessedAt = lastAccessedAt.Time
	}
	if revokedAt.Valid {
		token.RevokedAt = revokedAt.Time
	}
	return token, nil
}

func bookingAccessTokenFor(tokens []BookingAccessToken, scheduleID int64) *BookingAccessToken {
	now := time.Now().UTC()
	for i := range tokens {
		if tokens[i].ScheduleID == scheduleID && tokens[i].Active && tokens[i].RevokedAt.IsZero() && tokens[i].ExpiresAt.After(now) {
			return &tokens[i]
		}
	}
	for i := range tokens {
		if tokens[i].ScheduleID == scheduleID {
			return &tokens[i]
		}
	}
	return nil
}

func (a *App) ensureActiveBookingAccessToken(scheduleID int64, purpose string) (*BookingAccessToken, string, error) {
	schedule, err := a.findSpaceScheduleByID(scheduleID)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	row := a.db.QueryRow(`
		SELECT id, schedule_id, public_id, token_hash, purpose, active, expires_at, last_accessed_at, created_at, revoked_at
		FROM booking_access_tokens
		WHERE schedule_id = ?
		  AND purpose = ?
		  AND active = 1
		  AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, scheduleID, purpose)
	token, err := scanBookingAccessToken(row)
	if err == nil && token.ExpiresAt.After(now) {
		return &token, a.buildBookingAccessToken(scheduleID, token.PublicID, token.Purpose), nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}

	publicID, err := generateToken(18)
	if err != nil {
		return nil, "", err
	}
	rawToken := a.buildBookingAccessToken(scheduleID, publicID, purpose)
	tokenHash := hashValue(rawToken)
	expiresAt := a.bookingAccessTokenExpiry(schedule)
	result, err := a.db.Exec(`
		INSERT INTO booking_access_tokens (
			schedule_id, public_id, token_hash, purpose, active, expires_at, last_accessed_at, created_at, revoked_at
		)
		VALUES (?, ?, ?, ?, 1, ?, NULL, ?, NULL)
	`, scheduleID, publicID, tokenHash, purpose, expiresAt, now)
	if err != nil {
		return nil, "", err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, "", err
	}
	return &BookingAccessToken{
		ID:         id,
		ScheduleID: scheduleID,
		PublicID:   publicID,
		TokenHash:  tokenHash,
		Purpose:    purpose,
		Active:     true,
		ExpiresAt:  expiresAt,
		CreatedAt:  now,
	}, rawToken, nil
}

func (a *App) rotateBookingAccessToken(scheduleID int64, purpose string) (string, error) {
	now := time.Now().UTC()
	if _, err := a.db.Exec(`
		UPDATE booking_access_tokens
		SET active = 0, revoked_at = ?, last_accessed_at = COALESCE(last_accessed_at, ?)
		WHERE schedule_id = ? AND purpose = ? AND active = 1
	`, now, now, scheduleID, purpose); err != nil {
		return "", err
	}
	_, rawToken, err := a.ensureActiveBookingAccessToken(scheduleID, purpose)
	return rawToken, err
}

func (a *App) revokeBookingAccessToken(scheduleID int64, purpose string) error {
	now := time.Now().UTC()
	_, err := a.db.Exec(`
		UPDATE booking_access_tokens
		SET active = 0, revoked_at = ?
		WHERE schedule_id = ? AND purpose = ? AND active = 1
	`, now, scheduleID, purpose)
	return err
}

func (a *App) findActiveBookingByAccessToken(rawToken string) (*SpaceSchedule, *BookingAccessToken, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, nil, sql.ErrNoRows
	}
	parts := strings.SplitN(rawToken, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, nil, sql.ErrNoRows
	}
	row := a.db.QueryRow(`
		SELECT id, schedule_id, public_id, token_hash, purpose, active, expires_at, last_accessed_at, created_at, revoked_at
		FROM booking_access_tokens
		WHERE public_id = ?
		LIMIT 1
	`, parts[0])
	token, err := scanBookingAccessToken(row)
	if err != nil {
		return nil, nil, err
	}
	if !token.Active || !token.RevokedAt.IsZero() || !token.ExpiresAt.After(time.Now().UTC()) {
		return nil, nil, sql.ErrNoRows
	}
	expectedRaw := a.buildBookingAccessToken(token.ScheduleID, token.PublicID, token.Purpose)
	if !compareHashConstantTime(rawToken, token.TokenHash) || subtle.ConstantTimeCompare([]byte(rawToken), []byte(expectedRaw)) != 1 {
		return nil, nil, sql.ErrNoRows
	}
	if _, err := a.db.Exec(`UPDATE booking_access_tokens SET last_accessed_at = ? WHERE id = ?`, time.Now().UTC(), token.ID); err != nil {
		return nil, nil, err
	}
	schedule, err := a.findSpaceScheduleByID(token.ScheduleID)
	if err != nil {
		return nil, nil, err
	}
	return schedule, &token, nil
}

func (a *App) listBookingCancellationRequestsForScheduleIDs(scheduleIDs []int64) ([]BookingCancellationRequest, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT id, schedule_id, status, request_reason, requested_at, COALESCE(token_id, 0),
		       review_note, reviewed_at, COALESCE(reviewed_by_user_id, 0)
		FROM booking_cancellation_requests
		WHERE schedule_id IN (%s)
		ORDER BY requested_at DESC, id DESC
	`, scheduleIDs)
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []BookingCancellationRequest
	for rows.Next() {
		request, err := scanBookingCancellationRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func scanBookingCancellationRequest(row rowScanner) (BookingCancellationRequest, error) {
	var request BookingCancellationRequest
	var reviewedAt sql.NullTime
	if err := row.Scan(
		&request.ID,
		&request.ScheduleID,
		&request.Status,
		&request.RequestReason,
		&request.RequestedAt,
		&request.TokenID,
		&request.ReviewNote,
		&reviewedAt,
		&request.ReviewedByUserID,
	); err != nil {
		return BookingCancellationRequest{}, err
	}
	if reviewedAt.Valid {
		request.ReviewedAt = reviewedAt.Time
	}
	return request, nil
}

func pendingCancellationRequestFor(requests []BookingCancellationRequest, scheduleID int64) *BookingCancellationRequest {
	for i := range requests {
		if requests[i].ScheduleID == scheduleID && requests[i].Status == bookingStatusPending {
			return &requests[i]
		}
	}
	return nil
}

func bookingEligibleForCustomerCancellation(schedule *SpaceSchedule, now time.Time) bool {
	if schedule == nil || schedule.EntryType != "booking" || schedule.Status != bookingStatusConfirmed {
		return false
	}
	start, err := slotStartTime(schedule)
	if err != nil {
		return false
	}
	return start.After(now)
}

func (a *App) recordBookingLifecycleChangeTx(
	tx *sql.Tx,
	schedule *SpaceSchedule,
	actionType string,
	previousStatus string,
	newStatus string,
	reviewNote string,
	customerMessage string,
	financeNote string,
	changeSource string,
	changedByUserID int64,
) (int64, error) {
	if schedule == nil {
		return 0, errors.New("schedule is required")
	}
	var changedBy any
	if changedByUserID > 0 {
		changedBy = changedByUserID
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`
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
			previous_status,
			new_status,
			change_source,
			finance_note,
			review_note,
			customer_message,
			changed_by_user_id,
			changed_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		schedule.ID,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.Activity,
		schedule.Quantity,
		schedule.QuotedPrice,
		schedule.SlotDate,
		schedule.SlotHour,
		schedule.Activity,
		schedule.Quantity,
		schedule.QuotedPrice,
		actionType,
		previousStatus,
		newStatus,
		changeSource,
		financeNote,
		reviewNote,
		customerMessage,
		changedBy,
		now,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (a *App) transitionManagedBookingStatus(
	scheduleID int64,
	newStatus string,
	reviewNote string,
	customerMessage string,
	cancellationReason string,
	cancellationFinanceNote string,
	changeSource string,
	changedByUserID int64,
) (*SpaceSchedule, int64, error) {
	if newStatus != bookingStatusCancelled && newStatus != bookingStatusCompleted && newStatus != bookingStatusNoShow {
		return nil, 0, errors.New("invalid booking lifecycle status")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	schedule, err := findSpaceScheduleByIDQuery(tx, scheduleID)
	if err != nil {
		return nil, 0, err
	}
	if schedule.EntryType != "booking" {
		return nil, 0, errors.New("only customer bookings can use this lifecycle action")
	}
	start, err := slotStartTime(schedule)
	if err != nil {
		return nil, 0, err
	}
	nowLocal := time.Now()
	switch newStatus {
	case bookingStatusCancelled:
		if schedule.Status != bookingStatusConfirmed && schedule.Status != bookingStatusPending && schedule.Status != bookingStatusHeld && schedule.Status != bookingStatusReschedulePending {
			return nil, 0, errors.New("this booking cannot be cancelled from its current status")
		}
		if schedule.Status == bookingStatusConfirmed && !start.After(nowLocal) {
			return nil, 0, errors.New("only future confirmed bookings can be cancelled")
		}
		if strings.TrimSpace(cancellationReason) == "" {
			return nil, 0, errors.New("cancellation reason is required")
		}
	case bookingStatusCompleted, bookingStatusNoShow:
		if schedule.Status != bookingStatusConfirmed {
			return nil, 0, errors.New("only confirmed bookings can use this lifecycle action")
		}
		if start.After(nowLocal) {
			return nil, 0, errors.New("future bookings cannot be marked with that status")
		}
	}

	financial := bookingFinancialForSchedule(mustListBookingFinancialsTx(tx, scheduleID), scheduleID)
	if financial != nil {
		schedule.QuotedPrice = financial.QuotedAmount
	}
	result, err := tx.Exec(`
		UPDATE space_schedules
		SET
			status = ?,
			review_note = ?,
			customer_message = ?,
			status_changed_at = ?,
			status_changed_by_user_id = ?,
			status_change_source = ?,
			cancellation_reason = ?,
			cancellation_finance_note = ?,
			updated_at = ?
		WHERE id = ?
		  AND status = ?
	`,
		newStatus,
		reviewNote,
		customerMessage,
		time.Now().UTC(),
		nullIfZero(changedByUserID),
		changeSource,
		cancellationReason,
		cancellationFinanceNote,
		time.Now().UTC(),
		scheduleID,
		schedule.Status,
	)
	if err != nil {
		return nil, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	if affected != 1 {
		return nil, 0, errors.New("booking status could not be updated")
	}

	actionType := newStatus
	changeID, err := a.recordBookingLifecycleChangeTx(tx, schedule, actionType, schedule.Status, newStatus, reviewNote, customerMessage, cancellationFinanceNote, changeSource, changedByUserID)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	updated := *schedule
	updated.Status = newStatus
	updated.ReviewNote = reviewNote
	updated.CustomerMessage = customerMessage
	updated.StatusChangedAt = time.Now().UTC()
	updated.StatusChangedBy = changedByUserID
	updated.StatusSource = changeSource
	updated.CancellationReason = cancellationReason
	updated.CancellationFinanceNote = cancellationFinanceNote
	return &updated, changeID, nil
}

func mustListBookingFinancialsTx(tx *sql.Tx, scheduleID int64) []BookingFinancial {
	return listBookingFinancialsForSingleQueryer(tx, scheduleID)
}

func mustListBookingFinancialsTxMust(queryer sqlQueryer, scheduleID int64) []BookingFinancial {
	return listBookingFinancialsForSingleQueryer(queryer, scheduleID)
}

func listBookingFinancialsForSingleQueryer(queryer sqlQueryer, scheduleID int64) []BookingFinancial {
	financials, err := listBookingFinancialsForScheduleIDsQuery(queryer, []int64{scheduleID})
	if err != nil {
		return nil
	}
	return financials
}

func (a *App) recordBookingLifecycleChange(
	schedule *SpaceSchedule,
	actionType string,
	previousStatus string,
	newStatus string,
	reviewNote string,
	customerMessage string,
	financeNote string,
	changeSource string,
	changedByUserID int64,
) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	changeID, err := a.recordBookingLifecycleChangeTx(tx, schedule, actionType, previousStatus, newStatus, reviewNote, customerMessage, financeNote, changeSource, changedByUserID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changeID, nil
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
	Subject     string
	Heading     string
	Intro       string
	Facts       []bookingEmailFact
	Notes       []string
	SMSBody     string
	TrackingURL string
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
	_, rawToken, err := a.ensureActiveBookingAccessToken(scheduleID, "status")
	if err != nil {
		return nil, err
	}
	trackingURL := a.bookingTrackingURL(rawToken)

	dispatches, err := a.buildBookingCommunicationDispatches(*schedule, bookingFinancialForSchedule(financials, scheduleID), bookingReferralFor(referrals, scheduleID), changes, eventType, relatedEventType, trackingURL)
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
	trackingURL string,
) ([]bookingCommunicationDispatch, error) {
	effectiveEventType := eventType
	if eventType == bookingCommEventResent {
		effectiveEventType = relatedEventType
	}

	content, err := a.buildBookingCommunicationContent(schedule, financial, referral, changes, effectiveEventType, trackingURL)
	if err != nil {
		return nil, err
	}
	textBody := renderBookingEmailText(content)
	htmlBody := renderBookingEmailHTML(content)

	dispatches := make([]bookingCommunicationDispatch, 0, 2)
	if emailPattern.MatchString(strings.TrimSpace(schedule.RequesterEmail)) {
		dispatches = append(dispatches, bookingCommunicationDispatch{
			Channel:     bookingCommChannelEmail,
			Recipient:   schedule.RequesterEmail,
			Subject:     content.Subject,
			TextBody:    textBody,
			HTMLBody:    htmlBody,
			BodyPreview: truncateString(redactSensitiveValue(textBody, trackingURL), 240),
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
	trackingURL string,
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

	content := bookingCommunicationContent{TrackingURL: trackingURL}
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
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking request has been received. Reference: %s. Activity: %s. Date: %s. Time: %s. Awaiting confirmation. Track: %s", reference, bookingProductLabel(schedule.Activity, schedule.Quantity), schedule.SlotDate, schedule.SlotHour, trackingURL)
	case bookingCommEventHeld:
		content.Subject = "Mekmaa booking request on hold - " + reference
		content.Heading = "Your booking request is on hold"
		content.Intro = "Mekmaa placed the request on hold while the team reviews the slot."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Held"},
			{Label: "Date", Value: formatCalendarDate(schedule.SlotDate)},
			{Label: "Time", Value: formatClockTime(schedule.SlotHour)},
			{Label: "Activity", Value: bookingProductLabel(schedule.Activity, schedule.Quantity)},
		}
		if strings.TrimSpace(schedule.CustomerMessage) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Mekmaa message", Value: schedule.CustomerMessage})
		}
		content.Notes = []string{
			"The slot is still under review and is not confirmed yet.",
			"Mekmaa will update you again shortly.",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking request is on hold while our team reviews availability. Reference: %s. Activity: %s. Date: %s. Time: %s. Track: %s", reference, bookingProductLabel(schedule.Activity, schedule.Quantity), schedule.SlotDate, schedule.SlotHour, trackingURL)
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
		if strings.TrimSpace(schedule.CustomerMessage) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Mekmaa message", Value: schedule.CustomerMessage})
		}
		content.Notes = []string{
			"Venue: " + a.bookingMessages.VenueName,
			"Address: " + a.bookingMessages.VenueAddress,
			"Please arrive 10 to 15 minutes early and keep your booking reference ready.",
			"Need help before arrival? Contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
		content.SMSBody = fmt.Sprintf(
			"Your Mekmaa booking is confirmed. Reference: %s. Activity: %s. Date: %s. Time: %s. Amount: %s. Payment will be collected in cash. Track: %s",
			reference,
			bookingProductLabel(schedule.Activity, schedule.Quantity),
			schedule.SlotDate,
			schedule.SlotHour,
			amountLabel,
			trackingURL,
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
			{Label: "Mekmaa message", Value: strings.TrimSpace(schedule.CustomerMessage)},
		}
		content.Notes = []string{
			"If you would like help with another booking request, contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking request could not be accepted. Reference: %s. Reason: %s. Contact %s if you need assistance. Track: %s", reference, strings.TrimSpace(schedule.CustomerMessage), a.bookingMessages.ContactPhone, trackingURL)
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
			{Label: "Mekmaa message", Value: strings.TrimSpace(change.CustomerMessage)},
		}
		content.Notes = []string{
			"The new slot remains pending until Mekmaa confirms it.",
			"Contact us at " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + " if you need assistance.",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking has been rescheduled and is awaiting confirmation. Reference: %s. New activity: %s. New date: %s. New time: %s. Amount: %s. Track: %s", reference, bookingProductLabel(schedule.Activity, schedule.Quantity), schedule.SlotDate, schedule.SlotHour, amountLabel, trackingURL)
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
		if strings.TrimSpace(change.CustomerMessage) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Mekmaa message", Value: change.CustomerMessage})
		}
		content.Notes = []string{
			"Venue: " + a.bookingMessages.VenueName,
			"Address: " + a.bookingMessages.VenueAddress,
			"Please arrive 10 to 15 minutes early and keep your booking reference ready.",
			"Need help before arrival? Contact " + a.bookingMessages.ContactPhone + " or " + a.bookingMessages.ContactEmail + ".",
		}
		content.SMSBody = fmt.Sprintf(
			"Your Mekmaa booking has been rescheduled. Reference: %s. New activity: %s. New date: %s. New time: %s. Amount: %s. Payment will be collected in cash. Track: %s",
			reference,
			bookingProductLabel(schedule.Activity, schedule.Quantity),
			schedule.SlotDate,
			schedule.SlotHour,
			amountLabel,
			trackingURL,
		)
	case bookingCommEventCancellationRequested:
		content.Subject = "Mekmaa booking cancellation request received - " + reference
		content.Heading = "Your cancellation request was received"
		content.Intro = "Mekmaa received your booking cancellation request and will review it shortly."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Current status", Value: "Confirmed"},
			{Label: "Booked slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
		}
		content.Notes = []string{
			"Your booking remains confirmed until Mekmaa approves the cancellation request.",
			"Contact Mekmaa if the request is urgent.",
		}
	case bookingCommEventCancellationApproved, bookingCommEventCancelledByAdmin:
		content.Subject = "Mekmaa booking cancelled - " + reference
		content.Heading = "Your booking has been cancelled"
		content.Intro = "Mekmaa has cancelled the confirmed booking."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Cancelled"},
			{Label: "Cancelled slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
			{Label: "Payment status", Value: paymentLabel},
		}
		if strings.TrimSpace(schedule.CustomerMessage) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Mekmaa message", Value: schedule.CustomerMessage})
		}
		content.Notes = []string{
			"The booking no longer holds court capacity.",
			"Any cash handling is managed manually by Mekmaa and is not processed automatically through this system.",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking has been cancelled. Reference: %s. Activity: %s. Date: %s. Time: %s. Track: %s", reference, bookingProductLabel(schedule.Activity, schedule.Quantity), schedule.SlotDate, schedule.SlotHour, trackingURL)
	case bookingCommEventCancellationRejected:
		content.Subject = "Mekmaa booking cancellation request update - " + reference
		content.Heading = "Your cancellation request was not approved"
		content.Intro = "Mekmaa reviewed the cancellation request and kept the booking confirmed."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Current status", Value: "Confirmed"},
			{Label: "Booked slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
		}
		if strings.TrimSpace(schedule.CustomerMessage) != "" {
			content.Facts = append(content.Facts, bookingEmailFact{Label: "Mekmaa message", Value: schedule.CustomerMessage})
		}
		content.Notes = []string{
			"Contact Mekmaa if you still need assistance with this booking.",
		}
	case bookingCommEventCompleted:
		content.Subject = "Mekmaa booking completed - " + reference
		content.Heading = "Your booking is marked completed"
		content.Intro = "Mekmaa has closed the booking as completed."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Completed"},
			{Label: "Booked slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
			{Label: "Payment status", Value: paymentLabel},
		}
		content.Notes = []string{
			"Thank you for booking with Mekmaa.",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking is marked completed. Reference: %s. Thank you for playing with Mekmaa. Track: %s", reference, trackingURL)
	case bookingCommEventNoShow:
		content.Subject = "Mekmaa booking no-show - " + reference
		content.Heading = "Your booking was marked no-show"
		content.Intro = "Mekmaa marked the booking as a no-show."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "No-show"},
			{Label: "Booked slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking was marked no-show. Reference: %s. Track: %s", reference, trackingURL)
	case bookingCommEventExpired:
		content.Subject = "Mekmaa booking request expired - " + reference
		content.Heading = "Your booking request expired"
		content.Intro = "The requested playing time passed before the booking could be confirmed."
		content.Facts = []bookingEmailFact{
			{Label: "Booking reference", Value: reference},
			{Label: "Status", Value: "Expired"},
			{Label: "Requested slot", Value: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity)},
		}
		content.Notes = []string{
			"Submit a new request if you still need a court.",
		}
		content.SMSBody = fmt.Sprintf("Your Mekmaa booking request expired because the requested playing time passed before confirmation. Reference: %s. Track: %s", reference, trackingURL)
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
	if strings.TrimSpace(content.TrackingURL) != "" {
		builder.WriteString("\nView booking status: " + content.TrackingURL + "\n")
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
		func() string {
			if strings.TrimSpace(content.TrackingURL) == "" {
				return ""
			}
			escaped := template.HTMLEscapeString(content.TrackingURL)
			return "<div style=\"margin:20px 0 0;\"><a href=\"" + escaped + "\" style=\"display:inline-block;background:#0f172a;color:#f8fafc;text-decoration:none;padding:14px 20px;border-radius:999px;font-weight:700;\">View booking status</a><p style=\"margin:14px 0 0;font-size:13px;line-height:1.6;color:#64748b;word-break:break-all;\">" + escaped + "</p></div>"
		}() +
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
	case financial.PaymentStatus == "overpaid":
		return "Overpaid"
	case financial.PaymentStatus == "paid":
		return "Paid"
	case financial.PaymentStatus == "partially_paid":
		return "Part-paid"
	case financial.PaymentStatus == "voided":
		return "Voided"
	case bookingPaymentCollectibleStatus(scheduleStatus):
		return "Unpaid"
	default:
		return "No payment confirmed"
	}
}

func bookingPaymentStatusBadge(value string) string {
	switch strings.TrimSpace(value) {
	case "paid":
		return "Paid"
	case "partially_paid":
		return "Part-paid"
	case "overpaid":
		return "Overpaid"
	case "voided":
		return "Voided"
	default:
		return "Unpaid"
	}
}

func bookingPaymentStatusTone(value string) string {
	switch strings.TrimSpace(value) {
	case "paid":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case "partially_paid":
		return "border-amber-200 bg-amber-50 text-amber-950"
	case "overpaid":
		return "border-sky-200 bg-sky-50 text-sky-900"
	case "voided":
		return "border-slate/10 bg-cloud text-slate"
	default:
		return "border-red-200 bg-red-50 text-red-900"
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
	case bookingStatusConfirmed:
		for _, communication := range communications {
			if communication.EventType == bookingCommEventCancellationRejected ||
				(communication.EventType == bookingCommEventResent && communication.RelatedEventType == bookingCommEventCancellationRejected) {
				return bookingCommEventCancellationRejected
			}
			if communication.EventType == bookingCommEventRescheduledConfirmed ||
				(communication.EventType == bookingCommEventResent && communication.RelatedEventType == bookingCommEventRescheduledConfirmed) {
				return bookingCommEventRescheduledConfirmed
			}
		}
		return bookingCommEventConfirmed
	case bookingStatusCancelled:
		return bookingCommEventCancellationApproved
	case bookingStatusCompleted:
		return bookingCommEventCompleted
	case bookingStatusRejected:
		return bookingCommEventRejected
	case bookingStatusPending:
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

func maskBookingIdentity(schedule *SpaceSchedule) string {
	if schedule == nil {
		return ""
	}
	name := strings.TrimSpace(schedule.RequesterName)
	email := strings.TrimSpace(schedule.RequesterEmail)
	phone := strings.TrimSpace(schedule.RequesterPhone)
	firstName := strings.TrimSpace(strings.Split(name, " ")[0])
	switch {
	case firstName != "" && email != "":
		return firstName + " · " + maskEmail(email)
	case firstName != "" && phone != "":
		return firstName + " · " + maskPhone(phone)
	case firstName != "":
		return firstName
	case email != "":
		return maskEmail(email)
	case phone != "":
		return maskPhone(phone)
	default:
		return "Customer"
	}
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	local := parts[0]
	if len(local) <= 2 {
		return local[:1] + "***@" + parts[1]
	}
	return local[:1] + strings.Repeat("*", len(local)-2) + local[len(local)-1:] + "@" + parts[1]
}

func maskPhone(phone string) string {
	digits := phone
	if len(digits) <= 4 {
		return digits
	}
	return strings.Repeat("*", len(digits)-4) + digits[len(digits)-4:]
}

func buildSlotLabel(slotDate string, slotHour string, activity string, quantity int) string {
	return formatCalendarDate(slotDate) + " at " + formatClockTime(slotHour) + " · " + bookingProductLabel(activity, quantity)
}

func bookingStatusDetails(status string) (string, string, string, string) {
	switch status {
	case bookingStatusConfirmed:
		return "Confirmed", "emerald", "Your reservation is confirmed.", "Arrive 10 to 15 minutes early. Keep your booking reference ready and contact Mekmaa if anything changes."
	case bookingStatusRejected:
		return "Rejected", "rose", "This request could not be confirmed.", "Use the public booking page to request another slot or contact Mekmaa for help."
	case bookingStatusPending:
		return "Pending", "amber", "Your request is still under review.", "Do not treat the slot or payment as confirmed yet. Mekmaa will contact you once the review is complete."
	case bookingStatusHeld:
		return "On Hold", "violet", "Your request is on hold while Mekmaa reviews the slot.", "Do not treat the slot or payment as confirmed yet. Mekmaa will update you shortly."
	case bookingStatusReschedulePending:
		return "Reschedule Pending", "sky", "Mekmaa proposed an updated slot that is still awaiting final confirmation.", "Review the latest slot details and contact Mekmaa if you need help."
	case bookingStatusCancelled:
		return "Cancelled", "slate", "This booking has been cancelled.", "If you still need court time, submit a new request or contact Mekmaa directly."
	case bookingStatusCompleted:
		return "Completed", "sky", "This booking was completed.", "Keep the booking reference for your records."
	case bookingStatusNoShow:
		return "No-show", "orange", "Mekmaa marked this booking as a no-show.", "Contact Mekmaa if you believe this status is incorrect."
	case bookingStatusExpired:
		return "Expired", "slate", "This booking request expired before it was confirmed.", "Submit a new request if you still need the slot."
	default:
		if strings.TrimSpace(status) == "" {
			status = "Booking"
		} else {
			status = strings.ToUpper(status[:1]) + status[1:]
		}
		return status, "slate", "This booking is being reviewed.", "Contact Mekmaa if you need help with this booking."
	}
}

func (a *App) buildBookingStatusView(schedule *SpaceSchedule, financial *BookingFinancial, changes []BookingRequestChange, requests []BookingCancellationRequest) *BookingStatusView {
	if schedule == nil {
		return nil
	}
	statusLabel, tone, summary, nextSteps := bookingStatusDetails(schedule.Status)
	pendingCancellation := pendingCancellationRequestFor(requests, schedule.ID) != nil
	view := &BookingStatusView{
		Reference:                  bookingReference(schedule.ID),
		StatusLabel:                statusLabel,
		StatusTone:                 tone,
		StatusSummary:              summary,
		NextSteps:                  nextSteps,
		MaskedIdentity:             maskBookingIdentity(schedule),
		CustomerMessage:            strings.TrimSpace(schedule.CustomerMessage),
		CurrentSlotLabel:           buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity),
		ActivityLabel:              bookingProductLabel(schedule.Activity, schedule.Quantity),
		QuotedAmount:               quotedAmountLabel(financial),
		PaymentStatus:              bookingPaymentStatusLabel(financial, schedule.Status),
		TotalCollected:             money(0),
		OutstandingAmount:          money(0),
		Title:                      strings.TrimSpace(schedule.Title),
		ContactPhone:               a.bookingMessages.ContactPhone,
		ContactEmail:               a.bookingMessages.ContactEmail,
		VenueName:                  a.bookingMessages.VenueName,
		VenueAddress:               a.bookingMessages.VenueAddress,
		CanPrint:                   schedule.Status == bookingStatusConfirmed,
		CanRequestCancellation:     bookingEligibleForCustomerCancellation(schedule, time.Now()) && !pendingCancellation,
		PendingCancellationRequest: pendingCancellation,
	}
	if financial != nil && financial.PaidAt.After(time.Time{}) {
		view.PaidAtLabel = formatDateTime(financial.PaidAt)
	}
	if financial != nil {
		view.TotalCollected = money(financial.TotalCollected)
		if financial.OutstandingAmount > 0 {
			view.OutstandingAmount = money(financial.OutstandingAmount)
		}
	}
	if history := bookingRequestHistoryFor(changes, schedule.ID); len(history) > 0 {
		latest := history[0]
		view.PreviousSlotLabel = buildSlotLabel(latest.PreviousSlotDate, latest.PreviousSlotHour, latest.PreviousActivity, latest.PreviousQuantity)
		if strings.TrimSpace(view.CustomerMessage) == "" {
			view.CustomerMessage = strings.TrimSpace(latest.CustomerMessage)
		}
	}
	return view
}

func buildCustomerBookingTimeline(schedule SpaceSchedule, changes []BookingRequestChange, communications []BookingCommunication) []CustomerBookingTimelineItem {
	items := []CustomerBookingTimelineItem{{
		Label:  "Request submitted",
		Detail: buildSlotLabel(schedule.SlotDate, schedule.SlotHour, schedule.Activity, schedule.Quantity),
		When:   schedule.CreatedAt,
	}}
	for _, change := range bookingRequestHistoryFor(changes, schedule.ID) {
		label := bookingRequestActionLabel(change.ActionType)
		detail := buildSlotLabel(change.NewSlotDate, change.NewSlotHour, change.NewActivity, change.NewQuantity)
		if change.ActionType == "cancellation_requested" || change.ActionType == "cancellation_request_rejected" {
			detail = strings.TrimSpace(change.CustomerMessage)
			if detail == "" {
				detail = strings.TrimSpace(change.ReviewNote)
			}
		}
		if change.ActionType == bookingStatusCancelled || change.ActionType == bookingStatusCompleted || change.ActionType == bookingStatusNoShow {
			detail = bookingCommunicationEventTypeLabel(change.ActionType)
			if strings.TrimSpace(change.CustomerMessage) != "" {
				detail = change.CustomerMessage
			}
		}
		items = append(items, CustomerBookingTimelineItem{
			Label:  label,
			Detail: detail,
			When:   change.ChangedAt,
		})
	}
	for _, communication := range bookingCommunicationsFor(communications, schedule.ID) {
		if communication.Status != bookingCommStatusSent {
			continue
		}
		items = append(items, CustomerBookingTimelineItem{
			Label:   bookingCommunicationEventLabel(communication),
			Detail:  "Customer " + strings.ToUpper(communication.Channel) + " sent",
			When:    communication.SentAt,
			IsEmail: communication.Channel == bookingCommChannelEmail,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].When.After(items[j].When)
	})
	return items
}

func adminBookingCommunicationRedirect(scheduleID int64, status string, slotDate string) string {
	if status == "pending" {
		return "/admin/booking-requests"
	}
	return fmt.Sprintf("/admin/bookings?action=view&id=%d&date=%s#schedule-view", scheduleID, url.QueryEscape(slotDate))
}

func safeReturnPath(raw string, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return fallback
	}
	return raw
}
