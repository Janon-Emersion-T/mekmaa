ALTER TABLE payroll_payments ADD COLUMN earning_source TEXT NOT NULL DEFAULT 'salary_profile';
CREATE UNIQUE INDEX idx_payroll_one_to_one_run_user
 ON payroll_payments(payroll_run_id, user_id) WHERE earning_source = 'one_to_one';
ALTER TABLE payroll_payment_calculation_details
 DROP CONSTRAINT payroll_payment_calculation_details_source_type_check;
ALTER TABLE payroll_payment_calculation_details
 ADD CONSTRAINT payroll_payment_calculation_details_source_type_check
 CHECK (source_type IN ('', 'admission', 'student_enrollment', 'student_group_session_occurrence', 'one_to_one_booking_session'));
CREATE INDEX idx_payroll_calculation_source
 ON payroll_payment_calculation_details(source_type, source_id, payroll_payment_id);
