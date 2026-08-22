package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var errFinanceCategoryLocked = errors.New("finance category is linked to existing transactions")

func defaultFinanceCategories() []FinanceCategory {
	return []FinanceCategory{
		{Code: "manual_income", Name: "Manual income", Direction: "income", Active: true},
		{Code: "tournament_entry_income", Name: "Tournament entry income", Direction: "income", Active: true},
		{Code: "sponsorship_income", Name: "Sponsorship income", Direction: "income", Active: true},
		{Code: "other_income", Name: "Other income", Direction: "income", Active: true},
		{Code: "mcp_payment", Name: "Monthly court plan payment", Direction: "income", Active: true},
		{Code: "facility_expense", Name: "Facility or court rental", Direction: "expense", Active: true},
		{Code: "utilities_expense", Name: "Utilities", Direction: "expense", Active: true},
		{Code: "loan_repayment_expense", Name: "Loan repayment", Direction: "expense", Active: true},
		{Code: "staff_salary_expense", Name: "Staff salary", Direction: "expense", Active: true},
		{Code: "electricity_bills_expense", Name: "Utility bills - Electricity bills", Direction: "expense", Active: true},
		{Code: "telephone_bills_expense", Name: "Utility bills - Telephone bills", Direction: "expense", Active: true},
		{Code: "maintenance_expense", Name: "Maintenance and repairs", Direction: "expense", Active: true},
		{Code: "staff_expense", Name: "Staff and wages", Direction: "expense", Active: true},
		{Code: "donation_expense", Name: "Donation", Direction: "expense", Active: true},
		{Code: "stationery_expense", Name: "Stationery", Direction: "expense", Active: true},
		{Code: "equipment_expense", Name: "Equipment", Direction: "expense", Active: true},
		{Code: "sports_supplies_expense", Name: "Sports supplies", Direction: "expense", Active: true},
		{Code: "refreshments_expense", Name: "Refreshments and drinks", Direction: "expense", Active: true},
		{Code: "prizes_expense", Name: "Prizes and awards", Direction: "expense", Active: true},
		{Code: "marketing_expense", Name: "Marketing", Direction: "expense", Active: true},
		{Code: "transport_expense", Name: "Transport", Direction: "expense", Active: true},
		{Code: "event_expense", Name: "Event expense", Direction: "expense", Active: true},
		{Code: "bank_charges_expense", Name: "Bank charges", Direction: "expense", Active: true},
		{Code: "other_expense", Name: "Other expense", Direction: "expense", Active: true},
	}
}

func seedFinanceCategories(db *sql.DB) error {
	now := time.Now().UTC()
	for _, category := range defaultFinanceCategories() {
		if _, err := db.Exec(`
			INSERT INTO finance_categories (code, name, direction, active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(code) DO UPDATE SET
				name = excluded.name,
				direction = excluded.direction,
				active = excluded.active,
				updated_at = excluded.updated_at
		`, category.Code, category.Name, category.Direction, boolToInt(category.Active), now, now); err != nil {
			return err
		}
	}
	return nil
}

func validFinanceCategoryDirection(direction string) bool {
	return direction == "income" || direction == "expense"
}

func buildFinanceCategoryCode(name, direction string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range base {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	code := strings.Trim(b.String(), "_")
	if code == "" {
		return ""
	}
	suffix := "_" + direction
	if !strings.HasSuffix(code, suffix) {
		code += suffix
	}
	return code
}

func (a *App) listFinanceCategories(activeOnly bool) ([]FinanceCategory, error) {
	query := `
		SELECT fc.id,
		       fc.code,
		       fc.name,
		       fc.direction,
		       fc.active,
		       COALESCE(linked.transaction_count, 0),
		       fc.created_at,
		       fc.updated_at
		FROM finance_categories fc
		LEFT JOIN (
			SELECT category, COUNT(*) AS transaction_count
			FROM finance_transactions
			GROUP BY category
		) linked ON linked.category = fc.code
	`
	args := make([]any, 0, 1)
	if activeOnly {
		query += ` WHERE fc.active = 1`
	}
	query += ` ORDER BY CASE fc.direction WHEN 'income' THEN 0 ELSE 1 END, fc.name , fc.id`

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []FinanceCategory
	for rows.Next() {
		var category FinanceCategory
		var active int
		if err := rows.Scan(
			&category.ID,
			&category.Code,
			&category.Name,
			&category.Direction,
			&active,
			&category.LinkedTransactionCount,
			&category.CreatedAt,
			&category.UpdatedAt,
		); err != nil {
			return nil, err
		}
		category.Active = active == 1
		categories = append(categories, category)
	}
	return categories, rows.Err()
}

func (a *App) financeCategoryExists(direction, code string, requireActive bool) (bool, error) {
	query := `SELECT COUNT(*) FROM finance_categories WHERE direction = ? AND code = ?`
	args := []any{direction, code}
	if requireActive {
		query += ` AND active = 1`
	}
	var count int
	if err := a.db.QueryRow(query, args...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (a *App) createFinanceCategory(name, direction string, active bool) error {
	name = strings.TrimSpace(name)
	direction = strings.ToLower(strings.TrimSpace(direction))
	if name == "" || !validFinanceCategoryDirection(direction) {
		return errors.New("a valid category name and direction are required")
	}
	code := buildFinanceCategoryCode(name, direction)
	if code == "" {
		return errors.New("category name must contain letters or numbers")
	}
	now := time.Now().UTC()
	if _, err := a.db.Exec(`
		INSERT INTO finance_categories (code, name, direction, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, code, name, direction, boolToInt(active), now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("a finance category with that name already exists for this direction")
		}
		return err
	}
	return nil
}

func (a *App) updateFinanceCategory(categoryID int64, name, direction string, active bool) error {
	name = strings.TrimSpace(name)
	direction = strings.ToLower(strings.TrimSpace(direction))
	if categoryID <= 0 || name == "" || !validFinanceCategoryDirection(direction) {
		return errors.New("a valid finance category is required")
	}
	locked, err := a.financeCategoryLinked(categoryID)
	if err != nil {
		return err
	}
	if locked {
		return errFinanceCategoryLocked
	}
	code := buildFinanceCategoryCode(name, direction)
	if code == "" {
		return errors.New("category name must contain letters or numbers")
	}
	result, err := a.db.Exec(`
		UPDATE finance_categories
		SET code = ?, name = ?, direction = ?, active = ?, updated_at = ?
		WHERE id = ?
	`, code, name, direction, boolToInt(active), time.Now().UTC(), categoryID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("a finance category with that name already exists for this direction")
		}
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("finance category not found")
	}
	return nil
}

func (a *App) deleteFinanceCategory(categoryID int64) error {
	if categoryID <= 0 {
		return errors.New("finance category not found")
	}
	locked, err := a.financeCategoryLinked(categoryID)
	if err != nil {
		return err
	}
	if locked {
		return errFinanceCategoryLocked
	}
	result, err := a.db.Exec(`DELETE FROM finance_categories WHERE id = ?`, categoryID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("finance category not found")
	}
	return nil
}

func (a *App) financeCategoryLinked(categoryID int64) (bool, error) {
	var count int
	if err := a.db.QueryRow(`
		SELECT COUNT(*)
		FROM finance_transactions ft
		INNER JOIN finance_categories fc ON fc.code = ft.category
		WHERE fc.id = ?
	`, categoryID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func financeActiveCategoriesForDirection(categories []FinanceCategory, direction string) []FinanceCategory {
	filtered := make([]FinanceCategory, 0, len(categories))
	for _, category := range categories {
		if category.Active && category.Direction == direction {
			filtered = append(filtered, category)
		}
	}
	return filtered
}

func financeCategoriesForDirection(categories []FinanceCategory, direction string) []FinanceCategory {
	filtered := make([]FinanceCategory, 0, len(categories))
	for _, category := range categories {
		if category.Direction == direction {
			filtered = append(filtered, category)
		}
	}
	return filtered
}

func financeCategoryLabelFallback(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Transaction"
	}
	trimmed = strings.TrimSuffix(trimmed, "_income")
	trimmed = strings.TrimSuffix(trimmed, "_expense")
	parts := strings.Fields(strings.ReplaceAll(trimmed, "_", " "))
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	if len(parts) == 0 {
		return "Transaction"
	}
	return strings.Join(parts, " ")
}

func financeCategoryLockedMessage(err error) string {
	if errors.Is(err, errFinanceCategoryLocked) {
		return "This category already has linked finance records and cannot be changed."
	}
	return fmt.Sprintf("Finance category could not be updated: %v", err)
}
