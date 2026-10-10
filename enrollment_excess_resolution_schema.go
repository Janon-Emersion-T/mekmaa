package main

// enrollmentExcessResolutionSQLiteSchema defines the SQLite
// equivalent of PostgreSQL migration 000025.
// It is not executed until registered with SQLite bootstrap.
const enrollmentExcessResolutionSQLiteSchema = `
CREATE TABLE IF NOT EXISTS enrollment_excess_resolutions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    closure_id INTEGER NOT NULL UNIQUE
        REFERENCES enrollment_financial_closures(id),
    enrollment_id INTEGER NOT NULL UNIQUE
        REFERENCES student_enrollments(id),
    action TEXT NOT NULL
        CHECK (action IN ('retain', 'refund', 'transfer')),
    amount REAL NOT NULL CHECK (amount > 0),
    reason TEXT NOT NULL DEFAULT '',
    target_enrollment_id INTEGER
        REFERENCES student_enrollments(id),
    original_finance_transaction_id INTEGER
        REFERENCES finance_transactions(id),
    resolution_finance_transaction_id INTEGER
        REFERENCES finance_transactions(id),
    resolution_payment_id INTEGER
        REFERENCES student_monthly_payments(id),
    recorded_by_user_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        (action = 'transfer' AND target_enrollment_id IS NOT NULL)
        OR
        (action <> 'transfer' AND target_enrollment_id IS NULL)
    ),
    CHECK (action <> 'retain' OR LENGTH(TRIM(reason)) > 0)
);
`
