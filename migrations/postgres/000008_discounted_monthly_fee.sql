ALTER TABLE student_enrollments
ADD COLUMN IF NOT EXISTS discounted_monthly_fee NUMERIC NOT NULL DEFAULT 0;
