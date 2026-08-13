package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func activeSchedulesOnly(schedules []SpaceSchedule) []SpaceSchedule {
	active := make([]SpaceSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		if bookingStatusConsumesCapacity(schedule.Status) {
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
		iNeedsAction := requests[i].Status == bookingStatusPending || requests[i].Status == bookingStatusHeld || requests[i].Status == bookingStatusReschedulePending
		jNeedsAction := requests[j].Status == bookingStatusPending || requests[j].Status == bookingStatusHeld || requests[j].Status == bookingStatusReschedulePending
		if iNeedsAction != jNeedsAction {
			return iNeedsAction
		}
		if iNeedsAction && jNeedsAction {
			iStart, _ := slotStartTime(&requests[i])
			jStart, _ := slotStartTime(&requests[j])
			if !iStart.Equal(jStart) {
				return iStart.Before(jStart)
			}
		}
		return requests[i].CreatedAt.After(requests[j].CreatedAt)
	})
	return requests
}

func buildBookingRequestStats(requests []SpaceSchedule) []Stat {
	pending := 0
	held := 0
	reschedulePending := 0
	confirmed := 0
	rejected := 0
	receivedToday := 0
	today := time.Now().Format("2006-01-02")
	for _, request := range requests {
		switch request.Status {
		case bookingStatusPending:
			pending++
		case bookingStatusHeld:
			held++
		case bookingStatusReschedulePending:
			reschedulePending++
		case bookingStatusConfirmed:
			confirmed++
		case bookingStatusRejected:
			rejected++
		}
		if request.CreatedAt.In(time.Local).Format("2006-01-02") == today {
			receivedToday++
		}
	}
	return []Stat{
		{Label: "Pending", Value: strconv.Itoa(pending)},
		{Label: "Held", Value: strconv.Itoa(held)},
		{Label: "Reschedule pending", Value: strconv.Itoa(reschedulePending)},
		{Label: "Confirmed", Value: strconv.Itoa(confirmed)},
		{Label: "Rejected", Value: strconv.Itoa(rejected)},
		{Label: "Received today", Value: strconv.Itoa(receivedToday)},
	}
}

func unresolvedBookingRequestStatus(status string) bool {
	return status == bookingStatusPending || status == bookingStatusHeld || status == bookingStatusReschedulePending
}

func buildBookingReminders(requests []SpaceSchedule, now time.Time) []BookingReminder {
	reminders := make([]BookingReminder, 0)
	for _, request := range requests {
		if !unresolvedBookingRequestStatus(request.Status) {
			continue
		}
		start, err := slotStartTime(&request)
		if err != nil {
			continue
		}
		minutesUntil := int(start.Sub(now).Minutes())
		if minutesUntil > 120 {
			continue
		}
		reminder := BookingReminder{
			Schedule:          request,
			MinutesUntilStart: minutesUntil,
		}
		switch {
		case minutesUntil <= 0:
			reminder.UrgencyLabel = "Overdue"
			reminder.UrgencyTone = "border-red-300 bg-red-50 text-red-900"
			reminder.RemainingLabel = "Playing time reached"
			reminder.IsOverdue = true
		case minutesUntil <= 60:
			reminder.UrgencyLabel = "Urgent"
			reminder.UrgencyTone = "border-rose-300 bg-rose-50 text-rose-900"
			reminder.RemainingLabel = fmt.Sprintf("%d minutes remaining", minutesUntil)
		case minutesUntil <= 90:
			reminder.UrgencyLabel = "Important"
			reminder.UrgencyTone = "border-amber-300 bg-amber-50 text-amber-900"
			reminder.RemainingLabel = fmt.Sprintf("%d minutes remaining", minutesUntil)
		default:
			reminder.UrgencyLabel = "Attention"
			reminder.UrgencyTone = "border-sky-300 bg-sky-50 text-sky-900"
			reminder.RemainingLabel = fmt.Sprintf("%d minutes remaining", minutesUntil)
			reminder.IsApproachingWindow = true
		}
		reminders = append(reminders, reminder)
	}
	sort.Slice(reminders, func(i, j int) bool {
		if reminders[i].IsOverdue != reminders[j].IsOverdue {
			return reminders[i].IsOverdue
		}
		return reminders[i].MinutesUntilStart < reminders[j].MinutesUntilStart
	})
	return reminders
}

func buildBookingAttentionStats(reminders []BookingReminder, pendingCount int, heldCount int, reschedulePendingCount int) []Stat {
	approaching := 0
	insideWindow := 0
	overdue := 0
	for _, reminder := range reminders {
		switch {
		case reminder.IsOverdue:
			overdue++
		case reminder.MinutesUntilStart <= 120:
			insideWindow++
			if reminder.IsApproachingWindow {
				approaching++
			}
		}
	}
	return []Stat{
		{Label: "Pending", Value: strconv.Itoa(pendingCount)},
		{Label: "Held", Value: strconv.Itoa(heldCount)},
		{Label: "Reschedule pending", Value: strconv.Itoa(reschedulePendingCount)},
		{Label: "Approaching time", Value: strconv.Itoa(approaching)},
		{Label: "Inside 120 minutes", Value: strconv.Itoa(insideWindow)},
		{Label: "Overdue", Value: strconv.Itoa(overdue)},
	}
}

func (a *App) expireOverdueBookingRequests(now time.Time) {
	rows, err := a.db.Query(`
		SELECT id
		FROM space_schedules
		WHERE entry_type = 'booking'
		  AND status IN ('pending', 'held', 'reschedule_pending')
		  AND (slot_date < ? OR (slot_date = ? AND slot_hour <= ?))
	`, now.Format("2006-01-02"), now.Format("2006-01-02"), now.Format("15:04"))
	if err != nil {
		log.Printf("list overdue booking requests: %v", err)
		return
	}
	defer rows.Close()

	var scheduleIDs []int64
	for rows.Next() {
		var scheduleID int64
		if err := rows.Scan(&scheduleID); err != nil {
			log.Printf("scan overdue booking request: %v", err)
			return
		}
		scheduleIDs = append(scheduleIDs, scheduleID)
	}
	for _, scheduleID := range scheduleIDs {
		updated, changeID, err := a.transitionBookingRequestStatus(scheduleID, bookingStatusExpired, "Request expired after the requested playing time passed without confirmation.", "Your requested time passed before the booking could be confirmed. Please submit a new request if you still need the slot.", "system_expiry", 0)
		if err != nil {
			continue
		}
		if _, commErr := a.sendBookingCommunicationEvent(scheduleID, bookingCommEventExpired, "", fmt.Sprintf("schedule:%d:%s:change:%d", scheduleID, bookingCommEventExpired, changeID), 0); commErr != nil {
			log.Printf("send booking expiry communication: %v", commErr)
		}
		_ = updated
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
	case bookingStatusPending:
		return "border-amber-200 bg-amber-50 text-amber-900"
	case bookingStatusHeld:
		return "border-violet-200 bg-violet-50 text-violet-900"
	case bookingStatusConfirmed:
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case bookingStatusRejected:
		return "border-red-200 bg-red-50 text-red-800"
	case bookingStatusReschedulePending:
		return "border-sky-200 bg-sky-50 text-sky-900"
	case bookingStatusCancelled:
		return "border-slate-300 bg-slate-100 text-slate-800"
	case bookingStatusCompleted:
		return "border-sky-200 bg-sky-50 text-sky-900"
	case bookingStatusNoShow:
		return "border-orange-200 bg-orange-50 text-orange-900"
	case bookingStatusExpired:
		return "border-zinc-300 bg-zinc-100 text-zinc-800"
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

func bookingPaymentsForSchedule(collections []BookingPaymentCollection, scheduleID int64) []BookingPaymentCollection {
	filtered := make([]BookingPaymentCollection, 0)
	for _, collection := range collections {
		if collection.ScheduleID == scheduleID {
			filtered = append(filtered, collection)
		}
	}
	return filtered
}

func activeBookingPaymentsForSchedule(collections []BookingPaymentCollection, scheduleID int64) []BookingPaymentCollection {
	filtered := make([]BookingPaymentCollection, 0)
	for _, collection := range collections {
		if collection.ScheduleID == scheduleID && !collection.Voided {
			filtered = append(filtered, collection)
		}
	}
	return filtered
}

func customerVisibleBookingPaymentsForSchedule(collections []BookingPaymentCollection, scheduleID int64) []BookingPaymentCollection {
	filtered := make([]BookingPaymentCollection, 0)
	for _, collection := range collections {
		if collection.ScheduleID != scheduleID {
			continue
		}
		if collection.Voided {
			filtered = append(filtered, BookingPaymentCollection{
				ReceiptNumber: collection.ReceiptNumber,
				Amount:        collection.Amount,
				PaymentMethod: collection.PaymentMethod,
				CollectedAt:   collection.CollectedAt,
				Voided:        true,
			})
			continue
		}
		filtered = append(filtered, BookingPaymentCollection{
			ReceiptNumber: collection.ReceiptNumber,
			Amount:        collection.Amount,
			PaymentMethod: collection.PaymentMethod,
			CollectedAt:   collection.CollectedAt,
			Voided:        false,
		})
	}
	return filtered
}

func scheduleIDsFromFinancials(financials []BookingFinancial) []int64 {
	ids := make([]int64, 0, len(financials))
	for _, financial := range financials {
		ids = appendInt64Unique(ids, financial.ScheduleID)
	}
	return ids
}

func bookingCanCollectPayment(user *User, schedule *SpaceSchedule) bool {
	if user == nil || schedule == nil {
		return false
	}
	if schedule.EntryType != "booking" {
		return false
	}
	if !bookingPaymentCollectibleStatus(schedule.Status) {
		return false
	}
	return containsPermission(user.Permissions, "finance.manage") ||
		containsPermission(user.Permissions, "space_bookings.manage") ||
		containsPermission(user.Permissions, "booking_requests.manage")
}

func bookingCanVoidPayment(user *User) bool {
	return user != nil && containsPermission(user.Permissions, "finance.manage")
}

func bookingPaymentInactiveMessage(schedule *SpaceSchedule) string {
	if schedule == nil {
		return "Payment collection is unavailable for this booking."
	}
	switch schedule.Status {
	case bookingStatusPending, bookingStatusHeld, bookingStatusReschedulePending:
		return "Payment collection becomes available after the booking is confirmed."
	case bookingStatusRejected, bookingStatusExpired:
		return "Payment collection is unavailable because this request was not confirmed."
	default:
		return "Payment collection is unavailable for this booking."
	}
}

func bookingOutstandingExcess(amount, outstanding float64) float64 {
	excess := normalizeMoney(amount - outstanding)
	if excess < 0 {
		return 0
	}
	return excess
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
	case bookingCommEventHeld:
		return "Booking held"
	case bookingCommEventConfirmed:
		return "Booking confirmed"
	case bookingCommEventRejected:
		return "Booking rejected"
	case bookingCommEventRescheduledPending:
		return "Pending reschedule"
	case bookingCommEventRescheduledConfirmed:
		return "Rescheduled and confirmed"
	case bookingCommEventCancellationRequested:
		return "Cancellation requested"
	case bookingCommEventCancellationApproved:
		return "Cancellation approved"
	case bookingCommEventCancellationRejected:
		return "Cancellation request rejected"
	case bookingCommEventCancelledByAdmin:
		return "Booking cancelled"
	case bookingCommEventCompleted:
		return "Booking completed"
	case bookingCommEventNoShow:
		return "Booking marked no-show"
	case bookingCommEventExpired:
		return "Booking expired"
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	collections, err := listBookingPaymentCollectionsForScheduleIDsQuery(queryer, scheduleIDs)
	if err != nil {
		return nil, err
	}
	return enrichBookingFinancials(financials, collections), nil
}

func listBookingPaymentCollectionsForScheduleIDsQuery(queryer sqlQueryer, scheduleIDs []int64) ([]BookingPaymentCollection, error) {
	if len(scheduleIDs) == 0 {
		return nil, nil
	}
	query, args := scheduleIDScopedQuery(`
		SELECT
			bpc.id,
			bpc.schedule_id,
			bpc.finance_transaction_id,
			ft.receipt_number,
			ft.person_name,
			ft.description,
			bpc.amount,
			bpc.payment_method,
			COALESCE(bpc.payment_note, ''),
			COALESCE(bpc.collected_by_user_id, 0),
			COALESCE(collector.name, ''),
			bpc.collected_at,
			bpc.created_at,
			bpc.voided,
			COALESCE(bpc.void_reason, ''),
			COALESCE(bpc.voided_by_user_id, 0),
			COALESCE(voider.name, ''),
			bpc.voided_at
		FROM booking_payment_collections bpc
		JOIN finance_transactions ft
			ON ft.id = bpc.finance_transaction_id
		LEFT JOIN users collector
			ON collector.id = bpc.collected_by_user_id
		LEFT JOIN users voider
			ON voider.id = bpc.voided_by_user_id
		WHERE bpc.schedule_id IN (%s)
		ORDER BY bpc.collected_at DESC, bpc.id DESC
	`, scheduleIDs)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collections []BookingPaymentCollection
	for rows.Next() {
		var collection BookingPaymentCollection
		var voided int
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&collection.ID,
			&collection.ScheduleID,
			&collection.FinanceTransactionID,
			&collection.ReceiptNumber,
			&collection.PersonName,
			&collection.Description,
			&collection.Amount,
			&collection.PaymentMethod,
			&collection.PaymentNote,
			&collection.CollectedByUserID,
			&collection.CollectedByUserName,
			&collection.CollectedAt,
			&collection.CreatedAt,
			&voided,
			&collection.VoidReason,
			&collection.VoidedByUserID,
			&collection.VoidedByUserName,
			&voidedAt,
		); err != nil {
			return nil, err
		}
		collection.Voided = voided == 1
		if voidedAt.Valid {
			collection.VoidedAt = voidedAt.Time
		}
		collections = append(collections, collection)
	}
	return collections, rows.Err()
}

func bookingPaymentStatusValue(financial BookingFinancial, collections []BookingPaymentCollection) string {
	activeCount := 0
	voidedCount := 0
	totalCollected := 0.0
	for _, collection := range collections {
		if collection.Voided {
			voidedCount++
			continue
		}
		activeCount++
		totalCollected = normalizeMoney(totalCollected + collection.Amount)
	}
	if activeCount == 0 {
		if voidedCount > 0 {
			return "voided"
		}
		return "unpaid"
	}
	switch {
	case moneyEquals(totalCollected, financial.QuotedAmount):
		return "paid"
	case totalCollected > financial.QuotedAmount:
		return "overpaid"
	default:
		return "partially_paid"
	}
}

func enrichBookingFinancials(financials []BookingFinancial, collections []BookingPaymentCollection) []BookingFinancial {
	bySchedule := make(map[int64][]BookingPaymentCollection)
	for _, collection := range collections {
		bySchedule[collection.ScheduleID] = append(bySchedule[collection.ScheduleID], collection)
	}
	for i := range financials {
		scheduleCollections := bySchedule[financials[i].ScheduleID]
		totalCollected := 0.0
		activeCount := 0
		voidedCount := 0
		for _, collection := range scheduleCollections {
			if collection.Voided {
				voidedCount++
				continue
			}
			activeCount++
			totalCollected = normalizeMoney(totalCollected + collection.Amount)
			if financials[i].LastPaymentDate.IsZero() || collection.CollectedAt.After(financials[i].LastPaymentDate) {
				financials[i].LastPaymentDate = collection.CollectedAt
				financials[i].LastPaymentByUserID = collection.CollectedByUserID
				financials[i].LastPaymentByUserName = collection.CollectedByUserName
			}
		}
		financials[i].TotalCollected = totalCollected
		financials[i].OutstandingAmount = normalizeMoney(financials[i].QuotedAmount - totalCollected)
		financials[i].ActivePaymentCount = activeCount
		financials[i].VoidedPaymentCount = voidedCount
		financials[i].PaymentStatus = bookingPaymentStatusValue(financials[i], scheduleCollections)
		financials[i].Paid = financials[i].PaymentStatus == "paid" || financials[i].PaymentStatus == "overpaid"
		if activeCount == 0 {
			financials[i].PaymentMethod = ""
			financials[i].PaidAt = time.Time{}
			financials[i].FinanceTransactionID = 0
		} else {
			financials[i].PaymentMethod = "cash"
			financials[i].PaidAt = financials[i].LastPaymentDate
		}
	}
	return financials
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
			COALESCE(brch.previous_status, ''),
			COALESCE(brch.new_status, ''),
			COALESCE(brch.change_source, ''),
			COALESCE(brch.finance_note, ''),
			brch.review_note,
			COALESCE(brch.customer_message, ''),
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
			&change.PreviousStatus,
			&change.NewStatus,
			&change.ChangeSource,
			&change.FinanceNote,
			&change.ReviewNote,
			&change.CustomerMessage,
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
	case "auto_confirmed":
		return "Automatically confirmed"
	case "rescheduled_confirmed":
		return "Rescheduled and confirmed"
	case "rescheduled":
		return "Rescheduled"
	case "cancellation_requested":
		return "Cancellation requested"
	case "cancellation_request_rejected":
		return "Cancellation request rejected"
	case bookingStatusHeld:
		return "Placed on hold"
	case bookingStatusExpired:
		return "Expired"
	case bookingStatusCancelled:
		return "Cancelled"
	case bookingStatusCompleted:
		return "Completed"
	case bookingStatusNoShow:
		return "No-show"
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

func enrollmentsForAdmission(enrollments []StudentEnrollment, admissionID int64) []StudentEnrollment {
	filtered := make([]StudentEnrollment, 0)
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID {
			filtered = append(filtered, enrollment)
		}
	}
	return filtered
}

func enrollmentCountForAdmission(enrollments []StudentEnrollment, admissionID int64) int {
	total := 0
	for _, enrollment := range enrollments {
		if enrollment.AdmissionID == admissionID {
			total++
		}
	}
	return total
}

func coachAttendanceRecordFor(records []CoachAttendanceRecord, userID int64) CoachAttendanceRecord {
	for _, record := range records {
		if record.UserID == userID {
			return record
		}
	}
	return CoachAttendanceRecord{UserID: userID, Status: "absent"}
}

func coachAttendanceCount(records []CoachAttendanceRecord, status string) int {
	total := 0
	for _, record := range records {
		if normalizeAttendanceStatus(record.Status) == status {
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

func gameNameFor(games []Game, activity string) string {
	for _, game := range games {
		if strings.EqualFold(game.Activity, activity) {
			return game.Name
		}
	}
	return activityLabel(activity)
}

func gameNameByID(games []Game, gameID int64) string {
	for _, game := range games {
		if game.ID == gameID {
			return game.Name
		}
	}
	return ""
}

func bookingProductLabelForGames(games []Game, activity string, quantity int) string {
	label := gameNameFor(games, activity)
	if quantity <= 1 {
		return label
	}
	return fmt.Sprintf("%s x%d", label, quantity)
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

func courtActivityFor(activities []CourtActivity, activityName string) *CourtActivity {
	for i := range activities {
		if activities[i].Activity == activityName {
			return &activities[i]
		}
	}
	return nil
}

func bookingRuleHasCompletePublicPricing(rule *PricingRule) bool {
	if rule == nil {
		return false
	}
	return rule.WeekdayOffPeak > 0 &&
		rule.WeekdayPeak > 0 &&
		rule.WeekendOffPeak > 0 &&
		rule.WeekendPeak > 0
}

func (a *App) listActiveUnpricedBookingOptions() ([]BookingPricingIssue, error) {
	activities, layouts, err := a.activeBookingConfiguration()
	if err != nil {
		return nil, err
	}
	pricings, err := a.listPricingRules()
	if err != nil {
		return nil, err
	}
	options := bookingOptionCatalog(activities, layouts)
	issues := make([]BookingPricingIssue, 0)
	for _, option := range options {
		if option.Activity == "training" {
			continue
		}
		rule := pricingRuleForOption(pricings, option.Activity, option.Quantity)
		if bookingRuleHasCompletePublicPricing(rule) {
			continue
		}
		issues = append(issues, BookingPricingIssue{
			Activity: option.Activity,
			Quantity: option.Quantity,
			Label:    option.Label,
		})
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Activity == issues[j].Activity {
			return issues[i].Quantity < issues[j].Quantity
		}
		return issues[i].Activity < issues[j].Activity
	})
	return issues, nil
}

func bookingPricingUnavailableMessage(activity string, quantity int) string {
	label := bookingProductLabel(activity, quantity)
	return fmt.Sprintf("Pricing is currently unavailable for %s. Please choose another activity or contact Mekmaa directly.", label)
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
