ALTER TABLE student_enrollments
ADD COLUMN IF NOT EXISTS enrollment_date DATE;

UPDATE student_enrollments
SET enrollment_date = COALESCE(enrollment_date, created_at::date)
WHERE enrollment_date IS NULL;

ALTER TABLE student_enrollments
ALTER COLUMN enrollment_date SET NOT NULL;

ALTER TABLE student_enrollments
ADD COLUMN IF NOT EXISTS discounted_monthly_fee NUMERIC NOT NULL DEFAULT 0;

UPDATE student_enrollments
SET discounted_monthly_fee = 0
WHERE discounted_monthly_fee IS NULL;

CREATE INDEX IF NOT EXISTS idx_student_enrollments_enrollment_date
ON student_enrollments(enrollment_date);
