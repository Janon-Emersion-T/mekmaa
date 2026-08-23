package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"database/sql"
)

func seedMCPPricingBands(t *testing.T, app *App, bands ...MCPPricingBand) {
	t.Helper()
	for _, band := range bands {
		if _, err := app.createMCPPricingBand(band); err != nil {
			t.Fatalf("create MCP pricing band: %v", err)
		}
	}
}

func createMCPTestCustomer(t *testing.T, app *App, email string) int64 {
	t.Helper()
	customerID, err := app.createMCPMonthlyCustomer("MCP Customer", email, "0700000000", "password-123", "", true)
	if err != nil {
		t.Fatalf("create MCP customer: %v", err)
	}
	return customerID
}

func newConcurrentMCPTestApp(t *testing.T) *App {
	t.Helper()
	dbPath := t.TempDir() + "/mcp-concurrency.sqlite"
	db, err := sql.Open("sqlite", sqliteRuntimeDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { db.Close() })
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if err := seedFinanceCategories(db, databaseDriverSQLite); err != nil {
		t.Fatalf("seed finance categories: %v", err)
	}
	seedBookingEngine(t, db)
	if err := seedRoles(db); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	return &App{
		db: db,
		bookingMessages: BookingCommunicationSettings{
			VenueName:    "Mekmaa",
			VenueAddress: "No. 64, Temple Road, Jaffna 40000",
			ContactPhone: "+94772207297",
			ContactEmail: "bookings@mekmaa.example",
		},
		bookingAccess: BookingAccessSettings{
			BaseURL:     "http://localhost:8080",
			TokenSecret: "test-secret",
			TokenTTL:    180 * 24 * time.Hour,
		},
	}
}

func TestMCPPricingPreviewTotals(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	settings := &PricingSettings{PeakStartHour: "17:00", PeakEndHour: "21:00"}

	t.Run("A four sessions use one to five range", func(t *testing.T) {
		bands := []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 2500, Active: true}}
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}}, settings, bands)
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if preview.TotalSessions != 4 || preview.GrossAmount != 10000 {
			t.Fatalf("preview = %+v, want 4 sessions and 10000", preview)
		}
	})

	t.Run("B nine sessions use six to ten range", func(t *testing.T) {
		bands := []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 6, MaximumSessions: 10, PricePerSession: 2000, Active: true}}
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}, {Weekday: 2, StartHour: "08:00", EndHour: "09:00"}}, settings, bands)
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if preview.TotalSessions != 9 || preview.GrossAmount != 18000 {
			t.Fatalf("preview = %+v, want 9 sessions and 18000", preview)
		}
	})

	t.Run("C twelve sessions use eleven plus range", func(t *testing.T) {
		bands := []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 11, MaximumSessions: 0, PricePerSession: 1800, Active: true}}
		preview, err := app.buildMCPPlanPreview("2026-11", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 2, StartHour: "08:00", EndHour: "09:00"}, {Weekday: 3, StartHour: "08:00", EndHour: "09:00"}, {Weekday: 4, StartHour: "08:00", EndHour: "09:00"}}, settings, bands)
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if preview.TotalSessions != 12 {
			t.Fatalf("total sessions = %d, want 12", preview.TotalSessions)
		}
		if preview.GrossAmount != 21600 {
			t.Fatalf("gross amount = %.2f, want 21600", preview.GrossAmount)
		}
	})

	t.Run("D mixed tier uses per tier price for same total session range", func(t *testing.T) {
		bands := []MCPPricingBand{
			{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 6, MaximumSessions: 10, PricePerSession: 2000, Active: true},
			{Tier: mcpTierWeekdayPeak, MinimumSessions: 6, MaximumSessions: 10, PricePerSession: 2500, Active: true},
		}
		totalSessions := 9
		total := 0.0
		for i := 0; i < 7; i++ {
			band := applicableMCPPricingBand(bands, mcpTierWeekdayOffPeak, totalSessions, "2026-09-07")
			if band == nil {
				t.Fatal("weekday off-peak band not found")
			}
			total += band.PricePerSession
		}
		for i := 0; i < 2; i++ {
			band := applicableMCPPricingBand(bands, mcpTierWeekdayPeak, totalSessions, "2026-09-07")
			if band == nil {
				t.Fatal("weekday peak band not found")
			}
			total += band.PricePerSession
		}
		if normalizeMoney(total) != 19000 {
			t.Fatalf("mixed tier total = %.2f, want 19000", total)
		}
	})
}

func TestMCPRecurrenceAndTierValidation(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	settings := &PricingSettings{PeakStartHour: "17:00", PeakEndHour: "21:00"}

	t.Run("E two hour booking becomes exactly two one hour sessions", func(t *testing.T) {
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "10:00"}}, settings, []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 6, MaximumSessions: 10, PricePerSession: 2500, Active: true}})
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if len(preview.Sessions) != 8 {
			t.Fatalf("Monday 08:00-10:00 across September 2026 should yield 8 sessions, got %d", len(preview.Sessions))
		}
		firstDayCount := 0
		for _, session := range preview.Sessions {
			if session.SessionDate == "2026-09-07" {
				firstDayCount++
			}
		}
		if firstDayCount != 2 {
			t.Fatalf("2026-09-07 should contain exactly 2 sessions, got %d", firstDayCount)
		}
	})

	t.Run("F weekly recurrence generates the correct dates inside the selected month", func(t *testing.T) {
		dates, err := mcpRuleSessionsInMonth("2026-09", MCPPlanScheduleRule{Weekday: 1, StartHour: "08:00", EndHour: "09:00"})
		if err != nil {
			t.Fatalf("sessions in month: %v", err)
		}
		want := []string{"2026-09-07", "2026-09-14", "2026-09-21", "2026-09-28"}
		if len(dates) != len(want) {
			t.Fatalf("dates len = %d, want %d", len(dates), len(want))
		}
		for i, day := range want {
			if got := dates[i].Format("2006-01-02"); got != day {
				t.Fatalf("date[%d] = %s, want %s", i, got, day)
			}
		}
	})

	t.Run("G weekend booking rejected when weekend tier disabled", func(t *testing.T) {
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 6, StartHour: "08:00", EndHour: "09:00"}}, settings, []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 2500, Active: true}})
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if len(preview.Conflicts) == 0 {
			t.Fatal("expected weekend tier conflict")
		}
	})

	t.Run("H weekend booking accepted and priced when weekend tier enabled", func(t *testing.T) {
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 6, StartHour: "08:00", EndHour: "09:00"}}, settings, []MCPPricingBand{{Tier: mcpTierWeekendOffPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 2800, Active: true}})
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if len(preview.Conflicts) != 0 || preview.GrossAmount != 11200 {
			t.Fatalf("preview = %+v, want no conflicts and 11200", preview)
		}
	})

	t.Run("I peak booking rejected when peak tier disabled", func(t *testing.T) {
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 1, StartHour: "17:00", EndHour: "18:00"}}, settings, []MCPPricingBand{{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 2500, Active: true}})
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if len(preview.Conflicts) == 0 {
			t.Fatal("expected peak tier conflict")
		}
	})

	t.Run("J weekday peak booking accepted when peak tier enabled", func(t *testing.T) {
		preview, err := app.buildMCPPlanPreview("2026-09", "badminton", 1, []MCPPlanScheduleRule{{Weekday: 1, StartHour: "19:00", EndHour: "20:00"}}, settings, []MCPPricingBand{{Tier: mcpTierWeekdayPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 3000, Active: true}})
		if err != nil {
			t.Fatalf("build preview: %v", err)
		}
		if len(preview.Conflicts) != 0 || preview.GrossAmount != 12000 {
			t.Fatalf("preview = %+v, want no conflicts and 12000", preview)
		}
	})
}

func TestMCPAvailabilityConflictsAndSnapshots(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	customerID := createMCPTestCustomer(t, app, "mcp-test@example.com")
	seedMCPPricingBands(t, app,
		MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true},
		MCPPricingBand{Tier: mcpTierWeekdayPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 3000, Active: true},
	)

	t.Run("J existing normal booking causes MCP conflict", func(t *testing.T) {
		createConfirmedBookingForTests(t, app, SpaceSchedule{
			SlotDate: "2026-09-07", SlotHour: "08:00", EntryType: "booking", Activity: "badminton", Quantity: 1, Title: "Existing", Status: bookingStatusConfirmed,
		})
		_, err := app.createMCPMonthlyPlan(customerID, "2026-09", "badminton", 1, "Conflict Plan", "", []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}}, 0, false)
		if err == nil {
			t.Fatal("expected conflict with existing booking")
		}
	})

	t.Run("K training schedule causes MCP conflict", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		customerID2 := createMCPTestCustomer(t, app2, "mcp-training@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
		createConfirmedBookingForTests(t, app2, SpaceSchedule{
			SlotDate: "2026-09-08", SlotHour: "08:00", EntryType: "training", Activity: "training", Quantity: 1, Title: "Training Block", Status: bookingStatusConfirmed,
		})
		_, err := app2.createMCPMonthlyPlan(customerID2, "2026-09", "badminton", 1, "Training Conflict", "", []MCPPlanScheduleRule{{Weekday: 2, StartHour: "08:00", EndHour: "09:00"}}, 0, false)
		if err == nil {
			t.Fatal("expected training conflict")
		}
	})

	t.Run("L court closure causes MCP conflict", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		customerID2 := createMCPTestCustomer(t, app2, "mcp-closure@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
		now := time.Now().UTC()
		if _, err := app2.db.Exec(`INSERT INTO court_closures (court_id, closure_date, start_hour, end_hour, activity, title, reason, active, created_at, updated_at) VALUES (1, '2026-09-09', '08:00', '09:00', '', 'Closed', 'Maintenance', 1, ?, ?)`, now, now); err != nil {
			t.Fatalf("insert closure: %v", err)
		}
		_, err := app2.createMCPMonthlyPlan(customerID2, "2026-09", "badminton", 1, "Closure Conflict", "", []MCPPlanScheduleRule{{Weekday: 3, StartHour: "08:00", EndHour: "09:00"}}, 0, false)
		if err == nil {
			t.Fatal("expected closure conflict")
		}
	})

	t.Run("M badminton capacity of one rejects second overlapping MCP allocation", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		customer1 := createMCPTestCustomer(t, app2, "mcp-capacity-1@example.com")
		customer2 := createMCPTestCustomer(t, app2, "mcp-capacity-2@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
		if _, err := app2.createMCPMonthlyPlan(customer1, "2026-09", "badminton", 1, "First", "", []MCPPlanScheduleRule{{Weekday: 4, StartHour: "08:00", EndHour: "09:00"}}, 0, true); err != nil {
			t.Fatalf("create first plan: %v", err)
		}
		if _, err := app2.createMCPMonthlyPlan(customer2, "2026-09", "badminton", 1, "Second", "", []MCPPlanScheduleRule{{Weekday: 4, StartHour: "08:00", EndHour: "09:00"}}, 0, false); err == nil {
			t.Fatal("expected MCP capacity conflict")
		}
	})

	t.Run("N concurrent confirmed MCP creates only reserve one capacity slot", func(t *testing.T) {
		app2 := newConcurrentMCPTestApp(t)
		customer1 := createMCPTestCustomer(t, app2, "mcp-race-1@example.com")
		customer2 := createMCPTestCustomer(t, app2, "mcp-race-2@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})

		start := make(chan struct{})
		var wg sync.WaitGroup
		type result struct {
			planID int64
			err    error
		}
		results := make(chan result, 2)
		create := func(customerID int64, title string) {
			defer wg.Done()
			<-start
			planID, err := app2.createMCPMonthlyPlan(customerID, "2026-09", "badminton", 1, title, "", []MCPPlanScheduleRule{{Weekday: 4, StartHour: "08:00", EndHour: "09:00"}}, 0, true)
			results <- result{planID: planID, err: err}
		}
		wg.Add(2)
		go create(customer1, "Race One")
		go create(customer2, "Race Two")
		close(start)
		wg.Wait()
		close(results)

		successCount := 0
		conflictCount := 0
		for result := range results {
			if result.err == nil {
				successCount++
				continue
			}
			if strings.Contains(strings.ToLower(result.err.Error()), "08:00") || strings.Contains(strings.ToLower(result.err.Error()), "unavailable") || strings.Contains(strings.ToLower(result.err.Error()), "already") {
				conflictCount++
				continue
			}
			t.Fatalf("unexpected concurrency error: %v", result.err)
		}
		if successCount != 1 || conflictCount != 1 {
			t.Fatalf("want one success and one conflict, got success=%d conflict=%d", successCount, conflictCount)
		}

		var reservedCount int
		if err := app2.db.QueryRow(`
			SELECT COUNT(*)
			FROM mcp_plan_sessions
			WHERE session_date = '2026-09-03'
			  AND session_hour = '08:00'
			  AND status IN ('pending', 'confirmed')
		`).Scan(&reservedCount); err != nil {
			t.Fatalf("count reserved slot usage: %v", err)
		}
		if reservedCount != 1 {
			t.Fatalf("reserved slot usage = %d, want 1", reservedCount)
		}
	})

	t.Run("O pricing band overlap is rejected", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 5, PricePerSession: 2500, Active: true})
		if _, err := app2.createMCPPricingBand(MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 5, MaximumSessions: 10, PricePerSession: 2000, Active: true}); err == nil {
			t.Fatal("expected overlap validation error")
		}
	})

	t.Run("P historical confirmed plan price snapshot does not change after pricing edit", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		customer := createMCPTestCustomer(t, app2, "mcp-snapshot@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
		planID, err := app2.createMCPMonthlyPlan(customer, "2026-09", "badminton", 1, "Snapshot", "", []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}}, 0, true)
		if err != nil {
			t.Fatalf("create plan: %v", err)
		}
		plan, err := app2.findMCPMonthlyPlanByID(planID)
		if err != nil {
			t.Fatalf("find plan: %v", err)
		}
		original := plan.GrossAmount
		if _, err := app2.db.Exec(`UPDATE mcp_pricing_bands SET price_per_session = 9999`); err != nil {
			t.Fatalf("update pricing: %v", err)
		}
		plan, err = app2.findMCPMonthlyPlanByID(planID)
		if err != nil {
			t.Fatalf("find plan after pricing update: %v", err)
		}
		if plan.GrossAmount != original {
			t.Fatalf("gross amount changed from %.2f to %.2f", original, plan.GrossAmount)
		}
	})

	t.Run("Q continue to next month rechecks availability and current pricing", func(t *testing.T) {
		app2 := newBookingWorkflowTestApp(t)
		customer := createMCPTestCustomer(t, app2, "mcp-continue@example.com")
		seedMCPPricingBands(t, app2, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
		planID, err := app2.createMCPMonthlyPlan(customer, "2026-09", "badminton", 1, "Continue", "", []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}}, 0, true)
		if err != nil {
			t.Fatalf("create plan: %v", err)
		}
		if _, err := app2.db.Exec(`UPDATE mcp_pricing_bands SET price_per_session = 3000 WHERE tier = ?`, mcpTierWeekdayOffPeak); err != nil {
			t.Fatalf("update price: %v", err)
		}
		nextPlanID, err := app2.continueMCPMonthlyPlan(planID, "2026-10", 0)
		if err != nil {
			t.Fatalf("continue plan: %v", err)
		}
		nextPlan, err := app2.findMCPMonthlyPlanByID(nextPlanID)
		if err != nil {
			t.Fatalf("find next plan: %v", err)
		}
		if nextPlan.GrossAmount == 0 || nextPlan.GrossAmount == 10000 {
			t.Fatalf("expected repriced next month plan, got %.2f", nextPlan.GrossAmount)
		}
		createConfirmedBookingForTests(t, app2, SpaceSchedule{
			SlotDate: "2026-11-02", SlotHour: "08:00", EntryType: "booking", Activity: "badminton", Quantity: 1, Title: fmt.Sprintf("Conflict-%d", time.Now().UnixNano()), Status: bookingStatusConfirmed,
		})
		if _, err := app2.continueMCPMonthlyPlan(planID, "2026-11", 0); err == nil {
			t.Fatal("expected continue operation to fail when future availability changes")
		}
	})
}

func TestMCPCustomerOwnershipAndFinanceIntegration(t *testing.T) {
	app := newBookingWorkflowTestApp(t)
	templates, err := buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	app.templates = templates
	seedMCPPricingBands(t, app, MCPPricingBand{Tier: mcpTierWeekdayOffPeak, MinimumSessions: 1, MaximumSessions: 10, PricePerSession: 2500, Active: true})
	customerAID := createMCPTestCustomer(t, app, "mcp-owner-a@example.com")
	customerBID := createMCPTestCustomer(t, app, "mcp-owner-b@example.com")

	planAID, err := app.createMCPMonthlyPlan(customerAID, "2026-09", "badminton", 1, "Owner A", "", []MCPPlanScheduleRule{{Weekday: 1, StartHour: "08:00", EndHour: "09:00"}}, 0, true)
	if err != nil {
		t.Fatalf("create owner A plan: %v", err)
	}
	planBID, err := app.createMCPMonthlyPlan(customerBID, "2026-10", "badminton", 1, "Owner B", "", []MCPPlanScheduleRule{{Weekday: 2, StartHour: "08:00", EndHour: "09:00"}}, 0, false)
	if err != nil {
		t.Fatalf("create owner B plan: %v", err)
	}

	userA, _, err := app.findUserByEmail("mcp-owner-a@example.com")
	if err != nil {
		t.Fatalf("find owner A user: %v", err)
	}
	customerA, err := app.findMCPMonthlyCustomerByUserID(userA.ID)
	if err != nil {
		t.Fatalf("find owner A customer: %v", err)
	}

	t.Run("customer A can view own plan and receivable state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/customer/mcp/"+strconv.FormatInt(planAID, 10), nil)
		rec := httptest.NewRecorder()
		app.customerMCPDetailRouter(rec, req.WithContext(context.WithValue(req.Context(), userContextKey, userA)), userA, customerA, strconv.FormatInt(planAID, 10))
		if rec.Code != http.StatusOK {
			t.Fatalf("view own plan status = %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Owner A") {
			t.Fatalf("own plan response missing plan title: %s", rec.Body.String())
		}
	})

	t.Run("customer A can continue own plan but cannot view or continue customer B plan", func(t *testing.T) {
		form := url.Values{
			"csrf_token": {"token"},
			"plan_month": {"2026-11"},
		}
		continueReq := httptest.NewRequest(http.MethodPost, "/customer/mcp/"+strconv.FormatInt(planAID, 10)+"/continue", strings.NewReader(form.Encode()))
		continueReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		continueReq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
		continueReq.PostForm = form
		continueRec := httptest.NewRecorder()
		app.customerMCPDetailRouter(continueRec, continueReq.WithContext(context.WithValue(continueReq.Context(), userContextKey, userA)), userA, customerA, strconv.FormatInt(planAID, 10)+"/continue")
		if continueRec.Code != http.StatusSeeOther {
			t.Fatalf("continue own plan status = %d body=%s", continueRec.Code, continueRec.Body.String())
		}

		viewReq := httptest.NewRequest(http.MethodGet, "/customer/mcp/"+strconv.FormatInt(planBID, 10), nil)
		viewRec := httptest.NewRecorder()
		app.customerMCPDetailRouter(viewRec, viewReq.WithContext(context.WithValue(viewReq.Context(), userContextKey, userA)), userA, customerA, strconv.FormatInt(planBID, 10))
		if viewRec.Code != http.StatusNotFound {
			t.Fatalf("view other plan status = %d", viewRec.Code)
		}

		otherForm := url.Values{
			"csrf_token": {"token"},
			"plan_month": {"2026-11"},
		}
		otherReq := httptest.NewRequest(http.MethodPost, "/customer/mcp/"+strconv.FormatInt(planBID, 10)+"/continue", strings.NewReader(otherForm.Encode()))
		otherReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		otherReq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
		otherReq.PostForm = otherForm
		otherRec := httptest.NewRecorder()
		app.customerMCPDetailRouter(otherRec, otherReq.WithContext(context.WithValue(otherReq.Context(), userContextKey, userA)), userA, customerA, strconv.FormatInt(planBID, 10)+"/continue")
		if otherRec.Code != http.StatusNotFound {
			t.Fatalf("continue other plan status = %d", otherRec.Code)
		}
	})

	t.Run("MCP customer account cannot access admin MCP management", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/mcp", nil)
		rec := httptest.NewRecorder()
		protected := app.requirePermission(http.HandlerFunc(app.adminMCPManagementHandler), "mcp.manage")
		protected.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), userContextKey, userA)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("admin MCP route status = %d", rec.Code)
		}
	})

	t.Run("MCP payment collection links finance records, totals, and receipt route", func(t *testing.T) {
		if err := app.createRole("finance-officer", []string{"dashboard.view", "finance.manage"}); err != nil {
			t.Fatalf("create finance-officer role: %v", err)
		}
		financeUser, err := app.createManagedUser("Finance", "mcp-finance@example.com", "password-123", []string{"finance-officer"}, true)
		if err != nil {
			t.Fatalf("create finance user: %v", err)
		}
		sportsDivisionID, err := divisionIDByCode(app.db, divisionCodeSports)
		if err != nil {
			t.Fatalf("find sports division: %v", err)
		}
		if err := app.replaceUserDivisions(financeUser.ID, []int64{sportsDivisionID}); err != nil {
			t.Fatalf("assign finance user divisions: %v", err)
		}
		financeUser, _, err = app.findUserByEmail("mcp-finance@example.com")
		if err != nil {
			t.Fatalf("reload finance user: %v", err)
		}
		financeDivisions, err := app.divisionsForUser(financeUser.ID)
		if err != nil {
			t.Fatalf("load finance user divisions: %v", err)
		}
		fillUserDivisions(financeUser, financeDivisions)

		firstTxnID, err := app.collectMCPPayment(planAID, "cash", 2500, "Deposit", financeUser.ID)
		if err != nil {
			t.Fatalf("collect first MCP payment: %v", err)
		}
		secondTxnID, err := app.collectMCPPayment(planAID, "cash", 7500, "Balance", financeUser.ID)
		if err != nil {
			t.Fatalf("collect second MCP payment: %v", err)
		}

		plan, err := app.findMCPMonthlyPlanByID(planAID)
		if err != nil {
			t.Fatalf("reload MCP plan: %v", err)
		}
		if plan.GrossAmount != 10000 || plan.TotalCollected != 10000 || plan.OutstandingAmount != 0 || plan.PaymentStatus != "paid" {
			t.Fatalf("unexpected MCP plan totals: %#v", plan)
		}

		var category, referenceType, sourceType string
		var referenceID, sourceID int64
		if err := app.db.QueryRow(`
			SELECT category, reference_type, reference_id, source_type, COALESCE(source_id, 0)
			FROM finance_transactions
			WHERE id = ?
		`, secondTxnID).Scan(&category, &referenceType, &referenceID, &sourceType, &sourceID); err != nil {
			t.Fatalf("load MCP finance transaction: %v", err)
		}
		if category != "mcp_payment" || referenceType != "mcp_monthly_plan" || referenceID != planAID || sourceType != "mcp_payment_collection" || sourceID == 0 {
			t.Fatalf("unexpected MCP finance transaction linkage: category=%q refType=%q refID=%d sourceType=%q sourceID=%d", category, referenceType, referenceID, sourceType, sourceID)
		}

		var collectionTxnID int64
		if err := app.db.QueryRow(`SELECT finance_transaction_id FROM mcp_payment_collections WHERE id = ?`, sourceID).Scan(&collectionTxnID); err != nil {
			t.Fatalf("load MCP payment collection: %v", err)
		}
		if collectionTxnID != secondTxnID {
			t.Fatalf("MCP payment collection transaction = %d, want %d", collectionTxnID, secondTxnID)
		}

		receiptReq := httptest.NewRequest(http.MethodGet, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(firstTxnID, 10), nil)
		receiptRec := httptest.NewRecorder()
		app.financeReceiptHandler(receiptRec, receiptReq.WithContext(context.WithValue(receiptReq.Context(), userContextKey, financeUser)))
		if receiptRec.Code != http.StatusOK {
			t.Fatalf("receipt route status = %d body=%s", receiptRec.Code, receiptRec.Body.String())
		}

		if _, err := app.collectMCPPayment(planAID, "cash", 1, "Overpay", financeUser.ID); err == nil {
			t.Fatal("expected overpayment to be rejected")
		}
	})

	t.Run("MCP payment collection stores the requested collection date", func(t *testing.T) {
		collectedAt := time.Date(2026, time.August, 22, 14, 10, 0, 0, time.Local).UTC()
		transactionID, err := app.collectMCPPaymentAt(planBID, "cash", 2500, "Backdated MCP payment", collectedAt, 0)
		if err != nil {
			t.Fatalf("collect MCP payment at date: %v", err)
		}

		var paymentCollectedAt time.Time
		if err := app.db.QueryRow(`SELECT collected_at FROM mcp_payment_collections WHERE finance_transaction_id = ?`, transactionID).Scan(&paymentCollectedAt); err != nil {
			t.Fatalf("load MCP payment collection date: %v", err)
		}
		if paymentCollectedAt.In(time.Local).Format("2006-01-02") != "2026-08-22" {
			t.Fatalf("MCP collected_at = %s, want 2026-08-22", paymentCollectedAt.In(time.Local).Format("2006-01-02"))
		}

		transaction, err := app.findFinanceTransactionByID(transactionID)
		if err != nil {
			t.Fatalf("load MCP finance transaction: %v", err)
		}
		if transaction.RecordedAt.In(time.Local).Format("2006-01-02") != "2026-08-22" {
			t.Fatalf("MCP recorded_at = %s, want 2026-08-22", transaction.RecordedAt.In(time.Local).Format("2006-01-02"))
		}
	})
}
