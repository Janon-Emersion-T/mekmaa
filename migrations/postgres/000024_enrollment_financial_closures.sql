CREATE TABLE IF NOT EXISTS enrollment_financial_closures (
    id BIGSERIAL PRIMARY KEY,
    enrollment_id BIGINT NOT NULL UNIQUE
        REFERENCES student_enrollments(id),
    effective_date TEXT NOT NULL,
    final_monthly_fee NUMERIC(14,2) NOT NULL
        CHECK (final_monthly_fee >= 0),
    collected_amount NUMERIC(14,2) NOT NULL DEFAULT 0,
    discount_amount NUMERIC(14,2) NOT NULL DEFAULT 0,
    excess_amount NUMERIC(14,2) NOT NULL DEFAULT 0,
    excess_action TEXT NOT NULL DEFAULT '',
    excess_reason TEXT NOT NULL DEFAULT '',
    target_enrollment_id BIGINT
        REFERENCES student_enrollments(id),
    recorded_by_user_id BIGINT,
    resolution_transaction_id BIGINT
        REFERENCES finance_transactions(id),
    resolution_payment_id BIGINT
        REFERENCES student_monthly_payments(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        excess_action IN ('', 'retain', 'refund', 'transfer')
    ),
    CHECK (excess_amount >= 0)
);

CREATE INDEX IF NOT EXISTS idx_enrollment_financial_closures_date
    ON enrollment_financial_closures(effective_date);
