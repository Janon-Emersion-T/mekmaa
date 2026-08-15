package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

func (a *App) findAdmissionByID(
	admissionID int64,
) (*Admission, error) {
	row := a.db.QueryRow(`
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number,
			a.guardian_alternative_contact_number,
			a.medical_information,
			COALESCE(a.photo_path, ''),
			COALESCE(a.qr_code_path, ''),
			COALESCE(a.qr_code_value, ''),
			COALESCE(a.free_admission, 0),
			COALESCE(a.free_monthly_fee, 0),
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			COALESCE(a.payment_void_reason, ''),
			COALESCE(a.payment_voided_by_user_id, 0),
			COALESCE(vu.name, ''),
			a.payment_voided_at,
			a.created_at
		FROM admissions a
		LEFT JOIN users vu ON vu.id = a.payment_voided_by_user_id
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		WHERE a.id = ?
	`, admissionID)

	var admission Admission
	var freeAdmission int
	var freeMonthlyFee int
	var paymentCollected int
	var paymentCollectedAt sql.NullTime
	var paymentVoidedAt sql.NullTime

	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
		&admission.TrainingProgramID,
		&admission.TrainingProgramName,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&admission.PhotoPath,
		&admission.QRCodePath,
		&admission.QRCodeValue,
		&freeAdmission,
		&freeMonthlyFee,
		&paymentCollected,
		&paymentCollectedAt,
		&admission.AdmissionPaymentAmount,
		&admission.FinanceTransactionID,
		&admission.PaymentVoidReason,
		&admission.PaymentVoidedByUserID,
		&admission.PaymentVoidedByUserName,
		&paymentVoidedAt,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}

	admission.FreeAdmission = freeAdmission == 1
	admission.FreeMonthlyFee = freeMonthlyFee == 1
	admission.PaymentCollected = paymentCollected == 1

	if paymentCollectedAt.Valid {
		admission.PaymentCollectedAt = paymentCollectedAt.Time
	}
	if paymentVoidedAt.Valid {
		admission.PaymentVoidedAt = paymentVoidedAt.Time
	}

	assignments, err := a.listTrainingProgramsForAdmissions([]int64{admission.ID})
	if err != nil {
		return nil, err
	}
	if programs := assignments[admission.ID]; len(programs) > 0 {
		admission.TrainingPrograms = programs
		admission.TrainingProgramIDs = admission.TrainingProgramIDs[:0]
		for _, program := range programs {
			admission.TrainingProgramIDs = append(admission.TrainingProgramIDs, program.ID)
		}
		admission.TrainingProgramID = programs[0].ID
		admission.TrainingProgramName = programs[0].Name
		admission.TrainingProgramNames = trainingProgramNames(programs)
	} else if admission.TrainingProgramID > 0 {
		admission.TrainingProgramIDs = []int64{admission.TrainingProgramID}
		admission.TrainingProgramNames = admission.TrainingProgramName
	}

	return &admission, nil
}

func (a *App) findAdmissionByIDTx(
	tx *sql.Tx,
	admissionID int64,
) (*Admission, error) {
	row := tx.QueryRow(`
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.training_program_id, 0),
			COALESCE(
				tp.name,
				CASE
					WHEN TRIM(COALESCE(a.practice_type, '')) <> '' THEN 'Legacy training programme'
					ELSE ''
				END
			),
			a.address,
			a.passport_number,
			a.school,
			a.guardian_name,
			a.guardian_relationship,
			a.guardian_contact_number,
			a.guardian_alternative_contact_number,
			a.medical_information,
			COALESCE(a.photo_path, ''),
			COALESCE(a.qr_code_path, ''),
			COALESCE(a.qr_code_value, ''),
			COALESCE(a.free_admission, 0),
			COALESCE(a.free_monthly_fee, 0),
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			COALESCE(a.payment_void_reason, ''),
			COALESCE(a.payment_voided_by_user_id, 0),
			COALESCE(vu.name, ''),
			a.payment_voided_at,
			a.created_at
		FROM admissions a
		LEFT JOIN users vu ON vu.id = a.payment_voided_by_user_id
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		WHERE a.id = ?
	`, admissionID)

	var admission Admission
	var freeAdmission int
	var freeMonthlyFee int
	var paymentCollected int
	var paymentCollectedAt sql.NullTime
	var paymentVoidedAt sql.NullTime

	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
		&admission.TrainingProgramID,
		&admission.TrainingProgramName,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&admission.PhotoPath,
		&admission.QRCodePath,
		&admission.QRCodeValue,
		&freeAdmission,
		&freeMonthlyFee,
		&paymentCollected,
		&paymentCollectedAt,
		&admission.AdmissionPaymentAmount,
		&admission.FinanceTransactionID,
		&admission.PaymentVoidReason,
		&admission.PaymentVoidedByUserID,
		&admission.PaymentVoidedByUserName,
		&paymentVoidedAt,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}

	admission.FreeAdmission = freeAdmission == 1
	admission.FreeMonthlyFee = freeMonthlyFee == 1
	admission.PaymentCollected = paymentCollected == 1

	if paymentCollectedAt.Valid {
		admission.PaymentCollectedAt = paymentCollectedAt.Time
	}
	if paymentVoidedAt.Valid {
		admission.PaymentVoidedAt = paymentVoidedAt.Time
	}

	if err := populateAdmissionTrainingProgramsTx(tx, &admission); err != nil {
		return nil, err
	}

	return &admission, nil
}

func (a *App) findFinanceTransactionByID(transactionID int64) (*FinanceTransaction, error) {
	return a.findFinanceTransactionByIDContext(context.Background(), transactionID)
}

func (a *App) findFinanceTransactionByIDContext(ctx context.Context, transactionID int64) (*FinanceTransaction, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT ft.id,
		       ft.receipt_number,
		       COALESCE(ft.reference_number, ft.receipt_number),
		       COALESCE(ft.division_id, 0),
		       COALESCE(fd.code, ''),
		       COALESCE(fd.name, ''),
		       ft.category,
		       COALESCE(ft.approval_status, 'approved'),
		       COALESCE(ft.transaction_type, CASE WHEN ft.amount < 0 THEN 'expense' ELSE 'income' END),
		       ft.reference_type,
		       COALESCE(ft.reference_id, 0),
		       COALESCE(ft.source_type, ''),
		       COALESCE(ft.source_id, 0),
		       COALESCE(ft.finance_account_id, 0),
		       COALESCE(fa.account_code, ''),
		       COALESCE(fa.name, ''),
		       COALESCE(fa.account_type, ''),
		       COALESCE(ft.transfer_group_id, ''),
		       COALESCE(CASE
		       	WHEN ft.reference_type = 'admission' THEN adm.full_name
		       	WHEN ft.reference_type = 'student_enrollment' THEN sea.full_name
		       	WHEN ft.source_type = 'student_monthly_payment' THEN COALESCE(smp_adm.full_name, sea.full_name, adm.full_name)
		       	ELSE ''
		       END, ''),
		       COALESCE(CASE
		       	WHEN ft.reference_type = 'student_enrollment' THEN tp.name
		       	WHEN ft.reference_type = 'admission' THEN adm_tp.name
		       	WHEN ft.source_type = 'student_monthly_payment' THEN COALESCE(smp_tp.name, tp.name, adm_tp.name)
		       	ELSE ''
		       END, ''),
		       ft.person_name,
		       ft.description,
		       COALESCE(ft.notes, ''),
		       ft.payment_method,
		       ft.amount,
		       COALESCE(ft.recorded_by_user_id, 0),
		       COALESCE(u.name, ''),
		       COALESCE(ft.approved_by_user_id, 0),
		       COALESCE(au.name, ''),
		       ft.recorded_at,
		       ft.created_at,
		       COALESCE(CAST(ft.updated_at AS TEXT), CAST(ft.created_at AS TEXT), ''),
		       ft.approved_at,
		       ft.voided_at,
		       COALESCE(ft.voided_by_user_id, 0),
		       COALESCE(ft.void_reason, '')
		FROM finance_transactions ft
		LEFT JOIN divisions fd ON fd.id = ft.division_id
		LEFT JOIN finance_accounts fa ON fa.id = ft.finance_account_id
		LEFT JOIN users u ON u.id = ft.recorded_by_user_id
		LEFT JOIN users au ON au.id = ft.approved_by_user_id
		LEFT JOIN student_enrollments se ON ft.reference_type = 'student_enrollment' AND se.id = ft.reference_id
		LEFT JOIN admissions sea ON sea.id = se.admission_id
		LEFT JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN admissions adm ON ft.reference_type = 'admission' AND adm.id = ft.reference_id
		LEFT JOIN training_programs adm_tp ON adm_tp.id = adm.training_program_id
		LEFT JOIN student_monthly_payments smp ON ft.source_type = 'student_monthly_payment' AND smp.id = ft.source_id
		LEFT JOIN student_enrollments smp_se ON smp_se.id = smp.enrollment_id
		LEFT JOIN training_programs smp_tp ON smp_tp.id = smp_se.training_program_id
		LEFT JOIN admissions smp_adm ON smp_adm.id = smp.admission_id
		WHERE ft.id = ?
	`, transactionID)

	var transaction FinanceTransaction
	var voidedAt sql.NullTime
	var updatedAtRaw string
	if err := row.Scan(
		&transaction.ID,
		&transaction.ReceiptNumber,
		&transaction.ReferenceNumber,
		&transaction.DivisionID,
		&transaction.DivisionCode,
		&transaction.DivisionName,
		&transaction.Category,
		&transaction.ApprovalStatus,
		&transaction.TransactionType,
		&transaction.ReferenceType,
		&transaction.ReferenceID,
		&transaction.SourceType,
		&transaction.SourceID,
		&transaction.FinanceAccountID,
		&transaction.FinanceAccountCode,
		&transaction.FinanceAccountName,
		&transaction.FinanceAccountType,
		&transaction.TransferGroupID,
		&transaction.StudentName,
		&transaction.TrainingProgramName,
		&transaction.PersonName,
		&transaction.Description,
		&transaction.Notes,
		&transaction.PaymentMethod,
		&transaction.Amount,
		&transaction.RecordedByUser,
		&transaction.RecordedByUserName,
		&transaction.ApprovedByUserID,
		&transaction.ApprovedByUserName,
		&transaction.RecordedAt,
		&transaction.CreatedAt,
		&updatedAtRaw,
		&transaction.ApprovedAt,
		&voidedAt,
		&transaction.VoidedByUserID,
		&transaction.VoidReason,
	); err != nil {
		return nil, err
	}
	if voidedAt.Valid {
		transaction.Voided = true
		transaction.VoidedAt = voidedAt.Time
	}
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(updatedAtRaw)); err == nil {
		transaction.UpdatedAt = parsed
	} else if parsed, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", strings.TrimSpace(updatedAtRaw)); err == nil {
		transaction.UpdatedAt = parsed
	} else if parsed, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(updatedAtRaw)); err == nil {
		transaction.UpdatedAt = parsed
	} else {
		transaction.UpdatedAt = transaction.CreatedAt
	}
	transaction.MoneyIn, transaction.MoneyOut = financeAmountParts(transaction.Amount)
	transactions := []FinanceTransaction{transaction}
	if err := populateFinanceTransactionVoidStates(ctx, a.db, transactions); err != nil {
		return nil, err
	}
	return &transactions[0], nil
}

func (a *App) collectAdmissionPaymentTx(tx *sql.Tx, admission Admission, paymentMethod string, recordedByUserID int64) (int64, error) {
	admissionFee, _, err := trainingProgramFeesForAdmissionTx(
		tx,
		admission,
	)
	if err != nil {
		return 0, err
	}
	admissionFee = effectiveAdmissionFee(admission, admissionFee)

	if admissionFee <= 0 {
		return 0, ErrAdmissionFeeNotConfigured
	}

	now := time.Now().UTC()
	receiptNumber := fmt.Sprintf("ADM-%s-%06d", now.Format("20060102150405"), admission.ID)
	paymentMethod = normalizePaymentMethod(paymentMethod)
	if !validPaymentMethod(paymentMethod) {
		return 0, errors.New("invalid payment method")
	}
	divisionID, err := financeDivisionIDForEntryTx(tx, financeTransactionCreate{
		ReferenceType: "admission",
		ReferenceID:   admission.ID,
		SourceType:    "admission",
		SourceID:      admission.ID,
	})
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	description := fmt.Sprintf("Admission payment for %s", admission.FullName)
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    receiptNumber,
		ReferenceNumber:  receiptNumber,
		Category:         "admission_payment",
		TransactionType:  financeTxnTypeIncome,
		ReferenceType:    "admission",
		ReferenceID:      admission.ID,
		SourceType:       "admission",
		SourceID:         admission.ID,
		FinanceAccountID: account.ID,
		PersonName:       admission.FullName,
		Description:      description,
		PaymentMethod:    paymentMethod,
		Amount:           admissionFee,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       now,
	})
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		UPDATE admissions
		SET payment_collected = 1,
		    payment_collected_at = ?,
		    admission_payment_amount = ?,
		    finance_transaction_id = ?,
		    updated_at = ?
		WHERE id = ?
	`,
		now,
		admissionFee,
		transactionID,
		now,
		admission.ID,
	); err != nil {
		return 0, err
	}

	return transactionID, nil
}

func trainingProgramFeesForAdmissionTx(
	tx *sql.Tx,
	admission Admission,
) (float64, float64, error) {
	if len(admission.TrainingPrograms) > 0 {
		var admissionFee float64
		var monthlyFee float64
		for _, program := range admission.TrainingPrograms {
			admissionFee += program.AdmissionFee
			monthlyFee += program.MonthlyFee
		}
		return admissionFee, monthlyFee, nil
	}

	if len(admission.TrainingProgramIDs) > 0 {
		var admissionFee float64
		var monthlyFee float64
		for _, programID := range admission.TrainingProgramIDs {
			var programAdmissionFee float64
			var programMonthlyFee float64
			err := tx.QueryRow(`
				SELECT
					COALESCE(admission_fee, 0),
					COALESCE(monthly_fee, 0)
				FROM training_programs
				WHERE id = ?
			`, programID).Scan(&programAdmissionFee, &programMonthlyFee)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return 0, 0, errors.New(
						"one of the training programmes assigned to this student was not found",
					)
				}
				return 0, 0, err
			}
			admissionFee += programAdmissionFee
			monthlyFee += programMonthlyFee
		}
		return admissionFee, monthlyFee, nil
	}

	if admission.TrainingProgramID > 0 {
		return trainingProgramFeesForAdmissionTx(tx, Admission{
			TrainingProgramIDs: []int64{admission.TrainingProgramID},
		})
	}

	// Temporary backward-compatibility path for admissions created
	// before training_program_id was introduced.
	pricing, err := admissionPricingByPracticeTypeTx(
		tx,
		admission.PracticeType,
	)
	if err != nil {
		return 0, 0, err
	}

	return pricing.Price, pricing.MonthlyFee, nil
}

func populateAdmissionTrainingProgramsTx(tx *sql.Tx, admission *Admission) error {
	if admission == nil {
		return nil
	}

	rows, err := tx.Query(`
		SELECT
			tp.id,
			tp.name,
			tp.activity,
			tp.training_format,
			COALESCE(tp.admission_fee, 0),
			COALESCE(tp.monthly_fee, 0),
			COALESCE(tp.active, 0),
			COALESCE(tp.sort_order, 0),
			tp.created_at,
			tp.updated_at
		FROM admission_training_programs atp
		JOIN training_programs tp
			ON tp.id = atp.training_program_id
		WHERE atp.admission_id = ?
		ORDER BY tp.sort_order ASC, tp.name ASC, tp.id ASC
	`, admission.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var programs []TrainingProgram
	for rows.Next() {
		var program TrainingProgram
		var active int
		if err := rows.Scan(
			&program.ID,
			&program.Name,
			&program.Activity,
			&program.TrainingFormat,
			&program.AdmissionFee,
			&program.MonthlyFee,
			&active,
			&program.SortOrder,
			&program.CreatedAt,
			&program.UpdatedAt,
		); err != nil {
			return err
		}
		program.Active = active == 1
		programs = append(programs, program)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	admission.TrainingPrograms = programs
	admission.TrainingProgramIDs = admission.TrainingProgramIDs[:0]
	for _, program := range programs {
		admission.TrainingProgramIDs = append(admission.TrainingProgramIDs, program.ID)
	}
	if len(programs) > 0 {
		admission.TrainingProgramID = programs[0].ID
		admission.TrainingProgramName = programs[0].Name
		admission.TrainingProgramNames = trainingProgramNames(programs)
	} else if admission.TrainingProgramID > 0 {
		admission.TrainingProgramIDs = []int64{admission.TrainingProgramID}
		admission.TrainingProgramNames = admission.TrainingProgramName
	}

	return nil
}

func findStudentEnrollmentByIDTx(tx *sql.Tx, enrollmentID int64) (*StudentEnrollment, error) {
	row := tx.QueryRow(`
		SELECT
			se.id,
			se.admission_id,
			se.training_program_id,
			COALESCE(tp.name, ''),
			COALESCE(se.free_admission, 0),
			COALESCE(se.free_monthly_fee, 0),
			COALESCE(se.payment_collected, 0),
			se.payment_collected_at,
			COALESCE(se.admission_payment_amount, 0),
			COALESCE(se.finance_transaction_id, 0),
			COALESCE(se.active, 1),
			se.created_at,
			se.updated_at,
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(a.admission_date, ''),
			a.date_of_birth,
			a.gender,
			a.practice_type,
			COALESCE(a.address, ''),
			COALESCE(a.passport_number, ''),
			COALESCE(a.school, ''),
			COALESCE(a.guardian_name, ''),
			COALESCE(a.guardian_relationship, ''),
			COALESCE(a.guardian_contact_number, ''),
			COALESCE(a.guardian_alternative_contact_number, ''),
			COALESCE(a.medical_information, ''),
			COALESCE(a.photo_path, ''),
			COALESCE(a.qr_code_path, ''),
			COALESCE(a.qr_code_value, '')
		FROM student_enrollments se
		JOIN admissions a
			ON a.id = se.admission_id
		JOIN training_programs tp
			ON tp.id = se.training_program_id
		WHERE se.id = ?
	`, enrollmentID)

	var enrollment StudentEnrollment
	var freeAdmission int
	var freeMonthlyFee int
	var paymentCollected int
	var active int
	var paidAt sql.NullTime
	if err := row.Scan(
		&enrollment.ID,
		&enrollment.AdmissionID,
		&enrollment.TrainingProgramID,
		&enrollment.TrainingProgramName,
		&freeAdmission,
		&freeMonthlyFee,
		&paymentCollected,
		&paidAt,
		&enrollment.AdmissionPaymentAmount,
		&enrollment.FinanceTransactionID,
		&active,
		&enrollment.CreatedAt,
		&enrollment.UpdatedAt,
		&enrollment.Student.ID,
		&enrollment.Student.StudentID,
		&enrollment.Student.FullName,
		&enrollment.Student.AdmissionDate,
		&enrollment.Student.DateOfBirth,
		&enrollment.Student.Gender,
		&enrollment.Student.PracticeType,
		&enrollment.Student.Address,
		&enrollment.Student.PassportNumber,
		&enrollment.Student.School,
		&enrollment.Student.GuardianName,
		&enrollment.Student.GuardianRelationship,
		&enrollment.Student.GuardianContactNumber,
		&enrollment.Student.GuardianAlternativePhone,
		&enrollment.Student.MedicalInformation,
		&enrollment.Student.PhotoPath,
		&enrollment.Student.QRCodePath,
		&enrollment.Student.QRCodeValue,
	); err != nil {
		return nil, err
	}
	enrollment.FreeAdmission = freeAdmission == 1
	enrollment.FreeMonthlyFee = freeMonthlyFee == 1
	enrollment.AdmissionPaymentPaid = paymentCollected == 1
	enrollment.Active = active == 1
	if paidAt.Valid {
		enrollment.AdmissionPaymentPaidAt = paidAt.Time
	}
	return &enrollment, nil
}

func findFirstEnrollmentByAdmissionIDTx(tx *sql.Tx, admissionID int64) (*StudentEnrollment, error) {
	var enrollmentID int64
	if err := tx.QueryRow(`
		SELECT id
		FROM student_enrollments
		WHERE admission_id = ?
		ORDER BY id
		LIMIT 1
	`, admissionID).Scan(&enrollmentID); err != nil {
		return nil, err
	}
	return findStudentEnrollmentByIDTx(tx, enrollmentID)
}

func effectiveAdmissionFee(admission Admission, admissionFee float64) float64 {
	if admission.FreeAdmission {
		return 0
	}
	return admissionFee
}

func effectiveMonthlyFee(admission Admission, monthlyFee float64) float64 {
	if admission.FreeMonthlyFee {
		return 0
	}
	return monthlyFee
}

func admissionPricingByPracticeTypeTx(
	tx *sql.Tx,
	practiceType string,
) (*AdmissionPricing, error) {
	row := tx.QueryRow(`
		SELECT
			id,
			practice_type,
			price,
			COALESCE(monthly_fee, 0),
			created_at,
			updated_at
		FROM admission_pricing
		WHERE practice_type = ?
		LIMIT 1
	`,
		practiceType,
	)

	var pricing AdmissionPricing

	if err := row.Scan(
		&pricing.ID,
		&pricing.PracticeType,
		&pricing.Price,
		&pricing.MonthlyFee,
		&pricing.CreatedAt,
		&pricing.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New(
				"legacy admission pricing is not configured for this student",
			)
		}

		return nil, err
	}

	return &pricing, nil
}

func (a *App) collectStudentMonthlyPayment(enrollmentID int64, paymentMonth string, monthDate time.Time, paymentMethod string, recordedByUserID int64) (int64, error) {
	return a.collectStudentMonthlyPaymentAmount(enrollmentID, paymentMonth, monthDate, paymentMethod, 0, recordedByUserID)
}

func (a *App) collectStudentMonthlyPaymentAmount(enrollmentID int64, paymentMonth string, monthDate time.Time, paymentMethod string, amount float64, recordedByUserID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	enrollment, err := findStudentEnrollmentByIDTx(tx, enrollmentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		enrollment, err = findFirstEnrollmentByAdmissionIDTx(tx, enrollmentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}

	var admission *Admission
	if enrollment != nil {
		admission = &enrollment.Student
	} else {
		admission, err = a.findAdmissionByIDTx(tx, enrollmentID)
		if err != nil {
			return 0, err
		}
	}
	if admission.AdmissionDate > monthDate.AddDate(0, 1, -1).Format("2006-01-02") {
		return 0, ErrStudentNotAdmittedForMonth
	}

	totalCollected := 0.0
	if enrollment != nil {
		err = tx.QueryRow(`
			SELECT COALESCE(SUM(amount), 0)
			FROM student_monthly_payments
			WHERE enrollment_id = ? AND payment_month = ? AND COALESCE(voided, 0) = 0
		`, enrollment.ID, paymentMonth).Scan(&totalCollected)
	} else {
		err = tx.QueryRow(`
			SELECT COALESCE(SUM(amount), 0)
			FROM student_monthly_payments
			WHERE admission_id = ? AND (enrollment_id IS NULL OR enrollment_id = 0) AND payment_month = ? AND COALESCE(voided, 0) = 0
		`, admission.ID, paymentMonth).Scan(&totalCollected)
	}
	if err != nil {
		return 0, err
	}
	paymentMethod = normalizePaymentMethod(paymentMethod)
	if !validPaymentMethod(paymentMethod) {
		return 0, errors.New("invalid payment method")
	}

	var monthlyFee float64
	if enrollment != nil {
		err = tx.QueryRow(`SELECT COALESCE(monthly_fee, 0) FROM training_programs WHERE id = ?`, enrollment.TrainingProgramID).Scan(&monthlyFee)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrMonthlyFeeNotConfigured
			}
			return 0, err
		}
		if enrollment.FreeMonthlyFee {
			monthlyFee = 0
		}
	} else {
		_, monthlyFee, err = trainingProgramFeesForAdmissionTx(tx, *admission)
		if err != nil {
			return 0, err
		}
		monthlyFee = effectiveMonthlyFee(*admission, monthlyFee)
	}

	if monthlyFee <= 0 {
		return 0, ErrMonthlyFeeNotConfigured
	}
	billingStart, err := paymentBillingStartDate(enrollment, admission)
	if err != nil {
		return 0, err
	}
	monthlyFee, _ = applyFirstMonthEnrollmentDiscount(monthlyFee, billingStart, paymentMonth, monthDate.AddDate(0, 1, -1).Day())
	if enrollment != nil {
		leaves, err := listStudentEnrollmentLeavesTx(tx, enrollment.ID)
		if err != nil {
			return 0, err
		}
		leaveDays, err := overlappingLeaveDaysForMonth(leaves, monthDate)
		if err != nil {
			return 0, err
		}
		monthlyFee, _ = proratedMonthlyFee(monthlyFee, leaveDays, monthDate.AddDate(0, 1, -1).Day())
		if monthlyFee <= 0 {
			return 0, ErrStudentLeaveCoversMonth
		}
	}
	outstanding := normalizeMoney(monthlyFee - totalCollected)
	if outstanding <= 0.004 {
		return 0, ErrStudentPaymentAlreadyCollected
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, errors.New("payment amount is invalid")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		amount = outstanding
	}
	if amount > outstanding+0.004 {
		return 0, errors.New("payment amount exceeds the outstanding balance")
	}
	now := time.Now().UTC()
	divisionID, err := financeDivisionIDForEntryTx(tx, financeTransactionCreate{
		ReferenceType: "admission",
		ReferenceID:   admission.ID,
		SourceType:    "student_monthly_payment",
	})
	if err != nil {
		return 0, err
	}
	account, err := findFinanceAccountForPaymentMethodTx(tx, divisionID, paymentMethod)
	if err != nil {
		return 0, err
	}
	referenceID := admission.ID
	description := fmt.Sprintf("%s monthly payment for %s", paymentMonthLabel(paymentMonth), admission.FullName)
	if enrollment != nil {
		referenceID = enrollment.ID
		description = fmt.Sprintf("%s monthly payment for %s - %s", paymentMonthLabel(paymentMonth), admission.FullName, enrollment.TrainingProgramName)
	}
	receiptNumber := fmt.Sprintf("STU-%s-%06d-%s", strings.ReplaceAll(paymentMonth, "-", ""), referenceID, now.Format("150405"))
	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    receiptNumber,
		ReferenceNumber:  receiptNumber,
		Category:         "student_monthly_payment",
		TransactionType:  financeTxnTypeIncome,
		ReferenceType:    "admission",
		ReferenceID:      admission.ID,
		FinanceAccountID: account.ID,
		PersonName:       admission.FullName,
		Description:      description,
		PaymentMethod:    paymentMethod,
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		RecordedAt:       now,
	})
	if err != nil {
		return 0, err
	}

	result, err := tx.Exec(`
		INSERT INTO student_monthly_payments (
			admission_id, enrollment_id, payment_month, amount, payment_method, finance_transaction_id,
			collected_by_user_id, collected_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, admission.ID, nullIfZero(func() int64 {
		if enrollment != nil {
			return enrollment.ID
		}
		return 0
	}()), paymentMonth, amount, paymentMethod, transactionID, recordedByUserID, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, ErrStudentPaymentAlreadyCollected
		}
		return 0, err
	}
	paymentRowID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE finance_transactions
		SET source_type = 'student_monthly_payment',
		    source_id = ?,
		    updated_at = ?
		WHERE id = ?
	`, paymentRowID, now, transactionID); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return transactionID, nil
}

func listStudentEnrollmentLeavesTx(tx *sql.Tx, enrollmentID int64) ([]StudentEnrollmentLeave, error) {
	rows, err := tx.Query(`
		SELECT id, enrollment_id, start_date, end_date, COALESCE(reason, ''), COALESCE(active, 1), created_at, updated_at
		FROM student_enrollment_leaves
		WHERE enrollment_id = ?
		  AND COALESCE(active, 1) = 1
		ORDER BY start_date ASC, end_date ASC, id ASC
	`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var leaves []StudentEnrollmentLeave
	for rows.Next() {
		var leave StudentEnrollmentLeave
		var active int
		if err := rows.Scan(
			&leave.ID,
			&leave.EnrollmentID,
			&leave.StartDate,
			&leave.EndDate,
			&leave.Reason,
			&active,
			&leave.CreatedAt,
			&leave.UpdatedAt,
		); err != nil {
			return nil, err
		}
		leave.Active = active == 1
		leaves = append(leaves, leave)
	}
	return leaves, rows.Err()
}

func (a *App) findStudentGroupByID(groupID int64) (*StudentGroup, error) {
	row := a.db.QueryRow(`
		SELECT sg.id, sg.name, sg.code, sg.description, COALESCE(sg.training_program_id, 0), COALESCE(tp.name, ''), sg.created_at
		FROM student_groups sg
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		WHERE sg.id = ?
	`, groupID)

	var group StudentGroup
	if err := row.Scan(&group.ID, &group.Name, &group.Code, &group.Description, &group.TrainingProgramID, &group.TrainingProgramName, &group.CreatedAt); err != nil {
		return nil, err
	}
	students, err := a.listStudentsForGroup(group.ID)
	if err != nil {
		return nil, err
	}
	group.Students = students
	group.StudentCount = len(students)
	sessions, err := a.listStudentGroupSessions(group.ID)
	if err != nil {
		return nil, err
	}
	group.Sessions = sessions
	coaches, err := a.listCoachesForGroup(group.ID)
	if err != nil {
		return nil, err
	}
	group.Coaches = coaches
	group.CoachCount = len(coaches)
	return &group, nil
}

func (a *App) findSpaceScheduleByID(scheduleID int64) (*SpaceSchedule, error) {
	return findSpaceScheduleByIDQuery(a.db, scheduleID)
}

type scheduleRowQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func findSpaceScheduleByIDQuery(queryer scheduleRowQueryer, scheduleID int64) (*SpaceSchedule, error) {
	row := queryer.QueryRow(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		WHERE id = ?
	`, scheduleID)

	return scanSpaceSchedule(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanSpaceSchedule(row rowScanner) (*SpaceSchedule, error) {
	var schedule SpaceSchedule
	var statusChangedAt sql.NullTime
	if err := row.Scan(
		&schedule.ID,
		&schedule.SlotDate,
		&schedule.SlotHour,
		&schedule.EntryType,
		&schedule.Activity,
		&schedule.Quantity,
		&schedule.Title,
		&schedule.Notes,
		&schedule.Status,
		&schedule.RequesterName,
		&schedule.RequesterEmail,
		&schedule.RequesterPhone,
		&schedule.RequestedByUser,
		&schedule.ReviewNote,
		&schedule.CustomerMessage,
		&statusChangedAt,
		&schedule.StatusChangedBy,
		&schedule.StatusSource,
		&schedule.CancellationReason,
		&schedule.CancellationFinanceNote,
		&schedule.CreatedAt,
		&schedule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if statusChangedAt.Valid {
		schedule.StatusChangedAt = statusChangedAt.Time
	}
	schedule.Status = canonicalBookingStatus(schedule.Status)
	return &schedule, nil
}

func (a *App) findPricingRuleByID(pricingID int64) (*PricingRule, error) {
	row := a.db.QueryRow(`
		SELECT id, COALESCE(game_id, 0), activity, quantity, weekday_offpeak_price, weekday_peak_price,
		       weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		FROM pricing_rules
		WHERE id = ?
	`, pricingID)

	var rule PricingRule
	if err := row.Scan(
		&rule.ID,
		&rule.GameID,
		&rule.Activity,
		&rule.Quantity,
		&rule.WeekdayOffPeak,
		&rule.WeekdayPeak,
		&rule.WeekendOffPeak,
		&rule.WeekendPeak,
		&rule.CreatedAt,
		&rule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (a *App) findEventByID(eventID int64) (*Event, error) {
	row := a.db.QueryRow(`
		SELECT id, COALESCE(game_id, 0), title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		WHERE id = ?
	`, eventID)

	var event Event
	var published int
	if err := row.Scan(
		&event.ID,
		&event.GameID,
		&event.Title,
		&event.Category,
		&event.EventDate,
		&event.StartTime,
		&event.EndTime,
		&event.RegistrationDeadline,
		&event.Venue,
		&event.Summary,
		&event.ImagePath,
		&event.CTALabel,
		&event.CTALink,
		&published,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, err
	}
	event.Published = published == 1
	return &event, nil
}

func (a *App) deleteSessionByToken(token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, fmt.Sprintf("%x", hash[:]))
	return err
}

func (a *App) setFlash(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(message)),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10,
	})
}

func (a *App) consumeFlash(r *http.Request) string {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	a.clearCookieWithOptions(w, name, true)
}

func (a *App) clearCookieWithOptions(w http.ResponseWriter, name string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: httpOnly,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}
