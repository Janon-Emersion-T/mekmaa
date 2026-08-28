ALTER TABLE student_monthly_payments
    ADD COLUMN IF NOT EXISTS discount_amount NUMERIC NOT NULL DEFAULT 0;

ALTER TABLE student_monthly_payments
    ADD COLUMN IF NOT EXISTS adjustment_reason TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_student_monthly_payment_student_month;

DROP INDEX IF EXISTS idx_student_monthly_payment_enrollment_month;

CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_admission_month
    ON student_monthly_payments(admission_id, payment_month, collected_at);

CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_enrollment_month
    ON student_monthly_payments(enrollment_id, payment_month, collected_at);
