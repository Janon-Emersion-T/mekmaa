package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func normalizeScopedDivisionIDs(divisionIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(divisionIDs))
	normalized := make([]int64, 0, len(divisionIDs))
	for _, divisionID := range divisionIDs {
		if divisionID <= 0 {
			continue
		}
		if _, ok := seen[divisionID]; ok {
			continue
		}
		seen[divisionID] = struct{}{}
		normalized = append(normalized, divisionID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func int64ScopePlaceholders(values []int64) (string, []any) {
	normalized := normalizeScopedDivisionIDs(values)
	if len(normalized) == 0 {
		return "", nil
	}
	placeholders := make([]string, 0, len(normalized))
	args := make([]any, 0, len(normalized))
	for _, value := range normalized {
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	return strings.Join(placeholders, ", "), args
}

func scanAdmissionIdentity(scanner interface {
	Scan(dest ...any) error
}, admission *Admission) error {
	var freeAdmission int
	var freeMonthlyFee int
	var paymentCollected int
	var paymentCollectedAt sql.NullTime
	return scanner.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
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
		&admission.CreatedAt,
	)
}

func hydrateAdmissionIdentityBooleans(admission *Admission, freeAdmission, freeMonthlyFee, paymentCollected int, paymentCollectedAt sql.NullTime) {
	admission.FreeAdmission = freeAdmission == 1
	admission.FreeMonthlyFee = freeMonthlyFee == 1
	admission.PaymentCollected = paymentCollected == 1
	if paymentCollectedAt.Valid {
		admission.PaymentCollectedAt = paymentCollectedAt.Time
	}
}

func sanitizeAdmissionIdentity(admission *Admission) {
	if admission == nil {
		return
	}
	admission.FreeAdmission = false
	admission.FreeMonthlyFee = false
	admission.PaymentCollected = false
	admission.PaymentCollectedAt = time.Time{}
	admission.AdmissionPaymentAmount = 0
	admission.FinanceTransactionID = 0
	admission.PaymentVoidReason = ""
	admission.PaymentVoidedByUserID = 0
	admission.PaymentVoidedByUserName = ""
	admission.PaymentVoidedAt = time.Time{}
}

func (a *App) listAdmissionIdentities() ([]Admission, error) {
	rows, err := a.queryDB(`
		SELECT
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
			COALESCE(a.qr_code_value, ''),
			COALESCE(a.free_admission, 0),
			COALESCE(a.free_monthly_fee, 0),
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		ORDER BY
			a.admission_date DESC,
			a.created_at DESC,
			a.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admissions []Admission
	for rows.Next() {
		var admission Admission
		var freeAdmission int
		var freeMonthlyFee int
		var paymentCollected int
		var paymentCollectedAt sql.NullTime
		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.PracticeType,
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
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		hydrateAdmissionIdentityBooleans(&admission, freeAdmission, freeMonthlyFee, paymentCollected, paymentCollectedAt)
		sanitizeAdmissionIdentity(&admission)
		admissions = append(admissions, admission)
	}
	return admissions, rows.Err()
}

func (a *App) findAdmissionIdentityByID(admissionID int64) (*Admission, error) {
	row := a.queryRowDB(`
		SELECT
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
			COALESCE(a.qr_code_value, ''),
			COALESCE(a.free_admission, 0),
			COALESCE(a.free_monthly_fee, 0),
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		WHERE a.id = ?
	`, admissionID)

	var admission Admission
	var freeAdmission int
	var freeMonthlyFee int
	var paymentCollected int
	var paymentCollectedAt sql.NullTime
	if err := row.Scan(
		&admission.ID,
		&admission.StudentID,
		&admission.FullName,
		&admission.AdmissionDate,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.PracticeType,
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
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}
	hydrateAdmissionIdentityBooleans(&admission, freeAdmission, freeMonthlyFee, paymentCollected, paymentCollectedAt)
	sanitizeAdmissionIdentity(&admission)
	return &admission, nil
}

func (a *App) listAdmissionIdentitiesByIDs(admissionIDs []int64) ([]Admission, error) {
	if len(admissionIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(admissionIDs))
	args := make([]any, 0, len(admissionIDs))
	for _, admissionID := range admissionIDs {
		if admissionID <= 0 {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, admissionID)
	}
	if len(placeholders) == 0 {
		return nil, nil
	}
	rows, err := a.queryDB(`
		SELECT
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
			COALESCE(a.qr_code_value, ''),
			COALESCE(a.free_admission, 0),
			COALESCE(a.free_monthly_fee, 0),
			COALESCE(a.payment_collected, 0),
			a.payment_collected_at,
			COALESCE(a.admission_payment_amount, 0),
			COALESCE(a.finance_transaction_id, 0),
			a.created_at
		FROM admissions a
		WHERE a.id IN (`+strings.Join(placeholders, ", ")+`)
		ORDER BY LOWER(COALESCE(a.student_id, '')) ASC, a.created_at ASC, a.id ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	admissions := make([]Admission, 0, len(admissionIDs))
	for rows.Next() {
		var admission Admission
		var freeAdmission int
		var freeMonthlyFee int
		var paymentCollected int
		var paymentCollectedAt sql.NullTime
		if err := rows.Scan(
			&admission.ID,
			&admission.StudentID,
			&admission.FullName,
			&admission.AdmissionDate,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.PracticeType,
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
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		hydrateAdmissionIdentityBooleans(&admission, freeAdmission, freeMonthlyFee, paymentCollected, paymentCollectedAt)
		sanitizeAdmissionIdentity(&admission)
		admissions = append(admissions, admission)
	}
	return admissions, rows.Err()
}

func (a *App) listAdmissions() ([]Admission, error) {
	rows, err := a.queryDB(`
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
			a.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
		ORDER BY
			a.admission_date DESC,
			a.created_at DESC,
			a.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admissions []Admission

	for rows.Next() {
		var admission Admission
		var freeAdmission int
		var freeMonthlyFee int
		var paymentCollected int
		var paymentCollectedAt sql.NullTime

		if err := rows.Scan(
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

		admissions = append(admissions, admission)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := a.populateAdmissionsTrainingPrograms(admissions); err != nil {
		return nil, err
	}

	return admissions, nil
}

func (a *App) listAdmissionsFiltered(filter AdmissionsFilter) ([]Admission, int, error) {
	whereParts := make([]string, 0, 1)
	args := make([]any, 0, 8)

	if filter.Search != "" {
		searchLike := "%" + strings.ToLower(filter.Search) + "%"
		whereParts = append(whereParts, `(LOWER(COALESCE(a.student_id, '')) LIKE ? OR LOWER(COALESCE(a.full_name, '')) LIKE ? OR LOWER(COALESCE(a.guardian_name, '')) LIKE ? OR LOWER(COALESCE(a.guardian_contact_number, '')) LIKE ?)`)
		args = append(args, searchLike, searchLike, searchLike, searchLike)
	}
	divisionFilter := strings.TrimSpace(filter.Division)

	// "all" represents the shared All Mekmaa workspace and must not
	// restrict the student master directory to a specific division.
	if divisionFilter != "" && !strings.EqualFold(divisionFilter, divisionScopeAll) {
		whereParts = append(whereParts, `EXISTS (
			SELECT 1
			FROM admission_training_programs atp_filter
			JOIN training_programs tp_filter ON tp_filter.id = atp_filter.training_program_id
			JOIN divisions d_filter ON d_filter.id = tp_filter.division_id
			WHERE atp_filter.admission_id = a.id
			  AND (LOWER(d_filter.slug) = LOWER(?) OR UPPER(d_filter.code) = UPPER(?))
		)`)
		args = append(args, divisionFilter, divisionFilter)
	}
	if len(filter.DivisionIDs) > 0 {
		placeholders := make([]string, 0, len(filter.DivisionIDs))
		for _, divisionID := range filter.DivisionIDs {
			if divisionID <= 0 {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, divisionID)
		}
		if len(placeholders) > 0 {
			whereParts = append(whereParts, `EXISTS (
				SELECT 1
				FROM admission_training_programs atp_scope
				JOIN training_programs tp_scope ON tp_scope.id = atp_scope.training_program_id
				WHERE atp_scope.admission_id = a.id
				  AND tp_scope.division_id IN (`+strings.Join(placeholders, ", ")+`)
			)`)
		}
	}

	whereSQL := ""
	if len(whereParts) > 0 {
		whereSQL = " WHERE " + strings.Join(whereParts, " AND ")
	}

	var total int
	countQuery := `
		SELECT COUNT(*)
		FROM admissions a
	` + whereSQL
	if err := a.queryRowDB(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderDirection := "ASC"
	if filter.Direction == "desc" {
		orderDirection = "DESC"
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, filter.Limit, (filter.Page-1)*filter.Limit)

	rows, err := a.queryDB(`
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
			a.created_at
		FROM admissions a
		LEFT JOIN training_programs tp
			ON tp.id = a.training_program_id
	`+whereSQL+`
		ORDER BY
			LOWER(COALESCE(a.student_id, '')) `+orderDirection+`,
			a.created_at `+orderDirection+`,
			a.id `+orderDirection+`
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	admissions := make([]Admission, 0, filter.Limit)
	for rows.Next() {
		var admission Admission
		var freeAdmission int
		var freeMonthlyFee int
		var paymentCollected int
		var paymentCollectedAt sql.NullTime

		if err := rows.Scan(
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
			&admission.CreatedAt,
		); err != nil {
			return nil, 0, err
		}

		admission.FreeAdmission = freeAdmission == 1
		admission.FreeMonthlyFee = freeMonthlyFee == 1
		admission.PaymentCollected = paymentCollected > 0
		if paymentCollectedAt.Valid {
			admission.PaymentCollectedAt = paymentCollectedAt.Time
		}
		admissions = append(admissions, admission)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := a.populateAdmissionsTrainingProgramsByDivisionIDs(admissions, filter.DivisionIDs); err != nil {
		return nil, 0, err
	}

	return admissions, total, nil
}

func (a *App) populateAdmissionsTrainingPrograms(admissions []Admission) error {
	return a.populateAdmissionsTrainingProgramsByDivisionIDs(admissions, nil)
}

func (a *App) populateAdmissionsTrainingProgramsByDivisionIDs(admissions []Admission, divisionIDs []int64) error {
	if len(admissions) == 0 {
		return nil
	}

	admissionIDs := make([]int64, 0, len(admissions))
	indexByID := make(map[int64]int, len(admissions))
	for i := range admissions {
		admissionIDs = append(admissionIDs, admissions[i].ID)
		indexByID[admissions[i].ID] = i
		if admissions[i].TrainingProgramID > 0 {
			admissions[i].TrainingProgramIDs = []int64{admissions[i].TrainingProgramID}
			admissions[i].TrainingProgramNames = admissions[i].TrainingProgramName
		}
	}

	assignments, err := a.listTrainingProgramsForAdmissionsByDivisionIDs(admissionIDs, divisionIDs)
	if err != nil {
		return err
	}

	for admissionID, programs := range assignments {
		index, ok := indexByID[admissionID]
		if !ok {
			continue
		}
		admissions[index].TrainingPrograms = programs
		admissions[index].TrainingProgramIDs = make([]int64, 0, len(programs))
		admissions[index].DivisionIDs = admissions[index].DivisionIDs[:0]
		admissions[index].DivisionCodes = admissions[index].DivisionCodes[:0]
		admissions[index].Divisions = admissions[index].Divisions[:0]
		seenDivisions := map[int64]struct{}{}
		names := make([]string, 0, len(programs))
		for _, program := range programs {
			admissions[index].TrainingProgramIDs = append(admissions[index].TrainingProgramIDs, program.ID)
			names = append(names, program.Name)
			if program.DivisionID > 0 {
				if _, ok := seenDivisions[program.DivisionID]; !ok {
					seenDivisions[program.DivisionID] = struct{}{}
					admissions[index].DivisionIDs = append(admissions[index].DivisionIDs, program.DivisionID)
					admissions[index].DivisionCodes = append(admissions[index].DivisionCodes, program.DivisionCode)
					admissions[index].Divisions = append(admissions[index].Divisions, Division{
						ID:   program.DivisionID,
						Code: program.DivisionCode,
						Name: program.DivisionName,
					})
				}
			}
		}
		if len(names) > 0 {
			admissions[index].TrainingProgramNames = strings.Join(names, ", ")
		}
	}

	return nil
}

func (a *App) listTrainingProgramsForAdmissions(admissionIDs []int64) (map[int64][]TrainingProgram, error) {
	return a.listTrainingProgramsForAdmissionsByDivisionIDs(admissionIDs, nil)
}

func (a *App) listTrainingProgramsForAdmissionsByDivisionIDs(admissionIDs []int64, divisionIDs []int64) (map[int64][]TrainingProgram, error) {
	if len(admissionIDs) == 0 {
		return map[int64][]TrainingProgram{}, nil
	}

	placeholders := make([]string, 0, len(admissionIDs))
	args := make([]any, 0, len(admissionIDs))
	for _, admissionID := range admissionIDs {
		placeholders = append(placeholders, "?")
		args = append(args, admissionID)
	}

	query := fmt.Sprintf(`
		SELECT
			atp.admission_id,
			tp.id,
			COALESCE(tp.game_id, 0),
			COALESCE(tp.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
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
		LEFT JOIN divisions d
			ON d.id = tp.division_id
		WHERE atp.admission_id IN (%s)
	`, strings.Join(placeholders, ", "))
	if scopePlaceholders, scopeArgs := int64ScopePlaceholders(divisionIDs); scopePlaceholders != "" {
		query += ` AND tp.division_id IN (` + scopePlaceholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY atp.admission_id, tp.sort_order ASC, tp.name ASC, tp.id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignments := make(map[int64][]TrainingProgram, len(admissionIDs))
	for rows.Next() {
		var admissionID int64
		var program TrainingProgram
		var active int
		if err := rows.Scan(
			&admissionID,
			&program.ID,
			&program.GameID,
			&program.DivisionID,
			&program.DivisionCode,
			&program.DivisionName,
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
			return nil, err
		}
		program.Active = active == 1
		assignments[admissionID] = append(assignments[admissionID], program)
	}

	return assignments, rows.Err()
}

func (a *App) findAdmissionIdentityByIDForDivisionIDs(admissionID int64, divisionIDs []int64) (*Admission, error) {
	admission, err := a.findAdmissionIdentityByID(admissionID)
	if err != nil {
		return nil, err
	}
	scopedAdmissions := []Admission{*admission}
	if err := a.populateAdmissionsTrainingProgramsByDivisionIDs(scopedAdmissions, divisionIDs); err != nil {
		return nil, err
	}
	admission = &scopedAdmissions[0]
	if len(normalizeScopedDivisionIDs(divisionIDs)) > 0 && len(admission.DivisionIDs) == 0 {
		return nil, sql.ErrNoRows
	}
	return admission, nil
}

func (a *App) listTrainingProgramsByIDs(programIDs []int64) ([]TrainingProgram, error) {
	if len(programIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, 0, len(programIDs))
	args := make([]any, 0, len(programIDs))
	for _, programID := range programIDs {
		placeholders = append(placeholders, "?")
		args = append(args, programID)
	}

	rows, err := a.queryDB(fmt.Sprintf(`
		SELECT
			training_programs.id,
			COALESCE(training_programs.game_id, 0),
			COALESCE(training_programs.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
			training_programs.name,
			training_programs.activity,
			training_programs.training_format,
			training_programs.admission_fee,
			training_programs.monthly_fee,
			training_programs.active,
			training_programs.sort_order,
			training_programs.created_at,
			training_programs.updated_at
		FROM training_programs
		LEFT JOIN divisions d ON d.id = training_programs.division_id
		WHERE id IN (%s)
	`, strings.Join(placeholders, ", ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[int64]TrainingProgram, len(programIDs))
	for rows.Next() {
		var program TrainingProgram
		var active int
		if err := rows.Scan(
			&program.ID,
			&program.GameID,
			&program.DivisionID,
			&program.DivisionCode,
			&program.DivisionName,
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
			return nil, err
		}
		program.Active = active == 1
		byID[program.ID] = program
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	programs := make([]TrainingProgram, 0, len(programIDs))
	for _, id := range programIDs {
		program, ok := byID[id]
		if !ok {
			return nil, sql.ErrNoRows
		}
		programs = append(programs, program)
	}

	return programs, nil
}

func trainingProgramNames(programs []TrainingProgram) string {
	names := make([]string, 0, len(programs))
	for _, program := range programs {
		if strings.TrimSpace(program.Name) == "" {
			continue
		}
		names = append(names, program.Name)
	}
	return strings.Join(names, ", ")
}

func trainingProgramIDsCSV(programIDs []int64) string {
	if len(programIDs) == 0 {
		return ""
	}
	values := make([]string, 0, len(programIDs))
	for _, programID := range programIDs {
		values = append(values, strconv.FormatInt(programID, 10))
	}
	return strings.Join(values, ",")
}

func (a *App) listStudentEnrollments() ([]StudentEnrollment, error) {
	return a.listStudentEnrollmentsByDivisionIDs(nil)
}

func (a *App) listStudentEnrollmentsByDivisionIDs(divisionIDs []int64) ([]StudentEnrollment, error) {
	query := `
		SELECT
			se.id,
			se.admission_id,
			se.training_program_id,
			COALESCE(tp.name, ''),
			COALESCE(tp.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
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
		LEFT JOIN divisions d
			ON d.id = tp.division_id
	`
	args := make([]any, 0, len(divisionIDs))
	if placeholders, scopedArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` WHERE tp.division_id IN (` + placeholders + `)`
		args = append(args, scopedArgs...)
	}
	query += ` ORDER BY LOWER(a.full_name), tp.sort_order ASC, tp.name ASC, se.id ASC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var enrollments []StudentEnrollment
	for rows.Next() {
		var enrollment StudentEnrollment
		var freeAdmission int
		var freeMonthlyFee int
		var paymentCollected int
		var active int
		var paidAt sql.NullTime
		if err := rows.Scan(
			&enrollment.ID,
			&enrollment.AdmissionID,
			&enrollment.TrainingProgramID,
			&enrollment.TrainingProgramName,
			&enrollment.DivisionID,
			&enrollment.DivisionCode,
			&enrollment.DivisionName,
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
		enrollments = append(enrollments, enrollment)
	}
	return enrollments, rows.Err()
}

func (a *App) findStudentEnrollmentDivisionByID(enrollmentID int64) (int64, error) {
	var divisionID int64
	err := a.queryRowDB(`
		SELECT COALESCE(tp.division_id, 0)
		FROM student_enrollments se
		JOIN training_programs tp ON tp.id = se.training_program_id
		WHERE se.id = ?
	`, enrollmentID).Scan(&divisionID)
	return divisionID, err
}

func (a *App) findStudentEnrollmentByID(enrollmentID int64) (*StudentEnrollment, error) {
	return a.findStudentEnrollmentByIDForDivisionIDs(enrollmentID, nil)
}

func (a *App) findStudentEnrollmentByIDForDivisionIDs(enrollmentID int64, divisionIDs []int64) (*StudentEnrollment, error) {
	query := `
		SELECT
			se.id,
			se.admission_id,
			se.training_program_id,
			COALESCE(tp.name, ''),
			COALESCE(tp.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
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
		LEFT JOIN divisions d
			ON d.id = tp.division_id
		WHERE se.id = ?
	`
	args := []any{enrollmentID}
	if placeholders, scopedArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND tp.division_id IN (` + placeholders + `)`
		args = append(args, scopedArgs...)
	}
	row := a.queryRowDB(query, args...)

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
		&enrollment.DivisionID,
		&enrollment.DivisionCode,
		&enrollment.DivisionName,
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

func (a *App) listTrainingProgramsByDivisionIDs(divisionIDs []int64, includeInactive bool, excludeCorporate bool) ([]TrainingProgram, error) {
	query := `
		SELECT
			training_programs.id,
			COALESCE(training_programs.game_id, 0),
			COALESCE(training_programs.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
			training_programs.name,
			training_programs.activity,
			training_programs.training_format,
			training_programs.admission_fee,
			training_programs.monthly_fee,
			training_programs.active,
			training_programs.sort_order,
			training_programs.created_at,
			training_programs.updated_at
		FROM training_programs
		LEFT JOIN divisions d ON d.id = training_programs.division_id
		WHERE 1 = 1
	`
	args := make([]any, 0, len(divisionIDs)+1)
	if !includeInactive {
		query += ` AND training_programs.active = 1`
	}
	if excludeCorporate {
		query += ` AND UPPER(COALESCE(d.code, '')) <> UPPER(?)`
		args = append(args, divisionCodeCorporate)
	}
	if placeholders, scopedArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND training_programs.division_id IN (` + placeholders + `)`
		args = append(args, scopedArgs...)
	}
	query += ` ORDER BY training_programs.sort_order ASC, training_programs.name ASC, training_programs.id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var programs []TrainingProgram
	for rows.Next() {
		var program TrainingProgram
		var active int
		if err := rows.Scan(
			&program.ID,
			&program.GameID,
			&program.DivisionID,
			&program.DivisionCode,
			&program.DivisionName,
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
			return nil, err
		}
		program.Active = active == 1
		programs = append(programs, program)
	}
	return programs, rows.Err()
}

func (a *App) listStudentEnrollmentLeaves(enrollmentID int64) ([]StudentEnrollmentLeave, error) {
	rows, err := a.queryDB(`
		SELECT id, enrollment_id, start_date, end_date, COALESCE(reason, ''), COALESCE(active, 1), created_at, updated_at
		FROM student_enrollment_leaves
		WHERE enrollment_id = ?
		ORDER BY start_date DESC, end_date DESC, id DESC
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

func (a *App) listStudentEnrollmentLeavesByEnrollmentIDs(enrollmentIDs []int64) (map[int64][]StudentEnrollmentLeave, error) {
	result := make(map[int64][]StudentEnrollmentLeave, len(enrollmentIDs))
	if len(enrollmentIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, 0, len(enrollmentIDs))
	args := make([]any, 0, len(enrollmentIDs))
	for _, enrollmentID := range enrollmentIDs {
		if enrollmentID <= 0 {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, enrollmentID)
	}
	if len(placeholders) == 0 {
		return result, nil
	}

	rows, err := a.queryDB(fmt.Sprintf(`
		SELECT id, enrollment_id, start_date, end_date, COALESCE(reason, ''), COALESCE(active, 1), created_at, updated_at
		FROM student_enrollment_leaves
		WHERE enrollment_id IN (%s)
		  AND COALESCE(active, 1) = 1
		ORDER BY enrollment_id ASC, start_date ASC, end_date ASC, id ASC
	`, strings.Join(placeholders, ", ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
		result[leave.EnrollmentID] = append(result[leave.EnrollmentID], leave)
	}
	return result, rows.Err()
}

func (a *App) listEvents() ([]Event, error) {
	rows, err := a.queryDB(`
		SELECT id, COALESCE(game_id, 0), title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		ORDER BY event_date ASC, start_time ASC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var published int
		if err := rows.Scan(
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
		events = append(events, event)
	}
	return events, rows.Err()
}

func (a *App) listGames(includeInactive bool) ([]Game, error) {
	query := `
		SELECT id, name, activity, COALESCE(description, ''), active, sort_order, created_at, updated_at
		FROM games
	`
	args := []any{}
	if !includeInactive {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY active DESC, sort_order ASC, LOWER(name) ASC, id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var games []Game
	for rows.Next() {
		var game Game
		var active int
		if err := rows.Scan(
			&game.ID,
			&game.Name,
			&game.Activity,
			&game.Description,
			&active,
			&game.SortOrder,
			&game.CreatedAt,
			&game.UpdatedAt,
		); err != nil {
			return nil, err
		}
		game.Active = active == 1
		games = append(games, game)
	}
	return games, rows.Err()
}

func (a *App) findGameByID(gameID int64) (*Game, error) {
	row := a.queryRowDB(`
		SELECT id, name, activity, COALESCE(description, ''), active, sort_order, created_at, updated_at
		FROM games
		WHERE id = ?
	`, gameID)

	var game Game
	var active int
	if err := row.Scan(
		&game.ID,
		&game.Name,
		&game.Activity,
		&game.Description,
		&active,
		&game.SortOrder,
		&game.CreatedAt,
		&game.UpdatedAt,
	); err != nil {
		return nil, err
	}
	game.Active = active == 1
	return &game, nil
}

func (a *App) listPublishedEvents() ([]Event, error) {
	rows, err := a.queryDB(`
		SELECT id, COALESCE(game_id, 0), title, category, event_date, COALESCE(start_time, ''), COALESCE(end_time, ''),
		       COALESCE(registration_deadline, ''), venue, summary, COALESCE(image_path, ''),
		       cta_label, cta_link, published, created_at, updated_at
		FROM events
		WHERE published = 1
		ORDER BY event_date ASC, start_time ASC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var published int
		if err := rows.Scan(
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
		events = append(events, event)
	}
	return events, rows.Err()
}

func (a *App) listStudentGroups() ([]StudentGroup, error) {
	return a.listStudentGroupsByDivisionIDs(nil)
}

func (a *App) listStudentGroupsByDivisionIDs(divisionIDs []int64) ([]StudentGroup, error) {
	query := `
		SELECT sg.id, sg.name, sg.code, sg.description, COALESCE(sg.training_program_id, 0), COALESCE(tp.name, ''), sg.created_at
		FROM student_groups sg
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		WHERE 1 = 1
	`
	args := []any{}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY sg.created_at DESC, sg.id DESC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []StudentGroup
	for rows.Next() {
		var group StudentGroup
		if err := rows.Scan(&group.ID, &group.Name, &group.Code, &group.Description, &group.TrainingProgramID, &group.TrainingProgramName, &group.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range groups {
		students, err := a.listStudentsForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		coaches, err := a.listCoachesForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		assignedStaff, err := a.listStudentGroupStaff(groups[i].ID)
		if err != nil {
			return nil, err
		}

		sessions, err := a.listStudentGroupSessions(groups[i].ID)
		if err != nil {
			return nil, err
		}

		groups[i].Students = students
		groups[i].StudentCount = len(students)
		groups[i].Coaches = coaches
		groups[i].CoachCount = len(coaches)
		groups[i].AssignedStaff = assignedStaff
		groups[i].StaffCount = len(assignedStaff)
		groups[i].Sessions = sessions
	}

	return groups, nil
}

func (a *App) listStudentGroupsForCoach(userID int64) ([]StudentGroup, error) {
	return a.listStudentGroupsForCoachByDivisionIDs(userID, nil)
}

func (a *App) listStudentGroupsForCoachByDivisionIDs(userID int64, divisionIDs []int64) ([]StudentGroup, error) {
	query := `
		SELECT
			sg.id,
			sg.name,
			sg.code,
			sg.description,
			COALESCE(sg.training_program_id, 0),
			COALESCE(tp.name, ''),
			sg.created_at
		FROM student_groups sg
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		JOIN student_group_coaches sgc
			ON sgc.group_id = sg.id
		WHERE sgc.user_id = ?
	`
	args := []any{userID}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	query += ` ORDER BY sg.created_at DESC, sg.id DESC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []StudentGroup

	for rows.Next() {
		var group StudentGroup

		if err := rows.Scan(
			&group.ID,
			&group.Name,
			&group.Code,
			&group.Description,
			&group.TrainingProgramID,
			&group.TrainingProgramName,
			&group.CreatedAt,
		); err != nil {
			return nil, err
		}

		groups = append(groups, group)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range groups {
		students, err := a.listStudentsForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		coaches, err := a.listCoachesForGroup(groups[i].ID)
		if err != nil {
			return nil, err
		}

		assignedStaff, err := a.listStudentGroupStaff(groups[i].ID)
		if err != nil {
			return nil, err
		}

		sessions, err := a.listStudentGroupSessions(groups[i].ID)
		if err != nil {
			return nil, err
		}

		groups[i].Students = students
		groups[i].StudentCount = len(students)
		groups[i].Coaches = coaches
		groups[i].CoachCount = len(coaches)
		groups[i].AssignedStaff = assignedStaff
		groups[i].StaffCount = len(assignedStaff)
		groups[i].Sessions = sessions
	}

	return groups, nil
}

func (a *App) coachAssignedToGroup(
	userID int64,
	groupID int64,
) (bool, error) {
	var assigned int

	err := a.queryRowDB(`
		SELECT EXISTS (
			SELECT 1
			FROM student_group_coaches
			WHERE user_id = ?
				AND group_id = ?
		)
	`, userID, groupID).Scan(&assigned)
	if err != nil {
		return false, err
	}

	return assigned == 1, nil
}

func (a *App) findStudentGroupDivisionByID(groupID int64) (int64, error) {
	var divisionID int64
	err := a.queryRowDB(`
		SELECT COALESCE(tp.division_id, 0)
		FROM student_groups sg
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		WHERE sg.id = ?
	`, groupID).Scan(&divisionID)
	return divisionID, err
}

func (a *App) findStudentGroupByIDForDivisionIDs(groupID int64, divisionIDs []int64) (*StudentGroup, error) {
	query := `
		SELECT sg.id, sg.name, sg.code, sg.description, COALESCE(sg.training_program_id, 0), COALESCE(tp.name, ''), sg.created_at
		FROM student_groups sg
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		WHERE sg.id = ?
	`
	args := []any{groupID}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND COALESCE(tp.division_id, 0) IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	row := a.queryRowDB(query, args...)

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

	assignedStaff, err := a.listStudentGroupStaff(group.ID)
	if err != nil {
		return nil, err
	}

	group.Coaches = coaches
	group.CoachCount = len(coaches)
	group.AssignedStaff = assignedStaff
	group.StaffCount = len(assignedStaff)

	return &group, nil
}

func (a *App) listAttendanceRecords(groupID int64, sessionID int64, attendanceDate string) ([]AttendanceRecord, error) {
	query := `
		SELECT ar.id, ar.group_id, COALESCE(ar.session_id, 0), COALESCE(sgs.title, ''), ar.admission_id, ar.attendance_date, ar.status, ar.note, COALESCE(ar.recorded_by_user_id, 0), ar.recorded_at, ar.updated_at
		FROM attendance_records ar
		LEFT JOIN student_group_sessions sgs ON sgs.id = ar.session_id
		WHERE ar.group_id = ? AND COALESCE(ar.session_id, 0) = ? AND ar.attendance_date = ?
		ORDER BY ar.admission_id ASC, ar.id ASC
	`
	args := []any{groupID, sessionID, attendanceDate}

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []AttendanceRecord
	for rows.Next() {
		var record AttendanceRecord
		if err := rows.Scan(
			&record.ID,
			&record.GroupID,
			&record.SessionID,
			&record.SessionTitle,
			&record.AdmissionID,
			&record.AttendanceDate,
			&record.Status,
			&record.Note,
			&record.RecordedByUserID,
			&record.RecordedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (a *App) listRecentAttendanceDates(groupID int64, sessionID int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 8
	}
	query := `
		SELECT DISTINCT attendance_date
		FROM attendance_records
		WHERE group_id = ? AND COALESCE(session_id, 0) = ?
		ORDER BY attendance_date DESC
		LIMIT ?
	`
	args := []any{groupID, sessionID, limit}

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	return dates, rows.Err()
}

func (a *App) getAttendanceSummary(groupID int64, sessionID int64) (AttendanceSummary, error) {
	var summary AttendanceSummary
	query := `
		SELECT
			COUNT(DISTINCT attendance_date || ':' || COALESCE(CAST(session_id AS TEXT), '0')),
			COALESCE(SUM(CASE WHEN status = 'present' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'absent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'late' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'excused' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM attendance_records
		WHERE group_id = ? AND COALESCE(session_id, 0) = ?
	`
	err := a.queryRowDB(query, groupID, sessionID).Scan(
		&summary.SessionCount,
		&summary.PresentCount,
		&summary.AbsentCount,
		&summary.LateCount,
		&summary.ExcusedCount,
		&summary.TotalEntries,
	)
	if err != nil {
		return AttendanceSummary{}, err
	}

	return summary, nil
}

func (a *App) listAttendanceLimitWarnings(groupID int64, sessionID int64, attendanceDate string, limit int) ([]AttendanceLimitWarning, error) {
	if limit <= 0 {
		limit = 8
	}
	query := `
		SELECT
			a.id,
			a.student_id,
			a.full_name,
			COALESCE(tp.name, ''),
			monthly_sessions.session_count
		FROM attendance_records ar
		JOIN admissions a ON a.id = ar.admission_id
		JOIN student_groups sg ON sg.id = ar.group_id
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		JOIN (
			SELECT ar2.admission_id, COALESCE(sg2.training_program_id, 0) AS training_program_id, COUNT(*) AS session_count
			FROM attendance_records ar2
			JOIN student_groups sg2 ON sg2.id = ar2.group_id
			WHERE SUBSTR(ar2.attendance_date, 1, 7) = SUBSTR(?, 1, 7)
			  AND ar2.status IN ('present', 'late')
			GROUP BY ar2.admission_id, COALESCE(sg2.training_program_id, 0)
		) AS monthly_sessions ON monthly_sessions.admission_id = ar.admission_id
		                       AND monthly_sessions.training_program_id = COALESCE(sg.training_program_id, 0)
		WHERE ar.group_id = ?
		  AND COALESCE(ar.session_id, 0) = ?
		  AND ar.attendance_date = ?
		  AND ar.status IN ('present', 'late')
		  AND monthly_sessions.session_count > ?
		ORDER BY monthly_sessions.session_count DESC, LOWER(a.full_name), a.id
	`
	args := []any{attendanceDate, groupID, sessionID, attendanceDate, limit}

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var warnings []AttendanceLimitWarning
	for rows.Next() {
		var warning AttendanceLimitWarning
		warning.Limit = limit
		if err := rows.Scan(&warning.AdmissionID, &warning.StudentID, &warning.FullName, &warning.TrainingProgramName, &warning.SessionCount); err != nil {
			return nil, err
		}
		warnings = append(warnings, warning)
	}
	return warnings, rows.Err()
}

func (a *App) listStudentGroupSessions(groupID int64) ([]StudentGroupSession, error) {
	rows, err := a.queryDB(`
		SELECT id, group_id, title, day_of_week, start_time, end_time, COALESCE(active, 1), created_at, updated_at
		FROM student_group_sessions
		WHERE group_id = ?
		ORDER BY
			CASE day_of_week
				WHEN 'monday' THEN 1
				WHEN 'tuesday' THEN 2
				WHEN 'wednesday' THEN 3
				WHEN 'thursday' THEN 4
				WHEN 'friday' THEN 5
				WHEN 'saturday' THEN 6
				WHEN 'sunday' THEN 7
				ELSE 8
			END,
			start_time,
			id
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []StudentGroupSession
	for rows.Next() {
		var session StudentGroupSession
		var active int
		if err := rows.Scan(&session.ID, &session.GroupID, &session.Title, &session.DayOfWeek, &session.StartTime, &session.EndTime, &active, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		session.Active = active == 1
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (a *App) findStudentGroupSessionByID(sessionID int64) (*StudentGroupSession, error) {
	row := a.queryRowDB(`
		SELECT id, group_id, title, day_of_week, start_time, end_time, COALESCE(active, 1), created_at, updated_at
		FROM student_group_sessions
		WHERE id = ?
	`, sessionID)

	var session StudentGroupSession
	var active int
	if err := row.Scan(&session.ID, &session.GroupID, &session.Title, &session.DayOfWeek, &session.StartTime, &session.EndTime, &active, &session.CreatedAt, &session.UpdatedAt); err != nil {
		return nil, err
	}
	session.Active = active == 1
	return &session, nil
}

func (a *App) listCourts(includeInactive bool) ([]Court, error) {
	query := `
		SELECT
			id,
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM courts
	`

	if !includeInactive {
		query += ` WHERE active = 1`
	}

	query += ` ORDER BY sort_order, name, id`

	rows, err := a.queryDB(query)
	if err != nil {
		return nil, err
	}

	var courts []Court

	for rows.Next() {
		var court Court

		if err := rows.Scan(
			&court.ID,
			&court.Name,
			&court.Code,
			&court.Description,
			&court.Active,
			&court.SortOrder,
			&court.CreatedAt,
			&court.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		courts = append(courts, court)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range courts {
		layouts, err := a.listCourtLayouts(
			courts[i].ID,
			includeInactive,
		)
		if err != nil {
			return nil, err
		}

		courts[i].Layouts = layouts
	}

	return courts, nil
}

func (a *App) findCourtByID(courtID int64) (*Court, error) {
	var court Court

	err := a.queryRowDB(`
		SELECT
			id,
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM courts
		WHERE id = ?
	`, courtID).Scan(
		&court.ID,
		&court.Name,
		&court.Code,
		&court.Description,
		&court.Active,
		&court.SortOrder,
		&court.CreatedAt,
		&court.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	activities, err := a.listCourtActivities(court.ID, true)
	if err != nil {
		return nil, err
	}

	layouts, err := a.listCourtLayouts(court.ID, true)
	if err != nil {
		return nil, err
	}

	court.Activities = activities
	court.Layouts = layouts

	return &court, nil
}

func (a *App) listCourtActivities(
	courtID int64,
	includeInactive bool,
) ([]CourtActivity, error) {
	query := `
		SELECT
			id,
			court_id,
			COALESCE(game_id, 0),
			activity,
			display_name,
			max_quantity,
			auto_accept,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_activities
		WHERE court_id = ?
	`

	if !includeInactive {
		query += ` AND active = 1`
	}

	query += ` ORDER BY sort_order, display_name, id`

	rows, err := a.queryDB(query, courtID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []CourtActivity

	for rows.Next() {
		var activity CourtActivity
		var autoAccept int

		err := rows.Scan(
			&activity.ID,
			&activity.CourtID,
			&activity.GameID,
			&activity.Activity,
			&activity.DisplayName,
			&activity.MaxQuantity,
			&autoAccept,
			&activity.Active,
			&activity.SortOrder,
			&activity.CreatedAt,
			&activity.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		activity.AutoAccept = autoAccept == 1

		activities = append(activities, activity)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return activities, nil
}

func (a *App) findCourtActivityByID(
	activityID int64,
) (*CourtActivity, error) {
	var activity CourtActivity
	var autoAccept int

	err := a.queryRowDB(`
		SELECT
			id,
			court_id,
			COALESCE(game_id, 0),
			activity,
			display_name,
			max_quantity,
			auto_accept,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_activities
		WHERE id = ?
	`, activityID).Scan(
		&activity.ID,
		&activity.CourtID,
		&activity.GameID,
		&activity.Activity,
		&activity.DisplayName,
		&activity.MaxQuantity,
		&autoAccept,
		&activity.Active,
		&activity.SortOrder,
		&activity.CreatedAt,
		&activity.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	activity.AutoAccept = autoAccept == 1
	return &activity, nil
}

func (a *App) listCourtLayouts(
	courtID int64,
	includeInactive bool,
) ([]CourtLayout, error) {
	query := `
		SELECT
			id,
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_layouts
		WHERE court_id = ?
	`

	if !includeInactive {
		query += ` AND active = 1`
	}

	query += ` ORDER BY sort_order, name, id`

	rows, err := a.queryDB(query, courtID)
	if err != nil {
		return nil, err
	}

	var layouts []CourtLayout

	for rows.Next() {
		var layout CourtLayout

		if err := rows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		layouts = append(layouts, layout)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range layouts {
		items, err := a.listCourtLayoutItems(layouts[i].ID)
		if err != nil {
			return nil, err
		}

		layouts[i].Items = items
	}

	return layouts, nil
}

func (a *App) findCourtLayoutByID(layoutID int64) (*CourtLayout, error) {
	var layout CourtLayout

	err := a.queryRowDB(`
		SELECT
			id,
			court_id,
			name,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		FROM court_layouts
		WHERE id = ?
	`, layoutID).Scan(
		&layout.ID,
		&layout.CourtID,
		&layout.Name,
		&layout.Description,
		&layout.Active,
		&layout.SortOrder,
		&layout.CreatedAt,
		&layout.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	items, err := a.listCourtLayoutItems(layout.ID)
	if err != nil {
		return nil, err
	}

	layout.Items = items

	return &layout, nil
}

func (a *App) listCourtLayoutItems(
	layoutID int64,
) ([]CourtLayoutItem, error) {
	rows, err := a.queryDB(`
		SELECT
			cli.id,
			cli.layout_id,
			cli.activity,
			COALESCE(ca.display_name, cli.activity),
			cli.quantity
		FROM court_layout_items cli
		LEFT JOIN court_layouts cl
			ON cl.id = cli.layout_id
		LEFT JOIN court_activities ca
			ON ca.court_id = cl.court_id
			AND ca.activity = cli.activity
		WHERE cli.layout_id = ?
		ORDER BY
			COALESCE(ca.sort_order, 9999),
			COALESCE(ca.display_name, cli.activity),
			cli.id
	`, layoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CourtLayoutItem

	for rows.Next() {
		var item CourtLayoutItem

		err := rows.Scan(
			&item.ID,
			&item.LayoutID,
			&item.Activity,
			&item.DisplayName,
			&item.Quantity,
		)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (a *App) listCourtClosures(
	courtID int64,
	includeInactive bool,
) ([]CourtClosure, error) {
	query := `
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.court_id = ?
	`

	if !includeInactive {
		query += ` AND cc.active = 1`
	}

	query += `
		ORDER BY
			cc.closure_date DESC,
			cc.start_hour,
			cc.id DESC
	`

	rows, err := a.queryDB(
		query,
		courtID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure

	for rows.Next() {
		var closure CourtClosure

		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}

		closures = append(
			closures,
			closure,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return closures, nil
}

func (a *App) findCourtClosureByID(
	closureID int64,
) (*CourtClosure, error) {
	var closure CourtClosure

	err := a.queryRowDB(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.id = ?
	`, closureID).Scan(
		&closure.ID,
		&closure.CourtID,
		&closure.CourtName,
		&closure.ClosureDate,
		&closure.StartHour,
		&closure.EndHour,
		&closure.Activity,
		&closure.Title,
		&closure.Reason,
		&closure.Active,
		&closure.CreatedAt,
		&closure.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &closure, nil
}

func (a *App) listActiveCourtClosures() (
	[]CourtClosure,
	error,
) {
	return listActiveCourtClosuresQuery(a.db)
}

type sqlQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func listActiveCourtClosuresQuery(queryer sqlQueryer) (
	[]CourtClosure,
	error,
) {
	rows, err := queryer.Query(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.active = 1
		  AND c.active = 1
		ORDER BY
			cc.closure_date,
			cc.start_hour,
			cc.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure
	for rows.Next() {
		var closure CourtClosure
		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}
		closures = append(closures, closure)
	}

	return closures, rows.Err()
}

func activeBookingConfigurationQuery(
	queryer sqlQueryer,
) ([]CourtActivity, []CourtLayout, error) {
	activitiesRows, err := queryer.Query(`
		SELECT
			ca.id,
			ca.court_id,
			COALESCE(ca.game_id, 0),
			ca.activity,
			ca.display_name,
			ca.max_quantity,
			ca.auto_accept,
			ca.active,
			ca.sort_order,
			ca.created_at,
			ca.updated_at
		FROM court_activities ca
		JOIN courts c
			ON c.id = ca.court_id
		WHERE ca.active = 1
		  AND c.active = 1
		ORDER BY
			ca.sort_order,
			ca.display_name,
			ca.id
	`)
	if err != nil {
		return nil, nil, err
	}
	defer activitiesRows.Close()

	var activities []CourtActivity
	for activitiesRows.Next() {
		var activity CourtActivity
		var autoAccept int
		if err := activitiesRows.Scan(
			&activity.ID,
			&activity.CourtID,
			&activity.GameID,
			&activity.Activity,
			&activity.DisplayName,
			&activity.MaxQuantity,
			&autoAccept,
			&activity.Active,
			&activity.SortOrder,
			&activity.CreatedAt,
			&activity.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		activity.AutoAccept = autoAccept == 1
		activities = append(activities, activity)
	}
	if err := activitiesRows.Err(); err != nil {
		return nil, nil, err
	}

	layoutRows, err := queryer.Query(`
		SELECT
			cl.id,
			cl.court_id,
			cl.name,
			cl.description,
			cl.active,
			cl.sort_order,
			cl.created_at,
			cl.updated_at,
			COALESCE(cli.id, 0),
			COALESCE(cli.activity, ''),
			COALESCE(ca.display_name, cli.activity, ''),
			COALESCE(cli.quantity, 0)
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		LEFT JOIN court_layout_items cli
			ON cli.layout_id = cl.id
		LEFT JOIN court_activities ca
			ON ca.court_id = cl.court_id
			AND ca.activity = cli.activity
		WHERE cl.active = 1
		  AND c.active = 1
		ORDER BY
			cl.sort_order,
			cl.name,
			cl.id,
			COALESCE(ca.sort_order, 9999),
			COALESCE(ca.display_name, cli.activity),
			cli.id
	`)
	if err != nil {
		return nil, nil, err
	}
	defer layoutRows.Close()

	layoutMap := make(map[int64]*CourtLayout)
	layoutOrder := make([]int64, 0)

	for layoutRows.Next() {
		var (
			layout          CourtLayout
			itemID          int64
			itemActivity    string
			itemDisplayName string
			itemQuantity    int
		)

		if err := layoutRows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
			&itemID,
			&itemActivity,
			&itemDisplayName,
			&itemQuantity,
		); err != nil {
			return nil, nil, err
		}

		existing := layoutMap[layout.ID]
		if existing == nil {
			layoutCopy := layout
			layoutMap[layout.ID] = &layoutCopy
			layoutOrder = append(layoutOrder, layout.ID)
			existing = &layoutCopy
		}

		if itemID > 0 {
			existing.Items = append(existing.Items, CourtLayoutItem{
				ID:          itemID,
				LayoutID:    layout.ID,
				Activity:    itemActivity,
				DisplayName: itemDisplayName,
				Quantity:    itemQuantity,
			})
		}
	}
	if err := layoutRows.Err(); err != nil {
		return nil, nil, err
	}

	layouts := make([]CourtLayout, 0, len(layoutOrder))
	for _, layoutID := range layoutOrder {
		layouts = append(layouts, *layoutMap[layoutID])
	}

	if len(layouts) == 0 {
		return nil, nil, errors.New("no active court layouts are configured")
	}

	return activities, layouts, nil
}

func (a *App) activeCourtClosuresForDate(
	closureDate string,
) ([]CourtClosure, error) {
	rows, err := a.queryDB(`
		SELECT
			cc.id,
			cc.court_id,
			c.name,
			cc.closure_date,
			cc.start_hour,
			cc.end_hour,
			cc.activity,
			cc.title,
			cc.reason,
			cc.active,
			cc.created_at,
			cc.updated_at
		FROM court_closures cc
		JOIN courts c
			ON c.id = cc.court_id
		WHERE cc.active = 1
		  AND c.active = 1
		  AND cc.closure_date = ?
		ORDER BY
			c.sort_order,
			cc.start_hour,
			cc.id
	`, closureDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var closures []CourtClosure

	for rows.Next() {
		var closure CourtClosure

		if err := rows.Scan(
			&closure.ID,
			&closure.CourtID,
			&closure.CourtName,
			&closure.ClosureDate,
			&closure.StartHour,
			&closure.EndHour,
			&closure.Activity,
			&closure.Title,
			&closure.Reason,
			&closure.Active,
			&closure.CreatedAt,
			&closure.UpdatedAt,
		); err != nil {
			return nil, err
		}

		closures = append(
			closures,
			closure,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return closures, nil
}

func (a *App) createCourtClosure(
	closure CourtClosure,
) (int64, error) {
	activities, err := a.listCourtActivities(
		closure.CourtID,
		false,
	)
	if err != nil {
		return 0, err
	}

	if err := validateCourtClosure(
		closure,
		activities,
	); err != nil {
		return 0, err
	}

	now := time.Now().UTC()

	result, err := a.execDB(`
		INSERT INTO court_closures (
			court_id,
			closure_date,
			start_hour,
			end_hour,
			activity,
			title,
			reason,
			active,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		closure.CourtID,
		closure.ClosureDate,
		closure.StartHour,
		closure.EndHour,
		closure.Activity,
		closure.Title,
		closure.Reason,
		closure.Active,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

func (a *App) updateCourtClosure(
	closure CourtClosure,
) error {
	if closure.ID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	activities, err := a.listCourtActivities(
		closure.CourtID,
		false,
	)
	if err != nil {
		return err
	}

	if err := validateCourtClosure(
		closure,
		activities,
	); err != nil {
		return err
	}

	result, err := a.execDB(`
		UPDATE court_closures
		SET
			court_id = ?,
			closure_date = ?,
			start_hour = ?,
			end_hour = ?,
			activity = ?,
			title = ?,
			reason = ?,
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		closure.CourtID,
		closure.ClosureDate,
		closure.StartHour,
		closure.EndHour,
		closure.Activity,
		closure.Title,
		closure.Reason,
		closure.Active,
		time.Now().UTC(),
		closure.ID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) toggleCourtClosure(
	closureID int64,
) error {
	if closureID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	var active bool

	if err := a.queryRowDB(`
		SELECT active
		FROM court_closures
		WHERE id = ?
	`, closureID).Scan(&active); err != nil {
		return err
	}

	_, err := a.execDB(`
		UPDATE court_closures
		SET
			active = ?,
			updated_at = ?
		WHERE id = ?
	`,
		!active,
		time.Now().UTC(),
		closureID,
	)

	return err
}

func (a *App) deleteCourtClosure(
	closureID int64,
) error {
	if closureID <= 0 {
		return errors.New(
			"valid court closure is required",
		)
	}

	result, err := a.execDB(`
		DELETE FROM court_closures
		WHERE id = ?
	`, closureID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) listActiveCourtLayouts() ([]CourtLayout, error) {
	rows, err := a.queryDB(`
		SELECT
			cl.id,
			cl.court_id,
			cl.name,
			cl.description,
			cl.active,
			cl.sort_order,
			cl.created_at,
			cl.updated_at
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		WHERE cl.active = 1
		  AND c.active = 1
		ORDER BY
			c.sort_order,
			cl.sort_order,
			cl.name,
			cl.id
	`)
	if err != nil {
		return nil, err
	}

	var layouts []CourtLayout

	for rows.Next() {
		var layout CourtLayout

		if err := rows.Scan(
			&layout.ID,
			&layout.CourtID,
			&layout.Name,
			&layout.Description,
			&layout.Active,
			&layout.SortOrder,
			&layout.CreatedAt,
			&layout.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}

		layouts = append(layouts, layout)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range layouts {
		items, err := a.listCourtLayoutItems(layouts[i].ID)
		if err != nil {
			return nil, err
		}

		layouts[i].Items = items
	}

	return layouts, nil
}

func (a *App) listSpaceSchedules() ([]SpaceSchedule, error) {
	rows, err := a.queryDB(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		var statusChangedAt sql.NullTime
		if err := rows.Scan(
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
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (a *App) listActiveSpaceSchedulesBetween(
	startDate string,
	endDate string,
) ([]SpaceSchedule, error) {
	rows, err := a.queryDB(`
		SELECT id, slot_date, slot_hour, entry_type, activity, quantity, title, notes, status,
		       requester_name, requester_email, requester_phone, COALESCE(requested_by_user_id, 0), review_note,
		       COALESCE(customer_message, ''),
		       status_changed_at, COALESCE(status_changed_by_user_id, 0), COALESCE(status_change_source, ''),
		       COALESCE(cancellation_reason, ''), COALESCE(cancellation_finance_note, ''),
		       created_at, updated_at
		FROM space_schedules
		WHERE slot_date >= ?
		  AND slot_date <= ?
		ORDER BY slot_date ASC, slot_hour ASC, entry_type ASC, id ASC
	`, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []SpaceSchedule
	for rows.Next() {
		var schedule SpaceSchedule
		var statusChangedAt sql.NullTime
		if err := rows.Scan(
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
		schedules = append(schedules, schedule)
	}

	return schedules, rows.Err()
}

func (a *App) listPricingRules() ([]PricingRule, error) {
	return listPricingRulesQuery(a.db)
}

func listPricingRulesQuery(queryer sqlQueryer) ([]PricingRule, error) {
	rows, err := queryer.Query(`
		SELECT id, COALESCE(game_id, 0), activity, quantity, weekday_offpeak_price, weekday_peak_price,
		       weekend_offpeak_price, weekend_peak_price, created_at, updated_at
		FROM pricing_rules
		ORDER BY activity ASC, quantity ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []PricingRule
	for rows.Next() {
		var rule PricingRule
		if err := rows.Scan(
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
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (a *App) listTrainingPrograms(includeInactive bool) ([]TrainingProgram, error) {
	return a.listTrainingProgramsByDivisionIDs(nil, includeInactive, false)
}

func (a *App) findTrainingProgramByID(programID int64) (*TrainingProgram, error) {
	row := a.queryRowDB(`
		SELECT
			training_programs.id,
			COALESCE(training_programs.game_id, 0),
			COALESCE(training_programs.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
			training_programs.name,
			training_programs.activity,
			training_programs.training_format,
			training_programs.admission_fee,
			training_programs.monthly_fee,
			training_programs.active,
			training_programs.sort_order,
			training_programs.created_at,
			training_programs.updated_at
		FROM training_programs
		LEFT JOIN divisions d ON d.id = training_programs.division_id
		WHERE training_programs.id = ?
	`, programID)

	var program TrainingProgram
	var active int

	if err := row.Scan(
		&program.ID,
		&program.GameID,
		&program.DivisionID,
		&program.DivisionCode,
		&program.DivisionName,
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
		return nil, err
	}

	program.Active = active == 1

	return &program, nil
}

func (a *App) createTrainingProgram(program TrainingProgram) (int64, error) {
	now := time.Now().UTC()

	result, err := a.execDB(`
		INSERT INTO training_programs (
			game_id,
			division_id,
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		program.GameID,
		nullIfZero(program.DivisionID),
		program.Name,
		program.Activity,
		program.TrainingFormat,
		program.AdmissionFee,
		program.MonthlyFee,
		boolToInt(program.Active),
		program.SortOrder,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	programID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return programID, nil
}

func (a *App) updateTrainingProgram(program TrainingProgram) error {
	_, err := a.execDB(`
		UPDATE training_programs
		SET
			game_id = ?,
			division_id = ?,
			name = ?,
			activity = ?,
			training_format = ?,
			admission_fee = ?,
			monthly_fee = ?,
			active = ?,
			sort_order = ?,
			updated_at = ?
		WHERE id = ?
	`,
		program.GameID,
		nullIfZero(program.DivisionID),
		program.Name,
		program.Activity,
		program.TrainingFormat,
		program.AdmissionFee,
		program.MonthlyFee,
		boolToInt(program.Active),
		program.SortOrder,
		time.Now().UTC(),
		program.ID,
	)

	return err
}

func (a *App) setTrainingProgramActive(programID int64, active bool) error {
	result, err := a.execDB(`
		UPDATE training_programs
		SET active = ?, updated_at = ?
		WHERE id = ?
	`,
		boolToInt(active),
		time.Now().UTC(),
		programID,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (a *App) deleteTrainingProgram(programID int64) error {
	var admissionCount int

	err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM admissions
		WHERE training_program_id = ?
	`, programID).Scan(&admissionCount)
	if err != nil {
		return err
	}

	if admissionCount > 0 {
		return errors.New(
			"this training programme is assigned to students and cannot be deleted",
		)
	}

	result, err := a.execDB(`
		DELETE FROM training_programs
		WHERE id = ?
	`, programID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}
