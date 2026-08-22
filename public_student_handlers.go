package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func normalizePublicStudentLookup(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	marker := "mekmaa.com/student-id/"
	if index := strings.Index(lower, marker); index >= 0 {
		raw = raw[index+len(marker):]
	}
	raw = strings.TrimSpace(strings.Trim(raw, "/"))
	if strings.Contains(raw, "?") {
		if parsed, err := url.Parse(raw); err == nil {
			if candidate := strings.TrimSpace(parsed.Query().Get("student_id")); candidate != "" {
				raw = candidate
			}
		}
	}
	return strings.ToUpper(strings.TrimSpace(raw))
}

func (a *App) findAdmissionByPublicLookup(lookup string) (*Admission, error) {
	lookup = normalizePublicStudentLookup(lookup)
	if lookup == "" {
		return nil, sql.ErrNoRows
	}

	var admissionID int64
	err := a.queryRowDB(`
		SELECT id
		FROM admissions
		WHERE UPPER(TRIM(student_id)) = UPPER(TRIM(?))
		   OR UPPER(TRIM(COALESCE(qr_code_value, ''))) = UPPER(TRIM(?))
		ORDER BY id DESC
		LIMIT 1
	`, lookup, lookup).Scan(&admissionID)
	if err != nil {
		return nil, err
	}

	return a.findAdmissionByID(admissionID)
}

func (a *App) listStudentEnrollmentsByAdmissionID(admissionID int64) ([]StudentEnrollment, error) {
	rows, err := a.queryDB(`
		SELECT
			se.id,
			se.admission_id,
			se.training_program_id,
			COALESCE(CAST(se.enrollment_date AS TEXT), ''),
			COALESCE(tp.name, ''),
			COALESCE(tp.division_id, 0),
			COALESCE(d.code, ''),
			COALESCE(d.name, ''),
			COALESCE(se.free_admission, 0),
			COALESCE(se.free_monthly_fee, 0),
			COALESCE(se.discounted_monthly_fee, 0),
			COALESCE(se.payment_collected, 0),
			se.payment_collected_at,
			COALESCE(se.admission_payment_amount, 0),
			COALESCE(se.finance_transaction_id, 0),
			COALESCE(se.payment_void_reason, ''),
			COALESCE(se.payment_voided_by_user_id, 0),
			COALESCE(vu.name, ''),
			se.payment_voided_at,
			COALESCE(se.active, 1),
			se.created_at,
			se.updated_at
		FROM student_enrollments se
		JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN divisions d ON d.id = tp.division_id
		LEFT JOIN users vu ON vu.id = se.payment_voided_by_user_id
		WHERE se.admission_id = ?
		ORDER BY COALESCE(se.active, 1) DESC, tp.sort_order ASC, tp.name ASC, se.id ASC
	`, admissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	enrollments := make([]StudentEnrollment, 0)
	for rows.Next() {
		var enrollment StudentEnrollment
		var freeAdmission int
		var freeMonthlyFee int
		var discountedMonthlyFee float64
		var paymentCollected int
		var active int
		var paidAt sql.NullTime
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&enrollment.ID,
			&enrollment.AdmissionID,
			&enrollment.TrainingProgramID,
			&enrollment.EnrollmentDate,
			&enrollment.TrainingProgramName,
			&enrollment.DivisionID,
			&enrollment.DivisionCode,
			&enrollment.DivisionName,
			&freeAdmission,
			&freeMonthlyFee,
			&discountedMonthlyFee,
			&paymentCollected,
			&paidAt,
			&enrollment.AdmissionPaymentAmount,
			&enrollment.FinanceTransactionID,
			&enrollment.PaymentVoidReason,
			&enrollment.PaymentVoidedByUserID,
			&enrollment.PaymentVoidedByUserName,
			&voidedAt,
			&active,
			&enrollment.CreatedAt,
			&enrollment.UpdatedAt,
		); err != nil {
			return nil, err
		}
		enrollment.FreeAdmission = freeAdmission == 1
		enrollment.FreeMonthlyFee = freeMonthlyFee == 1
		enrollment.DiscountedMonthlyFee = normalizeMoney(discountedMonthlyFee)
		enrollment.AdmissionPaymentPaid = paymentCollected == 1
		enrollment.Active = active == 1
		if paidAt.Valid {
			enrollment.AdmissionPaymentPaidAt = paidAt.Time
		}
		if voidedAt.Valid {
			enrollment.PaymentVoidedAt = voidedAt.Time
		}
		enrollments = append(enrollments, enrollment)
	}
	return enrollments, rows.Err()
}

func (a *App) listStudentAttendanceHistoryPublic(admissionID int64) ([]StudentAttendanceHistoryRow, error) {
	if admissionID <= 0 {
		return nil, sql.ErrNoRows
	}

	rows, err := a.queryDB(`
		SELECT
			ar.id,
			ar.admission_id,
			ar.attendance_date,
			ar.status,
			COALESCE(ar.note, ''),
			ar.group_id,
			COALESCE(sg.name, ''),
			COALESCE(ar.session_id, 0),
			COALESCE(sgs.title, ''),
			COALESCE(tp.name, ''),
			COALESCE(d.name, '')
		FROM attendance_records ar
		LEFT JOIN student_groups sg ON sg.id = ar.group_id
		LEFT JOIN student_group_sessions sgs ON sgs.id = ar.session_id
		LEFT JOIN training_programs tp ON tp.id = sg.training_program_id
		LEFT JOIN divisions d ON d.id = tp.division_id
		WHERE ar.admission_id = ?
		ORDER BY ar.attendance_date DESC, COALESCE(sgs.start_time, '') DESC, ar.id DESC
	`, admissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history := make([]StudentAttendanceHistoryRow, 0)
	for rows.Next() {
		var row StudentAttendanceHistoryRow
		if err := rows.Scan(
			&row.ID,
			&row.AdmissionID,
			&row.AttendanceDate,
			&row.Status,
			&row.Note,
			&row.GroupID,
			&row.GroupName,
			&row.SessionID,
			&row.SessionTitle,
			&row.TrainingProgramName,
			&row.DivisionName,
		); err != nil {
			return nil, err
		}
		history = append(history, row)
	}
	return history, rows.Err()
}

func (a *App) listStudentMonthlyPaymentHistoryByAdmissionID(admissionID int64) ([]StudentMonthlyPayment, error) {
	rows, err := a.queryDB(`
		SELECT
			smp.id,
			smp.admission_id,
			COALESCE(smp.enrollment_id, 0),
			COALESCE(tp.name, ''),
			COALESCE(d.name, ''),
			smp.payment_month,
			smp.amount,
			smp.payment_method,
			COALESCE(smp.finance_transaction_id, 0),
			COALESCE(smp.collected_by_user_id, 0),
			COALESCE(u.name, ''),
			COALESCE(smp.voided, 0),
			COALESCE(smp.void_reason, ''),
			smp.voided_at,
			smp.collected_at,
			smp.created_at
		FROM student_monthly_payments smp
		LEFT JOIN student_enrollments se ON se.id = smp.enrollment_id
		LEFT JOIN training_programs tp ON tp.id = se.training_program_id
		LEFT JOIN divisions d ON d.id = tp.division_id
		LEFT JOIN users u ON u.id = smp.collected_by_user_id
		WHERE smp.admission_id = ?
		ORDER BY smp.payment_month DESC, smp.collected_at DESC, smp.id DESC
	`, admissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history := make([]StudentMonthlyPayment, 0)
	for rows.Next() {
		var payment StudentMonthlyPayment
		var voided int
		var voidedAt sql.NullTime
		if err := rows.Scan(
			&payment.ID,
			&payment.AdmissionID,
			&payment.EnrollmentID,
			&payment.TrainingProgramName,
			&payment.DivisionName,
			&payment.PaymentMonth,
			&payment.Amount,
			&payment.PaymentMethod,
			&payment.FinanceTransactionID,
			&payment.CollectedByUserID,
			&payment.CollectedByUserName,
			&voided,
			&payment.VoidReason,
			&voidedAt,
			&payment.CollectedAt,
			&payment.CreatedAt,
		); err != nil {
			return nil, err
		}
		payment.Voided = voided == 1
		if voidedAt.Valid {
			payment.VoidedAt = voidedAt.Time
		}
		history = append(history, payment)
	}
	return history, rows.Err()
}

func studentPaymentRowsForAdmission(rows []StudentPaymentRow, admissionID int64) []StudentPaymentRow {
	filtered := make([]StudentPaymentRow, 0)
	for _, row := range rows {
		if row.Admission.ID == admissionID {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func enrollmentIDsForPublicStudent(enrollments []StudentEnrollment) []int64 {
	ids := make([]int64, 0, len(enrollments))
	for _, enrollment := range enrollments {
		if enrollment.ID > 0 {
			ids = append(ids, enrollment.ID)
		}
	}
	return ids
}

func (a *App) publicStudentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lookup := normalizePublicStudentLookup(r.URL.Query().Get("student_id"))

	data := a.newTemplateData(w, r, nil)
	data.Title = "My Student"
	data.Description = "Search a Mekmaa student by Student ID or scan the QR code to view attendance and payment details."
	data.PublicStudentSearchStudentID = lookup
	data.PaymentMonth = time.Now().Format("2006-01")
	data.PaymentMonthLabel = paymentMonthLabel(data.PaymentMonth)

	if lookup != "" {
		admission, err := a.findAdmissionByPublicLookup(lookup)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				data.PublicStudentSearchNotFound = true
				a.render(w, "mystudent", data, http.StatusOK)
				return
			}
			log.Printf("public student lookup: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		enrollments, err := a.listStudentEnrollmentsByAdmissionID(admission.ID)
		if err != nil {
			log.Printf("public student enrollments: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		for i := range enrollments {
			enrollments[i].Student = *admission
		}

		leaves, err := a.listStudentEnrollmentLeavesByEnrollmentIDs(enrollmentIDsForPublicStudent(enrollments))
		if err != nil {
			log.Printf("public student leaves: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		attendanceHistory, err := a.listStudentAttendanceHistoryPublic(admission.ID)
		if err != nil {
			log.Printf("public student attendance history: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		paymentRows, err := a.listStudentPaymentRows(data.PaymentMonth)
		if err != nil {
			log.Printf("public student monthly snapshot: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		paymentHistory, err := a.listStudentMonthlyPaymentHistoryByAdmissionID(admission.ID)
		if err != nil {
			log.Printf("public student payment history: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		data.SelectedAdmission = admission
		data.Enrollments = enrollments
		data.StudentAttendanceHistory = attendanceHistory
		data.StudentAttendanceSummary = summarizeStudentAttendanceHistory(attendanceHistory)
		data.StudentPaymentRows = studentPaymentRowsForAdmission(paymentRows, admission.ID)
		data.PublicStudentPaymentHistory = paymentHistory

		for _, enrollment := range enrollments {
			if enrollmentLeaves := leaves[enrollment.ID]; len(enrollmentLeaves) > 0 {
				data.EnrollmentLeaves = append(data.EnrollmentLeaves, enrollmentLeaves...)
			}
		}

		for _, row := range data.StudentPaymentRows {
			data.PaymentTotalDue += row.MonthlyFee
			data.PaymentCollected += row.CollectedAmount
			data.PaymentOutstanding += row.OutstandingAmount
			if row.OutstandingAmount > 0.004 {
				data.PaymentPendingCount++
			} else {
				data.PaymentPaidCount++
			}
		}
	}

	a.render(w, "mystudent", data, http.StatusOK)
}
