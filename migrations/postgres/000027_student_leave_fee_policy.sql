ALTER TABLE student_enrollment_leaves
    ADD COLUMN IF NOT EXISTS fee_policy TEXT NOT NULL DEFAULT 'legacy_prorated';

ALTER TABLE student_enrollment_leaves
    DROP CONSTRAINT IF EXISTS student_enrollment_leaves_fee_policy_check;

ALTER TABLE student_enrollment_leaves
    ADD CONSTRAINT student_enrollment_leaves_fee_policy_check
    CHECK (fee_policy IN (
        'legacy_prorated',
        'full_fee',
        'half_fee',
        'no_fee'
    ));
