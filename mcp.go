package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	mcpPlanStatusDraft     = "draft"
	mcpPlanStatusPending   = "pending"
	mcpPlanStatusConfirmed = "confirmed"
	mcpPlanStatusCancelled = "cancelled"
	mcpPlanStatusCompleted = "completed"

	mcpTierWeekdayOffPeak = "weekday_offpeak"
	mcpTierWeekdayPeak    = "weekday_peak"
	mcpTierWeekendOffPeak = "weekend_offpeak"
	mcpTierWeekendPeak    = "weekend_peak"
)

type MCPMonthlyCustomer struct {
	ID        int64
	UserID    int64
	Name      string
	Email     string
	Phone     string
	Active    bool
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MCPPricingBand struct {
	ID              int64
	Tier            string
	MinimumSessions int
	MaximumSessions int
	PricePerSession float64
	Active          bool
	EffectiveFrom   string
	EffectiveTo     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MCPPlanScheduleRule struct {
	ID        int64
	PlanID    int64
	Weekday   int
	StartHour string
	EndHour   string
	CreatedAt time.Time
}

type MCPPlanSession struct {
	ID              int64
	PlanID          int64
	SessionDate     string
	SessionHour     string
	Activity        string
	Quantity        int
	PricingTier     string
	PricingBandID   int64
	PricingBandMin  int
	PricingBandMax  int
	PricePerSession float64
	Amount          float64
	Status          string
	ConflictReason  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MCPMonthlyPlan struct {
	ID                int64
	CustomerID        int64
	CustomerName      string
	CustomerEmail     string
	UserID            int64
	PlanMonth         string
	GameID            int64
	Activity          string
	Quantity          int
	Title             string
	Status            string
	TotalSessions     int
	GrossAmount       float64
	TotalCollected    float64
	OutstandingAmount float64
	PaymentStatus     string
	Notes             string
	CreatedByUserID   int64
	RequestedByUserID int64
	ConfirmedAt       time.Time
	ConfirmedByUserID int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Rules             []MCPPlanScheduleRule
	Sessions          []MCPPlanSession
}

type MCPReceivable struct {
	Plan                  MCPMonthlyPlan
	LastCollectedAt       time.Time
	LastCollectedByName   string
	ActiveCollectionCount int
	PaymentCollections    []MCPPaymentCollection
}

type MCPPaymentCollection struct {
	ID                   int64
	PlanID               int64
	FinanceTransactionID int64
	Amount               float64
	PaymentMethod        string
	PaymentNote          string
	CollectedAt          time.Time
	Voided               bool
	VoidReason           string
}

type MCPPlanConflict struct {
	Date   string
	Hour   string
	Reason string
}

type MCPPlanPreview struct {
	Month         string
	Activity      string
	Quantity      int
	TotalSessions int
	GrossAmount   float64
	Sessions      []MCPPlanSession
	Conflicts     []MCPPlanConflict
}

func validMCPPlanStatus(status string) bool {
	switch canonicalBookingStatus(status) {
	case mcpPlanStatusDraft, mcpPlanStatusPending, mcpPlanStatusConfirmed, mcpPlanStatusCancelled, mcpPlanStatusCompleted:
		return true
	default:
		return false
	}
}

func mcpPlanConsumesCapacity(status string) bool {
	switch canonicalBookingStatus(status) {
	case mcpPlanStatusPending, mcpPlanStatusConfirmed:
		return true
	default:
		return false
	}
}

func mcpPaymentStatus(total, collected float64) string {
	total = normalizeMoney(total)
	collected = normalizeMoney(collected)
	switch {
	case total <= 0:
		return "not_applicable"
	case collected <= 0:
		return "unpaid"
	case collected+0.004 >= total:
		return "paid"
	default:
		return "partially_paid"
	}
}

func mcpTierForSlot(settings *PricingSettings, slotDate, slotHour string) string {
	if isWeekendDate(slotDate) {
		if isPeakHour(slotHour, settings) {
			return mcpTierWeekendPeak
		}
		return mcpTierWeekendOffPeak
	}
	if isPeakHour(slotHour, settings) {
		return mcpTierWeekdayPeak
	}
	return mcpTierWeekdayOffPeak
}

func mcpTierLabel(tier string) string {
	switch tier {
	case mcpTierWeekdayOffPeak:
		return "Weekday off-peak"
	case mcpTierWeekdayPeak:
		return "Weekday peak"
	case mcpTierWeekendOffPeak:
		return "Weekend off-peak"
	case mcpTierWeekendPeak:
		return "Weekend peak"
	default:
		return tier
	}
}

func validMCPTier(tier string) bool {
	switch strings.TrimSpace(tier) {
	case mcpTierWeekdayOffPeak, mcpTierWeekdayPeak, mcpTierWeekendOffPeak, mcpTierWeekendPeak:
		return true
	default:
		return false
	}
}

func mcpMonthBounds(month string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01", strings.TrimSpace(month), time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("valid plan month is required")
	}
	start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, 0)
	return start, end, nil
}

func (a *App) mcpOptionalDateValue(value string) any {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if a != nil && a.runtimeConfig.DBDriver == databaseDriverPostgres {
		return nil
	}
	return ""
}

func (a *App) mcpPlanMonthValue(month string) (string, error) {
	start, _, err := mcpMonthBounds(month)
	if err != nil {
		return "", err
	}
	if a != nil && a.runtimeConfig.DBDriver == databaseDriverPostgres {
		return start.Format("2006-01-02"), nil
	}
	return start.Format("2006-01"), nil
}

func mcpRuleSessionsInMonth(month string, rule MCPPlanScheduleRule) ([]time.Time, error) {
	start, end, err := mcpMonthBounds(month)
	if err != nil {
		return nil, err
	}
	if rule.Weekday < 0 || rule.Weekday > 6 {
		return nil, errors.New("valid weekday is required")
	}
	startTime, err := time.Parse("15:04", strings.TrimSpace(rule.StartHour))
	if err != nil {
		return nil, errors.New("valid start hour is required")
	}
	endTime, err := time.Parse("15:04", strings.TrimSpace(rule.EndHour))
	if err != nil {
		return nil, errors.New("valid end hour is required")
	}
	if !endTime.After(startTime) {
		return nil, errors.New("end hour must be after start hour")
	}
	if endTime.Sub(startTime)%time.Hour != 0 {
		return nil, errors.New("MCP sessions must use whole one-hour blocks")
	}

	var result []time.Time
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		if int(day.Weekday()) != rule.Weekday {
			continue
		}
		for hour := startTime; hour.Before(endTime); hour = hour.Add(time.Hour) {
			result = append(result, time.Date(day.Year(), day.Month(), day.Day(), hour.Hour(), hour.Minute(), 0, 0, time.Local))
		}
	}
	return result, nil
}

func validateMCPPricingBandInput(bands []MCPPricingBand, candidate MCPPricingBand, excludeID int64) error {
	if !validMCPTier(candidate.Tier) {
		return errors.New("valid MCP pricing tier is required")
	}
	if candidate.MinimumSessions < 1 {
		return errors.New("minimum sessions must be at least 1")
	}
	if candidate.MaximumSessions > 0 && candidate.MaximumSessions < candidate.MinimumSessions {
		return errors.New("maximum sessions must be greater than or equal to minimum sessions")
	}
	if math.IsNaN(candidate.PricePerSession) || math.IsInf(candidate.PricePerSession, 0) || candidate.PricePerSession <= 0 {
		return errors.New("price per session must be positive")
	}
	openEndedSeen := candidate.MaximumSessions == 0
	for _, existing := range bands {
		if existing.ID == excludeID || existing.Tier != candidate.Tier || !existing.Active {
			continue
		}
		if existing.MaximumSessions == 0 {
			if openEndedSeen {
				return errors.New("only one open-ended pricing band is allowed per tier")
			}
			openEndedSeen = true
		}
		if mcpSessionRangeOverlaps(existing.MinimumSessions, existing.MaximumSessions, candidate.MinimumSessions, candidate.MaximumSessions) {
			return errors.New("pricing bands cannot overlap within the same tier")
		}
	}
	return nil
}

func mcpSessionRangeOverlaps(aMin, aMax, bMin, bMax int) bool {
	if aMax == 0 {
		aMax = int(^uint(0) >> 1)
	}
	if bMax == 0 {
		bMax = int(^uint(0) >> 1)
	}
	return aMin <= bMax && bMin <= aMax
}

func applicableMCPPricingBand(bands []MCPPricingBand, tier string, totalSessions int, sessionDate string) *MCPPricingBand {
	for i := range bands {
		band := &bands[i]
		if !band.Active || band.Tier != tier {
			continue
		}
		if totalSessions < band.MinimumSessions {
			continue
		}
		if band.MaximumSessions > 0 && totalSessions > band.MaximumSessions {
			continue
		}
		if strings.TrimSpace(band.EffectiveFrom) != "" && sessionDate < band.EffectiveFrom {
			continue
		}
		if strings.TrimSpace(band.EffectiveTo) != "" && sessionDate > band.EffectiveTo {
			continue
		}
		return band
	}
	return nil
}

func listMCPPricingBandsQuery(queryer sqlQueryer) ([]MCPPricingBand, error) {
	rows, err := queryer.Query(`
		SELECT
			id,
			tier,
			minimum_sessions,
			maximum_sessions,
			price_per_session,
			CASE WHEN active THEN 1 ELSE 0 END,
			COALESCE(CAST(effective_from AS TEXT), ''),
			COALESCE(CAST(effective_to AS TEXT), ''),
			created_at,
			updated_at
		FROM mcp_pricing_bands
		ORDER BY tier ASC, minimum_sessions ASC, maximum_sessions ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bands []MCPPricingBand
	for rows.Next() {
		var band MCPPricingBand
		var active int
		if err := rows.Scan(&band.ID, &band.Tier, &band.MinimumSessions, &band.MaximumSessions, &band.PricePerSession, &active, &band.EffectiveFrom, &band.EffectiveTo, &band.CreatedAt, &band.UpdatedAt); err != nil {
			return nil, err
		}
		band.Active = active == 1
		bands = append(bands, band)
	}
	return bands, rows.Err()
}

func mcpTierConfiguredForDate(bands []MCPPricingBand, tier, sessionDate string) bool {
	for _, band := range bands {
		if !band.Active || band.Tier != tier {
			continue
		}
		if strings.TrimSpace(band.EffectiveFrom) != "" && sessionDate < band.EffectiveFrom {
			continue
		}
		if strings.TrimSpace(band.EffectiveTo) != "" && sessionDate > band.EffectiveTo {
			continue
		}
		return true
	}
	return false
}

func validateMCPSessionTierAllowed(settings *PricingSettings, bands []MCPPricingBand, sessionDate, sessionHour string) (string, error) {
	tier := mcpTierForSlot(settings, sessionDate, sessionHour)
	slotTime, err := time.Parse("15:04", sessionHour)
	if err != nil {
		return tier, errors.New("valid MCP session hour is required")
	}
	defaultStart, _ := time.Parse("15:04", "06:00")
	defaultEnd, _ := time.Parse("15:04", "18:00")

	switch tier {
	case mcpTierWeekdayOffPeak:
		if slotTime.Before(defaultStart) || !slotTime.Before(defaultEnd) {
			return tier, errors.New("default MCP weekday sessions must start between 06:00 and 18:00")
		}
		return tier, nil
	case mcpTierWeekdayPeak:
		if !mcpTierConfiguredForDate(bands, tier, sessionDate) {
			return tier, errors.New("weekday peak MCP is not enabled")
		}
		return tier, nil
	case mcpTierWeekendOffPeak:
		if !mcpTierConfiguredForDate(bands, tier, sessionDate) {
			return tier, errors.New("weekend MCP is not enabled")
		}
		return tier, nil
	case mcpTierWeekendPeak:
		if !mcpTierConfiguredForDate(bands, tier, sessionDate) {
			return tier, errors.New("weekend peak MCP is not enabled")
		}
		return tier, nil
	default:
		return tier, errors.New("invalid MCP pricing tier")
	}
}

func lockMCPReservationWindowTx(tx *sql.Tx) error {
	_, err := tx.Exec(`
		UPDATE pricing_settings
		SET updated_at = updated_at
		WHERE id = (SELECT id FROM pricing_settings ORDER BY id ASC LIMIT 1)
	`)
	return err
}

func (a *App) findMCPMonthlyCustomerByUserID(userID int64) (*MCPMonthlyCustomer, error) {
	var customer MCPMonthlyCustomer
	var active int
	err := a.queryRowDB(`
		SELECT id, user_id, name, email, phone, CASE WHEN active THEN 1 ELSE 0 END, notes, created_at, updated_at
		FROM mcp_customers
		WHERE user_id = $1
	`, userID).Scan(
		&customer.ID,
		&customer.UserID,
		&customer.Name,
		&customer.Email,
		&customer.Phone,
		&active,
		&customer.Notes,
		&customer.CreatedAt,
		&customer.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	customer.Active = active == 1
	return &customer, nil
}

func (a *App) findMCPMonthlyCustomerByID(customerID int64) (*MCPMonthlyCustomer, error) {
	var customer MCPMonthlyCustomer
	var active int
	err := a.queryRowDB(`
		SELECT id, user_id, name, email, phone, CASE WHEN active THEN 1 ELSE 0 END, notes, created_at, updated_at
		FROM mcp_customers
		WHERE id = $1
	`, customerID).Scan(
		&customer.ID,
		&customer.UserID,
		&customer.Name,
		&customer.Email,
		&customer.Phone,
		&active,
		&customer.Notes,
		&customer.CreatedAt,
		&customer.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	customer.Active = active == 1
	return &customer, nil
}

func (a *App) listMCPMonthlyCustomers() ([]MCPMonthlyCustomer, error) {
	rows, err := a.queryDB(`
		SELECT id, user_id, name, email, phone, CASE WHEN active THEN 1 ELSE 0 END, notes, created_at, updated_at
		FROM mcp_customers
		ORDER BY active DESC, LOWER(name), id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var customers []MCPMonthlyCustomer
	for rows.Next() {
		var customer MCPMonthlyCustomer
		var active int
		if err := rows.Scan(&customer.ID, &customer.UserID, &customer.Name, &customer.Email, &customer.Phone, &active, &customer.Notes, &customer.CreatedAt, &customer.UpdatedAt); err != nil {
			return nil, err
		}
		customer.Active = active == 1
		customers = append(customers, customer)
	}
	return customers, rows.Err()
}

func (a *App) createMCPMonthlyCustomer(name, email, phone, password, notes string, active bool) (int64, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	phone = strings.TrimSpace(phone)
	password = strings.TrimSpace(password)
	notes = strings.TrimSpace(notes)
	if name == "" {
		return 0, errors.New("customer name is required")
	}
	if !emailPattern.MatchString(email) {
		return 0, errors.New("valid customer email is required")
	}
	if phone == "" {
		return 0, errors.New("customer phone is required")
	}
	if !passwordPattern.MatchString(password) {
		return 0, errors.New("password must be at least 10 characters")
	}
	user, err := a.createManagedUser(name, email, password, []string{"customer"}, true)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	var customerID int64
	err = a.queryRowDB(`
		INSERT INTO mcp_customers (user_id, name, email, phone, active, notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, user.ID, name, email, phone, active, notes, now, now).Scan(&customerID)
	if err != nil {
		return 0, err
	}
	return customerID, nil
}

func (a *App) listMCPPricingBands() ([]MCPPricingBand, error) {
	return listMCPPricingBandsQuery(a.db)
}

func (a *App) createMCPPricingBand(band MCPPricingBand) (int64, error) {
	bands, err := a.listMCPPricingBands()
	if err != nil {
		return 0, err
	}
	if err := validateMCPPricingBandInput(bands, band, 0); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	var bandID int64
	err = a.queryRowDB(`
		INSERT INTO mcp_pricing_bands (
			tier, minimum_sessions, maximum_sessions, price_per_session, active, effective_from, effective_to, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`, band.Tier, band.MinimumSessions, band.MaximumSessions, normalizeMoney(band.PricePerSession), band.Active, a.mcpOptionalDateValue(band.EffectiveFrom), a.mcpOptionalDateValue(band.EffectiveTo), now, now).Scan(&bandID)
	if err != nil {
		return 0, err
	}
	return bandID, nil
}

func mcpRulesFromRequest(r *http.Request) ([]MCPPlanScheduleRule, error) {
	weekdays := r.Form["weekday"]
	starts := r.Form["start_hour"]
	ends := r.Form["end_hour"]
	count := len(weekdays)
	if len(starts) < count {
		count = len(starts)
	}
	if len(ends) < count {
		count = len(ends)
	}
	var rules []MCPPlanScheduleRule
	for i := 0; i < count; i++ {
		weekday, err := strconv.Atoi(strings.TrimSpace(weekdays[i]))
		if err != nil {
			return nil, errors.New("valid weekdays are required")
		}
		rule := MCPPlanScheduleRule{
			Weekday:   weekday,
			StartHour: strings.TrimSpace(starts[i]),
			EndHour:   strings.TrimSpace(ends[i]),
		}
		if _, err := mcpRuleSessionsInMonth("2026-08", rule); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return nil, errors.New("at least one recurring schedule rule is required")
	}
	return rules, nil
}

func (a *App) buildMCPPlanPreview(month string, activity string, quantity int, rules []MCPPlanScheduleRule, settings *PricingSettings, pricingBands []MCPPricingBand) (*MCPPlanPreview, error) {
	month = strings.TrimSpace(month)
	activity = strings.TrimSpace(activity)
	if _, _, err := mcpMonthBounds(month); err != nil {
		return nil, err
	}
	if activity == "" {
		return nil, errors.New("activity is required")
	}
	if quantity <= 0 {
		return nil, errors.New("quantity must be at least 1")
	}
	var sessionTimes []time.Time
	for _, rule := range rules {
		ruleTimes, err := mcpRuleSessionsInMonth(month, rule)
		if err != nil {
			return nil, err
		}
		sessionTimes = append(sessionTimes, ruleTimes...)
	}
	sort.Slice(sessionTimes, func(i, j int) bool { return sessionTimes[i].Before(sessionTimes[j]) })
	if len(sessionTimes) == 0 {
		return nil, errors.New("the selected rules did not generate any sessions in the chosen month")
	}
	preview := &MCPPlanPreview{
		Month:         month,
		Activity:      activity,
		Quantity:      quantity,
		TotalSessions: len(sessionTimes),
	}
	for _, sessionTime := range sessionTimes {
		date := sessionTime.Format("2006-01-02")
		hour := sessionTime.Format("15:04")
		tier, err := validateMCPSessionTierAllowed(settings, pricingBands, date, hour)
		if err != nil {
			preview.Conflicts = append(preview.Conflicts, MCPPlanConflict{
				Date:   date,
				Hour:   hour,
				Reason: err.Error(),
			})
			continue
		}
		band := applicableMCPPricingBand(pricingBands, tier, len(sessionTimes), date)
		if band == nil {
			preview.Conflicts = append(preview.Conflicts, MCPPlanConflict{
				Date:   date,
				Hour:   hour,
				Reason: mcpTierLabel(tier) + " pricing is not configured for " + date,
			})
			continue
		}
		session := MCPPlanSession{
			SessionDate:     date,
			SessionHour:     hour,
			Activity:        activity,
			Quantity:        quantity,
			PricingTier:     tier,
			PricingBandID:   band.ID,
			PricingBandMin:  band.MinimumSessions,
			PricingBandMax:  band.MaximumSessions,
			PricePerSession: band.PricePerSession,
			Amount:          normalizeMoney(band.PricePerSession),
			Status:          mcpPlanStatusPending,
		}
		preview.GrossAmount = normalizeMoney(preview.GrossAmount + session.Amount)
		preview.Sessions = append(preview.Sessions, session)
	}
	return preview, nil
}

func queryMCPPlanSessionsByPlanID(queryer sqlQueryer, planID int64) ([]MCPPlanSession, error) {
	rows, err := queryer.Query(`
		SELECT id, plan_id, CAST(session_date AS TEXT), SUBSTR(CAST(session_hour AS TEXT), 1, 5), activity, quantity, pricing_tier, COALESCE(pricing_band_id, 0),
		       pricing_band_minimum, pricing_band_maximum, price_per_session, amount, status, conflict_reason, created_at, updated_at
		FROM mcp_plan_sessions
		WHERE plan_id = $1
		ORDER BY session_date, session_hour, id
	`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []MCPPlanSession
	for rows.Next() {
		var session MCPPlanSession
		if err := rows.Scan(&session.ID, &session.PlanID, &session.SessionDate, &session.SessionHour, &session.Activity, &session.Quantity, &session.PricingTier, &session.PricingBandID, &session.PricingBandMin, &session.PricingBandMax, &session.PricePerSession, &session.Amount, &session.Status, &session.ConflictReason, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func queryMCPPlanRulesByPlanID(queryer sqlQueryer, planID int64) ([]MCPPlanScheduleRule, error) {
	rows, err := queryer.Query(`
		SELECT id, plan_id, weekday, SUBSTR(CAST(start_hour AS TEXT), 1, 5), SUBSTR(CAST(end_hour AS TEXT), 1, 5), created_at
		FROM mcp_plan_rules
		WHERE plan_id = $1
		ORDER BY weekday, start_hour, id
	`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []MCPPlanScheduleRule
	for rows.Next() {
		var rule MCPPlanScheduleRule
		if err := rows.Scan(&rule.ID, &rule.PlanID, &rule.Weekday, &rule.StartHour, &rule.EndHour, &rule.CreatedAt); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (a *App) listMCPMonthlyPlans(customerID int64) ([]MCPMonthlyPlan, error) {
	query := `
		SELECT p.id, p.customer_id, c.name, c.email, c.user_id, SUBSTR(CAST(p.plan_month AS TEXT), 1, 7), p.game_id, p.activity, p.quantity, p.title, p.status,
		       p.total_sessions, p.gross_amount, p.total_collected, p.outstanding_amount, p.payment_status, p.notes,
		       COALESCE(p.created_by_user_id, 0), COALESCE(p.requested_by_user_id, 0), p.confirmed_at, COALESCE(p.confirmed_by_user_id, 0),
		       p.created_at, p.updated_at
		FROM mcp_monthly_plans p
		JOIN mcp_customers c ON c.id = p.customer_id
	`
	var args []any
	if customerID > 0 {
		query += ` WHERE p.customer_id = $1`
		args = append(args, customerID)
	}
	query += ` ORDER BY p.plan_month DESC, p.created_at DESC, p.id DESC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []MCPMonthlyPlan
	for rows.Next() {
		var plan MCPMonthlyPlan
		var confirmedAt sql.NullTime
		if err := rows.Scan(&plan.ID, &plan.CustomerID, &plan.CustomerName, &plan.CustomerEmail, &plan.UserID, &plan.PlanMonth, &plan.GameID, &plan.Activity, &plan.Quantity, &plan.Title, &plan.Status, &plan.TotalSessions, &plan.GrossAmount, &plan.TotalCollected, &plan.OutstandingAmount, &plan.PaymentStatus, &plan.Notes, &plan.CreatedByUserID, &plan.RequestedByUserID, &confirmedAt, &plan.ConfirmedByUserID, &plan.CreatedAt, &plan.UpdatedAt); err != nil {
			return nil, err
		}
		if confirmedAt.Valid {
			plan.ConfirmedAt = confirmedAt.Time
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range plans {
		plans[i].Rules, err = queryMCPPlanRulesByPlanID(a.db, plans[i].ID)
		if err != nil {
			return nil, err
		}
		plans[i].Sessions, err = queryMCPPlanSessionsByPlanID(a.db, plans[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func (a *App) findMCPMonthlyPlanByID(planID int64) (*MCPMonthlyPlan, error) {
	plans, err := a.listMCPMonthlyPlans(0)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if plans[i].ID == planID {
			return &plans[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func mcpSessionAsSchedule(session MCPPlanSession, status string) SpaceSchedule {
	return SpaceSchedule{
		SlotDate:  session.SessionDate,
		SlotHour:  session.SessionHour,
		EntryType: "booking",
		Activity:  session.Activity,
		Quantity:  session.Quantity,
		Status:    status,
		Title:     "MCP Session",
	}
}

func queryMCPExistingSchedulesForSlot(tx *sql.Tx, slotDate, slotHour string, excludePlanID int64) ([]SpaceSchedule, error) {
	rows, err := tx.Query(`
		SELECT CAST(s.session_date AS TEXT), SUBSTR(CAST(s.session_hour AS TEXT), 1, 5), s.activity, s.quantity, p.status
		FROM mcp_plan_sessions s
		JOIN mcp_monthly_plans p ON p.id = s.plan_id
		WHERE s.session_date = $1
		  AND s.session_hour = $2
		  AND p.id <> $3
		  AND p.status IN ('pending', 'confirmed')
		  AND s.status IN ('pending', 'confirmed')
	`, slotDate, slotHour, excludePlanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []SpaceSchedule
	for rows.Next() {
		var session MCPPlanSession
		var planStatus string
		if err := rows.Scan(&session.SessionDate, &session.SessionHour, &session.Activity, &session.Quantity, &planStatus); err != nil {
			return nil, err
		}
		schedules = append(schedules, mcpSessionAsSchedule(session, planStatus))
	}
	return schedules, rows.Err()
}

func validateMCPPreviewAvailabilityTx(
	tx *sql.Tx,
	driver DatabaseDriver,
	preview *MCPPlanPreview,
	excludePlanID int64,
) error {
	if preview == nil {
		return errors.New("preview is required")
	}
	preview.Conflicts = nil
	courtActivities, courtLayouts, err := activeBookingConfigurationQuery(tx)
	if err != nil {
		return err
	}
	courtClosures, err := listActiveCourtClosuresQuery(tx)
	if err != nil {
		return err
	}
	grouped := make(map[string][]SpaceSchedule)
	for _, session := range preview.Sessions {
		candidate := mcpSessionAsSchedule(session, mcpPlanStatusPending)
		if err := validateConfiguredBookingOption(candidate, courtActivities, courtLayouts); err != nil {
			preview.Conflicts = append(preview.Conflicts, MCPPlanConflict{Date: session.SessionDate, Hour: session.SessionHour, Reason: err.Error()})
			continue
		}
		if err := validateScheduleAgainstClosures(candidate, courtClosures); err != nil {
			preview.Conflicts = append(preview.Conflicts, MCPPlanConflict{Date: session.SessionDate, Hour: session.SessionHour, Reason: err.Error()})
			continue
		}
		existing, err := querySchedulesForSlot(
			tx,
			driver,
			session.SessionDate,
			session.SessionHour,
			0,
		)
		if err != nil {
			return err
		}
		mcpExisting, err := queryMCPExistingSchedulesForSlot(tx, session.SessionDate, session.SessionHour, excludePlanID)
		if err != nil {
			return err
		}
		slotKey := session.SessionDate + " " + session.SessionHour
		slotExisting := append(existing, mcpExisting...)
		slotExisting = append(slotExisting, grouped[slotKey]...)
		if err := validateSpaceScheduleSlotAgainstLayouts(slotExisting, candidate, courtLayouts); err != nil {
			preview.Conflicts = append(preview.Conflicts, MCPPlanConflict{Date: session.SessionDate, Hour: session.SessionHour, Reason: err.Error()})
			continue
		}
		grouped[slotKey] = append(grouped[slotKey], candidate)
	}
	return nil
}

func (a *App) validateMCPPreviewAvailability(preview *MCPPlanPreview, excludePlanID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return validateMCPPreviewAvailabilityTx(
		tx,
		a.runtimeConfig.DBDriver,
		preview,
		excludePlanID,
	)
}

func findMCPMonthlyPlanByIDQuery(queryer sqlQueryer, planID int64) (*MCPMonthlyPlan, error) {
	row := queryer.QueryRow(`
		SELECT p.id, p.customer_id, c.name, c.email, c.user_id, SUBSTR(CAST(p.plan_month AS TEXT), 1, 7), p.game_id, p.activity, p.quantity, p.title, p.status,
		       p.total_sessions, p.gross_amount, p.total_collected, p.outstanding_amount, p.payment_status, p.notes,
		       COALESCE(p.created_by_user_id, 0), COALESCE(p.requested_by_user_id, 0), p.confirmed_at, COALESCE(p.confirmed_by_user_id, 0),
		       p.created_at, p.updated_at
		FROM mcp_monthly_plans p
		JOIN mcp_customers c ON c.id = p.customer_id
		WHERE p.id = $1
	`, planID)
	var plan MCPMonthlyPlan
	var confirmedAt sql.NullTime
	if err := row.Scan(&plan.ID, &plan.CustomerID, &plan.CustomerName, &plan.CustomerEmail, &plan.UserID, &plan.PlanMonth, &plan.GameID, &plan.Activity, &plan.Quantity, &plan.Title, &plan.Status, &plan.TotalSessions, &plan.GrossAmount, &plan.TotalCollected, &plan.OutstandingAmount, &plan.PaymentStatus, &plan.Notes, &plan.CreatedByUserID, &plan.RequestedByUserID, &confirmedAt, &plan.ConfirmedByUserID, &plan.CreatedAt, &plan.UpdatedAt); err != nil {
		return nil, err
	}
	if confirmedAt.Valid {
		plan.ConfirmedAt = confirmedAt.Time
	}
	rules, err := queryMCPPlanRulesByPlanID(queryer, plan.ID)
	if err != nil {
		return nil, err
	}
	sessions, err := queryMCPPlanSessionsByPlanID(queryer, plan.ID)
	if err != nil {
		return nil, err
	}
	plan.Rules = rules
	plan.Sessions = sessions
	return &plan, nil
}

func mcpAvailabilityConflictError(conflicts []MCPPlanConflict, fallback string) error {
	if len(conflicts) == 0 {
		return errors.New(fallback)
	}
	conflict := conflicts[0]
	return fmt.Errorf("%s at %s %s", conflict.Reason, conflict.Date, conflict.Hour)
}

func (a *App) createMCPMonthlyPlan(customerID int64, month string, activity string, quantity int, title string, notes string, rules []MCPPlanScheduleRule, requestedByUserID int64, confirmed bool) (int64, error) {
	customer, err := a.findMCPMonthlyCustomerByID(customerID)
	if err != nil {
		return 0, errors.New("valid MCP customer is required")
	}
	if !customer.Active {
		return 0, errors.New("MCP customer account is inactive")
	}
	now := time.Now().UTC()
	status := mcpPlanStatusPending
	if confirmed {
		status = mcpPlanStatusConfirmed
	}
	gameID := int64(0)
	games, _ := a.listGames(true)
	for _, game := range games {
		if game.Activity == activity {
			gameID = game.ID
			break
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockMCPReservationWindowTx(tx); err != nil {
		return 0, err
	}
	settings, err := getPricingSettingsQuery(tx)
	if err != nil {
		return 0, err
	}
	bands, err := listMCPPricingBandsQuery(tx)
	if err != nil {
		return 0, err
	}
	preview, err := a.buildMCPPlanPreview(month, activity, quantity, rules, settings, bands)
	if err != nil {
		return 0, err
	}
	if err := validateMCPPreviewAvailabilityTx(
		tx,
		a.runtimeConfig.DBDriver,
		preview,
		0,
	); err != nil {
		return 0, err
	}
	if len(preview.Conflicts) > 0 {
		return 0, mcpAvailabilityConflictError(preview.Conflicts, "one or more MCP sessions are unavailable under the current court configuration")
	}
	planMonthValue, err := a.mcpPlanMonthValue(month)
	if err != nil {
		return 0, err
	}
	var planID int64
	err = a.queryRowTxDB(
		tx,
		`
		INSERT INTO mcp_monthly_plans (
			customer_id, plan_month, game_id, activity, quantity, title, status, total_sessions, gross_amount,
			total_collected, outstanding_amount, payment_status, notes, created_by_user_id, requested_by_user_id,
			confirmed_at, confirmed_by_user_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id
	`, customerID, planMonthValue, gameID, activity, quantity, strings.TrimSpace(title), status, preview.TotalSessions, preview.GrossAmount, preview.GrossAmount, mcpPaymentStatus(preview.GrossAmount, 0), strings.TrimSpace(notes), nullIfZero(requestedByUserID), nullIfZero(requestedByUserID), func() any {
			if confirmed {
				return now
			}
			return nil
		}(), nullIfZero(requestedByUserID), now, now).Scan(&planID)
	if err != nil {
		return 0, err
	}
	for _, rule := range rules {
		if _, err := a.execTxDB(tx, `INSERT INTO mcp_plan_rules (plan_id, weekday, start_hour, end_hour, created_at) VALUES ($1, $2, $3, $4, $5)`, planID, rule.Weekday, rule.StartHour, rule.EndHour, now); err != nil {
			return 0, err
		}
	}
	for _, session := range preview.Sessions {
		if _, err := a.execTxDB(tx, `
			INSERT INTO mcp_plan_sessions (
				plan_id, session_date, session_hour, activity, quantity, pricing_tier, pricing_band_id,
				pricing_band_minimum, pricing_band_maximum, price_per_session, amount, status, conflict_reason, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, '', $13, $14)
		`, planID, session.SessionDate, session.SessionHour, session.Activity, session.Quantity, session.PricingTier, nullIfZero(session.PricingBandID), session.PricingBandMin, session.PricingBandMax, session.PricePerSession, session.Amount, status, now, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return planID, nil
}

func (a *App) confirmMCPMonthlyPlan(planID int64, confirmedByUserID int64) error {
	now := time.Now().UTC()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockMCPReservationWindowTx(tx); err != nil {
		return err
	}
	plan, err := findMCPMonthlyPlanByIDQuery(tx, planID)
	if err != nil {
		return err
	}
	if plan.Status != mcpPlanStatusPending {
		return errors.New("only pending monthly court plans can be confirmed")
	}
	preview := &MCPPlanPreview{Sessions: plan.Sessions}
	if err := validateMCPPreviewAvailabilityTx(
		tx,
		a.runtimeConfig.DBDriver,
		preview,
		plan.ID,
	); err != nil {
		return err
	}
	if len(preview.Conflicts) > 0 {
		return mcpAvailabilityConflictError(preview.Conflicts, "availability changed and the plan can no longer be confirmed")
	}
	if _, err := a.execTxDB(tx, `UPDATE mcp_monthly_plans SET status = 'confirmed', confirmed_at = $1, confirmed_by_user_id = $2, updated_at = $3 WHERE id = $4`, now, nullIfZero(confirmedByUserID), now, planID); err != nil {
		return err
	}
	if _, err := a.execTxDB(tx, `UPDATE mcp_plan_sessions SET status = 'confirmed', updated_at = $1 WHERE plan_id = $2`, now, planID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) collectMCPPayment(planID int64, paymentMethod string, amount float64, paymentNote string, recordedByUserID int64) (int64, error) {
	return a.collectMCPPaymentAt(planID, paymentMethod, amount, paymentNote, time.Now().UTC(), recordedByUserID)
}

func (a *App) collectMCPPaymentAt(planID int64, paymentMethod string, amount float64, paymentNote string, collectedAt time.Time, recordedByUserID int64) (int64, error) {
	paymentMethod = normalizePaymentMethod(paymentMethod)
	if !validPaymentMethod(paymentMethod) {
		return 0, errors.New("MCP payment method is invalid")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return 0, errors.New("MCP payment amount must be greater than zero")
	}
	plan, err := a.findMCPMonthlyPlanByID(planID)
	if err != nil {
		return 0, errors.New("MCP receivable was not found")
	}
	if plan.GrossAmount <= 0 || plan.OutstandingAmount <= 0.004 {
		return 0, errors.New("MCP plan has no collectible balance")
	}
	if amount > normalizeMoney(plan.OutstandingAmount)+0.004 {
		return 0, errors.New("MCP payment amount cannot exceed the outstanding balance")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	divisionID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	collectedAt = collectedAt.UTC()
	now := time.Now().UTC()
	receiptNumber, err := a.nextReceiptNumberTx(tx, "mcp_payment", collectedAt)
	if err != nil {
		return 0, err
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    receiptNumber,
		ReferenceNumber:  receiptNumber,
		Category:         "mcp_payment",
		TransactionType:  financeTxnTypeIncome,
		ReferenceType:    "mcp_monthly_plan",
		ReferenceID:      planID,
		FinanceAccountID: account.ID,
		PersonName:       plan.CustomerName,
		Description:      fmt.Sprintf("MCP payment for %s (%s)", plan.Title, plan.PlanMonth),
		Notes:            strings.TrimSpace(paymentNote),
		PaymentMethod:    paymentMethod,
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       collectedAt,
	})
	if err != nil {
		return 0, err
	}
	if _, err := a.execTxDB(tx, `
		INSERT INTO mcp_payment_collections (
			plan_id, finance_transaction_id, amount, payment_method, payment_note, collected_by_user_id, collected_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, planID, transactionID, amount, paymentMethod, strings.TrimSpace(paymentNote), nullIfZero(recordedByUserID), collectedAt, now); err != nil {
		return 0, err
	}
	totalCollected := normalizeMoney(plan.TotalCollected + amount)
	outstanding := normalizeMoney(plan.GrossAmount - totalCollected)
	if outstanding < 0 {
		outstanding = 0
	}
	paymentStatus := mcpPaymentStatus(plan.GrossAmount, totalCollected)
	if _, err := a.execTxDB(tx, `UPDATE mcp_monthly_plans SET total_collected = $1, outstanding_amount = $2, payment_status = $3, updated_at = $4 WHERE id = $5`, totalCollected, outstanding, paymentStatus, now, planID); err != nil {
		return 0, err
	}
	if _, err := a.execTxDB(tx, `
		UPDATE finance_transactions
		SET source_type = 'mcp_payment_collection',
		    source_id = (SELECT id FROM mcp_payment_collections WHERE finance_transaction_id = $1 LIMIT 1),
		    updated_at = $2
		WHERE id = $3
	`, transactionID, now, transactionID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) voidMCPPayment(collectionID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var planID, financeTransactionID int64
	var voided bool
	if err := a.queryRowTxDB(tx, `
		SELECT plan_id, finance_transaction_id, voided
		FROM mcp_payment_collections
		WHERE id = ?
	`, collectionID).Scan(&planID, &financeTransactionID, &voided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("MCP payment was not found")
		}
		return err
	}
	if voided {
		return errors.New("MCP payment has already been voided")
	}
	now := time.Now().UTC()
	if _, err := a.execTxDB(tx, `
		UPDATE mcp_payment_collections
		SET voided = TRUE, void_reason = ?, voided_by_user_id = ?, voided_at = ?
		WHERE id = ? AND voided = FALSE
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, collectionID); err != nil {
		return err
	}
	if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, voidedByUserID); err != nil {
		return err
	}
	var grossAmount float64
	if err := a.queryRowTxDB(tx, `SELECT gross_amount FROM mcp_monthly_plans WHERE id = ?`, planID).Scan(&grossAmount); err != nil {
		return err
	}
	var totalCollected float64
	if err := a.queryRowTxDB(tx, `SELECT COALESCE(SUM(amount), 0) FROM mcp_payment_collections WHERE plan_id = ? AND voided = FALSE`, planID).Scan(&totalCollected); err != nil {
		return err
	}
	totalCollected = normalizeMoney(totalCollected)
	outstanding := normalizeMoney(grossAmount - totalCollected)
	if outstanding < 0 {
		outstanding = 0
	}
	if _, err := a.execTxDB(tx, `
		UPDATE mcp_monthly_plans
		SET total_collected = ?, outstanding_amount = ?, payment_status = ?, updated_at = ?
		WHERE id = ?
	`, totalCollected, outstanding, mcpPaymentStatus(grossAmount, totalCollected), now, planID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) listMCPPaymentCollectionsForPlanIDs(planIDs []int64) ([]MCPPaymentCollection, error) {
	if len(planIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(planIDs))
	args := make([]any, 0, len(planIDs))
	for _, planID := range planIDs {
		placeholders = append(placeholders, "?")
		args = append(args, planID)
	}
	query := `SELECT id, plan_id, finance_transaction_id, amount, payment_method, payment_note, collected_at, voided, void_reason
		FROM mcp_payment_collections WHERE plan_id IN (` + strings.Join(placeholders, ", ") + `) ORDER BY collected_at DESC, id DESC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	collections := make([]MCPPaymentCollection, 0)
	for rows.Next() {
		var collection MCPPaymentCollection
		if err := rows.Scan(&collection.ID, &collection.PlanID, &collection.FinanceTransactionID, &collection.Amount, &collection.PaymentMethod, &collection.PaymentNote, &collection.CollectedAt, &collection.Voided, &collection.VoidReason); err != nil {
			return nil, err
		}
		collections = append(collections, collection)
	}
	return collections, rows.Err()
}

func (a *App) continueMCPMonthlyPlan(planID int64, nextMonth string, requestedByUserID int64) (int64, error) {
	plan, err := a.findMCPMonthlyPlanByID(planID)
	if err != nil {
		return 0, err
	}
	var rules []MCPPlanScheduleRule
	for _, rule := range plan.Rules {
		rules = append(rules, MCPPlanScheduleRule{
			Weekday:   rule.Weekday,
			StartHour: rule.StartHour,
			EndHour:   rule.EndHour,
		})
	}
	return a.createMCPMonthlyPlan(plan.CustomerID, nextMonth, plan.Activity, plan.Quantity, plan.Title, plan.Notes, rules, requestedByUserID, false)
}

func (a *App) customerMCPLoginHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) requireMCPPortalCustomer(r *http.Request) (*User, *MCPMonthlyCustomer, error) {
	user := a.requireAuthenticatedUser(r)
	if user == nil {
		return nil, nil, errors.New("authentication is required")
	}
	customer, err := a.findMCPMonthlyCustomerByUserID(user.ID)
	if err != nil {
		return user, nil, errors.New("this account is not an MCP customer")
	}
	if !customer.Active {
		return user, nil, errors.New("this MCP customer account is inactive")
	}
	return user, customer, nil
}

func (a *App) customerMCPRouter(w http.ResponseWriter, r *http.Request) {
	user, customer, err := a.requireMCPPortalCustomer(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/customer/mcp")
	switch {
	case path == "" || path == "/":
		a.customerMCPListHandler(w, r, user, customer)
	case path == "/new":
		a.customerMCPNewHandler(w, r, user, customer)
	default:
		a.customerMCPDetailRouter(w, r, user, customer, path)
	}
}

func (a *App) customerMCPListHandler(w http.ResponseWriter, r *http.Request, user *User, customer *MCPMonthlyCustomer) {
	plans, err := a.listMCPMonthlyPlans(customer.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "Monthly Court Plans"
	data.Description = "Monthly court plans for the authenticated MCP customer."
	data.MCPPlans = plans
	data.SelectedMCPCustomer = customer
	data.MCPPortal = true
	data.HideChrome = false
	a.render(w, "customer-mcp", data, http.StatusOK)
}

func (a *App) customerMCPNewHandler(w http.ResponseWriter, r *http.Request, user *User, customer *MCPMonthlyCustomer) {
	switch r.Method {
	case http.MethodGet:
		activities, layouts, err := a.activeBookingConfiguration()
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		data := a.newTemplateData(w, r, user)
		data.Title = "New Monthly Court Plan"
		data.Description = "Create a monthly court plan."
		data.BookingOptions = bookingOptionCatalog(activities, layouts)
		data.SelectedMCPCustomer = customer
		data.MCPSelectedMonth = time.Now().AddDate(0, 1, 0).Format("2006-01")
		data.MCPPortal = true
		a.render(w, "customer-mcp-new", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form submission", http.StatusBadRequest)
			return
		}
		quantity, err := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
		if err != nil {
			http.Error(w, "valid quantity is required", http.StatusBadRequest)
			return
		}
		rules, err := mcpRulesFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		planID, err := a.createMCPMonthlyPlan(customer.ID, strings.TrimSpace(r.FormValue("plan_month")), strings.TrimSpace(r.FormValue("activity")), quantity, strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("notes")), rules, user.ID, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/customer/mcp/%d", planID), http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) customerMCPDetailRouter(w http.ResponseWriter, r *http.Request, user *User, customer *MCPMonthlyCustomer, path string) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	planID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || planID <= 0 {
		http.NotFound(w, r)
		return
	}
	plan, err := a.findMCPMonthlyPlanByID(planID)
	if err != nil || plan.CustomerID != customer.ID {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "continue" {
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
		nextID, err := a.continueMCPMonthlyPlan(planID, strings.TrimSpace(r.FormValue("plan_month")), user.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/customer/mcp/%d", nextID), http.StatusSeeOther)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = plan.Title
	data.Description = "Monthly court plan details."
	data.SelectedMCPPlan = plan
	data.SelectedMCPCustomer = customer
	data.MCPPortal = true
	a.render(w, "customer-mcp-detail", data, http.StatusOK)
}

func (a *App) adminMCPManagementHandler(w http.ResponseWriter, r *http.Request) {
	user := a.requireAuthenticatedUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form submission", http.StatusBadRequest)
			return
		}
		switch strings.TrimSpace(r.FormValue("intent")) {
		case "create_customer":
			_, err := a.createMCPMonthlyCustomer(r.FormValue("name"), r.FormValue("email"), r.FormValue("phone"), r.FormValue("password"), r.FormValue("notes"), r.FormValue("active") != "0")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		case "confirm_plan":
			planID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("plan_id")), 10, 64)
			if err := a.confirmMCPMonthlyPlan(planID, user.ID); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		http.Redirect(w, r, "/admin/mcp", http.StatusSeeOther)
		return
	}
	customers, err := a.listMCPMonthlyCustomers()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	plans, err := a.listMCPMonthlyPlans(0)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "Monthly Court Plans"
	data.Description = "Monthly court plan operations."
	data.MCPCustomers = customers
	data.MCPPlans = plans
	data.MCPPage = "management"
	a.render(w, "mcp-management", data, http.StatusOK)
}

func (a *App) adminMCPPricingHandler(w http.ResponseWriter, r *http.Request) {
	user := a.requireAuthenticatedUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form submission", http.StatusBadRequest)
			return
		}
		minimumSessions, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("minimum_sessions")))
		maximumSessions, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("maximum_sessions")))
		pricePerSession, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price_per_session")), 64)
		_, err := a.createMCPPricingBand(MCPPricingBand{
			Tier:            strings.TrimSpace(r.FormValue("tier")),
			MinimumSessions: minimumSessions,
			MaximumSessions: maximumSessions,
			PricePerSession: pricePerSession,
			Active:          r.FormValue("active") != "0",
			EffectiveFrom:   strings.TrimSpace(r.FormValue("effective_from")),
			EffectiveTo:     strings.TrimSpace(r.FormValue("effective_to")),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/mcp/pricing", http.StatusSeeOther)
		return
	}
	bands, err := a.listMCPPricingBands()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "MCP Pricing"
	data.Description = "Monthly court plan pricing bands."
	data.MCPPricingBands = bands
	data.MCPPage = "pricing"
	a.render(w, "mcp-pricing", data, http.StatusOK)
}

func (a *App) adminMCPReceivablesHandler(w http.ResponseWriter, r *http.Request) {
	user := a.requireAuthenticatedUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		if err := a.verifyCSRF(r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form submission", http.StatusBadRequest)
			return
		}
		planID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("plan_id")), 10, 64)
		amount, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		collectedAt, collectedAtErr := parseFinanceRecordedAtDate(
			r.FormValue("payment_collected_at"),
			time.Now(),
			"Payment collection date",
		)
		if collectedAtErr != nil {
			http.Error(w, collectedAtErr.Error(), http.StatusBadRequest)
			return
		}
		if _, err := a.collectMCPPaymentAt(planID, r.FormValue("payment_method"), amount, r.FormValue("payment_note"), collectedAt, user.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/mcp-receivables", http.StatusSeeOther)
		return
	}
	plans, err := a.listMCPMonthlyPlans(0)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	planIDs := make([]int64, 0, len(plans))
	var receivables []MCPReceivable
	for _, plan := range plans {
		if plan.GrossAmount > 0 && plan.Status != mcpPlanStatusCancelled {
			receivables = append(receivables, MCPReceivable{Plan: plan})
			planIDs = append(planIDs, plan.ID)
		}
	}
	collections, err := a.listMCPPaymentCollectionsForPlanIDs(planIDs)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	byPlanID := make(map[int64][]MCPPaymentCollection, len(receivables))
	for _, collection := range collections {
		byPlanID[collection.PlanID] = append(byPlanID[collection.PlanID], collection)
	}
	for i := range receivables {
		receivables[i].PaymentCollections = byPlanID[receivables[i].Plan.ID]
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "MCP Receivables"
	data.Description = "Monthly court plan receivables and collections."
	data.MCPReceivables = receivables
	data.MCPPage = "receivables"
	a.render(w, "mcp-receivables", data, http.StatusOK)
}

func (a *App) voidMCPPaymentHandler(w http.ResponseWriter, r *http.Request) {
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
	collectionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("collection_id")), 10, 64)
	if err != nil || collectionID <= 0 {
		http.Error(w, "invalid payment entry", http.StatusBadRequest)
		return
	}
	user := a.requireAuthenticatedUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := a.voidMCPPayment(collectionID, r.FormValue("void_reason"), user.ID); err != nil {
		a.setFlash(w, "MCP payment could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/mcp-receivables", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "MCP payment was voided and the plan balance was recalculated.")
	http.Redirect(w, r, "/admin/mcp-receivables", http.StatusSeeOther)
}

func (a *App) customerRedirectAfterLogin(user *User) string {
	if user == nil {
		return "/dashboard"
	}
	if _, err := a.findMCPMonthlyCustomerByUserID(user.ID); err == nil {
		return "/customer/mcp"
	}
	return "/dashboard"
}

func mcpPlanContinueMonthDefault(plan *MCPMonthlyPlan) string {
	if plan == nil {
		return ""
	}
	start, _, err := mcpMonthBounds(plan.PlanMonth)
	if err != nil {
		return ""
	}
	return start.AddDate(0, 1, 0).Format("2006-01")
}

func mcpContinueAction(plan *MCPMonthlyPlan) string {
	if plan == nil {
		return ""
	}
	return "/customer/mcp/" + strconv.FormatInt(plan.ID, 10) + "/continue"
}

func mcpPaymentCollectionAction() string {
	return "/admin/mcp-receivables"
}

func mcpPaymentVoidAction() string {
	return "/admin/mcp-receivables/payments/void"
}

func mcpCustomerMailto(email string) string {
	if strings.TrimSpace(email) == "" {
		return ""
	}
	return "mailto:" + url.QueryEscape(strings.TrimSpace(email))
}

func (a *App) requireAuthenticatedUser(r *http.Request) *User {
	user, _ := a.currentUser(r.Context())
	return user
}
