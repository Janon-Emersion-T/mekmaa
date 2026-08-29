package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	payrollDetailTypePerStudentIncluded         = "per_student_included"
	payrollDetailTypePerStudentExcludedFullLeave = "per_student_excluded_full_month_leave"
	payrollDetailTypePerSessionOccurrence       = "per_session_occurrence"
	payrollDetailTypeSummary                    = "summary"
	payrollDetailTypeManualNote                 = "manual_note"
)

type payrollCalculatedSnapshot struct {
	Quantity      float64
	QuantityLabel string
	BaseAmount    float64
	Status        string
	Notes         string
	Details       []PayrollPaymentCalculationDetail
}

type payrollStudentCandidate struct {
	AdmissionID     int64
	StudentID       string
	FullName        string
	EnrollmentID    int64
	EnrollmentDate  string
}

type payrollSessionCandidate struct {
	OccurrenceID          int64
	OccurrenceDate        string
	GroupName             string
	TimetableSessionTitle string
}

func payrollPaymentAllowsManualQuantity(payment PayrollPayment) bool {
	switch payment.Status {
	case PayrollPaymentStatusApproved,
		PayrollPaymentStatusPaid,
		PayrollPaymentStatusVoid:
		return false
	}

	switch normalizeSalaryType(payment.CompensationType) {
	case SalaryTypeHourly, SalaryTypeDaily:
		return true
	}

	return strings.Contains(
		strings.ToLower(strings.TrimSpace(payment.QuantityLabel)),
		"manual entry required",
	)
}

func (a *App) listPayrollPaymentCalculationDetails(
	paymentID int64,
) ([]PayrollPaymentCalculationDetail, error) {
	rows, err := a.queryDB(`
		SELECT
			id,
			payroll_payment_id,
			detail_type,
			COALESCE(source_type, ''),
			COALESCE(source_id, 0),
			COALESCE(label, ''),
			COALESCE(detail_note, ''),
			COALESCE(quantity, 0),
			COALESCE(rate_snapshot, 0),
			COALESCE(amount_snapshot, 0),
			COALESCE(sort_order, 0),
			created_at
		FROM payroll_payment_calculation_details
		WHERE payroll_payment_id = ?
		ORDER BY sort_order ASC, id ASC
	`, paymentID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") ||
			strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	details := make([]PayrollPaymentCalculationDetail, 0)
	for rows.Next() {
		var detail PayrollPaymentCalculationDetail
		if err := rows.Scan(
			&detail.ID,
			&detail.PayrollPaymentID,
			&detail.DetailType,
			&detail.SourceType,
			&detail.SourceID,
			&detail.Label,
			&detail.DetailNote,
			&detail.Quantity,
			&detail.RateSnapshot,
			&detail.AmountSnapshot,
			&detail.SortOrder,
			&detail.CreatedAt,
		); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}

	return details, rows.Err()
}

func replacePayrollPaymentCalculationDetailsTx(
	a *App,
	tx *sql.Tx,
	paymentID int64,
	details []PayrollPaymentCalculationDetail,
) error {
	if _, err := a.execTxDB(tx, `
		DELETE FROM payroll_payment_calculation_details
		WHERE payroll_payment_id = ?
	`, paymentID); err != nil {
		return err
	}

	now := time.Now().UTC()
	for index := range details {
		detail := details[index]
		if strings.TrimSpace(detail.Label) == "" {
			continue
		}
		if _, err := a.execTxDB(tx, `
			INSERT INTO payroll_payment_calculation_details (
				payroll_payment_id,
				detail_type,
				source_type,
				source_id,
				label,
				detail_note,
				quantity,
				rate_snapshot,
				amount_snapshot,
				sort_order,
				created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			paymentID,
			strings.TrimSpace(detail.DetailType),
			strings.TrimSpace(detail.SourceType),
			nullIfZero(detail.SourceID),
			strings.TrimSpace(detail.Label),
			strings.TrimSpace(detail.DetailNote),
			detail.Quantity,
			detail.RateSnapshot,
			detail.AmountSnapshot,
			index,
			now,
		); err != nil {
			return err
		}
	}

	return nil
}

func payrollManualCalculationSnapshot(
	profile StaffSalaryProfile,
	quantityLabel string,
	note string,
) payrollCalculatedSnapshot {
	label := strings.TrimSpace(quantityLabel)
	message := strings.TrimSpace(note)
	if label == "" {
		label = "quantity - manual entry required"
	}

	details := []PayrollPaymentCalculationDetail{
		{
			DetailType: payrollDetailTypeManualNote,
			Label:      "Manual calculation required",
			DetailNote: message,
		},
	}

	return payrollCalculatedSnapshot{
		Quantity:      0,
		QuantityLabel: label,
		BaseAmount:    0,
		Status:        PayrollPaymentStatusDraft,
		Notes:         appendPayrollCalculationNote(profile.Notes, message),
		Details:       details,
	}
}

func appendPayrollCalculationNote(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if note == "" {
		return existing
	}
	if existing == "" {
		return note
	}
	if strings.Contains(existing, note) {
		return existing
	}
	return existing + "\n" + note
}

func payrollSummaryDetail(
	label string,
	quantity float64,
	rate float64,
	amount float64,
) PayrollPaymentCalculationDetail {
	return PayrollPaymentCalculationDetail{
		DetailType:     payrollDetailTypeSummary,
		Label:          strings.TrimSpace(label),
		Quantity:       quantity,
		RateSnapshot:   normalizeMoney(rate),
		AmountSnapshot: normalizeMoney(amount),
	}
}

func (a *App) buildPayrollCalculationSnapshot(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (payrollCalculatedSnapshot, error) {
	if !salaryProfileAppliesToPayrollPeriod(profile, periodStart, periodEnd) {
		return payrollCalculatedSnapshot{}, errors.New("salary profile does not apply to this payroll period")
	}

	switch normalizeSalaryType(profile.CompensationType) {
	case SalaryTypeMonthly:
		baseAmount := normalizeMoney(profile.Rate)
		return payrollCalculatedSnapshot{
			Quantity:      1,
			QuantityLabel: "1 month",
			BaseAmount:    baseAmount,
			Status:        PayrollPaymentStatusCalculated,
			Notes:         strings.TrimSpace(profile.Notes),
			Details: []PayrollPaymentCalculationDetail{
				payrollSummaryDetail("1 month", 1, profile.Rate, baseAmount),
			},
		}, nil

	case SalaryTypePerSession:
		return a.buildPerSessionPayrollSnapshot(profile, periodStart, periodEnd)

	case SalaryTypePerStudent:
		return a.buildPerStudentPayrollSnapshot(profile, periodStart, periodEnd)

	case SalaryTypeHourly:
		return payrollManualCalculationSnapshot(
			profile,
			"hours - manual entry required",
			"Hourly quantity requires manual approval before salary payment.",
		), nil

	case SalaryTypeDaily:
		return payrollManualCalculationSnapshot(
			profile,
			"days - manual entry required",
			"Daily quantity requires manual approval before salary payment.",
		), nil

	case SalaryTypeWeekly:
		quantity, quantityLabel, err := a.calculatePayrollProfileQuantity(profile, periodStart, periodEnd)
		if err != nil {
			return payrollCalculatedSnapshot{}, err
		}
		baseAmount := normalizeMoney(profile.Rate * quantity)
		return payrollCalculatedSnapshot{
			Quantity:      quantity,
			QuantityLabel: quantityLabel,
			BaseAmount:    baseAmount,
			Status:        PayrollPaymentStatusCalculated,
			Notes:         strings.TrimSpace(profile.Notes),
			Details: []PayrollPaymentCalculationDetail{
				payrollSummaryDetail(quantityLabel, quantity, profile.Rate, baseAmount),
			},
		}, nil

	default:
		return payrollManualCalculationSnapshot(
			profile,
			"quantity - manual entry required",
			"Unknown compensation type requires manual review before salary payment.",
		), nil
	}
}

func (a *App) buildPerStudentPayrollSnapshot(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (payrollCalculatedSnapshot, error) {
	if profile.TrainingProgramID <= 0 {
		return payrollManualCalculationSnapshot(
			profile,
			"eligible students - manual entry required",
			"Per-student automatic calculation requires a training programme on the salary profile.",
		), nil
	}

	switch normalizeSalaryStudentBasis(profile.StudentBasis) {
	case SalaryStudentBasisActiveEnrollment:
		return a.buildPerStudentActiveEnrollmentSnapshot(profile, periodStart, periodEnd)
	case SalaryStudentBasisGroupMembership:
		return a.buildPerStudentGroupMembershipSnapshot(profile, periodStart, periodEnd)
	case SalaryStudentBasisAttendance:
		return payrollManualCalculationSnapshot(
			profile,
			"attendance students - manual entry required",
			"Per-student attendance-based salary calculation is not defined for automatic payroll yet.",
		), nil
	default:
		return payrollManualCalculationSnapshot(
			profile,
			"eligible students - manual entry required",
			"Unsupported per-student calculation basis requires manual payroll review.",
		), nil
	}
}

func (a *App) buildPerStudentActiveEnrollmentSnapshot(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (payrollCalculatedSnapshot, error) {
	candidates, err := a.listPayrollAssignedStudentEnrollmentCandidates(profile)
	if err != nil {
		return payrollCalculatedSnapshot{}, err
	}

	enrollmentIDs := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.EnrollmentID > 0 {
			enrollmentIDs = append(enrollmentIDs, candidate.EnrollmentID)
		}
	}

	leavesByEnrollmentID, err := a.listStudentEnrollmentLeavesByEnrollmentIDs(enrollmentIDs)
	if err != nil {
		return payrollCalculatedSnapshot{}, err
	}

	start, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		return payrollCalculatedSnapshot{}, errors.New("invalid payroll period start")
	}
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return payrollCalculatedSnapshot{}, errors.New("invalid payroll period end")
	}

	details := make([]PayrollPaymentCalculationDetail, 0, len(candidates)+1)
	includedCount := 0.0
	for _, candidate := range candidates {
		enrollmentDate, err := time.Parse("2006-01-02", strings.TrimSpace(candidate.EnrollmentDate))
		if err != nil {
			return payrollCalculatedSnapshot{}, fmt.Errorf("student %s has an invalid enrollment date", payrollStudentLabel(candidate.StudentID, candidate.FullName))
		}
		if enrollmentDate.After(end) {
			continue
		}

		fullMonthLeave, leaveNote, err := payrollEnrollmentHasFullPeriodLeave(leavesByEnrollmentID[candidate.EnrollmentID], start, end)
		if err != nil {
			return payrollCalculatedSnapshot{}, fmt.Errorf("student %s has invalid leave data: %w", payrollStudentLabel(candidate.StudentID, candidate.FullName), err)
		}

		if fullMonthLeave {
			details = append(details, PayrollPaymentCalculationDetail{
				DetailType:     payrollDetailTypePerStudentExcludedFullLeave,
				SourceType:     "admission",
				SourceID:       candidate.AdmissionID,
				Label:          payrollStudentLabel(candidate.StudentID, candidate.FullName),
				DetailNote:     leaveNote,
				Quantity:       0,
				RateSnapshot:   normalizeMoney(profile.Rate),
				AmountSnapshot: 0,
			})
			continue
		}

		includedCount++
		details = append(details, PayrollPaymentCalculationDetail{
			DetailType:     payrollDetailTypePerStudentIncluded,
			SourceType:     "admission",
			SourceID:       candidate.AdmissionID,
			Label:          payrollStudentLabel(candidate.StudentID, candidate.FullName),
			Quantity:       1,
			RateSnapshot:   normalizeMoney(profile.Rate),
			AmountSnapshot: normalizeMoney(profile.Rate),
		})
	}

	sort.SliceStable(details, func(i, j int) bool {
		return details[i].Label < details[j].Label
	})

	baseAmount := normalizeMoney(profile.Rate * includedCount)
	details = append([]PayrollPaymentCalculationDetail{
		payrollSummaryDetail(
			fmt.Sprintf("%d eligible students", int(includedCount)),
			includedCount,
			profile.Rate,
			baseAmount,
		),
	}, details...)

	return payrollCalculatedSnapshot{
		Quantity:      includedCount,
		QuantityLabel: fmt.Sprintf("%d eligible students", int(includedCount)),
		BaseAmount:    baseAmount,
		Status:        PayrollPaymentStatusCalculated,
		Notes: appendPayrollCalculationNote(
			profile.Notes,
			"Eligibility uses active enrollment, assigned groups, and excludes only full-period active leave.",
		),
		Details: details,
	}, nil
}

func (a *App) buildPerStudentGroupMembershipSnapshot(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (payrollCalculatedSnapshot, error) {
	candidates, err := a.listPayrollAssignedGroupMembershipCandidates(profile)
	if err != nil {
		return payrollCalculatedSnapshot{}, err
	}

	details := make([]PayrollPaymentCalculationDetail, 0, len(candidates)+1)
	for _, candidate := range candidates {
		details = append(details, PayrollPaymentCalculationDetail{
			DetailType:     payrollDetailTypePerStudentIncluded,
			SourceType:     "admission",
			SourceID:       candidate.AdmissionID,
			Label:          payrollStudentLabel(candidate.StudentID, candidate.FullName),
			Quantity:       1,
			RateSnapshot:   normalizeMoney(profile.Rate),
			AmountSnapshot: normalizeMoney(profile.Rate),
		})
	}

	baseAmount := normalizeMoney(profile.Rate * float64(len(candidates)))
	details = append([]PayrollPaymentCalculationDetail{
		payrollSummaryDetail(
			fmt.Sprintf("%d group students", len(candidates)),
			float64(len(candidates)),
			profile.Rate,
			baseAmount,
		),
	}, details...)

	return payrollCalculatedSnapshot{
		Quantity:      float64(len(candidates)),
		QuantityLabel: fmt.Sprintf("%d group students", len(candidates)),
		BaseAmount:    baseAmount,
		Status:        PayrollPaymentStatusCalculated,
		Notes: appendPayrollCalculationNote(
			profile.Notes,
			"Group-membership basis counts deduplicated students from assigned groups in the scoped training programme.",
		),
		Details: details,
	}, nil
}

func (a *App) buildPerSessionPayrollSnapshot(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) (payrollCalculatedSnapshot, error) {
	sessions, err := a.listPayrollSessionOccurrencesWorked(profile, periodStart, periodEnd)
	if err != nil {
		return payrollCalculatedSnapshot{}, err
	}

	details := make([]PayrollPaymentCalculationDetail, 0, len(sessions)+1)
	for _, session := range sessions {
		label := strings.TrimSpace(session.OccurrenceDate)
		if strings.TrimSpace(session.GroupName) != "" {
			label += " · " + strings.TrimSpace(session.GroupName)
		}
		if strings.TrimSpace(session.TimetableSessionTitle) != "" {
			label += " · " + strings.TrimSpace(session.TimetableSessionTitle)
		}

		details = append(details, PayrollPaymentCalculationDetail{
			DetailType:     payrollDetailTypePerSessionOccurrence,
			SourceType:     "student_group_session_occurrence",
			SourceID:       session.OccurrenceID,
			Label:          label,
			Quantity:       1,
			RateSnapshot:   normalizeMoney(profile.Rate),
			AmountSnapshot: normalizeMoney(profile.Rate),
		})
	}

	quantity := float64(len(sessions))
	baseAmount := normalizeMoney(profile.Rate * quantity)
	details = append([]PayrollPaymentCalculationDetail{
		payrollSummaryDetail(
			fmt.Sprintf("%d sessions worked", len(sessions)),
			quantity,
			profile.Rate,
			baseAmount,
		),
	}, details...)

	return payrollCalculatedSnapshot{
		Quantity:      quantity,
		QuantityLabel: fmt.Sprintf("%d sessions worked", len(sessions)),
		BaseAmount:    baseAmount,
		Status:        PayrollPaymentStatusCalculated,
		Notes: appendPayrollCalculationNote(
			profile.Notes,
			"Per-session salary counts only completed actual occurrences explicitly recorded as worked.",
		),
		Details: details,
	}, nil
}

func payrollStudentLabel(studentID string, fullName string) string {
	studentID = strings.TrimSpace(studentID)
	fullName = strings.TrimSpace(fullName)
	switch {
	case studentID != "" && fullName != "":
		return studentID + " · " + fullName
	case fullName != "":
		return fullName
	default:
		return studentID
	}
}

func payrollEnrollmentHasFullPeriodLeave(
	leaves []StudentEnrollmentLeave,
	periodStart time.Time,
	periodEnd time.Time,
) (bool, string, error) {
	for _, leave := range leaves {
		start, err := time.Parse("2006-01-02", strings.TrimSpace(leave.StartDate))
		if err != nil {
			return false, "", errors.New("invalid leave start date")
		}
		end, err := time.Parse("2006-01-02", strings.TrimSpace(leave.EndDate))
		if err != nil {
			return false, "", errors.New("invalid leave end date")
		}
		if start.After(end) {
			return false, "", errors.New("leave start date is after end date")
		}
		if !start.After(periodStart) && !end.Before(periodEnd) {
			return true, fmt.Sprintf("Full-period leave: %s to %s", leave.StartDate, leave.EndDate), nil
		}
	}

	return false, "", nil
}

func (a *App) listPayrollAssignedStudentEnrollmentCandidates(
	profile StaffSalaryProfile,
) ([]payrollStudentCandidate, error) {
	query := `
		SELECT DISTINCT
			a.id,
			COALESCE(a.student_id, ''),
			COALESCE(a.full_name, ''),
			se.id,
			COALESCE(se.enrollment_date, '')
		FROM student_groups sg
		JOIN student_group_members sgm
			ON sgm.group_id = sg.id
		JOIN admissions a
			ON a.id = sgm.admission_id
		JOIN student_enrollments se
			ON se.admission_id = sgm.admission_id
		   AND se.training_program_id = sg.training_program_id
		JOIN training_programs tp
			ON tp.id = sg.training_program_id
		WHERE sg.training_program_id = ?
		  AND COALESCE(se.active, 1) = 1
		  AND TRIM(COALESCE(se.enrollment_date, '')) <> ''
		  AND (
			EXISTS (
				SELECT 1
				FROM student_group_staff sgs
				WHERE sgs.group_id = sg.id
				  AND sgs.user_id = ?
			)
			OR EXISTS (
				SELECT 1
				FROM student_group_coaches sgc
				WHERE sgc.group_id = sg.id
				  AND sgc.user_id = ?
			)
		  )
	`
	args := []any{profile.TrainingProgramID, profile.UserID, profile.UserID}
	if profile.DivisionID > 0 {
		query += ` AND tp.division_id = ?`
		args = append(args, profile.DivisionID)
	}
	query += ` ORDER BY LOWER(COALESCE(a.full_name, '')) ASC, LOWER(COALESCE(a.student_id, '')) ASC, a.id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]payrollStudentCandidate, 0)
	for rows.Next() {
		var candidate payrollStudentCandidate
		if err := rows.Scan(
			&candidate.AdmissionID,
			&candidate.StudentID,
			&candidate.FullName,
			&candidate.EnrollmentID,
			&candidate.EnrollmentDate,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}

	return candidates, rows.Err()
}

func (a *App) listPayrollAssignedGroupMembershipCandidates(
	profile StaffSalaryProfile,
) ([]payrollStudentCandidate, error) {
	query := `
		SELECT DISTINCT
			a.id,
			COALESCE(a.student_id, ''),
			COALESCE(a.full_name, ''),
			0,
			''
		FROM student_groups sg
		JOIN student_group_members sgm
			ON sgm.group_id = sg.id
		JOIN admissions a
			ON a.id = sgm.admission_id
		JOIN training_programs tp
			ON tp.id = sg.training_program_id
		WHERE sg.training_program_id = ?
		  AND (
			EXISTS (
				SELECT 1
				FROM student_group_staff sgs
				WHERE sgs.group_id = sg.id
				  AND sgs.user_id = ?
			)
			OR EXISTS (
				SELECT 1
				FROM student_group_coaches sgc
				WHERE sgc.group_id = sg.id
				  AND sgc.user_id = ?
			)
		  )
	`
	args := []any{profile.TrainingProgramID, profile.UserID, profile.UserID}
	if profile.DivisionID > 0 {
		query += ` AND tp.division_id = ?`
		args = append(args, profile.DivisionID)
	}
	query += ` ORDER BY LOWER(COALESCE(a.full_name, '')) ASC, LOWER(COALESCE(a.student_id, '')) ASC, a.id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]payrollStudentCandidate, 0)
	for rows.Next() {
		var candidate payrollStudentCandidate
		if err := rows.Scan(
			&candidate.AdmissionID,
			&candidate.StudentID,
			&candidate.FullName,
			&candidate.EnrollmentID,
			&candidate.EnrollmentDate,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}

	return candidates, rows.Err()
}

func (a *App) listPayrollSessionOccurrencesWorked(
	profile StaffSalaryProfile,
	periodStart string,
	periodEnd string,
) ([]payrollSessionCandidate, error) {
	query := `
		SELECT DISTINCT
			o.id,
			CAST(o.occurrence_date AS TEXT),
			COALESCE(g.name, ''),
			COALESCE(s.title, '')
		FROM student_group_session_staff ss
		JOIN student_group_session_occurrences o
			ON o.id = ss.occurrence_id
		JOIN student_groups g
			ON g.id = o.group_id
		JOIN training_programs tp
			ON tp.id = g.training_program_id
		LEFT JOIN student_group_sessions s
			ON s.id = o.timetable_session_id
		WHERE ss.user_id = ?
		  AND LOWER(COALESCE(ss.work_status, '')) = 'worked'
		  AND LOWER(COALESCE(o.status, '')) = 'completed'
		  AND CAST(o.occurrence_date AS TEXT) >= ?
		  AND CAST(o.occurrence_date AS TEXT) <= ?
	`
	args := []any{profile.UserID, periodStart, periodEnd}
	if profile.TrainingProgramID > 0 {
		query += ` AND g.training_program_id = ?`
		args = append(args, profile.TrainingProgramID)
	}
	if profile.DivisionID > 0 {
		query += ` AND tp.division_id = ?`
		args = append(args, profile.DivisionID)
	}
	query += ` ORDER BY CAST(o.occurrence_date AS TEXT) ASC, LOWER(COALESCE(g.name, '')) ASC, o.id ASC`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]payrollSessionCandidate, 0)
	for rows.Next() {
		var candidate payrollSessionCandidate
		if err := rows.Scan(
			&candidate.OccurrenceID,
			&candidate.OccurrenceDate,
			&candidate.GroupName,
			&candidate.TimetableSessionTitle,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}

	return candidates, rows.Err()
}

func (a *App) recalculatePayrollRun(
	runID int64,
	actorUserID int64,
) error {
	run, err := a.findPayrollRunByID(runID)
	if err != nil {
		return err
	}

	if run.Status == PayrollRunStatusApproved {
		return errors.New("approved payroll cannot be recalculated")
	}
	if run.Status == PayrollRunStatusClosed {
		return errors.New("closed payroll cannot be recalculated")
	}

	return a.syncPayrollRunPayments(*run, actorUserID, true)
}

func (a *App) syncPayrollRunPayments(
	run PayrollRun,
	actorUserID int64,
	allowExisting bool,
) error {
	profiles, err := a.listStaffSalaryProfiles()
	if err != nil {
		return err
	}

	applicable := make([]StaffSalaryProfile, 0, len(profiles))
	for _, profile := range profiles {
		if salaryProfileAppliesToPayrollPeriod(profile, run.PeriodStart, run.PeriodEnd) {
			applicable = append(applicable, profile)
		}
	}
	if len(applicable) == 0 {
		return errors.New("no active salary profiles apply to this payroll period")
	}

	type applicableSnapshot struct {
		Profile  StaffSalaryProfile
		Snapshot payrollCalculatedSnapshot
	}

	computed := make([]applicableSnapshot, 0, len(applicable))
	for _, profile := range applicable {
		snapshot, err := a.buildPayrollCalculationSnapshot(
			profile,
			run.PeriodStart,
			run.PeriodEnd,
		)
		if err != nil {
			return fmt.Errorf(
				"calculate salary for %s: %w",
				profile.UserName,
				err,
			)
		}

		computed = append(computed, applicableSnapshot{
			Profile:  profile,
			Snapshot: snapshot,
		})
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := a.queryTxDB(tx, `
		SELECT
			id,
			COALESCE(salary_profile_id, 0),
			status,
			COALESCE(notes, '')
		FROM payroll_payments
		WHERE payroll_run_id = ?
		ORDER BY id ASC
	`, run.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type existingPaymentState struct {
		ID              int64
		SalaryProfileID int64
		Status          string
		Notes           string
	}
	existingByProfileID := make(map[int64]existingPaymentState)
	existingCount := 0
	for rows.Next() {
		var row existingPaymentState
		if err := rows.Scan(&row.ID, &row.SalaryProfileID, &row.Status, &row.Notes); err != nil {
			return err
		}
		existingCount++
		if row.SalaryProfileID > 0 {
			existingByProfileID[row.SalaryProfileID] = row
		}
		if row.Status == PayrollPaymentStatusApproved {
			return errors.New("approved payroll payment cannot be recalculated")
		}
		if row.Status == PayrollPaymentStatusPaid {
			return errors.New("paid payroll payment cannot be recalculated")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if existingCount > 0 && !allowExisting {
		return errors.New("payroll has already been generated for this period")
	}

	now := time.Now().UTC()
	seenProfileIDs := make(map[int64]struct{}, len(applicable))

	for _, item := range computed {
		profile := item.Profile
		snapshot := item.Snapshot
		if existing, ok := existingByProfileID[profile.ID]; ok {
			if _, err := a.execTxDB(tx, `
				UPDATE payroll_payments
				SET
					user_id = ?,
					division_id = ?,
					training_program_id = ?,
					compensation_type = ?,
					rate_snapshot = ?,
					quantity = ?,
					quantity_label = ?,
					base_amount = ?,
					status = ?,
					notes = ?,
					updated_at = ?
				WHERE id = ?
			`,
				profile.UserID,
				nullIfZero(profile.DivisionID),
				nullIfZero(profile.TrainingProgramID),
				normalizeSalaryType(profile.CompensationType),
				normalizeMoney(profile.Rate),
				snapshot.Quantity,
				snapshot.QuantityLabel,
				snapshot.BaseAmount,
				snapshot.Status,
				snapshot.Notes,
				now,
				existing.ID,
			); err != nil {
				return err
			}

			if err := replacePayrollPaymentCalculationDetailsTx(a, tx, existing.ID, snapshot.Details); err != nil {
				return err
			}
			if err := recalculatePayrollPaymentTx(a, tx, existing.ID); err != nil {
				return err
			}
		} else {
			paymentID, err := a.insertAndReturnIDTx(tx, `
				INSERT INTO payroll_payments (
					payroll_run_id,
					user_id,
					salary_profile_id,
					division_id,
					training_program_id,
					compensation_type,
					rate_snapshot,
					quantity,
					quantity_label,
					base_amount,
					additions_total,
					deductions_total,
					net_amount,
					status,
					notes,
					created_at,
					updated_at
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?)
			`,
				run.ID,
				profile.UserID,
				profile.ID,
				nullIfZero(profile.DivisionID),
				nullIfZero(profile.TrainingProgramID),
				normalizeSalaryType(profile.CompensationType),
				normalizeMoney(profile.Rate),
				snapshot.Quantity,
				snapshot.QuantityLabel,
				snapshot.BaseAmount,
				snapshot.BaseAmount,
				snapshot.Status,
				snapshot.Notes,
				now,
				now,
			)
			if err != nil {
				return err
			}

			if err := replacePayrollPaymentCalculationDetailsTx(a, tx, paymentID, snapshot.Details); err != nil {
				return err
			}
		}

		seenProfileIDs[profile.ID] = struct{}{}
	}

	if allowExisting {
		for profileID, existing := range existingByProfileID {
			if _, ok := seenProfileIDs[profileID]; ok {
				continue
			}
			if existing.Status == PayrollPaymentStatusVoid {
				continue
			}
			if _, err := a.execTxDB(tx, `
				UPDATE payroll_payments
				SET
					status = ?,
					notes = ?,
					updated_at = ?
				WHERE id = ?
			`,
				PayrollPaymentStatusVoid,
				appendPayrollCalculationNote(existing.Notes, "Voided by payroll recalculation because the salary profile no longer applies to this payroll period."),
				now,
				existing.ID,
			); err != nil {
				return err
			}
		}
	}

	if _, err := a.execTxDB(tx, `
		UPDATE payroll_runs
		SET
			status = ?,
			updated_at = ?
		WHERE id = ?
	`,
		PayrollRunStatusCalculated,
		now,
		run.ID,
	); err != nil {
		return err
	}

	return tx.Commit()
}
