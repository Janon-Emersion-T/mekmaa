package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

var monthlyFeeSMSCycleMu sync.Mutex

func monthlyFeeSMSAutomationEnabled() bool {
	return strings.EqualFold(
		strings.TrimSpace(os.Getenv("MONTHLY_FEE_SMS_ENABLED")),
		"true",
	)
}

func (a *App) runMonthlyFeeSMSReminderCycle(now time.Time) {
	if !monthlyFeeSMSAutomationEnabled() {
		return
	}

	if !monthlyFeeReminderDue(now) {
		return
	}

	if !monthlyFeeSMSCycleMu.TryLock() {
		log.Printf("monthly fee SMS cycle skipped: previous cycle still running")
		return
	}
	defer monthlyFeeSMSCycleMu.Unlock()

	if !a.sms.Enabled {
		log.Printf("monthly fee SMS reminder skipped: SMSLenz is not configured")
		return
	}

	prepared, err := a.prepareMonthlyFeeSMSReminders(now)
	if err != nil {
		log.Printf("monthly fee SMS preparation failed: %v", err)
		return
	}

	sent, err := a.deliverMonthlyFeeSMSReminders(now)
	if err != nil {
		log.Printf("monthly fee SMS delivery failed: %v", err)
		return
	}

	log.Printf(
		"monthly fee SMS reminder cycle completed: prepared=%d sent=%d",
		prepared,
		sent,
	)
}

func (a *App) startMonthlyFeeSMSReminderScheduler() {
	if !monthlyFeeSMSAutomationEnabled() {
		log.Printf("monthly fee SMS scheduler disabled")
		return
	}

	log.Printf(
		"monthly fee SMS scheduler enabled: 15th of each month at 09:00 Asia/Colombo",
	)

	go func() {
		// Check once immediately in case the application
		// starts during the scheduled reminder hour.
		a.runMonthlyFeeSMSReminderCycle(time.Now())

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for now := range ticker.C {
			a.runMonthlyFeeSMSReminderCycle(now)
		}
	}()
}
