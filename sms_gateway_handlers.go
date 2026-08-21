package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
)

const smsGatewayExtraTestPhone = "+94 77 435 2345"

func maskSMSPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}

	runes := []rune(phone)
	if len(runes) <= 6 {
		return phone
	}

	prefixLen := 4
	suffixLen := 3

	if len(runes) <= prefixLen+suffixLen {
		return phone
	}

	return string(runes[:prefixLen]) +
		strings.Repeat("•", len(runes)-prefixLen-suffixLen) +
		string(runes[len(runes)-suffixLen:])
}

func (a *App) getSMSGatewayAdminView() (*SMSGatewayAdminView, error) {
	view := &SMSGatewayAdminView{
		GatewayEnabled:    a.sms.Enabled,
		BookingSMSEnabled: a.bookingMessages.SMSEnabled,
		SenderID:          strings.TrimSpace(a.sms.SenderID),
		AlertPhone:        maskSMSPhone(a.sms.AlertPhone),
	}

	var balance sql.NullFloat64
	var chargedFrom string
	var alerted200 int
	var alerted100 int
	var updatedAt sql.NullTime

	err := a.queryRowDB(`
		SELECT
			latest_balance,
			charged_from,
			alerted_200,
			alerted_100,
			updated_at
		FROM sms_gateway_state
		WHERE id = 1
	`).Scan(
		&balance,
		&chargedFrom,
		&alerted200,
		&alerted100,
		&updatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}

	if balance.Valid {
		view.BalanceKnown = true
		view.LatestBalance = balance.Float64
	}

	view.ChargedFrom = strings.TrimSpace(chargedFrom)
	view.Alerted200 = alerted200 != 0
	view.Alerted100 = alerted100 != 0

	if updatedAt.Valid {
		view.UpdatedAt = updatedAt.Time
	}

	return view, nil
}

func (a *App) smsGatewayManagementHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())

	gateway, err := a.getSMSGatewayAdminView()
	if err != nil {
		log.Printf("load sms gateway state: %v", err)
		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "SMS Gateway"
	data.Description = "Monitor SMS delivery and automatic balance alerts."
	data.SMSGateway = gateway

	a.render(
		w,
		"sms-gateway-management",
		data,
		http.StatusOK,
	)
}

func (a *App) smsGatewayTestHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := a.verifyCSRF(r); err != nil {
		http.Error(
			w,
			"invalid request",
			http.StatusForbidden,
		)
		return
	}

	if !a.sms.Enabled {
		a.setFlash(
			w,
			"SMS gateway credentials are not configured.",
		)
		http.Redirect(
			w,
			r,
			"/admin/sms-gateway",
			http.StatusSeeOther,
		)
		return
	}

	recipients := []string{}
	seen := map[string]struct{}{}
	for _, phone := range []string{a.sms.AlertPhone, smsGatewayExtraTestPhone} {
		normalized := strings.TrimSpace(phone)
		if normalized == "" {
			continue
		}
		canonical := strings.ReplaceAll(normalized, " ", "")
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		recipients = append(recipients, normalized)
	}
	if len(recipients) == 0 {
		a.setFlash(
			w,
			"No SMS test recipients are configured.",
		)
		http.Redirect(
			w,
			r,
			"/admin/sms-gateway",
			http.StatusSeeOther,
		)
		return
	}

	for _, recipient := range recipients {
		if err := a.sendSMSMessage(
			recipient,
			"Mekmaa SMS gateway test successful.",
		); err != nil {
			log.Printf("sms gateway admin test: recipient=%s err=%v", recipient, err)

			a.setFlash(
				w,
				"SMS gateway test failed: "+err.Error(),
			)

			http.Redirect(
				w,
				r,
				"/admin/sms-gateway",
				http.StatusSeeOther,
			)
			return
		}
	}

	a.setFlash(
		w,
		"Test SMS sent successfully.",
	)

	http.Redirect(
		w,
		r,
		"/admin/sms-gateway",
		http.StatusSeeOther,
	)
}
