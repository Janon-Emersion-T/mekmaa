DO $$
DECLARE
    legacy RECORD;
    keeper_id BIGINT;
BEGIN
    FOR legacy IN
        SELECT id, activity
        FROM training_programs
        WHERE training_format = 'one_to_one'
        ORDER BY id ASC
    LOOP
        SELECT id
        INTO keeper_id
        FROM training_programs
        WHERE activity = legacy.activity
          AND training_format = 'group'
          AND id <> legacy.id
        ORDER BY id ASC
        LIMIT 1;

        IF keeper_id IS NULL THEN
            UPDATE training_programs
            SET training_format = 'group',
                updated_at = CURRENT_TIMESTAMP
            WHERE id = legacy.id;
        ELSE
            UPDATE admissions
            SET training_program_id = keeper_id
            WHERE training_program_id = legacy.id;

            UPDATE student_groups
            SET training_program_id = keeper_id
            WHERE training_program_id = legacy.id;

            UPDATE staff_salary_profiles
            SET training_program_id = keeper_id
            WHERE training_program_id = legacy.id;

            UPDATE admission_training_programs atp
            SET training_program_id = keeper_id
            WHERE atp.training_program_id = legacy.id
              AND NOT EXISTS (
                SELECT 1
                FROM admission_training_programs existing
                WHERE existing.admission_id = atp.admission_id
                  AND existing.training_program_id = keeper_id
              );

            DELETE FROM admission_training_programs
            WHERE training_program_id = legacy.id;

            UPDATE student_enrollments se
            SET training_program_id = keeper_id
            WHERE se.training_program_id = legacy.id
              AND NOT EXISTS (
                SELECT 1
                FROM student_enrollments existing
                WHERE existing.admission_id = se.admission_id
                  AND existing.training_program_id = keeper_id
              );

            DELETE FROM student_enrollments
            WHERE training_program_id = legacy.id;

            DELETE FROM training_programs
            WHERE id = legacy.id;
        END IF;

        keeper_id := NULL;
    END LOOP;

    UPDATE training_programs
    SET training_format = 'group',
        updated_at = CURRENT_TIMESTAMP
    WHERE training_format IS NULL
       OR BTRIM(training_format) = ''
       OR training_format <> 'group';
END $$;
