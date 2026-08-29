\set ON_ERROR_STOP on
\pset pager off

BEGIN;
SET TRANSACTION READ ONLY;

\echo '============================================================'
\echo ' Mekmaa Payroll Phase 4 - Production Preflight'
\echo '============================================================'

\echo ''
\echo '1. Current migration state'
SELECT version, name, applied_at
FROM schema_migrations
ORDER BY version;

\echo ''
\echo '2. Payroll data summary'
SELECT COUNT(*) AS salary_profiles
FROM staff_salary_profiles;

SELECT COUNT(*) AS payroll_runs
FROM payroll_runs;

SELECT COUNT(*) AS payroll_payments
FROM payroll_payments;

SELECT compensation_type, COUNT(*) AS profile_count
FROM staff_salary_profiles
GROUP BY compensation_type
ORDER BY compensation_type;

\echo ''
\echo '3. Checking orphan payroll finance transaction references'

DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO bad_count
    FROM payroll_payments pp
    LEFT JOIN finance_transactions ft
        ON ft.id = pp.finance_transaction_id
    WHERE pp.finance_transaction_id IS NOT NULL
      AND ft.id IS NULL;

    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'PRECHECK FAILED: % payroll payment(s) reference missing finance transactions',
            bad_count;
    END IF;

    RAISE NOTICE 'PASS: no orphan payroll finance references';
END $$;

\echo ''
\echo '4. Checking duplicate payroll finance source rows'

DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO bad_count
    FROM (
        SELECT ft.source_id
        FROM finance_transactions ft
        WHERE ft.source_type = 'payroll_payment'
          AND ft.source_id IS NOT NULL
        GROUP BY ft.source_id
        HAVING COUNT(*) > 1
    ) duplicates;

    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'PRECHECK FAILED: % duplicate payroll finance source_id group(s) exist',
            bad_count;
    END IF;

    RAISE NOTICE 'PASS: no duplicate payroll finance source rows';
END $$;

\echo ''
\echo '5. Checking existing salary compensation types'

DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO bad_count
    FROM staff_salary_profiles
    WHERE compensation_type NOT IN (
        'hourly',
        'daily',
        'weekly',
        'monthly',
        'per_student',
        'per_session'
    );

    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'PRECHECK FAILED: % salary profile(s) have unsupported compensation types',
            bad_count;
    END IF;

    RAISE NOTICE 'PASS: salary profile compensation types are valid';
END $$;

\echo ''
\echo '6. Checking existing payroll payment compensation types'

DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO bad_count
    FROM payroll_payments
    WHERE compensation_type NOT IN (
        'hourly',
        'daily',
        'weekly',
        'monthly',
        'per_student',
        'per_session'
    );

    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'PRECHECK FAILED: % payroll payment(s) have unsupported compensation types',
            bad_count;
    END IF;

    RAISE NOTICE 'PASS: payroll payment compensation types are valid';
END $$;

\echo ''
\echo '7. Payroll finance rows currently present'

SELECT
    source_id AS payroll_payment_id,
    COUNT(*) AS finance_rows,
    SUM(amount) AS recorded_amount
FROM finance_transactions
WHERE source_type = 'payroll_payment'
GROUP BY source_id
ORDER BY source_id;

COMMIT;

\echo ''
\echo '============================================================'
\echo ' PRECHECK PASSED'
\echo ' Production data is compatible with payroll migrations.'
\echo '============================================================'
