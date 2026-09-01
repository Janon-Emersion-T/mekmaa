package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type financeTransactionCreate struct {
	ReceiptNumber    string
	ReferenceNumber  string
	DivisionID       int64
	Category         string
	ApprovalStatus   string
	TransactionType  string
	ReferenceType    string
	ReferenceID      int64
	SourceType       string
	SourceID         int64
	FinanceAccountID int64
	TransferGroupID  string
	PersonName       string
	Description      string
	Notes            string
	PaymentMethod    string
	Amount           float64
	RecordedByUserID int64
	ApprovedByUserID int64
	RecordedAt       time.Time
	ApprovedAt       time.Time
}

type financeStatementRow struct {
	OpeningBalance float64
	ClosingBalance float64
	Rows           []FinanceTransaction
}

func validFinanceAccountType(value string) bool {
	switch value {
	case financeAccountTypeCash, financeAccountTypeBank:
		return true
	default:
		return false
	}
}

func validFinanceTransactionType(value string) bool {
	switch value {
	case financeTxnTypeIncome, financeTxnTypeExpense, financeTxnTypeTransferIn, financeTxnTypeTransferOut, financeTxnTypeOpeningBalance, financeTxnTypeAdjustment:
		return true
	default:
		return false
	}
}

func validFinancePaymentMethod(value string) bool {
	switch value {
	case "cash", "bank_transfer", "qr_pay":
		return true
	default:
		return false
	}
}

func financePaymentMethodForAccount(accountType string) string {
	if accountType == financeAccountTypeBank {
		return "bank_transfer"
	}
	return "cash"
}

func normalizeFinanceAccountCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func generateFinanceAccountCode(accountType string, existingCount int) string {
	prefix := "BANK"
	if accountType == financeAccountTypeCash {
		prefix = "CASH"
	}
	return fmt.Sprintf("%s-%03d", prefix, existingCount+1)
}

func financeDirectionForTransaction(transaction FinanceTransaction) string {
	if transaction.MoneyOut > 0 {
		return "expense"
	}
	return "income"
}

func financeHighRiskAuthorized(user *User, permissions ...string) bool {
	if user == nil || !containsRole(user.Roles, "superadmin") {
		return false
	}
	for _, permission := range permissions {
		if containsPermission(user.Permissions, permission) {
			return true
		}
	}
	return false
}

func financeAccountBalanceEffect(transaction FinanceTransaction) float64 {
	if !listFinanceBalancesInclude(transaction) {
		return 0
	}
	return normalizeMoney(transaction.Amount)
}

func financeAmountParts(amount float64) (float64, float64) {
	amount = normalizeMoney(amount)
	if amount >= 0 {
		return amount, 0
	}
	return 0, normalizeMoney(-amount)
}

func normalizePaymentMethod(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "bank", "bank_transfer":
		return "bank_transfer"
	case "qr", "qr_pay", "qrcode", "qr_code":
		return "qr_pay"
	default:
		return "cash"
	}
}

func financeAccountTypeLabel(value string) string {
	switch value {
	case financeAccountTypeCash:
		return "Cash"
	case financeAccountTypeBank:
		return "Bank"
	default:
		return strings.TrimSpace(value)
	}
}

func financeAccountDisplayName(name, divisionName string) string {
	name = strings.TrimSpace(name)
	divisionName = strings.TrimSpace(divisionName)
	if name == "" {
		return divisionName
	}
	if divisionName == "" {
		return name
	}
	return name + " · " + divisionName
}

func financeTransactionTypeLabel(value string) string {
	switch value {
	case financeTxnTypeIncome:
		return "Income"
	case financeTxnTypeExpense:
		return "Expense"
	case financeTxnTypeTransferIn:
		return "Transfer in"
	case financeTxnTypeTransferOut:
		return "Transfer out"
	case financeTxnTypeOpeningBalance:
		return "Opening balance"
	case financeTxnTypeAdjustment:
		return "Adjustment"
	default:
		return strings.TrimSpace(strings.ReplaceAll(value, "_", " "))
	}
}

func financeTransactionStatusLabel(transaction FinanceTransaction) string {
	if transaction.Voided {
		return "Voided"
	}
	if transaction.ApprovalStatus == financeApprovalPending {
		return "Pending approval"
	}
	return "Active"
}

func financeAccountTone(accountType string) string {
	if accountType == financeAccountTypeBank {
		return "border-sky-100 bg-sky-50"
	}
	return "border-emerald-100 bg-emerald-50"
}

func financeSystemAccountCode(divisionCode, accountType string) string {
	divisionCode = strings.ToUpper(strings.TrimSpace(divisionCode))
	switch accountType {
	case financeAccountTypeBank:
		return divisionCode + "-BANK-001"
	default:
		return divisionCode + "-CASH-001"
	}
}

func financeSystemAccountDescription(divisionName, accountType string) string {
	if strings.TrimSpace(divisionName) == "" {
		divisionName = "Mekmaa"
	}
	if accountType == financeAccountTypeBank {
		return fmt.Sprintf("Primary bank balance for %s.", divisionName)
	}
	return fmt.Sprintf("Physical cash currently held by %s.", divisionName)
}

func loadOperationalDivisionsTx(tx *sql.Tx) ([]Division, error) {
	rows, err := tx.Query(`
		SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at
		FROM divisions
		WHERE UPPER(code) IN ($1, $2, $3, $4)
		ORDER BY name ASC, id ASC
	`, divisionCodeSports, divisionCodeKEC, divisionCodeChess, divisionCodeCorporate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var divisions []Division
	for rows.Next() {
		var division Division
		var active int
		if err := rows.Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
			return nil, err
		}
		division.Active = active == 1
		divisions = append(divisions, division)
	}
	return divisions, rows.Err()
}

func loadDivisionByIDQuery(queryer sqlQueryer, divisionID int64) (*Division, error) {
	row := queryer.QueryRow(`
		SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at
		FROM divisions
		WHERE id = $1
	`, divisionID)
	var division Division
	var active int
	if err := row.Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
		return nil, err
	}
	division.Active = active == 1
	return &division, nil
}

func ensureFinanceDivisionTables(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS divisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_divisions (
			user_id INTEGER NOT NULL,
			division_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, division_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_divisions_division_user ON user_divisions(division_id, user_id)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return seedDivisions(db)
}

func ensureFinanceSystemAccountsTx(tx *sql.Tx) error {
	now := time.Now().UTC()
	divisions, err := loadOperationalDivisionsTx(tx)
	if err != nil {
		return err
	}
	for _, division := range divisions {
		required := []FinanceAccount{
			{DivisionID: division.ID, DivisionCode: division.Code, DivisionName: division.Name, Name: financeAccountCashInHand, AccountCode: financeSystemAccountCode(division.Code, financeAccountTypeCash), AccountType: financeAccountTypeCash, Description: financeSystemAccountDescription(division.Name, financeAccountTypeCash), IsSystem: true, IsActive: true},
			{DivisionID: division.ID, DivisionCode: division.Code, DivisionName: division.Name, Name: financeAccountMainBank, AccountCode: financeSystemAccountCode(division.Code, financeAccountTypeBank), AccountType: financeAccountTypeBank, Description: financeSystemAccountDescription(division.Name, financeAccountTypeBank), IsSystem: true, IsActive: true},
		}
		for _, account := range required {
			var existingID int64
			err := tx.QueryRow(`
				SELECT id
				FROM finance_accounts
				WHERE division_id = $1 AND LOWER(name) = LOWER($2)
				LIMIT 1
			`, account.DivisionID, account.Name).Scan(&existingID)
			if err == nil {
				if _, updateErr := tx.Exec(`
					UPDATE finance_accounts
					SET account_code = COALESCE(NULLIF(account_code, ''), $1),
					    account_type = COALESCE(NULLIF(account_type, ''), $2),
					    description = CASE WHEN TRIM(COALESCE(description, '')) = '' THEN $3 ELSE description END,
					    is_system = 1,
					    is_active = 1,
					    updated_at = $4
					WHERE id = $5
				`, account.AccountCode, account.AccountType, account.Description, now, existingID); updateErr != nil {
					return updateErr
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(`
				INSERT INTO finance_accounts (
					division_id, account_code, name, account_type, description, opening_balance, is_system, is_active,
					created_at, updated_at, created_by_user_id, updated_by_user_id
				) VALUES ($1, $2, $3, $4, $5, 0, 1, 1, $6, $7, NULL, NULL)
			`, account.DivisionID, account.AccountCode, account.Name, account.AccountType, account.Description, now, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureFinanceAccountForDivisionTx(tx *sql.Tx, account *FinanceAccount, divisionID int64) (int64, error) {
	if account == nil {
		return 0, errors.New("finance account is required")
	}
	if divisionID <= 0 {
		return 0, errors.New("finance account division is required")
	}
	if account.DivisionID == divisionID {
		return account.ID, nil
	}
	existing, err := findFinanceAccountByNameTx(tx, divisionID, account.Name)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	division, err := loadDivisionByIDQuery(tx, divisionID)
	if err != nil {
		return 0, err
	}
	accountCode := account.AccountCode
	if account.IsSystem {
		accountCode = financeSystemAccountCode(division.Code, account.AccountType)
	}
	now := time.Now().UTC()
	var accountID int64
	err = tx.QueryRow(
		`INSERT INTO finance_accounts (
			division_id, account_code, name, account_type, description, opening_balance, is_system, is_active,
			created_at, updated_at, created_by_user_id, updated_by_user_id
		) VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		divisionID,
		accountCode,
		account.Name,
		account.AccountType,
		account.Description,
		boolToInt(account.IsSystem),
		boolToInt(account.IsActive),
		now,
		now,
		nullIfZero(account.CreatedByUserID),
		nullIfZero(account.UpdatedByUserID),
	).Scan(&accountID)
	if err != nil {
		return 0, err
	}
	return accountID, nil
}

func migrateFinanceAccountOwnershipTx(tx *sql.Tx) error {
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_accounts
		SET division_id = (
			SELECT MIN(ft.division_id)
			FROM finance_transactions ft
			WHERE ft.finance_account_id = finance_accounts.id
			  AND COALESCE(ft.division_id, 0) > 0
			GROUP BY ft.finance_account_id
			HAVING COUNT(DISTINCT ft.division_id) = 1
		)
		WHERE COALESCE(division_id, 0) <= 0
	`); err != nil {
		return err
	}

	rows, err := tx.Query(`
		SELECT fa.id, COALESCE(fa.division_id, 0), fa.account_code, fa.name, fa.account_type, fa.description, fa.opening_balance,
		       fa.is_system, fa.is_active, COALESCE(fa.created_by_user_id, 0), COALESCE(fa.updated_by_user_id, 0), fa.created_at, fa.updated_at
		FROM finance_accounts fa
		WHERE COALESCE(fa.division_id, 0) <= 0
		  AND EXISTS (
			SELECT 1
			FROM finance_transactions ft
			WHERE ft.finance_account_id = fa.id
			  AND COALESCE(ft.division_id, 0) > 0
			GROUP BY ft.finance_account_id
			HAVING COUNT(DISTINCT ft.division_id) > 1
		  )
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var account FinanceAccount
		var isSystem, isActive int
		if err := rows.Scan(
			&account.ID, &account.DivisionID, &account.AccountCode, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
			&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
		); err != nil {
			return err
		}
		account.IsSystem = isSystem == 1
		account.IsActive = isActive == 1

		distinctRows, err := tx.Query(`
			SELECT DISTINCT division_id
			FROM finance_transactions
			WHERE finance_account_id = $1
			  AND COALESCE(division_id, 0) > 0
			ORDER BY division_id ASC
		`, account.ID)
		if err != nil {
			return err
		}
		var divisionIDs []int64
		for distinctRows.Next() {
			var divisionID int64
			if err := distinctRows.Scan(&divisionID); err != nil {
				distinctRows.Close()
				return err
			}
			divisionIDs = append(divisionIDs, divisionID)
		}
		if err := distinctRows.Err(); err != nil {
			distinctRows.Close()
			return err
		}
		distinctRows.Close()
		if len(divisionIDs) == 0 {
			continue
		}

		keepDivisionID := divisionIDs[0]
		for _, divisionID := range divisionIDs {
			if divisionID == sportsID {
				keepDivisionID = divisionID
				break
			}
		}
		if _, err := tx.Exec(`UPDATE finance_accounts SET division_id = $1 WHERE id = $2`, keepDivisionID, account.ID); err != nil {
			return err
		}
		account.DivisionID = keepDivisionID

		for _, divisionID := range divisionIDs {
			if divisionID == keepDivisionID {
				continue
			}
			targetAccountID, err := ensureFinanceAccountForDivisionTx(tx, &account, divisionID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`
				UPDATE finance_transactions
				SET finance_account_id = $1
				WHERE finance_account_id = $2
				  AND division_id = $3
			`, targetAccountID, account.ID, divisionID); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET division_id = (
			SELECT fa.division_id
			FROM finance_accounts fa
			WHERE fa.id = finance_transactions.finance_account_id
		)
		WHERE COALESCE(division_id, 0) <= 0
		  AND COALESCE(finance_account_id, 0) > 0
	`); err != nil {
		return err
	}

	mismatchRows, err := tx.Query(`
		SELECT ft.id, ft.division_id,
		       fa.id, fa.division_id, fa.account_code, fa.name, fa.account_type, fa.description, fa.opening_balance,
		       fa.is_system, fa.is_active, COALESCE(fa.created_by_user_id, 0), COALESCE(fa.updated_by_user_id, 0), fa.created_at, fa.updated_at
		FROM finance_transactions ft
		JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		WHERE COALESCE(ft.division_id, 0) > 0
		  AND COALESCE(fa.division_id, 0) > 0
		  AND ft.division_id <> fa.division_id
	`)
	if err != nil {
		return err
	}
	defer mismatchRows.Close()

	for mismatchRows.Next() {
		var transactionID int64
		var divisionID int64
		var account FinanceAccount
		var isSystem, isActive int
		if err := mismatchRows.Scan(
			&transactionID, &divisionID,
			&account.ID, &account.DivisionID, &account.AccountCode, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
			&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
		); err != nil {
			return err
		}
		account.IsSystem = isSystem == 1
		account.IsActive = isActive == 1
		targetAccountID, err := ensureFinanceAccountForDivisionTx(tx, &account, divisionID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE finance_transactions SET finance_account_id = $1 WHERE id = $2`, targetAccountID, transactionID); err != nil {
			return err
		}
	}
	if err := mismatchRows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE finance_accounts SET division_id = $1 WHERE COALESCE(division_id, 0) <= 0`, sportsID); err != nil {
		return err
	}
	return nil
}

func migrateFinanceCashbook(db *sql.DB) error {
	if err := ensureFinanceDivisionTables(db); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS finance_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			division_id INTEGER,
			account_code TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			account_type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			opening_balance REAL NOT NULL DEFAULT 0,
			is_system INTEGER NOT NULL DEFAULT 0,
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			created_by_user_id INTEGER,
			updated_by_user_id INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_accounts_type_active ON finance_accounts(account_type, is_active)`,
		`CREATE TABLE IF NOT EXISTS cash_reconciliations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			finance_account_id INTEGER NOT NULL,
			reconciliation_date TEXT NOT NULL,
			expected_balance REAL NOT NULL DEFAULT 0,
			counted_balance REAL NOT NULL DEFAULT 0,
			difference REAL NOT NULL DEFAULT 0,
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'balanced',
			reconciled_by_user_id INTEGER,
			void_reason TEXT NOT NULL DEFAULT '',
			voided_by_user_id INTEGER,
			voided_at DATETIME,
			superseded_by_reconciliation_id INTEGER,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (finance_account_id) REFERENCES finance_accounts(id),
			FOREIGN KEY (reconciled_by_user_id) REFERENCES users(id),
			FOREIGN KEY (voided_by_user_id) REFERENCES users(id),
			FOREIGN KEY (superseded_by_reconciliation_id) REFERENCES cash_reconciliations(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cash_reconciliations_date ON cash_reconciliations(reconciliation_date DESC)`,
		`CREATE TABLE IF NOT EXISTS finance_operation_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			operation_scope TEXT NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			fingerprint TEXT NOT NULL,
			result_ref TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			UNIQUE(operation_scope, user_id, fingerprint)
		)`,
		`CREATE TABLE IF NOT EXISTS finance_period_locks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			locked_until TEXT,
			notes TEXT NOT NULL DEFAULT '',
			updated_by_user_id INTEGER,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (updated_by_user_id) REFERENCES users(id)
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	for _, migration := range []struct {
		table  string
		column string
		stmt   string
	}{
		{"finance_accounts", "division_id", `ALTER TABLE finance_accounts ADD COLUMN division_id INTEGER`},
		{"finance_accounts", "account_code", `ALTER TABLE finance_accounts ADD COLUMN account_code TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "division_id", `ALTER TABLE finance_transactions ADD COLUMN division_id INTEGER`},
		{"finance_transactions", "finance_account_id", `ALTER TABLE finance_transactions ADD COLUMN finance_account_id INTEGER`},
		{"finance_transactions", "approval_status", `ALTER TABLE finance_transactions ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'approved'`},
		{"finance_transactions", "approved_by_user_id", `ALTER TABLE finance_transactions ADD COLUMN approved_by_user_id INTEGER`},
		{"finance_transactions", "approved_at", `ALTER TABLE finance_transactions ADD COLUMN approved_at DATETIME`},
		{"finance_transactions", "transaction_type", `ALTER TABLE finance_transactions ADD COLUMN transaction_type TEXT NOT NULL DEFAULT 'income'`},
		{"finance_transactions", "source_type", `ALTER TABLE finance_transactions ADD COLUMN source_type TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "source_id", `ALTER TABLE finance_transactions ADD COLUMN source_id INTEGER`},
		{"finance_transactions", "transfer_group_id", `ALTER TABLE finance_transactions ADD COLUMN transfer_group_id TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "reference_number", `ALTER TABLE finance_transactions ADD COLUMN reference_number TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "notes", `ALTER TABLE finance_transactions ADD COLUMN notes TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "voided_at", `ALTER TABLE finance_transactions ADD COLUMN voided_at DATETIME`},
		{"finance_transactions", "voided_by_user_id", `ALTER TABLE finance_transactions ADD COLUMN voided_by_user_id INTEGER`},
		{"finance_transactions", "void_reason", `ALTER TABLE finance_transactions ADD COLUMN void_reason TEXT NOT NULL DEFAULT ''`},
		{"finance_transactions", "updated_at", `ALTER TABLE finance_transactions ADD COLUMN updated_at DATETIME`},
		{"student_monthly_payments", "voided", `ALTER TABLE student_monthly_payments ADD COLUMN voided INTEGER NOT NULL DEFAULT 0`},
		{"student_monthly_payments", "void_reason", `ALTER TABLE student_monthly_payments ADD COLUMN void_reason TEXT NOT NULL DEFAULT ''`},
		{"student_monthly_payments", "voided_by_user_id", `ALTER TABLE student_monthly_payments ADD COLUMN voided_by_user_id INTEGER`},
		{"student_monthly_payments", "voided_at", `ALTER TABLE student_monthly_payments ADD COLUMN voided_at DATETIME`},
		{"admissions", "payment_void_reason", `ALTER TABLE admissions ADD COLUMN payment_void_reason TEXT NOT NULL DEFAULT ''`},
		{"admissions", "payment_voided_by_user_id", `ALTER TABLE admissions ADD COLUMN payment_voided_by_user_id INTEGER`},
		{"admissions", "payment_voided_at", `ALTER TABLE admissions ADD COLUMN payment_voided_at DATETIME`},
		{"booking_referrals", "void_reason", `ALTER TABLE booking_referrals ADD COLUMN void_reason TEXT NOT NULL DEFAULT ''`},
		{"booking_referrals", "voided_by_user_id", `ALTER TABLE booking_referrals ADD COLUMN voided_by_user_id INTEGER`},
		{"booking_referrals", "voided_at", `ALTER TABLE booking_referrals ADD COLUMN voided_at DATETIME`},
		{"cash_reconciliations", "void_reason", `ALTER TABLE cash_reconciliations ADD COLUMN void_reason TEXT NOT NULL DEFAULT ''`},
		{"cash_reconciliations", "voided_by_user_id", `ALTER TABLE cash_reconciliations ADD COLUMN voided_by_user_id INTEGER`},
		{"cash_reconciliations", "voided_at", `ALTER TABLE cash_reconciliations ADD COLUMN voided_at DATETIME`},
		{"cash_reconciliations", "superseded_by_reconciliation_id", `ALTER TABLE cash_reconciliations ADD COLUMN superseded_by_reconciliation_id INTEGER`},
	} {
		exists, err := tableHasColumn(db, migration.table, migration.column)
		if err != nil {
			return fmt.Errorf("check %s %s column: %w", migration.table, migration.column, err)
		}
		if !exists {
			if _, err := db.Exec(migration.stmt); err != nil {
				return fmt.Errorf("add %s %s column: %w", migration.table, migration.column, err)
			}
		}
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_cash_reconciliations_account_date`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_cash_reconciliations_account_date_active ON cash_reconciliations(finance_account_id, reconciliation_date) WHERE voided_at IS NULL`); err != nil {
		return fmt.Errorf("create active cash reconciliation unique index: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_account ON finance_transactions(finance_account_id, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_approval ON finance_transactions(approval_status, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_type ON finance_transactions(transaction_type, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_status ON finance_transactions(voided_at, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_source ON finance_transactions(source_type, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_transfer_group ON finance_transactions(transfer_group_id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_finance_accounts_name_ci`,
		`DROP INDEX IF EXISTS idx_finance_accounts_code_ci`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := migrateFinanceAccountOwnershipTx(tx); err != nil {
		return err
	}
	if err := ensureFinanceSystemAccountsTx(tx); err != nil {
		return err
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	var cashAccountID, bankAccountID int64
	if err := tx.QueryRow(`SELECT id FROM finance_accounts WHERE division_id = $1 AND LOWER(name) = LOWER($2) LIMIT 1`, sportsID, financeAccountCashInHand).Scan(&cashAccountID); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT id FROM finance_accounts WHERE division_id = $1 AND LOWER(name) = LOWER($2) LIMIT 1`, sportsID, financeAccountMainBank).Scan(&bankAccountID); err != nil {
		return err
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE finance_transactions SET reference_number = receipt_number WHERE TRIM(COALESCE(reference_number, '')) = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET source_type = reference_type WHERE TRIM(COALESCE(source_type, '')) = '' AND TRIM(COALESCE(reference_type, '')) <> ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET source_id = reference_id WHERE source_id IS NULL AND reference_id IS NOT NULL`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET updated_at = COALESCE(updated_at, created_at, recorded_at, $1)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET approval_status = COALESCE(NULLIF(approval_status, ''), 'approved')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET approved_at = COALESCE(approved_at, created_at, recorded_at, $1) WHERE approval_status = 'approved'`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET payment_method = 'cash' WHERE TRIM(COALESCE(payment_method, '')) = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_accounts SET account_code = CASE WHEN LOWER(account_type) = 'cash' THEN 'CASH-' || SUBSTR('000' || CAST(finance_accounts.id AS TEXT), LENGTH('000' || CAST(finance_accounts.id AS TEXT)) - 2, 3) ELSE 'BANK-' || SUBSTR('000' || CAST(finance_accounts.id AS TEXT), LENGTH('000' || CAST(finance_accounts.id AS TEXT)) - 2, 3) END WHERE TRIM(COALESCE(account_code, '')) = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_accounts
		SET account_code = CASE
			WHEN LOWER(account_type) = 'cash' THEN 'CASH-' || SUBSTR('000' || CAST(finance_accounts.id AS TEXT), LENGTH('000' || CAST(finance_accounts.id AS TEXT)) - 2, 3)
			ELSE 'BANK-' || SUBSTR('000' || CAST(finance_accounts.id AS TEXT), LENGTH('000' || CAST(finance_accounts.id AS TEXT)) - 2, 3)
		END
		WHERE id IN (
			SELECT fa.id
			FROM finance_accounts fa
			JOIN (
				SELECT UPPER(TRIM(account_code)) AS normalized_code, MIN(id) AS keep_id
				FROM finance_accounts
				WHERE TRIM(COALESCE(account_code, '')) <> ''
				GROUP BY UPPER(TRIM(account_code))
				HAVING COUNT(*) > 1
			) dup ON dup.normalized_code = UPPER(TRIM(fa.account_code))
			WHERE fa.id <> dup.keep_id
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET transaction_type = CASE
			WHEN category = 'opening_balance' THEN 'opening_balance'
			WHEN category = 'cash_adjustment' THEN 'adjustment'
			WHEN amount < 0 THEN 'expense'
			ELSE 'income'
		END
		WHERE TRIM(COALESCE(transaction_type, '')) = ''
		   OR transaction_type = 'income'
	`); err != nil {
		return err
	}

	rows, err := tx.Query(`
		SELECT id, category, COALESCE(payment_method, ''), amount
		FROM finance_transactions
		WHERE finance_account_id IS NULL OR finance_account_id = 0
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			transactionID int64
			category      string
			paymentMethod string
			amount        float64
		)
		if err := rows.Scan(&transactionID, &category, &paymentMethod, &amount); err != nil {
			return err
		}
		accountID := cashAccountID
		paymentMethod = normalizePaymentMethod(paymentMethod)
		if paymentMethod == "bank_transfer" {
			accountID = bankAccountID
		}
		if category == "referral_commission_payment" && paymentMethod == "" {
			accountID = cashAccountID
		}
		if amount < 0 && paymentMethod == "bank_transfer" {
			accountID = bankAccountID
		}
		if _, err := tx.Exec(`UPDATE finance_transactions SET finance_account_id = $1 WHERE id = $2`, accountID, transactionID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'booking_payment_collection',
		    source_id = (
				SELECT bpc.id FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = finance_transactions.id
				LIMIT 1
			)
		WHERE category = 'booking_payment'
		  AND EXISTS (
				SELECT 1 FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = finance_transactions.id
		  )
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'student_monthly_payment',
		    source_id = (
				SELECT smp.id FROM student_monthly_payments smp
				WHERE smp.finance_transaction_id = finance_transactions.id
				LIMIT 1
			)
		WHERE category = 'student_monthly_payment'
		  AND EXISTS (
				SELECT 1 FROM student_monthly_payments smp
				WHERE smp.finance_transaction_id = finance_transactions.id
		  )
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'booking_referral_payment',
		    source_id = (
				SELECT br.id FROM booking_referrals br
				WHERE br.finance_transaction_id = finance_transactions.id
				LIMIT 1
			)
		WHERE category = 'referral_commission_payment'
		  AND EXISTS (
				SELECT 1 FROM booking_referrals br
				WHERE br.finance_transaction_id = finance_transactions.id
		  )
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'admission',
		    source_id = reference_id
		WHERE category = 'admission_payment'
		  AND TRIM(COALESCE(source_type, '')) IN ('', 'admission')
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET voided_at = (
				SELECT bpc.voided_at
				FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = finance_transactions.id
				  AND bpc.voided = 1
				LIMIT 1
			),
		    voided_by_user_id = (
				SELECT bpc.voided_by_user_id
				FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = finance_transactions.id
				  AND bpc.voided = 1
				LIMIT 1
			),
		    void_reason = COALESCE((
				SELECT bpc.void_reason
				FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = finance_transactions.id
				  AND bpc.voided = 1
				LIMIT 1
			), '')
		WHERE EXISTS (
			SELECT 1
			FROM booking_payment_collections bpc
			WHERE bpc.finance_transaction_id = finance_transactions.id
			  AND bpc.voided = 1
		)
	`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, spec := range []struct {
		indexName  string
		sourceType string
	}{
		{"idx_finance_transactions_source_admission", "admission"},
		{"idx_finance_transactions_source_student_monthly_payment", "student_monthly_payment"},
		{"idx_finance_transactions_source_booking_payment_collection", "booking_payment_collection"},
		{"idx_finance_transactions_source_booking_referral_payment", "booking_referral_payment"},
	} {
		if err := ensureFinanceSourceUniqueIndex(db, spec.indexName, spec.sourceType); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_finance_accounts_name_ci`,
		`DROP INDEX IF EXISTS idx_finance_accounts_code_ci`,
		`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_type_active ON finance_accounts(division_id, account_type, is_active, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_finance_accounts_division_name_ci ON finance_accounts(division_id, LOWER(name))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_finance_accounts_division_code_ci ON finance_accounts(division_id, UPPER(account_code)) WHERE TRIM(COALESCE(account_code, '')) <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_division_account ON finance_transactions(division_id, finance_account_id, recorded_at DESC, id DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE finance_accounts SET division_id = COALESCE(division_id, 0)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_name ON finance_accounts(division_id, name )`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_code ON finance_accounts(division_id, account_code )`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_cash_reconciliations_account_voided ON cash_reconciliations(finance_account_id, voided_at, reconciliation_date DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_active ON finance_accounts(division_id, is_active, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_type_active ON finance_accounts(division_id, account_type, is_active)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_lookup ON finance_accounts(division_id, LOWER(name), UPPER(account_code))`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_system ON finance_accounts(division_id, is_system, is_active)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_transactions_account_division ON finance_transactions(finance_account_id, division_id, recorded_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_id ON finance_accounts(division_id, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_system_name ON finance_accounts(division_id, is_system, name )`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_account_type ON finance_accounts(division_id, account_type, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_transactions_division_payment ON finance_transactions(division_id, payment_method, recorded_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_transactions_division_type_account ON finance_transactions(division_id, transaction_type, finance_account_id, recorded_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_created ON finance_accounts(division_id, created_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_updated ON finance_accounts(division_id, updated_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_description ON finance_accounts(division_id, description)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_opening ON finance_accounts(division_id, opening_balance)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_flags ON finance_accounts(division_id, is_system, is_active, account_type)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_name_id ON finance_accounts(division_id, name , id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_code_id ON finance_accounts(division_id, account_code , id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_finance_accounts_division_type_name ON finance_accounts(division_id, account_type, name )`); err != nil {
		return fmt.Errorf("create finance account code index: %w", err)
	}
	return nil
}

func financeSourceDuplicateCount(db *sql.DB, sourceType string) (int, error) {
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM (
			SELECT source_id
			FROM finance_transactions
			WHERE source_type = $1
			  AND COALESCE(source_id, 0) <> 0
			GROUP BY source_id
			HAVING COUNT(*) > 1
		)
	`, sourceType).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func ensureFinanceSourceUniqueIndex(db *sql.DB, indexName string, sourceType string) error {
	duplicates, err := financeSourceDuplicateCount(db, sourceType)
	if err != nil {
		return err
	}
	if duplicates > 0 {
		log.Printf("startup finance migration warning: skipped %s because %d legacy duplicate %s source link(s) exist", indexName, duplicates, sourceType)
		return nil
	}
	_, err = db.Exec(fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS %s ON finance_transactions(source_type, source_id) WHERE source_type = %q`,
		indexName,
		sourceType,
	))
	return err
}

func (a *App) listFinanceAccounts(activeOnly bool) ([]FinanceAccount, error) {
	return a.listFinanceAccountsByDivisionIDs(nil, activeOnly)
}

func (a *App) listFinanceAccountsByDivisionIDs(divisionIDs []int64, activeOnly bool) ([]FinanceAccount, error) {
	query := `
		SELECT finance_accounts.id, COALESCE(finance_accounts.division_id, 0), COALESCE(divisions.code, ''), COALESCE(divisions.name, ''),
		       finance_accounts.account_code, finance_accounts.name, finance_accounts.account_type, finance_accounts.description, finance_accounts.opening_balance, finance_accounts.is_system, finance_accounts.is_active,
		       COALESCE(finance_accounts.created_by_user_id, 0), COALESCE(finance_accounts.updated_by_user_id, 0), finance_accounts.created_at, finance_accounts.updated_at
		FROM finance_accounts
		LEFT JOIN divisions ON divisions.id = finance_accounts.division_id
	`
	var conditions []string
	args := make([]any, 0, len(divisionIDs))
	if activeOnly {
		conditions = append(conditions, `is_active = 1`)
	}
	if len(divisionIDs) > 0 {
		placeholders := make([]string, 0, len(divisionIDs))
		for _, divisionID := range divisionIDs {
			placeholders = append(placeholders, "?")
			args = append(args, divisionID)
		}
		conditions = append(conditions, `finance_accounts.division_id IN (`+strings.Join(placeholders, ",")+`)`)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY divisions.name , finance_accounts.is_system DESC, finance_accounts.account_type ASC, finance_accounts.account_code , finance_accounts.name , finance_accounts.id ASC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []FinanceAccount
	for rows.Next() {
		var account FinanceAccount
		var isSystem, isActive int
		if err := rows.Scan(
			&account.ID, &account.DivisionID, &account.DivisionCode, &account.DivisionName,
			&account.AccountCode, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
			&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
		); err != nil {
			return nil, err
		}
		account.IsSystem = isSystem == 1
		account.IsActive = isActive == 1
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func findFinanceAccountByIDQuery(queryer sqlQueryer, accountID int64) (*FinanceAccount, error) {
	row := queryer.QueryRow(`
		SELECT finance_accounts.id, COALESCE(finance_accounts.division_id, 0), COALESCE(divisions.code, ''), COALESCE(divisions.name, ''),
		       finance_accounts.account_code, finance_accounts.name, finance_accounts.account_type, finance_accounts.description, finance_accounts.opening_balance, finance_accounts.is_system, finance_accounts.is_active,
		       COALESCE(finance_accounts.created_by_user_id, 0), COALESCE(finance_accounts.updated_by_user_id, 0), finance_accounts.created_at, finance_accounts.updated_at
		FROM finance_accounts
		LEFT JOIN divisions ON divisions.id = finance_accounts.division_id
		WHERE finance_accounts.id = $1
	`, accountID)
	var account FinanceAccount
	var isSystem, isActive int
	if err := row.Scan(
		&account.ID, &account.DivisionID, &account.DivisionCode, &account.DivisionName,
		&account.AccountCode, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
		&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
	); err != nil {
		return nil, err
	}
	account.IsSystem = isSystem == 1
	account.IsActive = isActive == 1
	return &account, nil
}

func (a *App) findFinanceAccountByID(accountID int64) (*FinanceAccount, error) {
	return findFinanceAccountByIDQuery(a.db, accountID)
}

func normalizeFinanceAccountName(name string) string {
	return strings.TrimSpace(name)
}

func (a *App) createFinanceAccount(divisionID int64, accountCode, name, accountType, description string, createdByUserID int64) (int64, error) {
	name = normalizeFinanceAccountName(name)
	accountCode = normalizeFinanceAccountCode(accountCode)
	description = strings.TrimSpace(description)
	if divisionID <= 0 {
		return 0, errors.New("finance account division is required")
	}
	if name == "" {
		return 0, errors.New("account name is required")
	}
	if !validFinanceAccountType(accountType) {
		return 0, errors.New("a valid account type is required")
	}
	if accountCode == "" {
		var count int
		if err := a.queryRowDB(`SELECT COUNT(*) FROM finance_accounts WHERE division_id = ? AND account_type = ?`, divisionID, accountType).Scan(&count); err != nil {
			return 0, err
		}
		if division, err := a.findDivisionByID(divisionID); err == nil {
			accountCode = financeSystemAccountCode(division.Code, accountType)
			if count > 0 {
				accountCode = fmt.Sprintf("%s-%03d", strings.TrimSuffix(accountCode, "001"), count+1)
			}
		} else {
			accountCode = generateFinanceAccountCode(accountType, count)
		}
	}
	now := time.Now().UTC()
	accountID, err := a.insertAndReturnID(`
		INSERT INTO finance_accounts (
			division_id, account_code, name, account_type, description, opening_balance, is_system, is_active,
			created_at, updated_at, created_by_user_id, updated_by_user_id
		) VALUES (?, ?, ?, ?, ?, 0, 0, 1, ?, ?, ?, ?)
	`, divisionID, accountCode, name, accountType, description, now, now, nullIfZero(createdByUserID), nullIfZero(createdByUserID))
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, errors.New("a finance account with that name or code already exists")
		}
		return 0, err
	}
	return accountID, nil
}

func (a *App) updateFinanceAccount(accountID int64, accountCode, name, accountType, description string, isActive bool, updatedByUserID int64) error {
	name = normalizeFinanceAccountName(name)
	accountCode = normalizeFinanceAccountCode(accountCode)
	description = strings.TrimSpace(description)
	if accountID <= 0 {
		return errors.New("finance account is required")
	}
	if name == "" {
		return errors.New("account name is required")
	}
	if !validFinanceAccountType(accountType) {
		return errors.New("a valid account type is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("finance account was not found")
		}
		return err
	}
	if account.IsSystem {
		accountCode = account.AccountCode
		name = account.Name
		accountType = account.AccountType
		isActive = true
	}
	if accountCode == "" {
		accountCode = account.AccountCode
	}
	var transactionCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE finance_account_id = $1`, accountID).Scan(&transactionCount); err != nil {
		return err
	}
	if transactionCount > 0 && (!strings.EqualFold(name, account.Name) || accountType != account.AccountType || !strings.EqualFold(accountCode, account.AccountCode)) {
		return errors.New("accounts with finance history cannot change code, name, or type")
	}
	if !isActive {
		if transactionCount > 0 {
			return errors.New("accounts with finance history cannot be deactivated")
		}
	}
	_, err = tx.Exec(`
		UPDATE finance_accounts
		SET account_code = $1, name = $2, account_type = $3, description = $4, is_active = $5, updated_at = $6, updated_by_user_id = $7
		WHERE id = $8
	`, accountCode, name, accountType, description, boolToInt(isActive), time.Now().UTC(), nullIfZero(updatedByUserID), accountID)
	if err != nil {
		if isUniqueConstraintError(err) {
			return errors.New("a finance account with that name or code already exists")
		}
		return err
	}
	return tx.Commit()
}

func (a *App) deleteFinanceAccount(accountID int64) error {
	if accountID <= 0 {
		return errors.New("finance account is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("finance account was not found")
		}
		return err
	}
	if account.IsSystem {
		return errors.New("system accounts cannot be deleted")
	}

	var transactionCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM finance_transactions WHERE finance_account_id = $1`, accountID).Scan(&transactionCount); err != nil {
		return err
	}
	if transactionCount > 0 {
		return errors.New("linked accounts cannot be deleted")
	}

	var reconciliationCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM cash_reconciliations WHERE finance_account_id = $1`, accountID).Scan(&reconciliationCount); err != nil {
		return err
	}
	if reconciliationCount > 0 {
		return errors.New("linked accounts cannot be deleted")
	}

	result, err := tx.Exec(`DELETE FROM finance_accounts WHERE id = $1`, accountID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("finance account was not found")
	}

	return tx.Commit()
}

func findFinanceAccountByNameTx(tx *sql.Tx, divisionID int64, name string) (*FinanceAccount, error) {
	row := tx.QueryRow(`
		SELECT finance_accounts.id, COALESCE(finance_accounts.division_id, 0), COALESCE(divisions.code, ''), COALESCE(divisions.name, ''),
		       finance_accounts.account_code, finance_accounts.name, finance_accounts.account_type, finance_accounts.description, finance_accounts.opening_balance, finance_accounts.is_system, finance_accounts.is_active,
		       COALESCE(finance_accounts.created_by_user_id, 0), COALESCE(finance_accounts.updated_by_user_id, 0), finance_accounts.created_at, finance_accounts.updated_at
		FROM finance_accounts
		LEFT JOIN divisions ON divisions.id = finance_accounts.division_id
		WHERE finance_accounts.division_id = $1
		  AND LOWER(finance_accounts.name) = LOWER($2)
		LIMIT 1
	`, divisionID, name)
	var account FinanceAccount
	var isSystem, isActive int
	if err := row.Scan(
		&account.ID, &account.DivisionID, &account.DivisionCode, &account.DivisionName,
		&account.AccountCode, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
		&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
	); err != nil {
		return nil, err
	}
	account.IsSystem = isSystem == 1
	account.IsActive = isActive == 1
	return &account, nil
}

func findFinanceAccountForPaymentMethodTx(tx *sql.Tx, divisionID int64, paymentMethod string) (*FinanceAccount, error) {
	if divisionID <= 0 {
		var err error
		divisionID, err = divisionIDByCodeTx(tx, divisionCodeSports)
		if err != nil {
			return nil, errors.New("division is required for payment-method finance account lookup")
		}
	}
	switch normalizePaymentMethod(paymentMethod) {
	case "bank_transfer", "qr_pay":
		return findFinanceAccountByNameTx(tx, divisionID, financeAccountMainBank)
	default:
		return findFinanceAccountByNameTx(tx, divisionID, financeAccountCashInHand)
	}
}

func financeAccountBalanceTx(tx *sql.Tx, accountID int64) (float64, error) {
	var balance float64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = $1
		  AND voided_at IS NULL
		  AND approval_status = 'approved'
	`, accountID).Scan(&balance); err != nil {
		return 0, err
	}
	return normalizeMoney(balance), nil
}

func (a *App) financeAccountBalance(accountID int64) (float64, error) {
	var balance float64
	if err := a.queryRowDB(`
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = ?
		  AND voided_at IS NULL
		  AND approval_status = 'approved'
	`, accountID).Scan(&balance); err != nil {
		return 0, err
	}
	return normalizeMoney(balance), nil
}

func financeBalanceCutoffForDate(date string) (time.Time, error) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return day.AddDate(0, 0, 1).Add(-time.Nanosecond).UTC(), nil
}

func financeAccountBalanceAsOfTx(tx *sql.Tx, accountID int64, cutoff time.Time) (float64, error) {
	var balance float64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = $1
		  AND voided_at IS NULL
		  AND approval_status = 'approved'
		  AND recorded_at <= $2
	`, accountID, cutoff.UTC()).Scan(&balance); err != nil {
		return 0, err
	}
	return normalizeMoney(balance), nil
}

func financeDateNotInFuture(date time.Time) bool {
	return financeDateNotInFutureAt(date, time.Now())
}

func financeDateNotInFutureAt(date time.Time, now time.Time) bool {
	localDate := time.Date(date.In(time.Local).Year(), date.In(time.Local).Month(), date.In(time.Local).Day(), 0, 0, 0, 0, time.Local)
	today := now.In(time.Local)
	todayDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	return !localDate.After(todayDate)
}

func validateFinanceRecordedAt(recordedAt time.Time, label string) error {
	return validateFinanceRecordedAtAt(recordedAt, time.Now(), label)
}

func validateFinanceRecordedAtAt(recordedAt time.Time, now time.Time, label string) error {
	if recordedAt.IsZero() {
		return errors.New(label + " is required")
	}
	if err := validateHistoricalEntryTime(recordedAt, label); err != nil {
		return err
	}
	if !financeDateNotInFutureAt(recordedAt, now) {
		return errors.New(label + " cannot be in the future")
	}
	return nil
}

func parseFinanceRecordedAtDate(raw string, now time.Time, label string) (time.Time, error) {
	now = now.In(time.Local)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		recordedAt := now.UTC()
		if err := validateFinanceRecordedAtAt(recordedAt, now, label); err != nil {
			return time.Time{}, err
		}
		return recordedAt, nil
	}

	parsedDate, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}, errors.New("valid " + strings.ToLower(label) + " is required")
	}

	recordedAt := time.Date(
		parsedDate.Year(),
		parsedDate.Month(),
		parsedDate.Day(),
		now.Hour(),
		now.Minute(),
		now.Second(),
		0,
		time.Local,
	)
	if err := validateFinanceRecordedAtAt(recordedAt, now, label); err != nil {
		return time.Time{}, err
	}
	return recordedAt.UTC(), nil
}

func syncFinanceAccountOpeningBalanceMetadataTx(tx *sql.Tx, accountID int64) error {
	var openingBalance float64
	if err := tx.QueryRow(`
		SELECT COALESCE(amount, 0)
		FROM finance_transactions
		WHERE finance_account_id = $1
		  AND transaction_type = 'opening_balance'
		  AND voided_at IS NULL
		ORDER BY recorded_at DESC, finance_transactions.id DESC
		LIMIT 1
	`, accountID).Scan(&openingBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			openingBalance = 0
		} else {
			return err
		}
	}
	_, err := tx.Exec(`UPDATE finance_accounts SET opening_balance = $1, updated_at = $2 WHERE id = $3`, normalizeMoney(openingBalance), time.Now().UTC(), accountID)
	return err
}

func reserveFinanceOperationTx(tx *sql.Tx, scope string, userID int64, fingerprint string) (string, bool, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return "", false, errors.New("operation fingerprint is required")
	}
	now := time.Now().UTC()
	_, err := tx.Exec(`
		INSERT INTO finance_operation_keys (operation_scope, user_id, fingerprint, created_at)
		VALUES ($1, $2, $3, $4)
	`, scope, userID, fingerprint, now)
	if err == nil {
		return "", false, nil
	}
	if !isUniqueConstraintError(err) {
		return "", false, err
	}
	var existing string
	if scanErr := tx.QueryRow(`
		SELECT result_ref
		FROM finance_operation_keys
		WHERE operation_scope = $1 AND user_id = $2 AND fingerprint = $3
		LIMIT 1
	`, scope, userID, fingerprint).Scan(&existing); scanErr != nil {
		return "", false, scanErr
	}
	return existing, true, nil
}

func completeFinanceOperationTx(tx *sql.Tx, scope string, userID int64, fingerprint string, resultRef string) error {
	_, err := tx.Exec(`
		UPDATE finance_operation_keys
		SET result_ref = $1
		WHERE operation_scope = $2 AND user_id = $3 AND fingerprint = $4
	`, strings.TrimSpace(resultRef), scope, userID, strings.TrimSpace(fingerprint))
	return err
}

func financeTransferReference(now time.Time) string {
	return fmt.Sprintf("MKM-TRF-%s", now.UTC().Format("20060102150405"))
}

func financeVoucherReference(prefix string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%09d", prefix, now.UTC().Format("20060102150405"), now.UTC().Nanosecond())
}

func insertFinanceTransactionTx(tx *sql.Tx, entry financeTransactionCreate) (int64, error) {
	entry.Amount = normalizeMoney(entry.Amount)
	if entry.Amount == 0 {
		return 0, errors.New("finance transaction amount must not be zero")
	}
	if !validFinanceTransactionType(entry.TransactionType) {
		return 0, errors.New("invalid finance transaction type")
	}
	account, err := findFinanceAccountByIDQuery(tx, entry.FinanceAccountID)
	if err != nil {
		return 0, fmt.Errorf("load finance account for transaction: %w", err)
	}
	if account.DivisionID <= 0 {
		return 0, errors.New("selected finance account is missing a division")
	}
	if !account.IsActive {
		return 0, errors.New("selected finance account is inactive")
	}
	expectedMethod := financePaymentMethodForAccount(account.AccountType)
	if normalizePaymentMethod(entry.PaymentMethod) != expectedMethod {
		return 0, errors.New("payment method does not match the selected finance account")
	}
	recordedAt := entry.RecordedAt.UTC()
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}
	createdAt := time.Now().UTC()
	approvalStatus := strings.TrimSpace(entry.ApprovalStatus)
	if approvalStatus == "" {
		approvalStatus = financeApprovalApproved
	}
	if !validFinanceApprovalStatus(approvalStatus) {
		return 0, errors.New("invalid finance approval status")
	}
	approvedAt := entry.ApprovedAt.UTC()
	if approvalStatus == financeApprovalApproved && approvedAt.IsZero() {
		approvedAt = createdAt
	}
	receiptNumber := strings.TrimSpace(entry.ReceiptNumber)
	if receiptNumber == "" {
		receiptNumber = financeVoucherReference("MKM-FIN", createdAt)
	}
	referenceNumber := strings.TrimSpace(entry.ReferenceNumber)
	if referenceNumber == "" {
		referenceNumber = receiptNumber
	}
	divisionID, err := financeDivisionIDForEntryTx(tx, entry)
	if err != nil {
		return 0, err
	}
	if divisionID <= 0 {
		divisionID = account.DivisionID
	}
	if divisionID != account.DivisionID {
		return 0, errors.New("selected finance account belongs to a different division")
	}
	var transactionID int64

	err = tx.QueryRow(`
		INSERT INTO finance_transactions (
			receipt_number, reference_number, division_id, category, approval_status, transaction_type,
			reference_type, reference_id, source_type, source_id, finance_account_id,
			transfer_group_id, person_name, description, notes, payment_method, amount,
			recorded_by_user_id, approved_by_user_id, recorded_at, created_at, updated_at, approved_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23
		)
		RETURNING id
	`,
		receiptNumber,
		referenceNumber,
		nullIfZero(divisionID),
		strings.TrimSpace(entry.Category),
		approvalStatus,
		entry.TransactionType,
		strings.TrimSpace(entry.ReferenceType),
		nullIfZero(entry.ReferenceID),
		strings.TrimSpace(entry.SourceType),
		nullIfZero(entry.SourceID),
		entry.FinanceAccountID,
		strings.TrimSpace(entry.TransferGroupID),
		truncateString(strings.TrimSpace(entry.PersonName), 120),
		truncateString(strings.TrimSpace(entry.Description), 300),
		truncateString(strings.TrimSpace(entry.Notes), 400),
		expectedMethod,
		entry.Amount,
		nullableExistingUserIDTx(tx, entry.RecordedByUserID),
		nullableExistingUserIDTx(tx, entry.ApprovedByUserID),
		recordedAt,
		createdAt,
		createdAt,
		nullIfZeroTime(approvedAt),
	).Scan(&transactionID)
	if err != nil {
		return 0, fmt.Errorf("insert finance transaction: %w", err)
	}

	return transactionID, nil
}

func voidFinanceTransactionTx(tx *sql.Tx, transactionID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	var alreadyVoided int
	var recordedAt time.Time
	if err := tx.QueryRow(`SELECT CASE WHEN voided_at IS NULL THEN 0 ELSE 1 END, recorded_at FROM finance_transactions WHERE id = $1`, transactionID).Scan(&alreadyVoided, &recordedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("finance transaction not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("finance transaction has already been voided")
	}
	if err := ensureFinanceDateUnlockedTx(tx, recordedAt, "transaction date"); err != nil {
		return err
	}
	_, err := tx.Exec(`
		UPDATE finance_transactions
		SET voided_at = $1, voided_by_user_id = $2, void_reason = $3, updated_at = $4
		WHERE id = $5 AND voided_at IS NULL
	`, time.Now().UTC(), nullableExistingUserIDTx(tx, voidedByUserID), reason, time.Now().UTC(), transactionID)
	return err
}

func financeVoidWorkflowMessage(transaction *FinanceTransaction) string {
	switch transaction.SourceType {
	case "booking_payment_collection":
		return "Void booking income from the booking payment workflow so the booking balance stays synchronized."
	case "admission":
		return "Void admission income from the admission payment workflow so the admission record stays synchronized."
	case "student_enrollment":
		return "Void registration income from the enrollment workflow so the enrollment fee state stays synchronized."
	case "student_monthly_payment":
		return "Void monthly income from the student payment workflow so the monthly payment record stays synchronized."
	case "booking_referral_payment":
		return "Void referral payouts from the referral payment workflow so the payout record stays synchronized."
	case "mcp_payment_collection":
		return "Void MCP income from the MCP receivables workflow so the plan balance stays synchronized."
	case "tournament_sponsorship", "tournament_official_payment", "tournament_expense":
		return "Void this tournament entry from its tournament finance workflow so the tournament totals stay synchronized."
	case "payroll_payment":
		return "Void payroll payments from the payroll workflow so payment status stays synchronized."
	case "finance_transfer":
		return "Void transfers from the transfer workflow so both sides of the transfer are reversed together."
	default:
		return "This finance entry must be voided from its source workflow."
	}
}

func financeTransactionAllowsGeneralVoid(transaction *FinanceTransaction) bool {
	switch transaction.SourceType {
	case "", "manual", "finance_adjustment", "finance_account_opening_balance":
		return transaction.TransferGroupID == ""
	default:
		return false
	}
}

func financeTransactionSourceExistsQuery(queryer sqlQueryer, transaction *FinanceTransaction) (bool, error) {
	if transaction == nil {
		return false, errors.New("finance transaction is required")
	}
	switch transaction.SourceType {
	case "admission":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM admissions WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "student_enrollment":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM student_enrollments WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "student_monthly_payment":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM student_monthly_payments WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "booking_payment_collection":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM booking_payment_collections WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "booking_referral_payment":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM booking_referrals WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "mcp_payment_collection":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM mcp_payment_collections WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "tournament":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM tournaments WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "tournament_sponsorship":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM tournament_sponsorships WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "tournament_official_payment":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM tournament_official_payments WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "tournament_expense":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM tournament_expenses WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	case "payroll_payment":
		var count int
		if err := queryer.QueryRow(`SELECT COUNT(*) FROM payroll_payments WHERE id = $1`, transaction.SourceID).Scan(&count); err != nil {
			return false, err
		}
		return count > 0, nil
	default:
		return true, nil
	}
}

func financeTransactionRepairableOrphan(transaction *FinanceTransaction) bool {
	switch transaction.SourceType {
	case "admission", "student_enrollment", "student_monthly_payment":
		return true
	default:
		return false
	}
}

func financeTransactionNeedsLedgerRepairQuery(queryer sqlQueryer, transaction *FinanceTransaction) (bool, error) {
	if transaction == nil {
		return false, errors.New("finance transaction is required")
	}
	switch transaction.SourceType {
	case "admission":
		var count int
		if err := queryer.QueryRow(`
			SELECT COUNT(*)
			FROM admissions
			WHERE id = $1
			  AND payment_collected = 1
			  AND COALESCE(finance_transaction_id, 0) = $2
		`, transaction.SourceID, transaction.ID).Scan(&count); err != nil {
			return false, err
		}
		return count == 0, nil
	case "student_enrollment":
		var count int
		if err := queryer.QueryRow(`
			SELECT COUNT(*)
			FROM student_enrollments
			WHERE id = $1
			  AND payment_collected = 1
			  AND COALESCE(finance_transaction_id, 0) = $2
		`, transaction.SourceID, transaction.ID).Scan(&count); err != nil {
			return false, err
		}
		return count == 0, nil
	case "student_monthly_payment":
		var count int
		if err := queryer.QueryRow(`
			SELECT COUNT(*)
			FROM student_monthly_payments
			WHERE id = $1
			  AND voided = 0
			  AND finance_transaction_id = $2
		`, transaction.SourceID, transaction.ID).Scan(&count); err != nil {
			return false, err
		}
		return count == 0, nil
	default:
		return false, nil
	}
}

func populateFinanceTransactionVoidStates(ctx context.Context, db *sql.DB, transactions []FinanceTransaction) error {
	if len(transactions) == 0 {
		return nil
	}

	admissionIDs := make([]int64, 0)
	enrollmentIDs := make([]int64, 0)
	monthlyPaymentIDs := make([]int64, 0)
	admissionSeen := make(map[int64]bool)
	enrollmentSeen := make(map[int64]bool)
	monthlySeen := make(map[int64]bool)

	for i := range transactions {
		transactions[i].GeneralVoidAllowed = financeTransactionAllowsGeneralVoid(&transactions[i])
		transactions[i].OrphanedSource = false
		if transactions[i].GeneralVoidAllowed || transactions[i].Voided {
			continue
		}
		switch transactions[i].SourceType {
		case "admission":
			if !admissionSeen[transactions[i].SourceID] {
				admissionSeen[transactions[i].SourceID] = true
				admissionIDs = append(admissionIDs, transactions[i].SourceID)
			}
		case "student_enrollment":
			if !enrollmentSeen[transactions[i].SourceID] {
				enrollmentSeen[transactions[i].SourceID] = true
				enrollmentIDs = append(enrollmentIDs, transactions[i].SourceID)
			}
		case "student_monthly_payment":
			if !monthlySeen[transactions[i].SourceID] {
				monthlySeen[transactions[i].SourceID] = true
				monthlyPaymentIDs = append(monthlyPaymentIDs, transactions[i].SourceID)
			}
		}
	}

	admissionState, err := loadAdmissionLedgerRepairState(ctx, db, admissionIDs)
	if err != nil {
		return err
	}
	enrollmentState, err := loadEnrollmentLedgerRepairState(ctx, db, enrollmentIDs)
	if err != nil {
		return err
	}
	monthlyState, err := loadStudentPaymentLedgerRepairState(ctx, db, monthlyPaymentIDs)
	if err != nil {
		return err
	}

	for i := range transactions {
		if transactions[i].GeneralVoidAllowed || transactions[i].Voided || !financeTransactionRepairableOrphan(&transactions[i]) {
			continue
		}
		needsRepair := false
		switch transactions[i].SourceType {
		case "admission":
			state, ok := admissionState[transactions[i].SourceID]
			needsRepair = !ok || !state.PaymentCollected || state.FinanceTransactionID != transactions[i].ID
		case "student_enrollment":
			state, ok := enrollmentState[transactions[i].SourceID]
			needsRepair = !ok || !state.PaymentCollected || state.FinanceTransactionID != transactions[i].ID
		case "student_monthly_payment":
			state, ok := monthlyState[transactions[i].SourceID]
			needsRepair = !ok || state.Voided || state.FinanceTransactionID != transactions[i].ID
		}
		transactions[i].OrphanedSource = needsRepair
		if needsRepair {
			transactions[i].GeneralVoidAllowed = true
		}
	}
	return nil
}

type admissionLedgerRepairState struct {
	FinanceTransactionID int64
	PaymentCollected     bool
}

func loadAdmissionLedgerRepairState(ctx context.Context, db *sql.DB, admissionIDs []int64) (map[int64]admissionLedgerRepairState, error) {
	state := make(map[int64]admissionLedgerRepairState, len(admissionIDs))
	if len(admissionIDs) == 0 {
		return state, nil
	}
	query, args := int64INClause(`
		SELECT id, COALESCE(finance_transaction_id, 0), COALESCE(payment_collected, 0)
		FROM admissions
		WHERE id IN (%s)
	`, admissionIDs)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                   int64
			financeTransactionID int64
			paymentCollected     int
		)
		if err := rows.Scan(&id, &financeTransactionID, &paymentCollected); err != nil {
			return nil, err
		}
		state[id] = admissionLedgerRepairState{
			FinanceTransactionID: financeTransactionID,
			PaymentCollected:     paymentCollected == 1,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return state, nil
}

type enrollmentLedgerRepairState struct {
	FinanceTransactionID int64
	PaymentCollected     bool
}

func loadEnrollmentLedgerRepairState(ctx context.Context, db *sql.DB, enrollmentIDs []int64) (map[int64]enrollmentLedgerRepairState, error) {
	state := make(map[int64]enrollmentLedgerRepairState, len(enrollmentIDs))
	if len(enrollmentIDs) == 0 {
		return state, nil
	}
	query, args := int64INClause(`
		SELECT id, COALESCE(finance_transaction_id, 0), COALESCE(payment_collected, 0)
		FROM student_enrollments
		WHERE id IN (%s)
	`, enrollmentIDs)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                   int64
			financeTransactionID int64
			paymentCollected     int
		)
		if err := rows.Scan(&id, &financeTransactionID, &paymentCollected); err != nil {
			return nil, err
		}
		state[id] = enrollmentLedgerRepairState{
			FinanceTransactionID: financeTransactionID,
			PaymentCollected:     paymentCollected == 1,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return state, nil
}

type studentPaymentLedgerRepairState struct {
	FinanceTransactionID int64
	Voided               bool
}

func loadStudentPaymentLedgerRepairState(ctx context.Context, db *sql.DB, paymentIDs []int64) (map[int64]studentPaymentLedgerRepairState, error) {
	state := make(map[int64]studentPaymentLedgerRepairState, len(paymentIDs))
	if len(paymentIDs) == 0 {
		return state, nil
	}
	query, args := int64INClause(`
		SELECT id, COALESCE(finance_transaction_id, 0), COALESCE(voided, 0)
		FROM student_monthly_payments
		WHERE id IN (%s)
	`, paymentIDs)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                   int64
			financeTransactionID int64
			voided               int
		)
		if err := rows.Scan(&id, &financeTransactionID, &voided); err != nil {
			return nil, err
		}
		state[id] = studentPaymentLedgerRepairState{
			FinanceTransactionID: financeTransactionID,
			Voided:               voided == 1,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return state, nil
}

func int64INClause(template string, values []int64) (string, []any) {
	placeholders := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for i, value := range values {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, value)
	}
	return fmt.Sprintf(template, strings.Join(placeholders, ",")), args
}

func syncLegacyAdmissionPaymentVoidStateTx(tx *sql.Tx, admissionID int64, financeTransactionID int64, reason string, voidedByUserID int64, now time.Time) error {
	if admissionID <= 0 {
		return nil
	}
	if financeTransactionID > 0 {
		if _, err := tx.Exec(`
			UPDATE admissions
			SET payment_collected = 0,
			    payment_collected_at = NULL,
			    admission_payment_amount = 0,
			    finance_transaction_id = NULL,
			    payment_void_reason = $1,
			    payment_voided_by_user_id = $2,
			    payment_voided_at = $3,
			    updated_at = $4
			WHERE id = $5
			  AND COALESCE(finance_transaction_id, 0) = $6
		`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, now, admissionID, financeTransactionID); err != nil {
			return err
		}
		return nil
	}
	_, err := tx.Exec(`
		UPDATE admissions
		SET payment_void_reason = CASE
				WHEN COALESCE(payment_collected, 0) = 1 OR payment_voided_at IS NOT NULL THEN $1
				ELSE payment_void_reason
			END,
		    payment_voided_by_user_id = CASE
				WHEN COALESCE(payment_collected, 0) = 1 OR payment_voided_at IS NOT NULL THEN $2
				ELSE payment_voided_by_user_id
			END,
		    payment_voided_at = CASE
				WHEN COALESCE(payment_collected, 0) = 1 OR payment_voided_at IS NOT NULL THEN $3
				ELSE payment_voided_at
			END,
		    updated_at = CASE
				WHEN COALESCE(payment_collected, 0) = 1 OR payment_voided_at IS NOT NULL THEN $4
				ELSE updated_at
			END
		WHERE id = $5
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, now, admissionID)
	return err
}

func resolveEnrollmentAdmissionPaymentByAdmissionTx(tx *sql.Tx, driver DatabaseDriver, admissionID int64) (*StudentEnrollment, error) {
	var legacyFinanceTransactionID int64
	if err := tx.QueryRow(`
		SELECT COALESCE(finance_transaction_id, 0)
		FROM admissions
		WHERE id = $1
	`, admissionID).Scan(&legacyFinanceTransactionID); err != nil {
		return nil, err
	}

	if legacyFinanceTransactionID > 0 {
		var enrollmentID int64
		err := tx.QueryRow(`
			SELECT se.id
			FROM student_enrollments se
			WHERE se.admission_id = $1
			  AND COALESCE(se.payment_collected, 0) = 1
			  AND COALESCE(se.finance_transaction_id, 0) = $2
			ORDER BY se.id
			LIMIT 1
		`, admissionID, legacyFinanceTransactionID).Scan(&enrollmentID)
		if err == nil {
			return findStudentEnrollmentByIDTx(tx, driver, enrollmentID)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	rows, err := tx.Query(`
		SELECT se.id
		FROM student_enrollments se
		WHERE se.admission_id = $1
		  AND COALESCE(se.payment_collected, 0) = 1
		ORDER BY se.id
	`, admissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matchedEnrollmentID int64
	matchCount := 0
	for rows.Next() {
		var enrollmentID int64
		if err := rows.Scan(&enrollmentID); err != nil {
			return nil, err
		}
		matchedEnrollmentID = enrollmentID
		matchCount++
		if matchCount > 1 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if matchCount == 0 {
		return nil, sql.ErrNoRows
	}
	if matchCount > 1 {
		return nil, errors.New("multiple enrollment registration payments are linked to this student; void from the enrollment workflow")
	}
	return findStudentEnrollmentByIDTx(tx, driver, matchedEnrollmentID)
}

func voidStudentEnrollmentAdmissionPaymentTx(tx *sql.Tx, enrollment *StudentEnrollment, reason string, voidedByUserID int64) error {
	if enrollment == nil {
		return errors.New("enrollment payment was not found")
	}
	if !enrollment.AdmissionPaymentPaid {
		var priorVoidCount int
		if err := tx.QueryRow(`
			SELECT COUNT(*)
			FROM finance_transactions
			WHERE source_type = 'student_enrollment'
			  AND source_id = $1
			  AND category = 'admission_payment'
			  AND voided_at IS NOT NULL
		`, enrollment.ID).Scan(&priorVoidCount); err != nil {
			return err
		}
		if priorVoidCount > 0 {
			return errors.New("enrollment payment has already been voided")
		}
		return errors.New("enrollment payment has not been collected")
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE student_enrollments
		SET payment_collected = 0,
		    payment_collected_at = NULL,
		    admission_payment_amount = 0,
		    finance_transaction_id = NULL,
		    updated_at = $1
		WHERE id = $2 AND payment_collected = 1
	`, now, enrollment.ID); err != nil {
		return err
	}
	if enrollment.FinanceTransactionID > 0 {
		if err := voidFinanceTransactionTx(tx, enrollment.FinanceTransactionID, reason, voidedByUserID); err != nil {
			return err
		}
	}
	return syncLegacyAdmissionPaymentVoidStateTx(tx, enrollment.AdmissionID, enrollment.FinanceTransactionID, reason, voidedByUserID, now)
}

func (a *App) voidEnrollmentAdmissionPayment(enrollmentID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, a.runtimeConfig.DBDriver, enrollmentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("enrollment payment was not found")
		}
		return err
	}
	if err := voidStudentEnrollmentAdmissionPaymentTx(tx, enrollment, reason, voidedByUserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) voidAdmissionPayment(admissionID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	enrollment, err := resolveEnrollmentAdmissionPaymentByAdmissionTx(tx, a.runtimeConfig.DBDriver, admissionID)
	if err == nil {
		if err := voidStudentEnrollmentAdmissionPaymentTx(tx, enrollment, reason, voidedByUserID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var financeTransactionID int64
	var paymentCollected int
	var paymentVoidedAt sql.NullTime
	if err := tx.QueryRow(`
		SELECT COALESCE(finance_transaction_id, 0), COALESCE(payment_collected, 0), payment_voided_at
		FROM admissions
		WHERE id = $1
	`, admissionID).Scan(&financeTransactionID, &paymentCollected, &paymentVoidedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("admission payment was not found")
		}
		return err
	}
	if paymentCollected == 0 {
		if paymentVoidedAt.Valid {
			return errors.New("admission payment has already been voided")
		}
		return errors.New("admission payment has not been collected")
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE admissions
		SET payment_collected = 0,
		    payment_collected_at = NULL,
		    admission_payment_amount = 0,
		    finance_transaction_id = NULL,
		    payment_void_reason = $1,
		    payment_voided_by_user_id = $2,
		    payment_voided_at = $3,
		    updated_at = $4
		WHERE id = $5 AND payment_collected = 1
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, now, admissionID); err != nil {
		return err
	}
	if financeTransactionID > 0 {
		if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, voidedByUserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) voidStudentMonthlyPayment(paymentID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if paymentID <= 0 {
		return errors.New("student payment was not found")
	}
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var financeTransactionID int64
	var alreadyVoided int
	if err := tx.QueryRow(`
		SELECT finance_transaction_id, voided
		FROM student_monthly_payments
		WHERE id = $1
	`, paymentID).Scan(&financeTransactionID, &alreadyVoided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("student payment was not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("student payment has already been voided")
	}
	if financeTransactionID <= 0 {
		return errors.New("student payment has no linked finance transaction")
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`
		UPDATE student_monthly_payments
		SET voided = 1, void_reason = $1, voided_by_user_id = $2, voided_at = $3
		WHERE id = $4 AND voided = 0
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, paymentID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("student payment has already been voided")
	}
	if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, voidedByUserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) voidReferralCommissionPayment(referralID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var financeTransactionID int64
	var paid int
	var voidedAt sql.NullTime
	if err := tx.QueryRow(`
		SELECT COALESCE(finance_transaction_id, 0), paid, voided_at
		FROM booking_referrals
		WHERE id = $1
	`, referralID).Scan(&financeTransactionID, &paid, &voidedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("referral payment was not found")
		}
		return err
	}
	if paid == 0 {
		if voidedAt.Valid {
			return errors.New("referral payment has already been voided")
		}
		return errors.New("referral commission has not been paid")
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE booking_referrals
		SET paid = 0,
		    paid_at = NULL,
		    payment_method = '',
		    finance_transaction_id = NULL,
		    void_reason = $1,
		    voided_by_user_id = $2,
		    voided_at = $3
		WHERE id = $4 AND paid = 1
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, referralID); err != nil {
		return err
	}
	if financeTransactionID > 0 {
		if err := voidFinanceTransactionTx(tx, financeTransactionID, reason, voidedByUserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) voidCashReconciliation(reconciliationID int64, reason string, voidedByUserID int64, supersededByID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var alreadyVoided int
	var reconciliationDate string
	if err := tx.QueryRow(`SELECT CASE WHEN voided_at IS NULL THEN 0 ELSE 1 END, reconciliation_date FROM cash_reconciliations WHERE id = $1`, reconciliationID).Scan(&alreadyVoided, &reconciliationDate); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("cash reconciliation was not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("cash reconciliation has already been voided")
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(reconciliationDate), time.Local)
	if err != nil {
		return errors.New("cash reconciliation date is invalid")
	}
	if err := ensureFinanceDateUnlockedTx(tx, day, "reconciliation date"); err != nil {
		return err
	}
	_, err = tx.Exec(`
		UPDATE cash_reconciliations
		SET void_reason = $1, voided_by_user_id = $2, voided_at = $3, superseded_by_reconciliation_id = $4
		WHERE id = $5 AND voided_at IS NULL
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), time.Now().UTC(), nullIfZero(supersededByID), reconciliationID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) createManualFinanceTransactionForAccount(category, personName, description, notes string, accountID int64, amount float64, recordedAt time.Time, recordedByUserID int64) (int64, error) {
	return a.createManualFinanceTransactionForAccountWithApproval(category, personName, description, notes, accountID, amount, recordedAt, recordedByUserID, financeApprovalApproved)
}

func (a *App) createManualFinanceTransactionForAccountWithApproval(category, personName, description, notes string, accountID int64, amount float64, recordedAt time.Time, recordedByUserID int64, approvalStatus string) (int64, error) {
	return a.createManualFinanceTransactionForAccountWithApprovalInDivision(category, personName, description, notes, accountID, amount, 0, recordedAt, recordedByUserID, approvalStatus)
}

func (a *App) createManualFinanceTransactionForAccountWithApprovalInDivision(category, personName, description, notes string, accountID int64, amount float64, divisionID int64, recordedAt time.Time, recordedByUserID int64, approvalStatus string) (int64, error) {
	if err := validateFinanceRecordedAt(recordedAt, "transaction date"); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "transaction date"); err != nil {
		return 0, err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, fmt.Errorf("load finance account for manual transaction: %w", err)
	}
	if divisionID <= 0 {
		divisionID = account.DivisionID
	}
	transactionType := financeTxnTypeIncome
	if amount < 0 {
		transactionType = financeTxnTypeExpense
	}
	prefix := "MKM-INC"
	if amount < 0 {
		prefix = "MKM-EXP"
	}
	fingerprint := fmt.Sprintf("%d|%s|%d|%.2f|%s|%s|%s|%s", recordedByUserID, category, account.ID, normalizeMoney(amount), recordedAt.In(time.Local).Format("2006-01-02"), strings.TrimSpace(personName), strings.TrimSpace(description), strings.TrimSpace(notes))
	if resultRef, duplicate, err := reserveFinanceOperationTx(tx, "manual_finance_transaction", recordedByUserID, fingerprint); err != nil {
		return 0, err
	} else if duplicate {
		transactionID := parseInt64Query(resultRef)
		if transactionID > 0 {
			return transactionID, tx.Commit()
		}
		return 0, errors.New("this finance entry was already recorded")
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference(prefix, recordedAt),
		ReferenceNumber:  financeVoucherReference(prefix, recordedAt),
		DivisionID:       divisionID,
		Category:         category,
		ApprovalStatus:   approvalStatus,
		TransactionType:  transactionType,
		ReferenceType:    "manual",
		SourceType:       "manual",
		FinanceAccountID: account.ID,
		PersonName:       personName,
		Description:      description,
		Notes:            notes,
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return 0, err
	}
	if err := completeFinanceOperationTx(tx, "manual_finance_transaction", recordedByUserID, fingerprint, strconv.FormatInt(transactionID, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) createManualFinanceTransaction(category, personName, description, paymentMethod string, amount float64, recordedAt time.Time, recordedByUserID int64) (int64, error) {
	return a.createManualFinanceTransactionInDivision(category, personName, description, paymentMethod, amount, 0, recordedAt, recordedByUserID)
}

func (a *App) createManualFinanceTransactionInDivision(category, personName, description, paymentMethod string, amount float64, divisionID int64, recordedAt time.Time, recordedByUserID int64) (int64, error) {
	if err := validateFinanceRecordedAt(recordedAt, "transaction date"); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "transaction date"); err != nil {
		return 0, err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if divisionID <= 0 {
		divisionID, err = divisionIDByCodeTx(tx, divisionCodeSports)
		if err != nil {
			return 0, err
		}
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	transactionType := financeTxnTypeIncome
	if amount < 0 {
		transactionType = financeTxnTypeExpense
	}
	prefix := "MKM-INC"
	if amount < 0 {
		prefix = "MKM-EXP"
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference(prefix, recordedAt),
		ReferenceNumber:  financeVoucherReference(prefix, recordedAt),
		DivisionID:       divisionID,
		Category:         category,
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  transactionType,
		ReferenceType:    "manual",
		SourceType:       "manual",
		FinanceAccountID: account.ID,
		PersonName:       personName,
		Description:      description,
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) createFinanceTransfer(fromAccountID, toAccountID int64, amount float64, transferDate time.Time, referenceNumber, description, notes string, recordedByUserID int64) (string, error) {
	if fromAccountID <= 0 || toAccountID <= 0 {
		return "", errors.New("both transfer accounts are required")
	}
	if fromAccountID == toAccountID {
		return "", errors.New("transfer accounts must differ")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return "", errors.New("transfer amount must be greater than zero")
	}
	if err := validateFinanceRecordedAt(transferDate, "transfer date"); err != nil {
		return "", err
	}
	if err := a.ensureFinanceDateUnlocked(transferDate, "transfer date"); err != nil {
		return "", err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	fromAccount, err := findFinanceAccountByIDQuery(tx, fromAccountID)
	if err != nil {
		return "", err
	}
	toAccount, err := findFinanceAccountByIDQuery(tx, toAccountID)
	if err != nil {
		return "", err
	}
	if !fromAccount.IsActive || !toAccount.IsActive {
		return "", errors.New("transfer accounts must be active")
	}
	if fromAccount.DivisionID <= 0 || toAccount.DivisionID <= 0 {
		return "", errors.New("transfer accounts must belong to a division")
	}
	if fromAccount.DivisionID != toAccount.DivisionID {
		return "", errors.New("transfers across divisions are not allowed")
	}
	fingerprint := fmt.Sprintf("%d|%d|%d|%.2f|%s|%s|%s|%s", recordedByUserID, fromAccount.ID, toAccount.ID, amount, transferDate.In(time.Local).Format("2006-01-02"), strings.TrimSpace(referenceNumber), strings.TrimSpace(description), strings.TrimSpace(notes))
	if resultRef, duplicate, err := reserveFinanceOperationTx(tx, "finance_transfer", recordedByUserID, fingerprint); err != nil {
		return "", err
	} else if duplicate {
		if strings.TrimSpace(resultRef) != "" {
			return strings.TrimSpace(resultRef), tx.Commit()
		}
		return "", errors.New("this transfer was already recorded")
	}
	fromBalance, err := financeAccountBalanceTx(tx, fromAccount.ID)
	if err != nil {
		return "", err
	}
	if fromBalance+0.004 < amount {
		return "", errors.New("transfer amount exceeds the available balance in the source account")
	}
	if transferDate.IsZero() {
		transferDate = time.Now().UTC()
	}
	now := time.Now().UTC()
	groupID := fmt.Sprintf("TRF-%s-%d", now.Format("20060102150405"), now.UnixNano())
	referenceNumber = strings.TrimSpace(referenceNumber)
	if referenceNumber == "" {
		referenceNumber = financeTransferReference(now)
	}
	outReceipt := referenceNumber + "-OUT"
	inReceipt := referenceNumber + "-IN"
	if _, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    outReceipt,
		ReferenceNumber:  referenceNumber,
		DivisionID:       fromAccount.DivisionID,
		Category:         "internal_transfer",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeTransferOut,
		ReferenceType:    "finance_transfer",
		SourceType:       "finance_transfer",
		FinanceAccountID: fromAccount.ID,
		TransferGroupID:  groupID,
		PersonName:       toAccount.Name,
		Description:      strings.TrimSpace(description),
		Notes:            notes,
		PaymentMethod:    financePaymentMethodForAccount(fromAccount.AccountType),
		Amount:           -amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       transferDate,
		ApprovedAt:       transferDate,
	}); err != nil {
		return "", err
	}
	if _, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    inReceipt,
		ReferenceNumber:  referenceNumber,
		DivisionID:       toAccount.DivisionID,
		Category:         "internal_transfer",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeTransferIn,
		ReferenceType:    "finance_transfer",
		SourceType:       "finance_transfer",
		FinanceAccountID: toAccount.ID,
		TransferGroupID:  groupID,
		PersonName:       fromAccount.Name,
		Description:      strings.TrimSpace(description),
		Notes:            notes,
		PaymentMethod:    financePaymentMethodForAccount(toAccount.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       transferDate,
		ApprovedAt:       transferDate,
	}); err != nil {
		return "", err
	}
	if err := completeFinanceOperationTx(tx, "finance_transfer", recordedByUserID, fingerprint, groupID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return groupID, nil
}

func (a *App) voidFinanceTransferGroup(groupID string, reason string, voidedByUserID int64) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return errors.New("transfer group is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`
		SELECT id, transaction_type, amount
		FROM finance_transactions
		WHERE transfer_group_id = $1
		ORDER BY id ASC
	`, groupID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	var types []string
	var amounts []float64
	for rows.Next() {
		var id int64
		var transactionType string
		var amount float64
		if err := rows.Scan(&id, &transactionType, &amount); err != nil {
			return err
		}
		ids = append(ids, id)
		types = append(types, transactionType)
		amounts = append(amounts, amount)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) != 2 {
		return errors.New("transfer pair could not be resolved")
	}
	if !(types[0] == financeTxnTypeTransferOut && types[1] == financeTxnTypeTransferIn || types[0] == financeTxnTypeTransferIn && types[1] == financeTxnTypeTransferOut) {
		return errors.New("transfer pair is invalid")
	}
	if !moneyEquals(amounts[0], -amounts[1]) {
		return errors.New("transfer pair amounts do not match")
	}
	for _, id := range ids {
		if err := voidFinanceTransactionTx(tx, id, reason, voidedByUserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) listFinanceTransfers() ([]FinanceTransfer, error) {
	rows, err := a.queryDB(`
		SELECT
			ft.transfer_group_id AS transfer_group_id,
			ft.reference_number AS reference_number,
			MAX(CASE WHEN ft.transaction_type = 'transfer_out' THEN ft.finance_account_id ELSE 0 END) AS from_account_id,
			MAX(CASE WHEN ft.transaction_type = 'transfer_out' THEN COALESCE(fa.name, '') ELSE '' END) AS from_account_name,
			MAX(CASE WHEN ft.transaction_type = 'transfer_in' THEN ft.finance_account_id ELSE 0 END) AS to_account_id,
			MAX(CASE WHEN ft.transaction_type = 'transfer_in' THEN COALESCE(fa.name, '') ELSE '' END) AS to_account_name,
			MAX(ABS(ft.amount)) AS transfer_amount,
			MIN(ft.recorded_at) AS transfer_date,
			MAX(ft.description) AS transfer_description,
			MAX(ft.notes) AS transfer_notes,
			MAX(COALESCE(ft.recorded_by_user_id, 0)) AS recorded_by_user_id,
			MAX(COALESCE(u.name, '')) AS recorded_by_user_name,
			MAX(CASE WHEN ft.voided_at IS NULL THEN 0 ELSE 1 END) AS voided_flag,
			MAX(ft.voided_at) AS voided_at,
			MAX(COALESCE(ft.void_reason, '')) AS void_reason,
			MAX(COALESCE(ft.voided_by_user_id, 0)) AS voided_by_user_id,
			MIN(ft.created_at) AS created_at,
			MAX(CASE WHEN ft.transaction_type = 'transfer_out' THEN ft.id ELSE 0 END) AS transfer_out_id,
			MAX(CASE WHEN ft.transaction_type = 'transfer_in' THEN ft.id ELSE 0 END) AS transfer_in_id
		FROM finance_transactions ft
		LEFT JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		LEFT JOIN users u ON u.id = ft.recorded_by_user_id
		WHERE ft.transfer_group_id <> ''
		GROUP BY ft.transfer_group_id, ft.reference_number
		ORDER BY MIN(ft.recorded_at) DESC, ft.transfer_group_id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []FinanceTransfer
	for rows.Next() {
		var transfer FinanceTransfer
		var voided int
		var voidedAt sql.NullTime
		var transferDateRaw string
		var createdAtRaw string
		if err := rows.Scan(
			&transfer.GroupID,
			&transfer.ReferenceNumber,
			&transfer.FromAccountID,
			&transfer.FromAccountName,
			&transfer.ToAccountID,
			&transfer.ToAccountName,
			&transfer.Amount,
			&transferDateRaw,
			&transfer.Description,
			&transfer.Notes,
			&transfer.RecordedByUserID,
			&transfer.RecordedByUserName,
			&voided,
			&voidedAt,
			&transfer.VoidReason,
			&transfer.VoidedByUserID,
			&createdAtRaw,
			&transfer.TransferOutID,
			&transfer.TransferInID,
		); err != nil {
			return nil, err
		}
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(transferDateRaw)); err == nil {
			transfer.TransferDate = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", strings.TrimSpace(transferDateRaw)); err == nil {
			transfer.TransferDate = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(transferDateRaw)); err == nil {
			transfer.TransferDate = parsed
		}
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(createdAtRaw)); err == nil {
			transfer.CreatedAt = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", strings.TrimSpace(createdAtRaw)); err == nil {
			transfer.CreatedAt = parsed
		} else if parsed, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(createdAtRaw)); err == nil {
			transfer.CreatedAt = parsed
		}
		transfer.Voided = voided == 1
		if voidedAt.Valid {
			transfer.VoidedAt = voidedAt.Time
		}
		transfers = append(transfers, transfer)
	}
	return transfers, rows.Err()
}

func (a *App) createFinanceOpeningBalance(accountID int64, amount float64, recordedAt time.Time, notes string, recordedByUserID int64) (int64, error) {
	amount = normalizeMoney(amount)
	if amount == 0 {
		return 0, errors.New("opening balance amount must not be zero")
	}
	if err := validateFinanceRecordedAt(recordedAt, "opening balance date"); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "opening balance date"); err != nil {
		return 0, err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, fmt.Errorf("load finance account for opening balance: %w", err)
	}
	var existingCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM finance_transactions
		WHERE finance_account_id = $1
		  AND transaction_type = 'opening_balance'
		  AND voided_at IS NULL
	`, accountID).Scan(&existingCount); err != nil {
		return 0, err
	}
	if existingCount > 0 {
		return 0, errors.New("an opening balance already exists for this account")
	}
	fingerprint := fmt.Sprintf("%d|%d|%.2f|%s|%s", recordedByUserID, account.ID, amount, recordedAt.In(time.Local).Format("2006-01-02"), strings.TrimSpace(notes))
	if resultRef, duplicate, err := reserveFinanceOperationTx(tx, "finance_opening_balance", recordedByUserID, fingerprint); err != nil {
		return 0, err
	} else if duplicate {
		transactionID := parseInt64Query(resultRef)
		if transactionID > 0 {
			return transactionID, tx.Commit()
		}
		return 0, errors.New("this opening balance was already recorded")
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference("MKM-OPEN", recordedAt),
		ReferenceNumber:  financeVoucherReference("MKM-OPEN", recordedAt),
		DivisionID:       account.DivisionID,
		Category:         "opening_balance",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeOpeningBalance,
		ReferenceType:    "finance_account",
		ReferenceID:      account.ID,
		SourceType:       "finance_account_opening_balance",
		SourceID:         account.ID,
		FinanceAccountID: account.ID,
		PersonName:       account.Name,
		Description:      "Opening balance for " + account.Name,
		Notes:            notes,
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return 0, err
	}
	if err := syncFinanceAccountOpeningBalanceMetadataTx(tx, account.ID); err != nil {
		return 0, err
	}
	if err := completeFinanceOperationTx(tx, "finance_opening_balance", recordedByUserID, fingerprint, strconv.FormatInt(transactionID, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func (a *App) createFinanceAdjustment(accountID int64, amount float64, recordedAt time.Time, reason string, recordedByUserID int64) (int64, error) {
	amount = normalizeMoney(amount)
	reason = strings.TrimSpace(reason)
	if amount == 0 {
		return 0, errors.New("adjustment amount must not be zero")
	}
	if reason == "" {
		return 0, errors.New("adjustment reason is required")
	}
	if err := validateFinanceRecordedAt(recordedAt, "adjustment date"); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "adjustment date"); err != nil {
		return 0, err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, err
	}
	fingerprint := fmt.Sprintf("%d|%d|%.2f|%s|%s", recordedByUserID, account.ID, amount, recordedAt.In(time.Local).Format("2006-01-02"), reason)
	if resultRef, duplicate, err := reserveFinanceOperationTx(tx, "finance_adjustment", recordedByUserID, fingerprint); err != nil {
		return 0, err
	} else if duplicate {
		transactionID := parseInt64Query(resultRef)
		if transactionID > 0 {
			return transactionID, tx.Commit()
		}
		return 0, errors.New("this adjustment was already recorded")
	}
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference("MKM-ADJ", recordedAt),
		ReferenceNumber:  financeVoucherReference("MKM-ADJ", recordedAt),
		DivisionID:       account.DivisionID,
		Category:         "cash_adjustment",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeAdjustment,
		ReferenceType:    "finance_account",
		ReferenceID:      account.ID,
		SourceType:       "finance_adjustment",
		SourceID:         account.ID,
		FinanceAccountID: account.ID,
		PersonName:       account.Name,
		Description:      "Adjustment for " + account.Name,
		Notes:            reason,
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return 0, err
	}
	if err := completeFinanceOperationTx(tx, "finance_adjustment", recordedByUserID, fingerprint, strconv.FormatInt(transactionID, 10)); err != nil {
		return 0, err
	}
	return transactionID, tx.Commit()
}

func (a *App) createCashReconciliation(accountID int64, reconciliationDate string, countedBalance float64, notes string, reconciledByUserID int64) (int64, error) {
	reconciliationDate = strings.TrimSpace(reconciliationDate)
	day, err := time.ParseInLocation("2006-01-02", reconciliationDate, time.Local)
	if err != nil {
		return 0, errors.New("a valid reconciliation date is required")
	}
	if err := validateFinanceRecordedAt(day, "reconciliation date"); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(day, "reconciliation date"); err != nil {
		return 0, err
	}
	countedBalance = normalizeMoney(countedBalance)
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, err
	}
	if account.AccountType != financeAccountTypeCash {
		return 0, errors.New("cash reconciliation is available only for cash accounts")
	}
	cutoff, err := financeBalanceCutoffForDate(reconciliationDate)
	if err != nil {
		return 0, errors.New("a valid reconciliation date is required")
	}
	expected, err := financeAccountBalanceAsOfTx(tx, account.ID, cutoff)
	if err != nil {
		return 0, err
	}
	fingerprint := fmt.Sprintf("%d|%d|%s|%.2f|%s", reconciledByUserID, account.ID, reconciliationDate, countedBalance, strings.TrimSpace(notes))
	if resultRef, duplicate, err := reserveFinanceOperationTx(tx, "cash_reconciliation", reconciledByUserID, fingerprint); err != nil {
		return 0, err
	} else if duplicate {
		reconciliationID := parseInt64Query(resultRef)
		if reconciliationID > 0 {
			return reconciliationID, tx.Commit()
		}
		return 0, errors.New("this reconciliation was already recorded")
	}
	diff := normalizeMoney(countedBalance - expected)
	if math.Abs(diff) > 0.004 && strings.TrimSpace(notes) == "" {
		return 0, errors.New("notes are required when the cash count differs from the expected balance")
	}
	status := "balanced"
	switch {
	case diff < -0.004:
		status = "short"
	case diff > 0.004:
		status = "over"
	}
	now := time.Now().UTC()
	id, err := a.insertAndReturnIDTx(
		tx,
		`INSERT INTO cash_reconciliations (
			finance_account_id, reconciliation_date, expected_balance, counted_balance, difference,
			notes, status, reconciled_by_user_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account.ID,
		reconciliationDate,
		expected,
		countedBalance,
		diff,
		strings.TrimSpace(notes),
		status,
		nullableExistingUserIDTx(tx, reconciledByUserID),
		now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, errors.New("a cash reconciliation already exists for this account and date")
		}
		return 0, err
	}
	if err := completeFinanceOperationTx(tx, "cash_reconciliation", reconciledByUserID, fingerprint, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (a *App) listCashReconciliations(limit int) ([]CashReconciliation, error) {
	query := `
		SELECT cr.id, cr.finance_account_id, fa.name, cr.reconciliation_date, cr.expected_balance,
		       cr.counted_balance, cr.difference, cr.notes, cr.status,
		       COALESCE(cr.reconciled_by_user_id, 0), COALESCE(u.name, ''),
		       COALESCE(cr.void_reason, ''), COALESCE(cr.voided_by_user_id, 0), COALESCE(vu.name, ''),
		       cr.voided_at, COALESCE(cr.superseded_by_reconciliation_id, 0), cr.created_at
		FROM cash_reconciliations cr
		JOIN finance_accounts fa ON fa.id = cr.finance_account_id
		LEFT JOIN users u ON u.id = cr.reconciled_by_user_id
		LEFT JOIN users vu ON vu.id = cr.voided_by_user_id
		ORDER BY cr.reconciliation_date DESC, cr.id DESC
	`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = a.queryDB(query+` LIMIT ?`, limit)
	} else {
		rows, err = a.queryDB(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reconciliations []CashReconciliation
	for rows.Next() {
		var item CashReconciliation
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.FinanceAccountID, &item.FinanceAccountName, &item.ReconciliationDate, &item.ExpectedBalance,
			&item.CountedBalance, &item.Difference, &item.Notes, &item.Status, &item.ReconciledByUserID, &item.ReconciledByName,
			&item.VoidReason, &item.VoidedByUserID, &item.VoidedByName, &voidedAt, &item.SupersededByID, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.Voided = voidedAt.Valid
		if voidedAt.Valid {
			item.VoidedAt = voidedAt.Time
		}
		reconciliations = append(reconciliations, item)
	}
	return reconciliations, rows.Err()
}

func (a *App) lastCashReconciliationForAccount(accountID int64) (*CashReconciliation, error) {
	row := a.queryRowDB(`
		SELECT cr.id, cr.finance_account_id, fa.name, cr.reconciliation_date, cr.expected_balance,
		       cr.counted_balance, cr.difference, cr.notes, cr.status,
		       COALESCE(cr.reconciled_by_user_id, 0), COALESCE(u.name, ''),
		       COALESCE(cr.void_reason, ''), COALESCE(cr.voided_by_user_id, 0), COALESCE(vu.name, ''),
		       cr.voided_at, COALESCE(cr.superseded_by_reconciliation_id, 0), cr.created_at
		FROM cash_reconciliations cr
		JOIN finance_accounts fa ON fa.id = cr.finance_account_id
		LEFT JOIN users u ON u.id = cr.reconciled_by_user_id
		LEFT JOIN users vu ON vu.id = cr.voided_by_user_id
		WHERE cr.finance_account_id = ?
		  AND cr.voided_at IS NULL
		ORDER BY cr.reconciliation_date DESC, cr.id DESC
		LIMIT 1
	`, accountID)
	var item CashReconciliation
	var voidedAt sql.NullTime
	if err := row.Scan(
		&item.ID, &item.FinanceAccountID, &item.FinanceAccountName, &item.ReconciliationDate, &item.ExpectedBalance,
		&item.CountedBalance, &item.Difference, &item.Notes, &item.Status, &item.ReconciledByUserID, &item.ReconciledByName,
		&item.VoidReason, &item.VoidedByUserID, &item.VoidedByName, &voidedAt, &item.SupersededByID, &item.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	item.Voided = voidedAt.Valid
	if voidedAt.Valid {
		item.VoidedAt = voidedAt.Time
	}
	return &item, nil
}

func (a *App) buildFinanceStatement(accountID int64, from, to string) (*financeStatementRow, error) {
	statement := &financeStatementRow{}
	args := []any{accountID}
	openingQuery := `
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = ?
		  AND voided_at IS NULL
	`
	if from != "" {
		openingQuery += ` AND SUBSTR(TRIM(CAST(recorded_at AS TEXT)), 1, 10) < ?`
		args = append(args, from)
	}
	if err := a.queryRowDB(openingQuery, args...).Scan(&statement.OpeningBalance); err != nil {
		return nil, err
	}
	filter := FinanceFilter{From: from, To: to, AccountID: accountID}
	rows, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		return nil, err
	}
	sortFinanceTransactionsChronological(rows)
	running := normalizeMoney(statement.OpeningBalance)
	for i := range rows {
		running = normalizeMoney(running + financeAccountBalanceEffect(rows[i]))
		rows[i].MoneyIn, rows[i].MoneyOut = financeAmountParts(rows[i].Amount)
		rows[i].RunningBalance = running
	}
	statement.Rows = rows
	statement.ClosingBalance = running
	return statement, nil
}

func financeAccountDisplayBalance(accounts []FinanceAccount, transactions []FinanceTransaction, name string) float64 {
	accountIDs := make(map[int64]struct{})
	for _, account := range accounts {
		if strings.EqualFold(account.Name, name) {
			accountIDs[account.ID] = struct{}{}
		}
	}
	return financeAccountDisplayBalanceForIDs(accountIDs, transactions)
}

func financeAccountTypeDisplayBalance(accounts []FinanceAccount, transactions []FinanceTransaction, accountType string) float64 {
	accountIDs := make(map[int64]struct{})
	for _, account := range accounts {
		if strings.EqualFold(account.AccountType, accountType) {
			accountIDs[account.ID] = struct{}{}
		}
	}
	return financeAccountDisplayBalanceForIDs(accountIDs, transactions)
}

func financeAccountDisplayBalanceForIDs(accountIDs map[int64]struct{}, transactions []FinanceTransaction) float64 {
	if len(accountIDs) == 0 {
		return 0
	}
	total := 0.0
	for _, transaction := range transactions {
		if _, ok := accountIDs[transaction.FinanceAccountID]; ok && listFinanceBalancesInclude(transaction) {
			total = normalizeMoney(total + transaction.Amount)
		}
	}
	return total
}

func latestUnreconciledCashDelta(accounts []FinanceAccount, reconciliations []CashReconciliation, transactions []FinanceTransaction) (float64, string) {
	var cashAccountID int64
	for _, account := range accounts {
		if account.AccountType == financeAccountTypeCash && strings.EqualFold(account.Name, financeAccountCashInHand) {
			cashAccountID = account.ID
			break
		}
	}
	if cashAccountID == 0 {
		return 0, ""
	}
	var latest *CashReconciliation
	for i := range reconciliations {
		if reconciliations[i].FinanceAccountID != cashAccountID || reconciliations[i].Voided {
			continue
		}
		if latest == nil || reconciliations[i].ReconciliationDate > latest.ReconciliationDate {
			latest = &reconciliations[i]
		}
	}
	if latest == nil {
		return financeAccountDisplayBalance(accounts, transactions, financeAccountCashInHand), ""
	}
	current := financeAccountDisplayBalance(accounts, transactions, financeAccountCashInHand)
	return normalizeMoney(current - latest.CountedBalance), latest.ReconciliationDate
}

func sortFinanceTransactionsChronological(rows []FinanceTransaction) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RecordedAt.Equal(rows[j].RecordedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].RecordedAt.Before(rows[j].RecordedAt)
	})
}

func parseInt64Query(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func (a *App) createFinanceTransferHandler(w http.ResponseWriter, r *http.Request) {
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
	fromAccountID := parseInt64Query(r.FormValue("from_account_id"))
	toAccountID := parseInt64Query(r.FormValue("to_account_id"))
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "a valid transfer amount is required", http.StatusBadRequest)
		return
	}
	transferDate := time.Now().UTC()
	if value := strings.TrimSpace(r.FormValue("transfer_date")); value != "" {
		transferDate, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			http.Error(w, "a valid transfer date is required", http.StatusBadRequest)
			return
		}
	}
	currentUser, _ := a.currentUser(r.Context())
	fromAccount, err := a.findFinanceAccountByID(fromAccountID)
	if err != nil {
		a.setFlash(w, "Transfer could not be recorded: finance account was not found.")
		http.Redirect(w, r, "/admin/finance/transfers", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, fromAccount.DivisionID) {
		return
	}
	recordedBy := int64(0)
	if currentUser != nil {
		recordedBy = currentUser.ID
	}
	groupID, err := a.createFinanceTransfer(
		fromAccountID,
		toAccountID,
		amount,
		transferDate,
		strings.TrimSpace(r.FormValue("reference_number")),
		strings.TrimSpace(r.FormValue("description")),
		strings.TrimSpace(r.FormValue("notes")),
		recordedBy,
	)
	if err != nil {
		a.setFlash(w, "Transfer could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance/transfers", http.StatusSeeOther)
		return
	}
	var transactionID int64
	if err := a.queryRowDB(`
		SELECT id
		FROM finance_transactions
		WHERE transfer_group_id = ?
		  AND transaction_type = 'transfer_out'
		ORDER BY id ASC
		LIMIT 1
	`, groupID).Scan(&transactionID); err != nil {
		a.setFlash(w, "Transfer was recorded.")
		http.Redirect(w, r, "/admin/finance/transfers", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createFinanceAccountHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_accounts.create") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	divisionID := parseInt64Query(r.FormValue("division_id"))
	if !a.requireDivisionAccessForDivision(w, r, currentUser, divisionID) {
		return
	}
	accountID, err := a.createFinanceAccount(
		divisionID,
		strings.TrimSpace(r.FormValue("account_code")),
		strings.TrimSpace(r.FormValue("name")),
		strings.ToLower(strings.TrimSpace(r.FormValue("account_type"))),
		strings.TrimSpace(r.FormValue("description")),
		currentUser.ID,
	)
	if err != nil {
		a.setFlash(w, "Finance account could not be created: "+err.Error())
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance account created.")
	http.Redirect(w, r, "/admin/finance/accounts/statement?account_id="+strconv.FormatInt(accountID, 10), http.StatusSeeOther)
}

func (a *App) updateFinanceAccountHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_accounts.update") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	accountID := parseInt64Query(r.FormValue("account_id"))
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Finance account could not be updated: finance account was not found.")
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, account.DivisionID) {
		return
	}
	err = a.updateFinanceAccount(
		accountID,
		strings.TrimSpace(r.FormValue("account_code")),
		strings.TrimSpace(r.FormValue("name")),
		strings.ToLower(strings.TrimSpace(r.FormValue("account_type"))),
		strings.TrimSpace(r.FormValue("description")),
		r.FormValue("is_active") != "0",
		currentUser.ID,
	)
	if err != nil {
		a.setFlash(w, "Finance account could not be updated: "+err.Error())
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance account updated.")
	http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
}

func (a *App) deleteFinanceAccountHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_accounts.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	accountID := parseInt64Query(r.FormValue("account_id"))
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Finance account could not be deleted: finance account was not found.")
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, account.DivisionID) {
		return
	}
	if err := a.deleteFinanceAccount(accountID); err != nil {
		a.setFlash(w, "Finance account could not be deleted: "+err.Error())
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}

	a.setFlash(w, "Finance account deleted.")
	http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
}

func (a *App) createFinanceOpeningBalanceHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/finance/accounts"
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_accounts.create") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	accountID := parseInt64Query(r.FormValue("account_id"))
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Opening balance could not be recorded: finance account was not found.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, account.DivisionID) {
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		a.setFlash(w, "A valid opening balance amount is required.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	recordedAt := time.Now().UTC()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			a.setFlash(w, "A valid opening balance date is required.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	transactionID, err := a.createFinanceOpeningBalance(accountID, amount, recordedAt, strings.TrimSpace(r.FormValue("notes")), currentUser.ID)
	if err != nil {
		a.setFlash(w, "Opening balance could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createFinanceAdjustmentHandler(w http.ResponseWriter, r *http.Request) {
	target := "/admin/finance/accounts"
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_accounts.update") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	accountID := parseInt64Query(r.FormValue("account_id"))
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Adjustment could not be recorded: finance account was not found.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, account.DivisionID) {
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		a.setFlash(w, "A valid adjustment amount is required.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if strings.TrimSpace(r.FormValue("direction")) == "decrease" {
		amount = -amount
	}
	recordedAt := time.Now().UTC()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			a.setFlash(w, "A valid adjustment date is required.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	transactionID, err := a.createFinanceAdjustment(accountID, amount, recordedAt, strings.TrimSpace(r.FormValue("reason")), currentUser.ID)
	if err != nil {
		a.setFlash(w, "Adjustment could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createCashReconciliationHandler(w http.ResponseWriter, r *http.Request) {
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
	accountID := parseInt64Query(r.FormValue("account_id"))
	countedBalance, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("counted_balance")), 64)
	if err != nil {
		http.Error(w, "a valid counted balance is required", http.StatusBadRequest)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Cash reconciliation could not be recorded: finance account was not found.")
		http.Redirect(w, r, "/admin/finance/reconciliations", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, account.DivisionID) {
		return
	}
	reconciledBy := int64(0)
	if currentUser != nil {
		reconciledBy = currentUser.ID
	}
	if _, err := a.createCashReconciliation(accountID, strings.TrimSpace(r.FormValue("reconciliation_date")), countedBalance, strings.TrimSpace(r.FormValue("notes")), reconciledBy); err != nil {
		a.setFlash(w, "Cash reconciliation could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance/reconciliations", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Cash reconciliation recorded.")
	http.Redirect(w, r, "/admin/finance/reconciliations", http.StatusSeeOther)
}

func (a *App) voidFinanceTransactionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_transactions.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	transactionID := parseInt64Query(r.FormValue("transaction_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	transaction, err := a.findFinanceTransactionByIDContext(ctx, transactionID)
	if err != nil {
		http.Error(w, "finance transaction not found", http.StatusNotFound)
		return
	}

	// General finance voiding needs the derived ledger-repair state.
	// The shared single-transaction lookup intentionally does not populate
	// this state because receipt/reporting callers do not require it.
	voidState := []FinanceTransaction{*transaction}
	if err := populateFinanceTransactionVoidStates(ctx, a.db, voidState); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	transaction = &voidState[0]

	if !a.requireDivisionAccessForDivision(w, r, currentUser, transaction.DivisionID) {
		return
	}
	if !financeTransactionAllowsGeneralVoid(transaction) {
		if transaction.OrphanedSource && financeTransactionRepairableOrphan(transaction) {
			tx, err := a.db.Begin()
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			defer tx.Rollback()
			if err := voidFinanceTransactionTx(tx, transaction.ID, reason, currentUser.ID); err != nil {
				a.setFlash(w, "Finance transaction could not be voided: "+err.Error())
				http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
				return
			}
			if err := tx.Commit(); err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			a.setFlash(w, "Orphaned finance transaction was voided from the ledger because its source record no longer exists.")
			http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
			return
		}
		a.setFlash(w, financeVoidWorkflowMessage(transaction))
		http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := voidFinanceTransactionTx(tx, transaction.ID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Finance transaction could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
		return
	}
	if transaction.TransactionType == financeTxnTypeOpeningBalance {
		if err := syncFinanceAccountOpeningBalanceMetadataTx(tx, transaction.FinanceAccountID); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	a.setFlash(w, "Finance transaction was voided.")
	http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
}

func (a *App) voidFinanceTransferHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_transfers.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	groupID := strings.TrimSpace(r.FormValue("group_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	var divisionID int64
	if err := a.queryRowDB(`
		SELECT COALESCE(division_id, 0)
		FROM finance_transactions
		WHERE transfer_group_id = ?
		ORDER BY id ASC
		LIMIT 1
	`, groupID).Scan(&divisionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, currentUser, divisionID) {
		return
	}
	if err := a.voidFinanceTransferGroup(groupID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Transfer could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/finance/transfers", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Transfer was voided.")
	http.Redirect(w, r, "/admin/finance/transfers", http.StatusSeeOther)
}

func (a *App) approveFinanceTransactionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_transactions.update") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	transactionID := parseInt64Query(r.FormValue("transaction_id"))
	if err := a.approveFinanceTransaction(transactionID, currentUser.ID); err != nil {
		a.setFlash(w, "Finance transaction could not be approved: "+err.Error())
		http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance transaction approved and posted to the ledger.")
	http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
}

func (a *App) updateFinancePeriodLockHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance.update") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if err := a.updateFinancePeriodLock(strings.TrimSpace(r.FormValue("locked_until")), strings.TrimSpace(r.FormValue("notes")), currentUser.ID); err != nil {
		a.setFlash(w, "Finance period lock could not be updated: "+err.Error())
		http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Finance period controls updated.")
	http.Redirect(w, r, "/admin/finance/ledger", http.StatusSeeOther)
}

func (a *App) voidCashReconciliationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser, "finance_reconciliations.delete") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	reconciliationID := parseInt64Query(r.FormValue("reconciliation_id"))
	replacementID := parseInt64Query(r.FormValue("replacement_reconciliation_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	if err := a.voidCashReconciliation(reconciliationID, reason, currentUser.ID, replacementID); err != nil {
		a.setFlash(w, "Cash reconciliation could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/finance/reconciliations", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Cash reconciliation was voided.")
	http.Redirect(w, r, "/admin/finance/reconciliations", http.StatusSeeOther)
}

func (a *App) financeAccountStatementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	accountID := parseInt64Query(r.URL.Query().Get("account_id"))
	if accountID <= 0 {
		a.setFlash(w, "Select a valid finance account to open its statement.")
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		a.setFlash(w, "Finance account not found.")
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	if !a.requireDivisionAccessForDivision(w, r, user, account.DivisionID) {
		return
	}
	statement, err := a.buildFinanceStatement(accountID, strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to")))
	if err != nil {
		a.setFlash(w, "Account statement could not be loaded.")
		http.Redirect(w, r, "/admin/finance/accounts", http.StatusSeeOther)
		return
	}
	if strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) == "csv" {
		filename := fmt.Sprintf("mekmaa-account-statement-%d.csv", account.ID)
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		fmt.Fprintf(w, "Reference,Date,Account,Type,Category,Source,Party,Description,Payment Method,Money In,Money Out,Status,Recorded By,Void Reason\n")
		for _, row := range statement.Rows {
			fmt.Fprintf(
				w,
				"%q,%q,%q,%q,%q,%q,%q,%q,%q,%.2f,%.2f,%q,%q,%q\n",
				row.ReferenceNumber,
				formatDateTime(row.RecordedAt),
				account.Name,
				financeTransactionTypeLabel(row.TransactionType),
				financeCategoryLabel(row.Category),
				row.SourceType,
				row.PersonName,
				row.Description,
				row.PaymentMethod,
				row.MoneyIn,
				row.MoneyOut,
				financeTransactionStatusLabel(row),
				row.RecordedByUserName,
				row.VoidReason,
			)
		}
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = account.Name + " Statement"
	data.Description = "Account statement."
	data.FinancePage = "statement"
	data.SelectedFinanceAccount = account
	data.FinanceTransactions = statement.Rows
	data.FinanceFilter = FinanceFilter{
		From:      strings.TrimSpace(r.URL.Query().Get("from")),
		To:        strings.TrimSpace(r.URL.Query().Get("to")),
		AccountID: accountID,
	}
	data.StatementOpeningBalance = statement.OpeningBalance
	data.StatementClosingBalance = statement.ClosingBalance
	for _, row := range statement.Rows {
		if !financeTransactionPosted(row) {
			continue
		}
		data.StatementMoneyIn += row.MoneyIn
		data.StatementMoneyOut += row.MoneyOut
	}
	data.StatementNetMovement = normalizeMoney(data.StatementMoneyIn - data.StatementMoneyOut)
	a.render(w, "finance-management", data, http.StatusOK)
}
