ALTER TABLE student_enrollments
    ADD COLUMN enrollment_date DATE;

UPDATE student_enrollments
SET enrollment_date = created_at::date
WHERE enrollment_date IS NULL;

ALTER TABLE student_enrollments
    ALTER COLUMN enrollment_date SET NOT NULL;

CREATE INDEX idx_student_enrollments_enrollment_date
    ON student_enrollments(enrollment_date);
