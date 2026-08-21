package main

import "database/sql"

func migrateMCPSQLiteSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS mcp_customers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL UNIQUE,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			phone TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			notes TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_pricing_bands (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tier TEXT NOT NULL,
			minimum_sessions INTEGER NOT NULL,
			maximum_sessions INTEGER NOT NULL DEFAULT 0,
			price_per_session REAL NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			effective_from TEXT NOT NULL DEFAULT '',
			effective_to TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_monthly_plans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			customer_id INTEGER NOT NULL,
			plan_month TEXT NOT NULL,
			game_id INTEGER NOT NULL DEFAULT 0,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL DEFAULT 1,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			total_sessions INTEGER NOT NULL DEFAULT 0,
			gross_amount REAL NOT NULL DEFAULT 0,
			total_collected REAL NOT NULL DEFAULT 0,
			outstanding_amount REAL NOT NULL DEFAULT 0,
			payment_status TEXT NOT NULL DEFAULT 'unpaid',
			notes TEXT NOT NULL DEFAULT '',
			created_by_user_id INTEGER,
			requested_by_user_id INTEGER,
			confirmed_at DATETIME,
			confirmed_by_user_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (customer_id) REFERENCES mcp_customers(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by_user_id) REFERENCES users(id),
			FOREIGN KEY (requested_by_user_id) REFERENCES users(id),
			FOREIGN KEY (confirmed_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_plan_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER NOT NULL,
			weekday INTEGER NOT NULL,
			start_hour TEXT NOT NULL,
			end_hour TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (plan_id) REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_plan_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER NOT NULL,
			session_date TEXT NOT NULL,
			session_hour TEXT NOT NULL,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL DEFAULT 1,
			pricing_tier TEXT NOT NULL,
			pricing_band_id INTEGER,
			pricing_band_minimum INTEGER NOT NULL DEFAULT 0,
			pricing_band_maximum INTEGER NOT NULL DEFAULT 0,
			price_per_session REAL NOT NULL DEFAULT 0,
			amount REAL NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			conflict_reason TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (plan_id) REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE,
			FOREIGN KEY (pricing_band_id) REFERENCES mcp_pricing_bands(id)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_payment_collections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER NOT NULL,
			finance_transaction_id INTEGER NOT NULL UNIQUE,
			amount REAL NOT NULL DEFAULT 0,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			payment_note TEXT NOT NULL DEFAULT '',
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			voided INTEGER NOT NULL DEFAULT 0,
			void_reason TEXT NOT NULL DEFAULT '',
			voided_by_user_id INTEGER,
			voided_at DATETIME,
			FOREIGN KEY (plan_id) REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (collected_by_user_id) REFERENCES users(id),
			FOREIGN KEY (voided_by_user_id) REFERENCES users(id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_pricing_band_unique_range ON mcp_pricing_bands(tier, minimum_sessions, maximum_sessions, COALESCE(effective_from, ''), COALESCE(effective_to, ''))`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_plans_customer_month ON mcp_monthly_plans(customer_id, plan_month DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_plans_status ON mcp_monthly_plans(status, plan_month DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_sessions_slot ON mcp_plan_sessions(session_date, session_hour, status)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_payments_plan ON mcp_payment_collections(plan_id, collected_at DESC, id DESC)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
