package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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
			filtered[i].BlockedReason = "Pricing is currently unavailable for the remaining bookable options"
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
		activeSlotSchedules := activeSchedulesOnly(slotSchedules)
		slotClosures := closuresForSlot(closures, slotDate, hour)

		bookingDraft := &SpaceSchedule{
			EntryType: "booking",
			SlotDate:  slotDate,
			SlotHour:  hour,
		}
		bookingOptions, blockedReason, _ := buildAdminBookingOptions(
			activeSlotSchedules,
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
			activeSlotSchedules,
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
				row.CollectedRevenue += financial.TotalCollected
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
	switch financial.PaymentStatus {
	case "paid":
		item.PaymentLabel = "Paid"
		item.PaymentTone = "text-emerald-700"
	case "partially_paid":
		item.PaymentLabel = "Part-paid"
		item.PaymentTone = "text-amber-700"
	case "overpaid":
		item.PaymentLabel = "Overpaid"
		item.PaymentTone = "text-sky-700"
	case "voided":
		item.PaymentLabel = "Voided"
		item.PaymentTone = "text-slate/55"
	default:
		if bookingPaymentCollectibleStatus(schedule.Status) {
			item.PaymentLabel = "Unpaid"
			item.PaymentTone = "text-red-700"
			item.IsUnpaid = true
		} else {
			item.PaymentLabel = "Quoted"
			item.PaymentTone = "text-amber-700"
		}
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
