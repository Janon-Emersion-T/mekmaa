package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func financeFilterFromRequest(r *http.Request) FinanceFilter {
	query := r.URL.Query()
	page, limit := normalizedFinancePage(
		parseIntQuery(query.Get("page")),
		parseIntQuery(query.Get("limit")),
	)
	filter := FinanceFilter{
		From:                strings.TrimSpace(query.Get("from")),
		To:                  strings.TrimSpace(query.Get("to")),
		Direction:           strings.ToLower(strings.TrimSpace(query.Get("direction"))),
		Categories:          normalizeStringFilterValues(query["category"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		AccountIDs:          normalizeInt64FilterValues(query["account_id"]),
		TransactionTypes:    normalizeStringFilterValues(query["transaction_type"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		SourceTypes:         normalizeStringFilterValues(query["source_type"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		ReferenceTypes:      normalizeStringFilterValues(query["reference_type"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		PaymentMethods:      normalizeStringFilterValues(query["payment_method"], normalizePaymentMethod),
		ApprovalStatuses:    normalizeStringFilterValues(query["approval_status"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		DetailMode:          strings.ToLower(strings.TrimSpace(query.Get("detail_mode"))),
		TrainingProgramIDs:  normalizeInt64FilterValues(query["training_program_id"]),
		BookingActivities:   normalizeStringFilterValues(query["booking_activity"], func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }),
		OneToOneOfferingIDs: normalizeInt64FilterValues(query["one_to_one_offering_id"]),
		RecordedUserID:      parseInt64Query(query.Get("recorded_user_id")),
		Status:              strings.ToLower(strings.TrimSpace(query.Get("status"))),
		Reference:           strings.TrimSpace(query.Get("reference")),
		Search:              strings.TrimSpace(query.Get("search")),
		ExportKind:          strings.ToLower(strings.TrimSpace(query.Get("kind"))),
		Page:                page,
		Limit:               limit,
	}
	if len(filter.Categories) == 1 {
		filter.Category = filter.Categories[0]
	}
	if len(filter.AccountIDs) == 1 {
		filter.AccountID = filter.AccountIDs[0]
	}
	if len(filter.TransactionTypes) == 1 {
		filter.TransactionType = filter.TransactionTypes[0]
	}
	if len(filter.SourceTypes) == 1 {
		filter.SourceType = filter.SourceTypes[0]
	}
	if len(filter.ReferenceTypes) == 1 {
		filter.ReferenceType = filter.ReferenceTypes[0]
	}
	if len(filter.PaymentMethods) == 1 {
		filter.PaymentMethod = filter.PaymentMethods[0]
	}
	if _, err := time.Parse("2006-01-02", filter.From); err != nil {
		filter.From = ""
	}
	if _, err := time.Parse("2006-01-02", filter.To); err != nil {
		filter.To = ""
	}
	if filter.Direction != "income" && filter.Direction != "expense" {
		filter.Direction = ""
	}
	if filter.Status != "active" && filter.Status != "voided" {
		filter.Status = ""
	}
	if filter.DetailMode != "detailed" {
		filter.DetailMode = "summary"
	}
	filter.PaymentMethods = normalizeStringFilterValues(filter.PaymentMethods, func(value string) string {
		if validFinancePaymentMethod(value) {
			return value
		}
		return ""
	})
	if len(filter.PaymentMethods) == 1 {
		filter.PaymentMethod = filter.PaymentMethods[0]
	} else if len(filter.PaymentMethods) == 0 {
		filter.PaymentMethod = ""
	}
	filter.ApprovalStatuses = normalizeStringFilterValues(filter.ApprovalStatuses, func(value string) string {
		if validFinanceApprovalStatus(value) {
			return value
		}
		return ""
	})
	return filter
}

func normalizeStringFilterValues(values []string, normalize func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		value := normalize(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func normalizeInt64FilterValues(values []string) []int64 {
	seen := make(map[int64]struct{}, len(values))
	normalized := make([]int64, 0, len(values))
	for _, raw := range values {
		value := parseInt64Query(raw)
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func financeFilterPageURL(r *http.Request, filter FinanceFilter, page int) string {
	if page < 1 {
		page = 1
	}
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page))
	query.Set("limit", strconv.Itoa(filter.Limit))
	return "/admin/finance/ledger?" + query.Encode() + "#ledger"
}

func financeFilterExportURL(filter FinanceFilter) string {
	query := url.Values{}
	if filter.From != "" {
		query.Set("from", filter.From)
	}
	if filter.To != "" {
		query.Set("to", filter.To)
	}
	if filter.Direction != "" {
		query.Set("direction", filter.Direction)
	}
	if filter.Status != "" {
		query.Set("status", filter.Status)
	}
	if filter.Reference != "" {
		query.Set("reference", filter.Reference)
	}
	if filter.Search != "" {
		query.Set("search", filter.Search)
	}
	for _, value := range filter.Categories {
		query.Add("category", value)
	}
	for _, value := range filter.AccountIDs {
		query.Add("account_id", strconv.FormatInt(value, 10))
	}
	for _, value := range filter.TransactionTypes {
		query.Add("transaction_type", value)
	}
	for _, value := range filter.SourceTypes {
		query.Add("source_type", value)
	}
	for _, value := range filter.ReferenceTypes {
		query.Add("reference_type", value)
	}
	for _, value := range filter.PaymentMethods {
		query.Add("payment_method", value)
	}
	for _, value := range filter.ApprovalStatuses {
		query.Add("approval_status", value)
	}
	if filter.DetailMode != "" {
		query.Set("detail_mode", filter.DetailMode)
	}
	for _, value := range filter.TrainingProgramIDs {
		query.Add("training_program_id", strconv.FormatInt(value, 10))
	}
	for _, value := range filter.BookingActivities {
		query.Add("booking_activity", value)
	}
	for _, value := range filter.OneToOneOfferingIDs {
		query.Add("one_to_one_offering_id", strconv.FormatInt(value, 10))
	}
	if filter.RecordedUserID > 0 {
		query.Set("recorded_user_id", strconv.FormatInt(filter.RecordedUserID, 10))
	}
	return "/admin/finance/export?" + query.Encode()
}
