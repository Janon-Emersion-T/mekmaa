package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	bookingSlotStartHour = 6
	bookingSlotEndHour   = 22
)

var (
	bookingSlotIncrement = 15 * time.Minute
	bookingSlotDuration  = time.Hour
)

func bookingHours() []string {
	var hours []string
	for hour := bookingSlotStartHour; hour <= bookingSlotEndHour; hour++ {
		for minute := 0; minute < 60; minute += 15 {
			hours = append(hours, fmt.Sprintf("%02d:%02d", hour, minute))
		}
	}
	return hours
}

func bookingSlotStartTime(slotDate string, slotHour string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(slotDate)+" "+strings.TrimSpace(slotHour), time.Local)
}

func bookingSlotEndTime(slotDate string, slotHour string) (time.Time, error) {
	start, err := bookingSlotStartTime(slotDate, slotHour)
	if err != nil {
		return time.Time{}, err
	}
	return start.Add(bookingSlotDuration), nil
}

func bookingSlotsOverlap(leftDate, leftHour, rightDate, rightHour string) bool {
	leftStart, err := bookingSlotStartTime(leftDate, leftHour)
	if err != nil {
		return false
	}
	leftEnd := leftStart.Add(bookingSlotDuration)
	rightStart, err := bookingSlotStartTime(rightDate, rightHour)
	if err != nil {
		return false
	}
	rightEnd := rightStart.Add(bookingSlotDuration)
	return leftStart.Before(rightEnd) && rightStart.Before(leftEnd)
}

func scheduleOverlapsSlot(schedule SpaceSchedule, slotDate string, slotHour string) bool {
	return bookingSlotsOverlap(schedule.SlotDate, schedule.SlotHour, slotDate, slotHour)
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

func trainingProgramIDsFromRequest(r *http.Request) []int64 {
	values := r.Form["training_program_id"]
	seen := make(map[int64]struct{}, len(values))
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := parsePositiveInt64(value)
		if err != nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
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
		SlotDate:     strings.TrimSpace(r.FormValue("slot_date")),
		SlotHour:     strings.TrimSpace(r.FormValue("slot_hour")),
		EntryType:    entryType,
		Activity:     activity,
		Quantity:     quantity,
		Title:        strings.TrimSpace(r.FormValue("title")),
		Notes:        strings.TrimSpace(r.FormValue("notes")),
		ReferralCode: strings.ToUpper(strings.TrimSpace(r.FormValue("referral_code"))),
	}
}

func pricingRuleFromRequest(r *http.Request) (PricingRule, error) {
	gameID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("game_id")), 10, 64)
	if err != nil || gameID <= 0 {
		return PricingRule{}, errors.New("valid game is required")
	}
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
		GameID:         gameID,
		Quantity:       quantity,
		WeekdayOffPeak: weekdayOffPeak,
		WeekdayPeak:    weekdayPeak,
		WeekendOffPeak: weekendOffPeak,
		WeekendPeak:    weekendPeak,
	}, nil
}

func oneToOneOfferingFromRequest(r *http.Request) (OneToOneOffering, error) {
	price, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price")), 64)
	if err != nil {
		return OneToOneOffering{}, errors.New("valid price is required")
	}
	sessionCount, err := strconv.Atoi(strings.TrimSpace(r.FormValue("session_count")))
	if err != nil || sessionCount <= 0 {
		sessionCount = 1
	}
	return OneToOneOffering{
		Name:         strings.TrimSpace(r.FormValue("name")),
		Game:         strings.ToLower(strings.TrimSpace(r.FormValue("game"))),
		Audience:     strings.ToLower(strings.TrimSpace(r.FormValue("audience"))),
		Occurrence:   strings.ToLower(strings.TrimSpace(r.FormValue("occurrence"))),
		SessionCount: sessionCount,
		Price:        price,
		Active:       r.FormValue("active") == "1",
	}, nil
}

func validateOneToOneOffering(offering OneToOneOffering, activities []CourtActivity, games []Game) error {
	var selectedGame *Game
	for i := range games {
		if games[i].Active && games[i].Activity == offering.Game {
			selectedGame = &games[i]
			break
		}
	}

	switch {
	case strings.TrimSpace(offering.Name) == "":
		return errors.New("name is required")
	case strings.TrimSpace(offering.Game) == "":
		return errors.New("game is required")
	case offering.Game == "training":
		return errors.New("training is reserved for internal schedules; choose a customer-bookable game")
	case selectedGame == nil:
		return errors.New("selected game is not available in the games list")
	case !bookingGameExists(selectedGame.ID, activities):
		return errors.New("selected game is not active in court manager")
	case offering.Audience != "local" && offering.Audience != "foreign":
		return errors.New("who must be local or foreign")
	case offering.Occurrence != "per_day" && offering.Occurrence != "per_week" && offering.Occurrence != "per_month":
		return errors.New("occurrence must be per day, per week, or per month")
	case offering.Occurrence == "per_day" && offering.SessionCount != 1:
		return errors.New("per day setups must use exactly 1 session")
	case offering.Occurrence != "per_day" && offering.SessionCount <= 0:
		return errors.New("session count must be at least 1")
	case offering.Price < 0:
		return errors.New("price must be zero or greater")
	default:
		return nil
	}
}

func bookingGameExists(gameID int64, activities []CourtActivity) bool {
	if gameID <= 0 {
		return false
	}

	for _, candidate := range activities {
		if candidate.Active && candidate.GameID == gameID {
			return true
		}
	}

	return false
}

func gameFromRequest(r *http.Request) (Game, error) {
	sortOrder := 0
	if rawSortOrder := strings.TrimSpace(r.FormValue("sort_order")); rawSortOrder != "" {
		parsed, err := strconv.Atoi(rawSortOrder)
		if err != nil {
			return Game{}, errors.New("valid sort order is required")
		}
		sortOrder = parsed
	}

	return Game{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Activity:    strings.ToLower(strings.TrimSpace(r.FormValue("activity"))),
		Description: strings.TrimSpace(r.FormValue("description")),
		Active:      r.FormValue("active") == "1",
		SortOrder:   sortOrder,
	}, nil
}

var gameActivitySanitizer = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeGameActivitySlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = gameActivitySanitizer.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	return name
}

func oneToOneBookingFormValues(r *http.Request) (int64, string, string, string, int, float64, int64, float64, string, string, error) {
	offeringID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("offering_id")), 10, 64)
	if err != nil || offeringID <= 0 {
		return 0, "", "", "", 0, 0, 0, 0, "", "", errors.New("valid 1 to 1 selection is required")
	}
	sessions, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sessions")))
	if err != nil || sessions <= 0 {
		return 0, "", "", "", 0, 0, 0, 0, "", "", errors.New("valid sessions count is required")
	}
	discountedPrice := float64(-1)
	rawDiscountedPrice := strings.TrimSpace(r.FormValue("discounted_price"))
	if rawDiscountedPrice != "" {
		parsedDiscountedPrice, err := strconv.ParseFloat(rawDiscountedPrice, 64)
		if err != nil {
			return 0, "", "", "", 0, 0, 0, 0, "", "", errors.New("valid final package price is required")
		}
		discountedPrice = parsedDiscountedPrice
	}
	coachUserID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("coach_user_id")), 10, 64)
	if err != nil || coachUserID <= 0 {
		return 0, "", "", "", 0, 0, 0, 0, "", "", errors.New("select a coach")
	}

	coachFee, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("coach_fee")), 64)
	if err != nil {
		return 0, "", "", "", 0, 0, 0, 0, "", "", errors.New("valid coach fee is required")
	}
	return offeringID,
		strings.TrimSpace(r.FormValue("customer_name")),
		strings.TrimSpace(r.FormValue("slot_date")),
		strings.TrimSpace(r.FormValue("slot_hour")),
		sessions,
		discountedPrice,
		coachUserID,
		coachFee,
		strings.TrimSpace(r.FormValue("notes")),
		strings.ToUpper(strings.TrimSpace(r.FormValue("referral_code"))),
		nil
}

func oneToOneBookingUpdateFormValues(
	r *http.Request,
) (int64, string, int, float64, int64, float64, string, error) {
	bookingID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("booking_id")),
		10,
		64,
	)
	if err != nil || bookingID <= 0 {
		return 0, "", 0, 0, 0, 0, "", errors.New("valid 1 to 1 package is required")
	}

	sessions, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sessions")))
	if err != nil || sessions <= 0 {
		return 0, "", 0, 0, 0, 0, "", errors.New("valid sessions count is required")
	}

	discountedPrice, err := strconv.ParseFloat(
		strings.TrimSpace(r.FormValue("discounted_price")),
		64,
	)
	if err != nil {
		return 0, "", 0, 0, 0, 0, "", errors.New("valid final package price is required")
	}

	coachUserID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("coach_user_id")),
		10,
		64,
	)
	if err != nil || coachUserID <= 0 {
		return 0, "", 0, 0, 0, 0, "", errors.New("select a coach")
	}

	coachFee, err := strconv.ParseFloat(
		strings.TrimSpace(r.FormValue("coach_fee")),
		64,
	)
	if err != nil {
		return 0, "", 0, 0, 0, 0, "", errors.New("valid coach fee is required")
	}

	return bookingID,
		strings.TrimSpace(r.FormValue("customer_name")),
		sessions,
		discountedPrice,
		coachUserID,
		coachFee,
		strings.TrimSpace(r.FormValue("notes")),
		nil
}

func bookingActivityExists(activity string, activities []CourtActivity) bool {
	activity = strings.TrimSpace(activity)
	for _, candidate := range activities {
		if candidate.Active && candidate.Activity == activity {
			return true
		}
	}
	return false
}

func (a *App) eventFromRequest(r *http.Request) (Event, error) {
	imagePath, err := a.uploadedEventImagePath(r)
	if err != nil {
		return Event{}, err
	}
	gameID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("game_id")), 10, 64)
	if err != nil || gameID <= 0 {
		return Event{}, errors.New("valid game is required")
	}
	return Event{
		GameID:               gameID,
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
		EntryType:    "booking",
		SlotDate:     calendarDate,
		SlotHour:     strings.TrimSpace(r.URL.Query().Get("hour")),
		Activity:     strings.ToLower(strings.TrimSpace(r.URL.Query().Get("activity"))),
		Quantity:     1,
		Title:        strings.TrimSpace(r.URL.Query().Get("title")),
		Notes:        strings.TrimSpace(r.URL.Query().Get("notes")),
		ReferralCode: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("ref"))),
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
	if referralCode := strings.TrimSpace(query.Get("referral_code")); referralCode != "" {
		schedule.ReferralCode = strings.ToUpper(referralCode)
	}
	if referralCode := strings.TrimSpace(query.Get("ref")); referralCode != "" {
		schedule.ReferralCode = strings.ToUpper(referralCode)
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
		Name:              strings.TrimSpace(r.FormValue("name")),
		Code:              strings.ToUpper(strings.TrimSpace(r.FormValue("code"))),
		Description:       strings.TrimSpace(r.FormValue("description")),
		TrainingProgramID: parseInt64Query(r.FormValue("training_program_id")),
	}
}

func studentGroupSessionsFromRequest(r *http.Request) []StudentGroupSession {
	titles := r.Form["session_title"]
	days := r.Form["session_day"]
	starts := r.Form["session_start_time"]
	ends := r.Form["session_end_time"]
	maxLen := len(titles)
	if len(days) > maxLen {
		maxLen = len(days)
	}
	if len(starts) > maxLen {
		maxLen = len(starts)
	}
	if len(ends) > maxLen {
		maxLen = len(ends)
	}

	sessions := make([]StudentGroupSession, 0, maxLen)
	for i := 0; i < maxLen; i++ {
		session := StudentGroupSession{
			Title:     strings.TrimSpace(valueAt(titles, i)),
			DayOfWeek: strings.ToLower(strings.TrimSpace(valueAt(days, i))),
			StartTime: strings.TrimSpace(valueAt(starts, i)),
			EndTime:   strings.TrimSpace(valueAt(ends, i)),
			Active:    true,
		}
		if session.Title == "" && session.DayOfWeek == "" && session.StartTime == "" && session.EndTime == "" {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func validateAdmission(admission Admission) error {
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
	case group.TrainingProgramID <= 0:
		return errors.New("training programme is required")
	default:
		return nil
	}
}

func validateStudentGroupSessions(sessions []StudentGroupSession) error {
	if len(sessions) == 0 {
		return errors.New("at least one timetable session is required")
	}
	for _, session := range sessions {
		switch session.DayOfWeek {
		case "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
		default:
			return errors.New("each session must have a valid weekday")
		}
		if strings.TrimSpace(session.Title) == "" {
			return errors.New("each session must have a title")
		}
		start, err := time.Parse("15:04", session.StartTime)
		if err != nil {
			return errors.New("each session must have a valid start time")
		}
		end, err := time.Parse("15:04", session.EndTime)
		if err != nil {
			return errors.New("each session must have a valid end time")
		}
		if !start.Before(end) {
			return errors.New("session end time must be after the start time")
		}
	}
	return nil
}

func validateCourt(court Court) error {
	switch {
	case strings.TrimSpace(court.Name) == "":
		return errors.New("court name is required")
	case strings.TrimSpace(court.Code) == "":
		return errors.New("court code is required")
	case strings.TrimSpace(court.Description) == "":
		return errors.New("description is required")
	case court.SortOrder < 0:
		return errors.New("display order must be zero or greater")
	default:
		return nil
	}
}

func courtActivityFromRequest(
	r *http.Request,
	games []Game,
) (CourtActivity, error) {
	courtID, err := strconv.ParseInt(
		strings.TrimSpace(r.FormValue("court_id")),
		10,
		64,
	)
	if err != nil || courtID <= 0 {
		return CourtActivity{}, errors.New("valid court is required")
	}

	maxQuantity, err := strconv.Atoi(
		strings.TrimSpace(r.FormValue("max_quantity")),
	)
	if err != nil {
		return CourtActivity{}, errors.New("valid maximum quantity is required")
	}

	sortOrder, err := strconv.Atoi(
		strings.TrimSpace(r.FormValue("sort_order")),
	)
	if err != nil {
		return CourtActivity{}, errors.New("valid sort order is required")
	}

	activityType := strings.ToLower(
		strings.TrimSpace(r.FormValue("activity_type")),
	)

	activity := CourtActivity{
		CourtID:     courtID,
		DisplayName: strings.TrimSpace(r.FormValue("display_name")),
		MaxQuantity: maxQuantity,
		AutoAccept:  r.FormValue("auto_accept") == "1",
		Active:      r.FormValue("active") == "1",
		SortOrder:   sortOrder,
	}

	switch activityType {
	case "training":
		activity.Activity = "training"
		activity.GameID = 0
		if activity.DisplayName == "" {
			activity.DisplayName = "Training"
		}
	case "game":
		gameID, err := strconv.ParseInt(
			strings.TrimSpace(r.FormValue("game_id")),
			10,
			64,
		)
		if err != nil || gameID <= 0 {
			return CourtActivity{}, errors.New("valid game is required")
		}

		var selectedGame *Game
		for i := range games {
			if games[i].ID == gameID {
				selectedGame = &games[i]
				break
			}
		}
		if selectedGame == nil {
			return CourtActivity{}, errors.New("selected game was not found")
		}

		activity.GameID = selectedGame.ID
		activity.Activity = selectedGame.Activity
		if activity.DisplayName == "" {
			activity.DisplayName = selectedGame.Name
		}
	default:
		return CourtActivity{}, errors.New("valid activity type is required")
	}

	return activity, nil
}

func validateCourtActivity(activity CourtActivity) error {
	switch {
	case activity.CourtID <= 0:
		return errors.New("court is required")
	case strings.TrimSpace(activity.Activity) == "":
		return errors.New("activity is required")
	case strings.TrimSpace(activity.DisplayName) == "":
		return errors.New("display name is required")
	case activity.MaxQuantity <= 0:
		return errors.New("maximum quantity must be at least 1")
	case activity.SortOrder < 0:
		return errors.New("sort order must be zero or greater")
	case activity.Activity == "training" && activity.GameID > 0:
		return errors.New("training cannot be linked to a public game")
	default:
		return nil
	}
}

func validatePricingRule(rule PricingRule) error {
	switch {
	case rule.GameID <= 0:
		return errors.New("game is required")
	case rule.Quantity <= 0:
		return errors.New("quantity must be greater than 0")
	case rule.WeekdayOffPeak < 0 || rule.WeekdayPeak < 0 || rule.WeekendOffPeak < 0 || rule.WeekendPeak < 0:
		return errors.New("prices cannot be negative")
	}
	return nil
}

func validatePricingRuleAgainstOptions(
	rule PricingRule,
	activities []CourtActivity,
	layouts []CourtLayout,
) error {
	if !bookingOptionExists(
		rule.Activity,
		rule.Quantity,
		activities,
		layouts,
	) {
		return errors.New(
			"selected quantity is not available in court manager",
		)
	}
	return nil
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

func validateEnrollment(enrollment StudentEnrollment) error {
	switch {
	case enrollment.AdmissionID <= 0:
		return errors.New("student is required")
	case enrollment.TrainingProgramID <= 0:
		return errors.New("training programme is required")
	case strings.TrimSpace(enrollment.TrainingProgramName) == "":
		return errors.New("training programme name is required")
	case math.IsNaN(enrollment.DiscountedMonthlyFee),
		math.IsInf(enrollment.DiscountedMonthlyFee, 0),
		enrollment.DiscountedMonthlyFee < 0:
		return errors.New("discounted monthly fee cannot be negative")
	default:
		return nil
	}
}

func validateEvent(event Event) error {
	switch {
	case event.GameID <= 0:
		return errors.New("game is required")
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

func validateGame(game Game, activities []CourtActivity) error {
	switch {
	case strings.TrimSpace(game.Name) == "":
		return errors.New("name is required")
	case strings.TrimSpace(game.Activity) == "":
		return errors.New("internal activity is required")
	case game.Activity == "training":
		return errors.New("training cannot be used as a public game")
	case game.SortOrder < 0:
		return errors.New("sort order must be zero or greater")
	default:
		return nil
	}
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

	slotStart, err := bookingSlotStartTime(slotDate, slotHour)
	if err != nil {
		return false
	}
	slotEnd := slotStart.Add(bookingSlotDuration)
	closureStart, err := bookingSlotStartTime(closure.ClosureDate, closure.StartHour)
	if err != nil {
		slotHour = strings.TrimSpace(slotHour)
		return slotHour >= closure.StartHour && slotHour < closure.EndHour
	}
	closureEnd, err := bookingSlotStartTime(closure.ClosureDate, closure.EndHour)
	if err != nil {
		slotHour = strings.TrimSpace(slotHour)
		return slotHour >= closure.StartHour && slotHour < closure.EndHour
	}
	return slotStart.Before(closureEnd) && closureStart.Before(slotEnd)
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

const companyHistoricalEntryStartDate = "2026-07-01"

func historicalEntryStartDate() (time.Time, error) {
	return time.Parse("2006-01-02", companyHistoricalEntryStartDate)
}

func validateHistoricalEntryDateValue(
	value string,
	label string,
) error {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return errors.New("valid " + label + " is required")
	}
	return validateHistoricalEntryTime(parsed, label)
}

func validateHistoricalEntryTime(
	value time.Time,
	label string,
) error {
	historicalStart, err := historicalEntryStartDate()
	if err != nil {
		return errors.New("invalid historical booking configuration")
	}
	localValue := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.Local)
	localStart := time.Date(historicalStart.Year(), historicalStart.Month(), historicalStart.Day(), 0, 0, 0, 0, time.Local)
	if localValue.Before(localStart) {
		return fmt.Errorf("%s cannot be before %s", label, companyHistoricalEntryStartDate)
	}
	return nil
}

func validateAdminScheduleDate(schedule SpaceSchedule) error {
	slotDate, err := time.Parse("2006-01-02", schedule.SlotDate)
	if err != nil {
		return errors.New("valid booking date is required")
	}

	historicalStart, err := historicalEntryStartDate()
	if err != nil {
		return errors.New("invalid historical booking configuration")
	}

	if slotDate.Before(historicalStart) {
		return fmt.Errorf(
			"bookings before %s cannot be entered",
			companyHistoricalEntryStartDate,
		)
	}

	return nil
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
