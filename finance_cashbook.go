package main

import (
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
	Category         string
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
	RecordedAt       time.Time
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
	case "cash", "bank_transfer":
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

func financeDirectionForTransaction(transaction FinanceTransaction) string {
	if transaction.MoneyOut > 0 {
		return "expense"
	}
	return "income"
}

func financeHighRiskAuthorized(user *User) bool {
	return user != nil && containsPermission(user.Permissions, "finance.manage") && containsRole(user.Roles, "superadmin")
}

func financeAccountBalanceEffect(transaction FinanceTransaction) float64 {
	if transaction.Voided {
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
	return "Active"
}

func financeAccountTone(accountType string) string {
	if accountType == financeAccountTypeBank {
		return "border-sky-100 bg-sky-50"
	}
	return "border-emerald-100 bg-emerald-50"
}

func ensureFinanceSystemAccountsTx(tx *sql.Tx) error {
	now := time.Now().UTC()
	required := []FinanceAccount{
		{Name: financeAccountCashInHand, AccountType: financeAccountTypeCash, Description: "Physical cash currently held by Mekmaa.", IsSystem: true, IsActive: true},
		{Name: financeAccountMainBank, AccountType: financeAccountTypeBank, Description: "Primary business bank balance.", IsSystem: true, IsActive: true},
	}
	for _, account := range required {
		var existingID int64
		err := tx.QueryRow(`SELECT id FROM finance_accounts WHERE LOWER(name) = LOWER(?) LIMIT 1`, account.Name).Scan(&existingID)
		if err == nil {
			if _, updateErr := tx.Exec(`
				UPDATE finance_accounts
				SET account_type = COALESCE(NULLIF(account_type, ''), ?),
				    description = CASE WHEN TRIM(COALESCE(description, '')) = '' THEN ? ELSE description END,
				    is_system = 1,
				    is_active = 1,
				    updated_at = ?
				WHERE id = ?
			`, account.AccountType, account.Description, now, existingID); updateErr != nil {
				return updateErr
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO finance_accounts (
				name, account_type, description, opening_balance, is_system, is_active,
				created_at, updated_at, created_by_user_id, updated_by_user_id
			) VALUES (?, ?, ?, 0, 1, 1, ?, ?, NULL, NULL)
		`, account.Name, account.AccountType, account.Description, now, now); err != nil {
			return err
		}
	}
	return nil
}

func migrateFinanceCashbook(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS finance_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_finance_accounts_name_ci ON finance_accounts(LOWER(name))`,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cash_reconciliations_account_date_active ON cash_reconciliations(finance_account_id, reconciliation_date) WHERE voided_at IS NULL`,
		`CREATE TABLE IF NOT EXISTS finance_operation_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			operation_scope TEXT NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			fingerprint TEXT NOT NULL,
			result_ref TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			UNIQUE(operation_scope, user_id, fingerprint)
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
		{"finance_transactions", "finance_account_id", `ALTER TABLE finance_transactions ADD COLUMN finance_account_id INTEGER`},
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
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_account ON finance_transactions(finance_account_id, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_type ON finance_transactions(transaction_type, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_status ON finance_transactions(voided_at, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_source ON finance_transactions(source_type, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_transfer_group ON finance_transactions(transfer_group_id)`,
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
	if err := ensureFinanceSystemAccountsTx(tx); err != nil {
		return err
	}
	var cashAccountID, bankAccountID int64
	if err := tx.QueryRow(`SELECT id FROM finance_accounts WHERE LOWER(name) = LOWER(?) LIMIT 1`, financeAccountCashInHand).Scan(&cashAccountID); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT id FROM finance_accounts WHERE LOWER(name) = LOWER(?) LIMIT 1`, financeAccountMainBank).Scan(&bankAccountID); err != nil {
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
	if _, err := tx.Exec(`UPDATE finance_transactions SET updated_at = COALESCE(updated_at, created_at, recorded_at, ?)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE finance_transactions SET payment_method = 'cash' WHERE TRIM(COALESCE(payment_method, '')) = ''`); err != nil {
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
		if _, err := tx.Exec(`UPDATE finance_transactions SET finance_account_id = ? WHERE id = ?`, accountID, transactionID); err != nil {
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
	return nil
}

func financeSourceDuplicateCount(db *sql.DB, sourceType string) (int, error) {
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM (
			SELECT source_id
			FROM finance_transactions
			WHERE source_type = ?
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
	query := `
		SELECT id, name, account_type, description, opening_balance, is_system, is_active,
		       COALESCE(created_by_user_id, 0), COALESCE(updated_by_user_id, 0), created_at, updated_at
		FROM finance_accounts
	`
	if activeOnly {
		query += ` WHERE is_active = 1`
	}
	query += ` ORDER BY is_system DESC, account_type ASC, name COLLATE NOCASE ASC, id ASC`
	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []FinanceAccount
	for rows.Next() {
		var account FinanceAccount
		var isSystem, isActive int
		if err := rows.Scan(
			&account.ID, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
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
		SELECT id, name, account_type, description, opening_balance, is_system, is_active,
		       COALESCE(created_by_user_id, 0), COALESCE(updated_by_user_id, 0), created_at, updated_at
		FROM finance_accounts
		WHERE id = ?
	`, accountID)
	var account FinanceAccount
	var isSystem, isActive int
	if err := row.Scan(
		&account.ID, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
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

func findFinanceAccountByNameTx(tx *sql.Tx, name string) (*FinanceAccount, error) {
	row := tx.QueryRow(`
		SELECT id, name, account_type, description, opening_balance, is_system, is_active,
		       COALESCE(created_by_user_id, 0), COALESCE(updated_by_user_id, 0), created_at, updated_at
		FROM finance_accounts
		WHERE LOWER(name) = LOWER(?)
		LIMIT 1
	`, name)
	var account FinanceAccount
	var isSystem, isActive int
	if err := row.Scan(
		&account.ID, &account.Name, &account.AccountType, &account.Description, &account.OpeningBalance,
		&isSystem, &isActive, &account.CreatedByUserID, &account.UpdatedByUserID, &account.CreatedAt, &account.UpdatedAt,
	); err != nil {
		return nil, err
	}
	account.IsSystem = isSystem == 1
	account.IsActive = isActive == 1
	return &account, nil
}

func findFinanceAccountForPaymentMethodTx(tx *sql.Tx, paymentMethod string) (*FinanceAccount, error) {
	switch normalizePaymentMethod(paymentMethod) {
	case "bank_transfer":
		return findFinanceAccountByNameTx(tx, financeAccountMainBank)
	default:
		return findFinanceAccountByNameTx(tx, financeAccountCashInHand)
	}
}

func financeAccountBalanceTx(tx *sql.Tx, accountID int64) (float64, error) {
	var balance float64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = ?
		  AND voided_at IS NULL
	`, accountID).Scan(&balance); err != nil {
		return 0, err
	}
	return normalizeMoney(balance), nil
}

func (a *App) financeAccountBalance(accountID int64) (float64, error) {
	var balance float64
	if err := a.db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM finance_transactions
		WHERE finance_account_id = ?
		  AND voided_at IS NULL
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
		WHERE finance_account_id = ?
		  AND voided_at IS NULL
		  AND recorded_at <= ?
	`, accountID, cutoff.UTC()).Scan(&balance); err != nil {
		return 0, err
	}
	return normalizeMoney(balance), nil
}

func financeDateNotInFuture(date time.Time) bool {
	localDate := time.Date(date.In(time.Local).Year(), date.In(time.Local).Month(), date.In(time.Local).Day(), 0, 0, 0, 0, time.Local)
	today := time.Now().In(time.Local)
	todayDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	return !localDate.After(todayDate)
}

func validateFinanceRecordedAt(recordedAt time.Time, label string) error {
	if recordedAt.IsZero() {
		return errors.New(label + " is required")
	}
	if !financeDateNotInFuture(recordedAt) {
		return errors.New(label + " cannot be in the future")
	}
	return nil
}

func syncFinanceAccountOpeningBalanceMetadataTx(tx *sql.Tx, accountID int64) error {
	var openingBalance float64
	if err := tx.QueryRow(`
		SELECT COALESCE(amount, 0)
		FROM finance_transactions
		WHERE finance_account_id = ?
		  AND transaction_type = 'opening_balance'
		  AND voided_at IS NULL
		ORDER BY recorded_at DESC, id DESC
		LIMIT 1
	`, accountID).Scan(&openingBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			openingBalance = 0
		} else {
			return err
		}
	}
	_, err := tx.Exec(`UPDATE finance_accounts SET opening_balance = ?, updated_at = ? WHERE id = ?`, normalizeMoney(openingBalance), time.Now().UTC(), accountID)
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
		VALUES (?, ?, ?, ?)
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
		WHERE operation_scope = ? AND user_id = ? AND fingerprint = ?
		LIMIT 1
	`, scope, userID, fingerprint).Scan(&existing); scanErr != nil {
		return "", false, scanErr
	}
	return existing, true, nil
}

func completeFinanceOperationTx(tx *sql.Tx, scope string, userID int64, fingerprint string, resultRef string) error {
	_, err := tx.Exec(`
		UPDATE finance_operation_keys
		SET result_ref = ?
		WHERE operation_scope = ? AND user_id = ? AND fingerprint = ?
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
		return 0, err
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
	receiptNumber := strings.TrimSpace(entry.ReceiptNumber)
	if receiptNumber == "" {
		receiptNumber = financeVoucherReference("MKM-FIN", createdAt)
	}
	referenceNumber := strings.TrimSpace(entry.ReferenceNumber)
	if referenceNumber == "" {
		referenceNumber = receiptNumber
	}
	result, err := tx.Exec(`
		INSERT INTO finance_transactions (
			receipt_number, reference_number, category, transaction_type,
			reference_type, reference_id, source_type, source_id, finance_account_id,
			transfer_group_id, person_name, description, notes, payment_method, amount,
			recorded_by_user_id, recorded_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		receiptNumber,
		referenceNumber,
		strings.TrimSpace(entry.Category),
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
		recordedAt,
		createdAt,
		createdAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func voidFinanceTransactionTx(tx *sql.Tx, transactionID int64, reason string, voidedByUserID int64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("void reason is required")
	}
	var alreadyVoided int
	if err := tx.QueryRow(`SELECT CASE WHEN voided_at IS NULL THEN 0 ELSE 1 END FROM finance_transactions WHERE id = ?`, transactionID).Scan(&alreadyVoided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("finance transaction not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("finance transaction has already been voided")
	}
	_, err := tx.Exec(`
		UPDATE finance_transactions
		SET voided_at = ?, voided_by_user_id = ?, void_reason = ?, updated_at = ?
		WHERE id = ? AND voided_at IS NULL
	`, time.Now().UTC(), nullableExistingUserIDTx(tx, voidedByUserID), reason, time.Now().UTC(), transactionID)
	return err
}

func financeVoidWorkflowMessage(transaction *FinanceTransaction) string {
	switch transaction.SourceType {
	case "booking_payment_collection":
		return "Void booking income from the booking payment workflow so the booking balance stays synchronized."
	case "admission":
		return "Void admission income from the admission payment workflow so the admission record stays synchronized."
	case "student_monthly_payment":
		return "Void monthly income from the student payment workflow so the monthly payment record stays synchronized."
	case "booking_referral_payment":
		return "Void referral payouts from the referral payment workflow so the payout record stays synchronized."
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

	var financeTransactionID int64
	var paymentCollected int
	var paymentVoidedAt sql.NullTime
	if err := tx.QueryRow(`
		SELECT COALESCE(finance_transaction_id, 0), COALESCE(payment_collected, 0), payment_voided_at
		FROM admissions
		WHERE id = ?
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
		    payment_void_reason = ?,
		    payment_voided_by_user_id = ?,
		    payment_voided_at = ?,
		    updated_at = ?
		WHERE id = ? AND payment_collected = 1
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
		WHERE id = ?
	`, paymentID).Scan(&financeTransactionID, &alreadyVoided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("student payment was not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("student payment has already been voided")
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE student_monthly_payments
		SET voided = 1, void_reason = ?, voided_by_user_id = ?, voided_at = ?
		WHERE id = ? AND voided = 0
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), now, paymentID); err != nil {
		return err
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
		WHERE id = ?
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
		    void_reason = ?,
		    voided_by_user_id = ?,
		    voided_at = ?
		WHERE id = ? AND paid = 1
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
	if err := tx.QueryRow(`SELECT CASE WHEN voided_at IS NULL THEN 0 ELSE 1 END FROM cash_reconciliations WHERE id = ?`, reconciliationID).Scan(&alreadyVoided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("cash reconciliation was not found")
		}
		return err
	}
	if alreadyVoided == 1 {
		return errors.New("cash reconciliation has already been voided")
	}
	_, err = tx.Exec(`
		UPDATE cash_reconciliations
		SET void_reason = ?, voided_by_user_id = ?, voided_at = ?, superseded_by_reconciliation_id = ?
		WHERE id = ? AND voided_at IS NULL
	`, reason, nullableExistingUserIDTx(tx, voidedByUserID), time.Now().UTC(), nullIfZero(supersededByID), reconciliationID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) createManualFinanceTransactionForAccount(category, personName, description, notes string, accountID int64, amount float64, recordedAt time.Time, recordedByUserID int64) (int64, error) {
	if err := validateFinanceRecordedAt(recordedAt, "transaction date"); err != nil {
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
		Category:         category,
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
		RecordedAt:       recordedAt,
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
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountForPaymentMethodTx(tx, paymentMethod)
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
		Category:         category,
		TransactionType:  transactionType,
		ReferenceType:    "manual",
		SourceType:       "manual",
		FinanceAccountID: account.ID,
		PersonName:       personName,
		Description:      description,
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
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
		Category:         "internal_transfer",
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
		RecordedAt:       transferDate,
	}); err != nil {
		return "", err
	}
	if _, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    inReceipt,
		ReferenceNumber:  referenceNumber,
		Category:         "internal_transfer",
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
		RecordedAt:       transferDate,
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
		WHERE transfer_group_id = ?
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
	rows, err := a.db.Query(`
		SELECT
			transfer_group_id,
			reference_number,
			MAX(CASE WHEN transaction_type = 'transfer_out' THEN finance_account_id ELSE 0 END),
			MAX(CASE WHEN transaction_type = 'transfer_out' THEN COALESCE(fa.name, '') ELSE '' END),
			MAX(CASE WHEN transaction_type = 'transfer_in' THEN finance_account_id ELSE 0 END),
			MAX(CASE WHEN transaction_type = 'transfer_in' THEN COALESCE(fa.name, '') ELSE '' END),
			MAX(ABS(amount)),
			MIN(recorded_at),
			MAX(description),
			MAX(notes),
			MAX(COALESCE(recorded_by_user_id, 0)),
			MAX(COALESCE(u.name, '')),
			MAX(CASE WHEN voided_at IS NULL THEN 0 ELSE 1 END),
			MAX(voided_at),
			MAX(COALESCE(void_reason, '')),
			MAX(COALESCE(voided_by_user_id, 0)),
			MIN(created_at),
			MAX(CASE WHEN transaction_type = 'transfer_out' THEN id ELSE 0 END),
			MAX(CASE WHEN transaction_type = 'transfer_in' THEN id ELSE 0 END)
		FROM finance_transactions ft
		LEFT JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		LEFT JOIN users u ON u.id = ft.recorded_by_user_id
		WHERE transfer_group_id <> ''
		GROUP BY transfer_group_id, reference_number
		ORDER BY MIN(recorded_at) DESC, transfer_group_id DESC
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
		if err := rows.Scan(
			&transfer.GroupID,
			&transfer.ReferenceNumber,
			&transfer.FromAccountID,
			&transfer.FromAccountName,
			&transfer.ToAccountID,
			&transfer.ToAccountName,
			&transfer.Amount,
			&transfer.TransferDate,
			&transfer.Description,
			&transfer.Notes,
			&transfer.RecordedByUserID,
			&transfer.RecordedByUserName,
			&voided,
			&voidedAt,
			&transfer.VoidReason,
			&transfer.VoidedByUserID,
			&transfer.CreatedAt,
			&transfer.TransferOutID,
			&transfer.TransferInID,
		); err != nil {
			return nil, err
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
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, err
	}
	var existingCount int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM finance_transactions
		WHERE finance_account_id = ?
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
		Category:         "opening_balance",
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
		RecordedAt:       recordedAt,
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
		Category:         "cash_adjustment",
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
		RecordedAt:       recordedAt,
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
	result, err := tx.Exec(`
		INSERT INTO cash_reconciliations (
			finance_account_id, reconciliation_date, expected_balance, counted_balance, difference,
			notes, status, reconciled_by_user_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, account.ID, reconciliationDate, expected, countedBalance, diff, strings.TrimSpace(notes), status, nullableExistingUserIDTx(tx, reconciledByUserID), now)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, errors.New("a cash reconciliation already exists for this account and date")
		}
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
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
		rows, err = a.db.Query(query+` LIMIT ?`, limit)
	} else {
		rows, err = a.db.Query(query)
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
	row := a.db.QueryRow(`
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
	if err := a.db.QueryRow(openingQuery, args...).Scan(&statement.OpeningBalance); err != nil {
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
	var accountID int64
	for _, account := range accounts {
		if strings.EqualFold(account.Name, name) {
			accountID = account.ID
			break
		}
	}
	if accountID == 0 {
		return 0
	}
	total := 0.0
	for _, transaction := range transactions {
		if transaction.FinanceAccountID == accountID && !transaction.Voided {
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
		http.Redirect(w, r, "/admin/finance#transfers", http.StatusSeeOther)
		return
	}
	var transactionID int64
	if err := a.db.QueryRow(`
		SELECT id
		FROM finance_transactions
		WHERE transfer_group_id = ?
		  AND transaction_type = 'transfer_out'
		ORDER BY id ASC
		LIMIT 1
	`, groupID).Scan(&transactionID); err != nil {
		a.setFlash(w, "Transfer was recorded.")
		http.Redirect(w, r, "/admin/finance#transfers", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createFinanceOpeningBalanceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	accountID := parseInt64Query(r.FormValue("account_id"))
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "a valid opening balance amount is required", http.StatusBadRequest)
		return
	}
	recordedAt := time.Now().UTC()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			http.Error(w, "a valid opening balance date is required", http.StatusBadRequest)
			return
		}
	}
	transactionID, err := a.createFinanceOpeningBalance(accountID, amount, recordedAt, strings.TrimSpace(r.FormValue("notes")), currentUser.ID)
	if err != nil {
		a.setFlash(w, "Opening balance could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance#accounts", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/finance/receipt?transaction_id="+strconv.FormatInt(transactionID, 10), http.StatusSeeOther)
}

func (a *App) createFinanceAdjustmentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	currentUser, _ := a.currentUser(r.Context())
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	accountID := parseInt64Query(r.FormValue("account_id"))
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "a valid adjustment amount is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(r.FormValue("direction")) == "decrease" {
		amount = -amount
	}
	recordedAt := time.Now().UTC()
	if value := strings.TrimSpace(r.FormValue("recorded_date")); value != "" {
		recordedAt, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			http.Error(w, "a valid adjustment date is required", http.StatusBadRequest)
			return
		}
	}
	transactionID, err := a.createFinanceAdjustment(accountID, amount, recordedAt, strings.TrimSpace(r.FormValue("reason")), currentUser.ID)
	if err != nil {
		a.setFlash(w, "Adjustment could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance#accounts", http.StatusSeeOther)
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
	reconciledBy := int64(0)
	if currentUser != nil {
		reconciledBy = currentUser.ID
	}
	if _, err := a.createCashReconciliation(accountID, strings.TrimSpace(r.FormValue("reconciliation_date")), countedBalance, strings.TrimSpace(r.FormValue("notes")), reconciledBy); err != nil {
		a.setFlash(w, "Cash reconciliation could not be recorded: "+err.Error())
		http.Redirect(w, r, "/admin/finance#reconciliation", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Cash reconciliation recorded.")
	http.Redirect(w, r, "/admin/finance#reconciliation", http.StatusSeeOther)
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
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	transactionID := parseInt64Query(r.FormValue("transaction_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	transaction, err := a.findFinanceTransactionByID(transactionID)
	if err != nil {
		http.Error(w, "finance transaction not found", http.StatusNotFound)
		return
	}
	if !financeTransactionAllowsGeneralVoid(transaction) {
		a.setFlash(w, financeVoidWorkflowMessage(transaction))
		http.Redirect(w, r, "/admin/finance#ledger", http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin/finance#ledger", http.StatusSeeOther)
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
	http.Redirect(w, r, "/admin/finance#ledger", http.StatusSeeOther)
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
	if !financeHighRiskAuthorized(currentUser) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	groupID := strings.TrimSpace(r.FormValue("group_id"))
	reason := strings.TrimSpace(r.FormValue("void_reason"))
	if err := a.voidFinanceTransferGroup(groupID, reason, currentUser.ID); err != nil {
		a.setFlash(w, "Transfer could not be voided: "+err.Error())
		http.Redirect(w, r, "/admin/finance#transfers-list", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Transfer was voided.")
	http.Redirect(w, r, "/admin/finance#transfers-list", http.StatusSeeOther)
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
	if !financeHighRiskAuthorized(currentUser) {
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
		http.Redirect(w, r, "/admin/finance#reconciliation", http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Cash reconciliation was voided.")
	http.Redirect(w, r, "/admin/finance#reconciliation", http.StatusSeeOther)
}

func (a *App) financeAccountStatementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	accountID := parseInt64Query(r.URL.Query().Get("account_id"))
	if accountID <= 0 {
		http.Error(w, "account is required", http.StatusBadRequest)
		return
	}
	account, err := a.findFinanceAccountByID(accountID)
	if err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	statement, err := a.buildFinanceStatement(accountID, strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to")))
	if err != nil {
		http.Error(w, "could not build account statement", http.StatusInternalServerError)
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
				row.RecordedAt.Format("2006-01-02 15:04"),
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
	data.SelectedFinanceAccount = account
	data.FinanceTransactions = statement.Rows
	data.FinanceSummary = FinanceSummary{
		CashBalance:         statement.OpeningBalance,
		BankBalance:         statement.ClosingBalance,
		TotalAvailableFunds: statement.ClosingBalance,
	}
	a.render(w, "finance-management", data, http.StatusOK)
}
