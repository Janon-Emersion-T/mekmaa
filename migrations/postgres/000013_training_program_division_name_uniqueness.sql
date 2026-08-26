ALTER TABLE training_programs
    DROP CONSTRAINT IF EXISTS training_programs_activity_training_format_key;

DROP INDEX IF EXISTS training_programs_activity_training_format_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_training_programs_division_name_ci
    ON training_programs(COALESCE(division_id, 0), LOWER(BTRIM(name)));
