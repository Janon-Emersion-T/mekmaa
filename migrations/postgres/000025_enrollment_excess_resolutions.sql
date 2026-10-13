CREATE TABLE IF NOT EXISTS enrollment_excess_resolutions (
    id BIGSERIAL PRIMARY KEY,
    closure_id BIGINT NOT NULL UNIQUE
        REFERENCES enrollment_financial_closures(id),
    enrollment_id BIGINT NOT NULL
        REFERENCES student_enrollments(id),
    action TEXT NOT NULL
        CHECK (action IN ('retain', 'refund', 'transfer')),
    amount NUMERIC(14,2) NOT NULL
        CHECK (amount > 0),
    reason TEXT NOT NULL DEFAULT '',
    target_enrollment_id BIGINT
        REFERENCES student_enrollments(id),
    original_finance_transaction_id BIGINT
        REFERENCES finance_transactions(id),
    resolution_finance_transaction_id BIGINT
        REFERENCES finance_transactions(id),
    resolution_payment_id BIGINT
        REFERENCES student_monthly_payments(id),
    recorded_by_user_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        (action = 'transfer' AND target_enrollment_id IS NOT NULL)
        OR
        (action <> 'transfer' AND target_enrollment_id IS NULL)
    ),
    CHECK (action <> 'retain' OR LENGTH(TRIM(reason)) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS
    idx_enrollment_excess_resolutions_enrollment
ON enrollment_excess_resolutions(enrollment_id);
